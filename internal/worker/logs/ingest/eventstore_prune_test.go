package ingest

// 事件段回收（eventstore.Prune）接线的回归用例。
//
// 覆盖三条红线：
//  1. 水位判据正确：水位内的段被回收、水位之上的段一条都不动；
//  2. 配置关闭时**完全不** Prune（强语义：连源都不枚举）；
//  3. Prune 之后三处「段行数只增不减」的缓存/备忘仍然行为正确（conflict 二的落点）。
//
// 另有一条崩溃恢复用例：Prune 的 PRUNE.json 前滚由 eventstore 包自己的用例覆盖
// （TestPrune* / TestRecover*），本文件只验证**接入后**调用路径不破坏它。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// newPruneTestManager 建一个只关心段存储的最小 Manager（与 eventstore_coverage_test.go 同风格）。
func newPruneTestManager(t *testing.T) *Manager {
	t.Helper()
	m := newCoverageTestManager(t)
	// 把段大小上限压到很小：否则单段要攒到默认上限才封印，夹具造不出「多个已封印段」。
	// 这是 eventstore 提供的测试入口（SetMaxSegmentBytes），不改变生产语义。
	m.events.SetMaxSegmentBytes(2 << 10)
	// 默认关闭：与生产零配置形态一致。
	require.False(t, m.EventStorePrune().Enabled)
	return m
}

// appendInBatches 分批追加：eventstore 只在**每次 Append 结束时**检查段字节数并封印，
// 一次写 4000 条只会得到 1 段。分段追加才能稳定产出多个已封印段。
func appendInBatches(t *testing.T, m *Manager, key string, total, batch int) {
	t.Helper()
	for i := 0; i < total; i += batch {
		n := batch
		if i+n > total {
			n = total - i
		}
		require.NoError(t, m.events.Append(key, makePruneEvents(i, n)))
	}
}

// sealedPruneKey 是该夹具固定使用的源键。
const sealedPruneKey = "src/g1"

// makePruneEvents 造 n 条 record 区间为 [i*10, i*10+9] 的事件（record_end 递增）。
func makePruneEvents(startIdx, n int) []logtypes.Event {
	events := make([]logtypes.Event, 0, n)
	for i := startIdx; i < startIdx+n; i++ {
		events = append(events, logtypes.Event{
			EventID:       "eid-" + itoaTest(i),
			Source:        logtypes.SourceIdentity{LogSourceID: "src", SourceGeneration: "g1", ParserVersion: "v1"},
			Record:        logtypes.RecordRange{Start: uint64(i * 10), End: uint64(i*10 + 9)},
			EventTimeUTC:  "2026-01-01T00:00:00Z",
			IngestTimeUTC: "2026-01-01T00:00:00Z",
			Level:         "INFO",
			Stream:        "stdout",
			Message:       "msg-" + itoaTest(i),
			CanonicalHash: "hash-" + itoaTest(i),
		})
	}
	return events
}

