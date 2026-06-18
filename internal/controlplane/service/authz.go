package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// PermissionNode 权限节点。
// 平台管理员拥有全部权限；组管理员/组成员的权限受限于其所属用户组。
type PermissionNode string

const (
	// 用户管理（仅平台管理员）
	PermUserRead   PermissionNode = "user:read"
	PermUserManage PermissionNode = "user:manage"

	// 用户组管理
	PermGroupRead        PermissionNode = "group:read"
	PermGroupManage      PermissionNode = "group:manage"        // 创建/编辑/删除组（平台管理员）
	PermGroupMemberWrite PermissionNode = "group:member:write"  // 管理组成员（组管理员/平台管理员）
	PermGroupQuotaWrite  PermissionNode = "group:quota:write"   // 管理组配额（平台管理员）
	PermGroupQuotaRead   PermissionNode = "group:quota:read"    // 查看组配额用量

	// 节点管理（仅平台管理员）
	PermNodeRead   PermissionNode = "node:read"
	PermNodeManage PermissionNode = "node:manage"

	// 实例管理（按所属组隔离）
	PermInstanceRead    PermissionNode = "instance:read"
	PermInstanceWrite   PermissionNode = "instance:write"
	PermInstanceOperate PermissionNode = "instance:operate"
	PermInstanceDelete  PermissionNode = "instance:delete"
	PermInstanceCreate  PermissionNode = "instance:create"

	// 文件管理（按实例所属组隔离）
	PermFileRead  PermissionNode = "file:read"
	PermFileWrite PermissionNode = "file:write"

	// 终端访问（按实例所属组隔离）
	PermTerminalAccess PermissionNode = "terminal:access"

	// Bot 管理（按实例所属组隔离）
	PermBotRead   PermissionNode = "bot:read"
	PermBotManage PermissionNode = "bot:manage"
)

// UserAccess 当前用户的授权上下文，由 LoadUserAccess 构建。
// 平台管理员的 IsPlatformAdmin 为 true，其余集合为空但权限检查全部放行。
type UserAccess struct {
	UserID            uint
	Role              model.UserRole
	IsPlatformAdmin   bool
	AdminGroupIDs     map[uint]struct{} // 以组管理员身份管理的组 ID 集合
	MemberGroupIDs    map[uint]struct{} // 以普通成员身份所属的组 ID 集合
	AccessibleGroups  map[uint]struct{} // AdminGroupIDs ∪ MemberGroupIDs，用于读权限
}

// AuthzService 授权服务，负责加载用户授权上下文并执行权限判断。
// 参见 ADR-004: 用户组替代多租户（基于用户组而非 tenant_id 做隔离）。
type AuthzService struct {
	db *gorm.DB
}

// NewAuthzService 创建授权服务。
func NewAuthzService(db *gorm.DB) *AuthzService {
	return &AuthzService{db: db}
}

// LoadUserAccess 加载用户的全局角色与组成员关系，构建授权上下文。
func (s *AuthzService) LoadUserAccess(userID uint) (*UserAccess, error) {
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}

	access := &UserAccess{
		UserID:           user.ID,
		Role:             user.Role,
		IsPlatformAdmin:  user.Role == model.RolePlatformAdmin,
		AdminGroupIDs:    map[uint]struct{}{},
		MemberGroupIDs:   map[uint]struct{}{},
		AccessibleGroups: map[uint]struct{}{},
	}
	if access.IsPlatformAdmin {
		return access, nil
	}

	var memberships []model.GroupMember
	if err := s.db.Where("user_id = ?", userID).Find(&memberships).Error; err != nil {
		return nil, fmt.Errorf("查询组成员关系失败: %w", err)
	}

	for _, m := range memberships {
		access.AccessibleGroups[m.GroupID] = struct{}{}
		if m.Role == model.GroupMemberRoleAdmin {
			access.AdminGroupIDs[m.GroupID] = struct{}{}
		} else {
			access.MemberGroupIDs[m.GroupID] = struct{}{}
		}
	}
	return access, nil
}

