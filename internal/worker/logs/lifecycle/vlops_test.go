package lifecycle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestVLPartitionOpsMigratesVerifiedPartitionAndCleansOnlyAfterDetach(t *testing.T) {
	hotRoot, coldRoot := filepath.Join(t.TempDir(), "hot", "data"), filepath.Join(t.TempDir(), "cold", "data")
	day := "20260923"
	snapshot := filepath.Join(hotRoot, "partitions", day, "snapshots", "snapshot-1")
	require.NoError(t, os.MkdirAll(snapshot, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(snapshot, "part.bin"), []byte("verified partition"), 0o600))
	var mu sync.Mutex
	var calls []string
	hotActive, coldActive := true, false
	hotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, "hot:"+r.URL.Path)
		if r.URL.Path == "/internal/partition/detach" {
			hotActive = false
		}
		active := hotActive
		mu.Unlock()
		require.Equal(t, http.MethodPost, r.Method)
		switch r.URL.Path {
		case "/internal/partition/list":
			if active {
				_ = json.NewEncoder(w).Encode([]string{day})
			} else {
				_ = json.NewEncoder(w).Encode([]string{})
			}
		case "/internal/partition/snapshot/create":
			require.Equal(t, day, r.URL.Query().Get("partition_prefix"))
			_ = json.NewEncoder(w).Encode([]string{snapshot})
		}
	}))
	defer hotServer.Close()
	coldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, "cold:"+r.URL.Path)
		if r.URL.Path == "/internal/partition/attach" {
			coldActive = true
		}
		active := coldActive
		mu.Unlock()
		require.Equal(t, http.MethodPost, r.Method)
		if r.URL.Path == "/internal/partition/list" {
			if active {
				_ = json.NewEncoder(w).Encode([]string{day})
			} else {
				_ = json.NewEncoder(w).Encode([]string{})
			}
		} else {
			require.Equal(t, day, r.URL.Query().Get("name"))
		}
	}))
	defer coldServer.Close()
	hot, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: hotServer.URL})
	require.NoError(t, err)
	cold, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: coldServer.URL})
	require.NoError(t, err)
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-23"}
	require.NoError(t, cat.Put(catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")))
	physical, err := NewVLPartitionOps(hot, cold, hotRoot, coldRoot, cat)
	require.NoError(t, err)
	manager := New(cat, physical.Ops())
	manager.SetRetentionGate(physical)
	result, err := manager.StartMigration(key.StorageNamespace, key.UTCDay, catalog.OwnerCold, day)
	require.NoError(t, err)
	require.Equal(t, catalog.StateCleaned, result.Record.MigrationState)
	require.Equal(t, catalog.OwnerCold, result.Record.Owner)
	require.FileExists(t, filepath.Join(coldRoot, "partitions", day, "part.bin"))
	require.NoDirExists(t, filepath.Join(hotRoot, "partitions", day))
	require.False(t, cat.IsQueryDirVisible(key, "hot-g1"))
	require.True(t, cat.IsQueryDirVisible(key, day))
	require.Contains(t, calls, "hot:/internal/force_flush")
	require.Contains(t, calls, "hot:/internal/partition/detach")
	require.Contains(t, calls, "cold:/internal/partition/attach")
}
