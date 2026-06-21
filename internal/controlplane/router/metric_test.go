package router

import (
	"net/http"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func seedNodeCPU(t *testing.T, db *gorm.DB, nodeUUID string, v float64) {
	t.Helper()
	ms := service.NewMetricService(db)
	if err := ms.Ingest([]service.Sample{{
		NodeUUID: nodeUUID, Scope: model.MetricScopeNode, MetricKey: model.MetricNodeCPUPct,
		Unit: "pct", TS: time.Now().UTC(), Value: &v,
	}}); err != nil {
		t.Fatalf("seed 指标失败: %v", err)
	}
}

func TestMetricSeries_NodeScopeOK(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	seedNodeCPU(t, db, node.UUID, 42)

	w := makeRequest(r, "GET", "/api/v1/metrics/series?scope=node&targetId="+node.UUID+"&range=24h&resolution=raw", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["resolution"] != "raw" {
		t.Fatalf("期望 resolution=raw，得 %v", resp["resolution"])
	}
	series, ok := resp["series"].([]interface{})
	if !ok || len(series) == 0 {
		t.Fatalf("期望非空 series，得 %v", resp["series"])
	}
}

func TestMetricSeries_InvalidScope(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/metrics/series?scope=bogus&targetId=x", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "INVALID_SCOPE" {
		t.Fatalf("期望 INVALID_SCOPE，得 %v", got)
	}
}

func TestMetricSeries_InvalidRange(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, "GET", "/api/v1/metrics/series?scope=node&targetId="+node.UUID+"&range=bogus", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "INVALID_RANGE" {
		t.Fatalf("期望 INVALID_RANGE，得 %v", got)
	}
}

func TestMetricSeries_NodeNotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/metrics/series?scope=node&targetId=does-not-exist&range=24h", nil, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("期望 404，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "TARGET_NOT_FOUND" {
		t.Fatalf("期望 TARGET_NOT_FOUND，得 %v", got)
	}
}

func TestMetricSeries_InstanceForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r) // 触发 setup
	member := getMemberToken(t, r, "bob", "password123")
	node := createTestNode(t, db)

	// 未分配任何用户组的实例：组成员无权访问。
	inst := &model.Instance{
		NodeID: node.ID, Name: "i1", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar s.jar",
	}
	if err := db.Create(inst).Error; err != nil {
		t.Fatalf("创建实例失败: %v", err)
	}

	w := makeRequest(r, "GET", "/api/v1/metrics/series?scope=instance&targetId="+inst.UUID+"&range=24h", nil, member)
	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403，得 %d，body=%s", w.Code, w.Body.String())
	}
	if got := parseJSON(t, w)["error"]; got != "FORBIDDEN" {
		t.Fatalf("期望 FORBIDDEN，得 %v", got)
	}
}
