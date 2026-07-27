package grpc

import (
	"io"
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

// ControlPlaneHandler Control Plane 侧的 gRPC 处理器。
// 处理来自 Worker Node 的 Register 和 Heartbeat 请求。
// OrphanRuntimeIngester 心跳反向对账入口（FR-326）；由 service.OrphanRuntimeTracker 实现。
// 声明在 grpc 包避免 controlplane/grpc → service 循环依赖。
type OrphanRuntimeIngester interface {
	ObserveHeartbeat(nodeUUID string, reported []*workerpb.InstanceState)
}

type ControlPlaneHandler struct {
	workerpb.WorkerServiceServer
	db            *gorm.DB
	pool          *ClientPool
	metrics       MetricIngester        // 时序指标入库（nil 时心跳不落时序）
	tasks         TaskIngester          // 任务进度入库（nil 时心跳不落任务，FR-183）
	enroll        EnrollmentValidator   // enrollment token 校验消费（nil 时退化为 FR-004 自助注册）
	proxy         NodeProxyResolver     // 节点期望代理解析（nil 时心跳响应不携带代理，FR-185）
	wsTokenSecret string                // CP↔Worker WS 令牌密钥（空时注册/心跳响应不携带，FR-275）
	orphans       OrphanRuntimeIngester // 反向对账（nil 时不启用，FR-326）
}

// NewControlPlaneHandler 创建处理器。
func NewControlPlaneHandler(db *gorm.DB, pool *ClientPool) *ControlPlaneHandler {
	return &ControlPlaneHandler{db: db, pool: pool}
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

		if err := h.db.Model(&model.Node{}).Where("uuid = ?", req.NodeUuid).Updates(updates).Error; err != nil {
			slog.Warn("更新心跳数据失败", "nodeUUID", req.NodeUuid, "error", err)
		}

		// 同步实例状态并对账（即使 Worker 上报空也要对账：
		// Worker 重启未恢复某实例时，DB 会永远卡在 RUNNING 致所有生命周期操作 422）。
		h.syncInstanceStates(req.NodeUuid, req.Instances)

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
		// 携带该节点「已请求取消」的任务 id，Worker 据此真中断对应运行中任务（FR-227）。
		if h.tasks != nil {
			resp.CancelTaskIds = h.tasks.PendingCancelTaskIDsByNodeUUID(req.NodeUuid)
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
		q := h.db.Model(&model.Instance{}).Where("uuid = ?", s.InstanceUuid)
		// DAMAGED（FR-342 搭建失败损毁）是 CP 侧生命周期态，Worker 不感知：损毁实例在 Worker
		// 记账里本就是「没在跑」（STOPPED/CRASHED），与损毁不矛盾。此前无条件覆写会在搭建失败后的
		// 下一个心跳把 DAMAGED 降级成 STOPPED，启动守卫随即失效（真机回归）。仅当 Worker 上报
		// 运行类状态（进程确实活着）才允许覆盖 DAMAGED。
		if status == model.InstanceStatusStopped || status == model.InstanceStatusCrashed {
			q = q.Where("status <> ?", model.InstanceStatusDamaged)
		}
		if err := q.Update("status", status).Error; err != nil {
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
