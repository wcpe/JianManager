package router

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// md5Hex2 测试辅助：返回字节内容的 MD5 十六进制小写。
func md5Hex2(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// publishOneFileVersion 上传一个制品并发布含单文件（path）的版本，返回新版本号。
func publishOneFileVersion(t *testing.T, r *gin.Engine, token, channelID, path string, content []byte) int {
	t.Helper()
	artSha, artSize := uploadClientFile(t, r, token, channelID, content)
	body := map[string]any{
		"managedDirs": []string{"mods"},
		"files": []map[string]any{{
			"path": path, "sha256": sha256Hex2(path + "-raw"), "md5": "cd34", "size": 10,
			"sync": "strict", "platform": "",
			"artifact": map[string]any{"sha256": artSha, "size": artSize, "codec": "zstd"},
		}},
		"note": "发布 " + path,
	}
	w := makeRequest(r, "POST", "/api/v1/client-channels/"+channelID+"/versions", body, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("发布版本失败: %d %s", w.Code, w.Body.String())
	}
	return int(parseJSON(t, w)["version"].(float64))
}

// TestClientDist_VersionHistory_ListAndDetail 历史列表（DESC + isLatest 仅最高）与版本详情（文件清单）。
func TestClientDist_VersionHistory_ListAndDetail(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	createChannelAndKey(t, r, token, channelID)

	if v := publishOneFileVersion(t, r, token, channelID, "mods/a.jar", []byte("AAA")); v != 1 {
		t.Fatalf("首版应为 1，实际 %d", v)
	}
	if v := publishOneFileVersion(t, r, token, channelID, "mods/b.jar", []byte("BBB")); v != 2 {
		t.Fatalf("次版应为 2，实际 %d", v)
	}

	// 历史列表：版本号 DESC、isLatest 仅最高版本。
	lw := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/versions", nil, token)
	if lw.Code != http.StatusOK {
		t.Fatalf("列版本失败: %d %s", lw.Code, lw.Body.String())
	}
	arr := parseJSONArray(t, lw)
	if len(arr) != 2 {
		t.Fatalf("应 2 个版本，实际 %d", len(arr))
	}
	first := arr[0].(map[string]any)
	second := arr[1].(map[string]any)
	if int(first["version"].(float64)) != 2 || first["isLatest"] != true {
		t.Errorf("首项应 v2 且 isLatest=true，实际 %v", first)
	}
	if int(second["version"].(float64)) != 1 || second["isLatest"] != false {
		t.Errorf("次项应 v1 且 isLatest=false，实际 %v", second)
	}
	if int(first["fileCount"].(float64)) != 1 {
		t.Errorf("fileCount 应为 1，实际 %v", first["fileCount"])
	}

	// 详情：返回文件清单与 managedDirs。
	dw := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/versions/1", nil, token)
	if dw.Code != http.StatusOK {
		t.Fatalf("版本详情失败: %d %s", dw.Code, dw.Body.String())
	}
	detail := parseJSON(t, dw)
	files, _ := detail["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "mods/a.jar" {
		t.Errorf("v1 详情文件应为 mods/a.jar，实际 %v", detail["files"])
	}
	if detail["isLatest"] != false {
		t.Errorf("v1 不应是 latest，实际 %v", detail["isLatest"])
	}

	// 不存在版本 → 404。
	if nw := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/versions/99", nil, token); nw.Code != http.StatusNotFound {
		t.Errorf("不存在版本应 404，实际 %d", nw.Code)
	}
}

