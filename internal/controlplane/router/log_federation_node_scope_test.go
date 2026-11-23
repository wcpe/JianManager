package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// nodeScopeResolver 故意不展开 node:<id>：该标识是节点自身日志目标，
// HTTP 层必须先把实例来源的节点筛选收敛为可信 DB 中的 inst:<id> 集合。
// 空授权和 * 同生产 resolver 一样展开全集，防止空节点退化为全平台查询。
type nodeScopeResolver struct {
	mu      sync.Mutex
	targets map[string]logcoord.TargetInfo
	calls   [][]string
}

func (r *nodeScopeResolver) Resolve(_ context.Context, authorized []string) ([]logcoord.TargetInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), authorized...))
	all := len(authorized) == 0
	for _, id := range authorized {
		all = all || id == "*"
	}
	ids := append([]string(nil), authorized...)
	if all {
		ids = nil
		for id := range r.targets {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]logcoord.TargetInfo, 0, len(ids))
	for _, id := range ids {
		if target, ok := r.targets[id]; ok {
			out = append(out, target)
		} else {
			out = append(out, logcoord.TargetInfo{ID: id, Readiness: logcoord.ReadyNotReady})
		}
	}
	return out, nil
}

func (r *nodeScopeResolver) takeCalls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.calls
	r.calls = nil
	return out
}

// 其它 RPC 沿用已有替身；OpenView/Search 必须按请求 TargetIDs 取同源行，
// 不能不论目标是什么都返回实例日志，否则会把这次真实反例测成绿色。
type nodeScopeWorker struct {
	fedWorker
	mu       sync.Mutex
	requests [][]string
}

func (w *nodeScopeWorker) OpenView(ctx context.Context, req logcoord.WorkerSearchRequest) (*logcoord.WorkerSearchResponse, error) {
	return w.Search(ctx, req)
}

func (w *nodeScopeWorker) Search(_ context.Context, req logcoord.WorkerSearchRequest) (*logcoord.WorkerSearchResponse, error) {
	w.mu.Lock()
	w.requests = append(w.requests, append([]string(nil), req.TargetIDs...))
	w.mu.Unlock()
	resp := &logcoord.WorkerSearchResponse{
		ViewID: "node-scope-worker-view", Exhausted: true,
		Quality: logcoord.Quality{DuplicateQuality: logcoord.DupExact, StatsQuality: logcoord.StatsQExact},
	}
	for _, target := range req.TargetIDs {
		resp.Targets = append(resp.Targets, fedSuccess(target, "", target+"/file/g:1", "1"))
		if strings.Contains(req.Filter, `log_source_id:"inst:"`) && !strings.HasPrefix(target, "inst:") {
			continue
		}
		if strings.Contains(req.Filter, `log_source_id:"node:"`) && !strings.HasPrefix(target, "node:") {
			continue
		}
		resp.Items = append(resp.Items, fedEvent("node-scope-"+target, target+"/file", "g", 1,
			"2026-09-20T12:00:00Z", "INFO", "line from "+target))
	}
	return resp, nil
}

func (w *nodeScopeWorker) takeRequests() [][]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.requests
	w.requests = nil
	return out
}

