package process

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	networktypes "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDockerClient 是 client.APIClient 的测试替身：嵌入接口（未实现方法调用会 panic，
// 但测试只触发被覆盖的方法），仅实现 dockerStrategy 用到的容器/镜像 API。
type fakeDockerClient struct {
	client.APIClient

	mu sync.Mutex

	images []imagetypes.Summary

	createName   string
	createConfig *containertypes.Config
	createHost   *containertypes.HostConfig
	started      bool
	stopped      bool
	killed       bool
	removed      bool
	pulled       []string

	// attachConn 是 ContainerAttach 返回连接的服务器端（容器侧），测试经此读 stdin / 写 stdout。
	attachConn net.Conn
	// waitCh 投递容器退出码；测试控制何时让容器“退出”。
	waitCh chan containertypes.WaitResponse
}

func newFakeDockerClient() *fakeDockerClient {
	return &fakeDockerClient{
		waitCh: make(chan containertypes.WaitResponse, 1),
	}
}

func (f *fakeDockerClient) ImageList(_ context.Context, _ imagetypes.ListOptions) ([]imagetypes.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images, nil
}

func (f *fakeDockerClient) ImagePull(_ context.Context, ref string, _ imagetypes.PullOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	f.pulled = append(f.pulled, ref)
	// 拉取后视为本地已具备该镜像。
	tag := ref
	if !strings.Contains(ref, ":") {
		tag = ref + ":latest"
	}
	f.images = append(f.images, imagetypes.Summary{RepoTags: []string{tag}})
	f.mu.Unlock()
	return io.NopCloser(strings.NewReader("pull-progress")), nil
}

func (f *fakeDockerClient) ContainerList(_ context.Context, _ containertypes.ListOptions) ([]containertypes.Summary, error) {
	return nil, nil
}

func (f *fakeDockerClient) ContainerCreate(_ context.Context, config *containertypes.Config, hostConfig *containertypes.HostConfig, _ *networktypes.NetworkingConfig, _ *ocispec.Platform, name string) (containertypes.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createName = name
	f.createConfig = config
	f.createHost = hostConfig
	return containertypes.CreateResponse{ID: "fakecontainerid0001"}, nil
}

func (f *fakeDockerClient) ContainerAttach(_ context.Context, _ string, _ containertypes.AttachOptions) (types.HijackedResponse, error) {
	client, server := net.Pipe()
	f.mu.Lock()
	f.attachConn = server
	f.mu.Unlock()
	return types.NewHijackedResponse(client, "application/vnd.docker.multiplexed-stream"), nil
}

func (f *fakeDockerClient) ContainerStart(_ context.Context, _ string, _ containertypes.StartOptions) error {
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	return nil
}

func (f *fakeDockerClient) ContainerStop(_ context.Context, _ string, _ containertypes.StopOptions) error {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
	f.signalExit(0)
	return nil
}

func (f *fakeDockerClient) ContainerKill(_ context.Context, _ string, _ string) error {
	f.mu.Lock()
	f.killed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeDockerClient) ContainerRemove(_ context.Context, _ string, _ containertypes.RemoveOptions) error {
	f.mu.Lock()
	f.removed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeDockerClient) ContainerWait(_ context.Context, _ string, _ containertypes.WaitCondition) (<-chan containertypes.WaitResponse, <-chan error) {
	return f.waitCh, make(chan error)
}

func (f *fakeDockerClient) ContainerInspect(_ context.Context, _ string) (containertypes.InspectResponse, error) {
	return containertypes.InspectResponse{
		ContainerJSONBase: &containertypes.ContainerJSONBase{State: &containertypes.State{Pid: 4242}},
	}, nil
}

func (f *fakeDockerClient) Close() error { return nil }

// signalExit 让 fake 容器“退出”，投递退出码到 waitCh（幂等，避免重复关闭）。
func (f *fakeDockerClient) signalExit(code int64) {
	select {
	case f.waitCh <- containertypes.WaitResponse{StatusCode: code}:
	default:
	}
}

// newDockerStrategyWithFake 构造一个注入了 fake 客户端的 docker 策略。
func newDockerStrategyWithFake(mgr *Manager, spec CommandSpec, fake *fakeDockerClient) *dockerStrategy {
	d := newDockerStrategy(mgr, spec)
	d.newClient = func() (client.APIClient, error) { return fake, nil }
	return d
}

