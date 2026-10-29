package vlsup

// 日志存储层（VL）磁盘占用读数（2026-10-29）。
//
// ## 为什么需要它
//
// 配了 `-retention.maxDiskSpaceUsageBytes` / `-retention.maxDiskUsagePercent` 后，
// VL 会在超限时**丢弃最旧的按日分区**。官方文档写明了两条硬约束：
//
//  1. VL **至少保留最近 2 天** —— "the total disk space usage may exceed the configured
//     threshold if the size of the last two days of data exceeds the limit"；
//  2. 磁盘占用检查是**周期性**的 —— "Disk usage can go over the limits between two checks.
//     The disk could reach 100% usage… In this case, VictoriaLogs switches to read-only
//     mode and **cannot drop data as expected**"。
//
// 第 2 条是真正的危险点：磁盘写满后 VL **进入只读、反而无力丢弃**（丢弃也要写元数据）。
// 所以"设了上限"不等于"磁盘不可能写满"，必须把占用读数带到告警链路上。
//
// ## 为什么不自己 du 数据目录
//
// VL 自己已经把这些数值算好了且是**权威口径**（它知道哪些是活跃 part、有多少临时文件）。
// `du` 会得到另一个数，而告警要对齐的是 VL 的判据，不是我们的猜测。
//
// ## 指标名是实测确认的，不是猜的
//
// 2026-10-29 用 v1.52.0 实跑 `GET /metrics` 确认存在：
//
//	vl_free_disk_space_bytes{path="…"}
//	vl_total_disk_space_bytes{path="…"}
//	vl_partitions
//	vl_rows_dropped_total{reason="too_small_timestamp"}   ← 超保留期被丢
//	vl_rows_dropped_total{reason="too_big_timestamp"}     ← 超未来窗口
//	vl_rows_dropped_total{reason="debug"}                 ← 调试日志丢弃
//	vl_rows_dropped_total{reason="too_many_fields"}       ← 字段过多
//
// **必须按 reason 取值**：把四个 reason 加总会把「正常丢弃的 debug 日志」也算成数据丢失。

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// VL 指标名（v1.52.0 实测确认）。
const (
	vlMetricFreeDiskSpace  = "vl_free_disk_space_bytes"
	vlMetricTotalDiskSpace = "vl_total_disk_space_bytes"
	vlMetricPartitions     = "vl_partitions"
	vlMetricRowsDropped    = "vl_rows_dropped_total"

	// vlDropReasonTooSmall 是「事件时间早于 now-retentionPeriod ⇒ ingest 阶段直接丢」。
	// 这是唯一与「保留期/磁盘上限导致的历史数据消失」对应的 reason。
	//
	// 为什么只取它：官方文档对该 reason 有明确保证（"VictoriaLogs automatically drops
	// logs at data ingestion stage if they have timestamps outside the configured
	// retention… vl_rows_dropped_total is incremented"）。另外三个 reason 与保留无关：
	//   - debug：VL 自身调试日志；
	//   - too_many_fields：字段数超限（配置/数据形态问题）；
	//   - too_big_timestamp：超出 now+futureRetention（时钟错乱或伪造时间）。
	vlDropReasonTooSmall = "too_small_timestamp"
)

// vlMetricsTimeout 是抓一次 /metrics 的超时。
//
// 为什么这么短：本机 loopback，正常毫秒级；抓不到就不该拖住心跳（节拍 30s，
// 单拍已有其它采集预算）。拿不到返回零值 ⇒ CP 按「无读数」处理。
const vlMetricsTimeout = 2 * time.Second

// VLDiskSnapshotResult 是 VLDiskSnapshot 的返回形态。
//
// 与 heartbeat.VLDiskReading 字段一致但是独立类型：vlsup 不 import heartbeat
// （heartbeat 已经 import ingest/metrics，反向依赖会成环）。装配层做一次转换。
type VLDiskSnapshotResult struct {
	UsedBytes        int64
	LimitBytes       int64
	Partitions       int32
	RowsDroppedTotal uint64
}

// VLDiskSnapshot 采集一次 VL 磁盘占用读数。
//
// 未启用受管 VL（没有运行中实例）时返回零值 —— 调用方据此认为「无读数」而非「0 字节」。
func (s *Supervisor) VLDiskSnapshot() VLDiskSnapshotResult {
	if s == nil {
		return VLDiskSnapshotResult{}
	}
	s.mu.Lock()
	inst, ok := s.instances[NamespaceHot]
	s.mu.Unlock()
	if !ok || inst == nil {
		return VLDiskSnapshotResult{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), vlMetricsTimeout)
	defer cancel()

	body, err := s.fetchMetrics(ctx, inst.cfg.Port)
	if err != nil {
		// 不把抓取失败当「0 字节」——那会让告警误报「磁盘空了」。
		// 返回零值 = 无读数；调用方不会据此产生任何派生指标。
		return VLDiskSnapshotResult{}
	}

	g := parsePrometheus(body)
	total := g.anyLabel(vlMetricTotalDiskSpace)
	free := g.anyLabel(vlMetricFreeDiskSpace)
	used := int64(0)
	if total > 0 && free >= 0 && free <= total {
		used = int64(total - free)
	}
	return VLDiskSnapshotResult{
		UsedBytes:        used,
		LimitBytes:       inst.cfg.MaxDiskSpaceUsageBytes,
		Partitions:       int32(g.scalar(vlMetricPartitions)),
		RowsDroppedTotal: uint64(g.withLabel(vlMetricRowsDropped, "reason", vlDropReasonTooSmall)),
	}
}

