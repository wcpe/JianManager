package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestCutover_IngestInstanceOutput_Stopped 切换打开后：实例输出不再写入 CP logs 表。
func TestCutover_IngestInstanceOutput_Stopped(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))

	node := model.Node{UUID: "cutover-node", Name: "n", Host: "127.0.0.1"}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID:         "cutover-inst",
		Name:         "srv",
		NodeID:       node.ID,
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "echo hi",
	}
	require.NoError(t, db.Create(&inst).Error)

	svc.Cutover().SetEnabled(true)
	svc.Start()
	defer svc.Stop()

	svc.IngestInstanceOutput("cutover-node", "cutover-inst", "stdout", "should not persist\n", time.Now().Unix())

	require.Never(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceInstance).Count(&n)
		return n > 0
	}, 800*time.Millisecond, 40*time.Millisecond, "cutover on 时实例日志不得新入 CP logs")

	require.Greater(t, svc.Cutover().BlockedCount(), int64(0))
}

// TestCutover_WorkerSourceAlsoStopped 切换打开后：worker 来源同样停止新入 CP logs。
func TestCutover_WorkerSourceAlsoStopped(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	svc.Cutover().SetEnabled(true)
	svc.Start()
	defer svc.Stop()

	ok := svc.Ingest(IngestEntry{
		Source:  model.LogSourceWorker,
		Level:   model.LogLevelInfo,
		NodeID:  3,
		Message: "worker self log",
		Time:    time.Now(),
	})
	require.False(t, ok, "worker 来源在 cutover 下应被拒绝")

	require.Never(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceWorker).Count(&n)
		return n > 0
	}, 600*time.Millisecond, 40*time.Millisecond)
}

// TestCutover_PlatformStillIngests 切换打开后：platform slog 持久化路径不变。
func TestCutover_PlatformStillIngests(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	svc.Cutover().SetEnabled(true)
	svc.Start()
	defer svc.Stop()

	require.True(t, svc.Ingest(IngestEntry{
		Source:  model.LogSourceControlPlane,
		Level:   model.LogLevelInfo,
		Message: "platform still persists",
		Time:    time.Now(),
	}))

	require.Eventually(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceControlPlane).Count(&n)
		return n == 1
	}, 5*time.Second, 50*time.Millisecond)
}

// TestCutover_DisabledKeepsOldPath 开关关闭时行为与既有 log_test 一致。
func TestCutover_DisabledKeepsOldPath(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	require.False(t, svc.Cutover().Enabled())
	require.False(t, svc.cutoverBlocked(model.LogSourceInstance))
	require.False(t, svc.cutoverBlocked(model.LogSourceWorker))
	require.False(t, svc.cutoverBlocked(model.LogSourceControlPlane))
	require.Zero(t, svc.Cutover().BlockedCount())

	svc.Start()
	defer svc.Stop()
	svc.IngestInstanceOutput("node-only", "missing", "stdout", "still works", 0)
	require.Eventually(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Count(&n)
		return n == 1
	}, 5*time.Second, 50*time.Millisecond)
}

// TestCutover_WatermarkPreconditions 水位：能力确认 + 账本就绪才允许切换。
func TestCutover_WatermarkPreconditions(t *testing.T) {
	c := NewLogCutover()

	_, err := c.ApplyCutover("worker-a", time.Now())
	require.Error(t, err, "未确认能力/账本时不得切换")

	c.UpsertWatermark(LogCutoverWatermark{WorkerUUID: "worker-a", CapabilityConfirmed: true})
	_, err = c.ApplyCutover("worker-a", time.Now())
	require.Error(t, err, "仅能力确认、账本未就绪时仍不得切换")

	c.UpsertWatermark(LogCutoverWatermark{WorkerUUID: "worker-a", LedgerReady: true})
	cutoff := time.Now().Add(-time.Minute)
	w, err := c.ApplyCutover("worker-a", cutoff)
	require.NoError(t, err)
	require.True(t, w.CutoverApplied)
	require.True(t, w.CutoffTime.Equal(cutoff))

	got, ok := c.Watermark("worker-a")
	require.True(t, ok)
	require.True(t, got.CapabilityConfirmed)
	require.True(t, got.LedgerReady)
	require.True(t, got.CutoverApplied)
	require.True(t, got.CutoffTime.Equal(cutoff))

	// 未就绪时 Upsert 带 CutoverApplied 不得绕过前置条件。
	c.UpsertWatermark(LogCutoverWatermark{WorkerUUID: "worker-b", CutoverApplied: true, CutoffTime: time.Now()})
	gotB, ok := c.Watermark("worker-b")
	require.True(t, ok)
	require.False(t, gotB.CutoverApplied)
}

