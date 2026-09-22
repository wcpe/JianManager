package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ---- 制品来源校验（FR-441 §3.1/3.2）----

// TestResolveBinarySource_URLRequiresHTTPS https 是硬约束：明文传输的二进制可被中间人替换，
// 与自更新/JDK 下载源同口径。
func TestResolveBinarySource_URLRequiresHTTPS(t *testing.T) {
	svc := &ProvisionService{}

	for _, tc := range []struct {
		name string
		url  string
		ok   bool
	}{
		{"https 接受", "https://example.com/beacon-linux-amd64", true},
		{"http 拒绝", "http://example.com/beacon", false},
		{"file 协议拒绝", "file:///opt/beacon", false},
		{"ftp 拒绝", "ftp://example.com/beacon", false},
		{"缺主机拒绝", "https:///beacon", false},
		{"空地址拒绝", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, _, err := svc.resolveBinarySource(BinarySource{
				Kind: BinarySourceURL, URL: tc.url, Filename: "beacon",
			}, "")
			if tc.ok {
				require.NoError(t, err)
				require.Equal(t, tc.url, plan.DownloadURL)
				require.Equal(t, "url", plan.SourceKind)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestResolveBinarySource_SHA256Format 非法摘要必须挡在同步段（否则 Worker 侧永远校验失败，
// 用户只看到「校验不符」而不知是填错了格式）。
func TestResolveBinarySource_SHA256Format(t *testing.T) {
	svc := &ProvisionService{}
	good := strings.Repeat("ab", 32) // 64 位十六进制

	_, _, err := svc.resolveBinarySource(BinarySource{
		Kind: BinarySourceURL, URL: "https://example.com/b", Filename: "b", SHA256: good,
	}, "")
	require.NoError(t, err, "合法 sha256 应通过")

	for _, bad := range []string{"deadbeef", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		_, _, err := svc.resolveBinarySource(BinarySource{
			Kind: BinarySourceURL, URL: "https://example.com/b", Filename: "b", SHA256: bad,
		}, "")
		require.Error(t, err, "非法 sha256 应被拒: %s", bad)
	}
}

// TestResolveBinarySource_FilenameIsSecurityBoundary 文件名会进入结构化启动命令
// （./<filename>），可注入分隔符/空白即等同任意命令执行——必须严格校验。
func TestResolveBinarySource_FilenameIsSecurityBoundary(t *testing.T) {
	svc := &ProvisionService{}
	for _, bad := range []string{
		"", "..", ".", "../../etc/passwd", "a/b", `a\b`,
		"beacon --evil", "beacon\nrm -rf /", "beacon;rm", strings.Repeat("x", 129),
	} {
		_, _, err := svc.resolveBinarySource(BinarySource{
			Kind: BinarySourceURL, URL: "https://example.com/b", Filename: bad,
		}, "")
		require.Error(t, err, "非法文件名应被拒: %q", bad)
	}
}

// TestResolveBinarySource_NodeFileRootsGuard 路径越界必须被拒（spec §3.2 安全约束、验收项 3 负例）。
// 默认放行根为空集＝该来源关闭；显式放行后只允许根内路径。
func TestResolveBinarySource_NodeFileRootsGuard(t *testing.T) {
	sha := strings.Repeat("cd", 32)

	t.Run("默认未配置放行根：一律拒绝", func(t *testing.T) {
		svc := &ProvisionService{}
		_, _, err := svc.resolveBinarySource(BinarySource{
			Kind: BinarySourceNodeFile, NodePath: "/opt/binaries/beacon", Filename: "beacon",
		}, "")
		require.Error(t, err, "未配置放行根时 node_file 来源应被拒")
		require.Contains(t, err.Error(), "受控放行目录之外")
	})

	t.Run("配置放行根：根内通过、根外拒绝", func(t *testing.T) {
		svc := &ProvisionService{binaryRoots: func() []string { return []string{"/opt/binaries"} }}

		plan, _, err := svc.resolveBinarySource(BinarySource{
			Kind: BinarySourceNodeFile, NodePath: "/opt/binaries/beacon", Filename: "beacon", SHA256: sha,
		}, "")
		require.NoError(t, err, "根内路径应通过")
		require.Equal(t, "node_file", plan.SourceKind)
		require.Equal(t, "/opt/binaries/beacon", plan.NodePath)
		require.Equal(t, sha, plan.SHA256)

		// 根本身也放行（明确声明的目录本身即是受控区）。
		_, _, err = svc.resolveBinarySource(BinarySource{
			Kind: BinarySourceNodeFile, NodePath: "/opt/binaries", Filename: "beacon",
		}, "")
		require.NoError(t, err, "放行根自身应通过")

		for _, bad := range []string{
			"/etc/shadow",                   // 完全越界
			"/opt/binaries-evil/beacon",     // 前缀相似但不是同一目录（Rel 判定挡住）
			"/opt/binaries/../secrets/rust", // 穿越出根
			"/opt/binaries/sub/../../etc/passwd",
		} {
			_, _, err := svc.resolveBinarySource(BinarySource{
				Kind: BinarySourceNodeFile, NodePath: bad, Filename: "beacon",
			}, "")
			require.Error(t, err, "越界路径应被拒: %s", bad)
		}
	})

	t.Run("相对路径拒绝", func(t *testing.T) {
		svc := &ProvisionService{binaryRoots: func() []string { return []string{"/opt/binaries"} }}
		_, _, err := svc.resolveBinarySource(BinarySource{
			Kind: BinarySourceNodeFile, NodePath: "opt/binaries/beacon", Filename: "beacon",
		}, "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "绝对路径")
	})
}

// TestResolveBinarySource_UnknownKind 未知/缺失 kind 明确报错，不静默落空值。
func TestResolveBinarySource_UnknownKind(t *testing.T) {
	svc := &ProvisionService{}
	_, _, err := svc.resolveBinarySource(BinarySource{Filename: "beacon"}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "缺少 binarySource.kind")

	_, _, err = svc.resolveBinarySource(BinarySource{Kind: "github", Filename: "beacon"}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "不支持的 binarySource.kind")
}

// TestResolveBinarySource_AssetWithoutService 未装配制品库时必须明确报错，不静默当成 url 空值。
func TestResolveBinarySource_AssetWithoutService(t *testing.T) {
	svc := &ProvisionService{}
	_, _, err := svc.resolveBinarySource(BinarySource{Kind: BinarySourceAsset, AssetID: 7, Filename: "beacon"}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "制品库服务未装配")
}

// ---- 启动命令派生与阶段进度（spec §3.4 / §3.3）----

// TestDeriveBinaryStartCommand startCommand 复用既有字段（决策 2A）。
func TestDeriveBinaryStartCommand(t *testing.T) {
	require.Equal(t, "./beacon-1.1.0-linux-amd64", deriveBinaryStartCommand("beacon-1.1.0-linux-amd64"))
	require.Equal(t, "./beacon", deriveBinaryStartCommand("  beacon  "))
}

// TestBinaryProvisionStageFromBytes 字节进度映射到 [20,80]，未知总长时停在起点不谎报。
func TestBinaryProvisionStageFromBytes(t *testing.T) {
	require.Equal(t, binaryStageFetchFrom, binaryProvisionStageFromBytes(0, 100))
	require.Equal(t, binaryStageFetchFrom, binaryProvisionStageFromBytes(50, -1), "未知总长不应谎报进度")
	require.Equal(t, binaryStageFetchFrom, binaryProvisionStageFromBytes(0, 0))
	require.Equal(t, binaryStageFetchTo, binaryProvisionStageFromBytes(100, 100))
	require.Equal(t, binaryStageFetchTo, binaryProvisionStageFromBytes(200, 100), "超出总长按满进度封顶")

	// 半程落在区间中点附近（20 + 60*0.5 = 50）。
	mid := binaryProvisionStageFromBytes(50, 100)
	require.Greater(t, mid, binaryStageFetchFrom)
	require.Less(t, mid, binaryStageFetchTo)
}

// ---- 异步搭建端到端（FR-441 验收项 4/6/7/8）----

// binaryWorkerStub 记录 FetchBinary 请求并按需回放进度帧与终态。
type binaryWorkerStub struct {
	workerpb.WorkerServiceClient
	mu       sync.Mutex
	requests []*workerpb.FetchBinaryRequest
	// frames 是回放的进度帧；为空时默认回「成功」终态。
	frames []*workerpb.FetchBinaryProgress
	// hashRequests / hashResp 供磁盘内容比对路径（N-4）用：为空时回「文件不存在」。
	hashRequests []*workerpb.HashFileRequest
	hashResp     *workerpb.HashFileResponse
}

func (f *binaryWorkerStub) CreateInstance(_ context.Context, _ *workerpb.CreateInstanceRequest, _ ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

// HashFile 实现磁盘内容比对（FR-468 §2.5）的 Worker 侧契约（N-4）。
//
// 必须显式实现：嵌入的 workerpb.WorkerServiceClient 是**接口**，零值 nil——未实现的方法
// 一旦被调用就是 nil 解引用 panic。N-4 修正 View 的 status 漏选后，停止态实例的磁盘比对
// 真正开始执行，任何走 binaryWorkerStub 的用例都会打到这条路径，于是「静默不执行」的
// 旧缺陷被暴露成显式 panic。返回「无此文件」与真实 Worker 在文件缺失时的行为一致。
func (f *binaryWorkerStub) HashFile(_ context.Context, in *workerpb.HashFileRequest, _ ...grpc.CallOption) (*workerpb.HashFileResponse, error) {
	f.mu.Lock()
	f.hashRequests = append(f.hashRequests, in)
	resp := f.hashResp
	f.mu.Unlock()
	if resp != nil {
		return resp, nil
	}
	return &workerpb.HashFileResponse{Error: "no such file: " + in.Path}, nil
}

func (f *binaryWorkerStub) hashCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.hashRequests)
}

func (f *binaryWorkerStub) FetchBinary(_ context.Context, in *workerpb.FetchBinaryRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[workerpb.FetchBinaryProgress], error) {
	f.mu.Lock()
	f.requests = append(f.requests, in)
	frames := f.frames
	f.mu.Unlock()
	return &binaryFetchStream{frames: frames}, nil
}

func (f *binaryWorkerStub) lastRequest() *workerpb.FetchBinaryRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

// binaryFetchStream 回放预置帧；帧耗尽后 io.EOF。
// 未预置帧时默认回一帧「成功」终态——多数用例只关心编排，不关心帧细节。
type binaryFetchStream struct {
	grpc.ClientStream
	frames []*workerpb.FetchBinaryProgress
	i      int
}

func (s *binaryFetchStream) Recv() (*workerpb.FetchBinaryProgress, error) {
	if len(s.frames) == 0 {
		if s.i > 0 {
			return nil, io.EOF
		}
		s.i++
		return &workerpb.FetchBinaryProgress{Done: true, Success: true, Size: 1 << 20}, nil
	}
	if s.i < len(s.frames) {
		frame := s.frames[s.i]
		s.i++
		return frame, nil
	}
	return nil, io.EOF
}

// newBinaryHarness 建 binary 搭建测试基座（任务表 + 假 worker）。
func newBinaryHarness(t *testing.T, worker workerpb.WorkerServiceClient) (*ProvisionService, *TaskService, *model.Node) {
	t.Helper()
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskLog{}, &model.Notification{}, &model.PlatformSetting{}))
	node := &model.Node{UUID: "node-bin-" + t.Name(), Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)

	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	instSvc := NewInstanceService(db, NewGroupService(db), pool)
	svc := NewProvisionService(db, pool, instSvc, NewCoreService(), nil)
	taskSvc := NewTaskService(db)
	svc.SetTaskService(taskSvc)
	return svc, taskSvc, node
}

// TestBinaryProvisionAsync_SucceedsAndDerivesStartCommand 走 url 来源的异步搭建：
// 任务进入 succeeded、kind 为 binary_provision、实例为 generic/universal/未绑 JDK、
// 启动命令由文件名派生（验收项 7/8）。
func TestBinaryProvisionAsync_SucceedsAndDerivesStartCommand(t *testing.T) {
	worker := &binaryWorkerStub{}
	svc, taskSvc, node := newBinaryHarness(t, worker)

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID:   node.ID,
		Name:     "beacon-1",
		CoreType: "binary",
		BinarySource: &BinarySource{
			Kind: BinarySourceURL, URL: "https://example.com/beacon", Filename: "beacon-1.1.0-linux-amd64",
			SHA256: strings.Repeat("ab", 32),
		},
	}, 1)
	require.NoError(t, err)
	require.NotNil(t, inst)
	require.NotEmpty(t, taskID)

	task := waitTaskTerminal(t, taskSvc, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State, task.Error)
	require.Equal(t, model.TaskKindBinaryProvision, task.Kind)

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceTypeGeneric, got.Type, "二进制不是 MC Java 进程")
	require.Equal(t, model.InstanceRoleUniversal, got.Role)
	require.Zero(t, got.JDKID, "编译型二进制不绑定 JDK")
	require.Equal(t, "./beacon-1.1.0-linux-amd64", got.StartCommand)
	require.Empty(t, got.StatusReason)
	require.NotEmpty(t, got.ProvisionSpec, "损毁后须可重建")

	// 下发给 Worker 的取件请求：来源 url + 目标文件名 + 摘要。
	req := worker.lastRequest()
	require.NotNil(t, req)
	require.Equal(t, "url", req.SourceKind)
	require.Equal(t, "beacon-1.1.0-linux-amd64", req.DestFilename)
	require.Equal(t, "https://example.com/beacon", req.DownloadUrl)
}

