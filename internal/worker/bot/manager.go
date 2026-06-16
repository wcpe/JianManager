/**
 * Bot 管理器。
 * 负责 spawn Bot Worker 子进程（Node.js），通过 stdin/stdout JSON 行协议通信，
 * 管理 Bot 生命周期。
 *
 * 架构：Worker Node → (exec + IPC) → Bot Worker (Node.js)
 * 遵循 ADR-006: Bot 必须通过 Node.js 子进程 + stdin/stdout IPC。
 */

package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// BotState Bot 状态。
type BotState struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`
	Name     string  `json:"name,omitempty"`
	Health   float64 `json:"health,omitempty"`
	Food     int     `json:"food,omitempty"`
	Position *Vec3   `json:"position,omitempty"`
	Behavior string  `json:"behavior,omitempty"`
}

// BotEvent Bot 事件。
type BotEvent struct {
	BotID string                 `json:"botId"`
	Type  string                 `json:"type"`
	Data  map[string]interface{} `json:"data,omitempty"`
}

// ScriptProgress 脚本进度。
type ScriptProgress struct {
	ScriptID string `json:"scriptId"`
	BotID    string `json:"botId,omitempty"`
	Progress int    `json:"progress"`
	Total    int    `json:"total"`
	Status   string `json:"status"`
	Step     string `json:"step,omitempty"`
	Error    string `json:"error,omitempty"`
}

// BotWorkerEvent Bot Worker 发出的事件（JSON 解码目标）。
type BotWorkerEvent struct {
	Evt      string          `json:"evt"`
	Bots     []BotState      `json:"bots,omitempty"`
	BotID    string          `json:"botId,omitempty"`
	Type     string          `json:"type,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Error    string          `json:"error,omitempty"`
	ScriptID string          `json:"scriptId,omitempty"`
	Progress int             `json:"progress,omitempty"`
	Total    int             `json:"total,omitempty"`
	Status   string          `json:"status,omitempty"`
	Step     string          `json:"step,omitempty"`
}

// EventCallback 事件回调函数。
type EventCallback func(event *BotWorkerEvent)

// Manager Bot 管理器。
// spawn 一个 Bot Worker 子进程，通过 stdin/stdout JSON 行协议通信。
type Manager struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     *json.Encoder
	stdout    *bufio.Scanner
	running   bool
	cancel    context.CancelFunc
	bots      map[string]*BotState
	onEvent   EventCallback
	prewarm   int
	botWorker string // bot-worker 脚本路径
}

// ManagerConfig 管理器配置。
type ManagerConfig struct {
	BotWorkerPath string // bot-worker 入口脚本路径（dist/index.js）
	PrewarmCount  int    // 预热 Bot 数量
}

// NewManager 创建 Bot 管理器。
func NewManager(config ManagerConfig) *Manager {
	return &Manager{
		bots:      make(map[string]*BotState),
		prewarm:   config.PrewarmCount,
		botWorker: config.BotWorkerPath,
	}
}

// SetEventCallback 设置事件回调。
func (m *Manager) SetEventCallback(cb EventCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onEvent = cb
}

// Start 启动 Bot Worker 子进程。
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("Bot 管理器已在运行")
	}

	ctx, m.cancel = context.WithCancel(ctx)

	args := []string{m.botWorker}
	if m.prewarm > 0 {
		args = append(args, fmt.Sprintf("--prewarm=%d", m.prewarm))
	}

	m.cmd = exec.CommandContext(ctx, "node", args...)

	stdin, err := m.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("创建 stdin 管道失败: %w", err)
	}
	m.stdin = json.NewEncoder(stdin)

	stdout, err := m.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	m.stdout = bufio.NewScanner(stdout)
	// 增大扫描缓冲区，避免长行被截断
	m.stdout.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("启动 Bot Worker 失败: %w", err)
	}

	m.running = true
	slog.Info("Bot Worker 已启动", "pid", m.cmd.Process.Pid)

	// 启动事件读取循环
	go m.readLoop()

	return nil
}

