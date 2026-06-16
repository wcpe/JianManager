package grpc

import (
	"log/slog"
	"time"

	"golang.org/x/net/context"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// ControlPlaneHandler Control Plane 侧的 gRPC 处理器。
// 处理来自 Worker Node 的 Register 和 Heartbeat 请求。
type ControlPlaneHandler struct {
	workerpb.WorkerServiceServer
	db *gorm.DB
}

// NewControlPlaneHandler 创建处理器。
func NewControlPlaneHandler(db *gorm.DB) *ControlPlaneHandler {
	return &ControlPlaneHandler{db: db}
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

	return &workerpb.RegisterResponse{
		NodeUuid:   node.UUID,
		NodeSecret: node.Secret,
	}, nil
}

// Heartbeat 处理 Worker Node 心跳（双向流）。
func (h *ControlPlaneHandler) Heartbeat(stream workerpb.WorkerService_HeartbeatServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			slog.Warn("心跳流断开", "error", err)
			return err
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
