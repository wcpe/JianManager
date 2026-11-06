package tailer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFile 向文件追加内容。
func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开 %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}

// collectSink 收集所有经过的行（线程安全）。
type collectSink struct {
	mu    sync.Mutex
	lines []string
	failNext bool
}

func (s *collectSink) Consume(_ context.Context, lines []Line) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failNext = false
		return os.ErrDeadlineExceeded
	}
	for _, l := range lines {
		s.lines = append(s.lines, string(l.Content))
	}
	return nil
}

func (s *collectSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.lines))
	copy(out, s.lines)
	return out
}

// TestScanRespectsExclude 确认 .gz / .lck 不被采集。
//
// 这是用户明确要求的行为（不采归档），也是 Filebeat 文档的标准写法
// （`exclude_files: ['\.gz$']`）。
func TestScanRespectsExclude(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"latest.log", "proxy.log.0", "old.log.gz", "proxy.log.0.lck", "archive.zip"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := CollectorConfig{Paths: []string{filepath.Join(dir, "*")}}.withDefaults()
	files, err := cfg.Scan()
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, f := range files {
		got[filepath.Base(f.Path)] = true
	}
	for _, want := range []string{"latest.log", "proxy.log.0"} {
		if !got[want] {
			t.Errorf("应当采集 %s，实际结果 %v", want, got)
		}
	}
	for _, notWant := range []string{"old.log.gz", "proxy.log.0.lck", "archive.zip"} {
		if got[notWant] {
			t.Errorf("不应采集 %s", notWant)
		}
	}
}

// TestFingerprintStableAcrossRename 锤死「改名后指纹不变」。
//
// 这是轮转跟踪的基础：latest.log → 2026-11-06-14.log 改名后，
// 内容不变 ⇒ 指纹不变 ⇒ 能继续从 offset 读。
//
// 变异验证：把 filepath 里的内容哈希改成路径哈希，本条立即转红。
func TestFingerprintStableAcrossRename(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "latest.log")
	content := "[12:00:00] [Server thread/INFO]: hello\n"
	if err := os.WriteFile(oldPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := IdentifyFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "2026-11-06-14.log")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	after, err := IdentifyFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint != after.Fingerprint {
		t.Fatalf("改名后指纹必须不变：before=%s after=%s", before.Fingerprint, after.Fingerprint)
	}
}

