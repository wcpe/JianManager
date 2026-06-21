package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestRBAC_MemberCannotReadUsers 普通成员访问用户管理接口被拒。
func TestRBAC_MemberCannotReadUsers(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r) // 初始化管理员
	memberToken := getMemberToken(t, r, "mem1", "password123")

	w := makeRequest(r, "GET", "/api/v1/users", nil, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestRBAC_MemberCannotReadNodes 普通成员访问节点列表被拒（节点管理限平台管理员）。
func TestRBAC_MemberCannotReadNodes(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "mem2", "password123")

	w := makeRequest(r, "GET", "/api/v1/nodes", nil, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestRBAC_CrossGroupAccessForbidden 组 A 成员不能访问组 B 的实例与文件。
func TestRBAC_CrossGroupAccessForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminToken, "组A")
	groupB := createGroupViaAPI(t, r, adminToken, "组B")

	// alice 属于组 A（普通成员）
	aliceToken := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	// bob 属于组 B（普通成员）
	bobToken := getMemberToken(t, r, "bob", "password123")
	bobID := findUserIDByUsername(t, db, "bob")
	addMemberViaAPI(t, r, adminToken, groupB, bobID, model.GroupMemberRoleMember)

	// 在组 B 创建一个实例
	instB := createInstanceViaAPI(t, r, adminToken, 1, groupB)

	// alice 不能读取组 B 的实例详情（返回 404，避免泄露存在性）
	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(instB), nil, aliceToken)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// alice 不能获取组 B 实例的终端 token
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(instB)+"/terminal-token", nil, aliceToken)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// alice 不能写组 B 实例的文件
	writeBody := map[string]interface{}{"path": "a.txt", "content": "x"}
	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(instB)+"/files/write", writeBody, aliceToken)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// alice 列表里不应出现组 B 的实例
	w = makeRequest(r, "GET", "/api/v1/instances", nil, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	instances := parseJSONArray(t, w)
	assert.Len(t, instances, 0)

	// bob 能读取组 B 的实例详情
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(instB), nil, bobToken)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestRBAC_GroupAdminBoundary 组管理员能管理本组、不能管理别组。
func TestRBAC_GroupAdminBoundary(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminToken, "组A")
	groupB := createGroupViaAPI(t, r, adminToken, "组B")

	// gadmin 是组 A 的组管理员
	gadminToken := getMemberToken(t, r, "gadmin", "password123")
	gadminID := findUserIDByUsername(t, db, "gadmin")
	setGlobalRole(t, db, gadminID, model.RoleGroupAdmin)
	addMemberViaAPI(t, r, adminToken, groupA, gadminID, model.GroupMemberRoleAdmin)

	// 组管理员能查看本组详情
	w := makeRequest(r, "GET", "/api/v1/groups/"+itoa(groupA), nil, gadminToken)
	assert.Equal(t, http.StatusOK, w.Code)

	// 组管理员能更新本组
	updateBody := map[string]interface{}{"description": "由组管理员更新"}
	w = makeRequest(r, "PUT", "/api/v1/groups/"+itoa(groupA), updateBody, gadminToken)
	assert.Equal(t, http.StatusOK, w.Code)

	// 组管理员不能查看/管理别组（组 B）
	w = makeRequest(r, "GET", "/api/v1/groups/"+itoa(groupB), nil, gadminToken)
	assert.Equal(t, http.StatusNotFound, w.Code)

	w = makeRequest(r, "PUT", "/api/v1/groups/"+itoa(groupB), updateBody, gadminToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// 组管理员不能删除组（仅平台管理员）
	w = makeRequest(r, "DELETE", "/api/v1/groups/"+itoa(groupA), nil, gadminToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// 组管理员能向本组添加成员
	newMemberToken := getMemberToken(t, r, "newmember", "password123")
	newMemberID := findUserIDByUsername(t, db, "newmember")
	addBody := map[string]interface{}{"userId": newMemberID, "role": 0}
	w = makeRequest(r, "POST", "/api/v1/groups/"+itoa(groupA)+"/members", addBody, gadminToken)
	assert.Equal(t, http.StatusOK, w.Code)

	// 组管理员不能修改组配额（仅平台管理员）
	quotaBody := map[string]interface{}{"maxInstances": 20}
	w = makeRequest(r, "PUT", "/api/v1/groups/"+itoa(groupA)+"/quota", quotaBody, gadminToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// 组管理员能在本组创建实例
	inst := createInstanceViaAPI(t, r, gadminToken, 1, groupA)
	require.Greater(t, inst, uint(0))

	// 组管理员不能向别组分配实例
	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "越权实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
		"groupId":      groupB,
	}
	w = makeRequest(r, "POST", "/api/v1/instances", body, gadminToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// 新加入的普通成员 newmember 能读取本组实例
	_ = newMemberToken
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(inst), nil, newMemberToken)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestRBAC_GetGroupQuota 组成员可查看本组配额用量，不可查看别组。
func TestRBAC_GetGroupQuota(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminToken, "组A")
	groupB := createGroupViaAPI(t, r, adminToken, "组B")

	aliceToken := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	// 本组配额
	w := makeRequest(r, "GET", "/api/v1/groups/"+itoa(groupA)+"/quota", nil, aliceToken)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, float64(10), resp["maxInstances"])
	assert.Equal(t, float64(0), resp["usedInstances"])

	// 别组配额不可见
	w = makeRequest(r, "GET", "/api/v1/groups/"+itoa(groupB)+"/quota", nil, aliceToken)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestQuota_MaxInstancesExceeded 实例数配额超额拒绝。
func TestQuota_MaxInstancesExceeded(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	group := createGroupViaAPI(t, r, adminToken, "配额组")
	setGroupQuota(t, db, group, 1, 50, 0)

	// 第一个实例成功
	createInstanceViaAPI(t, r, adminToken, 1, group)

	// 第二个实例超额拒绝
	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "第二个",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
		"groupId":      group,
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, adminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	resp := parseJSON(t, w)
	assert.Contains(t, resp["message"], "实例数")
}

// TestQuota_MaxBotsExceeded Bot 数配额超额拒绝新建实例。
func TestQuota_MaxBotsExceeded(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	group := createGroupViaAPI(t, r, adminToken, "Bot配额组")
	setGroupQuota(t, db, group, 10, 1, 0)

	// 先建一个实例并塞入 1 个 Bot（达到 MaxBots 上限）
	inst := createInstanceViaAPI(t, r, adminToken, 1, group)
	createBotsInDB(t, db, inst, 1)

	// 再建第二个实例时应因 Bot 配额已满被拒
	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "第二个",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
		"groupId":      group,
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, adminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	resp := parseJSON(t, w)
	assert.Contains(t, resp["message"], "Bot")
}

// TestQuota_MaxStorageZeroUnlimited 存储配额为 0 表示不限制，创建实例不受阻。
func TestQuota_MaxStorageZeroUnlimited(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	group := createGroupViaAPI(t, r, adminToken, "存储不限组")
	setGroupQuota(t, db, group, 10, 50, 0)

	// MaxStorageMB=0 不限制，可正常创建
	inst := createInstanceViaAPI(t, r, adminToken, 1, group)
	require.Greater(t, inst, uint(0))
}

// TestQuota_MaxStorageExceeded 存储配额超额拒绝新建实例。
func TestQuota_MaxStorageExceeded(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	group := createGroupViaAPI(t, r, adminToken, "存储配额组")
	// 限制 500MB
	setGroupQuota(t, db, group, 10, 50, 500)

	// 建一个实例并塞入一个 600MB 的备份，使存储用量超额
	inst := createInstanceViaAPI(t, r, adminToken, 1, group)
	backup := &model.Backup{
		UUID:       "back-" + itoa(inst),
		InstanceID: inst,
		Name:       "大备份",
		FileSizeMB: 600,
		Status:     model.BackupStatusCompleted,
	}
	require.NoError(t, db.Create(backup).Error)

	// 再建实例应因存储配额超额被拒
	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "第二个",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
		"groupId":      group,
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, adminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	resp := parseJSON(t, w)
	assert.Contains(t, resp["message"], "存储")
}

// TestQuota_UsageReflectsInstancesAndBots 配额用量接口反映实例数与 Bot 数。
func TestQuota_UsageReflectsInstancesAndBots(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	group := createGroupViaAPI(t, r, adminToken, "用量组")
	inst := createInstanceViaAPI(t, r, adminToken, 1, group)
	createBotsInDB(t, db, inst, 3)

	w := makeRequest(r, "GET", "/api/v1/groups/"+itoa(group)+"/quota", nil, adminToken)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, float64(1), resp["usedInstances"])
	assert.Equal(t, float64(3), resp["usedBots"])
}
