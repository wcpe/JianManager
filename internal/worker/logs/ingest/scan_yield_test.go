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
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 2026-11-02 投递路径简化：本文件两条用例守的是「`publishedClosedForSourceCtx` 的**段读逐行聚合**
// 必须按切片让路 / 必须有轮内预算」。而那次聚合的唯一消费者是「已发布投影的封闭水位」——
// 投递不再调用 `publishSnapshot` ⇒ 该水位对任何源都恒不完整 ⇒ 整条聚合已经从**所有生产路径**上
// 消失（实测：`publishedClosedForSourceCtx` 在非测试代码里已无调用者，解算改走
// `autoResolveGapsFromPublished` 的 VL 可见性取证）。
//
// 被测对象不存在了，两条用例保留为 Skip 而不是删除：
//   - 它们记录的现场形态（2026-10-02 压测，单源 18GB 段扫描把一个 P 占满整轮）仍是有价值的事故档；
//   - 「单源大扫描不得撑满整轮」这条关切现在由**解算的按源预算 + 源间轮转游标**承担
//     （`ResolveGapsBudgetIsBoundedAndResumable` / `ResolveCoveredGapsLockHoldIsBoundedPerSource`）。
//
// TestPublishedClosedScanYieldsToCollection（现场终章 ③）：**段读聚合路径必须按切片让路**。
//
// 现场形态：`pollOnce` 在 WaitGroup 上等 1 分钟 ✗，workers 全在跑恢复期重活——其中一条被点名的
// 就是 `publishedClosedForSourceCtx→readSegment` 的**逐行段扫描**（单源数十万行、全库 18GB ✗）。
// 它本就可被 ctx 取消 ✓，但**从不让路** ⇒ 单源扫描可把一个 P 占满整轮 ✗。
//
// 转红方式（实测）：去掉逐行回调里的让路（只留 ctx 校验）⇒ 让路计数为 0 ⇒ 本用例变红 ✓。
func TestPublishedClosedScanYieldsToCollection(t *testing.T) {
	t.Skip("投递路径简化（2026-11-02）：段读聚合已从所有生产路径消失（不再发布投影），让路失去对象")
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	lines := make([]byte, 0, 4096)
	for i := 0; i < 300; i++ {
		lines = append(lines, []byte(fmt.Sprintf("[12:00:%02d] [Server thread/INFO]: line %d\n", i%60, i))...)
	}
	require.NoError(t, os.WriteFile(logPath, lines, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:scan/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:scan", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	require.NotNil(t, m.pipes[key])

	// 先把事件落进事件库（段读聚合的数据源 ✓）。
	m.pollOnce()

	yields := 0
	m.scanSliceRows = 32           // 300 行 ⇒ 至少 9 次让路（用小切片让断言确定性 ✓）
	m.scanYield = time.Millisecond // 真睡（交还 P 是让路的实质 ✓）；量小（~9ms）不影响用例时长 ✓
	m.scanYieldNotify = func() { yields++ }
	closed, complete, err := m.publishedClosedForSourceCtx(context.Background(), source, m.state.Sources[key])
	require.NoError(t, err)
	require.True(t, complete, "夹具应能给出完整的已发布投影")
	require.Greater(t, closed, uint64(0))
	require.GreaterOrEqual(t, yields, 3,
		"段读聚合必须按切片让路（实测 %d 次；不让路 = 单源扫描占满整轮 ✗）", yields)
}

// TestPublishedClosedScanRespectsRoundBudget（现场终章 ③ 的另一半）：**单源段读聚合必须有轮内预算**，
// 超预算按"证据不完整"处理（不消解缺口、不推进水位 ✓，下一轮续扫 ✓）——否则单个大源就能把
// `pollOnce` 的 WaitGroup 撑成分钟级 ✗✗（现场形态 ✓）。
//
// 转红方式（实测）：去掉 scanCtx 的 WithTimeout（= 无轮内预算）⇒ 扫描照常跑完 ⇒ 本用例在
// 「必须按不完整返回」处变红 ✓。
func TestPublishedClosedScanRespectsRoundBudget(t *testing.T) {
	t.Skip("投递路径简化（2026-11-02）：段读聚合已从所有生产路径消失（不再发布投影），轮内预算失去对象")
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	lines := make([]byte, 0, 4096)
	for i := 0; i < 300; i++ {
		lines = append(lines, []byte(fmt.Sprintf("[12:00:%02d] [Server thread/INFO]: line %d\n", i%60, i))...)
	}
	require.NoError(t, os.WriteFile(logPath, lines, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:scanbudget/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:scanbudget", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	require.NotNil(t, m.pipes[key])
	m.pollOnce()

	// 预算压到 1ns ⇒ 扫描必然超预算 ⇒ 必须返回"不完整"（安全方向 ✓），且计数可观测 ✓。
	m.scanBudget = time.Nanosecond
	m.scanSliceRows = 8
	m.scanYield = time.Nanosecond
	closed, complete, err := m.publishedClosedForSourceCtx(context.Background(), source, m.state.Sources[key])
	require.NoError(t, err, "超预算不是错误（按不完整处理 ✓）")
	require.False(t, complete, "超预算必须按证据不完整返回（不得让单源扫描撑满整轮 ✗）")
	require.Zero(t, closed)
	require.GreaterOrEqual(t, m.scanBudgetExceeded.Load(), int64(1), "截断必须可观测 ✓")
}
