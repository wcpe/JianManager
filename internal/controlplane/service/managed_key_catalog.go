package service

import "sort"

// 探针配置键的**编辑元数据**（类型 / 默认值 / 中文说明 / 取值范围 / 可选值 / 分组）。
//
// ## 为什么需要这份表
//
// 定档表（managed_mode_tiers.go）只回答"这个键能不能远程写、风险多高"，不回答
// "它是什么类型、能填什么、默认多少"。没有后者，前端只能给用户一个 JSON 文本框——
// 66 个点分键靠手写，既容易写错档位，也看不出取值范围。
//
// ## 权威来源
//
// 类型与默认值抄自探针的 `plugin/src/main/resources/config.yml`（390 行、66 键全注释），
// 取值范围抄自 `ConfigTier.kt` 的 `BOUNDS` / `SAFE_TYPES`，可选值抄自各键的注释枚举。
// **探针侧才是真源**：那边改了这边要跟着改，否则界面会给出探针不接受的值。
//
// ## 缺元数据的键怎么办
//
// 查不到元数据时 `ManagedKeyMetaOf` 返回 false，调用方回退到 JSON 编辑（而不是猜一个类型）。
// 猜错的后果是用户填了界面允许、探针却拒绝的值——比不给控件更糟。

// ManagedKeyKind 是配置键的编辑类型（决定前端用什么控件）。
type ManagedKeyKind string

const (
	// ManagedKeyBool 布尔：前端渲染开关。
	ManagedKeyBool ManagedKeyKind = "bool"
	// ManagedKeyInt 整数：前端渲染数字输入 + 可选上下限。
	ManagedKeyInt ManagedKeyKind = "int"
	// ManagedKeyFloat 浮点：前端渲染数字输入（step 允许小数）。
	ManagedKeyFloat ManagedKeyKind = "float"
	// ManagedKeyString 字符串：前端渲染文本框。
	ManagedKeyString ManagedKeyKind = "string"
	// ManagedKeyStringList 字符串数组：前端渲染标签列表（增删）。
	ManagedKeyStringList ManagedKeyKind = "stringList"
	// ManagedKeyObject 结构化对象：前端渲染专用编辑器或 JSON 回退。
	ManagedKeyObject ManagedKeyKind = "object"
)

// ManagedKeyGroup 是配置键的界面分组（与探针 config.yml 的注释分段对齐）。
type ManagedKeyGroup string

const (
	ManagedGroupCollect  ManagedKeyGroup = "collect"  // 采集节拍与内存缓冲
	ManagedGroupHistory  ManagedKeyGroup = "history"  // 历史落盘与归档
	ManagedGroupWorld    ManagedKeyGroup = "world"    // 世界 / 实体采集
	ManagedGroupFolia    ManagedKeyGroup = "folia"    // Folia region
	ManagedGroupForensic ManagedKeyGroup = "forensic" // 网络取证
	ManagedGroupHTTP     ManagedKeyGroup = "http"     // 外呼监控
	ManagedGroupCPU      ManagedKeyGroup = "cpu"      // CPU 归因
	ManagedGroupIncision ManagedKeyGroup = "incision" // Incision 织入
	ManagedGroupAlert    ManagedKeyGroup = "alert"    // 告警
	ManagedGroupMCP      ManagedKeyGroup = "mcp"      // MCP 任务与工件
	ManagedGroupManaged  ManagedKeyGroup = "managed"  // 托管上行节拍
	ManagedGroupLocal    ManagedKeyGroup = "local"    // 仅本地可改（监听面 / 凭证）
)

