package grpc

import (
	"context"
	"math"
	"os"
	"time"

	psproc "github.com/shirou/gopsutil/v4/process"

	"github.com/wcpe/JianManager/internal/worker/bot"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const managedRuntimeSampleTimeout = time.Second

type managedRuntimeCPUBaseline struct {
	processSeconds float64
	observedAt     time.Time
}

// ManagedRuntimeSnapshot 读取本 Worker 与已运行 Bot Worker 的当前资源。
// 它仅访问本进程及 Manager 已持有的子进程 PID，绝不启动 Bot Worker 或扫描其他进程。
func (s *Server) ManagedRuntimeSnapshot() *workerpb.ManagedRuntimeSnapshot {
	ctx, cancel := context.WithTimeout(context.Background(), managedRuntimeSampleTimeout)
	defer cancel()
	now := time.Now()
	result := &workerpb.ManagedRuntimeSnapshot{ObservedAtUnixMs: now.UTC().UnixMilli()}
	result.WorkerProcessRssBytes, result.WorkerProcessCpuPct = s.sampleManagedProcess(ctx, os.Getpid(), now)

	s.botEventMu.Lock()
	manager := s.botMgr
	s.botEventMu.Unlock()
	if manager == nil {
		result.BotUnavailableReason = "本节点未启用 Bot Worker"
		result.BotCapacityUnavailableReason = result.BotUnavailableReason
		return result
	}
	botRuntime := manager.RuntimeSnapshot()
	if !botRuntime.Running || botRuntime.PID <= 0 {
		result.BotUnavailableReason = "Bot Worker 未启动"
		result.BotCapacityUnavailableReason = result.BotUnavailableReason
		return result
	}
	if !botRuntime.Capacity.Ready {
		result.BotUnavailableReason = botRuntime.Capacity.UnavailableReason
		if result.BotUnavailableReason == "" {
			result.BotUnavailableReason = "Bot Worker 尚未就绪"
		}
		result.BotCapacityUnavailableReason = result.BotUnavailableReason
		s.forgetManagedProcess(botRuntime.PID)
		return result
	}
	result.BotAvailable = true
	result.BotWorkerRssBytes, result.BotWorkerCpuPct = s.sampleManagedProcess(ctx, botRuntime.PID, now)
	active := int32(botRuntime.Capacity.ActiveBots)
	connecting := int32(botRuntime.Capacity.ConnectingBots)
	eventLoop := botRuntime.Capacity.EventLoopP95Ms
	result.BotActiveCount = &active
	result.BotConnectingCount = &connecting
	result.BotEventLoopP95Ms = &eventLoop
	result.BotCapacityMax, result.BotCapacityUnavailableReason = managedBotCapacityMax(botRuntime.Capacity)
	return result
}

// managedBotCapacityMax 仅转换已就绪 Bot Worker 上报的真实容量，缺失或越界时保持缺测。
func managedBotCapacityMax(capacity bot.BotCapacitySnapshot) (*int32, string) {
	if !capacity.Ready {
		return nil, "Bot Worker 尚未就绪"
	}
	if capacity.MaxBots <= 0 || int64(capacity.MaxBots) > math.MaxInt32 {
		return nil, "Bot Worker 未报告有效容量"
	}
	value := int32(capacity.MaxBots)
	return &value, ""
}

func (s *Server) sampleManagedProcess(ctx context.Context, pid int, now time.Time) (*int64, *float64) {
	process, err := psproc.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		s.forgetManagedProcess(pid)
		return nil, nil
	}
	var rss *int64
	if memory, err := process.MemoryInfoWithContext(ctx); err == nil && memory != nil && memory.RSS <= math.MaxInt64 {
		value := int64(memory.RSS)
		rss = &value
	}
	times, err := process.TimesWithContext(ctx)
	if err != nil {
		return rss, nil
	}
	cpu := s.processCPUPercent(pid, times.User+times.System, now)
	return rss, cpu
}

func (s *Server) processCPUPercent(pid int, processSeconds float64, now time.Time) *float64 {
	s.managedRuntimeMu.Lock()
	defer s.managedRuntimeMu.Unlock()
	previous, found := s.managedRuntimeCPU[pid]
	s.managedRuntimeCPU[pid] = managedRuntimeCPUBaseline{processSeconds: processSeconds, observedAt: now}
	if !found || processSeconds < previous.processSeconds {
		return nil
	}
	elapsed := now.Sub(previous.observedAt).Seconds()
	if elapsed <= 0 {
		return nil
	}
	value := (processSeconds - previous.processSeconds) / elapsed * 100
	return &value
}

func (s *Server) forgetManagedProcess(pid int) {
	s.managedRuntimeMu.Lock()
	delete(s.managedRuntimeCPU, pid)
	s.managedRuntimeMu.Unlock()
}
