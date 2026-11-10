// Package config 提供 Worker Node 的配置加载（worker.yml + 环境变量覆盖）。
//
// 配置真正落盘到 worker.yml 而非堆砌 JIANMANAGER_* 环境变量；
// 所有项有合理默认，零配置即可启动开发环境。环境变量以 JIANMANAGER_ 前缀按路径覆盖
// （如 Control Plane gRPC 地址 → JIANMANAGER_CONTROL_PLANE_GRPC），与 Control Plane 配置惯例一致。
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/internal/platform/rollog"
	"github.com/wcpe/JianManager/internal/platform/vlruntime"
	"github.com/wcpe/JianManager/internal/worker/hostobs"
	"github.com/wcpe/JianManager/internal/worker/logs/logconf"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	"github.com/wcpe/JianManager/internal/worker/logs/tailer"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
	"github.com/wcpe/JianManager/internal/worker/process"
)

// Config Worker Node 配置。
type Config struct {
	Name string `mapstructure:"name"`
	// configPath 是本次**真的读到并应用**的配置文件路径（空串 = 未读到任何配置文件，
	// 全部取值来自内置默认与 JIANMANAGER_ 环境变量；包含「显式指定了路径但文件不存在」这一形态）。
	//
	// 为什么要记录并暴露它（2026-10-02 真机复验）：现场「改了 worker.yml 重启却没生效」的第一嫌疑
	// 永远是「进程读的不是你改的那个文件」（cwd / exe 旁 / configs 三处都在搜索路径里，服务形态下
	// cwd 还可能是 System32）。启动日志里打出实际读到的路径与生效的采集口径，一眼可判。
	configPath   string
	ControlPlane string   `mapstructure:"control_plane"`
	NodeSecret   string   `mapstructure:"node_secret"`
	WS           WSConfig `mapstructure:"ws"`
	// DataDir 是项目自包含数据根（默认 ./data，可经 JIANMANAGER_DATA_DIR 覆盖）。
	// JDK、服务器工作目录等运行态数据统一收口到此根。
	DataDir string `mapstructure:"data_dir"`
	// ServersDir 兼容旧配置：显式指定时覆盖数据根派生的 var/servers。
	// 留空则由数据根派生为 <DataDir>/var/servers。
	ServersDir string `mapstructure:"servers_dir"`
	// Host 注册上报给 CP 的本机地址；留空则自动探测出口 IP（供 CP 反向连接）。
	Host string `mapstructure:"host"`
	// JWTSecret 历史兼容字段；WS 令牌密钥只接受 CP 注册/心跳下发值。
	JWTSecret string `mapstructure:"jwt_secret"`
	// EnrollToken 一次性 enrollment token 明文。
	// 仅经环境变量/命令行传入、绝不写入 worker.yml（一次性凭据不留盘）；
	// 首次注册（无本地身份文件）时携带，注册成功后即作废。
	EnrollToken string `mapstructure:"enroll_token"`
	// EnrollTokenFile 是一次性凭据文件路径。适用于 systemd credentials 和
	// Windows 服务；Worker 注册成功后删除文件，明文不进入 YAML/命令行/日志。
	EnrollTokenFile string            `mapstructure:"enroll_token_file"`
	Log             LogConfig         `mapstructure:"log"`
	LogQuery        LogQueryConfig    `mapstructure:"log_query"`
	LogVL           LogVLConfig       `mapstructure:"log_vl"`
	LogCapacity     LogCapacityConfig `mapstructure:"log_capacity"`
	// LogCatalog 日志分区 Catalog 的持久化面（真机事故 2026-11-05）。
	//
	// 背景：分区记录曾改为纯内存（机械盘不做落盘），前提是「记录可随时重建」。
	// 该前提已被 2026-11-02 的投递路径简化推翻（记录改为只在投递成功时补建）。
	// 不落盘的后果是：进程重启后该源**之前所有天**在查询侧变成「不存在」——
	// VL 里行还在，没人知道去查它。详见 catalog.BatchedStore 的注释。
	LogCatalog LogCatalogConfig  `mapstructure:"log_catalog"`
	LogSources []LogSourceConfig `mapstructure:"log_sources"`
	// LogReconcile 启动增量对账：按「源 × UTC 天」只补缺失天，对账不可信则回退整窗重发。
	LogArchive LogArchiveConfig `mapstructure:"log_archive"`
	// LogIngest 采集归一化的节点级默认（缺陷 C 时区配置面）：源未显式配置时生效。
	LogIngest LogIngestConfig `mapstructure:"log_ingest"`
	// LogTailer 是新采集器（tailer/logtail，对齐 Fluent Bit / Filebeat 三层模型）的配置面。
	//
	// **与旧采集链路互斥**：Enabled=true 时 Worker 不构造 ingest.Manager，改由
	// logtail.Runner 直接扫 glob 采文件。两套同时跑会把同一文件读两遍（重复采集），
	// 故装配层必须保证只开一套——这不是建议，是硬约束。
	LogTailer LogTailerConfig `mapstructure:"log_tailer"`
	// LogRetention 保留策略（按级别/来源设保留期）与执行器。
	LogRetention LogRetentionConfig `mapstructure:"log_retention"`
	Decompiler   DecompilerConfig   `mapstructure:"decompiler"`
	// Search 全文搜索索引配置。
	Search SearchConfig `mapstructure:"search"`
	// ArtifactCache 节点本地制品缓存配置：按 sha256 缓存下载过的核心 jar，建实例命中即秒拷。
	ArtifactCache ArtifactCacheConfig `mapstructure:"artifact_cache"`
	// Proxy 本节点出站代理配置：所有出站下载（自更新/JDK/CFR）
	// 经此代理。url 留空=直连（沿用环境变量代理）。各 Worker 在不同机器各配各的。
	Proxy     httpclient.Config `mapstructure:"proxy"`
	Heartbeat time.Duration     `mapstructure:"-"`
	// MemoryGuard 启动内存闸：可用内存不足以再塞下待启实例时拒绝启动，
	// 防止把节点内存跑满至失去响应。零值即启用 + 默认保留水位 max(512MB, 总内存 10%)。
	MemoryGuard MemoryGuardConfig `mapstructure:"memory_guard"`
	// BotWorker bot-worker 子进程容量与资源参数（压测编排）。
	BotWorker BotWorkerConfig `mapstructure:"bot_worker"`
	// OrphanScan 运行期周期孤儿扫描：把孤儿清理从「仅启动时」升级为「运行期持续兜底」。
	OrphanScan OrphanScanConfig `mapstructure:"orphan_scan"`
	// Recover 接管恢复参数（①）：Worker 重启后对存活 wrapper 的 reconnect 重试窗口。
	Recover RecoverConfig `mapstructure:"recover"`
	// HealthScan 运行期实例健康巡检与自愈：识别假死、受控自愈、崩溃熔断。
	HealthScan HealthScanConfig `mapstructure:"health_scan"`
	// Startup 启动期可观测性配置。
	Startup StartupConfig `mapstructure:"startup"`
	// HostObserve 主机级观测采集配置。
	HostObserve HostObserveConfig `mapstructure:"host_observe"`
}

// HostObserveConfig 主机级观测采集配置。
//
// 全部有安全默认：写错/缺省即用默认值，观测配置不该让 Worker 起不来。
type HostObserveConfig struct {
	// TopN 整机 top-N 进程数量（默认 5，上限 10）。
	TopN int `mapstructure:"top_n"`
	// TopInterval 整机进程扫描间隔（默认 10s，**下限 10s**）。
	// 下限是硬约束：扫描是 O(全部进程)，而心跳 30s 才消费一次，更密只是白烧 CPU。
	TopInterval string `mapstructure:"top_interval"`
	// Mounts 分挂载点白名单；空=自动发现物理分区（跳过 tmpfs/overlay/proc 等伪文件系统）。
	// 容器部署建议显式配置，避免看到宿主挂载。
	Mounts []string `mapstructure:"mounts"`
}

// StartupConfig 启动期可观测性配置。
//
// 只有一个旋钮：状态规模告警阈值。阶段计时与阶段日志**不提供开关**——
// 启动期每次进程生命周期只跑一遍，没有噪声成本，而现场出问题时最需要的是
// 「上一次启动的日志里本来就有数据」。
type StartupConfig struct {
	// StateWarnMB 启动期状态数据（索引库 + 事件段）超过该 MB 数时打 WARN。
	// 0 = 使用默认值；负数 = 关闭告警。
	StateWarnMB int `mapstructure:"state_warn_mb"`
}

// 默认阈值。取值依据：2026-09-28 事故的状态规模是 1.2GB（该规模下注册被拖到 8m39s），
// 而正常生产规模（PRD 记录 state.json 约 178KiB、索引数百 MB 量级）远低于此。
// 512MB 落在「明显异常但不至于每天误报」的区间——宁可偶尔多一条 WARN，也不要重演
// 「1.2GB 拖了 8 分钟而全程无提示」。
const defaultStartupStateWarnMB = 512

// StartupStateWarnMB 返回状态规模告警阈值（MB）。负数表示关闭。
func (c *Config) StartupStateWarnMB() int {
	if c == nil {
		return defaultStartupStateWarnMB
	}
	if c.Startup.StateWarnMB < 0 {
		return 0 // 关闭
	}
	if c.Startup.StateWarnMB == 0 {
		return defaultStartupStateWarnMB
	}
	return c.Startup.StateWarnMB
}

// HealthScanConfig 运行期实例健康巡检与自愈配置。
//
// 本地基线（worker.yml）提供逃生口与默认口径；运行期策略（阈值/动作/探针类型）可经 CP
// 心跳响应下发覆盖（见 internal/worker/heartbeat 的 health policy 应用）。
type HealthScanConfig struct {
	// Disabled 显式关闭周期巡检（默认 false=启用）。应急逃生口：关闭后巡检不产生任何动作（spec §4 验收 9）。
	Disabled bool `mapstructure:"disabled"`
	// Interval 巡检周期（time.ParseDuration 字符串，默认 30s）。
	Interval string `mapstructure:"interval"`
	// ProbeKind 响应维度探针类型：tcp（连服务端口）/ http（GET 探针 /metrics）；空=auto。
	ProbeKind string `mapstructure:"probe_kind"`
	// SuspicionThreshold 连续判假死次数阈值（默认 3，越抖越保守）。
	SuspicionThreshold int `mapstructure:"suspicion_threshold"`
	// Action 假死动作：warn（默认，仅告警 + 审计 + 标原因）/ restart（优雅重启自愈）。
	Action string `mapstructure:"action"`
	// CircuitBreakerThreshold 滚动窗口内崩溃重启次数阈值（默认 5）。
	CircuitBreakerThreshold int `mapstructure:"circuit_breaker_threshold"`
	// CircuitBreakerWindow 崩溃熔断滚动窗口（time.ParseDuration 字符串，默认 10m）。
	CircuitBreakerWindow string `mapstructure:"circuit_breaker_window"`
	// StartupWarmup 启动宽限期（time.ParseDuration 字符串，默认 5m）：Start 返回 RUNNING 后
	// 这段时间内不做假死判定，避免慢启动 MC 被误判；"-1s"（负值）=关闭宽限。
	StartupWarmup string `mapstructure:"startup_warmup"`
	// SelfHealMaxRestarts 一熔断窗口内假死自愈重启次数上限（默认 3；0=用默认）。
	SelfHealMaxRestarts int `mapstructure:"self_heal_max_restarts"`
}

// ScanInterval 解析巡检周期：非法/空回退 30s（与 spec §2.1 默认一致）。
func (c HealthScanConfig) ScanInterval() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.Interval))
	if err != nil || d <= 0 {
		return process.DefaultHealthScanInterval
	}
	return d
}

// CircuitWindow 解析熔断滚动窗口：非法/空回退 10m。
func (c HealthScanConfig) CircuitWindow() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.CircuitBreakerWindow))
	if err != nil || d <= 0 {
		return process.DefaultCircuitBreakerWindow
	}
	return d
}

// StartupWarmupDuration 解析启动宽限期：非法/空回退 5m；显式 "-1s"（负值）=关闭宽限（测试/特殊场景）。
func (c HealthScanConfig) StartupWarmupDuration() time.Duration {
	raw := strings.TrimSpace(c.StartupWarmup)
	if raw == "" {
		return process.DefaultStartupWarmup
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return process.DefaultStartupWarmup
	}
	if d < 0 {
		return -1 // 显式关闭（归一化层把负值原样保留为「关闭」语义）
	}
	if d == 0 {
		return process.DefaultStartupWarmup
	}
	return d
}

// 默认值单一真源说明：巡检周期/熔断窗口/启动宽限的默认口径一律取自 process 包导出的
// Default* 常量（见 internal/worker/process/health_scan.go），本地不再重复定义字面量，
// 避免「本地默认」与「进程归一默认」两处漂移（复审项 12）。

// HealthPolicy 把本地配置组装为进程包可用的巡检策略（阈值/窗口未配时交给进程侧归一取默认）。
func (c HealthScanConfig) HealthPolicy() process.HealthPolicy {
	return process.HealthPolicy{
		Enabled:                 !c.Disabled,
		ScanInterval:            c.ScanInterval(),
		ProbeKind:               c.ProbeKind,
		SuspicionThreshold:      c.SuspicionThreshold,
		Action:                  c.Action,
		CircuitBreakerThreshold: c.CircuitBreakerThreshold,
		CircuitBreakerWindow:    c.CircuitWindow(),
		StartupWarmup:           c.StartupWarmupDuration(),
		SelfHealMaxRestarts:     c.SelfHealMaxRestarts,
	}
}

// OrphanScanConfig 运行期周期孤儿扫描配置。
type OrphanScanConfig struct {
	// Disabled 显式关闭周期扫描（默认 false=启用）。应急逃生口。
	Disabled bool `mapstructure:"disabled"`
	// Interval 扫描周期（time.ParseDuration 字符串，默认 60s）。
	Interval string `mapstructure:"interval"`
	// DisposePolicy 处置策略：warn（默认，只告警 + 落审计）/ auto（自动清理）。
	DisposePolicy string `mapstructure:"dispose_policy"`
	// AutoAdopt 未纳管活进程全自动收养（③，默认 true=启用）。
	//
	// 扫描发现「PID 目录中 wrapper 与 Java 均活、但本 Worker 未纳管」的实例时，经归属复核
	// （verifyProcessOwnership）通过即自动重连并登记为 RUNNING——**不重启也不杀进程**；复核不通过
	// 只告警。显式 false 只关收养，其余三态孤儿扫描与告警不变；见
	// docs/specs/auto-adopt-orphans/spec.md。默认口径的单一真源为 process.DefaultOrphanAutoAdopt。
	//
	// 关闭时**同时关闭对这些进程对的处置**：未纳管即不在受管集合中，它们本会被运行期扫描的
	// direct 相判为无主进程；`dispose_policy=auto` 若照旧 killTree，等于用处置策略绕过运维
	// 「这些进程我来管」的明示意图。故关闭时该类在 auto 档降级为 warn（WARN + 审计留痕）。
	AutoAdopt bool `mapstructure:"auto_adopt"`
}

// ScanInterval 解析扫描周期：非法/空回退 60s。
func (c OrphanScanConfig) ScanInterval() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.Interval))
	if err != nil || d <= 0 {
		return 60 * time.Second
	}
	return d
}

// AutoAdoptEnabled 返回未纳管活进程自动收养开关（③）。
//
// 与 RetryBackoffSequence 同口径：本类型的零值（测试/手工构造）按默认口径解释——config.Load 经
// viper SetDefault 写入 process.DefaultOrphanAutoAdopt，故只有显式配置 false 才会关闭。
// 注意：本类型的零值 AutoAdopt=false 无法与「显式关闭」区分，故装配点须用本方法而非直接读字段。
func (c OrphanScanConfig) AutoAdoptEnabled() bool {
	return c.AutoAdopt
}

