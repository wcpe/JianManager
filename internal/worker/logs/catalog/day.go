package catalog

import (
	"fmt"
	"sort"
	"time"
)

func (c *Catalog) dayRecordsLocked(utcDay string) ([]*Record, error) {
	var out []*Record
	for key, rec := range c.records {
		if key.UTCDay == utcDay {
			out = append(out, rec.Clone())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: UTC day %s", ErrNotFound, utcDay)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out, nil
}

// activeDayRecords 仅用于旧记录升级；新批成员始终来自持久白名单。
func activeDayRecords(records []*Record) []*Record {
	var out []*Record
	for _, rec := range records {
		if rec != nil && rec.MigrationState != "" && rec.MigrationState != StateCleaned {
			out = append(out, rec)
		}
	}
	return out
}

func (c *Catalog) ensureDayLocked(utcDay string) (*PhysicalDayMigration, error) {
	if day := c.days[utcDay]; day != nil {
		return day.Clone(), nil
	}
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return nil, err
	}
	active := activeDayRecords(records)
	if len(active) == 0 {
		return nil, ErrNotFound
	}
	rep := active[0]
	day := &PhysicalDayMigration{UTCDay: utcDay, TargetOwner: rep.TargetOwner, TargetDirID: rep.TargetDirID, Frozen: true}
	groups := make(map[string]int)
	for _, rec := range active {
		if !rec.TargetOwner.Valid() || rec.TargetDirID == "" || rec.TargetOwner != day.TargetOwner || rec.TargetDirID != day.TargetDirID {
			return nil, fmt.Errorf("%w: legacy day has conflicting migration targets", ErrInvalidState)
		}
		resume := MigrationState("")
		if rec.MigrationState == StateFailedRetryable {
			resume = c.legacyResumeStateLocked(rec.Key)
			if resume == "" {
				return nil, fmt.Errorf("%w: legacy retry state proof missing", ErrInvalidState)
			}
		}
		signature := fmt.Sprintf("%s|%s|%s|%t|%s", rec.Owner, rec.MigrationState, resume, rec.LastVerifyCompleted, rec.LastVerifyChecksum)
		index, found := groups[signature]
		if !found {
			index = len(day.Batches)
			groups[signature] = index
			day.Batches = append(day.Batches, DayMigrationBatch{ID: uint64(index + 1), State: rec.MigrationState, ResumeState: resume, Checksum: rec.LastVerifyChecksum})
		}
		day.Batches[index].Members = append(day.Batches[index].Members, authorityJournalRecord(rec))
	}
	_, err = c.commitDayLocked(day, nil, JournalEntry{Detail: "adopt legacy fixed physical-day batch"})
	return day, err
}

func (c *Catalog) legacyResumeStateLocked(key PartitionKey) MigrationState {
	entries := c.journal.Entries(key)
	fallback := MigrationState("")
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		state := MigrationState("")
		for _, record := range entry.Records {
			if record != nil && record.Key == key {
				state = record.MigrationState
				break
			}
		}
		if entry.Record != nil && entry.Record.Key == key {
			state = entry.Record.MigrationState
		} else if len(entry.Records) == 0 && entry.Key == key && state == "" {
			state = entry.State
		}
		if IsHappyPath(state) && state != StateCleaned {
			return state
		}
		if fallback == "" && IsFailure(state) && IsHappyPath(entry.FromState) && entry.FromState != StateCleaned {
			fallback = entry.FromState
		}
	}
	return fallback
}

