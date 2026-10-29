package service

import (
	"bytes"
	"log"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 对外只读统计契约服务层测试。
//
// 重点守三件事：
//  1. 5m/1h 档**绝不**打原始表（spec §5），用真实 SQL 日志断言；
//  2. 分页/时间窗超限返回明确错误且带附加字段（spec §4.2/§4.3）；
//  3. 可见范围收敛不退化成「不加过滤」（否则就是越权读）。

// newStatsSvc 建独立内存库并迁移统计相关全部表。
func newStatsSvc(t *testing.T, name string) *StatsService {
	t.Helper()
	dsn := "file:" + t.Name() + name + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.MetricSeries{},
		&model.MetricSampleRaw{}, &model.MetricRollup5m{}, &model.MetricRollup1h{},
		&model.LogEntry{}, &model.InstanceCrashStat{}, &model.InstanceRollingOp{},
	))
	return NewStatsService(db)
}

// newStatsSvcWithSQLLog 同上，但把执行过的 SQL 语句收集到 out（含表名，供「查了哪张表」断言）。
func newStatsSvcWithSQLLog(t *testing.T, name string, out *[]string) *StatsService {
	t.Helper()
	dsn := "file:" + t.Name() + name + "?mode=memory&cache=shared"
	cb := &gorm.Config{
		Logger: logger.New(log.New(&sqlTableSink{out: out}, "", 0), logger.Config{
			LogLevel: logger.Info, IgnoreRecordNotFoundError: true,
		}),
	}
	db, err := gorm.Open(sqlite.Open(dsn), cb)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.MetricSeries{},
		&model.MetricSampleRaw{}, &model.MetricRollup5m{}, &model.MetricRollup1h{},
		&model.LogEntry{}, &model.InstanceCrashStat{}, &model.InstanceRollingOp{},
	))
	return NewStatsService(db)
}

// sqlTableSink 收集全部 SQL 行（不筛关键字：raw 表可能出现在 COUNT/子查询里，
// 预先按 "SELECT" 过滤会漏掉 GORM 用别的关键字开头的语句）。
type sqlTableSink struct{ out *[]string }

func (s *sqlTableSink) Write(p []byte) (int, error) {
	*s.out = append(*s.out, string(bytes.TrimSpace(p)))
	return len(p), nil
}

// statsMetricBase 固定基准时刻，避免用例随真实时钟漂移。
func statsMetricBase() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }

// seedStatsInstance 建一个实例及其一条指标序列，返回实例与序列。
func seedStatsInstance(t *testing.T, svc *StatsService, nodeID uint, uuid, name string, metricKey string) (model.Instance, model.MetricSeries) {
	t.Helper()
	inst := model.Instance{
		UUID: uuid, NodeID: nodeID, Name: name,
		Type: model.InstanceTypeMinecraftJava, ProcessType: "direct",
		StartCommand: "java -jar server.jar", Status: model.InstanceStatusRunning,
	}
	require.NoError(t, svc.db.Create(&inst).Error)
	sr := model.MetricSeries{
		NodeUUID: "node-uuid-1", InstanceID: uuid,
		Scope: model.MetricScopeInstance, MetricKey: metricKey, Unit: "pct",
	}
	require.NoError(t, svc.db.Create(&sr).Error)
	return inst, sr
}

