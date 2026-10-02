package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// 回归：零数据源不得否决全局切换（2026-09-30 生产实证）。
//
// inst:152/file（bungee 的空文件日志）与 inst:153/stdout（Beacon 空 stdout）从未采集到
// 任何事件（Durable==0、无 events），publishedClosedForSource 因「无任何可发布投影」判
// complete=false → 永久产出 projection_not_published → cutover 闸门恒不可满足
// （其他 11 个源全绿，仅这两个空源把全局闸门焊死）。
// 「空」是合法状态：不得因空而否决；有数据但投影确未发布者仍照旧否决（由 instances_test
// 的既有用例守住）。移除守卫即转红。
func TestCutoverReadinessTreatsEmptySourceAsReady(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(work, "logs"), 0o700))
	// 存在 latest.log 但为空：注册产生源管道，而采集不会产出任何事件。
	require.NoError(t, os.WriteFile(filepath.Join(work, "logs", "latest.log"), nil, 0o600))

	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	require.NoError(t, m.RegisterInstance("uuid-empty", "inst:empty", "holder-g1", "FILE_PRIMARY", work))

	readiness := m.CutoverReadiness()
	require.NotContains(t, strings.Join(readiness.Reasons, ","), "projection_not_published",
		"零数据源属「空」的合法状态，不得产出 projection_not_published：%v", readiness.Reasons)
	require.True(t, readiness.LedgerReady,
		"全部源均为空（无任何数据可发布）时，采集账本应视为就绪：%v", readiness.Reasons)
}
