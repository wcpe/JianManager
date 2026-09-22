package service

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// capacityMinSamples 容量外推所需的最小样本数（FR-464）。
const capacityMinSamples = 100

// capacityMaxSlopePoints Theil–Sen 斜率计算的点数上限（超出则等距抽稀，避免 O(n²) 爆炸）。
const capacityMaxSlopePoints = 800

// capacityMaxHorizonDays 耗尽预测的合理时间上界（天）。100 年。
//
// 存在的理由：β 极小（如 1e-3 B/s）而剩余空间很大时，tExhaust 可达 3e8 天量级，
// time.Duration 以纳秒为 int64，约 292 年后溢出成**过去时间**（负 Duration），
// 会让 `exhaustAt` 呈现为已过期、也让前端把「几乎不增长」误读为「马上耗尽」。
// 故超过上界一律判为「实际不耗尽」：ExhaustAt/CI 置 null，并在 Note 说明。
const capacityMaxHorizonDays = 100 * 365.0

// eightyZ 正态近似 80% 置信区间的分位（双尾各 10%）。
const eightyZ = 1.2815515655446004

// 容量预测置信度（ForecastResult.Confidence）的具名取值（m2）。
//
// 为什么提为常量：该字段是**契约值**——`CapacityForecastCard.tsx` 按
// `confidence === 'insufficient'` 决定是否隐藏预测数字，前端类型亦为
// `'high' | 'low' | 'insufficient'`；同时 `CapacityTrendAlerter.Notify` 用它做
// 「是否评估阈值」的判据。原先裸字符串散落在赋值（3 处）与比较（1 处），
// 任一处拼写漂移都会静默改变告警行为（前端/告警器都不报错）。
const (
	// ForecastConfidenceHigh 样本充分且斜率相对标准误小。
	ForecastConfidenceHigh = "high"
	// ForecastConfidenceLow 有预测但斜率标准误偏大（区间宽）。
	ForecastConfidenceLow = "low"
	// ForecastConfidenceInsufficient 样本不足或趋势不显著，不给预测数字。
	ForecastConfidenceInsufficient = "insufficient"
)

// TrendPoint 外推用的一个 (相对秒, 值) 点。
type TrendPoint struct {
	T float64 // 相对窗口起点的秒
	V float64
}

// LinearForecast Theil–Sen 稳健线性拟合结果（FR-464）。
type LinearForecast struct {
	SlopePerSec float64 // 每秒增量 β
	Intercept   float64
	ResidStd    float64 // 残差标准差 σ_res
	SlopeStdErr float64 // 斜率标准误 σ_β
	Samples     int
}

// ForecastQuery 容量预测查询参数（FR-464）。
type ForecastQuery struct {
	Scope      model.MetricScope
	NodeUUID   string
	InstanceID string
	Metrics    []string // 待预测的「已用」指标键；空则按 scope 取默认
	From, To   time.Time
}

// ForecastResult 单指标容量外推结果（FR-464）。Exhaust* 为 null 表示无增长/样本不足，不伪造预测。
type ForecastResult struct {
	TargetID        string     `json:"targetId"`
	MetricKey       string     `json:"metricKey"`
	NowValue        float64    `json:"nowValue"`
	LimitValue      float64    `json:"limitValue"`
	SlopePerSec     float64    `json:"slopePerSec"`
	ExhaustAt       *time.Time `json:"exhaustAt"`
	ExhaustLowDays  *float64   `json:"exhaustLowDays"`  // 80% 置信下界（天）
	ExhaustHighDays *float64   `json:"exhaustHighDays"` // 80% 置信上界（天）
	Confidence      string     `json:"confidence"`      // ForecastConfidenceHigh | Low | Insufficient
	Samples         int        `json:"samples"`
	Note            string     `json:"note,omitempty"`
}

