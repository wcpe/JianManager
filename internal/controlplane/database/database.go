package database

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// New 创建并返回数据库连接。
func New(cfg config.DatabaseConfig) (*gorm.DB, error) {
	var dialector gorm.Dialector

	switch cfg.Driver {
	case "sqlite":
		// 首次部署自动创建数据库文件的父目录（含多级）：否则 modernc/glebarez 打开
		// 不存在目录下的文件报 SQLITE_CANTOPEN（表象 "out of memory (14)"），逼运维手动 mkdir。
		if err := ensureSQLiteParentDir(cfg.DSN); err != nil {
			return nil, err
		}
		// SQLite 走自建连接池（机制化防护，见 sqlite_pool.go）：
		// ① 每条物理连接经 guardConn 包装，让「连接带未提交事务」可被 database/sql 在回池前
		//    检测并驱逐（原先靠 SetMaxOpenConns(1) 消灭锁竞争来回避，放开连接数后必须换成检测）；
		// ② 启用 WAL，使读不阻塞写、写不阻塞读；
		// ③ 写事务经写闸门显式串行化，补回单连接顺带提供的「单写者」语义。
		// 逃生阀 JIANMANAGER_SQLITE_MAX_OPEN_CONNS=1 可一键退回原单连接行为。
		sqlDB, guard, plan, err := openSQLitePool(cfg.DSN)
		if err != nil {
			return nil, err
		}
		logSQLitePoolPlan(plan)
		// 用 Conn 注入既成连接池（Dialector 仅在 Conn 为空时才自建 sql.DB），并按 pool 形态
		// 设置 ConnPool —— 后者让所有 gorm 显式事务走写闸门入口。
		dialector = sqlite.Dialector{DSN: cfg.DSN, Conn: newGatedConnPool(sqlDB, guard)}
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s", cfg.Driver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		// SQL 日志跟随调试开关（FIX-7）：非调试静默、调试打印，运行时即时生效，
		// 不再硬编码 logger.Info 致生产 / 迁移仍逐条刷 SQL。
		Logger: newGormLogger(os.Stdout),
	})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	return db, nil
}

// logSQLitePoolPlan 把连接池决策打进启动日志。
//
// 为什么必须显式记录：WAL 是**持久化属性**（改动落在数据库文件头上），连接数决定并发形态，
// 降级则意味着本次优化实际上没生效。三者都不能静默，否则事后排障只能靠猜。
func logSQLitePoolPlan(plan sqlitePoolPlan) {
	switch {
	case plan.Memory:
		slog.Info("SQLite 使用单连接（内存库）",
			"max_open_conns", plan.MaxOpenConns, "reason", plan.Reason)
	case plan.Degraded:
		slog.Warn("SQLite WAL 启用失败，已降级为单连接（未放开连接数）",
			"max_open_conns", plan.MaxOpenConns, "reason", plan.Reason)
	case plan.WAL:
		slog.Info("SQLite 已启用 WAL 与写闸门",
			"max_open_conns", plan.MaxOpenConns,
			"note", "读并发不再被长写任务阻塞；写事务由写闸门串行化")
	default:
		// 逃生阀路径：不切 WAL，回到原始形态。
		slog.Warn("SQLite 已显式收敛为单连接（逃生阀），退回原始行为",
			"max_open_conns", plan.MaxOpenConns, "reason", plan.Reason)
	}
}

// ensureSQLiteParentDir 在打开 SQLite 文件前创建其父目录（含多级）。
// 纯内存库（:memory: / file::memory:）与无目录段的纯文件名跳过。
func ensureSQLiteParentDir(dsn string) error {
	path := sqliteFilePath(dsn)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建数据库目录 %s 失败: %w", dir, err)
	}
	return nil
}

// sqliteFilePath 从 SQLite DSN 提取磁盘文件路径；内存库等无磁盘路径返回空串。
// 处理 modernc/glebarez 支持的 file: 方案前缀与 ?query 参数。
func sqliteFilePath(dsn string) string {
	s := strings.TrimSpace(dsn)
	s = strings.TrimPrefix(s, "file:")
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if s == "" || s == ":memory:" {
		return ""
	}
	return s
}

