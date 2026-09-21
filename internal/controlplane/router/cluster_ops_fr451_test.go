package router

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// makeMCInstanceInGroup 插入一个 MC 语义实例（minecraft_java/backend）并分配到组，返回 ID。
// FR-451 画像门控：仅 MC 语义实例才有 startup.*/props.* 受管项。
func makeMCInstanceInGroup(t *testing.T, db *gorm.DB, nodeID, groupID uint, name string, status model.InstanceStatus) uint {
	t.Helper()
	inst := &model.Instance{
		NodeID:       nodeID,
		Name:         name,
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "java -jar server.jar",
		Status:       status,
	}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: groupID, InstanceID: inst.ID}).Error)
	return inst.ID
}

// TestConfigSurface_ReadAndUpdate 覆盖 FR-451：受管项清单读 + 内联写入启动命令并持久化。
func TestConfigSurface_ReadAndUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "surfg")
	id := makeMCInstanceInGroup(t, db, node.ID, g, "surf1", model.InstanceStatusStopped)

	w := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(id)+"/configs/surface", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	items, ok := body["items"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, items)

	v := "java -Xmx1G -jar server.jar nogui"
	w = makeRequest(r, http.MethodPut, "/api/v1/instances/"+itoa(id)+"/configs/surface", map[string]any{
		"items": []map[string]any{
			{"itemKey": "startup.command", "source": "inline", "inlineValue": v},
		},
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got model.Instance
	require.NoError(t, db.First(&got, id).Error)
	require.Equal(t, v, got.StartCommand, "内联写入应持久化到实例启动命令")
}

// TestConfigSurface_NonMCSemanticEmpty 覆盖 FR-451 验收 #8：generic 实例不呈现 MC 专有配置项。
func TestConfigSurface_NonMCSemanticEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "gencfg")
	// makeInstanceInGroup 建的是 generic/universal 实例。
	id := makeInstanceInGroup(t, db, node.ID, g, "gen1", model.InstanceStatusStopped)

	w := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(id)+"/configs/surface", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	items, ok := parseJSON(t, w)["items"].([]interface{})
	require.True(t, ok)
	require.Empty(t, items, "generic 实例不应出现 MC 专有配置项")

	w = makeRequest(r, http.MethodPut, "/api/v1/instances/"+itoa(id)+"/configs/surface", map[string]any{
		"items": []map[string]any{{"itemKey": "startup.command", "source": "inline", "inlineValue": "x"}},
	}, adminTok)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "generic 实例写入 MC 受管项应被拒")
}

// TestConfigSurface_StartupFileRejected 覆盖 FR-451：startup.* 不支持文件引用来源。
func TestConfigSurface_StartupFileRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "sfr")
	id := makeMCInstanceInGroup(t, db, node.ID, g, "sfc", model.InstanceStatusStopped)

	w := makeRequest(r, http.MethodPut, "/api/v1/instances/"+itoa(id)+"/configs/surface", map[string]any{
		"items": []map[string]any{{"itemKey": "startup.command", "source": "file", "filePath": "cmd.txt"}},
	}, adminTok)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