// ForecastLinear 对点集做 Theil–Sen 稳健斜率 + 最小二乘残差/斜率标准误（FR-464 纯函数内核）。
// 返回派生耗尽时间与置信区间所需的统计量。
func ForecastLinear(points []TrendPoint) LinearForecast {
	lf := LinearForecast{Samples: len(points)}
	if len(points) < 2 {
		return lf
	}
	slope := theilSenSlope(points)
	// 截距取 median(v - β t)，对离群更稳健。
	intercepts := make([]float64, len(points))
	for i, p := range points {
		intercepts[i] = p.V - slope*p.T
	}
	intercept := median(intercepts)

	// 残差与斜率标准误（正态近似）。
	var ss, stt, tSum float64
	for _, p := range points {
		tSum += p.T
	}
	tMean := tSum / float64(len(points))
	for _, p := range points {
		e := p.V - (intercept + slope*p.T)
		ss += e * e
		d := p.T - tMean
		stt += d * d
	}
	dof := float64(len(points) - 2)
	if dof > 0 {
		lf.ResidStd = math.Sqrt(ss / dof)
	}
	if stt > 0 {
		lf.SlopeStdErr = lf.ResidStd / math.Sqrt(stt)
	}
	lf.SlopePerSec = slope
	lf.Intercept = intercept
	return lf
}

// theilSenSlope 计算两两点斜率的中位数（点数超上限时等距抽稀）。
func theilSenSlope(points []TrendPoint) float64 {
	pts := downsampleTrend(points, capacityMaxSlopePoints)
	slopes := make([]float64, 0, len(pts)*(len(pts)-1)/2)
	for i := 0; i < len(pts); i++ {
		for j := i + 1; j < len(pts); j++ {
			dt := pts[j].T - pts[i].T
			if dt == 0 {
				continue
			}
			slopes = append(slopes, (pts[j].V-pts[i].V)/dt)
		}
	}
	return median(slopes)
}

// downsampleTrend 等距抽稀到至多 max 个点（保留首尾），保证斜率计算有界。
func downsampleTrend(points []TrendPoint, max int) []TrendPoint {
	if len(points) <= max || max < 2 {
		return points
	}
	out := make([]TrendPoint, 0, max)
	step := float64(len(points)-1) / float64(max-1)
	for i := 0; i < max; i++ {
		idx := int(math.Round(float64(i) * step))
		if idx >= len(points) {
			idx = len(points) - 1
		}
		out = append(out, points[idx])
	}
	return out
}