// BeginDayMigration 先持久冻结日级接纳，再等真实在途写者完成；重调不混入新成员。
func (c *Catalog) BeginDayMigration(utcDay string, target Owner, targetDirID string) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !target.Valid() || target == OwnerHot || targetDirID == "" {
		return nil, fmt.Errorf("%w: valid non-HOT target required", ErrInvalidState)
	}
	day := c.days[utcDay].Clone()
	if day == nil {
		records, err := c.dayRecordsLocked(utcDay)
		if err != nil {
			return nil, err
		}
		if len(activeDayRecords(records)) > 0 {
			day, err = c.ensureDayLocked(utcDay)
			if err != nil {
				return nil, err
			}
		} else {
			day = &PhysicalDayMigration{UTCDay: utcDay, TargetOwner: target, TargetDirID: targetDirID, Frozen: true}
			if _, err = c.commitDayLocked(day, nil, JournalEntry{Detail: "freeze physical-day write admission"}); err != nil {
				return nil, err
			}
		}
	}
	if day.TargetOwner != target || day.TargetDirID != targetDirID {
		return nil, fmt.Errorf("%w: fixed physical-day target differs", ErrInvalidState)
	}
	if batch := day.batch(); batch != nil && batch.State != StateCleaned {
		return c.batchRecordsLocked(day)
	}
	return c.beginResidualLocked(day)
}

func (c *Catalog) beginResidualLocked(day *PhysicalDayMigration) ([]*Record, error) {
	if !day.Frozen {
		day.Frozen = true
		if _, err := c.commitDayLocked(day, nil, JournalEntry{Detail: "freeze residual physical-day admission"}); err != nil {
			return nil, err
		}
	}
	if c.dayPermitCountLocked(day.UTCDay) > 0 {
		return nil, ErrWritePermitsActive
	}
	records, err := c.dayRecordsLocked(day.UTCDay)
	if err != nil {
		return nil, err
	}
	batch := DayMigrationBatch{ID: uint64(len(day.Batches) + 1), State: StateRoutingFrozen}
	var changed []*Record
	for _, rec := range records {
		if rec.Owner != OwnerHot {
			continue
		}
		if rec.MigrationState != "" && rec.MigrationState != StateCleaned {
			return nil, fmt.Errorf("%w: HOT member already belongs to a batch", ErrInvalidState)
		}
		batch.Members = append(batch.Members, authorityJournalRecord(rec))
		if err := BeginMigration(rec, day.TargetOwner, day.TargetDirID); err != nil {
			return nil, err
		}
		changed = append(changed, rec)
	}
	if len(changed) == 0 {
		if len(day.Batches) == 0 {
			day.Frozen = false
			if _, err := c.commitDayLocked(day, nil, JournalEntry{Detail: "stable non-HOT physical day requires no migration"}); err != nil {
				return nil, err
			}
		}
		return records, nil
	}
	day.Batches = append(day.Batches, batch)
	return c.commitDayLocked(day, changed, JournalEntry{Key: changed[0].Key, State: StateRoutingFrozen, Detail: "begin fixed physical-day migration batch"})
}

// BeginResidualDayMigration 只在前批已转移责任后为遗留 HOT 创建另一个固定批次。
func (c *Catalog) BeginResidualDayMigration(utcDay string) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	day, err := c.ensureDayLocked(utcDay)
	if err != nil {
		return nil, err
	}
	if batch := day.batch(); batch != nil {
		state := batch.State
		if state == StateFailedRetryable {
			state = batch.ResumeState
		}
		rank, err := HappyIndex(state)
		if err != nil || rank < happyIndex[StateOwnerSwitched] {
			return nil, fmt.Errorf("%w: previous batch has not transferred responsibility", ErrInvalidState)
		}
	}
	return c.beginResidualLocked(day)
}

