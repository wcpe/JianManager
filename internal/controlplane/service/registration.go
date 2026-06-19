package service

import (
	"errors"
	"fmt"
	"regexp"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// 注册关系相关错误（FR-032）。
var (
	ErrProxyNotFound        = errors.New("代理实例不存在")
	ErrBackendNotFound      = errors.New("后端实例不存在")
	ErrNotAProxy            = errors.New("目标实例不是代理")
	ErrNotABackend          = errors.New("目标实例不是后端")
	ErrAliasConflict        = errors.New("该代理内别名已占用")
	ErrAlreadyRegistered    = errors.New("该后端已注册进此代理")
	ErrRegistrationNotFound = errors.New("注册关系不存在")
	ErrInvalidAlias         = errors.New("别名非法：需匹配 [a-z0-9_-]{1,64}")
)

// aliasRe 校验代理内 server 别名（BungeeCord/Velocity 约定）。
var aliasRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// RegistrationSyncer 在注册关系变更后把它落到代理的实际配置（写 servers{}/priorities/forced-host
// 与 Velocity secret 下发）。FR-032 只维护关系数据，syncer 为 nil；FR-035 注入真正实现。
type RegistrationSyncer interface {
	SyncProxy(proxyID uint) error
}

// RegistrationService 管理 proxy↔backend 的 M:N 注册关系（FR-032 / ADR-007）。
type RegistrationService struct {
	db     *gorm.DB
	syncer RegistrationSyncer
}

// NewRegistrationService 创建注册服务。
func NewRegistrationService(db *gorm.DB) *RegistrationService {
	return &RegistrationService{db: db}
}

// SetSyncer 注入注册同步器（FR-035 写代理配置）。在 FR-032 下保持 nil。
func (s *RegistrationService) SetSyncer(syncer RegistrationSyncer) {
	s.syncer = syncer
}

// BackendBrief 注册列表里附带的后端实例概要。
type BackendBrief struct {
	ID         uint                 `json:"id"`
	Name       string               `json:"name"`
	Role       model.InstanceRole   `json:"role"`
	NodeID     uint                 `json:"nodeId"`
	ServerPort int                  `json:"serverPort"`
	Status     model.InstanceStatus `json:"status"`
}

// RegistrationView 注册关系 + 后端概要（列表展示用）。
type RegistrationView struct {
	model.ServerRegistration
	Backend *BackendBrief `json:"backend,omitempty"`
}

// CreateRegistrationRequest 将后端注册进代理的请求。
type CreateRegistrationRequest struct {
	BackendID  uint   `json:"backendId" binding:"required"`
	Alias      string `json:"alias"`
	Priority   *int   `json:"priority"`
	ForcedHost string `json:"forcedHost"`
	Restricted *bool  `json:"restricted"`
	Enabled    *bool  `json:"enabled"`
}

// UpdateRegistrationRequest 更新注册的可选字段。
type UpdateRegistrationRequest struct {
	Alias      *string `json:"alias"`
	Priority   *int    `json:"priority"`
	ForcedHost *string `json:"forcedHost"`
	Restricted *bool   `json:"restricted"`
	Enabled    *bool   `json:"enabled"`
}

// List 列出某代理的注册关系（按 priority 升序），附后端概要。
func (s *RegistrationService) List(proxyID uint) ([]RegistrationView, error) {
	if _, err := s.requireProxy(proxyID); err != nil {
		return nil, err
	}
	var regs []model.ServerRegistration
	if err := s.db.Where("proxy_id = ?", proxyID).Order("priority asc, id asc").Find(&regs).Error; err != nil {
		return nil, fmt.Errorf("查询注册关系失败: %w", err)
	}
	views := make([]RegistrationView, 0, len(regs))
	for _, r := range regs {
		views = append(views, RegistrationView{ServerRegistration: r, Backend: s.backendBrief(r.BackendID)})
	}
	return views, nil
}

// Create 把后端注册进代理。alias 缺省时取后端名 slug；priority 缺省追加到末尾。
// 落库后若已注入 syncer（FR-035），同步写代理配置；同步失败返回 view + 包装错误（关系已持久化）。
func (s *RegistrationService) Create(proxyID uint, req CreateRegistrationRequest) (*RegistrationView, error) {
	if _, err := s.requireProxy(proxyID); err != nil {
		return nil, err
	}
	backend, err := s.requireBackend(req.BackendID)
	if err != nil {
		return nil, err
	}

	alias := req.Alias
	if alias == "" {
		alias = slugify(backend.Name)
	}
	if !aliasRe.MatchString(alias) {
		return nil, ErrInvalidAlias
	}

	// 同一代理内别名唯一、同一后端不重复注册。
	var conflict int64
	s.db.Model(&model.ServerRegistration{}).Where("proxy_id = ? AND alias = ?", proxyID, alias).Count(&conflict)
	if conflict > 0 {
		return nil, ErrAliasConflict
	}
	var dup int64
	s.db.Model(&model.ServerRegistration{}).Where("proxy_id = ? AND backend_id = ?", proxyID, req.BackendID).Count(&dup)
	if dup > 0 {
		return nil, ErrAlreadyRegistered
	}

	priority := 0
	if req.Priority != nil {
		priority = *req.Priority
	} else {
		var existing int64
		s.db.Model(&model.ServerRegistration{}).Where("proxy_id = ?", proxyID).Count(&existing)
		priority = int(existing)
	}
	reg := model.ServerRegistration{
		ProxyID:    proxyID,
		BackendID:  req.BackendID,
		Alias:      alias,
		Priority:   priority,
		ForcedHost: req.ForcedHost,
		Restricted: boolOr(req.Restricted, false),
		Enabled:    boolOr(req.Enabled, true),
	}
	if err := s.db.Create(&reg).Error; err != nil {
		return nil, fmt.Errorf("创建注册关系失败: %w", err)
	}

	view := &RegistrationView{ServerRegistration: reg, Backend: s.backendBrief(reg.BackendID)}
	if err := s.sync(proxyID); err != nil {
		return view, fmt.Errorf("注册已保存但同步到代理配置失败: %w", err)
	}
	return view, nil
}

// Update 更新注册关系。alias 变更时校验唯一。
func (s *RegistrationService) Update(proxyID, rid uint, req UpdateRegistrationRequest) (*RegistrationView, error) {
	reg, err := s.getRegistration(proxyID, rid)
	if err != nil {
		return nil, err
	}

	if req.Alias != nil {
		alias := *req.Alias
		if !aliasRe.MatchString(alias) {
			return nil, ErrInvalidAlias
		}
		if alias != reg.Alias {
			var conflict int64
			s.db.Model(&model.ServerRegistration{}).Where("proxy_id = ? AND alias = ? AND id <> ?", proxyID, alias, rid).Count(&conflict)
			if conflict > 0 {
				return nil, ErrAliasConflict
			}
		}
		reg.Alias = alias
	}
	if req.Priority != nil {
		reg.Priority = *req.Priority
	}
	if req.ForcedHost != nil {
		reg.ForcedHost = *req.ForcedHost
	}
	if req.Restricted != nil {
		reg.Restricted = *req.Restricted
	}
	if req.Enabled != nil {
		reg.Enabled = *req.Enabled
	}
	if err := s.db.Save(reg).Error; err != nil {
		return nil, fmt.Errorf("更新注册关系失败: %w", err)
	}

	view := &RegistrationView{ServerRegistration: *reg, Backend: s.backendBrief(reg.BackendID)}
	if err := s.sync(proxyID); err != nil {
		return view, fmt.Errorf("注册已更新但同步到代理配置失败: %w", err)
	}
	return view, nil
}

// Delete 删除注册关系并同步代理配置。
func (s *RegistrationService) Delete(proxyID, rid uint) error {
	reg, err := s.getRegistration(proxyID, rid)
	if err != nil {
		return err
	}
	if err := s.db.Delete(&model.ServerRegistration{}, reg.ID).Error; err != nil {
		return fmt.Errorf("删除注册关系失败: %w", err)
	}
	if err := s.sync(proxyID); err != nil {
		return fmt.Errorf("注册已删除但同步到代理配置失败: %w", err)
	}
	return nil
}

// ListByBackend 返回某后端注册进的所有代理 ID（供 Velocity secret 一致性校验/下发复用，FR-035）。
func (s *RegistrationService) ListByBackend(backendID uint) ([]model.ServerRegistration, error) {
	var regs []model.ServerRegistration
	if err := s.db.Where("backend_id = ?", backendID).Find(&regs).Error; err != nil {
		return nil, fmt.Errorf("查询后端注册失败: %w", err)
	}
	return regs, nil
}

func (s *RegistrationService) requireProxy(id uint) (*model.Instance, error) {
	var inst model.Instance
	if err := s.db.First(&inst, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProxyNotFound
		}
		return nil, fmt.Errorf("查询代理实例失败: %w", err)
	}
	if inst.Role != model.InstanceRoleProxy {
		return nil, ErrNotAProxy
	}
	return &inst, nil
}

