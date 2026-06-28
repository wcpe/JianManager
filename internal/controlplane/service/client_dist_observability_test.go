package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newObsDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.ClientDistSnapshot{}, &model.ClientDistEvent{},
		&model.ClientTelemetry{}, &model.ClientChannel{}))
	return db
}

// atHour 返回某整小时桶起点（UTC）的「桶内」时刻（+10min），便于断言桶归属。
func atHour(base time.Time, hoursAgo int) time.Time {
	return base.UTC().Truncate(time.Hour).Add(-time.Duration(hoursAgo) * time.Hour).Add(10 * time.Minute)
}

// TestObs_Aggregate_PullDimensions 卷积拉取侧：manifest/制品计数、字节求和、CAS 命中/未命中分流、桶内机器码去重、版本分布。
func TestObs_Aggregate_PullDimensions(t *testing.T) {
	db := newObsDB(t)
	svc := NewClientDistObservabilityService(db)
	now := time.Now().UTC()
	ch := "s1"
	// 同一完结小时桶（2 小时前）内的明细。
	ts := atHour(now, 2)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", Version: 7, MachineID: "m1", Bytes: 100, Status: 200, CreatedAt: ts}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", Version: 7, MachineID: "m1", Bytes: 100, Status: 200, CreatedAt: ts}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", Version: 6, MachineID: "m2", Bytes: 100, Status: 200, CreatedAt: ts}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "artifact", MachineID: "m1", Bytes: 5000, Status: 200, CreatedAt: ts}).Error) // CAS miss
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "artifact", MachineID: "m3", Bytes: 0, Status: 304, CreatedAt: ts}).Error)    // CAS hit

	require.NoError(t, svc.AggregateAndPurge(now))

	var snap model.ClientDistSnapshot
	require.NoError(t, db.Where("channel_id = ?", ch).First(&snap).Error)
	require.Equal(t, int64(3), snap.ManifestPulls)
	require.Equal(t, int64(2), snap.ArtifactPulls)
	require.Equal(t, int64(5300), snap.DownloadBytes)
	require.Equal(t, int64(1), snap.CASHit)
	require.Equal(t, int64(1), snap.CASMiss)
	require.Equal(t, int64(3), snap.ActiveMachines, "m1/m2/m3 桶内去重计 3")
	require.Equal(t, ts.Truncate(time.Hour), snap.BucketTS.UTC())

	vd := unmarshalDist(snap.VersionDist)
	require.Equal(t, int64(2), vd["7"])
	require.Equal(t, int64(1), vd["6"])
}

// TestObs_Aggregate_UpdateDimensions 卷积更新侧：result 分流、平台分布、版本滞后（依频道 current_version）。
func TestObs_Aggregate_UpdateDimensions(t *testing.T) {
	db := newObsDB(t)
	svc := NewClientDistObservabilityService(db)
	now := time.Now().UTC()
	ch := "s2"
	require.NoError(t, db.Create(&model.ClientChannel{ChannelID: ch, Name: "S2", CurrentVersion: 8}).Error)
	ts := atHour(now, 3)
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: ch, Result: "success", ToVersion: 8, OS: "windows", CreatedAt: ts}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: ch, Result: "success", ToVersion: 7, OS: "windows", CreatedAt: ts}).Error) // lag 1
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: ch, Result: "fail-static", ToVersion: 8, OS: "linux", CreatedAt: ts}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: ch, Result: "rolled-back", ToVersion: 8, OS: "linux", CreatedAt: ts}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: ch, Result: "error", CreatedAt: ts}).Error)

	require.NoError(t, svc.AggregateAndPurge(now))

	var snap model.ClientDistSnapshot
	require.NoError(t, db.Where("channel_id = ?", ch).First(&snap).Error)
	require.Equal(t, int64(5), snap.UpdateTotal)
	require.Equal(t, int64(2), snap.UpdateSuccess)
	require.Equal(t, int64(1), snap.UpdateFailStatic)
	require.Equal(t, int64(1), snap.UpdateRolledBack)
	require.Equal(t, int64(1), snap.UpdateError)

	pd := unmarshalDist(snap.PlatformDist)
	require.Equal(t, int64(2), pd["windows"])
	require.Equal(t, int64(2), pd["linux"])

	ld := unmarshalDist(snap.LagDist)
	require.Equal(t, int64(3), ld["0"], "toVersion=8 与 current=8 滞后 0，计 3 条")
	require.Equal(t, int64(1), ld["1"], "toVersion=7 滞后 1")
}

