package bot

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// 分片感知的批次与单 bot 操作。这些方法的语义与 Manager 对应方法一致，
// 只是在多片场景下先做归属切分，再分别下发并合并结果。

// shardCallCtx 为分片内部的下发派生一个短超时 ctx，脱离调用方 ctx 的生命周期。
//
// 为什么不能直接用调用方的 ctx：分片后一次上层调用会串行/并发派发到多个片，
// 而调用方 ctx 常是 gRPC 请求级（响应返回后即取消）。若直接透传，第二个片会拿到
// 已取消的 ctx → Manager 判「stdin 阻塞」并主动 kill 子进程（真机表现：启动后
// 只剩 1 个 bot-worker，日志 requestId=...:s1 ctxErr="context canceled"）。
// 分片下游的 RPC 是「已接受的工作」，其完成不应随上游连接断开而中断。
//
// 超时取各片 Manager 自身的 requestTimeout（默认 10s）+ 余量，保证单片内的真实
// 超时语义仍由 Manager 自己判定，此处只兜底防止永久挂起。
func (s *ShardedManager) shardCallCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil || parent.Err() != nil {
		// 调用方已取消/无 ctx：仍要给下游一个可用的完整窗口。
		return context.WithTimeout(context.Background(), shardCallTimeout)
	}
	return context.WithTimeout(context.WithoutCancel(parent), shardCallTimeout)
}

// shardCallTimeout 分片级下发的兜底超时。
const shardCallTimeout = 30 * time.Second

// ApplyBotBatch 按各片剩余容量切分 configs，分别下发后合并回执。
//
// 合并规则：保持调用方传入的顺序（CP 侧按 assignments 顺序读取 results）。
// 每片的回执按其收到的 configs 顺序拼接，最终按原下标回填。
func (s *ShardedManager) ApplyBotBatch(ctx context.Context, requestID, batchID, idempotencyKey string, configs []BotConfig) (*BotWorkerEvent, error) {
	if len(s.shards) == 1 {
		event, err := s.shards[0].ApplyBotBatch(ctx, requestID, batchID, idempotencyKey, configs)
		if err == nil {
			for _, cfg := range configs {
				s.rememberShard(cfg.ID, 0)
			}
		}
		return event, err
	}

	type slice struct {
		shardIdx int
		configs  []BotConfig
		// positions 记录该切片内每个 config 在原始 configs 中的下标。
		positions []int
	}
	var slices []slice
	// 未分配完的 configs：容量耗尽时这些会在结果里标 capacity。
	var overflow []int

	// pending 记录本批次内「已分配但尚未下发」的数量。
	// 各片 CapacitySnapshot 只反映已上线/连接中的 bot，不含本批待下发的，
	// 若只看快照会把整批都塞进同一片（同一瞬间每片看起来同样空闲）。故按批内累加记账。
	pending := make(map[int]int, len(s.shards))

	// sliceIndex 快速定位已存在的切片，避免每次都线性扫描。
	sliceIndex := make(map[int]int, len(s.shards))

	for i := range configs {
		idx := s.pickShardWithPending(pending)
		if idx < 0 {
			overflow = append(overflow, i)
			continue
		}
		j, exists := sliceIndex[idx]
		if !exists {
			slices = append(slices, slice{shardIdx: idx})
			j = len(slices) - 1
			sliceIndex[idx] = j
		}
		slices[j].configs = append(slices[j].configs, configs[i])
		slices[j].positions = append(slices[j].positions, i)
		pending[idx]++
		s.rememberShard(configs[i].ID, idx)
	}

	merged := &BotWorkerEvent{
		Evt:            "bots-applied",
		RequestID:      requestID,
		BatchID:        batchID,
		IdempotencyKey: idempotencyKey,
		Results:        make([]BotItemResult, len(configs)),
		Accepted:       true,
	}
	// 容量耗尽的先标结果，保证调用方拿到完整长度的 results。
	for _, i := range overflow {
		merged.Results[i] = BotItemResult{
			BotID:     configs[i].ID,
			Accepted:  false,
			ErrorCode: "capacity_insufficient",
			Error:     "分片容量不足",
		}
		merged.Accepted = false
	}

	for _, sl := range slices {
		// 每片用独立 requestID，避免跨片幂等缓存串味。
		// ctx 也须脱离调用方生命周期：gRPC 请求 ctx 在响应后即取消，
		// 透传会让后续片被误判为 stdin 阻塞并遭 kill（见 shardCallCtx 注释）。
		callCtx, cancel := s.shardCallCtx(ctx)
		shardReqID := fmt.Sprintf("%s:s%d", requestID, sl.shardIdx)
		evt, err := s.shards[sl.shardIdx].ApplyBotBatch(callCtx, shardReqID, batchID, fmt.Sprintf("%s:s%d", idempotencyKey, sl.shardIdx), sl.configs)
		cancel()
		if err != nil {
			// 单片失败：把该片负责的项标失败，其余片继续（部分成功优于整体失败）。
			for _, pos := range sl.positions {
				merged.Results[pos] = BotItemResult{
					BotID: configs[pos].ID, Accepted: false,
					ErrorCode: "shard_error", Error: err.Error(),
				}
				s.forgetShard(configs[pos].ID)
			}
			merged.Accepted = false
			continue
		}
		if evt == nil {
			continue
		}
		// 回填该片结果：evt.Results 顺序应与其收到的 configs 一致。
		for k, pos := range sl.positions {
			if k < len(evt.Results) {
				merged.Results[pos] = evt.Results[k]
				if !evt.Results[k].Accepted {
					// 未被接受的不保留归属记账。
					s.forgetShard(configs[pos].ID)
				}
			}
		}
	}
	return merged, nil
}

