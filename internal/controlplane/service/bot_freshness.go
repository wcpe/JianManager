package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-365 状态新鲜度窗口：快照周期约 3 秒，默认 10 秒未刷新则从 connected/connecting 收敛。
const (
	botFreshnessStaleWindow    = 10 * time.Second
	botFreshnessMissingWindow  = 90 * time.Second
	botFreshnessSweepInterval  = 3 * time.Second
	botStatusErrorCodeStale    = "STATUS_STALE"
	botStatusErrorCodeMissing  = "RUNTIME_MISSING"
	botStatusErrorCodeOffline  = "EXECUTOR_OFFLINE"
)

// BotFreshnessRepository 封装新鲜度批量归真所需的数据访问。
type BotFreshnessRepository interface {
	// MarkStale 将 LastSeenAt 超窗且 desired=running 的 connected/connecting 收敛为 disconnected。
	MarkStale(ctx context.Context, now time.Time, window time.Duration) (int64, error)
	// MarkRuntimeMissing 将超长未出现在 snapshot 且节点在线的 Bot 标为 error。
	MarkRuntimeMissing(ctx context.Context, now time.Time, window time.Duration) (int64, error)
	// MarkExecutorOffline 节点离线时批量收敛该节点上活动 Bot，聚合一条日志上下文。
	MarkExecutorOffline(ctx context.Context, nodeID uint, now time.Time) (int64, error)
}

type gormBotFreshnessRepository struct{ db *gorm.DB }

func newGormBotFreshnessRepository(db *gorm.DB) *gormBotFreshnessRepository {
	return &gormBotFreshnessRepository{db: db}
}

func (r *gormBotFreshnessRepository) MarkStale(ctx context.Context, now time.Time, window time.Duration) (int64, error) {
	cutoff := now.Add(-window)
	result := r.db.WithContext(ctx).Model(&model.Bot{}).
		Where("deleted_at IS NULL").
		Where("desired_state = ?", model.BotDesiredRunning).
		Where("status IN ?", []model.BotStatus{model.BotStatusConnected, model.BotStatusConnecting}).
		Where("last_seen_at IS NOT NULL AND last_seen_at < ?", cutoff).
		Updates(map[string]any{
			"status":     model.BotStatusDisconnected,
			"last_error": botStatusErrorCodeStale,
		})
	return result.RowsAffected, result.Error
}

func (r *gormBotFreshnessRepository) MarkRuntimeMissing(ctx context.Context, now time.Time, window time.Duration) (int64, error) {
	cutoff := now.Add(-window)
	// 节点在线但 runtime 长期缺失：error RUNTIME_MISSING。
	result := r.db.WithContext(ctx).Model(&model.Bot{}).
		Where("deleted_at IS NULL").
		Where("desired_state = ?", model.BotDesiredRunning).
		Where("status IN ?", []model.BotStatus{model.BotStatusDisconnected, model.BotStatusConnecting, model.BotStatusConnected}).
		Where("last_seen_at IS NOT NULL AND last_seen_at < ?", cutoff).
		Where(`executor_node_id IN (
			SELECT id FROM nodes WHERE deleted_at IS NULL AND status = ?
		)`, model.NodeStatusOnline).
		Updates(map[string]any{
			"status":     model.BotStatusError,
			"last_error": botStatusErrorCodeMissing,
		})
	return result.RowsAffected, result.Error
}

func (r *gormBotFreshnessRepository) MarkExecutorOffline(ctx context.Context, nodeID uint, now time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Model(&model.Bot{}).
		Where("deleted_at IS NULL").
		Where("desired_state = ?", model.BotDesiredRunning).
		Where("status IN ?", []model.BotStatus{model.BotStatusConnected, model.BotStatusConnecting, model.BotStatusPending}).
		Where("executor_node_id = ? OR (executor_node_id IS NULL AND instance_id IN (?))",
			nodeID, r.db.Model(&model.Instance{}).Select("id").Where("node_id = ?", nodeID)).
		Updates(map[string]any{
			"status":     model.BotStatusDisconnected,
			"last_error": botStatusErrorCodeOffline,
			"last_seen_at": gorm.Expr("CASE WHEN last_seen_at IS NULL OR last_seen_at < ? THEN ? ELSE last_seen_at END", now, now),
		})
	return result.RowsAffected, result.Error
}

