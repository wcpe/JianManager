package service

import (
	"encoding/json"
)

// 客户端分发 manifest 的类型与 JSON 序列化（FR-087 / FR-256 简化后）。
//
// FR-256 起 manifest 不再携带 Ed25519 签名段（信任模型改为 HTTPS + 拉取密钥鉴权，
// 见 docs/specs/updater-arch-simplification/spec.md §2 A，推翻 ADR-022/053）。
// 客户端 updater-core 拉到 manifest 直接用，不再验签。
//
// MarshalJSON 仍走 manifestToTree 单一真源：保证全平台文件 platform 输出为 JSON null
// （而非 Go 零值 ""），与客户端 Manifest.parse 对齐。

// manifestSchemaVersion 契约结构版本（contract §2 schemaVersion）。结构 break 时 +1。
const manifestSchemaVersion = 1

// ManifestArtifact manifest 文件的下载制品引用（内容寻址，contract §2 files[].artifact）。
type ManifestArtifact struct {
	// SHA256 制品（压缩后）自身 hash = 下载寻址 key = client-file 资产的 sha256。
	SHA256 string `json:"sha256"`
	// Size 制品（压缩后）字节数。
	Size int64 `json:"size"`
	// Codec 压缩算法："zstd" | "none"。
	Codec string `json:"codec"`
}

// ManifestPatch manifest 单文件的 zstd patch-from 增量制品引用（FR-098）。
type ManifestPatch struct {
	// OldSHA256 patch 适用的本地旧文件原始内容 hash。
	OldSHA256 string `json:"oldSha256"`
	// NewSHA256 patch 应产出的新文件原始内容 hash，通常等于 files[].sha256。
	NewSHA256 string `json:"newSha256"`
	// Artifact patch 制品引用，codec 固定为 zstd-patch。
	Artifact ManifestArtifact `json:"artifact"`
}

// ManifestFile manifest 单文件条目（contract §2 files[]）。
// sha256/md5/size 描述**解压后原始内容**（强校验/快筛）；artifact 描述下载制品（压缩态）。
type ManifestFile struct {
	// Path 相对 gameDir 的 POSIX 路径（统一 `/`，不得逃逸 `..`）。
	Path string `json:"path"`
	// SHA256 解压后原始内容 hash（完整性校验，强）。
	SHA256 string `json:"sha256"`
	// MD5 解压后原始内容 md5（本地快筛，弱，不可作信任）。
	MD5 string `json:"md5"`
	// Size 解压后原始大小（字节）。
	Size int64 `json:"size"`
	// Sync 同步策略：strict=强制一致 | once=仅缺失时写 | ignore=不动。
	Sync string `json:"sync"`
	// Platform 平台门控：空=全平台 | windows | macos | linux。
	Platform string `json:"platform"`
	// Artifact 下载制品引用。
	Artifact ManifestArtifact `json:"artifact"`
	// Patch 可选 patch-from 增量制品；客户端无法应用时回退 Artifact。
	Patch *ManifestPatch `json:"patch,omitempty"`
}

// ValidSyncMode 报告 sync 取值是否合法（strict|once|ignore）。
func ValidSyncMode(s string) bool {
	switch s {
	case "strict", "once", "ignore":
		return true
	}
	return false
}

// ValidPlatform 报告 platform 取值是否合法（空=全平台，或 windows|macos|linux）。
func ValidPlatform(s string) bool {
	switch s {
	case "", "windows", "macos", "linux":
		return true
	}
	return false
}

// ManifestAgentArtifact 自更新段单平台制品（contract §2 agent.core.platforms[os].artifact）。
type ManifestAgentArtifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Codec  string `json:"codec"`
}

// ManifestWedge 楔子版本段（信息性；楔子随基础包、不自更新）。
type ManifestWedge struct {
	Version int `json:"version"`
}

// ManifestCore updater-core 自更新段（FR-091 消费）：版本 + 各平台制品。
// FR-256 起 core 自更新上移到楔子（FR-258），但 manifest 仍透传该段供楔子消费。
type ManifestCore struct {
	Version   int                              `json:"version"`
	Platforms map[string]ManifestAgentArtifact `json:"platforms"`
}

