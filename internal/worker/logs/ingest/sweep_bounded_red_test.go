package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestSweepReturnsBoundedlyWhenStepBlocked（用户 2026-10-05 纪律⑥ 的落地 ✓✓）：
// **某一步被卡住时，兜底环必须在有界时间内返回**（直指现场 `ticks=1` 冻结的形态 ✗）。
//
// 注入点为什么必须"准且稳"（本用例的由来 ✓）：用全局锁 `m.mu` 注入会**既测错位置**
// （`sweepOnce` 入口的 `sweepBacklogTotal` 也要拿它 ⇒ 卡在更早处 ✗）**又偶发**
// （时紧时松地持锁 ⇒ 目标步骤常能拿到锁 ✗）⇒ 改在**被有界执行的那一步内部**注入
// （`sweepStepHold` ✓，与 `persistStepHold`/`persistPlanHold` 同族 ✓）。
//
// 转红方式（实测）：**变异=绕过 `runSweepStepBounded` 直接调 `fn`** ⇒ 钩子把调用永久按住 ⇒
// 本用例在守卫 `time.After` 处红 ✓✓。
func TestSweepReturnsBoundedlyWhenStepBlocked(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	src := SourceConfig{
		LogSourceID: "inst:bounded/file", SourceGeneration: "g1", Path: filepath.Join(root, "b.log"),
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:bounded", UTCDay: runtimeTestUTCDay(),
	}
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(src.Path, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o644))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src}})
	require.NoError(t, err)
	pipe := m.pipes["inst:bounded/file/g1"]
	require.NotNil(t, pipe)
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "wal backlog limit exceeded (entries=5/4)"))

	hold := make(chan struct{})
	m.sweepStepHold = func() { <-hold } // 确定性把"这一步"按住 ✓
	m.sweepStepBudget = 20 * time.Millisecond

	done := make(chan struct{})
	started := time.Now()
	go func() { defer close(done); _, _ = m.maybeResumePausedSources() }()
	select {
	case <-done:
		elapsed := time.Since(started)
		require.Less(t, elapsed, 2*time.Second,
			"某步被卡住时兜底环必须**有界返回**（实测 %s；现场形态是 ticks=1 冻结 ✗✗）", elapsed)
	case <-time.After(2 * time.Second):
		close(hold) // 收尾：别把夹具留死 ✓
		t.Fatal("兜底环被卡住的步骤拖住了（无有界返回 ✗✗）")
	}
	require.GreaterOrEqual(t, m.skipBlocked.Load(), int64(1),
		"超时必须计入 skipBlocked（现场据此判'是否被挡' ✓），实测 %d", m.skipBlocked.Load())

	close(hold) // 放行后台那次被放弃的调用 ✓
	select {
	case <-done: // 已是关闭状态或立即关闭 ✓
	case <-time.After(3 * time.Second):
		t.Fatal("收尾超时")
	}
	_ = filepath.Join
}
