package service

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// AI 助手配置层的业务错误（FR-514）。router 层把它们映射成 HTTP 状态码。
var (
	// ErrAssistantNotFound 供应商/模型不存在。
	ErrAssistantNotFound = errors.New("对象不存在")
	// ErrAssistantConflict 名称或模型标识重复，或版本冲突。
	ErrAssistantConflict = errors.New("名称或标识已存在，或数据已被他人修改")
	// ErrAssistantInvalid 参数不合规（协议、地址、名称长度等）。
	ErrAssistantInvalid = errors.New("参数不合规")
	// ErrAssistantContextTooLarge 页面上下文整体超限（FR-518 §6）：
	// 与「某项被剔除」区分开——那是裁剪后继续，这是拒收本次请求。
	ErrAssistantContextTooLarge = errors.New("页面上下文过大")
	// ErrAssistantForbidden 执行期权限不足（FR-516 §5.1）。
	// 与「不存在」共用 404 语义会让「谁有这个资源」变成可探测的侧信道，
	// 故工具执行层面单独区分为 403。
	ErrAssistantForbidden = errors.New("无权限执行该操作")
	// FR-516 §7 确认票据错误族。
	ErrAssistantConfirmExpired  = errors.New("确认已过期")
	ErrAssistantConfirmConsumed = errors.New("确认已被执行")
	ErrAssistantConfirmStale    = errors.New("确认已失效")
	// ErrAssistantEncryptorMissing 未配置加密器却试图保存密文凭据。
	ErrAssistantEncryptorMissing = errors.New("未配置加密器，无法保存明文凭据")
	// ErrAssistantInUse 删除被引用的对象。
	ErrAssistantInUse = errors.New("对象正被引用，无法删除")
)

// assistantCredentialEnvPattern 允许的环境变量名（FR-514 spec §4）。
//
// 收窄到专用前缀只查进程环境：既有 QQ 那套 resolveEnvRef 还含"读文件回落"，
// 直接复用会让「一个环境变量名」实际变成「可读取数据根下任意文件」的入口。
var assistantCredentialEnvPattern = regexp.MustCompile(`^JIANMANAGER_ASSISTANT_[A-Z0-9_]{1,64}$`)

// AssistantService AI 供应商/模型配置服务（FR-514，见 ADR-105）。
type AssistantService struct {
	db  *gorm.DB
	enc *KeyEncryptor
	// egress 受保护出站工厂；配置阶段用它校验 BaseURL，探测/测试阶段用它发请求。
	// 用具体类型而非接口：它没有需要被 mock 的多实现，测试直接用真实工厂 + httptest。
	egress *httpclient.Guard
	// egressFactory 用「当前放行段」重建出站工厂；由装配层注入。
	//
	// 为什么需要工厂而不是直接改 egress 的字段：AllowPrivate 在 NewGuard 里被编译进
	// Transport 的拨号器，没有可变入口。与其给 Guard 加一个「运行期改策略」的后门
	// （那会让「谁能改准入」变成两处真相），不如让 service 持有重建能力。
	// 为 nil 表示不可重建（测试或未装配），此时写设置仍成功但不即时生效，
	// 由调用方在启动时完成装配——这比静默假装生效安全。
	egressFactory func(allowPrivate []netip.Prefix) *httpclient.Guard
	// allowInsecure 装配层是否放开了 http 明文（仅回显用，判定真源在 Guard 里）。
	allowInsecure bool
}

// NewAssistantService 构造服务。
//
// enc 允许为 nil（未配置加密）：此时仍可保存「无凭据」与「环境变量引用」两类供应商，
// 只有「密文落库」被拒绝——这比让整个 AI 功能因缺加密器而不可用更合理，也更诚实。
func NewAssistantService(db *gorm.DB, enc *KeyEncryptor) *AssistantService {
	return &AssistantService{db: db, enc: enc}
}

// SetEgress 注入受保护出站工厂。未注入时配置校验与探测都会明确报错。
func (s *AssistantService) SetEgress(g *httpclient.Guard) { s.egress = g }

