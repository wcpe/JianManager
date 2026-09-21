package metrics

import (
	"log/slog"
	"time"
)

// defaultDirectProbeTimeout 是直探（SLP/Query）默认超时。
// 对齐 ScrapeServerProbe 的 5s 量级但更轻；经平台设置可配（与 graceful_stop.timeout 同风格）。
const defaultDirectProbeTimeout = 3 * time.Second

// SourceMask 记录某拍实例遥测命中的来源位（供前端标注数据来源 / FR-447）。
type SourceMask uint8

const (
	SourceNone  SourceMask = 0
	SourceProbe SourceMask = 1 << 0
	SourceSLP   SourceMask = 1 << 1
	SourceQuery SourceMask = 1 << 2
)

// CollectConfig 描述一次实例直探所需的连接信息（端口由 CP 下发到 Worker）。
type CollectConfig struct {
	ProbePort    int           // ServerProbe /metrics 端口；0=未部署探针
	ServerPort   int           // SLP 目标端口（server-port）；0=未知
	QueryPort    int           // Query 目标端口（query.port）；0=未开启
	Host         string        // 直探主机，通常 "localhost"（与实例同机）
	SLPTimeout   time.Duration // SLP 超时；<=0 用默认
	QueryTimeout time.Duration // Query 超时；<=0 用默认
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
		if !t.PlayersOnlineAvailable {
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
// 任一来源失败仅记 Debug 日志并继续下一档，绝不 panic、不阻塞其它来源；
// SLP/Query 在同一实例任务内串行，避免为每实例翻倍 UDP/TCP 连接。
func CollectInstanceTelemetry(cfg CollectConfig) *InstanceTelemetry {
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}

	var probe *ProbeSnapshot
	if cfg.ProbePort > 0 {
		if snap, err := ScrapeServerProbe(host, cfg.ProbePort, ""); err == nil {
			probe = snap
		} else {
			slog.Debug("实例探针 /metrics 抓取失败，降级直探", "host", host, "probePort", cfg.ProbePort, "error", err)
		}
	}

	// SLP 总是尝试（MOTD/版本/favicon 只能由直探给出，探针给不了）。
	var slp *SLPSnapshot
	if cfg.ServerPort > 0 {
		if snap, err := PingSLP(host, cfg.ServerPort, cfg.SLPTimeout); err == nil {
			slp = snap
		} else {
			slog.Debug("实例 SLP 探测失败", "host", host, "serverPort", cfg.ServerPort, "error", err)
		}
	}

	// Query 仅在分配了 query.port 时尝试（需 enable-query=true，否则 UDP 无响应）。
	var query *QuerySnapshot
	if cfg.QueryPort > 0 {
		if snap, err := QueryServer(host, cfg.QueryPort, cfg.QueryTimeout); err == nil {
			query = snap
		} else {
			slog.Debug("实例 Query 探测失败（enable-query 未开或端口不可达）", "host", host, "queryPort", cfg.QueryPort, "error", err)
		}
	}

	return Orchestrate(probe, slp, query)
}
