package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func setupRuntimeRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	channelSvc := service.NewClientChannelService(db)
	svcs := &Services{
		Auth: service.NewAuthService(db, jwtCfg), User: service.NewUserService(db), Authz: service.NewAuthzService(db), Audit: service.NewAuditService(db),
		ClientChannel: channelSvc, ClientDistTracking: service.NewClientDistTrackingService(db), ClientRuntimeState: service.NewClientRuntimeStateService(db),
		ClientTelemetry: service.NewClientTelemetryService(db),
	}
	return Setup(svcs, jwtCfg.Secret)
}

func seedRuntimeChannel(t *testing.T, db *gorm.DB) {
	t.Helper()
	svc := service.NewClientChannelService(db)
	_, err := svc.CreateChannel("stable", "稳定", "")
	require.NoError(t, err)
	_, _, err = svc.CreateKey("stable", "测试密钥", "secret", nil)
	require.NoError(t, err)
}

func makeRuntimeRequest(r *gin.Engine, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestClientRuntimeHeartbeatEndpoint_RecordsState(t *testing.T) {
	db := setupTestDB(t)
	seedRuntimeChannel(t, db)
	r := setupRuntimeRouter(t, db)
	body := map[string]any{"platform": "windows", "javaVersion": "21", "launcher": "hmcl", "coreVersion": "1.2.3", "localVersion": 7}
	w := makeRuntimeRequest(r, "POST", "/api/v1/client-channels/stable/telemetry/heartbeat", body, map[string]string{"X-Client-Key": "secret", "X-Machine-Id": "machine-a"})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	var st model.ClientRuntimeState
	require.NoError(t, db.Where("channel_id = ? AND machine_id = ?", "stable", "machine-a").First(&st).Error)
	require.Equal(t, "windows", st.Platform)
	require.Equal(t, "1.2.3", st.CoreVersion)
	require.Equal(t, 7, st.LocalVersion)
}

func TestClientRuntimeAdminEndpoints_QueryBoundaries(t *testing.T) {
	db := setupTestDB(t)
	seedRuntimeChannel(t, db)
	r := setupRuntimeRouter(t, db)
	token := getAdminToken(t, r)
	now := time.Now()
	require.NoError(t, db.Create(&model.ClientRuntimeState{ChannelID: "stable", MachineID: "machine-a", IP: "127.0.0.1", Platform: "windows", CoreVersion: "1.2.3", LocalVersion: 7, FirstSeenAt: now, LastHeartbeatAt: now}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "stable", MachineID: "machine-a", IP: "127.0.0.1", Kind: "manifest", Version: 7, Status: 200, CreatedAt: now}).Error)

	w := makeRequest(r, "GET", "/api/v1/client-dist/clients?channelId=stable", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var clients struct {
		Summary struct {
			RecentStarts int64 `json:"recentStarts"`
			TodayStarts  int64 `json:"todayStarts"`
		} `json:"summary"`
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &clients))
	require.Equal(t, int64(1), clients.Summary.TodayStarts)
	require.Len(t, clients.Items, 1)

	w = makeRequest(r, "GET", "/api/v1/client-dist/events/search?machineId=machine-a", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var page struct {
		Items []model.ClientDistEvent `json:"items"`
		Total int64                   `json:"total"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.Equal(t, int64(1), page.Total)
}

func TestClientDistEventDetailEndpoint_RedactsHeaders(t *testing.T) {
	db := setupTestDB(t)
	seedRuntimeChannel(t, db)
	r := setupRuntimeRouter(t, db)
	token := getAdminToken(t, r)
	tracking := service.NewClientDistTrackingService(db)
	require.NoError(t, tracking.Record(service.ClientDistEventInput{ChannelID: "stable", MachineID: "m1", IP: "127.0.0.1", Kind: "manifest", Status: 200, Method: "GET", Path: "/api/v1/client-channels/stable/manifest?token=bad", RequestHeaders: map[string]string{"X-Client-Key": "secret", "Authorization": "bearer secret"}, ResponseHeaders: map[string]string{"ETag": "abc"}}))

	var ev model.ClientDistEvent
	require.NoError(t, db.First(&ev).Error)
	w := makeRequest(r, "GET", "/api/v1/client-dist/events/"+itoa(ev.ID), nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var detail struct {
		Path           string            `json:"path"`
		RequestHeaders map[string]string `json:"requestHeaders"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &detail))
	require.Equal(t, "/api/v1/client-channels/stable/manifest", detail.Path)
	require.Equal(t, "present", detail.RequestHeaders["X-Client-Key"])
	require.NotContains(t, detail.RequestHeaders, "Authorization")
}

