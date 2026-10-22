package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// 探测与真实测试（FR-514 spec §8）。
//
// 这一组用例要守住的是几个「错了会给出误导性结论」的地方：
//   - 上游拒绝（401/429/5xx）必须分类正确，否则管理员查错方向；
//   - 出站准入拒绝（私网/代理）不得混进 upstream_* 里；
//   - 凭据缺失/解密失败要在**发请求前**报出来，不能变成 401；
//   - 探测失败是 `ok:false` 的结论，不是 5xx。

// newProbeTestService 构造一个「允许回环 + 允许 http」的服务，便于连 httptest。
func newProbeTestService(t *testing.T, enc *KeyEncryptor) (*AssistantService, *httpclient.Guard) {
	t.Helper()
	db := newAssistantTestDB(t)
	s := NewAssistantService(db, enc)
	guard := httpclient.NewGuard(httpclient.GuardOptions{
		AllowInsecure: true,
		AllowPrivate:  []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")},
		Timeout:       5 * time.Second,
	})
	s.SetEgress(guard)
	if err := s.EnsureSetting(); err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
	return s, guard
}

// sseServer 构造一个返回固定 SSE 响应的假上游。
func sseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// openAIChatSSE 拼一段 OpenAI 兼容的最小流（含终止标记）。
func openAIChatSSE(text string) string {
	return fmt.Sprintf(
		"data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n"+
			"data: [DONE]\n\n", text)
}

// createProbeProvider 建一个指向假上游的供应商（none 凭据）。
func createProbeProvider(t *testing.T, s *AssistantService, baseURL, protocol string) *ProviderView {
	t.Helper()
	p := baseProviderParams()
	p.Protocol = protocol
	p.BaseURL = baseURL
	created, err := s.CreateProvider(p)
	if err != nil {
		t.Fatalf("建供应商失败: %v", err)
	}
	return created
}

// createProbeModel 建一个模型行。
func createProbeModel(t *testing.T, s *AssistantService, providerID uint, tools bool) *ModelView {
	t.Helper()
	m, err := s.CreateModel(SaveModelParams{
		ProviderID: providerID,
		ModelID:    "test-model",
		Enabled:    true,
		Streaming:  true,
		Tools:      tools,
		Version:    1,
	})
	if err != nil {
		t.Fatalf("建模型失败: %v", err)
	}
	return m
}

func TestTestModelSuccess(t *testing.T) {
	s, _ := newProbeTestService(t, testEncryptor(t))
	srv := sseServer(t, http.StatusOK, openAIChatSSE("pong"))
	p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)
	m := createProbeModel(t, s, p.ID, false)

	res, err := s.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("测试失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("应成功，实际 %+v", res)
	}
	if res.TextPreview != "pong" {
		t.Fatalf("文本预览异常: %q", res.TextPreview)
	}
	// 用量来自上游真实报告，必须原样带回。
	if res.PromptTokens == nil || *res.PromptTokens != 7 || res.OutputTokens == nil || *res.OutputTokens != 2 {
		t.Fatalf("用量未按上游报告回填: %+v", res)
	}
}

func TestTestModelClassifiesUpstreamStatus(t *testing.T) {
	// 每一档都要给出不同的分类码：管理员据此决定「换密钥 / 等一会 / 查服务端 / 查模型名」。
	cases := []struct {
		status int
		want   ai.ErrorCode
		desc   string
	}{
		{http.StatusUnauthorized, ai.CodeUpstreamAuth, "401 密钥错"},
		{http.StatusForbidden, ai.CodeUpstreamAuth, "403 无权限"},
		{http.StatusTooManyRequests, ai.CodeUpstreamRateLimited, "429 限流"},
		{http.StatusBadRequest, ai.CodeUpstreamRejected, "400 请求不合法"},
		{http.StatusNotFound, ai.CodeUpstreamRejected, "404 模型不存在"},
		{http.StatusInternalServerError, ai.CodeUpstreamError, "500 上游故障"},
		{http.StatusBadGateway, ai.CodeUpstreamError, "502 上游故障"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			s, _ := newProbeTestService(t, testEncryptor(t))
			srv := sseServer(t, tc.status, `{"error":"上游拒绝"}`)
			p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)
			m := createProbeModel(t, s, p.ID, false)

			res, err := s.TestModel(context.Background(), m.ID, false)
			if err != nil {
				t.Fatalf("上游失败应是结论而非请求错误，实际 err=%v", err)
			}
			if res.OK {
				t.Fatalf("应失败，实际 %+v", res)
			}
			if res.ClassifiedError != string(tc.want) {
				t.Fatalf("分类码应为 %s，实际 %q（message=%s）", tc.want, res.ClassifiedError, res.Message)
			}
		})
	}
}

