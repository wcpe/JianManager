package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-444 拉取端点的路由层测试。核心断言是「可选协同」边界：
// 未配置 beacon.endpoint 时端点仍注册但返回 503 明确提示（不静默成功、不 500），
// 且不影响平台其余功能。

// TestBeaconTopology_DisabledWhenNotConfigured 未配置协同时端点不注册：
// 请求落 404，平台其余功能（如分组树）完全正常——可选协同，绝非依赖（验收 #1/#10）。
//
// 注意：生产装配在未配置 endpoint 时仍会传一个 client=nil 的服务（端点注册但返回 503，
// 便于前端区分「未部署」与「未开启」）；此用例覆盖更彻底的「完全不装配」形态，
// 证明即便端点不存在也不影响任何既有功能。
func TestBeaconTopology_RoutesAbsentWithoutService(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/beacon/topology/status"},
		{"POST", "/api/v1/beacon/topology/pull"},
	} {
		w := makeRequest(r, tc.method, tc.path, nil, token)
		if w.Code != http.StatusNotFound {
			t.Fatalf("未装配服务时 %s %s 应 404，得 %d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	// 分组树功能独立可用（未部署 Beacon 时本平台能力不受影响）。
	w := makeRequest(r, "GET", "/api/v1/instance-groups", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("未部署 Beacon 时分组树应可用，得 %d body=%s", w.Code, w.Body.String())
	}
	w = makeRequest(r, "POST", "/api/v1/instance-groups", map[string]interface{}{"name": "亚洲区"}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("未部署 Beacon 时应能建分组，得 %d body=%s", w.Code, w.Body.String())
	}
}

// TestBeaconTopology_DisabledWhenPullNotEnabled 配了端点但未开 pull-enabled：
// configured=true 但 enabled=false，pull 仍 503（配置可见、能力未开）。
func TestBeaconTopology_DisabledWhenPullNotEnabled(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouterWithBeacon(db, "http://127.0.0.1:1", false)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/beacon/topology/status", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("status 应 200，得 %d body=%s", w.Code, w.Body.String())
	}
	status := parseJSON(t, w)
	if status["configured"] != true || status["enabled"] != false {
		t.Fatalf("配了端点未开拉取时应 configured=true/enabled=false，得 %v", status)
	}

	w = makeRequest(r, "POST", "/api/v1/beacon/topology/pull", nil, token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("未开拉取时 pull 应 503，得 %d body=%s", w.Code, w.Body.String())
	}
}

// TestBeaconTopology_UnreachableReturns502 已启用但 Beacon 不可达 → 502，
// 且本地分组树零改动（验收 #8 的路由层投影）。
func TestBeaconTopology_UnreachableReturns502(t *testing.T) {
	db := setupTestDB(t)
	// 127.0.0.1:1 必然连不上。
	r := setupTestRouterWithBeacon(db, "http://127.0.0.1:1", true)
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/beacon/topology/pull", nil, token)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("Beacon 不可达应 502，得 %d body=%s", w.Code, w.Body.String())
	}
	body := parseJSON(t, w)
	if body["error"] != "BEACON_UNREACHABLE" {
		t.Fatalf("错误码应为 BEACON_UNREACHABLE，得 %v", body["error"])
	}

	// 本地分组树应为空（零改动）。
	w = makeRequest(r, "GET", "/api/v1/instance-groups", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("查分组树应 200，得 %d", w.Code)
	}
	if body := w.Body.String(); body != "[]" {
		t.Fatalf("不可达时分组树应保持为空，得 %s", body)
	}
}

