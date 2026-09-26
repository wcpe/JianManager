package ingest

import (
	"os"
	"path/filepath"
	"strings"
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

func TestInstanceOutputSpoolsBeforeBindingAndFlushesInOrder(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	m, err := New(Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)

	err = m.AppendInstanceOutput("uuid-spool", "stdout", []byte("before-1\n"))
	require.ErrorIs(t, err, ErrInstanceBindingPending)
	err = m.AppendInstanceOutput("uuid-spool", "stderr", []byte("error-before\n"))
	require.ErrorIs(t, err, ErrInstanceBindingPending)
	require.FileExists(t, m.pendingInstancePath("uuid-spool", "stdout"))
	require.FileExists(t, m.pendingInstancePath("uuid-spool", "stderr"))
	readiness := m.CutoverReadiness()
	require.False(t, readiness.LedgerReady)
	require.Contains(t, strings.Join(readiness.Reasons, ","), "pending_spool:", "%v", readiness.Reasons)

	require.NoError(t, m.RegisterInstance("uuid-spool", "inst:spool", "holder-g1", "STDIO_PRIMARY", work))
	require.NoError(t, m.AppendInstanceOutput("uuid-spool", "stdout", []byte("after-1\n")))
	binding := m.state.Instances["uuid-spool"]
	stdout, err := os.ReadFile(m.rawInstancePath(binding, "stdout"))
	require.NoError(t, err)
	stderr, err := os.ReadFile(m.rawInstancePath(binding, "stderr"))
	require.NoError(t, err)
	require.Equal(t, "before-1\nafter-1\n", string(stdout))
	require.Equal(t, "error-before\n", string(stderr))
	require.NoFileExists(t, m.pendingInstancePath("uuid-spool", "stdout"))
	require.NoFileExists(t, m.pendingInstancePath("uuid-spool", "stderr"))
}

func TestInstanceOutputPendingSpoolSurvivesRestart(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	first, err := New(Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)
	require.ErrorIs(t, first.AppendInstanceOutput("uuid-restart", "stdout", []byte("survives restart\n")), ErrInstanceBindingPending)
	require.NoError(t, first.Stop())

	restarted, err := New(Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)
	require.NoError(t, restarted.RegisterInstance("uuid-restart", "inst:restart", "holder-g1", "STDIO_PRIMARY", work))
	binding := restarted.state.Instances["uuid-restart"]
	data, err := os.ReadFile(restarted.rawInstancePath(binding, "stdout"))
	require.NoError(t, err)
	require.Equal(t, "survives restart\n", string(data))
}

func TestInstanceOutputPendingFlushFailureKeepsGapAndPause(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	m, err := New(Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)
	require.ErrorIs(t, m.AppendInstanceOutput("uuid-fail", "stdout", []byte("must-not-disappear\n")), ErrInstanceBindingPending)
	binding := InstanceBinding{UUID: "uuid-fail", Generation: "holder-g1"}
	rawPath := m.rawInstancePath(binding, "stdout")
	require.NoError(t, os.MkdirAll(filepath.Dir(rawPath), 0o700))
	require.NoError(t, os.Mkdir(rawPath, 0o700))

	err = m.RegisterInstance("uuid-fail", "inst:fail", "holder-g1", "STDIO_PRIMARY", work)
	require.Error(t, err)
	pipe := m.pipes["inst:fail/stdout/holder-g1"]
	require.NotNil(t, pipe)
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.True(t, entry.AcquirePaused)
	require.Len(t, entry.Gaps, 1)
	require.Equal(t, "STDIO_RAW_WRITE_FAILED", entry.Gaps[0].Reason)
	require.False(t, m.CutoverReadiness().LedgerReady)
}

func TestInstanceOutputBindingRacePreservesAllBytes(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	m, err := New(Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)
	const count = 32
	chunk := []byte("concurrent-output\n")
	start := make(chan struct{})
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			<-start
			errs <- m.AppendInstanceOutput("uuid-race", "stdout", chunk)
		}()
	}
	close(start)
	require.NoError(t, m.RegisterInstance("uuid-race", "inst:race", "holder-g1", "STDIO_PRIMARY", work))
	for i := 0; i < count; i++ {
		err := <-errs
		if err != nil {
			require.ErrorIs(t, err, ErrInstanceBindingPending)
		}
	}
	binding := m.state.Instances["uuid-race"]
	data, err := os.ReadFile(m.rawInstancePath(binding, "stdout"))
	require.NoError(t, err)
	require.Len(t, data, count*len(chunk))
}
