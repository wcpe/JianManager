package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func setupFR032Router(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-fr032", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	pool := cpgrpc.NewClientPool()
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.Shutdown()
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	provisionSvc := service.NewProvisionService(db, pool, instanceSvc, nil, nil)

	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		User:          service.NewUserService(db),
		Group:         groupSvc,
		Node:          nodeSvc,
		Instance:      instanceSvc,
		InstanceBatch: service.NewInstanceBatchService(db, pool),
		Provision:     provisionSvc,
		Registration:  service.NewRegistrationService(db),
		Network:       service.NewNetworkService(db, instanceSvc),
		Audit:         service.NewAuditService(db),
		Authz:         authzSvc,
	}
	return Setup(svcs, jwtCfg.Secret)
}

func createFR032Instance(t *testing.T, db *gorm.DB, nodeID uint, name string, role model.InstanceRole, serverPort int) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		NodeID:       nodeID,
		Name:         name,
		Type:         model.InstanceTypeMinecraftJava,
		Role:         role,
		ProcessType:  model.ProcessTypeDaemon,
		StartCommand: "java -jar server.jar nogui",
		Status:       model.InstanceStatusStopped,
		ServerPort:   serverPort,
		QueryPort:    serverPort,
		ProbePort:    29940 + serverPort - 25565,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

