package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"gorm.io/gorm"
)

// ErrSuperAdminLocked 超级管理员权限不可撤销/降权。
var ErrSuperAdminLocked = errors.New("超级管理员拥有全部权限，不可撤销或降权")

// ErrRoleSystem 系统角色模板不可删除。
var ErrRoleSystem = errors.New("系统角色不可删除")

// ErrRoleExists 角色 key 已存在。
var ErrRoleExists = errors.New("角色 key 已存在")

// ErrOverrideInvalid 非法覆盖项。
var ErrOverrideInvalid = errors.New("非法权限覆盖项")

// PermissionService 角色模板 / 用户覆盖 / 有效权限（FR-432）。
type PermissionService struct {
	db *gorm.DB
}

// NewPermissionService 创建权限服务。
func NewPermissionService(db *gorm.DB) *PermissionService {
	return &PermissionService{db: db}
}

// SeedSystemRoles 幂等写入系统角色模板。
// platform_admin：**每次 seed 强制写回全量节点**（超级管理员不可残缺）。
// 其它系统模板：已存在时只补元数据，不覆盖节点（运营可改）。
func (s *PermissionService) SeedSystemRoles() error {
	seed := SystemRoleSeed()
	for key, meta := range seed {
		var role model.RoleTemplate
		err := s.db.Where("key = ?", key).First(&role).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return fmt.Errorf("查询角色模板 %s: %w", key, err)
			}
			role = model.RoleTemplate{Key: key, Name: meta.Name, Description: meta.Desc, IsSystem: true}
			if err := s.db.Create(&role).Error; err != nil {
				return fmt.Errorf("创建角色模板 %s: %w", key, err)
			}
			if err := s.replaceRoleNodes(role.ID, meta.Nodes); err != nil {
				return err
			}
			continue
		}
		updates := map[string]any{
			"name": meta.Name, "description": meta.Desc, "is_system": true,
		}
		if err := s.db.Model(&role).Updates(updates).Error; err != nil {
			return fmt.Errorf("更新角色模板 %s: %w", key, err)
		}
		// 超级管理员：强制全量节点，防止历史误清空
		if key == model.RoleKeyPlatformAdmin {
			if err := s.replaceRoleNodes(role.ID, AllPermissionNodes()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *PermissionService) replaceRoleNodes(roleID uint, nodes []string) error {
	if err := s.db.Where("role_id = ?", roleID).Delete(&model.RolePermission{}).Error; err != nil {
		return fmt.Errorf("清空角色节点: %w", err)
	}
	for _, n := range nodes {
		if !IsValidPermissionNode(n) {
			continue
		}
		if err := s.db.Create(&model.RolePermission{RoleID: roleID, Node: n}).Error; err != nil {
			return fmt.Errorf("写入角色节点 %s: %w", n, err)
		}
	}
	return nil
}

// Catalog 返回静态权限目录。
func (s *PermissionService) Catalog() []PermissionDomain { return PermissionCatalog }

// ListRoles 列出角色模板。platform_admin 的 nodes 始终返回全量（超级管理员不可残缺）。
func (s *PermissionService) ListRoles() ([]model.RoleTemplate, error) {
	var roles []model.RoleTemplate
	if err := s.db.Order("is_system desc, id asc").Find(&roles).Error; err != nil {
		return nil, err
	}
	return roles, nil
}

// RoleNodes 角色已授节点；platform_admin 强制全量。角色不存在返回错误（R-15）。
func (s *PermissionService) RoleNodes(roleID uint) ([]string, error) {
	var role model.RoleTemplate
	if err := s.db.First(&role, roleID).Error; err != nil {
		return nil, err
	}
	if role.Key == model.RoleKeyPlatformAdmin {
		return AllPermissionNodes(), nil
	}
	var perms []model.RolePermission
	if err := s.db.Where("role_id = ?", roleID).Find(&perms).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, p.Node)
	}
	return out, nil
}

// GetRole 按 id 取角色。
func (s *PermissionService) GetRole(id uint) (*model.RoleTemplate, error) {
	var role model.RoleTemplate
	if err := s.db.First(&role, id).Error; err != nil {
		return nil, err
	}
	return &role, nil
}

