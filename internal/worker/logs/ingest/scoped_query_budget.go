package ingest

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 批准引擎默认16KiB；使用12KiB实际query硬界，给固定子句与后续协议字段留余量。
const scopedProjectionQueryMaxBytes = 12 << 10
const projectionVerifyFields = " | fields _time, _msg, event_id, level, stream, canonical_content_hash"

type projectionVerifyWindow struct {
	start time.Time
	end   time.Time
}

// 取证时间窗只按原簇计算一次；字节子片只能缩小ID集合，不得漏掉原窗内跨片时间的坏副本。
func projectionQueryWindowFor(source SourceConfig, events []logtypes.Event) (projectionVerifyWindow, error) {
	seen := make(map[string]struct{}, len(events))
	var first, last time.Time
	for _, event := range events {
		if _, duplicate := seen[event.EventID]; duplicate {
			return projectionVerifyWindow{}, fmt.Errorf("duplicate canonical event_id %s", event.EventID)
		}
		seen[event.EventID] = struct{}{}
		when, err := canonicalEventTime(event)
		if err != nil {
			return projectionVerifyWindow{}, fmt.Errorf("invalid canonical event time for %s: %w", event.EventID, err)
		}
		if first.IsZero() || when.Before(first) {
			first = when
		}
		if last.IsZero() || when.After(last) {
			last = when
		}
	}
	window := projectionVerifyWindow{start: first.Add(-time.Second), end: last.Add(time.Second)}
	if dayStart, dayEnd, ok := utcDayBounds(source.UTCDay); ok {
		if window.start.Before(dayStart) {
			window.start = dayStart
		}
		if window.end.After(dayEnd) {
			window.end = dayEnd
		}
	}
	return window, nil
}

func scopedProjectionQuery(source SourceConfig, generations []string, events []logtypes.Event) string {
	parts := make([]string, 0, len(generations))
	for _, generation := range generations {
		parts = append(parts, "projection_generation:="+strconv.Quote(generation))
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, strconv.Quote(event.EventID))
	}
	if len(events) > 0 {
		if events[0].Source.LogSourceID != "" {
			source.LogSourceID = events[0].Source.LogSourceID
		}
		if events[0].Source.SourceGeneration != "" {
			source.SourceGeneration = events[0].Source.SourceGeneration
		}
	}
	return "(" + strings.Join(parts, " OR ") + ") AND event_id:in(" + strings.Join(ids, ",") + ") AND log_source_id:=" + strconv.Quote(source.LogSourceID) + " AND source_generation:=" + strconv.Quote(source.SourceGeneration) + projectionVerifyFields
}

func scopedProjectionChunkEnd(source SourceConfig, generations []string, events []logtypes.Event, start int) (int, error) {
	if start >= len(events) {
		return start, nil
	}
	if events[start].Source.LogSourceID != "" {
		source.LogSourceID = events[start].Source.LogSourceID
	}
	if events[start].Source.SourceGeneration != "" {
		source.SourceGeneration = events[start].Source.SourceGeneration
	}
	bytes := len(scopedProjectionQuery(source, generations, nil))
	end := start
	for end < len(events) {
		added := len(strconv.Quote(events[end].EventID))
		if end > start {
			added++
		}
		if bytes+added > scopedProjectionQueryMaxBytes {
			break
		}
		bytes += added
		end++
	}
	if end == start {
		return start, fmt.Errorf("ingest: 单条逐字段证据selector超过%d字节安全界", scopedProjectionQueryMaxBytes)
	}
	return end, nil
}

type scopedVerifySpan struct{ start, end int }

type scopedVerifyEvidenceKey struct{}

// 每轮完整证明独立记账；相同ID跨响应重现同样拒绝，重试不能拼接旧轮证据。
type scopedVerifyEvidence struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (proof *scopedVerifyEvidence) record(id string) error {
	if proof == nil {
		return nil
	}
	proof.mu.Lock()
	defer proof.mu.Unlock()
	if _, duplicate := proof.seen[id]; duplicate {
		return fmt.Errorf("unexpected or duplicate projection event_id %s", id)
	}
	proof.seen[id] = struct{}{}
	return nil
}

