package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// VLRehydratePublisher is the physical publication boundary for a closed Deep
// archive generation. The Rehydrate VL client must belong to the managed
// Rehydrate namespace, not the HOT instance.
type VLRehydratePublisher struct {
	Catalog *catalog.Catalog
	VL      *vlsup.Client
	Timeout time.Duration
}

func (p *VLRehydratePublisher) ReportFailure(key PartitionKey, reason string) error {
	if p == nil || p.Catalog == nil {
		return fmt.Errorf("archive: Catalog is unavailable")
	}
	catKey := catalog.PartitionKey{StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay}
	rec, ok := p.Catalog.Get(catKey)
	if !ok {
		return fmt.Errorf("archive: Catalog partition missing")
	}
	reasons := append([]string(nil), rec.PartialReasons...)
	found := false
	for _, existing := range reasons {
		if existing == reason {
			found = true
		}
	}
	if !found {
		reasons = append(reasons, reason)
	}
	rec.RecoveryRequired = true
	rec.PartialReasons = reasons
	return p.Catalog.AppendJournal(catalog.JournalEntry{
		Key: catKey, Record: rec, Detail: "rehydrate failed",
	})
}

func (p *VLRehydratePublisher) Publish(ctx context.Context, key PartitionKey, events []ArchivedEvent, taskID string) error {
	if p == nil || p.Catalog == nil || p.VL == nil || taskID == "" || len(events) == 0 {
		return fmt.Errorf("archive: rehydrate publisher is unavailable")
	}
	catKey := catalog.PartitionKey{StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay}
	rec, exists := p.Catalog.Get(catKey)
	if !exists || rec.Owner != catalog.OwnerArchive || rec.Generation != key.Generation ||
		(rec.RecoveryRequired && !onlyRehydrateFailure(rec.PartialReasons)) {
		return fmt.Errorf("archive: partition has no matching DEEP Catalog authority")
	}
	projectionGeneration := "rehydrate-" + taskID
	want := make(map[string]ArchivedEvent, len(events))
	seq := make(map[string]uint64)
	sources := make(map[catalog.SourceGenerationRef]struct{})
	var first, last time.Time
	var payload strings.Builder
	for _, event := range events {
		when, err := time.Parse(time.RFC3339Nano, event.EventTimeUTC)
		if err != nil || event.RecordEnd <= event.RecordStart || event.EventID == "" ||
			event.LogSourceID == "" || event.SourceGeneration == "" {
			return fmt.Errorf("archive: invalid canonical event in recovery task")
		}
		if event.CanonicalHash != logtypes.CanonicalContentHash(event.EventTimeUTC, event.Level, event.Stream, event.Message) {
			return fmt.Errorf("archive: canonical content hash mismatch for %s", event.EventID)
		}
		if existing, duplicate := want[event.EventID]; duplicate {
			if existing.CanonicalHash != event.CanonicalHash {
				return fmt.Errorf("archive: IDENTITY_CONFLICT for event_id %s", event.EventID)
			}
			return fmt.Errorf("archive: duplicate canonical event_id %s", event.EventID)
		}
		if first.IsZero() || when.Before(first) {
			first = when
		}
		if last.IsZero() || when.After(last) {
			last = when
		}
		want[event.EventID] = event
		ref := catalog.SourceGenerationRef{LogSourceID: event.LogSourceID, SourceGeneration: event.SourceGeneration}
		sources[ref] = struct{}{}
		seqKey := event.LogSourceID + "/" + event.SourceGeneration
		if event.RecordEnd > seq[seqKey] {
			seq[seqKey] = event.RecordEnd
		}
		line := map[string]any{
			"_time": event.EventTimeUTC, "_msg": event.Message,
			"event_id": event.EventID, "log_source_id": event.LogSourceID,
			"source_generation": event.SourceGeneration, "parser_version": event.ParserVersion, "record_start": event.RecordStart,
			"record_end": event.RecordEnd, "ingest_time_utc": event.IngestTimeUTC,
			"level": event.Level, "stream": event.Stream,
			"canonical_content_hash": event.CanonicalHash, "projection_generation": projectionGeneration,
		}
		data, err := json.Marshal(line)
		if err != nil {
			return err
		}
		payload.Write(data)
		payload.WriteByte('\n')
	}
	if err := verifyClosedSourceRanges(events); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// An uncommitted generation remains diagnostic only after a lost response.
	// Retrying the job allocates a new task ID and cannot publish the old target.
	if _, err := p.VL.InsertJSONLines(ctx, []byte(payload.String())); err != nil {
		return fmt.Errorf("archive: Rehydrate VL write result uncertain: %w", err)
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		complete, err := p.verify(verifyCtx, projectionGeneration, want, first, last)
		if err != nil {
			return fmt.Errorf("archive: rehydrate projection verification failed: %w", err)
		}
		if complete {
			break
		}
		select {
		case <-verifyCtx.Done():
			return fmt.Errorf("archive: rehydrate projection incomplete: %w", verifyCtx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	current, exists := p.Catalog.Get(catKey)
	if !exists || current.Owner != rec.Owner || current.Generation != rec.Generation || current.OwnerDirID != rec.OwnerDirID ||
		projectionVersion(current) != projectionVersion(rec) {
		return fmt.Errorf("archive: Catalog authority changed before projection publication")
	}
	refs := make([]catalog.SourceGenerationRef, 0, len(sources))
	for source := range sources {
		refs = append(refs, source)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].LogSourceID != refs[j].LogSourceID {
			return refs[i].LogSourceID < refs[j].LogSourceID
		}
		return refs[i].SourceGeneration < refs[j].SourceGeneration
	})
	var maxEnd uint64
	for _, value := range seq {
		if value > maxEnd {
			maxEnd = value
		}
	}
	seq[catKey.String()], seq["default"] = maxEnd, maxEnd
	projection := &catalog.PublishedProjection{
		ManifestVersion:          "manifest-" + projectionGeneration,
		ProjectionGeneration:     projectionGeneration,
		CoveredSourceGenerations: refs,
		ClosedVisibleSeq:         seq,
		CoverageComplete:         true,
		QueryLocationDirID:       rec.OwnerDirID,
		QueryGeneration:          rec.Generation,
		ProjectionGenerations:    []string{projectionGeneration},
	}
	remainingReasons := make([]string, 0, len(rec.PartialReasons))
	for _, reason := range rec.PartialReasons {
		if reason != query.ReasonRehydrateFailed {
			remainingReasons = append(remainingReasons, reason)
		}
	}
	current.PublishedProjection = projection
	current.RecoveryRequired = len(remainingReasons) > 0
	current.PartialReasons = remainingReasons
	return p.Catalog.PublishProjection(rec, current)
}

func onlyRehydrateFailure(reasons []string) bool {
	if len(reasons) == 0 {
		return false
	}
	for _, reason := range reasons {
		if reason != query.ReasonRehydrateFailed {
			return false
		}
	}
	return true
}

func verifyClosedSourceRanges(events []ArchivedEvent) error {
	bySource := make(map[string][]ArchivedEvent)
	for _, event := range events {
		key := event.LogSourceID + "\x00" + event.SourceGeneration
		bySource[key] = append(bySource[key], event)
	}
	for source, list := range bySource {
		sort.Slice(list, func(i, j int) bool {
			if list[i].RecordStart != list[j].RecordStart {
				return list[i].RecordStart < list[j].RecordStart
			}
			return list[i].RecordEnd < list[j].RecordEnd
		})
		for i := 1; i < len(list); i++ {
			if list[i].RecordStart < list[i-1].RecordEnd {
				return fmt.Errorf("archive: overlapping record ranges for source %q", source)
			}
			if list[i].RecordStart-list[i-1].RecordEnd > 1 {
				return fmt.Errorf("archive: unclosed record gap for source %q", source)
			}
		}
	}
	return nil
}

func projectionVersion(rec *catalog.Record) string {
	if rec.PublishedProjection != nil {
		return rec.PublishedProjection.ManifestVersion
	}
	return ""
}

func (p *VLRehydratePublisher) verify(ctx context.Context, generation string, want map[string]ArchivedEvent, first, last time.Time) (bool, error) {
	params := url.Values{
		"query": {"projection_generation:=" + strconv.Quote(generation) + " | fields _time, _msg, event_id, canonical_content_hash"},
		"limit": {strconv.Itoa(len(want) + 1)},
		"start": {first.Add(-time.Second).UTC().Format(time.RFC3339Nano)},
		"end":   {last.Add(time.Second).UTC().Format(time.RFC3339Nano)},
	}
	seen := make(map[string]bool, len(want))
	err := p.VL.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		sc := bufio.NewScanner(io.LimitReader(body, 32<<20+1))
		sc.Buffer(make([]byte, 64*1024), 4<<20)
		for sc.Scan() {
			var row struct {
				ID      string `json:"event_id"`
				Hash    string `json:"canonical_content_hash"`
				Time    string `json:"_time"`
				Message string `json:"_msg"`
			}
			if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
				return err
			}
			event, ok := want[row.ID]
			if !ok || seen[row.ID] || row.Hash != event.CanonicalHash || row.Time != event.EventTimeUTC || row.Message != event.Message {
				return fmt.Errorf("unexpected or duplicate rehydrate event_id %s", row.ID)
			}
			seen[row.ID] = true
		}
		return sc.Err()
	})
	return len(seen) == len(want), err
}

var _ RehydratePublisher = (*VLRehydratePublisher)(nil)
