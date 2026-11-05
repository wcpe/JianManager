package ingest

// 端到端契约（2026-11-05 生产故障）：**投递一条 ⇒ 查询规划出一条 range**。
//
// ── 为什么必须有这一条 ────────────────────────────────────────────────────────
//
// 故障现象：VL 里确实有 615 行，接口返回 `items=0 / exhausted=true /
// coverage.complete=true`——空结果被粉饰成「完整且无数据」，用户看到「这天没日志」。
//
// 当时 `internal/worker/logs` 下有五十余条用例全绿，**没有任何一条**断言
// 「投递后能在查询面规划出 range」。既有最接近的用例
// （`TestDeliveryLeavesCatalogPartitionQueryable`）只断言
// `View.TargetIDs` 与 `QueryableTargets`——而故障恰恰是「目标进了视图、
// 但 `Ranges` 为空」，于是它一路绿灯放行。
//
// 更隐蔽的是**夹具形状**：既有夹具写
//
//	StorageNamespace: "node:cp", LogSourceID: "node:cp"     // ← 同值
//
// 而真机是 `StorageNamespace: "inst:318"` / `log_source_id: "inst:318/file"`。
// 同值夹具让「命名空间 == 完整源 id」这个错误前提恒成立，
// 任何按命名空间去精确匹配 `log_source_id` 的缺陷都不可能暴露。
//
// 本文件用**真机形状**走完整条链：
//
//	投递 → Catalog 分区记录 → query.Planner.Plan → Ranges 非空

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// TestDeliverThenPlanYieldsRangeForRealSourceShape 是本次故障的端到端回归。
//
// 真机形状（缺一不可）：
//   - StorageNamespace 是**授权粒度**的 target id（`inst:318`）；
//   - LogSourceID 是**物理源 id**，带流后缀（`inst:318/file`）；
//   - 源的 UTCDay 是登记日，与事件所属天不同（跨天后必然如此）。
func TestDeliverThenPlanYieldsRangeForRealSourceShape(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o644))

	// **真机形状**：三者刻意互不相同。
	//
	// 2026-11-05 订正（时间炸弹）：原先这里写死 `eventDay = "2026-11-04"`，
	// 并用 `[2026-11-04T10:00:00Z] ...` 作为行首。两处都有问题：
	//
	//  1. 写死日期在 11-05 当天就会红（分区记录落到今天，断言却要 11-04）；
	//  2. 更根本的是：`[2026-11-04T10:00:00Z]` **不是解析器认识的格式**。
	//     Minecraft 行首只有 `[HH:MM:SS]` / `[HH:MM:SS.mmm]`（**无日期**），
	//     日期由 `applyClock` 用采集基准（base）的年月日补齐。写完整 ISO 时间戳
	//     会让 `hasTime=false` ⇒ 事件时间 fallback 到 `IngestTimeUTC`（= 今天）。
	//     所以旧夹具实际验证的是一条不存在的时间戳路径。
	//
	// 现在用真实格式：事件日 = 今天（采集基准日）⇒ 断言随之跟随，永不因跨天而红。
	// 而“事件日 ≠ 源登记日”这个核心前提仍然成立（sourceDay 固定在很久以前）。
	const (
		namespace = "inst:318"      // 授权目标 / Catalog StorageNamespace
		sourceID  = "inst:318/file" // 物理源 id（VL 行上的 log_source_id）
		sourceDay = "2026-10-30"    // 源登记日（早已过去，与事件日必然不同）
	)
	eventDay := time.Now().UTC().Format("2006-01-02")
	require.NotEqual(t, sourceDay, eventDay,
		"夹具前提：事件日必须与源登记日不同，否则本用例守不到「按事件日归天」")
	source := SourceConfig{
		LogSourceID: sourceID, SourceGeneration: "gen-1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: namespace,
		UTCDay: sourceDay,
	}

	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)

	// 行首用**真实 Minecraft 格式**（HH:MM:SS，无日期）；日期由采集基准补齐 ⇒
	// 事件落进今天的分区。若写成完整 ISO 时间戳，解析器不认，会 fallback 到
	// IngestTimeUTC，那时本用例除了今天以外什么都验不到。
	//
	// 时刻取**当前 UTC 时刻**而不是写死的 10:00：`applyClock` 有跨午夜修正——
	// 若“今天的该时刻”比采集基准晚超过 12h，会回拨一天（不把时间猜进未来）。
	// 写死 10:00 在凌晨运行时（基准 08:xx）会被判为“未来 2h”，仍属今天；
	// 但在深夜运行时它会变成“昨天”⇒ 断言今天分区即红。取当前时刻恒安全。
	stamp := time.Now().UTC().Format("15:04:05")
	appendPollTestLines(t, path,
		"["+stamp+"] [Server thread/INFO]: e2e contract probe\n"+
			"["+stamp+"] [Server thread/INFO]: e2e contract probe 2\n")
	m.pollOnce()

	// —— 前置：数据真的进了 VL，否则后面测的是「没投出去」而不是「投出去却查不到」——
	require.NotEmpty(t, fixture.writes, "夹具前提：一轮采集必须真的写进 VL")
	delivered := durableEvents(t, m, sourceID+"/gen-1")
	require.NotEmpty(t, delivered, "夹具前提：投递面必须有事件")
	require.Equal(t, sourceID, delivered[0].Source.LogSourceID,
		"事件携带的 log_source_id 必须是**完整源 id**；这正是 VL 行上的字段值")

	// —— 判据 ①：Catalog 记录建在**事件所属天**，且键用**命名空间**——
	key := catalog.PartitionKey{StorageNamespace: namespace, UTCDay: eventDay}
	rec, ok := cat.Get(key)
	require.True(t, ok,
		"投递后必须为 (StorageNamespace=%s, UTCDay=%s) 建记录；否则查询侧 selectKeys 选不到", namespace, eventDay)
	require.NotNil(t, rec.PublishedProjection,
		"投影结构必须非 nil：querySnapshotLocked 的门禁要求它")
	// 简化投递路径补建的记录**刻意不填**逐源投影（一批数据只写一次、行上无代次标签）。
	require.Empty(t, rec.PublishedProjection.SourceProjections,
		"简化路径的记录不填逐源投影 ⇒ 查询侧必然走「命名空间回退」分支")
	require.Empty(t, rec.PublishedProjection.CoveredSourceGenerations,
		"同上：不得有逐源代次覆盖")

	// —— 判据 ②（★ 本用例的核心）：规划必须产出**非空 Ranges** ——
	//
	// 这一条当时没人断言。目标进了视图、`Queryable=true`，却一条 range 也没有，
	// 于是 service 走「无 range 但 coverage 完整」分支 ⇒ 返回空成功。
	planner := query.NewPlanner(cat, nil)
	// 窗口取「事件当天」的完整一天。
	//
	// 2026-11-05 订正：原先 `ToUTC` 写死为字符串常量 `"2026-11-05T00:00:00Z"`，
	// 一旦 eventDay 变成今天（11-05），区间就退化为 `[11-05, 11-05)` ——**空区间**，→
	// `dayOverlapsRange` 把分区滤掉 ⇒ Ranges 为空，而失败信息会把锅归给产品。
	// 窗口必须由 eventDay 推导，不能与它脱钩。
	dayStart, parseErr := time.Parse("2006-01-02", eventDay)
	require.NoError(t, parseErr)
	res := planner.Plan(query.PlanRequest{
		TargetIDs: []string{namespace},
		TimeRange: query.TimeRange{
			FromUTC: dayStart.Format(time.RFC3339),
			ToUTC:   dayStart.AddDate(0, 0, 1).Format(time.RFC3339),
		},
		Budget: query.Budget{Limit: 10},
	})
	require.Nil(t, res.Err, "规划不得报错")
	require.NotEmpty(t, res.Ranges,
		"★ 投递后必须产出至少一条 range；为空即「数据在 VL 里却查不到」（本次生产故障）")
	require.True(t, res.Coverage.Complete, "覆盖应为完整")

	rng := res.Ranges[0]
	require.Equal(t, namespace, rng.Key.StorageNamespace, "range 的分区键必须用命名空间")
	require.Equal(t, eventDay, rng.Key.UTCDay, "range 的日期必须是事件所属天")
	require.NotNil(t, rng.Projection)
	require.Empty(t, rng.Projection.SourceProjections,
		"range 的投影必须保持「无逐源投影」形态：转成精确形态会让查询换分支")

	// —— 判据 ③：该 range 最终会被标为可查 ——
	require.True(t, res.View.QueryableTargets[key.String()],
		"该分区必须标为可查；否则 planRangesForView 只给覆盖、不给 range")

	t.Logf("端到端通过：投递 %d 条 → Catalog(%s/%s) → Ranges=%d（目标 %s）",
		len(delivered), namespace, eventDay, len(res.Ranges), sourceID)
}

