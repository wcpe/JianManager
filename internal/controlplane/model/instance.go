package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// InstanceStatus 实例状态。
type InstanceStatus string

const (
	InstanceStatusStopped  InstanceStatus = "STOPPED"
	InstanceStatusStarting InstanceStatus = "STARTING"
	InstanceStatusRunning  InstanceStatus = "RUNNING"
	InstanceStatusStopping InstanceStatus = "STOPPING"
	InstanceStatusCrashed  InstanceStatus = "CRASHED"
	// InstanceStatusDamaged 损毁（FR-342）：一键搭建/代理搭建过程中任一阶段失败进入此态，
	// 原始搭建参数（ProvisionSpec）保留，可「重建」复用参数重跑搭建；损毁实例不可直接启动。
	InstanceStatusDamaged InstanceStatus = "DAMAGED"
)

// InstanceType 实例类型。
type InstanceType string

const (
	InstanceTypeMinecraftJava InstanceType = "minecraft_java"
	InstanceTypeGeneric       InstanceType = "generic"
)

// InstanceRole 实例角色。
//
// 分两类语义：
//   - MC 群组服角色（backend/proxy）：参与 proxy↔backend 拓扑、后端注册、玩家查询，
//     是 server_registrations 的合法两端。
//   - 非群组服角色（universal/beacon）：受 JM 生命周期管理，但不参与群组拓扑。
//
// 详见 ADR-007。
type InstanceRole string

const (
	// InstanceRoleBackend 后端子服（Paper/Spigot/Purpur），可注册进多个代理。
	InstanceRoleBackend InstanceRole = "backend"
	// InstanceRoleProxy 代理（BungeeCord/Waterfall/Velocity），聚合多个后端。
	InstanceRoleProxy InstanceRole = "proxy"
	// InstanceRoleUniversal 通用实例（默认；非群组服角色，保留自由命令）。
	InstanceRoleUniversal InstanceRole = "universal"
	// InstanceRoleBeacon 配套服务实例（如 Beacon 控制面）。
	// 非群组服角色：不参与 proxy↔backend 拓扑、不适用 MC 探针与后端注册、不参与玩家查询；
	// 仅作为受 JM 生命周期管理的普通服务进程存在，因此无法被选为代理或后端。
	InstanceRoleBeacon InstanceRole = "beacon"
)

// ValidInstanceType 校验实例类型是否在允许枚举内。
//
// 用于显式改 type 的入口（如 UpdateInstanceFields.Type）拒绝未知值：静默回落会让调用方
// 误以为改成功，而 type 是探针适用性判定（IsProbeApplicable）的关键输入，脏值代价高。
func ValidInstanceType(t InstanceType) bool {
	switch t {
	case InstanceTypeMinecraftJava, InstanceTypeGeneric:
		return true
	}
	return false
}

// ValidInstanceRole 校验角色是否在允许枚举内。
func ValidInstanceRole(r InstanceRole) bool {
	switch r {
	case InstanceRoleBackend, InstanceRoleProxy, InstanceRoleUniversal, InstanceRoleBeacon:
		return true
	}
	return false
}

// IsProbeApplicable 判断该实例是否适用 ServerProbe（Bukkit 插件）监控探针（FR-454）。
//
// ServerProbe 是 Bukkit 插件，只有 Minecraft Java 服务端能加载：
//   - 代理端（BungeeCord/Waterfall/Velocity，role=proxy）无法加载 Bukkit 插件；
//   - 通用二进制（type=generic，FR-441）与 Beacon（role=beacon，FR-442）不是 MC Java 进程。
//
// 因此不适用探针的实例不应被分配探针端口、不参与 /metrics 抓取、也不接收探针 jar 推送。
// 判定同时看 type 与 role：即使历史数据把 beacon 实例的 type 误记为 minecraft_java，
// role=beacon 仍会被正确判为不适用（FR-454 数据归正的代码层兜底）。
func IsProbeApplicable(t InstanceType, r InstanceRole) bool {
	if t != InstanceTypeMinecraftJava {
		return false
	}
	switch r {
	case InstanceRoleProxy, InstanceRoleBeacon:
		return false
	}
	return true
}

