package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/directprobe"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/internal/platform/trafficlimit"
)

// 平台设置可写白名单键。
// 只有这些键允许经 PUT /settings 落库覆盖，其余键一律拒绝。
const (
	// SettingKeyLogLevel CP 日志级别（debug|info|warn|error）。落库即时生效（slog LevelVar）。
	SettingKeyLogLevel = "log.level"
	// SettingKeyDebugMode 调试模式总开关（true|false增强）。落库即时生效：
	// 开=日志 debug + Gin debug 模式；关=日志回 log.level 基线 + Gin release（默认 false，启动即静默）。
	SettingKeyDebugMode = "debug.mode"
	// SettingKeyJDKMirrorTemurin / Corretto / Zulu JDK 下载镜像源基址。
	// 安装 JDK 时 CP 取生效值经 InstallJDKRequest.mirror_base 下发 Worker，使配置真生效。
	SettingKeyJDKMirrorTemurin  = "jdk.mirror.temurin"
	SettingKeyJDKMirrorCorretto = "jdk.mirror.corretto"
	SettingKeyJDKMirrorZulu     = "jdk.mirror.zulu"
	// SettingKeyRuntimeMirrorNodeJS Node.js dist 下载镜像源基址（语义同 jdk.mirror.*）。
	// 安装 Node.js 时 CP 取生效值经 InstallRuntimeRequest.mirror_base 下发 Worker。
	SettingKeyRuntimeMirrorNodeJS = "runtime.mirror.nodejs"
	// SettingKeyGracefulStopTimeout 优雅停止超时（Go duration 文本）。
	// 启动实例时 CP 取生效值经 CreateInstanceRequest 下发 Worker→wrapper，对其后新启动的实例生效。
	SettingKeyGracefulStopTimeout = "graceful_stop.timeout"
	// SettingKeyDirectProbeSLPTimeout / QueryTimeout 是 MC 直探（SLP / Query）超时（Go duration 文本）。
	// 心跳响应按拍下发（HeartbeatResponse.direct_probe_*_timeout_ms），Worker 填入采集编排链；
	// 与 graceful_stop.timeout 同风格：DB 覆盖 > 基线默认，Worker 侧生效、无需重启。
	SettingKeyDirectProbeSLPTimeout   = "direct_probe.slp_timeout"
	SettingKeyDirectProbeQueryTimeout = "direct_probe.query_timeout"
	// SettingKeyHealth* 是实例健康巡检与自愈策略：心跳响应按拍下发
	// （HeartbeatResponse.health_*），Worker 写入巡检器生效值；与 direct_probe.* 同风格
	// （DB 覆盖 > 基线默认，Worker 侧生效、无需重启）。
	//   - scan_enabled 巡检总开关（默认 true）；
	//   - scan_interval 巡检周期（默认 30s）；
	//   - probe_kind 响应维度探针类型 tcp|http|空(auto)；
	//   - suspicion_threshold 连续判假死次数阈值（默认 3）；
	//   - action 假死动作 warn（默认）|restart；
	//   - circuit_breaker_threshold 熔断窗口内重启次数阈值（默认 5）；
	//   - circuit_breaker_window 熔断滚动窗口（默认 10m）；
	// startup_warmup 启动宽限期（默认 5m复审项 2：慢启动 MC 不被误判假死）；
	//   - self_heal_max_restarts 假死自愈在熔断窗口内的重启次数上限（默认 3，复审项 7）。
	SettingKeyHealthScanEnabled         = "health.scan_enabled"
	SettingKeyHealthScanInterval        = "health.scan_interval"
	SettingKeyHealthProbeKind           = "health.probe_kind"
	SettingKeyHealthSuspicionThreshold  = "health.suspicion_threshold"
	SettingKeyHealthAction              = "health.action"
	SettingKeyHealthCircuitThreshold    = "health.circuit_breaker_threshold"
	SettingKeyHealthCircuitWindow       = "health.circuit_breaker_window"
	SettingKeyHealthStartupWarmup       = "health.startup_warmup"
	SettingKeyHealthSelfHealMaxRestarts = "health.self_heal_max_restarts"
	// === 日志采集配置（平台默认；Worker 级覆盖见 NodeScopedSettingKey）===
	// 语义与 Worker 侧 worker.yml 基线一致：规则引擎与实时采集默认开。
	// gz 导入总开关**默认开**（2026-10-15 定案）：默认关会让 worker.yml 基线为「开」的
	// 存量部署在升级后静默失去定时扫描；定时扫描的**周期**则默认留空 = 未配置，
	// 交由各节点 worker.yml 的 archive_scan_interval 决定（不是「关闭」）。
	SettingKeyLogAcqRulesEnabled   = "log.acq.rules_enabled"
	SettingKeyLogAcqRealtimeTail   = "log.acq.realtime_tail"
	SettingKeyLogAcqGzImport       = "log.acq.gz_import_enabled"
	SettingKeyLogAcqGzScanInterval = "log.acq.gz_scan_interval"
	// SettingKeyLogAcqPathOverrides 实例级日志路径覆盖映射（JSON 对象文本，§3.4）。
	SettingKeyLogAcqPathOverrides = "log.acq.path_overrides"
	// SettingKeyBackupRetentionDays 默认备份保留天数（整数）。CP 后台巡检据此裁剪超期备份。
	SettingKeyBackupRetentionDays = "backup.retention_days"
	// SettingKeyQuotaEnforceInterval 运行期配额强制巡检周期（Go duration）。默认 60s（M-2）。
	SettingKeyQuotaEnforceInterval = "quota.enforce_interval"
	// SettingKeyQuotaEnforceMode 平台默认的运行期配额超限处置档位（alert|throttle|stop）。
	// 默认 alert：最保守，绝不因配额误停实例；组的 EnforceMode 可覆盖本值。
	SettingKeyQuotaEnforceMode = "quota.enforce_mode"
	// SettingKeyQuotaEnforceStreak 连续超限拍数阈值（整数）。默认 5（M-2：与 60s 组合约 5 分钟窗口）。
	SettingKeyQuotaEnforceStreak = "quota.enforce_streak"
	// SettingKeySnapshotRetentionCount 实例快照按条数保留上限（整数）。默认 10。
	SettingKeySnapshotRetentionCount = "snapshot.retention_count"
	// SettingKeySnapshotRetentionDays 实例快照按天数保留上限（整数）。默认 30。
	SettingKeySnapshotRetentionDays = "snapshot.retention_days"
	// SettingKeySnapshotPreRollbackKeep pre_rollback 快照保留条数下限（整数）。
	// pre_rollback 不受 snapshot.retention_count 裁剪，至少保留这么多条，避免「回滚把回滚点挤掉」。
	SettingKeySnapshotPreRollbackKeep = "snapshot.pre_rollback_keep"
	// SettingKeySnapshotMaxPerInstance 单实例快照条数上限（整数m-2）。默认 20；0=不限。
	// 创建前保护：拦截「保留策略尚未生效就把磁盘写满」的自伤（快照是全量归档）。
	SettingKeySnapshotMaxPerInstance = "snapshot.max_per_instance"
	// SettingKeySnapshotMaxTotalMB 单实例快照总占用上限（MiB 整数m-2）。默认 0=不限。
	SettingKeySnapshotMaxTotalMB = "snapshot.max_total_mb"
	// SettingKeyCrashStatRetentionDays 崩溃趋势统计保留天数（整数）。默认 90。
	SettingKeyCrashStatRetentionDays = "crash.stat_retention_days"
	// SettingKeyProxyURL CP 出站代理地址（network 类）。敏感（脱敏展示）。
	// 落库即重建 CP 出站持有者（优先级 DB > control-plane.yml > env）；同时作为各节点默认代理。
	SettingKeyProxyURL = "proxy.url"
	// SettingKeyProxyNoProxy CP/全局默认出站代理的免代理列表（逗号分隔，语义同 NO_PROXY）。
	SettingKeyProxyNoProxy = "proxy.no_proxy"
	// SettingKeyOrphanGracePeriod 无主运行时宽限期（Go duration）。默认 10m；首次发现后观察期内若 CP 又有记录则取消。
	SettingKeyOrphanGracePeriod = "instance_reverse_reconcile.grace_period"
	// SettingKeyOrphanAutoDispose 宽限后是否自动下发处置（true|false）。默认 false：只列表/日志，管理员手动确认。
	SettingKeyOrphanAutoDispose = "instance_reverse_reconcile.auto_dispose"
	// SettingKeyBotReclaimGracePeriod 失效 Bot 回收宽限期（Go duration）。默认 2m——Bot 生命周期短，容忍一次世代迁移窗口即可。
	SettingKeyBotReclaimGracePeriod = "bot_reclaim.grace_period"
	// SettingKeyBotReclaimAuto 宽限后是否自动回收失效 Bot（true|false）。默认 true，但仅对 Fleet 归属 Bot 生效；V1 手动 Bot 永不自动回收。
	SettingKeyBotReclaimAuto = "bot_reclaim.auto_reclaim"
	// SettingKeyBotZombieSessionIdleThreshold 僵尸压测会话停滞阈值（Go duration）。
	// 默认 10m——会话在线 Bot 数为 0 且停滞超过该时长即收敛为 stopped，终止对失联会话的无效补足与写放大。
	SettingKeyBotZombieSessionIdleThreshold = "bot_reclaim.zombie_session_idle_threshold"
	// SettingKeyPlatformPublicBaseURL 是平台生成绝对链接唯一允许使用的公共基址。
	// 允许 HTTP 供无 TLS 的自托管内网使用；不允许由请求头推断。
	SettingKeyPlatformPublicBaseURL = "platform.public_base_url"
	// SettingKeyInviteSMTPHost / Port / Username / Password / From 是独立于告警通道的邀请邮件配置。
	SettingKeyInviteSMTPHost     = "invite.smtp.host"
	SettingKeyInviteSMTPPort     = "invite.smtp.port"
	SettingKeyInviteSMTPUsername = "invite.smtp.username"
	SettingKeyInviteSMTPPassword = "invite.smtp.password"
	SettingKeyInviteSMTPFrom     = "invite.smtp.from"
	// SettingKeyGitHubToken 可选 GitHub API token（敏感，脱敏展示）。落库即时生效（各 GitHub API
	// 调用点在发请求时读取生效值）：自更新与 ServerProbe 制品版本库同步共用，
	// 非空时请求带 Authorization 把额度从匿名 60 次/时/IP 提升到 5000 次/时——共享出口 IP 耗尽
	// 匿名额度即 403。基线取 update.github_token（yml/env），优先级 DB > yml > env；值支持
	// ${ENV_VAR} 引用（与 SMTP 密码同约定）或字面令牌。明文落库的暴露面与 proxy.url 含凭据时一致。
	SettingKeyGitHubToken = "github.token"

	// === 控制面入口限流（HTTP 层，与后端业务无关）===
	//
	// 这些键控制的是「单个客户端 IP 每分钟/秒能发多少次请求」，不是业务配额。
	// 分成四组预算（只读 / 写 / 日志查询 / 全局），每组两个键（速率、容量）：
	//   - 速率 RatePerSecond：稳态补充速度，决定长期吞吐；
	//   - 容量 Capacity：突发上限，决定「一阵猛点」能撑多久。
	// 两者同时为 0 表示该组完全不限流（超高并发专场可显式关闭，全局桶除外）。
	//
	// 为什么做成平台设置（2026-11-13）：限流阈值与部署形态强相关——单机内网、
	// 反代后集中出口 IP、多人共用一台跳板机，合理取值差一个数量级。写死在代码里
	// 只能二选一：要么卡住正常运维（实测 3 标签页 30 次 429），要么为少数场景放宽
	// 到对所有部署都不设防。
	SettingKeyRateLimitReadRate      = "traffic.rate_limit.read.rate_per_second"
	SettingKeyRateLimitReadCapacity  = "traffic.rate_limit.read.capacity"
	SettingKeyRateLimitWriteRate     = "traffic.rate_limit.write.rate_per_second"
	SettingKeyRateLimitWriteCapacity = "traffic.rate_limit.write.capacity"
	SettingKeyRateLimitLogRate       = "traffic.rate_limit.log_query.rate_per_second"
	SettingKeyRateLimitLogCapacity   = "traffic.rate_limit.log_query.capacity"
	SettingKeyRateLimitGlobalRate    = "traffic.rate_limit.global.rate_per_second"
	SettingKeyRateLimitGlobalCap     = "traffic.rate_limit.global.capacity"
)

