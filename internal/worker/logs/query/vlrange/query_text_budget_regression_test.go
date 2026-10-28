package vlrange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func queryLongScope() (query.AuthoritativeRange, []string) {
	generations := make([]string, 617)
	for i := range generations {
		generations[i] = fmt.Sprintf("proof-%03d-%s", i, strings.Repeat("g", 55))
	}
	rng := query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "text-budget", UTCDay: "2026-09-22"}, Owner: catalog.OwnerHot, ClosedVisibleSeq: 1000,
		Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "source", SourceGeneration: "s1"}, ProjectionGenerations: generations, ClosedVisibleSeq: 1000}}}}
	return rng, []string{generations[0], generations[308], generations[616]}
}

func TestQueryTextBudgetLargeScopesSearchStatsFieldsFacets(t *testing.T) {
	rng, visible := queryLongScope()
	var maxQuery, wireBytes, requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		text := r.Form.Get("query")
		n := int64(len(text) + len(r.Form.Get("extra_filters")))
		requests.Add(1)
		for old := maxQuery.Load(); n > old && !maxQuery.CompareAndSwap(old, n); old = maxQuery.Load() {
		}
		if n > 16<<10 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("search.maxQueryLen=16384"))
			return
		}
		selected := make([]string, 0, 3)
		for _, generation := range visible {
			if strings.Contains(text, "log_source_id:in(") || strings.Contains(text, strconv.Quote(generation)) {
				selected = append(selected, generation)
			}
		}
		var body strings.Builder
		if strings.Contains(text, " | stats") {
			if strings.Contains(text, " by (level)") {
				fmt.Fprintf(&body, "{\"level\":\"INFO\",\"_jm_count\":%d}\n", len(selected))
			} else {
				fmt.Fprintf(&body, "{\"_jm_count\":%d}\n", len(selected))
			}
		} else {
			for i, generation := range selected {
				fmt.Fprintf(&body, "{\"_time\":\"2026-09-22T12:00:0%dZ\",\"event_id\":%q,\"log_source_id\":\"source\",\"source_generation\":\"s1\",\"projection_generation\":%q,\"record_end\":10,\"level\":\"INFO\",\"instance_id\":\"1\"}\n", i, generation, generation)
			}
		}
		wireBytes.Add(int64(body.Len()))
		_, _ = w.Write([]byte(body.String()))
	}))
	t.Cleanup(srv.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	q := query.RangeQuery{ClosedVisibleSeq: 1000, Budget: query.Budget{Limit: 100, MaxBytes: 1 << 20}}
	search, err := client.Search(context.Background(), rng, q)
	require.NoError(t, err, "617个物理代次不能突破批准VL默认16KiB硬界")
	require.Len(t, search.Items, 3)
	stats, err := client.Stats(context.Background(), rng, q)
	require.NoError(t, err)
	var count uint64
	for _, point := range stats.Points {
		count += point.Count
	}
	require.EqualValues(t, 3, count, "按不重叠物理谓词分片后必须完整合并逻辑统计")
	fields, err := client.Fields(context.Background(), rng, q)
	require.NoError(t, err)
	require.Contains(t, fields.Fields, "instance_id")
	facets, err := client.Facets(context.Background(), rng, q, []string{"level"}, 10)
	require.NoError(t, err)
	require.Len(t, facets.Values, 1)
	require.EqualValues(t, 3, facets.Values[0].Count)
	require.LessOrEqual(t, maxQuery.Load(), int64(physicalMaxQueryBytes))
	require.Greater(t, requests.Load(), int64(4), "统计分片必须走真实多请求，不能靠提高VL上限")
	wireBytes.Store(0)
	q.Budget.MaxBytes = 40
	partial, err := client.Stats(context.Background(), rng, q)
	require.NoError(t, err)
	require.LessOrEqual(t, partial.Bytes, uint64(40))
	require.Contains(t, partial.CoverageReasons, query.ReasonBudgetExceeded, "跨片总字节见底必须partial，不能每片复制额度")
}