func planScopedVerifySpans(source SourceConfig, generations []string, events []logtypes.Event) ([]scopedVerifySpan, error) {
	spans := []scopedVerifySpan{}
	for start := 0; start < len(events); {
		end, err := scopedProjectionChunkEnd(source, generations, events, start)
		if err != nil {
			return nil, err
		}
		spans = append(spans, scopedVerifySpan{start, end})
		start = end
	}
	return spans, nil
}

func scopedVerifyFailure(generation string, lastErr error) error {
	if lastErr != nil && !verifyErrorIsTimeoutLike(lastErr) {
		return fmt.Errorf("ingest: projection %s verification failed: %w", generation, lastErr)
	}
	return fmt.Errorf("ingest: projection %s not fully visible before deadline: %w", generation, context.DeadlineExceeded)
}

// ctx由调用方一次性定格，所有探测与完整证明共用同一deadline、窗口与原全局许可器。
func (m *Manager) verifyScopedWithReservedBudget(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, allowed map[string]logtypes.Event, spans []scopedVerifySpan, window projectionVerifyWindow, maxQueries int, generations []string) (verifyQueryStats, error) {
	var stats verifyQueryStats
	remaining := maxQueries
	needed := len(spans)
	if needed == 0 || needed > remaining {
		return stats, scopedVerifyFailure(generation, nil)
	}
	backoff := m.verifyBackoffMin
	var lastErr error
	query := func(queryCtx context.Context, span scopedVerifySpan) (bool, error) {
		if remaining < 1 {
			return false, scopedVerifyFailure(generation, lastErr)
		}
		// 规划保证每个span至多一条实际请求；即使查询尚未开始就失败，也不在本轮无限补发。
		remaining--
		complete, part, err := m.verifyProjectionQueryInWindow(queryCtx, client, source, generation, events[span.start:span.end], allowed, window, generations)
		stats.Queries += part.Queries
		stats.Rows += part.Rows
		stats.Bytes += part.Bytes
		return complete, err
	}
	terminal := func(err error) error {
		if err == nil {
			return nil
		}
		if vlsup.IsPermanent(err) {
			return fmt.Errorf("ingest: projection %s verification failed permanently: %w", generation, err)
		}
		if verifyErrorIsSemantic(err) {
			return fmt.Errorf("ingest: projection %s verification failed: %w", generation, err)
		}
		if ctx.Err() != nil {
			return scopedVerifyFailure(generation, ctx.Err())
		}
		return nil
	}
	for remaining >= needed {
		if ctx.Err() != nil {
			return stats, scopedVerifyFailure(generation, ctx.Err())
		}
		candidate := remaining == needed
		if !candidate {
			complete, err := query(ctx, spans[0])
			if stop := terminal(err); stop != nil {
				return stats, stop
			}
			lastErr = err
			candidate = complete && err == nil
		}
		if candidate {
			// 候选片不复用：重新读完所有片，所有字段与跨响应去重均属于同一证据轮。
			proofCtx := context.WithValue(ctx, scopedVerifyEvidenceKey{}, &scopedVerifyEvidence{seen: map[string]struct{}{}})
			complete := true
			for _, span := range spans {
				partComplete, err := query(proofCtx, span)
				if stop := terminal(err); stop != nil {
					return stats, stop
				}
				if err != nil {
					lastErr = err
					complete = false
					break
				}
				complete = complete && partComplete
			}
			if complete {
				return stats, nil
			}
		}
		if remaining < needed {
			break
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return stats, scopedVerifyFailure(generation, ctx.Err())
		case <-timer.C:
		}
		backoff = nextVerifyBackoff(backoff, m.verifyBackoffMax)
	}
	return stats, scopedVerifyFailure(generation, lastErr)
}
