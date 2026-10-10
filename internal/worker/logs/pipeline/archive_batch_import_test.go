package pipeline

// 缺陷二回归：大归档导入一次灌入 ⇒ 撞单源 WAL 上限 ⇒ 源永久卡死（2026-10-10 生产实测）。
//
// ── 现场 ───────────────────────────────────────────────────────────────────────
// `inst:220/file`（gz 544,278 行）：
//   - 归档导入解析出 **128,856 个事件** = 单源上限 20000 的 **6.4 倍**；
//   - 触发 `单源积压超过上限，已暂停该源采集`（`entries=128856/20000`）；
//   - 重启后更坏：`entries=424205/20000`、`paused=1`；
//   - `delivery_pos` 一直 None、VL 侧 inst:220 = 0 行 ⇒ **数据进不去 VL**；
//   - 不自愈：5 分钟内暂停源数 101 → 102（零自愈）。
//
// ── 机制 ───────────────────────────────────────────────────────────────────────
// `ImportArchive` 把整个 gz 的事件作为**一个**批次交给 WAL（追加 → Commit → 投递），
// 单次追加量 = 整个归档的事件数 ⇒ 一次越闸；越闸后 `WAL.Append` 的首判据（已暂停）
// 拒绝**后续**追加 ⇒ 源在暂停上停死。
//
// ── 本文件守住什么 ─────────────────────────────────────────────────────────────
// ① 大归档能被**完整**导入（跨轮推进到 EOF）且全程**不触发暂停**（主回归）；
// ② 分批/跨轮后事件总数守恒、顺序与身份不变（不丢、不乱、不重）；
// ③ 单轮放行量恒不越闸（预算由生效上限派生，不是写死的常数）；
// ④ 中途失败不伪装成成功（不置 Imported，且已落库批次仍在外发）；
// ⑤ 小归档仍是单轮一批（既有行为不回退）。

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// writeBigGzip 生成一个含 count 行可解析日志的 gz 归档（文件名带 UTC 日，供 normalize BaseTime）。
func writeBigGzip(t *testing.T, dir string, count int) string {
	t.Helper()
	path := filepath.Join(dir, "2026-09-20-latest.log.gz")
	f, err := os.Create(path)
	require.NoError(t, err)
	zw := gzip.NewWriter(f)
	var sb strings.Builder
	// 时间戳跨秒递增，保证每行 event_id 唯一（避免身份冲突干扰计数）。
	for i := 0; i < count; i++ {
		sec := i % 60
		min := (i / 60) % 60
		hour := (i / 3600) % 24
		fmt.Fprintf(&sb, "[%02d:%02d:%02d] [Server thread/INFO]: archive line %d\n", hour, min, sec, i)
	}
	_, err = zw.Write([]byte(sb.String()))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return path
}

// newLimitedPipeline 建一条**带真实 WAL 上限**的管道（用于按生效上限推导放行量的断言）。
//
// 同时注入 **ReclaimProof**：回收（TryReclaim）在无恢复分段时会被门禁拒绝
// （`no recovery segment covers position`），而回收不推进 ⇒ 积压不回落 ⇒ 余量永不恢复。
// 生产路径由 ingest.Manager 的 releaseRecovery 提供该证明；这里用等价的最小实现
// （登记一个「责任已转移」的分段覆盖当前位置），使「积压回落 → 余量恢复 → 导入续推」
// 这条链在用例里**能真的走通**，否则测的就不是修复而是死锁。
func newLimitedPipeline(t *testing.T, maxEntries int64, hook DeliveryHook) *Pipeline {
	t.Helper()
	dir := t.TempDir()
	live := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(live, nil, 0o644))
	var p *Pipeline
	var err error
	p, err = New(Options{
		Mode:             ModeFilePrimary,
		LogSourceID:      "src-big-archive",
		SourceGeneration: "g1",
		Path:             live,
		Stream:           "stdout",
		Delivery:         hook,
		Location:         time.UTC,
		BaseTime:         time.Date(2026, 9, 20, 12, 0, 1, 0, time.UTC),
		// MaxBytes 传负值哨兵 = 字节维度不设上限（键 log_capacity.max_wal_bytes=0 的语义），
		// 使本用例只测「条目维度」这一条闸，避免字节闸在细小用例里意外先咬而混淆归因。
		WALLimits: &acquire.WALLimits{MaxEntries: maxEntries, MaxBytes: -1},
		ReclaimProof: func(events []logtypes.Event) error {
			if len(events) == 0 || p == nil {
				return nil
			}
			return registerReclaimProof(p, events[len(events)-1].Record.End)
		},
	})
	require.NoError(t, err)
	return p
}

