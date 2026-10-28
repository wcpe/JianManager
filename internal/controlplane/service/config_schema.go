package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 配置 Schema 校验（FR-529 §6）。
//
// 只实现产品需要的 JSON Schema Draft 2020-12 子集：
// `type / properties / required / enum / minimum / maximum / pattern / items / additionalProperties`。
// 刻意不引第三方校验库：
//   - 依赖面最小（配置下发是核心链路，不希望为一个子集引入 full spec 实现）；
//   - 错误信息可以按运维视角组织（指明键路径与原值），而不是 JSON Pointer；
//   - 子集边界显式——遇到不认识的约束关键字时**跳过**而不是假装校验过。

// SchemaIssue 是一条校验问题。
type SchemaIssue struct {
	// Level error 阻断保存/下发；warning 仅提示。
	Level string `json:"level"`
	// Path 出问题的键路径（点分）；根级问题为空。
	Path string `json:"path,omitempty"`
	// Message 人类可读说明，含期望与实际。
	Message string `json:"message"`
}

// ValidateConfigAgainstSchema 按 JSON Schema 子集校验扁平化后的配置值。
// schemaJSON 为空时视为无约束（返回 nil）。
//
// values 来自 ParseConfigContent：properties 为平铺键，yaml/json 为点分路径 → 原生值。
// properties 格式按扁平键名匹配 `properties`（§6.2）。
func ValidateConfigAgainstSchema(format model.ConfigFileFormat, schemaJSON string, values map[string]any) []SchemaIssue {
	if strings.TrimSpace(schemaJSON) == "" {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(schemaJSON), &root); err != nil {
		return []SchemaIssue{{Level: "error", Message: fmt.Sprintf("schema 不是合法 JSON: %v", err)}}
	}
	issues := make([]SchemaIssue, 0, 4)
	// properties 是扁平键匹配，直接对顶层键做约束；yaml/json 走嵌套递归。
	if format == model.ConfigFormatProperties {
		validateFlatSchema(root, values, &issues)
		return issues
	}
	validateSchemaNode(root, values, "", &issues)
	return issues
}

// validateFlatSchema 校验 properties 格式：`properties` 的键与配置的平铺键一一对应匹配。
func validateFlatSchema(schema map[string]any, values map[string]any, issues *[]SchemaIssue) {
	props, _ := schema["properties"].(map[string]any)
	// required 对扁平键同样生效。
	for _, req := range stringSlice(schema["required"]) {
		if _, ok := values[req]; !ok {
			*issues = append(*issues, SchemaIssue{Level: "error", Path: req, Message: fmt.Sprintf("缺少必填键 %q", req)})
		}
	}
	// 逐个已配置键做匹配。未在 properties 中声明的键按 additionalProperties 处理。
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sub, ok := props[k].(map[string]any)
		if !ok {
			if additional, exists := schema["additionalProperties"].(bool); exists && !additional {
				*issues = append(*issues, SchemaIssue{Level: "error", Path: k, Message: fmt.Sprintf("不允许的键 %q（additionalProperties=false）", k)})
			}
			continue
		}
		validateSchemaNode(sub, values[k], k, issues)
	}
}

