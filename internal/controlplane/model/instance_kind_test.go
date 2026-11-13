package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestKindOrInfer 旧数据（Kind 为空）的回退推断。
//
// 回退必须**保守且不丢信息**：旧库里的实例没有 kind 列值，若一律显示「未知」，
// 侧栏那一行的第二行就白占了；这里按 type+role 给出最合理的结论。
func TestKindOrInfer(t *testing.T) {
	cases := []struct {
		name string
		inst Instance
		want InstanceKind
	}{
		{
			name: "显式 kind 优先，不做推断",
			inst: Instance{Kind: InstanceKindBinary, Type: InstanceTypeMinecraftJava, Role: InstanceRoleBackend},
			want: InstanceKindBinary,
		},
		{
			name: "旧数据 role=proxy → 代理",
			inst: Instance{Type: InstanceTypeMinecraftJava, Role: InstanceRoleProxy},
			want: InstanceKindProxy,
		},
		{
			name: "旧数据 type=generic → 二进制",
			inst: Instance{Type: InstanceTypeGeneric, Role: InstanceRoleUniversal},
			want: InstanceKindBinary,
		},
		{
			name: "旧数据 beacon 角色（generic 类型）→ 二进制",
			inst: Instance{Type: InstanceTypeGeneric, Role: InstanceRoleBeacon},
			want: InstanceKindBinary,
		},
		{
			name: "其余 MC 服务端 → 插件服",
			inst: Instance{Type: InstanceTypeMinecraftJava, Role: InstanceRoleBackend},
			want: InstanceKindPlugin,
		},
		{
			name: "脏值（非法 kind 字符串）也走回退",
			inst: Instance{Kind: InstanceKind("garbage"), Type: InstanceTypeMinecraftJava, Role: InstanceRoleBackend},
			want: InstanceKindPlugin,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.inst.KindOrInfer())
		})
	}
}

// TestValidInstanceKind 枚举校验：只有三类合法。
func TestValidInstanceKind(t *testing.T) {
	for _, k := range []InstanceKind{InstanceKindPlugin, InstanceKindProxy, InstanceKindBinary} {
		assert.True(t, ValidInstanceKind(k), "%s 应合法", k)
	}
	for _, k := range []InstanceKind{"", "unknown", "PAPER", "plugin "} {
		assert.False(t, ValidInstanceKind(k), "%q 应非法", k)
	}
}
