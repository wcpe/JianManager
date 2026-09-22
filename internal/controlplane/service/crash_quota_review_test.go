package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestCrashCorrelation_RSSIsSumOverLatestSample R6：RSS 口径必须是「最新一拍的整棵进程树求和」。
//
// 缺陷现场：原实现用 `MAX(rss_bytes)`，得到的是**单进程峰值**，而 `MemLimitMB` 是整机
// 容器上限，二者不同量纲。MC 场景下 JVM 主进程通常最大，MAX 多数时候碰巧接近真值；
// 但心跳样本被裁为「每实例最多 10 个进程、按 CPU 排序」，主进程一旦 CPU 偏低就被挤出样本，
// MAX 系统性低报 → NearOOM（RSS >= 0.9×limit）漏判——正是要查 OOM 时最不该给的错误结论。
//
// 本测试构造「主进程被截断、只剩两个小进程」的形态：SUM 之和逼近上限而 MAX 远低于它，
// 断言 NearOOM 为 true（MAX 口径下会是 false）。
func TestCrashCorrelation_RSSIsSumOverLatestSample(t *testing.T) {
	db := newCrashTestDB(t)
	require.NoError(t, db.Create(&model.Node{Name: "n1", Host: "h", Secret: "s"}).Error)
	inst := seedCrashInstance(t, db, "smp")
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("mem_limit_mb", 1024).Error)

	crashAt := time.Now().UTC()
	sampleAt := crashAt.Add(-time.Minute)
	// 同一拍的两个子进程：各自 500MiB，单看任何一个都不到 0.9×1024=921MiB。
	// SUM = 1000MiB ≥ 921MiB → 逼近上限；MAX = 500MiB → 判定为「未逼近」（漏判）。
	for _, pid := range []int32{101, 102} {
		require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
			NodeUUID: "node-uuid", InstanceUUID: inst.UUID, PID: pid,
			RSSBytes: 500 * 1024 * 1024, SampledAt: sampleAt,
		}).Error)
	}
	// 更早的一拍：两个进程合计 1800MiB。若实现对窗口内**所有**样本求和（另一种错误口径），
	// 会得到虚高的 2800MiB——本行用来锁死「只取最新一拍求和」而不是「全窗口求和」。
	for _, pid := range []int32{201, 202} {
		require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
			NodeUUID: "node-uuid", InstanceUUID: inst.UUID, PID: pid,
			RSSBytes: 900 * 1024 * 1024, SampledAt: sampleAt.Add(-time.Minute),
		}).Error)
	}

	corr, err := NewCrashCorrelationService(db).CorrelateInstance(inst.ID, crashAt)
	require.NoError(t, err)
	require.Equal(t, int64(1000*1024*1024), corr.RSSAtCrash,
		"RSS 必须是「最新一拍整棵树」的 SUM：既不是单进程 MAX，也不是跨拍求和")
	require.True(t, corr.NearOOM, "求和逼近上限必须判 nearOOM（MAX 口径下会漏判）")
	require.Empty(t, corr.Note)
}

