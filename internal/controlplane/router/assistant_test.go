package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// AI 助手路由权限测试（FR-514，见 ADR-105）。
//
// 这组用例守的是「谁能改 AI 配置」这条线。它特别值得单独测，因为项目里已有一个
// 名字叫 requirePlatformAdmin 的 helper，但它实际要求的是 node.manage 权限节点——
// 若 AI 管理面误用它，任何持有 node.manage 的自定义角色都能改供应商地址与凭据引用。
// 这里用「持有 node.manage 但不是平台管理员」的用户专门证伪该误用。

// assistantTestFixture 装好路由与两类用户，返回可用的 router 与 token。
type assistantTestFixture struct {
	router      *gin.Engine
	adminToken  string
	manageToken string // 持有 node.manage 但非平台管理员
	plainToken  string // 无任何权限的普通成员
	// db 供审计类断言直接查表（审计是「写进去的东西」，只能从存储侧验证）。
	db *gorm.DB
}

func newAssistantTestFixture(t *testing.T) assistantTestFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	authz := service.NewAuthzService(db)
	users := service.NewUserService(db)

	_, err := users.Create("ai_admin", "password123", model.RolePlatformAdmin, model.UserStatusActive)
	require.NoError(t, err)

	manageUser, err := users.Create("ai_manage", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	manageRole, err := authz.Permissions().CreateRole("ai-node-manage", "", []string{"node.manage"})
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(manageUser.ID, manageRole.ID))

	plain, err := users.Create("ai_plain", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	plainRole, err := authz.Permissions().CreateRole("ai-none", "", nil)
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(plain.ID, plainRole.ID))

	return assistantTestFixture{
		router:      r,
		db:          db,
		adminToken:  getTokenForUser(t, r, "ai_admin", "password123"),
		manageToken: getTokenForUser(t, r, "ai_manage", "password123"),
		plainToken:  getTokenForUser(t, r, "ai_plain", "password123"),
	}
}

// setupStrictAssistantRouter 构造带严格出站准入的路由，供「明文 http 被拒」这类用例使用。
//
// 为什么不复用 setupTestRouter：它装配的 AI 服务用宽松出站（放行 http 与回环，
// 方便其它用例打 httptest）。若用它测「http 被拒」，断言会被配置本身绕过而失去意义。
func setupStrictAssistantRouter(t *testing.T, db *gorm.DB) (*gin.Engine, *service.AssistantService) {
	t.Helper()
	svc := service.NewAssistantService(db, testAssistantEncryptor(t))
	svc.SetEgress(httpclient.NewGuard(httpclient.GuardOptions{}))
	require.NoError(t, svc.EnsureSetting())
	return setupTestRouterWithAssistantOverride(db, svc), svc
}

// testAssistantEncryptor 取测试用加密器（dev 密钥，不碰生产主密钥）。
func testAssistantEncryptor(t *testing.T) *service.KeyEncryptor {
	t.Helper()
	enc, _, err := service.ResolveKeyEncryptor("", true, "")
	require.NoError(t, err)
	require.NotNil(t, enc)
	return enc
}