// BotFreshnessService 按时间窗将幽灵在线收敛为 disconnected/error（FR-365）。
type BotFreshnessService struct {
	repository BotFreshnessRepository
	clock      BotLoadClock
	stale      time.Duration
	missing    time.Duration
}

// NewBotFreshnessService 创建默认 10s/90s 窗口的新鲜度服务。
func NewBotFreshnessService(db *gorm.DB, clock BotLoadClock) *BotFreshnessService {
	return NewBotFreshnessServiceWithRepository(newGormBotFreshnessRepository(db), clock, botFreshnessStaleWindow, botFreshnessMissingWindow)
}

// NewBotFreshnessServiceWithRepository 使用可注入仓储与窗口，便于测试。
func NewBotFreshnessServiceWithRepository(repository BotFreshnessRepository, clock BotLoadClock, stale, missing time.Duration) *BotFreshnessService {
	if stale <= 0 {
		stale = botFreshnessStaleWindow
	}
	if missing <= 0 {
		missing = botFreshnessMissingWindow
	}
	return &BotFreshnessService{
		repository: repository, clock: normalizeBotLoadClock(clock),
		stale: stale, missing: missing,
	}
}

// Sweep 执行一轮新鲜度归真：先 STATUS_STALE，再 RUNTIME_MISSING。
func (s *BotFreshnessService) Sweep(ctx context.Context) error {
	if s == nil || s.repository == nil {
		return nil
	}
	now := s.clock.Now().UTC()
	staleN, err := s.repository.MarkStale(ctx, now, s.stale)
	if err != nil {
		return fmt.Errorf("新鲜度 STATUS_STALE 归真失败: %w", err)
	}
	missingN, err := s.repository.MarkRuntimeMissing(ctx, now, s.missing)
	if err != nil {
		return fmt.Errorf("新鲜度 RUNTIME_MISSING 归真失败: %w", err)
	}
	if staleN > 0 || missingN > 0 {
		slog.Info("Bot 状态新鲜度归真完成", "stale", staleN, "missing", missingN)
	}
	return nil
}

// MarkNodeOffline 节点离线时聚合收敛，避免逐 Bot 刷屏。
func (s *BotFreshnessService) MarkNodeOffline(ctx context.Context, nodeID uint) error {
	if s == nil || s.repository == nil || nodeID == 0 {
		return nil
	}
	n, err := s.repository.MarkExecutorOffline(ctx, nodeID, s.clock.Now().UTC())
	if err != nil {
		return fmt.Errorf("节点离线 Bot 归真失败: %w", err)
	}
	if n > 0 {
		slog.Warn("执行节点离线，批量收敛 Bot 状态", "nodeId", nodeID, "count", n, "category", botStatusErrorCodeOffline)
	}
	return nil
}

// BotFreshnessSweeper 周期性巡检活动 Bot 新鲜度；Stop 后释放 goroutine。
type BotFreshnessSweeper struct {
	service  *BotFreshnessService
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewBotFreshnessSweeper 创建默认 3 秒周期的新鲜度巡检器。
func NewBotFreshnessSweeper(service *BotFreshnessService) *BotFreshnessSweeper {
	return &BotFreshnessSweeper{service: service, interval: botFreshnessSweepInterval}
}

// Start 启动后台巡检；重复 Start 幂等。
func (s *BotFreshnessSweeper) Start() {
	if s == nil || s.service == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	interval := s.interval
	if interval <= 0 {
		interval = botFreshnessSweepInterval
	}
	go s.loop(ctx, interval)
}

func (s *BotFreshnessSweeper) loop(ctx context.Context, interval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.service.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("Bot 新鲜度巡检失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Stop 取消巡检并等待 goroutine 退出。
func (s *BotFreshnessSweeper) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