func (s *RegistrationService) requireBackend(id uint) (*model.Instance, error) {
	var inst model.Instance
	if err := s.db.First(&inst, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBackendNotFound
		}
		return nil, fmt.Errorf("查询后端实例失败: %w", err)
	}
	if inst.Role != model.InstanceRoleBackend {
		return nil, ErrNotABackend
	}
	return &inst, nil
}

func (s *RegistrationService) getRegistration(proxyID, rid uint) (*model.ServerRegistration, error) {
	var reg model.ServerRegistration
	if err := s.db.Where("id = ? AND proxy_id = ?", rid, proxyID).First(&reg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRegistrationNotFound
		}
		return nil, fmt.Errorf("查询注册关系失败: %w", err)
	}
	return &reg, nil
}

// backendBrief 加载后端概要；查不到返回 nil（不阻断列表）。
func (s *RegistrationService) backendBrief(backendID uint) *BackendBrief {
	var inst model.Instance
	if err := s.db.First(&inst, backendID).Error; err != nil {
		return nil
	}
	return &BackendBrief{
		ID:         inst.ID,
		Name:       inst.Name,
		Role:       inst.Role,
		NodeID:     inst.NodeID,
		ServerPort: inst.ServerPort,
		Status:     inst.Status,
	}
}

func (s *RegistrationService) sync(proxyID uint) error {
	if s.syncer == nil {
		return nil
	}
	return s.syncer.SyncProxy(proxyID)
}

// boolOr 返回 *b 或缺省值。
func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}
