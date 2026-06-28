package router

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// uploadClientFileNone 以 multipart 上传 client-file 制品并显式 codec=none（FR-214 文本预览前提：
// 默认 uploadClientFile 用 codec=zstd 会被判 binary）。返回制品 sha256。
func uploadClientFileNone(t *testing.T, r *gin.Engine, token, channelID string, content []byte, filename string) string {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(content)
	_ = mw.WriteField("codec", "none")
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
	if sha == "" {
		t.Fatalf("响应缺 sha256: %v", resp)
	}
	return sha
}

// TestClientDist_ArtifactContent_TextPreview 管理面文本预览端点（FR-214）：
// 文本制品 → kind=text + 内容一致；含 NUL → kind=binary；缺 sha256 → 400；不存在 → 404；非管理员 → 403。
func TestClientDist_ArtifactContent_TextPreview(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "skyblock-s1"
	_ = createChannelAndKey(t, r, token, channelID)

	// 文本制品（codec=none）→ kind=text + 内容一致。
	textSha := uploadClientFileNone(t, r, token, channelID, []byte("motd=Hello\nmax-players=20\n"), "server.properties")
	w := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/files/content?sha256="+textSha, nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("取制品内容应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["kind"] != "text" {
		t.Errorf("文本制品 kind 应为 text，实际 %v", resp["kind"])
	}
	if got, _ := resp["content"].(string); !strings.Contains(got, "motd=Hello") {
		t.Errorf("内容应含 motd=Hello，实际 %q", got)
	}

	// 含 NUL 字节 → kind=binary（不回内容）。
	binSha := uploadClientFileNone(t, r, token, channelID, []byte{0x00, 0x01, 0x02, 'a'}, "blob.bin")
	wb := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/files/content?sha256="+binSha, nil, token)
	if wb.Code != http.StatusOK {
		t.Fatalf("取二进制制品内容应 200（带降级标记），实际 %d", wb.Code)
	}
	rb := parseJSON(t, wb)
	if rb["kind"] != "binary" {
		t.Errorf("含 NUL 制品 kind 应为 binary，实际 %v", rb["kind"])
	}
	if s, _ := rb["content"].(string); s != "" {
		t.Errorf("二进制降级不应回内容，实际 %q", s)
	}

	// 缺 sha256 → 400。
	w400 := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/files/content", nil, token)
	if w400.Code != http.StatusBadRequest {
		t.Errorf("缺 sha256 应 400，实际 %d", w400.Code)
	}

	// 不存在的制品 → 404。
	w404 := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/files/content?sha256="+sha256Hex2("nope"), nil, token)
	if w404.Code != http.StatusNotFound {
		t.Errorf("不存在制品应 404，实际 %d", w404.Code)
	}

	// 非平台管理员 → 403。
	memberToken := getMemberToken(t, r, "member1", "password123")
	wf := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/files/content?sha256="+textSha, nil, memberToken)
	if wf.Code != http.StatusForbidden {
		t.Errorf("非管理员取内容应 403，实际 %d", wf.Code)
	}
}

// TestClientDist_ArtifactContent_TooLarge 超过预览上限的制品 → kind=too-large（不回内容，FR-214）。
func TestClientDist_ArtifactContent_TooLarge(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	_ = createChannelAndKey(t, r, token, channelID)

	// 1 MiB + 1 字节 → 超 ArtifactTextPreviewMaxBytes。
	big := bytes.Repeat([]byte("a"), (1<<20)+1)
	sha := uploadClientFileNone(t, r, token, channelID, big, "big.txt")
	w := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/files/content?sha256="+sha, nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("取超大制品内容应 200（带降级标记），实际 %d", w.Code)
	}
	resp := parseJSON(t, w)
	if resp["kind"] != "too-large" {
		t.Errorf("超大制品 kind 应为 too-large，实际 %v", resp["kind"])
	}
	if s, _ := resp["content"].(string); s != "" {
		t.Errorf("超大降级不应回内容")
	}
}

// TestClientDist_ArtifactContent_Download 管理面下载端点（FR-214）：JWT 下载字节一致；无 JWT → 401。
func TestClientDist_ArtifactContent_Download(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	_ = createChannelAndKey(t, r, token, channelID)

	content := []byte("0123456789abcdef")
	sha := uploadClientFileNone(t, r, token, channelID, content, "data.bin")

	// JWT 下载 → 200 + 字节一致 + attachment。
	w := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/files/download?sha256="+sha, nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("下载制品应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), content) {
		t.Errorf("下载内容不一致: %q", w.Body.Bytes())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("应附 Content-Disposition: attachment，实际 %q", cd)
	}

	// 无 JWT → 401。
	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/files/download?sha256="+sha, nil)
	nw := httptest.NewRecorder()
	r.ServeHTTP(nw, req)
	if nw.Code != http.StatusUnauthorized {
		t.Errorf("无 JWT 下载应 401，实际 %d", nw.Code)
	}
}
