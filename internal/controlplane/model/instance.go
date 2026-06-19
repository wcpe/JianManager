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
)

// InstanceType 实例类型。
type InstanceType string

const (
	InstanceTypeMinecraftJava InstanceType = "minecraft_java"
	InstanceTypeGeneric       InstanceType = "generic"
)

// InstanceRole 实例在 MC 群组服拓扑中的角色（ADR-007）。
type InstanceRole string

const (
	// InstanceRoleBackend 后端子服（Paper/Spigot/Purpur），可注册进多个代理。
	InstanceRoleBackend InstanceRole = "backend"
	// InstanceRoleProxy 代理（BungeeCord/Waterfall/Velocity），聚合多个后端。
	InstanceRoleProxy InstanceRole = "proxy"
	// InstanceRoleUniversal 通用实例（默认；非群组服角色，保留自由命令）。
	InstanceRoleUniversal InstanceRole = "universal"
)

// ValidInstanceRole 校验角色是否在允许枚举内。
func ValidInstanceRole(r InstanceRole) bool {
	switch r {
	case InstanceRoleBackend, InstanceRoleProxy, InstanceRoleUniversal:
		return true
	}
	return false
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
	ID            uint           `gorm:"primaryKey" json:"id"`
	UUID          string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	NodeID        uint           `gorm:"not null;index" json:"nodeId"`
	Name          string         `gorm:"type:varchar(128);not null" json:"name"`
	Type          InstanceType   `gorm:"type:varchar(64);not null" json:"type"`
	Role          InstanceRole   `gorm:"type:varchar(16);default:universal;index" json:"role"`
	ProcessType   ProcessType    `gorm:"type:varchar(32);not null" json:"processType"`
	Status        InstanceStatus `gorm:"type:varchar(32);default:STOPPED" json:"status"`
	StartCommand      string         `gorm:"type:varchar(1024);not null" json:"startCommand"`
	JDKID             uint           `gorm:"index" json:"jdkId"`
	JavaMajorVersion  int            `gorm:"index" json:"javaMajorVersion"`
	LaunchSpec        string         `gorm:"type:text" json:"launchSpec"`
	WorkDir           string         `gorm:"type:varchar(512)" json:"workDir"`
	EnvVars           string         `gorm:"type:text" json:"envVars"` // JSON
	AutoStart     bool           `gorm:"default:false" json:"autoStart"`
	AutoRestart   bool           `gorm:"default:true" json:"autoRestart"`
	RCONPort      int            `gorm:"default:0" json:"rconPort"`
	RCONPassword  string         `gorm:"type:varchar(128)" json:"-"`
	// ForwardingSecret 是 Velocity modern 转发的 forwarding secret（代理实例 provision 时生成）。
	// 下发到所注册后端 paper 配置 + 跨代理一致校验复用；BungeeCord/Waterfall 不使用。参见 FR-035。
	ForwardingSecret string `gorm:"type:varchar(128)" json:"-"`
	ServerPort    int            `gorm:"default:0" json:"serverPort"`
	QueryPort     int            `gorm:"default:0" json:"queryPort"`
	PID           int            `gorm:"default:0" json:"pid"`
	StartedAt     *time.Time     `json:"startedAt"`
	CrashCount    int            `gorm:"default:0" json:"crashCount"`
	Tags          string         `gorm:"type:text" json:"tags"` // JSON
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`

	Node Node `gorm:"foreignKey:NodeID" json:"node,omitempty"`
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
	ID         uint `gorm:"primaryKey" json:"id"`
	GroupID    uint `gorm:"not null;index" json:"groupId"`
	InstanceID uint `gorm:"uniqueIndex;not null" json:"instanceId"`
	CreatedAt  time.Time `json:"createdAt"`
}
