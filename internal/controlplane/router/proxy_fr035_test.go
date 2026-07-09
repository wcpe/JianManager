package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakeFR035RouteWorker struct {
	workerpb.WorkerServiceClient
	download *workerpb.DownloadCoreRequest
	configs  map[string]map[string]string
}

func (f *fakeFR035RouteWorker) CreateInstance(context.Context, *workerpb.CreateInstanceRequest, ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *fakeFR035RouteWorker) DownloadCore(_ context.Context, req *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	f.download = req
	return &workerpb.DownloadCoreResponse{Success: true, Size: 42}, nil
}

func (f *fakeFR035RouteWorker) ReadConfig(_ context.Context, req *workerpb.ReadConfigRequest, _ ...grpc.CallOption) (*workerpb.ReadConfigResponse, error) {
	if f.configs == nil || f.configs[req.InstanceUuid] == nil {
		return &workerpb.ReadConfigResponse{Path: req.Path, Content: ""}, nil
	}
	return &workerpb.ReadConfigResponse{Path: req.Path, Content: f.configs[req.InstanceUuid][req.Path]}, nil
}

func (f *fakeFR035RouteWorker) WriteConfig(_ context.Context, req *workerpb.WriteConfigRequest, _ ...grpc.CallOption) (*workerpb.WriteConfigResponse, error) {
	if f.configs == nil {
		f.configs = make(map[string]map[string]string)
	}
	if f.configs[req.InstanceUuid] == nil {
		f.configs[req.InstanceUuid] = make(map[string]string)
	}
	f.configs[req.InstanceUuid][req.Path] = req.Content
	return &workerpb.WriteConfigResponse{Success: true}, nil
}

type fakeFR035RouteCoreTransport struct{}

func (fakeFR035RouteCoreTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	switch req.URL.Path {
	case "/v3/projects/velocity":
		// fill v3：versions 为分组对象（组与组内均新→旧），扁平化后为 3.3.0-SNAPSHOT/3.2.0-SNAPSHOT。
		body = `{"versions":{"3.3.0":["3.3.0-SNAPSHOT"],"3.2.0":["3.2.0-SNAPSHOT"]}}`
	case "/v3/projects/velocity/versions/3.3.0-SNAPSHOT/builds":
		// fill v3：builds 为数组，Velocity 下载同样在 downloads["server:default"]（真实 API 已核验）。
		body = `[{"id":500,"downloads":{"server:default":{"name":"velocity-3.3.0-SNAPSHOT-500.jar","checksums":{"sha256":"` + strings.Repeat("b", 64) + `"},"url":"https://cdn.example/velocity-3.3.0-SNAPSHOT-500.jar"}}}]`
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found")), Request: req}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func setupFR035Router(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool, core *service.CoreService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-fr035", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.Shutdown()
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	regSvc := service.NewRegistrationService(db)
	proxySvc := service.NewProxyService(db, pool, instanceSvc, core, regSvc)
	regSvc.SetSyncer(proxySvc)

	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		User:          service.NewUserService(db),
		Group:         groupSvc,
		Node:          nodeSvc,
		JDK:           service.NewJDKService(db, pool),
		Instance:      instanceSvc,
		InstanceBatch: service.NewInstanceBatchService(db, pool),
		Audit:         service.NewAuditService(db),
		Authz:         authzSvc,
		Core:          core,
		Proxy:         proxySvc,
		Registration:  regSvc,
	}
	return Setup(svcs, jwtCfg.Secret)
}

func TestFR035ProvisionProxyRoutes(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	core := service.NewCoreService()
	core.SetHTTPClient(&http.Client{Transport: fakeFR035RouteCoreTransport{}})
	r := setupFR035Router(t, db, pool, core)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-fr035", "password123")
	node := createTestNode(t, db)
	worker := &fakeFR035RouteWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/opt/jdks/temurin-21"}
	require.NoError(t, db.Create(jdk).Error)
	backend := &model.Instance{NodeID: node.ID, Name: "lobby", Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped, ServerPort: 25566, QueryPort: 25566}
	require.NoError(t, db.Create(backend).Error)

	forbidden := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/proxy", map[string]any{}, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	versionsResp := makeRequest(r, http.MethodGet, "/api/v1/cores?type=velocity", nil, adminToken)
	require.Equal(t, http.StatusOK, versionsResp.Code, versionsResp.Body.String())
	require.Equal(t, []any{"3.3.0-SNAPSHOT", "3.2.0-SNAPSHOT"}, parseJSON(t, versionsResp)["versions"])

	createResp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/proxy", map[string]any{
		"nodeId": node.ID, "name": "fr035-route-velocity", "proxyType": "velocity", "version": "3.3.0-SNAPSHOT", "jdkId": jdk.ID, "memoryMb": 1024, "onlineMode": false,
		"backendRegistrations": []map[string]any{{"backendId": backend.ID, "alias": "lobby", "priority": 0, "forcedHost": "play.example.com"}},
	}, adminToken)
	require.Equal(t, http.StatusCreated, createResp.Code, createResp.Body.String())
	var result service.ProvisionProxyResult
	require.NoError(t, json.Unmarshal(createResp.Body.Bytes(), &result))
	require.NotEmpty(t, result.ForwardingSecret)
	require.NotNil(t, result.Instance)
	require.Equal(t, model.InstanceRoleProxy, result.Instance.Role)
	require.Equal(t, 25565, result.Instance.ServerPort)
	require.Len(t, result.Registrations, 1)
	require.Equal(t, "lobby", result.Registrations[0].Alias)
	require.NotNil(t, worker.download)
	require.Equal(t, "server.jar", worker.download.DestFilename)
	require.Equal(t, strings.Repeat("b", 64), worker.download.Sha256)
	require.Equal(t, result.ForwardingSecret, worker.configs[result.Instance.UUID]["forwarding.secret"])
	require.Contains(t, worker.configs[result.Instance.UUID]["velocity.toml"], "player-info-forwarding-mode = \"modern\"")
	require.Contains(t, worker.configs[result.Instance.UUID]["velocity.toml"], "lobby = \"127.0.0.1:25566\"")
	require.Contains(t, worker.configs[backend.UUID]["config/paper-global.yml"], result.ForwardingSecret)

	resyncResp := makeRequest(r, http.MethodPost, "/api/v1/proxies/"+itoa(result.Instance.ID)+"/resync", nil, adminToken)
	require.Equal(t, http.StatusOK, resyncResp.Code, resyncResp.Body.String())
	resync := parseJSON(t, resyncResp)
	require.Equal(t, true, resync["synced"])
	require.Equal(t, true, resync["secretConsistent"])
}