// TestDockerStrategy_StartCreatesContainerWithBindAndPorts 验证启动路径：
// 缺镜像→拉取→按 workDir bind-mount + 端口映射创建容器→attach→start。
func TestDockerStrategy_StartCreatesContainerWithBindAndPorts(t *testing.T) {
	mgr := NewManager(t.TempDir())
	fake := newFakeDockerClient()
	spec := CommandSpec{
		UUID:        "inst-docker-1",
		Name:        "Docker MC",
		Image:       "itzg/minecraft-server:latest",
		WorkDir:     "/host/servers/mc-abc123",
		ProcessType: ProcessTypeDocker,
		PortMappings: []PortMapping{
			{ContainerPort: 25565, HostPort: 25570, Protocol: "tcp"},
			{ContainerPort: 25565, HostPort: 25570, Protocol: "udp"},
		},
		EnvVars: map[string]string{"EULA": "TRUE"},
	}
	d := newDockerStrategyWithFake(mgr, spec, fake)
	// Manager 需登记实例，markStrategyState 等回调才有目标。
	mgr.instances[spec.UUID] = &Instance{UUID: spec.UUID, State: StateStopped, strategy: d, processType: ProcessTypeDocker}

	require.NoError(t, d.Start(context.Background()))
	t.Cleanup(func() { _ = d.Close() })

	assert.Equal(t, StateRunning, d.State())
	assert.True(t, fake.started, "容器应被启动")
	assert.Equal(t, "jianmanager-inst-docker-1", fake.createName, "容器命名应为 jianmanager-<uuid>")

	// 镜像本地缺失→应触发拉取。
	assert.Equal(t, []string{"itzg/minecraft-server:latest"}, fake.pulled)

	// 工作目录应 bind-mount 到 /data。
	require.Len(t, fake.createHost.Mounts, 1)
	assert.Equal(t, spec.WorkDir, fake.createHost.Mounts[0].Source)
	assert.Equal(t, containerWorkDir, fake.createHost.Mounts[0].Target)

	// 端口映射应发布 tcp + udp 25565→25570。
	assert.Len(t, fake.createHost.PortBindings, 2)
	assert.Contains(t, fake.createConfig.Env, "EULA=TRUE")
}

// TestDockerStrategy_StartInjectsResourceLimits 验证 docker 资源限额注入 HostConfig（FR-079）：
// cpu_limit→NanoCPUs（×1e9）、mem_limit_mb→Memory（×1024×1024）；disk_limit 不注入 HostConfig。
func TestDockerStrategy_StartInjectsResourceLimits(t *testing.T) {
	mgr := NewManager(t.TempDir())
	fake := newFakeDockerClient()
	fake.images = []imagetypes.Summary{{RepoTags: []string{"x:latest"}}}
	spec := CommandSpec{
		UUID:        "inst-reslimit",
		Image:       "x:latest",
		ProcessType: ProcessTypeDocker,
		CPULimit:    1.5,
		MemLimitMB:  2048,
		DiskLimitMB: 10240,
	}
	d := newDockerStrategyWithFake(mgr, spec, fake)
	mgr.instances[spec.UUID] = &Instance{UUID: spec.UUID, State: StateStopped, strategy: d}

	require.NoError(t, d.Start(context.Background()))
	t.Cleanup(func() { _ = d.Close() })

	// 1.5 核 → 1.5e9 NanoCPUs。
	assert.Equal(t, int64(1_500_000_000), fake.createHost.NanoCPUs, "cpu_limit 应注入 NanoCPUs")
	// 2048 MiB → 字节。
	assert.Equal(t, int64(2048)*1024*1024, fake.createHost.Memory, "mem_limit_mb 应注入 Memory")
}

// TestDockerStrategy_StartNoLimitsLeavesUnset 验证零值限额不注入（0=不限制，沿用 Docker 默认）。
func TestDockerStrategy_StartNoLimitsLeavesUnset(t *testing.T) {
	mgr := NewManager(t.TempDir())
	fake := newFakeDockerClient()
	fake.images = []imagetypes.Summary{{RepoTags: []string{"x:latest"}}}
	d := newDockerStrategyWithFake(mgr, CommandSpec{UUID: "inst-nolimit", Image: "x:latest", ProcessType: ProcessTypeDocker}, fake)
	mgr.instances["inst-nolimit"] = &Instance{UUID: "inst-nolimit", State: StateStopped, strategy: d}

	require.NoError(t, d.Start(context.Background()))
	t.Cleanup(func() { _ = d.Close() })

	assert.Zero(t, fake.createHost.NanoCPUs, "未设限额时 NanoCPUs 应为 0")
	assert.Zero(t, fake.createHost.Memory, "未设限额时 Memory 应为 0")
}

