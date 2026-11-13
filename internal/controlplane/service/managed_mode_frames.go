package service

import (
	"encoding/json"
	"fmt"
)

// 托管模式的帧协议（探针侧权威实现在 ServerProbe project:core 的 bridge/ 与 config/）。
//
// 本文件只定义 CP 侧需要构造 / 解析的那部分帧，形态**逐字段对齐探针实现**：
//   - 上行：`type=event` + `event=<子类型>`，业务载荷在 `data`（command_result）或
//     `payload`（config_applied / config_state / metric_snapshot）。两个键名不可互换，
//     探针侧就是按字面取值的。
//   - 下行：`type=command`，结构化载荷优先 `payload`（嵌套对象），
//     字符串形式用 `payloadJson`；探针先取 payloadJson、为空才取 payload。

// 托管模式下探针经桥上行的帧事件名（仅列出 CP 需要处理的）。
const (
	// BridgeEventHello 是连接后的首帧（顺序敏感：探针保证其它上行排在它之后）。
	BridgeEventHello = "hello"
	// BridgeEventMetricSnapshot 是托管模式的指标推送帧（代替 HTTP 拉取 /metrics）。
	BridgeEventMetricSnapshot = "metric_snapshot"
	// BridgeEventConfigState 是托管状态的周期上报（版本 / 生效值 / 连接态）。
	BridgeEventConfigState = "config_state"
	// BridgeEventConfigApplied 是配置应用回执（applied / failed / rejected）。
	BridgeEventConfigApplied = "config_applied"
	// BridgeEventCommandResult 是指令回执（治理命令与 core.mcp 共用）。
	BridgeEventCommandResult = "command_result"
)

// 探针在 hello 与 core.bridge_status 里声明的能力位（托管模式专属）。
const (
	// BridgeCapabilityMetricPush 表示指标改由 WS 推送；缺该位时 CP 应回退 HTTP 拉取。
	BridgeCapabilityMetricPush = "metric_push"
	// BridgeCapabilityConfigPush 表示探针接受 config_push 热下发。
	BridgeCapabilityConfigPush = "config_push"
	// BridgeCapabilityConfigState 表示探针周期上报 config_state。
	BridgeCapabilityConfigState = "config_state"
	// BridgeCapabilityMcpBridge 表示 MCP 工具面已挂上桥（零监听下的事故诊断通道）。
	// **只在挂载成功后声明**，故 hello 可能没有而后续 bridge_status 有。
	BridgeCapabilityMcpBridge = "mcp_bridge"
)

