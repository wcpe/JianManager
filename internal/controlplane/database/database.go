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
		dialector = sqlite.Open(cfg.DSN)
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s", cfg.Driver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		// SQL 日志跟随 FR-225 调试开关（FIX-7）：非调试静默、调试打印，运行时即时生效，
		// 不再硬编码 logger.Info 致生产 / 迁移仍逐条刷 SQL。
		Logger: newGormLogger(os.Stdout),
	})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	// SQLite 收敛为单连接（FR-318 根因修复）：glebarez/go-sqlite 的 COMMIT 因 SQLITE_BUSY
	// 失败时事务保持打开，而驱动不实现 driver.Validator/SessionResetter，database/sql 会把
	// 带着打开事务的连接放回池中且永不驱逐——此后抽到该连接的请求全部报
	// "cannot start a transaction within a transaction"，直到重启（线上 OOM/swap 慢 IO 即触发）。
	// BUSY 的唯一来源是自身连接池内的锁竞争（CP 是数据库唯一读写方，见架构不变量「数据所有权」），
	// 单连接下同库锁竞争不复存在，毒化前提被整体消除。机制与回归见 sqlite_txpoison_test.go。
	if cfg.Driver == "sqlite" {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("获取底层连接池失败: %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
	}

	return db, nil
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

	if err := db.AutoMigrate(
		&model.User{},
		&model.Group{},
		&model.GroupMember{},
		&model.GroupQuota{},
		&model.Node{},
		&model.NodeJDK{},
		// 节点运行时库（FR-298）：非 JDK 运行时（nodejs/python 预留）承载表，
		// 读侧与 node_jdks 拼统一视图，写侧各走各表；JDK 不迁移。
		&model.NodeRuntime{},
		// 节点包管理器与 registry 配置（FR-306，节点单例）。
		&model.NodePMConfig{},
		&model.NodeEnrollToken{},
		// Agent 专用令牌（FR-384，见 ADR-079）：与人类 JWT 分离，scope + 写白名单。
		&model.AgentToken{},
		&model.Instance{},
		&model.GroupInstance{},
		// 实例组织分组树（FR-165，见 ADR-033）：自引用邻接表 + 实例 M:N，
		// 与用户组 / 网络群组正交，仅供组织归类，不承载 RBAC / 部署语义。
		&model.InstanceGroupNode{},
		&model.InstanceGroupMember{},
		// 实例崩溃快照（FR-313）：进程非正常退出现场留存，每实例滚动保留最近 5 条。
		&model.InstanceCrashSnapshot{},
		// 无主运行时反向对账跟踪（FR-326）：Worker 有、CP 无记录的实例宽限/处置状态。
		&model.OrphanRuntime{},
		&model.ServerRegistration{},
		&model.Network{},
		&model.NetworkMember{},
		&model.BotStressSession{},
		&model.BotLoadBatch{},
		&model.BotLoadActionResult{},
		// FR-369 命令编排 occurrence checkpoint。
		&model.BotLoadCommandCheckpoint{},
		// FR-370 压测模板、5 秒聚合样本与 append-only 运行事件。
		&model.BotLoadTemplate{},
		&model.BotLoadMetricSample{},
		&model.BotLoadRunEvent{},
		&model.Bot{},
		&model.BanRecord{},
		&model.AlertRule{},
		&model.AlertEvent{},
		&model.AlertChannel{},
		&model.Schedule{},
		&model.ScheduleExecutionLog{},
		&model.Backup{},
		&model.BackupStorage{},
		&model.Template{},
		&model.AuditLog{},
		&model.InstanceConfigVersion{},
		&model.FileVersion{},
		&model.Asset{},
		// 制品存储渠道（FR-347，见 ADR-073）：client-file 制品外置对象存储的渠道配置，
		// 内置「本机存储」行由 service EnsureBuiltin 幂等 seed。
		&model.ArtifactStorageChannel{},
		// 制品存量迁移（FR-348）：迁移任务登记与实时计数 + 逐条失败明细。
		&model.ArtifactMigration{},
		&model.ArtifactMigrationFailure{},
		// 制品索引 ↔ S3 对象一致性对账（FR-349）：运行记录 + 差异明细 + 定期设置（单行）。
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
		&model.ClientChannel{},
		&model.ClientPullKey{},
		&model.ClientVersion{},
		// 原 updater-core 集中版本注册表（client_core_versions）已随 FR-193 反转废弃（见 ADR-045 改写）：
		// updater-core 改由 CP 内嵌默认版本自动驱动 manifest agent.core，运营不再管理。AutoMigrate 不删表，
		// 存量库的旧表留着无害、不再写入；模型已移除，故此处不再 AutoMigrate 该表。
		&model.ClientMachine{},
		&model.ClientDistEvent{},
		&model.ClientDistDaily{},
		&model.ClientIPRule{},
		&model.ClientTelemetry{},
		&model.ClientTelemetryDaily{},
		// 客户端启动运行态（FR-265）：独立于 client_telemetry，避免运行态污染分发请求观测。
		&model.ClientRuntimeState{},
		&model.ClientSecurityProfile{},
		&model.ClientSecurityHello{},
		&model.ClientSecurityRiskEvent{},
		&model.ClientProtectionAction{},
		&model.ClientSecurityGroup{},
		&model.ClientSecurityCounter{},
		// 客户端分发观测时序快照（FR-217，见 ADR-049）：离线把 events/telemetry 卷积为
		// 按频道×小时桶的观测快照，供观测·分发监控页跨频道/平台时序，与写时聚合解耦。
		&model.ClientDistSnapshot{},
		// JBIS 业务事件汇聚（FR-116 底座 / FR-122 经济，见 ADR-028）：
		// 通用 envelope（按 domain+dedupKey 去重）+ 经济结构化镜像（node→zone 维度）+ 经济变更审计。
		&model.BusinessEvent{},
		&model.EconomyBalanceMirror{},
		&model.EconomyLedgerEntry{},
		// 全局任务中心 + 站内信（FR-183，见 ADR-040）：长任务（Task）+ 滚动日志（TaskLog）+
		// 站内信（Notification）；任务进度经心跳上报、CP 汇聚 upsert，终态发站内信。
		&model.Task{},
		&model.TaskLog{},
		&model.Notification{},
		// 系统更新页检查结果缓存（FR-186，增强 FR-182）：单行覆盖式，存上次成功 CheckResult 的
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
	// FR-365：幂等回填 bots.desired_state / desired_state_generation。
	return backfillBotDesiredState(db)
}

// backfillBotDesiredState 将历史 Bot 的 desired_state 按活动会话与 runtime status 回填（FR-365）。
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
			newName := fmt.Sprintf("%s-dup-%d", n.Name, n.ID)
			if err := db.Model(&model.Node{}).Where("id = ?", n.ID).
				Update("name", newName).Error; err != nil {
				return fmt.Errorf("重命名重名节点 id=%d 失败: %w", n.ID, err)
			}
		}
	}
	return nil
}

// migrateGRPCPortColumn 将旧的 g_rpc_port 列迁移为 grpc_port。
// GORM 对 GRPCPort 的默认 snake_case 转换是 g_r_p_c_port，
// 显式 column tag 修正为 grpc_port，这里处理已有数据库的列重命名。
func migrateGRPCPortColumn(db *gorm.DB) error {
	// 检查 nodes 表是否存在
	if !db.Migrator().HasTable("nodes") {
		return nil
	}

	// 检查旧列是否存在
	if !db.Migrator().HasColumn("nodes", "g_rpc_port") {
		return nil
	}

	// 重命名列：g_rpc_port → grpc_port
	if err := db.Exec("ALTER TABLE nodes RENAME COLUMN g_rpc_port TO grpc_port").Error; err != nil {
		return fmt.Errorf("迁移 g_rpc_port 列失败: %w", err)
	}

	return nil
}

// dynamicGormLogger 让 GORM 的 SQL 日志跟随 FR-225 调试开关（FIX-7）。
//
// 原实现把 GORM logger 硬编码为 logger.Info，与 config.LogLevelVar（FR-063/225 运行时日志级别）
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
