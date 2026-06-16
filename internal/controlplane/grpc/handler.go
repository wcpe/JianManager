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

	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// nodeSecretHeader 心跳请求中携带 node_secret 的 gRPC metadata header 名。
// 与 internal/worker/heartbeat 中的常量保持一致。
const nodeSecretHeader = "node-secret"

// ControlPlaneHandler Control Plane 侧的 gRPC 处理器。
// 处理来自 Worker Node 的 Register 和 Heartbeat 请求。
type ControlPlaneHandler struct {
	workerpb.WorkerServiceServer
	db   *gorm.DB
	pool *ClientPool
}

// NewControlPlaneHandler 创建处理器。
func NewControlPlaneHandler(db *gorm.DB, pool *ClientPool) *ControlPlaneHandler {
	return &ControlPlaneHandler{db: db, pool: pool}
}

// Register 处理 Worker Node 注册。
func (h *ControlPlaneHandler) Register(ctx context.Context, req *workerpb.RegisterRequest) (*workerpb.RegisterResponse, error) {
	// 查找已有节点（按名称匹配）
	var node model.Node
	err := h.db.Where("name = ?", req.Name).First(&node).Error

	if err == gorm.ErrRecordNotFound {
		// 新节点，创建记录
		now := time.Now()
		node = model.Node{
			Name:     req.Name,
			Host:     req.Host,
			GRPCPort: int(req.GrpcPort),
			WSPort:   int(req.WsPort),
			Secret:   uuid.New().String(),
			Status:   model.NodeStatusOnline,
			OS:       req.Os,
			Arch:     req.Arch,
			CPUCores: int(req.CpuCores),
			MemoryMB: req.MemoryMb,
			DiskTotalMB: req.DiskTotalMb,
			LastHeartbeat: &now,
		}

		if err := h.db.Create(&node).Error; err != nil {
			slog.Error("创建节点失败", "name", req.Name, "error", err)
			return nil, err
		}

		slog.Info("新节点已注册", "name", req.Name, "uuid", node.UUID)
	} else if err != nil {
		return nil, err
	} else {
		// 已有节点，更新信息
		updates := map[string]interface{}{
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

		if err := h.db.Model(&node).Updates(updates).Error; err != nil {
			slog.Error("更新节点失败", "name", req.Name, "error", err)
			return nil, err
		}

		slog.Info("节点已重新注册", "name", req.Name, "uuid", node.UUID)
	}

	// 建立到 Worker Node 的反向 gRPC 连接
	if req.GrpcPort > 0 {
		addr := fmt.Sprintf("%s:%d", req.Host, req.GrpcPort)
		if err := h.pool.Connect(node.UUID, addr); err != nil {
			slog.Warn("连接 Worker Node 失败，稍后重试", "nodeUUID", node.UUID, "addr", addr, "error", err)
		}
	}

	return &workerpb.RegisterResponse{
		NodeUuid:   node.UUID,
		NodeSecret: node.Secret,
	}, nil
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
			"cpu_usage":      req.CpuUsage,
			"memory_usage":   req.MemoryUsage,
			"disk_usage":     req.DiskUsage,
			"memory_used_mb": req.MemoryUsedMb,
			"disk_used_mb":   req.DiskUsedMb,
			"last_heartbeat": time.Now(),
			"status":         model.NodeStatusOnline,
		}

		if err := h.db.Model(&model.Node{}).Where("uuid = ?", req.NodeUuid).Updates(updates).Error; err != nil {
			slog.Warn("更新心跳数据失败", "nodeUUID", req.NodeUuid, "error", err)
		}

		// 同步实例状态
		if len(req.Instances) > 0 {
			h.syncInstanceStates(req.Instances)
		}

		// 返回响应
		if err := stream.Send(&workerpb.HeartbeatResponse{
			Timestamp: time.Now().Unix(),
		}); err != nil {
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
func (h *ControlPlaneHandler) syncInstanceStates(states []*workerpb.InstanceState) {
	for _, s := range states {
		status := model.InstanceStatus(s.State)
		if err := h.db.Model(&model.Instance{}).
			Where("uuid = ?", s.InstanceUuid).
			Update("status", status).Error; err != nil {
			slog.Warn("同步实例状态失败", "instanceUUID", s.InstanceUuid, "state", s.State, "error", err)
		}
	}
}
