package service

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// attributionMinSamples 归因所需的最小对齐样本数；不足则返回 insufficient 而不硬给排序（FR-465）。
const attributionMinSamples = 30

// 归因结果状态（AttributionResult.Status）的具名取值（m2）。
//
// 为什么提为常量：该字段是**契约值**——`AttributionCard.tsx` 按 `status === 'insufficient'`
// 决定是否渲染因子列表，前端类型也声明为 `'ok' | 'insufficient'`。原先裸字符串散落在
// `return nil, samples, AttributionStatusInsufficient`（3 处）与 `status != "ok"` 的比较里，
// 任一处拼写漂移都会静默改变前端行为（既不报错也不被类型系统拦住）。
const (
	// AttributionStatusOK 归因成功，Factors 非空。
	AttributionStatusOK = "ok"
	// AttributionStatusInsufficient 样本不足或无显著因子，Factors 为空、不伪造排序。
	AttributionStatusInsufficient = "insufficient"
)

// attributionTopN 归因输出保留的因子数上限。
const attributionTopN = 6

// AttributionQuery 性能归因查询参数（FR-465）。v1 仅支持 instance 维度。
type AttributionQuery struct {
	InstanceID string // 实例 UUID
	Target     string // 目标指标键，默认 inst_tps
	From, To   time.Time
}

// AttributionFactorInput 一个候选因子与目标对齐后的取值序列（同长；NaN 表示该拍该因子缺测）。
type AttributionFactorInput struct {
	MetricKey string
	Label     string
	Values    []float64
}

// Factor 单个因子的归因评分。
type Factor struct {
	MetricKey   string  `json:"metricKey"`
	Label       string  `json:"label"`
	Correlation float64 `json:"correlation"` // 与目标的 Pearson 相关（TPS 越低因子越高时为负）
	Weight      float64 `json:"weight"`      // 归一化 |r|·|z| 贡献权重，合计 1
	Note        string  `json:"note,omitempty"`
}

// AttributionWindow 归因窗口。
type AttributionWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// AttributionResult 性能归因结果（FR-465）。Status=insufficient 时 Factors 为空、不伪造排序。
type AttributionResult struct {
	Target  string            `json:"target"`
	Window  AttributionWindow `json:"window"`
	Status  string            `json:"status"` // AttributionStatusOK | AttributionStatusInsufficient
	TLDR    string            `json:"tldr"`
	Factors []Factor          `json:"factors"`
	Samples int               `json:"samples"`
}

