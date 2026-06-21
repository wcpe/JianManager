package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// makeBot 直接向库插入一个指定状态/行为的 Bot，返回其 ID。
func makeBot(t *testing.T, db *gorm.DB, instanceID uint, name string, status model.BotStatus, behavior string) uint {
	t.Helper()
	b := &model.Bot{
		InstanceID: instanceID,
		Name:       name,
		Status:     status,
		Behavior:   behavior,
	}
	require.NoError(t, db.Create(b).Error)
	return b.ID
}

// parseBotList 解析分页列表响应 {items,total,page,pageSize}。
func parseBotList(t *testing.T, w *httptest.ResponseRecorder) (items []interface{}, total, page, pageSize int) {
	t.Helper()
	m := parseJSON(t, w)
	if arr, ok := m["items"].([]interface{}); ok {
		items = arr
	}
	total = int(m["total"].(float64))
	page = int(m["page"].(float64))
	pageSize = int(m["pageSize"].(float64))
	return
}

// --- 分页 ---

func TestBot_List_Pagination(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))
	createBotsInDB(t, db, inst, 25)

	tests := []struct {
		name          string
		query         string
		wantItems     int
		wantTotal     int
		wantPage      int
		wantPageSize  int
	}{
		{"默认分页", "", 20, 25, 1, 20},
		{"第二页", "?page=2&pageSize=20", 5, 25, 2, 20},
		{"自定义页大小", "?page=1&pageSize=10", 10, 25, 1, 10},
		{"页码越界归一", "?page=0", 20, 25, 1, 20},
		{"页大小超上限裁剪", "?pageSize=1000", 25, 25, 1, 100},
		{"超出范围页空列表", "?page=99&pageSize=20", 0, 25, 99, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := makeRequest(r, "GET", "/api/v1/bots"+tt.query, nil, token)
			require.Equal(t, http.StatusOK, w.Code)
			items, total, page, pageSize := parseBotList(t, w)
			assert.Len(t, items, tt.wantItems)
			assert.Equal(t, tt.wantTotal, total)
			assert.Equal(t, tt.wantPage, page)
			assert.Equal(t, tt.wantPageSize, pageSize)
		})
	}
}

// --- 过滤 ---

func TestBot_List_Filters(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	nodeA := createTestNodeWithSuffix(t, db, "nodeA")
	nodeB := createTestNodeWithSuffix(t, db, "nodeB")
	g := createGroupViaAPI(t, r, token, "g")
	instA := createInstanceViaAPI(t, r, token, nodeA.ID, g)
	instB := createInstanceViaAPI(t, r, token, nodeB.ID, g)

	makeBot(t, db, instA, "guard-1", model.BotStatusConnected, "guard")
	makeBot(t, db, instA, "follow-1", model.BotStatusConnecting, "follow")
	makeBot(t, db, instB, "guard-2", model.BotStatusConnected, "guard")
	makeBot(t, db, instB, "idle-1", model.BotStatusError, "idle")

	tests := []struct {
		name      string
		query     string
		wantTotal int
	}{
		{"按实例", "?instanceId=" + itoa(instA), 2},
		{"按节点", "?nodeId=" + itoa(nodeB.ID), 2},
		{"按状态", "?status=connected", 2},
		{"按行为", "?behavior=guard", 2},
		{"关键字匹配名", "?q=follow", 1},
		{"组合实例+状态", "?instanceId=" + itoa(instB) + "&status=error", 1},
		{"无匹配", "?behavior=patrol", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := makeRequest(r, "GET", "/api/v1/bots"+tt.query, nil, token)
			require.Equal(t, http.StatusOK, w.Code)
			_, total, _, _ := parseBotList(t, w)
			assert.Equal(t, tt.wantTotal, total)
		})
	}
}

// --- 摘要 ---

func TestBot_Summary_Global(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))
	makeBot(t, db, inst, "b1", model.BotStatusConnected, "guard")
	makeBot(t, db, inst, "b2", model.BotStatusConnected, "guard")
	makeBot(t, db, inst, "b3", model.BotStatusConnecting, "idle")
	makeBot(t, db, inst, "b4", model.BotStatusError, "idle")

	w := makeRequest(r, "GET", "/api/v1/bots/summary", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(4), m["total"])
	byStatus := m["byStatus"].(map[string]interface{})
	assert.Equal(t, float64(2), byStatus["connected"])
	assert.Equal(t, float64(1), byStatus["connecting"])
	assert.Equal(t, float64(1), byStatus["error"])
	assert.Nil(t, m["groups"]) // 无 groupBy 不返回 groups
}

