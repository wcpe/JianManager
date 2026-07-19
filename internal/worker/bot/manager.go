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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
)

// BotState Bot 状态。
type BotState struct {
	ID                    string  `json:"id"`
	Status                string  `json:"status"`
	Name                  string  `json:"name,omitempty"`
	Health                float64 `json:"health,omitempty"`
	Food                  int     `json:"food,omitempty"`
	Position              *Vec3   `json:"position,omitempty"`
	Behavior              string  `json:"behavior,omitempty"`
	SessionID             string  `json:"sessionId,omitempty"`
	Generation            int64   `json:"generation,omitempty"`
	ConfigHash            string  `json:"configHash,omitempty"`
	WorkerEpoch           string  `json:"workerEpoch,omitempty"`
	WorkerEpochGeneration int64   `json:"workerEpochGeneration,omitempty"`
	EventSeq              int64   `json:"eventSeq,omitempty"`
	CurrentStepID         string  `json:"currentStepId,omitempty"`
	ReconnectCount        int     `json:"reconnectCount,omitempty"`
	ErrorCode             string  `json:"errorCode,omitempty"`
	LastError             string  `json:"lastError,omitempty"`
	ObservedAt            int64   `json:"observedAt,omitempty"`
}

// BotEvent Bot 事件。
type BotEvent struct {
	BotID string                 `json:"botId"`
	Type  string                 `json:"type"`
	Data  map[string]interface{} `json:"data,omitempty"`
}

