package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 群组（Network 软标签）相关错误（FR-032）。
var (
	ErrNetworkNotFound     = errors.New("群组不存在")
	ErrNetworkNameConflict = errors.New("群组名已存在")
	ErrInvalidBatchAction  = errors.New("不支持的批量操作")
)

// NetworkService 管理 Network 软标签（FR-032 / ADR-007）：非独占分组，仅供 UI 筛选/批量运维。
type NetworkService struct {
	db       *gorm.DB
	instance *InstanceService
}

// NewNetworkService 创建群组服务。
func NewNetworkService(db *gorm.DB, instance *InstanceService) *NetworkService {
	return &NetworkService{db: db, instance: instance}
}

// MemberStatusCounts 群组成员按运行状态的计数桶（五态零补齐，FR-335）。
// 供列表页免详情请求直接渲染健康分布条。
type MemberStatusCounts struct {
	Running  int `json:"running"`
	Stopped  int `json:"stopped"`
	Crashed  int `json:"crashed"`
	Starting int `json:"starting"`
	Stopping int `json:"stopping"`
}

// NetworkSummary 群组列表项（含成员数与成员健康计数，FR-032/FR-335）。
type NetworkSummary struct {
	ID           uint               `json:"id"`
	UUID         string             `json:"uuid"`
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	MemberCount  int                `json:"memberCount"`
	MemberStatus MemberStatusCounts `json:"memberStatus"`
	CreatedAt    time.Time          `json:"createdAt"`
}

// NetworkMemberView 群组成员实例概要。
type NetworkMemberView struct {
	InstanceID uint                 `json:"instanceId"`
	Name       string               `json:"name"`
	Role       model.InstanceRole   `json:"role"`
	NodeID     uint                 `json:"nodeId"`
	Status     model.InstanceStatus `json:"status"`
}

// NetworkDetail 群组详情（含成员）。
type NetworkDetail struct {
	ID          uint                `json:"id"`
	UUID        string              `json:"uuid"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Members     []NetworkMemberView `json:"members"`
}

// BatchActionItemResult 单个成员的批量操作结果。
type BatchActionItemResult struct {
	InstanceID uint   `json:"instanceId"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// BatchActionResult 群组批量操作汇总。
type BatchActionResult struct {
	Action    string                  `json:"action"`
	Total     int                     `json:"total"`
	Succeeded int                     `json:"succeeded"`
	Failed    int                     `json:"failed"`
	Results   []BatchActionItemResult `json:"results"`
}

// List 返回所有群组及成员健康计数（新→旧，FR-335）。
// 一次 JOIN+GROUP BY 聚合出全部群组按状态的成员计数（替代原 per-network Count 循环）；
// INNER JOIN 天然剔除悬空成员（实例已删但 network_members 残留），与 Get 的成员列表口径一致。
// memberCount 为五桶之和（即实际存在的成员数）。
func (s *NetworkService) List() ([]NetworkSummary, error) {
	var networks []model.Network
	if err := s.db.Order("created_at desc").Find(&networks).Error; err != nil {
		return nil, fmt.Errorf("查询群组列表失败: %w", err)
	}

	// 一次聚合：每个 (network_id, status) 一行计数。
	type statusCountRow struct {
		NetworkID uint
		Status    model.InstanceStatus
		Cnt       int
	}
	var rows []statusCountRow
	if err := s.db.
		Model(&model.NetworkMember{}).
		Select("network_members.network_id AS network_id, instances.status AS status, COUNT(*) AS cnt").
		Joins("JOIN instances ON instances.id = network_members.instance_id").
		Group("network_members.network_id, instances.status").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合群组成员状态失败: %w", err)
	}

	counts := make(map[uint]*MemberStatusCounts, len(networks))
	for _, r := range rows {
		c := counts[r.NetworkID]
		if c == nil {
			c = &MemberStatusCounts{}
			counts[r.NetworkID] = c
		}
		addStatusCount(c, r.Status, r.Cnt)
	}

	out := make([]NetworkSummary, 0, len(networks))
	for _, n := range networks {
		var mc MemberStatusCounts
		if c := counts[n.ID]; c != nil {
			mc = *c
		}
		out = append(out, NetworkSummary{
			ID:           n.ID,
			UUID:         n.UUID,
			Name:         n.Name,
			Description:  n.Description,
			MemberCount:  mc.Running + mc.Stopped + mc.Crashed + mc.Starting + mc.Stopping,
			MemberStatus: mc,
			CreatedAt:    n.CreatedAt,
		})
	}
	return out, nil
}