// TestDeliveryPathKeepsNamespaceDistinctFromSourceID 防止夹具退回「同值形状」。
//
// 本文件的存在意义全部来自「命名空间 ≠ 完整源 id」这个事实。若哪天有人把
// 夹具改回两个值相同，上面的端到端用例会继续绿——但**它已经不测那次故障了**。
// 这条用例把「两者必须不同」这个前提本身钉住，让夹具退化立刻可见。
func TestDeliveryPathKeepsNamespaceDistinctFromSourceID(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o644))

	const (
		namespace = "inst:318"
		sourceID  = "inst:318/file"
	)
	require.NotEqual(t, namespace, sourceID,
		"夹具前提：命名空间与完整源 id 必须不同，否则缺陷在测试里不可见")

	source := SourceConfig{
		LogSourceID: sourceID, SourceGeneration: "gen-1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: namespace,
		UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)

	// 跑一轮真实采集，确认链路不把两者合并成同一个值。
	appendPollTestLines(t, path, "[12:00:01] [Server thread/INFO]: shape probe\n")
	m.pollOnce()

	recorded, ok := savedSourceConfig(m, sourceID+"/gen-1")
	require.True(t, ok, "登记后必须能从状态里读到该源的配置")
	require.Equal(t, namespace, recorded.StorageNamespace,
		"分区命名空间必须保持授权粒度（不含流后缀）")
	require.Equal(t, sourceID, recorded.LogSourceID,
		"物理源 id 必须保持带流后缀的完整形态")
	require.NotEqual(t, recorded.StorageNamespace, recorded.LogSourceID,
		"两者相等会让「命名空间 vs 完整源 id」的缺陷在测试里永不暴露")
}

// savedSourceConfig 在读锁下取一份源配置快照（同包的 savedSource 只管 Sources 表）。
func savedSourceConfig(m *Manager, key string) (SourceConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.state.SourceConfigs[key]
	return cfg, ok
}
