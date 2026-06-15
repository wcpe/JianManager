package grpc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/wxys233/JianManager/internal/worker/process"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// Server Worker Node gRPC 服务器实现。
type Server struct {
	workerpb.WorkerServiceServer
	manager *process.Manager
	nodeUUID string
}

// NewServer 创建 Worker gRPC 服务器。
func NewServer(manager *process.Manager, nodeUUID string) *Server {
	return &Server{
		manager:  manager,
		nodeUUID: nodeUUID,
	}
}

// Register 处理节点注册。
func (s *Server) Register(ctx context.Context, req *workerpb.RegisterRequest) (*workerpb.RegisterResponse, error) {
	slog.Info("收到注册请求", "name", req.Name, "host", req.Host)
	// 实际注册逻辑由 Worker 启动时调用 Control Plane 完成
	return &workerpb.RegisterResponse{
		NodeUuid:   s.nodeUUID,
		NodeSecret: "placeholder",
	}, nil
}

// CreateInstance 创建实例。
func (s *Server) CreateInstance(ctx context.Context, req *workerpb.CreateInstanceRequest) (*workerpb.CreateInstanceResponse, error) {
	err := s.manager.Create(
		req.InstanceUuid,
		req.Name,
		req.StartCommand,
		req.WorkDir,
		req.EnvVars,
		req.AutoRestart,
	)
	if err != nil {
		return &workerpb.CreateInstanceResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

// StartInstance 启动实例。
func (s *Server) StartInstance(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.InstanceActionResponse, error) {
	if err := s.manager.Start(req.InstanceUuid); err != nil {
		return &workerpb.InstanceActionResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// StopInstance 停止实例。
func (s *Server) StopInstance(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.InstanceActionResponse, error) {
	if err := s.manager.Stop(req.InstanceUuid); err != nil {
		return &workerpb.InstanceActionResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// RestartInstance 重启实例。
func (s *Server) RestartInstance(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.InstanceActionResponse, error) {
	if err := s.manager.Kill(req.InstanceUuid); err != nil {
		// 忽略 kill 错误（可能已停止）
	}
	if err := s.manager.Start(req.InstanceUuid); err != nil {
		return &workerpb.InstanceActionResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// KillInstance 强制终止实例。
func (s *Server) KillInstance(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.InstanceActionResponse, error) {
	if err := s.manager.Kill(req.InstanceUuid); err != nil {
		return &workerpb.InstanceActionResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// SendCommand 向实例发送命令。
func (s *Server) SendCommand(ctx context.Context, req *workerpb.SendCommandRequest) (*workerpb.SendCommandResponse, error) {
	if err := s.manager.SendCommand(req.InstanceUuid, req.Command); err != nil {
		return &workerpb.SendCommandResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.SendCommandResponse{Success: true}, nil
}

// GetInstanceStatus 获取实例状态。
func (s *Server) GetInstanceStatus(ctx context.Context, req *workerpb.InstanceActionRequest) (*workerpb.GetInstanceStatusResponse, error) {
	state, err := s.manager.GetState(req.InstanceUuid)
	if err != nil {
		return nil, fmt.Errorf("获取实例状态失败: %w", err)
	}
	return &workerpb.GetInstanceStatusResponse{
		InstanceUuid: req.InstanceUuid,
		State:        string(state),
	}, nil
}

// ListInstances 列出所有实例。
func (s *Server) ListInstances(ctx context.Context, req *workerpb.ListInstancesRequest) (*workerpb.ListInstancesResponse, error) {
	instances := s.manager.ListInstances()
	result := make([]*workerpb.InstanceInfo, len(instances))
	for i, inst := range instances {
		state, _ := s.manager.GetState(inst)
		result[i] = &workerpb.InstanceInfo{
			InstanceUuid: inst,
			State:        string(state),
		}
	}
	return &workerpb.ListInstancesResponse{Instances: result}, nil
}

// GetNodeMetrics 获取节点指标。
func (s *Server) GetNodeMetrics(ctx context.Context, req *workerpb.GetNodeMetricsRequest) (*workerpb.GetNodeMetricsResponse, error) {
	// TODO: 实际指标采集
	return &workerpb.GetNodeMetricsResponse{}, nil
}

// GetInstanceMetrics 获取实例指标。
func (s *Server) GetInstanceMetrics(ctx context.Context, req *workerpb.GetInstanceMetricsRequest) (*workerpb.GetInstanceMetricsResponse, error) {
	// TODO: 实际指标采集
	return &workerpb.GetInstanceMetricsResponse{}, nil
}

// IssueTerminalToken 签发终端 token（由 Control Plane 处理，此处不实现）。
func (s *Server) IssueTerminalToken(ctx context.Context, req *workerpb.IssueTerminalTokenRequest) (*workerpb.IssueTerminalTokenResponse, error) {
	return nil, fmt.Errorf("终端 token 由 Control Plane 签发")
}
