package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newStatsDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.ClientDistDaily{}, &model.ClientTelemetryDaily{}, &model.ClientDistEvent{}))
	return db
}

// TestClientDistStats_Overview 复合聚合：下载趋势 + 版本分布 + 成功率/回退率 + 活跃机器码 + TopIP。
func TestClientDistStats_Overview(t *testing.T) {
	db := newStatsDB(t)
	svc := NewClientDistStatsService(db)
	const ch = "s1"
	day := time.Now().UTC().Format("2006-01-02")
	now := time.Now()

	// 下载/版本聚合（client_dist_daily）。
	require.NoError(t, db.Create(&model.ClientDistDaily{Day: day, ChannelID: ch, Version: 1, Kind: "manifest", Requests: 5, Bytes: 500}).Error)
	require.NoError(t, db.Create(&model.ClientDistDaily{Day: day, ChannelID: ch, Version: 2, Kind: "manifest", Requests: 3, Bytes: 300}).Error)
	// 遥测结果聚合（client_telemetry_daily）。
	require.NoError(t, db.Create(&model.ClientTelemetryDaily{Day: day, ChannelID: ch, Result: "success", Count: 8}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetryDaily{Day: day, ChannelID: ch, Result: "rolled-back", Count: 2}).Error)
	// 明细（机器码/IP）。
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", MachineID: "m1", IP: "1.1.1.1", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", MachineID: "m1", IP: "1.1.1.1", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", MachineID: "m2", IP: "2.2.2.2", CreatedAt: now}).Error)

	st, err := svc.Overview(ch, 30)
	require.NoError(t, err)

	// 下载趋势：1 天、requests=8、bytes=800。
	require.Len(t, st.Downloads, 1)
	require.Equal(t, int64(8), st.Downloads[0].Requests)
	require.Equal(t, int64(800), st.Downloads[0].Bytes)
	// 版本分布。
	require.Len(t, st.Versions, 2)
	// 成功率/回退率。
	require.InDelta(t, 0.8, st.SuccessRate, 0.001)
	require.InDelta(t, 0.2, st.RollbackRate, 0.001)
	// 活跃机器码 = 2（m1/m2）。
	require.Equal(t, int64(2), st.ActiveMachines)
	// TopIP：1.1.1.1 计 2 居首。
	require.GreaterOrEqual(t, len(st.TopIPs), 1)
	require.Equal(t, "1.1.1.1", st.TopIPs[0].IP)
	require.Equal(t, int64(2), st.TopIPs[0].Count)
}

// TestClientDistStats_EmptyChannel 无数据频道返回空集 + 零率（不报错）。
func TestClientDistStats_EmptyChannel(t *testing.T) {
	db := newStatsDB(t)
	st, err := NewClientDistStatsService(db).Overview("nope", 0)
	require.NoError(t, err)
	require.Equal(t, 30, st.Days, "days<=0 归一为 30")
	require.Empty(t, st.Downloads)
	require.Equal(t, float64(0), st.SuccessRate)
	require.Equal(t, int64(0), st.ActiveMachines)
}