// AutoMigrate 自动迁移所有模型。
func AutoMigrate(db *gorm.DB) error {
	// 迁移 g_rpc_port → grpc_port（修复 GORM snake_case 对全大写缩写的错误转换）
	if err := migrateGRPCPortColumn(db); err != nil {
		return err
	}
	// 迁移 process_metric_snapshots.p_id → pid（真机验收抓到：
	// GORM 把 PID 蛇形化为 p_id，与 managedProcessHistory 的 WHERE pid 查询不一致）。
	if err := migrateProcessMetricPIDColumn(db); err != nil {
		return err
	}

	if err := db.AutoMigrate(
		&model.User{},
		&model.UserInvitation{},
		&model.AuthSetupLock{},
		// 权限树（ADR-089）：角色模板 + 节点 + 用户绑定 + 覆盖
		&model.RoleTemplate{},
		&model.RolePermission{},
		&model.UserRoleBinding{},
		&model.UserPermissionOverride{},
		&model.Group{},
		&model.GroupMember{},
		&model.GroupQuota{},
		&model.Node{},
		&model.NodeJDK{},
		// 节点运行时库：非 JDK 运行时（nodejs/python 预留）承载表，
		// 读侧与 node_jdks 拼统一视图，写侧各走各表；JDK 不迁移。
		&model.NodeRuntime{},
		// 节点包管理器与 registry 配置（节点单例）。
		&model.NodePMConfig{},
		&model.NodeEnrollToken{},
		// Worker 启动阶段进度：注册前的临时进度，注册成功后删除。
		// 单独成表而非给 Node 加列——首装时 CP 还没有该节点的正式记录（连 UUID 都没有）。
		&model.NodeStartupProgress{},
		// 主机级观测快照：分挂载点磁盘与整机 top-N 进程，均为多值维度。
		&model.HostMountSnapshot{},
		&model.HostProcessSnapshot{},
		// Agent 专用令牌（见 ADR-076）：与人类 JWT 分离，scope + 写白名单。
		&model.AgentToken{},
		// Agent 调用流水（见 ADR-076）：读+写 Ops 与日后 MCP tool。
		&model.AgentCallLog{},
		&model.AgentTransferTicket{},
		&model.Instance{},
		&model.LogTargetHolder{},
		&model.LogCutoverState{},
		&model.LogCutoverWorker{},
		&model.GroupInstance{},
		// 实例组织分组树（见 ADR-033）：自引用邻接表 + 实例 M:N，
		// 与用户组 / 网络群组正交，仅供组织归类，不承载 RBAC / 部署语义。
		&model.InstanceGroupNode{},
		&model.InstanceGroupMember{},
		// 实例崩溃快照：进程非正常退出现场留存，每实例滚动保留最近 5 条。
		// 起快照增根因/指纹/证据/置信度列（旧行为空，读侧降级 unknown）。
		&model.InstanceCrashSnapshot{},
		// 崩溃趋势汇总：按 (实例, 天, 根因, 指纹) 计数，独立于 K=5 滚动裁剪，
		// 使「连崩 >5 次」「同类聚合」等趋势不被快照裁剪抹掉。
		&model.InstanceCrashStat{},
		// 业务事件位点账本（ADR-107）：只存投递元数据（streamSeq/seq/dedupKey），**无 payload**，
		// 让业务台能精确定位缺口并只对受影响的实例/域拉快照。默认保留 7 天，定期巡检裁剪。
		&model.BusinessEventCursor{},
		// 实例二进制版本绑定：记录实例当前/上一版本制品，使重建冻结版本、
		// 并提供受控升级与一级回滚。
		&model.InstanceBinaryBinding{},
		// 实例整机快照：时间点语义的可回滚点，底层复用 Backup 的归档与回放。
		&model.InstanceSnapshot{},
		// 无主运行时反向对账跟踪：Worker 有、CP 无记录的实例宽限/处置状态。
		&model.OrphanRuntime{},
		// 失效 Bot 自动回收：按 workerEpoch 换代判定僵死 Fleet Bot 的宽限/处置状态。
		&model.FleetBotReclaim{},
		&model.ServerRegistration{},
		&model.Network{},
		&model.NetworkMember{},
		&model.BotStressSession{},
		&model.BotLoadBatch{},
		&model.BotLoadActionResult{},
		// 命令编排 occurrence checkpoint。
		&model.BotLoadCommandCheckpoint{},
		// 压测模板、5 秒聚合样本与 append-only 运行事件。
		&model.BotLoadTemplate{},
		&model.BotLoadMetricSample{},
		&model.BotLoadRunEvent{},
		&model.Bot{},
		&model.BanRecord{},
		&model.AlertRule{},
		&model.AlertEvent{},
		&model.AlertChannel{},
		// QQ 已发现群 + 网关连接状态：网关事件落库与连接状态持久化。
		&model.QQDiscoveredGroup{},
		&model.QQGatewayConnection{},
		&model.Schedule{},
		&model.ScheduleExecutionLog{},
		&model.Backup{},
		&model.BackupStorage{},
		&model.Template{},
		&model.AuditLog{},
		&model.InstanceConfigVersion{},
		// 受管配置项来源登记表：二态来源（内联/文件引用）。
		&model.InstanceConfigSource{},
		// 实例滚动/分批/灰度编排会话。
		&model.InstanceRollingOp{},
		// 配置基线（模板）与漂移收敛。
		&model.ConfigBaseline{},
		// 配置分层覆盖：配置逻辑文件 + 各作用域独立的不可变版本追加链。
		// 作用域只有 group/instance 两级，多层语义由分组树深度表达（不硬编码大区/小区）。
		&model.ConfigFile{},
		&model.ConfigLayerVersion{},
		// 配置分批灰度与生效编排（532）：交付单（状态机）+ 目标（逐台经历）+ 回滚记录
		// （回滚可再回滚，必须留链）。三者都在配置交付链路内，与分层覆盖的「定义」分表存放。
		&model.ConfigDeliveryOrder{},
		&model.ConfigDeliveryTarget{},
		&model.ConfigDeliveryRollback{},
		&model.FileVersion{},
		&model.Asset{},
		// 通用制品版本库：逻辑包/来源/版本映射到既有 Asset CAS，首个接入为 ServerProbe。
		&model.ArtifactPackage{},
		&model.ArtifactSource{},
		&model.ArtifactVersion{},
		// 制品存储渠道（见 ADR-073）：client-file 制品外置对象存储的渠道配置，
		// 内置「本机存储」行由 service EnsureBuiltin 幂等 seed。
		&model.ArtifactStorageChannel{},
		// 制品存量迁移：迁移任务登记与实时计数 + 逐条失败明细。
		&model.ArtifactMigration{},
		&model.ArtifactMigrationFailure{},
		// 制品索引 ↔ S3 对象一致性对账：运行记录 + 差异明细 + 定期设置（单行）。
		&model.ArtifactReconcileRun{},
		&model.ArtifactReconcileDiff{},
		&model.ArtifactReconcileSetting{},
		&model.LogEntry{},
		&model.MetricSeries{},
		&model.MetricSampleRaw{},
		&model.ProcessMetricSnapshot{},
		&model.MetricRollup5m{},
		&model.MetricRollup1h{},
		&model.PlatformSetting{},
		// AI 助手配置底座（见 ADR-105）：供应商、模型与单行全局设置。
		// 与 PlatformSetting 的字符串键覆盖层分开——这三张表有唯一约束、默认模型指针
		// 与乐观并发版本，塞进标量键会把约束全让给应用层自己守。
		&model.AssistantProvider{},
		&model.AssistantModel{},
		&model.AssistantSetting{},
		// AI 助手运行时：会话 / 消息 / run / 事件四表。
		// run 与事件是**两套**语义——run 是状态机（乐观并发 + 会终态），事件是 append-only 过程流；
		// 单会话单活跃 run 的部分唯一索引无法用 GORM tag 表达，随后手工建（见 migrateAssistantRunUniqueIndexes）。
		&model.AssistantConversation{},
		&model.AssistantMessage{},
		&model.AssistantRun{},
		&model.AssistantEvent{},
		// 工具调用的确认票据与执行回执。
		// 票据表达「用户是否批准这次写入」，回执表达「这次调用实际发生了什么」；
		// 只读调用不出票（confirmation_id 可空）但同样写回执，供审计串联。
		&model.AssistantConfirmation{},
		&model.AssistantReceipt{},
		// 491：平台级审批票据与执行回执（与聊天确认是两套，见 model/approval.go 注释）。
		&model.ApprovalTicket{},
		&model.ApprovalReceipt{},
		// 外部 MCP 连接与工具（工具逐条启停与设定风险，故单独成表）。
		&model.AssistantMCPConnection{},
		&model.AssistantMCPTool{},
		&model.ClientChannel{},
		&model.ClientPullKey{},
		&model.ClientVersion{},
		// 原 updater-core 集中版本注册表（client_core_versions）已随反转废弃（见 ADR-045 改写）：
		// updater-core 改由 CP 内嵌默认版本自动驱动 manifest agent.core，运营不再管理。AutoMigrate 不删表，
		// 存量库的旧表留着无害、不再写入；模型已移除，故此处不再 AutoMigrate 该表。
		&model.ClientMachine{},
		&model.ClientDistEvent{},
		&model.ClientDistDaily{},
		&model.ClientIPRule{},
		&model.ClientTelemetry{},
		&model.ClientTelemetryDaily{},
		// 客户端启动运行态：独立于 client_telemetry，避免运行态污染分发请求观测。
		&model.ClientRuntimeState{},
		&model.ClientSecurityProfile{},
		&model.ClientSecurityHello{},
		&model.ClientSecurityRiskEvent{},
		&model.ClientProtectionAction{},
		&model.ClientSecurityGroup{},
		&model.ClientSecurityCounter{},
		// 客户端分发观测时序快照（见 ADR-049）：离线把 events/telemetry 卷积为
		// 按频道×小时桶的观测快照，供观测·分发监控页跨频道/平台时序，与写时聚合解耦。
		&model.ClientDistSnapshot{},
		// JBIS 业务事件汇聚三表（business_events / economy_balance_mirrors /
		// economy_ledger_entries）已随退役（ADR-106），不再 AutoMigrate。
		// AutoMigrate 不删表，存量库由 migrateRetireBusinessTables 显式 DROP。
		// 全局任务中心 + 站内信（见 ADR-040）：长任务（Task）+ 滚动日志（TaskLog）+
		// 站内信（Notification）；任务进度经心跳上报、CP 汇聚 upsert，终态发站内信。
		&model.Task{},
		&model.TaskLog{},
		&model.Notification{},
		// 系统更新页检查结果缓存（增强）：单行覆盖式，存上次成功 CheckResult 的
		// JSON blob + checkedAt，进页即显 + 后台静默刷新，刷新失败保留旧缓存。
		&model.SelfUpdateCheckCache{},
	); err != nil {
		return err
	}

	// 节点名活跃唯一约束（见 ADR-039，修复 BUG-A）：先对存量重名活跃节点去重，再建部分唯一索引。
	// 必须在 AutoMigrate 建表后执行（依赖 nodes 表与 deleted_at 列存在）。
	if err := migrateNodeNameUnique(db); err != nil {
		return err
	}
	if err := migrateBackupStorageActiveNameKey(db); err != nil {
		return err
	}
	if err := migrateScopedUniqueIndexes(db); err != nil {
		return err
	}
	if err := migrateArtifactVersionSourceVersionIndex(db); err != nil {
		return err
	}
	// §5.2 / §6.1：助手 run 的两个部分唯一索引（单会话单活跃 run、提交幂等键）。
	// 必须在 AutoMigrate 建出 assistant_runs 之后执行。
	if err := migrateAssistantRunUniqueIndexes(db); err != nil {
		return err
	}
	// §8：同 run 单张 pending 票据（部分唯一索引，随后手工建）。
	if err := migrateAssistantConfirmPendingIndex(db); err != nil {
		return err
	}
	// §3.1：审批票据的幂等部分唯一索引。
	if err := migrateApprovalPendingUniqueIndex(db); err != nil {
		return err
	}
	// ADR-106：V1 业务镜像表退役，直接删表（用户明确决策，不备份）。
	if err := migrateRetireBusinessTables(db); err != nil {
		return err
	}
	// 幂等归正 role=beacon 的历史实例 type（早期误记为 minecraft_java）→ generic，
	// 并顺带归零非适用实例的历史脏 probe_port。best-effort：这是「仅告警/补偿」的数据归正，
	// 不承担 CP 启动的关键路径（失败不阻断启动，仅日志告警，因而不 return err）。
	if err := backfillBeaconInstanceType(db); err != nil {
		slog.Warn("Beacon 实例 type/probe_port 归正未完成（不阻断启动）", "error", err)
	}
	// 幂等回填 bots.desired_state / desired_state_generation。
	return backfillBotDesiredState(db)
}

