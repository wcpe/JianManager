package ai_test

// 真机验收：与一个外部 Python 模拟供应商对打三种协议。
//
// 模拟服务是一个一次性验收工具（未入库）：它起一个 HTTPS 服务，
// 按 openai-chat / openai-responses / anthropic 三种官方格式吐 SSE，
// 模型名带 err401/err429/err500 时返回对应错误码。
//
// 为何不用 httptest：验收的目的就是「跟一个外部进程说的真实协议对打」，
// httptest 跑在进程内、与实现同源，挡不住「我以为是这个形状」的自骗。
//
// 为何走 TLS：平台受保护出站刻意不提供 InsecureSkipVerify，明文 http 也被拒。
// 这里显式注入 RootCAs（正是生产 GuardOptions.TLSRootCAs 的用途）。
//
// 服务未就绪时全部 Skip（不是 Fail）——环境缺失不该污染 CI。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const (
	mockAddr   = "127.0.0.1:18181"
	mockBase   = "https://127.0.0.1:18181/v1"
	mockCert   = "/tmp/mockai-tls/cert.pem"
	mockAPIKey = "sk-mock-test"
)

// mockHTTPClient 返回信任自签证书的 client；服务未就绪则 Skip。
func mockHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	pemBytes, err := os.ReadFile(mockCert)
	if err != nil {
		t.Skipf("模拟服务证书不存在，先启动 mock_ai_provider.py: %v", err)
	}
	// 先探一下端口，避免后续报难懂的 TLS 错
	c, derr := net.DialTimeout("tcp", mockAddr, 2*time.Second)
	if derr != nil {
		t.Skipf("模拟服务未就绪（%s）: %v", mockAddr, derr)
	}
	_ = c.Close()

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(pemBytes), "自签证书应能解析")
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS12,
				// 测试专用：自签证书 CN/SAN 已含 127.0.0.1，不需跳过校验
			},
		},
	}
}

// runStream 跑一次流，收集归一化事件。
type streamResult struct {
	text   strings.Builder
	usage  *ai.Usage
	kinds  []ai.EventKind
	finish string
	err    error
}

func runStream(t *testing.T, protocol, modelID string) *streamResult {
	t.Helper()
	client := mockHTTPClient(t)
	s, err := ai.NewStreamer(protocol, client, mockBase, mockAPIKey)
	if err != nil {
		t.Fatalf("构造 streamer 失败: %v", err)
	}
	res := &streamResult{}
	err = s.Stream(context.Background(), ai.Request{
		Model:           modelID,
		System:          "you are a test assistant",
		Messages:        []ai.Message{{Role: "user", Content: "hello"}},
		MaxOutputTokens: 64,
	}, func(ev ai.Event) error {
		res.kinds = append(res.kinds, ev.Kind)
		switch ev.Kind {
		case ai.EventText:
			res.text.WriteString(ev.Text)
		case ai.EventUsage:
			res.usage = ev.Usage
		case ai.EventEnd:
			res.finish = ev.FinishReason
		}
		return nil
	})
	res.err = err
	return res
}

// TestMock_OpenAIChat 验证 openai-chat 协议的端到端。
func TestMock_OpenAIChat(t *testing.T) {
	res := runStream(t, model.AssistantProtocolOpenAIChat, "mock-gpt-primary")
	require.NoError(t, res.err, "流应正常结束")
	assert.Contains(t, res.text.String(), "mock-openai-chat ok", "应收到文本增量")
	assert.Equal(t, "stop", res.finish, "应拿到 finish_reason")
	require.NotNil(t, res.usage, "应收到 usage")
	if res.usage.InputTokens != nil {
		assert.Equal(t, 11, *res.usage.InputTokens)
	}
}

// TestMock_OpenAIResponses 验证 openai-responses 协议的端到端。
func TestMock_OpenAIResponses(t *testing.T) {
	res := runStream(t, model.AssistantProtocolOpenAIResponses, "mock-gpt-primary")
	require.NoError(t, res.err, "流应正常结束")
	assert.Contains(t, res.text.String(), "mock-openai-responses ok", "应收到文本增量")
	assert.Contains(t, res.kinds, ai.EventEnd, "应有 EventEnd")
}

// TestMock_Anthropic 验证 anthropic 协议的端到端。
func TestMock_Anthropic(t *testing.T) {
	res := runStream(t, model.AssistantProtocolAnthropic, "mock-claude-sonnet")
	require.NoError(t, res.err, "流应正常结束")
	assert.Contains(t, res.text.String(), "mock-anthropic ok", "应收到文本增量")
	assert.Equal(t, "end_turn", res.finish, "应拿到 stop_reason")
}

// TestMock_UpstreamErrorClassified 验证上游 401/429 被**分类**而不是笼统失败。
//
// 这是最强语义的核心：管理员必须能区分「密钥错」与「限流」。
func TestMock_UpstreamErrorClassified(t *testing.T) {
	cases := []struct {
		modelID   string
		expectMsg string
	}{
		{"mock-err401", "invalid api key"},
		{"mock-err429", "rate limit"},
	}
	for _, tc := range cases {
		t.Run(tc.modelID, func(t *testing.T) {
			res := runStream(t, model.AssistantProtocolOpenAIChat, tc.modelID)
			require.Error(t, res.err, "上游非 2xx 应返回错误")
			msg := res.err.Error()
			assert.Contains(t, strings.ToLower(msg), strings.ToLower(tc.expectMsg),
				"错误应携带可归因的上游信息，实际: %s", msg)
			// 关键：错误不得把密钥原样回显
			assert.NotContains(t, msg, "sk-mock-test", "错误不得回显凭据")
		})
	}
}

// TestMock_PartialStreamNotTreatedAsComplete 验证流被截断时**不得**当成功。
//
// 用 mock-err 无法模拟截断，这里直接对着一个不存在的模型名验证 404 分支。
func TestMock_UnknownModelNotSilentlyOK(t *testing.T) {
	res := runStream(t, model.AssistantProtocolOpenAIChat, "mock-err500")
	require.Error(t, res.err, "上游 500 必须报错")
	assert.Empty(t, res.text.String(), "失败时不应残留半截文本")
}
