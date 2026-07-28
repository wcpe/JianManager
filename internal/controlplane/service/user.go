package service

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// UserService 用户管理服务。
type UserService struct {
	db           *gorm.DB
	invitation   *UserInvitationService
	passwordCost int
}

// NewUserService 创建用户管理服务。
func NewUserService(db *gorm.DB) *UserService {
	return &UserService{db: db, invitation: NewUserInvitationService(db), passwordCost: bcrypt.DefaultCost}
}

// InvitationService 返回与用户持久化边界共享数据库的邀请服务。
func (s *UserService) InvitationService() *UserInvitationService { return s.invitation }

// SetPasswordCostForTest 设置测试 bcrypt 成本，生产装配不得调用。
func (s *UserService) SetPasswordCostForTest(cost int) { s.passwordCost = cost }

// Create 创建由平台管理员直接指定角色与状态的用户。
func (s *UserService) Create(username, password string, role model.UserRole, status model.UserStatus) (*model.User, error) {
	if !validUserRole(role) || !validUserStatus(status) {
		return nil, ErrUserInvalid
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), s.passwordCost)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}
	user := &model.User{Username: username, Password: string(hashed), Role: role, Status: status}
	if err := s.db.Create(user).Error; err != nil {
		if isUniqueConstraint(err) {
			return nil, ErrUserExists
		}
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}
	return user, nil
}

func validUserRole(role model.UserRole) bool {
	return role == model.RoleMember || role == model.RoleGroupAdmin || role == model.RolePlatformAdmin
}

func validUserStatus(status model.UserStatus) bool {
	return status == model.UserStatusActive || status == model.UserStatusDisabled
}

func isUniqueConstraint(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(err.Error()), "unique")
}

// UserListFilter 用户列表筛选（FR-336）。零值不限制。
type UserListFilter struct {
	// Q 用户名模糊匹配（子串，LIKE %q%，%/_ 由服务端转义）。
	Q string
	// Limit <=0 表示不分页返回全部。
	Limit int
	// Offset 偏移，仅 Limit>0 时生效；负值按 0 处理。
	Offset int
}

// escapeLike 转义 LIKE 模式中的通配符。选 '!' 作转义符（配套 ESCAPE '!'）：
// 反斜杠在 SQLite/MySQL 的字符串字面量语义不一致（MySQL 需双写），'!' 两库皆可原样内联。
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

// List 返回用户列表与命中总数（FR-336）。total 与 Q 条件同源（Count 同 WHERE）；
// 排序统一 username ASC, id ASC——分页窗口稳定必需，分页/全量两形态一致。
func (s *UserService) List(f UserListFilter) ([]model.User, int64, error) {
	query := s.db.Model(&model.User{})
	if f.Q != "" {
		query = query.Where("username LIKE ? ESCAPE '!'", "%"+escapeLike(f.Q)+"%")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计用户总数失败: %w", err)
	}

	query = query.Order("username ASC, id ASC")
	if f.Limit > 0 {
		offset := f.Offset
		if offset < 0 {
			offset = 0
		}
		query = query.Limit(f.Limit).Offset(offset)
	}

	var users []model.User
	if err := query.Find(&users).Error; err != nil {
		return nil, 0, fmt.Errorf("查询用户列表失败: %w", err)
	}
	return users, total, nil
}

// GetByID 按 ID 获取用户。
func (s *UserService) GetByID(id uint) (*model.User, error) {
	var user model.User
	if err := s.db.First(&user, id).Error; err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return &user, nil
}

// Update 更新用户信息（角色、状态、密码）。
// password 非空时重置登录密码（bcrypt 加密）；长度下限由路由层 binding 守住（与初始化/创建一致，FR-156）。
func (s *UserService) Update(id uint, role *model.UserRole, status *model.UserStatus, password *string) (*model.User, error) {
	user, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}
	if role != nil {
		updates["role"] = *role
	}
	if status != nil {
		updates["status"] = *status
	}
	if password != nil && *password != "" {
		hashed, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("加密密码失败: %w", err)
		}
		updates["password"] = string(hashed)
	}
	if role != nil || status != nil || (password != nil && *password != "") {
		updates["auth_version"] = gorm.Expr("auth_version + ?", 1)
	}

	if len(updates) > 0 {
		if err := s.db.Model(user).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("更新用户失败: %w", err)
		}
	}

	return user, nil
}

// Delete 删除用户（软删除）。
func (s *UserService) Delete(id uint) error {
	if err := s.db.Delete(&model.User{}, id).Error; err != nil {
		return fmt.Errorf("删除用户失败: %w", err)
	}
	return nil
}