// ForecastCapacity 对关键资源做线性外推的耗尽预测与 80% 置信区间（FR-464）。
//
// 依赖 alert_baseline.go「统计工具」分区的 median（升序排序副本，空返回 0）：
// Theil–Sen 的两点斜率中位数与稳健截距都建立在「中位数对离群不敏感」之上，
// 故本文件的稳健性完全落在那一处实现，不另立副本。
func (s *MetricService) ForecastCapacity(q ForecastQuery) ([]ForecastResult, error) {
	span := q.To.Sub(q.From)
	if span <= 0 {
		return nil, fmt.Errorf("非法窗口")
	}
	res := selectResolution(span, "auto")
	from, to := q.From.UTC(), q.To.UTC()
	now := to

	keys := q.Metrics
	if len(keys) == 0 {
		if q.Scope == model.MetricScopeNode {
			keys = []string{model.MetricNodeDiskUsed, model.MetricNodeMemUsed}
		} else {
			keys = []string{model.MetricInstHeapUsed}
		}
	}
	targetUUID := q.InstanceID
	if q.Scope == model.MetricScopeNode {
		targetUUID = q.NodeUUID
	}

	out := make([]ForecastResult, 0, len(keys))
	for _, usedKey := range keys {
		fr := ForecastResult{TargetID: targetUUID, MetricKey: usedKey, Confidence: ForecastConfidenceInsufficient}
		pts, lastVal, ok := s.forecastPoints(q, usedKey, from, to, res)
		fr.Samples = len(pts)
		if !ok || len(pts) < capacityMinSamples {
			fr.Note = "样本不足，不预测"
			out = append(out, fr)
			continue
		}
		limitVal, hasLimit := s.forecastLimit(q, usedKey, from, to, res)
		if !hasLimit {
			fr.Note = "缺少容量上限（无上限序列且无节点容量快照），不预测"
			out = append(out, fr)
			continue
		}
		fr.NowValue = lastVal
		fr.LimitValue = limitVal
		lf := ForecastLinear(pts)
		fr.SlopePerSec = lf.SlopePerSec
		// β ≤ 0：无增长趋势，不预测。
		if lf.SlopePerSec <= 0 {
			fr.Note = "无增长趋势，不预测"
			out = append(out, fr)
			continue
		}
		// β 的 80% 区间跨 0 → 趋势不显著。
		if lf.SlopePerSec-eightyZ*lf.SlopeStdErr <= 0 {
			fr.Note = "趋势不显著（斜率置信区间跨 0）"
			out = append(out, fr)
			continue
		}

		remaining := limitVal - lastVal
		if remaining <= 0 {
			fr.Note = "已超上限"
			out = append(out, fr)
			continue
		}
		tExhaust := remaining / lf.SlopePerSec // 秒
		// T 的不确定度：由分子残差与斜率标准误合成。
		// 平方写成自乘而非 math.Pow(x, 2)：语义等价、无 float 幂函数的额外开销（lint QF1005）。
		residTerm := lf.ResidStd / lf.SlopePerSec
		slopeTerm := tExhaust * lf.SlopeStdErr / lf.SlopePerSec
		sigmaT := math.Sqrt(residTerm*residTerm + slopeTerm*slopeTerm)
		low := tExhaust - eightyZ*sigmaT
		high := tExhaust + eightyZ*sigmaT
		if low < 0 {
			low = 0
		}
		lowDays := low / 86400
		highDays := high / 86400
		// 上界钳制（M1）：超过 capacityMaxHorizonDays 视为「实际不耗尽」，不给任何耗尽时间——
		// 既避免 time.Duration 溢出成过去时间，也避免前端渲染 3e8 天这类无意义数字。
		if tExhaust/86400 > capacityMaxHorizonDays {
			fr.Note = fmt.Sprintf("增长极缓（预计 %.0f 天以上才耗尽），超出可预测范围，不预测", capacityMaxHorizonDays)
			out = append(out, fr)
			continue
		}
		// 置信上界同样钳制：避免「点估计在界内、上界却溢出」造成低/高界不自洽。
		if highDays > capacityMaxHorizonDays {
			highDays = capacityMaxHorizonDays
		}
		if lowDays < 0 {
			lowDays = 0
		}
		// 上界钳制见上方 capacityMaxHorizonDays（100 年）：此处把秒数喂给 time.Duration
		// 之前，上面的 `tExhaust/86400 > capacityMaxHorizonDays` 分支已先行 return，
		// 故 tExhaust 恒 < 100 年 ≈ 3.15e9 秒，乘 1e9 后仍在 int64 纳秒（约 292 年）之内，
		// 不会溢出成负 Duration（负值会让 exhaustAt 呈现为过去时间）。
		// 换言之：本行是该常量上界的**唯一消费点**，两处必须成对维护。
		at := now.Add(time.Duration(tExhaust * float64(time.Second)))
		fr.ExhaustAt = &at
		fr.ExhaustLowDays = &lowDays
		fr.ExhaustHighDays = &highDays
		// 定级：样本充分且斜率相对标准误小 → high，否则 low。
		if eightyZ*lf.SlopeStdErr <= 0.5*lf.SlopePerSec {
			fr.Confidence = ForecastConfidenceHigh
		} else {
			fr.Confidence = ForecastConfidenceLow
		}
		out = append(out, fr)
	}
	return out, nil
}

