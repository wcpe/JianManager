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

	mu               sync.RWMutex
	active           map[string]int // nodeUUID → 活跃隧道数（常态 ≤1；重建瞬间旧未关新已开可短暂为 2）
	onConnected      func(nodeUUID string)
	onFirstConnected func(nodeUUID string)
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

// SetOnConnected 设置「每次隧道建立」回调（含瞬时 active=2 的新连）。用于幂等重推等
// 每次连接都应尝试触发的工作（去重由回调侧 ResyncDeduper 吸收）。
func (r *TunnelRegistry) SetOnConnected(fn func(nodeUUID string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onConnected = fn
}

// SetOnFirstConnected 设置「节点从无活跃隧道变为有活跃隧道（active 0→1）」回调。
// 用于只应每连接建立**一次**的长驻订阅类工作（事件流 / 插件事件流 / Bot Fleet 恢复），
// 避免瞬时 active=2 时重复建立长驻订阅流（FR-456 F4：重复订阅会导致 stdout/stderr 双写、
// 事件重复扇出）。
func (r *TunnelRegistry) SetOnFirstConnected(fn func(nodeUUID string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onFirstConnected = fn
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
	onFirstConnected := r.onFirstConnected
	r.mu.Unlock()
	slog.Info("节点反向隧道已建立", "nodeUUID", uuid, "active", n)
	// FR-455②：不再以 n==1 为触发条件。CP 重启后旧隧道 onClose 常晚于新隧道 onOpen
	//（瞬时 active=2），若只在 n==1 触发，新连不触发重推、该轮节点规格同步丢失。
	// 改为「隧道建立即触发」，由回调侧按节点去重 + 幂等（ResyncDeduper）吸收重复触发。
	if onConnected != nil {
		go onConnected(uuid)
	}
	// FR-456 F4：长驻订阅类副作用（事件流 / 插件事件流 / Bot Fleet 恢复）只在节点「从无到有」
	//（active 0→1）时触发一次，避免瞬时 active=2 时重复建立长驻订阅流。重推仍走上面的
	// onConnected（每次建立都触发，由 ResyncDeduper 幂等）——两条语义分离，互不影响。
	if n == 1 && onFirstConnected != nil {
		go onFirstConnected(uuid)
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