func (c *Catalog) AdvanceDay(utcDay string, to MigrationState) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	day, err := c.ensureDayLocked(utcDay)
	if err != nil {
		return nil, err
	}
	batch := day.batch()
	if batch == nil {
		return nil, ErrWritePermitsActive
	}
	records, err := c.batchRecordsLocked(day)
	if err != nil {
		return nil, err
	}
	if to == StateOwnerSwitched {
		if batch.State != StateFailedRetryable || batch.ResumeState != StateOwnerSwitched {
			return nil, fmt.Errorf("%w: use SwitchDayOwners for initial authority switch", ErrIllegalTransition)
		}
		for _, rec := range records {
			if rec.Owner != day.TargetOwner || rec.OwnerDirID != day.TargetDirID || rec.Generation != rec.TargetGeneration || rec.PublishedProjection == nil || rec.PublishedProjection.QueryGeneration != rec.Generation || rec.PublishedProjection.QueryLocationDirID != rec.OwnerDirID || !rec.LastVerifyCompleted || rec.LastVerifyChecksum == "" {
				return nil, fmt.Errorf("%w: committed authority proof missing", ErrInvalidState)
			}
		}
	}
	if to == StateSnapshotting && c.dayPermitCountLocked(utcDay) > 0 {
		return nil, ErrWritePermitsActive
	}
	if to == StateDetached {
		if ok, reason := c.canRetireDayLocked(utcDay, time.Now().UTC()); !ok {
			return nil, fmt.Errorf("%w: %s", ErrPhysicalDayNotRetirable, reason)
		}
	}
	from := batch.State
	if to == StateCleaned {
		for _, rec := range records {
			if !rec.JournalComplete || !rec.LastVerifyCompleted || len(rec.ResidualDirs) != 0 {
				return nil, fmt.Errorf("%w: cleanup and journal proof required", ErrInvalidState)
			}
		}
	}
	for _, rec := range records {
		if rec.MigrationState != from {
			return nil, fmt.Errorf("%w: fixed batch has divergent states", ErrInvalidState)
		}
		if err := Transition(rec, to); err != nil {
			return nil, err
		}
		rec.RecoveryRequired = false
	}
	batch.State = to
	if to == StateCleaned {
		complete := true
		for _, b := range day.Batches {
			complete = complete && b.State == StateCleaned
		}
		if complete {
			day.Frozen = false
		}
	}
	return c.commitDayLocked(day, records, JournalEntry{Key: records[0].Key, State: to, FromState: from, Detail: "advance fixed physical-day batch"})
}

func (c *Catalog) RecordDayVerification(utcDay, checksum string) error {
	if checksum == "" {
		return fmt.Errorf("%w: checksum required", ErrInvalidState)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	day, err := c.ensureDayLocked(utcDay)
	if err != nil {
		return err
	}
	records, err := c.batchRecordsLocked(day)
	if err != nil {
		return err
	}
	if day.batch() == nil {
		return ErrInvalidState
	}
	day.batch().Checksum = checksum
	for _, rec := range records {
		rec.LastVerifyCompleted = true
		rec.LastVerifyChecksum = checksum
	}
	_, err = c.commitDayLocked(day, records, JournalEntry{Key: records[0].Key, State: day.batch().State, Detail: "fixed physical-day batch verified"})
	return err
}

func (c *Catalog) SwitchDayOwners(utcDay string, target Owner, targetDirID string, projections map[PartitionKey]*PublishedProjection) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	day, err := c.ensureDayLocked(utcDay)
	if err != nil {
		return nil, err
	}
	if day.TargetOwner != target || day.TargetDirID != targetDirID {
		return nil, ErrInvalidState
	}
	records, err := c.batchRecordsLocked(day)
	if err != nil {
		return nil, err
	}
	if day.batch() == nil {
		return nil, ErrInvalidState
	}
	for _, rec := range records {
		if !rec.LastVerifyCompleted || rec.LastVerifyChecksum == "" {
			return nil, fmt.Errorf("%w: member was not verified", ErrInvalidState)
		}
		if err := ApplyOwnerSwitch(rec, target, targetDirID, rec.TargetGeneration, projections[rec.Key]); err != nil {
			return nil, err
		}
	}
	day.batch().State = StateOwnerSwitched
	return c.commitDayLocked(day, records, JournalEntry{Key: records[0].Key, State: StateOwnerSwitched, FromState: StateAttachedStaging, AuthorityCommit: true, Detail: "fixed physical-day owner switch"})
}

