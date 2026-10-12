//go:build fr496repro

// FR-496 生产事故（2026-10-01 04:50~05:29）的现场复现台。
//
// 这不是常规回归用例：它需要真 VictoriaLogs、真 SQLite 索引与生产状态副本，故用 build tag
// `fr496repro` 隔离，默认 `go test ./...` 不会编译它。运行方式见注释末尾。
//
// 用法：
//
//	JM_FR496_ROOT=<实验根> JM_FR496_VL=http://127.0.0.1:59463 \
//	  go test -tags fr496repro -run TestFR496RootCauseRepro -v ./internal/worker/logs/ingest/
//
// 实验根需含 var/log/ingest.state.json（旧 JSON 状态）与 var/log/events/（事件段）。
// 每一轮模拟一次「进程启动 → ingest.New（含迁移/恢复整窗重发/投影校验）」，并把该轮
// 结果的错误、以及**落库后**的投影代次打印出来——后者是判定「代次是否随重启推进」的关键。
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// reproIndexGeneration 直接读索引库里的投影代次（绕过 Manager，确认「落库真值」）。
func reproIndexGeneration(t *testing.T, root, sourceKey string) string {
	t.Helper()
	store, err := stateindex.Open(filepath.Join(root, "var", "log", "ingest.index.db"))
	if err != nil {
		t.Fatalf("打开索引失败: %v", err)
	}
	defer func() { _ = store.Close() }()
	rows, err := store.Load()
	if err != nil {
		t.Fatalf("读取索引失败: %v", err)
	}
	for _, row := range rows.Projections {
		if row.Key == sourceKey {
			return fmt.Sprintf("%s(pending=%v)", row.Generation, row.Pending)
		}
	}
	return "<无行>"
}

// reproIndexCounts 打印索引各表行数，确认迁移确实落库。
func reproIndexCounts(t *testing.T, root string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := ExportIndexJSON(root, &buf); err != nil {
		return "导出失败: " + err.Error()
	}
	var payload struct {
		Sources map[string]struct {
			ProjectionGeneration string `json:"projection_generation"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		return "解析失败: " + err.Error()
	}
	out := ""
	for key, source := range payload.Sources {
		out += fmt.Sprintf("%s=%s ", key, source.ProjectionGeneration)
	}
	return out
}

func TestFR496RootCauseRepro(t *testing.T) {
	root := os.Getenv("JM_FR496_ROOT")
	vlURL := os.Getenv("JM_FR496_VL")
	if root == "" || vlURL == "" {
		t.Skip("需要 JM_FR496_ROOT 与 JM_FR496_VL")
	}
	rounds := 3
	if value := os.Getenv("JM_FR496_ROUNDS"); value != "" {
		fmt.Sscanf(value, "%d", &rounds)
	}
	const sourceKey = "inst:142/file/instance:7d336c4a-9b22-45f2-81ca-afa0013e5573@worker:20fd52ed-1bfc-428f-8f16-2048e84ea2df"

	for round := 1; round <= rounds; round++ {
		client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: vlURL, QueryTimeout: 60 * time.Second})
		if err != nil {
			t.Fatalf("构造 VL 客户端失败: %v", err)
		}
		t.Logf("=== 第 %d 次启动：启动前落库代次 = %s", round, reproIndexGeneration(t, root, sourceKey))
		manager, err := New(Options{
			Root:                root,
			VL:                  client,
			Catalog:             catalog.New(nil),
			VerificationTimeout: 60 * time.Second,
		})
		if err != nil {
			t.Logf("=== 第 %d 次启动：ingest.New 失败: %v", round, err)
		} else {
			t.Logf("=== 第 %d 次启动：ingest.New 成功", round)
			if err := manager.CloseIndex(); err != nil {
				t.Logf("关闭索引失败: %v", err)
			}
		}
		t.Logf("=== 第 %d 次启动：启动后落库代次 = %s", round, reproIndexGeneration(t, root, sourceKey))
		t.Logf("=== 第 %d 次启动：全库代次 = %s", round, reproIndexCounts(t, root))
	}
}
