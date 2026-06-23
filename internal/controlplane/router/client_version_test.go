package router

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// setupClientDistRouter 建一个仅含 FR-086/087 客户端分发路由的最小引擎，
// 发布端点挂 JWT 平台管理员组、消费端点挂公网（拉取密钥鉴权）组。返回引擎与制品库服务（供断言）。
func setupClientDistRouter(t *testing.T, db *gorm.DB) (*gin.Engine, *service.ClientVersionService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}

	root, err := dataroot.Init(filepath.Join(os.TempDir(), "jm-clientdist-test-"+strconv.FormatInt(time.Now().UnixNano(), 10)))
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

	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		Authz:         service.NewAuthzService(db),
		Audit:         service.NewAuditService(db),
		Asset:         assetSvc,
		ClientChannel: channelSvc,
		ClientVersion: versionSvc,
	}
	_ = cpgrpc.NewClientPool() // 与 setupTestRouter 一致：确保 gRPC 包初始化无副作用。
	return Setup(svcs, jwtCfg.Secret), versionSvc
}

// createChannelAndKey 经管理端点建频道 + 拉取密钥，返回 (channelId, 明文 key)。
func createChannelAndKey(t *testing.T, r *gin.Engine, token, channelID string) string {
	t.Helper()
	w := makeRequest(r, "POST", "/api/v1/client-channels",
		map[string]string{"channelId": channelID, "name": "测试频道"}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("建频道失败: status=%d body=%s", w.Code, w.Body.String())
	}
	w = makeRequest(r, "POST", "/api/v1/client-channels/"+channelID+"/keys",
		map[string]string{"name": "正式包"}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("建密钥失败: status=%d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	key, _ := resp["key"].(string)
	if key == "" {
		t.Fatalf("响应缺一次性明文 key: %v", resp)
	}
	return key
}

// uploadClientFile 以 multipart 上传客户端文件制品（发布端点，需管理员 token），返回 (sha256, size)。
func uploadClientFile(t *testing.T, r *gin.Engine, token, channelID string, content []byte) (string, int64) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "foo.jar.zst")
	_, _ = fw.Write(content)
	_ = mw.WriteField("codec", "zstd")
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/api/v1/client-channels/"+channelID+"/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("上传制品失败: status=%d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	sha, _ := resp["sha256"].(string)
	size, _ := resp["size"].(float64)
	if sha == "" {
		t.Fatalf("响应缺 sha256: %v", resp)
	}
	return sha, int64(size)
}

// TestClientDist_PublishAndManifest_EndToEnd 走完整链路：上传制品 → 发布版本 → 玩家拉取签名 manifest →
// 用内置开发公钥验签（等价客户端 Signatures.verify）；并断言 ETag/304/Cache-Control。
func TestClientDist_PublishAndManifest_EndToEnd(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "skyblock-s1"
	key := createChannelAndKey(t, r, token, channelID)

	// 上传两个文件制品（artifact 内容寻址）。
	content := []byte("compressed-mod-bytes")
	artSha, artSize := uploadClientFile(t, r, token, channelID, content)

	// 解压后原始内容 hash（信任校验字段，随便造）。
	rawSha := sha256Hex2("mods/foo.jar-raw")

	// 发布版本。
	pubBody := map[string]any{
		"managedDirs": []string{"mods", "config"},
		"files": []map[string]any{
			{
				"path": "mods/foo.jar", "sha256": rawSha, "md5": "cd34", "size": 123456,
				"sync": "strict", "platform": "",
				"artifact": map[string]any{"sha256": artSha, "size": artSize, "codec": "zstd"},
			},
		},
		"note": "首发",
	}
	w := makeRequest(r, "POST", "/api/v1/client-channels/"+channelID+"/versions", pubBody, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("发布版本失败: status=%d body=%s", w.Code, w.Body.String())
	}
	pubResp := parseJSON(t, w)
	if v, _ := pubResp["version"].(float64); v != 1 {
		t.Fatalf("首版版本号应为 1，实际 %v", pubResp["version"])
	}

	// 玩家拉取 manifest（带 X-Client-Key）。
	mreq := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	mreq.Header.Set("X-Client-Key", key)
	mreq.Header.Set("X-Machine-Id", "machine-abc")
	mw := httptest.NewRecorder()
	r.ServeHTTP(mw, mreq)
	if mw.Code != http.StatusOK {
		t.Fatalf("拉取 manifest 失败: status=%d body=%s", mw.Code, mw.Body.String())
	}
	if cc := mw.Header().Get("Cache-Control"); cc == "" {
		t.Errorf("manifest 响应应含 Cache-Control")
	}
	etag := mw.Header().Get("ETag")
	if etag != `"1:k1"` {
		t.Errorf("ETag 应为 \"1:k1\"，实际 %q", etag)
	}

	// 验签：用内置开发公钥（与客户端回填同值）校验签名 manifest（等价客户端验签通过）。
	verifyManifestSignature(t, mw.Body.Bytes())

	// If-None-Match 命中 → 304。
	creq := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	creq.Header.Set("X-Client-Key", key)
	creq.Header.Set("If-None-Match", etag)
	cw := httptest.NewRecorder()
	r.ServeHTTP(cw, creq)
	if cw.Code != http.StatusNotModified {
		t.Errorf("If-None-Match 命中应 304，实际 %d", cw.Code)
	}
}

// TestClientDist_Manifest_AuthBoundary 消费端点鉴权边界：无 key/无效 key/吊销 key 一律 401。
func TestClientDist_Manifest_AuthBoundary(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)

	// 发布一版，使 manifest 可达（隔离鉴权与「无版本」404）。
	artSha, artSize := uploadClientFile(t, r, token, channelID, []byte("x"))
	pubBody := map[string]any{
		"managedDirs": []string{"mods"},
		"files": []map[string]any{{
			"path": "mods/a.jar", "sha256": sha256Hex2("a"), "md5": "m", "size": 1,
			"sync": "strict", "platform": "",
			"artifact": map[string]any{"sha256": artSha, "size": artSize, "codec": "zstd"},
		}},
	}
	if w := makeRequest(r, "POST", "/api/v1/client-channels/"+channelID+"/versions", pubBody, token); w.Code != http.StatusCreated {
		t.Fatalf("发布版本失败: %d %s", w.Code, w.Body.String())
	}

	cases := []struct {
		name   string
		setKey func(*http.Request)
	}{
		{"无 key", func(req *http.Request) {}},
		{"无效 key", func(req *http.Request) { req.Header.Set("X-Client-Key", "jmck_invalid") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
			tc.setKey(req)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("应 401，实际 %d body=%s", w.Code, w.Body.String())
			}
		})
	}

	// 吊销 key 后立即 401。先取 keyId。
	detail := parseJSON(t, makeRequest(r, "GET", "/api/v1/client-channels/"+channelID, nil, token))
	keys, _ := detail["keys"].([]any)
	if len(keys) == 0 {
		t.Fatalf("频道详情缺 keys")
	}
	kid := int(keys[0].(map[string]any)["id"].(float64))
	if w := makeRequest(r, "DELETE", fmt.Sprintf("/api/v1/client-channels/%s/keys/%d", channelID, kid), nil, token); w.Code != http.StatusOK {
		t.Fatalf("吊销密钥失败: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	req.Header.Set("X-Client-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("吊销后应 401，实际 %d", w.Code)
	}
}

