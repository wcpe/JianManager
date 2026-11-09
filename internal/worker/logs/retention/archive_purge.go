package retention

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// 本文件实现「归档对象自身的回收」（归档目的地/桶内生命周期）。
//
// # 为什么必须有它
//
// 在此之前，`ActionArchiveCopy` 只解决了「热层到期后要不要删」，而**归档目的地只增不减**：
// 每一天的热层数据被导出成一份归档副本，然后再也不会有人回头看那份副本是否还该留着。
// 730d 之后那些归档仍然留着 ⇒ 「保留策略」只对热层生效，对真正长期占盘的那一层失效。
// spec §7 把这条如实登记为「未做：归档对象自身的回收（桶内生命周期）」。
//
// # 三闸复用，不新造
//
// 删归档对象与删热层数据是同一量级的不可逆删除（甚至更彻底：热层删掉还有归档副本，
// 归档删掉就真的没了）。因此这里**不引入**任何新的开关，直接复用
// `Policy.DeleteGatesOpen()`（discard + sweep.vl_sweep + log_vl.delete_enabled），
// 三闸默认全关 ⇒ 默认零删除。新开关会让「几道闸才算够」变成一个看情况的问题，
// 而闸门数量正是这条路径的安全论证。
//
// # 次序不可调换
//
// 与 `MarkArchived → RemoveArchivedPartition → MarkArchiveReclaimed` 完全同构：
//
//	MarkPurgeRequested（落 journal，记「从这一刻起开始删」）
//	→ 逐对象 List + Delete
//	→ 全部成功才 MarkPurged；任一失败 ⇒ RollbackPurge（权威态退回未清除）
//
// 反序（先删后改权威态）会留下一个窗口：对象已经没了而 Catalog 仍说「归档里有」，
// 此时查询面照旧把用户引向 rehydrate——一条必然失败的死路。
//
// # 「目录已不存在」为什么是成功
//
// `List` 返回空（目录被人工清掉、或上一轮已经删净但标记没落成）不是错误：
// 我们的目标是「这份归档不再占盘」，空目录恰好已经是目标状态。把它当失败会让
// 一个已达成目标的分区永久卡在「清除进行中」，每轮重试、每轮报警。
// 反过来，`List` 返回**错误**（读不出来）必须算失败——「读不出来」不等于「里面没东西」。

// ArchiveObjectRetentionPolicy 是归档对象自身回收的策略面。
//
// 保留期从**归档时刻**（ArchiveEvidence.ArchivedAt）起算，而不是从热层回收时刻：
// 运维配 `730d` 的语义是「这份归档副本留两年」，起点必须是它诞生那一刻。
type ArchiveObjectRetentionPolicy struct {
	// Enabled 是否允许按保留期清除归档对象（默认 false）。
	Enabled bool `mapstructure:"enabled"`
	// Retention 归档对象保留期；<=0 表示未启用（即使 Enabled 为真也不动）。
	Retention time.Duration `mapstructure:"retention"`
	// SweepAtHour 每日触发的本地整点（0..23）；越界回退 DefaultSweepAtHour。
	SweepAtHour int `mapstructure:"sweep_at_hour"`
}

// Active 报告本策略是否真的会清除归档对象。
//
// 两个条件的**与**，缺一不可：只开开关而不给保留期 ⇒ 没有「到期」这个概念，
// 删任何一天都是在猜；只给保留期而不开开关 ⇒ 默认关是用户护栏，不可默认打开。
// 配置层已对「开了开关但保留期为空」启动即拒（见 internal/worker 的
// archive_object_retention 校验），这里的判定是运行期最后一道，防止热更绕过。
func (p ArchiveObjectRetentionPolicy) Active() bool {
	return p.Enabled && p.Retention > 0
}

// Normalize 补齐缺省（越界小时数回退默认：配错就按默认时刻跑，而不是半夜 0 点跑）。
func (p ArchiveObjectRetentionPolicy) Normalize() ArchiveObjectRetentionPolicy {
	out := p
	if out.SweepAtHour < 0 || out.SweepAtHour > 23 {
		out.SweepAtHour = DefaultSweepAtHour
	}
	return out
}