// ActionEvent 是 Bot Worker 上报的类型化动作事件。
type ActionEvent struct {
	BotID            string          `json:"botId"`
	SessionID        string          `json:"sessionId"`
	Generation       int64           `json:"generation"`
	ActionRunID      string          `json:"actionRunId"`
	StepID           string          `json:"stepId"`
	Attempt          int             `json:"attempt"`
	Status           string          `json:"status"`
	ErrorCode        string          `json:"errorCode,omitempty"`
	Message          string          `json:"message,omitempty"`
	CorrelationToken string          `json:"correlationToken,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	DurationMS       int64           `json:"durationMs,omitempty"`
	ObservedAt       int64           `json:"observedAt,omitempty"`
}

// BotCapacitySnapshot 描述当前 bot-worker 的 fleet 准入与利用率。
type BotCapacitySnapshot struct {
	Ready                 bool
	Legacy                bool
	MaxBots               int
	ActiveBots            int
	ConnectingBots        int
	CapacityGeneration    int64
	WorkerEpoch           string
	WorkerEpochGeneration int64
	BotWorkerVersion      string
	RSSBytes              int64
	EventLoopP95Ms        float64
	DroppedEvents         int64
	Features              []string
	ObservedAt            time.Time
	UnavailableReason     string
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
	Evt                   string             `json:"evt"`
	RequestID             string             `json:"requestId,omitempty"`
	BatchID               string             `json:"batchId,omitempty"`
	IdempotencyKey        string             `json:"idempotencyKey,omitempty"`
	Bots                  []BotState         `json:"bots,omitempty"`
	Results               []BotItemResult    `json:"results,omitempty"`
	SignalResults         []SignalItemResult `json:"signalResults,omitempty"`
	Action                *ActionEvent       `json:"action,omitempty"`
	BotID                 string             `json:"botId,omitempty"`
	Type                  string             `json:"type,omitempty"`
	Data                  json.RawMessage    `json:"data,omitempty"`
	Error                 string             `json:"error,omitempty"`
	ScriptID              string             `json:"scriptId,omitempty"`
	Progress              int                `json:"progress,omitempty"`
	Total                 int                `json:"total,omitempty"`
	Status                string             `json:"status,omitempty"`
	Step                  string             `json:"step,omitempty"`
	WorkerEpoch           string             `json:"workerEpoch,omitempty"`
	WorkerEpochGeneration int64              `json:"workerEpochGeneration,omitempty"`
	BotWorkerVersion      string             `json:"botWorkerVersion,omitempty"`
	MaxBots               int                `json:"maxBots,omitempty"`
	Features              []string           `json:"features,omitempty"`
	ActiveBots            int                `json:"activeBots,omitempty"`
	ConnectingBots        int                `json:"connectingBots,omitempty"`
	RSSBytes              int64              `json:"rssBytes,omitempty"`
	EventLoopP95Ms        float64            `json:"eventLoopP95Ms,omitempty"`
	DroppedEvents         int64              `json:"droppedEvents,omitempty"`
	CapacityGeneration    int64              `json:"capacityGeneration,omitempty"`
}

// EventCallback 事件回调函数。
type EventCallback func(event *BotWorkerEvent)

type pendingRequestResult struct {
	event *BotWorkerEvent
	err   error
}

const eventSubscriberQueueLimit = 4096

type eventSubscriber struct {
	mu       sync.Mutex
	out      chan *BotWorkerEvent
	wake     chan struct{}
	done     chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
	queue    []*BotWorkerEvent
	limit    int
}

func newEventSubscriber(buffer int) *eventSubscriber {
	limit := eventSubscriberQueueLimit
	if buffer > limit {
		limit = buffer
	}
	subscriber := &eventSubscriber{
		out: make(chan *BotWorkerEvent, buffer), wake: make(chan struct{}, 1),
		done: make(chan struct{}), stopped: make(chan struct{}), limit: limit,
	}
	go subscriber.run()
	return subscriber
}

func (s *eventSubscriber) run() {
	defer close(s.stopped)
	defer close(s.out)
	for {
		event := s.next()
		if event == nil {
			select {
			case <-s.wake:
				continue
			case <-s.done:
				return
			}
		}
		select {
		case s.out <- event:
		case <-s.done:
			return
		}
	}
}

func (s *eventSubscriber) next() *BotWorkerEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return nil
	}
	event := s.queue[0]
	s.queue[0] = nil
	s.queue = s.queue[1:]
	return event
}

func (s *eventSubscriber) enqueue(event *BotWorkerEvent) bool {
	select {
	case <-s.done:
		return false
	default:
	}
	bufferedReliable := s.drainBufferedForExit(event)
	s.mu.Lock()
	if len(bufferedReliable) > 0 {
		s.queue = append(bufferedReliable, s.queue...)
	}
	if event != nil && event.Evt == "worker-exit" {
		s.dropQueuedRuntimeLocked()
	}
	accepted := s.enqueueLocked(event)
	s.mu.Unlock()
	if accepted {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return accepted
}

func (s *eventSubscriber) drainBufferedForExit(event *BotWorkerEvent) []*BotWorkerEvent {
	if event == nil || event.Evt != "worker-exit" {
		return nil
	}
	retained := make([]*BotWorkerEvent, 0, cap(s.out))
	for {
		select {
		case buffered, ok := <-s.out:
			if !ok {
				return retained
			}
			if buffered != nil && buffered.Evt == "action-event" {
				retained = append(retained, buffered)
			}
		default:
			return retained
		}
	}
}

func (s *eventSubscriber) dropQueuedRuntimeLocked() {
	retained := s.queue[:0]
	for _, queued := range s.queue {
		if isReliableBotEvent(queued) {
			retained = append(retained, queued)
		}
	}
	for i := len(retained); i < len(s.queue); i++ {
		s.queue[i] = nil
	}
	s.queue = retained
}

func (s *eventSubscriber) enqueueLocked(event *BotWorkerEvent) bool {
	if !isReliableBotEvent(event) && s.mergeRuntimeLocked(event) {
		return true
	}
	if len(s.queue) < s.limit {
		s.queue = append(s.queue, event)
		return true
	}
	if !isReliableBotEvent(event) {
		return true
	}
	for i, queued := range s.queue {
		if !isReliableBotEvent(queued) {
			copy(s.queue[i:], s.queue[i+1:])
			s.queue[len(s.queue)-1] = event
			return true
		}
	}
	return false
}

func (s *eventSubscriber) mergeRuntimeLocked(event *BotWorkerEvent) bool {
	if event == nil || (event.Evt != "bot-state" && event.Evt != "heartbeat") {
		return false
	}
	for i := len(s.queue) - 1; i >= 0; i-- {
		if s.queue[i] != nil && s.queue[i].Evt == event.Evt {
			s.queue[i] = event
			return true
		}
	}
	return false
}

func (s *eventSubscriber) abort() {
	s.stopOnce.Do(func() { close(s.done) })
}

func (s *eventSubscriber) stop() {
	s.abort()
	<-s.stopped
}

func isReliableBotEvent(event *BotWorkerEvent) bool {
	return event != nil && (event.Evt == "action-event" || event.Evt == "worker-exit")
}

// Manager Bot 管理器。
// spawn 一个 Bot Worker 子进程，通过 stdin/stdout JSON 行协议通信。
type Manager struct {
	mu                     sync.Mutex
	writeMu                sync.Mutex
	cmd                    *exec.Cmd
	stdin                  *json.Encoder
	stdinPipe              io.Closer
	stdout                 *bufio.Scanner
	activeReaderGeneration int64
	running                bool
	stopping               bool
	cancel                 context.CancelFunc
	waitDone               chan struct{} // 本代子进程 Wait 归来即关闭（单一 waiter，Stop 复用）
	bots                   map[string]*BotState
	onEvent                EventCallback
	eventSubs              map[uint64]*eventSubscriber
	nextSubID              uint64
	pending                map[string]chan pendingRequestResult
	requestTimeout         time.Duration
	capacity               BotCapacitySnapshot
	readyCh                chan struct{}
	prewarm                int
	botWorker              string        // bot-worker 脚本路径
	nodeRes                *NodeResolver // node 可执行解析器（FR-300：托管/扫描 Node 优先，回退 PATH）
	extraEnv               []string      // spawn 追加环境（FR-308：NODE_PATH 仅作 CJS 兜底）
	// prepareSpawn 在依赖预检前刷新受控 dist 的 ESM node_modules 链接。
	prepareSpawn func(distDir string) error
	// depsPrecheck spawn 前只按 ESM 可见路径检查依赖，避免裸全局根误放行。
	depsPrecheck func(distDir string) error
}

// stderrTailLimit 子进程 stderr 尾巴保留上限（崩溃取证用，防无界增长）。
const stderrTailLimit = 4 * 1024

// tailBuffer 只保留最后 limit 字节的写入缓冲（并发安全）。
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// ManagerConfig 管理器配置。
type ManagerConfig struct {
	BotWorkerPath string // bot-worker 入口脚本路径（dist/index.js）
	PrewarmCount  int    // 预热 Bot 数量
	// NodePath 显式指定 spawn 用的 node 可执行（FR-300；V1 无 UI/配置面，仅留结构）。
	NodePath string
	// NodeResolver 可注入的 node 解析器（测试/后续 CP 下发接入点）；
	// nil 时按 NodePath + 本地扫描构造默认解析器。
	NodeResolver *NodeResolver
	// ExtraEnv spawn 时在继承环境上追加的变量（FR-308：NODE_PATH 仅作 CJS 兜底）。
	ExtraEnv []string
	// PrepareSpawn 在依赖预检前准备当前入口目录；受控 dist 用它刷新 node_modules 链接。
	PrepareSpawn func(distDir string) error
	// DepsPrecheck spawn 前按 ESM 可见路径预检依赖；nil 时不预检（测试/旧行为）。
	DepsPrecheck func(distDir string) error
	// RequestTimeout 同步 IPC 请求等待逐项回执的超时；零值使用 10 秒。
	RequestTimeout time.Duration
}

// NewManager 创建 Bot 管理器。
func NewManager(config ManagerConfig) *Manager {
	resolver := config.NodeResolver
	if resolver == nil {
		resolver = NewNodeResolver(config.NodePath, nil)
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 10 * time.Second
	}
	return &Manager{
		bots:           make(map[string]*BotState),
		eventSubs:      make(map[uint64]*eventSubscriber),
		pending:        make(map[string]chan pendingRequestResult),
		requestTimeout: requestTimeout,
		capacity: BotCapacitySnapshot{
			Legacy:                true,
			MaxBots:               50,
			CapacityGeneration:    1,
			WorkerEpochGeneration: 0,
			ObservedAt:            time.Now(),
			UnavailableReason:     "bot-worker 尚未就绪",
		},
		readyCh:      make(chan struct{}),
		prewarm:      config.PrewarmCount,
		botWorker:    config.BotWorkerPath,
		nodeRes:      resolver,
		extraEnv:     config.ExtraEnv,
		prepareSpawn: config.PrepareSpawn,
		depsPrecheck: config.DepsPrecheck,
	}
}

// SetBotWorkerPath 运行时更新 bot-worker 入口脚本路径（FR-308：自愈下发完成后切到数据根物化副本）。
// 只影响下一次 spawn；已运行的子进程不打扰。
func (m *Manager) SetBotWorkerPath(p string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.botWorker = p
}

// SetEventCallback 设置事件回调。
func (m *Manager) SetEventCallback(cb EventCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onEvent = cb
}

// SubscribeEvents 订阅 Bot Worker 事件。
// 每个订阅者使用单一有界泵：runtime 可合并/丢旧，action/退出事件优先保留，不反压 stdout。
func (m *Manager) SubscribeEvents(buffer int) (<-chan *BotWorkerEvent, func()) {
	if buffer <= 0 {
		buffer = 1
	}
	subscriber := newEventSubscriber(buffer)

	m.mu.Lock()
	if m.eventSubs == nil {
		m.eventSubs = make(map[uint64]*eventSubscriber)
	}
	id := m.nextSubID
	m.nextSubID++
	m.eventSubs[id] = subscriber
	m.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			m.mu.Lock()
			sub, ok := m.eventSubs[id]
			if ok {
				delete(m.eventSubs, id)
			}
			m.mu.Unlock()
			if ok {
				sub.stop()
			}
		})
	}
	return subscriber.out, cancel
}

// Start 启动 Bot Worker 子进程。
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("Bot 管理器已在运行")
	}

	distDir := filepath.Dir(m.botWorker)
	if m.prepareSpawn != nil {
		if err := m.prepareSpawn(distDir); err != nil {
			return fmt.Errorf("准备 Bot Worker 运行时失败: %w", err)
		}
	}
	// mineflayer 不随 dist 分发；链接刷新后再按 ESM 实际可见路径预检，
	// 避免裸受控根完整但入口仍指向旧根时误放行。
	if m.depsPrecheck != nil {
		if err := m.depsPrecheck(distDir); err != nil {
			return err
		}
	}

	m.capacity.WorkerEpochGeneration++
	m.capacity.Ready = false
	m.capacity.ActiveBots = 0
	m.capacity.ConnectingBots = 0
	m.capacity.UnavailableReason = "bot-worker 正在启动"
	m.capacity.ObservedAt = time.Now()
	m.bumpCapacityGenerationLocked(0)
	m.readyCh = make(chan struct{})
	args := []string{m.botWorker, fmt.Sprintf("--worker-epoch-generation=%d", m.capacity.WorkerEpochGeneration)}
	if m.prewarm > 0 {
		args = append(args, fmt.Sprintf("--prewarm=%d", m.prewarm))
	}

	// 三类来源都先真实探测并强制最低版本；只有成功解析才缓存。
	res, err := m.nodeRes.Resolve()
	if err != nil {
		return err
	}
	childCtx, cancel := context.WithCancel(ctx)
	stderrTail, spawnErr := m.spawnLocked(childCtx, res.Path, args)
	if spawnErr != nil {
		// 探测与 spawn 间仍可能发生删除/移动；清缓存重扫后固定重试一次。
		// PATH 来源文本可能仍是 "node"，但实际解析到的可执行已经变化，不能只比较字符串路径。
		retry, refreshErr := m.nodeRes.Refresh()
		if refreshErr != nil {
			cancel()
			return fmt.Errorf("%w；重扫 Node.js 失败: %v", spawnErr, refreshErr)
		}
		slog.Warn("Bot Worker 启动失败，重扫 Node.js 后重试",
			"error", spawnErr, "node", retry.Path, "nodeVersion", retry.Version, "nodeSource", retry.Source)
		res = retry
		stderrTail, spawnErr = m.spawnLocked(childCtx, res.Path, args)
	}
	if spawnErr != nil {
		cancel()
		return spawnErr
	}

	m.cancel = cancel
	m.running = true
	m.activeReaderGeneration = m.capacity.WorkerEpochGeneration
	m.waitDone = make(chan struct{})
	slog.Info("Bot Worker 已启动",
		"pid", m.cmd.Process.Pid, "node", res.Path, "nodeVersion", res.Version, "nodeSource", res.Source)

	// 启动事件读取循环；固定绑定本代 scanner 与本地世代，旧 reader 的迟到事件一律丢弃。
	go m.readLoop(m.stdout, m.activeReaderGeneration)
	// 单一 waiter：子进程退出（崩溃/被杀/正常）即归位 running=false，
	// 使 ensureBotManager 懒重拉恢复生效；否则后续 IPC 全部写入死管道、Bot 永卡 connecting。
	go m.waitChild(m.cmd, m.waitDone, stderrTail)

	return nil
}

// spawnLocked 用指定 node 可执行拉起 bot-worker 子进程并接好 stdio（须持有 m.mu）。
// 失败不留半成品：exec.Cmd.Start 出错时会自行关闭已建管道，Manager 状态由调用方决定是否重试。
func (m *Manager) spawnLocked(ctx context.Context, nodePath string, args []string) (*tailBuffer, error) {
	cmd := exec.CommandContext(ctx, nodePath, args...)
	if len(m.extraEnv) > 0 {
		cmd.Env = append(os.Environ(), m.extraEnv...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdin 管道失败: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}

	// 捕获 stderr 尾巴：子进程崩溃时留取证现场（此前直接丢弃，死因不可查）。
	stderrTail := &tailBuffer{limit: stderrTailLimit}
	cmd.Stderr = stderrTail

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 Bot Worker 失败: %w", err)
	}

	m.cmd = cmd
	m.stdin = json.NewEncoder(stdin)
	m.stdinPipe = stdin
	m.stdout = bufio.NewScanner(stdout)
	// 增大扫描缓冲区，避免长行被截断
	m.stdout.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return stderrTail, nil
}

// waitChild 等待子进程退出并归位运行态（本代 cmd 的唯一 Wait 调用方）。
func (m *Manager) waitChild(cmd *exec.Cmd, done chan struct{}, stderrTail *tailBuffer) {
	err := cmd.Wait()

	m.mu.Lock()
	// 代际守卫：仅当仍是本代子进程时归位，避免旧代 Wait 迟到冲掉重拉后的新状态。
	current := m.cmd == cmd
	wasRunning := m.running
	wasStopping := m.stopping
	var exitEvent *BotWorkerEvent
	var cb EventCallback
	if current {
		exitEvent, cb = m.invalidateRuntimeLocked("bot-worker 进程已退出", fmt.Errorf("Bot Worker 进程已退出"))
		m.cmd = nil
		m.stdin = nil
		m.stdinPipe = nil
		m.stdout = nil
		m.cancel = nil
	}
	m.mu.Unlock()
	close(done)

	if cb != nil {
		cb(exitEvent)
	}
	if current && wasRunning && !wasStopping {
		// 非 Stop 主动收束的退出（崩溃/自亡）：留取证日志。
		slog.Error("Bot Worker 子进程意外退出，下次 Bot 操作将自动重拉",
			"error", err, "stderrTail", stderrTail.String())
	}
}

func (m *Manager) invalidateRuntimeLocked(reason string, pendingErr error) (*BotWorkerEvent, EventCallback) {
	epoch := m.capacity.WorkerEpoch
	generation := m.capacity.WorkerEpochGeneration
	m.running = false
	m.stopping = false
	m.activeReaderGeneration = 0
	m.bots = make(map[string]*BotState)
	m.capacity.Ready = false
	m.capacity.Legacy = true
	m.capacity.ActiveBots = 0
	m.capacity.ConnectingBots = 0
	m.capacity.WorkerEpoch = ""
	m.capacity.BotWorkerVersion = ""
	m.capacity.RSSBytes = 0
	m.capacity.EventLoopP95Ms = 0
	m.capacity.DroppedEvents = 0
	m.capacity.Features = nil
	m.capacity.UnavailableReason = reason
	m.capacity.ObservedAt = time.Now()
	m.bumpCapacityGenerationLocked(0)
	m.failPendingLocked(pendingErr)
	m.closeReadySignalLocked()

	event := &BotWorkerEvent{
		Evt: "worker-exit", Error: reason,
		WorkerEpoch: epoch, WorkerEpochGeneration: generation,
	}
	m.dispatchTerminalEventLocked(event)
	return event, m.onEvent
}

// Stop 停止 Bot 管理器和 Bot Worker 子进程。
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.running || m.stopping {
		m.mu.Unlock()
		return
	}
	m.stopping = true
	cancel, done, cmd := m.cancel, m.waitDone, m.cmd
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// 等待 waitChild（唯一 Wait 调用方）确认子进程退出，超时兜底强杀。
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}

	slog.Info("Bot Worker 已停止")
}

// IsRunning 是否在运行。
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

var errBotWriterReplaced = errors.New("Bot Worker stdin 已失效")

// sendCommand 向 Bot Worker 发送命令。
func (m *Manager) sendCommand(cmd interface{}) error {
	m.mu.Lock()
	if !m.running || m.stdin == nil {
		m.mu.Unlock()
		return fmt.Errorf("Bot Worker 未运行")
	}
	generation, encoder, timeout := m.activeReaderGeneration, m.stdin, m.requestTimeout
	m.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	writeDone := m.startWrite(generation, encoder, cmd)
	select {
	case err := <-writeDone:
		return err
	case <-timer.C:
		if err, completed := pollWrite(writeDone); completed {
			return err
		}
		err := fmt.Errorf("等待 Bot Worker stdin 写入超时")
		m.isolateBlockedWriter(generation, encoder, err)
		<-writeDone
		return err
	}
}

const maxPendingRequests = 1024

func (m *Manager) sendRequest(ctx context.Context, requestID string, cmd interface{}) (*BotWorkerEvent, error) {
	if requestID == "" {
		return nil, fmt.Errorf("requestId 不能为空")
	}
	m.mu.Lock()
	if !m.running || m.stdin == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("Bot Worker 未运行")
	}
	if len(m.pending) >= maxPendingRequests {
		m.mu.Unlock()
		return nil, fmt.Errorf("Bot Worker 待处理请求过多")
	}
	if _, exists := m.pending[requestID]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("requestId 已存在: %s", requestID)
	}
	resultCh := make(chan pendingRequestResult, 1)
	m.pending[requestID] = resultCh
	generation, encoder, timeout := m.activeReaderGeneration, m.stdin, m.requestTimeout
	m.mu.Unlock()

	// 超时必须先于 Encode 启动，阻塞管道写也受同一 deadline 约束。
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	writeDone := m.startWrite(generation, encoder, cmd)
	select {
	case err := <-writeDone:
		if err != nil {
			if errors.Is(err, errBotWriterReplaced) {
				select {
				case result := <-resultCh:
					return result.event, result.err
				default:
				}
			}
			m.removePendingRequest(requestID, resultCh)
			if !errors.Is(err, errBotWriterReplaced) {
				m.isolateBlockedWriter(generation, encoder, err)
			}
			return nil, fmt.Errorf("写入 Bot Worker stdin 失败: %w", err)
		}
	case <-ctx.Done():
		m.removePendingRequest(requestID, resultCh)
		if _, completed := pollWrite(writeDone); !completed {
			m.isolateBlockedWriter(generation, encoder, ctx.Err())
			<-writeDone
		}
		return nil, ctx.Err()
	case <-timer.C:
		m.removePendingRequest(requestID, resultCh)
		if _, completed := pollWrite(writeDone); !completed {
			err := fmt.Errorf("等待 Bot Worker stdin 写入超时: requestId=%s", requestID)
			m.isolateBlockedWriter(generation, encoder, err)
			<-writeDone
			return nil, err
		}
		return nil, fmt.Errorf("等待 Bot Worker 回执超时: requestId=%s", requestID)
	}

	select {
	case result := <-resultCh:
		return result.event, result.err
	case <-ctx.Done():
		m.removePendingRequest(requestID, resultCh)
		return nil, ctx.Err()
	case <-timer.C:
		m.removePendingRequest(requestID, resultCh)
		return nil, fmt.Errorf("等待 Bot Worker 回执超时: requestId=%s", requestID)
	}
}

func (m *Manager) startWrite(generation int64, encoder *json.Encoder, cmd interface{}) <-chan error {
	done := make(chan error, 1)
	go func() {
		m.writeMu.Lock()
		defer m.writeMu.Unlock()
		m.mu.Lock()
		current := m.running && m.activeReaderGeneration == generation && m.stdin == encoder
		m.mu.Unlock()
		if !current {
			done <- errBotWriterReplaced
			return
		}
		done <- encoder.Encode(cmd)
	}()
	return done
}

func pollWrite(done <-chan error) (error, bool) {
	select {
	case err := <-done:
		return err, true
	default:
		return nil, false
	}
}

func (m *Manager) isolateBlockedWriter(generation int64, encoder *json.Encoder, cause error) {
	m.mu.Lock()
	if !m.running || m.activeReaderGeneration != generation || m.stdin != encoder {
		m.mu.Unlock()
		return
	}
	cmd, pipe, cancel := m.cmd, m.stdinPipe, m.cancel
	pendingErr := fmt.Errorf("Bot Worker stdin 不可用: %w", cause)
	exitEvent, cb := m.invalidateRuntimeLocked("bot-worker stdin 不可用", pendingErr)
	m.cmd, m.stdin, m.stdinPipe, m.stdout, m.cancel = nil, nil, nil, nil, nil
	m.mu.Unlock()

	if pipe != nil {
		_ = pipe.Close()
	}
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if cb != nil {
		cb(exitEvent)
	}
	slog.Warn("Bot Worker stdin 阻塞，已隔离不健康子进程", "error", cause)
}

func (m *Manager) removePendingRequest(requestID string, waiter chan pendingRequestResult) {
	m.mu.Lock()
	if m.pending[requestID] == waiter {
		delete(m.pending, requestID)
	}
	m.mu.Unlock()
}

func (m *Manager) failPendingLocked(err error) {
	for requestID, waiter := range m.pending {
		delete(m.pending, requestID)
		waiter <- pendingRequestResult{err: err}
	}
}

// PendingRequestCount 返回当前同步 IPC 等待者数量。
func (m *Manager) PendingRequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// ApplyBotBatch 发送单次 create-bots 并等待 batch-result。
func (m *Manager) ApplyBotBatch(ctx context.Context, requestID, batchID, idempotencyKey string, configs []BotConfig) (*BotWorkerEvent, error) {
	return m.sendRequest(ctx, requestID, CreateBotsCommand{
		Cmd: "create-bots", RequestID: requestID, BatchID: batchID,
		IdempotencyKey: idempotencyKey, Bots: configs,
	})
}

// StopBotBatch 发送 stop-bots 并等待逐项 batch-result。
func (m *Manager) StopBotBatch(ctx context.Context, requestID string, botIDs []string, generation int64, reason string) (*BotWorkerEvent, error) {
	return m.sendRequest(ctx, requestID, StopBotsCommand{
		Cmd: "stop-bots", RequestID: requestID, BotIds: botIDs, Generation: generation, Reason: reason,
	})
}

// SignalActions 投递通用外部动作信号并等待 signal-result。
func (m *Manager) SignalActions(ctx context.Context, requestID string, signals []ActionSignal) (*BotWorkerEvent, error) {
	return m.sendRequest(ctx, requestID, SignalActionsCommand{Cmd: "signal-actions", RequestID: requestID, Signals: signals})
}

// RequestFleetSnapshot 请求 bot-worker 返回当前全部 Bot 快照。
func (m *Manager) RequestFleetSnapshot(ctx context.Context, requestID string) (*BotWorkerEvent, error) {
	return m.sendRequest(ctx, requestID, GetFleetSnapshotCommand{Cmd: "get-fleet-snapshot", RequestID: requestID})
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

// CapacitySnapshot 返回当前 fleet 容量快照。
func (m *Manager) CapacitySnapshot() BotCapacitySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := m.capacity
	result.Features = append([]string(nil), m.capacity.Features...)
	return result
}

// WaitReady 等待 bot-worker 发出 worker-ready；老版本也会就绪但标记 Legacy。
func (m *Manager) WaitReady(ctx context.Context) error {
	m.mu.Lock()
	if m.capacity.Ready {
		m.mu.Unlock()
		return nil
	}
	if !m.running {
		reason := m.capacity.UnavailableReason
		m.mu.Unlock()
		return fmt.Errorf("Bot Worker 未运行: %s", reason)
	}
	readyCh := m.readyCh
	generation := m.capacity.WorkerEpochGeneration
	m.mu.Unlock()

	select {
	case <-readyCh:
		m.mu.Lock()
		ready := m.running && m.capacity.Ready && m.capacity.WorkerEpochGeneration == generation
		reason := m.capacity.UnavailableReason
		currentGeneration := m.capacity.WorkerEpochGeneration
		m.mu.Unlock()
		if ready {
			return nil
		}
		if currentGeneration != generation {
			return fmt.Errorf("Bot Worker 世代已切换: expected=%d actual=%d", generation, currentGeneration)
		}
		return fmt.Errorf("Bot Worker 进程已退出: %s", reason)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) closeReadySignalLocked() {
	if m.readyCh == nil {
		return
	}
	select {
	case <-m.readyCh:
	default:
		close(m.readyCh)
	}
}

// FleetSnapshot 返回指定会话或全部 Bot 的内存快照。
func (m *Manager) FleetSnapshot(sessionID string) []BotState {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]BotState, 0, len(m.bots))
	for _, state := range m.bots {
		if sessionID != "" && state.SessionID != sessionID {
			continue
		}
		copyState := *state
		if state.Position != nil {
			position := *state.Position
			copyState.Position = &position
		}
		result = append(result, copyState)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
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

// readLoop 读取指定子进程世代的 Bot Worker stdout 事件。
func (m *Manager) readLoop(scanner *bufio.Scanner, sourceGeneration int64) {
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event BotWorkerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			slog.Warn("解析 Bot Worker 事件失败", "error", err, "line", line)
			continue
		}
		m.handleReaderEvent(&event, scanner, sourceGeneration)
	}

	m.mu.Lock()
	current := m.stdout == scanner && m.activeReaderGeneration == sourceGeneration
	m.mu.Unlock()
	if err := scanner.Err(); err != nil && current {
		slog.Error("Bot Worker stdout 读取错误", "error", err)
	}
}

func (m *Manager) handleReaderEvent(event *BotWorkerEvent, scanner *bufio.Scanner, sourceGeneration int64) {
	m.mu.Lock()
	if m.stdout != scanner || m.activeReaderGeneration != sourceGeneration {
		m.mu.Unlock()
		return
	}
	cb, accepted := m.handleEventLocked(event, sourceGeneration)
	m.mu.Unlock()
	if accepted && cb != nil {
		cb(event)
	}
}

// handleEvent 处理测试注入或不绑定 reader 的 Bot Worker 事件。
func (m *Manager) handleEvent(event *BotWorkerEvent) {
	m.mu.Lock()
	cb, accepted := m.handleEventLocked(event, 0)
	m.mu.Unlock()
	if accepted && cb != nil {
		cb(event)
	}
}

func (m *Manager) handleEventLocked(event *BotWorkerEvent, sourceGeneration int64) (EventCallback, bool) {
	if event == nil {
		return nil, false
	}
	if sourceGeneration > 0 && event.Evt == "worker-ready" &&
		event.WorkerEpochGeneration != 0 && event.WorkerEpochGeneration != sourceGeneration {
		return nil, false
	}
	if sourceGeneration > 0 {
		// 本地 reader 世代覆盖 JSON 值，供 Server 在广播前隔离旧 child 已排队事件。
		event.WorkerEpochGeneration = sourceGeneration
	}

	switch event.Evt {
	case "bot-state":
		accepted := event.Bots[:0]
		for i := range event.Bots {
			if state, ok := m.mergeBotState(event.Bots[i], sourceGeneration); ok {
				accepted = append(accepted, state)
			}
		}
		event.Bots = accepted
		if len(event.Bots) == 0 {
			return nil, false
		}
	case "worker-ready":
		m.applyWorkerReady(event, sourceGeneration)
	case "heartbeat":
		m.applyHeartbeat(event)
	case "fleet-snapshot-result":
		m.replaceFleetSnapshotLocked(event, sourceGeneration)
		m.completePendingLocked(event)
	case "batch-result", "signal-result":
		m.completePendingLocked(event)
	case "bot-event", "bot-error", "script-progress", "action-event":
		// 事件直接转发给回调和订阅者。
	}

	cb := m.onEvent
	m.dispatchEventLocked(event)
	return cb, true
}

func (m *Manager) completePendingLocked(event *BotWorkerEvent) {
	if waiter, ok := m.pending[event.RequestID]; ok {
		delete(m.pending, event.RequestID)
		waiter <- pendingRequestResult{event: event}
	} else if event.RequestID != "" {
		slog.Debug("收到已超时的 Bot Worker 回执", "requestId", event.RequestID, "event", event.Evt)
	}
}

func (m *Manager) mergeBotState(state BotState, sourceGeneration int64) (BotState, bool) {
	state, ok := m.normalizeBotState(state, sourceGeneration)
	if !ok {
		return BotState{}, false
	}
	existing, exists := m.bots[state.ID]
	if exists && state.WorkerEpochGeneration < existing.WorkerEpochGeneration {
		return BotState{}, false
	}
	if exists && state.WorkerEpochGeneration == existing.WorkerEpochGeneration &&
		existing.EventSeq > 0 && state.EventSeq <= existing.EventSeq {
		return BotState{}, false
	}
	if state.Status == "stopped" || state.Status == "not_found" {
		delete(m.bots, state.ID)
		return state, true
	}
	if !exists {
		copyState := state
		m.bots[state.ID] = &copyState
		return state, true
	}
	mergeBotStateFields(existing, state)
	return state, true
}

func (m *Manager) normalizeBotState(state BotState, sourceGeneration int64) (BotState, bool) {
	if state.ID == "" {
		return BotState{}, false
	}
	if sourceGeneration > 0 {
		if state.WorkerEpochGeneration != 0 && state.WorkerEpochGeneration != sourceGeneration {
			return BotState{}, false
		}
		state.WorkerEpochGeneration = sourceGeneration
	} else if state.WorkerEpochGeneration == 0 {
		state.WorkerEpochGeneration = m.capacity.WorkerEpochGeneration
	}
	if m.capacity.WorkerEpochGeneration > 0 && state.WorkerEpochGeneration < m.capacity.WorkerEpochGeneration {
		return BotState{}, false
	}
	if state.WorkerEpoch == "" {
		state.WorkerEpoch = m.capacity.WorkerEpoch
	}
	if state.ObservedAt == 0 {
		state.ObservedAt = time.Now().UnixMilli()
	}
	return state, true
}

func mergeBotStateFields(existing *BotState, state BotState) {
	if state.Status != "" {
		existing.Status = state.Status
	}
	if state.Name != "" {
		existing.Name = state.Name
	}
	if state.Health != 0 {
		existing.Health = state.Health
	}
	if state.Food != 0 {
		existing.Food = state.Food
	}
	if state.Position != nil {
		existing.Position = state.Position
	}
	if state.Behavior != "" {
		existing.Behavior = state.Behavior
	}
	if state.SessionID != "" {
		existing.SessionID = state.SessionID
	}
	if state.Generation != 0 {
		existing.Generation = state.Generation
	}
	if state.ConfigHash != "" {
		existing.ConfigHash = state.ConfigHash
	}
	existing.WorkerEpoch = state.WorkerEpoch
	existing.WorkerEpochGeneration = state.WorkerEpochGeneration
	existing.EventSeq = state.EventSeq
	if state.CurrentStepID != "" {
		existing.CurrentStepID = state.CurrentStepID
	}
	if state.ReconnectCount != 0 {
		existing.ReconnectCount = state.ReconnectCount
	}
	if state.ErrorCode != "" {
		existing.ErrorCode = state.ErrorCode
	}
	if state.LastError != "" {
		existing.LastError = state.LastError
	}
	existing.ObservedAt = state.ObservedAt
}

func (m *Manager) replaceFleetSnapshotLocked(event *BotWorkerEvent, sourceGeneration int64) {
	next := make(map[string]*BotState, len(event.Bots))
	accepted := event.Bots[:0]
	for i := range event.Bots {
		state, ok := m.normalizeBotState(event.Bots[i], sourceGeneration)
		if !ok || state.Status == "stopped" || state.Status == "not_found" {
			continue
		}
		copyState := state
		next[state.ID] = &copyState
		accepted = append(accepted, state)
	}
	m.bots = next
	event.Bots = accepted
}

func (m *Manager) applyWorkerReady(event *BotWorkerEvent, sourceGeneration int64) {
	legacy := !hasFeature(event.Features, "fleet-v1")
	maxBots := m.capacity.MaxBots
	if event.MaxBots > 0 {
		maxBots = event.MaxBots
	}
	generation := event.WorkerEpochGeneration
	if sourceGeneration > 0 {
		generation = sourceGeneration
	}
	changed := !m.capacity.Ready || m.capacity.Legacy != legacy || m.capacity.MaxBots != maxBots ||
		m.capacity.WorkerEpoch != event.WorkerEpoch || m.capacity.WorkerEpochGeneration != generation ||
		!slices.Equal(m.capacity.Features, event.Features)

	m.capacity.Ready = true
	m.capacity.Legacy = legacy
	m.capacity.MaxBots = maxBots
	m.capacity.WorkerEpoch = event.WorkerEpoch
	if generation > m.capacity.WorkerEpochGeneration || sourceGeneration > 0 {
		m.capacity.WorkerEpochGeneration = generation
	}
	event.WorkerEpochGeneration = m.capacity.WorkerEpochGeneration
	m.capacity.BotWorkerVersion = event.BotWorkerVersion
	m.capacity.Features = append([]string(nil), event.Features...)
	m.capacity.ObservedAt = time.Now()
	m.capacity.UnavailableReason = ""
	if changed {
		m.bumpCapacityGenerationLocked(event.CapacityGeneration)
	}
	m.closeReadySignalLocked()
}

func (m *Manager) applyHeartbeat(event *BotWorkerEvent) {
	m.capacity.ActiveBots = event.ActiveBots
	m.capacity.ConnectingBots = event.ConnectingBots
	m.capacity.RSSBytes = event.RSSBytes
	m.capacity.EventLoopP95Ms = event.EventLoopP95Ms
	m.capacity.DroppedEvents = event.DroppedEvents
	m.capacity.ObservedAt = time.Now()
	if event.CapacityGeneration > m.capacity.CapacityGeneration {
		m.bumpCapacityGenerationLocked(event.CapacityGeneration)
	}
}

func (m *Manager) bumpCapacityGenerationLocked(reported int64) {
	if reported > m.capacity.CapacityGeneration {
		m.capacity.CapacityGeneration = reported
		return
	}
	m.capacity.CapacityGeneration++
}

func hasFeature(features []string, want string) bool {
	for _, feature := range features {
		if feature == want {
			return true
		}
	}
	return false
}

func (m *Manager) dispatchEventLocked(event *BotWorkerEvent) {
	m.enqueueSubscribersLocked(event)
}

func (m *Manager) dispatchTerminalEventLocked(event *BotWorkerEvent) {
	m.enqueueSubscribersLocked(event)
}

func (m *Manager) enqueueSubscribersLocked(event *BotWorkerEvent) {
	for id, subscriber := range m.eventSubs {
		if subscriber.enqueue(event) {
			continue
		}
		delete(m.eventSubs, id)
		subscriber.abort()
		slog.Error("Bot 可靠事件队列已满，终止慢订阅者", "event", event.Evt)
	}
}
