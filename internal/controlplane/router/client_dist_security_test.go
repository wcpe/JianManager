package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func publishSecurityArtifact(t *testing.T, r *gin.Engine, token, channelID string, content []byte) string {
	t.Helper()
	sha, size := uploadClientFile(t, r, token, channelID, content)
	body := map[string]any{
		"managedDirs": []string{"mods"},
		"files": []map[string]any{{
			"path": "mods/a.jar", "sha256": sha256Hex2("a-raw"), "md5": "m", "size": len(content),
			"sync": "strict", "platform": "",
			"artifact": map[string]any{"sha256": sha, "size": size, "codec": "zstd"},
		}},
	}
	w := makeRequest(r, "POST", "/api/v1/client-channels/"+channelID+"/versions", body, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("发布版本失败: %d %s", w.Code, w.Body.String())
	}
	return sha
}

func clientSecurityRequest(r *gin.Engine, method, path, key string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-Client-Key", key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestClientSecurityHello_ProfileAndRisk(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	key := createChannelAndKey(t, r, token, "s1")

	missing := clientSecurityRequest(r, "POST", "/api/v1/client-security/hello", key, []byte(`{"channel":"s1","playerName":"Steve"}`))
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("缺 machine/install 应 400，实际 %d", missing.Code)
	}
	unauth := clientSecurityRequest(r, "POST", "/api/v1/client-security/hello", "", []byte(`{"channel":"s1","playerName":"Steve","machineId":"m1","installId":"i1"}`))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("无 key 应 401，实际 %d", unauth.Code)
	}

	ok := clientSecurityRequest(r, "POST", "/api/v1/client-security/hello", key, []byte(`{"channel":"s1","playerName":"bad name!","machineId":"m1","installId":"i1"}`))
	if ok.Code != http.StatusAccepted {
		t.Fatalf("hello 应 202，实际 %d body=%s", ok.Code, ok.Body.String())
	}
	var profile model.ClientSecurityProfile
	if err := db.Where("channel_id = ? AND machine_id = ? AND install_id = ?", "s1", "m1", "i1").First(&profile).Error; err != nil {
		t.Fatalf("画像未落库: %v", err)
	}
	if profile.KeyID == 0 || profile.KeyPrefix == "" {
		t.Fatalf("画像应记录 key 信息，实际 %+v", profile)
	}
	var risks int64
	db.Model(&model.ClientSecurityRiskEvent{}).Where("rule_code = ?", "INVALID_PLAYER_NAME").Count(&risks)
	if risks != 1 {
		t.Fatalf("非法玩家名应写风险事件，实际 %d", risks)
	}
}

func TestClientSecurityKeyAndChannelProtection(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	key := createChannelAndKey(t, r, token, "s1")
	sha := publishSecurityArtifact(t, r, token, "s1", []byte("AAA"))

	detail := parseJSON(t, makeRequest(r, "GET", "/api/v1/client-channels/s1", nil, token))
	kid := strconv.Itoa(int(detail["keys"].([]any)[0].(map[string]any)["id"].(float64)))
	w := makeRequest(r, "POST", "/api/v1/client-dist/security/keys/"+kid+"/state", map[string]any{"state": "suspended"}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("暂停 key 失败: %d %s", w.Code, w.Body.String())
	}
	mw := clientSecurityRequest(r, "GET", "/api/v1/client-channels/s1/manifest", key, nil)
	if mw.Code != http.StatusForbidden || parseJSON(t, mw)["error"] != "CLIENT_KEY_SUSPENDED" || mw.Header().Get("Retry-After") == "" {
		t.Fatalf("暂停 key 应 403 CLIENT_KEY_SUSPENDED，实际 %d %s", mw.Code, mw.Body.String())
	}
	_ = makeRequest(r, "POST", "/api/v1/client-dist/security/keys/"+kid+"/state", map[string]any{"state": "normal"}, token)

	w = makeRequest(r, "PUT", "/api/v1/client-dist/security/channels/s1/protection", map[string]any{"mode": "protected"}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("设置频道保护失败: %d %s", w.Code, w.Body.String())
	}
	mw = clientSecurityRequest(r, "GET", "/api/v1/client-channels/s1/manifest", key, nil)
	if mw.Code != http.StatusOK {
		t.Fatalf("protected 下 manifest 应放行，实际 %d", mw.Code)
	}
	aw := clientSecurityRequest(r, "GET", "/api/v1/client-artifacts/"+sha, key, nil)
	if aw.Code != http.StatusTooManyRequests || parseJSON(t, aw)["error"] != "CHANNEL_PROTECTED" || aw.Header().Get("Retry-After") == "" {
		t.Fatalf("protected 下 artifact 应 429 CHANNEL_PROTECTED，实际 %d %s", aw.Code, aw.Body.String())
	}
}

