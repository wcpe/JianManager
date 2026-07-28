package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlatformObservabilityOverview_AdminOnly(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	member := getMemberToken(t, r, "member", "password123")

	w := makeRequest(r, http.MethodGet, "/api/v1/observability/overview", nil, member)
	require.Equalf(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
	w = makeRequest(r, http.MethodGet, "/api/v1/observability/overview", nil, admin)
	require.Equalf(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
}
