package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// QueryBackend adapts the archive registry/task manager to Worker Log RPCs.
// It keeps archive objects and rehydrate generations behind the query contract.
type QueryBackend struct {
	Registry  *Registry
	Tasks     *RehydrateManager
	Publisher RehydratePublisher
}

// RehydratePublisher owns the physical VL write, row verification and atomic
// Catalog publication. A task cannot succeed from an in-memory event list.
type RehydratePublisher interface {
	Publish(ctx context.Context, key PartitionKey, events []ArchivedEvent, taskID string) error
}

type RehydrateFailureReporter interface {
	ReportFailure(key PartitionKey, reason string) error
}

func (b *QueryBackend) SetPublisher(publisher RehydratePublisher) { b.Publisher = publisher }

// NewQueryBackend creates the production archive query adapter.
func NewQueryBackend(reg *Registry, rehydrate *RehydrateManager) *QueryBackend {
	return &QueryBackend{Registry: reg, Tasks: rehydrate}
}

func (b *QueryBackend) Rehydrate(ctx context.Context, req query.QueryRequest, objectIDs []string) query.ArchiveTaskResponse {
	if b == nil || b.Registry == nil || b.Tasks == nil {
		return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeUnsupported, Message: "rehydrate backend unavailable"}}
	}
	if b.Publisher == nil {
		return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeNotReady,
			Message: "rehydrate projection publisher is not ready", Retryable: true}}
	}
	key, err := partitionKey(req)
	if err != nil {
		return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeNotReady, Message: err.Error()}}
	}
	manifest, err := b.Registry.GetManifest(key)
	if err != nil || !manifest.Coverage.Complete || manifest.Coverage.EnumerationState != EnumerationExhausted {
		return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeNotReady,
			Message: "archive generation has no closed, verified manifest", Retryable: true}}
	}
	selected := make(map[string]bool, len(objectIDs))
	for _, id := range objectIDs {
		selected[id] = true
	}
	if len(selected) != len(manifest.RawFiles) {
		return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeNotReady,
			Message: "rehydrate requires the full closed archive object set"}}
	}
	for _, raw := range manifest.RawFiles {
		if !selected[raw.ObjectID] {
			return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeNotReady,
				Message: "rehydrate requires the full closed archive object set"}}
		}
	}
	events, loadErr := b.loadEvents(ctx, key, objectIDs)
	if loadErr != nil {
		b.reportFailure(key, query.ReasonRehydrateFailed)
		return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeNotReady, Message: loadErr.Error(), Retryable: true}}
	}
	if len(events) == 0 {
		return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeNotReady, Message: "archive has no verified canonical events"}}
	}
	job, err := b.Tasks.Start(ctx, RehydrateRequest{
		PartitionKey: key, ArchiveObjectIDs: objectIDs, Events: events, Holder: req.PermissionScope,
	})
	if err != nil {
		return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeNotReady, Message: err.Error(), Retryable: true}}
	}
	if job.Waiters > 1 {
		return query.ArchiveTaskResponse{TaskID: job.TaskID, State: string(job.State)}
	}
	if len(events) > 0 {
		if err := b.Publisher.Publish(ctx, key, events, job.TaskID); err != nil {
			_ = b.Tasks.Fail(job.TaskID, ReasonProviderUnavailable)
			b.reportFailure(key, query.ReasonRehydrateFailed)
			return query.ArchiveTaskResponse{TaskID: job.TaskID, State: string(TaskFailed), Err: &query.QueryError{
				Code: query.ErrCodeNotReady, Message: err.Error(), Retryable: true}}
		}
		job, err = b.Tasks.Complete(job.TaskID, events)
		if err != nil {
			return query.ArchiveTaskResponse{TaskID: job.TaskID, State: string(job.State), Err: &query.QueryError{Code: query.ErrCodeNotReady, Message: err.Error(), Retryable: true}}
		}
	}
	return query.ArchiveTaskResponse{TaskID: job.TaskID, State: string(job.State)}
}

func (b *QueryBackend) reportFailure(key PartitionKey, reason string) {
	if reporter, ok := b.Publisher.(RehydrateFailureReporter); ok {
		_ = reporter.ReportFailure(key, reason)
	}
}

func (b *QueryBackend) ArchiveStatus(ctx context.Context, req query.QueryRequest, objectIDs []string) query.ArchiveStatusResponse {
	res := query.ArchiveStatusResponse{RequestID: req.RequestID, Coverage: query.Coverage{Complete: true}}
	if b == nil || b.Registry == nil {
		res.Err = &query.QueryError{Code: query.ErrCodeUnsupported, Message: "archive registry unavailable"}
		res.Coverage.MarkIncomplete(query.ReasonUnsupported)
		return res
	}
	key, err := partitionKey(req)
	if err != nil {
		res.Err = &query.QueryError{Code: query.ErrCodeNotReady, Message: err.Error()}
		res.Coverage.MarkIncomplete(query.ReasonNoCatalogAuthority)
		return res
	}
	manifest, err := b.Registry.GetManifest(key)
	if err != nil {
		res.Err = &query.QueryError{Code: query.ErrCodeNotReady, Message: err.Error(), Retryable: true}
		res.Coverage.MarkIncomplete(query.ReasonArchiveNotRestored)
		return res
	}
	if !manifest.Coverage.Complete {
		res.Coverage.MarkIncomplete(query.ReasonArchiveNotRestored)
	}
	for _, id := range objectIDs {
		raw, ok := manifest.FindByObjectID(id)
		if !ok {
			res.MissingIDs = append(res.MissingIDs, id)
			continue
		}
		data, getErr := b.Registry.Provider().Get(ctx, key, raw.RelPath)
		if getErr != nil || ContentHash(data) != raw.ContentSHA256 {
			res.MissingIDs = append(res.MissingIDs, id)
			continue
		}
		res.AvailableIDs = append(res.AvailableIDs, id)
	}
	if len(res.MissingIDs) > 0 {
		res.Coverage.MarkIncomplete(query.ReasonArchiveNotRestored)
	}
	return res
}

