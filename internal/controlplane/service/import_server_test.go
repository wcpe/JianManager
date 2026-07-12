package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeImportWorker 伪 Worker：记录导入相关 RPC 调用并按预置应答。
type fakeImportWorker struct {
	workerpb.WorkerServiceClient
	inspectResp  *workerpb.InspectServerDirResponse
	inspectPaths []string
	importReqs   []*workerpb.ImportServerDirRequest
	importResp   *workerpb.ImportServerDirResponse
	created      []*workerpb.CreateInstanceRequest
	removed      []*workerpb.RemoveInstanceRequest
}

func (f *fakeImportWorker) InspectServerDir(_ context.Context, req *workerpb.InspectServerDirRequest, _ ...grpc.CallOption) (*workerpb.InspectServerDirResponse, error) {
	f.inspectPaths = append(f.inspectPaths, req.Path)
	if f.inspectResp == nil {
		return nil, errors.New("inspect 未预置")
	}
	return f.inspectResp, nil
}

func (f *fakeImportWorker) ImportServerDir(_ context.Context, req *workerpb.ImportServerDirRequest, _ ...grpc.CallOption) (*workerpb.ImportServerDirResponse, error) {
	f.importReqs = append(f.importReqs, req)
	if f.importResp == nil {
		return nil, errors.New("import 未预置")
	}
	return f.importResp, nil
}

func (f *fakeImportWorker) CreateInstance(_ context.Context, req *workerpb.CreateInstanceRequest, _ ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	f.created = append(f.created, req)
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *fakeImportWorker) RemoveInstance(_ context.Context, req *workerpb.RemoveInstanceRequest, _ ...grpc.CallOption) (*workerpb.RemoveInstanceResponse, error) {
	f.removed = append(f.removed, req)
	return &workerpb.RemoveInstanceResponse{Success: true, WorkDirSkipped: req.SkipWorkDir}, nil
}

func paperInspectResp() *workerpb.InspectServerDirResponse {
	return &workerpb.InspectServerDirResponse{
		Success: true,
		Jars: []*workerpb.ImportJarCandidate{
			{Path: "paper-1.20.4-497.jar", Size: 4096, MainClassHint: "io.papermc.paperclip.Main"},
			{Path: "plugins/Essentials.jar", Size: 128},
		},
		Jdks: []*workerpb.ImportJdkCandidate{
			{Path: `D:\legacy\srv\jre-17`, Vendor: "temurin", Version: "17.0.10", MajorVersion: 17, Arch: "x64"},
		},
		ServerPort:   25577,
		EulaAccepted: true,
		PropsFound:   true,
	}
}

func newImportEnv(t *testing.T) (*ImportServerService, *fakeImportWorker, *gorm.DB, *model.Node) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.Node{}, &model.NodeJDK{},
		&model.GroupInstance{}, &model.ServerRegistration{}, &model.NetworkMember{}))

	pool := cpgrpc.NewClientPool()
	instSvc := NewInstanceService(db, nil, pool)
	t.Cleanup(instSvc.Shutdown)
	svc := NewImportServerService(db, pool, instSvc)

	node := &model.Node{UUID: "import-node", Name: "import-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	worker := &fakeImportWorker{inspectResp: paperInspectResp()}
	pool.SetWorkerClientForTest(node.UUID, worker)
	return svc, worker, db, node
}

// Inspect 透传 Worker 探测结果。
func TestImportInspect_ProxiesWorker(t *testing.T) {
	svc, worker, _, node := newImportEnv(t)

	res, err := svc.Inspect(context.Background(), node.ID, `D:\legacy\srv`)
	require.NoError(t, err)
	require.Len(t, res.Jars, 2)
	assert.Equal(t, "paper-1.20.4-497.jar", res.Jars[0].Path)
	assert.Equal(t, 25577, res.ServerPort)
	assert.True(t, res.EulaAccepted)
	require.Len(t, worker.inspectPaths, 1)
}

// Inspect：Worker 守卫拒绝 → ErrImportRejected（供路由映射 4xx）。
func TestImportInspect_WorkerGuardRejection(t *testing.T) {
	svc, worker, _, node := newImportEnv(t)
	worker.inspectResp = &workerpb.InspectServerDirResponse{Success: false, Error: "无法访问: 不存在"}

	_, err := svc.Inspect(context.Background(), node.ID, `D:\nope`)
	require.ErrorIs(t, err, ErrImportRejected)
	assert.Contains(t, err.Error(), "无法访问")
}

// 就地导入：实例工作目录=原目录绝对路径 + 就地标记 + 端口沿用探测值 + 结构化启动派生命令 +
// 勾选的 JDK 登记为 managed=false。
func TestImportServer_InPlace(t *testing.T) {
	svc, worker, db, node := newImportEnv(t)

	inst, err := svc.Import(context.Background(), ImportServerRequest{
		NodeID:           node.ID,
		Path:             `D:\legacy\srv`,
		Mode:             "in_place",
		Name:             "老服",
		JarPath:          "paper-1.20.4-497.jar",
		MemoryMb:         2048,
		RegisterJdkPaths: []string{`D:\legacy\srv\jre-17`},
	})
	require.NoError(t, err)

	assert.Equal(t, `D:\legacy\srv`, inst.WorkDir, "就地模式工作目录必须是原目录绝对路径")
	assert.True(t, inst.WorkDirInPlace)
	assert.Equal(t, model.InstanceTypeMinecraftJava, inst.Type)
	assert.Equal(t, model.ProcessTypeDaemon, inst.ProcessType)
	assert.Equal(t, 25577, inst.ServerPort, "端口沿用探测值")
	assert.Contains(t, inst.StartCommand, "paper-1.20.4-497.jar", "结构化启动应派生 java 命令")
	assert.Contains(t, inst.StartCommand, "-Xmx2048M")
	assert.Empty(t, worker.importReqs, "就地模式不应调 ImportServerDir（no-op 在 CP 侧短路）")

	var jdks []model.NodeJDK
	require.NoError(t, db.Where("node_id = ?", node.ID).Find(&jdks).Error)
	require.Len(t, jdks, 1)
	assert.Equal(t, `D:\legacy\srv\jre-17`, jdks[0].Path)
	assert.False(t, jdks[0].Managed, "导入登记的 JDK 必须是外部登记语义（managed=false）")
	assert.Equal(t, 17, jdks[0].MajorVersion)
}

