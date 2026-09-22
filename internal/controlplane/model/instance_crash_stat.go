package model

import "time"

// InstanceCrashStat 崩溃趋势汇总（FR-470）：按 (实例, 天, 根因, 指纹) 计数。
//
// 关键决策：趋势数据**不复用** InstanceCrashSnapshot——后者每实例滚动保留 K=5 条
// （见 grpc/crash_snapshot.go 的 pruneCrashSnapshots），连崩超过 5 次后更早的记录被裁剪，
// 趋势随之被抹掉。独立汇总表在每次快照入库时 upsert 一条计数，即使快照被裁剪，趋势仍完整。
// 日粒度使行数有界（实例数 × 天数 × 根因数）。
type InstanceCrashStat struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// InstanceID 所属实例；(instance_id, bucket_day, root_cause, signature) 唯一，供 upsert。
	InstanceID uint `gorm:"not null;uniqueIndex:idx_crash_stat_daily,priority:1" json:"instanceId"`
	// BucketDay 按天聚合桶（YYYY-MM-DD，UTC），日粒度足够趋势分析且行数有界。
	BucketDay string `gorm:"type:char(10);not null;uniqueIndex:idx_crash_stat_daily,priority:2" json:"bucketDay"`
	// RootCause 根因归类（枚举见 service/crash_classify.go）。
	RootCause string `gorm:"type:varchar(32);not null;uniqueIndex:idx_crash_stat_daily,priority:3" json:"rootCause"`
	// Signature 归一化同类指纹（上限 255，与快照列一致）。
	Signature string `gorm:"type:varchar(255);not null;uniqueIndex:idx_crash_stat_daily,priority:4" json:"signature"`
	// Count 该桶内同 (根因, 指纹) 的崩溃次数。
	Count     int       `gorm:"not null" json:"count"`
	UpdatedAt time.Time `json:"updatedAt"`
}
