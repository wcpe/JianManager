package process

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

// containerWorkDir 是实例工作目录在容器内的固定挂载点。
// 宿主侧系统分配目录（ADR-010）bind-mount 到此，使文件管理/备份/配置编辑走同一套宿主路径。
// 与 itzg/minecraft-server 等常见 MC 镜像约定一致（容器内 /data 即服务器目录）。
const containerWorkDir = "/data"

// dockerStopGracePeriod 是发送停止命令后等待容器自行退出的兜底上限。
// 超过则由 Docker 守护进程 SIGKILL（ContainerStop 的 timeout 语义）。
const dockerStopGracePeriod = 30 * time.Second

// dockerStrategy 通过本机 Docker Engine API 管理容器化实例（ADR-019）。
// Worker 经本机 Docker 守护进程创建/启动/停止容器，stdio 经 attach 接现有终端与日志采集。
// 容器进程的隔离由 Docker 守护进程提供，故不叠 daemon wrapper（区别于 ADR-003 的 daemon 策略）。
type dockerStrategy struct {
	mu   sync.Mutex
	spec CommandSpec
	mgr  *Manager

	cli         client.APIClient
	containerID string
	// newClient 构造 Docker 客户端；默认 dockerClientFromEnv，测试可注入 fake。
	newClient func() (client.APIClient, error)
	// attach 是与容器 stdin/stdout/stderr 的劫持连接（tty=false 时为多路复用流）。
	attach *types.HijackedResponse
	state  InstanceState
	// crashCount 记录连续崩溃次数，用于指数退避（语义同 direct 策略）。
	crashCount int
	wg         sync.WaitGroup
	closed     bool
}

// newDockerStrategy 构造 docker 策略。
// Docker 客户端在 Start 时惰性创建（FromEnv 自动发现本机守护进程），
// 以免构造阶段就因守护进程不可达而失败、影响其它启动方式。
func newDockerStrategy(mgr *Manager, spec CommandSpec) *dockerStrategy {
	return &dockerStrategy{
		spec:  spec,
		mgr:   mgr,
		state: StateStopped,
	}
}

// containerName 返回实例对应的容器名（jianmanager-<uuid>）。
// 命名稳定便于排障与孤儿回收，且天然防重名。
func (d *dockerStrategy) containerName() string {
	return "jianmanager-" + d.spec.UUID
}

// dockerClientFromEnv 从环境（FromEnv，含 DOCKER_HOST）创建本机 Docker 客户端。
func dockerClientFromEnv() (client.APIClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("连接本机 Docker 守护进程失败（请确认已安装并运行 Docker）: %w", err)
	}
	return cli, nil
}

// ensureClient 惰性创建并缓存 Docker 客户端。
func (d *dockerStrategy) ensureClient() error {
	if d.cli != nil {
		return nil
	}
	factory := d.newClient
	if factory == nil {
		factory = dockerClientFromEnv
	}
	cli, err := factory()
	if err != nil {
		return err
	}
	d.cli = cli
	return nil
}

