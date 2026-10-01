package retention

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// 本文件实现**硬规则「删前归档」（archive-before-delete）**。
//
// 规则原文（用户决策，默认开启）：
//
//	保留期到期的数据**绝不裸删**，只能三选一，且①为默认：
//	  ① 首选：搬运到 COLD 层（复用既有 ROUTING_FROZEN→…→CLEANED 状态机）——
//	     搬运成功 + 校验通过，才允许 HOT 侧回收；搬运失败 = 保留原物 + 告警；
//	  ② 次选：导出/归档为独立副本（段归档/对象存储）；
//	  ③ 仅当配置里**显式** discard: true 才允许直接删。
//
// 为什么这条规则是硬的而不是「可调偏好」：VL 的删除**没有回收站**，
// 而保留期配错（少写一个 0、级别名拼错、把 90d 写成 90h）在现场只表现为
// 「日志不见了」，不会有任何报错。把默认动作定成「搬运」（数据仍在、仍可查）
// 而不是「删除」，使得**所有配置错误的最坏后果从「数据永久丢失」降级为「盘没省下来」**。
//
// 与采样侧的同构性：采样折叠也要由汇总事件承接区间、绝不凭空丢；保留期到期也要
// 先有另一份副本、绝不裸删。两处的共同原则是——**任何自动化的「减法」都必须留下可查的承接物**。

// 保留期到期后的动作。
const (
	// ActionMoveToCold 搬运到冷层（默认动作）。搬运成功且校验通过后才允许 HOT 侧回收。
	ActionMoveToCold = "move_to_cold"
	// ActionArchiveCopy 导出为独立副本（次选；接口已留，实现见「未做」栏）。
	ActionArchiveCopy = "archive_copy"
	// ActionDiscard 直接删除。**仅在显式 discard: true 时可达**。
	ActionDiscard = "discard"
	// ActionBlocked 无归档路径可用且未显式允许删除 ⇒ 保留原物 + 告警。
	//
	// 这是「宁占盘不丢数据」的落点：宁可保留期到了还占着盘，也不在没有承接物的情况下删。
	// 运维在观测面看到 blocked 的目标数与原因，再决定是配 COLD 路径还是显式放弃。
	ActionBlocked = "blocked"
)

// Mover 是冷层搬运接口，由既有的 lifecycle 状态机实现（本包不重写搬运）。
//
// 只暴露「把某个日分区搬到冷层」这一个动作：状态机内部的
// 冻结路由→排空→快照→staging 校验→原子切 owner→租约排空→detach→清理，
// 以及 last-copy 保护（下一层未完成责任接收和校验前不删除最后有效副本），
// 都是既有实现的责任，保留策略不该也不能绕过它。
type Mover interface {
	// MoveToCold 把 (存储命名空间, UTC 日) 分区搬到冷层。
	// 返回目标目录 ID 供审计；失败时返回错误——调用方**必须保留原物**。
	MoveToCold(ctx context.Context, storageNamespace, utcDay string) (string, error)
}

// ArchivePlanTarget 是一个待归档的日分区。
type ArchivePlanTarget struct {
	StorageNamespace string
	UTCDay           string
	// Action 本次要采取的动作（默认 ActionMoveToCold）。
	Action string
	// HotRetention 该分区在热层的有效保留期（= 其中保留期最长的级别那一档）。
	HotRetention time.Duration
	// EligibleAt 该分区最早可以离开热层的时刻。
	EligibleAt time.Time
	// Reason 为 ActionBlocked 时说明为什么不能动手。
	Reason string
}

// String 返回可读描述（日志与观测面用）。
func (t ArchivePlanTarget) String() string {
	return fmt.Sprintf("ns=%s day=%s action=%s hot=%s eligibleAt=%s",
		t.StorageNamespace, t.UTCDay, t.Action, humanTTL(t.HotRetention), t.EligibleAt.UTC().Format(time.RFC3339))
}

// MaxHotRetention 返回各级别保留期的最大值。
//
// 为什么取**最长**那一档作为整个日分区的热层保留期：
// 日分区里混着所有级别，而搬运的最小粒度就是「一天」（catalog 的 PartitionKey 是
// (namespace, utcDay)）。若按最短档搬走，保留期最长的那一级（通常是 ERROR 90d）
// 会被提前请出热层——它在热层「可快查」的承诺就失效了。
// 按最长档搬的代价是 DEBUG 级会跟着多留（成本没省到头），但**承诺不失守**。
//
// 这正是「按级别设保留期」与「按天搬运」两种粒度之间的真实张力，
// 已在 spec 的「已知边界」里如实登记；要做到真正独立的按级别热层保留，
// 需要把级别分流到不同的存储命名空间（属未做的可选路径）。
func (p Policy) MaxHotRetention() time.Duration {
	n := p.Normalize()
	var max time.Duration
	for _, key := range levelKeys {
		if v := n.ByLevel[key]; v > max {
			max = v
		}
	}
	return max
}

