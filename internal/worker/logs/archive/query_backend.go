package archive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// QueryBackend 将受管归档与恢复任务适配到既有 Worker 查询契约。
type QueryBackend struct {
	Registry  *Registry
	Tasks     *RehydrateManager
	Publisher RehydratePublisher
	budgetMu  sync.RWMutex
	budget    RehydrateResourceBudget
	published map[string]rehydratePublishedSummary
}

// RehydratePublisher 承担真实 VL 写入、逐行校验和 Catalog 原子发布，内存事件不是成功凭据。
type RehydratePublisher interface {
	Publish(ctx context.Context, key PartitionKey, events []ArchivedEvent, taskID string) error
}

// RehydrateFailureReporter 将恢复失败保持为可观察的 Catalog 不完整状态。
type RehydrateFailureReporter interface {
	ReportFailure(key PartitionKey, reason string) error
}

// SetPublisher 注入真实物化发布边界。
func (b *QueryBackend) SetPublisher(publisher RehydratePublisher) { b.Publisher = publisher }

// NewQueryBackend 保持旧构造签名，生产装配另行注入资源预算。
func NewQueryBackend(reg *Registry, rehydrate *RehydrateManager) *QueryBackend {
	return &QueryBackend{Registry: reg, Tasks: rehydrate}
}

func archiveNotReady(err error) *query.QueryError {
	return &query.QueryError{Code: query.ErrCodeNotReady, Message: err.Error(), Retryable: true}
}

