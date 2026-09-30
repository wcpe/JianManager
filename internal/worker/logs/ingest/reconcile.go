package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 启动增量对账（FR-497，spec：docs/specs/log-startup-reconcile/spec.md）。
//
// 背景：重启恢复原先无条件「整窗重发」——把该源全部事件重新写一遍 VL、逐条校验、发布新代。
// 生产实测 13 源 ≈5 分钟，按 60 实例外推将达十几至几十分钟。而绝大多数重启时 VL 数据完好，
// 重发只是纯开销。
//
// 本文件按「源 × UTC 天」做**条数级**对账：账本侧应发条数（canonical 权威集合按 UTC 天分组）
// 对比 VL 侧实有条数（只读 `| stats count()` 查询，选择器与查询侧同口径）。两侧一致 → 零重发；
// 某天不足 → 只重发该天；查询失败/超时/不可解析 → **回退整窗重发**。
//
// 口径（用户拍板）：**绝不丢、允许极少重复**——一切不确定都朝「多写一次」的方向收敛，
// 绝不朝「少写一次」的方向收敛。

const (
	// reconcileStatsPath 是 VictoriaLogs 的只读 stats 查询端点（`| stats count()`）。
	reconcileStatsPath = "/select/logsql/stats_query"
	// reconcileBasisCountOnly 是对账判定的依据标记：显式表达「本次只做了条数级比较，
	// 未做内容级校验」，避免结论被误读为内容级确认（spec §2.4）。
	reconcileBasisCountOnly = "count_only"

	reconcileDefaultConcurrency  = 4
	reconcileMaxConcurrency      = 32
	reconcileDefaultTimeout      = 30 * time.Second
	reconcileDefaultQueryTimeout = 10 * time.Second
)

// ReconcileConfig 是启动增量对账的配置面。
//
// 配置键（worker.yml）登记为 `log_reconcile.*`（见 spec §5）。当前生效路径是
// Options.Reconcile（包内注入 + 默认值）：YAML → Options 的接线需改
// internal/worker/config.go 与 apps/worker/main.go，超出本 FR 的改动面。
type ReconcileConfig struct {
	// Enabled 为 false 时不做对账，直接走现状整窗重发（应急逃生口）。
	Enabled bool
	// Concurrency 是并发对账的源数上限（越界/非正回退默认，上限 reconcileMaxConcurrency）。
	Concurrency int
	// Timeout 是单源对账总超时（含该源全部天的查询）。
	Timeout time.Duration
	// QueryTimeout 是单次 count 查询超时。
	QueryTimeout time.Duration
}

// DefaultReconcileConfig 返回默认对账配置（默认启用）。
func DefaultReconcileConfig() ReconcileConfig {
	return ReconcileConfig{
		Enabled:      true,
		Concurrency:  reconcileDefaultConcurrency,
		Timeout:      reconcileDefaultTimeout,
		QueryTimeout: reconcileDefaultQueryTimeout,
	}
}

// normalized 把越界/零值收敛到默认（配置误写不得让对账退化为无超时或高并发）。
func (c ReconcileConfig) normalized() ReconcileConfig {
	out := c
	if out.Concurrency <= 0 {
		out.Concurrency = reconcileDefaultConcurrency
	}
	if out.Concurrency > reconcileMaxConcurrency {
		out.Concurrency = reconcileMaxConcurrency
	}
	if out.Timeout <= 0 {
		out.Timeout = reconcileDefaultTimeout
	}
	if out.QueryTimeout <= 0 {
		out.QueryTimeout = reconcileDefaultQueryTimeout
	}
	return out
}

// ReconcileDayResult 是单「源 × UTC 天」的对账结论。
type ReconcileDayResult struct {
	UTCDay   string `json:"utc_day"`
	Expected int    `json:"expected"`
	Observed int    `json:"observed"`
	Missing  bool   `json:"missing"`
	// Basis 恒为 count_only：本条判定只比较条数，不比较内容（spec §2.4）。
	Basis string `json:"basis"`
	// Reason 是判定说明（observed_covers_expected / observed_below_expected / no_published_scope）。
	Reason string `json:"reason"`
}

