package model

import "time"

// 配置分批交付（FR-531）与生效编排（FR-532）的持久化模型。
//
// 为什么拆两张表而不是把目标塞进 order 的一列 JSON：
//   - order 是「一次变更」的状态机，行数少、状态与暂停原因被频繁改写；
//   - target 是「这次变更里每台机器发生了什么」，行数 = 选中实例数，必须能按
//     (order, batch, status) 过滤分页。塞进 JSON 后「这台为什么失败」「跳过的是哪几台」
//     就只能全量反序列化再在内存里筛，而这正是运维在出事时最常问的两个问题。

// ConfigDeliveryStatus 交付单级状态（FR-531 §7）。
type ConfigDeliveryStatus string

const (
	// DeliveryStatusDraft 草稿：可自由编辑与删除。
	DeliveryStatusDraft ConfigDeliveryStatus = "draft"
	// DeliveryStatusPendingApproval 已提交，等待审批。
	DeliveryStatusPendingApproval ConfigDeliveryStatus = "pending_approval"
	// DeliveryStatusApproved 已批准，等待开跑。
	DeliveryStatusApproved ConfigDeliveryStatus = "approved"
	// DeliveryStatusRolling 放量中（正在逐批下发）。
	DeliveryStatusRolling ConfigDeliveryStatus = "rolling"
	// DeliveryStatusObserving 观察窗中（当前批已下发完，等健康指标回归）。
	DeliveryStatusObserving ConfigDeliveryStatus = "observing"
	// DeliveryStatusPaused 已暂停（pause_kind 区分人工暂停 / 熔断）。
	DeliveryStatusPaused ConfigDeliveryStatus = "paused"
	// DeliveryStatusFailed 失败（熔断或不可恢复错误）。终态。
	DeliveryStatusFailed ConfigDeliveryStatus = "failed"
	// DeliveryStatusCompleted 全部批次下发完毕。终态（deferred 目标仍可被自动补推）。
	DeliveryStatusCompleted ConfigDeliveryStatus = "completed"
	// DeliveryStatusRollingBack 回滚中。
	DeliveryStatusRollingBack ConfigDeliveryStatus = "rolling_back"
	// DeliveryStatusRolledBack 已回滚。终态。
	DeliveryStatusRolledBack ConfigDeliveryStatus = "rolled_back"
	// DeliveryStatusCancelled 人工终止。终态。
	//
	// spec §11 定义了 cancel 端点但 §7 的状态机图里没有对应终态——这里补一个，
	// 否则「终止」只能复用 paused，运维无法区分「暂停等继续」与「已放弃」。
	DeliveryStatusCancelled ConfigDeliveryStatus = "cancelled"
)

// deliveryOrderTransitions 是交付单的**手写转移表**（不引入状态机库）。
//
// 用表而不是散落在各方法里的 if：状态机是这张单的核心契约，集中在一处才能一眼看出
// 「哪些转移是合法的」，也才能被单测逐条覆盖。每条转移都必须有对应的业务动作，
// 否则就是可以「跳步」的后门。
var deliveryOrderTransitions = map[ConfigDeliveryStatus][]ConfigDeliveryStatus{
	DeliveryStatusDraft:           {DeliveryStatusPendingApproval},
	DeliveryStatusPendingApproval: {DeliveryStatusApproved, DeliveryStatusDraft},
	DeliveryStatusApproved:        {DeliveryStatusRolling},
	DeliveryStatusRolling: {
		DeliveryStatusObserving, DeliveryStatusPaused, DeliveryStatusFailed,
		DeliveryStatusRollingBack, DeliveryStatusCompleted,
	},
	DeliveryStatusObserving: {
		// 观察窗结束且健康回归 → 回到 rolling 推下一批；这也正是 spec §7 图里
		// rolling ⇄ observing 之间那条隐含的回路，不写出来就只能靠绕过状态机。
		DeliveryStatusRolling, DeliveryStatusCompleted, DeliveryStatusPaused, DeliveryStatusFailed,
		DeliveryStatusRollingBack,
	},
	DeliveryStatusPaused: {
		DeliveryStatusRolling, DeliveryStatusRollingBack, DeliveryStatusCancelled,
	},
	// failed 与 completed 都允许回滚：失败后回滚是常态，成功后回滚是补救。
	DeliveryStatusFailed:    {DeliveryStatusRollingBack},
	DeliveryStatusCompleted: {DeliveryStatusRollingBack},
	DeliveryStatusRollingBack: {
		DeliveryStatusRolledBack, DeliveryStatusFailed,
	},
	// 已回滚仍可再次回滚（§10.3「回滚本身也可回滚」）——这一条是那个要求的落点。
	DeliveryStatusRolledBack: {DeliveryStatusRollingBack},
	DeliveryStatusCancelled:  {},
}

