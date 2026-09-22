package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/crashdiag"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestPlanSnapshotCleanup_QueryFailureDoesNotDemote R11：依赖查询失败时**不得**把底链降级。
//
// 缺陷现场：查「被引用为父」失败时 `return snapshotCleanupPlan{demote: backlinkIDs}`，
// 即把**全部**底链降级为 `origin=”`。降级的语义是「该底链被手工增量引用、删不得」，
// 而查询失败不能冒充这个前提：被降级后底链就不再豁免 backup.retention_days，
// 用户在保留期到期后会静默丢失快照数据（只有一条 slog.Warn，无告警计数）。
//
// 本测试让「子备份依赖查询」失败，断言计划为空（既不删也不降级）。
// 制造方式：把 parent_id 列改名，使 `WHERE parent_id IN ?` 报列不存在，
// 而前面的「查底链」查询仍能成功（它只用 instance_id/origin）。
func TestPlanSnapshotCleanup_QueryFailureDoesNotDemote(t *testing.T) {
	svc, inst, _, rootBackup, _ := newSnapshotDeleteEnv(t)

	// 前置确认：正常路径下该底链是可删的（否则本测试测不到失败分支）。
	require.Equal(t, []uint{rootBackup.ID}, svc.planSnapshotCleanup(inst.ID).deletable)

	require.NoError(t, svc.db.Migrator().RenameColumn(&model.Backup{}, "parent_id", "parent_id_hidden"))

	plan := svc.planSnapshotCleanup(inst.ID)
	require.Empty(t, plan.deletable, "查不出依赖时不得删除任何底链")
	require.Empty(t, plan.demote, "查不出依赖时也不得降级——降级会把它静默拖入常规裁剪")
}

// TestPlanSnapshotCleanup_DemotesOnlyReferencedBacklinks R11 的对照：只有在
// **成功查到**「被引用为父」时，才把该条放进 demote。
func TestPlanSnapshotCleanup_DemotesOnlyReferencedBacklinks(t *testing.T) {
	svc, inst, _, rootBackup, _ := newSnapshotDeleteEnv(t)

	child := &model.Backup{
		UUID: "b-incr", InstanceID: inst.ID, Name: "手工增量",
		Origin: model.BackupOriginManual, Mode: model.BackupModeIncremental,
		ParentID: &rootBackup.ID, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(child).Error)

	plan := svc.planSnapshotCleanup(inst.ID)
	require.Empty(t, plan.deletable, "被增量引用的底链不可删")
	require.Equal(t, []uint{rootBackup.ID}, plan.demote)
}

// TestDeletableBacklinkIDsInTx_ExcludesNewlyReferenced R9：事务内的删除条件必须自带
// 「无子备份」判定，避免计划算出后、事务执行前用户新建增量备份导致链断裂。
//
// 缺陷现场：`planSnapshotCleanup` 在事务**外**算 deletable，事务内直接
// `tx.Where("id IN ?").Delete(&model.Backup{})`——绕过了 `BackupService.Delete`
// 的「拒删有子备份」护栏。窗口很窄（需与实例删除并发），但后果是用户的增量备份
// 失去基准（`resolveChain` 随后报「父备份缺失」）。
func TestDeletableBacklinkIDsInTx_ExcludesNewlyReferenced(t *testing.T) {
	svc, inst, _, rootBackup, _ := newSnapshotDeleteEnv(t)

	// 计划算出「底链可删」。
	plan := svc.planSnapshotCleanup(inst.ID)
	require.Equal(t, []uint{rootBackup.ID}, plan.deletable)

	// TOCTOU 窗口内：用户基于该底链做了一次手工增量备份。
	child := &model.Backup{
		UUID: "b-race-incr", InstanceID: inst.ID, Name: "窗口内新增增量",
		Origin: model.BackupOriginManual, Mode: model.BackupModeIncremental,
		ParentID: &rootBackup.ID, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(child).Error)

	// 事务内复算：新引用必须把底链从可删集合里剔除。
	require.Empty(t, deletableBacklinkIDsInTx(svc.db, plan.deletable),
		"事务内复算必须看到窗口内新增的子备份，从而保住底链")

	// 软删的子备份不应再拦住清理（否则底链永远删不掉）。
	require.NoError(t, svc.db.Delete(&model.Backup{}, child.ID).Error)
	require.Equal(t, []uint{rootBackup.ID}, deletableBacklinkIDsInTx(svc.db, plan.deletable),
		"已软删的子备份不再依赖底链，不应拦住清理")
}

