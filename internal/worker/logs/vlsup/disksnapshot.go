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
// ## 为什么 UsedBytes 自己算目录大小，而不是用 total-free
//
// `vl_free_disk_space_bytes` / `vl_total_disk_space_bytes` 是**整块磁盘**的可用/总量
// （带 `path` 标签但语义是文件系统维度）。拿 total-free 当「VL 占了多少」是错的：
// 生产环境 2026-10-29 实测得到 903 GiB，而 VL 目录实际只有 22.3 GiB ——
// 多出的 880 GiB 是游戏服、数据库和其它一切。用它除 100GiB 上限会得出
// `disk_pct = 903%` 这种荒谬读数，告警立刻误报。
//
// VL **没有**一个“目录占用”的直接指标（已核 v1.52.0 的 /metrics）。
// 所以自己 walk `-storageDataPath`；这与 VL 自己判断磁盘上限的口径一致
// （官方 help："total size of data at -storageDataPath directory"）。
//
// 代价：周期性 walk 一个有界目录树。心跳 30s 一次、目录通常几千个文件，
// 实测毫秒级；若将来目录大到影响心跳，再加缓存。

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// VL 指标名（v1.52.0 实测确认）。
const (
	vlMetricPartitions  = "vl_partitions"
	vlMetricRowsDropped = "vl_rows_dropped_total"

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
	used := dirSizeBytes(inst.cfg.StorageDataPath)
	return VLDiskSnapshotResult{
		UsedBytes:        used,
		LimitBytes:       inst.cfg.MaxDiskSpaceUsageBytes,
		Partitions:       int32(g.scalar(vlMetricPartitions)),
		RowsDroppedTotal: uint64(g.withLabel(vlMetricRowsDropped, "reason", vlDropReasonTooSmall)),
	}
}

// dirSizeBytes 递归统计目录下所有常规文件的大小之和（与 VL 判上限的口径一致）。
//
// 单个文件 stat 失败不影响其它文件（少算一点点，宁可低估也不误报）；
// 目录不存在返回 0。不跟随符号链接（避免环）。
func dirSizeBytes(root string) int64 {
	if root == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单个条目读不到不影响汇总
		}
		if d.IsDir() {
			return nil
		}
		if info, statErr := d.Info(); statErr == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
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
