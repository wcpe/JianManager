package router

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// setupCoreVersionRouter 建一个含 FR-086/087 + FR-193 core 版本管理路由的最小引擎。
func setupCoreVersionRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}

	root, err := dataroot.Init(filepath.Join(os.TempDir(), "jm-corever-test-"+strconv.FormatInt(time.Now().UnixNano(), 10)))
	if err != nil {
		t.Fatalf("初始化数据根失败: %v", err)
	}
	assetSvc := service.NewAssetService(db, root)
	channelSvc := service.NewClientChannelService(db)
	signer, err := service.NewManifestSigner(service.DevSignPrivateKeyPKCS8Base64, service.DefaultSignKeyID)
	if err != nil {
		t.Fatalf("构造签名器失败: %v", err)
	}
	versionSvc := service.NewClientVersionService(db, assetSvc, channelSvc, signer)
	coreVersionSvc := service.NewClientCoreVersionService(db, assetSvc)
	versionSvc.SetCoreVersions(coreVersionSvc)

	svcs := &Services{
		Auth:              service.NewAuthService(db, jwtCfg),
		Authz:             service.NewAuthzService(db),
		Audit:             service.NewAuditService(db),
		Asset:             assetSvc,
		ClientChannel:     channelSvc,
		ClientVersion:     versionSvc,
		ClientCoreVersion: coreVersionSvc,
		ClientMachine:     service.NewClientMachineService(db),
	}
	_ = cpgrpc.NewClientPool()
	return Setup(svcs, jwtCfg.Secret)
}

// uploadCoreJar 经上传端点上传一份 core jar 字节，返回制品 sha256/size。
func uploadCoreJar(t *testing.T, r *gin.Engine, token string, content []byte) (string, int64) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "updater-core.jar.zst")
	_, _ = fw.Write(content)
	_ = mw.WriteField("codec", "zstd")
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/api/v1/client-core-versions/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("上传 core 制品失败: status=%d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	sha, _ := resp["sha256"].(string)
	size, _ := resp["size"].(float64)
	if sha == "" {
		t.Fatalf("响应缺 sha256: %v", resp)
	}
	return sha, int64(size)
}