// TestObs_Aggregate_OnlyClosedBuckets 当前未完结小时桶不卷（避免半桶入库）。
func TestObs_Aggregate_OnlyClosedBuckets(t *testing.T) {
	db := newObsDB(t)
	svc := NewClientDistObservabilityService(db)
	now := time.Now().UTC()
	// 当前小时桶内（未完结）。
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "s3", Kind: "manifest", MachineID: "m1", Status: 200, CreatedAt: now}).Error)

	require.NoError(t, svc.AggregateAndPurge(now))

	var cnt int64
	require.NoError(t, db.Model(&model.ClientDistSnapshot{}).Count(&cnt).Error)
	require.Equal(t, int64(0), cnt, "未完结桶不应卷积入库")
}

// TestObs_Aggregate_Idempotent 同窗重跑两次结果一致（upsert 覆盖不翻倍）。
func TestObs_Aggregate_Idempotent(t *testing.T) {
	db := newObsDB(t)
	svc := NewClientDistObservabilityService(db)
	now := time.Now().UTC()
	ts := atHour(now, 2)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "s4", Kind: "manifest", Version: 1, MachineID: "m1", Bytes: 10, Status: 200, CreatedAt: ts}).Error)

	require.NoError(t, svc.AggregateAndPurge(now))
	require.NoError(t, svc.AggregateAndPurge(now))

	var snaps []model.ClientDistSnapshot
	require.NoError(t, db.Where("channel_id = ?", "s4").Find(&snaps).Error)
	require.Len(t, snaps, 1, "重跑不应产生重复行")
	require.Equal(t, int64(1), snaps[0].ManifestPulls, "重跑覆盖而非累加")
}

// TestObs_Purge TTL 清理超留存期的快照。
func TestObs_Purge(t *testing.T) {
	db := newObsDB(t)
	svc := NewClientDistObservabilityService(db)
	now := time.Now().UTC()
	// 直接插一条远超留存期的旧快照。
	old := now.Add(-obsSnapshotRetention - 24*time.Hour).Truncate(time.Hour)
	require.NoError(t, db.Create(&model.ClientDistSnapshot{ChannelID: "s5", BucketTS: old, ManifestPulls: 1}).Error)
	// 一条仍在留存期内。
	fresh := now.Add(-24 * time.Hour).Truncate(time.Hour)
	require.NoError(t, db.Create(&model.ClientDistSnapshot{ChannelID: "s5", BucketTS: fresh, ManifestPulls: 1}).Error)

	require.NoError(t, svc.purge(now))

	var snaps []model.ClientDistSnapshot
	require.NoError(t, db.Order("bucket_ts").Find(&snaps).Error)
	require.Len(t, snaps, 1, "仅留存期内快照保留")
	require.Equal(t, fresh, snaps[0].BucketTS.UTC())
}

// TestObs_Query_SeriesAndDistributions 查询返回时序 + 区间分布 + 汇总率。
func TestObs_Query_SeriesAndDistributions(t *testing.T) {
	db := newObsDB(t)
	svc := NewClientDistObservabilityService(db)
	now := time.Now().UTC()
	ch := "s6"
	require.NoError(t, db.Create(&model.ClientChannel{ChannelID: ch, Name: "S6", CurrentVersion: 2}).Error)
	// 两个不同小时桶的明细 + 遥测。
	t1, t2 := atHour(now, 3), atHour(now, 2)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", Version: 2, MachineID: "m1", Bytes: 100, Status: 200, CreatedAt: t1}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", Version: 1, MachineID: "m2", Bytes: 100, Status: 200, CreatedAt: t2}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: ch, Result: "success", ToVersion: 2, OS: "windows", CreatedAt: t1}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: ch, Result: "error", ToVersion: 1, OS: "linux", CreatedAt: t2}).Error)
	require.NoError(t, svc.AggregateAndPurge(now))

	res, err := svc.Query(ObservabilityQuery{ChannelID: ch, From: now.Add(-6 * time.Hour), To: now})
	require.NoError(t, err)
	require.Len(t, res.Series, 2, "两个小时桶各一点")
	require.Equal(t, int64(2), res.Summary.ManifestPulls)
	require.Equal(t, int64(2), res.Summary.UpdateTotal)
	require.InDelta(t, 0.5, res.Summary.SuccessRate, 0.001)

	// 版本分布：v1/v2 各 1。
	require.Len(t, res.VersionDist, 2)
	// 平台分布：windows/linux 各 1。
	require.Len(t, res.PlatformDist, 2)
	// 滞后分布：toVersion=2(lag0)/toVersion=1(lag1)。
	require.Len(t, res.LagDist, 2)
	require.Equal(t, 0, res.LagDist[0].Lag)
	require.Equal(t, 1, res.LagDist[1].Lag)
}

