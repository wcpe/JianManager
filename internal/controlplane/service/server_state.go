package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// serverStateQueryTimeout 是 CP→Worker 的 QueryServerState gRPC 调用超时。
// 须大于 Worker 侧等探针回执的 5s（pluginCommandTimeout），留网络与排队余量（与 pluginExecTimeout 同量级）。
const serverStateQueryTimeout = 8 * time.Second

// ServerStateService 按需查询某实例的全量 Bukkit 内部状态（FR-076，见 ADR-016）。
//
// 经各实例的 ServerProbe 探针反向 WS 桥取回（复用 FR-065 的 QueryServerState 通道）：
// CP 把 instance 解析为 node，经 Worker 的 gRPC QueryServerState 同步取回探针采集的全状态 JSON。
// 轻指标（TPS/堆/在线 历史时序）仍走 /metrics（FR-060/061）；本服务仅服务前端「开 tab/手动刷新」的全量快照。
//
// state_json 是探针手拼的 JSON 字符串，CP **不解析**（json.RawMessage 透传给前端按结构渲染），
// 探针状态字段演进无需改 CP。探针未连入/采集超时一律优雅降级（connected/available=false），不向上抛错。
type ServerStateService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
}

// NewServerStateService 创建服务器状态查询服务。
func NewServerStateService(db *gorm.DB, pool *cpgrpc.ClientPool) *ServerStateService {
	return &ServerStateService{db: db, pool: pool}
}

// ServerStateResult 一次全状态查询的结果（透传给前端）。
type ServerStateResult struct {
	InstanceID uint `json:"instanceId"`
	// Connected 探针当前是否连入本机 Worker（false 时 State 为 null，前端提示部署/连接探针）。
	Connected bool `json:"connected"`
	// Available 本次是否成功取回状态（探针在线但采集超时/失败时为 false，前端提示重试）。
	Available bool `json:"available"`
	// State 探针采集的全量状态原始 JSON（server/worlds/jvm/classloader/scheduler/listeners），
	// CP 不解析、原样透传；不可得时为 null。
	State json.RawMessage `json:"state"`
	// Error 降级时的友好原因（节点未连/探针未连/采集超时），成功时为空。
	Error string `json:"error,omitempty"`
}

// QueryState 查询某实例的全量服务器状态。
//
// 解析 instance→node→gRPC client → QueryServerState；任何不可达环节（实例无节点/节点未连/探针未连/
// 采集超时/state_json 非法）一律降级为 connected/available=false + 友好提示，不返回 error（实例不存在除外）。
func (s *ServerStateService) QueryState(instanceID uint) (*ServerStateResult, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}

	result := &ServerStateResult{InstanceID: instanceID}

	var node model.Node
	if err := s.db.First(&node, inst.NodeID).Error; err != nil {
		result.Error = "节点不存在"
		return result, nil
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		result.Error = "节点未连接"
		return result, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), serverStateQueryTimeout)
	defer cancel()

	resp, err := client.Worker.QueryServerState(ctx, &workerpb.QueryServerStateRequest{InstanceUuid: inst.UUID})
	if err != nil {
		result.Error = "状态查询调用失败"
		return result, nil
	}
	mapServerStateResponse(result, resp)
	return result, nil
}

// mapServerStateResponse 把 Worker 的 QueryServerStateResponse 映射为透传结果（抽出便于单测）。
//
// 降级矩阵：
//   - success=false（本节点未启用插件桥）→ available=false + error。
//   - connected=false（探针未连入/刚断）→ available=false，State 为 null。
//   - connected=true 但 state_json 为空（采集超时）→ available=false + error（探针在、本次不可得）。
//   - connected=true 且 state_json 合法 JSON → available=true，State 透传。
//   - state_json 非合法 JSON（理论不该发生）→ available=false + error，避免给前端坏 JSON。
func mapServerStateResponse(result *ServerStateResult, resp *workerpb.QueryServerStateResponse) {
	if resp == nil {
		result.Error = "无状态响应"
		return
	}
	result.Connected = resp.Connected
	if !resp.Success {
		result.Error = fallbackStateMsg(resp.Error, "插件桥不可用")
		return
	}
	if !resp.Connected {
		result.Error = fallbackStateMsg(resp.Error, "探针未连入")
		return
	}
	if resp.StateJson == "" {
		result.Error = fallbackStateMsg(resp.Error, "状态采集超时")
		return
	}
	if !json.Valid([]byte(resp.StateJson)) {
		result.Error = "探针返回的状态数据无法解析"
		return
	}
	result.Available = true
	result.State = json.RawMessage(resp.StateJson)
}

// fallbackStateMsg 优先用 Worker 透传的具体 error，否则用兜底文案。
func fallbackStateMsg(detail, fallback string) string {
	if detail == "" {
		return fallback
	}
	return detail
}
