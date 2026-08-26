package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

type githubReleaseSourceConfig struct {
	Repository string `json:"repository"`
}

// GitHubReleaseArtifactProvider 是 ServerProbe 的首个制品来源 provider。
// client 每次调用时取当前出站 client，以遵守运行时代理配置。
type GitHubReleaseArtifactProvider struct {
	client func() *http.Client
}

// NewGitHubReleaseArtifactProvider 创建 GitHub Releases provider。
func NewGitHubReleaseArtifactProvider(client func() *http.Client) *GitHubReleaseArtifactProvider {
	return &GitHubReleaseArtifactProvider{client: client}
}

func (p *GitHubReleaseArtifactProvider) ListVersions(ctx context.Context, source model.ArtifactSource) ([]ArtifactRelease, error) {
	var config githubReleaseSourceConfig
	if err := json.Unmarshal([]byte(source.Config), &config); err != nil || !validGitHubRepository(config.Repository) {
		return nil, fmt.Errorf("%w: GitHub 仓库配置无效", ErrArtifactReleaseInvalid)
	}
	endpoint := "https://api.github.com/repos/" + config.Repository + "/releases?per_page=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
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
		return nil, fmt.Errorf("请求 GitHub Releases 失败: HTTP %d", resp.StatusCode)
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
