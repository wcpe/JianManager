package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
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

// NetworkSummary 群组列表项（含成员数）。
type NetworkSummary struct {
	ID          uint      `json:"id"`
	UUID        string    `json:"uuid"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	MemberCount int       `json:"memberCount"`
	CreatedAt   time.Time `json:"createdAt"`
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

// List 返回所有群组及成员数（新→旧）。
func (s *NetworkService) List() ([]NetworkSummary, error) {
	var networks []model.Network
	if err := s.db.Order("created_at desc").Find(&networks).Error; err != nil {
		return nil, fmt.Errorf("查询群组列表失败: %w", err)
	}
	out := make([]NetworkSummary, 0, len(networks))
	for _, n := range networks {
		var cnt int64
		s.db.Model(&model.NetworkMember{}).Where("network_id = ?", n.ID).Count(&cnt)
		out = append(out, NetworkSummary{
			ID:          n.ID,
			UUID:        n.UUID,
			Name:        n.Name,
			Description: n.Description,
			MemberCount: int(cnt),
			CreatedAt:   n.CreatedAt,
		})
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
