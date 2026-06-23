package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 告警级别（FR-085）。按严重度递增，用于路由与展示着色。
const (
	AlertLevelInfo     = "info"
	AlertLevelWarn     = "warn"
	AlertLevelCritical = "critical"
)

// 告警触发类型（FR-085）。在 FR-011 的指标阈值之外扩展为多触发源。
const (
	// AlertTriggerMetric 指标阈值（节点 cpu/memory/disk，FR-011 原型）。
	AlertTriggerMetric = "metric"
	// AlertTriggerInstanceCrash 实例崩溃（state_change → CRASHED）。
	AlertTriggerInstanceCrash = "instance_crash"
	// AlertTriggerNodeOffline 节点离线（心跳超时被标记 offline）。
	AlertTriggerNodeOffline = "node_offline"
	// AlertTriggerLogKeyword 日志关键字（实例 stdout/stderr 命中关键字）。
	AlertTriggerLogKeyword = "log_keyword"
	// AlertTriggerPlayerEvent 玩家事件（接 FR-066 PlayerEventService）。
	AlertTriggerPlayerEvent = "player_event"
	// AlertTriggerBackupFailed 备份失败。
	AlertTriggerBackupFailed = "backup_failed"
)

// 通知通道类型（FR-085）。在通用 webhook 之外扩展主流 IM / 邮件 / 站内。
const (
	ChannelTypeWebhook  = "webhook"
	ChannelTypeEmail    = "email"
	ChannelTypeDingtalk = "dingtalk"
	ChannelTypeWecom    = "wecom"
	ChannelTypeFeishu   = "feishu"
	ChannelTypeDiscord  = "discord"
	ChannelTypeTelegram = "telegram"
	ChannelTypeInApp    = "inapp"
)

