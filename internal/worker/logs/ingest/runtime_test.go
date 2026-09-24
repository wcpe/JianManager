package ingest

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

type appendFailJournal struct{ catalog.Journal }

func (j appendFailJournal) Append(catalog.JournalEntry) error { return errors.New("journal disk full") }

func runtimeTestUTCDay() string { return time.Now().UTC().Format("2006-01-02") }

func TestIdlePollDoesNotRewriteDurableHistory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	m, err := New(Options{Root: root, Catalog: catalog.New(nil), Sources: []SourceConfig{{LogSourceID: "idle", SourceGeneration: "g1", Path: path}}})
	require.NoError(t, err)
	require.NoError(t, m.persist())
	stamp := time.Unix(1700000000, 0)
	require.NoError(t, os.Chtimes(m.statePath, stamp, stamp))
	m.pollOnce()
	stat, err := os.Stat(m.statePath)
	require.NoError(t, err)
	require.Equal(t, stamp.Unix(), stat.ModTime().Unix(), "idle polling must not rewrite and fsync the complete history")
}

func TestAppendMicroProjectionPreservesLegacyPublishedGeneration(t *testing.T) {
	cat := catalog.New(nil)
	source := SourceConfig{LogSourceID: "node:1", SourceGeneration: "g1", StorageNamespace: "node:1", UTCDay: "2026-09-22"}
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
	rec.PublishedProjection = &catalog.PublishedProjection{ProjectionGeneration: "legacy-1", ManifestVersion: "legacy-manifest"}
	require.NoError(t, cat.Put(rec))
	m := &Manager{cat: cat}
	event := logtypes.Event{Record: logtypes.RecordRange{Start: 10, End: 20}}
	require.NoError(t, m.publish(source, "micro-2", []logtypes.Event{event}, false))
	rec, _ = cat.Get(key)
	require.Equal(t, []string{"legacy-1", "micro-2"}, rec.PublishedProjection.ProjectionGenerations)
}

func TestPublishPreservesOtherSourcesInSharedNamespace(t *testing.T) {
	cat := catalog.New(nil)
	m := &Manager{cat: cat}
	a := SourceConfig{LogSourceID: "source-a", SourceGeneration: "g1", StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	b := a
	b.LogSourceID = "source-b"
	events := []logtypes.Event{{Record: logtypes.RecordRange{Start: 1, End: 10}}}
	require.NoError(t, m.publish(a, "projection-1", events, false))
	key := catalog.PartitionKey{StorageNamespace: a.StorageNamespace, UTCDay: a.UTCDay}
	first, _ := cat.Get(key)
	require.NoError(t, m.publish(b, "projection-1", events, false))
	rec, _ := cat.Get(key)
	require.Len(t, rec.PublishedProjection.CoveredSourceGenerations, 2, "second source must not hide the first source")
	require.NotEqual(t, first.PublishedProjection.ManifestVersion, rec.PublishedProjection.ManifestVersion, "source-set change invalidates the old view")
	require.NoError(t, m.publish(a, "projection-2", events, true))
	rec, _ = cat.Get(key)
	require.Len(t, rec.PublishedProjection.CoveredSourceGenerations, 2, "compacting A must retain B")
	events[0].Record.End = 100
	require.NoError(t, m.publish(b, "projection-2", events, false))
	aEvent := logtypes.Event{Record: logtypes.RecordRange{Start: 10, End: 20}, EventTimeUTC: "2026-09-22T12:00:00Z"}
	closed, complete := m.publishedClosedForSource(a, persistedSource{Events: []logtypes.Event{aEvent}})
	require.False(t, complete, "B's position 100 cannot prove A's position 20; A is only closed through 10")
	require.Zero(t, closed)
}

type projectionVLFixture struct {
	mu     sync.Mutex
	writes [][]byte
}

func (f *projectionVLFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/insert/jsonline":
		payload, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.writes = append(f.writes, payload)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case "/select/logsql/query":
		selector := r.URL.Query().Get("query")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
		to, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
		f.mu.Lock()
		defer f.mu.Unlock()
		count := 0
		for _, payload := range f.writes {
			for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
				var row struct {
					ProjectionGeneration string `json:"projection_generation"`
					EventTimeUTC         string `json:"_time"`
					LogSourceID          string `json:"log_source_id"`
				}
				if json.Unmarshal([]byte(line), &row) != nil || !strings.Contains(selector, "projection_generation:="+strconv.Quote(row.ProjectionGeneration)) {
					continue
				}
				if strings.Contains(selector, "log_source_id:=") && !strings.Contains(selector, "log_source_id:="+strconv.Quote(row.LogSourceID)) {
					continue
				}
				when, timeErr := time.Parse(time.RFC3339Nano, row.EventTimeUTC)
				if timeErr == nil && ((!from.IsZero() && when.Before(from)) || (!to.IsZero() && when.After(to))) {
					continue
				}
				if limit > 0 && count >= limit {
					return
				}
				_, _ = w.Write(append([]byte(line), '\n'))
				count++
			}
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestManagerPartitionsProjectionByCanonicalEventUTCDay(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{LogSourceID: "node:cross-day", SourceGeneration: "g1", StorageNamespace: "node:cross-day", UTCDay: runtimeTestUTCDay()}
	m := &Manager{
		root: root, vl: client, cat: cat, journal: cat.Journal(), pipes: map[string]*pipeline.Pipeline{},
		state:     persistedState{Sources: map[string]persistedSource{source.LogSourceID + "/" + source.SourceGeneration: {}}},
		statePath: filepath.Join(root, "var", "log", "ingest.state.json"), verificationTimeout: time.Second,
	}
	identity := logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}
	events := []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 1, End: 10}, "2026-09-22T23:59:59Z", "2026-09-23T00:00:01Z", "INFO", "stdout", "before midnight"),
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 10, End: 20}, "2026-09-23T00:00:01Z", "2026-09-23T00:00:02Z", "INFO", "stdout", "after midnight"),
	}
	result, err := m.writeProjection(source, events, "projection-1", events, events, true)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, result.HTTPStatus)
	require.Len(t, fixture.writes, 2, "each UTC day must be written and verified independently")
	for _, day := range []string{"2026-09-22", "2026-09-23"} {
		rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		require.True(t, ok, day)
		require.Equal(t, "projection-1", rec.PublishedProjection.ProjectionGeneration)
		require.True(t, rec.PublishedProjection.CoverageComplete)
	}
	closed, complete := m.publishedClosedForSource(source, persistedSource{Events: events})
	require.True(t, complete)
	require.Equal(t, uint64(20), closed)
}