// TestCutover_DualPathSourceTags 双路径门面：结果带来源标签；合并统计不得标 exact。
func TestCutover_DualPathSourceTags(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	now := time.Now()
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "legacy row", Time: now.Add(-2 * time.Minute)})
	seed(t, db, model.LogEntry{Source: model.LogSourceWorker, Level: model.LogLevelInfo, NodeID: 2, Message: "worker legacy", Time: now.Add(-time.Minute)})
	seed(t, db, model.LogEntry{Source: model.LogSourceControlPlane, Level: model.LogLevelInfo, Message: "platform", Time: now})

	dual := svc.DualPath()
	require.NotNil(t, dual)

	leg, err := dual.QueryLegacy(LogFilter{})
	require.NoError(t, err)
	require.Equal(t, LogQuerySourceLegacy, leg.SourceTag)
	require.EqualValues(t, 2, leg.Total, "Legacy 只含 instance/worker")
	require.NotNil(t, leg.Coverage)
	require.Equal(t, LogQuerySourceLegacy, leg.Coverage.SourceTag)
	require.False(t, leg.Coverage.Complete)
	require.False(t, leg.Coverage.NDJSONInScope)

	fed, err := dual.QueryFederated(context.Background(), LogFilter{})
	require.ErrorIs(t, err, ErrFederatedNotReady)
	require.Equal(t, LogQuerySourceFederated, fed.SourceTag)
	require.False(t, fed.StatsExact)

	stats, err := dual.CombinedStats(context.Background(), LogFilter{})
	require.NoError(t, err)
	require.False(t, stats.StatsExact, "行/事件未等价前禁止精确合并")
	require.EqualValues(t, 2, stats.LegacyRows)
	require.False(t, stats.FederatedReady)
	require.Contains(t, stats.SourceTags, LogQuerySourceLegacy)

	fedRes, legRes, err := dual.QueryBoth(context.Background(), LogFilter{})
	// Federated 未就绪不视为整体失败；Legacy 仍可返回。
	require.NoError(t, err)
	require.NotNil(t, legRes)
	require.Equal(t, LogQuerySourceLegacy, legRes.SourceTag)
	if fedRes != nil {
		require.Equal(t, LogQuerySourceFederated, fedRes.SourceTag)
	}
}

// TestCutover_PlatformCapacityProtectsUnexpiredLegacy platform 容量淘汰不得挤出未到期 Legacy。
func TestCutover_PlatformCapacityProtectsUnexpiredLegacy(t *testing.T) {
	cfg := defaultCfg()
	cfg.RetentionDays = 0
	cfg.MaxTotalMB = 1 // 阈值 2048 行（仅统计非 Legacy）
	svc, _, db := newLogSvc(t, cfg)

	base := time.Now().Add(-2 * time.Hour)
	// 更旧的 Legacy 行：若无保护，会被容量裁剪最先删掉。
	for i := 0; i < 100; i++ {
		seed(t, db, model.LogEntry{
			Source: model.LogSourceInstance, Level: model.LogLevelInfo,
			InstanceID: 1, Message: "legacy keep",
			Time: base.Add(time.Duration(i) * time.Millisecond),
		})
	}
	// 较新的 platform 行超阈值。
	platBase := time.Now().Add(-time.Hour)
	for i := 0; i < 2100; i++ {
		seed(t, db, model.LogEntry{
			Source: model.LogSourceControlPlane, Level: model.LogLevelInfo,
			Message: "platform trim",
			Time:    platBase.Add(time.Duration(i) * time.Millisecond),
		})
	}

	svc.Cutover().SetEnabled(true)
	n, err := svc.archiveOverCapacity()
	require.NoError(t, err)
	require.Equal(t, 2100-2048, n, "只裁剪非 Legacy 超额部分")

	var legacyLeft int64
	db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceInstance).Count(&legacyLeft)
	require.EqualValues(t, 100, legacyLeft, "未到期 Legacy 不得被 platform 容量淘汰")

	var platLeft int64
	db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceControlPlane).Count(&platLeft)
	require.EqualValues(t, 2048, platLeft)
}

// TestCutover_PlatformTimeRetentionProtectsLegacy platform 时间保留在 cutover 下不清理 Legacy。
func TestCutover_PlatformTimeRetentionProtectsLegacy(t *testing.T) {
	cfg := defaultCfg()
	cfg.RetentionDays = 7
	cfg.MaxTotalMB = 0
	svc, _, db := newLogSvc(t, cfg)

	old := time.Now().AddDate(0, 0, -10)
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "legacy old", Time: old})
	seed(t, db, model.LogEntry{Source: model.LogSourceControlPlane, Level: model.LogLevelInfo, Message: "platform old", Time: old})

	svc.Cutover().SetEnabled(true)
	n, err := svc.archiveBeforeRetention()
	require.NoError(t, err)
	require.Equal(t, 1, n, "仅 platform 过期行被清；Legacy 走独立预算")

	var legacyLeft int64
	db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceInstance).Count(&legacyLeft)
	require.EqualValues(t, 1, legacyLeft)
}

