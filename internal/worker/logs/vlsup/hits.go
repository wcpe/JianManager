package vlsup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// hitsPath 是 VictoriaLogs 的「按时间桶统计命中数」端点。
//
// 选它而不是自己维护分区目录的理由（2026-11-05 架构审计 D1）：
// 「哪些源在哪些天有数据」是 VL 已经知道的**物理事实**，我们此前用 Catalog 副本
// 去近似它，副本一丢就要靠 journal 落盘 + 启动回填两层补救，两层又各自引入新问题。
// 一次 hits 请求（实测 372 个 (源,天) 分区 / 217ms / 31KB）即可拿到完整事实。
const hitsPath = "/select/logsql/hits"

// hitsResponse 是 /select/logsql/hits 的返回结构。
//
// 形态（实测 v1.52.0）：
//
//	{"hits":[{"fields":{"log_source_id":"inst:317/file"},
//	          "timestamps":["2026-10-01T00:00:00Z", ...],
//	          "values":[0, 0, ..., 243, 0],
//	          "total":243}, ...]}
//
// timestamps 与 values 等长且同序；values 是**权重值不是精确条数**
// （VL 内部用加权计数，0 才是「真的没有」这个判据可靠）。
type hitsResponse struct {
	Hits []struct {
		Fields     map[string]string `json:"fields"`
		Timestamps []string          `json:"timestamps"`
		Values     []uint64          `json:"values"`
		Total      uint64            `json:"total"`
	} `json:"hits"`
}

// SourceDay 是一个「源在某天有数据」的事实（不是权威，权威仍由 Catalog 决定）。
type SourceDay struct {
	// LogSourceID 是**完整源 id**（`inst:318/file`），来自 hits 的 field 分组值。
	LogSourceID string
	// UTCDay 是 `2006-01-02` 形式的 UTC 日。
	UTCDay string
	// Weight 是 VL 给的权重值（>0 即「有数据」）；不用于条数展示。
	Weight uint64
}

// HitsBySourceDay 一次查询拿回「(源, 天) 有数据」的完整集合。
//
// # 天界口径（改动前必读）
//
// **不传 `offset` 参数。** 我们的分区键是 **UTC 日**，而 hits 的 `offset` 会平移桶边界
// （实测 `offset=8h` 让桶起点从 `T00:00Z` 变成 `T16:00Z`，即 HKT 当地日）——
// 一旦传错，整张分区表会与既有 UTC 分区错开一天，正是历史上三次「天界」事故的根因。
//
// # 为什么 window 由调用方给
//
// 回溯窗口是策略（默认 30 天，可配），不该硬编码在 VL 适配层。
// 上限受 VL 的 `-search.maxQueryTimeRange` 约束（本机 4344h ≈ 181 天）。
//
// # 为什么 field 只能是单字段
//
// 实测：传 `field=log_source_id,level` 会被当**字面量**分组
// （返回 `fields` 为 `{"log_source_id,level":""}`）。需要多维度时调多次。
func (c *Client) HitsBySourceDay(ctx context.Context, start, end time.Time) ([]SourceDay, error) {
	if c == nil {
		return nil, fmt.Errorf("vlsup: nil client")
	}
	if !start.Before(end) {
		return nil, fmt.Errorf("vlsup: hits 时间窗无效 [%s, %s)", start, end)
	}
	q := url.Values{}
	q.Set("query", "*")
	q.Set("field", "log_source_id")
	q.Set("step", "1d")
	q.Set("start", start.UTC().Format(time.RFC3339Nano))
	q.Set("end", end.UTC().Format(time.RFC3339Nano))
	// 显式声明不传 offset：见函数头「天界口径」。
	body, err := c.Post(ctx, hitsPath, q)
	if err != nil {
		return nil, err
	}
	return parseHitsSourceDays(body)
}

// parseHitsSourceDays 把 hits 响应摊平成 (源, 天) 列表，只保留 weight>0 的桶。
//
// 抽成独立函数便于单测：天界解析与排序都容易写错，且一旦错了表现是「查不到数据」
// 而不是报错——必须能被用例直接钉住。
func parseHitsSourceDays(body []byte) ([]SourceDay, error) {
	var parsed hitsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("vlsup: hits 响应解析失败: %w", err)
	}
	out := make([]SourceDay, 0, len(parsed.Hits)*8)
	for _, series := range parsed.Hits {
		source := strings.TrimSpace(series.Fields["log_source_id"])
		if source == "" {
			// 无分组值 = 该 series 是聚合总数（未按 source 分组），对分区目录无用。
			continue
		}
		// timestamps 与 values 理论等长；不等长时按较短者处理，宁少不越界。
		n := len(series.Timestamps)
		if len(series.Values) < n {
			n = len(series.Values)
		}
		for i := 0; i < n; i++ {
			if series.Values[i] == 0 {
				// 0 表示该桶确实没有数据。这是本判据唯一可靠的否定证据。
				continue
			}
			day, err := utcDayOfBucketStart(series.Timestamps[i])
			if err != nil {
				// 单个桶时间解析失败不该让整次重建失败（那会让分区目录整体为空，
				// 比少一天严重得多）；跳过并由调用方从日志里看到计数差异。
				continue
			}
			out = append(out, SourceDay{LogSourceID: source, UTCDay: day, Weight: series.Values[i]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LogSourceID != out[j].LogSourceID {
			return out[i].LogSourceID < out[j].LogSourceID
		}
		return out[i].UTCDay < out[j].UTCDay
	})
	return out, nil
}

// utcDayOfBucketStart 把桶起点时间戳换算成 UTC 日（`2006-01-02`）。
//
// **必须按 UTC 取日**，不能按本地时区：分区键是 UTC 日，而本机是 HKT(+8)，
// 用 Local 会让每天整体错位 8 小时、跨零点的那批数据归错天。
func utcDayOfBucketStart(stamp string) (string, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if ts, err := time.Parse(layout, stamp); err == nil {
			return ts.UTC().Format("2006-01-02"), nil
		}
	}
	return "", fmt.Errorf("vlsup: 无法解析 hits 桶时间 %q", stamp)
}

