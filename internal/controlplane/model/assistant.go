package model

import "time"

// AI 供应商协议枚举（FR-514）。协议决定流式适配分支，不从显示名或模型名推断。
const (
	// AssistantProtocolOpenAIChat OpenAI 兼容 Chat Completions。
	AssistantProtocolOpenAIChat = "openai-chat"
	// AssistantProtocolOpenAIResponses OpenAI Responses API。
	AssistantProtocolOpenAIResponses = "openai-responses"
	// AssistantProtocolAnthropic Anthropic Messages API。
	AssistantProtocolAnthropic = "anthropic"
)

// AI 供应商出站代理模式（FR-539）。
const (
	// AssistantProxyDirect 直连，**不**读取进程环境代理。
	AssistantProxyDirect = "direct"
	// AssistantProxyEnv 跟随进程环境（HTTP_PROXY / HTTPS_PROXY / NO_PROXY）。
	AssistantProxyEnv = "env"
	// AssistantProxyCustom 使用供应商自定义代理地址。
	AssistantProxyCustom = "custom"
)

// AI 供应商凭据模式（FR-514）。
const (
	// AssistantCredentialNone 该供应商无需凭据（如本机自托管的无鉴权网关）。
	AssistantCredentialNone = "none"
	// AssistantCredentialEnv 从进程环境变量读取，库里只存变量名。
	AssistantCredentialEnv = "env"
	// AssistantCredentialStored 面板直填，密文落库（AES-256-GCM 可逆）。
	AssistantCredentialStored = "stored"
)

// AssistantProvider 模型供应商（FR-514）。
//
// 为什么单独建表而不复用 PlatformSetting 白名单字符串键：设置项是「一个键一个标量」的
// 覆盖层，多供应商 / 多模型 / 每项带凭据与版本的关系型数据塞进去只能序列化成 JSON，
// 等于把约束（唯一名、默认模型、乐观并发）全部让给应用层自己守。
//
// 凭据只以三种形态存在：无凭据 / 环境变量名 / 密文。明文与密文都**永不**出现在
// 任何 API 响应里（见 router 层 DTO），列表只回「是否已配置」。
type AssistantProvider struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Name 显示名，全平台唯一（不含软删——本表硬删除，见 spec §4）。
	Name string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	// Protocol 取值见 AssistantProtocol* 常量。
	Protocol string `gorm:"size:32;not null" json:"protocol"`
	// BaseURL 仅 origin（可含固定路径前缀）。不含 userinfo / query / fragment，
	// 出站前由受保护客户端再校验一次，配置阶段先挡一道。
	BaseURL string `gorm:"size:512;not null" json:"baseUrl"`
	// CredentialMode 取值见 AssistantCredential* 常量。
	CredentialMode string `gorm:"size:16;not null" json:"credentialMode"`
	// CredentialEnv 仅 CredentialMode=env 时有值，形如 JIANMANAGER_ASSISTANT_*。
	CredentialEnv string `gorm:"size:128" json:"credentialEnv"`
	// CredentialEnc AES-256-GCM 密文（base64(nonce‖密文)），仅 stored 模式有值。
	// json:"-" 是第二道防线：即使有人误把 model 直接回给前端也不会泄漏。
	CredentialEnc string `gorm:"size:1024" json:"-"`
	// CredentialSetAt 仅用于「已配置」标志与展示，不代表凭据当前可用。
	CredentialSetAt *time.Time `json:"credentialSetAt"`
	// ProxyMode 出站代理模式（FR-539）：direct 直连 / env 跟随进程环境 / custom 自定义。
	//
	// **默认 direct** 是刻意的：不回退到 `http.ProxyFromEnvironment`。
	// 否则「本机为了别的用途设了全局 HTTPS_PROXY」会悄悄把模型出站改道，
	// 而管理员在面板上看到的是「直连」——出了问题无从判断流量去了哪。
	ProxyMode string `gorm:"size:16;not null;default:direct" json:"proxyMode"`
	// ProxyURL 仅 ProxyMode=custom 时有值，形如 http://host:port 或 socks5://host:port。
	// 允许含 userinfo（代理常需口令），但该字段**永不出现在响应里**（见 router DTO）。
	ProxyURL string `gorm:"size:512" json:"-"`
	// ProxyHasAuth 代理地址是否带 userinfo，供前端显示「已配置代理凭据」而不回显。
	ProxyHasAuth bool `gorm:"not null;default:false" json:"proxyHasAuth"`
	Enabled      bool `gorm:"not null;default:true" json:"enabled"`
	// Version 乐观并发：写入须带当前 version，不匹配回 409，避免两个管理员
	// 同时编辑时后写覆盖前写且无人察觉。
	Version   int       `gorm:"not null;default:1" json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName 固定表名，避免 GORM 对缩写做意外蛇形化。
func (AssistantProvider) TableName() string { return "assistant_providers" }

// AssistantModel 某个供应商下的具体模型（FR-514）。
//
// ModelID 存上游原始标识（原样字符串），平台不硬编码任何品牌型号——型号会变，
// 而「哪个供应商的哪个 ID 可用」是管理员在配置时确认的事实。
type AssistantModel struct {
	ID         uint `gorm:"primaryKey" json:"id"`
	ProviderID uint `gorm:"not null;uniqueIndex:idx_assistant_model_provider_model" json:"providerId"`
	// ModelID 上游原始模型标识，同一供应商下唯一（跨供应商允许同名——
	// 不同网关提供同名模型是常态，全局唯一会挡住「A 网关和 B 网关都有 gpt-4o」）。
	ModelID     string `gorm:"size:256;not null;uniqueIndex:idx_assistant_model_provider_model" json:"modelId"`
	DisplayName string `gorm:"size:128" json:"displayName"`
	// Streaming / Tools 是显式能力声明，不从名称猜测；未声明的能力不投喂给运行器。
	Streaming bool `gorm:"not null;default:true" json:"streaming"`
	Tools     bool `gorm:"not null;default:false" json:"tools"`
	// ContextWindow / MaxOutputTokens 可空：未知就不填，前端显示「未声明」而不是 0。
	ContextWindow   *int `json:"contextWindow"`
	MaxOutputTokens *int `json:"maxOutputTokens"`
	// ReasoningEffort 思考强度（FR-541 §3.2）：minimal / low / medium / high。
	// 空 = 不指定，由上游默认。**不从模型名推断**——同名模型在不同网关上的支持度不同。
	ReasoningEffort string    `gorm:"size:16" json:"reasoningEffort"`
	Enabled         bool      `gorm:"not null;default:true" json:"enabled"`
	Version         int       `gorm:"not null;default:1" json:"version"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// TableName 固定表名。
