package router

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type cutoverReadinessWorker struct{ workerpb.WorkerServiceClient }

func (cutoverReadinessWorker) LogCutoverReadiness(context.Context, *workerpb.LogCutoverReadinessRequest, ...grpc.CallOption) (*workerpb.LogCutoverReadinessResponse, error) {
	return &workerpb.LogCutoverReadinessResponse{CapabilityConfirmed: true, LedgerReady: true,
		CutoffTimeUtc: "2026-09-23T05:00:00Z"}, nil
}

func TestNodeWorkerUUIDListerReadsWorkerCutoverStateThroughTunnelPool(t *testing.T) {
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest("worker-ready", cutoverReadinessWorker{})
	lister := nodeWorkerUUIDLister{svcs: &Services{LogRuntimePool: pool}}
	mark, err := lister.CutoverReadiness(context.Background(), "worker-ready")
	require.NoError(t, err)
	require.True(t, mark.CapabilityConfirmed)
	require.True(t, mark.LedgerReady)
	require.Equal(t, "2026-09-23T05:00:00Z", mark.CutoffTime.Format(time.RFC3339))
}

// TestLogCutover_Get_DefaultDisabled 默认关闭：status + 空水位 + Legacy 默认预算。
func TestLogCutover_Get_DefaultDisabled(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/logs/cutover", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)

	m := parseJSON(t, w)
	assert.Equal(t, false, m["enabled"], "默认 cutover 必须关闭")
	assert.Equal(t, float64(0), m["blockedCount"])
	wms, _ := m["watermarks"].([]interface{})
	assert.Empty(t, wms)

	leg, _ := m["legacy"].(map[string]interface{})
	require.NotNil(t, leg)
	assert.Equal(t, float64(config.DefaultLegacyRetentionDays), leg["retentionDays"])
	assert.Equal(t, float64(config.DefaultLegacyMaxTotalMB), leg["maxTotalMB"])
	assert.Equal(t, false, m["platformPurgeExcludesLegacy"])
}

// TestLogCutover_Put_EnableOpensSwitch 打开 enabled 后状态可见；platform 容量淘汰开始排除 Legacy。
func TestLogCutover_Put_EnableOpensSwitch(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"enabled": true,
		"workerWatermarks": []map[string]interface{}{
			{"workerUuid": "w-1", "capabilityConfirmed": true, "ledgerReady": true, "apply": true},
		},
	}, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, true, m["enabled"])

	// platformPurgeExcludesLegacy 在切换打开时为 true：容量淘汰排除未到期 Legacy。
	assert.Equal(t, true, m["platformPurgeExcludesLegacy"])

	// 再 GET 确认持久化到运行态。
	w = makeRequest(r, "GET", "/api/v1/logs/cutover", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	m = parseJSON(t, w)
	assert.Equal(t, true, m["enabled"])
}

// F-002：无就绪水位时全局 enabled=true 必须 400，禁止静默缺口。
func TestLogCutover_Put_EnableWithoutWatermarksRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	w := makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"enabled": true,
	}, adminToken)
	require.Equal(t, http.StatusBadRequest, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, "INVALID_REQUEST", m["error"])
}

// F-005：存在未就绪 Worker 时，仅提交一个 ready 水位不得全局开启。
func TestLogCutover_Put_EnableRequiresAllWorkersReady(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	// 先登记未就绪水位（不 apply）。
	w := makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"enabled": false,
		"workerWatermarks": []map[string]interface{}{
			{"workerUuid": "w-unready", "capabilityConfirmed": false, "ledgerReady": false, "apply": false},
		},
	}, adminToken)
	require.Equal(t, http.StatusOK, w.Code)

	// 仅 ready w-ready 时 enable=true：因 w-unready 仍在集合中 → 400
	w = makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"enabled": true,
		"workerWatermarks": []map[string]interface{}{
			{"workerUuid": "w-ready", "capabilityConfirmed": true, "ledgerReady": true, "apply": true},
		},
	}, adminToken)
	require.Equal(t, http.StatusBadRequest, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, "INVALID_REQUEST", m["error"])
	assert.Contains(t, fmt.Sprint(m["message"]), "w-unready")

	// 两个都 ready 后允许开启
	w = makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"enabled": true,
		"workerWatermarks": []map[string]interface{}{
			{"workerUuid": "w-ready", "capabilityConfirmed": true, "ledgerReady": true, "apply": true},
			{"workerUuid": "w-unready", "capabilityConfirmed": true, "ledgerReady": true, "apply": true},
		},
	}, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
}

// TestLogCutover_Put_WatermarkValidation apply 缺 capability/ledger_ready → 400。
func TestLogCutover_Put_WatermarkValidation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	// 缺 workerUuid。
	w := makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"workerWatermarks": []map[string]interface{}{
			{"capabilityConfirmed": true, "ledgerReady": true, "apply": true},
		},
	}, adminToken)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// apply 但未确认能力。
	w = makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"workerWatermarks": []map[string]interface{}{
			{"workerUuid": "w-a", "capabilityConfirmed": false, "ledgerReady": true, "apply": true},
		},
	}, adminToken)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// apply 但账本未就绪。
	w = makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"workerWatermarks": []map[string]interface{}{
			{"workerUuid": "w-b", "capabilityConfirmed": true, "ledgerReady": false, "apply": true},
		},
	}, adminToken)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 校验失败后 GET 仍为默认关闭。
	w = makeRequest(r, "GET", "/api/v1/logs/cutover", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, false, m["enabled"])
}