// ManagedConfigPush 是下行 `config_push` 的载荷（协议规格 §3.4.1）。
//
// ## baseVersion 是乐观并发基线，不是可选装饰
// 探针要求「当前版本必须等于 baseVersion」才应用，否则整包拒绝并回 `rejected`。
// CP 必须拿**上一次 `config_applied` 回报的版本**作为基线，而不能自增一个本地计数器：
// 探针的版本号是它自己的单调序号，与 CP 的代数不是同一回事。
// 首次下发（探针从未应用过任何托管配置）时 baseVersion 传 0。
//
// ## values 的键是 config.yml 的点分路径
// 例：`collect-period-ticks`（顶层）、`aggregation.window`（嵌套）、`world.sample-period-ticks`。
// 禁档键（bridge.* / metrics.* / mcp.* / web.*，共 18 个）**不得出现**，否则整包被拒。
type ManagedConfigPush struct {
	ConfigVersion int64          `json:"configVersion"`
	BaseVersion   int64          `json:"baseVersion"`
	Values        map[string]any `json:"values,omitempty"`
	Removals      []string       `json:"removals,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Operator      string         `json:"operator,omitempty"`
}

// BuildConfigPushPayload 把载荷序列化为下行帧可直接嵌套的 JSON 对象。
//
// 返回 json.RawMessage 而非字符串：探针优先取 `payloadJson`（字符串），
// 两者都支持，但嵌套对象形态让帧在日志里更可读，也避免二次转义。
func (p ManagedConfigPush) BuildConfigPushPayload() (json.RawMessage, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("序列化 config_push 载荷失败: %w", err)
	}
	return raw, nil
}

// ManagedConfigApplied 是探针回的应用结果（`config_applied` 帧的 payload）。
//
// ## result 只有三种取值
// `applied` / `failed` / `rejected`。注意 `conflict` **不是** result 取值，
// 它是 `rejected` 的 error 前缀（基线不符时探针回 `rejected` 且 error 以 conflict 开头）。
// 判错这两者会导致"版本冲突"被当成"协议错误"处理。
type ManagedConfigApplied struct {
	ConfigVersion int64          `json:"configVersion"`
	Result        string         `json:"result"`
	Effective     map[string]any `json:"effective,omitempty"`
	SelfCheck     map[string]any `json:"selfCheck,omitempty"`
	Error         string         `json:"error,omitempty"`
	AppliedAt     int64          `json:"appliedAt,omitempty"`
}

// 配置应用结果码（探针侧 ConfigPushOutcome.result 的原值）。
const (
	ConfigApplyResultApplied  = "applied"
	ConfigApplyResultFailed   = "failed"
	ConfigApplyResultRejected = "rejected"
)

// ManagedConfigState 是探针周期上报的托管状态（`config_state` 帧的 payload）。
//
// ## 两个字段的语义容易误读
//   - `cacheAgeSeconds`：距上次**成功应用**的秒数；从未应用过时为 **null**（不是 0）。
//     null 才是"无基线"的诚实表达，把它当 0 会误判成"刚刚生效过"。
//   - `connection`：四态 `connected` / `degraded` / `unauthorized` / `disconnected`。
//     其中 `unauthorized` 表示控制面拒了凭据——探针**不会**降级到文件配置（防止认知分裂），
//     所以 CP 看到它应当告警而不是当作普通断线重试。
type ManagedConfigState struct {
	ConfigVersion  int64          `json:"configVersion"`
	Effective      map[string]any `json:"effective,omitempty"`
	CacheAgeSecond *int64         `json:"cacheAgeSeconds"`
	Mode           string         `json:"mode"`
	Connection     string         `json:"connection"`
	Capabilities   []string       `json:"capabilities,omitempty"`
	LastError      string         `json:"lastError,omitempty"`
}

// 托管模式下探针上报的连接态四态（ManagedConfigState.Connection）。
const (
	BridgeConnectionConnected    = "connected"
	BridgeConnectionDegraded     = "degraded"
	BridgeConnectionUnauthorized = "unauthorized"
	BridgeConnectionDisconnected = "disconnected"
)

// ManagedMetricSnapshot 是托管模式下经桥上行的指标帧（payload 部分）。
//
// ## 单位口径（最容易接错的地方）
// `msptAvg` / `msptP95` 是**秒**，不是毫秒——协议规定毫秒换算在控制面侧做，
// 探针保持与 `/metrics` 的 `serverprobe_mspt_seconds` 同口径。
// 解析成内部结构时必须 *1000，否则面板上的 MSPT 会小一千倍。
//
// ## 缺项即不写
// 不可用字段（TPS/MSPT 为 null、哨兵 -1）探针**直接不输出该键**，不是填 0。
// 所以所有数值字段都必须是指针类型：零值无法区分"真的是 0"与"没给"。
type ManagedMetricSnapshot struct {
	TS       int64          `json:"ts"`
	Sequence int64          `json:"sequence"`
	Metrics  ManagedMetrics `json:"metrics"`
}

// ManagedMetrics 是 metric_snapshot.metrics 的字段集（协议规格 §7.2 的投影表）。
// 全部用指针：缺失与 0 必须能区分（见上）。
type ManagedMetrics struct {
	TPS           *float64                    `json:"tps,omitempty"`
	MSPTAvg       *float64                    `json:"msptAvg,omitempty"` // 秒
	MSPTP95       *float64                    `json:"msptP95,omitempty"` // 秒
	PlayersOnline *int32                      `json:"playersOnline,omitempty"`
	HeapUsed      *int64                      `json:"heapUsed,omitempty"` // 字节
	HeapMax       *int64                      `json:"heapMax,omitempty"`  // 字节
	Threads       *int32                      `json:"threads,omitempty"`
	CPULoad       *float64                    `json:"cpuLoad,omitempty"` // 0~1
	UptimeSeconds *float64                    `json:"uptimeSeconds,omitempty"`
	GCCountTotal  *float64                    `json:"gcCountTotal,omitempty"`
	GCTimeMillis  *float64                    `json:"gcTimeMillis,omitempty"`
	Worlds        map[string]ManagedWorldStat `json:"worlds,omitempty"`
}

// ManagedWorldStat 是逐世界的区块 / 实体 / 方块实体计数。
type ManagedWorldStat struct {
	LoadedChunks *int64 `json:"loadedChunks,omitempty"`
	Entities     *int64 `json:"entities,omitempty"`
	TileEntities *int64 `json:"tileEntities,omitempty"`
}

// ManagedHello 是探针首帧 hello 的解析结果。
//
// `managed` 是托管模式的硬标识：true 表示该探针按身份文件运行，
// 指标会走推送、本地面板/MCP/metrics 三个监听面已关断。
type ManagedHello struct {
	Type          string   `json:"type"`
	Instance      string   `json:"instance"`
	Platform      string   `json:"platform"`
	Version       string   `json:"version"`
	Capabilities  []string `json:"capabilities"`
	Subscriptions []string `json:"subscriptions"`
	Managed       bool     `json:"managed"`
}

// HasCapability 报告 hello / bridge_status 是否声明了某能力位。
func (h ManagedHello) HasCapability(capability string) bool {
	for _, c := range h.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}