// StopBotBatch 按 bot 归属切分后分别下发。
func (s *ShardedManager) StopBotBatch(ctx context.Context, requestID string, botIDs []string, generation int64, reason string) (*BotWorkerEvent, error) {
	if len(s.shards) == 1 {
		event, err := s.shards[0].StopBotBatch(ctx, requestID, botIDs, generation, reason)
		if err == nil {
			for _, id := range botIDs {
				s.forgetShard(id)
			}
		}
		return event, err
	}

	byShard := make(map[int][]string)
	for _, id := range botIDs {
		idx, _ := s.routeShard(id)
		if idx < 0 {
			// 未知归属：广播给所有片，让持有它的那片处理（幂等，未持有的会跳过）。
			for i := range s.shards {
				byShard[i] = append(byShard[i], id)
			}
			continue
		}
		byShard[idx] = append(byShard[idx], id)
	}

	merged := &BotWorkerEvent{Evt: "bots-stopped", RequestID: requestID, Accepted: true}
	for idx, ids := range byShard {
		callCtx, cancel := s.shardCallCtx(ctx)
		shardReqID := fmt.Sprintf("%s:s%d", requestID, idx)
		evt, err := s.shards[idx].StopBotBatch(callCtx, shardReqID, ids, generation, reason)
		cancel()
		if err != nil {
			slog.Warn("分片停止 Bot 失败", "shard", idx, "count", len(ids), "error", err)
			merged.Accepted = false
			merged.Error = err.Error()
			continue
		}
		if evt != nil {
			merged.Results = append(merged.Results, evt.Results...)
		}
	}
	for _, id := range botIDs {
		s.forgetShard(id)
	}
	return merged, nil
}

// SignalActions 按 bot 归属切分后分别下发。
func (s *ShardedManager) SignalActions(ctx context.Context, requestID string, signals []ActionSignal) (*BotWorkerEvent, error) {
	if len(s.shards) == 1 {
		return s.shards[0].SignalActions(ctx, requestID, signals)
	}
	byShard := make(map[int][]ActionSignal)
	for _, sig := range signals {
		idx, _ := s.routeShard(sig.BotID)
		if idx < 0 {
			for i := range s.shards {
				byShard[i] = append(byShard[i], sig)
			}
			continue
		}
		byShard[idx] = append(byShard[idx], sig)
	}

	merged := &BotWorkerEvent{Evt: "signals-applied", RequestID: requestID, Accepted: true}
	for idx, sigs := range byShard {
		callCtx, cancel := s.shardCallCtx(ctx)
		shardReqID := fmt.Sprintf("%s:s%d", requestID, idx)
		evt, err := s.shards[idx].SignalActions(callCtx, shardReqID, sigs)
		cancel()
		if err != nil {
			merged.Accepted = false
			merged.Error = err.Error()
			continue
		}
		if evt != nil {
			merged.SignalResults = append(merged.SignalResults, evt.SignalResults...)
		}
	}
	return merged, nil
}