// SetEgressFactory 注入「按放行段重建出站工厂」的能力（FR-514 私网放行）。
//
// 装配层传入的工厂应当固化其余参数（超时、代理、AllowInsecure），只让放行段变化。
func (s *AssistantService) SetEgressFactory(f func(allowPrivate []netip.Prefix) *httpclient.Guard) {
	s.egressFactory = f
}

// ── 供应商 ─────────────────────────────────────────────────────────────

// SaveProviderParams 供应商保存参数。
type SaveProviderParams struct {
	Name     string
	Protocol string
	BaseURL  string
	Enabled  bool
	// CredentialMode 三态：none / env / stored。
	CredentialMode string
	// CredentialEnv 仅 env 模式使用。
	CredentialEnv string
	// CredentialSecret 仅 stored 模式使用：非空表示替换；空表示保留原值。
	CredentialSecret string
	// ClearCredential 显式清除已保存的密文凭据。
	ClearCredential bool
	// ProxyMode 出站代理模式（FR-539）：direct / env / custom。
	// 空串表示「不改动」（编辑态）或使用默认 direct（创建态）。
	ProxyMode string
	// ProxyURL 仅 custom 模式使用。含 userinfo 时整串落库且**永不回显**。
	ProxyURL string
	// Version 乐观并发；创建时忽略。
	Version int
}

