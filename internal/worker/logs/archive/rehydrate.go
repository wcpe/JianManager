package archive

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ArchivedEvent 归档内事件的 foundation 表示。
// EventTimeUTC 对应原始 `_time`：Rehydrate 前后必须保持不变。
type ArchivedEvent struct {
	EventID          string `json:"event_id"`
	LogSourceID      string `json:"log_source_id"`
	SourceGeneration string `json:"source_generation"`
	ParserVersion    string `json:"parser_version"`
	EventTimeUTC     string `json:"event_time_utc"` // 原始 _time，不可变
	IngestTimeUTC    string `json:"ingest_time_utc"`
	Level            string `json:"level"`
	Stream           string `json:"stream"`
	Message          string `json:"message"`
	RecordStart      uint64 `json:"record_start"`
	RecordEnd        uint64 `json:"record_end"`
	CanonicalHash    string `json:"canonical_content_hash"`
	ObjectID         string `json:"object_id,omitempty"`
}

// RestoredPartition 恢复产物：generation 隔离的临时分区引用。
type RestoredPartition struct {
	PartitionKey PartitionKey    `json:"partition_key"`
	TempDirID    string          `json:"temp_dir_id"`
	Events       []ArchivedEvent `json:"events"`
	// OriginalTimes event_id → 恢复前登记的原始 _time，用于核验未被改写。
	OriginalTimes map[string]string `json:"original_times,omitempty"`
}

// RehydrateRequest 启动/合并 Rehydrate 任务的请求。
type RehydrateRequest struct {
	PartitionKey     PartitionKey
	ArchiveObjectIDs []string
	// Timeout 任务超时；0 使用 manager 默认值。
	Timeout time.Duration
	// DiskReserveBytes 本任务要求的磁盘预留；不足则结构化失败。
	DiskReserveBytes int64
	// Holder 请求者标识（可观测）。
	Holder string
	// Events 待恢复事件（foundation 直接提供；生产由 Provider 读取归档对象）。
	Events []ArchivedEvent
}