func TestClientSecurityIPBlockAndAdminAuth(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	key := createChannelAndKey(t, r, token, "s1")
	publishSecurityArtifact(t, r, token, "s1", []byte("AAA"))

	block := makeRequest(r, "POST", "/api/v1/client-dist/security/ip-blocks", map[string]any{"ip": "192.0.2.1", "ttlSeconds": 60}, token)
	if block.Code != http.StatusCreated {
		t.Fatalf("封禁 IP 失败: %d %s", block.Code, block.Body.String())
	}
	mw := clientSecurityRequest(r, "GET", "/api/v1/client-channels/s1/manifest", key, nil)
	if mw.Code != http.StatusForbidden || parseJSON(t, mw)["error"] != "IP_TEMP_BLOCKED" || mw.Header().Get("Retry-After") == "" {
		t.Fatalf("IP 封禁应 403 IP_TEMP_BLOCKED，实际 %d %s", mw.Code, mw.Body.String())
	}
	actionID := strconv.Itoa(int(parseJSON(t, block)["id"].(float64)))
	cancel := makeRequest(r, "POST", "/api/v1/client-dist/security/ip-blocks/"+actionID+"/cancel", nil, token)
	if cancel.Code != http.StatusOK {
		t.Fatalf("解封失败: %d %s", cancel.Code, cancel.Body.String())
	}
	mw = clientSecurityRequest(r, "GET", "/api/v1/client-channels/s1/manifest", key, nil)
	if mw.Code != http.StatusOK {
		t.Fatalf("解封后 manifest 应 200，实际 %d", mw.Code)
	}
	if w := makeRequest(r, "GET", "/api/v1/client-dist/security/overview", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("管理 API 无 JWT 应 401，实际 %d", w.Code)
	}
	if w := makeRequest(r, "GET", "/api/v1/client-dist/security/profiles", nil, token); w.Code != http.StatusOK {
		t.Fatalf("管理 API 有 JWT 应 200，实际 %d", w.Code)
	}
}

func TestClientSecurityGroupsCRUD(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)

	created := makeRequest(r, "POST", "/api/v1/client-dist/security/groups", map[string]any{"name": "高风险 IP", "kind": "manual", "targetType": "ip", "enabled": true}, token)
	if created.Code != http.StatusCreated {
		t.Fatalf("创建分组失败: %d %s", created.Code, created.Body.String())
	}
	id := strconv.Itoa(int(parseJSON(t, created)["id"].(float64)))
	updated := makeRequest(r, "PUT", "/api/v1/client-dist/security/groups/"+id, map[string]any{"name": "观察 IP", "kind": "manual", "targetType": "ip", "enabled": false}, token)
	if updated.Code != http.StatusOK {
		t.Fatalf("更新分组失败: %d %s", updated.Code, updated.Body.String())
	}
	groups := parseJSONArray(t, makeRequest(r, "GET", "/api/v1/client-dist/security/groups", nil, token))
	if len(groups) != 1 || groups[0].(map[string]any)["name"] != "观察 IP" || groups[0].(map[string]any)["enabled"] != false {
		t.Fatalf("分组更新后列表不符: %+v", groups)
	}
}

func TestClientSecurityAnalysisEndpoints_ReturnAggregates(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	now := time.Now()
	if err := db.Create(&model.ClientDistEvent{ChannelID: "s1", MachineID: "m1", IP: "127.0.0.1", Kind: "artifact", Status: 416, ErrCode: "INVALID_RANGE", Bytes: 12, CreatedAt: now}).Error; err != nil {
		t.Fatalf("写入分发事件失败: %v", err)
	}
	if err := db.Create(&model.ClientSecurityProfile{ChannelID: "s1", MachineID: "m1", InstallID: "i1", PlayerName: "Steve", PlayerNameNorm: "steve", LastIP: "127.0.0.1", KeyID: 1, RiskScore: 3, FirstSeen: now, LastSeen: now}).Error; err != nil {
		t.Fatalf("写入安全画像失败: %v", err)
	}

	ips := makeRequest(r, "GET", "/api/v1/client-dist/security/ip-analysis", nil, token)
	if ips.Code != http.StatusOK {
		t.Fatalf("IP 剖析应 200，实际 %d %s", ips.Code, ips.Body.String())
	}
	ipRows := parseJSONArray(t, ips)
	if len(ipRows) != 1 || ipRows[0].(map[string]any)["ip"] != "127.0.0.1" {
		t.Fatalf("IP 剖析聚合不符: %+v", ipRows)
	}

	players := makeRequest(r, "GET", "/api/v1/client-dist/security/player-analysis", nil, token)
	if players.Code != http.StatusOK {
		t.Fatalf("玩家剖析应 200，实际 %d %s", players.Code, players.Body.String())
	}
	playerRows := parseJSONArray(t, players)
	if len(playerRows) != 1 || playerRows[0].(map[string]any)["playerName"] != "Steve" {
		t.Fatalf("玩家剖析聚合不符: %+v", playerRows)
	}
}

