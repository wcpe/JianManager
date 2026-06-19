package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

var ErrBotNotFound = errors.New("Bot 不存在")

// botConnConfig 解析 Bot.Config(JSON)中的连接参数，用于向 Worker 下发 Mineflayer 连接目标。
type botConnConfig struct {
	Server   string `json:"server"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Version  string `json:"version"`
	Auth     string `json:"auth"`
}

// parseBotConn 解析连接配置；非法/空 JSON 返回零值（由调用方回退默认）。
func parseBotConn(cfg string) botConnConfig {
	var c botConnConfig
	if cfg != "" {
		_ = json.Unmarshal([]byte(cfg), &c)
	}
	return c
}

// botConnTarget 据 Bot.Config 与所属实例解析 Mineflayer 连接目标（host/port + 其余连接配置）。
// 缺省回环 + 实例 server_port，避免 Bot 连到错误端口；创建与重连复用同一逻辑。
func botConnTarget(bot *model.Bot, instance *model.Instance) (host string, port int, conn botConnConfig) {
	conn = parseBotConn(bot.Config)
	host = conn.Server
	if host == "" {
		host = conn.Host
	}
	if host == "" {
		host = "127.0.0.1"
	}
	port = conn.Port
	if port == 0 && instance != nil {
		port = instance.ServerPort
	}
	if port == 0 {
		port = 25565
	}
	// 默认用 Bot 名作为游戏内用户名（否则 bot-worker 用 Bot_xxxx 默认名）。
	if conn.Username == "" {
		conn.Username = sanitizeMCUsername(bot.Name)
	}
	return host, port, conn
}

// sanitizeMCUsername 规整为合法 MC 用户名（[A-Za-z0-9_]，≤16 位）；无合法字符返回空，
// 交由 bot-worker 兜底默认名。
func sanitizeMCUsername(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() >= 16 {
				break
			}
		}
	}
	return b.String()
}

// mapWorkerBotStatus 把 Worker/bot-worker 上报的状态字符串映射为模型状态枚举。
func mapWorkerBotStatus(s string) model.BotStatus {
	switch s {
	case "connecting":
		return model.BotStatusConnecting
	case "connected":
		return model.BotStatusConnected
	case "disconnected":
		return model.BotStatusDisconnected
	case "error":
		return model.BotStatusError
	case "stopped":
		return model.BotStatusStopped
	default:
		return model.BotStatus(s)
	}
}

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
	s.refreshStatus(&bot)
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

	// 解析连接目标：优先 Config 显式指定，否则回退到实例监听端口（同机回环）。
	host, port, conn := botConnTarget(bot, instance)

	resp, err := client.Worker.CreateBot(ctx, &workerpb.CreateBotRequest{
		BotUuid:      bot.UUID,
		InstanceUuid: instance.UUID,
		Name:         bot.Name,
		Host:         host,
		Port:         int32(port),
		Username:     conn.Username,
		Version:      conn.Version,
		Auth:         conn.Auth,
		Behavior:     bot.Behavior,
	})
	if err != nil {
		return fmt.Errorf("gRPC CreateBot 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker CreateBot 失败: %s", resp.Error)
	}

	// Bot 连接是异步的：先置 connecting，真实状态由读取时经 ListBots 拉取回填（refreshStatus）。
	status := model.BotStatusConnecting
	if resp.Status != "" {
		status = mapWorkerBotStatus(resp.Status)
	}
	_ = s.db.Model(bot).Update("status", status).Error
	return nil
}

// refreshStatus 从所属 Worker 拉取该 Bot 的实时状态并回填 DB（懒拉取，读取时触发）。
// Worker 离线或 Bot 不在 Worker 列表中时保留上次已知状态，不抹除。
// 状态源头：bot-worker(Mineflayer 事件) → Worker bot.Manager → 本方法。
func (s *BotService) refreshStatus(bot *model.Bot) {
	client, _, err := s.getWorkerClient(bot.InstanceID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.Worker.ListBots(ctx, &workerpb.ListBotsRequest{})
	if err != nil {
		return
	}
	for _, bi := range resp.Bots {
		if bi.BotUuid != bot.UUID {
			continue
		}
		st := mapWorkerBotStatus(bi.Status)
		if st != "" && st != bot.Status {
			bot.Status = st
			_ = s.db.Model(bot).Update("status", st).Error
		}
		return
	}
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
