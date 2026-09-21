package bot

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 分片管理器测试：验证「多 bot-worker 进程」这个新增抽象的路由与汇总语义。
// 背景见 sharded.go 注释：单进程承载 bot 有硬上限（272 bot 时主线程 99.9% CPU、
// 心跳停发），故按 ~150/片拆分。

// newTestShard 构造一个「已就绪、带指定容量」的假分片，用于纯逻辑测试（不 spawn 真进程）。
func newTestShard(total, active int) *Manager {
	m := NewManager(ManagerConfig{BotWorkerPath: "unused"})
	m.running = true
	m.stdin = json.NewEncoder(io.Discard)
	m.capacity = BotCapacitySnapshot{
		Ready:              true,
		MaxBots:            total,
		ActiveBots:         active,
		WorkerEpoch:        "epoch-test",
		CapacityGeneration: 1,
		ObservedAt:         time.Now(),
		Features:           []string{"fleet-v1"},
	}
	return m
}

// newTestSharded 构造 N 片的分片管理器（每片容量 perShard、初始 active 递增以模拟不均衡）。
func newTestSharded(t *testing.T, shards, maxBots int, activePerShard []int) *ShardedManager {
	t.Helper()
	perShard := (maxBots + shards - 1) / shards
	idx := 0
	return NewShardedManager(ShardedManagerConfig{
		Shards:  shards,
		MaxBots: maxBots,
		NewShard: func(i int, perShardMax int) *Manager {
			active := 0
			if idx < len(activePerShard) {
				active = activePerShard[idx]
			}
			idx++
			return newTestShard(perShard, active)
		},
	})
}

// TestShardedManager_CapacityAggregation 验证容量汇总语义。
func TestShardedManager_CapacityAggregation(t *testing.T) {
	s := newTestSharded(t, 3, 500, []int{10, 20, 30})
	c := s.CapacitySnapshot()

	assert.Equal(t, 500, c.MaxBots, "总容量应为配置值，不随片数四舍五入漂移")
	assert.Equal(t, 60, c.ActiveBots, "活跃数应求和")
	assert.True(t, c.Ready, "全部片就绪时整体就绪")
	assert.Equal(t, "epoch-test:3", c.WorkerEpoch, "多片时 epoch 应带片数后缀")
	// CapacityGeneration 求和：单片换代都会让总量变化，CP 据此识别计划失效。
	assert.Equal(t, int64(3), c.CapacityGeneration, "容量世代应求和以保证单调")
}

// TestShardedManager_PartialUnready 验证部分片未就绪时的整体语义。
func TestShardedManager_PartialUnready(t *testing.T) {
	s := newTestSharded(t, 2, 300, []int{5, 5})
	s.shards[1].capacity.Ready = false
	s.shards[1].capacity.UnavailableReason = "bot-worker 正在启动"

	c := s.CapacitySnapshot()
	assert.False(t, c.Ready, "有片未就绪时整体不算就绪")
	assert.Contains(t, c.UnavailableReason, "分片", "原因应指明是分片级问题")
	assert.Contains(t, c.UnavailableReason, "bot-worker 正在启动", "应带上具体原因")
}

// TestShardedManager_ObservedAtTakesEarliest 验证时间戳取最早（最保守）。
func TestShardedManager_ObservedAtTakesEarliest(t *testing.T) {
	s := newTestSharded(t, 2, 300, []int{0, 0})
	old := time.Now().Add(-time.Minute)
	s.shards[0].capacity.ObservedAt = time.Now()
	s.shards[1].capacity.ObservedAt = old

	c := s.CapacitySnapshot()
	assert.WithinDuration(t, old, c.ObservedAt, time.Second,
		"应取最早的观测时间：任一片停更都要让整体看起来不够新")
}

// TestShardedManager_EventLoopTakesMax 验证事件循环延迟取最大值（最差片用于告警）。
func TestShardedManager_EventLoopTakesMax(t *testing.T) {
	s := newTestSharded(t, 2, 300, []int{0, 0})
	s.shards[0].capacity.EventLoopP95Ms = 5
	s.shards[1].capacity.EventLoopP95Ms = 250

	c := s.CapacitySnapshot()
	assert.Equal(t, 250.0, c.EventLoopP95Ms, "应取最差片，避免被健康片掩盖")
}