// TestLogCutover_Put_WatermarkApply_Success 前置条件齐全时可登记并应用水位。
func TestLogCutover_Put_WatermarkApply_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"enabled": false,
		"workerWatermarks": []map[string]interface{}{
			{
				"workerUuid":          "worker-ready",
				"capabilityConfirmed": true,
				"ledgerReady":         true,
				"apply":               true,
			},
			{
				"workerUuid":          "worker-flags-only",
				"capabilityConfirmed": true,
				"ledgerReady":         true,
				"apply":               false,
			},
		},
	}, adminToken)
	require.Equal(t, http.StatusOK, w.Code)

	m := parseJSON(t, w)
	applied, _ := m["applied"].([]interface{})
	require.Len(t, applied, 2)
	first := applied[0].(map[string]interface{})
	assert.Equal(t, "worker-ready", first["workerUuid"])
	assert.Equal(t, true, first["cutoverApplied"])
	assert.Equal(t, true, first["capabilityConfirmed"])
	assert.Equal(t, true, first["ledgerReady"])

	second := applied[1].(map[string]interface{})
	assert.Equal(t, "worker-flags-only", second["workerUuid"])
	assert.Equal(t, false, second["cutoverApplied"], "apply=false 不得标记路由已切换")

	// 水位出现在 GET status。
	w = makeRequest(r, "GET", "/api/v1/logs/cutover", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	m = parseJSON(t, w)
	wms, _ := m["watermarks"].([]interface{})
	require.Len(t, wms, 2)
}

// TestLogCutover_RequiresPlatformAdmin 非平台管理员不可读写切换面。
func TestLogCutover_RequiresPlatformAdmin(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "cutover-member", "password123")

	for _, tc := range []struct {
		method string
		path   string
		body   interface{}
	}{
		{"GET", "/api/v1/logs/cutover", nil},
		{"PUT", "/api/v1/logs/cutover", map[string]interface{}{"enabled": true}},
		{"GET", "/api/v1/logs/legacy", nil},
	} {
		w := makeRequest(r, tc.method, tc.path, tc.body, memberToken)
		assert.Equal(t, http.StatusForbidden, w.Code, "%s %s", tc.method, tc.path)
	}

	// 未认证 → 401。
	w := makeRequest(r, "GET", "/api/v1/logs/cutover", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestLogCutover_Put_AuditRecorded PUT 成功后写入 action=log.cutover.update。
func TestLogCutover_Put_AuditRecorded(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"enabled": true,
		"workerWatermarks": []map[string]interface{}{
			{"workerUuid": "w-audit", "capabilityConfirmed": true, "ledgerReady": true, "apply": true},
		},
	}, adminToken)
	require.Equal(t, http.StatusOK, w.Code)

	var al model.AuditLog
	require.NoError(t, db.Where("action = ?", logCutoverAuditAction).First(&al).Error)
	assert.Equal(t, logCutoverAuditAction, al.Action)
	assert.NotEmpty(t, al.Detail)
}

// TestLogLegacy_Get_QueriesLegacyOnly Legacy 只读：instance/worker 行；platform 行不出现在结果。
func TestLogLegacy_Get_QueriesLegacyOnly(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "legacy instance"})
	seedLog(t, db, model.LogEntry{Source: model.LogSourceWorker, Level: model.LogLevelWarn, NodeID: 2, Message: "legacy worker"})
	seedLog(t, db, model.LogEntry{Source: model.LogSourceControlPlane, Level: model.LogLevelInfo, Message: "platform not legacy"})

	w := makeRequest(r, "GET", "/api/v1/logs/legacy", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)

	m := parseJSON(t, w)
	assert.Equal(t, "legacy", m["sourceTag"])
	assert.Equal(t, true, m["statsExact"], "表内 Legacy 行集合可数")

	total, _ := m["total"].(float64)
	assert.EqualValues(t, 2, total)
	assert.NotContains(t, w.Body.String(), "platform not legacy")

	items, _ := m["items"].([]interface{})
	require.Len(t, items, 2)
	for _, it := range items {
		row := it.(map[string]interface{})
		src, _ := row["source"].(string)
		assert.Contains(t, []string{string(model.LogSourceInstance), string(model.LogSourceWorker)}, src)
	}

	cov, _ := m["coverage"].(map[string]interface{})
	require.NotNil(t, cov)
	assert.Equal(t, "legacy", cov["sourceTag"])
	assert.Equal(t, false, cov["complete"])
}

// TestLogLegacy_Get_RejectsNonLegacySource source 仅允许空或 legacy。
func TestLogLegacy_Get_RejectsNonLegacySource(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/logs/legacy?source=control_plane", nil, adminToken)
	require.Equal(t, http.StatusBadRequest, w.Code)

	w = makeRequest(r, "GET", "/api/v1/logs/legacy?source=legacy", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, "legacy", m["sourceTag"])
}

// TestLogLegacy_Get_ReuseFilter 复用既有查询过滤维度。
func TestLogLegacy_Get_ReuseFilter(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	past := time.Now().Add(-time.Hour)
	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "keep me", Time: past})
	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelError, InstanceID: 1, Message: "error row", Time: past})

	w := makeRequest(r, "GET", "/api/v1/logs/legacy?level=info&keyword=keep", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	total, _ := m["total"].(float64)
	assert.EqualValues(t, 1, total)
	assert.Contains(t, w.Body.String(), "keep me")
	assert.NotContains(t, w.Body.String(), "error row")
}
