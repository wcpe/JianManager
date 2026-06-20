package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// TestGracefulStopCommand 校验按角色派生优雅停止命令：
// 仅代理用 end，其余（后端/通用/空）沿用 MC 的 stop。
// 回归 FR-035：代理误发 stop 不退出 → 端口占用 → 重启崩溃。
func TestGracefulStopCommand(t *testing.T) {
	tests := []struct {
		name string
		role model.InstanceRole
		want string
	}{
		{"代理用 end", model.InstanceRoleProxy, "end"},
		{"后端用 stop", model.InstanceRoleBackend, "stop"},
		{"通用用 stop", model.InstanceRoleUniversal, "stop"},
		{"空角色回退 stop", model.InstanceRole(""), "stop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, gracefulStopCommand(tt.role))
		})
	}
}