var (
	// ErrSettingKeyNotWritable 键不在可写白名单内（启动固定/敏感项）。
	ErrSettingKeyNotWritable = errors.New("配置项不可运行时修改")
	// ErrSettingValueInvalid 写入值未通过该键的语义校验。
	ErrSettingValueInvalid = errors.New("配置值非法")
)

// SettingsReader 暴露「按键取生效值」给其它服务（JDK 安装、实例启动等），
// 使它们读取平台设置的覆盖而无需依赖整个 SettingsService。*SettingsService 实现该接口。
// 为 nil 时消费方须自行回退到各自的默认/本地配置（依赖注入可选）。
type SettingsReader interface {
	// EffectiveValue 返回某键当前生效值（DB 覆盖 > 基线默认）。
	EffectiveValue(key string) string
}

// SettingsService 平台配置服务。
//
// 在 YAML+env 基线（cfg）之上叠加 platform_settings 的 DB 覆盖层，
// 解析「有效配置」、按白名单读写、并对可即时生效项接到真实读取点。
type SettingsService struct {
	db  *gorm.DB
	cfg *config.Config
	// proxyRebuilder 在 proxy.* 覆盖落库后被调用，令 CP 重建出站持有者使新代理即时生效。
	// 由 main 注入（传入重建 httpclient.Provider 的闭包）；为 nil 时不重建（仅落库，下次重启生效）。
	proxyRebuilder func(httpclient.Config)
	// ginModeApplier 在 debug.mode 切换 / 启动基线时被调用切 Gin 模式（true=debug / false=release）。
	// 由 main 注入（闭包调 gin.SetMode），使 gin 依赖留在入口层、不渗入 service。为 nil 时仅切日志级别。
	ginModeApplier func(debug bool)
	// rateLimitApplier 在 traffic.rate_limit.* 覆盖落库后被调用，令入口限流即时采用新预算。
	// 由 main 注入（闭包调 middleware.RateLimiters.Apply）；为 nil 时仅落库、下次重启生效。
	rateLimitApplier func(trafficlimit.Settings)
	// rateLimitMu 串行化限流预算的「配对校验 → 事务落库」；否则两个并发 Update
	// 可能分别基于旧快照通过校验，交错写成 rate>0/capacity=0 的非法组合。
	rateLimitMu sync.Mutex
}

// NewSettingsService 创建平台配置服务。
// 启动时把已落库的可即时生效覆盖项重放到运行时读取点（如日志级别），保证重启后覆盖仍生效。
func NewSettingsService(db *gorm.DB, cfg *config.Config) *SettingsService {
	s := &SettingsService{db: db, cfg: cfg}
	s.applyPersistedOverrides()
	return s
}

// SetProxyRebuilder 注入 CP 出站持有者重建回调。
// 保存 proxy.url/proxy.no_proxy 后以「当前生效代理」（DB 覆盖 > yaml > env）回调之，
// 令 CP 自身出站立即走新代理、免重启。不注入则代理覆盖仅落库、下次重启生效。
func (s *SettingsService) SetProxyRebuilder(fn func(httpclient.Config)) {
	s.proxyRebuilder = fn
}

// SetGinModeApplier 注入 Gin 模式切换回调。由 main 注入闭包调 gin.SetMode，使 gin 依赖留在入口层。
// 须在 ApplyDebugBaseline 前注入，以便启动按 debug.mode 设初始 Gin 模式。
func (s *SettingsService) SetGinModeApplier(fn func(debug bool)) {
	s.ginModeApplier = fn
}

// ApplyDebugBaseline 按当前生效 debug.mode 设初始 Gin 模式 + 日志级别。
// main 须在 router.Setup 前调用：默认 debug.mode=false → Gin release（不刷 [GIN-debug] 路由噪音）+ 日志 info。
func (s *SettingsService) ApplyDebugBaseline() {
	s.applyDebugMode(s.debugModeOn())
}

// debugModeOn 报告调试模式当前是否开启（生效值 == "true"）。
func (s *SettingsService) debugModeOn() bool {
	return s.EffectiveValue(SettingKeyDebugMode) == "true"
}

// applyDebugMode 应用调试模式：开=日志强制 debug + Gin debug；关=日志回 log.level 基线 + Gin release。
func (s *SettingsService) applyDebugMode(on bool) {
	if on {
		config.SetLogLevel("debug")
	} else {
		config.SetLogLevel(s.EffectiveValue(SettingKeyLogLevel))
	}
	if s.ginModeApplier != nil {
		s.ginModeApplier(on)
	}
}

// EffectiveProxy 返回 CP 当前生效的出站代理配置。
// 优先级：settings DB 覆盖（全局） > control-plane.yml proxy > 环境变量（空配回退）。
// 同时作为各节点的「全局默认代理」（节点 inherit 时下发此值）。
func (s *SettingsService) EffectiveProxy() httpclient.Config {
	url := s.cfg.Proxy.URL
	noProxy := s.cfg.Proxy.NoProxy
	if overrides, err := s.loadOverrides(); err == nil {
		if v, ok := overrides[SettingKeyProxyURL]; ok {
			url = v
		}
		if v, ok := overrides[SettingKeyProxyNoProxy]; ok {
			noProxy = v
		}
	}
	return httpclient.Config{URL: url, NoProxy: noProxy}
}

// DirectProbeTimeouts 返回 MC 直探（SLP / Query）当前生效超时。
// 心跳响应据此下发毫秒值给 Worker，使其填入采集编排链（无需重启 Worker）。
// 值非法/无覆盖时回退基线默认（3s），保证消费方始终拿到可用正超时。
func (s *SettingsService) DirectProbeTimeouts() (slp, query time.Duration) {
	return parseDurationOr(s.EffectiveValue(SettingKeyDirectProbeSLPTimeout), defaultDirectProbeTimeout),
		parseDurationOr(s.EffectiveValue(SettingKeyDirectProbeQueryTimeout), defaultDirectProbeTimeout)
}

// defaultDirectProbeTimeout / maxDirectProbeTimeout / probeScrapeTimeoutCap 是 MC 直探相关数值。
//
// **单一来源**：一律引用 internal/platform/directprobe（复审 NEW-ISSUE A），
// 与 Worker 侧（`internal/worker/metrics` 的归一、`internal/worker/heartbeat` 的采集预算）
// 引用同一批常量。上界 maxDirectProbeTimeout 由心跳节拍护栏反推（推导见 directprobe 包文档），
// 故「超时可配」与「心跳护栏」不可能再出现各写一份字面量而互相矛盾的情况。
const (
	// defaultDirectProbeTimeout 是 MC 直探超时基线（未配置时生效）。
	defaultDirectProbeTimeout = directprobe.DefaultTimeout
	// maxDirectProbeTimeout 是 MC 直探超时上界：写路径（validateSettingValue）拒收超限值，
	// 读取/下发路径（parseDurationOr）同样钳制到本上界，令 yaml/env 基线或历史落库值也不会突破。
	maxDirectProbeTimeout = directprobe.MaxTimeout
	// probeScrapeTimeoutCap 是 Worker 抓取 ServerProbe `/metrics` 的硬编码 HTTP 上限
	// （见 internal/worker/metrics.ScrapeServerProbe），仅用于 CP 侧估算实时链路最坏时延
	// （见 InstanceService.metricsFetchTimeout），非可配项。
	probeScrapeTimeoutCap = directprobe.ProbeScrapeTimeoutCap
)

// realtimeMetricsBudgetMargin 是实时指标链路额外余量：吸收 gRPC 往返、容器/进程采样与调度抖动，
// 使 CP 侧截止始终严格大于 Worker 侧串行编排链的最坏时延。
const realtimeMetricsBudgetMargin = 5 * time.Second

