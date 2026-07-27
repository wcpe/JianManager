package grpc

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/jhump/grpctunnel"
	"github.com/jhump/grpctunnel/tunnelpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TunnelRegistry 管理 Worker 主动建立的 gRPC 反向隧道（FR-281，见 ADR-066）。
// Worker 在 CP 既有 gRPC 端口上开常驻反向隧道并以节点身份（node-uuid/node-secret
// metadata，ADR-039）锚定归属；ClientPool 只从活跃隧道取得节点通道。
// 鉴权在 StreamAuthInterceptor 前置完成——到达 handler 的隧道必已通过 secret 校验，
// 杜绝「节点 A 冒领节点 B 的指令」。
type TunnelRegistry struct {
	db      *gorm.DB
	handler *grpctunnel.TunnelServiceHandler

	mu          sync.RWMutex
	active      map[string]int // nodeUUID → 活跃隧道数（常态 ≤1；重建瞬间旧未关新已开可短暂为 2）
	onConnected func(nodeUUID string)
}

// NewTunnelRegistry 创建反向隧道注册表。
func NewTunnelRegistry(db *gorm.DB) *TunnelRegistry {
	r := &TunnelRegistry{db: db, active: make(map[string]int)}
	r.handler = grpctunnel.NewTunnelServiceHandler(grpctunnel.TunnelServiceHandlerOptions{
		// 亲和键 = 节点 UUID：CP 侧按 UUID 取该节点专属通道（KeyAsChannel）。
		AffinityKey: func(t grpctunnel.TunnelChannel) any {
			return nodeUUIDFromContext(t.Context())
		},
		OnReverseTunnelOpen:  r.onOpen,
		OnReverseTunnelClose: r.onClose,
	})
	return r
}

// Service 返回挂到 CP gRPC server 的 TunnelService 实现（库自带 tunnel.proto）。
func (r *TunnelRegistry) Service() tunnelpb.TunnelServiceServer {
	return r.handler.Service()
}

// StreamAuthInterceptor 校验反向隧道建立请求的节点身份。
// 仅拦截 OpenReverseTunnel；其余流式方法（如 WorkerService.Heartbeat 走自身校验）原样放行。
// 缺身份/节点不存在/secret 不匹配一律拒绝——反向隧道是指令通道，不设旧版兼容豁免
// （老版本 Worker 根本不调本方法，不存在兼容路径）。
func (r *TunnelRegistry) StreamAuthInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if info.FullMethod != tunnelpb.TunnelService_OpenReverseTunnel_FullMethodName {
			return handler(srv, ss)
		}
		ctx := ss.Context()
		uuid := nodeUUIDFromContext(ctx)
		secret := nodeSecretFromContext(ctx)
		if uuid == "" || secret == "" {
			return status.Error(codes.Unauthenticated, "反向隧道身份无效")
		}
		var node model.Node
		if err := r.db.Where("uuid = ?", uuid).First(&node).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				slog.Error("反向隧道鉴权查询失败", "err", err)
				return status.Error(codes.Unavailable, "反向隧道鉴权暂不可用")
			}
			slog.Warn("反向隧道鉴权失败：节点不存在", "nodeUUID", uuid)
			return status.Error(codes.Unauthenticated, "反向隧道身份无效")
		}
		if node.Secret != secret {
			slog.Warn("反向隧道鉴权失败：secret 不匹配", "nodeUUID", uuid)
			return status.Error(codes.Unauthenticated, "反向隧道身份无效")
		}
		return handler(srv, ss)
	}
}

// Channel 返回指向该节点的隧道通道；无活跃隧道返回 false。
func (r *TunnelRegistry) Channel(nodeUUID string) (grpc.ClientConnInterface, bool) {
	r.mu.RLock()
	n := r.active[nodeUUID]
	r.mu.RUnlock()
	if n == 0 {
		return nil, false
	}
	channel := r.handler.KeyAsChannel(nodeUUID)
	if !channel.Ready() {
		return nil, false
	}
	return channel, true
}

// SetOnConnected 设置节点首次建立反向隧道后的回调。
func (r *TunnelRegistry) SetOnConnected(fn func(nodeUUID string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onConnected = fn
}

// ConnectedNodes 返回当前存在活跃反向隧道的节点 UUID。
func (r *TunnelRegistry) ConnectedNodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodes := make([]string, 0, len(r.active))
	for uuid := range r.active {
		nodes = append(nodes, uuid)
	}
	return nodes
}

// Connected 报告该节点当前是否有活跃反向隧道（节点观测面用）。
func (r *TunnelRegistry) Connected(nodeUUID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active[nodeUUID] > 0
}

func (r *TunnelRegistry) onOpen(t grpctunnel.TunnelChannel) {
	uuid := nodeUUIDFromContext(t.Context())
	if uuid == "" {
		// 拦截器已拒绝无身份隧道，此处防御性兜底。
		return
	}
	r.mu.Lock()
	r.active[uuid]++
	n := r.active[uuid]
	onConnected := r.onConnected
	r.mu.Unlock()
	slog.Info("节点反向隧道已建立", "nodeUUID", uuid, "active", n)
	if n == 1 && onConnected != nil {
		go onConnected(uuid)
	}
}

func (r *TunnelRegistry) onClose(t grpctunnel.TunnelChannel) {
	uuid := nodeUUIDFromContext(t.Context())
	if uuid == "" {
		return
	}
	r.mu.Lock()
	if r.active[uuid] > 0 {
		r.active[uuid]--
	}
	n := r.active[uuid]
	if n == 0 {
		delete(r.active, uuid)
	}
	r.mu.Unlock()
	slog.Info("节点反向隧道已断开", "nodeUUID", uuid, "active", n)
}
