package register

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"context"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/internal/worker/dirusage"
	"github.com/wcpe/JianManager/internal/worker/hostobs"
	"github.com/wcpe/JianManager/internal/worker/logmetrics"
	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// enrollTokenHeader 注册请求携带 enrollment token 的 gRPC metadata header 名（见 ADR-020）。
// 与 internal/controlplane/grpc 中的常量保持一致。token 经 metadata 传递、不改 proto。
const enrollTokenHeader = "enroll-token"

// nodeUUIDHeader / nodeSecretHeader 重注册请求携带本地身份的 gRPC metadata header 名（见 ADR-039）。
// 与 internal/controlplane/grpc、internal/worker/heartbeat 中的常量保持一致。
// 重注册时出示二者，CP 据此按 UUID（而非可重复的 name）匹配既有节点，杜绝重名覆写（BUG-A）。
const (
	nodeUUIDHeader   = "node-uuid"
	nodeSecretHeader = "node-secret"
)

// Config 注册配置。
type Config struct {
	ControlPlaneAddr string // Control Plane gRPC 地址
	NodeName         string // 节点名称
	WsPort           int    // WebSocket 端口
	GrpcPort         int    // 历史兼容字段；Worker 不再监听 gRPC 端口。
	Host             string // 本机 IP（留空自动检测，优先 127.0.0.1）
	// EnrollToken enrollment token 明文（见 ADR-020）。
	// 仅新节点首次注册（无本地身份文件）时携带；经 gRPC metadata 传给 CP 校验消费。
	// 已有本地身份的重注册留空；CP 仅按 UUID 与密钥放行，不消费 token。
	EnrollToken string
	// NodeUUID / NodeSecret 本地持久化的节点身份（见 ADR-039）。
	// 重注册时（已有本地身份文件）填入并经 gRPC metadata 出示，CP 按 UUID 匹配既有节点、
	// 校验 secret 后重注册，避免 name 锚定带来的重名覆写。首注册留空。
	NodeUUID   string
	NodeSecret string
}

// Result 注册结果。
type Result struct {
	NodeUUID   string
	NodeSecret string
	// WSTokenSecret CP 下发的 WS 令牌密钥（见 ADR-061）；旧 CP 响应为空。
	WSTokenSecret string
}

// Register 向 Control Plane 注册当前 Worker Node。
func Register(ctx context.Context, cfg Config) (*Result, error) {
	conn, err := grpc.NewClient(cfg.ControlPlaneAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("连接 Control Plane 失败: %w", err)
	}
	defer conn.Close()

	client := workerpb.NewWorkerServiceClient(conn)

	// 采集系统信息
	info := collectSystemInfo(cfg)

	// 首次注册携带 enrollment token（经 metadata，不改 proto）。
	// 重注册（已有本地身份）出示 node_uuid + node_secret，CP 按 UUID 匹配既有节点（ADR-039）。
	if cfg.EnrollToken != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, enrollTokenHeader, cfg.EnrollToken)
	}
	if cfg.NodeUUID != "" && cfg.NodeSecret != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, nodeUUIDHeader, cfg.NodeUUID, nodeSecretHeader, cfg.NodeSecret)
	}

	slog.Info("正在向 Control Plane 注册", "addr", cfg.ControlPlaneAddr, "name", cfg.NodeName,
		"withEnrollToken", cfg.EnrollToken != "", "withIdentity", cfg.NodeUUID != "")

	resp, err := client.Register(ctx, info)
	if err != nil {
		return nil, fmt.Errorf("注册失败: %w", err)
	}

	slog.Info("注册成功", "nodeUUID", resp.NodeUuid)
	return &Result{
		NodeUUID:      resp.NodeUuid,
		NodeSecret:    resp.NodeSecret,
		WSTokenSecret: resp.WsTokenSecret,
	}, nil
}

