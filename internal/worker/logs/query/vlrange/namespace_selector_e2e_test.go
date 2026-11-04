package vlrange

// 端到端契约：**分区命名空间 → LogsQL 来源谓词**的翻译必须与真机字段形状一致。
//
// ── 为什么单独建这个文件 ──────────────────────────────────────────────────────
//
// 2026-11-05 生产故障：投递链路把数据写进 VictoriaLogs，查询侧一条也查不到，
// 而接口报 `items=0 / exhausted=true / coverage.complete=true`——
// 空结果被粉饰成「完整且无数据」，用户看到的是「这天没日志」。
//
// 根因不是某一处写错，而是**同一个概念在四处各写了一遍**：
//
//	selectorQuery / rangeMatchesEvent / MergeableScope / disjointScopeTerms
//
// 旧夹具把 `StorageNamespace` 与 `LogSourceID` 写成**同一个值**
// （`StorageNamespace: "inst:9", LogSourceID: "inst:9"`），于是
// 「命名空间 == 完整源 id」这个错误前提在测试里恒成立，缺陷永不暴露。
//
// 真机形状从来不是这样：
//
//	Catalog 的 StorageNamespace  = "inst:318"        （target id，授权粒度）
//	VL 行上的 log_source_id      = "inst:318/file"   （完整源 id，含流后缀）
//
// VL 直查实证（inst:318，2026-11-04 单日窗，VictoriaLogs v1.52.0）：
//
//	log_source_id:="inst:318"                                 -> 0 条
//	log_source_id:="inst:318/file"                            -> 615 条
//	(log_source_id:="inst:318" OR log_source_id:"inst:318/"*)  -> 615 条
//
// 本文件钉住「渲染成后者」，并钉住反向边界（前缀不得跨源）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// simplifiedPartitionRange 构造**简化投递路径补建的记录**所产生的 range 形状。
//
// 关键：`Projection` 非 nil（`querySnapshotLocked` 的门禁要求它），但
// `SourceProjections` 与 `CoveredSourceGenerations` 都为空——
// 这正是 `ingest.ensureCatalogPartition`（runtime_source.go:841）的产物：
// 它刻意不填逐源投影（一批数据只写一次、行上没有代次标签）。
//
// 于是 selectorQuery 必然走「回退到 Key.StorageNamespace」那个分支。
func simplifiedPartitionRange(namespace, day string) query.AuthoritativeRange {
	return query.AuthoritativeRange{
		TargetID: namespace + "/" + day,
		Key:      catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day},
		Owner:    catalog.OwnerHot,
		Projection: &catalog.PublishedProjection{
			QueryLocationDirID: "hot-" + namespace,
			QueryGeneration:    1,
		},
	}
}

// TestSelectorRendersNamespaceAsStreamPrefix 钉住「命名空间 → 来源谓词」的渲染形状。
//
// 转红方式（实测）：把 `namespaceSelector` 改回只渲染等值
// `log_source_id:="<ns>"`，本用例立刻失败（缺 `inst:318/` 前缀那一条）。
func TestSelectorRendersNamespaceAsStreamPrefix(t *testing.T) {
	rng := simplifiedPartitionRange("inst:318", "2026-11-04")
	text := selectorQuery(rng, query.RangeQuery{})

	require.Contains(t, text, `log_source_id:="inst:318"`,
		"裸命名空间还要保留：历史上存在 LogSourceID == StorageNamespace 的源（如 node:1 无流后缀）")
	require.Contains(t, text, `log_source_id:"inst:318/"*`,
		"必须带 `/` 前缀分支：VL 行上的 log_source_id 是 `<TargetID>/<stream>`，只写等值恒查不到")

	// 前缀必须以 `/` 收口。不收口会让 `inst:3` 顺带命中 `inst:31` 与 `inst:318`
	// ——那是**跨实例读到别人的日志**，比查不到严重得多。
	require.Contains(t, text, `log_source_id:"inst:318/"*`, "前缀必须写成 `<ns>/`")
	require.NotContains(t, text, `log_source_id:"inst:318"*`,
		"前缀不得裸拼命名空间，必须以 `/` 收口")
}

// TestSelectorNamespaceDoesNotLeakAcrossSources 反向边界：相近名字不得互相命中。
//
// 这条与上一条方向相反，缺了它就防不住「为了让查得到而把前缀放宽」的改法。
func TestSelectorNamespaceDoesNotLeakAcrossSources(t *testing.T) {
	cases := []struct {
		namespace string
		rowSource string
		want      bool
		why       string
	}{
		{"inst:318", "inst:318/file", true, "本实例的日志流：必须命中"},
		{"inst:318", "inst:318", true, "裸命名空间本身：必须命中（历史形态）"},
		{"inst:318", "inst:31/file", false, "`inst:31` 是另一个实例，不得命中"},
		{"inst:318", "inst:3180/file", false, "`inst:3180` 是另一个实例，不得命中"},
		{"inst:3", "inst:31/file", false, "短名不得吃掉长名的前缀"},
		{"inst:318", "inst:318/file/rotated", true, "同一源的更细路径仍属本流"},
	}
	for _, tc := range cases {
		t.Run(tc.namespace+"_vs_"+tc.rowSource, func(t *testing.T) {
			got := (query.SourceScope{Namespace: tc.namespace}).MatchesLogSourceID(tc.rowSource)
			require.Equal(t, tc.want, got, tc.why)
		})
	}
}

