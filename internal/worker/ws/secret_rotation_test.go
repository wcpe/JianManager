// FR-275（见 ADR-061）：终端/插件桥校验密钥热更新（SetJWTSecret）对握手的即时影响。
package ws

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// terminalToken 用给定 secret 签发一个终端形态 token（instanceId+permission+exp）。
func terminalToken(t *testing.T, secret, instanceID string) string {
	t.Helper()
	return signToken(t, secret, jwt.MapClaims{
		"instanceId": instanceID,
		"permission": "read",
		"exp":        time.Now().Add(30 * time.Second).Unix(),
		"iat":        time.Now().Unix(),
	})
}

// dialTerminal 向测试服务器发起终端 WS 握手，返回是否升级成功（101）。
func dialTerminal(t *testing.T, srvURL, token string) bool {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + "/?token=" + token
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// TestTerminalServer_SetJWTSecret_RotatesValidation 换密钥后：旧密钥签的 token 拒、新密钥签的过。
func TestTerminalServer_SetJWTSecret_RotatesValidation(t *testing.T) {
	s := NewTerminalServer("old-secret")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// 初始密钥可握手。
	require.True(t, dialTerminal(t, srv.URL, terminalToken(t, "old-secret", "inst-1")), "初始密钥应握手成功")

	// 热更新密钥（模拟 CP 注册/心跳下发，FR-275）。
	s.SetJWTSecret("new-secret")

	assert.False(t, dialTerminal(t, srv.URL, terminalToken(t, "old-secret", "inst-2")), "旧密钥签的 token 应被拒")
	assert.True(t, dialTerminal(t, srv.URL, terminalToken(t, "new-secret", "inst-3")), "新密钥签的 token 应握手成功")
}

// TestPluginBridgeServer_SetJWTSecret_RotatesValidation 插件桥同款：热更新即时影响后续握手。
func TestPluginBridgeServer_SetJWTSecret_RotatesValidation(t *testing.T) {
	s := NewPluginBridgeServer("old-secret")
	s.SetJWTSecret("new-secret")

	claims := jwt.MapClaims{
		"instanceId": "inst-1",
		"scope":      PluginBridgeScope,
		"exp":        time.Now().Add(5 * time.Minute).Unix(),
	}

	// 校验入口读 currentJWTSecret（热更新后值）：旧密钥签名拒、新密钥签名过。
	_, err := validateBridgeToken(s.currentJWTSecret(), signToken(t, "old-secret", claims), "inst-1")
	assert.ErrorIs(t, err, errBridgeBadToken, "旧密钥签的桥 token 应被拒")

	got, err := validateBridgeToken(s.currentJWTSecret(), signToken(t, "new-secret", claims), "inst-1")
	require.NoError(t, err)
	assert.Equal(t, "inst-1", got)
}