// ArchivedObjectRef 是一个已归档分区的归档对象位置（由调用方从 Catalog 读出）。
//
// 为什么这些字段在 retention 一侧镜像一份：依赖方向必须单向——保留策略只认
// 「这份归档对象在哪、留到什么时候、能不能删」，不该依赖分区 Catalog 的内部类型。
// 适配放在装配处（apps/worker），与 Mover / Remover / ArchiveAuthority 同一取舍。
type ArchivedObjectRef struct {
	StorageNamespace string
	// UTCDay 形如 20060102（catalog 的 2006-01-02 也认，解析层两种都接受）。
	UTCDay string
	// ProviderKind 归档目的地类型（local / s3 …）。
	ProviderKind string
	// ObjectPrefix 归档对象前缀，形如 namespace/day/g{generation}。
	ObjectPrefix string
	// ArchivedAt 该归档副本的诞生时刻（保留期起点）。
	ArchivedAt time.Time
	// HotReclaimed 热层副本是否已回收。
	HotReclaimed bool
	// Purged 归档对象是否已清除（终态）。
	Purged bool
	// RecoveryRequired 是否有待恢复标记（此时归档是唯一可用副本）。
	RecoveryRequired bool
	// BlockedReason 非空表示 Catalog 侧复核拒绝清除，此处只做透传与审计。
	BlockedReason string
}

// String 返回可读描述（日志与观测面用）。
func (r ArchivedObjectRef) String() string {
	return fmt.Sprintf("ns=%s day=%s archive=%s/%s archivedAt=%s",
		r.StorageNamespace, r.UTCDay, r.ProviderKind, r.ObjectPrefix,
		r.ArchivedAt.UTC().Format(time.RFC3339))
}

// ArchivePurgeTarget 是一个待判定/待执行的归档对象清除目标。
type ArchivePurgeTarget struct {
	Ref ArchivedObjectRef
	// Retention 本目标采用的有效保留期。
	Retention time.Duration
	// EligibleAt 按保留期最早可以清除的时刻（= 归档时刻 + 保留期）。
	EligibleAt time.Time
	// Due 本轮是否应当动手。
	Due bool
	// Reason 不可动手/不动手的原因（审计与观测面用）。
	Reason string
}

// String 返回可读描述。
func (t ArchivePurgeTarget) String() string {
	return fmt.Sprintf("%s retention=%s eligibleAt=%s due=%v reason=%q",
		t.Ref, humanTTL(t.Retention), t.EligibleAt.UTC().Format(time.RFC3339), t.Due, t.Reason)
}

// PlanArchiveObjectPurge 计算一轮归档对象清除的判定结果（纯函数）。
//
// 判定次序（与「删前归档」同构，**先判能不能删，再判该不该删**）：
//
//  1. 已清除（Purged）⇒ 不动手（幂等重跑不重复计数）；
//  2. 自带阻塞条件的候选（热层未回收 / 待恢复标记）⇒ 不动手——**这里不依赖调用方**
//     把这些条件折算成 BlockedReason：判据本身在纯函数里重述一遍，两个来源任一
//     认为不安全就不动手。只留一个来源时，任何调用方漏填都会静默变成「删了唯一一份副本」；
//  3. 保留期未到 / 缺归档时刻 ⇒ 不动手；
//  4. 否则 Due=true。
//
// 注意**不看**配置开关与三闸：那是执行器与调用方的判据。把它们塞进纯函数会让
// 「到期了但闸门没开」与「压根没到期」在计划层面无法区分，而运维最需要知道的
// 恰恰是「有多少天在等闸门」。
func (p Policy) PlanArchiveObjectPurge(now time.Time, refs []ArchivedObjectRef) []ArchivePurgeTarget {
	ret := p.ArchiveObjectRetention.Normalize()
	out := make([]ArchivePurgeTarget, 0, len(refs))
	for _, ref := range refs {
		t := ArchivePurgeTarget{Ref: ref, Retention: ret.Retention}
		if ret.Retention > 0 {
			t.EligibleAt = ref.ArchivedAt.Add(ret.Retention)
		}
		switch {
		case ref.Purged:
			t.Reason = "归档对象已清除"
		case ref.BlockedReason != "":
			// 调用方（Catalog 适配层）已复核并给出原因：原样透传，不改写措辞——
			// 观测面与拒绝路径必须说同一句话，否则现场会去查两个不同的东西。
			t.Reason = ref.BlockedReason
		case !ref.HotReclaimed:
			// 热层仍在：此刻归档是「多存一份」的第二份。删掉它再让热层回收跑一轮，
			// 最后一份就没了——而那天本不该消失。
			t.Reason = "热层副本尚未回收，清除归档对象会先于热层回收丢掉一份副本"
		case ref.RecoveryRequired:
			// 恢复失败还挂着：归档是唯一可用副本，清除等于把「还能救」判成「救不回来」。
			t.Reason = "分区存在待恢复标记，归档对象仍是唯一可用副本"
		case ref.ArchivedAt.IsZero():
			// 没有归档时刻就无从计算保留期：不猜（猜短了会提前删掉仍有义务保留的副本）。
			t.Reason = "缺少归档时刻，无法计算保留期"
		case ret.Retention <= 0:
			t.Reason = "未配置归档对象保留期"
		case now.Before(t.EligibleAt):
			t.Reason = "未到保留期"
		default:
			t.Due = true
			t.Reason = "已过归档对象保留期"
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ref.StorageNamespace != out[j].Ref.StorageNamespace {
			return out[i].Ref.StorageNamespace < out[j].Ref.StorageNamespace
		}
		return out[i].Ref.UTCDay < out[j].Ref.UTCDay
	})
	return out
}

