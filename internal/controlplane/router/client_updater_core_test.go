package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// archiveTestCore 归档一个测试 core jar，返回其 sha256。
func archiveTestCore(t *testing.T, versionSvc *service.ClientVersionService, content, version string) string {
	t.Helper()
	a, err := versionSvc.ArchiveCoreJar(strings.NewReader(content), version)
	require.NoError(t, err)
	return a.SHA256
}

// serveNoAuth 发一个不带 JWT 的请求（用于消费端点拉取密钥鉴权测试）。
func serveNoAuth(r http.Handler, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestUpdaterCore_Endpoint_AuthBoundary coreEndpoint 鉴权边界：无 key/无效 key → 401；有效 key → 200。
func TestUpdaterCore_Endpoint_AuthBoundary(t *testing.T) {
	db := setupTestDB(t)
	r, versionSvc := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "core-s1"
	key := createChannelAndKey(t, r, token, channelID)
	shaV1 := archiveTestCore(t, versionSvc, "core-jar-v1", "1")

	// 无 key → 401。
	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/updater-core", nil)
	w := serveNoAuth(r, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 无效 key → 401。
	req2 := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/updater-core", nil)
	req2.Header.Set("X-Client-Key", "jmck_invalid")
	w2 := serveNoAuth(r, req2)
	require.Equal(t, http.StatusUnauthorized, w2.Code)

	// 有效 key → 200 + {version, sha256, downloadUrl, size}。
	req3 := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/updater-core", nil)
	req3.Host = "8.148.77.83:18370"
	req3.Header.Set("X-Client-Key", key)
	w3 := serveNoAuth(r, req3)
	require.Equal(t, http.StatusOK, w3.Code)

	resp := parseJSON(t, w3)
	require.EqualValues(t, 1, resp["version"])
	require.Equal(t, shaV1, resp["sha256"])
	dl, _ := resp["downloadUrl"].(string)
	require.Equal(t, "http://8.148.77.83:18370/api/v1/client-artifacts/"+shaV1, dl)
	require.NotZero(t, resp["size"])
}

func TestUpdaterCore_DefaultArchiveCanBeDownloaded(t *testing.T) {
	db := setupTestDB(t)
	r, versionSvc := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "core-default"
	key := createChannelAndKey(t, r, token, channelID)
	archiveTestCore(t, versionSvc, "core-jar-v1", "1")
	shaV2 := archiveTestCore(t, versionSvc, "core-jar-v2", "2")

	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/updater-core", nil)
	req.Header.Set("X-Client-Key", key)
	core := serveNoAuth(r, req)
	require.Equal(t, http.StatusOK, core.Code, core.Body.String())
	resp := parseJSON(t, core)
	require.Equal(t, shaV2, resp["sha256"])

	download := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+shaV2, nil)
	download.Header.Set("X-Client-Key", key)
	w := serveNoAuth(r, download)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "core-jar-v2", w.Body.String())
}

// TestUpdaterCore_Endpoint_NoArchive 频道有 key 但无 core 归档 → 404。
func TestUpdaterCore_Endpoint_NoArchive(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "core-empty"
	key := createChannelAndKey(t, r, token, channelID)

	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/updater-core", nil)
	req.Header.Set("X-Client-Key", key)
	w := serveNoAuth(r, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestUpdaterCore_Versions_AdminOnly 版本列表端点：JWT admin → 200；非 admin → 403；无 JWT → 401。
func TestUpdaterCore_Versions_AdminOnly(t *testing.T) {
	db := setupTestDB(t)
	r, versionSvc := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "core-ver"
	createChannelAndKey(t, r, token, channelID)
	archiveTestCore(t, versionSvc, "core-jar-v1", "1")
	archiveTestCore(t, versionSvc, "core-jar-v2", "2")

	// admin → 200 + 两条。
	w := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/updater-core/versions", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	versions := parseJSONArray(t, w)
	require.Len(t, versions, 2)

	// 无 JWT → 401。
	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/updater-core/versions", nil)
	w2 := serveNoAuth(r, req)
	require.Equal(t, http.StatusUnauthorized, w2.Code)

	// 非 admin → 403。
	memberToken := getMemberToken(t, r, "bob2", "password123")
	w3 := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/updater-core/versions", nil, memberToken)
	require.Equal(t, http.StatusForbidden, w3.Code)
}

// TestUpdaterCore_Select_AdminOnly 切换选定端点：JWT admin → 200；非 admin → 403；不存在 sha → 404。
func TestUpdaterCore_Select_AdminOnly(t *testing.T) {
	db := setupTestDB(t)
	r, versionSvc := setupClientDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "core-sel"
	key := createChannelAndKey(t, r, token, channelID)
	shaV1 := archiveTestCore(t, versionSvc, "core-jar-v1", "1")
	archiveTestCore(t, versionSvc, "core-jar-v2", "2")

	// admin 选定 v1（旧版回滚）→ 200。
	w := makeRequest(r, "PUT", "/api/v1/client-channels/"+channelID+"/updater-core/selected",
		map[string]string{"sha256": shaV1}, token)
	require.Equal(t, http.StatusOK, w.Code)

	// 选定后 coreEndpoint 应返回 v1（而非最新 v2）。
	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/updater-core", nil)
	req.Header.Set("X-Client-Key", key)
	w2 := serveNoAuth(r, req)
	require.Equal(t, http.StatusOK, w2.Code)
	resp := parseJSON(t, w2)
	require.EqualValues(t, 1, resp["version"], "切换后应返回选定版本 v1")

	// 不存在的 sha256 → 404。
	w3 := makeRequest(r, "PUT", "/api/v1/client-channels/"+channelID+"/updater-core/selected",
		map[string]string{"sha256": strings.Repeat("a", 64)}, token)
	require.Equal(t, http.StatusNotFound, w3.Code)

	// 非 admin → 403。
	memberToken := getMemberToken(t, r, "bob3", "password123")
	w4 := makeRequest(r, "PUT", "/api/v1/client-channels/"+channelID+"/updater-core/selected",
		map[string]string{"sha256": shaV1}, memberToken)
	require.Equal(t, http.StatusForbidden, w4.Code)
}