// getTokenForUser 登录并取 accessToken。
func getTokenForUser(t *testing.T, r *gin.Engine, username, password string) string {
	t.Helper()
	w := makeRequest(r, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": password}, "")
	require.Equalf(t, http.StatusOK, w.Code, "登录失败: %s", w.Body.String())
	var resp struct {
		AccessToken string `json:"accessToken"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.AccessToken, "登录响应缺少 accessToken")
	return resp.AccessToken
}

// doAssistantReq 发一个带 Bearer 的请求，返回状态码与响应体。
//
// 复用既有 makeRequest：它已统一处理 JSON 编码与 Authorization 头，
// 另造一个只会让「两种请求写法」在同一测试包里并存。
func doAssistantReq(t *testing.T, r *gin.Engine, method, path, token, body string) (int, string) {
	t.Helper()
	var payload interface{}
	if body != "" {
		var decoded map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(body), &decoded), "测试请求体不是合法 JSON: %s", body)
		payload = decoded
	}
	w := makeRequest(r, method, path, payload, token)
	return w.Code, w.Body.String()
}

// 可用模型摘要对所有登录用户开放（包括完全无业务权限的成员）。
func TestAssistantModelsReadableByAllUsers(t *testing.T) {
	f := newAssistantTestFixture(t)

	for name, token := range map[string]string{
		"平台管理员": f.adminToken,
		"节点管理者": f.manageToken,
		"普通成员":  f.plainToken,
	} {
		code, body := doAssistantReq(t, f.router, http.MethodGet, "/api/v1/assistant/models", token, "")
		require.Equalf(t, http.StatusOK, code, "%s 应可读可用模型，响应=%s", name, body)
		require.Contains(t, body, "defaultModelId")
	}
}

// 未登录不得访问。
func TestAssistantModelsRequiresLogin(t *testing.T) {
	f := newAssistantTestFixture(t)
	code, _ := doAssistantReq(t, f.router, http.MethodGet, "/api/v1/assistant/models", "", "")
	require.Equal(t, http.StatusUnauthorized, code)
}

// 关键：持有 node.manage 的自定义角色**不是**平台管理员，不能碰 AI 配置。
// 这条用例专门防止将来有人把 AI 管理面挂到既有 requirePlatformAdmin helper 上。
func TestAssistantAdminRejectsNodeManageRole(t *testing.T) {
	f := newAssistantTestFixture(t)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/assistant/admin/providers", ""},
		{http.MethodGet, "/api/v1/assistant/admin/models", ""},
		{http.MethodPost, "/api/v1/assistant/admin/providers", `{"name":"x","protocol":"openai-chat","baseUrl":"https://api.example.com","credentialMode":"none"}`},
		{http.MethodPut, "/api/v1/assistant/admin/settings/default-model", `{"modelId":null}`},
	} {
		code, body := doAssistantReq(t, f.router, tc.method, tc.path, f.manageToken, tc.body)
		require.Equalf(t, http.StatusForbidden, code,
			"node.manage 用户不应能 %s %s，响应=%s", tc.method, tc.path, body)
	}
}

// 普通成员同样被拒。
func TestAssistantAdminRejectsPlainMember(t *testing.T) {
	f := newAssistantTestFixture(t)
	code, _ := doAssistantReq(t, f.router, http.MethodGet, "/api/v1/assistant/admin/providers", f.plainToken, "")
	require.Equal(t, http.StatusForbidden, code)
}

// 平台管理员可完成「建供应商 → 建模型 → 设默认 → 读可用模型」闭环。
func TestAssistantAdminLifecycle(t *testing.T) {
	f := newAssistantTestFixture(t)

	code, body := doAssistantReq(t, f.router, http.MethodPost, "/api/v1/assistant/admin/providers", f.adminToken,
		`{"name":"主供应商","protocol":"openai-chat","baseUrl":"https://api.example.com/v1","credentialMode":"none","enabled":true}`)
	require.Equal(t, http.StatusCreated, code, "创建供应商失败: %s", body)
	require.NotContains(t, body, "credentialEnc", "响应不得包含密文字段")

	var provider struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &provider))
	require.NotZero(t, provider.ID)

	code, body = doAssistantReq(t, f.router, http.MethodPost, "/api/v1/assistant/admin/models", f.adminToken,
		`{"providerId":`+itoa(provider.ID)+`,"modelId":"model-a","displayName":"模型 A","streaming":true,"tools":true,"enabled":true}`)
	require.Equal(t, http.StatusCreated, code, "创建模型失败: %s", body)

	var created struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &created))

	code, body = doAssistantReq(t, f.router, http.MethodPut, "/api/v1/assistant/admin/settings/default-model", f.adminToken,
		`{"modelId":`+itoa(created.ID)+`}`)
	require.Equal(t, http.StatusOK, code, "设置默认模型失败: %s", body)

	code, body = doAssistantReq(t, f.router, http.MethodGet, "/api/v1/assistant/models", f.plainToken, "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "model-a")
	require.Contains(t, body, "defaultModelId")
	require.NotContains(t, body, "baseUrl", "普通用户模型摘要不得暴露供应商端点")
}

// 凭据只在写入方向流动：响应里既无明文也无密文。
func TestAssistantProviderSecretNeverEchoed(t *testing.T) {
	f := newAssistantTestFixture(t)

	code, body := doAssistantReq(t, f.router, http.MethodPost, "/api/v1/assistant/admin/providers", f.adminToken,
		`{"name":"带密钥的","protocol":"openai-chat","baseUrl":"https://api.example.com","credentialMode":"stored","credentialSecret":"sk-very-secret-value","enabled":true}`)
	require.Equal(t, http.StatusCreated, code, "创建失败: %s", body)
	require.NotContains(t, body, "sk-very-secret-value", "响应不得回显明文凭据")
	require.NotContains(t, body, "credentialEnc", "响应不得回显密文")
	require.Contains(t, body, "hasCredential", "应只回「是否已配置」标志")

	// 列表同样不回显。
	code, body = doAssistantReq(t, f.router, http.MethodGet, "/api/v1/assistant/admin/providers", f.adminToken, "")
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, body, "sk-very-secret-value")
	require.NotContains(t, body, "credentialEnc")
}

// 参数不合规回 422（不是 400 也不是 500），便于前端区分「输入错」与「服务错」。
//
// 注意这里**不能**复用 setupTestRouter 的宽松出站（它放行 http 与回环以便探测用例
// 打 httptest）；本条要验证的正是「明文 http 被拒」，宽松配置会让该断言失去意义。
func TestAssistantInvalidConfigReturns422(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupStrictAssistantRouter(t, db)

	users := service.NewUserService(db)
	_, err := users.Create("ai_strict_admin", "password123", model.RolePlatformAdmin, model.UserStatusActive)
	require.NoError(t, err)
	adminToken := getTokenForUser(t, r, "ai_strict_admin", "password123")

	for _, body := range []string{
		`{"name":"x","protocol":"unknown","baseUrl":"https://api.example.com","credentialMode":"none"}`,
		`{"name":"x","protocol":"openai-chat","baseUrl":"http://api.example.com","credentialMode":"none"}`,
		`{"name":"x","protocol":"openai-chat","baseUrl":"https://api.example.com","credentialMode":"env","credentialEnv":"BAD_NAME"}`,
		`{"name":"x","protocol":"openai-chat","baseUrl":"https://api.example.com","credentialMode":"stored"}`,
	} {
		code, resp := doAssistantReq(t, r, http.MethodPost, "/api/v1/assistant/admin/providers", adminToken, body)
		require.Equalf(t, http.StatusUnprocessableEntity, code, "请求体 %s 应为 422，实际 %d 响应=%s", body, code, resp)
	}
}

// 不存在的对象回 404（不是 500）。
func TestAssistantNotFoundReturns404(t *testing.T) {
	f := newAssistantTestFixture(t)
	code, _ := doAssistantReq(t, f.router, http.MethodGet, "/api/v1/assistant/admin/providers/9999", f.adminToken, "")
	require.Equal(t, http.StatusNotFound, code)
}

// 探测端点：上游失败仍是 200 + ok:false，而不是把上游状态码透传给面板。
//
// 为什么这条必须单独测：把「密钥错了」映射成 401，前端会误以为**自己的登录态**
// 失效并弹出重新登录；把「上游 5xx」映射成 502，则会让管理员以为是面板的服务坏了。
// 探测的语义是「给你一个结论」，所以永远是 200，结论体里带分类码。
func TestAssistantTestModelUpstreamFailureIsStill200(t *testing.T) {
	f := newAssistantTestFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-leak-1234567890"}}`))
	}))
	defer srv.Close()

	code, body := doAssistantReq(t, f.router, http.MethodPost, "/api/v1/assistant/admin/providers", f.adminToken,
		`{"name":"探测用","protocol":"openai-chat","baseUrl":"`+srv.URL+`","credentialMode":"none","enabled":true}`)
	require.Equal(t, http.StatusCreated, code, "建供应商失败: %s", body)
	var p struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &p))

	code, body = doAssistantReq(t, f.router, http.MethodPost,
		"/api/v1/assistant/admin/models", f.adminToken,
		fmt.Sprintf(`{"providerId":%d,"modelId":"m1","enabled":true,"streaming":true,"tools":false}`, p.ID))
	require.Equal(t, http.StatusCreated, code, "建模型失败: %s", body)
	var m struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &m))

	code, body = doAssistantReq(t, f.router, http.MethodPost,
		fmt.Sprintf("/api/v1/assistant/admin/models/%d/test", m.ID), f.adminToken, `{}`)
	require.Equal(t, http.StatusOK, code, "上游失败应是结论而非请求错误: %s", body)
	require.Contains(t, body, `"ok":false`, "应回 ok:false: %s", body)
	require.Contains(t, body, `"upstream_auth"`, "应带分类码: %s", body)
	require.NotContains(t, body, "leak-1234567890", "上游回显的密钥必须被掩蔽: %s", body)
}

