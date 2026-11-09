package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 模型自动发现（FR-538）。
//
// 为何单独补：该功能（DiscoverModels / discoverModelsURL / applyDiscoverAuth /
// parseDiscoveredModels）此前**零测试覆盖**——PRD 里 FR-538 标「部分交付·需契约补测」
// 指的就是这块。功能实现已有，缺的是钉住契约的回归。
//
// 本文件钉住四件事（都是「看着能用但会错」的形态）：
//  1. 两种响应形状都要认（{data:[{id}]} 与裸数组），解析不出**报错而非空**
//  2. 非 200 时**不得返回空列表**（那会让管理员以为供应商真没模型）
//  3. Anthropic 用 x-api-key + 版本头，OpenAI 系用 Bearer
//  4. 已配置标记 / 去重 / 超长跳过 / 200 条截断都要如实反映在结果里

func TestDiscoverModelsURL(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"https://api.openai.com/v1", "https://api.openai.com/v1/models"},
		// 末尾斜杠要被吃掉，不能拼出 //models
		{"https://gw/v1/", "https://gw/v1/models"},
		// BaseURL 不带版本前缀时也直接拼——这是既有口径，改动会影响存量配置
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080/models"},
	} {
		got := discoverModelsURL(model.AssistantProvider{BaseURL: tc.base})
		require.Equal(t, tc.want, got, "base=%q", tc.base)
	}
}

func TestApplyDiscoverAuth_PerProtocol(t *testing.T) {
	// Anthropic：x-api-key + anthropic-version（缺版本头上游会 400）
	req := httptest.NewRequest(http.MethodGet, "http://x/models", nil)
	applyDiscoverAuth(req, model.AssistantProtocolAnthropic, "sk-ant")
	require.Equal(t, "sk-ant", req.Header.Get("x-api-key"))
	require.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
	require.Empty(t, req.Header.Get("Authorization"), "Anthropic 不该再带 Bearer")

	// OpenAI 两档（chat / responses）：都是 Bearer
	for _, proto := range []string{model.AssistantProtocolOpenAIChat, model.AssistantProtocolOpenAIResponses} {
		r := httptest.NewRequest(http.MethodGet, "http://x/models", nil)
		applyDiscoverAuth(r, proto, "sk-oa")
		require.Equal(t, "Bearer sk-oa", r.Header.Get("Authorization"), "proto=%s", proto)
		require.Empty(t, r.Header.Get("x-api-key"))
	}

	// 空密钥不写任何鉴权头（否则会带上 "Bearer " 这种半成品）
	r := httptest.NewRequest(http.MethodGet, "http://x/models", nil)
	applyDiscoverAuth(r, model.AssistantProtocolOpenAIChat, "")
	require.Empty(t, r.Header.Get("Authorization"))
}

