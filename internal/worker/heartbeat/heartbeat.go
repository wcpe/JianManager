package heartbeat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/internal/platform/directprobe"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/internal/worker/register"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// maxConcurrentProbeScrapes 单次心跳并发抓取实例指标的上限，避免实例多时一拍抓爆。
// 抓取本身有 directprobe.ProbeScrapeTimeoutCap(5s) 超时（见 metrics.ScrapeServerProbe）；
// 实例规模化下的进一步优化见 spec 开放问题。
const maxConcurrentProbeScrapes = 8

// 单拍实例指标采集的**总预算**（FR-446 审计项 2 / 复审 NEW-ISSUE A）**由生效超时推导**，
// 不再是硬编码定值。
//
// 旧实现把预算写死 15s，而直探超时可配（旧上界 30s）：运营把超时调大后，单实例最坏
// = 探针(5s) + SLP(t) + Query(t)（t 可至 30s → 65s）远超 15s，该实例的时序样本每拍都被预算
// 静默砍成「不可用」，监控缺失却无任何告警。现在预算 = directprobe.CollectBudgetFor(生效 slp,
// 生效 query) = 余量 + 探针上限 + slp + query，恒 ≥ 单实例同源串行最坏，且上界
// （directprobe.MaxCollectBudget()，26s）< 心跳节拍（30s，ADR-013）——「超时可配」与「心跳护栏」
// 在代码层自洽（推导与三处同源的超时上界见 internal/platform/directprobe）。
//
// 预算耗尽仍按既有语义处理：用**已完成部分**返回、未完成实例补「全部不可用」空样本（CP 落 NULL
// 断点，绝不把缺测伪装成 0），并记 WARN（不静默丢弃）。仍在跑的采集协程会各自在自身超时内收敛，
// 其写入落在带缓冲结果通道里、不会阻塞或泄漏。
func collectInstanceBudget() time.Duration {
	slp, query := metrics.DirectProbeTimeouts()
	budget := directprobe.CollectBudgetFor(slp, query)
	warnIfBudgetBreaksTickGuardrail(budget, slp, query)
	return budget
}

// budgetGuardrailWarnOnce 保证「预算与节拍失配」这类**配置性**问题只告警一次（每拍重发会淹没日志中心）。
var budgetGuardrailWarnOnce sync.Once

// warnIfBudgetBreaksTickGuardrail 在预算与心跳节拍护栏不再自洽时显式 WARN（绝不静默）。
//
// 构造上不会触发（directprobe 的常量关系已保证 MaxCollectBudget < HeartbeatInterval）；它是一道
// 防御性闸门：若将来有人放宽上界/缩小节拍而忘了同步预算，问题会在运行期立即暴露成一条明确日志，
// 而不是退化成「部分实例指标静默消失」。
func warnIfBudgetBreaksTickGuardrail(budget, slp, query time.Duration) {
	if directprobe.BudgetCoversTick(budget) && budget >= directprobe.WorstCaseSerial(true, true, true, slp, query) {
		return
	}
	budgetGuardrailWarnOnce.Do(func() {
		slog.Warn("单拍采集预算与心跳节拍护栏失配：请下调 direct_probe.* 超时或上调节拍，"+
			"否则部分实例的心跳时序指标会被预算截断",
			"budget", budget, "tick", directprobe.HeartbeatInterval,
			"reserve", directprobe.HeartbeatTickReserve, "slp", slp, "query", query)
	})
}

