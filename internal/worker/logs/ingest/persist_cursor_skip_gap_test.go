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
// （2026-11-05 实测：同一行写两次 count = 2）⇒ 每次重启都多写一遍；而 `Delivery`
// 不前进的条件（VL 持续失败 / 磁盘暂停）恰恰最需要重启 ⇒ 重复量随重启次数**无界增长**。
//
// 两边代价不对称时选诚实：**承认丢，但让它可查、可告警、可人工裁定**。
//
// # 三个我写错过的版本（全部靠变异/实跑抓出来，写在这里免得再犯）
//
//  1. 登记点放 `flushCursorRegistry`，判据 `Delivery < Read`。
//     错：运行期 `Delivery < Read` 是**正常态**（本批投递中），于是给每个源每轮都加
//     一条缺口，打挂三条既有用例（它们断言「投递失败只留 1 条未解决缺口」）。已 revert。
//  2. 直接在 `resumeCursorFor` 里登记。
//     错：它在 `pipeline.New` 的**参数**里被调用，那时 `m.pipes[key]` 还是 nil，
//     拿不到账本 ⇒ 登记静默失效。
//  3. 在 `Register`（整函数持锁）里调用一个**自己加锁**的处置函数。
//     错：**自锁死锁**。实测形态：用例挂满超时，栈停在 `sync.Mutex.Lock`。
//     修法：处置函数改为 `...Locked`（不加锁），由持锁的调用方直接调。
//
// 因此本文件的用例必须**走真实 `Register`** —— 手动复现其中的两步会绕开真实时序与
// 锁范围，让变异验证失效（第 2 版就吃过这个亏）。

// seedRestartSkip 用**真实 `New`** 造一个「重启续读、文件已比游标长」的 Manager。
//
// 为什么不手工构造 `&Manager{}`：`Register` 依赖若干必须初始化的字段
// （`persistedRev` / `persistedCover` / `state.Sources` …），手搭会 panic
// （我第一次就踩到 `assignment to entry in nil map`）。`newTestManager` 走完整 `New`。
func seedRestartSkip(t *testing.T, cursorPos int64) (*Manager, SourceConfig, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")

	line := "[2026-11-05T00:00:00Z] [Server thread/INFO]: padding line for restart skip test\n"
	for len(line) < 200 {
		line += line
	}
	require.NoError(t, os.WriteFile(path, []byte(line[:200]), 0o644))

	source := SourceConfig{
		LogSourceID: "inst:9/file", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:9",
	}

	code, err := catalog.New(catalog.NewMemJournal()), error(nil)
	require.NoError(t, err)
	m, err := newTestManager(t, Options{Root: root, Catalog: code, Journal: code.Journal()})
	require.NoError(t, err)

	// 游标表：上次读到 cursorPos（模拟上次进程读到那里就死了、未投递）。
	m.cursorRegistryMu.Lock()
	m.cursorRegistry = cursorRegistry{
		"inst:9/file/g1": {SourceCursor: acquire.SourceCursor{
			Path: path, Cursor: cursorPos, LogicalPos: uint64(cursorPos),
		}},
	}
	m.cursorRegistryMu.Unlock()
	return m, source, path
}

// gapsOf 返回该源全部缺口（走真实管道取账本）。
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

func hasSkipGap(m *Manager) bool {
	for _, g := range gapsOf(m) {
		if g.Reason == ledger.GapReasonCursorSkippedOnRestart && !g.Resolved {
			return true
		}
	}
	return false
}

// TestRestartSkipGap_RegisteredOnResume 核心：走真实 Register，重启续读必须登记缺口。
//
// 转红方式（实测）：
//   - 把处置改成「自己加锁」⇒ 死锁（超时 / 栈停在 sync.Mutex.Lock）
//   - 删掉 `resumeCursorFor` 里的通知入账 ⇒ 找不到该缺口
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
	m, source, _ := seedRestartSkip(t, 200) // 游标 == 文件长度
	require.NoError(t, m.Register(source))
	require.False(t, hasSkipGap(m), "游标已在 EOF 时不得登记跳过缺口（纯噪音）")
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
	require.False(t, hasSkipGap(m), "首次登记没有「跳过」可言，不得登记该缺口")
}

// TestRestartSkipGap_NoGapForNormalPolling 正常运行期绝不登记（防回归：第 1 版错在这）。
//
// 第 1 版按 `Delivery < Read` 判，给正常源每轮都加缺口，打挂三条既有用例。
// 这条用例把「正常采集轮不产生该缺口」钉死。
//
// 注意：这里**不走 `pollOnce`**（它只轮询 `Options.Sources` 里登记的源，而本夹具是事后
// `Register` 的，`pollOnce` 拿不到它 ⇒ Read 恒 0，夹具前提不成立）。
// 改为直接推账本水位造出「Read 前进、Delivery 不同步」这个形态，再确认**没有任何路径**
// 会因为水位差距而登记跳过缺口。
func TestRestartSkipGap_NoGapForNormalPolling(t *testing.T) {
	m, source, _ := seedRestartSkip(t, 200) // 游标 == 文件长度，恢复后无跳过
	require.NoError(t, m.Register(source))

	m.mu.Lock()
	p := m.pipes["inst:9/file/g1"]
	m.mu.Unlock()
	require.NotNil(t, p)

	// 正常采集：Read 前进，Delivery 仍为 0（本批还在投递/或未投）。
	require.NoError(t, p.Ledger().AdvanceRead(p.Key(), 150))
	pos, ok := p.Positions()
	require.True(t, ok)
	require.Greater(t, pos.Read, pos.Delivery, "夹具前提：正常投递滞后（Read > Delivery）")

	// 跑一轮刷盘（它是第 1 版放错位置的那个路径）。
	m.flushCursorRegistry()

	require.False(t, hasSkipGap(m),
		"正常采集轮 Read>Delivery 是常态，绝不能登记跳过缺口（第 1 版就错在这）")
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
