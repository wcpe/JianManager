package router

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSLO_NonFiniteTargetRejected （M-3）SLO 的 `target` 必须拒掉 NaN / ±Inf / 其它非法值。
//
// 缺陷：原校验 `err != nil || v <= 0 || v > 1` 对 NaN **两个条件都不成立**
// （`strconv.ParseFloat("NaN", 64)` 返回 (NaN, nil)，且 NaN 与任何数比较恒 false），
// 于是 `SLOQuery.Target = NaN` → `budgetAllowed = span*(1-NaN) = NaN` →
// `c.JSON` 编不出 NaN → 写出 **200 + 空 body**（调用方拿不到数据也拿不到错误）。
// service 侧有 `sloNormalizeTarget` 出口兜底，但本用例锁的是**入口必须拒绝**：
// 若入口静默归一化，前端会以为 NaN 被接受（无法区分「我传错了」与「已按默认值处理」）。
func TestSLO_NonFiniteTargetRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	inst := makeInstanceWithMetric(t, db, node.UUID, node.ID, 0, "slo-nan")

	rejected := []struct {
		name   string
		target string
	}{
		{"NaN", "NaN"},
		{"小写 nan", "nan"},
		{"+Inf", "+Inf"},
		{"-Inf", "-Inf"},
		{"Inf 裸串", "Inf"},
		{"零", "0"},
		{"负数", "-0.5"},
		{"大于 1", "1.5"},
		{"非数字", "abc"},
	}
	for _, c := range rejected {
		t.Run("拒绝/"+c.name, func(t *testing.T) {
			w := makeRequest(r, "GET",
				"/api/v1/metrics/slo?scope=instance&targetId="+inst.UUID+"&range=1h&target="+url.QueryEscape(c.target),
				nil, token)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("target=%q 期望 400，得 %d，body=%s", c.target, w.Code, w.Body.String())
			}
			// B-3 的失败形态正是「200 + 空 body」，故连 body 非空也一并断言。
			if strings.TrimSpace(w.Body.String()) == "" {
				t.Fatalf("target=%q 期望可解析的错误 body，实得空 body", c.target)
			}
			if got := parseJSON(t, w)["error"]; got != "INVALID_TARGET" {
				t.Fatalf("target=%q 期望 INVALID_TARGET，得 %v", c.target, got)
			}
		})
	}

	// 合法值仍须放行且原值回显（避免校验改严后把正常路径一起拒掉）。
	accepted := []struct {
		raw string
		v   float64
	}{{"0.995", 0.995}, {"1", 1}, {"0.0001", 0.0001}}
	for _, c := range accepted {
		t.Run("放行/"+c.raw, func(t *testing.T) {
			w := makeRequest(r, "GET",
				"/api/v1/metrics/slo?scope=instance&targetId="+inst.UUID+"&range=1h&target="+c.raw, nil, token)
			if w.Code != http.StatusOK {
				t.Fatalf("target=%q 期望 200，得 %d，body=%s", c.raw, w.Code, w.Body.String())
			}
			if got := parseJSON(t, w)["target"].(float64); got != c.v {
				t.Fatalf("target=%q 期望回显 %v，得 %v", c.raw, c.v, got)
			}
		})
	}
}

// TestCapacityForecast_NonFiniteThresholdRejected （M-3）容量预测的 `thresholdDays`
// 必须拒掉 NaN / ±Inf。
//
// 影响比 target 更直接：NaN 被接受后，`Notify` 的判据
// `*fr.ExhaustLowDays >= thresholdDays` 与 NaN 比较**恒为 false**，
// 于是**所有**带 ExhaustLowDays 的预测都被判为「命中阈值」——
// 一个普通用户只需传 `thresholdDays=NaN` 就能把趋势告警变成
// 「每 6h × 每指标 × 每目标」的告警洪泛（配合 M-4 曾无上限的 metrics 可继续放大）。
func TestCapacityForecast_NonFiniteThresholdRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	rng := "&from=" + base.Format(time.RFC3339) + "&to=" + base.Add(time.Hour).Format(time.RFC3339)
	prefix := "/api/v1/metrics/capacity/forecast?scope=node&targetId=" + node.UUID + "&metrics=node_disk_used" + rng

	for _, bad := range []string{"NaN", "nan", "+Inf", "-Inf", "0", "-1", "abc"} {
		t.Run("拒绝/"+bad, func(t *testing.T) {
			w := makeRequest(r, "GET", prefix+"&thresholdDays="+url.QueryEscape(bad), nil, token)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("thresholdDays=%q 期望 400，得 %d，body=%s", bad, w.Code, w.Body.String())
			}
			if strings.TrimSpace(w.Body.String()) == "" {
				t.Fatalf("thresholdDays=%q 期望可解析的错误 body，实得空 body", bad)
			}
			if got := parseJSON(t, w)["error"]; got != "INVALID_THRESHOLD" {
				t.Fatalf("thresholdDays=%q 期望 INVALID_THRESHOLD，得 %v", bad, got)
			}
		})
	}

	wOK := makeRequest(r, "GET", prefix+"&thresholdDays=3.5", nil, token)
	if wOK.Code != http.StatusOK {
		t.Fatalf("thresholdDays=3.5 期望 200，得 %d，body=%s", wOK.Code, wOK.Body.String())
	}
}

