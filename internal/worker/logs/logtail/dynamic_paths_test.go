package logtail_test

// 本文件验证「实例工作目录由 CP 运行期下发」这条约束。
//
// # 为何必须单写（真机实测踩到）
//
// 最初按 `DefaultPaths(serversDir)` 猜一个根目录推 glob。真机上生产实例在
// `/home/user/servers/*`，而 `dataRoot/var/servers` 里只有 `.pid`/`.sock`、
// 0 个实例目录 ⇒ 采集器启动正常、日志里一句“已启动”，但**一条日志也采不到**。
// 这类“静默无数据”只在真机上才暴露，所以必须有用例钉住。

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtail"
)

// TestDefaultPathsForDerivesFromWorkDirs 锤死路径推导口径。
func TestDefaultPathsForDerivesFromWorkDirs(t *testing.T) {
	got := logtail.DefaultPathsFor([]string{"/srv/inst-a", "/srv/inst-b"})
	want := map[string]bool{
		filepath.Join("/srv/inst-a", "logs", "*.log"): false,
		filepath.Join("/srv/inst-a", "proxy.log.*"):   false,
		filepath.Join("/srv/inst-b", "logs", "*.log"): false,
		filepath.Join("/srv/inst-b", "proxy.log.*"):   false,
	}
	if len(got) != len(want) {
		t.Fatalf("期望 %d 条 pattern，实际 %d：%v", len(want), len(got), got)
	}
	for _, p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("意外的 pattern: %q", p)
			continue
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("缺少 pattern: %q", p)
		}
	}
}

// TestDefaultPathsForDeduplicatesAndSkipsEmpty 钉住去重与空值处理。
func TestDefaultPathsForDeduplicatesAndSkipsEmpty(t *testing.T) {
	got := logtail.DefaultPathsFor([]string{"/srv/a", "/srv/a", "", "   "})
	if len(got) != 2 {
		t.Fatalf("同一 workDir 应只产生 2 条 pattern，实际 %d：%v", len(got), got)
	}
}

// TestCollectedFromBindingsAfterStart 是本文件的核心：**启动后**才到达的实例也要被采。
//
// 场景（生产常态）：Worker 先起，CP 随后下发实例登记。若采集路径只看启动快照，
// 这个实例的日志要等到 Worker 下次重启才被采——而 Worker 可能几个月不重启。
func TestCollectedFromBindingsAfterStart(t *testing.T) {
	root := t.TempDir()
	// 注意：**启动时绑定表里什么都没有**，路径也一个都不配。
	bindings, err := logtail.OpenBindings("", "")
	if err != nil {
		t.Fatalf("打开绑定表: %v", err)
	}

	fake := newFakeVL(t)
	runner, err := logtail.Start(t.Context(), logtail.Config{
		Enabled:          true,
		Paths:            nil, // 故意不配静态路径
		ReadFromHead:     true,
		RegistryPath:     filepath.Join(root, "registry.jsonl"),
		ScanInterval:     30 * time.Millisecond,
		ProgressInterval: 30 * time.Millisecond,
		Bindings:         bindings,
	}, fake.client(t), nil)
	if err != nil {
		t.Fatalf("启动采集器（无任何静态路径、无绑定时也应能起）: %v", err)
	}
	defer runner.Stop()

	// 启动后 CP 才下发实例：先写日志，再登记绑定。
	workDir := filepath.Join(root, "servers", "late-instance")
	logPath := filepath.Join(workDir, "logs", "latest.log")
	writeLines(t, logPath, `[18:00:01] [Server thread/INFO]: arrived-late`)

	if err := bindings.Put(logtail.BindingEntry{
		UUID: "late", TargetID: "inst:77", WorkDir: workDir, Mode: "FILE_PRIMARY",
	}); err != nil {
		t.Fatalf("写绑定: %v", err)
	}

	if !waitFor(t, 8*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("启动后到达的实例日志未被采到（采集路径被启动快照钉死了），实际 %d 行", fake.count())
	}
	rows := fake.snapshot()
	if !strings.Contains(rows[0]["_msg"], "arrived-late") {
		t.Errorf("采到的内容不对：%q", rows[0]["_msg"])
	}
	if rows[0]["log_source_id"] != "inst:77/file" {
		t.Errorf("源标签不对：%q", rows[0]["log_source_id"])
	}
}
