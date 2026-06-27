package jdk

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
)

// foojayDefaultBase 是 foojay disco API 默认基址（FR-178）。
// 经环境变量 JIANMANAGER_JDK_FOOJAY_BASE 覆盖（CP 侧由出站代理 client 统一发起）。
const foojayDefaultBase = "https://api.foojay.io"

// foojayBase 返回 foojay disco API 基址（环境变量覆盖优先，否则官方默认）。
func foojayBase() string {
	return envOr("JIANMANAGER_JDK_FOOJAY_BASE", foojayDefaultBase)
}

// foojayDistribution 把面板厂商名映射为 foojay disco 的 distribution slug。
// 未知名原样透传（foojay 支持的发行版很多，允许直接用其 slug）。
func foojayDistribution(vendor string) string {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "temurin", "adoptium":
		return "temurin"
	case "corretto", "amazon":
		return "corretto"
	case "zulu", "azul":
		return "zulu"
	case "liberica", "bellsoft":
		return "liberica"
	case "microsoft":
		return "microsoft"
	case "semeru", "ibm":
		return "semeru"
	case "graalvm", "graalvm_community", "graal":
		return "graalvm_community"
	case "oracle_open_jdk", "openjdk":
		return "oracle_open_jdk"
	case "dragonwell":
		return "dragonwell"
	case "sap_machine", "sapmachine":
		return "sap_machine"
	}
	return strings.ToLower(strings.TrimSpace(vendor))
}

// foojayArch 把面板架构名归一为 foojay 期望的 architecture 值。
func foojayArch(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "amd64", "x86_64", "x64":
		return "x64"
	case "arm64", "aarch64":
		return "aarch64"
	}
	if arch == "" {
		return defaultArch()
	}
	return strings.ToLower(arch)
}

// foojayOS 返回当前运行平台对应的 foojay operating_system 值。
func foojayOS() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	default:
		return "linux"
	}
}

// foojayArchiveTypeFor 返回当前平台的默认归档类型（Windows zip，其余 tar.gz）。
func foojayArchiveTypeFor() string {
	if runtime.GOOS == "windows" {
		return "zip"
	}
	return "tar.gz"
}

// foojayPackage 是 foojay disco /packages 响应中的一条（仅取面板所需字段）。
type foojayPackage struct {
	Distribution         string `json:"distribution"`
	MajorVersion         int    `json:"major_version"`
	JavaVersion          string `json:"java_version"`
	DistributionVersion  string `json:"distribution_version"`
	ArchiveType          string `json:"archive_type"`
	OperatingSystem      string `json:"operating_system"`
	Architecture         string `json:"architecture"`
	LatestBuildAvailable bool   `json:"latest_build_available"`
	Links                struct {
		PkgDownloadRedirect string `json:"pkg_download_redirect"`
	} `json:"links"`
}

type foojayPackagesResponse struct {
	Result []foojayPackage `json:"result"`
}

// CatalogPackage 是喂前端版本选择器的一条可选 JDK（FR-178）。
type CatalogPackage struct {
	Distribution string `json:"distribution"`
	MajorVersion int    `json:"majorVersion"`
	JavaVersion  string `json:"javaVersion"`
	ArchiveType  string `json:"archiveType"`
	Latest       bool   `json:"latest"`
}

// foojayQuery 构造 foojay disco /packages 查询 URL。
// version 非空时按具体版本过滤；为空时按 major + latest=available 取该大版本可用构建。
func foojayQuery(base, distribution string, major int, version, arch, archiveType string) string {
	q := url.Values{}
	q.Set("distribution", distribution)
	if strings.TrimSpace(version) != "" {
		q.Set("version", version)
	} else if major > 0 {
		q.Set("version", strconv.Itoa(major))
	}
	q.Set("architecture", foojayArch(arch))
	q.Set("operating_system", foojayOS())
	if archiveType == "" {
		archiveType = foojayArchiveTypeFor()
	}
	q.Set("archive_type", archiveType)
	q.Set("package_type", "jdk")
	// linux 需指定 glibc，避免拿到 musl/alpine 构建。
	if foojayOS() == "linux" {
		q.Set("lib_c_type", "glibc")
	}
	q.Set("release_status", "ga")
	q.Set("latest", "available")
	q.Set("directly_downloadable", "true")
	return strings.TrimRight(base, "/") + "/disco/v3.0/packages?" + q.Encode()
}

