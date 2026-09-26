package lifecycle

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// Manager 驱动 Partition Catalog 迁移状态机与启动恢复。
type Manager struct {
	cat      *catalog.Catalog
	ops      Ops
	ret      RetentionGate
	runtime  Runtime
	observer StateObserver
}

// New 创建 Manager。cat 为 nil 时使用内存 Catalog（测试）。
func New(cat *catalog.Catalog, ops Ops) *Manager {
	if cat == nil {
		cat = catalog.New(nil)
	}
	return &Manager{cat: cat, ops: ops}
}

// Catalog 返回底层 Catalog。
func (m *Manager) Catalog() *catalog.Catalog { return m.cat }

// SetOps 注入/替换物理操作。
func (m *Manager) SetOps(ops Ops) { m.ops = ops }

// SetRetentionGate 注入 last-copy 保护钩子。
func (m *Manager) SetRetentionGate(g RetentionGate) { m.ret = g }

// SetRuntime 注入 VL 薄运行时（可选）。
func (m *Manager) SetRuntime(rt Runtime) { m.runtime = rt }

// SetObserver 注入状态观察回调（测试）。
func (m *Manager) SetObserver(fn StateObserver) { m.observer = fn }

// PutPartition 登记稳定分区（HOT 权威）。
func (m *Manager) PutPartition(key catalog.PartitionKey, owner catalog.Owner, generation uint64, dirID string) error {
	return m.cat.Put(catalog.NewStableRecord(key, owner, generation, dirID))
}

// Recover 启动恢复：journal 重放 + 每条记录逻辑恢复。
func (m *Manager) Recover() map[catalog.PartitionKey]catalog.RecoveryView {
	return m.cat.StartingRecover()
}

// RouteLateEvent 按 Catalog owner/generation 路由迟到事件。
// eventUTCDay 仅审计对照；禁止据此重新生成 HOT 权威分区。
func (m *Manager) RouteLateEvent(storageNamespace, utcDay, eventUTCDay string) (catalog.WriteTarget, bool) {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	return m.cat.RouteWrite(key, eventUTCDay)
}

// QueryOwner 返回 QueryPlanner 唯一权威。
func (m *Manager) QueryOwner(storageNamespace, utcDay string) (catalog.QueryRef, bool) {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	return m.cat.OwnerForQuery(key)
}

// IsDirQueryable 报告目录是否允许进入 QueryPlanner（残留 re-attach 过滤）。
func (m *Manager) IsDirQueryable(storageNamespace, utcDay, dirID string) bool {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	return m.cat.IsQueryDirVisible(key, dirID)
}

// GetPartition 返回分区记录副本。
func (m *Manager) GetPartition(storageNamespace, utcDay string) (*catalog.Record, bool) {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	return m.cat.Get(key)
}

