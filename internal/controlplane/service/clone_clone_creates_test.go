package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakeFR036CloneWorker struct {
	workerpb.WorkerServiceClient
	created []*workerpb.CreateInstanceRequest
	clone   *workerpb.CloneWorkDirRequest
	configs map[string]map[string]string
}

func (f *fakeFR036CloneWorker) CreateInstance(_ context.Context, req *workerpb.CreateInstanceRequest, _ ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	f.created = append(f.created, req)
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *fakeFR036CloneWorker) CloneWorkDir(_ context.Context, req *workerpb.CloneWorkDirRequest, _ ...grpc.CallOption) (*workerpb.CloneWorkDirResponse, error) {
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
	return &workerpb.CloneWorkDirResponse{Success: true, CopiedFiles: 3, CopiedBytes: 128, Skipped: []string{"logs"}}, nil
}

func (f *fakeFR036CloneWorker) ReadConfig(_ context.Context, req *workerpb.ReadConfigRequest, _ ...grpc.CallOption) (*workerpb.ReadConfigResponse, error) {
	if f.configs == nil || f.configs[req.InstanceUuid] == nil {
		return &workerpb.ReadConfigResponse{Path: req.Path, Content: ""}, nil
	}
	return &workerpb.ReadConfigResponse{Path: req.Path, Content: f.configs[req.InstanceUuid][req.Path]}, nil
}

func (f *fakeFR036CloneWorker) WriteConfig(_ context.Context, req *workerpb.WriteConfigRequest, _ ...grpc.CallOption) (*workerpb.WriteConfigResponse, error) {
	if f.configs == nil {
		f.configs = make(map[string]map[string]string)
	}
	if f.configs[req.InstanceUuid] == nil {
		f.configs[req.InstanceUuid] = make(map[string]string)
	}
	f.configs[req.InstanceUuid][req.Path] = req.Content
	return &workerpb.WriteConfigResponse{Success: true}, nil
}

type fakeFR036Syncer struct{ proxyIDs []uint }

func (f *fakeFR036Syncer) SyncProxy(proxyID uint) error {
	f.proxyIDs = append(f.proxyIDs, proxyID)
	return nil
}

func TestFR036CloneCreatesIndependentBackendAndFixesConfig(t *testing.T) {
	db := newCloneTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NodeJDK{}))
	pool := cpgrpc.NewClientPool()
	worker := &fakeFR036CloneWorker{configs: make(map[string]map[string]string)}
	instSvc := NewInstanceService(db, nil, pool)
	t.Cleanup(instSvc.Shutdown)
	regSvc := NewRegistrationService(db)
	syncer := &fakeFR036Syncer{}
	regSvc.SetSyncer(syncer)
	svc := NewCloneService(db, pool, instSvc, regSvc)

	node := &model.Node{UUID: "fr036-node", Name: "fr036-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/opt/jdks/temurin-21"}
	require.NoError(t, db.Create(jdk).Error)
	src := &model.Instance{
		NodeID: node.ID, Name: "lobby", Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -Xmx2G -jar server.jar nogui", JDKID: jdk.ID,
		Status: model.InstanceStatusStopped, ServerPort: 25565, QueryPort: 25565, AutoRestart: true,
		EnvVars: `{"JM_ENV":"prod"}`,
	}
	require.NoError(t, db.Create(src).Error)
	proxy := &model.Instance{NodeID: node.ID, Name: "velocity-main", Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleProxy, ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar", Status: model.InstanceStatusStopped, ServerPort: 25577}
	require.NoError(t, db.Create(proxy).Error)
	worker.configs[src.UUID] = map[string]string{
		"server.properties":       "server-port=25565\nquery.port=25565\nmotd=Old Lobby\nlevel-name=world\n",
		"config/paper-global.yml": "proxies:\n  velocity:\n    enabled: true\n    secret: keep-secret\n",
	}
	pool.SetWorkerClientForTest(node.UUID, worker)

	result, err := svc.Clone(context.Background(), src.ID, CloneInstanceRequest{
		Name: "lobby-2", Motd: "Lobby 2", LevelName: "world_lobby_2",
		RegisterToProxyIDs: []uint{proxy.ID}, Mode: "quick",
	})

	require.NoError(t, err)
	require.False(t, result.DryRun)
	require.NotNil(t, result.Instance)
	dst := result.Instance
	require.Equal(t, model.InstanceRoleBackend, dst.Role)
	require.Equal(t, model.InstanceTypeMinecraftJava, dst.Type)
	require.Equal(t, src.NodeID, dst.NodeID)
	require.Equal(t, src.StartCommand, dst.StartCommand)
	require.Equal(t, src.JDKID, dst.JDKID)
	require.True(t, dst.AutoRestart)
	require.Equal(t, 25566, dst.ServerPort)
	require.Equal(t, 25566, dst.QueryPort)
	require.True(t, dst.WorkDir != "" && dst.WorkDir != src.WorkDir)
	require.Contains(t, result.Excluded, "session.lock")
	require.Len(t, result.Registrations, 1)
	require.Equal(t, proxy.ID, result.Registrations[0].ProxyID)
	require.Equal(t, dst.ID, result.Registrations[0].BackendID)
	require.Equal(t, []uint{proxy.ID}, syncer.proxyIDs)

	require.NotNil(t, worker.clone)
	require.Equal(t, src.UUID, worker.clone.SrcInstanceUuid)
	require.Equal(t, dst.UUID, worker.clone.DstInstanceUuid)
	require.Equal(t, quickCloneIncludes, worker.clone.Include)
	require.Equal(t, defaultCloneExcludes, worker.clone.Exclude)
	require.GreaterOrEqual(t, len(worker.created), 3, "创建目标、确保源实例、确保目标实例都应向 Worker 注册")

	fixed := worker.configs[dst.UUID]["server.properties"]
	require.Contains(t, fixed, "server-port=25566")
	require.Contains(t, fixed, "query.port=25566")
	require.Contains(t, fixed, "motd=Lobby 2")
	require.Contains(t, fixed, "level-name=world_lobby_2")
	require.Equal(t, worker.configs[src.UUID]["config/paper-global.yml"], worker.configs[dst.UUID]["config/paper-global.yml"])
}