// foojayFetch 发起 foojay disco /packages 查询并解析响应。client 经进程级出站代理（FR-174）。
func foojayFetch(client *http.Client, queryURL string) ([]foojayPackage, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Get(queryURL)
	if err != nil {
		return nil, fmt.Errorf("foojay 查询失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("foojay 返回 HTTP %d", resp.StatusCode)
	}
	var out foojayPackagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("foojay 响应解析失败: %w", err)
	}
	return out.Result, nil
}

// foojayResolveDownloadURL 经 foojay disco 解析某发行版/版本的下载直链（links.pkg_download_redirect）。
// 取第一个含下载链接的结果；无结果或无链接报错（调用方据此回退直链或失败）。
func foojayResolveDownloadURL(client *http.Client, base, distribution string, major int, version, arch, archiveType string) (string, error) {
	pkgs, err := foojayFetch(client, foojayQuery(base, distribution, major, version, arch, archiveType))
	if err != nil {
		return "", err
	}
	for _, p := range pkgs {
		if p.Links.PkgDownloadRedirect != "" {
			return p.Links.PkgDownloadRedirect, nil
		}
	}
	return "", fmt.Errorf("foojay 无 %s %d 的可下载构建（arch=%s os=%s）", distribution, major, foojayArch(arch), foojayOS())
}

// FoojayCatalog 返回某发行版在某大版本下可选的具体 JDK 版本，喂前端版本选择器（FR-178）。
// major<=0 时返回该发行版的可用大版本概览（每大版本最新一条）。client 经出站代理。
func FoojayCatalog(client *http.Client, base, distribution string, major int, arch string) ([]CatalogPackage, error) {
	if base == "" {
		base = foojayBase()
	}
	dist := foojayDistribution(distribution)
	pkgs, err := foojayFetch(client, foojayQuery(base, dist, major, "", arch, ""))
	if err != nil {
		return nil, err
	}
	out := make([]CatalogPackage, 0, len(pkgs))
	seen := make(map[string]bool)
	for _, p := range pkgs {
		key := p.JavaVersion + "|" + p.ArchiveType
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, CatalogPackage{
			Distribution: p.Distribution,
			MajorVersion: p.MajorVersion,
			JavaVersion:  p.JavaVersion,
			ArchiveType:  p.ArchiveType,
			Latest:       p.LatestBuildAvailable,
		})
	}
	return out, nil
}

// buildDownloadURLV 在 buildDownloadURL 基础上支持「具体版本」与「扩厂商经 foojay」（FR-178）。
//   - 指定了 version（非空）→ 一律经 foojay 按具体版本解析（直链源不支持任意具体版本）。
//   - 未指定 version 的 Temurin/Corretto/Zulu → 沿用原直链回退（buildDownloadURL）。
//   - 未指定 version 的其它扩厂商（Liberica/Microsoft/Semeru/GraalVM…）→ 经 foojay 取该大版本最新。
//
// 经 foojay 解析失败时，对三家直链厂商回退直链；其余厂商返回 foojay 错误。
func buildDownloadURLV(client *http.Client, vendor string, major int, version, arch, mirrorBase string) (string, error) {
	hasVersion := strings.TrimSpace(version) != ""
	if !hasVersion && isDirectLinkVendor(vendor) {
		return buildDownloadURL(client, vendor, major, arch, mirrorBase)
	}

	dist := foojayDistribution(vendor)
	url, err := foojayResolveDownloadURL(client, foojayBase(), dist, major, version, arch, "")
	if err == nil {
		return url, nil
	}
	// foojay 失败：三家直链厂商仍可回退直链（仅对未指定具体版本可行）。
	if !hasVersion && isDirectLinkVendor(vendor) {
		return buildDownloadURL(client, vendor, major, arch, mirrorBase)
	}
	return "", err
}

// isDirectLinkVendor 报告 vendor 是否为有静态直链回退的三家（Temurin/Corretto/Zulu）。
func isDirectLinkVendor(vendor string) bool {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "temurin", "adoptium", "corretto", "amazon", "zulu", "azul":
		return true
	}
	return false
}
