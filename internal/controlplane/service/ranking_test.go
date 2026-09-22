package service

import (
	"bytes"
	"log"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// querySink 收集 GORM 日志中的 SQL 文本（M5：断言查询条数与实例数无关）。
type querySink struct {
	out *[]string
}

func (s *querySink) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("SELECT")) || bytes.Contains(p, []byte("select")) {
		*s.out = append(*s.out, string(bytes.TrimSpace(p)))
	}
	return len(p), nil
}

func rankSeedInstance(t *testing.T, svc *MetricService, uuid, name string) model.Instance {
	t.Helper()
	require.NoError(t, svc.db.AutoMigrate(&model.Instance{}))
	inst := model.Instance{
		UUID: uuid, NodeID: 1, Name: name,
		Type: "minecraft_java", ProcessType: "direct",
		StartCommand: "java -jar server.jar", Status: model.InstanceStatusRunning,
	}
	require.NoError(t, svc.db.Create(&inst).Error)
	return inst
}

func instSampleAt(nodeUUID, instUUID, key, unit string, ts time.Time, v float64) Sample {
	return Sample{
		NodeUUID: nodeUUID, InstanceID: instUUID, Scope: model.MetricScopeInstance,
		MetricKey: key, Unit: unit, TS: ts, Value: fp(v),
	}
}

func TestMetric_InstanceRanking(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Instance{}))
	a := rankSeedInstance(t, svc, "uuid-a", "alpha")
	b := rankSeedInstance(t, svc, "uuid-b", "bravo")
	rankSeedInstance(t, svc, "uuid-c", "charlie") // 有序列但窗口内无样本 → 不入榜

	now := time.Now().UTC()
	require.NoError(t, svc.Ingest([]Sample{
		instSampleAt("node-1", "uuid-a", model.MetricInstTPS, "tps", now, 15),
		instSampleAt("node-1", "uuid-b", model.MetricInstTPS, "tps", now, 20),
		// inst-c 只有窗口外的样本（series 存在、窗口内无数据）。
		instSampleAt("node-1", "uuid-c", model.MetricInstTPS, "tps", now.Add(-time.Hour), 99),
	}))

	res, err := svc.InstanceRanking(RankingQuery{MetricKey: model.MetricInstTPS, Window: 10 * time.Minute})
	require.NoError(t, err)
	require.Equal(t, "asc", res.Order, "TPS 默认低者在前")
	require.Len(t, res.Items, 2)
	require.Equal(t, a.ID, res.Items[0].InstanceID)
	require.Equal(t, 15.0, res.Items[0].Value)
	require.Equal(t, 1, res.Items[0].Rank)
	require.Equal(t, b.ID, res.Items[1].InstanceID)
	require.Equal(t, 2, res.Items[1].Rank)
	require.Equal(t, 1, res.SkippedNoData, "inst-c 有序列但窗口内无样本，不计入榜")
	require.False(t, res.Scoped)

	// limit 生效
	resLimit, err := svc.InstanceRanking(RankingQuery{MetricKey: model.MetricInstTPS, Window: 10 * time.Minute, Limit: 1})
	require.NoError(t, err)
	require.Len(t, resLimit.Items, 1)

	// 显式 desc
	resDesc, err := svc.InstanceRanking(RankingQuery{MetricKey: model.MetricInstTPS, Order: "desc", Window: 10 * time.Minute})
	require.NoError(t, err)
	require.Equal(t, 20.0, resDesc.Items[0].Value)
}

func TestMetric_InstanceRanking_Scoped(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Instance{}))
	// 实例 a 只用于「把可见集之外的实例也放进榜候选」——非管理员不可见，故无需其返回值（m8）。
	rankSeedInstance(t, svc, "uuid-a", "alpha")
	b := rankSeedInstance(t, svc, "uuid-b", "bravo")

	now := time.Now().UTC()
	require.NoError(t, svc.Ingest([]Sample{
		instSampleAt("node-1", "uuid-a", model.MetricInstPlayersOnline, "count", now, 5),
		instSampleAt("node-1", "uuid-b", model.MetricInstPlayersOnline, "count", now, 15),
	}))

	// 非管理员仅可见 b。
	res, err := svc.InstanceRanking(RankingQuery{
		MetricKey: model.MetricInstPlayersOnline, Window: 10 * time.Minute,
		Scoped: true, AllowedIDs: []uint{b.ID},
	})
	require.NoError(t, err)
	require.True(t, res.Scoped)
	require.Len(t, res.Items, 1)
	require.Equal(t, b.ID, res.Items[0].InstanceID)

	// 无可见实例 → 空榜。
	empty, err := svc.InstanceRanking(RankingQuery{
		MetricKey: model.MetricInstPlayersOnline, Window: 10 * time.Minute, Scoped: true, AllowedIDs: []uint{},
	})
	require.NoError(t, err)
	require.Empty(t, empty.Items)
}

