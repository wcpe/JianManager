package service

// 跨频道隔离回归：latest_version 子查询必须跟着外部 channel_id 过滤。
//
// 缺陷成因：外部查询用 `base.Where("channel_id = ?")` 圈定了频道，
// 但 SELECT 里的 `(SELECT MAX(e3.version) ...)` 是**独立 SQL 作用域**，
// 不会继承外部 WHERE。于是按频道 A 看时，频道 B 的更高版本号被当成 A 的最新版，
// A 频道里每台机器都凭空多出几版的「滞后」。
//
// 为何之前的用例没拓到：所有用例只用 ch1，没有第二个频道作对照。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestQueryMachines_LatestVersionScopedToChannel 验证 latest_version 不被其他频道污染。
func TestQueryMachines_LatestVersionScopedToChannel(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()

	// 频道 ch1：最高版本 5。
	require.NoError(t, svc.db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 3, Status: 200,
		CreatedAt: now.Add(-2 * time.Hour),
	}).Error)
	require.NoError(t, svc.db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 5, Status: 200,
		CreatedAt: now.Add(-1 * time.Hour),
	}).Error)
	// 频道 ch2：最高版本 99，故意与 ch1 差很多。
	// 若子查询没带 channel 过滤，下面 ch1 的 LatestVersion 会变成 99。
	require.NoError(t, svc.db.Create(&model.ClientDistEvent{
		ChannelID: "ch2", MachineID: "m9", Kind: "manifest", Version: 99, Status: 200,
		CreatedAt: now.Add(-30 * time.Minute),
	}).Error)

	res, err := svc.QueryMachines(ClientMachineListQuery{
		ChannelID: "ch1",
		From:      now.Add(-24 * time.Hour), To: now,
		Sort: "updates", Order: "desc", Page: 1, PageSize: 20,
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1, "只看 ch1：应只有 m1，ch2 的 m9 不得混进来")

	m1 := res.Items[0]
	require.Equal(t, "ch1", m1.ChannelID)
	require.Equal(t, 5, m1.LatestVersion,
		"latestVersion 必须只算 ch1 内；拿到 99 说明子查询漏了 channel_id 过滤")
	require.Equal(t, 5, m1.CurrentVersion)
	require.Equal(t, 0, m1.VersionLag, "同一频道内 m1 已是最新，滞后应为 0（而非 99-5=94）")
}

// TestQueryMachines_NoChannelFilterUsesGlobalLatest 验证不指定频道时语义仍为全局最新。
//
// 这条用例是为了钉住「按需加过滤」这个分支：无条件拼 `channel_id = ”` 会很安静地把基准算成 0。
func TestQueryMachines_NoChannelFilterUsesGlobalLatest(t *testing.T) {
	db := newTrackingDB(t)
	svc := NewClientDistTrackingService(db)
	now := time.Now().UTC()

	require.NoError(t, svc.db.Create(&model.ClientDistEvent{
		ChannelID: "ch1", MachineID: "m1", Kind: "manifest", Version: 5, Status: 200,
		CreatedAt: now.Add(-2 * time.Hour),
	}).Error)
	require.NoError(t, svc.db.Create(&model.ClientDistEvent{
		ChannelID: "ch2", MachineID: "m9", Kind: "manifest", Version: 99, Status: 200,
		CreatedAt: now.Add(-1 * time.Hour),
	}).Error)

	res, err := svc.QueryMachines(ClientMachineListQuery{
		From: now.Add(-24 * time.Hour), To: now,
		Sort: "updates", Order: "desc", Page: 1, PageSize: 20,
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 2, "不指定频道时应看到两个频道的机器")

	byMachine := map[string]int{}
	for _, it := range res.Items {
		byMachine[it.MachineID] = it.LatestVersion
	}
	require.Equal(t, 99, byMachine["m1"],
		"不指定频道时基准是全局最新 99；若得 5 说明错误地把全局语义改成了单频道")
}
