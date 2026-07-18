package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	psproc "github.com/shirou/gopsutil/v4/process"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/worker/artifactcache"
	"github.com/wcpe/JianManager/internal/worker/bot"
	"github.com/wcpe/JianManager/internal/worker/decompiler"
	"github.com/wcpe/JianManager/internal/worker/jdk"
	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/internal/worker/pkgmgr"
	"github.com/wcpe/JianManager/internal/worker/process"
	wruntime "github.com/wcpe/JianManager/internal/worker/runtime"
	"github.com/wcpe/JianManager/internal/worker/runtimescan"
	"github.com/wcpe/JianManager/internal/worker/search"
	"github.com/wcpe/JianManager/internal/worker/taskreg"
	"github.com/wcpe/JianManager/internal/worker/ws"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// instanceEvent 内部事件，分发给所有 StreamInstanceEvents 订阅者。
// 既承载状态变更（由 Manager 状态回调产生），也承载进程输出（stdout/stderr，由 EmitOutput 产生），
// 统一走一套订阅者扇出。Kind 区分两类，StreamInstanceEvents 按 Kind 决定 gRPC 事件的 type 与 data。
type instanceEvent struct {
	// Kind 为 "state_change" 或 "stdout"/"stderr"（即原始流名）。
	Kind      string
	UUID      string
	OldState  string
	NewState  string
	Data      string // Kind 为输出时的日志正文
	Timestamp int64
}

// Server Worker Node gRPC 服务器实现。
// 仅实现 Control Plane 反向调用 Worker 的 RPC（实例操作、文件操作、指标）。
// 生命周期 RPC（Register/Heartbeat）由 Worker 主动发起，不由 CP 调用 Worker，
// 因此走 UnimplementedWorkerServiceServer 的默认实现，返回 codes.Unimplemented，
// 避免因嵌入接口导致未实现方法被调用时 panic。
type Server struct {
	workerpb.UnimplementedWorkerServiceServer
	manager   *process.Manager
	nodeUUID  string
	collector *metrics.Collector
	jdkMgr    *jdk.Manager
	// root 是本节点数据根，用于把 CP 下发的相对工作目录解析为绝对路径。参见 ADR-010。
	root *dataroot.Root
	// botMgr 管理本节点 Bot（spawn bot-worker Node 子进程，stdin/stdout IPC）。参见 ADR-006。
	// 为 nil 表示本节点未启用 Bot 能力，相关 RPC 返回明确错误。由 SetBotManager 注入。
	botMgr *bot.Manager
	// botFleet 允许 Fleet RPC 复用 Manager，并为协议测试注入最小替身。
	botFleet        botFleetManager
	botStartMu      sync.Mutex
	botBatchMu      sync.Mutex
	botBatchResults map[string]*botBatchCacheEntry
	// botEventMu 保护 botEventSubs，StreamBotEvents 订阅/取消订阅时加锁。
	botEventMu     sync.Mutex
	botEventSubs   []chan *bot.BotWorkerEvent
	botEventCancel func()

	// eventMu 保护 eventSubs，StreamInstanceEvents 订阅/取消订阅时加锁。
	eventMu   sync.Mutex
	eventSubs []chan instanceEvent

	// 插件桥（ServerProbe 反向 WS，FR-065，见 ADR-016）。
	// bridge 提供「实例 UUID→探针会话」表与下发能力；为 nil 表示本节点未启用插件桥。由 SetPluginBridge 注入。
	bridge *ws.PluginBridgeServer

	// terminalWSAddr 本机终端 WS 服务回环地址（FR-281 M2，见 ADR-066）：
	// TerminalSession 据此回环拨自身 WS 终端服务桥接会话。由 SetTerminalWSAddr 注入。
	terminalWSAddr string

	// decompiler 解析/缓存 CFR 反编译器 jar（FR-075，见 ADR-018）。
	// 为 nil 表示本节点未启用反编译能力，DecompileClass 返回降级错误。由 SetDecompiler 注入。
	decompiler *decompiler.Provider
	// pluginEventMu 保护 pluginEventSubs，StreamPluginEvents 订阅/取消订阅时加锁。
	// 插件事件与实例事件刻意分两套订阅者总线：二者消费方、过滤维度、生命周期均不同。
	pluginEventMu   sync.Mutex
	pluginEventSubs []chan *workerpb.PluginEvent

	// restartFn 是自更新（FR-081）替换二进制后执行的重启动作；nil 时用默认 selfupdate.Restart。
	// 由 SetRestartFunc 注入（测试用，避免真重启）。
	restartFn func()
	// execPath 覆盖自更新（FR-081）待替换的可执行文件路径；空时用 os.Executable()。
	// 由 SetExecutablePath 注入（测试用，避免替换真二进制）。
	execPath string
	// httpClient 出站 client（经进程级代理，FR-174/ADR-037）：Worker 升级二进制下载经此 client。
	// 由 SetHTTPClient 注入；为 nil 时回退 http.DefaultClient（向后兼容）。
	httpClient *http.Client
	// httpProvider 运行时出站持有者（FR-185/ADR-043）：非 nil 时每次取当前 client，
	// 使 CP 经心跳下发的代理改动即时生效。优先于 httpClient。
	httpProvider func() *http.Client
	// 全文搜索索引（FR-074，见 ADR-017）。每实例一份 *search.Index，懒创建。
	// 索引落数据根 var/index/<instance-uuid>/，是 Worker 本地派生资产，不进 CP DB。
	// searchIgnore 为用户配置追加的忽略 glob（worker.yml search.ignore），叠加内置默认集。
	searchMu      sync.Mutex
	searchIndexes map[string]*search.Index
	searchIgnore  []string

	// tasks 运行中长任务内存登记表（FR-183，见 ADR-040）。
	// InstallJDK 异步执行时经此表更新进度/日志；心跳侧读 Snapshot 随心跳上报给 CP。
	// 非 nil（NewServer 初始化）。
	tasks *taskreg.Registry

	// cache 节点本地内容寻址制品缓存（FR-178）。仅服务 DownloadCore 的核心 jar：
	// 命中即从缓存秒拷到工作目录（免重下），未命中下载校验后存入。为 nil 表示未启用缓存
	// （DownloadCore 退化为原有「每次都下载」行为，向后兼容）。由 SetArtifactCache 注入。
	cache *artifactcache.Cache
	// coreFlightMu/coreFlights 同核心并发下载单飞（FR-330）：同缓存键（sha256 或组合键）的
	// 并发 DownloadCore 只有领队真正走网络，其余等待领队完成后从缓存秒取，不重复下载也不互相踩。
	coreFlightMu sync.Mutex
	coreFlights  map[string]*coreFlight

	// runtimeScanner 运行时扫描器（FR-298 节点运行时库）：ScanRuntimes 按常见安装路径
	// 发现 jdk/nodejs 候选。为 nil 表示未启用（返回空候选）。由 SetRuntimeScanner 注入。
	runtimeScanner *runtimescan.Scanner
	// runtimeMgr 非 JDK 运行时安装管理器（FR-299，首批 Node.js）：InstallRuntime/RemoveRuntime
	// 经此下载安装/托管删除。为 nil 表示未启用（RPC 返回明确错误）。由 SetRuntimeManager 注入。
	runtimeMgr *wruntime.Manager
	// pkgMgr 节点包管理器（FR-306）：PM 偏好（corepack 激活）与托管 .npmrc registry 配置读写。
	// 为 nil 表示未启用（PM 配置 RPC 返回明确错误）。由 SetPkgManager 注入。
	pkgMgr *pkgmgr.Manager
	// importJDKProbe 导入探测（FR-302）中「某目录是否为 JDK」的探测钩子，仅测试注入替身：
	// 真探测（jdk.detectAt 语义）要跑 bin/java，伪目录布局单测不可得。为 nil 时走 jdkMgr.Probe。
	importJDKProbe func(dir string) (*workerpb.ImportJdkCandidate, bool)
}