// TestStats_Resolution5mNeverTouchesRaw 守 spec §5 的硬约束：
// resolution=5m / 1h 的 SQL 里**不得出现** metric_sample_raws 表。
//
// 这是「必须走 rollup 表」的唯一守门测试：只要有人把 loadPoints 的档位分流改错
// （例如 default 分支落到原始表），本用例立刻转红。
func TestStats_Resolution5mNeverTouchesRaw(t *testing.T) {
	for _, resolution := range []string{"5m", "1m", "15s", "1h"} {
		t.Run(resolution, func(t *testing.T) {
			var queries []string
			svc := newStatsSvcWithSQLLog(t, "r"+resolution, &queries)
			base := statsMetricBase()
			_, sr := seedStatsInstance(t, svc, 1, "uuid-a", "alpha", model.MetricInstCPUPct)
			// 两张表都放数据：若实现查错表，会因为「有数据」而看不出来，
			// 故断言只依赖 SQL 文本，不依赖行数。
			require.NoError(t, svc.db.Create(&model.MetricSampleRaw{
				SeriesID: sr.ID, TS: base, Value: fp(11),
			}).Error)
			require.NoError(t, svc.db.Create(&model.MetricRollup5m{
				SeriesID: sr.ID, BucketTS: base.Truncate(5 * time.Minute), Avg: 22, Count: 1,
			}).Error)

			queries = queries[:0]
			_, err := svc.QueryInstanceSeries(StatsMetricQuery{
				From: base.Add(-time.Hour), To: base,
				Resolution: StatsResolution(resolution),
				Page:       1, PageSize: 100,
			}, Unscoped())
			require.NoError(t, err)

			require.NotEmpty(t, queries)
			var all bytes.Buffer
			for _, q := range queries {
				all.WriteString(q)
				all.WriteString("\n")
			}
			sqlText := all.String()
			require.NotContains(t, sqlText, "metric_sample_raws",
				"resolution=%s 绝不允许查原始表：\n%s", resolution, sqlText)
		})
	}
}

// TestStats_ResolutionRawOnlyUnderOneHour 守「raw 仅允许 < 1h」：
// 超窗时明确拒绝并给出 maxRange，**不静默降级到预聚合档**（降级会让调用方
// 以为拿到的是原始采样）。
func TestStats_ResolutionRawOnlyUnderOneHour(t *testing.T) {
	res, stErr := NormalizeStatsResolution("raw", 30*time.Minute)
	require.Nil(t, stErr)
	require.Equal(t, StatsResolutionRaw, res)

	_, stErr = NormalizeStatsResolution("raw", time.Hour)
	require.NotNil(t, stErr)
	require.Equal(t, StatsCodeTimeRangeExceeded, stErr.Code)
	require.Equal(t, "1h", stErr.MaxRange)

	_, stErr = NormalizeStatsResolution("bogus", time.Minute)
	require.NotNil(t, stErr)
	require.Equal(t, StatsCodeInvalidArgument, stErr.Code)
}

// TestStats_ResolutionRawQueryHitsRawTable 反向断言：raw 档**确实**查原始表。
//
// 没有这一条，「不查 raw 表」可以被「永远不查任何表」通过。
func TestStats_ResolutionRawQueryHitsRawTable(t *testing.T) {
	var queries []string
	svc := newStatsSvcWithSQLLog(t, "raw", &queries)
	base := statsMetricBase()
	_, sr := seedStatsInstance(t, svc, 1, "uuid-a", "alpha", model.MetricInstCPUPct)
	require.NoError(t, svc.db.Create(&model.MetricSampleRaw{
		SeriesID: sr.ID, TS: base.Add(-time.Minute), Value: fp(33),
	}).Error)

	queries = queries[:0]
	res, err := svc.QueryInstanceSeries(StatsMetricQuery{
		From: base.Add(-30 * time.Minute), To: base,
		Resolution: StatsResolutionRaw, Page: 1, PageSize: 100,
	}, Unscoped())
	require.NoError(t, err)
	require.Len(t, res.Series, 1)
	require.Len(t, res.Series[0].Points, 1)
	require.InDelta(t, 33, res.Series[0].Points[0].V, 1e-9)

	var all bytes.Buffer
	for _, q := range queries {
		all.WriteString(q)
		all.WriteString("\n")
	}
	require.Contains(t, all.String(), "metric_sample_raws")
}

// TestStats_PaginationLimit 守 spec §4.2：超上限返回 PAGE_SIZE_EXCEEDED 且带 maxPageSize，
// **不静默截断**。
func TestStats_PaginationLimit(t *testing.T) {
	page, size, stErr := NormalizeStatsPagination(1, StatsMaxPageSize)
	require.Nil(t, stErr)
	require.Equal(t, 1, page)
	require.Equal(t, StatsMaxPageSize, size)

	_, _, stErr = NormalizeStatsPagination(1, StatsMaxPageSize+1)
	require.NotNil(t, stErr)
	require.Equal(t, StatsCodePageSizeExceeded, stErr.Code)
	require.Equal(t, StatsMaxPageSize, stErr.MaxPageSize)

	// 缺省值回填。
	page, size, stErr = NormalizeStatsPagination(0, 0)
	require.Nil(t, stErr)
	require.Equal(t, 1, page)
	require.Equal(t, StatsDefaultPageSize, size)
}

