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

// WriteContext 业务高危写的操作者上下文与幂等键（FR-121，见 ADR-029）。
//
// CP 在写动作（manifest readOnly=false）下发前，把这些字段注入 payload JSON，
// 经探针桥透传给探针 Provider：
//   - TaskID 作业务幂等键（探针→mce BusinessOrder(taskId)，FR-120 缺失即拒绝）；对「同一逻辑操作的重试」必须稳定。
//   - Operator/OperatorID/NodeID/Reason 映射进插件审计流水（mce operator/reason），平台侧与插件侧审计可对账追责。
//
// 注入策略：仅当 payload 未显式带同名键时写入（不覆盖业务方入参）。
type WriteContext struct {
	// TaskID 业务幂等键。空时由 Dispatch 兜底生成 UUID（保证探针不因缺 taskId 拒绝，但失去重试去重保证）。
	TaskID string
	// Operator 操作者用户名（透传进 mce 流水 operator，追责到真人）。
	Operator string
	// OperatorID 操作者用户 ID（平台侧与插件侧审计对账）。
	OperatorID uint
	// NodeID 实例所属节点 UUID（「哪个节点」维度）。
	NodeID string
	// Reason 操作原因（可选，「为什么」，透传进 mce 流水 reason）。
	Reason string
}

// 业务写注入进 payload 的 JBIS 约定键名（与探针 Provider / 下游 FR-122/125 约定一致，见 ADR-029）。
const (
	payloadKeyTaskID     = "taskId"
	payloadKeyOperator   = "operator"
	payloadKeyOperatorID = "operatorId"
	payloadKeyNodeID     = "nodeId"
	payloadKeyReason     = "reason"
)

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
// 用于只读动作与元查询（manifest）：payload 原样下发，不注入操作者/幂等上下文。
// 高危写动作走 DispatchWrite（FR-121）。
//
// 解析 instance→node→gRPC client → SendPluginCommand（携 domain/action/payload_json）；任何不可达环节
// 一律降级为 available=false + 友好提示（不返回 error）。实例不存在 / 参数非法返回 error 供路由分流。
func (s *BusinessService) Dispatch(instanceID uint, domain, action, payloadJSON string) (*BusinessResult, error) {
	domain = strings.TrimSpace(domain)
	action = strings.TrimSpace(action)
	if domain == "" || action == "" {
		return nil, ErrInvalidBusinessCommand
	}
	inst, node, result, err := s.resolveTarget(instanceID, domain, action)
	if err != nil {
		return nil, err
	}
	if result.Error != "" {
		return result, nil
	}
	return s.sendToProbe(inst, node, result, payloadJSON), nil
}

// DispatchWrite 下发一条高危业务写动作（FR-121，见 ADR-029）。
//
// 与 Dispatch 同样解析 instance→node→client，但在下发前把操作者身份与幂等键注入 payload：
//   - 注入 taskId（幂等键，缺失则兜底 UUID）、operator/operatorId/nodeId/reason（操作者审计贯通）；
//   - 仅当 payload 未显式带同名键时写入，不覆盖业务方入参；
//   - nodeId 取实例所属节点 UUID（WriteContext.NodeID 为空时回填）。
//
// 降级矩阵与 Dispatch 一致；payload 非法 JSON 返回 ErrInvalidBusinessCommand（写不可在坏信封上裸跑）。
func (s *BusinessService) DispatchWrite(instanceID uint, domain, action, payloadJSON string, wc WriteContext) (*BusinessResult, error) {
	domain = strings.TrimSpace(domain)
	action = strings.TrimSpace(action)
	if domain == "" || action == "" {
		return nil, ErrInvalidBusinessCommand
	}
	inst, node, result, err := s.resolveTarget(instanceID, domain, action)
	if err != nil {
		return nil, err
	}
	if result.Error != "" {
		return result, nil
	}
	if wc.NodeID == "" {
		wc.NodeID = node.UUID
	}
	injected, injErr := injectWriteContext(payloadJSON, wc)
	if injErr != nil {
		return nil, ErrInvalidBusinessCommand
	}
	return s.sendToProbe(inst, node, result, injected), nil
}

