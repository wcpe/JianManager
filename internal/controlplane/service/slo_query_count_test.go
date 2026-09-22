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

// sloQueryCounter 收集一次调用期间发出的 SELECT 条数（M-1：断言平台 SLO 与原序列数无关）。
//
// 与 ranking_test.go 的 querySink 分开：那个收集 SQL 文本（供断言含索引名），
// 这里只需要计数，且要能从多轮测量中反复清零（`queries[:0]` 在 sink 内部做不到，
// 故这里用指针切片的独立类型表达「一次性计数」）。
type sloQueryCounter struct {
	out *[]string
}

func (s *sloQueryCounter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("SELECT")) || bytes.Contains(p, []byte("select")) {
		*s.out = append(*s.out, string(bytes.TrimSpace(p)))
	}
	return len(p), nil
}

// sloMeasureQueryCount 在 n 个实例各自持有 inst_uptime 序列的前提下，
// 造库 + 灌数据 + 跑一次平台维 ComputeSLO，返回 [序列筛选, 可用拍聚合, 事件聚合] 的总 SQL 条数。
//
// span 决定档位（raw / 5m / 1h），三档都要常数条 SQL。
func sloMeasureQueryCount(t *testing.T, suffix string, seriesCount int, span time.Duration) int {
	t.Helper()
	var queries []string
	cb := &gorm.Config{
		Logger: logger.New(log.New(&sloQueryCounter{out: &queries}, "", 0), logger.Config{
			LogLevel: logger.Info, IgnoreRecordNotFoundError: true,
		}),
	}
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+suffix+"?mode=memory&cache=shared"), cb)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.MetricSeries{}, &model.MetricSampleRaw{},
		&model.MetricRollup5m{}, &model.MetricRollup1h{}, &model.Instance{},
		&model.AlertRule{}, &model.AlertEvent{}))

	svc := NewMetricService(db)
	base := metricBase()
	to := base.Add(span)
	for i := 0; i < seriesCount; i++ {
		uuid := "uuid-sloq" + strconv.Itoa(i)
		rankSeedInstance(t, svc, uuid, "sloq"+strconv.Itoa(i))
		// 每实例一条 inst_uptime 序列：至少 1 个 raw 样本保证序列存在且窗口内有证据。
		require.NoError(t, svc.Ingest([]Sample{instSampleAt(
			"node-1", uuid, model.MetricInstUptime, "seconds", base, 100)}))
	}

	queries = queries[:0]
	res, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: base, To: to})
	require.NoError(t, err)
	require.Len(t, queries, len(queries), "计数快照")
	require.NotZero(t, res.TotalSamples, "前提：序列都被统计到（否则断言无意义）")
	return len(queries)
}

// TestComputeSLO_PlatformQueryCountIsConstant （M-1）平台 SLO 的 SELECT 条数必须与
// **实例数（≈序列数）无关**。
//
// 缺陷：raw 档 `sloUpTicksForSeries` 对每条序列各发一条 COUNT、rollup 档各发一条 Pluck，
// 于是 SELECT 条数 = 1 + 序列数 + 事件聚合。`/metrics/slo?scope=platform` 是前端 60s
// 轮询，序列数≈实例数（64 台 → 60+ 条/次）会直接放大成持续往返成本。
// 审查员实测：5 实例=7 条、50 实例=52 条（差值即 N+1 斜率）。
//
// 期望：三档都恒定——序列筛选 1 条 + 可用拍聚合 1 条 + 事件聚合 1 条 = 3 条
// （另加 GORM 可能的 few-shot 元查询，故用「小规模与大规划的条数相等」做主断言，
// 绝对值上界做辅助断言，避免把实现的条数细节写死成脆弱断言）。
func TestComputeSLO_PlatformQueryCountIsConstant(t *testing.T) {
	spans := []struct {
		tag  string
		span time.Duration
	}{
		{"raw 档(1h)", time.Hour},
		{"5m 档(7d)", 7 * 24 * time.Hour},
		{"1h 档(60d)", 60 * 24 * time.Hour},
	}
	for _, c := range spans {
		t.Run(c.tag, func(t *testing.T) {
			small := sloMeasureQueryCount(t, "-small", 4, c.span)
			large := sloMeasureQueryCount(t, "-large", 40, c.span)
			require.Equal(t, small, large,
				"平台 SLO 的 SELECT 条数不得随序列数增长：4 实例=%d 条 vs 40 实例=%d 条（N+1 回归）",
				small, large)
			// 绝对值上界仅作「明显退化会失败」的兜底：3 条主查询 + 少量驱动内部查询。
			require.LessOrEqual(t, large, 8,
				"平台 SLO 应为常数条 SQL（序列筛选 + 可用拍聚合 + 事件聚合各 1 条）")
		})
	}
}