// parseDurationOr 解析 Go duration 文本；解析失败或非正时回退 fallback，并钳制到 MC 直探超时上界。
// 仅供直探超时（SLP/Query）读取，故读侧钳制与写侧校验对称（复审 N3）：基线（yaml/env）或
// 历史落库值即便超限也只以 maxDirectProbeTimeout 生效，不让无限大超时冻结详情页与采集链。
//
// 归一委托 directprobe.NormalizeTimeout（复审 NEW-ISSUE A）：与 Worker 侧归一、CP 写校验
// 引用同一上界，三处不可能漂移。
func parseDurationOr(raw string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return fallback
	}
	return directprobe.NormalizeTimeout(d)
}

// SettingItem 单个配置项的对外表示。
type SettingItem struct {
	Key string `json:"key"`
	// Value 当前生效值（DB 覆盖 > env > YAML），敏感项已脱敏。
	Value string `json:"value"`
	// Editable 是否可经 PUT /settings 运行时修改。
	Editable bool `json:"editable"`
	// Sensitive 是否敏感项（值已脱敏，不返回明文）。
	Sensitive bool `json:"sensitive"`
	// Overridden 该项当前是否被 DB 覆盖（仅可编辑项有意义）。
	Overridden bool `json:"overridden"`
	// EffectiveImmediately 运行时修改是否在 CP 内即时生效（false 表示需改配置/重启或在 Worker 侧生效）。
	EffectiveImmediately bool `json:"effectiveImmediately"`
}

// SettingsView GET /settings 的响应：可编辑项与只读项分区。
type SettingsView struct {
	Editable []SettingItem `json:"editable"`
	ReadOnly []SettingItem `json:"readOnly"`
}

// Get 返回当前有效配置视图：可编辑项（含 DB 覆盖当前值）+ 只读项（启动固定值），敏感项脱敏。
func (s *SettingsService) Get() (*SettingsView, error) {
	overrides, err := s.loadOverrides()
	if err != nil {
		return nil, err
	}

	editable := []SettingItem{
		s.editableItem(SettingKeyLogLevel, s.defaultValue(SettingKeyLogLevel), overrides, true),
		// 调试模式总开关：开=日志 debug + Gin debug，关=info + release，运行时即时生效。
		s.editableItem(SettingKeyDebugMode, s.defaultValue(SettingKeyDebugMode), overrides, true),
		s.editableItem(SettingKeyJDKMirrorTemurin, s.defaultValue(SettingKeyJDKMirrorTemurin), overrides, false),
		s.editableItem(SettingKeyJDKMirrorCorretto, s.defaultValue(SettingKeyJDKMirrorCorretto), overrides, false),
		s.editableItem(SettingKeyJDKMirrorZulu, s.defaultValue(SettingKeyJDKMirrorZulu), overrides, false),
		// Node.js dist 镜像源：随安装下发 Worker（同 jdk.mirror.*，非 CP 内即时生效）。
		s.editableItem(SettingKeyRuntimeMirrorNodeJS, s.defaultValue(SettingKeyRuntimeMirrorNodeJS), overrides, false),
		s.editableItem(SettingKeyGracefulStopTimeout, s.defaultValue(SettingKeyGracefulStopTimeout), overrides, false),
		// MC 直探超时：经心跳下发 Worker，Worker 侧生效（非 CP 内即时生效）。
		s.editableItem(SettingKeyDirectProbeSLPTimeout, s.defaultValue(SettingKeyDirectProbeSLPTimeout), overrides, false),
		s.editableItem(SettingKeyDirectProbeQueryTimeout, s.defaultValue(SettingKeyDirectProbeQueryTimeout), overrides, false),
		// 实例健康巡检与自愈：经心跳下发 Worker，Worker 侧生效（非 CP 内即时生效）。
		s.editableItem(SettingKeyHealthScanEnabled, s.defaultValue(SettingKeyHealthScanEnabled), overrides, false),
		s.editableItem(SettingKeyHealthScanInterval, s.defaultValue(SettingKeyHealthScanInterval), overrides, false),
		s.editableItem(SettingKeyHealthProbeKind, s.defaultValue(SettingKeyHealthProbeKind), overrides, false),
		s.editableItem(SettingKeyHealthSuspicionThreshold, s.defaultValue(SettingKeyHealthSuspicionThreshold), overrides, false),
		s.editableItem(SettingKeyHealthAction, s.defaultValue(SettingKeyHealthAction), overrides, false),
		s.editableItem(SettingKeyHealthCircuitThreshold, s.defaultValue(SettingKeyHealthCircuitThreshold), overrides, false),
		s.editableItem(SettingKeyHealthCircuitWindow, s.defaultValue(SettingKeyHealthCircuitWindow), overrides, false),
		s.editableItem(SettingKeyHealthStartupWarmup, s.defaultValue(SettingKeyHealthStartupWarmup), overrides, false),
		s.editableItem(SettingKeyHealthSelfHealMaxRestarts, s.defaultValue(SettingKeyHealthSelfHealMaxRestarts), overrides, false),
		// 日志采集：经心跳下发 Worker，Worker 侧生效（非 CP 内即时生效）。
		// 这 5 项是平台默认值；单个 Worker 的覆盖在节点详情页配置（键加 `@node:<uuid>` 后缀）。
		s.editableItem(SettingKeyLogAcqRulesEnabled, s.defaultValue(SettingKeyLogAcqRulesEnabled), overrides, false),
		s.editableItem(SettingKeyLogAcqRealtimeTail, s.defaultValue(SettingKeyLogAcqRealtimeTail), overrides, false),
		s.editableItem(SettingKeyLogAcqGzImport, s.defaultValue(SettingKeyLogAcqGzImport), overrides, false),
		s.editableItem(SettingKeyLogAcqGzScanInterval, s.defaultValue(SettingKeyLogAcqGzScanInterval), overrides, false),
		s.editableItem(SettingKeyLogAcqPathOverrides, s.defaultValue(SettingKeyLogAcqPathOverrides), overrides, false),
		s.editableItem(SettingKeyBackupRetentionDays, s.defaultValue(SettingKeyBackupRetentionDays), overrides, false),
		// 运行期配额强制：CP 后台巡检读取生效值（即时生效，下一拍用新值）。
		s.editableItem(SettingKeyQuotaEnforceInterval, s.defaultValue(SettingKeyQuotaEnforceInterval), overrides, true),
		s.editableItem(SettingKeyQuotaEnforceMode, s.defaultValue(SettingKeyQuotaEnforceMode), overrides, true),
		s.editableItem(SettingKeyQuotaEnforceStreak, s.defaultValue(SettingKeyQuotaEnforceStreak), overrides, true),
		// 实例快照保留：CP 后台裁剪循环读取生效值。
		s.editableItem(SettingKeySnapshotRetentionCount, s.defaultValue(SettingKeySnapshotRetentionCount), overrides, true),
		s.editableItem(SettingKeySnapshotRetentionDays, s.defaultValue(SettingKeySnapshotRetentionDays), overrides, true),
		s.editableItem(SettingKeySnapshotPreRollbackKeep, s.defaultValue(SettingKeySnapshotPreRollbackKeep), overrides, true),
		// 快照创建上限（m-2）：创建前保护，读侧即时生效。
		s.editableItem(SettingKeySnapshotMaxPerInstance, s.defaultValue(SettingKeySnapshotMaxPerInstance), overrides, true),
		s.editableItem(SettingKeySnapshotMaxTotalMB, s.defaultValue(SettingKeySnapshotMaxTotalMB), overrides, true),
		// 崩溃趋势统计保留：日粒度统计表的清理窗口。
		s.editableItem(SettingKeyCrashStatRetentionDays, s.defaultValue(SettingKeyCrashStatRetentionDays), overrides, true),
		// 出站代理（network 类）：保存即在 CP 内重建出站持有者（即时生效）。
		// proxy.url 标 sensitive：含凭据时回显脱敏（仅展示 scheme://host:port），不外泄明文密码。
		s.proxyURLItem(overrides),
		s.editableItem(SettingKeyProxyNoProxy, s.defaultValue(SettingKeyProxyNoProxy), overrides, true),
		// 实例反向对账护栏：宽限期与自动处置开关；读侧即时生效（下一次心跳观察即用）。
		s.editableItem(SettingKeyOrphanGracePeriod, s.defaultValue(SettingKeyOrphanGracePeriod), overrides, true),
		s.editableItem(SettingKeyOrphanAutoDispose, s.defaultValue(SettingKeyOrphanAutoDispose), overrides, true),
		// 失效 Bot 自动回收护栏：宽限期与自动回收开关；读侧即时生效（下一拍巡检即用）。
		s.editableItem(SettingKeyBotReclaimGracePeriod, s.defaultValue(SettingKeyBotReclaimGracePeriod), overrides, true),
		s.editableItem(SettingKeyBotReclaimAuto, s.defaultValue(SettingKeyBotReclaimAuto), overrides, true),
		s.editableItem(SettingKeyPlatformPublicBaseURL, s.defaultValue(SettingKeyPlatformPublicBaseURL), overrides, true),
		s.editableItem(SettingKeyInviteSMTPHost, s.defaultValue(SettingKeyInviteSMTPHost), overrides, true),
		s.editableItem(SettingKeyInviteSMTPPort, s.defaultValue(SettingKeyInviteSMTPPort), overrides, true),
		s.editableItem(SettingKeyInviteSMTPUsername, s.defaultValue(SettingKeyInviteSMTPUsername), overrides, true),
		s.inviteSMTPPasswordItem(overrides),
		s.editableItem(SettingKeyInviteSMTPFrom, s.defaultValue(SettingKeyInviteSMTPFrom), overrides, true),
		// GitHub API 令牌（敏感，脱敏展示）：自更新与制品版本库同步发请求时读取生效值，即时生效。
		s.githubTokenItem(overrides),
		// 控制面入口限流：保存即在 CP 内热更新（下次请求即用新预算），无需重启。
		s.editableItem(SettingKeyRateLimitReadRate, s.defaultValue(SettingKeyRateLimitReadRate), overrides, true),
		s.editableItem(SettingKeyRateLimitReadCapacity, s.defaultValue(SettingKeyRateLimitReadCapacity), overrides, true),
		s.editableItem(SettingKeyRateLimitWriteRate, s.defaultValue(SettingKeyRateLimitWriteRate), overrides, true),
		s.editableItem(SettingKeyRateLimitWriteCapacity, s.defaultValue(SettingKeyRateLimitWriteCapacity), overrides, true),
		s.editableItem(SettingKeyRateLimitLogRate, s.defaultValue(SettingKeyRateLimitLogRate), overrides, true),
		s.editableItem(SettingKeyRateLimitLogCapacity, s.defaultValue(SettingKeyRateLimitLogCapacity), overrides, true),
		s.editableItem(SettingKeyRateLimitGlobalRate, s.defaultValue(SettingKeyRateLimitGlobalRate), overrides, true),
		s.editableItem(SettingKeyRateLimitGlobalCap, s.defaultValue(SettingKeyRateLimitGlobalCap), overrides, true),
	}

	readOnly := []SettingItem{
		readOnlyItem("server.host", s.cfg.Server.Host, false),
		readOnlyItem("server.port", strconv.Itoa(s.cfg.Server.Port), false),
		readOnlyItem("grpc.port", strconv.Itoa(s.cfg.GRPC.Port), false),
		readOnlyItem("database.driver", s.cfg.Database.Driver, false),
		readOnlyItem("database.dsn", maskDSN(s.cfg.Database.DSN), true),
		readOnlyItem("jwt.secret", maskSecret(s.cfg.JWT.Secret), true),
		// CP↔Worker WS 令牌密钥：显式配置时掩码显示；未配置时展示来源
		// （生产 autogen / dev 回退），不回显解析后的密钥值（避免把生成密钥带进设置响应）。
		readOnlyItem("jwt.ws_secret", s.wsTokenSecretDisplay(), s.cfg.JWT.WSSecret != ""),
		readOnlyItem("jwt.access_ttl", s.cfg.JWT.AccessTTL.String(), false),
		readOnlyItem("jwt.refresh_ttl", s.cfg.JWT.RefreshTTL.String(), false),
	}

	return &SettingsView{Editable: editable, ReadOnly: readOnly}, nil
}

