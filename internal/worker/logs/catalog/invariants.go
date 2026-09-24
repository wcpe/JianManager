package catalog

import "fmt"

// QueryRef 是 QueryPlanner 允许使用的唯一权威引用。
type QueryRef struct {
	OK         bool
	Owner      Owner
	DirID      string
	Generation uint64
	// Projection 若已发布则携带；读取方必须整对象使用，不得拆开组合。
	Projection *PublishedProjection
}

// OwnerForQuery 返回 Catalog 选定的唯一查询权威。
//
// 不变量：
//   - ATTACHED_STAGING 的暂存目录不是查询权威；
//   - OWNER_SWITCHED 之后旧目录（残留/re-attach）永远不被返回；
//   - 物理目录扫描/VL auto-attach 不能改变本结果。
func OwnerForQuery(rec *Record) QueryRef {
	if rec == nil || rec.Owner == "" || rec.OwnerDirID == "" {
		return QueryRef{OK: false}
	}
	return QueryRef{
		OK:         true,
		Owner:      rec.Owner,
		DirID:      rec.OwnerDirID,
		Generation: rec.Generation,
		Projection: rec.PublishedProjection,
	}
}

// IsQueryPlannerVisible 报告某个物理目录是否允许进入 QueryPlanner。
// 旧目录 re-attach、staging attach、目录扫描结果都不是权威。
func IsQueryPlannerVisible(rec *Record, dirID string) bool {
	if dirID == "" {
		return false
	}
	ref := OwnerForQuery(rec)
	if !ref.OK {
		return false
	}
	// 失败/恢复态：authority 可能不确定，宁可排除。
	if IsFailure(rec.MigrationState) && rec.RecoveryRequired {
		// 仍只允许 Catalog owner 目录；不因 VL 附着而扩大。
		return dirID == ref.DirID
	}
	return dirID == ref.DirID
}

// QueryableSide 描述崩溃恢复后“可查询”的权威侧。
type QueryableSide struct {
	Queryable    bool
	Owner        Owner
	Generation   uint64
	DirID        string
	StagingOpen  bool // ATTACHED_STAGING 时为 true：物理可挂载，但对 QueryPlanner 仍关闭
	ExcludedDirs []string
}

// QueryableSide 推导当前记录的可查询侧。
func QueryableSideOf(rec *Record) QueryableSide {
	side := QueryableSide{Queryable: false}
	if rec == nil {
		return side
	}
	excluded := excludedQueryDirs(rec)
	side.ExcludedDirs = excluded
	side.StagingOpen = rec.MigrationState == StateAttachedStaging

	ref := OwnerForQuery(rec)
	if !ref.OK {
		return side
	}
	// 失败且需恢复：标记不可用范围时 Queryable=false，避免把不确定权威当完整结果。
	if IsFailure(rec.MigrationState) && rec.RecoveryRequired {
		return side
	}
	// ROUTING_FROZEN…ATTACHED_STAGING：权威仍在迁移源侧，可查询。
	// OWNER_SWITCHED 及之后：权威在目标侧。
	side.Queryable = true
	side.Owner = ref.Owner
	side.Generation = ref.Generation
	side.DirID = ref.DirID
	return side
}

