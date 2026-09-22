package service

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestComputeSLO_InstanceNoProbeNotApplicable （M-5）实例维在「探针从未可用」时
// 必须与平台维同口径：`applicable=false`，而不是「可用率 0% + 预算 100% 已消耗」。
//
// 缺陷：m1 修复只落在平台维（`len(series)==0 → Applicable=false`）。
// 实例/节点维只要 `span > 0` 就无条件 `out.Applicable = true`，即使该实例从未有过
// `inst_uptime` 序列 → 分子 0、分母被 `total <= 0 → 1` 兜底与「窗口 / 采样间隔」撑起 → 0%。
//
// 为什么这是 bug 而非刻意设计（三处证据）：
//  1. 同一 `applicable` 字段必须有同一含义——前端 `SLOSection.tsx` 只按
//     `!data.applicable` 渲染 `slo.notApplicable`（文案「不适用（窗口内无可用证据）」，
//     与 scope 无关），它无法区分「平台维无证据」与「实例维无证据」；
//  2. `MonitoringPage.tsx:434` 对 node/instance 维度渲染**同一张卡片**，故实例页也会
//     命中该分支；
//  3. spec §5 明言生产上「未装探针的实例」是常见形态（沙箱 13 个实例 0 条 inst_uptime
//     序列）——若实例维按「全时宕机」报，最需要看 SLO 的那批实例给出的恰是误导数字。
//
// 关键区分（本用例同时锁定两侧，避免「一刀切成不适用」把真实结论也吃掉）：
//   - **无序列**（从未上报）→ applicable=false；
//   - **有序列但窗口内零可用拍** → applicable=true + 可用率 0（真实的全时不可用结论）。
func TestComputeSLO_InstanceNoProbeNotApplicable(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	// 实例行存在（不是软删/不存在导致的其他分支），但**从不写入 inst_uptime**。
	rankSeedInstance(t, svc, "uuid-noprobe", "noprobe")
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-noprobe", From: from, To: to,
	})
	require.NoError(t, err)
	require.False(t, res.Applicable,
		"实例从未上报 inst_uptime → 无可用证据，应与平台维同口径判为「不适用」")
	require.Zero(t, res.TotalSamples, "分母不可用 → totalSamples=0（前端据 totalSamples<=0 显示 --）")
	require.Zero(t, res.Availability)
	require.Zero(t, res.BudgetAllowedSec, "不适用时不得给出「允许 X 秒」，否则前端读成「预算充裕」")
	require.Zero(t, res.BudgetBurnedSec,
		"不得报「预算 100% 已消耗」——那是把「没数据」误报成「全时宕机」")
	assertFiniteSLO(t, "instance/无探针", res)
}

// TestComputeSLO_NodeNoCPUSeriesNotApplicable （M-5）节点维同口径：
// 节点从未上报 node_cpu_pct（无节点状态历史表，可用性只能走该指标）→ 不适用。
func TestComputeSLO_NodeNoCPUSeriesNotApplicable(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.AlertRule{}, &model.AlertEvent{}))
	node := model.Node{UUID: "node-nocpu", Name: "nocpu", Status: model.NodeStatusOnline}
	require.NoError(t, svc.db.Create(&node).Error)

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeNode, NodeUUID: "node-nocpu", From: from, To: to,
	})
	require.NoError(t, err)
	require.False(t, res.Applicable, "节点从未上报 node_cpu_pct → 无可用证据")
	require.Zero(t, res.TotalSamples)
	require.Zero(t, res.Availability)
	require.Zero(t, res.BudgetBurnedSec)
	assertFiniteSLO(t, "node/无序列", res)
}

// TestComputeSLO_ZeroUpTicksWithSeriesStaysApplicable （M-5 反例，防止修过头）
// **有序列**但窗口内零可用拍时仍是「适用 + 可用率 0」——那是真实的全时不可用结论，
// 不是「不适用」。若把判据误写成「可用拍为 0 即不适用」，实例真宕机时反而会显示
// 「不适用」，把最需要告警的情况藏起来。
func TestComputeSLO_ZeroUpTicksWithSeriesStaysApplicable(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	rankSeedInstance(t, svc, "uuid-down", "down")
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	// 序列存在（写入一拍建立序列），但查询窗口**完全不含**该拍 → 窗口内零可用拍。
	seedUptimeTicks(t, svc, "uuid-down", base, 1)
	from, to := base.Add(10*time.Minute), base.Add(70*time.Minute)

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-down", From: from, To: to,
	})
	require.NoError(t, err)
	require.True(t, res.Applicable, "有序列但窗口内零可用拍 = 真实的全时不可用结论，仍适用")
	require.Equal(t, 120, res.TotalSamples, "窗口 1h / 30s = 120 拍")
	require.Zero(t, res.UpSamples)
	require.InDelta(t, 0.0, res.Availability, 1e-12)
	require.InDelta(t, 3600, res.BudgetBurnedSec, 1e-6, "全时不可用 → 预算全部消耗（真实结论）")
	require.InDelta(t, 18, res.BudgetAllowedSec, 1e-6)
}

