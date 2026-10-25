package lifecycle

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

const repairMaxBytes = 64 << 20
const repairMaxEvents = 100000
const repairBatchEvents = 200

type repairSource struct {
	member *catalog.Record
	scope  catalog.SourceProjection
	events []logtypes.Event
	plan   catalog.DayRepairSource
}

func (v *VLPartitionOps) reserveBytes(key string, n int64) error {
	if v.ResourceBudget == nil {
		return fmt.Errorf("lifecycle: physical migration resource budget unavailable")
	}
	v.mu.Lock()
	_, exists := v.reservations[key]
	v.mu.Unlock()
	if exists {
		return nil
	}
	release, err := v.ResourceBudget.Reserve(vlsup.ReservationStaging, n)
	if err != nil {
		return err
	}
	v.mu.Lock()
	if _, exists = v.reservations[key]; exists {
		v.mu.Unlock()
		release()
		return nil
	}
	if v.reservations == nil {
		v.reservations = make(map[string]func())
	}
	v.reservations[key] = release
	v.mu.Unlock()
	return nil
}
func (v *VLPartitionOps) reserveTree(key, root string) error {
	var n int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("lifecycle: snapshot contains symbolic link")
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Size() > 1<<40-n {
				return fmt.Errorf("lifecycle: snapshot reservation exceeds bounded size")
			}
			n += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	return v.reserveBytes(key, n)
}
func (v *VLPartitionOps) releaseReservations(day string) {
	v.mu.Lock()
	var releases []func()
	for key, release := range v.reservations {
		if strings.HasSuffix(key, "/"+day) {
			releases = append(releases, release)
			delete(v.reservations, key)
		}
	}
	v.mu.Unlock()
	for _, release := range releases {
		release()
	}
}

func (v *VLPartitionOps) prepareHotSource(key catalog.PartitionKey) error {
	day, ok := v.Catalog.PhysicalDay(key.UTCDay)
	if !ok || !day.Frozen || len(day.Batches) == 0 {
		return fmt.Errorf("lifecycle: no controlled physical-day source batch")
	}
	memberAllowed := false
	for _, member := range day.ActiveBatch().Members {
		if member.Key == key && member.Owner == catalog.OwnerHot {
			memberAllowed = true
		}
	}
	if !memberAllowed {
		return fmt.Errorf("lifecycle: HOT reattach is outside fixed Catalog membership")
	}
	name, err := dayName(key)
	if err != nil {
		return err
	}
	return v.withTimeout(func(ctx context.Context) error {
		active, err := partitionActive(ctx, v.Hot, name)
		if err != nil || active {
			return err
		}
		root := filepath.Join(v.HotDataRoot, "partitions", name)
		if err := requirePathUnder(filepath.Join(v.HotDataRoot, "partitions"), root); err != nil {
			return err
		}
		if _, err = v.Hot.Post(ctx, "/internal/partition/attach", url.Values{"name": {name}}); err != nil {
			return err
		}
		return v.Catalog.RecordDayReattached(key.UTCDay)
	})
}

func repairPayload(event logtypes.Event, generation string) map[string]any {
	out := map[string]any{"_time": event.EventTimeUTC, "_msg": event.Message, "event_id": event.EventID, "log_source_id": event.Source.LogSourceID, "source_generation": event.Source.SourceGeneration, "parser_version": event.Source.ParserVersion, "record_start": event.Record.Start, "record_end": event.Record.End, "ingest_time_utc": event.IngestTimeUTC, "level": event.Level, "stream": event.Stream, "canonical_content_hash": event.CanonicalHash, "projection_generation": generation}
	for key, value := range event.Fields {
		if _, reserved := out[key]; !reserved {
			out[key] = value
		}
	}
	return out
}
func repairSelector(scope catalog.SourceProjection, generations []string, bounded bool) string {
	values := make([]string, 0, len(generations))
	for _, g := range generations {
		values = append(values, strconv.Quote(g))
	}
	selector := "log_source_id:=" + strconv.Quote(scope.LogSourceID) + " AND source_generation:=" + strconv.Quote(scope.SourceGeneration) + " AND projection_generation:in(" + strings.Join(values, ",") + ")"
	if bounded {
		selector += " AND record_end:<=" + strconv.FormatUint(scope.ClosedVisibleSeq, 10)
	}
	return selector
}