// parseRenderedSourcePredicates 从**真实渲染出的查询文本**里提取来源谓词分支。
//
// 为什么必须解析而不是自己推演：推演与实现共用同一套假设，实现错了推演也错，
// 用例就变成恒真的摆饰（第一版就是这么写的，变异验证时抓不到任何东西）。
// 这里只老实读 selectorQuery 的产物。
func parseRenderedSourcePredicates(text string) (equal []string, prefix []string) {
	// 前缀分支先抽：它的形式是 `log_source_id:"<val>"*`（无等号）。
	const prefixMarker = `log_source_id:"`
	const equalMarker = `log_source_id:="`
	for rest := text; ; {
		i := strings.Index(rest, prefixMarker)
		if i < 0 {
			break
		}
		// 如果这个位置前面紧紧跟着 `=`，那是等值分支，交给下一轮。
		rest = rest[i+len(prefixMarker):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			break
		}
		value := rest[:end]
		after := rest[end+1:]
		if strings.HasPrefix(after, "*") {
			prefix = append(prefix, value)
			rest = after[1:]
			continue
		}
		rest = after
	}
	for rest := text; ; {
		i := strings.Index(rest, equalMarker)
		if i < 0 {
			break
		}
		rest = rest[i+len(equalMarker):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			break
		}
		equal = append(equal, rest[:end])
		rest = rest[end+1:]
	}
	return equal, prefix
}

// renderedMatches 用**从文本解析出的分支**判定某行是否被该 selector 命中。
func renderedMatches(text, row string) bool {
	equal, prefix := parseRenderedSourcePredicates(text)
	for _, v := range equal {
		if row == v {
			return true
		}
	}
	for _, v := range prefix {
		if strings.HasPrefix(row, v) {
			return true
		}
	}
	return false
}

// TestSelectorNamespaceSelectorIsSingleSourceOfTruth 钉住「一处实现、多处共用」。
//
// 本次故障的本质是同一概念被抄成四份、改一处漏三处。这条用例把
// LogsQL 侧（从 selectorQuery 产物里解析出的分支）与内存归因侧
// （SourceScope.MatchesLogSourceID）的行为对齐：对同一批候选行，两边必须给出相同判定。
//
// 转红方式（实测）：命名空间退回只等值、或前缀改成不收口的 `<ns>*`，均使本用例失败。
func TestSelectorNamespaceSelectorIsSingleSourceOfTruth(t *testing.T) {
	const namespace = "inst:318"
	rng := simplifiedPartitionRange(namespace, "2026-11-04")
	text := selectorQuery(rng, query.RangeQuery{})

	// 前置：确实解析出了两种分支（否则下面的对照是空对空）。
	equal, prefix := parseRenderedSourcePredicates(text)
	require.NotEmpty(t, equal, "必须解析出等值分支；实际文本: %s", text)
	require.NotEmpty(t, prefix, "必须解析出前缀分支；实际文本: %s", text)

	rows := []string{
		"inst:318/file",
		"inst:318",
		"inst:31/file",
		"inst:3180/file",
		"other:1/file",
		"",
	}
	for _, row := range rows {
		logsqlMatch := renderedMatches(text, row)
		memoryMatch := (query.SourceScope{Namespace: namespace}).MatchesLogSourceID(row)
		require.Equal(t, logsqlMatch, memoryMatch,
			"LogsQL 侧与内存归因侧必须同口径，否则会出现「粗筛有行、归因全丢」的行 %q", row)
	}

	// 再钉住几条绝对事实（不只靠两边一致——两边一起错就看不出来）。
	require.True(t, renderedMatches(text, "inst:318/file"), "真机形态必须命中")
	require.False(t, renderedMatches(text, "inst:31/file"), "不得跳到别的实例")
	require.False(t, renderedMatches(text, "inst:3180/file"), "前缀必须以 `/` 收口")
}

// TestSelectorKeepsGenerationFilterOnScopedRanges 反向保护：精确形态不得被这次修复带偏。
//
// 简化后的命名空间回退分支不能「顺手」影响有逐源投影的 range——
// 那类 range 仍必须按 (log_source_id, source_generation, record_end 水位) 精确约束，
// 一条都不能放宽。
func TestSelectorKeepsGenerationFilterOnScopedRanges(t *testing.T) {
	rng := query.AuthoritativeRange{
		TargetID: "inst:9/2026-11-04",
		Key:      catalog.PartitionKey{StorageNamespace: "inst:9", UTCDay: "2026-11-04"},
		Owner:    catalog.OwnerHot,
		Projection: &catalog.PublishedProjection{
			SourceProjections: []catalog.SourceProjection{{
				SourceGenerationRef: catalog.SourceGenerationRef{
					LogSourceID: "inst:9/file", SourceGeneration: "gen-1",
				},
				ClosedVisibleSeq: 500,
			}},
		},
	}
	text := selectorQuery(rng, query.RangeQuery{})

	require.Contains(t, text, `log_source_id:="inst:9/file"`, "精确形态必须用完整源 id")
	require.Contains(t, text, `source_generation:="gen-1"`, "来源代次约束不得丢")
	require.Contains(t, text, `record_end:<=500`, "scope 水位约束不得丢")
	require.NotContains(t, text, `log_source_id:"inst:9/"*`,
		"有逐源投影时不得回退命名空间：那会把同命名空间的其他源也读出来")
}
