// FR-276（见 ADR-061）：终端代理 401 诊断兜底——Worker 拒绝令牌与网络类失败给出可区分的错误。
package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wgrpc "github.com/wcpe/JianManager/internal/worker/grpc"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// readFirstProxyMessage 从代理浏览器侧连接读取首条 JSON 消息（2s 超时）。
func readFirstProxyMessage(t *testing.T, wsURL, token string) map[string]any {
	t.Helper()
	conn := dialTerminalProxy(t, wsURL, token)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	var msg map[string]any
	require.NoError(t, conn.ReadJSON(&msg))
	return msg
}

// TestTerminalProxy_WorkerTokenRejected_Diagnostic Worker 用不同密钥校验（模拟密钥不一致）→
// 浏览器收到 code=WORKER_TOKEN_REJECTED 的定向诊断，而非裸「连接已断开」。
func TestTerminalProxy_WorkerTokenRejected_Diagnostic(t *testing.T) {
	// 「Worker」侧用另一把密钥 → CP 签的终端 token 在 Worker 侧 401。
	db, instanceID := newTerminalTestDB(t, startWorkerTerminalWS(t, "worker-side-DIFFERENT-secret"))
	svc := NewTerminalService(db, "cp-ws-secret", "ws://fallback.invalid")
	proxy := NewTerminalProxy("cp-ws-secret", svc)
	wsrv := &wgrpc.Server{}
	wsrv.SetTerminalWSAddr(startWorkerTerminalWS(t, "worker-side-DIFFERENT-secret"))
	client := startTerminalWorkerGRPC(t, wsrv)
	proxy.SetWorkerClients(func(string) (workerpb.WorkerServiceClient, bool) { return client, true })
	cpHTTP := httptest.NewServer(proxy.Handler())
	defer cpHTTP.Close()

	token, err := svc.IssueToken(instanceID, "write", mustHost(t, cpHTTP.URL), false)
	require.NoError(t, err)

	msg := readFirstProxyMessage(t, token.WSURL, token.Token)
	assert.Equal(t, "state", msg["type"])
	assert.Equal(t, "error", msg["state"])
	assert.Equal(t, TerminalProxyCodeWorkerTokenRejected, msg["code"])
	assert.Contains(t, msg["data"], "WS 令牌密钥与平台不一致", "诊断文案应点明根因")
}

// TestTerminalProxy_TunnelUnavailable_GenericError 没有活跃隧道时返回一般错误且保留令牌重试机会。
func TestTerminalProxy_TunnelUnavailable_GenericError(t *testing.T) {
	db, instanceID := newTerminalTestDB(t, "http://127.0.0.1:1")
	svc := NewTerminalService(db, "cp-ws-secret", "ws://fallback.invalid")
	proxy := NewTerminalProxy("cp-ws-secret", svc)
	cpHTTP := httptest.NewServer(proxy.Handler())
	defer cpHTTP.Close()

	token, err := svc.IssueToken(instanceID, "read", mustHost(t, cpHTTP.URL), false)
	require.NoError(t, err)

	msg := readFirstProxyMessage(t, token.WSURL, token.Token)
	assert.Equal(t, "error", msg["state"])
	_, hasCode := msg["code"]
	assert.False(t, hasCode, "网络类失败不应携带 WORKER_TOKEN_REJECTED 诊断码")
	assert.Contains(t, msg["data"], "反向隧道不可用")
}
