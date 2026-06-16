package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFile_Rename_MissingParams 缺少 oldPath 或 newPath 返回 400。
func TestFile_Rename_MissingParams(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "文件测试实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	// 缺少必要参数
	renameBody := map[string]interface{}{"oldPath": "a.txt"}
	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/files/rename", renameBody, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	renameBody = map[string]interface{}{"newPath": "b.txt"}
	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/files/rename", renameBody, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestFile_Rename_InstanceNotFound 对不存在实例执行 rename 返回错误。
func TestFile_Rename_InstanceNotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	renameBody := map[string]interface{}{"oldPath": "a.txt", "newPath": "b.txt"}
	w := makeRequest(r, "POST", "/api/v1/instances/999/files/rename", renameBody, token)
	assert.Contains(t, []int{http.StatusNotFound, http.StatusUnprocessableEntity}, w.Code)
}

// TestAlert_CreateListDelete 告警规则创建→列表→删除完整流程。
func TestAlert_CreateListDelete(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	// 创建告警规则
	ruleBody := map[string]interface{}{
		"name":         "CPU 告警",
		"targetType":   "node",
		"metric":       "cpu",
		"operator":     ">",
		"threshold":    90.0,
		"durationSec":  60,
		"notifyType":   "webhook",
		"notifyTarget": "https://example.com/hook",
	}
	w := makeRequest(r, "POST", "/api/v1/alerts/rules", ruleBody, token)
	assert.Equal(t, http.StatusCreated, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, "CPU 告警", resp["name"])
	assert.Equal(t, "cpu", resp["metric"])
	ruleID := uint(resp["id"].(float64))

	// 列表
	w = makeRequest(r, "GET", "/api/v1/alerts/rules", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	rules := parseJSONArray(t, w)
	require.Len(t, rules, 1)

	// 删除
	w = makeRequest(r, "DELETE", "/api/v1/alerts/rules/"+itoa(ruleID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 删除后列表为空
	w = makeRequest(r, "GET", "/api/v1/alerts/rules", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	rules = parseJSONArray(t, w)
	assert.Len(t, rules, 0)
}

// TestAlert_ListEvents_Empty 告警事件列表为空时返回空数组。
func TestAlert_ListEvents_Empty(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/alerts/events", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	events := parseJSONArray(t, w)
	assert.Len(t, events, 0)
}

// TestAlert_CreateRule_MissingFields 缺少必要字段返回 400。
func TestAlert_CreateRule_MissingFields(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{
		"name": "缺少必要字段",
	}
	w := makeRequest(r, "POST", "/api/v1/alerts/rules", body, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestSchedule_CreateListDelete 定时任务创建→列表→删除完整流程。
func TestSchedule_CreateListDelete(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	// 先创建一个实例
	instBody := map[string]interface{}{
		"nodeId":       1,
		"name":         "定时任务实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", instBody, token)
	require.Equal(t, http.StatusCreated, w.Code)
	inst := parseJSON(t, w)
	instID := uint(inst["id"].(float64))

	// 创建定时任务
	scheduleBody := map[string]interface{}{
		"instanceId": instID,
		"name":       "每天重启",
		"cronExpr":   "0 4 * * *",
		"action":     "restart",
	}
	w = makeRequest(r, "POST", "/api/v1/schedules", scheduleBody, token)
	assert.Equal(t, http.StatusCreated, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, "每天重启", resp["name"])
	assert.Equal(t, "0 4 * * *", resp["cronExpr"])
	scheduleID := uint(resp["id"].(float64))

	// 列表
	w = makeRequest(r, "GET", "/api/v1/schedules", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	schedules := parseJSONArray(t, w)
	require.Len(t, schedules, 1)

	// 按实例过滤
	w = makeRequest(r, "GET", "/api/v1/schedules?instanceId="+itoa(instID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	schedules = parseJSONArray(t, w)
	assert.Len(t, schedules, 1)

	// 删除
	w = makeRequest(r, "DELETE", "/api/v1/schedules/"+itoa(scheduleID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 删除后列表为空
	w = makeRequest(r, "GET", "/api/v1/schedules", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	schedules = parseJSONArray(t, w)
	assert.Len(t, schedules, 0)
}

// TestSchedule_ExecutionLogs_Empty 执行日志为空时返回空列表。
func TestSchedule_ExecutionLogs_Empty(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	// 创建实例和定时任务
	instBody := map[string]interface{}{
		"nodeId":       1,
		"name":         "日志实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", instBody, token)
	require.Equal(t, http.StatusCreated, w.Code)
	inst := parseJSON(t, w)
	instID := uint(inst["id"].(float64))

	scheduleBody := map[string]interface{}{
		"instanceId": instID,
		"name":       "日志任务",
		"cronExpr":   "0 * * * *",
		"action":     "command",
		"payload":    `{"command":"say hello"}`,
	}
	w = makeRequest(r, "POST", "/api/v1/schedules", scheduleBody, token)
	require.Equal(t, http.StatusCreated, w.Code)
	schedule := parseJSON(t, w)
	scheduleID := uint(schedule["id"].(float64))

	// 查询执行日志
	w = makeRequest(r, "GET", "/api/v1/schedules/"+itoa(scheduleID)+"/logs", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	logsResp := parseJSON(t, w)
	assert.Equal(t, float64(0), logsResp["total"])
	assert.Equal(t, float64(1), logsResp["page"])
}

// TestBackup_CreateListDelete 备份创建→列表→删除完整流程。
func TestBackup_CreateListDelete(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	// 创建实例
	instBody := map[string]interface{}{
		"nodeId":       1,
		"name":         "备份测试实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", instBody, token)
	require.Equal(t, http.StatusCreated, w.Code)
	inst := parseJSON(t, w)
	instID := uint(inst["id"].(float64))

	// 创建备份
	backupBody := map[string]interface{}{
		"name": "手动备份-20260617",
	}
	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/backups", backupBody, token)
	assert.Equal(t, http.StatusCreated, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, "手动备份-20260617", resp["name"])
	backupID := uint(resp["id"].(float64))

	// 列表
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(instID)+"/backups", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	backups := parseJSONArray(t, w)
	require.Len(t, backups, 1)

	// 删除
	w = makeRequest(r, "DELETE", "/api/v1/backups/"+itoa(backupID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 删除后列表为空
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(instID)+"/backups", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	backups = parseJSONArray(t, w)
	assert.Len(t, backups, 0)
}

// TestBackup_Create_MissingName 缺少名称返回 400。
func TestBackup_Create_MissingName(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	instBody := map[string]interface{}{
		"nodeId":       1,
		"name":         "备份400实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", instBody, token)
	require.Equal(t, http.StatusCreated, w.Code)
	inst := parseJSON(t, w)
	instID := uint(inst["id"].(float64))

	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/backups", map[string]interface{}{}, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