// TestBeaconTopology_NoAuthRejected 未带令牌一律拒绝（端点不因可选协同而放开鉴权）。
func TestBeaconTopology_NoAuthRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouterWithBeacon(db, "http://127.0.0.1:1", true)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/beacon/topology/status"},
		{"POST", "/api/v1/beacon/topology/pull"},
	} {
		w := makeRequest(r, tc.method, tc.path, nil, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 无令牌应 401，得 %d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

// TestBeaconTopology_PullEndToEnd 用一个 mock Beacon 走完整 HTTP 路径：
// 拉取 → 建三层分组树 → 实例归组 + 补标签（验收 #6/#7 的端到端投影）。
func TestBeaconTopology_PullEndToEnd(t *testing.T) {
	db := setupTestDB(t)
	node := createTestNode(t, db)
	inst := &model.Instance{
		Name: "r1-z1-g1", NodeID: node.ID, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	if err := db.Create(inst).Error; err != nil {
		t.Fatalf("造实例失败: %v", err)
	}

	srv := newMockBeacon(t)
	r := setupTestRouterWithBeacon(db, srv.URL, true)
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/beacon/topology/pull", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("拉取应 200，得 %d body=%s", w.Code, w.Body.String())
	}
	result := parseJSON(t, w)
	if int(result["clusters"].(float64)) != 1 ||
		int(result["regions"].(float64)) != 1 ||
		int(result["zones"].(float64)) != 1 {
		t.Fatalf("应映射出 1 集群/1 大区/1 小区，得 %v", result)
	}
	if int(result["matchedInstances"].(float64)) != 1 {
		t.Fatalf("应匹配 1 台实例，得 %v", result["matchedInstances"])
	}
	if int(result["createdGroups"].(float64)) != 3 {
		t.Fatalf("应新建 3 个分组，得 %v", result["createdGroups"])
	}

	// 分组树：集群 → 大区 → 小区 三层。
	w = makeRequest(r, "GET", "/api/v1/instance-groups", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("查分组树应 200，得 %d", w.Code)
	}
	tree := parseJSONArray(t, w)
	byName := map[string]map[string]interface{}{}
	for _, raw := range tree {
		n := raw.(map[string]interface{})
		byName[n["name"].(string)] = n
	}
	if len(tree) != 3 {
		t.Fatalf("应有 3 个分组节点，得 %d: %v", len(tree), byName)
	}
	cluster, ok := byName["bc-main"]
	if !ok || cluster["parentId"] != nil {
		t.Fatalf("bc-main 应为根分组，得 %v", cluster)
	}
	region, ok := byName["r1"]
	if !ok || uint(region["parentId"].(float64)) != uint(cluster["id"].(float64)) {
		t.Fatalf("r1 应是 bc-main 的子分组，得 %v", region)
	}
	zone, ok := byName["z1"]
	if !ok || uint(zone["parentId"].(float64)) != uint(region["id"].(float64)) {
		t.Fatalf("z1 应是 r1 的子分组，得 %v", zone)
	}

	// 实例已归入小区分组，且补齐 region:/zone:/role: 标签（isDefaultEntry=true → lobby）。
	w = makeRequest(r, "GET", "/api/v1/instance-groups/"+itoa(uint(zone["id"].(float64)))+"/instances", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("查子树实例应 200，得 %d body=%s", w.Code, w.Body.String())
	}
	ids, _ := parseJSON(t, w)["instanceIds"].([]interface{})
	if len(ids) != 1 || uint(ids[0].(float64)) != inst.ID {
		t.Fatalf("小区分组应含实例 %d，得 %v", inst.ID, ids)
	}
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(inst.ID), nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("查实例应 200，得 %d body=%s", w.Code, w.Body.String())
	}
	detail := parseJSON(t, w)
	tags := fmt.Sprintf("%v", detail["tags"])
	for _, want := range []string{"region:r1", "zone:z1", "role:lobby"} {
		if !strings.Contains(tags, want) {
			t.Fatalf("实例标签应含 %s，得 %v", want, detail["tags"])
		}
	}

	// 再拉一次：幂等，不新建分组（验收 #9）。
	w = makeRequest(r, "POST", "/api/v1/beacon/topology/pull", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("二次拉取应 200，得 %d body=%s", w.Code, w.Body.String())
	}
	if created := parseJSON(t, w)["createdGroups"].(float64); created != 0 {
		t.Fatalf("重复拉取不应新建分组，得 %v", created)
	}
	w = makeRequest(r, "GET", "/api/v1/instance-groups", nil, token)
	if got := len(parseJSONArray(t, w)); got != 3 {
		t.Fatalf("重复拉取后分组数应仍为 3，得 %d", got)
	}
}

// newMockBeacon 起一个返回单集群/单大区/单小区的 mock Beacon。
func newMockBeacon(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(service.BeaconAPIPathZoneTree, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"namespaceId": 1,
			"clusters": []any{map[string]any{
				"id": 1, "code": "bc-main",
				"regions": []any{map[string]any{
					"id": 10, "code": "r1",
					"zones": []any{map[string]any{"id": 100, "code": "z1", "serverCount": 1}},
				}},
			}},
		})
	})
	mux.HandleFunc(service.BeaconAPIPathServers, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"total": 1,
			"items": []any{map[string]any{
				"id": 1, "serverId": "r1-z1-g1", "zoneId": 100,
				"isDefaultEntry": true, "lifecycleStatus": "active",
			}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("编码 mock 响应失败: %v", err)
	}
}