func TestFR032NetworkResourceRoutes(t *testing.T) {
	db := setupTestDB(t)
	r := setupFR032Router(t, db)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-fr032", "password123")

	forbidden := makeRequest(r, http.MethodGet, "/api/v1/networks", nil, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	nodeA := createTestNode(t, db)
	nodeB := createTestNodeWithSuffix(t, db, "fr032-other-node")
	proxyA := createFR032Instance(t, db, nodeA.ID, "survival-proxy", model.InstanceRoleProxy, 25565)
	proxyB := createFR032Instance(t, db, nodeB.ID, "creative-proxy", model.InstanceRoleProxy, 25565)
	backendA := createFR032Instance(t, db, nodeA.ID, "Survival Lobby", model.InstanceRoleBackend, 25566)
	backendB := createFR032Instance(t, db, nodeA.ID, "survival-world", model.InstanceRoleBackend, 25567)
	_ = createFR032Instance(t, db, nodeB.ID, "other-node-backend", model.InstanceRoleBackend, 25566)

	portsResp := makeRequest(r, http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/ports", nodeA.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, portsResp.Code, portsResp.Body.String())
	var ports service.NodePortsResult
	require.NoError(t, json.Unmarshal(portsResp.Body.Bytes(), &ports))
	require.Equal(t, nodeA.ID, ports.NodeID)
	require.Equal(t, 25565, ports.Ranges.ServerPortBase)
	require.Equal(t, 2000, ports.Ranges.RangeSize)
	require.Len(t, ports.Occupied, 3)
	require.Equal(t, "survival-proxy", ports.Occupied[0].Name)
	require.Equal(t, model.InstanceRoleProxy, ports.Occupied[0].Role)
	require.Equal(t, "Survival Lobby", ports.Occupied[1].Name)
	require.Equal(t, model.InstanceRoleBackend, ports.Occupied[1].Role)

	regResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/proxies/%d/registrations", proxyA.ID), map[string]any{
		"backendId": backendA.ID,
		"alias":     "lobby",
		"priority":  7,
	}, adminToken)
	require.Equal(t, http.StatusCreated, regResp.Code, regResp.Body.String())
	var reg service.RegistrationView
	require.NoError(t, json.Unmarshal(regResp.Body.Bytes(), &reg))
	require.Equal(t, proxyA.ID, reg.ProxyID)
	require.Equal(t, backendA.ID, reg.BackendID)
	require.Equal(t, "lobby", reg.Alias)
	require.Equal(t, 7, reg.Priority)
	require.True(t, reg.Enabled)
	require.NotNil(t, reg.Backend)
	require.Equal(t, "Survival Lobby", reg.Backend.Name)

	dupResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/proxies/%d/registrations", proxyA.ID), map[string]any{
		"backendId": backendA.ID,
		"alias":     "lobby-dup",
	}, adminToken)
	require.Equal(t, http.StatusConflict, dupResp.Code)
	require.Equal(t, "ALREADY_REGISTERED", parseJSON(t, dupResp)["error"])

	aliasResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/proxies/%d/registrations", proxyA.ID), map[string]any{
		"backendId": backendB.ID,
		"alias":     "lobby",
	}, adminToken)
	require.Equal(t, http.StatusConflict, aliasResp.Code)
	require.Equal(t, "ALIAS_CONFLICT", parseJSON(t, aliasResp)["error"])

	m2nResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/proxies/%d/registrations", proxyB.ID), map[string]any{
		"backendId":  backendA.ID,
		"alias":      "shared-lobby",
		"restricted": true,
	}, adminToken)
	require.Equal(t, http.StatusCreated, m2nResp.Code, m2nResp.Body.String())

	patchResp := makeRequest(r, http.MethodPatch, fmt.Sprintf("/api/v1/proxies/%d/registrations/%d", proxyA.ID, reg.ID), map[string]any{
		"alias":      "hub",
		"forcedHost": "hub.example.com",
		"enabled":    false,
	}, adminToken)
	require.Equal(t, http.StatusOK, patchResp.Code, patchResp.Body.String())
	var patched service.RegistrationView
	require.NoError(t, json.Unmarshal(patchResp.Body.Bytes(), &patched))
	require.Equal(t, "hub", patched.Alias)
	require.Equal(t, "hub.example.com", patched.ForcedHost)
	require.False(t, patched.Enabled)

	deleteRegResp := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/proxies/%d/registrations/%d", proxyA.ID, reg.ID), nil, adminToken)
	require.Equal(t, http.StatusNoContent, deleteRegResp.Code)

	netResp := makeRequest(r, http.MethodPost, "/api/v1/networks", map[string]any{"name": "survival", "description": "生存大区"}, adminToken)
	require.Equal(t, http.StatusCreated, netResp.Code, netResp.Body.String())
	var network model.Network
	require.NoError(t, json.Unmarshal(netResp.Body.Bytes(), &network))

	addResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/networks/%d/members", network.ID), map[string]any{
		"instanceIds": []uint{backendA.ID, backendB.ID, backendA.ID, 9999},
	}, adminToken)
	require.Equal(t, http.StatusOK, addResp.Code, addResp.Body.String())
	var addResult struct {
		Added   int                         `json:"added"`
		Members []service.NetworkMemberView `json:"members"`
	}
	require.NoError(t, json.Unmarshal(addResp.Body.Bytes(), &addResult))
	require.Equal(t, 2, addResult.Added)
	require.Len(t, addResult.Members, 2)

	netResp2 := makeRequest(r, http.MethodPost, "/api/v1/networks", map[string]any{"name": "shared", "description": "共享标签"}, adminToken)
	require.Equal(t, http.StatusCreated, netResp2.Code, netResp2.Body.String())
	var network2 model.Network
	require.NoError(t, json.Unmarshal(netResp2.Body.Bytes(), &network2))
	addResp2 := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/networks/%d/members", network2.ID), map[string]any{
		"instanceIds": []uint{backendA.ID},
	}, adminToken)
	require.Equal(t, http.StatusOK, addResp2.Code, addResp2.Body.String())

	deleteNetResp := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/networks/%d", network.ID), nil, adminToken)
	require.Equal(t, http.StatusNoContent, deleteNetResp.Code)
	var regCount int64
	require.NoError(t, db.Model(&model.ServerRegistration{}).Where("backend_id = ?", backendA.ID).Count(&regCount).Error)
	require.Equal(t, int64(1), regCount, "删除 Network 软标签不能删除 proxy↔backend 注册关系")
	var memberCount int64
	require.NoError(t, db.Model(&model.NetworkMember{}).Where("instance_id = ?", backendA.ID).Count(&memberCount).Error)
	require.Equal(t, int64(1), memberCount, "同一实例可属于多个 Network，删除其中一个只清对应成员关系")
}