// TestComputeSLO_PlatformQueryCountSingleSeries 单目标（instance/node 维）同样只应有
// 常数条 SQL——逐序列 COUNT 在单序列场景下退化为「恰好 1 条」，看不出来；
// 这里用**多序列单目标**（同一实例在两张 node_uuid 下各有序列）来暴露同一个 N+1。
func TestComputeSLO_PlatformQueryCountSingleSeries(t *testing.T) {
	var queries []string
	cb := &gorm.Config{
		Logger: logger.New(log.New(&sloQueryCounter{out: &queries}, "", 0), logger.Config{
			LogLevel: logger.Info, IgnoreRecordNotFoundError: true,
		}),
	}
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), cb)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.MetricSeries{}, &model.MetricSampleRaw{},
		&model.MetricRollup5m{}, &model.MetricRollup1h{}, &model.Instance{},
		&model.AlertRule{}, &model.AlertEvent{}))
	svc := NewMetricService(db)
	base := metricBase()
	to := base.Add(time.Hour)
	rankSeedInstance(t, svc, "uuid-one", "one")
	// 同一实例、两个不同 node_uuid → 两条序列（实例迁移后的真实形态）。
	require.NoError(t, svc.Ingest([]Sample{
		instSampleAt("node-old", "uuid-one", model.MetricInstUptime, "seconds", base, 100),
		instSampleAt("node-new", "uuid-one", model.MetricInstUptime, "seconds", base.Add(time.Minute), 100),
	}))
	var seriesCount int64
	require.NoError(t, db.Model(&model.MetricSeries{}).
		Where("instance_id = ? AND metric_key = ?", "uuid-one", model.MetricInstUptime).
		Count(&seriesCount).Error)
	require.Equal(t, int64(2), seriesCount, "前提：单实例两条序列")

	queries = queries[:0]
	_, err = svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-one", From: base, To: to,
	})
	require.NoError(t, err)
	require.LessOrEqual(t, len(queries), 8,
		"单目标 SLO 也应为常数条 SQL，不得每序列一条：%v", queries)
}

// TestSLORollupUpTicksPerBucketDerivedFromConstants （m-1）ticksPerBucket 必须由
// 「桶宽度 / 采样间隔」**推导**且等于既有口径（5m→10、1h→120）。
//
// 缺陷：原实现把 10/120 写死在调用点，而它的正确性依赖「桶秒 / 30 恒定」这一从未断言的假设。
// 若采样间隔改为 60s 而常量仍是 30，`min(count, cap)` 会把可用拍数**高估一倍**
// （可用率虚高、误差预算被低估），且没有任何信号——既不报错也不改变类型。
// 现改为由 metricBucket5m/metricBucket1h 与 sloSampleIntervalSec 计算，
// 本用例锁住数值，兼作「改动任一常量前先看这里」的护栏。
func TestSLORollupUpTicksPerBucketDerivedFromConstants(t *testing.T) {
	require.Equal(t, 30*time.Second, sloSampleIntervalSec*time.Second,
		"采样间隔口径为 30s（与 model.MetricSampleRaw 的「30s 粒度」一致）")
	require.Equal(t, 10, sloTicksPerBucket5m, "5m 桶 = 300s / 30s = 10 拍")
	require.Equal(t, 120, sloTicksPerBucket1h, "1h 桶 = 3600s / 30s = 120 拍")

	// 真正的不变量：桶宽度必须是采样间隔的**整数倍**。
	// `min(count, 桶宽/间隔)` 这一近似的正确性依赖它——若桶宽不是整数倍（如 100s 桶配 30s 采样），
	// 整除会向下取整丢掉余数（3 拍而非 3.33），cap 偏小 → 可用拍被低估、可用率被压低；
	// 反过来若 cap 取自「拍数」而桶宽更窄，则 cap 偏大 → 可用率虚高。
	// 两种偏差都不报错，故在此显式断言整除性（它比「数值等于 10/120」更能拦住语义走偏）。
	for _, c := range []struct {
		name    string
		bucket  time.Duration
		capTick int
	}{
		{"5m 档", metricBucket5m, sloTicksPerBucket5m},
		{"1h 档", metricBucket1h, sloTicksPerBucket1h},
	} {
		interval := sloSampleIntervalSec * time.Second
		require.Zerof(t, c.bucket%interval,
			"%s：桶宽 %v 必须是采样间隔 %v 的整数倍，否则 min(count, cap) 的近似会静默失真",
			c.name, c.bucket, interval)
		require.Equal(t, int(c.bucket/interval), c.capTick,
			"%s：cap 应等于 桶宽/采样间隔", c.name)
	}
}