// NormalizeInstanceType 按 role 归一实例 type（FR-454），创建与更新两条路径共用同一口径，
// 避免「建实例归一并了、改 role 又把它改回脏值」的可复用缺陷。
//
// 归一规则（**单向**，仅定义 role→type 的必要约束）：
//
//	role=beacon ⇒ type=generic：Beacon 配套服务不是 Minecraft Java 进程，type 只能是 generic。
//
// 其余 role 保持传入 type 不变：generic 对 universal/backend/proxy 均是合法值（FR-441 通用
// 二进制、代理亦非 Bukkit 载体），因此**不能**反向强推 minecraft_java——那会把用户的通用二进制
// 误标成 MC Java 服务端。若需要把实例从 generic 恢复为 minecraft_java，调用方须显式传 type。
func NormalizeInstanceType(t InstanceType, r InstanceRole) InstanceType {
	if r == InstanceRoleBeacon {
		return InstanceTypeGeneric
	}
	return t
}

// ProcessType 启动方式。
type ProcessType string

const (
	ProcessTypeDirect ProcessType = "direct"
	ProcessTypeDaemon ProcessType = "daemon"
	ProcessTypeDocker ProcessType = "docker"
	ProcessTypeRCON   ProcessType = "rcon"
)

// Instance 实例。
type Instance struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	UUID        string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	NodeID      uint           `gorm:"not null;index" json:"nodeId"`
	Name        string         `gorm:"type:varchar(128);not null;index" json:"name"`
	Type        InstanceType   `gorm:"type:varchar(64);not null" json:"type"`
	Role        InstanceRole   `gorm:"type:varchar(16);default:universal;index" json:"role"`
	ProcessType ProcessType    `gorm:"type:varchar(32);not null" json:"processType"`
	Status      InstanceStatus `gorm:"type:varchar(32);default:STOPPED;index" json:"status"`
	// StatusReason 记录当前状态的原因，主要用于 CRASHED：异步委托（启动/停止）失败时写入具体错误
	// （如「实例未绑定 JDK…」），供前端显示，不再让用户只见「崩溃」无因。正常状态推进时清空。
	StatusReason     string `gorm:"type:varchar(512)" json:"statusReason"`
	StartCommand     string `gorm:"type:varchar(1024);not null" json:"startCommand"`
	JDKID            uint   `gorm:"index" json:"jdkId"`
	JavaMajorVersion int    `gorm:"index" json:"javaMajorVersion"`
	LaunchSpec       string `gorm:"type:text" json:"launchSpec"`
	// ProvisionSpec 存一键搭建/代理搭建的原始请求（JSON），供损毁后「重建」复用参数重跑搭建（FR-342）。
	// 仅经搭建入口创建的实例有值；手动/导入实例为空、不适用重建。
	ProvisionSpec string `gorm:"type:text" json:"provisionSpec,omitempty"`
	WorkDir       string `gorm:"type:varchar(512)" json:"workDir"`
	// WorkDirInPlace 就地导入标记（FR-302，见 ADR-069）：工作目录为托管区外的原始绝对路径
	// （ADR-007 系统分配原则的唯一合法例外）。删除实例时 CP 据此指示 Worker 跳过目录删除，
	// 原目录永不清理（双保险之一，另一道是 Worker 托管区守卫）。
	WorkDirInPlace bool   `gorm:"default:false" json:"workDirInPlace"`
	EnvVars        string `gorm:"type:text" json:"envVars"` // JSON
	// Image 是 docker 模式的容器镜像引用（如 itzg/minecraft-server:latest），仅 process_type=docker 使用（FR-078，ADR-019）。
	Image string `gorm:"type:varchar(256)" json:"image"`
	// ContainerID 记录 docker 模式实例最近一次运行的容器 ID（排障/展示用，运行态由 Worker 持有）。
	ContainerID string `gorm:"type:varchar(64)" json:"containerId"`
	// CPULimit 是 docker 模式的 CPU 核数上限（如 1.5），启动时注入容器 cgroup；0=不限制（FR-079，ADR-019）。
	CPULimit float64 `gorm:"default:0" json:"cpuLimit"`
	// MemLimitMB 是 docker 模式的内存上限（MiB），启动时注入容器 cgroup；0=不限制（FR-079，ADR-019）。
	MemLimitMB int64 `gorm:"default:0" json:"memLimitMb"`
	// DiskLimitMB 是 docker 模式的磁盘上限（MiB），仅持久化与展示，v1 不注入（依赖存储驱动）（FR-079）。
	DiskLimitMB int64 `gorm:"default:0" json:"diskLimitMb"`
	// ThrottleCPULimit / ThrottleMemLimitMB 是**运行期配额强制**（FR-467 throttle 档）登记的
	// 「待收紧限额」：docker 的 cgroup 限额只能在创建容器时注入，运行期收紧必须下次启动生效，
	// 故此处必须持久化该意图——否则「已登记限流」只是一条审计记录，重启后无人读取，
	// 限流永不生效（M-1：审计记成功却没有任何持久化意图）。
	//
	// 语义：0 表示无待收紧项；非 0 时启动/重建容器时与 CPULimit/MemLimitMB 取**较小值**合并
	// （收紧是单向的，永不因待收紧项而放宽运维显式配置的限额）。
	ThrottleCPULimit   float64 `gorm:"default:0" json:"throttleCpuLimit"`
	ThrottleMemLimitMB int64   `gorm:"default:0" json:"throttleMemLimitMb"`
	AutoStart          bool    `gorm:"default:false" json:"autoStart"`
	AutoRestart        bool    `gorm:"default:true" json:"autoRestart"`
	// Deprecated: RCON 已退役（FR-067，见 ADR-016）——治理改走 ServerProbe 探针。
	// 列保留仅为迁移安全（不破坏既有库与历史实例数据），新实例不再写入、读取方不再使用。
	RCONPort     int    `gorm:"default:0" json:"rconPort"`
	RCONPassword string `gorm:"type:varchar(128)" json:"-"`
	// ForwardingSecret 是 Velocity modern 转发的 forwarding secret（代理实例 provision 时生成）。
	// 下发到所注册后端 paper 配置 + 跨代理一致校验复用；BungeeCord/Waterfall 不使用。参见 FR-035。
	ForwardingSecret string `gorm:"type:varchar(128)" json:"-"`
	// ProxyOnlineMode 代理是否向 Mojang 校验正版（true=正版网络，false=离线模式群组服）。
	// 仅代理实例使用；持久化以便 SyncProxy 重新生成配置时保留选择（默认 true）。参见 FR-035。
	ProxyOnlineMode bool `gorm:"default:true" json:"proxyOnlineMode"`
	ServerPort      int  `gorm:"default:0" json:"serverPort"`
	QueryPort       int  `gorm:"default:0" json:"queryPort"`
	// ProbePort 是 ServerProbe 监控探针 /metrics 端口（系统分配，FR-010）。0 表示未部署探针。
	ProbePort int `gorm:"default:0" json:"probePort"`
	// ProbeVersionID 是实例显式选择的 ServerProbe 版本；0 表示继承所属 Worker 或全局默认（FR-409）。
	ProbeVersionID uint `gorm:"default:0;index" json:"probeVersionId"`
	PID            int  `gorm:"default:0" json:"pid"`
	// RuntimeDriftPID 是「实例工作目录下存在活进程、但平台未认作运行」时该外来进程的 PID（FR-471）。
	// 由心跳携带 Worker 观测结果写入；0 表示无漂移（已对齐或已被接管）。仅观测不自动处置。
	// 显式固定列名 runtime_drift_pid：gorm 默认命名会把 PID 转成 p_id（见既有 PID 字段），
	// 而本列仅由本 FR 的 SQL/前端/文档按 runtime_drift_pid 引用，值得钉死以免歧义。
	RuntimeDriftPID int64 `gorm:"column:runtime_drift_pid;default:0" json:"runtimeDriftPid"`
	// RuntimeDriftCmdline 上述漂移进程的命令行摘要（落库前已截断至 512 字节，与列宽对齐）。
	RuntimeDriftCmdline string `gorm:"type:varchar(512)" json:"runtimeDriftCmdline"`
	// RuntimeDriftAt 最近一次观测到漂移的时刻；漂移消失时置 NULL（json 省略）。
	RuntimeDriftAt *time.Time     `json:"runtimeDriftAt,omitempty"`
	StartedAt      *time.Time     `json:"startedAt"`
	CrashCount     int            `gorm:"default:0" json:"crashCount"`
	Tags           string         `gorm:"type:text" json:"tags"` // JSON
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`

	Node Node `gorm:"foreignKey:NodeID" json:"node,omitempty"`

	// Capabilities 实例能力画像（FR-445，ADR-091）：按当前 (type, role) 现算，不落库、不缓存。
	// gorm:"-" 使其不参与持久化；仅详情/单查路径填充（列表不计），故对既有响应零影响。
	Capabilities *InstanceCapabilityProfile `gorm:"-" json:"capabilities,omitempty"`
}

// BeforeCreate 创建前自动生成 UUID。
func (i *Instance) BeforeCreate(tx *gorm.DB) error {
	if i.UUID == "" {
		i.UUID = uuid.New().String()
	}
	return nil
}

// GroupInstance 实例与用户组的关联。
type GroupInstance struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	GroupID    uint      `gorm:"not null;index" json:"groupId"`
	InstanceID uint      `gorm:"uniqueIndex;not null" json:"instanceId"`
	CreatedAt  time.Time `json:"createdAt"`
}
