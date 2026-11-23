package model

import "time"

// 外部 MCP 连接与工具（§4）。
//
// 两表分开而不是把工具塞进连接 JSON：工具需要**逐条启停与设定风险分类**，
// 且 schema 变化要使该工具的确认票据失效——这些都以「一行一个工具」为前提。

// MCP 凭据注入方式（spec §4.1：auth_style）。
const (
	MCPAuthStyleNone   = "none"
	MCPAuthStyleHeader = "header"
	MCPAuthStyleBearer = "bearer"
)

// MCP 凭据来源（spec §4.1：auth_source，与 auth_style 正交）。
const (
	MCPAuthSourceNone   = "none"
	MCPAuthSourceEnv    = "env"
	MCPAuthSourceStored = "stored"
)

// MCP 工具风险分类（spec §4.2：由管理员确认，**不由 annotations 推导**）。
const (
	MCPToolRiskRead    = "read"
	MCPToolRiskWrite   = "write"
	MCPToolRiskUnknown = "unknown"
)

// AssistantMCPConnection 一个外部 MCP 连接（平台级，管理员配置）。
//
// 归属是**平台**而非用户：用户没有自行添加连接的路径（spec §3）。
type AssistantMCPConnection struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// Slug 工具命名空间用（`conn.<slug>.<remoteName>`）。
	// 保留字 `system` 不可用——那会与内置工具命名空间冲突。
	Slug string `gorm:"size:32;uniqueIndex;not null" json:"slug"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Endpoint 仅 origin + path（无 userinfo/query/fragment）。
	Endpoint string `gorm:"size:512;not null" json:"endpoint"`

	// AuthHeaderName 仅 auth_style=header 时生效，默认 Authorization。
	AuthHeaderName string `gorm:"size:64" json:"authHeaderName"`
	// AuthStyle 注入方式：none / header / bearer。
	AuthStyle string `gorm:"size:16;not null;default:'none'" json:"authStyle"`
	// AuthSource 凭据来源：none / env / stored。
	AuthSource string `gorm:"size:16;not null;default:'none'" json:"authSource"`
	// AuthEnv 环境变量名（auth_source=env）。**永不出现在响应**。
	AuthEnv string `gorm:"size:128" json:"-"`
	// AuthEnc 加密凭据（auth_source=stored）。**永不出现在响应**。
	AuthEnc string `gorm:"type:text" json:"-"`

	// AllowedOrigins 精确 origin + CIDR 放行（自托管目标）；空 = 默认拒绝私网。
	AllowedOrigins string `gorm:"type:text" json:"allowedOrigins"`

	Enabled bool `gorm:"not null;default:false" json:"enabled"`
	// PolicyVersion 每次策略变更递增；使旧确认失效。
	PolicyVersion int `gorm:"not null;default:1" json:"policyVersion"`

	LastProbeAt *time.Time `json:"lastProbeAt"`
	LastProbeOK *bool      `json:"lastProbeOk"`

	Version   int       `gorm:"not null;default:1" json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName 表名。
func (AssistantMCPConnection) TableName() string { return "assistant_mcp_connections" }

// AssistantMCPTool 一条从上游发现的工具。
type AssistantMCPTool struct {
	ID uint `gorm:"primaryKey" json:"id"`

	ConnectionID uint `gorm:"index;not null" json:"connectionId"`
	// RemoteName 上游原始工具名。
	RemoteName string `gorm:"size:128;not null" json:"remoteName"`
	// NamespacedName `conn.<slug>.<remoteName>`，唯一。
	NamespacedName string `gorm:"size:200;uniqueIndex;not null" json:"namespacedName"`
	// Description 上游描述（截断，**标为不可信**）。
	Description string `gorm:"type:text" json:"description"`
	InputSchema string `gorm:"type:text" json:"inputSchema"`

	// Risk 由管理员确认，不由 annotations 推导。
	Risk string `gorm:"size:16;not null;default:'unknown'" json:"risk"`
	// Enabled 管理员显式启用才可执行（连接建立后默认全部禁执行）。
	Enabled bool `gorm:"not null;default:false" json:"enabled"`
	// SchemaHash schema 变化使该工具的确认票据失效。
	SchemaHash string `gorm:"size:64" json:"schemaHash"`
	// DiscoveredAt 最近一次发现到它的时间。
	DiscoveredAt time.Time `json:"discoveredAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName 表名。
func (AssistantMCPTool) TableName() string { return "assistant_mcp_tools" }

// AssistantMCPToolTimeout 单次 MCP 工具调用的超时（spec §5.2）。
const AssistantMCPCallTimeout = 30 * time.Second

// AssistantMCPDiscoverTimeout 单页发现的超时（spec §5.2）。
const AssistantMCPDiscoverTimeout = 20 * time.Second

// AssistantMCPResponseMax 单次响应体上限（spec §5.2：1 MiB）。
const AssistantMCPResponseMax = 1 << 20

// AssistantMCPMaxTools 单连接工具数上限（spec §5.2：200，超出整次发现失败）。
const AssistantMCPMaxTools = 200
