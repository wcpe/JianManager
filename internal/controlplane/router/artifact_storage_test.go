package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// artifactStorageBody s3 渠道创建请求体样板。
func artifactStorageBody(name, endpoint string) map[string]any {
	return map[string]any{
		"name": name, "type": "s3", "endpoint": endpoint, "bucket": "jm-artifacts",
		"prefix": "jm", "accessKey": "test-ak", "secretKey": "test-sk", "useSsl": false,
	}
}

// TestArtifactStorageAPI_ListSeedsBuiltin 列表含内置「本机存储」（Builtin 最前、活跃兜底），
// 响应永不含凭证明文/密文。
func TestArtifactStorageAPI_ListSeedsBuiltin(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/artifact-storages", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	items := parseJSONArray(t, w)
	require.Len(t, items, 1)
	builtin := items[0].(map[string]any)
	assert.Equal(t, true, builtin["builtin"])
	assert.Equal(t, true, builtin["active"])
	assert.Equal(t, "local", builtin["type"])

	created := makeRequest(r, "POST", "/api/v1/artifact-storages", artifactStorageBody("rustfs", "rustfs.lan:9000"), token)
	require.Equal(t, http.StatusCreated, created.Code)
	body := created.Body.String()
	assert.NotContains(t, body, "test-ak", "响应不含凭证明文")
	assert.NotContains(t, body, "test-sk")
	assert.Contains(t, body, `"hasAccessKey":true`)
	assert.Contains(t, body, `"presignTtlSeconds":600`, "TTL 未填取默认")

	list := makeRequest(r, "GET", "/api/v1/artifact-storages", nil, token)
	items = parseJSONArray(t, list)
	require.Len(t, items, 2)
	assert.Equal(t, true, items[0].(map[string]any)["builtin"], "内置渠道恒排最前")
}

// TestArtifactStorageAPI_CreateValidation 重名/非法类型/缺 endpoint/TTL 越界 → 422；缺必填 → 400。
func TestArtifactStorageAPI_CreateValidation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	first := makeRequest(r, "POST", "/api/v1/artifact-storages", artifactStorageBody("dup", "a:9000"), token)
	require.Equal(t, http.StatusCreated, first.Code)
	second := makeRequest(r, "POST", "/api/v1/artifact-storages", artifactStorageBody("dup", "b:9000"), token)
	require.Equal(t, http.StatusUnprocessableEntity, second.Code)
	assert.Equal(t, "BUSINESS_ERROR", parseJSON(t, second)["error"])

	local := makeRequest(r, "POST", "/api/v1/artifact-storages", map[string]any{"name": "x", "type": "local"}, token)
	require.Equal(t, http.StatusUnprocessableEntity, local.Code, "面板不可创建 local 渠道")

	noEndpoint := makeRequest(r, "POST", "/api/v1/artifact-storages", map[string]any{
		"name": "y", "type": "s3", "bucket": "b",
	}, token)
	require.Equal(t, http.StatusUnprocessableEntity, noEndpoint.Code)

	badTTL := artifactStorageBody("z", "c:9000")
	badTTL["presignTtlSeconds"] = 10
	ttlResp := makeRequest(r, "POST", "/api/v1/artifact-storages", badTTL, token)
	require.Equal(t, http.StatusUnprocessableEntity, ttlResp.Code)

	missing := makeRequest(r, "POST", "/api/v1/artifact-storages", map[string]any{"type": "s3"}, token)
	require.Equal(t, http.StatusBadRequest, missing.Code, "缺 name 必填 → 400")
}

