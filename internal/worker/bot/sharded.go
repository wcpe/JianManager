package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ShardedManager 把 N 个独立的 bot-worker 子进程聚合为一个逻辑「Bot 管理器」。
//
// 动机：单 Node 进程承载 mineflayer bot 的数量有硬上限。实测 272 bot 时主线程持续
// 99.9% CPU、RSS 9.2GB，事件循环排不上 10s 心跳定时器 → stdout 停止输出 →
// 控制面判定容量快照过期（CAPACITY_SNAPSHOT_STALE）、availableBots 归零，新 bot 再也起不来。
// 分片后每片只承载 ~150 bot，心跳与事件循环保持可调度。
//
// 设计：组合而非改造 Manager —— 每片就是一个完整的 Manager（各自 spawn 一个 bot-worker
// 子进程、各自持有 IPC 与容量）。ShardedManager 只负责：
//   - 按片容量切分批次（ApplyBotBatch）
//   - 按 bot 归属路由单 bot 操作（StopBotBatch / SignalActions / SetBehavior / ...）
//   - 汇总容量与 fleet 快照
//   - 广播 session 级命令计划（CommandSchedule*）
//
// 片段数 = 1 时行为与直接用 Manager 完全一致（兼容旧部署）。
type ShardedManager struct {
	shards []*Manager
	// shardTotalCap 每片容量上限，用于分配时挑选目标片。
	shardTotalCap int
	maxBots       int

	mu      sync.RWMutex
	// botShard 记录每个 bot 归属的片索引。批次分配后写入，停止/操作时据此路由。
	botShard map[string]int

	onEvent EventCallback
	extraMu sync.Mutex

	// shardCancels 各片 Start 时派生的 context 取消函数，Stop 时统一收束。
	shardCancels []context.CancelFunc

	// 反查「哪个片持有该 bot」时的兜底：botShard 未命中则遍历各片（重启后重建场景）。
	rootCtx    context.Context
	rootCancel context.CancelFunc
}

// ShardedManagerConfig 构造 ShardedManager。
type ShardedManagerConfig struct {
	// Shards 分片数；<=1 时为单进程（与现状一致）。
	Shards int
	// MaxBots 本节点总容量（各片均分）。
	MaxBots int
	// NewShard 构造单个分片 Manager 的工厂；由调用方注入（便于测试与复用装配逻辑）。
	NewShard func(index int, perShardMax int) *Manager
}

// NewShardedManager 创建分片管理器。
func NewShardedManager(cfg ShardedManagerConfig) *ShardedManager {
	shards := cfg.Shards
	if shards <= 0 {
		shards = 1
	}
	maxBots := cfg.MaxBots
	if maxBots <= 0 {
		maxBots = 50
	}
	// 向上取整：500/3 => 167，3 片共 501 的容量，多出的一格无害（谁先用谁得）。
	perShard := (maxBots + shards - 1) / shards

	rootCtx, rootCancel := context.WithCancel(context.Background())
	s := &ShardedManager{
		shards:        make([]*Manager, 0, shards),
		shardTotalCap: perShard,
		maxBots:       maxBots,
		botShard:      make(map[string]int),
		rootCtx:       rootCtx,
		rootCancel:    rootCancel,
	}
	for i := 0; i < shards; i++ {
		s.shards = append(s.shards, cfg.NewShard(i, perShard))
	}
	return s
}

// ShardCount 返回分片数（供日志与测试断言）。
func (s *ShardedManager) ShardCount() int { return len(s.shards) }

// SetBotWorkerPath 转发到所有分片。
func (s *ShardedManager) SetBotWorkerPath(p string) {
	for _, sh := range s.shards {
		sh.SetBotWorkerPath(p)
	}
}

// SetEventCallback 记录回调并转发到所有分片。
//
// 事件里补充 shardIndex，便于排障时区分事件来自哪一片。
func (s *ShardedManager) SetEventCallback(cb EventCallback) {
	s.extraMu.Lock()
	s.onEvent = cb
	s.extraMu.Unlock()
	for i, sh := range s.shards {
		idx := i
		sh.SetEventCallback(func(evt *BotWorkerEvent) {
			if evt != nil {
				evt.ShardIndex = idx
			}
			s.extraMu.Lock()
			inner := s.onEvent
			s.extraMu.Unlock()
			if inner != nil {
				inner(evt)
			}
		})
	}
}

