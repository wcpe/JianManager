package router

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// seedMetricSamples 直接播种实例级样本（router 层端点测试共用）。
func seedMetricSamples(t *testing.T, db *gorm.DB, nodeUUID, instUUID string, samples []service.Sample) {
	t.Helper()
	ms := service.NewMetricService(db)
	if err := ms.Ingest(samples); err != nil {
		t.Fatalf("播种指标失败: %v", err)
	}
}

func instRawSample(nodeUUID, instUUID, key, unit string, ts time.Time, v float64) service.Sample {
	return service.Sample{
		NodeUUID: nodeUUID, InstanceID: instUUID, Scope: model.MetricScopeInstance,
		MetricKey: key, Unit: unit, TS: ts, Value: &v,
	}
}

func TestPerformanceAttribution_AdminOK(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	inst := makeInstanceWithMetric(t, db, node.UUID, node.ID, 0, "attr-1")

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	samples := make([]service.Sample, 0, 120)
	for i := 0; i < 60; i++ {
		ts := base.Add(time.Duration(i) * 30 * time.Second)
		tps := 20.0
		gc := 1.0
		if i >= 40 {
			tps = 15
			gc = 9
		}
		samples = append(samples,
			instRawSample(node.UUID, inst.UUID, model.MetricInstTPS, "tps", ts, tps),
			instRawSample(node.UUID, inst.UUID, model.MetricInstGCCount, "count_per_sec", ts, gc),
			instRawSample(node.UUID, inst.UUID, model.MetricInstThreads, "count", ts, 40),
		)
	}
	seedMetricSamples(t, db, node.UUID, inst.UUID, samples)

	w := makeRequest(r, "GET", "/api/v1/metrics/performance/attribution?scope=instance&targetId="+inst.UUID+"&range=1h", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["status"] != "ok" {
		t.Fatalf("期望 status=ok，得 %v", resp["status"])
	}
	factors, ok := resp["factors"].([]interface{})
	if !ok || len(factors) == 0 {
		t.Fatalf("期望非空 factors，得 %v", resp["factors"])
	}
	top := factors[0].(map[string]interface{})
	if top["metricKey"] != model.MetricInstGCCount {
		t.Fatalf("期望主因 GC，得 %v", top["metricKey"])
	}
}

func TestPerformanceAttribution_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	group := createGroupViaAPI(t, r, adminToken, "attr-group")
	inst := makeInstanceWithMetric(t, db, node.UUID, node.ID, group, "attr-private")

	memberToken := getMemberToken(t, r, "bob", "password123")
	w := makeRequest(r, "GET", "/api/v1/metrics/performance/attribution?scope=instance&targetId="+inst.UUID+"&range=1h", nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403，得 %d，body=%s", w.Code, w.Body.String())
	}
}

func TestInstanceRanking_AdminAndScoped(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "rank-a")
	groupB := createGroupViaAPI(t, r, adminToken, "rank-b")

	hi := makeInstanceWithMetric(t, db, node.UUID, node.ID, groupA, "rank-hi")
	lo := makeInstanceWithMetric(t, db, node.UUID, node.ID, groupB, "rank-lo")
	now := time.Now().UTC()
	seedMetricSamples(t, db, node.UUID, hi.UUID, []service.Sample{
		instRawSample(node.UUID, hi.UUID, model.MetricInstTPS, "tps", now, 20),
	})
	seedMetricSamples(t, db, node.UUID, lo.UUID, []service.Sample{
		instRawSample(node.UUID, lo.UUID, model.MetricInstTPS, "tps", now, 5),
	})

	// 管理员：全量排序，TPS 低者第一。
	w := makeRequest(r, "GET", "/api/v1/metrics/instances/ranking?metric=inst_tps&window=5m&limit=10", nil, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["scoped"] != false {
		t.Fatalf("管理员期望 scoped=false，得 %v", resp["scoped"])
	}
	items := resp["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("期望 2 条，得 %d", len(items))
	}
	first := items[0].(map[string]interface{})
	if first["instanceUuid"] != lo.UUID || first["rank"].(float64) != 1 {
		t.Fatalf("期望 lo 排第 1，得 %v", first)
	}

	// 非管理员：只见可访问实例（groupA → hi）。
	memberToken := getMemberToken(t, r, "carol", "password123")
	carolID := findUserIDByUsername(t, db, "carol")
	addMemberViaAPI(t, r, adminToken, groupA, carolID, model.GroupMemberRoleMember)
	wm := makeRequest(r, "GET", "/api/v1/metrics/instances/ranking?metric=inst_tps&window=5m", nil, memberToken)
	if wm.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", wm.Code, wm.Body.String())
	}
	rm := parseJSON(t, wm)
	if rm["scoped"] != true {
		t.Fatalf("非管理员期望 scoped=true，得 %v", rm["scoped"])
	}
	mItems := rm["items"].([]interface{})
	if len(mItems) != 1 {
		t.Fatalf("期望 1 条（越权实例不入榜），得 %d", len(mItems))
	}
	if got := mItems[0].(map[string]interface{})["instanceUuid"]; got != hi.UUID {
		t.Fatalf("期望仅见 hi，得 %v", got)
	}
}

