package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// insertQQChannelForTest 插一条启用 qq 通道（路由包端点测试用，appSecret 明文兼容解析）。
func insertQQChannelForTest(t *testing.T, db *gorm.DB, appID, secret, baseURL string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{
		"appId": appID, "appSecret": secret,
		"targetType": "group", "targetId": "openid-group", "baseUrl": baseURL,
	})
	require.NoError(t, db.Create(&model.AlertChannel{
		Name: "qq-" + appID, Type: model.ChannelTypeQQ, Enabled: true, Config: string(raw),
	}).Error)
}

// seedQQGroupsForTest 直插 N 条已发现群（绕开事件路径，聚焦端点行为）。
func seedQQGroupsForTest(t *testing.T, db *gorm.DB, appID string, n int) {
	t.Helper()
	base := time.Unix(1700000000, 0)
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		require.NoError(t, db.Create(&model.QQDiscoveredGroup{
			GroupOpenID:    fmt.Sprintf("gtest-%d", i),
			OpMemberOpenID: fmt.Sprintf("u%d", i),
			FirstSeenAt:    ts,
			LastSeenAt:     ts,
			SourceAppID:    appID,
		}).Error)
	}
}

// TestQQEndpoints_未登录401 三个端点未登录均 401。
func TestQQEndpoints_未登录401(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, http.MethodPost, "/api/v1/alerts/qq/share-link", map[string]string{}, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	w = makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/groups", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	w = makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/gateway/status", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestQQEndpoints_群列表分页与密钥安全 登录后分页正确、响应不含密钥。
func TestQQEndpoints_群列表分页与密钥安全(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := loginTestAdmin(t, r)
	seedQQGroupsForTest(t, db, "app1", 5)

	// 第 1 页 2 条。
	w := makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/groups?page=1&pageSize=2", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int64            `json:"total"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, int64(5), resp.Total)
	assert.Len(t, resp.Items, 2)

	// 第 3 页剩 1 条。
	w = makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/groups?page=3&pageSize=2", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, int64(5), resp.Total)
	assert.Len(t, resp.Items, 1)

	// 响应全文不含密钥字样。
	assert.NotContains(t, strings.ToLower(w.Body.String()), "appsecret")
	assert.NotContains(t, strings.ToLower(w.Body.String()), "secret")
	for _, it := range resp.Items {
		_, hasSecret := it["appSecret"]
		assert.False(t, hasSecret, "群条目不应含 appSecret 字段")
	}
}

// TestQQEndpoints_网关状态不含密钥 状态响应含四要素、无密钥。
func TestQQEndpoints_网关状态不含密钥(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := loginTestAdmin(t, r)

	w := makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/gateway/status", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	body := strings.ToLower(w.Body.String())
	assert.NotContains(t, body, "appsecret")
	assert.NotContains(t, body, `"secret"`)
	assert.Contains(t, w.Body.String(), "status")
}

// TestQQEndpoints_分享链接成功与不含密钥 分享端点返回 url、无密钥；失败时 502。
func TestQQEndpoints_分享链接成功与不含密钥(t *testing.T) {
	// 假开放平台：取 token + 分享链接。
	fakeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = w.Write([]byte(`{"access_token":"mock-token","expires_in":7200}`))
			return
		}
		if r.URL.Path == "/v2/generate_url_link" {
			assert.Equal(t, "QQBot mock-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"data":{"url":"https://qun.qq.com/qunpro/robot/qunshare?x=1"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer fakeAPI.Close()

	db := setupTestDB(t)
	insertQQChannelForTest(t, db, "1020001", "plain", fakeAPI.URL)
	r := setupTestRouter(db)
	token := loginTestAdmin(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/alerts/qq/share-link", map[string]string{}, token)
	require.Equal(t, http.StatusOK, w.Code, "分享链接应成功: %s", w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	url, _ := resp["url"].(string)
	assert.Contains(t, url, "qun.qq.com")
	assert.NotContains(t, strings.ToLower(w.Body.String()), "appsecret")
	assert.NotContains(t, strings.ToLower(w.Body.String()), `"secret"`)

	// 无 qq 通道时分享失败 → 502（不断言 4xx/5xx 之外，关键是不断言密钥且不 panic）。
	db2 := setupTestDB(t)
	r2 := setupTestRouter(db2)
	token2 := loginTestAdmin(t, r2)
	w2 := makeRequest(r2, http.MethodPost, "/api/v1/alerts/qq/share-link", map[string]string{}, token2)
	assert.Equal(t, http.StatusBadGateway, w2.Code)
	assert.NotContains(t, strings.ToLower(w2.Body.String()), "plain")
}

// TestQQEndpoints_无权限403 普通成员无 alert.manage 时三端点均 403。
func TestQQEndpoints_无权限403(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	memberToken := getMemberToken(t, r, "qqmember", "password123")

	w := makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/groups", nil, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	w = makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/gateway/status", nil, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	w = makeRequest(r, http.MethodPost, "/api/v1/alerts/qq/share-link", map[string]string{}, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestQQDiscoveryServiceForTest 占位：确保 model 包符号在路由测试可见（防未使用导入）。
var _ = model.QQGatewayConnected
