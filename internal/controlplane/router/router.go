package router

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/embed"
	"github.com/wcpe/JianManager/internal/controlplane/mcp"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Services 聚合所有服务依赖。
type Services struct {
	Auth       *service.AuthService
	User       *service.UserService
	Group      *service.GroupService
	Node       *service.NodeService
	NodeRepair *service.NodeRepairService
	// NodeProxy 节点级出站代理管控（FR-185，见 ADR-043）；nil 时节点代理端点关闭。
	NodeProxy     *service.NodeProxyService
	Instance      *service.InstanceService
	InstanceBatch *service.InstanceBatchService
	InstanceGroup *service.InstanceGroupService
	// BeaconSync Beacon 拓扑拉取映射（FR-444，见 ADR-090）；nil 时 /beacon/topology 端点关闭。
	BeaconSync  *service.BeaconSyncService
	JDK         *service.JDKService
	NodeRuntime *service.NodeRuntimeService
	// RuntimeLibrary 节点运行时库（FR-298）：统一 Runtime 视图 + 扫描发现 + 泛化登记；
	// nil 时 /nodes/:id/runtimes 端点关闭。
	RuntimeLibrary *service.RuntimeLibraryService
	// PMConfig 节点包管理器与 registry 配置（FR-306）；nil 时 /nodes/:id/pm-config 端点关闭。
	PMConfig    *service.PMConfigService
	Diagnostics *service.DiagnosticsService
	DockerImage *service.DockerImageService
	Terminal    *service.TerminalService
	File        *service.FileService
	FileVersion *service.FileVersionService
	Plugin      *service.PluginService
	Player      *service.PlayerService
	PlayerEvent *service.PlayerEventService
	ServerState *service.ServerStateService
	// CrashSnapshot 实例崩溃快照只读查询（FR-313）；nil 时端点关闭。
	CrashSnapshot *service.CrashSnapshotService
	Business      *service.BusinessService
	BusinessEvent *service.BusinessEventService
	Config        *service.ConfigService
	// ConfigSource 配置源明面化（FR-451）；nil 时 /instances/:id/configs/surface 端点关闭。
	ConfigSource *service.ConfigSourceService
	// InstanceRolling 实例滚动/分批/灰度编排（FR-457）；nil 时 /instances/rolling 端点关闭。
	InstanceRolling *service.InstanceRollingService
	// ConfigBaseline 配置基线下发/漂移/收敛（FR-458）；nil 时 /config-baselines 端点关闭。
	ConfigBaseline   *service.ConfigBaselineService
	Bot              *service.BotService
	BotStressSession *service.BotStressSessionService
	// BotLoadCapacity/Preflight/Execution 是 FR-351 进程级共享单例；nil 时仅保留旧会话入口。
	BotLoadCapacity  *service.BotLoadCapacityDirectory
	BotLoadPreflight *service.BotLoadPreflightService
	BotLoadExecution *service.BotLoadExecutionService
	// BotLoadTemplate 命令压测模板（FR-370）；nil 时 /bots/load-templates 关闭。
	BotLoadTemplate *service.BotLoadTemplateService
	// BotLoadReport 压测终态报告（FR-370）；nil 时 report/stream 关闭。
	BotLoadReport *service.BotLoadReportService
	// BotLoadMetrics 5s 指标采样与查询（FR-370）；nil 时 metrics 关闭。
	BotLoadMetrics *service.BotLoadMetricSampler
	// BotLoadProjection failures/events 投影（FR-370）；nil 时对应端点关闭。
	BotLoadProjection *service.BotLoadProjectionService
	Alert             *service.AlertService
	AlertChannel      *service.AlertChannelService
	Schedule          *service.ScheduleService
	Backup            *service.BackupService
	BackupStorage     *service.BackupStorageService
	// ArtifactStorage 制品存储渠道（FR-347，见 ADR-073）：client-file 外置对象存储配置；
	// nil 时渠道端点关闭（上传恒本地）。
	ArtifactStorage *service.ArtifactStorageChannelService
	// ArtifactMigration 制品存量迁移（FR-348）：渠道间搬运后台任务；nil 时迁移端点关闭。
	ArtifactMigration *service.ArtifactMigrationService
	// ArtifactReconcile 制品索引 ↔ S3 一致性对账（FR-349）：对账运行/差异报告/显式处置；
	// nil 时对账端点关闭。
	ArtifactReconcile *service.ArtifactReconcileService
	Template          *service.TemplateService
	Audit             *service.AuditService
	Authz             *service.AuthzService
	// Permission 可选显式权限服务；nil 时从 Authz.Permissions() 取（FR-432）。
	Permission *service.PermissionService
	Event      *service.EventService
	Asset      *service.AssetService
	// ArtifactVersion 通用制品版本库（FR-409）；首期为 ServerProbe 提供来源、缓存和版本选择。
	ArtifactVersion *service.ArtifactVersionService
	Core            *service.CoreService
	Provision       *service.ProvisionService
	Proxy           *service.ProxyService
	Clone           *service.CloneService
	// ImportServer 导入现有服务器（FR-302，见 ADR-069）；nil 时导入端点关闭。
	ImportServer *service.ImportServerService
	Registration *service.RegistrationService
	Network      *service.NetworkService
	Log          *service.LogService
	Metric       *service.MetricService
	// PlatformObservability 是平台管理员首页的有界总览读模型（FR-402）。
	PlatformObservability *service.PlatformObservabilityService
	Settings              *service.SettingsService
	// OrphanRuntime 实例反向对账无主运行时列表/确认处置（FR-326）；nil 时端点关闭。
	OrphanRuntime *service.OrphanRuntimeTracker
	// BotReclaim 失效 Bot 回收列表/确认处置（FR-460）；nil 时端点关闭。
	BotReclaim    *service.BotReclaimService
	ProbeUpdate   *service.ProbeUpdateService
	ClientChannel *service.ClientChannelService
	ClientVersion *service.ClientVersionService
	// ClientChunkUpload 大文件分块上传（FR-251，增强 FR-088）；nil 时分块端点关闭、前端回退单次上传。
	ClientChunkUpload *service.ChunkedUploadService
	// ClientUploadEfficiency 上传增效：秒传预查 + 小文件聚合（FR-346，增强 FR-250/251）；
	// nil 时增效端点关闭、前端预查失败降级为全量上传（不阻断发布）。
	ClientUploadEfficiency *service.ClientUploadEfficiencyService
	ClientMachine          *service.ClientMachineService
	ClientDistTracking     *service.ClientDistTrackingService
	ClientIPGuard          *service.ClientIPGuardService
	ClientTelemetry        *service.ClientTelemetryService
	ClientDistStats        *service.ClientDistStatsService
	// ClientRuntimeState 客户端运行态心跳与聚合（FR-265）。
	ClientRuntimeState *service.ClientRuntimeStateService
	ClientDistSecurity *service.ClientDistSecurityService
	// ClientDistObservability 分发观测时序底座（FR-217，见 ADR-049）。
	ClientDistObservability *service.ClientDistObservabilityService
	// ClientDistExport 分发统计与安全日志 CSV 导出（FR-361）。
	ClientDistExport *service.ClientDistExportService
	RuntimeAssets    *service.RuntimeAssetsService
	EnrollToken      *service.EnrollTokenService
	// AgentToken Agent 专用令牌 + 策略引擎（FR-384，见 ADR-076）；nil 时 agent 端点关闭。
	AgentToken *service.AgentTokenService
	// AgentCallLog Agent 调用流水（FR-390，见 ADR-076）；nil 时不记流水、无 call-logs/count。
	AgentCallLog *service.AgentCallLogService
	// AgentTransfer 流式传输票据（FR-397）：MCP 不承载大文件字节，改由票据换取一次性数据面。
	// nil 时 /api/v1/agent-transfer 关闭（未配置服务端主密钥）。
	AgentTransfer *service.AgentTransferTicketService
	// MCP 内嵌 MCP 网关（FR-389，见 ADR-077）；nil 时 /api/v1/mcp 与会话管理关闭。
	MCP *mcp.Handler
	// EnrollInstall 拼装一键安装命令所需的对外地址（FR-080，见 ADR-020）。
	EnrollInstall EnrollInstallConfig
	Storage       *service.StorageService
	DBBrowse      *service.DBBrowseService
	SelfUpdate    *service.SelfUpdateService
	// 全局任务中心 + 站内信（FR-183，见 ADR-040）。
	Task         *service.TaskService
	Notification *service.NotificationService
	// 统一通知中心（FR-216，见 ADR-048）：聚合站内信 + 告警为一条只读通知流。
	NotificationFeed *service.NotificationFeedService
}