func TestInstanceRanking_InvalidMetric(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	w := makeRequest(r, "GET", "/api/v1/metrics/instances/ranking?metric=node_cpu_pct", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "INVALID_METRIC" {
		t.Fatalf("期望 INVALID_METRIC，得 %v", got)
	}
}

func TestPlayerTrend_OK(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	inst := makeInstanceWithMetric(t, db, node.UUID, node.ID, 0, "trend-1")

	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	seedMetricSamples(t, db, node.UUID, inst.UUID, []service.Sample{
		instRawSample(node.UUID, inst.UUID, model.MetricInstPlayersOnline, "count", base, 10),
		instRawSample(node.UUID, inst.UUID, model.MetricInstPlayersOnline, "count", base.Add(time.Hour), 20),
	})

	w := makeRequest(r, "GET", "/api/v1/metrics/players/trend?range=6h&resolution=raw&tz=UTC", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	dist, ok := resp["hourlyDist"].([]interface{})
	if !ok || len(dist) != 24 {
		t.Fatalf("期望 24 时段，得 %v", resp["hourlyDist"])
	}
	if resp["peakValue"].(float64) != 20 {
		t.Fatalf("期望峰值 20，得 %v", resp["peakValue"])
	}
	if resp["timezone"] != "UTC" {
		t.Fatalf("期望 timezone=UTC，得 %v", resp["timezone"])
	}
}

// TestPlayerTrend_InvalidTZFallbackUTC （M4）tz 解析失败回退 UTC，不再 400。
//
// 背景：容器/裸机缺 /usr/share/zoneinfo 时 `time.LoadLocation` 对任何合法时区名都会失败，
// 而前端每逢查询都下发浏览器时区 → 官方部署下该卡片必然 400。修复后：回退 UTC 并回显实际生效时区。
func TestPlayerTrend_InvalidTZFallbackUTC(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	inst := makeInstanceWithMetric(t, db, node.UUID, node.ID, 0, "trend-fallback")
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	seedMetricSamples(t, db, node.UUID, inst.UUID, []service.Sample{
		instRawSample(node.UUID, inst.UUID, model.MetricInstPlayersOnline, "count", base, 10),
	})

	w := makeRequest(r, "GET", "/api/v1/metrics/players/trend?range=6h&resolution=raw&tz=Not/AZone", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("非法 tz 期望 200（回退 UTC），得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	tz, _ := resp["timezone"].(string)
	if !strings.Contains(tz, "UTC") || !strings.Contains(tz, "Not/AZone") {
		t.Fatalf("期望回显回退到 UTC 且标注原值，得 %v", tz)
	}
	if dist, ok := resp["hourlyDist"].([]interface{}); !ok || len(dist) != 24 {
		t.Fatalf("期望时段桶仍自洽（恒 24 项），得 %v", resp["hourlyDist"])
	}
}

// TestPlayerTrend_LegalTZResolves （M4）合法时区名（含依赖系统时区库的 IANA 名）必须可解析。
// 入口内嵌 time/tzdata 后，即使宿主无 zoneinfo 也能成功——本用例断言不 400 且回显原名。
func TestPlayerTrend_LegalTZResolves(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	for _, tz := range []string{"UTC", "Asia/Shanghai", "Asia/Kolkata", "America/New_York"} {
		w := makeRequest(r, "GET", "/api/v1/metrics/players/trend?range=6h&tz="+tz, nil, token)
		if w.Code != http.StatusOK {
			t.Fatalf("合法 tz %s 期望 200，得 %d，body=%s", tz, w.Code, w.Body.String())
		}
		if got := parseJSON(t, w)["timezone"]; got != tz {
			t.Fatalf("期望 timezone=%s，得 %v", tz, got)
		}
	}
}

func TestSLO_InstanceOKAndForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	group := createGroupViaAPI(t, r, adminToken, "slo-group")
	inst := makeInstanceWithMetric(t, db, node.UUID, node.ID, group, "slo-inst")

	// 60 拍 × 30s = 30 分钟，整体落在最近 1 小时窗口内（避免截断边界丢拍）。
	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Minute)
	samples := make([]service.Sample, 0, 60)
	for i := 0; i < 60; i++ {
		samples = append(samples, instRawSample(node.UUID, inst.UUID, model.MetricInstUptime, "seconds",
			base.Add(time.Duration(i)*30*time.Second), 100))
	}
	seedMetricSamples(t, db, node.UUID, inst.UUID, samples)

	w := makeRequest(r, "GET", "/api/v1/metrics/slo?scope=instance&targetId="+inst.UUID+"&range=1h", nil, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["upSamples"].(float64) != 60 {
		t.Fatalf("期望 upSamples=60，得 %v", resp["upSamples"])
	}
	if resp["mttrSeconds"] != nil {
		t.Fatalf("无故障期望 MTTR=null，得 %v", resp["mttrSeconds"])
	}
	if resp["mtbfSeconds"] != nil {
		t.Fatalf("无故障期望 MTBF=null（非 Infinity），得 %v", resp["mtbfSeconds"])
	}

	// 无权用户 403（验收 7）。
	memberToken := getMemberToken(t, r, "dave", "password123")
	wf := makeRequest(r, "GET", "/api/v1/metrics/slo?scope=instance&targetId="+inst.UUID+"&range=1h", nil, memberToken)
	if wf.Code != http.StatusForbidden {
		t.Fatalf("期望 403，得 %d，body=%s", wf.Code, wf.Body.String())
	}
}

func TestSLO_InvalidTarget(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	w := makeRequest(r, "GET", "/api/v1/metrics/slo?scope=instance&targetId=ghost&range=1h", nil, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("期望 404，得 %d", w.Code)
	}
	if got := parseJSON(t, w)["error"]; got != "TARGET_NOT_FOUND" {
		t.Fatalf("期望 TARGET_NOT_FOUND，得 %v", got)
	}
}

func TestCapacityForecast_NodeOKAndForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)

	// 节点容量快照（回退上限来源）。
	if err := db.Model(&model.Node{}).Where("id = ?", node.ID).
		Updates(map[string]interface{}{"disk_total_mb": 100 * 1024, "memory_mb": 16 * 1024}).Error; err != nil {
		t.Fatalf("更新节点容量失败: %v", err)
	}

	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	steps := int((6 * time.Hour) / (30 * time.Second))
	samples := make([]service.Sample, 0, steps+1)
	for i := 0; i <= steps; i++ {
		usedMB := 50*1024 + float64(i)*40
		if i%2 == 1 {
			usedMB += 4
		}
		samples = append(samples, service.Sample{
			NodeUUID: node.UUID, Scope: model.MetricScopeNode,
			MetricKey: model.MetricNodeDiskUsed, Unit: "bytes",
			TS: base.Add(time.Duration(i) * 30 * time.Second), Value: func() *float64 { v := usedMB * 1024 * 1024; return &v }(),
		})
	}
	seedMetricSamples(t, db, node.UUID, "", samples)

	w := makeRequest(r, "GET", "/api/v1/metrics/capacity/forecast?scope=node&targetId="+node.UUID+
		"&metrics=node_disk_used&from="+base.Format(time.RFC3339)+"&to="+base.Add(6*time.Hour).Format(time.RFC3339), nil, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	forecasts, ok := resp["forecasts"].([]interface{})
	if !ok || len(forecasts) != 1 {
		t.Fatalf("期望 1 条预测，得 %v", resp["forecasts"])
	}
	fr := forecasts[0].(map[string]interface{})
	if fr["confidence"] == "insufficient" {
		t.Fatalf("期望有增长预测，得 %v", fr)
	}
	if fr["exhaustLowDays"] == nil || fr["exhaustHighDays"] == nil {
		t.Fatalf("期望 80%% CI，得 %v", fr)
	}
}

func TestCapacityForecast_InstanceForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	group := createGroupViaAPI(t, r, adminToken, "cap-group")
	inst := makeInstanceWithMetric(t, db, node.UUID, node.ID, group, "cap-inst")

	memberToken := getMemberToken(t, r, "erin", "password123")
	w := makeRequest(r, "GET", "/api/v1/metrics/capacity/forecast?scope=instance&targetId="+inst.UUID, nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403，得 %d，body=%s", w.Code, w.Body.String())
	}
}

// TestInstanceRanking_WindowWhitelist （S4）window 必须落在白名单内，不再接受任意时长串。
func TestInstanceRanking_WindowWhitelist(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	// 白名单外的自由格式时长（原实现直接 time.ParseDuration 会接受）→ 400。
	for _, bad := range []string{"1h30m", "90s", "10m", "2d", "0s"} {
		w := makeRequest(r, "GET", "/api/v1/metrics/instances/ranking?metric=inst_tps&window="+bad, nil, token)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("window=%s 期望 400，得 %d，body=%s", bad, w.Code, w.Body.String())
		}
		if got := parseJSON(t, w)["error"]; got != "INVALID_WINDOW" {
			t.Fatalf("window=%s 期望 INVALID_WINDOW，得 %v", bad, got)
		}
	}
	// 白名单内取值 → 200。
	for _, ok := range []string{"5m", "15m", "1h", "6h", "24h", "7d", "30d"} {
		w := makeRequest(r, "GET", "/api/v1/metrics/instances/ranking?metric=inst_tps&window="+ok, nil, token)
		if w.Code != http.StatusOK {
			t.Fatalf("window=%s 期望 200，得 %d，body=%s", ok, w.Code, w.Body.String())
		}
	}
}

