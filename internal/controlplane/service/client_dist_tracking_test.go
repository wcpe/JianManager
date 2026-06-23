package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newTrackingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ClientDistEvent{}, &model.ClientDistDaily{}))
	return db
}

// TestClientDistTracking_RecordDetailAndDailyAggregate 写明细 + 写时增量聚合（同维度合并）。
func TestClientDistTracking_RecordDetailAndDailyAggregate(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)

	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", MachineID: "m1", IP: "1.2.3.4", Kind: "manifest", Version: 5, Bytes: 100, Status: 200, DurationMs: 3}))
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", MachineID: "m2", IP: "5.6.7.8", Kind: "manifest", Version: 5, Bytes: 120, Status: 200}))

	var detail int64
	db.Model(&model.ClientDistEvent{}).Count(&detail)
	require.Equal(t, int64(2), detail, "两次拉取应两行明细")

	var d model.ClientDistDaily
	require.NoError(t, db.Where("channel_id = ? AND version = ? AND kind = ?", "ch1", 5, "manifest").First(&d).Error)
	require.Equal(t, int64(2), d.Requests, "同维度聚合 requests 合并")
	require.Equal(t, int64(220), d.Bytes, "同维度聚合 bytes 累加")
}

// TestClientDistTracking_SkipsEmptyKind kind 空跳过（不记）。
func TestClientDistTracking_SkipsEmptyKind(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", Kind: ""}))
	var cnt int64
	db.Model(&model.ClientDistEvent{}).Count(&cnt)
	require.Equal(t, int64(0), cnt)
}

// TestClientDistTracking_ArtifactAllowsEmptyChannel 制品事件跨频道、频道可空仍记录。
func TestClientDistTracking_ArtifactAllowsEmptyChannel(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	require.NoError(t, svc.Record(ClientDistEventInput{Kind: "artifact", ArtifactSHA: "abc", Bytes: 50, Status: 200}))
	var cnt int64
	db.Model(&model.ClientDistEvent{}).Count(&cnt)
	require.Equal(t, int64(1), cnt, "制品事件频道空也应记录")
}

// TestClientDistTracking_CleanupDeletesOldDetailKeepsDaily 清理删过期明细、保留聚合。
func TestClientDistTracking_CleanupDeletesOldDetailKeepsDaily(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)

	// 过期明细（20 天前，超出默认 14 天保留）。CreatedAt 显式赋值非零，GORM 不覆盖。
	require.NoError(t, db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", Kind: "manifest", Version: 1, CreatedAt: time.Now().Add(-20 * 24 * time.Hour),
	}).Error)
	// 新明细 + 聚合。
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", Kind: "manifest", Version: 2, Bytes: 10, Status: 200}))

	n, err := svc.Cleanup()
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "应删 1 条过期明细")

	var detail int64
	db.Model(&model.ClientDistEvent{}).Count(&detail)
	require.Equal(t, int64(1), detail, "未过期明细保留")
	var dailies int64
	db.Model(&model.ClientDistDaily{}).Count(&dailies)
	require.GreaterOrEqual(t, dailies, int64(1), "聚合长保留、不被清理")
}

// TestClientDistTracking_QueryEventsFilters 检索按机器码/类型过滤。
func TestClientDistTracking_QueryEventsFilters(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", MachineID: "m1", IP: "A", Kind: "manifest", Version: 1, Status: 200}))
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", MachineID: "m2", IP: "B", Kind: "manifest", Version: 1, Status: 200}))

	byMachine, err := svc.QueryEvents(ClientDistEventFilter{MachineID: "m1"})
	require.NoError(t, err)
	require.Len(t, byMachine, 1)
	require.Equal(t, "m1", byMachine[0].MachineID)

	byKind, err := svc.QueryEvents(ClientDistEventFilter{Kind: "manifest"})
	require.NoError(t, err)
	require.Len(t, byKind, 2)
}
