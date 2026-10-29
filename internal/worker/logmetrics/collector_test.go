package logmetrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
)

// fakeSource 是可控的采集面读数源。
type fakeSource struct {
	snap ingest.LogMetricsSnapshot
}

func (f *fakeSource) LogMetricsSnapshot() ingest.LogMetricsSnapshot { return f.snap }

// newTestCollector 构造一个 root 指向临时目录、时间可控的采集器。
func newTestCollector(t *testing.T, src *fakeSource) (*Collector, *time.Time) {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	c := New(Options{Root: root}, func() (Source, error) { return src, nil })
	c.now = func() time.Time { return now }
	return c, &now
}

// 采集面不可用时整组不上报（Available=false），而不是报一串 0。
//
// 这是本项最重要的语义：0 在运维上读作「积压为零、零失败、零慢查询」——
// 恰好是最健康的读数。把「没采集」伪装成「一切正常」，比不实现更糟。
func TestCollector_UnavailableIsNotZero(t *testing.T) {
	// 采集面未装配（source 返回 nil）。
	c, _ := newTestCollector(t, nil)
	c.sampleOnce()
	require.False(t, c.Snapshot().Available, "无采集面时 Available 必须为 false")

	// 采集面存在但自报不可用（例如 0 源 / 未装配）。
	src := &fakeSource{snap: ingest.LogMetricsSnapshot{Available: false}}
	c2, _ := newTestCollector(t, src)
	c2.sampleOnce()
	require.False(t, c2.Snapshot().Available)
}

// 可用时各字段按源读数填充，速率由相邻两拍差分得出。
func TestCollector_FillsReadingsAndDerivesRates(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "var", "log")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	// 索引库 1 MB。
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "ingest.index.db"), make([]byte, 1<<20), 0o644))

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{snap: ingest.LogMetricsSnapshot{
		Available:            true,
		PersistBytesTotal:    1000,
		WALMaxBacklogEntries: 5000,
		WALMaxPausedSeconds:  3600,
		DeliveryBatches:      200,
		DeliveryFailures:     50,
		DominantFailureClass: "status_5xx",
		VerifyBackoffCount:   850,
		SlowSQLCount:         240,
		SlowSQLMaxMs:         2400,
	}}
	c := New(Options{Root: root}, func() (Source, error) { return src, nil })
	c.now = func() time.Time { return now }

	c.sampleOnce()
	first := c.Snapshot()
	require.True(t, first.Available)
	require.Equal(t, int64(1<<20), first.StateFileSizeBytes, "应真实 stat 索引文件")
	require.True(t, first.StateFileMeasured)
	require.False(t, first.StateFileGrowthMeasured, "首拍无基线，增速不可用")
	require.Zero(t, first.PersistWriteBytesPerSec, "首拍无基线，吞吐不可用")
	require.Equal(t, int64(5000), first.WALBacklogEntries)
	require.Equal(t, int64(3600), first.WALPausedSeconds)
	require.InDelta(t, 25.0, first.DeliveryFailureRate, 1e-9, "50/200 = 25%")
	require.Equal(t, "status_5xx", first.DeliveryFailureClass)
	require.Equal(t, int64(850), first.VerifyBackoffCount)
	require.Equal(t, int64(240), first.SlowSQLCount)
	require.Equal(t, int64(2400), first.SlowSQLMaxMs)

	// 第二拍：索引涨 30 MB / 30s = 1 MB/s；persist 累计增 3000 / 30s = 100 B/s。
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "ingest.index.db"),
		make([]byte, (1<<20)+(30<<20)), 0o644))
	src.snap.PersistBytesTotal = 4000
	now = now.Add(30 * time.Second)

	c.sampleOnce()
	second := c.Snapshot()
	require.True(t, second.StateFileGrowthMeasured)
	require.InDelta(t, float64(30<<20)/30, second.StateFileGrowthBytesPerSec, 1e-6)
	require.InDelta(t, 100.0, second.PersistWriteBytesPerSec, 1e-9)
}

// 索引文件不存在时不得报 0 字节（那是「索引被清空了」的虚假好转）。
func TestCollector_MissingStateFilesNotReportedAsZero(t *testing.T) {
	root := t.TempDir() // var/log 都不存在
	src := &fakeSource{snap: ingest.LogMetricsSnapshot{Available: true}}
	c := New(Options{Root: root}, func() (Source, error) { return src, nil })
	c.sampleOnce()

	snap := c.Snapshot()
	require.True(t, snap.Available, "采集面可用性独立于文件可测性")
	require.False(t, snap.StateFileMeasured, "拿不到文件时必须标记为未测到")
}

// 旧 state 文件的迁移归档必须计入占用——事故形态增长的正是这个面。
func TestCollector_CountsMigratedArchive(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "var", "log")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "ingest.index.db"), make([]byte, 1000), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "ingest.state.json.migrated"), make([]byte, 2000), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "unrelated.txt"), make([]byte, 4000), 0o644))

	src := &fakeSource{snap: ingest.LogMetricsSnapshot{Available: true}}
	c := New(Options{Root: root}, func() (Source, error) { return src, nil })
	c.sampleOnce()

	require.Equal(t, int64(3000), c.Snapshot().StateFileSizeBytes,
		"只计索引与迁移归档（1000+2000），不计无关文件")
}

// persist 累计计数回绕（索引重建）时不出速率，避免出现巨大负数或假尖峰。
func TestCollector_PersistCounterResetSkipsRate(t *testing.T) {
	src := &fakeSource{snap: ingest.LogMetricsSnapshot{Available: true, PersistBytesTotal: 1000}}
	c, now := newTestCollector(t, src)
	c.sampleOnce()

	// 计数器回退（重建索引）：本拍不得产出速率。
	src.snap.PersistBytesTotal = 100
	*now = now.Add(30 * time.Second)
	c.sampleOnce()
	require.Zero(t, c.Snapshot().PersistWriteBytesPerSec, "计数器重置时不出速率")
}

// 失败率的分母为 0 时返回 0（由 DeliveryBatches 区分「无投递活动」）。
func TestFailureRate(t *testing.T) {
	require.Zero(t, failureRate(0, 0), "无投递活动时失败率为 0")
	require.InDelta(t, 50.0, failureRate(1, 2), 1e-9)
	require.InDelta(t, 100.0, failureRate(5, 5), 1e-9)
	// 防御：失败数超过总数（不该发生）时按 100% 封顶，不产生 >100% 的读数。
	require.InDelta(t, 100.0, failureRate(9, 5), 1e-9)
}
