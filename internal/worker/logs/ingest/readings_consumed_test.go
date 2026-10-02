package ingest

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestResumeReadingsAreConsumedCounted（用户 2026-10-05 定的**更稳写法** ✓✓）：
// 可达性用**包内调用计数**证明 —— 不读外部源码、不依赖测试工作目录深度 ✗。
//
// 现场读法：分钟打点行里的 `consumed_diag/stats/inv` **逐分钟增长** ⇒ 接线活着 ✓；
// 恒 0 ⇒ 接线断了（= 第四次"无消费面" ✗✗）。
//
// 转红方式（实测两条，各自独立红 ✓）：
//   - 变异①：删掉任一方法入口的自增 ⇒ 对应计数不动 ⇒ 红；
//   - 变异②：把 `consumed_*` 从打点行里删掉（现场就看不到"是否被消费" ✗）⇒ 红。
func TestResumeReadingsAreConsumedCounted(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	src := SourceConfig{
		LogSourceID: "inst:consumed/file", SourceGeneration: "g1", Path: root + "/c.log",
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:consumed", UTCDay: runtimeTestUTCDay(),
	}
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(src.Path, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o644))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src}})
	require.NoError(t, err)
	pipe := m.pipes["inst:consumed/file/g1"]
	require.NotNil(t, pipe)
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "wal backlog limit exceeded (entries=5/4)"))

	// 调用前：全 0 ✓（新 Manager 未消费过 ✓）。
	require.Equal(t, int64(0), m.readingsConsumedStats.Load())
	require.Equal(t, int64(0), m.readingsConsumedInvariants.Load())

	// 消费一次：计数必须递增 ✓（这就是"可达面被调用"的直接证据 ✓）。
	_ = m.ResumeSweepStats()
	_ = m.PositionInvariantViolations()
	require.Equal(t, int64(1), m.readingsConsumedStats.Load(), "ResumeSweepStats 消费计数 ✓")
	require.Equal(t, int64(1), m.readingsConsumedInvariants.Load(), "PositionInvariantViolations 消费计数 ✓")

	// 打点行必须**带上**这三个计数（现场据此判断接线是否活着 ✓）。
	lines := m.ResumeDiagnosticLines()
	require.NotEmpty(t, lines)
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "consumed_diag=")
	require.Contains(t, joined, "consumed_stats=1")
	require.Contains(t, joined, "consumed_inv=1")
	require.GreaterOrEqual(t, m.readingsConsumedDiagnostics.Load(), int64(1))
}
