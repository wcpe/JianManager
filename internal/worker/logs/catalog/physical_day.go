package catalog

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

var (
	ErrPhysicalDayFrozen       = errors.New("catalog: physical UTC day admission frozen")
	ErrWritePermitsActive      = errors.New("catalog: physical writes still in flight")
	ErrPhysicalDayNotRetirable = errors.New("catalog: physical UTC day cannot retire")
)

// DayMigrationBatch 固定同批成员及输入投影，进度随同状态原子落盘。
type DayMigrationBatch struct {
	ID            uint64                          `json:"id"`
	Members       []*Record                       `json:"members"`
	State         MigrationState                  `json:"state"`
	ResumeState   MigrationState                  `json:"resume_state,omitempty"`
	SnapshotID    string                          `json:"snapshot_id,omitempty"`
	CopyID        string                          `json:"copy_id,omitempty"`
	Checksum      string                          `json:"checksum,omitempty"`
	LogicalRepair bool                            `json:"logical_repair,omitempty"`
	Reattached    bool                            `json:"reattached,omitempty"`
	Projections   map[string]*PublishedProjection `json:"projections,omitempty"`
	RepairSources []DayRepairSource               `json:"repair_sources,omitempty"`
}

// DayRepairSource 保存独立物理代次和精确写入进度，不把未知响应当成可重放写入。
type DayRepairSource struct {
	Key                  string `json:"key"`
	InputDigest          string `json:"input_digest"`
	ProjectionGeneration string `json:"projection_generation"`
	Events               int    `json:"events"`
	Bytes                int64  `json:"bytes"`
	AttemptedThrough     int    `json:"attempted_through"`
	WrittenThrough       int    `json:"written_through"`
	Verified             bool   `json:"verified"`
}

// PhysicalDayMigration 的冻结先于记录变更；在途写者仍可按旧 CAS 完成发布。
type PhysicalDayMigration struct {
	UTCDay      string              `json:"utc_day"`
	TargetOwner Owner               `json:"target_owner"`
	TargetDirID string              `json:"target_dir_id"`
	Frozen      bool                `json:"frozen"`
	Batches     []DayMigrationBatch `json:"batches,omitempty"`
}

func (d *PhysicalDayMigration) Clone() *PhysicalDayMigration {
	if d == nil {
		return nil
	}
	out := *d
	out.Batches = append([]DayMigrationBatch(nil), d.Batches...)
	for i := range out.Batches {
		batch := &out.Batches[i]
		batch.RepairSources = append([]DayRepairSource(nil), d.Batches[i].RepairSources...)
		batch.Members = make([]*Record, len(d.Batches[i].Members))
		for j, rec := range d.Batches[i].Members {
			batch.Members[j] = authorityJournalRecord(rec)
		}
		if batch.Projections != nil {
			batch.Projections = make(map[string]*PublishedProjection, len(d.Batches[i].Projections))
			for key, pp := range d.Batches[i].Projections {
				batch.Projections[key] = (&Record{PublishedProjection: pp}).Clone().PublishedProjection
			}
		}
	}
	return &out
}

func (d *PhysicalDayMigration) batch() *DayMigrationBatch {
	if d == nil || len(d.Batches) == 0 {
		return nil
	}
	for i := range d.Batches {
		b := &d.Batches[i]
		if b.State == StateFailedManual {
			return b
		}
		state := b.State
		if state == StateFailedRetryable {
			state = b.ResumeState
		}
		if state == StateOwnerSwitched {
			return b
		}
	}
	for i := range d.Batches {
		b := &d.Batches[i]
		state := b.State
		if state == StateFailedRetryable {
			state = b.ResumeState
		}
		if rank, ok := happyIndex[state]; ok && rank < happyIndex[StateOwnerSwitched] {
			return b
		}
	}
	for i := range d.Batches {
		if d.Batches[i].State == StateFailedRetryable {
			return &d.Batches[i]
		}
	}
	for i := len(d.Batches) - 1; i >= 0; i-- {
		if d.Batches[i].State != StateCleaned {
			return &d.Batches[i]
		}
	}
	return &d.Batches[len(d.Batches)-1]
}

// ActiveBatch 先推进未转移责任的固定批次，再共同退休物理日；返回所属快照内的批次。
func (d *PhysicalDayMigration) ActiveBatch() *DayMigrationBatch { return d.batch() }

func (c *Catalog) PhysicalDay(utcDay string) (*PhysicalDayMigration, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.days[utcDay]
	return d.Clone(), ok
}

// PendingPhysicalDays 仅复制未完成责任的日期，不深拷贝投影；人工和未知状态不自动接管。
func (c *Catalog) PendingPhysicalDays() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pending := map[string]bool{}
	blocked := map[string]bool{}
	for key, rec := range c.records {
		if rec.MigrationState == StateFailedManual || (rec.MigrationState != "" && !IsHappyPath(rec.MigrationState) && !IsFailure(rec.MigrationState)) {
			blocked[key.UTCDay] = true
		}
		if rec.MigrationState != "" && rec.MigrationState != StateCleaned {
			pending[key.UTCDay] = true
		}
	}
	for day, state := range c.days {
		if state.Frozen {
			pending[day] = true
		}
	}
	out := make([]string, 0, len(pending))
	for day := range pending {
		if !blocked[day] {
			out = append(out, day)
		}
	}
	sort.Strings(out)
	return out
}

