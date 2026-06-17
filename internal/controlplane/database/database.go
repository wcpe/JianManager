package database

import (
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wxys233/JianManager/internal/controlplane/config"
	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// New 创建并返回数据库连接。
func New(cfg config.DatabaseConfig) (*gorm.DB, error) {
	var dialector gorm.Dialector

	switch cfg.Driver {
	case "sqlite":
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

// AutoMigrate 自动迁移所有模型。
func AutoMigrate(db *gorm.DB) error {
	// 迁移 g_rpc_port → grpc_port（修复 GORM snake_case 对全大写缩写的错误转换）
	if err := migrateGRPCPortColumn(db); err != nil {
		return err
	}

	return db.AutoMigrate(
		&model.User{},
		&model.Group{},
		&model.GroupMember{},
		&model.GroupQuota{},
		&model.Node{},
		&model.NodeJDK{},
		&model.Instance{},
		&model.GroupInstance{},
		&model.Bot{},
		&model.AlertRule{},
		&model.AlertEvent{},
		&model.Schedule{},
		&model.ScheduleExecutionLog{},
		&model.Backup{},
		&model.Template{},
		&model.AuditLog{},
		&model.InstanceConfigVersion{},
	)
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