// TestMetricRange_OneYearAccepted （m4）range=1y 是前端 RangePicker 的既有选项，
// 后端必须接受（原先 /metrics/series 默认 24h 未覆盖 1y → 必然 400）。
func TestMetricRange_OneYearAccepted(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	inst := makeInstanceWithMetric(t, db, node.UUID, node.ID, 0, "year-1")

	for _, path := range []string{
		"/api/v1/metrics/series?scope=instance&targetId=" + inst.UUID + "&range=1y",
		"/api/v1/metrics/overview?range=1y",
		"/api/v1/metrics/players/trend?range=1y",
		"/api/v1/metrics/performance/attribution?scope=instance&targetId=" + inst.UUID + "&range=1y",
		"/api/v1/metrics/slo?scope=platform&range=1y",
		"/api/v1/metrics/capacity/forecast?scope=node&targetId=" + node.UUID + "&range=1y",
	} {
		w := makeRequest(r, "GET", path, nil, token)
		if w.Code != http.StatusOK {
			t.Fatalf("%s 期望 200，得 %d，body=%s", path, w.Code, w.Body.String())
		}
	}
	// 仍未收录的串照旧 400（不放松校验）。
	w := makeRequest(r, "GET", "/api/v1/metrics/series?scope=instance&targetId="+inst.UUID+"&range=2y", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("range=2y 期望 400，得 %d", w.Code)
	}
}