// addStatusCount 把一个状态的计数累加进对应桶（未知状态计入 stopped，与前端中性桶口径一致）。
func addStatusCount(c *MemberStatusCounts, status model.InstanceStatus, n int) {
	switch status {
	case model.InstanceStatusRunning:
		c.Running += n
	case model.InstanceStatusCrashed:
		c.Crashed += n
	case model.InstanceStatusStarting:
		c.Starting += n
	case model.InstanceStatusStopping:
		c.Stopping += n
	default: // STOPPED 及未知
		c.Stopped += n
	}
}

// MembersIndex 一次返回所有群组的成员归属（network_id → 成员实例 ID 列表，FR-335）。
// 供拓扑聚合端点按 network 分组布局；不 JOIN 实例表（悬空成员由聚合端点侧按 proxy/backend 存在性自然过滤）。
func (s *NetworkService) MembersIndex() (map[uint][]uint, error) {
	var members []model.NetworkMember
	if err := s.db.Order("network_id asc, id asc").Find(&members).Error; err != nil {
		return nil, fmt.Errorf("查询群组成员索引失败: %w", err)
	}
	idx := make(map[uint][]uint)
	for _, m := range members {
		idx[m.NetworkID] = append(idx[m.NetworkID], m.InstanceID)
	}
	return idx, nil
}

// NetworkTopoBrief 供拓扑分组布局的群组概要（含成员归属，FR-335）。
type NetworkTopoBrief struct {
	ID                uint   `json:"id"`
	Name              string `json:"name"`
	MemberInstanceIDs []uint `json:"memberInstanceIds"`
}

// TopoBriefs 返回所有群组的分组概要（id/name/成员归属），供拓扑聚合端点（FR-335）。
// 成员归属仅收敛到「实际存在的实例 ID」——existing 为存在的实例 ID 集合（proxy+backend），
// 不在其中的悬空成员被剔除，与 api.md「悬空成员不出现」一致。
func (s *NetworkService) TopoBriefs(existing map[uint]bool) ([]NetworkTopoBrief, error) {
	var networks []model.Network
	if err := s.db.Order("created_at desc").Find(&networks).Error; err != nil {
		return nil, fmt.Errorf("查询群组列表失败: %w", err)
	}
	idx, err := s.MembersIndex()
	if err != nil {
		return nil, err
	}
	out := make([]NetworkTopoBrief, 0, len(networks))
	for _, n := range networks {
		ids := make([]uint, 0, len(idx[n.ID]))
		for _, iid := range idx[n.ID] {
			if existing[iid] {
				ids = append(ids, iid)
			}
		}
		out = append(out, NetworkTopoBrief{ID: n.ID, Name: n.Name, MemberInstanceIDs: ids})
	}
	return out, nil
}

// Create 创建群组。名称在未软删群组间唯一。
func (s *NetworkService) Create(name, description string) (*model.Network, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("群组名不能为空")
	}
	var count int64
	s.db.Model(&model.Network{}).Where("name = ?", name).Count(&count)
	if count > 0 {
		return nil, ErrNetworkNameConflict
	}
	n := &model.Network{Name: name, Description: description}
	if err := s.db.Create(n).Error; err != nil {
		return nil, fmt.Errorf("创建群组失败: %w", err)
	}
	return n, nil
}