// backfillBeaconInstanceType 幂等归正 role=beacon 的历史实例 type → generic（验收③），
// 并顺带归零非适用实例的历史脏 probe_port（补口）。
//
// 背景：Beacon 配套服务实例不是 Minecraft Java 进程，早期搭建路径把其 type 误记为
// minecraft_java，于是 ServerProbe（Bukkit 插件）被误判为可加载。运行时 IsProbeApplicable 已按
// role 兜底（端口分配/心跳采集/探针推送三处均收口），但库中 type 仍是脏值；验收③要求数据层归正。
//
// 语义边界：**只按 role 归正**（type 的唯一身份依据是 role=beacon，不是 name）。name 含 "beacon"
// 但 role 非 beacon 的实例不擅自修改——名字是可变标签、不构成身份依据，误改会污染用户数据；
// 这类实例仅在日志中列出供人工复核（见 listBeaconNamedNonBeaconInstances）。
//
// 幂等：条件 `type <> 'generic'`，重复执行不再命中，可安全随每次 CP 启动重跑。
// 时机：随 AutoMigrate 在 CP 启动时执行；本次修复本身不改动任何生产库，归正在将来部署新二进制时发生。
func backfillBeaconInstanceType(db *gorm.DB) error {
	if !db.Migrator().HasTable("instances") {
		return nil
	}
	res := db.Model(&model.Instance{}).
		Where("role = ? AND type <> ?", model.InstanceRoleBeacon, model.InstanceTypeGeneric).
		Update("type", model.InstanceTypeGeneric)
	if res.Error != nil {
		return fmt.Errorf("归正 Beacon 实例 type 失败: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		slog.Info("已将 role=beacon 实例的 type 归正为 generic", "rows", res.RowsAffected)
	}
	// 顺带归零非适用实例的历史脏 probe_port：早期误配会在代理/Beacon/通用二进制上
	// 留下一个永不被采集的探针端口。SQL 条件与 model.IsProbeApplicable 严格同口径的反面：
	// 不适用 ⇔ type<>minecraft_java（type 非空）或 role∈(proxy,beacon)。role IS NULL 视为适用
	// （NULL 既不等于 proxy 也不等于 beacon），故不会被本条件命中而误清。
	if res := db.Model(&model.Instance{}).
		Where("probe_port <> 0 AND (type <> ? OR role IN ?)",
			string(model.InstanceTypeMinecraftJava),
			[]string{string(model.InstanceRoleProxy), string(model.InstanceRoleBeacon)}).
		Update("probe_port", 0); res.Error != nil {
		return fmt.Errorf("归零非适用实例探针端口失败: %w", res.Error)
	} else if res.RowsAffected > 0 {
		slog.Info("已归零非适用实例的历史探针端口", "rows", res.RowsAffected)
	}
	// 仅日志提示，绝不写入；纯诊断，失败不应影响启动（best-effort）。
	if ids := listBeaconNamedNonBeaconInstances(db); len(ids) > 0 {
		slog.Warn("存在名称含 beacon 但 role 非 beacon 的实例，未自动归正 type，请人工复核",
			"instanceIds", ids)
	}
	return nil
}

// listBeaconNamedNonBeaconInstances 返回 name 含 beacon（大小写不敏感）但 role 非 beacon 的实例 ID，
// 仅供调用方打日志提示人工复核，不做任何写入（：name 不是身份依据，不擅自归正名字匹配的实例）。
//
// best-effort：本函数是纯诊断，若把 error 冒泡给 AutoMigrate（CP 启动关键路径），一条「告警查询」
// 失败就会让 CP 起不来——收益与代价严重不匹配，故失败仅降级为一条 warn 日志并返回 nil（N4）。
// 判空用 `role IS NULL OR role <> 'beacon'`：SQL 三值逻辑下 `role <> 'beacon'` 对 NULL 行恒为 NULL，
// 会静默漏掉 role 为 NULL 的历史行（N2）。
func listBeaconNamedNonBeaconInstances(db *gorm.DB) []uint {
	var ids []uint
	if err := db.Model(&model.Instance{}).
		Where("(role IS NULL OR role <> ?) AND LOWER(name) LIKE ?", model.InstanceRoleBeacon, "%beacon%").
		Pluck("id", &ids).Error; err != nil {
		slog.Warn("查询待人工复核的 Beacon 命名实例失败（不影响启动，仅少一条诊断）", "error", err)
		return nil
	}
	return ids
}

// backfillBotDesiredState 将历史 Bot 的 desired_state 按活动会话与 runtime status 回填。
// 活动会话中 status=pending/connecting/connected/disconnected/error → running；
// stopped 或无活动会话 → stopped；DesiredStateGeneration≤0 时修正为 1。可重复运行。
func backfillBotDesiredState(db *gorm.DB) error {
	if !db.Migrator().HasTable("bots") || !db.Migrator().HasColumn(&model.Bot{}, "DesiredState") {
		return nil
	}
	// 活动会话：status 非 stopped 且未软删；Bot 未软删。
	activeStatuses := []model.BotStatus{
		model.BotStatusPending, model.BotStatusConnecting, model.BotStatusConnected,
		model.BotStatusDisconnected, model.BotStatusError,
	}
	// 有活动会话且 runtime 未停 → desired=running。
	if err := db.Exec(`
		UPDATE bots SET desired_state = ?
		WHERE deleted_at IS NULL
		  AND (desired_state = '' OR desired_state IS NULL OR desired_state = ?)
		  AND status IN (?,?,?,?,?)
		  AND stress_session_id IS NOT NULL
		  AND stress_session_id IN (
		    SELECT id FROM bot_stress_sessions
		    WHERE deleted_at IS NULL AND status <> ?
		  )
	`, model.BotDesiredRunning, model.BotDesiredStopped,
		model.BotStatusPending, model.BotStatusConnecting, model.BotStatusConnected,
		model.BotStatusDisconnected, model.BotStatusError,
		model.BotStressSessionStopped,
	).Error; err != nil {
		// SQLite/MySQL 对空串/NULL 兼容差异：回退到 GORM 条件更新。
		if err := backfillBotDesiredStateGORM(db, activeStatuses); err != nil {
			return fmt.Errorf("回填 Bot desired_state 失败: %w", err)
		}
	}
	// 其余仍为空或默认 stopped 的行保持 stopped；generation ≤0 修正为 1。
	if err := db.Model(&model.Bot{}).
		Where("deleted_at IS NULL AND desired_state_generation <= 0").
		Update("desired_state_generation", 1).Error; err != nil {
		return fmt.Errorf("回填 Bot desired_state_generation 失败: %w", err)
	}
	return nil
}

// backfillBotDesiredStateGORM 在原生 SQL 不兼容时用 GORM 幂等回填。
func backfillBotDesiredStateGORM(db *gorm.DB, activeStatuses []model.BotStatus) error {
	var activeSessionIDs []uint
	if err := db.Model(&model.BotStressSession{}).
		Where("deleted_at IS NULL AND status <> ?", model.BotStressSessionStopped).
		Pluck("id", &activeSessionIDs).Error; err != nil {
		return err
	}
	if len(activeSessionIDs) == 0 {
		return nil
	}
	return db.Model(&model.Bot{}).
		Where("deleted_at IS NULL").
		Where("stress_session_id IN ?", activeSessionIDs).
		Where("status IN ?", activeStatuses).
		Where("desired_state = ? OR desired_state = ''", model.BotDesiredStopped).
		Update("desired_state", model.BotDesiredRunning).Error
}

// nodeNameUniqueIndexName 节点名活跃唯一索引名（仅约束 deleted_at IS NULL 的活跃行）。
const nodeNameUniqueIndexName = "uniq_nodes_name_active"

// migrateNodeNameUnique 为 nodes.name 建立「活跃行唯一」约束（见 ADR-039 §3）。
//
// 用部分唯一索引（WHERE deleted_at IS NULL）而非普通唯一索引：身份由 UUID 锚定，name 为可变标签，
// 软删除节点应能释放其名供新节点复用（支撑坏节点修复后重新 enroll）。普通唯一索引会让已软删的
// 同名行永久占用名字。建索引前先对「存量重名活跃节点」去重，否则索引创建会失败。
func migrateNodeNameUnique(db *gorm.DB) error {
	if !db.Migrator().HasTable("nodes") {
		return nil
	}
	if err := dedupeActiveNodeNames(db); err != nil {
		return err
	}
	// 幂等：已存在则跳过（HasIndex 对部分索引同样适用）。
	if db.Migrator().HasIndex(&model.Node{}, nodeNameUniqueIndexName) {
		return nil
	}
	stmt := fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON nodes (name) WHERE deleted_at IS NULL",
		nodeNameUniqueIndexName)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("创建节点名唯一索引失败: %w", err)
	}
	return nil
}

