package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestResumeSweepHandlesMultipleSolvableSourcesPerRound（现场 2026-10-04：15 分钟只解 1 个 ✗）：
// 兜底环必须**每轮处理多个可解源**（有界但多源 ✓），而不是被慢源拖成"每轮一个" ✗。
//
// 现场形态：60 个暂停源里 30+ 积压早已清零 ✓，但 15 分钟只放行 1 个 ✗ ⇒ 观察上等价于"每轮容量 = 1"。
//
// 转红方式（实测）：把兜底环改成"首个成功即 return"（每轮容量退化为 1）⇒ 本用例在
// 「同一轮内多个源都必须被处理」处红 ✓✓。
func TestResumeSweepHandlesMultipleSolvableSourcesPerRound(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	const n = 3 // 三个都可解：积压类暂停 + 积压为零 + 缺口可被分类消解（回读愈合 ✓）
	cfgs := make([]SourceConfig, 0, n)
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, fmt.Sprintf("s%d", i))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		path := filepath.Join(dir, "latest.log")
		require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o644))
		cfgs = append(cfgs, SourceConfig{
			LogSourceID: fmt.Sprintf("inst:batch%d/file", i), SourceGeneration: "g1", Path: path,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: fmt.Sprintf("inst:batch%d", i), UTCDay: runtimeTestUTCDay(),
		})
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: cfgs})
	require.NoError(t, err)

	for _, cfg := range cfgs {
		key := cfg.LogSourceID + "/" + cfg.SourceGeneration
		pipe := m.pipes[key]
		require.NotNil(t, pipe)
		require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "wal backlog limit exceeded (entries=5/4)"))
		// "暂停造成、从未进 WAL"的缺口 ⇒ 只能靠恢复后回读愈合 ⇒ 属可解源 ✓（与现场同型 ✓）。
		require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, 100, "APPEND_REJECTED", "append rejected while paused"))
	}

	m.maybeResumePausedSources() // **一次**兜底扫描
	revived := 0
	for _, cfg := range cfgs {
		entry := m.pipes[cfg.LogSourceID+"/"+cfg.SourceGeneration].Ledger().Get(m.pipes[cfg.LogSourceID+"/"+cfg.SourceGeneration].Key())
		if entry != nil && !entry.AcquirePaused {
			revived++
		}
	}
	require.Equal(t, n, revived,
		"同一轮内所有可解源都必须被处理（实测 %d/%d；每轮容量退化为 1 = 现场「15 分钟只解 1 个」✗）", revived, n)
	_ = acquire.IsBacklogPauseReason
}