// TestInstanceRanking_SkippedNoDataCounts （M5）合并为单条条件聚合后，
// skippedNoData 仍须正确区分「有序列但窗口内无样本」与「有样本」。
func TestInstanceRanking_SkippedNoDataCounts(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	// A：窗口内有样本 → 进榜（helper 会自带一条当前时刻样本）。
	aUUID := makeInstanceWithMetric(t, db, node.UUID, node.ID, 0, "skip-a").UUID
	// B：手工建实例，只落窗口外旧样本 → 有序列但窗口内无样本，计入 skipped。
	b := &model.Instance{
		NodeID: node.ID, Name: "skip-b", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar s.jar",
	}
	if err := db.Create(b).Error; err != nil {
		t.Fatalf("创建实例失败: %v", err)
	}
	now := time.Now().UTC()
	seedMetricSamples(t, db, node.UUID, b.UUID, []service.Sample{
		instRawSample(node.UUID, b.UUID, model.MetricInstTPS, "tps", now.Add(-48*time.Hour), 9),
	})

	w := makeRequest(r, "GET", "/api/v1/metrics/instances/ranking?metric=inst_tps&window=5m", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if items := resp["items"].([]interface{}); len(items) != 1 {
		t.Fatalf("期望仅 A 进榜，得 %d 条", len(items))
	}
	if got := resp["skippedNoData"].(float64); got != 1 {
		t.Fatalf("期望 skippedNoData=1（B 有序列但窗口内无样本），得 %v", got)
	}

	// 24h 窗口走 5m 档：给 A 造一个窗口内桶、B 造一个窗口外桶，
	// 断言合并后的条件聚合在 rollup 档同样能区分「有序列」与「窗口内有样本」。
	seriesOf := func(uuid string) model.MetricSeries {
		var se model.MetricSeries
		if err := db.Where("instance_id = ? AND metric_key = ?", uuid, model.MetricInstTPS).First(&se).Error; err != nil {
			t.Fatalf("取序列失败: %v", err)
		}
		return se
	}
	buckets := []model.MetricRollup5m{
		{SeriesID: seriesOf(aUUID).ID, BucketTS: now.Add(-2 * time.Hour), Avg: 20, Min: 20, Max: 20, Last: 20, Count: 10},
		{SeriesID: seriesOf(b.UUID).ID, BucketTS: now.Add(-40 * time.Hour), Avg: 9, Min: 9, Max: 9, Last: 9, Count: 10},
	}
	if err := db.Create(&buckets).Error; err != nil {
		t.Fatalf("播种 rollup 桶失败: %v", err)
	}
	w2 := makeRequest(r, "GET", "/api/v1/metrics/instances/ranking?metric=inst_tps&window=24h", nil, token)
	if w2.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w2.Code, w2.Body.String())
	}
	resp2 := parseJSON(t, w2)
	if items := resp2["items"].([]interface{}); len(items) != 1 {
		t.Fatalf("24h 窗口内仅 A 有桶，期望 1 条，得 %d 条", len(items))
	}
	if got := resp2["skippedNoData"].(float64); got != 1 {
		t.Fatalf("期望 skippedNoData=1（B 仅有窗口外桶），得 %v", got)
	}
}