// RequestFleetSnapshot 合并各片快照。
func (s *ShardedManager) RequestFleetSnapshot(ctx context.Context, requestID string) (*BotWorkerEvent, error) {
	if len(s.shards) == 1 {
		return s.shards[0].RequestFleetSnapshot(ctx, requestID)
	}
	merged := &BotWorkerEvent{Evt: "fleet-snapshot", RequestID: requestID, Accepted: true}
	for i, sh := range s.shards {
		callCtx, cancel := s.shardCallCtx(ctx)
		shardReqID := fmt.Sprintf("%s:s%d", requestID, i)
		evt, err := sh.RequestFleetSnapshot(callCtx, shardReqID)
		cancel()
		if err != nil {
			slog.Warn("分片快照请求失败", "shard", i, "error", err)
			continue
		}
		if evt != nil {
			merged.Bots = append(merged.Bots, evt.Bots...)
		}
	}
	return merged, nil
}

// FleetSnapshot 合并各片快照（sessionID 为空时返回全部）。
func (s *ShardedManager) FleetSnapshot(sessionID string) []BotState {
	var out []BotState
	for _, sh := range s.shards {
		out = append(out, sh.FleetSnapshot(sessionID)...)
	}
	return out
}

// GetBots 合并各片 bot 表。
func (s *ShardedManager) GetBots() map[string]*BotState {
	out := make(map[string]*BotState)
	for _, sh := range s.shards {
		for id, st := range sh.GetBots() {
			out[id] = st
		}
	}
	return out
}

// GetBot 按归属查找 bot；未登记时遍历兜底。
func (s *ShardedManager) GetBot(botID string) (*BotState, bool) {
	_, sh := s.routeShard(botID)
	if sh == nil {
		return nil, false
	}
	return sh.GetBot(botID)
}

// CreateBots 分配后创建（单 bot 路径，FR-395 旧接口）。
func (s *ShardedManager) CreateBots(configs []BotConfig) error {
	for _, cfg := range configs {
		idx, sh := s.pickShard()
		if sh == nil {
			return fmt.Errorf("分片容量不足，无法创建 Bot %s", cfg.ID)
		}
		if err := sh.CreateBots([]BotConfig{cfg}); err != nil {
			return err
		}
		s.rememberShard(cfg.ID, idx)
	}
	return nil
}

// StopBots 按归属停止。
func (s *ShardedManager) StopBots(botIDs []string) error {
	for _, id := range botIDs {
		_, sh := s.routeShard(id)
		if sh == nil {
			continue
		}
		if err := sh.StopBots([]string{id}); err != nil {
			return err
		}
		s.forgetShard(id)
	}
	return nil
}

// SetBehavior 路由到 bot 所在片。
func (s *ShardedManager) SetBehavior(botID, behavior, target string) error {
	_, sh := s.routeShard(botID)
	if sh == nil {
		return fmt.Errorf("Bot 不存在: %s", botID)
	}
	return sh.SetBehavior(botID, behavior, target)
}

// SendBotCommand 路由到 bot 所在片。
func (s *ShardedManager) SendBotCommand(botID, command string) error {
	_, sh := s.routeShard(botID)
	if sh == nil {
		return fmt.Errorf("Bot 不存在: %s", botID)
	}
	return sh.SendBotCommand(botID, command)
}

