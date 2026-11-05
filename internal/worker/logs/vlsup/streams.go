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

// streamsPath 是 VictoriaLogs 的「按 stream 列出窗内条数」端点。
//
// 它和 `field_names`（文档字段全集）不是同一件事：stream 是 VL 在**入库时**
// 按流标签集合建立的索引，我们的行上流标签只有 `log_source_id` 与 `stream`
// （实测 `stream_field_names` 恒为这两项）。所以本端点的粒度是「(源, 流) 二元组」。
const streamsPath = "/select/logsql/streams"

// streamFieldNamesPath 是「stream 的标签维度名」端点。
const streamFieldNamesPath = "/select/logsql/stream_field_names"

// streamFieldValuesPath 是「stream 某标签维度的取值分布」端点。
const streamFieldValuesPath = "/select/logsql/stream_field_values"

// streamsResponse 是 /select/logsql/streams 的返回结构。
//
// 形态（实测 v1.52.0）：
//
//	{"values":[{"value":"{log_source_id=\"inst:318/file\",stream=\"stdout\"}","hits":7378}, ...]}
//
// # hits 语义分层（**必读**，2026-11-05 实测）
//
//   - 响应未达上限：hits = 该 stream 在窗内的**精确条数**
//     （实证：inst:318/file stream 在 11-01~11-06 窗 = 7379，与 field_values 同窗同一个值）；
//   - 响应达上限被截断：**所有** hits 一律变 0——不是少返回几条，是全零。
//
// 后者是本接口最大的坑：用 `limit` 参数触发时表现是「21 个流全是 0 条」，
// 用户会以为窗内没数据。因此本客户端**从不传 limit**，见 Streams 的注释。
type streamsResponse struct {
	Values []struct {
		Value string `json:"value"`
		Hits  uint64 `json:"hits"`
	} `json:"values"`
}

// Stream 是一条流与其在窗内的条数。
type Stream struct {
	// Raw 是 VL 原始串 `{log_source_id="...",stream="..."}`，无损保留。
	Raw string
	// LogSourceID 与 StreamName 从 Raw 拆出；拆不出时留空（不猜测）。
	LogSourceID string
	StreamName  string
	// Hits 在 Complete=true 时是精确条数；Complete=false 时不可信（不得当「0 条」）。
	Hits uint64
}

// Streams 返回窗内的 stream 集合与条数。
//
// # 为什么不传 `limit`（重要）
//
// 实测 `streams` 端点的 `limit` 一旦小于实际 stream 数，会把**所有** hits 静默归零：
//
//	limit=20（真实 21 个流） -> 20 项, sum_hits=0
//	limit=21                -> 21 项, sum_hits=11784
//
// 它不是「截断返回」而是「全零」，所以任何小于全集的上限都会把整张表变成噪声。
// 流数量本身受限于源数量（生产 21 个量级），全量取回没有成本问题。
//
// # 返回的 Complete 是什么
//
// 只有一种情形判 `Complete=false`：**所有** hits 都是 0 但返回项数 > 0。
// 那正是「被上游截断」的形态（真实空窗会返回零项，不是 n 项全零）。
// 调用方见 Complete=false 时不得把 Hits=0 当成断言。
//
// # 为什么必须传时间窗
//
// 不传 start/end 时 VL 默认全时段，直接报
// `too big time range selected ... maxQueryTimeRange=4344h`。
//
// # 天界红线
//
// **不传 `offset`**：它是时区平移（会移动桶/统计边界），而我们的分区键是 UTC 日。
// 实测 offset 对 streams 的总量无影响，但为了与 hits 保持同一口径，一律不用。
func (c *Client) Streams(ctx context.Context, query string, start, end time.Time) ([]Stream, bool, error) {
	if c == nil {
		return nil, false, fmt.Errorf("vlsup: nil client")
	}
	if !start.Before(end) {
		return nil, false, fmt.Errorf("vlsup: streams 时间窗无效 [%s, %s)", start, end)
	}
	params := url.Values{}
	if query == "" {
		query = "*"
	}
	params.Set("query", query)
	params.Set("start", start.UTC().Format(time.RFC3339Nano))
	params.Set("end", end.UTC().Format(time.RFC3339Nano))
	// 显式不传 limit / offset：见函数头两节。
	body, err := c.Post(ctx, streamsPath, params)
	if err != nil {
		return nil, false, err
	}
	return parseStreams(body)
}

