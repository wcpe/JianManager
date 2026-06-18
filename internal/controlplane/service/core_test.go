package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func newPaperStub(t *testing.T) *CoreService {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/paper", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"versions":["1.20.6","1.21","1.21.1"]}`)) // 旧→新
	})
	mux.HandleFunc("/paper/versions/1.21.1/builds", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"builds":[
			{"build":195,"downloads":{"application":{"name":"paper-1.21.1-195.jar","sha256":"aaa"}}},
			{"build":196,"downloads":{"application":{"name":"paper-1.21.1-196.jar","sha256":"bbb"}}}
		]}`))
	})
	mux.HandleFunc("/paper/versions/9.9.9/builds", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"builds":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &CoreService{client: srv.Client(), base: srv.URL}
}

func TestCoreListVersions(t *testing.T) {
	s := newPaperStub(t)
	vs, err := s.ListVersions(context.Background(), "Paper")
	require.NoError(t, err)
	require.Equal(t, []string{"1.21.1", "1.21", "1.20.6"}, vs) // 反转为新→旧

	_, err = s.ListVersions(context.Background(), "forge")
	require.Error(t, err) // 暂不支持
}

func TestCoreResolveBuild(t *testing.T) {
	s := newPaperStub(t)

	// 取最新构建 196
	latest, err := s.ResolveBuild(context.Background(), "paper", "1.21.1", 0)
	require.NoError(t, err)
	require.Equal(t, 196, latest.Build)
	require.Equal(t, "paper-1.21.1-196.jar", latest.Filename)
	require.Equal(t, "bbb", latest.SHA256)
	require.Equal(t, s.base+"/paper/versions/1.21.1/builds/196/downloads/paper-1.21.1-196.jar", latest.DownloadURL)

	// 指定构建 195
	pinned, err := s.ResolveBuild(context.Background(), "paper", "1.21.1", 195)
	require.NoError(t, err)
	require.Equal(t, 195, pinned.Build)
	require.Equal(t, "aaa", pinned.SHA256)

	// 不存在的构建
	_, err = s.ResolveBuild(context.Background(), "paper", "1.21.1", 999)
	require.Error(t, err)

	// 无构建的版本
	_, err = s.ResolveBuild(context.Background(), "paper", "9.9.9", 0)
	require.Error(t, err)

	// 缺 mcVersion
	_, err = s.ResolveBuild(context.Background(), "paper", "", 0)
	require.Error(t, err)
}
