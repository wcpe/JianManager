package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// TestGroup_UpdateQuota_Success 平台管理员可通过 API 设置组配额。
func TestGroup_UpdateQuota_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	group := createGroupViaAPI(t, r, token, "配额设置组")

	quotaBody := map[string]interface{}{
		"maxInstances": 5,
		"maxBots":      20,
		"maxStorageMb": 2048,
	}
	w := makeRequest(r, "PUT", "/api/v1/groups/"+itoa(group)+"/quota", quotaBody, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 查询配额验证
	w = makeRequest(r, "GET", "/api/v1/groups/"+itoa(group)+"/quota", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, float64(5), resp["maxInstances"])
	assert.Equal(t, float64(20), resp["maxBots"])
	assert.Equal(t, float64(2048), resp["maxStorageMb"])
}

// TestGroup_UpdateQuota_NotFound 不存在的组配额更新返回 404。
func TestGroup_UpdateQuota_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	quotaBody := map[string]interface{}{"maxInstances": 5}
	w := makeRequest(r, "PUT", "/api/v1/groups/999/quota", quotaBody, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestGroup_QuotaFullFlow 创建→添加成员→设置配额→超额拒绝完整流程。
func TestGroup_QuotaFullFlow(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	// 1. 创建用户组
	groupID := createGroupViaAPI(t, r, adminToken, "完整流程组")

	// 2. 注册成员并加入组
	_ = getMemberToken(t, r, "quotauser", "password123")
	memberID := findUserIDByUsername(t, db, "quotauser")
	addMemberViaAPI(t, r, adminToken, groupID, memberID, model.GroupMemberRoleMember)

	// 3. 设置配额：最大 1 个实例
	quotaBody := map[string]interface{}{
		"maxInstances": 1,
		"maxBots":      50,
		"maxStorageMb": 10240,
	}
	w := makeRequest(r, "PUT", "/api/v1/groups/"+itoa(groupID)+"/quota", quotaBody, adminToken)
	assert.Equal(t, http.StatusOK, w.Code)

	// 4. 成员创建第一个实例（通过管理员代建并分配组）
	createInstanceViaAPI(t, r, adminToken, 1, groupID)

	// 5. 再建一个实例应被超额拒绝
	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "超额实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
		"groupId":      groupID,
	}
	w = makeRequest(r, "POST", "/api/v1/instances", body, adminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, "QUOTA_EXCEEDED", resp["error"])
}

// TestGroup_Quota_MaxBotsViaAPI 通过 API 设置 Bot 配额后超额拒绝。
func TestGroup_Quota_MaxBotsViaAPI(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	group := createGroupViaAPI(t, r, token, "Bot配额API组")

	// 设置 Bot 配额为 1
	quotaBody := map[string]interface{}{
		"maxInstances": 10,
		"maxBots":      1,
		"maxStorageMb": 0,
	}
	w := makeRequest(r, "PUT", "/api/v1/groups/"+itoa(group)+"/quota", quotaBody, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 创建实例并插入 Bot 达到上限
	inst := createInstanceViaAPI(t, r, token, 1, group)
	createBotsInDB(t, db, inst, 1)

	// 再建实例应因 Bot 配额被拒
	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "Bot超额实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
		"groupId":      group,
	}
	w = makeRequest(r, "POST", "/api/v1/instances", body, token)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestGroup_Quota_MaxStorageViaAPI 通过 API 设置存储配额后超额拒绝。
func TestGroup_Quota_MaxStorageViaAPI(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	group := createGroupViaAPI(t, r, token, "存储配额API组")

	// 设置存储配额为 500MB
	quotaBody := map[string]interface{}{
		"maxInstances": 10,
		"maxBots":      50,
		"maxStorageMb": 500,
	}
	w := makeRequest(r, "PUT", "/api/v1/groups/"+itoa(group)+"/quota", quotaBody, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 创建实例并插入 600MB 备份
	inst := createInstanceViaAPI(t, r, token, 1, group)
	backup := &model.Backup{
		UUID:       "big-backup-" + itoa(inst),
		InstanceID: inst,
		Name:       "大备份",
		FileSizeMB: 600,
		Status:     model.BackupStatusCompleted,
	}
	require.NoError(t, db.Create(backup).Error)

	// 再建实例应因存储配额被拒
	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "存储超额实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
		"groupId":      group,
	}
	w = makeRequest(r, "POST", "/api/v1/instances", body, token)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestGroup_RemoveMember_Success 移除组成员成功。
func TestGroup_RemoveMember_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	groupID := createGroupViaAPI(t, r, token, "移除成员组")

	getMemberToken(t, r, "removeme", "password123")
	memberID := findUserIDByUsername(t, db, "removeme")
	addMemberViaAPI(t, r, token, groupID, memberID, model.GroupMemberRoleMember)

	// 移除成员
	w := makeRequest(r, "DELETE", "/api/v1/groups/"+itoa(groupID)+"/members/"+itoa(memberID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 再次移除应返回 404
	w = makeRequest(r, "DELETE", "/api/v1/groups/"+itoa(groupID)+"/members/"+itoa(memberID), nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
