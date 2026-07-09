package service

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// paperAPIBase 是 PaperMC 下载 API 根（FR-034/035 核心下载源：paper/velocity/waterfall）。
// 用 fill v3——旧 v2（api.papermc.io/v2）已 sunset（返回 410），真机建服因此全部失效。
// v3：versions 为 {minor:[patch...]} 分组对象；builds 为数组，下载在 downloads["server:default"]（直给 CDN url + sha256）。
const paperAPIBase = "https://fill.papermc.io/v3/projects"

// spongeMavenBase 是 Sponge 官方 Maven release 仓库根（FR-046：SpongeVanilla/SpongeForge）。
const spongeMavenBase = "https://repo.spongepowered.org/repository/maven-releases/org/spongepowered"

// forgeMavenBase 是 MinecraftForge 官方 Maven 仓库根（FR-046：SpongeForge 依赖 Forge installer）。
const forgeMavenBase = "https://maven.minecraftforge.net/net/minecraftforge/forge"

// coreHTTPUserAgent 避免部分官方下载页/仓库拒绝 Go 默认 User-Agent。
const coreHTTPUserAgent = "JianManager/FR-046 (+https://github.com/wcpe/JianManager)"

// bungeeJenkinsURL 是 BungeeCord 最新成功构建的 jar 地址（md-5 Jenkins，FR-035）。
// BungeeCord 不在 PaperMC API 上，仅提供单一 latest jar，无 sha256 校验。
const bungeeJenkinsURL = "https://ci.md-5.net/job/BungeeCord/lastSuccessfulBuild/artifact/bootstrap/target/BungeeCord.jar"

// CoreRuntimeInfo 描述服务端核心以外的运行时安装信息。
// SpongeForge 是 Forge mod，需要先安装 Forge 服务端，再把 SpongeForge jar 放入 mods/。
type CoreRuntimeInfo struct {
	Distribution      string `json:"distribution"`
	ModFilename       string `json:"modFilename,omitempty"`
	ForgeInstallerURL string `json:"forgeInstallerUrl,omitempty"`
	ForgeVersion      string `json:"forgeVersion,omitempty"`
	LaunchJar         string `json:"launchJar,omitempty"`
}

// CoreInfo 描述一个可下载的 MC 服务端核心构建。
type CoreInfo struct {
	Type        string           `json:"type"` // paper / spongevanilla / spongeforge / velocity / waterfall / bungeecord
	MCVersion   string           `json:"mcVersion"`
	Build       int              `json:"build"`
	Filename    string           `json:"filename"`
	DownloadURL string           `json:"downloadUrl"`
	SHA256      string           `json:"sha256"`
	Runtime     *CoreRuntimeInfo `json:"runtime,omitempty"`
}

// CoreService 解析 MC 服务端核心的可用版本与下载信息（FR-034/046）。
type CoreService struct {
	client *http.Client
	// httpProvider 运行时出站持有者（FR-185/ADR-043）：非 nil 时每次请求取当前 client，
	// 使全局代理改动即时生效（补足 20s 超时）。优先于固定 client。
	httpProvider func() *http.Client
	base         string // PaperMC 下载 API 根，测试可注入 httptest 地址
	spongeBase   string // Sponge Maven 根，测试可注入 httptest 地址
	forgeBase    string // Forge Maven 根，测试可注入 httptest 地址
}

// NewCoreService 创建核心服务（默认 PaperMC/Sponge/Forge 官方源）。
func NewCoreService() *CoreService {
	return &CoreService{
		client:     &http.Client{Timeout: 20 * time.Second},
		base:       paperAPIBase,
		spongeBase: spongeMavenBase,
		forgeBase:  forgeMavenBase,
	}
}

// SetHTTPClient 注入出站 client（经进程级代理，FR-174/ADR-037）：解析核心版本/构建的 API 请求经此 client。
// 由 main 装配；client 为 nil 时忽略（保留默认）。注入的 client 若未设 Timeout，补足 20s 避免无限等待。
func (s *CoreService) SetHTTPClient(c *http.Client) {
	if c == nil {
		return
	}
	s.client = withDefaultTimeout(c)
}