// ArchivedObjectLister 提供候选的已归档分区（生产由 catalog 适配）。
type ArchivedObjectLister interface {
	ListArchivedObjects() ([]ArchivedObjectRef, error)
}

// ArchivedObjectListerFunc 函数适配。
type ArchivedObjectListerFunc func() ([]ArchivedObjectRef, error)

// ListArchivedObjects 实现 ArchivedObjectLister。
func (f ArchivedObjectListerFunc) ListArchivedObjects() ([]ArchivedObjectRef, error) { return f() }

// ArchiveObjectStore 是归档目的地里「对象」这一层的删除出口。
//
// 只暴露对象层（List/Delete）与空目录清理，不暴露「删整个根」这类能力：
// 归档根由运维配置（`log_retention.archive_copy.root`），平台没有权限也没有理由
// 对整个根动手——错一次就是所有年份的归档一起消失。
type ArchiveObjectStore interface {
	// ListArchiveObjects 列出归档位置下的全部对象（相对路径，已排序）。
	// 目录不存在时返回**空列表 + nil 错误**（这是「已经是目标状态」，不是失败）。
	ListArchiveObjects(ctx context.Context, providerKind, objectPrefix string) ([]string, error)
	// DeleteArchiveObject 删除单个对象；对象已不存在视为成功（幂等，容忍 404）。
	DeleteArchiveObject(ctx context.Context, providerKind, objectPrefix, relPath string) error
	// RemoveArchivePartition 删除已清空的归档分区目录（尽力而为）。
	//
	// 实现方必须删掉**本分区子树**（`<root>/<ns>/<day>/g<gen>`），包括对象删完后
	// 剩下的 `raw/files/` 空骨架——只用 `os.Remove` 会在非空子目录上报 ENOTEMPTY，
	// 于是每个分区都留一个骨架、每轮告警。同时**不得**越过该子树：root 与 day 目录
	// 可能还有别的 generation / 别的日期，删它们等于把整个归档史删掉。
	//
	// 它删的只是**已经不装数据的目录**：失败不改变「对象已清除」这个事实，
	// 因此调用方只记录告警、不把整轮判失败（见 execute 的说明）。
	RemoveArchivePartition(ctx context.Context, providerKind, objectPrefix string) error
}

// ArchiveObjectAuthority 是「归档对象已清除」权威态的迁移出口（生产由 catalog 适配）。
//
// 三个方法对应不可调换的次序；实现方必须保证：
//   - MarkPurgeRequested 幂等：已清除的分区重跑不追加 journal；
//   - MarkPurged 只在**所有对象都删成功**之后调用；
//   - RollbackPurge 让「已清除」保持为假（删了一半不许宣称清除）。
type ArchiveObjectAuthority interface {
	MarkPurgeRequested(ctx context.Context, storageNamespace, utcDay string, reason string) (already bool, err error)
	MarkPurged(ctx context.Context, storageNamespace, utcDay string, reason string) error
	RollbackPurge(ctx context.Context, storageNamespace, utcDay string, reason string) error
}

