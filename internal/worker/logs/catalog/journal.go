package catalog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// JournalEntry 是迁移 journal 的追加项（append-only）。
// 不写入 Worker 业务 SQLite；持久化可选 FileStore。
type JournalEntry struct {
	Seq uint64       `json:"seq"`
	Key PartitionKey `json:"key"`
	// Record is the complete post-transition state for new journal entries.
	// Legacy entries without it remain readable through their individual fields.
	Record *Record `json:"record,omitempty"`
	// Records is a single-line atomic batch for all logical namespaces sharing
	// one physical VictoriaLogs UTC-day partition.
	Records []*Record `json:"records,omitempty"`
	// State 是本条 journal 记录的迁移状态（幂等重放目标）。
	State MigrationState `json:"state"`
	// FromState 变更前状态，便于失败恢复定位。
	FromState MigrationState `json:"from_state,omitempty"`
	// Owner/Generation/DirID 记录变更后的权威/路由意图。
	Owner      Owner  `json:"owner,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	DirID      string `json:"dir_id,omitempty"`
	// AuthorityCommit 为 true 表示 OWNER_SWITCHED 原子提交已持久化。
	AuthorityCommit bool `json:"authority_commit,omitempty"`
	// JournalComplete 为 true 表示该分区 journal 进入完成标记。
	JournalComplete bool `json:"journal_complete,omitempty"`
	// Detail 可观测说明。
	Detail string `json:"detail,omitempty"`
	// AtUnixMilli 追加时间（可观测，不参与权威判定）。
	AtUnixMilli         int64                `json:"at_unix_ms,omitempty"`
	PublishedProjection *PublishedProjection `json:"published_projection,omitempty"`
	WriteRoute          WriteRoute           `json:"write_route,omitempty"`
	Dirs                []PhysicalDir        `json:"dirs,omitempty"`
	ResidualDirs        []PhysicalDir        `json:"residual_dirs,omitempty"`
	RecoveryRequired    bool                 `json:"recovery_required,omitempty"`
	PartialReasons      []string             `json:"partial_reasons,omitempty"`
}

// Journal 是 append-only 迁移 journal。
type Journal interface {
	// Append 追加一条记录；seq 必须严格递增（由实现分配或校验）。
	Append(entry JournalEntry) error
	// Entries 返回某分区的全部 journal 项（按 seq 升序）。
	Entries(key PartitionKey) []JournalEntry
	// Snapshot 返回当前全部 journal 项。
	Snapshot() []JournalEntry
	// LastSeq 返回全局最大 seq。
	LastSeq() uint64
}

// Store 是可选的 journal 文件持久化接口（JSONL 等）。
// 明确不是 SQLite 业务库。
type Store interface {
	AppendEntry(entry JournalEntry) error
	LoadEntries() ([]JournalEntry, error)
}

// MemJournal 是线程安全的内存 append-only journal。
type MemJournal struct {
	mu      sync.RWMutex
	entries []JournalEntry
	byKey   map[PartitionKey][]int
	nextSeq uint64
}

// NewMemJournal 创建空的内存 journal。
func NewMemJournal() *MemJournal {
	return &MemJournal{byKey: make(map[PartitionKey][]int)}
}

// Append 追加 journal 项。实现分配单调 seq；若调用方指定 seq，必须大于当前 last。
func (j *MemJournal) Append(entry JournalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if entry.Seq == 0 {
		j.nextSeq++
		entry.Seq = j.nextSeq
	} else {
		if entry.Seq <= j.nextSeq && j.nextSeq != 0 {
			return fmt.Errorf("%w: got %d last %d", ErrJournalOrder, entry.Seq, j.nextSeq)
		}
		if entry.Seq <= j.nextSeq {
			return fmt.Errorf("%w: got %d last %d", ErrJournalOrder, entry.Seq, j.nextSeq)
		}
		j.nextSeq = entry.Seq
	}
	idx := len(j.entries)
	j.entries = append(j.entries, entry)
	indexed := make(map[PartitionKey]bool)
	if entry.Key.StorageNamespace != "" {
		j.byKey[entry.Key] = append(j.byKey[entry.Key], idx)
		indexed[entry.Key] = true
	}
	for _, rec := range entry.Records {
		if rec != nil && !indexed[rec.Key] {
			j.byKey[rec.Key] = append(j.byKey[rec.Key], idx)
			indexed[rec.Key] = true
		}
	}
	return nil
}

// Entries 返回分区 journal 副本。
func (j *MemJournal) Entries(key PartitionKey) []JournalEntry {
	j.mu.RLock()
	defer j.mu.RUnlock()
	idxs := j.byKey[key]
	out := make([]JournalEntry, 0, len(idxs))
	for _, i := range idxs {
		out = append(out, j.entries[i])
	}
	return out
}

// Snapshot 返回全部 journal 项副本。
func (j *MemJournal) Snapshot() []JournalEntry {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]JournalEntry, len(j.entries))
	copy(out, j.entries)
	return out
}

// LastSeq 返回当前最大 seq。
func (j *MemJournal) LastSeq() uint64 {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.nextSeq
}

// JSONLFileStore 是可选文件持久化：每行一条 JSON journal entry，只追加。
type JSONLFileStore struct {
	mu   sync.Mutex
	path string
}

// NewJSONLFileStore 打开（必要时创建）路径上的 JSONL journal 文件。
func NewJSONLFileStore(path string) (*JSONLFileStore, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: journal file path required", ErrInvalidState)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("catalog: mkdir journal dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("catalog: open journal file: %w", err)
	}
	_ = f.Close()
	return &JSONLFileStore{path: path}, nil
}

// AppendEntry 追加一行 JSON。
func (s *JSONLFileStore) AppendEntry(entry JournalEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("catalog: open journal file: %w", err)
	}
	defer f.Close()
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("catalog: marshal journal entry: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("catalog: write journal entry: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("catalog: sync journal entry: %w", err)
	}
	return nil
}

// LoadEntries 读取全部 journal 项。
func (s *JSONLFileStore) LoadEntries() ([]JournalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("catalog: read journal file: %w", err)
	}
	defer f.Close()
	var out []JournalEntry
	sc := bufio.NewScanner(f)
	// journal 单行不应巨大；放大 buffer 防御异常 detail。
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e JournalEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("catalog: decode journal line: %w", err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("catalog: scan journal file: %w", err)
	}
	return out, nil
}

// JournalWithStore 组合内存 journal 与可选文件 store：每次 Append 同步写 store。
type JournalWithStore struct {
	mu    sync.Mutex
	mem   *MemJournal
	store Store
}

// NewJournalWithStore 创建带可选持久化的 journal。store 为 nil 时退化为纯内存。
func NewJournalWithStore(store Store) (*JournalWithStore, error) {
	j := &JournalWithStore{mem: NewMemJournal(), store: store}
	if store == nil {
		return j, nil
	}
	entries, err := store.LoadEntries()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err := j.mem.Append(e); err != nil {
			return nil, fmt.Errorf("catalog: replay journal store: %w", err)
		}
	}
	return j, nil
}

// Append 写入内存并同步到 store（若有）。
func (j *JournalWithStore) Append(entry JournalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	last := j.mem.LastSeq()
	if entry.Seq != 0 && entry.Seq <= last {
		return fmt.Errorf("%w: got %d last %d", ErrJournalOrder, entry.Seq, last)
	}
	if entry.Seq == 0 {
		entry.Seq = last + 1
	}
	// 先持久化，再进入内存视图（崩溃时宁可 journal 少也不出现假完成）。
	if j.store != nil {
		if err := j.store.AppendEntry(entry); err != nil {
			return err
		}
	}
	return j.mem.Append(entry)
}

// Entries implements Journal.
func (j *JournalWithStore) Entries(key PartitionKey) []JournalEntry {
	return j.mem.Entries(key)
}

// Snapshot implements Journal.
func (j *JournalWithStore) Snapshot() []JournalEntry {
	return j.mem.Snapshot()
}

// LastSeq implements Journal.
func (j *JournalWithStore) LastSeq() uint64 {
	return j.mem.LastSeq()
}

// ReplayJournal 将分区 journal 重放到幂等逻辑状态（不改变权威，除非 entry.AuthorityCommit）。
// 返回是否检测到冲突 generation。
func ReplayJournal(rec *Record, entries []JournalEntry) (conflict string) {
	if rec == nil {
		return ""
	}
	var lastAuth *JournalEntry
	for _, e := range entries {
		if len(e.Records) > 0 {
			matched := false
			for _, candidate := range e.Records {
				if candidate != nil && candidate.Key == rec.Key {
					*rec = *candidate.Clone()
					e.Owner, e.Generation, e.DirID = candidate.Owner, candidate.Generation, candidate.OwnerDirID
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		} else if e.Key != rec.Key {
			continue
		}
		if e.Record != nil {
			*rec = *e.Record.Clone()
			// 完整 Record 是新版 journal 的权威后状态；同步填充旧格式兼容字段，
			// 避免 AuthorityCommit 将缺省顶层字段误判为一次“切到空权威”。
			e.Owner, e.Generation, e.DirID = e.Record.Owner, e.Record.Generation, e.Record.OwnerDirID
		}
		if e.JournalComplete {
			rec.JournalComplete = true
		}
		if e.Seq > rec.JournalLastSeq {
			rec.JournalLastSeq = e.Seq
		}
		if e.State != "" {
			// 幂等：只接受契约内状态。
			if IsHappyPath(e.State) || IsFailure(e.State) {
				rec.MigrationState = e.State
			}
		}
		if e.AuthorityCommit {
			// 冲突：同一 journal 流中出现互相矛盾的权威提交。
			validSwitch := lastAuth != nil && e.State == StateOwnerSwitched && e.FromState == StateAttachedStaging &&
				e.Generation == lastAuth.Generation+1
			if lastAuth != nil && !validSwitch && (lastAuth.Owner != e.Owner || lastAuth.DirID != e.DirID || lastAuth.Generation != e.Generation) {
				conflict = fmt.Sprintf("authority:%s/%s/%d→%s/%s/%d",
					lastAuth.Owner, lastAuth.DirID, lastAuth.Generation,
					e.Owner, e.DirID, e.Generation)
			}
			ec := e
			lastAuth = &ec
			if e.Owner.Valid() {
				rec.Owner = e.Owner
			}
			if e.Generation != 0 {
				rec.Generation = e.Generation
			}
			if e.DirID != "" {
				rec.OwnerDirID = e.DirID
				rec.WriteRoute.Owner = rec.Owner
				rec.WriteRoute.DirID = e.DirID
				rec.WriteRoute.Generation = rec.Generation
				rec.WriteRoute.Frozen = false
				rec.WriteRoute.LateOwner = rec.Owner
				rec.WriteRoute.LateDirID = e.DirID
			}
		}
	}
	rec.ConflictGeneration = conflict
	return conflict
}