// LogIndexReclaimRollConfig 已于 2026-10-29 删除（随 reclaim 改按投递证据推进）。

// LogCatalogConfig 是分区 Catalog journal 的持久化配置面（键 log_catalog.*）。
//
// 与 log_index.persist.manifest_sync_* 同一套取舍口径：同步频率越低，机械盘寻道越少，
// 崩溃时可能丢的记录窗口就越长；0 表示“不设该维度上界”。
type LogCatalogConfig struct {
	// JournalEnabled 控制 journal 是否落盘（键 log_catalog.journal_enabled，默认 true）。
	//
	// 默认**开**的理由：分区记录是查询侧判断「那天有没有数据」的唯一事实，
	// 不落盘就会在每次重启后丢失全部历史天。它不是可选优化，是正确性依赖。
	// 保留关闭开关供极端场景（如只读盘/排障）使用，关闭时退化为修复前的纯内存行为。
	JournalEnabled *bool `mapstructure:"journal_enabled"`
	// JournalSyncInterval 是攒批落盘的**时间上界**（duration 字符串；键
	// log_catalog.journal_sync_interval）。空/未设 = 用 catalog 默认（2s）；
	// 显式 `0s` = 同步写（每次追加立即 fsync，等价于修复前行为，最耗 IO）。
	JournalSyncInterval string `mapstructure:"journal_sync_interval"`
	// JournalMaxBatch 是攒批落盘的**条数上界**（键 log_catalog.journal_max_batch）。
	// 待落盘条目达到该数就当场 flush，给内存队列一个硬上界。0/未设 = 用默认 256。
	JournalMaxBatch int `mapstructure:"journal_max_batch"`
	// BackfillDays 是启动期「从 VL 反推天集合补建分区记录」的回溯窗口（天）。
	//
	// 键 log_catalog.backfill_days。0/未设 = 用 ingest 默认（30 天）；显式负值 = **关闭**该补建。
	//
	// 为何存在：journal 落盘只保证「从现在起不再丢」，它修不了已经掉的天。
	// 唯一知道「那天有数据」的地方是 VL 自己，所以启动期向 VL 问一次天集合，
	// 把「VL 有数据但 Catalog 没记录」的天补回来（详见 ingest/catalog_backfill.go）。
	//
	// 为何有上界：VL 单查询跨度上限是 31d（`-search.maxQueryTimeRange`），
	// 且无界回溯会产生大量查询与记录。30 天是可查范围内最宽的一档。
	BackfillDays int `mapstructure:"backfill_days"`
}

// backfillDisabledSentinel 是「显式关闭补建」的配置值。
//
// 用负值而不是 0 表示关闭：0 需要保留给「未设 ⇒ 用默认」，否则运维无法区分
// 「我没配」与「我要关掉」。这与 `journal_sync_interval` 用 bool 区分
// 「未设」与「显式 0s」是同一个理由（见 JournalSyncIntervalValue）。
const backfillDisabledSentinel = -1

// BackfillDisabled 报告是否显式关闭了启动期分区补建。
func (c LogCatalogConfig) BackfillDisabled() bool {
	return c.BackfillDays <= backfillDisabledSentinel
}

// BackfillDaysValue 把配置收敛为「回搜多少天」。
//
// 返回 0 表示「用 ingest 默认值」（未设）。负值不应到达这里（调用方先查
// BackfillDisabled）；若到了也按 0 处理，绝不能变成「全量历史」。
func (c LogCatalogConfig) BackfillDaysValue() int {
	if c.BackfillDays < 0 {
		return 0
	}
	return c.BackfillDays
}

// JournalPath 返回 journal 文件路径（位于数据根下 var/log）。
func (c LogCatalogConfig) JournalPath(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, "var", "log", "catalog.journal.jsonl")
}

// JournalDisabled 返回是否显式关闭了 journal 落盘（未设 = 不关闭）。
func (c LogCatalogConfig) JournalDisabled() bool {
	return c.JournalEnabled != nil && !*c.JournalEnabled
}

// JournalSyncIntervalValue 收敛为 time.Duration。
//
// 三种取值需区分清楚（都靠“未设 vs 显式 0”区分，不能用零值主义）：
//
//	未设（空串）  ⇒ (0, false)：调用方用 catalog 默认（2s）
//	显式 "0s"     ⇒ (0, true) ：同步写（每次追加即 fsync）
//	显式非法值     ⇒ (0, false)：回退默认（与既有 manifest_sync_interval 同口径）
func (c LogCatalogConfig) JournalSyncIntervalValue() (time.Duration, bool) {
	raw := strings.TrimSpace(c.JournalSyncInterval)
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, false
	}
	return d, true
}

// RecoverConfig 接管恢复（Worker 重启后接管存活 wrapper）配置（①）。
type RecoverConfig struct {
	// RetryBackoff 接管 reconnect 有界重试的间隔序列（逗号分隔的 duration 字符串，
	// 默认 "1s,2s,4s,8s,16s,32s,64s"≈127s）。交接窗口的 socket 未就绪/资源紧张常是瞬时的，
	// 多等一轮即可接管；序列越短越早进入处置判定，误杀面越大（测试用小值）。
	// 留空/全部非法回退默认序列。
	RetryBackoff string `mapstructure:"retry_backoff"`
}

// RetryBackoffSequence 解析重试间隔序列：留空/全部非法回退 process.DefaultRecoverRetryBackoff
// （默认口径的单一真源在进程包，本地不重复定义字面量）；逐项过滤非正项。
func (c RecoverConfig) RetryBackoffSequence() []time.Duration {
	raw := strings.TrimSpace(c.RetryBackoff)
	if raw == "" {
		return process.DefaultRecoverRetryBackoff
	}
	seq := make([]time.Duration, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		d, err := time.ParseDuration(strings.TrimSpace(part))
		if err != nil || d <= 0 {
			continue
		}
		seq = append(seq, d)
	}
	if len(seq) == 0 {
		return process.DefaultRecoverRetryBackoff
	}
	return seq
}

// BotWorkerConfig bot-worker 子进程调参；零值即用内置默认（总容量 50、单进程）。
type BotWorkerConfig struct {
	// MaxBots 本节点可创建的 Bot 总上限（多分片时为各片之和）；0 = 默认 50。
	// 大压测（数百 Bot）需调高，同时关注各片 eventLoopP95Ms 与 RSS。
	MaxBots int `mapstructure:"max_bots"`
	// Shards bot-worker 子进程分片数；0/1 = 单进程（与旧版行为一致）。
	//
	// 为什么需要分片：单 Node 进程承载 mineflayer bot 有硬上限——实测 272 bot 时
	// 主线程持续 99.9% CPU、RSS 9.2GB，事件循环排不上 10s 心跳定时器，stdout 停止输出，
	// 控制面据此判定容量快照过期（CAPACITY_SNAPSHOT_STALE）并使可用容量归零，
	// 新 bot 再也起不来。按 ~150 bot/片拆分可让每片的心跳保持可调度。
	Shards int `mapstructure:"shards"`
}

// Normalize 归一化分片配置：Shards<=0 视为 1（单进程），MaxBots<=0 用默认 50。
func (c BotWorkerConfig) Normalize() BotWorkerConfig {
	out := c
	if out.Shards <= 0 {
		out.Shards = 1
	}
	if out.MaxBots <= 0 {
		out.MaxBots = 50
	}
	return out
}

// PerShardMaxBots 返回每片的容量上限（总容量按片数向上取整均分）。
func (c BotWorkerConfig) PerShardMaxBots() int {
	n := c.Normalize()
	return (n.MaxBots + n.Shards - 1) / n.Shards
}

// ApplyEnv 把总容量落到子进程环境变量（单进程场景；多分片请用 ApplyEnvForShard）。
func (c BotWorkerConfig) ApplyEnv() []string {
	if c.MaxBots <= 0 {
		return nil
	}
	return []string{fmt.Sprintf("JM_BOT_WORKER_MAX_BOTS=%d", c.MaxBots)}
}

// ApplyEnvForShard 把「单片容量」落到子进程环境变量（供 bot-worker 读取）。
//
// 多分片时每个子进程只应知道自己那一片的上限，否则各片都按总容量准入，
// 合起来会超出节点可承载量。perShardMax<=0 时回退到总容量（保持旧行为）。
func (c BotWorkerConfig) ApplyEnvForShard(perShardMax int) []string {
	if perShardMax > 0 {
		return []string{fmt.Sprintf("JM_BOT_WORKER_MAX_BOTS=%d", perShardMax)}
	}
	return c.ApplyEnv()
}

// MemoryGuardConfig 启动内存闸配置。
type MemoryGuardConfig struct {
	// ReserveMB 保留水位（MB）；0 = 默认策略 max(512MB, 总内存 10%)。
	ReserveMB int64 `mapstructure:"reserve_mb"`
	// Disabled 显式关闭守卫（应急逃生口，如误判阻塞关键启动时）。
	Disabled bool `mapstructure:"disabled"`
}

// SearchConfig 全文搜索索引配置。
type SearchConfig struct {
	// Ignore 追加到内置默认忽略集的 glob 规则（相对实例工作目录，/ 分隔）。
	// 形如 logs/（目录前缀）、*.bak（basename glob）、vendor（路径段）。默认集已覆盖常见
	// 日志/缓存/二进制/归档/MC 世界数据，零配置即可用；此处仅做加性补充。
	Ignore []string `mapstructure:"ignore"`
}

// ArtifactCacheConfig 节点本地制品缓存配置。
type ArtifactCacheConfig struct {
	// MaxBytes 缓存容量上限（字节，0=不限）。存入新项后若超限按 lastUsedAt 升序 LRU 淘汰。
	// 可经 CP 端点 PUT /nodes/:id/artifact-cache/cap 运行时下发覆盖。
	MaxBytes int64 `mapstructure:"max_bytes"`
}

// DecompilerConfig 反编译能力配置。
type DecompilerConfig struct {
	// CFRPath 显式 CFR 反编译器 jar 路径（最高优先级，可空）。
	// 运维离线放置时直接指定；空则回退内嵌/数据根缓存/按需下载。
	CFRPath string `mapstructure:"cfr_path"`
	// AllowDownload 是否允许从 Maven Central 按需下载 CFR jar（sha256 pin 校验后落数据根缓存）。
	// 默认开启；离线环境可关并用 CFRPath/内嵌。
	AllowDownload bool `mapstructure:"allow_download"`
}

// WSConfig WebSocket 服务器配置。
type WSConfig struct {
	Port int `mapstructure:"port"`
}

// LogConfig 日志配置。
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
	// MutexProfileFraction 互斥锁争用采样率（诊断用）。0 = 关闭（Go 默认，也是本项默认）。
	// 此前该旋钮只认环境变量 JM_MUTEX_PROFILE_FRACTION（绕过配置体系 ⇒ 运维无法从配置面发现
	// 它、也无法纳入版本化管理）。1 = 全采（诊断窗口），数值越大采样越稀（5 = 1/5）。
	MutexProfileFraction int `mapstructure:"mutex_profile_fraction"`

	// 以下四项只看「进程自身日志文件」的轮转与保留（internal/platform/rollog），
	// 与实例日志 / VictoriaLogs 无关。零值一律回退内置默认，老配置不需要改。
	//
	// MaxSizeMB 单个日志文件触发轮转的大小（MB）。
	MaxSizeMB int `mapstructure:"max_size_mb"`
	// MaxBackups 轮转档保留份数（含未压缩与已压缩）。负值 = 不限。
	MaxBackups int `mapstructure:"max_backups"`
	// MaxAgeDays 轮转档保留天数。负值 = 关闭按天保留。
	MaxAgeDays int `mapstructure:"max_age_days"`
	// Compress 是否对轮转档做 gzip。默认 true；显式写 false 才关（用指针区分未配置）。
	Compress *bool `mapstructure:"compress"`
}

// Retention 把 YAML 里的日志保留旋钮转成 rollog 的保留策略。
//
// 零值语义（与 rollog 一致）：尺寸/份数/天数零值 → 回退内置默认；天数负值 → 关闭按天保留；
// 份数负值 → 不限；Compress 未配置（nil）→ 开。
func (c LogConfig) Retention() rollog.Retention {
	r := rollog.DefaultRetention()
	if c.MaxSizeMB > 0 {
		r.MaxSizeBytes = int64(c.MaxSizeMB) << 20
	}
	if c.MaxBackups != 0 {
		r.MaxBackups = c.MaxBackups
	}
	if c.MaxAgeDays != 0 {
		r.MaxAgeDays = c.MaxAgeDays
	}
	if c.Compress != nil {
		r.Compress = *c.Compress
	}
	return r
}

