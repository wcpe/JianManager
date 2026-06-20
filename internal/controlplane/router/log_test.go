package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// seedLog 直接写入一条日志（绕过异步采集，确定性断言路由层）。
func seedLog(t *testing.T, db *gorm.DB, e model.LogEntry) {
	t.Helper()
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	require.NoError(t, db.Create(&e).Error)
}

func parseLogPage(t *testing.T, w *httptest.ResponseRecorder) (items []interface{}, total int) {
	t.Helper()
	m := parseJSON(t, w)
	if arr, ok := m["items"].([]interface{}); ok {
		items = arr
	}
	if v, ok := m["total"].(float64); ok {
		total = int(v)
	}
	return
}

func TestLog_List_AdminSeesAll(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "inst line"})
	seedLog(t, db, model.LogEntry{Source: model.LogSourceControlPlane, Level: model.LogLevelWarn, Message: "platform line"})

	w := makeRequest(r, "GET", "/api/v1/logs", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	_, total := parseLogPage(t, w)
	assert.Equal(t, 2, total) // 管理员可见实例 + 平台日志
}

func TestLog_List_Filters(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "started ok"})
	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelError, InstanceID: 1, Message: "crash boom"})

	// 级别过滤。
	w := makeRequest(r, "GET", "/api/v1/logs?level=error", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	_, total := parseLogPage(t, w)
	assert.Equal(t, 1, total)

	// 关键字过滤。
	w = makeRequest(r, "GET", "/api/v1/logs?keyword=boom", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	_, total = parseLogPage(t, w)
	assert.Equal(t, 1, total)
}

func TestLog_List_CrossGroupIsolation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminToken, "组A")
	groupB := createGroupViaAPI(t, r, adminToken, "组B")

	aliceToken := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	instA := createInstanceViaAPI(t, r, adminToken, 1, groupA)
	instB := createInstanceViaAPI(t, r, adminToken, 1, groupB)

	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: instA, Message: "A line"})
	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: instB, Message: "B line"})
	seedLog(t, db, model.LogEntry{Source: model.LogSourceControlPlane, Level: model.LogLevelInfo, Message: "platform secret"})

	// alice 仅见组 A 实例日志，看不到组 B、也看不到平台日志。
	w := makeRequest(r, "GET", "/api/v1/logs", nil, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	items, total := parseLogPage(t, w)
	assert.Equal(t, 1, total)
	require.Len(t, items, 1)
	first := items[0].(map[string]interface{})
	assert.Equal(t, "A line", first["message"])

	// 即便显式请求平台日志，非管理员也被强制收敛为实例日志：
	// 绝不返回平台日志（"platform secret" 不出现），只会拿到自己有权的实例日志。
	w = makeRequest(r, "GET", "/api/v1/logs?source=control_plane", nil, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "platform secret")
	items, total = parseLogPage(t, w)
	assert.Equal(t, 1, total) // 强制回落为实例日志：alice 的组 A 实例日志
	require.Len(t, items, 1)
	assert.Equal(t, "A line", items[0].(map[string]interface{})["message"])

	// admin 见全部 3 条。
	w = makeRequest(r, "GET", "/api/v1/logs", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	_, adminTotal := parseLogPage(t, w)
	assert.Equal(t, 3, adminTotal)
}

func TestLog_Export_NDJSON(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "export one", Time: time.Now().Add(-time.Minute)})
	seedLog(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "export two"})

	w := makeRequest(r, "GET", "/api/v1/logs/export", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	assert.Len(t, lines, 2)
	// 导出按时间正序。
	assert.Contains(t, lines[0], "export one")
	assert.Contains(t, lines[1], "export two")
}

func TestLog_List_RequiresAuth(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	w := makeRequest(r, "GET", "/api/v1/logs", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