// ManagedKeyMeta 是一个配置键的编辑元数据。
type ManagedKeyMeta struct {
	// Key 点分配置键（与下发载荷逐字一致）。
	Key string `json:"key"`
	// Kind 编辑类型。
	Kind ManagedKeyKind `json:"kind"`
	// Group 界面分组。
	Group ManagedKeyGroup `json:"group"`
	// Tier 档位（safe / guarded / forbidden）。
	Tier string `json:"tier"`
	// Title 中文短名（界面上作为字段标签）。
	Title string `json:"title"`
	// Description 中文说明（作为字段下方的辅助文案）。
	Description string `json:"description"`
	// Default 探针默认值（JSON 标量或数组；object 类型不填）。
	Default any `json:"default,omitempty"`
	// Min/Max 数值边界（仅 int/float 有意义；nil 表示不限）。
	//
	// 取自探针 ConfigTier.BOUNDS：越界值探针会整包拒绝，前端据此提前拦下。
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// BoundsHint 越界时的中文理由（直接显示给用户，比只报"超出范围"有用）。
	BoundsHint string `json:"boundsHint,omitempty"`
	// Options 可选值（仅枚举型键；非空时前端渲染下拉而非自由输入）。
	Options []string `json:"options,omitempty"`
	// Unit 展示单位（如 "tick"、"秒"、"MB"）。
	Unit string `json:"unit,omitempty"`
	// Fields 子字段（仅 object 类型，如 alert.rules 的单条规则结构）。
	Fields []ManagedKeyMeta `json:"fields,omitempty"`
}

