package model

// Capability 实例能力（FR-445）：一个能力对应一段前端渲染 + 后端数据源实现，
// 在详情页通常呈现为一个页签。声明式画像据此决定可见 Tab 集合与顺序，取代散布的
// `if role === '...'` 硬编码分支（见 ADR-091）。
type Capability string

const (
	CapOverview   Capability = "overview"
	CapTerminal   Capability = "terminal"
	CapFiles      Capability = "files" // 文件配置（含 env 分段，FR-344）
	CapPlugins    Capability = "plugins"
	CapMetrics    Capability = "metrics" // MC/服务端指标（TPS 等，探针/直探来源）
	CapPlayers    Capability = "players"
	CapBusiness   Capability = "business"
	CapBot        Capability = "bot"
	CapBackup     Capability = "backup"
	CapBCTopology Capability = "bcTopology" // BC 子服拓扑（FR-449）
	CapProcess    Capability = "process"    // 通用进程指标（CPU/内存/线程/句柄，FR-450）
	CapHealth     Capability = "health"     // 端口 + 健康检查（FR-450）
	CapConfig     Capability = "config"     // 结构化配置编辑（FR-451）
)

// DataSource 能力的数据来源偏好，按序降级（FR-447 编排语义，本 FR 只声明不实现编排）。
type DataSource string

const (
	SourceProbe  DataSource = "probe"
	SourceDirect DataSource = "direct" // MC 直探 SLP/Query（FR-446）
	SourceNode   DataSource = "node"
	SourceNone   DataSource = "none"
)

// InstanceCapabilityProfile 实例能力画像（FR-445，ADR-091）：
// 以 (type, role) 为键声明该实例有哪些能力、是否具备 MC 世界语义、各能力的数据来源偏好。
//
// type 是实现形态（Java 进程 / 原生二进制），role 是拓扑职责（代理 / 子服 / 配套服务），
// 两轴正交，故键为组合而非单轴（beacon 既是原生二进制又是配套服务）。
type InstanceCapabilityProfile struct {
	Type string `json:"type"` // 实现形态
	Role string `json:"role"` // 拓扑职责
	// MCSemantics 是否具备 MC 服务端「世界语义」（world/chunk/TPS）。
	// 只影响同一 Tab 内的字段取舍，不作 Tab 级门控（Tab 显隐只由 capabilities 决定，FR-448）。
	MCSemantics bool `json:"mcSemantics"`
	// Capabilities 有序能力集合，决定 Tab 顺序。
	Capabilities []Capability `json:"capabilities"`
	// Sources 各能力的数据来源偏好（按序降级）；未声明的能力视为无来源偏好。
	Sources map[Capability][]DataSource `json:"sources,omitempty"`
}