// JobLease 任务租约。
type JobLease struct {
	LeaseID    string    `json:"lease_id"`
	TaskID     string    `json:"task_id"`
	Holder     string    `json:"holder,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Active 报告租约在 now 时刻是否仍有效。
func (l JobLease) Active(now time.Time) bool {
	if l.LeaseID == "" {
		return false
	}
	if l.ExpiresAt.IsZero() {
		return true
	}
	return now.Before(l.ExpiresAt)
}

// CleanupHold 清理保留约束：Rehydrate 任务 hold 与 Query View 租约 hold 使用同一登记表。
type CleanupHold struct {
	HoldID     string       `json:"hold_id"`
	Kind       string       `json:"kind"` // rehydrate | query_view
	TaskID     string       `json:"task_id,omitempty"`
	ViewID     string       `json:"view_id,omitempty"`
	LeaseID    string       `json:"lease_id,omitempty"`
	Partition  PartitionKey `json:"partition"`
	Generation uint64       `json:"generation"`
	ExpiresAt  time.Time    `json:"expires_at"`
	Reason     string       `json:"reason,omitempty"`
}

// Active 报告 hold 是否仍阻止清理。
func (h CleanupHold) Active(now time.Time) bool {
	if h.HoldID == "" {
		return false
	}
	if h.ExpiresAt.IsZero() {
		return true
	}
	return now.Before(h.ExpiresAt)
}

// HoldKind 常量。
const (
	HoldKindRehydrate = "rehydrate"
	HoldKindQueryView = "query_view"
)

// RehydrateJob 任务快照（对外只读视图）。
type RehydrateJob struct {
	TaskID           string             `json:"task_id"`
	MergeKey         string             `json:"merge_key"`
	PartitionKey     PartitionKey       `json:"partition_key"`
	Generation       uint64             `json:"generation"`
	State            TaskState          `json:"state"`
	Reason           string             `json:"reason,omitempty"`
	Lease            JobLease           `json:"lease"`
	ArchiveObjectIDs []string           `json:"archive_object_ids"`
	DiskReserveBytes int64              `json:"disk_reserve_bytes"`
	Timeout          time.Duration      `json:"timeout"`
	CreatedAt        time.Time          `json:"created_at"`
	Deadline         time.Time          `json:"deadline,omitempty"`
	CompletedAt      *time.Time         `json:"completed_at,omitempty"`
	Holder           string             `json:"holder,omitempty"`
	Waiters          int                `json:"waiters"`
	Result           *RestoredPartition `json:"result,omitempty"`
}

// Terminal 报告任务是否处于终态。
func (j RehydrateJob) Terminal() bool {
	switch j.State {
	case TaskSucceeded, TaskFailed, TaskCancelled, TaskStale:
		return true
	default:
		return false
	}
}

type jobEntry struct {
	mu       sync.Mutex
	job      RehydrateJob
	cancel   context.CancelFunc
	events   []ArchivedEvent
	leaseSeq int64
}

func (e *jobEntry) snapshot() RehydrateJob {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotLocked()
}

// snapshotLocked 假定调用方已持有 e.mu。
func (e *jobEntry) snapshotLocked() RehydrateJob {
	snap := e.job
	snap.ArchiveObjectIDs = append([]string(nil), e.job.ArchiveObjectIDs...)
	if e.job.Result != nil {
		rc := *e.job.Result
		rc.Events = append([]ArchivedEvent(nil), e.job.Result.Events...)
		if e.job.Result.OriginalTimes != nil {
			rc.OriginalTimes = make(map[string]string, len(e.job.Result.OriginalTimes))
			for k, v := range e.job.Result.OriginalTimes {
				rc.OriginalTimes[k] = v
			}
		}
		snap.Result = &rc
	}
	return snap
}

// RehydrateOptions manager 配置。
type RehydrateOptions struct {
	// DefaultTimeout 默认任务超时。
	DefaultTimeout time.Duration
	// DefaultLeaseTTL 租约默认有效期。
	DefaultLeaseTTL time.Duration
	// DiskAvailable 返回指定分区当前可用磁盘字节；nil 时视为充足（测试可注入）。
	DiskAvailable func(key PartitionKey) int64
	// Now 可注入时钟。
	Now func() time.Time
	// IDGen 可注入 task/lease id 生成器。
	IDGen func(prefix string) string
}

// RehydrateManager 管理 Rehydrate 任务：合并键复用、租约、取消、超时、磁盘预留、
// generation 隔离清理 hold，并与 Query View 租约共用 hold 登记。
type RehydrateManager struct {
	mu      sync.Mutex
	opts    RehydrateOptions
	byMerge map[string]*jobEntry
	byTask  map[string]*jobEntry
	holds   map[string]CleanupHold
	seq     int64
}

// NewRehydrateManager 创建 manager。
func NewRehydrateManager(opts RehydrateOptions) *RehydrateManager {
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = 5 * time.Minute
	}
	if opts.DefaultLeaseTTL <= 0 {
		opts.DefaultLeaseTTL = opts.DefaultTimeout
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.IDGen == nil {
		opts.IDGen = nil // lazy: manager 内部序号
	}
	return &RehydrateManager{
		opts:    opts,
		byMerge: make(map[string]*jobEntry),
		byTask:  make(map[string]*jobEntry),
		holds:   make(map[string]CleanupHold),
	}
}

func (m *RehydrateManager) now() time.Time { return m.opts.Now() }

func (m *RehydrateManager) nextID(prefix string) string {
	if m.opts.IDGen != nil {
		return m.opts.IDGen(prefix)
	}
	m.seq++
	return fmt.Sprintf("%s-%d-%d", prefix, m.now().UnixNano(), m.seq)
}

// Start 启动或合并 Rehydrate 任务。
//
// 同一 merge key（分区 + 排序后的 object_id 集合）且任务未终态时，复用在途任务与租约。
func (m *RehydrateManager) Start(ctx context.Context, req RehydrateRequest) (*RehydrateJob, error) {
	if err := req.PartitionKey.Validate(); err != nil {
		return nil, newError("Rehydrate.Start", req.PartitionKey.String(), ReasonInvalidKey, false, err)
	}
	if len(req.ArchiveObjectIDs) == 0 {
		return nil, newError("Rehydrate.Start", req.PartitionKey.String(), ReasonInvalidKey, false,
			fmt.Errorf("%w: archive_object_ids required", ErrArchive))
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = m.opts.DefaultTimeout
	}

	m.mu.Lock()
	m.expireTimeoutsLocked()
	merge := MergeKey(req.PartitionKey, req.ArchiveObjectIDs)

	if entry, ok := m.byMerge[merge]; ok {
		snap := entry.snapshot()
		if !snap.Terminal() {
			entry.mu.Lock()
			entry.job.Waiters++
			snap = entry.snapshotLocked()
			entry.mu.Unlock()
			m.mu.Unlock()
			return &snap, nil
		}
		// 终态任务不复用：允许同 key 重新发起（幂等重恢复）。
	}

	// 磁盘预留
	if req.DiskReserveBytes > 0 {
		if m.opts.DiskAvailable != nil {
			avail := m.opts.DiskAvailable(req.PartitionKey)
			if avail < req.DiskReserveBytes {
				m.mu.Unlock()
				return nil, newError("Rehydrate.Start", req.PartitionKey.String(), ReasonDiskReserveUnmet, true,
					fmt.Errorf("%w: available=%d reserve=%d", ErrDiskReserve, avail, req.DiskReserveBytes))
			}
		}
	}

	now := m.now()
	taskID := m.nextID("rh")
	leaseID := m.nextID("lease")
	job := RehydrateJob{
		TaskID:           taskID,
		MergeKey:         merge,
		PartitionKey:     req.PartitionKey,
		Generation:       req.PartitionKey.Generation,
		State:            TaskRunning,
		ArchiveObjectIDs: append([]string(nil), req.ArchiveObjectIDs...),
		DiskReserveBytes: req.DiskReserveBytes,
		Timeout:          timeout,
		CreatedAt:        now,
		Deadline:         now.Add(timeout),
		Holder:           req.Holder,
		Waiters:          1,
		Lease: JobLease{
			LeaseID:    leaseID,
			TaskID:     taskID,
			Holder:     req.Holder,
			AcquiredAt: now,
			ExpiresAt:  now.Add(m.opts.DefaultLeaseTTL),
		},
	}
	sort.Strings(job.ArchiveObjectIDs)

	entry := &jobEntry{
		job:    job,
		events: append([]ArchivedEvent(nil), req.Events...),
	}
	// foundation 不自动起执行循环；ctx 仅用于调用方取消语义登记。
	// 调用方可 Cancel(task_id)，或依赖超时扫描（Start/Get 触发 expire）。
	_ = ctx

	m.byMerge[merge] = entry
	m.byTask[taskID] = entry

	// 登记 rehydrate cleanup hold（绑定 generation）。
	hold := CleanupHold{
		HoldID:     m.nextID("hold"),
		Kind:       HoldKindRehydrate,
		TaskID:     taskID,
		Partition:  req.PartitionKey,
		Generation: req.PartitionKey.Generation,
		ExpiresAt:  job.Deadline,
		Reason:     "rehydrate-in-flight",
	}
	m.holds[hold.HoldID] = hold
	m.mu.Unlock()

	snap := entry.snapshot()
	return &snap, nil
}

// Complete 以恢复结果完成任务；核验原始 _time 未被改写。
// 锁序约定：m.mu 与 entry.mu 不得嵌套持有；holds 释放在 entry.mu 释放之后。
func (m *RehydrateManager) Complete(taskID string, events []ArchivedEvent) (*RehydrateJob, error) {
	m.mu.Lock()
	entry, ok := m.byTask[taskID]
	if !ok {
		m.mu.Unlock()
		return nil, newError("Rehydrate.Complete", taskID, ReasonTaskNotFound, false, ErrTaskNotFound)
	}
	m.mu.Unlock()

	entry.mu.Lock()
	if entry.job.Terminal() {
		if entry.job.State == TaskSucceeded {
			snap := entry.snapshotLocked()
			entry.mu.Unlock()
			return &snap, nil
		}
		entry.mu.Unlock()
		return nil, newError("Rehydrate.Complete", taskID, ReasonTaskTerminal, false, ErrTaskTerminal)
	}

	now := m.now()
	if !entry.job.Deadline.IsZero() && !now.Before(entry.job.Deadline) {
		entry.job.State = TaskStale
		entry.job.Reason = ReasonTaskTimeout
		entry.job.CompletedAt = &now
		snap := entry.snapshotLocked()
		entry.mu.Unlock()
		m.finishTask(entry)
		return &snap, newError("Rehydrate.Complete", taskID, ReasonTaskTimeout, true,
			fmt.Errorf("%w: deadline exceeded", ErrArchive))
	}

	// 核验 _time 不变：以启动时登记的 events 为权威原始时间。
	expected := make(map[string]string, len(entry.events))
	for _, ev := range entry.events {
		expected[ev.EventID] = ev.EventTimeUTC
	}
	restored := make([]ArchivedEvent, len(events))
	copy(restored, events)
	originalTimes := make(map[string]string, len(restored))
	for _, ev := range restored {
		want, has := expected[ev.EventID]
		if !has {
			originalTimes[ev.EventID] = ev.EventTimeUTC
			continue
		}
		if ev.EventTimeUTC != want {
			entry.job.State = TaskFailed
			entry.job.Reason = ReasonOriginalTimeMutated
			entry.job.CompletedAt = &now
			snap := entry.snapshotLocked()
			entry.mu.Unlock()
			m.finishTask(entry)
			return &snap, newError("Rehydrate.Complete", taskID, ReasonOriginalTimeMutated, false,
				fmt.Errorf("%w: event %s time %q != original %q", ErrTimeMutated, ev.EventID, ev.EventTimeUTC, want))
		}
		originalTimes[ev.EventID] = want
	}

	entry.job.Result = &RestoredPartition{
		PartitionKey:  entry.job.PartitionKey,
		TempDirID:     fmt.Sprintf("rehydrate-temp/%s", entry.job.PartitionKey.String()),
		Events:        restored,
		OriginalTimes: originalTimes,
	}
	entry.job.State = TaskSucceeded
	entry.job.Reason = ""
	entry.job.CompletedAt = &now
	snap := entry.snapshotLocked()
	entry.mu.Unlock()
	m.finishTask(entry)
	return &snap, nil
}

// finishTask 在 entry 锁外执行取消与 hold 释放。
func (m *RehydrateManager) finishTask(entry *jobEntry) {
	if entry == nil {
		return
	}
	entry.mu.Lock()
	cancel := entry.cancel
	taskID := entry.job.TaskID
	entry.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.releaseHoldsForTask(taskID)
}

// Cancel 取消任务（幂等）。
func (m *RehydrateManager) Cancel(taskID string) error {
	m.mu.Lock()
	entry, ok := m.byTask[taskID]
	m.mu.Unlock()
	if !ok {
		return newError("Rehydrate.Cancel", taskID, ReasonTaskNotFound, false, ErrTaskNotFound)
	}
	entry.mu.Lock()
	if entry.job.Terminal() {
		entry.mu.Unlock()
		return nil
	}
	now := m.now()
	entry.job.State = TaskCancelled
	entry.job.Reason = ReasonTaskCancelled
	entry.job.CompletedAt = &now
	entry.mu.Unlock()
	m.finishTask(entry)
	return nil
}

func (m *RehydrateManager) Fail(taskID, reason string) error {
	m.mu.Lock()
	entry, ok := m.byTask[taskID]
	m.mu.Unlock()
	if !ok {
		return newError("Rehydrate.Fail", taskID, ReasonTaskNotFound, false, ErrTaskNotFound)
	}
	entry.mu.Lock()
	if entry.job.Terminal() {
		entry.mu.Unlock()
		return nil
	}
	now := m.now()
	entry.job.State = TaskFailed
	entry.job.Reason = reason
	entry.job.CompletedAt = &now
	entry.mu.Unlock()
	m.finishTask(entry)
	return nil
}

// Get 返回任务快照。
func (m *RehydrateManager) Get(taskID string) (*RehydrateJob, error) {
	m.mu.Lock()
	entry, ok := m.byTask[taskID]
	m.mu.Unlock()
	if !ok {
		return nil, newError("Rehydrate.Get", taskID, ReasonTaskNotFound, false, ErrTaskNotFound)
	}
	snap := entry.snapshot()
	return &snap, nil
}

// GetByMergeKey 按合并键返回最近任务（含终态）。
func (m *RehydrateManager) GetByMergeKey(mergeKey string) (*RehydrateJob, bool) {
	m.mu.Lock()
	entry, ok := m.byMerge[mergeKey]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	snap := entry.snapshot()
	return &snap, true
}

// RenewLease 续租；租约过期的任务返回结构化错误。
func (m *RehydrateManager) RenewLease(taskID string, extend time.Duration) (*JobLease, error) {
	if extend <= 0 {
		extend = m.opts.DefaultLeaseTTL
	}
	m.mu.Lock()
	entry, ok := m.byTask[taskID]
	m.mu.Unlock()
	if !ok {
		return nil, newError("Rehydrate.RenewLease", taskID, ReasonTaskNotFound, false, ErrTaskNotFound)
	}
	entry.mu.Lock()
	now := m.now()
	if entry.job.Terminal() {
		entry.mu.Unlock()
		return nil, newError("Rehydrate.RenewLease", taskID, ReasonTaskTerminal, false, ErrTaskTerminal)
	}
	if !entry.job.Lease.Active(now) && entry.job.State == TaskRunning {
		entry.job.State = TaskStale
		entry.job.Reason = ReasonLeaseExpired
		entry.job.CompletedAt = &now
		entry.mu.Unlock()
		m.finishTask(entry)
		return nil, newError("Rehydrate.RenewLease", taskID, ReasonLeaseExpired, true, ErrArchive)
	}
	entry.job.Lease.ExpiresAt = now.Add(extend)
	lease := entry.job.Lease
	entry.mu.Unlock()
	return &lease, nil
}

// RegisterQueryViewLease 登记 Query View 租约 hold，阻止对应 generation 的清理。
func (m *RehydrateManager) RegisterQueryViewLease(key PartitionKey, leaseID, viewID, holder string, expires time.Time) (CleanupHold, error) {
	if err := key.Validate(); err != nil {
		return CleanupHold{}, newError("RegisterQueryViewLease", key.String(), ReasonInvalidKey, false, err)
	}
	if leaseID == "" {
		leaseID = m.nextID("qv-lease")
	}
	if expires.IsZero() {
		expires = m.now().Add(m.opts.DefaultLeaseTTL)
	}
	h := CleanupHold{
		HoldID:     m.nextID("hold"),
		Kind:       HoldKindQueryView,
		ViewID:     viewID,
		LeaseID:    leaseID,
		Partition:  key,
		Generation: key.Generation,
		ExpiresAt:  expires,
		Reason:     "query-view-lease",
	}
	m.mu.Lock()
	m.holds[h.HoldID] = h
	m.mu.Unlock()
	return h, nil
}

// ReleaseQueryViewLease 释放 Query View 租约 hold。
func (m *RehydrateManager) ReleaseQueryViewLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, h := range m.holds {
		if h.Kind == HoldKindQueryView && h.LeaseID == leaseID {
			delete(m.holds, id)
		}
	}
}

// ActiveHolds 返回仍阻止指定分区（按 generation 精确匹配）清理的 hold。
func (m *RehydrateManager) ActiveHolds(key PartitionKey) []CleanupHold {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []CleanupHold
	for _, h := range m.holds {
		if !h.Active(now) {
			continue
		}
		// generation 隔离：只匹配同一分区键（含 generation）。
		if h.Generation != key.Generation {
			continue
		}
		if h.Partition.StorageNamespace != key.StorageNamespace || h.Partition.UTCDay != key.UTCDay {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HoldID < out[j].HoldID })
	return out
}

// CanCleanup 报告指定 generation 分区是否可被清理。
// Query View 租约与 Rehydrate hold 任一有效时均不得清理。
func (m *RehydrateManager) CanCleanup(key PartitionKey) bool {
	return len(m.ActiveHolds(key)) == 0
}

// ListJobs 返回全部任务快照（可观测性/测试）。
func (m *RehydrateManager) ListJobs() []RehydrateJob {
	m.mu.Lock()
	entries := make([]*jobEntry, 0, len(m.byTask))
	for _, e := range m.byTask {
		entries = append(entries, e)
	}
	m.mu.Unlock()
	out := make([]RehydrateJob, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

func (m *RehydrateManager) releaseHoldsForTask(taskID string) {
	// 调用方可能已持有 entry 锁；manager 锁单独获取。
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for id, h := range m.holds {
		if h.Kind == HoldKindRehydrate && h.TaskID == taskID {
			// 任务结束即释放 rehydrate hold；query_view hold 独立。
			delete(m.holds, id)
			continue
		}
		if !h.Active(now) {
			delete(m.holds, id)
		}
	}
}

func (m *RehydrateManager) expireTimeoutsLocked() {
	// 调用方已持有 m.mu。这里只改任务状态并直接删 hold，不再取 entry.mu 之外的 manager 锁。
	// entry.mu 与 m.mu 的获取顺序：Start/Get 先 m.mu 后 entry.mu；Complete/Cancel 在释放
	// entry.mu 之后才碰 holds（releaseHoldsForTask 单独取 m.mu），避免反向嵌套。
	now := m.now()
	for _, entry := range m.byTask {
		entry.mu.Lock()
		if entry.job.Terminal() || entry.job.Deadline.IsZero() || now.Before(entry.job.Deadline) {
			entry.mu.Unlock()
			continue
		}
		entry.job.State = TaskStale
		entry.job.Reason = ReasonTaskTimeout
		completed := now
		entry.job.CompletedAt = &completed
		taskID := entry.job.TaskID
		entry.mu.Unlock()
		for id, h := range m.holds {
			if h.Kind == HoldKindRehydrate && h.TaskID == taskID {
				delete(m.holds, id)
			}
		}
	}
}

// RestoreEvents 按契约将归档事件恢复为可查询事件：保留原始 _time / event_id。
// 这是显式的身份变换入口，禁止在恢复路径上重算 event_time。
func RestoreEvents(src []ArchivedEvent) []ArchivedEvent {
	out := make([]ArchivedEvent, len(src))
	copy(out, src)
	return out
}

// VerifyTimesUnchanged 核验恢复结果与原始时间一致。
func VerifyTimesUnchanged(original []ArchivedEvent, restored []ArchivedEvent) error {
	want := make(map[string]string, len(original))
	for _, ev := range original {
		want[ev.EventID] = ev.EventTimeUTC
	}
	for _, ev := range restored {
		exp, ok := want[ev.EventID]
		if !ok {
			continue
		}
		if ev.EventTimeUTC != exp {
			return newError("VerifyTimes", ev.EventID, ReasonOriginalTimeMutated, false,
				fmt.Errorf("%w: %s got %q want %q", ErrTimeMutated, ev.EventID, ev.EventTimeUTC, exp))
		}
	}
	return nil
}