// Setup 创建并配置 Gin 路由引擎。
func Setup(svcs *Services, jwtSecret string) *gin.Engine {
	if svcs.User == nil && svcs.Auth != nil {
		svcs.User = svcs.Auth.UserService()
	}
	r := gin.New()
	// Control Plane 不自行信任反向代理；未显式配置的 X-Forwarded-For 不能伪造限流与审计 IP。
	if err := r.SetTrustedProxies(nil); err != nil {
		panic("配置 Gin 受信任代理失败: " + err.Error())
	}
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		Skip: func(c *gin.Context) bool {
			return strings.HasPrefix(c.Request.URL.Path, "/worker-assets/") || strings.HasPrefix(c.Request.URL.Path, "/probe-artifacts/")
		},
	}))
	r.Use(gin.Recovery())

	api := r.Group("/api/v1")
	api.Use(middleware.RateLimit(10, 20)) // 10 请求/秒，桶容量 20
	// API 错误统一落平台日志（FR-320）：4xx 业务拒绝 warn / 5xx error，经 log_slog 桥进日志中心，
	// /logs 页可追查「某操作为什么报错」（挂全局含未认证路径；401/404/429 噪音跳过）。
	api.Use(middleware.ErrorLog())

	// 公开路由（无需认证）
	var invitationSvc *service.UserInvitationService
	if svcs.User != nil {
		invitationSvc = svcs.User.InvitationService()
	}
	authHandler := NewAuthHandler(svcs.Auth, invitationSvc)
	authHandler.RegisterRoutes(api)

	setupHandler := NewSetupHandler(svcs.Auth)
	setupHandler.RegisterRoutes(api)

	// Agent 流式传输数据面（FR-397）：票据自身即完整凭据（HMAC 签名 + 一次性 + 实时重验），
	// 故挂公开组而非 Agent 鉴权组；端点不接受任何路径/实例参数，无参数注入面。
	if svcs.AgentTransfer != nil && svcs.File != nil && svcs.FileVersion != nil {
		NewAgentTransferHandler(svcs.AgentTransfer, svcs.File, svcs.FileVersion).RegisterRoutes(api)
	}

	// 面向玩家的客户端分发消费端点（FR-087，见 ADR-022/023、contract §4）：
	// manifest/制品端点用拉取密钥（X-Client-Key）鉴权，与运营浏览器 JWT 入口物理隔离，
	// 故挂在 api（公网、仅限流）而非 protected（JWT）。内容可信靠 manifest 签名而非密钥。
	if svcs.ClientChannel != nil && (svcs.ClientVersion != nil || svcs.ClientTelemetry != nil || svcs.ClientRuntimeState != nil || svcs.ClientDistSecurity != nil) {
		// L7 防护（FR-096，见 ADR-023）：消费端点独立子组挂 IP 黑白名单 + per-IP 限流 + 并发信号量，
		// 不影响其它 api 路由。L3/L4 容量型 DDoS 靠 CDN/云清洗，不在此。
		consumerGroup := api.Group("")
		if svcs.ClientIPGuard != nil {
			consumerGroup.Use(middleware.ClientDistGuard(svcs.ClientIPGuard, 5, 20, 256))
		}
		if svcs.ClientVersion != nil {
			clientConsumerHandler := NewClientVersionHandler(svcs.ClientVersion, svcs.ClientChannel, svcs.Audit, svcs.ClientMachine, svcs.ClientDistTracking, svcs.ClientDistSecurity)
			clientConsumerHandler.RegisterConsumerRoutes(consumerGroup)
		}
		// 客户端遥测上报（FR-094）：同为面向玩家公网端点，挂守卫子组（拉取密钥鉴权 + L7 防护）。
		if svcs.ClientTelemetry != nil {
			NewClientTelemetryHandler(svcs.ClientTelemetry, svcs.ClientChannel, svcs.ClientDistSecurity).RegisterRoutes(consumerGroup)
		}
		if svcs.ClientRuntimeState != nil {
			NewClientDistRuntimeHandler(svcs.ClientRuntimeState, svcs.ClientDistTracking, svcs.ClientChannel, svcs.Audit, svcs.ClientDistSecurity).RegisterConsumerRoutes(consumerGroup)
		}
		if svcs.ClientDistSecurity != nil {
			NewClientSecurityHandler(svcs.ClientDistSecurity, svcs.Audit).RegisterConsumerRoutes(consumerGroup)
		}
	}

	// 需要认证的路由
	protected := api.Group("")
	protected.Use(middleware.JWTAuth(jwtSecret))
	protected.Use(middleware.Audit(middleware.AuditConfig{
		RecordFunc: func(userID uint, action, targetType, targetID, detail, ip string, success bool, errMsg string) {
			svcs.Audit.RecordResultSafe(userID, action, targetType, targetID, detail, ip, success, errMsg)
		},
	}))
	// 加载授权上下文（用户角色 + 组成员关系 + 权限树节点），供后续权限判断使用
	protected.Use(middleware.LoadAccess(svcs.Authz))

	// 权限树（FR-432）：seed 系统角色模板 + /auth/me 扩展 + /rbac 管理面
	permSvc := svcs.Permission
	if permSvc == nil && svcs.Authz != nil {
		permSvc = svcs.Authz.Permissions()
	}
	if permSvc != nil {
		if err := permSvc.SeedSystemRoles(); err != nil {
			slog.Error("seed system role templates failed", "err", err)
		}
		NewAuthMeHandler(svcs.Authz, permSvc).RegisterRoutes(protected)
		NewRBACHandler(permSvc, svcs.User).RegisterRoutes(protected)
	}

	// 所有认证用户可访问的资源（FR-432：路由组挂权限节点，清空模板后 API 403）
	{
		// 域级读权限子组：组内无节点即整组拒绝
		permRead := func(nodes ...string) *gin.RouterGroup {
			return protected.Group("", middleware.RequireAnyPerm(nodes...))
		}

		nodeHandler := NewNodeHandler(svcs.Node, svcs.NodeRepair, svcs.Audit)
		nodeHandler.RegisterRoutes(permRead("node.read", "node.manage"))

		jdkHandler := NewJDKHandler(svcs.JDK)
		jdkHandler.RegisterRoutes(permRead("node.read", "node.manage"))

		// 节点运行时管理（FR-178）：制品缓存（列/清/逐项清/设上限）+ JDK 版本目录（foojay）+ 目录浏览。
		// Handler 内部按平台管理员收敛 + 破坏性操作写审计。
		if svcs.NodeRuntime != nil {
			nodeRuntimeHandler := NewNodeRuntimeHandler(svcs.NodeRuntime, svcs.Audit)
			nodeRuntimeHandler.RegisterRoutes(permRead("node.read", "node.manage"))
		}

		// 节点运行时库（FR-298）：统一 Runtime 视图（node_jdks + node_runtimes 读侧拼装）+
		// 扫描发现 + 泛化登记/删除。仅平台管理员；扫描/登记/删除写审计。
		if svcs.RuntimeLibrary != nil {
			runtimeLibraryHandler := NewRuntimeLibraryHandler(svcs.RuntimeLibrary, svcs.Audit)
			runtimeLibraryHandler.RegisterRoutes(permRead("node.read", "node.manage"))
		}

		// 节点包管理器与 registry 配置（FR-306）：PM 偏好（corepack 激活）+ 多 registry。仅平台管理员 + 审计。
		if svcs.PMConfig != nil {
			NewPMConfigHandler(svcs.PMConfig, svcs.Audit).RegisterRoutes(permRead("node.read", "node.manage"))
		}

		// 节点级出站代理（FR-185，见 ADR-043）：查看/设置某节点继承全局或自定义代理。
		// Handler 内按平台管理员收敛 + 设置写审计；经心跳下发 Worker 运行时生效。
		if svcs.NodeProxy != nil {
			nodeProxyHandler := NewNodeProxyHandler(svcs.NodeProxy, svcs.Audit)
			nodeProxyHandler.RegisterRoutes(permRead("node.read", "node.manage"))
		}

		// Docker 镜像管理（FR-078，见 ADR-019）：节点级列出/拉取/删除本机镜像。仅平台管理员。
		if svcs.DockerImage != nil {
			dockerImageHandler := NewDockerImageHandler(svcs.DockerImage)
			dockerImageHandler.RegisterRoutes(permRead("node.manage"))
		}

		instanceHandler := NewInstanceHandler(svcs.Instance, svcs.Authz)
		instanceHandler.RegisterRoutes(permRead("instance.read"))

		// 实例批量操作（FR-058）：独立 handler，挂 /instances/batch（与单实例路由共存）。
		instanceBatchHandler := NewInstanceBatchHandler(svcs.InstanceBatch, svcs.Authz)
		instanceBatchHandler.RegisterRoutes(permRead("instance.read", "instance.operate", "instance.write"))

		// 实例滚动/分批/灰度编排（FR-457）：独立 handler，挂 /instances/rolling。
		if svcs.InstanceRolling != nil {
			NewInstanceRollingHandler(svcs.InstanceRolling, svcs.Authz, svcs.Audit).
				RegisterRoutes(permRead("instance.read", "instance.operate"))
		}

		// 实例组织分组树（FR-165，见 ADR-033）：多级嵌套文件夹式归类 + 实例 M:N，
		// 正交于用户组 / 网络群组；读 instance:read、写 instance:write，挂 /instance-groups。
		if svcs.InstanceGroup != nil {
			instanceGroupHandler := NewInstanceGroupHandler(svcs.InstanceGroup, svcs.Authz)
			instanceGroupHandler.RegisterRoutes(permRead("instance.read"))
		}

		// Beacon 拓扑拉取（FR-444，见 ADR-090）：手动触发从 Beacon 拉取区服结构树并映射为
		// 分组树 + 补 region:/zone:/role: 标签。可选协同——未配置 beacon.endpoint 时端点仍注册
		// 但返回 503 明确提示（不静默成功），未部署 Beacon 时本平台全部功能不受影响。
		if svcs.BeaconSync != nil {
			beaconHandler := NewBeaconHandler(svcs.BeaconSync, svcs.Authz)
			beaconHandler.RegisterRoutes(permRead("instance.write"))
		}

		// 探针在线更新（FR-068）：单实例 + 批量下发已选版本，下次重启生效。instance:operate。
		if svcs.ProbeUpdate != nil {
			probeUpdateHandler := NewProbeUpdateHandler(svcs.ProbeUpdate, svcs.Instance, svcs.Authz, svcs.ArtifactVersion)
			probeUpdateHandler.RegisterRoutes(permRead("instance.read", "instance.operate"))
		}
		if svcs.ArtifactVersion != nil {
			NewArtifactVersionHandler(svcs.ArtifactVersion).RegisterSelectionRoutes(permRead("instance.read", "node.read", "node.manage"))
		}

		terminalHandler := NewTerminalHandler(svcs.Terminal, svcs.Authz)
		terminalHandler.RegisterRoutes(permRead("terminal.access"))

		fileHandler := NewFileHandler(svcs.File, svcs.FileVersion, svcs.Authz)
		fileHandler.RegisterRoutes(permRead("file.read"))

		// 插件/模组单服管理（FR-052）：实例级隔离，复用 file gRPC 完成文件操作。
		pluginHandler := NewPluginHandler(svcs.Plugin, svcs.Authz, svcs.Audit)
		pluginHandler.RegisterRoutes(permRead("file.read", "file.write"))

		configHandler := NewConfigHandler(svcs.Config, svcs.Authz)
		configHandler.SetConfigSource(svcs.ConfigSource)
		configHandler.SetAudit(svcs.Audit)
		configHandler.RegisterRoutes(permRead("file.read", "instance.read", "file.write", "instance.write"))

		// 配置基线下发/漂移/收敛（FR-458）：独立 handler，挂 /config-baselines。
		if svcs.ConfigBaseline != nil {
			NewConfigBaselineHandler(svcs.ConfigBaseline, svcs.Authz, svcs.Audit).
				RegisterRoutes(permRead("file.read", "instance.read", "file.write", "instance.write"))
		}

		// Bot 分布式容量静态路由必须先于 /bots/:id 注册，且复用进程级 CapacityDirectory。
		if svcs.BotLoadCapacity != nil {
			NewBotLoadHandler(svcs.BotLoadCapacity, svcs.Instance, svcs.Authz).RegisterRoutes(permRead("bot.read", "bot.manage"))
		}
		// FR-370 模板静态路由也须先于 /bots/:id。
		if svcs.BotLoadTemplate != nil {
			NewBotLoadTemplateHandler(svcs.BotLoadTemplate, svcs.Authz).RegisterRoutes(permRead("bot.read", "bot.manage"))
		}
		if svcs.BotStressSession != nil {
			botStressSessionHandler := NewBotStressSessionHandler(
				svcs.BotStressSession, svcs.Authz, svcs.BotLoadPreflight, svcs.BotLoadExecution, svcs.BotLoadReport, svcs.BotLoadMetrics, svcs.BotLoadProjection, svcs.Audit,
			)
			botStressSessionHandler.RegisterRoutes(permRead("bot.read", "bot.manage"))
		}

		botHandler := NewBotHandler(svcs.Bot, svcs.Authz)
		botHandler.RegisterRoutes(permRead("bot.read"))

		playerHandler := NewPlayerHandler(svcs.Player, svcs.PlayerEvent, svcs.Authz, svcs.Audit)
		playerHandler.RegisterRoutes(permRead("player.read"))

		// 服务器状态：按需经探针桥取回某实例全量 Bukkit 状态（FR-076/077），instance:read 且实例可访问。
		if svcs.ServerState != nil {
			serverStateHandler := NewServerStateHandler(svcs.ServerState, svcs.Authz)
			serverStateHandler.RegisterRoutes(permRead("instance.read"))
		}

		// 崩溃诊断：实例崩溃快照只读列表（FR-313），instance:read 且实例可访问。
		if svcs.CrashSnapshot != nil {
			NewCrashSnapshotHandler(svcs.CrashSnapshot, svcs.Authz).RegisterRoutes(permRead("instance.read"))
		}

		// JBIS 业务对接：经探针桥下发业务命令（domain.action+payload）并透传结果（FR-116），instance:operate 且实例可访问。
		if svcs.Business != nil {
			businessHandler := NewBusinessHandler(svcs.Business, svcs.Authz, svcs.Audit)
			businessHandler.RegisterRoutes(permRead("instance.read", "instance.business.write", "instance.operate"))
		}

		// JBIS 业务事件汇聚只读视图（FR-122，见 ADR-027/028）：业务事件流 / 经济镜像 / 跨区聚合。
		// 平台级只读（instance:read），汇聚镜像非业务真源；写入由探针事件流自动汇聚。
		if svcs.BusinessEvent != nil {
			businessEventHandler := NewBusinessEventHandler(svcs.BusinessEvent, svcs.Authz)
			businessEventHandler.RegisterRoutes(permRead("instance.read"))
		}

		eventHandler := NewEventHandler(svcs.Event)
		eventHandler.RegisterRoutes(permRead("instance.read", "node.read"))

		// 组相关：列表/创建由 group:read/group:manage 节点控制，
		// 组级资源（:id）由 GroupHandler 内部按授权上下文收敛
		groupHandler := NewGroupHandler(svcs.Group, svcs.Authz)
		groupHandler.RegisterRoutes(permRead("group.read"))

		alertHandler := NewAlertHandler(svcs.Alert, svcs.AlertChannel)
		alertHandler.RegisterRoutes(permRead("alert.read", "alert.manage"))

		scheduleHandler := NewScheduleHandler(svcs.Schedule)
		scheduleHandler.RegisterRoutes(permRead("schedule.read"))

		backupHandler := NewBackupHandler(svcs.Backup, svcs.Authz)
		backupHandler.RegisterRoutes(permRead("backup.read", "backup.write"))

		templateHandler := NewTemplateHandler(svcs.Template)
		templateHandler.RegisterRoutes(permRead("template.read", "template.manage"))

		// 制品库：平台级共享资源，Handler 内部按平台管理员收敛（FR-045）。
		assetHandler := NewAssetHandler(svcs.Asset)
		assetHandler.RegisterRoutes(permRead("node.manage", "node.read"))

		// 运行时与制品全局页聚合（FR-082）：JDK 矩阵 + 引用实例 + 制品占用/去重/冷热；
		// FR-301 另含多运行时矩阵与强制刷新（写审计）。平台级共享资源，Handler 内部按平台管理员收敛。
		if svcs.RuntimeAssets != nil {
			runtimeAssetsHandler := NewRuntimeAssetsHandler(svcs.RuntimeAssets, svcs.Audit)
			runtimeAssetsHandler.RegisterRoutes(permRead("node.read", "node.manage"))
		}

		// 日志中心：所有认证用户可查询，Handler 内部按可访问实例收敛、平台日志仅管理员可见（FR-049）。
		logHandler := NewLogHandler(svcs.Log, svcs.Authz)
		logHandler.RegisterRoutes(permRead("log.read"))

		// 时序监控历史曲线（FR-060）：node 维度对认证用户开放，instance 维度按 CanAccessInstance 收敛。
		metricHandler := NewMetricHandler(svcs.Metric, svcs.Authz)
		metricHandler.RegisterRoutes(permRead("monitor.read", "stats.read"))
		if svcs.PlatformObservability != nil {
			observabilityHandler := NewObservabilityHandler(svcs.PlatformObservability)
			observabilityHandler.RegisterRoutes(permRead("monitor.read", "stats.read", "node.read"))
		}

		// 全局任务中心（FR-183，见 ADR-040）：认证用户可见，非管理员只见自己发起的任务（service 层收敛）。
		if svcs.Task != nil {
			taskHandler := NewTaskHandler(svcs.Task)
			taskHandler.RegisterRoutes(permRead("task.read"))
		}

		// 站内信（FR-183，见 ADR-040）：认证用户只读/操作自己的站内信。
		if svcs.Notification != nil {
			notificationHandler := NewNotificationHandler(svcs.Notification)
			notificationHandler.RegisterRoutes(permRead("notification.read", "alert.read"))
		}

		// 统一通知中心（FR-216，见 ADR-048）：聚合站内信 + 告警为一条只读通知流，
		// 页眉单铃铛 + 通知中心页消费。认证用户（消息按本人、告警全局）。
		if svcs.NotificationFeed != nil {
			notificationFeedHandler := NewNotificationFeedHandler(svcs.NotificationFeed)
			notificationFeedHandler.RegisterRoutes(permRead("notification.read", "alert.read"))
		}
	}

	// 平台级资源：按权限树节点收敛（超级管理员节点全量恒通过；不再一刀切 RequireRole 10）
	{
		permRead := func(nodes ...string) *gin.RouterGroup {
			return protected.Group("", middleware.RequireAnyPerm(nodes...))
		}
		if svcs.User != nil {
			userHandler := NewUserHandler(svcs.User)
			userHandler.RegisterRoutes(permRead("user.read", "user.manage"))
		}

		auditHandler := NewAuditHandler(svcs.Audit)
		auditHandler.RegisterRoutes(permRead("audit.read"))

		// 一键搭建子服与核心查询（FR-034）、搭建代理（FR-035）：平台管理员
		provisionHandler := NewProvisionHandler(svcs.Core, svcs.Provision, svcs.Proxy, svcs.Clone)
		provisionHandler.RegisterRoutes(permRead("instance.create", "node.manage"))

		// 导入现有服务器：就地接管 / 搬迁托管区（FR-302，见 ADR-069）。平台管理员 + 审计。
		if svcs.ImportServer != nil {
			importServerHandler := NewImportServerHandler(svcs.ImportServer, svcs.Audit)
			importServerHandler.RegisterRoutes(permRead("instance.create", "node.manage"))
		}

		// 群组服关系模型：角色注册、Network 软标签（FR-032）。平台管理员。
		registrationHandler := NewRegistrationHandler(svcs.Registration)
		registrationHandler.RegisterRoutes(permRead("network.manage", "node.manage"))

		networkHandler := NewNetworkHandler(svcs.Network)
		networkHandler.RegisterRoutes(permRead("network.read", "network.manage"))

		// 群组拓扑聚合（FR-335）：一次返全量 proxy 注册 + network 成员归属，消 per-proxy N+1。平台管理员。
		topologyHandler := NewTopologyHandler(svcs.Registration, svcs.Network)
		topologyHandler.RegisterRoutes(permRead("network.read", "network.manage"))

		// 备份远程存储后端：含凭证 env 引用，平台级配置（FR-057）。
		if svcs.BackupStorage != nil {
			backupStorageHandler := NewBackupStorageHandler(svcs.BackupStorage)
			backupStorageHandler.RegisterRoutes(permRead("backup.read", "backup.write", "node.manage"))
		}

		// 制品存储渠道（FR-347）。
		if svcs.ArtifactStorage != nil {
			NewArtifactStorageHandler(svcs.ArtifactStorage).RegisterRoutes(permRead("node.manage"))
		}

		if svcs.ArtifactMigration != nil {
			NewArtifactMigrationHandler(svcs.ArtifactMigration).RegisterRoutes(permRead("node.manage"))
		}

		if svcs.ArtifactReconcile != nil {
			NewArtifactReconcileHandler(svcs.ArtifactReconcile, svcs.Audit).RegisterRoutes(permRead("node.manage"))
		}

		// 平台配置（FR-063 / ADR-015）。
		if svcs.Settings != nil {
			settingsHandler := NewSettingsHandler(svcs.Settings)
			settingsHandler.RegisterRoutes(permRead("settings.read", "settings.write", "license.read"))
		}

		if svcs.OrphanRuntime != nil {
			NewOrphanRuntimeHandler(svcs.OrphanRuntime).RegisterRoutes(permRead("node.manage", "instance.read"))
		}

		if svcs.BotReclaim != nil {
			// F5：读路由挂 bot.read|bot.manage；处置写路由仅挂 bot.manage（避免仅持 bot.read 即可停用）。
			NewBotReclaimHandler(svcs.BotReclaim).RegisterRoutes(
				permRead("bot.manage", "bot.read"),
				permRead("bot.manage"),
			)
		}

		if svcs.Diagnostics != nil {
			NewDiagnosticsHandler(svcs.Diagnostics).RegisterRoutes(permRead("node.manage", "node.read"))
		}

		if svcs.Storage != nil {
			storageHandler := NewStorageHandler(svcs.Storage)
			storageHandler.RegisterRoutes(permRead("node.manage"))
		}

		// 客户端分发频道与拉取密钥（FR-086）。
		if svcs.ClientChannel != nil {
			clientChannelHandler := NewClientChannelHandler(svcs.ClientChannel, svcs.Audit)
			clientChannelHandler.RegisterRoutes(permRead("channel.read", "channel.write"))
		}

		if svcs.ClientVersion != nil && svcs.ClientChannel != nil {
			clientVersionHandler := NewClientVersionHandler(svcs.ClientVersion, svcs.ClientChannel, svcs.Audit, svcs.ClientMachine, svcs.ClientDistTracking, svcs.ClientDistSecurity)
			clientVersionHandler.RegisterPublishRoutes(permRead("dist.publish", "channel.write"))
		}

		if svcs.ClientChannel != nil {
			NewClientUpdaterConfigHandler(svcs.ClientChannel).RegisterRoutes(permRead("channel.read", "channel.write"))
		}

		if svcs.ClientChunkUpload != nil && svcs.ClientChannel != nil {
			clientChunkUploadHandler := NewClientChunkUploadHandler(svcs.ClientChunkUpload, svcs.ClientChannel, svcs.Audit)
			clientChunkUploadHandler.RegisterRoutes(permRead("dist.publish", "channel.write"))
		}

		if svcs.ClientUploadEfficiency != nil && svcs.ClientChannel != nil {
			clientUploadEffHandler := NewClientUploadEfficiencyHandler(svcs.ClientUploadEfficiency, svcs.ClientChannel, svcs.Audit)
			clientUploadEffHandler.RegisterRoutes(permRead("dist.publish", "channel.write"))
		}

		if svcs.ClientDistSecurity != nil {
			NewClientSecurityHandler(svcs.ClientDistSecurity, svcs.Audit).RegisterAdminRoutes(permRead("dist.ops.read", "dist.ops.write"))
		}

		if svcs.ClientIPGuard != nil {
			clientIPRuleHandler := NewClientIPRuleHandler(svcs.ClientIPGuard, svcs.Audit)
			clientIPRuleHandler.RegisterRoutes(permRead("dist.ops.write", "dist.ops.read"))
		}

		if svcs.ClientDistStats != nil {
			clientStatsHandler := NewClientStatsHandler(svcs.ClientDistStats)
			clientStatsHandler.RegisterRoutes(permRead("dist.ops.read", "stats.read", "channel.read"))
		}

		if svcs.ClientDistObservability != nil {
			clientDistObsHandler := NewClientDistObservabilityHandler(svcs.ClientDistObservability, svcs.Audit)
			clientDistObsHandler.RegisterRoutes(permRead("dist.ops.read", "stats.read"))
		}

		if svcs.ClientRuntimeState != nil && svcs.ClientDistTracking != nil {
			NewClientDistRuntimeHandler(svcs.ClientRuntimeState, svcs.ClientDistTracking, svcs.ClientChannel, svcs.Audit, svcs.ClientDistSecurity).RegisterAdminRoutes(permRead("dist.ops.read"))
		}

		if svcs.ClientDistExport != nil {
			NewClientDistExportHandler(svcs.ClientDistExport, svcs.Audit).RegisterRoutes(permRead("dist.ops.read", "stats.read"))
		}

		// 客户端更新器接入引导（FR-107）。
		NewClientUpdaterJarsHandler().RegisterRoutes(permRead("channel.read", "channel.write"))

		// 节点 enrollment token（FR-080）。
		if svcs.EnrollToken != nil {
			enrollTokenHandler := NewEnrollTokenHandler(svcs.EnrollToken, svcs.Audit, svcs.EnrollInstall, svcs.SelfUpdate)
			enrollTokenHandler.RegisterRoutes(permRead("node.manage"))
		}
		// Agent Token（FR-384）。
		if svcs.AgentToken != nil {
			NewAgentTokenHandler(svcs.AgentToken, svcs.Audit, svcs.AgentCallLog).RegisterAdminRoutes(permRead("agent.token.read", "agent.token.manage"))
		}
		if svcs.MCP != nil {
			svcs.MCP.RegisterAdminRoutes(permRead("agent.mcp.read", "agent.token.manage"))
		}
		// 数据库资源管理器（FR-084）。
		if svcs.DBBrowse != nil {
			dbBrowseHandler := NewDBBrowseHandler(svcs.DBBrowse)
			dbBrowseHandler.RegisterRoutes(permRead("system.db.browse"))
		}

		// 面板自更新（FR-081）。
		if svcs.SelfUpdate != nil {
			selfUpdateHandler := NewSelfUpdateHandler(svcs.SelfUpdate, svcs.Audit)
			selfUpdateHandler.RegisterRoutes(permRead("system.update"))
		}
		// 版本化制品包（FR-409）。
		if svcs.ArtifactVersion != nil {
			NewArtifactVersionHandler(svcs.ArtifactVersion).RegisterRoutes(permRead("system.update", "node.manage"))
		}
	}

	// Agent 运维面（FR-384）：Bearer Agent Token（jmat_*），不走人类 JWT；策略在 CP 唯一真源。
	if svcs.AgentToken != nil && svcs.Instance != nil && svcs.Node != nil {
		agentGroup := api.Group("")
		agentGroup.Use(middleware.AgentAuth(svcs.AgentToken))
		// 要求已注入 agent principal，否则 401（拒绝纯 JWT 误入此组的写路径混用）
		agentGroup.Use(func(c *gin.Context) {
			if middleware.GetAgentPrincipal(c) == nil {
				c.AbortWithStatusJSON(401, gin.H{"error": "UNAUTHORIZED", "message": "需要有效的 Agent Token（jmat_ 前缀）"})
				return
			}
			c.Next()
		})
		NewAgentOpsHandler(svcs.AgentToken, svcs.Instance, svcs.Node, svcs.Audit, svcs.AgentCallLog).RegisterOpsRoutes(agentGroup)
	}

	// CP 内嵌 MCP 网关（FR-389 / ADR-077）：Streamable HTTP + SSE；仅 Agent Token。
	if svcs.MCP != nil && svcs.AgentToken != nil {
		mcpGroup := api.Group("/mcp")
		mcpGroup.Use(middleware.AgentAuth(svcs.AgentToken))
		mcpGroup.Use(func(c *gin.Context) {
			if middleware.GetAgentPrincipal(c) == nil {
				c.AbortWithStatusJSON(401, gin.H{"error": "UNAUTHORIZED", "message": "需要有效的 Agent Token（jmat_ 前缀）"})
				return
			}
			c.Next()
		})
		svcs.MCP.RegisterMCPRoutes(mcpGroup)
	}

	// Worker 一键安装脚本匿名静态端点（FR-080，见 ADR-020 §2）：一键命令 `curl <cp>/install-worker.sh | sh`
	// 依赖 CP 自托管这两个脚本。显式注册（根路径、非 /api/v1）以先于下方 SPA NoRoute 回退命中。
	registerInstallScriptRoutes(r)

	// Worker 二进制 CP-local 下载端点（FR-190）：匿名路径由短 token 保护，先于 SPA NoRoute 注册。
	if svcs.SelfUpdate != nil {
		NewSelfUpdateHandler(svcs.SelfUpdate, svcs.Audit).RegisterDownloadRoutes(r)
	}
	// ServerProbe 由 Worker 从 CP 本地 CAS 主动拉取；短 token 不经请求日志记录。
	if svcs.ArtifactVersion != nil {
		NewArtifactVersionHandler(svcs.ArtifactVersion).RegisterDownloadRoutes(r)
	}

	// 前端静态文件（go:embed 嵌入）
	embed.RegisterStaticRoutes(r)

	return r
}

func marshalAuditDetail(detail any) string {
	raw, err := json.Marshal(detail)
	if err != nil {
		slog.Warn("序列化审计详情失败", "error", err)
		return ""
	}
	return string(raw)
}

func parseUintDefault(raw string, fallback uint64) uint64 {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return value
}
