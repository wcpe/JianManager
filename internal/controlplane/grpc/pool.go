package grpc

import (
	"fmt"
	"log/slog"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/wxys233/JianManager/proto/workerpb"
)

// Client gRPC 客户端封装。
type Client struct {
	Conn   *grpc.ClientConn
	Worker workerpb.WorkerServiceClient
	NodeUUID string
}

// ClientPool 管理到多个 Worker Node 的 gRPC 连接。
type ClientPool struct {
	mu      sync.RWMutex
	clients map[string]*Client // nodeUUID → Client
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

	// 如果已连接，先关闭旧连接
	if existing, ok := p.clients[nodeUUID]; ok {
		existing.Conn.Close()
		delete(p.clients, nodeUUID)
	}

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("连接 Worker Node %s 失败: %w", addr, err)
	}

	client := &Client{
		Conn:     conn,
		Worker:   nil, // TODO: 生成代码后使用 NewWorkerServiceClient(conn)
		NodeUUID: nodeUUID,
	}

	p.clients[nodeUUID] = client
	slog.Info("已连接 Worker Node", "nodeUUID", nodeUUID, "addr", addr)
	return nil
}

// Get 获取指定节点的客户端。
func (p *ClientPool) Get(nodeUUID string) (*Client, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	client, ok := p.clients[nodeUUID]
	return client, ok
}

// Disconnect 断开指定节点的连接。
func (p *ClientPool) Disconnect(nodeUUID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if client, ok := p.clients[nodeUUID]; ok {
		client.Conn.Close()
		delete(p.clients, nodeUUID)
		slog.Info("已断开 Worker Node", "nodeUUID", nodeUUID)
	}
}

// Close 关闭所有连接。
func (p *ClientPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for uuid, client := range p.clients {
		client.Conn.Close()
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