func TestClientSecuritySummaryAndWriteAuth(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	adminToken := getAdminToken(t, r)
	_ = createChannelAndKey(t, r, adminToken, "s1")
	memberToken := getMemberToken(t, r, "security-member", "password123")

	summary := makeRequest(r, "GET", "/api/v1/client-channels/s1/security-summary", nil, adminToken)
	if summary.Code != http.StatusOK {
		t.Fatalf("频道安全摘要应 200，实际 %d %s", summary.Code, summary.Body.String())
	}
	for _, request := range []struct {
		method string
		path   string
		body   map[string]any
	}{
		{"POST", "/api/v1/client-dist/security/ip-blocks", map[string]any{"ip": "192.0.2.9", "durationMinutes": 30}},
		{"POST", "/api/v1/client-dist/security/keys/1/state", map[string]any{"state": "observe"}},
		{"PUT", "/api/v1/client-dist/security/channels/s1/protection", map[string]any{"mode": "queue"}},
	} {
		w := makeRequest(r, request.method, request.path, request.body, memberToken)
		if w.Code != http.StatusForbidden {
			t.Fatalf("普通成员写操作应 403，%s %s 实际 %d", request.method, request.path, w.Code)
		}
	}
}

func TestClientSecurityProfileDetail(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	now := time.Now()
	profile := model.ClientSecurityProfile{ChannelID: "s1", MachineID: "machine-abcdef", InstallID: "install-abcdef", PlayerName: "Alex", FirstSeen: now, LastSeen: now}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("写入画像失败: %v", err)
	}

	w := makeRequest(r, "GET", "/api/v1/client-dist/security/profiles/"+strconv.Itoa(int(profile.ID)), nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("画像详情应 200，实际 %d %s", w.Code, w.Body.String())
	}
	if detail := parseJSON(t, w); detail["machineId"] != "machine-abcdef" {
		t.Fatalf("画像详情字段不符: %+v", detail)
	}
}

func TestClientSecurityArtifactAuthorization_SelectedCoreOnly(t *testing.T) {
	db := setupTestDB(t)
	r, versionSvc := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	keyS1 := createChannelAndKey(t, r, token, "s1")
	_ = createChannelAndKey(t, r, token, "s2")
	shaV1 := archiveTestCore(t, versionSvc, "core-jar-v1", "1")
	shaV2 := archiveTestCore(t, versionSvc, "core-jar-v2", "2")

	if w := makeRequest(r, "PUT", "/api/v1/client-channels/s1/updater-core/selected", map[string]string{"sha256": shaV1}, token); w.Code != http.StatusOK {
		t.Fatalf("选定 s1 core 失败: %d %s", w.Code, w.Body.String())
	}
	if w := makeRequest(r, "PUT", "/api/v1/client-channels/s2/updater-core/selected", map[string]string{"sha256": shaV2}, token); w.Code != http.StatusOK {
		t.Fatalf("选定 s2 core 失败: %d %s", w.Code, w.Body.String())
	}

	ok := clientSecurityRequest(r, "GET", "/api/v1/client-artifacts/"+shaV1, keyS1, nil)
	if ok.Code != http.StatusOK {
		t.Fatalf("本频道选定 core 应允许下载，实际 %d %s", ok.Code, ok.Body.String())
	}
	blocked := clientSecurityRequest(r, "GET", "/api/v1/client-artifacts/"+shaV2, keyS1, nil)
	if blocked.Code != http.StatusForbidden || parseJSON(t, blocked)["error"] != "ARTIFACT_NOT_ALLOWED" {
		t.Fatalf("非本频道选定 core 应 403 ARTIFACT_NOT_ALLOWED，实际 %d %s", blocked.Code, blocked.Body.String())
	}
}
