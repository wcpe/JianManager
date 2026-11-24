package model

import (
	"fmt"
	"time"
)

// 托管探针配置的运营侧存储（下发 / 对账）。
//
// ## 与上游地基的分工
// 上游（managed_mode*.go / probe_managed_assembly.go）已经定义了**帧结构与档位表**，
// 本文件只补「运营侧缺的那部分状态」：
//   - 三层配置增量（global / node / instance）与版本；
//   - 一次下发会话（session）与逐台目标（target）的经历；
//   - 每台实例最近一次的托管状态（探针周期上报）。
//
// 帧本身不落库：`ManagedConfigPush` / `ManagedConfigApplied` / `ManagedConfigState`
// 是 service 包的协议类型，模型只保存搬运后的字段（避免 model 反向依赖 service）。

// ManagedConfigScope 是托管配置的三层作用域（低 → 高优先级）。
//
// 与既有 ConfigScopeLevel（group / instance）刻意分开：那套是「任意配置文件的分层覆盖」，
// 这里的对象是**探针的 config.yml**，层级固定为三层且 global 只有一行。
// 混用会让「group 层」与「node 层」的语义在同一个枚举里打架。
type ManagedConfigScope string

const (
	// ManagedScopeGlobal 全局层：作用于所有托管探针，scope_ref_id 恒为 0（只有一行）。
	ManagedScopeGlobal ManagedConfigScope = "global"
	// ManagedScopeNode 节点层：scope_ref_id = nodes.id。
	ManagedScopeNode ManagedConfigScope = "node"
	// ManagedScopeInstance 实例层：scope_ref_id = instances.id（优先级最高）。
	ManagedScopeInstance ManagedConfigScope = "instance"
)

// ValidManagedConfigScope 校验作用域枚举。
func ValidManagedConfigScope(s ManagedConfigScope) bool {
	switch s {
	case ManagedScopeGlobal, ManagedScopeNode, ManagedScopeInstance:
		return true
	}
	return false
}

// ManagedScopePriority 返回作用域的合并优先级（越大越靠后、越优先）。
//
// 合并顺序为 global(0) → node(1) → instance(2)，本函数是这张顺序表的**唯一真源**，
// 服务层的排序与前端展示都取它，避免两处各写一份 if。
func ManagedScopePriority(s ManagedConfigScope) int {
	switch s {
	case ManagedScopeGlobal:
		return 0
	case ManagedScopeNode:
		return 1
	case ManagedScopeInstance:
		return 2
	}
	return -1
}

// 托管配置层与下发会话共用的状态枚举。

// ManagedPushSessionStatus 是一次下发会话的状态。
//
// `awaiting_release` 是「本批已全部终结、等人工放行下一批」的显式状态：
// 若用 running 表示，界面就分不出「正在推」和「等人点继续」，运维会一直等一个不会动的进度条。
type ManagedPushSessionStatus string

const (
	// ManagedPushRunning 当前批正在推送/等待回执。
	ManagedPushRunning ManagedPushSessionStatus = "running"
	// ManagedPushAwaitingRelease 当前批已全部终结，等人工放行下一批。
	ManagedPushAwaitingRelease ManagedPushSessionStatus = "awaiting_release"
	// ManagedPushPaused 已暂停（人工暂停，或进程重启后由启动收口置入）。**可 Resume**。
	ManagedPushPaused ManagedPushSessionStatus = "paused"
	// ManagedPushCompleted 全部批次终结且未触发熔断。
	ManagedPushCompleted ManagedPushSessionStatus = "completed"
	// ManagedPushFailed 因熔断结束（保留现场，不再推进）。**终态，不可 Resume**。
	//
	// 与 paused 的关键差别不是「能不能再推进」，而是「该不该让它一键续推」：
	// 熔断说明按当前配置继续推是危险的，续推应改动配置后重建会话。
	ManagedPushFailed ManagedPushSessionStatus = "failed"
	// ManagedPushCancelled 人工取消。
	ManagedPushCancelled ManagedPushSessionStatus = "cancelled"
)

