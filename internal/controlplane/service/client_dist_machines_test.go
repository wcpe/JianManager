package service

// 机器级更新清单与钻取的回归。
//
// 这组用例针对的是一类**门禁看不见的断裂**：端点只存在于 devmock、真栈 404，
// 而 spec 却标着「已交付」。所以它们全部直接打 service 层，不经过任何 mock——
// 只要 SQL 或契约映射写错，这里必红。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// seedMachineEvents 造一批明细：同一机器多条 + 跨机器 + 一个应当被排除的空 machineId。
func seedMachineEvents(t *testing.T, svc *ClientDistTrackingService, base time.Time) {
	t.Helper()
	require.NoError(t, svc.db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 3, Bytes: 100, Status: 200,
		CreatedAt: base.Add(-3 * time.Hour),
	}).Error)
	require.NoError(t, svc.db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 5, Bytes: 200, Status: 200,
		CreatedAt: base.Add(-2 * time.Hour),
	}).Error)
	// m2 只有一条且版本更高：用来验「最新版本」与「版本滞后」的算法。
	require.NoError(t, svc.db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m2", Kind: "manifest", Version: 7, Bytes: 50, Status: 200,
		CreatedAt: base.Add(-1 * time.Hour),
	}).Error)
	// 空 machineId 不得进入清单（不是一台机器）。
	require.NoError(t, svc.db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "", Kind: "manifest", Version: 7, Status: 200,
		CreatedAt: base.Add(-1 * time.Hour),
	}).Error)
}

// TestQueryMachines_AggregatesAndPaginates 聚合值、排序、total 与服务端分页。
func TestQueryMachines_AggregatesAndPaginates(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()
	seedMachineEvents(t, svc, now)

	res, err := svc.QueryMachines(ClientMachineListQuery{
		From: now.Add(-24 * time.Hour), To: now,
		Sort: "updates", Order: "desc", Page: 1, PageSize: 20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), res.Total, "空 machineId 不应计入机器数")
	require.Len(t, res.Items, 2)

	// 按更新次数倒序：m1（2 次）在前，m2（1 次）在后。
	m1 := res.Items[0]
	require.Equal(t, "m1", m1.MachineID)
	require.Equal(t, int64(2), m1.Updates)
	require.Equal(t, int64(300), m1.DownloadBytes, "bytes 应求和")
	require.Equal(t, 7, m1.LatestVersion, "latestVersion 取**全局**窗口内最大 version（平台最新=7）")
	require.Equal(t, 5, m1.CurrentVersion, "currentVersion 取该机器最近一条事件的 version")
	require.Equal(t, 2, m1.VersionLag, "滞后 = 全局最新 7 - 本机 5")

	m2 := res.Items[1]
	require.Equal(t, "m2", m2.MachineID)
	require.Equal(t, int64(1), m2.Updates, "m2 只有一条事件")
}

// TestQueryMachines_PageTwoDoesNotRepeat 分页稳定：同排序值下靠 machine_id 次序键。
func TestQueryMachines_PageTwoDoesNotRepeat(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()
	seedMachineEvents(t, svc, now)

	q := ClientMachineListQuery{From: now.Add(-24 * time.Hour), To: now, Sort: "updates", Order: "desc", PageSize: 1}
	q.Page = 1
	p1, err := svc.QueryMachines(q)
	require.NoError(t, err)
	q.Page = 2
	p2, err := svc.QueryMachines(q)
	require.NoError(t, err)

	require.Len(t, p1.Items, 1)
	require.Len(t, p2.Items, 1)
	require.NotEqual(t, p1.Items[0].MachineID, p2.Items[0].MachineID, "翻页不得重复同一台机器")
}

// TestQueryMachines_SortsByVersionLag version_lag 排序按表达式而不是列别名（别名在 SQL 里不存在）。
func TestQueryMachines_SortsByVersionLag(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()

	// m3：早期低版本 + 后来高版本 → 当前版本追上，lag=0。
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m3", Kind: "manifest", Version: 2, Status: 200, CreatedAt: now.Add(-5 * time.Hour)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m3", Kind: "manifest", Version: 9, Status: 200, CreatedAt: now.Add(-1 * time.Hour)}).Error)
	// m4：只有旧版本 → lag>0（最新 9 - 当前 4）。
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m4", Kind: "manifest", Version: 4, Status: 200, CreatedAt: now.Add(-4 * time.Hour)}).Error)

	res, err := svc.QueryMachines(ClientMachineListQuery{
		From: now.Add(-24 * time.Hour), To: now, Sort: "versionLag", Order: "desc", Page: 1, PageSize: 10,
	})
	require.NoError(t, err, "按 version_lag 排序必须能用（不能引用不存在的列）")
	require.Len(t, res.Items, 2)
	require.Equal(t, "m4", res.Items[0].MachineID, "滞后大的排在前")
	require.Equal(t, 5, res.Items[0].VersionLag)
	require.Equal(t, "m3", res.Items[1].MachineID)
	require.Equal(t, 0, res.Items[1].VersionLag)
}

