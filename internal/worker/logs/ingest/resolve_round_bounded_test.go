package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestResolveGapsRoundIsBoundedAndYields（现场终章 ③ 的**端到端**红证）：
// **解算（恢复期重活）进行时，采集轮必须有界完成，且段读聚合在轮内真的让过路**。
//
// 触发态（已查明 ✓）：段读聚合 `publishedClosedForSourceCtx` 只在**解算路径**
// `resolveOneSourceGaps`（runtime.go:985）被调用 ⇒ 正常 `pollOnce` 轮根本不走它 ✗
// （此前我的草稿据 `pollOnce` 断言 ⇒ 让路计数 0 ⇒ 那不是 bug 而是触发条件未满足 ✓）。
//
// 转红方式（实测）：去掉段读的逐行让路 ⇒ 让路计数 0 ⇒ 本用例在「必须真的让路」处红 ✓；
// 去掉轮内预算 ⇒ 截断计数 0（且大源扫描跑到底 ⇒ 解算墙钟随数据量无界增长 ✗）。
func TestResolveGapsRoundIsBoundedAndYields(t *testing.T) {
	// **窄口径豁免（race 构建）**：本用例的两个见证（让路计数、超预算截断）都依赖"逐行聚合在
	// 预算内走到第 N 片"这一**时间比例** ✓，而竞态检测器把逐行开销放大 10–20× ⇒ 比例在两种构建间
	// 不可通约 ✗（实测：把预算放大 100× 仍不稳 ✗）。**替代覆盖（指名 ✓）**：
	//   - 让路语义：`TestPublishedClosedScanYieldsToCollection`（计数确定性 ✓，race 下绿 ✓）；
	//   - 轮内预算：`TestPublishedClosedScanRespectsRoundBudget`（截断计数确定性 ✓，race 下绿 ✓）；
	//   - 非 race 构建下本用例照跑 ✓（两处变异各自必红：去掉让路 / 去掉 WithTimeout ✓）。
	// 不打裸 skip ✗：被豁免的只是"两者在同一轮内同时可观测"这一条，两条性质本身各有替代用例 ✓。
	if raceEnabled {
		t.Skip("race 构建下时间比例不可通约；本用例的两条性质分别由 TestPublishedClosedScanYieldsToCollection 与 TestPublishedClosedScanRespectsRoundBudget 覆盖 ✓")
	}
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	buf := make([]byte, 0, 1<<20)
	for i := 0; i < 3000; i++ { // 让逐行聚合足够长，让路可观测 ✓
		buf = append(buf, []byte(fmt.Sprintf("[12:%02d:%02d] [Server thread/INFO]: line %d\n", i/60%60, i%60, i))...)
	}
	require.NoError(t, os.WriteFile(logPath, buf, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:resolveheavy/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:resolveheavy", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	// 前置：一次采集让事件落库 ✓ + 造一条**允许名单内**的缺口（解算才会真的干活 ✓）。
	m.pollOnce()
	closed, complete := m.publishedClosedForSource(source, m.state.Sources[key])
	require.True(t, complete)
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, closed, ledger.GapReasonDeliverError, "transport down"))

	// 让路 + 轮内预算（切片取小 ⇒ 确定性见证 ✓；预算压小 ⇒ 截断必然发生 ✓）。
	yields := 0
	m.scanSliceRows = 64
	m.scanYield = 50 * time.Microsecond
	m.scanYieldNotify = func() { yields++ }
	m.scanBudget = 3 * time.Millisecond
	if raceEnabled {
		// `-race` 下把**见证参数**缩放（不动任何被测时标 ✗）：检测器把每行聚合放大 10–20× ⇒
		// 3ms 预算可能在第一片之前就截断 ⇒ 让路计数 0 ⇒ 与"让路失效"无法区分 ✗（实测红 ✓）。
		// 缩小切片 + 放大预算，使两种构建下都能同时见证"让过路"与"截断" ✓（两处变异仍必红 ✓）。
		m.scanSliceRows = 16
		m.scanBudget = 300 * time.Millisecond
	}

	started := time.Now()
	resolveErr := m.resolveOneSourceGaps(context.Background(), key, pipe, source)
	elapsed := time.Since(started)
	t.Logf("解算结束：err=%v elapsed=%s yields=%d 截断=%d", resolveErr, elapsed, yields, m.scanBudgetExceeded.Load())

	require.GreaterOrEqual(t, yields, 1,
		"解算路径的段读聚合必须真的让路（实测 %d 次；不让路 = 恢复期重活把一个 P 占满整轮 ✗）", yields)
	require.GreaterOrEqual(t, m.scanBudgetExceeded.Load(), int64(1),
		"超预算必须按不完整截断（实测 %d 次；无上界 ⇒ 解算墙钟随数据量无界增长 ✗）",
		m.scanBudgetExceeded.Load())
	require.Less(t, elapsed, 30*time.Second,
		"解算必须**有界完成**（实测 %s；现场形态是 pollOnce 的 WaitGroup 等分钟级 ✗）", elapsed)
}
