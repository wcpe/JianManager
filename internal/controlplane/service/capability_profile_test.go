package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-445：四种内置画像的能力集合、MC 世界语义，以及未知组合的回退安全子集。
func TestProfileFor_BuiltinProfiles(t *testing.T) {
	backend := ProfileFor(model.InstanceTypeMinecraftJava, model.InstanceRoleBackend)
	assert.True(t, backend.MCSemantics)
	assert.Equal(t, []model.Capability{
		model.CapOverview, model.CapTerminal, model.CapFiles, model.CapPlugins,
		model.CapMetrics, model.CapPlayers, model.CapBusiness, model.CapBot, model.CapBackup,
		model.CapClone, // 动作级能力（不落 Tab）：仅后端子服可克隆
	}, backend.Capabilities, "backend = 现有 9 Tab + 动作级 clone")

	proxy := ProfileFor(model.InstanceTypeMinecraftJava, model.InstanceRoleProxy)
	assert.False(t, proxy.MCSemantics)
	assert.Contains(t, proxy.Capabilities, model.CapBCTopology)
	assert.Contains(t, proxy.Capabilities, model.CapProcess)
	assert.NotContains(t, proxy.Capabilities, model.CapMetrics, "代理无世界/TPS，故无 metrics")
	assert.NotContains(t, proxy.Capabilities, model.CapBusiness, "代理无业务/世界 Tab")

	for _, role := range []model.InstanceRole{model.InstanceRoleBeacon, model.InstanceRoleUniversal} {
		p := ProfileFor(model.InstanceTypeGeneric, role)
		assert.False(t, p.MCSemantics)
		assert.Equal(t, []model.Capability{
			model.CapOverview, model.CapTerminal, model.CapFiles,
			model.CapProcess, model.CapHealth, model.CapConfig, model.CapBackup,
		}, p.Capabilities, "generic/%s 无任何 MC 专有能力", role)
		for _, mc := range []model.Capability{model.CapMetrics, model.CapPlugins, model.CapPlayers, model.CapBusiness, model.CapBot} {
			assert.NotContains(t, p.Capabilities, mc)
		}
	}
}

// FR-445 零硬编码：动作级能力 clone（可克隆）只由后端子服类实例声明，
// 前端行菜单据 hasCapability(profile,'clone') 显隐，取代 role === 'backend' 硬编码。
func TestProfileFor_CloneCapabilityBackendOnly(t *testing.T) {
	for _, k := range KnownProfileTypes() {
		p := ProfileFor(k.Type, k.Role)
		if k.Type == model.InstanceTypeMinecraftJava && k.Role == model.InstanceRoleBackend {
			assert.Contains(t, p.Capabilities, model.CapClone, "后端子服应可克隆")
			continue
		}
		assert.NotContains(t, p.Capabilities, model.CapClone, "%s/%s 不应可克隆", k.Type, k.Role)
	}
}

func TestProfileFor_UnknownFallsBack(t *testing.T) {
	p := ProfileFor(model.InstanceType("mythical_binary"), model.InstanceRole("wizard"))
	assert.Equal(t, []model.Capability{
		model.CapOverview, model.CapTerminal, model.CapFiles, model.CapBackup,
	}, p.Capabilities)
	assert.False(t, p.MCSemantics)
	// 回退时仍如实反映实例两轴。
	assert.Equal(t, "mythical_binary", p.Type)
	assert.Equal(t, "wizard", p.Role)
}

// 现算切片不得与注册表共享底层数组（防止调用方就地修改污染全局注册表）。
func TestProfileFor_ReturnsDetachedSlice(t *testing.T) {
	p1 := ProfileFor(model.InstanceTypeMinecraftJava, model.InstanceRoleBackend)
	require.NotEmpty(t, p1.Capabilities)
	p1.Capabilities[0] = model.CapConfig
	p2 := ProfileFor(model.InstanceTypeMinecraftJava, model.InstanceRoleBackend)
	assert.Equal(t, model.CapOverview, p2.Capabilities[0])
}

func TestAttachCapabilities_NilSafe(t *testing.T) {
	AttachCapabilities(nil)
	inst := &model.Instance{Type: model.InstanceTypeGeneric, Role: model.InstanceRoleBeacon}
	AttachCapabilities(inst)
	require.NotNil(t, inst.Capabilities)
	assert.Equal(t, string(model.InstanceRoleBeacon), inst.Capabilities.Role)
}

// 注册表内每种画像都必须非空且包含通用的 overview/terminal/files，否则详情页会白屏。
func TestRegistry_AllProfilesUsable(t *testing.T) {
	keys := KnownProfileTypes()
	require.NotEmpty(t, keys)
	for _, k := range keys {
		p := ProfileFor(k.Type, k.Role)
		assert.NotEmpty(t, p.Capabilities, "画像 %s/%s 能力集合不应为空", k.Type, k.Role)
		for _, base := range []model.Capability{model.CapOverview, model.CapTerminal, model.CapFiles} {
			assert.Contains(t, p.Capabilities, base, "画像 %s/%s 缺通用能力 %s", k.Type, k.Role, base)
		}
	}
}
