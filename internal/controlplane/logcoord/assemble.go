// Package logcoord — 生产装配辅助（T11）。
package logcoord

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ConnectedNodes 查询当前活跃隧道/连接的 Worker UUID 集合。
type ConnectedNodes interface {
	ConnectedNodes() []string
}

// ProductionTargetResolver 把 CP 授权目标解析为可扇出联邦目标。
//
// 支持：
//   - `*` / 空：展开为全部实例（每条携带所属节点 WorkerUUID）
//   - `inst:<id>`：解析实例 → 所属节点 UUID（P1：路由层实例级查询）
//   - `node:<id>`：节点级目标
//   - 节点 UUID 字符串：兼容直接传 UUID
//
// Readiness 以 **节点状态 ∩ 活跃隧道** 判定：无活跃连接或节点 Offline 时为
// ReadyOffline，供 onlineOnly 筛选与 coverage 使用（P1：禁止无条件 ReadyOnline）。
type ProductionTargetResolver struct {
	nodes     *service.NodeService
	instances *service.InstanceService
	holders   *service.LogTargetHolderService
	connected ConnectedNodes
}

// NewProductionTargetResolver 创建生产目标解析器。
func NewProductionTargetResolver(nodes *service.NodeService, instances *service.InstanceService, connected ConnectedNodes, holders *service.LogTargetHolderService) *ProductionTargetResolver {
	return &ProductionTargetResolver{nodes: nodes, instances: instances, connected: connected, holders: holders}
}

func (r *ProductionTargetResolver) connectedSet() map[string]bool {
	out := make(map[string]bool)
	if r == nil || r.connected == nil {
		return out
	}
	for _, id := range r.connected.ConnectedNodes() {
		if id != "" {
			out[id] = true
		}
	}
	return out
}

func readinessFor(n model.Node, conn map[string]bool) Readiness {
	if n.UUID == "" {
		return ReadyNotReady
	}
	// 心跳在线 + 活跃隧道：在线；否则离线（onlineOnly 不得扇出）。
	if n.Status == model.NodeStatusOnline && conn[n.UUID] {
		return ReadyOnline
	}
	if n.Status == model.NodeStatusOnline {
		// 节点标记在线但隧道不可用：按不就绪处理，避免假扇出。
		return ReadyNotReady
	}
	return ReadyOffline
}

// Resolve 实现 TargetResolver。
func (r *ProductionTargetResolver) Resolve(ctx context.Context, authorized []string) ([]TargetInfo, error) {
	if r == nil || r.nodes == nil {
		return nil, errNoResolver
	}
	nodes, err := r.nodes.List()
	if err != nil {
		return nil, err
	}
	byID := make(map[uint]model.Node, len(nodes))
	byUUID := make(map[string]model.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
		if n.UUID != "" {
			byUUID[n.UUID] = n
		}
	}
	conn := r.connectedSet()
	ready := func(n model.Node) Readiness { return readinessFor(n, conn) }

	wantAll := len(authorized) == 0
	wanted := make(map[string]bool, len(authorized))
	for _, id := range authorized {
		wanted[id] = true
		if id == "*" {
			wantAll = true
		}
	}

	out := make([]TargetInfo, 0, len(nodes))

	appendNode := func(n model.Node, targetID string) {
		if n.UUID == "" {
			return
		}
		out = append(out, TargetInfo{
			ID: targetID, AuthorizationID: targetID,
			WorkerID:  n.UUID,
			Readiness: ready(n),
		})
	}

	appendInstance := func(inst model.Instance) error {
		n, ok := byID[inst.NodeID]
		if !ok || n.UUID == "" {
			return fmt.Errorf("instance %d: node %d has no worker uuid", inst.ID, inst.NodeID)
		}
		out = append(out, TargetInfo{
			ID:              fmt.Sprintf("inst:%d", inst.ID),
			AuthorizationID: fmt.Sprintf("inst:%d", inst.ID),
			WorkerID:        n.UUID,
			Readiness:       ready(n),
		})
		if r.holders != nil {
			holders, err := r.holders.ListForInstance(inst.ID)
			if err != nil {
				return err
			}
			for _, h := range holders {
				if h.Current || h.WorkerUUID == n.UUID {
					continue
				}
				readiness := ReadyOffline
				if conn[h.WorkerUUID] {
					readiness = ReadyOnline
				}
				out = append(out, TargetInfo{
					ID: fmt.Sprintf("holder:%d", h.ID), AuthorizationID: fmt.Sprintf("inst:%d", inst.ID),
					WorkerID: h.WorkerUUID, WorkerTargetID: h.StorageNamespace,
					Readiness: readiness, HistoricalHolder: true,
				})
			}
		}
		return nil
	}

	if wantAll {
		// 全量视图必须包含 Worker/Node 自身日志，即使节点当前没有实例。
		for _, n := range nodes {
			appendNode(n, fmt.Sprintf("node:%d", n.ID))
		}
		if r.instances == nil {
			return out, nil
		}
		insts, err := r.instances.List(service.InstanceFilter{})
		if err != nil {
			return nil, err
		}
		for _, inst := range insts {
			if err := appendInstance(inst); err != nil {
				continue
			}
		}
		return out, nil
	}

	for _, id := range authorized {
		switch {
		case strings.HasPrefix(id, "inst:"):
			if r.instances == nil {
				return nil, fmt.Errorf("instance target %s: instance service unavailable", id)
			}
			num, err := strconv.ParseUint(strings.TrimPrefix(id, "inst:"), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid instance target %q", id)
			}
			inst, err := r.instances.GetByID(uint(num))
			if err != nil {
				// 实例不存在：仍返回 not_ready 占位，coverage 可解释。
				out = append(out, TargetInfo{ID: id, Readiness: ReadyNotReady})
				continue
			}
			if err := appendInstance(*inst); err != nil {
				out = append(out, TargetInfo{ID: id, Readiness: ReadyNotReady})
			}
		case strings.HasPrefix(id, "node:"):
			num, err := strconv.ParseUint(strings.TrimPrefix(id, "node:"), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid node target %q", id)
			}
			n, ok := byID[uint(num)]
			if !ok {
				out = append(out, TargetInfo{ID: id, Readiness: ReadyNotReady})
				continue
			}
			appendNode(n, id)
		default:
			// 节点 UUID
			if n, ok := byUUID[id]; ok {
				appendNode(n, fmt.Sprintf("node:%d", n.ID))
				continue
			}
			if wanted[id] {
				out = append(out, TargetInfo{ID: id, Readiness: ReadyNotReady})
			}
		}
	}
	return out, nil
}

// PoolClientFactory 用 CP gRPC ClientPool 适配 Worker Log 客户端。
func PoolClientFactory(pool *cpgrpc.ClientPool) WorkerClientFactory {
	return func(_ context.Context, workerID string) (workerpb.WorkerServiceClient, error) {
		if pool == nil {
			return nil, fmt.Errorf("logcoord: client pool is nil")
		}
		cli, ok := pool.Get(workerID)
		if !ok || cli == nil {
			return nil, fmt.Errorf("logcoord: worker %s not connected", workerID)
		}
		return cli.Worker, nil
	}
}

// Assemble 生产装配：实例/节点/历史 holder 目标解析 + 隧道 Dialer + Coordinator。
func Assemble(pool *cpgrpc.ClientPool, nodeSvc *service.NodeService, instSvc *service.InstanceService, holders *service.LogTargetHolderService) *Coordinator {
	resolver := NewProductionTargetResolver(nodeSvc, instSvc, pool, holders)
	return New(resolver, NewTunnelDialer(PoolClientFactory(pool)))
}
