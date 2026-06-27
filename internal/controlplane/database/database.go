package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
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
		&model.NodeEnrollToken{},
		&model.Instance{},
		&model.GroupInstance{},
		// 实例组织分组树（FR-165，见 ADR-033）：自引用邻接表 + 实例 M:N，
		// 与用户组 / 网络群组正交，仅供组织归类，不承载 RBAC / 部署语义。
		&model.InstanceGroupNode{},
		&model.InstanceGroupMember{},
		&model.ServerRegistration{},
		&model.Network{},
		&model.NetworkMember{},
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
		&model.LogEntry{},
		&model.MetricSeries{},
		&model.MetricSampleRaw{},
		&model.MetricRollup5m{},
		&model.MetricRollup1h{},
		&model.PlatformSetting{},
		&model.ClientChannel{},
		&model.ClientPullKey{},
		&model.ClientVersion{},
		&model.ClientMachine{},
		&model.ClientDistEvent{},
		&model.ClientDistDaily{},
		&model.ClientIPRule{},
		&model.ClientTelemetry{},
		&model.ClientTelemetryDaily{},
		// JBIS 业务事件汇聚（FR-116 底座 / FR-122 经济，见 ADR-028）：
		// 通用 envelope（按 domain+dedupKey 去重）+ 经济结构化镜像（node→zone 维度）+ 经济变更审计。
		&model.BusinessEvent{},
		&model.EconomyBalanceMirror{},
		&model.EconomyLedgerEntry{},
	); err != nil {
		return err
	}

	// 节点名活跃唯一约束（见 ADR-039，修复 BUG-A）：先对存量重名活跃节点去重，再建部分唯一索引。
	// 必须在 AutoMigrate 建表后执行（依赖 nodes 表与 deleted_at 列存在）。
	return migrateNodeNameUnique(db)
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