// IsTerminal 报告会话是否已终结。
func (s ManagedPushSessionStatus) IsTerminal() bool {
	switch s {
	case ManagedPushCompleted, ManagedPushFailed, ManagedPushCancelled:
		return true
	}
	return false
}

// ManagedPushTargetState 是单台实例在一次会话中的状态。
//
// 与上游 `result` 三值的关系：`applied` / `failed` / `rejected` 直接来自探针回报；
// `timeout` / `pending` / `pushing` / `skipped` 是 CP 侧自己的经历，探针永远不会报。
// 不把两者混成一个枚举上传给前端时也不合并——运维要能区分「探针说失败」与「根本没回执」。
type ManagedPushTargetState string

const (
	// ManagedPushTargetPending 已排入本批但尚未下发。
	ManagedPushTargetPending ManagedPushTargetState = "pending"
	// ManagedPushTargetPushing 已下发、等 config_applied 回执。
	ManagedPushTargetPushing ManagedPushTargetState = "pushing"
	// ManagedPushTargetApplied 探针已应用（config_applied result=applied）。
	ManagedPushTargetApplied ManagedPushTargetState = "applied"
	// ManagedPushTargetFailed 探针应用失败（result=failed）。
	ManagedPushTargetFailed ManagedPushTargetState = "failed"
	// ManagedPushTargetRejected 探针整包拒绝（result=rejected，含版本冲突）。
	ManagedPushTargetRejected ManagedPushTargetState = "rejected"
	// ManagedPushTargetTimeout 已下发但超时未收到回执（CP 侧判定，不是探针结论）。
	ManagedPushTargetTimeout ManagedPushTargetState = "timeout"
	// ManagedPushTargetSkipped 未下发即跳过（离线/探针不适用）。
	ManagedPushTargetSkipped ManagedPushTargetState = "skipped"
)

// IsTerminal 报告目标是否已终结（含 skipped）。
func (s ManagedPushTargetState) IsTerminal() bool {
	switch s {
	case ManagedPushTargetApplied, ManagedPushTargetFailed, ManagedPushTargetRejected,
		ManagedPushTargetTimeout, ManagedPushTargetSkipped:
		return true
	}
	return false
}

// IsFailure 报告目标是否属于失败类（**不含** skipped）。
//
// 熔断分子的唯一判据：离线跳过的机器不该被算成「配置推坏了」。
func (s ManagedPushTargetState) IsFailure() bool {
	switch s {
	case ManagedPushTargetFailed, ManagedPushTargetRejected, ManagedPushTargetTimeout:
		return true
	}
	return false
}

// 目标失败原因码（机器可读，供前端做定向文案）。
const (
	// ManagedTargetCodeVersionConflict 探针拒绝：基线版本不符（rejected + error 前缀 conflict）。
	//
	// 【为什么单独一个码】`conflict` **不是** result 取值，它是 rejected 的 error 前缀。
	// 把它当协议错误处理会让运维去查「探针是不是不认这个字段」，而实际只是基线错了。
	ManagedTargetCodeVersionConflict = "VERSION_CONFLICT"
	// ManagedTargetCodeApplyFailed 探针应用失败（result=failed）。
	ManagedTargetCodeApplyFailed = "APPLY_FAILED"
	// ManagedTargetCodeRejected 探针整包拒绝（非版本冲突）。
	ManagedTargetCodeRejected = "REJECTED"
	// ManagedTargetCodeAckTimeout 已下发但超时未回执。
	ManagedTargetCodeAckTimeout = "ACK_TIMEOUT"
	// ManagedTargetCodeProbeUnreachable 节点未连接或探针未连接，指令根本没送到。
	ManagedTargetCodeProbeUnreachable = "PROBE_UNREACHABLE"
	// ManagedTargetCodeInstanceOffline 实例未运行，跳过。
	ManagedTargetCodeInstanceOffline = "INSTANCE_OFFLINE"
	// ManagedTargetCodeProbeNotApplicable 实例不适用探针（代理/Beacon/二进制），跳过。
	ManagedTargetCodeProbeNotApplicable = "PROBE_NOT_APPLICABLE"
	// ManagedTargetCodeNothingToPush 该实例合并后无可下发内容，创建时即跳过。
	ManagedTargetCodeNothingToPush = "NOTHING_TO_PUSH"
	// ManagedTargetCodeCancelledByUser 会话被人工取消，未下发目标一并跳过。
	ManagedTargetCodeCancelledByUser = "CANCELLED_BY_USER"
)

