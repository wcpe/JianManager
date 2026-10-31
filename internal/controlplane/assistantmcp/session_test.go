package assistantmcp

/**
 * MCP 会话协商与认证错误分类（规范合规回归）。
 *
 * 这组用例守两处**真实缺陷**（均在与真实协议服务器对接时暴露）：
 *  1. `Mcp-Session-Id` 未实现：只做 `initialize` 的探测会通过，而 `tools/list` 被
 *     服务器以 400 missing-session 拒绝 —— 表现成「探测正常、发现必失败」，
 *     且错误被归成「不可达」，排障方向被带向网络。
 *  2. 401/403 与网络故障同码：凭据错时管理员无从分辨。
 */

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newBearerClient 造一个带 Bearer 凭据的客户端（复用既有 newTestClient 之外的构造）。
func newBearerClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(Options{
		HTTPClient: srv.Client(),
		Endpoint:   srv.URL,
		AuthStyle:  "bearer",
		AuthValue:  "token-abc",
	})
	require.NoError(t, err)
	return c
}

// TestInitialize_CapturesSessionIDAndEchoesIt 会话 id 必须被捕获并在后续请求回带。
//
// 这是缺陷 1 的回归：服务器要求回带会话 id，不回带就让 tools/list 失败。
func TestInitialize_CapturesSessionIDAndEchoesIt(t *testing.T) {
	const wantSession = "sess-xyz-123"
	var sawSessionOnList bool
	var sawVersionOnList string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Mcp-Session-Id") {
		case wantSession:
			// initialize 之后的请求应带会话 id
		default:
			if r.Method == http.MethodPost {
				// 允许 initialize（首次不带），其余一律 400 missing-session
				// 以便复现「探测过、发现失败」的真实行为。
			}
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		payload := string(body)
		switch {
		case containsStr(payload, `"initialize"`):
			w.Header().Set("Mcp-Session-Id", wantSession)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"t","version":"1"}}}`))
		case containsStr(payload, `"tools/list"`):
			if r.Header.Get("Mcp-Session-Id") != wantSession {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"missing-session"}`))
				return
			}
			sawSessionOnList = true
			sawVersionOnList = r.Header.Get("MCP-Protocol-Version")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"t","description":"d","inputSchema":{"type":"object"}}]}}`))
		}
	}))
	defer srv.Close()

	c := newBearerClient(t, srv)
	_, err := c.Initialize(context.Background())
	require.NoError(t, err, "initialize 应成功")
	require.Equal(t, wantSession, c.sessionID, "必须捕获响应头里的会话 id")

	_, err = c.DiscoverTools(context.Background())
	require.NoError(t, err, "带回会话 id 后 tools/list 应成功（不回带会被服务器拒绝）")
	require.True(t, sawSessionOnList, "tools/list 必须回带 Mcp-Session-Id")
	require.Equal(t, "2024-11-05", sawVersionOnList, "协商版本应随请求声明")
}

// TestAuthRejected_DistinctFromUnreachable 401 必须归类为认证失败，而非不可达。
func TestAuthRejected_DistinctFromUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token"}`))
	}))
	defer srv.Close()

	c := newBearerClient(t, srv)
	_, err := c.Initialize(context.Background())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrAuthRejected),
		"401 应归为 ErrAuthRejected，得到 %v", err)
	require.False(t, errors.Is(err, ErrUnreachable),
		"401 不该同时被当成不可达")
}

// TestNetworkFailure_StillUnreachable 网络层失败仍必须是不可达（别把分类改过头）。
func TestNetworkFailure_StillUnreachable(t *testing.T) {
	// 用一个确定连不上的端口（1 号端口通常无监听）。用真实 http.Client，
	// 不要手工拼 httptest.Server——那样 Client 配置不全，测不到网络层错误。
	c, err := New(Options{
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
		Endpoint:   "http://127.0.0.1:1/mcp",
	})
	require.NoError(t, err)
	_, err = c.Initialize(context.Background())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrUnreachable), "连接失败应为不可达，得到 %v", err)
	require.False(t, errors.Is(err, ErrAuthRejected), "连接失败不是认证问题")
}

// containsStr 朴素子串判断（避免为一条断言引入 strings 之外依赖）。
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