// TestStats_TimeRangeLimits 守 spec §4.3 的三档上限。
func TestStats_TimeRangeLimits(t *testing.T) {
	now := statsMetricBase()

	// 指标：上限 30 天。
	from := now.Add(-StatsMetricsMaxRange)
	_, _, stErr := NormalizeStatsTimeRange(&from, &now, StatsMetricsDefaultRange,
		StatsMetricsMaxRange, StatsMetricsMaxRangeLabel, now)
	require.Nil(t, stErr)

	from = now.Add(-StatsMetricsMaxRange - time.Minute)
	_, _, stErr = NormalizeStatsTimeRange(&from, &now, StatsMetricsDefaultRange,
		StatsMetricsMaxRange, StatsMetricsMaxRangeLabel, now)
	require.NotNil(t, stErr)
	require.Equal(t, StatsCodeTimeRangeExceeded, stErr.Code)
	require.Equal(t, "720h", stErr.MaxRange)

	// 日志：上限 7 天。
	from = now.Add(-StatsLogsMaxRange - time.Minute)
	_, _, stErr = NormalizeStatsTimeRange(&from, &now, StatsLogsDefaultRange,
		StatsLogsMaxRange, StatsLogsMaxRangeLabel, now)
	require.NotNil(t, stErr)
	require.Equal(t, "168h", stErr.MaxRange)

	// to 早于 from：形状错误。
	badFrom := now
	badTo := now.Add(-time.Hour)
	_, _, stErr = NormalizeStatsTimeRange(&badFrom, &badTo, time.Hour, StatsMetricsMaxRange,
		StatsMetricsMaxRangeLabel, now)
	require.NotNil(t, stErr)
	require.Equal(t, StatsCodeInvalidArgument, stErr.Code)
}

// TestStats_CrashDaysLimit 崩滩趋势窗口：365 天上限，超出明确拒绝。
func TestStats_CrashDaysLimit(t *testing.T) {
	days, stErr := NormalizeStatsCrashDays(0)
	require.Nil(t, stErr)
	require.Equal(t, StatsCrashDefaultDays, days)

	days, stErr = NormalizeStatsCrashDays(StatsCrashMaxDays)
	require.Nil(t, stErr)
	require.Equal(t, StatsCrashMaxDays, days)

	_, stErr = NormalizeStatsCrashDays(StatsCrashMaxDays + 1)
	require.NotNil(t, stErr)
	require.Equal(t, StatsCodeTimeRangeExceeded, stErr.Code)
	require.NotEmpty(t, stErr.MaxRange)

	_, stErr = NormalizeStatsCrashDays(-1)
	require.NotNil(t, stErr)
	require.Equal(t, StatsCodeInvalidArgument, stErr.Code)
}

// TestStats_EmptyResultsAreArrays 守「空数据返回空数组而非 null」：
// 六组响应里的每个数组字段都必须已初始化。
func TestStats_EmptyResultsAreArrays(t *testing.T) {
	svc := newStatsSvc(t, "empty")

	ov, err := svc.Overview(Unscoped())
	require.NoError(t, err)
	require.Zero(t, ov.Nodes.Total)

	series, err := svc.QueryInstanceSeries(StatsMetricQuery{
		From: time.Now().Add(-time.Hour), To: time.Now(),
		Resolution: StatsResolution5m, Page: 1, PageSize: 100,
	}, Unscoped())
	require.NoError(t, err)
	require.NotNil(t, series.Series, "series 必须是 []，不能是 null")
	require.Empty(t, series.Series)

	nodeSeries, err := svc.QueryNodeSeries(StatsMetricQuery{
		From: time.Now().Add(-time.Hour), To: time.Now(),
		Resolution: StatsResolution5m, Page: 1, PageSize: 100,
	}, Unscoped())
	require.NoError(t, err)
	require.NotNil(t, nodeSeries.Series)

	logs, err := svc.QueryLogs(StatsLogQuery{
		From: time.Now().Add(-time.Hour), To: time.Now(), Page: 1, PageSize: 100,
	}, Unscoped())
	require.NoError(t, err)
	require.NotNil(t, logs.Entries, "entries 必须是 []，不能是 null")

	crashes, err := svc.QueryCrashTrend(StatsCrashQuery{Days: 30}, Unscoped())
	require.NoError(t, err)
	require.NotNil(t, crashes.ByDay, "byDay 必须是 []，不能是 null")
	require.NotNil(t, crashes.ByReason)

	changes, err := svc.ListInFlightChanges(StatsChangeQuery{Page: 1, PageSize: 100})
	require.NoError(t, err)
	require.NotNil(t, changes.Changes, "changes 必须是 []，不能是 null")
}

