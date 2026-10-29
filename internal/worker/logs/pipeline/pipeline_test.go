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
	return FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
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

// FR-474/475：Paper 风格多行堆栈文件 → pipeline 产出 ONE event（不是按行切）。
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

	// 2xx 写 REQUEST_DONE；投递路径自带一次 TryReclaim（acquire.Pipeline 的投递后回收）
	// ⇒ 只要认证覆盖到读入量，**本轮 Drain 结束前**回收就已推进到该位置。
	pos, ok := p.Positions()
	require.True(t, ok)
	require.Greater(t, pos.Read, uint64(0))
	require.Equal(t, logtypes.DeliveryRequestDone, p.DeliveryState())
	require.Equal(t, pos.Read, pos.Reclaim,
		"投递成功后回收已在同一轮推进到 min(认证覆盖, max(read,durable))")
	require.False(t, p.CanReclaimNow(), "已追平 ⇒ 没有待回收的前缀")
}

func mustPos(t *testing.T, p *Pipeline) logtypes.Positions {
	t.Helper()
	pos, ok := p.Positions()
	require.True(t, ok)
	return pos
}

// FR-473/474（2026-10-29 改写）：CanReclaimNow / TryReclaim 只认投递证据与读入量，
// 不再看恢复分段。
//
// 原用例（TestPipeline_CanReclaimGateAfterResponsibilityTransfer）验的是「登记 STAGED 恢复段
// 不得回收、责任转移到 WAL_RESPONSIBILITY_TRANSFERRED 后才放行、hold 仍拒绝」。
// 受管恢复分段机制已删（WAL 改纯内存后前提消失），那三条断言的对象不复存在。
// 但它们背后的**真实关切**仍是硬的：「2xx 之后到底能不能回收、能回收到哪个字节、
// 回收之后还有没有东西可回收」。所以本用例改写为逐条钉死新口径：
//
//	① 投递成功后回收已在同一轮推进到 min(认证覆盖, max(read,durable))（具体位置）；
//	② 已追平 ⇒ CanReclaimNow 为假，再调 TryReclaim 不推进、不报错（旧哨兵已删）。
//
// 转红方式（实测）：把 acquire.Pipeline 投递后的 TryReclaim 删掉 ⇒ ① 的
// `pos.Read == pos.Reclaim` 立即红（reclaim 会停在 0）；把 tryReclaimLocked 的上界
// 改回 Positions.Delivery ⇒ TestPipeline_AckLostBlocksReclaim 红。
func TestPipeline_ReclaimFollowsDeliveryEvidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(paperMultiline), 0o644))

	var got []logtypes.Event
	p := newFilePipeline(t, path, "src-reclaim", "g1", okDelivery(&got))
	_, err := p.Drain()
	require.NoError(t, err)
	require.Equal(t, logtypes.DeliveryRequestDone, p.DeliveryState())

	// ① 投递路径自带回收 ⇒ 本一轮结束时已推进到具体位置。
	pos := mustPos(t, p)
	require.Greater(t, pos.Read, uint64(0))
	require.Equal(t, pos.Read, pos.Reclaim, "回收推进到 min(认证覆盖末端, max(read,durable))")

	// ② 幂等：无事可做时返回当前值且不推进、不报错。
	require.False(t, p.CanReclaimNow(), "已追平后没有可回收的前缀")
	again, err := p.TryReclaim()
	require.NoError(t, err, "新语义下无事可做是 (当前Reclaim, nil)，不再有 ErrReclaimNoSegment")
	require.Equal(t, pos.Read, again)
}

// 2xx 带 AckLost ⇒ UNKNOWN ⇒ 认证覆盖为空 ⇒ 不得回收（2026-10-21 数据丢失形状的防线）。
//
// 新语义下 UNKNOWN 区间是回收的**停滞点**：TryReclaim 只能推到它之前（此处为 0）。
// 停滞必须由重投成功解开，而不是靠任何「证据代替物」（旧的恢复段机制已删）。
//
// 转红方式（实测）：把 tryReclaimLocked 的上界改回 Positions.Delivery
// ⇒ 本用例两条断言立即红（UNKNOWN 也会被算作可回收，回收会推进到读入量）。
func TestPipeline_AckLostBlocksReclaim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(paperMultiline), 0o644))

	var got []logtypes.Event
	p, err := New(Options{
		Mode:             ModeFilePrimary,
		LogSourceID:      "src-reclaim-acklost",
		SourceGeneration: "g1",
		Path:             path,
		Delivery: FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
			got = append(got, events...)
			return DeliveryResult{HTTPStatus: 200, AckLost: true}, nil
		}),
		BaseTime: time.Date(2026, 9, 20, 12, 0, 1, 0, time.UTC),
	})
	require.NoError(t, err)
	_, err = p.Drain()
	require.NoError(t, err)
	require.Equal(t, logtypes.DeliveryUnknown, p.DeliveryState())

	pos := mustPos(t, p)
	require.Greater(t, pos.Read, uint64(0), "前置：确实读入了字节")
	require.Equal(t, pos.Read, pos.Delivery, "Delivery 含 UNKNOWN ⇒ 连续前缀照常到读入量")
	require.Zero(t, pos.Reclaim, "UNKNOWN 区间不得被回收（它是停滞点，不是可回收证据）")
	entries, _ := p.WAL().Backlog()
	require.Positive(t, entries, "前置：存量确实还在内存队列里（没被回收剪掉）")
}

// FR-474：容量暂停必须记缺口，禁止静默丢弃。
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
		Delivery: FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
			return DeliveryResult{HTTPStatus: 200, AckLost: true}, nil
		}),
		BaseTime: time.Date(2026, 9, 20, 12, 0, 1, 0, time.UTC),
	})
	require.NoError(t, err)
	_, err = p.Drain()
	require.NoError(t, err)
	require.Equal(t, logtypes.DeliveryUnknown, p.DeliveryState())
	// UNKNOWN 区间是回收的停滞点：新语义下这个停滀是**行为上可观察到**的。
	require.Zero(t, mustPos(t, p).Reclaim, "UNKNOWN 不得被回收")
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
