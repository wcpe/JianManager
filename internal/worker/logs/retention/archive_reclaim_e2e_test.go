package retention

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// 本文件是「ARCHIVED 权威态 + 回收热层」的**端到端**回归（真实 catalog + 真实执行器）。
//
// 前面 archive_copy_test.go 用假 authority 固定执行器的次序；这里换成真实
// catalog.Catalog，验证一个完整闭环下**Catalog 侧的状态**是否正确：
//   - 三闸全开 + 校验通过 ⇒ 分区变 ARCHIVED 且 HotReclaimed；
//   - 回收失败 ⇒ 分区退回 HOT 且投影/目录恢复（查询能力不失）；
//   - 重跑 ⇒ 不重复归档、不产生半删状态。

func e2eKey() catalog.PartitionKey {
	return catalog.PartitionKey{StorageNamespace: "inst:147", UTCDay: "2026-06-01"}
}

// e2eCatalog 构造一条可查询的稳定 HOT 记录。
func e2eCatalog(t *testing.T) (*catalog.Catalog, catalog.PartitionKey) {
	t.Helper()
	cat := catalog.New(nil)
	key := e2eKey()
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "20260601")
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion:  "manifest-hot",
		CoverageComplete: true,
		CoveredSourceGenerations: []catalog.SourceGenerationRef{
			{LogSourceID: "source", SourceGeneration: "s1"},
		},
		ClosedVisibleSeq:      map[string]uint64{"source/s1": 10, "default": 10},
		QueryLocationDirID:    "20260601",
		QueryGeneration:       1,
		ProjectionGeneration:  "p-hot",
		ProjectionGenerations: []string{"p-hot"},
	}
	rec.LastVerifyCompleted = true
	rec.LastVerifyChecksum = "checksum-hot"
	require.NoError(t, cat.Put(rec))
	return cat, key
}

// e2eAuthority 是测试用的 catalog 适配（与 apps/worker 的实现同语义）。
type e2eAuthority struct {
	cat *catalog.Catalog
}

func (a *e2eAuthority) FreezeForArchive(_ context.Context, ns, day string) error {
	key, err := e2ePartitionKey(ns, day)
	if err != nil {
		return err
	}
	return a.cat.FreezeDayForArchive(key.UTCDay, "archive:"+key.UTCDay)
}

func (a *e2eAuthority) ReleaseArchiveFreeze(_ context.Context, ns, day string, reason string) error {
	key, err := e2ePartitionKey(ns, day)
	if err != nil {
		return err
	}
	for _, rec := range a.cat.RecordsForDay(key.UTCDay) {
		if rec != nil && rec.Owner == catalog.OwnerArchive {
			return nil
		}
	}
	return a.cat.AbortArchiveFreeze(key.UTCDay, reason)
}

func (a *e2eAuthority) MarkArchived(_ context.Context, ns, day string, ev ArchiveEvidence) error {
	key, err := e2ePartitionKey(ns, day)
	if err != nil {
		return err
	}
	_, _, err = a.cat.MarkArchived(key, catalog.ArchiveEvidence{
		ProviderKind:     ev.ProviderKind,
		ObjectPrefix:     ev.ObjectPrefix,
		ManifestChecksum: ev.ManifestChecksum,
		Coverage: catalog.ArchiveCoverage{
			ObjectCount: ev.ObjectCount, SourceBytes: ev.SourceBytes,
			StoredBytes: ev.StoredBytes, Complete: ev.CoverageComplete,
		},
	}, time.Time{})
	return err
}

func (a *e2eAuthority) RollbackArchived(_ context.Context, ns, day string, reason string) error {
	key, err := e2ePartitionKey(ns, day)
	if err != nil {
		return err
	}
	_, err = a.cat.RollbackArchived(key, reason, time.Time{})
	return err
}

func (a *e2eAuthority) MarkHotReclaimed(_ context.Context, ns, day string) error {
	key, err := e2ePartitionKey(ns, day)
	if err != nil {
		return err
	}
	_, err = a.cat.MarkArchiveReclaimed(key, time.Time{})
	return err
}

func e2ePartitionKey(ns, day string) (catalog.PartitionKey, error) {
	parsed, err := time.Parse("20060102", day)
	if err != nil {
		return catalog.PartitionKey{}, fmt.Errorf("bad day %q: %w", day, err)
	}
	return catalog.PartitionKey{StorageNamespace: ns, UTCDay: parsed.Format("2006-01-02")}, nil
}