// TestBinaryProvisionAsync_ExplicitStartCommandWins 显式启动命令优先于派生（带参数的场景）。
func TestBinaryProvisionAsync_ExplicitStartCommandWins(t *testing.T) {
	svc, taskSvc, node := newBinaryHarness(t, &binaryWorkerStub{})

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-cfg", CoreType: "binary",
		BinarySource: &BinarySource{Kind: BinarySourceURL, URL: "https://example.com/b", Filename: "beacon"},
		StartCommand: "./beacon -config config.yml",
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, "./beacon -config config.yml", got.StartCommand)
}

// TestBinaryProvisionAsync_FailureDamagedAndRebuildable 取件失败：任务 failed 带错误链、
// 实例进 DAMAGED 且标注「搭建未完成」，provisionSpec 保留可重建（验收项 6）。
func TestBinaryProvisionAsync_FailureDamagedAndRebuildable(t *testing.T) {
	worker := &binaryWorkerStub{frames: []*workerpb.FetchBinaryProgress{
		{Done: true, Success: false, Error: "下载二进制失败: 连接被重置"},
	}}
	svc, taskSvc, node := newBinaryHarness(t, worker)

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-fail", CoreType: "binary",
		BinarySource: &BinarySource{Kind: BinarySourceURL, URL: "https://example.com/b", Filename: "beacon"},
	}, 1)
	require.NoError(t, err, "同步段应成功（失败在后台任务）")

	task := waitTaskTerminal(t, taskSvc, taskID)
	require.Equal(t, model.TaskStateFailed, task.State)
	require.Contains(t, task.Error, "连接被重置", "任务错误应含底层原因")

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusDamaged, got.Status)
	require.True(t, strings.HasPrefix(got.StatusReason, "搭建未完成："), "实际 %q", got.StatusReason)
	require.NotEmpty(t, got.ProvisionSpec, "损毁实例须保留搭建参数供重建")

	// 重建走 binary 分支（复用存库参数，不重解析核心版本）。
	worker.mu.Lock()
	worker.frames = nil // 第二次取件成功
	worker.mu.Unlock()
	rebuildTaskID, err := svc.RebuildInstance(context.Background(), inst.ID, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, rebuildTaskID).State)

	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status, "重建成功应回到 STOPPED")
	require.Empty(t, got.StatusReason)
}

