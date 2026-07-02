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
	require.NoError(t, db.AutoMigrate(&model.ClientDistEvent{}, &model.ClientDistDaily{}, &model.ClientRuntimeState{}))
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

// TestClientDistTracking_RecordPersistsErrCode 失败事件写入语义错误码（FR-249）。
func TestClientDistTracking_RecordPersistsErrCode(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	require.NoError(t, svc.Record(ClientDistEventInput{
		ChannelID: "ch1", Kind: "manifest", Version: 0, Status: 404, ErrCode: "NO_LATEST_VERSION",
	}))

	var ev model.ClientDistEvent
	require.NoError(t, db.First(&ev).Error)
	require.Equal(t, "NO_LATEST_VERSION", ev.ErrCode, "失败事件应保留错误码")
}

// TestClientDistTracking_QueryOutcomeAndErrCode 按 outcome（成功/失败）+ errCode 精确筛（FR-249）。
// 失败⟺status>=400；成功⟺status>0 且 <400（含 304/200/206）。
func TestClientDistTracking_QueryOutcomeAndErrCode(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	// 成功事件：200、304。
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", Kind: "manifest", Version: 1, Status: 200}))
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", Kind: "manifest", Version: 1, Status: 304}))
	// 失败事件：401（无效密钥）、404（无版本）。
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", Kind: "manifest", Status: 401, ErrCode: "INVALID_CLIENT_KEY"}))
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", Kind: "manifest", Status: 404, ErrCode: "NO_LATEST_VERSION"}))

	failures, err := svc.QueryEvents(ClientDistEventFilter{Outcome: "failure"})
	require.NoError(t, err)
	require.Len(t, failures, 2, "outcome=failure 只返 status>=400")
	for _, e := range failures {
		require.GreaterOrEqual(t, e.Status, 400)
	}

	successes, err := svc.QueryEvents(ClientDistEventFilter{Outcome: "success"})
	require.NoError(t, err)
	require.Len(t, successes, 2, "outcome=success 只返 0<status<400（含 304）")
	for _, e := range successes {
		require.Greater(t, e.Status, 0)
		require.Less(t, e.Status, 400)
	}

	byCode, err := svc.QueryEvents(ClientDistEventFilter{ErrCode: "INVALID_CLIENT_KEY"})
	require.NoError(t, err)
	require.Len(t, byCode, 1, "errCode 精确筛")
	require.Equal(t, "INVALID_CLIENT_KEY", byCode[0].ErrCode)

	// outcome 空 → 不约束（返全部）。
	all, err := svc.QueryEvents(ClientDistEventFilter{})
	require.NoError(t, err)
	require.Len(t, all, 4)
}

func TestClientDistTracking_RecordSanitizedHeadersAndDetail(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	require.NoError(t, svc.Record(ClientDistEventInput{
		ChannelID: "ch1", MachineID: "m1", IP: "1.2.3.4", Kind: "manifest", Version: 5,
		Method: "GET", Path: "/api/v1/client-channels/ch1/manifest?secret=bad", Status: 200,
		RequestHeaders: map[string]string{
			"User-Agent": "JM-Updater", "X-Client-Key": "jmck_secret_should_not_persist", "Authorization": "Bearer nope",
			"Range": "bytes=0-99",
		},
		ResponseHeaders: map[string]string{"ETag": "\"5\"", "Set-Cookie": "bad=1", "Content-Length": "100"},
	}))

	detail, err := svc.GetEventDetail(1)
	require.NoError(t, err)
	require.Equal(t, "GET", detail.Method)
	require.Equal(t, "/api/v1/client-channels/ch1/manifest", detail.Path, "path 不应保留 query")
	require.Equal(t, "JM-Updater", detail.RequestHeaders["User-Agent"])
	require.Equal(t, "present", detail.RequestHeaders["X-Client-Key"], "拉取密钥只能保留 present 标记")
	require.NotContains(t, detail.RequestHeaders, "Authorization")
	require.Equal(t, "\"5\"", detail.ResponseHeaders["ETag"])
	require.NotContains(t, detail.ResponseHeaders, "Set-Cookie")
}

func TestClientDistTracking_SearchEventsRuntimeFiltersAndPagination(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	require.NoError(t, db.Create(&model.ClientRuntimeState{ChannelID: "ch1", MachineID: "m1", Platform: "windows", CoreVersion: "3", LocalVersion: 15}).Error)
	require.NoError(t, db.Create(&model.ClientRuntimeState{ChannelID: "ch1", MachineID: "m2", Platform: "linux", CoreVersion: "2", LocalVersion: 14}).Error)
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", MachineID: "m1", IP: "A", Kind: "manifest", Version: 15, Status: 200}))
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", MachineID: "m2", IP: "B", Kind: "manifest", Version: 14, Status: 200}))
	require.NoError(t, svc.Record(ClientDistEventInput{ChannelID: "ch1", MachineID: "m3", IP: "C", Kind: "manifest", Version: 13, Status: 200}))

	page, err := svc.SearchEvents(ClientDistEventSearchFilter{RuntimeVersion: intPtr(15), Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, "m1", page.Items[0].MachineID)

	page, err = svc.SearchEvents(ClientDistEventSearchFilter{Platform: "linux", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, "m2", page.Items[0].MachineID)

	page, err = svc.SearchEvents(ClientDistEventSearchFilter{Lag: intPtr(1), Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, "m2", page.Items[0].MachineID)
}

func TestClientDistTracking_RealtimeOverview(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m1", IP: "1.1.1.1", Kind: "manifest", Status: 200, CreatedAt: now.Add(-10 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m2", IP: "1.1.1.1", Kind: "artifact", Status: 206, CreatedAt: now.Add(-20 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m2", IP: "2.2.2.2", Kind: "manifest", Status: 401, ErrCode: "INVALID_CLIENT_KEY", CreatedAt: now.Add(-30 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "old", IP: "3.3.3.3", Kind: "manifest", Status: 200, CreatedAt: now.Add(-2 * time.Hour)}).Error)

	out, err := svc.Realtime(ClientDistRealtimeQuery{ChannelID: "ch1", Now: now})
	require.NoError(t, err)
	require.Equal(t, int64(2), out.Summary1h.ManifestPulls)
	require.Equal(t, int64(1), out.Summary1h.ArtifactPulls)
	require.Equal(t, int64(1), out.Summary1h.ErrorRequests)
	require.Equal(t, int64(2), out.Summary1h.ActiveMachines)
	require.Len(t, out.RecentErrors, 1)
	require.Equal(t, "INVALID_CLIENT_KEY", out.RecentErrors[0].ErrCode)
	require.Equal(t, "1.1.1.1", out.TopIPs1h[0].IP)
}

func intPtr(n int) *int { return &n }