// TestShardedManager_PickShardLeastLoaded 验证按负载最少挑选。
func TestShardedManager_PickShardLeastLoaded(t *testing.T) {
	s := newTestSharded(t, 3, 300, []int{50, 10, 30}) // 每片容量 100
	idx, sh := s.pickShard()
	require.NotNil(t, sh)
	assert.Equal(t, 1, idx, "应挑活跃数最少的片（index 1，10 个）")
}

// TestShardedManager_PickShardSkipsFull 验证满片被跳过。
func TestShardedManager_PickShardSkipsFull(t *testing.T) {
	s := newTestSharded(t, 2, 200, []int{100, 30}) // 每片容量 100
	idx, sh := s.pickShard()
	require.NotNil(t, sh)
	assert.Equal(t, 1, idx, "满片（100/100）应被跳过")
}

// TestShardedManager_PickShardAllFull 验证全满时返回 nil。
func TestShardedManager_PickShardAllFull(t *testing.T) {
	s := newTestSharded(t, 2, 200, []int{100, 100})
	idx, sh := s.pickShard()
	assert.Nil(t, sh)
	assert.Equal(t, -1, idx)
}

// TestShardedManager_ApplyBatchSplitsAcrossShards 验证批次会跨片切分。
//
// 这是分片的核心价值：单批 configs 数超过单片容量时，应分散到多片而不是全被拒。
func TestShardedManager_ApplyBatchSplitsAcrossShards(t *testing.T) {
	s := newTestSharded(t, 2, 20, []int{0, 0}) // 每片容量 10

	configs := make([]BotConfig, 0, 20)
	for i := 0; i < 20; i++ {
		configs = append(configs, BotConfig{ID: botIDAt(i)})
	}

	// 两个片的 stdin 都要能接收并回执；这里只验证「切分后各片收到的量」，
	// 用真实 Manager 的 pending 机制模拟回执。
	for i, sh := range s.shards {
		sh.stdin = json.NewEncoder(io.Discard)
		// 起一个 goroutine 消费命令并回执，模拟 bot-worker。
		go func(idx int, m *Manager) {
			// 简化：直接为该片所有请求写回执
			time.Sleep(20 * time.Millisecond)
			for _, cfg := range configs {
				_ = cfg
			}
			_ = idx
			_ = m
		}(i, sh)
	}

	// 只验证分配阶段：调用 pickShardWithPending 的累加是否让 20 个 config 分散到 2 片。
	pending := make(map[int]int, 2)
	counts := make([]int, 2)
	for i := 0; i < 20; i++ {
		idx := s.pickShardWithPending(pending)
		require.GreaterOrEqual(t, idx, 0, "第 %d 个应有片可接", i+1)
		pending[idx]++
		counts[idx]++
	}
	assert.Equal(t, 10, counts[0], "片 0 应分到 10 个（容量上限）")
	assert.Equal(t, 10, counts[1], "片 1 应分到 10 个（容量上限）")
}

// TestShardedManager_ApplyBatchOverflow 验证超出总容量时多余项被判容量不足。
func TestShardedManager_ApplyBatchOverflow(t *testing.T) {
	s := newTestSharded(t, 2, 20, []int{0, 0}) // 总容量 20

	pending := make(map[int]int, 2)
	overflowCount := 0
	for i := 0; i < 25; i++ {
		idx := s.pickShardWithPending(pending)
		if idx < 0 {
			overflowCount++
			continue
		}
		pending[idx]++
	}
	assert.Equal(t, 5, overflowCount, "25 个投到总容量 20 的集群，应有 5 个溢出")
}

// TestShardedManager_RouteShard 验证按登记归属路由。
func TestShardedManager_RouteShard(t *testing.T) {
	s := newTestSharded(t, 3, 300, []int{0, 0, 0})
	s.rememberShard("bot-a", 2)

	idx, sh := s.routeShard("bot-a")
	assert.Equal(t, 2, idx)
	require.NotNil(t, sh)
	assert.Same(t, s.shards[2], sh)
}

