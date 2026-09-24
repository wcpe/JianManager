package archive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func archivedTestEvent(day, message string) ArchivedEvent {
	source := logtypes.SourceIdentity{LogSourceID: "node:1", SourceGeneration: "g1", ParserVersion: DefaultParserVersion}
	event := logtypes.BuildEvent(source, logtypes.RecordRange{Start: 1, End: 8}, day+"T12:00:00Z", day+"T12:01:00Z", "INFO", "stdout", message)
	return ArchivedEvent{EventID: event.EventID, LogSourceID: source.LogSourceID,
		SourceGeneration: source.SourceGeneration, ParserVersion: source.ParserVersion,
		EventTimeUTC: event.EventTimeUTC, IngestTimeUTC: event.IngestTimeUTC,
		Level: event.Level, Stream: event.Stream, Message: event.Message,
		RecordStart: event.Record.Start, RecordEnd: event.Record.End, CanonicalHash: event.CanonicalHash}
}

func TestQueryBackendCannotClaimRestoredWithoutPublishedProjection(t *testing.T) {
	ctx := context.Background()
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22", Generation: 1}
	registry := NewRegistry(NewMemoryProvider())
	registered, err := registry.RegisterRaw(ctx, key, RawSource{
		Data:        []byte(`{"_time":"2026-09-22T12:00:00Z","_msg":"archived","event_id":"e1","log_source_id":"node:1","source_generation":"g1","record_start":1,"record_end":8,"canonical_content_hash":"h1"}` + "\n"),
		LogSourceID: "node:1", SourceGeneration: "g1", EventCount: 1,
	})
	require.NoError(t, err)
	backend := NewQueryBackend(registry, NewRehydrateManager(RehydrateOptions{}))
	res := backend.Rehydrate(ctx, query.QueryRequest{AuthorizedTargets: []string{"node:1/2026-09-22"}}, []string{registered.Object.ObjectID})
	require.NotEqual(t, string(TaskSucceeded), res.State)
	require.NotNil(t, res.Err, "a completed in-memory task is not a published VL/Catalog recovery")
}

func TestSealedArchiveGenerationRejectsNewObjects(t *testing.T) {
	ctx := context.Background()
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22", Generation: 1}
	reg := NewRegistry(NewMemoryProvider())
	first, err := reg.RegisterRaw(ctx, key, RawSource{Data: []byte("first"), EventCount: 1})
	require.NoError(t, err)
	manifest, err := reg.Seal(ctx, key)
	require.NoError(t, err)
	require.True(t, manifest.Coverage.Complete)
	require.Equal(t, EnumerationExhausted, manifest.Coverage.EnumerationState)
	_, err = reg.RegisterRaw(ctx, key, RawSource{Data: []byte("second"), EventCount: 1})
	require.ErrorContains(t, err, "sealed archive generation")
	again, err := reg.RegisterRaw(ctx, key, RawSource{Data: []byte("first"), EventCount: 1})
	require.NoError(t, err)
	require.Equal(t, first.Object.ObjectID, again.Object.ObjectID)
	require.True(t, again.AlreadyRegistered)
}

func TestRehydratePublishesOnlyAfterActualVLRowsAreVerified(t *testing.T) {
	ctx := context.Background()
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22", Generation: 1}
	event := archivedTestEvent("2026-09-22", "archived")
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	reg := NewRegistry(NewMemoryProvider())
	registered, err := reg.RegisterRaw(ctx, key, RawSource{Data: append(raw, '\n'), LogSourceID: "node:1", SourceGeneration: "g1", EventCount: 1})
	require.NoError(t, err)
	_, err = reg.Seal(ctx, key)
	require.NoError(t, err)
	cat := catalog.New(catalog.NewMemJournal())
	require.NoError(t, cat.Put(catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay}, catalog.OwnerArchive, 1, "deep-g1")))
	var writes [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/insert/jsonline":
			payload, _ := io.ReadAll(r.Body)
			writes = append(writes, payload)
			w.WriteHeader(http.StatusOK)
		case "/select/logsql/query":
			for _, data := range writes {
				if strings.Contains(r.URL.Query().Get("query"), "rehydrate-") {
					_, _ = w.Write(data)
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	backend := NewQueryBackend(reg, NewRehydrateManager(RehydrateOptions{}))
	backend.SetPublisher(&VLRehydratePublisher{Catalog: cat, VL: vl})
	res := backend.Rehydrate(ctx, query.QueryRequest{AuthorizedTargets: []string{"node:1/2026-09-22"}}, []string{registered.Object.ObjectID})
	require.Nil(t, res.Err)
	require.Equal(t, string(TaskSucceeded), res.State)
	require.Len(t, writes, 1)
	rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay})
	require.True(t, ok)
	require.Equal(t, "rehydrate-"+res.TaskID, rec.PublishedProjection.ProjectionGeneration)
	require.Equal(t, uint64(8), rec.PublishedProjection.ClosedVisibleSeq["node:1/g1"])
	replayed := catalog.New(cat.Journal())
	rec, ok = replayed.Get(rec.Key)
	require.True(t, ok)
	require.Equal(t, "rehydrate-"+res.TaskID, rec.PublishedProjection.ProjectionGeneration)
}