// Update 按白名单写入一批配置覆盖，校验每个键的值语义；可即时生效项写库后立即应用。
// 任一键非法（不在白名单 / 值不合法）则整体拒绝、不落库（避免半应用）。
func (s *SettingsService) Update(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	// 先全量校验，再统一落库 + 应用，保证原子性。
	for key, val := range values {
		if !isWritableSettingKey(key) {
			return fmt.Errorf("%w: %s", ErrSettingKeyNotWritable, key)
		}
		if err := validateSettingValue(key, val); err != nil {
			return err
		}
	}
	// 成组约束与落库必须在同一把锁下，避免并发提交分别读取旧的另一分量。
	if hasRateLimitSetting(values) {
		s.rateLimitMu.Lock()
		err := s.validateRateLimitPair(values)
		if err == nil {
			err = s.writeValues(values, false)
		}
		s.rateLimitMu.Unlock()
		if err != nil {
			return err
		}
		// 事务提交后再读取一次完整 overrides 快照并热应用；若紧接着有下一次更新，
		// 它同样会串行落库，最终应用的是一个完整预算而非跨写混读。
		s.applyRateLimits()
		return nil
	}
	return s.writeValues(values, true)
}

// hasRateLimitSetting reports whether a platform update touches any global rate-limit key.
func hasRateLimitSetting(values map[string]string) bool {
	for key := range values {
		if IsRateLimitSettingKey(key) {
			return true
		}
	}
	return false
}

// UpdateNodeScoped 写入**某节点的**平台键覆盖，是节点级覆盖的唯一写入口。
//
// 为什么单独成一个方法而不复用 Update（2026-10-20 审查修正）：`Update` 的键白名单
// （isWritableSettingKey）不区分「平台级端点」与「节点级端点」，两者共用会让
// `PUT /api/v1/settings`（只要求 settings.write、且不写审计）也能写 `@node:` 键，
// 从而绕过节点级门禁静默停掉任意节点的采集。现在 `@node:` 后缀在这里统一拼接，
// Update 侧对带后缀的键一律拒绝，平台端点物理上写不到节点键。
//
// 参数 keys 是**基础键**（不带后缀）；nodeUUID 必须非空。
func (s *SettingsService) UpdateNodeScoped(nodeUUID string, values map[string]string) error {
	if strings.TrimSpace(nodeUUID) == "" {
		return fmt.Errorf("%w: 节点标识为空", ErrSettingValueInvalid)
	}
	if len(values) == 0 {
		return nil
	}
	scoped := make(map[string]string, len(values))
	for baseKey, val := range values {
		// 基础键必须是确有节点级消费方的键（日志采集 5 个），否则写进去永远不生效。
		if !supportsNodeScope(baseKey) {
			return fmt.Errorf("%w: %s 不支持节点级覆盖", ErrSettingKeyNotWritable, baseKey)
		}
		// 调用方若已带后缀则拒绝，避免拼出 `x@node:a@node:b` 这类永不命中的键。
		if strings.Contains(baseKey, nodeScopedSettingSuffix) {
			return fmt.Errorf("%w: %s 不应自带节点后缀", ErrSettingValueInvalid, baseKey)
		}
		full := NodeScopedSettingKey(baseKey, nodeUUID)
		if err := validateSettingValue(full, val); err != nil {
			return err
		}
		scoped[full] = val
	}
	return s.writeValues(scoped, true)
}

// isWindowsAbsPath 判定 Windows 形态的绝对路径（盘符或 UNC）。
//
// 为什么 CP 要单独判（2026-10-20 审查）：CP 跑 Linux、节点可能跑 Windows 时，
// `filepath.IsAbs` 在 CP 上对 `C:\\logs\\a.log` / `\\server\share\x` 返回 false，
// 校验放行、返回 200；到了 Windows 节点上 `filepath.IsAbs` 为 true，Worker 的
// validateInstanceRelativePath 直接拒绝登记 —— 用户看到的是「保存成功但实例日志登记失败」。
func isWindowsAbsPath(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 2 && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) && p[1] == ':'
}

// escapesUnderWindowsSeparators 判断路径按 Windows 分隔符（`\` 与 `/`）折叠 `.`/`..` 后
// 是否逃出根。
//
// 为什么 CP 需要单独一道（2026-10-21 补）：CP 跑 Linux 时 `filepath.Clean` 只把 `/` 当
// 分隔符，`..\..\etc\passwd` 会原样保留、不带 `../` 前缀，于是宿主判据**放行**；可它下发
// 给 Windows 节点后会被 Worker 判为逃逸、直接拒绝登记——与盘符/UNC 完全同一类
// 「保存成功但实例日志登记失败」，而 isWindowsAbsPath 只能拦住后者。
//
// 判据与 Worker 侧 validateInstanceRelativePath 里的 escapesRoot（dialectWindows）**同源**
// （internal/worker/logs/ingest/instances.go）：深度下穿到负即逃逸。两处必须同步改动，
// 否则 CP 与 Worker 的结论会再次分叉。
//
// 注意本函数**只做收紧**：它仅在原本放行的值上追加拒绝，不改变任何既有放行结论。
func escapesUnderWindowsSeparators(p string) bool {
	depth := 0
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		switch seg {
		case ".": // 当前目录不改变深度
		case "..":
			depth--
			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}
	return false
}