// 搬迁导入：调 ImportServerDir（slug 与实例名派生一致），工作目录=托管区相对路径、无就地标记。
func TestImportServer_Migrate(t *testing.T) {
	svc, worker, _, node := newImportEnv(t)
	worker.importResp = &workerpb.ImportServerDirResponse{Success: true, WorkDir: `D:\data\var\servers\old-web-x`, Moved: true}

	inst, err := svc.Import(context.Background(), ImportServerRequest{
		NodeID:  node.ID,
		Path:    `D:\legacy\srv`,
		Mode:    "migrate",
		Name:    "old-web",
		JarPath: "paper-1.20.4-497.jar",
	})
	require.NoError(t, err)

	require.Len(t, worker.importReqs, 1)
	assert.Equal(t, "migrate", worker.importReqs[0].Mode)
	assert.True(t, strings.HasPrefix(worker.importReqs[0].TargetSlug, "old-web-"), "slug 应由实例名派生: %s", worker.importReqs[0].TargetSlug)

	assert.False(t, inst.WorkDirInPlace)
	assert.Equal(t, "var/servers/"+worker.importReqs[0].TargetSlug, inst.WorkDir, "搬迁模式落托管区相对路径（便携）")
}

// jarPath 必须在探测候选内（防任意路径注入结构化启动）。
func TestImportServer_RejectsUnknownJar(t *testing.T) {
	svc, _, _, node := newImportEnv(t)

	_, err := svc.Import(context.Background(), ImportServerRequest{
		NodeID: node.ID, Path: `D:\legacy\srv`, Mode: "in_place", Name: "x",
		JarPath: "../../evil.jar",
	})
	require.ErrorIs(t, err, ErrImportRejected)
	assert.Contains(t, err.Error(), "候选")
}

// registerJdkPaths 不在探测候选内的路径跳过（不落库）；重复导入同一 JDK 不重复登记。
func TestImportServer_JdkRegistrationGuards(t *testing.T) {
	svc, _, db, node := newImportEnv(t)
	require.NoError(t, db.Create(&model.NodeJDK{
		NodeID: node.ID, Vendor: "temurin", MajorVersion: 17, Version: "17.0.10", Arch: "x64",
		Path: `D:\legacy\srv\jre-17`, Managed: false,
	}).Error)

	_, err := svc.Import(context.Background(), ImportServerRequest{
		NodeID: node.ID, Path: `D:\legacy\srv`, Mode: "in_place", Name: "y",
		JarPath:          "paper-1.20.4-497.jar",
		RegisterJdkPaths: []string{`D:\legacy\srv\jre-17`, `D:\not\a\candidate`},
	})
	require.NoError(t, err)

	var count int64
	require.NoError(t, db.Model(&model.NodeJDK{}).Where("node_id = ?", node.ID).Count(&count).Error)
	assert.EqualValues(t, 1, count, "已登记不重复、非候选跳过")
}

// 端到端锁死（FR-302 关键守则）：删除就地实例时 CP 必须显式传 skip_work_dir=true；
// 非就地实例不传。
func TestDeleteInPlaceInstanceSendsSkipWorkDir(t *testing.T) {
	svc, worker, db, node := newImportEnv(t)

	inst, err := svc.Import(context.Background(), ImportServerRequest{
		NodeID: node.ID, Path: `D:\legacy\srv`, Mode: "in_place", Name: "老服",
		JarPath: "paper-1.20.4-497.jar",
	})
	require.NoError(t, err)

	require.NoError(t, svc.instance.Delete(inst.ID))
	require.Len(t, worker.removed, 1)
	assert.True(t, worker.removed[0].SkipWorkDir, "就地实例删除必须显式指示 Worker 跳过目录删除")

	// 对照：普通实例不带 skip 标记。
	normal := &model.Instance{NodeID: node.ID, Name: "normal", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar",
		Status: model.InstanceStatusStopped, WorkDir: "var/servers/normal-12345678"}
	require.NoError(t, db.Create(normal).Error)
	require.NoError(t, svc.instance.Delete(normal.ID))
	require.Len(t, worker.removed, 2)
	assert.False(t, worker.removed[1].SkipWorkDir)
}

// 节点不存在 / 未连接的错误面。
func TestImportServer_NodeErrors(t *testing.T) {
	svc, _, db, _ := newImportEnv(t)

	_, err := svc.Inspect(context.Background(), 9999, `D:\x`)
	require.ErrorIs(t, err, ErrNodeNotFound)

	offline := &model.Node{UUID: "offline-node", Name: "offline", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(offline).Error)
	_, err = svc.Inspect(context.Background(), offline.ID, `D:\x`)
	require.ErrorIs(t, err, ErrNodeOffline)
}