// TestDockerStrategy_StartMissingImage 验证缺镜像名时启动失败并置 CRASHED。
func TestDockerStrategy_StartMissingImage(t *testing.T) {
	mgr := NewManager(t.TempDir())
	fake := newFakeDockerClient()
	d := newDockerStrategyWithFake(mgr, CommandSpec{UUID: "inst-noimg", ProcessType: ProcessTypeDocker}, fake)
	mgr.instances["inst-noimg"] = &Instance{UUID: "inst-noimg", State: StateStopped, strategy: d}

	err := d.Start(context.Background())
	require.Error(t, err)
	assert.Equal(t, StateCrashed, d.State())
	assert.False(t, fake.started)
}

// TestDockerStrategy_ImageAlreadyPresentSkipsPull 验证本地已有镜像时不拉取。
func TestDockerStrategy_ImageAlreadyPresentSkipsPull(t *testing.T) {
	mgr := NewManager(t.TempDir())
	fake := newFakeDockerClient()
	fake.images = []imagetypes.Summary{{RepoTags: []string{"itzg/minecraft-server:latest"}}}
	d := newDockerStrategyWithFake(mgr, CommandSpec{UUID: "inst-img", Image: "itzg/minecraft-server:latest", ProcessType: ProcessTypeDocker}, fake)
	mgr.instances["inst-img"] = &Instance{UUID: "inst-img", State: StateStopped, strategy: d}

	require.NoError(t, d.Start(context.Background()))
	t.Cleanup(func() { _ = d.Close() })
	assert.Empty(t, fake.pulled, "本地已有镜像不应触发拉取")
}

// TestDockerStrategy_OutputDemuxToManager 验证容器多路复用 stdout 被解复用并路由到 Manager 回调。
func TestDockerStrategy_OutputDemuxToManager(t *testing.T) {
	mgr := NewManager(t.TempDir())
	var mu sync.Mutex
	var got string
	mgr.SetOutputHandler(func(_ string, stream string, data []byte) {
		if stream == "stdout" {
			mu.Lock()
			got += string(data)
			mu.Unlock()
		}
	})
	fake := newFakeDockerClient()
	d := newDockerStrategyWithFake(mgr, CommandSpec{UUID: "inst-out", Image: "x:latest", ProcessType: ProcessTypeDocker}, fake)
	mgr.instances["inst-out"] = &Instance{UUID: "inst-out", State: StateStopped, strategy: d}
	fake.images = []imagetypes.Summary{{RepoTags: []string{"x:latest"}}}

	require.NoError(t, d.Start(context.Background()))
	t.Cleanup(func() { _ = d.Close() })

	// 容器侧用 stdcopy 帧写一行 stdout。
	stdoutW := stdcopy.NewStdWriter(fake.attachConn, stdcopy.Stdout)
	_, err := stdoutW.Write([]byte("[Server] Done!\n"))
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got == "[Server] Done!\n"
	}, 2*time.Second, 10*time.Millisecond, "stdout 应解复用后路由到 Manager")
}

// TestDockerStrategy_SendCommandWritesStdin 验证 SendCommand 把命令写到容器 stdin（带换行）。
func TestDockerStrategy_SendCommandWritesStdin(t *testing.T) {
	mgr := NewManager(t.TempDir())
	fake := newFakeDockerClient()
	fake.images = []imagetypes.Summary{{RepoTags: []string{"x:latest"}}}
	d := newDockerStrategyWithFake(mgr, CommandSpec{UUID: "inst-cmd", Image: "x:latest", ProcessType: ProcessTypeDocker}, fake)
	mgr.instances["inst-cmd"] = &Instance{UUID: "inst-cmd", State: StateStopped, strategy: d}
	require.NoError(t, d.Start(context.Background()))
	t.Cleanup(func() { _ = d.Close() })

	readDone := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := fake.attachConn.Read(buf)
		readDone <- string(buf[:n])
	}()
	require.NoError(t, d.SendCommand("say hi"))
	select {
	case line := <-readDone:
		assert.Equal(t, "say hi\n", line)
	case <-time.After(2 * time.Second):
		t.Fatal("超时：stdin 未收到命令")
	}
}

// TestDockerStrategy_GetPID 验证从容器 inspect 取宿主侧 PID。
func TestDockerStrategy_GetPID(t *testing.T) {
	mgr := NewManager(t.TempDir())
	fake := newFakeDockerClient()
	fake.images = []imagetypes.Summary{{RepoTags: []string{"x:latest"}}}
	d := newDockerStrategyWithFake(mgr, CommandSpec{UUID: "inst-pid", Image: "x:latest", ProcessType: ProcessTypeDocker}, fake)
	mgr.instances["inst-pid"] = &Instance{UUID: "inst-pid", State: StateStopped, strategy: d}
	require.NoError(t, d.Start(context.Background()))
	t.Cleanup(func() { _ = d.Close() })
	assert.Equal(t, 4242, d.GetPID())
}

