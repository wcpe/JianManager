package grpc

import (
	"fmt"
	"log/slog"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/wcpe/JianManager/internal/platform/grpcmsg"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// Client gRPC 客户端封装。
type Client struct {
	Conn   *grpc.ClientConn
	Worker workerpb.WorkerServiceClient
	NodeUUID string
}

// TunnelProvider 提供节点反向隧道通道（FR-281，见 ADR-066）；由 TunnelRegistry 实现。
// 以接口声明避免 pool 对隧道实现的硬依赖（测试可注入假 provider）。
type TunnelProvider interface {
	Channel(nodeUUID string) (grpc.ClientConnInterface, bool)
}

// ClientPool 管理到多个 Worker Node 的 gRPC 连接。
type ClientPool struct {
	mu      sync.RWMutex
	clients map[string]*Client // nodeUUID → Client
	tunnels TunnelProvider     // 反向隧道来源；nil 表示未启用（纯直拨）
}

// NewClientPool 创建客户端连接池。
func NewClientPool() *ClientPool {
	return &ClientPool{
		clients: make(map[string]*Client),
	}
}

// Connect 连接到 Worker Node。
func (p *ClientPool) Connect(nodeUUID, addr string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 如果已连接，先关闭旧连接（测试注入的客户端无底层 Conn，判空防 panic）
	if existing, ok := p.clients[nodeUUID]; ok {
		if existing.Conn != nil {
			existing.Conn.Close()
		}
		delete(p.clients, nodeUUID)
	}

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// 两方向显式 64MiB（FR-305）：接收默认仅 4MiB（大响应暗礁）、发送默认无界，
		// 与 Worker 服务端/隧道守卫同源同值（grpcmsg.MaxMessageBytes）。
		grpc.WithDefaultCallOptions(grpcmsg.CallOptions()...),
	)
	if err != nil {
		return fmt.Errorf("连接 Worker Node %s 失败: %w", addr, err)
	}

	client := &Client{
		Conn:     conn,
		Worker:   workerpb.NewWorkerServiceClient(conn),
		NodeUUID: nodeUUID,
	}

	p.clients[nodeUUID] = client
	slog.Info("已连接 Worker Node", "nodeUUID", nodeUUID, "addr", addr)
	return nil
}

// SetWorkerClientForTest 直接注入某节点的 Worker gRPC 客户端（仅测试用）。
// 让 service 层能以伪 WorkerServiceClient 覆盖 CP→Worker 的 RPC 编排，无需真起 gRPC 服务。
func (p *ClientPool) SetWorkerClientForTest(nodeUUID string, worker workerpb.WorkerServiceClient) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients[nodeUUID] = &Client{Worker: worker, NodeUUID: nodeUUID}
}

// SetTunnelProvider 注入反向隧道来源（FR-281）；main 装配时调用一次。
func (p *ClientPool) SetTunnelProvider(tp TunnelProvider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tunnels = tp
}

// Get 获取指定节点的客户端。两级取连接（ADR-066 双模式）：
// 该节点存在活跃反向隧道 → 隧道优先（NAT/内网 worker 唯一可达路径）；
// 否则回退直拨（老版本 worker、隧道断连重建窗口——行为与引入隧道前完全一致）。
func (p *ClientPool) Get(nodeUUID string) (*Client, bool) {
	p.mu.RLock()
	tp := p.tunnels
	client, ok := p.clients[nodeUUID]
	p.mu.RUnlock()

	if tp != nil {
		if ch, tunneled := tp.Channel(nodeUUID); tunneled {
			// 隧道客户端按取即建（轻量封装，无底层 Conn 需管理）。
			return &Client{Worker: workerpb.NewWorkerServiceClient(ch), NodeUUID: nodeUUID}, true
		}
	}
	return client, ok
}

// Disconnect 断开指定节点的连接。
func (p *ClientPool) Disconnect(nodeUUID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if client, ok := p.clients[nodeUUID]; ok {
		if client.Conn != nil {
			client.Conn.Close()
		}
		delete(p.clients, nodeUUID)
		slog.Info("已断开 Worker Node", "nodeUUID", nodeUUID)
	}
}

// Close 关闭所有连接。
func (p *ClientPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for uuid, client := range p.clients {
		if client.Conn != nil {
			client.Conn.Close()
		}
		slog.Info("关闭 Worker Node 连接", "nodeUUID", uuid)
	}
	p.clients = make(map[string]*Client)
}

// ConnectedNodes 返回已连接的节点 UUID 列表。
func (p *ClientPool) ConnectedNodes() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	nodes := make([]string, 0, len(p.clients))
	for uuid := range p.clients {
		nodes = append(nodes, uuid)
	}
	return nodes
}
