package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Both API entrances share one lexical boundary. Ordinary keyword remains a
// LogsQL filter (including field expressions), never a hidden pipe entrance.
func TestLogQueryExpressionsShareFilterBoundary(t *testing.T) {
	valid := []string{
		`error OR (level:WARN AND stream:="stderr")`,
		`_msg:re("error|warn")`,
		`_msg:'a|b)("'`,
		"_msg:`a|b)(\"`",
		`_msg:"a\\\"b|c"`,
		`{stream="stderr"} AND _time:[2026-09-20T00:00:00Z, 2026-09-21T00:00:00Z)`,
		`log_source_id:="inst:2/file" OR (log_source_id:="inst:1/file" AND error)`,
	}
	invalid := []struct{ filter, hint string }{
		{`error | stats count()`, "管道"},
		{`error | union (*)`, "管道"},
		{`* | stream_context after 5`, "管道"},
		{`error) OR (*`, "括号"},
		{`_msg:"open`, "引号"},
		{`_msg:'open`, "引号"},
		{"_msg:`open", "引号"},
		{strings.Repeat("错", 3000), "8192"},
		{"error\x00warn", "控制字符"},
	}
	for _, entrance := range []string{"keyword", "logsQL"} {
		for _, filter := range valid {
			t.Run(entrance+" valid "+filter, func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				params := url.Values{entrance: {filter}, "level": {"warn"}, "source": {"instance"}}
				if entrance == "logsQL" {
					params.Set("advanced", "true")
				}
				c.Request = httptest.NewRequest(http.MethodGet, "/?"+params.Encode(), nil)
				got, err := federationFilter(c)
				require.NoError(t, err)
				require.Equal(t, "("+filter+") AND level:WARN AND log_source_id:\"inst:\"", got)
			})
		}
		for _, tc := range invalid {
			t.Run(entrance+" invalid "+tc.hint+tc.filter[:min(len(tc.filter), 20)], func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				params := url.Values{entrance: {tc.filter}}
				if entrance == "logsQL" {
					params.Set("advanced", "true")
				}
				c.Request = httptest.NewRequest(http.MethodGet, "/?"+params.Encode(), nil)
				got, err := federationFilter(c)
				require.ErrorContains(t, err, tc.hint)
				require.Empty(t, got)
			})
		}
	}
}

func TestAdvancedQueryCannotSelectTextSourcesOrAmbiguousInput(t *testing.T) {
	for _, query := range []string{
		"advanced=true&logsQL=*&source=worker",
		"advanced=true&logsQL=*&source=control_plane",
		"advanced=true&logsQL=*&view=platform",
		"advanced=true&logsQL=*&keyword=conflicting",
		"advanced=false&logsQL=*",
		"advanced=true&logsQL=",
	} {
		t.Run(query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)
			_, err := federationFilter(c)
			require.Error(t, err)
		})
	}
}

