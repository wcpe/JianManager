package lifecycle_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestCanonicalDayMigrationAdapterUsesOneFrozenPrefixAcrossThreeStarts(t *testing.T) {
	root := t.TempDir()
	source := logtypes.SourceIdentity{LogSourceID: "inst:7/file", SourceGeneration: "g1", ParserVersion: "parser/v1"}
	event := func(start, end uint64, when, msg string) logtypes.Event {
		return logtypes.BuildEvent(source, logtypes.RecordRange{Start: start, End: end}, when, when, "INFO", "stdout", msg)
	}
	events := []logtypes.Event{event(0, 10, "2026-10-20T12:00:00Z", "已捕获正文"), event(10, 20, "2026-10-20T13:00:00Z", "同日未捕获尾部"), event(20, 30, "2026-10-21T00:00:00Z", "另一天正文")}
	store, err := eventstore.Open(filepath.Join(root, "var", "log", "events"))
	require.NoError(t, err)
	require.NoError(t, store.Append(source.LogSourceID+"/"+source.SourceGeneration, events))
	require.NoError(t, store.Close())
	member := catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: "inst:7", UTCDay: "2026-10-20"}, catalog.OwnerHot, 1, "hot")
	member.PublishedProjection = &catalog.PublishedProjection{CoverageComplete: true, SourceProjections: []catalog.SourceProjection{{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration}, ProjectionGenerations: []string{"original"}, ClosedVisibleSeq: 10}}}
	for round := 1; round <= 3; round++ {
		manager, err := ingest.New(ingest.Options{Root: root, Catalog: catalog.New(nil)})
		require.NoError(t, err)
		got, err := manager.CanonicalDayMigrationInput(member)
		require.NoError(t, err)
		require.Len(t, got, 1, "第%d启不得混入同日新尾部或另一天", round)
		require.Equal(t, events[0], got[0])
		missing := member.Clone()
		missing.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 15
		_, err = manager.CanonicalDayMigrationInput(missing)
		require.Error(t, err, "缺少捕获前缀输入不能用较短canonical集合伪装完成")
		require.NoError(t, manager.Stop())
	}
}
