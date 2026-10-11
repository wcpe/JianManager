package ingest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 回归（ACQ-1 端到端，2026-10-12 终审修复）：`Replay` 回放标记必须随**生产持久化链路**往返，
// 而不只是在 stateindex 包内往返。
//
// 为什么必须补这条（原缺陷的漏网原因）：ACQ-1 首次修复只做了 stateindex 层——`source_wal` 表
// 加了 `replay` 列、`WALFingerprint` 纳入了它、旧库补列也都对，配套的
// `wal_replay_roundtrip_test.go` 也真的能转红。但**生产链路的两个映射函数完全没接**：
//   - 写侧 `walRowsOf` 构造 `stateindex.WALRow` 时丢掉 `Replay`；
//   - 读侧 `indexStateToState` 构造 `acquire.WALRef`/`WALEntry` 时同样丢掉。
//
// 于是库里的 `replay` 列恒为 0 ⇒ **重启后回放条目退回常规积压**并把源重新推过闸
// （闸视图 = 总量 − 回放），而这恰好发生在最需要它的时刻（恢复期回放与进程重启同一场景）。
// stateindex 层的用例测不到这两个映射，缺陷因此穿过了全部门禁。
//
// 转红方式（实测）：删掉 `walRowsOf` 任一侧的 `Replay:` 字段赋值（写侧丢），或删掉
// `indexStateToState` 中任一侧的 `Replay:`（读侧丢）⇒ 本文件对应的那条断言立即红。
// 内联与引用两侧各自独立断言，避免只修一半时用例仍绿。
func TestIndexStoreWALReplayFlagSurvivesIngestRoundTrip(t *testing.T) {
	m := &Manager{}
	// 覆盖两种形态：内联条目（Body 非 nil）与引用条目（Body 为 nil），二者都要保住标记。
	// 同时各配一条 Replay=false 的对照，确保断言不是在无脑要求「恒为 true」。
	src := persistedSource{
		WAL: []acquire.WALEntry{
			{
				Seq: 1, Replay: true, Appended: true, Durable: true,
				Event: logtypes.Event{EventID: "ev-inline-replay", Record: logtypes.RecordRange{Start: 10, End: 20}},
			},
			{
				Seq: 2, Replay: false, Appended: true, Durable: true,
				Event: logtypes.Event{EventID: "ev-inline-normal", Record: logtypes.RecordRange{Start: 30, End: 40}},
			},
		},
	}

	// —— 写侧：walRowsOf 必须把标记写进行 ——
	rows, err := m.walRowsOf("inst:acq1/file", src)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	written := make(map[string]bool, len(rows))
	for _, row := range rows {
		written[row.EventID] = row.Replay
	}
	require.True(t, written["ev-inline-replay"],
		"写侧：内联条目的 Replay 必须落库（否则库里 replay 列恒 0，重启后回放条目退回常规积压）")
	require.False(t, written["ev-inline-normal"], "对照：非回放条目不得被误标为回放")

	// —— 读侧：indexStateToState 必须把标记读回内存条目 ——
	state, err := indexStateToState(stateindex.State{
		Sources: []stateindex.SourceRow{{
			Key: "inst:acq1/file", LogSourceID: "inst:acq1/file",
			SourceGeneration: "g1", StorageNamespace: "inst:acq1/file",
		}},
		// 读侧以「position 行存在」为该源有状态的判据（见 indexStateToState 的 hasState 分支）。
		Positions: []stateindex.PositionRow{{Key: "inst:acq1/file"}},
		WAL:       rows,
	})
	require.NoError(t, err)

	got := state.Sources["inst:acq1/file"]
	gotReplay := make(map[string]bool, len(got.WAL))
	for _, e := range got.WAL {
		gotReplay[e.Event.EventID] = e.Replay
	}
	require.Len(t, got.WAL, 2, "内联条目必须全部恢复")
	require.True(t, gotReplay["ev-inline-replay"],
		"读侧：内联条目的 Replay 必须读回（这是重启后闸视图不看它的直接原因）")
	require.False(t, gotReplay["ev-inline-normal"], "对照：非回放条目读回后仍为 false")
}

// 读侧**引用条目**的 Replay 往返单独锁定：引用条目的正文需经 hydrate 从事件段存储取回，
// 与水合失败路径相关，故与上一条分开构造，避免夹具耦合。
//
// 转红方式（实测）：删掉 `walRowsOf` 引用侧的 `Replay:`，或删掉 `indexStateToState` 中构造
// `acquire.WALRef` 的 `Replay:` ⇒ 对应断言立即红。
func TestIndexStoreWALRefReplayFlagSurvivesIngestRoundTrip(t *testing.T) {
	m := &Manager{}
	rows, err := m.walRowsOf("inst:acq1ref/file", persistedSource{
		WALRefs: []acquire.WALRef{
			{Seq: 1, Replay: true, Appended: true, Durable: true, EventID: "ev-r1", RecordStart: 1, RecordEnd: 2},
			{Seq: 2, Replay: false, Appended: true, Durable: true, EventID: "ev-r2", RecordStart: 3, RecordEnd: 4},
		},
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.True(t, rows[0].Replay, "写侧：引用条目 Replay 必须落库（引用态条目重启后最能暴露丢标记）")
	require.False(t, rows[1].Replay, "写侧对照：非回放引用条目不得被误标")

	// 读侧：引用行经 indexStateToState 还原为 WALRef 时同样必须带上标记。
	state, err := indexStateToState(stateindex.State{
		Sources: []stateindex.SourceRow{{
			Key: "inst:acq1ref/file", LogSourceID: "inst:acq1ref/file",
			SourceGeneration: "g1", StorageNamespace: "inst:acq1ref/file",
		}},
		Positions: []stateindex.PositionRow{{Key: "inst:acq1ref/file"}},
		WAL:       rows,
	})
	require.NoError(t, err)

	refs := make(map[string]bool, 2)
	for _, r := range state.Sources["inst:acq1ref/file"].WALRefs {
		refs[r.EventID] = r.Replay
	}
	require.Len(t, refs, 2, "两条引用条目都应保留为 WALRef")
	require.True(t, refs["ev-r1"],
		"读侧：引用条目的 Replay 必须读回（WALRef.Replay → RestoreMixed → WALEntry.Replay）")
	require.False(t, refs["ev-r2"], "读侧对照：非回放引用条目读回后仍为 false")
}
