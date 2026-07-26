package mcp

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// mcpTextContentMaxBytes 是 MCP 文本正文上限（FR-397）：MCP 只承载小文本，
// 超限内容一律引导走流式传输票据，避免大字节经 JSON-RPC 往返。
const mcpTextContentMaxBytes = 512 * 1024

// ticketGuidance 是超限/二进制场景统一的中文替代路径指引。
const ticketGuidance = "请使用 file_issue_transfer_ticket 申请票据后走流式传输"

// binarySniffBytes 二进制探测采样字节数，与 Worker 索引侧口径一致。
const binarySniffBytes = 8000

// invalidUTF8PercentThreshold 非法 UTF-8 字节占比阈值（百分比），超过即判二进制。
const invalidUTF8PercentThreshold = 30

// callContentTool 内容运维域统一入口：按域依次尝试，未命中返回 handled=false。
func callContentTool(deps ToolDeps, p *service.AgentPrincipal, name, action string, args map[string]any) (ToolResult, bool) {
	if res, ok := callFileTool(deps, p, name, action, args); ok {
		return res, true
	}
	if res, ok := callConfigTool(deps, p, name, action, args); ok {
		return res, true
	}
	return callPluginTool(deps, p, name, action, args)
}

// requireTextWithinLimit 校验入参文本不超过 MCP 正文上限。
func requireTextWithinLimit(field, content string) error {
	if len(content) > mcpTextContentMaxBytes {
		return fmt.Errorf("%s 超过 512KiB 上限（当前 %d 字节），%s", field, len(content), ticketGuidance)
	}
	return nil
}

// buildReadTextResult 把读到的原始字节转为 MCP 文本结果：
// 二进制或超限一律拒绝并引导票据，绝不返回截断/损坏内容。
func buildReadTextResult(path string, content []byte) ToolResult {
	if looksBinaryContent(content) {
		return toolErr("文件 " + path + " 检测为二进制，无法以文本返回，" + ticketGuidance)
	}
	if len(content) > mcpTextContentMaxBytes {
		return toolErr(fmt.Sprintf("文件 %s 内容超过 512KiB 上限（当前 %d 字节），%s", path, len(content), ticketGuidance))
	}
	return toolOK(map[string]any{"path": path, "size": len(content), "content": string(content)})
}

// looksBinaryContent 判定内容是否为二进制：含 NUL 或非法 UTF-8 占比超阈值。
// 与 Worker 索引侧判定同口径；仅采样首部，避免大文件全量扫描。
func looksBinaryContent(content []byte) bool {
	sample := content
	if len(sample) > binarySniffBytes {
		sample = sample[:binarySniffBytes]
	}
	if len(sample) == 0 {
		return false
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	invalid, total := countInvalidUTF8(sample)
	return total > 0 && invalid*100/total > invalidUTF8PercentThreshold
}

// countInvalidUTF8 统计采样中的非法 UTF-8 rune 数与总 rune 数。
func countInvalidUTF8(sample []byte) (invalid, total int) {
	for len(sample) > 0 {
		r, size := utf8.DecodeRune(sample)
		if r == utf8.RuneError && size == 1 {
			invalid++
		}
		total++
		sample = sample[size:]
	}
	return invalid, total
}

// requireExactConfirm 危险操作的服务端精确确认（FR-397 §2.4）。
// 该函数刻意保持独立无依赖，便于与 FR-396 的 RequiresConfirm 目录机制收敛为一份。
func requireExactConfirm(field, expected, got string) error {
	if strings.TrimSpace(got) == "" {
		return fmt.Errorf("危险操作需显式确认：请提供 %s 且与目标完全一致", field)
	}
	if got != expected {
		return fmt.Errorf("%s 与目标不一致，已拒绝执行", field)
	}
	return nil
}

// authorizeInstanceTool 内容域工具统一的实例授权入口：目标由 CP 可信数据解析。
func authorizeInstanceTool(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) (uint, *ToolResult) {
	id, err := requireID(args)
	if err != nil {
		res := toolErr(err.Error())
		return 0, &res
	}
	if deps.Agent == nil {
		res := toolErr("策略服务不可用")
		return 0, &res
	}
	if _, _, aerr := deps.Agent.AuthorizeInstanceAction(p, action, id); aerr != nil {
		res := toolForbidden(aerr)
		return 0, &res
	}
	return id, nil
}

// requireStringArg 取必填字符串参数。
func requireStringArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("缺少必填参数 %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("参数 %s 须为字符串", key)
	}
	return s, nil
}

// optionalStringArg 取可选字符串参数；缺省返回空串。
func optionalStringArg(args map[string]any, key string) (string, error) {
	if _, ok := args[key]; !ok {
		return "", nil
	}
	return requireStringArg(args, key)
}

// optionalBoolArg 取可选布尔参数；缺省返回 false。
func optionalBoolArg(args map[string]any, key string) (bool, error) {
	v, ok := args[key]
	if !ok {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("参数 %s 须为布尔值", key)
	}
	return b, nil
}

// stringMapArg 取必填的「字符串→字符串」映射参数（如配置字段补丁）。
func stringMapArg(args map[string]any, key string) (map[string]string, error) {
	v, ok := args[key]
	if !ok {
		return nil, fmt.Errorf("缺少必填参数 %s", key)
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("参数 %s 须为对象", key)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("参数 %s 不能为空", key)
	}
	out := make(map[string]string, len(raw))
	for k, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("参数 %s.%s 须为字符串", key, k)
		}
		out[k] = s
	}
	return out, nil
}

// optionalUintArg 取可选正整数参数；缺省返回 0。
func optionalUintArg(args map[string]any, key string) (uint, error) {
	v, ok := args[key]
	if !ok {
		return 0, nil
	}
	n, err := toUint(v)
	if err != nil {
		return 0, fmt.Errorf("参数 %s 无效: %w", key, err)
	}
	return n, nil
}

// instancePathSchema 生成「实例 ID + 路径」类工具的 InputSchema。
func instancePathSchema(pathDesc string, pathRequired bool, extra map[string]any, required ...string) map[string]any {
	props := map[string]any{
		"id":   map[string]any{"type": "number", "description": "实例 ID"},
		"path": map[string]any{"type": "string", "description": pathDesc},
	}
	for k, v := range extra {
		props[k] = v
	}
	req := []string{"id"}
	if pathRequired {
		req = append(req, "path")
	}
	req = append(req, required...)
	return map[string]any{"type": "object", "properties": props, "required": req}
}

// stringProp 生成字符串属性描述。
func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// numberProp 生成数值属性描述。
func numberProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