// writeValues 是 Update / UpdateNodeScoped 共用的落库 + 应用实现（调用方已完成校验）。
func (s *SettingsService) writeValues(values map[string]string, applyRateLimit bool) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for key, val := range values {
			rec := model.PlatformSetting{Key: key, Value: val, UpdatedAt: time.Now()}
			if err := tx.Save(&rec).Error; err != nil {
				return fmt.Errorf("保存配置项失败: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for key, val := range values {
		s.applyOverride(key, val)
	}
	if _, ok := values[SettingKeyProxyURL]; ok {
		s.rebuildProxy()
	} else if _, ok := values[SettingKeyProxyNoProxy]; ok {
		s.rebuildProxy()
	}
	// 入口限流：任一限流键落库即热更新整组预算（回调用「当前生效值」而非本次提交值，
	// 这样只改容量的提交也能带动速率一起重新下发）。
	for key := range values {
		if IsRateLimitSettingKey(key) && applyRateLimit {
			s.applyRateLimits()
			break
		}
	}
	return nil
}

// rebuildProxy 以当前生效代理重建 CP 出站持有者。rebuilder 未注入时静默跳过（仅落库）。
func (s *SettingsService) rebuildProxy() {
	if s.proxyRebuilder == nil {
		return
	}
	s.proxyRebuilder(s.EffectiveProxy())
}

// editableItem 组装可编辑项：DB 覆盖存在则用覆盖值，否则用基线默认值（base）。
func (s *SettingsService) editableItem(key, base string, overrides map[string]string, immediate bool) SettingItem {
	val := base
	_, overridden := overrides[key]
	if overridden {
		val = overrides[key]
	}
	return SettingItem{
		Key:                  key,
		Value:                val,
		Editable:             true,
		Overridden:           overridden,
		EffectiveImmediately: immediate,
	}
}

// proxyURLItem 组装出站代理地址项：可编辑 + sensitive，回显时脱敏含凭据的 URL，
// 保存在 CP 内即时生效（重建出站持有者）。
func (s *SettingsService) proxyURLItem(overrides map[string]string) SettingItem {
	val, overridden := overrides[SettingKeyProxyURL]
	if !overridden {
		val = s.defaultValue(SettingKeyProxyURL)
	}
	return SettingItem{
		Key:                  SettingKeyProxyURL,
		Value:                httpclient.Sanitize(val), // 脱敏：不回显明文 user:pass
		Editable:             true,
		Sensitive:            true,
		Overridden:           overridden,
		EffectiveImmediately: true,
	}
}

func (s *SettingsService) inviteSMTPPasswordItem(overrides map[string]string) SettingItem {
	_, overridden := overrides[SettingKeyInviteSMTPPassword]
	return SettingItem{
		Key:                  SettingKeyInviteSMTPPassword,
		Value:                maskConfigured(overridden),
		Editable:             true,
		Sensitive:            true,
		Overridden:           overridden,
		EffectiveImmediately: true,
	}
}

// githubTokenItem 组装 GitHub API 令牌项（敏感）：已配置时回显掩码（保留首尾各 3 字符，
// 管理员可据此核对用的是哪个令牌前缀），不回显明文；空串=未配置（回退 yml/env 基线）。
func (s *SettingsService) githubTokenItem(overrides map[string]string) SettingItem {
	val, overridden := overrides[SettingKeyGitHubToken]
	if !overridden {
		val = s.defaultValue(SettingKeyGitHubToken)
	}
	display := ""
	if trimmed := strings.TrimSpace(val); trimmed != "" {
		display = maskSecret(trimmed)
	}
	return SettingItem{
		Key:                  SettingKeyGitHubToken,
		Value:                display,
		Editable:             true,
		Sensitive:            true,
		Overridden:           overridden,
		EffectiveImmediately: true,
	}
}

func maskConfigured(configured bool) string {
	if configured {
		return "(已配置)"
	}
	return ""
}

func readOnlyItem(key, value string, sensitive bool) SettingItem {
	return SettingItem{Key: key, Value: value, Editable: false, Sensitive: sensitive}
}

// defaultValue 返回某键的基线默认值（DB 无覆盖时的生效值）。
// 单点定义，供 Get（展示）与 EffectiveValue（消费）共享，避免默认值在两处漂移。
func (s *SettingsService) defaultValue(key string) string {
	// Worker 级覆盖键按基础键取默认值：调用方可能传入带 `@node:` 后缀的键。
	baseKey, _, _ := nodeScopeSplit(key)
	switch baseKey {
	case SettingKeyLogLevel:
		return s.cfg.Log.Level
	case SettingKeyDebugMode:
		return "false"
	case SettingKeyJDKMirrorTemurin:
		return "https://api.adoptium.net"
	case SettingKeyJDKMirrorCorretto:
		return "https://corretto.aws"
	case SettingKeyJDKMirrorZulu:
		return "https://api.azul.com"
	case SettingKeyRuntimeMirrorNodeJS:
		return "https://nodejs.org/dist"
	case SettingKeyGracefulStopTimeout:
		return "30s"
	case SettingKeyDirectProbeSLPTimeout, SettingKeyDirectProbeQueryTimeout:
		return "3s"
	case SettingKeyHealthScanEnabled:
		return "true"
	case SettingKeyHealthScanInterval:
		return "30s"
	case SettingKeyHealthProbeKind:
		return ""
	case SettingKeyHealthSuspicionThreshold:
		return "3"
	case SettingKeyHealthAction:
		return "warn"
	case SettingKeyHealthCircuitThreshold:
		return "5"
	case SettingKeyHealthCircuitWindow:
		return "10m"
	case SettingKeyHealthStartupWarmup:
		return "5m"
	case SettingKeyHealthSelfHealMaxRestarts:
		return "3"
	// 日志采集：默认与 Worker 侧基线一致——规则引擎与实时采集默认开；
	// gz 导入**总开关默认开、定时扫描周期默认空**（二者组合的效果仍是「默认不扫描」，
	// 因为生效周期取自 worker.yml 的 log_ingest.archive_scan_interval）；路径覆盖默认空。
	//
	// 为什么总开关默认开（2026-10-15 定案）：它同时是「是否回落 worker.yml 基线」的闸门——
	// 若默认关，配了 `log_ingest.archive_scan_interval` 的既有部署升级后会**静默失去定时扫描**
	// （Worker 侧折算在总开关处短路，且不打任何日志）。默认开 + 周期空既保持"默认不扫描"的
	// 保守行为，又让基线回落可达。
	case SettingKeyLogAcqRulesEnabled, SettingKeyLogAcqRealtimeTail, SettingKeyLogAcqGzImport:
		return "true"
	case SettingKeyLogAcqGzScanInterval:
		return ""
	case SettingKeyLogAcqPathOverrides:
		return ""
	case SettingKeyBackupRetentionDays:
		// 备份保留天数的出厂默认值。
		//
		// 历史（2026-10-29 修正）：这里曾写 `s.cfg.LogStore.RetentionDays`——把
		// 「日志保留天数」借来当「备份保留天数」的默认值。那是个**语义耦合错误**：
		// 两个毫不相干的设置被绑在同一个数字上，改日志保留会静默改掉备份保留。
		// log_store 废弃后该字段已不存在，故在此给一个独立的、符合备份语义的值。
		return "7"
	case SettingKeyQuotaEnforceInterval:
		// M-2：30s×3=90s 窗口短于 MC 世界加载/GC 长尾，stop 档误停风险高；改 60s。
		// 周期下限与规模有关：一轮最坏耗时 ≈ ceil(限额实例数 / quotaSampleConcurrency)
		// × quotaDiskTimeout(15s)，64 服时应为 120s（详见 spec §2.3）。默认 60s 对
		// ≤32 个限额实例成立；周期偏小只会把实际节拍拖长为「一轮耗时」，不会丢样本。
		return "60s"
	case SettingKeyQuotaEnforceMode:
		// 默认 alert：最保守档，不干预进程（spec §2.5「alert 默认」）。
		return "alert"
	case SettingKeyQuotaEnforceStreak:
		// M-2：与 60s 组合给出约 5 分钟判定窗口，跨过加载/GC 长尾再处置。
		return "5"
	case SettingKeySnapshotRetentionCount:
		return "10"
	case SettingKeySnapshotRetentionDays:
		return "30"
	case SettingKeySnapshotPreRollbackKeep:
		return "3"
	case SettingKeySnapshotMaxPerInstance:
		// 默认 20：约等于「10 条保留 + 一组回滚点」的量级，作为磁盘自伤前的最后一道闸。
		return "20"
	case SettingKeySnapshotMaxTotalMB:
		// 默认 0=不限：不同实例工作目录体积差异过大，不宜预设阈值；需限容的部署自行开。
		return "0"
	case SettingKeyCrashStatRetentionDays:
		return "90"
	case SettingKeyProxyURL:
		return s.cfg.Proxy.URL
	case SettingKeyProxyNoProxy:
		return s.cfg.Proxy.NoProxy
	case SettingKeyOrphanGracePeriod:
		return "10m"
	case SettingKeyOrphanAutoDispose:
		return "false"
	case SettingKeyBotReclaimGracePeriod:
		return "2m"
	case SettingKeyBotReclaimAuto:
		return "true"
	case SettingKeyPlatformPublicBaseURL, SettingKeyInviteSMTPHost, SettingKeyInviteSMTPPort,
		SettingKeyInviteSMTPUsername, SettingKeyInviteSMTPPassword, SettingKeyInviteSMTPFrom:
		return ""
	case SettingKeyGitHubToken:
		// 基线取 update.github_token（yml/env），DB 覆盖优先于基线（同 proxy.* 语义）。
		return s.cfg.Update.GitHubToken
	case SettingKeyRateLimitReadRate:
		return strconv.Itoa(trafficlimit.Defaults().Read.RatePerSecond)
	case SettingKeyRateLimitReadCapacity:
		return strconv.Itoa(trafficlimit.Defaults().Read.Capacity)
	case SettingKeyRateLimitWriteRate:
		return strconv.Itoa(trafficlimit.Defaults().Write.RatePerSecond)
	case SettingKeyRateLimitWriteCapacity:
		return strconv.Itoa(trafficlimit.Defaults().Write.Capacity)
	case SettingKeyRateLimitLogRate:
		return strconv.Itoa(trafficlimit.Defaults().LogQuery.RatePerSecond)
	case SettingKeyRateLimitLogCapacity:
		return strconv.Itoa(trafficlimit.Defaults().LogQuery.Capacity)
	case SettingKeyRateLimitGlobalRate:
		return strconv.Itoa(trafficlimit.Defaults().Global.RatePerSecond)
	case SettingKeyRateLimitGlobalCap:
		return strconv.Itoa(trafficlimit.Defaults().Global.Capacity)
	}
	return ""
}

// EffectiveValue 返回某键当前生效值（DB 覆盖 > 基线默认）。
// CP 各消费点（JDK 安装、备份裁剪、实例启动）据此读取单项设置，使覆盖真生效。
// 查询失败或键无默认时回退默认值，保证消费方始终拿到可用值（不因 DB 故障卡死）。
func (s *SettingsService) EffectiveValue(key string) string {
	if overrides, err := s.loadOverrides(); err == nil {
		if v, ok := overrides[key]; ok {
			return v
		}
	}
	return s.defaultValue(key)
}

// loadOverrides 读取全部 DB 覆盖为 map。
func (s *SettingsService) loadOverrides() (map[string]string, error) {
	var rows []model.PlatformSetting
	if err := s.db.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询平台配置失败: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out, nil
}

// applyPersistedOverrides 启动时把已落库的可即时生效项重放到运行时读取点。
func (s *SettingsService) applyPersistedOverrides() {
	overrides, err := s.loadOverrides()
	if err != nil {
		return // 启动期容忍：查询失败则沿用 YAML/env 基线。
	}
	for key, val := range overrides {
		s.applyOverride(key, val)
	}
	// 入口限流：启动时把已落库的预算推给中间件。
	// 中间件在此之后才装配（main 里先构造 SettingsService 再 router.Setup），
	// 故这里的调用发生在 SetRateLimitApplier 之前会是空操作——由装配侧补一次
	// ApplyCurrentRateLimits，保证「重启后覆盖仍生效」而不是回落到默认值。
}

// applyOverride 把单个覆盖项应用到 CP 内的运行时读取点。
// 仅日志级别在 CP 内即时生效（EffectiveImmediately）；其余项在各自动作发生时按需读取生效值：
// JDK 镜像源随安装下发、优雅停止超时随启动下发、备份保留天数由后台巡检读取（均经 EffectiveValue）。
func (s *SettingsService) applyOverride(key, val string) {
	switch key {
	case SettingKeyLogLevel:
		// 调试模式开启时由其强制 debug，log.level 设置不下压（关调试后回退到此值）。
		if !s.debugModeOn() {
			config.SetLogLevel(val)
		}
	case SettingKeyDebugMode:
		s.applyDebugMode(val == "true")
	}
}

// nodeScopeSplit 拆分 Worker 级覆盖键，返回基础键、节点 UUID 与「是否带作用域后缀」。
//
// 键形态 `<基础键>@node:<nodeUUID>`（构造侧见 NodeScopedSettingKey）。
// scoped 为 true 但 nodeUUID 为空表示写了 `x@node:` 这类残缺键——调用方应拒绝而非
// 当作平台键处理（否则「想配节点级」会静默变成「配了平台级」，最难排查的一类错）。
func nodeScopeSplit(key string) (baseKey, nodeUUID string, scoped bool) {
	if i := strings.Index(key, nodeScopedSettingSuffix); i >= 0 {
		return key[:i], key[i+len(nodeScopedSettingSuffix):], true
	}
	return key, "", false
}

// supportsNodeScope 报告某基础键是否支持 Worker 级覆盖。
//
// 判据是「**确有节点级消费方**」：只有日志采集的 5 个键会被按节点合并后下发
// （LogAcqPolicy(nodeUUID) 经心跳下发，节点级覆盖真会生效），故这里直接以
// LogAcqPlatformKeys() 为准——它同时也是合并实现（mergeLogAcqValues）的键集合，
// 二者不会再各自漂移出「放行了却没人读」的差集。
//
// 为什么不能按「是否经心跳下发」放行：health.* / direct_probe.* 也随心跳下发，但它们的
// 解析器（HealthScanPolicy / DirectProbeTimeouts）**没有节点形参**，全仓没有第二处
// 节点级合并；放行只会得到一条「写入 200 但永不生效」的静默键，且该写入路径不产生
// 审计 action（静默 + 无痕）——正是本函数要防的情形。
// 待这两类各自的节点级消费方就位（spec §3.1.1 登记为待办）再扩围。
func supportsNodeScope(baseKey string) bool {
	for _, key := range LogAcqPlatformKeys() {
		if key == baseKey {
			return true
		}
	}
	return false
}

// isWritableSettingKey 报告键是否在**平台级**可写白名单内。
//
// 节点级覆盖（`<平台键>@node:<uuid>`）**刻意不在此放行**（2026-10-20 审查修正）：
// 本函数是 `SettingsService.Update` 的守卫，而 `Update` 有两个调用方——
//   - 节点端点 `PUT /nodes/:id/settings/log-acquisition`（requirePlatformAdmin + 审计）
//   - 通用端点 `PUT /api/v1/settings`（**只要求 settings.write，且不写审计**）
//
// 若在这里放行 `@node:` 键，任何持 `settings.write` 的自定义角色（可以完全没有节点权限）
// 就能经通用端点把任意节点的 `log.acq.realtime_tail` 置 false —— 单向停掉该节点的实时采集，
// 既越过了节点级门禁，也不留任何审计痕迹。这正是 supportsNodeScope 的注释自称要防的
// 「静默 + 无痕」，只是入口从 health.* 换成了 log.acq.*。
//
// 节点级写入的唯一入口是 UpdateNodeScoped（内部加后缀），平台端点因此物理上写不到节点键。
func isWritableSettingKey(key string) bool {
	baseKey, _, scoped := nodeScopeSplit(key)
	if scoped {
		// 带节点后缀的键一律不属平台级白名单，交由 UpdateNodeScoped 处理。
		return false
	}
	switch baseKey {
	}
	switch baseKey {
	case SettingKeyLogLevel, SettingKeyDebugMode,
		SettingKeyJDKMirrorTemurin, SettingKeyJDKMirrorCorretto, SettingKeyJDKMirrorZulu,
		SettingKeyRuntimeMirrorNodeJS,
		SettingKeyGracefulStopTimeout, SettingKeyBackupRetentionDays,
		SettingKeyQuotaEnforceInterval, SettingKeyQuotaEnforceMode, SettingKeyQuotaEnforceStreak,
		SettingKeySnapshotRetentionCount, SettingKeySnapshotRetentionDays, SettingKeySnapshotPreRollbackKeep,
		SettingKeySnapshotMaxPerInstance, SettingKeySnapshotMaxTotalMB,
		SettingKeyCrashStatRetentionDays,
		SettingKeyDirectProbeSLPTimeout, SettingKeyDirectProbeQueryTimeout,
		SettingKeyHealthScanEnabled, SettingKeyHealthScanInterval, SettingKeyHealthProbeKind,
		SettingKeyHealthSuspicionThreshold, SettingKeyHealthAction,
		SettingKeyHealthCircuitThreshold, SettingKeyHealthCircuitWindow,
		SettingKeyHealthStartupWarmup, SettingKeyHealthSelfHealMaxRestarts,
		SettingKeyLogAcqRulesEnabled, SettingKeyLogAcqRealtimeTail,
		SettingKeyLogAcqGzImport, SettingKeyLogAcqGzScanInterval, SettingKeyLogAcqPathOverrides,
		SettingKeyProxyURL, SettingKeyProxyNoProxy,
		SettingKeyOrphanGracePeriod, SettingKeyOrphanAutoDispose,
		SettingKeyBotReclaimGracePeriod, SettingKeyBotReclaimAuto,
		SettingKeyPlatformPublicBaseURL, SettingKeyInviteSMTPHost, SettingKeyInviteSMTPPort,
		SettingKeyInviteSMTPUsername, SettingKeyInviteSMTPPassword, SettingKeyInviteSMTPFrom,
		SettingKeyGitHubToken,
		SettingKeyRateLimitReadRate, SettingKeyRateLimitReadCapacity,
		SettingKeyRateLimitWriteRate, SettingKeyRateLimitWriteCapacity,
		SettingKeyRateLimitLogRate, SettingKeyRateLimitLogCapacity,
		SettingKeyRateLimitGlobalRate, SettingKeyRateLimitGlobalCap:
		return true
	}
	return false
}

// rateLimitSettingKeys 是入口限流的八个键（四组预算 × 速率/容量）。
//
// 为什么把清单集中在这里：写入校验、默认值、Get 展示与热更新回调都按这组键工作，
// 分开维护会出现「某键能写但不生效」的静默差集（本仓库其他族也踩过这个坑）。
var rateLimitSettingKeys = []string{
	SettingKeyRateLimitReadRate, SettingKeyRateLimitReadCapacity,
	SettingKeyRateLimitWriteRate, SettingKeyRateLimitWriteCapacity,
	SettingKeyRateLimitLogRate, SettingKeyRateLimitLogCapacity,
	SettingKeyRateLimitGlobalRate, SettingKeyRateLimitGlobalCap,
}

// IsRateLimitSettingKey 报告某键是否属于入口限流（供写后判断是否要热更新）。
func IsRateLimitSettingKey(key string) bool {
	for _, k := range rateLimitSettingKeys {
		if k == key {
			return true
		}
	}
	return false
}

// RateLimitSettings 汇总当前生效的入口限流预算。
//
// 读取口径与其它设置一致：DB 覆盖 > 默认值。任一组解析失败按该组默认值处理
// （normalize 兜底），绝不让一个脏值把入口限流变成「全拒」或「全放」。
func (s *SettingsService) RateLimitSettings() trafficlimit.Settings {
	overrides, err := s.loadOverrides()
	if err != nil {
		overrides = map[string]string{}
	}
	return s.rateLimitSettingsFromOverrides(overrides)
}

func (s *SettingsService) rateLimitSettingsFromOverrides(overrides map[string]string) trafficlimit.Settings {
	atoi := func(key string) int {
		value, ok := overrides[key]
		if !ok {
			value = s.defaultValue(key)
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return -1 // 交给 Normalize 回落到该组默认值
		}
		return n
	}
	raw := trafficlimit.Settings{
		Read:     trafficlimit.Spec{RatePerSecond: atoi(SettingKeyRateLimitReadRate), Capacity: atoi(SettingKeyRateLimitReadCapacity)},
		Write:    trafficlimit.Spec{RatePerSecond: atoi(SettingKeyRateLimitWriteRate), Capacity: atoi(SettingKeyRateLimitWriteCapacity)},
		LogQuery: trafficlimit.Spec{RatePerSecond: atoi(SettingKeyRateLimitLogRate), Capacity: atoi(SettingKeyRateLimitLogCapacity)},
		Global:   trafficlimit.Spec{RatePerSecond: atoi(SettingKeyRateLimitGlobalRate), Capacity: atoi(SettingKeyRateLimitGlobalCap)},
	}
	return raw.Normalize()
}

// SetRateLimitApplier 注入入口限流热更新回调。
// 保存 traffic.rate_limit.* 后以当前生效预算回调，令 CP 入口立即采用新值、
// 不需要重启；不注入则仅落库、下次重启生效。
func (s *SettingsService) SetRateLimitApplier(fn func(trafficlimit.Settings)) {
	s.rateLimitApplier = fn
}

// applyRateLimits 把当前生效的限流预算推给入口中间件（未注入回调时为空操作）。
func (s *SettingsService) applyRateLimits() {
	if s.rateLimitApplier == nil {
		return
	}
	s.rateLimitApplier(s.RateLimitSettings())
}

// validateRateLimitSetting 校验单个限流键的取值。
//
// 语义：速率与容量是同一组预算的两个分量，必须**同时为正**或**同时为 0**。
//   - 同时为 0 = 该组不限流（超高并发部署的显式选择）；
//   - 只有一个为 0 = 无从推导桶行为（容量 0 会把所有请求拒掉，速率 0 则永不补充），必须拒绝；
//   - 全局组不允许「不限流」：那是被限流保护的对象本末倒置。
func validateRateLimitSetting(key, val string) error {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" || trimmed != val {
		return fmt.Errorf("%w: 限流值须为非负整数且首尾无空白", ErrSettingValueInvalid)
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return fmt.Errorf("%w: 限流值须为非负整数", ErrSettingValueInvalid)
	}
	if n < 0 {
		return fmt.Errorf("%w: 限流值不得为负数", ErrSettingValueInvalid)
	}
	isRate := strings.HasSuffix(key, ".rate_per_second")
	limit := trafficlimit.MaxCapacity
	if isRate {
		limit = trafficlimit.MaxRatePerSecond
	}
	if n > limit {
		if isRate {
			return fmt.Errorf("%w: 速率不得超过 %d 次/秒", ErrSettingValueInvalid, trafficlimit.MaxRatePerSecond)
		}
		return fmt.Errorf("%w: 容量不得超过 %d", ErrSettingValueInvalid, trafficlimit.MaxCapacity)
	}
	// 成对校验：速率与容量必须同为零或同为正。
	// 全局组不允许关闭：单 IP 无限打 CP 正是限流要防的事，故它的两个键都必须为正。
	if (key == SettingKeyRateLimitGlobalRate || key == SettingKeyRateLimitGlobalCap) && n == 0 {
		return fmt.Errorf("%w: 全局上限必须为正数（不允许关闭总闸，否则单个 IP 可无限请求）", ErrSettingValueInvalid)
	}
	return nil
}

