package vlrange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func mergedTestEvent(logSourceID, sourceGen, projGen string, recordEnd uint64) logtypes.Event {
	ev := logtypes.Event{
		EventID:       "eid-" + logSourceID + "-" + projGen,
		EventTimeUTC:  "2026-09-22T01:02:03Z",
		CanonicalHash: "hash-" + logSourceID + "-" + projGen,
	}
	ev.Source.LogSourceID = logSourceID
	ev.Source.SourceGeneration = sourceGen
	ev.Record.End = recordEnd
	if projGen != "" {
		ev.Fields = map[string]string{projectionGenerationField: projGen}
	}
	return ev
}

func scopedRange(targetID, src, gen string, gens []string, seq uint64) query.AuthoritativeRange {
	return query.AuthoritativeRange{
		TargetID:         targetID,
		Key:              queryKey(),
		Owner:            catalog.OwnerHot,
		ClosedVisibleSeq: seq,
		Projection: &catalog.PublishedProjection{
			SourceProjections: []catalog.SourceProjection{{
				SourceGenerationRef:   catalog.SourceGenerationRef{LogSourceID: src, SourceGeneration: gen},
				ProjectionGenerations: gens,
				ClosedVisibleSeq:      seq,
			}},
		},
	}
}

func TestMergedSearchQueryShape(t *testing.T) {
	q := query.RangeQuery{Budget: query.Budget{Limit: 100, MaxBytes: 1 << 20}}
	text := mergedSearchQuery([]string{"src-a", "src-b"}, q)
	// in() 语法（PoC 实测比 OR 组快 ~4.6 倍）+ 管道与 searchQuery 同形。
	require.Contains(t, text, `log_source_id:in("src-a","src-b")`)
	require.Contains(t, text, projectionGenerationField)
	require.Contains(t, text, "| sort by (_time desc)")
	require.Contains(t, text, "| limit 303") // (页 100 + 1 前瞻) × 3
	// 转义：字符串中的引号/反斜杠必须转义（与 selectorQuery 同款）。
	escaped := mergedSearchQuery([]string{`a"b\c`}, q)
	require.Contains(t, escaped, `log_source_id:in("a\"b\\c")`)
}

func TestRangeMatchesEventPredicates(t *testing.T) {
	// scoped：仅接受 (src-a, gen-a, projgen ∈ {pgen-A-1}, record_end <= 100)。
	scoped := scopedRange("inst:1", "src-a", "gen-a", []string{"pgen-A-1"}, 100)
	// 非 scoped：接受 CoveredSourceGenerations 的 (src-b, gen-b)，
	// 且 projection_generation 必须 ∈ {pgen-B}。
	nonScoped := query.AuthoritativeRange{
		TargetID: "inst:2",
		Key:      queryKey(),
		Owner:    catalog.OwnerHot,
		Projection: &catalog.PublishedProjection{
			CoveredSourceGenerations: []catalog.SourceGenerationRef{{LogSourceID: "src-b", SourceGeneration: "gen-b"}},
			ProjectionGenerations:    []string{"pgen-B"},
		},
	}
	// 无 Projection：回退 StorageNamespace 相等。
	plain := query.AuthoritativeRange{
		TargetID: "inst:3",
		Key:      catalog.PartitionKey{StorageNamespace: "src-c", UTCDay: "2026-09-22"},
		Owner:    catalog.OwnerHot,
	}

	cases := []struct {
		name string
		rng  query.AuthoritativeRange
		ev   logtypes.Event
		want bool
	}{
		{"scoped 命中", scoped, mergedTestEvent("src-a", "gen-a", "pgen-A-1", 100), true},
		{"scoped 水位等于上界（<= 保留）", scoped, mergedTestEvent("src-a", "gen-a", "pgen-A-1", 100), true},
		{"scoped 超水位", scoped, mergedTestEvent("src-a", "gen-a", "pgen-A-1", 101), false},
		{"scoped 旧代次", scoped, mergedTestEvent("src-a", "gen-a", "pgen-A-OLD", 100), false},
		{"scoped 错来源", scoped, mergedTestEvent("src-x", "gen-a", "pgen-A-1", 100), false},
		{"scoped 错 source_generation", scoped, mergedTestEvent("src-a", "gen-other", "pgen-A-1", 100), false},
		{"非 scoped 命中", nonScoped, mergedTestEvent("src-b", "gen-b", "pgen-B", 1000), true},
		{"非 scoped 代次集合不符", nonScoped, mergedTestEvent("src-b", "gen-b", "pgen-OTHER", 1000), false},
		{"非 scoped 来源不符", nonScoped, mergedTestEvent("src-z", "gen-b", "pgen-B", 1000), false},
		{"无 Projection：按 namespace 命中", plain, mergedTestEvent("src-c", "any", "", 10), true},
		{"无 Projection：来源不符", plain, mergedTestEvent("src-d", "any", "", 10), false},
		{
			"scoped 全 scope generation 为空 → 回退 namespace",
			query.AuthoritativeRange{
				TargetID: "inst:4",
				Key:      catalog.PartitionKey{StorageNamespace: "src-e", UTCDay: "2026-09-22"},
				Owner:    catalog.OwnerHot,
				Projection: &catalog.PublishedProjection{
					SourceProjections: []catalog.SourceProjection{{
						SourceGenerationRef:   catalog.SourceGenerationRef{LogSourceID: "src-e", SourceGeneration: "gen-e"},
						ProjectionGenerations: nil, // 空 → selectorQuery 会回退 NS
					}},
				},
			},
			mergedTestEvent("src-e", "gen-e", "whatever", 10),
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, rangeMatchesEvent(tc.rng, tc.ev))
		})
	}

	// range 级水位（rng.ClosedVisibleSeq）独立生效：scoped 内水位满足但 range 水位不满足。
	rngSeq := scopedRange("inst:5", "src-a", "gen-a", []string{"pgen-A-1"}, 50)
	require.False(t, rangeMatchesEvent(rngSeq, mergedTestEvent("src-a", "gen-a", "pgen-A-1", 51)))
	require.True(t, rangeMatchesEvent(rngSeq, mergedTestEvent("src-a", "gen-a", "pgen-A-1", 50)))

	// 无来源约束（NS 空）→ 永不匹配（保守：合并路径不放大可见范围）。
	bare := query.AuthoritativeRange{TargetID: "inst:6", Key: catalog.PartitionKey{UTCDay: "2026-09-22"}}
	require.False(t, rangeMatchesEvent(bare, mergedTestEvent("src-any", "g", "", 1)))
}

