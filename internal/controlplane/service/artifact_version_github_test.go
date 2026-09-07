package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func githubSourceForTest() model.ArtifactSource {
	return model.ArtifactSource{
		Provider: model.ArtifactProviderGitHubRelease,
		Name:     "官方 GitHub Releases",
		Config:   `{"repository":"wcpe/ServerProbe"}`,
		Enabled:  true,
	}
}

// newGitHubProviderForTest 构造指向 httptest 的 provider（生产端点为 api.github.com）。
func newGitHubProviderForTest(baseURL, token string) *GitHubReleaseArtifactProvider {
	p := NewGitHubReleaseArtifactProvider(func() *http.Client { return http.DefaultClient }, token)
	p.apiBase = baseURL
	return p
}

// TestGitHubReleaseArtifactProvider_SendsBearerToken 配置了令牌时请求须带 Authorization，
// 否则 GitHub 按匿名额度（60 次/时）限流。
func TestGitHubReleaseArtifactProvider_SendsBearerToken(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	_, err := newGitHubProviderForTest(server.URL, "ghp_token").ListVersions(context.Background(), githubSourceForTest())
	require.NoError(t, err)
	require.Equal(t, "Bearer ghp_token", got)
}

// TestGitHubReleaseArtifactProvider_AnonymousWithoutToken 未配令牌时保持匿名（不带 Authorization）。
func TestGitHubReleaseArtifactProvider_AnonymousWithoutToken(t *testing.T) {
	var got string
	var seen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	_, err := newGitHubProviderForTest(server.URL, "").ListVersions(context.Background(), githubSourceForTest())
	require.NoError(t, err)
	require.True(t, seen)
	require.Empty(t, got)
}

// TestGitHubReleaseArtifactProvider_RateLimited403 复现线上故障：匿名调用耗尽 60 次/时额度后
// GitHub 返回 403 + X-RateLimit-Remaining:0。错误必须说清「限流」与配额重置时间，而非裸的 HTTP 403。
func TestGitHubReleaseArtifactProvider_RateLimited403(t *testing.T) {
	resetAt := time.Now().Add(30 * time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 1.2.3.4."}`))
	}))
	defer server.Close()

	_, err := newGitHubProviderForTest(server.URL, "").ListVersions(context.Background(), githubSourceForTest())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUpdateRateLimited, "403+Remaining:0 必须归一到限流错误")
	require.Contains(t, err.Error(), "限流")
	require.Contains(t, err.Error(), resetAt.Format("15:04:05"), "应告知配额重置时间，而非只报 HTTP 403")
}

// TestGitHubReleaseArtifactProvider_RateLimited429 显式 429 同样归一到限流错误。
func TestGitHubReleaseArtifactProvider_RateLimited429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit."}`))
	}))
	defer server.Close()

	_, err := newGitHubProviderForTest(server.URL, "").ListVersions(context.Background(), githubSourceForTest())
	require.ErrorIs(t, err, ErrUpdateRateLimited)
}

// TestGitHubReleaseArtifactProvider_ForbiddenCarriesAPIMessage 非限流的 403（封禁、SSO 限制等）
// 必须带上 GitHub 返回的 message，否则管理员只看到「HTTP 403」无从下手。
func TestGitHubReleaseArtifactProvider_ForbiddenCarriesAPIMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer server.Close()

	_, err := newGitHubProviderForTest(server.URL, "").ListVersions(context.Background(), githubSourceForTest())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUpdateRateLimited, "无限流头不算限流")
	require.Contains(t, err.Error(), "Resource not accessible by integration")
}

// TestGitHubReleaseArtifactProvider_ParsesReleases 正常响应解析：去 v 前缀、跳过 draft/prerelease。
func TestGitHubReleaseArtifactProvider_ParsesReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.3.0","draft":false,"prerelease":false,"assets":[{"name":"ServerProbe-0.3.0.jar","browser_download_url":"https://example.test/a.jar","digest":"sha256:4e2ae065aa8d979a8c77f45cccdc2a05f65f783bb1a71348b0efcd08e41a7ba9"}]},
			{"tag_name":"v0.4.0","draft":true,"prerelease":false,"assets":[{"name":"ServerProbe-0.4.0.jar","browser_download_url":"https://example.test/b.jar","digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}]}
		]`))
	}))
	defer server.Close()

	releases, err := newGitHubProviderForTest(server.URL, "").ListVersions(context.Background(), githubSourceForTest())
	require.NoError(t, err)
	require.Len(t, releases, 1, "draft 必须跳过")
	require.Equal(t, "0.3.0", releases[0].Version)
	require.Equal(t, "v0.3.0", releases[0].ReleaseRef)
	// provider 原样透出 GitHub 的 digest 字段，去 sha256: 前缀由 SyncSource 入库前归一。
	require.Equal(t, "sha256:4e2ae065aa8d979a8c77f45cccdc2a05f65f783bb1a71348b0efcd08e41a7ba9", releases[0].SHA256)
}

// TestGitHubReleaseArtifactProvider_TokenProviderPriority 运行时令牌读取器（设置面板
// github.token，FR-063/FR-409）优先于构造基线；读取器为空回退基线；`${ENV_VAR}` 引用经
// 环境变量展开；引用了未配置的环境变量视同未配置（不发引用文本）。
func TestGitHubReleaseArtifactProvider_TokenProviderPriority(t *testing.T) {
	cases := []struct {
		name       string
		baseline   string
		dynamic    func() string
		envVar     string
		envValue   string
		wantHeader string
	}{
		{"动态优先于基线", "ghp_base", func() string { return "ghp_dynamic" }, "", "", "Bearer ghp_dynamic"},
		{"动态为空回退基线", "ghp_base", func() string { return "" }, "", "", "Bearer ghp_base"},
		{"无动态无基线则匿名", "", func() string { return "" }, "", "", ""},
		{"环境变量引用展开", "", func() string { return "${JM_TEST_GH_TOKEN}" }, "JM_TEST_GH_TOKEN", "ghp_from_env", "Bearer ghp_from_env"},
		{"引用未配置环境变量视同未配置", "ghp_base", func() string { return "${JM_TEST_GH_MISSING}" }, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envVar != "" {
				t.Setenv(tc.envVar, tc.envValue)
			}
			var got string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`[]`))
			}))
			defer server.Close()

			p := newGitHubProviderForTest(server.URL, tc.baseline)
			if tc.dynamic != nil {
				p.SetTokenProvider(tc.dynamic)
			}
			_, err := p.ListVersions(context.Background(), githubSourceForTest())
			require.NoError(t, err)
			require.Equal(t, tc.wantHeader, got)
		})
	}
}
