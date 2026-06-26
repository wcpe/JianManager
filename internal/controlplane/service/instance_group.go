package service

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 实例组织分组树相关错误（FR-165，见 ADR-XXXX）。
var (
	ErrInstanceGroupNotFound       = errors.New("实例分组不存在")
	ErrInstanceGroupParentNotFound = errors.New("父分组不存在")
	ErrInstanceGroupCycle          = errors.New("移动会形成环（不能移到自身或子孙下）")
	ErrInstanceGroupNotEmpty       = errors.New("分组非空，请先清空子分组与成员")
)

// InstanceGroupService 管理「实例组织分组树」（FR-165 / ADR-XXXX）：
// 自引用邻接表的多级嵌套分组 + 实例 M:N，仅供组织归类、折叠、批量运维，
// 与用户组（ADR-004 RBAC/配额）、网络群组（ADR-007 部署）三者正交，仅 CP 读写。
type InstanceGroupService struct {
	db *gorm.DB
}

// NewInstanceGroupService 创建实例组织分组服务。
func NewInstanceGroupService(db *gorm.DB) *InstanceGroupService {
	return &InstanceGroupService{db: db}
}

// InstanceGroupNodeView 树视图中的一个分组节点：基础字段 + 子树聚合（去重）实例数。
type InstanceGroupNodeView struct {
	ID            uint   `json:"id"`
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	ParentID      *uint  `json:"parentId"`
	Sort          int    `json:"sort"`
	InstanceCount int    `json:"instanceCount"`
}

// InstanceGroupMemberView 分组成员实例概要。
type InstanceGroupMemberView struct {
	InstanceID uint                 `json:"instanceId"`
	Name       string               `json:"name"`
	Role       model.InstanceRole   `json:"role"`
	NodeID     uint                 `json:"nodeId"`
	Status     model.InstanceStatus `json:"status"`
}

// wouldCreateCycle 判断把 nodeID 的父设为 newParentID 是否会形成环。
// 成环条件：newParentID 等于 nodeID 自身，或 newParentID 在 nodeID 的子孙集合内。
// 纯函数（不查 DB），便于表驱动测试；newParentID==nil（移到根）恒不成环。
// 若 newParentID 指向不存在的节点，本函数按「不成环」返回，存在性由调用方单独校验。
func wouldCreateCycle(nodes []model.InstanceGroupNode, nodeID uint, newParentID *uint) bool {
	if newParentID == nil {
		return false
	}
	if *newParentID == nodeID {
		return true
	}
	// 收集 nodeID 的全部子孙（含自身），新父落在其中即成环。
	childrenOf := map[uint][]uint{}
	for _, n := range nodes {
		if n.ParentID != nil {
			childrenOf[*n.ParentID] = append(childrenOf[*n.ParentID], n.ID)
		}
	}
	descendants := map[uint]struct{}{nodeID: {}}
	stack := []uint{nodeID}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, child := range childrenOf[cur] {
			if _, seen := descendants[child]; seen {
				continue
			}
			descendants[child] = struct{}{}
			stack = append(stack, child)
		}
	}
	_, inSubtree := descendants[*newParentID]
	return inSubtree
}

// subtreeCounts 计算每个节点的「子树聚合（去重）实例数」。
// memberInstanceIDs 为「节点直接挂载的实例 ID 列表」；返回每个节点子树（含自身及所有后代）
// 去重后的实例总数。同一实例在同一子树内（无论挂在祖先还是后代、是否重复）只计一次。
// 纯函数（不查 DB），便于表驱动测试。
func subtreeCounts(nodes []model.InstanceGroupNode, memberInstanceIDs map[uint][]uint) map[uint]int {
	childrenOf := map[uint][]uint{}
	idSet := map[uint]struct{}{}
	for _, n := range nodes {
		idSet[n.ID] = struct{}{}
		if n.ParentID != nil {
			childrenOf[*n.ParentID] = append(childrenOf[*n.ParentID], n.ID)
		}
	}

	// 后序累积每个子树的去重实例集合。用显式栈避免深树递归。
	subtreeSet := map[uint]map[uint]struct{}{}
	var build func(id uint) map[uint]struct{}
	build = func(id uint) map[uint]struct{} {
		if s, ok := subtreeSet[id]; ok {
			return s
		}
		s := map[uint]struct{}{}
		for _, iid := range memberInstanceIDs[id] {
			s[iid] = struct{}{}
		}
		for _, child := range childrenOf[id] {
			for iid := range build(child) {
				s[iid] = struct{}{}
			}
		}
		subtreeSet[id] = s
		return s
	}

	counts := make(map[uint]int, len(nodes))
	for id := range idSet {
		counts[id] = len(build(id))
	}
	return counts
}

// loadNodes 加载全部分组节点（未软删），用于纯函数树运算。
func (s *InstanceGroupService) loadNodes() ([]model.InstanceGroupNode, error) {
	var nodes []model.InstanceGroupNode
	if err := s.db.Order("sort asc, id asc").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询实例分组失败: %w", err)
	}
	return nodes, nil
}