// 探测端点：非管理员一律 403（与其它管理端子域同一判据）。
//
// 其中 manageToken 持有 node.manage 仍须被拒——这是对「误用 requirePlatformAdmin
// 那个只校验 node.manage 的 helper」的直接证伪。
func TestAssistantProbeRequiresPlatformAdmin(t *testing.T) {
	f := newAssistantTestFixture(t)
	for _, token := range []string{f.manageToken, f.plainToken} {
		code, _ := doAssistantReq(t, f.router, http.MethodPost,
			"/api/v1/assistant/admin/providers/1/test", token, "")
		require.Equal(t, http.StatusForbidden, code, "非平台管理员不得探测供应商")
		code, _ = doAssistantReq(t, f.router, http.MethodPost,
			"/api/v1/assistant/admin/models/1/test", token, "")
		require.Equal(t, http.StatusForbidden, code, "非平台管理员不得测试模型")
	}
	// 未登录 401。
	code, _ := doAssistantReq(t, f.router, http.MethodPost, "/api/v1/assistant/admin/models/1/test", "", "")
	require.Equal(t, http.StatusUnauthorized, code)
}

// 探测端点：对象不存在回 404。
func TestAssistantProbeNotFound(t *testing.T) {
	f := newAssistantTestFixture(t)
	code, _ := doAssistantReq(t, f.router, http.MethodPost,
		"/api/v1/assistant/admin/providers/9999/test", f.adminToken, "")
	require.Equal(t, http.StatusNotFound, code)
	code, _ = doAssistantReq(t, f.router, http.MethodPost,
		"/api/v1/assistant/admin/models/9999/test", f.adminToken, "")
	require.Equal(t, http.StatusNotFound, code)
}