// TestQueryMachines_JoinsRuntimeIdentity 玩家名/IP 来自运行态快照，安装 ID 来自遥测。
func TestQueryMachines_JoinsRuntimeIdentity(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()
	seedMachineEvents(t, svc, now)

	require.NoError(t, db.Create(&model.ClientRuntimeState{
		ChannelID: "ch1", MachineID: "m1", PlayerName: "Alice", IP: "10.0.0.9",
		LastHeartbeatAt: now.Add(-30 * time.Minute),
	}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetry{
		ChannelID: "ch1", MachineID: "m1", InstallID: "inst-abc", Result: "success",
		CreatedAt: now.Add(-30 * time.Minute),
	}).Error)

	res, err := svc.QueryMachines(ClientMachineListQuery{
		From: now.Add(-24 * time.Hour), To: now, Sort: "updates", Page: 1, PageSize: 20,
	})
	require.NoError(t, err)

	var m1 *ClientMachineSummary
	for i := range res.Items {
		if res.Items[i].MachineID == "m1" {
			m1 = &res.Items[i]
		}
	}
	require.NotNil(t, m1)
	require.Equal(t, "Alice", m1.PlayerName)
	require.Equal(t, "10.0.0.9", m1.IP)
	require.Equal(t, "inst-abc", m1.InstallID)

	// m2 没有任何运行态/遥测 → 三个字段为空串而不是报错或占位值。
	for _, it := range res.Items {
		if it.MachineID == "m2" {
			require.Empty(t, it.PlayerName)
			require.Empty(t, it.InstallID)
			require.Empty(t, it.IP)
		}
	}
}

// TestQueryMachines_ExactFlagReflectsRetention 超过明细保留窗时 exact=false。
func TestQueryMachines_ExactFlagReflectsRetention(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()

	inside, err := svc.QueryMachines(ClientMachineListQuery{From: now.Add(-24 * time.Hour), To: now})
	require.NoError(t, err)
	require.True(t, inside.Exact, "24h 窗在 14 天保留期内，应标精确")

	outside, err := svc.QueryMachines(ClientMachineListQuery{From: now.Add(-30 * 24 * time.Hour), To: now})
	require.NoError(t, err)
	require.False(t, outside.Exact, "30 天窗超出保留期，不得谎报精确")
}

// TestQueryMachines_RejectsBadWindow 零值/倒置时间窗直接拒绝，不静默全量。
func TestQueryMachines_RejectsBadWindow(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()

	_, err := svc.QueryMachines(ClientMachineListQuery{})
	require.Error(t, err, "空时间窗应拒绝")

	_, err = svc.QueryMachines(ClientMachineListQuery{From: now, To: now.Add(-time.Hour)})
	require.Error(t, err, "倒置时间窗应拒绝")
}

// TestQueryMachineEvents_MapsResults 时间线的事件归并到四个契约取值。
func TestQueryMachineEvents_MapsResults(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()

	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 5, Bytes: 10, Status: 200, CreatedAt: now.Add(-3 * time.Hour)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 5, Bytes: 20, Status: 500, CreatedAt: now.Add(-2 * time.Hour)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 4, Status: 200, ErrCode: "rollback", CreatedAt: now.Add(-1 * time.Hour)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 5, Status: 200, ErrCode: "static_mismatch", CreatedAt: now.Add(-30 * time.Minute)}).Error)

	res, err := svc.QueryMachineEvents("m1", "ch1", now.Add(-24*time.Hour), now)
	require.NoError(t, err)
	require.Len(t, res.Items, 4)

	var results []string
	for _, it := range res.Items {
		results = append(results, it.Result)
	}
	require.Contains(t, results, "success")
	require.Contains(t, results, "error")
	require.Contains(t, results, "rollback", "err_code=rollback 应归为 rollback 而不是 error")
	require.Contains(t, results, "fail-static", "static_mismatch 应归为 fail-static")
}

// TestQueryMachineEvents_RejectsEmptyMachineID 空 machineId 不得拉全量。
func TestQueryMachineEvents_RejectsEmptyMachineID(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()
	_, err := svc.QueryMachineEvents("  ", "", now.Add(-time.Hour), now)
	require.Error(t, err)
}

// TestMachineEventResultOf 归并规则的边界。
func TestMachineEventResultOf(t *testing.T) {
	cases := []struct {
		status  int
		errCode string
		want    string
	}{
		{200, "", "success"},
		{206, "", "success"},
		{304, "", "success"},
		{400, "", "error"},
		{500, "", "error"},
		{0, "", "error"},
		{200, "rollback", "rollback"},
		{500, "rollback_failed", "rollback"},
		{200, "static_mismatch", "fail-static"},
		{500, "static_verify_failed", "fail-static"},
		{200, "other_code", "success"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, machineEventResultOf(c.status, c.errCode), "status=%d errCode=%q", c.status, c.errCode)
	}
}
