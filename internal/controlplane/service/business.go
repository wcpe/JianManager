package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ErrInvalidBusinessCommand 业务命令缺 domain/action。
var ErrInvalidBusinessCommand = errors.New("业务命令缺少 domain 或 action")

// businessDispatchTimeout 是 CP→Worker 的业务命令 gRPC 调用超时。
// 须大于 Worker 侧等探针回执的 5s（pluginCommandTimeout），留网络与排队余量（与 serverStateQueryTimeout 同量级）。
const businessDispatchTimeout = 8 * time.Second

// JBIS 元查询保留域/动作（与探针侧 BridgeClient 约定一致）：
// 取业务能力清单而非派发到具体业务 Provider，供前端动态发现各域能力（ADR-026/027）。
const (
	businessMetaDomain = "jbis"
	manifestAction     = "manifest"
)

// BusinessService 是 JBIS 业务对接的 CP 编排脊柱（FR-116，见 ADR-026/027）。
//
// 它把前端发起的业务动作（`domain.action` + 结构化 payload）经既有插件桥（ADR-016）下发到
// 目标实例的 ServerProbe 业务对接层（BusinessHost），由探针侧 per-plugin Provider 执行并回执。
// CP **插件无关**：只认 domain/action/payload 信封，不理解具体业务语义；业务结果 JSON 原样透传给前端。
//
// 降级即默认（ADR-026）：实例无节点/节点未连/探针未连/域不可用/执行失败，一律降级为
// available=false + 友好提示，绝不 5xx、绝不拖垮调用方（实例不存在与参数非法除外，返回 error 供路由分流）。
type BusinessService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
}

// NewBusinessService 创建业务对接编排服务。
func NewBusinessService(db *gorm.DB, pool *cpgrpc.ClientPool) *BusinessService {
	return &BusinessService{db: db, pool: pool}
}

// BusinessResult 一次业务动作的结果（透传给前端）。
type BusinessResult struct {
	InstanceID uint   `json:"instanceId"`
	Domain     string `json:"domain"`
	Action     string `json:"action"`
	// Available 本次动作是否成功执行（探针在线 + Provider 执行成功）。失败时 Output 为 null、Error 给原因。
	Available bool `json:"available"`
	// Output 业务结果原始 JSON（探针 Provider 产出，CP 不解析、原样透传）；不可得时为 null。
	Output json.RawMessage `json:"output"`
	// Error 降级/失败时的友好原因（节点未连/探针未连/域不可用/执行失败），成功时为空。
	Error string `json:"error,omitempty"`
}

// Dispatch 向某实例下发一条业务命令并取回结果（同步往返，wait=true）。
//
// 解析 instance→node→gRPC client → SendPluginCommand（携 domain/action/payload_json）；任何不可达环节
// 一律降级为 available=false + 友好提示（不返回 error）。实例不存在 / 参数非法返回 error 供路由分流。
func (s *BusinessService) Dispatch(instanceID uint, domain, action, payloadJSON string) (*BusinessResult, error) {
	domain = strings.TrimSpace(domain)
	action = strings.TrimSpace(action)
	if domain == "" || action == "" {
		return nil, ErrInvalidBusinessCommand
	}

	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}

	result := &BusinessResult{InstanceID: instanceID, Domain: domain, Action: action}

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

	ctx, cancel := context.WithTimeout(context.Background(), businessDispatchTimeout)
	defer cancel()

	resp, err := client.Worker.SendPluginCommand(ctx, &workerpb.SendPluginCommandRequest{
		InstanceUuid: inst.UUID,
		Command: &workerpb.PluginCommand{
			Domain:      domain,
			Action:      action,
			PayloadJson: payloadJSON,
			RequestId:   uuid.NewString(),
		},
		Wait: true,
	})
	if err != nil {
		result.Error = "业务命令调用失败"
		return result, nil
	}
	mapBusinessResponse(result, resp)
	return result, nil
}

// Manifest 取某实例的业务能力清单（JBIS 元查询）。
//
// 复用 Dispatch 下发保留元命令（domain=jbis、action=manifest），探针侧返回各业务 Provider 汇总的
// 能力清单 JSON（{"domains":{...}}）于 output；供前端动态发现各域能力、动态渲染（不硬编码具体插件）。
// 探针未连/无业务 Provider 时同样优雅降级（available=false + 提示）。
func (s *BusinessService) Manifest(instanceID uint) (*BusinessResult, error) {
	return s.Dispatch(instanceID, businessMetaDomain, manifestAction, "")
}

// mapBusinessResponse 把 Worker 的 SendPluginCommandResponse 映射为透传结果（抽出便于单测）。
//
// 降级矩阵：
//   - nil 响应 → available=false + error。
//   - success=false（探针未连/域不可用/Provider 执行失败，由探针/Worker 透传具体原因）→ available=false + error。
//   - success=true 且 output 为空（即发即忘类动作）→ available=true、Output 为 null。
//   - success=true 且 output 合法 JSON → available=true，Output 透传。
//   - success=true 但 output 非合法 JSON（理论不该发生）→ available=false + error，避免给前端坏 JSON。
func mapBusinessResponse(result *BusinessResult, resp *workerpb.SendPluginCommandResponse) {
	if resp == nil {
		result.Error = "无业务响应"
		return
	}
	if !resp.Success {
		result.Error = fallbackBusinessMsg(resp.Error, "业务动作执行失败")
		return
	}
	if resp.Output == "" {
		result.Available = true
		return
	}
	if !json.Valid([]byte(resp.Output)) {
		result.Error = "业务返回数据无法解析"
		return
	}
	result.Available = true
	result.Output = json.RawMessage(resp.Output)
}

// fallbackBusinessMsg 优先用 Worker/探针透传的具体 error，否则用兜底文案。
func fallbackBusinessMsg(detail, fallback string) string {
	if detail == "" {
		return fallback
	}
	return detail
}
