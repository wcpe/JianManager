package service

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// UserService 用户管理服务。
type UserService struct {
	db *gorm.DB
}

// NewUserService 创建用户管理服务。
func NewUserService(db *gorm.DB) *UserService {
	return &UserService{db: db}
}

// List 返回用户列表。
func (s *UserService) List() ([]model.User, error) {
	var users []model.User
	if err := s.db.Find(&users).Error; err != nil {
		return nil, fmt.Errorf("查询用户列表失败: %w", err)
	}
	return users, nil
}

// GetByID 按 ID 获取用户。
func (s *UserService) GetByID(id uint) (*model.User, error) {
	var user model.User
	if err := s.db.First(&user, id).Error; err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return &user, nil
}

// Update 更新用户信息（角色、状态）。
func (s *UserService) Update(id uint, role *model.UserRole, status *model.UserStatus) (*model.User, error) {
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
