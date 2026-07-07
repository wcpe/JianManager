package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakeFR034ProvisionWorker struct {
	workerpb.WorkerServiceClient
	created  *workerpb.CreateInstanceRequest
	download *workerpb.DownloadCoreRequest
	configs  map[string]string
}

func (f *fakeFR034ProvisionWorker) CreateInstance(_ context.Context, req *workerpb.CreateInstanceRequest, _ ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	f.created = req
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *fakeFR034ProvisionWorker) DownloadCore(_ context.Context, req *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	f.download = req
	return &workerpb.DownloadCoreResponse{Success: true, Size: 12345}, nil
}

func (f *fakeFR034ProvisionWorker) WriteConfig(_ context.Context, req *workerpb.WriteConfigRequest, _ ...grpc.CallOption) (*workerpb.WriteConfigResponse, error) {
	if f.configs == nil {
		f.configs = make(map[string]string)
	}
	f.configs[req.Path] = req.Content
	return &workerpb.WriteConfigResponse{Success: true}, nil
}

func (f *fakeFR034ProvisionWorker) DeployServerProbe(context.Context, *workerpb.DeployServerProbeRequest, ...grpc.CallOption) (*workerpb.DeployServerProbeResponse, error) {
	return &workerpb.DeployServerProbeResponse{Success: true}, nil
}

func TestFR034ProvisionBukkitCreatesBackendAndConfiguresWorker(t *testing.T) {
	db := newJDKTestDB(t)
	pool := cpgrpc.NewClientPool()
	worker := &fakeFR034ProvisionWorker{}
	core := NewCoreService()
	core.base = fakeFR034PaperCoreRepo(t)
	groupSvc := NewGroupService(db)
	instanceSvc := NewInstanceService(db, groupSvc, pool)
	t.Cleanup(instanceSvc.Shutdown)
	prov := NewProvisionService(db, pool, instanceSvc, core, nil)

	node := &model.Node{UUID: "fr034-node", Name: "fr034-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/opt/jdks/temurin-21"}
	require.NoError(t, db.Create(jdk).Error)
	pool.SetWorkerClientForTest(node.UUID, worker)

	onlineMode := false
	inst, err := prov.ProvisionBukkit(context.Background(), ProvisionBukkitRequest{
		NodeID:     node.ID,
		Name:       "fr034-lobby",
		CoreType:   "paper",
		MCVersion:  "1.21.1",
		JDKID:      jdk.ID,
		MemoryMb:   4096,
		JvmArgs:    []string{"-XX:+UseG1GC"},
		OnlineMode: &onlineMode,
	})

	require.NoError(t, err)
	require.NotNil(t, inst)
	require.Equal(t, model.InstanceTypeMinecraftJava, inst.Type)
	require.Equal(t, model.InstanceRoleBackend, inst.Role)
	require.Equal(t, model.ProcessTypeDaemon, inst.ProcessType)
	require.Equal(t, model.InstanceStatusStopped, inst.Status)
	require.True(t, inst.AutoRestart)
	require.Equal(t, 25565, inst.ServerPort)
	require.Equal(t, 25565, inst.QueryPort)
	require.Equal(t, 29940, inst.ProbePort)
	require.True(t, strings.HasPrefix(inst.WorkDir, "var/servers/fr034-lobby-"), inst.WorkDir)
	require.Equal(t, "java -Xms4096M -Xmx4096M -XX:+UseG1GC -jar server.jar nogui", inst.StartCommand)

	require.NotNil(t, worker.created)
	require.Equal(t, inst.UUID, worker.created.InstanceUuid)
	require.Equal(t, inst.StartCommand, worker.created.StartCommand)
	require.Equal(t, inst.WorkDir, worker.created.WorkDir)
	require.Equal(t, "/opt/jdks/temurin-21", worker.created.JdkPath)
	require.Equal(t, int32(29940), worker.created.ProbePort)

	require.NotNil(t, worker.download)
	require.Equal(t, inst.UUID, worker.download.InstanceUuid)
	require.Equal(t, "server.jar", worker.download.DestFilename)
	require.Equal(t, "paper-1.21.1-196.jar", pathBase(worker.download.DownloadUrl))
	require.Equal(t, strings.Repeat("a", 64), worker.download.Sha256)

	require.Equal(t, "eula=true\n", worker.configs["eula.txt"])
	serverProps := worker.configs["server.properties"]
	require.Contains(t, serverProps, "server-port=25565")
	require.Contains(t, serverProps, "query.port=25565")
	require.Contains(t, serverProps, "online-mode=false")
}

func fakeFR034PaperCoreRepo(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/paper/versions/1.21.1/builds" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"builds":[{"build":196,"downloads":{"application":{"name":"paper-1.21.1-196.jar","sha256":"`+strings.Repeat("a", 64)+`"}}}]}`)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func pathBase(raw string) string {
	idx := strings.LastIndex(raw, "/")
	if idx < 0 {
		return raw
	}
	return raw[idx+1:]
}
