package model

import "time"

// ClientMachine 客户端机器码登记（FR-092，见 ADR-023）。
//
// 机器码由客户端生成、**不可信**——仅作统计与**辅助**限流维度，**不作信任/授权依据**（限流以 IP 为主，FR-096）。
// 每频道每机器码一行，记首/末见与命中计数；按机器码维度的统计看板归 FR-095、全链路追踪/留存归 FR-093。
type ClientMachine struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// ChannelID 所属频道 slug。与 MachineID 组成唯一键。
	ChannelID string `gorm:"column:channel_id;type:varchar(64);not null;uniqueIndex:idx_client_machines_channel_machine" json:"channelId"`
	// MachineID 客户端机器码（SHA-256 十六进制，不可逆）。库内仅存哈希。
	MachineID string `gorm:"column:machine_id;type:varchar(128);not null;uniqueIndex:idx_client_machines_channel_machine" json:"machineId"`
	// HitCount 该机器码在本频道的累计拉取命中次数（弱一致统计）。
	HitCount int64 `gorm:"default:0;not null" json:"hitCount"`
	// FirstSeen 首次登记时间。
	FirstSeen time.Time `json:"firstSeen"`
	// LastSeen 最近一次登记时间。
	LastSeen time.Time `json:"lastSeen"`
}
