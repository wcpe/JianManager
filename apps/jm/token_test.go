package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// token create 的价值在于「一条命令拿到可用配置」，因此要守住：
// 请求体正确、明文只展示一次、配置片段真的可粘贴。

// 签发请求要带上用户指定的 scope / 能力 / TTL。
func TestRunTokenCreate_RequestShape(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/agent/tokens", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plaintext": "jmat_test_plain",
			"token": map[string]any{
				"id": 7, "name": "agent-ci", "tokenPrefix": "jmat_test",
				"expiresAt": "2027-01-01T00:00:00Z",
			},
		})
	}))
	defer ts.Close()

	cfg := Config{BaseURL: ts.URL, Token: "jmut_ok", Output: "json"}
	require.NoError(t, runTokenCreate(cfg, []string{
		"--name", "agent-ci", "--node", "1", "--capability", "instance.life", "--ttl-days", "30",
	}))

	assert.Equal(t, "agent-ci", got["name"])
	assert.Equal(t, float64(30), got["ttlDays"])
	nodes, _ := got["scopedNodeIds"].([]any)
	require.Len(t, nodes, 1)
	assert.Equal(t, float64(1), nodes[0])
	caps, _ := got["capabilities"].([]any)
	require.Len(t, caps, 1)
	assert.Equal(t, "instance.life", caps[0])
}

// --name 必填：没有名称的令牌日后无法辨认，属于必须挡下的误用。
func TestRunTokenCreate_RequiresName(t *testing.T) {
	cfg := Config{BaseURL: "http://unused", Token: "jmut_ok"}
	err := runTokenCreate(cfg, []string{"--node", "1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--name")
}

// 参数校验：非法数字与未知参数都要报错，不能静默忽略。
func TestRunTokenCreate_ArgValidation(t *testing.T) {
	cfg := Config{BaseURL: "http://unused", Token: "jmut_ok"}
	assert.Error(t, runTokenCreate(cfg, []string{"--name", "x", "--node", "abc"}))
	assert.Error(t, runTokenCreate(cfg, []string{"--name", "x", "--ttl-days", "0"}))
	assert.Error(t, runTokenCreate(cfg, []string{"--name", "x", "--policy-version", "9"}))
	assert.Error(t, runTokenCreate(cfg, []string{"--name", "x", "--bogus"}))
}

// 未登录时必须先要求登录，而不是发出注定 401 的请求。
func TestRunTokenCreate_RequiresLogin(t *testing.T) {
	cfg := Config{BaseURL: "http://unused"}
	err := runTokenCreate(cfg, []string{"--name", "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未登录")
}

// 服务端没给明文时要明确报错，不能让用户以为创建成功却拿不到令牌。
func TestRunTokenCreate_MissingPlaintext(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": map[string]any{"id": 1, "name": "x"},
		})
	}))
	defer ts.Close()

	cfg := Config{BaseURL: ts.URL, Token: "jmut_ok", Output: "json"}
	err := runTokenCreate(cfg, []string{"--name", "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "明文")
}

// 生成的 MCP 配置片段必须包含端点与令牌，且是合法 JSON。
//
// 这段是直接粘进客户端的，格式错一位用户就配不通，因此用 JSON 解析来守。
func TestMCPConfigSnippet_IsValidJSON(t *testing.T) {
	snippet := mcpConfigSnippet("https://cp.example.com/", "jmat_abc123")

	// 片段里还带了 curl 示例，取第一个 JSON 块做校验。
	start := strings.Index(snippet, "{")
	end := strings.Index(snippet, "\n}")
	require.True(t, start >= 0 && end > start, "应包含 JSON 块")
	block := snippet[start : end+2]

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(block), &parsed), "配置片段必须是合法 JSON:\n%s", block)

	servers, ok := parsed["mcpServers"].(map[string]any)
	require.True(t, ok, "缺少 mcpServers")
	jmCfg, ok := servers["jianmanager"].(map[string]any)
	require.True(t, ok, "缺少 jianmanager 条目")
	// 尾部斜杠必须被裁掉，否则会拼出 //api/v1/mcp。
	assert.Equal(t, "https://cp.example.com/api/v1/mcp", jmCfg["url"])
	headers, _ := jmCfg["headers"].(map[string]any)
	assert.Equal(t, "Bearer jmat_abc123", headers["Authorization"])
}

// list 的响应是裸数组（与 PagedResult 不同），解析必须匹配。
func TestRunTokenList_ParsesArray(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "name": "a", "tokenPrefix": "jmat_a", "expiresAt": "2027-01-01", "revoked": false},
			{"id": 2, "name": "b", "tokenPrefix": "jmat_b", "expiresAt": "2027-01-01", "revoked": true},
		})
	}))
	defer ts.Close()

	cfg := Config{BaseURL: ts.URL, Token: "jmut_ok", Output: "text"}
	assert.NoError(t, runTokenList(cfg))
}
