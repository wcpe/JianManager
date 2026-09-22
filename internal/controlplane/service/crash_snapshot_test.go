package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/crashdiag"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newCrashTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{}, &model.Node{}, &model.InstanceCrashSnapshot{}, &model.InstanceCrashStat{},
		&model.ProcessMetricSnapshot{},
	))
	return db
}

func seedCrashInstance(t *testing.T, db *gorm.DB, name string) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		NodeID: 1, Name: name, Type: model.InstanceTypeGeneric,
		Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "./beacon",
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// TestCrashTrend_AggregatesByDayAndCause 趋势按天/根因聚合，且 Top 同类正确。
func TestCrashTrend_AggregatesByDayAndCause(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	svc := NewCrashSnapshotService(db)

	now := time.Now().UTC()
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now, "oom", "java.lang.OutOfMemoryError: Java heap space"))
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now, "oom", "java.lang.OutOfMemoryError: Java heap space"))
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now, "port_in_use", "java.net.BindException: Address already in use"))
	// 窗口外（40 天前）不应计入 30 天趋势。
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now.AddDate(0, 0, -40), "segfault", "SIGSEGV"))

	trend, err := svc.TrendByInstance(inst.ID, 30)
	require.NoError(t, err)
	assert.Equal(t, 3, trend.Total)
	require.NotEmpty(t, trend.ByRootCause)
	assert.Equal(t, "oom", trend.ByRootCause[0].RootCause)
	assert.Equal(t, 2, trend.ByRootCause[0].Count)
	require.Len(t, trend.TopSignatures, 2)
	assert.Equal(t, 2, trend.TopSignatures[0].Count)

	// 365 天上限内仍不含 40 天前的那条之外的增长；窗口放宽到 365 天才包含。
	wide, err := svc.TrendByInstance(inst.ID, 365)
	require.NoError(t, err)
	assert.Equal(t, 4, wide.Total)
}

// TestCrashTrend_AggregatesSameDaySameCauseAcrossSignatures 行源唯一键含**指纹**，
// 故「同一天同一根因」天然有多行；趋势点必须按 (天 × 根因) 求和成**一个点**。
//
// 这是本测试存在的唯一理由：原先 `Points` 是逐行 append，测试名写着「按天聚合」却
// 全文没有一条 `Points` 断言，于是失真长期隐身。此处对点数、点内计数、点序与
// 与 `ByRootCause` 的一致性逐一断言。
func TestCrashTrend_AggregatesSameDaySameCauseAcrossSignatures(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	svc := NewCrashSnapshotService(db)

	day := time.Now().UTC()
	// 同一天、同一根因 port_in_use，但**三个不同指纹**（真实表现：绑定失败的具体行不同）。
	// UpsertCrashStat 按 (实例, 天, 根因, 指纹) 唯一，故这里落 3 行。
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, day, "port_in_use", "java.net.BindException: Address already in use"))
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, day, "port_in_use", "BindException: Cannot assign requested address"))
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, day, "port_in_use", "BindException: Permission denied"))
	// 之后又崩两次，重复命中第一个指纹（同一行 count 累加到 2）。
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, day, "port_in_use", "java.net.BindException: Address already in use"))
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, day, "port_in_use", "java.net.BindException: Address already in use"))

	// 前置确认：行源确实是「多行」（3 个指纹），否则本测试测不到聚合。
	var rows int64
	require.NoError(t, db.Model(&model.InstanceCrashStat{}).Where("instance_id = ?", inst.ID).Count(&rows).Error)
	require.EqualValues(t, 3, rows, "行源应按指纹分成 3 行")
	var rowSum int
	require.NoError(t, db.Model(&model.InstanceCrashStat{}).Where("instance_id = ?", inst.ID).
		Select("COALESCE(SUM(count),0)").Scan(&rowSum).Error)
	require.Equal(t, 5, rowSum, "行源合计应为 5 次崩溃")

	trend, err := svc.TrendByInstance(inst.ID, 30)
	require.NoError(t, err)
	assert.Equal(t, 5, trend.Total)

	// 核心断言：同一天同一根因只产生 1 个点，且 count 为求和后的 5。
	require.Len(t, trend.Points, 1, "同一天同一根因的多次崩溃（多指纹）必须只产生 1 个点")
	assert.Equal(t, day.Format("2006-01-02"), trend.Points[0].Day)
	assert.Equal(t, "port_in_use", trend.Points[0].RootCause)
	assert.Equal(t, 5, trend.Points[0].Count, "点计数必须是该 (天, 根因) 的求和值，不是单行计数")

	// 趋势点合计必须与 Total / ByRootCause 自洽（逐行输出时这两处会互相矛盾）。
	pointSum := 0
	for _, p := range trend.Points {
		pointSum += p.Count
	}
	assert.Equal(t, trend.Total, pointSum, "趋势点合计应等于趋势总数")
	require.Len(t, trend.ByRootCause, 1)
	assert.Equal(t, "port_in_use", trend.ByRootCause[0].RootCause)
	assert.Equal(t, 5, trend.ByRootCause[0].Count, "Top 根因与趋势点必须口径一致")

	// 跨天不合并：换一天再崩一次 → 出现第 2 个点，且按天升序。
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, day.AddDate(0, 0, -1), "port_in_use", "java.net.BindException: Address already in use"))
	trend2, err := svc.TrendByInstance(inst.ID, 30)
	require.NoError(t, err)
	require.Len(t, trend2.Points, 2, "不同天不得合并成一个点")
	assert.Equal(t, day.AddDate(0, 0, -1).Format("2006-01-02"), trend2.Points[0].Day, "趋势点应按天升序")
	assert.Equal(t, day.Format("2006-01-02"), trend2.Points[1].Day)
	assert.Equal(t, 1, trend2.Points[0].Count)
	assert.Equal(t, 5, trend2.Points[1].Count)
}

