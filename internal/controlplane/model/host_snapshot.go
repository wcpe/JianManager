package model

import "time"

// HostMountSnapshot 是某节点某挂载点的最新磁盘用量。
//
// ## 为什么单独成表而不是给 nodes 加列
//
// 挂载点是**多值维度**：一台机器可能有 /、/var、/data 若干条，而 nodes 表是宽表，
// 加列就得预设「最多几个挂载点」——那是个会在生产里立刻被打破的假设。
//
// ## 为什么是快照而不是时序
//
// 与 ProcessMetricSnapshot 同款取舍：悬浮卡要的是「当前哪个盘快满了」，
// 一个节点 × 挂载点只保留最新一条，按 sampled_at 清理即可（TTL 与 raw 指标一致 48h）。
// 历史趋势若将来要做，走 MetricSampleRaw 的 metric_key 路径（见 spec §3.3 待定 4）。
type HostMountSnapshot struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// NodeUUID 节点身份。用 UUID 而非自增 id：Worker 上报的就是 UUID，
	// 且节点删除重建后 UUID 会变（names 可复用），用 UUID 能自然隔离新旧节点。
	NodeUUID string `gorm:"type:char(36);not null;uniqueIndex:ux_host_mount_node_mp,priority:1" json:"nodeUuid"`
	// Mountpoint 挂载点路径（如 /、/var）。
	Mountpoint string `gorm:"type:varchar(255);not null;uniqueIndex:ux_host_mount_node_mp,priority:2" json:"mountpoint"`
	TotalBytes uint64  `json:"totalBytes"`
	UsedBytes  uint64  `json:"usedBytes"`
	// UsedPct 使用率（0..100）。
	UsedPct float32 `json:"usedPct"`
	// SampledAt 采集时刻，用于判鲜度与 TTL 清理。
	SampledAt time.Time `gorm:"index" json:"sampledAt"`
}

// TableName 固定表名。
func (HostMountSnapshot) TableName() string { return "host_mount_snapshots" }

// HostProcessSnapshot 是某节点整机 top-N 进程的最新快照。
//
// 与 HostMountSnapshot 同理：多值维度、只保留最新一批。
//
// **隐私边界**：本表**不存 cmdline**（连列都没有）。只存 comm 与指标——
// cmdline 常含密码/token/内网地址，而「谁在吃 CPU」这个判断只需要进程名。
// 若将来要加 cmdline，必须先过隐私评审，不能顺手加一列。
type HostProcessSnapshot struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	NodeUUID string `gorm:"type:char(36);not null;uniqueIndex:ux_host_proc_node_pid,priority:1" json:"nodeUuid"`
	// PID 进程号。与 NodeUUID 组成唯一键：同一节点的同一 PID 只保留最新一条。
	PID uint32 `gorm:"not null;uniqueIndex:ux_host_proc_node_pid,priority:2" json:"pid"`
	// Comm 进程名（不含参数）。长度上限与 /proc 的 comm 一致（内核限 16 字节，
	// 留余量到 64 以兼容不同实现）。
	Comm string `gorm:"type:varchar(64)" json:"comm"`
	// CPUPct CPU 占用百分比（0..100，单核口径）。
	CPUPct float32 `json:"cpuPct"`
	// RSSBytes 常驻内存字节。
	RSSBytes  uint64    `json:"rssBytes"`
	SampledAt time.Time `gorm:"index" json:"sampledAt"`
}

// TableName 固定表名。
func (HostProcessSnapshot) TableName() string { return "host_process_snapshots" }