// TestCrashCorrelationBatch_MatchesSingleVersion R14：批量关联的每条结果必须与
// 单条版**逐字段一致**，且批量调用只发 O(1) 次查询。
//
// 为什么必须一致：列表端点改用批量版后，若两处口径漂移（例如批量版误用 MAX），
// 同一个实例在「列表」与「详情」里会给出互相矛盾的 OOM 结论——这正是 R6 那类
// 「同一张表两套口径」缺陷的成因类型。
func TestCrashCorrelationBatch_MatchesSingleVersion(t *testing.T) {
	db := newCrashTestDB(t)
	require.NoError(t, db.Create(&model.Node{Name: "n1", Host: "h", Secret: "s"}).Error)
	inst := seedCrashInstance(t, db, "smp")
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("mem_limit_mb", 1024).Error)

	now := time.Now().UTC()
	at1 := now.Add(-30 * time.Minute)
	at2 := now.Add(-90 * time.Minute)
	at3 := now.Add(-3 * time.Hour)

	// at1 窗口内：一拍两进程，合计 1000MiB（逼近上限）。
	for _, pid := range []int32{1, 2} {
		require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
			NodeUUID: "node-uuid", InstanceUUID: inst.UUID, PID: pid,
			RSSBytes: 500 * 1024 * 1024, SampledAt: at1.Add(-time.Minute),
		}).Error)
	}
	// at2 窗口内：一拍单进程 200MiB（未逼近）。
	require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
		NodeUUID: "node-uuid", InstanceUUID: inst.UUID, PID: 3,
		RSSBytes: 200 * 1024 * 1024, SampledAt: at2.Add(-time.Minute),
	}).Error)
	// at3 窗口内无样本 → 降级文案。

	svc := NewCrashCorrelationService(db)
	batch, err := svc.CorrelateInstanceBatch(inst.ID, []time.Time{at1, at2, at3})
	require.NoError(t, err)
	require.Len(t, batch, 3)

	for _, at := range []time.Time{at1, at2, at3} {
		single, serr := svc.CorrelateInstance(inst.ID, at)
		require.NoError(t, serr)
		got, ok := batch[at.UnixNano()]
		require.True(t, ok, "批量结果必须按 occurredAt 可检索")
		require.Equal(t, single.RSSAtCrash, got.RSSAtCrash, "RSS 口径必须与单条版一致（除 GCNote 外）")
		require.Equal(t, single.MemLimitMB, got.MemLimitMB)
		require.Equal(t, single.NearOOM, got.NearOOM)
		require.Equal(t, single.Note, got.Note)
		require.Equal(t, single.HeapUsedMax, got.HeapUsedMax)
	}

	require.True(t, batch[at1.UnixNano()].NearOOM)
	require.False(t, batch[at2.UnixNano()].NearOOM)
	require.Equal(t, "崩溃窗口内无进程资源样本", batch[at3.UnixNano()].Note)
}

// TestCrashCorrelationBatch_EmptyInput 空输入不查库、不报错（列表可能为空）。
func TestCrashCorrelationBatch_EmptyInput(t *testing.T) {
	db := newCrashTestDB(t)
	out, err := NewCrashCorrelationService(db).CorrelateInstanceBatch(1, nil)
	require.NoError(t, err)
	require.Empty(t, out)
}

// countingProcessMetricSource 记录调用次数的指标源。
type countingProcessMetricSource struct {
	latestCalls int
}

func (c *countingProcessMetricSource) LatestValue(model.MetricScope, string, string, string, time.Time) (*float64, error) {
	c.latestCalls++
	return nil, nil
}

// queryCountingLogger 统计 GORM 执行的 SQL 条数（R14 的量化依据）。
//
// 列表端点的放大系数只能靠「实际发了多少条查询」证明：批量版把实例行、节点行与
// 进程样本各自收敛为 1 次，而逐条版每一条崩溃都要重来一遍。
type queryCountingLogger struct {
	count int
}

func (l *queryCountingLogger) LogMode(logger.LogLevel) logger.Interface      { return l }
func (l *queryCountingLogger) Info(context.Context, string, ...interface{})  {}
func (l *queryCountingLogger) Warn(context.Context, string, ...interface{})  {}
func (l *queryCountingLogger) Error(context.Context, string, ...interface{}) {}
func (l *queryCountingLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	fc() // 触发 SQL 生成后计数
	l.count++
}

// withCountingLogger 用统计 logger 包装一个测试库（保持原连接）。
func withCountingLogger(t *testing.T, db *gorm.DB) (*gorm.DB, *queryCountingLogger) {
	t.Helper()
	counter := &queryCountingLogger{}
	return db.Session(&gorm.Session{Logger: counter}), counter
}