func e2eTarget() []PartitionRef {
	return []PartitionRef{{StorageNamespace: "inst:147", UTCDay: "20260601", Owner: "hot"}}
}

// e2eNow 让分区（2026-06-01 结束 + 热层窗口）落在窗口内。
//
// 热层窗口取**最长**档（DefaultPolicy 的 ERROR = 90d），不是 30d：
// 按 30d 断言会让分区一直 not_due，测试看似通过（结果同为「没删」），
// 实际从未走到本用例要验证的分支。
func e2eNow() time.Time { return time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC) }

// TestE2E_ArchiveReclaimMarksCatalogArchived 全链路成功：Catalog 变为 ARCHIVED 且已回收。
func TestE2E_ArchiveReclaimMarksCatalogArchived(t *testing.T) {
	cat, key := e2eCatalog(t)
	ex := NewArchiveExecutor(threeGatePolicy(), nil)
	ex.SetArchiveExporter(&fakeExporter{})
	ex.SetArchiveAuthority(&e2eAuthority{cat: cat})
	ex.SetRemover(&fakeRemover{})

	out := ex.ExecutePlanned(context.Background(), e2eNow(), threeGatePolicy().PlanArchive(e2eNow(), e2eTarget(), false, true))
	require.Len(t, out, 1)
	require.Equal(t, ArchiveCopied, out[0].Result, "err=%v", out[0].Err)

	rec, ok := cat.Get(key)
	require.True(t, ok)
	require.Equal(t, catalog.OwnerArchive, rec.Owner, "回收成功后权威必须是 ARCHIVED")
	require.NotNil(t, rec.ArchiveEvidence)
	require.True(t, rec.ArchiveEvidence.HotReclaimed)
	require.False(t, rec.ArchiveEvidence.HotReclaimedAt.IsZero())
	// 审计四要素齐全：何时 / 去哪 / 校验和 / 覆盖范围。
	require.False(t, rec.ArchiveEvidence.ArchivedAt.IsZero())
	require.Equal(t, "local", rec.ArchiveEvidence.ProviderKind)
	require.Equal(t, "inst:147/20260601/g1", rec.ArchiveEvidence.ObjectPrefix)
	require.Contains(t, rec.ArchiveEvidence.ManifestChecksum, "sha256:")
	require.Equal(t, 1, rec.ArchiveEvidence.Coverage.ObjectCount)
	require.True(t, rec.ArchiveEvidence.Coverage.Complete)
	// 查询面：热层目录不再可见，且不再产出可读 range。
	require.False(t, cat.IsQueryDirVisible(key, "20260601"))
	require.Empty(t, cat.PendingArchiveReclaims())
}

// TestE2E_ReclaimFailureRollsBackToHot 回收失败：权威退回 HOT 且查询能力恢复。
func TestE2E_ReclaimFailureRollsBackToHot(t *testing.T) {
	cat, key := e2eCatalog(t)
	ex := NewArchiveExecutor(threeGatePolicy(), nil)
	ex.SetArchiveExporter(&fakeExporter{})
	ex.SetArchiveAuthority(&e2eAuthority{cat: cat})
	ex.SetRemover(&fakeRemover{failWith: errors.New("目录被占用（模拟）")})

	out := ex.ExecutePlanned(context.Background(), e2eNow(), threeGatePolicy().PlanArchive(e2eNow(), e2eTarget(), false, true))
	require.Equal(t, ArchiveCopiedKeptOriginal, out[0].Result)
	require.Contains(t, out[0].Err.Error(), "已回滚为热层权威")

	rec, ok := cat.Get(key)
	require.True(t, ok)
	require.Equal(t, catalog.OwnerHot, rec.Owner, "回收失败后权威必须退回 HOT")
	require.Equal(t, "20260601", rec.OwnerDirID)
	require.NotNil(t, rec.PublishedProjection, "回滚必须把查询能力原样还回去")
	require.Equal(t, "20260601", rec.PublishedProjection.QueryLocationDirID)
	require.False(t, rec.WriteRoute.Frozen, "回滚后必须重新接受写入")
	require.Equal(t, 1, rec.ArchiveEvidence.RollbackCount)
	require.False(t, rec.ArchiveEvidence.HotReclaimed)
	// 热层目录重新可见（数据仍在热层）。
	require.True(t, cat.IsQueryDirVisible(key, "20260601"))
	// 观测面：没有残留的「已归档未回收」。
	require.Empty(t, cat.PendingArchiveReclaims())
}

