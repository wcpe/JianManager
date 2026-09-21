package mcp

import (
	"context"
	"encoding/json"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 实例标签维护工具（FR-440）。
//
// 与 instance_update_config 刻意分离为独立工具：UpdateConfig 走 instance.configure（较重，
// 覆盖启动命令/JDK/资源限额等），本工具走 instance.write（较轻，只动 tags）。
// 三态语义——缺省=不改动、空数组=清空、非空=整体覆盖；与既有 PUT /instances/:id 的
// tags 字段语义一致（传 null/缺省不变，传数组含空数组覆盖）。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "instance_update_tags",
				Description: "更新实例标签列表（须 instance.write）；缺省 tags 不改动、传空数组清空、传非空数组整体覆盖",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "number", "description": "实例 ID"},
						"tags": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "标签列表（如 region:r1）；空数组表示清空",
						},
					},
					"required": []string{"id", "tags"},
				},
			},
			Action: service.AgentActionInstanceUpdateTags,
			Exec:   execInstanceUpdateTags,
		},
	)
}

// execInstanceUpdateTags 用 RawMessage 判别三态：调用方必须显式给 tags 字段。
// 由于 InputSchema 已把 tags 列为 required，此处的空值判别仅用于防御性处理
// （如显式传 null）：null 视为不改动，避免把「传 null」误当成「清空」。
func execInstanceUpdateTags(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, id); err != nil {
		return toolForbidden(err)
	}
	if deps.Instance == nil {
		return toolErr("实例服务不可用")
	}
	raw, ok := args["tags"]
	if !ok || raw == nil {
		return toolErr("缺少必填参数 tags（清空请传空数组）")
	}
	tags, err := toStringSliceStrict(raw)
	if err != nil {
		return toolErr("参数 tags 无效: " + err.Error())
	}
	var f service.UpdateInstanceFields
	f.Tags = &tags
	inst, err := deps.Instance.Update(id, f)
	if err != nil {
		return toolErr("更新标签失败: " + err.Error())
	}
	return toolOK(inst)
}

// toStringSliceStrict 严格解析字符串数组：非数组、或元素非字符串时返回错误，
// 不做静默丢弃（与面向 UI 的宽松解析区分——MCP 是契约面，错类型应显式失败）。
func toStringSliceStrict(v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		// 兼容直接传入的 []string（单测/内部调用）。
		if ss, ok2 := v.([]string); ok2 {
			out := make([]string, len(ss))
			copy(out, ss)
			return out, nil
		}
		if raw, ok3 := v.(json.RawMessage); ok3 {
			var ss []string
			if err := json.Unmarshal(raw, &ss); err != nil {
				return nil, err
			}
			return ss, nil
		}
		return nil, errNotStringArray
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, errNotStringArray
		}
		out = append(out, s)
	}
	return out, nil
}

type errStrArray struct{}

func (errStrArray) Error() string { return "期望字符串数组" }

var errNotStringArray = errStrArray{}