// TestBinaryProvisionAsync_MissingTerminalFrameIsFailure 末帧缺失（流中断）必须判失败，
// 绝不能把「流断了」当成功——否则半截文件会被当成完整二进制落地。
func TestBinaryProvisionAsync_MissingTerminalFrameIsFailure(t *testing.T) {
	worker := &binaryWorkerStub{frames: []*workerpb.FetchBinaryProgress{
		{Downloaded: 1 << 20, Total: 4 << 20}, // 只有中间帧，无 done
	}}
	svc, taskSvc, node := newBinaryHarness(t, worker)

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-trunc", CoreType: "binary",
		BinarySource: &BinarySource{Kind: BinarySourceURL, URL: "https://example.com/b", Filename: "beacon"},
	}, 1)
	require.NoError(t, err)

	task := waitTaskTerminal(t, taskSvc, taskID)
	require.Equal(t, model.TaskStateFailed, task.State)
	require.Contains(t, task.Error, "未返回终态")

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusDamaged, got.Status)
}

// TestBinaryProvisionAsync_InvalidSourceFailsSynchronously 来源不合法必须在同步段失败：
// 不建实例、不登记任务（否则用户拿到一个注定失败的空壳实例）。
func TestBinaryProvisionAsync_InvalidSourceFailsSynchronously(t *testing.T) {
	svc, _, node := newBinaryHarness(t, &binaryWorkerStub{})

	for _, tc := range []struct {
		name string
		req  ProvisionServerRequest
		want string
	}{
		{
			name: "http 明文拒绝",
			req: ProvisionServerRequest{NodeID: node.ID, Name: "b1", CoreType: "binary",
				BinarySource: &BinarySource{Kind: BinarySourceURL, URL: "http://x.com/b", Filename: "b"}},
			want: "https",
		},
		{
			name: "node_file 越界拒绝",
			req: ProvisionServerRequest{NodeID: node.ID, Name: "b2", CoreType: "binary",
				BinarySource: &BinarySource{Kind: BinarySourceNodeFile, NodePath: "/etc/passwd", Filename: "b"}},
			want: "受控放行目录之外",
		},
		{
			name: "缺来源拒绝",
			req:  ProvisionServerRequest{NodeID: node.ID, Name: "b3", CoreType: "binary"},
			want: "缺少 binarySource.kind",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, taskID, err := svc.ProvisionServerAsync(context.Background(), tc.req, 1)
			require.Error(t, err)
			require.Nil(t, inst, "来源不合法不应建实例")
			require.Empty(t, taskID, "来源不合法不应登记任务")
			require.Contains(t, err.Error(), tc.want)
		})
	}

	var count int64
	require.NoError(t, svc.db.Model(&model.Instance{}).Count(&count).Error)
	require.Zero(t, count, "同步段失败不应残留实例")
}

