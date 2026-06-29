package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newDiagTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}))
	return db
}

// 仅放行带 host 的 http/https 绝对 URL，其余（空 / file:// / 相对）→ ErrInvalidTestURL（拒绝 SSRF 面外的 scheme）。
func TestDiagnostics_TestHTTPReachability_RejectsInvalidURL(t *testing.T) {
	svc := NewDiagnosticsService(nil, nil)
	svc.SetHTTPClientProvider(func() *http.Client { return http.DefaultClient })
	for _, bad := range []string{"", "   ", "file:///etc/passwd", "ftp://x", "not a url", "/relative", "http://"} {
		_, err := svc.TestHTTPReachability(context.Background(), bad)
		require.ErrorIs(t, err, ErrInvalidTestURL, "应拒绝: %q", bad)
	}
}

// 命中可达目标 → OK=true + 透出状态码（经注入的出站客户端）。
func TestDiagnostics_TestHTTPReachability_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()
	svc := NewDiagnosticsService(nil, nil)
	svc.SetHTTPClientProvider(srv.Client)
	res, err := svc.TestHTTPReachability(context.Background(), srv.URL)
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, http.StatusTeapot, res.Status)
}

// 出站客户端未注入 → 报错（非 panic）。
func TestDiagnostics_TestHTTPReachability_NoProvider(t *testing.T) {
	svc := NewDiagnosticsService(nil, nil)
	_, err := svc.TestHTTPReachability(context.Background(), "https://example.com")
	require.Error(t, err)
}

// PingNode：节点不存在 → ErrNodeNotFound。
func TestDiagnostics_PingNode_NotFound(t *testing.T) {
	db := newDiagTestDB(t)
	svc := NewDiagnosticsService(db, cpgrpc.NewClientPool())
	_, err := svc.PingNode(context.Background(), 999999)
	require.ErrorIs(t, err, ErrNodeNotFound)
}

// PingNode：节点存在但未连接（空连接池）→ Alive=false（非 error），便于前端按结果显示离线。
func TestDiagnostics_PingNode_Offline(t *testing.T) {
	db := newDiagTestDB(t)
	node := &model.Node{Name: "diag-offline", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", UUID: "diag-uuid-offline", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	svc := NewDiagnosticsService(db, cpgrpc.NewClientPool())
	res, err := svc.PingNode(context.Background(), node.ID)
	require.NoError(t, err)
	require.False(t, res.Alive)
	require.NotEmpty(t, res.Error)
}