// TestClientDist_Publish_RejectsWithoutJWT 发布端点须 JWT 平台管理员：无 JWT → 401，用拉取密钥也不行。
func TestClientDist_Publish_RejectsWithoutJWT(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)

	// 无任何凭据发布版本 → 401（JWT 中间件拦截）。
	req := httptest.NewRequest("POST", "/api/v1/client-channels/"+channelID+"/versions", bytes.NewBufferString(`{"files":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("无 JWT 发布应 401，实际 %d", w.Code)
	}

	// 拿拉取密钥冒充发布（关键漏洞防线）→ 仍 401：拉取密钥不是 JWT，发布端点不认 X-Client-Key。
	req2 := httptest.NewRequest("POST", "/api/v1/client-channels/"+channelID+"/files", bytes.NewBufferString(`x`))
	req2.Header.Set("X-Client-Key", key)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("用拉取密钥发布应 401，实际 %d body=%s", w2.Code, w2.Body.String())
	}
}

// TestClientDist_Artifact_RangeDelivery 制品端点支持 Range：完整 200 与部分 206 字节一致，无效 key 401。
func TestClientDist_Artifact_RangeDelivery(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)

	content := []byte("0123456789abcdef") // 16 字节，便于 Range 断言。
	artSha, _ := uploadClientFile(t, r, token, channelID, content)

	// 无 key → 401。
	nreq := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+artSha, nil)
	nw := httptest.NewRecorder()
	r.ServeHTTP(nw, nreq)
	if nw.Code != http.StatusUnauthorized {
		t.Errorf("无 key 取制品应 401，实际 %d", nw.Code)
	}

	// 完整下载 200。
	freq := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+artSha, nil)
	freq.Header.Set("X-Client-Key", key)
	fw := httptest.NewRecorder()
	r.ServeHTTP(fw, freq)
	if fw.Code != http.StatusOK {
		t.Fatalf("完整下载应 200，实际 %d body=%s", fw.Code, fw.Body.String())
	}
	if !bytes.Equal(fw.Body.Bytes(), content) {
		t.Errorf("完整下载内容不符: %q", fw.Body.Bytes())
	}
	if ar := fw.Header().Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("应声明 Accept-Ranges: bytes，实际 %q", ar)
	}

	// Range: bytes=4-9 → 206 + 6 字节。
	rreq := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+artSha, nil)
	rreq.Header.Set("X-Client-Key", key)
	rreq.Header.Set("Range", "bytes=4-9")
	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, rreq)
	if rw.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求应 206，实际 %d", rw.Code)
	}
	if got := rw.Body.String(); got != "456789" {
		t.Errorf("Range bytes=4-9 应得 \"456789\"，实际 %q", got)
	}

	// 不存在的制品 → 404。
	xreq := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+sha256Hex2("nope"), nil)
	xreq.Header.Set("X-Client-Key", key)
	xw := httptest.NewRecorder()
	r.ServeHTTP(xw, xreq)
	if xw.Code != http.StatusNotFound {
		t.Errorf("不存在制品应 404，实际 %d", xw.Code)
	}
}

// TestClientDist_Manifest_NoVersion 频道无 latest 时拉 manifest → 404（鉴权通过但无版本）。
func TestClientDist_Manifest_NoVersion(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "empty"
	key := createChannelAndKey(t, r, token, channelID)

	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	req.Header.Set("X-Client-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("无版本应 404，实际 %d body=%s", w.Code, w.Body.String())
	}
}

// verifyManifestSignature 解析响应 manifest，用内置开发公钥对 canonical(去 sig) 验签。
// Go 与客户端 Java 同为 RFC 8032 Ed25519，Go 验签通过等价客户端 Signatures.verify 通过。
func verifyManifestSignature(t *testing.T, raw []byte) {
	t.Helper()
	var m service.SignedManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 manifest 失败: %v body=%s", err, raw)
	}
	if m.Sig == nil || m.Sig.Alg != "Ed25519" || m.Sig.KeyID != "k1" {
		t.Fatalf("manifest 缺有效签名段: %+v", m.Sig)
	}
	pubDER, err := base64.StdEncoding.DecodeString(service.DevSignPublicKeySPKIBase64)
	if err != nil {
		t.Fatalf("解码公钥失败: %v", err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatalf("解析公钥失败: %v", err)
	}
	pub := pubAny.(ed25519.PublicKey)
	sigBytes, err := base64.StdEncoding.DecodeString(m.Sig.Value)
	if err != nil {
		t.Fatalf("解码签名失败: %v", err)
	}
	if !ed25519.Verify(pub, service.SigningBytes(&m), sigBytes) {
		t.Fatalf("manifest 签名验证失败（响应 JSON 与签名 canonical 不同源？）")
	}
}

// sha256Hex2 测试辅助：返回字符串 SHA-256 十六进制小写。
func sha256Hex2(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
