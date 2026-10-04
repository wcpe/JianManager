package ingest

import (
	"sort"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
)

// 本文件是 A-min（2026-10-04，resolve-gaps 挂死专项）的确定性红/绿：
//
// 现场（pprof 栈证）：stateToIndexStateScoped 先构建 changed 源（partial），再**全量**构建
// stateToIndexState(st)（对每个源 walRowsOf ⇒ 逐条 json.Marshal，实测 338,521 条），
// 然后把 full.WAL 整段覆盖丢弃 —— 纯浪费且全程在 m.mu 内。
// 修法：全量通道 skipWAL=true（WAL 行必被 partial 覆盖，跳过构建）。
//
// 绿（现状）：stateToIndexStateOpt(st, true) 的 WAL 行为空（跳过构建）；
// 红（变异：忽略 skipWAL 条件）⇒ WAL 行非空 ⇒ 转红。
func TestScopedFullChannelSkipsWALBuild(t *testing.T) {
	m := &Manager{state: persistedState{Sources: map[string]persistedSource{
		"inst:a/file": {WAL: []acquire.WALEntry{{}}},
	}}}
	skip, err := m.stateToIndexStateOpt(&m.state, true)
	require.NoError(t, err)
	require.Empty(t, skip.WAL, "A-min：全量通道 skipWAL 时不得构建任何 WAL 行（旧实现此处 marshal 全量后丢弃）")

	full, err := m.stateToIndexStateOpt(&m.state, false)
	require.NoError(t, err)
	require.NotEmpty(t, full.WAL, "对照：非 skip 通道仍应构建 WAL 行（语义未变更）")
}

// 变异记录（2026-10-04 实测，逐字还原）：
//   - 变异：把 walRowsOf 的 `if !skipWAL { ... }` 条件去掉（无条件构建）⇒ 首断言转红；还原后转绿。

// TestBuildWALRowsOutsideLock 是 A-Step2（2026-10-04）的红/绿：
//
// 现场（pprof 栈证）：planPersist 在 m.mu 内对 changed 源逐条 json.Marshal（338,521 条）。
// 修法：锁内只取"值拷贝快照"（walSnapshot），WAL 行构建移到锁外（buildWALRowsOutsideLock）。
//
// 绿（现状）：钩子处 TryLock 成功（未持 m.mu）+ 行内容与旧路径逐行一致（排序后比较）；
// 负例（红）：持锁调用同一函数 ⇒ 钩子观察到"已持锁" ⇒ 证明该断言具备红的敏感性。
func TestBuildWALRowsOutsideLock(t *testing.T) {
	m := &Manager{state: persistedState{Sources: map[string]persistedSource{
		"inst:a/file": {WAL: []acquire.WALEntry{{}, {}}},
		"inst:b/file": {WAL: []acquire.WALEntry{{}}},
	}}}
	changed := []string{"inst:a/file", "inst:b/file"}
	snaps := make([]walSnapshot, 0, len(changed))
	for _, k := range changed {
		snaps = append(snaps, walSnapshot{key: k, saved: m.state.Sources[k]})
	}

	var held atomic.Bool
	m.walRowsBuildHook.Store(func() {
		if m.mu.TryLock() {
			m.mu.Unlock()
		} else {
			held.Store(true)
		}
	})

	// 绿：锁外构建
	rows, err := m.buildWALRowsOutsideLock(snaps)
	require.NoError(t, err)
	require.False(t, held.Load(), "A-Step2：构建 WAL 行时不得持 m.mu（现场 338k marshal 曾压在锁内）")
	require.Len(t, rows, 3, "两源共 3 条 WAL 应全部构建")

	// c 约束：与旧路径逐行一致（行序无跨实现保证——旧路径遍历 map ⇒ 排序后比较）
	old, err := m.stateToIndexStateScoped(&m.state, changed)
	require.NoError(t, err)
	require.Equal(t, len(old.WAL), len(rows), "行数必须一致")
	sortRows := func(in []stateindex.WALRow) []stateindex.WALRow {
		out := append([]stateindex.WALRow{}, in...)
		sort.Slice(out, func(i, j int) bool {
			if out[i].Key != out[j].Key {
				return out[i].Key < out[j].Key
			}
			return out[i].Seq < out[j].Seq
		})
		return out
	}
	require.Equal(t, sortRows(old.WAL), sortRows(rows), "逐行内容必须一致（新旧一致 = 零语义漂移）")

	// 负例（红敏感性）：持锁调用 ⇒ 钩子必须观察到"已持锁"
	held.Store(false)
	m.mu.Lock()
	_, err = m.buildWALRowsOutsideLock(snaps)
	m.mu.Unlock()
	require.NoError(t, err)
	require.True(t, held.Load(), "负例：持锁调用时钩子必须观察到已持锁（证明绿断言可红）")
}
