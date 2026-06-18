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

// paperAPIBase 是 PaperMC 下载 API 根（FR-034 核心下载源）。
const paperAPIBase = "https://api.papermc.io/v2/projects"

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

// ListVersions 返回指定核心类型可用的 MC 版本（新→旧）。当前支持 paper。
func (s *CoreService) ListVersions(ctx context.Context, coreType string) ([]string, error) {
	if !supportedCore(coreType) {
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
func (s *CoreService) ResolveBuild(ctx context.Context, coreType, mcVersion string, build int) (*CoreInfo, error) {
	if !supportedCore(coreType) {
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

func supportedCore(t string) bool { return project(t) == "paper" }

func project(t string) string { return strings.ToLower(strings.TrimSpace(t)) }