// HasPermission 判断用户是否拥有指定权限节点（不含资源级隔离）。
// 平台管理员拥有全部权限；组管理员/组成员对管理类权限需要结合资源判断（见 CanManageGroup）。
func (a *UserAccess) HasPermission(node PermissionNode) bool {
	if a.IsPlatformAdmin {
		return true
	}
	switch node {
	case PermGroupRead, PermGroupQuotaRead,
		PermInstanceRead, PermFileRead, PermBotRead:
		// 只读权限：只要属于任意组即拥有（资源级再过滤）
		return len(a.AccessibleGroups) > 0
	case PermGroupMemberWrite:
		// 组管理员可管理本组成员
		return len(a.AdminGroupIDs) > 0
	case PermInstanceWrite, PermInstanceOperate, PermInstanceCreate,
		PermInstanceDelete, PermFileWrite, PermTerminalAccess, PermBotManage:
		// 实例操作类权限：组管理员或组成员均可，具体实例由 CanAccessInstance 收敛
		return len(a.AccessibleGroups) > 0
	default:
		// 用户管理、组/节点管理类仅平台管理员拥有
		return false
	}
}

// CanManageGroup 判断用户是否能管理指定组（平台管理员或该组的组管理员）。
func (a *UserAccess) CanManageGroup(groupID uint) bool {
	if a.IsPlatformAdmin {
		return true
	}
	_, ok := a.AdminGroupIDs[groupID]
	return ok
}

// CanAccessGroup 判断用户是否能访问指定组（管理或成员）。
func (a *UserAccess) CanAccessGroup(groupID uint) bool {
	if a.IsPlatformAdmin {
		return true
	}
	_, ok := a.AccessibleGroups[groupID]
	return ok
}

// CanAccessInstance 判断用户是否能访问指定实例。
// 平台管理员全量放行；否则实例必须归属于其可访问的组。
func (s *AuthzService) CanAccessInstance(access *UserAccess, instanceID uint) (bool, error) {
	if access.IsPlatformAdmin {
		return true, nil
	}
	groupID, err := s.getInstanceGroupID(instanceID)
	if err != nil {
		return false, err
	}
	if groupID == 0 {
		// 未分配组的实例仅平台管理员可访问
		return false, nil
	}
	return access.CanAccessGroup(groupID), nil
}

// CanManageInstance 判断用户是否能管理（写/删除）指定实例。
// 平台管理员全量放行；否则实例必须归属于其管理的组（组管理员）或所属组（成员对写操作的边界由调用方决定）。
func (s *AuthzService) CanManageInstance(access *UserAccess, instanceID uint) (bool, error) {
	if access.IsPlatformAdmin {
		return true, nil
	}
	groupID, err := s.getInstanceGroupID(instanceID)
	if err != nil {
		return false, err
	}
	if groupID == 0 {
		return false, nil
	}
	return access.CanAccessGroup(groupID), nil
}

// getInstanceGroupID 查询实例所属用户组 ID，未分配返回 0。
func (s *AuthzService) getInstanceGroupID(instanceID uint) (uint, error) {
	var gi model.GroupInstance
	err := s.db.Where("instance_id = ?", instanceID).First(&gi).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("查询实例组关联失败: %w", err)
	}
	return gi.GroupID, nil
}

// CanAccessBot 判断用户是否能访问指定 Bot（按其所属实例的组隔离）。
func (s *AuthzService) CanAccessBot(access *UserAccess, botID uint) (bool, error) {
	instanceID, err := s.getBotInstanceID(botID)
	if err != nil {
		return false, err
	}
	if instanceID == 0 {
		return false, nil
	}
	return s.CanAccessInstance(access, instanceID)
}