func TestSearchMergedFiltersDedupesAndAttributes(t *testing.T) {
	var seenQuery string
	var seenLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/select/logsql/query", r.URL.Path)
		seenQuery = r.URL.Query().Get("query")
		seenLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		// 返回顺序为 VL 的 _time desc 语义：
		// 1. src-a 当前代次（命中）
		// 2. src-a 旧代次同 event_id 同 hash（应被谓词过滤——不属于任何 rng）
		// 3. src-a 当前代次第二次出现（同 id 同 hash → 去重）
		// 4. src-a 同 id 异 hash（保留 + 质量降级）
		// 5. src-b 命中
		// 6. src-a 超水位（应被过滤）
		w.Write([]byte(
			`{"_time":"2026-09-22T03:00:05Z","_msg":"a1","event_id":"e1","log_source_id":"src-a","source_generation":"gen-a","projection_generation":"pgen-A-1","record_start":"90","record_end":"100","level":"INFO","stream":"stdout","canonical_content_hash":"h1"}` + "\n" +
				`{"_time":"2026-09-22T03:00:04Z","_msg":"a-old","event_id":"e1","log_source_id":"src-a","source_generation":"gen-a","projection_generation":"pgen-A-OLD","record_start":"90","record_end":"100","level":"INFO","stream":"stdout","canonical_content_hash":"h1"}` + "\n" +
				`{"_time":"2026-09-22T03:00:03Z","_msg":"a1-dup","event_id":"e1","log_source_id":"src-a","source_generation":"gen-a","projection_generation":"pgen-A-1","record_start":"90","record_end":"100","level":"INFO","stream":"stdout","canonical_content_hash":"h1"}` + "\n" +
				`{"_time":"2026-09-22T03:00:02Z","_msg":"a1-conflict","event_id":"e1","log_source_id":"src-a","source_generation":"gen-a","projection_generation":"pgen-A-1","record_start":"90","record_end":"100","level":"INFO","stream":"stdout","canonical_content_hash":"h2"}` + "\n" +
				`{"_time":"2026-09-22T03:00:01Z","_msg":"b1","event_id":"e2","log_source_id":"src-b","source_generation":"gen-b","projection_generation":"pgen-B","record_start":"1","record_end":"2","level":"INFO","stream":"stdout","canonical_content_hash":"h3"}` + "\n" +
				`{"_time":"2026-09-22T03:00:00Z","_msg":"a-beyond","event_id":"e3","log_source_id":"src-a","source_generation":"gen-a","projection_generation":"pgen-A-1","record_start":"101","record_end":"102","level":"INFO","stream":"stdout","canonical_content_hash":"h4"}` + "\n",
		))
	}))
	defer srv.Close()

	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, AllowNonLoopback: true})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	// 两个 range：scoped(src-a) + 非 scoped(src-b)——同 Owner 同天 → 合并。
	nonScoped := query.AuthoritativeRange{
		TargetID: "inst:2",
		Key:      catalog.PartitionKey{StorageNamespace: "src-b", UTCDay: "2026-09-22"},
		Owner:    catalog.OwnerHot,
		Projection: &catalog.PublishedProjection{
			CoveredSourceGenerations: []catalog.SourceGenerationRef{{LogSourceID: "src-b", SourceGeneration: "gen-b"}},
			ProjectionGenerations:    []string{"pgen-B"},
		},
	}
	rngs := []query.AuthoritativeRange{
		scopedRange("inst:1", "src-a", "gen-a", []string{"pgen-A-1"}, 100),
		nonScoped,
	}
	res, err := client.SearchMerged(context.Background(), rngs, query.RangeQuery{
		TimeRange: query.TimeRange{FromUTC: "2026-09-22T00:00:00Z", ToUTC: "2026-09-23T00:00:00Z"},
		Budget:    query.Budget{Limit: 100, MaxBytes: 1 << 20},
	})
	require.NoError(t, err)

	// 合并查询文本：in() 覆盖两来源；limit 为合并预算（(页 100+1 前瞻) × 3 = 303）。
	require.Contains(t, seenQuery, `log_source_id:in("src-a","src-b")`)
	require.Equal(t, "303", seenLimit)

	// 归因结果：a1(e1/h1) + a1-conflict(e1/h2 保留) + b1 —— 旧代次/重复/超水位被过滤。
	require.Len(t, res.Items, 3)
	require.Equal(t, "a1", res.Items[0].Message)
	require.Equal(t, "a1-conflict", res.Items[1].Message)
	require.Equal(t, "b1", res.Items[2].Message)
	// 同 id 异 hash → 质量降级（不得宣称 exact）。
	require.Equal(t, query.DupUnresolved, res.DuplicateQuality)
	// 千字节级响应未达预算 → 未截断。
	require.True(t, res.Exhausted)
}

