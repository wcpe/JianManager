package retention

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
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
	// ActionDiscard 直接删除。**仅在显式 discard: true 且没有搬运路径时可达**；
	// 执行阶段还要求装配了删除出口（Remover），否则落「保留原物」（见 ArchiveExecutor）。
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

// MoverFunc 函数适配（生产用它把既有 lifecycle 状态机接进来）。
type MoverFunc func(ctx context.Context, storageNamespace, utcDay string) (string, error)

// MoveToCold 实现 Mover。
func (f MoverFunc) MoveToCold(ctx context.Context, storageNamespace, utcDay string) (string, error) {
	return f(ctx, storageNamespace, utcDay)
}

// ArchiveExportResult 是一次「导出归档」的结果摘要（G10 观测面的「多少、去哪」）。
//
// 后半段字段（ProviderKind / ObjectPrefix / ManifestChecksum / CoverageComplete）
// 是「迁移到 ARCHIVED 权威态」所需的**证据**：没有它们，回收热层就没有可核验的
// 「正本在哪、校验和是什么、覆盖了什么」。导出侧必须如实填写；缺任一项都会被
// Catalog 侧的证据校验拒绝（宁可不删，不可少一项）。
type ArchiveExportResult struct {
	// ObjectCount 成功导出并**逐个回读校验通过**的对象数。
	ObjectCount int
	// SourceBytes 源侧（冻结快照）字节数合计，压缩前。
	SourceBytes int64
	// StoredBytes 归档侧字节数合计，即 gzip 压缩后的落盘量。
	StoredBytes int64
	// Location 归档位置的可读标识（审计「去哪」；如 `local inst:147/2026-09-01/g1`）。
	Location string
	// ProviderKind 归档目的地类型（如 local / s3）。
	ProviderKind string
	// ObjectPrefix 归档对象前缀（形如 namespace/day/g{generation}）。
	ObjectPrefix string
	// ManifestChecksum 归档 manifest 的**回读**字节 sha256。
	ManifestChecksum string
	// CoverageComplete 报告导出侧是否已穷尽源枚举。
	CoverageComplete bool
}

// ArchiveEvidence 是交给 Catalog 「已归档」权威态的证据（本包不 import catalog）。
//
// 为什么在本包镜像一份：依赖方向必须是单向的——保留策略只认
// 「副本是否可信、能不能删」这两个事实，不该依赖分区 Catalog 的内部类型；
// 适配放在装配处（apps/worker），由它映射成 catalog.ArchiveEvidence。
type ArchiveEvidence struct {
	ProviderKind     string
	ObjectPrefix     string
	ManifestChecksum string
	ObjectCount      int
	SourceBytes      int64
	StoredBytes      int64
	CoverageComplete bool
	At               time.Time
}

// evidenceOf 把导出结果折成归档证据。
func evidenceOf(res ArchiveExportResult, at time.Time) ArchiveEvidence {
	kind := res.ProviderKind
	if kind == "" {
		// 归档位置标识形如 `local ns/day/g1`：首词就是 provider kind。
		// 这里只做诊断兜底，不允许把它当成「有证据」——下游 Validate 仍会逐项校验。
		if i := strings.IndexByte(res.Location, ' '); i > 0 {
			kind = res.Location[:i]
		}
	}
	prefix := res.ObjectPrefix
	if prefix == "" {
		if i := strings.IndexByte(res.Location, ' '); i > 0 && i+1 < len(res.Location) {
			prefix = strings.TrimSpace(res.Location[i+1:])
		}
	}
	return ArchiveEvidence{
		ProviderKind:     kind,
		ObjectPrefix:     prefix,
		ManifestChecksum: res.ManifestChecksum,
		ObjectCount:      res.ObjectCount,
		SourceBytes:      res.SourceBytes,
		StoredBytes:      res.StoredBytes,
		CoverageComplete: res.CoverageComplete,
		At:               at,
	}
}