// Inspect the request before any fake result can hide a widening of scope.
func TestAdvancedQueryHTTPKeepsAuthorizationTimeAndBudget(t *testing.T) {
	resolver := &fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline}}}
	worker := &advancedBudgetWorker{fedWorker: fedWorker{targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "1", "1")}}}
	coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": worker}})
	r, _ := setupFederationRouter(t, coord)
	token := getAdminToken(t, r)
	params := url.Values{
		"advanced": {"true"}, "logsQL": {`log_source_id:="inst:2/file" OR _time:>now-180d`},
		"instanceId": {"1"}, "from": {"2026-09-20T00:00:00Z"}, "to": {"2026-09-21T00:00:00Z"},
		"limit": {"99999999"}, "timeoutMs": {"99999999"}, "maxBytes": {"999999999999"}, "maxFanout": {"99999999"},
	}
	resp := makeRequest(r, http.MethodGet, "/api/v1/logs/federation/search?"+params.Encode(), nil, token)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	req := worker.last
	require.Equal(t, []string{"inst:1"}, req.TargetIDs)
	require.Equal(t, params.Get("from"), req.FromUTC)
	require.Equal(t, params.Get("to"), req.ToUTC)
	require.Contains(t, req.Filter, `log_source_id:"inst:"`)
	// Coordinator reserves a single lookahead event for safe pagination.
	require.LessOrEqual(t, req.Budget.Limit, 1001)
	require.Positive(t, req.Budget.TimeoutMS)
	require.LessOrEqual(t, req.Budget.TimeoutMS, uint32(30000))
	require.Positive(t, req.Budget.MaxBytes)
	require.LessOrEqual(t, req.Budget.MaxBytes, uint64(32<<20))
	require.Positive(t, req.Budget.MaxFanout)
	require.LessOrEqual(t, req.Budget.MaxFanout, uint32(64))
	// The response exposes limits for the caller, not guessed frontend values.
	var body struct {
		QueryMeta map[string]any `json:"query_meta"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
	require.NotEmpty(t, body.QueryMeta)
}

type advancedBudgetWorker struct {
	fedWorker
	last logcoord.WorkerSearchRequest
}

func (w *advancedBudgetWorker) Search(ctx context.Context, req logcoord.WorkerSearchRequest) (*logcoord.WorkerSearchResponse, error) {
	w.last = req
	return w.fedWorker.Search(ctx, req)
}

func TestAdvancedQueryDeadlineReachesWorker(t *testing.T) {
	worker := &advancedDeadlineWorker{}
	resolver := &fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline}}}
	coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": worker}})
	r, _ := setupFederationRouter(t, coord)
	token := getAdminToken(t, r)
	resp := makeRequest(r, http.MethodGet, "/api/v1/logs/federation/search?instanceId=1&advanced=true&logsQL=*&timeoutMs=10", nil, token)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.True(t, worker.cancelled, "HTTP timeout must cancel the actual Worker read")
	require.Contains(t, resp.Body.String(), "timeout")
}

type advancedDeadlineWorker struct {
	fedWorker
	cancelled bool
}

func (w *advancedDeadlineWorker) Search(ctx context.Context, req logcoord.WorkerSearchRequest) (*logcoord.WorkerSearchResponse, error) {
	select {
	case <-ctx.Done():
		w.cancelled = true
		return nil, ctx.Err()
	case <-time.After(time.Second):
		return w.fedWorker.Search(ctx, req)
	}
}

func TestAdvancedQueryPageExportAndViewIdentity(t *testing.T) {
	router, token, scope, _, _ := cpPageExportFixture(t)
	base := scope + "&advanced=true&logsQL=*&limit=1"
	first := makeRequest(router, http.MethodGet, "/api/v1/logs/federation/search?"+base, nil, token)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	body := parseJSON(t, first)
	viewID := body["view"].(map[string]any)["view_id"].(string)
	page := "/api/v1/logs/federation/export?" + base + "&range=page&viewId=" + url.QueryEscape(viewID)
	exported := makeRequest(router, http.MethodPost, page, nil, token)
	require.Equal(t, http.StatusOK, exported.Code, exported.Body.String())
	require.Equal(t, "application/x-ndjson", exported.Header().Get("Content-Type"))
	changed := strings.Replace(page, "logsQL=*", "logsQL=level%3AERROR", 1)
	mismatch := makeRequest(router, http.MethodPost, changed, nil, token)
	require.Equal(t, http.StatusConflict, mismatch.Code, mismatch.Body.String())
	outsideTime := strings.Replace(page, "from=2026-09-22", "from=2026-09-21", 1)
	mismatch = makeRequest(router, http.MethodPost, outsideTime, nil, token)
	require.Equal(t, http.StatusConflict, mismatch.Code, mismatch.Body.String())
}

func TestAdvancedQueryBudgetInputsMustBePositive(t *testing.T) {
	for _, value := range []string{"maxBytes=-1", "maxFanout=0", "timeoutMs=0", "timeoutMs=NaN"} {
		t.Run(value, func(t *testing.T) {
			worker := &fedWorker{}
			coord := logcoord.New(&fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline}}}, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": worker}})
			r, _ := setupFederationRouter(t, coord)
			token := getAdminToken(t, r)
			resp := makeRequest(r, http.MethodGet, "/api/v1/logs/federation/search?advanced=true&logsQL=*&"+value, nil, token)
			require.Equal(t, http.StatusBadRequest, resp.Code, resp.Body.String())
			require.Contains(t, resp.Body.String(), "正整数")
			require.Zero(t, worker.searchN)
		})
	}
}

func TestAdvancedQueryHTTPDisconnectCancelsRead(t *testing.T) {
	worker := &advancedCancelWorker{started: make(chan struct{}), stopped: make(chan struct{})}
	coord := logcoord.New(&fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline}}}, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": worker}})
	r, _ := setupFederationRouter(t, coord)
	token := getAdminToken(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/federation/search?advanced=true&logsQL=*&instanceId=1", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() { r.ServeHTTP(response, req); close(finished) }()
	select {
	case <-worker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("query never reached Worker")
	}
	cancel()
	select {
	case <-worker.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP cancellation did not release the Worker read")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("handler failed to return after cancellation")
	}
}

type advancedCancelWorker struct {
	fedWorker
	started chan struct{}
	stopped chan struct{}
}

func (w *advancedCancelWorker) Search(ctx context.Context, _ logcoord.WorkerSearchRequest) (*logcoord.WorkerSearchResponse, error) {
	close(w.started)
	<-ctx.Done()
	close(w.stopped)
	return nil, ctx.Err()
}

func TestAdvancedQueryMemberScopeCannotBeBroadenedByExpression(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: 7, InstanceID: 11}).Error)
	handler := &LogFederationHandler{authz: service.NewAuthzService(db)}
	access := &service.UserAccess{UserID: 44, AccessibleGroups: map[uint]struct{}{7: {}}}
	for _, expression := range []string{
		`* OR log_source_id:="inst:99/file"`,
		`(log_source_id:="inst:99/file") OR (_time:>now-180d)`,
		`NOT log_source_id:="inst:11/file" OR *`,
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set(middleware.CtxAccess, access)
		params := url.Values{"view": {"node_instance"}, "advanced": {"true"}, "logsQL": {expression},
			"from": {"2026-09-20T00:00:00Z"}, "to": {"2026-09-21T00:00:00Z"}}
		c.Request = httptest.NewRequest(http.MethodGet, "/?"+params.Encode(), nil)
		query, ok := handler.buildQuery(c)
		require.True(t, ok)
		require.Equal(t, []string{"inst:11"}, query.AuthorizedTargetIDs)
		require.Equal(t, params.Get("from"), query.FromUTC)
		require.Equal(t, params.Get("to"), query.ToUTC)
		require.NotEmpty(t, query.PermissionScope)
		require.NotEmpty(t, query.PrincipalKey)
	}
	for _, suffix := range []string{"&instanceId=99", "&nodeId=1"} {
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Set(middleware.CtxAccess, access)
		c.Request = httptest.NewRequest(http.MethodGet, "/?advanced=true&logsQL=*"+suffix, nil)
		_, ok := handler.buildQuery(c)
		require.False(t, ok)
		require.Equal(t, http.StatusForbidden, response.Code)
	}
}