// memberIDsByGroup 返回 group_id → 该组直接挂载的实例 ID 列表（仅含仍存在的实例）。
func (s *InstanceGroupService) memberIDsByGroup() (map[uint][]uint, error) {
	type row struct {
		GroupID    uint
		InstanceID uint
	}
	var rows []row
	// JOIN instances 过滤悬空成员（实例已删但成员关系残留），保证计数不虚高。
	if err := s.db.Model(&model.InstanceGroupMember{}).
		Select("instance_group_members.group_id, instance_group_members.instance_id").
		Joins("JOIN instances ON instances.id = instance_group_members.instance_id AND instances.deleted_at IS NULL").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询分组成员失败: %w", err)
	}
	out := map[uint][]uint{}
	for _, r := range rows {
		out[r.GroupID] = append(out[r.GroupID], r.InstanceID)
	}
	return out, nil
}

// Tree 返回分组树（扁平节点列表，含每节点子树聚合去重实例数）。
// 前端据 parentId 自行重建层级；排序按 sort,id。
func (s *InstanceGroupService) Tree() ([]InstanceGroupNodeView, error) {
	nodes, err := s.loadNodes()
	if err != nil {
		return nil, err
	}
	members, err := s.memberIDsByGroup()
	if err != nil {
		return nil, err
	}
	counts := subtreeCounts(nodes, members)
	out := make([]InstanceGroupNodeView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, InstanceGroupNodeView{
			ID:            n.ID,
			UUID:          n.UUID,
			Name:          n.Name,
			ParentID:      n.ParentID,
			Sort:          n.Sort,
			InstanceCount: counts[n.ID],
		})
	}
	return out, nil
}

// Create 新建分组节点。parentID 为 nil 建根分组，非 nil 时父必须存在。
// 新节点 sort 取同级末尾（现有同级最大 sort + 1），追加在末尾。
func (s *InstanceGroupService) Create(name string, parentID *uint) (*model.InstanceGroupNode, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("分组名不能为空")
	}
	if parentID != nil {
		if _, err := s.get(*parentID); err != nil {
			if errors.Is(err, ErrInstanceGroupNotFound) {
				return nil, ErrInstanceGroupParentNotFound
			}
			return nil, err
		}
	}
	node := &model.InstanceGroupNode{Name: name, ParentID: parentID, Sort: s.nextSort(parentID)}
	if err := s.db.Create(node).Error; err != nil {
		return nil, fmt.Errorf("创建分组失败: %w", err)
	}
	return node, nil
}

// Update 改名和/或移动父节点（防环）。
//   - name != nil：改名。
//   - parentID != nil：本次要改父；*parentID == nil 表示移到根，*parentID != nil 表示移到该父下。
//     parentID == nil（外层）表示「不改父」。
func (s *InstanceGroupService) Update(id uint, name *string, parentID **uint) (*model.InstanceGroupNode, error) {
	if _, err := s.get(id); err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}
	if name != nil {
		nm := strings.TrimSpace(*name)
		if nm == "" {
			return nil, fmt.Errorf("分组名不能为空")
		}
		updates["name"] = nm
	}
	if parentID != nil {
		newParent := *parentID // 可能为 nil（移到根）
		if newParent != nil {
			// 目标父必须存在
			if _, err := s.get(*newParent); err != nil {
				if errors.Is(err, ErrInstanceGroupNotFound) {
					return nil, ErrInstanceGroupParentNotFound
				}
				return nil, err
			}
		}
		nodes, err := s.loadNodes()
		if err != nil {
			return nil, err
		}
		if wouldCreateCycle(nodes, id, newParent) {
			return nil, ErrInstanceGroupCycle
		}
		// gorm Updates(map) 对 nil 值会跳过，故移到根需显式写 NULL。
		updates["parent_id"] = newParent
		updates["sort"] = s.nextSort(newParent)
	}

	if len(updates) > 0 {
		if err := s.db.Model(&model.InstanceGroupNode{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("更新分组失败: %w", err)
		}
	}
	return s.get(id)
}

// Delete 删除分组节点。非空（有子节点或成员）时拒删（ErrInstanceGroupNotEmpty），不级联。
// 软删节点本身，不触及实例。
func (s *InstanceGroupService) Delete(id uint) error {
	if _, err := s.get(id); err != nil {
		return err
	}
	var childCount int64
	if err := s.db.Model(&model.InstanceGroupNode{}).Where("parent_id = ?", id).Count(&childCount).Error; err != nil {
		return fmt.Errorf("检查子分组失败: %w", err)
	}
	if childCount > 0 {
		return ErrInstanceGroupNotEmpty
	}
	var memberCount int64
	if err := s.db.Model(&model.InstanceGroupMember{}).Where("group_id = ?", id).Count(&memberCount).Error; err != nil {
		return fmt.Errorf("检查分组成员失败: %w", err)
	}
	if memberCount > 0 {
		return ErrInstanceGroupNotEmpty
	}
	return s.db.Delete(&model.InstanceGroupNode{}, id).Error
}

