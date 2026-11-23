// Package catalog 实现Partition Catalog 与迁移状态机基础（纯逻辑 + 内存 journal）。
//
// 契约来源：
//   - docs/specs/worker-log-platform-contract/spec.md §5 Catalog / PublishedProjection / 迁移状态
//   - docs/specs/worker-log-lifecycle/spec.md §3.1 迁移不变量、§3.2 崩溃验收
//
// 本包不实现 VL 进程控制（vlsup），不访问 CP 业务库，也不把物理目录扫描结果当作权威。
// 查询与写入权威只认 Catalog 记录；残留旧目录即使被 VL 自动 re-attach，也不得进入 QueryPlanner。
package catalog

import (
	"fmt"
	"time"
)

// Owner 是分区权威存储层。Catalog 为每个 (storage_namespace, utc_day) 维护唯一 owner。
//
// 第三态 OwnerArchive 就是「已归档（ARCHIVED）」权威态：
//   - 正本在归档目的地（manifest + 逐对象 sha256 + coverage 是权威证据，见 ArchiveEvidence）；
//   - 热层副本**可以**回收（回收后 OwnerDirID 指向归档标识，不再指向任何物理热层目录）；
//   - 查询面按「本地是否已恢复」决定可读性：有对齐的 PublishedProjection ⇒ 读 Rehydrate namespace；
//     否则显式降级为 ARCHIVE_NOT_RESTORED（绝不指向已删除的热层目录、绝不静默返回空结果）。
//
// 该取值此前只被 rehydrate 侧引用而**没有任何生产迁移路径能到达**（全仓仅测试构造），
// 故本次把它作为唯一权威态接通，而不是再造一个语义重复的第四态。
type Owner string

const (
	OwnerHot     Owner = "hot"
	OwnerCold    Owner = "cold"
	OwnerArchive Owner = "archive"
)

// Valid reports whether o is a known storage owner.
func (o Owner) Valid() bool {
	switch o {
	case OwnerHot, OwnerCold, OwnerArchive:
		return true
	default:
		return false
	}
}

// ArchiveCoverage 是归档覆盖范围的权威摘要（由导出侧逐文件枚举得出）。
type ArchiveCoverage struct {
	// ObjectCount 归档对象数；0 表示没有任何可读证据，禁止据此回收热层。
	ObjectCount int `json:"object_count"`
	// SourceBytes 源侧（冻结快照）字节数合计，压缩前。
	SourceBytes int64 `json:"source_bytes"`
	// StoredBytes 归档侧字节数合计，即压缩后落盘量。
	StoredBytes int64 `json:"stored_bytes"`
	// Complete 报告导出侧枚举是否已穷尽（缺此证明即不得声明「已归档」）。
	Complete bool `json:"complete"`
}

