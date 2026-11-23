package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestKindForCoreType coreType → 服务端类别映射。
//
// 三分法（用户确认粒度）：插件服 / 代理 / 二进制，不细分 Paper/Spigot/Bukkit。
func TestKindForCoreType(t *testing.T) {
	cases := map[string]model.InstanceKind{
		"velocity":      model.InstanceKindProxy,
		"VELOCITY":      model.InstanceKindProxy, // 归一化：大小写不敏感
		"waterfall":     model.InstanceKindProxy,
		"bungeecord":    model.InstanceKindProxy,
		"  bungeecord ": model.InstanceKindProxy, // 归一化：去首尾空白
		"binary":        model.InstanceKindBinary,
		"beacon":        model.InstanceKindBinary,
		"paper":         model.InstanceKindPlugin,
		"spongevanilla": model.InstanceKindPlugin,
		"spongeforge":   model.InstanceKindPlugin,
		// 未知 coreType 归最常见的一类，而不是报错/留空——让整行仍有信息量。
		"purpur": model.InstanceKindPlugin,
		"":       model.InstanceKindPlugin,
	}
	for coreType, want := range cases {
		t.Run(coreType, func(t *testing.T) {
			assert.Equal(t, want, KindForCoreType(coreType))
		})
	}
}
