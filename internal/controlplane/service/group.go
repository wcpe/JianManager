package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	ErrGroupNotFound = errors.New("用户组不存在")
	ErrAlreadyMember = errors.New("已经是组成员")
	ErrNotMember     = errors.New("不是组成员")
)

// GroupService 用户组管理服务。
type GroupService struct {
	db *gorm.DB
}

// NewGroupService 创建用户组服务。
func NewGroupService(db *gorm.DB) *GroupService {
	return &GroupService{db: db}
}

// List 返回用户组列表。
func (s *GroupService) List() ([]model.Group, error) {
	var groups []model.Group
	if err := s.db.Preload("Members.User").Preload("Quota").Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("查询用户组列表失败: %w", err)
	}
	return groups, nil
}

// GetByID 按 ID 获取用户组详情（含成员和配额）。
func (s *GroupService) GetByID(id uint) (*model.Group, error) {
	var group model.Group
	if err := s.db.Preload("Members.User").Preload("Quota").First(&group, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGroupNotFound
		}
		return nil, fmt.Errorf("查询用户组失败: %w", err)
	}
	return &group, nil
}

// Create 创建用户组，并自动创建默认配额。
func (s *GroupService) Create(name, description string) (*model.Group, error) {
	group := &model.Group{
		Name:        name,
		Description: description,
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(group).Error; err != nil {
			return fmt.Errorf("创建用户组失败: %w", err)
		}

		quota := &model.GroupQuota{
			GroupID: group.ID,
		}
		if err := tx.Create(quota).Error; err != nil {
			return fmt.Errorf("创建用户组配额失败: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return s.GetByID(group.ID)
}

// Update 更新用户组信息。
func (s *GroupService) Update(id uint, name, description *string) (*model.Group, error) {
	group, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}
	if name != nil {
		updates["name"] = *name
	}
	if description != nil {
		updates["description"] = *description
	}

	if len(updates) > 0 {
		if err := s.db.Model(group).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("更新用户组失败: %w", err)
		}
	}

	return s.GetByID(id)
}

// Delete 删除用户组。
func (s *GroupService) Delete(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// 删除关联的成员和配额
		if err := tx.Where("group_id = ?", id).Delete(&model.GroupMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", id).Delete(&model.GroupQuota{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&model.Group{}, id).Error; err != nil {
			return err
		}
		return nil
	})
}

// AddMember 向组中添加成员。
func (s *GroupService) AddMember(groupID, userID uint, role model.GroupMemberRole) error {
	// 检查是否已是成员
	var count int64
	s.db.Model(&model.GroupMember{}).Where("group_id = ? AND user_id = ?", groupID, userID).Count(&count)
	if count > 0 {
		return ErrAlreadyMember
	}

	member := &model.GroupMember{
		GroupID: groupID,
		UserID:  userID,
		Role:    role,
	}
	if err := s.db.Create(member).Error; err != nil {
		return fmt.Errorf("添加组成员失败: %w", err)
	}
	return nil
}

// RemoveMember 从组中移除成员。
func (s *GroupService) RemoveMember(groupID, userID uint) error {
	result := s.db.Where("group_id = ? AND user_id = ?", groupID, userID).Delete(&model.GroupMember{})
	if result.RowsAffected == 0 {
		return ErrNotMember
	}
	return result.Error
}

// UpdateQuota 更新组配额。
func (s *GroupService) UpdateQuota(groupID uint, maxInstances, maxBots, maxStorageMB *int) error {
	updates := map[string]interface{}{}
	if maxInstances != nil {
		updates["max_instances"] = *maxInstances
	}
	if maxBots != nil {
		updates["max_bots"] = *maxBots
	}
	if maxStorageMB != nil {
		updates["max_storage_mb"] = *maxStorageMB
	}

	if len(updates) == 0 {
		return nil
	}

	result := s.db.Model(&model.GroupQuota{}).Where("group_id = ?", groupID).Updates(updates)
	if result.RowsAffected == 0 {
		return ErrGroupNotFound
	}
	return result.Error
}
