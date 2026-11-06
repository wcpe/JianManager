package logtail_test

// 本文件是 p2.2–p2.5 的端到端验收：真启动 Runner（真 tailer + 真 VL 写入路径），
// 只把 VL 换成一个记录请求的假 HTTP 服务。
//
// # 为什么不用 mock 替掉 tailer
//
// 这四条要验的正是 tailer 的行为（offset 续读 / glob / exclude / 轮转跟随）。
// 如果把 tailer 也 mock 掉，测的就只是我们自己写的假货。
// 只 mock VL（真正的外部依赖）才能让它们成为对真实链路的验收。

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtail"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// fakeVL 是一个记录写入行的假 VictoriaLogs。
type fakeVL struct {
	mu   sync.Mutex
	rows []map[string]string
	srv  *httptest.Server
}

func newFakeVL(t *testing.T) *fakeVL {
	t.Helper()
	f := &fakeVL{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/insert/jsonline") {
			http.NotFound(w, r)
			return
		}
		// 生产环境 VL 收的就是 gzip 请求体（vlsup 默认开启压缩），
		// 所以这里也按 gzip 解——不解会得到一堆二进制碎片，测出来的是假失败。
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, zerr := gzip.NewReader(r.Body)
			if zerr != nil {
				t.Errorf("解压请求体: %v", zerr)
				http.Error(w, "bad gzip", http.StatusBadRequest)
				return
			}
			defer zr.Close()
			reader = zr
		}
		body, _ := io.ReadAll(reader)
		f.mu.Lock()
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			if line == "" {
				continue
			}
			var row map[string]string
			if json.Unmarshal([]byte(line), &row) == nil {
				f.rows = append(f.rows, row)
			}
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"rows":1}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeVL) snapshot() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]string, len(f.rows))
	copy(out, f.rows)
	return out
}

func (f *fakeVL) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func (f *fakeVL) client(t *testing.T) *vlsup.Client {
	t.Helper()
	c, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("构造 VL 客户端: %v", err)
	}
	return c
}

// waitFor 轮询直到条件成立或超时。
//
// 采集是周期性的（scan 10s / progress 2s），所以这里必须等；用短间隔让测试尽快完成。
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// writeLines 写一个带换行的日志文件。
func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开 %s: %v", path, err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("写 %s: %v", path, err)
		}
	}
}

// newRunner 启动一个测试用 Runner（短周期，便于测试快速收敛）。
func newRunner(t *testing.T, root string, paths []string, exclude []string, readFromHead bool, bindings *logtail.Bindings) (*logtail.Runner, *fakeVL) {
	t.Helper()
	fake := newFakeVL(t)
	runner, err := logtail.Start(context.Background(), logtail.Config{
		Enabled:          true,
		Paths:            paths,
		Exclude:          exclude,
		ReadFromHead:     readFromHead,
		RegistryPath:     filepath.Join(root, "registry.jsonl"),
		ScanInterval:     30 * time.Millisecond,
		ProgressInterval: 30 * time.Millisecond,
		TimeZone:         time.UTC,
		Bindings:         bindings,
	}, fake.client(t), nil)
	if err != nil {
		t.Fatalf("启动采集器: %v", err)
	}
	if runner == nil {
		t.Fatal("Enabled=true 时必须返回 Runner")
	}
	t.Cleanup(runner.Stop)
	return runner, fake
}

// newBindings 造一份内存绑定表（把 workDir 映成 inst:1）。
func newBindings(t *testing.T, workDir string) *logtail.Bindings {
	t.Helper()
	b, err := logtail.OpenBindings("", "")
	if err != nil {
		t.Fatalf("打开绑定表: %v", err)
	}
	if err := b.Put(logtail.BindingEntry{UUID: "u1", TargetID: "inst:1", WorkDir: workDir, Mode: "FILE_PRIMARY"}); err != nil {
		t.Fatalf("写绑定: %v", err)
	}
	return b
}

// TestCollectorWritesLinesToVL 是最小烟囱测试：
// 文件里的行最终以带标签的 JSON 形式到达 VL。
func TestCollectorWritesLinesToVL(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "game-01")
	logPath := filepath.Join(workDir, "logs", "latest.log")
	writeLines(t, logPath, `[18:00:01] [Server thread/INFO]: hello`, `[18:00:02] [Server thread/ERROR]: boom`)

	_, fake := newRunner(t, root, []string{filepath.Join(root, "servers", "*", "logs", "*.log")}, nil, true, newBindings(t, workDir))

	if !waitFor(t, 5*time.Second, func() bool { return fake.count() >= 2 }) {
		t.Fatalf("期望至少 2 行到达 VL，实际 %d", fake.count())
	}
	rows := fake.snapshot()
	for _, r := range rows {
		if r["log_source_id"] != "inst:1/file" {
			t.Errorf("log_source_id 应为 inst:1/file，实际 %q", r["log_source_id"])
		}
		if r["stream"] != "stdout" {
			t.Errorf("stream 应为 stdout，实际 %q", r["stream"])
		}
		if _, err := time.Parse(time.RFC3339Nano, r["_time"]); err != nil {
			t.Errorf("_time 必须是 RFC3339，实际 %q", r["_time"])
		}
	}
}

