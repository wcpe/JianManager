package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-398：普通 Bot 舰队工具。
// 说明：本文件与 tools_loadtest*.go 自带执行器分发与危险确认实现，
// 待 FR-396 的 toolSpec.Exec / RequiresConfirm 骨架合入后可整体收敛为一份。

// botToolExec 是 Bot 域工具执行器签名，等价 FR-396 规划中的 toolSpec.Exec。
type botToolExec func(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult

// BotLoadExecutor 抽象压测运行的启停与重试，便于 MCP 契约测试注入替身。
type BotLoadExecutor interface {
	Start(ctx context.Context, sessionID uint, planToken string) (*model.BotStressSession, error)
	Stop(ctx context.Context, sessionID uint, reasons ...string) (*model.BotStressSession, error)
	RetryFailed(ctx context.Context, sessionID uint, req service.BotLoadRetryRequest) (*service.BotLoadRetryResult, error)
}

// botDomainExecutors 汇总本 FR 全部工具执行器。
func botDomainExecutors() map[string]botToolExec {
	all := make(map[string]botToolExec, 24)
	for name, exec := range botToolExecutors() {
		all[name] = exec
	}
	for name, exec := range loadTestToolExecutors() {
		all[name] = exec
	}
	for name, exec := range loadTestQueryToolExecutors() {
		all[name] = exec
	}
	return all
}

// botDomainExec 按工具名取执行器。
func botDomainExec(name string) (botToolExec, bool) {
	exec, ok := botDomainExecutors()[name]
	return exec, ok
}

// botSendCommandSuccessText 是 ADR-075 规定的命令发送成功文案。
// 成功边界止于 bot.chat 调用未同步抛错，绝不宣称服务器已接受或业务已生效。
func botSendCommandSuccessText() string {
	return "已发送（bot.chat 调用成功）；不代表服务器已接受或业务已生效"
}

// botToolSpecs 声明普通 Bot 域工具协议。
func botToolSpecs() []toolSpec {
	return []toolSpec{
		{
			Def: ToolDef{
				Name:        "bot_list",
				Description: "列出 scope 内实例上的 Bot；可选按 instanceId 过滤，支持 page/pageSize（默认 20，上限 100）",
				InputSchema: objectSchema(map[string]any{
					"instanceId": numberProp("可选：仅返回该实例上的 Bot"),
					"page":       numberProp("页码，从 1 开始"),
					"pageSize":   numberProp("每页条数，默认 20，最大 100"),
				}, nil),
			},
			Action: service.AgentActionBotList,
		},
		{
			Def: ToolDef{
				Name:        "bot_get",
				Description: "获取指定 Bot 详情（Bot 所属实例须在 scope 内）",
				InputSchema: idSchema("Bot ID"),
			},
			Action: service.AgentActionBotGet,
		},
		{
			Def: ToolDef{
				Name:        "bot_create",
				Description: "在 scope 内实例上创建 Bot（须具备 bot.manage 能力）",
				InputSchema: objectSchema(map[string]any{
					"instanceId": numberProp("目标实例 ID"),
					"name":       stringProp("Bot 名称"),
					"config":     stringProp("可选：连接配置 JSON 字符串（server/port/username/version/auth）"),
					"behavior":   stringProp("可选：行为模式"),
				}, []string{"instanceId", "name"}),
			},
			Action: service.AgentActionBotCreate,
		},
		{
			Def: ToolDef{
				Name:        "bot_set_behavior",
				Description: "切换 Bot 行为模式；压测舰队管理的 Bot 拒绝改动（须具备 bot.manage 能力）",
				InputSchema: objectSchema(map[string]any{
					"id":       numberProp("Bot ID"),
					"behavior": stringProp("目标行为模式"),
				}, []string{"id", "behavior"}),
			},
			Action: service.AgentActionBotSetBehavior,
		},
		{
			Def: ToolDef{
				Name: "bot_send_command",
				// ADR-075：工具描述必须写明成功边界，防 Agent 误报业务成功。
				Description: "向 Bot 下发聊天/命令。成功仅表示 bot.chat 调用成功，不代表服务器已接受或业务已生效；" +
					"如需确认业务效果请另行观测服务器状态（须具备 bot.manage 能力）",
				InputSchema: objectSchema(map[string]any{
					"id":      numberProp("Bot ID"),
					"command": stringProp("要发送的聊天内容或命令"),
				}, []string{"id", "command"}),
			},
			Action: service.AgentActionBotSendCommand,
		},
		{
			Def: ToolDef{
				Name:        "bot_delete",
				Description: "删除 Bot（危险操作）。必须传 confirmBotName，其值需精确等于 Bot 名称，否则拒绝执行",
				InputSchema: objectSchema(map[string]any{
					"id":             numberProp("Bot ID"),
					"confirmBotName": stringProp("精确确认：必须等于目标 Bot 的名称"),
				}, []string{"id", "confirmBotName"}),
			},
			Action: service.AgentActionBotDelete,
		},
	}
}

// botToolExecutors 绑定普通 Bot 域执行器。
func botToolExecutors() map[string]botToolExec {
	return map[string]botToolExec{
		"bot_list":         execBotList,
		"bot_get":          execBotGet,
		"bot_create":       execBotCreate,
		"bot_set_behavior": execBotSetBehavior,
		"bot_send_command": execBotSendCommand,
		"bot_delete":       execBotDelete,
	}
}

// execBotList 以 principal 可访问实例集合收敛 Bot 列表，scope 由 service 在 SQL 层执行。
func execBotList(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if deps.Agent == nil || deps.Bot == nil {
		return toolErr("Bot 服务不可用")
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
	filter := service.BotFilter{}
	if raw, ok := args["instanceId"]; ok {
		id, convErr := toUint(raw)
		if convErr != nil {
			return toolErr("参数 instanceId 无效: " + convErr.Error())
		}
		filter.InstanceID = &id
	}
	page, pageSize := projectionPageArgs(args)
	// scope=true 强制按可访问实例集合收敛，空集合即返回空列表。
	res, err := deps.Bot.ListPaged(service.BotListQuery{Filter: filter, Page: page, PageSize: pageSize}, scopeIDs, true)
	if err != nil {
		return toolErr("查询 Bot 列表失败: " + err.Error())
	}
	return toolOK(res)
}

// execBotGet 读取单个 Bot；scope 外与不存在统一收敛为拒绝。
func execBotGet(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	bot, result, ok := authorizeBotTarget(deps, p, action, args)
	if !ok {
		return result
	}
	detail, err := deps.Bot.GetByID(bot.ID)
	if err != nil {
		return toolErr("查询 Bot 失败: " + err.Error())
	}
	return toolOK(detail)
}

// execBotCreate 在目标实例上创建 Bot，实例归属由 CP 数据解析后授权。
func execBotCreate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if deps.Agent == nil || deps.Bot == nil {
		return toolErr("Bot 服务不可用")
	}
	instanceID, err := toUint(args["instanceId"])
	if err != nil {
		return toolErr("参数 instanceId 无效: " + err.Error())
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return toolErr("缺少必填参数 name")
	}
	if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, instanceID); err != nil {
		return toolForbidden(err)
	}
	bot, err := deps.Bot.Create(service.CreateBotRequest{
		InstanceID: instanceID,
		Name:       name,
		Config:     stringArg(args, "config"),
		Behavior:   stringArg(args, "behavior"),
	})
	if err != nil {
		return toolErr("创建 Bot 失败: " + err.Error())
	}
	return toolOK(bot)
}

