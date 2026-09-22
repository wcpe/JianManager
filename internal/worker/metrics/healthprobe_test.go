package metrics

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FR-459 响应维度探针：TCP connect 成功/失败、HTTP 2xx/非 2xx、未配置端口。

func TestTCPHealthProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port

	assert.NoError(t, TCPHealthProbe("127.0.0.1", port, 0), "监听中应探测可达")
	assert.Error(t, TCPHealthProbe("127.0.0.1", 0, 0), "端口未配置应报错")

	require.NoError(t, ln.Close())
	assert.Error(t, TCPHealthProbe("127.0.0.1", port, 200*time.Millisecond), "监听关闭后应不可达")
}

func TestHTTPHealthProbe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/metrics", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	okPort := ok.Listener.Addr().(*net.TCPAddr).Port
	assert.NoError(t, HTTPHealthProbe("127.0.0.1", okPort), "2xx 应视为响应")

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	badPort := bad.Listener.Addr().(*net.TCPAddr).Port
	assert.Error(t, HTTPHealthProbe("127.0.0.1", badPort), "非 2xx 应视为不响应")

	assert.Error(t, HTTPHealthProbe("127.0.0.1", 0), "端口未配置应报错")
}
