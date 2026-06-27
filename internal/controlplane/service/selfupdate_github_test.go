package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
)

// assetName 按 ADR-036 §1 契约为本测试运行平台拼资产名（windows 带 .exe）。
// 测试固件据此构造与解析器同契约的资产名，使测试跨平台可移植。
func assetName(component string) string {
	n := fmt.Sprintf("%s-%s-%s", component, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

func TestParseAssetName(t *testing.T) {
	cases := []struct {
		name                       string
		wantComp, wantOS, wantArch string
		wantOK                     bool
	}{
		{"control-plane-linux-amd64", "control-plane", "linux", "amd64", true},
		{"worker-windows-amd64.exe", "worker", "windows", "amd64", true},
		{"control-plane-windows-amd64.exe", "control-plane", "windows", "amd64", true},
		{"worker-linux-amd64", "worker", "linux", "amd64", true},
		// 非法 / 非本契约资产名。
		{"checksums.txt", "", "", "", false},
		{"control-plane.exe", "", "", "", false},
		{"random-asset", "", "", "", false},
		{"server-linux-amd64", "", "", "", false}, // 非 control-plane/worker 组件
		{"", "", "", "", false},
		{"worker-linux-amd64.zip", "", "", "", false}, // 非法后缀
	}
	for _, c := range cases {
		comp, goos, goarch, ok := parseAssetName(c.name)
		if ok != c.wantOK {
			t.Fatalf("parseAssetName(%q) ok=%v 期望 %v", c.name, ok, c.wantOK)
		}
		if !ok {
			continue
		}
		if comp != c.wantComp || goos != c.wantOS || goarch != c.wantArch {
			t.Fatalf("parseAssetName(%q)=(%q,%q,%q) 期望 (%q,%q,%q)",
				c.name, comp, goos, goarch, c.wantComp, c.wantOS, c.wantArch)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	text := "  \n" + // 空行容忍
		"abc123  control-plane-linux-amd64\n" +
		"DEF456  worker-windows-amd64.exe\n" + // 大写 sha 归一为小写
		"\n" +
		"789aaa   worker-linux-amd64\n" // 额外空格容忍
	m := parseChecksums(text)
	require.Equal(t, "abc123", m["control-plane-linux-amd64"])
	require.Equal(t, "def456", m["worker-windows-amd64.exe"])
	require.Equal(t, "789aaa", m["worker-linux-amd64"])
	require.Len(t, m, 3)
}

// ghTestServer 起一个模拟 GitHub Releases API 的 httptest 服务，
// 覆盖 /releases/latest（stable）与 /releases/tags/latest（prerelease 滚动，FR-182）两端点与 checksums.txt 资产下载。
func ghTestServer(t *testing.T, latestBody, prereleaseBody string, withChecksums bool, checksumsBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	// checksums.txt 资产端点。
	mux.HandleFunc("/assets/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksumsBody))
	})

	// 二进制资产端点（仅占位，解析阶段不下载二进制）。
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("binary"))
	})

	cpName := assetName(ComponentControlPlane)
	wkName := assetName(ComponentWorker)
	releaseJSON := func(body string) string {
		assets := fmt.Sprintf(`
			{"name":"%s","browser_download_url":"%s/assets/%s"},
			{"name":"%s","browser_download_url":"%s/assets/%s"}`,
			cpName, srv.URL, cpName, wkName, srv.URL, wkName)
		if withChecksums {
			assets += fmt.Sprintf(`,
			{"name":"checksums.txt","browser_download_url":"%s/assets/checksums.txt"}`, srv.URL)
		}
		return fmt.Sprintf(`{"tag_name":"%s","body":"%s","prerelease":%v,"assets":[%s]}`,
			body, "release notes", strings.Contains(body, "dev"), assets)
	}

	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		if latestBody == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(releaseJSON(latestBody)))
	})
	// prerelease 渠道：滚动预发布固定 tag 名为 latest（FR-182，由 nightly 改名）。
	mux.HandleFunc("/repos/owner/repo/releases/tags/latest", func(w http.ResponseWriter, _ *http.Request) {
		if prereleaseBody == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(releaseJSON(prereleaseBody)))
	})
	t.Cleanup(srv.Close)
	return srv
}