// TestE2E_IdempotentAcrossRounds 重跑幂等：不重复归档、不产生半删状态。
//
// 场景：第一轮回收成功 ⇒ 第二轮执行器不应再对它动手（已不是候选），
// 且 Catalog 里归档证据不被重复追加。
func TestE2E_IdempotentAcrossRounds(t *testing.T) {
	cat, key := e2eCatalog(t)
	auth := &e2eAuthority{cat: cat}
	ex := NewArchiveExecutor(threeGatePolicy(), nil)
	ex.SetArchiveExporter(&fakeExporter{})
	ex.SetArchiveAuthority(auth)
	rm := &fakeRemover{}
	ex.SetRemover(rm)

	plan := threeGatePolicy().PlanArchive(e2eNow(), e2eTarget(), false, true)
	out := ex.ExecutePlanned(context.Background(), e2eNow(), plan)
	require.Equal(t, ArchiveCopied, out[0].Result)

	first, _ := cat.Get(key)
	seqAfterFirst := first.JournalLastSeq

	// 重跑同一计划（模拟重试/补跑）：MarkArchived 幂等 + 删除再成功一次也不改变权威。
	out2 := ex.ExecutePlanned(context.Background(), e2eNow().Add(time.Hour), plan)
	require.Equal(t, ArchiveCopied, out2[0].Result)
	second, _ := cat.Get(key)
	require.Equal(t, catalog.OwnerArchive, second.Owner)
	require.Equal(t, first.ArchiveEvidence.ArchivedAt, second.ArchiveEvidence.ArchivedAt,
		"重跑不得改写归档时刻（否则审计时间线会漂移）")
	require.True(t, second.ArchiveEvidence.HotReclaimed)
	require.Positive(t, second.JournalLastSeq-seqAfterFirst,
		"回收标记是一次真实提交（幂等的是归档迁移本身，不是整个流程）")
}

// TestE2E_ReexportAfterRollbackWorks 回滚后下一轮可以重新归档（不被历史证据卡死）。
func TestE2E_ReexportAfterRollbackWorks(t *testing.T) {
	cat, key := e2eCatalog(t)
	ex := NewArchiveExecutor(threeGatePolicy(), nil)
	ex.SetArchiveExporter(&fakeExporter{})
	ex.SetArchiveAuthority(&e2eAuthority{cat: cat})

	failing := &fakeRemover{failWith: errors.New("临时故障（模拟）")}
	ex.SetRemover(failing)
	plan := threeGatePolicy().PlanArchive(e2eNow(), e2eTarget(), false, true)
	require.Equal(t, ArchiveCopiedKeptOriginal, ex.ExecutePlanned(context.Background(), e2eNow(), plan)[0].Result)

	// 故障恢复后重跑：应能真的完成归档（历史 RollbackCount 保留为审计）。
	ex.SetRemover(&fakeRemover{})
	out := ex.ExecutePlanned(context.Background(), e2eNow().Add(time.Hour), plan)
	require.Equal(t, ArchiveCopied, out[0].Result, "err=%v", out[0].Err)

	rec, _ := cat.Get(key)
	require.Equal(t, catalog.OwnerArchive, rec.Owner)
	require.True(t, rec.ArchiveEvidence.HotReclaimed)
	require.Equal(t, 1, rec.ArchiveEvidence.RollbackCount, "历史回滚计数必须延续，不能因新一轮归档归零")
}

// TestE2E_ExportFailureReleasesFreeze 导出/校验失败必须解除冻结。
//
// 这是本轮自查发现的真实缺陷的回归：冻结由「导出前」建立、横跨整段流程；
// 若某条退出路径忘了解冻，该物理日会**永久**拒绝写入——现场只表现为
// 「这一天日志写不进去」，完全看不出这是归档流程留下的闸。
func TestE2E_ExportFailureReleasesFreeze(t *testing.T) {
	cat, key := e2eCatalog(t)
	ex := NewArchiveExecutor(threeGatePolicy(), nil)
	ex.SetArchiveExporter(&fakeExporter{failWith: errors.New("归档目的地不可达（模拟）")})
	ex.SetArchiveAuthority(&e2eAuthority{cat: cat})
	ex.SetRemover(&fakeRemover{})

	out := ex.ExecutePlanned(context.Background(), e2eNow(), threeGatePolicy().PlanArchive(e2eNow(), e2eTarget(), false, true))
	require.Equal(t, ArchiveKeptOriginal, out[0].Result)

	require.False(t, cat.ArchivedDay(key.UTCDay), "导出失败后必须解除冻结")
	target, err := cat.WriteAdmission(key)
	require.NoError(t, err, "解冻后该日必须重新接受写入")
	require.Equal(t, catalog.OwnerHot, target.Owner)
	// 权威态完全未动。
	rec, _ := cat.Get(key)
	require.Nil(t, rec.ArchiveEvidence)
}