// Stop 停止 Bot 管理器和 Bot Worker 子进程。
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return
	}

	m.running = false
	if m.cancel != nil {
		m.cancel()
	}

	// 等待子进程退出
	done := make(chan error, 1)
	go func() {
		done <- m.cmd.Wait()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if m.cmd.Process != nil {
			_ = m.cmd.Process.Kill()
		}
	}

	slog.Info("Bot Worker 已停止")
}

// IsRunning 是否在运行。
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// sendCommand 向 Bot Worker 发送命令。
func (m *Manager) sendCommand(cmd interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return fmt.Errorf("Bot Worker 未运行")
	}

	return m.stdin.Encode(cmd)
}

// CreateBots 批量创建 Bot。
func (m *Manager) CreateBots(configs []BotConfig) error {
	return m.sendCommand(CreateBotsCommand{
		Cmd:  "create-bots",
		Bots: configs,
	})
}

// StopBots 批量停止 Bot。
func (m *Manager) StopBots(botIds []string) error {
	return m.sendCommand(StopBotsCommand{
		Cmd:    "stop-bots",
		BotIds: botIds,
	})
}

// SetBehavior 切换 Bot 行为模式。
func (m *Manager) SetBehavior(botID, behavior, target string) error {
	return m.sendCommand(SetBehaviorCommand{
		Cmd:      "set-behavior",
		BotID:    botID,
		Behavior: behavior,
		Target:   target,
	})
}

// SendBotCommand 向 Bot 发送命令。
func (m *Manager) SendBotCommand(botID, command string) error {
	return m.sendCommand(SendBotCommand{
		Cmd:     "send-command",
		BotID:   botID,
		Command: command,
	})
}

// RunScript 执行脚本。
func (m *Manager) RunScript(scriptID string, steps []ScriptStep, botIds []string) error {
	return m.sendCommand(RunScriptCommand{
		Cmd:      "run-script",
		ScriptID: scriptID,
		Steps:    steps,
		BotIds:   botIds,
	})
}

// StopScript 停止脚本。
func (m *Manager) StopScript(scriptID string) error {
	return m.sendCommand(StopScriptCommand{
		Cmd:      "stop-script",
		ScriptID: scriptID,
	})
}

// GetBots 获取所有 Bot 状态。
func (m *Manager) GetBots() map[string]*BotState {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make(map[string]*BotState, len(m.bots))
	for k, v := range m.bots {
		cp := *v
		result[k] = &cp
	}
	return result
}

// GetBot 获取单个 Bot 状态。
func (m *Manager) GetBot(botID string) (*BotState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.bots[botID]
	if !ok {
		return nil, false
	}
	cp := *s
	return &cp, true
}

// readLoop 读取 Bot Worker stdout 事件。
func (m *Manager) readLoop() {
	for m.stdout.Scan() {
		line := m.stdout.Text()
		if line == "" {
			continue
		}

		var event BotWorkerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			slog.Warn("解析 Bot Worker 事件失败", "error", err, "line", line)
			continue
		}

		m.handleEvent(&event)
	}

	if err := m.stdout.Err(); err != nil {
		slog.Error("Bot Worker stdout 读取错误", "error", err)
	}
}

// handleEvent 处理 Bot Worker 事件。
func (m *Manager) handleEvent(event *BotWorkerEvent) {
	m.mu.Lock()

	switch event.Evt {
	case "bot-state":
		for _, bs := range event.Bots {
			if existing, ok := m.bots[bs.ID]; ok {
				existing.Status = bs.Status
				if bs.Health != 0 {
					existing.Health = bs.Health
				}
				if bs.Food != 0 {
					existing.Food = bs.Food
				}
				if bs.Position != nil {
					existing.Position = bs.Position
				}
				if bs.Behavior != "" {
					existing.Behavior = bs.Behavior
				}
			} else {
				m.bots[bs.ID] = &bs
			}
		}

	case "bot-event", "bot-error", "script-progress", "heartbeat", "worker-ready":
		// 事件直接转发给回调
	}

	cb := m.onEvent
	m.mu.Unlock()

	if cb != nil {
		cb(event)
	}
}
