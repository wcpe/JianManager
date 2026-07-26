package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-398：压测运行观测与报告投影工具。
// SSE 流不开放——MCP 是请求-响应模型，流式观测用轮询这些工具代替。

// maxReportInlineBytes 是 tool 响应内联报告的体量护栏（512KiB）。
// 超限返回中文摘要与引导，而非截断正文，避免 Agent 误当完整数据。
const maxReportInlineBytes = 512 * 1024

// loadTestQueryToolSpecs 声明观测与报告工具协议。
func loadTestQueryToolSpecs() []toolSpec {
	detailPaging := mergeProps(pagingProps(), map[string]any{
		"executorNodeId": numberProp("可选：按发压节点过滤"),
		"stepId":         stringProp("可选：按步骤过滤"),
	})
	return []toolSpec{
		{
			Def: ToolDef{
				Name:        "loadtest_run_bots",
				Description: "分页查询运行关联 Bot 明细；单次最多 100 条，大批量请用 page 递增遍历",
				InputSchema: runIDSchema(mergeProps(detailPaging, map[string]any{
					"q":         stringProp("可选：按名称或 UUID 模糊搜索"),
					"status":    stringProp("可选：按 Bot 状态过滤"),
					"errorCode": stringProp("可选：按错误码过滤"),
				})),
			},
			Action: service.AgentActionLoadTestRunBots,
		},
		{
			Def: ToolDef{
				Name:        "loadtest_run_failures",
				Description: "分页查询运行失败明细（含分类、错误码与可重试标记）；单次最多 100 条，用 page 递增遍历",
				InputSchema: runIDSchema(mergeProps(detailPaging, map[string]any{
					"category":  stringProp("可选：按失败分类过滤"),
					"errorCode": stringProp("可选：按错误码过滤"),
					"botUuid":   stringProp("可选：按 Bot UUID 过滤"),
					"from":      stringProp("可选：起始时间，RFC3339"),
					"to":        stringProp("可选：结束时间，RFC3339"),
				})),
			},
			Action: service.AgentActionLoadTestRunFailures,
		},
		{
			Def: ToolDef{
				Name: "loadtest_run_events",
				Description: "分页查询运行事件历史；单次最多 100 条，用 page 递增遍历。" +
					"首次调用返回 snapshotEventId，后续分页请回传以冻结视图",
				InputSchema: runIDSchema(mergeProps(detailPaging, map[string]any{
					"type":            stringProp("可选：按事件类型过滤"),
					"eventId":         stringProp("可选：定位单条事件"),
					"actionRunId":     stringProp("可选：按动作运行 ID 过滤"),
					"botUuid":         stringProp("可选：按 Bot UUID 过滤"),
					"snapshotEventId": stringProp("可选：冻结分页视图的快照事件 ID"),
					"from":            stringProp("可选：起始时间，RFC3339"),
					"to":              stringProp("可选：结束时间，RFC3339"),
				})),
			},
			Action: service.AgentActionLoadTestRunEvents,
		},
		{
			Def: ToolDef{
				Name:        "loadtest_run_metrics",
				Description: "查询运行聚合指标样本（须具备 observability.read 能力）；可选 from/to 与 resolution=raw|15s|1m|5m",
				InputSchema: runIDSchema(map[string]any{
					"from":       stringProp("可选：起始时间，RFC3339"),
					"to":         stringProp("可选：结束时间，RFC3339"),
					"resolution": stringProp("可选：聚合粒度 raw|15s|1m|5m"),
				}),
			},
			Action: service.AgentActionLoadTestRunMetrics,
		},
		{
			Def: ToolDef{
				Name: "loadtest_run_report",
				Description: "导出终态运行报告，format=json（默认）或 csv；运行未终态时返回未就绪。" +
					"报告过大时只返回摘要与引导，请改用分页明细工具获取完整数据",
				InputSchema: runIDSchema(map[string]any{
					"format": stringProp("json 或 csv，默认 json"),
				}),
			},
			Action: service.AgentActionLoadTestRunReport,
		},
	}
}

// loadTestQueryToolExecutors 绑定观测与报告执行器。
func loadTestQueryToolExecutors() map[string]botToolExec {
	return map[string]botToolExec{
		"loadtest_run_bots":     execRunBots,
		"loadtest_run_failures": execRunFailures,
		"loadtest_run_events":   execRunEvents,
		"loadtest_run_metrics":  execRunMetrics,
		"loadtest_run_report":   execRunReport,
	}
}

func execRunBots(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, _, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.Projection == nil {
		return toolErr("压测投影服务不可用")
	}
	page, pageSize := projectionPageArgs(args)
	res, err := deps.Projection.ListBots(ctx, session.ID, service.ListBotsQuery{
		Page: page, PageSize: pageSize,
		Q: stringArg(args, "q"), Status: stringArg(args, "status"),
		ExecutorNodeID: uintPtrArg(args, "executorNodeId"),
		StepID:         stringArg(args, "stepId"), ErrorCode: stringArg(args, "errorCode"),
	})
	if err != nil {
		return toolErr("查询运行 Bot 失败: " + err.Error())
	}
	return toolOK(res)
}

