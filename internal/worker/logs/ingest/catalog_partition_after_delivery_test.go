package ingest

// 回归：**投递后必须留下 Catalog 分区记录**（2026-11-03 实测的真 bug）。
//
// ── 缺陷形态 ──────────────────────────────────────────────────────────────
//
// 2026-11-02 投递路径简化后，`deliverCtx` 只做「去重 → 规范化 → 写段 → 写 VL」，
// **不再调 `publishSnapshot`**。而 Catalog 分区记录的唯一创建者就是它。
//
// 于是：数据真的写进了 VL（本文件实测 `VL writes=1`、事件段 1 条），
// 但 `query.Planner.Plan` 拿到的是 `view targets=[]`——
// 因为 `catalog.querySnapshotLocked` 的门禁是
// `rec == nil || rec.PublishedProjection == nil || !Recover(rec).Queryable || !OwnerForQuery(rec).OK`，
// 缺记录就直接 `continue`。用户看到的是「这天没日志」。
//
// 这比「丢数据」更难排查：日志一切正常、VL 里也有行，就是查不到。
//
// ── 两条判据 ──────────────────────────────────────────────────────────────
//
//  1. 投递后 Catalog 里有记录，且 owner / dirID 与写目标同源（`WriteAdmission`）；
//  2. `query.Planner` 能把该目标规划进视图且 `Queryable == true`。
//
// 转红方式（实测）：注释掉 `deliverCtx` 里的 `m.ensureCatalogPartition(source)`
// ⇒ 第一条报 `catalog record exists=false`、第二条报 `targets=[]`。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

func TestDeliveryLeavesCatalogPartitionQueryable(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	// 先登记再产生日志（FILE_PRIMARY 源一律 StartAtEOF，裁定 D）。
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "node:cp", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:cp", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	appendPollTestLines(t, path, "[12:00:01] [Server thread/INFO]: hello\n[12:00:02] [Server thread/INFO]: next\n")

	// 前置：数据真的到 VL 了（否则本条测的是“没投出去”，不是“投出去了但查不到”）。
	m.pollOnce()
	require.NotEmpty(t, fixture.writes, "夹具前提：一轮采集必须真的写进 VL")
	require.NotEmpty(t, durableEvents(t, m, "node:cp/g1"), "夹具前提：事件段必须有内容")

	// 判据 ①：Catalog 记录必须存在，且 owner/dirID 与写目标同源。
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	rec, ok := cat.Get(key)
	require.True(t, ok, "投递后 Catalog 必须有该分区的记录；否则查询侧直接 continue（用户看到「这天没日志」）")
	target, err := cat.WriteAdmission(key)
	require.NoError(t, err)
	require.Equal(t, target.Owner, rec.Owner, "记录的 owner 必须与写目标同源（否则会出现「记录写 HOT、数据写 COLD」）")
	require.Equal(t, target.Generation, rec.Generation)
	require.NotEmpty(t, rec.OwnerDirID)
	require.NotNil(t, rec.PublishedProjection,
		"投影结构必须非 nil：querySnapshotLocked 的门禁要求它，且空代次正好让查询不加代次过滤")

	// 判据 ②：查询侧能规划出该目标，且标记为可查。
	planner := query.NewPlanner(cat, nil)
	res := planner.Plan(query.PlanRequest{TargetIDs: []string{source.StorageNamespace}})
	require.Nil(t, res.Err, "规划不得报错")
	require.NotNil(t, res.View)
	require.Contains(t, res.View.TargetIDs, key.String(),
		"该分区必须进入视图；缺失即代表「数据在 VL 里却查不到」")
	require.True(t, res.View.QueryableTargets[key.String()], "该分区必须标记为可查")
}
