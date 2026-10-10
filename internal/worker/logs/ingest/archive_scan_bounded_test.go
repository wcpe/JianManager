package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是「归档扫描无界等 cycleMu」修复（2026-10-10）的确定性红/绿，与
// resolve_gaps_bounded_test.go（C 修复）同型。
//
// 现场：`log_ingest.archive_scan_interval: 30s` 已启用，但 20 分钟内
// 「手动导入归档扫描完成」与「定时归档导入扫描失败」**日志各 0 条** —— 扫描 goroutine 每 30 秒
// 调一次 ImportArchivesNow，卡在无界 `m.cycleMu.Lock()`：既不干活也不报错。
// （已只读排除"无归档可扫"：164 个 FILE_PRIMARY 源中 151 个含 814 个 gz，而 catalog 中 gzip
// 段记录 0 条 ⇒ pending 非空、本应有日志。）
//
// 修法：有界获取（TryLock 轮询 ≤ archiveScanCycleMuWait）——扫描"忙即明确失败"是安全的：
// 它未做任何变更，定时路径下一轮再来（幂等），手动路径由运维重试。
//
// 绿（现状）：测试占住 cycleMu ⇒ 调用在预算内返回「采集轮忙」且 scanned/imported 均为 0，
// 且**同一份归档在锁归还后确实能被扫到**（证明返回 0 不是因为"无可扫"）；
// 红（变异：改回无限 cycleMu.Lock）⇒ 调用阻塞远超预算 ⇒ "超预算仍未返回"分支转红。
func TestImportArchivesNowBoundedOnCycleMu(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	archivePath := filepath.Join(logDir, "2026-09-22-1.log.gz")
	writeRuntimeGzip(t, archivePath, "[10:00:00] [Server thread/INFO]: pending archive line\n")
	livePath := filepath.Join(logDir, "latest.log")
	require.NoError(t, os.WriteFile(livePath, []byte("[10:00:01] [Server thread/INFO]: live line\n"), 0o600))

	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:scan/file", SourceGeneration: "holder-g1", Path: livePath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:scan", UTCDay: runtimeTestUTCDay(),
		}}})
	require.NoError(t, err)
	key := ledger.SourceKey{LogSourceID: "inst:scan/file", SourceGeneration: "holder-g1"}

	// 占住 cycleMu（模拟"采集轮长持有"；现场即 pollOnce 的持有 33.8 万条 WAL 序列化）。
	//
	// 不用 defer 解锁：本用例要在解锁后走"对照段"，且 t.Cleanup 里的 Stop 也取 cycleMu
	// （defer 先于 Cleanup 执行，两种写法都不会自锁，这里显式归还以便中途对照）。
	m.cycleMu.Lock()
	locked := true
	defer func() {
		if locked {
			m.cycleMu.Unlock()
		}
	}()

	type scanResult struct {
		scanned, imported int
		err               error
	}
	done := make(chan scanResult, 1)
	started := time.Now()
	go func() {
		scanned, imported, err := m.ImportArchivesNow("", archiveScanOperator)
		done <- scanResult{scanned: scanned, imported: imported, err: err}
	}()
	select {
	case got := <-done:
		require.Error(t, got.err, "cycleMu 被占时必须明确失败而非挂死")
		require.Contains(t, got.err.Error(), "采集轮忙", "错误必须可判读（忙）")
		require.Contains(t, got.err.Error(), "预算", "错误必须点明超出等待预算")
		require.Contains(t, got.err.Error(), "未执行任何变更", "错误必须说明零副作用，便于运维安全重试")
		require.Zero(t, got.scanned, "超时路径不得产生任何副作用（scanned 必须为 0）")
		require.Zero(t, got.imported, "超时路径不得产生任何副作用（imported 必须为 0）")
		require.Less(t, time.Since(started), 2*archiveScanCycleMuWait,
			"必须在预算内返回（不被无限阻塞）")
	case <-time.After(3 * archiveScanCycleMuWait):
		t.Fatal("超预算仍未返回 ⇒ 仍在无限等 cycleMu（红：现场「定时扫描零日志」挂死形态）")
	}
	// 超时路径不得把归档标成已导入（否则下轮会被 pendingArchives 跳过 = 静默丢归档）。
	if entry := m.pipes[key.String()].Ledger().Get(key); entry != nil {
		for _, segment := range entry.Segments {
			require.False(t, segment.Imported, "超时路径不得把归档标记为已导入")
		}
	}

	// 对照段：锁归还后**同一调用**必须真的扫到并导入这份归档 ⇒ 上面的 0 是"没拿到锁"，不是"无归档"。
	m.cycleMu.Unlock()
	locked = false
	scanned, imported, err := m.ImportArchivesNow("", archiveScanOperator)
	require.NoError(t, err)
	require.Equal(t, 1, scanned, "锁归还后应扫到 1 份待导入归档（对照：证明超时路径的 0 不是「无可扫」）")
	require.Equal(t, 1, imported, "锁归还后应真正导入该归档")
	entry := m.pipes[key.String()].Ledger().Get(key)
	require.NotNil(t, entry)
	marked := false
	for _, segment := range entry.Segments {
		if filepath.Clean(segment.Path) == filepath.Clean(archivePath) && segment.Imported {
			marked = true
		}
	}
	require.True(t, marked, "成功路径必须把归档记为已导入（幂等的依据）")
}

// 变异记录（2026-10-10 真实执行，逐字还原）：
//
//	变异：把 ImportArchivesNow 里的
//	        if !m.acquireCycleMuBounded(context.Background(), archiveScanCycleMuWait) { ...超时 return... }
//	        w.locked()
//	      改回无界 `m.cycleMu.Lock()` + `w.locked()`（其余一字不动）。
//	结果：FAIL，`archive_scan_bounded_test.go:83: 超预算仍未返回 ⇒ 仍在无限等 cycleMu
//	      （红：现场「定时扫描零日志」挂死形态）`，用例耗时 15.04s（= 3×archiveScanCycleMuWait
//	      的超时分支；该 goroutine 此后一直阻塞在 Lock 上，直到用例 defer 归还 cycleMu 才继续）。
//	还原后：PASS，耗时 5.04s（预算 5s 内返回 + 锁归还后的对照段真正导入 1 份归档）。