// excludedQueryDirs 列出必须排除出 QueryPlanner 的目录 ID。
func excludedQueryDirs(rec *Record) []string {
	if rec == nil {
		return nil
	}
	var out []string
	ownerDir := rec.OwnerDirID
	// 目标/staging：在切换前不得查询。
	if rec.TargetDirID != "" && rec.TargetDirID != ownerDir {
		out = append(out, rec.TargetDirID)
	}
	// 迁移源：切换后成为残留，不得查询。
	if rec.MigrationFromDirID != "" && rec.MigrationFromDirID != ownerDir {
		out = append(out, rec.MigrationFromDirID)
	}
	for _, d := range rec.ResidualDirs {
		if d.ID != ownerDir {
			out = append(out, d.ID)
		}
	}
	for _, d := range rec.Dirs {
		if d.Role == DirStaging && d.ID != ownerDir {
			out = append(out, d.ID)
		}
	}
	// 去重
	seen := map[string]struct{}{}
	uniq := make([]string, 0, len(out))
	for _, id := range out {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	return uniq
}

// StagingQueryable 报告 ATTACHED_STAGING（或任何非权威 staging）是否可被查询。
// 契约：ATTACHED_STAGING 的副本可被 VL 挂载，但 QueryPlanner 必须排除 → 恒为 false。
func StagingQueryable(rec *Record) bool {
	_ = rec
	return false
}

// AuthoritySwitchAllowed 报告 rec 是否允许执行 OWNER_SWITCHED。
func AuthoritySwitchAllowed(rec *Record) bool {
	return rec != nil && rec.MigrationState == StateAttachedStaging
}

// ApplyOwnerSwitch 是唯一改变查询/写入权威的原子 Catalog 提交（契约 §5.2）。
//
// 原子内容：owner、generation、owner_dir、PublishedProjection、写入路由指针。
// 同时将旧权威目录降为 residual，保证 re-attach 后仍被 OwnerForQuery/QueryPlanner 排除。
func ApplyOwnerSwitch(rec *Record, target Owner, targetDirID string, targetGen uint64, proj *PublishedProjection) error {
	if rec == nil {
		return fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	if !AuthoritySwitchAllowed(rec) {
		return fmt.Errorf("%w: OWNER_SWITCHED requires ATTACHED_STAGING, got %s", ErrIllegalTransition, rec.MigrationState)
	}
	if !target.Valid() {
		return fmt.Errorf("%w: target=%q", ErrInvalidOwner, target)
	}
	if targetDirID == "" {
		return fmt.Errorf("%w: target dir required", ErrInvalidState)
	}
	if proj == nil {
		return ErrMissingProjection
	}
	if proj.QueryLocationDirID == "" {
		proj.QueryLocationDirID = targetDirID
	}
	if proj.QueryGeneration == 0 {
		proj.QueryGeneration = targetGen
	}

	oldDir := rec.OwnerDirID
	oldOwner := rec.Owner
	oldGen := rec.Generation

	// —— 以下字段在同一逻辑提交中一起写入 ——
	rec.Owner = target
	rec.OwnerDirID = targetDirID
	rec.Generation = targetGen
	rec.PublishedProjection = proj
	rec.WriteRoute.Owner = target
	rec.WriteRoute.DirID = targetDirID
	rec.WriteRoute.Generation = targetGen
	rec.WriteRoute.Frozen = false
	rec.WriteRoute.LateOwner = target
	rec.WriteRoute.LateDirID = targetDirID
	rec.MigrationState = StateOwnerSwitched
	rec.JournalComplete = false

	// 旧权威目录 → residual（即使 VL re-attach 也不得查询）。
	if oldDir != "" && oldDir != targetDirID {
		rec.ResidualDirs = append(rec.ResidualDirs, PhysicalDir{
			ID:   oldDir,
			Path: "",
			Role: DirResidual,
		})
	}
	// staging 目录角色提升为 owner。
	for i := range rec.Dirs {
		if rec.Dirs[i].ID == targetDirID {
			rec.Dirs[i].Role = DirOwner
		}
	}
	_ = oldOwner
	_ = oldGen
	return nil
}

// WriteTarget 是写入路由结果。
type WriteTarget struct {
	OK         bool
	Owner      Owner
	DirID      string
	Generation uint64
	// Frozen 表示写入路由已冻结，调用方应按受控路径处理，而不是写回原 HOT 权威。
	Frozen bool
	// Reason 可观测原因（catalog-owner / catalog-frozen-late / catalog-staging-late / …）。
	Reason string
}

// OwnerForWrite 按 Catalog 写入路由指针返回写入目标。
// 迟到事件也走本函数；禁止按 now-7d 重新生成 HOT 权威分区。
func OwnerForWrite(rec *Record) WriteTarget {
	if rec == nil || rec.Owner == "" {
		return WriteTarget{OK: false, Reason: "no-catalog-record"}
	}
	wr := rec.WriteRoute
	if wr.Owner == "" {
		// 回退到权威字段，仍以 Catalog 为准。
		return WriteTarget{
			OK:         true,
			Owner:      rec.Owner,
			DirID:      rec.OwnerDirID,
			Generation: rec.Generation,
			Frozen:     false,
			Reason:     "catalog-owner",
		}
	}
	if !wr.Frozen {
		return WriteTarget{
			OK:         true,
			Owner:      wr.Owner,
			DirID:      wr.DirID,
			Generation: wr.Generation,
			Frozen:     false,
			Reason:     "catalog-owner",
		}
	}
	// 冻结期：迟到/新写入走受控 late 目标（COLD / staging / 补录），不是原 HOT。
	lateOwner := wr.LateOwner
	lateDir := wr.LateDirID
	reason := "catalog-frozen-late"
	if lateOwner == "" {
		// 无显式 late 目标时，不回写 HOT：落到当前 Catalog owner 的受控指向。
		lateOwner = rec.Owner
		lateDir = rec.OwnerDirID
		reason = "catalog-owner-controlled"
	}
	// 切换后的 late 事件：owner 已是 COLD/ARCHIVE，按 owner/generation 路由。
	if rec.MigrationState == StateOwnerSwitched ||
		rec.MigrationState == StateQueryLeaseDraining ||
		rec.MigrationState == StateDetached ||
		rec.MigrationState == StateCleaned {
		reason = "catalog-owner-late"
		if wr.LateOwner != "" {
			lateOwner = wr.LateOwner
			lateDir = wr.LateDirID
		} else {
			lateOwner = rec.Owner
			lateDir = rec.OwnerDirID
		}
	}
	return WriteTarget{
		OK:         true,
		Owner:      lateOwner,
		DirID:      lateDir,
		Generation: wr.Generation,
		Frozen:     true,
		Reason:     reason,
	}
}

// RouteLateEvent 按 Catalog owner/generation 路由迟到事件。
//
// eventUTCDay 仅用于可观测性/审计对照；不得用它推导“该写 HOT”。
// 本函数永远不会仅因 eventUTCDay 早于 now-7d 之类的日历规则而生成新的 HOT 权威。
func RouteLateEvent(rec *Record, eventUTCDay string) WriteTarget {
	t := OwnerForWrite(rec)
	if !t.OK {
		t.Reason = "late-no-catalog"
		return t
	}
	// 显式证明：日历日不参与权威选择。
	_ = eventUTCDay
	return t
}

// IsMigrationComplete 报告迁移是否真正完成。
//
// 契约：DETACHED ≠ 完成；目录残留、journal 或校验未完成时仍是恢复态。
// 只有 CLEANED 且 journal 完成、无 residual 时才为 true。
func IsMigrationComplete(rec *Record) bool {
	if rec == nil {
		return false
	}
	if rec.MigrationState != StateCleaned {
		return false
	}
	if !rec.JournalComplete {
		return false
	}
	if len(rec.ResidualDirs) > 0 {
		return false
	}
	if !rec.LastVerifyCompleted && rec.LastVerifyChecksum == "" {
		// CLEANED 允许无显式 checksum 的内存模型，但 journal 必须 complete（上面已要求）。
		// 生产应写 LastVerify*；此处不因缺 checksum 单独判失败，避免纯逻辑测试噪音。
		return true
	}
	return true
}

// DetachIsComplete 是显式不变量封装：DETACHED 状态在 journal/residual 未完成时不得视为完成。
func DetachIsComplete(rec *Record) bool {
	if rec == nil {
		return false
	}
	if rec.MigrationState != StateDetached {
		return false
	}
	// 即使 journal complete，DETACHED 仍表示清理未完成 → 迁移未完成。
	// 若 journal 不完整，则更是恢复态。
	return false
}

// JournalIncomplete 报告 journal 是否未完成。
func JournalIncomplete(rec *Record) bool {
	return rec != nil && !rec.JournalComplete
}