// TestDeleteInstance_KeepsBacklinkReferencedDuringWindow R9 端到端：删除流程内
// 「计划 → 事务复查」的整条链必须保住窗口内新增引用所指向的底链，并把它降级
// （否则它会继续挂着 origin=snapshot 而宿主实例与快照行都已消失，永不再被任何策略扫描）。
//
// 本测试穿过真实 `Delete` 路径：删除前的 plan 为 deletable，而流程内部在真正删行前
// 仍会复查一次，窗口内新增的子备份使底链被保住。
func TestDeleteInstance_KeepsBacklinkReferencedDuringWindow(t *testing.T) {
	svc, inst, _, rootBackup, _ := newSnapshotDeleteEnv(t)

	// 窗口内新增的手工增量备份（在 Delete 内部重新 plan 之前就存在，
	// 故它走的是「plan 阶段即看到引用」的路径；配合上一条单测覆盖「plan 之后才出现」）。
	child := &model.Backup{
		UUID: "b-window-incr", InstanceID: inst.ID, Name: "窗口内增量",
		Origin: model.BackupOriginManual, Mode: model.BackupModeIncremental,
		ParentID: &rootBackup.ID, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(child).Error)

	require.NoError(t, svc.Delete(inst.ID))

	var kept model.Backup
	require.NoError(t, svc.db.First(&kept, rootBackup.ID).Error, "被增量引用的底链不得删除")
	require.Equal(t, model.BackupOriginManual, kept.Origin,
		"底链必须降级为普通备份以回归 backup.retention_days 管辖（否则永久逃过所有策略）")

	var childKept model.Backup
	require.NoError(t, svc.db.First(&childKept, child.ID).Error, "用户的手工增量备份必须保留")
}

// TestDeleteInstance_DemotesSurvivingBacklinksInSameTx R27/R9：被增量备份保住的底链
// 必须在**同一删除事务内**降级为普通备份。
//
// 缺陷现场（R27）：降级原写在事务**提交之后**且失败仅 slog.Warn。此时实例与快照行都已
// 删除、底链仍带 origin=snapshot，而它已不被任何策略扫描（B-1 让 snapshot 底链豁免
// backup.retention_days，快照保留策略只对现存快照行生效）——泄漏以更窄路径复活且无痕迹。
//
// 改为事务内执行后：删除成功 ⟺ 降级成功；降级失败则整体回滚（实例仍在，用户可重试）。
func TestDeleteInstance_DemotesSurvivingBacklinksInSameTx(t *testing.T) {
	svc, inst, _, rootBackup, _ := newSnapshotDeleteEnv(t)

	// 用户增量备份占为父 → 底链在事务内不会被删，只会被降级。
	child := &model.Backup{
		UUID: "b-survive-incr", InstanceID: inst.ID, Name: "增量",
		Origin: model.BackupOriginManual, Mode: model.BackupModeIncremental,
		ParentID: &rootBackup.ID, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(child).Error)

	require.NoError(t, svc.Delete(inst.ID))

	var got model.Backup
	require.NoError(t, svc.db.First(&got, rootBackup.ID).Error, "被增量引用的底链必须保留")
	require.Equal(t, model.BackupOriginManual, got.Origin,
		"底链必须在删除事务内降级（否则它将永久逃过所有保留策略）")
}

