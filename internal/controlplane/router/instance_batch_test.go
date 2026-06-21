package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// makeInstanceInGroup 直接插入一个指定节点/状态的实例并分配到组，返回其 ID。
// 用于构造批量目标（含 RUNNING 等非默认状态，createInstanceViaAPI 只能建 STOPPED）。
func makeInstanceInGroup(t *testing.T, db *gorm.DB, nodeID, groupID uint, name string, status model.InstanceStatus) uint {
	t.Helper()
	inst := &model.Instance{
		NodeID:       nodeID,
		Name:         name,
		Type:         model.InstanceTypeGeneric,
		Role:         model.InstanceRoleUniversal,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "java -jar server.jar",
		Status:       status,
	}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: groupID, InstanceID: inst.ID}).Error)
	return inst.ID
}

// --- 校验 ---

func TestInstanceBatch_Validation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	tests := []struct {
		name string
		body map[string]interface{}
	}{
		{"非法动作", map[string]interface{}{"action": "explode", "ids": []uint{1}}},
		{"无 ids 无 filter", map[string]interface{}{"action": "stop"}},
		{"command 缺 command", map[string]interface{}{"action": "command", "ids": []uint{1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := makeRequest(r, "POST", "/api/v1/instances/batch", tt.body, token)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// --- 计数：Worker 未连接 ---

// TestInstanceBatch_Stop_WorkerOffline 批量 stop：测试无 Worker 连接，委托全部失败计入 failed。
func TestInstanceBatch_Stop_WorkerOffline(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id1 := makeInstanceInGroup(t, db, node.ID, g, "i1", model.InstanceStatusRunning)
	id2 := makeInstanceInGroup(t, db, node.ID, g, "i2", model.InstanceStatusRunning)

	body := map[string]interface{}{"action": "stop", "ids": []uint{id1, id2}}
	w := makeRequest(r, "POST", "/api/v1/instances/batch", body, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(2), m["requested"])
	assert.Equal(t, float64(0), m["succeeded"])
	assert.Equal(t, float64(2), m["failed"]) // Worker 未连接 → 委托全失败
	assert.Equal(t, float64(0), m["skipped"])

	// 委托失败回写 CRASHED（与单实例 delegateToWorker 语义一致）
	var inst model.Instance
	require.NoError(t, db.First(&inst, id1).Error)
	assert.Equal(t, model.InstanceStatusCrashed, inst.Status)

	// errors 含失败明细
	errs := m["errors"].([]interface{})
	assert.Len(t, errs, 2)
}

// TestInstanceBatch_Command_OnlyRunning command 动作只对 RUNNING 实例委托，非运行实例计失败。
func TestInstanceBatch_Command_OnlyRunning(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	running := makeInstanceInGroup(t, db, node.ID, g, "run", model.InstanceStatusRunning)
	stopped := makeInstanceInGroup(t, db, node.ID, g, "stop", model.InstanceStatusStopped)

	body := map[string]interface{}{
		"action":  "command",
		"ids":     []uint{running, stopped},
		"command": "say hello",
	}
	w := makeRequest(r, "POST", "/api/v1/instances/batch", body, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(2), m["requested"])
	// 两者都失败：running 因 Worker 未连接，stopped 因状态校验直接拒绝；关键是都不 panic 且计数正确
	assert.Equal(t, float64(2), m["failed"])
	assert.Equal(t, float64(0), m["skipped"])

	// stopped 实例的失败明细应说明未运行
	errs := m["errors"].([]interface{})
	var sawNotRunning bool
	for _, e := range errs {
		em := e.(map[string]interface{})
		if uint(em["instanceId"].(float64)) == stopped {
			assert.Contains(t, em["error"], "未运行")
			sawNotRunning = true
		}
	}
	assert.True(t, sawNotRunning, "应包含未运行实例的失败明细")
}

// --- 目标解析 ---

// TestInstanceBatch_ByFilter 批量按 filter（status）选目标。
func TestInstanceBatch_ByFilter(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	makeInstanceInGroup(t, db, node.ID, g, "r1", model.InstanceStatusRunning)
	makeInstanceInGroup(t, db, node.ID, g, "r2", model.InstanceStatusRunning)
	makeInstanceInGroup(t, db, node.ID, g, "s1", model.InstanceStatusStopped)

	body := map[string]interface{}{
		"action": "stop",
		"filter": map[string]interface{}{"status": "RUNNING"},
	}
	w := makeRequest(r, "POST", "/api/v1/instances/batch", body, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(2), m["requested"]) // 只命中 2 个 RUNNING
}

// TestInstanceBatch_ByFilter_Node 批量按 filter（nodeId）选目标，只命中该节点。
func TestInstanceBatch_ByFilter_Node(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	nodeA := createTestNodeWithSuffix(t, db, "nodeA")
	nodeB := createTestNodeWithSuffix(t, db, "nodeB")
	g := createGroupViaAPI(t, r, token, "g")
	makeInstanceInGroup(t, db, nodeA.ID, g, "a1", model.InstanceStatusRunning)
	makeInstanceInGroup(t, db, nodeB.ID, g, "b1", model.InstanceStatusRunning)
	makeInstanceInGroup(t, db, nodeB.ID, g, "b2", model.InstanceStatusRunning)

	body := map[string]interface{}{
		"action": "kill",
		"filter": map[string]interface{}{"nodeId": nodeB.ID},
	}
	w := makeRequest(r, "POST", "/api/v1/instances/batch", body, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(2), m["requested"]) // 只命中节点 B 的 2 个
}

// --- 鉴权隔离 ---

// TestInstanceBatch_CrossGroupIsolation 批量越权 id 被静默剔除并计入 skipped，不泄露存在性。
func TestInstanceBatch_CrossGroupIsolation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminToken, "组A")
	groupB := createGroupViaAPI(t, r, adminToken, "组B")

	aliceToken := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	idA := makeInstanceInGroup(t, db, node.ID, groupA, "a1", model.InstanceStatusRunning)
	idB := makeInstanceInGroup(t, db, node.ID, groupB, "b1", model.InstanceStatusRunning)

	// alice 批量请求含组 A（有权）+ 组 B（越权）+ 不存在 id
	body := map[string]interface{}{
		"action": "stop",
		"ids":    []uint{idA, idB, 99999},
	}
	w := makeRequest(r, "POST", "/api/v1/instances/batch", body, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	// 仅组 A 的 1 个进入 requested；组 B + 不存在共 2 个计 skipped
	assert.Equal(t, float64(1), m["requested"])
	assert.Equal(t, float64(2), m["skipped"])

	// 组 B 的实例状态未被改动（仍 RUNNING，没有被误转 CRASHED）
	var instB model.Instance
	require.NoError(t, db.First(&instB, idB).Error)
	assert.Equal(t, model.InstanceStatusRunning, instB.Status)
}

// TestInstanceBatch_ByFilter_ScopeConverged 非管理员 filter 模式只命中可见组实例。
func TestInstanceBatch_ByFilter_ScopeConverged(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminToken, "组A")
	groupB := createGroupViaAPI(t, r, adminToken, "组B")

	aliceToken := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	makeInstanceInGroup(t, db, node.ID, groupA, "a1", model.InstanceStatusRunning)
	makeInstanceInGroup(t, db, node.ID, groupB, "b1", model.InstanceStatusRunning)
	makeInstanceInGroup(t, db, node.ID, groupB, "b2", model.InstanceStatusRunning)

	// alice 用 filter 选「所有 RUNNING」，应只命中组 A 的 1 个（组 B 在 SQL 层被收敛掉）
	body := map[string]interface{}{
		"action": "kill",
		"filter": map[string]interface{}{"status": "RUNNING"},
	}
	w := makeRequest(r, "POST", "/api/v1/instances/batch", body, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(1), m["requested"])
}

// TestInstanceBatch_Forbidden 无 instance:operate 权限（不属于任何组的用户）被拒。
func TestInstanceBatch_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r) // 完成 setup，使后续 register 走普通注册
	// bob 不属于任何组 → 无 instance:operate
	bobToken := getMemberToken(t, r, "bob", "password123")

	body := map[string]interface{}{"action": "stop", "ids": []uint{1}}
	w := makeRequest(r, "POST", "/api/v1/instances/batch", body, bobToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}