// execBotSetBehavior 切换行为模式；Fleet 托管 Bot 的拒绝原因原样返回，不吞。
func execBotSetBehavior(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	bot, result, ok := authorizeBotTarget(deps, p, action, args)
	if !ok {
		return result
	}
	behavior := strings.TrimSpace(stringArg(args, "behavior"))
	if behavior == "" {
		return toolErr("缺少必填参数 behavior")
	}
	if err := deps.Bot.UpdateBehavior(bot.ID, behavior); err != nil {
		return toolErr("切换 Bot 行为失败: " + err.Error())
	}
	return toolOK(map[string]any{"ok": true, "botId": bot.ID, "behavior": behavior})
}

// execBotSendCommand 下发命令；返回文案严格遵循 ADR-075 的发送边界。
func execBotSendCommand(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	bot, result, ok := authorizeBotTarget(deps, p, action, args)
	if !ok {
		return result
	}
	command := stringArg(args, "command")
	if strings.TrimSpace(command) == "" {
		return toolErr("缺少必填参数 command")
	}
	if err := deps.Bot.SendCommand(bot.ID, command); err != nil {
		// 失败带上 Bot 定位信息，便于 Agent 排障。
		return toolErr(fmt.Sprintf("发送失败（Bot %s / ID %d）: %s", bot.Name, bot.ID, err.Error()))
	}
	return toolOK(map[string]any{
		"botId":   bot.ID,
		"botName": bot.Name,
		"result":  botSendCommandSuccessText(),
	})
}

// execBotDelete 删除 Bot，需精确确认名称。
func execBotDelete(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	bot, result, ok := authorizeBotTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if err := requireExactConfirm("confirmBotName", bot.Name, stringArg(args, "confirmBotName")); err != nil {
		return toolErr(err.Error())
	}
	if err := deps.Bot.Delete(bot.ID); err != nil {
		return toolErr("删除 Bot 失败: " + err.Error())
	}
	return toolOK(map[string]any{"ok": true, "deletedBotId": bot.ID})
}

// authorizeBotTarget 解析 id 参数并完成 Bot 目标授权；失败时返回可直接回传的 ToolResult。
func authorizeBotTarget(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) (*model.Bot, ToolResult, bool) {
	if deps.Agent == nil || deps.Bot == nil {
		return nil, toolErr("Bot 服务不可用"), false
	}
	id, err := requireID(args)
	if err != nil {
		return nil, toolErr(err.Error()), false
	}
	_, bot, err := deps.Agent.AuthorizeBotAction(p, action, id)
	if err != nil {
		return nil, toolForbidden(err), false
	}
	return bot, ToolResult{}, true
}
