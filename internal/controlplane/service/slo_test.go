package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// seedUptimeTicks 造 inst_uptime 可用拍（每 30s 一拍，非 NULL 即「本拍可用」）。
func seedUptimeTicks(t *testing.T, svc *MetricService, instUUID string, from time.Time, ticks int) {
	t.Helper()
	require.NoError(t, svc.db.AutoMigrate(&model.Instance{}))
	samples := make([]Sample, 0, ticks)
	for i := 0; i < ticks; i++ {
		samples = append(samples, Sample{
			NodeUUID: "node-1", InstanceID: instUUID, Scope: model.MetricScopeInstance,
			MetricKey: model.MetricInstUptime, Unit: "seconds",
			TS: from.Add(time.Duration(i) * 30 * time.Second), Value: fp(100),
		})
	}
	require.NoError(t, svc.Ingest(samples))
}

func seedAlertEvents(t *testing.T, svc *MetricService, targetID uint, from time.Time, resolved bool, dur time.Duration) {
	t.Helper()
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))
	rule := &model.AlertRule{Name: "crash", Level: model.AlertLevelCritical,
		TriggerType: model.AlertTriggerInstanceCrash, TargetType: "instance", Enabled: true}
	require.NoError(t, svc.db.Create(rule).Error)
	ev := model.AlertEvent{
		RuleID: rule.ID, TargetID: targetID, Level: rule.Level,
		TriggerType: model.AlertTriggerInstanceCrash,
		DedupKey:    "k", FiredAt: from, Resolved: resolved,
	}
	if resolved {
		at := from.Add(dur)
		ev.ResolvedAt = &at
	}
	require.NoError(t, svc.db.Create(&ev).Error)
}

// TestComputeSLO_AvailabilityIncidentsMTTR 窗口内可用率/故障次数/MTTR/误差预算（FR-463 验收 1/2）。
func TestComputeSLO_AvailabilityIncidentsMTTR(t *testing.T) {
	svc := newMetricSvc(t)
	inst := rankSeedInstance(t, svc, "uuid-slo", "slo-inst")
	base := metricBase()
	from, to := base, base.Add(time.Hour)

	seedUptimeTicks(t, svc, "uuid-slo", from, 60) // 60/120 拍 → 50%
	seedAlertEvents(t, svc, inst.ID, base.Add(10*time.Minute), true, 5*time.Minute)
	seedAlertEvents(t, svc, inst.ID, base.Add(20*time.Minute), false, 0)

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-slo", From: from, To: to,
	})
	require.NoError(t, err)
	require.Equal(t, 60, res.UpSamples)
	require.Equal(t, 120, res.TotalSamples)
	require.InDelta(t, 0.5, res.Availability, 1e-9)
	require.Equal(t, 2, res.Incidents)
	require.Equal(t, 1, res.ActiveIncidents)
	require.NotNil(t, res.MTTRSeconds)
	require.InDelta(t, 300, *res.MTTRSeconds, 1e-6, "MTTR = Σ(恢复-触发)/已恢复数，未恢复不计入均值")
	require.NotNil(t, res.MTBFSeconds)
	require.InDelta(t, 1800, *res.MTBFSeconds, 1e-6, "MTBF = 窗口/故障次数")
	require.InDelta(t, 3600*0.005, res.BudgetAllowedSec, 1e-6, "默认目标 99.5%")
	require.InDelta(t, 1800, res.BudgetBurnedSec, 1e-6)
	require.InDelta(t, 0.995, res.Target, 1e-9)
	require.False(t, res.ApproximatedBuckets)
}

// TestComputeSLO_NoIncidents MTBF/MTTR 返回 null 而非 Infinity（验收 2）。
func TestComputeSLO_NoIncidents(t *testing.T) {
	svc := newMetricSvc(t)
	rankSeedInstance(t, svc, "uuid-quiet", "quiet")
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	seedUptimeTicks(t, svc, "uuid-quiet", from, 120)
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-quiet", From: from, To: to,
	})
	require.NoError(t, err)
	require.InDelta(t, 1.0, res.Availability, 1e-9)
	require.Equal(t, 0, res.Incidents)
	require.Nil(t, res.MTTRSeconds)
	require.Nil(t, res.MTBFSeconds, "无故障 → MTBF 为 null，不是 Infinity")
	require.Zero(t, res.BudgetBurnedSec)
	require.InDelta(t, 18, res.BudgetAllowedSec, 1e-6)
}