// dedupeActiveNodeNames 为存量重名活跃节点去重（见 ADR-039 §修复）：同名活跃行保留最近心跳者，
// 其余追加 "-dup-<id>" 后缀，避免历史重名阻断部分唯一索引创建。返回去重过程中的错误。
func dedupeActiveNodeNames(db *gorm.DB) error {
	type dupName struct {
		Name string
		Cnt  int
	}
	var dups []dupName
	if err := db.Model(&model.Node{}).
		Select("name, COUNT(*) AS cnt").
		Where("deleted_at IS NULL").
		Group("name").
		Having("COUNT(*) > 1").
		Scan(&dups).Error; err != nil {
		return fmt.Errorf("扫描重名活跃节点失败: %w", err)
	}
	for _, d := range dups {
		var nodes []model.Node
		// 保留最近心跳（NULL 心跳排末尾）、其次最新创建者；其余重命名。
		if err := db.Where("name = ? AND deleted_at IS NULL", d.Name).
			Order("last_heartbeat DESC, created_at DESC, id ASC").
			Find(&nodes).Error; err != nil {
			return fmt.Errorf("查询重名节点 %q 失败: %w", d.Name, err)
		}
		for i, n := range nodes {
			if i == 0 {
				continue // 保留首个（最近活跃）
			}
			newName, err := nextDedupeNodeName(db, n.Name, n.ID)
			if err != nil {
				return err
			}
			if err := db.Model(&model.Node{}).Where("id = ?", n.ID).
				Update("name", newName).Error; err != nil {
				return fmt.Errorf("重命名重名节点 id=%d 失败: %w", n.ID, err)
			}
		}
	}
	return nil
}