func repairRowMatches(row map[string]string, event logtypes.Event, generation string) bool {
	for key, value := range repairPayload(event, generation) {
		if key == "projection_generation" && generation == "" {
			continue
		}
		got, exists := row[key]
		if !exists {
			return false
		}
		if key == "_time" || key == "ingest_time_utc" {
			wantTime, err := time.Parse(time.RFC3339Nano, fmt.Sprint(value))
			gotTime, gotErr := time.Parse(time.RFC3339Nano, got)
			if err == nil && gotErr == nil && wantTime.Equal(gotTime) {
				continue
			}
		}
		if got != fmt.Sprint(value) {
			return false
		}
	}
	return true
}

// readRepairRows 对完整固定范围逐字段验证；未知、重复或截断均不产生接收凭据。
func (v *VLPartitionOps) readRepairRows(ctx context.Context, client *vlsup.Client, day, selector, generation string, events []logtypes.Event) (map[string]bool, error) {
	start, err := time.Parse("2006-01-02", day)
	if err != nil {
		return nil, err
	}
	want := make(map[string]logtypes.Event, len(events))
	for _, event := range events {
		if event.EventID == "" || event.CanonicalHash == "" {
			return nil, fmt.Errorf("lifecycle: canonical identity proof missing")
		}
		if _, dup := want[event.EventID]; dup {
			return nil, fmt.Errorf("lifecycle: duplicate canonical identity")
		}
		want[event.EventID] = event
	}
	seen := make(map[string]bool, len(events))
	params := url.Values{"query": {selector}, "start": {start.UTC().Format(time.RFC3339Nano)}, "end": {start.Add(24*time.Hour - time.Nanosecond).UTC().Format(time.RFC3339Nano)}}
	err = client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		reader := &io.LimitedReader{R: body, N: repairMaxBytes + 1}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 65536), 4<<20)
		rows := 0
		for scanner.Scan() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			rows++
			if rows > repairMaxEvents {
				return fmt.Errorf("lifecycle: canonical verification row budget exceeded")
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
				return err
			}
			row := make(map[string]string, len(raw))
			for key, value := range raw {
				var text string
				if err := json.Unmarshal(value, &text); err != nil {
					text = string(value)
				}
				row[key] = text
			}
			event, ok := want[row["event_id"]]
			if !ok || seen[event.EventID] {
				return fmt.Errorf("lifecycle: unexpected or duplicate canonical receiver identity")
			}
			if !repairRowMatches(row, event, generation) {
				return fmt.Errorf("lifecycle: canonical receiver field mismatch for %s", event.EventID)
			}
			seen[event.EventID] = true
		}
		if reader.N <= 0 {
			return fmt.Errorf("lifecycle: canonical verification byte budget exceeded")
		}
		return scanner.Err()
	})
	return seen, err
}

func (v *VLPartitionOps) canonicalRepairSources(req DayRepairRequest) ([]repairSource, int64, error) {
	if v.CanonicalDayInput == nil {
		return nil, 0, fmt.Errorf("lifecycle: controlled canonical input unavailable")
	}
	var sources []repairSource
	var total int64
	totalEvents := 0
	for _, member := range req.Members {
		if member == nil || member.Key.UTCDay != req.UTCDay || member.PublishedProjection == nil || !member.PublishedProjection.CoverageComplete || member.PublishedProjection.ConflictCount != 0 {
			return nil, 0, fmt.Errorf("lifecycle: fixed member canonical proof unavailable")
		}
		scopes := member.PublishedProjection.SourceProjections
		if len(scopes) == 0 {
			return nil, 0, fmt.Errorf("lifecycle: explicit canonical source scopes required")
		}
		events, err := v.CanonicalDayInput(member.Clone())
		if err != nil {
			return nil, 0, err
		}
		bySource := make(map[catalog.SourceGenerationRef][]logtypes.Event, len(scopes))
		for _, event := range events {
			ref := catalog.SourceGenerationRef{LogSourceID: event.Source.LogSourceID, SourceGeneration: event.Source.SourceGeneration}
			bySource[ref] = append(bySource[ref], event)
		}
		for _, scope := range scopes {
			selected := bySource[scope.SourceGenerationRef]
			delete(bySource, scope.SourceGenerationRef)
			sort.Slice(selected, func(i, j int) bool { return selected[i].EventID < selected[j].EventID })
			h := sha256.New()
			var bytes int64
			var maxEnd uint64
			for _, event := range selected {
				if event.Record.End > scope.ClosedVisibleSeq {
					return nil, 0, fmt.Errorf("lifecycle: canonical input exceeds fixed prefix")
				}
				data, err := json.Marshal(repairPayload(event, ""))
				if err != nil {
					return nil, 0, err
				}
				_, _ = h.Write(data)
				bytes += int64(len(data) + 1)
				if event.Record.End > maxEnd {
					maxEnd = event.Record.End
				}
			}
			if maxEnd != scope.ClosedVisibleSeq {
				return nil, 0, fmt.Errorf("lifecycle: canonical input does not reach captured source prefix")
			}
			total += bytes
			totalEvents += len(selected)
			if total > repairMaxBytes || totalEvents > repairMaxEvents {
				return nil, 0, fmt.Errorf("lifecycle: canonical repair input budget exceeded")
			}
			digest := hex.EncodeToString(h.Sum(nil))
			sourceKey := member.Key.String() + "/" + scope.LogSourceID + "/" + scope.SourceGeneration
			name := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s/%s", req.UTCDay, req.BatchID, sourceKey, digest)))
			sources = append(sources, repairSource{member: member, scope: scope, events: selected, plan: catalog.DayRepairSource{Key: sourceKey, InputDigest: digest, ProjectionGeneration: "day-repair-" + hex.EncodeToString(name[:16]), Events: len(selected), Bytes: bytes}})
		}
		if len(bySource) != 0 {
			return nil, 0, fmt.Errorf("lifecycle: canonical input contains undeclared source")
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].plan.Key < sources[j].plan.Key })
	return sources, total, nil
}

