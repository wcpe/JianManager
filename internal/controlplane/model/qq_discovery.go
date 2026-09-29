package model

import "time"

// QQ 机器人扫码接入与已发现群列表（FR-495，增强 FR-494）。
//
// 状态说明：
//   - 已发现群（QQDiscoveredGroup）：网关收到 GROUP_ADD_ROBOT 事件后落库，
//     运维建通道时从下拉选择（targetType=group 的 targetId 抄自此表）；
//     通道存的是 openid 字符串副本，群条目删除不级联（与通道表无硬外键）。
//   - 网关连接（QQGatewayConnection）：按 appId 建模的单连接状态，
//     预留多连接能力，本期前端只展示「默认机器人」的连接。

// QQDiscoveredGroup QQ 机器人已进入的群（FR-495）。
// 去重键为 GroupOpenID（唯一约束 + upsert）：入群事件是状态型而非消息型，
// 无需 msg_seq 去重；重复进群只刷新 LastSeenAt，不产生重复条目。
type QQDiscoveredGroup struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// GroupOpenID 群 openid（唯一），通道配置 targetId 抄此值。
	GroupOpenID string `gorm:"type:varchar(128);uniqueIndex:uq_qq_groups_openid;not null" json:"groupOpenid"`
	// OpMemberOpenID 把机器人拉进群的操作人 openid。
	OpMemberOpenID string `gorm:"type:varchar(128)" json:"opMemberOpenid"`
	// FirstSeenAt 首次发现时间。
	FirstSeenAt time.Time `json:"firstSeenAt"`
	// LastSeenAt 最近一次入群事件时间（重复进群刷新）。
	LastSeenAt time.Time `json:"lastSeenAt"`
	// SourceAppID 发现该群的机器人 appId。
	SourceAppID string `gorm:"type:varchar(64);index" json:"sourceAppId"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// QQ 网关连接状态（FR-495）。
const (
	// QQGatewayConnected 网关已连接（收到 READY / RESUMED）。
	QQGatewayConnected = "connected"
	// QQGatewayConnecting 网关正在连接（含重连退避等待中）。
	QQGatewayConnecting = "connecting"
	// QQGatewayDisconnected 网关未连接（初始态 / 主动停止 / 可恢复断线）。
	QQGatewayDisconnected = "disconnected"
	// QQGatewayError 网关进入人工介入态（4014 intent 无权限 / 4914 下架 / 4915 封禁等），
	// 重连无意义，需运维处理后重启服务。
	QQGatewayError = "error"
)

// QQGatewayConnection QQ 网关连接的持久化状态（FR-495，按 appId 唯一）。
type QQGatewayConnection struct {
	ID uint `gorm:"primaryKey" json:"-"`
	// AppID 机器人 appId（唯一）。
	AppID string `gorm:"type:varchar(64);uniqueIndex:uq_qq_gateway_appid;not null" json:"appId"`
	// Status 连接状态：connected | connecting | disconnected | error。
	Status string `gorm:"type:varchar(16);not null;default:disconnected" json:"status"`
	// LastEventAt 最近一次收到网关事件的时间。
	LastEventAt *time.Time `json:"lastEventAt"`
	// LastError 最近一次错误（人类可读，4014 时明确指向 intent 权限）。
	LastError string `gorm:"type:varchar(512)" json:"lastError"`
	// SessionID 网关会话 ID（READY 后保存，Resume 时使用）。
	SessionID string `gorm:"type:varchar(128)" json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
