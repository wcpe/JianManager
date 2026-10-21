package lifecycle

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// DayManager migrates the physical UTC-day partition shared by every logical
// storage namespace. Catalog changes use one batch journal entry per state.
type DayManager struct {
	cat *catalog.Catalog
	ops Ops
	ret RetentionGate
}

func NewDayManager(cat *catalog.Catalog, ops Ops, ret RetentionGate) *DayManager {
	return &DayManager{cat: cat, ops: ops, ret: ret}
}

func (m *DayManager) Start(utcDay string, target catalog.Owner, targetDirID string) error {
	if _, err := m.cat.BeginDayMigration(utcDay, target, targetDirID); err != nil {
		return err
	}
	return m.Resume(utcDay)
}

func (m *DayManager) Resume(utcDay string) error {
	records := m.cat.RecordsForDay(utcDay)
	if len(records) == 0 {
		return fmt.Errorf("lifecycle: UTC day %s not found", utcDay)
	}
	if records[0].MigrationState == catalog.StateFailedManual {
		return fmt.Errorf("lifecycle: manual recovery required")
	}
	if records[0].MigrationState == catalog.StateFailedRetryable {
		resume := catalog.MigrationState("")
		entries := m.cat.Journal().Entries(records[0].Key)
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].State == catalog.StateFailedRetryable {
				resume = entries[i].FromState
				break
			}
		}
		if resume == "" {
			return fmt.Errorf("lifecycle: day retry state missing")
		}
		if _, err := m.cat.AdvanceDay(utcDay, resume); err != nil {
			return err
		}
	}
	representative := records[0].Key
	fail := func(op string, err error) error {
		_, journalErr := m.cat.FailDay(utcDay, catalog.StateFailedRetryable, op+": "+err.Error())
		if journalErr != nil {
			return fmt.Errorf("lifecycle: %s failed: %v; journal: %v", op, err, journalErr)
		}
		return fmt.Errorf("lifecycle: %s failed: %w", op, err)
	}
	var snapshotID string
	for {
		records = m.cat.RecordsForDay(utcDay)
		// **只在「迁移中」的子集内要求状态一致**（2026-10-22 复审）。
		//
		// 旧实现取 `records[0].MigrationState` 当基准并要求该日**全部**记录一致。
		// 而混合态一定会发生：某日整体下沉到冷层后，迟到的写入或新出现的 namespace
		// 仍会在同一天新建一条 Owner=hot 的记录（migrate_target.go 的注释自己承认）。
		// 于是一旦 `BeginDayMigration` 成功、`Resume` 立刻在这里报 state divergence，
		// 且**不写失败 journal** ⇒ 该日 hot 分区停在 ROUTING_FROZEN、写入改走 late 目标、
		// 永不切换，比「直接拒绝」更糟（复审 P0-2）。
		//
		// 判据：已 CLEANED/空态 = 不参与本次迁移（早已下沉完成）；其余必须在同一状态。
		state := catalog.MigrationState("")
		var rep *catalog.Record
		for _, rec := range records {
			if rec.MigrationState == "" || rec.MigrationState == catalog.StateCleaned {
				continue
			}
			if state == "" {
				state = rec.MigrationState
				representative = rec.Key
				rep = rec
				continue
			}
			if rec.MigrationState != state {
				return fmt.Errorf("lifecycle: UTC-day state divergence")
			}
		}
		if state == "" {
			// 该日已无迁移中的记录：视为已完成（幂等）。
			return nil
		}
		// **rep 必须与 state 同源取自活跃子集**（2026-10-22 复审 P1-1）。
		//
		// 下面的所有目录/校验和/目标都必须读 `rep`，绝不能退回 `records[0]`：
		// `records[0]` 是按 key 排序的第一条，混合态日里它很可能是一条已 CLEANED 的记录，
		// 而它的 `LastVerifyCompleted` 在 MarkDayComplete 时已被置 true ⇒
		// `if !rep.LastVerifyCompleted` 恒假 ⇒ 整段 snapshot/copy/verify 被跳过，
		// 直接推进到 ATTACHED_STAGING ⇒ 新 namespace 的当日数据从未拷到冷层，
		// 却被标成「已迁移」并清理旧目录（与本次修复要消灭的丢数据同形）。
		//
		// 这条路径在混合态判定统一之前是**不可达**的（会先报 divergence 退出），
		// 所以必须与判定统一一起改：否则是把「响亮失败」换成「静默丢数据」。
		if rep == nil {
			// 理论上不可达（state != "" 意味着 loop 里赋过值）；显式失败胜过空指针。
			return fail("locate-representative", fmt.Errorf("lifecycle: no active record for %s", utcDay))
		}
		switch state {
		case catalog.StateRoutingFrozen:
			if m.ops.Drain != nil {
				if err := m.ops.Drain(representative); err != nil {
					return fail("drain", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateDraining); err != nil {
				return fail("advance-draining", err)
			}
		case catalog.StateDraining:
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateSnapshotting); err != nil {
				return fail("advance-snapshotting", err)
			}
		case catalog.StateSnapshotting:
			if m.ops.Snapshot != nil {
				var err error
				snapshotID, err = m.ops.Snapshot(representative, rep.MigrationFromDirID)
				if err != nil {
					return fail("snapshot", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateStagingVerify); err != nil {
				return fail("advance-staging-verify", err)
			}
		case catalog.StateStagingVerify:
			if !rep.LastVerifyCompleted {
				if snapshotID == "" && m.ops.Snapshot != nil {
					var err error
					snapshotID, err = m.ops.Snapshot(representative, rep.MigrationFromDirID)
					if err != nil {
						return fail("resume-snapshot", err)
					}
				}
				copyID := ""
				if m.ops.Copy != nil {
					var err error
					copyID, err = m.ops.Copy(representative, snapshotID, rep.TargetDirID)
					if err != nil {
						return fail("copy", err)
					}
				}
				checksum := ""
				if m.ops.Verify != nil {
					var err error
					checksum, err = m.ops.Verify(representative, copyID, rep.TargetDirID)
					if err != nil {
						return fail("verify", err)
					}
				}
				if checksum == "" {
					return fail("verify", fmt.Errorf("verification checksum missing"))
				}
				if err := m.cat.RecordDayVerification(utcDay, checksum); err != nil {
					return fail("record-verification", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateAttachedStaging); err != nil {
				return fail("advance-attached-staging", err)
			}
		case catalog.StateAttachedStaging:
			if m.ops.Attach != nil {
				if err := m.ops.Attach(representative, rep.TargetDirID); err != nil {
					return fail("attach", err)
				}
			}
			projections := make(map[catalog.PartitionKey]*catalog.PublishedProjection, len(records))
			for _, rec := range records {
				generation := rec.TargetGeneration
				if generation == 0 {
					generation = rec.MigrationFromGeneration + 1
				}
				projections[rec.Key] = migratedProjection(rec.PublishedProjection, rec.TargetDirID, generation)
			}
			if _, err := m.cat.SwitchDayOwners(utcDay, rep.TargetOwner, rep.TargetDirID, projections); err != nil {
				return fail("owner-switch", err)
			}
		case catalog.StateOwnerSwitched:
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateQueryLeaseDraining); err != nil {
				return fail("advance-lease-draining", err)
			}
		case catalog.StateQueryLeaseDraining:
			for _, rec := range records {
				if m.ops.LeaseDrain != nil {
					if err := m.ops.LeaseDrain(rec.Key, rec.MigrationFromGeneration); err != nil {
						return fail("lease-drain", err)
					}
				}
				if m.ret != nil {
					if ok, reason := m.ret.AllowDetach(rec.Key, rec.MigrationFromDirID, rec.OwnerDirID); !ok {
						return fmt.Errorf("lifecycle: retention last-copy protection: %s", reason)
					}
				}
			}
			if m.ops.Detach != nil {
				if err := m.ops.Detach(representative, rep.MigrationFromDirID); err != nil {
					return fail("detach", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateDetached); err != nil {
				return fail("advance-detached", err)
			}
		case catalog.StateDetached:
			if m.ops.Cleanup != nil {
				if err := m.ops.Cleanup(representative, rep.MigrationFromDirID); err != nil {
					return fail("cleanup", err)
				}
			}
			checksum := rep.LastVerifyChecksum
			if checksum == "" {
				return fail("journal-complete", fmt.Errorf("verification checksum missing"))
			}
			if err := m.cat.ClearDayResiduals(utcDay); err != nil {
				return fail("clear-residual", err)
			}
			if err := m.cat.MarkDayComplete(utcDay, checksum); err != nil {
				return fail("journal-complete", err)
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateCleaned); err != nil {
				return fail("advance-cleaned", err)
			}
			return nil
		case catalog.StateCleaned:
			return nil
		default:
			return fmt.Errorf("lifecycle: cannot resume UTC-day state %s", state)
		}
	}
}
