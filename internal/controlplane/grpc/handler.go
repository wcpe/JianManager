package grpc

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// nodeSecretHeader 心跳请求中携带 node_secret 的 gRPC metadata header 名。
// 与 internal/worker/heartbeat 中的常量保持一致。
const nodeSecretHeader = "node-secret"

// enrollTokenHeader 注册请求中携带 enrollment token 的 gRPC metadata header 名（FR-080，见 ADR-020）。
// 与 internal/worker/register 中的常量保持一致。token 经 metadata 传递、不改 proto。
const enrollTokenHeader = "enroll-token"

// nodeUUIDHeader 注册请求中携带本地持久化 node_uuid 的 gRPC metadata header 名（见 ADR-039）。
// 与 internal/worker/register 中的常量保持一致。升级后的 Worker 重注册时经 metadata 出示
// node_uuid + node_secret 证明身份，CP 据此按 UUID（而非可重复的 name）匹配既有节点，
// 杜绝「另一台机器用同名注册覆写旧节点身份」的 BUG-A。uuid 经 metadata 传递、不改 proto。
const nodeUUIDHeader = "node-uuid"

// MetricIngester 把心跳负载里的节点/实例指标落库为时序样本（FR-060）。
// 在 grpc 包内以接口声明、由 service.MetricService 实现，避免 grpc→service 反向依赖
// （service 已 import grpc）；接口只引用中立的 workerpb，无循环。
type MetricIngester interface {
	IngestHeartbeat(req *workerpb.HeartbeatRequest) error
}

// TaskIngester 把心跳负载里的运行中任务快照汇聚落库 + 终态副作用（FR-183，见 ADR-040）。
// 同 MetricIngester 以接口声明、由 service.TaskService 实现，避免 grpc→service 反向依赖。
type TaskIngester interface {
	IngestSnapshots(nodeUUID string, snaps []*workerpb.TaskSnapshot) error
	// PendingCancelTaskIDsByNodeUUID 返回该节点「已请求取消且未终态」的任务 id，供心跳下发 cancel_task_ids（FR-227）。
	PendingCancelTaskIDsByNodeUUID(nodeUUID string) []string
}

// EnrollmentValidator 校验并消费 enrollment token（FR-080，见 ADR-020）。
// 在 grpc 包内以接口声明、由 service.EnrollTokenService 实现，避免 grpc→service 反向依赖。
// ConsumeForNewNode 仅当 token 当前有效（未消费/未吊销/未过期）时原子消费、返回 nil；
// 否则返回非 nil（注册据此拒绝新节点）。
type EnrollmentValidator interface {
	ConsumeForNewNode(plaintext, nodeUUID string) (presetNodeName string, err error)
}

// NodeProxyResolver 计算某节点的期望出站代理 + generation，供心跳响应下发（FR-185，见 ADR-043）。
// 同 MetricIngester 以接口声明、由 service.NodeProxyService 实现，避免 grpc→service 反向依赖。
// 返回 url/noProxy 为期望代理（url 空=期望直连），generation 为其哈希（Worker 据此判定是否重建）。
type NodeProxyResolver interface {
	EffectiveNodeProxyByUUID(nodeUUID string) (url, noProxy, generation string)
}

// DirectProbeTimeoutResolver 提供 MC 直探（SLP / Query）当前生效超时，供心跳响应下发（FR-446）。
// 同 MetricIngester 以接口声明、由 service.SettingsService 实现，避免 grpc→service 反向依赖。
// 返回 <=0 表示未配置（Worker 回退内置默认）。
type DirectProbeTimeoutResolver interface {
	DirectProbeTimeouts() (slp, query time.Duration)
}

// HealthPolicySnapshot 是一次实例健康巡检策略（FR-459）的只读快照，供心跳响应下发 Worker。
// 值 <=0 的周期/阈值表示未配置，Worker 侧归一取默认。
type HealthPolicySnapshot struct {
	Enabled                 bool
	ScanInterval            time.Duration
	ProbeKind               string
	SuspicionThreshold      int
	Action                  string
	CircuitBreakerThreshold int
	CircuitBreakerWindow    time.Duration
	// StartupWarmup 启动宽限期（FR-459 复审项 2）；SelfHealMaxRestarts 假死自愈次数上限（复审项 7）。
	StartupWarmup       time.Duration
	SelfHealMaxRestarts int
}

// HealthPolicyResolver 提供实例健康巡检当前生效策略，供心跳响应下发（FR-459）。
// 同 MetricIngester 以接口声明、由 service.SettingsService 实现，避免 grpc→service 反向依赖。
type HealthPolicyResolver interface {
	HealthScanPolicy() HealthPolicySnapshot
}

// HealthAlertNotifier 在健康巡检上报熔断时投递站内信告警（FR-459 T4「发站内信告警」）。
// 由 service 侧实现（组装站内信 + 收件人查询），以接口声明避免 grpc→service 反向依赖。
// 未注入则只落审计与日志（仍保证「不静默」）。
type HealthAlertNotifier interface {
	NotifyInstanceCircuitBroken(nodeUUID, instanceUUID, reason string)
}

