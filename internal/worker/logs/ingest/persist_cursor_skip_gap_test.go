package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件钉住「重启续读时跳过的未投递区间必须可见」（2026-11-05）。
//
// # 缺陷形态（审计 P0-2，两处独立审计 + 我的复核，三处一致）
//
// 游标表落盘的是 tailer 的**已读位**。WAL 是纯内存队列、重启即丢；账本来自纯内存索引
// （`stateindex.OpenInMemory`）⇒ `rollbackReadToReclaim` 的前提
// `m.state.Sources[key]` 不成立 ⇒ **永不执行**。
//
// 于是新进程从持久化游标继续读，`[游标位, 文件当时位置)` 之间那段「已读但未投递」的
// 字节：WAL 已亡、账本空白、游标已越过 ⇒ **永久跳过且无声**。
//
// # 为什么选「登记缺口」而不是「回退游标重读」
//
// 重读能让数据不丢，但 ⇒ 重投；而 **VictoriaLogs 不按 event_id 去重**
// （2026-11-05 实测：同一行写两次 count = 2）⇒ 每次重启都多写一遍。
// 而 `Delivery` 不前进的条件（VL 持续失败 / 磁盘暂停）恰恰最需要重启
// ⇒ 重复量随重启次数**无界增长**。
//
// 两边代价不对称时选诚实：**承认丢，但让它可查、可告警、可人工裁定**。
//
// # 两个我写错过的版本（都靠变异验证抓出来，写在这里免得再犯）
//
//  1. 把登记点放在 `flushCursorRegistry`，判据 `Delivery < Read`。
//     错：运行期 `Delivery < Read` 是**正常态**（本批正在投递中），
//     于是给每个源每轮都加一条缺口，打挂三条既有用例（它们断言"投递失败只留 1 条"）。已 revert。
//  2. 直接在 `resumeCursorFor` 里登记。
//     错：它在 `pipeline.New` 的**参数**里被调用，那时 `m.pipes[key]` 还是 nil，
//     拿不到账本 ⇒ 登记静默失效。
//
// 正确做法：`resumeCursorFor` 只记下通知，由 **`Register` 在 `m.pipes[key] = p` 之后**
// 统一处置。所以本文件的用例必须**走真实 `Register`**——手动调两步会绕开时序，
// 让变异验证失效（第二版就吃过这个亏：手动调 `recordPendingResumeSkipGaps` 后，
// 把调用点移到入表前，用例仍然绿）。
func seedRestartSkip(t *testing.T, cursorPos int64) (*Manager, SourceConfig, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")

	// 文件 200 字节；游标停在 cursorPos。
	line := "[2026-11-05T00:00:00Z] [Server thread/INFO]: padding line for restart skip test\n"
	for len(line) < 200 {
		line += line
	}
	require.NoError(t, os.WriteFile(path, []byte(line[:200]), 0o644))

	source := SourceConfig{
		LogSourceID: "inst:9/file", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:9",
	}
	cat := catalog.New(catalog.NewMemJournal())
	m := &Manager{}
	m.cat = cat
	m.mu.Lock()
	m.sources = map[string]SourceConfig{}
	m.pipes = map[string]*pipeline.Pipeline{}
	m.state.SourceConfigs = map[string]SourceConfig{}
	m.mu.Unlock()

	m.cursorRegistryMu.Lock()
	m.cursorRegistry = cursorRegistry{
		"inst:9/file/g1": {SourceCursor: acquire.SourceCursor{
			Path: path, Cursor: cursorPos, LogicalPos: uint64(cursorPos),
		}},
	}
	m.cursorRegistryMu.Unlock()
	return m, source, path
}

// gapsOf 返回该源全部缺口。
func gapsOf(m *Manager) []ledger.Gap {
	m.mu.Lock()
	p := m.pipes["inst:9/file/g1"]
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	e := p.Ledger().Get(p.Key())
	if e == nil {
		return nil
	}
	return e.Gaps
}

