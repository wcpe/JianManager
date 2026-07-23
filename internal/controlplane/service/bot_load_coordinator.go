package service

import (
	"context"
	"fmt"
	"sync"
)

// BotLoadRunCoordinator 进程内 V2 运行注册表（FR-370 partial）。
// 完整 runner 循环（分片/采样/SSE）后续迭代；本版提供启停登记与恢复扫描钩子。
type BotLoadRunCoordinator struct {
	mu      sync.Mutex
	active  map[uint]context.CancelFunc
	intents *BotLoadRunIntentService
}

// NewBotLoadRunCoordinator 创建协调器。
func NewBotLoadRunCoordinator(intents *BotLoadRunIntentService) *BotLoadRunCoordinator {
	return &BotLoadRunCoordinator{
		active:  make(map[uint]context.CancelFunc),
		intents: intents,
	}
}

// RegisterStarting 登记 starting 运行；重复 start 返回错误。
func (c *BotLoadRunCoordinator) RegisterStarting(sessionID uint) (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.active[sessionID]; ok {
		return nil, fmt.Errorf("%w: 运行已在协调器中", ErrBotLoadInvalidState)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.active[sessionID] = cancel
	return ctx, nil
}

// Unregister 移除登记（终态或失败时调用）。
func (c *BotLoadRunCoordinator) Unregister(sessionID uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cancel, ok := c.active[sessionID]; ok {
		cancel()
		delete(c.active, sessionID)
	}
}

// Cancel 取消登记中的运行上下文。
func (c *BotLoadRunCoordinator) Cancel(sessionID uint) {
	c.Unregister(sessionID)
}

// IsActive 是否已登记。
func (c *BotLoadRunCoordinator) IsActive(sessionID uint) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.active[sessionID]
	return ok
}

// ActiveCount 当前登记数（测试/诊断）。
func (c *BotLoadRunCoordinator) ActiveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.active)
}

// RequestStop 对 V2 会话发 stop intent 并取消协调器上下文。
func (c *BotLoadRunCoordinator) RequestStop(ctx context.Context, sessionID uint, reason string) error {
	if c.intents == nil {
		return fmt.Errorf("意图服务未配置")
	}
	if _, err := c.intents.AcceptStop(ctx, sessionID, reason); err != nil {
		return err
	}
	c.Cancel(sessionID)
	return nil
}

// RequestCancel 对 V2 会话发 cancel intent 并取消协调器上下文。
func (c *BotLoadRunCoordinator) RequestCancel(ctx context.Context, sessionID uint, reason string) error {
	if c.intents == nil {
		return fmt.Errorf("意图服务未配置")
	}
	if _, err := c.intents.AcceptCancel(ctx, sessionID, reason); err != nil {
		return err
	}
	c.Cancel(sessionID)
	return nil
}