// parseStreams 解析 streams 响应并判定 complete。
//
// 抽成独立函数便于单测：「全零即截断」这个判据错了的表现是「用户看到窗内没数据」，
// 不报错——必须能被用例直接钉住。
func parseStreams(body []byte) ([]Stream, bool, error) {
	var parsed streamsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false, fmt.Errorf("vlsup: streams 响应解析失败: %w", err)
	}
	out := make([]Stream, 0, len(parsed.Values))
	anyHits := false
	for _, v := range parsed.Values {
		raw := strings.TrimSpace(v.Value)
		if raw == "" {
			continue
		}
		if v.Hits > 0 {
			anyHits = true
		}
		labels := parseStreamLabels(raw)
		out = append(out, Stream{
			Raw:         raw,
			LogSourceID: labels["log_source_id"],
			StreamName:  labels["stream"],
			Hits:        v.Hits,
		})
	}
	// 条数降序、同数按原始串升序：前端直接渲染即是有意义的顺序，不必自己再排。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].Raw < out[j].Raw
	})
	// 「有项但全零」= 上游截断。真实空窗返回零项（见 parseStreams 的用例）。
	return out, anyHits || len(out) == 0, nil
}

// parseStreamLabels 把 `{k="v",k2="v2"}` 拆成 map。
//
// 只做**最小可靠解析**：跳过空项，按第一个 `=` 切分，剥掉两侧引号。
// 值里若含逗号会被切碎——但流标签值（源 id / 流名）不含逗号，
// 而 Raw 已被无损保留，真出现异常形态时调用方仍能看到原串。
func parseStreamLabels(raw string) map[string]string {
	out := make(map[string]string, 4)
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"")
		if key != "" {
			out[key] = value
		}
	}
	return out
}

// StreamFieldNames 返回 stream 的标签维度名。
//
// 实测恒为 [`log_source_id`, `stream`]（流的标签就这两个），
// 但**不在客户端写死**：将来 VL 给流加了新标签，这里自动跟随。
//
// 与 `FieldNames`（文档字段全集）的区别见 streamsPath 注释。
func (c *Client) StreamFieldNames(ctx context.Context, query string, start, end time.Time) ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("vlsup: nil client")
	}
	if !start.Before(end) {
		return nil, fmt.Errorf("vlsup: stream_field_names 时间窗无效 [%s, %s)", start, end)
	}
	params := url.Values{}
	if query == "" {
		query = "*"
	}
	params.Set("query", query)
	params.Set("start", start.UTC().Format(time.RFC3339Nano))
	params.Set("end", end.UTC().Format(time.RFC3339Nano))
	body, err := c.Post(ctx, streamFieldNamesPath, params)
	if err != nil {
		return nil, err
	}
	var parsed fieldNamesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("vlsup: stream_field_names 响应解析失败: %w", err)
	}
	out := make([]string, 0, len(parsed.Values))
	for _, v := range parsed.Values {
		if name := strings.TrimSpace(v.Value); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// StreamFieldValues 返回某个 stream 标签维度的取值分布。
//
// 只对 stream 标签维度（`log_source_id` / `stream`）有值；传文档字段（如 `level`）
// 返回空 `values` 而非报错——这是 VL 的行为，此处不对它做二次猜测（
// 「非法字段」与「合法但空」在 VL 层不可区分，硬造一个判据只会引入假错误）。
//
// 返回的 Complete 与 Streams 同口径：有项但全零 ⇒ 上游截断。
func (c *Client) StreamFieldValues(ctx context.Context, query, field string, start, end time.Time) ([]FieldValue, bool, error) {
	if c == nil {
		return nil, false, fmt.Errorf("vlsup: nil client")
	}
	if strings.TrimSpace(field) == "" {
		return nil, false, fmt.Errorf("vlsup: stream_field_values 需要非空 field")
	}
	if !start.Before(end) {
		return nil, false, fmt.Errorf("vlsup: stream_field_values 时间窗无效 [%s, %s)", start, end)
	}
	params := url.Values{}
	if query == "" {
		query = "*"
	}
	params.Set("query", query)
	params.Set("field", field)
	params.Set("start", start.UTC().Format(time.RFC3339Nano))
	params.Set("end", end.UTC().Format(time.RFC3339Nano))
	body, err := c.Post(ctx, streamFieldValuesPath, params)
	if err != nil {
		return nil, false, err
	}
	var parsed fieldValuesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false, fmt.Errorf("vlsup: stream_field_values 响应解析失败: %w", err)
	}
	out := make([]FieldValue, 0, len(parsed.Values))
	anyHits := false
	for _, v := range parsed.Values {
		if v.Hits > 0 {
			anyHits = true
		}
		out = append(out, FieldValue{Value: v.Value, Hits: v.Hits})
	}
	return out, anyHits || len(out) == 0, nil
}