// 归档对象清除的结局分类（观测面）。
const (
	// ArchiveObjectPurged 「已清除」：对象全删成功且权威态已落盘。
	ArchiveObjectPurged = "purged"
	// ArchiveObjectPurgeKept 「保留」：未到保留期 / 有阻塞原因 / 闸门未开。
	ArchiveObjectPurgeKept = "kept"
	// ArchiveObjectPurgeRolledBack 「已回滚」：删到一半失败，权威态退回未清除。
	ArchiveObjectPurgeRolledBack = "rolled_back"
	// ArchiveObjectPurgeFailed 「失败」：根本没能开始删（权威态迁移失败、列不出对象）。
	ArchiveObjectPurgeFailed = "failed"
)

// ArchivePurgeOutcome 是单个分区的归档对象清除结果。
type ArchivePurgeOutcome struct {
	Target ArchivePurgeTarget
	// Result 见 ArchiveObject* 常量。
	Result string
	// Objects 本轮删除的对象数。
	Objects int
	// Err 非空表示失败或回滚的原因。
	Err error
	At  time.Time
}

// ArchivePurgeStats 是归档对象清除的累计读数。
type ArchivePurgeStats struct {
	// Planned 累计进入判定的分区数（含未到期的）。
	Planned int64
	// Purged 累计真的清除了归档对象的分区数。
	Purged int64
	// Failed 累计失败的分区数（含回滚；两者是不同事件，见 RolledBack）。
	Failed int64
	// RolledBack 累计「删到一半失败、权威态已回退」的分区数。
	RolledBack int64
	// Kept 累计「保留」的分区数（未到期 / 有阻塞原因 / 闸门未开）。
	Kept int64
	// LastError 最近一次失败原因。
	LastError string
	// LastRunAt 最近一轮执行时刻（零值 = 从未运行）。
	LastRunAt time.Time
}

// ArchivePurgeResult 是一轮归档对象清除的摘要（日志与观测面）。
type ArchivePurgeResult struct {
	At time.Time
	// Listed 本轮的候选分区数。
	Listed int
	// Due 到期且允许动手的分区数。
	Due    int
	Purged int
	Failed int
	// RolledBack 是 Failed 的子集：失败并且已经回滚（另一种形态是「根本没开始」）。
	RolledBack int
	Kept       int
	// GatesClosed 报告本轮被删除三闸挡住（此时**一个对象都没列、没删**）。
	GatesClosed bool
	// GateReason 未全开时点名缺失的那道闸。
	GateReason string
	Outcomes   []ArchivePurgeOutcome
}

// ArchiveObjectReclaimer 执行「按保留期清除归档对象」。
//
// 与 ArchiveExecutor 分开成两个类型（而不是塞进同一个）：
// 两者的对象、判据、观测读数都不同（一个删热层日分区、一个删归档目的地里的对象），
// 合成一个会让「哪个读数对应哪次删除」在告警里说不清；共用的是同一套三闸判据。
type ArchiveObjectReclaimer struct {
	policy    Policy
	lister    ArchivedObjectLister
	store     ArchiveObjectStore
	authority ArchiveObjectAuthority

	mu    sync.RWMutex
	stats ArchivePurgeStats
}

// NewArchiveObjectReclaimer 创建清除器。store / authority / lister 任一为 nil 时
// `Ready()` 为假，Step 会如实返回「未装配」而不是静默跳过。
func NewArchiveObjectReclaimer(policy Policy, lister ArchivedObjectLister, store ArchiveObjectStore, authority ArchiveObjectAuthority) *ArchiveObjectReclaimer {
	return &ArchiveObjectReclaimer{policy: policy, lister: lister, store: store, authority: authority}
}

// SetPolicy 替换策略。
func (r *ArchiveObjectReclaimer) SetPolicy(p Policy) {
	if r == nil {
		return
	}
	r.policy = p
}

// Ready 报告三个出口是否齐备，第二个返回值点名缺哪个（启动日志用）。
//
// 组装不全会造成「配置说已启用、实际什么都没删」：那是最难查的一类现场
// （没有任何报错，只有一个永远不动的计数器）。故这里要求显式点名。
func (r *ArchiveObjectReclaimer) Ready() (bool, string) {
	if r == nil {
		return false, "归档对象清除器未装配"
	}
	if r.lister == nil {
		return false, "未装配已归档分区枚举出口"
	}
	if r.store == nil {
		return false, "未装配归档对象存储出口（无法列出/删除归档目的地里的对象）"
	}
	if r.authority == nil {
		return false, "未装配归档对象权威态出口（无权威态就不得删）"
	}
	return true, ""
}