// TestInstanceRolling_CreateAndGet 覆盖 FR-457：滚动编排端点可达 + 会话可查。
func TestInstanceRolling_CreateAndGet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "rollg")
	id := makeInstanceInGroup(t, db, node.ID, g, "roll1", model.InstanceStatusRunning)

	// 用 command 动作：无 Worker 时委托失败也不回写实例状态，避免测试收尾的异步写入。
	w := makeRequest(r, http.MethodPost, "/api/v1/instances/rolling", map[string]any{
		"action": "command", "ids": []uint{id}, "command": "say hi",
		"batchSize": 1, "batchIntervalSec": 0,
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	require.NotZero(t, body["id"])
	opID := uint(body["id"].(float64))

	w = makeRequest(r, http.MethodGet, "/api/v1/instances/rolling/"+itoa(opID), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 等编排到达终态，避免后台 goroutine 越过测试 DB 关闭时刻。
	require.Eventually(t, func() bool {
		gw := makeRequest(r, http.MethodGet, "/api/v1/instances/rolling/"+itoa(opID), nil, adminTok)
		if gw.Code != http.StatusOK {
			return false
		}
		state, _ := parseJSON(t, gw)["state"].(string)
		return state == "done" || state == "canceled"
	}, 2*time.Second, 20*time.Millisecond)
}

// TestConfigBaseline_Endpoints 覆盖 FR-458：基线创建 / 列出 / 漂移检测端点可达。
func TestConfigBaseline_Endpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "all", "filePath": "server.properties", "content": "server-port=25565\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	require.NotZero(t, body["id"])
	blID := uint(body["id"].(float64))

	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	list := parseJSON(t, w)
	baselines, ok := list["baselines"].([]interface{})
	require.True(t, ok)
	require.Len(t, baselines, 1)

	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blID)+"/drift", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestInstanceRolling_ScopeIsolation 覆盖 FR-457 越权修复（IDOR）：非管理员不能读取/控制
// 目标不在其可访问集合内的编排会话（以 404 隐藏存在性），平台管理员放行。
func TestInstanceRolling_ScopeIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminTok, "rollA")
	groupB := createGroupViaAPI(t, r, adminTok, "rollB")
	aliceTok := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminTok, groupA, aliceID, model.GroupMemberRoleMember)

	instB := makeInstanceInGroup(t, db, node.ID, groupB, "b1", model.InstanceStatusRunning)

	// 管理员对组 B 实例建编排（command 动作，避免异步状态回写干扰）。
	w := makeRequest(r, http.MethodPost, "/api/v1/instances/rolling", map[string]any{
		"action": "command", "ids": []uint{instB}, "command": "say hi", "batchSize": 1,
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	opID := uint(parseJSON(t, w)["id"].(float64))

	// alice（仅组 A）读取组 B 编排 → 404。
	w = makeRequest(r, http.MethodGet, "/api/v1/instances/rolling/"+itoa(opID), nil, aliceTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	// alice 暂停组 B 编排 → 404（不泄露存在性，也不生效）。
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/rolling/"+itoa(opID)+"/pause", nil, aliceTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	// 管理员可见。
	w = makeRequest(r, http.MethodGet, "/api/v1/instances/rolling/"+itoa(opID), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// alice 对自己组内实例建编排并可见。
	instA := makeInstanceInGroup(t, db, node.ID, groupA, "a1", model.InstanceStatusRunning)
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/rolling", map[string]any{
		"action": "command", "ids": []uint{instA}, "command": "say hi", "batchSize": 1,
	}, aliceTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	opA := uint(parseJSON(t, w)["id"].(float64))
	w = makeRequest(r, http.MethodGet, "/api/v1/instances/rolling/"+itoa(opA), nil, aliceTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 等编排到终态，避免后台 goroutine 越过测试 DB 关闭。
	require.Eventually(t, func() bool {
		gw := makeRequest(r, http.MethodGet, "/api/v1/instances/rolling/"+itoa(opA), nil, adminTok)
		if gw.Code != http.StatusOK {
			return false
		}
		state, _ := parseJSON(t, gw)["state"].(string)
		return state == "done" || state == "canceled"
	}, 2*time.Second, 20*time.Millisecond)
}

// createInstanceGroupNodeViaAPI 经平台管理员 API 建「组织树分组」（ADR-033），返回分组 ID。
// 注意：这是配置基线 group: scope 唯一的 id 空间，与用户组（ADR-004）/网络群组（ADR-007）正交。
func createInstanceGroupNodeViaAPI(t *testing.T, r *gin.Engine, adminToken, name string) uint {
	t.Helper()
	w := makeRequest(r, http.MethodPost, "/api/v1/instance-groups", map[string]any{"name": name}, adminToken)
	require.Equalf(t, http.StatusCreated, w.Code, "创建组织树分组失败: %s", w.Body.String())
	return uint(parseJSON(t, w)["id"].(float64))
}

// makeInstanceInGroups 直接插入实例并同时挂到用户组（ADR-004，决定 RBAC 可见性）与
// 组织树分组（ADR-033，决定 group: scope 解析），两套 id 空间互不影响。
func makeInstanceInGroups(t *testing.T, db *gorm.DB, nodeID, userGroupID, orgGroupID uint, name string, status model.InstanceStatus) uint {
	t.Helper()
	id := makeInstanceInGroup(t, db, nodeID, userGroupID, name, status)
	if orgGroupID != 0 {
		require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: orgGroupID, InstanceID: id}).Error)
	}
	return id
}

