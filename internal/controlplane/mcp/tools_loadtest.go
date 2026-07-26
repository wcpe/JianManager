package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-398：压测模板 CRUD 与运行编排工具。

const (
	// agentTemplateOwnerID / agentTemplateIsAdmin：Agent 以平台级视角操作模板。
	// Token 授权边界已由 capability 表达，再叠加用户所有权会产生「Agent 创建的模板归谁」的悖论。
	// 决策见 ADR-080 附注：持 bot.load 即可管理全部压测模板；误删风险由精确确认参数兜底。
	agentTemplateOwnerID = uint(0)
	agentTemplateIsAdmin = true
)

func errInvalidUintSlice(key string) error {
	return fmt.Errorf("参数 %s 必须是正整数数组", key)
}

// loadTestToolSpecs 声明模板与运行编排工具协议。
func loadTestToolSpecs() []toolSpec {
	specs := loadTestTemplateToolSpecs()
	return append(specs, loadTestRunToolSpecs()...)
}

func loadTestTemplateToolSpecs() []toolSpec {
	templateBody := map[string]any{
		"name":            stringProp("模板名称"),
		"description":     stringProp("可选：模板描述"),
		"commandSchedule": objectProp("命令计划 JSON 对象：{commands:[{id,atMs,command}],durationMs,jitterMs}"),
		"loadProfile":     objectProp("负载曲线 JSON 对象：{type,targetBots,rampUpSeconds,durationSeconds}"),
		"thresholds":      objectProp("阈值 JSON 对象：minOnlineRate/minCommandSentRate 等"),
		"tags":            map[string]any{"type": "array", "description": "可选：标签数组", "items": map[string]any{"type": "string"}},
	}
	updateBody := runIDSchemaWithout(templateBody, "模板 ID")
	return []toolSpec{
		{
			Def: ToolDef{
				Name:        "loadtest_template_list",
				Description: "列出压测模板（Agent 为平台级视角，可见全部模板）；支持 page/pageSize/q/tag",
				InputSchema: objectSchema(mergeProps(pagingProps(), map[string]any{
					"q":   stringProp("可选：按名称或描述模糊搜索"),
					"tag": stringProp("可选：按标签过滤"),
				}), nil),
			},
			Action: service.AgentActionLoadTestTemplateList,
		},
		{
			Def: ToolDef{
				Name:        "loadtest_template_get",
				Description: "获取压测模板详情",
				InputSchema: idSchema("模板 ID"),
			},
			Action: service.AgentActionLoadTestTemplateGet,
		},
		{
			Def: ToolDef{
				Name:        "loadtest_template_create",
				Description: "创建压测模板（须具备 bot.load 能力）；命令计划/负载曲线/阈值均为结构化 JSON 对象，不接受 YAML 文本",
				InputSchema: objectSchema(templateBody, []string{"name", "commandSchedule", "loadProfile", "thresholds"}),
			},
			Action: service.AgentActionLoadTestTemplateCreate,
		},
		{
			Def: ToolDef{
				Name:        "loadtest_template_update",
				Description: "全量替换压测模板可编辑字段（须具备 bot.load 能力）",
				InputSchema: updateBody,
			},
			Action: service.AgentActionLoadTestTemplateUpdate,
		},
		{
			Def: ToolDef{
				Name:        "loadtest_template_delete",
				Description: "删除压测模板（危险操作）。必须传 confirmTemplateName，其值需精确等于模板名称，否则拒绝执行",
				InputSchema: objectSchema(map[string]any{
					"id":                  numberProp("模板 ID"),
					"confirmTemplateName": stringProp("精确确认：必须等于目标模板的名称"),
				}, []string{"id", "confirmTemplateName"}),
			},
			Action: service.AgentActionLoadTestTemplateDelete,
		},
	}
}