// TestDockerStrategy_StopGraceful 验证 Stop 经 stdin 下发停止命令后 ContainerStop。
func TestDockerStrategy_StopGraceful(t *testing.T) {
	mgr := NewManager(t.TempDir())
	fake := newFakeDockerClient()
	fake.images = []imagetypes.Summary{{RepoTags: []string{"x:latest"}}}
	d := newDockerStrategyWithFake(mgr, CommandSpec{UUID: "inst-stop", Image: "x:latest", StopCommand: "stop", ProcessType: ProcessTypeDocker}, fake)
	mgr.instances["inst-stop"] = &Instance{UUID: "inst-stop", State: StateStopped, strategy: d}
	require.NoError(t, d.Start(context.Background()))
	t.Cleanup(func() { _ = d.Close() })

	// 排空容器侧 stdin（Stop 会写停止命令）以免 SendCommand 在 net.Pipe 上阻塞。
	go func() {
		buf := make([]byte, 64)
		_, _ = fake.attachConn.Read(buf)
	}()

	require.NoError(t, d.Stop())
	assert.True(t, fake.stopped, "应调用 ContainerStop")
	// Stop 返回后策略为 STOPPING（与 direct 策略一致，最终 STOPPED 由 waitLoop 在容器退出后落定）。
	// fake 的 ContainerStop 触发退出码，waitLoop 据此把状态收敛到 STOPPED。
	assert.Eventually(t, func() bool { return d.State() == StateStopped }, 2*time.Second, 10*time.Millisecond,
		"容器退出后状态应收敛到 STOPPED")
}

// TestPortConfig 验证端口映射转 Docker ExposedPorts/PortBindings。
func TestPortConfig(t *testing.T) {
	exposed, bindings := portConfig([]PortMapping{
		{ContainerPort: 25565, HostPort: 25570, Protocol: "tcp"},
		{ContainerPort: 25565, HostPort: 25570, Protocol: "udp"},
		{ContainerPort: 0, HostPort: 1}, // 非法，跳过
		{ContainerPort: 19132, HostPort: 19140},
	})
	assert.Len(t, exposed, 3)
	assert.Len(t, bindings, 3)
	for port, binds := range bindings {
		require.Len(t, binds, 1)
		assert.NotEmpty(t, binds[0].HostPort)
		assert.NotEmpty(t, port.Port())
	}
}

// TestApplyResourceLimits 验证 CPU/内存限额到 HostConfig 的换算（FR-079）。
func TestApplyResourceLimits(t *testing.T) {
	tests := []struct {
		name         string
		cpu          float64
		memMB        int64
		wantNanoCPUs int64
		wantMemory   int64
	}{
		{"both set", 2, 1024, 2_000_000_000, 1024 * 1024 * 1024},
		{"fractional cpu", 0.5, 512, 500_000_000, 512 * 1024 * 1024},
		{"only cpu", 1, 0, 1_000_000_000, 0},
		{"only mem", 0, 256, 0, 256 * 1024 * 1024},
		{"none", 0, 0, 0, 0},
		{"negative ignored", -1, -1, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hostCfg := &containertypes.HostConfig{}
			applyResourceLimits(hostCfg, tt.cpu, tt.memMB)
			assert.Equal(t, tt.wantNanoCPUs, hostCfg.NanoCPUs)
			assert.Equal(t, tt.wantMemory, hostCfg.Memory)
		})
	}
}

// TestImagePresent 验证本地镜像命中判断（含自动补 :latest）。
func TestImagePresent(t *testing.T) {
	images := []imagetypes.Summary{
		{RepoTags: []string{"itzg/minecraft-server:latest"}},
		{RepoTags: []string{"alpine:3.20"}},
	}
	assert.True(t, imagePresent(images, "itzg/minecraft-server:latest"))
	assert.True(t, imagePresent(images, "itzg/minecraft-server")) // 自动补 :latest
	assert.True(t, imagePresent(images, "alpine:3.20"))
	assert.False(t, imagePresent(images, "alpine:3.19"))
	assert.False(t, imagePresent(images, "nginx"))
}

// TestDockerEnv 验证环境变量转 KEY=VALUE 列表。
func TestDockerEnv(t *testing.T) {
	out := dockerEnv(map[string]string{"A": "1", "B": "2"})
	assert.ElementsMatch(t, []string{"A=1", "B=2"}, out)
	assert.Empty(t, dockerEnv(nil))
}
