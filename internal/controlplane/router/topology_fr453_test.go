package router

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// topologyRespFull 是 GET /topology 全量响应的测试镜像，含 FR-452/453 新增的 instances 投影。
type topologyRespFull struct {
	Proxies []struct {
		ID            uint  `json:"id"`
		Registrations []any `json:"registrations"`
	} `json:"proxies"`
	Networks []struct {
		ID                uint   `json:"id"`
		MemberInstanceIDs []uint `json:"memberInstanceIds"`
	} `json:"networks"`
	Instances []struct {
		ID         uint   `json:"id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Status     string `json:"status"`
		NodeID     uint   `json:"nodeId"`
		ServerPort int    `json:"serverPort"`
		Tags       string `json:"tags"`
	} `json:"instances"`
}

// TestFR453TopologyIncludesAllInstances 验证 GET /topology 带出全量实例投影（FR-452/453）：
// 未注册实例（beacon / 独立服务 / 未挂 BC 的后端）也出现在 instances 里，而 proxies 仍只含已注册代理。
func TestFR453TopologyIncludesAllInstances(t *testing.T) {
	db := setupTestDB(t)
	r := setupFR032Router(t, db)
	adminToken := getAdminToken(t, r)

	// 空拓扑：instances 为空数组（非 null），前端可安全 .map。
	empty := makeRequest(r, http.MethodGet, "/api/v1/topology", nil, adminToken)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	var emptyResp topologyRespFull
	require.NoError(t, json.Unmarshal(empty.Body.Bytes(), &emptyResp))
	require.NotNil(t, emptyResp.Instances)
	require.Empty(t, emptyResp.Instances)

	node := createTestNode(t, db)
	proxy := createFR032Instance(t, db, node.ID, "topo-proxy", model.InstanceRoleProxy, 25565)
	backend := createFR032Instance(t, db, node.ID, "reg-backend", model.InstanceRoleBackend, 25566)
	// 未注册/配套服务：不进 proxies，但必须在 instances 里（FR-453 完整网络视图）。
	orphan := createFR032Instance(t, db, node.ID, "orphan-backend", model.InstanceRoleBackend, 25567)
	beacon := createFR032Instance(t, db, node.ID, "beacon-cp", model.InstanceRoleBeacon, 0)
	require.NoError(t, db.Model(&beacon).Update("type", model.InstanceTypeGeneric).Error)
	// 给 beacon 打 region/zone 标签，验证 tags 原文透出（前端分组依赖）。
	require.NoError(t, db.Model(&beacon).Update("tags", `["region:r1","zone:z1"]`).Error)

	mkReg := makeRequest(r, http.MethodPost, "/api/v1/proxies/"+itoa(proxy.ID)+"/registrations", map[string]any{
		"backendId": backend.ID, "alias": "lobby", "priority": 0,
	}, adminToken)
	require.Equal(t, http.StatusCreated, mkReg.Code, mkReg.Body.String())

	resp := makeRequest(r, http.MethodGet, "/api/v1/topology", nil, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	var topo topologyRespFull
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &topo))

	// proxies 仍只含已注册代理。
	require.Len(t, topo.Proxies, 1)
	require.Equal(t, proxy.ID, topo.Proxies[0].ID)

	// instances 含全部 4 台（含未注册的 orphan/beacon），id asc 保序。
	require.Len(t, topo.Instances, 4)
	require.Equal(t, proxy.ID, topo.Instances[0].ID)
	require.Equal(t, orphan.ID, topo.Instances[2].ID)
	require.Equal(t, "orphan-backend", topo.Instances[2].Name)
	require.Equal(t, beacon.ID, topo.Instances[3].ID)

	// 投影字段完整。
	b := topo.Instances[3]
	require.Equal(t, "beacon", b.Role)
	require.Equal(t, "generic", b.Type)
	require.Equal(t, node.ID, b.NodeID)
	require.Equal(t, `["region:r1","zone:z1"]`, b.Tags)
}

// TestFR453TopologyInstancesScoped 验证 GET /topology 的 instances 投影按调用者可访问实例收敛
// （FR-453 自审修复）：路由守卫 network.read 被组管理员/运维/只读持有，非管理员不得看到全量实例元数据。
// 平台管理员仍取全量；proxies/networks 维持既有全量契约不变。
func TestFR453TopologyInstancesScoped(t *testing.T) {
	db := setupTestDB(t)
	r := setupFR032Router(t, db)
	adminToken := getAdminToken(t, r)

	groupA := createGroupViaAPI(t, r, adminToken, "拓扑组A")
	groupB := createGroupViaAPI(t, r, adminToken, "拓扑组B")

	node := createTestNode(t, db)
	// 组 A 内后端（alice 可见）、组 B 内后端（alice 不可见）、未分组后端（alice 不可见）。
	inA := createFR032Instance(t, db, node.ID, "scoped-a", model.InstanceRoleBackend, 25566)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: groupA, InstanceID: inA.ID}).Error)
	inB := createFR032Instance(t, db, node.ID, "scoped-b", model.InstanceRoleBackend, 25567)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: groupB, InstanceID: inB.ID}).Error)
	createFR032Instance(t, db, node.ID, "scoped-orphan", model.InstanceRoleBackend, 25568)

	// 代理 + 一条注册关系：验证 proxies 对非管理员仍是全量契约。
	proxy := createFR032Instance(t, db, node.ID, "scoped-proxy", model.InstanceRoleProxy, 25565)
	mkReg := makeRequest(r, http.MethodPost, "/api/v1/proxies/"+itoa(proxy.ID)+"/registrations", map[string]any{
		"backendId": inA.ID, "alias": "lobby", "priority": 0,
	}, adminToken)
	require.Equal(t, http.StatusCreated, mkReg.Code, mkReg.Body.String())

	// alice：组 A 只读（持 network.read，非平台管理员）；bob：不属于任何组（可访问集合为空）。
	aliceToken := getMemberToken(t, r, "alice-topo", "password123")
	aliceID := findUserIDByUsername(t, db, "alice-topo")
	setGlobalRole(t, db, aliceID, model.RoleGroupViewer)
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	bobToken := getMemberToken(t, r, "bob-topo", "password123")
	bobID := findUserIDByUsername(t, db, "bob-topo")
	setGlobalRole(t, db, bobID, model.RoleGroupViewer)

	// 平台管理员：instances 全量（4 台：scoped-a/b/orphan + proxy）。
	adminResp := makeRequest(r, http.MethodGet, "/api/v1/topology", nil, adminToken)
	require.Equal(t, http.StatusOK, adminResp.Code, adminResp.Body.String())
	var adminTopo topologyRespFull
	require.NoError(t, json.Unmarshal(adminResp.Body.Bytes(), &adminTopo))
	require.Len(t, adminTopo.Instances, 4)

	// alice（限组 A）：instances 只含组 A 的 scoped-a；proxies/networks 仍全量。
	aliceResp := makeRequest(r, http.MethodGet, "/api/v1/topology", nil, aliceToken)
	require.Equal(t, http.StatusOK, aliceResp.Code, aliceResp.Body.String())
	var aliceTopo topologyRespFull
	require.NoError(t, json.Unmarshal(aliceResp.Body.Bytes(), &aliceTopo))
	require.Len(t, aliceTopo.Instances, 1)
	require.Equal(t, inA.ID, aliceTopo.Instances[0].ID)
	require.Equal(t, "scoped-a", aliceTopo.Instances[0].Name)
	// 既有契约不变：非管理员仍能看到代理注册与群组归属（不收敛）。
	require.Len(t, aliceTopo.Proxies, 1)
	require.Equal(t, proxy.ID, aliceTopo.Proxies[0].ID)
	require.Len(t, aliceTopo.Proxies[0].Registrations, 1)

	// bob（不属于任何组）：可访问集合为空 → instances 空（不回落全量）。
	bobResp := makeRequest(r, http.MethodGet, "/api/v1/topology", nil, bobToken)
	require.Equal(t, http.StatusOK, bobResp.Code, bobResp.Body.String())
	var bobTopo topologyRespFull
	require.NoError(t, json.Unmarshal(bobResp.Body.Bytes(), &bobTopo))
	require.NotNil(t, bobTopo.Instances)
	require.Empty(t, bobTopo.Instances)
}