// managedKeyCatalog 是 66 键的元数据表。
//
// 键必须与 managedConfigTiers 完全一致（有单测钉住），否则界面上会出现
// "能编辑但下发了会被拒"或"能下发但界面看不见"的键。
var managedKeyCatalog = map[string]ManagedKeyMeta{
	// ── 采集节拍 ──
	"collect-period-ticks": {
		Kind: ManagedKeyInt, Group: ManagedGroupCollect,
		Title:       "指标采集周期",
		Description: "多久采集一次指标。默认 100（约 5 秒）。调小会让指标更细但增加主线程压力。",
		Default:     100, Min: floatPtr(20), Unit: "tick",
		BoundsHint: "过小会放大指标量并压主线程（下限 20 tick = 1 秒）",
	},
	"history-capacity": {
		Kind: ManagedKeyInt, Group: ManagedGroupCollect,
		Title:       "内存历史保留份数",
		Description: "内存中保留多少份近期快照（每采集周期一份）。默认 360（约 30 分钟）。",
		Default:     360, Max: floatPtr(20000), Unit: "份",
		BoundsHint: "内存无界增长风险（上限 20000 份）",
	},
	"aggregation.window": {
		Kind: ManagedKeyInt, Group: ManagedGroupCollect,
		Title:       "聚合窗口大小",
		Description: "对近期历史做跨快照统计时取多少份。默认 12。必须不大于「内存历史保留份数」。",
		Default:     12, Unit: "份",
		BoundsHint: "逻辑一致性：不得大于 history-capacity",
	},
	"startup-top-n": {
		Kind: ManagedKeyInt, Group: ManagedGroupCollect,
		Title:       "启动慢插件榜条数",
		Description: "启动摘要里展示多少个最慢插件。默认 5。",
		Default:     5, Min: floatPtr(0), Unit: "条",
	},
	"server-name": {
		Kind: ManagedKeyString, Group: ManagedGroupCollect,
		Title:       "实例名称",
		Description: "用于区分多个服务端实例。留空则自动生成稳定实例 ID（跨重启不变）。",
		Default:     "",
	},
	"debug": {
		Kind: ManagedKeyBool, Group: ManagedGroupCollect,
		Title:       "调试日志",
		Description: "是否输出 DEBUG 级日志。默认关闭；排查问题时临时打开，用完记得关。",
		Default:     false,
	},
	"agent-stack-max-samples": {
		Kind: ManagedKeyInt, Group: ManagedGroupCollect,
		Title:       "启动画像栈样本数",
		Description: "启动期采集的折叠栈样本条数。默认 10；0 表示不限（画像可能很大）。",
		Default:     10, Min: floatPtr(0), Unit: "条",
	},

	// ── 历史落盘 ──
	"history-retention": {
		Kind: ManagedKeyInt, Group: ManagedGroupHistory,
		Title:       "启动画像归档保留数",
		Description: "每次启动落盘一份画像，超出后清理最旧的。默认 30。",
		Default:     30, Min: floatPtr(0), Unit: "份",
	},
	"history-file.enabled": {
		Kind: ManagedKeyBool, Group: ManagedGroupHistory,
		Title:       "历史指标落盘",
		Description: "把每次采集的快照按日滚动写入 data/metrics/。与内存缓冲、画像归档互不影响。",
		Default:     true,
	},
	"history-file.retention-days": {
		Kind: ManagedKeyInt, Group: ManagedGroupHistory,
		Title:       "历史文件保留天数",
		Description: "按自然日保留最近 N 天（含当天）。默认 7。",
		Default:     7, Min: floatPtr(0), Unit: "天", BoundsHint: "负数无意义",
	},
	"history-file.max-total-mb": {
		Kind: ManagedKeyInt, Group: ManagedGroupHistory,
		Title:       "历史文件总体积上限",
		Description: "超出后从最旧文件起清理至达标，绝不删除当天文件。默认 200。",
		Default:     200, Min: floatPtr(0), Unit: "MB",
	},
	"history-file.archive-days": {
		Kind: ManagedKeyInt, Group: ManagedGroupHistory,
		Title:       "历史归档保留天数",
		Description: "过期文件压缩为 .gz 后保留多少天。0 = 过期即删、不归档。默认 30。",
		Default:     30, Min: floatPtr(0), Unit: "天",
	},

	// ── 世界采集 ──
	"world.sample-period-ticks": {
		Kind: ManagedKeyInt, Group: ManagedGroupWorld,
		Title:       "世界采样周期",
		Description: "世界指标采样间隔。默认 600（约 30 秒）。需遍历所有区块，开销较高故独立限频。",
		Default:     600, Min: floatPtr(20), Unit: "tick",
		BoundsHint: "世界采集需遍历所有世界与区块，过小会放大采集开销（下限 20 tick）",
	},
	"world.entity-types": {
		Kind: ManagedKeyBool, Group: ManagedGroupWorld,
		Title:       "统计实体类型分布",
		Description: "开启后统计各世界按类型的实体分布；关闭仅给出实体总数。默认开启。",
		Default:     true,
	},

	// ── Folia ──
	"folia.observed-region-expire-seconds": {
		Kind: ManagedKeyInt, Group: ManagedGroupFolia,
		Title:       "Folia region 过期秒数",
		Description: "region 连续多久没有真实 tick 样本就从结果中移除。默认 60，最小 1。",
		Default:     60, Min: floatPtr(1), Unit: "秒",
	},

	// ── 网络取证 ──
	"network-forensics.enabled": {
		Kind: ManagedKeyBool, Group: ManagedGroupForensic,
		Title:       "网络取证",
		Description: "是否开启本地 SQLite 数据包取证。默认开启；驱动加载失败时仅取证降级，流量聚合继续。",
		Default:     true,
	},
	"network-forensics.payload-packet-types": {
		Kind: ManagedKeyStringList, Group: ManagedGroupForensic,
		Title:       "允许存载荷的包类型",
		Description: "规范化包类型列表。默认空 = 只存元数据与哈希。",
		Default:     []string{},
	},
	"network-forensics.payload-channels": {
		Kind: ManagedKeyStringList, Group: ManagedGroupForensic,
		Title:       "允许存载荷的频道",
		Description: "Plugin Message 频道列表，必须与上方包类型白名单同时命中才保存。默认空。",
		Default:     []string{},
	},
	"network-forensics.max-payload-bytes": {
		Kind: ManagedKeyInt, Group: ManagedGroupForensic,
		Title:       "单包载荷上限",
		Description: "单包最多保存多少字节，超出会截断（原始长度与 SHA-256 仍记录）。默认 65536。",
		Default:     65536, Min: floatPtr(0), Unit: "字节",
	},
	"network-forensics.retention-days": {
		Kind: ManagedKeyInt, Group: ManagedGroupForensic,
		Title:       "取证记录保留天数",
		Description: "默认 60；超期记录优先删除。",
		Default:     60, Min: floatPtr(0), Unit: "天",
	},
	"network-forensics.max-total-gib": {
		Kind: ManagedKeyInt, Group: ManagedGroupForensic,
		Title:       "取证库体积上限",
		Description: "SQLite 与 WAL 总上限。默认 4，最小 1；超限从最早记录开始删。",
		Default:     4, Min: floatPtr(1), Unit: "GiB",
	},

	// ── 外呼监控 ──
	"http-monitor.enabled": {
		Kind: ManagedKeyBool, Group: ManagedGroupHTTP,
		Title:       "外呼监控",
		Description: "需以 -javaagent 挂载 agent 才生效。记录哪个插件向哪发了请求、耗时、响应码。默认开启。",
		Default:     true,
	},
	"http-monitor.log-to-console": {
		Kind: ManagedKeyBool, Group: ManagedGroupHTTP,
		Title:       "外呼实时打日志",
		Description: "默认开启；外呼频繁刷屏时可关闭，仍会落盘与进 /probe http。",
		Default:     true,
	},
	"http-monitor.log-headers": {
		Kind: ManagedKeyBool, Group: ManagedGroupHTTP,
		Title:       "外呼日志含请求头",
		Description: "关闭时日志不附请求头。默认关闭。请求头中的敏感项由 agent 侧打码。",
		Default:     false,
	},
	"http-monitor.file-enabled": {
		Kind: ManagedKeyBool, Group: ManagedGroupHTTP,
		Title:       "外呼明细落盘",
		Description: "按日滚动写入 data/http/http-<日期>.log。默认开启。",
		Default:     true,
	},
	"http-monitor.file-retention-days": {
		Kind: ManagedKeyInt, Group: ManagedGroupHTTP,
		Title:       "外呼日志保留天数",
		Description: "过期文件压缩为 .gz 归档。默认 7。",
		Default:     7, Min: floatPtr(0), Unit: "天",
	},
	"http-monitor.file-archive-days": {
		Kind: ManagedKeyInt, Group: ManagedGroupHTTP,
		Title:       "外呼归档保留天数",
		Description: "0 = 过期直接删除、不归档。默认 30。",
		Default:     30, Min: floatPtr(0), Unit: "天",
	},
	"http-monitor.recent-capacity": {
		Kind: ManagedKeyInt, Group: ManagedGroupHTTP,
		Title:       "外呼内存缓冲条数",
		Description: "/probe http 可回看的近期外呼条数。默认 500。",
		Default:     500, Min: floatPtr(0), Unit: "条",
	},
	"http-monitor.drain-period-ticks": {
		Kind: ManagedKeyInt, Group: ManagedGroupHTTP,
		Title:       "从 agent 拉取周期",
		Description: "从 agent 增量拉取外呼记录的间隔。默认 20（约 1 秒）。",
		Default:     20, Min: floatPtr(1), Unit: "tick",
	},

	// ── CPU 归因 ──
	"cpu.enabled": {
		Kind: ManagedKeyBool, Group: ManagedGroupCPU,
		Title:       "CPU 归因",
		Description: "周期采样线程栈、按插件归并，得出哪个插件更吃线程。默认开启（采样在异步线程）。",
		Default:     true,
	},
	"cpu.sample-period-ticks": {
		Kind: ManagedKeyInt, Group: ManagedGroupCPU,
		Title:       "CPU 采样周期",
		Description: "默认 20（约 1 秒）。",
		Default:     20, Min: floatPtr(20), Unit: "tick",
		BoundsHint: "CPU 采样开销压在服务器主线程，过小会拖慢服务器（下限 20 tick）",
	},
	"cpu.window-rounds": {
		Kind: ManagedKeyInt, Group: ManagedGroupCPU,
		Title:       "CPU 聚合窗口轮数",
		Description: "每采样周期一轮。默认 60（约 1 分钟）。",
		Default:     60, Min: floatPtr(1), Unit: "轮",
	},

	// ── Incision ──
	"incision.enabled": {
		Kind: ManagedKeyBool, Group: ManagedGroupIncision,
		Title:       "Incision 方法级归因",
		Description: "织入 SimplePluginManager 获取逐插件精确耗时。默认关闭；开启需重启。织入失败会降级，不阻止插件启用。",
		Default:     false,
	},

	// ── 告警 ──
	"alert.enabled": {
		Kind: ManagedKeyBool, Group: ManagedGroupAlert,
		Title:       "告警引擎",
		Description: "总开关。默认关闭。",
		Default:     false,
	},
	"alert.channels.log": {
		Kind: ManagedKeyBool, Group: ManagedGroupAlert,
		Title:       "告警写日志",
		Description: "写入控制台/日志文件（WARN/CRITICAL/恢复 分级）。默认开启。",
		Default:     true,
	},
	"alert.channels.in-game": {
		Kind: ManagedKeyBool, Group: ManagedGroupAlert,
		Title:       "告警推游戏内",
		Description: "推送给在线 OP 或持 serverprobe.alert 权限的玩家（仅 Bukkit 端）。默认开启。",
		Default:     true,
	},
	"alert.channels.webhook.enabled": {
		Kind: ManagedKeyBool, Group: ManagedGroupAlert,
		Title:       "Webhook 告警",
		Description: "把告警以 JSON POST 到外部地址（钉钉/飞书/企业微信/自建网关）。默认关闭。",
		Default:     false,
	},
	"alert.channels.webhook.url": {
		Kind: ManagedKeyString, Group: ManagedGroupAlert,
		Title:       "Webhook 地址",
		Description: "回调地址。留空=不投递。地址可能含 token，探针不会写入日志，但请勿外泄。",
		Default:     "",
	},
	"alert.channels.webhook.timeout-ms": {
		Kind: ManagedKeyInt, Group: ManagedGroupAlert,
		Title:       "Webhook 超时",
		Description: "连接/读取超时。默认 3000；过小会被兜底为下限。",
		Default:     3000, Min: floatPtr(1), Unit: "毫秒",
	},
	"alert.rules": {
		Kind: ManagedKeyObject, Group: ManagedGroupAlert,
		Title:       "告警规则",
		Description: "每条规则含阈值、持续周期数、级别与启用开关。键名即规则名（如 tps-warn）。",
		Fields: []ManagedKeyMeta{
			{Key: "threshold", Kind: ManagedKeyFloat, Title: "阈值", Description: "超过/低于该值触发。", Default: 0},
			{Key: "sustain-cycles", Kind: ManagedKeyInt, Title: "持续周期", Description: "连续几个采集周期满足才触发（防抖）。", Default: 3, Min: floatPtr(1), Unit: "周期"},
			{Key: "level", Kind: ManagedKeyString, Title: "级别", Description: "告警严重程度。", Default: "WARN", Options: []string{"WARN", "CRITICAL"}},
			{Key: "enabled", Kind: ManagedKeyBool, Title: "启用", Description: "是否启用这条规则。", Default: true},
		},
	},

	// ── MCP 任务与工件 ──
	"mcp.task-max-concurrent": {
		Kind: ManagedKeyInt, Group: ManagedGroupMCP,
		Title:       "Arthas 并发任务数",
		Description: "同时执行的长任务上限。默认 4，最小 1。",
		Default:     4, Min: floatPtr(1), Unit: "个",
	},
	"mcp.task-timeout-minutes": {
		Kind: ManagedKeyInt, Group: ManagedGroupMCP,
		Title:       "Arthas 任务超时",
		Description: "单任务最长运行分钟数。0 = 不限时。默认 30。",
		Default:     30, Min: floatPtr(0), Unit: "分钟",
	},
	"mcp.task-output-mebibytes": {
		Kind: ManagedKeyInt, Group: ManagedGroupMCP,
		Title:       "任务输出上限",
		Description: "单任务内存输出上限。默认 64，最小 1。",
		Default:     64, Min: floatPtr(1), Unit: "MiB",
	},
	"mcp.task-retention-minutes": {
		Kind: ManagedKeyInt, Group: ManagedGroupMCP,
		Title:       "任务结果保留",
		Description: "已完成任务的结果保留分钟数。默认 60，最小 1。",
		Default:     60, Min: floatPtr(1), Unit: "分钟",
	},
	"mcp.artifact-retention-hours": {
		Kind: ManagedKeyInt, Group: ManagedGroupMCP,
		Title:       "工件保留小时数",
		Description: "默认 24，最小 1。",
		Default:     24, Min: floatPtr(1), Unit: "小时",
	},
	"mcp.artifact-max-gib": {
		Kind: ManagedKeyInt, Group: ManagedGroupMCP,
		Title:       "工件工作区上限",
		Description: "默认 10，最小 1。",
		Default:     10, Min: floatPtr(1), Unit: "GiB",
	},
	"mcp.audit-max-file-mb": {
		Kind: ManagedKeyInt, Group: ManagedGroupMCP,
		Title:       "审计轮转阈值",
		Description: "活跃审计文件超过该体积后 gzip 轮转。默认 20。",
		Default:     20, Min: floatPtr(1), Unit: "MB",
	},
	"mcp.audit-retention-days": {
		Kind: ManagedKeyInt, Group: ManagedGroupMCP,
		Title:       "审计归档保留天数",
		Description: "0 = 归档不过期。默认 30。",
		Default:     30, Min: floatPtr(0), Unit: "天",
	},

	// ── 托管上行节拍（预定档表，不在 66 键口径内）──
	"metrics-push-interval-ms": {
		Kind: ManagedKeyInt, Group: ManagedGroupManaged,
		Title:       "指标推送间隔",
		Description: "托管模式下指标经 WS 推送的间隔。物理下限 1000ms。默认 5000。",
		Default:     5000, Min: floatPtr(1000), Unit: "毫秒",
		BoundsHint: "推送间隔过短会把反向 WS 打满（下限 1000ms）",
	},
	"config-state-interval-ms": {
		Kind: ManagedKeyInt, Group: ManagedGroupManaged,
		Title:       "状态上报间隔",
		Description: "config_state 周期上报间隔。物理下限 5000ms。默认 30000。",
		Default:     30000, Min: floatPtr(5000), Unit: "毫秒",
		BoundsHint: "上报过密没有收益（下限 5000ms）",
	},

	// ── 禁止档：仅本地可改（列出但只读）──
	"bridge.enabled":      {Kind: ManagedKeyBool, Group: ManagedGroupLocal, Title: "插件桥开关", Description: "由控制面建服时自动写入。托管模式下改它会切断与控制面的连接。"},
	"bridge.url":          {Kind: ManagedKeyString, Group: ManagedGroupLocal, Title: "插件桥地址", Description: "本机 Worker 的 WS 地址，由控制面建服时写入。"},
	"bridge.instance":     {Kind: ManagedKeyString, Group: ManagedGroupLocal, Title: "插件桥实例标识", Description: "实例 UUID，须与 token 内 instanceId 一致。"},
	"bridge.token":        {Kind: ManagedKeyString, Group: ManagedGroupLocal, Title: "插件桥凭据", Description: "实例级 JWT，仅在 config 里存在，绝不入库或外泄。"},
	"metrics.enabled":     {Kind: ManagedKeyBool, Group: ManagedGroupLocal, Title: "/metrics 端点", Description: "托管模式已关闭此监听面（零监听承诺）。"},
	"metrics.host":        {Kind: ManagedKeyString, Group: ManagedGroupLocal, Title: "/metrics 绑定地址", Description: "托管模式不监听。"},
	"metrics.port":        {Kind: ManagedKeyInt, Group: ManagedGroupLocal, Title: "/metrics 端口", Description: "托管模式不监听。"},
	"metrics.token":       {Kind: ManagedKeyString, Group: ManagedGroupLocal, Title: "/metrics 凭据", Description: "凭证类键，禁止远程写入。"},
	"metrics.allowed-ips": {Kind: ManagedKeyStringList, Group: ManagedGroupLocal, Title: "/metrics IP 白名单", Description: "托管模式不监听。"},
	"mcp.enabled":         {Kind: ManagedKeyBool, Group: ManagedGroupLocal, Title: "MCP 本地服务", Description: "托管模式改为经插件桥暴露（mcp_bridge 能力位）。"},
	"mcp.host":            {Kind: ManagedKeyString, Group: ManagedGroupLocal, Title: "MCP 绑定地址", Description: "托管模式不监听。"},
	"mcp.port":            {Kind: ManagedKeyInt, Group: ManagedGroupLocal, Title: "MCP 端口", Description: "托管模式不监听。"},
	"mcp.secret":          {Kind: ManagedKeyString, Group: ManagedGroupLocal, Title: "MCP 密钥", Description: "凭证类键，禁止远程写入。"},
	"web.enabled":         {Kind: ManagedKeyBool, Group: ManagedGroupLocal, Title: "Web 面板", Description: "托管模式已关闭此监听面。"},
	"web.host":            {Kind: ManagedKeyString, Group: ManagedGroupLocal, Title: "Web 绑定地址", Description: "托管模式不监听。"},
	"web.port":            {Kind: ManagedKeyInt, Group: ManagedGroupLocal, Title: "Web 端口", Description: "托管模式不监听。"},
	"web.token":           {Kind: ManagedKeyString, Group: ManagedGroupLocal, Title: "Web 凭据", Description: "凭证类键，禁止远程写入。"},
	"web.allowed-ips":     {Kind: ManagedKeyStringList, Group: ManagedGroupLocal, Title: "Web IP 白名单", Description: "托管模式不监听。"},
}

