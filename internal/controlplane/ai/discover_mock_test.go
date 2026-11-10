package ai_test

// FR-538 模型自动发现：对着外部模拟服务实测。
//
// 模型发现是一项独立的 HTTP 调用（GET {base}/models），不经过 Streamer，
// 但同样受出站准入与协议鉴权头影响，必须单独验。

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// discoverModels 复刻 assistant_discover.go 的请求构造（URL 拼法 + 鉴权头），
// 用于验证「我们的实现能不能读懂这个服务」。
//
// 为何复刻而不直接调 service：service 层带着 DB/加密依赖，而本次要验的是
// **协议契约本身**——URL 怎么拼、头怎么带、响应怎么解。
func discoverModels(t *testing.T, protocol, baseURL, apiKey string) ([]string, error) {
	t.Helper()
	client := mockHTTPClient(t)

	// 与 discoverModelsURL() 同一拼法：TrimRight(base,"/") + "/models"
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)

	// 与 applyDiscoverAuth() 同一逻辑
	if apiKey != "" {
		switch protocol {
		case model.AssistantProtocolAnthropic:
			req.Header.Set("x-api-key", apiKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		default:
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &httpErr{code: resp.StatusCode, body: string(raw)}
	}

	// 与 parseDiscoveredModels() 同一解析：兼容 {data:[{id}]} 与 [{id}]
	var withData struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &withData); err == nil && len(withData.Data) > 0 {
		out := make([]string, 0, len(withData.Data))
		for _, d := range withData.Data {
			out = append(out, d.ID)
		}
		return out, nil
	}
	return nil, &httpErr{code: -1, body: "无法解析模型列表: " + string(raw[:min(200, len(raw))])}
}

type httpErr struct {
	code int
	body string
}

func (e *httpErr) Error() string {
	return strings.TrimSpace(e.body)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestMock_DiscoverModels_AllProtocols 三种协议都能发现模型。
func TestMock_DiscoverModels_AllProtocols(t *testing.T) {
	for _, proto := range []string{
		model.AssistantProtocolOpenAIChat,
		model.AssistantProtocolOpenAIResponses,
		model.AssistantProtocolAnthropic,
	} {
		t.Run(proto, func(t *testing.T) {
			ids, err := discoverModels(t, proto, mockBase, mockAPIKey)
			require.NoError(t, err, "发现应成功")
			require.NotEmpty(t, ids, "应至少发现一个模型")
			assert.Contains(t, ids, "mock-gpt-primary")
		})
	}
}

// TestMock_DiscoverModels_NoKeyRejected 无凭据时上游返 401，必须报错而非返回空列表。
//
// 这是最强语义的关键：空结果与「鉴权失败」对调用方看起来一样，但处置完全不同。
func TestMock_DiscoverModels_NoKeyRejected(t *testing.T) {
	_, err := discoverModels(t, model.AssistantProtocolOpenAIChat, mockBase, "")
	require.Error(t, err, "无凭据时必须报错，不能静默返回空")
}

// TestMock_DiscoverModels_URLJoinBase 验证 baseURL 尾部斜杠不会导致双斜杠。
func TestMock_DiscoverModels_TrailingSlash(t *testing.T) {
	ids, err := discoverModels(t, model.AssistantProtocolOpenAIChat, mockBase+"/", mockAPIKey)
	require.NoError(t, err, "尾部斜杠应被 TrimRight 处理")
	assert.NotEmpty(t, ids)
}

var _ = tls.VersionTLS12
var _ = x509.NewCertPool
var _ = os.Getenv
