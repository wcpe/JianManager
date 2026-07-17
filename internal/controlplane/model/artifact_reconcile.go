package model

import "time"

// 对账运行状态（FR-349，见 spec artifact-s3-reconcile §3.2）。
const (
	// ArtifactReconcileRunning 对账进行中（异步执行，前端轮询）。
	ArtifactReconcileRunning = "running"
	// ArtifactReconcileSucceeded 对账完成，差异明细已落库。
	ArtifactReconcileSucceeded = "succeeded"
	// ArtifactReconcileFailed 对账失败（存储遍历报错 / CP 重启中断），ErrorMessage 带原因。
	ArtifactReconcileFailed = "failed"
)

// 对账触发方式。
const (
	// ArtifactReconcileTriggerManual 面板手动触发。
	ArtifactReconcileTriggerManual = "manual"
	// ArtifactReconcileTriggerScheduled 定期任务触发。
	ArtifactReconcileTriggerScheduled = "scheduled"
)

// 差异类别。
const (
	// ArtifactDiffMissing 缺失：索引有、S3 无——下载必 404 的高危项。
	ArtifactDiffMissing = "missing"
	// ArtifactDiffOrphan 孤儿：S3 有、索引无——白占存储。
	ArtifactDiffOrphan = "orphan"
)

// 差异处置状态。
const (
	// ArtifactDiffOpen 待处置。
	ArtifactDiffOpen = "open"
	// ArtifactDiffResolved 已处置（ResolvedAction 记方式）。
	ArtifactDiffResolved = "resolved"
)

// 差异处置方式。
const (
	// ArtifactDiffActionMarkedLost 缺失 → 资产已标记失效（StorageState=lost）。
	ArtifactDiffActionMarkedLost = "marked_lost"
	// ArtifactDiffActionCleaned 孤儿 → S3 对象已清理删除。
	ArtifactDiffActionCleaned = "cleaned"
	// ArtifactDiffActionStale 差异已过时（run 后资产被删/迁移、或同键对象已被新上传合法引用），
	// 处置时守卫命中，不动资产也不删对象（FR-349 处置安全性，见 spec §3.5）。
	ArtifactDiffActionStale = "stale"
)

// ArtifactReconcileRun 一次制品索引 ↔ S3 对象清单的对账运行记录（FR-349）。
// 逐 s3 渠道对账；local 不对账（本地文件系统由 CAS 自管）。面板查最近 N 次。
type ArtifactReconcileRun struct {
	ID        uint `gorm:"primaryKey" json:"id"`
	ChannelID uint `gorm:"not null;index" json:"channelId"`
	// ChannelName 渠道名快照：渠道后续改名/删除不影响历史报告可读。
	ChannelName string `gorm:"type:varchar(128)" json:"channelName"`
	// Status running | succeeded | failed。
	Status string `gorm:"type:varchar(16);not null" json:"status"`
	// TriggeredBy manual | scheduled。
	TriggeredBy string     `gorm:"type:varchar(16);not null" json:"triggeredBy"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
	// IndexCount 索引侧扫描条数（该渠道 s3 资产数，含已 lost）。
	IndexCount int `json:"indexCount"`
	// ObjectCount 对象侧扫描条数（渠道 prefix 下 CAS client-file 命名空间内）。
	ObjectCount int `json:"objectCount"`
	// MatchedCount 两侧一致数（仅计数不落明细）。
	MatchedCount int `json:"matchedCount"`
	MissingCount int `json:"missingCount"`
	OrphanCount  int `json:"orphanCount"`
	// ErrorMessage failed 时的原因（截断 512）。
	ErrorMessage string    `gorm:"type:varchar(512)" json:"errorMessage"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ArtifactReconcileDiff 差异明细（缺失/孤儿），随 run 落库，处置后翻 resolved（FR-349）。
type ArtifactReconcileDiff struct {
	ID        uint `gorm:"primaryKey" json:"id"`
	RunID     uint `gorm:"not null;index" json:"runId"`
	ChannelID uint `gorm:"not null;index" json:"channelId"`
	// Kind missing | orphan。
	Kind string `gorm:"type:varchar(16);not null;index" json:"kind"`
	// AssetID missing：资产 ID；orphan：0（对象不在索引，无资产可指）。
	AssetID uint `json:"assetId"`
	// SHA256 missing：资产内容寻址键；orphan 空。
	SHA256 string `gorm:"type:char(64)" json:"sha256"`
	// ObjectKey CAS 相对键（missing=Asset.RelPath；orphan=剥渠道前缀后的对象键）。
	ObjectKey string `gorm:"type:varchar(512);not null" json:"objectKey"`
	Size      int64  `json:"size"`
	// LastModified orphan：S3 对象 Last-Modified；missing nil。
	LastModified *time.Time `json:"lastModified"`
	// Status open | resolved。
	Status     string     `gorm:"type:varchar(16);not null;default:open" json:"status"`
	ResolvedAt *time.Time `json:"resolvedAt"`
	// ResolvedAction marked_lost | cleaned | stale。
	ResolvedAction string `gorm:"type:varchar(32)" json:"resolvedAction"`
	// ResolveError 清理失败原因（保持 open 供重试）。
	ResolveError string    `gorm:"type:varchar(512)" json:"resolveError"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ArtifactReconcileSetting 定期对账设置（单行 id=1，服务 firstOrCreate 兜底；FR-349）。
type ArtifactReconcileSetting struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Enabled 定期对账开关，默认开。
	Enabled bool `gorm:"not null;default:true" json:"enabled"`
	// IntervalHours 周期小时数，默认 24（每日），钳制 [1,720]。
	IntervalHours int `gorm:"not null;default:24" json:"intervalHours"`
	// NextRunAt 下次定期触发时间；禁用时 nil。首个周期从启用/启动时刻起算（不在启动瞬间扫存储）。
	NextRunAt *time.Time `json:"nextRunAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}