// ManagedKeyMetaOf 查一个键的编辑元数据。
//
// ok=false 表示元数据缺失：调用方应回退到 JSON 编辑，**不要猜类型**。
func ManagedKeyMetaOf(key string) (ManagedKeyMeta, bool) {
	meta, ok := managedKeyCatalog[key]
	if !ok {
		return ManagedKeyMeta{}, false
	}
	// 档位以定档表为准（两张表职责不同：这张管"怎么编"，那张管"能不能写"）。
	// 用 WireName（英文枚举）而不是 String（中文）：接口字段供机器判断，
	// 中文只用于展示。混用会让前端只能硬编码中文来比较档位。
	if tier, known := ManagedConfigTierOf(key); known {
		meta.Tier = tier.WireName()
	} else {
		meta.Tier = ConfigTierForbidden.WireName()
	}
	// 子字段（如 alert.rules 的 threshold / level）本身不入定档表——档位由父键决定。
	// 这里把它们标成与父键同档，而不是留空：空字符串会让调用方无法区分
	// "这字段无档位概念"与"后端忘了填"，而两者在安全语义上是有差别的。
	for i := range meta.Fields {
		if meta.Fields[i].Tier == "" {
			meta.Fields[i].Tier = meta.Tier
		}
	}
	if meta.Key == "" {
		meta.Key = key
	}
	return meta, true
}

