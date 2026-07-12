package grpc

import (
	"log/slog"

	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FetchBotWorkerArchive Worker 自愈拉取 CP 内嵌 bot-worker dist（FR-308，见 ADR-070）。
//
// 走 Worker 既有的 CP gRPC 连接（注册/心跳同通道，NAT 节点天然可达），凭 node_uuid+node_secret
// 鉴权（与重注册同源校验）；known_sha256 与内嵌指纹一致时回空归档省流。归档 ~几百 KB，
// 远低于 64MiB 单消息上限（FR-305），单 unary 足够、不需流式。
func (h *ControlPlaneHandler) FetchBotWorkerArchive(ctx context.Context, req *workerpb.FetchBotWorkerArchiveRequest) (*workerpb.FetchBotWorkerArchiveResponse, error) {
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
		slog.Warn("bot-worker 归档拉取被拒：node_secret 不匹配", "uuid", req.NodeUuid)
		return nil, status.Errorf(codes.PermissionDenied, "节点身份校验失败")
	}

	m, ok := cpembed.EmbeddedBotWorkerManifest()
	if !ok {
		return &workerpb.FetchBotWorkerArchiveResponse{
			Success: false,
			Error:   "CP 未内嵌 bot-worker 归档（构建时未跑 make embed-botworker），Worker 将沿用本地已有 dist",
		}, nil
	}
	resp := &workerpb.FetchBotWorkerArchiveResponse{Success: true, Sha256: m.SHA256, Version: m.Version}
	if req.KnownSha256 == m.SHA256 {
		return resp, nil // 指纹一致：不回字节，Worker 沿用本地
	}
	archive, ok := cpembed.EmbeddedBotWorkerArchive()
	if !ok {
		return &workerpb.FetchBotWorkerArchiveResponse{Success: false, Error: "内嵌 bot-worker 归档缺失（manifest 存在但归档不可读）"}, nil
	}
	resp.Archive = archive
	slog.Info("下发 bot-worker 归档", "node", node.Name, "version", m.Version, "size", len(archive))
	return resp, nil
}
