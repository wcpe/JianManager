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
		state := records[0].MigrationState
		for _, rec := range records {
			if rec.MigrationState != state {
				return fmt.Errorf("lifecycle: UTC-day state divergence")
			}
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
				snapshotID, err = m.ops.Snapshot(representative, records[0].MigrationFromDirID)
				if err != nil {
					return fail("snapshot", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateStagingVerify); err != nil {
				return fail("advance-staging-verify", err)
			}
		case catalog.StateStagingVerify:
			if !records[0].LastVerifyCompleted {
				if snapshotID == "" && m.ops.Snapshot != nil {
					var err error
					snapshotID, err = m.ops.Snapshot(representative, records[0].MigrationFromDirID)
					if err != nil {
						return fail("resume-snapshot", err)
					}
				}
				copyID := ""
				if m.ops.Copy != nil {
					var err error
					copyID, err = m.ops.Copy(representative, snapshotID, records[0].TargetDirID)
					if err != nil {
						return fail("copy", err)
					}
				}
				checksum := ""
				if m.ops.Verify != nil {
					var err error
					checksum, err = m.ops.Verify(representative, copyID, records[0].TargetDirID)
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
				if err := m.ops.Attach(representative, records[0].TargetDirID); err != nil {
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
			if _, err := m.cat.SwitchDayOwners(utcDay, records[0].TargetOwner, records[0].TargetDirID, projections); err != nil {
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
				if err := m.ops.Detach(representative, records[0].MigrationFromDirID); err != nil {
					return fail("detach", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateDetached); err != nil {
				return fail("advance-detached", err)
			}
		case catalog.StateDetached:
			if m.ops.Cleanup != nil {
				if err := m.ops.Cleanup(representative, records[0].MigrationFromDirID); err != nil {
					return fail("cleanup", err)
				}
			}
			checksum := records[0].LastVerifyChecksum
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
