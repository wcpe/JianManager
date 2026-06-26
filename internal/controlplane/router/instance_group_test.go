package router

import (
	"net/http"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestInstanceGroup_HTTPLifecycle(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	// 建根组
	w := makeRequest(r, "POST", "/api/v1/instance-groups", map[string]interface{}{"name": "亚洲区"}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("建根组失败: status=%d body=%s", w.Code, w.Body.String())
	}
	root := parseJSON(t, w)
	rootID := uint(root["id"].(float64))

	// 嵌套子组
	w = makeRequest(r, "POST", "/api/v1/instance-groups", map[string]interface{}{"name": "生存", "parentId": rootID}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("嵌套子组失败: status=%d body=%s", w.Code, w.Body.String())
	}
	child := parseJSON(t, w)
	childID := uint(child["id"].(float64))

	// 改名
	w = makeRequest(r, "PUT", "/api/v1/instance-groups/"+itoa(childID), map[string]interface{}{"name": "生存服"}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("改名失败: status=%d body=%s", w.Code, w.Body.String())
	}

	// 移动成环（根移到子下）→ 409
	w = makeRequest(r, "PUT", "/api/v1/instance-groups/"+itoa(rootID), map[string]interface{}{"parentId": childID}, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("移动成环应 409，得 status=%d body=%s", w.Code, w.Body.String())
	}

	// 删非空根（有子组）→ 409
	w = makeRequest(r, "DELETE", "/api/v1/instance-groups/"+itoa(rootID), nil, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("删非空组应 409，得 status=%d body=%s", w.Code, w.Body.String())
	}

	// 加成员（造实例）
	node := createTestNode(t, db)
	inst := &model.Instance{
		Name: "srv-1", NodeID: node.ID, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	if err := db.Create(inst).Error; err != nil {
		t.Fatalf("造实例失败: %v", err)
	}
	w = makeRequest(r, "POST", "/api/v1/instance-groups/"+itoa(childID)+"/members", map[string]interface{}{"instanceIds": []uint{inst.ID}}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("加成员失败: status=%d body=%s", w.Code, w.Body.String())
	}
	added := parseJSON(t, w)
	if int(added["added"].(float64)) != 1 {
		t.Fatalf("应新增 1 个成员，得 %v", added["added"])
	}

	// 子树实例集合（根含子树 → 含该实例）
	w = makeRequest(r, "GET", "/api/v1/instance-groups/"+itoa(rootID)+"/instances", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("查子树实例失败: status=%d body=%s", w.Code, w.Body.String())
	}
	subtree := parseJSON(t, w)
	ids, _ := subtree["instanceIds"].([]interface{})
	if len(ids) != 1 || uint(ids[0].(float64)) != inst.ID {
		t.Fatalf("子树实例集合应含 [%d]，得 %v", inst.ID, subtree["instanceIds"])
	}

	// 树视图：根子树聚合计数 = 1
	w = makeRequest(r, "GET", "/api/v1/instance-groups", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("查树失败: status=%d body=%s", w.Code, w.Body.String())
	}
	tree := parseJSONArray(t, w)
	var rootCount int
	for _, n := range tree {
		nm := n.(map[string]interface{})
		if uint(nm["id"].(float64)) == rootID {
			rootCount = int(nm["instanceCount"].(float64))
		}
	}
	if rootCount != 1 {
		t.Fatalf("根子树聚合计数应为 1，得 %d", rootCount)
	}

	// 移除成员后删子组、再删根 → 成功
	w = makeRequest(r, "DELETE", "/api/v1/instance-groups/"+itoa(childID)+"/members", map[string]interface{}{"instanceIds": []uint{inst.ID}}, token)
	if w.Code != http.StatusNoContent {
		t.Fatalf("移除成员失败: status=%d body=%s", w.Code, w.Body.String())
	}
	w = makeRequest(r, "DELETE", "/api/v1/instance-groups/"+itoa(childID), nil, token)
	if w.Code != http.StatusNoContent {
		t.Fatalf("删空子组失败: status=%d body=%s", w.Code, w.Body.String())
	}
	w = makeRequest(r, "DELETE", "/api/v1/instance-groups/"+itoa(rootID), nil, token)
	if w.Code != http.StatusNoContent {
		t.Fatalf("删空根组失败: status=%d body=%s", w.Code, w.Body.String())
	}
}
