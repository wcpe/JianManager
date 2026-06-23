package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ErrAlertChannelNotFound 通知通道不存在。
var ErrAlertChannelNotFound = errors.New("通知通道不存在")

// ErrAlertChannelInUse 通道被告警规则引用，禁止删除。
var ErrAlertChannelInUse = errors.New("通道被告警规则引用，无法删除")

// AlertChannelService 通知通道服务（FR-085）。负责通道 CRUD、配置校验与测试发送。
type AlertChannelService struct {
	db       *gorm.DB
	notifier *ChannelNotifier
}

// NewAlertChannelService 创建通知通道服务。
func NewAlertChannelService(db *gorm.DB) *AlertChannelService {
	return &AlertChannelService{db: db, notifier: NewChannelNotifier()}
}

// ChannelRequest 创建/更新通道请求。Config 为 ChannelConfig 的结构化字段（前端直接传对象）。
type ChannelRequest struct {
	Name    string         `json:"name" binding:"required"`
	Type    string         `json:"type" binding:"required"`
	Enabled *bool          `json:"enabled"`
	Config  ChannelConfig  `json:"config"`
}

// Create 创建通道。校验类型 + 凭证 ${ENV} 引用，配置序列化为 JSON 落库。
func (s *AlertChannelService) Create(req ChannelRequest) (*model.AlertChannel, error) {
	if err := validateChannelConfig(req.Type, &req.Config); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req.Config)
	if err != nil {
		return nil, fmt.Errorf("序列化通道配置失败: %w", err)
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ch := &model.AlertChannel{
		Name:    req.Name,
		Type:    req.Type,
		Enabled: enabled,
		Config:  string(raw),
	}
	if err := s.db.Create(ch).Error; err != nil {
		return nil, fmt.Errorf("创建通道失败: %w", err)
	}
	// GORM 的 `default:true` 标签会把结构体零值 false 当作未设置而回填默认 true，
	// 导致「创建即禁用」的通道被错误地存为启用。显式回写一次确保 enabled=false 落库。
	if !enabled {
		if err := s.db.Model(ch).Update("enabled", false).Error; err != nil {
			return nil, fmt.Errorf("创建通道失败: %w", err)
		}
	}
	return ch, nil
}

// List 返回所有通道。
func (s *AlertChannelService) List() ([]model.AlertChannel, error) {
	var channels []model.AlertChannel
	if err := s.db.Order("id DESC").Find(&channels).Error; err != nil {
		return nil, err
	}
	return channels, nil
}

// Get 按 ID 返回通道。
func (s *AlertChannelService) Get(id uint) (*model.AlertChannel, error) {
	var ch model.AlertChannel
	if err := s.db.First(&ch, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAlertChannelNotFound
		}
		return nil, err
	}
	return &ch, nil
}

// Update 更新通道。重新校验配置。
func (s *AlertChannelService) Update(id uint, req ChannelRequest) (*model.AlertChannel, error) {
	ch, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if err := validateChannelConfig(req.Type, &req.Config); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req.Config)
	if err != nil {
		return nil, fmt.Errorf("序列化通道配置失败: %w", err)
	}
	updates := map[string]interface{}{
		"name":   req.Name,
		"type":   req.Type,
		"config": string(raw),
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := s.db.Model(ch).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("更新通道失败: %w", err)
	}
	return s.Get(id)
}

// Delete 删除通道。被任一规则的 channelIds 引用时拒绝。
func (s *AlertChannelService) Delete(id uint) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	if s.isReferenced(id) {
		return ErrAlertChannelInUse
	}
	return s.db.Delete(&model.AlertChannel{}, id).Error
}

// isReferenced 判断通道是否被任一规则的 channelIds 列表引用。
func (s *AlertChannelService) isReferenced(id uint) bool {
	var rules []model.AlertRule
	if err := s.db.Select("channel_ids").Where("channel_ids != ''").Find(&rules).Error; err != nil {
		return false
	}
	for i := range rules {
		for _, cid := range parseChannelIDs(rules[i].ChannelIDs) {
			if cid == id {
				return true
			}
		}
	}
	return false
}

// TestSend 向通道发送一条测试通知，验证配置与连通性。
func (s *AlertChannelService) TestSend(id uint) error {
	ch, err := s.Get(id)
	if err != nil {
		return err
	}
	note := AlertNotification{
		Event:   "alert_test",
		RuleID:  "test",
		Title:   "JianManager 告警通道测试",
		Message: "这是一条测试通知，收到即表示通道配置正确。",
		Level:   model.AlertLevelInfo,
		Count:   1,
		Time:    time.Now(),
	}
	return s.notifier.Send(ch.Type, ch.Config, note)
}

// parseChannelIDs 解析规则 ChannelIDs JSON 串（"[1,2]"）为 ID 列表。空/非法返回空。
func parseChannelIDs(raw string) []uint {
	if raw == "" {
		return nil
	}
	var ids []uint
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}