func (c *Catalog) ClearDayResiduals(utcDay string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	day, err := c.ensureDayLocked(utcDay)
	if err != nil {
		return err
	}
	if ok, reason := c.canRetireDayLocked(utcDay, time.Now().UTC()); !ok {
		return fmt.Errorf("%w: %s", ErrPhysicalDayNotRetirable, reason)
	}
	records, err := c.batchRecordsLocked(day)
	if err != nil {
		return err
	}
	for _, rec := range records {
		rec.ResidualDirs = nil
	}
	_, err = c.commitDayLocked(day, records, JournalEntry{Key: records[0].Key, Detail: "clear fixed-batch retired directories"})
	return err
}

func (c *Catalog) MarkDayComplete(utcDay, checksum string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, rec := range c.records {
		if key.UTCDay == utcDay && rec.Owner == OwnerHot {
			return fmt.Errorf("%w: HOT authority remains", ErrIllegalTransition)
		}
	}
	day, err := c.ensureDayLocked(utcDay)
	if err != nil {
		return err
	}
	if ok, reason := c.canRetireDayLocked(utcDay, time.Now().UTC()); !ok {
		return fmt.Errorf("%w: %s", ErrIllegalTransition, reason)
	}
	records, err := c.batchRecordsLocked(day)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if !rec.LastVerifyCompleted || rec.LastVerifyChecksum == "" || checksum == "" {
			return ErrInvalidState
		}
		rec.JournalComplete = true
	}
	_, err = c.commitDayLocked(day, records, JournalEntry{Key: records[0].Key, JournalComplete: true, Detail: "fixed-batch journal complete"})
	return err
}

func (c *Catalog) FailDay(utcDay string, to MigrationState, detail string) ([]*Record, error) {
	if !IsFailure(to) {
		return nil, ErrIllegalTransition
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	day, err := c.ensureDayLocked(utcDay)
	if err != nil {
		return nil, err
	}
	batch := day.batch()
	if batch == nil {
		return nil, ErrInvalidState
	}
	records, err := c.batchRecordsLocked(day)
	if err != nil {
		return nil, err
	}
	from := batch.State
	if from == to {
		return records, nil
	}
	for _, rec := range records {
		if err := Transition(rec, to); err != nil {
			return nil, err
		}
		rec.RecoveryRequired = true
	}
	if from != StateFailedRetryable {
		batch.ResumeState = from
	}
	batch.State = to
	return c.commitDayLocked(day, records, JournalEntry{Key: records[0].Key, State: to, FromState: from, Detail: detail})
}

// CompletePhysicalDay 只在全日屏障通过且物理清理成功后一次结清所有已转移批次。
func (c *Catalog) CompletePhysicalDay(utcDay string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	day, err := c.ensureDayLocked(utcDay)
	if err != nil {
		return err
	}
	if ok, reason := c.canRetireDayLocked(utcDay, time.Now().UTC()); !ok {
		return fmt.Errorf("%w: %s", ErrPhysicalDayNotRetirable, reason)
	}
	var records []*Record
	seen := map[PartitionKey]bool{}
	for i := range day.Batches {
		batch := &day.Batches[i]
		if batch.Checksum == "" {
			return ErrInvalidState
		}
		for _, member := range batch.Members {
			if seen[member.Key] {
				continue
			}
			seen[member.Key] = true
			rec := c.records[member.Key].Clone()
			if rec.Owner == OwnerHot {
				return ErrPhysicalDayNotRetirable
			}
			rec.MigrationState = StateCleaned
			rec.JournalComplete = true
			rec.RecoveryRequired = false
			rec.ResidualDirs = nil
			records = append(records, rec)
		}
		batch.State = StateCleaned
	}
	day.Frozen = false
	_, err = c.commitDayLocked(day, records, JournalEntry{Detail: "complete all transferred physical-day batches"})
	return err
}
