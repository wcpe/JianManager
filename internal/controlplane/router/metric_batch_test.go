package router

import (
	"net/http"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// makeInstanceWithMetric 建实例、可选分配到组，并播种一条实例级 TPS 序列；返回实例（含 UUID）。
func makeInstanceWithMetric(t *testing.T, db *gorm.DB, nodeUUID string, nodeID, groupID uint, name string) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		NodeID: nodeID, Name: name, Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar s.jar",
	}
	if err := db.Create(inst).Error; err != nil {
		t.Fatalf("创建实例失败: %v", err)
	}
	if groupID != 0 {
		if err := db.Create(&model.GroupInstance{GroupID: groupID, InstanceID: inst.ID}).Error; err != nil {
			t.Fatalf("分配实例到组失败: %v", err)
		}
	}
	v := 19.5
	ms := service.NewMetricService(db)
	if err := ms.Ingest([]service.Sample{{
		NodeUUID: nodeUUID, InstanceID: inst.UUID, Scope: model.MetricScopeInstance,
		MetricKey: model.MetricInstTPS, Unit: "tps", TS: time.Now().UTC(), Value: &v,
	}}); err != nil {
		t.Fatalf("播种实例指标失败: %v", err)
	}
	return inst
}

func TestMetricSeriesBatch_AdminOK(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	a := makeInstanceWithMetric(t, db, node.UUID, node.ID, 0, "a1")
	b := makeInstanceWithMetric(t, db, node.UUID, node.ID, 0, "b1")

	body := map[string]interface{}{
		"scope":     "instance",
		"targetIds": []string{a.UUID, b.UUID},
		"metrics":   []string{"inst_tps"},
		"range":     "24h",
	}
	w := makeRequest(r, "POST", "/api/v1/metrics/series/batch", body, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	series, ok := resp["series"].(map[string]interface{})
	if !ok {
		t.Fatalf("期望 series 为对象，得 %v", resp["series"])
	}
	if _, ok := series[a.UUID]; !ok {
		t.Fatalf("期望 series 含目标 a，得 %v", series)
	}
	if _, ok := series[b.UUID]; !ok {
		t.Fatalf("期望 series 含目标 b，得 %v", series)
	}
	if skipped, ok := resp["skipped"].([]interface{}); !ok || len(skipped) != 0 {
		t.Fatalf("期望 skipped 为空数组，得 %v", resp["skipped"])
	}
}

func TestMetricSeriesBatch_EmptyTargets(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{"scope": "instance", "targetIds": []string{}, "range": "24h"}
	w := makeRequest(r, "POST", "/api/v1/metrics/series/batch", body, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "INVALID_REQUEST" {
		t.Fatalf("期望 INVALID_REQUEST，得 %v", got)
	}
}

func TestMetricSeriesBatch_TooManyTargets(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	ids := make([]string, 51)
	for i := range ids {
		ids[i] = "uuid-" + itoa(uint(i))
	}
	body := map[string]interface{}{"scope": "instance", "targetIds": ids, "range": "24h"}
	w := makeRequest(r, "POST", "/api/v1/metrics/series/batch", body, token)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("期望 422，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "TOO_MANY_TARGETS" {
		t.Fatalf("期望 TOO_MANY_TARGETS，得 %v", got)
	}
}

func TestMetricSeriesBatch_InvalidScope(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{"scope": "node", "targetIds": []string{"x"}, "range": "24h"}
	w := makeRequest(r, "POST", "/api/v1/metrics/series/batch", body, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "INVALID_SCOPE" {
		t.Fatalf("期望 INVALID_SCOPE，得 %v", got)
	}
}

func TestMetricSeriesBatch_InvalidRange(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{"scope": "instance", "targetIds": []string{"x"}, "range": "bogus"}
	w := makeRequest(r, "POST", "/api/v1/metrics/series/batch", body, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "INVALID_RANGE" {
		t.Fatalf("期望 INVALID_RANGE，得 %v", got)
	}
}

func TestMetricSeriesBatch_InvalidResolution(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{"scope": "instance", "targetIds": []string{"x"}, "range": "24h", "resolution": "bogus"}
	w := makeRequest(r, "POST", "/api/v1/metrics/series/batch", body, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "INVALID_RESOLUTION" {
		t.Fatalf("期望 INVALID_RESOLUTION，得 %v", got)
	}
}

// TestMetricSeriesBatch_NotFoundSkipped：不存在的 targetId 进 skipped(not_found)，全剔除仍 200。
func TestMetricSeriesBatch_NotFoundSkipped(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{"scope": "instance", "targetIds": []string{"ghost-a", "ghost-b"}, "range": "24h"}
	w := makeRequest(r, "POST", "/api/v1/metrics/series/batch", body, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200（全剔除仍 200），得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if series, ok := resp["series"].(map[string]interface{}); !ok || len(series) != 0 {
		t.Fatalf("期望空 series，得 %v", resp["series"])
	}
	skipped, ok := resp["skipped"].([]interface{})
	if !ok || len(skipped) != 2 {
		t.Fatalf("期望 2 个 skipped，得 %v", resp["skipped"])
	}
	for _, s := range skipped {
		m := s.(map[string]interface{})
		if m["reason"] != "not_found" {
			t.Fatalf("期望 reason=not_found，得 %v", m["reason"])
		}
	}
}

// TestMetricSeriesBatch_ForbiddenSkipped：受限用户请求含越权目标 → 有权目标正常返回、
// 越权目标进 skipped(forbidden) 且响应不含其数据（FR-334 鉴权验收）。
func TestMetricSeriesBatch_ForbiddenSkipped(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminToken, "group-a")
	groupB := createGroupViaAPI(t, r, adminToken, "group-b")

	memberToken := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	allowed := makeInstanceWithMetric(t, db, node.UUID, node.ID, groupA, "allowed-1") // alice 有权
	forbidden := makeInstanceWithMetric(t, db, node.UUID, node.ID, groupB, "forbidden-1") // alice 越权

	body := map[string]interface{}{
		"scope":     "instance",
		"targetIds": []string{allowed.UUID, forbidden.UUID},
		"metrics":   []string{"inst_tps"},
		"range":     "24h",
	}
	w := makeRequest(r, "POST", "/api/v1/metrics/series/batch", body, memberToken)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	series, ok := resp["series"].(map[string]interface{})
	if !ok {
		t.Fatalf("期望 series 为对象，得 %v", resp["series"])
	}
	if _, ok := series[allowed.UUID]; !ok {
		t.Fatalf("期望有权目标正常返回，得 %v", series)
	}
	if _, ok := series[forbidden.UUID]; ok {
		t.Fatalf("越权目标不应出现在 series（无数据泄露），得 %v", series)
	}
	skipped, ok := resp["skipped"].([]interface{})
	if !ok || len(skipped) != 1 {
		t.Fatalf("期望 1 个 skipped，得 %v", resp["skipped"])
	}
	m := skipped[0].(map[string]interface{})
	if m["targetId"] != forbidden.UUID || m["reason"] != "forbidden" {
		t.Fatalf("期望越权目标进 skipped(forbidden)，得 %v", m)
	}
}
