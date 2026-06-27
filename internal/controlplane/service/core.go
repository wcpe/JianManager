package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// paperAPIBase 是 PaperMC 下载 API 根（FR-034/035 核心下载源：paper/velocity/waterfall）。
const paperAPIBase = "https://api.papermc.io/v2/projects"

// bungeeJenkinsURL 是 BungeeCord 最新成功构建的 jar 地址（md-5 Jenkins，FR-035）。
// BungeeCord 不在 PaperMC API 上，仅提供单一 latest jar，无 sha256 校验。
const bungeeJenkinsURL = "https://ci.md-5.net/job/BungeeCord/lastSuccessfulBuild/artifact/bootstrap/target/BungeeCord.jar"

// CoreInfo 描述一个可下载的 MC 服务端核心构建。
type CoreInfo struct {
	Type        string `json:"type"` // paper（后续可扩展 purpur/spigot）
	MCVersion   string `json:"mcVersion"`
	Build       int    `json:"build"`
	Filename    string `json:"filename"`
	DownloadURL string `json:"downloadUrl"`
	SHA256      string `json:"sha256"`
}

// CoreService 解析 MC 服务端核心的可用版本与下载信息（FR-034）。
type CoreService struct {
	client *http.Client
	base   string // 下载 API 根，测试可注入 httptest 地址
}

// NewCoreService 创建核心服务（默认 PaperMC API）。
func NewCoreService() *CoreService {
	return &CoreService{client: &http.Client{Timeout: 20 * time.Second}, base: paperAPIBase}
}

// SetHTTPClient 注入出站 client（经进程级代理，FR-174/ADR-037）：解析核心版本/构建的 API 请求经此 client。
// 由 main 装配；client 为 nil 时忽略（保留默认）。注入的 client 若未设 Timeout，补足 20s 避免无限等待。
func (s *CoreService) SetHTTPClient(c *http.Client) {
	if c == nil {
		return
	}
	if c.Timeout == 0 {
		cc := *c
		cc.Timeout = 20 * time.Second
		c = &cc
	}
	s.client = c
}

// ListVersions 返回指定核心类型可用的版本（新→旧）。
// paper/velocity/waterfall 走 PaperMC API；bungeecord 仅有单一 latest。
func (s *CoreService) ListVersions(ctx context.Context, coreType string) ([]string, error) {
	if project(coreType) == "bungeecord" {
		return []string{"latest"}, nil
	}
	if !paperFamily(coreType) {
		return nil, fmt.Errorf("暂不支持的核心类型: %s", coreType)
	}
	var out struct {
		Versions []string `json:"versions"`
	}
	if err := s.getJSON(ctx, fmt.Sprintf("%s/%s", s.base, project(coreType)), &out); err != nil {
		return nil, err
	}
	// PaperMC 返回旧→新，反转为新→旧便于前端默认选最新。
	for i, j := 0, len(out.Versions)-1; i < j; i, j = i+1, j-1 {
		out.Versions[i], out.Versions[j] = out.Versions[j], out.Versions[i]
	}
	return out.Versions, nil
}

// ResolveBuild 解析指定核心类型/版本的下载信息。build<=0 取最新构建。
// bungeecord 直接返回 md-5 Jenkins 的 latest jar（无版本/构建/校验）。
func (s *CoreService) ResolveBuild(ctx context.Context, coreType, mcVersion string, build int) (*CoreInfo, error) {
	if project(coreType) == "bungeecord" {
		return &CoreInfo{
			Type:        "bungeecord",
			MCVersion:   "latest",
			Build:       0,
			Filename:    "BungeeCord.jar",
			DownloadURL: bungeeJenkinsURL,
			SHA256:      "",
		}, nil
	}
	if !paperFamily(coreType) {
		return nil, fmt.Errorf("暂不支持的核心类型: %s", coreType)
	}
	if strings.TrimSpace(mcVersion) == "" {
		return nil, fmt.Errorf("缺少 mcVersion")
	}
	var out struct {
		Builds []struct {
			Build     int `json:"build"`
			Downloads struct {
				Application struct {
					Name   string `json:"name"`
					SHA256 string `json:"sha256"`
				} `json:"application"`
			} `json:"downloads"`
		} `json:"builds"`
	}
	if err := s.getJSON(ctx, fmt.Sprintf("%s/%s/versions/%s/builds", s.base, project(coreType), mcVersion), &out); err != nil {
		return nil, err
	}
	if len(out.Builds) == 0 {
		return nil, fmt.Errorf("%s %s 无可用构建", coreType, mcVersion)
	}
	sort.Slice(out.Builds, func(i, j int) bool { return out.Builds[i].Build > out.Builds[j].Build })

	chosen := out.Builds[0]
	if build > 0 {
		found := false
		for _, b := range out.Builds {
			if b.Build == build {
				chosen, found = b, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%s %s 无构建 #%d", coreType, mcVersion, build)
		}
	}

	name := chosen.Downloads.Application.Name
	if name == "" {
		return nil, fmt.Errorf("%s %s #%d 缺少下载产物", coreType, mcVersion, chosen.Build)
	}
	return &CoreInfo{
		Type:        project(coreType),
		MCVersion:   mcVersion,
		Build:       chosen.Build,
		Filename:    name,
		DownloadURL: fmt.Sprintf("%s/%s/versions/%s/builds/%d/downloads/%s", s.base, project(coreType), mcVersion, chosen.Build, name),
		SHA256:      chosen.Downloads.Application.SHA256,
	}, nil
}

func (s *CoreService) getJSON(ctx context.Context, url string, v interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求核心仓库失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("核心仓库返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// paperFamily 判断核心类型是否走 PaperMC API（paper 后端 + velocity/waterfall 代理）。
func paperFamily(t string) bool {
	switch project(t) {
	case "paper", "velocity", "waterfall":
		return true
	}
	return false
}

// IsProxyCore 判断核心类型是否为代理核心（FR-035）。
func IsProxyCore(coreType string) bool {
	switch project(coreType) {
	case "velocity", "waterfall", "bungeecord":
		return true
	}
	return false
}

// IsVelocityCore 判断是否为 Velocity（modern 转发，需下发 forwarding secret）。
func IsVelocityCore(coreType string) bool {
	return project(coreType) == "velocity"
}

func project(t string) string { return strings.ToLower(strings.TrimSpace(t)) }
