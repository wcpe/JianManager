package main

import (
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/wxys233/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/database"
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

	authSvc := service.NewAuthService(db, cfg.JWT)
	userSvc := service.NewUserService(db)
	groupSvc := service.NewGroupService(db)
	nodeSvc := service.NewNodeService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc)
	terminalSvc := service.NewTerminalService(db, cfg.JWT.Secret)
	pool := cpgrpc.NewClientPool()
	fileSvc := service.NewFileService(db, pool)
	botSvc := service.NewBotService(db)
	alertSvc := service.NewAlertService(db)
	scheduleSvc := service.NewScheduleService(db)
	backupSvc := service.NewBackupService(db)
	templateSvc := service.NewTemplateService(db)
	auditSvc := service.NewAuditService(db)

	r := router.Setup(&router.Services{
		Auth:     authSvc,
		User:     userSvc,
		Group:    groupSvc,
		Node:     nodeSvc,
		Instance: instanceSvc,
		Terminal: terminalSvc,
		File:     fileSvc,
		Bot:      botSvc,
		Alert:    alertSvc,
		Schedule: scheduleSvc,
		Backup:   backupSvc,
		Template: templateSvc,
		Audit:    auditSvc,
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
