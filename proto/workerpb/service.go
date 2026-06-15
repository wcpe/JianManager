// Package workerpb 由 proto/worker.proto 手动生成。
// 当 protoc 可用时，运行 make proto 重新生成。
package workerpb

import (
	"context"
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
