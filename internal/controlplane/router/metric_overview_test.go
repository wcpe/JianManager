package router

import (
	"net/http"
	"testing"
)

func TestMetricOverview_OK(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	seedNodeCPU(t, db, node.UUID, 42)

	w := makeRequest(r, "GET", "/api/v1/metrics/overview?range=24h", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if _, ok := resp["totals"]; !ok {
		t.Fatalf("期望返回 totals，得 %v", resp)
	}
	if resp["resolution"] == nil {
		t.Fatalf("期望返回 resolution，得 %v", resp)
	}
	if _, ok := resp["trends"].([]interface{}); !ok {
		t.Fatalf("期望返回 trends 数组，得 %v", resp["trends"])
	}
}

func TestMetricOverview_InvalidRange(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/metrics/overview?range=bogus", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "INVALID_RANGE" {
		t.Fatalf("期望 INVALID_RANGE，得 %v", got)
	}
}

func TestMetricOverview_InvalidResolution(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/metrics/overview?range=24h&resolution=bogus", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "INVALID_RESOLUTION" {
		t.Fatalf("期望 INVALID_RESOLUTION，得 %v", got)
	}
}
