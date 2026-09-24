package service

import (
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/directprobe"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// 平台设置可写白名单键（FR-063 / ADR-015）。
// 只有这些键允许经 PUT /settings 落库覆盖，其余键一律拒绝。
const (
	// SettingKeyLogLevel CP 日志级别（debug|info|warn|error）。落库即时生效（slog LevelVar）。
	SettingKeyLogLevel = "log.level"
	// SettingKeyDebugMode 调试模式总开关（true|false，FR-225，增强 FR-063）。落库即时生效：
	// 开=日志 debug + Gin debug 模式；关=日志回 log.level 基线 + Gin release（默认 false，启动即静默）。
	SettingKeyDebugMode = "debug.mode"
	// SettingKeyJDKMirrorTemurin / Corretto / Zulu JDK 下载镜像源基址。
	// 安装 JDK 时 CP 取生效值经 InstallJDKRequest.mirror_base 下发 Worker，使配置真生效（FR-063）。
	SettingKeyJDKMirrorTemurin  = "jdk.mirror.temurin"
	SettingKeyJDKMirrorCorretto = "jdk.mirror.corretto"
	SettingKeyJDKMirrorZulu     = "jdk.mirror.zulu"
	// SettingKeyRuntimeMirrorNodeJS Node.js dist 下载镜像源基址（FR-299，语义同 jdk.mirror.*）。
	// 安装 Node.js 时 CP 取生效值经 InstallRuntimeRequest.mirror_base 下发 Worker。
	SettingKeyRuntimeMirrorNodeJS = "runtime.mirror.nodejs"
	// SettingKeyGracefulStopTimeout 优雅停止超时（Go duration 文本）。
	// 启动实例时 CP 取生效值经 CreateInstanceRequest 下发 Worker→wrapper，对其后新启动的实例生效（FR-063）。
	SettingKeyGracefulStopTimeout = "graceful_stop.timeout"
	// SettingKeyDirectProbeSLPTimeout / QueryTimeout 是 MC 直探（SLP / Query）超时（Go duration 文本，FR-446）。
	// 心跳响应按拍下发（HeartbeatResponse.direct_probe_*_timeout_ms），Worker 填入采集编排链；
	// 与 graceful_stop.timeout 同风格：DB 覆盖 > 基线默认，Worker 侧生效、无需重启。
	SettingKeyDirectProbeSLPTimeout   = "direct_probe.slp_timeout"
	SettingKeyDirectProbeQueryTimeout = "direct_probe.query_timeout"
	// SettingKeyHealth* 是实例健康巡检与自愈（FR-459）策略：心跳响应按拍下发
	// （HeartbeatResponse.health_*），Worker 写入巡检器生效值；与 direct_probe.* 同风格
	// （DB 覆盖 > 基线默认，Worker 侧生效、无需重启）。
	//   - scan_enabled 巡检总开关（默认 true）；
	//   - scan_interval 巡检周期（默认 30s）；
	//   - probe_kind 响应维度探针类型 tcp|http|空(auto)；
	//   - suspicion_threshold 连续判假死次数阈值（默认 3）；
	//   - action 假死动作 warn（默认）|restart；
	//   - circuit_breaker_threshold 熔断窗口内重启次数阈值（默认 5）；
	//   - circuit_breaker_window 熔断滚动窗口（默认 10m）；
	//   - startup_warmup 启动宽限期（默认 5m，FR-459 复审项 2：慢启动 MC 不被误判假死）；
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
	// SettingKeyBackupRetentionDays 默认备份保留天数（整数）。CP 后台巡检据此裁剪超期备份（FR-063）。
	SettingKeyBackupRetentionDays = "backup.retention_days"
	// SettingKeyQuotaEnforceInterval 运行期配额强制巡检周期（Go duration，FR-467）。默认 60s（M-2）。
	SettingKeyQuotaEnforceInterval = "quota.enforce_interval"
	// SettingKeyQuotaEnforceMode 平台默认的运行期配额超限处置档位（alert|throttle|stop，FR-467）。
	// 默认 alert：最保守，绝不因配额误停实例；组的 EnforceMode 可覆盖本值。
	SettingKeyQuotaEnforceMode = "quota.enforce_mode"
	// SettingKeyQuotaEnforceStreak 连续超限拍数阈值（整数，FR-467）。默认 5（M-2：与 60s 组合约 5 分钟窗口）。
	SettingKeyQuotaEnforceStreak = "quota.enforce_streak"
	// SettingKeySnapshotRetentionCount 实例快照按条数保留上限（整数，FR-466）。默认 10。
	SettingKeySnapshotRetentionCount = "snapshot.retention_count"
	// SettingKeySnapshotRetentionDays 实例快照按天数保留上限（整数，FR-466）。默认 30。
	SettingKeySnapshotRetentionDays = "snapshot.retention_days"
	// SettingKeySnapshotPreRollbackKeep pre_rollback 快照保留条数下限（整数，FR-466）。
	// pre_rollback 不受 snapshot.retention_count 裁剪，至少保留这么多条，避免「回滚把回滚点挤掉」。
	SettingKeySnapshotPreRollbackKeep = "snapshot.pre_rollback_keep"
	// SettingKeySnapshotMaxPerInstance 单实例快照条数上限（整数，FR-466 m-2）。默认 20；0=不限。
	// 创建前保护：拦截「保留策略尚未生效就把磁盘写满」的自伤（快照是全量归档）。
	SettingKeySnapshotMaxPerInstance = "snapshot.max_per_instance"
	// SettingKeySnapshotMaxTotalMB 单实例快照总占用上限（MiB 整数，FR-466 m-2）。默认 0=不限。
	SettingKeySnapshotMaxTotalMB = "snapshot.max_total_mb"
	// SettingKeyCrashStatRetentionDays 崩溃趋势统计保留天数（整数，FR-470）。默认 90。
	SettingKeyCrashStatRetentionDays = "crash.stat_retention_days"
	// SettingKeyProxyURL CP 出站代理地址（network 类，FR-185/ADR-043）。敏感（脱敏展示）。
	// 落库即重建 CP 出站持有者（优先级 DB > control-plane.yml > env）；同时作为各节点默认代理。
	SettingKeyProxyURL = "proxy.url"
	// SettingKeyProxyNoProxy CP/全局默认出站代理的免代理列表（逗号分隔，语义同 NO_PROXY，FR-185）。
	SettingKeyProxyNoProxy = "proxy.no_proxy"
	// SettingKeyOrphanGracePeriod 无主运行时宽限期（Go duration，FR-326）。默认 10m；首次发现后观察期内若 CP 又有记录则取消。
	SettingKeyOrphanGracePeriod = "instance_reverse_reconcile.grace_period"
	// SettingKeyOrphanAutoDispose 宽限后是否自动下发处置（true|false，FR-326）。默认 false：只列表/日志，管理员手动确认。
	SettingKeyOrphanAutoDispose = "instance_reverse_reconcile.auto_dispose"
	// SettingKeyBotReclaimGracePeriod 失效 Bot 回收宽限期（Go duration，FR-460）。默认 2m——Bot 生命周期短，容忍一次世代迁移窗口即可。
	SettingKeyBotReclaimGracePeriod = "bot_reclaim.grace_period"
	// SettingKeyBotReclaimAuto 宽限后是否自动回收失效 Bot（true|false，FR-460）。默认 true，但仅对 Fleet 归属 Bot 生效；V1 手动 Bot 永不自动回收。
	SettingKeyBotReclaimAuto = "bot_reclaim.auto_reclaim"
	// SettingKeyBotZombieSessionIdleThreshold 僵尸压测会话停滞阈值（Go duration，FR-472）。
	// 默认 10m——会话在线 Bot 数为 0 且停滞超过该时长即收敛为 stopped，终止对失联会话的无效补足与写放大。
	SettingKeyBotZombieSessionIdleThreshold = "bot_reclaim.zombie_session_idle_threshold"
	// SettingKeyPlatformPublicBaseURL 是平台生成绝对链接唯一允许使用的公共基址（FR-405）。
	// 允许 HTTP 供无 TLS 的自托管内网使用；不允许由请求头推断。
	SettingKeyPlatformPublicBaseURL = "platform.public_base_url"
	// SettingKeyInviteSMTPHost / Port / Username / Password / From 是独立于告警通道的邀请邮件配置。
	SettingKeyInviteSMTPHost     = "invite.smtp.host"
	SettingKeyInviteSMTPPort     = "invite.smtp.port"
	SettingKeyInviteSMTPUsername = "invite.smtp.username"
	SettingKeyInviteSMTPPassword = "invite.smtp.password"
	SettingKeyInviteSMTPFrom     = "invite.smtp.from"
	// SettingKeyGitHubToken 可选 GitHub API token（敏感，脱敏展示）。落库即时生效（各 GitHub API
	// 调用点在发请求时读取生效值）：自更新（FR-175）与 ServerProbe 制品版本库同步（FR-409）共用，
	// 非空时请求带 Authorization 把额度从匿名 60 次/时/IP 提升到 5000 次/时——共享出口 IP 耗尽
	// 匿名额度即 403。基线取 update.github_token（yml/env），优先级 DB > yml > env；值支持
	// ${ENV_VAR} 引用（与 SMTP 密码同约定）或字面令牌。明文落库的暴露面与 proxy.url 含凭据时一致。
	SettingKeyGitHubToken = "github.token"
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

// SettingsService 平台配置服务（FR-063 / ADR-015）。
//
// 在 YAML+env 基线（cfg）之上叠加 platform_settings 的 DB 覆盖层，
// 解析「有效配置」、按白名单读写、并对可即时生效项接到真实读取点。
type SettingsService struct {
	db  *gorm.DB
	cfg *config.Config
	// proxyRebuilder 在 proxy.* 覆盖落库后被调用，令 CP 重建出站持有者使新代理即时生效（FR-185）。
	// 由 main 注入（传入重建 httpclient.Provider 的闭包）；为 nil 时不重建（仅落库，下次重启生效）。
	proxyRebuilder func(httpclient.Config)
	// ginModeApplier 在 debug.mode 切换 / 启动基线时被调用切 Gin 模式（true=debug / false=release，FR-225）。
	// 由 main 注入（闭包调 gin.SetMode），使 gin 依赖留在入口层、不渗入 service。为 nil 时仅切日志级别。
	ginModeApplier func(debug bool)
}

// NewSettingsService 创建平台配置服务。
// 启动时把已落库的可即时生效覆盖项重放到运行时读取点（如日志级别），保证重启后覆盖仍生效。
func NewSettingsService(db *gorm.DB, cfg *config.Config) *SettingsService {
	s := &SettingsService{db: db, cfg: cfg}
	s.applyPersistedOverrides()
	return s
}

// SetProxyRebuilder 注入 CP 出站持有者重建回调（FR-185，见 ADR-043）。
// 保存 proxy.url/proxy.no_proxy 后以「当前生效代理」（DB 覆盖 > yaml > env）回调之，
// 令 CP 自身出站立即走新代理、免重启。不注入则代理覆盖仅落库、下次重启生效。
func (s *SettingsService) SetProxyRebuilder(fn func(httpclient.Config)) {
	s.proxyRebuilder = fn
}

// SetGinModeApplier 注入 Gin 模式切换回调（FR-225）。由 main 注入闭包调 gin.SetMode，使 gin 依赖留在入口层。
// 须在 ApplyDebugBaseline 前注入，以便启动按 debug.mode 设初始 Gin 模式。
func (s *SettingsService) SetGinModeApplier(fn func(debug bool)) {
	s.ginModeApplier = fn
}

// ApplyDebugBaseline 按当前生效 debug.mode 设初始 Gin 模式 + 日志级别（FR-225）。
// main 须在 router.Setup 前调用：默认 debug.mode=false → Gin release（不刷 [GIN-debug] 路由噪音）+ 日志 info。
func (s *SettingsService) ApplyDebugBaseline() {
	s.applyDebugMode(s.debugModeOn())
}

// debugModeOn 报告调试模式当前是否开启（生效值 == "true"）。
func (s *SettingsService) debugModeOn() bool {
	return s.EffectiveValue(SettingKeyDebugMode) == "true"
}

// applyDebugMode 应用调试模式（FR-225）：开=日志强制 debug + Gin debug；关=日志回 log.level 基线 + Gin release。
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

// EffectiveProxy 返回 CP 当前生效的出站代理配置（FR-185，见 ADR-043）。
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

// DirectProbeTimeouts 返回 MC 直探（SLP / Query）当前生效超时（FR-446）。
// 心跳响应据此下发毫秒值给 Worker，使其填入采集编排链（无需重启 Worker）。
// 值非法/无覆盖时回退基线默认（3s），保证消费方始终拿到可用正超时。
func (s *SettingsService) DirectProbeTimeouts() (slp, query time.Duration) {
	return parseDurationOr(s.EffectiveValue(SettingKeyDirectProbeSLPTimeout), defaultDirectProbeTimeout),
		parseDurationOr(s.EffectiveValue(SettingKeyDirectProbeQueryTimeout), defaultDirectProbeTimeout)
}

// defaultDirectProbeTimeout / maxDirectProbeTimeout / probeScrapeTimeoutCap 是 MC 直探相关数值。
//
// **单一来源**：一律引用 internal/platform/directprobe（FR-446 复审 NEW-ISSUE A），
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
// 仅供直探超时（SLP/Query）读取，故读侧钳制与写侧校验对称（FR-446 复审 N3）：基线（yaml/env）或
// 历史落库值即便超限也只以 maxDirectProbeTimeout 生效，不让无限大超时冻结详情页与采集链。
//
// 归一委托 directprobe.NormalizeTimeout（FR-446 复审 NEW-ISSUE A）：与 Worker 侧归一、CP 写校验
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
		// 调试模式总开关（FR-225）：开=日志 debug + Gin debug，关=info + release，运行时即时生效。
		s.editableItem(SettingKeyDebugMode, s.defaultValue(SettingKeyDebugMode), overrides, true),
		s.editableItem(SettingKeyJDKMirrorTemurin, s.defaultValue(SettingKeyJDKMirrorTemurin), overrides, false),
		s.editableItem(SettingKeyJDKMirrorCorretto, s.defaultValue(SettingKeyJDKMirrorCorretto), overrides, false),
		s.editableItem(SettingKeyJDKMirrorZulu, s.defaultValue(SettingKeyJDKMirrorZulu), overrides, false),
		// Node.js dist 镜像源（FR-299）：随安装下发 Worker（同 jdk.mirror.*，非 CP 内即时生效）。
		s.editableItem(SettingKeyRuntimeMirrorNodeJS, s.defaultValue(SettingKeyRuntimeMirrorNodeJS), overrides, false),
		s.editableItem(SettingKeyGracefulStopTimeout, s.defaultValue(SettingKeyGracefulStopTimeout), overrides, false),
		// MC 直探超时（FR-446）：经心跳下发 Worker，Worker 侧生效（非 CP 内即时生效）。
		s.editableItem(SettingKeyDirectProbeSLPTimeout, s.defaultValue(SettingKeyDirectProbeSLPTimeout), overrides, false),
		s.editableItem(SettingKeyDirectProbeQueryTimeout, s.defaultValue(SettingKeyDirectProbeQueryTimeout), overrides, false),
		// 实例健康巡检与自愈（FR-459）：经心跳下发 Worker，Worker 侧生效（非 CP 内即时生效）。
		s.editableItem(SettingKeyHealthScanEnabled, s.defaultValue(SettingKeyHealthScanEnabled), overrides, false),
		s.editableItem(SettingKeyHealthScanInterval, s.defaultValue(SettingKeyHealthScanInterval), overrides, false),
		s.editableItem(SettingKeyHealthProbeKind, s.defaultValue(SettingKeyHealthProbeKind), overrides, false),
		s.editableItem(SettingKeyHealthSuspicionThreshold, s.defaultValue(SettingKeyHealthSuspicionThreshold), overrides, false),
		s.editableItem(SettingKeyHealthAction, s.defaultValue(SettingKeyHealthAction), overrides, false),
		s.editableItem(SettingKeyHealthCircuitThreshold, s.defaultValue(SettingKeyHealthCircuitThreshold), overrides, false),
		s.editableItem(SettingKeyHealthCircuitWindow, s.defaultValue(SettingKeyHealthCircuitWindow), overrides, false),
		s.editableItem(SettingKeyHealthStartupWarmup, s.defaultValue(SettingKeyHealthStartupWarmup), overrides, false),
		s.editableItem(SettingKeyHealthSelfHealMaxRestarts, s.defaultValue(SettingKeyHealthSelfHealMaxRestarts), overrides, false),
		s.editableItem(SettingKeyBackupRetentionDays, s.defaultValue(SettingKeyBackupRetentionDays), overrides, false),
		// 运行期配额强制（FR-467）：CP 后台巡检读取生效值（即时生效，下一拍用新值）。
		s.editableItem(SettingKeyQuotaEnforceInterval, s.defaultValue(SettingKeyQuotaEnforceInterval), overrides, true),
		s.editableItem(SettingKeyQuotaEnforceMode, s.defaultValue(SettingKeyQuotaEnforceMode), overrides, true),
		s.editableItem(SettingKeyQuotaEnforceStreak, s.defaultValue(SettingKeyQuotaEnforceStreak), overrides, true),
		// 实例快照保留（FR-466）：CP 后台裁剪循环读取生效值。
		s.editableItem(SettingKeySnapshotRetentionCount, s.defaultValue(SettingKeySnapshotRetentionCount), overrides, true),
		s.editableItem(SettingKeySnapshotRetentionDays, s.defaultValue(SettingKeySnapshotRetentionDays), overrides, true),
		s.editableItem(SettingKeySnapshotPreRollbackKeep, s.defaultValue(SettingKeySnapshotPreRollbackKeep), overrides, true),
		// 快照创建上限（FR-466 m-2）：创建前保护，读侧即时生效。
		s.editableItem(SettingKeySnapshotMaxPerInstance, s.defaultValue(SettingKeySnapshotMaxPerInstance), overrides, true),
		s.editableItem(SettingKeySnapshotMaxTotalMB, s.defaultValue(SettingKeySnapshotMaxTotalMB), overrides, true),
		// 崩溃趋势统计保留（FR-470）：日粒度统计表的清理窗口。
		s.editableItem(SettingKeyCrashStatRetentionDays, s.defaultValue(SettingKeyCrashStatRetentionDays), overrides, true),
		// 出站代理（network 类，FR-185/ADR-043）：保存即在 CP 内重建出站持有者（即时生效）。
		// proxy.url 标 sensitive：含凭据时回显脱敏（仅展示 scheme://host:port），不外泄明文密码。
		s.proxyURLItem(overrides),
		s.editableItem(SettingKeyProxyNoProxy, s.defaultValue(SettingKeyProxyNoProxy), overrides, true),
		// 实例反向对账护栏（FR-326）：宽限期与自动处置开关；读侧即时生效（下一次心跳观察即用）。
		s.editableItem(SettingKeyOrphanGracePeriod, s.defaultValue(SettingKeyOrphanGracePeriod), overrides, true),
		s.editableItem(SettingKeyOrphanAutoDispose, s.defaultValue(SettingKeyOrphanAutoDispose), overrides, true),
		// 失效 Bot 自动回收护栏（FR-460）：宽限期与自动回收开关；读侧即时生效（下一拍巡检即用）。
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
	}

	readOnly := []SettingItem{
		readOnlyItem("server.host", s.cfg.Server.Host, false),
		readOnlyItem("server.port", strconv.Itoa(s.cfg.Server.Port), false),
		readOnlyItem("grpc.port", strconv.Itoa(s.cfg.GRPC.Port), false),
		readOnlyItem("database.driver", s.cfg.Database.Driver, false),
		readOnlyItem("database.dsn", maskDSN(s.cfg.Database.DSN), true),
		readOnlyItem("jwt.secret", maskSecret(s.cfg.JWT.Secret), true),
		// CP↔Worker WS 令牌密钥（FR-275，见 ADR-061）：显式配置时掩码显示；未配置时展示来源
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

	// 落库成功后应用可即时生效项（CP 内读取点）。
	for key, val := range values {
		s.applyOverride(key, val)
	}

	// 本批涉及出站代理键 → 以当前生效代理（DB 覆盖 > yaml > env）重建 CP 出站持有者（FR-185/ADR-043）。
	// 单独于 applyOverride：重建不依赖单个键值、而依赖「全部覆盖叠加后的生效代理」，故落库后整体算一次。
	if _, ok := values[SettingKeyProxyURL]; ok {
		s.rebuildProxy()
	} else if _, ok := values[SettingKeyProxyNoProxy]; ok {
		s.rebuildProxy()
	}
	return nil
}

// rebuildProxy 以当前生效代理重建 CP 出站持有者（FR-185）。rebuilder 未注入时静默跳过（仅落库）。
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

// proxyURLItem 组装出站代理地址项（FR-185）：可编辑 + sensitive，回显时脱敏含凭据的 URL，
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
	switch key {
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
	case SettingKeyBackupRetentionDays:
		return strconv.Itoa(s.cfg.LogStore.RetentionDays)
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

// isWritableSettingKey 报告键是否在可写白名单内。
func isWritableSettingKey(key string) bool {
	switch key {
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
		SettingKeyProxyURL, SettingKeyProxyNoProxy,
		SettingKeyOrphanGracePeriod, SettingKeyOrphanAutoDispose,
		SettingKeyBotReclaimGracePeriod, SettingKeyBotReclaimAuto,
		SettingKeyPlatformPublicBaseURL, SettingKeyInviteSMTPHost, SettingKeyInviteSMTPPort,
		SettingKeyInviteSMTPUsername, SettingKeyInviteSMTPPassword, SettingKeyInviteSMTPFrom,
		SettingKeyGitHubToken:
		return true
	}
	return false
}

// validateSettingValue 按键的语义校验写入值。
func validateSettingValue(key, val string) error {
	switch key {
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
		// 上界守护：直探超时是每实例每拍的最坏阻塞量，过大直接把心跳节拍拖垮（见 ADR-013 与
		// FR-446 审计项 2 / 复审 NEW-ISSUE A）。上界由节拍护栏反推（directprobe.MaxTimeout），
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
		// 非空但非法（不支持 scheme / 不可解析）则拒绝，不静默直连（FR-185/ADR-043）。
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

// wsTokenSecretDisplay 生成 jwt.ws_secret 只读展示值（FR-275，见 ADR-061）。
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