// nextDedupeNodeName 生成未被其它活跃节点占用的迁移去重名称。
func nextDedupeNodeName(db *gorm.DB, name string, id uint) (string, error) {
	base := fmt.Sprintf("%s-dup-%d", name, id)
	for suffix := 0; ; suffix++ {
		candidate := base
		if suffix > 0 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		var count int64
		if err := db.Model(&model.Node{}).Where("name = ? AND deleted_at IS NULL", candidate).Count(&count).Error; err != nil {
			return "", fmt.Errorf("检查节点去重名称 %q 失败: %w", candidate, err)
		}
		if count == 0 {
			return candidate, nil
		}
	}
}

// migrateGRPCPortColumn 将旧的 g_rpc_port / g_r_p_c_port 列迁移为 grpc_port。
// GORM 对 GRPCPort 的默认 snake_case 转换是 g_r_p_c_port，历史手写迁移也曾使用
// g_rpc_port；两种旧列都必须保留数值，遇到冲突值时中止迁移，禁止静默丢端口。
func migrateGRPCPortColumn(db *gorm.DB) error {
	if !db.Migrator().HasTable("nodes") {
		return nil
	}
	for _, legacy := range []string{"g_rpc_port", "g_r_p_c_port"} {
		if !db.Migrator().HasColumn("nodes", legacy) {
			continue
		}
		if !db.Migrator().HasColumn("nodes", "grpc_port") {
			if err := db.Migrator().RenameColumn("nodes", legacy, "grpc_port"); err != nil {
				return fmt.Errorf("迁移 %s 列失败: %w", legacy, err)
			}
			continue
		}
		var conflicts int64
		query := fmt.Sprintf("SELECT COUNT(*) FROM nodes WHERE grpc_port <> 0 AND %s <> 0 AND grpc_port <> %s", legacy, legacy)
		if err := db.Raw(query).Scan(&conflicts).Error; err != nil {
			return fmt.Errorf("检查 %s 与 grpc_port 冲突失败: %w", legacy, err)
		}
		if conflicts > 0 {
			return fmt.Errorf("迁移 %s 失败：存在 %d 条与 grpc_port 冲突的数据", legacy, conflicts)
		}
		update := fmt.Sprintf("UPDATE nodes SET grpc_port = %s WHERE grpc_port = 0 AND %s <> 0", legacy, legacy)
		if err := db.Exec(update).Error; err != nil {
			return fmt.Errorf("回填 %s 到 grpc_port 失败: %w", legacy, err)
		}
		if err := db.Migrator().DropColumn("nodes", legacy); err != nil {
			return fmt.Errorf("删除已迁移的 %s 列失败: %w", legacy, err)
		}
	}
	return nil
}

