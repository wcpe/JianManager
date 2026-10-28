package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 配置 Schema 校验单测（FR-529 §6）。

// TestSchemaValidate_Types 验证 type 约束。
func TestSchemaValidate_Types(t *testing.T) {
	schemaJSON := `{
		"type":"object",
		"properties":{
			"port":{"type":"integer"},
			"motd":{"type":"string"},
			"enabled":{"type":"boolean"},
			"ratio":{"type":"number"}
		}
	}`
	values := map[string]any{
		"port":    int64(25565),
		"motd":    "hello",
		"enabled": true,
		"ratio":   jsonNumberForTest("0.75"),
	}
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, values))

	// 类型不符。
	bad := map[string]any{"port": "not-a-number"}
	issues := ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, bad)
	require.NotEmpty(t, issues)
	require.Equal(t, "port", issues[0].Path)
	require.Contains(t, issues[0].Message, "类型不符")
}

// jsonNumberForTest 构造 json.Number：与 parseStructured 的产出保持同一类型，
// 避免测试用 int64 而实现产出 json.Number 时的口径不一致。
func jsonNumberForTest(s string) any {
	return json.Number(s)
}

// TestSchemaValidate_Required 验证 required 约束。
func TestSchemaValidate_Required(t *testing.T) {
	schemaJSON := `{"type":"object","required":["port"],"properties":{"port":{"type":"integer"}}}`
	issues := ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{})
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0].Message, "缺少必填键")
}

// TestSchemaValidate_Enum 验证 enum 约束。
func TestSchemaValidate_Enum(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"mode":{"enum":["survival","creative"]}}}`
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{"mode": "survival"}))
	issues := ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{"mode": "hardcore"})
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0].Message, "不在允许范围")
}

// TestSchemaValidate_MinimumMaximum 验证数值区间约束。
func TestSchemaValidate_MinimumMaximum(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"port":{"type":"integer","minimum":25565,"maximum":25599}}}`
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{"port": int64(25570)}))

	for _, bad := range []int64{25564, 25600} {
		issues := ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{"port": bad})
		require.NotEmpty(t, issues, "越界值 %d 应被拒绝", bad)
		require.Equal(t, "port", issues[0].Path)
	}
}

// TestSchemaValidate_Pattern 验证正则约束。
func TestSchemaValidate_Pattern(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"version":{"type":"string","pattern":"^1\\.(1[6-9]|20)$"}}}`
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{"version": "1.20"}))
	issues := ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{"version": "1.8"})
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0].Message, "不匹配 pattern")
}

// TestSchemaValidate_Items 验证数组元素约束。
func TestSchemaValidate_Items(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"ports":{"type":"array","items":{"type":"integer","maximum":100}}}}`
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{
		"ports": []any{jsonNumberForTest("80"), jsonNumberForTest("99")},
	}))
	issues := ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{
		"ports": []any{jsonNumberForTest("80"), jsonNumberForTest("200")},
	})
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0].Path, "ports[1]", "必须指出是第几个元素")
}

// TestSchemaValidate_AdditionalProperties 验证 additionalProperties=false 拒绝未声明键。
func TestSchemaValidate_AdditionalProperties(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"a":{"type":"integer"}},"additionalProperties":false}`
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{"a": jsonNumberForTest("1")}))
	issues := ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{
		"a": jsonNumberForTest("1"), "b": jsonNumberForTest("2"),
	})
	require.NotEmpty(t, issues)
	require.Equal(t, "b", issues[0].Path)
}

// TestSchemaValidate_Nested 验证嵌套路径的校验与错误定位。
func TestSchemaValidate_Nested(t *testing.T) {
	schemaJSON := `{
		"type":"object",
		"properties":{
			"proxies":{"type":"object","properties":{
				"velocity":{"type":"object","required":["enabled"],"properties":{"enabled":{"type":"boolean"}}}
			}}
		}
	}`
	ok := map[string]any{"proxies": map[string]any{"velocity": map[string]any{"enabled": true}}}
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, ok))

	bad := map[string]any{"proxies": map[string]any{"velocity": map[string]any{"enabled": "yes"}}}
	issues := ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, bad)
	require.NotEmpty(t, issues)
	require.Equal(t, "proxies.velocity.enabled", issues[0].Path)
}

// TestSchemaValidate_PropertiesFlatKeys 验证 properties 按扁平键名匹配（§6.2）。
func TestSchemaValidate_PropertiesFlatKeys(t *testing.T) {
	schemaJSON := `{
		"type":"object",
		"required":["server-port"],
		"properties":{
			"server-port":{"type":"integer","minimum":25565,"maximum":25599},
			"max-players":{"type":"integer","minimum":1}
		}
	}`
	// properties 的值一律是字符串，但数值约束仍应生效。
	ok := map[string]any{"server-port": "25565", "max-players": "20"}
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatProperties, schemaJSON, ok))

	bad := map[string]any{"server-port": "1", "max-players": "20"}
	issues := ValidateConfigAgainstSchema(model.ConfigFormatProperties, schemaJSON, bad)
	require.NotEmpty(t, issues)
	require.Equal(t, "server-port", issues[0].Path)

	missing := map[string]any{"max-players": "20"}
	issues = ValidateConfigAgainstSchema(model.ConfigFormatProperties, schemaJSON, missing)
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0].Message, "缺少必填键")
}

// TestSchemaValidate_EmptySchemaNoConstraint 验证 schema 为空时不校验。
func TestSchemaValidate_EmptySchemaNoConstraint(t *testing.T) {
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, "", map[string]any{"anything": "x"}))
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, "   ", map[string]any{"anything": "x"}))
}

// TestSchemaValidate_InvalidSchemaReported 验证 schema 本身非法时报错（不静默通过）。
func TestSchemaValidate_InvalidSchemaReported(t *testing.T) {
	issues := ValidateConfigAgainstSchema(model.ConfigFormatJSON, "{not json", map[string]any{})
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0].Message, "schema 不是合法 JSON")
}

// TestSchemaValidate_UnknownTypeIgnored 验证未知 type 名被跳过（不误报也不声称校验过）。
func TestSchemaValidate_UnknownTypeIgnored(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"v":{"type":"duration"}}}`
	require.Empty(t, ValidateConfigAgainstSchema(model.ConfigFormatJSON, schemaJSON, map[string]any{"v": "1s"}))
}

// TestParseConfigContent_Properties 验证 properties 解析为平铺键值。
func TestParseConfigContent_Properties(t *testing.T) {
	values, err := ParseConfigContent(model.ConfigFormatProperties, "# 注释\nserver-port=25565\nmotd=hello world\n\nempty=\n")
	require.NoError(t, err)
	require.Equal(t, "25565", values["server-port"])
	require.Equal(t, "hello world", values["motd"])
	require.Equal(t, "", values["empty"])
	require.NotContains(t, values, "# 注释")
}

// TestParseConfigContent_YAMLFlat 验证 yaml 解析为点分路径。
func TestParseConfigContent_YAMLFlat(t *testing.T) {
	values, err := ParseConfigContent(model.ConfigFormatYAML, "proxies:\n  velocity:\n    enabled: true\n")
	require.NoError(t, err)
	require.Equal(t, true, values["proxies.velocity.enabled"])
	// 容器本身也在结果里。
	_, hasContainer := values["proxies.velocity"]
	require.True(t, hasContainer, "嵌套对象本身也应出现在扁平结果里")
}