// RegisterWithRetry 带指数退避重试的注册。
// Control Plane 未启动或暂时不可达时，Worker 不退出，按指数退避重试直到成功或 ctx 取消。
// 退避区间 [initialDelay, maxDelay]，每次 ×2，上限 maxDelay。
func RegisterWithRetry(ctx context.Context, cfg Config, initialDelay, maxDelay time.Duration) (*Result, error) {
	delay := initialDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	if maxDelay <= 0 {
		maxDelay = 60 * time.Second
	}

	var lastErr error
	for {
		result, err := Register(ctx, cfg)
		if err == nil {
			return result, nil
		}
		lastErr = err
		slog.Warn("注册 Control Plane 失败，稍后重试",
			"addr", cfg.ControlPlaneAddr, "error", err, "retryIn", delay)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("注册中止: %w (最后错误: %v)", ctx.Err(), lastErr)
		case <-time.After(delay):
		}

		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

// collectSystemInfo 采集系统信息用于注册。
func collectSystemInfo(cfg Config) *workerpb.RegisterRequest {
	host := cfg.Host
	if host == "" {
		host = getOutboundIP()
	}

	info := &workerpb.RegisterRequest{
		Name:     cfg.NodeName,
		Host:     host,
		GrpcPort: int32(cfg.GrpcPort),
		WsPort:   int32(cfg.WsPort),
		Os:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CpuCores: int32(runtime.NumCPU()),
	}

	if vmem, err := mem.VirtualMemory(); err == nil {
		info.MemoryMb = int64(vmem.Total / 1024 / 1024)
	}

	if usage, err := disk.Usage("/"); err == nil {
		info.DiskTotalMb = int64(usage.Total / 1024 / 1024)
	}

	return info
}

// getOutboundIP 获取本机出口 IP（通过 UDP 探测，不实际发送数据）。
// 失败时回退到 127.0.0.1。
func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// WaitForControlPlane 等待 Control Plane 可用。
func WaitForControlPlane(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		conn, err := grpc.NewClient(addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err == nil {
			// 尝试调用
			client := workerpb.NewWorkerServiceClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, err = client.Register(ctx, &workerpb.RegisterRequest{Name: "probe"})
			cancel()
			conn.Close()

			if err == nil {
				return nil
			}
		}

		slog.Info("等待 Control Plane 就绪...", "addr", addr)
		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("等待 Control Plane 超时: %s", addr)
}

// 日志采集健康：由 main 在采集运行时创建成功/失败后写入，随每次心跳上报。
// 2026-09-28 事故教训：采集静默停摆 40+ 分钟无人知晓——该信号让「采集健康」能被告警规则命中。
var (
	logIngestMu      sync.RWMutex
	logIngestHealthy bool
	logIngestDetail  string
)

// SetLogIngestHealth 记录日志采集运行时健康状态（healthy=false 时应给出原因）。
func SetLogIngestHealth(healthy bool, detail string) {
	logIngestMu.Lock()
	logIngestHealthy, logIngestDetail = healthy, detail
	logIngestMu.Unlock()
}

func logIngestHealth() (bool, string) {
	logIngestMu.RLock()
	defer logIngestMu.RUnlock()
	return logIngestHealthy, logIngestDetail
}

// CollectHeartbeatData 采集心跳数据；CPU 仅读主进程注入的同窗快照。
// 不传 reader 时标量降级为零，不启动采样、不读取累计时间。
// CollectHeartbeatData 采集一次心跳负载（增补主机级观测）。
//
// hostObs 为可选：nil 时跳过主机级采集，其余字段照常——
// 老调用方（测试/未装配 hostobs 的场景）不需要改动。
func CollectHeartbeatData(nodeUUID string, readers ...metrics.CPUReader) *workerpb.HeartbeatRequest {
	return CollectHeartbeatDataWithHost(nodeUUID, nil, readers...)
}

// CollectHeartbeatDataWithHost 在 CollectHeartbeatData 基础上附带主机级观测。
//
// 独立成函数而不是给原函数加参数：原函数被多处测试直接调用，
// 加必填参数会波及全部调用点，而主机级观测本身是可选增强。
func CollectHeartbeatDataWithHost(nodeUUID string, hostObs hostobs.Reader, readers ...metrics.CPUReader) *workerpb.HeartbeatRequest {
	req := &workerpb.HeartbeatRequest{
		NodeUuid: nodeUUID,
	}
	req.LogIngestHealthy, req.LogIngestDetail = logIngestHealth()

	if vmem, err := mem.VirtualMemory(); err == nil {
		req.MemoryUsage = float32(vmem.UsedPercent / 100.0)
		req.MemoryUsedMb = int64(vmem.Used / 1024 / 1024)
	}

	// 与周期 collector、实时 RPC 共用唯一 CPU owner，消费者不推进基线。
	if len(readers) > 0 && readers[0] != nil {
		snapshot := readers[0].Snapshot()
		if snapshot.Valid {
			req.CpuUsage, req.Iowait = snapshot.CPUUsage, snapshot.IOWait
			// CPU 时间构成。只在 Valid 时填——无效窗口的成分是零值，
			// 上报上去会被 CP 当成「全是 idle」，比不给更容易误导。
			req.CpuUser = snapshot.Breakdown.User
			req.CpuNice = snapshot.Breakdown.Nice
			req.CpuSystem = snapshot.Breakdown.System
			req.CpuIdle = snapshot.Breakdown.Idle
			req.CpuIrq = snapshot.Breakdown.IRQ
			req.CpuSoftirq = snapshot.Breakdown.SoftIRQ
			req.CpuSteal = snapshot.Breakdown.Steal
		}
	}

	if usage, err := disk.Usage("/"); err == nil {
		req.DiskUsage = float32(usage.UsedPercent / 100.0)
		req.DiskUsedMb = int64(usage.Used / 1024 / 1024)
	}

	// 网络 IO（所有网卡汇总）
	if counters, err := psnet.IOCounters(false); err == nil && len(counters) > 0 {
		req.NetworkBytesSent = int64(counters[0].BytesSent)
		req.NetworkBytesRecv = int64(counters[0].BytesRecv)
	}

	// 系统负载 load average。gopsutil 跨平台：Windows 经处理器队列长度模拟，
	// 预热期或取不到时为 0（CP 侧据此优雅留空）。
	if avg, err := load.Avg(); err == nil && avg != nil {
		req.LoadAvg1 = avg.Load1
		// 5/15 分钟两档，用于区分「瞬时尖峰」与「持续爬升」。
		req.LoadAvg5, req.LoadAvg15 = avg.Load5, avg.Load15
	}

	// 主机级观测（进程计数 / 整机 top-N / 分挂载点）。
	//
	// 只读采集器已发布的快照，**不在这里触发采集**：进程全表扫描是重活，
	// 放在心跳 goroutine 里会拖慢心跳本身（而心跳是 CP 判断存活的依据）。
	if hostObs != nil {
		if hs := hostObs.Snapshot(); hs.Valid {
			req.HostTopProcesses = make([]*workerpb.HostProcessSample, 0, len(hs.TopProcesses))
			for _, p := range hs.TopProcesses {
				req.HostTopProcesses = append(req.HostTopProcesses, &workerpb.HostProcessSample{
					Pid: p.PID, Comm: p.Comm, CpuPct: p.CPUPct, RssBytes: p.RSSBytes,
				})
			}
			req.HostMounts = make([]*workerpb.HostMountUsage, 0, len(hs.Mounts))
			for _, m := range hs.Mounts {
				req.HostMounts = append(req.HostMounts, &workerpb.HostMountUsage{
					Mountpoint: m.Mountpoint, TotalBytes: m.TotalBytes,
					UsedBytes: m.UsedBytes, UsedPct: m.UsedPct,
				})
			}
		}
	}

	// 进程计数（主机级）：读 /proc/stat 与 /proc/loadavg 的廉价字段，不需要扫描进程表。
	// 取不到时留 0，CP 侧据「是否上报过」区分缺测与真实为 0（见 spec §5 验收 2）。
	if running, total, err := hostProcCounts(); err == nil {
		req.ProcsRunning, req.ProcsTotal = running, total
	}

	// 日志采集平台可观测性。读取周期采集器已发布的快照，**不在这里触发采集**——
	// 那要遍历全部源、stat 文件、读 SQLite 采样环，会拖慢心跳本身。
	// 快照不可用（采集器未装配 / 采集面没起来）时**整组不上报**，CP 侧落 null 断点：
	// 上报 0 会被读成「积压为零、投递零失败」，把真实故障伪装成健康。
	if logObs := logMetricsSnapshot(); logObs != nil {
		applyLogMetrics(req, *logObs)
	}

	// 实例工作目录占用。同样只读已发布快照，不在这里触发遍历。
	applyWorkDirUsages(req, workDirUsageSnapshot())

	return req
}

// applyLogMetrics 把日志平台读数填进心跳负载。
//
// 与主机级字段同一套缺失值约定：**只在测到时填**，并置 log_metrics_available。
func applyLogMetrics(req *workerpb.HeartbeatRequest, snap logmetrics.Snapshot) {
	if req == nil || !snap.Available {
		return
	}
	req.LogMetricsAvailable = true
	// 尺寸/增速：各自独立判可用。尺寸测到但增速无基线（首拍）时只发尺寸——
	// 增速发 0 会被读成「索引停止增长」，而真相是「还不知道」。
	if snap.StateFileMeasured {
		req.LogStateFileSizeBytes = snap.StateFileSizeBytes
	}
	if snap.StateFileGrowthMeasured {
		req.LogStateFileGrowthBytesPerSec = snap.StateFileGrowthBytesPerSec
	}
	req.LogPersistWriteBytesPerSec = snap.PersistWriteBytesPerSec
	req.LogWalBacklogEntries = snap.WALBacklogEntries
	req.LogWalPausedSeconds = snap.WALPausedSeconds
	req.LogDeliveryFailureRate = snap.DeliveryFailureRate
	req.LogDeliveryFailureClass = snap.DeliveryFailureClass
	req.LogVerifyBackoffCount = snap.VerifyBackoffCount
	req.LogSlowSqlCount = snap.SlowSQLCount
	req.LogSlowSqlMaxMs = snap.SlowSQLMaxMs
}

// 日志平台观测采集器：由 main 在构造后注入，随每次心跳读取快照。
// 与 logIngestHealth 同款的可变全局——心跳是单 goroutine，写入只发生在启动装配期。
var (
	logMetricsMu  sync.RWMutex
	logMetricsObs logmetrics.Reader
)

// SetLogMetricsObserver 注入日志平台观测采集器。
//
// 独立成函数而不是加进 CollectHeartbeatDataWithHost 的参数：该函数被大量测试直接调用，
// 加必填参数会波及所有调用点，而本项是可选增强（未注入时整组字段不上报）。
func SetLogMetricsObserver(observer logmetrics.Reader) {
	logMetricsMu.Lock()
	logMetricsObs = observer
	logMetricsMu.Unlock()
}

// logMetricsSnapshot 取已发布的日志平台读数；未注入或不可用时返回 nil。
func logMetricsSnapshot() *logmetrics.Snapshot {
	logMetricsMu.RLock()
	observer := logMetricsObs
	logMetricsMu.RUnlock()
	if observer == nil {
		return nil
	}
	snap := observer.Snapshot()
	if !snap.Available {
		// 不可用与「读数全 0」必须区分：前者不填任何字段（CP 落 null），
		// 后者才是一组真实的零值。
		return nil
	}
	return &snap
}

// 实例工作目录占用采集器：由 main 在构造后注入，随每次心跳读取快照。
//
// 与 logMetricsObserver 同款的可变全局（心跳是单 goroutine，写入只发生在启动装配期）。
var (
	dirUsageMu  sync.RWMutex
	dirUsageObs dirusage.Reader
)

// SetWorkDirUsageObserver 注入实例工作目录占用采集器。
//
// 独立成函数而不是给 CollectHeartbeatDataWithHost 加必填参数：该函数被大量测试直接调用，
// 加必填参数会波及所有调用点，而本项是可选增强（未注入时不上报该字段组）。
func SetWorkDirUsageObserver(observer dirusage.Reader) {
	dirUsageMu.Lock()
	dirUsageObs = observer
	dirUsageMu.Unlock()
}

// applyWorkDirUsages 把已发布的实例工作目录占用填进心跳负载。
//
// 只读快照、**不在这里触发统计**：目录遍历是 O(文件数) 的重活，放在心跳 goroutine 里会
// 把心跳本身拖慢（而心跳是 CP 判断节点与实例存活的依据）。
//
// 快照为空（采集器未装配 / 首轮尚未完成）时整组不上报。这条约定很关键：
// CP 侧「本拍没有某实例的数据」= 保持既有值不动，而不是写入 0——写 0 会让组配额
// 用量凭空归零，运行期强制随之失效（超卖）。
//
// 逐个实例带 available 位：统计失败的实例也必须上报（available=false），否则 CP 无从
// 区分「这实例统计失败，请回退备份估算」与「老 Worker 根本不上报这一项」。
func applyWorkDirUsages(req *workerpb.HeartbeatRequest, usages map[string]dirusage.Usage) {
	if req == nil || len(usages) == 0 {
		return
	}
	// 稳定顺序：map 迭代无序会让同一份数据每拍产生不同的字节序列，测试与抓包对比都难以阅读。
	uuids := make([]string, 0, len(usages))
	for uuid := range usages {
		if uuid == "" {
			continue
		}
		uuids = append(uuids, uuid)
	}
	if len(uuids) == 0 {
		return
	}
	sort.Strings(uuids)
	req.WorkDirUsages = make([]*workerpb.InstanceWorkDirUsage, 0, len(uuids))
	for _, uuid := range uuids {
		u := usages[uuid]
		item := &workerpb.InstanceWorkDirUsage{
			InstanceUuid: uuid,
			Available:    u.Available,
		}
		// 不可用时不带字节数：带了会让任何忽略 available 的消费方读到 0 并当成真实占用。
		if u.Available {
			item.WorkDirBytes = u.Bytes
		}
		req.WorkDirUsages = append(req.WorkDirUsages, item)
	}
}

// workDirUsageSnapshot 取已发布的工作目录占用快照；未注入或无数据时返回 nil。
func workDirUsageSnapshot() map[string]dirusage.Usage {
	dirUsageMu.RLock()
	observer := dirUsageObs
	dirUsageMu.RUnlock()
	if observer == nil {
		return nil
	}
	return observer.Usages()
}

// hostProcCounts 读取主机级进程计数。
//
// 数据来源刻意选**廉价字段**而不是遍历进程表：
//   - /proc/stat 的 procs_running / procs_blocked：内核维护的计数器，读一行即得；
//   - /proc/loadavg 末两列：运行中/总线程数（形如 `0.15 0.10 0.05 1/234 5678`）。
//
// 为什么不用 `process.Pids()` 取长度：那要遍历 /proc 下每个 PID 目录，
// 在进程数千的宿主上单次可达数十毫秒——而心跳每 30s 跑一次，不值得为精确到个位数的差别付这个代价。
// 代价是与 `ps -e` 的计数可能差几十（spec §6 待定 3 已记录该取舍）。
//
// 非 Linux 平台返回错误，CP 侧按缺测处理（显示 `--` 而非 0）。
func hostProcCounts() (int32, int32, error) {
	// /proc/loadavg 第 4 列形如 "1/234"：分子=运行中，分母=总线程数。
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 4 {
		return 0, 0, fmt.Errorf("loadavg 格式异常: %q", string(data))
	}
	parts := strings.SplitN(fields[3], "/", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("loadavg 第 4 列格式异常: %q", fields[3])
	}
	running, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("解析运行进程数失败: %w", err)
	}
	total, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("解析总进程数失败: %w", err)
	}
	// 上限保护：int32 在 21 亿，正常宿主机远达不到；显式夹一下避免溢出成负数。
	if running < 0 || total < 0 || running > math.MaxInt32 || total > math.MaxInt32 {
		return 0, 0, fmt.Errorf("进程计数超出合理范围: %d/%d", running, total)
	}
	return int32(running), int32(total), nil
}
