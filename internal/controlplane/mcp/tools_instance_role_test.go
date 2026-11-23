package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// instance_update_config 的 role 参数（纠正建实例时的角色误选）。
//
// 真机场景：BungeeCord 代理建成 backend 后，群组拓扑（查 role='proxy'）永远认不出它，
// 代理注册也因此建不起来。此前实例角色只能在创建时指定、无法通过任何接口修改。
//
// 这里验证 schema 契约：工具必须暴露 role 字段及完整枚举，使调用方知道可传该字段。
// role 的业务校验（非法值拒绝）由 service 层单测覆盖（见 service/instance_role_test.go）。

// TestInstanceUpdateConfig_RoleSchemaExposed 验证 schema 暴露 role 且枚举完整。
func TestInstanceUpdateConfig_RoleSchemaExposed(t *testing.T) {
	var schema map[string]any
	for _, tl := range RegisteredTools() {
		if tl.Name == "instance_update_config" {
			schema = tl.InputSchema
			break
		}
	}
	require.NotNil(t, schema, "应注册 instance_update_config")

	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "schema 应含 properties")
	roleProp, ok := props["role"].(map[string]any)
	require.True(t, ok, "schema 应含 role 字段，否则调用方无法发现该能力")

	assert.Equal(t, "string", roleProp["type"])
	enum, ok := roleProp["enum"].([]string)
	require.True(t, ok, "role 应有枚举约束")
	assert.ElementsMatch(t, []string{"backend", "proxy", "universal", "beacon"}, enum,
		"枚举应覆盖全部四类角色（含新增的 beacon）")

	// 描述要能指导调用方理解为何要改角色。
	desc, _ := roleProp["description"].(string)
	assert.Contains(t, desc, "proxy", "描述应点明 proxy 用途")
}

// TestInstanceUpdateConfig_RoleIsOptional 回归保护：role 非必填，不影响既有调用。
func TestInstanceUpdateConfig_RoleIsOptional(t *testing.T) {
	for _, tl := range RegisteredTools() {
		if tl.Name != "instance_update_config" {
			continue
		}
		required, _ := tl.InputSchema["required"].([]string)
		for _, r := range required {
			assert.NotEqual(t, "role", r, "role 不应是必填项（保持既有调用兼容）")
		}
		return
	}
	t.Fatal("未找到 instance_update_config")
}
