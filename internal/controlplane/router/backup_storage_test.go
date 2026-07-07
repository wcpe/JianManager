package router

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestBackupStorage_ListIncludesStats(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{
		"name":     "S3 归档",
		"type":     "s3",
		"endpoint": "s3.local",
		"bucket":   "backups",
	}
	w := makeRequest(r, "POST", "/api/v1/backup-storages", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	storageID := uint(created["id"].(float64))

	require.NoError(t, db.Create(&model.Backup{InstanceID: 1, Name: "bk", StorageID: &storageID, Status: model.BackupStatusCompleted, FileSizeMB: 2}).Error)

	w = makeRequest(r, "GET", "/api/v1/backup-storages", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	items := parseJSONArray(t, w)
	require.Len(t, items, 1)
	item := items[0].(map[string]interface{})
	require.Equal(t, float64(1), item["backupCount"])
	require.Equal(t, float64(2097152), item["usedBytes"])
}

func TestBackupStorage_LocalStats(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	backupDir := filepath.Join(os.TempDir(), "jm-test-local-backups")
	require.NoError(t, os.MkdirAll(backupDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(backupDir, "ignored.tar.gz"), []byte("unused"), 0o644))
	require.NoError(t, db.Create(&model.Backup{InstanceID: 1, Name: "local", Status: model.BackupStatusCompleted, FileSizeMB: 3}).Error)

	w := makeRequest(r, "GET", "/api/v1/backup-storages/local/stats", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	require.Equal(t, float64(1), resp["backupCount"])
}

func TestBackupStorage_TestConnectionNoWorker(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{
		"name":     "DAV 归档",
		"type":     "webdav",
		"endpoint": "https://dav.local/backups",
	}
	w := makeRequest(r, "POST", "/api/v1/backup-storages", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	id := itoa(uint(created["id"].(float64)))

	w = makeRequest(r, "POST", "/api/v1/backup-storages/"+id+"/test", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	require.Equal(t, false, resp["ok"])
	require.Equal(t, "NO_WORKER", resp["errorCode"])
}
