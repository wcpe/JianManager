package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestBackupStorage_TestCandidateAndSaved(t *testing.T) {
	t.Setenv("JM_TEST_BK_AK", "ak")
	t.Setenv("JM_TEST_BK_SK", "sk")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodHead, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	body := map[string]any{
		"name": "s3", "type": "s3", "endpoint": server.URL, "bucket": "b",
		"accessKeyEnv": "${JM_TEST_BK_AK}", "secretKeyEnv": "${JM_TEST_BK_SK}",
	}

	testOnly := makeRequest(r, http.MethodPost, "/api/v1/backup-storages/test", body, token)
	require.Equal(t, http.StatusOK, testOnly.Code)
	testOnlyBody := parseJSON(t, testOnly)
	assert.True(t, testOnlyBody["ok"].(bool))
	assert.GreaterOrEqual(t, testOnlyBody["latencyMs"].(float64), float64(0))

	created := makeRequest(r, http.MethodPost, "/api/v1/backup-storages", body, token)
	require.Equal(t, http.StatusCreated, created.Code)
	id := uint(parseJSON(t, created)["id"].(float64))

	saved := makeRequest(r, http.MethodPost, "/api/v1/backup-storages/"+itoa(id)+"/test", nil, token)
	require.Equal(t, http.StatusOK, saved.Code)
	savedBody := parseJSON(t, saved)
	assert.True(t, savedBody["ok"].(bool))
	assert.GreaterOrEqual(t, savedBody["latencyMs"].(float64), float64(0))

	list := makeRequest(r, http.MethodGet, "/api/v1/backup-storages", nil, token)
	require.Equal(t, http.StatusOK, list.Code)
	rows := parseJSONArray(t, list)
	require.Len(t, rows, 1)
	row := rows[0].(map[string]any)
	assert.NotEmpty(t, row["lastTestAt"])
	assert.Equal(t, true, row["lastTestOk"])
	assert.Equal(t, float64(0), row["backupCount"])
	assert.Equal(t, float64(0), row["usedBytes"])
}
