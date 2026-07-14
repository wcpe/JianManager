package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// topologyResp 是 GET /topology 响应的测试镜像（避免依赖 router 内部私有类型）。
type topologyResp struct {
	Proxies []struct {
		ID            uint                       `json:"id"`
		Name          string                     `json:"name"`
		Status        string                     `json:"status"`
		ServerPort    int                        `json:"serverPort"`
		NodeID        uint                       `json:"nodeId"`
		Registrations []service.RegistrationView `json:"registrations"`
	} `json:"proxies"`
	Networks []struct {
		ID                uint   `json:"id"`
		Name              string `json:"name"`
		MemberInstanceIDs []uint `json:"memberInstanceIds"`
	} `json:"networks"`
}

// TestFR335TopologyAggregate 验证聚合拓扑端点：鉴权收敛 + 一次返全量 proxy 注册 + network 成员归属。
func TestFR335TopologyAggregate(t *testing.T) {
	db := setupTestDB(t)
	r := setupFR032Router(t, db)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-fr335", "password123")

	// 鉴权：非平台管理员 403。
	forbidden := makeRequest(r, http.MethodGet, "/api/v1/topology", nil, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	// 空拓扑：proxies 为空数组（非 null）。
	empty := makeRequest(r, http.MethodGet, "/api/v1/topology", nil, adminToken)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	var emptyResp topologyResp
	require.NoError(t, json.Unmarshal(empty.Body.Bytes(), &emptyResp))
	require.NotNil(t, emptyResp.Proxies)
	require.Empty(t, emptyResp.Proxies)

	// 铺数据：2 proxy + 2 backend，proxyA 注册 lobby+world，proxyB 注册 lobby（M:N）。
	node := createTestNode(t, db)
	proxyA := createFR032Instance(t, db, node.ID, "survival-proxy", model.InstanceRoleProxy, 25565)
	proxyB := createFR032Instance(t, db, node.ID, "creative-proxy", model.InstanceRoleProxy, 25568)
	backendA := createFR032Instance(t, db, node.ID, "Survival Lobby", model.InstanceRoleBackend, 25566)
	backendB := createFR032Instance(t, db, node.ID, "survival-world", model.InstanceRoleBackend, 25567)

	mkReg := func(proxyID, backendID uint, alias string, priority int) {
		resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/proxies/%d/registrations", proxyID), map[string]any{
			"backendId": backendID, "alias": alias, "priority": priority,
		}, adminToken)
		require.Equal(t, http.StatusCreated, resp.Code, resp.Body.String())
	}
	mkReg(proxyA.ID, backendA.ID, "lobby", 10)
	mkReg(proxyA.ID, backendB.ID, "world", 5)
	mkReg(proxyB.ID, backendA.ID, "shared-lobby", 0)

	// 群组：survival 含 proxyA + backendA。
	netResp := makeRequest(r, http.MethodPost, "/api/v1/networks", map[string]any{"name": "survival"}, adminToken)
	require.Equal(t, http.StatusCreated, netResp.Code, netResp.Body.String())
	var network model.Network
	require.NoError(t, json.Unmarshal(netResp.Body.Bytes(), &network))
	addResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/networks/%d/members", network.ID), map[string]any{
		"instanceIds": []uint{proxyA.ID, backendA.ID},
	}, adminToken)
	require.Equal(t, http.StatusOK, addResp.Code, addResp.Body.String())

	// 聚合拓扑：一次返全量。
	resp := makeRequest(r, http.MethodGet, "/api/v1/topology", nil, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	var topo topologyResp
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &topo))

	require.Len(t, topo.Proxies, 2)
	// 保序 id asc：proxyA 先。
	require.Equal(t, proxyA.ID, topo.Proxies[0].ID)
	require.Equal(t, "survival-proxy", topo.Proxies[0].Name)
	require.Equal(t, 25565, topo.Proxies[0].ServerPort)
	require.Len(t, topo.Proxies[0].Registrations, 2)
	// 排序 priority asc：world(5) 先于 lobby(10)。
	require.Equal(t, "world", topo.Proxies[0].Registrations[0].Alias)
	require.Equal(t, "lobby", topo.Proxies[0].Registrations[1].Alias)
	// 后端概要内联。
	require.NotNil(t, topo.Proxies[0].Registrations[1].Backend)
	require.Equal(t, "Survival Lobby", topo.Proxies[0].Registrations[1].Backend.Name)
	require.Len(t, topo.Proxies[1].Registrations, 1)

	// 同构性：聚合里 proxyA 的 registrations 与单发 GET /proxies/:id/registrations 一致。
	single := makeRequest(r, http.MethodGet, fmt.Sprintf("/api/v1/proxies/%d/registrations", proxyA.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, single.Code)
	var singleRegs []service.RegistrationView
	require.NoError(t, json.Unmarshal(single.Body.Bytes(), &singleRegs))
	require.Equal(t, singleRegs, topo.Proxies[0].Registrations)

	// network 成员归属正确。
	require.Len(t, topo.Networks, 1)
	require.Equal(t, "survival", topo.Networks[0].Name)
	require.ElementsMatch(t, []uint{proxyA.ID, backendA.ID}, topo.Networks[0].MemberInstanceIDs)
}