// migrateProcessMetricPIDColumn 将 process_metric_snapshots 的 p_id 列迁移为 pid。
// GORM 默认 snake_case 命名把全大写缩写 PID 转成 p_id，而 managedProcessHistory 查询用
// pid 列名，二者不一致导致进程历史样本查询报 no such column: pid（真机验收抓到）。
// 迁移在 AutoMigrate 建表前执行：新库直接按模型建 pid 列；存量库检测到 p_id 时重命名。
func migrateProcessMetricPIDColumn(db *gorm.DB) error {
	if !db.Migrator().HasTable("process_metric_snapshots") {
		return nil
	}
	if !db.Migrator().HasColumn("process_metric_snapshots", "p_id") {
		return nil
	}
	if err := db.Migrator().RenameColumn("process_metric_snapshots", "p_id", "pid"); err != nil {
		return fmt.Errorf("迁移 process_metric_snapshots.p_id → pid 失败: %w", err)
	}
	return nil
}

// migrateRetireBusinessTables 删除 V1 业务镜像三表（见 ADR-106）。
//
// 背景：JM 第五期 JBIS 业务对接把经济/背包的业务语义收进了平台（业务事件 envelope、
// 经济余额镜像、经济流水审计）。ADR-106 决定 JM 不做任何业务，业务数据由下游业务台持有，
// 平台只保留纯透传通道。故三表整体退役。
//
// 数据处置：**直接删表，不备份**（用户明确决策，V1 业务数据属早期实验性质、无生产依赖）。
//
// 幂等：DROP TABLE IF EXISTS，重复执行无副作用；新库本就没有这三张表，空跑。
func migrateRetireBusinessTables(db *gorm.DB) error {
	for _, table := range []string{
		"business_events",
		"economy_balance_mirrors",
		"economy_ledger_entries",
	} {
		if err := db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			return fmt.Errorf("删除已退役业务表 %s 失败: %w", table, err)
		}
	}
	return nil
}

// migrateBackupStorageActiveNameKey 为活跃备份存储回填名称唯一键，并清空归档记录的键。
// 以 nullable key + 普通唯一索引替代仅 SQLite 支持的部分唯一索引，保证 SQLite/MySQL 一致。
func migrateBackupStorageActiveNameKey(db *gorm.DB) error {
	if !db.Migrator().HasTable("backup_storages") || !db.Migrator().HasColumn("backup_storages", "active_name_key") {
		return nil
	}
	if err := db.Exec("UPDATE backup_storages SET active_name_key = NULL WHERE deleted_at IS NOT NULL AND active_name_key IS NOT NULL").Error; err != nil {
		return fmt.Errorf("清理归档备份存储名称键失败: %w", err)
	}
	if err := db.Exec("UPDATE backup_storages SET active_name_key = name WHERE deleted_at IS NULL AND (active_name_key IS NULL OR active_name_key = '')").Error; err != nil {
		return fmt.Errorf("回填活跃备份存储名称键失败: %w", err)
	}
	return nil
}

// migrateScopedUniqueIndexes 移除不含软删或 node→zone scope 的旧唯一索引。
// 新索引由 AutoMigrate 先创建，随后才释放旧索引，迁移期间不会出现无约束窗口。
func migrateScopedUniqueIndexes(db *gorm.DB) error {
	for _, migration := range []struct {
		model any
		index string
	}{
		{&model.BackupStorage{}, "idx_backup_storages_name"},
		{&model.BackupStorage{}, "uniq_backup_storage_name_active"},
	} {
		if db.Migrator().HasIndex(migration.model, migration.index) {
			if err := db.Migrator().DropIndex(migration.model, migration.index); err != nil {
				return fmt.Errorf("删除旧唯一索引 %s 失败: %w", migration.index, err)
			}
		}
	}
	return nil
}

// 助手 run 的两个部分唯一索引名。
const (
	// assistantRunActiveIndexName 单会话单活跃 run：只约束 queued/running/waiting_confirmation 行。
	assistantRunActiveIndexName = "ux_assistant_runs_active"
	// assistantRunIdemIndexName 提交幂等键：只约束 idempotency_key 非空的行。
	assistantRunIdemIndexName = "ux_assistant_runs_idem"
)

