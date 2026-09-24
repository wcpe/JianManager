// Package catalog 实现 FR-477 Partition Catalog 与迁移状态机基础（纯逻辑 + 内存 journal）。
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
}

// Clone 返回记录的浅层可变副本（切片元素共享，测试/恢复用）。
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