// TestCrashOverview_TrendAggregatesByDayAndCause 总览趋势同样必须按 (天 × 根因) 聚合
// ——Overview 有独立的循环，与 TrendByInstance 是两处代码，必须各自断言。
func TestCrashOverview_TrendAggregatesByDayAndCause(t *testing.T) {
	db := newCrashTestDB(t)
	a := seedCrashInstance(t, db, "a")
	b := seedCrashInstance(t, db, "b")
	svc := NewCrashSnapshotService(db)

	day := time.Now().UTC()
	// 同一天同一根因、跨两个实例、共 4 个指纹：聚合后必须是 1 个点、count=4。
	for i := 0; i < 3; i++ {
		require.NoError(t, crashdiag.UpsertCrashStat(db, a.ID, day, "port_in_use", "sig-detail-"+string(rune('a'+i))))
	}
	require.NoError(t, crashdiag.UpsertCrashStat(db, b.ID, day, "port_in_use", "sig-detail-d"))
	// 另一根因：同一天但不同根因 → 必须仍是独立的点。
	require.NoError(t, crashdiag.UpsertCrashStat(db, a.ID, day, "oom", "sig-oom"))

	ov, err := svc.Overview(30, nil)
	require.NoError(t, err)
	assert.Equal(t, 5, ov.Total)

	require.Len(t, ov.Trend, 2, "同天同因的多行（多实例多指纹）必须聚合，另一根因另成一点")
	assert.Equal(t, day.Format("2006-01-02"), ov.Trend[0].Day)
	assert.Equal(t, "oom", ov.Trend[0].RootCause, "读入按 (天, 根因) 升序，oom 在 port_in_use 之前")
	assert.Equal(t, 1, ov.Trend[0].Count)
	assert.Equal(t, "port_in_use", ov.Trend[1].RootCause)
	assert.Equal(t, 4, ov.Trend[1].Count)

	pointSum := 0
	for _, p := range ov.Trend {
		pointSum += p.Count
	}
	assert.Equal(t, ov.Total, pointSum, "总览趋势点合计应等于总览总数")
}

// TestCrashTrend_NoStatsReturnsEmptyPoints 无统计时 points 必须为空切片（JSON `[]` 而非 `null`）。
func TestCrashTrend_NoStatsReturnsEmptyPoints(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	svc := NewCrashSnapshotService(db)

	trend, err := svc.TrendByInstance(inst.ID, 30)
	require.NoError(t, err)
	assert.Empty(t, trend.Points)
	assert.NotNil(t, trend.Points, "空趋势应为空切片，保证 JSON 输出 `[]`")

	ov, err := svc.Overview(30, nil)
	require.NoError(t, err)
	assert.Empty(t, ov.Trend)
	assert.NotNil(t, ov.Trend)
}

// TestCrashTrend_InstanceNotFound 实例不存在返回 ErrInstanceNotFound（路由映射 404）。
func TestCrashTrend_InstanceNotFound(t *testing.T) {
	db := newCrashTestDB(t)
	_, err := NewCrashSnapshotService(db).TrendByInstance(999, 30)
	assert.ErrorIs(t, err, ErrInstanceNotFound)
}