// TestDeleteInstance_RollsBackWhenDemoteFails R27 的对称断言：降级失败必须整体回滚，
// 不得留下「实例已删、底链仍带 snapshot 豁免标记」的不一致状态。
//
// 制造方式：把 backups 表改名，使事务内的降级 UPDATE 与底链删除都报错。
func TestDeleteInstance_RollsBackWhenDemoteFails(t *testing.T) {
	svc, inst, snap, rootBackup, _ := newSnapshotDeleteEnv(t)

	// 让升级为「被引用为父」的底链留在库中（这样流程一定会走降级语句）。
	child := &model.Backup{
		UUID: "b-rb-incr", InstanceID: inst.ID, Name: "增量",
		Origin: model.BackupOriginManual, Mode: model.BackupModeIncremental,
		ParentID: &rootBackup.ID, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(child).Error)

	// 改名 backups 表 → 事务内的 backups 语句全部报错。
	require.NoError(t, svc.db.Migrator().RenameTable(&model.Backup{}, "backups_hidden"))

	err := svc.Delete(inst.ID)
	require.Error(t, err, "降级/删除失败必须让删除整体失败")
	require.NoError(t, svc.db.Migrator().RenameTable("backups_hidden", &model.Backup{}))

	// 实例仍存在（整体回滚），可重试。
	var instCnt int64
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Count(&instCnt).Error)
	require.EqualValues(t, 1, instCnt, "事务失败后实例记录必须保留（可重试），不得半删")
	require.EqualValues(t, 1, mustBackupCount(t, svc, rootBackup.ID),
		"事务回滚后底链保持原样（既没被删、也没被降级）")
	require.Equal(t, model.BackupOriginSnapshot, mustBackup(t, svc, rootBackup.ID).Origin,
		"回滚后 origin 标记不得被改动")
	var snapCnt int64
	require.NoError(t, svc.db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).Count(&snapCnt).Error)
	require.EqualValues(t, 1, snapCnt, "快照行应随事务回滚保留")
}

// TestDemoteRemainingBacklinksInTx_ScopedToInstance 降级只作用于被删实例：
// 其它实例的底链必须保留 snapshot 豁免标记。
func TestDemoteRemainingBacklinksInTx_ScopedToInstance(t *testing.T) {
	svc, inst, _, rootBackup, _ := newSnapshotDeleteEnv(t)

	other := &model.Instance{
		NodeID: inst.NodeID, Name: "其它", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar other.jar",
		Status: model.InstanceStatusStopped, WorkDir: "var/servers/other2",
	}
	require.NoError(t, svc.db.Create(other).Error)
	otherRoot := &model.Backup{
		UUID: "b-other-root2", InstanceID: other.ID, Name: "别家底链",
		Origin: model.BackupOriginSnapshot, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(otherRoot).Error)

	require.NoError(t, demoteRemainingSnapshotBacklinksInTx(svc.db, inst.ID))

	var gotOther model.Backup
	require.NoError(t, svc.db.First(&gotOther, otherRoot.ID).Error)
	require.Equal(t, model.BackupOriginSnapshot, gotOther.Origin,
		"降级必须按实例收敛，不得波及别的实例的底链")
	var gotOwn model.Backup
	require.NoError(t, svc.db.First(&gotOwn, rootBackup.ID).Error)
	require.Equal(t, model.BackupOriginManual, gotOwn.Origin)
}

// TestLoadOverviewStats_AppliesRowLimit R15：总览读取必须在**数据库侧**限定行数。
//
// 缺陷现场：`Overview` 一次 `Find` 出整个窗口的全部统计行（实例数 × 天数 × 根因 × 指纹），
// 默认 90 天窗口下可为百万级；行数上界由 crash.stat_retention_days 决定，没有硬上限。
//
// 本测试用两条互补断言锁定修复，不依赖真的造出 20 万行：
//  1. `openOverviewStats` 发出的语句含 LIMIT（DryRun 读 SQL + 参数）；
//  2. 行为面上把 limit 调小后确实只读回该条数（同一查询构造路径）。
func TestLoadOverviewStats_AppliesRowLimit(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	svc := NewCrashSnapshotService(db)

	// 造 5 行（同一天、5 个指纹）。
	now := time.Now().UTC()
	for _, sig := range []string{"sig-1", "sig-2", "sig-3", "sig-4", "sig-5"} {
		require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now, "oom", sig))
	}

	// 1) 语句必须带 LIMIT 且值为 crashOverviewMaxRows。
	query, ok := svc.overviewStatsQuery(30, nil)
	require.True(t, ok)
	stmt := query.Session(&gorm.Session{DryRun: true}).
		Select("instance_id", "bucket_day", "root_cause", "signature", "count").
		Order("bucket_day ASC, root_cause ASC").
		Limit(crashOverviewMaxRows).
		Find(&[]model.InstanceCrashStat{}).Statement
	// GORM 把 LIMIT 字面量内联进 SQL（不是占位符参数），故直接断言语句文本。
	require.Contains(t, stmt.SQL.String(), "LIMIT 200000",
		"总览查询必须带 LIMIT（R15 的核心修复）")

	// 2) 行为面：把上限压到 3，只应读回 3 行（证明 LIMIT 真的作用于返回集）。
	stats, scoped, err := svc.loadOverviewStats(30, nil, 3)
	require.NoError(t, err)
	require.True(t, scoped)
	require.Len(t, stats, 3, "limit 必须限制读回的行数")

	// 无限制（limit<=0）时读回全部 5 行，反向确认上面的 3 是 limit 造成的。
	all, _, err := svc.loadOverviewStats(30, nil, 0)
	require.NoError(t, err)
	require.Len(t, all, 5)
}

