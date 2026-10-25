package lifecycle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestControlledDayRepairUnknownWriteOnlyReadsUntilProved(t *testing.T) {
	key := catalog.PartitionKey{StorageNamespace: "residual", UTCDay: "2026-10-20"}
	identity := logtypes.SourceIdentity{LogSourceID: "residual-source", SourceGeneration: "g1", ParserVersion: "parser/v1"}
	events := []logtypes.Event{logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 0, End: 10}, "2026-10-20T12:00:00Z", "2026-10-20T12:01:00Z", "INFO", "stdout", "一"), logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 10, End: 20}, "2026-10-20T12:00:01Z", "2026-10-20T12:01:00Z", "INFO", "stdout", "二")}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot")
	rec.PublishedProjection = &catalog.PublishedProjection{CoverageComplete: true, ManifestVersion: "hot", QueryLocationDirID: "hot", QueryGeneration: 1, SourceProjections: []catalog.SourceProjection{{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: identity.LogSourceID, SourceGeneration: identity.SourceGeneration}, ProjectionGenerations: []string{"hot-original"}, ClosedVisibleSeq: 20}}}
	cat := catalog.New(nil)
	require.NoError(t, cat.Put(rec))
	_, err := cat.BeginDayMigration(key.UTCDay, catalog.OwnerCold, "20261020")
	require.NoError(t, err)
	for _, state := range []catalog.MigrationState{catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify} {
		_, err = cat.AdvanceDay(key.UTCDay, state)
		require.NoError(t, err)
	}
	hotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, event := range events {
			require.NoError(t, json.NewEncoder(w).Encode(repairPayload(event, "hot-original")))
		}
	}))
	defer hotServer.Close()
	var mu sync.Mutex
	var rows []map[string]any
	var inserts atomic.Int64
	coldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/internal/partition/list":
			require.NoError(t, json.NewEncoder(w).Encode([]string{"20261020"}))
		case "/internal/force_flush":
		case "/select/logsql/query":
			for _, row := range rows {
				require.NoError(t, json.NewEncoder(w).Encode(row))
			}
		case "/insert/jsonline":
			inserts.Add(1)
			decoder := json.NewDecoder(r.Body)
			var first map[string]any
			require.NoError(t, decoder.Decode(&first))
			rows = append(rows, first)
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer coldServer.Close()
	hot, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: hotServer.URL})
	require.NoError(t, err)
	cold, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: coldServer.URL})
	require.NoError(t, err)
	physical, err := NewVLPartitionOps(hot, cold, t.TempDir(), t.TempDir(), cat)
	require.NoError(t, err)
	physical.ResourceBudget = testCatalogBudget(t)
	physical.CanonicalDayInput = func(*catalog.Record) ([]logtypes.Event, error) { return events, nil }
	day, _ := cat.PhysicalDay(key.UTCDay)
	req := DayRepairRequest{UTCDay: key.UTCDay, BatchID: day.Batches[0].ID, TargetDirID: day.TargetDirID, Members: day.Batches[0].Members}
	_, err = physical.repairDay(req)
	require.Error(t, err)
	day, _ = cat.PhysicalDay(key.UTCDay)
	plan := day.Batches[0].RepairSources[0]
	require.Equal(t, 2, plan.AttemptedThrough)
	require.Zero(t, plan.WrittenThrough)
	require.False(t, plan.Verified)
	_, err = physical.repairDay(req)
	require.ErrorContains(t, err, "still uncertain")
	require.EqualValues(t, 1, inserts.Load(), "未知旧请求未被证明结束前不能盲目制造物理副本")
	mu.Lock()
	for _, event := range events {
		if rows[0]["event_id"] != event.EventID {
			rows = append(rows, repairPayload(event, plan.ProjectionGeneration))
		}
	}
	mu.Unlock()
	result, err := physical.repairDay(req)
	require.NoError(t, err)
	require.NotEmpty(t, result.Checksum)
	require.EqualValues(t, 1, inserts.Load(), "完整结果恢复可见后应只读取证并复用原代次")
	day, _ = cat.PhysicalDay(key.UTCDay)
	require.Equal(t, plan.ProjectionGeneration, day.Batches[0].RepairSources[0].ProjectionGeneration)
	require.True(t, day.Batches[0].RepairSources[0].Verified)
	original, _ := cat.Get(key)
	require.Equal(t, catalog.OwnerHot, original.Owner, "接收凭据本身不允许提前改写Catalog权威")
	physical.CanonicalDayInput = func(*catalog.Record) ([]logtypes.Event, error) {
		changed := append([]logtypes.Event(nil), events...)
		changed[0].Message = "变更输入"
		return changed, nil
	}
	_, err = physical.repairDay(req)
	require.Error(t, err)
	require.EqualValues(t, 1, inserts.Load())
}

func TestControlledDayRepairMissingBudgetRejectsBeforeWriting(t *testing.T) {
	physical := &VLPartitionOps{}
	require.ErrorContains(t, physical.reserveBytes("repair/2026-10-20", 1), "budget unavailable")
}