// TestClientDist_Rollback_RepublishesOldContentAsHigherVersion 回滚 = 以更高版本号重发旧内容为 latest，
// 客户端按单调版本正常前进、不被防降级拒绝（ADR-022 §3 / contract §3）。
func TestClientDist_Rollback_RepublishesOldContentAsHigherVersion(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)

	publishOneFileVersion(t, r, token, channelID, "mods/a.jar", []byte("AAA")) // v1
	publishOneFileVersion(t, r, token, channelID, "mods/b.jar", []byte("BBB")) // v2（latest）

	// 回滚到 v1 → 应以新版本号 v3 重发 v1 内容。
	rw := makeRequest(r, "POST", "/api/v1/client-channels/"+channelID+"/rollback",
		map[string]any{"sourceVersion": 1}, token)
	if rw.Code != http.StatusCreated {
		t.Fatalf("回滚失败: %d %s", rw.Code, rw.Body.String())
	}
	rb := parseJSON(t, rw)
	if int(rb["version"].(float64)) != 3 {
		t.Fatalf("回滚应产生 v3（单调），实际 %v", rb["version"])
	}
	if int(rb["sourceVersion"].(float64)) != 1 {
		t.Errorf("响应应带 sourceVersion=1，实际 %v", rb["sourceVersion"])
	}

	// latest manifest：version=3（> 2，防降级通过）、内容为 v1 的 mods/a.jar。
	mreq := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	mreq.Header.Set("X-Client-Key", key)
	mw := httptest.NewRecorder()
	r.ServeHTTP(mw, mreq)
	if mw.Code != http.StatusOK {
		t.Fatalf("拉 manifest 失败: %d %s", mw.Code, mw.Body.String())
	}
	var m service.SignedManifest
	if err := json.Unmarshal(mw.Body.Bytes(), &m); err != nil {
		t.Fatalf("解析 manifest 失败: %v", err)
	}
	if m.Version != 3 {
		t.Errorf("回滚后 latest version 应为 3，实际 %d", m.Version)
	}
	if len(m.Files) != 1 || m.Files[0].Path != "mods/a.jar" {
		t.Errorf("回滚后 manifest 内容应为 v1（mods/a.jar），实际 %+v", m.Files)
	}
	// 重发亦签名，验签仍通过。
	verifyManifestSignature(t, mw.Body.Bytes())
}

// TestClientDist_Rollback_NotFound 回滚到不存在的源版本 → 404。
func TestClientDist_Rollback_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	createChannelAndKey(t, r, token, channelID)
	publishOneFileVersion(t, r, token, channelID, "mods/a.jar", []byte("AAA"))

	rw := makeRequest(r, "POST", "/api/v1/client-channels/"+channelID+"/rollback",
		map[string]any{"sourceVersion": 99}, token)
	if rw.Code != http.StatusNotFound {
		t.Errorf("回滚不存在版本应 404，实际 %d body=%s", rw.Code, rw.Body.String())
	}
}

// TestClientDist_VersionAdminEndpoints_RejectPullKey 历史/详情/回滚是 admin JWT 端点：
// 拉取密钥（半公开）不得越权访问（关键安全分组，ADR-022 §4 / contract §4）。
func TestClientDist_VersionAdminEndpoints_RejectPullKey(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)
	publishOneFileVersion(t, r, token, channelID, "mods/a.jar", []byte("AAA"))

	cases := []struct {
		method, path string
		body         []byte
	}{
		{"GET", "/api/v1/client-channels/" + channelID + "/versions", nil},
		{"GET", "/api/v1/client-channels/" + channelID + "/versions/1", nil},
		{"POST", "/api/v1/client-channels/" + channelID + "/rollback", []byte(`{"sourceVersion":1}`)},
	}
	for _, c := range cases {
		// 无 JWT → 401。
		w := makeRequest(r, c.method, c.path, nil, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 无 JWT 应 401，实际 %d", c.method, c.path, w.Code)
		}
		// 仅带拉取密钥冒充 → 仍 401（管理端点不认 X-Client-Key）。
		var rdr *bytes.Buffer
		if c.body != nil {
			rdr = bytes.NewBuffer(c.body)
		} else {
			rdr = bytes.NewBuffer(nil)
		}
		req := httptest.NewRequest(c.method, c.path, rdr)
		req.Header.Set("X-Client-Key", key)
		req.Header.Set("Content-Type", "application/json")
		kw := httptest.NewRecorder()
		r.ServeHTTP(kw, req)
		if kw.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 用拉取密钥应 401，实际 %d body=%s", c.method, c.path, kw.Code, kw.Body.String())
		}
	}
}