// SetHTTPClientProvider 注入运行时出站持有者（FR-185/ADR-043）：每次请求取当前 client，
// 使全局代理改动即时生效。优先于 SetHTTPClient 注入的固定 client；补足 20s 超时避免无限等待。
func (s *CoreService) SetHTTPClientProvider(p func() *http.Client) {
	s.httpProvider = p
}

// httpClient 返回出站 client：优先运行时持有者（取当前），否则固定 client。
func (s *CoreService) httpClient() *http.Client {
	if s.httpProvider != nil {
		if c := s.httpProvider(); c != nil {
			return withDefaultTimeout(c)
		}
	}
	return s.client
}

// withDefaultTimeout 在 client 未设 Timeout 时补足 20s（避免无限等待）；不改原 client。
func withDefaultTimeout(c *http.Client) *http.Client {
	if c == nil {
		return &http.Client{Timeout: 20 * time.Second}
	}
	if c.Timeout == 0 {
		cc := *c
		cc.Timeout = 20 * time.Second
		return &cc
	}
	return c
}

// ListVersions 返回指定核心类型可用的版本（新→旧）。
// paper/velocity/waterfall 走 PaperMC API；Sponge 走官方 Maven metadata；bungeecord 仅有单一 latest。
func (s *CoreService) ListVersions(ctx context.Context, coreType string) ([]string, error) {
	p := project(coreType)
	if p == "bungeecord" {
		return []string{"latest"}, nil
	}
	if spongeFamily(p) {
		return s.listSpongeMCVersions(ctx, p)
	}
	if !paperFamily(p) {
		return nil, fmt.Errorf("暂不支持的核心类型: %s", coreType)
	}
	// fill v3：versions 为 {minor:[patch...]} 分组对象（组与组内均新→旧）。
	// 按 JSON 出现顺序扁平化以保留「新→旧」，供前端默认选最新。
	var out struct {
		Versions json.RawMessage `json:"versions"`
	}
	if err := s.getJSON(ctx, fmt.Sprintf("%s/%s", s.paperBase(), p), &out); err != nil {
		return nil, err
	}
	return flattenPaperVersions(out.Versions)
}

// flattenPaperVersions 把 fill v3 的分组版本对象 {minor:[patch...]} 按 JSON 出现顺序（新→旧）扁平化为版本列表。
// 用流式 token 解码保留键顺序（map 解码会丢序）。
func flattenPaperVersions(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("核心仓库未返回版本")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // 开括号 {
		return nil, err
	}
	versions := make([]string, 0)
	for dec.More() {
		if _, err := dec.Token(); err != nil { // minor 键
			return nil, err
		}
		var patches []string
		if err := dec.Decode(&patches); err != nil {
			return nil, err
		}
		versions = append(versions, patches...)
	}
	return versions, nil
}

// ResolveBuild 解析指定核心类型/版本的下载信息。build<=0 取最新构建。
// bungeecord 直接返回 md-5 Jenkins 的 latest jar（无版本/构建/校验）。
func (s *CoreService) ResolveBuild(ctx context.Context, coreType, mcVersion string, build int) (*CoreInfo, error) {
	p := project(coreType)
	if p == "bungeecord" {
		return &CoreInfo{
			Type:        "bungeecord",
			MCVersion:   "latest",
			Build:       0,
			Filename:    "BungeeCord.jar",
			DownloadURL: bungeeJenkinsURL,
			SHA256:      "",
		}, nil
	}
	if spongeFamily(p) {
		return s.resolveSpongeBuild(ctx, p, mcVersion, build)
	}
	if !paperFamily(p) {
		return nil, fmt.Errorf("暂不支持的核心类型: %s", coreType)
	}
	if strings.TrimSpace(mcVersion) == "" {
		return nil, fmt.Errorf("缺少 mcVersion")
	}
	// fill v3：builds 端点直接返回构建数组（含 id/channel/downloads），下载产物在 downloads["server:default"]。
	var builds []struct {
		ID        int `json:"id"`
		Downloads map[string]struct {
			Name      string `json:"name"`
			Checksums struct {
				SHA256 string `json:"sha256"`
			} `json:"checksums"`
			URL string `json:"url"`
		} `json:"downloads"`
	}
	if err := s.getJSON(ctx, fmt.Sprintf("%s/%s/versions/%s/builds", s.paperBase(), p, mcVersion), &builds); err != nil {
		return nil, err
	}
	if len(builds) == 0 {
		return nil, fmt.Errorf("%s %s 无可用构建", coreType, mcVersion)
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].ID > builds[j].ID })

	chosen := builds[0]
	if build > 0 {
		found := false
		for _, b := range builds {
			if b.ID == build {
				chosen, found = b, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%s %s 无构建 #%d", coreType, mcVersion, build)
		}
	}

	dl, ok := chosen.Downloads["server:default"]
	if !ok || dl.Name == "" || dl.URL == "" {
		return nil, fmt.Errorf("%s %s #%d 缺少下载产物", coreType, mcVersion, chosen.ID)
	}
	return &CoreInfo{
		Type:        p,
		MCVersion:   mcVersion,
		Build:       chosen.ID,
		Filename:    dl.Name,
		DownloadURL: dl.URL,
		SHA256:      dl.Checksums.SHA256,
	}, nil
}