// Start 启动所有分片。返回错误若任一片启动失败（已成功的片保持运行，便于部分可用）。
//
// 关键：每片用各自的 context（派生自 ShardedManager 的 rootCtx），而非共用调用方传入的 ctx。
// 原因：Manager.Start 内部会 context.WithCancel(ctx) 并在隔离不健康子进程时 cancel 它；
// 若各片共用同一父 ctx，任何一片的隔离操作都会波及所有片（真机表现：启动后仅剩 1 个
// bot-worker 进程，日志出现「stdin 阻塞，已隔离不健康子进程 error=context canceled」）。
// 分片生命周期归属 ShardedManager，Stop 时经 rootCancel 统一收束。
func (s *ShardedManager) Start(_ context.Context) error {
	var firstErr error
	for i, sh := range s.shards {
		// 每片独立派生：任一片取消不影响其它片。
		shardCtx, cancel := context.WithCancel(s.rootCtx)
		s.mu.Lock()
		s.shardCancels = append(s.shardCancels, cancel)
		s.mu.Unlock()

		if err := sh.Start(shardCtx); err != nil {
			slog.Warn("bot-worker 分片启动失败", "shard", i, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("分片 %d 启动失败: %w", i, err)
			}
		}
	}
	return firstErr
}

// Stop 停止所有分片。
func (s *ShardedManager) Stop() {
	s.rootCancel()
	s.mu.Lock()
	cancels := s.shardCancels
	s.shardCancels = nil
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, sh := range s.shards {
		sh.Stop()
	}
}

// IsRunning 返回是否至少有一片在运行。
func (s *ShardedManager) IsRunning() bool {
	for _, sh := range s.shards {
		if sh.IsRunning() {
			return true
		}
	}
	return false
}

// SubscribeEvents 把所有分片的事件汇聚到单一通道，供上层统一消费。
//
// 各片事件已由 SetEventCallback 补写 ShardIndex，上层可据此区分来源。
// 返回的 cancel 会关闭该汇聚通道并断开所有分片订阅。
func (s *ShardedManager) SubscribeEvents(buffer int) (<-chan *BotWorkerEvent, func()) {
	out := make(chan *BotWorkerEvent, buffer)
	cancels := make([]func(), 0, len(s.shards))
	var closeOnce sync.Once
	var wg sync.WaitGroup

	closeOut := func() {
		closeOnce.Do(func() {
			for _, c := range cancels {
				c()
			}
			wg.Wait()
			close(out)
		})
	}

	for i, sh := range s.shards {
		idx := i
		events, cancel := sh.SubscribeEvents(buffer)
		cancels = append(cancels, cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for evt := range events {
				if evt != nil {
					evt.ShardIndex = idx
				}
				// 上层通道满时丢弃而非阻塞：阻塞会卡住该片的事件泵，
				// 进而拖慢 bot-worker 的 stdout 消费。
				select {
				case out <- evt:
				default:
				}
			}
		}()
	}
	return out, closeOut
}

// WaitReady 等待所有分片就绪。
func (s *ShardedManager) WaitReady(ctx context.Context) error {
	for i, sh := range s.shards {
		if err := sh.WaitReady(ctx); err != nil {
			return fmt.Errorf("分片 %d 未就绪: %w", i, err)
		}
	}
	return nil
}

// CapacitySnapshot 汇总各片容量。
//
// 汇总语义：
//   - Max/Active/Connecting/RSS：求和（逻辑总量）
//   - EventLoopP95Ms：取最大值（最差片，用于告警阈值不受单片掩盖）
//   - ObservedAt：取最早（最保守，避免某片停更时整体仍显新鲜）
//   - Ready：全员就绪才算真；部分就绪时保留可用性但标注原因
//   - WorkerEpoch：主片值 + 分片数后缀（字符串比对语义，见设计说明）
func (s *ShardedManager) CapacitySnapshot() BotCapacitySnapshot {
	if len(s.shards) == 0 {
		return BotCapacitySnapshot{}
	}
	out := BotCapacitySnapshot{
		Ready:      true,
		MaxBots:    s.maxBots,
		ObservedAt: time.Time{},
	}
	var epoch string
	allReady := true
	var unreadyReason string
	for i, sh := range s.shards {
		c := sh.CapacitySnapshot()
		if i == 0 {
			epoch = c.WorkerEpoch
		}
		out.ActiveBots += c.ActiveBots
		out.ConnectingBots += c.ConnectingBots
		out.RSSBytes += c.RSSBytes
		out.DroppedEvents += c.DroppedEvents
		if c.EventLoopP95Ms > out.EventLoopP95Ms {
			out.EventLoopP95Ms = c.EventLoopP95Ms
		}
		// 取最早的非零时刻：任一片停更都应让整体看起来「不够新」。
		if !c.ObservedAt.IsZero() && (out.ObservedAt.IsZero() || c.ObservedAt.Before(out.ObservedAt)) {
			out.ObservedAt = c.ObservedAt
		}
		if i == 0 || c.WorkerEpochGeneration > out.WorkerEpochGeneration {
			out.WorkerEpochGeneration = c.WorkerEpochGeneration
		}
		// CapacityGeneration 求和保证单调：任一片换代都会让总量增大，CP 侧据此识别计划失效。
		out.CapacityGeneration += c.CapacityGeneration
		if i == 0 {
			out.BotWorkerVersion = c.BotWorkerVersion
			out.Features = append([]string(nil), c.Features...)
			out.Legacy = c.Legacy
		}
		if !c.Ready {
			allReady = false
			if unreadyReason == "" {
				unreadyReason = c.UnavailableReason
			}
		}
	}
	out.Ready = allReady
	if !allReady {
		// 部分片未就绪：整体标不可用，原因指出是分片级问题，避免误判为整机故障。
		out.UnavailableReason = fmt.Sprintf("部分 bot-worker 分片未就绪: %s", unreadyReason)
	}
	if len(s.shards) > 1 {
		// 复合标识：CP 侧只做字符串比对与展示，加后缀可区分多片部署。
		out.WorkerEpoch = fmt.Sprintf("%s:%d", epoch, len(s.shards))
	} else {
		out.WorkerEpoch = epoch
	}
	return out
}