// AnalyzeAttribution 对指定实例窗口内的目标指标做相关性 + 标准化系数归因（FR-465）。
// 世界级因子（区块/实体/方块实体）先按 instance_id 跨 world 求和成实例级序列。
func (s *MetricService) AnalyzeAttribution(q AttributionQuery) (AttributionResult, error) {
	target := q.Target
	if target == "" {
		target = model.MetricInstTPS
	}
	res := selectResolution(q.To.Sub(q.From), "auto")
	keys := append([]string{target, model.MetricInstHeapUsed, model.MetricInstHeapMax},
		attributionCandidateKeys()...)

	_, series, err := s.QuerySeries(SeriesQuery{
		Scope:      model.MetricScopeInstance,
		InstanceID: q.InstanceID,
		MetricKeys: dedupeKeys(keys),
		From:       q.From.UTC(),
		To:         q.To.UTC(),
		Resolution: res,
	})
	if err != nil {
		return AttributionResult{}, err
	}

	instBuckets := map[string]map[int64]float64{}  // instance 级：key → bucket → 值（单序列）
	worldBuckets := map[string]map[int64]float64{} // world 级：key → bucket → Σ各世界
	// m2 修复：统一「按 series 身份择一 + 跨 world 求和」语义。
	//
	// 原实现在 instance 级用 `=` 覆盖：同一实例同一指标若存在多条实例级序列
	// （如实例换节点后 node_uuid 变化会产生新序列），后读到的序列会静默覆盖先读到的，
	// 哪个存活取决于查询返回顺序 —— 结果不确定，且可能把「有数据的那条」覆盖成空。
	// 现改为：instance 级每个 bucket **只取首个非空值**（序列已按 id 升序返回，结果确定），
	// world 级仍跨 world 求和（各世界是同一实例的不同分区，天然可加）。
	seenInst := map[string]map[int64]bool{}
	for _, sr := range series {
		if sr.World != "" {
			b := worldBuckets[sr.MetricKey]
			if b == nil {
				b = map[int64]float64{}
				worldBuckets[sr.MetricKey] = b
			}
			for _, p := range sr.Points {
				if p.Avg == nil {
					continue
				}
				b[p.TS.UnixNano()] += *p.Avg
			}
			continue
		}
		b := instBuckets[sr.MetricKey]
		if b == nil {
			b = map[int64]float64{}
			instBuckets[sr.MetricKey] = b
			seenInst[sr.MetricKey] = map[int64]bool{}
		}
		seen := seenInst[sr.MetricKey]
		for _, p := range sr.Points {
			if p.Avg == nil {
				continue
			}
			ts := p.TS.UnixNano()
			if seen[ts] {
				continue // 同 metric 多条实例级序列：按身份择一，避免静默覆盖
			}
			b[ts] = *p.Avg
			seen[ts] = true
		}
	}

	// 堆占比因子：inst_heap_used / inst_heap_max 逐桶求比（缺上限或上限为 0 时该桶缺测）。
	heapRatio := map[int64]float64{}
	if used, ok := instBuckets[model.MetricInstHeapUsed]; ok {
		if max, ok2 := instBuckets[model.MetricInstHeapMax]; ok2 {
			for ts, u := range used {
				if m, ok3 := max[ts]; ok3 && m > 0 {
					heapRatio[ts] = u / m
				}
			}
		}
	}

	targetBuckets := instBuckets[target]
	tsList := make([]int64, 0, len(targetBuckets))
	for ts := range targetBuckets {
		tsList = append(tsList, ts)
	}
	sort.Slice(tsList, func(i, j int) bool { return tsList[i] < tsList[j] })

	targetVals := make([]float64, 0, len(tsList))
	for _, ts := range tsList {
		targetVals = append(targetVals, targetBuckets[ts])
	}

	inputs := make([]AttributionFactorInput, 0, 8)
	for _, c := range attributionCandidates {
		var b map[int64]float64
		switch {
		case c.Key == "heap_ratio":
			b = heapRatio
		case c.WorldSum:
			b = worldBuckets[c.Key]
		default:
			b = instBuckets[c.Key]
		}
		if b == nil {
			continue
		}
		vals := make([]float64, len(tsList))
		for i, ts := range tsList {
			if v, ok := b[ts]; ok {
				vals[i] = v
			} else {
				vals[i] = math.NaN()
			}
		}
		inputs = append(inputs, AttributionFactorInput{MetricKey: c.Key, Label: c.Label, Values: vals})
	}

	factors, samples, status := AnalyzeAttributionFactors(targetVals, inputs)
	return AttributionResult{
		Target:  target,
		Window:  AttributionWindow{From: q.From.UTC(), To: q.To.UTC()},
		Status:  status,
		TLDR:    attributionTLDR(status, factors),
		Factors: factors,
		Samples: samples,
	}, nil
}

// attributionCandidate 一个候选因子的取数来源。
type attributionCandidate struct {
	Key      string
	Label    string
	WorldSum bool // true=world 级序列，按 instance_id 跨 world 求和
}

var attributionCandidates = []attributionCandidate{
	{Key: model.MetricInstGCTime, Label: "GC 暂停占用"},
	{Key: model.MetricInstGCCount, Label: "GC 次数"},
	{Key: model.MetricWorldLoadedChunks, Label: "已加载区块", WorldSum: true},
	{Key: model.MetricWorldEntities, Label: "实体数", WorldSum: true},
	{Key: model.MetricWorldTileEntities, Label: "方块实体数", WorldSum: true},
	{Key: "heap_ratio", Label: "堆内存占比"},
	{Key: model.MetricInstThreads, Label: "线程数"},
}

func attributionCandidateKeys() []string {
	out := make([]string, 0, len(attributionCandidates))
	for _, c := range attributionCandidates {
		if c.Key == "heap_ratio" {
			continue // 由 used/max 派生，无需单独取序列
		}
		out = append(out, c.Key)
	}
	return out
}

