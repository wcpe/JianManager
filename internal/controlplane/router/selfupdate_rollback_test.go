package router

import (
	"net/http"
	"testing"
)

// TestSelfUpdate_RollbackCP_NoBackup 无备份时回滚 CP 返回 409 UPDATE_NO_BACKUP。
func TestSelfUpdate_RollbackCP_NoBackup(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/control-plane/rollback", map[string]any{}, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("无备份回滚应 409，实得 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "UPDATE_NO_BACKUP" {
		t.Fatalf("错误码应为 UPDATE_NO_BACKUP: %v", resp)
	}
}

// TestSelfUpdate_RollbackCP_MemberForbidden 普通成员无权回滚（仅平台管理员）。
func TestSelfUpdate_RollbackCP_MemberForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r)
	member := getMemberToken(t, r, "bob", "password123")

	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/control-plane/rollback", map[string]any{}, member)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通成员回滚应 403，实得 %d: %s", w.Code, w.Body.String())
	}
}

// TestSelfUpdate_RollbackNode_Offline 离线节点回滚返回 503 NODE_OFFLINE。
func TestSelfUpdate_RollbackNode_Offline(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/nodes/"+itoa(node.ID)+"/rollback", map[string]any{}, token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("离线节点回滚应 503，实得 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "NODE_OFFLINE" {
		t.Fatalf("错误码应为 NODE_OFFLINE: %v", resp)
	}
}
