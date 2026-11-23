package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 组织分组写路径的目标组归属守卫回归（requireGroupWritable）。
//
// 【缺陷现场】写分组树原本只校验两件事：全局 instance:write 节点，
// 以及「成员实例可写」。**分组节点本身没有任何归属校验**，于是只属于 A 组（RBAC）的运维，
// 持 instance:write 就能：
//   - Create(parentId=<别人组的子分组>)：把新组挂到无关树上；
//   - Update(id=<别人组>)：改名或把别人组整个搬走；
//   - Delete(id=<别人组>)：删掉别人的分类节点；
//   - AddMembers/RemoveMembers(id=<别人组>)：往别人的组里塞/抽实例。
//
// 本文件逐个端点断言「非本组运维被 404 拒绝」，并验证空组仍可写（不得把功能锁死）。

// newGroupScopeFixture 建两个互不相干的组织分组：own（含调用者可管实例）与 other（含别人实例）。
func newGroupScopeFixture(t *testing.T) (r *gin.Engine, tok string, ownGroupID, otherGroupID, otherInstanceID uint) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r = setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	// 两个 RBAC 用户组：调用者在 ownRbac，别人在 otherRbac。
	ownRbac := createGroupViaAPI(t, r, adminTok, "守卫测试-本组")
	otherRbac := createGroupViaAPI(t, r, adminTok, "守卫测试-他组")

	node := createTestNode(t, db)

	// 实例 A 属于 ownRbac（调用者可管）；实例 B 属于 otherRbac（调用者不可管）。
	ownInst := &model.Instance{
		Name: "guard-own", NodeID: node.ID,
		Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(ownInst).Error)
	otherInst := &model.Instance{
		Name: "guard-other", NodeID: node.ID,
		Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(otherInst).Error)

	// 实例↔RBAC 用户组的归属在关联表 GroupInstance 里（AuthzService 就是这么查的）。
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: ownRbac, InstanceID: ownInst.ID}).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: otherRbac, InstanceID: otherInst.ID}).Error)

	// 两个组织分组（树节点），分别挂上各自的实例。
	w := makeRequest(r, http.MethodPost, "/api/v1/instance-groups", map[string]interface{}{"name": "守卫-本组树"}, adminTok)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	ownGroupID = uint(parseJSON(t, w)["id"].(float64))

	w = makeRequest(r, http.MethodPost, "/api/v1/instance-groups", map[string]interface{}{"name": "守卫-他组树"}, adminTok)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	otherGroupID = uint(parseJSON(t, w)["id"].(float64))

	w = makeRequest(r, http.MethodPost, "/api/v1/instance-groups/"+itoa(ownGroupID)+"/members",
		map[string]interface{}{"instanceIds": []uint{ownInst.ID}}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = makeRequest(r, http.MethodPost, "/api/v1/instance-groups/"+itoa(otherGroupID)+"/members",
		map[string]interface{}{"instanceIds": []uint{otherInst.ID}}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 调用者：持 instance.read/write 节点，但只属于 ownRbac。
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	tok = gateUserToken(t, r, db, authz, userSvc, "guarduser",
		[]string{"instance.read", "instance.write", "instance.delete"}, ownRbac)

	return r, tok, ownGroupID, otherGroupID, otherInst.ID
}

// TestInstanceGroup_WriteGate_CrossGroupRejected 五个写端点对「别人的组」必须 404。
func TestInstanceGroup_WriteGate_CrossGroupRejected(t *testing.T) {
	r, tok, ownGroupID, otherGroupID, otherInstanceID := newGroupScopeFixture(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"改别人的组名", http.MethodPut, "/api/v1/instance-groups/" + itoa(otherGroupID),
			map[string]any{"name": "被改名了"}},
		{"把新组挂到别人组下", http.MethodPost, "/api/v1/instance-groups",
			map[string]any{"name": "寄生虫", "parentId": otherGroupID}},
		{"把别人的组搬到自己组下", http.MethodPut, "/api/v1/instance-groups/" + itoa(otherGroupID),
			map[string]any{"parentId": ownGroupID}},
		{"删别人的组", http.MethodDelete, "/api/v1/instance-groups/" + itoa(otherGroupID), nil},
		{"往别人组里加实例", http.MethodPost, "/api/v1/instance-groups/" + itoa(otherGroupID) + "/members",
			map[string]any{"instanceIds": []uint{otherInstanceID}}},
		{"从别人组里抽实例", http.MethodDelete, "/api/v1/instance-groups/" + itoa(otherGroupID) + "/members",
			map[string]any{"instanceIds": []uint{otherInstanceID}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := makeRequest(r, tc.method, tc.path, tc.body, tok)
			require.Equal(t, http.StatusNotFound, w.Code,
				"%s 应被 404 拒绝（跨组写分组）: %s", tc.name, w.Body.String())
		})
	}
}

// TestInstanceGroup_WriteGate_OwnGroupAllowed 自己的组仍可正常维护（不得误伤）。
func TestInstanceGroup_WriteGate_OwnGroupAllowed(t *testing.T) {
	r, tok, ownGroupID, _, _ := newGroupScopeFixture(t)

	w := makeRequest(r, http.MethodPut, "/api/v1/instance-groups/"+itoa(ownGroupID),
		map[string]any{"name": "守卫-本组树-改名"}, tok)
	require.Equal(t, http.StatusOK, w.Code, "自己的组应可改名: %s", w.Body.String())

	// 在自有组下建子组（父组可写）。
	w = makeRequest(r, http.MethodPost, "/api/v1/instance-groups",
		map[string]any{"name": "本组子组", "parentId": ownGroupID}, tok)
	require.Equal(t, http.StatusCreated, w.Code, "应可在自有组下建子组: %s", w.Body.String())
}

// TestInstanceGroup_WriteGate_EmptyGroupAllowed 空组仍可写（否则非管理员无法建分类骨架）。
func TestInstanceGroup_WriteGate_EmptyGroupAllowed(t *testing.T) {
	r, tok, ownGroupID, _, _ := newGroupScopeFixture(t)

	// 在自有组下建一个空子组，再对它改名/删除——空组没有实例可越权，应放行。
	w := makeRequest(r, http.MethodPost, "/api/v1/instance-groups",
		map[string]any{"name": "空子组", "parentId": ownGroupID}, tok)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	emptyID := uint(parseJSON(t, w)["id"].(float64))

	w = makeRequest(r, http.MethodPut, "/api/v1/instance-groups/"+itoa(emptyID),
		map[string]any{"name": "空子组-改名"}, tok)
	require.Equal(t, http.StatusOK, w.Code, "空组改名应放行: %s", w.Body.String())

	w = makeRequest(r, http.MethodDelete, "/api/v1/instance-groups/"+itoa(emptyID), nil, tok)
	require.Equal(t, http.StatusNoContent, w.Code, "空组删除应放行: %s", w.Body.String())
}

// TestInstanceGroup_WriteGate_RootCreateAllowed 建根组（parentId=null）不受组守卫影响。
func TestInstanceGroup_WriteGate_RootCreateAllowed(t *testing.T) {
	r, tok, _, _, _ := newGroupScopeFixture(t)
	w := makeRequest(r, http.MethodPost, "/api/v1/instance-groups",
		map[string]any{"name": "新的根组"}, tok)
	require.Equal(t, http.StatusCreated, w.Code, "建根组应放行（父=根不归任何人）: %s", w.Body.String())
}