// AlertChannel 通知通道（FR-085）。一个通道是一个可复用的通知出口，
// 多条规则可路由到同一通道。敏感凭证字段（webhook 地址含 secret、SMTP 密码、bot token）
// 必须以 ${ENV_VAR} 形式引用环境变量，不得硬编码明文（见 .claude/rules/config-files.md）。
type AlertChannel struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	UUID      string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	Name      string         `gorm:"type:varchar(128);not null" json:"name"`
	Type      string         `gorm:"type:varchar(32);not null" json:"type"`
	Enabled   bool           `gorm:"default:true" json:"enabled"`
	// Config 通道连接配置的 JSON 串。各类型字段不同（见 service/channel_notifier.go ChannelConfig）：
	//   webhook/dingtalk/wecom/feishu/discord: {"url":"${ENV}"}（URL 含 access_token/secret，强制 ${ENV} 引用）
	//   telegram: {"token":"${ENV}","chatId":"..."}
	//   email:    {"host","port","username","password":"${ENV}","from","to"}
	//   inapp:    {}（无外部配置）
	// 凭证子字段（URL/token/password）强制经 ${ENV_VAR} 引用，CreateChannel/UpdateChannel 时校验、发送时解析。
	Config    string         `gorm:"type:text" json:"config"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (c *AlertChannel) BeforeCreate(tx *gorm.DB) error {
	if c.UUID == "" {
		c.UUID = uuid.New().String()
	}
	return nil
}

// AlertRule 告警规则（FR-011 + FR-085 扩展）。
type AlertRule struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UUID       string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	Name       string `gorm:"type:varchar(128);not null" json:"name"`
	// TriggerType 触发类型（FR-085）：metric|instance_crash|node_offline|log_keyword|player_event|backup_failed。
	// 为兼容 FR-011 存量规则，空值按 metric 处理。
	TriggerType string `gorm:"type:varchar(32);default:metric" json:"triggerType"`
	// Level 告警级别（FR-085）：info|warn|critical。空值按 warn 处理。
	Level      string `gorm:"type:varchar(16);default:warn" json:"level"`
	TargetType string `gorm:"type:varchar(32);not null" json:"targetType"` // node, instance
	TargetID   *uint  `json:"targetId"`                                    // nil 表示全局

	// ── metric 触发专用（FR-011）──
	Metric      string  `gorm:"type:varchar(64)" json:"metric"`   // cpu, memory, disk
	Operator    string  `gorm:"type:varchar(4)" json:"operator"`  // >, <, >=, <=, ==
	Threshold   float64 `json:"threshold"`
	DurationSec int     `gorm:"default:0" json:"durationSec"`

	// ── 非指标触发专用（FR-085）──
	// Keyword 日志关键字触发的匹配子串（log_keyword）。
	Keyword string `gorm:"type:varchar(256)" json:"keyword"`
	// EventMatch 玩家事件触发的子类型（player_event）：join|quit|chat|cross_server，空=任意。
	EventMatch string `gorm:"type:varchar(64)" json:"eventMatch"`

	// ── 聚合 / 静默 / 路由（FR-085）──
	// ChannelIDs 路由目标通道 ID 列表的 JSON 串（如 "[1,2]"）。空=不发外部通知（仍入事件库 + 站内）。
	ChannelIDs string `gorm:"type:varchar(256)" json:"channelIds"`
	// DedupWindowSec 去抖聚合窗口（秒）。同一告警键在窗口内重复触发只通知一次、累计计数。0=不去抖。
	DedupWindowSec int `gorm:"default:0" json:"dedupWindowSec"`
	// SilenceStart/SilenceEnd 静默窗口（"HH:MM" 24h，本地时区）。落在窗口内触发的告警不发外部通知（仍入库）。
	// 支持跨午夜（start > end，如 23:00→07:00）。两者皆空=不静默。
	SilenceStart string `gorm:"type:varchar(8)" json:"silenceStart"`
	SilenceEnd   string `gorm:"type:varchar(8)" json:"silenceEnd"`
	// NotifyRecover 恢复时是否发送恢复通知（仅对可恢复的触发类型：metric/instance_crash/node_offline）。
	NotifyRecover bool `gorm:"default:true" json:"notifyRecover"`

	// NotifyType/NotifyTarget 兼容 FR-011 的单 webhook 直发（未配置 ChannelIDs 时回退）。
	NotifyType   string `gorm:"type:varchar(32)" json:"notifyType"`
	NotifyTarget string `gorm:"type:varchar(512)" json:"notifyTarget"`

	Enabled   bool           `gorm:"default:true" json:"enabled"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (a *AlertRule) BeforeCreate(tx *gorm.DB) error {
	if a.UUID == "" {
		a.UUID = uuid.New().String()
	}
	return nil
}

// AlertEvent 告警事件（FR-011 + FR-085 扩展）。
type AlertEvent struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	RuleID   uint   `gorm:"not null;index" json:"ruleId"`
	TargetID uint   `json:"targetId"`
	// Level/TriggerType 冗余快照规则当时的级别与类型，便于历史筛选（规则可能被改/删）。
	Level       string `gorm:"type:varchar(16);default:warn" json:"level"`
	TriggerType string `gorm:"type:varchar(32);default:metric" json:"triggerType"`
	// DedupKey 去抖键（rule + target + 触发标识）。聚合窗口内复发更新同一活跃事件而非新建。
	DedupKey string  `gorm:"type:varchar(256);index" json:"-"`
	Value    float64 `json:"value"`
	Message  string  `gorm:"type:varchar(512)" json:"message"`
	// Count 聚合计数：去抖窗口内该告警被触发的次数（≥1）。
	Count    int        `gorm:"default:1" json:"count"`
	Resolved bool       `gorm:"default:false" json:"resolved"`
	FiredAt  time.Time  `json:"firedAt"`
	// LastFiredAt 最近一次复发时间（聚合时更新）。
	LastFiredAt *time.Time `json:"lastFiredAt"`
	ResolvedAt  *time.Time `json:"resolvedAt"`

	// ── 确认 / 已读（FR-085）──
	Acknowledged   bool       `gorm:"default:false" json:"acknowledged"`
	AcknowledgedBy *uint      `json:"acknowledgedBy"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt"`
	// Read 站内已读状态（仅影响未读角标，不影响确认）。
	Read bool `gorm:"default:false" json:"read"`

	Rule AlertRule `gorm:"foreignKey:RuleID" json:"rule,omitempty"`
}