// assistantConfirmPendingIndexName 同 run 单张 pending 票据（§8）。
//
// 为什么需要它：一轮 run 可能在**同一个工具轮次里**产生多个写调用
// （模型一次回多个 tool_calls）。若每个都出票，前端会同时出现多张确认卡，
// 而 run 只有一个 waiting_confirmation 状态，用户批准先后顺序无法表达——
// 更糟的是先批的那张执行后 run 已推进，后一张的确认语义就落空了。
const assistantConfirmPendingIndexName = "ux_assistant_confirmations_pending"

// migrateAssistantRunUniqueIndexes 为 assistant_runs 建两个部分唯一索引（§5.2 / §6.1）。
//
// 为什么必须是数据库约束而不是进程内锁：单活跃 run 要能挡住重启后的残留槽与同一实例内的并发提交，
// 进程锁在重启后丢失、在两个实例下直接失效；而幂等键的唯一性一旦让给应用层「先查再插」，
// 两个并发请求同时未命中就都会插入，幂等当场破功（§6.1「插入冲突即回读」正是为这个窗口准备的）。
//
// 为什么是**部分**索引：
//   - 单活跃索引只覆盖活跃态——终态 run 必须允许同一会话继续累积历史，普通唯一索引会让一个会话
//     永久只能有一个 run；
//   - 幂等索引排除空值（NULL 与空串都排除）——不带 Idempotency-Key 的请求不应互相顶掉，
//     这是 §6.1 的硬要求。
//
// 幂等：HasIndex 先行跳过 + DDL 自带 IF NOT EXISTS，重复执行（每次 CP 启动）不报错。
func migrateAssistantRunUniqueIndexes(db *gorm.DB) error {
	if !db.Migrator().HasTable("assistant_runs") {
		return nil
	}
	// 活跃态谓词由 model.AssistantRunActiveStatuses 拼装而非另写一份字面量：
	// 两处一旦漂移，索引会放过本该冲突的插入，而这种漏洞在测试里几乎是静默的。
	quoted := make([]string, 0, len(model.AssistantRunActiveStatuses()))
	for _, status := range model.AssistantRunActiveStatuses() {
		quoted = append(quoted, "'"+status+"'")
	}
	ddls := []struct {
		name string
		stmt string
	}{
		{
			name: assistantRunActiveIndexName,
			stmt: fmt.Sprintf(
				"CREATE UNIQUE INDEX IF NOT EXISTS %s ON assistant_runs (conversation_id) WHERE status IN (%s)",
				assistantRunActiveIndexName, strings.Join(quoted, ", ")),
		},
		{
			name: assistantRunIdemIndexName,
			stmt: fmt.Sprintf(
				"CREATE UNIQUE INDEX IF NOT EXISTS %s ON assistant_runs (owner_user_id, conversation_id, idempotency_key) "+
					"WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''",
				assistantRunIdemIndexName),
		},
	}
	for _, ddl := range ddls {
		// HasIndex 对部分索引同样适用（与 migrateNodeNameUnique 同口径）。
		if db.Migrator().HasIndex(&model.AssistantRun{}, ddl.name) {
			continue
		}
		if err := db.Exec(ddl.stmt).Error; err != nil {
			return fmt.Errorf("创建助手 run 唯一索引 %s 失败: %w", ddl.name, err)
		}
	}
	return nil
}

// migrateAssistantConfirmPendingIndex 建「同 run 单张 pending 票据」的部分唯一索引。
//
// 与 run 的两个索引同构：约束放在数据库层而不是应用层「先查再插」，
// 因为后者在并发下两个请求都未命中就会双双插入（这正是 run 幂等键踩过的坑）。
//
// **存量数据处理**：早期版本不限制同 run 多张 pending，历史库里可能已有此类行。
// 直接建索引会让 CP 启动失败，所以先把同一 run 的多余 pending **标为失效**，
// 只保留最新一张。这是有意的取舍：那些票据从未被确认过，且对应 run 早已推进到
// 终态或本身已不可达，把它们标失效不会丢掉任何「用户已批准」的事实
// （已批准的票据状态是 consumed/approved，不在本处理范围内）。
func migrateAssistantConfirmPendingIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("assistant_confirmations") {
		return nil
	}
	if db.Migrator().HasIndex(&model.AssistantConfirmation{}, assistantConfirmPendingIndexName) {
		return nil
	}

	// 先归正存量：每个 run 只留 id 最大的那张 pending。
	//
	// 用 id 而不是 created_at 判「最新」：同一次事务里插入的行 created_at 可能相同，
	// 而 id 单调，能给出确定的唯一选择。
	var dupRunIDs []uint
	if err := db.Model(&model.AssistantConfirmation{}).
		Where("status = ?", model.AssistantConfirmStatusPending).
		Group("run_id").
		Having("COUNT(*) > 1").
		Pluck("run_id", &dupRunIDs).Error; err != nil {
		return fmt.Errorf("统计重复 pending 票据失败: %w", err)
	}
	for _, runID := range dupRunIDs {
		// 找出该 run 下应保留的那张。
		var keepID uint
		if err := db.Model(&model.AssistantConfirmation{}).
			Where("run_id = ? AND status = ?", runID, model.AssistantConfirmStatusPending).
			Order("id DESC").Limit(1).Pluck("id", &keepID).Error; err != nil {
			return fmt.Errorf("查询保留票据失败: %w", err)
		}
		if err := db.Model(&model.AssistantConfirmation{}).
			Where("run_id = ? AND status = ? AND id <> ?", runID, model.AssistantConfirmStatusPending, keepID).
			Update("status", model.AssistantConfirmStatusInvalidated).Error; err != nil {
			return fmt.Errorf("归正重复 pending 票据失败: %w", err)
		}
	}

	stmt := fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON assistant_confirmations (run_id) WHERE status = '%s'",
		assistantConfirmPendingIndexName, model.AssistantConfirmStatusPending)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("创建助手确认票据 pending 唯一索引失败: %w", err)
	}
	return nil
}