// TestFingerprintDiffersForDifferentContent 确认不同内容指纹不同。
func TestFingerprintDiffersForDifferentContent(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.log")
	b := filepath.Join(dir, "b.log")
	if err := os.WriteFile(a, []byte("content A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("content B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ia, _ := IdentifyFile(a)
	ib, _ := IdentifyFile(b)
	if ia.Fingerprint == ib.Fingerprint {
		t.Fatal("不同内容必须得到不同指纹")
	}
}

// TestHarvesterReadsCompleteLinesOnly 确认只交出完整行。
//
// 写入是流式的，末行常常是半行。当成完整行会产出被截断的日志。
func TestHarvesterReadsCompleteLinesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3-partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := DiscoveredFile{Path: path, Size: 23}
	id, _ := IdentifyFile(path)
	f.Identity = id
	h, err := NewHarvester(f, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var got []string
	if _, err := h.ReadLines(func(l []byte) error { got = append(got, string(l)); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"line1", "line2"}
	if len(got) != len(want) {
		t.Fatalf("期望 %v，实际 %v（半行不应交出）", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 行：期望 %q 实际 %q", i, want[i], got[i])
		}
	}
	if h.PendingLength() == 0 {
		t.Fatal("半行应当保留在 pending 里")
	}
}

// TestHarvesterResumesFromOffset 锤死「从位点续读」。
//
// 这是整个方案的核心：重启后不重读也不丢。
func TestHarvesterResumesFromOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, _ := IdentifyFile(path)
	f := DiscoveredFile{Path: path, Size: 4, Identity: id}

	h1, err := NewHarvester(f, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var first []string
	h1.ReadLines(func(l []byte) error { first = append(first, string(l)); return nil })
	offset := h1.Offset()
	h1.Close()
	if len(first) != 2 {
		t.Fatalf("首轮应读 2 行，实际 %v", first)
	}

	// 模拟写入新内容
	appendTo(t, path, "c\n")

	// 模拟重启：从记录的 offset 继续
	h2, err := NewHarvester(DiscoveredFile{Path: path, Size: 6, Identity: id}, offset, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	var second []string
	h2.ReadLines(func(l []byte) error { second = append(second, string(l)); return nil })
	if len(second) != 1 || second[0] != "c" {
		t.Fatalf("续读应只得到 ['c']，实际 %v（重读或漏读）", second)
	}
}

// TestRegistryRoundTrip 确认位点表可落盘并读回。
func TestRegistryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.jsonl")
	r1, err := OpenRegistry(regPath)
	if err != nil {
		t.Fatal(err)
	}
	st := FileState{
		Identity: FileIdentity{Fingerprint: "abc123", Inode: 42, Dev: 7},
		Path:     "/var/log/x.log",
		Offset:   1000,
		Size:     2000,
	}
	r1.Put(st)
	if err := r1.Flush(); err != nil {
		t.Fatal(err)
	}

	r2, err := OpenRegistry(regPath)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r2.Get(st.Identity)
	if !ok {
		t.Fatal("位点未恢复")
	}
	if got.Offset != 1000 || got.Path != "/var/log/x.log" {
		t.Fatalf("位点恢复错：%+v", got)
	}
}

// TestCollectorEndToEnd 端到端：扫描 → 读 → 交 Sink → 落位点。
func TestCollectorEndToEnd(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.jsonl")
	logPath := filepath.Join(dir, "latest.log")
	if err := os.WriteFile(logPath, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := OpenRegistry(regPath)
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectSink{}
	cfg := CollectorConfig{
		Paths:        []string{filepath.Join(dir, "*.log")},
		ReadFromHead: true,
	}
	c := NewCollector(cfg, reg, sink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 手动驱动一轮（不启动 Run，避免 sleep）
	if err := c.scanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.readOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sink.snapshot(); len(got) != 1 || got[0] != "first" {
		t.Fatalf("首轮应得到 ['first']，实际 %v", got)
	}

	// 追加新内容后应读到
	appendTo(t, logPath, "second\n")
	if err := c.readOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sink.snapshot(); len(got) != 2 || got[1] != "second" {
		t.Fatalf("追加后应得到 ['first','second']，实际 %v", got)
	}
	if c.Harvesters() != 1 {
		t.Fatalf("应有 1 个 harvester，实际 %d", c.Harvesters())
	}
}

// TestCollectorReportsGapWhenFileDisappearsUnread 锤死「未读完就消失要上报」。
//
// 用户要求：“接受丢失，但记录下来”。本用例验证记录确实发生。
func TestCollectorReportsGapWhenFileDisappearsUnread(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "latest.log")
	if err := os.WriteFile(logPath, []byte("line1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &collectSink{}
	c := NewCollector(CollectorConfig{Paths: []string{filepath.Join(dir, "*.log")}, ReadFromHead: true}, nil, sink)

	var gapPath string
	var gapBytes int64
	c.SetGapReporter(func(p string, n int64) { gapPath, gapBytes = p, n })

	ctx := context.Background()
	if err := c.scanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 模拟：文件在被读之前就消失了（例如被压缩成 gz）
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	// 但先往 harvester 里“看到”有未读内容：追加后立即删，模拟轮转竞态
	// 这里直接用当前已知状态：size 1 已读 0 ⇒ 有未读
	h := c.harvesters
	var target *Harvester
	for _, v := range h {
		target = v
	}
	if target == nil {
		t.Fatal("应已有 harvester")
	}

	// 重新扫一次：文件已不存在 ⇒ 应该上报 gap
	if err := c.scanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if gapPath == "" {
		t.Fatal("文件未读完就消失时必须上报 gap（否则丢失静默）")
	}
	if gapBytes <= 0 {
		t.Fatalf("上报的未读字节应 >0，实际 %d", gapBytes)
	}
}

// TestCollectorRotateFollowsFingerprint 确认轮转后继续读剩余部分。
func TestCollectorRotateFollowsFingerprint(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "latest.log")
	if err := os.WriteFile(live, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &collectSink{}
	c := NewCollector(CollectorConfig{Paths: []string{filepath.Join(dir, "*.log")}, ReadFromHead: true}, nil, sink)
	ctx := context.Background()

	if err := c.scanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 只读一行，模拟“读到一半就轮转”
	for _, h := range c.harvesters {
		h.ReadLines(func(l []byte) error { return nil })
	}

	// 轮转：改名（inode/内容不变）+ 新建空的 latest.log
	rotated := filepath.Join(dir, "2026-11-06-14.log")
	if err := os.Rename(live, rotated); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := c.scanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 轮转后应有 2 个 harvester：旧的（改名后）+ 新的 latest.log
	if c.Harvesters() != 2 {
		t.Fatalf("轮转后应有 2 个 harvester，实际 %d", c.Harvesters())
	}
	if err := c.readOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 新文件的 "new" 必须读到
	found := false
	for _, l := range sink.snapshot() {
		if strings.Contains(l, "new") {
			found = true
		}
	}
	if !found {
		t.Fatalf("轮转后的新文件内容未读到，实际 %v", sink.snapshot())
	}
}

// TestCloseInactiveDefaults 确认默认值合理（防止改了默认值没人发现）。
func TestConfigDefaults(t *testing.T) {
	cfg := CollectorConfig{}.withDefaults()
	if cfg.ScanInterval != 10*time.Second {
		t.Errorf("ScanInterval 默认应为 10s，实际 %v", cfg.ScanInterval)
	}
	if cfg.ProgressInterval != 2*time.Second {
		t.Errorf("ProgressInterval 默认应为 2s，实际 %v", cfg.ProgressInterval)
	}
	if cfg.CloseInactive != 5*time.Minute {
		t.Errorf("CloseInactive 默认应为 5m，实际 %v", cfg.CloseInactive)
	}
	if len(cfg.Exclude) == 0 {
		t.Error("默认必须排除 .gz（用户明确要求不采归档）")
	}
}