func (v *VLPartitionOps) repairDay(req DayRepairRequest) (*DayRepairResult, error) {
	if err := v.Catalog.CheckPhysicalDayDrained(req.UTCDay); err != nil {
		return nil, err
	}
	sources, bytes, err := v.canonicalRepairSources(req)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("lifecycle: fixed canonical repair input is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, source := range sources {
		if len(source.scope.ProjectionGenerations) == 0 {
			return nil, fmt.Errorf("lifecycle: original physical generation proof missing")
		}
		visible, err := v.readRepairRows(ctx, v.Hot, req.UTCDay, repairSelector(source.scope, source.scope.ProjectionGenerations, true), "", source.events)
		if err != nil {
			return nil, err
		}
		if len(visible) != len(source.events) {
			return nil, fmt.Errorf("lifecycle: fixed canonical input does not cover published HOT projection")
		}
	}
	plans := make([]catalog.DayRepairSource, len(sources))
	for i, source := range sources {
		plans[i] = source.plan
	}
	plans, err = v.Catalog.PlanDayRepair(req.UTCDay, req.BatchID, plans)
	if err != nil {
		return nil, err
	}
	if err = v.reserveBytes("repair/"+req.UTCDay, bytes); err != nil {
		return nil, err
	}
	if err = v.attachCold(req.Members[0].Key, req.TargetDirID); err != nil {
		return nil, err
	}
	result := &DayRepairResult{CopyID: "canonical-repair", Projections: make(map[catalog.PartitionKey]*catalog.PublishedProjection, len(req.Members))}
	checksum := sha256.New()
	for i, source := range sources {
		plan := plans[i]
		selector := repairSelector(source.scope, []string{plan.ProjectionGeneration}, false)
		if _, err = v.Cold.Post(ctx, "/internal/force_flush", nil); err != nil {
			return nil, err
		}
		visible, err := v.readRepairRows(ctx, v.Cold, req.UTCDay, selector, plan.ProjectionGeneration, source.events)
		if err != nil {
			return nil, err
		}
		for index := 0; index < plan.AttemptedThrough; index++ {
			if !visible[source.events[index].EventID] {
				return nil, fmt.Errorf("lifecycle: prior canonical write outcome is still uncertain; original retained")
			}
		}
		if plan.AttemptedThrough > plan.WrittenThrough {
			plan.WrittenThrough = plan.AttemptedThrough
			if err = v.Catalog.RecordDayRepairProgress(req.UTCDay, req.BatchID, plan.Key, plan.AttemptedThrough, plan.WrittenThrough, false); err != nil {
				return nil, err
			}
		}
		for start := plan.WrittenThrough; start < len(source.events); {
			if err = v.ResourceBudget.Admit(vlsup.WorkBackground); err != nil {
				return nil, err
			}
			end := start + repairBatchEvents
			if end > len(source.events) {
				end = len(source.events)
			}
			var payload strings.Builder
			for _, event := range source.events[start:end] {
				if visible[event.EventID] {
					continue
				}
				data, err := json.Marshal(repairPayload(event, plan.ProjectionGeneration))
				if err != nil {
					return nil, err
				}
				payload.Write(data)
				payload.WriteByte('\n')
			}
			if payload.Len() > 4<<20 {
				return nil, fmt.Errorf("lifecycle: canonical repair batch byte budget exceeded")
			}
			if err = v.Catalog.RecordDayRepairProgress(req.UTCDay, req.BatchID, plan.Key, end, start, false); err != nil {
				return nil, err
			}
			if payload.Len() > 0 {
				if _, err = v.Cold.InsertJSONLines(ctx, []byte(payload.String())); err != nil {
					return nil, err
				}
			}
			if _, err = v.Cold.Post(ctx, "/internal/force_flush", nil); err != nil {
				return nil, err
			}
			ids := make([]string, 0, end-start)
			for _, event := range source.events[start:end] {
				ids = append(ids, strconv.Quote(event.EventID))
			}
			seen, err := v.readRepairRows(ctx, v.Cold, req.UTCDay, selector+" AND event_id:in("+strings.Join(ids, ",")+")", plan.ProjectionGeneration, source.events[start:end])
			if err != nil {
				return nil, err
			}
			if len(seen) != end-start {
				return nil, fmt.Errorf("lifecycle: canonical repair batch is not fully visible")
			}
			if err = v.Catalog.RecordDayRepairProgress(req.UTCDay, req.BatchID, plan.Key, end, end, false); err != nil {
				return nil, err
			}
			start = end
		}
		all, err := v.readRepairRows(ctx, v.Cold, req.UTCDay, selector, plan.ProjectionGeneration, source.events)
		if err != nil {
			return nil, err
		}
		if len(all) != len(source.events) {
			return nil, fmt.Errorf("lifecycle: canonical receiver remains incomplete")
		}
		if err = v.Catalog.RecordDayRepairProgress(req.UTCDay, req.BatchID, plan.Key, len(source.events), len(source.events), true); err != nil {
			return nil, err
		}
		projection := result.Projections[source.member.Key]
		if projection == nil {
			projection = source.member.Clone().PublishedProjection
			projection.SourceProjections = nil
			projection.ProjectionGenerations = nil
			result.Projections[source.member.Key] = projection
		}
		scope := source.scope
		scope.ProjectionGenerations = []string{plan.ProjectionGeneration}
		projection.SourceProjections = append(projection.SourceProjections, scope)
		projection.ProjectionGenerations = append(projection.ProjectionGenerations, plan.ProjectionGeneration)
		projection.ProjectionGeneration = plan.ProjectionGeneration
		_, _ = checksum.Write([]byte(plan.Key + "/" + plan.InputDigest + "/" + plan.ProjectionGeneration))
	}
	result.Checksum = "logical:" + hex.EncodeToString(checksum.Sum(nil))
	for _, projection := range result.Projections {
		projection.ManifestVersion = result.Checksum
		projection.QueryLocationDirID = req.TargetDirID
	}
	return result, nil
}

func (v *VLPartitionOps) verifyAllDayReceivers(day string) error {
	metadata, ok := v.Catalog.PhysicalDay(day)
	if !ok {
		return catalog.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, batch := range metadata.Batches {
		sources, _, err := v.canonicalRepairSources(DayRepairRequest{UTCDay: day, BatchID: batch.ID, TargetDirID: metadata.TargetDirID, Members: batch.Members})
		if err != nil {
			return err
		}
		for _, source := range sources {
			current, ok := v.Catalog.Get(source.member.Key)
			if !ok || current.PublishedProjection == nil {
				return catalog.ErrNotFound
			}
			var generations []string
			for _, scope := range current.PublishedProjection.SourceProjections {
				if scope.SourceGenerationRef == source.scope.SourceGenerationRef {
					generations = scope.ProjectionGenerations
				}
			}
			if len(generations) == 0 {
				return fmt.Errorf("lifecycle: receiver whitelist missing")
			}
			seen, err := v.readRepairRows(ctx, v.Cold, day, repairSelector(source.scope, generations, true), "", source.events)
			if err != nil {
				return err
			}
			if len(seen) != len(source.events) {
				return fmt.Errorf("lifecycle: transferred receiver no longer covers canonical input")
			}
		}
	}
	return nil
}