func loadTestRunToolSpecs() []toolSpec {
	return []toolSpec{
		{
			Def: ToolDef{
				Name:        "loadtest_run_create",
				Description: "创建压测运行：传 templateId 则从模板快照创建，否则按 count/behavior 直接创建（须具备 bot.load 能力）",
				InputSchema: objectSchema(map[string]any{
					"instanceId": numberProp("目标实例 ID"),
					"templateId": numberProp("可选：来源模板 ID；提供时从模板深拷贝快照创建"),
					"name":       stringProp("运行名称（模板路径必填）"),
					"namePrefix": stringProp("Bot 名称前缀"),
					"count":      numberProp("Bot 数量（非模板路径必填）"),
					"behavior":   stringProp("可选：行为模式（非模板路径）"),
					"config":     objectProp("连接配置 JSON 对象：{server,port,auth:\"offline\",version?}"),
				}, []string{"instanceId", "namePrefix"}),
			},
			Action: service.AgentActionLoadTestRunCreate,
		},
		{
			Def: ToolDef{
				Name:        "loadtest_run_list",
				Description: "列出 scope 内实例的压测运行；支持 page/pageSize",
				InputSchema: objectSchema(pagingProps(), nil),
			},
			Action: service.AgentActionLoadTestRunList,
		},
		{
			Def: ToolDef{
				Name:        "loadtest_run_get",
				Description: "获取压测运行详情（含分配计划与批次摘要）",
				InputSchema: runIDSchema(nil),
			},
			Action: service.AgentActionLoadTestRunGet,
		},
		{
			Def: ToolDef{
				Name:        "loadtest_node_capacity",
				Description: "查询 scope 内发压节点容量目录（可用/上限），用于规划 executorNodeIds",
				InputSchema: objectSchema(map[string]any{}, nil),
			},
			Action: service.AgentActionLoadTestNodeCapacity,
		},
		{
			Def: ToolDef{
				Name: "loadtest_run_preflight",
				Description: "容量预检并生成计划令牌。返回的 planToken 不透明、60 秒内有效，" +
					"只能整串原样回传给 loadtest_run_start，不要解析或改写；容量不足时返回 blockers 且不创建任何 Bot",
				InputSchema: runIDSchema(map[string]any{
					"executorNodeIds":             map[string]any{"type": "array", "description": "可选：指定发压节点 ID 集合；任一不在 scope 内则整体拒绝", "items": map[string]any{"type": "number"}},
					"connectRatePerSecondPerNode": numberProp("可选：每节点每秒连接速率，1..50"),
				}),
			},
			Action: service.AgentActionLoadTestRunPreflight,
		},
		{
			Def: ToolDef{
				Name: "loadtest_run_start",
				Description: "启动压测运行。planToken 必须是 loadtest_run_preflight 原样返回的字符串；" +
					"过期或容量世代变化会被拒绝，此时请重新预检。重复调用幂等，不产生额外副作用",
				InputSchema: runIDSchema(map[string]any{
					"planToken": stringProp("预检返回的不透明计划令牌，整串回传"),
				}),
			},
			Action: service.AgentActionLoadTestRunStart,
		},
		{
			Def: ToolDef{
				Name: "loadtest_run_stop",
				Description: "停止压测运行（安全方向）。若运行涉及的部分发压节点已不在 scope 内，仍会执行停止，" +
					"但会在返回的 outOfScopeExecutorNodeIds 中列出这些节点。重复调用幂等",
				InputSchema: runIDSchema(map[string]any{
					"reason": stringProp("可选：停止原因，最长 255 字符"),
				}),
			},
			Action: service.AgentActionLoadTestRunStop,
		},
		{
			Def: ToolDef{
				Name: "loadtest_run_retry_failed",
				Description: "只重试失败 Bot 子集。requestId 必须是 UUID 且按批固定：" +
					"重试同一批失败集合必须复用同一 requestId，否则会被视为新请求",
				InputSchema: runIDSchema(map[string]any{
					"requestId":  stringProp("UUID 幂等键；同一批重试须复用"),
					"botUuids":   map[string]any{"type": "array", "description": "可选：限定重试的 Bot UUID 列表", "items": map[string]any{"type": "string"}},
					"errorCodes": map[string]any{"type": "array", "description": "可选：按错误码筛选重试目标", "items": map[string]any{"type": "string"}},
					"fromStepId": stringProp("可选：从指定步骤恢复"),
				}),
			},
			Action: service.AgentActionLoadTestRunRetryFailed,
		},
	}
}

// runIDSchemaWithout 构造带 id 必填的模板更新 schema。
func runIDSchemaWithout(body map[string]any, idDesc string) map[string]any {
	properties := map[string]any{"id": numberProp(idDesc)}
	for key, value := range body {
		properties[key] = value
	}
	return objectSchema(properties, []string{"id", "name", "commandSchedule", "loadProfile", "thresholds"})
}

