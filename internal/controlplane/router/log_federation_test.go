package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// F-009：writeFederationError HTTP 状态映射回归（不得回落 500）。
func TestWriteFederationError_HTTPStatusMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"authz → 403", fmt.Errorf("%w: principal mismatch", logcoord.ErrViewAuthz), http.StatusForbidden, "FORBIDDEN"},
		{"reuse mismatch → 409 VIEW_STALE", fmt.Errorf("%w: filter mismatch", logcoord.ErrViewReuseMismatch), http.StatusConflict, "VIEW_STALE"},
		{"view not found → 400", errors.New("logcoord: view not found: x"), http.StatusBadRequest, "INVALID_REQUEST"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			writeFederationError(c, tc.err)
			assert.Equal(t, tc.wantStatus, w.Code)
			assert.Contains(t, w.Body.String(), tc.wantCode)
			assert.NotEqual(t, http.StatusInternalServerError, w.Code)
		})
	}
}

// F-008/F-007/F-010 集成：View 复用时条件变化 → 409；跨主体 viewId → 403。
// 必须在构造 Router 前注入 testLogCoord（禁止 t.Skip 绕过）。
func TestLogFederation_ViewReuseHTTPStatus(t *testing.T) {
	resolver := &fedResolver{targets: []logcoord.TargetInfo{
		{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
	}}
	w1 := &fedWorker{
		items: []logcoord.Event{{
			EventID: "e1", LogSourceID: "s", SourceGeneration: "g",
			EventTimeUTC: "2026-09-20T12:00:00Z", Level: "info", Message: "hello",
		}},
		targets: []logcoord.WorkerTargetResult{{
			TargetID: "inst:1",
			State:    logcoord.CoverageSuccess,
		}},
	}
	dialer := &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}}
	coord := logcoord.New(resolver, dialer)
	// F-010：先注入再 setup，路由才会挂载联邦端点。
	r, _ := setupFederationRouter(t, coord)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/logs/federation/search?instanceId=1", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, "federation search must be mounted, body=%s", w.Body.String())
	body := parseJSON(t, w)
	viewObj, _ := body["view"].(map[string]interface{})
	viewID, _ := viewObj["view_id"].(string)
	require.NotEmpty(t, viewID)

	// 变更时间范围复用同一 viewId → 409 VIEW_STALE
	w2 := makeRequest(r, "GET", "/api/v1/logs/federation/search?instanceId=1&viewId="+viewID+"&from=2020-01-01T00:00:00Z&to=2020-01-02T00:00:00Z", nil, adminToken)
	require.Equal(t, http.StatusConflict, w2.Code, "body=%s", w2.Body.String())
	assert.Contains(t, fmt.Sprint(parseJSON(t, w2)["error"]), "VIEW_STALE")

	// 伪造其他主体创建的 view 再带当前 token 复用 → 403
	foreign, err := coord.CreateView(context.Background(), logcoord.Query{
		AuthorizedTargetIDs: []string{"inst:1"},
		PrincipalKey:        "user:999|role:0|targets:other",
		PermissionScope:     "scoped|role:x|auth:0|nodes:0|targets:0",
		Budget:              logcoord.QueryBudget{Limit: 10},
	})
	require.NoError(t, err)
	w3 := makeRequest(r, "GET", "/api/v1/logs/federation/search?instanceId=1&viewId="+foreign.ViewID, nil, adminToken)
	require.Equal(t, http.StatusForbidden, w3.Code, "body=%s", w3.Body.String())
	assert.Contains(t, fmt.Sprint(parseJSON(t, w3)["error"]), "FORBIDDEN")
}