// TestE2E_MarkArchivedFailureReleasesFreeze 权威态迁移失败也必须解除冻结。
func TestE2E_MarkArchivedFailureReleasesFreeze(t *testing.T) {
	cat, key := e2eCatalog(t)
	failing := &failingMarkAuthority{e2eAuthority: &e2eAuthority{cat: cat}}
	ex := NewArchiveExecutor(threeGatePolicy(), nil)
	ex.SetArchiveExporter(&fakeExporter{})
	ex.SetArchiveAuthority(failing)
	rm := &fakeRemover{}
	ex.SetRemover(rm)

	out := ex.ExecutePlanned(context.Background(), e2eNow(), threeGatePolicy().PlanArchive(e2eNow(), e2eTarget(), false, true))
	require.Equal(t, ArchiveCopiedKeptOriginal, out[0].Result)
	require.Empty(t, rm.calls, "权威态迁移失败时绝不能删")

	require.False(t, cat.ArchivedDay(key.UTCDay), "权威态迁移失败后必须解除冻结")
	_, err := cat.WriteAdmission(key)
	require.NoError(t, err, "解冻后该日必须重新接受写入")
	rec, _ := cat.Get(key)
	require.Equal(t, catalog.OwnerHot, rec.Owner)
}

// failingMarkAuthority 只让 MarkArchived 失败，用于验证冻结在失败路径上的解除。
type failingMarkAuthority struct {
	*e2eAuthority
}

func (a *failingMarkAuthority) MarkArchived(context.Context, string, string, ArchiveEvidence) error {
	return errors.New("journal 不可写（模拟）")
}

// 注意区分两件事：
//   - **权威态**（Owner/证据/投影）绝对不许变——这是数据安全的核心；
//   - 物理日冻结会经历「冻结→解冻」两次提交（导出必须读一致快照），
//     但解冻后该日重新接受写入，净效果为零。故这里不断言 journal 零提交，
//     而断言「权威态字段与写入能力都回到原样」。
func TestE2E_GatesClosedNeverTouchesCatalog(t *testing.T) {
	cat, key := e2eCatalog(t)

	p := archiveCopyPolicy() // 只开导出开关，三闸默认全关
	ex := NewArchiveExecutor(p, nil)
	ex.SetArchiveExporter(&fakeExporter{})
	ex.SetArchiveAuthority(&e2eAuthority{cat: cat})
	rm := &fakeRemover{}
	ex.SetRemover(rm)

	out := ex.ExecutePlanned(context.Background(), e2eNow(), p.PlanArchive(e2eNow(), e2eTarget(), false, true))
	require.Equal(t, ArchiveCopiedKeptOriginal, out[0].Result)
	require.Empty(t, rm.calls, "三闸未全开时绝不能删")

	after, _ := cat.Get(key)
	require.Equal(t, catalog.OwnerHot, after.Owner, "三闸未全开时权威态必须保持 HOT")
	require.Nil(t, after.ArchiveEvidence, "三闸未全开时不得写归档证据")
	require.NotNil(t, after.PublishedProjection, "投影不得被清空")
	require.Equal(t, "20260601", after.OwnerDirID)
	// 冻结必须已被解除：否则该物理日会永久拒绝写入（运维只看到「日志写不进去」）。
	require.False(t, cat.ArchivedDay(key.UTCDay), "导出后未归档时必须解除归档冻结")
	require.False(t, after.WriteRoute.Frozen, "写入路由必须回到可写状态")
	target, err := cat.WriteAdmission(key)
	require.NoError(t, err, "解冻后该日必须重新接受写入")
	require.Equal(t, catalog.OwnerHot, target.Owner)
}
