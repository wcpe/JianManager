package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 对账矩阵的「列表展示」与「档位筛选」必须同源。
//
// 这组用例守的是一个真实存在过的分叉：`config_version` 是 NOT NULL DEFAULT 0 的列，
// 而 MarkBridgeConnected 会为「桥刚连上、但探针还没报过 config_state」的实例建一行，
// 只填连接证据、不填 reported_at。
//
// 这类行的两种读法给出相反结论：
//   - 按列值直接扫 → 观测版本 = 0 → 「从未下发 + 观测 0」判成 inSync；
//   - 按 SQL 条件（要求 reported_at 非空）→ 筛不出 inSync。
//
// 于是页面上显示「已一致」，点「已一致」芯片却看不到这台机器。
// 修法是让「没有上报时刻」等价于「没有观测」，SQL 与 Go 两侧都按这一条判。

// newMatrixTestDB 建对账矩阵所需的临时库（实例 / 节点 / 下发目标 / 实例状态）。
func newMatrixTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{},
		&model.ManagedPushSession{}, &model.ManagedPushTarget{},
		&model.ManagedInstanceConfigState{},
	))
	return db
}

// seedMatrixInstance 建一台实例并返回其 ID。
func seedMatrixInstance(t *testing.T, db *gorm.DB, name string) model.Instance {
	t.Helper()
	node := model.Node{Name: name + "-node", UUID: name + "-node-uuid"}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		Name: name, UUID: name + "-uuid", NodeID: node.ID,
	}
	require.NoError(t, db.Create(&inst).Error)
	return inst
}