// AccessibleInstanceIDs 返回用户可访问（可读）的实例 ID 集合，用于将跨组隔离下沉为 SQL 谓词。
// 平台管理员返回 (nil, false)，表示不收敛（调用方不应附加实例 IN 过滤）；
// 非管理员返回 (ids, true)，ids 为其可访问组下的实例 ID（可能为空切片，表示无任何可见实例）。
// 万级 Bot 下不可用逐条 CanAccessInstance 循环，故以集合谓词替代（参见 FR-038）。
func (s *AuthzService) AccessibleInstanceIDs(access *UserAccess) ([]uint, bool, error) {
	if access.IsPlatformAdmin {
		return nil, false, nil
	}
	if len(access.AccessibleGroups) == 0 {
		return []uint{}, true, nil
	}
	groupIDs := make([]uint, 0, len(access.AccessibleGroups))
	for gid := range access.AccessibleGroups {
		groupIDs = append(groupIDs, gid)
	}
	var instanceIDs []uint
	if err := s.db.Model(&model.GroupInstance{}).
		Where("group_id IN ?", groupIDs).
		Distinct().
		Pluck("instance_id", &instanceIDs).Error; err != nil {
		return nil, false, fmt.Errorf("查询可访问实例集合失败: %w", err)
	}
	return instanceIDs, true, nil
}

// CanManageBot 判断用户是否能管理指定 Bot。
func (s *AuthzService) CanManageBot(access *UserAccess, botID uint) (bool, error) {
	instanceID, err := s.getBotInstanceID(botID)
	if err != nil {
		return false, err
	}
	if instanceID == 0 {
		return false, nil
	}
	return s.CanManageInstance(access, instanceID)
}

// getBotInstanceID 查询 Bot 所属实例 ID。
func (s *AuthzService) getBotInstanceID(botID uint) (uint, error) {
	var bot model.Bot
	if err := s.db.First(&bot, botID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("查询 Bot 失败: %w", err)
	}
	return bot.InstanceID, nil
}

// QuotaUsage 组配额用量。
type QuotaUsage struct {
	GroupID        uint `json:"groupId"`
	MaxInstances   int  `json:"maxInstances"`
	MaxBots        int  `json:"maxBots"`
	MaxStorageMB   int  `json:"maxStorageMb"`
	UsedInstances  int  `json:"usedInstances"`
	UsedBots       int  `json:"usedBots"`
	UsedStorageMB  int  `json:"usedStorageMb"`
}

// GetQuotaUsage 查询组配额及当前用量。
// 用量统计：实例数按 group_instances 计数；Bot 数按组内实例关联的 bots 计数；存储用量暂按实例工作目录预留 0（运行时累计，见 FR-003 验收）。
func (s *AuthzService) GetQuotaUsage(groupID uint) (*QuotaUsage, error) {
	var quota model.GroupQuota
	if err := s.db.Where("group_id = ?", groupID).First(&quota).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGroupNotFound
		}
		return nil, fmt.Errorf("查询组配额失败: %w", err)
	}

	usage := &QuotaUsage{
		GroupID:      groupID,
		MaxInstances: quota.MaxInstances,
		MaxBots:      quota.MaxBots,
		MaxStorageMB: quota.MaxStorageMB,
	}

	// 实例数
	var instanceCount int64
	if err := s.db.Model(&model.GroupInstance{}).
		Where("group_id = ?", groupID).
		Count(&instanceCount).Error; err != nil {
		return nil, fmt.Errorf("统计组实例数失败: %w", err)
	}
	usage.UsedInstances = int(instanceCount)

	// Bot 数：组内实例关联的 Bot 总数
	var botCount int64
	if err := s.db.Model(&model.Bot{}).
		Joins("JOIN group_instances ON group_instances.instance_id = bots.instance_id").
		Where("group_instances.group_id = ?", groupID).
		Count(&botCount).Error; err != nil {
		return nil, fmt.Errorf("统计组 Bot 数失败: %w", err)
	}
	usage.UsedBots = int(botCount)

	// 存储用量：按组内实例的备份总大小累计（MB）。
	// Worker 工作目录实时大小暂未上报，备份大小作为存储占用的保守下界。
	// TODO(FR-003): 接入 Worker 工作目录大小上报后替换为更精确的累计。
	var storageSum struct {
		Total float64
	}
	if err := s.db.Model(&model.Backup{}).
		Select("COALESCE(SUM(file_size_mb), 0) as total").
		Joins("JOIN group_instances ON group_instances.instance_id = backups.instance_id").
		Where("group_instances.group_id = ?", groupID).
		Scan(&storageSum).Error; err != nil {
		return nil, fmt.Errorf("统计组存储用量失败: %w", err)
	}
	usage.UsedStorageMB = int(storageSum.Total)

	return usage, nil
}