// TestBinaryProvisionAsync_RequiresTaskService 强制异步：无任务中心时拒绝而非降级为同步
// （spec §3.3「所有 binary 搭建必须走 RunAsync，不得同步执行」）。
func TestBinaryProvisionAsync_RequiresTaskService(t *testing.T) {
	svc, _, node := newBinaryHarness(t, &binaryWorkerStub{})
	svc.SetTaskService(nil)

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "b", CoreType: "binary",
		BinarySource: &BinarySource{Kind: BinarySourceURL, URL: "https://x.com/b", Filename: "b"},
	}, 1)
	require.Error(t, err)
	require.Nil(t, inst)
	require.Empty(t, taskID)
	require.Contains(t, err.Error(), "强制异步")
}

// TestBinaryProvisionAsync_ProgressStagesReported 阶段轨迹覆盖 spec §3.3 的表格关键节点。
func TestBinaryProvisionAsync_ProgressStagesReported(t *testing.T) {
	worker := &binaryWorkerStub{frames: []*workerpb.FetchBinaryProgress{
		{Downloaded: 1 << 20, Total: 3 << 20},
		{Downloaded: 3 << 20, Total: 3 << 20},
		{Done: true, Success: true, Size: 3 << 20, Sha256: strings.Repeat("ab", 32)},
	}}
	svc, taskSvc, node := newBinaryHarness(t, worker)

	_, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-stage", CoreType: "binary",
		BinarySource: &BinarySource{Kind: BinarySourceURL, URL: "https://x.com/b", Filename: "beacon"},
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)

	for _, want := range []string{"解析制品来源", "获取二进制", "下载/复制中", "校验完整性", "写入工作目录", "派生启动命令"} {
		require.True(t, stageLogContains(t, taskSvc, taskID, want), "阶段轨迹应含 %q", want)
	}
}