func (b *QueryBackend) Rehydrate(ctx context.Context, req query.QueryRequest, objectIDs []string) query.ArchiveTaskResponse {
	if b == nil || b.Registry == nil || b.Tasks == nil {
		return query.ArchiveTaskResponse{Err: &query.QueryError{Code: query.ErrCodeUnsupported, Message: "rehydrate backend unavailable"}}
	}
	if b.Publisher == nil {
		return query.ArchiveTaskResponse{Err: archiveNotReady(fmt.Errorf("rehydrate projection publisher is not ready"))}
	}
	if err := ctx.Err(); err != nil {
		return query.ArchiveTaskResponse{Err: archiveNotReady(err)}
	}
	key, err := partitionKey(req)
	if err != nil {
		return query.ArchiveTaskResponse{Err: archiveNotReady(err)}
	}
	if err := validateArchiveObjectIDs(objectIDs); err != nil {
		return query.ArchiveTaskResponse{Err: archiveNotReady(err)}
	}
	if job, ok := b.reusePublished(ctx, key, objectIDs, req.PermissionScope); ok {
		return query.ArchiveTaskResponse{TaskID: job.TaskID, State: string(job.State)}
	}

	// 先合并、登记受管责任，重复请求不再各自读取正文或占用一份 staging。
	job, err := b.Tasks.Start(ctx, RehydrateRequest{PartitionKey: key, ArchiveObjectIDs: objectIDs, Holder: req.PermissionScope})
	if err != nil {
		return query.ArchiveTaskResponse{Err: archiveNotReady(err)}
	}
	if job.Waiters > 1 {
		return query.ArchiveTaskResponse{TaskID: job.TaskID, State: string(job.State)}
	}
	fail := func(cause error) query.ArchiveTaskResponse {
		var terminalErr error
		if ctx.Err() != nil {
			terminalErr = b.Tasks.Cancel(job.TaskID)
		} else {
			terminalErr = b.Tasks.Fail(job.TaskID, ReasonProviderUnavailable)
		}
		if terminalErr != nil {
			cause = fmt.Errorf("%w; archive: recovery terminal transition failed: %w", cause, terminalErr)
		}
		b.reportFailure(key, query.ReasonRehydrateFailed)
		// Cancel/Fail 对已终态任务幂等；只返回实读状态，未知任务不能伪造失败终态。
		state := ""
		if current, getErr := b.Tasks.Get(job.TaskID); getErr == nil {
			state = string(current.State)
		} else {
			cause = fmt.Errorf("%w; archive: recovery task state unavailable: %w", cause, getErr)
		}
		return query.ArchiveTaskResponse{TaskID: job.TaskID, State: state, Err: archiveNotReady(cause)}
	}
	consumerCtx, unpin, err := b.Tasks.pinArchiveConsumer(ctx, key, job)
	if err != nil {
		return fail(err)
	}
	ctx = consumerCtx
	var metadataRelease, bodyRelease func()
	defer func() {
		// 先摘除本次生产任务的瞬时正文，再释放预留；取消只发信号，不代替实际消费者退出。
		b.Tasks.dropQueryPayload(job.TaskID)
		if bodyRelease != nil {
			bodyRelease()
		}
		if metadataRelease != nil {
			metadataRelease()
		}
		unpin()
	}()
	budget := b.resourceBudget()
	if err := budget.admit(ctx); err != nil {
		return fail(err)
	}
	metadataRelease, err = budget.reserve(ctx, archiveManifestStagingBytes)
	if err != nil {
		return fail(err)
	}
	manifest, err := b.queryManifest(ctx, key)
	if err != nil {
		return fail(err)
	}
	if !manifest.Coverage.Complete || manifest.Coverage.EnumerationState != EnumerationExhausted {
		return fail(fmt.Errorf("archive generation has no closed, verified manifest"))
	}
	selected := make(map[string]bool, len(objectIDs))
	for _, id := range objectIDs {
		selected[id] = true
	}
	if len(selected) != len(manifest.RawFiles) {
		return fail(fmt.Errorf("rehydrate requires the full closed archive object set"))
	}
	for _, raw := range manifest.RawFiles {
		if !selected[raw.ObjectID] {
			return fail(fmt.Errorf("rehydrate requires the full closed archive object set"))
		}
	}
	plan, err := planArchiveLoad(manifest, objectIDs, true)
	if err != nil {
		return fail(err)
	}
	bodyRelease, err = budget.reserve(ctx, plan.staging)
	if err != nil {
		return fail(err)
	}
	events, err := b.loadEvents(ctx, key, manifest, plan)
	if err != nil {
		return fail(err)
	}
	if len(events) == 0 {
		return fail(fmt.Errorf("archive has no verified canonical events"))
	}
	if err := b.Tasks.setQueryOriginals(job.TaskID, events); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := b.Publisher.Publish(ctx, key, events, job.TaskID); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	completed, err := b.Tasks.Complete(job.TaskID, events)
	if err != nil {
		return fail(err)
	}
	// 成功可复用标记最后公开，避免合并请求在无独立预留下复制尚未摘除的 Result.Events。
	b.Tasks.dropQueryPayload(job.TaskID)
	completed.Result = nil
	b.rememberPublished(MergeKey(key, objectIDs), req.PermissionScope, completed, events)
	return query.ArchiveTaskResponse{TaskID: completed.TaskID, State: string(completed.State)}
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
	fail := func(err error) query.ArchiveStatusResponse {
		res.Err = archiveNotReady(err)
		res.Coverage.MarkIncomplete(query.ReasonArchiveNotRestored)
		return res
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	key, err := partitionKey(req)
	if err != nil {
		res.Err = archiveNotReady(err)
		res.Coverage.MarkIncomplete(query.ReasonNoCatalogAuthority)
		return res
	}
	if err := validateArchiveObjectIDs(objectIDs); err != nil {
		return fail(err)
	}
	budget := b.resourceBudget()
	if err := budget.admit(ctx); err != nil {
		return fail(err)
	}
	releaseMetadata, err := budget.reserve(ctx, archiveManifestStagingBytes)
	if err != nil {
		return fail(err)
	}
	defer releaseMetadata()
	manifest, err := b.queryManifest(ctx, key)
	if err != nil {
		return fail(err)
	}
	if !manifest.Coverage.Complete || manifest.Coverage.EnumerationState != EnumerationExhausted {
		res.Coverage.MarkIncomplete(query.ReasonArchiveNotRestored)
	}
	var knownIDs []string
	for _, id := range objectIDs {
		if _, ok := manifest.FindByObjectID(id); !ok {
			res.MissingIDs = append(res.MissingIDs, id)
		} else {
			knownIDs = append(knownIDs, id)
		}
	}
	if len(knownIDs) == 0 {
		res.Coverage.MarkIncomplete(query.ReasonArchiveNotRestored)
		return res
	}
	plan, err := planArchiveLoad(manifest, knownIDs, false)
	if err != nil {
		return fail(err)
	}
	release, err := budget.reserve(ctx, plan.staging)
	if err != nil {
		return fail(err)
	}
	defer release()
	ctx, unpin, err := b.Tasks.pinArchiveConsumer(ctx, key, nil)
	if err != nil {
		return fail(err)
	}
	defer unpin()
	for _, raw := range plan.raws {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		reader, err := openArchiveObject(ctx, b.Registry.Provider(), key, raw.RelPath)
		if err != nil {
			res.MissingIDs = append(res.MissingIDs, raw.ObjectID)
			continue
		}
		hash := sha256.New()
		n, readErr := io.CopyBuffer(hash, io.LimitReader(&archiveContextReader{ctx: ctx, reader: reader}, raw.SizeBytes+1), make([]byte, archiveReadBufferBytes))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || n != raw.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != raw.ContentSHA256 {
			res.MissingIDs = append(res.MissingIDs, raw.ObjectID)
			continue
		}
		res.AvailableIDs = append(res.AvailableIDs, raw.ObjectID)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if len(res.MissingIDs) > 0 {
		res.Coverage.MarkIncomplete(query.ReasonArchiveNotRestored)
	}
	return res
}

func (b *QueryBackend) loadEvents(ctx context.Context, key PartitionKey, manifest *Manifest, plan archiveLoadPlan) ([]ArchivedEvent, error) {
	out := make([]ArchivedEvent, 0)
	seen := make(map[string]string)
	var records uint64
	for _, raw := range plan.raws {
		reader, err := openArchiveObject(ctx, b.Registry.Provider(), key, raw.RelPath)
		if err != nil {
			return nil, err
		}
		err = func() (readErr error) {
			defer func() {
				if closeErr := reader.Close(); readErr == nil {
					readErr = closeErr
				}
			}()
			hash := sha256.New()
			limited := &io.LimitedReader{R: &archiveContextReader{ctx: ctx, reader: reader}, N: raw.SizeBytes + 1}
			scanner := bufio.NewScanner(io.TeeReader(limited, hash))
			scanner.Buffer(make([]byte, archiveReadBufferBytes), archiveMaxLineBytes)
			for scanner.Scan() {
				if err := ctx.Err(); err != nil {
					return err
				}
				if raw.SizeBytes+1-limited.N > raw.SizeBytes {
					return fmt.Errorf("archive object %s exceeds declared byte bound", raw.ObjectID)
				}
				line := bytes.TrimSpace(scanner.Bytes())
				if len(line) == 0 {
					continue
				}
				records++
				if records > plan.records {
					return fmt.Errorf("archive: canonical record count exceeds manifest bound")
				}
				var decoded struct {
					ArchivedEvent
					Time    string `json:"_time"`
					Message string `json:"_msg"`
				}
				if err := json.Unmarshal(line, &decoded); err != nil {
					return fmt.Errorf("decode archive %s: %w", raw.ObjectID, err)
				}
				event := decoded.ArchivedEvent
				if event.EventTimeUTC == "" {
					event.EventTimeUTC = decoded.Time
				}
				if event.Message == "" {
					event.Message = decoded.Message
				}
				if event.ParserVersion == "" {
					event.ParserVersion = manifest.ParserVersion
				}
				if event.EventID == "" || event.LogSourceID == "" || event.SourceGeneration == "" || event.RecordEnd <= event.RecordStart || event.EventTimeUTC == "" {
					return fmt.Errorf("archive object %s has incomplete canonical event", raw.ObjectID)
				}
				identity := logtypes.SourceIdentity{LogSourceID: event.LogSourceID, SourceGeneration: event.SourceGeneration, ParserVersion: event.ParserVersion}
				if logtypes.EventID(identity, logtypes.RecordRange{Start: event.RecordStart, End: event.RecordEnd}) != event.EventID {
					return fmt.Errorf("archive object %s has invalid event identity", raw.ObjectID)
				}
				if _, err := time.Parse(time.RFC3339Nano, event.EventTimeUTC); err != nil {
					return fmt.Errorf("archive object %s has invalid original time: %w", raw.ObjectID, err)
				}
				sum := logtypes.CanonicalContentHash(event.EventTimeUTC, event.Level, event.Stream, event.Message)
				if event.CanonicalHash != "" && event.CanonicalHash != sum {
					return fmt.Errorf("archive object %s has conflicting canonical content", raw.ObjectID)
				}
				event.CanonicalHash = sum
				if event.ObjectID == "" {
					event.ObjectID = raw.ObjectID
				}
				if existing, ok := seen[event.EventID]; ok {
					if existing != sum {
						return fmt.Errorf("archive IDENTITY_CONFLICT for event_id %s", event.EventID)
					}
					continue
				}
				seen[event.EventID] = sum
				out = append(out, event)
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("archive object %s bounded read: %w", raw.ObjectID, err)
			}
			if raw.SizeBytes+1-limited.N != raw.SizeBytes {
				return fmt.Errorf("archive object %s differs from declared byte bound", raw.ObjectID)
			}
			if hex.EncodeToString(hash.Sum(nil)) != raw.ContentSHA256 {
				return fmt.Errorf("archive object %s checksum mismatch", raw.ObjectID)
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}
	if records != plan.records {
		return nil, fmt.Errorf("archive: canonical record count differs from manifest")
	}
	return out, nil
}

func partitionKey(req query.QueryRequest) (PartitionKey, error) {
	if len(req.AuthorizedTargets) == 0 {
		return PartitionKey{}, fmt.Errorf("archive target is required")
	}
	if len(req.AuthorizedTargets[0]) > 4096 {
		return PartitionKey{}, fmt.Errorf("archive target exceeds bounded identity limit")
	}
	parts := strings.Split(strings.TrimSpace(req.AuthorizedTargets[0]), "/")
	if len(parts) == 1 {
		day := ""
		if len(req.TimeRange.FromUTC) >= 10 {
			day = req.TimeRange.FromUTC[:10]
		}
		if day == "" {
			return PartitionKey{}, fmt.Errorf("archive target must include a UTC day or query range")
		}
		key := PartitionKey{StorageNamespace: parts[0], UTCDay: day, Generation: 1}
		return key, key.Validate()
	}
	key := PartitionKey{StorageNamespace: strings.Join(parts[:len(parts)-1], "/"), UTCDay: parts[len(parts)-1], Generation: 1}
	if strings.HasPrefix(key.UTCDay, "g") && len(parts) >= 3 {
		key.StorageNamespace = strings.Join(parts[:len(parts)-2], "/")
		key.UTCDay = parts[len(parts)-2]
		generation, err := strconv.ParseUint(strings.TrimPrefix(parts[len(parts)-1], "g"), 10, 64)
		if err != nil {
			return PartitionKey{}, fmt.Errorf("%w: archive generation: %w", ErrInvalidKey, err)
		}
		key.Generation = generation
	}
	if err := key.Validate(); err != nil {
		return PartitionKey{}, err
	}
	return key, nil
}
