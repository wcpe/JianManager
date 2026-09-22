package service

import (
	"errors"
	"log/slog"
	"math"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// crashCorrelationWindow 崩溃关联窗口：查崩溃时刻往前 Δ 的资源样本（FR-470 §2.3）。
const crashCorrelationWindow = 5 * time.Minute

// crashMetricSource 是关联所需的堆指标数据源（*MetricService 实现）；可为 nil（跳过堆关联）。
type crashMetricSource interface {
	LatestValue(scope model.MetricScope, nodeUUID, instanceID, metricKey string, since time.Time) (*float64, error)
}

// CrashCorrelation 崩溃与资源的关联证据（FR-470 §2.3，OOM 证据链）。
type CrashCorrelation struct {
	// NearOOM 崩前 RSS 是否逼近实例内存上限（或样本缺失时无法判定为 false）。
	NearOOM bool `json:"nearOom"`
	// RSSAtCrash 崩溃窗口内进程树 RSS 峰值（字节）；无样本为 0。
	RSSAtCrash int64 `json:"rssAtCrash"`
	// MemLimitMB 实例内存上限（MiB）；0=未设限。
	MemLimitMB int64 `json:"memLimitMb"`
	// HeapUsedMax 崩溃窗口内 JVM 堆已用峰值（字节）；无探针样本为 0。
	HeapUsedMax int64 `json:"heapUsedMax"`
	// GCNote GC 关联提示（FR-465 落地前为「待依赖」，见 spec §5）。
	GCNote string `json:"gcNote"`
	// Note 人可读结论（无数据/降级原因）。
	Note string `json:"note,omitempty"`
}

// CrashCorrelationService 崩溃与资源关联查询（FR-470 §2.3）。只读，不写新表。
type CrashCorrelationService struct {
	db *gorm.DB
	// metrics 堆指标数据源；nil 时仅做进程 RSS 关联。
	metrics crashMetricSource
}

// NewCrashCorrelationService 创建崩溃关联服务。
func NewCrashCorrelationService(db *gorm.DB) *CrashCorrelationService {
	return &CrashCorrelationService{db: db}
}

// SetMetrics 注入时序数据源（FR-462 同源），用于堆指标关联。
func (s *CrashCorrelationService) SetMetrics(m crashMetricSource) { s.metrics = m }

// CorrelateInstance 计算某实例在某时刻的崩溃关联证据。
// 窗口 = [occurredAt − 5min, occurredAt]；无样本时返回零值 + Note 降级说明（不报错）。
func (s *CrashCorrelationService) CorrelateInstance(instanceID uint, occurredAt time.Time) (CrashCorrelation, error) {
	out := CrashCorrelation{}
	var inst model.Instance
	if err := s.db.Select("id", "uuid", "node_id", "mem_limit_mb").First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return out, ErrInstanceNotFound
		}
		return out, err
	}
	out.MemLimitMB = inst.MemLimitMB

	to := occurredAt
	from := to.Add(-crashCorrelationWindow)
	if to.IsZero() {
		to = time.Now()
		from = to.Add(-crashCorrelationWindow)
	}

	// 1. 进程树 RSS 峰值（FR-170 ProcessMetricSnapshot）。
	//
	// R6：口径必须与配额维度**同源**（quota_metric_source.go 的 LatestProcessSample 用
	// `SUM(rss_bytes)` 求整棵树）。ProcessMetricSnapshot 是按**进程**一行的 TOPN 表
	// （含 pid 列，写入方逐 sm.Pid 一行），故：
	//   - `MAX(rss_bytes)` 得到的是「单进程峰值」，而 MemLimitMB 是**整机容器**上限，
	//     二者不同量纲；MC 场景下 JVM 主进程通常最大，MAX 多数时候碰巧接近真值；
	//   - 但心跳样本被裁为「每实例最多 10 个进程、按 CPU 排序」，主进程一旦 CPU 偏低
	//     就被挤出样本，MAX 系统性低报 → NearOOM（RSS >= 0.9×limit）漏判，
	//     而这恰恰是要查 OOM 时最不该给的错误结论。
	// 正确口径：先按 MAX(sampled_at) 定格**一拍**（心跳上报的多进程样本共享同一拍），
	// 再对该拍 `SUM(rss_bytes)` 得到「这一刻整棵树」的占用——与配额侧完全一致。
	// 不能对窗口内所有样本求和：那会把不同时刻的进程占用叠加成虚高值。
	var latest model.ProcessMetricSnapshot
	err := s.db.Select("sampled_at").
		Where("instance_uuid = ? AND sampled_at >= ? AND sampled_at <= ?", inst.UUID, from, to).
		Order("sampled_at DESC").First(&latest).Error
	switch {
	case err == nil:
		var agg struct {
			SumRSS uint64
		}
		if aerr := s.db.Model(&model.ProcessMetricSnapshot{}).
			Where("instance_uuid = ? AND sampled_at = ?", inst.UUID, latest.SampledAt).
			Select("COALESCE(SUM(rss_bytes), 0) as sum_rss").
			Scan(&agg).Error; aerr == nil && agg.SumRSS <= uint64(math.MaxInt64) {
			out.RSSAtCrash = int64(agg.SumRSS)
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 窗口内无样本：RSS 留 0，由下方 switch 给出「无样本」的口径。
	default:
		// 查询失败不阻断关联：RSS 留 0 + 降级说明。
		slog.Warn("崩溃关联：查询进程样本失败", "instanceId", instanceID, "error", err)
	}

	// 2. 堆已用峰值（ServerProbe inst_heap_used）。
	if s.metrics != nil {
		var node model.Node
		if err := s.db.Select("uuid").First(&node, inst.NodeID).Error; err == nil {
			if v, err := s.metrics.LatestValue(model.MetricScopeInstance, node.UUID, inst.UUID, model.MetricInstHeapUsed, from); err == nil && v != nil {
				out.HeapUsedMax = int64(*v)
			}
		}
	}

	// 3. 结论：RSS 逼近上限（>=90%）判 nearOOM；有堆数据时一并标注。
	// 与批量版共用 finalizeCorrelationNote，避免两处判定口径漂移。
	finalizeCorrelationNote(&out)
	// FR-465 依赖：GC 指标尚未采集入库（spec §5）。
	out.GCNote = "GC 关联待 FR-465 落地（serverprobe_gc_* 尚未入库）"
	return out, nil
}

