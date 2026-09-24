package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

func TestInstanceStdioPersistsRawBeforePollingAndRestoresBindings(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(nil)
	m, err := New(Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	require.NoError(t, m.RegisterInstance("uuid-1", "inst:1", "holder-g1", "STDIO_PRIMARY", t.TempDir()))
	require.NoError(t, m.AppendInstanceOutput("uuid-1", "stdout", []byte("stdout raw\n")))
	require.NoError(t, m.AppendInstanceOutput("uuid-1", "stderr", []byte("stderr raw\n")))
	data, err := os.ReadFile(m.rawInstancePath(m.state.Instances["uuid-1"], "stdout"))
	require.NoError(t, err)
	require.Equal(t, "stdout raw\n", string(data))
	require.Empty(t, cat.Keys(), "no query publication before polling")
	m.pollOnce()
	rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
	require.Len(t, rec.PublishedProjection.SourceProjections, 2)
	restarted, err := New(Options{Root: root, VL: vl, Catalog: catalog.New(cat.Journal()), Journal: cat.Journal()})
	require.NoError(t, err)
	require.Len(t, restarted.pipes, 2, "sources restore without CP or config replay")
	require.NoError(t, restarted.AppendInstanceOutput("uuid-1", "stdout", []byte("after restart\n")))
	restarted.pollOnce()
	require.Len(t, durableEvents(t, restarted, "inst:1/stdout/holder-g1"), 2)
	require.Len(t, durableEvents(t, restarted, "inst:1/stderr/holder-g1"), 1)
	require.NoError(t, os.Remove(restarted.rawInstancePath(restarted.state.Instances["uuid-1"], "stdout")))
	require.Error(t, restarted.AppendInstanceOutput("uuid-1", "stdout", []byte("not captured\n")))
	require.False(t, restarted.CutoverReadiness().LedgerReady)
	require.ErrorContains(t, restarted.ResolveCoveredGaps(), "projection alone cannot resolve")
}

func TestInstanceFilePrimaryDoesNotDoubleCollectConsoleOutput(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(work, "logs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(work, "logs", "latest.log"), []byte("file canonical\n"), 0o600))
	cat := catalog.New(nil)
	m, err := New(Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	require.NoError(t, m.RegisterInstance("uuid-1", "inst:1", "holder-g1", "FILE_PRIMARY", work))
	require.Equal(t, []string{"unbound"}, m.MissingInstanceBindings([]string{"uuid-1", "unbound"}))
	require.NoError(t, m.RegisterInstance("uuid-1", "inst:1", "holder-g1", "FILE_PRIMARY", work))
	require.NoError(t, m.AppendInstanceOutput("uuid-1", "stdout", []byte("console copy\n")))
	m.pollOnce()
	require.Len(t, m.pipes, 1)
	events := durableEvents(t, m, "inst:1/file/holder-g1")
	require.Len(t, events, 1)
	require.Equal(t, "file canonical", events[0].Message)
}