// TestCrashOverview_TopCausesAndInstances 平台总览的 Top 根因 / Top 实例。
func TestCrashOverview_TopCausesAndInstances(t *testing.T) {
	db := newCrashTestDB(t)
	a := seedCrashInstance(t, db, "a")
	b := seedCrashInstance(t, db, "b")
	svc := NewCrashSnapshotService(db)
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		require.NoError(t, crashdiag.UpsertCrashStat(db, a.ID, now, "oom", "sig-oom"))
	}
	require.NoError(t, crashdiag.UpsertCrashStat(db, b.ID, now, "permission", "sig-perm"))

	ov, err := svc.Overview(30, nil)
	require.NoError(t, err)
	assert.Equal(t, 4, ov.Total)
	require.NotEmpty(t, ov.TopRootCauses)
	assert.Equal(t, "oom", ov.TopRootCauses[0].RootCause)
	assert.Equal(t, 3, ov.TopRootCauses[0].Count)
	require.Len(t, ov.TopInstances, 2)
	assert.Equal(t, a.ID, ov.TopInstances[0].InstanceID)
	assert.Equal(t, "a", ov.TopInstances[0].InstanceName)
	assert.Equal(t, 3, ov.TopInstances[0].Count)
}

// TestReclassifyAll_BackfillsAndIsIdempotent 历史快照（无分类列）重分类回填 + 重复执行幂等。
func TestReclassifyAll_BackfillsAndIsIdempotent(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	svc := NewCrashSnapshotService(db)
	occurred := time.Now().UTC().Add(-2 * time.Hour)

	// 模拟 FR-470 之前入库的快照：分类列为空、无统计行。
	snap := &model.InstanceCrashSnapshot{
		InstanceID: inst.ID, OccurredAt: occurred, ExitCode: 1,
		TailOutput: "java.lang.OutOfMemoryError: Java heap space",
	}
	require.NoError(t, db.Create(snap).Error)

	res, err := svc.ReclassifyAll()
	require.NoError(t, err)
	assert.Equal(t, 1, res.Scanned)
	assert.Equal(t, 1, res.Updated)
	assert.Equal(t, 1, res.Backfilled)

	var got model.InstanceCrashSnapshot
	require.NoError(t, db.First(&got, snap.ID).Error)
	assert.Equal(t, "oom", got.RootCause)
	assert.NotEmpty(t, got.Signature)
	assert.NotEmpty(t, got.Evidence)

	var stat model.InstanceCrashStat
	require.NoError(t, db.Where("instance_id = ? AND root_cause = ?", inst.ID, "oom").First(&stat).Error)
	assert.Equal(t, 1, stat.Count)

	// 幂等：第二次运行不再变动。
	res2, err := svc.ReclassifyAll()
	require.NoError(t, err)
	assert.Equal(t, 1, res2.Scanned)
	assert.Zero(t, res2.Updated)
	var cnt int
	require.NoError(t, db.Model(&model.InstanceCrashStat{}).Where("instance_id = ?", inst.ID).
		Select("COALESCE(SUM(count),0)").Scan(&cnt).Error)
	assert.Equal(t, 1, cnt, "重复重分类不得重复计数")
}

// TestReclassifyAll_MovesStatBetweenBuckets 根因变化时统计做差量搬移（旧桶减、新桶加）。
func TestReclassifyAll_MovesStatBetweenBuckets(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	svc := NewCrashSnapshotService(db)
	occurred := time.Now().UTC()

	// 旧分类错误地记为 class_not_found，而正文其实是端口占用。
	snap := &model.InstanceCrashSnapshot{
		InstanceID: inst.ID, OccurredAt: occurred, ExitCode: 1,
		TailOutput: "java.net.BindException: Address already in use",
		RootCause:  "class_not_found", Signature: "wrong-sig", Confidence: 0.85,
	}
	require.NoError(t, db.Create(snap).Error)
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, occurred, "class_not_found", "wrong-sig"))

	res, err := svc.ReclassifyAll()
	require.NoError(t, err)
	assert.Equal(t, 1, res.Updated)
	assert.Zero(t, res.Backfilled, "旧分类非空不算回填")

	var oldCount int64
	require.NoError(t, db.Model(&model.InstanceCrashStat{}).
		Where("instance_id = ? AND root_cause = ?", inst.ID, "class_not_found").Count(&oldCount).Error)
	assert.Zero(t, oldCount, "旧桶应被搬空并删除行")

	var stat model.InstanceCrashStat
	require.NoError(t, db.Where("instance_id = ? AND root_cause = ?", inst.ID, "port_in_use").First(&stat).Error)
	assert.Equal(t, 1, stat.Count)

	// 总量守恒。
	var total int
	require.NoError(t, db.Model(&model.InstanceCrashStat{}).Where("instance_id = ?", inst.ID).
		Select("COALESCE(SUM(count),0)").Scan(&total).Error)
	assert.Equal(t, 1, total)
}

