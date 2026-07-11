package embed

import (
	"embed"
	"encoding/json"
)

// workerFS 内嵌与 CP 同构建的 Worker 二进制（FR-278，见 ADR-062 修订 ADR-059）。
// 二进制与 manifest 经 `make embed-worker` 注入到 embed/worker/；目录内 .gitignore
// 保证未注入时目录依然存在、go:embed 不致编译失败，运行时优雅降级「未内嵌」。
//
//go:embed all:worker
var workerFS embed.FS

// WorkerAssetManifestEntry 描述一条内嵌 Worker 资产（构建期由 embed-worker 生成）。
type WorkerAssetManifestEntry struct {
	// OS 目标操作系统（GOOS，如 windows/linux）。
	OS string `json:"os"`
	// Arch 目标架构（GOARCH，如 amd64）。
	Arch string `json:"arch"`
	// File 相对 embed/worker/ 的文件名（如 worker-linux-amd64）。
	File string `json:"file"`
	// SHA256 二进制内容指纹（小写 hex），物化进缓存时作元数据与校验依据。
	SHA256 string `json:"sha256"`
	// Size 二进制字节数。
	Size int64 `json:"size"`
}

// WorkerAssetManifest 是内嵌 Worker 资产清单（embed/worker/manifest.json）。
type WorkerAssetManifest struct {
	// Version 构建期 Worker 版本（与 CP version.Version 同源注入）。
	Version string `json:"version"`
	// Assets 各平台条目。
	Assets []WorkerAssetManifestEntry `json:"assets"`
}

// EmbeddedWorkerManifest 返回内嵌 Worker 资产清单；未经 `make embed-worker` 注入
// 或 manifest 不可解析时返回 nil（调用方据此降级到缓存/远程链路）。
func EmbeddedWorkerManifest() *WorkerAssetManifest {
	raw, err := workerFS.ReadFile("worker/manifest.json")
	if err != nil {
		return nil
	}
	var m WorkerAssetManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return &m
}

// EmbeddedWorkerBinary 按 manifest 条目取内嵌 Worker 二进制字节；缺失返回 nil。
func EmbeddedWorkerBinary(entry WorkerAssetManifestEntry) []byte {
	if entry.File == "" {
		return nil
	}
	b, err := workerFS.ReadFile("worker/" + entry.File)
	if err != nil {
		return nil
	}
	return b
}
