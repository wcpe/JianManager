package metrics

import (
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/platform/directprobe"
)

// defaultDirectProbeTimeout / maxDirectProbeTimeout 是直探（SLP/Query）超时的默认与上界。
//
// **不再各自写字面量**（FR-446 复审 NEW-ISSUE A）：本包、CP 写路径校验、CP 读/下发钳制统一引用
// internal/platform/directprobe，避免「CP 收下 30s、Worker 按另一上界钳制」这类静默漂移。
// 上界由心跳节拍护栏反推（`directprobe.MaxTimeout` 的文档给出了完整推导）。
const (
	defaultDirectProbeTimeout = directprobe.DefaultTimeout
	maxDirectProbeTimeout     = directprobe.MaxTimeout
	// probeScrapeTimeoutCap 是抓取探针 `/metrics` 的 HTTP 超时；同时被心跳采集预算计入
	// 「单实例同源串行最坏」（见 internal/worker/heartbeat）。
	probeScrapeTimeoutCap = directprobe.ProbeScrapeTimeoutCap
)

// SourceMask 记录某拍实例遥测命中的来源位（供前端标注数据来源 / FR-447）。
type SourceMask uint8

const (
	SourceNone  SourceMask = 0
	SourceProbe SourceMask = 1 << 0
	SourceSLP   SourceMask = 1 << 1
	SourceQuery SourceMask = 1 << 2
)

// CollectConfig 描述一次实例直探所需的连接信息（端口由 CP 下发到 Worker）。
// 端口为 0 表示该来源未配置/本拍跳过（如处于失败退避中），编排链不尝试该来源。
type CollectConfig struct {
	ProbePort    int           // ServerProbe /metrics 端口；0=未部署探针
	ServerPort   int           // SLP 目标端口（server-port）；0=未知
	QueryPort    int           // Query 目标端口（query.port）；0=未开启
	Host         string        // 直探主机，通常 "localhost"（与实例同机）
	SLPTimeout   time.Duration // SLP 超时；<=0 用生效默认（见 SetDirectProbeTimeouts）
	QueryTimeout time.Duration // Query 超时；<=0 用生效默认（见 SetDirectProbeTimeouts）
}

// InstanceTelemetry 是一拍实例遥测：各指标带可用性语义与来源，三档皆无 → 对应可用位为 false（显式「不可用」）。
//
// 优先级链 `探针 → SLP → Query → 不可用`（FR-447）：同指标多源以探针为准；探针缺该指标时按 SLP → Query 回落；
// 玩家名单例外——Query 优先（唯一实名来源），Query 无则回退 SLP sample 并标 PlayerNamesPartial。
// 不可用是明确的「缺测」语义，不再以 -1/-- 之类的伪值占位。
type InstanceTelemetry struct {
	ProbeAvailable bool

	// 探针深度指标（仅 SourceProbe 命中时有效）。
	TPS           float64
	MSPTMillis    float64
	MSPTP95Millis float64
	HeapUsedBytes int64
	HeapMaxBytes  int64
	Threads       int32
	CPULoad       float64
	UptimeSeconds float64
	Worlds        map[string]WorldStat

	// 基础信息（探针 / SLP / Query 任一命中）。
	Motd               string
	Version            string
	Favicon            string
	PlayersOnline      int32
	PlayersMax         int32
	PlayerNames        []string
	PlayerNamesPartial bool // true=名单取自 SLP sample（可能不完整，非实名）
	Plugins            []string
	Map                string

	// 可用性位（false = 不可用/缺测；前端据此渲染「不可用」而非伪值）。
	PlayersOnlineAvailable bool
	PlayersMaxAvailable    bool
	MotdAvailable          bool
	VersionAvailable       bool
	FaviconAvailable       bool
	PlayerNamesAvailable   bool
	PluginsAvailable       bool
	MapAvailable           bool

	SLPAvailable   bool
	QueryAvailable bool
	Sources        SourceMask

	// 各来源的失败原因（仅对应来源不可用且**本拍尝试过**时非空）：
	// 供心跳的「来源皆不可用」告警把错误类别并入文案（FR-446 审计项 4），不参与编排决策。
	ProbeErr error
	SLPErr   error
	QueryErr error
}