// TestComputeSLO_PlatformAggregates 平台维度 = 各实例可用拍求和 / 总拍求和（不做简单平均）。
func TestComputeSLO_PlatformAggregates(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	// M6 后平台分母只统计**现存实例**的行，故两条序列都必须有对应 instances 行。
	rankSeedInstance(t, svc, "uuid-p1", "p1")
	rankSeedInstance(t, svc, "uuid-p2", "p2")
	seedUptimeTicks(t, svc, "uuid-p1", from, 120) // 满勤
	seedUptimeTicks(t, svc, "uuid-p2", from, 60)  // 半勤
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	res, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to})
	require.NoError(t, err)
	require.Equal(t, 180, res.UpSamples)
	require.Equal(t, 240, res.TotalSamples, "分母 = 2 个实例 × 120 拍")
	require.InDelta(t, 0.75, res.Availability, 1e-9)

	// 收敛到单个实例：分母随之变为 1×120。
	resScoped, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopePlatform, From: from, To: to, InstanceUUIDs: []string{"uuid-p2"},
	})
	require.NoError(t, err)
	require.Equal(t, 60, resScoped.UpSamples)
	require.Equal(t, 120, resScoped.TotalSamples)
	require.InDelta(t, 0.5, resScoped.Availability, 1e-9)

	// 空可见集 → 全部为 0，不报错。
	resEmpty, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopePlatform, From: from, To: to, InstanceUUIDs: []string{},
	})
	require.NoError(t, err)
	require.Zero(t, resEmpty.UpSamples)
	require.Zero(t, resEmpty.Availability)
}

func TestComputeSLO_RollupApproximation(t *testing.T) {
	svc := newMetricSvc(t)
	rankSeedInstance(t, svc, "uuid-long", "long")
	base := metricBase()
	from, to := base, base.Add(7*24*time.Hour)

	// 先落一条 raw 以创建序列。
	seedUptimeTicks(t, svc, "uuid-long", from, 1)
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))
	// 造 21 个 5m 桶：20 个 count=10、1 个 count=25（应被截到 10）。
	rows := make([]model.MetricRollup5m, 0, 21)
	var series model.MetricSeries
	require.NoError(t, svc.db.Where("instance_id = ? AND metric_key = ?", "uuid-long", model.MetricInstUptime).
		First(&series).Error)
	for i := 0; i < 21; i++ {
		count := 10
		if i == 20 {
			count = 25
		}
		rows = append(rows, model.MetricRollup5m{
			SeriesID: series.ID,
			BucketTS: from.Add(time.Duration(i) * 5 * time.Minute),
			Avg:      100, Min: 100, Max: 100, Last: 100, Count: count,
		})
	}
	require.NoError(t, svc.db.Create(&rows).Error)

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-long", From: from, To: to,
	})
	require.NoError(t, err)
	require.True(t, res.ApproximatedBuckets)
	require.Equal(t, 210, res.UpSamples, "21 桶 × min(count,10)")
	require.Equal(t, 7*24*3600/300, res.TotalSamples)
}