// TestComputeSLO_NoSeriesStillReportsIncidents （M-5 边界）「不适用」只否定可用率与误差预算，
// **不否定故障事实**：`Incidents` 来自 `AlertEvent` 快照，与「有没有可用性序列」无关。
// 某实例探针从未可用、但崩溃事件照常落库时，实例页仍应显示它崩溃了几次。
func TestComputeSLO_NoSeriesStillReportsIncidents(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	inst := rankSeedInstance(t, svc, "uuid-crashonly", "crashonly")
	// 刻意不写 inst_uptime：只造崩溃事件。
	seedAlertEvents(t, svc, inst.ID, base.Add(10*time.Minute), true, 5*time.Minute)
	seedAlertEvents(t, svc, inst.ID, base.Add(20*time.Minute), false, 0)

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-crashonly", From: from, To: to,
	})
	require.NoError(t, err)
	require.False(t, res.Applicable, "无可用证据 → 不适用")
	require.Equal(t, 2, res.Incidents, "不适用只否定可用率/预算，故障次数仍须如实给出")
	require.Equal(t, 1, res.ActiveIncidents)
	require.NotNil(t, res.MTTRSeconds, "已恢复故障的 MTTR 仍须给出")
	require.InDelta(t, 300, *res.MTTRSeconds, 1e-6)
	require.NotNil(t, res.MTBFSeconds, "有故障 → MTBF = 窗口/次数")
	require.InDelta(t, 1800, *res.MTBFSeconds, 1e-6)
}

// TestComputeSLO_AllScopesApplicableSemanticsAligned （M-5 一致性）三档 scope 对
// 「无序列」给出同一结论：`applicable=false` + 全部数值字段为 0（且有限）。
// 这条是「同一字段同一含义」的直白表达，防止将来只改其中一档。
func TestComputeSLO_AllScopesApplicableSemanticsAligned(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	rankSeedInstance(t, svc, "uuid-align", "align")
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.AlertRule{}, &model.AlertEvent{}))
	require.NoError(t, svc.db.Create(&model.Node{
		UUID: "node-align", Name: "align", Status: model.NodeStatusOnline,
	}).Error)

	cases := []struct {
		tag string
		q   SLOQuery
	}{
		{"platform(空可见集)", SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to, InstanceUUIDs: []string{}}},
		{"instance(无序列)", SLOQuery{Scope: model.MetricScopeInstance, InstanceID: "uuid-align", From: from, To: to}},
		{"node(无序列)", SLOQuery{Scope: model.MetricScopeNode, NodeUUID: "node-align", From: from, To: to}},
	}
	for _, c := range cases {
		t.Run(c.tag, func(t *testing.T) {
			res, err := svc.ComputeSLO(c.q)
			require.NoError(t, err)
			require.Falsef(t, res.Applicable, "%s：无可用证据应判为不适用", c.tag)
			require.Equalf(t, 0, res.TotalSamples, "%s：分母不可用", c.tag)
			require.Equalf(t, 0, res.UpSamples, "%s", c.tag)
			require.Zerof(t, res.Availability, "%s", c.tag)
			require.Zerof(t, res.BudgetAllowedSec, "%s", c.tag)
			require.Zerof(t, res.BudgetBurnedSec, "%s", c.tag)
			require.Falsef(t, math.IsNaN(res.Availability), "%s：不得出 NaN", c.tag)
			assertFiniteSLO(t, c.tag, res)
		})
	}
}

// TestComputeSLO_InstanceWithProbeStillApplicable （M-5 放行侧）装了探针、有可用拍的实例
// 必须仍走正常统计路径——本用例锁住「修复没有把正常路径一起判成不适用」。
func TestComputeSLO_InstanceWithProbeStillApplicable(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	rankSeedInstance(t, svc, "uuid-probe", "probe")
	seedUptimeTicks(t, svc, "uuid-probe", from, 60) // 60/120 → 50%
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-probe", From: from, To: to,
	})
	require.NoError(t, err)
	require.True(t, res.Applicable)
	require.Equal(t, 60, res.UpSamples)
	require.Equal(t, 120, res.TotalSamples)
	require.InDelta(t, 0.5, res.Availability, 1e-9)
	require.InDelta(t, 18, res.BudgetAllowedSec, 1e-6)
	require.InDelta(t, 1800, res.BudgetBurnedSec, 1e-6)
}
