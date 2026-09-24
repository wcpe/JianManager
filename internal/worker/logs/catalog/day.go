package catalog

import (
	"fmt"
	"sort"
	"time"
)

func (c *Catalog) dayRecordsLocked(utcDay string) ([]*Record, error) {
	var records []*Record
	for key, rec := range c.records {
		if key.UTCDay == utcDay {
			records = append(records, rec.Clone())
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: UTC day %s", ErrNotFound, utcDay)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Key.String() < records[j].Key.String() })
	return records, nil
}

func (c *Catalog) BeginDayMigration(utcDay string, target Owner, targetDirID string) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		if rec.Owner != OwnerHot {
			return nil, fmt.Errorf("%w: %s owner=%s", ErrIllegalTransition, rec.Key, rec.Owner)
		}
		if err := BeginMigration(rec, target, targetDirID); err != nil {
			return nil, err
		}
	}
	return c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: StateRoutingFrozen,
		Detail: "begin physical UTC-day migration", AtUnixMilli: time.Now().UnixMilli()})
}

func (c *Catalog) AdvanceDay(utcDay string, to MigrationState) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return nil, err
	}
	from := records[0].MigrationState
	for _, rec := range records {
		if rec.MigrationState != from {
			return nil, fmt.Errorf("%w: UTC-day records have divergent states", ErrInvalidState)
		}
		if err := Transition(rec, to); err != nil {
			return nil, err
		}
		if to == StateSnapshotting && rec.TargetGeneration == 0 {
			rec.TargetGeneration = rec.MigrationFromGeneration + 1
		}
	}
	return c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: to, FromState: from,
		Detail: "advance physical UTC-day migration", AtUnixMilli: time.Now().UnixMilli()})
}

func (c *Catalog) RecordDayVerification(utcDay, checksum string) error {
	if checksum == "" {
		return fmt.Errorf("%w: verification checksum required", ErrInvalidState)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return err
	}
	for _, rec := range records {
		rec.LastVerifyCompleted = true
		rec.LastVerifyChecksum = checksum
	}
	_, err = c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: records[0].MigrationState,
		Detail: "UTC-day staging copy verified", AtUnixMilli: time.Now().UnixMilli()})
	return err
}

func (c *Catalog) SwitchDayOwners(utcDay string, target Owner, targetDirID string, projections map[PartitionKey]*PublishedProjection) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		projection := projections[rec.Key]
		generation := rec.TargetGeneration
		if generation == 0 {
			generation = rec.MigrationFromGeneration + 1
		}
		if err := ApplyOwnerSwitch(rec, target, targetDirID, generation, projection); err != nil {
			return nil, err
		}
	}
	return c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: StateOwnerSwitched,
		FromState: StateAttachedStaging, AuthorityCommit: true,
		Detail: "UTC-day owner-switched atomic commit", AtUnixMilli: time.Now().UnixMilli()})
}

func (c *Catalog) ClearDayResiduals(utcDay string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return err
	}
	for _, rec := range records {
		rec.ResidualDirs = nil
	}
	_, err = c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: records[0].MigrationState,
		Detail: "UTC-day residual directories cleared", AtUnixMilli: time.Now().UnixMilli()})
	return err
}

func (c *Catalog) MarkDayComplete(utcDay, checksum string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return err
	}
	for _, rec := range records {
		rec.JournalComplete = true
		rec.LastVerifyCompleted = true
		rec.LastVerifyChecksum = checksum
	}
	_, err = c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: records[0].MigrationState,
		JournalComplete: true, Detail: "UTC-day journal complete", AtUnixMilli: time.Now().UnixMilli()})
	return err
}

func (c *Catalog) FailDay(utcDay string, to MigrationState, detail string) ([]*Record, error) {
	if to != StateFailedRetryable && to != StateFailedManual {
		return nil, fmt.Errorf("%w: invalid day failure state", ErrIllegalTransition)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return nil, err
	}
	from := records[0].MigrationState
	for _, rec := range records {
		if rec.MigrationState != from {
			return nil, fmt.Errorf("%w: UTC-day records have divergent states", ErrInvalidState)
		}
		if err := Transition(rec, to); err != nil {
			return nil, err
		}
		rec.RecoveryRequired = true
	}
	return c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: to, FromState: from,
		Detail: detail, AtUnixMilli: time.Now().UnixMilli()})
}