// TestCrashCorrelationBatch_ReducesQueryCount R14：批量关联的 DB 查询数必须是 O(1)
// （实例 1 + 进程样本 1 + 节点 1 + 指标 K），而逐条版是 O(K×3+)。
//
// 缺陷现场：崩溃快照列表在循环里逐条 `Correlate`，每条至少 2 次 DB（实例、RSS 聚合），
// 注入 metrics 后再加 1 次节点查 + LatestValue。K=5 条 → 10~20+ 次查询，列表是控制台
// 高频读接口，放大系数直接体现在响应时间上。
func TestCrashCorrelationBatch_ReducesQueryCount(t *testing.T) {
	db := newCrashTestDB(t)
	require.NoError(t, db.Create(&model.Node{Name: "n1", Host: "h", Secret: "s"}).Error)
	inst := seedCrashInstance(t, db, "smp")

	now := time.Now().UTC()
	// 5 条崩溃，每条窗口内各有两拍样本（覆盖「按拍聚合 + 逐条窗口筛选」两条路径）。
	ats := make([]time.Time, 0, 5)
	for i := 0; i < 5; i++ {
		at := now.Add(-time.Duration(i+1) * 30 * time.Minute)
		ats = append(ats, at)
		for _, pid := range []int32{1, 2} {
			require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
				NodeUUID: "node-uuid", InstanceUUID: inst.UUID, PID: pid,
				RSSBytes: 100 * 1024 * 1024, SampledAt: at.Add(-time.Minute),
			}).Error)
		}
	}

	// 批量版：3 次固定查询（实例 + 进程样本 + 节点）+ K 次指标查询。
	counted, counter := withCountingLogger(t, db)
	svc := NewCrashCorrelationService(counted)
	svc.SetMetrics(&countingProcessMetricSource{})
	_, err := svc.CorrelateInstanceBatch(inst.ID, ats)
	require.NoError(t, err)
	batchQueries := counter.count

	// 逐条版：每条 3 次固定查询（实例 + 节点 + 样本定位/聚合）+ K 次指标查询。
	counted2, counter2 := withCountingLogger(t, db)
	svc2 := NewCrashCorrelationService(counted2)
	svc2.SetMetrics(&countingProcessMetricSource{})
	for _, at := range ats {
		_, serr := svc2.CorrelateInstance(inst.ID, at)
		require.NoError(t, serr)
	}
	singleQueries := counter2.count

	require.Less(t, batchQueries, singleQueries,
		"批量版的查询数必须少于逐条累加（实际 batch=%d, single=%d）", batchQueries, singleQueries)
	// 批量版的固定查询不随 K 增长：样本定位 1 + 聚合 1 + 实例 1 + 节点 1 = 4，其余是逐窗口的指标查。
	require.LessOrEqual(t, batchQueries, 4+len(ats),
		"批量版每条崩溃只应额外增加 1 次指标查询（实际 %d 次）", batchQueries)
}

// TestQuotaSampleInstance_DistinguishesFailureFromUnavailable R12：采样必须区分
// 「失败」（DB/RPC 异常，需留痕）与「不可用」（节点离线/实例未运行，预期状态）。
//
// 缺陷现场：`InstanceResourceUsage`/`InstanceWorkDirBytes` 把四种失败全部吞成
// `(nil,false,nil)`，error 恒为 nil；节点离线期间配额巡检静默漏检且无任何告警，
// 运维无法回答「这台实例的配额到底有没有在管」。
func TestQuotaSampleInstance_DistinguishesFailureFromUnavailable(t *testing.T) {
	failure := errors.New("连接池无客户端")
	cases := []struct {
		name        string
		sample      *quotaSample
		unavailable bool
		err         error
		wantErr     bool
		wantOK      bool
		wantSampled bool
	}{
		{name: "查询失败必须带 error", err: failure, wantErr: true},
		{name: "不可用但无错误属预期状态", unavailable: true, wantErr: false, wantOK: false},
		{name: "正常采样", sample: &quotaSample{RSSBytes: 1024}, wantErr: false, wantOK: true, wantSampled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := quotaTestDB(t)
			inst := makeQuotaInstance(t, db, "a", 0, 100, 0, 0)
			metrics := &stubQuotaMetrics{
				sample:      quotaSample{},
				usage:       tc.sample,
				unavailable: tc.unavailable,
				usageErr:    tc.err,
			}
			e, _ := newEnforcer(t, db, metrics)
			sample, ok, err := e.sampleInstance(inst)
			if tc.wantErr {
				require.ErrorIs(t, err, failure, "查询失败必须把错误传出去，供调用方计数告警")
				return
			}
			require.NoError(t, err)
			if tc.wantSampled {
				require.True(t, ok)
				require.Equal(t, int64(1024), sample.RSSBytes)
				return
			}
			require.False(t, ok)
		})
	}
}