// ControlPlaneHandler Control Plane 侧的 gRPC 处理器。
// 处理来自 Worker Node 的 Register 和 Heartbeat 请求。
// OrphanRuntimeIngester 心跳反向对账入口（FR-326）；由 service.OrphanRuntimeTracker 实现。
// 声明在 grpc 包避免 controlplane/grpc → service 循环依赖。
type OrphanRuntimeIngester interface {
	ObserveHeartbeat(nodeUUID string, reported []*workerpb.InstanceState)
}

type ControlPlaneHandler struct {
	workerpb.WorkerServiceServer
	db          *gorm.DB
	pool        *ClientPool
	metrics     MetricIngester             // 时序指标入库（nil 时心跳不落时序）
	tasks       TaskIngester               // 任务进度入库（nil 时心跳不落任务，FR-183）
	enroll      EnrollmentValidator        // enrollment token 校验消费（nil 时退化为 FR-004 自助注册）
	proxy       NodeProxyResolver          // 节点期望代理解析（nil 时心跳响应不携带代理，FR-185）
	directProbe DirectProbeTimeoutResolver // MC 直探超时解析（nil 时心跳响应不携带直探超时，FR-446）
	// healthPolicy 实例健康巡检策略解析（nil 时心跳响应不携带巡检策略，FR-459）。
	healthPolicy HealthPolicyResolver
	// healthNotifier 健康熔断站内信告警投递（nil 时只落审计/日志，FR-459）。
	healthNotifier HealthAlertNotifier
	// healthAlerted 记录各实例最近一次已告警的熔断原因，避免每拍重复发信（FR-459，按 nodeUUID+uuid 去重）。
	healthAlertedMu sync.Mutex
	healthAlerted   map[string]string
	wsTokenSecret   string                // CP↔Worker WS 令牌密钥（空时注册/心跳响应不携带，FR-275）
	orphans         OrphanRuntimeIngester // 反向对账（nil 时不启用，FR-326）
	// evidence 进程侧证据拉取客户端（nil 时 syncInstanceStates 退化为旧行为，FR-455③）。
	evidence EvidenceProbeClient
	// orphanAudit 孤儿处置审计落库器（nil 时丢弃上报，FR-455/456）。
	orphanAudit OrphanAuditRecorder
	// resyncTrigger 注册成功后/心跳兜底触发的幂等重推入口（FR-455②）；nil 时不触发。
	resyncTrigger func(nodeUUID string)
	// reconcileGrace 记录「DB 运行态但清单缺失、进程侧证据未确认停机」的连续心跳拍数（FR-455③）。
	reconcileMu    sync.Mutex
	reconcileGrace map[string]int
	// reconcileDispatcher 派发一次证据对账任务（FR-456 F10）；nil 时同步内联执行（保持既有语义，
	// 测试默认）。生产装配为「按节点单飞 + 异步」的派发器，把最长 8s 的证据拉取移出心跳应答关键路径。
	reconcileDispatcher func(nodeUUID string, task func())
}

// NewControlPlaneHandler 创建处理器。
func NewControlPlaneHandler(db *gorm.DB, pool *ClientPool) *ControlPlaneHandler {
	return &ControlPlaneHandler{db: db, pool: pool, reconcileGrace: make(map[string]int), healthAlerted: make(map[string]string)}
}

// SetResyncTrigger 注入实例规格重推的幂等入口（FR-455②）。
// 注入后：Register 成功与心跳均触发该入口（由实现方按节点去重），使重推触发多源化、消除单点漏推。
func (h *ControlPlaneHandler) SetResyncTrigger(fn func(nodeUUID string)) {
	h.resyncTrigger = fn
}

// SetReconcileDispatcher 注入证据对账任务的派发器（FR-456 F10）。
// 注入后，心跳里的「清单缺失实例 + 进程侧证据」对账在派发器上执行（生产为异步单飞、移出心跳应答
// 关键路径，避免最长 8s 的证据拉取阻塞心跳）；不注入则同步内联执行（保持既有语义）。
func (h *ControlPlaneHandler) SetReconcileDispatcher(fn func(nodeUUID string, task func())) {
	h.reconcileDispatcher = fn
}

// triggerResync 触发幂等重推（未注入/空节点则忽略）。
func (h *ControlPlaneHandler) triggerResync(nodeUUID string) {
	if h.resyncTrigger != nil && nodeUUID != "" {
		h.resyncTrigger(nodeUUID)
	}
}

// SetMetricIngester 注入时序指标入库器（FR-060）；不注入则心跳仅更新节点当前值不落时序。
func (h *ControlPlaneHandler) SetMetricIngester(m MetricIngester) {
	h.metrics = m
}

// SetTaskIngester 注入任务进度入库器（FR-183，见 ADR-040）；不注入则心跳不处理任务快照。
func (h *ControlPlaneHandler) SetTaskIngester(t TaskIngester) {
	h.tasks = t
}

// SetEnrollmentValidator 注入 enrollment token 校验器（FR-080，见 ADR-020）。
// 注入后：新节点（name 未命中）注册必须携带有效 enrollment token；
// 不注入则退化为 FR-004 行为（任何 name 均可自助注册），保证开发环境与既有部署零配置可用。
func (h *ControlPlaneHandler) SetEnrollmentValidator(v EnrollmentValidator) {
	h.enroll = v
}

// SetNodeProxyResolver 注入节点期望代理解析器（FR-185，见 ADR-043）。
// 注入后每次心跳响应携带该节点期望代理（url/no_proxy/generation），Worker 据 generation
// 变化运行时重建出站 client；不注入则心跳响应不带代理（退化为 Worker 仅用本地 yaml/env，向后兼容）。
func (h *ControlPlaneHandler) SetNodeProxyResolver(r NodeProxyResolver) {
	h.proxy = r
}