// TestConfigBaseline_ScopeIsolation 覆盖 FR-458 越权修复：非管理员 list/get/drift/converge 受 scope 过滤。
//
// 关键：`group:<id>` 的 id 是组织树分组（ADR-033 InstanceGroupNode），不是用户组（ADR-004 GroupInstance）。
// 旧版本用「用户组 id」当 scope，实际解析为组织树中不存在的节点（空集），使隔离断言以空集空转通过。
// 本用例改为构造真实的组织树分组，并显式断言 scope 真的解析到目标实例（非空、且是预期那一台）。
func TestConfigBaseline_ScopeIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)

	// 组织树分组（group: scope 的 id 空间）。
	orgB := createInstanceGroupNodeViaAPI(t, r, adminTok, "blB-org")
	orgA := createInstanceGroupNodeViaAPI(t, r, adminTok, "blA-org")

	// 用户组（ADR-004）：alice 仅属 A 组，决定她的可访问实例集合。
	groupA := createGroupViaAPI(t, r, adminTok, "blA")
	groupB := createGroupViaAPI(t, r, adminTok, "blB")
	aliceTok := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminTok, groupA, aliceID, model.GroupMemberRoleMember)

	instA := makeInstanceInGroups(t, db, node.ID, groupA, orgA, "a1", model.InstanceStatusRunning)
	instB := makeInstanceInGroups(t, db, node.ID, groupB, orgB, "b1", model.InstanceStatusRunning)

	// 基线 scope 指向组织树分组 B（其成员实例 instB 不在 alice 的用户组内）。
	scopeB := "group:" + itoa(orgB)
	w := makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": scopeB, "filePath": "server.properties", "content": "server-port=25565\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	blB := uint(parseJSON(t, w)["id"].(float64))

	// 断言 scope 真的解析到 instB（防止「空集假绿」）：管理员漂移检测恰好覆盖这一台。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blB)+"/drift", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	itemsB := parseJSON(t, w)["items"].([]interface{})
	require.Len(t, itemsB, 1, "group:<orgB> 必须解析出 1 台实例，否则本用例空转")
	require.EqualValues(t, instB, itemsB[0].(map[string]interface{})["instanceId"])

	// alice 看不到组 B 基线：list 空、get/drift/converge 404。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines", nil, aliceTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, parseJSON(t, w)["baselines"].([]interface{}), "非管理员不应看到 scope 外的基线")
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blB), nil, aliceTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blB)+"/drift", nil, aliceTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	w = makeRequest(r, http.MethodPost, "/api/v1/config-baselines/"+itoa(blB)+"/converge", nil, aliceTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	// alice 以 scope 外的组织树分组创建/覆盖基线 → 403（scope 解析出的实例不在其可访问集合内）。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": scopeB, "filePath": "ops.properties", "content": "x\n",
	}, aliceTok)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// scope=all 的基线：alice drift 只返回可访问实例（组 A 的 1 台）。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "all", "filePath": "server.properties", "content": "server-port=25565\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	blAll := uint(parseJSON(t, w)["id"].(float64))
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blAll)+"/drift", nil, aliceTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	items := parseJSON(t, w)["items"].([]interface{})
	require.Len(t, items, 1, "非管理员 drift 只覆盖可访问实例")
	require.EqualValues(t, instA, items[0].(map[string]interface{})["instanceId"])

	// 管理员看到全部。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines", nil, adminTok)
	require.Len(t, parseJSON(t, w)["baselines"].([]interface{}), 2)
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blAll)+"/drift", nil, adminTok)
	require.Len(t, parseJSON(t, w)["items"].([]interface{}), 2)
}

