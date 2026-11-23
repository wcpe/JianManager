package catalog

import "time"

// RecoveryView 是启动崩溃恢复后某分区的逻辑状态投影（契约 §5.3 / lifecycle §3.2）。
type RecoveryView struct {
	// 权威侧（Catalog 最终选定）。
	Owner      Owner
	Generation uint64
	OwnerDirID string

	// 查询侧。
	Queryable         bool
	QueryDirID        string
	StagingOpen       bool
	ExcludedQueryDirs []string

	// 写入侧。
	WriteOwner      Owner
	WriteDirID      string
	WriteGeneration uint64
	WriteFrozen     bool

	// 迁移/恢复元数据。
	MigrationState     MigrationState
	AuthoritySwitched  bool
	JournalIncomplete  bool
	MigrationComplete  bool
	RecoveryRequired   bool
	PartialReasons     []string
	ConflictGeneration string
	NextRecoveryAction string
}

// Recover 对单条 Catalog 记录做幂等逻辑恢复，返回期望的 owner/generation/queryable 侧。
//
// 原则：
//  1. 权威以 journal/Catalog 中最后一次 OWNER_SWITCHED 提交为准；没有该提交则保持迁移前 owner；
//  2. ATTACHED_STAGING 可物理挂载，但对 QueryPlanner 不可查询；
//  3. DETACHED ≠ 完成；journal/residual 未完成时 RecoveryRequired=true；
//  4. 残留旧目录始终进入 ExcludedQueryDirs，不得因 VL re-attach 进入查询；
//  5. FAILED_* 权威停留在最后一次成功提交，且标记恢复动作。
//
// Recover 不修改 rec（纯函数语义）；需要落库时由 Catalog.StartingRecover 复制后应用。
func Recover(rec *Record) RecoveryView {
	if rec == nil {
		return RecoveryView{RecoveryRequired: true, NextRecoveryAction: "nil-record"}
	}
	c := rec.Clone()

	view := RecoveryView{
		MigrationState:     c.MigrationState,
		JournalIncomplete:  !c.JournalComplete,
		ConflictGeneration: c.ConflictGeneration,
	}

	// —— 权威判定 ——
	// 记录字段在 OWNER_SWITCHED 提交时已原子更新；Recover 信任该提交后的 owner。
	// 若状态仍是切换前但 TargetGeneration/Residual 显示已切换（journal 重放），以 owner 字段为准。
	authoritySwitched := c.MigrationState == StateOwnerSwitched ||
		c.MigrationState == StateQueryLeaseDraining ||
		c.MigrationState == StateDetached ||
		c.MigrationState == StateCleaned ||
		(IsFailure(c.MigrationState) && c.OwnerDirID != "" && c.MigrationFromDirID != "" && c.OwnerDirID != c.MigrationFromDirID)

	// 失败态且尚未切换：权威回到迁移前。
	if IsFailure(c.MigrationState) && c.OwnerDirID == c.MigrationFromDirID {
		authoritySwitched = false
	}
	if IsFailure(c.MigrationState) && c.MigrationFromDirID != "" && c.Owner == c.MigrationFromOwner && c.OwnerDirID == c.MigrationFromDirID {
		authoritySwitched = false
	}

	view.AuthoritySwitched = authoritySwitched
	view.Owner = c.Owner
	view.Generation = c.Generation
	view.OwnerDirID = c.OwnerDirID

	// 写入侧按 Catalog 路由指针推导（冻结期走受控 late 目标，而不是日历）。
	wt := OwnerForWrite(c)
	view.WriteOwner = wt.Owner
	view.WriteDirID = wt.DirID
	view.WriteGeneration = wt.Generation
	view.WriteFrozen = wt.Frozen
	if !wt.OK {
		view.WriteOwner = c.Owner
		view.WriteDirID = c.OwnerDirID
		view.WriteGeneration = c.Generation
	}

	// —— 查询侧 ——
	side := QueryableSideOf(c)
	view.Queryable = side.Queryable
	view.QueryDirID = side.DirID
	view.StagingOpen = side.StagingOpen
	view.ExcludedQueryDirs = side.ExcludedDirs

	// ATTACHED_STAGING：staging 可挂载，查询侧必须仍是切换前权威且不包含 staging。
	if c.MigrationState == StateAttachedStaging {
		view.StagingOpen = true
		if c.TargetDirID != "" {
			view.ExcludedQueryDirs = appendUnique(view.ExcludedQueryDirs, c.TargetDirID)
		}
		// 查询权威仍在源侧。
		view.Owner = c.MigrationFromOwner
		view.OwnerDirID = c.MigrationFromDirID
		view.Generation = c.MigrationFromGeneration
		view.Queryable = c.MigrationFromOwner.Valid() && c.MigrationFromDirID != ""
		view.QueryDirID = c.MigrationFromDirID
		// 写入侧：冻结期迟到/新写入走受控 late 目标（已由 OwnerForWrite 推导）。
		view.WriteFrozen = true
	}

	// 切换前的正常路径：查询权威在源侧；写入路由冻结后按 late 目标。
	preSwitch := []MigrationState{
		StateRoutingFrozen, StateDraining, StateSnapshotting, StateStagingVerify,
	}
	for _, s := range preSwitch {
		if c.MigrationState == s {
			view.Owner = c.MigrationFromOwner
			view.OwnerDirID = c.MigrationFromDirID
			view.Generation = c.MigrationFromGeneration
			view.Queryable = c.MigrationFromOwner.Valid() && c.MigrationFromDirID != ""
			view.QueryDirID = c.MigrationFromDirID
			view.WriteFrozen = true
			if c.TargetDirID != "" {
				view.ExcludedQueryDirs = appendUnique(view.ExcludedQueryDirs, c.TargetDirID)
			}
			break
		}
	}

	// —— 完成度与恢复动作 ——
	view.MigrationComplete = IsMigrationComplete(c)
	view.RecoveryRequired = false
	view.PartialReasons = append([]string(nil), c.PartialReasons...)

	switch c.MigrationState {
	case StateRoutingFrozen:
		view.NextRecoveryAction = "resume-from-draining:verify-freeze-then-drain"
		if view.JournalIncomplete {
			view.RecoveryRequired = true
			view.PartialReasons = appendUnique(view.PartialReasons, "JOURNAL_INCOMPLETE")
		}
	case StateDraining:
		view.NextRecoveryAction = "resume-draining:complete-inflight-then-snapshot"
		if view.JournalIncomplete {
			view.RecoveryRequired = true
			view.PartialReasons = appendUnique(view.PartialReasons, "JOURNAL_INCOMPLETE")
		}
	case StateSnapshotting:
		view.NextRecoveryAction = "resume-snapshotting:restart-or-verify-snapshot"
		view.RecoveryRequired = true
		view.PartialReasons = appendUnique(view.PartialReasons, "MIGRATION_IN_PROGRESS")
	case StateStagingVerify:
		view.NextRecoveryAction = "resume-staging-verify:reverify-staging-not-queryable"
		view.RecoveryRequired = true
		view.PartialReasons = appendUnique(view.PartialReasons, "MIGRATION_IN_PROGRESS")
	case StateAttachedStaging:
		view.NextRecoveryAction = "keep-staging-excluded-await-owner-switch"
		view.RecoveryRequired = true
		view.PartialReasons = appendUnique(view.PartialReasons, "STAGING_ATTACHED_NOT_QUERYABLE")
	case StateOwnerSwitched:
		view.NextRecoveryAction = "resume-query-lease-draining"
		if view.JournalIncomplete {
			view.RecoveryRequired = true
			view.PartialReasons = appendUnique(view.PartialReasons, "JOURNAL_INCOMPLETE")
		}
		// 切换已提交：查询权威必须是新 owner；旧目录排除。
		view.Queryable = c.Owner.Valid() && c.OwnerDirID != ""
		view.QueryDirID = c.OwnerDirID
		if c.MigrationFromDirID != "" {
			view.ExcludedQueryDirs = appendUnique(view.ExcludedQueryDirs, c.MigrationFromDirID)
		}
	case StateQueryLeaseDraining:
		view.NextRecoveryAction = "drain-legacy-query-leases-then-detach"
		if hasActiveLeases(c) {
			view.RecoveryRequired = true
			view.PartialReasons = appendUnique(view.PartialReasons, "QUERY_LEASES_ACTIVE")
		}
		if view.JournalIncomplete {
			view.RecoveryRequired = true
			view.PartialReasons = appendUnique(view.PartialReasons, "JOURNAL_INCOMPLETE")
		}
		view.Queryable = c.Owner.Valid() && c.OwnerDirID != ""
		view.QueryDirID = c.OwnerDirID
	case StateDetached:
		// DETACHED ≠ 完成。
		view.MigrationComplete = false
		view.RecoveryRequired = true
		view.PartialReasons = appendUnique(view.PartialReasons, "DETACHED_NOT_COMPLETE")
		if view.JournalIncomplete {
			view.PartialReasons = appendUnique(view.PartialReasons, "JOURNAL_INCOMPLETE")
		}
		if len(c.ResidualDirs) > 0 {
			view.PartialReasons = appendUnique(view.PartialReasons, "RESIDUAL_DIRS")
		}
		view.NextRecoveryAction = "verify-residuals-journal-then-clean"
		view.Queryable = c.Owner.Valid() && c.OwnerDirID != ""
		view.QueryDirID = c.OwnerDirID
	case StateCleaned:
		if !c.JournalComplete || len(c.ResidualDirs) > 0 {
			view.RecoveryRequired = true
			if !c.JournalComplete {
				view.PartialReasons = appendUnique(view.PartialReasons, "JOURNAL_INCOMPLETE")
			}
			if len(c.ResidualDirs) > 0 {
				view.PartialReasons = appendUnique(view.PartialReasons, "RESIDUAL_DIRS")
			}
			view.NextRecoveryAction = "complete-journal-or-remove-residuals"
			view.MigrationComplete = false
		} else {
			view.NextRecoveryAction = "none"
			view.MigrationComplete = true
		}
		view.Queryable = c.Owner.Valid() && c.OwnerDirID != ""
		view.QueryDirID = c.OwnerDirID
	case StateFailedRetryable:
		view.RecoveryRequired = true
		view.PartialReasons = appendUnique(view.PartialReasons, "FAILED_RETRYABLE")
		view.NextRecoveryAction = "retry-migration-from-journal-from-state"
		// 权威：未切换则保持 from 侧。
		if !authoritySwitched {
			view.Owner = c.MigrationFromOwner
			view.OwnerDirID = c.MigrationFromDirID
			view.Generation = c.MigrationFromGeneration
			view.Queryable = c.MigrationFromOwner.Valid() && c.MigrationFromDirID != ""
			view.QueryDirID = c.MigrationFromDirID
			view.WriteOwner = c.MigrationFromOwner
			view.WriteDirID = c.MigrationFromDirID
			view.WriteGeneration = c.MigrationFromGeneration
			view.WriteFrozen = true
		}
		if c.TargetDirID != "" {
			view.ExcludedQueryDirs = appendUnique(view.ExcludedQueryDirs, c.TargetDirID)
		}
	case StateFailedManual:
		view.RecoveryRequired = true
		view.PartialReasons = appendUnique(view.PartialReasons, "FAILED_MANUAL")
		view.NextRecoveryAction = "manual-intervention-required"
		if !authoritySwitched {
			view.Owner = c.MigrationFromOwner
			view.OwnerDirID = c.MigrationFromDirID
			view.Generation = c.MigrationFromGeneration
			view.Queryable = c.MigrationFromOwner.Valid() && c.MigrationFromDirID != ""
			view.QueryDirID = c.MigrationFromDirID
			view.WriteOwner = c.MigrationFromOwner
			view.WriteDirID = c.MigrationFromDirID
			view.WriteGeneration = c.MigrationFromGeneration
			view.WriteFrozen = true
		}
		if c.TargetDirID != "" {
			view.ExcludedQueryDirs = appendUnique(view.ExcludedQueryDirs, c.TargetDirID)
		}
	default:
		// 无迁移的稳定态（StateCleaned 已覆盖；空状态按稳定权威处理）。
		if c.MigrationState == "" {
			view.Queryable = c.Owner.Valid() && c.OwnerDirID != ""
			view.QueryDirID = c.OwnerDirID
			view.NextRecoveryAction = "none"
			view.MigrationComplete = c.JournalComplete
		}
	}

	// 冲突 generation：查询范围降级为 PARTIAL/RECOVERY_REQUIRED。
	if c.ConflictGeneration != "" {
		view.RecoveryRequired = true
		view.PartialReasons = appendUnique(view.PartialReasons, "CONFLICT_GENERATION")
		if view.NextRecoveryAction == "none" {
			view.NextRecoveryAction = "resolve-conflict-generation"
		}
	}

	// residual/staging 永远排除；owner 目录若被误列则去掉。
	view.ExcludedQueryDirs = filterOut(view.ExcludedQueryDirs, view.OwnerDirID)
	if view.QueryDirID != "" {
		view.ExcludedQueryDirs = filterOut(view.ExcludedQueryDirs, view.QueryDirID)
	}

	return view
}

func hasActiveLeases(c *Record) bool {
	// 未显式失效且尚未到期的租约会阻塞 QUERY_LEASE_DRAINING 完成。
	// 到期时间为零表示“无到期约束”，在租约排空阶段视为仍持有。
	now := time.Now()
	for _, l := range c.QueryLeases {
		if l.InvalidReason != "" {
			continue
		}
		if l.ExpiresAt.IsZero() || now.Before(l.ExpiresAt) {
			return true
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func filterOut(list []string, drop string) []string {
	if drop == "" || len(list) == 0 {
		return list
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x == drop {
			continue
		}
		out = append(out, x)
	}
	return out
}