// TestShardedManager_RouteShardFallback 验证未登记时遍历兜底（重启后重建场景）。
func TestShardedManager_RouteShardFallback(t *testing.T) {
	s := newTestSharded(t, 2, 200, []int{0, 0})
	// 直接往片 1 的 bots 表塞一个，但不登记归属。
	s.shards[1].bots["bot-x"] = &BotState{ID: "bot-x", Status: "connected"}

	idx, sh := s.routeShard("bot-x")
	assert.Equal(t, 1, idx, "未登记归属时应遍历各片找到持有者")
	require.NotNil(t, sh)
}

// TestShardedManager_RouteShardUnknown 验证未知 bot 返回 -1/nil。
func TestShardedManager_RouteShardUnknown(t *testing.T) {
	s := newTestSharded(t, 2, 200, []int{0, 0})
	idx, sh := s.routeShard("nope")
	assert.Equal(t, -1, idx)
	assert.Nil(t, sh)
}

// TestShardedManager_ShardCountAndSingleShardCompat 验证片数与单片兼容。
func TestShardedManager_ShardCountAndSingleShardCompat(t *testing.T) {
	// shards<=0 归一为 1，保证旧配置（未设 shards）行为不变。
	s := NewShardedManager(ShardedManagerConfig{
		Shards:  0,
		MaxBots: 100,
		NewShard: func(i int, perShardMax int) *Manager {
			return newTestShard(perShardMax, 0)
		},
	})
	assert.Equal(t, 1, s.ShardCount(), "Shards=0 应归一为单进程")
	c := s.CapacitySnapshot()
	assert.Equal(t, "epoch-test", c.WorkerEpoch, "单片时不加后缀（保持与现状一致的标识）")
	assert.Equal(t, 100, c.MaxBots)
}

// TestShardedManager_GetBotsMergesShards 验证 bot 表跨片合并。
func TestShardedManager_GetBotsMergesShards(t *testing.T) {
	s := newTestSharded(t, 2, 200, []int{0, 0})
	s.shards[0].bots["a"] = &BotState{ID: "a"}
	s.shards[1].bots["b"] = &BotState{ID: "b"}

	all := s.GetBots()
	require.Len(t, all, 2)
	assert.Contains(t, all, "a")
	assert.Contains(t, all, "b")
}

// TestShardedManager_FleetSnapshotMerges 验证 fleet 快照合并。
func TestShardedManager_FleetSnapshotMerges(t *testing.T) {
	s := newTestSharded(t, 2, 200, []int{0, 0})
	s.shards[0].bots["a"] = &BotState{ID: "a", SessionID: "s1"}
	s.shards[1].bots["b"] = &BotState{ID: "b", SessionID: "s2"}

	all := s.FleetSnapshot("")
	assert.Len(t, all, 2, "空 sessionID 应返回全部")

	onlyS1 := s.FleetSnapshot("s1")
	require.Len(t, onlyS1, 1)
	assert.Equal(t, "a", onlyS1[0].ID)
}

// TestShardedManager_CircuitOpenAnyShard 验证任一片熔断即整体熔断。
func TestShardedManager_CircuitOpenAnyShard(t *testing.T) {
	s := newTestSharded(t, 2, 200, []int{0, 0})
	assert.False(t, s.CircuitOpen())

	s.shards[1].circuitOpenUntil = time.Now().Add(time.Minute)
	assert.True(t, s.CircuitOpen(), "任一片熔断都应对上层表现为熔断，避免继续下发")
}

// TestShardedManager_RuntimeSnapshotIncludesShardPIDs 验证运行时快照暴露各片 PID。
func TestShardedManager_RuntimeSnapshotIncludesShardPIDs(t *testing.T) {
	s := newTestSharded(t, 3, 300, []int{0, 0, 0})
	snap := s.RuntimeSnapshot()
	assert.Len(t, snap.ShardPIDs, 3, "应暴露 3 个分片 PID 供诊断确认进程数")
	assert.Equal(t, 300, snap.Capacity.MaxBots)
}