func (b *QueryBackend) loadEvents(ctx context.Context, key PartitionKey, objectIDs []string) ([]ArchivedEvent, error) {
	manifest, err := b.Registry.GetManifest(key)
	if err != nil {
		return nil, err
	}
	var out []ArchivedEvent
	seen := make(map[string]string)
	for _, id := range objectIDs {
		raw, ok := manifest.FindByObjectID(id)
		if !ok {
			return nil, fmt.Errorf("archive object %s not found", id)
		}
		data, err := b.Registry.Provider().Get(ctx, key, raw.RelPath)
		if err != nil {
			return nil, err
		}
		if ContentHash(data) != raw.ContentSHA256 {
			return nil, fmt.Errorf("archive object %s checksum mismatch", id)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var event ArchivedEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				return nil, fmt.Errorf("decode archive %s: %w", id, err)
			}
			var vl struct {
				Time    string `json:"_time"`
				Message string `json:"_msg"`
			}
			if err := json.Unmarshal([]byte(line), &vl); err != nil {
				return nil, fmt.Errorf("decode archive %s: %w", id, err)
			}
			if event.EventTimeUTC == "" {
				event.EventTimeUTC = vl.Time
			}
			if event.Message == "" {
				event.Message = vl.Message
			}
			if event.ParserVersion == "" {
				event.ParserVersion = manifest.ParserVersion
			}
			if event.EventID == "" || event.LogSourceID == "" || event.SourceGeneration == "" ||
				event.RecordEnd <= event.RecordStart || event.EventTimeUTC == "" {
				return nil, fmt.Errorf("archive object %s has incomplete canonical event", id)
			}
			identity := logtypes.SourceIdentity{LogSourceID: event.LogSourceID,
				SourceGeneration: event.SourceGeneration, ParserVersion: event.ParserVersion}
			if logtypes.EventID(identity, logtypes.RecordRange{Start: event.RecordStart, End: event.RecordEnd}) != event.EventID {
				return nil, fmt.Errorf("archive object %s has invalid event identity", id)
			}
			if _, err := time.Parse(time.RFC3339Nano, event.EventTimeUTC); err != nil {
				return nil, fmt.Errorf("archive object %s has invalid original time: %w", id, err)
			}
			hash := logtypes.CanonicalContentHash(event.EventTimeUTC, event.Level, event.Stream, event.Message)
			if event.CanonicalHash != "" && event.CanonicalHash != hash {
				return nil, fmt.Errorf("archive object %s has conflicting canonical content", id)
			}
			event.CanonicalHash = hash
			if event.ObjectID == "" {
				event.ObjectID = id
			}
			if existing, ok := seen[event.EventID]; ok {
				if existing != event.CanonicalHash {
					return nil, fmt.Errorf("archive IDENTITY_CONFLICT for event_id %s", event.EventID)
				}
				continue
			}
			seen[event.EventID] = event.CanonicalHash
			out = append(out, event)
		}
	}
	return out, nil
}

func partitionKey(req query.QueryRequest) (PartitionKey, error) {
	if len(req.AuthorizedTargets) == 0 {
		return PartitionKey{}, fmt.Errorf("archive target is required")
	}
	parts := strings.Split(strings.TrimSpace(req.AuthorizedTargets[0]), "/")
	if len(parts) == 1 {
		day := ""
		if req.TimeRange.FromUTC != "" && len(req.TimeRange.FromUTC) >= 10 {
			day = req.TimeRange.FromUTC[:10]
		}
		if day == "" {
			return PartitionKey{}, fmt.Errorf("archive target must include a UTC day or query range")
		}
		return PartitionKey{StorageNamespace: parts[0], UTCDay: day, Generation: 1}, nil
	}
	key := PartitionKey{StorageNamespace: strings.Join(parts[:len(parts)-1], "/"), UTCDay: parts[len(parts)-1], Generation: 1}
	if strings.HasPrefix(key.UTCDay, "g") && len(parts) >= 3 {
		key.UTCDay = parts[len(parts)-2]
		gen, _ := strconv.ParseUint(strings.TrimPrefix(parts[len(parts)-1], "g"), 10, 64)
		key.Generation = gen
	}
	if err := key.Validate(); err != nil {
		return PartitionKey{}, err
	}
	return key, nil
}