// ReconcileReport 是一个源的启动对账结论（只读观测面，供日志与回归取证）。
type ReconcileReport struct {
	Source string               `json:"source"`
	Days   []ReconcileDayResult `json:"days,omitempty"`
	// MissingDays 是需要重发的 UTC 天（升序）；Fallback 为 true 时无意义。
	MissingDays []string `json:"missing_days,omitempty"`
	// Fallback 表示该源放弃增量判定、回退整窗重发（安全兜底）。
	Fallback       bool   `json:"fallback"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	// Queries 是本次实际发出的 count 查询数。
	Queries int `json:"queries"`
	// Basis 是报告级依据标记（恒为 count_only）。
	Basis    string        `json:"basis"`
	Duration time.Duration `json:"duration"`
}

// needsReplay 报告该源是否需要写 VL（缺失天非空，或回退整窗）。
func (r ReconcileReport) needsReplay() bool {
	return r.Fallback || len(r.MissingDays) > 0
}

// startupSource 是一个待恢复的源：登记完成、权威集合已重建。
type startupSource struct {
	source SourceConfig
	key    string
	events []logtypes.Event
}

// reconcileStartup 并发对账一批源，返回与输入等长的报告（顺序一一对应）。
//
// 并发只按「源」切分；源内按 UTC 天串行查询（天数量通常 1–3）。
func (m *Manager) reconcileStartup(ctx context.Context, items []startupSource) []ReconcileReport {
	cfg := m.reconcile
	reports := make([]ReconcileReport, len(items))
	if cfg.Concurrency <= 1 {
		for i, item := range items {
			reports[i] = m.reconcileSource(ctx, item, cfg)
		}
		return reports
	}
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			reports[index] = m.reconcileSource(ctx, items[index], cfg)
		}(i)
	}
	wg.Wait()
	return reports
}

// reconcileSource 对一个源做「源 × UTC 天」条数级对账。
//
// 判定与回退口径见 spec §2.1：不可判定（无已发布 scope）→ 判缺失（保守重发该天）；
// 查询不可信（失败/超时/不可解析）→ 回退整窗重发。
func (m *Manager) reconcileSource(ctx context.Context, item startupSource, cfg ReconcileConfig) (report ReconcileReport) {
	report = ReconcileReport{Source: item.key, Basis: reconcileBasisCountOnly}
	started := time.Now()
	defer func() { report.Duration = time.Since(started) }()
	if !cfg.Enabled {
		report.Fallback, report.FallbackReason = true, "reconcile_disabled"
		return report
	}
	m.mu.Lock()
	pending := m.state.Sources[item.key].PublicationPending
	m.mu.Unlock()
	if pending {
		// 上次发布未完成 → 发布状态不确定，不做增量裁剪（spec §2.5）。
		report.Fallback, report.FallbackReason = true, "publication_pending"
		return report
	}
	if m.vl == nil && m.vlRoute == nil {
		report.Fallback, report.FallbackReason = true, "vl_client_unavailable"
		return report
	}
	client, _, err := m.clientForSource(item.source)
	if err != nil || client == nil {
		report.Fallback, report.FallbackReason = true, "vl_client_unavailable"
		return report
	}
	grouped, days, err := groupEventsByUTCDay(item.source, item.events)
	if err != nil {
		report.Fallback, report.FallbackReason = true, "day_grouping_failed"
		return report
	}
	sourceCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	for _, day := range days {
		expected := len(grouped[day])
		if expected == 0 {
			continue
		}
		scope, scoped := m.publishedScope(item.source, day)
		if !scoped {
			// 无已发布投影（或该源 scope 未建立）→ 无法证明 VL 有数据 → 保守判缺失。
			report.Days = append(report.Days, ReconcileDayResult{
				UTCDay: day, Expected: expected, Missing: true,
				Basis: reconcileBasisCountOnly, Reason: "no_published_scope",
			})
			report.MissingDays = append(report.MissingDays, day)
			continue
		}
		observed, countErr := countVLDay(sourceCtx, client, item.source, day, scope, cfg.QueryTimeout)
		report.Queries++
		if countErr != nil {
			// 对账不可信 → 回退整窗重发（绝不因对账失败而少发）。
			report.Fallback, report.FallbackReason = true, "count_query_failed"
			report.Days, report.MissingDays = nil, nil
			slog.Warn("启动增量对账失败，回退整窗重发",
				"source", item.key, "utcDay", day, "error", countErr)
			return report
		}
		dayResult := ReconcileDayResult{
			UTCDay: day, Expected: expected, Observed: observed, Basis: reconcileBasisCountOnly,
		}
		if observed < expected {
			dayResult.Missing, dayResult.Reason = true, "observed_below_expected"
			report.MissingDays = append(report.MissingDays, day)
		} else {
			dayResult.Reason = "observed_covers_expected"
		}
		report.Days = append(report.Days, dayResult)
	}
	return report
}

// publishedScope 返回该源在该 UTC 天的「已发布代次白名单 + 封闭水位」。
//
// 与查询侧（query/vlrange.selectorQuery）同口径：对账测的是「查询侧能不能看到」。
// 白名单缺失（scope 不存在或 ProjectionGenerations 为空）时返回 ok=false，调用方据此
// 保守判缺失（宁可重发，不可漏发）。
func (m *Manager) publishedScope(source SourceConfig, day string) (catalog.SourceProjection, bool) {
	if m.cat == nil {
		return catalog.SourceProjection{}, false
	}
	record, ok := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
	if !ok || record.PublishedProjection == nil {
		return catalog.SourceProjection{}, false
	}
	for _, scope := range record.PublishedProjection.SourceProjections {
		if scope.LogSourceID != source.LogSourceID || scope.SourceGeneration != source.SourceGeneration {
			continue
		}
		if len(scope.ProjectionGenerations) == 0 {
			return catalog.SourceProjection{}, false
		}
		return scope, true
	}
	return catalog.SourceProjection{}, false
}

// countVLDay 查询该源该 UTC 天在 VL 中的条数（只读 stats count）。
func countVLDay(ctx context.Context, client *vlsup.Client, source SourceConfig, day string, scope catalog.SourceProjection, queryTimeout time.Duration) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("ingest: VictoriaLogs 客户端不可用")
	}
	dayStart, dayEnd, ok := utcDayBounds(day)
	if !ok {
		return 0, fmt.Errorf("ingest: 非法 UTC 日 %q", day)
	}
	if queryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, queryTimeout)
		defer cancel()
	}
	params := url.Values{
		"query": {reconcileSelector(source, scope) + " | stats count()"},
		"start": {dayStart.UTC().Format(time.RFC3339Nano)},
		"end":   {dayEnd.UTC().Format(time.RFC3339Nano)},
	}
	body, err := client.Get(ctx, reconcileStatsPath, params)
	if err != nil {
		return 0, err
	}
	return parseStatsCount(body)
}

// reconcileSelector 构造与查询侧同口径的 VL 选择器。
//
// 与 ingest 校验路径（verifyProjectionOnceAllowed）的差别：那里按「本次写入的代次」过滤，
// 这里按 catalog 已发布白名单过滤——因为对账问的是「查询侧现在能看到几条」。
func reconcileSelector(source SourceConfig, scope catalog.SourceProjection) string {
	parts := []string{
		"log_source_id:=" + strconv.Quote(source.LogSourceID),
		"source_generation:=" + strconv.Quote(source.SourceGeneration),
	}
	generations := make([]string, 0, len(scope.ProjectionGenerations))
	for _, generation := range scope.ProjectionGenerations {
		if strings.TrimSpace(generation) == "" {
			continue
		}
		generations = append(generations, "projection_generation:="+strconv.Quote(generation))
	}
	if len(generations) > 0 {
		parts = append(parts, "("+strings.Join(generations, " OR ")+")")
	}
	if scope.ClosedVisibleSeq > 0 {
		parts = append(parts, "record_end:<="+strconv.FormatUint(scope.ClosedVisibleSeq, 10))
	}
	return strings.Join(parts, " AND ")
}

// parseStatsCount 解析 stats 查询响应里的计数。
//
// 兼容两种已知形态（真机形态未在本地验证，故两侧都收）：
//   - Prometheus 向量形态：`data.result[].value = [<ts>, "<count>"]`（字符串或数字）；
//   - field/value 形态：`data.result[].values[].{field,value}`（取 field 含 count 者）。
//
// 空 result 判 0（VL 对「无匹配」的合法表达）；**任何解析歧义都返回错误**——调用方据此
// 回退整窗重发，绝不把「看不懂」当成 0 而漏发（spec §6）。
func parseStatsCount(body []byte) (int, error) {
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value  []json.RawMessage `json:"value"`
				Values []struct {
					Field string          `json:"field"`
					Value json.RawMessage `json:"value"`
				} `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("ingest: 解析 stats count 响应失败: %w", err)
	}
	if payload.Status != "" && !strings.EqualFold(payload.Status, "success") {
		return 0, fmt.Errorf("ingest: stats count 查询未成功: status=%s", payload.Status)
	}
	if len(payload.Data.Result) == 0 {
		return 0, nil
	}
	total, parsed := 0, 0
	for _, series := range payload.Data.Result {
		if len(series.Value) >= 2 {
			value, err := parseCountValue(series.Value[len(series.Value)-1])
			if err != nil {
				return 0, err
			}
			total += value
			parsed++
			continue
		}
		for _, field := range series.Values {
			if field.Field != "" && !strings.Contains(strings.ToLower(field.Field), "count") {
				continue
			}
			value, err := parseCountValue(field.Value)
			if err != nil {
				return 0, err
			}
			total += value
			parsed++
		}
	}
	if parsed == 0 {
		return 0, fmt.Errorf("ingest: stats count 响应无可识别的计数（series=%d）", len(payload.Data.Result))
	}
	return total, nil
}

