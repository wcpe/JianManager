package mcp

import "encoding/json"

// FR-398：Bot 域工具共用的 InputSchema 构造与参数读取helpers。

// objectSchema 构造 object 型 InputSchema；required 为空时不写该字段。
func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func numberProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func objectProp(desc string) map[string]any {
	return map[string]any{"type": "object", "description": desc}
}

// runIDSchema 构造以运行 ID 为必填、并可附加额外属性的 schema。
func runIDSchema(extra map[string]any) map[string]any {
	properties := map[string]any{"id": numberProp("压测运行（会话）ID")}
	for key, value := range extra {
		properties[key] = value
	}
	return objectSchema(properties, []string{"id"})
}

// pagingProps 返回分页参数属性，供明细类工具复用。
func pagingProps() map[string]any {
	return map[string]any{
		"page":     numberProp("页码，从 1 开始"),
		"pageSize": numberProp("每页条数，默认 20，最大 100；大批量明细请用 page 递增遍历"),
	}
}

// stringArg 读取字符串参数；缺失或类型不符返回空串。
func stringArg(args map[string]any, key string) string {
	if raw, ok := args[key].(string); ok {
		return raw
	}
	return ""
}

// projectionPageArgs 解析分页参数，越界收敛交由 service 的 normalizeProjectionPage 兜底。
func projectionPageArgs(args map[string]any) (page, pageSize int) {
	return intArg(args, "page"), intArg(args, "pageSize")
}

// intArg 读取整数参数；缺失或非法返回 0，由下游归一化。
func intArg(args map[string]any, key string) int {
	switch n := args[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		if v, err := n.Int64(); err == nil {
			return int(v)
		}
	}
	return 0
}

// rawJSONArg 把结构化 JSON 对象参数转回原始字节，供 service 既有校验复用。
// MCP 只接受结构化 JSON（不接受 YAML 文本），避免解析歧义。
func rawJSONArg(args map[string]any, key string) (json.RawMessage, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// uintPtrArg 读取可选的正整数参数指针；缺失返回 nil。
func uintPtrArg(args map[string]any, key string) *uint {
	if _, ok := args[key]; !ok {
		return nil
	}
	value, err := toUint(args[key])
	if err != nil {
		return nil
	}
	return &value
}

// uintSliceArg 读取 uint 数组参数（如 executorNodeIds）。
func uintSliceArg(args map[string]any, key string) ([]uint, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errInvalidUintSlice(key)
	}
	out := make([]uint, 0, len(items))
	for _, item := range items {
		value, err := toUint(item)
		if err != nil {
			return nil, errInvalidUintSlice(key)
		}
		out = append(out, value)
	}
	return out, nil
}
