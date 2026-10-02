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

// TestSweepRotationCoversAllEligiblePausedSources（用户 2026-10-05 ② 的两条红证 ✓✓）：
//
//	① **长期暂停的源必须进入候选集**（不许"前 N 个永远霸占窗口"✗）；
//	② **每轮枚举必须覆盖（并轮转）全部合格暂停者** —— E 个合格 + 每轮 N ⇒ ⌈E/N⌉ 轮内**全部**被处理 ✓。
//
// 现场依据：`candidates` 恒定 2 而应复活 38 ✗ ⇒ 要么环停了（心跳已覆盖 ✓）、要么窗口不轮转 ⇒
// 长暂停源**永不被覆盖** ✗✗。本用例钉住后者 ✓。
//
// 转红方式（实测）：把游标推进去掉（= 固定窗口、永远从 0 开始取前 N 个 ✗）⇒ 后 4 家永不进候选集 ⇒ 红 ✓✓。
func TestSweepRotationCoversAllEligiblePausedSources(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	const n = 6
	cfgs := make([]SourceConfig, 0, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(root, fmt.Sprintf("s%d.log", i))
		require.NoError(t, os.MkdirAll(root, 0o755))
		require.NoError(t, os.WriteFile(p, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o644))
		cfgs = append(cfgs, SourceConfig{
			LogSourceID: fmt.Sprintf("inst:rot%d/file", i), SourceGeneration: "g1", Path: p,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: fmt.Sprintf("inst:rot%d", i), UTCDay: runtimeTestUTCDay(),
		})
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: cfgs})
	require.NoError(t, err)
	for _, cfg := range cfgs {
		pipe := m.pipes[cfg.LogSourceID+"/"+cfg.SourceGeneration]
		require.NotNil(t, pipe)
		require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "wal backlog limit exceeded (entries=5/4)"))
		// **关键：让它们保持暂停**（否则被复活后就退出合格集 ⇒ 固定窗口也能"扫完" ⇒ 变异不可判别 ✗✗，
		// 这正是我上一版夹具的问题 ✓）。用**不可分类**的残余缺口（STDIO 类 ⇒ 永远挡住 ResumeAcquire ✓）。
		require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 100, 200, ledger.GapReasonStdioRawWriteFailed, "raw write failed"))
	}
	m.resumeBatchPerRound = 2 // 每轮窗口 = 2 ⇒ 6 家需 ≥ ⌈6/2⌉ = 3 轮 ✓

	var mu sync.Mutex
	visited := map[string]int{}
	m.resumeProbe = func(id string) { mu.Lock(); visited[id]++; mu.Unlock() }

	_, _ = m.maybeResumePausedSources()
	require.Equal(t, int64(n), m.ResumeSweepStats().EligibleNow,
		"gauge：合格暂停源总数必须一眼可见（%d 家 ✓）", n)
	_, _ = m.maybeResumePausedSources()
	_, _ = m.maybeResumePausedSources() // ⌈6/2⌉ = 3 轮

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, visited, n,
		"③ 轮内枚举必须覆盖**全部**合格暂停者（实测只覆盖 %d/%d ⇒ 固定窗口不轮转 ✗✗）", len(visited), n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("inst:rot%d/file", i)
		require.Contains(t, visited, id, "① 长期暂停的源 %s 必须进入候选集（不许被前 N 个永久挤掉 ✗）", id)
	}
}