func execRunFailures(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, _, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.Projection == nil {
		return toolErr("压测投影服务不可用")
	}
	from, to, err := optionalTimeRange(args)
	if err != nil {
		return toolErr(err.Error())
	}
	page, pageSize := projectionPageArgs(args)
	res, err := deps.Projection.ListFailures(ctx, session.ID, service.ListFailuresQuery{
		Page: page, PageSize: pageSize,
		Category: stringArg(args, "category"), ErrorCode: stringArg(args, "errorCode"),
		BotUUID: stringArg(args, "botUuid"), StepID: stringArg(args, "stepId"),
		ExecutorNodeID: uintPtrArg(args, "executorNodeId"),
		From:           from, To: to,
	})
	if err != nil {
		return toolErr("查询失败明细失败: " + err.Error())
	}
	return toolOK(res)
}

func execRunEvents(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, _, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.Projection == nil {
		return toolErr("压测投影服务不可用")
	}
	from, to, err := optionalTimeRange(args)
	if err != nil {
		return toolErr(err.Error())
	}
	page, pageSize := projectionPageArgs(args)
	res, err := deps.Projection.ListEvents(ctx, session.ID, service.ListEventsQuery{
		Page: page, PageSize: pageSize,
		Type: stringArg(args, "type"), EventID: stringArg(args, "eventId"),
		ActionRunID: stringArg(args, "actionRunId"), BotUUID: stringArg(args, "botUuid"),
		ExecutorNodeID: uintPtrArg(args, "executorNodeId"), StepID: stringArg(args, "stepId"),
		SnapshotEventID: stringArg(args, "snapshotEventId"),
		From:            from, To: to,
	})
	if err != nil {
		return toolErr("查询运行事件失败: " + err.Error())
	}
	return toolOK(res)
}

func execRunMetrics(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, _, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.Metrics == nil {
		return toolErr("压测指标服务不可用")
	}
	from, to, err := optionalTimeRange(args)
	if err != nil {
		return toolErr(err.Error())
	}
	res, err := deps.Metrics.ListMetrics(ctx, session.ID, from, to, stringArg(args, "resolution"))
	if err != nil {
		return toolErr("查询运行指标失败: " + err.Error())
	}
	return toolOK(res)
}

// execRunReport 导出终态报告；超过体量护栏时返回摘要与引导而非截断正文。
func execRunReport(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	session, _, result, ok := authorizeRunTarget(deps, p, action, args)
	if !ok {
		return result
	}
	if deps.Report == nil {
		return toolErr("压测报告服务不可用")
	}
	format := strings.ToLower(strings.TrimSpace(stringArg(args, "format")))
	if format == "csv" {
		raw, err := deps.Report.BuildCSV(session.ID)
		if err != nil {
			return toolErr("导出报告失败: " + err.Error())
		}
		if len(raw) > maxReportInlineBytes {
			return toolOK(oversizedReportNotice(session.ID, len(raw)))
		}
		return toolOK(map[string]any{"runId": session.ID, "format": "csv", "content": string(raw)})
	}
	if format != "" && format != "json" {
		return toolErr("参数 format 仅支持 json 或 csv")
	}
	report, err := deps.Report.BuildJSON(session.ID)
	if err != nil {
		return toolErr("导出报告失败: " + err.Error())
	}
	return toolOK(report)
}

// oversizedReportNotice 构造超限报告的中文摘要引导。
func oversizedReportNotice(runID uint, size int) map[string]any {
	return map[string]any{
		"runId":     runID,
		"oversized": true,
		"sizeBytes": size,
		"notice": fmt.Sprintf(
			"报告体量 %d 字节，超过单次返回上限 %d 字节，未内联返回完整正文；"+
				"请改用 loadtest_run_bots / loadtest_run_failures / loadtest_run_events 分页获取明细",
			size, maxReportInlineBytes),
	}
}

// optionalTimeRange 解析可选的 from/to（RFC3339）。
func optionalTimeRange(args map[string]any) (from, to *time.Time, err error) {
	from, err = parseRFC3339Arg(args, "from")
	if err != nil {
		return nil, nil, err
	}
	to, err = parseRFC3339Arg(args, "to")
	if err != nil {
		return nil, nil, err
	}
	return from, to, nil
}

func parseRFC3339Arg(args map[string]any, key string) (*time.Time, error) {
	raw := strings.TrimSpace(stringArg(args, key))
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("参数 %s 须为 RFC3339 时间格式", key)
	}
	return &parsed, nil
}