// validateSchemaNode 递归校验一个 schema 节点对 value 的约束。
func validateSchemaNode(schema map[string]any, value any, path string, issues *[]SchemaIssue) {
	if schema == nil {
		return
	}
	// type
	if wantType, ok := schema["type"].(string); ok && !matchesJSONType(value, wantType) {
		*issues = append(*issues, SchemaIssue{
			Level: "error", Path: path,
			Message: fmt.Sprintf("类型不符: 期望 %s, 实际 %s", wantType, actualJSONType(value)),
		})
		// 类型不符时后续约束无意义（会二次报警），直接返回。
		return
	}
	// enum
	if enumVals, ok := schema["enum"].([]any); ok && len(enumVals) > 0 {
		if !containsAny(enumVals, value) {
			*issues = append(*issues, SchemaIssue{
				Level: "error", Path: path,
				Message: fmt.Sprintf("值 %v 不在允许范围 %v 内", value, enumVals),
			})
		}
	}
	// minimum / maximum（仅数值）
	if num, ok := numericValue(value); ok {
		if min, has := numericValue(schema["minimum"]); has && num < min {
			*issues = append(*issues, SchemaIssue{Level: "error", Path: path, Message: fmt.Sprintf("值 %v 小于最小值 %v", num, min)})
		}
		if max, has := numericValue(schema["maximum"]); has && num > max {
			*issues = append(*issues, SchemaIssue{Level: "error", Path: path, Message: fmt.Sprintf("值 %v 大于最大值 %v", num, max)})
		}
	}
	// pattern（仅字符串）
	if pattern, ok := schema["pattern"].(string); ok && pattern != "" {
		if s, isStr := value.(string); isStr {
			re, err := regexp.Compile(pattern)
			if err != nil {
				*issues = append(*issues, SchemaIssue{Level: "error", Path: path, Message: fmt.Sprintf("schema 的 pattern 非法: %v", err)})
			} else if !re.MatchString(s) {
				*issues = append(*issues, SchemaIssue{Level: "error", Path: path, Message: fmt.Sprintf("值 %q 不匹配 pattern %q", s, pattern)})
			}
		}
	}
	// items（仅数组）：必须在对象分支之前处理，否则数组值会在 isObj=false 处提前返回。
	if items, ok := schema["items"].(map[string]any); ok {
		if arr, isArr := value.([]any); isArr {
			for i, item := range arr {
				validateSchemaNode(items, item, fmt.Sprintf("%s[%d]", path, i), issues)
			}
		}
	}
	// properties / required / additionalProperties（仅对象）
	obj, isObj := value.(map[string]any)
	if !isObj {
		return
	}
	props, _ := schema["properties"].(map[string]any)
	for _, req := range stringSlice(schema["required"]) {
		if _, ok := obj[req]; !ok {
			childPath := joinPath(path, req)
			*issues = append(*issues, SchemaIssue{Level: "error", Path: childPath, Message: fmt.Sprintf("缺少必填键 %q", childPath)})
		}
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sub, ok := props[k].(map[string]any)
		childPath := joinPath(path, k)
		if !ok {
			if additional, exists := schema["additionalProperties"].(bool); exists && !additional {
				*issues = append(*issues, SchemaIssue{Level: "error", Path: childPath, Message: fmt.Sprintf("不允许的键 %q（additionalProperties=false）", childPath)})
			}
			continue
		}
		validateSchemaNode(sub, obj[k], childPath, issues)
	}
}

// joinPath 拼接点分路径；父为空时直接返回子。
func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

// matchesJSONType 判断实际值是否符合 JSON Schema type 名。
func matchesJSONType(value any, want string) bool {
	switch want {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := numericValue(value)
		return ok
	case "integer":
		n, ok := numericValue(value)
		return ok && n == float64(int64(n))
	case "null":
		return value == nil
	default:
		// 未知 type 名：不声称校验通过，但也不误报错误（跳过）。
		return true
	}
}

// actualJSONType 返回实际值的 JSON 类型名，用于错误文案。
func actualJSONType(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		if _, ok := numericValue(value); ok {
			return "number"
		}
		return "unknown"
	}
}

// numericValue 把任意数值表示转成 float64；非数值返回 false。
func numericValue(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint64:
		return float64(t), true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f, true
		}
		return 0, false
	case string:
		// properties 的值一律是字符串：`"25565"` 应能与 minimum/maximum 比较，
		// 否则 properties 的数值约束永远不生效。
		var f float64
		if _, err := fmt.Sscanf(t, "%g", &f); err == nil && strings.TrimSpace(t) != "" {
			return f, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// containsAny 判断 value 是否等于 enum 中任一元素（按 JSON 语义比较，允许数值跨类型相等）。
func containsAny(enumVals []any, value any) bool {
	for _, e := range enumVals {
		if jsonEqual(e, value) {
			return true
		}
	}
	return false
}

// jsonEqual 按 JSON 语义比较两个值：数值跨 int/float/字符串比较，其余按相等。
func jsonEqual(a, b any) bool {
	if na, ok := numericValue(a); ok {
		if nb, ok2 := numericValue(b); ok2 {
			return na == nb
		}
		return false
	}
	as, aIsStr := a.(string)
	bs, bIsStr := b.(string)
	if aIsStr && bIsStr {
		return as == bs
	}
	ab, aIsBool := a.(bool)
	bb, bIsBool := b.(bool)
	if aIsBool && bIsBool {
		return ab == bb
	}
	if a == nil && b == nil {
		return true
	}
	// 对象/数组退化为 JSON 文本比较。
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ja) == string(jb)
}

// stringSlice 把 schema 里的字符串数组取成 []string；非法元素忽略。
func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, isStr := item.(string); isStr {
			out = append(out, s)
		}
	}
	return out
}