// parseCountValue 把单个计数值（字符串或数字字面量）解析为非负整数。
func parseCountValue(raw json.RawMessage) (int, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, fmt.Errorf("ingest: stats count 计数值为空")
	}
	text = strings.Trim(text, `"`)
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("ingest: 解析 stats count 计数值 %q 失败: %w", text, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("ingest: stats count 计数值为负: %v", value)
	}
	return int(value), nil
}

// eventsForDays 按 UTC 天过滤权威集合（增量重发的写入面）。
func eventsForDays(source SourceConfig, events []logtypes.Event, days []string) ([]logtypes.Event, error) {
	if len(days) == 0 {
		return nil, nil
	}
	want := make(map[string]struct{}, len(days))
	for _, day := range days {
		want[day] = struct{}{}
	}
	out := make([]logtypes.Event, 0, len(events))
	for _, event := range events {
		day, err := eventUTCDay(source, event)
		if err != nil {
			return nil, err
		}
		if _, ok := want[day]; ok {
			out = append(out, event)
		}
	}
	return out, nil
}

// sameUTCDays 校验两组 UTC 天集合一致（增量写入面必须与计划天集合完全对应）。
//
// 为什么要硬校验：写入面与「计划天」错位时，可能出现「某天被标记为已发布却没写任何数据」
// ——那是静默丢数据。宁可启动失败，也不接受这种错位。
func sameUTCDays(got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("ingest: 增量写入面与计划 UTC 天不一致: 写入 %v, 计划 %v", got, want)
	}
	counts := make(map[string]int, len(got))
	for _, day := range got {
		counts[day]++
	}
	for _, day := range want {
		counts[day]--
		if counts[day] < 0 {
			return fmt.Errorf("ingest: 增量写入面缺少 UTC 天 %s（写入 %v, 计划 %v）", day, got, want)
		}
	}
	for day, remaining := range counts {
		if remaining != 0 {
			return fmt.Errorf("ingest: 增量写入面含计划外 UTC 天 %s（写入 %v, 计划 %v）", day, got, want)
		}
	}
	return nil
}