// TestBinaryCore_ResolveBuildShortCircuits binary 必须短路 MC 的版本解析（spec §3.1）。
func TestBinaryCore_ResolveBuildShortCircuits(t *testing.T) {
	svc := NewCoreService()
	_, err := svc.ResolveBuild(context.Background(), "binary", "1.0.0", 0)
	require.ErrorIs(t, err, ErrBinaryCoreNotResolvable)

	_, err = svc.ListVersions(context.Background(), "binary")
	require.ErrorIs(t, err, ErrBinaryCoreNotResolvable)

	require.True(t, IsBinaryCore("binary"))
	require.True(t, IsBinaryCore("BINARY"))
	require.False(t, IsBinaryCore("paper"))
}

// TestBinaryProvisionAsync_StartBlockedWhileInFlight 取件未完成时启动被拒（复用启动闸）。
func TestBinaryProvisionAsync_StartBlockedWhileInFlight(t *testing.T) {
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskLog{}, &model.Notification{}, &model.PlatformSetting{}))
	node := &model.Node{UUID: "node-bin-gate", Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)
	instSvc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())

	inst := &model.Instance{Name: "bin-gate", NodeID: node.ID, Type: model.InstanceTypeGeneric,
		Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "./beacon", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.Task{TaskID: "t-bin", NodeID: node.ID, InstanceID: inst.ID,
		Kind: model.TaskKindBinaryProvision, State: model.TaskStateRunning}).Error)

	err := instSvc.Start(inst.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "二进制尚未取件完成")
}

