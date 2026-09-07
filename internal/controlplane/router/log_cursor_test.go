package router

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

/**
`/logs` 游标分页的路由层契约（FR-419，spec §4.2）：模式切换、参数校验、
以及「页码分页调用方行为不变」的回归。
*/

func seedLogsForCursor(t *testing.T, db *gorm.DB, n int) []model.LogEntry {
	t.Helper()
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	rows := make([]model.LogEntry, 0, n)
	for i := 0; i < n; i++ {
		e := model.LogEntry{
			Source:     model.LogSourceInstance,
			Level:      model.LogLevelInfo,
			InstanceID: 1,
			NodeID:     1,
			Stream:     "stdout",
			Message:    fmt.Sprintf("line-%d", i),
			Time:       base.Add(time.Duration(i) * time.Second),
		}
		require.NoError(t, db.Create(&e).Error)
		rows = append(rows, e)
	}
	return rows
}

func logMessages(t *testing.T, body map[string]interface{}) []string {
	t.Helper()
	items, ok := body["items"].([]interface{})
	require.True(t, ok, "响应缺少 items 数组")
	out := make([]string, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]interface{})
		require.True(t, ok)
		out = append(out, item["message"].(string))
	}
	return out
}

func TestLogs_CursorModeSwitchedByLimit(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	seedLogsForCursor(t, db, 5)

	// 出现 limit 即切游标模式：返回 nextCursor，不返回 total（游标模式不做全表 COUNT）。
	w := makeRequest(r, http.MethodGet, "/api/v1/logs?instanceId=1&limit=2", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, []string{"line-4", "line-3"}, logMessages(t, body))
	assert.Equal(t, float64(2), body["limit"])
	require.NotNil(t, body["nextCursor"])
	_, hasTotal := body["total"]
	assert.False(t, hasTotal, "游标模式不应返回 total")

	// 续翻：cursor 参数带回来，继续往更早走。
	cursor := body["nextCursor"].(string)
	w = makeRequest(r, http.MethodGet, "/api/v1/logs?instanceId=1&limit=2&cursor="+url.QueryEscape(cursor), nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	body = parseJSON(t, w)
	assert.Equal(t, []string{"line-2", "line-1"}, logMessages(t, body))

	// 末页：nextCursor 显式为 null（前端据此显示「已是最早」而非静默停住）。
	cursor = body["nextCursor"].(string)
	w = makeRequest(r, http.MethodGet, "/api/v1/logs?instanceId=1&limit=2&cursor="+url.QueryEscape(cursor), nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	body = parseJSON(t, w)
	assert.Equal(t, []string{"line-0"}, logMessages(t, body))
	assert.Nil(t, body["nextCursor"])
}

func TestLogs_CursorModeRejectsMalformedCursor(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	seedLogsForCursor(t, db, 3)

	// 非法游标必须 400：静默回退到「从最新开始」会让前端在翻页中途跳回表头，反复加载同一页。
	w := makeRequest(r, http.MethodGet, "/api/v1/logs?limit=2&cursor=garbage", nil, token)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "INVALID_REQUEST", parseJSON(t, w)["error"])
}

func TestLogs_CursorModeCarriesLevelFilter(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	for i := 0; i < 4; i++ {
		level := model.LogLevelInfo
		if i%2 == 1 {
			level = model.LogLevelError
		}
		require.NoError(t, db.Create(&model.LogEntry{
			Source: model.LogSourceInstance, Level: level, InstanceID: 1, NodeID: 1,
			Message: fmt.Sprintf("m-%d", i), Time: base.Add(time.Duration(i) * time.Second),
		}).Error)
	}

	// 级别过滤带进游标查询（spec §4.3：先过滤再回溯）。
	w := makeRequest(r, http.MethodGet, "/api/v1/logs?instanceId=1&level=error&limit=10", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"m-3", "m-1"}, logMessages(t, parseJSON(t, w)))
}

func TestLogs_PageModeUnchangedByCursorAddition(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	seedLogsForCursor(t, db, 5)

	// 回归：日志中心的页码调用方不带 limit/cursor，响应形状与语义一字不改。
	w := makeRequest(r, http.MethodGet, "/api/v1/logs?instanceId=1&page=1&pageSize=2", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, float64(5), body["total"])
	assert.Equal(t, float64(1), body["page"])
	assert.Equal(t, float64(2), body["pageSize"])
	assert.Equal(t, []string{"line-4", "line-3"}, logMessages(t, body))
	_, hasNext := body["nextCursor"]
	assert.False(t, hasNext, "页码模式不应返回 nextCursor")

	// 连 page/pageSize 都不带（StoppedLogsView 那类调用）同样走页码模式。
	w = makeRequest(r, http.MethodGet, "/api/v1/logs?instanceId=1", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	body = parseJSON(t, w)
	assert.Equal(t, float64(5), body["total"])
	assert.Equal(t, float64(50), body["pageSize"])
}