func TestRehydrateLostVLResponseAbandonsPhysicalGeneration(t *testing.T) {
	ctx := context.Background()
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22", Generation: 1}
	event := archivedTestEvent("2026-09-22", "archived")
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	reg := NewRegistry(NewMemoryProvider())
	registered, err := reg.RegisterRaw(ctx, key, RawSource{Data: append(raw, '\n'), EventCount: 1})
	require.NoError(t, err)
	_, err = reg.Seal(ctx, key)
	require.NoError(t, err)
	catKey := catalog.PartitionKey{StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay}
	cat := catalog.New(catalog.NewMemJournal())
	require.NoError(t, cat.Put(catalog.NewStableRecord(catKey, catalog.OwnerArchive, 1, "deep-g1")))
	var writes [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" {
			data, _ := io.ReadAll(r.Body)
			writes = append(writes, data)
			if len(writes) == 1 {
				conn, _, hijackErr := w.(http.Hijacker).Hijack()
				if hijackErr == nil {
					conn.Close()
				}
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/select/logsql/query" && len(writes) > 1 {
			_, _ = w.Write(writes[1])
		}
	}))
	defer server.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	backend := NewQueryBackend(reg, NewRehydrateManager(RehydrateOptions{}))
	backend.SetPublisher(&VLRehydratePublisher{Catalog: cat, VL: vl})
	req := query.QueryRequest{AuthorizedTargets: []string{"node:1/2026-09-22"}}
	first := backend.Rehydrate(ctx, req, []string{registered.Object.ObjectID})
	require.NotNil(t, first.Err)
	require.Equal(t, string(TaskFailed), first.State)
	rec, ok := cat.Get(catKey)
	require.True(t, ok)
	require.Nil(t, rec.PublishedProjection)
	require.True(t, rec.RecoveryRequired)
	require.Contains(t, rec.PartialReasons, query.ReasonRehydrateFailed)
	failedPlan := query.NewPlanner(cat, nil).Plan(query.PlanRequest{TargetIDs: []string{"node:1"}})
	require.False(t, failedPlan.Coverage.Complete)
	require.Contains(t, failedPlan.Coverage.PartialReasons, query.ReasonRehydrateFailed)
	second := backend.Rehydrate(ctx, req, []string{registered.Object.ObjectID})
	require.Nil(t, second.Err)
	require.Equal(t, string(TaskSucceeded), second.State)
	require.NotEqual(t, first.TaskID, second.TaskID)
	require.Len(t, writes, 2)
	require.NotEqual(t, string(writes[0]), string(writes[1]))
	rec, _ = cat.Get(catKey)
	require.Equal(t, "rehydrate-"+second.TaskID, rec.PublishedProjection.ProjectionGeneration)
	require.False(t, rec.RecoveryRequired)
	require.NotContains(t, rec.PartialReasons, query.ReasonRehydrateFailed)
}

