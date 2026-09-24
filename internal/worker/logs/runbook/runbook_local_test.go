// Package runbook — T11 本地 Runbook 证据（不替代远程真机验收）。
package runbook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// Runbook A：ACK 丢失 → 不可 reclaim；责任转移后可 reclaim；非法 reason 拒绝。
func TestRunbookA_WALAckLossAndReclaimGate(t *testing.T) {
	led := ledger.New()
	key := ledger.SourceKey{LogSourceID: "runbook-a", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "runbook-a", SourceGeneration: "g1", ParserVersion: "1.0.0"})
	require.NoError(t, led.AdvanceDurable(key, 100))
	require.NoError(t, led.RecordDelivery(key, 0, 50, logtypes.DeliveryUnknown))
	_, err := led.TryReclaim(key)
	require.Error(t, err)

	require.NoError(t, led.RegisterRecovery(key, ledger.RecoveryRef{
		SegmentID: "seg", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 100,
	}))
	require.Error(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryReleased, "NOT_A_REASON", "recv"))
	require.NoError(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryWALResponsibilityXfer, "", "recv-A"))
	n, err := led.TryReclaim(key)
	require.NoError(t, err)
	assert.EqualValues(t, 100, n)
}

// Runbook B：迁移状态 Recover；staging 不可查询。
func TestRunbookB_CatalogCrashRecovery(t *testing.T) {
	key := catalog.PartitionKey{StorageNamespace: "ns", UTCDay: "20260921"}
	for _, st := range []catalog.MigrationState{
		catalog.StateRoutingFrozen,
		catalog.StateDraining,
		catalog.StateSnapshotting,
		catalog.StateStagingVerify,
		catalog.StateAttachedStaging,
		catalog.StateOwnerSwitched,
		catalog.StateQueryLeaseDraining,
		catalog.StateDetached,
	} {
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
		rec.MigrationState = st
		if st == catalog.StateAttachedStaging {
			rec.TargetDirID = "cold-staging"
		}
		if st == catalog.StateOwnerSwitched || st == catalog.StateQueryLeaseDraining || st == catalog.StateDetached {
			rec.Owner = catalog.OwnerCold
			rec.Generation = 2
		}
		view := catalog.Recover(rec)
		require.NotNil(t, view)
		if st == catalog.StateAttachedStaging {
			require.False(t, catalog.StagingQueryable(rec))
		}
	}
}

func TestRunbookB_PersistentJournalFiltersReattachedResidual(t *testing.T) {
	dir := t.TempDir()
	store, err := catalog.NewJSONLFileStore(filepath.Join(dir, "catalog.jsonl"))
	require.NoError(t, err)
	journal, err := catalog.NewJournalWithStore(store)
	require.NoError(t, err)
	key := catalog.PartitionKey{StorageNamespace: "node:reattach", UTCDay: "2026-09-22"}
	projection := &catalog.PublishedProjection{
		ManifestVersion: "projection-1", CoverageComplete: true,
		QueryLocationDirID: "hot-g1", QueryGeneration: 1,
		ProjectionGeneration: "projection-1",
		ClosedVisibleSeq:     map[string]uint64{key.String(): 10},
	}
	require.NoError(t, journal.Append(catalog.JournalEntry{
		Seq: 1, Key: key, State: catalog.StateOwnerSwitched, Owner: catalog.OwnerHot,
		Generation: 1, DirID: "hot-g1", AuthorityCommit: true, JournalComplete: true,
		PublishedProjection: projection,
		Dirs:                []catalog.PhysicalDir{{ID: "hot-g1", Role: catalog.DirOwner}},
		ResidualDirs:        []catalog.PhysicalDir{{ID: "old-hot-reattach", Role: catalog.DirResidual}},
	}))

	replayed, err := catalog.NewJournalWithStore(store)
	require.NoError(t, err)
	cat := catalog.New(replayed)
	views := cat.StartingRecover()
	view := views[key]
	require.True(t, view.Queryable)
	require.Equal(t, catalog.OwnerHot, view.Owner)
	require.Contains(t, view.ExcludedQueryDirs, "old-hot-reattach")
	require.True(t, cat.IsQueryDirVisible(key, "hot-g1"))
	require.False(t, cat.IsQueryDirVisible(key, "old-hot-reattach"))
	ref, ok := cat.OwnerForQuery(key)
	require.True(t, ok)
	require.Equal(t, "hot-g1", ref.DirID)
}

// Runbook C：资产校验、loopback-only 客户端、防递归 sink。
func TestRunbookC_SupervisorGuards(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "victoria-logs-fake")
	require.NoError(t, os.WriteFile(exe, []byte("fake"), 0o755))
	require.Error(t, vlsup.VerifyAsset(exe, "deadbeef"))

	_, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: "http://example.com:9428", Username: "u", Password: "p"})
	require.Error(t, err)

	assert.False(t, vlsup.IndependentLoggerSink{}.WritesIntoVL())
}

// 装配：UnimplementedRangeClient 不宣称 search 能力。
func TestAssemble_QueryStackCapabilities(t *testing.T) {
	cat := catalog.New(nil)
	planner := query.NewPlanner(cat, nil)
	qs := query.NewService(planner, nil, "runbook-build")
	caps := qs.GetCapabilities(query.CapabilitiesRequest{ProtocolVersion: query.DefaultProtocolVersion})
	assert.True(t, caps.Supported)
	for _, c := range caps.Capabilities {
		assert.NotEqual(t, "search", c)
	}
}

// 管道配置烟测：FILE_PRIMARY 默认。
func TestPipeline_DefaultMode(t *testing.T) {
	require.Equal(t, pipeline.ModeFilePrimary, pipeline.DefaultMode())
	require.True(t, pipeline.ModeFilePrimary.Valid())
}
