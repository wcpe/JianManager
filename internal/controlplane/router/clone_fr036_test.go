package router

import (
	"context"
	"encoding/json"
	"net/http"
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

type fakeFR036RouteWorker struct {
	workerpb.WorkerServiceClient
	clone   *workerpb.CloneWorkDirRequest
	configs map[string]map[string]string
}

func (f *fakeFR036RouteWorker) CreateInstance(context.Context, *workerpb.CreateInstanceRequest, ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *fakeFR036RouteWorker) CloneWorkDir(_ context.Context, req *workerpb.CloneWorkDirRequest, _ ...grpc.CallOption) (*workerpb.CloneWorkDirResponse, error) {
	f.clone = req
	if f.configs == nil {
		f.configs = make(map[string]map[string]string)
	}
	if f.configs[req.DstInstanceUuid] == nil {
		f.configs[req.DstInstanceUuid] = make(map[string]string)
	}
	for path, content := range f.configs[req.SrcInstanceUuid] {
		f.configs[req.DstInstanceUuid][path] = content
	}
	return &workerpb.CloneWorkDirResponse{Success: true, CopiedFiles: 2, CopiedBytes: 64}, nil
}

func (f *fakeFR036RouteWorker) ReadConfig(_ context.Context, req *workerpb.ReadConfigRequest, _ ...grpc.CallOption) (*workerpb.ReadConfigResponse, error) {
	if f.configs == nil || f.configs[req.InstanceUuid] == nil {
		return &workerpb.ReadConfigResponse{Path: req.Path, Content: ""}, nil
	}
	return &workerpb.ReadConfigResponse{Path: req.Path, Content: f.configs[req.InstanceUuid][req.Path]}, nil
}

func (f *fakeFR036RouteWorker) WriteConfig(_ context.Context, req *workerpb.WriteConfigRequest, _ ...grpc.CallOption) (*workerpb.WriteConfigResponse, error) {
	if f.configs == nil {
		f.configs = make(map[string]map[string]string)
	}
	if f.configs[req.InstanceUuid] == nil {
		f.configs[req.InstanceUuid] = make(map[string]string)
	}
	f.configs[req.InstanceUuid][req.Path] = req.Content
	return &workerpb.WriteConfigResponse{Success: true}, nil
}

func setupFR036Router(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-fr036", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.Shutdown()
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	regSvc := service.NewRegistrationService(db)
	cloneSvc := service.NewCloneService(db, pool, instanceSvc, regSvc)

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
		Core:          service.NewCoreService(),
		Clone:         cloneSvc,
		Registration:  regSvc,
	}
	return Setup(svcs, jwtCfg.Secret)
}

func TestFR036CloneRoutes(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR036Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-fr036", "password123")
	node := createTestNode(t, db)
	worker := &fakeFR036RouteWorker{configs: make(map[string]map[string]string)}
	pool.SetWorkerClientForTest(node.UUID, worker)

	src := &model.Instance{NodeID: node.ID, Name: "lobby", Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped, StartCommand: "java -jar server.jar nogui", ServerPort: 25565, QueryPort: 25565}
	require.NoError(t, db.Create(src).Error)
	proxy := &model.Instance{NodeID: node.ID, Name: "velocity-main", Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleProxy, ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped, StartCommand: "java -jar server.jar", ServerPort: 25577}
	require.NoError(t, db.Create(proxy).Error)
	worker.configs[src.UUID] = map[string]string{"server.properties": "server-port=25565\nquery.port=25565\nmotd=Old\nlevel-name=world\n"}

	forbidden := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(src.ID)+"/clone", map[string]any{}, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	dryRunResp := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(src.ID)+"/clone", map[string]any{"name": "lobby-2", "dryRun": true, "mode": "quick"}, adminToken)
	require.Equal(t, http.StatusOK, dryRunResp.Code, dryRunResp.Body.String())
	dryRun := parseJSON(t, dryRunResp)
	require.Equal(t, true, dryRun["dryRun"])
	require.Nil(t, dryRun["instance"])
	require.NotNil(t, dryRun["allocated"])

	cloneResp := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(src.ID)+"/clone", map[string]any{
		"name": "lobby-2", "motd": "Lobby 2", "levelName": "world_lobby_2", "registerToProxyIds": []uint{proxy.ID}, "mode": "quick",
	}, adminToken)
	require.Equal(t, http.StatusCreated, cloneResp.Code, cloneResp.Body.String())
	var result service.CloneResult
	require.NoError(t, json.Unmarshal(cloneResp.Body.Bytes(), &result))
	require.NotNil(t, result.Instance)
	require.Equal(t, "lobby-2", result.Instance.Name)
	require.Equal(t, model.InstanceRoleBackend, result.Instance.Role)
	require.Equal(t, 25566, result.Allocated.ServerPort)
	require.Equal(t, 25566, result.Allocated.QueryPort)
	require.Contains(t, result.Excluded, "session.lock")
	require.Len(t, result.Registrations, 1)
	require.Equal(t, proxy.ID, result.Registrations[0].ProxyID)
	require.NotNil(t, worker.clone)
	require.Equal(t, quickCloneIncludesForRouter(), worker.clone.Include)
	fixed := worker.configs[result.Instance.UUID]["server.properties"]
	require.Contains(t, fixed, "server-port=25566")
	require.Contains(t, fixed, "query.port=25566")
	require.Contains(t, fixed, "motd=Lobby 2")
	require.Contains(t, fixed, "level-name=world_lobby_2")

	running := &model.Instance{NodeID: node.ID, Name: "running", Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusRunning, StartCommand: "java", ServerPort: 25570}
	require.NoError(t, db.Create(running).Error)
	runningResp := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(running.ID)+"/clone", map[string]any{"name": "running-copy"}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, runningResp.Code)
	require.Equal(t, "SOURCE_RUNNING", parseJSON(t, runningResp)["error"])
}

func quickCloneIncludesForRouter() []string {
	return []string{"*.jar", "plugins", "server.properties", "eula.txt", "*.yml", "*.yaml", "*.properties"}
}
