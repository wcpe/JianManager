package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// uploadAsset 用 multipart 向 POST /assets 上传一份内容。
func uploadAsset(t *testing.T, r http.Handler, token, assetType, filename string, content []byte, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("type", assetType)
	for k, v := range extra {
		_ = mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAssets_UploadListGet(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	content := []byte("paper-jar-bytes")
	w := uploadAsset(t, r, token, "core", "paper.jar", content, map[string]string{"name": "paper-1.20.4", "version": "435"})
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body=%s", w.Code, w.Body.String())
	}
	created := parseJSON(t, w)
	sum := sha256.Sum256(content)
	if created["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 mismatch: %v", created["sha256"])
	}
	if created["type"] != "core" {
		t.Fatalf("type = %v", created["type"])
	}
	id := uint(created["id"].(float64))

	// List 按 type 过滤。
	w = makeRequest(r, http.MethodGet, "/api/v1/assets?type=core", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	listResp := parseJSON(t, w)
	if listResp["total"].(float64) != 1 {
		t.Fatalf("expected total 1, got %v", listResp["total"])
	}

	// 过滤到空类型。
	w = makeRequest(r, http.MethodGet, "/api/v1/assets?type=plugin", nil, token)
	if parseJSON(t, w)["total"].(float64) != 0 {
		t.Fatalf("expected 0 plugins")
	}

	// Get by id。
	w = makeRequest(r, http.MethodGet, "/api/v1/assets/"+itoa(id), nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d", w.Code)
	}
}

func TestAssets_DedupReturnsSame(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	content := []byte("dedupe-content")
	w1 := uploadAsset(t, r, token, "core", "a.jar", content, nil)
	w2 := uploadAsset(t, r, token, "core", "b.jar", content, nil)
	id1 := uint(parseJSON(t, w1)["id"].(float64))
	id2 := uint(parseJSON(t, w2)["id"].(float64))
	if id1 != id2 {
		t.Fatalf("dedup expected same id, got %d and %d", id1, id2)
	}
}

func TestAssets_ChecksumMismatchRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := uploadAsset(t, r, token, "blob", "x.bin", []byte("data"), map[string]string{
		"expectedSha256": "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on checksum mismatch, got %d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "CHECKSUM_MISMATCH" {
		t.Fatalf("error code = %v", resp["error"])
	}
}

func TestAssets_DeleteRefProtection(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := uploadAsset(t, r, token, "core", "core.jar", []byte("ref-protected"), nil)
	id := uint(parseJSON(t, w)["id"].(float64))

	// 标记被引用。
	if err := db.Model(&model.Asset{}).Where("id = ?", id).Update("ref_count", 2).Error; err != nil {
		t.Fatalf("set ref_count: %v", err)
	}
	w = makeRequest(r, http.MethodDelete, "/api/v1/assets/"+itoa(id), nil, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for referenced asset, got %d body=%s", w.Code, w.Body.String())
	}
	if parseJSON(t, w)["error"] != "ASSET_IN_USE" {
		t.Fatalf("error code = %v", parseJSON(t, w)["error"])
	}

	// 解除引用后可删。
	if err := db.Model(&model.Asset{}).Where("id = ?", id).Update("ref_count", 0).Error; err != nil {
		t.Fatalf("clear ref_count: %v", err)
	}
	w = makeRequest(r, http.MethodDelete, "/api/v1/assets/"+itoa(id), nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting unreferenced asset, got %d", w.Code)
	}
}

func TestAssets_PlatformAdminOnly(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r) // 先建管理员，使后续注册者为普通成员
	memberToken := getMemberToken(t, r, "member1", "password123")

	w := makeRequest(r, http.MethodGet, "/api/v1/assets", nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member should be forbidden, got %d", w.Code)
	}

	// 上传也应被拒。
	w = uploadAsset(t, r, memberToken, "core", "x.jar", []byte("x"), nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member upload should be forbidden, got %d", w.Code)
	}
}
