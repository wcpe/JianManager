package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件钉住「缺口核对的**待办规模不得随运行时长增长**」这条契约
// （2026-11-02 真机缺陷，也是本次修复的**真正根因**）。
//
// 事故形态（60 源 × 15 行/s 长稳实测，`landedCount`）：
//
//	06:26:29  361 → 06:27:58  1694 → 06:29:54  3428 → 06:32:46  5420 → 6452
//
// 线性增长、永不收敛。而一次探测的 selector 被 12KB 上限切成 ~180 条/片 ⇒
// 5420 条 = 30+ 片 ⇒ 6s 预算必然超时 ⇒ 缺口永不消解 ⇒ WAL 不回收 ⇒ 积压丢弃
// ⇒ 更多缺口（正反馈）。
//
// 根因不在「怎么查」（预算/并发/重试，前四层改的都是这个），而在「查什么范围」：
// 异步 worker 跑完把整批事件丢弃，于是同源下次登记时 `mergeGapResolveEvents`
// 把新事件与**旧待办**做并集 —— 待办规模由「运行时长」定界，而不是由「真实未决量」定界。
//
// 修复后的语义：核对完**只把「仍未确认可见」的事件留在待办**；已确认可见的
// 由 `recordVerifiedRuns` 持久化成区间凭据，不必再核。

// scopeFixture 造一个「事件已在 VL 里可见」的单源环境。
func scopeFixture(t *testing.T, events []logtypes.Event, visible bool) (*Manager, SourceConfig) {
	t.Helper()
	source, root := gapVisibility_TestSource(t)
	day := source.UTCDay

	client, fixture, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	publishGapVisibilityDay(t, cat, source, events, day)

	if visible {
		fixture.mu.Lock()
		for _, ev := range events {
			fixture.rows = append(fixture.rows, reconcileVLRow{
				ProjectionGeneration: "projection-1",
				EventID:              ev.EventID,
				EventTimeUTC:         ev.EventTimeUTC,
				LogSourceID:          source.LogSourceID,
				SourceGeneration:     source.SourceGeneration,
				RecordEnd:            ev.Record.End,
				Message:              ev.Message,
				Level:                ev.Level,
				Stream:               ev.Stream,
				CanonicalHash:        ev.CanonicalHash,
			})
		}
		fixture.mu.Unlock()
	}

	// 必须走 New() 才能拿到 pipeline（resolveGapsLandedByDeliveryScope 无 pipe 时整批返回）。
	m, err := New(Options{
		Root: root, VL: client, Catalog: cat, Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	m.verifyChunkConcurrency = 4
	return m, source
}

// TestGapResolveScopeDoesNotGrowWhenEverythingVisible 是**核心验收**：
// 事件全部可见时，核对完的返回值必须为空 —— 即「这批不需要再核」。
//
// 若返回非空，说明待办会不断累积 ⇒ 待办规模随运行时长线性增长 ⇒ 必红。
//
// 转红方式（实测）：把 resolveGapsLandedByDeliveryScope 的返回值改成 `return events`
// （等价于修复前「整批丢弃再整批重来」的语义）⇒ 本用例必红。
func TestGapResolveScopeDoesNotGrowWhenEverythingVisible(t *testing.T) {
	const total = 50
	spans := make([][2]uint64, 0, total)
	for i := 0; i < total; i++ {
		spans = append(spans, [2]uint64{uint64(i * 10), uint64(i*10 + 5)})
	}
	proto := gapVisibilityEvents(SourceConfig{
		LogSourceID: "inst:gapvis/file", SourceGeneration: "g1",
	}, "2026-11-01", spans, "scope")
	m, source := scopeFixture(t, proto, true)

	unresolved := m.resolveGapsLandedByDeliveryScope(context.Background(), source, proto)
	require.Empty(t, unresolved,
		"全部可见时必须返回空（这批已由 recordVerifiedRuns 证明，不该留在待办里重核）；"+
			"返回非空会让待办随运行时长线性增长——这正是 361→6452 的成因")
}

// TestGapResolveScopeKeepsOnlyInvisible 钉住反方向：
// 部分可见时，**只保留不可见的那部分**（可见的不再重核、不可见的一条都不能丢）。
//
// 转红方式（实测）：
//   - 全部返回（含可见的）⇒ 断言 `len == 期望` 转红；
//   - 全部丢弃（返回空）⇒ 同样转红（那些真没落库的欠账被遗忘了）。
func TestGapResolveScopeKeepsOnlyInvisible(t *testing.T) {
	const total = 40
	const visibleCount = 25
	spans := make([][2]uint64, 0, total)
	for i := 0; i < total; i++ {
		spans = append(spans, [2]uint64{uint64(i * 10), uint64(i*10 + 5)})
	}
	proto := gapVisibilityEvents(SourceConfig{
		LogSourceID: "inst:gapvis/file", SourceGeneration: "g1",
	}, "2026-11-01", spans, "scope")
	m, source := scopeFixture(t, proto[:visibleCount], true)

	unresolved := m.resolveGapsLandedByDeliveryScope(context.Background(), source, proto)
	require.Len(t, unresolved, total-visibleCount,
		"只应保留本次**未确认可见**的事件（可见的由 VerifiedRuns 证明，不必再核；不可见的一条都不能丢）")

	// 且必须**恰好**是后面那批（不是随便 15 条）。
	visibleSet := make(map[string]struct{}, visibleCount)
	for _, ev := range proto[:visibleCount] {
		visibleSet[ev.EventID] = struct{}{}
	}
	for _, ev := range unresolved {
		_, isVisible := visibleSet[ev.EventID]
		require.False(t, isVisible, "已确认可见的事件不得留在待办里（%s）", ev.EventID)
	}
}

// TestGapResolveScopeKeepsAllWhenNoneVisible 钉住「一条都查不到时不许遗忘」。
//
// 转红方式：把 `len(runs)==0` 分支改成 `return nil` ⇒ 本用例必红（这正是最危险的
// 那种「静默抹平欠账」）。
func TestGapResolveScopeKeepsAllWhenNoneVisible(t *testing.T) {
	const total = 20
	spans := make([][2]uint64, 0, total)
	for i := 0; i < total; i++ {
		spans = append(spans, [2]uint64{uint64(i * 10), uint64(i*10 + 5)})
	}
	proto := gapVisibilityEvents(SourceConfig{
		LogSourceID: "inst:gapvis/file", SourceGeneration: "g1",
	}, "2026-11-01", spans, "scope")
	m, source := scopeFixture(t, proto, false) // 一条都不写进 VL

	unresolved := m.resolveGapsLandedByDeliveryScope(context.Background(), source, proto)
	require.Len(t, unresolved, total,
		"一条都查不到时必须原样全部留在待办里重试——遗忘它们等于静默抹平欠账")
}

// 确保 time 导入被使用（退避常量在断言里用到）。
var _ = gapResolveRetryBackoff
var _ = time.Second
