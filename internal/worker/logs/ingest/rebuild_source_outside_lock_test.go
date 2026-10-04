package ingest

// #55-B 的红/绿：`planPersist` 里「重建 + 提交」的重活必须在 m.mu **之外**，且锁外期间源又变了
// 时**必须丢弃**本次重建（防"用计划时的旧水位覆盖新水位"的丢失更新）。
//
// 现场：planPersist 的 m.mu 内除了逐条 json.Marshal（A-Step2 已外移），还有一次**面向整块**的
// 耗时 —— 对每个变更源做 rebuildSource：`Ledger().Snapshot()`（逐条 clone）+ `WAL().SnapshotSplit()`
//（内联正文拷贝）+ 写回 m.state，逐条镜像重建阶段达 260–632ms，整段持锁 ⇒ 投递/登记/停止/解算
// 一起排队。
//
// 修法（三段式，父级裁定）：锁内守卫判定 + O(1) 输入快照；锁外构建重建体；
// 第三段锁内**复验守卫**后写回。
//
// 转红方式（可复现）：把 `buildRebuiltSourceOutsideLock` 的调用挪回锁内（或在其中加
// `m.mu.Lock()`）⇒ 首条用例的"未持锁"断言立即转红；删掉第三段的复验 ⇒ 第二条用例转红。

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// newRebuildProbeManager 构造带**真实 pipeline**（真账本 + 真 WAL）的最小 Manager：
// persist 路径需要管道才有账本修订号（无管道的源按"每轮都变更"处理，测不到守卫与复验）。
func newRebuildProbeManager(t *testing.T) (*Manager, SourceConfig, string, *pipeline.Pipeline) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	src := SourceConfig{
		LogSourceID:      "node:rebuild-probe",
		SourceGeneration: "g1",
		Path:             path,
		StorageNamespace: "ns:rebuild-probe",
		UTCDay:           runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(nil), Sources: []SourceConfig{src}})
	require.NoError(t, err)
	key := src.LogSourceID + "/" + src.SourceGeneration
	p := m.pipes[key]
	require.NotNil(t, p, "夹具必须登记出真实管道")
	return m, src, key, p
}

// injectRebuildWAL 往该源 WAL 注入 n 条 durable 条目（真实条目形态：带正文），供 B1a 切分断言。
func injectRebuildWAL(t *testing.T, p *pipeline.Pipeline, src SourceConfig, n int) []acquire.WALEntry {
	t.Helper()
	identity := logtypes.SourceIdentity{
		LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1",
	}
	entries := make([]acquire.WALEntry, 0, n)
	for i := 0; i < n; i++ {
		rec := logtypes.RecordRange{Start: uint64(i*10 + 1), End: uint64(i*10 + 10)}
		entries = append(entries, acquire.WALEntry{
			Seq: uint64(i + 1), Durable: true,
			Event: logtypes.BuildEvent(identity, rec, "2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", "wal"),
		})
	}
	require.NoError(t, p.WAL().Restore(entries))
	return entries
}

