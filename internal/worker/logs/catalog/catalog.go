package catalog

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Catalog 是 Worker 本地 Partition Catalog。
//
// 约束：
//   - 每个 (storage_namespace, utc_day) 唯一记录；
//   - 物理目录扫描只是输入，权威来自本 Catalog；
//   - 启动先 Recover，再为 Catalog 选定的 owner 建 QueryPlanner 路由；
//   - 不实现 VL 进程控制。
type Catalog struct {
	mu      sync.RWMutex
	records map[PartitionKey]*Record
	journal Journal
}

// New 创建 Catalog。journal 为 nil 时使用内存 journal。
func New(journal Journal) *Catalog {
	if journal == nil {
		journal = NewMemJournal()
	}
	c := &Catalog{
		records: make(map[PartitionKey]*Record),
		journal: journal,
	}
	for _, entry := range journal.Snapshot() {
		c.applyJournalEntry(entry)
	}
	return c
}

func (c *Catalog) applyJournalEntry(entry JournalEntry) {
	if c == nil {
		return
	}
	if len(entry.Records) > 0 {
		for _, item := range entry.Records {
			if item == nil {
				continue
			}
			rec := item.Clone()
			rec.JournalLastSeq = entry.Seq
			c.records[rec.Key] = rec
		}
		return
	}
	if entry.Record != nil {
		rec := entry.Record.Clone()
		rec.JournalLastSeq = entry.Seq
		c.records[entry.Key] = rec
		return
	}
	rec := c.records[entry.Key]
	if rec == nil {
		rec = &Record{Key: entry.Key}
		c.records[entry.Key] = rec
	}
	if entry.Seq > rec.JournalLastSeq {
		rec.JournalLastSeq = entry.Seq
	}
	if entry.JournalComplete {
		rec.JournalComplete = true
	}
	if entry.State != "" && (IsHappyPath(entry.State) || IsFailure(entry.State)) {
		rec.MigrationState = entry.State
	}
	if entry.Owner.Valid() {
		rec.Owner = entry.Owner
	}
	if entry.Generation != 0 {
		rec.Generation = entry.Generation
	}
	if entry.DirID != "" {
		rec.OwnerDirID = entry.DirID
	}
	if entry.WriteRoute.Owner != "" || entry.WriteRoute.DirID != "" {
		rec.WriteRoute = entry.WriteRoute
	}
	if entry.PublishedProjection != nil {
		rec.PublishedProjection = entry.PublishedProjection
	}
	if entry.Dirs != nil {
		rec.Dirs = append([]PhysicalDir(nil), entry.Dirs...)
	}
	if entry.ResidualDirs != nil {
		rec.ResidualDirs = append([]PhysicalDir(nil), entry.ResidualDirs...)
	}
	rec.RecoveryRequired = entry.RecoveryRequired
	rec.PartialReasons = append([]string(nil), entry.PartialReasons...)
}

func (c *Catalog) commitBatchLocked(records []*Record, entry JournalEntry) ([]*Record, error) {
	entry.Records = make([]*Record, 0, len(records))
	for _, rec := range records {
		entry.Records = append(entry.Records, rec.Clone())
	}
	if err := c.journal.Append(entry); err != nil {
		return nil, err
	}
	seq := c.journal.LastSeq()
	out := make([]*Record, 0, len(records))
	for _, rec := range records {
		rec.JournalLastSeq = seq
		c.records[rec.Key] = rec
		out = append(out, rec.Clone())
	}
	return out, nil
}