// registerCoreVersion 经登记端点登记一个 core 版本，返回分配的版本号。
func registerCoreVersion(t *testing.T, r *gin.Engine, token, sha string, size int64, note string) int {
	t.Helper()
	w := makeRequest(r, "POST", "/api/v1/client-core-versions",
		map[string]any{"artifactSha256": sha, "artifactSize": size, "codec": "zstd", "note": note}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("登记 core 版本失败: status=%d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	v, _ := resp["version"].(float64)
	return int(v)
}

// publishOneVersion 经发布端点发一版最小内容，使频道有 latest 可组装 manifest。
func publishOneVersion(t *testing.T, r *gin.Engine, token, channelID string) {
	t.Helper()
	sha, size := uploadClientFile(t, r, token, channelID, []byte("mod-bytes-"+channelID))
	w := makeRequest(r, "POST", "/api/v1/client-channels/"+channelID+"/versions", map[string]any{
		"files": []map[string]any{{
			"path": "mods/foo.jar", "sha256": "ab12", "md5": "cd34", "size": 1, "sync": "strict",
			"artifact": map[string]any{"sha256": sha, "size": size, "codec": "zstd"},
		}},
		"managedDirs": []string{"mods"},
	}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("发布版本失败: status=%d body=%s", w.Code, w.Body.String())
	}
}

// fetchManifestCoreVersion 经玩家拉取端点取 manifest，返回 agent.core.version（-1=无 core 段）。
func fetchManifestCoreVersion(t *testing.T, r *gin.Engine, channelID, key string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	req.Header.Set("X-Client-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("拉取 manifest 失败: status=%d body=%s", w.Code, w.Body.String())
	}
	m := parseJSON(t, w)
	agent, _ := m["agent"].(map[string]any)
	if agent == nil {
		return -1, nil
	}
	core, _ := agent["core"].(map[string]any)
	if core == nil {
		return -1, nil
	}
	ver, _ := core["version"].(float64)
	return int(ver), core
}

// TestCoreVersion_PinDrivesManifest_EndToEnd 端到端：上传 core → 登记 → pin → manifest agent.core 反映；
// 更新 pin → 升；回退（重发更高版本）→ 仍升、内容为旧字节。覆盖 ADR-045 决策 1/4。
func TestCoreVersion_PinDrivesManifest_EndToEnd(t *testing.T) {
	db := setupTestDB(t)
	r := setupCoreVersionRouter(t, db)
	token := getAdminToken(t, r)
	key := createChannelAndKey(t, r, token, "skyblock-s1")
	publishOneVersion(t, r, token, "skyblock-s1")

	// 无 core 注册 → manifest 无 agent.core（本次发布未带手填透传）。
	if v, _ := fetchManifestCoreVersion(t, r, "skyblock-s1", key); v != -1 {
		t.Fatalf("无 core 注册时不应有 agent.core，得 version=%d", v)
	}

	// 上传 + 登记两版 core。
	sha1, size1 := uploadCoreJar(t, r, token, []byte("good-core-bytes"))
	v1 := registerCoreVersion(t, r, token, sha1, size1, "好版本")
	sha2, size2 := uploadCoreJar(t, r, token, []byte("bad-core-bytes"))
	v2 := registerCoreVersion(t, r, token, sha2, size2, "坏版本")
	if v1 != 1 || v2 != 2 {
		t.Fatalf("core 版本号应 1,2，得 %d,%d", v1, v2)
	}

	// 频道默认 pin=0 → 用最新 v2。
	if v, _ := fetchManifestCoreVersion(t, r, "skyblock-s1", key); v != v2 {
		t.Fatalf("默认 pin 应解析为最新 v%d，得 v%d", v2, v)
	}

	// 显式 pin 回 v1。
	w := makeRequest(r, "PUT", "/api/v1/client-channels/skyblock-s1/core-pin", map[string]any{"version": v1}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("设 pin 失败: status=%d body=%s", w.Code, w.Body.String())
	}
	if v, core := fetchManifestCoreVersion(t, r, "skyblock-s1", key); v != v1 {
		t.Fatalf("pin v1 后 manifest 应 v%d，得 v%d (core=%v)", v1, v, core)
	}

	// 回退坏 core：以 v1 字节重发为更高版本 v3，pin 到 v3。
	w = makeRequest(r, "POST", "/api/v1/client-channels/skyblock-s1/core-rollback",
		map[string]any{"sourceVersion": v1}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("回退失败: status=%d body=%s", w.Code, w.Body.String())
	}
	rb := parseJSON(t, w)
	if int(rb["version"].(float64)) != 3 {
		t.Fatalf("回退应以更高版本号 3 重发，得 %v", rb["version"])
	}
	// manifest agent.core.version 升到 3，内容（sha256）= v1 的字节。
	v, core := fetchManifestCoreVersion(t, r, "skyblock-s1", key)
	if v != 3 {
		t.Fatalf("回退后 agent.core.version 应 3（只升不降），得 %d", v)
	}
	// manifest schema：agent.core.platforms[os] = { artifact: { sha256, size, codec } }（contract §2）。
	platforms, _ := core["platforms"].(map[string]any)
	win, _ := platforms["windows"].(map[string]any)
	winArt, _ := win["artifact"].(map[string]any)
	if winArt["sha256"] != sha1 {
		t.Fatalf("回退内容应为 v1 字节 sha=%s，得 %v", sha1, winArt["sha256"])
	}
	// 三平台 fan-out 齐全，且各带 artifact。
	for _, os := range []string{"windows", "macos", "linux"} {
		p, ok := platforms[os].(map[string]any)
		if !ok {
			t.Fatalf("manifest agent.core 缺 %s 平台键", os)
		}
		if _, ok := p["artifact"].(map[string]any); !ok {
			t.Fatalf("manifest agent.core.%s 缺 artifact", os)
		}
	}
}

// TestCoreVersion_RBAC_NonAdminForbidden 非平台管理员不得访问 core 版本端点。
func TestCoreVersion_RBAC_NonAdminForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupCoreVersionRouter(t, db)
	_ = getAdminToken(t, r) // 占用 setup（首个用户即管理员），再注册普通成员。
	memberToken := getMemberToken(t, r, "bob", "password123")

	w := makeRequest(r, "GET", "/api/v1/client-core-versions", nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通成员列 core 版本应 403，得 %d body=%s", w.Code, w.Body.String())
	}
	w = makeRequest(r, "POST", "/api/v1/client-core-versions",
		map[string]any{"artifactSha256": "deadbeef"}, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通成员登记 core 版本应 403，得 %d", w.Code)
	}
}

// TestCoreVersion_RegisterUnknownArtifact 登记未上传制品 → 400 CORE_ARTIFACT_NOT_FOUND。
func TestCoreVersion_RegisterUnknownArtifact(t *testing.T) {
	db := setupTestDB(t)
	r := setupCoreVersionRouter(t, db)
	token := getAdminToken(t, r)
	w := makeRequest(r, "POST", "/api/v1/client-core-versions",
		map[string]any{"artifactSha256": "00000000000000000000000000000000000000000000000000000000deadbeef"}, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未上传制品登记应 400，得 %d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "CORE_ARTIFACT_NOT_FOUND" {
		t.Fatalf("错误码应 CORE_ARTIFACT_NOT_FOUND，得 %v", resp["error"])
	}
}

// TestCoreVersion_SetPinUnknownVersion pin 到不存在版本 → 404 CORE_VERSION_NOT_FOUND。
func TestCoreVersion_SetPinUnknownVersion(t *testing.T) {
	db := setupTestDB(t)
	r := setupCoreVersionRouter(t, db)
	token := getAdminToken(t, r)
	createChannelAndKey(t, r, token, "skyblock-s1")
	w := makeRequest(r, "PUT", "/api/v1/client-channels/skyblock-s1/core-pin",
		map[string]any{"version": 999}, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("pin 不存在版本应 404，得 %d body=%s", w.Code, w.Body.String())
	}
}