// validateRateLimitPair 在整批写入时重新检查每组预算的配对约束。
//
// 单键校验只能看到自己：允许把「速率为 5、容量为 0」逐个写进去（各自都是合法的非负整数），
// 落库后却让该组变成「容量 0 ⇒ 所有请求被拒」。故在 Update 全量校验通过后再按组复核一次，
// 用的值 = 本次提交值覆盖在现有生效值之上（未提交的分量沿用现值）。
func (s *SettingsService) validateRateLimitPair(values map[string]string) error {
	hasAny := false
	for key := range values {
		if IsRateLimitSettingKey(key) {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return nil
	}
	overrides, err := s.loadOverrides()
	if err != nil {
		overrides = map[string]string{}
	}
	effective := func(key string) int {
		if v, ok := values[key]; ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
		if v, ok := overrides[key]; ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
		n, _ := strconv.Atoi(strings.TrimSpace(s.defaultValue(key)))
		return n
	}
	groups := []struct {
		name     string
		rateKey  string
		capKey   string
		allowOff bool
	}{
		{"只读请求", SettingKeyRateLimitReadRate, SettingKeyRateLimitReadCapacity, true},
		{"写操作", SettingKeyRateLimitWriteRate, SettingKeyRateLimitWriteCapacity, true},
		{"日志查询", SettingKeyRateLimitLogRate, SettingKeyRateLimitLogCapacity, true},
		{"全局上限", SettingKeyRateLimitGlobalRate, SettingKeyRateLimitGlobalCap, false},
	}
	for _, g := range groups {
		rate, capacity := effective(g.rateKey), effective(g.capKey)
		if rate == 0 && capacity == 0 {
			if !g.allowOff {
				return fmt.Errorf("%w: %s 不允许关闭（速率与容量须为正数）", ErrSettingValueInvalid, g.name)
			}
			continue
		}
		if rate == 0 || capacity == 0 {
			return fmt.Errorf("%w: %s 的速率与容量必须同时为 0（不限流）或同时为正数", ErrSettingValueInvalid, g.name)
		}
	}
	return nil
}

// validateSettingValue 按键的语义校验写入值。
func validateSettingValue(key, val string) error {
	// Worker 级覆盖键按基础键走同一套校验——后缀只表达作用域，不改变值语义。
	baseKey, _, _ := nodeScopeSplit(key)
	// 入口限流的八个键：非负整数 + 上界 + 全局组不允许 0（成对约束由 Update 另行复核）。
	if IsRateLimitSettingKey(baseKey) {
		return validateRateLimitSetting(baseKey, val)
	}
	switch baseKey {
	case SettingKeyLogLevel:
		if !config.ValidLogLevel(val) {
			return fmt.Errorf("%w: 日志级别须为 debug|info|warn|error", ErrSettingValueInvalid)
		}
	case SettingKeyDebugMode:
		if val != "true" && val != "false" {
			return fmt.Errorf("%w: 调试模式须为 true|false", ErrSettingValueInvalid)
		}
	case SettingKeyGracefulStopTimeout:
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return fmt.Errorf("%w: 优雅停止超时须为正的 Go duration（如 30s）", ErrSettingValueInvalid)
		}
	case SettingKeyDirectProbeSLPTimeout, SettingKeyDirectProbeQueryTimeout:
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return fmt.Errorf("%w: MC 直探超时须为正的 Go duration（如 3s）", ErrSettingValueInvalid)
		}
		// 上界守护：直探超时是每实例每拍的最坏阻塞量，过大直接把心跳节拍拖垮（与
		// 审计项 2 / 复审 NEW-ISSUE A）。上界由节拍护栏反推（directprobe.MaxTimeout），
		// 超出拒绝而非静默截断，使「可配」与「护栏」始终自洽。
		if d > maxDirectProbeTimeout {
			return fmt.Errorf("%w: MC 直探超时不得大于 %s（单拍采集预算 = 余量 + 探针 %s + slp + query，"+
				"必须小于 %s 心跳节拍）", ErrSettingValueInvalid,
				maxDirectProbeTimeout, probeScrapeTimeoutCap, directprobe.HeartbeatInterval)
		}
	case SettingKeyHealthScanEnabled:
		if val != "true" && val != "false" {
			return fmt.Errorf("%w: 健康巡检总开关须为 true|false", ErrSettingValueInvalid)
		}
	case SettingKeyQuotaEnforceInterval:
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return fmt.Errorf("%w: 配额巡检周期须为正的 Go duration（如 30s）", ErrSettingValueInvalid)
		}
	case SettingKeyQuotaEnforceMode:
		if val != "alert" && val != "throttle" && val != "stop" {
			return fmt.Errorf("%w: 配额处置档位须为 alert|throttle|stop", ErrSettingValueInvalid)
		}
	case SettingKeyQuotaEnforceStreak:
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("%w: 配额连续超限阈值须为 ≥1 的整数", ErrSettingValueInvalid)
		}
	case SettingKeySnapshotRetentionCount:
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("%w: 快照保留条数须为 ≥1 的整数", ErrSettingValueInvalid)
		}
	case SettingKeySnapshotPreRollbackKeep:
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("%w: pre_rollback 保留条数须为 ≥1 的整数", ErrSettingValueInvalid)
		}
	case SettingKeySnapshotRetentionDays, SettingKeyCrashStatRetentionDays:
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("%w: 保留天数须为 ≥1 的整数", ErrSettingValueInvalid)
		}
	case SettingKeySnapshotMaxPerInstance, SettingKeySnapshotMaxTotalMB:
		// m-2 磁盘防护开关（R4）：两键默认/兜底口径都是 **0=不限**，故 0 与正数接受、只拒负数。
		// 与 intSetting 的回落条件（`err != nil || n < 0` 才用 fallback，0 会被采纳）严格一致：
		// 若这里改判 `n < 1`，运维就**无法表达「不限」**（如 max_total_mb 想关掉总量闸），
		// 「可配」又在另一个方向上失真。负数是唯一真正无意义的取值（无对应语义、且会被
		// intSetting 静默回落为不限，写进去等于没写）。
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("%w: 该上限须为 ≥0 的整数（0=不限）", ErrSettingValueInvalid)
		}
	case SettingKeyHealthScanInterval:
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return fmt.Errorf("%w: 健康巡检周期须为正的 Go duration（如 30s）", ErrSettingValueInvalid)
		}
	case SettingKeyHealthSuspicionThreshold:
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("%w: 假死判定阈值须为 ≥1 的整数", ErrSettingValueInvalid)
		}
	case SettingKeyHealthAction:
		if val != "warn" && val != "restart" {
			return fmt.Errorf("%w: 假死动作须为 warn|restart", ErrSettingValueInvalid)
		}
	case SettingKeyHealthCircuitThreshold:
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("%w: 崩溃熔断阈值须为 ≥1 的整数", ErrSettingValueInvalid)
		}
	case SettingKeyHealthCircuitWindow:
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return fmt.Errorf("%w: 崩溃熔断窗口须为正的 Go duration（如 10m）", ErrSettingValueInvalid)
		}
	case SettingKeyHealthStartupWarmup:
		d, err := time.ParseDuration(val)
		if err != nil || d < 0 {
			return fmt.Errorf("%w: 启动宽限期须为非负的 Go duration（如 5m；0 表示回退默认）", ErrSettingValueInvalid)
		}
	case SettingKeyHealthSelfHealMaxRestarts:
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("%w: 假死自愈重启上限须为 ≥1 的整数", ErrSettingValueInvalid)
		}
	case SettingKeyHealthProbeKind:
		if val != "" && val != "tcp" && val != "http" {
			return fmt.Errorf("%w: 探针类型须为 tcp|http 或留空(auto)", ErrSettingValueInvalid)
		}
	// 日志采集。逐项语义校验，不做通用字符串透传——否则非法值会在心跳下发后
	// 被 Worker 静默回退默认，用户以为配上了却不生效。
	case SettingKeyLogAcqRulesEnabled, SettingKeyLogAcqRealtimeTail, SettingKeyLogAcqGzImport:
		if val != "true" && val != "false" {
			return fmt.Errorf("%w: 日志采集开关须为 true|false", ErrSettingValueInvalid)
		}
	case SettingKeyLogAcqGzScanInterval:
		// 空值 = **未配置**，由该节点 worker.yml 的 `log_ingest.archive_scan_interval` 决定
		// （2026-10-20 审查修正：此前注释与错误消息都把它说成「关闭」，与 wire 契约相反——
		// proto 明确「0 = 未配置 ⇒ 回落基线」，Worker 的 ArchiveScanInterval() 也只在 >0 时
		// 才用本值。说成「关闭」会让运维以为已停扫，实际仍按基线周期在扫；真正能停掉定时
		// 扫描的只有把 log.acq.gz_import_enabled 置 false）。
		//
		// 非空须为正 duration，且**有上界**：下发侧是 int32 毫秒
		// （grpc/handler.go 的 int32(p.GzScanInterval/time.Millisecond)），超过约 24.8 天会
		// 整数截断——`720h`(30天) 截成负数 ⇒ Worker 归零当「未配置」；`1200h`(50天) 截成
		// 25032704ms ≈ 6.96 小时 ⇒ 被当成合法周期照用，用户配 50 天却每 7 小时扫一次。
		if strings.TrimSpace(val) != "" {
			d, err := time.ParseDuration(val)
			if err != nil || d <= 0 {
				return fmt.Errorf("%w: gz 扫描周期须留空（交由该节点 worker.yml 基线决定；要停止定时扫描请关闭 gz 导入总开关）或为正的 Go duration（如 1h）", ErrSettingValueInvalid)
			}
			if d/time.Millisecond > math.MaxInt32 {
				return fmt.Errorf("%w: gz 扫描周期上限约 24 天（协议以 int32 毫秒下发，更长会被截断成错误值）", ErrSettingValueInvalid)
			}
			// 下界：不足 1ms 会被截断成 0，语义变回「未配置」。
			if d < time.Millisecond {
				return fmt.Errorf("%w: gz 扫描周期不得小于 1ms（协议以毫秒下发，更短会被截断成 0 = 未配置）", ErrSettingValueInvalid)
			}
		}
	case SettingKeyLogAcqPathOverrides:
		// 空值 = 无覆盖。非空须为 JSON 对象文本（值一律字符串），供 Worker 直接解析。
		if strings.TrimSpace(val) != "" {
			var m map[string]string
			if err := json.Unmarshal([]byte(val), &m); err != nil {
				return fmt.Errorf(`%w: 路径覆盖须为 JSON 对象文本（形如 {"inst:1":"proxy.log.0"}）`, ErrSettingValueInvalid)
			}
			// 逐条校验路径本身（2026-10-19，安全约束）：必须是相对实例工作目录的路径，
			// 且折叠 `..` 后不得逃出。放行绝对路径等于允许把任意实例的日志源指向宿主机
			// 任意可读文件（/etc/shadow 之类），Worker 会照读并入库。
			// 键也校验：必须是 `inst:<id>` 形态，否则该覆盖永远不会命中任何实例，
			// 静默失效比报错更难排查。键的校验与 Worker 侧 policy 的 targetID 语义一致。
			for target, p := range m {
				// 键必须是**规范**的 `inst:<十进制ID>`（2026-10-20 审查修正）。
				// 只查前缀会放行 `inst:abc`、`inst: 1`、`inst:1/2`、全角 `inst:１` 等变体：
				// Worker 侧按 binding.TargetID（恒为 `inst:<数字>`）精确匹配，这些键永远命中
				// 不到任何实例——与「写入 200 但永不生效」同类，且没有报错可循。
				// 规范化写法照 router/log_runtime.go 的 ParseUint + 回拼比对。
				idText, ok := strings.CutPrefix(target, "inst:")
				if !ok || idText == "" {
					return fmt.Errorf("%w: 路径覆盖的键须为 inst:<实例ID>（got %q）", ErrSettingValueInvalid, target)
				}
				id, convErr := strconv.ParseUint(idText, 10, 64)
				if convErr != nil || target != fmt.Sprintf("inst:%d", id) {
					return fmt.Errorf("%w: 路径覆盖的键须为规范的 inst:<十进制实例ID>（got %q）", ErrSettingValueInvalid, target)
				}
				trimmed := strings.TrimSpace(p)
				if trimmed == "" {
					return fmt.Errorf("%w: 路径覆盖的值不能为空（%s）", ErrSettingValueInvalid, target)
				}
				// 首尾空白直接拒绝而不静默 trim：validateSettingValue 只校验、不回写规范值，
				// 「校验时 trim、落库仍是原串」会让 Worker 拼出带空格的路径而找不到文件，
				// 表现为「保存成功但查不到数据」。与 github.token 的「含空白整体拒绝」同口径。
				if trimmed != p {
					return fmt.Errorf("%w: 路径覆盖的值首尾不得有空白（%s = %q）", ErrSettingValueInvalid, target, p)
				}
				if filepath.IsAbs(filepath.FromSlash(trimmed)) {
					return fmt.Errorf("%w: 路径覆盖须为相对实例工作目录的路径，不接受绝对路径（%s = %q）", ErrSettingValueInvalid, target, p)
				}
				// Windows 形态（盘符 / UNC）在 Linux 的 CP 上 IsAbs 为 false，但下发给
				// Windows 节点时会被 Worker 判为绝对路径并拒绝登记——届时表现为
				// 「保存成功、实例日志登记失败」。这里提前拒掉。
				if isWindowsAbsPath(trimmed) {
					return fmt.Errorf("%w: 路径覆盖不支持 Windows 盘符或 UNC 路径（%s = %q）", ErrSettingValueInvalid, target, p)
				}
				// Windows 分隔符下的逃逸与盘符/UNC 同属「CP 放行、Windows 节点必拒」的一类：
				// 下面那道宿主判据在 Linux 上把 `..\..\etc\passwd` 当普通文件名放行，
				// 到 Windows 节点才失败。这里提前拒掉，让用户在保存时就看到错误。
				if escapesUnderWindowsSeparators(trimmed) {
					return fmt.Errorf("%w: 路径覆盖不得逃出实例工作目录（按 Windows 分隔符语义）（%s = %q）", ErrSettingValueInvalid, target, p)
				}
				clean := filepath.Clean(filepath.FromSlash(trimmed))
				if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
					return fmt.Errorf("%w: 路径覆盖不得逃出实例工作目录（%s = %q）", ErrSettingValueInvalid, target, p)
				}
			}
		}
	case SettingKeyBackupRetentionDays:
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("%w: 备份保留天数须为非负整数", ErrSettingValueInvalid)
		}
	case SettingKeyJDKMirrorTemurin, SettingKeyJDKMirrorCorretto, SettingKeyJDKMirrorZulu,
		SettingKeyRuntimeMirrorNodeJS:
		if val == "" {
			return fmt.Errorf("%w: 镜像源不能为空", ErrSettingValueInvalid)
		}
	case SettingKeyProxyURL:
		// 复用 httpclient 的 URL/scheme 校验：空=清除代理覆盖（合法，回退 yaml/env）；
		// 非空但非法（不支持 scheme / 不可解析）则拒绝，不静默直连。
		if val != "" {
			if _, err := httpclient.New(httpclient.Config{URL: val}); err != nil {
				return fmt.Errorf("%w: 代理地址非法（%v）", ErrSettingValueInvalid, err)
			}
		}
	case SettingKeyOrphanGracePeriod:
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return fmt.Errorf("%w: 无主运行时宽限期须为正的 Go duration（如 10m）", ErrSettingValueInvalid)
		}
	case SettingKeyOrphanAutoDispose:
		if val != "true" && val != "false" {
			return fmt.Errorf("%w: 自动处置须为 true|false", ErrSettingValueInvalid)
		}
	case SettingKeyBotReclaimGracePeriod:
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return fmt.Errorf("%w: 失效 Bot 回收宽限期须为正的 Go duration（如 2m）", ErrSettingValueInvalid)
		}
	case SettingKeyBotReclaimAuto:
		if val != "true" && val != "false" {
			return fmt.Errorf("%w: 失效 Bot 自动回收须为 true|false", ErrSettingValueInvalid)
		}
	case SettingKeyPlatformPublicBaseURL:
		if err := validatePublicBaseURL(val); err != nil {
			return fmt.Errorf("%w: %v", ErrSettingValueInvalid, err)
		}
	case SettingKeyInviteSMTPPort:
		if val != "" {
			port, err := strconv.Atoi(val)
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("%w: SMTP 端口须为 1-65535", ErrSettingValueInvalid)
			}
		}
	case SettingKeyInviteSMTPFrom:
		if val != "" {
			address, err := mail.ParseAddress(val)
			if err != nil || address.Address != val {
				return fmt.Errorf("%w: SMTP 发件人地址非法", ErrSettingValueInvalid)
			}
		}
	case SettingKeyInviteSMTPPassword:
		if val != "" && !environmentReferencePattern.MatchString(val) {
			return fmt.Errorf("%w: SMTP 密码必须为 ${ENV_VAR} 引用", ErrSettingValueInvalid)
		}
	case SettingKeyGitHubToken:
		// 空=清除覆盖回退 yml/env 基线；非空允许字面令牌或 ${ENV_VAR} 引用，仅挡含空白的明显误贴
		//（令牌与环境变量名都不含空白）。实际能否通过 GitHub 鉴权由调用结果反馈，不做格式臆测。
		if strings.TrimSpace(val) != val || strings.ContainsAny(val, " \t\r\n") {
			return fmt.Errorf("%w: GitHub 令牌不能含空白字符", ErrSettingValueInvalid)
		}
	}
	return nil
}