// RuntimeSnapshot 聚合各片运行时视图（PID 取主片，运行态为「任一片在跑」）。
func (s *ShardedManager) RuntimeSnapshot() RuntimeSnapshot {
	if len(s.shards) == 0 {
		return RuntimeSnapshot{}
	}
	primary := s.shards[0].RuntimeSnapshot()
	out := primary
	out.Running = s.IsRunning()
	out.ShardPIDs = make([]int, 0, len(s.shards))
	for _, sh := range s.shards {
		out.ShardPIDs = append(out.ShardPIDs, sh.RuntimeSnapshot().PID)
	}
	out.Capacity = s.CapacitySnapshot()
	return out
}

// routeShard 返回 bot 所在的片索引；未登记时遍历各片兜底（重启后重建场景）。
func (s *ShardedManager) routeShard(botID string) (int, *Manager) {
	s.mu.RLock()
	idx, ok := s.botShard[botID]
	s.mu.RUnlock()
	if ok && idx >= 0 && idx < len(s.shards) {
		return idx, s.shards[idx]
	}
	for i, sh := range s.shards {
		if _, exists := sh.GetBot(botID); exists {
			return i, sh
		}
	}
	return -1, nil
}

// rememberShard 记录 bot 归属。
func (s *ShardedManager) rememberShard(botID string, shard int) {
	s.mu.Lock()
	s.botShard[botID] = shard
	s.mu.Unlock()
}

// forgetShard 移除 bot 归属。
func (s *ShardedManager) forgetShard(botID string) {
	s.mu.Lock()
	delete(s.botShard, botID)
	s.mu.Unlock()
}

// pickShard 选当前负载最低且仍有余量的片；返回 nil 表示全满。
//
// 用「ActiveBots 最少」而非轮询：bot 分布天然随批次到达顺序漂移，按实际负载挑选
// 能自动纠偏，且不依赖历史分配记录。
func (s *ShardedManager) pickShard() (int, *Manager) {
	idx := s.pickShardWithPending(nil)
	if idx < 0 {
		return -1, nil
	}
	return idx, s.shards[idx]
}

// pickShardWithPending 在 pickShard 基础上叠加「本批已分配但未下发」的计数。
//
// 必要性：批次内连续分配时，各片 CapacitySnapshot 尚未反映刚分配出去的 bot
//（它们要等 RPC 下发后才计入 ActiveBots），只看快照会把整批塞进同一片。
// pending 为空时退化为普通 pickShard。
func (s *ShardedManager) pickShardWithPending(pending map[int]int) int {
	bestIdx := -1
	bestLoad := 0
	for i, sh := range s.shards {
		c := sh.CapacitySnapshot()
		if !c.Ready {
			continue
		}
		load := c.ActiveBots + c.ConnectingBots
		if pending != nil {
			load += pending[i]
		}
		if load >= s.shardTotalCap {
			continue
		}
		if bestIdx == -1 || load < bestLoad {
			bestIdx, bestLoad = i, load
		}
	}
	return bestIdx
}

// shardFree 返回该片剩余可容纳的 bot 数。
func (s *ShardedManager) shardFree(m *Manager) int {
	c := m.CapacitySnapshot()
	free := s.shardTotalCap - c.ActiveBots - c.ConnectingBots
	if free < 0 {
		return 0
	}
	return free
}

// PendingRequestCount 汇总各片在途请求数。
func (s *ShardedManager) PendingRequestCount() int {
	total := 0
	for _, sh := range s.shards {
		total += sh.PendingRequestCount()
	}
	return total
}

// CircuitOpen 任一片熔断即视为整体熔断（避免向已知有问题的片继续下发）。
func (s *ShardedManager) CircuitOpen() bool {
	for _, sh := range s.shards {
		if sh.CircuitOpen() {
			return true
		}
	}
	return false
}