func (AssistantModel) TableName() string { return "assistant_models" }

// AssistantSetting AI 助手全局设置，全表单行（ID 恒为 1）。
//
// 默认模型用「单行里的可空外键」而不是每行模型的 isDefault 布尔：后者允许多行同时为真，
// 需要在每次写入时靠应用层扫全表来维持不变式；单行指针天然只能有一个值，
// 且换默认可以在同一事务里与启停同生共死。
type AssistantSetting struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// DefaultModelID 可空：未配置默认模型是合法状态（助手应给出明确引导而非报错）。
	DefaultModelID *uint `json:"defaultModelId"`
	// AllowPrivateCIDRs 出站准入放行的私网段（JSON 数组文本，空 = 一律禁止非公网目标）。
	//
	// 为什么存在设置表而不是配置文件：这是**部署期与运行期都要能改**的策略——
	// 自托管模型网关的网段常随环境调整，要求改文件+重启会让运维绕过它（比如直接把
	// 网关暴露到公网）。风险由「谁能改」控制：只有平台管理员能写，且写入必留审计。
	//
	// 用 TEXT + JSON 而不是独立表：它只是单行设置里的一个字段，
	// 独立表会让「读设置」多一次 join 而毫无收益（条数是个位数）。
	//
	// 显式 column：GORM 的默认蛇形转换把 `CIDRs` 拆成 `c_id_rs`
	// （缩写与复数被逐字母拆开），得到 `allow_private_c_id_rs` 这种没法读的列名。
	AllowPrivateCIDRs string `gorm:"column:allow_private_cidrs;type:text" json:"allowPrivateCidrs"`
	// SystemPrompt 自定义系统提示词（FR-541 §3.1）。空 = 只用内置基线。
	//
	// 组装顺序固定为「内置基线 + 自定义 + 已启用技能」，三层各自可为空。
	// 自定义内容**不得**被当作可信指令去放开工具权限——它只改「说什么」，
	// 不改「能做什么」（写操作仍逐个确认）。
	SystemPrompt string `gorm:"column:system_prompt;type:text" json:"systemPrompt"`
	// BuiltinToolsEnabled 是否向助手暴露平台自带工具（FR-540 §4.2）。
	//
	// 用 *bool 而不是 bool：零值 bool 会把「未配置」读成 false，
	// 而默认语义是**启用**。指针让「没配置」与「显式关闭」可区分。
	BuiltinToolsEnabled *bool     `gorm:"column:builtin_tools_enabled" json:"builtinToolsEnabled"`
	Version             int       `gorm:"not null;default:1" json:"version"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// TableName 固定表名。
func (AssistantSetting) TableName() string { return "assistant_settings" }