func TestParseDiscoveredModels_BothShapes(t *testing.T) {
	// OpenAI 标准形态
	ids, err := parseDiscoveredModels([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-4o", "gpt-4o-mini"}, ids)

	// 少数网关直接给数组
	ids, err = parseDiscoveredModels([]byte(`[{"id":"m1"},{"id":"m2"}]`))
	require.NoError(t, err)
	require.Equal(t, []string{"m1", "m2"}, ids)

	// 解析不出必须报错，**不能返回空切片**：
	// 空结果与「协议不兼容」在调用方看都是「没有模型」，但处置完全不同。
	for _, body := range []string{
		`{"object":"list"}`,
		`[]`,
		`not json at all`,
		`{"data":[]}`,
	} {
		_, err := parseDiscoveredModels([]byte(body))
		require.Error(t, err, "body=%s 应当报错而不是返回空", body)
		require.ErrorIs(t, err, ErrAssistantInvalid)
	}
}

// newDiscoverTestService 复用 probe 测试的装配：那里已配好
// 「允许回环 + 允许 http」的 egress Guard，否则 DiscoverModels 会在
// egressClientFor 里对 nil Guard 解引用（本文件初版就是这么 panic 的）。
func newDiscoverTestService(t *testing.T) (*AssistantService, *gorm.DB) {
	t.Helper()
	s, _ := newProbeTestService(t, nil)
	return s, s.db
}

func TestDiscoverModels_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/models", r.URL.Path)
		require.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"data":[{"id":"m-a"},{"id":"m-b"},{"id":"m-a"}]}`))
	}))
	defer srv.Close()

	svc, db := newDiscoverTestService(t)
	p := model.AssistantProvider{
		Name: "t", BaseURL: srv.URL + "/v1", Protocol: model.AssistantProtocolOpenAIChat,
		CredentialMode: model.AssistantCredentialEnv, CredentialEnv: "JM_TEST_KEY", Enabled: true,
	}
	require.NoError(t, db.Create(&p).Error)
	// 已配置一个，用来验 alreadyConfigured 标记
	require.NoError(t, db.Create(&model.AssistantModel{
		ProviderID: p.ID, ModelID: "m-b", DisplayName: "m-b", Enabled: true,
	}).Error)
	t.Setenv("JM_TEST_KEY", "sk-test")

	res, err := svc.DiscoverModels(context.Background(), p.ID)
	require.NoError(t, err)
	// 去重后两条（m-a 出现两次）
	require.Len(t, res.Discovered, 2)
	require.Equal(t, "m-a", res.Discovered[0].ModelID)
	require.False(t, res.Discovered[0].AlreadyConfigured)
	require.True(t, res.Discovered[1].AlreadyConfigured, "m-b 已配置，前端应默认不勾选")
	// Total 是上游原始条数（含重复），用来说明「为什么这里比上游少」
	require.Equal(t, 3, res.Total)
	require.False(t, res.Truncated)
	require.Nil(t, res.Skipped)
}

func TestDiscoverModels_Non200MustNotReturnEmptyList(t *testing.T) {
	// 关键契约：上游 401/404 时返回错误，不能让调用方拿到「空列表」——
	// 那会把「鉴权失败 / 列表端点不支持」误读成「这个供应商没有模型」。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key sk-leak-123"}}`))
	}))
	defer srv.Close()

	svc, db := newDiscoverTestService(t)
	p := model.AssistantProvider{
		Name: "t", BaseURL: srv.URL, Protocol: model.AssistantProtocolOpenAIChat,
		CredentialMode: model.AssistantCredentialEnv, CredentialEnv: "JM_TEST_KEY2", Enabled: true,
	}
	require.NoError(t, db.Create(&p).Error)
	t.Setenv("JM_TEST_KEY2", "sk-test")

	res, err := svc.DiscoverModels(context.Background(), p.ID)
	require.Error(t, err, "非 200 必须报错")
	require.Nil(t, res, "不得返回空结果集")
	require.ErrorIs(t, err, ErrAssistantInvalid)
	require.Contains(t, err.Error(), "401")
	// 上游回显的密钥必须被脱敏（ai.RedactUpstreamBody 的职责，这里验串联生效）
	require.NotContains(t, err.Error(), "sk-leak-123")
}

func TestDiscoverModels_TruncatesAtLimit(t *testing.T) {
	// 造 assistantDiscoverMaxModels+5 条，验截断标记确实置上
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := 0; i < assistantDiscoverMaxModels+5; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"m-`)
		b.WriteString(strings.Repeat("x", 3))
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`"}`)
	}
	b.WriteString(`]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	svc, db := newDiscoverTestService(t)
	p := model.AssistantProvider{
		Name: "t", BaseURL: srv.URL, Protocol: model.AssistantProtocolOpenAIChat,
		CredentialMode: model.AssistantCredentialEnv, CredentialEnv: "JM_TEST_KEY3", Enabled: true,
	}
	require.NoError(t, db.Create(&p).Error)
	t.Setenv("JM_TEST_KEY3", "sk-test")

	res, err := svc.DiscoverModels(context.Background(), p.ID)
	require.NoError(t, err)
	require.Len(t, res.Discovered, assistantDiscoverMaxModels)
	require.True(t, res.Truncated, "超上限时必须显式告知，不能让调用方以为上游只有这么多")
}

func TestDiscoverModels_SkipsOversizedID(t *testing.T) {
	long := strings.Repeat("L", 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"` + long + `"},{"id":"ok"}]}`))
	}))
	defer srv.Close()

	svc, db := newDiscoverTestService(t)
	p := model.AssistantProvider{
		Name: "t", BaseURL: srv.URL, Protocol: model.AssistantProtocolOpenAIChat,
		CredentialMode: model.AssistantCredentialEnv, CredentialEnv: "JM_TEST_KEY4", Enabled: true,
	}
	require.NoError(t, db.Create(&p).Error)
	t.Setenv("JM_TEST_KEY4", "sk-test")

	res, err := svc.DiscoverModels(context.Background(), p.ID)
	require.NoError(t, err)
	require.Len(t, res.Discovered, 1)
	require.Equal(t, "ok", res.Discovered[0].ModelID)
	// 超长项要进 Skipped 并带原因——静默丢弃会让「上游 30 个这里 29 个」无从解释
	require.Len(t, res.Skipped, 1)
	for _, reason := range res.Skipped {
		require.Contains(t, reason, "256")
	}
}

func TestDiscoverModels_ProviderNotFound(t *testing.T) {
	svc, _ := newDiscoverTestService(t)
	_, err := svc.DiscoverModels(context.Background(), 99999)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAssistantNotFound)
}
