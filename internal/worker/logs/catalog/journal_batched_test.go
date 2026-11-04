package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestKey(ns, day string) PartitionKey {
	return PartitionKey{StorageNamespace: ns, UTCDay: day}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// TestBatchedStore_CoalescesIntoOneFile 攒批后 N 条全部落地、顺序不变、行完整。
func TestBatchedStore_CoalescesIntoOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	inner, err := NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// interval 设得极大：确保只有显式 Flush 才落盘（用来测时间窗口）。
	batched := NewBatchedStore(inner, time.Hour, 0)
	defer func() { _ = batched.Close() }()

	for i := 0; i < 5; i++ {
		rec := NewStableRecord(newTestKey("inst:1", "2026-11-0"+string(rune('1'+i))), OwnerHot, 1, "d")
		if err := batched.AppendEntry(JournalEntry{Seq: uint64(i + 1), Key: rec.Key, Record: rec}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// 未 flush 前不应有行（这是可丢弃窗口的正面证据：写入已「成功」但未落盘）。
	if got := countLines(t, path); got != 0 {
		t.Fatalf("未 flush 前不应落盘，实测 %d 行", got)
	}
	if err := batched.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := countLines(t, path); got != 5 {
		t.Fatalf("flush 后应有 5 行，实测 %d", got)
	}
	entries, err := inner.LoadEntries()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("应有 5 条，实测 %d", len(entries))
	}
	// 顺序必须与 append 顺序一致（追加日志的核心语义）。
	for i, e := range entries {
		want := "2026-11-0" + string(rune('1'+i))
		if e.Key.UTCDay != want {
			t.Fatalf("第 %d 条日期应为 %s，实测 %s", i, want, e.Key.UTCDay)
		}
	}
	stats := batched.Stats()
	if stats.Flushed != 5 || stats.FlushFailed != 0 {
		t.Fatalf("读数应为 flushed=5 failed=0，实测 %+v", stats)
	}
}

// TestBatchedStore_SyncModeIsImmediate interval<=0 ⇒ 同步写，不存在丢弃窗口。
func TestBatchedStore_SyncModeIsImmediate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	inner, err := NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	batched := NewBatchedStore(inner, 0, 0)
	defer func() { _ = batched.Close() }()
	rec := NewStableRecord(newTestKey("inst:1", "2026-11-02"), OwnerHot, 1, "d")
	if err := batched.AppendEntry(JournalEntry{Seq: 1, Key: rec.Key, Record: rec}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// 关键：不调 Flush 也必须已经落盘。
	if got := countLines(t, path); got != 1 {
		t.Fatalf("同步形态应立即落盘 1 行，实测 %d", got)
	}
}

// TestBatchedStore_MaxBatchTriggersFlush 队列上界：达 maxBatch 当场落盘。
func TestBatchedStore_MaxBatchTriggersFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	inner, err := NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	batched := NewBatchedStore(inner, time.Hour, 3)
	defer func() { _ = batched.Close() }()
	for i := 0; i < 3; i++ {
		rec := NewStableRecord(newTestKey("inst:1", "2026-11-02"), OwnerHot, 1, "d")
		if err := batched.AppendEntry(JournalEntry{Seq: uint64(i + 1), Key: rec.Key, Record: rec}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if got := countLines(t, path); got != 3 {
		t.Fatalf("达 maxBatch 应已落盘 3 行，实测 %d", got)
	}
}

// TestBatchedStore_CloseFlushesRemaining 正常关停不丢任何一条。
func TestBatchedStore_CloseFlushesRemaining(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	inner, err := NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	batched := NewBatchedStore(inner, time.Hour, 0)
	rec := NewStableRecord(newTestKey("inst:1", "2026-11-02"), OwnerHot, 1, "d")
	if err := batched.AppendEntry(JournalEntry{Seq: 1, Key: rec.Key, Record: rec}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := batched.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := countLines(t, path); got != 1 {
		t.Fatalf("Close 应落盘 1 行，实测 %d", got)
	}
	// 幂等
	if err := batched.Close(); err != nil {
		t.Fatalf("close 第二次应幂等，实测 %v", err)
	}
	// 关停后写入必须报错，不能静默丢掉。
	rec2 := NewStableRecord(newTestKey("inst:1", "2026-11-03"), OwnerHot, 1, "d")
	if err := batched.AppendEntry(JournalEntry{Seq: 2, Key: rec2.Key, Record: rec2}); !errors.Is(err, ErrBatchedStoreClosed) {
		t.Fatalf("关停后写入应返回 ErrBatchedStoreClosed，实测 %v", err)
	}
}

// failingStore 总是落盘失败（验证失败不被静默吃掉）。
type failingStore struct {
	mu    sync.Mutex
	calls int
}

func (f *failingStore) AppendEntry(JournalEntry) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return errors.New("disk full (injected)")
}

func (f *failingStore) LoadEntries() ([]JournalEntry, error) { return nil, nil }

// TestBatchedStore_FlushFailureIsObservable 落盘失败必须可观测（不静默）。
//
// 这是攒批设计唯一的安全代价：AppendEntry 会先行返回 nil，失败只能在 flush 时得知。
// 因此「失败可观测」本身就是必须被钉住的契约。
func TestBatchedStore_FlushFailureIsObservable(t *testing.T) {
	inner := &failingStore{}
	batched := NewBatchedStore(inner, time.Hour, 0)
	defer func() { _ = batched.Close() }()
	rec := NewStableRecord(newTestKey("inst:1", "2026-11-02"), OwnerHot, 1, "d")
	// 写入本身不报错——这是被明确记录的语义弱化（见 BatchedStore 注释）。
	if err := batched.AppendEntry(JournalEntry{Seq: 1, Key: rec.Key, Record: rec}); err != nil {
		t.Fatalf("攒批形态下 AppendEntry 不应报错，实测 %v", err)
	}
	if err := batched.Flush(); err == nil {
		t.Fatal("Flush 必须返回错误，不能把落盘失败吞掉")
	}
	stats := batched.Stats()
	if stats.FlushFailed != 1 {
		t.Fatalf("FlushFailed 应为 1，实测 %d", stats.FlushFailed)
	}
	if !strings.Contains(stats.LastError, "disk full") {
		t.Fatalf("LastError 应保留根因，实测 %q", stats.LastError)
	}
}

// TestJSONLFileStore_ToleratesTruncatedLastLine 崩溃残留的半行不得阻止启动。
//
// 转红方式（实测）：把 LoadEntries 的末行容忍分支（`if i == len(lines)-1` 那段）
// 删掉、改回「任何解码失败都报错」，本用例会以 `decode journal line 3` 失败。
func TestJSONLFileStore_ToleratesTruncatedLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	store, err := NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 2; i++ {
		rec := NewStableRecord(newTestKey("inst:1", "2026-11-0"+string(rune('2'+i))), OwnerHot, 1, "d")
		if err := store.AppendEntry(JournalEntry{Seq: uint64(i + 1), Key: rec.Key, Record: rec}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// 手工造一个「写了一半就被杀」的末行（无结尾换行、JSON 不闭合）。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open tail: %v", err)
	}
	if _, err := f.WriteString(`{"seq":3,"key":{"storage_namespace":"inst:1","utc_day":"2026-11`); err != nil {
		t.Fatalf("write tail: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close tail: %v", err)
	}

	entries, err := store.LoadEntries()
	if err != nil {
		t.Fatalf("末行截断应被容忍（否则崩溃后无法启动），实测报错: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("应重放前 2 条完整行，实测 %d", len(entries))
	}
	// 重放构造也必须成功——这才是真机启动路径。
	if _, err := NewJournalWithStore(store); err != nil {
		t.Fatalf("含截断末行时 NewJournalWithStore 必须能建立，实测 %v", err)
	}
}

// TestJSONLFileStore_RejectsCorruptMiddleLine 非末行损坏仍是硬错误（判据不放宽）。
func TestJSONLFileStore_RejectsCorruptMiddleLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	store, err := NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 3; i++ {
		rec := NewStableRecord(newTestKey("inst:1", "2026-11-0"+string(rune('2'+i))), OwnerHot, 1, "d")
		if err := store.AppendEntry(JournalEntry{Seq: uint64(i + 1), Key: rec.Key, Record: rec}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("应有 3 行，实测 %d", len(lines))
	}
	// 把**中间**行弄坏（模拟文件被改写/被另一个写者踩过）。
	lines[1] = "{not json"
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if _, err := store.LoadEntries(); err == nil {
		t.Fatal("中间行损坏必须报错，不能静默跳过（那会把真损坏当成正常尾巴）")
	}
}

// TestBatchedStore_LoadEntriesSeesPending LoadEntries 前会先 flush，读者不会看到缺数据。
func TestBatchedStore_LoadEntriesSeesPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	inner, err := NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	batched := NewBatchedStore(inner, time.Hour, 0)
	defer func() { _ = batched.Close() }()
	rec := NewStableRecord(newTestKey("inst:1", "2026-11-02"), OwnerHot, 1, "d")
	if err := batched.AppendEntry(JournalEntry{Seq: 1, Key: rec.Key, Record: rec}); err != nil {
		t.Fatalf("append: %v", err)
	}
	entries, err := batched.LoadEntries()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("LoadEntries 应包含待落盘条目，实测 %d", len(entries))
	}
}

// TestBatchedStore_ReplayRestoresPartitions 端到端：攒批写入 → 用同一文件重建 Catalog，
// 分区记录必须全部回来。这是整条修复的**真正的验收断言**（重启后历史天可查）。
func TestBatchedStore_ReplayRestoresPartitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	inner, err := NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	batched := NewBatchedStore(inner, time.Hour, 0)
	// 走 JournalWithStore 的真实路径（seq 由它分配）。
	journal, err := NewJournalWithStore(batched)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	days := []string{"2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05"}
	for _, day := range days {
		key := newTestKey("inst:318", day)
		rec := NewStableRecord(key, OwnerHot, 1, "hot-"+day)
		if err := journal.Append(JournalEntry{Key: key, Record: rec}); err != nil {
			t.Fatalf("append %s: %v", day, err)
		}
	}
	if err := batched.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 「重启」：新开一个 store + journal，从同一文件重建。
	reopened, err := NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	replayed, err := NewJournalWithStore(reopened)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	cat := New(replayed)
	got := map[string]bool{}
	for _, k := range cat.Keys() {
		if k.StorageNamespace == "inst:318" {
			got[k.UTCDay] = true
		}
	}
	for _, day := range days {
		if !got[day] {
			t.Fatalf("重启后 inst:318 的 %s 分区记录必须仍存在，实测缺失（got=%v）", day, got)
		}
	}
}