func TestRehydrateDamagedObjectPersistsFailureAndCanRecover(t *testing.T) {
	ctx := context.Background()
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-21", Generation: 1}
	event := archivedTestEvent("2026-09-21", "recoverable archive")
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	payload := append(raw, '\n')
	provider := NewMemoryProvider()
	reg := NewRegistry(provider)
	registered, err := reg.RegisterRaw(ctx, key, RawSource{Data: payload, EventCount: 1})
	require.NoError(t, err)
	_, err = reg.Seal(ctx, key)
	require.NoError(t, err)
	catKey := catalog.PartitionKey{StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay}
	cat := catalog.New(catalog.NewMemJournal())
	require.NoError(t, cat.Put(catalog.NewStableRecord(catKey, catalog.OwnerArchive, 1, "deep-g1")))
	publisher := &VLRehydratePublisher{Catalog: cat}
	backend := NewQueryBackend(reg, NewRehydrateManager(RehydrateOptions{}))
	backend.SetPublisher(publisher)

	require.NoError(t, provider.Put(ctx, key, registered.Object.RelPath, []byte("damaged")))
	failed := backend.Rehydrate(ctx, query.QueryRequest{AuthorizedTargets: []string{"node:1/2026-09-21"}}, []string{registered.Object.ObjectID})
	require.NotNil(t, failed.Err)
	rec, ok := cat.Get(catKey)
	require.True(t, ok)
	require.Contains(t, rec.PartialReasons, query.ReasonRehydrateFailed)
	_, err = reg.RegisterRaw(ctx, key, RawSource{Data: payload, EventCount: 1})
	require.ErrorContains(t, err, "no longer matches its manifest")

	require.NoError(t, provider.Put(ctx, key, registered.Object.RelPath, payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/insert/jsonline":
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("X-Test-Payload", string(body))
			w.WriteHeader(http.StatusOK)
		case "/select/logsql/query":
			line := map[string]any{
				"_time": event.EventTimeUTC, "_msg": event.Message, "event_id": event.EventID,
				"canonical_content_hash": event.CanonicalHash,
			}
			_ = json.NewEncoder(w).Encode(line)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	publisher.VL = vl
	recovered := backend.Rehydrate(ctx, query.QueryRequest{AuthorizedTargets: []string{"node:1/2026-09-21"}}, []string{registered.Object.ObjectID})
	require.Nil(t, recovered.Err)
	rec, ok = cat.Get(catKey)
	require.True(t, ok)
	require.False(t, rec.RecoveryRequired)
	require.NotContains(t, rec.PartialReasons, query.ReasonRehydrateFailed)
}

func TestRehydrateRejectsIdentityTamperingAndUnclosedSourceGap(t *testing.T) {
	event := archivedTestEvent("2026-09-22", "archived")
	tampered := event
	tampered.EventID = strings.Repeat("f", 64)
	raw, err := json.Marshal(tampered)
	require.NoError(t, err)
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22", Generation: 1}
	reg := NewRegistry(NewMemoryProvider())
	registered, err := reg.RegisterRaw(context.Background(), key, RawSource{Data: append(raw, '\n'), EventCount: 1})
	require.NoError(t, err)
	_, err = reg.Seal(context.Background(), key)
	require.NoError(t, err)
	backend := NewQueryBackend(reg, NewRehydrateManager(RehydrateOptions{}))
	backend.SetPublisher(publisherFunc(func(context.Context, PartitionKey, []ArchivedEvent, string) error { return nil }))
	res := backend.Rehydrate(context.Background(), query.QueryRequest{AuthorizedTargets: []string{"node:1/2026-09-22"}}, []string{registered.Object.ObjectID})
	require.NotNil(t, res.Err)
	require.Contains(t, res.Err.Message, "invalid event identity")

	second := archivedTestEvent("2026-09-22", "second")
	second.RecordStart, second.RecordEnd = 20, 25
	second.EventID = logtypes.EventID(logtypes.SourceIdentity{LogSourceID: second.LogSourceID,
		SourceGeneration: second.SourceGeneration, ParserVersion: second.ParserVersion},
		logtypes.RecordRange{Start: second.RecordStart, End: second.RecordEnd})
	require.ErrorContains(t, verifyClosedSourceRanges([]ArchivedEvent{event, second}), "unclosed record gap")
}

type publisherFunc func(context.Context, PartitionKey, []ArchivedEvent, string) error

func (f publisherFunc) Publish(ctx context.Context, key PartitionKey, events []ArchivedEvent, taskID string) error {
	return f(ctx, key, events, taskID)
}

// TestArchiveBackendRejectsEmptyAuthorizedTargets 锁定 FR-472 §9「越权 Rehydrate/ArchiveStatus 被拒绝且不泄露」：
// 无授权目标时必须在 Worker 层结构化拒绝，不得默认落到 default 分区。
func TestArchiveBackendRejectsEmptyAuthorizedTargets(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(NewMemoryProvider())
	backend := NewQueryBackend(registry, NewRehydrateManager(RehydrateOptions{}))

	for _, tc := range []struct {
		name string
		call func(query.QueryRequest) query.QueryError
	}{
		{
			name: "rehydrate",
			call: func(req query.QueryRequest) query.QueryError {
				return *backend.Rehydrate(ctx, req, []string{"obj-1"}).Err
			},
		},
		{
			name: "archive_status",
			call: func(req query.QueryRequest) query.QueryError {
				return *backend.ArchiveStatus(ctx, req, []string{"obj-1"}).Err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.call(query.QueryRequest{AuthorizedTargets: nil, TimeRange: query.TimeRange{FromUTC: "2026-09-22T00:00:00Z"}})
			require.NotEqual(t, query.ErrorCode(""), resp.Code, "空授权目标必须返回结构化错误，而非静默成功")
			require.NotEqual(t, query.ErrCodeUnsupported, resp.Code)
		})
	}
}

// TestArchiveBackendRejectsMalformedAuthorizedTarget 非法目标（缺 UTC 日/无法解析）必须报错且不猜分区。
func TestArchiveBackendRejectsMalformedAuthorizedTarget(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(NewMemoryProvider())
	backend := NewQueryBackend(registry, NewRehydrateManager(RehydrateOptions{}))

	// 只有 namespace、且无时间范围 → 无法确定 UTC 日。
	res := backend.ArchiveStatus(ctx, query.QueryRequest{AuthorizedTargets: []string{"node:1"}}, []string{"obj-1"})
	require.NotNil(t, res.Err, "缺 UTC 日必须报错，不得猜 default 分区")
	require.NotEqual(t, query.ErrCodeUnsupported, res.Err.Code)
}