// wsTokenSecretDisplay 生成 jwt.ws_secret 只读展示值。
// 显式配置 → 掩码；未配置 → 按 dev_mode 推导来源描述（不回显 autogen 解析出的密钥值）。
func (s *SettingsService) wsTokenSecretDisplay() string {
	if s.cfg.JWT.WSSecret != "" {
		return maskSecret(s.cfg.JWT.WSSecret)
	}
	if s.cfg.Server.DevMode {
		return "(dev 回退)"
	}
	return "(自动生成: 数据根 etc/ws-token-secret.key)"
}

// maskSecret 对密钥类敏感值脱敏：保留首尾各 3 字符，中间以 *** 代替；过短则全部打码。
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 6 {
		return "******"
	}
	return s[:3] + "***" + s[len(s)-3:]
}

// maskDSN 对数据库 DSN 脱敏：sqlite 路径无凭证可原样返回；含 user:pass@ 时打掉口令段。
func maskDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	// 形如 user:pass@tcp(host)/db 的 MySQL DSN：打掉 ":pass@" 中的口令。
	at := indexByte(dsn, '@')
	colon := indexByte(dsn, ':')
	if at > 0 && colon >= 0 && colon < at {
		return dsn[:colon+1] + "***" + dsn[at:]
	}
	// sqlite 文件路径等无凭证 DSN：原样返回（不含敏感信息）。
	return dsn
}

// indexByte 返回 b 在 s 中首次出现的下标，未找到返回 -1。
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