func TestTestModelMasksSecretInUpstreamBody(t *testing.T) {
	// 上游 401 常见形态是**回显它收到的那个密钥**（"Incorrect API key provided: sk-xxx"）。
	// 这段文本会进 message、进前端提示、可能进日志与截图——必须掩掉。
	// 同时保留其余内容，否则管理员看不出「上游到底在抱怨什么」（模型名写错时正是靠它）。
	const secret = "sk-should-not-leak-1234567890"
	s, _ := newProbeTestService(t, testEncryptor(t))
	srv := sseServer(t, http.StatusUnauthorized,
		`{"error":{"message":"Incorrect API key provided: `+secret+`","type":"invalid_request_error"}} `+
			strings.Repeat("x", 2000))
	p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)
	m := createProbeModel(t, s, p.ID, false)

	res, err := s.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("不应返回请求错误: %v", err)
	}
	if res.OK {
		t.Fatalf("应失败，实际 %+v", res)
	}
	if strings.Contains(res.Message, secret) {
		t.Fatalf("上游错误体里的密钥未被掩蔽，message=%q", res.Message)
	}
	if strings.Contains(res.Message, "should-not-leak") {
		t.Fatalf("密钥片段仍有残留，message=%q", res.Message)
	}
	// 掩蔽不等于清空：上游的诊断信息要保留。
	if !strings.Contains(res.Message, "Incorrect API key") {
		t.Fatalf("上游诊断信息不应被一并抹掉，message=%q", res.Message)
	}
	// 截断仍然生效（512 字节上限 + 分类前缀）。
	if len(res.Message) > 600 {
		t.Fatalf("message 未截断，长度 %d", len(res.Message))
	}
}

func TestTestModelMissingCredentialFailsBeforeRequest(t *testing.T) {
	// env 模式指向一个不存在的变量：必须在**发请求前**报出来。
	// 若放过去，会收到 401，管理员就会去查密钥对不对——而真实原因是变量没设。
	t.Setenv("JIANMANAGER_ASSISTANT_PROBE_TEST_ABSENT", "")
	s, _ := newProbeTestService(t, testEncryptor(t))
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(openAIChatSSE("pong")))
	}))
	defer srv.Close()

	p := baseProviderParams()
	p.BaseURL = srv.URL
	p.CredentialMode = model.AssistantCredentialEnv
	p.CredentialEnv = "JIANMANAGER_ASSISTANT_PROBE_TEST_ABSENT"
	created, err := s.CreateProvider(p)
	if err != nil {
		t.Fatalf("建供应商失败: %v", err)
	}
	m := createProbeModel(t, s, created.ID, false)

	res, err := s.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("不应返回请求错误: %v", err)
	}
	if res.OK {
		t.Fatalf("应失败，实际 %+v", res)
	}
	if !strings.Contains(res.Message, "未设置") {
		t.Fatalf("应指出环境变量未设置，实际 %q", res.Message)
	}
	// 关键断言：确实没发出请求。发出去就等于把「配置缺失」伪装成「上游拒绝」。
	if hits != 0 {
		t.Fatalf("配置缺失时不应向上游发请求，实际 %d 次", hits)
	}
}

func TestTestModelDecryptFailure(t *testing.T) {
	// 密文解密失败不得静默降级为空密钥：那会得到一个 401，把「密钥换了」
	// 误报成「密钥不对」。
	s, _ := newProbeTestService(t, testEncryptor(t))
	srv := sseServer(t, http.StatusOK, openAIChatSSE("pong"))
	p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)
	m := createProbeModel(t, s, p.ID, false)

	// 直接把密文改成一段无法解密的垃圾（模拟主密钥更换/密文损坏）。
	if err := s.db.Model(&model.AssistantProvider{}).Where("id = ?", p.ID).
		Updates(map[string]any{
			"credential_mode": model.AssistantCredentialStored,
			"credential_enc":  "not-a-valid-ciphertext",
		}).Error; err != nil {
		t.Fatalf("改密文失败: %v", err)
	}

	res, err := s.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("不应返回请求错误: %v", err)
	}
	if res.OK {
		t.Fatalf("应失败，实际 %+v", res)
	}
	if !strings.Contains(res.Message, "解密失败") {
		t.Fatalf("应指出解密失败，实际 %q", res.Message)
	}
}

func TestTestModelToolsCapabilityGuard(t *testing.T) {
	// 模型未声明 tools 却要求工具验证：显式拒绝，而不是「去掉工具再测」。
	// 后者会给出 ok=true 的假绿——管理员以为工具链路验证过了，实际根本没带工具。
	s, _ := newProbeTestService(t, testEncryptor(t))
	srv := sseServer(t, http.StatusOK, openAIChatSSE("pong"))
	p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)
	m := createProbeModel(t, s, p.ID, false) // tools=false

	_, err := s.TestModel(context.Background(), m.ID, true)
	if !errors.Is(err, ErrAssistantInvalid) {
		t.Fatalf("应拒绝，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Fatalf("错误应说明能力缺失，实际 %v", err)
	}
}

