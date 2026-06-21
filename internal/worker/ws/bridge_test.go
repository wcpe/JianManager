package ws

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-bridge-secret"

// signToken 用给定 secret 签发一个 HS256 token，便于各用例构造合法/非法 token。
func signToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	require.NoError(t, err)
	return s
}

func TestValidateBridgeToken(t *testing.T) {
	now := time.Now()
	validClaims := jwt.MapClaims{
		"instanceId": "inst-uuid-1",
		"scope":      PluginBridgeScope,
		"exp":        now.Add(5 * time.Minute).Unix(),
		"iat":        now.Unix(),
	}

	tests := []struct {
		name          string
		token         string
		queryInstance string
		wantInstance  string
		wantErr       error
	}{
		{
			name:          "合法 token + instance 一致",
			token:         signToken(t, testSecret, validClaims),
			queryInstance: "inst-uuid-1",
			wantInstance:  "inst-uuid-1",
		},
		{
			name:          "合法 token + query 省略 instance（仅校验签名与 scope）",
			token:         signToken(t, testSecret, validClaims),
			queryInstance: "",
			wantInstance:  "inst-uuid-1",
		},
		{
			name:    "空 token 被拒",
			token:   "",
			wantErr: errBridgeNoToken,
		},
		{
			name:    "错误签名密钥被拒",
			token:   signToken(t, "wrong-secret", validClaims),
			wantErr: errBridgeBadToken,
		},
		{
			name: "scope 非 plugin-bridge 被拒（终端 token 不得冒连）",
			token: signToken(t, testSecret, jwt.MapClaims{
				"instanceId": "inst-uuid-1",
				"permission": "write", // 终端 token 形态，无 plugin-bridge scope
				"exp":        now.Add(5 * time.Minute).Unix(),
			}),
			wantErr: errBridgeBadScope,
		},
		{
			name: "缺 instanceId 被拒",
			token: signToken(t, testSecret, jwt.MapClaims{
				"scope": PluginBridgeScope,
				"exp":   now.Add(5 * time.Minute).Unix(),
			}),
			wantErr: errBridgeNoInstance,
		},
		{
			name:          "instanceId 与 query instance 不一致被拒",
			token:         signToken(t, testSecret, validClaims),
			queryInstance: "other-uuid",
			wantErr:       errBridgeInstMismatch,
		},
		{
			name: "过期 token 被拒",
			token: signToken(t, testSecret, jwt.MapClaims{
				"instanceId": "inst-uuid-1",
				"scope":      PluginBridgeScope,
				"exp":        now.Add(-1 * time.Minute).Unix(),
			}),
			wantErr: errBridgeBadToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateBridgeToken(testSecret, tt.token, tt.queryInstance)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantInstance, got)
		})
	}
}

// dialBridge 用合法 token 连一个 httptest 起的插件桥服务端，返回客户端连接。
func dialBridge(t *testing.T, srvURL, instance string) *websocket.Conn {
	t.Helper()
	token := signToken(t, testSecret, jwt.MapClaims{
		"instanceId": instance,
		"scope":      PluginBridgeScope,
		"exp":        time.Now().Add(5 * time.Minute).Unix(),
	})
	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + "/ws/plugin-bridge?token=" + token + "&instance=" + instance
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	return conn
}

// TestSessionTable_SingleActiveReplace 验证「实例 UUID→会话」表的单活动会话顶替语义：
// 同实例第二次连入顶替第一次（旧连被关闭），表内同实例始终仅 1 个会话；不同实例各占一格。
// 经真实 WS 服务端建会话（避免不可构造的裸 *websocket.Conn）。
func TestSessionTable_SingleActiveReplace(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn1 := dialBridge(t, srv.URL, "inst-1")
	defer conn1.Close()
	// 等首连入表。
	require.Eventually(t, func() bool { return s.IsConnected("inst-1") }, time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, s.SessionCount())

	// 同实例第二连：顶替旧连，表内 inst-1 仍仅 1 个会话。
	conn2 := dialBridge(t, srv.URL, "inst-1")
	defer conn2.Close()
	require.Eventually(t, func() bool {
		// 顶替完成后旧连被关闭、新连在表，会话数稳定为 1。
		return s.SessionCount() == 1 && s.IsConnected("inst-1")
	}, time.Second, 10*time.Millisecond)

	// 不同实例独立占一格。
	conn3 := dialBridge(t, srv.URL, "inst-2")
	defer conn3.Close()
	require.Eventually(t, func() bool { return s.IsConnected("inst-2") }, time.Second, 10*time.Millisecond)
	assert.Equal(t, 2, s.SessionCount())
}

// TestBridge_HandshakeHeartbeatLifecycle 端到端走一遍握手→connected→hello→ping/pong→断开→disconnected。
// 用 httptest 起真实 WS 服务端，gorilla 客户端连入，校验事件冒泡顺序与心跳回应。
func TestBridge_HandshakeHeartbeatLifecycle(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)

	var mu sync.Mutex
	var events []PluginEvent
	s.SetEventHandler(func(e PluginEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	token := signToken(t, testSecret, jwt.MapClaims{
		"instanceId": "inst-1",
		"scope":      PluginBridgeScope,
		"exp":        time.Now().Add(5 * time.Minute).Unix(),
	})
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/plugin-bridge?token=" + token + "&instance=inst-1"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer conn.Close()

	// 服务端应回 welcome。
	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))
	assert.Equal(t, "welcome", welcome.Type)
	assert.Equal(t, "inst-1", welcome.Instance)

	// 客户端发 hello + ping。
	require.NoError(t, conn.WriteJSON(bridgeMessage{Type: "hello", Platform: "bukkit", Version: "1.2.3"}))
	require.NoError(t, conn.WriteJSON(bridgeMessage{Type: "ping"}))

	// 服务端应回 pong。
	var pong bridgeMessage
	require.NoError(t, conn.ReadJSON(&pong))
	assert.Equal(t, "pong", pong.Type)

	// 主动关闭，触发 disconnected。
	conn.Close()

	// 轮询等待 disconnected 冒泡（异步 handleSession）。
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range events {
			if e.Type == PluginEventDisconnected {
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond, "应冒泡 disconnected 事件")

	mu.Lock()
	defer mu.Unlock()
	// 首个事件必为 connected。
	require.NotEmpty(t, events)
	assert.Equal(t, PluginEventConnected, events[0].Type)
	assert.Equal(t, "inst-1", events[0].InstanceUUID)
	// 应包含一次 heartbeat（来自 ping）。
	var sawHeartbeat bool
	for _, e := range events {
		if e.Type == PluginEventHeartbeat {
			sawHeartbeat = true
		}
	}
	assert.True(t, sawHeartbeat, "ping 应冒泡 heartbeat 事件")
}

// TestBridge_RejectsBadScopeOverWS 验证非法 token（scope 错）在 WS 握手层被拒（HTTP 401，不升级）。
func TestBridge_RejectsBadScopeOverWS(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	badToken := signToken(t, testSecret, jwt.MapClaims{
		"instanceId": "inst-1",
		"permission": "write", // 无 plugin-bridge scope
		"exp":        time.Now().Add(5 * time.Minute).Unix(),
	})
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/plugin-bridge?token=" + badToken + "&instance=inst-1"

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err) // 握手失败
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