// CanTransitTo 判断交付单能否从当前状态转移到目标状态（纯函数，便于单测）。
func (s ConfigDeliveryStatus) CanTransitTo(to ConfigDeliveryStatus) bool {
	for _, allowed := range deliveryOrderTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// IsTerminal 判断是否为终态（终态不再接受 submit/resume 等推进动作）。
func (s ConfigDeliveryStatus) IsTerminal() bool {
	switch s {
	case DeliveryStatusCompleted, DeliveryStatusFailed, DeliveryStatusRolledBack, DeliveryStatusCancelled:
		return true
	}
	return false
}

// ValidConfigDeliveryStatus 校验枚举是否合法。
func ValidConfigDeliveryStatus(s ConfigDeliveryStatus) bool {
	_, ok := deliveryOrderTransitions[s]
	return ok
}

// ConfigDeliveryTargetStatus 目标级状态（FR-531 §7.1）。
type ConfigDeliveryTargetStatus string

const (
	// DeliveryTargetPending 未下发。
	DeliveryTargetPending ConfigDeliveryTargetStatus = "pending"
	// DeliveryTargetPushing 下发中。
	DeliveryTargetPushing ConfigDeliveryTargetStatus = "pushing"
	// DeliveryTargetPushed 落盘成功（尚未生效）。
	DeliveryTargetPushed ConfigDeliveryTargetStatus = "pushed"
	// DeliveryTargetActivating 生效中。
	DeliveryTargetActivating ConfigDeliveryTargetStatus = "activating"
	// DeliveryTargetActivated 生效成功。
	DeliveryTargetActivated ConfigDeliveryTargetStatus = "activated"
	// DeliveryTargetFailed 失败（reason_code 带原因码）。
	DeliveryTargetFailed ConfigDeliveryTargetStatus = "failed"
	// DeliveryTargetDeferred 离线/暂不可达：**不计入失败率**，上线后自动补推。
	DeliveryTargetDeferred ConfigDeliveryTargetStatus = "deferred"
	// DeliveryTargetSkipped 主动跳过（熔断或终止）。
	DeliveryTargetSkipped ConfigDeliveryTargetStatus = "skipped"
)

// IsTerminal 判断目标是否已到终态。
//
// deferred **不是终态**：它是「等目标上线再补推」的挂起态，补推会把它重新推回
// pushing。若把 deferred 当终态，「最多 3 次补推」就永远没有下一次。
//
// skipped 计入终态：它表示「本次变更主动放弃这台」，后续只有人工 resume 才会再看它。
func (s ConfigDeliveryTargetStatus) IsTerminal() bool {
	switch s {
	case DeliveryTargetActivated, DeliveryTargetFailed, DeliveryTargetSkipped:
		return true
	}
	return false
}

// ValidConfigDeliveryTargetStatus 校验枚举是否合法。
func ValidConfigDeliveryTargetStatus(s ConfigDeliveryTargetStatus) bool {
	switch s {
	case DeliveryTargetPending, DeliveryTargetPushing, DeliveryTargetPushed, DeliveryTargetActivating,
		DeliveryTargetActivated, DeliveryTargetFailed, DeliveryTargetDeferred, DeliveryTargetSkipped:
		return true
	}
	return false
}

// ConfigDeliveryBatchMode 分批模式。
type ConfigDeliveryBatchMode string

const (
	// DeliveryBatchPercent 按百分比（batch_sizes 形如 [10,30,60]，总和应为 100）。
	DeliveryBatchPercent ConfigDeliveryBatchMode = "percent"
	// DeliveryBatchCount 按台数（batch_sizes 形如 [1,5,20]）。
	DeliveryBatchCount ConfigDeliveryBatchMode = "count"
)

// ValidConfigDeliveryBatchMode 校验分批模式枚举。
func ValidConfigDeliveryBatchMode(m ConfigDeliveryBatchMode) bool {
	return m == DeliveryBatchPercent || m == DeliveryBatchCount
}

// ConfigActivationMethod 生效方式（FR-532 §13）。
type ConfigActivationMethod string

const (
	// ActivationHotReload 下发 core.config_reload 由插件重载。
	ActivationHotReload ConfigActivationMethod = "hot_reload"
	// ActivationRestart 停实例 → 起实例 → 等健康回归。
	ActivationRestart ConfigActivationMethod = "restart"
	// ActivationPushOnly 只落盘，不触发任何动作（下次启动自然生效）。
	ActivationPushOnly ConfigActivationMethod = "push_only"
)

// ValidConfigActivationMethod 校验生效方式枚举。
func ValidConfigActivationMethod(m ConfigActivationMethod) bool {
	switch m {
	case ActivationHotReload, ActivationRestart, ActivationPushOnly:
		return true
	}
	return false
}

// ConfigDeliveryPauseKind 暂停原因分类。
type ConfigDeliveryPauseKind string

const (
	// DeliveryPauseManual 人工暂停。
	DeliveryPauseManual ConfigDeliveryPauseKind = "manual"
	// DeliveryPauseCircuitBreak 熔断（失败率/不健康率/离线率触阈）。
	DeliveryPauseCircuitBreak ConfigDeliveryPauseKind = "circuit_break"
)

// ConfigDeliveryOrder 配置交付单（FR-531 §6）。
type ConfigDeliveryOrder struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// ConfigFileID 关联的配置逻辑文件（FR-529 ConfigFile）。
	ConfigFileID uint `gorm:"index;not null" json:"configFileId"`
	// FilePath 落盘目标路径（相对实例工作目录）。为空时取 ConfigFile.Name。
	FilePath string `gorm:"type:varchar(512);not null" json:"filePath"`
	// TargetLayerLevel/TargetLayerScopeID 指定本次应用的是哪一层的版本。
	TargetLayerLevel   ConfigScopeLevel `gorm:"type:varchar(16);not null" json:"targetLayerLevel"`
	TargetLayerScopeID uint             `gorm:"not null" json:"targetLayerScopeId"`
	// TargetLayerVersionID 提交时刻该层的 head 版本（审计用：到底应用了哪一版）。
	TargetLayerVersionID uint `json:"targetLayerVersionId"`
	// FromLayerVersionID 变更前的层 head（整批回滚的版本回退目标）。
	FromLayerVersionID *uint `json:"fromLayerVersionId,omitempty"`
	// SelectorJSON 目标实例选择器（分组/标签/显式 ID + 过滤）。
	SelectorJSON string `gorm:"type:text" json:"-"`
	// BatchMode/BatchSizesJSON 分批策略。空数组表示不分批（一次全下）。
	BatchMode      ConfigDeliveryBatchMode `gorm:"type:varchar(16);not null" json:"batchMode"`
	BatchSizesJSON string                  `gorm:"type:text" json:"-"`
	// ObserveWindowSec 批间观察窗（默认 120）。
	ObserveWindowSec int `gorm:"default:120" json:"observeWindowSec"`
	// FailureRateThreshold 失败率阈值 %，0=关闭。
	FailureRateThreshold int `gorm:"default:0" json:"failureRateThreshold"`
	// UnhealthyRateThreshold 不健康率阈值 %，0=关闭。
	UnhealthyRateThreshold int `gorm:"default:0" json:"unhealthyRateThreshold"`
	// ActivationMethod 生效方式。
	ActivationMethod ConfigActivationMethod `gorm:"type:varchar(16);not null" json:"activationMethod"`
	// ActivateTimeoutSec 生效等待上限（restart 等健康回归；hot_reload 之外的兜底）。
	ActivateTimeoutSec int `gorm:"default:300" json:"activateTimeoutSec"`
	// ReloadTimeoutSec core.config_reload 单独的超时（默认 10s，**不复用业务命令 5s**）。
	ReloadTimeoutSec int `gorm:"default:10" json:"reloadTimeoutSec"`
	// AutoRetryDeferred 离线目标上线后是否自动补推（默认 true，§8.2）。
	//
	// **刻意不写 `default:true`**：GORM 对带 default tag 的零值字段会从 INSERT 里剔除，
	// 让数据库默认值生效——而这里 false 是个有意义的取值（显式关闭补推）。
	// 一旦带上 default tag，"关掉开关"会被静默落库成 true，且读回来的行看不出异常。
	// 默认值改由服务层补（normalizeDeliveryInput 里 boolValue(nil, true)）。
	AutoRetryDeferred bool `gorm:"not null" json:"autoRetryDeferred"`
	// DeferredRetryLimit 同一目标最多补推次数（默认 3，§8.2）。
	DeferredRetryLimit int `gorm:"default:3" json:"deferredRetryLimit"`

	Status      ConfigDeliveryStatus `gorm:"type:varchar(24);not null;index" json:"status"`
	PauseKind   string               `gorm:"type:varchar(24)" json:"pauseKind,omitempty"`
	PauseReason string               `gorm:"type:varchar(512)" json:"pauseReason,omitempty"`
	// BreakReason/BreakThreshold/BreakObserved 熔断现场：哪条阈值、阈值多少、实测多少。
	BreakReason    string  `gorm:"type:varchar(255)" json:"breakReason,omitempty"`
	BreakThreshold float64 `gorm:"default:0" json:"breakThreshold,omitempty"`
	BreakObserved  float64 `gorm:"default:0" json:"breakObserved,omitempty"`

	// 进度计数（读列表时不必 JOIN 目标表）。
	PlannedCount   int `gorm:"default:0" json:"plannedCount"`
	PushedCount    int `gorm:"default:0" json:"pushedCount"`
	ActivatedCount int `gorm:"default:0" json:"activatedCount"`
	FailedCount    int `gorm:"default:0" json:"failedCount"`
	DeferredCount  int `gorm:"default:0" json:"deferredCount"`
	SkippedCount   int `gorm:"default:0" json:"skippedCount"`
	// CurrentBatch 当前批号（1 起）。
	CurrentBatch int `gorm:"default:0" json:"currentBatch"`
	TotalBatches int `gorm:"default:0" json:"totalBatches"`
	// FailedBatchNo 熔断/失败发生在第几批（0=未记录）。
	FailedBatchNo int `gorm:"default:0" json:"failedBatchNo,omitempty"`
	// BatchStatesJSON 逐批终态（batchNo → completed/failed），落实 §9.3「当前批置 failed」
	// ——批不是实体，只能挂在单上记账。
	BatchStatesJSON string `gorm:"type:text" json:"-"`

	CreatedBy  uint       `gorm:"index" json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// TableName 指定表名（与 spec §6 的命名一致）。
func (ConfigDeliveryOrder) TableName() string { return "config_delivery_order" }

// ConfigDeliveryTarget 交付目标：一次变更在一台实例上的完整经历（FR-531 §6）。
type ConfigDeliveryTarget struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// OrderID 所属交付单；唯一约束 (order_id, instance_id) 防止同一单把同一台算两次。
	OrderID      uint   `gorm:"not null;uniqueIndex:idx_cfg_delivery_target" json:"orderId"`
	InstanceID   uint   `gorm:"not null;uniqueIndex:idx_cfg_delivery_target" json:"instanceId"`
	InstanceName string `gorm:"type:varchar(255)" json:"instanceName"`
	BatchNo      int    `gorm:"default:0;index" json:"batchNo"`

	Status ConfigDeliveryTargetStatus `gorm:"type:varchar(16);not null;index" json:"status"`
	// Reason 人读原因（展示在目标列表里）。
	Reason string `gorm:"type:varchar(512)" json:"reason,omitempty"`
	// ReasonCode 机器可读原因码（PATH_ESCAPE / WRITE_VERIFY_FAILED / TIMEOUT / BEST_EFFORT…）。
	ReasonCode string `gorm:"type:varchar(48)" json:"reasonCode,omitempty"`

	// ChangedFileCount 本次实际改动的文件数（当前一单一文件，落盘成功即 1）。
	ChangedFileCount int `gorm:"default:0" json:"changedFileCount"`
	// BackupPresent 写前是否已存在可回滚的旧版本。
	BackupPresent bool `gorm:"default:false" json:"backupPresent"`
	// BackupPath 写前快照相对路径（Worker .jm-backup/<path>.<stamp>）。
	BackupPath string `gorm:"type:varchar(512)" json:"backupPath,omitempty"`
	// PreviousVersionID 写入前的实例配置版本（回滚到它）。
	PreviousVersionID *uint `json:"previousVersionId,omitempty"`
	// NewVersionID 本次写入产生的实例配置版本。
	NewVersionID *uint `json:"newVersionId,omitempty"`

	// DiskWritten 落盘是否成功。与 Status 分开记：**落盘成功 ≠ 生效成功**（FR-532 §16）。
	DiskWritten bool `json:"diskWritten"`
	// ActivateConfirmed 生效是否得到确切确认。best_effort 时为 false——
	// 这正是「已请求重载」不能写成「已生效」的机器可读依据。
	ActivateConfirmed bool `gorm:"default:false" json:"activateConfirmed"`
	// BestEffort 插件未提供确切回执（Bukkit reloadConfig() 是 void）。
	BestEffort bool `gorm:"default:false" json:"bestEffort"`
	// ActivationMessage 诚实文案（"配置已重新加载" / "已请求重载，插件未提供确认" / "重载超时…"）。
	ActivationMessage string `gorm:"type:varchar(512)" json:"activationMessage,omitempty"`
	// ReceiptJSON 探针回执原文（原样留存，便于对账）。
	ReceiptJSON string `gorm:"type:text" json:"-"`

	// RetryCount deferred 补推次数（上限 DeferredRetryLimit）。
	RetryCount int `gorm:"default:0" json:"retryCount"`
	// RolledBack 该目标是否已被回滚（回滚可再回滚，故用时间戳而非布尔）。
	RolledBack bool `gorm:"default:false" json:"rolledBack"`
	// RollbackOfRollback 本次动作是「回滚的回滚」（§10.3）。
	RollbackOfRollback bool `gorm:"default:false" json:"rollbackOfRollback"`

	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// TableName 指定表名。
func (ConfigDeliveryTarget) TableName() string { return "config_delivery_target" }

// ConfigDeliveryRollback 回滚记录（§10.3：回滚本身也可再回滚，必须留下链条）。
//
// 独立成表而不是只改目标行：一次整批回滚覆盖 N 台，若只写目标的 rolled_back 标记，
// 就答不出「这一轮回滚是哪次、回滚了哪些、有没有回滚过这轮回滚」。
type ConfigDeliveryRollback struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	OrderID uint `gorm:"not null;index" json:"orderId"`
	// Scope 回滚范围：full（整批，含版本回退）/ subset（目标级子集，版本不回退）。
	Scope string `gorm:"type:varchar(16);not null" json:"scope"`
	// InstanceIDsJSON 本轮回滚覆盖的实例 ID 列表（空数组表示整批）。
	InstanceIDsJSON string `gorm:"type:text" json:"-"`
	// LayerRolledBack 是否执行了配置层版本回退。
	LayerRolledBack bool `json:"layerRolledBack"`
	// RolledBackOrderID 若本轮回滚是「回滚某次回滚」，指向被覆盖的上一轮回滚。
	RolledBackOrderID *uint `json:"rolledBackOrderId,omitempty"`
	// RollbackOfRollback 本次是否为「回滚的回滚」。
	RollbackOfRollback bool   `gorm:"default:false" json:"rollbackOfRollback"`
	Reason             string `gorm:"type:varchar(512)" json:"reason,omitempty"`
	// Succeeded/Failed 逐目标结果计数。
	Succeeded int       `gorm:"default:0" json:"succeeded"`
	Failed    int       `gorm:"default:0" json:"failed"`
	CreatedBy uint      `gorm:"index" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// TableName 指定表名。
func (ConfigDeliveryRollback) TableName() string { return "config_delivery_rollback" }