// TestConfigBaseline_GroupScopeFailClosed 覆盖修复：`group:<id>` 指向不存在/无成员的分组时不再静默
// 解析为空集（旧行为会创建一条「谁都不覆盖」的基线，并让隔离用例假绿），而是返回可读错误。
func TestConfigBaseline_GroupScopeFailClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)

	countBaselines := func() int64 {
		var cnt int64
		require.NoError(t, db.Model(&model.ConfigBaseline{}).Count(&cnt).Error)
		return cnt
	}

	// 1) 组织树中不存在的 id → 明确报错且不落库。
	w := makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "group:999999", "filePath": "server.properties", "content": "A\n",
	}, adminTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Contains(t, parseJSON(t, w)["message"], "组织树分组")
	require.Zero(t, countBaselines(), "非法 group scope 不得落库")

	// 2) 误填「用户组 id」的典型场景：该 id 在用户组表里真实存在，但不是组织树节点 → 同样拒绝，
	//    不再静默空集（这正是 NEW-ISSUE 描述的误用路径）。
	misFillGroup := uint(0)
	for i := 0; i < 6 && misFillGroup == 0; i++ {
		cand := createGroupViaAPI(t, r, adminTok, "notAnOrg"+itoa(uint(i)))
		var orgCnt int64
		require.NoError(t, db.Model(&model.InstanceGroupNode{}).Where("id = ?", cand).Count(&orgCnt).Error)
		if orgCnt == 0 {
			misFillGroup = cand
		}
	}
	require.NotZero(t, misFillGroup, "未能构造出「组织树中不存在」的用户组 id")
	w = makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "group:" + itoa(misFillGroup), "filePath": "server.properties", "content": "A\n",
	}, adminTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Zero(t, countBaselines())

	// 3) 分组存在但子树内无成员实例 → 与「不存在」区分的明确原因（422）。
	emptyOrg := createInstanceGroupNodeViaAPI(t, r, adminTok, "empty-org")
	w = makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "group:" + itoa(emptyOrg), "filePath": "server.properties", "content": "A\n",
	}, adminTok)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, parseJSON(t, w)["message"], "没有成员实例")
	require.Zero(t, countBaselines())

	// 4) 正常路径仍然可用：有成员实例的分组 → 200，且漂移检测确实覆盖到该实例。
	okOrg := createInstanceGroupNodeViaAPI(t, r, adminTok, "ok-org")
	userGrp := createGroupViaAPI(t, r, adminTok, "ok-user-group")
	instID := makeInstanceInGroups(t, db, node.ID, userGrp, okOrg, "ok1", model.InstanceStatusRunning)
	w = makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "group:" + itoa(okOrg), "filePath": "server.properties", "content": "A\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	blID := uint(parseJSON(t, w)["id"].(float64))
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blID)+"/drift", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	items := parseJSON(t, w)["items"].([]interface{})
	require.Len(t, items, 1)
	require.EqualValues(t, instID, items[0].(map[string]interface{})["instanceId"])

	// 5) 脏数据（分组被删）对管理员可读报错、对非管理员仍隐藏存在性。
	require.NoError(t, db.Delete(&model.InstanceGroupNode{}, okOrg).Error)
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blID)+"/drift", nil, adminTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	aliceTok := getMemberToken(t, r, "alice", "password123")
	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blID), nil, aliceTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// TestConfigBaseline_CreateDeleteScopeGuard 覆盖 FR-458 越权修复：非管理员不能创建/覆盖 scope 超出