// sortUTCDays 归一化天的顺序（报告与写入面都按升序，便于断言与排障）。
func sortUTCDays(days []string) []string {
	out := append([]string(nil), days...)
	sort.Strings(out)
	return out
}

// reconcileConfigOf 归一化启动对账配置：nil 表示用默认（启用）。
func reconcileConfigOf(configured *ReconcileConfig) ReconcileConfig {
	if configured == nil {
		return DefaultReconcileConfig()
	}
	return configured.normalized()
}

// recordReconcileReports 保留最近一次启动对账结论，并把逐源结论写进日志。
func (m *Manager) recordReconcileReports(reports []ReconcileReport) {
	m.mu.Lock()
	m.reconcileReports = append([]ReconcileReport(nil), reports...)
	m.mu.Unlock()
	for _, report := range reports {
		switch {
		case report.Fallback:
			slog.Warn("启动增量对账不可信，回退整窗重发",
				"source", report.Source, "reason", report.FallbackReason,
				"queries", report.Queries, "duration", report.Duration)
		case len(report.MissingDays) > 0:
			slog.Info("启动增量对账发现缺失，仅重发缺失天",
				"source", report.Source, "missingDays", report.MissingDays,
				"days", len(report.Days), "queries", report.Queries, "duration", report.Duration)
		default:
			slog.Info("启动增量对账一致，跳过重发",
				"source", report.Source, "days", len(report.Days),
				"queries", report.Queries, "duration", report.Duration)
		}
	}
}

