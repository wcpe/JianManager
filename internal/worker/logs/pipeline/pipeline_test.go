package pipeline

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

const paperMultiline = `[12:00:00] [Server thread/ERROR]: Encountered an unexpected exception
java.lang.NullPointerException: Cannot invoke "String.length()"
	at net.minecraft.server.MinecraftServer.tickChildren(MinecraftServer.java:1)
Caused by: java.lang.IllegalStateException: nested
	at com.example.inner(Inner.java:9)
`

func okDelivery(p *[]logtypes.Event) DeliveryHook {
	return FuncHook(func(events []logtypes.Event) (DeliveryResult, error) {
		*p = append(*p, events...)
		return DeliveryResult{HTTPStatus: 200}, nil
	})
}

func newFilePipeline(t *testing.T, path, id, gen string, hook DeliveryHook) *Pipeline {
	t.Helper()
	p, err := New(Options{
		Mode:             ModeFilePrimary,
		LogSourceID:      id,
		SourceGeneration: gen,
		Path:             path,
		Stream:           "stdout",
		Delivery:         hook,
		Location:         time.UTC,
		BaseTime:         time.Date(2026, 9, 20, 12, 0, 1, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, ModeFilePrimary, p.Mode())
	return p
}

// FR-473/435：Paper 风格多行堆栈文件 → pipeline 产出 ONE event（不是按行切）。
func TestPipeline_PaperMultilineOneEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(paperMultiline), 0o644))

	var got []logtypes.Event
	p := newFilePipeline(t, path, "src-paper", "g1", okDelivery(&got))

	events, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, events, 1, "multiline stack must merge into one event")
	require.Len(t, got, 1, "delivery hook receives the same single event")
	require.Equal(t, 1, p.DeliveredCount())

	ev := events[0]
	require.Equal(t, "ERROR", ev.Level)
	require.Equal(t, normalize.ParserVersion, ev.Source.ParserVersion)
	require.Equal(t, "stdout", ev.Stream)
	require.Contains(t, ev.Message, "NullPointerException")
	require.Contains(t, ev.Message, "Caused by: java.lang.IllegalStateException")
	require.Contains(t, ev.Message, "MinecraftServer.java:1")
	require.Contains(t, ev.Message, "Inner.java:9")
	require.NotEmpty(t, ev.EventID)
	require.NotEmpty(t, ev.CanonicalHash)

	// 事件身份可复算；record 为绝对源位置（整段多行）。
	wantID := logtypes.EventID(ev.Source, ev.Record)
	require.Equal(t, wantID, ev.EventID)
	require.EqualValues(t, 0, ev.Record.Start)
	require.Greater(t, ev.Record.End, ev.Record.Start)

	// 事件数 vs 行数：多行归并后事件数 < 原文行数。
	st := p.Stats()
	require.Equal(t, 5, st.LineCount)
	require.Equal(t, 1, st.EventCount)
	require.Less(t, st.EventCount, st.LineCount)

	// WAL durable 先于 delivery；2xx 只写 REQUEST_DONE。
	pos, ok := p.Positions()
	require.True(t, ok)
	require.Greater(t, pos.Read, uint64(0))
	require.Equal(t, logtypes.DeliveryRequestDone, p.DeliveryState())

	// HTTP 2xx 单独不得推进 reclaim；无恢复分段时 CanReclaim=false。
	require.False(t, p.CanReclaimNow())
	_, err = p.TryReclaim()
	require.Error(t, err, "2xx must not reclaim without recovery responsibility transfer")
	require.Equal(t, uint64(0), mustPos(t, p).Reclaim)
}

func mustPos(t *testing.T, p *Pipeline) logtypes.Positions {
	t.Helper()
	pos, ok := p.Positions()
	require.True(t, ok)
	return pos
}

