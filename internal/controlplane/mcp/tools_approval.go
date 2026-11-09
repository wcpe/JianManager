package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 审批票据自查工具（spec §8.4 补齐）。
//
// 【为何需要】审批拦截层在需要审批时返回 `awaiting_approval` + `requestId`，
// 但**此前的 MCP 工具面没有审批查询工具**：Agent 提交后就只能盲等，
// 要么反复查目标状态猜「批了没」，要么完全放弃回读。而 FR-490 的原文要求
// 「使 Agent 提交后能轮询结果、闭环不断在盲等」。
//
// 【限域口径：按 TokenID 而非 applicant】一个用户可能持多把 Token（CI 一把、
// 本地调试一把）。按 applicant 限域会让 Token A 看到 Token B 的票（含冻结参数与
// 目标），比必要范围宽。所有查询都强制带当前 principal 的 TokenID，
// 拿不到 TokenID（助手委托链）就直接拒绝——那条链没有自己的票。
var approvalToolSpecs = []toolSpec{
	{
		Def: ToolDef{
			Name: "approval_list",
			Description: "列出本 Token 提交的审批票据（按 token 严格限域；可用 status 过滤，默认倒序）。" +
				"高危操作被拦截返回 awaiting_approval 后，用它查有没有人批了",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status": map[string]any{
						"type":        "string",
						"description": "按状态过滤：pending / approved / rejected / executing / succeeded / failed / expired / withdrawn",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "返回条数上限（默认 20，最大 100）",
					},
				},
				"required": []string{},
			},
		},
		Action: service.AgentActionApprovalList,
		Exec:   execApprovalList,
	},
	{
		Def: ToolDef{
			Name:        "approval_get",
			Description: "查询本 Token 提交的某张审批票据详情（含状态、失败原因、冻结参数摘要）",
			InputSchema: idSchema("票据 ID（拦截应答里的 ticketId）"),
		},
		Action: service.AgentActionApprovalGet,
		Exec:   execApprovalGet,
	},
	{
		Def: ToolDef{
			Name:        "approval_withdraw",
			Description: "撤回本 Token 提交的待审票据（仅 pending 可撤；已决策/已执行/已过期不可撤）",
			InputSchema: idSchema("票据 ID"),
		},
		Action: service.AgentActionApprovalWithdraw,
		Exec:   execApprovalWithdraw,
	},
}

// approvalTokenScope 取当前 principal 的 Token 限域键。
//
// 返回 false 表示「这个调用方没有 Token」（助手委托链），直接拒绝：
// 那条链提交的票 applicant_kind=agent 但 token_id 为空，按空串查会把所有人的
// 无 Token 票混在一起，比不查更危险。
func approvalTokenScope(p *service.AgentPrincipal) (string, bool) {
	if p == nil || p.TokenID == 0 {
		return "", false
	}
	return fmt.Sprintf("%d", p.TokenID), true
}

func execApprovalList(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, _ string, args map[string]any) ToolResult {
	if deps.Approval == nil {
		return toolErr("审批服务不可用")
	}
	tokenID, ok := approvalTokenScope(p)
	if !ok {
		return toolErr("审批自查仅支持 Agent Token 调用：当前调用身份没有 Token，无法确定票据归属")
	}

	status, err := optionalString(args, "status")
	if err != nil {
		return toolErr(err.Error())
	}
	limit := 20
	if v, ok := args["limit"]; ok {
		n, e := toUint(v)
		if e != nil {
			return toolErr("参数 limit 无效: " + e.Error())
		}
		if n > 0 {
			limit = int(n)
		}
	}

	tickets, err := deps.Approval.ListApprovals(service.ApprovalListFilter{
		TokenID: &tokenID,
		Status:  status,
		Limit:   limit,
	})
	if err != nil {
		return toolErr("查询审批票据失败: " + err.Error())
	}
	// 只回必要字段：冻结参数里可能有凭据类值，列表场景不需要它。
	out := make([]map[string]any, 0, len(tickets))
	for i := range tickets {
		out = append(out, summarizeApproval(&tickets[i], false))
	}
	return toolOK(map[string]any{"items": out, "count": len(out)})
}

func execApprovalGet(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, _ string, args map[string]any) ToolResult {
	if deps.Approval == nil {
		return toolErr("审批服务不可用")
	}
	tokenID, ok := approvalTokenScope(p)
	if !ok {
		return toolErr("审批自查仅支持 Agent Token 调用：当前调用身份没有 Token，无法确定票据归属")
	}
	id, err := requireID(args)
	if err != nil {
		return toolErr(err.Error())
	}
	t, err := deps.Approval.GetApproval(id, nil)
	if err != nil {
		return toolErr(err.Error())
	}
	// 限域判定放在取出之后：不区分「不存在」与「不是你的」，不给探测接口。
	if t.TokenID != tokenID {
		return toolErr("审批票据不存在")
	}
	return toolOK(summarizeApproval(t, true))
}

func execApprovalWithdraw(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, _ string, args map[string]any) ToolResult {
	if deps.Approval == nil {
		return toolErr("审批服务不可用")
	}
	tokenID, ok := approvalTokenScope(p)
	if !ok {
		return toolErr("审批自查仅支持 Agent Token 调用：当前调用身份没有 Token，无法确定票据归属")
	}
	id, err := requireID(args)
	if err != nil {
		return toolErr(err.Error())
	}
	// 先验归属再撒：Withdraw 按 applicantID 判归属，而这里手里只有 TokenID。
	// 先把票取出来确认 token_id 对得上，再用票里的 applicant 去撒——
	// 两步之间票据状态可能变，但 Withdraw 自身是 CAS（仅 pending），不会误撒。
	t, err := deps.Approval.GetApproval(id, nil)
	if err != nil {
		return toolErr(err.Error())
	}
	if t.TokenID != tokenID {
		return toolErr("审批票据不存在")
	}
	withdrawn, err := deps.Approval.Withdraw(id, t.ApplicantUserID)
	if err != nil {
		return toolErr("撤回失败: " + err.Error())
	}
	return toolOK(summarizeApproval(withdrawn, true))
}

// summarizeApproval 把票据压成对 Agent 可读的形状。
//
// includePayload：详情才回冻结参数（Agent 需要核对自己当时提交了什么）；
// 列表不回，避免一次拉一堆参数体。
func summarizeApproval(t *model.ApprovalTicket, includePayload bool) map[string]any {
	out := map[string]any{
		"id":            t.ID,
		"requestId":     t.UUID,
		"action":        t.Action,
		"targetType":    t.TargetType,
		"targetId":      t.TargetID,
		"status":        t.Status,
		"reason":        t.Reason,
		"createdAt":     t.CreatedAt,
		"expiresAt":     t.ExpiresAt,
		"applicantKind": t.ApplicantKind,
	}
	if t.FailureReason != "" {
		out["failureReason"] = t.FailureReason
	}
	if t.DecidedAt != nil {
		out["decidedAt"] = *t.DecidedAt
	}
	if includePayload && t.PayloadFrozen != "" {
		out["payload"] = json.RawMessage(t.PayloadFrozen)
	}
	return out
}

// optionalString 取可选字符串参数；类型不对时显式报错（MCP 是契约面，不静默忽略）。
func optionalString(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("参数 %s 须为字符串", key)
	}
	return s, nil
}