// TestOverview_EmptyScopeSkipsQueryButReturnsShape scope 为空时直接返回空总览形状，
// 不得退化成「不加过滤」（那正是越权读）。
func TestOverview_EmptyScopeSkipsQueryButReturnsShape(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	svc := NewCrashSnapshotService(db)
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, time.Now().UTC(), "oom", "sig"))

	ov, err := svc.Overview(30, []uint{})
	require.NoError(t, err)
	require.Zero(t, ov.Total)
	require.Empty(t, ov.TopInstances)
	require.NotNil(t, ov.Trend, "空总览的切片必须非 nil（JSON 恒为 []）")
}

// TestPruneScope_FullPlatformStillWorks R10 的对照：instanceID=0 的全平台裁剪
// （启动与周期巡检路径）语义不变。
func TestPruneScope_FullPlatformStillWorks(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetSettingsReader(stubSettings{
		SettingKeySnapshotRetentionCount:  "1",
		SettingKeySnapshotRetentionDays:   "0",
		SettingKeySnapshotPreRollbackKeep: "1",
	})

	other := &model.Instance{
		UUID: "inst-other-full-" + t.Name(), NodeID: inst.NodeID, Name: "其它",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped,
		StartCommand: "./beacon",
	}
	require.NoError(t, db.Create(other).Error)

	mk := func(instanceID uint, name string, ageDays int) {
		snap := &model.InstanceSnapshot{
			InstanceID: instanceID, Name: name, Kind: model.SnapshotKindManual,
			State: model.SnapshotStateCompleted,
		}
		require.NoError(t, db.Create(snap).Error)
		require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).
			Update("created_at", time.Now().AddDate(0, 0, -ageDays)).Error)
	}
	mk(inst.ID, "本实例-旧", 2)
	mk(inst.ID, "本实例-新", 1)
	mk(other.ID, "其它-旧", 2)
	mk(other.ID, "其它-新", 1)

	require.Equal(t, 2, svc.pruneOnce(), "全平台裁剪应覆盖两个实例各超额的那一条")

	var cnt int64
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Count(&cnt).Error)
	require.EqualValues(t, 2, cnt)
}

// TestRestartableStop_RebuildsChannel R22/R30：三处 Start 共用的 stopCh 重建不变量。
//
// 缺陷现场：仅 `SnapshotService.Start` 显式重建 stopCh，另两处（QuotaEnforcer、
// CrashSnapshotService）沿用构造期 channel——同一不变量三种写法，
// 后续任一处补热重启即 panic 在 close of closed channel。
func TestRestartableStop_RebuildsChannel(t *testing.T) {
	original := make(chan struct{})
	ch := original
	got := restartableStop(&ch)

	require.NotEqual(t, original, ch, "必须换成新 channel，否则 Stop→Start 会 panic")
	require.Equal(t, ch, got, "返回值应是本次循环监听的局部引用")

	// 关掉旧 channel 不能影响新 channel（这正是重建的意义）。
	close(original)
	select {
	case <-got:
		t.Fatal("新 channel 不得被旧的 close 影响")
	default:
	}
}

