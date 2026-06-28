package router

import (
	"net/http"
	"testing"
)

// TestSelfUpdate_Check_AdminUnconfigured 未配源时管理员检查更新返回 200 + configured=false。
func TestSelfUpdate_Check_AdminUnconfigured(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodGet, "/api/v1/self-update/check", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实得 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if configured, _ := resp["configured"].(bool); configured {
		t.Fatalf("未配源 configured 应为 false: %v", resp)
	}
	cp, ok := resp["controlPlane"].(map[string]interface{})
	if !ok || cp["currentVersion"] == "" {
		t.Fatalf("应含 controlPlane.currentVersion: %v", resp)
	}
}

// TestSelfUpdate_Check_MemberForbidden 普通成员无权检查更新（仅平台管理员）。
func TestSelfUpdate_Check_MemberForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r) // 先建管理员，使后续注册的是普通成员
	member := getMemberToken(t, r, "alice", "password123")

	w := makeRequest(r, http.MethodGet, "/api/v1/self-update/check", nil, member)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通成员应被拒 403，实得 %d: %s", w.Code, w.Body.String())
	}
}

// TestSelfUpdate_UpgradeCP_Unconfigured 未配源时升级 CP 返回 409 UPDATE_NOT_CONFIGURED。
func TestSelfUpdate_UpgradeCP_Unconfigured(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/control-plane/upgrade", map[string]any{}, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("未配源应 409，实得 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "UPDATE_NOT_CONFIGURED" {
		t.Fatalf("错误码应为 UPDATE_NOT_CONFIGURED: %v", resp)
	}
}

// TestSelfUpdate_UpgradeNode_Offline 未配源时升级离线/未连接节点返回 409（未配源先于节点检查）。
func TestSelfUpdate_UpgradeNode_Unconfigured(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/nodes/"+itoa(node.ID)+"/upgrade", map[string]any{}, token)
	// 未配源：resolveArtifact 先 FetchFeed 返回未配源 → 409。
	if w.Code != http.StatusConflict {
		t.Fatalf("未配源应 409，实得 %d: %s", w.Code, w.Body.String())
	}
}

// TestSelfUpdate_Rollout_Idle 从未发起 rollout 时查询返回 idle。
func TestSelfUpdate_Rollout_Idle(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodGet, "/api/v1/self-update/rollout", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实得 %d", w.Code)
	}
	resp := parseJSON(t, w)
	if resp["state"] != "idle" {
		t.Fatalf("应为 idle: %v", resp)
	}
}

// TestSelfUpdate_UpgradeAll_Unconfigured 未配源时全网升级返回 409。
func TestSelfUpdate_UpgradeAll_Unconfigured(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/nodes/upgrade-all", map[string]any{}, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("未配源应 409，实得 %d: %s", w.Code, w.Body.String())
	}
}

// TestSelfUpdate_Check_EmptyCacheNotCached 无缓存时 GET /check 返回 200 + cached=false（FR-186）。
func TestSelfUpdate_Check_EmptyCacheNotCached(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodGet, "/api/v1/self-update/check", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实得 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if cached, _ := resp["cached"].(bool); cached {
		t.Fatalf("无缓存 cached 应为 false: %v", resp)
	}
}

// TestSelfUpdate_RefreshCheck_MemberForbidden 普通成员无权 refresh（仅平台管理员，FR-186）。
func TestSelfUpdate_RefreshCheck_MemberForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r)
	member := getMemberToken(t, r, "alice", "password123")

	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/check/refresh", map[string]any{}, member)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通成员应被拒 403，实得 %d: %s", w.Code, w.Body.String())
	}
}

// TestSelfUpdate_RefreshCheck_Unconfigured 未配源时 refresh 返回 200 + configured=false（FR-186）。
// 与 GET /check 一致：未配源是「可渲染的正常态」（页面显示未配置提示），不作错误处理；
// 真正的网络/限流失败才透出 502/429（见服务层 TestRefreshCheck_FailureKeepsCache）。
func TestSelfUpdate_RefreshCheck_Unconfigured(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/check/refresh", map[string]any{}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("未配源 refresh 应 200，实得 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if configured, _ := resp["configured"].(bool); configured {
		t.Fatalf("未配源 configured 应为 false: %v", resp)
	}
	if cached, _ := resp["cached"].(bool); !cached {
		t.Fatalf("refresh 成功应写缓存并标 cached=true: %v", resp)
	}
}