// TestStats_OverviewCounts 总览计数与资源水位口径。
func TestStats_OverviewCounts(t *testing.T) {
	svc := newStatsSvc(t, "ov")
	require.NoError(t, svc.db.Create(&model.Node{
		UUID: "n1", Name: "n1", Host: "h1", Status: model.NodeStatusOnline,
		CPUCores: 8, MemoryMB: 32768, MemoryUsedMB: 16384, CPUUsage: 40,
	}).Error)
	require.NoError(t, svc.db.Create(&model.Node{
		UUID: "n2", Name: "n2", Host: "h2", Status: model.NodeStatusOnline,
		Maintenance: true, CPUCores: 4, MemoryMB: 16384, MemoryUsedMB: 8192, CPUUsage: 20,
	}).Error)
	// 离线节点：计总数与 offline，但不计入资源水位（读数是陈旧值）。
	require.NoError(t, svc.db.Create(&model.Node{
		UUID: "n3", Name: "n3", Host: "h3", Status: model.NodeStatusOffline,
		CPUCores: 100, MemoryMB: 1024, MemoryUsedMB: 1024, CPUUsage: 99,
	}).Error)

	for _, s := range []model.InstanceStatus{
		model.InstanceStatusRunning, model.InstanceStatusRunning,
		model.InstanceStatusStopped, model.InstanceStatusCrashed,
	} {
		require.NoError(t, svc.db.Create(&model.Instance{
			UUID: "i-" + string(s) + "-" + time.Now().Format("150405.000000000"), NodeID: 1, Name: "i",
			Type: model.InstanceTypeMinecraftJava, ProcessType: "direct",
			StartCommand: "java", Status: s,
		}).Error)
	}

	ov, err := svc.Overview(Unscoped())
	require.NoError(t, err)
	require.Equal(t, 3, ov.Nodes.Total)
	require.Equal(t, 2, ov.Nodes.Online)
	require.Equal(t, 1, ov.Nodes.Maintenance)
	require.Equal(t, 4, ov.Instances.Total)
	require.Equal(t, 2, ov.Instances.Running)
	require.Equal(t, 1, ov.Instances.Stopped)
	require.Equal(t, 1, ov.Instances.Crashed)
	// 只算在线节点：8+4 核、32+16 GB 内存。
	require.Equal(t, 12, ov.Resources.CPUCores)
	require.Equal(t, int64(49152), ov.Resources.MemoryTotalMb)
	require.Equal(t, int64(24576), ov.Resources.MemoryUsedMb)
	require.InDelta(t, 30, ov.Resources.CPUUsagePct, 1e-9)
	// 未注入探针判定时一律视为未连入。
	require.Equal(t, 0, ov.Probes.Connected)
	require.Equal(t, 4, ov.Probes.Disconnected)
}

