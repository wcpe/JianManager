package service

import "github.com/wcpe/JianManager/internal/controlplane/model"

// profileKey 画像注册表键：实现形态 × 拓扑职责（两轴正交，见 ADR-091）。
type profileKey struct {
	Type model.InstanceType
	Role model.InstanceRole
}

// universalFallback 未知 (type, role) 组合的安全回退画像：只保留对任何进程都成立的能力，
// 保证非白屏（FR-445 §2.3、ADR-091 §理由）。
var universalFallback = model.InstanceCapabilityProfile{
	Type:         "",
	Role:         "",
	MCSemantics:  false,
	Capabilities: []model.Capability{model.CapOverview, model.CapTerminal, model.CapFiles, model.CapBackup},
}

// capabilityRegistry 代码内描述符注册表（不入 DB，见 ADR-091 §理由）：
// 画像每一项都对应一段前端渲染 + 后端数据源实现，二者随代码发布；存 DB 会让
// 「声明了某 Tab 但代码还没有」成为可能状态。新增产品 = 加一条描述符。
var capabilityRegistry = map[profileKey]model.InstanceCapabilityProfile{
	// 后端子服（Paper/Spigot/Purpur）：= 现有 9 Tab 的子集，行为零变化。
	{model.InstanceTypeMinecraftJava, model.InstanceRoleBackend}: {
		Type:        string(model.InstanceTypeMinecraftJava),
		Role:        string(model.InstanceRoleBackend),
		MCSemantics: true,
		Capabilities: []model.Capability{
			model.CapOverview, model.CapTerminal, model.CapFiles, model.CapPlugins,
			model.CapMetrics, model.CapPlayers, model.CapBusiness, model.CapBot, model.CapBackup,
			// 动作级能力（不落 Tab）：仅后端子服可克隆（复制工作目录/配置）。
			model.CapClone,
		},
		Sources: map[model.Capability][]model.DataSource{
			model.CapMetrics: {model.SourceProbe, model.SourceDirect},
			model.CapPlayers: {model.SourceProbe, model.SourceDirect},
			model.CapProcess: {model.SourceNode},
		},
	},
	// BC 代理（BungeeCord/Waterfall/Velocity）：无世界/TPS，故无 metrics；自身运行指标走 process；
	// 有跨服玩家（players）与 BungeeCord 插件（plugins）、子服拓扑（bcTopology）。
	{model.InstanceTypeMinecraftJava, model.InstanceRoleProxy}: {
		Type:        string(model.InstanceTypeMinecraftJava),
		Role:        string(model.InstanceRoleProxy),
		MCSemantics: false,
		Capabilities: []model.Capability{
			model.CapOverview, model.CapTerminal, model.CapFiles, model.CapBCTopology,
			model.CapPlayers, model.CapPlugins, model.CapProcess, model.CapHealth, model.CapConfig, model.CapBackup,
		},
		Sources: map[model.Capability][]model.DataSource{
			model.CapBCTopology: {model.SourceNode},
			model.CapPlayers:    {model.SourceProbe, model.SourceDirect},
			model.CapProcess:    {model.SourceNode},
			model.CapHealth:     {model.SourceDirect},
			model.CapConfig:     {model.SourceNode},
		},
	},
	// 通用/beacon：配套服务与非群组原生二进制，进程指标 + 端口健康 + 配置（FR-450）。
	{model.InstanceTypeGeneric, model.InstanceRoleBeacon}: {
		Type:        string(model.InstanceTypeGeneric),
		Role:        string(model.InstanceRoleBeacon),
		MCSemantics: false,
		Capabilities: []model.Capability{
			model.CapOverview, model.CapTerminal, model.CapFiles,
			model.CapProcess, model.CapHealth, model.CapConfig, model.CapBackup,
		},
		Sources: map[model.Capability][]model.DataSource{
			model.CapProcess: {model.SourceNode},
			model.CapHealth:  {model.SourceDirect},
			model.CapConfig:  {model.SourceNode},
		},
	},
	{model.InstanceTypeGeneric, model.InstanceRoleUniversal}: {
		Type:        string(model.InstanceTypeGeneric),
		Role:        string(model.InstanceRoleUniversal),
		MCSemantics: false,
		Capabilities: []model.Capability{
			model.CapOverview, model.CapTerminal, model.CapFiles,
			model.CapProcess, model.CapHealth, model.CapConfig, model.CapBackup,
		},
		Sources: map[model.Capability][]model.DataSource{
			model.CapProcess: {model.SourceNode},
			model.CapHealth:  {model.SourceDirect},
			model.CapConfig:  {model.SourceNode},
		},
	},
}

// ProfileFor 按 (type, role) 取实例能力画像；未知组合回退安全子集（overview/terminal/files + backup）。
// 返回的 Capabilities 为新切片，调用方修改不影响注册表。
func ProfileFor(t model.InstanceType, r model.InstanceRole) model.InstanceCapabilityProfile {
	p, ok := capabilityRegistry[profileKey{t, r}]
	if !ok {
		p = universalFallback
	}
	// 注册表内画像的 Type/Role 填回请求值，保证未知组合回退时也如实反映实例两轴。
	p.Type = string(t)
	p.Role = string(r)
	p.Capabilities = append([]model.Capability(nil), p.Capabilities...)
	return p
}

// KnownProfileTypes 返回注册表内全部已有画像的两轴组合，供一致性校验/测试枚举。
func KnownProfileTypes() []profileKey {
	out := make([]profileKey, 0, len(capabilityRegistry))
	for k := range capabilityRegistry {
		out = append(out, k)
	}
	return out
}

// AttachCapabilities 给实例挂上按当前 (type, role) 现算的画像（FR-445 §2.4）。
// 详情/单查路径与列表路径都会调用（列表逐行，故列表响应也带 capabilities）。
func AttachCapabilities(inst *model.Instance) {
	if inst == nil {
		return
	}
	p := ProfileFor(inst.Type, inst.Role)
	inst.Capabilities = &p
}

// attachCapabilitiesAll 给一批实例逐行挂画像（FR-445 §2.4/§2.5）：列表也优先用后端画像，
// 前端本地兜底表仅作降级。纯计算（map 查表 + 切片拷贝），不查库、不落库。
func attachCapabilitiesAll(insts []model.Instance) {
	for i := range insts {
		AttachCapabilities(&insts[i])
	}
}