// ArchiveEvidence 是「正本已在归档」的权威证据。
//
// 为什么这些字段必须落在 Catalog 记录里（而不是只写在归档 manifest 里）：
// 删除热层是一个**不可逆**动作，它必须以「Catalog 承认这一天已归档」为前提。
// 若证据只在归档侧，查询规划器仍会认为该分区属 HOT 权威并指向即将被删的目录——
// 这正是此前不敢放行删除的原因。证据入 Catalog 后，删除前后每步都有单一真源可查：
// 何时（ArchivedAt）、去哪（ProviderKind + ObjectPrefix）、清单校验和（ManifestChecksum）、
// 覆盖范围（Coverage）、以及回收是否真的发生（HotReclaimed）。
type ArchiveEvidence struct {
	// ProviderKind 归档目的地类型（如 local / s3）。
	ProviderKind string `json:"provider_kind"`
	// ObjectPrefix 归档对象前缀，形如 namespace/day/g{generation}。
	ObjectPrefix string `json:"object_prefix"`
	// ManifestChecksum 写盘后**回读**的 manifest 字节 sha256。
	ManifestChecksum string `json:"manifest_checksum"`
	// Coverage 覆盖范围。
	Coverage ArchiveCoverage `json:"coverage"`
	// ArchivedAt 迁移到 ARCHIVED 权威态的时刻（UTC）。
	ArchivedAt time.Time `json:"archived_at"`

	// —— 迁移前权威快照：回滚的唯一依据（回滚必须把查询能力原样还给热层）——
	PrevOwner      Owner                `json:"prev_owner"`
	PrevDirID      string               `json:"prev_dir_id"`
	PrevGeneration uint64               `json:"prev_generation"`
	PrevProjection *PublishedProjection `json:"prev_projection,omitempty"`

	// HotReclaimed 报告热层是否已真的回收。false 表示「已归档但热层残留」——
	// 这是安全方向（数据多存一份、盘没省下来），但必须可观测，故单独成字段。
	HotReclaimed bool `json:"hot_reclaimed"`
	// HotReclaimedAt 热层回收完成时刻。
	HotReclaimedAt time.Time `json:"hot_reclaimed_at,omitempty"`
	// RollbackCount 归档→热层回滚的累计次数（回收失败被退回 HOT 的次数）。
	RollbackCount int `json:"rollback_count,omitempty"`
	// LastRollbackReason 最近一次回滚原因（审计用）。
	LastRollbackReason string `json:"last_rollback_reason,omitempty"`

	// —— 归档对象自身的回收（「归档对象回收」，桶内生命周期）——
	//
	// 为什么这四个字段构成**独立的终态**，而不是把 Owner 改成一个新的 `purged` 取值：
	// Owner 的语义是「正本在哪一层」（hot/cold/archive），而归档对象被按保留期清除之后
	// 发生的事不是「正本搬去了另一层」——是**这一层的副本没了**。若为此新增
	// `OwnerPurged`，全仓十余处 `Owner == OwnerHot / OwnerCold / OwnerArchive` 的
	// switch 都会落进各自的 default 分支（TierOf 会返回一个不存在的 tier、
	// NamespaceForOwner 会去找不存在的 namespace、writeAdmissionLocked 的
	// 「已归档拒绝写入」判据会失效改走「按 owner 猜位置」），而它们的 default 分支
	// 各写各的、无人审计 ⇒ 一个数据已经不在的选择会伪装成「另一个存储层」。
	// 保持 Owner=OwnerArchive（权威确实在归档目的地这一层，只是该层的副本被清掉了），
	// 把「已清除」表达为证据的属性，查询面据 Purged 给出**可区分**的原因
	// （ARCHIVE_PURGED，而不是谎报 LOG_ARCHIVE_MISSING）。

	// PurgeRequestedAt 本轮「归档对象回收」的**开始**时刻（已落 journal）。
	//
	// 为什么开始时刻要单独记：回收是「先改权威态、再逐对象删、成功才标完成」的三步，
	// 进程在第二步中途退出时，记录上只会留下这个时刻。运维据此能区分
	// 「从未尝试过」与「尝试过、删了一半」——两者的人工处置完全不同
	// （前者等下一轮，后者必须人工比对归档目录里还剩什么）。
	PurgeRequestedAt time.Time `json:"purge_requested_at,omitempty"`
	// Purged 报告归档对象的副本是否**已全部**按保留期清除（终态）。
	//
	// 为 true 时该分区在本地既没有热层副本（若 HotReclaimed）也没有归档对象：
	// 数据已按保留期真实消失，查询面必须显式说「已清除、不可恢复」，
	// 绝不能再报「未恢复（去 rehydrate）」——那条建议此刻是一条死路。
	Purged bool `json:"purged,omitempty"`
	// PurgedAt 清除完成时刻（UTC）。
	PurgedAt time.Time `json:"purged_at,omitempty"`
	// PurgeReason 清除原因（保留期到期 / 手工处置），审计用。
	PurgeReason string `json:"purge_reason,omitempty"`
	// PurgeAttempts 累计尝试次数。**回滚不清零**：它记录的是「这个分区被尝试清除过几次」，
	// 一次失败尝试是真实发生过的事故，不能因为退回原态就从审计里消失。
	PurgeAttempts int `json:"purge_attempts,omitempty"`
	// LastPurgeError 最近一次清除失败的原因（回滚时写入）。
	LastPurgeError string `json:"last_purge_error,omitempty"`
}

// ArchiveObjectPurged 报告该分区的归档对象副本是否已按保留期清除。
//
// 单独成方法而不是让调用方各自判 `ev.Purged`：这个判断在查询面与观测面有多个调用点，
// 而「证据为 nil」与「证据存在但未清除」必须被同一处代码区分开
// （nil 证据意味着记录不自洽，绝不是「已清除」）。
func (e *ArchiveEvidence) ArchiveObjectPurged() bool {
	return e != nil && e.Purged
}

// Clone 返回证据的深副本（嵌套投影不得被读取方改写）。
func (e *ArchiveEvidence) Clone() *ArchiveEvidence {
	if e == nil {
		return nil
	}
	out := *e
	if e.PrevProjection != nil {
		out.PrevProjection = (&Record{PublishedProjection: e.PrevProjection}).Clone().PublishedProjection
	}
	return &out
}