// F-008：PermissionScope 摘要稳定且随权限/目标变化。
func TestFederationPermissionScopeDigest(t *testing.T) {
	a := &service.UserAccess{UserID: 1, Role: 10, RoleKey: "admin", AuthVersion: 0, IsPlatformAdmin: true, Nodes: map[string]struct{}{"log.read": {}}}
	b := &service.UserAccess{UserID: 1, Role: 10, RoleKey: "admin", AuthVersion: 1, IsPlatformAdmin: true, Nodes: map[string]struct{}{"log.read": {}}}
	sa := federationPermissionScope(a, []string{"inst:1"})
	sb := federationPermissionScope(b, []string{"inst:1"})
	assert.NotEmpty(t, sa)
	assert.NotEqual(t, sa, sb, "AuthVersion change must change scope digest")
	assert.Equal(t, sa, federationPermissionScope(a, []string{"inst:1"}))
}

// fedResolver 静态目标注册表：忽略入参，返回固定目标（HTTP 层授权收敛后的目标集合）。
type fedResolver struct {
	targets []logcoord.TargetInfo
}

func (f *fedResolver) Resolve(_ context.Context, _ []string) ([]logcoord.TargetInfo, error) {
	return append([]logcoord.TargetInfo(nil), f.targets...), nil
}

// fedWorker 单 Worker 可编程日志查询面。
type fedWorker struct {
	items   []logcoord.Event
	targets []logcoord.WorkerTargetResult
	trunc   bool
	unsup   bool
	fail    string
	searchN int
}

func (w *fedWorker) OpenView(ctx context.Context, req logcoord.WorkerSearchRequest) (*logcoord.WorkerSearchResponse, error) {
	resp, err := w.Search(ctx, req)
	if resp != nil {
		resp.ViewID = "worker-view-test"
	}
	return resp, err
}

func (w *fedWorker) Search(_ context.Context, req logcoord.WorkerSearchRequest) (*logcoord.WorkerSearchResponse, error) {
	w.searchN++
	return &logcoord.WorkerSearchResponse{
		ViewID:      req.ViewID,
		Items:       append([]logcoord.Event(nil), w.items...),
		Targets:     append([]logcoord.WorkerTargetResult(nil), w.targets...),
		Exhausted:   !w.trunc,
		Truncated:   w.trunc,
		Unsupported: w.unsup,
		Error:       w.fail,
	}, nil
}

func (w *fedWorker) Tail(ctx context.Context, req logcoord.WorkerSearchRequest, _ string) (*logcoord.WorkerSearchResponse, error) {
	return w.Search(ctx, req)
}

func (w *fedWorker) Fields(_ context.Context, req logcoord.WorkerSearchRequest) (*logcoord.WorkerFieldsResponse, error) {
	return &logcoord.WorkerFieldsResponse{ViewID: req.ViewID, Fields: []string{"level", "stream"},
		Targets: append([]logcoord.WorkerTargetResult(nil), w.targets...)}, nil
}

func (w *fedWorker) Stats(_ context.Context, req logcoord.WorkerSearchRequest, _ []string, _ string) (*logcoord.WorkerStatsResponse, error) {
	return &logcoord.WorkerStatsResponse{
		ViewID:  req.ViewID,
		Targets: append([]logcoord.WorkerTargetResult(nil), w.targets...),
	}, nil
}

func (w *fedWorker) Facets(_ context.Context, req logcoord.WorkerSearchRequest, _ []string, _ uint32) (*logcoord.WorkerFacetsResponse, error) {
	return &logcoord.WorkerFacetsResponse{
		ViewID:  req.ViewID,
		Targets: append([]logcoord.WorkerTargetResult(nil), w.targets...),
	}, nil
}

// fedDialer workerID → client。
type fedDialer struct {
	clients map[string]logcoord.WorkerClient
	fail    map[string]error
}

func (d *fedDialer) Dial(_ context.Context, workerID string) (logcoord.WorkerClient, error) {
	if err, ok := d.fail[workerID]; ok && err != nil {
		return nil, err
	}
	c, ok := d.clients[workerID]
	if !ok {
		return nil, fmt.Errorf("unknown worker %s", workerID)
	}
	return c, nil
}