// PhysicalDayMigrations 返回仍冻结或未完成的持久日批次，包含尚未捕获成员的冻结。
func (c *Catalog) PhysicalDayMigrations() []*PhysicalDayMigration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*PhysicalDayMigration
	for _, day := range c.days {
		if day.Frozen {
			out = append(out, day.Clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UTCDay < out[j].UTCDay })
	return out
}

func (c *Catalog) commitDayLocked(day *PhysicalDayMigration, records []*Record, entry JournalEntry) ([]*Record, error) {
	entry.DayMigration = day.Clone()
	if len(records) == 0 {
		if err := c.journal.Append(entry); err != nil {
			return nil, err
		}
		c.days[day.UTCDay] = day
		return nil, nil
	}
	out, err := c.commitBatchLocked(records, entry)
	if err == nil {
		c.days[day.UTCDay] = day
	}
	return out, err
}

func (c *Catalog) dayPermitCountLocked(utcDay string) int {
	n := 0
	for _, p := range c.permits {
		if p.Key.UTCDay == utcDay {
			n++
		}
	}
	return n
}

// WritePermit 保活一次物理写、核验和发布的全部阶段；只能由所属 Catalog 释放。
type WritePermit struct {
	Key        PartitionKey
	Owner      Owner
	DirID      string
	Generation uint64
	id         uint64
	catalog    *Catalog
}

func (c *Catalog) writeAdmissionLocked(key PartitionKey) (WriteTarget, error) {
	if day := c.days[key.UTCDay]; day != nil && day.Frozen {
		return WriteTarget{}, ErrPhysicalDayFrozen
	}
	if rec := c.records[key]; rec != nil {
		if rec.WriteRoute.Frozen {
			return WriteTarget{}, ErrPhysicalDayFrozen
		}
		if rec.RecoveryRequired {
			return WriteTarget{}, fmt.Errorf("%w: recovery required", ErrInvalidState)
		}
		target := OwnerForWrite(rec)
		if !target.OK {
			return target, fmt.Errorf("%w: %s", ErrInvalidState, target.Reason)
		}
		return target, nil
	}
	if day := c.days[key.UTCDay]; day != nil && day.TargetOwner.Valid() {
		return WriteTarget{OK: true, Owner: day.TargetOwner, DirID: day.TargetDirID, Generation: 1, Reason: "physical-day-owner"}, nil
	}
	for other, rec := range c.records {
		if other.UTCDay == key.UTCDay && rec.Owner == OwnerCold {
			return WriteTarget{OK: true, Owner: rec.Owner, DirID: rec.OwnerDirID, Generation: 1, Reason: "physical-day-owner"}, nil
		}
	}
	return WriteTarget{OK: true, Owner: OwnerHot, Generation: 1, Reason: "new-physical-day"}, nil
}

// WriteAdmission 仅供路由预检，实际写入必须同时取得许可。
func (c *Catalog) WriteAdmission(key PartitionKey) (WriteTarget, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.writeAdmissionLocked(key)
}