// StartMigration 以 (ns, day) 为目标，走完 catalog 迁移状态机。
//
// 步骤：BeginMigration(ROUTING_FROZEN) → Drain → SNAPSHOTTING/Snapshot →
// STAGING_VERIFY/Copy+Verify → ATTACHED_STAGING/Attach（staging 不可查询）→
// SwitchOwner(OWNER_SWITCHED 原子) → QUERY_LEASE_DRAINING/LeaseDrain →
// retention AllowDetach → DETACHED/Detach → 清理 residual → CLEANED。
//
// 操作失败时将分区标 FAILED_RETRYABLE（权威停留在最后一次成功提交），并返回错误。
// 崩溃模拟可不走本函数，而是手动 Advance 到目标状态后调用 Recover。
func (m *Manager) StartMigration(storageNamespace, utcDay string, target catalog.Owner, targetDirID string) (*MigrationResult, error) {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	res := &MigrationResult{StagingDirID: targetDirID}

	fail := func(from catalog.MigrationState, op string, err error) (*MigrationResult, error) {
		detail := fmt.Sprintf("%s failed at %s: %v", op, from, err)
		rec, ferr := m.cat.Fail(key, catalog.StateFailedRetryable, detail)
		res.Record = rec
		res.BlockedReason = detail
		res.OpsCalled = append(res.OpsCalled, "fail:"+op)
		if ferr != nil {
			return res, fmt.Errorf("lifecycle: %s (fail-journal: %v)", detail, ferr)
		}
		return res, fmt.Errorf("lifecycle: %s", detail)
	}

	observe := func() {
		if m.observer == nil {
			return
		}
		if rec, ok := m.cat.Get(key); ok {
			m.observer(key, rec)
		}
	}

	rec, err := m.cat.BeginMigration(key, target, targetDirID)
	if err != nil {
		return res, err
	}
	res.Record = rec
	res.FromDirID = rec.MigrationFromDirID
	res.StatesWalked = append(res.StatesWalked, catalog.StateRoutingFrozen)
	observe()

	// —— DRAINING ——
	if m.ops.Drain != nil {
		res.OpsCalled = append(res.OpsCalled, "drain")
		if err := m.ops.Drain(key); err != nil {
			return fail(catalog.StateRoutingFrozen, "drain", err)
		}
	}
	rec, err = m.cat.Advance(key, catalog.StateDraining)
	if err != nil {
		return fail(catalog.StateRoutingFrozen, "advance-draining", err)
	}
	res.Record = rec
	res.StatesWalked = append(res.StatesWalked, catalog.StateDraining)
	observe()

	// —— SNAPSHOTTING ——
	rec, err = m.cat.Advance(key, catalog.StateSnapshotting)
	if err != nil {
		return fail(catalog.StateDraining, "advance-snapshotting", err)
	}
	res.Record = rec
	res.StatesWalked = append(res.StatesWalked, catalog.StateSnapshotting)
	observe()
	if m.ops.Snapshot != nil {
		res.OpsCalled = append(res.OpsCalled, "snapshot")
		sid, err := m.ops.Snapshot(key, rec.MigrationFromDirID)
		if err != nil {
			return fail(catalog.StateSnapshotting, "snapshot", err)
		}
		res.SnapshotID = sid
	}

	// —— STAGING_VERIFY ——
	rec, err = m.cat.Advance(key, catalog.StateStagingVerify)
	if err != nil {
		return fail(catalog.StateSnapshotting, "advance-staging-verify", err)
	}
	res.Record = rec
	res.StatesWalked = append(res.StatesWalked, catalog.StateStagingVerify)
	observe()
	var copyID string
	if m.ops.Copy != nil {
		res.OpsCalled = append(res.OpsCalled, "copy")
		cid, err := m.ops.Copy(key, res.SnapshotID, targetDirID)
		if err != nil {
			return fail(catalog.StateStagingVerify, "copy", err)
		}
		copyID = cid
		res.CopyID = cid
	}
	if m.ops.Verify != nil {
		res.OpsCalled = append(res.OpsCalled, "verify")
		sum, err := m.ops.Verify(key, copyID, targetDirID)
		if err != nil {
			return fail(catalog.StateStagingVerify, "verify", err)
		}
		res.Checksum = sum
		if err := m.cat.RecordVerification(key, sum); err != nil {
			return fail(catalog.StateStagingVerify, "record-verification", err)
		}
	}

	// —— ATTACHED_STAGING：可挂载，QueryPlanner 必须排除 ——
	rec, err = m.cat.Advance(key, catalog.StateAttachedStaging)
	if err != nil {
		return fail(catalog.StateStagingVerify, "advance-attached-staging", err)
	}
	res.Record = rec
	res.StatesWalked = append(res.StatesWalked, catalog.StateAttachedStaging)
	if m.runtime != nil {
		ns := NamespaceForOwner(target)
		_ = m.runtime.AttachStorage(ns, targetDirID, targetDirID)
	}
	if m.ops.Attach != nil {
		res.OpsCalled = append(res.OpsCalled, "attach")
		if err := m.ops.Attach(key, targetDirID); err != nil {
			return fail(catalog.StateAttachedStaging, "attach", err)
		}
	}
	// 观察点：staging 已 attach 但仍不可查询。
	observe()

	// —— OWNER_SWITCHED 原子提交 ——
	cur, ok := m.cat.Get(key)
	if !ok {
		return fail(catalog.StateAttachedStaging, "get-before-switch", fmt.Errorf("record missing"))
	}
	targetGen := cur.TargetGeneration
	if targetGen == 0 {
		targetGen = cur.MigrationFromGeneration + 1
	}
	proj := migratedProjection(cur.PublishedProjection, targetDirID, targetGen)
	rec, err = m.cat.SwitchOwner(key, target, targetDirID, targetGen, proj)
	if err != nil {
		return fail(catalog.StateAttachedStaging, "owner-switch", err)
	}
	res.Record = rec
	res.OwnerSwitched = true
	res.StatesWalked = append(res.StatesWalked, catalog.StateOwnerSwitched)
	observe()

	// —— QUERY_LEASE_DRAINING ——
	rec, err = m.cat.Advance(key, catalog.StateQueryLeaseDraining)
	if err != nil {
		return fail(catalog.StateOwnerSwitched, "advance-lease-draining", err)
	}
	res.Record = rec
	res.StatesWalked = append(res.StatesWalked, catalog.StateQueryLeaseDraining)
	if m.ops.LeaseDrain != nil {
		res.OpsCalled = append(res.OpsCalled, "lease-drain")
		oldGen := rec.MigrationFromGeneration
		if err := m.ops.LeaseDrain(key, oldGen); err != nil {
			return fail(catalog.StateQueryLeaseDraining, "lease-drain", err)
		}
	}
	observe()

	// —— retention last-copy 保护：未确认接收前不得 detach 最后有效副本 ——
	fromDir := rec.MigrationFromDirID
	if fromDir == "" {
		fromDir = res.FromDirID
	}
	if m.ret != nil {
		ok, reason := m.ret.AllowDetach(key, fromDir, targetDirID)
		if !ok {
			detail := "retention last-copy protection: " + reason
			res.BlockedReason = detail
			res.OpsCalled = append(res.OpsCalled, "retention-block")
			// 权威已切换；不 detach，保持恢复态。
			return res, fmt.Errorf("lifecycle: %s", detail)
		}
	}

	// —— DETACHED ——
	if m.ops.Detach != nil {
		res.OpsCalled = append(res.OpsCalled, "detach")
		if err := m.ops.Detach(key, fromDir); err != nil {
			return fail(catalog.StateQueryLeaseDraining, "detach", err)
		}
	}
	if m.runtime != nil && fromDir != "" {
		_ = m.runtime.DetachStorage(NamespaceForOwner(rec.MigrationFromOwner), fromDir)
	}
	rec, err = m.cat.Advance(key, catalog.StateDetached)
	if err != nil {
		return fail(catalog.StateQueryLeaseDraining, "advance-detached", err)
	}
	res.Record = rec
	res.StatesWalked = append(res.StatesWalked, catalog.StateDetached)
	// detach ≠ 完成：此处理由测试观察。
	observe()

	// —— CLEANED：清 residual + journal complete ——
	if m.ops.Cleanup != nil && fromDir != "" {
		res.OpsCalled = append(res.OpsCalled, "cleanup")
		if err := m.ops.Cleanup(key, fromDir); err != nil {
			return fail(catalog.StateDetached, "cleanup", err)
		}
	}
	if fromDir != "" {
		if cur, ok := m.cat.Get(key); ok {
			var remain []catalog.PhysicalDir
			for _, d := range cur.ResidualDirs {
				if d.ID != fromDir {
					remain = append(remain, d)
				}
			}
			cur.ResidualDirs = remain
			if err := m.cat.Put(cur); err != nil {
				return fail(catalog.StateDetached, "clear-residual", err)
			}
		}
	}
	checksum := res.Checksum
	if checksum == "" {
		checksum = "lifecycle-cleaned"
	}
	if err := m.cat.MarkJournalComplete(key, checksum); err != nil {
		return fail(catalog.StateDetached, "journal-complete", err)
	}
	rec, err = m.cat.Advance(key, catalog.StateCleaned)
	if err != nil {
		return fail(catalog.StateDetached, "advance-cleaned", err)
	}
	res.Record = rec
	res.StatesWalked = append(res.StatesWalked, catalog.StateCleaned)
	res.OpsCalled = append(res.OpsCalled, "cleaned")
	observe()
	return res, nil
}

