package embed

import (
	"embed"
	"encoding/json"
)

// botWorkerFS 内嵌与 CP 同构建的 bot-worker dist 归档（FR-308，见 ADR-070 修订 ADR-006）。
// 归档与 manifest 经 `make embed-botworker` 注入到 embed/botworker/；目录内 .gitignore
// 保证未注入时目录依然存在、go:embed 不致编译失败，运行时优雅降级「未内嵌」。
//
//go:embed all:botworker
var botWorkerFS embed.FS

// BotWorkerManifest 内嵌 bot-worker 归档的清单（构建期由 embed-botworker 生成）。
type BotWorkerManifest struct {
	// Version 构建版本（与 CP version 同源同值）。
	Version string `json:"version"`
	// SHA256 tar.gz 归档指纹（小写 hex），Worker 据此比对本地 dist 决定是否重拉。
	SHA256 string `json:"sha256"`
	// Size 归档字节数。
	Size int64 `json:"size"`
}

// EmbeddedBotWorkerManifest 返回内嵌 bot-worker 清单；未注入返回 (zero,false)。
func EmbeddedBotWorkerManifest() (BotWorkerManifest, bool) {
	raw, err := botWorkerFS.ReadFile("botworker/manifest.json")
	if err != nil {
		return BotWorkerManifest{}, false
	}
	var m BotWorkerManifest
	if err := json.Unmarshal(raw, &m); err != nil || m.SHA256 == "" {
		return BotWorkerManifest{}, false
	}
	return m, true
}

// EmbeddedBotWorkerArchive 返回内嵌 bot-worker tar.gz 字节；未注入返回 (nil,false)。
func EmbeddedBotWorkerArchive() ([]byte, bool) {
	b, err := botWorkerFS.ReadFile("botworker/bot-worker.tar.gz")
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}
