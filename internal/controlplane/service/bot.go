package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

var ErrBotNotFound = errors.New("Bot 不存在")

// BotService Bot 管理服务。
type BotService struct {
	db   *gorm.DB
	pool *grpc.ClientPool
}

// NewBotService 创建 Bot 服务。
func NewBotService(db *gorm.DB, pool *grpc.ClientPool) *BotService {
	return &BotService{db: db, pool: pool}
}

// CreateBotRequest 创建 Bot 请求。
type CreateBotRequest struct {
	InstanceID uint   `json:"instanceId" binding:"required"`
	Name       string `json:"name" binding:"required"`
	Config     string `json:"config"` // JSON
	Behavior   string `json:"behavior"`
}

// Create 创建 Bot 并委托 Worker 启动 Mineflayer 连接。
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

	// 委托 Worker 创建 Bot 连接（失败不阻塞 DB 创建，记 warning）
	if err := s.delegateCreateBot(bot); err != nil {
		slog.Warn("委托 Worker 创建 Bot 失败", "botID", bot.ID, "error", err)
	}

	return bot, nil
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

// Delete 删除 Bot 并委托 Worker 停止连接。
func (s *BotService) Delete(id uint) error {
	var bot model.Bot
	if err := s.db.First(&bot, id).Error; err != nil {
		return ErrBotNotFound
	}

	if err := s.delegateDeleteBot(&bot); err != nil {
		slog.Warn("委托 Worker 删除 Bot 失败", "botID", id, "error", err)
	}

	return s.db.Delete(&model.Bot{}, id).Error
}

// UpdateBehavior 更新 Bot 行为并委托 Worker 切换。
func (s *BotService) UpdateBehavior(id uint, behavior string) error {
	var bot model.Bot
	if err := s.db.First(&bot, id).Error; err != nil {
		return ErrBotNotFound
	}

	if err := s.db.Model(&bot).Update("behavior", behavior).Error; err != nil {
		return err
	}

	if err := s.delegateSetBehavior(&bot, behavior); err != nil {
		slog.Warn("委托 Worker 切换行为失败", "botID", id, "error", err)
	}

	return nil
}

// delegateCreateBot 委托 Worker 创建 Bot 连接。
func (s *BotService) delegateCreateBot(bot *model.Bot) error {
	client, instance, err := s.getWorkerClient(bot.InstanceID)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.CreateBot(ctx, &workerpb.CreateBotRequest{
		BotUuid:        bot.UUID,
		InstanceUuid:   instance.UUID,
		Name:           bot.Name,
		Behavior:       bot.Behavior,
		BehaviorConfig: bot.Config,
	})
	if err != nil {
		return fmt.Errorf("gRPC CreateBot 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker CreateBot 失败: %s", resp.Error)
	}

	_ = s.db.Model(bot).Update("status", model.BotStatusConnected).Error
	return nil
}

// delegateDeleteBot 委托 Worker 停止 Bot。
func (s *BotService) delegateDeleteBot(bot *model.Bot) error {
	client, _, err := s.getWorkerClient(bot.InstanceID)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.DeleteBot(ctx, &workerpb.DeleteBotRequest{BotUuid: bot.UUID})
	if err != nil {
		return fmt.Errorf("gRPC DeleteBot 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker DeleteBot 失败: %s", resp.Error)
	}
	return nil
}

// delegateSetBehavior 委托 Worker 切换 Bot 行为。
func (s *BotService) delegateSetBehavior(bot *model.Bot, behavior string) error {
	client, _, err := s.getWorkerClient(bot.InstanceID)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.SetBotBehavior(ctx, &workerpb.SetBotBehaviorRequest{
		BotUuid:  bot.UUID,
		Behavior: behavior,
	})
	if err != nil {
		return fmt.Errorf("gRPC SetBotBehavior 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker SetBotBehavior 失败: %s", resp.Error)
	}
	return nil
}

// getWorkerClient 根据实例 ID 获取 Worker gRPC 客户端和实例信息。
func (s *BotService) getWorkerClient(instanceID uint) (*grpc.Client, *model.Instance, error) {
	var instance model.Instance
	if err := s.db.Preload("Node").First(&instance, instanceID).Error; err != nil {
		return nil, nil, fmt.Errorf("实例不存在: %w", err)
	}

	client, ok := s.pool.Get(instance.Node.UUID)
	if !ok {
		return nil, nil, fmt.Errorf("Worker %s 未连接", instance.Node.UUID)
	}

	return client, &instance, nil
}
