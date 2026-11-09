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

func TestFR037TerminalServerAcceptsControlPlaneToken(t *testing.T) {
	server := NewTerminalServer(testSecret)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/terminal", server.Handler())
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	now := time.Now()
	token := signToken(t, testSecret, jwt.MapClaims{
		"instanceId": "fr037-survival-uuid",
		"permission": "write",
		"exp":        now.Add(30 * time.Second).Unix(),
		"iat":        now.Unix(),
	})
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/terminal"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token="+url.QueryEscape(token), nil)
	if resp != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))

	var welcome TerminalMessage
	require.NoError(t, conn.ReadJSON(&welcome))
	assert.Equal(t, "stdout", welcome.Type)
	assert.Equal(t, "fr037-survival-uuid", welcome.InstanceID)
	assert.Contains(t, welcome.Data, "已连接到实例 fr037-survival-uuid")
}
