package router

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func setupClientDistExportRouter(t *testing.T, db *gorm.DB, maxRows int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	exportSvc := service.NewClientDistExportService(db, service.NewClientDistObservabilityService(db))
	exportSvc.SetMaxRowsForTest(maxRows)
	return Setup(&Services{
		Auth:             service.NewAuthService(db, jwtCfg),
		Authz:            service.NewAuthzService(db),
		Audit:            service.NewAuditService(db),
		ClientDistExport: exportSvc,
	}, jwtCfg.Secret)
}

func TestClientDistExport_RBACAndRateLimit(t *testing.T) {
	db := setupTestDB(t)
	r := setupClientDistExportRouter(t, db, 10000)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "export-member", "password123")

	forbidden := makeRequest(r, http.MethodGet, "/api/v1/client-dist/export?kind=stats-summary&range=7d", nil, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code, forbidden.Body.String())

	first := makeRequest(r, http.MethodGet, "/api/v1/client-dist/export?kind=stats-summary&range=7d", nil, adminToken)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	limited := makeRequest(r, http.MethodGet, "/api/v1/client-dist/export?kind=stats-summary&range=7d", nil, adminToken)
	require.Equal(t, http.StatusTooManyRequests, limited.Code, limited.Body.String())
	require.Equal(t, "60", limited.Header().Get("Retry-After"))
}

func TestClientDistExport_MasksSensitiveFieldsAndAudits(t *testing.T) {
	db := setupTestDB(t)
	r := setupClientDistExportRouter(t, db, 10000)
	token := getAdminToken(t, r)
	now := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Create(&model.ClientSecurityHello{
		ChannelID: "stable", MachineID: "abcdef1234567890", InstallID: "install1234567890",
		PlayerName: "VeryLongPlayerNameHere", Accepted: true, IP: "192.0.2.1", CreatedAt: now,
		PayloadJSON: `{"playerName":"VeryLongPlayerNameHere","machineId":"abcdef1234567890","installId":"install1234567890","token":"secret-value"}`,
	}).Error)
	var sourceCount int64
	require.NoError(t, db.Model(&model.ClientSecurityHello{}).Where("created_at >= ? AND created_at < ?", now.Add(-7*24*time.Hour), time.Now().UTC()).Count(&sourceCount).Error)
	require.Equal(t, int64(1), sourceCount)

	w := makeRequest(r, http.MethodGet, "/api/v1/client-dist/export?kind=security-logs&range=7d&channelId=stable", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "text/csv; charset=utf-8", w.Header().Get("Content-Type"))
	require.True(t, strings.HasPrefix(w.Body.String(), "\ufeffid,type,title"))
	require.Contains(t, w.Body.String(), "VeryLongPlayerN…")
	require.Contains(t, w.Body.String(), "abcdef…7890")
	require.Contains(t, w.Body.String(), "instal…7890")
	require.NotContains(t, w.Body.String(), "VeryLongPlayerNameHere")
	require.NotContains(t, w.Body.String(), "abcdef1234567890")
	require.NotContains(t, w.Body.String(), "secret-value")

	var audit model.AuditLog
	require.NoError(t, db.Where("action = ?", "client_dist.export.csv").First(&audit).Error)
	require.Contains(t, audit.Detail, `"kind":"security-logs"`)
}

func TestClientDistExport_TruncatesAtConfiguredLimit(t *testing.T) {
	db := setupTestDB(t)
	r := setupClientDistExportRouter(t, db, 2)
	token := getAdminToken(t, r)
	now := time.Now().UTC().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		require.NoError(t, db.Create(&model.ClientDistEvent{
			ChannelID: "stable", MachineID: "abcdef1234567890", IP: "192.0.2.1", Kind: "manifest",
			Status: http.StatusOK, CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}).Error)
	}
	var sourceCount int64
	require.NoError(t, db.Model(&model.ClientDistEvent{}).Where("created_at >= ? AND created_at < ?", now.Add(-7*24*time.Hour), time.Now().UTC()).Count(&sourceCount).Error)
	require.Equal(t, int64(3), sourceCount)

	w := makeRequest(r, http.MethodGet, "/api/v1/client-dist/export?kind=dist-events&range=7d&channelId=stable", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "true", w.Header().Get("X-Export-Truncated"))
	require.Contains(t, w.Body.String(), "truncated=true")
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(w.Body.String(), "\ufeff")), "\n")
	require.Len(t, lines, 4, w.Body.String()) // 表头 + 2 数据行 + 截断标记
}
