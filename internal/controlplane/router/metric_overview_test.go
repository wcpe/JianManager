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

// TestMetricOverview_OneYearRange （N-8）`1y` 是最长区间（前端总览可选、且每 10s 轮询），
// 走 1h 档 + 单条聚合 SQL；此处守住「端点对 1y 正常返回」这一路径（聚合成本见 service 侧用例）。
func TestMetricOverview_OneYearRange(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	seedNodeCPU(t, db, node.UUID, 42)

	w := makeRequest(r, "GET", "/api/v1/metrics/overview?range=1y", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200（1y 应被 metricRangeDurations 接受），得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	// 365d > 30d → auto 选 1h 档。
	if got := resp["resolution"]; got != "1h" {
		t.Fatalf("1y 应自动选 1h 档，得 %v", got)
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