// TestObs_Query_TotalAcrossChannels 不传 channelId 跨频道合并同小时桶。
func TestObs_Query_TotalAcrossChannels(t *testing.T) {
	db := newObsDB(t)
	svc := NewClientDistObservabilityService(db)
	now := time.Now().UTC()
	ts := atHour(now, 2)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "a", Kind: "manifest", Version: 1, MachineID: "m1", Bytes: 100, Status: 200, CreatedAt: ts}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "b", Kind: "manifest", Version: 1, MachineID: "m2", Bytes: 200, Status: 200, CreatedAt: ts}).Error)
	require.NoError(t, svc.AggregateAndPurge(now))

	res, err := svc.Query(ObservabilityQuery{From: now.Add(-6 * time.Hour), To: now})
	require.NoError(t, err)
	require.Len(t, res.Series, 1, "两频道同小时合并为一点")
	require.Equal(t, int64(2), res.Series[0].ManifestPulls)
	require.Equal(t, int64(300), res.Series[0].DownloadBytes)
	require.Equal(t, int64(2), res.Summary.ManifestPulls)
}

// TestObs_Query_ActiveMachinesExact 去重口径：保留窗内回查明细精确去重 + exact=true；超窗求和近似 + exact=false。
func TestObs_Query_ActiveMachinesExact(t *testing.T) {
	db := newObsDB(t)
	svc := NewClientDistObservabilityService(db)
	now := time.Now().UTC()
	ch := "s7"
	// 同一客户端 m1 在两个小时桶各拉取一次 → 桶内各计 1，跨桶求和=2，但独立数=1。
	t1, t2 := atHour(now, 3), atHour(now, 2)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", MachineID: "m1", Status: 200, CreatedAt: t1}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: ch, Kind: "manifest", MachineID: "m1", Status: 200, CreatedAt: t2}).Error)
	require.NoError(t, svc.AggregateAndPurge(now))

	// 区间在保留窗内 → 精确去重独立数=1。
	res, err := svc.Query(ObservabilityQuery{ChannelID: ch, From: now.Add(-6 * time.Hour), To: now})
	require.NoError(t, err)
	require.True(t, res.Summary.ActiveMachinesExact)
	require.Equal(t, int64(1), res.Summary.ActiveMachines, "保留窗内精确去重独立客户端=1")

	// 区间下界超出明细保留窗 → 退化为各桶求和（人次近似），exact=false。
	res2, err := svc.queryAt(now, ObservabilityQuery{ChannelID: ch, From: now.Add(-obsEventDetailRetention - 24*time.Hour), To: now})
	require.NoError(t, err)
	require.False(t, res2.Summary.ActiveMachinesExact)
	require.Equal(t, int64(2), res2.Summary.ActiveMachines, "超保留窗退化为各桶 active_machines 求和=2")
}

// TestObs_Query_EmptyChannel 未知频道返回空时序 + 零汇总（不报错、不 404）。
func TestObs_Query_EmptyChannel(t *testing.T) {
	db := newObsDB(t)
	svc := NewClientDistObservabilityService(db)
	now := time.Now().UTC()
	res, err := svc.Query(ObservabilityQuery{ChannelID: "nope", From: now.Add(-24 * time.Hour), To: now})
	require.NoError(t, err)
	require.Empty(t, res.Series)
	require.Equal(t, int64(0), res.Summary.ManifestPulls)
	require.Equal(t, float64(0), res.Summary.SuccessRate)
	require.Empty(t, res.VersionDist)
}
