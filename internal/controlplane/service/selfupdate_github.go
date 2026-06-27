package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ErrUpdateRateLimited GitHub API 限流（403/429，匿名 60 次/时耗尽）。配 github_token 可提升额度。
var ErrUpdateRateLimited = errors.New("GitHub API 限流，请稍后重试或配置 github_token")

// defaultGitHubAPIBase 是 GitHub REST API 默认基址；测试经 SelfUpdateConfig.GitHubAPIBase 覆盖。
const defaultGitHubAPIBase = "https://api.github.com"

// prereleaseTag 是滚动预发布的固定 tag 名（FR-182，见 ADR-039，由 ADR-036 §3 的 nightly 改名而来）。
// 与 FR-173 发布管线 .github/workflows/release.yml 的预发布 tag 强耦合，须保持一致。
const prereleaseTag = "latest"

// githubReqTimeout 是单次 GitHub API / checksums 请求的整体超时。
const githubReqTimeout = 15 * time.Second

// checksumsMaxBytes 限制 checksums.txt 下载体积（防异常超大响应），正常仅几行。
const checksumsMaxBytes = 1 << 20 // 1MiB

// ghRelease 对齐 GitHub Releases API 的 release JSON（仅取所需字段）。
type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Body       string    `json:"body"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

// ghAsset 对齐 release 资产 JSON（仅取所需字段）。
type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// channel 返回归一后的 GitHub 源渠道（空=stable）。
func (s *SelfUpdateService) channel() string {
	c := strings.TrimSpace(strings.ToLower(s.cfg.Channel))
	if c == "" {
		return "stable"
	}
	return c
}

// apiBase 返回 GitHub API 基址（默认 defaultGitHubAPIBase；测试覆盖）。
func (s *SelfUpdateService) apiBase() string {
	if b := strings.TrimSpace(s.cfg.GitHubAPIBase); b != "" {
		return strings.TrimRight(b, "/")
	}
	return defaultGitHubAPIBase
}

// parseAssetName 按 ADR-036 §1 命名 <component>-<os>-<arch>[.exe] 反解资产名。
// component ∈ {control-plane, worker}；windows 目标带 .exe、其余无后缀。
// 不符契约（如 checksums.txt、未知组件、非法后缀）返回 ok=false。
func parseAssetName(name string) (component, goos, goarch string, ok bool) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", "", "", false
	}
	// windows 资产带 .exe；其余无后缀，含其它扩展名（.zip/.txt）视为非契约资产。
	hasExe := strings.HasSuffix(n, ".exe")
	if hasExe {
		n = strings.TrimSuffix(n, ".exe")
	} else if i := strings.LastIndex(n, "."); i >= 0 {
		// 含非 .exe 的扩展名（如 checksums.txt）—— 非二进制契约资产。
		return "", "", "", false
	}

	// 组件前缀优先匹配 control-plane（含连字符），再 worker。
	var rest string
	switch {
	case strings.HasPrefix(n, ComponentControlPlane+"-"):
		component = ComponentControlPlane
		rest = strings.TrimPrefix(n, ComponentControlPlane+"-")
	case strings.HasPrefix(n, ComponentWorker+"-"):
		component = ComponentWorker
		rest = strings.TrimPrefix(n, ComponentWorker+"-")
	default:
		return "", "", "", false
	}

	// rest 形如 <os>-<arch>，os/arch 均不含连字符（runtime.GOOS/GOARCH 取值）。
	parts := strings.Split(rest, "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	goos, goarch = parts[0], parts[1]

	// windows 必须带 .exe；非 windows 不应带 .exe（防错配）。
	if (goos == "windows") != hasExe {
		return "", "", "", false
	}
	return component, goos, goarch, true
}

// parseChecksums 解析 ADR-036 §2 的 checksums.txt：每行 <sha256(小写)>␠␠<filename>。
// 容忍空行与额外空格；返回 filename→sha256（sha 归一为小写）。
func parseChecksums(text string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sha := strings.ToLower(fields[0])
		// 文件名取最后一段（sha256sum 默认 "<sha>  <file>"，file 不含空格）。
		fname := fields[len(fields)-1]
		out[fname] = sha
	}
	return out
}

// fetchGitHubRelease 据 channel 读 GitHub Releases API，组装等价归一 *Feed（FR-175，见 ADR-036 §7）。
// stable→/releases/latest（天然排除 prerelease/draft）；prerelease→/releases/tags/nightly。
// sha256 取自 release 的 checksums.txt 资产；无 checksums.txt 报错（不允许裸下载）。
func (s *SelfUpdateService) fetchGitHubRelease(ctx context.Context) (*Feed, error) {
	repo := strings.TrimSpace(s.cfg.GitHubRepo)
	if repo == "" {
		return nil, ErrUpdateNotConfigured
	}
	if strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return nil, fmt.Errorf("github_repo 须为 owner/repo 形态: %q", repo)
	}

	var endpoint string
	if s.channel() == "prerelease" {
		// 滚动预发布固定 tag 由 nightly 改名为 latest（FR-182，见 ADR-039，取代 ADR-036 §7 命名）。
		// 注意与 stable 的 /releases/latest 不同：此处是名为 latest 的 tag（/releases/tags/latest）。
		endpoint = fmt.Sprintf("%s/repos/%s/releases/tags/%s", s.apiBase(), repo, prereleaseTag)
	} else {
		endpoint = fmt.Sprintf("%s/repos/%s/releases/latest", s.apiBase(), repo)
	}

	rel, err := s.getGitHubRelease(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	// 找 checksums.txt 资产并下载解析（sha256 唯一完整性根，缺则报错不裸下载）。
	var checksumsURL string
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			checksumsURL = a.BrowserDownloadURL
			break
		}
	}
	if checksumsURL == "" {
		return nil, fmt.Errorf("release %s 缺 checksums.txt，无可信校验源，拒绝升级", rel.TagName)
	}
	sums, err := s.fetchChecksums(ctx, checksumsURL)
	if err != nil {
		return nil, err
	}

	feed := &Feed{Version: rel.TagName, Notes: rel.Body}
	for _, a := range rel.Assets {
		component, goos, goarch, ok := parseAssetName(a.Name)
		if !ok {
			continue // 非契约资产（含 checksums.txt 自身）。
		}
		sha := sums[a.Name]
		if sha == "" {
			// 缺 sha256 的二进制不可信，跳过并记日志（不静默放行无校验）。
			slog.Warn("GitHub release 资产无对应 checksums 条目，跳过", "asset", a.Name, "release", rel.TagName)
			continue
		}
		feed.Artifacts = append(feed.Artifacts, FeedArtifact{
			Component: component,
			OS:        goos,
			Arch:      goarch,
			URL:       a.BrowserDownloadURL,
			SHA256:    sha,
		})
	}
	return feed, nil
}

// getGitHubRelease 发起一次 GitHub Releases API 请求并解析 release JSON，附状态码映射。
func (s *SelfUpdateService) getGitHubRelease(ctx context.Context, endpoint string) (*ghRelease, error) {
	reqCtx, cancel := context.WithTimeout(ctx, githubReqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造 GitHub API 请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := strings.TrimSpace(s.cfg.GitHubToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := s.outboundClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub API 失败: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		// 继续解析。
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrUpdateNoArtifact
	case isRateLimited(resp):
		return nil, ErrUpdateRateLimited
	default:
		return nil, fmt.Errorf("GitHub API 返回 HTTP %d", resp.StatusCode)
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析 GitHub release 失败: %w", err)
	}
	return &rel, nil
}

// isRateLimited 判断响应是否为 GitHub 限流：429，或 403 且 X-RateLimit-Remaining 为 0。
func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.StatusCode == http.StatusForbidden {
		// GitHub 限流以 403 + X-RateLimit-Remaining:0 表达；无该头的 403（如鉴权失败）不视为限流。
		return resp.Header.Get("X-RateLimit-Remaining") == "0"
	}
	return false
}

// fetchChecksums 经出站代理下载并解析 checksums.txt 资产（限体积）。
func (s *SelfUpdateService) fetchChecksums(ctx context.Context, url string) (map[string]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, githubReqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造 checksums 请求失败: %w", err)
	}
	if tok := strings.TrimSpace(s.cfg.GitHubToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := s.outboundClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载 checksums.txt 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 checksums.txt 失败: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, checksumsMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("读取 checksums.txt 失败: %w", err)
	}
	sums := parseChecksums(string(data))
	if len(sums) == 0 {
		return nil, errors.New("checksums.txt 为空或无法解析")
	}
	return sums, nil
}