// TestCapacityForecast_MetricsCountCapped （M-4）`metrics` 参数必须有条数上限。
//
// 缺陷：`splitMetricKeys` 原先不做条数限制，而 `ForecastCapacity` 对每个 key
// 各调一次 `forecastPoints`（1 次 QuerySeries）+ `forecastLimit`（1 次 QuerySeries），
// 即每个 key ≈ 2~3 条 SELECT。审查员实测传 200 个键 → 发出 200 条 SELECT。
// 对比同一文件的 `SeriesBatch` 有 `metricBatchMaxTargets = 50` 硬上限。
//
// 断言要点：超限走 **422 TOO_MANY_METRICS**（与 SeriesBatch 的 422 同族，
// 区别于「语法/取值非法」的 400——这里参数形状合法，只是量太大）。
func TestCapacityForecast_MetricsCountCapped(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	rng := "&from=" + base.Format(time.RFC3339) + "&to=" + base.Add(time.Hour).Format(time.RFC3339)
	prefix := "/api/v1/metrics/capacity/forecast?scope=node&targetId=" + node.UUID + "&metrics="

	// 表驱动「key 数 → 期望结果」：上限为 8，边界两侧各打一发。
	// 用 strconv.Itoa 保证每个 key 互不相同（避免去重把用例意图吃掉）。
	keys := func(n int) string {
		parts := make([]string, 0, n)
		// 前两个用真实指标键，其余用合成键（ForecastCapacity 对未知键也会返回
		// 「样本不足」条目，故条数等于传入 key 数——下面的断言依赖这一点）。
		parts = append(parts, "node_disk_used", "node_mem_used")
		for i := 2; i < n; i++ {
			parts = append(parts, "synth_"+strconv.Itoa(i))
		}
		return strings.Join(parts[:n], ",")
	}

	t.Run("上限内放行且逐键返回", func(t *testing.T) {
		w := makeRequest(r, "GET", prefix+keys(8)+rng, nil, token)
		if w.Code != http.StatusOK {
			t.Fatalf("8 个 key（上限内）期望 200，得 %d，body=%s", w.Code, w.Body.String())
		}
		got := len(parseJSON(t, w)["forecasts"].([]interface{}))
		if got != 8 {
			t.Fatalf("期望逐 key 各返回 1 条预测（8 条），得 %d", got)
		}
	})

	t.Run("超限 1 个即拒", func(t *testing.T) {
		w := makeRequest(r, "GET", prefix+keys(9)+rng, nil, token)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("9 个 key 期望 422，得 %d，body=%s", w.Code, w.Body.String())
		}
		if got := parseJSON(t, w)["error"]; got != "TOO_MANY_METRICS" {
			t.Fatalf("期望 TOO_MANY_METRICS，得 %v", got)
		}
	})

	t.Run("200 个键被拒", func(t *testing.T) {
		// 审查员复现量级（实测原实现会发出 200 条 SELECT）。
		w := makeRequest(r, "GET", prefix+keys(200)+rng, nil, token)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("200 个 key 期望 422，得 %d，body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("重复键先去重再判上限", func(t *testing.T) {
		// 9 个「同一个键」去重后只剩 1 个，不应被误判为超限。
		dup := strings.Join([]string{
			"node_disk_used", "node_disk_used", "node_disk_used",
			"node_disk_used", "node_disk_used", "node_disk_used",
			"node_disk_used", "node_disk_used", "node_disk_used",
		}, ",")
		w := makeRequest(r, "GET", prefix+dup+rng, nil, token)
		if w.Code != http.StatusOK {
			t.Fatalf("9 个重复 key 去重后仅 1 个，期望 200，得 %d，body=%s", w.Code, w.Body.String())
		}
		if got := len(parseJSON(t, w)["forecasts"].([]interface{})); got != 1 {
			t.Fatalf("去重后期望 1 条预测，得 %d", got)
		}
	})
}

