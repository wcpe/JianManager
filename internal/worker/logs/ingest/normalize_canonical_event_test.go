package ingest

// normalizeCanonicalEvent 幂等性回归（FR-505 后续，2026-11-02）。
//
// ## 缺陷形态（实测取证，不是推理）
//
// 同一物理行在两个地方存在**两种形态**：
//   - **WAL 副本**：采集时存入的 normalize 产物。无时间戳行（Paper 之前的 Bungee 形态、
//     纯文本行、堆栈续行……）的 `EventTimeUTC` 为**空**，hash 由空时间算出；
//   - **段副本**：投递时存的是**已归一化**形态（`normalizeCanonicalEvent` 重算过，
//     `EventTimeUTC` 非空、hash 变了）。
//
// 旧实现的 `normalizeCanonicalEvent` 只在 `EventTimeUTC == ""` 时重算 hash，对非空的直接
// 原样返回（注释写的是"已归一化，不重算"）。所以：
//   - 旧段形态（未归一化的 raw）读回再归一化 ⇒ hash **变了** ⇒ 不幂等；
//   - 比对两个副本时，谁先被认作"先到者"决定了胜出 hash，而**双方都自称是同一行**。
//
// 当前不产生可观测冲突，只因为 `canonicalRecoveryEvents` 先迭代段、再迭代 WAL，两条都会被
// 规范化，差异被抹平。那是**偶然的**（依赖两个来源的顺序），不是不变量。
//
// ## 修法（向后兼容，不改任何已发布数据语义）
//
// 把函数抬成真正的幂等函数（`f(f(x)) == f(x)`）：
//   - 已归一化形态（EventTimeUTC 非空 **且** hash 自洽）⇒ 原样返回（零开销，绝大多数情况）；
//   - 旧段形态（EventTimeUTC 非空但 hash 不自洽）⇒ 以 EventTimeUTC 重算 hash；
//   - 无时间戳行（EventTimeUTC 空）⇒ 仍靠 IngestTimeUTC 反推（旧段兼容的**唯一**途径，保留）。
//
// 关键：对任何 hash 本来就自洽的事件（= 全部正常路径产出的事件），重算值**逐字相同** ⇒
// 已发布数据的语义一字未变。
//
// ## 变异验证（已实测）
//
// 把实现改回旧版（`if event.EventTimeUTC != "" { return event, nil }`）⇒
// `TestNormalizeCanonicalEventIsIdempotent` 的 legacy 子用例与
// `TestNormalizeCanonicalEventSegmentRoundTripIsStable` 同时转红。

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// normalizeProbeIdentity 是本文件夹具用的源身份（与任何生产源无关）。
func normalizeProbeIdentity() logtypes.SourceIdentity {
	return logtypes.SourceIdentity{LogSourceID: "node:normalize-probe", SourceGeneration: "g1", ParserVersion: "acquire-v1"}
}