// TestMetric_InstanceRanking_ExcludesSoftDeletedInstance （N-1）实例删除是 GORM 软删，行仍在
// instances 表内且其 metric_series 残留；软删实例不得出现在跨实例排行榜（也不得计入 skippedNoData）。
func TestMetric_InstanceRanking_ExcludesSoftDeletedInstance(t *testing.T) {
	svc := newMetricSvc(t)
	live := rankSeedInstance(t, svc, "uuid-live", "live")
	gone := rankSeedInstance(t, svc, "uuid-gone", "gone")

	now := time.Now().UTC()
	require.NoError(t, svc.Ingest([]Sample{
		instSampleAt("node-1", "uuid-live", model.MetricInstTPS, "tps", now, 15),
		// 软删实例在窗口内仍有样本（其序列与历史样本都残留）。
		instSampleAt("node-1", "uuid-gone", model.MetricInstTPS, "tps", now, 20),
	}))
	require.NoError(t, svc.db.Delete(&model.Instance{}, gone.ID).Error, "走 GORM 软删（与 InstanceService.Delete 同语义）")

	// 前提校验：软删行仍在表内（正是缺陷成因），常规查询只看到 live。
	var rawRows int64
	require.NoError(t, svc.db.Unscoped().Model(&model.Instance{}).
		Where("id = ? AND deleted_at IS NOT NULL", gone.ID).Count(&rawRows).Error)
	require.Equal(t, int64(1), rawRows, "软删实例行仍在 instances 表内")

	res, err := svc.InstanceRanking(RankingQuery{MetricKey: model.MetricInstTPS, Window: 10 * time.Minute})
	require.NoError(t, err)
	require.Len(t, res.Items, 1, "软删实例不得进榜")
	require.Equal(t, live.ID, res.Items[0].InstanceID)
	require.Equal(t, 1, res.Items[0].Rank)
	require.Zero(t, res.SkippedNoData, "软删实例既不进榜也不计入「有序列但无样本」")

	// 收敛路径同样不得命中软删实例（AllowedIDs 含它时仍应被 JOIN 过滤掉）。
	resScoped, err := svc.InstanceRanking(RankingQuery{
		MetricKey: model.MetricInstTPS, Window: 10 * time.Minute,
		Scoped: true, AllowedIDs: []uint{gone.ID},
	})
	require.NoError(t, err)
	require.Empty(t, resScoped.Items, "显式可见集里的软删实例同样不进榜")
}

func TestMetric_PlayerTrend_Timezone(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	require.NoError(t, svc.Ingest([]Sample{
		instSampleAt("node-1", "uuid-a", model.MetricInstPlayersOnline, "count", base, 10),
		instSampleAt("node-1", "uuid-a", model.MetricInstPlayersOnline, "count", base.Add(time.Hour), 20),
	}))
	from, to := base.Add(-time.Minute), base.Add(3*time.Hour)

	utc, err := svc.PlayerTrend(PlayerTrendQuery{From: from, To: to, Resolution: "raw", Location: time.UTC})
	require.NoError(t, err)
	require.InDelta(t, 10, utc.HourlyDist[0], 1e-9)
	require.InDelta(t, 20, utc.HourlyDist[1], 1e-9)
	require.InDelta(t, 20, utc.PeakValue, 1e-9)
	require.InDelta(t, 15, utc.DailyAvg, 1e-9)
	require.NotNil(t, utc.PeakAt)
	require.Equal(t, 1, utc.PeakAt.Hour())

	// +8 时区：时段整体右移 8 小时，与 UTC 结果可区分（验收 6）。
	p8 := time.FixedZone("UTC+8", 8*3600)
	east, err := svc.PlayerTrend(PlayerTrendQuery{From: from, To: to, Resolution: "raw", Location: p8})
	require.NoError(t, err)
	require.InDelta(t, 10, east.HourlyDist[8], 1e-9)
	require.InDelta(t, 20, east.HourlyDist[9], 1e-9)
	require.Zero(t, east.HourlyDist[0])
	require.Equal(t, "UTC+8", east.Timezone)
}

