package logcoord

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ParseBucketDuration 解析时间桶宽度（绝对 UTC 对齐用）。
// 支持 Go duration 后缀：ns/us/ms/s/m/h，以及 d（天）。
func ParseBucketDuration(spec string) (time.Duration, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, fmt.Errorf("logcoord: empty time bucket")
	}
	if strings.HasSuffix(spec, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(spec, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("logcoord: invalid time bucket %q", spec)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(spec)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("logcoord: invalid time bucket %q", spec)
	}
	return d, nil
}

// BucketStartUTC 将事件时间对齐到绝对 UTC 桶起点。
//
// 对齐锚点为 Unix epoch（UTC），保证同一 bucket 跨 Worker/跨天边界一致。
func BucketStartUTC(t time.Time, bucket time.Duration) time.Time {
	if bucket <= 0 {
		return t.UTC().Truncate(time.Second)
	}
	unix := t.UTC().UnixNano()
	size := int64(bucket)
	start := unix - (unix % size)
	if unix < 0 && unix%size != 0 {
		// 负时间对齐（历史极早数据）。
		start -= size
	}
	return time.Unix(0, start).UTC()
}

// BucketStartRFC3339 便捷包装。
func BucketStartRFC3339(eventTimeUTC, bucketSpec string) (string, error) {
	d, err := ParseBucketDuration(bucketSpec)
	if err != nil {
		return "", err
	}
	t := MustParseRFC3339(eventTimeUTC)
	if t.IsZero() {
		return "", fmt.Errorf("logcoord: invalid event_time %q", eventTimeUTC)
	}
	return BucketStartUTC(t, d).Format(time.RFC3339), nil
}

// dimsKey 生成维度字典的稳定键。
func dimsKey(dims map[string]string, timeBucketUTC string) string {
	keys := make([]string, 0, len(dims))
	for k := range dims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("tb=")
	b.WriteString(timeBucketUTC)
	for _, k := range keys {
		b.WriteByte('|')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(dims[k])
	}
	return b.String()
}

// MergeStatsPoints 可合并聚合二次合并。
//
// 口径：count/sum 直接相加；min/max 取边界；avg 不参与合并，
// 由调用方在最终点上以 sum/count 计算（加权平均）。
// 时间桶键使用绝对 UTC 桶起点字符串。
func MergeStatsPoints(points []StatsPoint) []StatsPoint {
	order := make([]string, 0, len(points))
	byKey := make(map[string]*StatsPoint, len(points))
	for i := range points {
		p := points[i]
		key := dimsKey(p.Dimensions, p.TimeBucketUTC)
		if existing, ok := byKey[key]; ok {
			existing.Agg.Merge(p.Agg)
			continue
		}
		cp := StatsPoint{
			Dimensions:    copyDims(p.Dimensions),
			TimeBucketUTC: p.TimeBucketUTC,
			Agg:           p.Agg,
		}
		byKey[key] = &cp
		order = append(order, key)
	}
	out := make([]StatsPoint, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	// 确定性排序：时间桶升序，再维度键升序。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TimeBucketUTC != out[j].TimeBucketUTC {
			return out[i].TimeBucketUTC < out[j].TimeBucketUTC
		}
		return dimsKey(out[i].Dimensions, "") < dimsKey(out[j].Dimensions, "")
	})
	return out
}

func copyDims(d map[string]string) map[string]string {
	if d == nil {
		return nil
	}
	out := make(map[string]string, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

// WorkerPointsToStats 将单 Worker 点转为可合并 StatsPoint。
func WorkerPointsToStats(pts []WorkerStatsPoint) []StatsPoint {
	out := make([]StatsPoint, 0, len(pts))
	for _, p := range pts {
		out = append(out, StatsPoint{
			Dimensions:    copyDims(p.Dimensions),
			TimeBucketUTC: p.TimeBucketUTC,
			Agg:           p.Agg,
		})
	}
	return out
}