// CorrelateInstanceBatch 一次算出同一实例多条崩溃时刻的关联证据（R14）。
//
// 为什么要批量：崩溃快照列表端点原先在循环里逐条调 CorrelateInstance，每条至少 2 次
// DB（实例行 + RSS 聚合），注入 metrics 后再加 1 次节点查询 + LatestValue——
// K=5 条即 10~20+ 次查询，而列表是控制台高频读接口。
//
// 批量口径：实例行与节点行各查 1 次；进程样本**一次查回窗口并集**后在内存里按时刻
// 归拍。RSS 口径与单条版严格一致：先取窗口内 `MAX(sampled_at)` 定格一拍，再对该拍
// `SUM(rss_bytes)`。注意**每条崩溃时刻各自一个窗口**（[t−5min, t]），故按并集取回
// 后逐条在内存里筛窗口，而不是对并集做一次聚合——后者会把不同崩溃的样本混在一起。
//
// occurredAts 为空时返回空映射；实例不存在返回 ErrInstanceNotFound。
func (s *CrashCorrelationService) CorrelateInstanceBatch(instanceID uint, occurredAts []time.Time) (map[int64]CrashCorrelation, error) {
	out := make(map[int64]CrashCorrelation, len(occurredAts))
	if len(occurredAts) == 0 {
		return out, nil
	}
	var inst model.Instance
	if err := s.db.Select("id", "uuid", "node_id", "mem_limit_mb").First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, err
	}

	// 归一化每个请求时刻的窗口（零值按「现在」处理，与单条版一致）。
	type win struct {
		key  int64
		from time.Time
		to   time.Time
	}
	wins := make([]win, 0, len(occurredAts))
	unionFrom, unionTo := time.Time{}, time.Time{}
	for _, at := range occurredAts {
		to := at
		if to.IsZero() {
			to = time.Now()
		}
		from := to.Add(-crashCorrelationWindow)
		wins = append(wins, win{key: to.UnixNano(), from: from, to: to})
		if unionFrom.IsZero() || from.Before(unionFrom) {
			unionFrom = from
		}
		if unionTo.IsZero() || to.After(unionTo) {
			unionTo = to
		}
	}

	// 一次取回窗口并集内的全部进程样本（含 pid 维度：SUM 在内存里按拍聚合）。
	var samples []model.ProcessMetricSnapshot
	if err := s.db.Select("pid", "rss_bytes", "sampled_at").
		Where("instance_uuid = ? AND sampled_at >= ? AND sampled_at <= ?", inst.UUID, unionFrom, unionTo).
		Find(&samples).Error; err != nil {
		slog.Warn("崩溃关联（批量）：查询进程样本失败", "instanceId", instanceID, "error", err)
		samples = nil
	}

	// 按拍聚合 RSS 总和，便于逐条窗口内取「最新一拍」。
	sumBySample := make(map[int64]uint64, len(samples))
	for i := range samples {
		sumBySample[samples[i].SampledAt.UnixNano()] += samples[i].RSSBytes
	}

	// 节点行一次（堆指标定位用）。
	var nodeUUID string
	if s.metrics != nil {
		var node model.Node
		if err := s.db.Select("uuid").First(&node, inst.NodeID).Error; err == nil {
			nodeUUID = node.UUID
		}
	}

	for _, w := range wins {
		c := CrashCorrelation{MemLimitMB: inst.MemLimitMB}
		// 该条窗口内最新的一拍（与单条版 MAX(sampled_at) 同口径）。
		var latestKey int64
		for i := range samples {
			ts := samples[i].SampledAt
			if ts.Before(w.from) || ts.After(w.to) {
				continue
			}
			if key := ts.UnixNano(); key > latestKey {
				latestKey = key
			}
		}
		if latestKey != 0 {
			if sum := sumBySample[latestKey]; sum <= uint64(math.MaxInt64) {
				c.RSSAtCrash = int64(sum)
			}
		}
		if s.metrics != nil && nodeUUID != "" {
			if v, err := s.metrics.LatestValue(model.MetricScopeInstance, nodeUUID, inst.UUID,
				model.MetricInstHeapUsed, w.from); err == nil && v != nil {
				c.HeapUsedMax = int64(*v)
			}
		}
		finalizeCorrelationNote(&c)
		out[w.key] = c
	}
	return out, nil
}

// finalizeCorrelationNote 按 RSS 与内存上限给出 NearOOM 判定与人可读结论。
//
// 单条版与批量版共用同一段判定，避免两处口径漂移（这正是 R6 的成因类型）。
func finalizeCorrelationNote(c *CrashCorrelation) {
	switch {
	case c.MemLimitMB > 0 && c.RSSAtCrash > 0:
		limitBytes := c.MemLimitMB * 1024 * 1024
		c.NearOOM = float64(c.RSSAtCrash) >= float64(limitBytes)*0.9
		if !c.NearOOM {
			c.Note = "崩前 RSS 未逼近内存上限"
		}
	case c.RSSAtCrash == 0:
		c.Note = "崩溃窗口内无进程资源样本"
	default:
		c.Note = "实例未设内存上限，无法判定 RSS 逼近度"
	}
}