// 会话暂停原因分类。
const (
	// ManagedPushPauseManual 人工暂停。
	ManagedPushPauseManual = "manual"
	// ManagedPushPauseCircuitBreak 失败率熔断。
	//
	// 注意：熔断写的是 `failed`（终态），不是 `paused`——它表达「按当前配置继续推是危险的」，
	// 允许 Resume 一键续推会绕过那次判断。本常量只用于记录暂停现场的分类。
	ManagedPushPauseCircuitBreak = "circuit_break"
	// ManagedPushPauseRestart 进程重启导致推进中断（启动时由 RecoverInterrupted 写入）。
	//
	// 归到暂停分类而非另立状态：处置动作与人工暂停相同（看一眼现场 → 决定续推或取消），
	// 只是原因不同，而原因由本字段表达。
	ManagedPushPauseRestart = "restart"
)

// 会话类型：下发 / 回滚。
const (
	// ManagedPushKindPush 常规下发。
	ManagedPushKindPush = "push"
	// ManagedPushKindRollback 回滚（重推上一次 completed 会话的冻结快照）。
	ManagedPushKindRollback = "rollback"
)

// 对账矩阵的一致性判定取值。
const (
	// ManagedConsistencyInSync 观测版本与期望版本一致。
	ManagedConsistencyInSync = "inSync"
	// ManagedConsistencyDrift 观测版本与期望版本不一致。
	ManagedConsistencyDrift = "drift"
	// ManagedConsistencyNeverReported 从未收到 config_state（无观测）。
	ManagedConsistencyNeverReported = "neverReported"
	// ManagedConsistencyFailed 最近一次下发目标终结为失败类（含版本冲突 / 未回执）。
	ManagedConsistencyFailed = "failed"
)

// 合成后的连接状态取值（与探针自报的四态同名，但语义是**合成值**）。
const (
	ManagedConnectionConnected    = "connected"
	ManagedConnectionDegraded     = "degraded"
	ManagedConnectionUnauthorized = "unauthorized"
	ManagedConnectionDisconnected = "disconnected"
)

// ManagedBridgeConnectionOnline 是探针上行的 `online` 字面量。
//
// ## 为什么需要它：两套词表，语义相同
// 探针侧的线上取值来自 `BridgeConnectionState.wireName`：`online` / `degraded` /
// `unauthorized` / `disconnected`。而 CP 合成值的词表用的是 `connected` / `degraded` /
// `unauthorized` / `disconnected`——只有"在线"这一项拼写不同。
//
// 早期合成逻辑只认 `connected`，于是探针报 `online` 落进 fallback 分支被记成 `connected`：
// **在"在线"这一项上侥幸正确**，所以真机联调没暴露。但 fallback 同时兜住了
// `degraded` / `unauthorized` 之外的任何未知值——探针一旦真报这两态，它们恰好能命中白名单；
// 而如果探针新增第五态（如 `maintenance`），也会被静默记成 `connected`，
// 那等于把"不确定"说成"在线"。故这里显式收口：`online` 归一为 `connected`，
// 未知取值按 §"桥连着、自报未知"记为 connected，但保留可辨识的入口。
const ManagedBridgeConnectionOnline = "online"