func TestQueryTextBudgetLargeMergedSourceSetIsSplit(t *testing.T) {
	var requests, maxQuery atomic.Int64
	ranges := make([]query.AuthoritativeRange, 300)
	for i := range ranges {
		source := fmt.Sprintf("source-%03d-%s", i, strings.Repeat("x", 90))
		ranges[i] = query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: source, UTCDay: "2026-09-22"}, Owner: catalog.OwnerHot, ClosedVisibleSeq: 10,
			Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: source, SourceGeneration: "s1"}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 10}}}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		text := r.Form.Get("query")
		n := int64(len(text) + len(r.Form.Get("extra_filters")))
		requests.Add(1)
		for old := maxQuery.Load(); n > old && !maxQuery.CompareAndSwap(old, n); old = maxQuery.Load() {
		}
		if n > 16<<10 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for i, rng := range ranges {
			if !strings.Contains(text, strconv.Quote(rng.Key.StorageNamespace)) {
				continue
			}
			fmt.Fprintf(w, "{\"_time\":%q,\"event_id\":%q,\"log_source_id\":%q,\"source_generation\":\"s1\",\"projection_generation\":\"p1\",\"record_end\":10}\n", time.Date(2026, 9, 22, 12, 0, 0, i, time.UTC).Format(time.RFC3339Nano), strconv.Itoa(i), rng.Key.StorageNamespace)
		}
	}))
	t.Cleanup(srv.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	result, err := client.SearchMerged(context.Background(), ranges, query.RangeQuery{Budget: query.Budget{Limit: 1000, MaxBytes: 1 << 20}})
	require.NoError(t, err)
	require.Len(t, result.Items, len(ranges))
	for i := 1; i < len(result.Items); i++ {
		require.Negative(t, query.CompareSortKey(query.SortKeyOf(result.Items[i-1]), query.SortKeyOf(result.Items[i])), "物理来源分片必须恢复全局冻结复合排序")
	}
	require.True(t, result.Exhausted)
	require.Empty(t, result.CoverageReasons)
	require.LessOrEqual(t, maxQuery.Load(), int64(physicalMaxQueryBytes))
	require.Greater(t, requests.Load(), int64(1))
}

func TestQueryTextBudgetOversizedUserFilterRejectedBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(srv.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	rng := query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "source", UTCDay: "2026-09-22"}, ClosedVisibleSeq: 10}
	_, err = client.Search(context.Background(), rng, query.RangeQuery{ClosedVisibleSeq: 10, Filter: strings.Repeat("中", 6000)})
	require.Error(t, err)
	require.Zero(t, calls.Load(), "不可安全拆分的用户语法按UTF-8实际字节在本地明确拒绝")
}

func TestQueryTextBudgetSearchFallbackKeepsExplicitPrefix(t *testing.T) {
	rng, generations := queryLongScope()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "{\"_time\":\"2026-09-22T12:00:00Z\",\"event_id\":\"after-fixed-prefix\",\"log_source_id\":\"source\",\"source_generation\":\"s1\",\"projection_generation\":%q,\"record_end\":900}\n", generations[0])
	}))
	t.Cleanup(srv.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	result, err := client.Search(context.Background(), rng, query.RangeQuery{ClosedVisibleSeq: 700, Budget: query.Budget{MaxBytes: 1 << 20}})
	require.NoError(t, err)
	require.Empty(t, result.Items, "长query切换为粗筛后，不能以range最新水位冲掉请求固定前缀")
	require.True(t, result.Exhausted)
}

func TestQueryTextBudgetFinalBoundaryQueryDoesNotBypassHardGuard(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.NoError(t, r.ParseForm())
		require.LessOrEqual(t, len(r.Form.Get("query"))+len(r.Form.Get("extra_filters")), physicalMaxQueryBytes)
		for i := 0; i < 2; i++ {
			fmt.Fprintf(w, "{\"_time\":\"2026-09-22T12:00:00Z\",\"event_id\":\"%d\",\"log_source_id\":\"source\",\"record_end\":1}\n", i)
		}
	}))
	t.Cleanup(srv.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	rng := query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "source", UTCDay: "2026-09-22"}, ClosedVisibleSeq: 10}
	q := query.RangeQuery{ClosedVisibleSeq: 10, Budget: query.Budget{Limit: 1, MaxBytes: 1 << 20}}
	q.Filter = strings.Repeat("a", physicalMaxQueryBytes-len(searchQuery(rng, q)))
	result, err := client.Search(context.Background(), rng, q)
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load(), "边界组扩大limit的额外字节必须在最终发送前重新验证")
	require.False(t, result.Exhausted)
	require.Empty(t, result.Items, "未完整读取的并列组不能成为游标锚点")
	require.Contains(t, result.CoverageReasons, query.ReasonBudgetExceeded)
}