// AnalyzeAttributionFactors 是归因评分的纯函数内核（可穷举测试，FR-465）。
//
// 步骤：① 逐因子算与目标的 Pearson 相关 r；② 取目标低于基线（窗口 P10）的样本子集，
// 算各因子相对窗口均值的 z 分；③ 贡献权重 = 归一化的 |r|·|z|，降序输出 TopN。
// 目标样本数 < attributionMinSamples 或全部权重为 0（无显著因子）→ status=insufficient。
// 返回 (factors, samples, status)。
func AnalyzeAttributionFactors(target []float64, inputs []AttributionFactorInput) ([]Factor, int, string) {
	samples := 0
	for _, v := range target {
		if !math.IsNaN(v) {
			samples++
		}
	}
	if samples < attributionMinSamples {
		return nil, samples, AttributionStatusInsufficient
	}

	// 目标基线：窗口 P10（低 TPS 视为劣化）。
	cleanTarget := make([]float64, 0, len(target))
	for _, v := range target {
		if !math.IsNaN(v) {
			cleanTarget = append(cleanTarget, v)
		}
	}
	p10 := percentile(cleanTarget, 0.10)

	type scored struct {
		f    Factor
		best float64 // |r|·|z|
	}
	out := make([]scored, 0, len(inputs))
	var total float64
	for _, in := range inputs {
		r, pairs := pearsonR(target, in.Values)
		if pairs < attributionMinSamples {
			continue
		}
		// 低 TPS 子集：目标低于基线、且该因子本拍有值。
		sub := make([]float64, 0, len(target))
		all := make([]float64, 0, len(in.Values))
		for i, v := range in.Values {
			if math.IsNaN(v) {
				continue
			}
			all = append(all, v)
			if !math.IsNaN(target[i]) && target[i] <= p10 {
				sub = append(sub, v)
			}
		}
		if len(sub) == 0 || len(all) == 0 {
			continue
		}
		mean, std := meanStd(all)
		if std == 0 {
			continue // 无波动因子无法解释劣化
		}
		z := meanStdZ(sub, mean, std)
		score := math.Abs(r) * math.Abs(z)
		if score == 0 {
			continue
		}
		out = append(out, scored{f: Factor{
			MetricKey:   in.MetricKey,
			Label:       in.Label,
			Correlation: r,
			Note:        "相关性非因果",
		}, best: score})
		total += score
	}

	if total == 0 || len(out) == 0 {
		return nil, samples, AttributionStatusInsufficient
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].best == out[j].best {
			return out[i].f.MetricKey < out[j].f.MetricKey
		}
		return out[i].best > out[j].best
	})
	if len(out) > attributionTopN {
		out = out[:attributionTopN]
	}
	factors := make([]Factor, 0, len(out))
	for _, s := range out {
		f := s.f
		f.Weight = s.best / total
		factors = append(factors, f)
	}
	return factors, samples, AttributionStatusOK
}

// attributionTLDR 依据状态与因子生成一句话结论。
func attributionTLDR(status string, factors []Factor) string {
	if status != AttributionStatusOK || len(factors) == 0 {
		return "样本不足或无显著因子，无法给出可靠归因"
	}
	top := factors[0]
	return fmt.Sprintf("劣化主因：%s（权重 %.2f）", top.Label, top.Weight)
}

// pearsonR 计算 Pearson 相关系数，跳过任一为 NaN 的样本对；返回 (r, 有效对数)。
// 任一侧无方差时返回 (0, n)。
func pearsonR(xs, ys []float64) (float64, int) {
	if len(xs) != len(ys) || len(xs) == 0 {
		return 0, 0
	}
	var n int
	var sx, sy float64
	for i := range xs {
		if math.IsNaN(xs[i]) || math.IsNaN(ys[i]) {
			continue
		}
		sx += xs[i]
		sy += ys[i]
		n++
	}
	if n == 0 {
		return 0, 0
	}
	mx, my := sx/float64(n), sy/float64(n)
	var cov, vx, vy float64
	for i := range xs {
		if math.IsNaN(xs[i]) || math.IsNaN(ys[i]) {
			continue
		}
		dx, dy := xs[i]-mx, ys[i]-my
		cov += dx * dy
		vx += dx * dx
		vy += dy * dy
	}
	if vx == 0 || vy == 0 {
		return 0, n
	}
	return cov / math.Sqrt(vx*vy), n
}

// percentile 返回切片（内部升序排序）的 p 分位（0~1），用线性插值；空切片返回 0。
func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	if p <= 0 {
		return cp[0]
	}
	if p >= 1 {
		return cp[len(cp)-1]
	}
	idx := p * float64(len(cp)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return cp[lo]
	}
	frac := idx - float64(lo)
	return cp[lo]*(1-frac) + cp[hi]*frac
}

// meanStd 返回均值和总体标准差（按 n 归一，避免小样本放大离群点）。
//
// m7：实现收口到 alert_baseline.go「统计工具」分区的 mean/stdDev——二者数学等价
// （都是按 n 归一的总体标准差，n<2 时为 0），此前本文件另留了一份逐行重复的副本。
// 保留本函数只为调用点可读性（一次拿到 mean+std），不再持有独立算法，
// 避免两处将来各自漂移（例如一处改成样本标准差）。
func meanStd(vals []float64) (float64, float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	return mean(vals), stdDev(vals)
}

// meanStdZ 返回子集均值相对给定基准均值/标准差的 z 分。
// 参数命名为 base 而非 mean，以便复用 alert_baseline.go 的 mean（同一统计工具分区）。
func meanStdZ(vals []float64, base, std float64) float64 {
	if len(vals) == 0 || std == 0 {
		return 0
	}
	return (mean(vals) - base) / std
}

// dedupeKeys 去重并保序，剔除空白项。
func dedupeKeys(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, k := range in {
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}