// TestNormalizeCanonicalEventIsIdempotent 是**幂等性本身**的验收：
// 对每一种真实存在的形态，`f(f(x)) == f(x)` 必须成立，且结果自洽（hash 与字段一致）。
func TestNormalizeCanonicalEventIsIdempotent(t *testing.T) {
	id := normalizeProbeIdentity()

	// 形态 1：无时间戳行（原始形态：EventTimeUTC 空、hash 由空时间算出）。
	noTime := logtypes.BuildEvent(id, logtypes.RecordRange{Start: 0, End: 10},
		"", "2026-09-20T12:00:01.000000001Z", "INFO", "stdout", "bungee style line")
	require.Empty(t, noTime.EventTimeUTC, "夹具：无时间戳行归一化前 EventTimeUTC 必须为空")

	// 形态 2：正常带时间的行（hash 本就自洽）。
	withTime := logtypes.BuildEvent(id, logtypes.RecordRange{Start: 10, End: 20},
		"2026-09-20T12:00:00Z", "2026-09-20T12:00:01Z", "INFO", "stdout", "paper style line")

	// 形态 3：旧段形态 —— EventTimeUTC 非空但 hash **不自洽**（历史段里存的就是这样）。
	legacyIncoherent := withTime
	legacyIncoherent.CanonicalHash = logtypes.CanonicalContentHash("", legacyIncoherent.Level, legacyIncoherent.Stream, legacyIncoherent.Message)
	require.NotEqual(t, logtypes.CanonicalContentHash(legacyIncoherent.EventTimeUTC,
		legacyIncoherent.Level, legacyIncoherent.Stream, legacyIncoherent.Message), legacyIncoherent.CanonicalHash,
		"夹具：legacyIncoherent 的 hash 必须与字段不自洽")

	cases := []struct {
		name  string
		input logtypes.Event
	}{
		{"no-timestamp (raw form)", noTime},
		{"with-timestamp (already canonical)", withTime},
		{"legacy segment (incoherent hash)", legacyIncoherent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := normalizeCanonicalEvent(tc.input)
			require.NoError(t, err)
			second, err := normalizeCanonicalEvent(first)
			require.NoError(t, err)
			third, err := normalizeCanonicalEvent(second)
			require.NoError(t, err)

			require.NotEmpty(t, first.EventTimeUTC, "归一化后事件时间不得留空")
			require.Equal(t, first.CanonicalHash, second.CanonicalHash,
				"f(f(x)) 必须等于 f(x)（旧实现在 legacy 形态下不成立⇒转红）")
			require.Equal(t, second.CanonicalHash, third.CanonicalHash, "反复归一化必须稳定")
			require.Equal(t, first.EventTimeUTC, second.EventTimeUTC, "事件时间不得随多次归一化漂移")

			// 自洽：归一化后的 hash 必须就是由它自己的字段算出的值（否则"规范形态"无从谈起）。
			require.Equal(t, logtypes.CanonicalContentHash(first.EventTimeUTC, first.Level, first.Stream, first.Message),
				first.CanonicalHash, "归一化后 hash 必须与字段自洽")
		})
	}
}

// TestNormalizeCanonicalEventBackfillsLegacyFromIngestTime 锤住**向后兼容**：
// 旧段里 EventTimeUTC 为空的形态，必须仍能靠 IngestTimeUTC 反推（否则旧段无法定日/定身份）。
func TestNormalizeCanonicalEventBackfillsLegacyFromIngestTime(t *testing.T) {
	id := normalizeProbeIdentity()
	legacy := logtypes.BuildEvent(id, logtypes.RecordRange{Start: 0, End: 10},
		"", "2026-09-20T12:00:01Z", "WARN", "stderr", "legacy no-time line")

	got, err := normalizeCanonicalEvent(legacy)
	require.NoError(t, err)
	require.Equal(t, "2026-09-20T12:00:01Z", got.EventTimeUTC,
		"旧段兼容：必须用 IngestTimeUTC 反推出事件时间（不得报错、不得留空）")
	require.NotEqual(t, legacy.CanonicalHash, got.CanonicalHash,
		"旧段的 hash 是按空时间算的 ⇒ 反推后必须重算（这正是归一化的目的）")
	require.Equal(t, logtypes.CanonicalContentHash(got.EventTimeUTC, got.Level, got.Stream, got.Message), got.CanonicalHash)
}

// TestNormalizeCanonicalEventRejectsMissingBothTimes 守住错误面：两个时间都没有时必须报错
// （不能静默编造一个时间——那会让它落进错误的 UTC 日、并得到无法复现的 hash）。
func TestNormalizeCanonicalEventRejectsMissingBothTimes(t *testing.T) {
	id := normalizeProbeIdentity()
	event := logtypes.BuildEvent(id, logtypes.RecordRange{Start: 0, End: 10}, "", "", "INFO", "stdout", "no time at all")
	_, err := normalizeCanonicalEvent(event)
	require.Error(t, err, "既无事件时间又无采集时间时必须报错（不得默认成零值时间）")
}