func (c *Catalog) AcquireWritePermit(key PartitionKey) (*WritePermit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	target, err := c.writeAdmissionLocked(key)
	if err != nil {
		return nil, err
	}
	c.nextPermit++
	permit := &WritePermit{Key: key, Owner: target.Owner, DirID: target.DirID, Generation: target.Generation, id: c.nextPermit, catalog: c}
	c.permits[permit.id] = permit
	return permit, nil
}
func (c *Catalog) ReleaseWritePermit(permit *WritePermit) {
	if permit == nil || permit.catalog != c {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.permits[permit.id] == permit {
		delete(c.permits, permit.id)
	}
}

// CheckPhysicalDayDrained 证明已冻结接纳且没有尚未结束的物理写者。
func (c *Catalog) CheckPhysicalDayDrained(utcDay string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	day := c.days[utcDay]
	if day == nil || !day.Frozen {
		return ErrPhysicalDayFrozen
	}
	if c.dayPermitCountLocked(utcDay) > 0 {
		return ErrWritePermitsActive
	}
	return nil
}

// CanRetirePhysicalDay 同时核查所有 namespace 的权威、接收证明、写许可和旧读取。
func (c *Catalog) CanRetirePhysicalDay(utcDay string) (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.canRetireDayLocked(utcDay, time.Now().UTC())
}
func (c *Catalog) canRetireDayLocked(utcDay string, now time.Time) (bool, string) {
	day := c.days[utcDay]
	if day == nil || !day.Frozen {
		return false, "physical admission is not frozen"
	}
	if c.dayPermitCountLocked(utcDay) > 0 {
		return false, "physical writes are still in flight"
	}
	for key, rec := range c.records {
		if key.UTCDay != utcDay {
			continue
		}
		if rec.Owner == OwnerHot {
			return false, "HOT authority remains: " + key.String()
		}
		if rec.MigrationFromOwner == OwnerHot && (!rec.LastVerifyCompleted || rec.LastVerifyChecksum == "") {
			return false, "receiver responsibility is unverified: " + key.String()
		}
		for _, lease := range rec.QueryLeases {
			if lease.Owner != OwnerHot && lease.Generation != rec.MigrationFromGeneration {
				continue
			}
			if lease.InvalidReason == "" && (lease.ExpiresAt.IsZero() || now.Before(lease.ExpiresAt)) {
				return false, "old query lease is active"
			}
		}
	}
	for _, view := range c.views {
		for _, snap := range view.snapshots {
			if snap.Key.UTCDay == utcDay && snap.Owner == OwnerHot && (view.pins > 0 || (view.invalid == "" && now.Before(snap.ExpiresAt))) {
				return false, "old query view or read pin is active"
			}
		}
	}
	return true, "all physical-day references transferred"
}

func (c *Catalog) batchRecordsLocked(day *PhysicalDayMigration) ([]*Record, error) {
	batch := day.batch()
	if batch == nil {
		return nil, nil
	}
	out := make([]*Record, 0, len(batch.Members))
	for _, member := range batch.Members {
		rec := c.records[member.Key]
		if rec == nil {
			return nil, fmt.Errorf("%w: batch member %s disappeared", ErrInvalidState, member.Key)
		}
		out = append(out, rec.Clone())
	}
	return out, nil
}

// RecordDayReattached 记录只针对当前固定残留批次的物理源重新挂载。
func (c *Catalog) RecordDayReattached(utcDay string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	day := c.days[utcDay].Clone()
	if day == nil || day.batch() == nil || !day.Frozen {
		return ErrInvalidState
	}
	day.batch().Reattached = true
	_, err := c.commitDayLocked(day, nil, JournalEntry{Detail: "reattach Catalog-authorized HOT residual batch"})
	return err
}

// PlanDayRepair 首次落盘固定输入及代次，后续只允许逐字段一致的原计划。
func (c *Catalog) PlanDayRepair(utcDay string, batchID uint64, plans []DayRepairSource) ([]DayRepairSource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	day := c.days[utcDay].Clone()
	if day == nil || day.batch() == nil || day.batch().ID != batchID {
		return nil, ErrInvalidState
	}
	existing := day.batch().RepairSources
	if len(existing) > 0 {
		if len(existing) != len(plans) {
			return nil, fmt.Errorf("%w: canonical repair source set changed", ErrInvalidState)
		}
		for i, p := range plans {
			old := existing[i]
			if old.Key != p.Key || old.InputDigest != p.InputDigest || old.ProjectionGeneration != p.ProjectionGeneration || old.Events != p.Events || old.Bytes != p.Bytes {
				return nil, fmt.Errorf("%w: canonical repair input changed", ErrInvalidState)
			}
		}
		return append([]DayRepairSource(nil), existing...), nil
	}
	day.batch().RepairSources = append([]DayRepairSource(nil), plans...)
	if _, err := c.commitDayLocked(day, nil, JournalEntry{Detail: "freeze canonical repair input and physical generations"}); err != nil {
		return nil, err
	}
	return append([]DayRepairSource(nil), plans...), nil
}

func (c *Catalog) RecordDayRepairProgress(utcDay string, batchID uint64, source string, attempted, written int, verified bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	day := c.days[utcDay].Clone()
	if day == nil || day.batch() == nil || day.batch().ID != batchID {
		return ErrInvalidState
	}
	for i := range day.batch().RepairSources {
		p := &day.batch().RepairSources[i]
		if p.Key != source {
			continue
		}
		if attempted < p.AttemptedThrough || written < p.WrittenThrough || written > attempted || attempted > p.Events {
			return ErrInvalidState
		}
		p.AttemptedThrough = attempted
		p.WrittenThrough = written
		p.Verified = p.Verified || verified
		_, err := c.commitDayLocked(day, nil, JournalEntry{Detail: "canonical repair write and verification progress"})
		return err
	}
	return ErrNotFound
}

// RecordDayProgress 在任何下一状态提交前持久化可恢复的物理句柄和受控补录投影。
func (c *Catalog) RecordDayProgress(utcDay, snapshotID, copyID string, logical bool, projections map[PartitionKey]*PublishedProjection) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	day := c.days[utcDay].Clone()
	if day == nil || day.batch() == nil {
		return ErrNotFound
	}
	b := day.batch()
	if snapshotID != "" {
		b.SnapshotID = snapshotID
	}
	if copyID != "" {
		b.CopyID = copyID
	}
	b.LogicalRepair = b.LogicalRepair || logical
	if projections != nil {
		b.Projections = make(map[string]*PublishedProjection, len(projections))
		for key, pp := range projections {
			b.Projections[key.String()] = (&Record{PublishedProjection: pp}).Clone().PublishedProjection
		}
	}
	_, err := c.commitDayLocked(day, nil, JournalEntry{Detail: "physical-day migration progress"})
	return err
}