func (s *CoreService) getJSON(ctx context.Context, url string, v interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", coreHTTPUserAgent)
	resp, err := s.httpClient().Do(req)
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

func (s *CoreService) getXML(ctx context.Context, url string, v interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", coreHTTPUserAgent)
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("请求核心仓库失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("核心仓库返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return xml.NewDecoder(resp.Body).Decode(v)
}

func (s *CoreService) paperBase() string {
	if strings.TrimSpace(s.base) != "" {
		return strings.TrimRight(s.base, "/")
	}
	return paperAPIBase
}

func (s *CoreService) spongeMavenBase() string {
	if strings.TrimSpace(s.spongeBase) != "" {
		return strings.TrimRight(s.spongeBase, "/")
	}
	return spongeMavenBase
}

func (s *CoreService) forgeMavenBase() string {
	if strings.TrimSpace(s.forgeBase) != "" {
		return strings.TrimRight(s.forgeBase, "/")
	}
	return forgeMavenBase
}

// paperFamily 判断核心类型是否走 PaperMC API（paper 后端 + velocity/waterfall 代理）。
func paperFamily(t string) bool {
	switch project(t) {
	case "paper", "velocity", "waterfall":
		return true
	}
	return false
}

// spongeFamily 判断核心类型是否为 Sponge 后端核心（FR-046）。
func spongeFamily(t string) bool {
	switch project(t) {
	case "spongevanilla", "spongeforge":
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

type mavenMetadata struct {
	Versioning struct {
		Versions []string `xml:"versions>version"`
	} `xml:"versioning"`
}

type spongeBuildInfo struct {
	Artifact     string
	Version      string
	MCVersion    string
	Build        int
	Filename     string
	DownloadURL  string
	ForgeVersion string
	Runtime      *CoreRuntimeInfo
	order        int
}

func (s *CoreService) listSpongeMCVersions(ctx context.Context, artifact string) ([]string, error) {
	builds, err := s.spongeBuilds(ctx, artifact)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	versions := make([]string, 0)
	for i := len(builds) - 1; i >= 0; i-- {
		mc := builds[i].MCVersion
		if mc != "" && !seen[mc] {
			seen[mc] = true
			versions = append(versions, mc)
		}
	}
	return versions, nil
}

func (s *CoreService) resolveSpongeBuild(ctx context.Context, artifact, mcVersion string, build int) (*CoreInfo, error) {
	mcVersion = strings.TrimSpace(mcVersion)
	if mcVersion == "" {
		return nil, fmt.Errorf("缺少 mcVersion")
	}
	builds, err := s.spongeBuilds(ctx, artifact)
	if err != nil {
		return nil, err
	}
	matches := make([]spongeBuildInfo, 0)
	for _, b := range builds {
		if b.MCVersion == mcVersion {
			matches = append(matches, b)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%s %s 无可用构建", artifact, mcVersion)
	}
	chosen := matches[len(matches)-1]
	if build > 0 {
		found := false
		for i := len(matches) - 1; i >= 0; i-- {
			if matches[i].Build == build {
				chosen, found = matches[i], true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%s %s 无构建 #%d", artifact, mcVersion, build)
		}
	}
	if artifact == "spongeforge" && (chosen.Runtime == nil || chosen.Runtime.ForgeInstallerURL == "") {
		return nil, fmt.Errorf("无法从 SpongeForge 版本 %s 解析 Forge installer 坐标", chosen.Version)
	}
	return &CoreInfo{
		Type:        artifact,
		MCVersion:   chosen.MCVersion,
		Build:       chosen.Build,
		Filename:    chosen.Filename,
		DownloadURL: chosen.DownloadURL,
		SHA256:      "",
		Runtime:     chosen.Runtime,
	}, nil
}

func (s *CoreService) spongeBuilds(ctx context.Context, artifact string) ([]spongeBuildInfo, error) {
	var meta mavenMetadata
	url := fmt.Sprintf("%s/%s/maven-metadata.xml", s.spongeMavenBase(), artifact)
	if err := s.getXML(ctx, url, &meta); err != nil {
		return nil, err
	}
	builds := make([]spongeBuildInfo, 0, len(meta.Versioning.Versions))
	for i, version := range meta.Versioning.Versions {
		if b, ok := s.parseSpongeVersion(artifact, strings.TrimSpace(version), i); ok {
			builds = append(builds, b)
		}
	}
	if len(builds) == 0 {
		return nil, fmt.Errorf("%s 无可用构建", artifact)
	}
	return builds, nil
}

func (s *CoreService) parseSpongeVersion(artifact, version string, order int) (spongeBuildInfo, bool) {
	parts := strings.Split(version, "-")
	if len(parts) < 2 || parts[0] == "" {
		return spongeBuildInfo{}, false
	}
	filename := fmt.Sprintf("%s-%s-universal.jar", artifact, version)
	info := spongeBuildInfo{
		Artifact:    artifact,
		Version:     version,
		MCVersion:   parts[0],
		Build:       parseSpongeBuildNumber(version),
		Filename:    filename,
		DownloadURL: fmt.Sprintf("%s/%s/%s/%s", s.spongeMavenBase(), artifact, version, filename),
		order:       order,
	}
	if artifact == "spongeforge" {
		forgeVersion := parseSpongeForgeVersion(parts)
		info.ForgeVersion = forgeVersion
		info.Runtime = &CoreRuntimeInfo{
			Distribution: "spongeforge",
			ModFilename:  "SpongeForge.jar",
			ForgeVersion: forgeVersion,
			LaunchJar:    forgeLaunchJar(forgeVersion),
		}
		if forgeVersion != "" {
			info.Runtime.ForgeInstallerURL = fmt.Sprintf("%s/%s/forge-%s-installer.jar", s.forgeMavenBase(), forgeVersion, forgeVersion)
		}
	}
	return info, true
}

var spongeRCBuildRE = regexp.MustCompile(`(?:^|-)RC(\d+)$`)
var trailingNumberRE = regexp.MustCompile(`(\d+)$`)

func parseSpongeBuildNumber(version string) int {
	if m := spongeRCBuildRE.FindStringSubmatch(version); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	if m := trailingNumberRE.FindStringSubmatch(version); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 0
}

func parseSpongeForgeVersion(parts []string) string {
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	// 现代 SpongeForge 版本形如 1.21.1-52.1.5-12.0.4-RC2665，Forge Maven 坐标为 1.21.1-52.1.5。
	// 旧 1.12.2-2838-* 只含 Forge build 号，无法可靠还原官方 installer 坐标，交由 resolve 阶段报错。
	if !strings.Contains(parts[1], ".") {
		return ""
	}
	return parts[0] + "-" + parts[1]
}

func forgeLaunchJar(forgeVersion string) string {
	if strings.TrimSpace(forgeVersion) == "" {
		return ""
	}
	return "forge-" + forgeVersion + "-server.jar"
}