// Get 返回群组详情（含成员）。
func (s *NetworkService) Get(id uint) (*NetworkDetail, error) {
	n, err := s.get(id)
	if err != nil {
		return nil, err
	}
	var members []model.NetworkMember
	s.db.Where("network_id = ?", id).Order("id asc").Find(&members)
	views := make([]NetworkMemberView, 0, len(members))
	for _, m := range members {
		var inst model.Instance
		if err := s.db.First(&inst, m.InstanceID).Error; err != nil {
			continue // 实例已删除：成员关系悬空，跳过展示
		}
		views = append(views, NetworkMemberView{
			InstanceID: inst.ID,
			Name:       inst.Name,
			Role:       inst.Role,
			NodeID:     inst.NodeID,
			Status:     inst.Status,
		})
	}
	return &NetworkDetail{ID: n.ID, UUID: n.UUID, Name: n.Name, Description: n.Description, Members: views}, nil
}

// Update 重命名/改描述。
func (s *NetworkService) Update(id uint, name, description *string) (*NetworkDetail, error) {
	n, err := s.get(id)
	if err != nil {
		return nil, err
	}
	updates := map[string]interface{}{}
	if name != nil {
		nm := strings.TrimSpace(*name)
		if nm == "" {
			return nil, fmt.Errorf("群组名不能为空")
		}
		if nm != n.Name {
			var count int64
			s.db.Model(&model.Network{}).Where("name = ? AND id <> ?", nm, id).Count(&count)
			if count > 0 {
				return nil, ErrNetworkNameConflict
			}
		}
		updates["name"] = nm
	}
	if description != nil {
		updates["description"] = *description
	}
	if len(updates) > 0 {
		if err := s.db.Model(n).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("更新群组失败: %w", err)
		}
	}
	return s.Get(id)
}

// Delete 软删除群组并硬删除其成员关系；不触及成员实例与其 server_registrations（ADR-007）。
func (s *NetworkService) Delete(id uint) error {
	if _, err := s.get(id); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("network_id = ?", id).Delete(&model.NetworkMember{}).Error; err != nil {
			return fmt.Errorf("删除群组成员关系失败: %w", err)
		}
		return tx.Delete(&model.Network{}, id).Error
	})
}

// AddMembers 将实例加入群组（幂等：已存在或不存在的实例跳过）。返回新增数与最新详情。
func (s *NetworkService) AddMembers(id uint, instanceIDs []uint) (int, *NetworkDetail, error) {
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
		s.db.Model(&model.NetworkMember{}).Where("network_id = ? AND instance_id = ?", id, iid).Count(&exists)
		if exists > 0 {
			continue
		}
		if err := s.db.Create(&model.NetworkMember{NetworkID: id, InstanceID: iid}).Error; err == nil {
			added++
		}
	}
	detail, err := s.Get(id)
	return added, detail, err
}

// RemoveMember 从群组移除一个实例（不影响实例本身）。
func (s *NetworkService) RemoveMember(id, instanceID uint) error {
	if _, err := s.get(id); err != nil {
		return err
	}
	return s.db.Where("network_id = ? AND instance_id = ?", id, instanceID).Delete(&model.NetworkMember{}).Error
}

// BatchAction 对群组成员批量执行生命周期操作（按标签批量运维）。
// 经 InstanceService 委托，逐个汇总成功/失败，不因单个失败中断。
func (s *NetworkService) BatchAction(id uint, action string) (*BatchActionResult, error) {
	if action != "start" && action != "stop" && action != "restart" {
		return nil, ErrInvalidBatchAction
	}
	detail, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	result := &BatchActionResult{Action: action, Total: len(detail.Members)}
	for _, m := range detail.Members {
		var aerr error
		switch action {
		case "start":
			aerr = s.instance.Start(m.InstanceID)
		case "stop":
			aerr = s.instance.Stop(m.InstanceID)
		case "restart":
			aerr = s.instance.Restart(m.InstanceID)
		}
		item := BatchActionItemResult{InstanceID: m.InstanceID, OK: aerr == nil}
		if aerr != nil {
			item.Error = aerr.Error()
			result.Failed++
		} else {
			result.Succeeded++
		}
		result.Results = append(result.Results, item)
	}
	return result, nil
}

func (s *NetworkService) get(id uint) (*model.Network, error) {
	var n model.Network
	if err := s.db.First(&n, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNetworkNotFound
		}
		return nil, fmt.Errorf("查询群组失败: %w", err)
	}
	return &n, nil
}