// TestCrashCorrelation_WindowAndOOM 资源关联：窗口内的 RSS 峰值参与「逼近上限」判定。
func TestCrashCorrelation_WindowAndOOM(t *testing.T) {
	db := newCrashTestDB(t)
	require.NoError(t, db.Create(&model.Node{Name: "n1", Host: "h", Secret: "s"}).Error)
	inst := seedCrashInstance(t, db, "smp")
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("mem_limit_mb", 1024).Error)

	crashAt := time.Now().UTC()
	// 窗口内：975MB（≈ 1024MB 的 95%）→ 逼近上限。
	require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
		NodeUUID: "node-uuid", InstanceUUID: inst.UUID, PID: 1,
		RSSBytes: 975 * 1024 * 1024, SampledAt: crashAt.Add(-time.Minute),
	}).Error)
	// 窗口外（10 分钟前）：不应参与。
	require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
		NodeUUID: "node-uuid", InstanceUUID: inst.UUID, PID: 1,
		RSSBytes: 4096 * 1024 * 1024, SampledAt: crashAt.Add(-10 * time.Minute),
	}).Error)

	svc := NewCrashCorrelationService(db)
	corr, err := svc.CorrelateInstance(inst.ID, crashAt)
	require.NoError(t, err)
	assert.Equal(t, int64(975*1024*1024), corr.RSSAtCrash)
	assert.Equal(t, int64(1024), corr.MemLimitMB)
	assert.True(t, corr.NearOOM)
	assert.Zero(t, corr.HeapUsedMax, "未注入时序源时堆数据缺省为 0")
	assert.NotEmpty(t, corr.GCNote)
}

// TestCrashCorrelation_NoSamples 无样本时降级不报错（spec §3 单测：无数据降级）。
func TestCrashCorrelation_NoSamples(t *testing.T) {
	db := newCrashTestDB(t)
	require.NoError(t, db.Create(&model.Node{Name: "n1", Host: "h", Secret: "s"}).Error)
	inst := seedCrashInstance(t, db, "smp")

	corr, err := NewCrashCorrelationService(db).CorrelateInstance(inst.ID, time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, corr.NearOOM)
	assert.Zero(t, corr.RSSAtCrash)
	assert.Equal(t, "崩溃窗口内无进程资源样本", corr.Note)
}

// TestCrashCorrelation_InstanceNotFound 实例不存在 → ErrInstanceNotFound。
func TestCrashCorrelation_InstanceNotFound(t *testing.T) {
	db := newCrashTestDB(t)
	_, err := NewCrashCorrelationService(db).CorrelateInstance(12345, time.Now())
	assert.ErrorIs(t, err, ErrInstanceNotFound)
}

// TestCrashSnapshotService_CorrelationGate 未装配关联服务时不附关联段（CorrelationEnabled=false）。
func TestCrashSnapshotService_CorrelationGate(t *testing.T) {
	db := newCrashTestDB(t)
	svc := NewCrashSnapshotService(db)
	assert.False(t, svc.CorrelationEnabled())
	corr, err := svc.Correlate(1, time.Now())
	require.NoError(t, err)
	assert.Equal(t, CrashCorrelation{}, corr)

	svc.SetCorrelation(NewCrashCorrelationService(db))
	assert.True(t, svc.CorrelationEnabled())
}

// TestPruneCrashStats_RetentionWindow 统计保留窗口按天裁剪（crash.stat_retention_days，FR-470 §5）。
func TestPruneCrashStats_RetentionWindow(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	svc := NewCrashSnapshotService(db)
	svc.SetSettingsReader(stubSettings{SettingKeyCrashStatRetentionDays: "30"})

	now := time.Now().UTC()
	// 窗口内（今天）与窗口外（60 天前）。
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now, "oom", "sig-a"))
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now.AddDate(0, 0, -60), "oom", "sig-b"))
	// 直接写一条更早的桶（UpsertCrashStat 按传入时刻定桶）。
	require.NoError(t, db.Create(&model.InstanceCrashStat{
		InstanceID: inst.ID, BucketDay: now.AddDate(0, 0, -45).Format("2006-01-02"),
		RootCause: "segfault", Signature: "sig-c", Count: 3,
	}).Error)

	deleted := svc.pruneCrashStatsOnce()
	require.Equal(t, 2, deleted, "两条超期统计应被裁剪")

	var remaining int64
	require.NoError(t, db.Model(&model.InstanceCrashStat{}).Count(&remaining).Error)
	require.EqualValues(t, 1, remaining, "窗口内统计保留")
}