func mergeProps(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

// loadTestToolExecutors 绑定模板与运行编排执行器。
func loadTestToolExecutors() map[string]botToolExec {
	return map[string]botToolExec{
		"loadtest_template_list":    execTemplateList,
		"loadtest_template_get":     execTemplateGet,
		"loadtest_template_create":  execTemplateCreate,
		"loadtest_template_update":  execTemplateUpdate,
		"loadtest_template_delete":  execTemplateDelete,
		"loadtest_run_create":       execRunCreate,
		"loadtest_run_list":         execRunList,
		"loadtest_run_get":          execRunGet,
		"loadtest_node_capacity":    execNodeCapacity,
		"loadtest_run_preflight":    execRunPreflight,
		"loadtest_run_start":        execRunStart,
		"loadtest_run_stop":         execRunStop,
		"loadtest_run_retry_failed": execRunRetryFailed,
	}
}

func execTemplateList(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if deps.LoadTemplate == nil {
		return toolErr("压测模板服务不可用")
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	page, pageSize := projectionPageArgs(args)
	res, err := deps.LoadTemplate.List(agentTemplateOwnerID, agentTemplateIsAdmin, service.BotLoadTemplateListQuery{
		Page: page, PageSize: pageSize, Q: stringArg(args, "q"), Tag: stringArg(args, "tag"),
	})
	if err != nil {
		return toolErr("查询压测模板失败: " + err.Error())
	}
	return toolOK(res)
}

func execTemplateGet(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, result, ok := authorizeTemplateAction(deps, p, action, args)
	if !ok {
		return result
	}
	view, err := deps.LoadTemplate.Get(id, agentTemplateOwnerID, agentTemplateIsAdmin)
	if err != nil {
		return toolErr("查询压测模板失败: " + err.Error())
	}
	return toolOK(view)
}

func execTemplateCreate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if deps.LoadTemplate == nil {
		return toolErr("压测模板服务不可用")
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	input, err := templateInputFromArgs(args)
	if err != nil {
		return toolErr(err.Error())
	}
	view, err := deps.LoadTemplate.Create(agentTemplateOwnerID, input)
	if err != nil {
		return toolErr("创建压测模板失败: " + err.Error())
	}
	return toolOK(view)
}

func execTemplateUpdate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, result, ok := authorizeTemplateAction(deps, p, action, args)
	if !ok {
		return result
	}
	input, err := templateInputFromArgs(args)
	if err != nil {
		return toolErr(err.Error())
	}
	view, err := deps.LoadTemplate.Update(id, agentTemplateOwnerID, agentTemplateIsAdmin, input)
	if err != nil {
		return toolErr("更新压测模板失败: " + err.Error())
	}
	return toolOK(view)
}

func execTemplateDelete(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, result, ok := authorizeTemplateAction(deps, p, action, args)
	if !ok {
		return result
	}
	existing, err := deps.LoadTemplate.Get(id, agentTemplateOwnerID, agentTemplateIsAdmin)
	if err != nil {
		return toolErr("查询压测模板失败: " + err.Error())
	}
	if err := requireExactConfirm("confirmTemplateName", existing.Name, stringArg(args, "confirmTemplateName")); err != nil {
		return toolErr(err.Error())
	}
	if err := deps.LoadTemplate.Delete(id, agentTemplateOwnerID, agentTemplateIsAdmin); err != nil {
		return toolErr("删除压测模板失败: " + err.Error())
	}
	return toolOK(map[string]any{"ok": true, "deletedTemplateId": id})
}

// authorizeTemplateAction 模板无实例归属，仅做 capability 校验后解析 id。
func authorizeTemplateAction(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) (uint, ToolResult, bool) {
	if deps.LoadTemplate == nil {
		return 0, toolErr("压测模板服务不可用"), false
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return 0, toolForbidden(err), false
	}
	id, err := requireID(args)
	if err != nil {
		return 0, toolErr(err.Error()), false
	}
	return id, ToolResult{}, true
}

// templateInputFromArgs 把结构化 JSON 参数转为 service 输入，校验完全复用 service 内既有逻辑。
func templateInputFromArgs(args map[string]any) (service.BotLoadTemplateInput, error) {
	schedule, err := rawJSONArg(args, "commandSchedule")
	if err != nil {
		return service.BotLoadTemplateInput{}, fmt.Errorf("参数 commandSchedule 无效: %w", err)
	}
	profile, err := rawJSONArg(args, "loadProfile")
	if err != nil {
		return service.BotLoadTemplateInput{}, fmt.Errorf("参数 loadProfile 无效: %w", err)
	}
	thresholds, err := rawJSONArg(args, "thresholds")
	if err != nil {
		return service.BotLoadTemplateInput{}, fmt.Errorf("参数 thresholds 无效: %w", err)
	}
	return service.BotLoadTemplateInput{
		Name:            stringArg(args, "name"),
		Description:     stringArg(args, "description"),
		CommandSchedule: schedule,
		LoadProfile:     profile,
		Thresholds:      thresholds,
		Tags:            stringSliceArg(args, "tags"),
	}, nil
}

func stringSliceArg(args map[string]any, key string) []string {
	items, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}
	return out
}

