package service

// 回归：artifact/core 事件（version 恒 0）不得污染「当前版本」。
//
// 缺陷成因：current_version 子查询取「这台机器窗口内最近一条事件的 version」，
// 而明细表里只有 manifest 拉取才带版本号；artifact（制品下载）与 core（updater 本体）
// 落库时 Version 恒为 0。updater 的正常时序是「先拉 manifest(vN) → 再下载制品(v0)×N」，
// 所以最近一条事件通常就是 artifact。
// 不看 kind 直接取最近一条，一台已升到最新版的机器会被算成 v0 →
// lag = latest - 0，它反而被排到「最需更新」的第一位。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestQueryMachines_ArtifactEventsDoNotResetCurrentVersion 验证制品事件不把当前版本压成 0。
func TestQueryMachines_ArtifactEventsDoNotResetCurrentVersion(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()

	// m1 已升到最新版 v9，随后又下了两个制品（version=0，时间更晚）。
	require.NoError(t, db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 9, Bytes: 10, Status: 200,
		CreatedAt: now.Add(-3 * time.Hour),
	}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m1", Kind: "artifact", Version: 0, Bytes: 500, Status: 200,
		CreatedAt: now.Add(-2 * time.Hour),
	}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m1", Kind: "artifact", Version: 0, Bytes: 700, Status: 200,
		CreatedAt: now.Add(-1 * time.Hour),
	}).Error)

	res, err := svc.QueryMachines(ClientMachineListQuery{
		From: now.Add(-24 * time.Hour), To: now,
		Sort: "updates", Order: "desc", Page: 1, PageSize: 20,
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)

	m1 := res.Items[0]
	require.Equal(t, 9, m1.CurrentVersion,
		"当前版本应取最近一条 manifest(9)；得 0 说明 artifact(v0) 事件把它覆盖了")
	require.Equal(t, 9, m1.LatestVersion)
	require.Equal(t, 0, m1.VersionLag,
		"已是最新版的机器滞后应为 0；得 9 说明它会被误报为严重滞后")
}

// TestQueryMachines_VersionLagSortMatchesDisplayedValues 验证排序结果与展示值一致。
//
// 缺陷成因（已修）：ORDER BY 早期用 Go 格式化的 UTC 字符串字面量重建时间窗，
// 与 SELECT 侧的 time.Time 绑定不等价（时区 + 精度）。排序依据的 lag 与返回的 lag
// 可能不是同一个数，用户点「按滞后排序」看到的顺序与数字互相矛盾。
func TestQueryMachines_VersionLagSortMatchesDisplayedValues(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()

	// m1：最新 v9 → lag 0
	require.NoError(t, db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 9, Status: 200,
		CreatedAt: now.Add(-1 * time.Hour),
	}).Error)
	// m2：v4 → lag 5
	require.NoError(t, db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m2", Kind: "manifest", Version: 4, Status: 200,
		CreatedAt: now.Add(-2 * time.Hour),
	}).Error)
	// m3：v7 → lag 2
	require.NoError(t, db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m3", Kind: "manifest", Version: 7, Status: 200,
		CreatedAt: now.Add(-3 * time.Hour),
	}).Error)

	for _, dir := range []string{"desc", "asc"} {
		res, err := svc.QueryMachines(ClientMachineListQuery{
			From: now.Add(-24 * time.Hour), To: now,
			Sort: "versionLag", Order: dir, Page: 1, PageSize: 20,
		})
		require.NoError(t, err)
		require.Len(t, res.Items, 3)

		lags := make([]int, 0, len(res.Items))
		for _, it := range res.Items {
			lags = append(lags, it.VersionLag)
		}
		// 顺序必须与返回的 lag 数值单调一致——这正是「排序静默失效」会破坏的东西。
		for i := 1; i < len(lags); i++ {
			if dir == "desc" {
				require.GreaterOrEqual(t, lags[i-1], lags[i],
					"desc 排序下 lag 应单调不增，实得 %v（排序依据与展示值不一致）", lags)
			} else {
				require.LessOrEqual(t, lags[i-1], lags[i],
					"asc 排序下 lag 应单调不减，实得 %v", lags)
			}
		}
		require.Equal(t, 5, lags[0]+lags[len(lags)-1], "应含 lag 5 与 lag 0 两端")
	}
}
