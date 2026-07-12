package model

import "time"

// NodePMConfig 节点级包管理器与 registry 配置（FR-306，节点单例）。
// 承载 corepack 激活的 PM 偏好与多 registry（默认源 + @scope 域源 + 可选凭据）；
// registries 以 JSON 数组存 []PMRegistry；token 入库明文（节点级配置密级同 proxy.url），
// 出参与日志脱敏。被 FR-307 全局包管理 / FR-308 bot-worker 依赖装全局消费。
type NodePMConfig struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	NodeID uint `gorm:"not null;uniqueIndex" json:"nodeId"`
	// PM 包管理器偏好：npm（默认）/ pnpm / yarn。pnpm/yarn 经 corepack enable 激活。
	PM string `gorm:"type:varchar(16);not null;default:npm" json:"pm"`
	// Registries JSON 数组（[]PMRegistry）：默认源用 scope="" 那条，其余为 @scope 域源。
	Registries string `gorm:"type:text" json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// PMRegistry 单条 registry 配置。scope 为空表示默认 registry；非空为 @scope 域源（不含 @ 前缀由前端约定）。
// Token 为可选凭据（写入 .npmrc 的 _authToken），API 出参脱敏。
type PMRegistry struct {
	Name  string `json:"name"`  // 展示名（可选）
	URL   string `json:"url"`   // http(s) registry 地址
	Scope string `json:"scope"` // 空=默认源；非空=@<scope> 域源
	Token string `json:"token"` // 可选凭据（出参脱敏）
}