// TestCollectorResumesFromRegistryAcrossRestart 是 p2.2 的验收。
//
// # 为什么必须单写这条
//
// 现有的端到端用例只验证「能采到东西」，抓不到「忽略 registry 位点」这个变异：
// 只要 Collector 是新建的、且文件里就那么多行，忽略位点与否看起来都一样（重读会重复写）。
// 所以这条把「重启」显式造出来：先采一轮 → 停 → 追加行 → 用**同一个 registry 路径**
// 重新起一个 Collector → 断言只收到了新增的那一行。
func TestCollectorResumesFromRegistryAcrossRestart(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "game-01")
	logPath := filepath.Join(workDir, "logs", "latest.log")
	writeLines(t, logPath, `[18:00:01] [Server thread/INFO]: first`)
	paths := []string{filepath.Join(root, "servers", "*", "logs", "*.log")}

	runner1, fake1 := newRunner(t, root, paths, nil, true, newBindings(t, workDir))
	if !waitFor(t, 5*time.Second, func() bool { return fake1.count() >= 1 }) {
		t.Fatalf("首轮未采到行，实际 %d", fake1.count())
	}
	runner1.Stop()

	// 停机期间新增一行（真实世界的常态：进程还在跑，日志还在写）。
	writeLines(t, logPath, `[18:00:02] [Server thread/INFO]: second`)

	// 用同一个 root 重新起（registry.jsonl 在同一路径，会被重新加载）。
	runner2, fake2 := newRunner(t, root, paths, nil, true, newBindings(t, workDir))
	if !waitFor(t, 5*time.Second, func() bool { return fake2.count() >= 1 }) {
		t.Fatalf("重启后未采到新增行，实际 %d", fake2.count())
	}
	// 给可能的重复写入一点时间显形。
	time.Sleep(200 * time.Millisecond)
	runner2.Stop()

	rows := fake2.snapshot()
	if len(rows) != 1 {
		t.Fatalf("重启后应只采到 1 条新增行（位点续读），实际 %d 条：%v", len(rows), rows)
	}
	if !strings.Contains(rows[0]["_msg"], "second") {
		t.Errorf("采到的应是新增行 second，实际 %q", rows[0]["_msg"])
	}
}

// TestCollectorGlobMultipleInstances 是 p2.3 的验收：一条 glob 覆盖多个实例目录。
func TestCollectorGlobMultipleInstances(t *testing.T) {
	root := t.TempDir()
	bindings, err := logtail.OpenBindings("", "")
	if err != nil {
		t.Fatalf("打开绑定表: %v", err)
	}
	for i, name := range []string{"game-01", "game-02", "proxy-01"} {
		workDir := filepath.Join(root, "servers", name)
		writeLines(t, filepath.Join(workDir, "logs", "latest.log"), `[18:00:0`+string(rune('1'+i))+`] [Server thread/INFO]: `+name)
		if err := bindings.Put(logtail.BindingEntry{
			UUID: name, TargetID: "inst:" + string(rune('1'+i)), WorkDir: workDir, Mode: "FILE_PRIMARY",
		}); err != nil {
			t.Fatalf("写绑定: %v", err)
		}
	}

	_, fake := newRunner(t, root, []string{filepath.Join(root, "servers", "*", "logs", "*.log")}, nil, true, bindings)
	if !waitFor(t, 5*time.Second, func() bool { return fake.count() >= 3 }) {
		t.Fatalf("期望 3 个实例各 1 行，实际 %d", fake.count())
	}

	seen := map[string]bool{}
	for _, r := range fake.snapshot() {
		seen[r["log_source_id"]] = true
	}
	for _, want := range []string{"inst:1/file", "inst:2/file", "inst:3/file"} {
		if !seen[want] {
			t.Errorf("缺少源 %s 的行（现看到 %v）", want, seen)
		}
	}
}