func newProjectionVL(t *testing.T) (*vlsup.Client, *projectionVLFixture) {
	t.Helper()
	fixture := &projectionVLFixture{}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture
}

// durableEvents 返回某源的权威 canonical 事件集合（FR-483 后事件体在磁盘段，不在 state 内联）。
// 顺序与事件段一致（追加序），便于断言消息内容。
func durableEvents(t *testing.T, m *Manager, key string) []logtypes.Event {
	t.Helper()
	var events []logtypes.Event
	require.NoError(t, m.events.Iterate(key, func(event logtypes.Event) error {
		events = append(events, event)
		return nil
	}))
	return events
}

// savedSource 返回某源的 state 元数据（事件体已外置，这里只用于读投影代次等字段）。
func savedSource(t *testing.T, m *Manager, key string) persistedSource {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Sources[key]
}

func TestManagerPollPublishesCatalogAndRestoresState(t *testing.T) {
	client, fixture := newProjectionVL(t)

	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: hello\n[12:00:02] [Server thread/INFO]: next\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "node:1", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
		}},
	})
	require.NoError(t, err)
	m.pollOnce()
	readiness := m.CutoverReadiness()
	require.True(t, readiness.LedgerReady, "%v", readiness.Reasons)
	pipe := m.pipes["node:1/g1"]
	positions, ok := pipe.Positions()
	require.True(t, ok)
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "operator review"))
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, positions.Reclaim, "TEST", "covered"))
	require.NoError(t, m.ResolveCoveredGaps())
	resolvedEntry := pipe.Ledger().Get(pipe.Key())
	require.False(t, resolvedEntry.AcquirePaused)
	require.True(t, resolvedEntry.Gaps[0].Resolved)
	require.Contains(t, string(fixture.writes[0]), "projection_generation")
	rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
	require.NotNil(t, rec.PublishedProjection)
	require.True(t, rec.PublishedProjection.CoverageComplete)
	require.NotEmpty(t, rec.PublishedProjection.ProjectionGeneration)
	_, err = os.Stat(filepath.Join(root, "var", "log", "ingest.state.json"))
	require.NoError(t, err)

	// 新 Manager 从状态文件恢复同一 source，在新 projection generation 中重建事件。
	cat2 := catalog.New(cat.Journal())
	m2, err := New(Options{Root: root, VL: client, Catalog: cat2, Journal: cat2.Journal(), Sources: []SourceConfig{{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m2.pollOnce()
	require.Contains(t, string(fixture.writes[len(fixture.writes)-1]), "projection-2")
	rec2, ok := cat2.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
	require.Equal(t, "projection-2", rec2.PublishedProjection.ProjectionGeneration)
}

func TestManagerAckLossDefersReclaimUntilProjectionReplay(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: ack one\n[12:00:02] [Server thread/INFO]: ack two\n"), 0o644))
	source := SourceConfig{LogSourceID: "ack:1", SourceGeneration: "g1", Path: logPath, Mode: pipeline.ModeFilePrimary, StorageNamespace: "ack:1", UTCDay: runtimeTestUTCDay()}
	t.Setenv("JIANMANAGER_LOG_INJECT_ACK_LOSS", "1")
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	m.pollOnce()
	entry := m.pipes["ack:1/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "ack:1", SourceGeneration: "g1"})
	require.Equal(t, logtypes.DeliveryUnknown, entry.DeliveryState)
	require.Zero(t, entry.Positions.Reclaim)

	os.Unsetenv("JIANMANAGER_LOG_INJECT_ACK_LOSS")
	cat2 := catalog.New(cat.Journal())
	m2, err := New(Options{Root: root, VL: client, Catalog: cat2, Journal: cat2.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	entry2 := m2.pipes["ack:1/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "ack:1", SourceGeneration: "g1"})
	require.Equal(t, logtypes.DeliveryReplayRequired, entry2.DeliveryState)
	require.NotZero(t, entry2.Positions.Reclaim)
}

func TestMicroGenerationLostResponseIsExcludedAndRestartCompacts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
	fixture := &projectionVLFixture{}
	insertCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" {
			insertCount++
			if insertCount == 2 {
				payload, _ := io.ReadAll(r.Body)
				fixture.mu.Lock()
				fixture.writes = append(fixture.writes, payload)
				fixture.mu.Unlock()
				conn, _, hijackErr := w.(http.Hijacker).Hijack()
				if hijackErr == nil {
					_ = conn.Close()
				}
				return
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	source := SourceConfig{LogSourceID: "node:lost", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:lost", UTCDay: time.Now().UTC().Format("2006-01-02")}
	manager, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	manager.pollOnce()
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	first, ok := cat.Get(key)
	require.True(t, ok)
	require.Equal(t, []string{"projection-1"}, first.PublishedProjection.ProjectionGenerations)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("[12:00:03] [Server thread/INFO]: uncertain\n[12:00:04] [Server thread/INFO]: next boundary\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	manager.pollOnce()
	afterLoss, ok := cat.Get(key)
	require.True(t, ok)
	require.Equal(t, []string{"projection-1"}, afterLoss.PublishedProjection.ProjectionGenerations)
	require.Equal(t, "projection-2", manager.state.Sources[source.LogSourceID+"/"+source.SourceGeneration].ProjectionGeneration)
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("[12:00:05] [Server thread/INFO]: following batch\n[12:00:06] [Server thread/INFO]: following boundary\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	manager.pollOnce()
	continued, _ := cat.Get(key)
	require.Equal(t, []string{"projection-3"}, continued.PublishedProjection.ProjectionGenerations, "retry without restart must replace any partially published attempt")
	fixture.mu.Lock()
	lastPayload := string(fixture.writes[len(fixture.writes)-1])
	fixture.mu.Unlock()
	require.Contains(t, lastPayload, "uncertain", "must recover prior durable events before advancing their closed prefix")

	recoveredCatalog := catalog.New(journal)
	recovered, err := New(Options{Root: root, VL: client, Catalog: recoveredCatalog, Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	require.Equal(t, "projection-4", recovered.state.Sources[source.LogSourceID+"/"+source.SourceGeneration].ProjectionGeneration)
	rec, ok := recoveredCatalog.Get(key)
	require.True(t, ok)
	require.Equal(t, []string{"projection-4"}, rec.PublishedProjection.ProjectionGenerations)
	require.NotContains(t, rec.PublishedProjection.ProjectionGenerations, "projection-2")
}

func TestManagerDoesNotExposeProjectionWhenJournalAppendFails(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o600))
	journal := appendFailJournal{catalog.NewMemJournal()}
	cat := catalog.New(journal)
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	_, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.False(t, ok, "uncommitted projection must never be queryable")
	require.Empty(t, durableEvents(t, m, "node:1/g1"))
	require.NotEmpty(t, m.state.Sources["node:1/g1"].WAL)

	recoveredJournal := catalog.NewMemJournal()
	recoveredCatalog := catalog.New(recoveredJournal)
	recovered, err := New(Options{Root: root, VL: client, Catalog: recoveredCatalog, Journal: recoveredJournal, Sources: []SourceConfig{{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	require.Len(t, durableEvents(t, recovered, "node:1/g1"), 1)
	_, ok = recoveredCatalog.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
}

func TestOwnerChangeDuringVerificationCannotPublishToNewOwner(t *testing.T) {
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "node:race", UTCDay: runtimeTestUTCDay()}
	fixture := &projectionVLFixture{}
	switched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/select/logsql/query" && !switched {
			switched = true
			rec := catalog.NewStableRecord(key, catalog.OwnerCold, 2, "cold-after-switch")
			require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, Record: rec, AuthorityCommit: true}))
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	root := t.TempDir()
	path := filepath.Join(root, "source.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: race\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
	m, err := New(Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{LogSourceID: "node:race", SourceGeneration: "g1", StorageNamespace: "node:race", Path: path}}})
	require.NoError(t, err)
	m.pollOnce()
	rec, _ := cat.Get(key)
	require.Nil(t, rec.PublishedProjection, "HOT verification must not publish into a newly selected COLD owner")
	require.NotEmpty(t, m.state.Sources["node:race/g1"].WAL)
	require.Empty(t, durableEvents(t, m, "node:race/g1"))
}

func TestManagerPersistsDurableWALBeforeInsertRequest(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "var", "log", "ingest.state.json")
	fixture := &projectionVLFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			data, err := os.ReadFile(statePath)
			if err != nil {
				t.Errorf("WAL snapshot missing before VL insert: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var state persistedState
			if err := json.Unmarshal(data, &state); err != nil {
				t.Errorf("WAL snapshot corrupt before VL insert: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			wal := state.Sources["node:1/g1"].WAL
			if len(wal) == 0 || !wal[0].Durable {
				t.Error("WAL event must be durable on disk before VL insert")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	entry := m.pipes["node:1/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:1", SourceGeneration: "g1"})
	require.Equal(t, logtypes.DeliveryRequestDone, entry.DeliveryState)
}

func TestManagerDoesNotPublishWhenProjectionRowsAreNotVisible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal(), VerificationTimeout: 50 * time.Millisecond,
		Sources: []SourceConfig{{LogSourceID: "node:1", SourceGeneration: "g1", Path: path,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()}}})
	require.NoError(t, err)
	m.pollOnce()
	_, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.False(t, ok, "request success without visible canonical rows cannot publish")
	require.NotEmpty(t, m.state.Sources["node:1/g1"].WAL)
}

func TestManagerNewBatchPublishesIsolatedView(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o600))
	source := SourceConfig{LogSourceID: "node:1", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	m.pollOnce()
	partition := catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()}
	first, ok := cat.Get(partition)
	require.True(t, ok)
	require.Equal(t, "projection-1", first.PublishedProjection.ProjectionGeneration)
	planner := query.NewPlanner(cat, nil)
	old := planner.Plan(query.PlanRequest{TargetIDs: []string{"node:1"}}).View
	require.NotNil(t, old)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("[12:00:03] [Server thread/INFO]: third\n[12:00:04] [Server thread/INFO]: fourth\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	m.pollOnce()
	second, ok := cat.Get(partition)
	require.True(t, ok)
	require.Equal(t, "projection-2", second.PublishedProjection.ProjectionGeneration)
	require.Equal(t, []string{"projection-1", "projection-2"}, second.PublishedProjection.ProjectionGenerations)
	require.Equal(t, query.ErrCodeViewStale, planner.Plan(query.PlanRequest{ViewRef: &query.ViewRef{ViewID: old.ViewID}}).Err.Code)
	require.Len(t, durableEvents(t, m, "node:1/g1"), 3)
	require.Len(t, fixture.writes, 2)
	require.NotContains(t, string(fixture.writes[1]), "first", "normal batches write only the new immutable micro-generation")
	positions, ok := m.pipes["node:1/g1"].Positions()
	require.True(t, ok)
	require.Equal(t, positions.Durable, positions.Reclaim)
}

func TestCanonicalRecoveryEventsUseDurableTimeForRawEvent(t *testing.T) {
	identity := logtypes.SourceIdentity{LogSourceID: "node:1", SourceGeneration: "g1", ParserVersion: "1.0.0"}
	event := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 1, End: 2}, "", "2026-09-22T12:00:00Z", "", "stdout", "raw")
	result, err := (&Manager{}).canonicalRecoveryEvents("node:1/g1", persistedSource{
		Events: []logtypes.Event{event},
		WAL:    []acquire.WALEntry{{Event: event, Durable: true}},
	})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, event.EventID, result[0].EventID)
	require.Equal(t, "2026-09-22T12:00:00Z", result[0].EventTimeUTC)
	require.Equal(t, logtypes.CanonicalContentHash(result[0].EventTimeUTC, "", "stdout", "raw"), result[0].CanonicalHash)
}

func TestProjectionVerificationQueriesHistoricalEventTime(t *testing.T) {
	event := logtypes.BuildEvent(logtypes.SourceIdentity{LogSourceID: "node:1", SourceGeneration: "g1", ParserVersion: "v1"},
		logtypes.RecordRange{Start: 1, End: 3}, "2026-08-01T03:04:05Z", "2026-09-22T12:00:00Z", "INFO", "stdout", "old")
	var from, to string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, to = r.URL.Query().Get("start"), r.URL.Query().Get("end")
		_, _ = w.Write([]byte(`{"_time":"2026-08-01T03:04:05Z","_msg":"old","event_id":"` + event.EventID +
			`","level":"INFO","stream":"stdout","canonical_content_hash":"` + event.CanonicalHash + `"}` + "\n"))
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m := &Manager{vl: client}
	complete, err := m.verifyProjectionOnce(context.Background(), SourceConfig{LogSourceID: "node:1", SourceGeneration: "g1"},
		"projection-old", []logtypes.Event{event})
	require.NoError(t, err)
	require.True(t, complete)
	require.NotEmpty(t, from)
	require.NotEmpty(t, to)
	require.Less(t, from, event.EventTimeUTC)
	require.Greater(t, to, event.EventTimeUTC)
}

func TestManagerCapacityPausePersistsGapBeforeVLDelivery(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: disk full\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		CapacityProvider: func() (acquire.CapacityBudget, error) {
			budget := acquire.DefaultCapacityBudget()
			budget.DiskUsagePercent = 95
			return budget, nil
		}, Sources: []SourceConfig{{
			LogSourceID: "node:disk", SourceGeneration: "g1", Path: path,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:disk", UTCDay: "2026-09-23",
		}}})
	require.NoError(t, err)
	m.pollOnce()
	require.Empty(t, fixture.writes, "paused capacity must never call VictoriaLogs")
	entry := m.pipes["node:disk/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:disk", SourceGeneration: "g1"})
	require.NotNil(t, entry)
	require.True(t, entry.AcquirePaused)
	require.NotEmpty(t, entry.Gaps)
	require.Equal(t, "PAUSED", entry.Gaps[0].Reason)
	require.Contains(t, entry.Gaps[0].Detail, "95.0%")
	state, err := os.ReadFile(filepath.Join(root, "var", "log", "ingest.state.json"))
	require.NoError(t, err)
	require.Contains(t, string(state), "PAUSED")
	readiness := m.CutoverReadiness()
	require.False(t, readiness.LedgerReady)
	require.Contains(t, readiness.Reasons, "node:disk/g1:acquire_paused")
}

func TestManagerRecoveryHoldSurvivesRestartAndReleasesAfterConstraintClears(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: held\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
	source := SourceConfig{LogSourceID: "node:hold", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:hold", UTCDay: "2026-09-23"}
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	held, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{source},
		RecoveryHold: func(SourceConfig, string) (bool, string) { return true, "active query lease" }})
	require.NoError(t, err)
	held.pollOnce()
	entry := held.pipes["node:hold/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:hold", SourceGeneration: "g1"})
	require.Zero(t, entry.Positions.Reclaim)
	require.Len(t, entry.RecoveryRefs, 1)
	require.Equal(t, "projection-recovery-projection-1-"+strconv.FormatUint(entry.Positions.Durable, 10), entry.RecoveryRefs[0].SegmentID)
	require.Equal(t, logtypes.RecoveryWALResponsibilityXfer, entry.RecoveryRefs[0].State)
	require.True(t, entry.RecoveryRefs[0].HasHold)
	require.Empty(t, entry.RecoveryRefs[0].ReleaseReason)
	require.Equal(t, "projection:projection-1", entry.RecoveryRefs[0].ResponsibilityReceiver)

	var resumedGeneration string
	recovered, err := New(Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, RecoveryHold: func(_ SourceConfig, generation string) (bool, string) {
			resumedGeneration = generation
			return false, ""
		}})
	require.NoError(t, err)
	require.Equal(t, "projection-2", resumedGeneration)
	require.Equal(t, "projection-2", recovered.state.Sources["node:hold/g1"].ProjectionGeneration)
	entry = recovered.pipes["node:hold/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:hold", SourceGeneration: "g1"})
	require.Equal(t, entry.Positions.Durable, entry.Positions.Reclaim)
	require.Empty(t, entry.Gaps)
	require.Len(t, entry.RecoveryRefs, 1, "restart resumes the existing held responsibility record")
	for _, ref := range entry.RecoveryRefs {
		require.Contains(t, ref.SegmentID, "projection-1")
		require.Equal(t, logtypes.RecoveryCleaned, ref.State)
		require.False(t, ref.HasHold)
		require.Equal(t, logtypes.ReleaseProjectionBacked, ref.ReleaseReason)
		require.Equal(t, "projection:projection-2", ref.ResponsibilityReceiver)
	}
	recovered.pollOnce()
	entry = recovered.pipes["node:hold/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:hold", SourceGeneration: "g1"})
	require.Empty(t, entry.Gaps, "replaying the pending line after restart must not manufacture a zero-width event")
}