// LogQueryConfig configures the optional localhost VictoriaLogs query client.
// Empty VLURL deliberately keeps the explicit LOG_UNSUPPORTED compatibility path.
type LogQueryConfig struct {
	VLURL    string `mapstructure:"vl_url"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

// LogVLConfig 配置受管 HOT/COLD/Rehydrate；未指定资产时兼容外部回环 VL。
// cache、Go 软限制与 RSS 是不同维度，不能互相充当硬内存上限。
type LogVLConfig struct {
	BinaryPath      string `mapstructure:"binary_path"`
	PackagePath     string `mapstructure:"package_path"`
	AssetSHA256     string `mapstructure:"asset_sha256"`
	Username        string `mapstructure:"username"`
	Password        string `mapstructure:"password"`
	DataRoot        string `mapstructure:"data_root"`
	RetentionPeriod string `mapstructure:"retention_period"`
	// 2026-10-29：`cold_retention_period` 已删除 —— 冷热双层已废除，只剩余一个 VL，
	// `retention_period` 就是唯一的保留期（既管热也管长期），不再存在「冷层留存多久」。
	// DeleteEnabled 打开受管 VL 的 -delete.enable（默认 false）。
	//
	// 为什么要这个开关：`projection_generation` 白名单只让旧代次「看不见」，
	// **不会让旧行不存在**；VL 只追加。缺删除能力时，任何「整窗重写 + 换代次」
	// 都会线性放大体积（生产实测单日 75×、880MB）。打开后可由孤儿代次 GC 回收。
	// 默认关：删除不可逆，必须运维显式承担。
	DeleteEnabled bool `mapstructure:"delete_enabled"`
	// MaxDiskSpaceBytes / MaxDiskUsagePercent 是受管 VL 的**磁盘占用上限**（二者互斥）。
	//
	// 为什么需要它（2026-10-29）：只有 `retention_period` 时，磁盘用量 = 保留期 × 实际写入量，
	// 没有任何上限——单个源日志暴增就会把磁盘拖涨。VL 提供两种上限：
	//   - `max_disk_space_bytes`：`-retention.maxDiskSpaceUsageBytes`，按绝对字节数；
	//   - `max_disk_usage_percent`：`-retention.maxDiskUsagePercent`，按磁盘使用百分比（1-100）。
	//
	// **推荐用字节数**（本项目生产是 VL 与游戏服**共享磁盘**，`/home` 已 71%）：
	// 百分比口径在共享盘上会被**其它进程**的写入触发，那会把日志丢掉而非删别的，风险更大。
	//
	// 注意 VL 的语义：超过上限时**丢最旧的按日分区**，不是按字节精确切；
	// 它与 `retention_period` **同时生效**（谁先到谁触发），不是替代关系。
	MaxDiskSpaceBytes   int64 `mapstructure:"max_disk_space_bytes"`
	MaxDiskUsagePercent int   `mapstructure:"max_disk_usage_percent"`
	HotCacheBytes       int64 `mapstructure:"hot_cache_bytes"`
	// MemoryLimitBytes 受管 VL 进程 Go 软内存上限（GOMEMLIMIT，字节）；0 用默认 512MiB，负值不注入。
	MemoryLimitBytes int64 `mapstructure:"memory_limit_bytes"`
	// MemoryAllowedPercent 非 0 时用 VL 官方推荐的 -memory.allowedPercent，
	// 让 VL 按宿主机可用内存自适应；显式 hot_cache_bytes 仍优先。
	// 默认 10（256GiB 机器约 25GiB 上限，实际用量远低）。
	MemoryAllowedPercent int `mapstructure:"memory_allowed_percent"`
	// NamespaceBudgets 显式按层覆盖预算；零字段沿用旧键和各层有限模板。
	NamespaceBudgets map[string]LogVLNamespaceBudget `mapstructure:"namespace_budgets"`
	// Budget 区分全局日志磁盘与数据面 RSS，并限制临时空间预留。
	Budget  LogVLRuntimeBudget `mapstructure:"budget"`
	HotPort int                `mapstructure:"hot_port"`
	// StreamFields 是 `/insert/jsonline` 的 `_stream_fields`（逗号分隔）；空 = 默认 `log_source_id,stream`。
	//
	// 默认开流标签（与 VL 官方接入方式对齐）：流是**分区键**，把逐行变化的字段放进去
	// 会让 VL 为每行建一个 stream。取值默认由架构裁定，这里只作为**诊断/应急**旋钮，
	// 且构造客户端时会校验——无界基数字段（event_id / record_* / ingest_time_utc …）
	// 一律报错拒启，不会静默生效。
	StreamFields string `mapstructure:"stream_fields"`
	// DisableStreamFields 关闭流标签（不发 `_stream_fields` 等写入参数）。
	//
	// 供「外部回环 VL 由其他系统一并写入」或需要与既有 stream 布局对齐的部署退回：
	// 一旦启用流标签，同一个 VL 里新旧数据的分区形态就不一致。关掉后与本改动前的
	// 线上请求字节级一致（连 `_msg_field` / `_time_field` 也不发）。
	DisableStreamFields bool `mapstructure:"disable_stream_fields"`
}

// LogVLNamespaceBudget 表达单层 cache、Go 软限制与 RSS 准入阈值。
// RSS 为零不设置该层上界；Go 软限制的负值兼容旧键，表示不注入 GOMEMLIMIT。
type LogVLNamespaceBudget struct {
	CacheBytes         int64 `mapstructure:"cache_bytes"`
	GoMemoryLimitBytes int64 `mapstructure:"go_memory_limit_bytes"`
	RSSBytes           int64 `mapstructure:"rss_bytes"`
}

// LogVLRuntimeBudget 分开约束日志磁盘占用、聚合 RSS 与三类临时预留。
// 全局零值表示不配置该维度的上界，不会从 cache 或另一维度猜测。
type LogVLRuntimeBudget struct {
	DiskBytes    int64                   `mapstructure:"disk_bytes"`
	RSSBytes     int64                   `mapstructure:"rss_bytes"`
	Reservations LogVLReservationBudgets `mapstructure:"reservations"`
}

// LogVLReservationBudgets 是 WAL、迁移暂存和临时导出的独立预留上限。
type LogVLReservationBudgets struct {
	WAL     int64 `mapstructure:"wal"`
	Staging int64 `mapstructure:"staging"`
	Export  int64 `mapstructure:"export"`
}

// ValidateDiskLimit 校验受管 VL 的磁盘占用上限配置。
//
// 三条红线（均在**启动时**拒绝，不静默忽略）：
//  1. 负数：无意义，几乎必然是配置写错；
//  2. 两者同时非零：VL 官方明确二者**互斥**。2026-10-29 用真实 v1.52.0 实跑确认：
//     同配时 VL 自己直接 `fatal` 退出（不是告警、也不是选一个）。我们提前到配置层拦，
//     是为了给出中文原因与键名，而不是让运维去看 VL 的 fatal；
//  3. 百分比 > 100：超出 VL 接受范围（1-100）。
//
// 为什么特意不把“两个都配”降级成告警：这是那种“只在出事时才暴露”的配置——
// 磁盘满的那一刻，运维以为自己设了 N GiB，实际生效的是另一条，事故现场才发現。
func (c LogVLConfig) ValidateDiskLimit() error {
	if c.MaxDiskSpaceBytes < 0 {
		return fmt.Errorf("log_vl.max_disk_space_bytes 必须非负（0 = 不限制）")
	}
	if c.MaxDiskUsagePercent < 0 || c.MaxDiskUsagePercent > 100 {
		return fmt.Errorf("log_vl.max_disk_usage_percent 必须在 [0,100]（0 = 不限制）")
	}
	if c.MaxDiskSpaceBytes > 0 && c.MaxDiskUsagePercent > 0 {
		return fmt.Errorf("log_vl.max_disk_space_bytes 与 max_disk_usage_percent 互斥（VL 官方限制），只能配一个")
	}
	return nil
}

// ValidateStreamFields 校验流标签配置（启动即拒，不静默忽略）。
//
// 为什么要单独校验而不是只靠 vlsup.NewClient：这个错误的代价是**单向不可逆**的（写进 VL 的
// stream 元数据只能靠删分区回收），且生效点深在写入链路里、首次写入时才暴露。放到 Load
// 阶段拒，运维在启动日志里就能看到中文原因与具体字段名。
func (c LogVLConfig) ValidateStreamFields() error {
	if c.DisableStreamFields {
		return nil
	}
	spec := strings.TrimSpace(c.StreamFields)
	if spec == "" {
		spec = vlsup.DefaultStreamFields
	}
	return vlsup.ValidateStreamFields(spec)
}

// RuntimeBudget 将新键与旧内存键归并为运行时唯一预算，拒绝拼错层名和负上界。
func (c LogVLConfig) RuntimeBudget() (vlsup.RuntimeBudget, error) {
	namespaces := vlsup.DefaultNamespaceBudgets()
	if c.HotCacheBytes < 0 {
		return vlsup.RuntimeBudget{}, fmt.Errorf("log_vl.hot_cache_bytes 必须非负")
	}
	for ns, budget := range namespaces {
		if ns == vlsup.NamespaceHot && c.HotCacheBytes > 0 {
			budget.CacheBytes = c.HotCacheBytes
		}
		if c.MemoryLimitBytes != 0 {
			budget.GoMemoryLimitBytes = c.MemoryLimitBytes
		}
		namespaces[ns] = budget
	}
	for name, override := range c.NamespaceBudgets {
		ns := vlsup.Namespace(name)
		budget, ok := namespaces[ns]
		if !ok {
			return vlsup.RuntimeBudget{}, fmt.Errorf("log_vl.namespace_budgets 含未知层 %q", name)
		}
		if override.CacheBytes < 0 || override.RSSBytes < 0 {
			return vlsup.RuntimeBudget{}, fmt.Errorf("log_vl.namespace_budgets.%s 的 cache_bytes/rss_bytes 必须非负", name)
		}
		if override.CacheBytes != 0 {
			budget.CacheBytes = override.CacheBytes
		}
		if override.GoMemoryLimitBytes != 0 {
			budget.GoMemoryLimitBytes = override.GoMemoryLimitBytes
		}
		budget.RSSBytes = override.RSSBytes
		namespaces[ns] = budget
	}
	budget := vlsup.RuntimeBudget{
		Namespaces: namespaces, DiskBytes: c.Budget.DiskBytes, RSSBytes: c.Budget.RSSBytes,
		Reservations: map[vlsup.ReservationKind]int64{
			vlsup.ReservationWAL:     c.Budget.Reservations.WAL,
			vlsup.ReservationStaging: c.Budget.Reservations.Staging,
			vlsup.ReservationExport:  c.Budget.Reservations.Export,
		},
	}
	if _, err := vlsup.NewBudgetController(budget); err != nil {
		return vlsup.RuntimeBudget{}, fmt.Errorf("log_vl.budget: %w", err)
	}
	return budget, nil
}

// DefaultMaxWALBytes 是单源 WAL **当前积压**的字节上限默认值。
//
// 2026-10-29：语义已从「越界暂停采集」改为「越界丢弃最旧、不暂停」（见
// acquire.WAL.enforceBacklogLimitLocked）。因此本值现在是**丢多少的阈值**，
// 而不是“守多久”的阈值——取太大意味着卡住时丢得更多。
//
// 取 512MiB 的依据：稳态 WAL 在 reclaim 正常推进后是 KB 量级（真机实测 107MB → 256B），
// 只有 reclaim 停滞时才会累积到百 MB 级。故 512MiB 远高于任何正常态。
// 0 表示字节维度不设上限（此时条目维度仍由 MaxWALEntries 兜住）。
const DefaultMaxWALBytes uint64 = 512 << 20

// DefaultMaxWALEntries 是单源 WAL **当前积压**的条目数上限默认值。
//
// ## 2026-10-29 下调：20000 → 5000
//
// 两个原因，都是这次改造直接带来的：
//
//  1. **语义变了**：原来是“超了就停采”（保留全部积压、停止读新数据），现在是
//     “超了就丢弃”（不暂停、不背压）。上限不再代表“能守多久”，而是**丢多少**。
//     20000 条在生产上（单条约 900B 均值、60 源）意味着卡住时每个源先丢 20000 条，
//     而实际上更早丢就能更早腾出内存、更早恢复投递。
//  2. **内存上界**：WAL 改纯内存后，条目数直接决定进程内存。5000 条 × 约 1KB ≈ 5MB/源，
//     60 源约 300MB，对 251GB 机器可接受；20000 条则是 1.2GB，且会随源数线性放大。
//
// 为什么仍保留这个键：它是运维手上唯一的“积压容量旋钮”，完全去掉会让
// “这个节点到底能容多少未投递日志”变成不可配的魔法值。
// 字节维度同时生效、**先到者触发**。
const DefaultMaxWALEntries int64 = 5000

// DefaultMaxReplayEventsPerDrain 是暂停/恢复期单轮外发事件数的默认上限（回放限速）。
//
// 为什么需要：VL 变慢或刚恢复时 WAL 存量可能有数千条，一次全塞进去会把恢复瞬间变成
// 一次自我制造的流量尖峰。限速只把尖峰摊成若干轮（采集轮持续在跑），未发的条目留在
// WAL 与位置账本里，**不丢数据**。
const DefaultMaxReplayEventsPerDrain int = 500

type LogCapacityConfig struct {
	MaxWALBytes uint64 `mapstructure:"max_wal_bytes"`
	// RecoveryQuotaFactor / RecoveryDrainGrace：**恢复期独立配额**（方案②，2026-10-04 用户定调）。
	//
	// 恢复期的回放/补账是设计行为 ⇒ 必然把源推过常规闸 ⇒ 常规闸在恢复期注定自锁 ✗。
	// factor 默认 4（同倍拓宽，仍有界 ✓）；grace 默认 30m（现场排空动辄小时级 ⇒ 更短的窗有
	// "刚回归即再锁"风险 ✓）；宽限过期即回归常规闸 ✓（常规期语义不削弱 ✗ = 红线）。
	RecoveryQuotaFactor float64 `mapstructure:"recovery_quota_factor"`
	RecoveryDrainGrace  string  `mapstructure:"recovery_drain_grace"`
	MaxWALEntries       int64   `mapstructure:"max_wal_entries"`
	MaxReplayEvents     int     `mapstructure:"max_replay_events_per_drain"`
	MaxGaps             int     `mapstructure:"max_gaps"`
	DegradedAtPercent   float64 `mapstructure:"degraded_at_percent"`
	PauseAtPercent      float64 `mapstructure:"pause_at_percent"`
}

// LogSourceConfig 配置一个由 Worker 常驻采集的日志源。
//
// 实际用途只有一处（2026-10-19 更正，评审 m-1）：**给实例/节点日志源做路径覆盖**——
// apps/worker/main.go 按 `storage_namespace == targetID` 匹配，命中则以其 path 覆盖
// 采集规则推导出的默认路径（见 docs/specs/log-acquisition-rules/spec.md §3.4）。
// 早先这里写的是「配置源用于 Worker/Node 日志和离线部署」，与实现不符：并不存在
// 「Worker 自身日志走 log_sources」这条路径，`node:<id>` 指的是**节点维度目标**，
// 不是 Worker 进程自己的 stdout。validateLogSources 的白名单口径正建立在这个事实上。
type LogSourceConfig struct {
	LogSourceID      string `mapstructure:"log_source_id"`
	SourceGeneration string `mapstructure:"source_generation"`
	Path             string `mapstructure:"path"`
	Mode             string `mapstructure:"mode"`
	RotateTo         string `mapstructure:"rotate_to"`
	ArchiveGlob      string `mapstructure:"archive_glob"`
	Stream           string `mapstructure:"stream"`
	SourceCategory   string `mapstructure:"source_category"`
	StorageNamespace string `mapstructure:"storage_namespace"`
	UTCDay           string `mapstructure:"utc_day"`
}

// logSourceTargetRe 是可被查询枚举到的目标标识形态：`inst:<实例ID>` / `node:<节点ID>`。
//
// 与 retention.sourceIDRe 的字符集白名单互补：那条回答「能否安全用于删除过滤器」，
// 本正则回答「这个命名空间是否可能被 CP 枚举到」。
//
// 三种合法形态（与 CP 侧 logcoord/assemble.go 的目标解析一一对应）：
//   - `inst:<实例ID>`  实例日志（CP 按实例 ID 枚举）
//   - `node:<节点ID>`  节点自身日志（CP 按节点 ID 枚举）
//   - `node:<节点UUID>` 同上，但用 UUID 寻址
//
// 为什么必须支持 UUID（2026-10-19，评审 M-3）：节点数字 ID 由 CP 在**注册时**自增分配，
// 运维写 worker.yml 时无法预知；只允许 `node:<数字>` 等于要求"先注册、查 ID、再改配置重启"，
// 而且写错数字仍能通过校验、静默查不到。CP 的 assemble.go 对未知前缀走 default 分支
// 按 UUID 解析并归一成 `node:<id>`，因此用本节点 UUID 是**可发现、可验证**的写法：
// 它就在 `<dataDir>/etc/node-identity.json` 里。
var logSourceTargetRe = regexp.MustCompile(
	`^(inst:[1-9][0-9]*|node:[1-9][0-9]*|node:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

// validateLogSources 校验静态 `log_sources` 的目标形态，非法即拒绝启动。
//
// 为什么必须拒（2026-10-17 dev 实测）：CP 的日志查询按**目标**收敛覆盖率
// （`node:<节点ID>` / `inst:<实例ID>`），而 Worker 侧 coverage 只从自己持有的 Catalog
// 分区反推，匹配依据正是分区键的 `storage_namespace`。若配置里写了一个不会被任何 CP 目标
// 匹配到的命名空间，整条链路会**全程静默**——采集照常、VL 里有数据、日志页却显示
// 「日志查询服务未启用」（CP 收到空目标列表 → target_missing_in_worker_response →
// 归一化为 ENGINE_NOT_READY）。现场排查会一路去怀疑 VL，而真因只是命名空间写错。
//
// 形态由平台自己生成、可完全枚举，因此不预留宽松口子：`log_sources` 的
// `storage_namespace` 语义本就是「等于 targetID」（见 docs/specs/log-acquisition-rules/spec.md §3.4），
// 实例级条目必须是 `inst:<id>`；节点自身日志用 `node:<节点ID>` 或 `node:<节点UUID>`。
func validateLogSources(sources []LogSourceConfig) error {
	seen := make(map[string]struct{}, len(sources))
	for i, s := range sources {
		where := fmt.Sprintf("log_sources[%d]", i)
		id := strings.TrimSpace(s.LogSourceID)
		ns := strings.TrimSpace(s.StorageNamespace)
		if id == "" {
			return fmt.Errorf("%s.log_source_id 不能为空", where)
		}
		if ns == "" {
			return fmt.Errorf("%s.storage_namespace 不能为空：必须是 inst:<实例ID> 或 node:<节点ID|节点UUID>", where)
		}
		if !logSourceTargetRe.MatchString(ns) {
			return fmt.Errorf("%s.storage_namespace = %q 不是可被查询的目标形态：必须是 inst:<实例ID>、"+
				"node:<节点ID> 或 node:<节点UUID>（UUID 见 <dataDir>/etc/node-identity.json）。"+
				"写成其它值（如 dev:smoke）时该源仍会被采集并写入 VL，但 CP 按目标枚举永远匹配不到，"+
				"日志页会显示「日志查询服务未启用」而真因只是命名空间不对", where, s.StorageNamespace)
		}
		// 源标识与命名空间的关系按类别分流（2026-10-19 评审修正）：
		//   - instance 类：必须是 `<storage_namespace>/<流名>`（生产形态 `inst:317/file`），
		//     否则分区键与源身份脱节；
		//   - worker / node 类：允许 `id == storage_namespace`（即 `node:1` 自身），
		//     这是 configs/worker.yml 的出厂示例形态（`log_source_id: node:1` +
		//     `storage_namespace: node:1`）——此前一律强制 `<ns>/` 会把它判成非法，
		//     存量部署按示例配置升级后直接起不来。
		isInstanceCategory := s.SourceCategory == "" || s.SourceCategory == string(logtypes.SourceInstance)
		if isInstanceCategory {
			if !strings.HasPrefix(id, ns+"/") || len(id) == len(ns)+1 {
				return fmt.Errorf("%s.log_source_id = %q 必须以 %q 开头（形态 <storage_namespace>/<流名>，例如 %s/file）",
					where, s.LogSourceID, ns+"/", ns)
			}
		} else if id != ns && !strings.HasPrefix(id, ns+"/") {
			return fmt.Errorf("%s.log_source_id = %q 必须是 %q 本身或以 %q 开头（%s 类源的两种合法写法）",
				where, s.LogSourceID, ns, ns+"/", s.SourceCategory)
		}
		if cat := strings.TrimSpace(s.SourceCategory); cat != "" &&
			cat != string(logtypes.SourceInstance) && cat != string(logtypes.SourceWorker) && cat != string(logtypes.SourceNode) {
			return fmt.Errorf("%s.source_category = %q 非法：只接受 instance / worker / node（留空按 instance）", where, cat)
		}
		// 采集模式白名单校验（与 pipeline.AcquireMode 的合法值逐字一致：worker.yml 直接写
		// 字面量 FILE_PRIMARY / STDIO_PRIMARY / PUSH_ONLY，由 main.go 透传为 pipeline.AcquireMode）。
		// 空串沿用 FILE_PRIMARY（pipeline 默认值）。PUSH_ONLY 为纯接收源（的 HTTP 端点）：
		// 不读取任何物理文件，因此允许 path 为空——它走自己的 ledger+WAL，事件经 Ingest 注入。
		mode := strings.TrimSpace(s.Mode)
		if mode != "" && mode != "FILE_PRIMARY" && mode != "STDIO_PRIMARY" && mode != "PUSH_ONLY" {
			return fmt.Errorf("%s.mode = %q 非法：只接受 FILE_PRIMARY / STDIO_PRIMARY / PUSH_ONLY（留空按 FILE_PRIMARY）", where, s.Mode)
		}
		if mode == "FILE_PRIMARY" && strings.TrimSpace(s.Path) == "" {
			return fmt.Errorf("%s.mode = FILE_PRIMARY 必须有 path（采集文件的绝对路径）", where)
		}
		if mode == "PUSH_ONLY" && strings.TrimSpace(s.Path) != "" {
			return fmt.Errorf("%s.mode = PUSH_ONLY 为纯接收源，不应配置 path（事件经 HTTP 注入）", where)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("%s.log_source_id = %q 与前面的源重复：同一源不能登记两次", where, s.LogSourceID)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// LogIngestConfig 采集归一化的节点级默认配置面，键为 `log_ingest.*`。
//
// 缺陷 C（时间戳偏移）的配置面：`[HH:MM:SS]` 行内时间按哪个时区解释。换算逻辑早已在归一化层
// （normalize.applyClock 按 Location 解释并做跨午夜回拨），缺的是把节点默认时区接进来——
// 此前 Location 恒为 UTC，中文 locale 的 JVM 按本地时区写的日志会被整体偏移（现场 +8 小时）。
//
// 默认零行为变化：留空 = UTC。节点与 JVM 同机部署时配 `local` 即可对齐。
// 合法性在 Load 阶段校验（非法值启动即拒）——时区配错会让整源时间轴静默偏移，
// 不能让运维在查询结果里发现；与登记阶段拒绝非法源级时区是同一取舍。
// 见 docs/specs/worker-log-normalizer/spec.md §3.4。
type LogIngestConfig struct {
	// ReadLimits 在分配整行正文之前限制单行和单轮读入，零字段沿用采集模板。
	ReadLimits LogReadLimitsConfig `mapstructure:"read_limits"`
	// TimeZone 解释日志行内 [HH:MM:SS] 所用的节点级默认时区：空串 = UTC（既有行为）；
	// "local" = 跟随节点进程本地时区（TZ）；其余按 IANA 名解析（如 Asia/Hong_Kong）。
	// 源可用 SourceConfig.TimeZone 覆盖本默认。
	TimeZone string `mapstructure:"time_zone"`
	// Charset 是节点级默认日志字符集：空串 = auto（合法 UTF-8 原样返回，非法 UTF-8 才按
	// GB18030 解码并做「含 U+FFFD 即放弃」的可靠性判定，见 normalize 的字符集收口）；
	// "utf-8" / "gbk" / "gb18030" 则强制按该字符集解码。
	//
	// 为什么需要它（复审 P2-3）：字符集判定与转码逻辑早已在 normalize 落地，节点级默认也已有
	// ingest.Options.DefaultCharset 接线点，但**没有任何配置面**——生产要让「中文 locale 的
	// JVM 写出 GBK 日志」稳定按 GBK 解释，只能改代码。自动判定在纯 ASCII 行与中文行混排时
	// 依赖粘滞启发式，运维显式声明才是确定性方案。
	// 源可用 SourceConfig.Charset 覆盖本默认；非法值在 Load 阶段启动即拒。
	Charset string `mapstructure:"charset"`
}

// LogTailerConfig 是新采集器的配置面，键为 `log_tailer.*`。
//
// # 为什么是独立的一段而不是复用 log_ingest
//
// 两者的模型根本不同：旧链路「一个源 = 一个文件 = 四水位账本」，新采集器「每个文件平级、
// 只记 offset」。配置项也因此不同（旧的是源级预算，新的是 glob + 位点表）。
// 阶段三删除旧链路后，`log_ingest.*` 里属于旧链路的键会随之消失。
type LogTailerConfig struct {
	// Enabled 是新采集器总开关。**默认 true**（2026-11-06 切换）。
	//
	// # 为何可以默认开了
	//
	// 阶段二已用真机验证：多实例采集、重启续读、轮转跟随、gz 排除、stdio 桥接、
	// 查询可见性（Catalog 补建）均正常；并用变异测试钉住了四个丢数据形态
	// （忽略位点 / 去掉可采闸门 / 去掉前缀容忍 / 不注入分区补建）。
	//
	// # 回退开关
	//
	// 配 `log_tailer.enabled: false` 即退回旧 ingest 链路（两套互斥，不会同时跑）。
	// 旧链路代码在阶段三删除前一直保留，所以回退是**改一行配置**的事。
	Enabled bool `mapstructure:"enabled"`
	// Paths 是**额外的**采集 glob（通常留空）。
	//
	// 实例日志的采集路径由**已绑定实例的 WorkDir** 每轮现算
	// （`logtail.DefaultPathsFor`），不再靠一个 serversRoot 猜。
	// 这里只用于采非实例日志（节点自身日志等）。
	Paths []string `mapstructure:"paths"`
	// Exclude 是排除规则（文件名 glob）。空 = 用实现包的默认值
	// `*.gz,*.lck,*.zip,*.tmp`——gz 是用户明确要求不采的归档。
	Exclude []string `mapstructure:"exclude"`
	// ReadFromHead 决定首次发现的文件从哪里开始读。
	//
	// 默认 **true**：首次部署时要能补上部署前的历史日志（用户明确要这个）。
	// 想只采新行就配 false。
	ReadFromHead bool `mapstructure:"read_from_head"`
	// RegistryPath 是位点表路径。空 = `var/log/tailer.registry.jsonl`（相对数据根）。
	//
	// 位点表的兄弟文件 `<同名>.bindings.json` 存「实例工作目录 → 日志目标」映射，
	// 重启后据此继续定位源（否则要等 CP 重新下发实例登记，期间的日志会因
	// 「无法识别源」被跳过）。
	RegistryPath string `mapstructure:"registry_path"`
	// ScanInterval 是目录扫描周期（发现新文件）。空 = 实现包默认 10s。
	ScanInterval string `mapstructure:"scan_interval"`
	// ProgressInterval 是读文件周期。空 = 实现包默认 2s。
	ProgressInterval string `mapstructure:"progress_interval"`

	// Multiline 是多行合并规则（键 log_tailer.multiline.*），对齐 Filebeat 的
	// `parsers: - multiline:`。留空 = 不合并（每行一条事件）。
	Multiline MultilineConfig `mapstructure:"multiline"`

	// IncludeGzip 决定是否导入历史 `.gz` 归档（键 log_tailer.include_gzip）。
	//
	// # 与「不采 gz」不矛盾
	//
	// 用户明确要求**实时采集**不碰归档（那是重复：live 文件已经采过了）。
	// 但生产有 1374 个从未进过平台的 gz 归档，那些历史需要能补进来。
	// 两者是不同的事：live 文件持续追读，归档一次性读完。
	//
	// 默认 **false**：导入历史归档是「主动补数据」的动作，该由运维明确打开，
	// 而不是升级后突然开始读几千个文件、把 VL 打满。
	IncludeGzip bool `mapstructure:"include_gzip"`
}

// MultilineConfig 是多行合并的配置面（键 log_tailer.multiline.*）。
//
// # 字段与官方一一对应
//
// Filebeat filestream：
//
//	parsers:
//	- multiline:
//	    type: pattern        （本项目只实现 pattern 型）
//	    pattern: '^\['
//	    negate: true
//	    match: after
//	    flush_pattern: 'END$'
//	    max_lines: 500
//	    timeout: 5s
//
// # 为什么不做 XML/JSON 那种结构化合并
//
// 那些是「解析器」，不是「行组装」。本项目的需求是 Java 堆栈，pattern 型就够；
// 多实现一种就多一套要验证的语义（而配置写错的表现是「日志变乱」而非报错）。
// DefaultMultilinePattern 是**本项目默认启用**的多行合并规则。
//
// # 为什么默认开（与 Filebeat / Fluent Bit 不同，这是有意的）
//
// Filebeat 的 `multiline.*` 与 Fluent Bit 的 multiline filter **默认都是关闭的**
// （官方文档：必须显式配 pattern / multiline.parser 才生效）。理由是它们是通用
// 采集器，不知道你在采什么，默认不动数据是对的。
//
// 而本平台的采集对象是**确定的**：这台机 19 个实例全是 Minecraft（Paper/Spigot）
// 与 BungeeCord，Java 堆栈必然多行。默认关的后果是实测可见的——88673 行 Bungee
// 日志里 123 行是堆栈续行，全部被拆成独立事件，用户读崩溃堆栈时看到的是散行。
//
// 所以这里把「我们比通例更确知」这件事落成默认值，并用下面的 pattern 同时覆盖
// 两种并存的日志形态：
//
//	[01:19:43] [ServerMain/INFO]: ...   ← Paper/MC（时刻带方括号）
//	18:12:59 [INFO] ...                 ← BungeeCord（时刻不带方括号）
//
// negate=true 的语义：**不匹配**这个 pattern 的行 = 续行，并入上一行
// （`	at ...`、`Caused by:`、`... N more`、netty 异常行）。
//
// # 实测（2026-11-07，生产日志）
//
//	Bungee proxy.log.0：88673 行 → 88550 条事件，最大堆栈 42 行合并成 1 条
//	MC latest.log      ：44 行 → 44 条（该时段全是单行，无误合并）
//
// # 如何关掉
//
// 配 `log_tailer.multiline.pattern: ""`（显式空 = 关），或用 flush_pattern 等
// 覆盖成自己的规则。配置写错在启动即拒——见 MultilineSettings 的说明。
const DefaultMultilinePattern = `^\[?[0-9]{2}:[0-9]{2}:[0-9]{2}|^[0-9]{4}-[0-9]{2}-[0-9]{2}`

type MultilineConfig struct {
	// Pattern 是 `multiline.pattern`（RE2 语法，与官方一致：Go regexp）。
	//
	// 空值 = 用 DefaultMultilinePattern（**默认启用**，见上）。
	// 显式写成 "" 的语义与「没配」无法区分，故提供 Disabled 开关显式关闭。
	Pattern string `mapstructure:"pattern"`
	// Disabled 显式关闭多行合并（优先级高于 Pattern）。
	//
	// 为什么需要它而不是「Pattern 留空即关闭」：默认开启后，「留空」必须表示
	// 「用默认规则」，否则任何没写 multiline 段的部署都会静默变成不合并——
	// 那正是本次要修的默认值。要关的多行必须有一个不可与「没配」混淆的开关。
	Disabled bool `mapstructure:"disabled"`
	// Negate 对应 `multiline.negate`。默认规则用 true（不匹配=续行）；
	// 若 Pattern 为空则本字段的零值 false 会被忽略（见 MultilineSettings）。
	Negate bool `mapstructure:"negate"`
	// Match 对应 `multiline.match`：after（默认）或 before。
	Match string `mapstructure:"match"`
	// FlushPattern 对应 `multiline.flush_pattern`（可选）。
	FlushPattern string `mapstructure:"flush_pattern"`
	// MaxLines 对应 `multiline.max_lines`，零值用实现包默认 500。
	MaxLines int `mapstructure:"max_lines"`
	// Timeout 对应 `multiline.timeout`（如 "5s"），空 = 实现包默认 5s。
	Timeout string `mapstructure:"timeout"`
}

// MultilineSettings 把配置编译成实现包的规则。
//
// 返回 (nil, 0, nil) 表示未启用多行（Pattern 为空）。
// pattern/match/flush_pattern/timeout 非法一律返回错误——启动即拒。
//
// # 为何必须启动即拒而不是回退默认
//
// 多行规则写错的表现是「日志被错误地拼在一起」或「堆栈被拆散」，
// 都不是报错，而是**数据变脏**。那种错误只能靠人眼在查询结果里发现，
// 而发现时脏数据已经进库了。所以在启动时就把非法配置挡掉。
func (c *Config) MultilineSettings() (*tailer.Multiline, time.Duration, error) {
	if c == nil {
		return nil, 0, nil
	}
	mc := c.LogTailer.Multiline
	// 显式关闭优先：Disabled 是唯一能与「没配」区分的关闭方式。
	if mc.Disabled {
		return nil, 0, nil
	}
	// 未配 pattern = 用默认规则（**默认启用**，见 DefaultMultilinePattern）。
	// Negate 的零值 false 与默认规则需要的 true 冲突，故 pattern 为空时由本函数
	// 按默认规则的语义补齐，而不是直接用零值。
	pattern := strings.TrimSpace(mc.Pattern)
	negate := mc.Negate
	if pattern == "" {
		pattern = DefaultMultilinePattern
		negate = true
	}
	rule, err := tailer.NewMultiline(tailer.MultilineConfig{
		Pattern:      pattern,
		Negate:       negate,
		Match:        tailer.MultilineMatch(strings.ToLower(strings.TrimSpace(mc.Match))),
		FlushPattern: mc.FlushPattern,
		MaxLines:     mc.MaxLines,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("log_tailer.multiline: %w", err)
	}
	var timeout time.Duration
	if raw := strings.TrimSpace(mc.Timeout); raw != "" {
		d, perr := time.ParseDuration(raw)
		if perr != nil {
			return nil, 0, fmt.Errorf("log_tailer.multiline.timeout 非法: %q（应为时长如 5s）", raw)
		}
		if d <= 0 {
			return nil, 0, fmt.Errorf("log_tailer.multiline.timeout 必须为正: %q", raw)
		}
		timeout = d
	}
	return rule, timeout, nil
}

// LogTailerIntervals 解析 log_tailer.scan_interval / progress_interval。
//
// 空串表示用实现包默认值（10s / 2s），此时返回 0 让调用方走零值分支。
// 非法值返回 error（在 Load 阶段启动即拒）：采集周期配错会让「实时性」静默变形，
// 而这件事只有在「日志怎么半天不上来」时才会被发现，排查方向会完全带偏。
func (c *Config) LogTailerIntervals() (scan, progress time.Duration, err error) {
	if c == nil {
		return 0, 0, nil
	}
	parse := func(key, raw string) (time.Duration, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return 0, nil
		}
		d, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			return 0, fmt.Errorf("log_tailer.%s 非法: %q（应为时长如 2s/10s）", key, raw)
		}
		if d <= 0 {
			return 0, fmt.Errorf("log_tailer.%s 必须为正: %q", key, raw)
		}
		return d, nil
	}
	if scan, err = parse("scan_interval", c.LogTailer.ScanInterval); err != nil {
		return 0, 0, err
	}
	if progress, err = parse("progress_interval", c.LogTailer.ProgressInterval); err != nil {
		return 0, 0, err
	}
	return scan, progress, nil
}

// LogReadLimitsConfig 配置单物理行与单轮读取字节上界，包含换行字节。
type LogReadLimitsConfig struct {
	MaxLineBytes    int   `mapstructure:"max_line_bytes"`
	MaxBytesPerPoll int64 `mapstructure:"max_bytes_per_poll"`
}

// LogRetentionConfig 保留策略的配置面（键 `log_retention.*`）。
//
// 保留期写成**字符串**（`3d` / `7d` / `30d` / `90d`）：Go 的 time.ParseDuration 不认 d/w，
// 而保留期天然按天表达，故经 retention.ParseTTL 解析（见该函数的说明）。
//
// 两道闸分开的原因：删除**不可逆**，与采样折叠（有汇总事件承接区间，可查可证）不是
// 同一量级的风险。`enabled` 决定「保留多久」（策略），`sweep.vl_sweep` 决定「真的删」
// （执行），后者默认关；且 VL 侧还需运维在受管进程上加 `-delete.enable`（VL 自身默认关）。
type LogRetentionConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// ByLevel 级别 → 保留期字符串；"0" 或空串 = 该级别永久保留。
	// 未列出的级别按 D1 推荐值补齐（漏配一档不得退化成永久保留）。
	ByLevel map[string]string `mapstructure:"by_level"`
	// Sources 来源级覆盖。
	Sources []LogRetentionSource `mapstructure:"sources"`
	Sweep   LogRetentionSweep    `mapstructure:"sweep"`
	// Discard 是否允许**直接删除**到期数据（裸删）。默认 false。
	//
	// 这是**意图闸**，与 sweep.vl_sweep（执行闸）相互独立，两者同时成立才可达删除。
	// 默认动作是把日分区搬运到冷层；本项的作用是解除「没有归档路径时的阻塞」，
	// 即使显式打开，只要冷层路径可用，动作**仍然**是搬运（见 retention.PlanArchive 的次序）。
	Discard bool `mapstructure:"discard"`
	// HotRetention 热层保留窗口（超期即搬运到冷层）；空 = 用默认 90d。
	//
	// 为什么是**字符串**而不是 time.Duration：Go 的 time.ParseDuration 不认 `d`/`w`，
	// 而保留期天然按天写（用户口径就是「热 90 天 / 冷 730 天」）。同 by_level 的处理方式，
	// 统一经 retention.ParseTTL 解析。若用 time.Duration，viper 解码 `90d` 会直接失败——
	// 那会让**整个 Load 失败**，而不只是这一个键无效（曾经真的发生过）。
	HotRetention string `mapstructure:"hot_retention"`
	// ColdRetention 冷层保留期（所有级别统一）；空 = 用默认 730d。
	ColdRetention string `mapstructure:"cold_retention"`
	// Trigger 搬运触发口径（年龄 + 磁盘水位取先到）。
	Trigger LogRetentionTriggerConfig `mapstructure:"trigger"`
	// Migration 是启层时必须明确声明的有限复制校验与故障恢复 SLA。
	Migration LogRetentionMigrationConfig `mapstructure:"migration"`
	// ArchiveCopy 是「导出为独立副本」的配置面（键 `log_retention.archive_copy.*`）。
	ArchiveCopy LogRetentionArchiveCopyConfig `mapstructure:"archive_copy"`
	// ArchiveObjectRetention 是「归档对象自身的回收」的配置面
	// （键 `log_retention.archive_object_retention.*`）。
	ArchiveObjectRetention LogRetentionArchiveObjectRetentionConfig `mapstructure:"archive_object_retention"`
}

// LogRetentionArchiveObjectRetentionConfig 是归档目的地自身保留期的配置面。
//
// 定位：`archive_copy` 回答「到期数据往哪写一份副本」，本项回答「那份副本留多久」。
// 没有本项时归档目的地**只增不减**——热层清得再干净，归档桶仍按天累积
// （spec §7 的「归档对象自身的回收未做」正是这条）。
//
// 两道配置分家的理由：写副本与删副本的风险量级不同（删除不可逆）。
// 合成一个开关会让「想清旧归档、但不想再导出新的」变成不可能，也会让
// 「打开导出」顺带获得删归档的授权。
//
// 为什么 Retention 是字符串而不是 time.Duration：与 hot_retention 同一取舍——
// Go 的 time.ParseDuration 不认 `d`/`w`，而归档保留期天然按天写（如 730d）。
// 用 time.Duration 会让 viper 解码 `730d` 直接失败，连带**整个 Load 失败**。
//
// 默认值口径：enabled=false、retention 空。**空 = 不启用**，不补默认天数。
// 平台不该替运维决定「归档留几年」：补一个 730d 意味着打开开关就承诺两年后无条件删除，
// 而那个数字是合规/成本口径的事，必须由运维说出口。
type LogRetentionArchiveObjectRetentionConfig struct {
	// Enabled 是否允许按保留期清除归档对象（默认 false）。
	//
	// 打开它**仍然不是**删除授权：执行侧还要过与热层删除**同一套**三闸
	// （`discard` + `sweep.vl_sweep` + `log_vl.delete_enabled`），默认全关。
	Enabled bool `mapstructure:"enabled"`
	// Retention 归档副本保留期（自 ArchiveEvidence.ArchivedAt 起算）；空 = 不启用。
	//
	// 支持 `730d` / `104w` / `17520h` 等写法（经 retention.ParseTTL 解析）。
	// 启用但为空 ⇒ 启动即拒（与 archive_copy.root 同一取舍：
	// 静默按未启用处理会让运维以为归档在按保留期清理，而它一条都没删）。
	Retention string `mapstructure:"retention"`
	// SweepAtHour 每日触发的本地整点（0..23，默认 3）。
	//
	// 写成 int 并靠 SetDefault 注入默认值：0 是合法值（午夜），没有可用哨兵，
	// 与 eventstore_prune.sweep_at_hour 同一约定。越界值启动即拒
	// （时刻写错只会表现成「清理在业务高峰跑」，从日志里几乎看不出来）。
	SweepAtHour int `mapstructure:"sweep_at_hour"`
}

// LogRetentionArchiveCopyConfig 是硬规则次选路径「导出归档」的配置面。
//
// 定位：`move_to_cold` 搬不了（COLD 不可用）时的**替代 blocked** 的路径——
// 导出一份独立副本 + 逐文件回读校验，校验通过且删除三闸全开时才允许回收热层。
//
// 为什么需要 Root：导出必须写到一个**独立于热层与冷层**的目的地，否则「副本」不成立。
// 没有默认值——平台不该替运维决定归档写在哪块盘上（写错盘与写不下都是现场事故）。
// 启用但 Root 为空 ⇒ 启动即拒（见 Config.RetentionPolicy），不允许「开了开关但没处写」。
type LogRetentionArchiveCopyConfig struct {
	// Enabled 是否允许在搬不了冷层时导出为独立副本（默认 false）。
	Enabled bool `mapstructure:"enabled"`
	// Provider 归档目的地类型：local（默认）| s3 | minio。
	//
	// 为什么这里要允许 s3/minio（收尾）：导出归档的价值在「另存一份」——
	// 若目的地只能是与热层同机同盘的本地目录，那么单盘故障/整机报废时副本跟着没，
	// 「删前归档」这条硬规则就只剩「省空间」而丢掉了「保一份」。
	// Provider 抽象（`archive.Provider`）与真实 SigV4 实现（`archive.RemoteS3Provider`，
	// 底层 `internal/worker/storage/s3.go`）早由 交付并在真机 RustFS 上验过，
	// 本项只是把同一个接口接到导出侧——不新造协议、不新造凭证模型。
	Provider string `mapstructure:"provider"`
	// Root 归档目的地根目录（`provider=local` 时必填；`archive.LocalArchive` 的 Root）。
	//
	// 布局由 archive 包固定：`<root>/<storage_namespace>/<utc_day>/g<generation>/…`，
	// manifest 落在分区根下的 `manifest.json`（与 的 `logs/archive` 契约一致）。
	Root string `mapstructure:"root"`
	// —— 以下仅在 provider=s3|minio 时生效，语义与 `log_archive.*` 完全一致（同一份
	// `storage.Config`，同一个 SigV4 客户端）——
	// Endpoint 对象存储端点（如 s3.example.com:9000，不带 scheme；scheme 由 UseSSL 决定）。
	Endpoint string `mapstructure:"endpoint"`
	// Bucket 桶名。
	Bucket string `mapstructure:"bucket"`
	// Region SigV4 区域，缺省 us-east-1。
	Region string `mapstructure:"region"`
	// Prefix 对象键前缀（与本地布局相同，前缀之后仍是 `<ns>/<day>/g<gen>/…`）。
	Prefix string `mapstructure:"prefix"`
	// AccessKey/SecretKey 明文凭证引用。
	//
	// 与 `log_archive.secret_key` 同一纪律：只接受 `${ENV}` 引用，不在 yaml 里写死明文
	// （见 .claude/rules/config-files.md）。解析在装配处完成，本结构只承载已解析值。
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	// UseSSL 是否启用 TLS。
	UseSSL bool `mapstructure:"use_ssl"`
}

// ArchiveProviderKind 归一化归档目的地类型（空 = local）。
func (c LogRetentionArchiveCopyConfig) ArchiveProviderKind() string {
	kind := strings.ToLower(strings.TrimSpace(c.Provider))
	if kind == "" {
		return "local"
	}
	return kind
}

// ValidateArchiveCopyDestination 校验「导出归档的目的地是否真的给全了」。
//
// 为什么这件事必须挡在启动阶段（而不是等到第一次导出时才发现）：
// 静默按「未启用」处理会让运维以为归档在跑（策略说 enabled=true），而到期数据
// 继续占着热层直到盘满——这正是本功能要避免的结局。反过来对 s3/minio 分支
// 少判一个键，就会在第一次导出时以「对象键拼不出来」的形式失败，而那时**热层
// 已经冻结**，退出路径还要解冻，失败面比启动拒绝大得多。
func (c LogRetentionArchiveCopyConfig) ValidateArchiveCopyDestination() error {
	switch c.ArchiveProviderKind() {
	case "local":
		if strings.TrimSpace(c.Root) == "" {
			return fmt.Errorf("log_retention.archive_copy.root 不能为空（启用导出归档必须给出独立的目的地根目录）")
		}
	case "s3", "minio":
		if strings.TrimSpace(c.Endpoint) == "" {
			return fmt.Errorf("log_retention.archive_copy.endpoint 不能为空（provider=%s 必须给出对象存储端点）", c.ArchiveProviderKind())
		}
		if strings.TrimSpace(c.Bucket) == "" {
			return fmt.Errorf("log_retention.archive_copy.bucket 不能为空（provider=%s 必须给出桶名）", c.ArchiveProviderKind())
		}
	default:
		return fmt.Errorf("log_retention.archive_copy.provider 不支持 %q（支持 local / s3 / minio）", c.Provider)
	}
	return nil
}

// Normalized 把非法配置收敛到安全默认（不拒启：本项不影响安全边界，
// 与 budget 类参数同一口径——丢一个不合法的时刻不值得让节点升不上来）。
func (c LogArchiveInstanceGzAdoptConfig) Normalized() LogArchiveInstanceGzAdoptConfig {
	out := c
	if out.SweepAtHour < 0 || out.SweepAtHour > 23 {
		// 越界即告警并用默认值，而不是拒启。
		//
		// 为什么不拒：本项只控制「每日几点开始扫描并吸存」。吸存是只增不删、幂等的动作，
		// 时刻写错只是推迟一天，没有正确性后果。把它做成启动即拒，会把一个手误放大成
		// 「整个节点起不来」——那正是 2026-10-27 `copy_verify_budget` 那次事故的形态。
		slog.Warn("log_archive.instance_gz_adopt.sweep_at_hour 越界，回退默认 4 点（本项不拒启）",
			"configured", out.SweepAtHour)
		out.SweepAtHour = 4
	}
	return out
}

// LogRetentionMigrationConfig 声明搬运所需余量，不代表无限失败下的最后副本保证。
type LogRetentionMigrationConfig struct {
	CopyVerifyBudget string `mapstructure:"copy_verify_budget"`
	RecoveryBudget   string `mapstructure:"recovery_budget"`
}

// LogRetentionTriggerConfig 搬运触发口径（键 `log_retention.trigger.*`）。
type LogRetentionTriggerConfig struct {
	// DiskPercent 磁盘水位阈值；0 = 只按年龄（默认）。读数复用容量门禁同一真源。
	DiskPercent float64 `mapstructure:"disk_percent"`
	// MinAge 磁盘触发时的最小分区年龄（字符串，支持 7d）；空 = 用默认 7d。
	MinAge string `mapstructure:"min_age"`
	// Interval 是**冷层搬运驱动器的调度周期**（字符串，支持 15m/1h）。
	//
	// 为什么必须有本键（B1，2026-10-12 修复）：生产唯一的驱动器构造点
	// `retention.NewDriver` 取的是 `policy.Trigger.Normalize().Interval`
	// （见 driver.go 的 `interval: policy.Trigger.Normalize().Interval`），
	// 而不是 `sweep.interval`（后者只作用于删除执行器 `Sweeper`，而本版本
	// `NewSweeper` 在生产**没有构造点**）。
	// 此前本结构体缺 Interval ⇒ 运维若按直觉写 `trigger.interval` 会被 viper **静默丢弃**
	// （不报错、不警告），实际节奏恒为 `DefaultDriverInterval`；而样例配置把
	// `sweep.interval` 当作调度旋钮呈现 ⇒ 形成"文档说的键不生效、生效的键配不了"的死旋钮。
	// 空/非法回退 DefaultDriverInterval。
	Interval string `mapstructure:"interval"`
	// SweepAtHour 是**每日搬运触发的本地整点**（字符串，如 "3" 表示凌晨 3 点；默认 3）。
	//
	// 为什么是字符串而不是 int：本结构体的另两个键（min_age / interval）都用「字符串 + 空 = 默认」
	// 的约定，而同包其它「0 是有效值」的键没有可用哨兵（0 点就是合法的午夜），
	// 用 int 会让"未配置"与"配成 0 点"不可区分 —— 前者必须取默认 3 点，后者是用户明确的午夜。
	// 写成字符串后空串即"未配置"，与 min_age / interval 的读法完全一致。
	//
	// 取值：0..23 为本地整点；-1 表示**不启用每日调度**，回退到上面的 Interval 间隔 ticker
	// （保留旧行为的兼容档，避免存量 `trigger.interval` 变成静默失效的死键）。
	// 其余值启动即拒（时刻写错只会表现成"搬运在业务高峰跑"，从日志里极难发现）。
	SweepAtHour string `mapstructure:"sweep_at_hour"`
}

// LogRetentionSource 某个来源的保留期覆盖。
type LogRetentionSource struct {
	// Match 源标识；以 * 结尾表示前缀匹配（如 `inst:`）。
	Match string `mapstructure:"match"`
	// ByLevel 级别 → 保留期字符串；"0" = 该级别永久保留。
	ByLevel map[string]string `mapstructure:"by_level"`
}

// LogRetentionSweep 保留策略执行器配置。
type LogRetentionSweep struct {
	// VLSweep 是否允许调用 VL 删除接口（默认 false）。
	//
	// 两道闸之一：本项是**执行闸**，还需要 `discard: true`（意图闸）同时成立才会真的删。
	// 两个独立开关而非一个，是因为这条路径的后果不可逆，一处误开与两处同时误开的
	// 概率差得很远；默认路径永远是把日分区搬运到冷层（数据仍在、仍可查）。
	VLSweep bool `mapstructure:"vl_sweep"`
	// Interval 扫描间隔；0 用默认 1h。
	Interval time.Duration `mapstructure:"interval"`
	// Timeout 单次删除请求超时；0 用默认 30s。
	Timeout time.Duration `mapstructure:"timeout"`
}

// RetentionPolicy 装配为 retention 包策略。解析失败时返回错误（由 Load 启动即拒）。
func (c *Config) RetentionPolicy() (retention.Policy, error) {
	if c == nil {
		return retention.Policy{}, nil
	}
	cfg := c.LogRetention
	p := retention.Policy{
		Enabled: cfg.Enabled,
		Discard: cfg.Discard,
		Trigger: retention.Trigger{
			DiskPercent: cfg.Trigger.DiskPercent,
		},
		Sweep: retention.Sweep{
			VLSweep:  cfg.Sweep.VLSweep,
			Interval: cfg.Sweep.Interval,
			Timeout:  cfg.Sweep.Timeout,
			// 第三闸（VL 侧）来自 log_vl.delete_enabled：它是受管 VL 进程的参数事实，
			// 由 vlsup 按同一配置位注入 -delete.enable。两处读同一个键，避免「策略以为开了、
			// VL 其实没开」的分裂读数（见 retention.Policy.DeleteGatesOpen）。
			VLDeleteEnabled: c.LogVL.DeleteEnabled,
		},
		ArchiveCopy: retention.ArchiveCopyPolicy{Enabled: cfg.ArchiveCopy.Enabled},
	}
	// 导出归档启用但没给目的地 ⇒ 启动即拒。
	//
	// 为什么用拒绝而不是「静默按未启用处理」：静默处理会让运维以为归档在跑
	// （策略说 enabled=true），而到期数据继续占着热层直到盘满——这正是本功能要避免的结局。
	// 反过来，把「没有目的地」显式挡在启动阶段，问题在第一次部署时就暴露。
	if cfg.ArchiveCopy.Enabled {
		if err := cfg.ArchiveCopy.ValidateArchiveCopyDestination(); err != nil {
			return retention.Policy{}, err
		}
	}
	// 归档对象回收：开了开关就必须给出保留期，否则启动即拒。
	//
	// 与 archive_copy.root 同一取舍——「开了但没参数」在生产表现为「什么都没发生」：
	// 运维看到 enabled=true，去面板看归档仍然一天天涨，而日志里没有任何报错。
	// 静默空转是这类「默认关闭 + 必须显式配置」的功能最典型的现场。
	if cfg.ArchiveObjectRetention.Enabled && strings.TrimSpace(cfg.ArchiveObjectRetention.Retention) == "" {
		return retention.Policy{}, fmt.Errorf(
			"log_retention.archive_object_retention.retention 不能为空（启用归档对象回收必须给出明确的保留期，如 730d）")
	}
	if raw := strings.TrimSpace(cfg.ArchiveObjectRetention.Retention); raw != "" {
		d, err := retention.ParseTTL(raw)
		if err != nil {
			return retention.Policy{}, fmt.Errorf("log_retention.archive_object_retention.retention: %w", err)
		}
		// 解析成 0 表示「永久保留」——对**删除**类配置来说这是个陷阱：
		// 运维写 "0" 的直觉可能是「立刻删」，而语义恰好相反。
		// 明确拒绝，避免「以为配了保留期，实际一条都不会删」。
		if d <= 0 {
			return retention.Policy{}, fmt.Errorf(
				"log_retention.archive_object_retention.retention 必须为正的有限时长，得到 %q（0 表示永久保留，与「启用回收」相矛盾）", raw)
		}
		p.ArchiveObjectRetention.Retention = d
	}
	p.ArchiveObjectRetention.Enabled = cfg.ArchiveObjectRetention.Enabled
	p.ArchiveObjectRetention.SweepAtHour = cfg.ArchiveObjectRetention.SweepAtHour
	if h := p.ArchiveObjectRetention.SweepAtHour; h < 0 || h > 23 {
		return retention.Policy{}, fmt.Errorf(
			"log_retention.archive_object_retention.sweep_at_hour 必须在 0..23（本地整点），得到 %d", h)
	}
	if strings.TrimSpace(cfg.HotRetention) != "" {
		d, err := retention.ParseTTL(cfg.HotRetention)
		if err != nil {
			return retention.Policy{}, fmt.Errorf("log_retention.hot_retention: %w", err)
		}
		p.HotRetention = d
	}
	if strings.TrimSpace(cfg.ColdRetention) != "" {
		d, err := retention.ParseTTL(cfg.ColdRetention)
		if err != nil {
			return retention.Policy{}, fmt.Errorf("log_retention.cold_retention: %w", err)
		}
		p.ColdRetention = d
	}
	if strings.TrimSpace(cfg.Trigger.MinAge) != "" {
		d, err := retention.ParseTTL(cfg.Trigger.MinAge)
		if err != nil {
			return retention.Policy{}, fmt.Errorf("log_retention.trigger.min_age: %w", err)
		}
		p.Trigger.MinAge = d
	}

	// B1：搬运驱动器的调度周期（NewDriver 实际读取的键）。空 = 用 DefaultDriverInterval。
	if strings.TrimSpace(cfg.Trigger.Interval) != "" {
		d, err := retention.ParseTTL(cfg.Trigger.Interval)
		if err != nil {
			return retention.Policy{}, fmt.Errorf("log_retention.trigger.interval: %w", err)
		}
		p.Trigger.Interval = d
	}

	// 每日搬运触发时刻（默认凌晨 3 点）。空 = 默认值；-1 = 回退上面的 interval ticker 兼容档。
	// 不在这里做范围判断以外的事：0..23 的有效性由 retention.Policy.Validate 统一拦
	// （启动即拒，与 hot_retention 等键同一取舍）。
	p.Trigger.SweepAtHour = retention.DefaultSweepAtHour
	if raw := strings.TrimSpace(cfg.Trigger.SweepAtHour); raw != "" {
		hour, err := strconv.Atoi(raw)
		if err != nil {
			return retention.Policy{}, fmt.Errorf(
				"log_retention.trigger.sweep_at_hour: 无法解析 %q（应为本地整点小时数 0..23，或 -1 表示回退间隔 ticker）", raw)
		}
		p.Trigger.SweepAtHour = hour
	}
	if len(cfg.ByLevel) > 0 {
		p.ByLevel = make(map[string]time.Duration, len(cfg.ByLevel))
		for rawLevel, rawTTL := range cfg.ByLevel {
			level := retention.LevelKeyOf(rawLevel)
			if level == "" {
				return retention.Policy{}, fmt.Errorf("log_retention.by_level 含未知级别 %q（支持 TRACE/DEBUG/INFO/WARN/ERROR）", rawLevel)
			}
			ttl, err := retention.ParseTTL(rawTTL)
			if err != nil {
				return retention.Policy{}, fmt.Errorf("log_retention.by_level.%s: %w", rawLevel, err)
			}
			p.ByLevel[level] = ttl
		}
	}
	for i, src := range cfg.Sources {
		override := retention.SourceOverride{Match: strings.TrimSpace(src.Match)}
		if len(src.ByLevel) > 0 {
			override.ByLevel = make(map[string]time.Duration, len(src.ByLevel))
			for rawLevel, rawTTL := range src.ByLevel {
				level := retention.LevelKeyOf(rawLevel)
				if level == "" {
					return retention.Policy{}, fmt.Errorf("log_retention.sources[%d].by_level 含未知级别 %q", i, rawLevel)
				}
				ttl, err := retention.ParseTTL(rawTTL)
				if err != nil {
					return retention.Policy{}, fmt.Errorf("log_retention.sources[%d].by_level.%s: %w", i, rawLevel, err)
				}
				override.ByLevel[level] = ttl
			}
		}
		p.Sources = append(p.Sources, override)
	}
	if err := p.Validate(); err != nil {
		return retention.Policy{}, err
	}
	return p, nil
}

// defaultCopyVerifyBudget / defaultRecoveryBudget 是「未声明时」的保守默认值。
//
// 取值依据：它们只参与 `ValidateRuntimeHeadroom` 的**余量评估**
// （`required = 24h + DriverInterval + CopyVerifyBudget + RecoveryBudget`），
// 不控制任何实际执行路径——所以「缺省」不会让功能少做一步，只会让余量估算偏保守。
//
// 取 1h 的理由：真机上单日分区的导出 + 三重校验是分钟级（见 log-cost-governance spec），
// 1h 给出 60× 上界余量；恢复同理。取得更大只会无谓抬高余量门，
// 让「hot 27d / native 30d」这类余量只剩 3d 的存量配置被误拒。
const (
	defaultCopyVerifyBudget = time.Hour
	defaultRecoveryBudget   = time.Hour
)

// validateRuntimeRetention 使用与子进程相同的默认和解析规则，启层必须留出明确迁移余量。
// 有限原生 TTL 在故障超出声明 SLA 时仍可能淘汰，不能替代独立副本。
//
// 2026-10-29：冷热双层已废除 ⇒ 不再校验 `cold_retention_period`（该键已删除），
// 也不再要求 `start_cold`（没有第二个接收端可启）。剩下的是一个 VL 的保留期校验。
func (c *Config) validateRuntimeRetention(p retention.Policy) error {
	hotRaw := strings.TrimSpace(c.LogVL.RetentionPeriod)
	if hotRaw == "" {
		hotRaw = vlsup.DefaultRetentionPeriod
	}
	actual, err := retention.ParseRuntimeTTL(hotRaw)
	if err != nil {
		return fmt.Errorf("log_vl.retention_period: %w", err)
	}
	// 保留期必须覆盖**两段声明**：产品热窗口（到期前可查）与冷留存（长期可查）。
	// 单实例下同一份 retention 同时承担两者，取两者最大值才不会静默提前淘汰。
	declared := p.HotWindow()
	if cold := p.Normalize().ColdRetention; cold > declared {
		declared = cold
	}
	if actual < declared {
		return fmt.Errorf("log_vl.retention_period 实际生效 %q (%s) 短于 log_retention 声明 %s；VL 原生淘汰不受应用删除开关保护", hotRaw, actual, declared)
	}
	if !p.Enabled {
		return nil
	}
	requirements := retention.RuntimeHeadroom{DriverInterval: p.Trigger.Normalize().Interval}
	if _, err := retention.ValidateRuntimeHeadroom(hotRaw, p.HotWindow(), requirements); err != nil {
		return fmt.Errorf("log_vl.retention_period: %w", err)
	}
	if strings.TrimSpace(c.LogVL.BinaryPath) == "" && strings.TrimSpace(c.LogVL.PackagePath) == "" {
		return fmt.Errorf("log_vl: 启用分层需要受管 binary_path 或 package_path，外部 HOT-only 无接收端/搬运驱动")
	}
	if strings.TrimSpace(c.LogVL.BinaryPath) != "" && strings.TrimSpace(c.LogVL.PackagePath) == "" && strings.TrimSpace(c.LogVL.AssetSHA256) == "" {
		return fmt.Errorf("log_vl.asset_sha256: 启用分层需要明确受管资产校验")
	}
	if c.LogVL.Username == "" || c.LogVL.Password == "" {
		return fmt.Errorf("log_vl: 启用分层需要非空本地鉴权凭据")
	}
	for _, field := range []struct {
		key      string
		raw      string
		dst      *time.Duration
		fallback time.Duration
	}{
		{"copy_verify_budget", c.LogRetention.Migration.CopyVerifyBudget, &requirements.CopyVerifyBudget, defaultCopyVerifyBudget},
		{"recovery_budget", c.LogRetention.Migration.RecoveryBudget, &requirements.RecoveryBudget, defaultRecoveryBudget},
	} {
		d, err := retention.ParseTTL(strings.TrimSpace(field.raw))
		if err != nil {
			return fmt.Errorf("log_retention.migration.%s 无法解析：%w", field.key, err)
		}
		if d <= 0 {
			// 显式写了 0 / 负数要拒：那是“声明了一个用不了的预算”，静默改成默认等于把运维的意图掉包。
			if strings.TrimSpace(field.raw) != "" {
				return fmt.Errorf("log_retention.migration.%s 必须为正的有限时长，实际 %q", field.key, field.raw)
			}
			// **未声明则取默认值，而不是拒启**。
			//
			// 为什么（2026-10-27 真机事故）：这两项原本要求必须显式声明，而 SetDefault 给的是空串、
			// `configs/worker.yml` 样例也没写。于是**任何升级前合法的存量配置**（包括正在生产跑的
			// 那份）升级后都启动失败。这直接违反本函数下方自己写下的那条原则：
			// 「启动即拒会让存量节点升不上来」。预算类参数缺的是“耗时上界”而非安全边界，
			// 有安全默认值可用，没有强制人工指定的必要。
			d = field.fallback
		}
		*field.dst = d
	}
	if _, err := retention.ValidateRuntimeHeadroom(hotRaw, p.HotWindow(), requirements); err != nil {
		return fmt.Errorf("log_vl.retention_period: %w", err)
	}
	// 每日固定时刻调度（默认凌晨 3 点）把「到点 → 开始搬」的最坏等待从 ticker 的 15m
	// 放宽到一整天（见 retention.Policy.SweepWaitBudget），于是上面那条按 interval 预留扫描余量的
	// 校验，对**余量偏紧**的存量配置不再足够。这里只**告警不拒绝**：那些配置在升级前是被接受的
	// （启动即拒会让存量节点升不上来），但按新口径确实差了一点，必须在启动日志里点名，
	// 否则「少留了一天」永远不会有人看见。
	dailyReqs := retention.RuntimeHeadroom{
		DriverInterval:   p.SweepWaitBudget(),
		CopyVerifyBudget: requirements.CopyVerifyBudget,
		RecoveryBudget:   requirements.RecoveryBudget,
	}
	if dailyReqs.DriverInterval != requirements.DriverInterval { // ticker 兼容档下两个口径相同，不重复告警
		if _, tight := retention.ValidateRuntimeHeadroom(hotRaw, p.HotWindow(), dailyReqs); tight != nil {
			slog.Warn("原生TTL相对热窗口的余量按每日调度口径偏紧（不阻断启动，但 VL 原生淘汰可能抢先删）",
				"hotRetention", p.HotWindow().String(), "nativeRetention", hotRaw,
				"scheduleWait", dailyReqs.DriverInterval.String(), "error", tight)
		}
	}
	return nil
}

// WALBudgetNotice 返回「WAL 积压上限被显式关掉」的提醒（未关掉时返回空串）。
//
// 语义（2026-10-02 复核后收紧）：本值现在是**真实积压上限**（直接接 WAL.SetLimits），
// 过去它被接到一个只增不减的累计量上，因此 0 在当时确实等于「字节上没有上界」——
// 而现在 0 表示「字节维度不设上限」，条目维度的 log_capacity.max_wal_entries
// （默认 5000）仍然生效，恢复状态不会无界。这不直接拒绝配置：受管环境可能确实由外部
// 配额兜底，但必须在启动日志里被点名，否则「没配」与「配成不限」在现场看起来完全一样。
// RecoveryQuotaTuning 把 `log_capacity.recovery_*` 收敛为 ingest 的恢复期配额旋钮。
//
// 口径：factor <1 或 grace 非法/非正 ⇒ 各自回退默认（4 / 30m ✓）——回退默认永远是安全方向：
// 拓宽多一点只是更宽的有界界 ✓，宽限长一点只是更晚回归 ✓，都不会破坏"有界"与红线 ✓。
func (c *Config) RecoveryQuotaTuning() (factor float64, grace time.Duration) {
	factor, grace = 4, 30*time.Minute
	if c == nil {
		return factor, grace
	}
	if c.LogCapacity.RecoveryQuotaFactor >= 1 {
		factor = c.LogCapacity.RecoveryQuotaFactor
	}
	if d, err := time.ParseDuration(strings.TrimSpace(c.LogCapacity.RecoveryDrainGrace)); err == nil && d > 0 {
		grace = d
	}
	return factor, grace
}

// EffectiveMaxWALBytes 返回**交给 acquire.WAL 的字节上限**：配置 0 表示"不限" ⇒ 返回 -1 哨兵
// （acquire 侧负值 = 不设上限）；>0 原样返回。
//
// 为什么必须显式转换（2026-10-03 现场 16 MiB 之谜）：配置 0 的语义是"字节维度不限"，而
// acquire 把 0 当作"用包内默认"（16 MiB）✗ —— 两者相撞的现场表现就是"不设上限"静默变成 16 MiB，
// 一批源停在 32.6 MiB（双闸同时起作用）而另一批停在 512 MiB（显式配了值）✓✓。
func (c *Config) EffectiveMaxWALBytes() int64 {
	if c == nil || c.LogCapacity.MaxWALBytes == 0 {
		return -1 // 不限
	}
	if c.LogCapacity.MaxWALBytes > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(c.LogCapacity.MaxWALBytes)
}

// WALBudgetNotice 返回启动时必须点名的容量口径提示（空串表示无需提示）。
func (c *Config) WALBudgetNotice() string {
	if c == nil || c.LogCapacity.MaxWALBytes != 0 {
		return ""
	}
	return "log_capacity.max_wal_bytes=0（字节维度不限）：暂停仍由 log_capacity.max_wal_entries 兜住，但单条超大事件（如整段堆栈）不再有字节上界，请确认这是有意为之"
}

// LogLevel 解析 `log.level` 为 slog 等级。
//
// 为什么需要它：`log.level` 此前**没有任何装配点**——Worker 从不调用 slog.SetDefault，
// 于是配置项形同不存在（写 debug 不产生任何 DEBUG 输出，写 error 也压不住 INFO）。
// 现场表现是「配置改了但日志没变」，排查时极难归因（与 log_ingest 系列接线断掉的形态同类）。
//
// 非法值**启动即拒**：日志等级写错会让排障所依赖的输出静默消失，
// 与 timezone/charset 是同一类「配错就静默」的危险项，不能用回退掩盖。
func (c *Config) LogLevel() (slog.Level, error) {
	raw := ""
	if c != nil {
		raw = strings.TrimSpace(c.Log.Level)
	}
	switch strings.ToLower(raw) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("log.level 非法: %q（支持 debug/info/warn/error）", raw)
	}
}

// LogFormat 解析 `log.format`（text/json）。
//
// 同样此前没有装配点。非法值启动即拒，理由同 LogLevel。
func (c *Config) LogFormat() (string, error) {
	raw := ""
	if c != nil {
		raw = strings.TrimSpace(c.Log.Format)
	}
	switch strings.ToLower(raw) {
	case "", "text":
		return "text", nil
	case "json":
		return "json", nil
	default:
		return "", fmt.Errorf("log.format 非法: %q（支持 text/json）", raw)
	}
}

// ConfigPath 返回本次实际读取并应用的配置文件绝对路径；空串表示本次启动没有任何配置文件生效
// （键值全部来自内置默认与 JIANMANAGER_ 环境变量，包含「指定了不存在的路径」）。
func (c *Config) ConfigPath() string {
	if c == nil {
		return ""
	}
	return c.configPath
}

// IngestDefaultTimeZone 返回装配给 ingest.Options.DefaultTimeZone 的节点级默认时区名。
//
// 独立成方法而非在装配点内联读字段：装配值需要被测试直接盯住（配置 → 采集归一化这条接线
// 一旦断掉，日志时间轴会静默偏移，且没有任何编译错误提示）。空串 = UTC。
func (c *Config) IngestDefaultTimeZone() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.LogIngest.TimeZone)
}