// TestComputeSLO_PlatformIgnoresOrphanSeries （M6 + N-1）实例行已删除、但 metric_series 仍残留
// （序列从不删除）的孤儿序列不得进入平台分母，否则会凭空拉低平台可用率。
//
// N-1 修复：**必须按生产删除语义（软删）验证**。`InstanceService.Delete` 走
// `tx.Delete(&model.Instance{}, id)` = GORM 软删，实例行仍留在表里（`deleted_at` 非空）；
// 原用例用 `Unscoped().Delete` 硬删绕开了真实语义，使得「EXISTS 未带 deleted_at IS NULL」
// 这一缺陷在测试中不可见（假绿）。这里覆盖软删与硬删两条路径。
func TestComputeSLO_PlatformIgnoresOrphanSeries(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	// 真实实例：满勤。
	rankSeedInstance(t, svc, "uuid-live", "live")
	seedUptimeTicks(t, svc, "uuid-live", from, 120)
	// 软删实例：先建实例行并落序列，随后按**生产删除语义**软删（序列残留）。
	soft := rankSeedInstance(t, svc, "uuid-soft", "soft")
	seedUptimeTicks(t, svc, "uuid-soft", from, 120)
	require.NoError(t, svc.db.Delete(&model.Instance{}, soft.ID).Error, "走 GORM 软删（与 InstanceService.Delete 同语义）")
	// 硬删实例（如 NodeService.Delete 级联）：行整体消失。
	hard := rankSeedInstance(t, svc, "uuid-hard", "hard")
	seedUptimeTicks(t, svc, "uuid-hard", from, 120)
	require.NoError(t, svc.db.Unscoped().Delete(&model.Instance{}, hard.ID).Error)
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	// 前提校验：软删后实例行仍在表内（正是 N-1 缺陷的成因），但带软删过滤的常规查询看不到。
	var softRows int64
	require.NoError(t, svc.db.Unscoped().Model(&model.Instance{}).
		Where("id = ? AND deleted_at IS NOT NULL", soft.ID).Count(&softRows).Error)
	require.Equal(t, int64(1), softRows, "软删实例行仍在 instances 表内")
	var visible int64
	require.NoError(t, svc.db.Model(&model.Instance{}).Count(&visible).Error)
	require.Equal(t, int64(1), visible, "常规查询只看到 1 个现存实例（uuid-live）")

	res, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to})
	require.NoError(t, err)
	require.Equal(t, 120, res.TotalSamples, "分母只含现存实例（软删/硬删实例的残留序列都不计）")
	require.Equal(t, 120, res.UpSamples)
	require.InDelta(t, 1.0, res.Availability, 1e-9, "孤儿序列不得把平台可用率拉低")
	require.True(t, res.Applicable)

	// 收敛到软删实例：它已不是现存实例，不得进入分母（否则分母>0、分子为 0 → 可用率凭空为 0）。
	resScoped, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopePlatform, From: from, To: to, InstanceUUIDs: []string{"uuid-soft"},
	})
	require.NoError(t, err)
	require.False(t, resScoped.Applicable, "软删实例无可用证据 → 不适用，而不是「可用率 0」")
	require.Zero(t, resScoped.TotalSamples)
}

// TestComputeSLO_UpSamplesNeverExceedsTotal （M7）同实例存在两条 inst_uptime 序列时，
// 分子求和可能超过分母；断言 upSamples <= totalSamples 且可用率不超过 1。
func TestComputeSLO_UpSamplesNeverExceedsTotal(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	rankSeedInstance(t, svc, "uuid-dup", "dup")
	seedUptimeTicks(t, svc, "uuid-dup", from, 120)
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	// 人为再造一条同实例同指标序列（不同 node_uuid 使唯一索引不冲突，
	// 但 SLO 的实例维查询只按 instance_id 过滤 → 分子会双计）。
	dup := model.MetricSeries{
		NodeUUID: "node-other", InstanceID: "uuid-dup", Scope: model.MetricScopeInstance,
		MetricKey: model.MetricInstUptime, Unit: "seconds", World: "",
	}
	require.NoError(t, svc.db.Create(&dup).Error)
	rows := make([]model.MetricSampleRaw, 0, 120)
	for i := 0; i < 120; i++ {
		rows = append(rows, model.MetricSampleRaw{
			SeriesID: dup.ID, TS: from.Add(time.Duration(i) * 30 * time.Second), Value: fp(100),
		})
	}
	require.NoError(t, svc.db.CreateInBatches(&rows, 60).Error)

	res, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-dup", From: from, To: to,
	})
	require.NoError(t, err)
	require.LessOrEqual(t, res.UpSamples, res.TotalSamples, "upSamples 不得超过 totalSamples")
	require.LessOrEqual(t, res.Availability, 1.0, "可用率不得超过 1")

	resPlat, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to})
	require.NoError(t, err)
	require.LessOrEqual(t, resPlat.UpSamples, resPlat.TotalSamples, "平台维同样受钳制")
	require.LessOrEqual(t, resPlat.Availability, 1.0)
}

