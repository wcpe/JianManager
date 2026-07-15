package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

type blockingFR035ProxyWorker struct {
	fakeFR035ProxyWorker
	started chan struct{}
	release chan struct{}
}

func (f *blockingFR035ProxyWorker) DownloadCore(ctx context.Context, req *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	f.download = req
	close(f.started)
	select {
	case <-f.release:
		return &workerpb.DownloadCoreResponse{Success: true, Size: 12345}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type failingFR035ProxyWorker struct {
	fakeFR035ProxyWorker
	removed   chan *workerpb.RemoveInstanceRequest
	removeErr string
}

func (f *failingFR035ProxyWorker) DownloadCore(_ context.Context, req *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	f.download = req
	return &workerpb.DownloadCoreResponse{Success: false, Error: "代理核心下载中断"}, nil
}

func (f *failingFR035ProxyWorker) RemoveInstance(_ context.Context, req *workerpb.RemoveInstanceRequest, _ ...grpc.CallOption) (*workerpb.RemoveInstanceResponse, error) {
	f.removed <- req
	if f.removeErr != "" {
		return &workerpb.RemoveInstanceResponse{Success: false, Error: f.removeErr}, nil
	}
	return &workerpb.RemoveInstanceResponse{Success: true}, nil
}

type offlineDuringFailureFR035ProxyWorker struct {
	fakeFR035ProxyWorker
	started chan struct{}
	release chan struct{}
	removed chan struct{}
}

func (f *offlineDuringFailureFR035ProxyWorker) DownloadCore(ctx context.Context, req *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	f.download = req
	close(f.started)
	select {
	case <-f.release:
		return &workerpb.DownloadCoreResponse{Success: false, Error: "代理核心下载中断"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *offlineDuringFailureFR035ProxyWorker) RemoveInstance(_ context.Context, _ *workerpb.RemoveInstanceRequest, _ ...grpc.CallOption) (*workerpb.RemoveInstanceResponse, error) {
	close(f.removed)
	return &workerpb.RemoveInstanceResponse{Success: true}, nil
}

func newAsyncFR035ProxyService(t *testing.T, worker workerpb.WorkerServiceClient) (*ProxyService, *TaskService, *model.Node, func()) {
	t.Helper()
	db := newJDKTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ServerRegistration{}, &model.Task{}, &model.TaskLog{}, &model.Notification{}))
	pool := cpgrpc.NewClientPool()
	core := NewCoreService()
	core.base = fakeFR035PaperCoreRepo(t)
	instanceSvc := NewInstanceService(db, NewGroupService(db), pool)
	regSvc := NewRegistrationService(db)
	proxySvc := NewProxyService(db, pool, instanceSvc, core, regSvc)
	regSvc.SetSyncer(proxySvc)
	taskSvc := NewTaskService(db)
	proxySvc.SetTaskService(taskSvc)
	node := &model.Node{UUID: "fr035-async-node-" + t.Name(), Name: "fr035-async-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	pool.SetWorkerClientForTest(node.UUID, worker)
	return proxySvc, taskSvc, node, instanceSvc.Shutdown
}

func TestProvisionProxyAsync_DetachesFromRequestContext(t *testing.T) {
	worker := &blockingFR035ProxyWorker{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	proxySvc, taskSvc, node, done := newAsyncFR035ProxyService(t, worker)
	defer done()

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	startedAt := time.Now()
	result, err := proxySvc.ProvisionProxyAsync(requestCtx, ProvisionProxyRequest{
		NodeID: node.ID, Name: "async-velocity", ProxyType: "velocity", Version: "3.3.0-SNAPSHOT", MemoryMb: 1024,
	}, 7)
	require.NoError(t, err)
	require.Less(t, time.Since(startedAt), time.Second, "POST 同步段不应等待核心下载")
	require.NotNil(t, result.Instance)
	require.NotEmpty(t, result.TaskID)

	cancelRequest()
	select {
	case <-worker.started:
	case <-time.After(time.Second):
		t.Fatal("后台下载未启动")
	}
	responseReason := result.Instance.StatusReason
	require.NotEmpty(t, responseReason)
	marshalStop := make(chan struct{})
	marshalDone := make(chan struct{})
	go func() {
		defer close(marshalDone)
		for {
			select {
			case <-marshalStop:
				return
			default:
				_, _ = json.Marshal(result)
			}
		}
	}()
	close(worker.release)

	task := waitTaskTerminal(t, taskSvc, result.TaskID)
	close(marshalStop)
	<-marshalDone
	require.Equal(t, model.TaskStateSucceeded, task.State)
	require.Empty(t, task.Result, "任务结果不得持久化 forwarding secret 或完整 HTTP 响应")
	require.NotContains(t, task.Result, result.ForwardingSecret)
	require.Equal(t, responseReason, result.Instance.StatusReason, "后台不得修改正在序列化的 HTTP 响应快照")
	var stored model.Instance
	require.NoError(t, proxySvc.db.First(&stored, result.Instance.ID).Error)
	require.Empty(t, stored.StatusReason, "供给成功后应清空数据库中的搭建原因")
}

func TestProvisionProxyAsync_FailureCompensatesInstanceWorkerAndWorkDir(t *testing.T) {
	worker := &failingFR035ProxyWorker{removed: make(chan *workerpb.RemoveInstanceRequest, 1)}
	proxySvc, taskSvc, node, done := newAsyncFR035ProxyService(t, worker)
	defer done()

	result, err := proxySvc.ProvisionProxyAsync(context.Background(), ProvisionProxyRequest{
		NodeID: node.ID, Name: "failed-velocity", ProxyType: "velocity", Version: "3.3.0-SNAPSHOT", MemoryMb: 1024,
	}, 9)
	require.NoError(t, err, "下载失败应发生在后台任务")
	require.NotEmpty(t, result.TaskID)

	task := waitTaskTerminal(t, taskSvc, result.TaskID)
	require.Equal(t, model.TaskStateFailed, task.State)
	require.Contains(t, task.Error, "代理核心下载中断")
	require.Empty(t, task.Result)
	require.NotContains(t, task.Error, result.ForwardingSecret)
	_, logs, getErr := taskSvc.Get(nil, result.TaskID)
	require.NoError(t, getErr)
	for _, line := range logs {
		require.NotContains(t, line.Line, result.ForwardingSecret)
	}

	select {
	case removed := <-worker.removed:
		require.Equal(t, result.Instance.UUID, removed.InstanceUuid)
		require.Equal(t, result.Instance.WorkDir, removed.WorkDir)
	case <-time.After(time.Second):
		t.Fatal("失败补偿未调用 Worker RemoveInstance")
	}
	var count int64
	require.NoError(t, proxySvc.db.Model(&model.Instance{}).Where("id = ?", result.Instance.ID).Count(&count).Error)
	require.Zero(t, count, "失败补偿后不应保留实例半成品")
}

func TestProvisionProxyAsync_FailurePreservesInstanceWhenRemoveFails(t *testing.T) {
	worker := &failingFR035ProxyWorker{
		removed:   make(chan *workerpb.RemoveInstanceRequest, 1),
		removeErr: "工作目录被占用",
	}
	proxySvc, taskSvc, node, done := newAsyncFR035ProxyService(t, worker)
	defer done()

	result, err := proxySvc.ProvisionProxyAsync(context.Background(), ProvisionProxyRequest{
		NodeID: node.ID, Name: "cleanup-failed-velocity", ProxyType: "velocity", Version: "3.3.0-SNAPSHOT", MemoryMb: 1024,
	}, 10)
	require.NoError(t, err)

	task := waitTaskTerminal(t, taskSvc, result.TaskID)
	require.Equal(t, model.TaskStateFailed, task.State)
	require.Contains(t, task.Error, "代理核心下载中断")
	require.Contains(t, task.Error, "失败补偿清理失败")
	require.Contains(t, task.Error, "工作目录被占用")
	require.Empty(t, task.Result)
	require.NotContains(t, task.Error, result.ForwardingSecret)

	var stored model.Instance
	require.NoError(t, proxySvc.db.First(&stored, result.Instance.ID).Error)
	require.Contains(t, stored.StatusReason, "搭建失败")
	require.Contains(t, stored.StatusReason, "待清理")
	require.Contains(t, stored.StatusReason, "代理核心下载中断")
}

func TestProvisionProxyAsync_FailurePreservesInstanceWhenNodeOffline(t *testing.T) {
	worker := &offlineDuringFailureFR035ProxyWorker{
		started: make(chan struct{}),
		release: make(chan struct{}),
		removed: make(chan struct{}),
	}
	proxySvc, taskSvc, node, done := newAsyncFR035ProxyService(t, worker)
	defer done()

	result, err := proxySvc.ProvisionProxyAsync(context.Background(), ProvisionProxyRequest{
		NodeID: node.ID, Name: "offline-cleanup-velocity", ProxyType: "velocity", Version: "3.3.0-SNAPSHOT", MemoryMb: 1024,
	}, 11)
	require.NoError(t, err)
	select {
	case <-worker.started:
	case <-time.After(time.Second):
		t.Fatal("后台下载未启动")
	}
	require.NoError(t, proxySvc.db.Model(node).Update("status", model.NodeStatusOffline).Error)
	close(worker.release)

	task := waitTaskTerminal(t, taskSvc, result.TaskID)
	require.Equal(t, model.TaskStateFailed, task.State)
	require.Contains(t, task.Error, "失败补偿清理失败")
	require.Contains(t, task.Error, "节点")
	require.Contains(t, task.Error, "离线")
	require.Empty(t, task.Result)
	require.NotContains(t, task.Error, result.ForwardingSecret)

	var stored model.Instance
	require.NoError(t, proxySvc.db.First(&stored, result.Instance.ID).Error)
	require.Contains(t, stored.StatusReason, "搭建失败")
	require.Contains(t, stored.StatusReason, "待清理")
	select {
	case <-worker.removed:
		t.Fatal("离线节点不得调用 RemoveInstance")
	default:
	}
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
		// fill v3：builds 为数组，Velocity 下载同样在 downloads["server:default"]（真实 API 已核验）。
		_, _ = fmt.Fprint(w, `[{"id":500,"downloads":{"server:default":{"name":"velocity-3.3.0-SNAPSHOT-500.jar","checksums":{"sha256":"`+strings.Repeat("b", 64)+`"},"url":"https://cdn.example/velocity-3.3.0-SNAPSHOT-500.jar"}}}]`)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}