// SetDirectProbeTimeoutResolver 注入 MC 直探超时解析器（FR-446）。
// 注入后每次心跳响应携带直探超时毫秒值，Worker 据此配置采集编排链（无需重启 Worker）；
// 不注入则心跳响应不带该字段（0），Worker 回退内置默认 3s（向后兼容）。
func (h *ControlPlaneHandler) SetDirectProbeTimeoutResolver(r DirectProbeTimeoutResolver) {
	h.directProbe = r
}

// SetHealthPolicyResolver 注入实例健康巡检策略解析器（FR-459）。
// 注入后每次心跳响应携带巡检策略（enabled/interval/probe_kind/阈值/动作），Worker 据此配置
// 巡检器（无需重启 Worker）；不注入则心跳响应不带该字段，Worker 回退本地 worker.yml。
func (h *ControlPlaneHandler) SetHealthPolicyResolver(r HealthPolicyResolver) {
	h.healthPolicy = r
}

// SetHealthAlertNotifier 注入健康熔断站内信告警投递器（FR-459）；不注入则只落审计/日志。
func (h *ControlPlaneHandler) SetHealthAlertNotifier(n HealthAlertNotifier) {
	h.healthNotifier = n
}

// SetOrphanRuntimeIngester 注入实例反向对账跟踪器（FR-326）。
// 注入后每拍心跳在正向对账之后观察 Worker 在管清单；不注入则反向对账关闭（向后兼容/测试默认）。
func (h *ControlPlaneHandler) SetOrphanRuntimeIngester(o OrphanRuntimeIngester) {
	h.orphans = o
}

// SetWSTokenSecret 注入 CP↔Worker WS 令牌密钥（FR-275，见 ADR-061）。
// 注入后注册响应（首注册/重注册）与每拍心跳响应均携带该密钥，Worker 持久化并热应用到
// 终端/插件桥校验；不注入（零值）则响应不携带，Worker 回退本地 jwt_secret（向后兼容）。
func (h *ControlPlaneHandler) SetWSTokenSecret(s string) {
	h.wsTokenSecret = s
}

// Register 处理 Worker Node 注册。
//
// 已有节点重注册必须同时出示 node-uuid 与 node-secret；Host 和名称不是身份凭据。
// 新节点首次注册不携带身份，但必须凭有效 enrollment token 准入。
func (h *ControlPlaneHandler) Register(ctx context.Context, req *workerpb.RegisterRequest) (*workerpb.RegisterResponse, error) {
	claimedUUID := nodeUUIDFromContext(ctx)
	claimedSecret := nodeSecretFromContext(ctx)
	if (claimedUUID == "") != (claimedSecret == "") {
		return nil, status.Error(codes.Unauthenticated, "节点身份必须同时携带 node_uuid 和 node_secret")
	}
	if claimedUUID != "" {
		var node model.Node
		err := h.db.Where("uuid = ?", claimedUUID).First(&node).Error
		if err == nil {
			// 命中既有节点：必须 secret 匹配方可重注册（防伪造 uuid 冒认身份）。
			if node.Secret != claimedSecret {
				slog.Warn("节点注册被拒：node_secret 与 UUID 不匹配", "name", req.Name, "uuid", claimedUUID)
				return nil, status.Errorf(codes.PermissionDenied, "节点身份校验失败（node_secret 不匹配）")
			}
			return h.reregisterExisting(&node, req, "uuid")
		}
		if err == gorm.ErrRecordNotFound {
			return nil, status.Errorf(codes.NotFound, "节点 %s 不存在", claimedUUID)
		}
		if err != nil {
			return nil, err
		}
	}

	// 不带身份的请求只能是首次注册；同名既有节点一律拒绝，禁止 Host 伪身份重注册。
	var existing model.Node
	err := h.db.Where("name = ?", req.Name).First(&existing).Error
	switch {
	case err == nil:
		slog.Warn("节点注册被拒：既有节点缺少 UUID 身份", "name", req.Name, "uuid", existing.UUID)
		return nil, status.Error(codes.Unauthenticated, "既有节点重注册必须携带 node_uuid 和 node_secret")
	case err == gorm.ErrRecordNotFound:
		// 名字未占用：作为全新节点首注册。
		return h.createNewNode(ctx, req)
	default:
		return nil, err
	}
}