// ManagedKeyCatalog 返回全部键元数据，按分组再按键名排序。
//
// 排序固定，前端可直接渲染分组表单，不需要自己再排。
func ManagedKeyCatalog() []ManagedKeyMeta {
	out := make([]ManagedKeyMeta, 0, len(managedKeyCatalog))
	for key := range managedKeyCatalog {
		meta, _ := ManagedKeyMetaOf(key)
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// floatPtr 是数值边界的构造辅助。
func floatPtr(v float64) *float64 { return &v }

// managedKeyGroupLabels 是分组的中文名（前端直接展示，不再维护第二份翻译）。
//
// 放在服务端而不是前端 i18n：分组集合由后端定义，前端维护一份映射必然在
// "后端加了新分组"时漂移，而漂移的表现是界面出现一个没有标题的空组——很难归因。
var managedKeyGroupLabels = map[ManagedKeyGroup]string{
	ManagedGroupCollect:  "采集节拍",
	ManagedGroupHistory:  "历史落盘",
	ManagedGroupWorld:    "世界采集",
	ManagedGroupFolia:    "Folia",
	ManagedGroupForensic: "网络取证",
	ManagedGroupHTTP:     "外呼监控",
	ManagedGroupCPU:      "CPU 归因",
	ManagedGroupIncision: "Incision",
	ManagedGroupAlert:    "告警",
	ManagedGroupMCP:      "MCP 任务与工件",
	ManagedGroupManaged:  "托管上行节拍",
	ManagedGroupLocal:    "仅本地可改（禁止远程写入）",
}

// ManagedKeyGroupLabels 返回分组 → 中文名，供前端渲染分组标题。
func ManagedKeyGroupLabels() map[ManagedKeyGroup]string {
	out := make(map[ManagedKeyGroup]string, len(managedKeyGroupLabels))
	for k, v := range managedKeyGroupLabels {
		out[k] = v
	}
	return out
}