// ManagedConfigLayer 是一层托管配置的**增量**（不是合并后的全量）。
//
// 增量存储的三个直接好处：
//  1. 节点层只写自己关心的几个键，不会被全局层的后续变更覆盖成快照；
//  2. 逐键来源能精确回答「这个值是谁定的」；
//  3. 显式 null 删键天然可表达（删掉低层设的键），不必额外存一份删除清单。
//
// `(scope, scope_ref_id)` 唯一：一层只有一行，改动走版本自增（version 单调）。
type ManagedConfigLayer struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Scope 作用域（global / node / instance）。
	Scope ManagedConfigScope `gorm:"type:varchar(16);not null;uniqueIndex:idx_managed_layer_scope" json:"scope"`
	// ScopeRefID 作用域引用：global=0、node=nodes.id、instance=instances.id。
	ScopeRefID uint `gorm:"not null;uniqueIndex:idx_managed_layer_scope" json:"scopeRefId"`
	// ScopeName 作用域可读名（节点名/实例名；global 为空）。冗余存一份，列表不必逐行 JOIN。
	ScopeName string `gorm:"type:varchar(255)" json:"scopeName,omitempty"`
	// Config 增量配置 JSON（嵌套对象；显式 null 表示删除该键）。
	Config string `gorm:"type:longtext;not null" json:"config"`
	// Version 该层的写入代数（每次保存 +1，供界面与审计对账）。
	Version int `gorm:"not null;default:1" json:"version"`
	// Identity 层身份令牌：创建时随机生成，删除重建不复用（自增主键会被 SQLite
	// 复用，单靠 ID 兜不住「删层重建后版本撞回 v1」），与版本一起做乐观并发校验。
	Identity string `gorm:"type:varchar(64);not null;default:''" json:"identity"`
	// Remark 本次保存的备注（可选，进审计）。
	Remark    string    `gorm:"type:varchar(255)" json:"remark,omitempty"`
	CreatedBy uint      `gorm:"index" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName 指定表名。
func (ManagedConfigLayer) TableName() string { return "managed_config_layers" }

// ManagedPushSession 是一次下发会话（一次「把某套托管配置推到一批实例」的完整经历）。
//
// ## 为什么要把合并结果冻结在会话上
// 回滚的定义是「重推上一次 completed 推送的**冻结快照**」，不是「按现在的层重新合并一遍」。
// 若回滚时重新合并，中间任何人改了层都会让「回滚」推出一套从未存在过的配置。
// 所以 ValuesJSON / RemovalsJSON 在建会话时就落库，之后永不重算。
type ManagedPushSession struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Kind 会话类型（push / rollback）。
	Kind string `gorm:"type:varchar(16);not null;default:push" json:"kind"`
	// Scope/ScopeRefID 本次下发针对哪一层（global/node/instance）。
	Scope      ManagedConfigScope `gorm:"type:varchar(16);not null;index" json:"scope"`
	ScopeRefID uint               `gorm:"not null" json:"scopeRefId"`
	ScopeName  string             `gorm:"type:varchar(255)" json:"scopeName,omitempty"`

	// ConfigVersion 本次下发使用的 configVersion（会话级，全局单调递增）。
	// uniqueIndex 是 MAX+1 铸造并发撞号的最后防线：两个并发会话若算出同一版本，
	// 探针的 config_applied 按版本匹配会把回执错记到另一条会话的目标上。
	ConfigVersion int64 `gorm:"not null;uniqueIndex" json:"configVersion"`
	// ValuesJSON 冻结的点分 values（{「collect-period-ticks」:100, ...}）。
	ValuesJSON string `gorm:"type:longtext;not null" json:"-"`
	// RemovalsJSON 冻结的删除路径列表。
	RemovalsJSON string `gorm:"type:longtext" json:"-"`
	// TracedJSON 冻结的逐键来源（对账矩阵与下钻用）。
	TracedJSON string `gorm:"type:longtext" json:"-"`

	Reason   string `gorm:"type:varchar(512)" json:"reason,omitempty"`
	Operator string `gorm:"type:varchar(128)" json:"operator,omitempty"`

	// BatchSize 每批台数；0 = 单批全量。
	BatchSize int `gorm:"not null;default:0" json:"batchSize"`
	// FailureRateThreshold 失败率熔断阈值（百分比，默认 20；0 = 关闭熔断）。
	// 不带 gorm default：显式传 0（关闭熔断）也是合法值，
	// 数据库默认值会把用户明确给的 0 偷换成 20。默认值由 service 层统一给。
	FailureRateThreshold float64 `gorm:"not null" json:"failureRateThreshold"`
	// AckTimeoutSec 单台 ACK 超时（秒，默认 30）。
	AckTimeoutSec int `gorm:"not null;default:30" json:"ackTimeoutSec"`

	Status ManagedPushSessionStatus `gorm:"type:varchar(24);not null;index" json:"status"`
	// PauseKind/PauseReason 暂停现场（人工 / 熔断 + 人读原因）。
	PauseKind   string `gorm:"type:varchar(24)" json:"pauseKind,omitempty"`
	PauseReason string `gorm:"type:varchar(512)" json:"pauseReason,omitempty"`
	// BreakReason/BreakThreshold/BreakObserved 熔断现场：哪条阈值、阈值多少、实测多少。
	BreakReason    string  `gorm:"type:varchar(255)" json:"breakReason,omitempty"`
	BreakThreshold float64 `gorm:"not null;default:0" json:"breakThreshold,omitempty"`
	BreakObserved  float64 `gorm:"not null;default:0" json:"breakObserved,omitempty"`

	// 进度计数（读列表不必逐行 COUNT）。
	PlannedCount int `gorm:"not null;default:0" json:"plannedCount"`
	AppliedCount int `gorm:"not null;default:0" json:"appliedCount"`
	FailedCount  int `gorm:"not null;default:0" json:"failedCount"`
	TimeoutCount int `gorm:"not null;default:0" json:"timeoutCount"`
	SkippedCount int `gorm:"not null;default:0" json:"skippedCount"`
	PendingCount int `gorm:"not null;default:0" json:"pendingCount"`

	// CurrentBatch 当前批号（1 起）；TotalBatches 总批数。
	CurrentBatch int `gorm:"not null;default:0" json:"currentBatch"`
	TotalBatches int `gorm:"not null;default:0" json:"totalBatches"`

	// SourceSessionID 回滚会话指向被回滚的那次 completed 会话。
	SourceSessionID *uint `gorm:"index" json:"sourceSessionId,omitempty"`
	// ConfirmToken 回滚二次确认令牌（回滚必须显式确认，不做自动回滚）。
	ConfirmToken string `gorm:"type:varchar(64)" json:"-"`

	CreatedBy  uint       `gorm:"index" json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// TableName 指定表名。
