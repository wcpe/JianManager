package ws

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalServer_RejectsReusedToken(t *testing.T) {
	server := NewTerminalServer(testSecret)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/terminal", server.Handler())
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	now := time.Now()
	token := signToken(t, testSecret, jwt.MapClaims{
		"instanceId": "inst-terminal-reuse",
		"permission": "write",
		"exp":        now.Add(30 * time.Second).Unix(),
		"iat":        now.Unix(),
	})
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/terminal"

	first, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token="+url.QueryEscape(token), nil)
	if resp != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err)
	require.NoError(t, first.SetReadDeadline(time.Now().Add(2*time.Second)))
	var welcome TerminalMessage
	require.NoError(t, first.ReadJSON(&welcome))
	assert.Equal(t, "stdout", welcome.Type)
	require.NoError(t, first.Close())

	second, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token="+url.QueryEscape(token), nil)
	if second != nil {
		_ = second.Close()
	}
	require.Error(t, err, "同一个终端 token 不能直接复用连接 Worker WS")
	if resp != nil {
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
}
