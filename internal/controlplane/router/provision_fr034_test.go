package router

import (
	"context"
	"encoding/json"
	"fmt"
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

type fakeFR034RouteWorker struct {
	workerpb.WorkerServiceClient
	download *workerpb.DownloadCoreRequest
	configs  map[string]string
}

func (f *fakeFR034RouteWorker) CreateInstance(context.Context, *workerpb.CreateInstanceRequest, ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *fakeFR034RouteWorker) DownloadCore(_ context.Context, req *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	f.download = req
	return &workerpb.DownloadCoreResponse{Success: true, Size: 42}, nil
}

func (f *fakeFR034RouteWorker) WriteConfig(_ context.Context, req *workerpb.WriteConfigRequest, _ ...grpc.CallOption) (*workerpb.WriteConfigResponse, error) {
	if f.configs == nil {
		f.configs = make(map[string]string)
	}
	f.configs[req.Path] = req.Content
	return &workerpb.WriteConfigResponse{Success: true}, nil
}

func (f *fakeFR034RouteWorker) DeployServerProbe(context.Context, *workerpb.DeployServerProbeRequest, ...grpc.CallOption) (*workerpb.DeployServerProbeResponse, error) {
	return &workerpb.DeployServerProbeResponse{Success: true}, nil
}

type fakeFR034CoreTransport struct{}

func (fakeFR034CoreTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	switch req.URL.Path {
	case "/v3/projects/paper":
		// fill v3：versions 为分组对象（组与组内均新→旧），扁平化后为 1.21.1/1.21/1.20.6。
		body = `{"versions":{"1.21":["1.21.1","1.21"],"1.20":["1.20.6"]}}`
	case "/v3/projects/paper/versions/1.21.1/builds":
		// fill v3：builds 为数组，下载在 downloads["server:default"].{name,checksums.sha256,url}。
		body = `[{"id":196,"downloads":{"server:default":{"name":"paper-1.21.1-196.jar","checksums":{"sha256":"` + strings.Repeat("a", 64) + `"},"url":"https://cdn.example/paper-1.21.1-196.jar"}}}]`
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found")), Request: req}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func setupFR034Router(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool, core *service.CoreService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-fr034", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.Shutdown()
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	jdkSvc := service.NewJDKService(db, pool)
	provSvc := service.NewProvisionService(db, pool, instanceSvc, core, nil)

	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		User:          service.NewUserService(db),
		Group:         groupSvc,
		Node:          nodeSvc,
		JDK:           jdkSvc,
		Instance:      instanceSvc,
		InstanceBatch: service.NewInstanceBatchService(db, pool),
		Audit:         service.NewAuditService(db),
		Authz:         authzSvc,
		Core:          core,
		Provision:     provSvc,
	}
	return Setup(svcs, jwtCfg.Secret)
}

func TestFR034ProvisionRoutes(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	core := service.NewCoreService()
	core.SetHTTPClient(&http.Client{Transport: fakeFR034CoreTransport{}})
	r := setupFR034Router(t, db, pool, core)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-fr034", "password123")
	node := createTestNode(t, db)
	worker := &fakeFR034RouteWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/opt/jdks/temurin-21"}
	require.NoError(t, db.Create(jdk).Error)

	forbidden := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/bukkit", map[string]any{}, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	versionsResp := makeRequest(r, http.MethodGet, "/api/v1/cores?type=paper", nil, adminToken)
	require.Equal(t, http.StatusOK, versionsResp.Code, versionsResp.Body.String())
	require.Equal(t, []any{"1.21.1", "1.21", "1.20.6"}, parseJSON(t, versionsResp)["versions"])

	resolveResp := makeRequest(r, http.MethodGet, "/api/v1/cores?type=paper&mcVersion=1.21.1", nil, adminToken)
	require.Equal(t, http.StatusOK, resolveResp.Code, resolveResp.Body.String())
	require.Equal(t, "paper-1.21.1-196.jar", parseJSON(t, resolveResp)["filename"])

	createResp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/bukkit", map[string]any{
		"nodeId": node.ID, "name": "fr034-route-lobby", "coreType": "paper", "mcVersion": "1.21.1", "jdkId": jdk.ID, "memoryMb": 2048, "onlineMode": false,
	}, adminToken)
	require.Equal(t, http.StatusCreated, createResp.Code, createResp.Body.String())
	var inst model.Instance
	require.NoError(t, json.Unmarshal(createResp.Body.Bytes(), &inst))
	require.Equal(t, model.InstanceRoleBackend, inst.Role)
	require.Equal(t, model.InstanceTypeMinecraftJava, inst.Type)
	require.Equal(t, 25565, inst.ServerPort)
	require.NotNil(t, worker.download)
	require.Equal(t, "server.jar", worker.download.DestFilename)
	require.Equal(t, strings.Repeat("a", 64), worker.download.Sha256)
	require.Contains(t, worker.configs["server.properties"], "online-mode=false")
	require.Contains(t, worker.configs["server.properties"], fmt.Sprintf("server-port=%d", inst.ServerPort))
}