// Stats 返回累计读数副本。
func (r *ArchiveObjectReclaimer) Stats() ArchivePurgeStats {
	if r == nil {
		return ArchivePurgeStats{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stats
}

// ArchivePurgeSnapshot 实现 /metrics 需要的读数口径。
//
// 与 ArchiveStats 同口径：LastRunAt 零值表示「从未运行」，抓取侧据此区分
// 「没跑过」与「跑过但零清除」。
func (r *ArchiveObjectReclaimer) ArchivePurgeSnapshot() (lastRun time.Time, purged, failed, rolledBack int64) {
	if r == nil {
		return time.Time{}, 0, 0, 0
	}
	st := r.Stats()
	return st.LastRunAt, st.Purged, st.Failed, st.RolledBack
}

func (r *ArchiveObjectReclaimer) mergeStats(round ArchivePurgeStats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.Planned += round.Planned
	r.stats.Purged += round.Purged
	r.stats.Failed += round.Failed
	r.stats.RolledBack += round.RolledBack
	r.stats.Kept += round.Kept
	if !round.LastRunAt.IsZero() {
		r.stats.LastRunAt = round.LastRunAt
	}
	if round.LastError != "" {
		r.stats.LastError = round.LastError
	}
}

// Step 执行一轮：判闸 → 列候选 → 算计划 → 逐分区「先落权威态、再删对象」。
//
// 错误口径：列不出候选分区要如实返回错误（「没有任何归档分区」与「读不出来」
// 在观测面上是两件完全不同的事）；单个分区失败不影响其它分区。
func (r *ArchiveObjectReclaimer) Step(ctx context.Context, now time.Time) (ArchivePurgeResult, error) {
	res := ArchivePurgeResult{At: now}
	if r == nil || !r.policy.Enabled || !r.policy.ArchiveObjectRetention.Active() {
		return res, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// **三闸先判，且判在列举之前**：闸门没开时连枚举都不做。
	// 这样「未全开」的现场在读数上表现为「零行动」，而不是「枚举了一堆然后全部保留」——
	// 后者会让运维误以为「策略在跑、只是没删到期的」，从而去查保留期配置而不是闸门。
	if open, reason := r.policy.DeleteGatesOpen(); !open {
		res.GatesClosed = true
		res.GateReason = reason
		round := ArchivePurgeStats{LastRunAt: now}
		r.mergeStats(round)
		return res, nil
	}
	if r.lister == nil {
		return res, fmt.Errorf("retention: 归档对象清除未装配已归档分区枚举出口")
	}
	all, err := r.lister.ListArchivedObjects()
	if err != nil {
		return res, fmt.Errorf("retention: 枚举已归档分区失败：%w", err)
	}
	res.Listed = len(all)

	plan := r.policy.PlanArchiveObjectPurge(now, all)
	round := ArchivePurgeStats{Planned: int64(len(plan)), LastRunAt: now}
	defer func() { r.mergeStats(round) }()

	for _, t := range plan {
		outcome := ArchivePurgeOutcome{Target: t, At: now}
		if !t.Due {
			outcome.Result = ArchiveObjectPurgeKept
			round.Kept++
			res.Kept++
			res.Outcomes = append(res.Outcomes, outcome)
			continue
		}
		res.Due++
		r.execute(ctx, t, &outcome, &round)
		switch outcome.Result {
		case ArchiveObjectPurged:
			res.Purged++
		case ArchiveObjectPurgeRolledBack:
			res.RolledBack++
			res.Failed++
		case ArchiveObjectPurgeFailed:
			res.Failed++
		}
		res.Outcomes = append(res.Outcomes, outcome)
	}
	return res, nil
}

// execute 执行单个分区的归档对象清除。
//
// 单独成方法而不是在 Step 里摊开：这五步每一步失败都必须落到**同一个**诚实结局，
// 摊开写最容易漏掉某个 break——漏一次就是「删了一半却记了已清除」。
//
// 判定次序（不可调换）：
//  1. 出口未装配 ⇒ 失败（不假装成功）；
//  2. `MarkPurgeRequested` 落 journal（先改权威态）；失败 ⇒ 失败，**零删除**；
//  3. `List` 取对象清单；错误 ⇒ **回滚**，零删除（「读不出来」≠「里面没东西」）；
//  4. 清单为空 ⇒ 幂等成功（目录本就不存在/已删净），直接标记完成；
//  5. 逐对象 Delete；任一失败 ⇒ **回滚**（已删掉的那些无法恢复，但绝不许宣称已清除）；
//  6. 全部删成功 ⇒ `MarkPurged`（终态）；标记失败要如实上报
//     （对象确实没了，但权威态没落盘 ⇒ 下一轮会重试，重复删是幂等的）。
func (r *ArchiveObjectReclaimer) execute(ctx context.Context, t ArchivePurgeTarget, outcome *ArchivePurgeOutcome, round *ArchivePurgeStats) {
	ref := t.Ref
	if r.store == nil || r.authority == nil {
		outcome.Result = ArchiveObjectPurgeFailed
		outcome.Err = fmt.Errorf("归档对象清除出口未装配完整")
		round.Failed++
		round.LastError = fmt.Sprintf("%s 归档对象清除出口未装配，未删除任何对象", ref)
		return
	}
	if err := ctx.Err(); err != nil {
		outcome.Result = ArchiveObjectPurgeFailed
		outcome.Err = err
		round.Failed++
		round.LastError = fmt.Sprintf("%s 归档对象清除被中止：%v", ref, err)
		return
	}

	// 第 2 步：先改权威态。已经清除过的分区在这里拿到 already=true（幂等）。
	already, err := r.authority.MarkPurgeRequested(ctx, ref.StorageNamespace, ref.UTCDay, t.Reason)
	if err != nil {
		outcome.Result = ArchiveObjectPurgeFailed
		outcome.Err = fmt.Errorf("归档对象清除请求未能落盘（未删除任何对象）：%w", err)
		round.Failed++
		round.LastError = fmt.Sprintf("%s 归档对象清除请求落盘失败，未删除任何对象：%v", ref, err)
		return
	}
	if already {
		// 记录上写着「已清除」：**一个对象都不许删**，也不需要再删。
		// 这条分支防的是「Catalog 已清除但归档目录被人工恢复」——那种情况下
		// 平台必须尊重权威记录，人工处置要走人工登记（重新归档）。
		outcome.Result = ArchiveObjectPurgeKept
		outcome.Target.Reason = "权威记录显示归档对象已清除"
		round.Kept++
		return
	}

	// rollback 统一收口：把一个失败落成「回滚成功」或「回滚也失败」两种如实结局。
	//
	// 为什么要收口成一个 helper：三条失败路径（列清单失败、上下文中止、单对象删除失败）
	// 都必须走同一段计数与措辞。散着写时最容易出现的偏差是「某条路径忘了 ++ 计数」
	// ——那时告警数会比真实失败数小，而失败恰恰是最需要被看见的那一类事件。
	rollback := func(cause error, detail string) {
		rbErr := r.authority.RollbackPurge(ctx, ref.StorageNamespace, ref.UTCDay, detail)
		round.Failed++
		if rbErr == nil {
			round.RolledBack++
			outcome.Result = ArchiveObjectPurgeRolledBack
			outcome.Err = cause
			round.LastError = fmt.Sprintf("%s %s，权威态已回退：%v", ref, detail, cause)
			return
		}
		// 回滚也失败：权威态停在「清除进行中」，归档里可能还剩一部分对象。
		// 这是必须人工处置的形态——两个错误都要报出来，只报一个会让现场少一半信息。
		outcome.Result = ArchiveObjectPurgeFailed
		outcome.Err = fmt.Errorf("%w；且权威态回滚失败：%v（该分区停留为「清除进行中」，需人工核对归档目录残留）", cause, rbErr)
		round.LastError = fmt.Sprintf("%s %s，且权威态回滚失败：%v / %v", ref, detail, cause, rbErr)
	}

	// 第 3 步：取清单。
	objects, err := r.store.ListArchiveObjects(ctx, ref.ProviderKind, ref.ObjectPrefix)
	if err != nil {
		rollback(fmt.Errorf("列出归档对象失败（未删除任何对象）：%w", err), "列出归档对象失败："+err.Error())
		return
	}

	// 第 4/5 步：逐对象删除。空清单直接走成功（幂等）。
	for _, rel := range objects {
		if err := ctx.Err(); err != nil {
			rollback(fmt.Errorf("清除被中止（已删 %d 个对象）：%w", outcome.Objects, err), "清除被中止："+err.Error())
			return
		}
		if derr := r.store.DeleteArchiveObject(ctx, ref.ProviderKind, ref.ObjectPrefix, rel); derr != nil {
			rollback(
				fmt.Errorf("删除归档对象 %s 失败（已删 %d 个）：%w", rel, outcome.Objects, derr),
				"删除归档对象失败："+derr.Error(),
			)
			return
		}
		outcome.Objects++
	}

	// 第 6 步：全部删成功 ⇒ 落终态。
	if err := r.authority.MarkPurged(ctx, ref.StorageNamespace, ref.UTCDay, t.Reason); err != nil {
		// 对象确实已经删掉（数据面已达成目标），但权威态没落盘：
		// 下一轮会重试（重复删除是幂等的），但**本轮绝不能记成「已清除」**——
		// 那会让观测面报出一个 Catalog 并不承认的终态。
		outcome.Result = ArchiveObjectPurgeFailed
		outcome.Err = fmt.Errorf("归档对象已删除但清除标记未落盘（下一轮重试，需人工核对）：%w", err)
		round.Failed++
		round.LastError = fmt.Sprintf("%s 归档对象已删除但清除标记落盘失败：%v", ref, err)
		return
	}

	// 空目录清理是尽力而为：它删的只是不装数据的目录，失败不影响「已清除」这个事实。
	if err := r.store.RemoveArchivePartition(ctx, ref.ProviderKind, ref.ObjectPrefix); err != nil {
		slog.Warn("归档分区空目录清理失败（对象已清除，仅残留空目录）",
			"namespace", ref.StorageNamespace, "day", ref.UTCDay,
			"archive", ref.ProviderKind+"/"+ref.ObjectPrefix, "error", err)
	}
	outcome.Result = ArchiveObjectPurged
	round.Purged++
}

// ArchivePurgeDriver 按调度口径反复执行 Step。
//
// 与 retention.Driver 同构（首轮立即执行 + 之后每日固定时刻），理由也相同：
// 停机期间攒下的「已过保留期的归档」最需要在启动时处理，而归档对象的枚举成本
// 由候选数决定（只对已达四个前置条件的分区做 List），不在热路径上。
//
// 与 eventstore 的段回收刻意不同（它不在启动时跑）：那里扫的是**平台自己的全部暂存段**，
// 重启风暴下每次启动全量扫段是纯 IO 浪费；这里只对「已归档 + 热层已回收 + 已过期 +
// 三闸全开」的分区动手，本来就是该做的事。
type ArchivePurgeDriver struct {
	reclaimer   *ArchiveObjectReclaimer
	sweepAtHour int
}

// NewArchivePurgeDriver 创建调度器。
func NewArchivePurgeDriver(r *ArchiveObjectReclaimer) *ArchivePurgeDriver {
	hour := DefaultSweepAtHour
	if r != nil {
		hour = r.policy.ArchiveObjectRetention.Normalize().SweepAtHour
	}
	return &ArchivePurgeDriver{reclaimer: r, sweepAtHour: hour}
}

// SweepAtHour 返回每日触发时刻（本地整点）。
func (d *ArchivePurgeDriver) SweepAtHour() int {
	if d == nil {
		return DefaultSweepAtHour
	}
	return d.sweepAtHour
}

// DescribeSchedule 返回调度口径的一句话说明（启动日志用）。
func (d *ArchivePurgeDriver) DescribeSchedule() string {
	if d == nil || d.reclaimer == nil {
		return "未装配"
	}
	return fmt.Sprintf("每天 %02d:00（进程本地时区 %s）执行一轮", d.SweepAtHour(), localZoneLabel())
}

// Run 反复执行 Step 直到 ctx 结束（首轮立即执行，之后每日固定时刻）。
func (d *ArchivePurgeDriver) Run(ctx context.Context) {
	if d == nil || d.reclaimer == nil {
		return
	}
	if !d.reclaimer.policy.Enabled || !d.reclaimer.policy.ArchiveObjectRetention.Active() {
		return
	}
	for {
		res, err := d.reclaimer.Step(ctx, time.Now())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("归档对象清除本轮失败（下轮重试）", "error", err)
		} else if res.Listed > 0 || res.GatesClosed {
			slog.Info("归档对象清除完成一轮",
				"listed", res.Listed, "due", res.Due, "purged", res.Purged,
				"failed", res.Failed, "rolledBack", res.RolledBack, "kept", res.Kept,
				"gatesClosed", res.GatesClosed, "gateReason", res.GateReason)
		}
		timer := time.NewTimer(nextSweepAt(time.Now(), d.SweepAtHour()).Sub(time.Now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
