package metrics

import (
	"encoding/json"
	"sync"
)

// 托管模式（ServerProbe PR#64）经反向 WS 推送的指标接入。
//
// ## 为什么需要这一层
// 托管模式的零监听承诺关掉了探针的 `/metrics` HTTP 端点，而 Worker 默认的采集链
// （[CollectInstanceTelemetry]）是去 HTTP 抓那个端点——托管下必然失败。
// 探针于是改经插件桥周期推 `metric_snapshot`，这里负责把它落住并转成与 HTTP 路径
// **同一个** [ProbeSnapshot] 类型，使 Orchestrate 及其下游（心跳、时序库、详情页）
// 完全无感。
//
// ## 关键：单位换算在这一层完成
// 协议规定 `msptAvg` / `msptP95` 是**秒**，而 [ProbeSnapshot.MSPTAvgMillis] 是**毫秒**。
// 与 HTTP 路径一致，换算必须在这里做（`*1000`）。漏了这一步面板上的 MSPT 会小一千倍，
// 而且不会报错——只是所有服“看起来都很快”。
//
// ## 缺项即缺测，绝不补 0
// 探针对不可用指标**直接不输出该键**（不是填 0）。因此这里必须能区分“键不存在”与
// “值就是 0”：用指针接，nil 时保留 [ProbeSnapshot] 的不可用语义（-1 哨兵 / nil 映射）。
// 把缺失当 0 会让「探针刚启动、TPS 尚未采样」被展示成「TPS=0，服卡死了」。

// PushedSnapshotStore 保存各实例最近一次经桥推送的指标快照。
//
// 并发安全（桥读循环写、心跳 goroutine 读）。刻意只存**最新一份**：
// 托管模式的推送是周期性的，历史由心跳上报后的时序存储负责，这里不做缓冲队列——
// 缓冲只会让“卡住的旧值”被反复当成新值上报。
type PushedSnapshotStore struct {
	mu   sync.RWMutex
	data map[string]ProbeSnapshot
}

// NewPushedSnapshotStore 创建空存储。
func NewPushedSnapshotStore() *PushedSnapshotStore {
	return &PushedSnapshotStore{data: make(map[string]ProbeSnapshot)}
}

// Store 记录某实例的最新推送快照。
//
// @param instanceUUID 实例 UUID（与心跳、桥会话键一致）。
// @param snap 已转换的快照。
func (s *PushedSnapshotStore) Store(instanceUUID string, snap ProbeSnapshot) {
	if s == nil || instanceUUID == "" {
		return
	}
	s.mu.Lock()
	s.data[instanceUUID] = snap
	s.mu.Unlock()
}

// Load 取某实例最近一次推送的快照。
//
// 第二返回值 false 表示该实例从未推送过（此时调用方应回退到 HTTP 抓取或直探）。
// 不做过期判定：是否“太旧”应当由消费方结合时间语义决定，
// 在存储层静默丢旧值会让“推送恰好停了”这件事变得不可观测。
func (s *PushedSnapshotStore) Load(instanceUUID string) (ProbeSnapshot, bool) {
	if s == nil {
		return ProbeSnapshot{}, false
	}
	s.mu.RLock()
	snap, ok := s.data[instanceUUID]
	s.mu.RUnlock()
	return snap, ok
}

// Forget 丢弃某实例的推送快照（实例删除/探针断开时调用，避免残留旧值）。
func (s *PushedSnapshotStore) Forget(instanceUUID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.data, instanceUUID)
	s.mu.Unlock()
}

// PushedMetrics 是 metric_snapshot.metrics 的解析形态（协议规格 §7.2）。
//
// 全部用指针：探针对不可用字段**直接不输出该键**，零值无法区分“真的是 0”与“没给”。
type PushedMetrics struct {
	TPS           *float64                   `json:"tps"`
	MSPTAvgSec    *float64                   `json:"msptAvg"` // 秒，需 *1000 转毫秒
	MSPTP95Sec    *float64                   `json:"msptP95"` // 秒
	PlayersOnline *int32                     `json:"playersOnline"`
	HeapUsed      *int64                     `json:"heapUsed"`
	HeapMax       *int64                     `json:"heapMax"`
	Threads       *int32                     `json:"threads"`
	CPULoad       *float64                   `json:"cpuLoad"`
	UptimeSeconds *float64                   `json:"uptimeSeconds"`
	GCCountTotal  *float64                   `json:"gcCountTotal"`
	GCTimeMillis  *float64                   `json:"gcTimeMillis"`
	Worlds        map[string]PushedWorldStat `json:"worlds"`

	// ── 桥自身的中转指标（本通道）─────────────────────────────────
	//
	// 与上面的 TPS/堆/线程不是一类：那些是「服务器/JVM 的快照」，这些是「通道的快照」。
	// 全部用指针：老探针**根本不输出这些键**，零值区分不了“真的是 0”与“没给”——
	// 而把“不知道”当成“健康”正是本项监控要防的事。
	BridgeReliableQueue          *int32 `json:"bridgeReliableQueue"`
	BridgeBestEffortQueue        *int32 `json:"bridgeBestEffortQueue"`
	BridgeReliableRejectedTotal  *int64 `json:"bridgeReliableRejectedTotal"`
	BridgeBestEffortDroppedTotal *int64 `json:"bridgeBestEffortDroppedTotal"`
	BridgeReceiptRejectedTotal   *int64 `json:"bridgeReceiptRejectedTotal"`
	BridgeSpillDroppedTotal      *int64 `json:"bridgeSpillDroppedTotal"`
	BridgeWriteFailuresTotal     *int64 `json:"bridgeWriteFailuresTotal"`
	BridgeWriteTimeoutsTotal     *int64 `json:"bridgeWriteTimeoutsTotal"`
}

