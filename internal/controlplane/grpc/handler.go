package grpc

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/context"
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
}

// EnrollmentValidator 校验并消费 enrollment token（FR-080，见 ADR-020）。
// 在 grpc 包内以接口声明、由 service.EnrollTokenService 实现，避免 grpc→service 反向依赖。
// ConsumeForNewNode 仅当 token 当前有效（未消费/未吊销/未过期）时原子消费、返回 nil；
// 否则返回非 nil（注册据此拒绝新节点）。
type EnrollmentValidator interface {
	ConsumeForNewNode(plaintext, nodeUUID string) error
}

// NodeProxyResolver 计算某节点的期望出站代理 + generation，供心跳响应下发（FR-185，见 ADR-043）。
// 同 MetricIngester 以接口声明、由 service.NodeProxyService 实现，避免 grpc→service 反向依赖。
// 返回 url/noProxy 为期望代理（url 空=期望直连），generation 为其哈希（Worker 据此判定是否重建）。
type NodeProxyResolver interface {
	EffectiveNodeProxyByUUID(nodeUUID string) (url, noProxy, generation string)
}

// ControlPlaneHandler Control Plane 侧的 gRPC 处理器。
// 处理来自 Worker Node 的 Register 和 Heartbeat 请求。
type ControlPlaneHandler struct {
	workerpb.WorkerServiceServer
	db              *gorm.DB
	pool            *ClientPool
	onWorkerConnect func(nodeUUID string)  // Worker 注册成功后回调
	metrics         MetricIngester         // 时序指标入库（nil 时心跳不落时序）
	tasks           TaskIngester           // 任务进度入库（nil 时心跳不落任务，FR-183）
	enroll          EnrollmentValidator    // enrollment token 校验消费（nil 时退化为 FR-004 自助注册）
	proxy           NodeProxyResolver      // 节点期望代理解析（nil 时心跳响应不携带代理，FR-185）
}

// NewControlPlaneHandler 创建处理器。
func NewControlPlaneHandler(db *gorm.DB, pool *ClientPool) *ControlPlaneHandler {
	return &ControlPlaneHandler{db: db, pool: pool}
}