// newGHService 构造一个把 apiBase 指向 httptest 的 GitHub 源服务。
func newGHService(t *testing.T, apiBase, channel string) *SelfUpdateService {
	t.Helper()
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(),
		SelfUpdateConfig{GitHubRepo: "owner/repo", Channel: channel, GitHubAPIBase: apiBase, AllowInsecure: true}, nil)
	return svc
}

func TestFetchGitHubRelease_Stable(t *testing.T) {
	checksums := fmt.Sprintf("aaa  %s\nbbb  %s\n", assetName(ComponentControlPlane), assetName(ComponentWorker))
	srv := ghTestServer(t, "v1.2.3", "", true, checksums)
	svc := newGHService(t, srv.URL, "stable")

	feed, err := svc.fetchGitHubRelease(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v1.2.3", feed.Version)
	require.Equal(t, "release notes", feed.Notes)

	cp, ok := SelectArtifact(feed, ComponentControlPlane, runtime.GOOS, runtime.GOARCH)
	require.True(t, ok, "应解析出 control-plane 本平台制品")
	require.Equal(t, "aaa", cp.SHA256, "sha256 应取自 checksums.txt")
	require.Contains(t, cp.URL, "control-plane")

	wk, ok := SelectArtifact(feed, ComponentWorker, runtime.GOOS, runtime.GOARCH)
	require.True(t, ok, "应解析出 worker 本平台制品")
	require.Equal(t, "bbb", wk.SHA256)
}

func TestFetchGitHubRelease_PrereleaseChannel(t *testing.T) {
	checksums := fmt.Sprintf("ccc  %s\nddd  %s\n", assetName(ComponentControlPlane), assetName(ComponentWorker))
	// /releases/latest 返回 v1.0.0；/releases/tags/latest 返回滚动预发布。channel=prerelease 应取后者（FR-182）。
	srv := ghTestServer(t, "v1.0.0", "0.0.0-dev+abc1234", true, checksums)
	svc := newGHService(t, srv.URL, "prerelease")

	feed, err := svc.fetchGitHubRelease(context.Background())
	require.NoError(t, err)
	require.Equal(t, "0.0.0-dev+abc1234", feed.Version, "prerelease 渠道应取滚动预发布 latest tag")
}

func TestFetchGitHubRelease_NoRelease404(t *testing.T) {
	srv := ghTestServer(t, "", "", true, "")
	svc := newGHService(t, srv.URL, "stable")
	_, err := svc.fetchGitHubRelease(context.Background())
	require.ErrorIs(t, err, ErrUpdateNoArtifact, "仓库/渠道无 release 应映射 ErrUpdateNoArtifact")
}

func TestFetchGitHubRelease_NoChecksums(t *testing.T) {
	srv := ghTestServer(t, "v1.0.0", "", false, "")
	svc := newGHService(t, srv.URL, "stable")
	_, err := svc.fetchGitHubRelease(context.Background())
	require.Error(t, err, "无 checksums.txt 应报错，不允许裸下载")
	require.NotErrorIs(t, err, ErrUpdateNoArtifact)
}

func TestFetchGitHubRelease_RateLimited(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	svc := newGHService(t, srv.URL, "stable")
	_, err := svc.fetchGitHubRelease(context.Background())
	require.ErrorIs(t, err, ErrUpdateRateLimited, "403 + X-RateLimit-Remaining:0 应映射 ErrUpdateRateLimited")
}

func TestFetchGitHubRelease_RateLimited429(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	svc := newGHService(t, srv.URL, "stable")
	_, err := svc.fetchGitHubRelease(context.Background())
	require.ErrorIs(t, err, ErrUpdateRateLimited, "429 应映射 ErrUpdateRateLimited")
}

func TestFetchGitHubRelease_NoPlatformArtifact(t *testing.T) {
	// release 只含一个不匹配本平台的资产 + checksums，本平台无匹配制品。
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/assets/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("eee  control-plane-plan9-mips\n"))
	})
	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(
			`{"tag_name":"v9","body":"n","prerelease":false,"assets":[`+
				`{"name":"control-plane-plan9-mips","browser_download_url":"%s/x"},`+
				`{"name":"checksums.txt","browser_download_url":"%s/assets/checksums.txt"}]}`,
			srv.URL, srv.URL)))
	})
	svc := newGHService(t, srv.URL, "stable")
	feed, err := svc.fetchGitHubRelease(context.Background())
	require.NoError(t, err, "解析本身成功，只是本平台无匹配")
	_, ok := SelectArtifact(feed, ComponentControlPlane, runtime.GOOS, runtime.GOARCH)
	require.False(t, ok, "本平台不应有匹配制品（仅 plan9/mips）")
}