// 失败来源的短期退避（FR-446 审计项 2 的「失败实例短期退避/跳拍」）。
//
// query.port 已分配但 enable-query 未开、探针端口被占这类**持续性**失败，每拍都要白等一个
// 完整超时；69 实例规模下这正是把节拍拖垮的主因。故连续失败达阈值后按失败次数指数推迟重探：
// 60s → 120s（上限 probeSourceMaxRetryInterval），一旦恢复立即清零。
//
// 首档**必须严格大于心跳节拍**（30s，ADR-013），否则退避窗口会被下一拍起点追上而形同虚设
// （FR-446 复审 N4）；配合「退避基准取探测完成时刻」（见 collectInstanceMetricsAt），
// 首档 60s 实际至少跳过一拍。
// 退避**只作用于 30s 时序采样**：用户手动打开详情页走的 GetInstanceMetrics 实时链路不查询退避，
// 永远给最新值（否则一次抖动会让详情页「不可用」停留到退避结束）。
const (
	probeSourceRetryInterval    = 60 * time.Second
	probeSourceMaxRetryInterval = 2 * time.Minute
	// probeSourceFailThreshold 是开始退避前允许的连续失败次数：单次网络抖动仍每拍重探。
	probeSourceFailThreshold = 2
)

// 直探来源标识（退避状态与告警文案共用）。
const (
	sourceProbe = "probe"
	sourceSLP   = "slp"
	sourceQuery = "query"
)

// probeScrapeState 记录各实例上一次采集结果的**稳定判据**（不可用来源集合），用于「变化才告警」
// 的降噪：同一故障每拍重刷会淹没日志中心（未部署探针的实例会永久失败），而完全静默又会让
// 「探针桥已连接但 /metrics 不通」（FR-411：导入实例 probe_port=0、端口被占、探针降级）无从排障。
// 判据不含错误类别/退避态，避免文案抖动造成每拍重发（FR-446 复审 N5）。
// 实例删除/迁移时由 pruneProbeState 随本拍实例集合清理（FR-446 审计项 10）。
var probeScrapeState = struct {
	sync.Mutex
	lastSignature map[string]string
}{lastSignature: map[string]string{}}

// directProbeBackoff 记录各「实例×来源」的连续失败次数与下次允许重探时刻（见上文退避说明）。
// 键为 uuid + "|" + 来源标识。
var directProbeBackoff = struct {
	sync.Mutex
	entries map[string]*probeBackoffEntry
}{entries: map[string]*probeBackoffEntry{}}

// probeBackoffEntry 单个「实例×来源」的退避状态。
type probeBackoffEntry struct {
	fails     int
	nextRetry time.Time
}

// probeAttempt 描述一次实例采集**实际尝试**的来源（false = 本拍未尝试，可能是未配置或退避中）。
type probeAttempt struct {
	probe bool
	slp   bool
	query bool
}

// nodeSecretHeader gRPC metadata 中携带 node_secret 的 header 名。
// 心跳鉴权不放进 proto 字段，改用 gRPC metadata（HTTP/2 header），
// 避免改动 proto 与重新生成代码。
const nodeSecretHeader = "node-secret"

// InstanceStateProvider 提供所有实例的状态快照。
type InstanceStateProvider interface {
	GetAllInstanceStates() []process.InstanceSnapshot
}

// TaskSnapshotProvider 提供运行中长任务的心跳快照（FR-183，见 ADR-040）。
// 由 Worker gRPC 服务实现持有的内存任务表实现。终态任务上报后由本心跳调 Drop 移除。
type TaskSnapshotProvider interface {
	TaskSnapshots() []*workerpb.TaskSnapshot
	DropTask(taskID string)
	// CancelTask 强制停止运行中任务（FR-227）：真中断 Worker 操作（如下载）并置 canceled 终态。
	CancelTask(taskID string) bool
}

// ManagedRuntimeProvider 提供 Worker 与已运行 Bot Worker 的只读运行时快照。
// 采集端不得因调用本接口启动、重启或扫描任意非受管进程。
type ManagedRuntimeProvider interface {
	ManagedRuntimeSnapshot() *workerpb.ManagedRuntimeSnapshot
}