// SetOnWorkerConnect 设置 Worker 注册成功后的回调。
func (h *ControlPlaneHandler) SetOnWorkerConnect(fn func(nodeUUID string)) {
	h.onWorkerConnect = fn
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

// Register 处理 Worker Node 注册。
//
// 身份匹配优先级（见 ADR-039，修复 BUG-A「节点重名覆盖」）：
//  1. metadata 携带 node-uuid 且命中库中节点 → 校验 node-secret：匹配则按 UUID 重注册
//     （更新 host/port/os/arch，允许改名）；secret 不符回 PermissionDenied，绝不覆写。
//  2. 过渡兼容：未升级旧 Worker 只带 name，name 命中既有节点且本次连接 host 与库存 host
//     一致（同机重启信号）→ 放行重注册并告警建议升级；host 不一致落到 3。
//  3. 否则视为新节点首注册：必须凭有效 enrollment token 准入（FR-080，见 ADR-020）；
//     若上报名与既有节点撞名 → 回 AlreadyExists 拒绝（提示改名），绝不覆写既有身份。
//
// 之所以三级而非纯 UUID：纯 UUID 会破坏 ADR-020 之前无身份文件的 legacy 节点重启；
// 同机 host 兼容兜住未升级节点的真实重启，待全网 Worker 带 uuid/secret 后可移除（届时纯 UUID 锚定）。
func (h *ControlPlaneHandler) Register(ctx context.Context, req *workerpb.RegisterRequest) (*workerpb.RegisterResponse, error) {
	// 1. UUID 证明：升级后 Worker 经 metadata 出示 node-uuid + node-secret。
	if claimedUUID := nodeUUIDFromContext(ctx); claimedUUID != "" {
		var node model.Node
		err := h.db.Where("uuid = ?", claimedUUID).First(&node).Error
		if err == nil {
			// 命中既有节点：必须 secret 匹配方可重注册（防伪造 uuid 冒认身份）。
			if node.Secret != nodeSecretFromContext(ctx) {
				slog.Warn("节点注册被拒：node_secret 与 UUID 不匹配", "name", req.Name, "uuid", claimedUUID)
				return nil, status.Errorf(codes.PermissionDenied, "节点身份校验失败（node_secret 不匹配）")
			}
			return h.reregisterExisting(&node, req, "uuid")
		}
		if err != gorm.ErrRecordNotFound {
			return nil, err
		}
		// claimedUUID 不在库（如残留身份指向已删节点）：不命中，落到下方 token 新建路径。
	}

	// 2. 过渡兼容 / 3. 新建：按 name 查既有节点。
	var existing model.Node
	err := h.db.Where("name = ?", req.Name).First(&existing).Error
	switch {
	case err == nil:
		// name 命中。仅当本次连接 host 与库存 host 一致（同机重启）才放行重注册；
		// host 不一致正是「另一台机器冒用同名」——拒绝，绝不覆写（BUG-A 根因）。
		if existing.Host == req.Host {
			slog.Warn("节点未出示 UUID 身份、按同机 host 命中重注册，建议升级 Worker 以启用 UUID 身份",
				"name", req.Name, "host", req.Host, "uuid", existing.UUID)
			return h.reregisterExisting(&existing, req, "same-host-legacy")
		}
		slog.Warn("节点注册被拒：节点名已被占用且非同机重启（疑似异机冒用同名）",
			"name", req.Name, "reqHost", req.Host, "existingHost", existing.Host)
		return nil, status.Errorf(codes.AlreadyExists,
			"节点名 %q 已被占用，请改用其它名称（或在已占用机器上升级 Worker 以启用 UUID 身份重注册）", req.Name)
	case err == gorm.ErrRecordNotFound:
		// 名字未占用：作为全新节点首注册。
		return h.createNewNode(ctx, req)
	default:
		return nil, err
	}
}

// reregisterExisting 对已确认身份的既有节点重注册：更新 host/port/os/arch/资源与心跳时间，
// 重建反向 gRPC 连接，返回既有 UUID/secret（不重签）。matchBy 仅用于日志区分匹配路径。
func (h *ControlPlaneHandler) reregisterExisting(node *model.Node, req *workerpb.RegisterRequest, matchBy string) (*workerpb.RegisterResponse, error) {
	updates := map[string]interface{}{
		"name":           req.Name, // 允许改名（UUID 锚定身份，name 降为可变标签，受唯一约束）
		"host":           req.Host,
		"grpc_port":      req.GrpcPort,
		"ws_port":        req.WsPort,
		"status":         model.NodeStatusOnline,
		"os":             req.Os,
		"arch":           req.Arch,
		"cpu_cores":      req.CpuCores,
		"memory_mb":      req.MemoryMb,
		"disk_total_mb":  req.DiskTotalMb,
		"last_heartbeat": time.Now(),
	}
	if err := h.db.Model(node).Updates(updates).Error; err != nil {
		slog.Error("更新节点失败", "name", req.Name, "error", err)
		return nil, err
	}
	slog.Info("节点已重新注册", "name", req.Name, "uuid", node.UUID, "matchBy", matchBy)

	h.connectWorker(node.UUID, req)
	return &workerpb.RegisterResponse{NodeUuid: node.UUID, NodeSecret: node.Secret}, nil
}

// createNewNode 创建全新节点：凭有效 enrollment token 准入（FR-080，见 ADR-020），
// 换发全新 UUID/secret。未注入校验器（开发/既有部署零配置）则退化为自助注册。
func (h *ControlPlaneHandler) createNewNode(ctx context.Context, req *workerpb.RegisterRequest) (*workerpb.RegisterResponse, error) {
	newUUID := uuid.New().String()
	if h.enroll != nil {
		enrollToken := enrollTokenFromContext(ctx)
		if cerr := h.enroll.ConsumeForNewNode(enrollToken, newUUID); cerr != nil {
			slog.Warn("新节点注册被拒：enrollment token 无效", "name", req.Name)
			return nil, status.Errorf(codes.PermissionDenied,
				"新节点注册需要有效的 enrollment token（请在面板「添加节点」重新生成）")
		}
	}

	now := time.Now()
	node := model.Node{
		UUID:          newUUID,
		Name:          req.Name,
		Host:          req.Host,
		GRPCPort:      int(req.GrpcPort),
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

	h.connectWorker(node.UUID, req)
	return &workerpb.RegisterResponse{NodeUuid: node.UUID, NodeSecret: node.Secret}, nil
}

// connectWorker 建立到 Worker Node 的反向 gRPC 连接（req.GrpcPort>0 时）。失败仅告警、不阻断注册。
func (h *ControlPlaneHandler) connectWorker(nodeUUID string, req *workerpb.RegisterRequest) {
	if req.GrpcPort <= 0 {
		return
	}
	addr := fmt.Sprintf("%s:%d", req.Host, req.GrpcPort)
	if err := h.pool.Connect(nodeUUID, addr); err != nil {
		slog.Warn("连接 Worker Node 失败，稍后重试", "nodeUUID", nodeUUID, "addr", addr, "error", err)
		return
	}
	if h.onWorkerConnect != nil {
		h.onWorkerConnect(nodeUUID)
	}
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
	// 首次心跳到达时校验 node_secret（通过 gRPC metadata 传递，不改 proto）。
	// secret 在 Register 阶段由 CP 签发，Worker 存入本地并在每次心跳携带。
	var nodeSecretValid bool
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if vals := md.Get(nodeSecretHeader); len(vals) > 0 {
			secret := vals[0]
			// 用第一条心跳的 nodeUUID 查 DB secret 并校验；
			// 无 secret header 的旧版 Worker（FR-004 阶段）跳过校验以保持向后兼容。
			_ = secret // 实际校验在首次 Recv 拿到 nodeUUID 后进行
			nodeSecretValid = true
		}
	}

	for {
		req, err := stream.Recv()
		if err != nil {
			slog.Warn("心跳流断开", "error", err)
			return err
		}

		// 首次收到心跳时做 secret 校验（需要 nodeUUID 查 DB）
		if nodeSecretValid {
			var node model.Node
			if err := h.db.Where("uuid = ?", req.NodeUuid).First(&node).Error; err != nil {
				slog.Warn("心跳鉴权失败：节点不存在", "nodeUUID", req.NodeUuid)
				return status.Errorf(codes.NotFound, "节点 %s 不存在", req.NodeUuid)
			}
			md, _ := metadata.FromIncomingContext(stream.Context())
			secret := md.Get(nodeSecretHeader)[0]
			if node.Secret != secret {
				slog.Warn("心跳鉴权失败：secret 不匹配", "nodeUUID", req.NodeUuid)
				return status.Errorf(codes.PermissionDenied, "心跳鉴权失败")
			}
			// 校验通过后关闭标记，后续心跳不再重复查 DB
			nodeSecretValid = false
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

		if err := h.db.Model(&model.Node{}).Where("uuid = ?", req.NodeUuid).Updates(updates).Error; err != nil {
			slog.Warn("更新心跳数据失败", "nodeUUID", req.NodeUuid, "error", err)
		}

		// CP 重启后反向连接池为空，而 Worker 仅启动时注册一次、不会重连后重注册，
		// 导致 CP→Worker 的 RPC（建 Bot/装 JDK/拉状态）全部 NODE_OFFLINE。
		// 借心跳重建：池中缺该 Worker 客户端时按节点 host+grpcPort 重连。
		if _, ok := h.pool.Get(req.NodeUuid); !ok {
			var node model.Node
			if err := h.db.Where("uuid = ?", req.NodeUuid).First(&node).Error; err == nil && node.GRPCPort > 0 {
				addr := fmt.Sprintf("%s:%d", node.Host, node.GRPCPort)
				if err := h.pool.Connect(node.UUID, addr); err != nil {
					slog.Warn("心跳重建 Worker 反向连接失败", "nodeUUID", node.UUID, "addr", addr, "error", err)
				} else {
					slog.Info("心跳重建到 Worker 的反向 gRPC 连接", "nodeUUID", node.UUID, "addr", addr)
					if h.onWorkerConnect != nil {
						h.onWorkerConnect(node.UUID)
					}
				}
			}
		}

		// 同步实例状态并对账（即使 Worker 上报空也要对账：
		// Worker 重启未恢复某实例时，DB 会永远卡在 RUNNING 致所有生命周期操作 422）。
		h.syncInstanceStates(req.NodeUuid, req.Instances)

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
		if h.proxy != nil {
			resp.ProxyUrl, resp.ProxyNoProxy, resp.ProxyGeneration = h.proxy.EffectiveNodeProxyByUUID(req.NodeUuid)
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
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
	reported := make([]string, 0, len(states))
	for _, s := range states {
		reported = append(reported, s.InstanceUuid)
		status := model.InstanceStatus(s.State)
		if err := h.db.Model(&model.Instance{}).
			Where("uuid = ?", s.InstanceUuid).
			Update("status", status).Error; err != nil {
			slog.Warn("同步实例状态失败", "instanceUUID", s.InstanceUuid, "state", s.State, "error", err)
		}
	}

	// 对账：本节点上 DB 认为在运行（RUNNING/STARTING/STOPPING）但 Worker 未上报的实例，
	// 说明 Worker 已不再持有它（如 Worker 重启未恢复），置为 STOPPED，
	// 否则实例永远卡 RUNNING、start/stop/kill 全部 422，无法操作。
	var node model.Node
	if err := h.db.Where("uuid = ?", nodeUUID).First(&node).Error; err != nil {
		return
	}
	q := h.db.Model(&model.Instance{}).
		Where("node_id = ? AND status IN ?", node.ID, []string{"RUNNING", "STARTING", "STOPPING"})
	if len(reported) > 0 {
		q = q.Where("uuid NOT IN ?", reported)
	}
	if err := q.Update("status", "STOPPED").Error; err != nil {
		slog.Warn("对账离线实例状态失败", "nodeUUID", nodeUUID, "error", err)
	}
}