// TestArtifactStorageAPI_UpdateDeleteGuards 内置不可编辑/删除；活跃不可删；被制品引用不可删；
// 编辑凭证留空保留；404 语义。
func TestArtifactStorageAPI_UpdateDeleteGuards(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	list := makeRequest(r, "GET", "/api/v1/artifact-storages", nil, token)
	builtinID := uint(parseJSONArray(t, list)[0].(map[string]any)["id"].(float64))

	// 内置行不可编辑/删除。
	wu := makeRequest(r, "PUT", "/api/v1/artifact-storages/"+itoa(builtinID), artifactStorageBody("重命名", "a:9000"), token)
	require.Equal(t, http.StatusUnprocessableEntity, wu.Code)
	wd := makeRequest(r, "DELETE", "/api/v1/artifact-storages/"+itoa(builtinID), nil, token)
	require.Equal(t, http.StatusUnprocessableEntity, wd.Code)

	created := makeRequest(r, "POST", "/api/v1/artifact-storages", artifactStorageBody("rustfs", "a:9000"), token)
	require.Equal(t, http.StatusCreated, created.Code)
	id := uint(parseJSON(t, created)["id"].(float64))

	// 设活跃后禁删。
	wa := makeRequest(r, "POST", "/api/v1/artifact-storages/"+itoa(id)+"/activate", nil, token)
	require.Equal(t, http.StatusOK, wa.Code)
	assert.Equal(t, true, parseJSON(t, wa)["active"])
	wd = makeRequest(r, "DELETE", "/api/v1/artifact-storages/"+itoa(id), nil, token)
	require.Equal(t, http.StatusUnprocessableEntity, wd.Code)
	assert.Contains(t, parseJSON(t, wd)["message"], "活跃")

	// 切回内置；被制品引用仍禁删（附引用数）。
	_ = makeRequest(r, "POST", "/api/v1/artifact-storages/"+itoa(builtinID)+"/activate", nil, token)
	require.NoError(t, db.Create(&model.Asset{
		Type: model.AssetTypeClientFile, SHA256: strings.Repeat("b", 64), Size: 1,
		StorageBackend: model.AssetBackendS3, StorageChannelID: id,
	}).Error)
	wd = makeRequest(r, "DELETE", "/api/v1/artifact-storages/"+itoa(id), nil, token)
	require.Equal(t, http.StatusUnprocessableEntity, wd.Code)
	assert.Contains(t, parseJSON(t, wd)["message"], "制品引用")

	// 清引用后可删；编辑凭证留空=保留（hasSecretKey 仍 true）。
	require.NoError(t, db.Where("storage_channel_id = ?", id).Delete(&model.Asset{}).Error)
	edit := artifactStorageBody("rustfs", "b:9100")
	edit["accessKey"] = ""
	edit["secretKey"] = ""
	we := makeRequest(r, "PUT", "/api/v1/artifact-storages/"+itoa(id), edit, token)
	require.Equal(t, http.StatusOK, we.Code)
	eb := parseJSON(t, we)
	assert.Equal(t, "b:9100", eb["endpoint"])
	assert.Equal(t, true, eb["hasSecretKey"], "凭证留空保留原值")

	wd = makeRequest(r, "DELETE", "/api/v1/artifact-storages/"+itoa(id), nil, token)
	require.Equal(t, http.StatusOK, wd.Code)

	w404 := makeRequest(r, "PUT", "/api/v1/artifact-storages/99999", artifactStorageBody("ghost", "a:1"), token)
	require.Equal(t, http.StatusNotFound, w404.Code)
}

// TestArtifactStorageAPI_TestEndpoints 候选测试（fake S3 真连）与已存测试（持久化 lastTest*）。
func TestArtifactStorageAPI_TestEndpoints(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	fakeS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut, http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer fakeS3.Close()

	body := artifactStorageBody("probe", fakeS3.URL)
	testOnly := makeRequest(r, "POST", "/api/v1/artifact-storages/test", body, token)
	require.Equal(t, http.StatusOK, testOnly.Code)
	tb := parseJSON(t, testOnly)
	assert.Equal(t, true, tb["ok"], "候选真连探测应通过: %v", tb)

	created := makeRequest(r, "POST", "/api/v1/artifact-storages", body, token)
	require.Equal(t, http.StatusCreated, created.Code)
	id := uint(parseJSON(t, created)["id"].(float64))

	saved := makeRequest(r, "POST", "/api/v1/artifact-storages/"+itoa(id)+"/test", nil, token)
	require.Equal(t, http.StatusOK, saved.Code)
	assert.Equal(t, true, parseJSON(t, saved)["ok"])

	list := makeRequest(r, "GET", "/api/v1/artifact-storages", nil, token)
	for _, item := range parseJSONArray(t, list) {
		row := item.(map[string]any)
		if uint(row["id"].(float64)) == id {
			assert.NotEmpty(t, row["lastTestAt"], "已存测试应持久化 lastTest*")
			assert.Equal(t, true, row["lastTestOk"])
		}
	}
}

// TestArtifactStorageAPI_RequiresAdmin 渠道端点仅平台管理员可用（403），未认证 401。
func TestArtifactStorageAPI_RequiresAdmin(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-as", "password123")

	w := makeRequest(r, "GET", "/api/v1/artifact-storages", nil, memberToken)
	require.Equal(t, http.StatusForbidden, w.Code)

	req := httptest.NewRequest("GET", "/api/v1/artifact-storages", nil)
	nw := httptest.NewRecorder()
	r.ServeHTTP(nw, req)
	require.Equal(t, http.StatusUnauthorized, nw.Code)
}