// ArchiveAuthorityID 返回归档权威位置的可读标识。
//
// 它**不是**物理目录：OwnerDirID 在 ARCHIVED 态指向归档标识而不是热层目录，
// 于是「已删除的热层目录」不可能再被 OwnerForQuery / IsQueryPlannerVisible 选中。
func (e *ArchiveEvidence) ArchiveAuthorityID() string {
	if e == nil {
		return ""
	}
	return "archive:" + e.ProviderKind + ":" + e.ObjectPrefix
}

// PartitionKey 唯一标识一条 Catalog 记录。
type PartitionKey struct {
	StorageNamespace string `json:"storage_namespace"`
	// UTCDay 形如 "2006-01-02"。
	UTCDay string `json:"utc_day"`
}

func (k PartitionKey) String() string {
	return k.StorageNamespace + "/" + k.UTCDay
}

// DirRole 描述物理目录在 Catalog 中的角色。扫描结果只是输入，角色由 Catalog 赋予。
type DirRole string

const (
	// DirOwner 当前权威副本（唯一可进入 QueryPlanner / 写入路由的目录）。
	DirOwner DirRole = "owner"
	// DirStaging 迁移暂存副本；可被 VL 挂载，但 QueryPlanner 必须排除。
	DirStaging DirRole = "staging"
	// DirResidual 迁移后残留旧目录；re-attach 后仍必须排除出查询。
	DirResidual DirRole = "residual"
)

// PhysicalDir 是 Catalog 登记的物理目录引用。
type PhysicalDir struct {
	ID   string  `json:"id"`
	Path string  `json:"path,omitempty"`
	Role DirRole `json:"role"`
}

