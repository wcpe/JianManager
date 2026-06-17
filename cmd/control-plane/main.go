package main

import (
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"

	"github.com/wxys233/JianManager/internal/controlplane/config"
	"github.com/wxys233/JianManager/internal/controlplane/database"
	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/router"
	"github.com/wxys233/JianManager/internal/controlplane/service"
	"github.com/wxys233/JianManager/proto/workerpb"
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
	pool := cpgrpc.NewClientPool()
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	terminalSvc := service.NewTerminalService(db, cfg.JWT.Secret, fmt.Sprintf("ws://localhost:%d", cfg.Server.Port))
	fileSvc := service.NewFileService(db, pool)
	configSvc := service.NewConfigService(db, pool)
	botSvc := service.NewBotService(db, pool)
	alertSvc := service.NewAlertService(db)
	scheduleSvc := service.NewScheduleService(db)
	backupSvc := service.NewBackupService(db, pool)
	templateSvc := service.NewTemplateService(db)
	auditSvc := service.NewAuditService(db)
	authzSvc := service.NewAuthzService(db)
	eventSvc := service.NewEventService(pool)

	// 告警评估器：每 60s 检测节点指标，触发 Webhook 通知
	alertEvaluator := service.NewAlertEvaluator(db)
	alertEvaluator.Start()
	defer alertEvaluator.Stop()

	// 实例事件服务：订阅 Worker 状态变更流并推送给前端 SSE
	defer eventSvc.Stop()

	// 定时任务调度器：每分钟检查到期任务并执行
	scheduleExecutor := service.NewScheduleExecutorImpl(db, instanceSvc, backupSvc, pool)
	scheduler := service.NewScheduler(db, scheduleExecutor)
	scheduler.Start()
	defer scheduler.Stop()

	r := router.Setup(&router.Services{
		Auth:     authSvc,
		User:     userSvc,
		Group:    groupSvc,
		Node:     nodeSvc,
		Instance: instanceSvc,
		Terminal: terminalSvc,
		File:     fileSvc,
		Config:   configSvc,
		Bot:      botSvc,
		Alert:    alertSvc,
		Schedule: scheduleSvc,
		Backup:   backupSvc,
		Template: templateSvc,
		Audit:    auditSvc,
		Authz:    authzSvc,
		Event:    eventSvc,
	}, cfg.JWT.Secret)

	// 注册 WebSocket 终端代理（浏览器 → CP → Worker）
	terminalProxy := service.NewTerminalProxy(cfg.JWT.Secret, terminalSvc)
	r.GET("/ws/terminal", gin.WrapF(terminalProxy.Handler()))

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	slog.Info("Control Plane 启动", "addr", addr)

	// 启动 gRPC 服务器（用于 Worker Node 注册和心跳）
	grpcHandler := cpgrpc.NewControlPlaneHandler(db, pool)
	grpcHandler.SetOnWorkerConnect(func(nodeUUID string) {
		eventSvc.StartWorkerStream(nodeUUID)
	})
	grpcServer := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(grpcServer, grpcHandler)

	grpcAddr := fmt.Sprintf(":%d", cfg.GRPC.Port)
	grpcListener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("监听 gRPC 端口失败: %v", err)
	}

	go func() {
		slog.Info("gRPC 服务器就绪", "addr", grpcAddr)
		if err := grpcServer.Serve(grpcListener); err != nil {
			slog.Error("gRPC 服务器退出", "error", err)
		}
	}()

	// 启动离线检测器
	cpgrpc.StartOfflineDetector(db)

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
