package service

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	workerws "github.com/wcpe/JianManager/internal/worker/ws"
)

// TestBuildTerminalWSURL 覆盖终端代理 WS URL 的 scheme 选择与 baseURL 回退。
func TestBuildTerminalWSURL(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		requestHost string
		secure      bool
		want        string
	}{
		{"非加密访问 → ws", "ws://localhost:8080", "192.168.1.100:8080", false, "ws://192.168.1.100:8080/ws/terminal"},
		{"HTTPS/反代访问 → wss", "ws://localhost:8080", "panel.example.com", true, "wss://panel.example.com/ws/terminal"},
		{"空 Host 回退 baseURL", "ws://localhost:8080", "", false, "ws://localhost:8080/ws/terminal"},
		{"空 Host 时 secure 不改写 baseURL", "wss://panel.example.com", "", true, "wss://panel.example.com/ws/terminal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, buildTerminalWSURL(tt.baseURL, tt.requestHost, tt.secure))
		})
	}
}

func TestIssueToken_ThirtySecondTTL(t *testing.T) {
	db, instanceID := newTerminalTestDB(t, "http://127.0.0.1:19007")
	svc := NewTerminalService(db, "terminal-secret", "ws://fallback.invalid")

	tok, err := svc.IssueToken(instanceID, "write", "panel.example.com", true)
	require.NoError(t, err)
	require.NotEmpty(t, tok.Token)
	assert.Equal(t, "wss://panel.example.com/ws/terminal", tok.WSURL)
	assert.Equal(t, 30, tok.ExpiresIn)

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(tok.Token, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte("terminal-secret"), nil
	})
	require.NoError(t, err)
	require.True(t, parsed.Valid)
	assert.Equal(t, "inst-terminal-test", claims["instanceId"])
	assert.Equal(t, "write", claims["permission"])

	exp, err := claims.GetExpirationTime()
	require.NoError(t, err)
	require.NotNil(t, exp)
	iat, err := claims.GetIssuedAt()
	require.NoError(t, err)
	require.NotNil(t, iat)
	assert.InDelta(t, 30, exp.Time.Sub(iat.Time).Seconds(), 1)
}

func TestTerminalProxy_RejectsReusedToken(t *testing.T) {
	secret := "terminal-proxy-secret"
	workerServer := workerws.NewTerminalServer(secret)
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("/ws/terminal", workerServer.Handler())
	workerHTTP := httptest.NewServer(workerMux)
	defer workerHTTP.Close()

	db, instanceID := newTerminalTestDB(t, workerHTTP.URL)
	svc := NewTerminalService(db, secret, "ws://fallback.invalid")
	proxy := NewTerminalProxy(secret, svc)
	cpHTTP := httptest.NewServer(proxy.Handler())
	defer cpHTTP.Close()

	token, err := svc.IssueToken(instanceID, "write", mustHost(t, cpHTTP.URL), false)
	require.NoError(t, err)

	first := dialTerminalProxy(t, token.WSURL, token.Token)
	require.NoError(t, first.SetReadDeadline(time.Now().Add(2*time.Second)))
	var welcome map[string]any
	require.NoError(t, first.ReadJSON(&welcome))
	require.NoError(t, first.Close())

	second, resp, err := websocket.DefaultDialer.Dial(token.WSURL+"?token="+url.QueryEscape(token.Token), nil)
	if second != nil {
		_ = second.Close()
	}
	require.Error(t, err, "同一个终端 token 只能完成一次 WS 握手")
	if resp != nil {
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
}

func newTerminalTestDB(t *testing.T, workerURL string) (*gorm.DB, uint) {
	t.Helper()
	host, port := mustHostPort(t, workerURL)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "terminal.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))

	wsPort, err := strconv.Atoi(port)
	require.NoError(t, err)
	node := model.Node{
		UUID:     "node-terminal-test",
		Name:     "node-terminal-test",
		Host:     host,
		GRPCPort: 19101,
		WSPort:   wsPort,
		Secret:   "node-secret",
		Status:   model.NodeStatusOnline,
	}
	require.NoError(t, db.Create(&node).Error)

	inst := model.Instance{
		UUID:         "inst-terminal-test",
		NodeID:       node.ID,
		Name:         "terminal-test",
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDirect,
		Status:       model.InstanceStatusRunning,
		StartCommand: "smoke",
	}
	require.NoError(t, db.Create(&inst).Error)
	return db, inst.ID
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Host
}

func mustHostPort(t *testing.T, rawURL string) (string, string) {
	t.Helper()
	hostPort := mustHost(t, rawURL)
	host, port, err := net.SplitHostPort(hostPort)
	require.NoError(t, err)
	return host, port
}

func dialTerminalProxy(t *testing.T, wsURL, token string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token="+url.QueryEscape(token), nil)
	if resp != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err)
	return conn
}