// 审计留痕（FR-514 交付缺口补齐）。
//
// 这组用例的重点不是「有没有写审计」，而是**写进去的 detail 里绝不能有密钥**。
// 审计表会被检索、导出、长期保留——一旦凭据进去，就等于把只在服务端内存里流转的
// 密钥复制到一个人人可查的地方，而且删不掉（历史行不会因为换密钥而消失）。
func TestAssistantAuditRecordsWithoutSecrets(t *testing.T) {
	f := newAssistantTestFixture(t)
	db := f.db

	const secret = "sk-audit-must-not-leak-9876543210"
	code, body := doAssistantReq(t, f.router, http.MethodPost, "/api/v1/assistant/admin/providers", f.adminToken,
		`{"name":"审计用","protocol":"openai-chat","baseUrl":"https://api.example.com","credentialMode":"stored","credentialSecret":"`+secret+`","enabled":true}`)
	require.Equal(t, http.StatusCreated, code, "创建失败: %s", body)

	var p struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &p))

	// 更新：改显示名但不动凭据，detail 里 credentialChanged 应为 false。
	code, body = doAssistantReq(t, f.router, http.MethodPut,
		fmt.Sprintf("/api/v1/assistant/admin/providers/%d", p.ID), f.adminToken,
		fmt.Sprintf(`{"name":"审计用改名","protocol":"openai-chat","baseUrl":"https://api.example.com","credentialMode":"stored","version":%s}`,
			jsonField(t, body, "version")))
	require.Equal(t, http.StatusOK, code, "更新失败: %s", body)

	// 模型：创建 + 设默认 + 删除。
	code, body = doAssistantReq(t, f.router, http.MethodPost, "/api/v1/assistant/admin/models", f.adminToken,
		fmt.Sprintf(`{"providerId":%d,"modelId":"m-audit","enabled":true,"streaming":true}`, p.ID))
	require.Equal(t, http.StatusCreated, code, "建模型失败: %s", body)
	var m struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &m))

	code, body = doAssistantReq(t, f.router, http.MethodPut,
		"/api/v1/assistant/admin/settings/default-model", f.adminToken,
		fmt.Sprintf(`{"modelId":%d,"version":0}`, m.ID))
	require.Equal(t, http.StatusOK, code, "设默认失败: %s", body)

	code, _ = doAssistantReq(t, f.router, http.MethodDelete,
		fmt.Sprintf("/api/v1/assistant/admin/models/%d", m.ID), f.adminToken, "")
	require.Equal(t, http.StatusOK, code)

	// 断言：审计行存在，且 detail 全局不含密钥/密文字样。
	var rows []model.AuditLog
	require.NoError(t, db.Where("action LIKE ?", "assistant.%").Order("id asc").Find(&rows).Error)
	require.NotEmpty(t, rows, "应有 assistant.* 审计行")

	actions := map[string]bool{}
	for _, r := range rows {
		actions[r.Action] = true
		require.NotContains(t, r.Detail, secret, "审计 detail 不得含明文密钥（action=%s）", r.Action)
		require.NotContains(t, r.Detail, "credentialEnc", "审计 detail 不得含密文字段（action=%s）", r.Action)
		require.NotContains(t, r.Detail, "credentialSecret", "审计 detail 不得含密钥字段名（action=%s）", r.Action)
	}
	for _, want := range []string{
		"assistant.provider.create", "assistant.provider.update",
		"assistant.model.create", "assistant.settings.default", "assistant.model.delete",
	} {
		require.True(t, actions[want], "缺少审计事件 %s（实际 %v）", want, actions)
	}

	// 「凭据是否被动过」必须可见：否则审计无法回答「这次改动会不会影响密钥」。
	var updateRow model.AuditLog
	require.NoError(t, db.Where("action = ?", "assistant.provider.update").First(&updateRow).Error)
	require.Contains(t, updateRow.Detail, `"credentialChanged":false`,
		"未改凭据时应记 false，实际 %s", updateRow.Detail)
}

// jsonField 从响应体里取一个标量字段的原始文本（用于回传 version）。
func jsonField(t *testing.T, body, key string) string {
	t.Helper()
	var m map[string]any
	require.NoErrorf(t, json.Unmarshal([]byte(body), &m), "响应体不是对象: %s", body)
	raw, err := json.Marshal(m[key])
	require.NoError(t, err)
	return string(raw)
}