// registerReclaimProof 登记「已投递区间的内容级保证」：把恢复分段标为责任已转移，
// 使 CanReclaim 放行回收（与生产 releaseRecovery 的终态等价，只是不写 catalog）。
func registerReclaimProof(p *Pipeline, through uint64) error {
	if through == 0 {
		return nil
	}
	segID := fmt.Sprintf("test-recovery-%d", through)
	if err := p.BindRecoverySegment(segID, "test-recovery", 0, through); err != nil {
		return err
	}
	return p.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer,
		logtypes.ReleaseProjectionBacked, "test")
}

// drainArchive 反复调用 ImportArchive 直到归档被读完（模拟采集轮的多轮推进）。
//
// 返回：累计投递事件、每轮放行条数、轮数。任一错误立即返回（由调用方断言）。
// maxRounds 兜住「永远推进不完」的死循环（真实场景由采集轮 ticker 持续驱动，测试里必须自己设界）。
func drainArchive(t *testing.T, p *Pipeline, gzPath string, maxRounds int) (delivered []logtypes.Event, perRound []int, err error) {
	t.Helper()
	for round := 0; round < maxRounds; round++ {
		res, callErr := p.ImportArchive(gzPath)
		if callErr != nil {
			return delivered, perRound, callErr
		}
		if res == nil {
			return delivered, perRound, fmt.Errorf("ImportArchive returned nil result")
		}
		delivered = append(delivered, res.Events...)
		perRound = append(perRound, len(res.Events))
		if !res.Partial {
			// 归档已被读完（无论是否本次仍有事件）。
			return delivered, perRound, nil
		}
	}
	return delivered, perRound, fmt.Errorf("archive import did not finish within %d rounds", maxRounds)
}

// TestArchiveImportBatchesNeverExceedWALLimit 是缺陷二的**主回归**（可变异转红）。
//
// 形态与现场一致：归档事件数（1200）是单源上限（300）的 4 倍。
//
// 采样点是**投递钩子**，因为它在 `WAL.Append → Commit` **之后**、投递之前被调用
// （见 acquire.Pipeline.Ingest 的顺序）—— 此刻读到的积压与暂停态正是「本次追加造成了什么」。
//
// 断言三件事（缺一不可，原因见下）：
//  1. 归档被**完整**导入（总数守恒）；
//  2. 每次追加之**后**源都不处于暂停态，且积压从未越过上限；
//  3. 每轮放行量 ≤ 上限一半。
//
// 为什么必须是「追加后即刻」而不是「全部跑完再看」（第一版的实测教训 ✗）：
// 第一版只在最后一轮结束时断言 `AcquirePaused == false`，结果**变异不转红** ——
// 因为越闸造成的暂停会被紧随其后的回收（TryReclaim → 滞回）在同一轮内自动清掉，
// 于是「跑完再看」永远是 false，这条断言形同虚设。真正该守的是**那一刻越没越闸**，
// 证据必须在越闸发生的同时取到，而不能事后回看。
//
// 变异验证（实测）：把 `ImportArchive` 换回单次 `p.ingest(全部事件)`（修复前实现），
// 第 2 条即转红（一次追加 1200 > 300 ⇒ 投递时刻 entries=1200、已暂停）。
func TestArchiveImportBatchesNeverExceedWALLimit(t *testing.T) {
	const (
		totalEvents = 1200
		walMax      = int64(300)
	)
	dir := t.TempDir()
	gzPath := writeBigGzip(t, dir, totalEvents)

	var (
		p          *Pipeline
		delivered  []logtypes.Event
		batchSizes []int
		// peakBacklog 是「追加之后」的积压峰值；pausedAtDelivery 记录投递时刻是否已暂停。
		peakBacklog      int64
		pausedAtDelivery bool
	)
	p = newLimitedPipeline(t, walMax, FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
		delivered = append(delivered, events...)
		batchSizes = append(batchSizes, len(events))
		if backlog, _ := p.wal.Backlog(); backlog > peakBacklog {
			peakBacklog = backlog
		}
		if entry := p.Ledger().Get(p.Key()); entry != nil && entry.AcquirePaused {
			pausedAtDelivery = true
		}
		return DeliveryResult{HTTPStatus: 200}, nil
	}))

	_, perRound, err := drainArchive(t, p, gzPath, 64)
	require.NoError(t, err, "大归档必须能被跨轮完整导入（不得因越闸暂停而卡死）")

	// ③ 每轮放行量不越闸。
	limit := int(walMax / 2)
	for index, size := range perRound {
		require.LessOrEqual(t, size, limit,
			"第 %d 轮放行 %d 条超过上限一半 %d：单次追加不得把源推过闸", index, size, limit)
	}
	require.Greater(t, len(perRound), 1, "归档远大于上限时必须真的分多轮（否则等同于修复前的一次灌入）")

	// ② 追加后即刻的证据：不得越闸、不得暂停（这是现场「源停死」的直接成因）。
	require.False(t, pausedAtDelivery,
		"投递时刻源已处于暂停态：本次追加越闸了（现场后果 = 源停死、数据进不去 VL、零自愈）")
	require.LessOrEqual(t, peakBacklog, walMax,
		"追加后的积压峰值 %d 越过上限 %d —— 单次追加把源推过了闸", peakBacklog, walMax)

	// ① 完整导入 + 终态未暂停。
	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	require.False(t, entry.AcquirePaused, "终态不得停留在暂停上")
	require.Empty(t, entry.PauseReason, "不得留下任何暂停原因")
	require.Equal(t, totalEvents, len(delivered),
		"投递面拿到的总条数必须等于归档事件数（跨轮推进不丢）")

	// 归档最终必须被标为已导入（否则采集轮会永远重复导入它）。
	require.True(t, archiveImported(t, p, gzPath), "读完的归档必须被标为已导入")
}

