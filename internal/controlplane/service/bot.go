package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

var ErrBotNotFound = errors.New("Bot 不存在")

// BotService Bot 管理服务。
type BotService struct {
	db *gorm.DB
}

// NewBotService 创建 Bot 服务。
func NewBotService(db *gorm.DB) *BotService {
	return &BotService{db: db}
}

// CreateBotRequest 创建 Bot 请求。
type CreateBotRequest struct {
	InstanceID uint   `json:"instanceId" binding:"required"`
	Name       string `json:"name" binding:"required"`
	Config     string `json:"config"` // JSON
	Behavior   string `json:"behavior"`
}

// Create 创建 Bot。
func (s *BotService) Create(req CreateBotRequest) (*model.Bot, error) {
	bot := &model.Bot{
		InstanceID: req.InstanceID,
		Name:       req.Name,
		Config:     req.Config,
		Behavior:   req.Behavior,
		Status:     model.BotStatusPending,
	}
	if err := s.db.Create(bot).Error; err != nil {
		return nil, fmt.Errorf("创建 Bot 失败: %w", err)
	}
	return bot, nil
}

// List 返回 Bot 列表。
func (s *BotService) List(instanceID *uint, status *model.BotStatus) ([]model.Bot, error) {
	var bots []model.Bot
	q := s.db.Model(&model.Bot{})
	if instanceID != nil {
		q = q.Where("instance_id = ?", *instanceID)
	}
	if status != nil {
		q = q.Where("status = ?", *status)
	}
	if err := q.Find(&bots).Error; err != nil {
		return nil, fmt.Errorf("查询 Bot 列表失败: %w", err)
	}
	return bots, nil
}

// GetByID 获取 Bot 详情。
func (s *BotService) GetByID(id uint) (*model.Bot, error) {
	var bot model.Bot
	if err := s.db.First(&bot, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotNotFound
		}
		return nil, err
	}
	return &bot, nil
}

// Delete 删除 Bot。
func (s *BotService) Delete(id uint) error {
	return s.db.Delete(&model.Bot{}, id).Error
}

// UpdateBehavior 更新 Bot 行为。
func (s *BotService) UpdateBehavior(id uint, behavior string) error {
	return s.db.Model(&model.Bot{}).Where("id = ?", id).Update("behavior", behavior).Error
}
