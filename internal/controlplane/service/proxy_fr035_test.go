package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakeFR035ProxyWorker struct {
	workerpb.WorkerServiceClient
	created  *workerpb.CreateInstanceRequest
	download *workerpb.DownloadCoreRequest
	configs  map[string]map[string]string
}

func (f *fakeFR035ProxyWorker) CreateInstance(_ context.Context, req *workerpb.CreateInstanceRequest, _ ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	f.created = req
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *fakeFR035ProxyWorker) DownloadCore(_ context.Context, req *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	f.download = req
	return &workerpb.DownloadCoreResponse{Success: true, Size: 12345}, nil
}

func (f *fakeFR035ProxyWorker) ReadConfig(_ context.Context, req *workerpb.ReadConfigRequest, _ ...grpc.CallOption) (*workerpb.ReadConfigResponse, error) {
	if f.configs == nil || f.configs[req.InstanceUuid] == nil {
		return &workerpb.ReadConfigResponse{Path: req.Path, Content: ""}, nil
	}
	return &workerpb.ReadConfigResponse{Path: req.Path, Content: f.configs[req.InstanceUuid][req.Path]}, nil
}

func (f *fakeFR035ProxyWorker) WriteConfig(_ context.Context, req *workerpb.WriteConfigRequest, _ ...grpc.CallOption) (*workerpb.WriteConfigResponse, error) {
	if f.configs == nil {
		f.configs = make(map[string]map[string]string)
	}
	if f.configs[req.InstanceUuid] == nil {
		f.configs[req.InstanceUuid] = make(map[string]string)
	}
	f.configs[req.InstanceUuid][req.Path] = req.Content
	return &workerpb.WriteConfigResponse{Success: true}, nil
}

func TestFR035ProvisionProxyCreatesVelocityAndSyncsBackend(t *testing.T) {
	db := newJDKTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ServerRegistration{}))
	pool := cpgrpc.NewClientPool()
	worker := &fakeFR035ProxyWorker{}
	core := NewCoreService()
	core.base = fakeFR035PaperCoreRepo(t)
	groupSvc := NewGroupService(db)
	instanceSvc := NewInstanceService(db, groupSvc, pool)
	t.Cleanup(instanceSvc.Shutdown)
	regSvc := NewRegistrationService(db)
	proxySvc := NewProxyService(db, pool, instanceSvc, core, regSvc)
	regSvc.SetSyncer(proxySvc)

	node := &model.Node{UUID: "fr035-node", Name: "fr035-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/opt/jdks/temurin-21"}
	require.NoError(t, db.Create(jdk).Error)
	backend := &model.Instance{NodeID: node.ID, Name: "lobby", Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped, ServerPort: 25566, QueryPort: 25566}
	require.NoError(t, db.Create(backend).Error)
	pool.SetWorkerClientForTest(node.UUID, worker)

	onlineMode := false
	priority := 0
	result, err := proxySvc.ProvisionProxy(context.Background(), ProvisionProxyRequest{
		NodeID:     node.ID,
		Name:       "fr035-velocity",
		ProxyType:  "velocity",
		Version:    "3.3.0-SNAPSHOT",
		JDKID:      jdk.ID,
		MemoryMb:   1024,
		JvmArgs:    []string{"-XX:+UseG1GC"},
		OnlineMode: &onlineMode,
		BackendRegistrations: []CreateRegistrationRequest{{
			BackendID: backend.ID,
			Alias:     "lobby", Priority: &priority, ForcedHost: "play.example.com",
		}},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.ForwardingSecret)
	require.Len(t, result.ForwardingSecret, 32)
	require.Len(t, result.Registrations, 1)
	inst := result.Instance
	require.NotNil(t, inst)
	require.Equal(t, model.InstanceTypeMinecraftJava, inst.Type)
	require.Equal(t, model.InstanceRoleProxy, inst.Role)
	require.Equal(t, model.ProcessTypeDaemon, inst.ProcessType)
	require.Equal(t, model.InstanceStatusStopped, inst.Status)
	require.False(t, inst.ProxyOnlineMode)
	require.Equal(t, 25565, inst.ServerPort)
	require.True(t, strings.HasPrefix(inst.WorkDir, "var/servers/fr035-velocity-"), inst.WorkDir)
	require.Equal(t, "java -Xms1024M -Xmx1024M -XX:+UseG1GC -jar server.jar", inst.StartCommand)

	require.NotNil(t, worker.created)
	require.Equal(t, inst.UUID, worker.created.InstanceUuid)
	require.Equal(t, inst.StartCommand, worker.created.StartCommand)
	require.Equal(t, "/opt/jdks/temurin-21", worker.created.JdkPath)
	require.NotNil(t, worker.download)
	require.Equal(t, "server.jar", worker.download.DestFilename)
	require.Equal(t, "velocity-3.3.0-SNAPSHOT-500.jar", pathBase(worker.download.DownloadUrl))
	require.Equal(t, strings.Repeat("b", 64), worker.download.Sha256)

	proxyConfigs := worker.configs[inst.UUID]
	require.Equal(t, result.ForwardingSecret, proxyConfigs["forwarding.secret"])
	var velocity map[string]interface{}
	require.NoError(t, toml.Unmarshal([]byte(proxyConfigs["velocity.toml"]), &velocity))
	require.Equal(t, "0.0.0.0:25565", velocity["bind"])
	require.Equal(t, false, velocity["online-mode"])
	require.Equal(t, "modern", velocity["player-info-forwarding-mode"])
	require.Equal(t, "forwarding.secret", velocity["forwarding-secret-file"])
	servers := velocity["servers"].(map[string]interface{})
	require.Equal(t, "127.0.0.1:25566", servers["lobby"])
	forcedHosts := velocity["forced-hosts"].(map[string]interface{})
	require.Contains(t, forcedHosts, "play.example.com")

	backendConfigs := worker.configs[backend.UUID]
	var paperGlobal map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(backendConfigs["config/paper-global.yml"]), &paperGlobal))
	vel := paperGlobal["proxies"].(map[string]interface{})["velocity"].(map[string]interface{})
	require.Equal(t, true, vel["enabled"])
	require.Equal(t, true, vel["online-mode"])
	require.Equal(t, result.ForwardingSecret, vel["secret"])
}

func fakeFR035PaperCoreRepo(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/velocity/versions/3.3.0-SNAPSHOT/builds" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"builds":[{"build":500,"downloads":{"application":{"name":"velocity-3.3.0-SNAPSHOT-500.jar","sha256":"`+strings.Repeat("b", 64)+`"}}}]}`)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}