// TestMatrix_ConnectedButNeverReportedIsNotInSync 守「桥连上但从未上报」不被判成已一致。
//
// 这是分叉能被观察到的最小夹具：行存在（所以 SQL 的 s.id 非空）、config_version 取默认 0、
// reported_at 为空。正确结论是 neverReported，且用 inSync 筛选时不应出现。
func TestMatrix_ConnectedButNeverReportedIsNotInSync(t *testing.T) {
	db := newMatrixTestDB(t)
	inst := seedMatrixInstance(t, db, "hub")

	// 只填连接证据，刻意不填 reported_at / config_version —— 与 MarkBridgeConnected 一致。
	require.NoError(t, db.Create(&model.ManagedInstanceConfigState{
		InstanceID:          inst.ID,
		InstanceUUID:        inst.UUID,
		BridgeConnected:     true,
		EffectiveConnection: model.ManagedConnectionConnected,
	}).Error)

	svc := NewManagedMatrixService(db, nil, nil)

	rows, _, err := svc.Matrix(ManagedMatrixFilter{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)

	assert.Equal(t, model.ManagedConsistencyNeverReported, rows[0].Consistency,
		"桥连上但从未上报 config_state 的行必须判 neverReported，不能因为它有状态行就当成已观测")
	assert.Nil(t, rows[0].ObservedVersion,
		"没有上报时刻就应对外报 null，而不是把列的默认值 0 当成真实观测版本")

	// 筛选侧必须与展示侧同源：inSync 筛选不能把这一行捞出来。
	inSyncRows, _, err := svc.Matrix(ManagedMatrixFilter{
		Consistency: model.ManagedConsistencyInSync, Page: 1, PageSize: 10,
	})
	require.NoError(t, err)
	assert.Empty(t, inSyncRows, "列表说不一致，inSync 筛选就不该命中它")

	// neverReported 筛选则应命中——两个方向都要断言，否则「两个都筛不出来」也会绿。
	nrRows, _, err := svc.Matrix(ManagedMatrixFilter{
		Consistency: model.ManagedConsistencyNeverReported, Page: 1, PageSize: 10,
	})
	require.NoError(t, err)
	assert.Len(t, nrRows, 1, "从未上报的行必须能被 neverReported 筛选命中")
}

// TestMatrix_ReportedZeroIsInSync 是上一条的反向对照。
//
// 同样 config_version = 0，但**有** reported_at：这是「探针确实报过第 0 版」。
// 若把 reported_at 判据写成恒假、或干脆按列值判，这条会与上一条一起绿/一起红，
// 只有两条并置才能证明判据是 reported_at 本身而不是别的。
func TestMatrix_ReportedZeroIsInSync(t *testing.T) {
	db := newMatrixTestDB(t)
	inst := seedMatrixInstance(t, db, "lobby")

	now := time.Now()
	require.NoError(t, db.Create(&model.ManagedInstanceConfigState{
		InstanceID:          inst.ID,
		InstanceUUID:        inst.UUID,
		ConfigVersion:       0,
		BridgeConnected:     true,
		EffectiveConnection: model.ManagedConnectionConnected,
		ReportedAt:          &now,
	}).Error)

	svc := NewManagedMatrixService(db, nil, nil)

	rows, _, err := svc.Matrix(ManagedMatrixFilter{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, model.ManagedConsistencyInSync, rows[0].Consistency,
		"有上报时刻的 0 版 + 从未下发 = inSync（CP 没有期望就不存在漂移）")
	require.NotNil(t, rows[0].ObservedVersion)
	assert.Equal(t, int64(0), *rows[0].ObservedVersion, "真实上报的 0 要如实报 0，不能变成 null")

	inSyncRows, _, err := svc.Matrix(ManagedMatrixFilter{
		Consistency: model.ManagedConsistencyInSync, Page: 1, PageSize: 10,
	})
	require.NoError(t, err)
	assert.Len(t, inSyncRows, 1, "inSync 筛选必须与展示同源")
}

// TestMatrix_SummaryMatchesList 守 summary 与矩阵列表口径一致。
//
// summary 走的是 Go 判定路径（按行累计），列表走 SQL 路径；两者若分叉，
// 页头芯片数字与点进去看到的内容会不一致——这正是上面那个缺陷的表象。
func TestMatrix_SummaryMatchesList(t *testing.T) {
	db := newMatrixTestDB(t)

	// 一台：桥连上未上报 → neverReported
	connected := seedMatrixInstance(t, db, "connected")
	require.NoError(t, db.Create(&model.ManagedInstanceConfigState{
		InstanceID:          connected.ID,
		InstanceUUID:        connected.UUID,
		BridgeConnected:     true,
		EffectiveConnection: model.ManagedConnectionConnected,
	}).Error)

	// 另一台：有上报、0 版 → inSync
	reported := seedMatrixInstance(t, db, "reported")
	now := time.Now()
	require.NoError(t, db.Create(&model.ManagedInstanceConfigState{
		InstanceID:          reported.ID,
		InstanceUUID:        reported.UUID,
		ConfigVersion:       0,
		BridgeConnected:     true,
		EffectiveConnection: model.ManagedConnectionConnected,
		ReportedAt:          &now,
	}).Error)

	svc := NewManagedMatrixService(db, nil, nil)

	rows, total, err := svc.Matrix(ManagedMatrixFilter{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	summary, err := svc.Summary(ManagedMatrixFilter{})
	require.NoError(t, err)

	assert.Equal(t, total, summary.Total, "total 与 summary.Total 必须是同一个分母")

	// 逐档计数必须等于按该档筛选出来的行数——这是「芯片数字 = 点进去的结果」的定义。
	for _, c := range []string{
		model.ManagedConsistencyInSync,
		model.ManagedConsistencyDrift,
		model.ManagedConsistencyNeverReported,
		model.ManagedConsistencyFailed,
	} {
		filtered, _, ferr := svc.Matrix(ManagedMatrixFilter{
			Consistency: c, Page: 1, PageSize: 10,
		})
		require.NoError(t, ferr)
		listed := int64(len(filtered))
		// failed 在 summary 里可能来自「目标状态失败」而非观测版本，故只在非 failed 档比对。
		if c != model.ManagedConsistencyFailed {
			assert.Equal(t, listed, summary.ByConsistency[c],
				"档位 %s 的 summary 计数必须等于按该档筛选出的行数", c)
		}
	}
}