// PushedWorldStat 是逐世界的区块 / 实体 / 方块实体计数。
type PushedWorldStat struct {
	LoadedChunks *int64 `json:"loadedChunks"`
	Entities     *int64 `json:"entities"`
	TileEntities *int64 `json:"tileEntities"`
}

// ProbeSnapshotFromPushed 把推送的 metrics 对象转换为 [ProbeSnapshot]。
//
// 转换规则与 HTTP 路径（parseServerProbeMetrics）**逐字段对齐**：
//   - MSPT：秒 → 毫秒（*1000）
//   - 不可用：保留 [ProbeSnapshot] 的既有哨兵语义（TPS/MSPT 用 -1，其余为 0）
//   - 世界表：缺项不建条目
//
// @param raw metric_snapshot.payload.metrics 的原始 JSON。
// @return 快照与是否解析成功（解析失败时调用方不应污染存储）。
func ProbeSnapshotFromPushed(raw json.RawMessage) (ProbeSnapshot, bool) {
	snap := ProbeSnapshot{Worlds: map[string]WorldStat{}, TPS: -1, MSPTAvgMillis: -1}
	if len(raw) == 0 {
		return snap, false
	}
	var m PushedMetrics
	if err := json.Unmarshal(raw, &m); err != nil {
		return snap, false
	}
	if m.TPS != nil {
		snap.TPS = *m.TPS
	}
	if m.MSPTAvgSec != nil {
		snap.MSPTAvgMillis = *m.MSPTAvgSec * 1000
	}
	if m.PlayersOnline != nil {
		snap.PlayersOnline = *m.PlayersOnline
	}
	if m.HeapUsed != nil {
		snap.HeapUsedBytes = *m.HeapUsed
	}
	if m.HeapMax != nil {
		snap.HeapMaxBytes = *m.HeapMax
	}
	if m.Threads != nil {
		snap.Threads = *m.Threads
	}
	if m.CPULoad != nil {
		snap.SystemCPULoad = *m.CPULoad
	}
	if m.UptimeSeconds != nil {
		snap.UptimeSeconds = *m.UptimeSeconds
	}
	if m.GCCountTotal != nil {
		snap.GCCountTotal = int64(*m.GCCountTotal)
	}
	if m.GCTimeMillis != nil {
		snap.GCTimeMillis = *m.GCTimeMillis
	}
	// 桥中转指标：**全部缺失时保持 nil**，不造一个全零的 BridgeSnapshot。
	//
	// 这是本项最关键的一条判定：老探针不发这些键，若这里无条件建一个全零快照，
	// CP 侧就会把它当成“队列空、一点没丢”的健康状态——恰好和事实相反。
	// 只要任一字段出现（新探针），就认为整组可信（它们同源于 SenderStatus 的一次快照）。
	if m.BridgeReliableQueue != nil || m.BridgeBestEffortQueue != nil ||
		m.BridgeReliableRejectedTotal != nil || m.BridgeBestEffortDroppedTotal != nil ||
		m.BridgeReceiptRejectedTotal != nil || m.BridgeSpillDroppedTotal != nil ||
		m.BridgeWriteFailuresTotal != nil || m.BridgeWriteTimeoutsTotal != nil {
		bridge := &BridgeSnapshot{}
		if m.BridgeReliableQueue != nil {
			bridge.ReliableQueue = *m.BridgeReliableQueue
		}
		if m.BridgeBestEffortQueue != nil {
			bridge.BestEffortQueue = *m.BridgeBestEffortQueue
		}
		if m.BridgeReliableRejectedTotal != nil {
			bridge.ReliableRejectedTotal = *m.BridgeReliableRejectedTotal
		}
		if m.BridgeBestEffortDroppedTotal != nil {
			bridge.BestEffortDroppedTotal = *m.BridgeBestEffortDroppedTotal
		}
		if m.BridgeReceiptRejectedTotal != nil {
			bridge.ReceiptRejectedTotal = *m.BridgeReceiptRejectedTotal
		}
		if m.BridgeSpillDroppedTotal != nil {
			bridge.SpillDroppedTotal = *m.BridgeSpillDroppedTotal
		}
		if m.BridgeWriteFailuresTotal != nil {
			bridge.WriteFailuresTotal = *m.BridgeWriteFailuresTotal
		}
		if m.BridgeWriteTimeoutsTotal != nil {
			bridge.WriteTimeoutsTotal = *m.BridgeWriteTimeoutsTotal
		}
		snap.Bridge = bridge
	}
	for name, w := range m.Worlds {
		stat := WorldStat{}
		if w.LoadedChunks != nil {
			stat.LoadedChunks = *w.LoadedChunks
		}
		if w.Entities != nil {
			stat.Entities = *w.Entities
		}
		if w.TileEntities != nil {
			stat.TileEntities = *w.TileEntities
		}
		snap.Worlds[name] = stat
	}
	return snap, true
}