// NewServer 创建 Worker gRPC 服务器。
// 注册 Manager 状态变更回调，将事件扇出到所有 StreamInstanceEvents 订阅者。
// jdkMgr 可为 nil（未启用 JDK 托管时）。root 用于解析相对工作目录，可为 nil（按绝对路径处理）。
func NewServer(manager *process.Manager, nodeUUID string, collector *metrics.Collector, jdkMgr *jdk.Manager, root *dataroot.Root) *Server {
	s := &Server{
		manager:         manager,
		nodeUUID:        nodeUUID,
		collector:       collector,
		jdkMgr:          jdkMgr,
		root:            root,
		botBatchResults: make(map[string]*botBatchCacheEntry),
		searchIndexes:   make(map[string]*search.Index),
		tasks:           taskreg.New(),
	}
	manager.SetStateChangeHandler(func(uuid string, oldState, newState process.InstanceState) {
		s.dispatch(instanceEvent{
			Kind:      "state_change",
			UUID:      uuid,
			OldState:  string(oldState),
			NewState:  string(newState),
			Timestamp: time.Now().Unix(),
		})
	})
	return s
}

// SetHTTPClient 注入出站 client（经进程级代理，FR-174/ADR-037）：Worker 升级二进制下载经此 client。
// 由 main 装配；不调用则回退 http.DefaultClient（向后兼容，测试不受影响）。
func (s *Server) SetHTTPClient(c *http.Client) {
	s.httpClient = c
}

// SetHTTPClientProvider 注入运行时出站持有者（FR-185/ADR-043）：每次下载取当前 client，
// 使 CP 经心跳下发的代理改动即时生效。优先于 SetHTTPClient 注入的固定 client。
func (s *Server) SetHTTPClientProvider(p func() *http.Client) {
	s.httpProvider = p
}

// outboundClient 返回出站 client：优先运行时持有者（取当前），其次固定注入，再回退 DefaultClient。
func (s *Server) outboundClient() *http.Client {
	if s.httpProvider != nil {
		if c := s.httpProvider(); c != nil {
			return c
		}
	}
	if s.httpClient != nil {
		return s.httpClient
	}
	return http.DefaultClient
}

// SetArtifactCache 注入节点本地制品缓存（FR-178）：DownloadCore 据此命中复用核心 jar。
// 由 main 装配；不调用则 DownloadCore 退化为原有「每次都下载」行为（向后兼容，测试不受影响）。
func (s *Server) SetArtifactCache(c *artifactcache.Cache) {
	s.cache = c
}

// dispatch 把一条内部事件非阻塞地扇出给所有 StreamInstanceEvents 订阅者。
// 订阅者消费太慢则丢弃，绝不阻塞产生方（状态回调或进程输出回调）。
func (s *Server) dispatch(evt instanceEvent) {
	s.eventMu.Lock()
	subs := make([]chan instanceEvent, len(s.eventSubs))
	copy(subs, s.eventSubs)
	s.eventMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- evt:
		default:
		}
	}
}

// EmitOutput 把实例进程输出（stdout/stderr）作为事件扇出到 StreamInstanceEvents 订阅者，
// 供 CP 侧采集落库（FR-049）。stream 为原始流名（stdout/stderr）。
// 与 WS 终端广播相互独立：终端面向交互、本路径面向持久化，二者从同一份输出分流。
func (s *Server) EmitOutput(instanceUUID, stream, data string) {
	if data == "" {
		return
	}
	s.dispatch(instanceEvent{
		Kind:      stream,
		UUID:      instanceUUID,
		Data:      data,
		Timestamp: time.Now().Unix(),
	})
}

