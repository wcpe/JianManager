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
		// fill v3：versions 为分组对象（组与组内均新→旧），扁平化后为 1.21.1/1.21/1.20.6。
		_, _ = w.Write([]byte(`{"versions":{"1.21":["1.21.1","1.21"],"1.20":["1.20.6"]}}`))
	})
	mux.HandleFunc("/paper/versions/1.21.1/builds", func(w http.ResponseWriter, r *http.Request) {
		// fill v3：直接返回构建数组，下载在 downloads["server:default"]（直给 url + sha256）。
		_, _ = w.Write([]byte(`[
			{"id":195,"downloads":{"server:default":{"name":"paper-1.21.1-195.jar","checksums":{"sha256":"aaa"},"url":"https://cdn.example/paper-1.21.1-195.jar"}}},
			{"id":196,"downloads":{"server:default":{"name":"paper-1.21.1-196.jar","checksums":{"sha256":"bbb"},"url":"https://cdn.example/paper-1.21.1-196.jar"}}}
		]`))
	})
	mux.HandleFunc("/paper/versions/9.9.9/builds", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &CoreService{client: srv.Client(), base: srv.URL, spongeBase: srv.URL, forgeBase: srv.URL}
}

func newSpongeStub(t *testing.T) *CoreService {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/spongevanilla/maven-metadata.xml", func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<metadata><versioning><versions>
  <version>1.20.6-11.0.1-RC1200</version>
  <version>1.21.1-12.0.3-RC2600</version>
  <version>1.21.1-12.0.4-RC2665</version>
</versions></versioning></metadata>`))
	})
	mux.HandleFunc("/spongeforge/maven-metadata.xml", func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<metadata><versioning><versions>
  <version>1.12.2-2838-7.4.8</version>
  <version>1.20.1-47.2.0-11.0.0-RC1000</version>
  <version>1.21.1-52.1.5-12.0.4-RC2665</version>
</versions></versioning></metadata>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &CoreService{client: srv.Client(), base: srv.URL, spongeBase: srv.URL, forgeBase: srv.URL + "/forge"}
}

func TestCoreListVersions(t *testing.T) {
	s := newPaperStub(t)
	vs, err := s.ListVersions(context.Background(), "Paper")
	require.NoError(t, err)
	require.Equal(t, []string{"1.21.1", "1.21", "1.20.6"}, vs) // 反转为新→旧

	_, err = s.ListVersions(context.Background(), "forge")
	require.Error(t, err) // 暂不支持独立 Forge
}

func TestCoreResolveBuild(t *testing.T) {
	s := newPaperStub(t)

	// 取最新构建 196
	latest, err := s.ResolveBuild(context.Background(), "paper", "1.21.1", 0)
	require.NoError(t, err)
	require.Equal(t, 196, latest.Build)
	require.Equal(t, "paper-1.21.1-196.jar", latest.Filename)
	require.Equal(t, "bbb", latest.SHA256)
	require.Equal(t, "https://cdn.example/paper-1.21.1-196.jar", latest.DownloadURL)

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

func TestCoreService_SpongeVanillaResolve(t *testing.T) {
	s := newSpongeStub(t)
	versions, err := s.ListVersions(context.Background(), "spongevanilla")
	require.NoError(t, err)
	require.Equal(t, []string{"1.21.1", "1.20.6"}, versions)

	latest, err := s.ResolveBuild(context.Background(), "spongevanilla", "1.21.1", 0)
	require.NoError(t, err)
	require.Equal(t, "spongevanilla", latest.Type)
	require.Equal(t, 2665, latest.Build)
	require.Equal(t, "spongevanilla-1.21.1-12.0.4-RC2665-universal.jar", latest.Filename)
	require.Equal(t, s.spongeBase+"/spongevanilla/1.21.1-12.0.4-RC2665/spongevanilla-1.21.1-12.0.4-RC2665-universal.jar", latest.DownloadURL)
	require.Nil(t, latest.Runtime)

	pinned, err := s.ResolveBuild(context.Background(), "spongevanilla", "1.21.1", 2600)
	require.NoError(t, err)
	require.Equal(t, "spongevanilla-1.21.1-12.0.3-RC2600-universal.jar", pinned.Filename)

	_, err = s.ResolveBuild(context.Background(), "spongevanilla", "1.21.1", 9999)
	require.Error(t, err)
}

func TestCoreService_SpongeForgeResolve(t *testing.T) {
	s := newSpongeStub(t)
	versions, err := s.ListVersions(context.Background(), "spongeforge")
	require.NoError(t, err)
	require.Equal(t, []string{"1.21.1", "1.20.1", "1.12.2"}, versions)

	latest, err := s.ResolveBuild(context.Background(), "spongeforge", "1.21.1", 0)
	require.NoError(t, err)
	require.Equal(t, "spongeforge", latest.Type)
	require.Equal(t, 2665, latest.Build)
	require.Equal(t, "spongeforge-1.21.1-52.1.5-12.0.4-RC2665-universal.jar", latest.Filename)
	require.NotNil(t, latest.Runtime)
	require.Equal(t, "spongeforge", latest.Runtime.Distribution)
	require.Equal(t, "SpongeForge.jar", latest.Runtime.ModFilename)
	require.Equal(t, "1.21.1-52.1.5", latest.Runtime.ForgeVersion)
	require.Equal(t, "forge-1.21.1-52.1.5-server.jar", latest.Runtime.LaunchJar)
	require.Equal(t, s.forgeBase+"/1.21.1-52.1.5/forge-1.21.1-52.1.5-installer.jar", latest.Runtime.ForgeInstallerURL)

	pinned, err := s.ResolveBuild(context.Background(), "spongeforge", "1.20.1", 1000)
	require.NoError(t, err)
	require.Equal(t, "1.20.1-47.2.0", pinned.Runtime.ForgeVersion)

	_, err = s.ResolveBuild(context.Background(), "spongeforge", "1.12.2", 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "无法从 SpongeForge")
}