// PlanArchive 计算待归档的日分区计划。
//
// actionFor 的判定顺序**不可调换**，它直接编码了硬规则的「首选/次选/仅当」次序：
//  1. 有搬运路径 ⇒ 搬运（**首选**，数据仍在、仍可查）；
//  2. 否则显式 discard ⇒ 允许直接删（运维的知情选择）；
//  3. 否则 ⇒ blocked（保留原物 + 告警）。
//
// 注意第 1 条排在第 2 条**之前**：即使运维显式写了 discard: true，只要冷层路径可用，
// 默认动作仍是**搬运**而不是删除。`discard` 的作用是解除「没有归档路径时的阻塞」，
// 不是把默认动作从搬运改成删除。反过来写（discard 优先）会让「显式开启过删除」
// 变成一处永久的默认，而硬规则的意图恰恰相反——**能搬就搬，搬不了才谈放弃**。
func (p Policy) PlanArchive(now time.Time, partitions []PartitionRef, moverAvailable bool) []ArchivePlanTarget {
	if !p.Enabled {
		return nil
	}
	hot := p.HotWindow()
	out := make([]ArchivePlanTarget, 0, len(partitions))
	for _, part := range partitions {
		if part.StorageNamespace == "" || part.UTCDay == "" {
			continue
		}
		eligibleAt, ok := part.eligibleAt(hot)
		if !ok {
			// 日期不可解析 ⇒ 不猜、不动手。误搬一个还在写入口上的分区，
			// 代价远大于「这一天多占一会儿盘」。
			continue
		}
		t := ArchivePlanTarget{
			StorageNamespace: part.StorageNamespace,
			UTCDay:           part.UTCDay,
			HotRetention:     hot,
			EligibleAt:       eligibleAt,
		}
		switch {
		case moverAvailable:
			t.Action = ActionMoveToCold
		case p.Discard:
			t.Action = ActionDiscard
		default:
			t.Action = ActionBlocked
			t.Reason = "未配置冷层搬运路径且未显式允许直接删除；按「删前归档」保留原物"
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StorageNamespace != out[j].StorageNamespace {
			return out[i].StorageNamespace < out[j].StorageNamespace
		}
		return out[i].UTCDay < out[j].UTCDay
	})
	return out
}

// HotWindow 返回热层保留窗口。显式配置优先，否则按级别 TTL 取最长档。
func (p Policy) HotWindow() time.Duration {
	if p.HotRetention > 0 {
		return p.HotRetention
	}
	return p.MaxHotRetention()
}

// PartitionRef 是一个候选日分区（由调用方从 catalog 读出，不在此重复建模）。
type PartitionRef struct {
	StorageNamespace string
	// UTCDay 形如 20060102。
	UTCDay string
	// Owner 当前拥有者（hot/cold/…）。仅用于观测，不参与判定。
	Owner string
}

