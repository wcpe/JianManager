package vlrange

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

var errSearchHydrationIdentity = errors.New("vlrange: search hydration identity mismatch")

type searchHydrationIdentity struct {
	time, id, source, generation, parser, hash string
	start, end                                 uint64
}

func hydrationIdentity(ev logtypes.Event) (searchHydrationIdentity, error) {
	at, err := time.Parse(time.RFC3339Nano, ev.EventTimeUTC)
	if err != nil {
		return searchHydrationIdentity{}, fmt.Errorf("%w: invalid event time", errSearchHydrationIdentity)
	}
	return searchHydrationIdentity{at.UTC().Format(time.RFC3339Nano), ev.EventID,
		ev.Source.LogSourceID, ev.Source.SourceGeneration, ev.Source.ParserVersion, ev.CanonicalHash,
		ev.Record.Start, ev.Record.End}, nil
}

func (id searchHydrationIdentity) selector() string {
	parts := []string{
		"event_id:=" + strconv.Quote(id.id),
		"log_source_id:=" + strconv.Quote(id.source),
		"source_generation:=" + strconv.Quote(id.generation),
		"parser_version:=" + strconv.Quote(id.parser),
		"canonical_content_hash:=" + strconv.Quote(id.hash),
		"_time:[" + strconv.Quote(id.time) + "," + strconv.Quote(id.time) + "]",
	}
	for _, record := range []struct {
		field string
		value uint64
	}{{"record_start", id.start}, {"record_end", id.end}} {
		value := strconv.Quote(strconv.FormatUint(record.value, 10))
		if record.value == 0 {
			// Legacy canonical rows can omit a zero record boundary. Both decode
			// to the same identity; the returned row is checked again below.
			parts = append(parts, record.field+":in("+value+`,"")`)
		} else {
			parts = append(parts, record.field+":="+value)
		}
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

// mergedHydrationSelector preserves one matching frozen range's authority in
// LogsQL, including its generation and watermarks. Restricting to that event's
// source keeps a large merged scope expressible without widening authorization.
func mergedHydrationSelector(rngs []query.AuthoritativeRange, q query.RangeQuery) func(logtypes.Event) string {
	return func(ev logtypes.Event) string {
		for _, rng := range rngs {
			if !rangeMatchesEvent(rng, ev) {
				continue
			}
			if rng.Projection != nil {
				projection := *rng.Projection
				if len(projection.SourceProjections) > 0 {
					for _, scope := range projection.SourceProjections {
						if scope.LogSourceID == ev.Source.LogSourceID && scope.SourceGeneration == ev.Source.SourceGeneration && ev.Record.End <= scope.ClosedVisibleSeq {
							projection.SourceProjections = []catalog.SourceProjection{scope}
							break
						}
					}
				} else {
					for _, source := range projection.CoveredSourceGenerations {
						if source.LogSourceID == ev.Source.LogSourceID && source.SourceGeneration == ev.Source.SourceGeneration {
							projection.CoveredSourceGenerations = []catalog.SourceGenerationRef{source}
							break
						}
					}
				}
				rng.Projection = &projection
			}
			partQ := q
			partQ.ClosedVisibleSeq = rng.ClosedVisibleSeq
			return selectorQuery(rng, partQ)
		}
		return ""
	}
}

func hydrationQuery(terms []string) string {
	// There is no full-width sort: each term names an already selected event.
	// One extra row detects duplicate physical rows instead of hiding a missing
	// identity behind a server limit. The client shares a single row allowance.
	return "(" + strings.Join(terms, " OR ") + ") | limit " + strconv.Itoa(len(terms)+1)
}

func splitHydrationTerms(terms []string, params url.Values) ([][]string, error) {
	p := cloneParams(params)
	p.Set("query", hydrationQuery(terms))
	p.Set("limit", strconv.Itoa(len(terms)+1))
	if physicalQueryFits(p.Get("query"), p.Get("extra_filters")) && len("GET "+queryPath+"?"+p.Encode()+" HTTP/1.1\r\n") <= physicalRequestLineMaxBytes {
		return [][]string{terms}, nil
	}
	if len(terms) <= 1 {
		return nil, fmt.Errorf("%w: selected event exceeds physical query limits", query.ErrRangeQueryBudget)
	}
	middle := len(terms) / 2
	left, err := splitHydrationTerms(terms[:middle], params)
	if err != nil {
		return nil, err
	}
	right, err := splitHydrationTerms(terms[middle:], params)
	return append(left, right...), err
}

func hydrationPartial(out *query.RangeResult, reason string) {
	out.Exhausted = false
	out.StatsQuality = query.StatsPartial
	out.CoverageReasons = append(out.CoverageReasons, reason)
}

// hydrateSearchItems runs only after the projected page and its safe timestamp
// groups are fixed. It reuses the original HTTP window/filter and read context;
// all requests debit the bytes already spent on top-K and boundary reads.
func (c *Client) hydrateSearchItems(ctx context.Context, params url.Values, q query.RangeQuery, out *query.RangeResult, matches func(logtypes.Event) bool, selectors ...func(logtypes.Event) string) error {
	if len(out.Items) == 0 {
		return nil
	}
	maxBytes := scanLimit(q.Budget)
	if out.Bytes >= maxBytes {
		hydrationPartial(out, query.ReasonBudgetExceeded)
		return nil
	}
	base := params.Get("query")
	if i := strings.LastIndex(base, " | fields "); i >= 0 {
		base = base[:i]
	} else {
		hydrationPartial(out, query.ReasonRangeUnavailable)
		return fmt.Errorf("%w: missing projected search selector", errSearchHydrationIdentity)
	}
	wanted := make(map[searchHydrationIdentity]int, len(out.Items))
	identities := make([]searchHydrationIdentity, len(out.Items))
	terms := make([]string, len(out.Items))
	identityByTerm := make(map[string]searchHydrationIdentity, len(out.Items))
	for i, ev := range out.Items {
		id, err := hydrationIdentity(ev)
		if err != nil {
			hydrationPartial(out, query.ReasonRangeUnavailable)
			return err
		}
		authority := base
		if len(selectors) > 0 {
			authority = selectors[0](ev)
		}
		if authority == "" {
			hydrationPartial(out, query.ReasonRangeUnavailable)
			return fmt.Errorf("%w: no frozen authority", errSearchHydrationIdentity)
		}
		identities[i], wanted[id] = id, i
		terms[i] = "((" + authority + ") AND " + id.selector() + ")"
		identityByTerm[terms[i]] = id
	}
	batches, err := splitHydrationTerms(terms, params)
	if err != nil {
		hydrationPartial(out, query.ReasonBudgetExceeded)
		return err
	}
	fields := make(map[searchHydrationIdentity]map[string]string, len(wanted))
	// A duplicate physical copy may use the lookahead slot. This allowance is
	// shared across every batch instead of being reset for each HTTP request.
	remainingRows := uint64(len(out.Items))*2 + 1
	for _, batch := range batches {
		if out.Bytes >= maxBytes || remainingRows == 0 {
			hydrationPartial(out, query.ReasonBudgetExceeded)
			return nil
		}
		allowed := make(map[searchHydrationIdentity]bool, len(batch))
		for _, term := range batch {
			allowed[identityByTerm[term]] = true
		}
		p := cloneParams(params)
		p.Set("query", hydrationQuery(batch))
		p.Set("limit", strconv.Itoa(len(batch)+1))
		n, cut, readErr := c.stream(ctx, p, maxBytes-out.Bytes, remainingRows, func(line []byte) error {
			remainingRows--
			ev, decodeErr := eventFromJSON(line)
			if decodeErr != nil {
				return decodeErr
			}
			id, identityErr := hydrationIdentity(ev)
			if identityErr != nil {
				return identityErr
			}
			i, ok := wanted[id]
			if !ok || !allowed[id] || !matches(ev) {
				return errSearchHydrationIdentity
			}
			original := out.Items[i]
			if ev.Message != original.Message || ev.Level != original.Level || ev.Stream != original.Stream || ev.IngestTimeUTC != original.IngestTimeUTC {
				return errSearchHydrationIdentity
			}
			if previous, duplicate := fields[id]; duplicate && !maps.Equal(previous, ev.Fields) {
				return fmt.Errorf("%w: conflicting structured fields", errSearchHydrationIdentity)
			}
			fields[id] = ev.Fields
			return nil
		})
		out.Bytes += n
		if cut || readErr != nil {
			reason := query.ReasonRangeUnavailable
			if cut || errors.Is(readErr, query.ErrRangeQueryBudget) {
				reason = query.ReasonBudgetExceeded
			}
			hydrationPartial(out, reason)
			if readErr != nil && !errors.Is(readErr, query.ErrRangeQueryBudget) {
				return fmt.Errorf("vlrange: hydrate selected search fields: %w", readErr)
			}
			return nil
		}
	}
	if len(fields) != len(wanted) {
		hydrationPartial(out, query.ReasonRangeUnavailable)
		return fmt.Errorf("%w: returned %d of %d selected events", errSearchHydrationIdentity, len(fields), len(wanted))
	}
	for i, id := range identities {
		out.Items[i].Fields = fields[id]
	}
	return nil
}
