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

// RepairProjectionQueryGeneration 把「投影的 QueryGeneration 落后于权威 Generation」的
// 分区对齐到权威代次，使 `PublishProjection` 的前置校验（`QueryGeneration == Generation`）
// 重新成立。
//
// ── 返回值的三种语义（必须区分开，2026-10-21 复审补记的可观测性缺口）──────────────
//   - repaired=true                       ：本次**真的**提交了一次对齐（落 journal，重放后仍成立）；
//   - repaired=false ∧ needsManual==""    ：**无需修复**（投影与权威已一致）；
//   - repaired=false ∧ needsManual!=""    ：**不修，但也绝不静默**——该分区属于「只能交人工」
//     的形态，needsManual 是给人看的原因文案。调用方必须把它计入并单独打出来。
//
// 为什么「不修」也必须报出来：这三种形态既不会被修复、也不会返回 error，早前调用方只在
// 「修复数或失败数 > 0」时打日志 ⇒ 现场看到的是「自愈完成」，而实际上还有 N 个分区永久
// 不可发布，日志里一个字都没有。沉默的「不修」在可观测性上等价于「假装健康」，
// 与既成损害（28 个分区长期不可发布）的发现过程完全一致：正是因为没人报。
//
// ── 为什么需要它（2026-10-21 生产既成损害）──────────────────────────────────────
// `resetNamespaceConflictCounts` 的旧实现用无 CAS 的 `AppendJournal` 做「派生计数对齐」，
// 并把 `Generation` 自增 1 却**不动** `PublishedProjection.QueryGeneration`。于是造出一批
// `generation=2 ∧ query_generation=1` 的分区（生产实测 inst:320 有 28 个，覆盖 09-20～10-19
// 的活跃日期）。这类分区此后**永远无法再发布**：`PublishProjection` 一进门就返回
// `projection and authority do not match`，采集侧的补投/重建因此恒失败。
//
// ── 为什么是「把 query_generation 抬到 generation」而不是「把 generation 退回」──────
// 旧实现自增 `Generation` 之后，后续写入已按**新代次**把数据写进 VL（`projection_generation`
// 由代次派生）。退回代次会与已落库的数据脱节、并与「代次不得复用」的红线冲突。
// 抬 `query_generation` 只是把「这条投影属于哪个权威代次」这句话改对，不动任何物理数据。
//
// ── 安全边界 ────────────────────────────────────────────────────────────────
//   · 只处理 `query_generation < generation` 的**落后**形态。反过来（qgen > gen）说明记录
//     自相矛盾且成因未知，一律不碰，交人工判断。
//   · 仅当**其余字段**没有别的矛盾时才修：`QueryLocationDirID == OwnerDirID`（否则投影指向
//     的目录已不是权威目录，属另一类不一致）。
//   · 并发安全由**全程持 `c.mu` 写锁**保证（见下方提交处的注释）：所有写者都取同一把锁，
//     本函数内不存在可插入的临界区空隙，因此**没有**内部快照比对式 CAS，也不需要。
//   · 刻意**不**碰 `ConflictCount` 等派生值：本函数只修代次一致性这一件事。
func (c *Catalog) RepairProjectionQueryGeneration(key PartitionKey) (repaired bool, needsManual string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.records[key]
	if !ok || cur == nil {
		return false, "", fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if cur.PublishedProjection == nil {
		// 没有已发布投影：既没有「落后的 QueryGeneration」可修，也无从判断它是否发布过。
		// 这是「只能交人工」的形态之一 —— 调用方必须看到它，否则该分区会一直不可查询。
		return false, "分区没有已发布投影（无从判断代次是否落后，需人工确认该分区应否有投影）", nil
	}
	qgen, gen := cur.PublishedProjection.QueryGeneration, cur.Generation
	if qgen == gen {
		// 已一致：真正的「无需修复」。
		return false, "", nil
	}
	if qgen > gen {
		// 反向矛盾（投影声称属于更晚的代次）：成因未知，任何自动化改写都可能掩盖一次真实的
		// 权威切换事故（代次不得回退，而「把 generation 抬到 qgen」等于凭空造一次权威前进），
		// 故一律不碰。
		return false, fmt.Sprintf("投影 query_generation=%d 大于权威 generation=%d（反向矛盾，成因未知）", qgen, gen), nil
	}
	if cur.PublishedProjection.QueryLocationDirID != cur.OwnerDirID {
		// 目录也已经不一致：属另一类损坏（投影指向的目录已不是权威目录）。只抬代次会把
		// 这层损坏一起藏起来 —— 该分区即使「能发布」也读不到正确数据。
		return false, fmt.Sprintf("投影目录与权威目录不一致（query_location_dir_id=%q owner_dir_id=%q）",
			cur.PublishedProjection.QueryLocationDirID, cur.OwnerDirID), nil
	}
	next := cur.Clone()
	next.PublishedProjection.QueryGeneration = gen
	// 代次对齐**不构成**一次权威切换：owner/generation/owner_dir 全部保持不变，
	// 因此这里不能走 PublishProjection（它要求 QueryGeneration 已等于 Generation），
	// 而是在持锁内直接提交（调用方已持 c.mu，绝不可再取一次）。
	//
	// 关于并发与「CAS」（2026-10-21 复审修正，早前注释与实现不符）：本函数**没有**内部 CAS。
	// 早前这里写了一段「比对 expected 快照的 JournalLastSeq / Generation」的比较，但 expected
	// 就是 cur 在同一临界区内的克隆，两者之间不存在任何可插入的执行点 ⇒ 条件恒为假、
	// ErrInvalidState 在本函数内不可达，宣称的「并发下若别人已发布到更新代次则本次放弃」
	// 并不成立。真正的保证是另外两条，写在这里以免再被误读：
	//   1. 并发安全由全程持有 c.mu 写锁给出：所有写者（PublishProjection / AppendJournal /
	//      SwitchOwner / 本函数）都取同一把锁，临界区内不可能有别的提交插进来；
	//   2. 下面这次提交会推进 JournalLastSeq，于是任何**此前**取过快照的外部写者，
	//      其 PublishProjection 的 CAS（按 journal 序号 / owner / 代次 / 目录逐项比对）
	//      必然失败并放弃，不会用旧快照覆盖本函数刚写下的状态。
	committed := next.Clone()
	entry := JournalEntry{
		Key:     committed.Key,
		Detail:  "derived query_generation repaired to match authority generation",
		State:   committed.MigrationState,
	}
	if _, err := c.commitRecordLocked(committed, entry); err != nil {
		return false, "", err
	}
	return true, "", nil
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