func TestClientDistRealtimeEndpoint_UsesOnlyEvents(t *testing.T) {
	db := setupTestDB(t)
	seedRuntimeChannel(t, db)
	r := setupRuntimeRouter(t, db)
	token := getAdminToken(t, r)
	now := time.Now()
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: "stable", MachineID: "telemetry-only", Result: "ok", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "stable", MachineID: "event-machine", IP: "127.0.0.1", Kind: "artifact", Status: 500, ErrCode: "ARTIFACT_ERROR", CreatedAt: now}).Error)

	w := makeRequest(r, "GET", "/api/v1/client-dist/realtime?channelId=stable", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var rt struct {
		Summary1h struct {
			ArtifactPulls  int64 `json:"artifactPulls"`
			ErrorRequests  int64 `json:"errorRequests"`
			ActiveMachines int64 `json:"activeMachines"`
		} `json:"summary1h"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rt))
	require.Equal(t, int64(1), rt.Summary1h.ArtifactPulls)
	require.Equal(t, int64(1), rt.Summary1h.ErrorRequests)
	require.Equal(t, int64(1), rt.Summary1h.ActiveMachines)
}

func TestClientRuntimeClientsEndpoint_UsesTelemetryUpdateResults(t *testing.T) {
	db := setupTestDB(t)
	seedRuntimeChannel(t, db)
	r := setupRuntimeRouter(t, db)
	token := getAdminToken(t, r)

	heartbeatBody := map[string]any{"platform": "windows", "javaVersion": "21", "launcher": "hmcl", "coreVersion": "1.2.3", "localVersion": 7}
	heartbeat := makeRuntimeRequest(r, "POST", "/api/v1/client-channels/stable/telemetry/heartbeat", heartbeatBody, map[string]string{"X-Client-Key": "secret", "X-Machine-Id": "machine-a"})
	require.Equal(t, http.StatusAccepted, heartbeat.Code, heartbeat.Body.String())

	telemetryBody := map[string]any{"channel": "stable", "result": "success", "fromVersion": 6, "toVersion": 7, "os": "windows", "bootSuccess": true}
	telemetry := makeRuntimeRequest(r, "POST", "/api/v1/client-telemetry", telemetryBody, map[string]string{"X-Client-Key": "secret", "X-Machine-Id": "machine-a"})
	require.Equal(t, http.StatusAccepted, telemetry.Code, telemetry.Body.String())
	var written model.ClientTelemetry
	require.NoError(t, db.Where("channel_id = ?", "stable").First(&written).Error)
	require.Equal(t, "success", written.Result)

	w := makeRequest(r, "GET", "/api/v1/client-dist/clients?channelId=stable&range=7d", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var clients struct {
		Summary struct {
			UpdateSuccessRate float64 `json:"updateSuccessRate"`
			UpdateFailureRate float64 `json:"updateFailureRate"`
		} `json:"summary"`
		UpdateResultSeries []struct {
			Success int64 `json:"success"`
		} `json:"updateResultSeries"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &clients))
	require.Greater(t, clients.Summary.UpdateSuccessRate, 0.0)
	require.Equal(t, 0.0, clients.Summary.UpdateFailureRate)
	require.NotEmpty(t, clients.UpdateResultSeries)
	require.Equal(t, int64(1), clients.UpdateResultSeries[0].Success)
}