func (d *dockerStrategy) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureClient(); err != nil {
		d.state = StateCrashed
		return err
	}

	image := strings.TrimSpace(d.spec.Image)
	if image == "" {
		d.state = StateCrashed
		return fmt.Errorf("docker 模式缺少镜像名（image）")
	}

	// 启动前确保镜像存在：本地缺失则拉取。
	if err := ensureImage(ctx, d.cli, image); err != nil {
		d.state = StateCrashed
		return fmt.Errorf("准备镜像 %s 失败: %w", image, err)
	}

	// 清理可能残留的同名旧容器（上次异常退出未清理），避免 create 撞名。
	d.removeExistingContainer(ctx)

	exposed, bindings := portConfig(d.spec.PortMappings)

	cfg := &containertypes.Config{
		Image:        image,
		Env:          dockerEnv(d.spec.EnvVars),
		WorkingDir:   containerWorkDir,
		ExposedPorts: exposed,
		// tty=false + 三路 attach：stdout/stderr 多路复用，便于分流到日志采集（FR-049）。
		Tty:          false,
		OpenStdin:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}
	// StartCommand 非空时覆盖镜像默认 entrypoint 的 CMD（结构化启动派生命令在容器内执行）；
	// 为空则交给镜像 entrypoint（如 itzg/minecraft-server 自管启动）。
	if cmd := strings.TrimSpace(d.spec.StartCommand); cmd != "" {
		cfg.Cmd = []string{"sh", "-c", cmd}
	}

	hostCfg := &containertypes.HostConfig{
		PortBindings: bindings,
	}
	// 资源限额注入 cgroup（FR-079，见 ADR-019）：CPU 核数→NanoCPUs、内存 MiB→字节。
	// 0 值表示不限制，保持 Docker 默认（不写 Resources 字段）。磁盘限额 v1 不注入
	// （bind-mount 工作目录的配额依赖存储驱动，HostConfig 无法简单施加）。
	applyResourceLimits(hostCfg, d.spec.CPULimit, d.spec.MemLimitMB)
	// 工作目录 bind-mount 进容器（ADR-010 数据根的宿主绝对路径）。
	if d.spec.WorkDir != "" {
		hostCfg.Mounts = []mount.Mount{{
			Type:   mount.TypeBind,
			Source: d.spec.WorkDir,
			Target: containerWorkDir,
		}}
	}

	created, err := d.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, d.containerName())
	if err != nil {
		d.state = StateCrashed
		return fmt.Errorf("创建容器失败: %w", err)
	}
	d.containerID = created.ID

	// attach 必须在 start 前建立，否则会丢失容器启动初期的输出。
	attach, err := d.cli.ContainerAttach(ctx, d.containerID, containertypes.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		d.state = StateCrashed
		return fmt.Errorf("attach 容器失败: %w", err)
	}
	d.attach = &attach

	if err := d.cli.ContainerStart(ctx, d.containerID, containertypes.StartOptions{}); err != nil {
		attach.Close()
		d.attach = nil
		d.state = StateCrashed
		return fmt.Errorf("启动容器失败: %w", err)
	}

	d.state = StateRunning
	slog.Info("docker 实例已启动", "instanceId", d.spec.UUID, "container", d.containerID[:12], "image", image)

	d.wg.Add(2)
	go d.pipeOutput(attach.Reader)
	go d.waitLoop()
	return nil
}

// pipeOutput 把容器多路复用输出解复用为 stdout/stderr 路由到 Manager 输出回调。
// tty=false 时 Docker 用 stdcopy 帧封装 stdout/stderr，StdCopy 负责拆帧。
func (d *dockerStrategy) pipeOutput(r io.Reader) {
	defer d.wg.Done()
	outW := &instanceWriter{manager: d.mgr, instanceID: d.spec.UUID, stream: "stdout"}
	errW := &instanceWriter{manager: d.mgr, instanceID: d.spec.UUID, stream: "stderr"}
	if _, err := stdcopy.StdCopy(outW, errW, r); err != nil && err != io.EOF {
		slog.Debug("docker 输出流结束", "instanceId", d.spec.UUID, "err", err)
	}
}

