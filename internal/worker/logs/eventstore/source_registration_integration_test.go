package eventstore_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	worker "github.com/wcpe/JianManager/internal/worker"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 运维合法配置经真实加载与登记后可到达旧有损命名碰撞；不把它夸大为未认证越权。
func TestLegalConfiguredSourcesRegisterAndKeepSeparateEventSegments(t *testing.T) {
	root := t.TempDir()
	ids := []string{"inst:41/a-b", "inst:41/a:b"}
	paths := []string{filepath.Join(root, "first.log"), filepath.Join(root, "second.log")}
	for _, path := range paths {
		require.NoError(t, os.WriteFile(path, nil, 0o600))
	}
	configPath := filepath.Join(root, "worker.yml")
	configBody := fmt.Sprintf(`name: collision-regression
data_dir: %q
log_sources:
  - log_source_id: %q
    source_generation: g1
    storage_namespace: inst:41
    source_category: instance
    mode: FILE_PRIMARY
    path: %q
  - log_source_id: %q
    source_generation: g1
    storage_namespace: inst:41
    source_category: instance
    mode: FILE_PRIMARY
    path: %q
`, filepath.Join(root, "worker-data"), ids[0], paths[0], ids[1], paths[1])
	require.NoError(t, os.WriteFile(configPath, []byte(configBody), 0o600))
	config, err := worker.Load(configPath)
	require.NoError(t, err, "碰撞候选必须通过真实配置加载校验，而非任意内部键")
	require.Len(t, config.LogSources, 2)
	managerRoot := filepath.Join(root, "managed")
	cat := catalog.New(catalog.NewMemJournal())
	manager, err := ingest.New(ingest.Options{Root: managerRoot, Catalog: cat, Journal: cat.Journal(), Reconcile: &ingest.ReconcileConfig{Enabled: false}})
	require.NoError(t, err)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = manager.Stop()
		}
	})
	keys := make([]string, 0, len(config.LogSources))
	for _, configured := range config.LogSources {
		source := ingest.SourceConfig{
			LogSourceID: configured.LogSourceID, SourceGeneration: configured.SourceGeneration,
			Path: configured.Path, Mode: pipeline.AcquireMode(configured.Mode),
			SourceCategory: logtypes.Source(configured.SourceCategory), StorageNamespace: configured.StorageNamespace,
			UTCDay: "2026-10-22",
		}
		require.NoError(t, manager.Register(source), "合法配置源必须经真实注册入口接纳")
		keys = append(keys, source.LogSourceID+"/"+source.SourceGeneration)
	}
	require.NoError(t, manager.Stop())
	stopped = true
	legacyName := strings.NewReplacer("/", "_", "\\", "_", ":", "-")
	require.Equal(t, legacyName.Replace(keys[0]), legacyName.Replace(keys[1]), "确认旧有损命名确实冲突")
	eventsRoot := filepath.Join(managerRoot, "var", "log", "events")
	store, err := eventstore.Open(eventsRoot)
	require.NoError(t, err)
	for i, key := range keys {
		event := logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: ids[i], SourceGeneration: "g1", ParserVersion: "acquire-v1"},
			logtypes.RecordRange{Start: 0, End: uint64(i + 1)},
			"2026-10-22T00:00:00Z", "2026-10-22T00:00:01Z", "INFO", "stdout", fmt.Sprintf("source-%d", i),
		)
		require.NoError(t, store.Append(key, []logtypes.Event{event}))
	}
	require.NoError(t, store.Close())
	for round := 0; round < 3; round++ {
		reopened, err := eventstore.Open(eventsRoot)
		require.NoError(t, err)
		for i, key := range keys {
			snapshot, err := reopened.Snapshot(key)
			require.NoError(t, err)
			require.Equal(t, key, snapshot.Manifest().Key)
			require.Equal(t, 1, snapshot.Count())
			got := 0
			require.NoError(t, snapshot.Iterate(func(event logtypes.Event) error {
				got++
				require.Equal(t, ids[i], event.Source.LogSourceID)
				require.Equal(t, fmt.Sprintf("source-%d", i), event.Message)
				return nil
			}))
			require.Equal(t, 1, got)
			require.NoError(t, snapshot.Close())
		}
		require.NoError(t, reopened.Close())
		t.Logf("合法配置与登记链轮次=%d 两源各保留一事件；旧目录同名=%s", round+1, legacyName.Replace(keys[0]))
	}
	entries, err := os.ReadDir(eventsRoot)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	manifestKeys := make(map[string]bool)
	for _, entry := range entries {
		require.True(t, strings.HasPrefix(entry.Name(), "source-"))
		body, err := os.ReadFile(filepath.Join(eventsRoot, entry.Name(), "MANIFEST.json"))
		require.NoError(t, err)
		var manifest eventstore.Manifest
		require.NoError(t, json.Unmarshal(body, &manifest))
		manifestKeys[manifest.Key] = true
	}
	require.Equal(t, map[string]bool{keys[0]: true, keys[1]: true}, manifestKeys)
}