// eligibleAt 返回该分区最早可离开热层的时刻。
//
// 语义：分区覆盖的是**一整天**，故以当天结束（次日 00:00 UTC）为起点加保留期——
// 用当天开始会让最后几小时的数据少留一天。
func (part PartitionRef) eligibleAt(hot time.Duration) (time.Time, bool) {
	day, err := time.ParseInLocation("20060102", part.UTCDay, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return day.Add(24 * time.Hour).Add(hot), true
}

// Due 报告该目标是否已经到点。
func (t ArchivePlanTarget) Due(now time.Time) bool {
	return !now.Before(t.EligibleAt)
}

// 归档执行结果的分类。
const (
	// ArchiveMoved 搬运成功（含校验通过）。
	ArchiveMoved = "moved"
	// ArchiveKeptOriginal 搬运失败或无路径 ⇒ 保留原物（绝不回收、绝不删除）。
	ArchiveKeptOriginal = "kept_original"
	// ArchiveDiscarded 显式知情下的直接删除。
	ArchiveDiscarded = "discarded"
	// ArchiveNotDue 尚未到点。
	ArchiveNotDue = "not_due"
)

// ArchiveOutcome 是单个分区的执行结果（G10 观测面的「谁、何时、多少、去哪」）。
type ArchiveOutcome struct {
	Target ArchivePlanTarget
	// Result 见 Archive* 常量。
	Result string
	// TargetDirID 搬运成功后的目标目录 ID（审计「去哪」）。
	TargetDirID string
	// Err 非空表示搬运失败。
	Err error
	// At 动作发生时刻。
	At time.Time
}

// ArchiveExecutor 驱动「删前归档」。
//
// 关键不变量（硬规则的可执行形态）：
//   - 搬运失败 ⇒ 结果必为 ArchiveKeptOriginal，且**绝不**触达任何删除路径；
//   - 未显式 discard ⇒ 结果**永不**为 ArchiveDiscarded；
//   - 无搬运路径 ⇒ ArchiveKeptOriginal + 告警计数，而不是删。
type ArchiveExecutor struct {
	policy Policy
	mover  Mover
	stats  ArchiveStats
}

// ArchiveStats 是归档的累计读数（G10 观测面）。
type ArchiveStats struct {
	// Planned 累计计划目标数。
	Planned int64
	// Moved 累计搬运成功数。
	Moved int64
	// KeptOriginal 累计「保留原物」数（搬运失败或无路径）。
	KeptOriginal int64
	// Discarded 累计显式删除数。
	Discarded int64
	// NotDue 累计未到点数。
	NotDue int64
	// LastError 最近一次失败原因。
	LastError string
	// LastRunAt 最近一次执行时刻。
	LastRunAt time.Time
}

// NewArchiveExecutor 创建执行器。mover 为 nil 时所有目标都落到「保留原物 + 告警」。
func NewArchiveExecutor(policy Policy, mover Mover) *ArchiveExecutor {
	return &ArchiveExecutor{policy: policy, mover: mover}
}

// SetPolicy 替换策略。
func (e *ArchiveExecutor) SetPolicy(p Policy) {
	if e == nil {
		return
	}
	e.policy = p
}

// Stats 返回累计读数副本。
func (e *ArchiveExecutor) Stats() ArchiveStats {
	if e == nil {
		return ArchiveStats{}
	}
	return e.stats
}

// Execute 执行一轮归档。
//
// 错误隔离同 Sweeper：单个分区失败不影响其余分区，失败只记录并保留原物。
func (e *ArchiveExecutor) Execute(ctx context.Context, now time.Time, partitions []PartitionRef) []ArchiveOutcome {
	if e == nil || !e.policy.Enabled {
		return nil
	}
	plan := e.policy.PlanArchive(now, partitions, e.mover != nil)
	e.stats.Planned += int64(len(plan))
	e.stats.LastRunAt = now

	out := make([]ArchiveOutcome, 0, len(plan))
	for _, t := range plan {
		outcome := ArchiveOutcome{Target: t, At: now}
		if !t.Due(now) {
			outcome.Result = ArchiveNotDue
			e.stats.NotDue++
			out = append(out, outcome)
			continue
		}
		switch t.Action {
		case ActionDiscard:
			// 只有显式 discard 才走到这里。这里仍**不直接调删除接口**——
			// 交给调用方按已审计的计划执行，使「谁删了什么」有单一出口可查。
			outcome.Result = ArchiveDiscarded
			e.stats.Discarded++
		case ActionMoveToCold:
			dirID, err := e.mover.MoveToCold(ctx, t.StorageNamespace, t.UTCDay)
			if err != nil {
				// 硬规则的核心分支：搬运失败 = 保留原物 + 告警，绝不回收、绝不删除。
				outcome.Result = ArchiveKeptOriginal
				outcome.Err = err
				e.stats.KeptOriginal++
				e.stats.LastError = fmt.Sprintf("%s/%s 搬运失败，已保留原物：%v", t.StorageNamespace, t.UTCDay, err)
			} else {
				outcome.Result = ArchiveMoved
				outcome.TargetDirID = dirID
				e.stats.Moved++
			}
		default:
			// ActionBlocked：没有任何承接物，什么都不做——保留原物。
			outcome.Result = ArchiveKeptOriginal
			outcome.Err = fmt.Errorf("%s", t.Reason)
			e.stats.KeptOriginal++
			e.stats.LastError = fmt.Sprintf("%s/%s 无归档路径：%s", t.StorageNamespace, t.UTCDay, t.Reason)
		}
		out = append(out, outcome)
	}
	return out
}