// 跨天合并：窗口取各天并集、行归因跨天正确。
// 回归守护（2026-10-08 生产缺陷）：曾因 SearchMerged 残留"同 UTC 天"防御，
// 使 24h 跨天查询整组报错、全部 target 标 RANGE_UNAVAILABLE 返回 0 条。
func TestSearchMergedAcrossDaysUsesUnionWindow(t *testing.T) {
	var seenStart, seenEnd string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenStart = r.URL.Query().Get("start")
		seenEnd = r.URL.Query().Get("end")
		w.Header().Set("Content-Type", "application/json")
		// 两行分属两天（09-22 与 09-23），各自由对应 rng 的谓词接受。
		w.Write([]byte(
			`{"_time":"2026-09-22T20:00:00Z","_msg":"d1","event_id":"e1","log_source_id":"src-a","source_generation":"gen-a","projection_generation":"pgen-A-1","record_start":"1","record_end":"2","level":"INFO","stream":"stdout","canonical_content_hash":"h1"}` + "\n" +
				`{"_time":"2026-09-23T05:00:00Z","_msg":"d2","event_id":"e2","log_source_id":"src-b","source_generation":"gen-b","projection_generation":"pgen-B-1","record_start":"1","record_end":"2","level":"INFO","stream":"stdout","canonical_content_hash":"h2"}` + "\n",
		))
	}))
	defer srv.Close()

	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, AllowNonLoopback: true})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	day1 := scopedRange("inst:1", "src-a", "gen-a", []string{"pgen-A-1"}, 100)
	day2 := scopedRange("inst:2", "src-b", "gen-b", []string{"pgen-B-1"}, 100)
	day2.Key.UTCDay = "2026-09-23"

	res, err := client.SearchMerged(context.Background(), []query.AuthoritativeRange{day1, day2}, query.RangeQuery{
		TimeRange: query.TimeRange{FromUTC: "2026-09-22T12:00:00Z", ToUTC: "2026-09-23T12:00:00Z"},
		Budget:    query.Budget{Limit: 100, MaxBytes: 1 << 20},
	})
	require.NoError(t, err, "跨天合并必须被接受（回归：曾整组报错返回 0 条）")
	require.Len(t, res.Items, 2, "两天的行都应归因保留")
	// 窗口 = 各天并集 ∩ TimeRange。
	require.Equal(t, "2026-09-22T12:00:00Z", seenStart)
	require.Equal(t, "2026-09-23T12:00:00Z", seenEnd)
}

// 混 Owner → 拒绝（不同分布层级的数据时间可重叠，不得合并）。
func TestSearchMergedRejectsMixedOwner(t *testing.T) {
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: "http://127.0.0.1:1", AllowNonLoopback: true})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	hot := scopedRange("inst:1", "src-a", "gen-a", []string{"pgen-A-1"}, 100)
	cold := scopedRange("inst:2", "src-b", "gen-b", []string{"pgen-B-1"}, 100)
	cold.Owner = catalog.OwnerCold

	_, err = client.SearchMerged(context.Background(), []query.AuthoritativeRange{hot, cold}, query.RangeQuery{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "share owner"))

	// 无来源约束的 range 被拒绝（不放大可见范围）。
	bare := query.AuthoritativeRange{
		TargetID: "inst:3",
		Key:      catalog.PartitionKey{UTCDay: "2026-09-22"},
		Owner:    catalog.OwnerHot,
	}
	_, err = client.SearchMerged(context.Background(), []query.AuthoritativeRange{hot, bare}, query.RangeQuery{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "no expressible source scope"))
}