// 真 Router + DB 的节点范围回归：同一节点有两实例，另一个节点有一实例，
// 另加无实例节点，避免 ID 恰巧相同或全量替身掩盖范围错误。
func TestLogFederation_NodeScopeHTTP(t *testing.T) {
	for _, endpoint := range []string{"/api/v1/logs/federation", "/api/v1/logs/federation/search"} {
		t.Run(strings.TrimPrefix(endpoint, "/api/v1/logs/"), func(t *testing.T) {
			resolver := &nodeScopeResolver{targets: make(map[string]logcoord.TargetInfo)}
			workers := []*nodeScopeWorker{{}, {}, {}}
			dialer := &fedDialer{clients: map[string]logcoord.WorkerClient{
				"scope-worker-1": workers[0], "scope-worker-2": workers[1], "scope-worker-empty": workers[2],
			}}
			router, db := setupFederationRouter(t, logcoord.New(resolver, dialer))
			token := getAdminToken(t, router)
			nodes := []model.Node{
				{UUID: "scope-worker-1", Name: "scope-node-1", Status: model.NodeStatusOnline},
				{UUID: "scope-worker-2", Name: "scope-node-2", Status: model.NodeStatusOnline},
				{UUID: "scope-worker-empty", Name: "scope-node-empty", Status: model.NodeStatusOnline},
			}
			for i := range nodes {
				require.NoError(t, db.Create(&nodes[i]).Error)
				id := fmt.Sprintf("node:%d", nodes[i].ID)
				resolver.targets[id] = logcoord.TargetInfo{ID: id, AuthorizationID: id, WorkerID: nodes[i].UUID, Readiness: logcoord.ReadyOnline}
			}
			var own []string
			var foreign string
			for i, nodeIndex := range []int{0, 0, 1} {
				inst := model.Instance{UUID: fmt.Sprintf("scope-inst-%d", i), Name: fmt.Sprintf("scope-inst-%d", i),
					NodeID: nodes[nodeIndex].ID, Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true"}
				require.NoError(t, db.Create(&inst).Error)
				id := fmt.Sprintf("inst:%d", inst.ID)
				resolver.targets[id] = logcoord.TargetInfo{ID: id, AuthorizationID: id, WorkerID: nodes[nodeIndex].UUID, Readiness: logcoord.ReadyOnline}
				if nodeIndex == 0 {
					own = append(own, id)
				} else {
					foreign = id
				}
			}
			nodeTarget := fmt.Sprintf("node:%d", nodes[0].ID)
			emptyTarget := fmt.Sprintf("node:%d", nodes[2].ID)
			aggregated := append([]string{nodeTarget}, own...)
			cases := []struct {
				name       string
				source     string
				nodeID     uint
				wantTarget []string
				wantRows   []string
			}{
				{"instance_source", "instance", nodes[0].ID, own, own},
				{"worker_source", "worker", nodes[0].ID, []string{nodeTarget}, []string{nodeTarget}},
				{"omitted_source_aggregates", "", nodes[0].ID, aggregated, aggregated},
				{"all_source_aggregates", "all", nodes[0].ID, aggregated, aggregated},
				{"empty_node_does_not_expand_all", "instance", nodes[2].ID, []string{emptyTarget}, nil},
			}
			for i, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					resolver.takeCalls()
					for _, worker := range workers {
						worker.takeRequests()
					}
					// 各用例独立绝对窗口，避免已有 Search 缓存跳过目标解析断言。
					params := url.Values{"view": {"node_instance"}, "nodeId": {fmt.Sprint(tc.nodeID)}, "limit": {"100"},
						"from": {"2026-09-20T00:00:00Z"}, "to": {fmt.Sprintf("2026-09-20T23:59:%02dZ", i)}}
					if tc.source != "" {
						params.Set("source", tc.source)
					}
					response := makeRequest(router, http.MethodGet, endpoint+"?"+params.Encode(), nil, token)
					require.Equal(t, http.StatusOK, response.Code, "body=%s", response.Body.String())
					calls := resolver.takeCalls()
					require.NotEmpty(t, calls, "请求必须经授权目标解析")
					for _, authorized := range calls {
						assert.ElementsMatch(t, tc.wantTarget, authorized, "HTTP 层须按来源把节点筛选展开为授权目标")
						assert.NotContains(t, authorized, "*", "节点范围不得退化为全平台")
						assert.NotContains(t, authorized, foreign, "不得纳入其它节点的实例")
					}
					var actualTargets []string
					for _, worker := range workers {
						for _, request := range worker.takeRequests() {
							actualTargets = append(actualTargets, request...)
						}
					}
					assert.ElementsMatch(t, tc.wantTarget, actualTargets, "Worker 必须收到同一授权集合")
					var body logcoord.SearchResponse
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
					var sources []string
					for _, row := range body.Items {
						sources = append(sources, strings.TrimSuffix(row.LogSourceID, "/file"))
					}
					assert.ElementsMatch(t, tc.wantRows, sources, "响应只能包含所选节点及来源的同源行")
					for _, coverage := range body.Coverage.Targets {
						assert.NotContains(t, coverage.Reasons, "TARGET_NO_DATA", "有实例日志的节点不得被误查为节点自身无数据")
					}
				})
			}

			t.Run("member_node_only_forbidden", func(t *testing.T) {
				authz := service.NewAuthzService(db)
				member, err := service.NewUserService(db).Create("node_scope_member", "password123", model.RoleMember, model.UserStatusActive)
				require.NoError(t, err)
				role, err := authz.Permissions().CreateRole("node-scope-log-read", "", []string{"log.read"})
				require.NoError(t, err)
				require.NoError(t, authz.Permissions().BindUserRole(member.ID, role.ID))
				memberToken := loginUserToken(t, router, "node_scope_member", "password123")
				resolver.takeCalls()
				response := makeRequest(router, http.MethodGet, endpoint+"?source=instance&nodeId="+fmt.Sprint(nodes[0].ID), nil, memberToken)
				require.Equal(t, http.StatusForbidden, response.Code, "body=%s", response.Body.String())
				assert.Empty(t, resolver.takeCalls(), "授权拒绝不得进入日志目标解析")
			})
		})
	}
}

func TestLogFederation_NodeScopeDirectoryFailures(t *testing.T) {
	access := &service.UserAccess{IsPlatformAdmin: true}
	contextFor := func(source string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/logs/federation?nodeId=1&source="+source, nil)
		return c, w
	}

	t.Run("missing_directory_fails_closed", func(t *testing.T) {
		h := &LogFederationHandler{}
		c, w := contextFor("instance")
		targets, ok := h.authorizedTargets(c, access)
		assert.False(t, ok)
		assert.Nil(t, targets)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Contains(t, w.Body.String(), "ENGINE_NOT_READY")
	})

	t.Run("directory_read_failure_does_not_fall_back_to_all", func(t *testing.T) {
		db := setupTestDB(t)
		instances := service.NewInstanceService(db, nil, nil)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
		h := &LogFederationHandler{instances: instances}
		c, w := contextFor("instance")
		targets, ok := h.authorizedTargets(c, access)
		assert.False(t, ok)
		assert.Nil(t, targets)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.NotContains(t, w.Body.String(), "database is closed")
	})

	t.Run("worker_source_does_not_need_instance_directory", func(t *testing.T) {
		h := &LogFederationHandler{}
		c, w := contextFor("worker")
		targets, ok := h.authorizedTargets(c, access)
		assert.True(t, ok)
		assert.Equal(t, []string{"node:1"}, targets)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}
