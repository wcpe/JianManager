package main

import (
	"fmt"
	"log"
	"log/slog"
	"os"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/config"
	"github.com/wxys233/JianManager/internal/controlplane/database"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/controlplane/router"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

func main() {
	cfgPath := ""
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	initLogger(cfg.Log)

	db, err := database.New(cfg.Database)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}

	if err := database.AutoMigrate(db); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}

	if err := bootstrapAdmin(db, cfg.Bootstrap); err != nil {
		slog.Warn("初始化管理员账号失败", "error", err)
	}

	authSvc := service.NewAuthService(db, cfg.JWT)
	userSvc := service.NewUserService(db)
	groupSvc := service.NewGroupService(db)
	nodeSvc := service.NewNodeService(db)

	r := router.Setup(&router.Services{
		Auth:  authSvc,
		User:  userSvc,
		Group: groupSvc,
		Node:  nodeSvc,
	}, cfg.JWT.Secret)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	slog.Info("Control Plane 启动", "addr", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("启动服务器失败: %v", err)
	}
}

func initLogger(cfg config.LogConfig) {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// bootstrapAdmin 确保管理员账号存在。
func bootstrapAdmin(db *gorm.DB, cfg config.BootstrapConfig) error {
	if cfg.AdminUsername == "" || cfg.AdminPassword == "" {
		return nil
	}

	var count int64
	db.Model(&model.User{}).Where("username = ?", cfg.AdminUsername).Count(&count)
	if count > 0 {
		return nil
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("加密管理员密码失败: %w", err)
	}

	admin := &model.User{
		Username: cfg.AdminUsername,
		Password: string(hashed),
		Role:     model.RolePlatformAdmin,
		Status:   model.UserStatusActive,
	}

	if err := db.Create(admin).Error; err != nil {
		return fmt.Errorf("创建管理员账号失败: %w", err)
	}

	slog.Info("已创建管理员账号", "username", cfg.AdminUsername)
	return nil
}