// TestSLO_PlatformTinyWindowRendersJSON （B-3 端到端复现）
//
// 平台维在窗口不足一个采样间隔时算得 availability=NaN。encoding/json 编不出 NaN，
// gin 的 `c.JSON` 在此情况下写出 **200 + 空 body**——调用方既拿不到数据也拿不到错误。
// 修复前本用例在 `parseJSON` 处因空 body 失败（实测 w.Code=200、body=""）。
func TestSLO_PlatformTinyWindowRendersJSON(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	inst := makeInstanceWithMetric(t, db, node.UUID, node.ID, 0, "slo-tiny")

	// 窗口内有可用拍，确保走「有序列」分支（即缺 perInstance 兜底的那条）。
	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	seedMetricSamples(t, db, node.UUID, inst.UUID, []service.Sample{
		instRawSample(node.UUID, inst.UUID, model.MetricInstUptime, "seconds", base, 100),
	})

	// from/to 显式给 5s 跨度：parseMetricRange 只校验 to > from，可直达该分支。
	from := base.UTC().Format(time.RFC3339)
	to := base.Add(5 * time.Second).UTC().Format(time.RFC3339)
	w := makeRequest(r, "GET",
		"/api/v1/metrics/slo?scope=platform&from="+from+"&to="+to, nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d，body=%s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) == "" {
		t.Fatalf("期望可解码 JSON body，实得空 body（NaN 渲染回归：调用方拿不到数据也拿不到错误）")
	}
	resp := parseJSON(t, w)
	if resp["applicable"].(bool) {
		t.Fatalf("窗口不足一个采样间隔 → 期望 applicable=false，得 true")
	}
	if got := resp["availability"].(float64); got != 0 {
		t.Fatalf("期望 availability=0，得 %v", got)
	}
}