// 可访问范围的基线（如 scopeKey:"all"），也不能删除 scope 外基线（以 404 隐藏存在性）。
func TestConfigBaseline_CreateDeleteScopeGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminTok, "guardA")
	groupB := createGroupViaAPI(t, r, adminTok, "guardB")
	aliceTok := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminTok, groupA, aliceID, model.GroupMemberRoleMember)

	instA := makeInstanceInGroup(t, db, node.ID, groupA, "ga1", model.InstanceStatusRunning)
	makeInstanceInGroup(t, db, node.ID, groupB, "gb1", model.InstanceStatusRunning)

	// 平台管理员建 scope=all 平台基线（含组 B 实例）。
	w := makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "all", "filePath": "server.properties", "content": "server-port=25565\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	platformBL := uint(parseJSON(t, w)["id"].(float64))

	// alice（仅组 A）以 scopeKey:"all" 创建/覆盖平台基线 → 403，且平台基线内容未被覆盖。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "all", "filePath": "server.properties", "content": "COMPROMISED\n",
	}, aliceTok)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	var platform model.ConfigBaseline
	require.NoError(t, db.First(&platform, platformBL).Error)
	require.Equal(t, "server-port=25565\n", platform.Content, "越权覆盖不得改动平台基线")

	// alice 对 scope 外实例（组 B）创建基线 → 403。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "all", "filePath": "ops.properties", "content": "x\n",
	}, aliceTok)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// alice 对自身可访问实例创建基线 → 200。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "instance:" + itoa(instA), "filePath": "server.properties", "content": "own=1\n",
	}, aliceTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	aliceBL := uint(parseJSON(t, w)["id"].(float64))

	// alice 删除 scope 外平台基线 → 404，且未删除。
	w = makeRequest(r, http.MethodDelete, "/api/v1/config-baselines/"+itoa(platformBL), nil, aliceTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	var cnt int64
	require.NoError(t, db.Model(&model.ConfigBaseline{}).Where("id = ?", platformBL).Count(&cnt).Error)
	require.EqualValues(t, 1, cnt, "越权删除不得生效")

	// alice 删除自身基线 → 200。
	w = makeRequest(r, http.MethodDelete, "/api/v1/config-baselines/"+itoa(aliceBL), nil, aliceTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 管理员仍可删除平台基线。
	w = makeRequest(r, http.MethodDelete, "/api/v1/config-baselines/"+itoa(platformBL), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestConfigSurface_ProxyNotMCSemantic 覆盖 #E：proxy（用 config.yml）不呈现 server.properties 受管项；
// 默认角色（universal）的 MC 服务端仍呈现（避免误伤手动/MCP 创建的 MC 实例）。
func TestConfigSurface_ProxyNotMCSemantic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "proxycfg")

	mk := func(name string, role model.InstanceRole) uint {
		inst := &model.Instance{
			NodeID: node.ID, Name: name, Type: model.InstanceTypeMinecraftJava, Role: role,
			ProcessType: model.ProcessTypeDirect, StartCommand: "java -jar x.jar",
			Status: model.InstanceStatusStopped,
		}
		require.NoError(t, db.Create(inst).Error)
		require.NoError(t, db.Create(&model.GroupInstance{GroupID: g, InstanceID: inst.ID}).Error)
		return inst.ID
	}
	proxyID := mk("proxy1", model.InstanceRoleProxy)
	universalID := mk("uni1", model.InstanceRoleUniversal)

	w := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(proxyID)+"/configs/surface", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, parseJSON(t, w)["items"].([]interface{}), "proxy 不应出现 server.properties 受管项")

	w = makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(universalID)+"/configs/surface", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotEmpty(t, parseJSON(t, w)["items"].([]interface{}), "默认角色 MC 服务端仍应呈现受管项")
}

// TestInstanceRolling_FilterRatioSubset 覆盖 #B 端到端：filter+instanceIds 模式下 ratio<1 抽样为子集，
// 避免前端误发 ids 导致灰度静默退化为全量。
func TestInstanceRolling_FilterRatioSubset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "ratg")
	ids := make([]uint, 0, 4)
	for i := 0; i < 4; i++ {
		ids = append(ids, makeInstanceInGroup(t, db, node.ID, g, "r"+itoa(uint(i)), model.InstanceStatusRunning))
	}

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/rolling", map[string]any{
		"action": "command", "command": "say hi",
		"filter": map[string]any{"instanceIds": ids},
		"ratio":  0.5, "batchSize": 1,
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	targets, ok := body["targets"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, targets)
	require.Less(t, len(targets), len(ids), "ratio<1 时目标应为真子集")

	allowed := map[float64]bool{}
	for _, id := range ids {
		allowed[float64(id)] = true
	}
	for _, tg := range targets {
		require.True(t, allowed[tg.(float64)], "抽样目标必须是请求集合的子集")
	}

	// 等编排到终态，避免后台 goroutine 越过测试 DB 关闭时刻。
	opID := uint(body["id"].(float64))
	require.Eventually(t, func() bool {
		gw := makeRequest(r, http.MethodGet, "/api/v1/instances/rolling/"+itoa(opID), nil, adminTok)
		if gw.Code != http.StatusOK {
			return false
		}
		state, _ := parseJSON(t, gw)["state"].(string)
		return state == "done" || state == "canceled"
	}, 2*time.Second, 20*time.Millisecond)
}