// approvalPendingIndexName 审批票据幂等索引（§3.1）。
//
// 语义：同一 (申请人, action, 目标, payload) 在 **pending 期间**只允许一张票。
// 重复提交返回原票，而不是新建——否则 Agent 重试就会刷出多张票，
// 批准人多批一张就多执行一次。
const approvalPendingIndexName = "ux_approval_tickets_pending"

// migrateApprovalPendingUniqueIndex 建审批票据的幂等部分唯一索引。
//
// 为什么必须是数据库约束：应用层「先查再插」在并发下两个请求都未命中就都会插入，
// 幂等当场破功。这与 assistant_runs 幂等键踩过的是同一个坑。
//
// 为什么是**部分**索引：终态票据必须允许同一组条件再次申请（上一张被驳回了，
// 申请人改好理由再来一次是正常流程）；只有 pending 需要互斥。
func migrateApprovalPendingUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("approval_tickets") {
		return nil
	}
	if db.Migrator().HasIndex(&model.ApprovalTicket{}, approvalPendingIndexName) {
		return nil
	}
	stmt := fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON approval_tickets "+
			"(applicant_user_id, action, target_type, target_id, payload_hash) WHERE status = '%s'",
		approvalPendingIndexName, model.ApprovalStatusPending)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("创建审批票据幂等唯一索引失败: %w", err)
	}
	return nil
}

const artifactVersionSourceVersionIndexName = "idx_artifact_versions_source_version" // migrateArtifactVersionSourceVersionIndex 修复首版遗漏 Version 字段的唯一索引。
// 来源已从属于制品包，故同一来源内只允许一个同名版本，允许同一来源有多个不同版本。
func migrateArtifactVersionSourceVersionIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.ArtifactVersion{}) {
		return nil
	}
	correct, err := artifactVersionSourceVersionIndexIsCorrect(db)
	if err != nil {
		return err
	}
	if correct {
		return nil
	}
	if db.Migrator().HasIndex(&model.ArtifactVersion{}, artifactVersionSourceVersionIndexName) {
		if err := db.Migrator().DropIndex(&model.ArtifactVersion{}, artifactVersionSourceVersionIndexName); err != nil {
			return fmt.Errorf("删除错误的制品版本唯一索引失败: %w", err)
		}
	}
	if err := db.Migrator().CreateIndex(&model.ArtifactVersion{}, artifactVersionSourceVersionIndexName); err != nil {
		return fmt.Errorf("创建制品来源版本唯一索引失败: %w", err)
	}
	return nil
}

func artifactVersionSourceVersionIndexIsCorrect(db *gorm.DB) (bool, error) {
	indexes, err := db.Migrator().GetIndexes(&model.ArtifactVersion{})
	if err != nil {
		return false, fmt.Errorf("读取制品版本索引失败: %w", err)
	}
	for _, index := range indexes {
		if index.Name() != artifactVersionSourceVersionIndexName {
			continue
		}
		unique, known := index.Unique()
		columns := index.Columns()
		return known && unique && len(columns) == 2 && columns[0] == "source_id" && columns[1] == "version", nil
	}
	return false, nil
}

// dynamicGormLogger 让 GORM 的 SQL 日志跟随调试开关（FIX-7）。
//
// 原实现把 GORM logger 硬编码为 logger.Info，与 config.LogLevelVar（225 运行时日志级别）
// 脱钩，导致生产（release / 非 debug）启动与迁移仍逐条打印 SQL（如 migrator 的
// `SELECT count(*) FROM sqlite_master`）。改为按调试开关动态取级别：调试（LogLevelVar ≤ Debug）
// → Info（打印全部 SQL）；否则 → Warn（仅错误 / 慢查询，生产静默）。LogLevelVar 可运行时切换
// （设置面板「调试模式」），故 SQL 日志亦随之即时生效、无需重启。
type dynamicGormLogger struct {
	info logger.Interface
	warn logger.Interface
}

// effectiveGormLevel 返回当前应生效的 GORM 日志级别（跟随调试开关）。
func effectiveGormLevel() logger.LogLevel {
	if config.LogLevelVar.Level() <= slog.LevelDebug {
		return logger.Info
	}
	return logger.Warn
}

func (l dynamicGormLogger) current() logger.Interface {
	if effectiveGormLevel() == logger.Info {
		return l.info
	}
	return l.warn
}

// LogMode 忽略外部显式级别——级别由调试开关动态决定，返回自身以保持动态行为。
func (l dynamicGormLogger) LogMode(logger.LogLevel) logger.Interface { return l }

func (l dynamicGormLogger) Info(ctx context.Context, msg string, data ...any) {
	l.current().Info(ctx, msg, data...)
}

func (l dynamicGormLogger) Warn(ctx context.Context, msg string, data ...any) {
	l.current().Warn(ctx, msg, data...)
}

func (l dynamicGormLogger) Error(ctx context.Context, msg string, data ...any) {
	l.current().Error(ctx, msg, data...)
}

func (l dynamicGormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.current().Trace(ctx, begin, fc, err)
}

// newGormLogger 构造跟随调试开关的 GORM logger，日志写入 w（生产传 os.Stdout）。
func newGormLogger(w io.Writer) logger.Interface {
	build := func(lv logger.LogLevel) logger.Interface {
		return logger.New(log.New(w, "\r\n", log.LstdFlags), logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  lv,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		})
	}
	return dynamicGormLogger{info: build(logger.Info), warn: build(logger.Warn)}
}
