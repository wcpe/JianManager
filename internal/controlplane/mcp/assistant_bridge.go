package mcp

import (
	"context"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 渐进式工具披露的 MCP 侧接线。
//
// ## 为什么放在 mcp 包
//
// 工具目录的**单一真源是 mcp 包的 `allToolSpecs`**。助手不再维护一份平行定义，
// 而是由这里把目录与执行入口注入给 service——这样「MCP 新增一个工具，
// 助手立刻能用」，不需要在两处各改一遍（历史上正是两套定义导致了
// 「助手只有 13 个工具、MCP 有 108 个」的偏差）。
//
// 反向 import 会成环（mcp 已 import service），因此走既有注入模式
// （与 SetApprovalToolActionResolver 同一手法）。

func init() {
	service.SetAssistantToolCatalog(assistantToolCatalogForService)
	service.SetAssistantToolDetail(assistantToolDetailForService)
	service.SetAssistantToolExecutor(assistantToolExecutorForService)
	service.SetAssistantToolMeta(assistantToolMetaForService)
}

// assistantToolCatalogForService 枚举全部可披露的工具（不含参数 schema）。
//
// 不按调用者裁剪的原因：这张表是**给模型看的候选清单**，此时还没有目标资源，
// 无法判定 scope。真正的权限判定在执行期（CallTool 内的 Authorize），
// 早裁反而会让模型以为「平台没有这个能力」。
func assistantToolCatalogForService() []service.AssistantToolDescriptor {
	out := make([]service.AssistantToolDescriptor, 0, len(allToolSpecs))
	for _, spec := range allToolSpecs {
		out = append(out, service.AssistantToolDescriptor{
			Name:      spec.Def.Name,
			Summary:   spec.Def.Description,
			Operation: operationOfAction(spec.Action),
			// 写类必须确认（与的不变量一致）；只读自动执行。
			RequiresConfirm: operationOfAction(spec.Action) != service.AgentOperationRead,
			Category:        service.AssistantToolCategoryOf(spec.Def.Name),
		})
	}
	return out
}

// assistantToolDetailForService 取单个工具的完整定义（含原始 schema）。
func assistantToolDetailForService(name string) (service.AssistantToolDetail, bool) {
	for _, spec := range allToolSpecs {
		if spec.Def.Name != name {
			continue
		}
		op := operationOfAction(spec.Action)
		return service.AssistantToolDetail{
			AssistantToolDescriptor: service.AssistantToolDescriptor{
				Name:            spec.Def.Name,
				Summary:         spec.Def.Description,
				Operation:       op,
				RequiresConfirm: op != service.AgentOperationRead,
				Category:        service.AssistantToolCategoryOf(spec.Def.Name),
			},
			Parameters: spec.Def.InputSchema,
		}, true
	}
	return service.AssistantToolDetail{}, false
}

// AssistantToolExecutorBinder 由装配层调用，把「按 owner 执行工具」的实现注入 mcp 包。
//
// 为什么需要它：执行一次 MCP 工具需要 `ToolDeps`（全套领域服务）与该用户的
// authz——这两样只有装配层（main.go）拿得到。mcp 包不自己造 ToolDeps，
// 否则会在这里再造一份装配，正是本 FR 要消除的「重复造轮子」。
var assistantExecutorResolver func() func(ctx context.Context, owner uint, name string, args map[string]any) (string, error)

// SetAssistantExecutorResolver 由装配层注入执行器解析器。
func SetAssistantExecutorResolver(fn func() func(ctx context.Context, owner uint, name string, args map[string]any) (string, error)) {
	assistantExecutorResolver = fn
}

// assistantToolExecutorForService 进程内执行一次 MCP 工具调用。
//
// 走 `CallTool` 而不是另写一条执行路径：审批拦截、能力判定、scope 校验、
// 精确确认（confirmXxx）、调用流水全部与 MCP 入口一致——**不重复造轮子**，
// 也不会出现「助手能绕过 MCP 的闸」这种偏差。
//
// principal 由调用者（助手运行器）按 owner 构造：助手没有 Agent Token，
// 需要一把「代表人」的等效身份。这里用一条最小 V2 principal：
// 能力按用户权限在服务层重验，因此不在这里硬编码放行。
func assistantToolExecutorForService(ctx context.Context, owner uint, name string, args map[string]any) (string, error) {
	if assistantExecutorResolver == nil {
		return "", &assistantToolCallError{Text: "工具执行未装配"}
	}
	inner := assistantExecutorResolver()
	if inner == nil {
		return "", &assistantToolCallError{Text: "工具执行未装配"}
	}
	return inner(ctx, owner, name, args)
}

// CallToolAsOwner 由装配层注入的实现调用：按用户派生身份后走 CallTool。
//
// 单独导出是为了让「派生身份 + 走 MCP 执行」这条关键路径只有一处实现，
// 且能被装配层用真实的 ToolDeps 驱动。
func CallToolAsOwner(authz *service.AuthzService, deps ToolDeps, ctx context.Context, owner uint, name string, args map[string]any) (string, error) {
	p, err := service.AssistantPrincipalForOwner(authz, owner)
	if err != nil {
		return "", &assistantToolCallError{Text: "无法派生调用身份: " + err.Error()}
	}
	if p == nil {
		return "", &assistantToolCallError{Text: "当前用户没有任何平台工具权限"}
	}
	res := CallTool(ctx, deps, p, name, args)
	text := toolResultText(res)
	if res.IsError {
		return "", &assistantToolCallError{Text: text}
	}
	return text, nil
}

// toolResultText 取工具结果的第一段文本（生产代码里没有同名 helper：
// 测试包里的 firstText 不可用，且不应为了复用一行代码而导入测试符号）。
func toolResultText(res ToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

// assistantToolCallError 把工具失败转成 error，让上层按失败语义处理（不吞错）。
type assistantToolCallError struct{ Text string }

func (e *assistantToolCallError) Error() string { return e.Text }

// operationOfAction 从 action 目录取操作类型；未知按 write 保守处理。
//
// 为什么未知按 write：`unknown` 会走逐次确认，代价是多点一次；
// 而误判为 read 会让一条写操作自动执行——后果不可逆。
func operationOfAction(action string) string {
	if d, ok := service.DescribeAgentAction(action); ok {
		switch d.Operation {
		case service.AgentOperationRead:
			return service.AgentOperationRead
		case service.AgentOperationWrite:
			return service.AgentOperationWrite
		case service.AgentOperationDestructive:
			return service.AgentOperationDestructive
		}
	}
	return service.AgentOperationWrite
}

// assistantToolMetaForService 由 MCP 目录派生工具的执行元数据。
//
// ## 这是「单一真源」的落点
//
// 助手侧曾经自己维护一份 13 个工具的注册表（`assistantToolRegistry`），
// 与 MCP 的 108 个工具并存——两套定义迟早会漂移（事实上已经漂了：
// 助手只有 13 个，且无法覆盖外部工具）。现在确认票据与权限重验所需的
// metadata 全部从 MCP 目录派生，助手侧不再有任何工具定义。
func assistantToolMetaForService(name string) (*service.AssistantToolAction, bool) {
	for _, spec := range allToolSpecs {
		if spec.Def.Name != name {
			continue
		}
		d, ok := service.DescribeAgentAction(spec.Action)
		if !ok {
			return nil, false
		}
		op := operationOfAction(spec.Action)
		return &service.AssistantToolAction{
			Name:            spec.Def.Name,
			Action:          spec.Action,
			Capability:      d.V2Capability,
			ResourceType:    d.ResourceType,
			Operation:       op,
			RequiresConfirm: op != service.AgentOperationRead,
			Description:     spec.Def.Description,
		}, true
	}
	return nil, false
}