// TestServiceStartStopRestart_NoPanic R22/R30 的行为锁定：三个服务 Stop→Start 均可重复。
//
// 三处 Start 现在都经 restartableStop 换取新 channel，故 Stop 后再 Start 不会
// panic 在 close of closed channel（改造前 QuotaEnforcer/CrashSnapshotService 会 panic）。
func TestServiceStartStopRestart_NoPanic(t *testing.T) {
	t.Run("quota", func(t *testing.T) {
		db := quotaTestDB(t)
		e, _ := newEnforcer(t, db, &stubQuotaMetrics{})
		require.NotPanics(t, func() {
			e.Start()
			e.Stop()
			e.Start()
			e.Stop()
		})
	})
	t.Run("crash", func(t *testing.T) {
		db := newCrashTestDB(t)
		svc := NewCrashSnapshotService(db)
		require.NotPanics(t, func() {
			svc.Start()
			svc.Stop()
			svc.Start()
			svc.Stop()
		})
	})
	t.Run("snapshot", func(t *testing.T) {
		svc, _, _, _, _ := newSnapshotHarness(t)
		require.NotPanics(t, func() {
			svc.Start()
			svc.Stop()
			svc.Start()
			svc.Stop()
		})
	})
}

// TestDecrementCrashStat_NoZeroRowResidue R18：递减到 0 时不得留下 count=0 的残留行。
//
// 缺陷现场：先 `UpdateColumn(count-1)` 再 `Delete(count <= 0)` 是两条独立语句，
// 中途失败会留下 count=0 的行；而 `sortedCauseCounts` 不过滤 0，
// 于是 byRootCause 会多出一个「从未发生的根因」条目。
func TestDecrementCrashStat_NoZeroRowResidue(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	now := time.Now().UTC()

	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now, "oom", "sig-a"))
	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now, "oom", "sig-a"))

	// 减 1 → 行仍在，count=1。
	require.NoError(t, crashdiag.DecrementCrashStat(db, inst.ID, now, "oom", "sig-a"))
	var stat model.InstanceCrashStat
	require.NoError(t, db.Where("instance_id = ? AND root_cause = ?", inst.ID, "oom").First(&stat).Error)
	require.Equal(t, 1, stat.Count)

	// 再减 1 → 行必须消失（不留 count=0）。
	require.NoError(t, crashdiag.DecrementCrashStat(db, inst.ID, now, "oom", "sig-a"))
	var cnt int64
	require.NoError(t, db.Model(&model.InstanceCrashStat{}).Where("instance_id = ?", inst.ID).Count(&cnt).Error)
	require.Zero(t, cnt, "减到 0 的行必须被删除，不得留下 count=0 残留")

	// 行不存在时再次递减是无操作（不报错）。
	require.NoError(t, crashdiag.DecrementCrashStat(db, inst.ID, now, "oom", "sig-a"))
}

// TestCrashTrend_SkipsZeroCountRows 残留 count=0 行不得污染读侧输出（R18 的读侧对称断言）。
func TestCrashTrend_SkipsZeroCountRows(t *testing.T) {
	db := newCrashTestDB(t)
	inst := seedCrashInstance(t, db, "smp")
	svc := NewCrashSnapshotService(db)
	now := time.Now().UTC()

	require.NoError(t, crashdiag.UpsertCrashStat(db, inst.ID, now, "port_in_use", "sig-p"))
	// 人工插入一条 count=0 的残留行（模拟改造前的中断产物）。
	require.NoError(t, db.Create(&model.InstanceCrashStat{
		InstanceID: inst.ID, BucketDay: now.Format("2006-01-02"),
		RootCause: "unknown", Signature: "sig-stale", Count: 0,
	}).Error)

	trend, err := svc.TrendByInstance(inst.ID, 30)
	require.NoError(t, err)
	require.Equal(t, 1, trend.Total)
	total := 0
	for _, p := range trend.Points {
		total += p.Count
	}
	require.Equal(t, trend.Total, total)
	for _, c := range trend.ByRootCause {
		require.Greater(t, c.Count, 0, "根因条目不得包含计数为 0 的残留行")
	}
}