// reregisterExisting 对已确认身份的既有节点重注册：更新节点展示与资源信息，返回既有 UUID/secret。
func (h *ControlPlaneHandler) reregisterExisting(node *model.Node, req *workerpb.RegisterRequest, matchBy string) (*workerpb.RegisterResponse, error) {
	updates := map[string]interface{}{
		"host":           req.Host,
		"grpc_port":      0,
		"ws_port":        req.WsPort,
		"status":         model.NodeStatusOnline,
		"os":             req.Os,
		"arch":           req.Arch,
		"cpu_cores":      req.CpuCores,
		"memory_mb":      req.MemoryMb,
		"disk_total_mb":  req.DiskTotalMb,
		"last_heartbeat": time.Now(),
	}
	// 允许改名（UUID 锚定身份，name 降为可变标签，受唯一约束）；但**空名上报不清空既有名**——
	// worker 仅设 JIANMANAGER_NAME（identity.NodeName 为空）时每次重启会上报空名，否则会把好名字抹成空。
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if err := h.db.Model(node).Updates(updates).Error; err != nil {
		slog.Error("更新节点失败", "name", req.Name, "error", err)
		return nil, err
	}
	slog.Info("节点已重新注册", "name", req.Name, "uuid", node.UUID, "matchBy", matchBy)

	// FR-455②：注册成功即触发一次幂等重推（去重器吸收与隧道 onOpen 的重复触发）。
	h.triggerResync(node.UUID)

	// WS 令牌密钥随重注册下发（FR-275）：存量节点升级/重启即拿到密钥，无需人工同步。
	return &workerpb.RegisterResponse{NodeUuid: node.UUID, NodeSecret: node.Secret, WsTokenSecret: h.wsTokenSecret}, nil
}

// createNewNode 创建全新节点：凭有效 enrollment token 准入（FR-080，见 ADR-020），
// 换发全新 UUID/secret。未注入校验器（开发/既有部署零配置）则退化为自助注册。
func (h *ControlPlaneHandler) createNewNode(ctx context.Context, req *workerpb.RegisterRequest) (*workerpb.RegisterResponse, error) {
	newUUID := uuid.New().String()
	name := req.Name
	if h.enroll != nil {
		enrollToken := enrollTokenFromContext(ctx)
		presetName, cerr := h.enroll.ConsumeForNewNode(enrollToken, newUUID)
		if cerr != nil {
			slog.Warn("新节点注册被拒：enrollment token 无效", "name", req.Name)
			return nil, status.Errorf(codes.PermissionDenied,
				"新节点注册需要有效的 enrollment token（请在面板「添加节点」重新生成）")
		}
		// worker 未上报名（仅设 JIANMANAGER_NAME、未经 setup --name/JIANMANAGER_NODE_NAME 上报）时，
		// 采用 token 上「添加节点」预设的名，避免空名节点导致一键搭建「选择节点」按名过滤取不到。
		if name == "" {
			name = presetName
		}
	}

	now := time.Now()
	node := model.Node{
		UUID:          newUUID,
		Name:          name,
		Host:          req.Host,
		GRPCPort:      0,
		WSPort:        int(req.WsPort),
		Secret:        uuid.New().String(),
		Status:        model.NodeStatusOnline,
		OS:            req.Os,
		Arch:          req.Arch,
		CPUCores:      int(req.CpuCores),
		MemoryMB:      req.MemoryMb,
		DiskTotalMB:   req.DiskTotalMb,
		LastHeartbeat: &now,
	}
	if err := h.db.Create(&node).Error; err != nil {
		slog.Error("创建节点失败", "name", req.Name, "error", err)
		return nil, err
	}
	slog.Info("新节点已注册", "name", req.Name, "uuid", node.UUID)

	// FR-455②：首注册成功即触发一次幂等重推（新节点此刻可能尚无实例，重推为空亦无副作用）。
	h.triggerResync(node.UUID)

	// WS 令牌密钥随首注册下发（FR-275）：一键安装的新节点开箱终端/插件桥可用。
	return &workerpb.RegisterResponse{NodeUuid: node.UUID, NodeSecret: node.Secret, WsTokenSecret: h.wsTokenSecret}, nil
}

// enrollTokenFromContext 从 gRPC metadata 取 enrollment token 明文（FR-080）；缺失返回空串。
func enrollTokenFromContext(ctx context.Context) string {
	return metadataValue(ctx, enrollTokenHeader)
}

// nodeUUIDFromContext 从 gRPC metadata 取 Worker 出示的 node_uuid（ADR-039）；缺失返回空串。
func nodeUUIDFromContext(ctx context.Context) string {
	return metadataValue(ctx, nodeUUIDHeader)
}

// nodeSecretFromContext 从 gRPC metadata 取 Worker 出示的 node_secret（ADR-039）；缺失返回空串。
func nodeSecretFromContext(ctx context.Context) string {
	return metadataValue(ctx, nodeSecretHeader)
}

