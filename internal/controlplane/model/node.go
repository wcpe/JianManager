package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NodeStatus 节点状态。
type NodeStatus int

const (
	NodeStatusOffline  NodeStatus = 0 // 离线
	NodeStatusOnline   NodeStatus = 1 // 在线
	NodeStatusStarting NodeStatus = 2 // 启动中
)

// Node Worker Node 节点。
type Node struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	UUID string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	// Name 节点名（人类可读标签）。身份由 UUID 锚定（见 ADR-039），name 仅为可变标签，
	// 但活跃节点间名字唯一：唯一性由 database.AutoMigrate 建的「部分唯一索引」
	// （仅约束 deleted_at IS NULL 的活跃行）强制，软删除节点可释放其名供新节点复用。
	Name        string     `gorm:"type:varchar(128);not null" json:"name"`
	Host        string     `gorm:"type:varchar(256);not null" json:"host"`
	GRPCPort    int        `gorm:"column:grpc_port;not null" json:"grpcPort"`
	WSPort      int        `gorm:"not null" json:"wsPort"`
	Secret      string     `gorm:"type:varchar(128);not null" json:"-"`
	Status      NodeStatus `gorm:"default:0" json:"status"`
	OS          string     `gorm:"type:varchar(64)" json:"os"`
	Arch        string     `gorm:"type:varchar(32)" json:"arch"`
	CPUCores    int        `json:"cpuCores"`
	MemoryMB    int64      `json:"memoryMb"`
	DiskTotalMB int64      `json:"diskTotalMb"`
	CPUUsage    float32    `gorm:"default:0" json:"cpuUsage"`
	MemoryUsage float32    `gorm:"default:0" json:"memoryUsage"`
	DiskUsage   float32    `gorm:"default:0" json:"diskUsage"`
	// Maintenance 维护模式（cordon）标记。为 true 时禁止新实例调度/分配到本节点，
	// 与 Status（在线/离线，由心跳驱动）正交：节点可同时「在线 + 维护中」。
	// 参见 FR-048。
	Maintenance      bool  `gorm:"default:false" json:"maintenance"`
	MemoryUsedMB     int64 `gorm:"default:0" json:"memoryUsedMb"`
	DiskUsedMB       int64 `gorm:"default:0" json:"diskUsedMb"`
	NetworkBytesSent int64 `gorm:"default:0" json:"networkBytesSent"`
	NetworkBytesRecv int64 `gorm:"default:0" json:"networkBytesRecv"`
	// LoadAvg1 节点 1 分钟 load average（FR-062，心跳驱动）。
	LoadAvg1 float64 `gorm:"default:0" json:"loadAvg1"`
	// ManagedRuntimeObservedAt 是 Worker 随已认证 Heartbeat 上报受管运行时快照的实际观测时间。
	// 运行时不可用或旧 Worker 未上报时，相关字段必须清空，避免陈旧值被当成当前资源。
	ManagedRuntimeObservedAt *time.Time `json:"managedRuntimeObservedAt"`
	WorkerProcessRSSBytes    *int64     `json:"workerProcessRssBytes"`
	WorkerProcessCPUPct      *float64   `json:"workerProcessCpuPct"`
	BotWorkerRSSBytes        *int64     `json:"botWorkerRssBytes"`
	BotWorkerCPUPct          *float64   `json:"botWorkerCpuPct"`
	BotActiveCount           *int32     `json:"botActiveCount"`
	BotConnectingCount       *int32     `json:"botConnectingCount"`
	BotEventLoopP95MS        *float64   `json:"botEventLoopP95Ms"`
	BotCapacityMax           *int32     `json:"botCapacityMax"`
	BotCapacityUnavailableReason string `gorm:"type:varchar(256)" json:"botCapacityUnavailableReason"`
	BotAvailable             bool       `gorm:"default:false" json:"botAvailable"`
	BotUnavailableReason     string     `gorm:"type:varchar(256)" json:"botUnavailableReason"`
	// ProxyMode 节点出站代理模式（FR-185，见 ADR-043）：
	//   inherit（默认）= 用平台全局默认代理（settings DB > control-plane.yml > env）；
	//   custom         = 用本节点自定义 ProxyURL/ProxyNoProxy。
	// CP 据此算每节点期望代理经心跳响应下发，Worker 运行时重建出站 client（免登机器改 worker.yml）。
	ProxyMode string `gorm:"type:varchar(16);default:inherit" json:"proxyMode"`
	// ProxyURL 节点自定义代理地址（仅 ProxyMode=custom 有效；含凭据，API 响应/日志脱敏）。
	ProxyURL string `gorm:"type:varchar(512)" json:"-"`
	// ProxyNoProxy 节点自定义免代理列表（逗号分隔，仅 ProxyMode=custom 有效）。
	ProxyNoProxy  string     `gorm:"type:varchar(1024)" json:"proxyNoProxy"`
	LastHeartbeat *time.Time `json:"lastHeartbeat"`
	// RuntimeSyncedAt 上次运行时库存从 Worker 同步成功的时间（FR-301）。
	// JDKService.syncFromWorker 成功即刷新（含 JDK 面板隐式同步与运行时资产页手动刷新）；
	// nil = 从未同步。运行时资产页据此显示「上次同步 <相对时间>」并识别陈旧节点。
	RuntimeSyncedAt *time.Time     `json:"runtimeSyncedAt"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`

	// TunnelConnected 该节点当前是否有活跃反向隧道（FR-281，见 ADR-066）。
	// 运行态字段不落库（gorm:"-"），由 NodeService 在响应时据 TunnelRegistry 填充：
	// true=指令经隧道下发（NAT/内网节点可用），false=直拨回退（老 worker/隧道重建窗口）。
	TunnelConnected bool `gorm:"-" json:"tunnelConnected"`
}

// BeforeCreate 创建前自动生成 UUID。
func (n *Node) BeforeCreate(tx *gorm.DB) error {
	if n.UUID == "" {
		n.UUID = uuid.New().String()
	}
	return nil
}