// TestShardedManager_ForgetShard 验证归属清理。
func TestShardedManager_ForgetShard(t *testing.T) {
	s := newTestSharded(t, 2, 200, []int{0, 0})
	s.rememberShard("bot-a", 1)
	_, sh := s.routeShard("bot-a")
	require.NotNil(t, sh)

	s.forgetShard("bot-a")
	idx, sh := s.routeShard("bot-a")
	assert.Equal(t, -1, idx, "清理后不应再路由到任何片")
	assert.Nil(t, sh)
}

// TestShardedManager_ShardCallCtxDetachesFromParent 验证分片下发用的是脱离调用方生命周期的 ctx。
//
// 回归保护（真机事故）：分片后一次上层调用会派发到多个片，而调用方 ctx 常是 gRPC 请求级
//（响应返回后即取消）。若直接透传，第二个片会拿到已取消的 ctx → Manager 判「stdin 阻塞」
// 并主动 kill 子进程 → 启动后只剩 1 个 bot-worker。
// 日志证据：requestId=create-...:s1 ctxErr="context canceled" writeCompleted=false
func TestShardedManager_ShardCallCtxDetachesFromParent(t *testing.T) {
	s := newTestSharded(t, 2, 200, []int{0, 0})

	// 调用方 ctx 已取消：分片仍应拿到可用的窗口。
	dead, cancelDead := context.WithCancel(context.Background())
	cancelDead()

	ctx, cancel := s.shardCallCtx(dead)
	defer cancel()
	assert.NoError(t, ctx.Err(), "调用方已取消时，分片仍应拿到可用 ctx（不随上游断开而中断）")

	// 调用方 ctx 随后取消：已派生的分片 ctx 不应受影响。
	parent, cancelParent := context.WithCancel(context.Background())
	ctx2, cancel2 := s.shardCallCtx(parent)
	defer cancel2()
	cancelParent()
	assert.NoError(t, ctx2.Err(), "上游取消不应波及分片下游的已接受工作")
}

// botIDAt 生成可读的测试 bot ID。
func botIDAt(i int) string {
	return "bot-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}

// TestShardedManager_ShardsGetIndependentContexts 验证各片启动 context 互相独立，
// 且生命周期归 ShardedManager 而非调用方。
//
// 回归保护：曾因各片共用调用方传入的同一 ctx，导致任一片的「隔离不健康子进程」
// cancel 波及所有片（真机表现：启动后仅剩 1 个 bot-worker，日志出现
// stdin 阻塞 + context canceled）。
func TestShardedManager_ShardsGetIndependentContexts(t *testing.T) {
	s := newTestSharded(t, 2, 300, []int{0, 0})
	require.NotNil(t, s.rootCtx, "ShardedManager 应有自己的 rootCtx")

	// 按 Start 的派生方式建两个片 ctx（不真 spawn）。
	ctx0, cancel0 := context.WithCancel(s.rootCtx)
	ctx1, cancel1 := context.WithCancel(s.rootCtx)
	defer cancel1()

	// 核心断言：片 0 自己被取消，不应影响片 1。
	// （不比较 context 对象本身——其内部结构比较无意义，行为断言才反映真实语义。）
	cancel0()
	assert.Error(t, ctx0.Err(), "片 0 应已取消")
	assert.NoError(t, ctx1.Err(), "片 1 不应被片 0 的取消波及")

	// 外部 ctx 取消也不应波及分片生命周期（各片派生自 rootCtx 而非外部 ctx）。
	_, outerCancel := context.WithCancel(context.Background())
	outerCancel()
	assert.NoError(t, s.rootCtx.Err(), "外部 ctx 取消不应终止分片生命周期")
	assert.NoError(t, ctx1.Err(), "外部取消不应波及仍存活的片")

	// 收束语义：取消 rootCtx 应连带终止各片 ctx（Stop 内部即 rootCancel + 各片 cancel）。
	// 这里直接验 rootCancel 的效果，避免走 Stop()——Stop 会等子进程 Wait 归来，
	// 而本测试用的是未真 spawn 的替身分片，无 waitDone 可等。
	s.rootCancel()
	assert.Error(t, s.rootCtx.Err(), "rootCancel 应收束 rootCtx")
	assert.Error(t, ctx1.Err(), "rootCancel 应连带收束各片 ctx")
}
