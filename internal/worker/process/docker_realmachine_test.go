//go:build docker_integration

// 本文件是 docker 模式的真机集成测试，需本机 Docker 守护进程可用。
// 默认不参与构建/CI（build tag docker_integration），手动跑：
//
//	go test -tags docker_integration -run TestDockerRealMachine ./internal/worker/process/ -v
//
// 用 alpine 轻量镜像驱动真实 dockerStrategy 走完整生命周期（拉镜像→创建→attach→
// stdin/stdout→停止→清理），验证 FR-078 容器化实例真机链路（ADR-019）。
package process

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
)

func mustPort(t *testing.T, p string) nat.Port {
	t.Helper()
	port, err := nat.NewPort(strings.SplitN(p, "/", 2)[1], strings.SplitN(p, "/", 2)[0])
	require.NoError(t, err)
	return port
}

const realImage = "alpine:3.20"

// TestDockerRealMachine_Lifecycle 真机跑通：拉镜像→创建容器（卷挂载+端口映射）→attach→
// stdin 输入经容器 echo 回 stdout→Stop→容器清理。
func TestDockerRealMachine_Lifecycle(t *testing.T) {
	mgr := NewManager(t.TempDir())

	var mu sync.Mutex
	var output strings.Builder
	mgr.SetOutputHandler(func(_ string, _ string, data []byte) {
		mu.Lock()
		output.Write(data)
		mu.Unlock()
	})

	uuid := "realmachine-docker-1"
	workDir := t.TempDir()
	spec := CommandSpec{
		UUID:        uuid,
		Name:        "RealMachine Docker",
		Image:       realImage,
		WorkDir:     workDir,
		ProcessType: ProcessTypeDocker,
		// 读 stdin 逐行 echo（验证 stdin→stdout 经 attach 解复用路由）。
		StartCommand: "while read line; do echo \"GOT:$line\"; done",
		StopCommand:  "bye",
		PortMappings: []PortMapping{
			{ContainerPort: 25565, HostPort: 34567, Protocol: "tcp"},
		},
	}
	mgr.instances[uuid] = &Instance{UUID: uuid, State: StateStopped, strategy: newDockerStrategy(mgr, spec), processType: ProcessTypeDocker}
	d := mgr.instances[uuid].strategy.(*dockerStrategy)

	require.NoError(t, d.Start(context.Background()), "docker 真机启动应成功（拉 alpine + 创建 + attach + start）")
	t.Cleanup(func() { _ = d.Kill() })

	require.Equal(t, StateRunning, d.State())
	require.NotEmpty(t, d.containerID)

	// 容器主进程在宿主上应有真实 PID。
	require.Greater(t, d.GetPID(), 0, "应能取到容器主进程宿主 PID")

	// 经 stdin 发一行，容器 echo "GOT:hello" 回来（验证 stdin + stdout 解复用全链路）。
	require.NoError(t, d.SendCommand("hello"))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(output.String(), "GOT:hello")
	}, 10*time.Second, 50*time.Millisecond, "stdin 输入应经容器 echo 回 stdout 并路由到 Manager")

	// 验证端口确实发布到宿主（容器 inspect 的 NetworkSettings.Ports）。
	requirePortPublished(t, d, "25565/tcp", "34567")

	// 停止：StopCommand 让 read 循环结束 → 容器退出；ContainerStop 兜底。
	require.NoError(t, d.Stop())
	require.Eventually(t, func() bool { return d.State() == StateStopped }, 40*time.Second, 100*time.Millisecond,
		"Stop 后容器退出，状态应收敛到 STOPPED")

	// Kill 清理容器，确认同名容器不再存在（停/删干净）。
	require.NoError(t, d.Kill())
	requireContainerGone(t, "jianmanager-"+uuid)
}

// TestDockerRealMachine_ImageManagement 真机跑通镜像管理：列出→拉取→列出含拉取项。
// 不删除（避免误删本机其它用途镜像）；删除路径由 fake 单测覆盖。
func TestDockerRealMachine_ImageManagement(t *testing.T) {
	ctx := context.Background()

	// 列出本机镜像应成功（Docker 可用）。
	before, err := ListDockerImages(ctx)
	require.NoError(t, err, "真机 ListDockerImages 应成功")
	t.Logf("本机镜像数: %d", len(before))

	// 拉取 alpine:3.20（可能已存在，幂等）。
	require.NoError(t, PullDockerImage(ctx, realImage), "真机 PullDockerImage 应成功")

	// 拉取后列表应包含该镜像。
	after, err := ListDockerImages(ctx)
	require.NoError(t, err)
	found := false
	for _, img := range after {
		for _, tag := range img.Tags {
			if tag == realImage {
				found = true
			}
		}
	}
	require.True(t, found, "拉取后镜像列表应含 %s", realImage)
}

func requirePortPublished(t *testing.T, d *dockerStrategy, containerPort, hostPort string) {
	t.Helper()
	info, err := d.cli.ContainerInspect(context.Background(), d.containerID)
	require.NoError(t, err)
	require.NotNil(t, info.NetworkSettings)
	binds, ok := info.NetworkSettings.Ports[mustPort(t, containerPort)]
	require.True(t, ok, "容器端口 %s 应已发布", containerPort)
	require.NotEmpty(t, binds)
	found := false
	for _, b := range binds {
		if b.HostPort == hostPort {
			found = true
		}
	}
	require.True(t, found, "宿主端口 %s 应绑定到容器端口 %s", hostPort, containerPort)
}

func requireContainerGone(t *testing.T, name string) {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	defer cli.Close()
	containers, err := cli.ContainerList(context.Background(), containertypes.ListOptions{All: true})
	require.NoError(t, err)
	for _, c := range containers {
		for _, n := range c.Names {
			require.NotEqual(t, name, strings.TrimPrefix(n, "/"), "容器 %s 应已删除", name)
		}
	}
}