// TestStats_ScopeConvergence 守「可见范围收敛绝不退化成不加过滤」。
func TestStats_ScopeConvergence(t *testing.T) {
	svc := newStatsSvc(t, "scope")
	base := statsMetricBase()
	a, srA := seedStatsInstance(t, svc, 1, "uuid-a", "alpha", model.MetricInstCPUPct)
	_, srB := seedStatsInstance(t, svc, 1, "uuid-b", "bravo", model.MetricInstCPUPct)
	require.NoError(t, svc.db.Create(&model.MetricSampleRaw{
		SeriesID: srA.ID, TS: base, Value: fp(1),
	}).Error)
	require.NoError(t, svc.db.Create(&model.MetricSampleRaw{
		SeriesID: srB.ID, TS: base, Value: fp(2),
	}).Error)

	// 只可见 a。
	scoped := StatsScope{Scoped: true, InstanceIDs: []uint{a.ID}}
	res, err := svc.QueryInstanceSeries(StatsMetricQuery{
		From: base.Add(-time.Hour), To: base,
		Resolution: StatsResolutionRaw, Page: 1, PageSize: 100,
	}, scoped)
	require.NoError(t, err)
	require.Len(t, res.Series, 1)
	require.Equal(t, a.ID, res.Series[0].InstanceID)

	// 什么都看不到：必须返回空而不是全量。
	empty := StatsScope{Scoped: true, InstanceIDs: []uint{}}
	res, err = svc.QueryInstanceSeries(StatsMetricQuery{
		From: base.Add(-time.Hour), To: base,
		Resolution: StatsResolutionRaw, Page: 1, PageSize: 100,
	}, empty)
	require.NoError(t, err)
	require.Empty(t, res.Series)

	ov, err := svc.Overview(empty)
	require.NoError(t, err)
	require.Zero(t, ov.Instances.Total)

	// 显式指定越界实例：视为不可见，不泄露存在性。
	res, err = svc.QueryInstanceSeries(StatsMetricQuery{
		InstanceIDs: []uint{a.ID + 999},
		From:        base.Add(-time.Hour), To: base,
		Resolution: StatsResolutionRaw, Page: 1, PageSize: 100,
	}, scoped)
	require.NoError(t, err)
	require.Empty(t, res.Series)
}

// TestStats_MetricNameStripsScopePrefix 对外指标名剥掉 scope 前缀（契约示例是 cpu_pct / tps）。
func TestStats_MetricNameStripsScopePrefix(t *testing.T) {
	require.Equal(t, "cpu_pct", statsMetricName(model.MetricScopeInstance, model.MetricInstCPUPct))
	require.Equal(t, "tps", statsMetricName(model.MetricScopeInstance, model.MetricInstTPS))
	require.Equal(t, "cpu_pct", statsMetricName(model.MetricScopeNode, model.MetricNodeCPUPct))
	// 未知前缀原样返回，避免新增指标键被静默改名。
	require.Equal(t, "custom_thing", statsMetricName(model.MetricScopeInstance, "custom_thing"))
}

// TestStats_LogsQuery 日志检索：级别/关键字/时间窗过滤与字段投影。
func TestStats_LogsQuery(t *testing.T) {
	svc := newStatsSvc(t, "logs")
	base := statsMetricBase()
	require.NoError(t, svc.db.Create(&model.LogEntry{
		Source: model.LogSourceInstance, Level: model.LogLevelError, InstanceID: 7,
		NodeID: 1, Stream: "stderr", Message: "Exception in tick", Time: base.Add(-time.Minute),
	}).Error)
	require.NoError(t, svc.db.Create(&model.LogEntry{
		Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 7,
		NodeID: 1, Stream: "stdout", Message: "Loaded 12 chunks", Time: base.Add(-2 * time.Minute),
	}).Error)

	all, err := svc.QueryLogs(StatsLogQuery{
		From: base.Add(-time.Hour), To: base, Page: 1, PageSize: 100,
	}, Unscoped())
	require.NoError(t, err)
	require.Equal(t, 2, all.Pagination.Total)
	require.Len(t, all.Entries, 2)
	// 倒序：最新的在前。
	require.Equal(t, "Exception in tick", all.Entries[0].Message)
	// 级别投影为契约的大写枚举。
	require.Equal(t, "ERROR", all.Entries[0].Level)
	require.Equal(t, "INFO", all.Entries[1].Level)
	require.Equal(t, "stderr", all.Entries[0].Source)

	errOnly, err := svc.QueryLogs(StatsLogQuery{
		Level: "ERROR", From: base.Add(-time.Hour), To: base, Page: 1, PageSize: 100,
	}, Unscoped())
	require.NoError(t, err)
	require.Equal(t, 1, errOnly.Pagination.Total)

	kw, err := svc.QueryLogs(StatsLogQuery{
		Keyword: "chunks", From: base.Add(-time.Hour), To: base, Page: 1, PageSize: 100,
	}, Unscoped())
	require.NoError(t, err)
	require.Equal(t, 1, kw.Pagination.Total)

	// 非法级别：明确 400 语义，不静默忽略。
	_, err = svc.QueryLogs(StatsLogQuery{
		Level: "TRACE", From: base.Add(-time.Hour), To: base, Page: 1, PageSize: 100,
	}, Unscoped())
	require.Error(t, err)
	stErr, ok := err.(*StatsError)
	require.True(t, ok)
	require.Equal(t, StatsCodeInvalidArgument, stErr.Code)
}