// Heartbeat 心跳上报器。
type Heartbeat struct {
	controlPlaneAddr string
	nodeUUID         string
	nodeSecret       string
	interval         time.Duration
	stopCh           chan struct{}
	instanceProvider InstanceStateProvider
	// taskProvider 运行中任务快照来源（FR-183）；为 nil 时心跳不带任务字段（向后兼容）。
	taskProvider TaskSnapshotProvider
	// managedRuntimeProvider 由 Worker 主进程注入；nil 时保持旧 Worker 的 Heartbeat 兼容形状。
	managedRuntimeProvider ManagedRuntimeProvider
	// proxyApplier 据心跳响应里 CP 下发的期望代理运行时重建 Worker 出站 client（FR-185，见 ADR-043）；
	// 为 nil 时心跳不应用下发代理（Worker 仅用本地 yaml/env，向后兼容旧 CP）。
	proxyApplier *proxyApplier
	// wsSecretApplier 据心跳响应里 CP 下发的 WS 令牌密钥热应用（FR-275，见 ADR-061）；
	// 为 nil 时不应用（向后兼容旧 CP：Worker 沿用启动时生效值）。
	wsSecretApplier *wsSecretApplier
}

// New 创建心跳上报器。
// nodeSecret 由注册阶段从 Control Plane 获得，用于心跳鉴权。
func New(controlPlaneAddr, nodeUUID, nodeSecret string, interval time.Duration, provider InstanceStateProvider) *Heartbeat {
	return &Heartbeat{
		controlPlaneAddr: controlPlaneAddr,
		nodeUUID:         nodeUUID,
		nodeSecret:       nodeSecret,
		interval:         interval,
		stopCh:           make(chan struct{}),
		instanceProvider: provider,
	}
}

// SetTaskProvider 注入运行中任务快照来源（FR-183，见 ADR-040）。
// 由 main 装配（传入 Worker gRPC 服务实现）；不调用则心跳不携带任务进度。
func (h *Heartbeat) SetTaskProvider(p TaskSnapshotProvider) {
	h.taskProvider = p
}

// SetManagedRuntimeProvider 注入受管运行时快照来源（FR-400）。
func (h *Heartbeat) SetManagedRuntimeProvider(p ManagedRuntimeProvider) {
	h.managedRuntimeProvider = p
}

// SetProxyRebuilder 注入「据心跳下发代理重建出站 client」的回调（FR-185，见 ADR-043）。
// 由 main 装配（包裹 httpclient.Provider.Rebuild）；不调用则忽略 CP 下发的代理（向后兼容）。
// 内部用 generation 比较，仅在期望代理变化时才重建（避免每拍重建）。
func (h *Heartbeat) SetProxyRebuilder(rebuild func(httpclient.Config) error) {
	h.proxyApplier = newProxyApplier(rebuild)
}

// SetWSSecretApplier 注入「据心跳下发 WS 令牌密钥热应用」的回调（FR-275，见 ADR-061）。
// current 为启动时已生效的密钥（首拍据此去重）；apply 由 main 装配（热更新终端/插件桥 +
// 持久化身份文件）。不调用则忽略 CP 下发的密钥（向后兼容）。
func (h *Heartbeat) SetWSSecretApplier(current string, apply func(secret string) error) {
	h.wsSecretApplier = newWSSecretApplier(current, apply)
}

// Start 启动心跳上报。
func (h *Heartbeat) Start() {
	go h.loop()
	slog.Info("心跳上报已启动", "interval", h.interval, "nodeUUID", h.nodeUUID)
}

// Stop 停止心跳上报。
func (h *Heartbeat) Stop() {
	close(h.stopCh)
	slog.Info("心跳上报已停止")
}

func (h *Heartbeat) loop() {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.sendHeartbeatWithRetry()
		}
	}
}

