package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

type failingAuditExportWriter struct {
	header http.Header
	status int
}

func (w *failingAuditExportWriter) Header() http.Header {
	return w.header
}

func (w *failingAuditExportWriter) WriteHeader(status int) {
	w.status = status
}

func (w *failingAuditExportWriter) Write([]byte) (int, error) {
	return 0, errors.New("写入失败")
}

func seedAuditRows(t *testing.T, dbUserID uint, count int, db func(*model.AuditLog) error) {
	t.Helper()
	for i := 0; i < count; i++ {
		require.NoError(t, db(&model.AuditLog{
			UserID:     dbUserID,
			Action:     "instance.start",
			TargetType: "instance",
			TargetID:   itoa(uint(i + 1)),
			Detail:     `{"ok":true}`,
			IP:         "127.0.0.1",
		}))
	}
}

func TestAudit_List_LegacyArrayAndPaginatedEnvelope(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	var user model.User
	require.NoError(t, db.Where("username = ?", "admin").First(&user).Error)
	seedAuditRows(t, user.ID, 3, func(log *model.AuditLog) error { return db.Create(log).Error })

	legacy := makeRequest(r, http.MethodGet, "/api/v1/audit?limit=2", nil, token)
	require.Equal(t, http.StatusOK, legacy.Code)
	assert.Len(t, parseJSONArray(t, legacy), 2)

	paged := makeRequest(r, http.MethodGet, "/api/v1/audit?page=1&pageSize=2", nil, token)
	require.Equal(t, http.StatusOK, paged.Code)
	resp := parseJSON(t, paged)
	assert.Equal(t, float64(1), resp["page"])
	assert.Equal(t, float64(2), resp["pageSize"])
	assert.Equal(t, float64(3), resp["total"])
	items, ok := resp["items"].([]interface{})
	require.True(t, ok)
	assert.Len(t, items, 2)

	clamped := makeRequest(r, http.MethodGet, "/api/v1/audit?page=1&pageSize=999", nil, token)
	require.Equal(t, http.StatusOK, clamped.Code)
	assert.Equal(t, float64(200), parseJSON(t, clamped)["pageSize"])
}

func TestAudit_Export_NDJSON(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	var user model.User
	require.NoError(t, db.Where("username = ?", "admin").First(&user).Error)
	seedAuditRows(t, user.ID, 205, func(log *model.AuditLog) error { return db.Create(log).Error })

	w := makeRequest(r, http.MethodGet, "/api/v1/audit/export?pageSize=2&limit=1", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/x-ndjson", w.Header().Get("Content-Type"))
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	require.Len(t, lines, 205)
	for _, line := range lines {
		var exported map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &exported))
		assert.Equal(t, "instance.start", exported["action"])
		assert.NotContains(t, exported, "detail")
		assert.NotContains(t, exported, "uuid")
	}

	var exportLog model.AuditLog
	require.NoError(t, db.Where("action = ?", "audit.export").First(&exportLog).Error)
	var detail map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(exportLog.Detail), &detail))
	assert.Equal(t, "success", detail["status"])
	assert.Equal(t, "ndjson", detail["format"])
}

func TestAudit_Export_RejectsUnsupportedFormat(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodGet, "/api/v1/audit/export?format=csv", nil, token)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "UNSUPPORTED_FORMAT")
}

func TestAudit_Export_RecordFailureAuditOnWriteError(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	var user model.User
	require.NoError(t, db.Where("username = ?", "admin").First(&user).Error)
	seedAuditRows(t, user.ID, 1, func(log *model.AuditLog) error { return db.Create(log).Error })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/export", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := &failingAuditExportWriter{header: http.Header{}}
	r.ServeHTTP(w, req)

	var exportLog model.AuditLog
	require.NoError(t, db.Where("action = ?", "audit.export").First(&exportLog).Error)
	var detail map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(exportLog.Detail), &detail))
	assert.Equal(t, "failure", detail["status"])
	assert.Equal(t, "ndjson", detail["format"])
}