func fedSuccess(id, workerID, cvs, gen string) logcoord.WorkerTargetResult {
	return logcoord.WorkerTargetResult{
		TargetID:          id,
		State:             logcoord.CoverageSuccess,
		ClosedVisibleSeq:  cvs,
		CatalogGeneration: gen,
	}
}

func fedEvent(id, source, gen string, start uint64, eventTime, level, msg string) logcoord.Event {
	return logcoord.Event{
		EventID:          id,
		LogSourceID:      source,
		SourceGeneration: gen,
		RecordStart:      start,
		RecordEnd:        start + 1,
		EventTimeUTC:     eventTime,
		Level:            level,
		Message:          msg,
	}
}

// setupFederationRouter 构建带 logcoord 联邦端点的测试路由（经 testLogCoord 注入）。
func setupFederationRouter(t *testing.T, coord *logcoord.Coordinator) (*gin.Engine, *gorm.DB) {
	t.Helper()
	testLogCoord = coord
	t.Cleanup(func() { testLogCoord = nil })
	db := setupTestDB(t)
	r := setupTestRouter(db)
	return r, db
}

func TestLogFederation_Search_PartialCoverageJSON(t *testing.T) {
	on, off := "inst:1", "inst:2"
	resolver := &fedResolver{targets: []logcoord.TargetInfo{
		{ID: on, WorkerID: "w-on", Readiness: logcoord.ReadyOnline},
		{ID: off, WorkerID: "w-off", Readiness: logcoord.ReadyOffline, HistoricalHolder: true},
	}}
	wOn := &fedWorker{
		items: []logcoord.Event{
			fedEvent("e1", "src-on", "g", 1, "2026-09-20T12:00:00Z", "INFO", "ok line"),
		},
		targets: []logcoord.WorkerTargetResult{
			fedSuccess(on, "w-on", "src-on/g:1", "1"),
		},
	}
	dialer := &fedDialer{
		clients: map[string]logcoord.WorkerClient{"w-on": wOn},
		fail:    map[string]error{"w-off": errors.New("tunnel closed")},
	}
	coord := logcoord.New(resolver, dialer)

	r, _ := setupFederationRouter(t, coord)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/logs/federation/search?limit=50", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	body := parseJSON(t, w)
	cov, ok := body["coverage"].(map[string]interface{})
	require.True(t, ok, "coverage missing: %v", body)
	assert.Equal(t, false, cov["complete"], "partial coverage must not claim complete")

	targets, _ := cov["targets"].([]interface{})
	require.Len(t, targets, 2)
	byID := map[string]map[string]interface{}{}
	for _, raw := range targets {
		tc := raw.(map[string]interface{})
		byID[tc["target_id"].(string)] = tc
	}
	require.Contains(t, byID, on)
	require.Contains(t, byID, off)
	assert.Equal(t, "success", byID[on]["state"])
	assert.Equal(t, "offline", byID[off]["state"])

	// View 固化 closed_visible_seq 向量
	view, _ := body["view"].(map[string]interface{})
	require.NotNil(t, view)
	cvs, _ := view["closed_visible_seq"].(map[string]interface{})
	require.NotNil(t, cvs, "view.closed_visible_seq must be persisted after search")
	assert.Equal(t, "src-on/g:1", cvs[on])

	items, _ := body["items"].([]interface{})
	assert.NotEmpty(t, items)
}

