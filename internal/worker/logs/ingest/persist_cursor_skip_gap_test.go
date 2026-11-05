package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件钉住「重启后会跳过未投递区间」这件事**必须是可见的**（2026-11-05）。
//
// # 缺陷形态（审计 P0-2，两处独立审计 + 我的复核三处一致）
//
// 游标表落盘的是 tailer 的**已读位**。而 WAL 是纯内存队列、重启即丢；账本来自纯内存
// 索引（`stateindex.OpenInMemory`）⇒ `rollbackReadToReclaim` 的前提
// `m.state.Sources[key]` 不成立 ⇒ **永不执行**。
//
// 于是新进程从「已读位」继续读，`[已投递, 已读)` 那段**永久跳过**，
// 而且原本**完全无声**：WAL 已亡、账本空白、游标已越过。
//
// # 为什么选「登记缺口」而不是「回退游标重读」
//
// 重读能让数据不丢，但代价是重投；而 VictoriaLogs **不按 event_id 去重**
// （2026-11-05 实测：同一行写两次 count = 2）⇒ 每次重启都多写一遍，
// 而 `Delivery` 不前进的条件恰恰最需要重启 ⇒ 重复量无界增长。
//
// 所以选诚实：**承认丢，但让它可查、可告警、可人工裁定**。

// seedUndeliveredPipe 造一个「读了但没投递」的管道：Read 前进、Delivery 留在 0。
func seedUndeliveredPipe(t *testing.T) (*Manager, *pipeline.Pipeline) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	content := ""
	for i := 0; i < 10; i++ {
		content += "[2026-11-05T00:00:0" + string(rune('0'+i)) + "Z] [Server thread/INFO]: line " + string(rune('0'+i)) + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	cat := catalog.New(catalog.NewMemJournal())
	pipe, err := pipeline.New(pipeline.Options{
		Mode: pipeline.ModeFilePrimary, LogSourceID: "inst:1/file",
		SourceGeneration: "g1", Path: path,
	})
	require.NoError(t, err)
	// 真读一批 ⇒ Read 前进。
	_, pollErr := pipe.Poll()
	require.NoError(t, pollErr)

	pos, ok := pipe.Positions()
	require.True(t, ok)
	require.NotZero(t, pos.Read, "夹具前提：Read 必须已前进，否则测不到 [Delivery,Read) 区间")
	require.Zero(t, pos.Delivery, "夹具前提：Delivery 必须仍为 0（一条都没投递成功）")

	m := &Manager{}
	m.cat = cat
	m.mu.Lock()
	m.pipes = map[string]*pipeline.Pipeline{"inst:1/file/g1": pipe}
	m.sources = map[string]SourceConfig{
		"inst:1/file/g1": {
			LogSourceID: "inst:1/file", SourceGeneration: "g1", Path: path,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:1",
		},
	}
	m.mu.Unlock()
	return m, pipe
}

// TestFlushCursorRegistry_RecordsSkippedRangeGap 核心：落盘时必须登记 `[Delivery,Read)` 缺口。
//
// 转红方式（实测）：删掉 `collectCursorRegistryCandidates` 里的
// `m.recordCursorSkipGap(kp.pipe)`，本用例变红（找不到该原因的缺口）。
func TestFlushCursorRegistry_RecordsSkippedRangeGap(t *testing.T) {
	m, pipe := seedUndeliveredPipe(t)

	_ = m.collectCursorRegistryCandidates()

	pos, ok := pipe.Positions()
	require.True(t, ok)
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)

	var found *ledger.Gap
	for i := range entry.Gaps {
		if entry.Gaps[i].Reason == ledger.GapReasonCursorSkippedOnRestart && !entry.Gaps[i].Resolved {
			found = &entry.Gaps[i]
			break
		}
	}
	require.NotNil(t, found,
		"落盘前必须登记 %s 缺口；否则那段数据丢了却毫无痕迹", ledger.GapReasonCursorSkippedOnRestart)
	require.EqualValues(t, pos.Delivery, found.StartPos, "缺口起点必须是已投递位")
	require.EqualValues(t, pos.Read, found.EndPos, "缺口终点必须是已读位")
	// detail 必须写清「区间大小 / 为何跳 / 怎么补」三件事。
	require.Contains(t, found.Detail, "un-delivered bytes", "detail 应说明区间大小")
	require.Contains(t, found.Detail, "memory-only", "detail 应说明为何跳（WAL 纯内存）")
	require.Contains(t, found.Detail, "remedy", "detail 必须给出补救动作")
}

