package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

type githubReleaseSourceConfig struct {
	Repository string `json:"repository"`
}

// githubAPIErrorBodyLimit 限制读取错误响应体的体积，避免异常响应撑爆内存。
const githubAPIErrorBodyLimit = 8 << 10

// GitHubReleaseArtifactProvider 是 ServerProbe 的首个制品来源 provider。
// client 每次调用时取当前出站 client，以遵守运行时代理配置。
type GitHubReleaseArtifactProvider struct {
	client func() *http.Client
	// token 基线令牌（yml/env 的 update.github_token）；tokenFunc 非空且返回非空值时优先。
	token string
	// tokenFunc 运行时令牌读取器（main 注入 settings 生效值 github.token），
	// 使设置面板改令牌即时生效：每次 GitHub API 请求发出发前读取当前值。
	tokenFunc func() string
	// apiBase GitHub REST API 基址；生产恒为 defaultGitHubAPIBase，测试指向 httptest。
	apiBase string
}

// SetTokenProvider 注入运行时令牌读取器（FR-063 设置面板 github.token），
// 优先于构造时的基线令牌；读不到（空）时回退基线，行为与改造前一致。
func (p *GitHubReleaseArtifactProvider) SetTokenProvider(fn func() string) {
	if p == nil {
		return
	}
	p.tokenFunc = fn
}

// effectiveToken 解析当前生效令牌：运行时读取器 > 基线；`${ENV_VAR}` 引用经环境变量展开。
func (p *GitHubReleaseArtifactProvider) effectiveToken() string {
	value := ""
	if p != nil && p.tokenFunc != nil {
		value = strings.TrimSpace(p.tokenFunc())
	}
	if value == "" && p != nil {
		value = strings.TrimSpace(p.token)
	}
	return resolveGitHubTokenValue(value)
}

// resolveGitHubTokenValue 把令牌配置值解析为实际令牌：`${ENV_VAR}` 引用经环境变量展开
//（与 SMTP 密码同约定，凭据可不入库），其余按字面令牌。引用了未配置的环境变量视同未配置
//（返回空串），不把引用文本当令牌发出去。
func resolveGitHubTokenValue(value string) string {
	if value == "" {
		return ""
	}
	if m := environmentReferencePattern.FindStringSubmatch(value); m != nil {
		resolved, ok := os.LookupEnv(m[1])
		if !ok {
			return ""
		}
		return strings.TrimSpace(resolved)
	}
	return value
}

// NewGitHubReleaseArtifactProvider 创建 GitHub Releases provider。
// token 为空表示匿名调用（受 60 次/时额度约束，耗尽即 403）。
func NewGitHubReleaseArtifactProvider(client func() *http.Client, token string) *GitHubReleaseArtifactProvider {
	return &GitHubReleaseArtifactProvider{
		client:  client,
		token:   strings.TrimSpace(token),
		apiBase: defaultGitHubAPIBase,
	}
}

func (p *GitHubReleaseArtifactProvider) ListVersions(ctx context.Context, source model.ArtifactSource) ([]ArtifactRelease, error) {
	var config githubReleaseSourceConfig
	if err := json.Unmarshal([]byte(source.Config), &config); err != nil || !validGitHubRepository(config.Repository) {
		return nil, fmt.Errorf("%w: GitHub 仓库配置无效", ErrArtifactReleaseInvalid)
	}
	base := defaultGitHubAPIBase
	if p != nil && p.apiBase != "" {
		base = p.apiBase
	}
	endpoint := strings.TrimRight(base, "/") + "/repos/" + config.Repository + "/releases?per_page=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := p.effectiveToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := http.DefaultClient
	if p != nil && p.client != nil {
		if configured := p.client(); configured != nil {
			client = configured
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub Releases 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, githubAPIError("请求 GitHub Releases 失败", resp)
	}
	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("解析 GitHub Releases 响应失败: %w", err)
	}
	result := make([]ArtifactRelease, 0, len(releases))
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		asset, ok := serverProbeReleaseAsset(release.Assets)
		if !ok {
			continue
		}
		version := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
		entry := ArtifactRelease{
			Version:    version,
			ReleaseRef: release.TagName,
			AssetName:  asset.Name,
			URL:        asset.BrowserDownloadURL,
			SHA256:     asset.Digest,
		}
		if err := validateArtifactRelease(entry); err != nil {
			return nil, fmt.Errorf("%w: release %s", err, release.TagName)
		}
		result = append(result, entry)
	}
	return result, nil
}

// githubAPIError 把 GitHub API 的错误响应转成可诊断错误：
//   - 限流（429，或 403 且 X-RateLimit-Remaining:0）归一到 ErrUpdateRateLimited 并附配额重置时间，
//     让管理员看出是限额问题、何时能恢复，而不是对着裸的「HTTP 403」无从下手；
//   - 其余状态附带 GitHub 返回的 message（如封禁、SSO 限制）。
//
// 只解析响应体的 message 字段并截断，不落任何敏感信息（令牌永不出现在错误里）。
func githubAPIError(prefix string, resp *http.Response) error {
	if isRateLimited(resp) {
		if hint := rateLimitResetHint(resp); hint != "" {
			return fmt.Errorf("%s: %w（%s）", prefix, ErrUpdateRateLimited, hint)
		}
		return fmt.Errorf("%s: %w", prefix, ErrUpdateRateLimited)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, githubAPIErrorBodyLimit))
	if msg := githubErrorMessage(body); msg != "" {
		if len(msg) > 256 {
			msg = msg[:256]
		}
		return fmt.Errorf("%s: HTTP %d %s", prefix, resp.StatusCode, msg)
	}
	return fmt.Errorf("%s: HTTP %d", prefix, resp.StatusCode)
}

// githubErrorMessage 取 GitHub 错误响应的 message 字段（取不到返回空串）。
func githubErrorMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Message)
}

// rateLimitResetHint 把 X-RateLimit-Reset（Unix 秒）转成本地时间提示；缺失或不可解析返回空串。
func rateLimitResetHint(resp *http.Response) string {
	raw := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset"))
	if raw == "" {
		return ""
	}
	sec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return ""
	}
	return "GitHub 配额将于 " + time.Unix(sec, 0).Local().Format("2006-01-02 15:04:05") + " 重置"
}

type githubRelease struct {
	TagName    string               `json:"tag_name"`
	Draft      bool                 `json:"draft"`
	Prerelease bool                 `json:"prerelease"`
	Assets     []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

func serverProbeReleaseAsset(assets []githubReleaseAsset) (githubReleaseAsset, bool) {
	var selected githubReleaseAsset
	for _, asset := range assets {
		name := strings.TrimSpace(asset.Name)
		if !strings.HasPrefix(name, "ServerProbe-") || !strings.HasSuffix(strings.ToLower(name), ".jar") {
			continue
		}
		if selected.Name != "" {
			return githubReleaseAsset{}, false
		}
		selected = asset
	}
	return selected, selected.Name != ""
}

func validGitHubRepository(repository string) bool {
	parts := strings.Split(strings.TrimSpace(repository), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	if strings.ContainsAny(repository, "\\?&#") || path.Clean(repository) != repository {
		return false
	}
	_, err := url.Parse("https://github.com/" + repository)
	return err == nil
}