// AddMembers 将实例批量加入分组（幂等：已存在或不存在的实例跳过）。返回新增数与最新成员视图。
func (s *InstanceGroupService) AddMembers(id uint, instanceIDs []uint) (int, []InstanceGroupMemberView, error) {
	if _, err := s.get(id); err != nil {
		return 0, nil, err
	}
	added := 0
	for _, iid := range instanceIDs {
		var inst model.Instance
		if err := s.db.First(&inst, iid).Error; err != nil {
			continue // 实例不存在：跳过
		}
		var exists int64
		s.db.Model(&model.InstanceGroupMember{}).Where("group_id = ? AND instance_id = ?", id, iid).Count(&exists)
		if exists > 0 {
			continue
		}
		if err := s.db.Create(&model.InstanceGroupMember{GroupID: id, InstanceID: iid}).Error; err == nil {
			added++
		}
	}
	members, err := s.Members(id)
	return added, members, err
}

// RemoveMembers 从分组批量移除实例（不影响实例本身）。返回最新成员视图前先校验组存在。
func (s *InstanceGroupService) RemoveMembers(id uint, instanceIDs []uint) error {
	if _, err := s.get(id); err != nil {
		return err
	}
	if len(instanceIDs) == 0 {
		return nil
	}
	return s.db.Where("group_id = ? AND instance_id IN ?", id, instanceIDs).
		Delete(&model.InstanceGroupMember{}).Error
}

// Members 返回某分组直接挂载的成员实例概要（不含子组成员；悬空成员跳过）。
func (s *InstanceGroupService) Members(id uint) ([]InstanceGroupMemberView, error) {
	if _, err := s.get(id); err != nil {
		return nil, err
	}
	var members []model.InstanceGroupMember
	if err := s.db.Where("group_id = ?", id).Order("id asc").Find(&members).Error; err != nil {
		return nil, fmt.Errorf("查询分组成员失败: %w", err)
	}
	views := make([]InstanceGroupMemberView, 0, len(members))
	for _, m := range members {
		var inst model.Instance
		if err := s.db.First(&inst, m.InstanceID).Error; err != nil {
			continue
		}
		views = append(views, InstanceGroupMemberView{
			InstanceID: inst.ID,
			Name:       inst.Name,
			Role:       inst.Role,
			NodeID:     inst.NodeID,
			Status:     inst.Status,
		})
	}
	return views, nil
}

// SubtreeInstanceIDs 返回某分组「子树（含自身及所有后代）去重后的实例 ID 集合」，
// 供按组（含子树）筛选实例用。组不存在返回错误。
func (s *InstanceGroupService) SubtreeInstanceIDs(id uint) ([]uint, error) {
	if _, err := s.get(id); err != nil {
		return nil, err
	}
	nodes, err := s.loadNodes()
	if err != nil {
		return nil, err
	}
	// 收集 id 子树内的全部节点 ID。
	childrenOf := map[uint][]uint{}
	for _, n := range nodes {
		if n.ParentID != nil {
			childrenOf[*n.ParentID] = append(childrenOf[*n.ParentID], n.ID)
		}
	}
	inSubtree := map[uint]struct{}{id: {}}
	stack := []uint{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, child := range childrenOf[cur] {
			if _, seen := inSubtree[child]; seen {
				continue
			}
			inSubtree[child] = struct{}{}
			stack = append(stack, child)
		}
	}
	groupIDs := make([]uint, 0, len(inSubtree))
	for gid := range inSubtree {
		groupIDs = append(groupIDs, gid)
	}
	var instanceIDs []uint
	if err := s.db.Model(&model.InstanceGroupMember{}).
		Joins("JOIN instances ON instances.id = instance_group_members.instance_id AND instances.deleted_at IS NULL").
		Where("instance_group_members.group_id IN ?", groupIDs).
		Distinct().
		Pluck("instance_group_members.instance_id", &instanceIDs).Error; err != nil {
		return nil, fmt.Errorf("查询子树实例集合失败: %w", err)
	}
	return instanceIDs, nil
}

// nextSort 返回某父下新增节点的排序权重（同级最大 sort + 1，空级从 0 起）。
func (s *InstanceGroupService) nextSort(parentID *uint) int {
	var maxSort *int
	q := s.db.Model(&model.InstanceGroupNode{}).Select("MAX(sort)")
	if parentID == nil {
		q = q.Where("parent_id IS NULL")
	} else {
		q = q.Where("parent_id = ?", *parentID)
	}
	if err := q.Scan(&maxSort).Error; err != nil || maxSort == nil {
		return 0
	}
	return *maxSort + 1
}

// get 按 ID 取分组节点，不存在返回 ErrInstanceGroupNotFound。
func (s *InstanceGroupService) get(id uint) (*model.InstanceGroupNode, error) {
	var node model.InstanceGroupNode
	if err := s.db.First(&node, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceGroupNotFound
		}
		return nil, fmt.Errorf("查询分组失败: %w", err)
	}
	return &node, nil
}
