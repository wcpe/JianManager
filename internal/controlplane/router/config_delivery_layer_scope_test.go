package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// config_delivery_layer_scope_test.go —— 配置交付的**层作用域**授权回归。
//
// 【缺陷现场（主审实证）】selector 只选自己可写的实例（完全合法），却把
// targetLayerScopeId 指向别人的组织分组。建单 201，预置 completed 后
// POST /rollback 得 200，且 RollbackLayer 向**那个别人的组层**追加了一个版本
// （实测 foreign 组层 2 版 → 3 版）。
//
// 【覆盖边界】只断言「跨配置层 DB 写」，不涉及 Worker 落盘（无真实实例进程）。
//
// 【注意两个 group 不是同一个 ID 空间】这里的 org 是 InstanceGroupNode（组织树），
// 不是 RBAC 用户组。二者的 ID 空间完全独立，混淆会让用例变成空转。

// newDeliveryLayerScopeFixture 造两个互不相干的世界：
//   - own：alice 所在 RBAC 组下的实例 + 一个组织分组；
//   - foreign：别人的 RBAC 组 + 组织分组（alice 不可见）。
func newDeliveryLayerScopeFixture(t *testing.T) (r *gin.Engine, db *gorm.DB, adminTok, aliceTok string, ownOrg, foreignOrg, ownInst, foreignInst uint) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db = setupTestDB(t)
	r = setupTestRouter(db)
	adminTok = getAdminToken(t, r)
	node := createTestNode(t, db)

	ownRbac := createGroupViaAPI(t, r, adminTok, "交付-本组")
	foreignRbac := createGroupViaAPI(t, r, adminTok, "交付-他组")
	ownOrg = createInstanceGroupNodeViaAPI(t, r, adminTok, "交付-本组织")
	foreignOrg = createInstanceGroupNodeViaAPI(t, r, adminTok, "交付-他组织")

	ownInst = makeInstanceInGroups(t, db, node.ID, ownRbac, ownOrg, "dl-own", model.InstanceStatusRunning)
	foreignInst = makeInstanceInGroups(t, db, node.ID, foreignRbac, foreignOrg, "dl-foreign", model.InstanceStatusRunning)

	// 直接用管理员 API 建成员再登录：不用 getMemberToken，
	// 因为它内部会调 loginTestAdmin 重建管理员，与本 fixture 已初始化的库冲突（409）。
	w := makeRequest(r, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "alice", "password": "password123",
		"role": model.RoleMember, "status": model.UserStatusActive,
	}, adminTok)
	require.Equal(t, http.StatusCreated, w.Code, "创建 alice 失败: %s", w.Body.String())
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminTok, ownRbac, aliceID, model.GroupMemberRoleMember)
	aliceTok = loginUserToken(t, r, "alice", "password123")
	return r, db, adminTok, aliceTok, ownOrg, foreignOrg, ownInst, foreignInst
}

// countOwnLayerVersions 统计某组织分组层的版本数。
func countOwnLayerVersions(t *testing.T, db *gorm.DB, cfgID, orgID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.ConfigLayerVersion{}).
		Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?", cfgID, model.ConfigScopeGroup, orgID).
		Count(&n).Error)
	return n
}

// TestDelivery_CreateRejectsForeignLayerScope 反控：targetLayerScopeId 指向别人的组织组必须被拒。
func TestDelivery_CreateRejectsForeignLayerScope(t *testing.T) {
	r, _, adminTok, aliceTok, _, foreignOrg, ownInst, _ := newDeliveryLayerScopeFixture(t)

	w := makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name": "dl.properties", "format": "properties",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cfgID := uint(parseJSON(t, w)["id"].(float64))

	// 【关键】目标层必须已有版本，否则 Create 会因 layerHeads 报 404，
	// 那样"被拒"就不是因为越权，而是因为层不存在——典型的假绿。
	// 这里由管理员给 foreign 层预置一版（模拟别人已经在用这个层）。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/"+itoa(cfgID)+"/layers", map[string]any{
		"scopeType": "group", "scopeId": foreignOrg, "content": "a=1\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, "预置 foreign 层失败: %s", w.Body.String())

	// 反控：alice 把层指向 foreign 组织组。selector 只含自己的实例（合法），
	// 但层作用域越权——这正是主审实证的那条路径。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-deliveries", map[string]any{
		"configFileId":       cfgID,
		"filePath":           "dl.properties",
		"targetLayerLevel":   "group",
		"targetLayerScopeId": foreignOrg,
		"selector":           map[string]any{"instanceIds": []uint{ownInst}},
		"batchMode":          "percent",
		"batchSizes":         []int{100},
		"activationMethod":   "push_only",
	}, aliceTok)
	require.NotEqual(t, http.StatusCreated, w.Code,
		"层作用域越权必须被拒，实际: %s", w.Body.String())
}