func TestMetric_PlayerTrend_Empty(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	res, err := svc.PlayerTrend(PlayerTrendQuery{From: base, To: base.Add(time.Hour), Resolution: "raw"})
	require.NoError(t, err)
	require.Nil(t, res.PeakAt)
	require.Zero(t, res.PeakValue)
	require.Len(t, res.HourlyDist, 24)
}

// TestMetric_InstanceRanking_QueryCountAndIndex （M5）排行必须保持「单条聚合 SQL + 一条计数 SQL」，
// 不随实例数增长退化为 N+1；且 metric_series 上有 (scope, metric_key) 复合索引支撑序列筛选。
func TestMetric_InstanceRanking_QueryCountAndIndex(t *testing.T) {
	// 20 个实例，各一条序列与样本：查询条数应与实例数无关（不得 N+1）。
	now := time.Now().UTC()
	var queries []string
	cb := &gorm.Config{
		Logger: logger.New(log.New(&querySink{out: &queries}, "", 0), logger.Config{
			LogLevel: logger.Info, IgnoreRecordNotFoundError: true,
		}),
	}
	db2, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), cb)
	require.NoError(t, err)
	require.NoError(t, db2.AutoMigrate(&model.MetricSeries{}, &model.MetricSampleRaw{},
		&model.MetricRollup5m{}, &model.MetricRollup1h{}, &model.Instance{}))

	// 索引断言：AutoMigrate 后 (scope, metric_key) 复合索引存在。
	var idxNames []string
	require.NoError(t, db2.Raw("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'metric_series'").
		Scan(&idxNames).Error)
	require.Contains(t, idxNames, "idx_metric_series_scope_metric",
		"metric_series 需有 (scope, metric_key) 复合索引支撑排行/SLO 的序列筛选")

	svc2 := NewMetricService(db2)
	for i := 0; i < 20; i++ {
		uuid := "uuid-q" + strconv.Itoa(i)
		rankSeedInstance(t, svc2, uuid, "q"+strconv.Itoa(i))
		require.NoError(t, svc2.Ingest([]Sample{
			instSampleAt("node-1", uuid, model.MetricInstTPS, "tps", now.Add(-time.Minute), float64(i)),
		}))
	}
	queries = queries[:0]
	_, err = svc2.InstanceRanking(RankingQuery{MetricKey: model.MetricInstTPS, Limit: 50})
	require.NoError(t, err)
	require.NotEmpty(t, queries)
	require.LessOrEqual(t, len(queries), 4,
		"排行应为常数条 SQL（聚合 + 计数），不得随实例数 N+1：%v", queries)
}