// waitLoop 等待容器退出并按需触发指数退避重启（语义同 direct.waitLoop）。
func (d *dockerStrategy) waitLoop() {
	defer d.wg.Done()

	d.mu.Lock()
	cli := d.cli
	containerID := d.containerID
	d.mu.Unlock()
	if cli == nil || containerID == "" {
		return
	}

	statusCh, errCh := cli.ContainerWait(context.Background(), containerID, containertypes.WaitConditionNotRunning)
	var exitCode int64
	select {
	case st := <-statusCh:
		exitCode = st.StatusCode
	case err := <-errCh:
		slog.Debug("ContainerWait 错误", "instanceId", d.spec.UUID, "err", err)
	}

	d.mu.Lock()
	if d.attach != nil {
		d.attach.Close()
		d.attach = nil
	}
	// Stopping/Stopped 视为正常停止（容器被 Stop/Kill）。
	if d.state == StateStopping || d.state == StateStopped {
		d.state = StateStopped
		d.mu.Unlock()
		slog.Info("docker 实例已停止", "instanceId", d.spec.UUID)
		return
	}
	d.state = StateCrashed
	d.crashCount++
	crashCount := d.crashCount
	d.mu.Unlock()

	// 同步崩溃状态到 Manager 记账并扇出（与 direct 策略一致，使 Start 守卫允许重启）。
	d.mgr.markStrategyState(d.spec.UUID, StateCrashed)
	slog.Warn("docker 实例崩溃", "instanceId", d.spec.UUID, "exitCode", exitCode, "crashCount", crashCount)

	if d.spec.AutoRestart {
		delay := backoffDelay(crashCount)
		slog.Info("将在延迟后自动重启", "instanceId", d.spec.UUID, "delay", delay, "crashCount", crashCount)
		time.Sleep(delay)

		d.mu.Lock()
		currentState := d.state
		d.mu.Unlock()
		if currentState == StateCrashed {
			if restartErr := d.mgr.Start(d.spec.UUID); restartErr != nil {
				slog.Error("自动重启失败", "instanceId", d.spec.UUID, "error", restartErr)
			}
		}
	}
}

func (d *dockerStrategy) Stop() error {
	d.mu.Lock()
	cli := d.cli
	containerID := d.containerID
	stopCmd := d.spec.StopCommand
	d.state = StateStopping
	d.mu.Unlock()

	if cli == nil || containerID == "" {
		return nil
	}

	// 优雅停止：先经 stdin 下发停止命令（MC 后端 stop / 代理 end），给进程自行落盘退出的机会；
	// 命令为空时回退默认 stop。随后 ContainerStop 在宽限期后兜底 SIGKILL。
	if stopCmd == "" {
		stopCmd = "stop"
	}
	_ = d.SendCommand(stopCmd)

	timeout := int(dockerStopGracePeriod.Seconds())
	if err := cli.ContainerStop(context.Background(), containerID, containertypes.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("停止容器失败: %w", err)
	}
	return nil
}

func (d *dockerStrategy) Kill() error {
	d.mu.Lock()
	cli := d.cli
	containerID := d.containerID
	d.mu.Unlock()

	if cli == nil || containerID == "" {
		return nil
	}
	// 强制终止并删除容器，确保停止后端口/卷彻底释放（停/删干净）。
	if err := cli.ContainerKill(context.Background(), containerID, "SIGKILL"); err != nil {
		slog.Debug("ContainerKill 失败（可能已退出）", "instanceId", d.spec.UUID, "err", err)
	}
	return cli.ContainerRemove(context.Background(), containerID, containertypes.RemoveOptions{Force: true})
}

func (d *dockerStrategy) SendCommand(command string) error {
	d.mu.Lock()
	attach := d.attach
	d.mu.Unlock()
	if attach == nil {
		return fmt.Errorf("实例 %s 的容器 stdin 不可用", d.spec.UUID)
	}
	_, err := attach.Conn.Write([]byte(command + "\n"))
	return err
}

func (d *dockerStrategy) State() InstanceState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

func (d *dockerStrategy) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	attach := d.attach
	d.attach = nil
	cli := d.cli
	d.mu.Unlock()

	// 释放 attach 连接（不终止容器，停止由 Stop/Kill 负责）。
	if attach != nil {
		attach.Close()
	}
	if cli != nil {
		return cli.Close()
	}
	return nil
}

// GetPID 返回容器主进程在宿主上的 PID，未运行时返回 0。
// 供 OS 层进程内存采集复用（与 direct/daemon 一致的指标路径）。
func (d *dockerStrategy) GetPID() int {
	d.mu.Lock()
	cli := d.cli
	containerID := d.containerID
	d.mu.Unlock()
	if cli == nil || containerID == "" {
		return 0
	}
	info, err := cli.ContainerInspect(context.Background(), containerID)
	if err != nil || info.State == nil {
		return 0
	}
	return info.State.Pid
}