// TestNormalizeCanonicalEventSegmentRoundTripIsStable 是**端到端**的幂等验收：
// 同一个事件写入段存储、读回、再归一化、再写回，CanonicalHash 必须逐轮稳定不变，
// 且段里**不得因为重写而多出行**。
//
// 这也是旧实现真正会出问题的地方：段里存的（已归一化）与 WAL 里的（未归一化）是
// **两个不同 hash 的同一行**，谁先被当成"先到者"决定了胜出哈希。
func TestNormalizeCanonicalEventSegmentRoundTripIsStable(t *testing.T) {
	m := newNormalizeSegmentManager(t)
	key := "node:normalize-roundtrip/g1"
	m.mu.Lock()
	src := m.sources[key]
	m.mu.Unlock()
	id := logtypes.SourceIdentity{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "acquire-v1"}

	// 无时间戳行：最容易被两种形态拉开的那一类。
	raw := logtypes.BuildEvent(id, logtypes.RecordRange{Start: 0, End: 20},
		"", "2026-09-20T12:00:01Z", "INFO", "stdout", "no timestamp line")

	// 第 1 轮：写入（走生产同一条 appendEvents 入口）。
	require.NoError(t, m.appendEvents(key, []logtypes.Event{raw}))
	first := iterateEvents(t, m, key)
	require.Len(t, first, 1)

	// 第 2 轮：把读回的（已归一化）形态再喂一次 appendEvents。
	// 这是真实发生的形态：恢复/重建路径的输入来自段读回，而不是原始 WAL。
	require.NoError(t, m.appendEvents(key, first))
	second := iterateEvents(t, m, key)
	require.Len(t, second, 1, "读回再写不得在段里多出一行（同身份同哈希 ⇒ 不得重复追加）")
	require.Equal(t, first[0].CanonicalHash, second[0].CanonicalHash, "读回再写必须幂等")

	// 第 3 轮：再跑一次，仍然稳定（验证不是一次性巧合）。
	require.NoError(t, m.appendEvents(key, second))
	third := iterateEvents(t, m, key)
	require.Len(t, third, 1)
	require.Equal(t, first[0].CanonicalHash, third[0].CanonicalHash)
}

// TestNormalizeCanonicalEventPublishedHashUnchanged 是**「不得改变已发布数据语义」**的验收：
// 对任何本来就自洽的事件（= 正常路径产出），归一化必须是一个**逐字恒等**的变换。
func TestNormalizeCanonicalEventPublishedHashUnchanged(t *testing.T) {
	id := normalizeProbeIdentity()
	cases := []logtypes.Event{
		logtypes.BuildEvent(id, logtypes.RecordRange{Start: 0, End: 10}, "2026-09-20T00:00:00Z", "2026-09-20T00:00:01Z", "INFO", "stdout", "a"),
		logtypes.BuildEvent(id, logtypes.RecordRange{Start: 10, End: 20}, "2026-09-20T00:00:02+08:00", "2026-09-20T00:00:03Z", "ERROR", "stderr", "多行\n堆栈\n  at Foo.java:1"),
	}
	for _, in := range cases {
		out, err := normalizeCanonicalEvent(in)
		require.NoError(t, err)
		require.Equal(t, in.CanonicalHash, out.CanonicalHash,
			"本来就自洽的 hash 必须逐字不变（否则等于改写了已发布数据的身份）")
		require.Equal(t, in.EventTimeUTC, out.EventTimeUTC, "事件时间不得被改写")
	}
}

// newNormalizeSegmentManager 建一个**段根唯一且用完即清**的 Manager（只关心段存储）。
//
// 为什么不能用现成的夹具：`eventstore.MemoryRoot` 只用 `filepath.Base(root)` 做 tmpfs 目录名，
// 而 `t.TempDir()` 的 basename 在跨运行之间是稳定的 "001" ⇒ 同名用例会共用同一个 /dev/shm
// 段根（见 docs/CONVENTIONS.md 「日志相关包的测试不得并行运行」）。本用例断言"段里只有 1 行"，
// 一旦读到别人残留的段就会变成假红（实测：残留 5 行）。
// 这里用 `uniqueSegmentRoot` 生成唯一根名，并在建 Manager **前**先将它清一次。
func newNormalizeSegmentManager(t *testing.T) *Manager {
	t.Helper()
	root, _ := freshSegmentRoot(t, "normalize-roundtrip")
	cat := catalog.New(catalog.NewMemJournal())
	src := SourceConfig{
		LogSourceID: "node:normalize-roundtrip", SourceGeneration: "g1",
		Path: filepath.Join(root, "latest.log"), Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:normalize-roundtrip", UTCDay: runtimeTestUTCDay(),
	}
	m, err := New(Options{Root: root, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })
	return m
}

// iterateEvents 读出该源段里的全部事件（按追加序）。
func iterateEvents(t *testing.T, m *Manager, key string) []logtypes.Event {
	t.Helper()
	var out []logtypes.Event
	require.NoError(t, m.events.Iterate(key, func(e logtypes.Event) error {
		out = append(out, e)
		return nil
	}))
	return out
}