func TestTestModelToolCallParsed(t *testing.T) {
	// 带工具时，能解析出完整参数即 toolCallParsed=true。
	// 注意：这里**只解析不执行**——探测不该有副作用。
	s, _ := newProbeTestService(t, testEncryptor(t))
	srv := sseServer(t, http.StatusOK,
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"assistant_probe_ping\"}}]}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"nonce\\\":\\\"abc\\\"}\"}}]}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
			"data: [DONE]\n\n")
	p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)
	m := createProbeModel(t, s, p.ID, true) // tools=true

	res, err := s.TestModel(context.Background(), m.ID, true)
	if err != nil {
		t.Fatalf("测试失败: %v", err)
	}
	if !res.OK || !res.ToolCallParsed {
		t.Fatalf("应解析出工具调用，实际 %+v", res)
	}
}

func TestTestProviderUsesFirstEnabledModel(t *testing.T) {
	s, _ := newProbeTestService(t, testEncryptor(t))
	srv := sseServer(t, http.StatusOK, openAIChatSSE("pong"))
	p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)
	createProbeModel(t, s, p.ID, false)

	res, err := s.TestProvider(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("应成功，实际 %+v", res)
	}
}

func TestTestProviderWithoutModelIsExplicitError(t *testing.T) {
	// 没有模型就无法探测：明确报错，而不是「假装通过」或「假装失败」。
	s, _ := newProbeTestService(t, testEncryptor(t))
	srv := sseServer(t, http.StatusOK, openAIChatSSE("pong"))
	p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)

	_, err := s.TestProvider(context.Background(), p.ID)
	if !errors.Is(err, ErrAssistantInvalid) {
		t.Fatalf("应返回参数不合规，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "还没有模型") {
		t.Fatalf("错误应指出缺少模型，实际 %v", err)
	}
}

func TestTestProviderNotFound(t *testing.T) {
	s, _ := newProbeTestService(t, testEncryptor(t))
	if _, err := s.TestProvider(context.Background(), 9999); !errors.Is(err, ErrAssistantNotFound) {
		t.Fatalf("应返回不存在，实际 %v", err)
	}
	if _, err := s.TestModel(context.Background(), 9999, false); !errors.Is(err, ErrAssistantNotFound) {
		t.Fatalf("应返回不存在，实际 %v", err)
	}
}

func TestTestModelEgressDeniedClassifiedSeparately(t *testing.T) {
	// 出站准入拒绝（这里是私网未放行）必须分类成 egress_denied，
	// 不能混进 upstream_unreachable——那会让管理员去查供应商网络，
	// 而真实原因是面板自己不让请求出去。
	db := newAssistantTestDB(t)
	s := NewAssistantService(db, testEncryptor(t))
	// 不放行任何私网，但允许 http，以便 ValidateBaseURL 过关。
	s.SetEgress(httpclient.NewGuard(httpclient.GuardOptions{AllowInsecure: true}))
	if err := s.EnsureSetting(); err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}

	srv := sseServer(t, http.StatusOK, openAIChatSSE("pong"))
	p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)
	m := createProbeModel(t, s, p.ID, false)

	res, err := s.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("不应返回请求错误: %v", err)
	}
	if res.OK {
		t.Fatalf("应被准入拒绝，实际 %+v", res)
	}
	if res.ClassifiedError != string(ai.CodeEgressDenied) {
		t.Fatalf("分类码应为 %s，实际 %q（message=%s）",
			ai.CodeEgressDenied, res.ClassifiedError, res.Message)
	}
}

func TestTestModelTimeoutClassified(t *testing.T) {
	// 上游迟迟不返回：必须归为 upstream_timeout，而不是笼统的 unreachable。
	s, _ := newProbeTestService(t, testEncryptor(t))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 长于探测预算（30s）显然不可接受，但测试不能真等 30s：
		// 改为让上游立刻断开且不写任何 SSE 帧，模拟「连上了但流立刻被切断」。
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	p := createProbeProvider(t, s, srv.URL, model.AssistantProtocolOpenAIChat)
	m := createProbeModel(t, s, p.ID, false)

	res, err := s.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("不应返回请求错误: %v", err)
	}
	// 空流 = 没有终止信号，必须判为截断而不是成功。
	if res.OK {
		t.Fatalf("空流不应判为成功，实际 %+v", res)
	}
	if res.ClassifiedError != string(ai.CodeUpstreamStreamTruncated) {
		t.Fatalf("分类码应为 %s，实际 %q（message=%s）",
			ai.CodeUpstreamStreamTruncated, res.ClassifiedError, res.Message)
	}
}