// itoaTest 是极小的十进制转换（避免为一个断言引入 fmt 依赖链）。
func itoaTest(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// timeAtLocal 在**本地时区**构造时刻：nextEventStorePruneAt 用的是 now.Location()，
// 用 UTC 构造会让「本地整点」与断言整点错位（CI 在 UTC+8 之外跑会红）。
func timeAtLocal(t *testing.T, year, month, day, hour, minute, sec int) time.Time {
	t.Helper()
	return time.Date(year, time.Month(month), day, hour, minute, sec, 0, time.Local)
}

// segmentsOf 返回该源当前段清单（用于断言「删了几段」）。
func segmentsOf(t *testing.T, m *Manager, key string) []eventstore.SegmentMeta {
	t.Helper()
	return m.events.Segments(key)
}

// TestEventStorePruneWatermarkFrom 钉住水位判据的每一条安全前提。
//
// 这是本功能的核心：水位给错 = 数据被删且不可恢复，因此每个「应该返回 0」的分支都必须有
// 独立断言，不能只测「正常路径能算出水位」。
func TestEventStorePruneWatermarkFrom(t *testing.T) {
	segs := []eventstore.SegmentMeta{
		{Seq: 1, MinEnd: 10, MaxEnd: 100, Sealed: true},
		{Seq: 2, MinEnd: 110, MaxEnd: 200, Sealed: true},
		{Seq: 3, MinEnd: 210, MaxEnd: 300, Sealed: false},
	}
	runsFull := []ledger.PositionRange{{From: 1, To: 200}}

	t.Run("Reclaim 为 0 时水位必须为 0（WAL 还没释放任何东西）", func(t *testing.T) {
		w, _, _ := eventStorePruneWatermarkFrom(0, runsFull, segs)
		require.Zero(t, w)
	})

	t.Run("段数不足 2 时水位必须为 0（Prune 要保留最后一段）", func(t *testing.T) {
		w, _, _ := eventStorePruneWatermarkFrom(1000, runsFull, segs[:1])
		require.Zero(t, w)
		w, _, _ = eventStorePruneWatermarkFrom(1000, runsFull, nil)
		require.Zero(t, w)
	})

	t.Run("锚点不在任何校验覆盖区间内时水位必须为 0", func(t *testing.T) {
		// 覆盖从 50 起，而段内最早事件的 record_end 是 10 ⇒ 10..49 这段没有校验证据。
		runs := []ledger.PositionRange{{From: 50, To: 200}}
		w, _, _ := eventStorePruneWatermarkFrom(1000, runs, segs)
		require.Zero(t, w)
	})

	t.Run("校验覆盖有空洞时只取锚点所在那一段的末端（不跨空洞）", func(t *testing.T) {
		// 段内最小位置 10 落在第一段；第二段从 150 起（与第一段落 100 之间是空洞）。
		runs := []ledger.PositionRange{{From: 1, To: 100}, {From: 150, To: 400}}
		w, _, verifiedEnd := eventStorePruneWatermarkFrom(1000, runs, segs)
		require.Equal(t, uint64(100), w)
		require.Equal(t, uint64(100), verifiedEnd)
	})

	t.Run("取 Reclaim 与校验覆盖末端的更小者", func(t *testing.T) {
		// 校验覆盖到 200，Reclaim 只到 120 ⇒ 水位 120。
		w, reclaim, verifiedEnd := eventStorePruneWatermarkFrom(120, runsFull, segs)
		require.Equal(t, uint64(120), w)
		require.Equal(t, uint64(120), reclaim)
		require.Equal(t, uint64(200), verifiedEnd)

		// 校验只覆盖到 200，Reclaim 到 1000 ⇒ 水位 200。
		w, _, _ = eventStorePruneWatermarkFrom(1000, runsFull, segs)
		require.Equal(t, uint64(200), w)
	})

	t.Run("VerifiedRuns 有界保留丢的是最旧段：锚点段消失即退化为 0（安全方向）", func(t *testing.T) {
		// 模拟 maxVerifiedRunsPerSource 裁剪后只剩后半段：锚点 10 不再被覆盖。
		runs := []ledger.PositionRange{{From: 150, To: 400}}
		w, _, _ := eventStorePruneWatermarkFrom(1000, runs, segs)
		require.Zero(t, w, "丢弃最旧区间只能让水位变小，绝不允许抬高")
	})
}

// TestEventStorePruneDisabledDoesNothing 钉住「配置关闭时完全不 Prune」。
func TestEventStorePruneDisabledDoesNothing(t *testing.T) {
	m := newPruneTestManager(t)
	const key = sealedPruneKey
	require.NoError(t, m.events.Append(key, makePruneEvents(0, 60)))
	before := segmentsOf(t, m, key)
	require.NotEmpty(t, before)
	beforeRows, err := m.events.Count(key)
	require.NoError(t, err)

	// 即便把其它条件都摆成「可回收」，开关关闭也必须一个段都不删。
	m.mu.Lock()
	m.state.Sources[key] = persistedSource{VerifiedRuns: []ledger.PositionRange{{From: 1, To: 1 << 40}}}
	m.mu.Unlock()

	res, err := m.PruneEventStoreSegments()
	require.NoError(t, err)
	require.True(t, res.Disabled, "关闭时必须显式标记本轮什么都没做")
	require.Zero(t, res.ReclaimedSegments)
	require.Empty(t, res.Sources, "关闭时连源都不枚举")

	require.Equal(t, before, segmentsOf(t, m, key))
	afterRows, err := m.events.Count(key)
	require.NoError(t, err)
	require.Equal(t, beforeRows, afterRows)
}

// TestEventStorePruneReclaimsWatermarkPrefix 验证「水位内的段被回收、水位之上的保留」。
func TestEventStorePruneReclaimsWatermarkPrefix(t *testing.T) {
	m := newPruneTestManager(t)
	const key = sealedPruneKey
	// 分段追加：每批写满即封印，从而稳定产出多个已封印段。
	appendInBatches(t, m, key, 4000, 20)
	segs := segmentsOf(t, m, key)
	require.Greater(t, len(segs), 2, "夹具需要至少 3 段才能验证「回收前缀、保留其余」")
	require.True(t, segs[0].Sealed, "夹具前提：头部段必须已封印（Prune 只回收已封印段）")

	// 水位取「第一个已封印段的末端」：只有它（以及更早的已封印段）可以被回收。
	watermark := segs[0].MaxEnd
	require.NotZero(t, watermark)

	res, err := m.events.Prune(key, watermark)
	require.NoError(t, err)
	require.GreaterOrEqual(t, res.ReclaimedSegments, 1)
	after := segmentsOf(t, m, key)
	require.Less(t, len(after), len(segs), "水位内的段必须真的被回收")
	// 保留段里至少还有一段的 MaxEnd 超过水位（水位之上的一条都没删）。
	require.NotEmpty(t, after, "至少保留最后一段")
	survivedAbove := false
	for _, s := range after {
		if s.MaxEnd > watermark {
			survivedAbove = true
		}
	}
	require.True(t, survivedAbove, "水位之上的段必须保留（否则就是多删）")

	// 漏删/误删的直接核对：被回收段数必须恰好等于「MaxEnd <= watermark 的已封印前缀」长度。
	wantPrefix := 0
	for _, s := range segs {
		if s.Sealed && s.MaxEnd <= watermark {
			wantPrefix++
		} else {
			break
		}
	}
	require.Equal(t, wantPrefix, res.ReclaimedSegments, "回收段数必须恰好等于水位内的已封印前缀")
}

// TestEventStorePruneInvalidatesRowCaches 是 conflict 二的**主断言**：
// Prune 之后三处「段行数只增不减」的缓存/备忘必须立刻失效，而不是靠「行数恰好对不上」侥幸。
func TestEventStorePruneInvalidatesRowCaches(t *testing.T) {
	m := newPruneTestManager(t)
	const key = sealedPruneKey
	appendInBatches(t, m, key, 4000, 20)

	// 1) 建立 cachedStoredRows 基线。
	rowsBefore, ok := m.cachedStoredRows(key)
	require.True(t, ok)
	require.Positive(t, rowsBefore)

	// 2) 建立 storedCoverageMemo 备忘（同一份集合被问第二次时应命中）。
	events := makePruneEvents(0, 10)
	_, _, err := m.missingFromEventStore(key, events)
	require.NoError(t, err)
	digest := eventSetDigest(events)
	m.rememberStoredCoverage(key, digest)
	require.True(t, m.hasStoredCoverageMemo(key))
	covered, err := m.storedCoverage(key, digest)
	require.NoError(t, err)
	require.True(t, covered, "前提：备忘此刻应当命中")

	epochBefore := m.eventsStoredEpoch[key]

	// 3) 真的回收一段（直接走 eventstore，再调接线层的缓存失效 —— 与生产路径同序）。
	segs := segmentsOf(t, m, key)
	require.Greater(t, len(segs), 2)
	pruned, err := m.events.Prune(key, segs[0].MaxEnd)
	require.NoError(t, err)
	require.GreaterOrEqual(t, pruned.ReclaimedSegments, 1)
	m.invalidateEventStoreRowCaches(key)

	// 4) 三处缓存必须已失效。
	require.Greater(t, m.eventsStoredEpoch[key], epochBefore, "回收后必须推进段版本号")
	require.False(t, m.hasStoredCoverageMemo(key), "回收后必须丢弃覆盖备忘")
	_, cached := m.eventsStoredRows[key]
	require.False(t, cached, "回收后必须丢弃行数基线")

	// 5) 行为正确：重建基线必须等于新的（更小的）物理行数。
	rowsAfter, err := m.events.Count(key)
	require.NoError(t, err)
	require.Less(t, rowsAfter, rowsBefore, "回收后物理行数必须变小（前提）")
	rebuilt, ok := m.cachedStoredRows(key)
	require.True(t, ok)
	require.Equal(t, rowsAfter, rebuilt, "重建后的基线必须等于真实行数，不能沿用回收前的值")
}

// TestMissingFromEventStoreAfterPrune 验证 Prune 之后 missingFromEventStore 仍按**身份**
// 正确判定「缺 / 不缺」：被回收的身份会被判为「缺」（调用方据此重新追加，不是静默丢数据）。
func TestMissingFromEventStoreAfterPrune(t *testing.T) {
	m := newPruneTestManager(t)
	const key = sealedPruneKey
	appendInBatches(t, m, key, 4000, 20)

	segs := segmentsOf(t, m, key)
	require.Greater(t, len(segs), 2)
	pruned, err := m.events.Prune(key, segs[0].MaxEnd)
	require.NoError(t, err)
	require.GreaterOrEqual(t, pruned.ReclaimedSegments, 1)
	m.invalidateEventStoreRowCaches(key)

	// 水位之内的事件（第一批）现在已不在段里 ⇒ 必须被判为「缺」。
	head := makePruneEvents(0, 5)
	missing, conflicts, err := m.missingFromEventStore(key, head)
	require.NoError(t, err)
	require.Empty(t, conflicts)
	require.Len(t, missing, len(head), "被回收的身份必须被判为缺失（重新追加，而不是静默丢数据）")

	// 仍在段里的事件 ⇒ 不得被判缺（否则每轮重复追加，段无界增长）。
	tail := makePruneEvents(3990, 10)
	missingTail, _, err := m.missingFromEventStore(key, tail)
	require.NoError(t, err)
	require.Empty(t, missingTail, "仍在段中的身份不得被判缺")
}

// TestStoredCoverageAfterPrune 验证覆盖备忘在 Prune 之后不会把「已不存在的身份」报成已覆盖。
func TestStoredCoverageAfterPrune(t *testing.T) {
	m := newPruneTestManager(t)
	const key = sealedPruneKey
	appendInBatches(t, m, key, 4000, 20)

	head := makePruneEvents(0, 5)
	digest := eventSetDigest(head)
	m.rememberStoredCoverage(key, digest)
	covered, err := m.storedCoverage(key, digest)
	require.NoError(t, err)
	require.True(t, covered, "前提：回收前该集合确实已被段覆盖")

	segs := segmentsOf(t, m, key)
	require.Greater(t, len(segs), 2)
	_, err = m.events.Prune(key, segs[0].MaxEnd)
	require.NoError(t, err)
	m.invalidateEventStoreRowCaches(key)

	// 回收后备忘必须失效：不能再用「旧结论」回答「这段内容还是不是那段」。
	covered, err = m.storedCoverage(key, digest)
	require.NoError(t, err)
	require.False(t, covered, "回收后覆盖备忘必须失效")
	require.False(t, m.hasStoredCoverageMemo(key))
}

// TestEventStorePruneConfigNormalized 钉住配置口径：默认关闭、越界小时回退默认。
func TestEventStorePruneConfigNormalized(t *testing.T) {
	def := DefaultEventStorePruneConfig()
	require.False(t, def.Enabled, "删除类能力必须默认关闭")
	require.Equal(t, DefaultEventStorePruneSweepAtHour, def.SweepAtHour)

	out := EventStorePruneConfig{Enabled: true, SweepAtHour: 99}.Normalized()
	require.True(t, out.Enabled)
	require.Equal(t, DefaultEventStorePruneSweepAtHour, out.SweepAtHour, "越界小时回退默认，不得变高频")

	out = EventStorePruneConfig{Enabled: true, SweepAtHour: -3}.Normalized()
	require.Equal(t, DefaultEventStorePruneSweepAtHour, out.SweepAtHour)

	require.Equal(t, EventStorePruneConfig{Enabled: false, SweepAtHour: 5},
		EventStorePruneConfig{Enabled: false, SweepAtHour: 5}.Normalized())

	// nil 配置（键未配）走默认 = 关闭。
	require.False(t, eventStorePruneConfigOf(nil).Enabled)
}

// TestPruneEventStoreSegmentsEndToEnd 是**接线层**的端到端用例：用真实 pipeline 把 Reclaim
// 推到某个位置，再走 Manager 的生产入口（PruneEventStoreSegments）验证：
//   - 开启时：水位内的段被回收；
//   - 关闭时：同一形态一个段都不删。
//
// 这一条比纯函数用例更重要：它证明「水位真的能从账本读出来」且「开关真的挡在前面」。
func TestPruneEventStoreSegmentsEndToEnd(t *testing.T) {
	newFixture := func(t *testing.T) (*Manager, string) {
		t.Helper()
		m := newPruneTestManager(t)
		path := filepath.Join(t.TempDir(), "source.log")
		require.NoError(t, os.WriteFile(path, nil, 0o600))
		p, err := pipeline.New(pipeline.Options{
			Mode: pipeline.ModeFilePrimary, Path: path,
			LogSourceID: "prune-e2e", SourceGeneration: "g1",
			Delivery: pipeline.FuncHook(func([]logtypes.Event, bool) (pipeline.DeliveryResult, error) {
				return pipeline.DeliveryResult{HTTPStatus: 200}, nil
			}),
			ReclaimProof: func([]logtypes.Event) error { return nil },
		})
		require.NoError(t, err)
		key := "prune-e2e/g1"
		m.mu.Lock()
		m.pipes[key] = p
		m.mu.Unlock()
		appendInBatches(t, m, key, 4000, 20)
		return m, key
	}

	t.Run("开启时按水位回收", func(t *testing.T) {
		m, key := newFixture(t)
		segs := segmentsOf(t, m, key)
		require.Greater(t, len(segs), 2)
		watermark := segs[0].MaxEnd

		// 让账本的 Reclaim 推到 watermark（水位两输入的另一个是逐字段校验覆盖）。
		p := m.pipes[key]
		require.NoError(t, p.WAL().Append(makePruneEvents(0, 5)...))
		require.NoError(t, p.WAL().Commit())
		// 2026-10-29：回收只看「REQUEST_DONE 连续覆盖 + max(Read,Durable)」，不再需要恢复段。
		// 这里把认证覆盖与读入量都推到 watermark，让账本的前置条件成立
		// （本用例只验证裁剪接线与水位）。
		require.NoError(t, p.Ledger().AdvanceRead(p.Key(), watermark))
		require.NoError(t, p.Ledger().RecordDelivery(p.Key(), 0, watermark, logtypes.DeliveryRequestDone))
		_, reclaimErr := p.WAL().TryReclaimThrough(watermark)
		require.NoError(t, reclaimErr)
		positions, ok := p.Positions()
		require.True(t, ok)
		require.GreaterOrEqual(t, positions.Reclaim, watermark,
			"前提：Reclaim 必须真的被推到水位（否则本用例退化成无效）")

		// 逐字段校验覆盖：覆盖到 watermark。
		m.mu.Lock()
		m.state.Sources[key] = persistedSource{VerifiedRuns: []ledger.PositionRange{{From: 0, To: watermark}}}
		m.mu.Unlock()
		m.SetEventStorePrune(EventStorePruneConfig{Enabled: true, SweepAtHour: 3})

		res, err := m.PruneEventStoreSegments()
		require.NoError(t, err)
		require.False(t, res.Disabled)
		require.Zero(t, res.Failures)
		require.GreaterOrEqual(t, res.ReclaimedSegments, 1, "水位内的段必须被回收")
		require.Less(t, len(segmentsOf(t, m, key)), len(segs))
	})

	t.Run("关闭时同一形态一个段都不删", func(t *testing.T) {
		m, key := newFixture(t)
		segs := segmentsOf(t, m, key)
		watermark := segs[0].MaxEnd
		p := m.pipes[key]
		require.NoError(t, p.WAL().Append(makePruneEvents(0, 5)...))
		require.NoError(t, p.WAL().Commit())
		// 2026-10-29：同上——用真实投递证据（而非已删的恢复段）建立回收前置。
		require.NoError(t, p.Ledger().AdvanceRead(p.Key(), watermark))
		require.NoError(t, p.Ledger().RecordDelivery(p.Key(), 0, watermark, logtypes.DeliveryRequestDone))
		_, reclaimErr := p.WAL().TryReclaimThrough(watermark)
		require.NoError(t, reclaimErr)
		m.mu.Lock()
		m.state.Sources[key] = persistedSource{VerifiedRuns: []ledger.PositionRange{{From: 0, To: watermark}}}
		m.mu.Unlock()
		// 默认关闭（不调用 SetEventStorePrune）。
		res, err := m.PruneEventStoreSegments()
		require.NoError(t, err)
		require.True(t, res.Disabled)
		require.Equal(t, segs, segmentsOf(t, m, key), "关闭时段的段清单必须逐字节不变")
	})
}

// TestNextEventStorePruneAt 钉住每日调度算法（下一轮严格在 now 之后、且是本地的整点）。
func TestNextEventStorePruneAt(t *testing.T) {
	base := timeAtLocal(t, 2026, 3, 1, 2, 30, 0)
	next := nextEventStorePruneAt(base, 3)
	require.Equal(t, 3, next.Hour())
	require.Equal(t, 1, next.Day())
	require.True(t, next.After(base))

	// 已过 3 点 ⇒ 推到次日 3 点。
	base = timeAtLocal(t, 2026, 3, 1, 3, 0, 0)
	next = nextEventStorePruneAt(base, 3)
	require.Equal(t, 2, next.Day(), "恰好等于触发时刻时必须推到次日（严格 After）")

	// 越界小时按默认处理。
	require.Equal(t, DefaultEventStorePruneSweepAtHour, nextEventStorePruneAt(base, 42).Hour())
}
