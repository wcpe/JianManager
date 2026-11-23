package embed

import (
	"embed"
	"encoding/json"
)

// jmctlFS 内嵌与 CP 同构建的 jmctl 紧急控制台二进制。
// 二进制与 manifest 经 `make embed-jmctl` 注入到 embed/jmctl/；目录内 .gitignore
// 保证未注入时目录依然存在、go:embed 不致编译失败，运行时优雅降级「未内嵌」。
//
// jmctl 只与 Worker 有关（操作 <数据根>/var/servers/*.pid），CP 内嵌它是为了
// install-worker 首次分发与 Worker 升级时同步更新（经 /downloads/jmctl-* 路由）。
//
//go:embed all:jmctl
var jmctlFS embed.FS

// JmctlAssetManifestEntry 描述一条内嵌 jmctl 资产（构建期由 embed-jmctl 生成）。
type JmctlAssetManifestEntry struct {
	// OS 目标操作系统（GOOS，如 windows/linux）。
	OS string `json:"os"`
	// Arch 目标架构（GOARCH，如 amd64）。
	Arch string `json:"arch"`
	// File 相对 embed/jmctl/ 的文件名（如 jmctl-linux-amd64）。
	File string `json:"file"`
	// SHA256 二进制内容指纹（小写 hex）。
	SHA256 string `json:"sha256"`
	// Size 二进制字节数。
	Size int64 `json:"size"`
}

// JmctlAssetManifest 是内嵌 jmctl 资产清单（embed/jmctl/manifest.json）。
type JmctlAssetManifest struct {
	// Version 构建期 jmctl 版本（与 CP version.Version 同源注入）。
	Version string `json:"version"`
	// Assets 各平台条目。
	Assets []JmctlAssetManifestEntry `json:"assets"`
}

// EmbeddedJmctlManifest 返回内嵌 jmctl 资产清单；未经 `make embed-jmctl` 注入
// 或 manifest 不可解析时返回 nil（调用方据此降级为「未内嵌」）。
func EmbeddedJmctlManifest() *JmctlAssetManifest {
	raw, err := jmctlFS.ReadFile("jmctl/manifest.json")
	if err != nil {
		return nil
	}
	var m JmctlAssetManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return &m
}

// EmbeddedJmctlBinary 按 manifest 条目取内嵌 jmctl 二进制字节；缺失返回 nil。
func EmbeddedJmctlBinary(entry JmctlAssetManifestEntry) []byte {
	if entry.File == "" {
		return nil
	}
	b, err := jmctlFS.ReadFile("jmctl/" + entry.File)
	if err != nil {
		return nil
	}
	return b
}
