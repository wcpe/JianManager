package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
)

// cpPageExportPB 让 HTTP、CP 与 Worker Service 使用真实映射，只有反向隧道传输被进程内替代。
type cpPageExportPB struct {
	workerpb.WorkerServiceClient
	service *grpcsvc.Service
	stats   atomic.Int32
}

func (p *cpPageExportPB) LogCreateView(ctx context.Context, query *workerpb.LogCreateViewRequest, _ ...grpc.CallOption) (*workerpb.LogCreateViewResponse, error) {
	return p.service.LogCreateView(ctx, query)
}

func (p *cpPageExportPB) LogSearch(ctx context.Context, query *workerpb.LogSearchRequest, _ ...grpc.CallOption) (*workerpb.LogSearchResponse, error) {
	return p.service.LogSearch(ctx, query)
}

func (p *cpPageExportPB) LogStats(ctx context.Context, query *workerpb.LogStatsRequest, _ ...grpc.CallOption) (*workerpb.LogStatsResponse, error) {
	p.stats.Add(1)
	return p.service.LogStats(ctx, query)
}

func cpPageExportFixture(t *testing.T) (*gin.Engine, string, string, *cpPageExportPB, *atomic.Bool) {
	t.Helper()
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "cp-page-hot")
	record.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "cp-page-m1", CoverageComplete: true,
		QueryLocationDirID: "cp-page-hot", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 30},
		SourceProjections: []catalog.SourceProjection{{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "src", SourceGeneration: "g"}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 30}}}
	require.NoError(t, cat.Put(record))
	rows := []logtypes.Event{
		{EventID: "first", EventTimeUTC: "2026-09-22T12:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "src", SourceGeneration: "g"}, Record: logtypes.RecordRange{Start: 0, End: 10}},
		{EventID: "second", EventTimeUTC: "2026-09-22T11:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "src", SourceGeneration: "g"}, Record: logtypes.RecordRange{Start: 10, End: 20}},
		{EventID: "third", EventTimeUTC: "2026-09-22T10:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "src", SourceGeneration: "g"}, Record: logtypes.RecordRange{Start: 20, End: 30}},
	}
	partial := &atomic.Bool{}
	ranges := workerquery.FuncRangeClient{
		SearchFn: func(context.Context, workerquery.AuthoritativeRange, workerquery.RangeQuery) (workerquery.RangeResult, error) {
			return workerquery.RangeResult{Items: rows, Exhausted: true, DuplicateQuality: workerquery.DupExact, StatsQuality: workerquery.StatsExact}, nil
		},
		StatsFn: func(context.Context, workerquery.AuthoritativeRange, workerquery.RangeQuery) (workerquery.StatsResult, error) {
			out := workerquery.StatsResult{Points: []workerquery.StatsPoint{{Count: 3}}, StatsQuality: workerquery.StatsExact}
			if partial.Load() {
				out.CoverageReasons = []string{workerquery.ReasonRangeUnavailable}
			}
			return out, nil
		},
	}
	planner := workerquery.NewPlanner(cat, nil)
	pb := &cpPageExportPB{service: grpcsvc.New(workerquery.NewService(planner, ranges, "cp-page-test"), planner)}
	resolver := &fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline}}}
	coord := logcoord.New(resolver, logcoord.NewTunnelDialer(func(context.Context, string) (workerpb.WorkerServiceClient, error) { return pb, nil }))
	router, _ := setupFederationRouter(t, coord)
	token := getAdminToken(t, router)
	scope := "instanceId=1&from=2026-09-22T00:00:00Z&to=2026-09-23T00:00:00Z"
	return router, token, scope, pb, partial
}

func TestSixModuleHTTPPageExportPreservesPageAndFreshGate(t *testing.T) {
	router, token, scope, pb, partial := cpPageExportFixture(t)
	first := makeRequest(router, http.MethodGet, "/api/v1/logs/federation/search?"+scope+"&limit=1", nil, token)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	body := parseJSON(t, first)
	viewID := body["view"].(map[string]any)["view_id"].(string)
	cursor := body["next_cursor"].(string)
	params := scope + "&viewId=" + url.QueryEscape(viewID)
	// 预先填入 HTTP Stats 缓存；随后页面导出仍必须重新调用真实 Worker。
	stats := makeRequest(router, http.MethodGet, "/api/v1/logs/federation/stats?"+params, nil, token)
	require.Equal(t, http.StatusOK, stats.Code, stats.Body.String())
	before := pb.stats.Load()
	path := "/api/v1/logs/federation/export?" + params + "&range=page&limit=1&cursor=" + url.QueryEscape(cursor)
	exported := makeRequest(router, http.MethodPost, path, nil, token)
	require.Equal(t, http.StatusOK, exported.Code, exported.Body.String())
	require.Equal(t, "application/x-ndjson", exported.Header().Get("Content-Type"), exported.Body.String())
	require.Contains(t, exported.Header().Get("Content-Disposition"), "attachment")
	var event logcoord.Event
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(exported.Body.String())), &event))
	require.Equal(t, "second", event.EventID)
	require.Greater(t, pb.stats.Load(), before)
	partial.Store(true)
	blocked := makeRequest(router, http.MethodPost, path, nil, token)
	require.Equal(t, http.StatusOK, blocked.Code, blocked.Body.String())
	require.Contains(t, blocked.Header().Get("Content-Type"), "application/json")
	incomplete := parseJSON(t, blocked)
	require.Equal(t, true, incomplete["export_incomplete"])
	require.Nil(t, incomplete["artifact"])
	require.Empty(t, blocked.Header().Get("Content-Disposition"))
}

func TestSixModuleHTTPExportRangeValidationAndAllCompatibility(t *testing.T) {
	router, token, scope, _, _ := cpPageExportFixture(t)
	for _, suffix := range []string{"&range=unknown", "&range=page"} {
		response := makeRequest(router, http.MethodPost, "/api/v1/logs/federation/export?"+scope+suffix, nil, token)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	first := makeRequest(router, http.MethodGet, "/api/v1/logs/federation/search?"+scope+"&limit=1", nil, token)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	body := parseJSON(t, first)
	viewID := body["view"].(map[string]any)["view_id"].(string)
	cursor := body["next_cursor"].(string)
	page := "/api/v1/logs/federation/export?" + scope + "&range=page&viewId=" + url.QueryEscape(viewID)
	changed := makeRequest(router, http.MethodPost, page+"&keyword=changed", nil, token)
	require.Equal(t, http.StatusConflict, changed.Code, changed.Body.String())
	foreign := makeRequest(router, http.MethodPost, strings.Replace(page, "instanceId=1", "instanceId=2", 1), nil, token)
	require.Equal(t, http.StatusForbidden, foreign.Code, foreign.Body.String())
	for _, explicit := range []string{"", "&range=all"} {
		response := makeRequest(router, http.MethodPost, "/api/v1/logs/federation/export?"+scope+"&viewId="+url.QueryEscape(viewID)+"&cursor="+url.QueryEscape(cursor)+explicit, nil, token)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, "application/x-ndjson", response.Header().Get("Content-Type"))
		lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n")
		require.Len(t, lines, 3, "默认/all 均须忽略当前页游标并导出整窗")
		require.Contains(t, lines[0], `"event_id":"first"`)
	}
}