func TestLogFederation_Export_IncompleteNoArtifact(t *testing.T) {
	on, off := "inst:1", "inst:2"
	resolver := &fedResolver{targets: []logcoord.TargetInfo{
		{ID: on, WorkerID: "w-on", Readiness: logcoord.ReadyOnline},
		{ID: off, WorkerID: "w-off", Readiness: logcoord.ReadyNotReady},
	}}
	wOn := &fedWorker{
		items: []logcoord.Event{
			fedEvent("e1", "src-on", "g", 1, "2026-09-20T12:00:00Z", "INFO", "partial"),
		},
		targets: []logcoord.WorkerTargetResult{
			fedSuccess(on, "w-on", "src-on/g:1", "1"),
		},
	}
	dialer := &fedDialer{
		clients: map[string]logcoord.WorkerClient{"w-on": wOn},
		fail:    map[string]error{"w-off": errors.New("not ready")},
	}
	coord := logcoord.New(resolver, dialer)

	r, _ := setupFederationRouter(t, coord)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/logs/federation/export", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	// 不完整导出：JSON incomplete 标记，无成功附件
	assert.NotContains(t, w.Header().Get("Content-Type"), "application/x-ndjson")
	assert.Empty(t, w.Header().Get("Content-Disposition"))

	body := parseJSON(t, w)
	assert.Equal(t, true, body["export_incomplete"])
	assert.Nil(t, body["artifact"], "incomplete export must not deliver artifact")
	reasons, _ := body["incomplete_reasons"].([]interface{})
	assert.NotEmpty(t, reasons)
}

func TestLogFederation_Export_BlocksUnresolvedQuality(t *testing.T) {
	// partial → duplicate_quality=unresolved → export 阻断
	on, off := "inst:1", "inst:2"
	resolver := &fedResolver{targets: []logcoord.TargetInfo{
		{ID: on, WorkerID: "w-on", Readiness: logcoord.ReadyOnline},
		{ID: off, WorkerID: "w-off", Readiness: logcoord.ReadyOnline},
	}}
	wOn := &fedWorker{
		items: []logcoord.Event{
			fedEvent("e1", "src-on", "g", 1, "2026-09-20T12:00:00Z", "INFO", "ok"),
		},
		targets: []logcoord.WorkerTargetResult{
			fedSuccess(on, "w-on", "src-on/g:1", "1"),
		},
	}
	wOff := &fedWorker{
		// partial 覆盖 → quality unresolved
		targets: []logcoord.WorkerTargetResult{{
			TargetID: off,
			State:    logcoord.CoveragePartial,
			Reasons:  []string{"worker_truncated"},
		}},
	}
	dialer := &fedDialer{
		clients: map[string]logcoord.WorkerClient{"w-on": wOn, "w-off": wOff},
	}
	coord := logcoord.New(resolver, dialer)

	r, _ := setupFederationRouter(t, coord)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/logs/federation/export", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, true, body["export_incomplete"])
	assert.Nil(t, body["artifact"])
}

func TestLogFederation_Unauthorized403_NoToken(t *testing.T) {
	coord := logcoord.New(&fedResolver{}, &fedDialer{})
	r, _ := setupFederationRouter(t, coord)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/logs/federation/search"},
		{"GET", "/api/v1/logs/federation/stats"},
		{"GET", "/api/v1/logs/federation/facets?dimensions=level"},
		{"POST", "/api/v1/logs/federation/export"},
	} {
		w := makeRequest(r, tc.method, tc.path, nil, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code, "%s %s", tc.method, tc.path)
	}
}

func TestLogFederation_Unauthorized403_NoLogRead(t *testing.T) {
	coord := logcoord.New(&fedResolver{targets: []logcoord.TargetInfo{
		{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
	}}, &fedDialer{clients: map[string]logcoord.WorkerClient{
		"w1": &fedWorker{targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "1", "1")}},
	}})
	r, db := setupFederationRouter(t, coord)

	// 无 log.read 节点的成员 → RequireAnyPerm 403
	authz := service.NewAuthzService(db)
	empty, err := service.NewUserService(db).Create("fed_noperm", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	emptyRole, err := authz.Permissions().CreateRole("fed-empty", "none", nil)
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(empty.ID, emptyRole.ID))
	tok := loginUserToken(t, r, "fed_noperm", "password123")

	w := makeRequest(r, "GET", "/api/v1/logs/federation/search", nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, "FORBIDDEN", body["error"])
}

