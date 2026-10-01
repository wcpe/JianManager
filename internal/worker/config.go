// Package config 提供 Worker Node 的配置加载（worker.yml + 环境变量覆盖）。
//
// 配置真正落盘到 worker.yml 而非堆砌 JIANMANAGER_* 环境变量（FR-080，见 ADR-020）；
// 所有项有合理默认，零配置即可启动开发环境。环境变量以 JIANMANAGER_ 前缀按路径覆盖
// （如 Control Plane gRPC 地址 → JIANMANAGER_CONTROL_PLANE_GRPC），与 Control Plane 配置惯例一致。
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	"github.com/wcpe/JianManager/internal/worker/logs/sampling"
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
	// JDK、服务器工作目录等运行态数据统一收口到此根。参见 ADR-010。
	DataDir string `mapstructure:"data_dir"`
	// ServersDir 兼容旧配置：显式指定时覆盖数据根派生的 var/servers。
	// 留空则由数据根派生为 <DataDir>/var/servers。
	ServersDir string `mapstructure:"servers_dir"`
	// Host 注册上报给 CP 的本机地址；留空则自动探测出口 IP（供 CP 反向连接）。
	Host string `mapstructure:"host"`
	// JWTSecret 历史兼容字段；WS 令牌密钥只接受 CP 注册/心跳下发值。
	JWTSecret string `mapstructure:"jwt_secret"`
	// EnrollToken 一次性 enrollment token 明文（FR-080，见 ADR-020）。
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
	LogSources      []LogSourceConfig `mapstructure:"log_sources"`
	// LogReconcile 启动增量对账（FR-497）：按「源 × UTC 天」只补缺失天，对账不可信则回退整窗重发。
	LogReconcile LogReconcileConfig `mapstructure:"log_reconcile"`
	// LogIndex 采集索引（FR-496）的配置面：历史投递批次裁剪（§6）与持久化提交单元预算（§3.4）。
	LogIndex   LogIndexConfig   `mapstructure:"log_index"`
	LogArchive LogArchiveConfig `mapstructure:"log_archive"`
	// LogIngest 采集归一化的节点级默认（缺陷 C 时区配置面）：源未显式配置时生效。
	LogIngest LogIngestConfig `mapstructure:"log_ingest"`
	// LogRetention 保留策略（按级别/来源设保留期）与执行器。
	LogRetention LogRetentionConfig `mapstructure:"log_retention"`
	Decompiler   DecompilerConfig   `mapstructure:"decompiler"`
	// Search 全文搜索索引配置（FR-074，见 ADR-017）。
	Search SearchConfig `mapstructure:"search"`
	// ArtifactCache 节点本地制品缓存配置（FR-178）：按 sha256 缓存下载过的核心 jar，建实例命中即秒拷。
	ArtifactCache ArtifactCacheConfig `mapstructure:"artifact_cache"`
	// Proxy 本节点出站代理配置（FR-174，见 ADR-037）：所有出站下载（自更新/JDK/CFR）
	// 经此代理。url 留空=直连（沿用环境变量代理）。各 Worker 在不同机器各配各的。
	Proxy     httpclient.Config `mapstructure:"proxy"`
	Heartbeat time.Duration     `mapstructure:"-"`
	// MemoryGuard 启动内存闸（FR-317）：可用内存不足以再塞下待启实例时拒绝启动，
	// 防止把节点内存跑满至失去响应。零值即启用 + 默认保留水位 max(512MB, 总内存 10%)。
	MemoryGuard MemoryGuardConfig `mapstructure:"memory_guard"`
	// BotWorker bot-worker 子进程容量与资源参数（FR-398 压测编排）。
	BotWorker BotWorkerConfig `mapstructure:"bot_worker"`
	// OrphanScan 运行期周期孤儿扫描（FR-456）：把孤儿清理从「仅启动时」升级为「运行期持续兜底」。
	OrphanScan OrphanScanConfig `mapstructure:"orphan_scan"`
	// Recover 接管恢复参数（FR-455①）：Worker 重启后对存活 wrapper 的 reconnect 重试窗口。
	Recover RecoverConfig `mapstructure:"recover"`
	// HealthScan 运行期实例健康巡检与自愈（FR-459）：识别假死、受控自愈、崩溃熔断。
	HealthScan HealthScanConfig `mapstructure:"health_scan"`
}