func (ManagedPushSession) TableName() string { return "managed_push_sessions" }

// ManagedPushTarget 是一次会话在一台实例上的经历。
//
// `(session_id, instance_id)` 唯一：同一会话不会把同一台算两次（重试走 `Attempts` 自增）。
type ManagedPushTarget struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	SessionID    uint   `gorm:"not null;uniqueIndex:idx_managed_push_target" json:"sessionId"`
	InstanceID   uint   `gorm:"not null;uniqueIndex:idx_managed_push_target;index" json:"instanceId"`
	InstanceUUID string `gorm:"type:char(36);index" json:"instanceUuid"`
	InstanceName string `gorm:"type:varchar(128)" json:"instanceName"`
	NodeID       uint   `gorm:"index" json:"nodeId"`
	BatchNo      int    `gorm:"not null;default:1;index" json:"batchNo"`

	State ManagedPushTargetState `gorm:"type:varchar(16);not null;index" json:"state"`
	// DesiredVersion 本次下发给该实例的 configVersion（CP 侧期望值）。
	DesiredVersion int64 `gorm:"not null;default:0" json:"desiredVersion"`
	// BaseVersion 下发时使用的乐观并发基线（取上一次回执/上报的版本，首次为 0）。
	BaseVersion int64 `gorm:"not null;default:0" json:"baseVersion"`
	// AckVersion 探针回报的版本（applied 时 = DesiredVersion；未回报为 nil）。
	AckVersion *int64 `json:"ackVersion,omitempty"`
	// EffectiveJSON 探针回报的生效值（applied 时记录，供下钻比对）。
	EffectiveJSON string `gorm:"type:longtext" json:"-"`
	// PushedPayloadJSON 本次**实际发出去**的 config_push 载荷原文（CP 视角，下发时记录）。
	//
	// 与 EffectiveJSON 的区别必须分清：那是探针回报的「它现在生效什么」，这是 CP 写下的
	// 「我当时发了什么」。回滚要重推的是后者——只有它才能让「回到上一次推送的状态」这句话成立。
	//
	// 为什么必须在**每台目标**上存而不是只存会话级：每台的载荷都不同。三层配置里
	// node/instance 层按实例求值，同一会话下 A 机器的有效配置不是 B 机器的，故会话级的
	// ValuesJSON 只是代表值，拿它重推会让回滚变成「推一份从未存在过的配置」。
	PushedPayloadJSON string `gorm:"type:longtext" json:"-"`
	// PlannedPayloadJSON 创建会话时冻结的每实例合并载荷（计划值，与实际发送不同列）。
	//
	// 为什么不能到时再合并：多批会话跨越分钟级，期间有人改层，晚批实例就会收到一份
	// 「创建会话时谁也没见过的配置」——同一次下发在不同批次内容不一致。冻结在创建时，
	// 下发时只换版本号与基线。回滚不重用它（回滚的源是 PushedPayloadJSON）。
	PlannedPayloadJSON string `gorm:"type:longtext" json:"-"`
	// ErrorKind/Error 失败/拒绝的原因（机器码 + 人读文案）。
	ErrorKind string `gorm:"type:varchar(48)" json:"errorKind,omitempty"`
	Error     string `gorm:"type:varchar(1024)" json:"error,omitempty"`
	// Attempts 重试次数（含首次：首次下发即 1）。
	Attempts int `gorm:"not null;default:0" json:"attempts"`

	PushedAt   *time.Time `json:"pushedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// TableName 指定表名。
func (ManagedPushTarget) TableName() string { return "managed_push_targets" }

// ManagedInstanceConfigState 是每台实例「最近一次托管状态」（config_state 周期上报）。
//
// ## 为什么 cache_age_seconds 必须可空
// 上游 `ManagedConfigState.CacheAgeSecond` 的语义是「距上次成功应用的秒数」，
// **从未应用过时为 null**。null 与 0 语义相反：0 表示「刚刚生效过」，null 表示「无基线」。
// 把 null 落成 0 会让对账矩阵把一台从未生效过的机器显示成「刚刚同步完成」。
//
// ## 桥连接状态单独一列
// `EffectiveConnection` 是**合成值**：探针自报的 `connection` 走的是同一条桥，
// 桥断连时上报帧根本发不出来，界面会一直显示断连前的陈旧值。
// 所以以桥连接事件为主证据（BridgeConnected），自报值单独保留在 ReportedConnection。
type ManagedInstanceConfigState struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// InstanceID 实例 ID（唯一：每台一行，覆盖式 upsert）。
	InstanceID   uint   `gorm:"not null;uniqueIndex" json:"instanceId"`
	InstanceUUID string `gorm:"type:char(36);index" json:"instanceUuid"`

	// ConfigVersion 探针当前生效的托管配置版本（config_state.configVersion）。
	ConfigVersion int64 `gorm:"not null;default:0" json:"configVersion"`
	// EffectiveJSON 探针回报的生效值。
	EffectiveJSON string `gorm:"type:longtext" json:"-"`
	// CacheAgeSeconds 距上次成功应用的秒数；**null = 从未应用过**（不是 0）。
	CacheAgeSeconds *int64 `json:"cacheAgeSeconds"`
	// Mode 探针运行模式（探针自报，原样保留）。
	Mode string `gorm:"type:varchar(32)" json:"mode,omitempty"`
	// ReportedConnection 探针自报的连接态（四态，可能因断连而过期）。
	ReportedConnection string `gorm:"type:varchar(24)" json:"reportedConnection,omitempty"`
	// BridgeConnected 桥连接证据（CP 视角，来自 connected/disconnected 事件）。
	BridgeConnected bool `gorm:"not null;default:false;index" json:"bridgeConnected"`
	// EffectiveConnection 合成后的连接状态（写时算好，读时直接筛）。
	EffectiveConnection string `gorm:"type:varchar(24);not null;default:disconnected;index" json:"connection"`
	// CapabilitiesJSON 探针自报能力位。
	CapabilitiesJSON string `gorm:"type:text" json:"-"`

	LastError  string     `gorm:"type:varchar(1024)" json:"lastError,omitempty"`
	ReportedAt *time.Time `json:"reportedAt,omitempty"`
	// BridgeUpdatedAt 最近一次桥连接事件时刻（判定「观测是否早于连接」的辅证）。
	BridgeUpdatedAt *time.Time `json:"bridgeUpdatedAt,omitempty"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// TableName 指定表名。