// TestComputeSLO_PlatformEmptyNotApplicable （m1）无任何探针实例时不得报「误差预算 100% 已消耗」。
func TestComputeSLO_PlatformEmptyNotApplicable(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	// instances 表须存在：平台分母的「现存实例」判定依赖它（生产恒有该表）。
	require.NoError(t, svc.db.AutoMigrate(&model.Instance{}, &model.AlertRule{}, &model.AlertEvent{}))

	res, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to})
	require.NoError(t, err)
	require.False(t, res.Applicable, "无可用证据 → 不适用")
	require.Zero(t, res.BudgetBurnedSec, "不适用时不得报已消耗满额")
	require.Zero(t, res.TotalSamples)
	// 不适用时不给出「允许时长」——否则前端会把「允许 X 秒 / 已消耗 0」读成「预算充裕」，
	// 掩盖「根本没有可用证据」这一事实（BudgetAllowedSec 仅在 applicable 时有意义）。
	require.Zero(t, res.BudgetAllowedSec)

	// 有实例时 applicable=true（不误伤正常路径）。
	rankSeedInstance(t, svc, "uuid-app", "app")
	seedUptimeTicks(t, svc, "uuid-app", from, 120)
	res2, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to})
	require.NoError(t, err)
	require.True(t, res2.Applicable)
	require.Greater(t, res2.BudgetAllowedSec, 0.0, "适用时如实给出允许不可用时长")
}

// TestComputeSLO_NodeAndInstanceEventsNotMixed （M3）nodes.id 与 instances.id 独立自增会撞号：
// 构造 id 相同的节点事件与实例事件，断言实例维/节点维不会互相混算。
func TestComputeSLO_NodeAndInstanceEventsNotMixed(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	inst := rankSeedInstance(t, svc, "uuid-mix", "mix")
	seedUptimeTicks(t, svc, "uuid-mix", from, 120)
	// 节点：自增 id 与实例 id 撞号（同一数字）。
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.AlertRule{}, &model.AlertEvent{}))
	node := model.Node{UUID: "node-mix", Name: "n-mix", Status: model.NodeStatusOnline}
	require.NoError(t, svc.db.Create(&node).Error)
	require.Equal(t, inst.ID, node.ID, "场景前提：两表 id 撞号")

	// 节点离线事件（target_type=node），target_id = node.ID。
	nodeRule := &model.AlertRule{Name: "node-offline", Level: model.AlertLevelCritical,
		TriggerType: model.AlertTriggerNodeOffline, TargetType: "node", Enabled: true}
	require.NoError(t, svc.db.Create(nodeRule).Error)
	nodeEv := model.AlertEvent{RuleID: nodeRule.ID, TargetID: node.ID, Level: nodeRule.Level,
		TriggerType: model.AlertTriggerNodeOffline, DedupKey: "n", FiredAt: from.Add(5 * time.Minute), Resolved: true}
	at := from.Add(10 * time.Minute)
	nodeEv.ResolvedAt = &at
	require.NoError(t, svc.db.Create(&nodeEv).Error)

	// 实例崩溃事件（target_type=instance），target_id 与上面同号。
	instRule := &model.AlertRule{Name: "inst-crash", Level: model.AlertLevelCritical,
		TriggerType: model.AlertTriggerInstanceCrash, TargetType: "instance", Enabled: true}
	require.NoError(t, svc.db.Create(instRule).Error)
	instEv := model.AlertEvent{RuleID: instRule.ID, TargetID: inst.ID, Level: instRule.Level,
		TriggerType: model.AlertTriggerInstanceCrash, DedupKey: "i", FiredAt: from.Add(20 * time.Minute), Resolved: false}
	require.NoError(t, svc.db.Create(&instEv).Error)

	// 实例维：只应看到实例的 1 次故障。
	resInst, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-mix", From: from, To: to,
	})
	require.NoError(t, err)
	require.Equal(t, 1, resInst.Incidents, "实例维不得把同号节点离线事件算进来")
	require.Equal(t, 1, resInst.ActiveIncidents)

	// 节点维：只应看到节点的 1 次故障。
	resNode, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeNode, NodeUUID: "node-mix", From: from, To: to,
	})
	require.NoError(t, err)
	require.Equal(t, 1, resNode.Incidents, "节点维不得把同号实例崩溃事件算进来")
	require.NotNil(t, resNode.MTTRSeconds, "节点事件已恢复 → 有 MTTR")
	require.InDelta(t, 300, *resNode.MTTRSeconds, 1e-6)

	// 平台维：只应收敛到 instance 目标的事件。
	resPlat, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to})
	require.NoError(t, err)
	require.Equal(t, 1, resPlat.Incidents, "平台维只收 instance 目标事件")
	require.Equal(t, 1, resPlat.ActiveIncidents)
}

