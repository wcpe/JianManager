package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsProbeApplicable FR-454：ServerProbe 是 Bukkit 插件，仅 Minecraft Java 服务端可加载。
// 代理（role=proxy）、Beacon（role=beacon）与通用二进制（type=generic）均不适用；
// 判定同时看 type 与 role，使历史误记 type=minecraft_java 的 beacon 仍被正确排除。
func TestIsProbeApplicable(t *testing.T) {
	tests := []struct {
		name string
		typ  InstanceType
		role InstanceRole
		want bool
	}{
		{"MC 后端适用", InstanceTypeMinecraftJava, InstanceRoleBackend, true},
		{"MC 通用适用", InstanceTypeMinecraftJava, InstanceRoleUniversal, true},
		{"MC 代理不适用", InstanceTypeMinecraftJava, InstanceRoleProxy, false},
		{"MC 角色的 Beacon 不适用", InstanceTypeMinecraftJava, InstanceRoleBeacon, false},
		{"通用二进制不适用", InstanceTypeGeneric, InstanceRoleUniversal, false},
		{"Beacon 预设不适用", InstanceTypeGeneric, InstanceRoleBeacon, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsProbeApplicable(tc.typ, tc.role))
		})
	}
}
