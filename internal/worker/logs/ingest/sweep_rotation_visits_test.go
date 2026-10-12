package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestSweepRotationDoesNotRevisitSameSourceTwice（用户 2026-10-05 红证 ✓）：
// **同一慢源不得连续两轮占满预算** —— 即"跳过即轮转"必须**可验证**（骨架本已在：游标按
// 已扫描数前进 ⇒ 被跳过者也算已扫描 ✓）。
//
// 转红方式（实测，**有效变异** ✓：先验编译）：去掉游标推进（= 固定窗口 ⇒ 每轮都停在同一位 ✗）⇒
// 本用例在「连续两轮的访问源必须不同」处红 ✓✓。
func TestSweepRotationDoesNotRevisitSameSourceTwice(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	const n = 3
	cfgs := make([]SourceConfig, 0, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(root, fmt.Sprintf("r%d.log", i))
		require.NoError(t, os.MkdirAll(root, 0o755))
		require.NoError(t, os.WriteFile(p, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o644))
		cfgs = append(cfgs, SourceConfig{
			LogSourceID: fmt.Sprintf("inst:visit%d/file", i), SourceGeneration: "g1", Path: p,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: fmt.Sprintf("inst:visit%d", i), UTCDay: runtimeTestUTCDay(),
		})
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: cfgs})
	require.NoError(t, err)
	for _, cfg := range cfgs {
		pipe := m.pipes[cfg.LogSourceID+"/"+cfg.SourceGeneration]
		require.NotNil(t, pipe)
		require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "wal backlog limit exceeded (entries=5/4)"))
		// **不可分类残余缺口** ⇒ 始终合格且不会被复活 ✓（与本会话既有夹具同形 ✓）。
		require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 100, 200, ledger.GapReasonStdioRawWriteFailed, "raw write failed"))
	}
	m.resumeBatchPerRound = 1 // 每轮只处理 1 个 ⇒ 访问序可直接读出 ✓

	var mu sync.Mutex
	var round []string
	m.resumeProbe = func(id string) { mu.Lock(); round = append(round, id); mu.Unlock() }

	visitOnce := func() string {
		mu.Lock()
		round = nil
		mu.Unlock()
		_, _ = m.maybeResumePausedSources()
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, round, 1, "batch=1 时必须恰好访问 1 个源 ✓")
		return round[0]
	}
	first := visitOnce()
	second := visitOnce()
	require.NotEqual(t, first, second,
		"**同一慢源不得连续两轮占满预算**（实测两轮都是 %s ⇒ 固定窗口/未轮转 ✗✗）", first)

	seen := map[string]bool{first: true, second: true}
	for i := 0; i < 4; i++ { // 再跑几轮 ⇒ ⌈3/1⌉ 轮内三个源都应被访问 ✓
		seen[visitOnce()] = true
	}
	require.Len(t, seen, n, "⌈合格数/batch⌉ 轮内必须覆盖**全部**合格源（实测 %v）", seen)
}