func TestLogFederation_Unauthorized403_CrossInstanceScope(t *testing.T) {
	coord := logcoord.New(&fedResolver{targets: []logcoord.TargetInfo{
		{ID: "inst:99", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
	}}, &fedDialer{clients: map[string]logcoord.WorkerClient{
		"w1": &fedWorker{targets: []logcoord.WorkerTargetResult{fedSuccess("inst:99", "w1", "1", "1")}},
	}})
	r, db := setupFederationRouter(t, coord)

	authz := service.NewAuthzService(db)
	// 有 log.read 但无可访问实例，请求任意 instanceId → 403
	member, err := service.NewUserService(db).Create("fed_lonely", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	role, err := authz.Permissions().CreateRole("fed-log-read", "", []string{"log.read"})
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(member.ID, role.ID))
	tok := loginUserToken(t, r, "fed_lonely", "password123")

	w := makeRequest(r, "GET", "/api/v1/logs/federation/search?instanceId=99", nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
}

func TestLogFederation_AdminSearchOK_WithKeywordAndLimits(t *testing.T) {
	resolver := &fedResolver{targets: []logcoord.TargetInfo{
		{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
	}}
	w1 := &fedWorker{
		items: []logcoord.Event{
			fedEvent("e1", "src", "g", 1, "2026-09-20T12:00:00Z", "INFO", "needle in haystack"),
		},
		targets: []logcoord.WorkerTargetResult{
			fedSuccess("inst:1", "w1", "src/g:1", "1"),
		},
	}
	coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}})
	r, _ := setupFederationRouter(t, coord)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/logs/federation/search?keyword=needle&limit=10&onlineOnly=false", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	cov := body["coverage"].(map[string]interface{})
	assert.Equal(t, true, cov["complete"])
	items := body["items"].([]interface{})
	require.Len(t, items, 1)
	assert.Equal(t, "e1", items[0].(map[string]interface{})["event_id"])
}

func TestLogFederation_StatsAndFacets_AdminOK(t *testing.T) {
	resolver := &fedResolver{targets: []logcoord.TargetInfo{
		{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
	}}
	w1 := &fedWorker{
		targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "src/g:1", "1")},
	}
	coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}})
	r, _ := setupFederationRouter(t, coord)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/logs/federation/stats?groupBy=level&timeBucket=1m", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	body := parseJSON(t, w)
	assert.Contains(t, body, "coverage")
	assert.Contains(t, body, "view")

	w = makeRequest(r, "GET", "/api/v1/logs/federation/facets?dimensions=level&dimensionLimit=10", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	body = parseJSON(t, w)
	assert.Contains(t, body, "dimensions")

	w = makeRequest(r, "GET", "/api/v1/logs/federation/fields", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	body = parseJSON(t, w)
	assert.ElementsMatch(t, []interface{}{"level", "stream"}, body["fields"])

	w = makeRequest(r, "GET", "/api/v1/logs/federation/tail?mode=FOLLOW_LIVE", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	body = parseJSON(t, w)
	assert.Equal(t, false, body["exhausted"])
	cov := body["coverage"].(map[string]interface{})
	assert.Equal(t, "OPEN", cov["enumeration_state"])
}

func TestLogFederation_Export_CompleteSuccessAttachment(t *testing.T) {
	resolver := &fedResolver{targets: []logcoord.TargetInfo{
		{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
	}}
	w1 := &fedWorker{
		items: []logcoord.Event{
			fedEvent("e1", "src", "g", 1, "2026-09-20T12:00:00Z", "INFO", "hello"),
		},
		targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "src/g:1", "1")},
	}
	coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}})
	r, _ := setupFederationRouter(t, coord)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/logs/federation/export?limit=100", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "application/x-ndjson")
	assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
	assert.Contains(t, w.Body.String(), `"e1"`)
}
