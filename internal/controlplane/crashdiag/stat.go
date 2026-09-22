package crashdiag

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// crashStatDayLayout 崩溃统计的日粒度桶格式（UTC，YYYY-MM-DD）。
const crashStatDayLayout = "2006-01-02"

// ClassifyCrashSnapshotRecord 在事务内对一条即将落库的快照做同步分类，
// 回填分类字段（调用方负责持久化），并 upsert 日粒度统计（FR-470 §2.2）。
//
// 事务内完成是为了让「快照行」与「统计行」一致：任一步失败都回滚，不会出现
// 「分类写了但统计没记」导致趋势少计一次的偏差。
//
// 统计**不复用快照表**：快照每实例滚动保留 K=5（grpc.pruneCrashSnapshots），趋势若读快照表
// 会被裁剪抹掉；统计表按 (实例, 天, 根因, 指纹) 独立累计，K 裁剪不影响它。
//
// 返回的 error 仅来自统计 upsert——分类是纯函数（无 I/O、无副作用），不会失败。
// 调用方据此区分「统计降级」与「分类降级」：前者保留分类结果，后者才回落 unknown（m-8）。
func ClassifyCrashSnapshotRecord(tx *gorm.DB, snap *model.InstanceCrashSnapshot) error {
	class := ClassifyCrashSnapshot(snap.ExitCode, snap.Signal, snap.TailOutput)
	ApplyCrashClassification(snap, class)
	return UpsertCrashStat(tx, snap.InstanceID, snap.OccurredAt, class.RootCause, class.Signature)
}

// ApplyCrashClassification 把一次分类结果回填到快照实体（不落库，调用方决定持久化时机）。
func ApplyCrashClassification(snap *model.InstanceCrashSnapshot, class CrashClassification) {
	snap.RootCause = class.RootCause
	snap.Signature = class.Signature
	snap.Evidence = EncodeCrashEvidence(class.Evidence)
	snap.Confidence = class.Confidence
}

// UpsertCrashStat 累加一条 (实例, 天, 根因, 指纹) 计数；已存在则 count+1（幂等 upsert）。
//
// Signature 的回退口径：signature 空则退 rootCause，rootCause 也空则退 unknown。
// 两处独立求值易被误改成不同基准，故先算一次 cause 再复用（R17）。
func UpsertCrashStat(tx *gorm.DB, instanceID uint, occurredAt time.Time, rootCause, signature string) error {
	cause := emptyAs(rootCause, CrashRootCauseUnknown)
	stat := model.InstanceCrashStat{
		InstanceID: instanceID,
		BucketDay:  crashDay(occurredAt),
		RootCause:  cause,
		Signature:  emptyAs(signature, cause),
		Count:      1,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "instance_id"}, {Name: "bucket_day"}, {Name: "root_cause"}, {Name: "signature"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"count":      gorm.Expr("count + 1"),
			"updated_at": time.Now(),
		}),
	}).Create(&stat).Error
}

// DecrementCrashStat 递减一条计数；减到 0 时删除该行（保持表有界）。行不存在则为无操作。
//
// R18：递减与删除原先分两条独立语句（`UpdateColumn(count-1)` 后 `Delete(count <= 0)`），
// 中途失败会留下 `count = 0` 的残留行：`TrendByInstance` 的 Total 不受影响（加 0），
// 但 `ByRootCause` 会多出一个计数为 0 的条目（`sortedCauseCounts` 不过滤 0），
// 让运维看到「从未发生的根因」。
//
// 修法：一条语句完成「归零即删」——`DELETE WHERE count <= 1` 先删，再对仍存在的行递减。
// 两者放在同一 gorm 事务内（调用方 `ReapplyCrashStat` 已带事务；此处再加一次 ensure
// 使单独调用也自洽），故「先删后减」在任何中断点都不会留下 count=0 的行：
//   - 删成功、减未执行 → 该行本身已被删除（语义正确）；
//   - 删未执行 → 递减照常，行仍在（下次重分类会再次尝试删除）。
func DecrementCrashStat(tx *gorm.DB, instanceID uint, occurredAt time.Time, rootCause, signature string) error {
	day := crashDay(occurredAt)
	cause := emptyAs(rootCause, CrashRootCauseUnknown)
	sig := emptyAs(signature, cause)
	return tx.Transaction(func(inner *gorm.DB) error {
		// 1) count <= 1 的行直接删除（递减后即为 0，删掉等价且免留残留行）。
		if err := inner.Where("instance_id = ? AND bucket_day = ? AND root_cause = ? AND signature = ? AND count <= 1",
			instanceID, day, cause, sig).Delete(&model.InstanceCrashStat{}).Error; err != nil {
			return err
		}
		// 2) 其余（count >= 2）递减 1。
		return inner.Model(&model.InstanceCrashStat{}).
			Where("instance_id = ? AND bucket_day = ? AND root_cause = ? AND signature = ? AND count > 1",
				instanceID, day, cause, sig).
			UpdateColumn("count", gorm.Expr("count - 1")).Error
	})
}

// ReapplyCrashStat 重分类时把一条快照的计数从旧 (根因, 指纹) 移到新桶：
// hadOld 为 false（旧快照无分类，属回填）时只新增，不减旧，保证幂等。
func ReapplyCrashStat(tx *gorm.DB, instanceID uint, occurredAt time.Time, oldCause, oldSignature, newCause, newSignature string, hadOld bool) error {
	if hadOld && (oldCause != newCause || oldSignature != newSignature) {
		if err := DecrementCrashStat(tx, instanceID, occurredAt, oldCause, oldSignature); err != nil {
			return err
		}
	}
	if !hadOld || oldCause != newCause || oldSignature != newSignature {
		return UpsertCrashStat(tx, instanceID, occurredAt, newCause, newSignature)
	}
	return nil
}

// crashDay 归一化日粒度桶（UTC）。
func crashDay(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(crashStatDayLayout)
}

func emptyAs(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
