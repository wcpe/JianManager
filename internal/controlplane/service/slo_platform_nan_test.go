package service

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// assertFiniteSLO 逐字段核查 SLO 出口**不含 NaN/Inf**。
//
// 必要性：encoding/json 无法编码 NaN/Inf，`c.JSON` 在此情况下写出 **200 + 空 body**——
// 调用方既拿不到数据也拿不到错误（比 500 更难排查）。故每个出口字段都要显式确认有限。
func assertFiniteSLO(t *testing.T, tag string, res SLOResult) {
	t.Helper()
	require.False(t, math.IsNaN(res.Availability) || math.IsInf(res.Availability, 0),
		"%s: availability 必须有限，实得 %v", tag, res.Availability)
	require.False(t, math.IsNaN(res.BudgetAllowedSec) || math.IsInf(res.BudgetAllowedSec, 0),
		"%s: budgetAllowedSec 必须有限，实得 %v", tag, res.BudgetAllowedSec)
	require.False(t, math.IsNaN(res.BudgetBurnedSec) || math.IsInf(res.BudgetBurnedSec, 0),
		"%s: budgetBurnedSec 必须有限，实得 %v", tag, res.BudgetBurnedSec)
	require.False(t, math.IsNaN(res.Target) || math.IsInf(res.Target, 0),
		"%s: target 必须有限，实得 %v", tag, res.Target)
	if res.MTTRSeconds != nil {
		require.False(t, math.IsNaN(*res.MTTRSeconds) || math.IsInf(*res.MTTRSeconds, 0),
			"%s: mttrSeconds 必须有限，实得 %v", tag, *res.MTTRSeconds)
	}
	if res.MTBFSeconds != nil {
		require.False(t, math.IsNaN(*res.MTBFSeconds) || math.IsInf(*res.MTBFSeconds, 0),
			"%s: mtbfSeconds 必须有限，实得 %v", tag, *res.MTBFSeconds)
	}
}

// TestComputeSLO_PlatformTinyWindowNotNaN （B-3 复现）
//
// 缺陷：平台维 `perInstance := int(span.Seconds() / float64(unitSec))` 在 span < unitSec
// （raw 档 unitSec=30，如窗口 5s）时取整为 0，而实例维/节点维有 `total <= 0 → 1` 兜底、
// 平台维**漏了**，于是 Availability = 0/0 = NaN、BudgetBurnedSec = span*(1-NaN) = NaN，
// 且 Applicable 仍为 true。
// 触发路径真实可达：GET /metrics/slo?scope=platform&from=T&to=T+5s 由 parseMetricRange
// 接受任意区间，只校验 to > from。
//
// 期望语义（与 m1「无可用证据」同构）：perInstance 取不到一个完整采样间隔 → 分母不可定义，
// 判为 Applicable=false、可用率与预算归 0，绝不放 NaN 出闸。
func TestComputeSLO_PlatformTinyWindowNotNaN(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	rankSeedInstance(t, svc, "uuid-tiny", "tiny")
	// 窗口内确有可用拍，确保走的是「有序列」分支（即缺兜底的那条）。
	seedUptimeTicks(t, svc, "uuid-tiny", base, 1)
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopePlatform, From: base, To: base.Add(5 * time.Second),
	})
	require.NoError(t, err)
	assertFiniteSLO(t, "span=5s", res)
	require.False(t, res.Applicable, "不足一个采样间隔 → 无可用证据，不适用")
	require.Zero(t, res.TotalSamples)
	require.Zero(t, res.Availability)
	require.Zero(t, res.BudgetBurnedSec)
}

// TestComputeSLO_PlatformTinyWindowRedisplaysValidValue 窗口恰好跨过一个采样间隔时
// perInstance 取到 1，恢复正常统计（确认兜底没有把合法窗口一起判成不适用）。
func TestComputeSLO_PlatformTinyWindowRedisplaysValidValue(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	rankSeedInstance(t, svc, "uuid-edge", "edge")
	seedUptimeTicks(t, svc, "uuid-edge", base, 1)
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopePlatform, From: base, To: base.Add(30 * time.Second),
	})
	require.NoError(t, err)
	assertFiniteSLO(t, "span=30s", res)
	require.True(t, res.Applicable)
	require.Equal(t, 1, res.UpSamples)
	require.Equal(t, 1, res.TotalSamples)
	require.InDelta(t, 1.0, res.Availability, 1e-9)
}