func migratedProjection(current *catalog.PublishedProjection, targetDirID string, targetGen uint64) *catalog.PublishedProjection {
	projection := &catalog.PublishedProjection{
		ManifestVersion:    fmt.Sprintf("migration-%s-g%d", targetDirID, targetGen),
		QueryLocationDirID: targetDirID, QueryGeneration: targetGen,
	}
	if current == nil {
		return projection
	}
	projection.CoverageComplete = current.CoverageComplete
	projection.ConflictCount = current.ConflictCount
	projection.ProjectionGeneration = current.ProjectionGeneration
	projection.SourceProjections = catalog.CloneSourceProjections(current.SourceProjections)
	projection.ProjectionGenerations = append([]string(nil), current.ProjectionGenerations...)
	projection.CoveredSourceGenerations = append([]catalog.SourceGenerationRef(nil), current.CoveredSourceGenerations...)
	if current.ClosedVisibleSeq != nil {
		projection.ClosedVisibleSeq = make(map[string]uint64, len(current.ClosedVisibleSeq))
		for key, value := range current.ClosedVisibleSeq {
			projection.ClosedVisibleSeq[key] = value
		}
	}
	return projection
}

// ResumeMigration continues the persisted state after process restart. Every
// physical operation is required to be idempotent; authority changes only via
// SwitchOwner and physical cleanup runs only after DETACHED is journaled.
func (m *Manager) ResumeMigration(storageNamespace, utcDay string) (*MigrationResult, error) {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	rec, ok := m.cat.Get(key)
	if !ok {
		return nil, fmt.Errorf("lifecycle: partition %s not found", key)
	}
	result := &MigrationResult{Record: rec, FromDirID: rec.MigrationFromDirID, StagingDirID: rec.TargetDirID}
	if rec.MigrationState == catalog.StateCleaned && rec.TargetDirID == "" {
		return result, nil
	}
	if rec.MigrationState == catalog.StateFailedManual {
		return result, fmt.Errorf("lifecycle: manual recovery required")
	}
	if rec.MigrationState == catalog.StateFailedRetryable {
		resume := catalog.MigrationState("")
		entries := m.cat.Journal().Entries(key)
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].State == catalog.StateFailedRetryable {
				resume = entries[i].FromState
				break
			}
		}
		if resume == "" {
			return result, fmt.Errorf("lifecycle: retry state missing from journal")
		}
		var err error
		rec, err = m.cat.Advance(key, resume)
		if err != nil {
			return result, err
		}
	}
	observe := func(rec *catalog.Record) {
		result.Record = rec
		if m.observer != nil {
			m.observer(key, rec)
		}
	}
	fail := func(op string, err error) (*MigrationResult, error) {
		failed, journalErr := m.cat.Fail(key, catalog.StateFailedRetryable, op+": "+err.Error())
		result.Record, result.BlockedReason = failed, err.Error()
		if journalErr != nil {
			return result, fmt.Errorf("lifecycle: %s failed: %v; journal: %v", op, err, journalErr)
		}
		return result, fmt.Errorf("lifecycle: %s failed: %w", op, err)
	}
	var snapshotID string
	for {
		rec, _ = m.cat.Get(key)
		switch rec.MigrationState {
		case catalog.StateRoutingFrozen:
			if m.ops.Drain != nil {
				if err := m.ops.Drain(key); err != nil {
					return fail("drain", err)
				}
			}
			next, err := m.cat.Advance(key, catalog.StateDraining)
			if err != nil {
				return fail("advance-draining", err)
			}
			observe(next)
		case catalog.StateDraining:
			next, err := m.cat.Advance(key, catalog.StateSnapshotting)
			if err != nil {
				return fail("advance-snapshotting", err)
			}
			observe(next)
		case catalog.StateSnapshotting:
			if m.ops.Snapshot != nil {
				var err error
				snapshotID, err = m.ops.Snapshot(key, rec.MigrationFromDirID)
				if err != nil {
					return fail("snapshot", err)
				}
			}
			next, err := m.cat.Advance(key, catalog.StateStagingVerify)
			if err != nil {
				return fail("advance-staging-verify", err)
			}
			observe(next)
		case catalog.StateStagingVerify:
			if !rec.LastVerifyCompleted {
				if snapshotID == "" && m.ops.Snapshot != nil {
					var err error
					snapshotID, err = m.ops.Snapshot(key, rec.MigrationFromDirID)
					if err != nil {
						return fail("resume-snapshot", err)
					}
				}
				copyID := ""
				if m.ops.Copy != nil {
					var err error
					copyID, err = m.ops.Copy(key, snapshotID, rec.TargetDirID)
					if err != nil {
						return fail("copy", err)
					}
				}
				checksum := ""
				if m.ops.Verify != nil {
					var err error
					checksum, err = m.ops.Verify(key, copyID, rec.TargetDirID)
					if err != nil {
						return fail("verify", err)
					}
				}
				if checksum == "" {
					return fail("verify", fmt.Errorf("verification checksum missing"))
				}
				if err := m.cat.RecordVerification(key, checksum); err != nil {
					return fail("record-verification", err)
				}
			}
			next, err := m.cat.Advance(key, catalog.StateAttachedStaging)
			if err != nil {
				return fail("advance-attached-staging", err)
			}
			observe(next)
		case catalog.StateAttachedStaging:
			if m.ops.Attach != nil {
				if err := m.ops.Attach(key, rec.TargetDirID); err != nil {
					return fail("attach", err)
				}
			}
			targetGen := rec.TargetGeneration
			if targetGen == 0 {
				targetGen = rec.MigrationFromGeneration + 1
			}
			next, err := m.cat.SwitchOwner(key, rec.TargetOwner, rec.TargetDirID, targetGen,
				migratedProjection(rec.PublishedProjection, rec.TargetDirID, targetGen))
			if err != nil {
				return fail("owner-switch", err)
			}
			result.OwnerSwitched = true
			observe(next)
		case catalog.StateOwnerSwitched:
			next, err := m.cat.Advance(key, catalog.StateQueryLeaseDraining)
			if err != nil {
				return fail("advance-lease-draining", err)
			}
			observe(next)
		case catalog.StateQueryLeaseDraining:
			if m.ops.LeaseDrain != nil {
				if err := m.ops.LeaseDrain(key, rec.MigrationFromGeneration); err != nil {
					return fail("lease-drain", err)
				}
			}
			if m.ret != nil {
				if allowed, reason := m.ret.AllowDetach(key, rec.MigrationFromDirID, rec.OwnerDirID); !allowed {
					return result, fmt.Errorf("lifecycle: retention last-copy protection: %s", reason)
				}
			}
			if m.ops.Detach != nil {
				if err := m.ops.Detach(key, rec.MigrationFromDirID); err != nil {
					return fail("detach", err)
				}
			}
			next, err := m.cat.Advance(key, catalog.StateDetached)
			if err != nil {
				return fail("advance-detached", err)
			}
			observe(next)
		case catalog.StateDetached:
			if m.ops.Cleanup != nil && rec.MigrationFromDirID != "" {
				if err := m.ops.Cleanup(key, rec.MigrationFromDirID); err != nil {
					return fail("cleanup", err)
				}
			}
			updated := rec.Clone()
			updated.ResidualDirs = nil
			if err := m.cat.Put(updated); err != nil {
				return fail("clear-residual", err)
			}
			checksum := updated.LastVerifyChecksum
			if checksum == "" {
				return fail("journal-complete", fmt.Errorf("verification checksum missing"))
			}
			if err := m.cat.MarkJournalComplete(key, checksum); err != nil {
				return fail("journal-complete", err)
			}
			next, err := m.cat.Advance(key, catalog.StateCleaned)
			if err != nil {
				return fail("advance-cleaned", err)
			}
			observe(next)
			return result, nil
		case catalog.StateCleaned:
			return result, nil
		default:
			return result, fmt.Errorf("lifecycle: cannot resume state %s", rec.MigrationState)
		}
	}
}
