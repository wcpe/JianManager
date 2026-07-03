package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newRuntimeStateDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ClientRuntimeState{}, &model.ClientTelemetry{}))
	return db
}

func TestClientRuntimeState_RecordHeartbeatUpsertsLatest(t *testing.T) {
	db := newRuntimeStateDB(t)
	svc := NewClientRuntimeStateService(db)

	require.NoError(t, svc.RecordHeartbeat(ClientRuntimeHeartbeatInput{
		ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", IP: "1.1.1.1", Platform: "windows",
		JavaVersion: "17", Launcher: "HMCL", CoreVersion: "3", LocalVersion: 14,
	}))
	time.Sleep(time.Millisecond)
	require.NoError(t, svc.RecordHeartbeat(ClientRuntimeHeartbeatInput{
		ChannelID: "s1", MachineID: "m1", PlayerName: "Steve", IP: "2.2.2.2", Platform: "linux",
		JavaVersion: "21", Launcher: "PCL2", CoreVersion: "4", LocalVersion: 15,
	}))

	var rows []model.ClientRuntimeState
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1, "同一 channel+machine 应 upsert 最新运行态")
	require.Equal(t, "Steve", rows[0].PlayerName)
	require.Equal(t, "2.2.2.2", rows[0].IP)
	require.Equal(t, "linux", rows[0].Platform)
	require.Equal(t, "4", rows[0].CoreVersion)
	require.Equal(t, 15, rows[0].LocalVersion)
	require.False(t, rows[0].FirstSeenAt.IsZero())
	require.False(t, rows[0].LastHeartbeatAt.IsZero())
}

func TestClientRuntimeState_OverviewSeparatesRuntimeAndTelemetry(t *testing.T) {
	db := newRuntimeStateDB(t)
	svc := NewClientRuntimeStateService(db)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	require.NoError(t, db.Create(&model.ClientRuntimeState{
		ChannelID: "s1", MachineID: "m1", Platform: "windows", Launcher: "HMCL", CoreVersion: "3", LocalVersion: 15,
		FirstSeenAt: now.Add(-time.Hour), LastHeartbeatAt: now.Add(-2 * time.Minute),
	}).Error)
	require.NoError(t, db.Create(&model.ClientRuntimeState{
		ChannelID: "s1", MachineID: "m2", Platform: "linux", Launcher: "PCL2", CoreVersion: "3", LocalVersion: 14,
		FirstSeenAt: now.Add(-2 * time.Hour), LastHeartbeatAt: now.Add(-30 * time.Minute),
	}).Error)
	require.NoError(t, db.Create(&model.ClientRuntimeState{
		ChannelID: "s2", MachineID: "m3", Platform: "macos", Launcher: "unknown", CoreVersion: "2", LocalVersion: 9,
		FirstSeenAt: now.Add(-2 * time.Minute), LastHeartbeatAt: now.Add(-2 * time.Minute),
	}).Error)

	// 更新结果只来自 client_telemetry，心跳不写 telemetry。
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: "s1", MachineID: "m1", Result: "success", CreatedAt: now.Add(-time.Hour)}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: "s1", MachineID: "m2", Result: "fail-static", CreatedAt: now.Add(-time.Hour)}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: "s1", MachineID: "m2", Result: "error", CreatedAt: now.Add(-time.Hour)}).Error)

	out, err := svc.Overview(ClientRuntimeQuery{ChannelID: "s1", Range: "7d", Now: now})
	require.NoError(t, err)
	require.Equal(t, int64(1), out.Summary.RecentStarted, "近 5 分钟启动只统计 recent 心跳")
	require.Equal(t, int64(2), out.Summary.TodayStarted, "今日启动按今日心跳统计")
	require.InDelta(t, 1.0/3.0, out.Summary.UpdateSuccessRate, 0.0001)
	require.InDelta(t, 2.0/3.0, out.Summary.UpdateFailureRate, 0.0001)
	require.Equal(t, []RuntimeVersionCount{{Version: 15, Count: 1}, {Version: 14, Count: 1}}, out.RuntimeVersionDist)
	require.Equal(t, []RuntimeStringCount{{Value: "3", Count: 2}}, out.CoreVersionDist)
	require.Equal(t, []RuntimeLagCount{{Lag: 0, Count: 1}, {Lag: 1, Count: 1}}, out.LagDist)
	require.Len(t, out.UpdateResultSeries, 1)
	require.Equal(t, int64(1), out.UpdateResultSeries[0].Success)
	require.Equal(t, int64(1), out.UpdateResultSeries[0].FailStatic)
	require.Equal(t, int64(1), out.UpdateResultSeries[0].Error)
}