// IngestDefaultCharset 返回装配给 ingest.Options.DefaultCharset 的节点级默认字符集。
//
// 独立成方法的原因同 IngestDefaultTimeZone：这条接线一旦断掉，中文日志会被静默按 UTF-8 净化
// （正文里出现替换字符）而没有任何编译错误提示。空串 = auto（既有行为，零配置零变化）。
func (c *Config) IngestDefaultCharset() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.LogIngest.Charset)
}

// ReclaimRollTuning 已于 2026-10-29 删除：它收敛的两个旋钮（max_steps / budget）
// 服务的是「分段链逐跳推进」，reclaim 改按投递证据推进后一次就到顶，旋钮失去作用对象。
// 配置键 `log_index.reclaim_roll.*` 同步删除（viper 忽略未知键，旧 worker.yml 留着不报错）。

// LogArchiveConfig 是 Worker Deep Archive 的受管对象存储配置。
// SecretKey 只允许由环境变量注入，禁止写入 worker.yml 或诊断快照。
type LogArchiveConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	Provider  string `mapstructure:"provider"`
	Endpoint  string `mapstructure:"endpoint"`
	Bucket    string `mapstructure:"bucket"`
	Region    string `mapstructure:"region"`
	Prefix    string `mapstructure:"prefix"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	// InstanceGzAdopt 是「实例本地 logs/*.log.gz 归档吸存」的调度面。
	InstanceGzAdopt LogArchiveInstanceGzAdoptConfig `mapstructure:"instance_gz_adopt"`
}

// LogArchiveInstanceGzAdoptConfig 是实例 gz 吸存的开关与时刻。
//
// 默认**开**——这与删除类能力（默认全关）的口径刻意相反，理由不是标尺不一致：
// 吸存只**增加**一份副本，不删任何东西（连实例本地的 .gz 也不动）。
// 它唯一的代价是归档盘占用，而那正是「吸存」这个动作的目的。
// 反之若默认关，则默认部署下这条能力永远为零调用——正是此前它被登记为
// 「骨架具备、生产零调用」的原因。
//
// 关掉它不会删除任何已吸存的副本。
type LogArchiveInstanceGzAdoptConfig struct {
	// Enabled 控制每日扫描；默认 true。
	Enabled bool `mapstructure:"enabled"`
	// SweepAtHour 是每日执行的小时（进程本地时区），默认 4 点——
	// 比保留驱动器与归档对象回收（都是 3 点）错开一小时，避开同一个 IO 高峰。
	SweepAtHour int `mapstructure:"sweep_at_hour"`
}

// Load 从配置文件和环境变量加载 Worker 配置。
//
// path 为空时在工作目录与 configs/ 下查找 worker.yml（均可选），找不到回退 worker.yaml（兼容）。
// 所有项有合理默认，零配置即可启动；JIANMANAGER_ 前缀环境变量按路径覆盖配置文件值。
// enrollment token 经 JIANMANAGER_ENROLL_TOKEN 注入、不从 yaml 读取（一次性凭据不留盘）。
func Load(path string) (*Config, error) {
	v := viper.New()

	// 默认值（零配置即可启动开发环境）。
	v.SetDefault("name", "node-01")
	v.SetDefault("control_plane", "localhost:9100")
	v.SetDefault("node_secret", "")
	v.SetDefault("ws.port", 9102)
	v.SetDefault("data_dir", "")
	v.SetDefault("servers_dir", "")
	v.SetDefault("host", "")
	v.SetDefault("jwt_secret", "dev-secret-change-me")
	v.SetDefault("enroll_token", "")
	v.SetDefault("enroll_token_file", "")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
	v.SetDefault("log_query.vl_url", "")
	v.SetDefault("log_query.username", "")
	v.SetDefault("log_query.password", "")
	v.SetDefault("log_vl.binary_path", "")
	v.SetDefault("log_vl.package_path", "")
	v.SetDefault("log_vl.asset_sha256", "")
	v.SetDefault("log_vl.username", "")
	v.SetDefault("log_vl.password", "")
	v.SetDefault("log_vl.data_root", "")
	v.SetDefault("log_vl.retention_period", vlruntime.DefaultRetentionPeriod)
	// 磁盘占用上限（2026-10-29）：默认 0 = 不限制，与改动前行为逐字一致。
	// 不在这里给“聪明”的默认值：上限该设多少取决于运维给 VL 划了多少盘，代码猜不出来。
	v.SetDefault("log_vl.max_disk_space_bytes", 0)
	v.SetDefault("log_vl.max_disk_usage_percent", 0)
	// 2026-10-29：`log_vl.cold_retention_period` 已删除（冷热双层废除）。
	// `log_retention.cold_retention` 作为**声明**保留：它现在表达「期望的可查范围」，
	// 与 hot_retention 一起取最大值去校 `log_vl.retention_period`（见 validateRuntimeRetention）。
	v.SetDefault("log_retention.hot_retention", vlruntime.DefaultHotRetention.String())
	v.SetDefault("log_retention.cold_retention", vlruntime.DefaultColdRetention.String())
	v.SetDefault("log_vl.hot_cache_bytes", vlsup.DefaultHotCacheBytes)
	// VL 官方推荐按可用内存百分比自适应；显式设为 0 可退回按 hot_cache_bytes 的旧行为。
	v.SetDefault("log_vl.memory_allowed_percent", 10)
	v.SetDefault("log_vl.memory_limit_bytes", int64(0))
	v.SetDefault("log_vl.delete_enabled", false)
	// 2026-10-29：只为唯一的受管实例（hot）登记默认。
	// 旧键 `namespace_budgets.cold.*` / `.rehydrate.*` 不再登记——它们会被
	// `RuntimeBudget()` 当成未知层而拒绝，这是刻意的：静默忽略一个运维真写下的限额
	// 比启动失败更危险。
	for _, key := range []string{"cache_bytes", "go_memory_limit_bytes", "rss_bytes"} {
		v.SetDefault("log_vl.namespace_budgets.hot."+key, int64(0))
	}
	v.SetDefault("log_vl.budget.disk_bytes", int64(0))
	v.SetDefault("log_vl.budget.rss_bytes", int64(0))
	v.SetDefault("log_vl.budget.reservations.wal", int64(0))
	v.SetDefault("log_vl.budget.reservations.staging", int64(0))
	v.SetDefault("log_vl.budget.reservations.export", int64(0))
	v.SetDefault("log_retention.migration.copy_verify_budget", "")
	v.SetDefault("log_retention.migration.recovery_budget", "")
	// 事件段回收：默认关闭（删除类能力，用户护栏要求默认全关）+ 每日本地 3 点。
	// 导出归档（硬规则次选路径）：默认关闭，且默认不给根目录——没有目的地就不该有这条路径。
	v.SetDefault("log_retention.archive_copy.enabled", false)
	// 默认 local：把默认值定成 s3 会让「本地目录已够用」的部署凭空多出一套凭证要求。
	v.SetDefault("log_retention.archive_copy.provider", "local")
	v.SetDefault("log_retention.archive_copy.root", "")
	v.SetDefault("log_retention.archive_copy.endpoint", "")
	v.SetDefault("log_retention.archive_copy.bucket", "")
	v.SetDefault("log_retention.archive_copy.region", "us-east-1")
	v.SetDefault("log_retention.archive_copy.prefix", "")
	v.SetDefault("log_retention.archive_copy.access_key", "")
	v.SetDefault("log_retention.archive_copy.secret_key", "")
	v.SetDefault("log_retention.archive_copy.use_ssl", false)
	// 归档对象自身的回收：默认关闭、默认**无保留期**、每日本地 3 点。
	// 不开 `retention` 的默认值是有意的：平台不替运维决定「归档留几年」
	// （730d 是例子里最常见的值，但它属于合规/成本口径，必须由运维说出口）。
	v.SetDefault("log_retention.archive_object_retention.enabled", false)
	v.SetDefault("log_retention.archive_object_retention.retention", "")
	v.SetDefault("log_retention.archive_object_retention.sweep_at_hour", 3)
	v.SetDefault("log_vl.hot_port", 0)
	// 流标签（2026-10-31）：默认开启，取值就是架构裁定的 `log_source_id,stream`。
	// 与以往保持一致：默认值不写在 SetDefault 里（那是 vlsup 的单一真源），
	// 这里只登记空串，让未配置时走 vlsup.DefaultStreamFields。
	v.SetDefault("log_vl.stream_fields", "")
	v.SetDefault("log_vl.disable_stream_fields", false)
	// 2026-10-29：冷热双层废除后这 4 个键已删（结构体字段不存在）。
	// 此处不再写它们的 SetDefault——那只会造成「键还在」的错觉；viper 对未知键不报错，
	// 旧 worker.yml 里留着它们也不会出问题。
	// 2026-11-06：磁盘缓冲（log_spool.*）已整体删除。
	// 它只有写端、没有读端（Peek/PeekEvents/Ack 在 spool 包外零调用），
	// 写进去的事件永远不会被重投——那不是缓冲，是「换个地方丢」。
	// 生产配置里曾开着 enabled=true，导致每次启动白报一条 ERROR。
	v.SetDefault("log_capacity.max_wal_bytes", DefaultMaxWALBytes)
	v.SetDefault("log_capacity.recovery_quota_factor", 4)
	v.SetDefault("log_capacity.recovery_drain_grace", "30m")
	v.SetDefault("log_capacity.max_wal_entries", DefaultMaxWALEntries)
	v.SetDefault("log_capacity.max_replay_events_per_drain", DefaultMaxReplayEventsPerDrain)
	v.SetDefault("log_capacity.max_gaps", 0)
	v.SetDefault("log_capacity.degraded_at_percent", 80.0)
	v.SetDefault("log_capacity.pause_at_percent", 90.0)
	v.SetDefault("log_sources", []LogSourceConfig{})
	// 采集索引（，`log_index.*`）整组已随旧链路删除：新架构没有 SQLite 采集索引，
	// 也没有「投递批次裁剪 / 持久化提交单元」这些概念（逐批直写 VL）。
	// 旧 worker.yml 里留着这些键不会报错（viper 忽略未知键）。
	v.SetDefault("log_archive.enabled", false)
	v.SetDefault("log_archive.provider", "local")
	v.SetDefault("log_archive.endpoint", "")
	v.SetDefault("log_archive.bucket", "")
	v.SetDefault("log_archive.region", "us-east-1")
	v.SetDefault("log_archive.prefix", "logs")
	v.SetDefault("log_archive.access_key", "")
	v.SetDefault("log_archive.secret_key", "")
	// 实例 gz 吸存：默认**开**——它只增加副本、不删任何东西。
	// 默认 4 点，与保留驱动器/归档对象回收（都是 3 点）错开，避开同一个 IO 高峰。
	v.SetDefault("log_archive.instance_gz_adopt.enabled", true)
	v.SetDefault("log_archive.instance_gz_adopt.sweep_at_hour", 4)
	// 采集归一化的节点级默认时区（缺陷 C）：留空 = UTC，未配置时零行为变化。
	// 中文 locale 的 JVM 与 Worker 同机部署时配 `local` 即可对齐本地时间。
	v.SetDefault("log_ingest.time_zone", "")
	// 采集归一化的节点级默认字符集（复审 P2-3）：留空 = auto。中文 locale 的 JVM 与 Worker
	// 同机部署且日志为 GBK 时配 gbk 即可（显式声明优于自动判定）。
	v.SetDefault("log_ingest.charset", "")
	v.SetDefault("log_ingest.read_limits.max_line_bytes", 0)
	v.SetDefault("log_ingest.read_limits.max_bytes_per_poll", int64(0))
	// 新采集器（tailer/logtail）：**默认开**（2026-11-06 切换）。旧链路（ingest/ledger）已于同批删除，
	// 不存在“两套互斥”的配置面了：本开关只控制“开不开采集”。
	v.SetDefault("log_tailer.enabled", true)
	v.SetDefault("log_tailer.paths", []string{})
	v.SetDefault("log_tailer.exclude", []string{})
	// 首次部署要能补上部署前的历史日志（用户明确要这个）。
	v.SetDefault("log_tailer.read_from_head", true)
	v.SetDefault("log_tailer.registry_path", "")
	v.SetDefault("log_tailer.scan_interval", "")
	v.SetDefault("log_tailer.progress_interval", "")
	// 多行合并（键 log_tailer.multiline.*）：默认**不启用**——它对日志语义有实质影响
	// （决定“一条事件是什么”），与采样一样属于“开了就改变存下来的东西”，
	// 必须由运维显式打开。
	v.SetDefault("log_tailer.multiline.pattern", "")
	v.SetDefault("log_tailer.multiline.negate", false)
	v.SetDefault("log_tailer.multiline.match", "after")
	v.SetDefault("log_tailer.multiline.flush_pattern", "")
	v.SetDefault("log_tailer.multiline.max_lines", 500)
	v.SetDefault("log_tailer.multiline.timeout", "5s")
	// 历史 gz 导入：默认**关**（主动补数据才开，避免升级后突然读几千个文件）。
	v.SetDefault("log_tailer.include_gzip", false)
	v.SetDefault("search.ignore", []string{})
	// 节点制品缓存：默认 0=不限（建实例命中即秒拷免重下；按需经 CP 设上限触发 LRU）。
	v.SetDefault("artifact_cache.max_bytes", int64(0))
	// 反编译：默认无显式 CFR 路径、允许按需下载（首次反编译时拉 CFR 落数据根缓存）。
	v.SetDefault("decompiler.cfr_path", "")
	v.SetDefault("decompiler.allow_download", true)
	// 出站代理：默认空（直连/沿用环境变量代理），不破坏现状。
	v.SetDefault("proxy.url", "")
	v.SetDefault("proxy.no_proxy", "")
	// 运行期周期孤儿扫描：默认启用、周期 60s、只告警（可配 auto 自动清理）。
	v.SetDefault("orphan_scan.disabled", false)
	v.SetDefault("orphan_scan.interval", "60s")
	v.SetDefault("orphan_scan.dispose_policy", "warn")
	// 未纳管活进程自动收养（③）：默认启用（只重连、不杀不重启），显式 false 关闭收养。
	v.SetDefault("orphan_scan.auto_adopt", process.DefaultOrphanAutoAdopt)
	// 接管恢复（①）：接管存活 wrapper 的 reconnect 重试窗口默认 1s→...→64s（≈127s），
	// 覆盖分钟级瞬时故障（socket 未就绪/资源紧张），避免「拨不通即处置」的误杀。
	v.SetDefault("recover.retry_backoff", "1s,2s,4s,8s,16s,32s,64s")
	// 运行期实例健康巡检与自愈：默认启用、周期 30s、只告警（可配 restart 自愈）。
	v.SetDefault("health_scan.disabled", false)
	v.SetDefault("health_scan.interval", "30s")
	v.SetDefault("health_scan.probe_kind", "")
	v.SetDefault("health_scan.suspicion_threshold", 3)
	v.SetDefault("health_scan.action", "warn")
	v.SetDefault("health_scan.circuit_breaker_threshold", 5)
	v.SetDefault("health_scan.circuit_breaker_window", "10m")

	if path != "" {
		v.SetConfigFile(path)
	} else {
		// .yml 优先、找不到回退 .yaml。viper 默认搜索按 SupportedExts 顺序（yaml 先于 yml），
		// 无法据此让 .yml 优先；故显式按 [.yml, .yaml] 在搜索目录探测，命中即 SetConfigFile。
		// 两者同为 YAML 格式，SetConfigType 固定 yaml 保证解析正确。
		v.SetConfigName("worker")
		v.SetConfigType("yaml")
		// 搜索目录：cwd > exe 旁 > configs（FIX-3：服务/从别处启动致 cwd 非安装目录时仍能找到二进制旁配置）。
		dirs := configSearchDirs()
		for _, d := range dirs {
			v.AddConfigPath(d)
		}
		if found := FindConfigFile("worker", dirs...); found != "" {
			v.SetConfigFile(found)
		}
	}

	v.SetEnvPrefix("JIANMANAGER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	// 显式绑定与历史 main.go 不同名的环境变量，保持向后兼容。
	if err := v.BindEnv("name", "JIANMANAGER_NODE_NAME"); err != nil {
		return nil, fmt.Errorf("绑定节点名称环境变量失败: %w", err)
	}
	// 正式键 CONTROL_PLANE_GRPC；别名 CONTROL_PLANE 兼容旧 compose/文档误写。
	if err := v.BindEnv("control_plane", "JIANMANAGER_CONTROL_PLANE_GRPC", "JIANMANAGER_CONTROL_PLANE"); err != nil {
		return nil, fmt.Errorf("绑定 Control Plane 环境变量失败: %w", err)
	}
	if err := v.BindEnv("data_dir", "JIANMANAGER_DATA_DIR"); err != nil {
		return nil, fmt.Errorf("绑定数据目录环境变量失败: %w", err)
	}
	if err := v.BindEnv("servers_dir", "JIANMANAGER_WORK_DIR"); err != nil {
		return nil, fmt.Errorf("绑定工作目录环境变量失败: %w", err)
	}
	if err := v.BindEnv("enroll_token", "JIANMANAGER_ENROLL_TOKEN"); err != nil {
		return nil, fmt.Errorf("绑定注册令牌环境变量失败: %w", err)
	}

	readErr := v.ReadInConfig()
	if readErr != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(readErr, &notFound) && !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("读取 Worker 配置失败: %w", readErr)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	// 只有**真的读到并应用**了文件才记录路径：显式指定了不存在的路径（或自动查找落空）时留空，
	// 让启动日志一眼看出「本次启动没有任何配置文件生效」（那正是「改了 yml 却没生效」的现场）。
	if readErr == nil {
		if used := v.ConfigFileUsed(); used != "" {
			if abs, absErr := filepath.Abs(used); absErr == nil {
				cfg.configPath = abs
			} else {
				cfg.configPath = used
			}
		}
	}
	// `log_ingest.sampling.*` 的校验已随采样下线：新采集器没有采样实现，
	// 这组键配了不再产生效果（旧 worker.yml 留着不报错，viper 忽略未知键）。
	// 静态采集源的**目标形态**校验（非法即拒）：写错命名空间的源会被照常采集，
	// 却永远匹配不到 CP 的目标枚举，最终表现为「日志查询服务未启用」这类误导性状态。
	// 见 validateLogSources 的文档注释（含 2026-10-17 dev 实测现场）。
	if err := validateLogSources(cfg.LogSources); err != nil {
		return nil, fmt.Errorf("log_sources: %w", err)
	}
	// **无条件校验保留期一致性**（2026-10-21 架构复审）：旧实现在 `Enabled` 为假时
	// 整段跳过，于是最危险的组合「搬运关闭 + VL retention 有限」**完全不报错**
	// ⇒ 热层数据到期被 VictoriaLogs **直接删除**，不写审计、不经「删前归档」、
	// 不受最后副本保护，而且没人知道。
	//
	// 这里分成两种处置：
	//   · `Enabled=false`（默认）：**只告警不拒绝**——这是出厂默认，直接拒启动会让
	//     所有既有部署起不来；但必须把「数据会在 N 天后消失」说清楚（见下方 Warn）。
	//   · `Enabled=true`：保留期必须严格长于热层窗口并留搬运余量，否则拒绝启动。
	{
		policy, err := cfg.RetentionPolicy()
		if err != nil {
			return nil, err
		}
		if err := cfg.validateRuntimeRetention(policy); err != nil {
			if cfg.LogRetention.Enabled {
				return nil, err
			}
			slog.Warn("保留期配置不一致，但保留策略未启用（不阻断启动）：数据到期将由 VictoriaLogs 直接删除",
				"error", err, "hint", "设置 log_retention.enabled: true 以启用到期处理")
		}
		if !cfg.LogRetention.Enabled {
			slog.Warn("日志自动保留关闭：原生有限 TTL 仍独立淘汰；关闭应用删除不等于永久保留或已有副本",
				"automaticRetention", false, "nativeRetention", cfg.LogVL.RetentionPeriod,
				"applicationDeleteEnabled", cfg.LogVL.DeleteEnabled)
		} else {
			slog.Info("日志保留策略已启用：到期分区只能导出独立副本或保留原物",
				"nativeRetention", cfg.LogVL.RetentionPeriod,
				"copyVerifyBudget", cfg.LogRetention.Migration.CopyVerifyBudget,
				"recoveryBudget", cfg.LogRetention.Migration.RecoveryBudget)
		}
	}
	if _, err := cfg.LogVL.RuntimeBudget(); err != nil {
		return nil, err
	}
	if err := cfg.LogVL.ValidateDiskLimit(); err != nil {
		return nil, err
	}
	if err := cfg.LogVL.ValidateStreamFields(); err != nil {
		return nil, err
	}
	if cfg.EnrollToken == "" && strings.TrimSpace(cfg.EnrollTokenFile) != "" {
		data, readErr := os.ReadFile(cfg.EnrollTokenFile)
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				return nil, fmt.Errorf("读取 enrollment token 文件失败: %w", readErr)
			}
			data = nil
		}
		if data != nil {
			token := strings.TrimSpace(string(data))
			const envPrefix = "JIANMANAGER_ENROLL_TOKEN="
			if strings.HasPrefix(token, envPrefix) {
				token = strings.TrimSpace(strings.TrimPrefix(token, envPrefix))
			}
			if token == "" {
				return nil, fmt.Errorf("enrollment token 文件为空")
			}
			cfg.EnrollToken = token
		}
	}
	// 采集时区（缺陷 C）：非法值必须在启动即拒——时区配错会让整源时间轴静默偏移，
	// 等查询结果对不上账才发现（与登记阶段拒绝非法源级时区同一取舍）。
	if tz := cfg.IngestDefaultTimeZone(); tz != "" && !logconf.IsValidTimeZone(tz) {
		return nil, fmt.Errorf("log_ingest.time_zone 非法: %q（支持 UTC/local 或 IANA 名，如 Asia/Hong_Kong）", cfg.LogIngest.TimeZone)
	}
	// 采集字符集（复审 P2-3）：非法取值必须在启动即拒——按未知字符集「回退 auto」会让
	// GBK 中文被当作非法 UTF-8 净化成替换字符，正文静默损坏，等运维在日志里发现已经晚了
	// （与源级登记拒绝非法字符集同一取舍）。
	if cs := cfg.IngestDefaultCharset(); cs != "" && !logconf.IsValidCharset(cs) {
		return nil, fmt.Errorf("log_ingest.charset 非法: %q（支持 auto/utf-8/gbk/gb18030）", cfg.LogIngest.Charset)
	}
	// 新采集器周期（键 log_tailer.scan_interval / progress_interval）：非法值启动即拒。
	if _, _, err := cfg.LogTailerIntervals(); err != nil {
		return nil, err
	}
	// 多行规则（键 log_tailer.multiline.*）：非法值启动即拒。
	// 多行配错的表现是「日志被错误拼接」而非报错——那种脏数据只能人眼发现，
	// 而发现时已经入库了。所以必须在启动时挡掉。
	if _, _, err := cfg.MultilineSettings(); err != nil {
		return nil, err
	}
	// 新旧采集器互斥的**配置面**检查（2026-11-06）：两者同时开就是同一文件读两遍。
	// 装配层已经用 if/else 保证只会构造一套，这里再加一道启动即拒是因为「配了两套」
	// 本身就是运维的误解，而它的表现（日志重复、EVENT 条数翻倍）不会指向配置。
	if cfg.LogTailer.Enabled && len(cfg.LogSources) > 0 {
		slog.Warn("log_tailer.enabled=true：旧采集链路的 log_sources 配置将被忽略（新采集器按绑定表推导路径）",
			"logSources", len(cfg.LogSources))
	}
	if cfg.LogCapacity.DegradedAtPercent <= 0 || cfg.LogCapacity.DegradedAtPercent >= 100 ||
		cfg.LogCapacity.PauseAtPercent <= cfg.LogCapacity.DegradedAtPercent || cfg.LogCapacity.PauseAtPercent > 100 {
		return nil, fmt.Errorf("log_capacity thresholds must satisfy 0 < degraded < pause <= 100")
	}
	configuredPorts := []int{cfg.LogVL.HotPort}
	seenPorts := make(map[int]bool)
	for _, port := range configuredPorts {
		if port == 0 {
			continue
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("log_vl ports must be within 1..65535")
		}
		if seenPorts[port] {
			return nil, fmt.Errorf("log_vl ports must be distinct")
		}
		seenPorts[port] = true
	}
	return &cfg, nil
}

// FindConfigFile 在给定目录中按 .yml 优先、.yaml 兼容回退的顺序查找 <name>.<ext> 配置文件。
// 返回首个存在的文件路径；都不存在时返回空串（交回 viper 的名字搜索 + 默认值，零配置仍可启动）。
//
// 导出以供 worker 入口的「未配置自检」复用：自检需判断工作目录是否已有
// worker.yml/.yaml 配置文件。
func FindConfigFile(name string, dirs ...string) string {
	for _, dir := range dirs {
		for _, ext := range []string{"yml", "yaml"} {
			p := filepath.Join(dir, name+"."+ext)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

// WorkerConfigExists 报告工作目录、可执行文件所在目录或 configs/ 下是否存在 worker 配置文件
// （.yml 优先、.yaml 回退）。供 worker 入口未配置自检使用。
// 纳入「exe 旁」搜索（FIX-3）：Windows 服务/从别处启动时 cwd 可能非安装目录，配置随二进制仍可被发现。
func WorkerConfigExists() bool {
	return FindConfigFile("worker", configSearchDirs()...) != ""
}

// executableDir 返回当前可执行文件所在目录（解析失败返回空）。声明为变量便于测试覆盖。
var executableDir = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// configSearchDirs 返回 worker 配置文件搜索目录（按优先级）：cwd "." > 可执行文件所在目录 > "configs"。
func configSearchDirs() []string {
	dirs := []string{"."}
	// 「exe 旁」搜索（FIX-3）：覆盖「服务/从别处启动致 cwd 非安装目录」时仍能发现二进制旁的 worker.yml。
	if d := executableDir(); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, "configs")
	return dirs
}

// HostObserveOptions 把配置收敛为 hostobs 的构造选项。
//
// 非法 interval 回退默认而不报错：观测配置写错不该阻断启动，
// 而 hostobs.New 内部还有 10s 硬下限兜底。
func (c *Config) HostObserveOptions() hostobs.Options {
	opts := hostobs.Options{}
	if c == nil {
		return opts
	}
	opts.TopN = c.HostObserve.TopN
	opts.Mounts = c.HostObserve.Mounts
	if d, err := time.ParseDuration(strings.TrimSpace(c.HostObserve.TopInterval)); err == nil && d > 0 {
		opts.Interval = d
	}
	return opts
}
