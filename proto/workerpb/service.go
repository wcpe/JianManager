// Package workerpb 由 proto/worker.proto 手动生成。
// 当 protoc 可用时，运行 make proto 重新生成。
package workerpb

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
)

// WorkerServiceServer Worker 服务端接口。
type WorkerServiceServer interface {
	Register(context.Context, *RegisterRequest) (*RegisterResponse, error)
	Heartbeat(WorkerService_HeartbeatServer) error
	CreateInstance(context.Context, *CreateInstanceRequest) (*CreateInstanceResponse, error)
	StartInstance(context.Context, *InstanceActionRequest) (*InstanceActionResponse, error)
	StopInstance(context.Context, *InstanceActionRequest) (*InstanceActionResponse, error)
	RestartInstance(context.Context, *InstanceActionRequest) (*InstanceActionResponse, error)
	KillInstance(context.Context, *InstanceActionRequest) (*InstanceActionResponse, error)
	SendCommand(context.Context, *SendCommandRequest) (*SendCommandResponse, error)
	GetInstanceStatus(context.Context, *InstanceActionRequest) (*GetInstanceStatusResponse, error)
	ListInstances(context.Context, *ListInstancesRequest) (*ListInstancesResponse, error)
	StreamInstanceEvents(*StreamInstanceEventsRequest, WorkerService_StreamInstanceEventsServer) error
	IssueTerminalToken(context.Context, *IssueTerminalTokenRequest) (*IssueTerminalTokenResponse, error)
	ListFiles(context.Context, *ListFilesRequest) (*ListFilesResponse, error)
	ReadFile(context.Context, *ReadFileRequest) (*ReadFileResponse, error)
	WriteFile(context.Context, *WriteFileRequest) (*WriteFileResponse, error)
	DeleteFile(context.Context, *DeleteFileRequest) (*DeleteFileResponse, error)
	GetNodeMetrics(context.Context, *GetNodeMetricsRequest) (*GetNodeMetricsResponse, error)
	GetInstanceMetrics(context.Context, *GetInstanceMetricsRequest) (*GetInstanceMetricsResponse, error)
}

// WorkerService_HeartbeatServer 心跳双向流服务端。
type WorkerService_HeartbeatServer interface {
	Recv() (*HeartbeatRequest, error)
	Send(*HeartbeatResponse) error
}

// WorkerService_StreamInstanceEventsServer 实例事件流服务端。
type WorkerService_StreamInstanceEventsServer interface {
	Send(*InstanceEvent) error
}

// WorkerServiceClient Worker 客户端接口。
type WorkerServiceClient interface {
	Register(ctx context.Context, in *RegisterRequest) (*RegisterResponse, error)
	Heartbeat(ctx context.Context) (WorkerService_HeartbeatClient, error)
	CreateInstance(ctx context.Context, in *CreateInstanceRequest) (*CreateInstanceResponse, error)
	StartInstance(ctx context.Context, in *InstanceActionRequest) (*InstanceActionResponse, error)
	StopInstance(ctx context.Context, in *InstanceActionRequest) (*InstanceActionResponse, error)
	RestartInstance(ctx context.Context, in *InstanceActionRequest) (*InstanceActionResponse, error)
	KillInstance(ctx context.Context, in *InstanceActionRequest) (*InstanceActionResponse, error)
	SendCommand(ctx context.Context, in *SendCommandRequest) (*SendCommandResponse, error)
	GetInstanceStatus(ctx context.Context, in *InstanceActionRequest) (*GetInstanceStatusResponse, error)
	ListInstances(ctx context.Context, in *ListInstancesRequest) (*ListInstancesResponse, error)
	StreamInstanceEvents(ctx context.Context, in *StreamInstanceEventsRequest) (WorkerService_StreamInstanceEventsClient, error)
	IssueTerminalToken(ctx context.Context, in *IssueTerminalTokenRequest) (*IssueTerminalTokenResponse, error)
	ListFiles(ctx context.Context, in *ListFilesRequest) (*ListFilesResponse, error)
	ReadFile(ctx context.Context, in *ReadFileRequest) (*ReadFileResponse, error)
	WriteFile(ctx context.Context, in *WriteFileRequest) (*WriteFileResponse, error)
	DeleteFile(ctx context.Context, in *DeleteFileRequest) (*DeleteFileResponse, error)
	GetNodeMetrics(ctx context.Context, in *GetNodeMetricsRequest) (*GetNodeMetricsResponse, error)
	GetInstanceMetrics(ctx context.Context, in *GetInstanceMetricsRequest) (*GetInstanceMetricsResponse, error)
}

// WorkerService_HeartbeatClient 心跳双向流客户端。
type WorkerService_HeartbeatClient interface {
	Send(*HeartbeatRequest) error
	Recv() (*HeartbeatResponse, error)
}

// WorkerService_StreamInstanceEventsClient 实例事件流客户端。
type WorkerService_StreamInstanceEventsClient interface {
	Recv() (*InstanceEvent, error)
}

// RegisterWorkerServiceServer 注册服务端到 gRPC 服务器。
func RegisterWorkerServiceServer(s *grpc.Server, srv WorkerServiceServer) {
	// 当 protoc 生成代码时，此函数将由生成代码替代
}

// NewWorkerServiceClient 创建客户端。
func NewWorkerServiceClient(conn *grpc.ClientConn) WorkerServiceClient {
	// 当 protoc 生成代码时，此函数将由生成代码替代
	return &workerServiceClientImpl{conn: conn}
}

// workerServiceClientImpl 客户端实现桩。
type workerServiceClientImpl struct {
	conn *grpc.ClientConn
}

func (c *workerServiceClientImpl) Register(ctx context.Context, in *RegisterRequest) (*RegisterResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) Heartbeat(ctx context.Context) (WorkerService_HeartbeatClient, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) CreateInstance(ctx context.Context, in *CreateInstanceRequest) (*CreateInstanceResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) StartInstance(ctx context.Context, in *InstanceActionRequest) (*InstanceActionResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) StopInstance(ctx context.Context, in *InstanceActionRequest) (*InstanceActionResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) RestartInstance(ctx context.Context, in *InstanceActionRequest) (*InstanceActionResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) KillInstance(ctx context.Context, in *InstanceActionRequest) (*InstanceActionResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) SendCommand(ctx context.Context, in *SendCommandRequest) (*SendCommandResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) GetInstanceStatus(ctx context.Context, in *InstanceActionRequest) (*GetInstanceStatusResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) ListInstances(ctx context.Context, in *ListInstancesRequest) (*ListInstancesResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) StreamInstanceEvents(ctx context.Context, in *StreamInstanceEventsRequest) (WorkerService_StreamInstanceEventsClient, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) IssueTerminalToken(ctx context.Context, in *IssueTerminalTokenRequest) (*IssueTerminalTokenResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) ListFiles(ctx context.Context, in *ListFilesRequest) (*ListFilesResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) ReadFile(ctx context.Context, in *ReadFileRequest) (*ReadFileResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) WriteFile(ctx context.Context, in *WriteFileRequest) (*WriteFileResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) DeleteFile(ctx context.Context, in *DeleteFileRequest) (*DeleteFileResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) GetNodeMetrics(ctx context.Context, in *GetNodeMetricsRequest) (*GetNodeMetricsResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}

func (c *workerServiceClientImpl) GetInstanceMetrics(ctx context.Context, in *GetInstanceMetricsRequest) (*GetInstanceMetricsResponse, error) {
	return nil, fmt.Errorf("桩代码：protoc 生成后实现")
}