// CreateRole 新建自定义角色。key 冲突时在事务内递增重试（R-13）。
func (s *PermissionService) CreateRole(name, desc string, nodes []string) (*model.RoleTemplate, error) {
	base := "custom_" + sanitizeRoleKey(name)
	if base == "custom_" {
		base = fmt.Sprintf("custom_%d", len(name))
	}
	var created model.RoleTemplate
	err := s.db.Transaction(func(tx *gorm.DB) error {
		key := base
		for i := 0; i < 8; i++ {
			var exists int64
			if err := tx.Model(&model.RoleTemplate{}).Where("key = ?", key).Count(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				role := model.RoleTemplate{Key: key, Name: name, Description: desc, IsSystem: false}
				if err := tx.Create(&role).Error; err != nil {
					if isUniqueViolation(err) {
						key = fmt.Sprintf("%s_%d", base, i+2)
						continue
					}
					return err
				}
				for _, n := range nodes {
					if !IsValidPermissionNode(n) {
						continue
					}
					if err := tx.Create(&model.RolePermission{RoleID: role.ID, Node: n}).Error; err != nil {
						return err
					}
				}
				created = role
				return nil
			}
			key = fmt.Sprintf("%s_%d", base, i+2)
		}
		return ErrRoleExists
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}

func sanitizeRoleKey(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		}
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return string(out)
}

// UpdateRole 更新角色元数据与节点。platform_admin 节点集合锁定为全量，拒绝降权。
func (s *PermissionService) UpdateRole(id uint, name, desc *string, nodes []string, replaceNodes bool) (*model.RoleTemplate, error) {
	role, err := s.GetRole(id)
	if err != nil {
		return nil, err
	}
	if role.Key == model.RoleKeyPlatformAdmin {
		// 超级管理员：忽略请求中的节点裁剪，强制全量
		replaceNodes = true
		nodes = AllPermissionNodes()
	}
	updates := map[string]any{}
	if name != nil && *name != "" && role.Key != model.RoleKeyPlatformAdmin {
		updates["name"] = *name
	}
	if desc != nil && role.Key != model.RoleKeyPlatformAdmin {
		updates["description"] = *desc
	}
	if len(updates) > 0 {
		if err := s.db.Model(role).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	if replaceNodes {
		if err := s.replaceRoleNodes(role.ID, nodes); err != nil {
			return nil, err
		}
	}
	return s.GetRole(id)
}

// DeleteRole 删除自定义角色；系统模板拒绝（R-11：错误类型与超管锁分离）。
func (s *PermissionService) DeleteRole(id uint) error {
	role, err := s.GetRole(id)
	if err != nil {
		return err
	}
	if role.Key == model.RoleKeyPlatformAdmin || role.IsSystem {
		return ErrRoleSystem
	}
	if err := s.db.Where("role_id = ?", id).Delete(&model.RolePermission{}).Error; err != nil {
		return err
	}
	if err := s.db.Where("role_id = ?", id).Delete(&model.UserRoleBinding{}).Error; err != nil {
		return err
	}
	return s.db.Delete(&model.RoleTemplate{}, id).Error
}


// userIsSuperAdmin：users.role==10 或当前绑定模板为 platform_admin。
func (s *PermissionService) userIsSuperAdmin(user *model.User) bool {
	if user.Role == model.RolePlatformAdmin {
		return true
	}
	var binding model.UserRoleBinding
	if err := s.db.Where("user_id = ?", user.ID).First(&binding).Error; err != nil {
		return false
	}
	role, err := s.GetRole(binding.RoleID)
	if err != nil {
		return false
	}
	return role.Key == model.RoleKeyPlatformAdmin
}

// ensureNotDemotingSuperAdmin：超管（legacy 或绑定模板）不可改绑到非 platform_admin 模板。
func (s *PermissionService) ensureNotDemotingSuperAdmin(user *model.User, targetRoleKey string) error {
	if !s.userIsSuperAdmin(user) {
		return nil
	}
	if targetRoleKey == model.RoleKeyPlatformAdmin {
		return nil
	}
	return ErrSuperAdminLocked
}

// BindUserRole 绑定用户主角色。禁止把平台管理员（users.role=10）绑到非超级管理员模板。
func (s *PermissionService) BindUserRole(userID, roleID uint) error {
	role, err := s.GetRole(roleID)
	if err != nil {
		return err
	}
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		return err
	}
	if err := s.ensureNotDemotingSuperAdmin(&user, role.Key); err != nil {
		return err
	}
	var binding model.UserRoleBinding
	err = s.db.Where("user_id = ?", userID).First(&binding).Error
	if err == gorm.ErrRecordNotFound {
		return s.db.Create(&model.UserRoleBinding{UserID: userID, RoleID: roleID}).Error
	}
	if err != nil {
		return err
	}
	return s.db.Model(&binding).Update("role_id", role.ID).Error
}

// UserOverrides 用户覆盖列表。
func (s *PermissionService) UserOverrides(userID uint) ([]model.UserPermissionOverride, error) {
	var list []model.UserPermissionOverride
	if err := s.db.Where("user_id = ?", userID).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// SetUserOverrides 整表替换用户覆盖（事务，R-10）。超管（legacy/绑定模板）拒绝覆盖。
func (s *PermissionService) SetUserOverrides(userID uint, items []model.UserPermissionOverride) error {
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		return err
	}
	if s.userIsSuperAdmin(&user) {
		return ErrSuperAdminLocked
	}
	for _, it := range items {
		if it.Effect != model.OverrideAllow && it.Effect != model.OverrideDeny {
			return ErrOverrideInvalid
		}
		if !IsValidPermissionNode(it.Node) {
			return ErrOverrideInvalid
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserPermissionOverride{}).Error; err != nil {
			return err
		}
		for _, it := range items {
			rec := model.UserPermissionOverride{UserID: userID, Node: it.Node, Effect: it.Effect}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// EffectiveForUser 计算用户有效权限与 roleKey。
func (s *PermissionService) EffectiveForUser(userID uint, legacyRole model.UserRole) (nodes map[string]struct{}, roleKey string, err error) {
	isPlatformAdmin := legacyRole == model.RolePlatformAdmin

	var binding model.UserRoleBinding
	bound := false
	if e := s.db.Where("user_id = ?", userID).First(&binding).Error; e == nil {
		bound = true
	} else if e != gorm.ErrRecordNotFound {
		return nil, "", e
	}

	roleKey = model.RoleKeyForLegacyUserRole(legacyRole)
	var roleNodes []string
	if bound {
		var role model.RoleTemplate
		if e := s.db.First(&role, binding.RoleID).Error; e == nil {
			roleKey = role.Key
			if role.Key == model.RoleKeyPlatformAdmin {
				isPlatformAdmin = true
			}
			roleNodes, err = s.RoleNodes(role.ID)
			if err != nil {
				return nil, "", err
			}
		}
	} else {
		// 无绑定：按兼容位映射系统模板，**优先读库内节点**（后台改过的模板立即生效）
		var role model.RoleTemplate
		if e := s.db.Where("key = ?", roleKey).First(&role).Error; e == nil {
			if role.Key == model.RoleKeyPlatformAdmin {
				isPlatformAdmin = true
			}
			roleNodes, err = s.RoleNodes(role.ID)
			if err != nil {
				return nil, "", err
			}
		} else if seed, ok := SystemRoleSeed()[roleKey]; ok {
			roleNodes = seed.Nodes
		}
	}

	ovs, err := s.UserOverrides(userID)
	if err != nil {
		return nil, "", err
	}
	list := make([]PermOverride, 0, len(ovs))
	for _, o := range ovs {
		list = append(list, PermOverride{Node: o.Node, Effect: o.Effect})
	}
	return EffectivePermissionNodes(roleKey, roleNodes, list, isPlatformAdmin), roleKey, nil
}

// Authorize 判断用户是否拥有节点。
func (s *PermissionService) Authorize(userID uint, legacyRole model.UserRole, node string) bool {
	if legacyRole == model.RolePlatformAdmin {
		return true
	}
	set, _, err := s.EffectiveForUser(userID, legacyRole)
	if err != nil {
		return false
	}
	_, ok := set[node]
	return ok
}

// RoleDetail 角色 + 节点。
type RoleDetail struct {
	model.RoleTemplate
	Nodes []string `json:"nodes"`
}

// UserPermissionView 用户权限视图。
type UserPermissionView struct {
	UserID        uint     `json:"userId"`
	RoleKey       string   `json:"roleKey"`
	RoleID        *uint    `json:"roleId,omitempty"`
	Nodes         []string `json:"nodes"`
	Overrides     []model.UserPermissionOverride `json:"overrides"`
	IsPlatformAdmin bool   `json:"isPlatformAdmin"`
}