// fieldNamesPath 是「该时间窗内出现过的字段名全集」端点。
const fieldNamesPath = "/select/logsql/field_names"

// fieldNamesResponse 是 /select/logsql/field_names 的返回结构。
//
// 形态（实测 v1.52.0）：
//
//	{"values":[{"value":"_msg","hits":5276},{"value":"event_id","hits":5276}, ...]}
type fieldNamesResponse struct {
	Values []struct {
		Value string `json:"value"`
		Hits  uint64 `json:"hits"`
	} `json:"values"`
}

// FieldNames 返回给定查询与时间窗内**出现过的全部字段名**。
//
// # filter 与 query 的区别（2026-11-06 审查修复）
//
// 两者在 VL 里是两个独立参数，缺一不可：
//   - `query` 是 LogsQL 主查询；
//   - `extra_filters` 是附加上滤条件（VL 会 AND 上去）。
//
// 原实现只接 `query`，而在 vlrange 侧作用域约束是拼进 `query` 的，
// 而**用户的结构化筛选项**（level / keyword / source）走的是 `filter`
// ⇒ 原实现把它们全丢了：用户选 `level=ERROR` 后看字段列表，拿到的是全量字段集合。
//
// # 为什么需要它（2026-11-05 架构审计 D3）
//
// 旧实现（`vlrange.Client.Fields`）是靠 `Search` 拿一页日志、再遍历返回行的
// `Fields` map 去重。那只能看到**本页那几十条**日志恰好带的字段——不是该时间窗的
// 字段全集。实测同一窗口：旧实现返回 `[level, stream]` 两项，而 VL 里实际有 15+ 个。
//
// 官方端点是专门为这个问题设计的，一次聚合即得全集。
//
// 返回的字段名按 VL 给的顺序（通常是 hits 降序），调用方可自行排序。
func (c *Client) FieldNames(ctx context.Context, query, filter string, start, end time.Time) ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("vlsup: nil client")
	}
	if !start.Before(end) {
		return nil, fmt.Errorf("vlsup: field_names 时间窗无效 [%s, %s)", start, end)
	}
	params := url.Values{}
	if query == "" {
		query = "*"
	}
	params.Set("query", query)
	params.Set("start", start.UTC().Format(time.RFC3339Nano))
	params.Set("end", end.UTC().Format(time.RFC3339Nano))
	if filter != "" {
		params.Set("extra_filters", filter)
	}
	body, err := c.Post(ctx, fieldNamesPath, params)
	if err != nil {
		return nil, err
	}
	var parsed fieldNamesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("vlsup: field_names 响应解析失败: %w", err)
	}
	out := make([]string, 0, len(parsed.Values))
	for _, v := range parsed.Values {
		if name := strings.TrimSpace(v.Value); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// fieldValuesPath 是「某个字段的值分布」端点。
const fieldValuesPath = "/select/logsql/field_values"

// fieldValuesResponse 是 /select/logsql/field_values 的返回结构。
//
// 形态（实测 v1.52.0）：
//
//	{"values":[{"value":"INFO","hits":880},{"value":"WARN","hits":10}]}
type fieldValuesResponse struct {
	Values []struct {
		Value string `json:"value"`
		Hits  uint64 `json:"hits"`
	} `json:"values"`
}

// FieldValue 是一个字段值的聚合结果。
type FieldValue struct {
	Value string
	Hits  uint64
}

// FieldValues 返回某字段在给定查询与时间窗内的值分布（按 hits 降序，由 VL 给）。
//
// # 为什么用它而不是 /select/logsql/facets
//
// 2026-11-05 实测：官方 `facets` 的语义是「**自动发现**低基数字段」——
// 它扫描所有字段、返回「值数量 ≤ max_values_per_field 的那些字段」，
// 而 `field=` / `fields=` 参数都会被忽略。
//
// 我们的查询契约是「按调用方指定的 dimensions 聚合」，与它不匹配。
// `field_values` 才是「按指定字段精确聚合」，与服务契约同形。
//
// 实测两者对 level 的结果逐值一致（INFO 880 / WARN 10）。
//
// filter 与 query 的分工见 `FieldNames`——两个参数都必须下发。
func (c *Client) FieldValues(ctx context.Context, query, filter, field string, start, end time.Time) ([]FieldValue, error) {
	if c == nil {
		return nil, fmt.Errorf("vlsup: nil client")
	}
	if strings.TrimSpace(field) == "" {
		return nil, fmt.Errorf("vlsup: field_values 需要非空 field")
	}
	if !start.Before(end) {
		return nil, fmt.Errorf("vlsup: field_values 时间窗无效 [%s, %s)", start, end)
	}
	params := url.Values{}
	if query == "" {
		query = "*"
	}
	params.Set("query", query)
	params.Set("field", field)
	params.Set("start", start.UTC().Format(time.RFC3339Nano))
	params.Set("end", end.UTC().Format(time.RFC3339Nano))
	if filter != "" {
		params.Set("extra_filters", filter)
	}
	body, err := c.Post(ctx, fieldValuesPath, params)
	if err != nil {
		return nil, err
	}
	var parsed fieldValuesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("vlsup: field_values 响应解析失败: %w", err)
	}
	out := make([]FieldValue, 0, len(parsed.Values))
	for _, v := range parsed.Values {
		out = append(out, FieldValue{Value: v.Value, Hits: v.Hits})
	}
	return out, nil
}