// ManifestAgent 楔子 + updater-core 自更新段（contract §2 agent）。
type ManifestAgent struct {
	Wedge *ManifestWedge `json:"wedge,omitempty"`
	Core  *ManifestCore  `json:"core,omitempty"`
}

// SignedManifest 完整 manifest（contract §2）。
//
// 类型名保留「Signed」以兼容既有引用（FR-256 起不再签名，sig 段已去）。
// MarshalJSON 走 manifestToTree 单一真源：全平台文件 platform 统一为 JSON null。
type SignedManifest struct {
	SchemaVersion int      `json:"schemaVersion"`
	Channel       string   `json:"channel"`
	Version       int      `json:"version"`
	IssuedAt      string   `json:"issuedAt"`
	ManagedDirs   []string `json:"managedDirs"`
	// CleanExclude 运营自定义追加排除（FR-255）：命中前缀的路径永不删（叠加在 PLAYER_ZONE 之上）。
	// 空则省略（omitempty）——老 manifest canonical 字节不变，schemaVersion 维持 1（方案 A）。
	CleanExclude []string       `json:"cleanExclude,omitempty"`
	Files        []ManifestFile `json:"files"`
	Agent        *ManifestAgent `json:"agent,omitempty"`
}

// MarshalJSON 从 manifestToTree 生成响应 JSON，确保全平台 platform 输出为 null（见 SignedManifest 文档）。
func (m *SignedManifest) MarshalJSON() ([]byte, error) {
	return json.Marshal(manifestToTree(m))
}

// manifestToTree 把 SignedManifest 转为有序无关的原生对象树（map/slice/string/int64/nil），
// 供 MarshalJSON 序列化。键名与 contract §2 字段名严格一致。
// FR-256 起不再产 sig 段。
func manifestToTree(m *SignedManifest) map[string]any {
	root := map[string]any{
		"schemaVersion": int64(m.SchemaVersion),
		"channel":       m.Channel,
		"version":       int64(m.Version),
		"issuedAt":      m.IssuedAt,
		"managedDirs":   stringsToTree(m.ManagedDirs),
		"files":         filesToTree(m.Files),
	}
	// FR-255：cleanExclude 仅在非空时输出（omitempty）。
	if len(m.CleanExclude) > 0 {
		root["cleanExclude"] = stringsToTree(m.CleanExclude)
	}
	if m.Agent != nil {
		root["agent"] = agentToTree(m.Agent)
	}
	return root
}

func stringsToTree(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func filesToTree(files []ManifestFile) []any {
	out := make([]any, len(files))
	for i, f := range files {
		item := map[string]any{
			"path":     f.Path,
			"sha256":   f.SHA256,
			"md5":      f.MD5,
			"size":     f.Size,
			"sync":     f.Sync,
			"platform": platformValue(f.Platform),
			"artifact": map[string]any{
				"sha256": f.Artifact.SHA256,
				"size":   f.Artifact.Size,
				"codec":  f.Artifact.Codec,
			},
		}
		if f.Patch != nil {
			item["patch"] = map[string]any{
				"oldSha256": f.Patch.OldSHA256,
				"newSha256": f.Patch.NewSHA256,
				"artifact": map[string]any{
					"sha256": f.Patch.Artifact.SHA256,
					"size":   f.Patch.Artifact.Size,
					"codec":  f.Patch.Artifact.Codec,
				},
			}
		}
		out[i] = item
	}
	return out
}

func agentToTree(a *ManifestAgent) map[string]any {
	out := map[string]any{}
	if a.Wedge != nil {
		out["wedge"] = map[string]any{"version": int64(a.Wedge.Version)}
	}
	if a.Core != nil {
		platforms := map[string]any{}
		for os, art := range a.Core.Platforms {
			platforms[os] = map[string]any{
				"artifact": map[string]any{
					"sha256": art.SHA256,
					"size":   art.Size,
					"codec":  art.Codec,
				},
			}
		}
		out["core"] = map[string]any{
			"version":   int64(a.Core.Version),
			"platforms": platforms,
		}
	}
	return out
}

// platformValue 把空字符串平台映射为 JSON null（contract §2：null=全平台）。
func platformValue(p string) any {
	if p == "" {
		return nil
	}
	return p
}