// TestStats_CrashTrend 崩溃趋势：按天 + 按根因聚合，且一天多指纹要合并（不得逐行 append）。
func TestStats_CrashTrend(t *testing.T) {
	svc := newStatsSvc(t, "crash")
	today := "2026-06-01"
	yesterday := "2026-05-31"
	for _, row := range []model.InstanceCrashStat{
		{InstanceID: 1, BucketDay: today, RootCause: "OOM", Signature: "sig-a", Count: 2},
		{InstanceID: 1, BucketDay: today, RootCause: "OOM", Signature: "sig-b", Count: 3},
		{InstanceID: 1, BucketDay: yesterday, RootCause: "MOD_CONFLICT", Signature: "sig-c", Count: 1},
	} {
		require.NoError(t, svc.db.Create(&row).Error)
	}

	res, err := svc.QueryCrashTrend(StatsCrashQuery{Days: 365}, Unscoped())
	require.NoError(t, err)
	// 同一天的两个指纹必须合并成 1 个点、count 求和。
	require.Len(t, res.ByDay, 2)
	require.Equal(t, yesterday, res.ByDay[0].Date)
	require.Equal(t, 1, res.ByDay[0].Count)
	require.Equal(t, today, res.ByDay[1].Date)
	require.Equal(t, 5, res.ByDay[1].Count)
	// 根因按计数倒序。
	require.Len(t, res.ByReason, 2)
	require.Equal(t, "OOM", res.ByReason[0].Reason)
	require.Equal(t, 5, res.ByReason[0].Count)
}

// TestStats_Changes 在途变更：状态映射 + 分页。
func TestStats_Changes(t *testing.T) {
	svc := newStatsSvc(t, "changes")
	for _, op := range []model.InstanceRollingOp{
		{Action: "restart", State: model.RollingStateRunning, Requested: 12, Succeeded: 5},
		{Action: "stop", State: model.RollingStatePaused, Requested: 3},
		{Action: "start", State: model.RollingStateDone, Requested: 2, Succeeded: 2},
	} {
		require.NoError(t, svc.db.Create(&op).Error)
	}

	statuses, stErr := ParseStatsChangeStatuses("")
	require.Nil(t, stErr)
	res, err := svc.ListInFlightChanges(StatsChangeQuery{Statuses: statuses, Page: 1, PageSize: 100})
	require.NoError(t, err)
	// 终态（done）不算在途。
	require.Equal(t, 2, res.Pagination.Total)
	require.Len(t, res.Changes, 2)
	for _, c := range res.Changes {
		require.Contains(t, []string{"rolling", "paused"}, c.Status)
		require.NotEmpty(t, c.Title)
	}

	// 显式过滤 paused。
	paused, stErr := ParseStatsChangeStatuses("paused")
	require.Nil(t, stErr)
	res, err = svc.ListInFlightChanges(StatsChangeQuery{Statuses: paused, Page: 1, PageSize: 100})
	require.NoError(t, err)
	require.Equal(t, 1, res.Pagination.Total)
	require.Equal(t, "paused", res.Changes[0].Status)

	// 未知状态名：明确拒绝，不静默忽略（忽略会让调用方以为过滤生效）。
	_, stErr = ParseStatsChangeStatuses("nope")
	require.NotNil(t, stErr)
	require.Equal(t, StatsCodeInvalidArgument, stErr.Code)
}