// ArchiveAuthority 是「已归档」权威态的迁移出口（生产由 catalog 适配实现）。
//
// 为什么删除热层必须经过它，而不是直接调 Remover：
// 删一个仍属 HOT 权威的日分区，会让查询规划器指向一个已不存在的目录
// （数据在查询面上失联）。因此删除前必须先把权威态改成 ARCHIVED 并落 journal；
// 删除失败时再回滚。三条方法对应这个次序，调用次序不可调换。
//
// 实现方（适配层）必须保证：
//   - MarkArchived 幂等：同一归档位置重跑不产生第二条归档记录；
//   - RollbackArchived 在热层已回收后**拒绝**回滚（数据已不在热层，回滚等于伪造）；
//   - MarkHotReclaimed 只在删除真的成功之后调用。
type ArchiveAuthority interface {
	// FreezeForArchive 冻结该物理日的写入接纳，为导出取一致快照提供前置。
	//
	// 为什么由它负责而不是导出器：冻结必须横跨「快照 → 归档标记 → 删除/回滚」整段。
	// 若导出器临时冻结再自行解冻，就会出现「快照已取、冻结已解、新写入落进热层，
	// 随后归档删热层连新数据一起删掉」的窗口。让权威态出口持有这把闸，
	// 解冻的时机就与「权威态最终有没有变成 ARCHIVED」严格同步。
	FreezeForArchive(ctx context.Context, storageNamespace, utcDay string) error
	// MarkArchived 把分区切换到「正本在归档」的权威态；失败即不得删除。
	MarkArchived(ctx context.Context, storageNamespace, utcDay string, ev ArchiveEvidence) error
	// RollbackArchived 把权威态退回热层（回收失败时使用）。
	RollbackArchived(ctx context.Context, storageNamespace, utcDay string, reason string) error
	// MarkHotReclaimed 记录热层已回收（删除成功后的审计闭环）。
	MarkHotReclaimed(ctx context.Context, storageNamespace, utcDay string) error
	// ReleaseArchiveFreeze 解除归档冻结（导出完成但最终**没有**归档时使用）。
	//
	// 不解除会怎样：该物理日会永久拒绝写入（迟到事件全部失败），
	// 而运维只看到「日志写不进去」，完全看不出这是归档流程留下的闸。
	ReleaseArchiveFreeze(ctx context.Context, storageNamespace, utcDay string, reason string) error
}

// ArchiveExporter 是「导出为独立副本」的出口（硬规则的**次选**路径）。
//
// 为什么本包只声明接口而不实现（与 Mover/Remover 同一取舍）：
//   - 导出要读**冻结快照**、要 gzip、要写 manifest 并回读校验，这些能力属于归档侧
//     （`internal/worker/logs/archive` 的 Provider 契约 + `lifecycle` 的冻结快照），
//     而 `retention` 与 `archive` 之间**刻意互不依赖**（见包注释）；在本包内实现会让
//     保留策略连带依赖对象存储与 manifest 契约。
//   - 执行器只需要知道「导出成功/失败」这一个事实，才能用假实现做出
//     「导出失败一律保留原物」的可转红回归。
//
// 实现方**必须**在返回 nil 错误前完成「逐文件回读 + sha256 比对」：本包把「校验通过」
// 当作回收热层的**唯一**前置证明，导出侧若只写不验，回收就会建立在未经验证的副本上。
type ArchiveExporter interface {
	// ExportPartition 把 (存储命名空间, UTC 日) 分区的冻结快照导出为独立归档副本。
	//
	// 实现方必须自行完成冻结保护（复用 catalog 的排空双闸 + 快照创建），
	// 不得直接读取仍在写入的 HOT 目录。失败时必须返回错误——调用方**保留原物**。
	ExportPartition(ctx context.Context, storageNamespace, utcDay string) (ArchiveExportResult, error)
}

// ArchiveExporterFunc 函数适配（测试与轻量装配用）。
type ArchiveExporterFunc func(ctx context.Context, storageNamespace, utcDay string) (ArchiveExportResult, error)

// ExportPartition 实现 ArchiveExporter。
func (f ArchiveExporterFunc) ExportPartition(ctx context.Context, storageNamespace, utcDay string) (ArchiveExportResult, error) {
	return f(ctx, storageNamespace, utcDay)
}

// ArchivePlanTarget 是一个待归档的日分区。
type ArchivePlanTarget struct {
	StorageNamespace string
	UTCDay           string
	// Action 本次要采取的动作（默认 ActionMoveToCold）。
	Action string
	// HotRetention 该分区在热层的有效保留期（= 其中保留期最长的级别那一档）。
	HotRetention time.Duration
	// EligibleAt 按**年龄**最早可以离开热层的时刻。
	EligibleAt time.Time
	// Reason 为 ActionBlocked 时说明为什么不能动手。
	Reason string
	// Due 本轮是否应当动手。
	//
	// 它是**计划阶段定下的结论**而不是现场重算：触发口径有两路（年龄 + 磁盘水位取先到），
	// 让执行阶段再去重算一遍，等于把「为什么现在搬」的判断分散到两处，
	// 现场就再也说不清「这一轮到底是因为到点还是因为盘紧」。
	Due bool
	// TriggerReason 触发原因（TriggerAge / TriggerDisk；未到点为空）。
	TriggerReason string
	// DiskPressure 报告本轮是在磁盘吃紧下放宽了口径。
	DiskPressure bool
}

// OwnerColdName 是「已在冷层」的 owner 取值。
//
// 由调用方把 catalog 的 owner 映射成本值（见 apps/worker 的适配器），
// 本包不 import catalog：保留策略只该认「这个分区在哪一层」这一个事实，
// 不该依赖分区 Catalog 的内部类型。
const OwnerColdName = "cold"

// String 返回可读描述（日志与观测面用）。
func (t ArchivePlanTarget) String() string {
	trigger := t.TriggerReason
	if trigger == "" {
		trigger = "none"
	}
	return fmt.Sprintf("ns=%s day=%s action=%s hot=%s eligibleAt=%s trigger=%s due=%v",
		t.StorageNamespace, t.UTCDay, t.Action, humanTTL(t.HotRetention),
		t.EligibleAt.UTC().Format(time.RFC3339), trigger, t.Due)
}

