package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectionWritersCannotOverwriteAnotherCommittedManifest(t *testing.T) {
	cat := New(nil)
	key := PartitionKey{StorageNamespace: "shared", UTCDay: "2026-09-23"}
	rec := NewStableRecord(key, OwnerHot, 1, "hot")
	require.NoError(t, cat.Put(rec))
	expected, _ := cat.Get(key)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, version := range []string{"a", "b"} {
		next := expected.Clone()
		next.PublishedProjection = &PublishedProjection{ManifestVersion: version, QueryLocationDirID: "hot", QueryGeneration: 1}
		go func() { <-start; results <- cat.PublishProjection(expected, next) }()
	}
	close(start)
	first, second := <-results, <-results
	require.True(t, (first == nil) != (second == nil), "exactly one writer may commit against the same base")
	got, _ := cat.Get(key)
	replayed, _ := New(cat.Journal()).Get(key)
	require.Equal(t, got.PublishedProjection.ManifestVersion, replayed.PublishedProjection.ManifestVersion)
	require.EqualValues(t, 1, cat.Journal().LastSeq())
}