// TestStats_RateLimiter 守 spec §4.4：每 token 每分钟 120 次，超限给 Retry-After。
func TestStats_RateLimiter(t *testing.T) {
	now := statsMetricBase()
	limiter := NewStatsRateLimiter(StatsRateLimitPerMinute, time.Minute)
	limiter.SetClockForTest(func() time.Time { return now })

	for i := 0; i < StatsRateLimitPerMinute; i++ {
		remaining, _, ok := limiter.Allow("agent:1")
		require.True(t, ok, "第 %d 次应放行", i+1)
		require.Equal(t, StatsRateLimitPerMinute-i-1, remaining)
	}
	remaining, retryAfter, ok := limiter.Allow("agent:1")
	require.False(t, ok, "第 121 次必须被拒")
	require.Zero(t, remaining)
	require.GreaterOrEqual(t, retryAfter, 1)

	// 另一个 token 不受影响（按 token 计费，不是全局桶）。
	_, _, ok = limiter.Allow("agent:2")
	require.True(t, ok)

	// 窗口滑过后恢复。
	now = now.Add(2 * time.Minute)
	_, _, ok = limiter.Allow("agent:1")
	require.True(t, ok)
}

// ── StatsScope 的零值语义（权限原语 fail-open 修复）─────────────────
//
// ## 守的是什么
//
// 原先用 `Scoped bool`：`!Scoped` 即「不收敛」，于是 **零值 `StatsScope{}` 天然看到全部**。
// 这是 fail-open 的默认值——任何忘记构造范围的调用方（新增端点、错误分支里
// `return StatsScope{}, ...` 之后被误用）都会静默获得全平台可见性。
//
// 改成显式 `AllowAll` 后：零值 = 什么都看不到。漏赋值只会让用户看不到数据
// （立刻能被发现），而不是看到别人的数据（谁也不会发现）。

// TestStatsScope_ZeroValueSeesNothing 零值必须什么都看不到。
func TestStatsScope_ZeroValueSeesNothing(t *testing.T) {
	var zero StatsScope
	assert.False(t, zero.allowsAnyInstance(),
		"零值不得看到任何实例——这是 fail-closed 的底线")
	assert.False(t, zero.allowsAllNodes(),
		"零值不得看到全部节点")
}

// TestStatsScope_UnscopedAllowsAll 显式 Unscoped 才是不受限。
func TestStatsScope_UnscopedAllowsAll(t *testing.T) {
	all := Unscoped()
	assert.True(t, all.AllowAll, "Unscoped 必须显式置 AllowAll")
	assert.True(t, all.allowsAnyInstance())
	assert.True(t, all.allowsAllNodes())
}

// TestStatsScope_ScopedWithInstancesOnlySeesThose 有实例白名单时只看到列出的。
func TestStatsScope_ScopedWithInstancesOnlySeesThose(t *testing.T) {
	s := StatsScope{Scoped: true, InstanceIDs: []uint{1, 2}}
	assert.True(t, s.allowsAnyInstance())
	assert.False(t, s.allowsAllNodes(), "未置 AllNodes 时节点维度不得放开")
}

// TestStatsScope_ScopedWithEmptyInstancesSeesNothing 空白名单等于什么都看不到。
func TestStatsScope_ScopedWithEmptyInstancesSeesNothing(t *testing.T) {
	s := StatsScope{Scoped: true, InstanceIDs: []uint{}, NodeIDs: []uint{}}
	assert.False(t, s.allowsAnyInstance())
	assert.False(t, s.allowsAllNodes())
}

// TestStatsScope_AllowAllIsTheOnlyWayToUnrestrict 只有 AllowAll 能放开实例维度。
//
// 这条把「放开」收敛到**一个显式开关**：将来若有人给 StatsScope 加字段，
// 不会因为某个字段的零值恰好满足了旧判据而意外获得全量可见性。
func TestStatsScope_AllowAllIsTheOnlyWayToUnrestrict(t *testing.T) {
	candidates := []StatsScope{
		{},
		{Scoped: true},
		{AllNodes: true}, // 旧实现里这会让实例维度也放开
		{Scoped: true, AllNodes: true},
	}
	for i, s := range candidates {
		if s.AllowAll {
			continue
		}
		assert.False(t, s.allowsAnyInstance(),
			"候选 %d（%+v）不得在未置 AllowAll 时放开实例维度", i, s)
	}
}