// Orchestrate 按「探针 → SLP → Query → 不可用」优先级链合并三个来源（FR-447）。
// 纯函数、无 IO，三个入参可为 nil（对应来源不可用），便于穷举三态组合测试。
func Orchestrate(probe *ProbeSnapshot, slp *SLPSnapshot, query *QuerySnapshot) *InstanceTelemetry {
	t := &InstanceTelemetry{}

	// 探针为权威来源：提供 TPS/MSPT/JVM/分世界，以及在线人数（三档中最细）。
	if probe != nil {
		t.ProbeAvailable = true
		t.Sources |= SourceProbe
		t.TPS = probe.TPS
		t.MSPTMillis = probe.MSPTAvgMillis
		t.HeapUsedBytes = probe.HeapUsedBytes
		t.HeapMaxBytes = probe.HeapMaxBytes
		t.Threads = probe.Threads
		t.CPULoad = probe.SystemCPULoad
		t.UptimeSeconds = probe.UptimeSeconds
		t.Worlds = probe.Worlds
		// 探针无最大人数/MOTD/版本/名单；仅在线人数可用。
		t.PlayersOnline = probe.PlayersOnline
		t.PlayersOnlineAvailable = true
	}

	// SLP 为直探保底：MOTD/版本/favicon 的主来源，并回填探针缺的在线/最大人数。
	if slp != nil {
		t.SLPAvailable = true
		t.Sources |= SourceSLP
		if !t.PlayersOnlineAvailable {
			t.PlayersOnline = slp.PlayersOnline
			t.PlayersOnlineAvailable = true
		}
		if !t.PlayersMaxAvailable && slp.PlayersMax > 0 {
			t.PlayersMax = slp.PlayersMax
			t.PlayersMaxAvailable = true
		}
		if slp.Motd != "" {
			t.Motd = slp.Motd
			t.MotdAvailable = true
		}
		if slp.Version != "" {
			t.Version = slp.Version
			t.VersionAvailable = true
		}
		if slp.Favicon != "" {
			t.Favicon = slp.Favicon
			t.FaviconAvailable = true
		}
		if len(slp.PlayerSample) > 0 && !t.PlayerNamesAvailable {
			t.PlayerNames = append([]string(nil), slp.PlayerSample...)
			t.PlayerNamesAvailable = true
			t.PlayerNamesPartial = true // SLP sample 弱信息，标注「可能不完整」
		}
	}

	// Query 为增强来源：唯一实名名单，并补插件列表/地图名；基础信息仅在探针/SLP 都缺时回填。
	if query != nil {
		t.QueryAvailable = true
		t.Sources |= SourceQuery
		// 玩家名单优先级例外：Query 优先（实名可信）。
		if len(query.PlayerNames) > 0 {
			t.PlayerNames = append([]string(nil), query.PlayerNames...)
			t.PlayerNamesAvailable = true
			t.PlayerNamesPartial = false
		}
		// 在线人数：仅在 numplayers 真的存在且可解析时才认（FR-447 不伪造 0）。
		if !t.PlayersOnlineAvailable && query.PlayersOnlineAvailable {
			t.PlayersOnline = query.PlayersOnline
			t.PlayersOnlineAvailable = true
		}
		if !t.PlayersMaxAvailable && query.PlayersMax > 0 {
			t.PlayersMax = query.PlayersMax
			t.PlayersMaxAvailable = true
		}
		if !t.MotdAvailable && query.Motd != "" {
			t.Motd = query.Motd
			t.MotdAvailable = true
		}
		if !t.VersionAvailable && query.Version != "" {
			t.Version = query.Version
			t.VersionAvailable = true
		}
		if len(query.Plugins) > 0 {
			t.Plugins = append([]string(nil), query.Plugins...)
			t.PluginsAvailable = true
		}
		if query.Map != "" {
			t.Map = query.Map
			t.MapAvailable = true
		}
	}

	return t
}

// CollectInstanceTelemetry 执行一次实例采集（FR-447 编排链）：
// 先抓探针；再按需直探 SLP / Query 补基础信息；最后统一编排。
//
// 三者皆无 → 返回的遥测所有可用位为 false（显式「不可用」，不留 -1/-- 占位）。
// 任一来源失败仅记 Debug 日志并把错误类别留在 InstanceTelemetry.ProbeErr/SLPErr/QueryErr
// （供调用方并入告警文案），继续下一档，绝不 panic、不阻塞其它来源；
// SLP/Query 在同一实例任务内串行，避免为每实例翻倍 UDP/TCP 连接。
//
// cfg 中超时 <=0 时用进程生效值（CP 经心跳下发，见 SetDirectProbeTimeouts），仍为 0 用内置默认。
func CollectInstanceTelemetry(cfg CollectConfig) *InstanceTelemetry {
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}
	effectiveSLP, effectiveQuery := DirectProbeTimeouts()
	if cfg.SLPTimeout > 0 {
		effectiveSLP = cfg.SLPTimeout
	}
	if cfg.QueryTimeout > 0 {
		effectiveQuery = cfg.QueryTimeout
	}

	var probe *ProbeSnapshot
	var probeErr error
	if cfg.ProbePort > 0 {
		if snap, err := ScrapeServerProbe(host, cfg.ProbePort, ""); err == nil {
			probe = snap
		} else {
			probeErr = err
			slog.Debug("实例探针 /metrics 抓取失败，降级直探", "host", host, "probePort", cfg.ProbePort,
				"category", ProbeErrorCategory(err), "error", err)
		}
	}

	// SLP 总是尝试（MOTD/版本/favicon 只能由直探给出，探针给不了）。
	var slp *SLPSnapshot
	var slpErr error
	if cfg.ServerPort > 0 {
		if snap, err := PingSLP(host, cfg.ServerPort, effectiveSLP); err == nil {
			slp = snap
		} else {
			slpErr = err
			slog.Debug("实例 SLP 探测失败", "host", host, "serverPort", cfg.ServerPort,
				"category", ProbeErrorCategory(err), "error", err)
		}
	}

	// Query 仅在分配了 query.port 时尝试（需 enable-query=true，否则 UDP 无响应）。
	var query *QuerySnapshot
	var queryErr error
	if cfg.QueryPort > 0 {
		if snap, err := QueryServer(host, cfg.QueryPort, effectiveQuery); err == nil {
			query = snap
		} else {
			queryErr = err
			slog.Debug("实例 Query 探测失败（enable-query 未开或端口不可达）", "host", host,
				"queryPort", cfg.QueryPort, "category", ProbeErrorCategory(err), "error", err)
		}
	}

	tel := Orchestrate(probe, slp, query)
	// 错误只挂在**本拍尝试过且失败**的来源上：未配置（端口 0）时保持 nil，避免把「未启用」误报成故障。
	tel.ProbeErr = probeErr
	tel.SLPErr = slpErr
	tel.QueryErr = queryErr
	return tel
}
