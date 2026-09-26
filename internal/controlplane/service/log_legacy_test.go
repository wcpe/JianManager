package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestLegacy_ReaderVisibleAfterCutover 切换后：切换前实例行仍可经 LegacyLogReader 只读访问。
func TestLegacy_ReaderVisibleAfterCutover(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))

	base := time.Now().Add(-time.Hour)
	seed(t, db, model.LogEntry{
		Source: model.LogSourceInstance, Level: model.LogLevelInfo,
		InstanceID: 1, InstanceUUID: "legacy-inst", Message: "pre-cutover instance line",
		Time: base,
	})
	seed(t, db, model.LogEntry{
		Source: model.LogSourceWorker, Level: model.LogLevelWarn,
		NodeID: 2, Message: "pre-cutover worker line",
		Time: base.Add(time.Minute),
	})
	seed(t, db, model.LogEntry{
		Source: model.LogSourceControlPlane, Level: model.LogLevelInfo,
		Message: "platform not legacy", Time: base.Add(2 * time.Minute),
	})

	svc.Cutover().SetEnabled(true)

	leg := svc.Legacy()
	require.NotNil(t, leg)

	page, err := leg.Query(LogFilter{})
	require.NoError(t, err)
	require.Equal(t, LogQuerySourceLegacy, page.SourceTag)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, page.Items, 2)
	for _, item := range page.Items {
		require.True(t, isLegacyLogSource(item.Source))
		require.NotEqual(t, "platform not legacy", item.Message)
	}

	require.NotNil(t, page.Coverage.FromTime)
	require.NotNil(t, page.Coverage.ToTime)
	require.False(t, page.Coverage.Complete)
	require.False(t, page.Coverage.NDJSONInScope)

	// 按实例过滤仍只读 Legacy 集合。
	iid := uint(1)
	page, err = leg.Query(LogFilter{InstanceID: &iid})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, "pre-cutover instance line", page.Items[0].Message)
}

// TestLegacy_IndependentRetentionBudget Legacy 独立时间预算：platform 保留天数不影响 Legacy 清理。
func TestLegacy_IndependentRetentionBudget(t *testing.T) {
	cfg := defaultCfg()
	cfg.RetentionDays = 7 // platform 短周期
	cfg.MaxTotalMB = 0
	svc, _, db := newLogSvc(t, cfg)

	old := time.Now().AddDate(0, 0, -20) // 超 platform 7 天，但未超 Legacy 默认 30 天
	seed(t, db, model.LogEntry{
		Source: model.LogSourceInstance, Level: model.LogLevelInfo,
		InstanceID: 1, Message: "legacy within budget", Time: old,
	})

	svc.Cutover().SetEnabled(true)
	require.Equal(t, legacyDefaultRetentionDays, svc.Legacy().RetentionDays)

	n, err := svc.archiveBeforeRetention()
	require.NoError(t, err)
	require.Equal(t, 0, n, "platform 时间巡检不得清 Legacy")

	var left int64
	db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceInstance).Count(&left)
	require.EqualValues(t, 1, left)

	// Legacy 自己的预算（更短）到期后才清理。
	svc.Legacy().RetentionDays = 7
	n, err = svc.Legacy().RunRetention()
	require.NoError(t, err)
	require.Equal(t, 1, n)
	db.Model(&model.LogEntry{}).Count(&left)
	require.EqualValues(t, 0, left)
}

// TestLegacy_CapacityBudgetIndependent Legacy 独立总量预算只作用于 instance/worker 行。
func TestLegacy_CapacityBudgetIndependent(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now()
	for i := 0; i < 20; i++ {
		seed(t, db, model.LogEntry{
			Source: model.LogSourceInstance, Level: model.LogLevelInfo,
			InstanceID: 1, Message: "legacy bulk",
			Time: base.Add(time.Duration(i) * time.Second),
		})
		seed(t, db, model.LogEntry{
			Source: model.LogSourceControlPlane, Level: model.LogLevelInfo,
			Message: "platform bulk",
			Time:    base.Add(time.Duration(i) * time.Second),
		})
	}

	svc.Cutover().SetEnabled(true)
	// Legacy 预算极小：按 singleEntryBytes 折算，几乎必超。
	// MaxTotalMB=1 → 2048 行，20 条不够超；直接用逻辑：设 RetentionDays 后再测容量。
	// 此处验证：Legacy MaxTotalMB<=0 时不按总量清理。
	svc.Legacy().RetentionDays = 0
	svc.Legacy().MaxTotalMB = 0
	n, err := svc.Legacy().RunRetention()
	require.NoError(t, err)
	require.Equal(t, 0, n)

	// 手工把预算收到「必超」：用负值以外的方式 —— 将 MaxTotalMB 设为触发阈值以下。
	// singleEntryBytes=512；要让 20 行超限，需要 maxRows < 20，即 MaxTotalMB 使  MB*1MB/512 < 20
	// 最小正整数 MB 仍是 2048 行，无法用 MB 表达。因此容量保护验证改由
	// platform 容量测试（TestCutover_PlatformCapacityProtectsUnexpiredLegacy）覆盖；
	// 此处确认 Legacy 不会误清 platform 行。
	var platLeft int64
	db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceControlPlane).Count(&platLeft)
	require.EqualValues(t, 20, platLeft)
	var instLeft int64
	db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceInstance).Count(&instLeft)
	require.EqualValues(t, 20, instLeft)
}

// TestLegacy_QueryIgnoresPlatformAndNDJSON Legacy 查询不返回 platform 行；Coverage 标明 NDJSON 范围外。
func TestLegacy_QueryIgnoresPlatformAndNDJSON(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	now := time.Now()
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 9, Message: "only this", Time: now})
	seed(t, db, model.LogEntry{Source: model.LogSourceControlPlane, Level: model.LogLevelInfo, Message: "platform noise", Time: now})

	page, err := svc.Legacy().Query(LogFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, "only this", page.Items[0].Message)
	require.Equal(t, LogQuerySourceLegacy, page.Coverage.SourceTag)
	for _, note := range page.Coverage.Notes {
		require.NotEmpty(t, note)
	}
	require.False(t, page.Coverage.NDJSONInScope)
}

// TestLegacy_WatermarkAppearsInCoverage 已应用水位出现在 Coverage.WorkerCutoffs。
func TestLegacy_WatermarkAppearsInCoverage(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "x", Time: time.Now()})

	cut := time.Now().Add(-5 * time.Minute)
	svc.Cutover().UpsertWatermark(LogCutoverWatermark{WorkerUUID: "w1", CapabilityConfirmed: true, LedgerReady: true})
	_, err := svc.Cutover().ApplyCutover("w1", cut)
	require.NoError(t, err)

	page, err := svc.Legacy().Query(LogFilter{})
	require.NoError(t, err)
	require.Contains(t, page.Coverage.WorkerCutoffs, "w1")
	require.True(t, page.Coverage.WorkerCutoffs["w1"].Equal(cut) || page.Coverage.WorkerCutoffs["w1"].Equal(cut.UTC()))
}

// TestLegacy_NilReaderSafety nil reader 不 panic。
func TestLegacy_NilReaderSafety(t *testing.T) {
	var r *LegacyLogReader
	_, err := r.Query(LogFilter{})
	require.Error(t, err)
	n, err := r.RunRetention()
	require.NoError(t, err)
	require.Equal(t, 0, n)
}
