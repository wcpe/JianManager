package embed

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
)

// probeFS 内嵌 ServerProbe 监控探针 jar（FR-010 建服自动部署）。
// jar 为构建产物，经 `make embed-probe` 注入到 embed/probe/；目录内的 .gitignore
// 保证未注入 jar 时该目录依然存在、go:embed 不致编译失败。
//
//go:embed all:probe
var probeFS embed.FS

// ProbeEmbeddedVersion 是内嵌探针 jar 的版本号（构建期常量，与 ServerProbe fork 的
// gradle.properties `version` 同步）。jar 的 MANIFEST.MF 不稳定携带版本，故以此常量作展示用
// 「内嵌最新版本」（FR-068 在线更新的版本对比展示）。升级探针时同步本常量与 gradle 版本。
const ProbeEmbeddedVersion = "0.1.0"

// ServerProbeJar 返回内嵌的 ServerProbe 探针 jar 字节。
// 未经 `make embed-probe` 注入（jar 缺失）时返回 nil，调用方据此优雅跳过自动部署
// （探针端口仍已分配，运维手动放入探针 jar 后 Worker 抓取即生效）。
func ServerProbeJar() []byte {
	b, err := probeFS.ReadFile("probe/ServerProbe.jar")
	if err != nil {
		return nil
	}
	return b
}

// ProbeJarInfo 描述内嵌探针 jar 的元信息，供 FR-068 在线更新展示版本对比。
type ProbeJarInfo struct {
	// Available 为 false 表示 CP 未内嵌探针 jar（未跑 make embed-probe），无法推送更新。
	Available bool `json:"available"`
	// Version 为内嵌版本号（ProbeEmbeddedVersion，构建期常量）。
	Version string `json:"version"`
	// Fingerprint 为 jar 字节 SHA-256 的前 8 位十六进制短指纹，用于「在位 vs 内嵌」内容比对
	// （在位版本无可靠来源时，指纹至少标识本次推送内容；jar 缺失为空）。
	Fingerprint string `json:"fingerprint"`
	// SizeBytes 为 jar 字节数（jar 缺失为 0）。
	SizeBytes int `json:"sizeBytes"`
}

// ServerProbeJarInfo 返回内嵌探针 jar 的元信息（版本 + 短指纹 + 大小）。
// jar 未内嵌时返回 Available=false、其余零值。
func ServerProbeJarInfo() ProbeJarInfo {
	b := ServerProbeJar()
	if len(b) == 0 {
		return ProbeJarInfo{Available: false, Version: ProbeEmbeddedVersion}
	}
	sum := sha256.Sum256(b)
	return ProbeJarInfo{
		Available:   true,
		Version:     ProbeEmbeddedVersion,
		Fingerprint: hex.EncodeToString(sum[:])[:8],
		SizeBytes:   len(b),
	}
}