// TestQuotaEvaluate_LogsUnsampleableInstances 巡检必须留下「本轮有多少实例没采到」的痕迹。
//
// 只断言状态不产生副作用（不误判为未超限），并用 stub 的调用计数证明巡检确实尝试过采样。
func TestQuotaEvaluate_DoesNotTreatUnavailableAsUnderLimit(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "a", 0, 100, 0, 0) // MemLimitMB=100 → 会进入采样
	metrics := &stubQuotaMetrics{unavailable: true}
	e, _ := newEnforcer(t, db, metrics)

	// 连跑超过阈值次数：不可采样不得累积成「超限」而触发处置。
	for i := 0; i < 5; i++ {
		e.evaluate()
	}
	var fired int64
	require.NoError(t, db.Model(&model.AlertEvent{}).Count(&fired).Error)
	require.Zero(t, fired, "不可采样不得冒充超限（也不得冒充未超限地产生任何处置）")

	// 实例本身未被改动（status_reason 无配额停止痕迹）。
	var reloaded model.Instance
	require.NoError(t, db.First(&reloaded, inst.ID).Error)
	require.Empty(t, reloaded.StatusReason)
	require.Positive(t, metrics.calls, "巡检确实尝试过采样（而非直接跳过）")
}

// TestQuotaStatus_SurfacesDiskReadFailure R12 读侧：磁盘读取失败必须与「占用为 0」区分。
func TestQuotaStatus_SurfacesDiskReadFailure(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "a", 1, 100, 0, 0)
	metrics := &stubQuotaMetrics{usage: &quotaSample{RSSBytes: 10 << 20}, diskErr: errors.New("节点离线")}
	e, _ := newEnforcer(t, db, metrics)

	status, err := e.Status(inst.ID)
	require.NoError(t, err)
	require.Zero(t, status.DiskBytes)
	require.Contains(t, status.SampleNote, "磁盘占用读取失败",
		"读取失败必须显式呈现，不能与「占用为 0」混同")
}

// TestQuotaMetricSource_DerivesDeadlineFromInboundCtx m-1：按需磁盘 RPC 的 deadline
// 必须派生自调用方 ctx，使入站取消能传递下去。
func TestQuotaMetricSource_DerivesDeadlineFromInboundCtx(t *testing.T) {
	// quotaDiskContext 是纯函数式的派生逻辑：取消父 ctx 后派生 ctx 必须同样取消。
	parent, cancelParent := context.WithCancel(context.Background())
	child, cancelChild := quotaDiskContext(parent)
	defer cancelChild()

	require.NoError(t, child.Err())
	cancelParent()
	require.ErrorIs(t, child.Err(), context.Canceled,
		"父 ctx 取消必须传导到派生的 RPC deadline（改造前用 Background 派生，取消不传导）")

	// nil ctx 不得 panic（退回 Background）。
	nilChild, cancelNil := quotaDiskContext(nil)
	defer cancelNil()
	require.NoError(t, nilChild.Err())
}