func TestManagerRoutesStableColdOwnerAndKeepsFrozenStagingUnpublished(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frozen bool
	}{
		{name: "stable cold"},
		{name: "frozen staging", frozen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hot, hotFixture := newProjectionVL(t)
			cold, coldFixture := newProjectionVL(t)
			root := t.TempDir()
			path := filepath.Join(root, "latest.log")
			require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: routed\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
			key := catalog.PartitionKey{StorageNamespace: "node:routed", UTCDay: time.Now().UTC().Format("2006-01-02")}
			cat := catalog.New(catalog.NewMemJournal())
			if tc.frozen {
				require.NoError(t, cat.Put(catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")))
				_, err := cat.BeginMigration(key, catalog.OwnerCold, "cold-g2")
				require.NoError(t, err)
			} else {
				require.NoError(t, cat.Put(catalog.NewStableRecord(key, catalog.OwnerCold, 2, "cold-g2")))
			}
			manager, err := New(Options{Root: root, VL: hot, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
				LogSourceID: "node:routed", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
				StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay,
			}}, VLRoute: func(source SourceConfig) (*vlsup.Client, bool, error) {
				target, ok := cat.RouteWrite(key, source.UTCDay)
				require.True(t, ok)
				require.Equal(t, catalog.OwnerCold, target.Owner)
				return cold, target.Frozen, nil
			}})
			require.NoError(t, err)
			manager.pollOnce()
			require.Empty(t, hotFixture.writes)
			require.Len(t, coldFixture.writes, 1)
			rec, ok := cat.Get(key)
			require.True(t, ok)
			if tc.frozen {
				require.Nil(t, rec.PublishedProjection)
				require.NotEmpty(t, manager.state.Sources["node:routed/g1"].WAL)
			} else {
				require.NotNil(t, rec.PublishedProjection)
				require.Equal(t, "cold-g2", rec.PublishedProjection.QueryLocationDirID)
				require.EqualValues(t, 2, rec.PublishedProjection.QueryGeneration)
			}
		})
	}
}