// CreateInstance 创建实例。
// CP 下发的 WorkDir 按数据根相对存储（系统分配的 var/servers/<slug>-<shortid>），
// 此处解析为本节点绝对路径并确保目录存在。参见 ADR-010。
func (s *Server) CreateInstance(ctx context.Context, req *workerpb.CreateInstanceRequest) (*workerpb.CreateInstanceResponse, error) {
	if _, err := s.registerInstanceFromProto(req); err != nil {
		return &workerpb.CreateInstanceResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

// registerInstanceFromProto 把 CP 下发的实例规格填入进程管理器（不启动进程）。
// 解析相对 WorkDir 为本节点绝对路径并建目录（ADR-010），调 manager.Create 注册为 STOPPED，
// docker 实例补记镜像/端口/限额（启动时随 spec 定型，ADR-019 / FR-079）。
// 返回 created：true=本次新登记，false=实例已存在（已刷新随启动定型的超时与 docker 配置）。
// CreateInstance（CP 建实例/启动前幂等重注册）与 ResyncInstances（重连重推，见 ADR-050）共用此逻辑。
func (s *Server) registerInstanceFromProto(req *workerpb.CreateInstanceRequest) (created bool, err error) {
	workDir := req.WorkDir
	if s.root != nil && workDir != "" {
		workDir = s.root.Abs(workDir)
		if mkErr := os.MkdirAll(workDir, 0o755); mkErr != nil {
			return false, fmt.Errorf("创建工作目录失败: %v", mkErr)
		}
	}
	cerr := s.manager.Create(
		req.InstanceUuid,
		req.Name,
		req.StartCommand,
		req.StopCommand,
		workDir,
		req.EnvVars,
		req.AutoRestart,
		process.ProcessType(req.ProcessType),
		req.JdkPath,
		req.JdkBinPath,
		int(req.ProbePort),
		int(req.GracefulStopTimeoutSeconds),
	)
	if cerr != nil {
		// 实例已存在（CP 在每次启动前幂等重注册，或重连重推命中已恢复的 RUNNING 实例）：
		// 刷新随启动定型的优雅停止超时与 docker 配置，使设置/镜像/端口变更对下一次启动生效
		// （FR-063 / FR-078）。该错误对幂等路径是良性的（按已注册处理）。
		if strings.Contains(cerr.Error(), "已存在") {
			s.manager.SetGracefulStopTimeout(req.InstanceUuid, int(req.GracefulStopTimeoutSeconds))
			// 刷新启动配置（启动命令 / 绑定 JDK / 环境变量），使配置编辑（FR-233 重绑 JDK 等）对下次启动生效——
			// 否则 worker 保留旧 spec，重绑的 JDK 不被采用、preflight 仍报「未绑定 JDK」。
			s.manager.SetLaunchConfig(req.InstanceUuid, req.StartCommand, req.JdkPath, req.JdkBinPath, req.EnvVars, req.AutoRestart)
			if req.ProcessType == string(process.ProcessTypeDocker) {
				s.manager.SetDockerConfig(req.InstanceUuid, req.Image, portMappingsFromProto(req.PortMappings), req.CpuLimit, req.MemLimitMb, req.DiskLimitMb)
			}
			return false, nil
		}
		return false, cerr
	}
	// docker 模式：把镜像、端口映射与资源限额存到实例记账，启动时随 spec 定型（ADR-019 / FR-079）。
	if req.ProcessType == string(process.ProcessTypeDocker) {
		s.manager.SetDockerConfig(req.InstanceUuid, req.Image, portMappingsFromProto(req.PortMappings), req.CpuLimit, req.MemLimitMb, req.DiskLimitMb)
	}
	return true, nil
}

// ResyncInstances Worker 重连/重注册后接收 CP 重推的整节点实例规格，填充内存注册表（见 ADR-050）。
// 「只补不覆盖」：已在表者（含 RecoverDaemonInstances 恢复的 RUNNING 实例）跳过，
// 不在表者按 STOPPED 登记且不启动进程——只让停机实例可被文件/配置/归档 op 定位工作目录，
// 绝不擅自拉起。修 bug #2（重启后停机实例 op 报「实例不存在」）。
func (s *Server) ResyncInstances(ctx context.Context, req *workerpb.ResyncInstancesRequest) (*workerpb.ResyncInstancesResponse, error) {
	var registered, skipped int32
	for _, spec := range req.Instances {
		if spec == nil || spec.InstanceUuid == "" {
			continue
		}
		// 已在内存表 → 跳过（不覆盖 RUNNING 恢复实例；重复重推幂等）。
		if _, exists := s.manager.GetInstance(spec.InstanceUuid); exists {
			skipped++
			continue
		}
		created, err := s.registerInstanceFromProto(spec)
		if err != nil {
			slog.Warn("重连同步实例失败，跳过", "instanceId", spec.InstanceUuid, "error", err)
			continue
		}
		if created {
			registered++
		} else {
			// registerInstanceFromProto 在「已存在」时返回 created=false（GetInstance 与 Create 之间
			// 的竞态窗口；正常路径上文已跳过），按跳过计。
			skipped++
		}
	}
	if registered > 0 || skipped > 0 {
		slog.Info("已接收 CP 重连重推的实例规格", "registered", registered, "skipped", skipped, "total", len(req.Instances))
	}
	return &workerpb.ResyncInstancesResponse{Registered: registered, Skipped: skipped}, nil
}

// StartInstance 启动实例。
func (s *Server) StartInstance(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.InstanceActionResponse, error) {
	if err := s.manager.Start(req.InstanceUuid); err != nil {
		return &workerpb.InstanceActionResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// PreflightStartInstance 启动前同步预检（FR-314）：返回逐项检查聚合结果。success=全部通过，
// message=拼接失败原因（供 CP 同步拦截配置错误、写 statusReason）。实例未注册按预检失败处理。
func (s *Server) PreflightStartInstance(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.InstanceActionResponse, error) {
	checks, err := s.manager.PreflightStart(req.InstanceUuid)
	if err != nil {
		slog.Warn("启动预检无法执行", "instanceId", req.InstanceUuid, "error", err)
		return &workerpb.InstanceActionResponse{Success: false, Error: "启动预检无法执行: " + err.Error()}, nil
	}
	var failed []string
	for _, c := range checks {
		if !c.OK && c.Message != "" {
			failed = append(failed, c.Message)
		}
	}
	if len(failed) == 0 {
		return &workerpb.InstanceActionResponse{Success: true}, nil
	}
	return &workerpb.InstanceActionResponse{Success: false, Error: strings.Join(failed, "；")}, nil
}

// StopInstance 停止实例。
func (s *Server) StopInstance(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.InstanceActionResponse, error) {
	if err := s.manager.Stop(req.InstanceUuid); err != nil {
		return &workerpb.InstanceActionResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// RestartInstance 重启实例。
func (s *Server) RestartInstance(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.InstanceActionResponse, error) {
	if err := s.manager.Restart(req.InstanceUuid); err != nil {
		return &workerpb.InstanceActionResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// KillInstance 强制终止实例。
func (s *Server) KillInstance(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.InstanceActionResponse, error) {
	if err := s.manager.Kill(req.InstanceUuid); err != nil {
		return &workerpb.InstanceActionResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// SendCommand 向实例发送命令。
func (s *Server) SendCommand(ctx context.Context, req *workerpb.SendCommandRequest) (*workerpb.SendCommandResponse, error) {
	if err := s.manager.SendCommand(req.InstanceUuid, req.Command); err != nil {
		return &workerpb.SendCommandResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.SendCommandResponse{Success: true}, nil
}

// GetInstanceStatus 获取实例状态。
func (s *Server) GetInstanceStatus(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.GetInstanceStatusResponse, error) {
	state, err := s.manager.GetState(req.InstanceUuid)
	if err != nil {
		return nil, fmt.Errorf("获取实例状态失败: %w", err)
	}
	return &workerpb.GetInstanceStatusResponse{
		InstanceUuid: req.InstanceUuid,
		State:        string(state),
	}, nil
}

// ListInstances 列出所有实例。
func (s *Server) ListInstances(ctx context.Context, req *workerpb.ListInstancesRequest) (*workerpb.ListInstancesResponse, error) {
	instances := s.manager.ListInstances()
	result := make([]*workerpb.InstanceInfo, len(instances))
	for i, inst := range instances {
		state, _ := s.manager.GetState(inst)
		result[i] = &workerpb.InstanceInfo{
			InstanceUuid: inst,
			State:        string(state),
		}
	}
	return &workerpb.ListInstancesResponse{Instances: result}, nil
}

// GetNodeMetrics 获取节点指标。
func (s *Server) GetNodeMetrics(ctx context.Context, req *workerpb.GetNodeMetricsRequest) (*workerpb.GetNodeMetricsResponse, error) {
	if s.collector == nil {
		return &workerpb.GetNodeMetricsResponse{}, nil
	}

	m := s.collector.Collect()
	return &workerpb.GetNodeMetricsResponse{
		CpuUsage:      m.CPUUsage,
		MemoryUsage:   m.MemoryUsage,
		DiskUsage:     m.DiskUsage,
		MemoryUsedMb:  m.MemoryUsedMB,
		MemoryTotalMb: m.MemoryTotalMB,
		DiskUsedMb:    m.DiskUsedMB,
		DiskTotalMb:   m.DiskTotalMB,
	}, nil
}

// GetInstanceMetrics 获取实例指标（纯 ServerProbe，FR-067 退役 RCON 后）。
// 抓取 ServerProbe /metrics（localhost:probe_port，FR-010 富指标：TPS/MSPT/堆/线程/世界）；
// 探针未部署或抓取失败时 TPS/在线人数返回 N/A（-1，probe_available=false），不再有 RCON 兜底。
// OS 进程内存近似仍作为 memory_mb 的可用回退（与探针无关）。
func (s *Server) GetInstanceMetrics(ctx context.Context, req *workerpb.GetInstanceMetricsRequest) (*workerpb.GetInstanceMetricsResponse, error) {
	resp := &workerpb.GetInstanceMetricsResponse{}

	// 获取实例状态，确认实例存在
	state, err := s.manager.GetState(req.InstanceUuid)
	if err != nil {
		return resp, fmt.Errorf("实例不存在: %w", err)
	}

	// 仅运行中的实例有指标
	if state != "RUNNING" {
		return resp, nil
	}

	// docker 容器：宿主 PID 在 Docker Desktop/WSL2 下通常不是可见进程，OS 进程内存采不到，
	// 改用 Engine API 的 cgroup 统计取 CPU%/内存（#6）；非 docker 实例回退 OS 进程内存近似 MC JVM 内存。
	if cpu, mem, memLimit, ok := s.manager.GetInstanceDockerStats(ctx, req.InstanceUuid); ok {
		resp.MemoryMb = mem / (1024 * 1024)
		resp.CpuPercent = cpu
		// 容器内存上限作内存条分母（未配置限额时为宿主总内存）。docker 无 JVM 探针，
		// 复用 HeapMaxMb 字段承载「内存上限」语义，仅卡片实时视图消费（不入时序）。
		resp.HeapMaxMb = memLimit / (1024 * 1024)
	} else if pid := s.manager.GetInstancePID(req.InstanceUuid); pid > 0 {
		// 受管根进程在 daemon 模式是 Go wrapper（RSS ~20MB），真实游戏 JVM 隔着中间 sh 层；
		// 必须解析到游戏进程本身再采样，否则概览/卡片显示 wrapper 体格（真机 1.4GB 显 20MB）。
		if proc := resolveGameProc(ctx, int32(pid)); proc != nil {
			if memInfo, err := proc.MemoryInfoWithContext(ctx); err == nil && memInfo != nil {
				resp.MemoryMb = int64(memInfo.RSS / 1024 / 1024)
			}
			// FR-343 系统级基础指标：无需 ServerProbe，直接采进程 CPU%/运行时长，让未部署/未连探针的
			// 运行中实例在概览也有真实 CPU 与运行时长（内存 RSS 上面已采）。CPUPercent 为累计 CPU%
			//（与 FR-170 进程采样器同源、非阻塞）；uptime 由进程创建时刻推算。探针可用时下方会覆盖。
			if cpu, err := proc.CPUPercentWithContext(ctx); err == nil {
				resp.CpuPercent = cpu
			}
			if createMs, err := proc.CreateTimeWithContext(ctx); err == nil && createMs > 0 {
				resp.UptimeSeconds = float64(time.Now().UnixMilli()-createMs) / 1000
			}
		}
	}

	// 优先 ServerProbe /metrics（FR-010 富指标，取代 RCON 粗指标）。
	// 探针与实例同机，抓 localhost:probe_port；本机 IP 白名单放行，无需 token。
	if req.ProbePort > 0 {
		if snap, perr := metrics.ScrapeServerProbe("localhost", int(req.ProbePort), ""); perr == nil {
			resp.Tps = float32(snap.TPS)
			resp.OnlinePlayers = snap.PlayersOnline
			if snap.HeapUsedBytes > 0 {
				resp.MemoryMb = snap.HeapUsedBytes / (1024 * 1024)
			}
			resp.MsptMillis = float32(snap.MSPTAvgMillis)
			resp.Threads = snap.Threads
			resp.CpuPercent = snap.SystemCPULoad * 100
			resp.HeapMaxMb = snap.HeapMaxBytes / (1024 * 1024)
			resp.UptimeSeconds = snap.UptimeSeconds
			for name, w := range snap.Worlds {
				resp.Worlds = append(resp.Worlds, &workerpb.WorldMetric{
					Name:         name,
					LoadedChunks: w.LoadedChunks,
					Entities:     w.Entities,
					TileEntities: w.TileEntities,
				})
			}
			resp.ProbeAvailable = true
			return resp, nil
		}
		// 探针未就绪/抓取失败 → 指标 N/A（FR-067 退役 RCON 后无兜底）。
	}

	// 探针未部署或抓取失败：TPS/在线人数为 N/A（-1），probe_available 默认 false。
	// memory_mb 若上面已由 OS 进程内存填充则保留，否则为 0。
	resp.Tps = -1
	resp.OnlinePlayers = -1
	return resp, nil
}

// resolveGameProc 从受管根进程解析真正的游戏进程（JVM）。daemon 模式根进程是 Go wrapper，
// 真实 java 隔着中间 shell 层（wrapper→sh→java）；direct 模式根进程可能就是 shell。
// 深度优先遍历后代：优先返回名为 java 的最深进程，无 java 命名者回退最深后代，无后代回退根进程。
// FR-343 指标与 FR-344 运行时环境都读游戏进程本身，而非托管壳。
func resolveGameProc(ctx context.Context, pid int32) *psproc.Process {
	root, err := psproc.NewProcessWithContext(ctx, pid)
	if err != nil {
		return nil
	}
	deepest, deepestDepth := root, -1
	var javaDeepest *psproc.Process
	javaDepth := -1
	seen := map[int32]struct{}{}
	var walk func(p *psproc.Process, depth int)
	walk = func(p *psproc.Process, depth int) {
		// 进程树规模守卫：受管实例树很小（wrapper/sh/java + 少量子工具），64 足够且防环。
		if p == nil || len(seen) >= 64 {
			return
		}
		if _, ok := seen[p.Pid]; ok {
			return
		}
		seen[p.Pid] = struct{}{}
		if depth > deepestDepth {
			deepestDepth, deepest = depth, p
		}
		if name, err := p.NameWithContext(ctx); err == nil {
			if n := strings.ToLower(name); n == "java" || n == "java.exe" {
				if depth > javaDepth {
					javaDepth, javaDeepest = depth, p
				}
			}
		}
		children, err := p.ChildrenWithContext(ctx)
		if err != nil {
			return
		}
		for _, c := range children {
			walk(c, depth+1)
		}
	}
	walk(root, 0)
	if javaDeepest != nil {
		return javaDeepest
	}
	return deepest
}

// GetInstanceEnv 返回运行中实例 Java 进程的实际环境（FR-344 环境变量下区）。
// 取进程 PID → gopsutil Environ 读进程环境（Linux 走 /proc/pid/environ）；未运行/平台受限则 available=false + note。
func (s *Server) GetInstanceEnv(ctx context.Context, req *workerpb.GetInstanceEnvRequest) (*workerpb.GetInstanceEnvResponse, error) {
	resp := &workerpb.GetInstanceEnvResponse{}
	state, err := s.manager.GetState(req.InstanceUuid)
	if err != nil {
		return resp, fmt.Errorf("实例不存在: %w", err)
	}
	if state != "RUNNING" {
		resp.Note = "实例未运行，无运行时环境"
		return resp, nil
	}
	pid := s.manager.GetInstancePID(req.InstanceUuid)
	if pid <= 0 {
		resp.Note = "无法获取实例进程 PID"
		return resp, nil
	}
	// 解析到游戏 JVM 本身：wrapper 的环境缺 composeEnv 注入的 JAVA_HOME/PATH 前缀与自定义变量，
	// 直读根进程会把「运行时实际环境」展示成 wrapper 继承的 systemd 环境（FR-344 下区语义是 JVM）。
	proc := resolveGameProc(ctx, int32(pid))
	if proc == nil {
		resp.Note = "读取进程失败：进程不存在"
		return resp, nil
	}
	environ, err := proc.EnvironWithContext(ctx)
	if err != nil || len(environ) == 0 {
		resp.Note = "读取进程环境不支持或为空（部分平台如 Windows 受限）"
		return resp, nil
	}
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	resp.Env = m
	resp.Available = true
	return resp, nil
}

// IssueTerminalToken 签发终端 token。
// 有意不在 Worker 侧实现：终端 token 由 Control Plane 签发并代理（见 FR-007/FR-019 决策），
// 浏览器经 CP 拿到 token 后直连 Worker WS。此处返回明确错误而非走 Unimplemented，
// 便于调用方区分「该能力归属 CP」与「Worker 未实现」。
func (s *Server) IssueTerminalToken(ctx context.Context, req *workerpb.IssueTerminalTokenRequest) (*workerpb.IssueTerminalTokenResponse, error) {
	return nil, fmt.Errorf("终端 token 由 Control Plane 签发，Worker 不实现此 RPC")
}

// ListJDKs 列出 Worker 本地 JDK 注册表。
func (s *Server) ListJDKs(ctx context.Context, req *workerpb.ListJDKsRequest) (*workerpb.ListJDKsResponse, error) {
	if s.jdkMgr == nil {
		return &workerpb.ListJDKsResponse{}, nil
	}
	infos, err := s.jdkMgr.List()
	if err != nil {
		return nil, fmt.Errorf("扫描 JDK 失败: %w", err)
	}
	out := make([]*workerpb.JDKInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, &workerpb.JDKInfo{
			Vendor:       i.Vendor,
			MajorVersion: int32(i.MajorVersion),
			Version:      i.Version,
			Arch:         i.Arch,
			Path:         i.Path,
			Managed:      i.Managed,
		})
	}
	return &workerpb.ListJDKsResponse{Jdks: out}, nil
}

// InstallJDK 下载并安装指定 JDK。
//
// 当 req.TaskId 非空时走**异步**路径（FR-183，见 ADR-040）：登记内存任务表为 running、
// 启动后台 goroutine 执行下载解压（经进度回调更新任务表），RPC **立即返回** task_id，
// 不再阻塞最长 20min；进度/日志/终态经心跳上报给 CP，CP 据终态落 NodeJDK + 发站内信。
// 当 req.TaskId 为空时回退**同步**路径（向后兼容旧 CP）。
func (s *Server) InstallJDK(ctx context.Context, req *workerpb.InstallJDKRequest) (*workerpb.InstallJDKResponse, error) {
	if s.jdkMgr == nil {
		return &workerpb.InstallJDKResponse{Success: false, Error: "JDK 管理器未启用"}, nil
	}

	// 异步路径：启动即返回 task_id，后台执行。
	if req.TaskId != "" {
		taskID := req.TaskId
		// 可取消 context 登记进任务表：强制停止（FR-227）时由心跳侧 registry.Cancel 真中断下载。
		taskCtx, cancel := context.WithCancel(context.Background())
		s.tasks.Start(taskID, cancel)
		// 复制下发参数，goroutine 不持有 req（避免在 RPC 返回后引用其底层内存）。
		vendor, major, arch := req.Vendor, int(req.MajorVersion), req.Arch
		version := req.Version // 具体版本（FR-178，可选；非空经 foojay 解析）
		installDir, mirrorBase := req.InstallDir, req.MirrorBase
		go func() {
			defer cancel() // goroutine 退出即释放 context（正常完成/失败/被取消均释放）
			info, err := s.jdkMgr.InstallWithProgress(taskCtx, vendor, major, version, arch, installDir, mirrorBase,
				func(percent int, line string) {
					s.tasks.SetProgress(taskID, int32(percent))
					if line != "" {
						s.tasks.AppendLog(taskID, line)
					}
				})
			if err != nil {
				s.tasks.AppendLog(taskID, "安装失败: "+err.Error())
				s.tasks.Fail(taskID, err.Error())
				return
			}
			// 成功结果序列化进任务 result，供 CP 终态副作用落 NodeJDK。
			result, _ := json.Marshal(jdkResult{
				Vendor:       info.Vendor,
				MajorVersion: info.MajorVersion,
				Version:      info.Version,
				Arch:         info.Arch,
				Path:         info.Path,
				Managed:      info.Managed,
			})
			s.tasks.Succeed(taskID, string(result))
		}()
		return &workerpb.InstallJDKResponse{Success: true, TaskId: taskID}, nil
	}

	// 同步路径（向后兼容）。
	info, err := s.jdkMgr.Install(req.Vendor, int(req.MajorVersion), req.Arch, req.InstallDir, req.MirrorBase)
	if err != nil {
		return &workerpb.InstallJDKResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstallJDKResponse{
		Success: true,
		Jdk: &workerpb.JDKInfo{
			Vendor:       info.Vendor,
			MajorVersion: int32(info.MajorVersion),
			Version:      info.Version,
			Arch:         info.Arch,
			Path:         info.Path,
			Managed:      info.Managed,
		},
	}, nil
}

// jdkResult 是 jdk_install 任务成功时写入 TaskSnapshot.result 的 JSON 结构（FR-183，见 ADR-040）。
// CP 收到终态 succeeded 时反序列化它落一条 model.NodeJDK。字段与 workerpb.JDKInfo 一一对应。
type jdkResult struct {
	Vendor       string `json:"vendor"`
	MajorVersion int    `json:"majorVersion"`
	Version      string `json:"version"`
	Arch         string `json:"arch"`
	Path         string `json:"path"`
	Managed      bool   `json:"managed"`
}

// ProbeJDK 探测节点上某路径的 JDK 信息（FR-228），供登记前自动填厂商/版本/架构（不再手填）。
func (s *Server) ProbeJDK(ctx context.Context, req *workerpb.ProbeJDKRequest) (*workerpb.ProbeJDKResponse, error) {
	if s.jdkMgr == nil {
		return &workerpb.ProbeJDKResponse{Valid: false, Error: "JDK 管理器未启用"}, nil
	}
	info, err := s.jdkMgr.Probe(req.Path)
	if err != nil {
		return &workerpb.ProbeJDKResponse{Valid: false, Error: err.Error()}, nil
	}
	return &workerpb.ProbeJDKResponse{
		Valid:        true,
		Vendor:       info.Vendor,
		MajorVersion: int32(info.MajorVersion),
		Version:      info.Version,
		Arch:         info.Arch,
		JavaHome:     info.Path,
	}, nil
}

// TaskSnapshots 返回运行中任务的心跳快照（FR-183）。心跳侧据此随心跳上报给 CP。
func (s *Server) TaskSnapshots() []*workerpb.TaskSnapshot {
	return s.tasks.Snapshot()
}

// DropTask 从内存任务表移除任务（终态被心跳上报后调用，避免重复上报）。
func (s *Server) DropTask(taskID string) {
	s.tasks.Drop(taskID)
}

// CancelTask 强制停止运行中任务（FR-227）：真中断 Worker 操作（取消其下载 context）并置 canceled 终态，
// 由心跳侧收到 CP 下发的 cancel_task_ids 时调用。幂等（不存在/已终态返回 false）。
func (s *Server) CancelTask(taskID string) bool {
	return s.tasks.Cancel(taskID)
}

// RemoveJDK 删除托管 JDK。
func (s *Server) RemoveJDK(ctx context.Context, req *workerpb.RemoveJDKRequest) (*workerpb.RemoveJDKResponse, error) {
	if s.jdkMgr == nil {
		return &workerpb.RemoveJDKResponse{Success: false, Error: "JDK 管理器未启用"}, nil
	}
	if err := s.jdkMgr.Remove(req.Path); err != nil {
		return &workerpb.RemoveJDKResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.RemoveJDKResponse{Success: true}, nil
}

// StreamInstanceEvents 订阅实例状态变更事件流。
// CP 调用此 RPC 后，Worker 持续推送实例状态转换事件（STOPPED→STARTING→RUNNING 等）。
// instance_uuid 为空时表示订阅所有实例。流关闭时自动取消订阅。
func (s *Server) StreamInstanceEvents(req *workerpb.StreamInstanceEventsRequest, stream workerpb.WorkerService_StreamInstanceEventsServer) error {
	ch := make(chan instanceEvent, 64)
	s.eventMu.Lock()
	s.eventSubs = append(s.eventSubs, ch)
	s.eventMu.Unlock()

	defer func() {
		s.eventMu.Lock()
		for i, sub := range s.eventSubs {
			if sub == ch {
				s.eventSubs = append(s.eventSubs[:i], s.eventSubs[i+1:]...)
				break
			}
		}
		s.eventMu.Unlock()
		close(ch)
	}()

	slog.Info("StreamInstanceEvents 订阅开始", "filter", req.InstanceUuid)

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			// 按 instance_uuid 过滤
			if req.InstanceUuid != "" && evt.UUID != req.InstanceUuid {
				continue
			}
			pbEvt := &workerpb.InstanceEvent{
				InstanceUuid: evt.UUID,
				Type:         evt.Kind,
				Timestamp:    evt.Timestamp,
			}
			if evt.Kind == "state_change" {
				pbEvt.Data = fmt.Sprintf("%s→%s", evt.OldState, evt.NewState)
			} else {
				// stdout/stderr：原样下发日志正文
				pbEvt.Data = evt.Data
			}
			if err := stream.Send(pbEvt); err != nil {
				return err
			}
		}
	}
}

// === Bot 操作 ===
//
// Worker 侧 Bot 由 bot.Manager spawn 的 Node 子进程（bot-worker）经 stdin/stdout IPC 管理，
// 一个 bot-worker 进程承载本节点全部 Bot。参见 ADR-006。
// botMgr 为 nil 表示本节点未启用 Bot 能力，相关 RPC 返回明确错误而非 panic。
// Bot 状态（connecting/connected/disconnected）由 CP 经 ListBots 拉取回填（懒拉取，见 BotService.refreshStatus）。

// SetBotManager 注入 Bot 管理器，由 Worker 主进程在启动时设置。
func (s *Server) SetBotManager(m *bot.Manager) {
	s.botEventMu.Lock()
	if s.botEventCancel != nil {
		s.botEventCancel()
		s.botEventCancel = nil
	}
	s.botMgr = m
	s.botFleet = m
	s.botEventMu.Unlock()

	if m == nil {
		return
	}
	events, cancel := m.SubscribeEvents(128)
	s.botEventMu.Lock()
	s.botEventCancel = cancel
	s.botEventMu.Unlock()

	go func() {
		for event := range events {
			s.dispatchBotEvent(event)
		}
	}()
}

// dispatchBotEvent 把 bot-worker 事件非阻塞地扇出给所有 StreamBotEvents 订阅者。
func (s *Server) dispatchBotEvent(event *bot.BotWorkerEvent) {
	s.botEventMu.Lock()
	defer s.botEventMu.Unlock()
	for _, ch := range s.botEventSubs {
		select {
		case ch <- event:
		default:
			// 慢订阅者跳过本帧，避免阻塞 bot-worker stdout 读取循环。
		}
	}
}

// SetSearchIgnore 设置全文搜索的追加忽略规则（worker.yml search.ignore，FR-074）。
// 叠加在内置默认忽略集之上，由 Worker 主进程在启动时按配置注入。须在首次 SearchFiles 前调用。
func (s *Server) SetSearchIgnore(rules []string) {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	s.searchIgnore = rules
}

// ensureBotManager 确保 bot-worker 子进程已启动（首次创建 Bot 时懒启动）。
// 子进程生命周期跟随 Worker（用 background 上下文），由 botMgr.Stop() 收束。
func (s *Server) ensureBotManager() error {
	s.botStartMu.Lock()
	defer s.botStartMu.Unlock()
	if s.botMgr == nil {
		return fmt.Errorf("本节点未启用 Bot 能力")
	}
	if !s.botMgr.IsRunning() {
		if err := s.botMgr.Start(context.Background()); err != nil {
			return fmt.Errorf("启动 bot-worker 失败: %w", err)
		}
	}
	return nil
}

// CreateBot 创建并连接一个 Bot。
func (s *Server) CreateBot(ctx context.Context, req *workerpb.CreateBotRequest) (*workerpb.CreateBotResponse, error) {
	if err := s.ensureBotManager(); err != nil {
		// 失败必须留 Worker 侧日志：此前只装进响应，Worker 日志零痕迹，
		// 真机排障（bot 依赖未装/node 缺失）两侧都看不到原因。
		slog.Warn("CreateBot 拒绝：bot-worker 未就绪", "botUuid", req.BotUuid, "error", err)
		return &workerpb.CreateBotResponse{Success: false, Error: err.Error()}, nil
	}
	slog.Info("CreateBot", "botUuid", req.BotUuid, "host", req.Host, "port", req.Port, "username", req.Username, "version", req.Version)
	cfg := bot.BotConfig{
		ID:       req.BotUuid,
		Name:     req.Name,
		Host:     req.Host,
		Port:     int(req.Port),
		Username: req.Username,
		Version:  req.Version,
		Auth:     req.Auth,
		Behavior: req.Behavior,
	}
	if req.BehaviorConfig != "" {
		cfg.BehaviorConfig = json.RawMessage(req.BehaviorConfig)
	}
	if err := s.botMgr.CreateBots([]bot.BotConfig{cfg}); err != nil {
		return &workerpb.CreateBotResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.CreateBotResponse{Success: true, Status: "connecting"}, nil
}

// DeleteBot 停止并删除 Bot。
func (s *Server) DeleteBot(ctx context.Context, req *workerpb.DeleteBotRequest) (*workerpb.DeleteBotResponse, error) {
	if s.botMgr == nil {
		return &workerpb.DeleteBotResponse{Success: true}, nil
	}
	if err := s.botMgr.StopBots([]string{req.BotUuid}); err != nil {
		return &workerpb.DeleteBotResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.DeleteBotResponse{Success: true}, nil
}

// SetBotBehavior 切换 Bot 行为模式。
func (s *Server) SetBotBehavior(ctx context.Context, req *workerpb.SetBotBehaviorRequest) (*workerpb.SetBotBehaviorResponse, error) {
	if s.botMgr == nil {
		return &workerpb.SetBotBehaviorResponse{Success: false, Error: "本节点未启用 Bot 能力"}, nil
	}
	if err := s.botMgr.SetBehavior(req.BotUuid, req.Behavior, req.Target); err != nil {
		return &workerpb.SetBotBehaviorResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.SetBotBehaviorResponse{Success: true}, nil
}

// SendBotCommand 向 Bot 发送聊天/命令。
func (s *Server) SendBotCommand(ctx context.Context, req *workerpb.SendBotCommandRequest) (*workerpb.SendBotCommandResponse, error) {
	if s.botMgr == nil {
		return &workerpb.SendBotCommandResponse{Success: false, Error: "本节点未启用 Bot 能力"}, nil
	}
	if err := s.botMgr.SendBotCommand(req.BotUuid, req.Command); err != nil {
		return &workerpb.SendBotCommandResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.SendBotCommandResponse{Success: true}, nil
}

// ListBots 返回本节点 Bot 的实时状态快照（CP 据此回填 DB）。
func (s *Server) ListBots(ctx context.Context, req *workerpb.ListBotsRequest) (*workerpb.ListBotsResponse, error) {
	if s.botMgr == nil {
		return &workerpb.ListBotsResponse{}, nil
	}
	bots := s.botMgr.GetBots()
	out := make([]*workerpb.BotInfo, 0, len(bots))
	for _, b := range bots {
		info := &workerpb.BotInfo{
			BotUuid:  b.ID,
			Name:     b.Name,
			Status:   b.Status,
			Behavior: b.Behavior,
			Health:   float32(b.Health),
			Food:     int32(b.Food),
		}
		if b.Position != nil {
			info.PosX = float32(b.Position.X)
			info.PosY = float32(b.Position.Y)
			info.PosZ = float32(b.Position.Z)
		}
		out = append(out, info)
	}
	return &workerpb.ListBotsResponse{Bots: out}, nil
}

// StreamBotEvents 推送 Bot 实时状态与行为事件（FR-041）。
func (s *Server) StreamBotEvents(req *workerpb.StreamBotEventsRequest, stream workerpb.WorkerService_StreamBotEventsServer) error {
	if s.botMgr == nil {
		return fmt.Errorf("本节点未启用 Bot 能力")
	}

	filter := req.BotUuid
	ch := make(chan *bot.BotWorkerEvent, 128)
	s.botEventMu.Lock()
	s.botEventSubs = append(s.botEventSubs, ch)
	s.botEventMu.Unlock()
	defer func() {
		s.botEventMu.Lock()
		for i, sub := range s.botEventSubs {
			if sub == ch {
				s.botEventSubs = append(s.botEventSubs[:i], s.botEventSubs[i+1:]...)
				break
			}
		}
		s.botEventMu.Unlock()
		close(ch)
	}()

	slog.Info("StreamBotEvents 订阅开始", "botUuid", filter)
	if err := s.sendBotStateSnapshot(filter, stream); err != nil {
		return err
	}

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			for _, pbEvt := range botWorkerEventToProto(event, filter) {
				if err := stream.Send(pbEvt); err != nil {
					return err
				}
			}
		}
	}
}

func (s *Server) sendBotStateSnapshot(filter string, stream workerpb.WorkerService_StreamBotEventsServer) error {
	bots := s.botMgr.GetBots()
	for _, b := range bots {
		if filter != "" && b.ID != filter {
			continue
		}
		data, err := json.Marshal(botStateEventData(b))
		if err != nil {
			continue
		}
		if err := stream.Send(&workerpb.BotEvent{
			BotUuid:   b.ID,
			Type:      "state",
			Data:      string(data),
			Timestamp: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func botWorkerEventToProto(event *bot.BotWorkerEvent, filter string) []*workerpb.BotEvent {
	now := time.Now().UnixMilli()
	switch event.Evt {
	case "bot-state":
		events := make([]*workerpb.BotEvent, 0, len(event.Bots))
		for i := range event.Bots {
			bs := event.Bots[i]
			if filter != "" && bs.ID != filter {
				continue
			}
			data, err := json.Marshal(botStateEventData(&bs))
			if err != nil {
				continue
			}
			events = append(events, &workerpb.BotEvent{
				BotUuid:   bs.ID,
				Type:      "state",
				Data:      string(data),
				Timestamp: now,
			})
		}
		return events
	case "bot-event":
		if filter != "" && event.BotID != filter {
			return nil
		}
		data := string(event.Data)
		if data == "" {
			data = "{}"
		}
		return []*workerpb.BotEvent{{
			BotUuid:   event.BotID,
			Type:      event.Type,
			Data:      data,
			Timestamp: now,
		}}
	case "bot-error":
		if filter != "" && event.BotID != filter {
			return nil
		}
		data, _ := json.Marshal(map[string]string{"error": event.Error})
		return []*workerpb.BotEvent{{
			BotUuid:   event.BotID,
			Type:      "error",
			Data:      string(data),
			Timestamp: now,
		}}
	case "script-progress":
		if filter != "" && event.BotID != filter {
			return nil
		}
		data, _ := json.Marshal(map[string]any{
			"scriptId": event.ScriptID,
			"progress": event.Progress,
			"total":    event.Total,
			"status":   event.Status,
			"step":     event.Step,
		})
		return []*workerpb.BotEvent{{
			BotUuid:   event.BotID,
			Type:      "script-progress",
			Data:      string(data),
			Timestamp: now,
		}}
	default:
		return nil
	}
}

func botStateEventData(state *bot.BotState) map[string]any {
	data := map[string]any{
		"status":   state.Status,
		"name":     state.Name,
		"behavior": state.Behavior,
	}
	if state.Health != 0 {
		data["health"] = state.Health
	}
	if state.Food != 0 {
		data["food"] = state.Food
	}
	if state.Position != nil {
		data["position"] = state.Position
	}
	return data
}