func TestQueryTextBudgetFanoutSharedAcrossShardsAndFacets(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("{\"level\":\"INFO\",\"_jm_count\":1}\n"))
	}))
	t.Cleanup(srv.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	rng, _ := queryLongScope()
	result, err := client.Stats(context.Background(), rng, query.RangeQuery{ClosedVisibleSeq: 1000, Budget: query.Budget{MaxFanout: 2, MaxBytes: 1 << 20}})
	require.ErrorIs(t, err, query.ErrRangeQueryBudget)
	require.EqualValues(t, 2, calls.Load(), "所有物理分片共享一次fanout预算")
	require.Greater(t, result.Bytes, uint64(0))
	calls.Store(0)
	rng.Projection = nil
	_, err = client.Facets(context.Background(), rng, query.RangeQuery{ClosedVisibleSeq: 1000, Budget: query.Budget{MaxFanout: 1}}, []string{"level", "stream"}, 10)
	require.ErrorIs(t, err, query.ErrRangeQueryBudget)
	require.EqualValues(t, 1, calls.Load(), "不同facet维度不能分别重置fanout")
}

func TestQueryTextBudgetDisjointScopeTermsCollapseOverlappingEvidence(t *testing.T) {
	rng, _ := queryLongScope()
	scope := rng.Projection.SourceProjections[0]
	scope.ClosedVisibleSeq = 400
	scope.ProjectionGenerations = append(scope.ProjectionGenerations, scope.ProjectionGenerations[0])
	rng.Projection.SourceProjections = append(rng.Projection.SourceProjections, scope)
	q := query.RangeQuery{ClosedVisibleSeq: 700}
	terms := disjointScopeTerms(rng, q)
	require.Len(t, terms, 617, "重叠证据不能使物理统计分片重复计数")
	for _, term := range terms {
		require.Contains(t, term, "record_end:<=700")
	}
	source := "来源\\\"\n\t"
	rng.Projection.SourceProjections[0].LogSourceID = source
	terms = disjointScopeTerms(rng, q)
	require.Contains(t, strings.Join(terms, " OR "), "log_source_id:="+strconv.Quote(source), "控制字符和引号须按最终LogsQL文本计算")
	rng.Projection.SourceProjections = []catalog.SourceProjection{{ProjectionGenerations: []string{""}, ClosedVisibleSeq: 0}}
	require.Equal(t, []string{`(log_source_id:="" AND source_generation:="" AND projection_generation:="" AND record_end:<=0)`}, disjointScopeTerms(rng, q), "明确空字段是精确谓词，不可在分片时当作无约束")
}