func TestBot_Summary_GroupBy(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	nodeA := createTestNodeWithSuffix(t, db, "nodeA")
	nodeB := createTestNodeWithSuffix(t, db, "nodeB")
	g := createGroupViaAPI(t, r, token, "g")
	instA := createInstanceViaAPI(t, r, token, nodeA.ID, g)
	instB := createInstanceViaAPI(t, r, token, nodeB.ID, g)
	makeBot(t, db, instA, "a1", model.BotStatusConnected, "guard")
	makeBot(t, db, instA, "a2", model.BotStatusError, "guard")
	makeBot(t, db, instB, "b1", model.BotStatusConnected, "idle")

	tests := []struct {
		name       string
		groupBy    string
		wantGroups int
		// 校验某个 key 的 total/online
		checkKey    string
		wantTotal   float64
		wantOnline  float64
	}{
		{"按实例", "instance", 2, itoa(instA), 2, 1},
		{"按节点", "node", 2, itoa(nodeB.ID), 1, 1},
		{"按状态", "status", 2, "connected", 2, 2},
		{"按行为", "behavior", 2, "guard", 2, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := makeRequest(r, "GET", "/api/v1/bots/summary?groupBy="+tt.groupBy, nil, token)
			require.Equal(t, http.StatusOK, w.Code)
			m := parseJSON(t, w)
			assert.Equal(t, tt.groupBy, m["groupBy"])
			groups := m["groups"].([]interface{})
			assert.Len(t, groups, tt.wantGroups)

			var found bool
			for _, gi := range groups {
				gm := gi.(map[string]interface{})
				if gm["key"] == tt.checkKey {
					found = true
					assert.Equal(t, tt.wantTotal, gm["total"], "total for key %s", tt.checkKey)
					assert.Equal(t, tt.wantOnline, gm["online"], "online for key %s", tt.checkKey)
				}
			}
			assert.True(t, found, "未找到分组 key=%s", tt.checkKey)
		})
	}
}

func TestBot_Summary_InvalidGroupBy(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/bots/summary?groupBy=color", nil, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- 批量 ---

func TestBot_Batch_Validation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	tests := []struct {
		name string
		body map[string]interface{}
	}{
		{"非法动作", map[string]interface{}{"action": "explode", "ids": []uint{1}}},
		{"无 ids 无 filter", map[string]interface{}{"action": "stop"}},
		{"set-behavior 缺 behavior", map[string]interface{}{"action": "set-behavior", "ids": []uint{1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := makeRequest(r, "POST", "/api/v1/bots/batch", tt.body, token)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// TestBot_Batch_SetBehavior_DBChange set-behavior 即使 Worker 未连接也改 DB 行为，Worker 委托计失败。
func TestBot_Batch_SetBehavior_DBChange(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))
	id1 := makeBot(t, db, inst, "b1", model.BotStatusConnected, "idle")
	id2 := makeBot(t, db, inst, "b2", model.BotStatusConnected, "idle")

	body := map[string]interface{}{
		"action":   "set-behavior",
		"ids":      []uint{id1, id2},
		"behavior": "follow",
	}
	w := makeRequest(r, "POST", "/api/v1/bots/batch", body, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(2), m["requested"])
	// Worker 未连接 → 委托全部失败，但 DB 行为已更新
	assert.Equal(t, float64(2), m["failed"])
	assert.Equal(t, float64(0), m["skipped"])

	var b model.Bot
	require.NoError(t, db.First(&b, id1).Error)
	assert.Equal(t, "follow", b.Behavior)
}

// TestBot_Batch_Delete 批量删除：DB 行被软删，Worker 委托失败不阻塞删除。
func TestBot_Batch_Delete(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))
	id1 := makeBot(t, db, inst, "b1", model.BotStatusConnected, "idle")

	body := map[string]interface{}{"action": "delete", "ids": []uint{id1}}
	w := makeRequest(r, "POST", "/api/v1/bots/batch", body, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(1), m["requested"])

	var cnt int64
	db.Model(&model.Bot{}).Where("id = ?", id1).Count(&cnt)
	assert.Equal(t, int64(0), cnt, "Bot 应已软删除")
}

// TestBot_Batch_Stop_SetsStopped 批量 stop：DB 状态置 stopped。
func TestBot_Batch_Stop_SetsStopped(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))
	id1 := makeBot(t, db, inst, "b1", model.BotStatusConnected, "idle")

	body := map[string]interface{}{"action": "stop", "ids": []uint{id1}}
	w := makeRequest(r, "POST", "/api/v1/bots/batch", body, token)
	require.Equal(t, http.StatusOK, w.Code)

	var b model.Bot
	require.NoError(t, db.First(&b, id1).Error)
	assert.Equal(t, model.BotStatusStopped, b.Status)
}