// TestRebuildSourceBuildRunsOutsideMutex 断言重建体在锁外构建（含负例证明可红），
// 并守住 B1a 切分契约（段已覆盖 ⇒ 引用；未覆盖 ⇒ 内联，绝不丢正文）。
func TestRebuildSourceBuildRunsOutsideMutex(t *testing.T) {
	m, src, key, p := newRebuildProbeManager(t)
	entries := injectRebuildWAL(t, p, src, 3)

	var held atomic.Bool
	m.rebuildSourceBuildHook.Store(func() {
		if m.mu.TryLock() {
			m.mu.Unlock()
			return
		}
		held.Store(true)
	})

	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	// 与 planPersist 第一段同型的任务：O(1) 输入快照（段尚未覆盖 ⇒ 全内联）。
	task := rebuildTask{
		key: key, pipe: p, rev: p.Ledger().Revision(p.Key()), cover: coverSignatureOf(saved),
		eventsStored: saved.EventsStored, through: saved.EventsStoredThrough, inline: saved.Events,
	}
	rebuilt := m.buildRebuiltSourceOutsideLock(task)
	require.False(t, held.Load(),
		"#55-B：重建体（Ledger 快照 + WAL 分片 + 内联拷贝）不得在 m.mu 内构建")
	require.Empty(t, rebuilt.WALRefs, "段未覆盖（eventsStored=false）时不得把条目降级为引用")
	require.Len(t, rebuilt.WAL, len(entries), "段未覆盖时必须全部内联保存（绝不丢正文）")

	// B1a 契约：段已覆盖的条目只写引用，其余仍内联，且事件体已在段时不得写回内联切片。
	task.eventsStored, task.through = true, entries[0].Event.Record.End
	rebuilt = m.buildRebuiltSourceOutsideLock(task)
	require.Len(t, rebuilt.WALRefs, 1, "已被段覆盖的条目必须降级为引用")
	require.Len(t, rebuilt.WAL, len(entries)-1, "其余条目必须内联保存")
	require.Empty(t, rebuilt.Events, "事件体已在段存储时不得写回内联切片（否则 state 重新涨回）")

	// 负例（红敏感性）：持锁调用 ⇒ 钩子必须观察到"已持锁"（这正是改前 rebuildSource 的形态）。
	held.Store(false)
	m.mu.Lock()
	_ = m.buildRebuiltSourceOutsideLock(task)
	m.mu.Unlock()
	require.True(t, held.Load(), "负例：持锁调用时钩子必须观察到已持锁（证明绿断言可红）")
}

// TestPlanPersistDiscardsRebuildWhenSourceChangedOutsideLock 是本修复**新增语义**的确定性用例：
// 锁外构建期间源又前进（真实现场形态：appendEvents 推进段覆盖水位）⇒ 第三段复验必须**丢弃**
// 本次重建，绝不把计划时的旧水位写回去（那会造成丢失更新，并让 commitPersist 的水位复验被
// "刚被自己回退过"的签名骗过 ⇒ 陈旧行被标成已落库）。
func TestPlanPersistDiscardsRebuildWhenSourceChangedOutsideLock(t *testing.T) {
	m, _, key, _ := newRebuildProbeManager(t)

	// 前置：抬高段覆盖水位 ⇒ 与注册时记下的水印不符 ⇒ 本轮守卫必然命中、必排一次重建。
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.EventsStored, saved.EventsStoredThrough = true, 100
	m.state.Sources[key] = saved
	m.mu.Unlock()

	const midFlight = 200
	var fired atomic.Bool
	m.rebuildSourceBuildHook.Store(func() {
		if !fired.CompareAndSwap(false, true) {
			return
		}
		// 锁外重建期间并发前进（钩子在锁外被调用 ⇒ 这里可以正常取锁）。
		m.mu.Lock()
		cur := m.state.Sources[key]
		cur.EventsStored, cur.EventsStoredThrough = true, midFlight
		m.state.Sources[key] = cur
		m.mu.Unlock()
	})

	require.NoError(t, m.persist())

	m.mu.Lock()
	got := m.state.Sources[key]
	watermark, hasWatermark := m.persistedCover[key]
	m.mu.Unlock()
	require.True(t, fired.Load(), "前置：锁外构建钩子必须被调用（否则测的不是锁外窗口）")
	require.Equal(t, uint64(midFlight), got.EventsStoredThrough,
		"锁外期间前进的水位不得被计划时的旧重建体回退（丢失更新）")
	require.True(t, hasWatermark, "前置：本轮必须写下过水位记账")
	require.NotEqual(t, coverSignatureOf(got), watermark,
		"本轮既然丢弃了重建，就不得把水位标成已落库 ⇒ 下一轮守卫必命中并重做（自愈）")
}