// metadataValue 从入站 gRPC metadata 取首个指定 header 值；无 metadata 或无该 header 返回空串。
func metadataValue(ctx context.Context, header string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if vals := md.Get(header); len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// Heartbeat 处理 Worker Node 心跳（双向流）。
func (h *ControlPlaneHandler) Heartbeat(stream workerpb.WorkerService_HeartbeatServer) error {
	secret := nodeSecretFromContext(stream.Context())
	if secret == "" {
		return status.Error(codes.Unauthenticated, "心跳缺少 node_secret")
	}

	// 认证通过后 UUID 必须在整条流内保持不变，防止同一已认证流冒用其他节点。
	var lastNodeUUID string
	var authenticatedUUID string
	for {
		req, err := stream.Recv()
		if err != nil {
			// Worker 每拍心跳「发一拍→收响应→cancel 流」属正常收尾（每 worker 每 30s 一次），
			// 按 WARN 刷屏会淹没真异常（真机 2 小时 484 条）：Canceled/EOF 降 Debug，
			// 其余（网络中断/协议错）保留 WARN 并带节点标识（FR-322）。
			if err == io.EOF || status.Code(err) == codes.Canceled {
				slog.Debug("心跳流正常收尾", "nodeUUID", lastNodeUUID)
			} else {
				slog.Warn("心跳流异常断开", "nodeUUID", lastNodeUUID, "error", err)
			}
			return err
		}
		lastNodeUUID = req.NodeUuid

		if authenticatedUUID == "" {
			var node model.Node
			if err := h.db.Where("uuid = ?", req.NodeUuid).First(&node).Error; err != nil {
				slog.Warn("心跳鉴权失败：节点不存在", "nodeUUID", req.NodeUuid)
				return status.Errorf(codes.NotFound, "节点 %s 不存在", req.NodeUuid)
			}
			if node.Secret != secret {
				slog.Warn("心跳鉴权失败：secret 不匹配", "nodeUUID", req.NodeUuid)
				return status.Errorf(codes.PermissionDenied, "心跳鉴权失败")
			}
			authenticatedUUID = req.NodeUuid
		} else if req.NodeUuid != authenticatedUUID {
			slog.Warn("心跳鉴权失败：流内节点 UUID 改变", "expectedNodeUUID", authenticatedUUID, "nodeUUID", req.NodeUuid)
			return status.Error(codes.PermissionDenied, "心跳流节点身份不一致")
		}

		// 更新节点指标和心跳时间
		updates := map[string]interface{}{
			"cpu_usage":          req.CpuUsage,
			"memory_usage":       req.MemoryUsage,
			"disk_usage":         req.DiskUsage,
			"memory_used_mb":     req.MemoryUsedMb,
			"disk_used_mb":       req.DiskUsedMb,
			"network_bytes_sent": req.NetworkBytesSent,
			"network_bytes_recv": req.NetworkBytesRecv,
			"load_avg1":          req.LoadAvg1,
			"last_heartbeat":     time.Now(),
			"status":             model.NodeStatusOnline,
		}
		for key, value := range managedRuntimeUpdates(req.ManagedRuntime) {
			updates[key] = value
		}

		if err := h.db.Model(&model.Node{}).Where("uuid = ?", req.NodeUuid).Updates(updates).Error; err != nil {
			slog.Warn("更新心跳数据失败", "nodeUUID", req.NodeUuid, "error", err)
		}

		// 同步实例状态并对账（即使 Worker 上报空也要对账：
		// Worker 重启未恢复某实例时，DB 会永远卡在 RUNNING 致所有生命周期操作 422）。
		h.syncInstanceStates(req.NodeUuid, req.Instances)

		// FR-455②：心跳作为重推的自然兜底触发源（低频；由 ResyncDeduper 按节点节流吸收）。
		h.triggerResync(req.NodeUuid)

		// 反向对账（FR-326）：Worker 有、CP 无记录的无主运行时跟踪/宽限/自动处置。
		// 在正向对账之后执行；不改写正向语义。nil 注入=关闭。
		if h.orphans != nil {
			h.orphans.ObserveHeartbeat(req.NodeUuid, req.Instances)
		}

		// 心跳负载落库为时序样本（节点指标 + 每实例 ServerProbe 快照，FR-060）。
		// 失败不影响心跳本身（节点当前值已更新），仅记录告警。
		if h.metrics != nil {
			if err := h.metrics.IngestHeartbeat(req); err != nil {
				slog.Warn("时序指标入库失败", "nodeUUID", req.NodeUuid, "error", err)
			}
		}

		// 心跳负载里的运行中任务快照汇聚落库 + 终态副作用（落 NodeJDK / 发站内信，FR-183，见 ADR-040）。
		// 失败不影响心跳本身，仅记录告警。
		if h.tasks != nil && len(req.Tasks) > 0 {
			if err := h.tasks.IngestSnapshots(req.NodeUuid, req.Tasks); err != nil {
				slog.Warn("任务进度入库失败", "nodeUUID", req.NodeUuid, "error", err)
			}
		}

		// 返回响应；携带该节点期望出站代理供 Worker 运行时应用（FR-185，见 ADR-043）。
		// generation 变化时 Worker 才重建出站 client（避免每拍重建）；重连/重启天然由后续心跳重发。
		resp := &workerpb.HeartbeatResponse{Timestamp: time.Now().Unix()}
		// WS 令牌密钥每拍携带（FR-275，见 ADR-061）：Worker 比对变化才热应用 + 持久化，
		// 仅能到达本处的流已完成 node_secret 鉴权，密钥可以安全下发。
		resp.WsTokenSecret = h.wsTokenSecret
		if h.proxy != nil {
			resp.ProxyUrl, resp.ProxyNoProxy, resp.ProxyGeneration = h.proxy.EffectiveNodeProxyByUUID(req.NodeUuid)
		}
		// 携带 MC 直探（SLP / Query）超时（FR-446）：取自平台设置 direct_probe.*，Worker 存内存
		// 并填入采集编排链，使超时真可配且无需重启 Worker。每次心跳重发，幂等。
		if h.directProbe != nil {
			slp, query := h.directProbe.DirectProbeTimeouts()
			resp.DirectProbeSlpTimeoutMs = int32(slp / time.Millisecond)
			resp.DirectProbeQueryTimeoutMs = int32(query / time.Millisecond)
		}
		// 携带实例健康巡检策略（FR-459）：取自平台设置 health.*，Worker 存内存并写入巡检器生效值。
		// enabled 用 optional（非 nil）表达「本 CP 已下发策略」，每次心跳重发、幂等。
		if h.healthPolicy != nil {
			p := h.healthPolicy.HealthScanPolicy()
			enabled := p.Enabled
			resp.HealthScanEnabled = &enabled
			resp.HealthScanIntervalMs = int32(p.ScanInterval / time.Millisecond)
			resp.HealthProbeKind = p.ProbeKind
			resp.HealthSuspicionThreshold = int32(p.SuspicionThreshold)
			resp.HealthAction = p.Action
			resp.HealthCircuitBreakerThreshold = int32(p.CircuitBreakerThreshold)
			resp.HealthCircuitBreakerWindowMs = int32(p.CircuitBreakerWindow / time.Millisecond)
			resp.HealthStartupWarmupMs = int32(p.StartupWarmup / time.Millisecond)
			resp.HealthSelfHealMaxRestarts = int32(p.SelfHealMaxRestarts)
		}
		// 携带该节点「已请求取消」的任务 id，Worker 据此真中断对应运行中任务（FR-227）。
		if h.tasks != nil {
			resp.CancelTaskIds = h.tasks.PendingCancelTaskIDsByNodeUUID(req.NodeUuid)
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

func managedRuntimeUpdates(snapshot *workerpb.ManagedRuntimeSnapshot) map[string]interface{} {
	updates := map[string]interface{}{
		"managed_runtime_observed_at":     nil,
		"worker_process_rss_bytes":        nil,
		"worker_process_cpu_pct":          nil,
		"bot_worker_rss_bytes":            nil,
		"bot_worker_cpu_pct":              nil,
		"bot_active_count":                nil,
		"bot_connecting_count":            nil,
		"bot_event_loop_p95_ms":           nil,
		"bot_capacity_max":                nil,
		"bot_capacity_unavailable_reason": "Worker 未上报受管运行时快照",
		"bot_available":                   false,
		"bot_unavailable_reason":          "Worker 未上报受管运行时快照",
	}
	if snapshot == nil {
		return updates
	}
	if snapshot.ObservedAtUnixMs > 0 {
		observedAt := time.UnixMilli(snapshot.ObservedAtUnixMs).UTC()
		updates["managed_runtime_observed_at"] = &observedAt
	}
	updates["worker_process_rss_bytes"] = snapshot.WorkerProcessRssBytes
	updates["worker_process_cpu_pct"] = snapshot.WorkerProcessCpuPct
	updates["bot_capacity_unavailable_reason"] = truncateRuntimeReason(snapshot.BotCapacityUnavailableReason)
	updates["bot_available"] = snapshot.BotAvailable
	if snapshot.BotAvailable {
		updates["bot_worker_rss_bytes"] = snapshot.BotWorkerRssBytes
		updates["bot_worker_cpu_pct"] = snapshot.BotWorkerCpuPct
		updates["bot_active_count"] = snapshot.BotActiveCount
		updates["bot_connecting_count"] = snapshot.BotConnectingCount
		updates["bot_event_loop_p95_ms"] = snapshot.BotEventLoopP95Ms
		updates["bot_capacity_max"] = snapshot.BotCapacityMax
		updates["bot_unavailable_reason"] = ""
		if updates["bot_capacity_max"] == nil && updates["bot_capacity_unavailable_reason"] == "" {
			updates["bot_capacity_unavailable_reason"] = "Bot Worker 未报告有效容量"
		}
		return updates
	}
	updates["bot_unavailable_reason"] = truncateRuntimeReason(snapshot.BotUnavailableReason)
	if updates["bot_unavailable_reason"] == "" {
		updates["bot_unavailable_reason"] = "Bot Worker 不可用"
	}
	if updates["bot_capacity_unavailable_reason"] == "" {
		updates["bot_capacity_unavailable_reason"] = updates["bot_unavailable_reason"]
	}
	return updates
}

func truncateRuntimeReason(reason string) string {
	const maxBytes = 256
	if len(reason) <= maxBytes {
		return reason
	}
	return reason[:maxBytes]
}

// StartOfflineDetector 启动离线检测器。
// 超过 90s 未收到心跳的节点标记为离线。
func StartOfflineDetector(db *gorm.DB) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			threshold := time.Now().Add(-90 * time.Second)
			result := db.Model(&model.Node{}).
				Where("status = ? AND last_heartbeat < ?", model.NodeStatusOnline, threshold).
				Update("status", model.NodeStatusOffline)

			if result.RowsAffected > 0 {
				slog.Info("节点已标记为离线", "count", result.RowsAffected)
			}
		}
	}()

	slog.Info("离线检测器已启动", "threshold", "90s")
}

// syncInstanceStates 从心跳数据同步实例状态到数据库。
func (h *ControlPlaneHandler) syncInstanceStates(nodeUUID string, states []*workerpb.InstanceState) {
	emptyReason := ""
	reported := make([]string, 0, len(states))
	for _, s := range states {
		reported = append(reported, s.InstanceUuid)
		status := model.InstanceStatus(s.State)
		// status_reason 的待写值（nil=本拍无可写原因，不动该列）。
		// FR-459 终验 Major：status 与 status_reason 的写入条件不同，必须拆成两条 UPDATE——
		// status 恒写（仅带 DAMAGED 守卫），status_reason 在泛化原因场景下额外带「库中原因为空」
		// 的 WHERE 条件。若二者挤在同一条 UPDATE，该条件会连带冻结 status：实例 RUNNING 被判假死
		// 写入非空 status_reason 后真崩溃，本拍命中 CRASHED 分支置 reasonEmptyCond=true，
		// 整条 UPDATE 因 status_reason 非空而不匹配 → status 永远停在 RUNNING（desync 卡死）。
		var reasonValue *string
		// reasonEmptyCond：仅当库中 status_reason 为空/未记录时才写巡检泛化原因（见下方分支）。
		reasonEmptyCond := false
		// FR-459：健康巡检原因 → instances.status_reason。
		//   - 熔断原因（health=="circuit_broken"）是需要在面板可见的具体升级原因 → 恒写入；
		//   - CRASHED 的巡检原因是泛化文案（如「实例已崩溃…」），**不得覆盖 CP 已写入的具体原因**
		//     （如「未绑定 JDK」「Worker 操作失败: …」，FR-312）：仅在库中原因为空时补写；
		//   - 其余情况（假死/嫌疑）按上报写入，使假死在面板与健康墙可见；
		//   - 巡检明确健康（health=="healthy"）且无原因 → 清空历史原因（假死已恢复）。
		// 老 Worker 不上报 health/status_reason（均空），此分支不触发，保持既有语义。
		switch {
		case s.StatusReason == "" && status == model.InstanceStatusRunning && s.Health == healthFaultHealthy:
			reasonValue = &emptyReason
		case s.StatusReason == "":
			// 无原因可写（含老 Worker / 健康实例）：不动 status_reason。
		case s.Health == healthFaultCircuitBroken:
			reasonValue = &s.StatusReason
		case status == model.InstanceStatusCrashed:
			// 具体原因优先：仅当库中原因为空/未记录时写入泛化巡检原因。
			reasonValue = &s.StatusReason
			reasonEmptyCond = true
		default:
			reasonValue = &s.StatusReason
		}
		// DAMAGED（FR-342 搭建失败损毁）是 CP 侧生命周期态，Worker 不感知：损毁实例在 Worker
		// 记账里本就是「没在跑」（STOPPED/CRASHED），与损毁不矛盾。此前无条件覆写会在搭建失败后的
		// 下一个心跳把 DAMAGED 降级成 STOPPED，启动守卫随即失效（真机回归）。仅当 Worker 上报
		// 运行类状态（进程确实活着）才允许覆盖 DAMAGED。
		damagedGuard := status == model.InstanceStatusStopped || status == model.InstanceStatusCrashed

		// ① status 恒写（不含 reasonEmptyCond，避免原因条件连带冻结状态）。
		statusQ := h.db.Model(&model.Instance{}).Where("uuid = ?", s.InstanceUuid)
		if damagedGuard {
			statusQ = statusQ.Where("status <> ?", model.InstanceStatusDamaged)
		}
		if err := statusQ.Update("status", status).Error; err != nil {
			slog.Warn("同步实例状态失败", "instanceUUID", s.InstanceUuid, "state", s.State, "error", err)
		}

		// ② status_reason 单独写：泛化原因场景附加「库中原因为空」条件；DAMAGED 守卫与 status 一致，
		//    以免把巡检原因写到不打算改动状态的损毁实例上。
		if reasonValue != nil {
			reasonQ := h.db.Model(&model.Instance{}).Where("uuid = ?", s.InstanceUuid)
			if damagedGuard {
				reasonQ = reasonQ.Where("status <> ?", model.InstanceStatusDamaged)
			}
			if reasonEmptyCond {
				reasonQ = reasonQ.Where("status_reason IS NULL OR status_reason = ''")
			}
			if err := reasonQ.Update("status_reason", *reasonValue).Error; err != nil {
				slog.Warn("同步实例状态原因失败", "instanceUUID", s.InstanceUuid, "state", s.State, "error", err)
			}
		}
		// FR-459：熔断上报 → 站内信告警（按实例去重，原因变化才重发）。
		h.maybeNotifyCircuitBroken(nodeUUID, s)
	}

	// 对账：本节点上 DB 认为在运行（RUNNING/STARTING/STOPPING）但 Worker 未上报的实例。
	// 旧行为一律置 STOPPED——但心跳清单只是「Worker 内存表」的快照，任何让内存表暂缺已运行实例的
	// 情形（FR-436：CP 重启后 63 实例集体失联）都会误判成「面板 STOPPED 而进程在跑」。
	// FR-455③：先向该 Worker 拉进程侧证据，证据也认为不在跑才落 STOPPED；证据显示在跑则保持当前态。
	// FR-455③ / F7：本拍清单里出现的实例，其「连续缺失」宽限计数必须复位——否则实例重新上报后
	// 计数不归零，下一拍再缺失就会累计到阈值而误落 STOPPED（宽限语义要求「连续 N 拍缺失」）。
	for _, uuid := range reported {
		h.clearReconcileGrace(uuid)
	}

	var node model.Node
	if err := h.db.Where("uuid = ?", nodeUUID).First(&node).Error; err != nil {
		return
	}
	// FR-456 F10：证据对账（进程侧拉取，最长 evidenceProbeTimeout=8s）交由派发器执行，生产装配为
	// 「按节点单飞 + 异步」，把耗时移出心跳应答关键路径（与 spec §2.4「重推不应阻塞心跳」一致）；
	// 未注入派发器则同步内联（保持既有语义，测试默认）。
	if h.reconcileDispatcher != nil {
		h.reconcileDispatcher(node.UUID, func() { h.reconcileMissingInstances(node, reported) })
		return
	}
	h.reconcileMissingInstances(node, reported)
}

// runningStatuses DB 侧「运行类」状态集合。
var runningStatuses = []string{"RUNNING", "STARTING", "STOPPING"}

// FR-459 健康巡检结论标识（与 internal/worker/process 的 HealthFault* 取值对齐）。
const (
	healthFaultHealthy       = "healthy"
	healthFaultCircuitBroken = "circuit_broken"
)

// maybeNotifyCircuitBroken 在心跳上报健康熔断时为实例投递站内信告警（FR-459 T4）。
// 按 nodeUUID+uuid 去重：原因未变化不重复发信；实例恢复（health 不再为熔断）时清除记录。
func (h *ControlPlaneHandler) maybeNotifyCircuitBroken(nodeUUID string, s *workerpb.InstanceState) {
	if s == nil || s.InstanceUuid == "" {
		return
	}
	key := nodeUUID + "|" + s.InstanceUuid
	if s.Health != healthFaultCircuitBroken {
		h.healthAlertedMu.Lock()
		delete(h.healthAlerted, key)
		h.healthAlertedMu.Unlock()
		return
	}
	reason := s.StatusReason
	h.healthAlertedMu.Lock()
	prev, seen := h.healthAlerted[key]
	if seen && prev == reason {
		h.healthAlertedMu.Unlock()
		return
	}
	h.healthAlerted[key] = reason
	notifier := h.healthNotifier
	h.healthAlertedMu.Unlock()
	if notifier != nil {
		notifier.NotifyInstanceCircuitBroken(nodeUUID, s.InstanceUuid, reason)
	}
}

// reconcileMissingInstances 处理「DB 运行态但心跳清单缺失」的实例（FR-455③）。
// 未注入证据客户端时退化为旧行为（直接落 STOPPED）。
func (h *ControlPlaneHandler) reconcileMissingInstances(node model.Node, reported []string) {
	q := h.db.Model(&model.Instance{}).Where("node_id = ? AND status IN ?", node.ID, runningStatuses)
	if len(reported) > 0 {
		q = q.Where("uuid NOT IN ?", reported)
	}
	if h.evidence == nil {
		if err := q.Update("status", "STOPPED").Error; err != nil {
			slog.Warn("对账离线实例状态失败", "nodeUUID", node.UUID, "error", err)
		}
		return
	}

	var missing []struct {
		UUID   string
		Status string
	}
	if err := q.Select("uuid", "status").Scan(&missing).Error; err != nil {
		slog.Warn("对账查询缺失实例失败", "nodeUUID", node.UUID, "error", err)
		return
	}
	if len(missing) == 0 {
		return
	}

	uuids := make([]string, 0, len(missing))
	for _, m := range missing {
		uuids = append(uuids, m.UUID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), evidenceProbeTimeout)
	defer cancel()
	evidence, evErr := h.evidence.ProbeInstanceEvidence(ctx, node.UUID, uuids)
	if evErr != nil {
		slog.Warn("实例对账证据拉取失败，本轮按不可得宽限处理", "nodeUUID", node.UUID, "error", evErr)
	}

	for _, m := range missing {
		running, hasEvidence := evidence[m.UUID]
		switch {
		case evErr == nil && hasEvidence && running:
			// 证据确认仍在跑：保持当前态 + 标 statusReason（消除通道抖动误判），清宽限计数。
			h.clearReconcileGrace(m.UUID)
			if err := h.db.Model(&model.Instance{}).Where("uuid = ?", m.UUID).
				Update("status_reason", "进程侧证据显示实例仍在运行（心跳清单暂缺，已延迟对账）").Error; err != nil {
				slog.Warn("更新对账 statusReason 失败", "instanceUUID", m.UUID, "error", err)
			}
		case evErr == nil && hasEvidence && !running:
			// 证据确认已不在跑：落 STOPPED（PID 无、socket 不可达）。
			h.clearReconcileGrace(m.UUID)
			h.setStopped(m.UUID)
		default:
			// 证据缺失/拉取失败：宽限计数，连续 N 拍才落 STOPPED，避免抖动误判。
			beats := h.bumpReconcileGrace(m.UUID)
			if beats >= evidenceReconcileGraceBeats {
				h.clearReconcileGrace(m.UUID)
				h.setStopped(m.UUID)
			} else if err := h.db.Model(&model.Instance{}).Where("uuid = ?", m.UUID).
				Update("status_reason", fmt.Sprintf("进程侧证据暂不可得，宽限对账中（第 %d/%d 拍）", beats, evidenceReconcileGraceBeats)).Error; err != nil {
				slog.Warn("更新对账 statusReason 失败", "instanceUUID", m.UUID, "error", err)
			}
		}
	}
}

// setStopped 把实例置 STOPPED 并清空 statusReason。
func (h *ControlPlaneHandler) setStopped(uuid string) {
	if err := h.db.Model(&model.Instance{}).Where("uuid = ?", uuid).
		Updates(map[string]interface{}{"status": "STOPPED", "status_reason": ""}).Error; err != nil {
		slog.Warn("对账落 STOPPED 失败", "instanceUUID", uuid, "error", err)
	}
}