// TestFlushCursorRegistry_NoGapWhenFullyDelivered 全部投递成功时不登记（零噪音）。
func TestFlushCursorRegistry_NoGapWhenFullyDelivered(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path,
		[]byte("[2026-11-05T00:00:00Z] [Server thread/INFO]: only line\n"), 0o644))

	cat := catalog.New(catalog.NewMemJournal())
	pipe, err := pipeline.New(pipeline.Options{
		Mode: pipeline.ModeFilePrimary, LogSourceID: "inst:2/file",
		SourceGeneration: "g1", Path: path,
	})
	require.NoError(t, err)

	// 把 Read 与 Delivery 都推到同一位置（模拟「已投递」）。
	require.NoError(t, pipe.Ledger().AdvanceRead(pipe.Key(), 100))
	require.NoError(t, pipe.Ledger().AdvanceDurable(pipe.Key(), 100))
	require.NoError(t, pipe.Ledger().RecordDelivery(pipe.Key(), 0, 100,
		logtypes.DeliveryRequestDone))

	m := &Manager{}
	m.cat = cat
	m.mu.Lock()
	m.pipes = map[string]*pipeline.Pipeline{"inst:2/file/g1": pipe}
	m.sources = map[string]SourceConfig{
		"inst:2/file/g1": {
			LogSourceID: "inst:2/file", SourceGeneration: "g1", Path: path,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:2",
		},
	}
	m.mu.Unlock()

	_ = m.collectCursorRegistryCandidates()

	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	for _, gap := range entry.Gaps {
		require.NotEqual(t, ledger.GapReasonCursorSkippedOnRestart, gap.Reason,
			"全部投递成功时不得登记跳过缺口（那会是纯噪音）")
	}
}

// TestCursorSkippedGap_NotAutoResolvable 该原因**不得**进任何自动消解/愈合名单。
//
// 判据（与 DELIVER_ERROR 的语义区别）：
//   - DELIVER_ERROR：重试能解决（事件还在 WAL / 还可重读）⇒ 可愈合；
//   - 本原因：只能从**源重采**或人工裁定 ⇒ 混进自愈环会让它反复尝试一个永远不会成功的动作。
//
// 转红方式（实测）：把 GapReasonCursorSkippedOnRestart 加进
// gapReasonsHealableOnReResume 或 gapAutoResolvableReasons，本用例变红。
func TestCursorSkippedGap_NotAutoResolvable(t *testing.T) {
	// 它应当属于「永久丢失、不阻塞采集」类：阻塞也救不回来，只会牺牲之后的新日志。
	require.True(t, ledger.IsGapReasonPermanentlyLost(ledger.GapReasonCursorSkippedOnRestart),
		"应归入永久丢失类：否则它会永久阻塞该源的采集")

	// 但**不能**出现在「可愈合」名单里（那会让自愈环以为回读能救回来）。
	for _, reason := range ledger.GapReasonsHealableOnReResume() {
		require.NotEqual(t, ledger.GapReasonCursorSkippedOnRestart, reason,
			"本原因不得进可愈合名单：回读不会发生（游标已过）")
	}
	// 也不能出现在「非阻塞且可穿过 reclaim」的名单里 ——
	// 那两个语义不同（那个是「主动丢弃、可预期」）。
	for _, reason := range ledger.GapReasonsNonBlocking() {
		require.NotEqual(t, ledger.GapReasonCursorSkippedOnRestart, reason,
			"本原因不得进 NonBlocking 名单：它不是「主动丢弃」，不能给 reclaim 当通行证")
	}
}

// TestCursorSkippedGap_SurvivesInLedger 端到端：登记后能从账本读回（重启后可见的基础）。
//
// 注意这里**不模拟真实重启**（账本是纯内存的，重启即空——那正是本缺陷的成因）。
// 本用例证明的是：**在进程存活期间**，这条缺口是可查的，因此能被观测/告警/人工处置。
func TestCursorSkippedGap_SurvivesInLedger(t *testing.T) {
	m, pipe := seedUndeliveredPipe(t)
	_ = m.collectCursorRegistryCandidates()

	// 再跑一轮：同因缺口会被合并（rangesTouch），不应堆成多条。
	_ = m.collectCursorRegistryCandidates()

	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	count := 0
	for _, gap := range entry.Gaps {
		if gap.Reason == ledger.GapReasonCursorSkippedOnRestart {
			count++
		}
	}
	require.Equal(t, 1, count,
		"同区间反复登记应被合并成一条（否则每轮刷盘都新增一条，缺口风暴）")
}