// ProviderView 供应商对外视图（**不含**任何凭据明文或密文）。
type ProviderView struct {
	ID             uint   `json:"id"`
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	BaseURL        string `json:"baseUrl"`
	CredentialMode string `json:"credentialMode"`
	// CredentialEnv 回显变量名（不是值）——变量名本身不敏感，且管理员需要知道引用了哪个。
	CredentialEnv string `json:"credentialEnv,omitempty"`
	// HasCredential 是否已配置可用凭据（stored 有密文 / env 有变量名）。
	HasCredential bool `json:"hasCredential"`
	// ProxyMode 出站代理模式（FR-539）。
	ProxyMode string `json:"proxyMode"`
	// ProxyHasAuth 代理地址是否带 userinfo：只回布尔，**绝不回显地址与口令**。
	ProxyHasAuth bool   `json:"proxyHasAuth"`
	Enabled      bool   `json:"enabled"`
	Version      int    `json:"version"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

// toProviderView 转视图，**刻意不带** CredentialEnc。
func toProviderView(p model.AssistantProvider) ProviderView {
	return ProviderView{
		ID:             p.ID,
		Name:           p.Name,
		Protocol:       p.Protocol,
		BaseURL:        p.BaseURL,
		CredentialMode: p.CredentialMode,
		CredentialEnv:  p.CredentialEnv,
		HasCredential:  hasCredential(p),
		ProxyMode:      normalizeProxyMode(p.ProxyMode),
		ProxyHasAuth:   p.ProxyHasAuth,
		Enabled:        p.Enabled,
		Version:        p.Version,
		CreatedAt:      p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:      p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// hasCredential 判断当前配置下是否已有可用凭据。
func hasCredential(p model.AssistantProvider) bool {
	switch p.CredentialMode {
	case model.AssistantCredentialStored:
		return strings.TrimSpace(p.CredentialEnc) != ""
	case model.AssistantCredentialEnv:
		return strings.TrimSpace(p.CredentialEnv) != ""
	default:
		return false
	}
}

// ListProviders 全量供应商（按 ID 升序，内置无特殊行）。
func (s *AssistantService) ListProviders() ([]ProviderView, error) {
	var rows []model.AssistantProvider
	if err := s.db.Order("id asc").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询供应商失败: %w", err)
	}
	out := make([]ProviderView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProviderView(r))
	}
	return out, nil
}

// CreateProvider 创建供应商。
func (s *AssistantService) CreateProvider(p SaveProviderParams) (*ProviderView, error) {
	if err := s.validateProviderParams(&p, false); err != nil {
		return nil, err
	}
	row := model.AssistantProvider{
		Name:           p.Name,
		Protocol:       p.Protocol,
		BaseURL:        p.BaseURL,
		CredentialMode: p.CredentialMode,
		CredentialEnv:  p.CredentialEnv,
		Enabled:        p.Enabled,
		Version:        1,
	}
	if err := s.applyCredential(&row, p); err != nil {
		return nil, err
	}
	if err := applyProviderProxy(&row, p); err != nil {
		return nil, err
	}
	if err := s.db.Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAssistantConflict
		}
		return nil, fmt.Errorf("创建供应商失败: %w", err)
	}
	view := toProviderView(row)
	return &view, nil
}

// UpdateProvider 更新供应商（带乐观并发）。
func (s *AssistantService) UpdateProvider(id uint, p SaveProviderParams) (*ProviderView, error) {
	if err := s.validateProviderParams(&p, true); err != nil {
		return nil, err
	}
	var row model.AssistantProvider
	if err := s.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAssistantNotFound
		}
		return nil, fmt.Errorf("查询供应商失败: %w", err)
	}
	if p.Version != 0 && p.Version != row.Version {
		return nil, ErrAssistantConflict
	}

	row.Name = p.Name
	row.Protocol = p.Protocol
	row.BaseURL = p.BaseURL
	row.CredentialMode = p.CredentialMode
	row.CredentialEnv = p.CredentialEnv
	row.Enabled = p.Enabled
	if err := s.applyCredential(&row, p); err != nil {
		return nil, err
	}
	if err := applyProviderProxy(&row, p); err != nil {
		return nil, err
	}
	row.Version++

	// 带版本条件更新：并发写时后写者失败而不是覆盖。
	res := s.db.Model(&model.AssistantProvider{}).
		Where("id = ? AND version = ?", id, p.Version).
		Updates(map[string]any{
			"name":              row.Name,
			"protocol":          row.Protocol,
			"base_url":          row.BaseURL,
			"credential_mode":   row.CredentialMode,
			"credential_env":    row.CredentialEnv,
			"credential_enc":    row.CredentialEnc,
			"credential_set_at": row.CredentialSetAt,
			"enabled":           row.Enabled,
			"proxy_mode":        row.ProxyMode,
			"proxy_url":         row.ProxyURL,
			"proxy_has_auth":    row.ProxyHasAuth,
			"version":           row.Version,
		})
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return nil, ErrAssistantConflict
		}
		return nil, fmt.Errorf("更新供应商失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, ErrAssistantConflict
	}
	updated, err := s.GetProvider(id)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// GetProvider 取单个供应商视图。
func (s *AssistantService) GetProvider(id uint) (*ProviderView, error) {
	var row model.AssistantProvider
	if err := s.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAssistantNotFound
		}
		return nil, fmt.Errorf("查询供应商失败: %w", err)
	}
	view := toProviderView(row)
	return &view, nil
}

// DeleteProvider 删除供应商。存在子模型时拒绝（不级联删除，避免静默丢配置）。
func (s *AssistantService) DeleteProvider(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.AssistantModel{}).Where("provider_id = ?", id).Count(&count).Error; err != nil {
			return fmt.Errorf("统计子模型失败: %w", err)
		}
		if count > 0 {
			return fmt.Errorf("%w: 该供应商下还有 %d 个模型", ErrAssistantInUse, count)
		}
		res := tx.Delete(&model.AssistantProvider{}, id)
		if res.Error != nil {
			return fmt.Errorf("删除供应商失败: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrAssistantNotFound
		}
		return nil
	})
}

// validateProviderParams 校验并规范化供应商参数。
func (s *AssistantService) validateProviderParams(p *SaveProviderParams, isUpdate bool) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len([]rune(p.Name)) > 64 {
		return fmt.Errorf("%w: 名称需为 1-64 个字符", ErrAssistantInvalid)
	}
	if !ai.ProtocolSupported(p.Protocol) {
		return fmt.Errorf("%w: 不支持的协议 %q", ErrAssistantInvalid, p.Protocol)
	}
	if s.egress == nil {
		return fmt.Errorf("%w: 出站准入未初始化", ErrAssistantInvalid)
	}
	parsed, err := s.egress.ValidateBaseURL(p.BaseURL)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrAssistantInvalid, err.Error())
	}
	p.BaseURL = parsed.String()

	switch p.CredentialMode {
	case model.AssistantCredentialNone:
		p.CredentialEnv = ""
		if !p.ClearCredential && strings.TrimSpace(p.CredentialSecret) != "" {
			return fmt.Errorf("%w: 凭据模式为 none 时不得提供密钥", ErrAssistantInvalid)
		}
	case model.AssistantCredentialEnv:
		p.CredentialEnv = strings.TrimSpace(p.CredentialEnv)
		if !assistantCredentialEnvPattern.MatchString(p.CredentialEnv) {
			return fmt.Errorf("%w: 环境变量名须形如 JIANMANAGER_ASSISTANT_XXX", ErrAssistantInvalid)
		}
	case model.AssistantCredentialStored:
		p.CredentialEnv = ""
		// 创建时必须给密钥；更新时留空表示保留原密文。
		if !isUpdate && strings.TrimSpace(p.CredentialSecret) == "" {
			return fmt.Errorf("%w: 密文模式创建时必须提供密钥", ErrAssistantInvalid)
		}
	case "":
		return fmt.Errorf("%w: 缺少凭据模式", ErrAssistantInvalid)
	default:
		return fmt.Errorf("%w: 未知凭据模式 %q", ErrAssistantInvalid, p.CredentialMode)
	}
	return nil
}

// applyCredential 按三态语义写入/保留/清除凭据。
func (s *AssistantService) applyCredential(row *model.AssistantProvider, p SaveProviderParams) error {
	// 显式清除：清空密文与时间戳，并把模式降为 none（否则会留下「stored 但无密文」的悬空态）。
	if p.ClearCredential {
		row.CredentialEnc = ""
		row.CredentialSetAt = nil
		if row.CredentialMode == model.AssistantCredentialStored {
			row.CredentialMode = model.AssistantCredentialNone
		}
		return nil
	}
	// 切到非 stored 模式：旧密文不再使用，一并清掉避免以后切回来时意外复用。
	if p.CredentialMode != model.AssistantCredentialStored {
		row.CredentialEnc = ""
		row.CredentialSetAt = nil
		return nil
	}
	// stored 模式且未提供新密钥：保留原密文（更新路径）。
	if strings.TrimSpace(p.CredentialSecret) == "" {
		return nil
	}
	// 关键守卫：nil 加密器的 Encrypt 会返回空串且不报错，不拦就等于静默丢凭据。
	if s.enc == nil {
		return ErrAssistantEncryptorMissing
	}
	enc, err := s.enc.Encrypt(p.CredentialSecret)
	if err != nil {
		return fmt.Errorf("加密凭据失败: %w", err)
	}
	if enc == "" {
		// 双保险：即使将来 Encrypt 语义变化，也不允许把空串当密文存下去。
		return ErrAssistantEncryptorMissing
	}
	row.CredentialEnc = enc
	now := time.Now()
	row.CredentialSetAt = &now
	return nil
}

// ── 模型 ───────────────────────────────────────────────────────────────

// SaveModelParams 模型保存参数。
type SaveModelParams struct {
	ProviderID      uint
	ModelID         string
	DisplayName     string
	Streaming       bool
	Tools           bool
	ContextWindow   *int
	MaxOutputTokens *int
	Enabled         bool
	Version         int
}

// ModelView 模型对外视图。
type ModelView struct {
	ID              uint   `json:"id"`
	ProviderID      uint   `json:"providerId"`
	ProviderName    string `json:"providerName"`
	Protocol        string `json:"protocol"`
	ModelID         string `json:"modelId"`
	DisplayName     string `json:"displayName"`
	Streaming       bool   `json:"streaming"`
	Tools           bool   `json:"tools"`
	ContextWindow   *int   `json:"contextWindow"`
	MaxOutputTokens *int   `json:"maxOutputTokens"`
	Enabled         bool   `json:"enabled"`
	// ProviderEnabled 便于前端提示「模型开启但供应商停用」的不可用态。
	ProviderEnabled bool `json:"providerEnabled"`
	Version         int  `json:"version"`
}

// ListModels 全量模型（回填供应商名与协议，避免前端为显示一个名字再拉全量供应商）。
func (s *AssistantService) ListModels() ([]ModelView, error) {
	var rows []model.AssistantModel
	if err := s.db.Order("provider_id asc, id asc").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询模型失败: %w", err)
	}
	var providers []model.AssistantProvider
	if err := s.db.Find(&providers).Error; err != nil {
		return nil, fmt.Errorf("查询供应商失败: %w", err)
	}
	byID := make(map[uint]model.AssistantProvider, len(providers))
	for _, p := range providers {
		byID[p.ID] = p
	}
	out := make([]ModelView, 0, len(rows))
	for _, r := range rows {
		p := byID[r.ProviderID]
		out = append(out, ModelView{
			ID:              r.ID,
			ProviderID:      r.ProviderID,
			ProviderName:    p.Name,
			Protocol:        p.Protocol,
			ModelID:         r.ModelID,
			DisplayName:     r.DisplayName,
			Streaming:       r.Streaming,
			Tools:           r.Tools,
			ContextWindow:   r.ContextWindow,
			MaxOutputTokens: r.MaxOutputTokens,
			Enabled:         r.Enabled,
			ProviderEnabled: p.Enabled,
			Version:         r.Version,
		})
	}
	return out, nil
}

// CreateModel 创建模型。
func (s *AssistantService) CreateModel(p SaveModelParams) (*ModelView, error) {
	if err := s.validateModelParams(&p); err != nil {
		return nil, err
	}
	row := model.AssistantModel{
		ProviderID:      p.ProviderID,
		ModelID:         p.ModelID,
		DisplayName:     p.DisplayName,
		Streaming:       p.Streaming,
		Tools:           p.Tools,
		ContextWindow:   p.ContextWindow,
		MaxOutputTokens: p.MaxOutputTokens,
		Enabled:         p.Enabled,
		Version:         1,
	}
	if err := s.db.Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAssistantConflict
		}
		return nil, fmt.Errorf("创建模型失败: %w", err)
	}
	views, err := s.ListModels()
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		if v.ID == row.ID {
			return &v, nil
		}
	}
	return nil, ErrAssistantNotFound
}

// UpdateModel 更新模型（带乐观并发）。
func (s *AssistantService) UpdateModel(id uint, p SaveModelParams) (*ModelView, error) {
	if err := s.validateModelParams(&p); err != nil {
		return nil, err
	}
	var row model.AssistantModel
	if err := s.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAssistantNotFound
		}
		return nil, fmt.Errorf("查询模型失败: %w", err)
	}
	if p.Version != 0 && p.Version != row.Version {
		return nil, ErrAssistantConflict
	}
	res := s.db.Model(&model.AssistantModel{}).
		Where("id = ? AND version = ?", id, p.Version).
		Updates(map[string]any{
			"provider_id":       p.ProviderID,
			"model_id":          p.ModelID,
			"display_name":      p.DisplayName,
			"streaming":         p.Streaming,
			"tools":             p.Tools,
			"context_window":    p.ContextWindow,
			"max_output_tokens": p.MaxOutputTokens,
			"enabled":           p.Enabled,
			"version":           row.Version + 1,
		})
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return nil, ErrAssistantConflict
		}
		return nil, fmt.Errorf("更新模型失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, ErrAssistantConflict
	}
	views, err := s.ListModels()
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		if v.ID == id {
			return &v, nil
		}
	}
	return nil, ErrAssistantNotFound
}

// DeleteModel 删除模型。若它是当前默认模型则一并清空默认（同事务，不留悬空引用）。
func (s *AssistantService) DeleteModel(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&model.AssistantModel{}, id)
		if res.Error != nil {
			return fmt.Errorf("删除模型失败: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrAssistantNotFound
		}
		var setting model.AssistantSetting
		if err := tx.First(&setting, 1).Error; err == nil {
			if setting.DefaultModelID != nil && *setting.DefaultModelID == id {
				if uerr := tx.Model(&model.AssistantSetting{}).Where("id = ?", 1).
					Updates(map[string]any{"default_model_id": nil, "version": setting.Version + 1}).Error; uerr != nil {
					return fmt.Errorf("清空默认模型失败: %w", uerr)
				}
			}
		}
		return nil
	})
}

// validateModelParams 校验模型参数。
func (s *AssistantService) validateModelParams(p *SaveModelParams) error {
	p.ModelID = strings.TrimSpace(p.ModelID)
	if p.ModelID == "" || len(p.ModelID) > 256 {
		return fmt.Errorf("%w: 模型标识需为 1-256 个字符", ErrAssistantInvalid)
	}
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if len([]rune(p.DisplayName)) > 128 {
		return fmt.Errorf("%w: 显示名最长 128 个字符", ErrAssistantInvalid)
	}
	if p.ContextWindow != nil && *p.ContextWindow <= 0 {
		return fmt.Errorf("%w: 上下文窗口需为正数或留空", ErrAssistantInvalid)
	}
	if p.MaxOutputTokens != nil && *p.MaxOutputTokens <= 0 {
		return fmt.Errorf("%w: 输出上限需为正数或留空", ErrAssistantInvalid)
	}
	var count int64
	if err := s.db.Model(&model.AssistantProvider{}).Where("id = ?", p.ProviderID).Count(&count).Error; err != nil {
		return fmt.Errorf("查询供应商失败: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("%w: 供应商不存在", ErrAssistantInvalid)
	}
	return nil
}

// ── 设置与可达模型 ──────────────────────────────────────────────────────

// EnsureSetting 幂等 seed 单行设置（CP 启动时调用一次）。
func (s *AssistantService) EnsureSetting() error {
	var count int64
	if err := s.db.Model(&model.AssistantSetting{}).Where("id = ?", 1).Count(&count).Error; err != nil {
		return fmt.Errorf("查询助手设置失败: %w", err)
	}
	if count > 0 {
		return nil
	}
	row := model.AssistantSetting{ID: 1, Version: 1}
	if err := s.db.Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil // 并发启动时另一方已创建。
		}
		return fmt.Errorf("创建设置失败: %w", err)
	}
	return nil
}

// AvailableModel 普通用户可见的模型摘要（**不含端点与凭据字段**）。
//
// 关于 ProviderName：早先这里刻意不带供应商名（理由是「普通用户不需要知道有几个供应商」），
// 但 FR-519 §5.1 要求界面上明示「相关数据将发送给 <供应商>」——这是**用户知情权**，
// 不是暴露组织信息：用户要知道自己的提问发给谁。因此补上名称，
// 仍然**不含** baseUrl / 凭据引用 / 密文（那些才是真正的敏感面）。
type AvailableModel struct {
	ID uint `json:"id"`
	// ProviderName 供应商显示名（仅名称，不含端点与凭据）。
	ProviderName string `json:"providerName"`
	ModelID      string `json:"modelId"`
	DisplayName  string `json:"displayName"`
	Protocol     string `json:"protocol"`
	Streaming    bool   `json:"streaming"`
	Tools        bool   `json:"tools"`
}

// AvailableModels 返回所有登录用户可用的模型摘要与默认模型 ID。
//
// 只列「模型启用且供应商启用」的行；defaultModelId 仅在默认模型确实可用时才给出，
// 否则回 null 让前端展示「未配置默认模型」而不是指向一个不可用的模型。
func (s *AssistantService) AvailableModels() ([]AvailableModel, *uint, error) {
	var rows []model.AssistantModel
	if err := s.db.Where("enabled = ?", true).Order("id asc").Find(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("查询模型失败: %w", err)
	}
	var providers []model.AssistantProvider
	if err := s.db.Find(&providers).Error; err != nil {
		return nil, nil, fmt.Errorf("查询供应商失败: %w", err)
	}
	byID := make(map[uint]model.AssistantProvider, len(providers))
	for _, p := range providers {
		byID[p.ID] = p
	}
	out := make([]AvailableModel, 0, len(rows))
	available := make(map[uint]bool, len(rows))
	for _, r := range rows {
		p, ok := byID[r.ProviderID]
		if !ok || !p.Enabled {
			continue
		}
		available[r.ID] = true
		out = append(out, AvailableModel{
			ID:           r.ID,
			ProviderName: p.Name,
			ModelID:      r.ModelID,
			DisplayName:  r.DisplayName,
			Protocol:     p.Protocol,
			Streaming:    r.Streaming,
			Tools:        r.Tools,
		})
	}
	var setting model.AssistantSetting
	var defaultID *uint
	if err := s.db.First(&setting, 1).Error; err == nil {
		if setting.DefaultModelID != nil && available[*setting.DefaultModelID] {
			defaultID = setting.DefaultModelID
		}
	}
	return out, defaultID, nil
}

// normalizeProxyMode 归一化代理模式（空值按 direct）。
//
// 默认 direct 是刻意的（FR-539）：不回退到 `ProxyFromEnvironment`，
// 否则「本机为别的用途设了全局代理」会悄悄把模型出站改道，
// 而管理员在面板上看到的是「直连」。
func normalizeProxyMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case model.AssistantProxyEnv:
		return model.AssistantProxyEnv
	case model.AssistantProxyCustom:
		return model.AssistantProxyCustom
	default:
		return model.AssistantProxyDirect
	}
}

// applyProviderProxy 校验并把代理配置写到行上。
//
// ## 校验为什么必须在保存期
//
// 代理地址是出站请求的前置条件：写错一个 scheme 会让该供应商的所有调用
// 在「拨号阶段」失败，而错误表现是笼统的「连不上上游」——排查会先怀疑密钥与网络。
// 在保存期挡住，错误就落在「填错了」这个真实原因上。
//
// ## 为什么不校验可达性
//
// 保存期只判语法（scheme/host/port）。可达性是运行期事实，
// 在这里探测会让「保存配置」变成一次网络请求，且代理可能只在特定网络下可达。
func applyProviderProxy(row *model.AssistantProvider, p SaveProviderParams) error {
	// 空串语义分两种：
	//   - 编辑态（row.ID != 0）：不改动，保留库里原值——否则「只想改名字」会顺手把代理清掉；
	//   - 创建态（row.ID == 0）：落到默认 direct（**不是**空串，否则视图会回一个空模式）。
	if strings.TrimSpace(p.ProxyMode) == "" {
		if row.ID != 0 {
			return nil
		}
		row.ProxyMode = model.AssistantProxyDirect
		row.ProxyURL = ""
		row.ProxyHasAuth = false
		return nil
	}
	mode := normalizeProxyMode(p.ProxyMode)
	row.ProxyMode = mode

	if mode != model.AssistantProxyCustom {
		// 非 custom 模式必须清空地址：否则切回 direct 后旧地址仍在库里，
		// 将来误切回 custom 会静默启用一个早已废弃的代理。
		row.ProxyURL = ""
		row.ProxyHasAuth = false
		return nil
	}

	raw := strings.TrimSpace(p.ProxyURL)
	if raw == "" {
		// custom 但地址为空：保留原值（编辑态只改别的字段时不该要求重填地址）。
		if row.ProxyURL != "" {
			row.ProxyMode = model.AssistantProxyCustom
			return nil
		}
		return fmt.Errorf("%w: 代理模式为 custom 时必须填写代理地址", ErrAssistantInvalid)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: 代理地址不合法（需形如 http://host:port 或 socks5://host:port）", ErrAssistantInvalid)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5":
	default:
		return fmt.Errorf("%w: 不支持的代理协议 %q（仅 http / https / socks5）", ErrAssistantInvalid, u.Scheme)
	}
	row.ProxyURL = raw
	row.ProxyHasAuth = u.User != nil
	return nil
}