// resolveTarget 解析实例与节点，构造初始结果。
//
// 三种返回：
//   - 硬错误（实例不存在 / 查询失败）→ (_, _, nil, err)，调用方以 error 返回供路由分流；
//   - 软降级（实例的节点记录不存在）→ (_, _, result(Error 已填), nil)，调用方直接回传 200+available=false；
//   - 正常 → (inst, node, result(Error 空), nil)，调用方继续下发。
func (s *BusinessService) resolveTarget(instanceID uint, domain, action string) (model.Instance, model.Node, *BusinessResult, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return inst, model.Node{}, nil, ErrInstanceNotFound
		}
		return inst, model.Node{}, nil, fmt.Errorf("查询实例失败: %w", err)
	}
	result := &BusinessResult{InstanceID: instanceID, Domain: domain, Action: action}
	var node model.Node
	if err := s.db.First(&node, inst.NodeID).Error; err != nil {
		result.Error = "节点不存在"
		return inst, node, result, nil
	}
	return inst, node, result, nil
}

// sendToProbe 执行 gRPC 下发并映射响应（payload 已是最终形态）。节点未连入降级为 available=false。
func (s *BusinessService) sendToProbe(inst model.Instance, node model.Node, result *BusinessResult, payloadJSON string) *BusinessResult {
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		result.Error = "节点未连接"
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessDispatchTimeout)
	defer cancel()

	resp, err := client.Worker.SendPluginCommand(ctx, &workerpb.SendPluginCommandRequest{
		InstanceUuid: inst.UUID,
		Command: &workerpb.PluginCommand{
			Domain:      result.Domain,
			Action:      result.Action,
			PayloadJson: payloadJSON,
			RequestId:   uuid.NewString(),
		},
		Wait: true,
	})
	if err != nil {
		result.Error = "业务命令调用失败"
		return result
	}
	mapBusinessResponse(result, resp)
	return result
}

// injectWriteContext 把操作者身份与幂等键注入业务 payload JSON（FR-121）。
//
// 规则：
//   - payload 为空 → 以 {} 起步；非法 JSON（非对象）→ 返回 error（写不可在坏信封上裸跑）。
//   - 仅当对应键不存在时写入（不覆盖业务方显式入参）。
//   - TaskID 为空时兜底生成 UUID（保证探针不因缺 taskId 拒绝；但失重试去重保证，前端写应始终带稳定 taskId）。
//   - Reason / Operator 为空则不写该键（避免污染流水为空字符串）；OperatorID 为 0 同理跳过。
func injectWriteContext(payloadJSON string, wc WriteContext) (string, error) {
	obj := map[string]any{}
	trimmed := strings.TrimSpace(payloadJSON)
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
			return "", fmt.Errorf("payload 非合法 JSON 对象: %w", err)
		}
	}

	taskID := wc.TaskID
	if taskID == "" {
		taskID = uuid.NewString()
	}
	putIfAbsent(obj, payloadKeyTaskID, taskID)
	if wc.Operator != "" {
		putIfAbsent(obj, payloadKeyOperator, wc.Operator)
	}
	if wc.OperatorID != 0 {
		putIfAbsent(obj, payloadKeyOperatorID, wc.OperatorID)
	}
	if wc.NodeID != "" {
		putIfAbsent(obj, payloadKeyNodeID, wc.NodeID)
	}
	if wc.Reason != "" {
		putIfAbsent(obj, payloadKeyReason, wc.Reason)
	}

	out, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("序列化注入后 payload 失败: %w", err)
	}
	return string(out), nil
}

// putIfAbsent 仅当 key 不存在时写入，保护业务方显式入参不被覆盖。
func putIfAbsent(m map[string]any, key string, val any) {
	if _, exists := m[key]; !exists {
		m[key] = val
	}
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
