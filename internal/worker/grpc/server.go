package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/wxys233/JianManager/internal/worker/metrics"
	"github.com/wxys233/JianManager/internal/worker/process"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// instanceEvent 内部事件，由 Manager 状态回调产生，分发给所有 StreamInstanceEvents 订阅者。
type instanceEvent struct {
	UUID      string
	OldState  string
	NewState  string
	Timestamp int64
}

// Server Worker Node gRPC 服务器实现。
// 仅实现 Control Plane 反向调用 Worker 的 RPC（实例操作、文件操作、指标）。
// 生命周期 RPC（Register/Heartbeat）由 Worker 主动发起，不由 CP 调用 Worker，
// 因此走 UnimplementedWorkerServiceServer 的默认实现，返回 codes.Unimplemented，
// 避免因嵌入接口导致未实现方法被调用时 panic。
type Server struct {
	workerpb.UnimplementedWorkerServiceServer
	manager   *process.Manager
	nodeUUID  string
	collector *metrics.Collector

	// eventMu 保护 eventSubs，StreamInstanceEvents 订阅/取消订阅时加锁。
	eventMu   sync.Mutex
	eventSubs []chan instanceEvent
}

// NewServer 创建 Worker gRPC 服务器。
// 注册 Manager 状态变更回调，将事件扇出到所有 StreamInstanceEvents 订阅者。
func NewServer(manager *process.Manager, nodeUUID string, collector *metrics.Collector) *Server {
	s := &Server{
		manager:   manager,
		nodeUUID:  nodeUUID,
		collector: collector,
	}
	manager.SetStateChangeHandler(func(uuid string, oldState, newState process.InstanceState) {
		evt := instanceEvent{
			UUID:      uuid,
			OldState:  string(oldState),
			NewState:  string(newState),
			Timestamp: time.Now().Unix(),
		}
		s.eventMu.Lock()
		subs := make([]chan instanceEvent, len(s.eventSubs))
		copy(subs, s.eventSubs)
		s.eventMu.Unlock()
		for _, ch := range subs {
			select {
			case ch <- evt:
			default:
				// 订阅者消费太慢，丢弃事件避免阻塞
			}
		}
	})
	return s
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
		process.ProcessType(req.ProcessType),
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
	if s.collector == nil {
		return &workerpb.GetNodeMetricsResponse{}, nil
	}

	m := s.collector.Collect()
	return &workerpb.GetNodeMetricsResponse{
		CpuUsage:     m.CPUUsage,
		MemoryUsage:  m.MemoryUsage,
		DiskUsage:    m.DiskUsage,
		MemoryUsedMb: m.MemoryUsedMB,
		MemoryTotalMb: m.MemoryTotalMB,
		DiskUsedMb:   m.DiskUsedMB,
		DiskTotalMb:  m.DiskTotalMB,
	}, nil
}

// GetInstanceMetrics 获取实例指标。
func (s *Server) GetInstanceMetrics(ctx context.Context, req *workerpb.GetInstanceMetricsRequest) (*workerpb.GetInstanceMetricsResponse, error) {
	resp := &workerpb.GetInstanceMetricsResponse{}

	// 获取实例状态，确认实例存在
	state, err := s.manager.GetState(req.InstanceUuid)
	if err != nil {
		return resp, fmt.Errorf("实例不存在: %w", err)
	}

	// 仅运行中的实例有指标
	if state != "RUNNING" {
		return resp, nil
	}

	// 通过 RCON 查询 MC 专用指标
	rconPort, rconPassword, err := s.manager.GetRCONConfig(req.InstanceUuid)
	if err != nil || rconPort == 0 {
		// 没有 RCON 配置，返回默认值
		resp.Tps = 20.0
		resp.OnlinePlayers = 0
		return resp, nil
	}

	tps, onlinePlayers, _ := metrics.QueryInstanceMetrics("localhost", rconPort, rconPassword)
	resp.Tps = tps
	resp.OnlinePlayers = onlinePlayers
	resp.MemoryMb = 0

	return resp, nil
}

// IssueTerminalToken 签发终端 token。
// 有意不在 Worker 侧实现：终端 token 由 Control Plane 签发并代理（见 FR-007/FR-019 决策），
// 浏览器经 CP 拿到 token 后直连 Worker WS。此处返回明确错误而非走 Unimplemented，
// 便于调用方区分「该能力归属 CP」与「Worker 未实现」。
func (s *Server) IssueTerminalToken(ctx context.Context, req *workerpb.IssueTerminalTokenRequest) (*workerpb.IssueTerminalTokenResponse, error) {
	return nil, fmt.Errorf("终端 token 由 Control Plane 签发，Worker 不实现此 RPC")
}

// StreamInstanceEvents 订阅实例状态变更事件流。
// CP 调用此 RPC 后，Worker 持续推送实例状态转换事件（STOPPED→STARTING→RUNNING 等）。
// instance_uuid 为空时表示订阅所有实例。流关闭时自动取消订阅。
func (s *Server) StreamInstanceEvents(req *workerpb.StreamInstanceEventsRequest, stream workerpb.WorkerService_StreamInstanceEventsServer) error {
	ch := make(chan instanceEvent, 64)
	s.eventMu.Lock()
	s.eventSubs = append(s.eventSubs, ch)
	s.eventMu.Unlock()

	defer func() {
		s.eventMu.Lock()
		for i, sub := range s.eventSubs {
			if sub == ch {
				s.eventSubs = append(s.eventSubs[:i], s.eventSubs[i+1:]...)
				break
			}
		}
		s.eventMu.Unlock()
		close(ch)
	}()

	slog.Info("StreamInstanceEvents 订阅开始", "filter", req.InstanceUuid)

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			// 按 instance_uuid 过滤
			if req.InstanceUuid != "" && evt.UUID != req.InstanceUuid {
				continue
			}
			if err := stream.Send(&workerpb.InstanceEvent{
				InstanceUuid: evt.UUID,
				Type:         "state_change",
				Data:         fmt.Sprintf("%s→%s", evt.OldState, evt.NewState),
				Timestamp:    evt.Timestamp,
			}); err != nil {
				return err
			}
		}
	}
}
