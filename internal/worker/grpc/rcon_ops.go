package grpc

import (
	"context"
	"log/slog"

	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ExecRconCommand 经实例 RCON 执行一条命令并返回原始输出（FR-054 玩家管理）。
//
// 为什么由 CP 下发 RCON 端口/密码：架构不变量要求 Worker 不访问数据库，RCON 凭据
// 持久化在 CP 侧（model.Instance.RCONPort/RCONPassword），故随请求传入，Worker 仅
// 负责连接 localhost:rcon_port 执行命令。实例必须处于 RUNNING 才有 RCON 监听。
//
// 返回 available=false（而非 gRPC error）表示 RCON 不可用（未运行/未配置/连接失败/执行失败），
// 让 CP 聚合多后端时不因单点失败中断，并向前端展示「该子服 RCON 不可用」的优雅降级提示。
func (s *Server) ExecRconCommand(ctx context.Context, req *workerpb.ExecRconCommandRequest) (*workerpb.ExecRconCommandResponse, error) {
	// 实例必须存在且运行中，否则 RCON 端口不会监听。
	state, err := s.manager.GetState(req.InstanceUuid)
	if err != nil {
		return &workerpb.ExecRconCommandResponse{Available: false, Error: "实例不存在"}, nil
	}
	if state != "RUNNING" {
		return &workerpb.ExecRconCommandResponse{Available: false, Error: "实例未运行"}, nil
	}

	if req.RconPort <= 0 {
		return &workerpb.ExecRconCommandResponse{Available: false, Error: "实例未配置 RCON"}, nil
	}

	// RCON 连接的是实例本机端口，固定 localhost（与 GetInstanceMetrics 一致）。
	client := metrics.NewRCONClient("localhost", int(req.RconPort), req.RconPassword)
	defer client.Close()

	if err := client.Connect(); err != nil {
		slog.Debug("RCON 连接失败，优雅降级", "instanceId", req.InstanceUuid, "port", req.RconPort, "error", err)
		return &workerpb.ExecRconCommandResponse{Available: false, Error: "RCON 连接失败"}, nil
	}

	output, err := client.SendCommand(req.Command)
	if err != nil {
		slog.Warn("RCON 命令执行失败", "instanceId", req.InstanceUuid, "command", req.Command, "error", err)
		return &workerpb.ExecRconCommandResponse{Available: false, Error: "RCON 命令执行失败"}, nil
	}

	return &workerpb.ExecRconCommandResponse{Available: true, Output: output}, nil
}