func (ManagedInstanceConfigState) TableName() string { return "managed_instance_config_states" }

// SynthesizeManagedConnection 合成连接状态：桥连接证据优先，自报值仅作补充。
//
// 规则（三条，缺一不可）：
//  1. 桥没连 → 一律 `disconnected`。此时任何自报值都必然是断连前的陈旧值，采信它等于撒谎。
//  2. 桥连着、自报是四态之一 → 采信自报（它能区分 degraded / unauthorized，
//     后者表示控制面拒了凭据，探针不会降级到文件配置，属需要告警的异常而非普通断线）。
//     探针上行的 `online` 与合成值的 `connected` 同义，需先归一（见 ManagedBridgeConnectionOnline）。
//  3. 桥连着、但自报缺失或未知 → 记为 connected（桥本身就是最强的可用性证据）。
//
// 抽成纯函数是为了让「写时算」与「测试断言」用同一份逻辑：
// 矩阵的筛选作用在 EffectiveConnection 列上，若写入与判定各写一套，筛选结果就会和展示不一致。
func SynthesizeManagedConnection(bridgeConnected bool, reported string) string {
	if !bridgeConnected {
		return ManagedConnectionDisconnected
	}
	switch reported {
	case ManagedConnectionConnected, ManagedBridgeConnectionOnline:
		return ManagedConnectionConnected
	case ManagedConnectionDegraded, ManagedConnectionUnauthorized:
		return reported
	}
	return ManagedConnectionConnected
}

