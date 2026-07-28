package router

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func TestBotRuntimeMetric_NodeAndPlatformAuthorization(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	member := getMemberToken(t, r, "member", "password123")
	node := createTestNode(t, db)
	value := 123.0
	require.NoError(t, service.NewMetricService(db).Ingest([]service.Sample{{
		NodeUUID: node.UUID, Scope: model.MetricScopeNode, MetricKey: model.MetricBotWorkerRSSBytes,
		Unit: "bytes", TS: time.Now().UTC(), Value: &value,
	}}))

	w := makeRequest(r, http.MethodGet, "/api/v1/metrics/bot-runtime?nodeId="+strconv.Itoa(int(node.ID))+"&resolution=raw", nil, admin)
	require.Equalf(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	result := parseJSON(t, w)
	require.Equal(t, true, result["sharedRuntime"])
	require.NotEmpty(t, result["notice"])

	w = makeRequest(r, http.MethodGet, "/api/v1/metrics/bot-runtime", nil, member)
	require.Equalf(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())

	w = makeRequest(r, http.MethodGet, "/api/v1/metrics/bot-runtime?nodeId=1&instanceId=2", nil, admin)
	require.Equalf(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
	w = makeRequest(r, http.MethodGet, "/api/v1/metrics/bot-runtime?from=2026-07-01T00:00:00Z", nil, admin)
	require.Equalf(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
}

func TestBotRuntimeMetric_InstanceAndSessionRequireTargetInstanceAccess(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	member := getMemberToken(t, r, "member", "password123")
	node := createTestNode(t, db)
	instance := &model.Instance{
		UUID: "bot-runtime-instance", NodeID: node.ID, Name: "target",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped, WorkDir: "/srv/target",
	}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "runtime", NamePrefix: "runtime", BotCount: 1,
		Status: model.BotStressSessionPending,
	}
	require.NoError(t, db.Create(session).Error)

	w := makeRequest(r, http.MethodGet, "/api/v1/metrics/bot-runtime?instanceId="+strconv.Itoa(int(instance.ID)), nil, member)
	require.Equalf(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
	w = makeRequest(r, http.MethodGet, "/api/v1/metrics/bot-runtime?sessionId="+strconv.Itoa(int(session.ID)), nil, member)
	require.Equalf(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
	w = makeRequest(r, http.MethodGet, "/api/v1/metrics/bot-runtime?sessionId="+strconv.Itoa(int(session.ID)), nil, admin)
	require.Equalf(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
}
