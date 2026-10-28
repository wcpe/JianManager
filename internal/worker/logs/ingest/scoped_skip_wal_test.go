package ingest

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件原是两个「锁外构建 WAL 行」回归（A-min skipWAL / A-Step2 buildWALRowsOutsideLock）。
//
// 2026-10-29：WAL 不再落盘（`source_wal` 表已删），WAL 行的构建、`skipWAL` 参数、
// `buildWALRowsOutsideLock` 与它的 `walSnapshot` 输入全部随之下线——规划阶段不再有
// 「WAL 行」这个贵活，也就没有「它必须在锁外做」这件事。
//
// 保留下来的、仍然成立的不变量是**门外的规划不持 m.mu**的安全边界本身：
// planPersist 在**锁外**做逐源重建（Ledger 快照 + 队列快照），这段是现场 260–632ms 的
// 整块耗时（见 buildRebuiltSourceOutsideLock 的注释）。本用例继续钉这条：
//
// 绿（现状）：`rebuildSourceBuildHook` 处 `m.mu.TryLock()` 成功（未持锁）；
// 负例（红敏感性）：持锁执行同一段 ⇒ 钩子观察到"已持锁" ⇒ 证明绿断言具备红的敏感性。

func TestBuildRebuiltSourceOutsideLockHoldsNoStateLock(t *testing.T) {
	m := &Manager{state: persistedState{Sources: map[string]persistedSource{}}}
	// 2026-10-29：buildRebuiltSourceOutsideLock 现在直接读 pipe 的账本/队列快照（原来只读
	// state 里的 WAL 行镜像），因此任务里必须带真实的 pipeline；空 pipe 会 nil 解引用。
	pipe, err := pipeline.New(pipeline.Options{LogSourceID: "inst:a", SourceGeneration: "file", Path: filepath.Join(t.TempDir(), "latest.log")})
	require.NoError(t, err)
	var held atomic.Bool
	m.rebuildSourceBuildHook.Store(func() {
		if m.mu.TryLock() {
			m.mu.Unlock()
		} else {
			held.Store(true)
		}
	})

	// 绿：锁外重建
	rebuilt := m.buildRebuiltSourceOutsideLock(rebuildTask{key: "inst:a/file", pipe: pipe})
	require.False(t, held.Load(), "锁外重建期间不得持 m.mu（现场这块曾达 260–632ms）")
	require.NotNil(t, rebuilt.Ledger, "账本快照必须被真的取到")

	// 负例（红敏感性）：持锁调用 ⇒ 钩子必须观察到"已持锁"
	held.Store(false)
	m.mu.Lock()
	_ = m.buildRebuiltSourceOutsideLock(rebuildTask{key: "inst:a/file", pipe: pipe})
	m.mu.Unlock()
	require.True(t, held.Load(), "负例：持锁调用时钩子必须观察到已持锁（证明绿断言可红）")
}