// fetchMetrics 抓取本机 VL 的 /metrics（带 Basic Auth）。
func (s *Supervisor) fetchMetrics(ctx context.Context, port int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ListenAddr(port)+"/metrics", nil)
	if err != nil {
		return "", err
	}
	if s.authUser != "" || s.authPass != "" {
		req.SetBasicAuth(s.authUser, s.authPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", &metricsStatusError{status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type metricsStatusError struct{ status int }

func (e *metricsStatusError) Error() string {
	return "vlsup: /metrics status " + strconv.Itoa(e.status)
}

// prometheusGauges 是 /metrics 文本里我们关心的那几行的解析结果。
//
// 为什么不引第三方 Prometheus 客户端：只需要读 4 个 gauge，而引入一个依赖
// （及其版本/供应链成本）远超收益。解析规则也只覆盖 VL 实际输出的形态。
type prometheusGauges struct {
	// scalars 存无标签指标（vl_partitions 等）。
	scalars map[string]float64
	// labeled 存 labelName=labelValue → 指标名 → 值（vl_rows_dropped_total{reason=…}）。
	labeled map[string]map[string]float64
}

// scalar 取一个**无标签** gauge 的值；缺失返回 0。
func (g prometheusGauges) scalar(name string) float64 { return g.scalars[name] }

// anyLabel 取 name 在**任意标签组合**下的值（单实例受管 VL 只有一个存储路径）。
//
// 为什么需要它：`vl_free_disk_space_bytes` / `vl_total_disk_space_bytes` 实际是**带标签**的
// （`{path="…"}`，2026-10-29 实跑确认）——只用 scalar() 会恒得 0，
// 而那会让盘上告警永远不触发（本文件的用例当场抓到了这一点）。
//
// 单实例受管 VL 只有一个 storageDataPath，所以取到哪个都行；
// 若将来多路径共存，应改成按具体 path 精确匹配（而不是现在这样取首个）。
func (g prometheusGauges) anyLabel(name string) float64 {
	if v, ok := g.scalars[name]; ok {
		return v
	}
	for _, metrics := range g.labeled {
		if v, ok := metrics[name]; ok {
			return v
		}
	}
	return 0
}

// withLabel 取 name{labelName="labelValue"} 的值；缺失返回 0。
//
// 缺失返回 0 在这里是安全的：CP 用相邻心跳的差值算速率，缺一次只会让那一段
// 差分为 0，不会产生虚假增量。
func (g prometheusGauges) withLabel(name, labelName, labelValue string) float64 {
	if m, ok := g.labeled[labelName+"="+labelValue]; ok {
		return m[name]
	}
	return 0
}

// parsePrometheus 解析 Prometheus 文本格式里我们关心的小部分：
// `name value` 与 `name{label="value",...} value`。
//
// 严格忽略：注释（# 开头）、空行、无法解析的行。宁可少读也不错读一个数
// ——告警面宁缺毋滥，读错会直接变成误报。
func parsePrometheus(body string) prometheusGauges {
	out := prometheusGauges{
		scalars: make(map[string]float64, 8),
		labeled: make(map[string]map[string]float64, 8),
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := splitMetricName(line)
		if !ok {
			continue
		}
		valueText := rest
		if labels, tail, hasLabels := splitLabels(rest); hasLabels {
			valueText = tail
			v, err := strconv.ParseFloat(valueText, 64)
			if err != nil {
				continue
			}
			for labelName, labelValue := range labels {
				key := labelName + "=" + labelValue
				if out.labeled[key] == nil {
					out.labeled[key] = make(map[string]float64, 2)
				}
				out.labeled[key][name] = v
			}
			continue
		}
		if v, err := strconv.ParseFloat(valueText, 64); err == nil {
			out.scalars[name] = v
		}
	}
	return out
}

// splitMetricName 切出指标名与其后的剩余文本。
func splitMetricName(line string) (name, rest string, ok bool) {
	if i := strings.IndexAny(line, "{ \t"); i > 0 {
		return line[:i], strings.TrimSpace(line[i:]), true
	}
	return "", "", false
}

// splitLabels 解析 `{a="b",c="d"} value` 形态，返回标签集合与值文本。
func splitLabels(rest string) (labels map[string]string, valueText string, ok bool) {
	if !strings.HasPrefix(rest, "{") {
		return nil, "", false
	}
	end := strings.LastIndex(rest, "}")
	if end < 0 {
		return nil, "", false
	}
	labels = make(map[string]string, 2)
	for _, pair := range strings.Split(rest[1:end], ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.Index(pair, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.Trim(strings.TrimSpace(pair[eq+1:]), `"`)
		if key != "" {
			labels[key] = val
		}
	}
	return labels, strings.TrimSpace(rest[end+1:]), true
}