// JudgeManagedConsistency 判定对账矩阵的一致性（四个取值之一）。
//
// 判定顺序即优先级，每条都有明确的运维动作：
//  1. `failed`：最近一次下发终结为失败类。**必须优先于其它三条**——一台从未上报过、
//     且这次又被拒的机器，标成 neverReported 会让运维去等它上报，而实际要先修基线冲突。
//  2. `neverReported`：从未收到 config_state，没有可比对的观测值。
//  3. `inSync` / `drift`：期望（未下发按 0 计）与观测比较。
//     未下发 + 观测为 0（探针按文件配置运行）归 inSync：CP 没有期望，就不存在漂移。
func JudgeManagedConsistency(latestTargetState string, desiredVersion *int64, observedVersion *int64) string {
	switch ManagedPushTargetState(latestTargetState) {
	case ManagedPushTargetFailed, ManagedPushTargetRejected, ManagedPushTargetTimeout:
		return ManagedConsistencyFailed
	}
	if observedVersion == nil {
		return ManagedConsistencyNeverReported
	}
	var desired int64
	if desiredVersion != nil {
		desired = *desiredVersion
	}
	if desired == *observedVersion {
		return ManagedConsistencyInSync
	}
	return ManagedConsistencyDrift
}

// DescribeManagedPushState 返回目标状态的中文名（错误文案与前端兜底用）。
func DescribeManagedPushState(s ManagedPushTargetState) string {
	switch s {
	case ManagedPushTargetPending:
		return "待下发"
	case ManagedPushTargetPushing:
		return "等待回执"
	case ManagedPushTargetApplied:
		return "已应用"
	case ManagedPushTargetFailed:
		return "应用失败"
	case ManagedPushTargetRejected:
		return "整包被拒"
	case ManagedPushTargetTimeout:
		return "未回执"
	case ManagedPushTargetSkipped:
		return "已跳过"
	}
	return fmt.Sprintf("未知状态(%s)", string(s))
}
