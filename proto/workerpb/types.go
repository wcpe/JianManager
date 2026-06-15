// Package workerpb 由 proto/worker.proto 手动生成。
// 当 protoc 可用时，运行 make proto 重新生成。
package workerpb

// RegisterRequest 节点注册请求。
type RegisterRequest struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	GrpcPort    int32  `json:"grpcPort"`
	WsPort      int32  `json:"wsPort"`
	Os          string `json:"os"`
	Arch        string `json:"arch"`
	CpuCores    int32  `json:"cpuCores"`
	MemoryMb    int64  `json:"memoryMb"`
	DiskTotalMb int64  `json:"diskTotalMb"`
}

// RegisterResponse 节点注册响应。
type RegisterResponse struct {
	NodeUuid   string `json:"nodeUuid"`
	NodeSecret string `json:"nodeSecret"`
}

// HeartbeatRequest 心跳请求。
type HeartbeatRequest struct {
	NodeUuid     string  `json:"nodeUuid"`
	CpuUsage     float32 `json:"cpuUsage"`
	MemoryUsage  float32 `json:"memoryUsage"`
	DiskUsage    float32 `json:"diskUsage"`
	MemoryUsedMb int64   `json:"memoryUsedMb"`
	DiskUsedMb   int64   `json:"diskUsedMb"`
}

// HeartbeatResponse 心跳响应。
type HeartbeatResponse struct {
	Timestamp int64 `json:"timestamp"`
}

// CreateInstanceRequest 创建实例请求。
type CreateInstanceRequest struct {
	InstanceUuid string            `json:"instanceUuid"`
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	ProcessType  string            `json:"processType"`
	StartCommand string            `json:"startCommand"`
	WorkDir      string            `json:"workDir"`
	EnvVars      map[string]string `json:"envVars"`
	AutoRestart  bool              `json:"autoRestart"`
}

// CreateInstanceResponse 创建实例响应。
type CreateInstanceResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// InstanceActionRequest 实例操作请求。
type InstanceActionRequest struct {
	InstanceUuid string `json:"instanceUuid"`
}

// InstanceActionResponse 实例操作响应。
type InstanceActionResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// SendCommandRequest 发送命令请求。
type SendCommandRequest struct {
	InstanceUuid string `json:"instanceUuid"`
	Command      string `json:"command"`
}

// SendCommandResponse 发送命令响应。
type SendCommandResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// GetInstanceStatusResponse 获取实例状态响应。
type GetInstanceStatusResponse struct {
	InstanceUuid string `json:"instanceUuid"`
	State        string `json:"state"`
}

// ListInstancesRequest 列出实例请求。
type ListInstancesRequest struct{}

// ListInstancesResponse 列出实例响应。
type ListInstancesResponse struct {
	Instances []*InstanceInfo `json:"instances"`
}

// InstanceInfo 实例信息。
type InstanceInfo struct {
	InstanceUuid string `json:"instanceUuid"`
	Name         string `json:"name"`
	State        string `json:"state"`
	Type         string `json:"type"`
	StartedAt    int64  `json:"startedAt"`
	Pid          int32  `json:"pid"`
}

// StreamInstanceEventsRequest 订阅实例事件请求。
type StreamInstanceEventsRequest struct {
	InstanceUuid string `json:"instanceUuid"` // 空表示所有实例
}

// InstanceEvent 实例事件。
type InstanceEvent struct {
	InstanceUuid string `json:"instanceUuid"`
	Type         string `json:"type"`
	Data         string `json:"data"`
	Timestamp    int64  `json:"timestamp"`
}

// IssueTerminalTokenRequest 签发终端 token 请求。
type IssueTerminalTokenRequest struct {
	InstanceUuid string `json:"instanceUuid"`
	Permission   string `json:"permission"`
}

// IssueTerminalTokenResponse 签发终端 token 响应。
type IssueTerminalTokenResponse struct {
	Token     string `json:"token"`
	WsUrl     string `json:"wsUrl"`
	ExpiresIn int32  `json:"expiresIn"`
}

// ListFilesRequest 列出文件请求。
type ListFilesRequest struct {
	InstanceUuid string `json:"instanceUuid"`
	Path         string `json:"path"`
}

// ListFilesResponse 列出文件响应。
type ListFilesResponse struct {
	Files []*FileInfo `json:"files"`
}

// FileInfo 文件信息。
type FileInfo struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
}

// ReadFileRequest 读取文件请求。
type ReadFileRequest struct {
	InstanceUuid string `json:"instanceUuid"`
	Path         string `json:"path"`
}

// ReadFileResponse 读取文件响应。
type ReadFileResponse struct {
	Content []byte `json:"content"`
}

// WriteFileRequest 写入文件请求。
type WriteFileRequest struct {
	InstanceUuid string `json:"instanceUuid"`
	Path         string `json:"path"`
	Content      []byte `json:"content"`
}

// WriteFileResponse 写入文件响应。
type WriteFileResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// DeleteFileRequest 删除文件请求。
type DeleteFileRequest struct {
	InstanceUuid string `json:"instanceUuid"`
	Path         string `json:"path"`
}

// DeleteFileResponse 删除文件响应。
type DeleteFileResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// GetNodeMetricsRequest 获取节点指标请求。
type GetNodeMetricsRequest struct{}

// GetNodeMetricsResponse 获取节点指标响应。
type GetNodeMetricsResponse struct {
	CpuUsage     float32 `json:"cpuUsage"`
	MemoryUsage  float32 `json:"memoryUsage"`
	DiskUsage    float32 `json:"diskUsage"`
	MemoryUsedMb int64   `json:"memoryUsedMb"`
	MemoryTotalMb int64  `json:"memoryTotalMb"`
	DiskUsedMb   int64   `json:"diskUsedMb"`
	DiskTotalMb  int64   `json:"diskTotalMb"`
}

// GetInstanceMetricsRequest 获取实例指标请求。
type GetInstanceMetricsRequest struct {
	InstanceUuid string `json:"instanceUuid"`
}

// GetInstanceMetricsResponse 获取实例指标响应。
type GetInstanceMetricsResponse struct {
	Tps           float32 `json:"tps"`
	OnlinePlayers int32   `json:"onlinePlayers"`
	MemoryMb      int64   `json:"memoryMb"`
}
