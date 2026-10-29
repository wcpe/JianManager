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

// TestSyncArchiveScanHotStartStop 验证定时扫描的启停同步是幂等的。
//
// 为什么幂等是硬要求：调用方每拍心跳都会同步一次当前周期（策略每拍下发），若同值也会重新
// 启停 goroutine，就等于每 30s 重启一次扫描器——周期一到就被重置，扫描**永不触发**
// （这正是「配了 30s 却 20 分钟零日志」那类问题的形态）。
func TestSyncArchiveScanHotStartStop(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)

	require.True(t, m.SyncArchiveScan(time.Hour), "首次启用应报告变化")
	require.False(t, m.SyncArchiveScan(time.Hour), "同值重复同步必须幂等（不得重启扫描 goroutine）")
	require.True(t, m.SyncArchiveScan(30*time.Minute), "周期变化应报告变化")
	require.False(t, m.SyncArchiveScan(30*time.Minute))
	require.True(t, m.SyncArchiveScan(0), "关闭应报告变化")
	require.False(t, m.SyncArchiveScan(0), "重复关闭应幂等")
	require.False(t, m.SyncArchiveScan(-time.Minute), "负数归一为关闭，与 0 同值 ⇒ 不再报告变化")

	require.NotPanics(t, func() { m.StopArchiveScan() })
	require.NotPanics(t, func() { m.StopArchiveScan() }, "停止必须幂等")
}

// TestRealtimeTailSwitchPausesAndResumes 验证实时采集应急开关：停采不推进位置、恢复续采不丢。
//
// 契约（spec §2.1）：关闭只停读——账本、WAL、游标与磁盘文件全部保持原样，实例进程与心跳
// 不受影响；恢复后由游标断点续采，停采期间写入的内容不会丢。
func TestRealtimeTailSwitchPausesAndResumes(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	livePath := filepath.Join(logDir, "latest.log")
	require.NoError(t, os.WriteFile(livePath, []byte("[10:00:00] [Server thread/INFO]: live line\n"), 0o600))

	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:pause/file", SourceGeneration: "holder-g1", Path: livePath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:pause", UTCDay: runtimeTestUTCDay(),
		}}})
	require.NoError(t, err)
	key := ledger.SourceKey{LogSourceID: "inst:pause/file", SourceGeneration: "holder-g1"}
	pipe := m.pipes[key.String()]
	require.NotNil(t, pipe)
	durable := func() uint64 {
		pos, ok := pipe.Positions()
		require.True(t, ok, "源应已有位置")
		return pos.Durable
	}
	appendLine := func(text string) {
		f, err := os.OpenFile(livePath, os.O_APPEND|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(text)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	// 停采：连续两轮完整采集轮，位置不得推进。
	require.True(t, m.SetAcqPolicy(AcqPolicy{RulesEnabled: true, RealtimeTail: false}))
	m.pollOnce()
	m.pollOnce()
	pausedAt := durable()
	require.Zero(t, pausedAt, "停采期间不得采集任何内容（源此前从未被采过）")

	// 停采期间实例继续写日志：只停读，实例文件不被触碰。
	appendLine("[10:00:05] [Server thread/INFO]: written while paused\n")
	size := uint64(0)
	if fi, err := os.Stat(livePath); err == nil {
		size = uint64(fi.Size())
	}
	require.Greater(t, size, pausedAt, "前置断言：停采期间确有未采内容")

	// 停采期间再跑一轮：仍不推进（证明不是"恰好本轮无新行"）。
	m.pollOnce()
	require.Equal(t, pausedAt, durable(), "停采期间每轮都不得推进位置")

	// 恢复：位置必须推进到覆盖停采期间的写入（断点续采，不丢内容）。
	//
	// 追加一行再驱动采集轮：采集器对**文件尾部行**要等下一行到达（或静默超时）才闭合，
	// 否则最后一行会被当成「可能仍在写」而留在缓冲里——这是既有的多行事件语义，
	// 与停采无关；不追加会让本用例误判成「恢复后读不到最后一行」。
	require.True(t, m.SetAcqPolicy(AcqPolicy{RulesEnabled: true, RealtimeTail: true}))
	appendLine("[10:00:10] [Server thread/INFO]: tail marker after resume\n")
	for round := 0; round < 6 && durable() < size; round++ {
		m.pollOnce()
	}
	require.GreaterOrEqual(t, durable(), size,
		"恢复后必须把停采期间的写入采完（位置推进到停采期间的文件大小，不丢内容）")
}