// HealthScanConfig 运行期实例健康巡检与自愈配置（FR-459）。
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
// 避免「本地默认」与「进程归一默认」两处漂移（FR-459 复审项 12）。

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

// OrphanScanConfig 运行期周期孤儿扫描配置（FR-456）。
type OrphanScanConfig struct {
	// Disabled 显式关闭周期扫描（默认 false=启用）。应急逃生口。
	Disabled bool `mapstructure:"disabled"`
	// Interval 扫描周期（time.ParseDuration 字符串，默认 60s）。
	Interval string `mapstructure:"interval"`
	// DisposePolicy 处置策略：warn（默认，只告警 + 落审计）/ auto（自动清理）。
	DisposePolicy string `mapstructure:"dispose_policy"`
	// AutoAdopt 未纳管活进程全自动收养（FR-497③，默认 true=启用）。
	//
	// 扫描发现「PID 目录中 wrapper 与 Java 均活、但本 Worker 未纳管」的实例时，经归属复核
	// （verifyProcessOwnership）通过即自动重连并登记为 RUNNING——**不重启也不杀进程**；复核不通过
	// 只告警。显式 false 只关收养，其余三态孤儿扫描与告警不变；见
	// docs/specs/auto-adopt-orphans/spec.md。默认口径的单一真源为 process.DefaultOrphanAutoAdopt。
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

// AutoAdoptEnabled 返回未纳管活进程自动收养开关（FR-497③）。
//
// 与 RetryBackoffSequence 同口径：本类型的零值（测试/手工构造）按默认口径解释——config.Load 经
// viper SetDefault 写入 process.DefaultOrphanAutoAdopt，故只有显式配置 false 才会关闭。
// 注意：本类型的零值 AutoAdopt=false 无法与「显式关闭」区分，故装配点须用本方法而非直接读字段。
func (c OrphanScanConfig) AutoAdoptEnabled() bool {
	return c.AutoAdopt
}

// LogReconcileConfig 启动增量对账（FR-497）配置面，键为 `log_reconcile.*`。
//
// 默认口径的单一真源在 ingest 包（ingest.DefaultReconcileConfig：默认启用，并发 4、单源总超时
// 30s、单次 count 查询 10s）；本类型只做 YAML → ingest.Options 的搬运，非法/非正值一律回退该
// 默认——配置误写不得让对账退化为无超时或高并发，关闭只能走显式 `log_reconcile.enabled: false`
// （该源回退整窗重发）。见 docs/specs/log-startup-reconcile/spec.md §5。
type LogReconcileConfig struct {
	// Enabled 启动增量对账开关（默认 true）。false = 放弃增量裁剪、该源回退整窗重发（应急逃生口）。
	Enabled bool `mapstructure:"enabled"`
	// Concurrency 并发对账的源数上限（默认 4）；非正回退默认，超上限时由 ingest 收敛到 32。
	Concurrency int `mapstructure:"concurrency"`
	// Timeout 单源对账总超时（duration 字符串，默认 30s）；非法/非正回退默认。
	Timeout string `mapstructure:"timeout"`
	// QueryTimeout 单天 count 查询超时（duration 字符串，默认 10s）；非法/非正回退默认。
	QueryTimeout string `mapstructure:"query_timeout"`
	// Budget 整批对账的总预算（duration 字符串，默认 60s）；非法/非正回退默认。
	//
	// 为什么需要它（复审 P2-10）：对账在 ingest.New 里**同步**执行，而单源超时 30s × 60 源 /
	// 并发 4 ≈ 7.5 分钟的最坏情况会把启动拖成分钟级。预算到期即取消在途查询，未完成的源
	// 回退整窗重发（只影响「重发多少」，不影响「要不要重发」）。
	Budget string `mapstructure:"budget"`
}

// ReconcileConfig 把本地配置面收敛为 ingest 的对账配置（非法/非正一律回退归一化默认）。
//
// 零值语义：Enabled=false 的零值无法与「显式关闭」区分，故 Load 经 viper SetDefault 写入默认
// true（与 orphan_scan.auto_adopt 同口径）；手工构造 Config{} 的调用方须自行置位。
func (c LogReconcileConfig) ReconcileConfig() ingest.ReconcileConfig {
	out := ingest.DefaultReconcileConfig()
	out.Enabled = c.Enabled
	if c.Concurrency > 0 {
		out.Concurrency = c.Concurrency
	}
	if d, err := time.ParseDuration(strings.TrimSpace(c.Timeout)); err == nil && d > 0 {
		out.Timeout = d
	}
	if d, err := time.ParseDuration(strings.TrimSpace(c.QueryTimeout)); err == nil && d > 0 {
		out.QueryTimeout = d
	}
	if d, err := time.ParseDuration(strings.TrimSpace(c.Budget)); err == nil && d > 0 {
		out.Budget = d
	}
	return out
}

// LogIndexConfig 采集索引（FR-496）的配置面，键为 `log_index.*`。
//
// 默认口径的单一真源在两个实现包里：ledger.DefaultDeliveryBatchPruneConfig（默认开启裁剪）
// 与 stateindex.DefaultCommitBudget（每提交单元 512 行、目标 40ms）。本类型只做 YAML →
// ingest.Options 的搬运，非法值一律回退默认——配置误写不得让裁剪失效或让切分消失
// （那会把「单次持久化 ≤50ms」的达标线交还给配置运气）。见
// docs/specs/log-index-sqlite/spec.md §3.4（切分）与 §6（配置键登记）。
type LogIndexConfig struct {
	// BatchPrune 历史投递批次（delivery_batch）裁剪：索引有界化的唯一裁剪路径。
	BatchPrune LogIndexBatchPruneConfig `mapstructure:"batch_prune"`
	// Persist 持久化提交单元预算：单次持久化按行数 + 耗时双上界切成多个提交单元。
	Persist LogIndexPersistConfig `mapstructure:"persist"`
}

// LogIndexBatchPruneConfig 是历史投递批次裁剪的配置面（键 `log_index.batch_prune.*`）。
//
// 零值语义：Enabled=false 的零值无法与「显式关闭」区分（与 log_reconcile.enabled、
// orphan_scan.auto_adopt 同口径），故 Load 经 viper SetDefault 写入默认 true；
// 手工构造 Config{} 的调用方须自行置位。
type LogIndexBatchPruneConfig struct {
	// Enabled 为 false 时完全不裁（应急逃生口：怀疑裁剪影响判定时先关它）。
	Enabled bool `mapstructure:"enabled"`
	// KeepRecent 是无条件保留的最近批次条数（审计尾窗），只多留不少留；负数为误写 → 回退默认。
	KeepRecent int `mapstructure:"keep_recent"`
}

// DeliveryBatchPruneConfig 把本地配置面收敛为 ledger 的裁剪配置（负尾窗回退默认 0）。
func (c LogIndexBatchPruneConfig) DeliveryBatchPruneConfig() ledger.DeliveryBatchPruneConfig {
	return ledger.DeliveryBatchPruneConfig{Enabled: c.Enabled, KeepRecent: c.KeepRecent}.Normalized()
}

// LogIndexPersistConfig 是索引持久化提交单元预算的配置面（键 `log_index.persist.*`）。
type LogIndexPersistConfig struct {
	// MaxTxRows 单个提交单元（一次 IMMEDIATE 事务）的行数上限；非正回退默认 512
	// （真源是 stateindex.DefaultCommitMaxRows）。
	MaxTxRows int `mapstructure:"max_tx_rows"`
	// MinTxRows 自适应收缩的下限（再慢也不退化成逐行）；非正/大于上限时回退默认 64。
	MinTxRows int `mapstructure:"min_tx_rows"`
	// TxDurationTarget 单个提交单元的耗时目标（duration 字符串，默认 40ms）；非法/非正回退默认。
	// 执行中的耗时硬上界取它的 5/4（默认 50ms = 验收线「单次持久化 ≤50ms」本身）。
	TxDurationTarget string `mapstructure:"tx_duration_target"`
}

// CommitBudget 把本地配置面收敛为 stateindex 的提交单元预算（非法值一律回退归一化默认）。
func (c LogIndexPersistConfig) CommitBudget() stateindex.CommitBudget {
	out := stateindex.DefaultCommitBudget()
	if c.MaxTxRows > 0 {
		out.MaxRows = c.MaxTxRows
	}
	if c.MinTxRows > 0 {
		out.MinRows = c.MinTxRows
	}
	if d, err := time.ParseDuration(strings.TrimSpace(c.TxDurationTarget)); err == nil && d > 0 {
		out.Target = d
	}
	return out.Normalized()
}

// IndexPrune 返回可直接交给 ingest.Options.IndexPrune 的裁剪配置。
func (c LogIndexConfig) IndexPrune() ledger.DeliveryBatchPruneConfig {
	return c.BatchPrune.DeliveryBatchPruneConfig()
}

// CommitBudget 返回可直接交给 ingest.Options.IndexCommit 的提交单元预算。
func (c LogIndexConfig) CommitBudget() stateindex.CommitBudget { return c.Persist.CommitBudget() }

// RecoverConfig 接管恢复（Worker 重启后接管存活 wrapper）配置（FR-455①）。
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

// MemoryGuardConfig 启动内存闸配置（FR-317）。
type MemoryGuardConfig struct {
	// ReserveMB 保留水位（MB）；0 = 默认策略 max(512MB, 总内存 10%)。
	ReserveMB int64 `mapstructure:"reserve_mb"`
	// Disabled 显式关闭守卫（应急逃生口，如误判阻塞关键启动时）。
	Disabled bool `mapstructure:"disabled"`
}

// SearchConfig 全文搜索索引配置（FR-074，见 ADR-017）。
type SearchConfig struct {
	// Ignore 追加到内置默认忽略集的 glob 规则（相对实例工作目录，/ 分隔）。
	// 形如 logs/（目录前缀）、*.bak（basename glob）、vendor（路径段）。默认集已覆盖常见
	// 日志/缓存/二进制/归档/MC 世界数据，零配置即可用；此处仅做加性补充。
	Ignore []string `mapstructure:"ignore"`
}

// ArtifactCacheConfig 节点本地制品缓存配置（FR-178）。
type ArtifactCacheConfig struct {
	// MaxBytes 缓存容量上限（字节，0=不限）。存入新项后若超限按 lastUsedAt 升序 LRU 淘汰。
	// 可经 CP 端点 PUT /nodes/:id/artifact-cache/cap 运行时下发覆盖。
	MaxBytes int64 `mapstructure:"max_bytes"`
}

// DecompilerConfig 反编译能力配置（FR-075，见 ADR-018）。
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
}