// sendHeartbeatWithRetry 发送一次心跳，失败时按指数退避重试，
// 直到成功或到达单轮最大重试时长（不阻塞 ticker 过久）。
// Control Plane 不可达时 Worker 不 panic，仅记录日志并等待下一周期。
//
// 采集（节点指标 + 每实例直探）**每拍只做一次**，重试只重发同一份负载
// （FR-446 审计项 2）：旧实现把采集放在 sendHeartbeat 内，一次失败的 RPC 会让 69 实例的
// 全量直探再跑一遍，把最坏成本乘以重试次数。
func (h *Heartbeat) sendHeartbeatWithRetry() {
	const maxBackoff = 30 * time.Second
	backoff := 2 * time.Second
	deadline := time.Now().Add(h.interval)

	req, terminalTaskIDs := h.buildHeartbeatRequest()

	for {
		if err := h.sendHeartbeat(req); err == nil {
			// 终态任务已随本次心跳上报且 CP 已确认接收，从内存表移除避免重复上报
			//（FR-183，见 ADR-040）。仅在心跳成功确认后才 Drop，确保 CP 至少收到一次终态快照。
			if h.taskProvider != nil {
				for _, id := range terminalTaskIDs {
					h.taskProvider.DropTask(id)
				}
			}
			return
		}

		if time.Now().After(deadline) {
			slog.Warn("本周期心跳重试已达上限，等待下一周期", "nodeUUID", h.nodeUUID)
			return
		}

		select {
		case <-h.stopCh:
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// buildHeartbeatRequest 组装本拍心跳负载（含节点指标、每实例直探快照、运行中任务），
// 返回负载与该拍可移除的终态任务 id。采集只在此处发生一次，由重试循环复用。
func (h *Heartbeat) buildHeartbeatRequest() (*workerpb.HeartbeatRequest, []string) {
	req := register.CollectHeartbeatData(h.nodeUUID)

	// 附加实例状态快照 + 每实例直探快照（FR-060 时序留存 + FR-446/447）。
	if h.instanceProvider != nil {
		states := h.instanceProvider.GetAllInstanceStates()
		// 显式空切片（非 nil）：新 Worker 无在管实例时仍启用反向对账「清单已空」语义（FR-326）；
		// 老路径 instanceProvider==nil 才保持 Instances=nil，CP 不启用反向对账。
		req.Instances = make([]*workerpb.InstanceState, 0, len(states))
		for _, s := range states {
			// pid 可选字段（FR-326）：供 CP 反向对账诊断；老 CP 忽略，零值兼容。
			req.Instances = append(req.Instances, &workerpb.InstanceState{
				InstanceUuid: s.UUID,
				State:        s.State,
				Pid:          int32(s.PID),
			})
		}
		req.InstanceMetrics = collectInstanceMetrics(states)
		req.ProcessMetrics = collectProcessMetrics(states)
	}
	if h.managedRuntimeProvider != nil {
		req.ManagedRuntime = h.managedRuntimeProvider.ManagedRuntimeSnapshot()
	}

	// 附加运行中长任务进度快照（FR-183，见 ADR-040）。
	var terminalTaskIDs []string
	if h.taskProvider != nil {
		req.Tasks = h.taskProvider.TaskSnapshots()
		for _, t := range req.Tasks {
			if t.State == "succeeded" || t.State == "failed" || t.State == "canceled" {
				terminalTaskIDs = append(terminalTaskIDs, t.TaskId)
			}
		}
	}
	return req, terminalTaskIDs
}

// sendHeartbeat 把已组装好的负载发往 Control Plane 并应用响应里的下发项。
func (h *Heartbeat) sendHeartbeat(req *workerpb.HeartbeatRequest) error {
	conn, err := grpc.NewClient(h.controlPlaneAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		slog.Warn("心跳连接 Control Plane 失败", "error", err)
		return err
	}
	defer conn.Close()

	client := workerpb.NewWorkerServiceClient(conn)

	// 通过 gRPC metadata 携带 node_secret 供 Control Plane 鉴权
	ctx := metadata.AppendToOutgoingContext(context.Background(), nodeSecretHeader, h.nodeSecret)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := client.Heartbeat(ctx)
	if err != nil {
		slog.Warn("心跳发送失败", "error", err)
		return err
	}

	if err := resp.Send(req); err != nil {
		slog.Warn("心跳发送失败", "error", err)
		return err
	}

	reply, err := resp.Recv()
	if err != nil {
		slog.Warn("心跳响应接收失败", "error", err)
		return err
	}

	// 应用 CP 下发的期望出站代理（FR-185，见 ADR-043）：generation 变化才重建出站 client。
	// 重连/重启天然由后续心跳重发，无需 Worker 落盘。
	h.proxyApplier.apply(reply)

	// 应用 CP 下发的 WS 令牌密钥（FR-275，见 ADR-061）：值变化才热更新终端/插件桥校验 + 持久化。
	h.wsSecretApplier.applyReply(reply)

	// 应用 CP 下发的 MC 直探超时（FR-446）：写入 metrics 生效值，心跳与实时两条链路共用。
	applyDirectProbeTimeouts(reply)

	// 应用 CP 下发的取消请求（FR-227）：真中断对应运行中任务（如下载），canceled 经下一拍心跳上报。
	if h.taskProvider != nil {
		for _, id := range reply.GetCancelTaskIds() {
			h.taskProvider.CancelTask(id)
		}
	}

	slog.Debug("心跳已发送", "timestamp", reply.Timestamp,
		"cpu", req.CpuUsage, "memory", req.MemoryUsage)
	return nil
}

// collectInstanceMetrics 对 RUNNING 且任一采集源端口已知的实例并发采集本机指标，
// 构造心跳负载里的每实例快照（FR-060 时序 + FR-446/447 直探）。
// 采集走统一编排链 `探针 → SLP → Query → 不可用`：探针失败时由 SLP/Query 直探补基础信息；
// 三源皆不可用则为「不可用」（CP 落 NULL 断点，不补假值），不阻塞其他实例采集。无可采实例时返回 nil。
func collectInstanceMetrics(snaps []process.InstanceSnapshot) []*workerpb.InstanceMetricSample {
	return collectInstanceMetricsAt(snaps, time.Now(), collectInstanceBudget())
}

// collectInstanceMetricsAt 是带**总预算**与可注入时钟的采集实现（FR-446 审计项 2）。
// 预算耗尽即返回已完成部分：未完成实例补一条「全部不可用」的空样本（与真实缺测同语义，
// CP 落 NULL 断点），绝不把缺测伪装成 0。仍在跑的采集协程会各自在自身超时内收敛，
// 其写入落在带缓冲的结果通道里、不会阻塞或泄漏。
func collectInstanceMetricsAt(snaps []process.InstanceSnapshot, now time.Time, budget time.Duration) []*workerpb.InstanceMetricSample {
	// 实例删除/迁移后清理其告警与退避状态（FR-446 审计项 10）：以本拍可见实例集合为准。
	pruneProbeState(snaps)

	targets := make([]process.InstanceSnapshot, 0, len(snaps))
	for _, s := range snaps {
		// 运行中且任一来源端口已知（探针 / server-port / query.port）才采集。
		if s.State == string(process.StateRunning) && (s.ProbePort > 0 || s.ServerPort > 0 || s.QueryPort > 0) {
			targets = append(targets, s)
		}
	}
	if len(targets) == 0 {
		return nil
	}

	type probeResult struct {
		idx    int
		sample *workerpb.InstanceMetricSample
	}
	// 带缓冲（容量=目标数）：预算耗尽后仍在跑的协程可无阻塞写入，不阻塞、不泄漏。
	results := make(chan probeResult, len(targets))
	sem := make(chan struct{}, maxConcurrentProbeScrapes)
	for i, t := range targets {
		go func(i int, t process.InstanceSnapshot) {
			sem <- struct{}{}
			defer func() { <-sem }()

			// 探针与实例同机，直探 localhost；编排链内部串行探针 → SLP → Query。
			// 端口按来源退避置 0 → 编排链跳过该来源（见 directProbeBackoff 说明）。
			cfg, att := probeConfigWithBackoff(t, now)
			sample := &workerpb.InstanceMetricSample{InstanceUuid: t.UUID}
			tel := metrics.CollectInstanceTelemetry(cfg)
			fillInstanceMetricSample(sample, tel)
			// 退避基准取**探测完成时刻**而非拍起点（FR-446 复审 N4）：以拍起点加冷却时长会与
			// 下一拍起点重合，使退避窗口被下一拍「追上」而形同虚设。
			recordProbeSourceOutcomes(t.UUID, att, tel, time.Now())
			// 降噪：任一来源可用则清空告警；本拍来源皆不可用才记，且按**稳定判据**变化才告警
			// （错误类别抖动/进出退避不重发，见 unreachableSourcesSignature）。
			recordInstanceSourceResult(t.UUID,
				unreachableSourcesSignature(t, att, tel), unreachableSourcesReason(t, att, tel))
			results <- probeResult{idx: i, sample: sample}
		}(i, t)
	}

	out := make([]*workerpb.InstanceMetricSample, len(targets))
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	for received := 0; received < len(targets); {
		select {
		case r := <-results:
			out[r.idx] = r.sample
			received++
		case <-deadline.C:
			slog.Warn("本拍实例指标采集超出总预算，使用已完成部分（未完成实例本拍按不可用）",
				"budget", budget, "targets", len(targets), "completed", received)
			for i := range out {
				if out[i] == nil {
					out[i] = &workerpb.InstanceMetricSample{InstanceUuid: targets[i].UUID}
				}
			}
			return out
		}
	}
	return out
}

// probeConfigWithBackoff 组装某实例本拍的采集配置：处于退避期的来源把端口置 0，
// 使编排链跳过它（避免持续性失败每拍都白等一个完整超时）。
// 同时把 CP 下发的直探超时**显式**填入配置（FR-446 审计项 1），不依赖包内隐式默认。
func probeConfigWithBackoff(t process.InstanceSnapshot, now time.Time) (metrics.CollectConfig, probeAttempt) {
	slpTimeout, queryTimeout := metrics.DirectProbeTimeouts()
	cfg := metrics.CollectConfig{
		ProbePort:    t.ProbePort,
		ServerPort:   t.ServerPort,
		QueryPort:    t.QueryPort,
		Host:         "localhost",
		SLPTimeout:   slpTimeout,
		QueryTimeout: queryTimeout,
	}
	att := probeAttempt{probe: cfg.ProbePort > 0, slp: cfg.ServerPort > 0, query: cfg.QueryPort > 0}
	if att.probe && shouldSkipProbeSource(t.UUID, sourceProbe, now) {
		cfg.ProbePort, att.probe = 0, false
	}
	if att.slp && shouldSkipProbeSource(t.UUID, sourceSLP, now) {
		cfg.ServerPort, att.slp = 0, false
	}
	if att.query && shouldSkipProbeSource(t.UUID, sourceQuery, now) {
		cfg.QueryPort, att.query = 0, false
	}
	return cfg, att
}

// fillInstanceMetricSample 把编排链结果填入心跳负载的每实例快照（FR-060/446/447）。
//
// 只填**心跳链路真正有消费者**的字段（CP IngestHeartbeat）：探针深度指标 + 在线人数及其可用位 +
// 各直探来源可用位。proto 里的 motd/version/max_players/player_names/plugins/map/source_mask/
// player_names_partial 为预留字段，心跳不填——它们已由实时详情链路（GetInstanceMetrics）端到端
// 提供，每拍重复携带只会放大载荷（FR-446 审计项 3）。
func fillInstanceMetricSample(sample *workerpb.InstanceMetricSample, tel *metrics.InstanceTelemetry) {
	sample.ProbeAvailable = tel.ProbeAvailable
	sample.Tps = tel.TPS
	sample.MsptMillis = tel.MSPTMillis
	sample.PlayersOnline = tel.PlayersOnline
	// 在线人数可用位（FR-447）：Query 有响应但缺 numplayers 时为 false，CP 据此落 NULL 断点，
	// 不把「协议未给出」写成 0 在线。
	sample.PlayersOnlineAvailable = tel.PlayersOnlineAvailable
	sample.HeapUsedBytes = tel.HeapUsedBytes
	sample.HeapMaxBytes = tel.HeapMaxBytes
	sample.Threads = tel.Threads
	sample.CpuLoad = tel.CPULoad
	sample.UptimeSeconds = tel.UptimeSeconds
	for name, w := range tel.Worlds {
		sample.Worlds = append(sample.Worlds, &workerpb.WorldMetric{
			Name:         name,
			LoadedChunks: w.LoadedChunks,
			Entities:     w.Entities,
			TileEntities: w.TileEntities,
		})
	}
	sample.SlpAvailable = tel.SLPAvailable
	sample.QueryAvailable = tel.QueryAvailable
}

// recordProbeSourceOutcomes 按本拍**实际尝试过**的来源记录退避状态（未尝试的不动，避免把「未配置」
// 当成「失败」）。任一来源恢复即清零其退避，下拍立刻恢复正常采样。
func recordProbeSourceOutcomes(instanceUUID string, att probeAttempt, tel *metrics.InstanceTelemetry, now time.Time) {
	if att.probe {
		recordProbeSourceResult(instanceUUID, sourceProbe, tel.ProbeAvailable, now)
	}
	if att.slp {
		recordProbeSourceResult(instanceUUID, sourceSLP, tel.SLPAvailable, now)
	}
	if att.query {
		recordProbeSourceResult(instanceUUID, sourceQuery, tel.QueryAvailable, now)
	}
}

// unreachableSourcesReason 生成「本拍尝试过的来源皆不可用」时的来源化描述（供变化告警）；任一来源
// 可用、或本拍什么都没尝试时返回空串。文案把**错误类别**并入（FR-446 审计项 4），并标注处于退避
// 而未尝试的来源（避免把「主动跳拍」误读成「探测成功」）。
func unreachableSourcesReason(t process.InstanceSnapshot, att probeAttempt, tel *metrics.InstanceTelemetry) string {
	if tel.ProbeAvailable || tel.SLPAvailable || tel.QueryAvailable {
		return ""
	}
	var parts []string
	if att.probe {
		parts = append(parts, sourceFailureText("探针", "probe_port", t.ProbePort, tel.ProbeErr))
	} else if t.ProbePort > 0 {
		parts = append(parts, fmt.Sprintf("探针(probe_port=%d): 退避中", t.ProbePort))
	}
	if att.slp {
		parts = append(parts, sourceFailureText("SLP", "server_port", t.ServerPort, tel.SLPErr))
	} else if t.ServerPort > 0 {
		parts = append(parts, fmt.Sprintf("SLP(server_port=%d): 退避中", t.ServerPort))
	}
	if att.query {
		parts = append(parts, sourceFailureText("Query", "query_port", t.QueryPort, tel.QueryErr))
	} else if t.QueryPort > 0 {
		parts = append(parts, fmt.Sprintf("Query(query_port=%d): 退避中", t.QueryPort))
	}
	if len(parts) == 0 {
		return ""
	}
	return "实例指标来源皆不可用: " + strings.Join(parts, ", ")
}

// sourceFailureText 拼一条来源失败描述：`SLP(server_port=25565): timeout`。
// 无错误对象（理论不达：尝试过才计入）时只留端口。
func sourceFailureText(label, portName string, port int, err error) string {
	text := fmt.Sprintf("%s(%s=%d)", label, portName, port)
	if cat := metrics.ProbeErrorCategory(err); cat != "" {
		text += ": " + cat
	}
	return text
}

// shouldSkipProbeSource 报告某实例的某来源本拍是否处于退避期（应跳过重探）。
func shouldSkipProbeSource(instanceUUID, source string, now time.Time) bool {
	key := instanceUUID + "|" + source
	directProbeBackoff.Lock()
	defer directProbeBackoff.Unlock()
	e, ok := directProbeBackoff.entries[key]
	if !ok {
		return false
	}
	return now.Before(e.nextRetry)
}

// recordProbeSourceResult 记录一次来源探测结果并推进/清零其退避：
// 成功 → 清零；失败达阈值后按失败次数指数推迟重探（60s/120s，上限 probeSourceMaxRetryInterval）。
// now 应为**探测完成时刻**（而非拍起点），使退避窗口从探测真正结束的时间起算。
func recordProbeSourceResult(instanceUUID, source string, available bool, now time.Time) {
	key := instanceUUID + "|" + source
	directProbeBackoff.Lock()
	defer directProbeBackoff.Unlock()
	if available {
		delete(directProbeBackoff.entries, key)
		return
	}
	e := directProbeBackoff.entries[key]
	if e == nil {
		e = &probeBackoffEntry{}
		directProbeBackoff.entries[key] = e
	}
	e.fails++
	if e.fails < probeSourceFailThreshold {
		return // 单次抖动：下拍照常重探
	}
	cooldown := probeSourceRetryInterval * time.Duration(1<<uint(min(e.fails-probeSourceFailThreshold, 2)))
	if cooldown > probeSourceMaxRetryInterval {
		cooldown = probeSourceMaxRetryInterval
	}
	e.nextRetry = now.Add(cooldown)
}

// pruneProbeState 清理不在本拍实例集合里的告警与退避状态（实例删除/迁移，FR-446 审计项 10）。
// 无实例（含全删）时清空全部，避免状态无界增长。
func pruneProbeState(snaps []process.InstanceSnapshot) {
	known := make(map[string]struct{}, len(snaps))
	for _, s := range snaps {
		known[s.UUID] = struct{}{}
	}
	probeScrapeState.Lock()
	for uuid := range probeScrapeState.lastSignature {
		if _, ok := known[uuid]; !ok {
			delete(probeScrapeState.lastSignature, uuid)
		}
	}
	probeScrapeState.Unlock()

	directProbeBackoff.Lock()
	for key := range directProbeBackoff.entries {
		uuid, _, _ := strings.Cut(key, "|")
		if _, ok := known[uuid]; !ok {
			delete(directProbeBackoff.entries, key)
		}
	}
	directProbeBackoff.Unlock()
}

// unreachableSourcesSignature 返回「本拍来源皆不可用」的**稳定判据**，仅含"哪些来源不可用"这一
// 集合（来源标签，按固定顺序），**不含错误类别与退避态**（FR-446 复审 N5）。变化告警据此去重：
// 错误类别抖动（timeout ↔ connection refused ↔ unreachable）或某来源进出退避（错误文案 ↔ "退避中"）
// 都不会让同一故障每拍重发 WARN——只有真正"哪些来源挂了"发生变化才告警。任一来源可用、或本拍
// 未配置任何来源 → 空串（无故障）。
func unreachableSourcesSignature(t process.InstanceSnapshot, att probeAttempt, tel *metrics.InstanceTelemetry) string {
	if tel.ProbeAvailable || tel.SLPAvailable || tel.QueryAvailable {
		return ""
	}
	var labels []string
	if att.probe || t.ProbePort > 0 {
		labels = append(labels, sourceProbe)
	}
	if att.slp || t.ServerPort > 0 {
		labels = append(labels, sourceSLP)
	}
	if att.query || t.QueryPort > 0 {
		labels = append(labels, sourceQuery)
	}
	return strings.Join(labels, ",")
}

// recordInstanceSourceResult 记录一次实例采集结果并按变化告警：新故障/故障集合变化 → WARN，
// 恢复 → INFO；同一故障持续存在则保持静默（避免每拍刷屏）。
//
// signature 是**稳定判据**（unreachableSourcesSignature），detail 是含错误类别/退避标注的告警正文
// （unreachableSourcesReason）。以 signature 而非 detail 去重，使文案抖动不再触发重复 WARN，
// 同时保留首次/变化时的完整可排障信息（FR-446 复审 N5）。
func recordInstanceSourceResult(instanceUUID, signature, detail string) {
	probeScrapeState.Lock()
	prev, had := probeScrapeState.lastSignature[instanceUUID]
	if signature == "" {
		delete(probeScrapeState.lastSignature, instanceUUID)
	} else {
		probeScrapeState.lastSignature[instanceUUID] = signature
	}
	probeScrapeState.Unlock()

	switch {
	case signature == "":
		if had {
			slog.Info("实例指标来源已恢复", "instanceId", instanceUUID)
		}
	case !had || prev != signature:
		slog.Warn("实例指标来源皆不可用（该实例的监控时序将缺测）：请检查服务端是否已监听 server-port（SLP）、"+
			"query.port 是否与 enable-query 一致、探针是否部署且 probe_port 与探针 config 一致",
			"instanceId", instanceUUID, "detail", detail)
	}
}