// RecordsForDay returns all logical namespaces sharing one physical UTC-day partition.
func (c *Catalog) RecordsForDay(utcDay string) []*Record {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*Record
	for key, rec := range c.records {
		if key.UTCDay == utcDay {
			out = append(out, rec.Clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out
}

// Journal 返回底层 journal（只读使用约定）。
func (c *Catalog) Journal() Journal {
	return c.journal
}

// Get 返回分区记录副本。
func (c *Catalog) Get(key PartitionKey) (*Record, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rec, ok := c.records[key]
	if !ok {
		return nil, false
	}
	return rec.Clone(), true
}

// Put 写入/覆盖一条稳定记录（测试与恢复装载用）。
func (c *Catalog) Put(rec *Record) error {
	if rec == nil {
		return fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	if rec.Key.StorageNamespace == "" || rec.Key.UTCDay == "" {
		return fmt.Errorf("%w: partition key required", ErrInvalidState)
	}
	if rec.Owner != "" && !rec.Owner.Valid() {
		return fmt.Errorf("%w: owner=%q", ErrInvalidOwner, rec.Owner)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records[rec.Key] = rec.Clone()
	return nil
}

// Keys 返回当前全部分区键。
func (c *Catalog) Keys() []PartitionKey {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]PartitionKey, 0, len(c.records))
	for k := range c.records {
		out = append(out, k)
	}
	return out
}

// AppendJournal 追加 journal 并同步更新记录侧可见的 journal 状态。
func (c *Catalog) AppendJournal(entry JournalEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.journal.Append(entry); err != nil {
		return err
	}
	entry.Seq = c.journal.LastSeq()
	c.applyJournalEntry(entry)
	return nil
}

// PublishProjection atomically validates the authority observed before physical
// writes and commits its new manifest. A concurrent migration/publication wins;
// the stale writer must retain recovery responsibility and retry in isolation.
func (c *Catalog) PublishProjection(expected, next *Record) error {
	if next == nil {
		return fmt.Errorf("%w: nil projection record", ErrInvalidState)
	}
	if next.PublishedProjection == nil || next.PublishedProjection.QueryGeneration != next.Generation ||
		next.PublishedProjection.QueryLocationDirID != next.OwnerDirID {
		return fmt.Errorf("%w: projection and authority do not match", ErrInvalidState)
	}
	if expected != nil && (next.Owner != expected.Owner || next.Generation != expected.Generation || next.OwnerDirID != expected.OwnerDirID || next.WriteRoute != expected.WriteRoute) {
		return fmt.Errorf("%w: projection publication cannot change authority", ErrInvalidState)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.records[next.Key]
	if expected == nil {
		if current != nil {
			return fmt.Errorf("%w: Catalog authority created during projection verification", ErrInvalidState)
		}
	} else {
		if current == nil || expected.Key != next.Key || current.JournalLastSeq != expected.JournalLastSeq ||
			current.Owner != expected.Owner || current.Generation != expected.Generation || current.OwnerDirID != expected.OwnerDirID ||
			current.WriteRoute != expected.WriteRoute || current.RecoveryRequired != expected.RecoveryRequired ||
			projectionManifest(current) != projectionManifest(expected) {
			return fmt.Errorf("%w: Catalog authority changed during projection verification", ErrInvalidState)
		}
	}
	_, err := c.commitRecordLocked(next.Clone(), JournalEntry{Key: next.Key, AuthorityCommit: true, Detail: "verified projection publication"})
	return err
}

func projectionManifest(rec *Record) string {
	if rec != nil && rec.PublishedProjection != nil {
		return rec.PublishedProjection.ManifestVersion
	}
	return ""
}

// commitRecordLocked keeps a transition invisible until its full state is
// durable in the journal. Caller holds c.mu.
func (c *Catalog) commitRecordLocked(rec *Record, entry JournalEntry) (*Record, error) {
	entry.Record = rec.Clone()
	if err := c.journal.Append(entry); err != nil {
		return nil, err
	}
	rec.JournalLastSeq = c.journal.LastSeq()
	c.records[rec.Key] = rec
	return rec.Clone(), nil
}

// BeginMigration 冻结路由并登记迁移目标，同时写 journal。
func (c *Catalog) BeginMigration(key PartitionKey, target Owner, targetDirID string) (*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.records[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	rec = rec.Clone()
	from := rec.MigrationState
	if err := BeginMigration(rec, target, targetDirID); err != nil {
		return nil, err
	}
	return c.commitRecordLocked(rec, JournalEntry{
		Key:         key,
		State:       StateRoutingFrozen,
		FromState:   from,
		Owner:       rec.Owner,
		Generation:  rec.Generation,
		DirID:       rec.OwnerDirID,
		Detail:      "begin-migration routing frozen",
		AtUnixMilli: time.Now().UnixMilli(),
	})
}

// Advance 将分区推进到下一个迁移状态并写 journal（不含 OWNER_SWITCHED）。
func (c *Catalog) Advance(key PartitionKey, to MigrationState) (*Record, error) {
	if to == StateOwnerSwitched {
		return nil, fmt.Errorf("%w: use SwitchOwner for OWNER_SWITCHED", ErrIllegalTransition)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.records[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	rec = rec.Clone()
	from := rec.MigrationState
	if err := Transition(rec, to); err != nil {
		return nil, err
	}
	// 进入 SNAPSHOTTING 时锁定目标 generation。
	if to == StateSnapshotting && rec.TargetGeneration == 0 {
		rec.TargetGeneration = rec.MigrationFromGeneration + 1
	}
	// 进入 CLEANED：要求 journal 完成标记由调用方在验证后写入；此处不自动 complete。
	return c.commitRecordLocked(rec, JournalEntry{
		Key:         key,
		State:       to,
		FromState:   from,
		Owner:       rec.Owner,
		Generation:  rec.Generation,
		DirID:       rec.OwnerDirID,
		Detail:      "advance",
		AtUnixMilli: time.Now().UnixMilli(),
	})
}

// SwitchOwner 执行 OWNER_SWITCHED 原子提交，并写入 AuthorityCommit journal。
func (c *Catalog) SwitchOwner(key PartitionKey, target Owner, targetDirID string, targetGen uint64, proj *PublishedProjection) (*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.records[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	rec = rec.Clone()
	from := rec.MigrationState
	if err := ApplyOwnerSwitch(rec, target, targetDirID, targetGen, proj); err != nil {
		return nil, err
	}
	return c.commitRecordLocked(rec, JournalEntry{
		Key:             key,
		State:           StateOwnerSwitched,
		FromState:       from,
		Owner:           rec.Owner,
		Generation:      rec.Generation,
		DirID:           rec.OwnerDirID,
		AuthorityCommit: true,
		Detail:          "owner-switched atomic commit",
		AtUnixMilli:     time.Now().UnixMilli(),
	})
}

// MarkJournalComplete 标记分区 journal 完成（清理校验通过后）。
func (c *Catalog) MarkJournalComplete(key PartitionKey, checksum string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.records[key]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	rec = rec.Clone()
	rec.JournalComplete = true
	rec.LastVerifyCompleted = true
	rec.LastVerifyChecksum = checksum
	_, err := c.commitRecordLocked(rec, JournalEntry{
		Key:             key,
		State:           rec.MigrationState,
		Owner:           rec.Owner,
		Generation:      rec.Generation,
		DirID:           rec.OwnerDirID,
		JournalComplete: true,
		Detail:          "journal-complete",
		AtUnixMilli:     time.Now().UnixMilli(),
	})
	return err
}

// RecordVerification persists the staging copy checksum before authority can
// switch. It is not the final journal-complete marker.
func (c *Catalog) RecordVerification(key PartitionKey, checksum string) error {
	if checksum == "" {
		return fmt.Errorf("%w: verification checksum required", ErrInvalidState)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.records[key]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	rec = rec.Clone()
	rec.LastVerifyCompleted = true
	rec.LastVerifyChecksum = checksum
	_, err := c.commitRecordLocked(rec, JournalEntry{
		Key: key, State: rec.MigrationState, Owner: rec.Owner, Generation: rec.Generation,
		DirID: rec.OwnerDirID, Detail: "staging-copy-verified", AtUnixMilli: time.Now().UnixMilli(),
	})
	return err
}

// Fail 将分区标为失败态。
func (c *Catalog) Fail(key PartitionKey, to MigrationState, detail string) (*Record, error) {
	if to != StateFailedRetryable && to != StateFailedManual {
		return nil, fmt.Errorf("%w: fail target must be FAILED_*", ErrIllegalTransition)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.records[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	rec = rec.Clone()
	from := rec.MigrationState
	if err := Transition(rec, to); err != nil {
		return nil, err
	}
	rec.RecoveryRequired = true
	return c.commitRecordLocked(rec, JournalEntry{
		Key:         key,
		State:       to,
		FromState:   from,
		Owner:       rec.Owner,
		Generation:  rec.Generation,
		DirID:       rec.OwnerDirID,
		Detail:      detail,
		AtUnixMilli: time.Now().UnixMilli(),
	})
}

// StartingRecover 启动恢复：按 journal 重放到幂等逻辑状态，再 Recover 每条记录。
//
// 步骤对应契约 §5.3：
//  1. 读取/校验 journal；
//  2. 重放到逻辑状态，标出冲突 generation；
//  3. Recover 给出可查询/可写入/不可用范围；
//  4. 只有 Catalog owner 可进入 QueryPlanner（由 IsQueryPlannerVisible/OwnerForQuery 保证）。
func (c *Catalog) StartingRecover() map[PartitionKey]RecoveryView {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[PartitionKey]RecoveryView, len(c.records))
	for key, rec := range c.records {
		entries := c.journal.Entries(key)
		conflict := ReplayJournal(rec, entries)
		if conflict != "" {
			rec.ConflictGeneration = conflict
			rec.RecoveryRequired = true
		}
		view := Recover(rec)
		// 把恢复结论写回记录，供查询层读取。
		rec.RecoveryRequired = view.RecoveryRequired
		rec.PartialReasons = view.PartialReasons
		rec.MigrationState = view.MigrationState
		out[key] = view
	}
	return out
}

// OwnerForQuery 按分区返回 QueryPlanner 权威。
func (c *Catalog) OwnerForQuery(key PartitionKey) (QueryRef, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rec, ok := c.records[key]
	if !ok {
		return QueryRef{}, false
	}
	return OwnerForQuery(rec), true
}

// IsQueryDirVisible 报告目录是否可进入 QueryPlanner（残留 re-attach 过滤入口）。
func (c *Catalog) IsQueryDirVisible(key PartitionKey, dirID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rec, ok := c.records[key]
	if !ok {
		return false
	}
	return IsQueryPlannerVisible(rec, dirID)
}

// RouteWrite 按 Catalog 路由写入（含迟到事件）。
func (c *Catalog) RouteWrite(key PartitionKey, eventUTCDay string) (WriteTarget, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rec, ok := c.records[key]
	if !ok {
		return WriteTarget{OK: false, Reason: "late-no-catalog"}, false
	}
	return RouteLateEvent(rec, eventUTCDay), true
}