// ageAt 返回该分区**自当天结束起算**的年龄。
func (t ArchivePlanTarget) ageAt(now time.Time) (time.Duration, bool) {
	end := t.EligibleAt.Add(-t.HotRetention)
	if end.After(now) {
		return 0, true
	}
	return now.Sub(end), true
}

// UTCDayTime 返回「本次归档动作的时刻」（UTC）。
func (t ArchivePlanTarget) UTCDayTime() time.Time {
	return time.Now().UTC()
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
//  2. 否则有归档目的地 ⇒ 导出为独立副本（**次选**，数据仍在、但要回读才能查）；
//  3. 否则显式 discard ⇒ 允许直接删（运维的知情选择）；
//  4. 否则 ⇒ blocked（保留原物 + 告警）。
//
// 注意第 1 条排在第 3 条**之前**：即使运维显式写了 discard: true，只要冷层路径可用，
// 默认动作仍是**搬运**而不是删除。`discard` 的作用是解除「没有归档路径时的阻塞」，
// 不是把默认动作从搬运改成删除。反过来写（discard 优先）会让「显式开启过删除」
// 变成一处永久的默认，而硬规则的意图恰恰相反——**能搬就搬，搬不了才谈放弃**。
//
// 第 2 条同样排在第 3 条之前，理由一致且更强：既然存在一条能留下副本的路，
// 「知情放弃」的前提（没有别的承接物）就不成立了。故 `discard` 只有在
// **既搬不了冷层、也导不出归档**时才可达——这只会让删除更少，不会更多。
//
// 参数 archiverAvailable 报告**计划阶段**是否存在可用的导出出口
// （装配了 ArchiveExporter 且策略开关打开，见 ArchiveExecutor.ArchiverAvailable）。
// 它与 moverAvailable 分开传：两条路径的装配状态互相独立，合成一个布尔会让
// 「冷层断了但归档还通」这种真实状态无法表达。
func (p Policy) PlanArchive(now time.Time, partitions []PartitionRef, moverAvailable, archiverAvailable bool) []ArchivePlanTarget {
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
		case archiverAvailable:
			// 次选：搬不了冷层，但能导出一份独立副本。
			t.Action = ActionArchiveCopy
		case p.Discard:
			t.Action = ActionDiscard
		default:
			t.Action = ActionBlocked
			t.Reason = "未配置冷层搬运路径与归档目的地且未显式允许直接删除；按「删前归档」保留原物"
		}
		// 失败恢复不是新的到期搬运；即便尚未到年龄，也要续跑已有状态机。
		// 没有恢复入口则保留原物，绝不能把续跑降级成 discard。
		if part.Retryable {
			t.Due = true
			t.TriggerReason = "retry"
			// 续跑**只能**由搬运路径承担：已有的是 COLD 迁移状态机，
			// 而导出归档是从冻结快照另存一份，不推进也不修复那个状态机
			// （它连「目标侧已就位」这个前提都不满足）。故此时即使归档出口可用，
			// 也仍然落 blocked——谎报「已归档」比继续告警更危险。
			if !moverAvailable {
				t.Action = ActionBlocked
				t.Reason = "可重试迁移缺少恢复入口；保留原物（导出归档无法续跑迁移状态机）"
			}
		} else if t.Due = !now.Before(eligibleAt); t.Due {
			t.TriggerReason = TriggerAge
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

// DueByAge 返回按**纯年龄**口径是否已到点。触发口径的最终结论在 Due 字段里。
func (t ArchivePlanTarget) DueByAge(now time.Time) bool {
	return !now.Before(t.EligibleAt)
}

// 归档执行结果的分类。
const (
	// ArchiveMoved 搬运成功（含校验通过）。
	ArchiveMoved = "moved"
	// ArchiveCopied 导出归档成功（含逐文件回读校验）**且**热层已回收。
	ArchiveCopied = "copied"
	// ArchiveCopiedKeptOriginal 导出归档成功，但热层**未**回收（三闸未全开、
	// 删除出口未装配或回收失败）⇒ 归档副本与原物同时存在。
	//
	// 为什么要与 ArchiveKeptOriginal 区分：两者都是「原物还在」，但成因完全不同——
	// 前者是「副本已经安全了、只是还没获准删」，后者是「连副本都没有」。
	// 合成一个读数会让「归档一直在成功、只是闸门没开」与「归档一直失败」长得一模一样。
	ArchiveCopiedKeptOriginal = "copied_kept_original"
	// ArchiveKeptOriginal 搬运/导出失败或无路径 ⇒ 保留原物（绝不回收、绝不删除）。
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

// Remover 是「显式知情放弃」时**真正执行删除**的出口。
//
// 为什么要有它（RET-3，2026-10-11 复核）：原实现的 ActionDiscard 分支只把结果标成
// ArchiveDiscarded 并 `Discarded++`，注释说「交给调用方按已审计的计划执行」，但**没有任何
// 调用方执行删除**——于是观测面报「已删除」而数据原封不动，审计面（G10「谁、何时、多少、去哪」）
// 在删除动作上失真。现在把删除出口显式化：
//   - 装配了 Remover ⇒ 调用它，**成功才**计 ArchiveDiscarded / Discarded++；
//   - 未装配（本版本的生产形态）⇒ 落 ArchiveKeptOriginal + 原因，绝不声称删过。
//
// 接口刻意只留一个方法（与 Mover 同口径）：执行器不该知道「删除一个日分区」在 VL 侧是
// 删目录还是走 /delete，那样它才能用假实现做出「删了什么、没删什么」的可转红回归。
type Remover interface {
	// RemovePartition 删除 (存储命名空间, UTC 日) 分区的数据。失败时返回错误——
	// 调用方必须保留原物并把失败如实上报（绝不把失败当成删除成功）。
	RemovePartition(ctx context.Context, storageNamespace, utcDay string) error
}

// RemoverFunc 函数适配（生产用它把受管 VL 的分区删除接进来）。
type RemoverFunc func(ctx context.Context, storageNamespace, utcDay string) error

// RemovePartition 实现 Remover。
func (f RemoverFunc) RemovePartition(ctx context.Context, storageNamespace, utcDay string) error {
	return f(ctx, storageNamespace, utcDay)
}

// ArchiveExecutor 驱动「删前归档」。
//
// 关键不变量（硬规则的可执行形态）：
//   - 搬运失败 ⇒ 结果必为 ArchiveKeptOriginal，且**绝不**触达任何删除路径；
//   - 未显式 discard ⇒ 结果**永不**为 ArchiveDiscarded；
//   - 无搬运路径 ⇒ ArchiveKeptOriginal + 告警计数，而不是删；
//   - 显式 discard 但**没有删除出口** ⇒ 同样落 ArchiveKeptOriginal（宁占盘不假报删除）。
type ArchiveExecutor struct {
	policy    Policy
	mover     Mover
	remover   Remover
	exporter  ArchiveExporter
	authority ArchiveAuthority

	// mu 守护 stats。
	//
	// 为什么需要它：写入发生在 Driver.Run 那条 goroutine（ExecutePlanned 收尾合并），
	// 而读取来自另一条 goroutine（/metrics 的抓取）。此前 stats 是裸字段——单 goroutine
	// 自用没问题，一旦有了并发读的观测端点，读与累加就是数据竞争：`-race` 会直接报，
	// 生产上表现为读数偶发错乱、排查时又复现不出。
	//
	// 选 RWMutex 而非 Mutex：形态是「多路 scrape 并发读、单 goroutine 写」，读多写少。
	mu    sync.RWMutex
	stats ArchiveStats
}

// ArchiveStats 是归档的累计读数（G10 观测面）。
type ArchiveStats struct {
	// Planned 累计计划目标数。
	Planned int64
	// Moved 累计搬运成功数。
	Moved int64
	// ArchiveCopied 累计「导出归档 + 回读校验」成功数（不含热层是否回收）。
	ArchiveCopied int64
	// ArchiveCopyReclaimed 累计「导出成功且热层已回收」数（= ArchiveCopied 中获准回收的子集）。
	ArchiveCopyReclaimed int64
	// ArchiveCopyKeptOriginal 累计「导出成功但保留原物」数（三闸未全开 / 删除出口未装配 / 回收失败）。
	ArchiveCopyKeptOriginal int64
	// ArchiveRolledBack 累计「已迁移 ARCHIVED 但回收失败、权威回滚 HOT」数。
	//
	// 为什么要单独一条：它与 ArchiveCopyKeptOriginal 都表现为「原物还在」，
	// 但成因不同——前者根本没改权威态，后者是改了又退回来（意味着一次失败删除尝试）。
	// 合并后运维无法判断「归档链路是否已经开始真的动热层」。
	ArchiveRolledBack int64
	// KeptOriginal 累计「保留原物」数（搬运失败、导出失败、无路径、删除出口未装配或删除失败）。
	KeptOriginal int64
	// Discarded 累计**真的执行成功**的显式删除数（未装配删除出口或删除失败都不计入）。
	Discarded int64
	// NotDue 累计未到点数。
	NotDue int64
	// LastError 最近一次失败原因。
	LastError string
	// LastRunAt 最近一次执行时刻。
	LastRunAt time.Time
}

// NewArchiveExecutor 创建执行器。mover 为 nil 时所有目标都落到「保留原物 + 告警」。
//
// 删除出口（Remover）经 SetRemover 单独装配：本版本的 `ActionDiscard` 只在没有搬运路径时
// 才被规划出来，而生产恒有搬运路径，故删除出口默认保持未装配（＝不会删）。
func NewArchiveExecutor(policy Policy, mover Mover) *ArchiveExecutor {
	return &ArchiveExecutor{policy: policy, mover: mover}
}

// SetRemover 装配「显式知情放弃」的删除出口；传 nil 表示未装配（到达该动作时保留原物）。
//
// 本出口同时承担**回收热层**（ActionArchiveCopy 校验通过后的那一步）：两条路径删的是
// 同一样东西（一个日分区的热层数据），分成两个出口只会让「哪条闸门守护了哪次删除」
// 变得说不清。故它们共用同一个 Remover 与同一套三闸判据（见 Policy.DeleteGatesOpen）。
func (e *ArchiveExecutor) SetRemover(r Remover) {
	if e == nil {
		return
	}
	e.remover = r
}

// SetArchiveExporter 装配「导出为独立副本」的出口；传 nil 表示未装配。
//
// 未装配时计划阶段**不会**产出 ActionArchiveCopy（见 ArchiverAvailable），
// 于是行为与本次改动之前完全一致：mover 缺席即落 blocked（保留原物）。
func (e *ArchiveExecutor) SetArchiveExporter(x ArchiveExporter) {
	if e == nil {
		return
	}
	e.exporter = x
}

// SetArchiveAuthority 装配「已归档」权威态的迁移出口。
//
// **未装配即不删**：这是本次改动新增的第二道判据。语义是「没有 Catalog 侧的
// ARCHIVED 权威态，就不允许回收热层」——删掉一个仍属 HOT 权威的目录，会让查询面
// 指向不存在的路径。因此未装配时即使三闸全开也落 ArchiveCopiedKeptOriginal。
//
// 与 Remover 分开装配而非合并：两者守的是不同的事——Authority 回答
// 「权威态改得动吗」，Remover 回答「物理目录删得掉吗」。
// 合并会让「有删除能力但没有权威迁移能力」这种真实形态无法表达（结果就是偷偷删）。
func (e *ArchiveExecutor) SetArchiveAuthority(a ArchiveAuthority) {
	if e == nil {
		return
	}
	e.authority = a
}

// ArchiveReadyForReclaim 报告「导出成功后能否真的回收热层」的三项前置是否齐备。
//
// 三项缺一不可：三闸全开（运维授权）、Authority 已装配（权威态可迁移）、
// Remover 已装配（真有删除出口）。本方法导出供装配日志使用，
// 使现场能一眼看出「归档在跑但为什么盘没省下来」。
func (e *ArchiveExecutor) ArchiveReadyForReclaim() (bool, string) {
	if e == nil {
		return false, "执行器未装配"
	}
	if open, reason := e.policy.DeleteGatesOpen(); !open {
		return false, reason
	}
	if e.authority == nil {
		return false, "未装配归档权威态迁移出口（不得删仍属 HOT 权威的热层）"
	}
	if e.remover == nil {
		return false, "未装配删除出口"
	}
	return true, ""
}

// ArchiverAvailable 报告「导出归档」这条路径此刻是否真的可用。
//
// 判据是**两个条件的与**，缺一不可：
//   - 策略开关 `archive_copy.enabled` 打开（运维显式授权把数据导出到归档目的地）；
//   - 执行器装配了导出出口（真有地方可写，且能回读校验）。
//
// 为什么必须与：只装配出口而开关关闭 ⇒ 默认部署（开关默认关）会在升级后
// 悄悄多出一条导出行为，那是「默认值改变行为」的典型事故；只开开关而没有出口 ⇒
// 计划会报 archive_copy 而执行必然失败，观测面变成一片假告警。
//
// 本方法导出是有意的：`Driver.Step` 在计划阶段需要与执行阶段**同一个**判据，
// 否则会出现「计划说能导、执行说不能」的分裂。
func (e *ArchiveExecutor) ArchiverAvailable() bool {
	if e == nil {
		return false
	}
	return e.exporter != nil && e.policy.ArchiveCopy.Enabled
}

// SetPolicy 替换策略。
func (e *ArchiveExecutor) SetPolicy(p Policy) {
	if e == nil {
		return
	}
	e.policy = p
}

// Stats 返回累计读数副本。
//
// 取读锁后复制：调用方拿到的是值副本，可以随意持有，不会与执行侧后续的累加打架。
func (e *ArchiveExecutor) Stats() ArchiveStats {
	if e == nil {
		return ArchiveStats{}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.stats
}

// mergeStats 把一轮的局部读数并入累计读数。
//
// 锁只在这一处合并上持有，临界区里没有 IO：单轮真正的耗时全在 mover/remover 的搬运/删除上，
// 若把锁包住整个 ExecutePlanned，抓取侧就会被磁盘拖住——观测面不该反压被观测对象，
// 一次慢搬运会让 scrape 超时，恰恰在故障时丢掉最重要的读数。
//
// 语义与直接累加一致：LastRunAt 只在非零时覆盖（零值代表「本轮没执行」），
// LastError 同理（空串不代表一次新的空错误，只是本轮没失败）。
func (e *ArchiveExecutor) mergeStats(round ArchiveStats) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stats.Planned += round.Planned
	e.stats.Moved += round.Moved
	e.stats.ArchiveCopied += round.ArchiveCopied
	e.stats.ArchiveCopyReclaimed += round.ArchiveCopyReclaimed
	e.stats.ArchiveCopyKeptOriginal += round.ArchiveCopyKeptOriginal
	e.stats.ArchiveRolledBack += round.ArchiveRolledBack
	e.stats.KeptOriginal += round.KeptOriginal
	e.stats.Discarded += round.Discarded
	e.stats.NotDue += round.NotDue
	if !round.LastRunAt.IsZero() {
		e.stats.LastRunAt = round.LastRunAt
	}
	if round.LastError != "" {
		e.stats.LastError = round.LastError
	}
}

// Execute 执行一轮归档：自己算计划（纯年龄口径）再执行。
//
// 触发口径更丰富的场景（年龄 + 磁盘水位取先到）用 ExecutePlanned，
// 由调用方先算好计划——判定与执行分离，使「为什么现在搬」只有一个来源。
func (e *ArchiveExecutor) Execute(ctx context.Context, now time.Time, partitions []PartitionRef) []ArchiveOutcome {
	if e == nil || !e.policy.Enabled {
		return nil
	}
	plan := e.policy.PlanArchive(now, partitions, e.mover != nil, e.ArchiverAvailable())
	return e.ExecutePlanned(ctx, now, plan)
}

// ExecutePlanned 执行一份**已算好的**计划。
//
// 错误隔离同 Sweeper：单个分区失败不影响其余分区，失败只记录并保留原物。
func (e *ArchiveExecutor) ExecutePlanned(ctx context.Context, now time.Time, plan []ArchivePlanTarget) []ArchiveOutcome {
	if e == nil || !e.policy.Enabled {
		return nil
	}
	// 本轮读数先累到局部，收尾时一次性并入累计读数（见 mergeStats）。
	//
	// 为什么不逐条加锁写 e.stats：单轮里最少要累加 1 次、最多 3×len(plan) 次，逐条加锁既碎
	// 又挡不住「读到一个只算了一半的轮次」；而把锁包住整个函数会让抓取侧在搬运期间被阻塞。
	// 局部累加 + 收尾合并两头都避开：临界区只有一次赋值级别的合并，且抓取侧看到的是
	// 一轮的完整增量，不会是半轮。
	//
	// LastRunAt 因此从「本轮开始时可见」变成「本轮结束时可见」，差一个搬运时长。
	// 这是有意的取舍：一个只在收尾可见的、完整的读数，比一个提前可见但尚未成形的读数更有用。
	round := ArchiveStats{Planned: int64(len(plan)), LastRunAt: now}
	// 注意用闭包而不是 `defer e.mergeStats(round)`：后者会在 defer 那一刻按值拷贝参数，
	// 循环里的累加全部丢失（这正是本函数最容易写错的一行）。
	defer func() { e.mergeStats(round) }()

	out := make([]ArchiveOutcome, 0, len(plan))
	for _, t := range plan {
		outcome := ArchiveOutcome{Target: t, At: now}
		if !t.Due {
			outcome.Result = ArchiveNotDue
			round.NotDue++
			out = append(out, outcome)
			continue
		}
		switch t.Action {
		case ActionDiscard:
			// 只有显式 discard 才走到这里。**必须真的调用删除出口**：计数只在删除成功后推进，
			// 否则观测面会报「已删除」而数据还在（RET-3：那是审计面失真，不是省事）。
			if e.remover == nil {
				outcome.Result = ArchiveKeptOriginal
				outcome.Err = fmt.Errorf("未装配删除出口（显式 discard 也不得假报删除）")
				round.KeptOriginal++
				round.LastError = fmt.Sprintf("%s/%s 显式 discard 但删除出口未装配，已保留原物",
					t.StorageNamespace, t.UTCDay)
				break
			}
			if err := e.remover.RemovePartition(ctx, t.StorageNamespace, t.UTCDay); err != nil {
				// 与搬运失败同口径：失败 ⇒ 保留原物 + 告警，绝不计入已删除。
				outcome.Result = ArchiveKeptOriginal
				outcome.Err = err
				round.KeptOriginal++
				round.LastError = fmt.Sprintf("%s/%s 删除失败，已保留原物：%v", t.StorageNamespace, t.UTCDay, err)
				break
			}
			outcome.Result = ArchiveDiscarded
			round.Discarded++
		case ActionMoveToCold:
			dirID, err := e.mover.MoveToCold(ctx, t.StorageNamespace, t.UTCDay)
			if err != nil {
				// 硬规则的核心分支：搬运失败 = 保留原物 + 告警，绝不回收、绝不删除。
				outcome.Result = ArchiveKeptOriginal
				outcome.Err = err
				round.KeptOriginal++
				round.LastError = fmt.Sprintf("%s/%s 搬运失败，已保留原物：%v", t.StorageNamespace, t.UTCDay, err)
			} else {
				outcome.Result = ArchiveMoved
				outcome.TargetDirID = dirID
				round.Moved++
			}
		case ActionArchiveCopy:
			// 次选路径：导出 → 压缩 → 校验 → （三闸全开才）回收热层。
			//
			// 不变量：**任何一步不成立都落到「保留原物」**，且只有校验通过 +
			// 三闸全开 + 回收真的成功，才允许声称「已回收」。
			e.executeArchiveCopy(ctx, t, &outcome, &round)
		default:
			// ActionBlocked：没有任何承接物，什么都不做——保留原物。
			outcome.Result = ArchiveKeptOriginal
			outcome.Err = fmt.Errorf("%s", t.Reason)
			round.KeptOriginal++
			round.LastError = fmt.Sprintf("%s/%s 无归档路径：%s", t.StorageNamespace, t.UTCDay, t.Reason)
		}
		out = append(out, outcome)
	}
	return out
}

// executeArchiveCopy 执行单个分区的「冻结 → 导出 → 校验 → 归档 → 回收」。
//
// 为什么单独成一个方法而不是在 switch 里摊开：这六步每一步失败都必须落到**同一个**
// 结局（保留原物），而摊开写最容易漏掉其中一条 break——漏掉一次就是「导出失败却计了已归档」
// 或「没回收却计了已回收」，两者都会让审计面与真实状态脱节。
//
// 判定次序（不可调换）：
//  0. 未装配归档权威态出口 ⇒ 保留原物（它同时持有导出所需的**物理日冻结**；
//     没有冻结就没有一致快照，导出的副本不可信，更谈不上删热层）；
//  1. 冻结该物理日；失败 ⇒ 保留原物（不导出，因为导出的副本必然不可信）；
//  2. 没有导出出口 / 导出 + 逐文件回读校验失败 ⇒ **解冻** + 保留原物（副本不可信）；
//  3. 校验通过但删除三闸未全开 ⇒ **解冻** + 保留原物（副本已安全，只是还没获准删）；
//  4. 三闸全开但没有删除出口 ⇒ **解冻** + 保留原物（宁占盘不假报删除）；
//  5. 先把权威态改成 ARCHIVED（落 journal、可审计）；迁移失败 ⇒ **解冻** + 不删；
//  6. 再删热层；删除失败 ⇒ **回滚 ARCHIVED→HOT**，保留原物并如实上报；
//  7. 删除成功 ⇒ 记 ArchiveCopied + ArchiveCopyReclaimed，并标记热层已回收。
//
// 关于第 5 步为什么必须在删除之前：Catalog 是查询/写入权威的唯一真源。
// 反序（先删后改）会存在一个窗口：目录已消失而 Catalog 仍说 HOT ⇒ 查询规划器
// 会为它产出 range 并指向不存在的目录，表现为「这一天查不到」而不是「已归档」。
//
// 关于「解冻」为什么散落在每条退出路径上：冻结期会拒绝该日的迟到写入。
// 只在成功路径解冻，会让「导出成功但闸门没开」的每一天永久拒绝写入。
func (e *ArchiveExecutor) executeArchiveCopy(ctx context.Context, t ArchivePlanTarget, outcome *ArchiveOutcome, round *ArchiveStats) {
	if e.authority == nil {
		// 没有权威态出口 ⇒ 既无法建立导出所需的冻结，也无法在归档后安全删热层。
		// 这里必须保守：计划写了 archive_copy 不代表能安全执行。
		outcome.Result = ArchiveCopiedKeptOriginal
		outcome.Err = fmt.Errorf("未装配归档权威态出口（无法冻结物理日、也无法把权威态改成 ARCHIVED）")
		round.KeptOriginal++
		round.LastError = fmt.Sprintf("%s/%s 计划为导出归档但归档权威态出口未装配，已保留原物",
			t.StorageNamespace, t.UTCDay)
		return
	}
	if err := e.authority.FreezeForArchive(ctx, t.StorageNamespace, t.UTCDay); err != nil {
		outcome.Result = ArchiveKeptOriginal
		outcome.Err = fmt.Errorf("归档前冻结物理日失败（未导出、未删除）：%w", err)
		round.KeptOriginal++
		round.LastError = fmt.Sprintf("%s/%s 归档前冻结失败，已保留原物：%v",
			t.StorageNamespace, t.UTCDay, err)
		return
	}
	// release 是「最终没有归档」的统一出口：解冻并保留原物。
	// 它在每条未归档路径上都会被调用，避免冻结泄漏（该日永久拒绝写入）。
	release := func(reason string) {
		if err := e.authority.ReleaseArchiveFreeze(ctx, t.StorageNamespace, t.UTCDay, reason); err != nil {
			round.LastError = fmt.Sprintf("%s/%s 解冻失败（该物理日将继续拒绝写入）：%v",
				t.StorageNamespace, t.UTCDay, err)
		}
	}

	if e.exporter == nil {
		// 计划阶段说能导出、执行侧却没有出口：属于装配错误。
		outcome.Result = ArchiveKeptOriginal
		outcome.Err = fmt.Errorf("未装配导出归档出口（计划为 archive_copy 但执行侧不可用）")
		round.KeptOriginal++
		round.LastError = fmt.Sprintf("%s/%s 计划为导出归档但导出出口未装配，已保留原物",
			t.StorageNamespace, t.UTCDay)
		release("导出出口未装配")
		return
	}

	res, err := e.exporter.ExportPartition(ctx, t.StorageNamespace, t.UTCDay)
	if err != nil {
		// 硬规则在次选路径上的对应分支：导出（或校验）失败 = 保留原物 + 告警。
		outcome.Result = ArchiveKeptOriginal
		outcome.Err = err
		round.KeptOriginal++
		round.LastError = fmt.Sprintf("%s/%s 导出归档失败，已保留原物：%v", t.StorageNamespace, t.UTCDay, err)
		release("导出或校验失败")
		return
	}
	// 至此副本已落盘且逐文件回读校验通过；「去哪」记录在 TargetDirID（与搬运同口径）。
	round.ArchiveCopied++
	outcome.TargetDirID = res.Location

	// 回收热层 = 删除，必须与既有删除路径守同一套三闸。三闸未全开 ⇒ 副本与原物并存。
	if open, reason := e.policy.DeleteGatesOpen(); !open {
		outcome.Result = ArchiveCopiedKeptOriginal
		outcome.Err = fmt.Errorf("导出归档已校验通过，但删除三闸未全开：%s", reason)
		round.ArchiveCopyKeptOriginal++
		round.KeptOriginal++
		round.LastError = fmt.Sprintf("%s/%s 已导出归档（%d 个对象）但热层未回收：%s",
			t.StorageNamespace, t.UTCDay, res.ObjectCount, reason)
		release("删除三闸未全开")
		return
	}
	if e.remover == nil {
		outcome.Result = ArchiveCopiedKeptOriginal
		outcome.Err = fmt.Errorf("三闸已开但未装配删除出口（不得假报回收）")
		round.ArchiveCopyKeptOriginal++
		round.KeptOriginal++
		round.LastError = fmt.Sprintf("%s/%s 已导出归档但删除出口未装配，热层未回收",
			t.StorageNamespace, t.UTCDay)
		release("删除出口未装配")
		return
	}

	// 第 5 步：先把权威态改成 ARCHIVED（落 journal）。失败 ⇒ 不删。
	ev := evidenceOf(res, t.UTCDayTime())
	if err := e.authority.MarkArchived(ctx, t.StorageNamespace, t.UTCDay, ev); err != nil {
		outcome.Result = ArchiveCopiedKeptOriginal
		outcome.Err = fmt.Errorf("归档权威态迁移失败（未回收热层）：%w", err)
		round.ArchiveCopyKeptOriginal++
		round.KeptOriginal++
		round.LastError = fmt.Sprintf("%s/%s 已导出归档但权威态迁移失败，热层未回收：%v",
			t.StorageNamespace, t.UTCDay, err)
		release("权威态迁移失败")
		return
	}

	// 第 7 步：删热层。失败必须回滚权威态，否则会留下「Catalog 说已归档、目录还在」的不一致。
	if rerr := e.remover.RemovePartition(ctx, t.StorageNamespace, t.UTCDay); rerr != nil {
		rbErr := e.authority.RollbackArchived(ctx, t.StorageNamespace, t.UTCDay, "热层回收失败："+rerr.Error())
		round.ArchiveRolledBack++
		outcome.Result = ArchiveCopiedKeptOriginal
		round.ArchiveCopyKeptOriginal++
		round.KeptOriginal++
		if rbErr != nil {
			// 回滚也失败：权威态仍为 ARCHIVED、热层仍在。这是安全方向的不一致
			// （数据多一份），但必须把两个错误都报出来，绝不能只报一个。
			outcome.Err = fmt.Errorf("热层回收失败：%w；且权威态回滚失败：%v（该分区为 ARCHIVED 且热层未回收，需人工核对）", rerr, rbErr)
			round.LastError = fmt.Sprintf("%s/%s 已导出归档、回收失败且回滚失败：%v / %v",
				t.StorageNamespace, t.UTCDay, rerr, rbErr)
			return
		}
		outcome.Err = fmt.Errorf("热层回收失败，已回滚为热层权威：%w", rerr)
		round.LastError = fmt.Sprintf("%s/%s 已导出归档但热层回收失败，权威已回滚 HOT：%v",
			t.StorageNamespace, t.UTCDay, rerr)
		return
	}

	// 第 8 步：删除成功 ⇒ 审计闭环。标记失败只影响「已回收」这一读数的可见性，
	// 不影响正确性（目录确实删了），故不把结果降级，但必须如实写进 LastError。
	outcome.Result = ArchiveCopied
	round.ArchiveCopyReclaimed++
	if merr := e.authority.MarkHotReclaimed(ctx, t.StorageNamespace, t.UTCDay); merr != nil {
		outcome.Err = fmt.Errorf("热层已删除但回收标记未落盘（审计读数缺失，需人工核对）：%w", merr)
		round.LastError = fmt.Sprintf("%s/%s 热层已删除但回收标记失败：%v",
			t.StorageNamespace, t.UTCDay, merr)
	}
}