func TestFetchGitHubRelease_BadRepo(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(),
		SelfUpdateConfig{GitHubRepo: "no-slash", Channel: "stable", AllowInsecure: true}, nil)
	_, err := svc.fetchGitHubRelease(context.Background())
	require.Error(t, err, "非 owner/repo 形态仓库应报错")
}

// === 分发 / 回退路径 ===

func TestConfigured_GitHubRepo(t *testing.T) {
	// 仅配 github_repo（无 feed_url）也应视为已配置。
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(),
		SelfUpdateConfig{GitHubRepo: "owner/repo"}, nil)
	require.True(t, svc.Configured(), "github_repo 非空应为已配置")

	// 都空则未配置。
	svc2 := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	require.False(t, svc2.Configured())
}

func TestResolveRelease_FallbackToFeed(t *testing.T) {
	// github_repo 空 + feed_url 非空 → 走原 feed 路径（FR-081 行为不破）。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"5.0.0","notes":"feed","artifacts":[{"component":"worker","os":"linux","arch":"amd64","url":"u","sha256":"s"}]}`))
	}))
	defer ts.Close()
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(),
		SelfUpdateConfig{FeedURL: ts.URL, AllowInsecure: true}, nil)
	feed, err := svc.resolveRelease(context.Background())
	require.NoError(t, err)
	require.Equal(t, "5.0.0", feed.Version, "应走 feed 回退路径")
}

func TestResolveRelease_PrefersGitHubOverFeed(t *testing.T) {
	// 同时配 github_repo 与 feed_url → 优先 GitHub 源。
	checksums := fmt.Sprintf("aaa  %s\n", assetName(ComponentControlPlane))
	gh := ghTestServer(t, "v7.7.7", "", true, checksums)
	feedTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"1.0.0","notes":"feed"}`))
	}))
	defer feedTS.Close()
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(),
		SelfUpdateConfig{GitHubRepo: "owner/repo", Channel: "stable", GitHubAPIBase: gh.URL, FeedURL: feedTS.URL, AllowInsecure: true}, nil)
	feed, err := svc.resolveRelease(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v7.7.7", feed.Version, "github_repo 非空应优先 GitHub 源")
}

func TestResolveRelease_NotConfigured(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	_, err := svc.resolveRelease(context.Background())
	require.ErrorIs(t, err, ErrUpdateNotConfigured)
}

func TestResolveRelease_FeedFetcherStubPrecedence(t *testing.T) {
	// feedFetcher 测试桩优先于一切（现有测试依赖此）。
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(),
		SelfUpdateConfig{GitHubRepo: "owner/repo"}, nil)
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{Version: "stub"}, nil
	}
	feed, err := svc.resolveRelease(context.Background())
	require.NoError(t, err)
	require.Equal(t, "stub", feed.Version, "feedFetcher 桩应优先")
}

func TestCheckUpdate_Source(t *testing.T) {
	// GitHub 源 → source 标 github:owner/repo@channel。
	checksums := fmt.Sprintf("aaa  %s\n", assetName(ComponentControlPlane))
	gh := ghTestServer(t, "v2.0.0", "", true, checksums)
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(),
		SelfUpdateConfig{GitHubRepo: "owner/repo", Channel: "stable", GitHubAPIBase: gh.URL, AllowInsecure: true}, nil)
	res, err := svc.CheckUpdate(context.Background())
	require.NoError(t, err)
	require.Equal(t, "github:owner/repo@stable", res.Source)
	require.Equal(t, "v2.0.0", res.LatestVersion)

	// feed 源 → source 标 feed。
	feedTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"3.0.0","notes":"f"}`))
	}))
	defer feedTS.Close()
	svc2 := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(),
		SelfUpdateConfig{FeedURL: feedTS.URL, AllowInsecure: true}, nil)
	res2, err := svc2.CheckUpdate(context.Background())
	require.NoError(t, err)
	require.Equal(t, "feed", res2.Source)
}

func TestCheckUpdate_RateLimitedPropagates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	svc := newGHService(t, srv.URL, "stable")
	_, err := svc.CheckUpdate(context.Background())
	require.ErrorIs(t, err, ErrUpdateRateLimited, "限流应从 CheckUpdate 透出")
	require.True(t, errors.Is(err, ErrUpdateRateLimited))
}