// TestComputeSLO_NonFiniteTargetNormalized B-3 加固：非有限 target 不得污染误差预算。
//
// `strconv.ParseFloat("NaN", 64)` 会成功返回 NaN，而 `NaN <= 0` 恒为 false，故裸
// `if target <= 0` 拦不住 NaN；target=NaN → `span*(1-NaN)` = NaN → 同样以
// 「200 + 空 body」静默失败。入口层已有 `v <= 0 || v > 1` 挡着，此处确认出口侧也兜住。
func TestComputeSLO_NonFiniteTargetNormalized(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	rankSeedInstance(t, svc, "uuid-nan", "nan")
	seedUptimeTicks(t, svc, "uuid-nan", base, 120)
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))
	from, to := base, base.Add(time.Hour)

	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, -1} {
		for _, scope := range []struct {
			name string
			q    SLOQuery
		}{
			{"platform", SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to, Target: bad}},
			{"instance", SLOQuery{Scope: model.MetricScopeInstance, InstanceID: "uuid-nan", From: from, To: to, Target: bad}},
			{"node", SLOQuery{Scope: model.MetricScopeNode, NodeUUID: "node-x", From: from, To: to, Target: bad}},
		} {
			res, err := svc.ComputeSLO(scope.q)
			require.NoError(t, err)
			tag := scope.name + " target=" + formatFloat(bad)
			assertFiniteSLO(t, tag, res)
			require.InDelta(t, sloDefaultTarget, res.Target, 1e-12, "%s: target 回落到默认", tag)
		}
	}
}

// formatFloat 打印 NaN/Inf 的可读形式（Go 的 %v 对 NaN 输出 "NaN"，此处仅求稳定可读）。
func formatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// 逐个出口字段确认无 NaN/Inf（B-3 要求的「同类根因完整加固」，非仅平台维）。
func TestComputeSLO_AllScopesFiniteOnDegenerateWindows(t *testing.T) {
	spans := []time.Duration{time.Second, 5 * time.Second, 29 * time.Second, 30 * time.Second}
	maxTarget := 1.0 // target=1 → budgetAllowed = span*(1-1) = 0，不产生 0/0
	// svc 只建一次：newMetricSvc 的 DSN 就是 t.Name()，同名单测内重复建库会命中同一实例行
	// （memory&cache=shared）从而撞 uuid 唯一约束。span 只影响查询窗口，不影响种子。
	svc := newMetricSvc(t)
	base := metricBase()
	rankSeedInstance(t, svc, "uuid-all", "all")
	seedUptimeTicks(t, svc, "uuid-all", base, 1)
	// 种子拍覆盖最长窗口（30s），保证每个 span 下窗口内都有可用拍。
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.AlertRule{}, &model.AlertEvent{}))
	for _, span := range spans {

		from, to := base, base.Add(span)
		// 平台维（有序列，走缺兜底分支）。
		res, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to})
		require.NoError(t, err)
		assertFiniteSLO(t, "platform span="+span.String(), res)

		// 平台维（无可用实例 → 空集分支）。
		resEmpty, err := svc.ComputeSLO(SLOQuery{
			Scope: model.MetricScopePlatform, From: from, To: to, InstanceUUIDs: []string{},
		})
		require.NoError(t, err)
		assertFiniteSLO(t, "platform(empty) span="+span.String(), resEmpty)

		// 实例维。
		resInst, err := svc.ComputeSLO(SLOQuery{
			Scope: model.MetricScopeInstance, InstanceID: "uuid-all", From: from, To: to,
		})
		require.NoError(t, err)
		assertFiniteSLO(t, "instance span="+span.String(), resInst)

		// 节点维（无序列 → 分母兜底 1）。
		resNode, err := svc.ComputeSLO(SLOQuery{
			Scope: model.MetricScopeNode, NodeUUID: "node-none", From: from, To: to,
		})
		require.NoError(t, err)
		assertFiniteSLO(t, "node span="+span.String(), resNode)

		// target=1（禁止 target<=0 走默认值的干扰）下同样必须有限。
		resT1, err := svc.ComputeSLO(SLOQuery{
			Scope: model.MetricScopePlatform, From: from, To: to, Target: maxTarget,
		})
		require.NoError(t, err)
		assertFiniteSLO(t, "platform target=1 span="+span.String(), resT1)
	}
}

// TestComputeSLO_TinyWindowSpanningTickBoundary 窗口恰好跨过一个采样间隔时，
// perInstance 取整为 1 而 up 可能为 0（窗口内那拍落在采样网格之前）——available/预算
// 仍须是合法数值（0 可用率是**真实结论**，不是 NaN）。
func TestComputeSLO_TinyWindowSpanningTickBoundary(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	rankSeedInstance(t, svc, "uuid-bound", "bound")
	// 只在 base 有一拍；查询窗口取 [base+1s, base+31s]：跨度 30s（perInstance=1）、
	// 但该拍落在窗口外 → up=0，可用率 0（不适用性不该被触发）。
	seedUptimeTicks(t, svc, "uuid-bound", base, 1)
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopePlatform, From: base.Add(time.Second), To: base.Add(31 * time.Second),
	})
	require.NoError(t, err)
	assertFiniteSLO(t, "boundary", res)
	require.True(t, res.Applicable, "窗口跨度足够 → 适用（0 可用率是真实结论）")
	require.Equal(t, 1, res.TotalSamples)
	require.Zero(t, res.UpSamples)
	require.InDelta(t, 0.0, res.Availability, 1e-12)
}