func TestCutoverStateAndWorkerWatermarkSurviveControlPlaneRestart(t *testing.T) {
	_, _, db := newLogSvc(t, defaultCfg())
	require.NoError(t, db.AutoMigrate(&model.LogCutoverState{}, &model.LogCutoverWorker{}))
	cutover := NewPersistentLogCutover(db)
	require.NoError(t, cutover.UpsertWatermarkChecked(LogCutoverWatermark{
		WorkerUUID: "worker-1", CapabilityConfirmed: true, LedgerReady: true,
	}))
	cutoff := time.Date(2026, 9, 23, 5, 0, 0, 0, time.UTC)
	_, err := cutover.ApplyCutover("worker-1", cutoff)
	require.NoError(t, err)
	require.NoError(t, cutover.SetEnabledChecked(true))

	restarted := NewPersistentLogCutover(db)
	require.True(t, restarted.Enabled())
	mark, ok := restarted.Watermark("worker-1")
	require.True(t, ok)
	require.True(t, mark.Ready())
	require.True(t, mark.CutoverApplied)
	require.Equal(t, cutoff, mark.CutoffTime)
}

// TestCutover_BlocksIngestSource 单元：来源拦截矩阵。
func TestCutover_BlocksIngestSource(t *testing.T) {
	var nilC *LogCutover
	require.False(t, nilC.BlocksIngestSource(model.LogSourceInstance))

	c := NewLogCutover()
	require.False(t, c.BlocksIngestSource(model.LogSourceInstance))
	c.SetEnabled(true)
	require.True(t, c.BlocksIngestSource(model.LogSourceInstance))
	require.True(t, c.BlocksIngestSource(model.LogSourceWorker))
	require.False(t, c.BlocksIngestSource(model.LogSourceControlPlane))
}

// TestLogStore_CutoverConfigDefaults 零配置：切换关闭；Legacy 走 DefaultLegacy* 预算。
func TestLogStore_CutoverConfigDefaults(t *testing.T) {
	require.False(t, config.DefaultLogCutoverEnabled)
	require.Equal(t, 30, config.DefaultLegacyRetentionDays)
	require.Zero(t, config.DefaultLegacyMaxTotalMB)

	svc, _, _ := newLogSvc(t, config.LogStoreConfig{Enabled: true, PersistPlatform: true})
	require.False(t, svc.Cutover().Enabled())
	require.Equal(t, config.DefaultLegacyRetentionDays, svc.Legacy().RetentionDays)
	require.Equal(t, config.DefaultLegacyMaxTotalMB, svc.Legacy().MaxTotalMB)

	st := svc.CutoverStatus()
	require.False(t, st.Enabled)
	require.Equal(t, config.DefaultLegacyRetentionDays, st.LegacyRetentionDays)
	require.False(t, st.PlatformPurgeExcludesLegacy)
	require.NotNil(t, st.Watermarks)
	require.Len(t, st.Watermarks, 0)
}

// TestLogStore_CutoverConfigApplied 配置接线：开关与 Legacy 预算从 cfg.Cutover 读取；
// 打开切换不停 platform（control_plane）入库门闩。
func TestLogStore_CutoverConfigApplied(t *testing.T) {
	cfg := defaultCfg()
	cfg.Cutover = config.LogCutoverStoreConfig{
		Enabled:             true,
		LegacyRetentionDays: 45,
		LegacyMaxTotalMB:    128,
	}
	svc, _, _ := newLogSvc(t, cfg)
	require.True(t, svc.Cutover().Enabled())
	require.Equal(t, 45, svc.Legacy().RetentionDays)
	require.Equal(t, 128, svc.Legacy().MaxTotalMB)

	// 打开切换不拦 control_plane 来源。
	require.False(t, svc.cutoverBlocked(model.LogSourceControlPlane))
	require.True(t, svc.Ingest(IngestEntry{
		Source:  model.LogSourceControlPlane,
		Level:   model.LogLevelInfo,
		Message: "platform still accepted",
		Time:    time.Now(),
	}))

	st := svc.CutoverStatus()
	require.True(t, st.Enabled)
	require.True(t, st.PlatformPurgeExcludesLegacy)
	require.Equal(t, 45, st.LegacyRetentionDays)
	require.Equal(t, 128, st.LegacyMaxTotalMB)
}

// TestLogStore_ApplyAdminWatermarkValidation 管理面水位：apply 必须 capability+ledger_ready。
func TestLogStore_ApplyAdminWatermarkValidation(t *testing.T) {
	svc, _, _ := newLogSvc(t, defaultCfg())

	_, err := svc.ApplyAdminWatermark(LogCutoverWatermark{}, true)
	require.Error(t, err)

	_, err = svc.ApplyAdminWatermark(LogCutoverWatermark{WorkerUUID: "w-1"}, true)
	require.Error(t, err, "apply 缺前置条件必须拒绝")

	// 只登记确认位：不 apply。
	got, err := svc.ApplyAdminWatermark(LogCutoverWatermark{
		WorkerUUID:          "w-1",
		CapabilityConfirmed: true,
		LedgerReady:         true,
	}, false)
	require.NoError(t, err)
	require.True(t, got.CapabilityConfirmed)
	require.True(t, got.LedgerReady)
	require.False(t, got.CutoverApplied)

	// 前置条件齐全后 apply 成功。
	got, err = svc.ApplyAdminWatermark(LogCutoverWatermark{
		WorkerUUID:          "w-1",
		CapabilityConfirmed: true,
		LedgerReady:         true,
	}, true)
	require.NoError(t, err)
	require.True(t, got.CutoverApplied)
}