// FR-473：轮转压缩后同一逻辑源不重复计数；ArchiveImporter 跳过已关联整包。
func TestPipeline_RotationNoDoubleCount(t *testing.T) {
	dir := t.TempDir()
	latest := filepath.Join(dir, "latest.log")
	rotated := filepath.Join(dir, "latest.log.1")
	gzPath := filepath.Join(dir, "latest.log.1.gz")

	lines := []string{
		`[12:00:00] [Server thread/INFO]: Starting minecraft server`,
		`[12:00:01] [Server thread/INFO]: Preparing spawn`,
		`[12:00:02] [Server thread/INFO]: Done (2.0s)`,
	}
	require.NoError(t, os.WriteFile(latest, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	var got []logtypes.Event
	p := newFilePipeline(t, latest, "src-rot", "g1", okDelivery(&got))
	p.SetRotateTarget(rotated)

	events, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, events, 3, "three INFO lines → three events")
	require.Len(t, got, 3)
	logicalAfterLive := p.FileTailer().LogicalPos()
	require.Greater(t, logicalAfterLive, uint64(0))

	// 轮转：内容进 rotated/gz，latest 截断。
	require.NoError(t, os.WriteFile(rotated, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	writeGzipLines(t, gzPath, lines)
	require.NoError(t, os.WriteFile(latest, []byte(""), 0o644))
	p.SetRotateTarget(gzPath)

	_, err = p.Poll()
	require.NoError(t, err)
	require.GreaterOrEqual(t, p.FileTailer().RotationCount(), 1)

	// 登记已导入的轮转分段 + 链接，禁止归档整包重放。
	key := p.Key()
	require.NoError(t, p.Ledger().RegisterSegment(key, ledger.Segment{
		Path:     gzPath,
		Kind:     ledger.SegmentGzip,
		EndPos:   logicalAfterLive,
		Imported: true,
	}))
	require.NoError(t, p.Ledger().LinkRotation(key, latest, gzPath, logicalAfterLive))

	impRes, err := p.ImportArchive(gzPath)
	require.NoError(t, err)
	require.True(t, impRes.Skipped, "rotation-linked archive must skip whole reimport")
	require.Equal(t, 0, impRes.ImportedCount)

	// 总事件仍为 3：live 已计入 + archive 跳过，不双计。
	require.Equal(t, 3, p.EventsEmitted(), "normalize events from live file")
	require.Equal(t, 3, p.DeliveredCount(), "rotation-linked archive must not double-count deliveries")
	require.Empty(t, p.Gaps(), "rotation must not create gaps")
}

// FR-472/434：恢复分段责任转移后 CanReclaim 才放行；hold 仍拒绝。
func TestPipeline_CanReclaimGateAfterResponsibilityTransfer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(paperMultiline), 0o644))

	var got []logtypes.Event
	p := newFilePipeline(t, path, "src-reclaim", "g1", okDelivery(&got))
	_, err := p.Drain()
	require.NoError(t, err)
	require.Equal(t, logtypes.DeliveryRequestDone, p.DeliveryState())

	// 无恢复分段：不得回收。
	require.False(t, p.CanReclaimNow())

	// 登记 STAGED 恢复分段：仍不得回收。
	pos := mustPos(t, p)
	require.NoError(t, p.BindRecoverySegment("seg-1", "/tmp/seg-1", 0, pos.Durable))
	require.False(t, p.CanReclaimNow())
	_, err = p.TryReclaim()
	require.Error(t, err)

	// 责任转移后允许回收。
	require.NoError(t, p.TransitionRecovery("seg-1", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, p.TransitionRecovery("seg-1", logtypes.RecoveryWALResponsibilityXfer, "", ""))
	require.True(t, p.CanReclaimNow())
	reclaimed, err := p.TryReclaim()
	require.NoError(t, err)
	require.Equal(t, pos.Durable, reclaimed)
	require.Equal(t, pos.Durable, mustPos(t, p).Reclaim)

	// hold 时再次拒绝。
	require.NoError(t, p.BindRecoverySegment("seg-2", "/tmp/seg-2", pos.Durable, pos.Durable+10))
	require.NoError(t, p.Ledger().SetRecoveryHold(p.Key(), "seg-2", true))
	_, err = p.TryReclaim()
	require.Error(t, err)
}

// FR-473：容量暂停必须记缺口，禁止静默丢弃。
func TestPipeline_CapacityPauseRecordsGap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(paperMultiline), 0o644))

	budget := acquire.DefaultCapacityBudget()
	budget.DiskUsagePercent = 95 // ≥ 90 pause
	p, err := New(Options{
		Mode:             ModeFilePrimary,
		LogSourceID:      "src-cap",
		SourceGeneration: "g1",
		Path:             path,
		Delivery:         NopHook{},
		Capacity:         &budget,
		BaseTime:         time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	_, err = p.Drain()
	require.Error(t, err)
	require.Contains(t, err.Error(), "capacity paused")
	gaps := p.Gaps()
	require.NotEmpty(t, gaps, "capacity pause must report gap")
	require.True(t, p.Ledger().Get(p.Key()).AcquirePaused)
}

// STDIO_PRIMARY：受管 Raw 通道也走同一 normalize→WAL→delivery。
func TestPipeline_StdioPrimaryMultiline(t *testing.T) {
	reader := strings.NewReader(paperMultiline)
	var got []logtypes.Event
	p, err := New(Options{
		Mode:             ModeStdioPrimary,
		LogSourceID:      "src-stdio",
		SourceGeneration: "g1",
		Reader:           reader,
		Delivery:         okDelivery(&got),
		BaseTime:         time.Date(2026, 9, 20, 12, 0, 1, 0, time.UTC),
	})
	require.NoError(t, err)

	events, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Contains(t, events[0].Message, "NullPointerException")
	require.Len(t, got, 1)
}

// ackLost → UNKNOWN，保留恢复责任。
func TestPipeline_AckLostYieldsUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(paperMultiline), 0o644))

	p, err := New(Options{
		Mode:             ModeFilePrimary,
		LogSourceID:      "src-ack",
		SourceGeneration: "g1",
		Path:             path,
		Delivery: FuncHook(func(events []logtypes.Event) (DeliveryResult, error) {
			return DeliveryResult{HTTPStatus: 200, AckLost: true}, nil
		}),
		BaseTime: time.Date(2026, 9, 20, 12, 0, 1, 0, time.UTC),
	})
	require.NoError(t, err)
	_, err = p.Drain()
	require.NoError(t, err)
	require.Equal(t, logtypes.DeliveryUnknown, p.DeliveryState())
	require.False(t, p.CanReclaimNow())
}

func writeGzipLines(t *testing.T, path string, lines []string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	zw := gzip.NewWriter(f)
	for _, ln := range lines {
		_, err := zw.Write([]byte(ln + "\n"))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
}