// forecastPoints 取某「已用」指标的窗口点集（升序）与最新值。
//
// B-2 修复：同指标**多条实例级序列**（实例换节点后 node_uuid 变化即产生新序列）此前被无条件
// 混进同一条趋势线，且 lastVal 随迭代被覆盖为「枚举顺序上最后一条序列的末点」——而枚举顺序由
// QuerySeries 的 Order("metric_key, world") 加 SQLite 行序决定，**未定义**，故 nowValue 会在
// 新旧序列的值之间跳变、Samples 变成各序列点数之和，remaining = limit - nowValue 随之失真。
//
// 现按 attribution.go（m2 修复）同口径收敛：**按序列身份择一 + 逐时点去重**。
// 每个时点只保留一条序列的值（优先「窗口内最新时点所属的活跃序列」，其余时点由更旧的序列补全），
// 既保住完整时间线（实例迁移当口不因换序列丢历史、不跌回「样本不足」），又不重复累计。
// NowValue 取**窗口内最新非空时点**的值，与序列枚举顺序无关（同刻冲突由 seriesKeyLess 定序）。
func (s *MetricService) forecastPoints(q ForecastQuery, usedKey string, from, to time.Time, res string) ([]TrendPoint, float64, bool) {
	sq := SeriesQuery{
		Scope:      q.Scope,
		MetricKeys: []string{usedKey},
		From:       from,
		To:         to,
		Resolution: res,
	}
	if q.Scope == model.MetricScopeNode {
		sq.NodeUUID = q.NodeUUID
	} else {
		sq.InstanceID = q.InstanceID
	}
	_, series, err := s.QuerySeries(sq)
	if err != nil {
		return nil, 0, false
	}
	// 只收实例级序列：这些「已用」指标（node_disk_used/node_mem_used/inst_heap_used）从不落
	// world 序列（world 级是分世界分区指标），保持既有「world != "" 跳过」的过滤不变。
	keep := make([]Series, 0, len(series))
	for _, sr := range series {
		if sr.MetricKey == usedKey && sr.World == "" && len(sr.Points) > 0 {
			keep = append(keep, sr)
		}
	}
	// 定序：末点更晚（当前仍在报数的序列）优先 → 同刻冲突时活跃序列胜出。
	sort.SliceStable(keep, func(i, j int) bool { return seriesKeyLess(keep[i], keep[j]) })

	byTS := make(map[int64]float64, len(keep)*8)
	for _, sr := range keep {
		for _, p := range sr.Points {
			if p.Avg == nil {
				continue // 缺测为断点，不补假值（ADR-013）
			}
			k := p.TS.UnixNano()
			if _, seen := byTS[k]; seen {
				continue // 逐时点择一：先写的活跃序列已占该时点
			}
			byTS[k] = *p.Avg
		}
	}
	keys := make([]int64, 0, len(byTS))
	for k := range byTS {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	pts := make([]TrendPoint, 0, len(keys))
	for _, k := range keys {
		pts = append(pts, TrendPoint{T: time.Unix(0, k).Sub(from).Seconds(), V: byTS[k]})
	}
	if len(pts) == 0 {
		return pts, 0, false
	}
	// pts 已升序 → 末项即窗口内最新非空时点的值。
	return pts, pts[len(pts)-1].V, true
}

// lastValueTS 返回序列最后一个非空点的时刻（无值返回零值 + false）。
func lastValueTS(sr Series) (time.Time, bool) {
	for i := len(sr.Points) - 1; i >= 0; i-- {
		if sr.Points[i].Avg != nil {
			return sr.Points[i].TS, true
		}
	}
	return time.Time{}, false
}

// seriesKeyLess 给出与枚举顺序无关的序列定序键：末值时刻（新→旧）→ 点数（多→少）→ 逐点字典序。
// 前两级已覆盖「实例换节点」这类真实场景（活跃序列末点更晚、历史更全）；
// 逐点字典序只用于彻底同形序列的确定性收尾，保证同一批数据无论枚举顺序如何都得到同一结果。
func seriesKeyLess(a, b Series) bool {
	at, aok := lastValueTS(a)
	bt, bok := lastValueTS(b)
	if !at.Equal(bt) {
		return at.After(bt)
	}
	if aok != bok {
		return aok
	}
	if len(a.Points) != len(b.Points) {
		return len(a.Points) > len(b.Points)
	}
	for i := range a.Points {
		if !a.Points[i].TS.Equal(b.Points[i].TS) {
			return a.Points[i].TS.After(b.Points[i].TS)
		}
		av, bv := a.Points[i].Avg, b.Points[i].Avg
		if (av == nil) != (bv == nil) {
			return av != nil
		}
		if av != nil && *av != *bv {
			return *av > *bv
		}
	}
	return false
}

// forecastLimit 解析某「已用」指标的容量上限：优先上限序列，回退节点容量快照。
func (s *MetricService) forecastLimit(q ForecastQuery, usedKey string, from, to time.Time, res string) (float64, bool) {
	limitKey := saturationLimitKey(usedKey)
	if limitKey != "" {
		sq := SeriesQuery{
			Scope:      q.Scope,
			MetricKeys: []string{limitKey},
			From:       from,
			To:         to,
			Resolution: res,
		}
		if q.Scope == model.MetricScopeNode {
			sq.NodeUUID = q.NodeUUID
		} else {
			sq.InstanceID = q.InstanceID
		}
		_, series, err := s.QuerySeries(sq)
		if err == nil {
			for _, sr := range series {
				if sr.MetricKey != limitKey || sr.World != "" {
					continue
				}
				for i := len(sr.Points) - 1; i >= 0; i-- {
					if sr.Points[i].Avg != nil && *sr.Points[i].Avg > 0 {
						return *sr.Points[i].Avg, true
					}
				}
			}
		}
	}
	// 回退节点容量快照（生产从不写 node_*_total 序列）。
	if q.Scope == model.MetricScopeNode && q.NodeUUID != "" {
		var node model.Node
		if err := s.db.Where("uuid = ?", q.NodeUUID).First(&node).Error; err == nil {
			if v := nodeSnapshotLimit(usedKey, &node); v != nil && *v > 0 {
				return *v, true
			}
		}
	}
	return 0, false
}

// CapacityTrendThresholdDays 默认「预计 N 天内耗尽」告警阈值（天）。
const CapacityTrendThresholdDays = 7.0

// capacityReNotifyInterval 同一（规则, 目标, 指标）趋势告警的最小重发间隔。
//
// 必要性：趋势告警由**查询**触发（前端 60s 轮询），而 `DedupWindowSec` 默认 0（不去抖）。
// M2 修复把 Resolvable 改为 false 后，Dispatcher 不再把复发聚合进旧事件，
// 若不加节流，一个持续命中的目标会每 60s 落一条事件（1440 条/天）。
// 用户显式配置了 `DedupWindowSec` 时以它为准（尊重既有去抖语义）。
const capacityReNotifyInterval = 6 * time.Hour

// CapacityTrendAlerter 在容量预测命中「预计 N 天内耗尽」时经告警体系发趋势告警（FR-464）。
//
// 复用既有 metric 规则（TriggerType=metric）作为投递规则与去抖窗口，不新增触发类型；
// DedupKey = metric:<ruleID>:<targetID>:capacity:<metricKey>。
// 触发频率由 capacityReNotifyInterval（或规则自身的 DedupWindowSec）节流。
type CapacityTrendAlerter struct {
	db         *gorm.DB
	dispatcher *AlertDispatcher
	// mu 串行化「去抖判定 → 落库」这一对读-写操作。
	//
	// M-8 必要性：`Notify` 由 HTTP 处理器并发调用（前端 60s 轮询 × 多个调用方/标签页），
	// 而 `dueForNotify` 读「上一事件」与 `dispatcher.Fire` 写「新事件」之间存在窗口——
	// 无锁时两个并发查询会**都**读到「无历史事件」、都判定该发，于是同一条告警落两条事件
	// 并外发两次。异步化只把投递移出线程，判定与落库仍在调用线程，故必须显式串行化
	// 才能真正做到「同一去抖键不重复发」。
	// 临界区只含 DB 读 + 插入；正常路径下外发投递已异步入队（不阻塞），
	// 仅当队列满/已关停的退化路径才会在锁内内联投递（该情形已要求 ≥42 分钟积压，可接受）。
	mu sync.Mutex
}

// NewCapacityTrendAlerter 创建容量趋势告警器。
func NewCapacityTrendAlerter(db *gorm.DB, dispatcher *AlertDispatcher) *CapacityTrendAlerter {
	return &CapacityTrendAlerter{db: db, dispatcher: dispatcher}
}

// Notify 对满足阈值（Confidence != insufficient 且 ExhaustLowDays < thresholdDays）的预测逐条触发。
// 返回实际触发条数。无匹配 metric 规则时不触发（尊重用户配置，避免凭空造告警）。
//
// m6 说明：全局规则（TargetID 为 nil）在这里与 `AlertEvaluator.evaluateNodeMetricRule` 同族——
// 全局规则本就按「每个目标一条」语义扇出（既有告警体系行为），故不禁止；事件量改由
// capacityReNotifyInterval 节流（每目标每指标 ≤ 4 条/天，不再是「每次查询各一条」）。
//
// M-8 说明：本函数由 `GET /metrics/capacity/forecast` **同步**调用，故触发时置
// `DeliverAsync=true`——只有**外发投递**（webhook/IM/邮件的 HTTP/SMTP，
// `ChannelNotifier` 超时 10s）移到后台队列；去抖判定与落库仍在本线程完成，
// 并由 a.mu 串行化（见该字段注释），保证同一去抖键在并发查询下不重复触发/外发。
func (a *CapacityTrendAlerter) Notify(scope model.MetricScope, targetID uint, targetName string, results []ForecastResult, thresholdDays float64) int {
	if a == nil || a.dispatcher == nil || targetID == 0 {
		return 0
	}
	if thresholdDays <= 0 {
		thresholdDays = CapacityTrendThresholdDays
	}
	var rules []model.AlertRule
	if err := a.db.Where("enabled = ? AND target_type = ?", true, string(scope)).Order("id").Find(&rules).Error; err != nil || len(rules) == 0 {
		return 0
	}
	// 串行化「去抖判定 → 落库」：整段临界区只含 DB 读 + 事件插入（外发已异步入队），
	// 故持锁时间在毫秒级；若不加锁，并发查询会各自读到「无历史事件」而重复触发。
	a.mu.Lock()
	defer a.mu.Unlock()
	fired := 0
	for _, fr := range results {
		if fr.Confidence == ForecastConfidenceInsufficient || fr.ExhaustLowDays == nil {
			continue
		}
		if *fr.ExhaustLowDays >= thresholdDays {
			continue
		}
		rule := matchCapacityRule(rules, targetID)
		if rule == nil {
			continue
		}
		key := fmt.Sprintf("metric:%d:%d:capacity:%s", rule.ID, targetID, fr.MetricKey)
		if !a.dueForNotify(*rule, key) {
			continue
		}
		a.dispatcher.Fire(AlertTrigger{
			Rule:     rule,
			TargetID: targetID,
			DedupKey: key,
			Value:    *fr.ExhaustLowDays,
			Message: fmt.Sprintf("%s %s 预计 %.1f 天内耗尽（80%% 置信下界，当前 %.0f / 上限 %.0f）",
				scopeLabel(string(scope)), targetName, *fr.ExhaustLowDays, fr.NowValue, fr.LimitValue),
			// M-8：外发投递不阻塞本 GET 请求线程（见函数注释）。
			DeliverAsync: true,
			// Resolvable=false（M2 修复）：容量趋势是**瞬时判定**——每次查询独立重算，
			// 没有「条件恢复」这一事件可观测（预测不再命中只表现为本次不 Fire，不产生任何信号）。
			// 若置 true，Dispatcher 会因「已存在未恢复事件」而永久只累计不通知
			// （alert_dispatcher.go 的 hasActive + Resolvable 分支），下次真正恶化时用户再也收不到告警。
			// 故落库即视为已解决（与日志/玩家事件同族），重复抑制靠本函数的 dueForNotify 节流。
			Resolvable: false,
		})
		fired++
	}
	return fired
}

// dueForNotify 判断某去抖键是否已超过重发间隔。无历史事件 → 可发；
// 查询失败按「可发」处理（宁可多发一条，也不因查询故障永久静音）。
// 时间基准取 dispatcher 的注入式时钟，便于测试推进时间而不依赖真实等待。
func (a *CapacityTrendAlerter) dueForNotify(rule model.AlertRule, key string) bool {
	interval := capacityReNotifyInterval
	if rule.DedupWindowSec > 0 {
		interval = time.Duration(rule.DedupWindowSec) * time.Second
	}
	var last model.AlertEvent
	err := a.db.Select("fired_at").Where("rule_id = ? AND dedup_key = ?", rule.ID, key).
		Order("fired_at DESC").First(&last).Error
	if err != nil {
		return true
	}
	now := time.Now()
	if a.dispatcher != nil && a.dispatcher.now != nil {
		now = a.dispatcher.now()
	}
	return now.Sub(last.FiredAt) >= interval
}

// matchCapacityRule 选取用于投递的目标规则：优先目标专属规则，否则全局规则（TargetID 为 nil）。
func matchCapacityRule(rules []model.AlertRule, targetID uint) *model.AlertRule {
	var global *model.AlertRule
	for i := range rules {
		r := &rules[i]
		if r.TargetID != nil {
			if *r.TargetID == targetID {
				return r
			}
			continue
		}
		if global == nil {
			global = r
		}
	}
	return global
}
