package lifecycle

import (
	"errors"
	"fmt"
	"sync"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// DayManager 串行推进持久固定批次；成员转移与物理日退休是两个独立屏障。
type DayManager struct {
	cat *catalog.Catalog
	ops Ops
	ret RetentionGate
	mu  sync.Mutex
}

func NewDayManager(cat *catalog.Catalog, ops Ops, ret RetentionGate) *DayManager {
	return &DayManager{cat: cat, ops: ops, ret: ret}
}
func (m *DayManager) Start(day string, target catalog.Owner, dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.cat.BeginDayMigration(day, target, dir); err != nil {
		return err
	}
	return m.resume(day)
}
func (m *DayManager) Resume(day string) error { m.mu.Lock(); defer m.mu.Unlock(); return m.resume(day) }

func (m *DayManager) resume(utcDay string) error {
	records := m.cat.RecordsForDay(utcDay)
	if len(records) == 0 {
		return fmt.Errorf("lifecycle: UTC day %s not found", utcDay)
	}
	if _, ok := m.cat.PhysicalDay(utcDay); !ok {
		var target catalog.Owner
		var dir string
		for _, rec := range records {
			if rec.TargetOwner.Valid() && rec.TargetDirID != "" {
				target, dir = rec.TargetOwner, rec.TargetDirID
				break
			}
			if rec.Owner == catalog.OwnerCold {
				target, dir = rec.Owner, rec.OwnerDirID
			}
		}
		if dir == "" {
			return fmt.Errorf("lifecycle: physical-day migration target unavailable")
		}
		if _, err := m.cat.BeginDayMigration(utcDay, target, dir); err != nil {
			return err
		}
	}
	for rounds := 0; rounds < 128; rounds++ {
		day, _ := m.cat.PhysicalDay(utcDay)
		if len(day.Batches) == 0 {
			if !day.Frozen {
				return nil
			}
			if _, err := m.cat.BeginDayMigration(utcDay, day.TargetOwner, day.TargetDirID); err != nil {
				return err
			}
			continue
		}
		batch := *day.ActiveBatch()
		fail := func(op string, err error) error {
			if errors.Is(err, catalog.ErrWritePermitsActive) || errors.Is(err, catalog.ErrPhysicalDayNotRetirable) {
				return fmt.Errorf("lifecycle: %s: %w", op, err)
			}
			if _, journalErr := m.cat.FailDay(utcDay, catalog.StateFailedRetryable, op+": "+err.Error()); journalErr != nil {
				return fmt.Errorf("lifecycle: %s: %v; journal: %w", op, err, journalErr)
			}
			return fmt.Errorf("lifecycle: %s: %w", op, err)
		}
		if batch.State == catalog.StateFailedManual {
			return fmt.Errorf("lifecycle: manual recovery required")
		}
		if batch.State == catalog.StateFailedRetryable {
			if batch.ResumeState == "" {
				return fmt.Errorf("lifecycle: day retry state missing")
			}
			if _, err := m.cat.AdvanceDay(utcDay, batch.ResumeState); err != nil {
				return err
			}
			continue
		}
		if len(batch.Members) == 0 {
			return fail("members", fmt.Errorf("fixed batch is empty"))
		}
		representative := batch.Members[0].Key
		rep, ok := m.cat.Get(representative)
		if !ok {
			return fail("members", catalog.ErrNotFound)
		}
		switch batch.State {
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
			if m.ops.Snapshot == nil {
				return fail("snapshot", fmt.Errorf("physical snapshot operation unavailable"))
			}
			snapshot, err := m.ops.Snapshot(representative, rep.MigrationFromDirID)
			if err != nil {
				return fail("snapshot", err)
			}
			if err = m.cat.RecordDayProgress(utcDay, snapshot, "", false, nil); err != nil {
				return fail("record-snapshot", err)
			}
			if _, err = m.cat.AdvanceDay(utcDay, catalog.StateStagingVerify); err != nil {
				return fail("advance-staging-verify", err)
			}
		case catalog.StateStagingVerify:
			if !rep.LastVerifyCompleted {
				if m.ops.Copy == nil || m.ops.Verify == nil {
					return fail("copy", fmt.Errorf("physical copy and verification operations unavailable"))
				}
				snapshot := batch.SnapshotID
				if snapshot == "" {
					if m.ops.Snapshot == nil {
						return fail("snapshot", fmt.Errorf("snapshot reference unavailable"))
					}
					var err error
					snapshot, err = m.ops.Snapshot(representative, rep.MigrationFromDirID)
					if err != nil {
						return fail("snapshot", err)
					}
					if err = m.cat.RecordDayProgress(utcDay, snapshot, "", false, nil); err != nil {
						return fail("record-snapshot", err)
					}
				}
				copyID, err := m.ops.Copy(representative, snapshot, rep.TargetDirID)
				checksum := ""
				if errors.Is(err, ErrColdDayExists) {
					if m.ops.RepairDay == nil {
						return fail("logical-repair", fmt.Errorf("%w: controlled canonical repair input unavailable", ErrColdDayExists))
					}
					result, repairErr := m.ops.RepairDay(DayRepairRequest{UTCDay: utcDay, BatchID: batch.ID, TargetDirID: rep.TargetDirID, Members: batch.Members})
					if repairErr != nil {
						return fail("logical-repair", repairErr)
					}
					if result == nil || result.Checksum == "" || len(result.Projections) != len(batch.Members) {
						return fail("logical-repair", fmt.Errorf("complete receiver proof missing"))
					}
					for _, member := range batch.Members {
						if result.Projections[member.Key] == nil {
							return fail("logical-repair", fmt.Errorf("member proof missing: %s", member.Key))
						}
					}
					checksum, copyID = result.Checksum, result.CopyID
					if err = m.cat.RecordDayProgress(utcDay, snapshot, copyID, true, result.Projections); err != nil {
						return fail("record-repair", err)
					}
				} else {
					if err != nil {
						return fail("copy", err)
					}
					if err = m.cat.RecordDayProgress(utcDay, snapshot, copyID, false, nil); err != nil {
						return fail("record-copy", err)
					}
					checksum, err = m.ops.Verify(representative, copyID, rep.TargetDirID)
					if err != nil {
						return fail("verify", err)
					}
				}
				if checksum == "" {
					return fail("verify", fmt.Errorf("verification checksum missing"))
				}
				if err = m.cat.RecordDayVerification(utcDay, checksum); err != nil {
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
			projections := make(map[catalog.PartitionKey]*catalog.PublishedProjection, len(batch.Members))
			for _, member := range batch.Members {
				current, _ := m.cat.Get(member.Key)
				projection := member.PublishedProjection
				if replacement := batch.Projections[member.Key.String()]; replacement != nil {
					projection = replacement
				}
				projections[member.Key] = migratedProjection(projection, current.TargetDirID, current.TargetGeneration)
			}
			if _, err := m.cat.SwitchDayOwners(utcDay, rep.TargetOwner, rep.TargetDirID, projections); err != nil {
				return fail("owner-switch", err)
			}
		case catalog.StateOwnerSwitched:
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateQueryLeaseDraining); err != nil {
				return fail("advance-lease-draining", err)
			}
		case catalog.StateQueryLeaseDraining, catalog.StateDetached, catalog.StateCleaned:
			residual := false
			for _, rec := range m.cat.RecordsForDay(utcDay) {
				residual = residual || rec.Owner == catalog.OwnerHot
			}
			if residual {
				if _, err := m.cat.BeginResidualDayMigration(utcDay); err != nil {
					return err
				}
				continue
			}
			if batch.State == catalog.StateCleaned {
				return nil
			}
			if ok, reason := m.cat.CanRetirePhysicalDay(utcDay); !ok {
				return fmt.Errorf("%w: %s", catalog.ErrPhysicalDayNotRetirable, reason)
			}
			for _, b := range day.Batches {
				for _, member := range b.Members {
					current, _ := m.cat.Get(member.Key)
					if m.ops.LeaseDrain != nil && batch.State != catalog.StateDetached {
						if err := m.ops.LeaseDrain(member.Key, current.MigrationFromGeneration); err != nil {
							return fail("lease-drain", err)
						}
					}
					if m.ret != nil {
						if ok, reason := m.ret.AllowDetach(member.Key, current.MigrationFromDirID, current.OwnerDirID); !ok {
							return fmt.Errorf("lifecycle: retention last-copy protection: %s", reason)
						}
					}
				}
			}
			if batch.State != catalog.StateDetached {
				if m.ops.Detach != nil {
					if err := m.ops.Detach(representative, rep.MigrationFromDirID); err != nil {
						return fail("detach", err)
					}
				}
				if _, err := m.cat.AdvanceDay(utcDay, catalog.StateDetached); err != nil {
					return fail("advance-detached", err)
				}
			}
			if m.ops.Cleanup != nil {
				if err := m.ops.Cleanup(representative, rep.MigrationFromDirID); err != nil {
					return fail("cleanup", err)
				}
			}
			if err := m.cat.CompletePhysicalDay(utcDay); err != nil {
				return fail("complete-physical-day", err)
			}
			return nil
		default:
			return fmt.Errorf("lifecycle: cannot resume fixed batch state %s", batch.State)
		}
	}
	return fmt.Errorf("lifecycle: physical-day batch advancement limit reached")
}