// archiveImported 报告该归档在账本里是否已被标为已导入。
func archiveImported(t *testing.T, p *Pipeline, gzPath string) bool {
	t.Helper()
	entry := p.Ledger().Get(p.Key())
	if entry == nil {
		return false
	}
	for _, seg := range entry.Segments {
		if filepath.Clean(seg.Path) == filepath.Clean(gzPath) && seg.Imported {
			return true
		}
	}
	return false
}

// TestArchiveImportBudgetDerivesFromEffectiveLimits 守住「放行量来自**生效**上限」，
// 而不是又一个写死的常数 —— 「代码默认 2000 条、生产生效 20000 条」的脱节正是现场形态之一。
//
// 同时守住「余量耗尽 ⇒ 明确拒绝推进」：返回 ok=false 而不是硬塞一个最小批次
// （硬塞就会越闸暂停，那正是本缺陷的形态）。
//
// 变异：把 `archiveImportBudget` 改成返回固定常数 ⇒ 本用例转红。
func TestArchiveImportBudgetDerivesFromEffectiveLimits(t *testing.T) {
	for _, maxEntries := range []int64{300, 2000, 20000, 40000} {
		p := newLimitedPipeline(t, maxEntries, okDeliveryNoop())
		size, ok := p.archiveImportBudget()
		require.True(t, ok)
		want := int(maxEntries / 2)
		if want > archiveImportBatchMax {
			want = archiveImportBatchMax
		}
		require.Equal(t, want, size,
			"生效上限 %d 时放行量应为 %d（由上限派生，不得写死）", maxEntries, want)
	}

	// 余量耗尽：拒绝推进（而不是硬塞最小批次）。
	p := newLimitedPipeline(t, 300, okDeliveryNoop())
	require.NoError(t, p.wal.Append(makeTestEvents(350)...))
	require.Positive(t, mustBacklog(t, p), "前置：积压确实已顶到上限")
	size, ok := p.archiveImportBudget()
	require.False(t, ok, "余量耗尽时必须明确拒绝本轮推进（硬塞最小批次会越闸暂停）")
	require.Zero(t, size)
}

func okDeliveryNoop() DeliveryHook {
	return FuncHook(func([]logtypes.Event, bool) (DeliveryResult, error) {
		return DeliveryResult{HTTPStatus: 200}, nil
	})
}

// makeTestEvents 造 n 条最小可入 WAL 的事件（只为把积压顶起来）。
func makeTestEvents(n int) []logtypes.Event {
	out := make([]logtypes.Event, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, logtypes.Event{
			EventID: fmt.Sprintf("filler-%d", i),
			Record:  logtypes.RecordRange{Start: uint64(i * 2), End: uint64(i*2 + 1)},
			Message: "filler",
		})
	}
	return out
}

func mustBacklog(t *testing.T, p *Pipeline) int64 {
	t.Helper()
	entries, _ := p.wal.Backlog()
	return entries
}

