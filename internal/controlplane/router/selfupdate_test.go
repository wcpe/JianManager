package router

import (
	"net/http"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/model"
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

// TestSelfUpdate_UpgradeNode_FailureAudited 升级失败同样落审计（failed=true + 脱敏错误）。
// E2E 验收发现：此前 respondUpgradeError 只回 HTTP 不写审计，升级失败历史无法追溯（FR-321 语义）。
func TestSelfUpdate_UpgradeNode_FailureAudited(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/nodes/"+itoa(node.ID)+"/upgrade", map[string]any{}, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("未配源应 409，实得 %d: %s", w.Code, w.Body.String())
	}

	// 审计表应有一条 self_update.node 失败记录，错误内容为脱敏后的原因。
	var logs []model.AuditLog
	if err := db.Where("action = ?", "self_update.node").Find(&logs).Error; err != nil {
		t.Fatalf("查询审计日志失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("升级失败应落 1 条审计，实得 %d", len(logs))
	}
	if !logs[0].Failed {
		t.Fatalf("审计记录应标 failed=true: %+v", logs[0])
	}
	if logs[0].Error != "未配置更新源" {
		t.Fatalf("审计错误内容应为「未配置更新源」: %q", logs[0].Error)
	}
	if logs[0].TargetID != itoa(node.ID) {
		t.Fatalf("审计 targetID 应为节点 ID %q: %q", itoa(node.ID), logs[0].TargetID)
	}
}

// TestSanitizeWorkerAssetToken 错误摘要中的下载凭据（token=…）统一脱敏（ADR-059）。
func TestSanitizeWorkerAssetToken(t *testing.T) {
	in := `节点升级失败: Get "http://cp/worker-assets/1.0.0/windows/amd64/worker?token=eyJhbGciOiJ9": dial tcp: connection refused`
	got := sanitizeWorkerAssetToken(in)
	if strings.Contains(got, "eyJhbGciOiJ9") {
		t.Fatalf("token 明文应被脱敏: %q", got)
	}
	if !strings.Contains(got, "token=REDACTED") {
		t.Fatalf("应替换为 token=REDACTED: %q", got)
	}
	// 无 token 的消息原样透传
	plain := "校验不符: 期望 abc 实得 def"
	if sanitizeWorkerAssetToken(plain) != plain {
		t.Fatalf("普通消息不应被改写: %q", sanitizeWorkerAssetToken(plain))
	}
}