// QueryLeaseRef 是绑定到分区/generation 的查询租约引用。租约计数是派生值。
type QueryLeaseRef struct {
	LeaseID    string    `json:"lease_id"`
	ViewID     string    `json:"view_id"`
	Generation uint64    `json:"generation"`
	Owner      Owner     `json:"owner,omitempty"`
	DirID      string    `json:"dir_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Holder     string    `json:"holder,omitempty"`
	// InvalidReason 非空表示租约已因 generation 切换/权限等原因失效。
	InvalidReason string `json:"invalid_reason,omitempty"`
}

// Active reports whether the lease still holds on the given generation.
func (l QueryLeaseRef) Active(now time.Time, generation uint64) bool {
	if l.InvalidReason != "" {
		return false
	}
	if !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt) {
		return false
	}
	return l.Generation == generation
}

// SourceGenerationRef 记录 PublishedProjection 覆盖的源分段。
type SourceGenerationRef struct {
	LogSourceID      string `json:"log_source_id"`
	SourceGeneration string `json:"source_generation"`
}

// SourceProjection binds physical micro-generations to one source and its
// closed prefix. Independent sources may reuse the same generation name.
type SourceProjection struct {
	SourceGenerationRef
	ProjectionGenerations []string `json:"projection_generations"`
	ClosedVisibleSeq      uint64   `json:"closed_visible_seq"`
}

// PublishedProjection 是一次 Catalog 持久提交发布的不可变查询对象（契约 §5.1）。
// 读取方不得分别取“最新 manifest”和“最新水位”自行组合。
type PublishedProjection struct {
	SourceProjections        []SourceProjection    `json:"source_projections,omitempty"`
	ManifestVersion          string                `json:"manifest_version"`
	CoveredSourceGenerations []SourceGenerationRef `json:"covered_source_generations,omitempty"`
	ClosedVisibleSeq         map[string]uint64     `json:"closed_visible_seq,omitempty"`
	CoverageComplete         bool                  `json:"coverage_complete"`
	ConflictCount            uint64                `json:"conflict_count"`
	QueryLocationDirID       string                `json:"query_location_dir_id"`
	QueryGeneration          uint64                `json:"query_generation"`
	// ProjectionGeneration 是实际查询位置中的隔离 generation；响应不确定时
	// 新 generation 可独立重建并发布，旧物理目标保持诊断可见但不再进入 QueryPlanner。
	ProjectionGeneration string `json:"projection_generation,omitempty"`
	// ProjectionGenerations 是共同构成当前规范事件集合的不可变微 generation。
	// 非空时查询必须使用完整列表；单值字段仅保留旧 journal 兼容与最新 generation 诊断。
	ProjectionGenerations []string `json:"projection_generations,omitempty"`
}

// WriteRoute 是写入路由指针。迟到/在途事件按 Catalog 路由，禁止按 now-7d 猜测 HOT 权威。
type WriteRoute struct {
	Owner      Owner  `json:"owner"`
	Generation uint64 `json:"generation"`
	DirID      string `json:"dir_id"`
	// Frozen 为 true 时本分区写入路由已冻结，新写入不得再进入原 HOT 权威目录。
	Frozen bool `json:"frozen"`
	// LateOwner/LateDirID：冻结期及迁移未完成时的受控迟到写入目标（COLD / staging / 补录）。
	LateOwner Owner  `json:"late_owner"`
	LateDirID string `json:"late_dir_id"`
}

// Record 是每个 (storage_namespace, utc_day) 的 Catalog 记录。
type Record struct {
	Key PartitionKey `json:"key"`

	// 当前查询/写入权威（仅 OWNER_SWITCHED 提交可原子变更）。
	Owner      Owner  `json:"owner"`
	Generation uint64 `json:"generation"`
	OwnerDirID string `json:"owner_dir_id"`

	// 迁移目标侧（空 Owner 表示无在途迁移）。
	TargetOwner      Owner  `json:"target_owner,omitempty"`
	TargetDirID      string `json:"target_dir_id,omitempty"`
	TargetGeneration uint64 `json:"target_generation,omitempty"`

	// 迁移源侧（冻结时快照，恢复时用于对照）。
	MigrationFromOwner      Owner  `json:"migration_from_owner,omitempty"`
	MigrationFromDirID      string `json:"migration_from_dir_id,omitempty"`
	MigrationFromGeneration uint64 `json:"migration_from_generation,omitempty"`

	// 迁移状态（见 migration.go）。
	MigrationState MigrationState `json:"migration_state"`

	// Journal 状态。JournalIncomplete 时 DETACHED 不得视为完成。
	JournalLastSeq      uint64 `json:"journal_last_seq"`
	JournalComplete     bool   `json:"journal_complete"`
	LastVerifyChecksum  string `json:"last_verify_checksum,omitempty"`
	LastVerifyCompleted bool   `json:"last_verify_completed"`

	// 查询租约引用；计数由 len(QueryLeases) 派生。
	QueryLeases []QueryLeaseRef `json:"query_leases,omitempty"`

	// 写入路由指针。
	WriteRoute WriteRoute `json:"write_route"`

	// 已发布投影（OWNER_SWITCHED 必须携带）。
	PublishedProjection *PublishedProjection `json:"published_projection,omitempty"`

	// 目录登记：物理扫描是输入，权威角色由 Catalog 决定。
	Dirs []PhysicalDir `json:"dirs,omitempty"`

	// 残留旧目录；re-attach 后必须继续排除出 QueryPlanner。
	ResidualDirs []PhysicalDir `json:"residual_dirs,omitempty"`

	// 启动恢复标记。
	RecoveryRequired bool     `json:"recovery_required"`
	PartialReasons   []string `json:"partial_reasons,omitempty"`
	// ConflictGeneration 非空表示 journal/目录对照出现冲突 generation。
	ConflictGeneration string `json:"conflict_generation,omitempty"`

	// ArchiveEvidence 非空表示本分区此前/当前的价值来自归档（ARCHIVED 权威态）。
	//
	// 与 Owner 的关系：`Owner == OwnerArchive` 时它是**当前**权威证据；
	// Owner 已回滚为 HOT 时它保留为**历史**审计（回滚次数、原因），此时不得据此删除热层。
	ArchiveEvidence *ArchiveEvidence `json:"archive_evidence,omitempty"`

	// RetiredHotDirs 是「已归档、热层副本已回收」的目录登记。
	//
	// 单独成字段而**不**放进 ResidualDirs：ResidualDirs 参与
	// IsMigrationComplete 的「迁移未完成」判定，而归档完成后的热层回收集合
	// 恰恰是**已完成**的正常形态——混用会让每个已归档分区永久显示为迁移未完成。
	// 它们仍必须被 QueryPlanner 排除（见 excludedQueryDirs），否则 VL 重新 attach
	// 一个旧目录就能把已删除数据的位置塞回查询面。
	RetiredHotDirs []PhysicalDir `json:"retired_hot_dirs,omitempty"`
}

// Clone 返回记录及嵌套投影的深副本，读取方不能改写权威对象。
func (r *Record) Clone() *Record {
	if r == nil {
		return nil
	}
	c := *r
	if r.QueryLeases != nil {
		c.QueryLeases = append([]QueryLeaseRef(nil), r.QueryLeases...)
	}
	if r.Dirs != nil {
		c.Dirs = append([]PhysicalDir(nil), r.Dirs...)
	}
	if r.ResidualDirs != nil {
		c.ResidualDirs = append([]PhysicalDir(nil), r.ResidualDirs...)
	}
	if r.PartialReasons != nil {
		c.PartialReasons = append([]string(nil), r.PartialReasons...)
	}
	if r.RetiredHotDirs != nil {
		c.RetiredHotDirs = append([]PhysicalDir(nil), r.RetiredHotDirs...)
	}
	c.ArchiveEvidence = r.ArchiveEvidence.Clone()
	if r.PublishedProjection != nil {
		pp := *r.PublishedProjection
		if r.PublishedProjection.ClosedVisibleSeq != nil {
			pp.ClosedVisibleSeq = make(map[string]uint64, len(r.PublishedProjection.ClosedVisibleSeq))
			for k, v := range r.PublishedProjection.ClosedVisibleSeq {
				pp.ClosedVisibleSeq[k] = v
			}
		}
		if r.PublishedProjection.CoveredSourceGenerations != nil {
			pp.CoveredSourceGenerations = append([]SourceGenerationRef(nil), r.PublishedProjection.CoveredSourceGenerations...)
		}
		if r.PublishedProjection.ProjectionGenerations != nil {
			pp.ProjectionGenerations = append([]string(nil), r.PublishedProjection.ProjectionGenerations...)
		}
		pp.SourceProjections = CloneSourceProjections(r.PublishedProjection.SourceProjections)
		c.PublishedProjection = &pp
	}
	return &c
}

func CloneSourceProjections(sources []SourceProjection) []SourceProjection {
	if sources == nil {
		return nil
	}
	out := append([]SourceProjection(nil), sources...)
	for i := range out {
		out[i].ProjectionGenerations = append([]string(nil), sources[i].ProjectionGenerations...)
	}
	return out
}

// LeaseCount 是派生的租约计数，不作为权威字段存储。
func (r *Record) LeaseCount() int {
	return len(r.QueryLeases)
}

// ActiveLeaseCount 统计在给定时刻仍绑定当前 generation 的租约。
func (r *Record) ActiveLeaseCount(now time.Time) int {
	n := 0
	for _, l := range r.QueryLeases {
		if l.Active(now, r.Generation) {
			n++
		}
	}
	return n
}

// AddLease 追加查询租约引用。
func (r *Record) AddLease(l QueryLeaseRef) {
	r.QueryLeases = append(r.QueryLeases, l)
}

// DirByID 按目录 ID 查找登记目录。
func (r *Record) DirByID(dirID string) (PhysicalDir, bool) {
	for _, d := range r.Dirs {
		if d.ID == dirID {
			return d, true
		}
	}
	for _, d := range r.ResidualDirs {
		if d.ID == dirID {
			return d, true
		}
	}
	return PhysicalDir{}, false
}

// NewStableRecord 构造无在途迁移的稳定分区记录（HOT 权威）。
func NewStableRecord(key PartitionKey, owner Owner, generation uint64, dirID string) *Record {
	return &Record{
		Key:             key,
		Owner:           owner,
		Generation:      generation,
		OwnerDirID:      dirID,
		MigrationState:  StateCleaned, // 无迁移时视为已清理的稳定态；权威仍由 Owner/Dir 表达
		JournalComplete: true,
		Dirs: []PhysicalDir{{
			ID:   dirID,
			Role: DirOwner,
		}},
		WriteRoute: WriteRoute{
			Owner:      owner,
			Generation: generation,
			DirID:      dirID,
			Frozen:     false,
			LateOwner:  owner,
			LateDirID:  dirID,
		},
	}
}

// ErrCatalog 是本包业务错误基类。
var ErrCatalog = fmt.Errorf("catalog")

// 常见错误。
var (
	ErrIllegalTransition = fmt.Errorf("%w: illegal migration transition", ErrCatalog)
	ErrInvalidState      = fmt.Errorf("%w: invalid state", ErrCatalog)
	ErrInvalidOwner      = fmt.Errorf("%w: invalid owner", ErrCatalog)
	ErrMissingProjection = fmt.Errorf("%w: published projection required", ErrCatalog)
	ErrNotFound          = fmt.Errorf("%w: record not found", ErrCatalog)
	ErrJournalOrder      = fmt.Errorf("%w: journal entries must be append-only increasing seq", ErrCatalog)
)