func TestQueryTextBudgetHTTPShardsShareWholeRequestDeadline(t *testing.T) {
	for _, operation := range []string{"search", "stats", "facets"} {
		t.Run(operation, func(t *testing.T) {
			rng, _ := queryLongScope()
			generations := []string{"short-generation"}
			if operation != "search" {
				generations = make([]string, 8)
				for i := range generations {
					generations[i] = fmt.Sprintf("deadline-%d-%s", i, strings.Repeat("g", 2100))
				}
			}
			rng.Projection.SourceProjections[0].ProjectionGenerations = generations
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) > 1 {
					<-r.Context().Done()
					return
				}
				if operation != "search" {
					_, _ = w.Write([]byte("{\"level\":\"INFO\",\"_jm_count\":1}\n"))
					return
				}
				for i := 0; i < 2; i++ {
					fmt.Fprintf(w, "{\"_time\":\"2026-09-22T12:00:00Z\",\"event_id\":\"%d\",\"log_source_id\":\"source\",\"source_generation\":\"s1\",\"projection_generation\":%q,\"record_end\":10}\n", i, generations[0])
				}
			}))
			t.Cleanup(srv.Close)
			vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
			require.NoError(t, err)
			client, err := New(vl)
			require.NoError(t, err)
			cat := catalog.New(nil)
			record := catalog.NewStableRecord(rng.Key, catalog.OwnerHot, 1, "hot-1")
			record.PublishedProjection = rng.Projection
			record.PublishedProjection.ManifestVersion = "deadline"
			record.PublishedProjection.CoverageComplete = true
			record.PublishedProjection.QueryLocationDirID = "hot-1"
			record.PublishedProjection.QueryGeneration = 1
			require.NoError(t, cat.PublishProjection(nil, record))
			planner := query.NewPlanner(cat, nil)
			t.Cleanup(func() { require.NoError(t, planner.Close()) })
			var rangeClient query.RangeClient = client
			if operation == "search" {
				// 该夹具专门进入单 range 页边界补查，合并粗筛路径由独立回归覆盖。
				rangeClient = &queryCountingRange{inner: client}
			}
			svc := query.NewService(planner, rangeClient, "http-deadline")
			req := query.QueryRequest{AuthorizedTargets: []string{rng.Key.String()}, Budget: query.Budget{Limit: 1, TimeoutMS: 100, MaxBytes: 1 << 20}}
			var queryErr *query.QueryError
			var coverage query.Coverage
			switch operation {
			case "search":
				response := svc.Search(context.Background(), req)
				queryErr, coverage = response.Err, response.Coverage
				require.Empty(t, response.NextCursor)
				require.False(t, response.Exhausted)
			case "stats":
				response := svc.Stats(context.Background(), req, nil)
				queryErr, coverage = response.Err, response.Coverage
				require.NotEqual(t, query.StatsExact, response.Quality.StatsQuality)
			case "facets":
				response := svc.Facets(context.Background(), req, []string{"level"}, 10)
				queryErr, coverage = response.Err, response.Coverage
			}
			require.NotNil(t, queryErr)
			require.Equal(t, query.ErrCodeCancelled, queryErr.Code)
			require.False(t, coverage.Complete)
			require.Contains(t, coverage.PartialReasons, query.ReasonCancelled)
			require.EqualValues(t, 2, calls.Load(), "实际首片/边界片后整请求到期，不得为后续片续期或发出第三次HTTP")
		})
	}
}

func TestRealVLQueryTextBudget617Generations(t *testing.T) {
	ctx, _, _, hot := queryApprovedVL(t)
	client, err := New(hot)
	require.NoError(t, err)
	rng, _ := queryLongScope()
	generations := rng.Projection.SourceProjections[0].ProjectionGenerations
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	rng.Key.UTCDay = day.Format("2006-01-02")
	var body strings.Builder
	for i, generation := range generations {
		event := queryPhysicalEvent("source", "s1", uint64(10+i), day.Add(12*time.Hour+time.Duration(i)*time.Second), fmt.Sprintf("长scope原文-%d", i))
		row := map[string]any{"_time": event.EventTimeUTC, "_msg": event.Message, "event_id": event.EventID,
			"log_source_id": event.Source.LogSourceID, "source_generation": event.Source.SourceGeneration,
			"parser_version": event.Source.ParserVersion, "record_start": event.Record.Start, "record_end": event.Record.End,
			"ingest_time_utc": event.IngestTimeUTC, "level": event.Level, "stream": event.Stream,
			"canonical_content_hash": event.CanonicalHash, "projection_generation": generation}
		encoded, encodeErr := json.Marshal(row)
		require.NoError(t, encodeErr)
		body.Write(encoded)
		body.WriteByte('\n')
	}
	_, err = hot.InsertJSONLines(ctx, []byte(body.String()))
	require.NoError(t, err)
	_, err = hot.Post(ctx, "/internal/force_flush", nil)
	require.NoError(t, err)
	q := query.RangeQuery{ClosedVisibleSeq: 1000, Budget: query.Budget{Limit: 1000, MaxBytes: 1 << 20}}
	result, err := client.Search(ctx, rng, q)
	require.NoError(t, err)
	require.Len(t, result.Items, len(generations))
	require.True(t, result.Exhausted)
	stats, err := client.Stats(ctx, rng, q)
	require.NoError(t, err)
	var count uint64
	for _, point := range stats.Points {
		count += point.Count
	}
	require.EqualValues(t, len(generations), count)
	facets, err := client.Facets(ctx, rng, q, []string{"level"}, 10)
	require.NoError(t, err)
	require.Len(t, facets.Values, 1)
	require.EqualValues(t, len(generations), facets.Values[0].Count)
	t.Logf("批准VL默认16KiB硬界未调高，617代次Search/Stats/Facets固定617条，身份哈希=%s", queryIdentityDigest(result.Items))
}
