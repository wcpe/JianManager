package service

import (
	"context"
	"net/http"
	"net/netip"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

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
	svc := NewDiagnosticsService(nil, nil)
	svc.SetHTTPClientProvider(func() *http.Client {
		return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusTeapot, Body: http.NoBody, Request: req}, nil
		})}
	})
	res, err := svc.TestHTTPReachability(context.Background(), "https://8.8.8.8/download")
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, http.StatusTeapot, res.Status)
}

func TestDiagnostics_TestHTTPReachability_RejectsRestrictedAddress(t *testing.T) {
	svc := NewDiagnosticsService(nil, nil)
	svc.SetHTTPClientProvider(func() *http.Client { return http.DefaultClient })
	for _, rawURL := range []string{
		"http://127.0.0.1",
		"http://10.0.0.1",
		"http://100.64.0.1",
		"http://169.254.169.254",
		"http://[::1]",
		"http://[::ffff:127.0.0.1]",
		"http://[fe80::1]",
		"http://user:pass@example.com",
	} {
		_, err := svc.TestHTTPReachability(context.Background(), rawURL)
		require.ErrorIs(t, err, ErrInvalidTestURL, "应拒绝: %q", rawURL)
	}
}

func TestDiagnostics_TestHTTPReachability_RejectsDomainResolvingPrivate(t *testing.T) {
	original := lookupHost
	lookupHost = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, nil
	}
	t.Cleanup(func() { lookupHost = original })

	svc := NewDiagnosticsService(nil, nil)
	svc.SetHTTPClientProvider(func() *http.Client { return http.DefaultClient })
	_, err := svc.TestHTTPReachability(context.Background(), "https://download.example.com/server.jar")
	require.ErrorIs(t, err, ErrInvalidTestURL)
}

func TestDiagnostics_TestHTTPReachability_PinsValidatedAddress(t *testing.T) {
	original := lookupHost
	lookupHost = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	t.Cleanup(func() { lookupHost = original })

	svc := NewDiagnosticsService(nil, nil)
	svc.SetHTTPClientProvider(func() *http.Client {
		return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "8.8.8.8", req.URL.Hostname())
			require.Equal(t, "download.example.com", req.Host)
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
		})}
	})
	res, err := svc.TestHTTPReachability(context.Background(), "https://download.example.com/server.jar")
	require.NoError(t, err)
	require.True(t, res.OK)
}

func TestDiagnostics_TestHTTPReachability_DoesNotFollowRedirect(t *testing.T) {
	redirected := false
	svc := NewDiagnosticsService(nil, nil)
	svc.SetHTTPClientProvider(func() *http.Client {
		return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/redirect" {
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": []string{"http://127.0.0.1/private"}},
					Body:       http.NoBody,
					Request:    req,
				}, nil
			}
			redirected = true
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
		})}
	})

	res, err := svc.TestHTTPReachability(context.Background(), "https://8.8.8.8/redirect")
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, http.StatusFound, res.Status)
	require.False(t, redirected)
}

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