// TestArchiveImportBatchesPreserveOrderAndIdentities 守住分批/跨轮不改变语义：
// 事件顺序与身份必须与「不分批」逐位一致（无乱序、无重复、无丢行）。
//
// 变异：让续读偏移算错（如忽略 EndPos ⇒ 每轮重读同一前缀）⇒ 顺序/重复断言转红。
func TestArchiveImportBatchesPreserveOrderAndIdentities(t *testing.T) {
	const totalEvents = 700
	dir := t.TempDir()
	gzPath := writeBigGzip(t, dir, totalEvents)

	var got []logtypes.Event
	p := newLimitedPipeline(t, 100, FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
		got = append(got, events...)
		return DeliveryResult{HTTPStatus: 200}, nil
	}))
	_, perRound, err := drainArchive(t, p, gzPath, 64)
	require.NoError(t, err)
	require.Greater(t, len(perRound), 1, "上限 100 而归档 700 条 ⇒ 必须多轮")

	require.Equal(t, totalEvents, len(got), "总数守恒（跨轮续读不丢也不重）")
	ids := make(map[string]bool, len(got))
	prevEnd := uint64(0)
	for index, ev := range got {
		require.False(t, ids[ev.EventID], "第 %d 条 event_id 重复：%s（续读偏移算错会重读前缀）", index, ev.EventID)
		ids[ev.EventID] = true
		// 记录区间必须有严格全序（顺序读的产物）：乱序或丢行都会破坏它。
		require.GreaterOrEqual(t, ev.Record.Start, prevEnd,
			"第 %d 条记录起点 %d 早于上一条结束 %d：分批改变了顺序", index, ev.Record.Start, prevEnd)
		prevEnd = ev.Record.End
	}
	// 解析面的事件数与投递面一致：跨轮读的是**归档的不同区段**，每段各建一个
	// NormalizeBoundary（与生产同形），故 Stats() 只反映最后一次；这里改为断言
	// 「投递面总数 = 归档行数」这一条真正的不变量（已在上面的 len(got) 断言里守住），
	// 并额外确认段内没有多行误并（每行都是独立 INFO 行 ⇒ 事件数 == 行数）。
	require.Equal(t, totalEvents, len(got), "事件数必须等于归档行数（多行误并会减少它）")
}

// TestArchiveImportFailureDoesNotMarkImported 守住「中途失败不伪装成成功」：
// 投递失败时归档**不得**被 MarkImported（否则下一轮会跳过它 ⇒ 静默丢尾部），
// 且已成功落库的前若干轮仍在外发（不因后续失败而回退已完成的进展）。
//
// 变异：让 ImportArchive 无论成败都 MarkImported ⇒ 本用例转红。
func TestArchiveImportFailureDoesNotMarkImported(t *testing.T) {
	const totalEvents = 600
	dir := t.TempDir()
	gzPath := writeBigGzip(t, dir, totalEvents)

	attempts := 0
	var delivered []logtypes.Event
	p := newLimitedPipeline(t, 200, FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
		attempts++
		if attempts >= 2 {
			return DeliveryResult{HTTPStatus: 500}, fmt.Errorf("injected delivery failure")
		}
		delivered = append(delivered, events...)
		return DeliveryResult{HTTPStatus: 200}, nil
	}))

	_, _, err := drainArchive(t, p, gzPath, 8)
	require.Error(t, err, "投递失败必须如实上报（不得伪装成成功）")
	require.NotEmpty(t, delivered, "第一轮已成功落库的事件仍应在外发（不得因后续失败回退已完成的进展）")
	require.Less(t, len(delivered), totalEvents, "失败点之后不得继续追加（否则只会把积压推高）")
	require.False(t, archiveImported(t, p, gzPath),
		"中途失败的归档不得被标为已导入（否则下一轮跳过它 ⇒ 静默丢尾部数据）")

	// 失败恢复后：归档仍应能被完整导入（证明「未置导入」确实带来了可续传）。
	attempts = -1000 // 后续调用全部成功
	recovered, perRound, err := drainArchive(t, p, gzPath, 64)
	require.NoError(t, err, "投递恢复后归档必须仍可被完整导入（可续传）")
	require.NotEmpty(t, perRound)
	require.True(t, archiveImported(t, p, gzPath), "恢复后读完的归档必须被标为已导入")
	require.Greater(t, len(recovered), 0)
}

// TestArchiveImportSmallArchiveStaysSingleBatch 守住既有行为不回退：
// 事件数远小于上限的归档仍走**单轮单批**（不把小事切碎引入额外 WAL/投递往返），
// 且导入完成后立即可被标为已导入。
func TestArchiveImportSmallArchiveStaysSingleBatch(t *testing.T) {
	dir := t.TempDir()
	gzPath := writeBigGzip(t, dir, 40)

	var batches []int
	p := newLimitedPipeline(t, 20000, FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
		batches = append(batches, len(events))
		return DeliveryResult{HTTPStatus: 200}, nil
	}))
	res, err := p.ImportArchive(gzPath)
	require.NoError(t, err)
	require.Equal(t, 40, len(res.Events))
	require.False(t, res.Partial, "小归档必须一轮读完（Partial=false）")
	require.Len(t, batches, 1, "小归档应当单批（与修复前一致）")
	require.True(t, archiveImported(t, p, gzPath), "完整导入的小归档必须被标为已导入")
	require.Empty(t, p.Ledger().Get(p.Key()).PauseReason)
}