// TestClientDist_PublishFile_ReturnsMD5 上传制品响应回传 md5（供发布向导填 file.md5，codec=none 时 = 原始内容 md5）。
func TestClientDist_PublishFile_ReturnsMD5(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	createChannelAndKey(t, r, token, channelID)

	content := []byte("raw-mod-content")
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "a.jar")
	_, _ = fw.Write(content)
	_ = mw.WriteField("codec", "none")
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/client-channels/"+channelID+"/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("上传失败: %d %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["md5"] != md5Hex2(content) {
		t.Errorf("响应 md5 应为 %s，实际 %v", md5Hex2(content), resp["md5"])
	}
	if resp["sha256"] != sha256Hex2(string(content)) {
		t.Errorf("响应 sha256 应为内容 sha256，实际 %v", resp["sha256"])
	}
	if resp["codec"] != "none" {
		t.Errorf("响应 codec 应为 none，实际 %v", resp["codec"])
	}
}

// TestClientDist_MachineRegistration 拉 manifest 携带 X-Machine-Id 时机器码登记 upsert（FR-092）；
// 不携带则不登记。机器码不可信、仅统计/辅助限流。
func TestClientDist_MachineRegistration(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)
	publishOneFileVersion(t, r, token, channelID, "mods/a.jar", []byte("AAA"))

	pull := func(machineID string) {
		req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
		req.Header.Set("X-Client-Key", key)
		if machineID != "" {
			req.Header.Set("X-Machine-Id", machineID)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("拉 manifest 失败: %d %s", w.Code, w.Body.String())
		}
	}

	// 带机器码两次 → 登记 upsert hit_count=2。
	pull("machine-xyz")
	pull("machine-xyz")
	var m model.ClientMachine
	if err := db.Where("channel_id = ? AND machine_id = ?", channelID, "machine-xyz").First(&m).Error; err != nil {
		t.Fatalf("机器码应已登记: %v", err)
	}
	if m.HitCount != 2 {
		t.Errorf("两次拉取应 hit_count=2，实际 %d", m.HitCount)
	}

	// 不带机器码 → 不新增登记行。
	pull("")
	var total int64
	db.Model(&model.ClientMachine{}).Where("channel_id = ?", channelID).Count(&total)
	if total != 1 {
		t.Errorf("无 X-Machine-Id 不应新增登记，频道登记行应为 1，实际 %d", total)
	}
}

// TestClientDist_PullTrackingAndEventQuery 拉取/下载追踪落库（FR-093）+ 管理面检索端点（含鉴权）。
func TestClientDist_PullTrackingAndEventQuery(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)
	publishOneFileVersion(t, r, token, channelID, "mods/a.jar", []byte("AAA"))

	// 玩家拉 manifest（带机器码）→ 追踪 defer 同步记录。
	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	req.Header.Set("X-Client-Key", key)
	req.Header.Set("X-Machine-Id", "mach-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("拉 manifest 失败: %d %s", w.Code, w.Body.String())
	}

	var ev model.ClientDistEvent
	if err := db.Where("channel_id = ? AND kind = ?", channelID, "manifest").First(&ev).Error; err != nil {
		t.Fatalf("manifest 拉取事件应落库: %v", err)
	}
	if ev.Version != 1 || ev.MachineID != "mach-1" || ev.Status != http.StatusOK || ev.Bytes <= 0 {
		t.Errorf("事件字段异常: version=%d machine=%s status=%d bytes=%d", ev.Version, ev.MachineID, ev.Status, ev.Bytes)
	}

	// 检索端点（admin）。
	lw := makeRequest(r, "GET", "/api/v1/client-dist/events?kind=manifest", nil, token)
	if lw.Code != http.StatusOK {
		t.Fatalf("检索失败: %d %s", lw.Code, lw.Body.String())
	}
	if arr := parseJSONArray(t, lw); len(arr) < 1 {
		t.Errorf("检索应返回至少 1 条事件")
	}

	// 检索端点限管理员：无 JWT → 401。
	if nw := makeRequest(r, "GET", "/api/v1/client-dist/events", nil, ""); nw.Code != http.StatusUnauthorized {
		t.Errorf("检索端点无 JWT 应 401，实际 %d", nw.Code)
	}
}