// TestDelivery_CreateAllowsOwnLayerScope 正控：指向自己的组织组应放行（不得把功能锁死）。
func TestDelivery_CreateAllowsOwnLayerScope(t *testing.T) {
	r, _, adminTok, aliceTok, ownOrg, _, ownInst, _ := newDeliveryLayerScopeFixture(t)

	w := makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name": "dl2.properties", "format": "properties",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cfgID := uint(parseJSON(t, w)["id"].(float64))

	// 目标层必须已有版本（否则 Create 会 404，正控就测不到放行）。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/"+itoa(cfgID)+"/layers", map[string]any{
		"scopeType": "group", "scopeId": ownOrg, "content": "a=1\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, "预置 own 层失败: %s", w.Body.String())

	w = makeRequest(r, http.MethodPost, "/api/v1/config-deliveries", map[string]any{
		"configFileId":       cfgID,
		"filePath":           "dl2.properties",
		"targetLayerLevel":   "group",
		"targetLayerScopeId": ownOrg,
		"selector":           map[string]any{"instanceIds": []uint{ownInst}},
		"batchMode":          "percent",
		"batchSizes":         []int{100},
		"activationMethod":   "push_only",
	}, aliceTok)
	require.Equal(t, http.StatusCreated, w.Code,
		"正控：自己的组织组必须能建单，实际: %s", w.Body.String())
}

// TestDelivery_RollbackCannotWriteForeignLayer 核心回归（主审实证路径）。
//
// 场景：已存在的单带着「foreign 层引用」（用管理员建单模拟——管理员可任意指定层，
// 而 alice 只能看自己的实例）。alice 发整单回滚必须被拦，
// **不得**向 foreign 组织组追加配置层版本。
//
// 覆盖边界：只断言配置层 DB 写入，不涉及 Worker 落盘。
func TestDelivery_RollbackCannotWriteForeignLayer(t *testing.T) {
	r, db, adminTok, aliceTok, _, foreignOrg, ownInst, _ := newDeliveryLayerScopeFixture(t)

	w := makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name": "dl3.properties", "format": "properties",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cfgID := uint(parseJSON(t, w)["id"].(float64))

	// foreign 组层已有两版（模拟别人在用）。
	for i, content := range []string{"a=1\n", "a=2\n"} {
		require.NoError(t, db.Create(&model.ConfigLayerVersion{
			ConfigFileID: cfgID, ScopeLevel: model.ConfigScopeGroup, ScopeRefID: foreignOrg,
			VersionNo: i + 1, Content: content, ContentHash: content,
		}).Error)
	}
	nBefore := countOwnLayerVersions(t, db, cfgID, foreignOrg)
	require.Equal(t, int64(2), nBefore, "前提：foreign 层应有 2 版")

	// 管理员建单：层指向 foreign，selector 只选 alice 的实例。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-deliveries", map[string]any{
		"configFileId":       cfgID,
		"filePath":           "dl3.properties",
		"targetLayerLevel":   "group",
		"targetLayerScopeId": foreignOrg,
		"selector":           map[string]any{"instanceIds": []uint{ownInst}},
		"batchMode":          "percent",
		"batchSizes":         []int{100},
		"activationMethod":   "push_only",
	}, adminTok)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	orderID := uint(parseJSON(t, w)["id"].(float64))

	// 预置 completed（隔离回滚鉴权，不模拟 Worker 执行）。
	require.NoError(t, db.Model(&model.ConfigDeliveryOrder{}).
		Where("id = ?", orderID).Update("status", model.DeliveryStatusCompleted).Error)

	// alice 发整单回滚。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-deliveries/"+itoa(orderID)+"/rollback",
		map[string]any{}, aliceTok)
	require.NotEqual(t, http.StatusOK, w.Code,
		"整单回滚指向他人的配置层必须被拒，实际: %s", w.Body.String())

	// 最关键的断言：foreign 组层不得多出版本。
	nAfter := countOwnLayerVersions(t, db, cfgID, foreignOrg)
	require.Equal(t, nBefore, nAfter,
		"跨配置层写入必须被阻断：foreign 组层由 %d 版变为 %d 版", nBefore, nAfter)
}

// TestDelivery_RollbackAllowsOwnLayer 正控：层指向自己的组织组时整单回滚应放行。
func TestDelivery_RollbackAllowsOwnLayer(t *testing.T) {
	r, db, adminTok, aliceTok, ownOrg, _, ownInst, _ := newDeliveryLayerScopeFixture(t)

	w := makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name": "dl4.properties", "format": "properties",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cfgID := uint(parseJSON(t, w)["id"].(float64))

	// 自己的组层先有一版（回滚需要 FromLayerVersionID）。
	require.NoError(t, db.Create(&model.ConfigLayerVersion{
		ConfigFileID: cfgID, ScopeLevel: model.ConfigScopeGroup, ScopeRefID: ownOrg,
		VersionNo: 1, Content: "a=1\n", ContentHash: "a=1\n",
	}).Error)

	w = makeRequest(r, http.MethodPost, "/api/v1/config-deliveries", map[string]any{
		"configFileId":       cfgID,
		"filePath":           "dl4.properties",
		"targetLayerLevel":   "group",
		"targetLayerScopeId": ownOrg,
		"selector":           map[string]any{"instanceIds": []uint{ownInst}},
		"batchMode":          "percent",
		"batchSizes":         []int{100},
		"activationMethod":   "push_only",
	}, aliceTok)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	orderID := uint(parseJSON(t, w)["id"].(float64))

	require.NoError(t, db.Model(&model.ConfigDeliveryOrder{}).
		Where("id = ?", orderID).Update("status", model.DeliveryStatusCompleted).Error)

	w = makeRequest(r, http.MethodPost, "/api/v1/config-deliveries/"+itoa(orderID)+"/rollback",
		map[string]any{}, aliceTok)
	require.Equal(t, http.StatusOK, w.Code,
		"正控：自己的层必须能回滚，实际: %s", w.Body.String())
}
