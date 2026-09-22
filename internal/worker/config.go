// Package config 提供 Worker Node 的配置加载（worker.yml + 环境变量覆盖）。
//
// 配置真正落盘到 worker.yml 而非堆砌 JIANMANAGER_* 环境变量（FR-080，见 ADR-020）；
// 所有项有合理默认，零配置即可启动开发环境。环境变量以 JIANMANAGER_ 前缀按路径覆盖
// （如 Control Plane gRPC 地址 → JIANMANAGER_CONTROL_PLANE_GRPC），与 Control Plane 配置惯例一致。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/internal/worker/process"
)

// Config Worker Node 配置。
type Config struct {
	Name         string   `mapstructure:"name"`
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
	EnrollToken string           `mapstructure:"enroll_token"`
	Log         LogConfig        `mapstructure:"log"`
	Decompiler  DecompilerConfig `mapstructure:"decompiler"`
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
}

// ScanInterval 解析扫描周期：非法/空回退 60s。
func (c OrphanScanConfig) ScanInterval() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.Interval))
	if err != nil || d <= 0 {
		return 60 * time.Second
	}
	return d
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
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
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

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return nil, fmt.Errorf("读取 Worker 配置失败: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
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