// TestMetric_InstanceRanking_AllSupportedMetricsOrder （FR-469 验收 1）5 个白名单指标
// **逐个**锁住默认排序方向与入榜行为：仅 tps 为 asc（吞吐越低越差），
// 其余 mspt / cpu / heap / players 均为 desc。缺任一指标的方向断言时，
// rankingOrder 的「其余 desc」语义只靠实现存在、可被静默改坏。
func TestMetric_InstanceRanking_AllSupportedMetricsOrder(t *testing.T) {
	svc := newMetricSvc(t)

	// 表驱动覆盖全部 5 个白名单指标：metricKey → (单位, 默认方向, 显式覆盖方向)。
	// 显式覆盖恒取默认的反向，确保「asc/desc 优先于指标语义」这一分支也被打到。
	cases := []struct {
		metricKey     string
		unit          string
		defaultOrder  string
		overrideOrder string
	}{
		{model.MetricInstTPS, "tps", "asc", "desc"},
		{model.MetricInstMSPT, "ms", "desc", "asc"},
		{model.MetricInstCPUPct, "pct", "desc", "asc"},
		{model.MetricInstHeapUsed, "bytes", "desc", "asc"},
		{model.MetricInstPlayersOnline, "count", "desc", "asc"},
	}

	// 锁住白名单本身：表必须与 rankingSupportedMetrics 精确同集，
	// 以后新增指标键而未补方向断言时该断言即失败。
	require.Len(t, cases, len(rankingSupportedMetrics),
		"表驱动用例需与 rankingSupportedMetrics 同步，当前白名单 %d 项：%v",
		len(rankingSupportedMetrics), rankingSupportedMetrics)
	for _, c := range cases {
		require.Truef(t, rankingSupportedMetrics[c.metricKey],
			"%s 应在白名单内（或表内加入了非白名单键）", c.metricKey)
	}

	// 三实例：alpha/bravo 窗口内有样本（10 / 30，可区分两个方向），
	// charlie 仅有窗口外样本 → 有序列但不入榜，用于锁 skippedNoData 计数。
	alpha := rankSeedInstance(t, svc, "uuid-alpha", "alpha")
	bravo := rankSeedInstance(t, svc, "uuid-bravo", "bravo")
	rankSeedInstance(t, svc, "uuid-charlie", "charlie")

	now := time.Now().UTC()
	for _, c := range cases {
		require.NoErrorf(t, svc.Ingest([]Sample{
			instSampleAt("node-1", "uuid-alpha", c.metricKey, c.unit, now, 10),
			instSampleAt("node-1", "uuid-bravo", c.metricKey, c.unit, now, 30),
			instSampleAt("node-1", "uuid-charlie", c.metricKey, c.unit, now.Add(-time.Hour), 99),
		}), "%s 写入样本失败", c.metricKey)
	}

	for _, c := range cases {
		// defaultOrder 决定「谁在榜首先」：asc → 小值 alpha，desc → 大值 bravo。
		firstID, firstVal, secondID, secondVal := bravo.ID, 30.0, alpha.ID, 10.0
		if c.defaultOrder == "asc" {
			firstID, firstVal, secondID, secondVal = alpha.ID, 10.0, bravo.ID, 30.0
		}

		t.Run(c.metricKey+"/默认方向", func(t *testing.T) {
			res, err := svc.InstanceRanking(RankingQuery{MetricKey: c.metricKey, Window: 10 * time.Minute})
			require.NoError(t, err)
			require.Equal(t, c.defaultOrder, res.Order,
				"%s 默认排序方向应为 %s", c.metricKey, c.defaultOrder)
			require.Len(t, res.Items, 2, "%s 仅窗口内有样本的实例入榜", c.metricKey)
			require.Equal(t, firstID, res.Items[0].InstanceID, "%s 榜首实例不符", c.metricKey)
			require.InDelta(t, firstVal, res.Items[0].Value, 1e-9)
			require.Equal(t, 1, res.Items[0].Rank)
			require.Equal(t, secondID, res.Items[1].InstanceID, "%s 次位实例不符", c.metricKey)
			require.InDelta(t, secondVal, res.Items[1].Value, 1e-9)
			require.Equal(t, 2, res.Items[1].Rank)
			require.Equal(t, 1, res.SkippedNoData,
				"%s：charlie 有序列但窗口内无样本，应计入 skippedNoData 且不入榜", c.metricKey)
		})

		t.Run(c.metricKey+"/显式覆盖方向", func(t *testing.T) {
			res, err := svc.InstanceRanking(RankingQuery{
				MetricKey: c.metricKey, Order: c.overrideOrder, Window: 10 * time.Minute,
			})
			require.NoError(t, err)
			require.Equal(t, c.overrideOrder, res.Order,
				"显式 %s 应覆盖 %s 的指标语义方向", c.overrideOrder, c.metricKey)
			expectedFirst := bravo.ID
			if c.overrideOrder == "asc" {
				expectedFirst = alpha.ID
			}
			require.Len(t, res.Items, 2)
			require.Equal(t, expectedFirst, res.Items[0].InstanceID,
				"%s 显式 %s 时榜首实例不符", c.metricKey, c.overrideOrder)
		})
	}
}

// TestMetric_InstanceRanking_RejectsUnsupportedMetric 非白名单指标必须报错而非返回空榜，
// 避免前端把「指标不支持」误渲染成「无数据」。
func TestMetric_InstanceRanking_RejectsUnsupportedMetric(t *testing.T) {
	svc := newMetricSvc(t)
	_, err := svc.InstanceRanking(RankingQuery{MetricKey: model.MetricInstHeapMax, Window: 10 * time.Minute})
	require.Error(t, err, "非白名单指标应报错")
	require.False(t, RankingSupportedMetric(model.MetricInstHeapMax))
	require.True(t, RankingSupportedMetric(model.MetricInstTPS))
}

