package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newTelemetryDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ClientTelemetry{}, &model.ClientTelemetryDaily{}))
	return db
}

// TestClientTelemetry_RecordAndDailyAggregate 明细落库 + 按 result 日聚合。
func TestClientTelemetry_RecordAndDailyAggregate(t *testing.T) {
	db := newTelemetryDB(t)
	svc := NewClientTelemetryService(db)

	require.NoError(t, svc.Record(ClientTelemetryInput{ChannelID: "s1", Result: "success", ToVersion: 2}))
	require.NoError(t, svc.Record(ClientTelemetryInput{ChannelID: "s1", Result: "success", ToVersion: 2}))
	require.NoError(t, svc.Record(ClientTelemetryInput{ChannelID: "s1", Result: "fail-static"}))

	var raw int64
	db.Model(&model.ClientTelemetry{}).Count(&raw)
	require.Equal(t, int64(3), raw)

	var ok model.ClientTelemetryDaily
	require.NoError(t, db.Where("channel_id = ? AND result = ?", "s1", "success").First(&ok).Error)
	require.Equal(t, int64(2), ok.Count, "success 日聚合应为 2")
	var fail model.ClientTelemetryDaily
	require.NoError(t, db.Where("channel_id = ? AND result = ?", "s1", "fail-static").First(&fail).Error)
	require.Equal(t, int64(1), fail.Count)
}

// TestClientTelemetry_InvalidResultNormalized 非法 result 归一为 error（防脏数据）。
func TestClientTelemetry_InvalidResultNormalized(t *testing.T) {
	db := newTelemetryDB(t)
	svc := NewClientTelemetryService(db)
	require.NoError(t, svc.Record(ClientTelemetryInput{ChannelID: "s1", Result: "weird-injection"}))
	var row model.ClientTelemetry
	require.NoError(t, db.First(&row).Error)
	require.Equal(t, "error", row.Result)
}

// TestClientTelemetry_Cleanup 清理过期明细、保留聚合。
func TestClientTelemetry_Cleanup(t *testing.T) {
	db := newTelemetryDB(t)
	svc := NewClientTelemetryService(db)
	require.NoError(t, db.Create(&model.ClientTelemetry{
		ChannelID: "s1", Result: "success", CreatedAt: time.Now().Add(-20 * 24 * time.Hour),
	}).Error)
	require.NoError(t, svc.Record(ClientTelemetryInput{ChannelID: "s1", Result: "success"}))

	n, err := svc.Cleanup()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	var raw int64
	db.Model(&model.ClientTelemetry{}).Count(&raw)
	require.Equal(t, int64(1), raw, "未过期明细保留")
	var dailies int64
	db.Model(&model.ClientTelemetryDaily{}).Count(&dailies)
	require.GreaterOrEqual(t, dailies, int64(1), "聚合不被清理")
}