// TestCollectorExcludePreventsGz 是 p2.4 的验收：.gz 不采。
//
// 用户明确要求“不采归档”，而 gz 采进来会是一堆二进制垃圾。
func TestCollectorExcludePreventsGz(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "game-01")
	logsDir := filepath.Join(workDir, "logs")
	writeLines(t, filepath.Join(logsDir, "latest.log"), `[18:00:01] [Server thread/INFO]: live`)
	// 归档文件（内容不是真 gzip 也无所谓——只要它被 glob 匹配又必须被排除）。
	writeLines(t, filepath.Join(logsDir, "2026-11-05-1.log.gz"), `not really gzip`)

	_, fake := newRunner(t, root, []string{filepath.Join(logsDir, "*")}, []string{"*.gz"}, true, newBindings(t, workDir))
	if !waitFor(t, 5*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("未采到任何行，实际 %d", fake.count())
	}
	time.Sleep(200 * time.Millisecond)
	for _, r := range fake.snapshot() {
		if strings.Contains(r["_msg"], "not really gzip") {
			t.Fatalf(".gz 被采了（应被 exclude 排除）：%v", r)
		}
	}
}

// TestCollectorFollowsRotation 是 p2.5 的验收：轮转跟随。
//
// 形态（真实 MC 的每日轮转）：
//
//	latest.log 改名成 2026-11-05-1.log，然后新建一个 latest.log
//
// 期望：旧文件的剩余部分仍被读完，新文件从头采；两者都归到同一个实例源。
func TestCollectorFollowsRotation(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "game-01")
	logsDir := filepath.Join(workDir, "logs")
	live := filepath.Join(logsDir, "latest.log")
	writeLines(t, live, `[18:00:01] [Server thread/INFO]: before-rotation`)

	_, fake := newRunner(t, root, []string{filepath.Join(logsDir, "*.log")}, nil, true, newBindings(t, workDir))
	if !waitFor(t, 5*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("首行未采到，实际 %d", fake.count())
	}

	// 轮转：改名 + 新建（MC/log4j 的等价形态；不是 copytruncate）。
	rotated := filepath.Join(logsDir, "2026-11-05-1.log")
	if err := os.Rename(live, rotated); err != nil {
		t.Fatalf("轮转改名: %v", err)
	}
	writeLines(t, live, `[18:00:02] [Server thread/INFO]: after-rotation`)

	if !waitFor(t, 8*time.Second, func() bool {
		n := 0
		for _, r := range fake.snapshot() {
			if strings.Contains(r["_msg"], "after-rotation") {
				n++
			}
		}
		return n >= 1
	}) {
		t.Fatalf("轮转后的新文件未被采到，已收到 %d 条：%v", fake.count(), fake.snapshot())
	}

	// 轮转前的行不得因改名而重复上报。
	n := 0
	for _, r := range fake.snapshot() {
		if strings.Contains(r["_msg"], "before-rotation") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("轮转前的行应恰好 1 次，实际 %d 次（改名后被当新文件重读了）", n)
	}
}

// TestCollectorSkipsFilesWithoutBinding 是针对「先读后丢」的回归测试。
//
// # 这个 bug 不可见且致命
//
// 不加采集闸时：文件被读 → resolver 返回空 → 行被丢 → **offset 依然推进** →
// 绑定到达后再也不会重读。表现为“历史日志永远缺一段”，而日志里只有一条计数。
func TestCollectorSkipsFilesWithoutBinding(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "game-01")
	logPath := filepath.Join(workDir, "logs", "latest.log")
	writeLines(t, logPath, `[18:00:01] [Server thread/INFO]: historical`)
	paths := []string{filepath.Join(root, "servers", "*", "logs", "*.log")}

	// 起时绑定表里没有这个目录。
	empty, err := logtail.OpenBindings("", "")
	if err != nil {
		t.Fatalf("打开绑定表: %v", err)
	}
	runner, fake := newRunner(t, root, paths, nil, true, empty)
	time.Sleep(400 * time.Millisecond)
	if fake.count() != 0 {
		t.Fatalf("无绑定时应一行都不写，实际 %d", fake.count())
	}

	// 绑定到达（CP 下发登记）——同一份绑定表实例，就地更新。
	if err := empty.Put(logtail.BindingEntry{UUID: "u1", TargetID: "inst:1", WorkDir: workDir, Mode: "FILE_PRIMARY"}); err != nil {
		t.Fatalf("写绑定: %v", err)
	}
	if !waitFor(t, 8*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("绑定到达后应从头采到历史行，实际 %d", fake.count())
	}
	if !strings.Contains(fake.snapshot()[0]["_msg"], "historical") {
		t.Errorf("采到的应是历史行，实际 %q", fake.snapshot()[0]["_msg"])
	}
	runner.Stop()
}