// TestRestartSkipGap_RegisteredOnResume 核心：走真实 Register，重启续读必须登记缺口。
//
// 转红方式（实测）：
//   - 把 `recordPendingResumeSkipGaps()` 移到 `m.pipes[key] = p` 之前 ⇒ 红
//     （那正是第二版实现踩过的时序 bug）
//   - 删掉 `resumeCursorFor` 里的通知入账 ⇒ 红
func TestRestartSkipGap_RegisteredOnResume(t *testing.T) {
	m, source, _ := seedRestartSkip(t, 100)

	require.NoError(t, m.Register(source), "登记应成功")

	all := gapsOf(m)
	idx := -1
	for i := range all {
		if all[i].Reason == ledger.GapReasonCursorSkippedOnRestart {
			idx = i
			break
		}
	}
	require.NotEqual(t, -1, idx,
		"重启续读必须登记 %s 缺口；否则那段数据丢了却毫无痕迹",
		ledger.GapReasonCursorSkippedOnRestart)
	found := all[idx]
	require.EqualValues(t, 100, found.StartPos, "缺口起点应是持久化游标位")
	require.EqualValues(t, 200, found.EndPos, "缺口终点应是文件当前长度")
	require.Contains(t, found.Detail, "un-delivered", "detail 应说明这是未投递区间")
	require.Contains(t, found.Detail, "memory-only", "detail 应说明为何跳（WAL 纯内存）")
	require.Contains(t, found.Detail, "remedy", "detail 必须给出补救动作")
}

// TestRestartSkipGap_NoGapWhenCursorAtEOF 游标已到文件末尾 ⇒ 没跳过东西，不登记。
func TestRestartSkipGap_NoGapWhenCursorAtEOF(t *testing.T) {
	m, source, _ := seedRestartSkip(t, 200)
	require.NoError(t, m.Register(source))

	for _, g := range gapsOf(m) {
		require.NotEqual(t, ledger.GapReasonCursorSkippedOnRestart, g.Reason,
			"游标已在 EOF 时不得登记跳过缺口（纯噪音）")
	}
}

// TestRestartSkipGap_NoGapOnFreshSource 首次登记（无游标）⇒ 不登记。
//
// `StartAtEOF` 是**有意的起点选择**（不吃别人的历史），不是"跳过"。
func TestRestartSkipGap_NoGapOnFreshSource(t *testing.T) {
	m, source, _ := seedRestartSkip(t, 100)
	m.cursorRegistryMu.Lock()
	m.cursorRegistry = cursorRegistry{}
	m.cursorRegistryMu.Unlock()

	require.NoError(t, m.Register(source))

	for _, g := range gapsOf(m) {
		require.NotEqual(t, ledger.GapReasonCursorSkippedOnRestart, g.Reason,
			"首次登记没有「跳过」可言，不得登记该缺口")
	}
}

// TestRestartSkipGap_NoGapForNormalPolling 正常运行期绝不登记（防回归：第一版就错在这）。
//
// 第一版按 `Delivery < Read` 判，给正常源每轮都加缺口，打挂三条既有用例。
// 这条用例把「正常采集轮不产生该缺口」钉死。
func TestRestartSkipGap_NoGapForNormalPolling(t *testing.T) {
	m, source, path := seedRestartSkip(t, 200) // 游标 == 文件长度，恢复后无跳过
	require.NoError(t, m.Register(source))

	// 追加两行并跑一轮采集：Read 前进、Delivery 仍 0（没有投递钩子）。
	require.NoError(t, os.WriteFile(path,
		[]byte("[2026-11-05T00:00:01Z] [Server thread/INFO]: a\n"+
			"[2026-11-05T00:00:02Z] [Server thread/INFO]: b\n"), 0o644))
	m.pollOnce()

	m.mu.Lock()
	p := m.pipes["inst:9/file/g1"]
	m.mu.Unlock()
	require.NotNil(t, p)
	pos, ok := p.Positions()
	require.True(t, ok)
	require.Greater(t, pos.Read, pos.Delivery, "夹具前提：正常投递滞后（Read > Delivery）")

	for _, g := range gapsOf(m) {
		require.NotEqual(t, ledger.GapReasonCursorSkippedOnRestart, g.Reason,
			"正常采集轮 Read>Delivery 是常态，绝不能登记跳过缺口（第一版就错在这）")
	}
}

// TestRestartSkipGap_ReasonClassification 该原因的归类判据。
//
// 转红方式（实测）：加进 `gapReasonsHealableOnReResume` 或 `gapReasonsNonBlocking`。
func TestRestartSkipGap_ReasonClassification(t *testing.T) {
	require.True(t, ledger.IsGapReasonPermanentlyLost(ledger.GapReasonCursorSkippedOnRestart),
		"应归入永久丢失类：否则它会永久阻塞该源的采集")
	for _, r := range ledger.GapReasonsHealableOnReResume() {
		require.NotEqual(t, ledger.GapReasonCursorSkippedOnRestart, r,
			"不得进可愈合名单：回读不会发生（游标已过）")
	}
	for _, r := range ledger.GapReasonsNonBlocking() {
		require.NotEqual(t, ledger.GapReasonCursorSkippedOnRestart, r,
			"不得进 NonBlocking 名单：它不是主动丢弃，不能给 reclaim 当通行证")
	}
}
