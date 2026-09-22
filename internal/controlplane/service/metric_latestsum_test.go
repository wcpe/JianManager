package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// seedPlayers 经真实写入路径（Ingest）追加一条实例级在线人数样本。
// 同实例同指标同 TS 反复调用即模拟心跳重放/重试：ensureSeries 命中同一序列、
// metric_sample_raws 纯追加，于是留下同 (series_id, ts) 的多行。
func seedPlayers(t *testing.T, svc *MetricService, inst string, ts time.Time, v *float64) {
	t.Helper()
	require.NoError(t, svc.Ingest([]Sample{{
		InstanceID: inst, Scope: model.MetricScopeInstance,
		MetricKey: model.MetricInstPlayersOnline, Unit: "count",
		TS: ts, Value: v,
	}}))
}

// TestMetric_LatestSum_ReplayedSampleNotDoubleCounted （B-1 复现）
//
// 缺陷：latestSum 的 `latest.mts = v.ts` 是**等值自联接**而非「每序列取一行」，
// 故同一序列同一 ts 有多少行，外层 SUM 就把它计多少次。而写入路径完全不设防：
// metric_sample_raws 无唯一约束（仅 id 自增主键）、idx_metric_raw_series_ts 是普通索引、
// Ingest 对样本纯追加且无 OnConflict —— 心跳重放/重试即产生同 (series_id, ts) 多行。
// 后果：/metrics/overview 的在线人数按重放次数翻倍。
//
// 期望语义：每条序列只计一行——该序列窗口内最新非空拍；同 ts 多行时取**最后写入**的那行
// （重放/重试后到达的最新观测，即 id 最大者）。
func TestMetric_LatestSum_ReplayedSampleNotDoubleCounted(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()

	// i-a 在 base 时刻被重放两次（值 5，随后修正为 7）；i-b 正常单行（值 3）。
	seedPlayers(t, svc, "i-a", base, fp(5))
	seedPlayers(t, svc, "i-a", base, fp(7))
	seedPlayers(t, svc, "i-b", base, fp(3))

	got, err := svc.latestSum(model.MetricScopeInstance, model.MetricInstPlayersOnline, base.Add(-time.Minute))
	require.NoError(t, err)
	require.Equal(t, 10.0, got,
		"每序列只计一行：i-a 取最后写入的 7 + i-b 的 3 = 10；修复前为 5+7+3=15")
}

// TestMetric_LatestSum_DuplicatesAcrossSeries 多序列各自重复：重复行不得跨序列叠加放大。
func TestMetric_LatestSum_DuplicatesAcrossSeries(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()

	// 三条序列各重放两次：真实值 4+6+2=12，修复前会得 8+12+4=24。
	for _, se := range []struct {
		inst string
		v    float64
	}{{"i-1", 4}, {"i-2", 6}, {"i-3", 2}} {
		seedPlayers(t, svc, se.inst, base, fp(se.v))
		seedPlayers(t, svc, se.inst, base, fp(se.v))
	}

	got, err := svc.latestSum(model.MetricScopeInstance, model.MetricInstPlayersOnline, base.Add(-time.Minute))
	require.NoError(t, err)
	require.Equal(t, 12.0, got, "三条序列各只计一行（4+6+2），不因重放翻倍")
}

// TestMetric_LatestSum_WindowAndNullSemanticsPreserved 修复不得改变既有语义：
// ① 只取窗口内（ts >= since）的样本；② NULL 缺测拍不取值，退回更早的非空拍；③ 跨序列求和。
func TestMetric_LatestSum_WindowAndNullSemanticsPreserved(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	since := base.Add(-time.Minute)

	// i-a：窗口外有更大值（必须忽略），窗口内最新非空拍为 5。
	seedPlayers(t, svc, "i-a", base.Add(-time.Hour), fp(999))
	seedPlayers(t, svc, "i-a", base, fp(5))
	// i-b：窗口内最新拍缺测（NULL），应退回上一非空拍 3，而不是 0 或 NULL。
	seedPlayers(t, svc, "i-b", base.Add(-30*time.Second), fp(3))
	seedPlayers(t, svc, "i-b", base, nil)

	got, err := svc.latestSum(model.MetricScopeInstance, model.MetricInstPlayersOnline, since)
	require.NoError(t, err)
	require.Equal(t, 8.0, got, "5（窗口内最新非空）+ 3（跨过 NULL 拍回退）= 8")
}

// TestMetric_LatestSum_OutOfOrderArrivalKeepsLatestTS 「最新拍」必须按 **ts** 判定，
// 不是按写入先后（id）。心跳重试可能让**更旧**的拍后到：此时 id 最大的那行 ts 更旧，
// 若按 id 锚定就会把「陈旧值」当当前值报出去（在线人数被少计）。
//
// 注意它同时还是「同 ts 多行」场景：i-a 在 base 有 T_latest 的两行（5 与 7），
// 更旧的拍（值 3，T_older）最后写入 → 期望仍取 T_latest 的同刻最后观测 7。
func TestMetric_LatestSum_OutOfOrderArrivalKeepsLatestTS(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	older := base.Add(-time.Minute)
	latest := base

	seedPlayers(t, svc, "i-a", latest, fp(5))
	seedPlayers(t, svc, "i-a", latest, fp(7)) // T_latest 的同刻重放
	seedPlayers(t, svc, "i-a", older, fp(3))  // 更旧的拍后到（id 最大）

	got, err := svc.latestSum(model.MetricScopeInstance, model.MetricInstPlayersOnline, base.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 7.0, got,
		"取 ts 最新那一行的值（7）；按写入先后(id)锚定会错取陈旧拍的 3")
}

// TestMetric_LatestSum_SingleRowPerSeriesUnchanged 反向确认：正常数据（每序列每拍一行）
// 的数值与修复前完全一致——覆盖「只取窗口内最新非空拍 + 跨序列求和」的既有语义。
func TestMetric_LatestSum_SingleRowPerSeriesUnchanged(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()

	seedPlayers(t, svc, "i-a", base.Add(-2*time.Minute), fp(1))
	seedPlayers(t, svc, "i-a", base.Add(-time.Minute), fp(2))
	seedPlayers(t, svc, "i-a", base, fp(5))
	seedPlayers(t, svc, "i-b", base, fp(3))

	got, err := svc.latestSum(model.MetricScopeInstance, model.MetricInstPlayersOnline, base.Add(-3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 8.0, got, "取各序列窗口内最新拍再求和：(5 + 3) = 8")
}