// TestBot_Batch_ByFilter 批量按 filter 选目标。
func TestBot_Batch_ByFilter(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))
	makeBot(t, db, inst, "g1", model.BotStatusConnected, "guard")
	makeBot(t, db, inst, "g2", model.BotStatusConnected, "guard")
	makeBot(t, db, inst, "i1", model.BotStatusConnected, "idle")

	body := map[string]interface{}{
		"action":   "set-behavior",
		"filter":   map[string]interface{}{"behavior": "guard"},
		"behavior": "patrol",
	}
	w := makeRequest(r, "POST", "/api/v1/bots/batch", body, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(2), m["requested"]) // 只命中 2 个 guard
}

// --- 鉴权隔离 ---

// TestBot_List_CrossGroupIsolation 组成员只见有权实例下的 Bot。
func TestBot_List_CrossGroupIsolation(t *testing.T) {
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
	makeBot(t, db, instA, "a1", model.BotStatusConnected, "guard")
	makeBot(t, db, instB, "b1", model.BotStatusConnected, "guard")
	makeBot(t, db, instB, "b2", model.BotStatusConnected, "guard")

	// alice 仅见组 A 的 1 个 Bot
	w := makeRequest(r, "GET", "/api/v1/bots", nil, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	_, total, _, _ := parseBotList(t, w)
	assert.Equal(t, 1, total)

	// 摘要也只统计组 A
	w = makeRequest(r, "GET", "/api/v1/bots/summary", nil, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(1), m["total"])

	// admin 见全部 3 个
	w = makeRequest(r, "GET", "/api/v1/bots", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code)
	_, adminTotal, _, _ := parseBotList(t, w)
	assert.Equal(t, 3, adminTotal)
}

// TestBot_Batch_CrossGroupIsolation 批量越权 id 被静默剔除并计入 skipped，不泄露存在性。
func TestBot_Batch_CrossGroupIsolation(t *testing.T) {
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
	idA := makeBot(t, db, instA, "a1", model.BotStatusConnected, "idle")
	idB := makeBot(t, db, instB, "b1", model.BotStatusConnected, "idle")

	// alice 批量请求含组 A（有权）+ 组 B（越权）+ 不存在 id
	body := map[string]interface{}{
		"action":   "set-behavior",
		"ids":      []uint{idA, idB, 99999},
		"behavior": "follow",
	}
	w := makeRequest(r, "POST", "/api/v1/bots/batch", body, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	// 仅组 A 的 1 个进入 requested；组 B + 不存在共 2 个计 skipped
	assert.Equal(t, float64(1), m["requested"])
	assert.Equal(t, float64(2), m["skipped"])

	// 组 B 的 Bot 行为未被修改
	var bB model.Bot
	require.NoError(t, db.First(&bB, idB).Error)
	assert.Equal(t, "idle", bB.Behavior)
}

// TestBot_Summary_EmptyAccess 属于某个空组（组内无实例）的成员，摘要 total=0、列表为空。
// 注：完全不属于任何组的用户没有 bot:read 权限（返回 403），不在本用例覆盖范围。
func TestBot_Summary_EmptyAccess(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	// admin 建组 B 并放一个 Bot
	groupB := createGroupViaAPI(t, r, adminToken, "组B")
	instB := createInstanceViaAPI(t, r, adminToken, 1, groupB)
	makeBot(t, db, instB, "b1", model.BotStatusConnected, "idle")

	// alice 属于一个无实例的空组 A → 有 bot:read 但可见集合为空
	groupA := createGroupViaAPI(t, r, adminToken, "空组A")
	aliceToken := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	w := makeRequest(r, "GET", "/api/v1/bots/summary", nil, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(0), m["total"])

	w = makeRequest(r, "GET", "/api/v1/bots", nil, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	_, total, _, _ := parseBotList(t, w)
	assert.Equal(t, 0, total)
}