// LogQueryConfig configures the optional localhost VictoriaLogs query client.
// Empty VLURL deliberately keeps the explicit LOG_UNSUPPORTED compatibility path.
type LogQueryConfig struct {
	VLURL    string `mapstructure:"vl_url"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

// LogVLConfig enables the managed HOT/COLD/Rehydrate supervisor. Empty BinaryPath
// keeps externally managed localhost VL compatibility mode.
type LogVLConfig struct {
	BinaryPath      string `mapstructure:"binary_path"`
	PackagePath     string `mapstructure:"package_path"`
	AssetSHA256     string `mapstructure:"asset_sha256"`
	Username        string `mapstructure:"username"`
	Password        string `mapstructure:"password"`
	DataRoot        string `mapstructure:"data_root"`
	RetentionPeriod string `mapstructure:"retention_period"`
	HotCacheBytes   int64  `mapstructure:"hot_cache_bytes"`
	// MemoryLimitBytes 受管 VL 进程 Go 软内存上限（GOMEMLIMIT，字节）；0 用默认 512MiB，负值不注入。
	MemoryLimitBytes int64 `mapstructure:"memory_limit_bytes"`
	HotPort          int   `mapstructure:"hot_port"`
	ColdPort         int   `mapstructure:"cold_port"`
	RehydratePort    int   `mapstructure:"rehydrate_port"`
	StartCold        bool  `mapstructure:"start_cold"`
	StartRehydrate   bool  `mapstructure:"start_rehydrate"`
}

// DefaultMaxWALBytes 是单源 WAL 字节预算的**有限默认值**。
//
// 此前默认是 0 = 不限，于是唯一的体积防护形同不存在：压测实测单 Worker WAL 涨到 1.9GB
// 而容量门禁的 PASSIVE 分支从不触发（「不限」不是「很宽」，是「没有上界」）。
//
// 取 512MiB 的依据：稳态 WAL 在 reclaim 正常推进后是 KB 量级（真机实测 107MB → 256B），
// 只有 reclaim 停滞时才会累积到百 MB 级。故 512MiB 远高于任何正常态，
// 只在真的失控时才触发——触发后的动作是 PAUSED + 登记缺口（不静默丢），正合「宁停不丢」。
//
// 0 仍可作为显式逃生阀（真的需要不限时写 0），但会在启动日志里被点名提醒。
const DefaultMaxWALBytes uint64 = 512 << 20

type LogCapacityConfig struct {
	MaxWALBytes       uint64  `mapstructure:"max_wal_bytes"`
	MaxGaps           int     `mapstructure:"max_gaps"`
	DegradedAtPercent float64 `mapstructure:"degraded_at_percent"`
	PauseAtPercent    float64 `mapstructure:"pause_at_percent"`
}

// LogSourceConfig 配置一个由 Worker 常驻采集的日志源。
// 实例生命周期可以在创建/迁移时动态登记同一模型；配置源用于 Worker/Node 日志和离线部署。
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
	// Sampling 采集侧采样与降级策略（键 log_ingest.sampling.*）。
	//
	// 挂在 log_ingest 下而不是顶层：它与时区/字符集同属「采集归一化」的节点级默认，
	// 作用域与装配点完全一致（都在构造 ingest.Options 时接线）。
	Sampling LogSamplingConfig `mapstructure:"sampling"`
}

// LogSamplingConfig 采集侧采样与降级策略的配置面（键 `log_ingest.sampling.*`）。
//
// 默认**全关**：本项一旦启用就会改变「哪些日志被存下来」，属于必须由运维显式开启的能力。
// 取值口径与 retention 一致（0 = 没填走默认，负数 = 写错启动即拒）——
// 采样配错的后果是日志静默少存，现场只会表现为「查不到」，不会表现为报错。
type LogSamplingConfig struct {
	// Enabled 总开关。
	Enabled bool `mapstructure:"enabled"`
	// MinLevel 等级过滤：低于该级别的原文折叠为汇总事件。空串 = 不启用。
	// 无法归类的级别一律放行。
	MinLevel string `mapstructure:"min_level"`
	// BurstWindow / BurstThreshold 同源同消息高频抑制：窗口内前 N 条放行原文，
	// 其余合并为汇总事件（含条数与样例）。BurstWindow 为 0 = 不启用。
	BurstWindow    time.Duration `mapstructure:"burst_window"`
	BurstThreshold int           `mapstructure:"burst_threshold"`
	// MaxSignatures 模式表上限（有界：采样自身不得成为无界增长点）。
	MaxSignatures int `mapstructure:"max_signatures"`
	// BudgetMaxEventsPerWindow / BudgetWindow / BudgetKeepEvery 每源预算：
	// 窗口内允许原文落库 N 条，超出后每 KeepEvery 条保留 1 条原文，其余折叠。
	BudgetMaxEventsPerWindow int           `mapstructure:"budget_max_events_per_window"`
	BudgetWindow             time.Duration `mapstructure:"budget_window"`
	BudgetKeepEvery          int           `mapstructure:"budget_keep_every"`
	// MaxAggregateEvents 单条汇总事件承接的原文条数上限。
	MaxAggregateEvents int `mapstructure:"max_aggregate_events"`
	// DegradeEnabled 风暴自动降级开关；触发源复用容量门禁的磁盘读数（同一真源）。
	DegradeEnabled     bool          `mapstructure:"degrade_enabled"`
	DegradeTargetLevel string        `mapstructure:"degrade_target_level"`
	DegradeDiskPercent float64       `mapstructure:"degrade_disk_percent"`
	DegradeHold        time.Duration `mapstructure:"degrade_hold"`
}

// SamplingPolicy 装配为 pipeline.Options.Sampling / ingest.Options.Sampling。
//
// 独立成方法而不是在装配点内联读字段：这条接线一旦断掉，运维会以为「已经开了采样」，
// 而实际上一条都没压，成本问题会以「配了没用」的形态长期挂着——
// 与 IngestDefaultTimeZone/Charset 同一条理由（配置 → 实现之间没有任何编译期约束）。
func (c *Config) SamplingPolicy() sampling.Policy {
	if c == nil {
		return sampling.Policy{}
	}
	s := c.LogIngest.Sampling
	return sampling.Policy{
		Enabled: s.Enabled,
		Level:   sampling.LevelFilter{MinLevel: s.MinLevel},
		Burst: sampling.Burst{
			Window:        s.BurstWindow,
			Threshold:     s.BurstThreshold,
			MaxSignatures: s.MaxSignatures,
		},
		Budget: sampling.Budget{
			MaxEventsPerWindow: s.BudgetMaxEventsPerWindow,
			Window:             s.BudgetWindow,
			KeepEvery:          s.BudgetKeepEvery,
		},
		Degrade: sampling.Degrade{
			Enabled:     s.DegradeEnabled,
			TargetLevel: s.DegradeTargetLevel,
			DiskPercent: s.DegradeDiskPercent,
			Hold:        s.DegradeHold,
		},
		MaxAggregateEvents: s.MaxAggregateEvents,
	}
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
	// HotRetention 热层保留窗口（超期即搬运到冷层）。0 = 用默认 30d（与受管 VL 的
	// -retentionPeriod 对齐，热层保持现状、长留靠冷层）。
	HotRetention time.Duration `mapstructure:"hot_retention"`
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
		Enabled:      cfg.Enabled,
		Discard:      cfg.Discard,
		HotRetention: cfg.HotRetention,
		Sweep: retention.Sweep{
			VLSweep:  cfg.Sweep.VLSweep,
			Interval: cfg.Sweep.Interval,
			Timeout:  cfg.Sweep.Timeout,
		},
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

// WALBudgetNotice 返回「WAL 预算被显式关掉」的提醒（无限时返回空串）。
//
// 为什么不直接拒绝：0 = 不限是合法配置（例如受管环境由外部配额兜底），
// 但它是**唯一没有上界的形态**，必须在启动日志里被点名，否则「没配」与「配成不限」
// 在现场看起来完全一样——两者都由 viper 默认值与显式 0 落到同一个值上。
func (c *Config) WALBudgetNotice() string {
	if c == nil || c.LogCapacity.MaxWALBytes != 0 {
		return ""
	}
	return "log_capacity.max_wal_bytes=0（不限）：WAL 将没有任何字节上界，reclaim 停滞后会一路涨到下盘满，请确认这是有意为之"
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
}

// Load 从配置文件和环境变量加载 Worker 配置（FR-080，见 ADR-020）。
//
// path 为空时在工作目录与 configs/ 下查找 worker.yml（均可选），找不到回退 worker.yaml（FR-224 兼容）。
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
	v.SetDefault("log_vl.retention_period", "30d")
	v.SetDefault("log_vl.hot_cache_bytes", int64(512*1024*1024))
	v.SetDefault("log_vl.hot_port", 0)
	v.SetDefault("log_vl.cold_port", 0)
	v.SetDefault("log_vl.rehydrate_port", 0)
	v.SetDefault("log_vl.start_cold", false)
	v.SetDefault("log_vl.start_rehydrate", false)
	v.SetDefault("log_capacity.max_wal_bytes", DefaultMaxWALBytes)
	v.SetDefault("log_capacity.max_gaps", 0)
	v.SetDefault("log_capacity.degraded_at_percent", 80.0)
	v.SetDefault("log_capacity.pause_at_percent", 90.0)
	v.SetDefault("log_sources", []LogSourceConfig{})
	// 启动增量对账（FR-497）：默认值直接取自 ingest 的单一真源，不在本地重复字面量。
	reconcileDefaults := ingest.DefaultReconcileConfig()
	v.SetDefault("log_reconcile.enabled", reconcileDefaults.Enabled)
	v.SetDefault("log_reconcile.concurrency", reconcileDefaults.Concurrency)
	v.SetDefault("log_reconcile.timeout", reconcileDefaults.Timeout.String())
	v.SetDefault("log_reconcile.query_timeout", reconcileDefaults.QueryTimeout.String())
	v.SetDefault("log_reconcile.budget", reconcileDefaults.Budget.String())
	// 采集索引（FR-496）：历史投递批次裁剪 + 持久化提交单元预算。默认值同样取自实现包的单一真源。
	indexPruneDefaults := ledger.DefaultDeliveryBatchPruneConfig()
	indexCommitDefaults := stateindex.DefaultCommitBudget()
	v.SetDefault("log_index.batch_prune.enabled", indexPruneDefaults.Enabled)
	v.SetDefault("log_index.batch_prune.keep_recent", indexPruneDefaults.KeepRecent)
	v.SetDefault("log_index.persist.max_tx_rows", indexCommitDefaults.MaxRows)
	v.SetDefault("log_index.persist.min_tx_rows", indexCommitDefaults.MinRows)
	v.SetDefault("log_index.persist.tx_duration_target", indexCommitDefaults.Target.String())
	v.SetDefault("log_archive.enabled", false)
	v.SetDefault("log_archive.provider", "local")
	v.SetDefault("log_archive.endpoint", "")
	v.SetDefault("log_archive.bucket", "")
	v.SetDefault("log_archive.region", "us-east-1")
	v.SetDefault("log_archive.prefix", "logs")
	v.SetDefault("log_archive.access_key", "")
	v.SetDefault("log_archive.secret_key", "")
	// 采集归一化的节点级默认时区（缺陷 C）：留空 = UTC，未配置时零行为变化。
	// 中文 locale 的 JVM 与 Worker 同机部署时配 `local` 即可对齐本地时间。
	v.SetDefault("log_ingest.time_zone", "")
	// 采集归一化的节点级默认字符集（复审 P2-3）：留空 = auto。中文 locale 的 JVM 与 Worker
	// 同机部署且日志为 GBK 时配 gbk 即可（显式声明优于自动判定）。
	v.SetDefault("log_ingest.charset", "")
	v.SetDefault("search.ignore", []string{})
	// 节点制品缓存（FR-178）：默认 0=不限（建实例命中即秒拷免重下；按需经 CP 设上限触发 LRU）。
	v.SetDefault("artifact_cache.max_bytes", int64(0))
	// 反编译（FR-075）：默认无显式 CFR 路径、允许按需下载（首次反编译时拉 CFR 落数据根缓存）。
	v.SetDefault("decompiler.cfr_path", "")
	v.SetDefault("decompiler.allow_download", true)
	// 出站代理（FR-174，见 ADR-037）：默认空（直连/沿用环境变量代理），不破坏现状。
	v.SetDefault("proxy.url", "")
	v.SetDefault("proxy.no_proxy", "")
	// 运行期周期孤儿扫描（FR-456）：默认启用、周期 60s、只告警（可配 auto 自动清理）。
	v.SetDefault("orphan_scan.disabled", false)
	v.SetDefault("orphan_scan.interval", "60s")
	v.SetDefault("orphan_scan.dispose_policy", "warn")
	// 未纳管活进程自动收养（FR-497③）：默认启用（只重连、不杀不重启），显式 false 关闭收养。
	v.SetDefault("orphan_scan.auto_adopt", process.DefaultOrphanAutoAdopt)
	// 接管恢复（FR-455①）：接管存活 wrapper 的 reconnect 重试窗口默认 1s→...→64s（≈127s），
	// 覆盖分钟级瞬时故障（socket 未就绪/资源紧张），避免「拨不通即处置」的误杀。
	v.SetDefault("recover.retry_backoff", "1s,2s,4s,8s,16s,32s,64s")
	// 运行期实例健康巡检与自愈（FR-459）：默认启用、周期 30s、只告警（可配 restart 自愈）。
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
		// .yml 优先、找不到回退 .yaml（FR-224）。viper 默认搜索按 SupportedExts 顺序（yaml 先于 yml），
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
	// 正式键 CONTROL_PLANE_GRPC；别名 CONTROL_PLANE 兼容旧 compose/文档误写（FR-354）。
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
	if tz := cfg.IngestDefaultTimeZone(); tz != "" && !ingest.IsValidTimeZone(tz) {
		return nil, fmt.Errorf("log_ingest.time_zone 非法: %q（支持 UTC/local 或 IANA 名，如 Asia/Hong_Kong）", cfg.LogIngest.TimeZone)
	}
	// 采集字符集（复审 P2-3）：非法取值必须在启动即拒——按未知字符集「回退 auto」会让
	// GBK 中文被当作非法 UTF-8 净化成替换字符，正文静默损坏，等运维在日志里发现已经晚了
	// （与源级登记拒绝非法字符集同一取舍）。
	if cs := cfg.IngestDefaultCharset(); cs != "" && !ingest.IsValidCharset(cs) {
		return nil, fmt.Errorf("log_ingest.charset 非法: %q（支持 auto/utf-8/gbk/gb18030）", cfg.LogIngest.Charset)
	}
	if cfg.LogCapacity.DegradedAtPercent <= 0 || cfg.LogCapacity.DegradedAtPercent >= 100 ||
		cfg.LogCapacity.PauseAtPercent <= cfg.LogCapacity.DegradedAtPercent || cfg.LogCapacity.PauseAtPercent > 100 {
		return nil, fmt.Errorf("log_capacity thresholds must satisfy 0 < degraded < pause <= 100")
	}
	configuredPorts := []int{cfg.LogVL.HotPort, cfg.LogVL.ColdPort, cfg.LogVL.RehydratePort}
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

// FindConfigFile 在给定目录中按 .yml 优先、.yaml 兼容回退的顺序查找 <name>.<ext> 配置文件（FR-224）。
// 返回首个存在的文件路径；都不存在时返回空串（交回 viper 的名字搜索 + 默认值，零配置仍可启动）。
//
// 导出以供 worker 入口的「未配置自检」复用（FR-222，见 ADR-051）：自检需判断工作目录是否已有
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
// （.yml 优先、.yaml 回退）。供 worker 入口未配置自检使用（FR-222，见 ADR-051）。
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