// execRunCreate 创建运行：目标实例过 scope；templateId 存在时走模板快照路径。
func execRunCreate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	instanceID, err := toUint(args["instanceId"])
	if err != nil {
		return toolErr("参数 instanceId 无效: " + err.Error())
	}
	if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, instanceID); err != nil {
		return toolForbidden(err)
	}
	if templateID := uintPtrArg(args, "templateId"); templateID != nil {
		return createRunFromTemplate(deps, instanceID, *templateID, args)
	}
	return createRunDirect(deps, instanceID, args)
}

func createRunFromTemplate(deps ToolDeps, instanceID, templateID uint, args map[string]any) ToolResult {
	if deps.LoadTemplate == nil {
		return toolErr("压测模板服务不可用")
	}
	config, err := rawJSONArg(args, "config")
	if err != nil {
		return toolErr("参数 config 无效: " + err.Error())
	}
	session, err := deps.LoadTemplate.CreateRunFromTemplate(
		templateID, agentTemplateOwnerID, agentTemplateIsAdmin,
		instanceID, stringArg(args, "name"), stringArg(args, "namePrefix"), config,
		nil, nil, nil,
	)
	if err != nil {
		return toolErr("从模板创建运行失败: " + err.Error())
	}
	return toolOK(map[string]any{"runId": session.ID, "runUuid": session.UUID, "instanceId": session.InstanceID})
}

func createRunDirect(deps ToolDeps, instanceID uint, args map[string]any) ToolResult {
	if deps.StressSession == nil {
		return toolErr("压测会话服务不可用")
	}
	config, err := rawJSONArg(args, "config")
	if err != nil {
		return toolErr("参数 config 无效: " + err.Error())
	}
	view, err := deps.StressSession.Create(service.CreateBotStressSessionRequest{
		InstanceID: instanceID,
		Count:      intArg(args, "count"),
		Behavior:   stringArg(args, "behavior"),
		NamePrefix: stringArg(args, "namePrefix"),
		Config:     config,
	})
	if err != nil {
		return toolErr("创建压测运行失败: " + err.Error())
	}
	return toolOK(view)
}