// TestComputeSLO_LegacyRuleWithoutTargetType （M3 边界）规则缺 target_type 的存量行：
// 维度可判定的触发类型（node_offline / instance_crash）按家族归入对应维度；
// 维度**不可判定**的 metric 触发一律不计（宁可少计也不跨维度错计，与 health_wall 同口径）。
func TestComputeSLO_LegacyRuleWithoutTargetType(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	inst := rankSeedInstance(t, svc, "uuid-legacy", "legacy")
	seedUptimeTicks(t, svc, "uuid-legacy", from, 120)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.AlertRule{}, &model.AlertEvent{}))
	node := model.Node{UUID: "node-legacy", Name: "n-legacy", Status: model.NodeStatusOnline}
	require.NoError(t, svc.db.Create(&node).Error)

	// 旧规则（target_type 空）+ metric 触发：数字 target_id 无法判别维度 → 两个维度都不计。
	legacyMetric := &model.AlertRule{Name: "legacy-metric", Level: model.AlertLevelWarn,
		TriggerType: model.AlertTriggerMetric, Enabled: true}
	require.NoError(t, svc.db.Create(legacyMetric).Error)
	require.NoError(t, svc.db.Create(&model.AlertEvent{
		RuleID: legacyMetric.ID, TargetID: inst.ID, Level: legacyMetric.Level,
		TriggerType: model.AlertTriggerMetric, DedupKey: "lm", FiredAt: from.Add(5 * time.Minute), Resolved: false,
	}).Error)

	// 旧规则 + instance_crash（家族可判定）→ 实例维应计入。
	legacyCrash := &model.AlertRule{Name: "legacy-crash", Level: model.AlertLevelCritical,
		TriggerType: model.AlertTriggerInstanceCrash, Enabled: true}
	require.NoError(t, svc.db.Create(legacyCrash).Error)
	require.NoError(t, svc.db.Create(&model.AlertEvent{
		RuleID: legacyCrash.ID, TargetID: inst.ID, Level: legacyCrash.Level,
		TriggerType: model.AlertTriggerInstanceCrash, DedupKey: "lc", FiredAt: from.Add(10 * time.Minute), Resolved: false,
	}).Error)

	resInst, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-legacy", From: from, To: to,
	})
	require.NoError(t, err)
	require.Equal(t, 1, resInst.Incidents, "只计入家族可判定的 instance_crash，metric 歧义行不计")

	// 旧规则 + node_offline（家族可判定）→ 节点维应计入，且不受同号实例行影响。
	legacyOffline := &model.AlertRule{Name: "legacy-offline", Level: model.AlertLevelCritical,
		TriggerType: model.AlertTriggerNodeOffline, Enabled: true}
	require.NoError(t, svc.db.Create(legacyOffline).Error)
	require.NoError(t, svc.db.Create(&model.AlertEvent{
		RuleID: legacyOffline.ID, TargetID: node.ID, Level: legacyOffline.Level,
		TriggerType: model.AlertTriggerNodeOffline, DedupKey: "lo", FiredAt: from.Add(15 * time.Minute), Resolved: false,
	}).Error)
	resNode, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeNode, NodeUUID: "node-legacy", From: from, To: to,
	})
	require.NoError(t, err)
	require.Equal(t, 1, resNode.Incidents, "旧规则 node_offline 按家族归入节点维")

	// 平台维（只收 instance 目标）：同样只认家族可判定的 instance_crash。
	resPlat, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to})
	require.NoError(t, err)
	require.Equal(t, 1, resPlat.Incidents, "平台维与实例维的旧规则回退语义一致")
}

