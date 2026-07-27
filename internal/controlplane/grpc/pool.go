package grpc

import (
	"sync"

	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// Client gRPC 客户端封装。
type Client struct {
	Conn     *grpc.ClientConn
	Worker   workerpb.WorkerServiceClient
	NodeUUID string
}

// TunnelProvider 提供节点反向隧道通道（FR-281，见 ADR-066）；由 TunnelRegistry 实现。
// 以接口声明避免 pool 对隧道实现的硬依赖（测试可注入假 provider）。
type TunnelProvider interface {
	Channel(nodeUUID string) (grpc.ClientConnInterface, bool)
}

// TunnelNodeProvider 提供当前存在活跃隧道的节点列表。
type TunnelNodeProvider interface {
	ConnectedNodes() []string
}

// ClientPool 管理到多个 Worker Node 的 gRPC 连接。
type ClientPool struct {
	mu          sync.RWMutex
	testClients map[string]*Client // 仅供单元测试注入，不创建网络连接
	tunnels     TunnelProvider     // 唯一的生产节点 RPC 承载
}

// NewClientPool 创建客户端连接池。
func NewClientPool() *ClientPool {
	return &ClientPool{
		testClients: make(map[string]*Client),
	}
}

// SetWorkerClientForTest 直接注入某节点的 Worker gRPC 客户端（仅测试用）。
// 让 service 层能以伪 WorkerServiceClient 覆盖 CP→Worker 的 RPC 编排，无需真起 gRPC 服务。
func (p *ClientPool) SetWorkerClientForTest(nodeUUID string, worker workerpb.WorkerServiceClient) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.testClients[nodeUUID] = &Client{Worker: worker, NodeUUID: nodeUUID}
}

// SetTunnelProvider 注入反向隧道来源（FR-281）；main 装配时调用一次。
func (p *ClientPool) SetTunnelProvider(tp TunnelProvider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tunnels = tp
}

// Get 获取指定节点的客户端。生产路径只允许活跃反向隧道；无隧道即不可调用。
func (p *ClientPool) Get(nodeUUID string) (*Client, bool) {
	p.mu.RLock()
	tp := p.tunnels
	testClient, testOK := p.testClients[nodeUUID]
	p.mu.RUnlock()

	if tp != nil {
		if ch, tunneled := tp.Channel(nodeUUID); tunneled {
			// 隧道客户端按取即建（轻量封装，无底层 Conn 需管理）。
			return &Client{Worker: workerpb.NewWorkerServiceClient(ch), NodeUUID: nodeUUID}, true
		}
		return nil, false
	}
	return testClient, testOK
}

// Disconnect 断开指定节点的连接。
func (p *ClientPool) Disconnect(nodeUUID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if client, ok := p.testClients[nodeUUID]; ok {
		if client.Conn != nil {
			client.Conn.Close()
		}
		delete(p.testClients, nodeUUID)
	}
}

// Close 关闭所有连接。
func (p *ClientPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, client := range p.testClients {
		if client.Conn != nil {
			client.Conn.Close()
		}
	}
	p.testClients = make(map[string]*Client)
}

// ConnectedNodes 返回已连接的节点 UUID 列表。
func (p *ClientPool) ConnectedNodes() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	tp := p.tunnels
	nodes := make([]string, 0, len(p.testClients))
	for uuid := range p.testClients {
		nodes = append(nodes, uuid)
	}
	if provider, ok := tp.(TunnelNodeProvider); ok {
		return provider.ConnectedNodes()
	}
	return nodes
}
