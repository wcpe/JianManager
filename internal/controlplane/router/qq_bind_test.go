package router

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qqBindRouterFake 假绑定上游（路由层端点测试用）：真实 q.qq.com 不能在单测里访问，
// 故经 QQBindService.SetBaseURL（只放行 https 与回环）把域名指向它。
type qqBindRouterFake struct {
	server *httptest.Server

	mu         sync.Mutex
	createBody string
	pollBody   string
	keyB64     string
	authHeader string
}

func newQQBindRouterFake(t *testing.T) *qqBindRouterFake {
	t.Helper()
	f := &qqBindRouterFake{
		createBody: `{"retcode":0,"data":{"task_id":"task-route-1"}}`,
		pollBody:   `{"retcode":0,"data":{"status":1}}`,
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.authHeader = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/lite/create_bind_task":
			var req struct {
				Key string `json:"key"`
			}
			_ = json.Unmarshal(raw, &req)
			f.keyB64 = req.Key
			body := f.createBody
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		case "/lite/poll_bind_result":
			body := f.pollBody
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		default:
			f.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// setPoll 改写轮询响应。
func (f *qqBindRouterFake) setPoll(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pollBody = body
}

// setCreate 改写创建响应。
func (f *qqBindRouterFake) setCreate(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createBody = body
}

// localKey 取出创建请求里带的本地密钥（造密文用）。
func (f *qqBindRouterFake) localKey(t *testing.T) []byte {
	t.Helper()
	f.mu.Lock()
	raw := f.keyB64
	f.mu.Unlock()
	key, err := base64.StdEncoding.DecodeString(raw)
	require.NoError(t, err)
	require.Len(t, key, 32)
	return key
}

// encryptQQBindSecretForRouterTest 按官方布局造密文：base64([12 字节 IV][密文][16 字节 authTag])。
func encryptQQBindSecretForRouterTest(t *testing.T, key []byte, plain string) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	iv := make([]byte, 12)
	for i := range iv {
		iv[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(append(iv, gcm.Seal(nil, iv, []byte(plain), nil)...))
}

// TestQQBindEndpoints_未登录401 两个绑定端点未登录均 401。
func TestQQBindEndpoints_未登录401(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, http.MethodPost, "/api/v1/alerts/qq/bind-task", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	w = makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/bind-task/task-1", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestQQBindEndpoints_无权限403 普通成员无 alert.manage 时两端点均 403（不泄露任何数据）。
func TestQQBindEndpoints_无权限403(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	memberToken := getMemberToken(t, r, "qqbindmember", "password123")

	w := makeRequest(r, http.MethodPost, "/api/v1/alerts/qq/bind-task", nil, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	w = makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/bind-task/task-1", nil, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestQQBindEndpoints_创建任务返回二维码URL 创建端点返回 taskId + qrUrl，失败时 502，
// 且响应里绝不出现密钥或上游凭据。
func TestQQBindEndpoints_创建任务返回二维码URL(t *testing.T) {
	fake := newQQBindRouterFake(t)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	require.NoError(t, testQQBind.SetBaseURL(fake.server.URL))
	token := loginTestAdmin(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/alerts/qq/bind-task", nil, token)
	require.Equal(t, http.StatusOK, w.Code, "创建绑定任务应成功: %s", w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "task-route-1", resp["taskId"])

	qrURL, _ := resp["qrUrl"].(string)
	assert.Contains(t, qrURL, "/qqbot/openclaw/connect.html")
	assert.Contains(t, qrURL, "task_id=task-route-1")
	assert.Contains(t, qrURL, "source=JianManager")
	// 绑定端点不带 Authorization 头（官方实测口径）。
	fake.mu.Lock()
	assert.Empty(t, fake.authHeader, "绑定端点不应带鉴权头")
	fake.mu.Unlock()

	lower := strings.ToLower(w.Body.String())
	assert.NotContains(t, lower, "appsecret")
	assert.NotContains(t, lower, `"secret"`)

	// 上游失败 → 502，且把上游 msg 带出来（不带任何密钥）。
	fake.setCreate(`{"retcode":100017,"msg":"接口调用超过频率限制"}`)
	w = makeRequest(r, http.MethodPost, "/api/v1/alerts/qq/bind-task", nil, token)
	require.Equal(t, http.StatusBadGateway, w.Code)
	assert.Contains(t, w.Body.String(), "频率限制")
	assert.NotContains(t, strings.ToLower(w.Body.String()), "appsecret")
}

// TestQQBindEndpoints_完成态只回引用名不含密钥 完成态响应只含 appId / userOpenid / ${ENV} 引用名，
// 明文 appSecret 必须留在服务端（落盘 etc/qq-<appId>.key）。
func TestQQBindEndpoints_完成态只回引用名不含密钥(t *testing.T) {
	const secret = "router-raw"
	fake := newQQBindRouterFake(t)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	require.NoError(t, testQQBind.SetBaseURL(fake.server.URL))
	token := loginTestAdmin(t, r)

	// 1) 创建任务（拿到本地密钥）。
	w := makeRequest(r, http.MethodPost, "/api/v1/alerts/qq/bind-task", nil, token)
	require.Equal(t, http.StatusOK, w.Code, "创建绑定任务应成功: %s", w.Body.String())

	// 2) 上游给出完成态：密文用本地密钥加密（等价真机扫码后的返回）。
	enc := encryptQQBindSecretForRouterTest(t, fake.localKey(t), secret)
	fake.setPoll(`{"retcode":0,"data":{"status":2,"bot_appid":"1020001","bot_encrypt_secret":"` + enc + `","user_openid":"user-openid-7"}}`)

	w = makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/bind-task/task-route-1", nil, token)
	require.Equal(t, http.StatusOK, w.Code, "轮询应成功: %s", w.Body.String())
	body := w.Body.String()

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "completed", resp["status"])
	assert.Equal(t, "1020001", resp["appId"])
	assert.Equal(t, "user-openid-7", resp["userOpenid"])
	assert.Equal(t, "${QQ-1020001}", resp["secretEnv"], "只回前端该填的 ${ENV} 引用名")

	// 密钥安全：明文、字段名与密文都不得出现在响应里。
	assert.NotContains(t, body, secret, "响应绝不能带 appSecret 明文")
	assert.NotContains(t, body, "appSecret")
	assert.NotContains(t, body, enc, "响应绝不能带密文")
	assert.Len(t, resp, 4, "完成态只应有 status/appId/userOpenid/secretEnv 四个字段")
}

// TestQQBindEndpoints_本地密钥过期返回404 上游已完成但本地密钥已被清理时，返回 404 提示重扫。
func TestQQBindEndpoints_本地密钥过期返回404(t *testing.T) {
	fake := newQQBindRouterFake(t)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	require.NoError(t, testQQBind.SetBaseURL(fake.server.URL))
	token := loginTestAdmin(t, r)

	enc := encryptQQBindSecretForRouterTest(t, make([]byte, 32), "whatever")
	fake.setPoll(`{"retcode":0,"data":{"status":2,"bot_appid":"1020001","bot_encrypt_secret":"` + enc + `"}}`)

	// 该 taskId 从未在本进程创建过（等价重启后拿旧链接轮询）。
	w := makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/bind-task/never-created", nil, token)
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "BIND_TASK_EXPIRED")
	assert.NotContains(t, strings.ToLower(w.Body.String()), "appsecret")
}

// TestQQBindEndpoints_上游失败返回502 轮询上游失败时返回 502，错误信息可定位且不含密钥。
func TestQQBindEndpoints_上游失败返回502(t *testing.T) {
	fake := newQQBindRouterFake(t)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	require.NoError(t, testQQBind.SetBaseURL(fake.server.URL))
	token := loginTestAdmin(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/alerts/qq/bind-task", nil, token)
	require.Equal(t, http.StatusOK, w.Code)

	fake.setPoll(`{"retcode":11244,"msg":"task not found"}`)
	w = makeRequest(r, http.MethodGet, "/api/v1/alerts/qq/bind-task/task-route-1", nil, token)
	require.Equal(t, http.StatusBadGateway, w.Code)
	assert.Contains(t, w.Body.String(), "BIND_TASK_FAILED")
	assert.NotContains(t, strings.ToLower(w.Body.String()), "appsecret")
}