func TestPrepareCutoverFlushesPendingEventBeforeReportingReady(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	payload := []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: pending\n")
	require.NoError(t, os.WriteFile(path, payload, 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	manager, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "node:cutover", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:cutover", UTCDay: "2026-09-23",
	}}})
	require.NoError(t, err)
	manager.pollOnce()
	require.Len(t, durableEvents(t, manager, "node:cutover/g1"), 1)
	ready := manager.PrepareCutoverReadiness()
	require.True(t, ready.LedgerReady, "%v", ready.Reasons)
	require.Len(t, durableEvents(t, manager, "node:cutover/g1"), 2)
	entry := manager.pipes["node:cutover/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:cutover", SourceGeneration: "g1"})
	require.EqualValues(t, len(payload), entry.Positions.Read)
	require.Equal(t, entry.Positions.Read, entry.Positions.Durable)
	require.Equal(t, entry.Positions.Durable, entry.Positions.Reclaim)
}

func TestManagerAutoImportsHistoricalGzipBeforeCurrentFile(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	archivePath := filepath.Join(logDir, "2026-09-22-1.log.gz")
	writeRuntimeGzip(t, archivePath,
		"[10:00:00] [Server thread/INFO]: historical one\n"+
			"[10:00:01] [Server thread/ERROR]: historical two\n")
	livePath := filepath.Join(logDir, "latest.log")
	require.NoError(t, os.WriteFile(livePath, []byte(
		"[10:00:02] [Server thread/INFO]: current one\n"+
			"[10:00:03] [Server thread/INFO]: current boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "inst:archive/file", SourceGeneration: "holder-g1", Path: livePath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:archive", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	saved := durableEvents(t, m, "inst:archive/file/holder-g1")
	require.Len(t, saved, 3, "two finite archive events and the first closed live event")
	require.Contains(t, saved[0].Message, "historical one")
	require.Contains(t, saved[1].Message, "historical two")
	require.Contains(t, saved[2].Message, "current one")
	entry := m.pipes["inst:archive/file/holder-g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "inst:archive/file", SourceGeneration: "holder-g1"})
	require.NotNil(t, entry)
	imported := false
	for _, segment := range entry.Segments {
		if segment.Path == archivePath {
			imported = segment.Imported
			require.Equal(t, uint64(0), segment.StartPos)
		}
	}
	require.True(t, imported)
	before := len(saved)
	m.pollOnce()
	require.Len(t, durableEvents(t, m, "inst:archive/file/holder-g1"), before, "imported gzip must not replay")
	require.NoError(t, m.Stop())
	require.Len(t, durableEvents(t, m, "inst:archive/file/holder-g1"), before+1,
		"graceful stop closes the one pending live event")
	restartedCatalog := catalog.New(cat.Journal())
	restarted, err := New(Options{Root: root, VL: client, Catalog: restartedCatalog, Journal: restartedCatalog.Journal()})
	require.NoError(t, err)
	restarted.pollOnce()
	require.Len(t, durableEvents(t, restarted, "inst:archive/file/holder-g1"), before+1,
		"restart must resume the live file from its segment-local cursor")
	ready := restarted.PrepareCutoverReadiness()
	require.True(t, ready.LedgerReady, "%v", ready.Reasons)
	require.Len(t, durableEvents(t, restarted, "inst:archive/file/holder-g1"), before+1,
		"the closed live boundary and imported gzip must not replay")
}

func TestManagerRotationImportsOnlyUnreadTailBeforeReplacementFile(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	livePath := filepath.Join(logDir, "latest.log")
	first := "[11:00:00] [Server thread/INFO]: already read\n"
	pending := "[11:00:01] [Server thread/INFO]: closed by rotation\n"
	require.NoError(t, os.WriteFile(livePath, []byte(first+pending), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "inst:rotate/file", SourceGeneration: "holder-g1", Path: livePath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:rotate", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	require.Len(t, durableEvents(t, m, "inst:rotate/file/holder-g1"), 1)

	archivePath := filepath.Join(logDir, "2026-09-23-1.log.gz")
	writeRuntimeGzip(t, archivePath, first+pending+"[11:00:02] [Server thread/WARN]: unread rotated tail\n")
	require.NoError(t, os.WriteFile(livePath, []byte(
		"[11:00:03] [Server thread/INFO]: replacement current\n"+
			"[11:00:04] [Server thread/INFO]: replacement boundary\n"), 0o600))
	m.pollOnce()
	saved := durableEvents(t, m, "inst:rotate/file/holder-g1")
	require.Len(t, saved, 4)
	require.Contains(t, saved[0].Message, "already read")
	require.Contains(t, saved[1].Message, "closed by rotation")
	require.Contains(t, saved[2].Message, "unread rotated tail")
	require.Contains(t, saved[3].Message, "replacement current")
	ids := make(map[string]bool)
	for _, event := range saved {
		require.False(t, ids[event.EventID], "logical rotation must not duplicate an event identity")
		ids[event.EventID] = true
	}
}

// 事件体改为追加式磁盘段后，每轮 poll 都会把「完整权威集合」交给 appendEvents。
// 若实现把它整份再写一次，段内容会随轮次成倍膨胀——这正是本测试钉住的回归点。
func TestManagerRepeatedPollKeepsStoredEventSetStable(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(
		"[12:00:01] [Server thread/INFO]: first\n"+
			"[12:00:02] [Server thread/INFO]: second\n"+
			"[12:00:03] [Server thread/INFO]: boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "node:idem", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:idem", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	settled, err := New(Options{Root: root, VL: client, Catalog: catalog.New(cat.Journal()), Journal: cat.Journal()})
	require.NoError(t, err)
	baseline := durableEvents(t, settled, "node:idem/g1")
	require.NotEmpty(t, baseline)
	// 文件无新增时重复轮询必须幂等：集合既不膨胀也不重复。
	settled.pollOnce()
	settled.pollOnce()
	after := durableEvents(t, settled, "node:idem/g1")
	require.Len(t, after, len(baseline),
		"repeated polls must not duplicate already-stored events")
	ids := make(map[string]int, len(after))
	for _, event := range after {
		ids[event.EventID]++
	}
	for id, count := range ids {
		require.Equal(t, 1, count, "event %s stored more than once", id)
	}
}

func TestManagerQuarantinesUnchangedCorruptGzipWithoutRepeatingGap(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	badPath := filepath.Join(logDir, "2026-09-22-1.log.gz")
	require.NoError(t, os.WriteFile(badPath, []byte("not-gzip"), 0o600))
	livePath := filepath.Join(logDir, "latest.log")
	require.NoError(t, os.WriteFile(livePath, []byte(
		"[12:00:00] [Server thread/INFO]: live\n"+
			"[12:00:01] [Server thread/INFO]: boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "inst:bad/file", SourceGeneration: "holder-g1", Path: livePath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:bad", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	key := ledger.SourceKey{LogSourceID: "inst:bad/file", SourceGeneration: "holder-g1"}
	entry := m.pipes[key.String()].Ledger().Get(key)
	require.Len(t, entry.Gaps, 1)
	require.Contains(t, entry.Gaps[0].Reason, "ARCHIVE_GZIP_CORRUPT")
	quarantined := false
	for _, segment := range entry.Segments {
		if segment.Path == badPath {
			quarantined = segment.ImportError != "" && !segment.Imported
		}
	}
	require.True(t, quarantined)
	m.pollOnce()
	entry = m.pipes[key.String()].Ledger().Get(key)
	require.Len(t, entry.Gaps, 1, "an unchanged quarantined object must not add one gap every 250ms")
}

func writeRuntimeGzip(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	zw := gzip.NewWriter(f)
	_, err = zw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
}

// TestGroupEventsByUTCDayFallsBackToIngestTime 锁定：无解析语义时间的真实日志行不得令分组失败
// （否则投递/投影失败 → reclaim 永不推进 → WAL 保留全部事件，真机 64 源曾致 Worker RSS 2GiB）。
func TestGroupEventsByUTCDayFallsBackToIngestTime(t *testing.T) {
	source := SourceConfig{LogSourceID: "s1", SourceGeneration: "g1", StorageNamespace: "ns", UTCDay: "2026-09-20"}
	withoutEventTime := logtypes.Event{
		EventID: "e1", IngestTimeUTC: "2026-09-21T10:00:00Z", EventTimeUTC: "",
	}
	withEventTime := logtypes.Event{
		EventID: "e2", IngestTimeUTC: "2026-09-21T10:00:00Z", EventTimeUTC: "2026-09-22T10:00:00Z",
	}
	grouped, days, err := groupEventsByUTCDay(source, []logtypes.Event{withoutEventTime, withEventTime})
	require.NoError(t, err)
	require.Equal(t, []string{"2026-09-21", "2026-09-22"}, days)
	require.Len(t, grouped["2026-09-21"], 1)
	require.Len(t, grouped["2026-09-22"], 1)

	// 两者皆缺时回退源配置的 UTCDay。
	bare := logtypes.Event{EventID: "e3"}
	grouped, days, err = groupEventsByUTCDay(source, []logtypes.Event{bare})
	require.NoError(t, err)
	require.Equal(t, []string{"2026-09-20"}, days)
	require.Len(t, grouped["2026-09-20"], 1)
}
