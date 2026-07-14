package grpc

import (
	"log/slog"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// maxCrashSnapshotsPerInstance 每实例滚动保留的崩溃快照条数（K=5，写死不做配置，FR-313 spec §6）。
const maxCrashSnapshotsPerInstance = 5

// ReportCrashSnapshot Worker 上报实例崩溃快照（FR-313）。
//
// 走注册/心跳同信道（Worker→CP 出站 gRPC，NAT 节点天然可达），凭 node_uuid+node_secret
// 鉴权（与 FetchBotWorkerArchive 同源校验），且实例必须属于该节点（节点只能报自己的实例）。
// 落库后按实例修剪只留最近 K 条（按 occurred_at 倒序），防连崩刷表。
func (h *ControlPlaneHandler) ReportCrashSnapshot(ctx context.Context, req *workerpb.ReportCrashSnapshotRequest) (*workerpb.ReportCrashSnapshotResponse, error) {
	if req.NodeUuid == "" || req.NodeSecret == "" {
		return nil, status.Errorf(codes.Unauthenticated, "缺少节点身份（node_uuid/node_secret）")
	}
	var node model.Node
	if err := h.db.Where("uuid = ?", req.NodeUuid).First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, status.Errorf(codes.PermissionDenied, "节点身份校验失败")
		}
		return nil, err
	}
	if node.Secret != req.NodeSecret {
		slog.Warn("崩溃快照上报被拒：node_secret 不匹配", "uuid", req.NodeUuid)
		return nil, status.Errorf(codes.PermissionDenied, "节点身份校验失败")
	}

	var inst model.Instance
	if err := h.db.Where("uuid = ?", req.InstanceUuid).First(&inst).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, status.Errorf(codes.NotFound, "实例不存在")
		}
		return nil, err
	}
	if inst.NodeID != node.ID {
		slog.Warn("崩溃快照上报被拒：实例不属于该节点", "node", node.Name, "instance", req.InstanceUuid)
		return nil, status.Errorf(codes.PermissionDenied, "实例不属于该节点")
	}

	snap := model.InstanceCrashSnapshot{
		InstanceID: inst.ID,
		OccurredAt: time.UnixMilli(req.OccurredAtUnixMs),
		ExitCode:   int(req.ExitCode),
		Signal:     req.Signal,
		DurationMs: req.DurationMs,
		TailOutput: req.TailOutput,
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&snap).Error; err != nil {
			return err
		}
		return pruneCrashSnapshots(tx, inst.ID)
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "崩溃快照落库失败: %v", err)
	}

	slog.Info("崩溃快照已入库", "instance", inst.Name, "exitCode", snap.ExitCode, "durationMs", snap.DurationMs)
	return &workerpb.ReportCrashSnapshotResponse{}, nil
}

// pruneCrashSnapshots 按实例修剪崩溃快照，只保留 occurred_at 最新的 K 条。
// 先查保留集 id 再删补集（两步而非 NOT IN 子查询 + LIMIT：MySQL 不支持 IN 子查询带 LIMIT）。
func pruneCrashSnapshots(tx *gorm.DB, instanceID uint) error {
	var keepIDs []uint
	if err := tx.Model(&model.InstanceCrashSnapshot{}).
		Where("instance_id = ?", instanceID).
		Order("occurred_at DESC, id DESC").
		Limit(maxCrashSnapshotsPerInstance).
		Pluck("id", &keepIDs).Error; err != nil {
		return err
	}
	return tx.Where("instance_id = ? AND id NOT IN ?", instanceID, keepIDs).
		Delete(&model.InstanceCrashSnapshot{}).Error
}