func execRunList(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if deps.Agent == nil || deps.StressSession == nil {
		return toolErr("压测会话服务不可用")
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	instances, err := deps.Agent.ListAccessibleInstances(p, nil)
	if err != nil {
		return toolForbidden(err)
	}
	scopeIDs := make([]uint, 0, len(instances))
	for _, inst := range instances {
		scopeIDs = append(scopeIDs, inst.ID)
	}
	page, pageSize := projectionPageArgs(args)
	res, err := deps.StressSession.List(service.BotStressSessionListQuery{Page: page, PageSize: pageSize}, scopeIDs, true)
	if err != nil {
		return toolErr("查询压测运行失败: " + err.Error())
	}
	return toolOK(res)
}

func execRunGet(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, _, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.StressSession == nil {
		return toolErr("压测会话服务不可用")
	}
	view, err := deps.StressSession.Get(session.ID)
	if err != nil {
		return toolErr("查询压测运行失败: " + err.Error())
	}
	return toolOK(view)
}

// execNodeCapacity 返回 scope 内发压节点容量，越界节点直接过滤不暴露。
func execNodeCapacity(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, _ map[string]any) ToolResult {
	if deps.Capacity == nil {
		return toolErr("发压容量目录不可用")
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	snapshot, err := deps.Capacity.Snapshot(ctx, 0)
	if err != nil {
		return toolErr("查询发压节点容量失败: " + err.Error())
	}
	items := make([]service.BotLoadNodeCapacity, 0, len(snapshot.NodeCapacities))
	totalCapacity, availableCapacity := 0, 0
	for _, item := range snapshot.NodeCapacities {
		if !agentCanUseNode(p, item.NodeID) {
			continue
		}
		items = append(items, item)
		totalCapacity += item.MaxBots
		availableCapacity += item.AvailableBots
	}
	return toolOK(map[string]any{
		"items": items, "totalCapacity": totalCapacity,
		"availableCapacity": availableCapacity, "updatedAt": snapshot.UpdatedAt,
	})
}

// execRunPreflight 预检：目标实例 + 显式 executor 节点集合双重 scope，任一越界整体拒绝。
// planToken 由 service 生成后原样投影，MCP 不解析不缓存。
func execRunPreflight(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, outOfScope, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.Preflight == nil || deps.StressSession == nil {
		return toolErr("压测预检服务不可用")
	}
	// 启动方向严格：已持久化的 executor 节点集合中任一越界即拒绝。
	if len(outOfScope) > 0 {
		return toolErr(fmt.Sprintf("拒绝：运行涉及的发压节点 %v 不在 Token scope 内", outOfScope))
	}
	nodeIDs, err := uintSliceArg(args, "executorNodeIds")
	if err != nil {
		return toolErr(err.Error())
	}
	if err := deps.Agent.AuthorizeBotRunExecutorNodes(p, nodeIDs); err != nil {
		return toolErr("拒绝：指定的发压节点存在不在 Token scope 内的节点，不做静默缩减")
	}
	loaded, err := deps.StressSession.LoadForBotLoad(ctx, session.ID)
	if err != nil {
		return toolErr("加载压测运行失败: " + err.Error())
	}
	res, err := deps.Preflight.Preflight(ctx, loaded, service.BotLoadPreflightInput{
		TargetBots:                  loaded.BotCount,
		ExecutorNodeIDs:             nodeIDs,
		ConnectRatePerSecondPerNode: intArg(args, "connectRatePerSecondPerNode"),
		Probe: service.BotLoadProbeStatus{
			InstanceID: loaded.InstanceID, InstanceUUID: loaded.Instance.UUID,
		},
	})
	if err != nil {
		return toolErr("预检失败: " + err.Error())
	}
	return toolOK(res)
}

// execRunStart 启动运行：planToken 原样透传给 service，不解析不重签不缓存。
func execRunStart(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, outOfScope, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.Execution == nil {
		return toolErr("压测执行服务不可用")
	}
	if len(outOfScope) > 0 {
		return toolErr(fmt.Sprintf("拒绝：运行涉及的发压节点 %v 不在 Token scope 内", outOfScope))
	}
	planToken := stringArg(args, "planToken")
	if strings.TrimSpace(planToken) == "" {
		return toolErr("缺少必填参数 planToken：请先调用 loadtest_run_preflight 获取")
	}
	if _, err := deps.Execution.Start(ctx, session.ID, planToken); err != nil {
		return toolErr("启动失败: " + err.Error())
	}
	return toolOK(map[string]any{"ok": true, "runId": session.ID})
}

// execRunStop 停止运行：安全方向允许部分节点越界仍执行，但必须在返回中列出越界节点。
func execRunStop(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, outOfScope, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.Execution == nil {
		return toolErr("压测执行服务不可用")
	}
	if _, err := deps.Execution.Stop(ctx, session.ID, stringArg(args, "reason")); err != nil {
		return toolErr("停止失败: " + err.Error())
	}
	payload := map[string]any{"ok": true, "runId": session.ID}
	if len(outOfScope) > 0 {
		payload["outOfScopeExecutorNodeIds"] = outOfScope
		payload["notice"] = "已执行停止；上述发压节点不在当前 Token scope 内，其停止结果请另行确认"
	}
	return toolOK(payload)
}

// execRunRetryFailed 重试失败子集；requestId 幂等由 service 审计保证。
func execRunRetryFailed(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, outOfScope, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.Execution == nil {
		return toolErr("压测执行服务不可用")
	}
	if len(outOfScope) > 0 {
		return toolErr(fmt.Sprintf("拒绝：运行涉及的发压节点 %v 不在 Token scope 内", outOfScope))
	}
	res, err := deps.Execution.RetryFailed(ctx, session.ID, service.BotLoadRetryRequest{
		RequestID:  stringArg(args, "requestId"),
		BotUUIDs:   stringSliceArg(args, "botUuids"),
		ErrorCodes: stringSliceArg(args, "errorCodes"),
		FromStepID: stringArg(args, "fromStepId"),
	})
	if err != nil {
		return toolErr("重试失败: " + err.Error())
	}
	return toolOK(res)
}

// authorizeRunTarget 解析运行 ID 并完成双重 scope 授权，返回越界 executor 节点供调用方按方向处置。
func authorizeRunTarget(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) (*model.BotStressSession, []uint, ToolResult, bool) {
	if deps.Agent == nil {
		return nil, nil, toolErr("策略服务不可用"), false
	}
	id, err := requireID(args)
	if err != nil {
		return nil, nil, toolErr(err.Error()), false
	}
	_, session, outOfScope, err := deps.Agent.AuthorizeBotRunAction(p, action, id)
	if err != nil {
		return nil, nil, toolForbidden(err), false
	}
	return session, outOfScope, ToolResult{}, true
}

// agentCanUseNode 判断节点是否在 Token 的节点 scope 内。
func agentCanUseNode(p *service.AgentPrincipal, nodeID uint) bool {
	for _, id := range p.ScopedNodeIDs {
		if id == nodeID {
			return true
		}
	}
	return false
}