// TestMetric_InstanceRanking_OneSeatPerInstance （M-6）同一实例的多条序列只占**一席**。
//
// 缺陷：主聚合按 `(instance_id, node_uuid)` 分组，而序列身份含 `node_uuid`
// （`MetricSeries` 唯一索引 = node_uuid+instance_id+scope+metric_key+world），
// 故实例换节点后残留的新旧两条序列会各产出一行、rank 各占一名。
// 审查员实测：同一 UUID 同时占 rank 1 与 rank 2，仅 nodeUuid 不同。
//
// spec §1.3 明确「**一个实例一行**，取该实例的实例级序列值」，故这是 bug：
// 跨实例排行榜把同一个实例列两次（同 UUID、同 name），前端表格出现重复行、
// 名次被无意义地占用，且 `skippedNoData`（按 DISTINCT instance_id 计）与
// `items`（按 (instance_id,node_uuid) 计）口径不一致。
func TestMetric_InstanceRanking_OneSeatPerInstance(t *testing.T) {
	svc := newMetricSvc(t)
	inst := rankSeedInstance(t, svc, "uuid-migrated", "migrated")
	other := rankSeedInstance(t, svc, "uuid-other", "other")

	now := time.Now().UTC()
	// 同一实例、两个不同 node_uuid → 两条序列（实例迁移后的真实形态）。
	// 旧序列在窗口中段、新序列在窗口后段（与真实迁移一致：旧序列停止更新）。
	require.NoError(t, svc.Ingest([]Sample{
		instSampleAt("node-old", "uuid-migrated", model.MetricInstPlayersOnline, "count", now.Add(-8*time.Minute), 8),
		instSampleAt("node-new", "uuid-migrated", model.MetricInstPlayersOnline, "count", now.Add(-1*time.Minute), 8),
		instSampleAt("node-1", "uuid-other", model.MetricInstPlayersOnline, "count", now.Add(-2*time.Minute), 3),
	}))
	// 前提校验：该实例确有两条序列（否则本用例打不到目标分支）。
	var seriesCount int64
	require.NoError(t, svc.db.Model(&model.MetricSeries{}).
		Where("instance_id = ? AND metric_key = ?", "uuid-migrated", model.MetricInstPlayersOnline).
		Count(&seriesCount).Error)
	require.Equal(t, int64(2), seriesCount, "前提：同一实例两条序列")

	res, err := svc.InstanceRanking(RankingQuery{
		MetricKey: model.MetricInstPlayersOnline, Window: 10 * time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 2, "两个实例 → 两行（不得因多序列裂成三行）")

	// 每个实例 ID 只出现一次，且名次连续 1..N。
	seen := map[uint]int{}
	for i, it := range res.Items {
		seen[it.InstanceID]++
		require.Equal(t, i+1, it.Rank, "名次应连续递增、不跳号")
	}
	require.Equal(t, 1, seen[inst.ID], "同一实例只占一席")
	require.Equal(t, 1, seen[other.ID])

	// 迁移实例的代表值 = 两条序列**全体样本**的窗口均值（8 与 8 → 8），
	// 而不是「两条各自均值各占一行」。
	for _, it := range res.Items {
		if it.InstanceID == inst.ID {
			require.InDelta(t, 8.0, it.Value, 1e-9, "同实例多序列应合为一个代表值")
			require.Equal(t, "node-new", it.NodeUUID,
				"nodeUuid 应取自窗口内最新样本所属序列（实例迁移后显示当前节点）")
			require.Equal(t, now.Add(-1*time.Minute).Unix(), it.SampledAt.Unix(),
				"sampled_at 取窗口内最新一拍")
		}
	}
}

// TestMetric_InstanceRanking_MultiSeriesAggregatesNotSummed （M-6 语义）同一实例的
// 多序列是「同一测量的先后两段」，代表值必须是均值而非求和——若误用 SUM，
// 实例换节点后这个指标会凭空翻倍（与 capacity.go 的 B-2 修复同口径：
// 实例级序列收敛为一条后**不相加**）。
func TestMetric_InstanceRanking_MultiSeriesAggregatesNotSummed(t *testing.T) {
	svc := newMetricSvc(t)
	rankSeedInstance(t, svc, "uuid-multi", "multi")

	now := time.Now().UTC()
	require.NoError(t, svc.Ingest([]Sample{
		instSampleAt("node-old", "uuid-multi", model.MetricInstHeapUsed, "bytes", now.Add(-5*time.Minute), 100),
		instSampleAt("node-new", "uuid-multi", model.MetricInstHeapUsed, "bytes", now.Add(-1*time.Minute), 200),
	}))

	res, err := svc.InstanceRanking(RankingQuery{
		MetricKey: model.MetricInstHeapUsed, Window: 10 * time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	require.InDelta(t, 150.0, res.Items[0].Value, 1e-9,
		"两条序列各 100/200 → 均值 150（不是求和 300，也不是只取其中一条）")
}