// removeExistingContainer 删除同名残留容器（上次异常退出未清理时）。
// 调用方持有 d.mu。
func (d *dockerStrategy) removeExistingContainer(ctx context.Context) {
	if d.cli == nil {
		return
	}
	name := d.containerName()
	// 按名精确查找（包含已停止容器）。
	containers, err := d.cli.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return
	}
	for _, c := range containers {
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == name {
				_ = d.cli.ContainerRemove(ctx, c.ID, containertypes.RemoveOptions{Force: true})
				return
			}
		}
	}
}

// dockerEnv 把实例自定义环境变量转为容器 Env 列表（KEY=VALUE）。
// docker 模式不注入宿主 JDK（JAVA_HOME/PATH），JDK 随镜像提供（ADR-019）。
func dockerEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// nanoCPUsPerCPU 是一个 CPU 核对应的 NanoCPUs 单位（Docker 以 10^-9 核计量 CPU 配额）。
const nanoCPUsPerCPU = 1_000_000_000

// bytesPerMiB 是 1 MiB 的字节数（内存限额以 MiB 下发、以字节注入）。
const bytesPerMiB = 1024 * 1024

// applyResourceLimits 把 CPU 核数与内存 MiB 上限注入 HostConfig 的 cgroup 限额字段（FR-079）。
// cpuLimit/memLimitMB 为 0 时不设对应字段（保持 Docker 默认=不限制）。负值按未设处理。
func applyResourceLimits(hostCfg *containertypes.HostConfig, cpuLimit float64, memLimitMB int64) {
	if cpuLimit > 0 {
		hostCfg.NanoCPUs = int64(cpuLimit * nanoCPUsPerCPU)
	}
	if memLimitMB > 0 {
		hostCfg.Memory = memLimitMB * bytesPerMiB
	}
}

// portConfig 把端口映射转为 Docker 的 ExposedPorts 与 PortBindings。
// 每条映射把容器内端口发布到宿主端口（宿主端口来自 FR-032 端口池）。
// 协议默认 tcp；MC query 等 UDP 端口由调用方在映射中显式标注。
func portConfig(mappings []PortMapping) (nat.PortSet, nat.PortMap) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, pm := range mappings {
		if pm.ContainerPort <= 0 || pm.HostPort <= 0 {
			continue
		}
		proto := pm.Protocol
		if proto == "" {
			proto = "tcp"
		}
		port, err := nat.NewPort(proto, strconv.Itoa(pm.ContainerPort))
		if err != nil {
			continue
		}
		exposed[port] = struct{}{}
		bindings[port] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(pm.HostPort)}}
	}
	return exposed, bindings
}

// ensureImage 确保镜像在本地存在，缺失则从 registry 拉取。
// 拉取流必须读尽（drain）才算完成，否则 Docker 守护进程可能中断拉取。
func ensureImage(ctx context.Context, cli client.APIClient, ref string) error {
	images, err := cli.ImageList(ctx, imagetypes.ListOptions{})
	if err != nil {
		return fmt.Errorf("列出本地镜像失败: %w", err)
	}
	if imagePresent(images, ref) {
		return nil
	}
	rc, err := cli.ImagePull(ctx, ref, imagetypes.PullOptions{})
	if err != nil {
		return fmt.Errorf("拉取镜像失败: %w", err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("读取拉取进度失败: %w", err)
	}
	return nil
}

// imagePresent 判断本地镜像列表是否已含指定引用（按 RepoTags 匹配，自动补 :latest）。
func imagePresent(images []imagetypes.Summary, ref string) bool {
	want := ref
	if !strings.Contains(ref, ":") || strings.HasSuffix(ref, "/") {
		want = ref + ":latest"
	}
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == ref || tag == want {
				return true
			}
		}
	}
	return false
}
