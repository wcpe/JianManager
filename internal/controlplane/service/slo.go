package service

import (
	"errors"
	"math"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// sloDefaultTarget 默认可用性目标（FR-463）。可由查询覆盖。
const sloDefaultTarget = 0.995

// sloNormalizeTarget 归一化可用性目标：非有限值（NaN/±Inf）或 <=0 一律回落到默认目标。
//
// B-3 加固：`strconv.ParseFloat("NaN", 64)` 会**成功**返回 NaN，而 `NaN <= 0` 恒为 false，
// 故裸 `if target <= 0` 拦不住 NaN。target 一旦是 NaN，`1-target` 即 NaN，误差预算
// （BudgetAllowedSec/BudgetBurnedSec）与可用率比较全部污染成 NaN，最终同样以
// 「200 + 空 body」的形式静默失败。当前唯一调用方（SLO 处理器）已用 `v <= 0 || v > 1`
// 挡在前面，但入口层不该是唯一防线——此处做出口侧兜底，任何调用方都不可能把 NaN 带进结果。
func sloNormalizeTarget(target float64) float64 {
	if math.IsNaN(target) || math.IsInf(target, 0) || target <= 0 {
		return sloDefaultTarget
	}
	return target
}

// sloEventTriggers 计入故障的告警触发类型（FR-463）。与告警体系一致：崩溃 / 节点离线 / 指标阈值。
var sloEventTriggers = []string{
	model.AlertTriggerInstanceCrash,
	model.AlertTriggerNodeOffline,
	model.AlertTriggerMetric,
}

// SLOQuery 可用性/SLO 查询参数（FR-463）。Scope=node 用 NodeUUID 定位，Scope=instance 用 InstanceID（UUID），
// Scope=platform 汇总全部**有探针序列**的实例（各实例可用拍求和 / 总拍求和，不做简单平均）。
type SLOQuery struct {
	Scope      model.MetricScope
	NodeUUID   string
	InstanceID string
	From, To   time.Time
	Target     float64 // 可用性目标，<=0 用默认 99.5%
	// InstanceUUIDs 为 platform 维度的可见实例收敛（非空即仅统计这些实例）；nil=不收敛。
	InstanceUUIDs []string
}

// SLOResult 窗口内可用性与 SLO 聚合结果（FR-463）。
type SLOResult struct {
	Scope           model.MetricScope `json:"scope"`
	Availability    float64           `json:"availability"` // 0~1
	TotalSamples    int               `json:"totalSamples"` // 分母：窗口时长 / 采样间隔推算
	UpSamples       int               `json:"upSamples"`    // 分子：可用拍数
	Incidents       int               `json:"incidents"`    // 窗口内故障次数
	ActiveIncidents int               `json:"activeIncidents"`
	// MTTRSeconds / MTBFSeconds 为 null 表示无已恢复故障 / 无故障（**不返回 Infinity**）。
	MTTRSeconds         *float64 `json:"mttrSeconds"`
	MTBFSeconds         *float64 `json:"mtbfSeconds"`
	BudgetAllowedSec    float64  `json:"budgetAllowedSec"`
	BudgetBurnedSec     float64  `json:"budgetBurnedSec"`
	Target              float64  `json:"target"`
	ApproximatedBuckets bool     `json:"approximatedBuckets"` // true=窗口超 raw 留存，按 rollup 桶近似
	// Applicable=false 表示本窗口无可用证据（分母为 0）：可用率与误差预算都**不适用**，
	// 此时 BudgetBurnedSec 恒为 0，前端应显示「不适用」而不是「预算 100% 已消耗」（自审 m1）。
	Applicable bool `json:"applicable"`
}

// ComputeSLO 计算窗口内可用率、故障次数、MTTR/MTBF 与误差预算（FR-463）。
//
// 可用性口径：以「有可用证据的心跳/探针拍」为在线，缺拍计不可用（保守默认）。实例用非 NULL 的
// inst_uptime 拍；节点用非 NULL 的 node_cpu_pct 拍（无节点状态历史表，见 spec 偏差说明）。
// 平台维度 = 各实例可用拍求和 / 总拍求和。超过 raw 留存的窗口只能按 rollup 桶近似（桶内 count 上限即该桶拍数）。
//
// M-5：「无可用证据」在三档 scope 上同义，故 `Applicable` 必须有同一含义。
// 判据是**序列是否存在**（= 该目标历史上是否上报过可用性指标），而不是可用拍数是否为 0：
//   - 无序列（实例从未装探针 / 节点从未上报 node_cpu_pct）→ `Applicable=false`，
//     前端渲染「不适用」；
//   - 有序列但窗口内零可用拍 → `Applicable=true` + 可用率 0，这是**真实的全时不可用结论**
//     （与 `TestComputeSLO_TinyWindowSpanningTickBoundary` 同口径），不是「不适用」。
//
// 为什么必须区分：`total` 由「窗口时长 / 采样间隔」推算，被 `total <= 0 → 1` 兜底撑起，
// 故无序列时**分母不是 0**、`applicable` 原实现恒为 true，于是「未装探针」被渲染成
// 「可用率 0% / 预算 100% 已消耗」——与 spec §1 验收 1 的「无证据不伪造」直接冲突，
// 而 spec §5 明言生产上「未装探针的实例」是常见形态（沙箱里 13 个实例 0 条 inst_uptime 序列）。
func (s *MetricService) ComputeSLO(q SLOQuery) (SLOResult, error) {
	q.Target = sloNormalizeTarget(q.Target)
	if q.Scope == model.MetricScopePlatform {
		return s.computePlatformSLO(q)
	}
	out := SLOResult{Scope: q.Scope, Target: q.Target}
	span := q.To.Sub(q.From)
	if span <= 0 {
		return out, errors.New("非法窗口")
	}
	from, to := q.From.UTC(), q.To.UTC()

	metricKey := model.MetricInstUptime
	if q.Scope == model.MetricScopeNode {
		metricKey = model.MetricNodeCPUPct
	}
	up, unitSec, hasSeries, approx, err := s.sloUpTicks(q.Scope, q.NodeUUID, q.InstanceID, metricKey, from, to, span)
	if err != nil {
		return out, err
	}
	total := int(span.Seconds() / float64(unitSec))
	if total <= 0 {
		total = 1
	}
	// M-5：无可用证据 → 与平台维 m1 同构判为「不适用」，可用率与预算都归 0，
	// 不给出「0% 可用 + 预算 100% 已消耗」这类把「没数据」误报成「全时宕机」的数字。
	// 注意 total 仍留 0（而不是留兜底后的 1）：响应语义应是「分母不可用」，
	// 前端据 totalSamples <= 0 显示 `--`，与 applicable=false 的「不适用」互相印证。
	if !hasSeries {
		out.ApproximatedBuckets = approx
		out.Incidents, out.ActiveIncidents, out.MTTRSeconds, out.MTBFSeconds, err = s.sloIncidentSummary(q, from, to, span)
		return out, err
	}
	// M7 修复：先用**钳制后**的 up 计算可用率，再把钳制值写回 out.UpSamples。
	// 原实现先落未钳制值、只钳局部变量，会出现 upSamples > totalSamples 的自相矛盾响应。
	if up > total {
		up = total
	}
	out.UpSamples = up
	out.TotalSamples = total
	out.ApproximatedBuckets = approx
	out.Availability = float64(up) / float64(total)
	out.Applicable = true

	// 误差预算：允许不可用时长 = 窗口 × (1 - 目标)；已消耗 = 窗口 × (1 - 实测可用率)。
	budgetAllowed := span.Seconds() * (1 - out.Target)
	burned := span.Seconds() * (1 - out.Availability)
	if burned < 0 {
		burned = 0
	}
	out.BudgetAllowedSec = budgetAllowed
	out.BudgetBurnedSec = burned

	out.Incidents, out.ActiveIncidents, out.MTTRSeconds, out.MTBFSeconds, err = s.sloIncidentSummary(q, from, to, span)
	if err != nil {
		return out, err
	}
	return out, nil
}

// sloIncidentSummary 汇总目标窗口内的故障次数/进行中数/MTTR/MTBF（MTBF = 窗口 / 故障数，无故障则 nil）。
//
// 抽出来是为了让「无可用证据」分支也能给出故障统计：`Incidents` 来自 `AlertEvent`，
// 与「有没有可用性序列」无关——某实例探针从未可用、但崩溃事件照常落库时，
// 实例页仍应显示它崩溃了几次（`applicable=false` 只否定可用率与误差预算，不否定故障事实）。
// M-5 修复前该分支不存在（恒走统计路径），故这次抽出不改变原来的输出，只是把两条出口合一。
func (s *MetricService) sloIncidentSummary(q SLOQuery, from, to time.Time, span time.Duration) (int, int, *float64, *float64, error) {
	targetID, err := s.sloTargetID(q)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	// M3 修复：故障事件按目标类型收敛——实例维只收 instance 目标、节点维只收 node 目标，
	// 避免 nodes.id 与 instances.id 的独立自增序列撞号时互相计入。
	// 事件无 scope/维度字段可用时（存量行），退化为按 trigger_type 家族保守判定。
	incidents, active, mttr, err := s.sloIncidents(targetID, q.Scope, from, to)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	var mtbf *float64
	if incidents > 0 {
		v := span.Seconds() / float64(incidents)
		mtbf = &v
	}
	return incidents, active, mttr, mtbf, nil
}

// sloSampleIntervalSec 可用性判定的采样间隔（秒）：一条 30s 样本视为该目标「本拍可用」。
// 与 model.MetricSampleRaw 的「30s 粒度」同一口径，是 raw 档 unitSec 与 rollup 档
// 「一个桶代表多少拍」的共同基准（m1：此前 30/300/3600 与 10/120 都是散落字面量，
// 采样间隔一变 min(count, cap) 就会静默失真，没有任何断言拦得住）。
const sloSampleIntervalSec = 30

// sloTicksPerBucket5m / sloTicksPerBucket1h 由桶常量**推导**「一个 rollup 桶至多代表多少拍」。
//
// 为什么用推导而非字面量：rollup 桶只存聚合值（count 为桶内样本数），无法还原本拍是否可用，
// 故可用拍按 `min(count, ticksPerBucket)` 近似——这个上限的正确性完全取决于
// 「桶宽度 = 采样间隔 × 拍数」。写成 10/120 常量时，若将来采样间隔或桶宽度改动，
// 该近似会静默偏移（偏大则可用率虚高），且不会有任何编译期或运行期信号。
// 现在改为从 metricBucket5m/metricBucket1h 与 sloSampleIntervalSec 计算，
// 并有 slo_rollup_ticks_test.go 断言结果（5m→10、1h→120）。
var (
	sloTicksPerBucket5m = int(metricBucket5m / (sloSampleIntervalSec * time.Second))
	sloTicksPerBucket1h = int(metricBucket1h / (sloSampleIntervalSec * time.Second))
)

// sloUpTicks 返回窗口内的可用拍数、采样间隔（秒）与「是否存在可用性序列」。
// 超 raw 留存时按 rollup 桶近似（count 上限为拍数）。
//
// M-5：第三个返回值由 `approx bool` 改为「序列是否存在 + 近似档位」——
// 「无序列」是判定 `applicable=false` 的依据，不能与「有序列但零可用拍」混同。
// hasSeries 与 approx 的语义正交，故用两个独立返回值表达。
func (s *MetricService) sloUpTicks(scope model.MetricScope, nodeUUID, instanceID, metricKey string, from, to time.Time, span time.Duration) (up, unitSec int, hasSeries, approx bool, err error) {
	seriesQ := s.db.Model(&model.MetricSeries{}).Where("scope = ? AND metric_key = ? AND world = ''", scope, metricKey)
	if scope == model.MetricScopeNode {
		seriesQ = seriesQ.Where("node_uuid = ?", nodeUUID)
	} else {
		seriesQ = seriesQ.Where("instance_id = ?", instanceID)
	}
	var series []model.MetricSeries
	if err := seriesQ.Find(&series).Error; err != nil {
		return 0, sloSampleIntervalSec, false, false, err
	}
	up, unitSec, approx, err = s.sloUpTicksForSeries(series, from, to, span)
	return up, unitSec, len(series) > 0, approx, err
}

// sloUpTicksForSeries 按序列集合累计可用拍（raw 逐样本计数；5m/1h 按桶 count 上限近似）。
// 返回 (可用拍, 采样间隔秒, 是否 rollup 近似档)。
// 调用方据「序列集合是否为空」判定 applicable（M-5），故本函数不重复表达该语义。
//
// M-1 修复：改为**两条常数 SQL**（一条取序列、一条聚合），不再逐序列查。
// 原实现对**每条序列**各发一条 COUNT（raw）/ 一条 Pluck（rollup），而平台维的序列数≈实例数，
// 于是 SELECT 条数随实例数线性增长：实测 5 实例=7 条、50 实例=52 条。
// `/metrics/slo?scope=platform` 是前端 60s 轮询，这条 N+1 会直接放大成持续往返成本。
// 同批次的 latestSum/rankingSkippedNoData 都已改单条聚合，此处补齐同一约定。
// 语义（每序列每桶取 min(count, ticksPerBucket) 后求和）逐字节等价——见下方聚合实现注释。
func (s *MetricService) sloUpTicksForSeries(series []model.MetricSeries, from, to time.Time, span time.Duration) (int, int, bool, error) {
	ids := make([]uint, 0, len(series))
	for _, se := range series {
		ids = append(ids, se.ID)
	}
	// 档位：raw ≤ 48h；5m ≤ 30d；否则 1h。unitSec 即该档一「拍」的时间跨度。
	switch {
	case span <= metricRawRetention:
		up, err := sloRawUpTicks(s.db, ids, from, to)
		return up, sloSampleIntervalSec, false, err
	case span <= metric5mRetention:
		up, err := sloRollupUp(s.db, "MetricRollup5m", ids, from, to, sloTicksPerBucket5m)
		return up, int(metricBucket5m.Seconds()), true, err
	default:
		up, err := sloRollupUp(s.db, "MetricRollup1h", ids, from, to, sloTicksPerBucket1h)
		return up, int(metricBucket1h.Seconds()), true, err
	}
}

// computePlatformSLO 平台维度可用性：各实例可用拍求和 / 总拍求和（不做简单平均，避免实例数变化扭曲）。
// 故障次数/MTTR/MTBF 由各实例事件汇总（MTTR = Σ恢复时长 / Σ已恢复数，MTBF = 窗口 / Σ故障数）。
func (s *MetricService) computePlatformSLO(q SLOQuery) (SLOResult, error) {
	out := SLOResult{Scope: model.MetricScopePlatform, Target: q.Target}
	if out.Target <= 0 {
		out.Target = sloDefaultTarget // 已由 ComputeSLO 经 sloNormalizeTarget 归一，此处为直调兜底
	}
	span := q.To.Sub(q.From)
	if span <= 0 {
		return out, errors.New("非法窗口")
	}
	from, to := q.From.UTC(), q.To.UTC()

	// 平台可用性只用「有 inst_uptime 序列**且实例仍存在**」的实例（无探针实例无在线证据，不计分母）。
	// M6 修复：JOIN instances——`metric_series` 从不删除，实例删除后序列会残留成孤儿，
	// 若不 JOIN 就会把已不存在的实例计入分母，凭空拉低平台可用率（孤儿序列永远没有新样本）。
	//
	// 用 EXISTS 子查询而非 JOIN：语义等价（每个实例至多一条序列命中），
	// 但不需要 instances 表与 metric_series 同查询存在——单测/最小化 schema 下也不会报
	// 「no such table: instances」，不把迁移状态变成可用性前提。
	//
	// N-1 修复：`deleted_at IS NULL` 必须显式写出。实例删除（`InstanceService.Delete`）走的是
	// `tx.Delete(&model.Instance{}, id)` = **GORM 软删**，行仍留在 instances 表里；
	// 只判「行是否存在」会把软删实例的残留序列继续算进平台分母，而它已不再产出新样本
	// → 分子不涨、分母恒涨，凭空拉低平台可用率（M6 要修的缺陷并未真正闭合）。
	// EXISTS 子查询是**手写 SQL 片段**，GORM 不会自动注入软删谓词，故必须显式加上。
	// 硬删（如 NodeService.Delete 的 `Unscoped().Delete` 级联删实例）则因行不存在而天然被排除。
	seriesQ := s.db.Model(&model.MetricSeries{}).
		Where("scope = ? AND metric_key = ? AND world = ''",
			model.MetricScopeInstance, model.MetricInstUptime).
		Where("EXISTS (SELECT 1 FROM instances WHERE instances.uuid = metric_series.instance_id AND instances.deleted_at IS NULL)")
	if q.InstanceUUIDs != nil {
		if len(q.InstanceUUIDs) == 0 {
			seriesQ = seriesQ.Where("1 = 0")
		} else {
			seriesQ = seriesQ.Where("instance_id IN ?", q.InstanceUUIDs)
		}
	}
	var series []model.MetricSeries
	if err := seriesQ.Find(&series).Error; err != nil {
		return out, err
	}
	up, unitSec, approx, err := s.sloUpTicksForSeries(series, from, to, span)
	if err != nil {
		return out, err
	}
	// 平台分母 = 参与实例数 × 单实例期望拍数（分子分母同口径放大，故可用率是「拍加权」而非实例平均）。
	perInstance := int(span.Seconds() / float64(unitSec))
	// B-3 修复：`span < unitSec`（raw 档 30s，如窗口 5s）时 perInstance 取整为 0。
	// 实例维/节点维有 `total <= 0 → 1` 兜底，平台维原先**漏了**，于是走 else 分支得
	// Availability = up/0 = NaN、BudgetBurnedSec = span*(1-NaN) = NaN，且 Applicable 仍为 true；
	// encoding/json 编不出 NaN → `c.JSON` 写出 200 + 空 body，调用方既拿不到数据也拿不到错误。
	// 触发路径真实可达：`GET /metrics/slo?scope=platform&from=T&to=T+5s`。
	//
	// 语义选择：与 m1「无可用证据」同构判为 applicable=false（而不是像实例维那样兜底成 1）。
	// 窗口不足一个采样间隔时，分母「单实例期望拍数」根本不可定义——按 0 拍算会把「窗口太短」
	// 误报成「全时不可用」（可用率 0），凭空空耗误差预算。显式「不适用」比伪造一个数值诚实，
	// 且与前端据 applicable=false 显示「不适用」的既有约定一致。
	if len(series) == 0 || perInstance <= 0 {
		// m1 修复：空平台（无任何探针实例）分母为 0，可用率与误差预算都**不适用**。
		// BudgetAllowedSec 也留 0：若给出「允许 X 秒 / 已消耗 0」，前端会读成「预算充裕」，
		// 反而掩盖「根本没有可用证据」这一事实。前端据 applicable=false 显示「不适用」。
		out.Applicable = false
		out.ApproximatedBuckets = approx
	} else {
		out.Applicable = true
		out.TotalSamples = perInstance * len(series)
		// M7 修复：钳制后再写入 out.UpSamples 并据此算可用率。原实现先落未钳制值、
		// 只把局部变量钳到 total，会出现 upSamples > totalSamples 的自相矛盾响应。
		if up > out.TotalSamples {
			up = out.TotalSamples
		}
		out.UpSamples = up
		out.Availability = float64(up) / float64(out.TotalSamples)
		// 误差预算消耗按「窗口 × (1 - 可用率)」计：可用率已是 0~1 比例。
		burned := span.Seconds() * (1 - out.Availability)
		if burned < 0 {
			burned = 0
		}
		out.BudgetBurnedSec = burned
		out.BudgetAllowedSec = span.Seconds() * (1 - out.Target)
	}

	// M3 修复：平台维故障事件**只收 instance 目标**——平台是实例集合的汇总，节点离线事件
	// 不属于该集合；且 nodes.id/instances.id 独立自增会撞号，不加 target_type 会把节点故障算进平台。
	incidents, active, sumResolved, resolvedCount, err := s.sloPlatformIncidents(q.InstanceUUIDs, from, to)
	if err != nil {
		return out, err
	}
	out.Incidents = incidents
	out.ActiveIncidents = active
	if resolvedCount > 0 {
		v := sumResolved / float64(resolvedCount)
		out.MTTRSeconds = &v
	}
	if incidents > 0 {
		mtbf := span.Seconds() / float64(incidents)
		out.MTBFSeconds = &mtbf
	}
	return out, nil
}

// sloPlatformIncidents 汇总平台维度（可选收敛到 q.InstanceUUIDs）窗口内的故障/进行中/恢复时长。
//
// M3 修复：加 target_type=instance 收敛。`AlertEvent.TargetID` 对节点与实例共用同一个数字列，
// 而 `nodes.id` 与 `instances.id` 是**两张表各自的自增**（生产 e2e 库就出现 nodes.id=1 与
// instances.id ∈ [1,14] 并存），不加目标类型会把节点离线事件误算成实例故障。
func (s *MetricService) sloPlatformIncidents(instanceUUIDs []string, from, to time.Time) (int, int, float64, int, error) {
	if instanceUUIDs != nil && len(instanceUUIDs) == 0 {
		return 0, 0, 0, 0, nil
	}
	// M3 修复：平台故障事件**只收 instance 目标**——平台是实例集合的汇总，节点离线事件
	// 不属于该集合；且 nodes.id/instances.id 独立自增会撞号，不加 target_type 会把节点故障算进平台。
	// 复用 sloIncidentScopeFilter 以获得与实例维一致的旧规则回退语义。
	query := sloIncidentScopeFilter(sloEventQuery(s.db, from, to), model.MetricScopeInstance)
	if instanceUUIDs != nil {
		ids, err := s.ResolveInstanceIDs(instanceUUIDs)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		targets := make([]uint, 0, len(ids))
		for _, id := range ids {
			targets = append(targets, id)
		}
		if len(targets) == 0 {
			return 0, 0, 0, 0, nil
		}
		query = query.Where("e.target_id IN ?", targets)
	}
	var events []model.AlertEvent
	if err := query.Find(&events).Error; err != nil {
		return 0, 0, 0, 0, err
	}
	incidents, active, resolvedCount := len(events), 0, 0
	var sumResolved float64
	for i := range events {
		e := &events[i]
		if !e.Resolved {
			active++
			continue
		}
		if e.ResolvedAt != nil {
			sumResolved += e.ResolvedAt.Sub(e.FiredAt).Seconds()
			resolvedCount++
		}
	}
	return incidents, active, sumResolved, resolvedCount, nil
}

// sloRawUpTicks 单条 SQL 累计窗口内所有目标序列的可用拍数（非 NULL 样本即「本拍可用」）。
//
// M-1 修复：原实现逐序列 `Count(&n)` 再累加，SQL 条数 = 序列数。这里改为一条条件聚合，
// SQL 条数恒为 1（与序列数无关），语义不变：`COUNT(*)` 只数非空样本，逐序列计数再求和
// 与「全体非空样本计数」在数值上完全等价（同一行只会属于一条序列）。
//
// `series_id IN ?` 为空切片时 GORM 生成 `IN (NULL)`（恒假）→ 0，与「无序列」一致，
// 不额外分支（避免调用方漏判空集）。
// 计数列名用 `COUNT(v.value)`：raw 档 value 可空，缺测（NULL）不得计入可用拍。
func sloRawUpTicks(db *gorm.DB, seriesIDs []uint, from, to time.Time) (int, error) {
	var up int64
	if err := db.Table(db.NamingStrategy.TableName("MetricSampleRaw")).
		Where("series_id IN ? AND value IS NOT NULL AND ts >= ? AND ts <= ?", seriesIDs, from, to).
		Count(&up).Error; err != nil {
		return 0, err
	}
	return int(up), nil
}

// sloRollupUp 单条 SQL 按 rollup 桶累计可用拍：每桶可用拍 = min(count, ticksPerBucket)。
//
// M-1 修复：原实现逐序列 `Pluck("count")` 后在 Go 侧累加，SQL 条数 = 序列数；
// 现改为一条 `SUM(MIN(count, ?))`。
//
// 等价性：原先的 `for _, c := range counts { if c > cap { c = cap }; total += c }`
// 与 SQL 的 `SUM(MIN(count, cap))` 是同一运算——SQLite 的标量 `min(a,b)` 返回较小者。
// 内层再包 `COALESCE(count, 0)`：SQLite 的双参 `min` 遇 NULL 返回 NULL（该行被 SUM 跳过），
// 而「count 为 NULL」只可能是异常写入，其语义应是「该桶无可用证据」= 贡献 0；
// 用 COALESCE 显式落成 0，避免「整行被静默跳过」与「贡献 0」在数值上虽然相同、
// 但在「NULL 会传染整个表达式」这一层留下意外（一旦将来外层从 SUM 改成别的聚合，
// NULL 传染会让结果整体变 NULL）。
//
// 空集时 SUM 返回 NULL，`COALESCE(SUM(...), 0)` 收敛成 0。
//
// 参数从 `[]model.MetricSeries` 改为 `[]uint`：本函数只需要序列 ID，
// 让调用方按需取 ID 可避免「为了拿 ID 而加载整行序列」。
func sloRollupUp(db *gorm.DB, modelName string, seriesIDs []uint, from, to time.Time, ticksPerBucket int) (int, error) {
	table := db.NamingStrategy.TableName(modelName)
	var up *int64
	if err := db.Table(table).
		Select("COALESCE(SUM(MIN(COALESCE(count, 0), ?)), 0)", ticksPerBucket).
		Where("series_id IN ? AND bucket_ts >= ? AND bucket_ts <= ?", seriesIDs, from, to).
		Scan(&up).Error; err != nil {
		return 0, err
	}
	if up == nil {
		return 0, nil
	}
	return int(*up), nil
}

// sloTargetID 把 UUID 目标解析为告警事件使用的数值目标 ID。
func (s *MetricService) sloTargetID(q SLOQuery) (uint, error) {
	if q.Scope == model.MetricScopeNode {
		if q.NodeUUID == "" {
			return 0, nil
		}
		var node model.Node
		err := s.db.Select("id").Where("uuid = ?", q.NodeUUID).First(&node).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return node.ID, err
	}
	if q.InstanceID == "" {
		return 0, nil
	}
	id, _, err := s.ResolveInstanceID(q.InstanceID)
	return id, err
}

// sloEventQuery 构造故障事件查询：窗口 + 触发类型过滤，并 JOIN alert_rules 取目标类型维度
// （M3：`alert_events` 只有数字 target_id，目标类型只存在于规则上）。
//
// 维度判定不只看 r.target_type：存量行可能是 FR-462 之前的旧规则（target_type 缺失/空），
// 此时退回按触发类型家族判定（node_offline 恒为节点；崩溃等实例家族恒为实例）；
// metric 触发在 node/instance **两个维度都存在**，无冗余列时无法安全判别，故只在
// target_type 明确时才计入对应维度（宁可少计，也不跨维度错计）。
//
// N-7：**LEFT JOIN 是刻意选择，不是笔误**。`AlertRule` 软删（`DeleteRule` 走 `db.Delete`），
// 规则被删后其**历史事件仍应计入 SLO**（已发生过的故障不因后来删规则而消失，
// 否则 Incidents/MTBF 会在删规则后凭空下降、MTTR 也会漂移）。GORM 的软删谓词只对
// `Model(&X{})` 查询注入（`callbacks.BuildQuerySQL` 用 `Statement.Schema.QueryClauses`），
// 对 `Joins("...")` 的裸 SQL 串不注入，故此处实际等价于「含软删规则」。
// 为让该语义**显式且不依赖 GORM 这一实现细节**，这里用 LEFT JOIN 并配 COALESCE 回退判定；
// 软删/不存在规则的事件按触发类型家族回退归入维度（与「旧规则无 target_type」同一回退分支）。
// 语义已写入 docs/specs/capacity-and-slo/spec.md §5。
func sloEventQuery(db *gorm.DB, from, to time.Time) *gorm.DB {
	return db.Table("alert_events AS e").
		Select("e.*").
		Joins("LEFT JOIN alert_rules AS r ON r.id = e.rule_id").
		Where("e.fired_at >= ? AND e.fired_at <= ? AND e.trigger_type IN ?", from, to, sloEventTriggers)
}

// sloIncidentScopeFilter 给事件查询追加「目标维度 = scope」的收敛条件。
//
// 两层判定：
//  1. `r.target_type` 明确等于目标维度 → 命中；
//  2. `r.target_type` 缺失（FR-462 之前的旧规则）→ 只在触发类型**只可能属于该维度**时才收：
//     `node_offline` 恒为节点、`instance_crash` 恒为实例。
//     `metric` 在 node 与 instance **两个维度都存在**（`AlertRule.TargetType` 可为 "node" 或
//     "instance"，见 `alert_evaluator.go` 的 `evaluateNodeMetricRule`/`metricTargetInstances`），
//     而两表 id 独立自增会撞号——无法安全判别时**宁可少计也不跨维度错计**
//     （与 `health_wall.go` 的 `attributeAlertNode`「维度完全缺失则不归因」同口径）。
func sloIncidentScopeFilter(q *gorm.DB, scope model.MetricScope) *gorm.DB {
	want := string(scope)
	switch scope {
	case model.MetricScopeNode:
		return q.Where("r.target_type = ? OR (COALESCE(r.target_type, '') = '' AND e.trigger_type = ?)",
			want, model.AlertTriggerNodeOffline)
	case model.MetricScopeInstance:
		return q.Where("r.target_type = ? OR (COALESCE(r.target_type, '') = '' AND e.trigger_type = ?)",
			want, model.AlertTriggerInstanceCrash)
	default:
		return q.Where("r.target_type = ?", want)
	}
}

// sloIncidents 聚合窗口内故障：次数、进行中数、MTTR（Σ(恢复-触发)/已恢复数）。
// scope 用于收敛目标维度（M3），调用方保证 scope 为 node/instance。
func (s *MetricService) sloIncidents(targetID uint, scope model.MetricScope, from, to time.Time) (int, int, *float64, error) {
	if targetID == 0 {
		return 0, 0, nil, nil
	}
	q := sloEventQuery(s.db, from, to).Where("e.target_id = ?", targetID)
	q = sloIncidentScopeFilter(q, scope)
	var events []model.AlertEvent
	if err := q.Find(&events).Error; err != nil {
		return 0, 0, nil, err
	}
	incidents := len(events)
	active := 0
	var sumResolved float64
	resolvedCount := 0
	for i := range events {
		e := &events[i]
		if !e.Resolved {
			active++
			continue
		}
		if e.ResolvedAt != nil {
			sumResolved += e.ResolvedAt.Sub(e.FiredAt).Seconds()
			resolvedCount++
		}
	}
	var mttr *float64
	if resolvedCount > 0 {
		v := sumResolved / float64(resolvedCount)
		mttr = &v
	}
	return incidents, active, mttr, nil
}