// LastReconcileReports 返回最近一次启动对账的逐源结论（只读快照）。
//
// 暴露出来是为了让「启动到底重发了什么、为什么」可被运维与回归直接取证，
// 而不是只能从 VL 侧的写入量反推。
func (m *Manager) LastReconcileReports() []ReconcileReport {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ReconcileReport(nil), m.reconcileReports...)
}

// applyStartupRecovery 按对账结论执行一个源的启动恢复（FR-497 spec §2.2）。
//
// 三种走向：
//   - 一致（无缺失、无回退）→ **零重发**：不生成新代、不写 VL、不改 catalog、不推进回收责任；
//   - 有缺失 → **只重发缺失天**（写入面限定，发布语义仍为 replace 取代该天旧代）；
//   - 回退 → **整窗重发**（写入面扩为权威全量，与旧实现逐字一致）。
func (m *Manager) applyStartupRecovery(item startupSource, report ReconcileReport) error {
	if !report.needsReplay() {
		m.mu.Lock()
		p := m.pipes[item.key]
		m.mu.Unlock()
		if p != nil && p.DeliveryState() == logtypes.DeliveryUnknown {
			// 对账已证明该源全部天的数据在 VL 中可见，故按既有语义解算 UNKNOWN 投递。
			// 不做 releaseRecovery：回收责任推进需要内容级保证，留给运行期正常路径。
			if err := p.ResolveUnknownThroughProjection(item.events); err != nil {
				return err
			}
		}
		return nil
	}
	// A new VL data root or a lost projection response must never rely on
	// the old physical generation. Rebuild a new isolated projection and
	// publish its manifest/watermark atomically.
	m.mu.Lock()
	saved := m.state.Sources[item.key]
	generation := nextProjectionGeneration(saved.ProjectionGeneration)
	saved.ProjectionGeneration = generation
	m.state.Sources[item.key] = saved
	m.mu.Unlock()
	plan := projectionWritePlan{Write: item.events, Replace: true}
	if !report.Fallback {
		writeEvents, err := eventsForDays(item.source, item.events, report.MissingDays)
		if err != nil {
			return err
		}
		plan.Write = writeEvents
		plan.Days = sortUTCDays(report.MissingDays)
	}
	if _, err := m.writeProjectionPlan(item.source, item.events, generation, plan); err != nil {
		return err
	}
	m.mu.Lock()
	p := m.pipes[item.key]
	m.mu.Unlock()
	if p != nil && p.DeliveryState() == logtypes.DeliveryUnknown {
		if err := p.ResolveUnknownThroughProjection(item.events); err != nil {
			return err
		}
	}
	return m.releaseRecovery(item.source, item.events)
}
