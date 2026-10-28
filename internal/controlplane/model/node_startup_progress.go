package model

import "time"

// NodeStartupProgress 是 Worker 启动阶段的**临时**进度记录（FR-486 范围②）。
//
// ## 为什么单独一张表，而不是给 Node 加列
//
// 时间顺序上不允许：启动进度产生于 Worker **注册之前**，那时 CP 还没有这个节点的正式记录
//（首次启动时连 UUID 都没有）。若塞进 nodes 表，就得先伪造一条节点记录，
// 结果是在节点列表里凭空出现「已存在但从未上线」的节点——比看不到更误导。
//
// ## 生命周期
//
// 一行 = 一个正在启动的节点的当前状态（**不是历史**，同一键持续覆盖）：
//   - 首次上报时创建；
//   - 注册成功后由 MarkStartupProgressDone 删除（节点已可服务，进度不再有意义）；
//   - 若 Worker 加载中途崩掉，行会残留——由 UpdatedAt 判陈旧（见 IsStale），
//     面板据此显示「启动中断」而不是永远「启动中」。
//
// ## Key 的两种形态
//
//   - 重启场景（Worker 已有身份）：Key = node_uuid，可与正式节点稳定对应；
//   - 首装场景（CP 尚未签发 UUID）：Key = "name:<节点名>" 占位；
//     注册成功后 RekeyStartupProgress 把它改写成真实 UUID，避免同表出现
//     「按名字的旧行」与「真节点」两条并存。
type NodeStartupProgress struct {
	// Key 主键：node_uuid 或 "name:<name>"（见类型注释）。
	Key string `gorm:"primaryKey;type:varchar(160)" json:"key"`
	// NodeUUID 已注册节点的 UUID；首装尚未注册时为空。
	NodeUUID string `gorm:"type:char(36);index" json:"nodeUuid,omitempty"`
	// NodeName Worker 自称的节点名（用于「谁在启动」的展示）。
	NodeName string `gorm:"type:varchar(128)" json:"nodeName,omitempty"`
	// Phase 当前阶段标识，取值与 Worker 侧 startup.Phase 对齐
	//（config / dataroot / identity / process_recover / ingest_state_load / register / heartbeat）。
	Phase string `gorm:"type:varchar(64);not null" json:"phase"`
	// PhaseElapsedMs 该阶段已耗时（0 = 阶段刚开始、尚未结束）。
	PhaseElapsedMs int64 `json:"phaseElapsedMs"`
	// TotalElapsedMs 自进程启动起的总耗时。
	TotalElapsedMs int64 `json:"totalElapsedMs"`
	// PhasesDone / PhasesTotal 供面板渲染进度（如 5/7）。
	PhasesDone  int32 `json:"phasesDone"`
	PhasesTotal int32 `json:"phasesTotal"`
	// UpdatedAt 最近一次上报时刻，用于判陈旧。
	UpdatedAt time.Time `gorm:"index" json:"updatedAt"`
}

// TableName 固定表名，避免 GORM 复数化规则变化导致迁移漂移。
func (NodeStartupProgress) TableName() string { return "node_startup_progresses" }

// startupProgressStaleAfter 超过该时长未更新即判定「启动中断」。
//
// 取值依据：Worker 每完成一个阶段上报一次，而最慢的阶段（大索引加载）实测可达数秒到数十秒；
// 正常启动不会出现数分钟静默。5 分钟既能容忍极端慢盘，又不至于让「崩掉的启动」
// 在面板上长时间伪装成「正在启动」。
const startupProgressStaleAfter = 5 * time.Minute

// IsStale 报告该进度是否已陈旧（Worker 可能已崩溃或被杀）。
//
// 为什么要它：进度行在 Worker 异常退出时不会被清理（进程没机会发最后一条）。
// 没有陈旧判定，面板会永远显示「启动中 · ingest_state_load」，
// 而这恰恰是运维最容易被骗的场景——以为它在慢慢加载，其实进程早没了。
func (p NodeStartupProgress) IsStale(now time.Time) bool {
	if p.UpdatedAt.IsZero() {
		return true
	}
	return now.Sub(p.UpdatedAt) > startupProgressStaleAfter
}
