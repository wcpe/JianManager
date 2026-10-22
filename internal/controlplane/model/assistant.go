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
	Enabled         bool       `gorm:"not null;default:true" json:"enabled"`
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
	ContextWindow   *int      `json:"contextWindow"`
	MaxOutputTokens *int      `json:"maxOutputTokens"`
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
	DefaultModelID *uint     `json:"defaultModelId"`
	Version        int       `gorm:"not null;default:1" json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// TableName 固定表名。
func (AssistantSetting) TableName() string { return "assistant_settings" }