// TestComputeSLO_SoftDeletedRuleKeepsHistoryEvents （N-7）规则被删除（软删）后，其**历史事件
// 仍须计入 SLO**：已发生过的故障不因后来删规则而消失，否则 Incidents/MTBF 会在删规则后凭空下降。
// 连带锁定维度回退：规则行不可见时按触发类型家族判定（与「旧规则无 target_type」同一分支）。
func TestComputeSLO_SoftDeletedRuleKeepsHistoryEvents(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	inst := rankSeedInstance(t, svc, "uuid-delrule", "delrule")
	seedUptimeTicks(t, svc, "uuid-delrule", from, 120)
	require.NoError(t, svc.db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	// 实例崩溃规则 + 两条历史事件（一条已恢复 5min，一条进行中）。
	rule := &model.AlertRule{Name: "crash-del", Level: model.AlertLevelCritical,
		TriggerType: model.AlertTriggerInstanceCrash, TargetType: "instance", Enabled: true}
	require.NoError(t, svc.db.Create(rule).Error)

	// 删除前的基线。
	seed := func(resolved bool, firedAt time.Time, dur time.Duration, dedup string) {
		ev := model.AlertEvent{RuleID: rule.ID, TargetID: inst.ID, Level: rule.Level,
			TriggerType: model.AlertTriggerInstanceCrash, DedupKey: dedup, FiredAt: firedAt, Resolved: resolved}
		if resolved {
			at := firedAt.Add(dur)
			ev.ResolvedAt = &at
		}
		require.NoError(t, svc.db.Create(&ev).Error)
	}
	seed(true, base.Add(10*time.Minute), 5*time.Minute, "d1")

	before, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-delrule", From: from, To: to,
	})
	require.NoError(t, err)
	require.Equal(t, 1, before.Incidents)
	require.NotNil(t, before.MTTRSeconds)

	// 按生产语义删除规则（`AlertService.DeleteRule` = `db.Delete` = 软删）。
	require.NoError(t, svc.db.Delete(&model.AlertRule{}, rule.ID).Error)
	var deleted int64
	require.NoError(t, svc.db.Unscoped().Model(&model.AlertRule{}).
		Where("id = ? AND deleted_at IS NOT NULL", rule.ID).Count(&deleted).Error)
	require.Equal(t, int64(1), deleted, "规则按软删下线（行仍在表内）")

	// 再加一条进行中的事件，确认删除后新事件也不被「规则不可见」连坐。
	seed(false, base.Add(20*time.Minute), 0, "d2")

	after, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-delrule", From: from, To: to,
	})
	require.NoError(t, err)
	require.Equal(t, 2, after.Incidents,
		"规则软删后其历史事件仍计入（原 INNER JOIN 在 GORM 下虽未注入软删谓词，但语义不得依赖该实现细节）")
	require.Equal(t, 1, after.ActiveIncidents, "删除后新增的进行中事件同样计入")
	require.NotNil(t, after.MTTRSeconds)
	require.InDelta(t, *before.MTTRSeconds, *after.MTTRSeconds, 1e-9,
		"删规则不得让 MTTR 漂移（历史事件仍在集合内）")

	// 维度回退：规则行不可见时按触发类型家族（instance_crash → 实例）归入实例维，
	// 且不得因同号节点事件误入。构造一条同号 node_offline 事件做对照。
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}))
	nodeRule := &model.AlertRule{Name: "offline-del", Level: model.AlertLevelCritical,
		TriggerType: model.AlertTriggerNodeOffline, TargetType: "node", Enabled: true}
	require.NoError(t, svc.db.Create(nodeRule).Error)
	require.NoError(t, svc.db.Create(&model.AlertEvent{
		RuleID: nodeRule.ID, TargetID: inst.ID, Level: nodeRule.Level,
		TriggerType: model.AlertTriggerNodeOffline, DedupKey: "dn", FiredAt: base.Add(30 * time.Minute), Resolved: false,
	}).Error)
	require.NoError(t, svc.db.Delete(&model.AlertRule{}, nodeRule.ID).Error)

	resInst, err := svc.ComputeSLO(SLOQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-delrule", From: from, To: to,
	})
	require.NoError(t, err)
	require.Equal(t, 2, resInst.Incidents, "软删的 node_offline 规则事件不得计入实例维")

	// 平台维（只收 instance 目标）：与实例维一致，历史事件仍计入。
	resPlat, err := svc.ComputeSLO(SLOQuery{Scope: model.MetricScopePlatform, From: from, To: to})
	require.NoError(t, err)
	require.Equal(t, 2, resPlat.Incidents, "平台维同样不因规则软删而丢弃历史故障")
}