// RunScript 按 bot 归属切分；空 botIds 表示全体（广播）。
func (s *ShardedManager) RunScript(scriptID string, steps []ScriptStep, botIDs []string) error {
	if len(botIDs) == 0 {
		for _, sh := range s.shards {
			if err := sh.RunScript(scriptID, steps, nil); err != nil {
				return err
			}
		}
		return nil
	}
	byShard := make(map[int][]string)
	for _, id := range botIDs {
		idx, _ := s.routeShard(id)
		if idx < 0 {
			continue
		}
		byShard[idx] = append(byShard[idx], id)
	}
	for idx, ids := range byShard {
		if err := s.shards[idx].RunScript(scriptID, steps, ids); err != nil {
			return err
		}
	}
	return nil
}

// StopScript 广播到所有片（脚本可能跨片）。
func (s *ShardedManager) StopScript(scriptID string) error {
	var firstErr error
	for _, sh := range s.shards {
		if err := sh.StopScript(scriptID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CommandSchedule 相关：session 级计划，广播到所有片（各片只对自己持有的 bot 生效）。
func (s *ShardedManager) ApplyCommandSchedule(ctx context.Context, requestID string, cmd CommandScheduleCommand, deadline time.Duration) (*BotWorkerEvent, error) {
	return s.broadcastCommandSchedule(ctx, requestID, func(sh *Manager, callCtx context.Context, reqID string) (*BotWorkerEvent, error) {
		return sh.ApplyCommandSchedule(callCtx, reqID, cmd, deadline)
	})
}

func (s *ShardedManager) ReleaseCommandSchedule(ctx context.Context, requestID string, cmd CommandScheduleReleaseCommand, deadline time.Duration) (*BotWorkerEvent, error) {
	return s.broadcastCommandSchedule(ctx, requestID, func(sh *Manager, callCtx context.Context, reqID string) (*BotWorkerEvent, error) {
		return sh.ReleaseCommandSchedule(callCtx, reqID, cmd, deadline)
	})
}

func (s *ShardedManager) CancelCommandSchedule(ctx context.Context, requestID string, cmd CommandScheduleCancelCommand, deadline time.Duration) (*BotWorkerEvent, error) {
	return s.broadcastCommandSchedule(ctx, requestID, func(sh *Manager, callCtx context.Context, reqID string) (*BotWorkerEvent, error) {
		return sh.CancelCommandSchedule(callCtx, reqID, cmd, deadline)
	})
}

// broadcastCommandSchedule 向所有片下发同一命令计划语义的操作并合并回执。
//
// 每片用脱离调用方生命周期的 ctx（见 shardCallCtx）：gRPC 请求 ctx 在响应后即取消，
// 透传到后续片会让其被误判为 stdin 阻塞并遭 kill。
func (s *ShardedManager) broadcastCommandSchedule(ctx context.Context, requestID string,
	call func(*Manager, context.Context, string) (*BotWorkerEvent, error)) (*BotWorkerEvent, error) {
	if len(s.shards) == 1 {
		return call(s.shards[0], ctx, requestID)
	}
	merged := &BotWorkerEvent{RequestID: requestID, Accepted: true}
	for i, sh := range s.shards {
		callCtx, cancel := s.shardCallCtx(ctx)
		shardReqID := fmt.Sprintf("%s:s%d", requestID, i)
		evt, err := call(sh, callCtx, shardReqID)
		cancel()
		if err != nil {
			merged.Accepted = false
			merged.Error = err.Error()
			continue
		}
		if evt != nil {
			merged.CommandResults = append(merged.CommandResults, evt.CommandResults...)
			merged.ScheduleRunID = evt.ScheduleRunID
			if evt.AlreadyReleased {
				merged.AlreadyReleased = true
			}
			if evt.AlreadyCancelled {
				merged.AlreadyCancelled = true
			}
		}
	}
	return merged, nil
}

// DesiredSnapshot 合并各片期望状态。
func (s *ShardedManager) DesiredSnapshot() map[string]desiredAssignment {
	out := make(map[string]desiredAssignment)
	for _, sh := range s.shards {
		for id, d := range sh.DesiredSnapshot() {
			out[id] = d
		}
	}
	return out
}