// TestBinaryProvisionAsync_AssetSourceReusesSignedDelivery kind=asset 走既有签名分发通道：
// 下发给 Worker 的是 CP 上的签名 URL（而非原始资产路径），且摘要取自制品库。
func TestBinaryProvisionAsync_AssetSourceReusesSignedDelivery(t *testing.T) {
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskLog{}, &model.Notification{}, &model.PlatformSetting{}))
	node := &model.Node{UUID: "node-bin-asset", Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)

	content := []byte("fake-beacon-binary")
	sum := sha256.Sum256(content)
	assetSvc := NewAssetService(db, nil)
	asset := &model.Asset{
		Type: model.AssetTypeBlob, Name: "beacon", Filename: "beacon-1.1.0-linux-amd64",
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content)), StorageState: model.AssetStorageHot,
	}
	require.NoError(t, db.AutoMigrate(&model.Asset{}))
	require.NoError(t, db.Create(asset).Error)

	artifactSvc := NewArtifactVersionService(db, assetSvc)
	worker := &binaryWorkerStub{}
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	instSvc := NewInstanceService(db, NewGroupService(db), pool)
	svc := NewProvisionService(db, pool, instSvc, NewCoreService(), nil, artifactSvc)
	taskSvc := NewTaskService(db)
	svc.SetTaskService(taskSvc)
	svc.SetBinaryAssets(assetSvc)
	svc.SetBinaryArtifactVersions(artifactSvc)
	// 无请求上下文时回退平台公共基址。
	require.NoError(t, db.Create(&model.PlatformSetting{
		Key: SettingKeyPlatformPublicBaseURL, Value: "https://cp.example.com",
	}).Error)

	_, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-asset", CoreType: "binary",
		BinarySource: &BinarySource{Kind: BinarySourceAsset, AssetID: asset.ID, Filename: "beacon-1.1.0-linux-amd64"},
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)

	req := worker.lastRequest()
	require.NotNil(t, req)
	require.Equal(t, "url", req.SourceKind, "asset 归一到 url 形态经签名通道交付")
	require.Contains(t, req.DownloadUrl, "https://cp.example.com/binary-assets/", "应走 CP 签名分发端点")
	require.Contains(t, req.DownloadUrl, "token=", "必须带签名 token")
	require.Equal(t, hex.EncodeToString(sum[:]), req.Sha256, "摘要取自制品库（内容寻址）")
}

// TestPlatformPublicBaseURL_RejectsInvalidValue 非法基址不参与拼接（宁报错不拼出坏地址）。
func TestPlatformPublicBaseURL_RejectsInvalidValue(t *testing.T) {
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.PlatformSetting{}))
	svc := &ProvisionService{db: db}

	require.Empty(t, svc.platformPublicBaseURL(), "未配置时为空")

	require.NoError(t, db.Create(&model.PlatformSetting{
		Key: SettingKeyPlatformPublicBaseURL, Value: "not-a-url",
	}).Error)
	require.Empty(t, svc.platformPublicBaseURL(), "非法值不应被采用")

	require.NoError(t, db.Model(&model.PlatformSetting{}).
		Where("key = ?", SettingKeyPlatformPublicBaseURL).
		Update("value", "https://cp.example.com").Error)
	require.Equal(t, "https://cp.example.com", svc.platformPublicBaseURL())
}