// TestProcessTop_UnknownNodeReturns404 （m4）`nodeId` 必须是**存在**的节点 UUID。
//
// 缺陷：`ProcessTop` 把 `c.Query("nodeId")` 直接当 NodeUUID 使用，不校验存在性
// （对比同文件的 Series/SLO/CapacityForecast 都调了 NodeExists/NodeByUUID）。
// 行为上不越权（不存在的 UUID 只让过滤条件恒假 → 空列表），但「静默返回空」
// 与「该节点确实没有受管进程」在响应上完全一致，运维会误判。
// 期望：与其它端点同口径的 404 TARGET_NOT_FOUND。
func TestProcessTop_UnknownNodeReturns404(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/metrics/processes/top?nodeId=ghost-node-uuid", nil, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的 nodeId 期望 404，得 %d，body=%s", w.Code, w.Body.String())
	}
	if got := parseJSON(t, w)["error"]; got != "TARGET_NOT_FOUND" {
		t.Fatalf("期望 TARGET_NOT_FOUND，得 %v", got)
	}

	// 存在节点（无快照）→ 不是 404（空列表是合法结果）。
	node := createTestNodeWithSuffix(t, db, "processtop-ok")
	wOK := makeRequest(r, "GET", "/api/v1/metrics/processes/top?nodeId="+node.UUID, nil, token)
	if wOK.Code == http.StatusNotFound {
		t.Fatalf("存在的 nodeId 不得返回 404，body=%s", wOK.Body.String())
	}
	if wOK.Code != http.StatusOK {
		t.Fatalf("存在的 nodeId 期望 200，得 %d，body=%s", wOK.Code, wOK.Body.String())
	}

	// 不传 nodeId（管理员全量视角）不受影响。
	wAll := makeRequest(r, "GET", "/api/v1/metrics/processes/top", nil, token)
	if wAll.Code != http.StatusOK {
		t.Fatalf("不传 nodeId 期望 200，得 %d，body=%s", wAll.Code, wAll.Body.String())
	}
}

// TestPlayerTrend_TZFallbackTruncated （m6）tz 回退提示里回显的用户输入必须有长度上限。
//
// 缺陷：`res.Timezone = "UTC (fallback from " + tzFallback + ")"` 把**未净化**的
// 用户输入原样拼进 JSON 字符串。Gin 的 JSON 编码会正确转义（无注入风险），
// 但无长度上限——`?tz=<1MB 串>` 会原样进入响应体，响应尺寸由调用方决定。
// 期望：截断到 64 字节并加省略号标记（让调用方看出「显示的不是完整原值」）。
func TestPlayerTrend_TZFallbackTruncated(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	long := strings.Repeat("X", 5000)
	w := makeRequest(r, "GET", "/api/v1/metrics/players/trend?range=1h&tz="+url.QueryEscape(long), nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200（tz 非法回退 UTC，不 400），得 %d，body=%s", w.Code, w.Body.String())
	}
	tz, _ := parseJSON(t, w)["timezone"].(string)
	if !strings.Contains(tz, "fallback from") {
		t.Fatalf("期望回退标注，得 timezone=%q", tz)
	}
	// 整个 timezone 字段应是常数级长度（前缀 "UTC (fallback from " 21 字节 + 64 + 省略号）。
	if len(tz) > 128 {
		t.Fatalf("timezone 回显过长（%d 字节），用户输入未被截断", len(tz))
	}
	if !strings.Contains(tz, "…") {
		t.Fatalf("截断应留省略号标记，得 %q", tz)
	}

	// 短输入必须原样回显（截断不得误伤正常路径）。
	wShort := makeRequest(r, "GET", "/api/v1/metrics/players/trend?range=1h&tz=Not%2FAZone", nil, token)
	if wShort.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d", wShort.Code)
	}
	if got := parseJSON(t, wShort)["timezone"].(string); got != "UTC (fallback from Not/AZone)" {
		t.Fatalf("短输入应原样回显，得 %q", got)
	}
}
