package service

import (
	"fmt"
	"strings"
)

// 高危操作的审批拦截。
//
// 唯一入口：`InterceptForApproval`。MCP 的 CallTool 在分发前调用它，
// 返回 `Blocked=true` 时**必须直接返回**，不得继续执行——这是「零副作用」的落点。
//
// 为什么把判定抽成独立函数而不是写在 CallTool 里：
//   - 可以单测（不必起整个 MCP 栈与依赖）；
//   - 「哪些 action 需审」这件事只有一个判定点，不会出现两处各判一套。

// ApprovalInterceptResult 拦截结果。
type ApprovalInterceptResult struct {
	// Blocked true 表示已拦截（已落票据或已拒绝），调用方不得继续执行。
	Blocked bool
	// TicketID 已落票据的 id（仅需审时非零）。
	TicketID uint
	// RequestID 给 Agent 的受理标识（申请人可在审批中心据它核对状态）。
	RequestID string
	// Message 给 Agent 的中文说明。
	Message string
	// Err 需要以错误形式返回时（未登记/参数非法等）。
	Err error
}

// ActionOperationOf 取某个 MCP 工具名对应 action 的操作类型。
//
// 回退规则：工具名不在 catalog 里时按 **write** 处理（保守）。
// 理由：catalog 之外的工具若真有副作用，按读处理会直接放行；
// 按写处理则会被失败关闭挡下——两种误判里，只有前者是安全事故。
func ActionOperationOf(toolName string) string {
	action, ok := toolActionByNameForApproval(toolName)
	if !ok {
		return AgentOperationWrite
	}
	d, ok := DescribeAgentAction(action)
	if !ok {
		return AgentOperationWrite
	}
	if strings.TrimSpace(d.Operation) == "" {
		return AgentOperationWrite
	}
	return d.Operation
}

// toolActionByNameForApproval 由 mcp 包注入的工具名→action 映射。
//
// 用注入而不是直接 import mcp 包：`mcp` 包已经 import 了 `service`，
// 反向 import 会形成环。注入也顺带说明了「谁是工具目录的真源」。
var approvalToolActionResolver func(toolName string) (string, bool)

// SetApprovalToolActionResolver 注入工具名→action 解析器（装配期调用一次）。
func SetApprovalToolActionResolver(fn func(toolName string) (string, bool)) {
	approvalToolActionResolver = fn
}

func toolActionByNameForApproval(toolName string) (string, bool) {
	if approvalToolActionResolver == nil {
		// 未注入时无法判定 → 当作 catalog 外，按 write 保守处理（见 ActionOperationOf）。
		return "", false
	}
	return approvalToolActionResolver(toolName)
}

// ApprovalInterceptParams 拦截入参。
type ApprovalInterceptParams struct {
	// ToolName MCP 工具名（如 instance_stop）。
	ToolName string
	// Action catalog action 名（由调用方解析后给出，避免重复解析）。
	Action string
	// Operation read/write/destructive。
	Operation string
	// TargetType / TargetID 已由服务端解析的可信目标。
	TargetType string
	TargetID   string
	// Payload 规范化后的参数（用于冻结与哈希）。
	Payload map[string]any
	// Evidence 申请时的资源快照。
	Evidence map[string]any
	// Reason 审批理由。Agent 未提供时由系统生成一句可读说明
	// （强制它填理由会让模型随便编一句；系统生成至少是准确的）。
	Reason string
	// ApplicantUserID / ApplicantKind / TokenID 申请人信息。
	ApplicantUserID uint
	ApplicantKind   string
	TokenID         string
}

// InterceptForApproval 判定并（必要时）落票据。
//
// 返回值语义：
//   - `Blocked=false`：无需审批，调用方继续执行；
//   - `Blocked=true, Err==nil`：已落票据，调用方返回 requestId 给 Agent，**不执行**；
//   - `Blocked=true, Err!=nil`：拒绝（未登记/参数非法），调用方返回错误。
func (s *ApprovalService) InterceptForApproval(p ApprovalInterceptParams) ApprovalInterceptResult {
	isWrite := p.Operation == AgentOperationWrite || p.Operation == AgentOperationDestructive
	req, err := ClassifyApprovalRequirement(p.ToolName, isWrite)
	if err != nil {
		// 失败关闭：未登记的写操作一律拒绝，且**不执行**。
		return ApprovalInterceptResult{Blocked: true, Err: err, Message: err.Error()}
	}
	if !req.Required {
		return ApprovalInterceptResult{Blocked: false}
	}

	reason := strings.TrimSpace(p.Reason)
	if reason == "" {
		reason = fmt.Sprintf("Agent 申请执行 %s：%s", p.ToolName, req.Reason)
	}
	ticket, _, err := s.SubmitApproval(SubmitApprovalParams{
		ApplicantUserID: p.ApplicantUserID,
		ApplicantKind:   p.ApplicantKind,
		TokenID:         p.TokenID,
		Action:          p.ToolName,
		TargetType:      p.TargetType,
		TargetID:        p.TargetID,
		Payload:         p.Payload,
		Evidence:        p.Evidence,
		Reason:          reason,
	})
	if err != nil {
		return ApprovalInterceptResult{Blocked: true, Err: err, Message: err.Error()}
	}
	return ApprovalInterceptResult{
		Blocked:   true,
		TicketID:  ticket.ID,
		RequestID: ticket.UUID,
		// 文案只承诺真实存在的能力。
		//
		// 此前这里写「请用 own.get 轮询」，但 MCP 工具面并没有审批查询工具，
		// Agent 面也没有对应端点——那句话会让调用方去调一个不存在的东西，
		// 比不提示更糟。现在如实说明：票已落库、等人工决定、批准后由服务端执行，
		// 调用方稍后重新查询目标状态即可看到结果。
		Message: fmt.Sprintf(
			"该操作需要人工审批，已提交申请（requestId=%s）。本次调用**未执行**任何操作；"+
				"批准后由服务端执行，请稍后重新查询目标状态确认结果。", ticket.UUID),
	}
}
