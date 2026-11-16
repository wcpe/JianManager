// Package skill 内嵌 Agent 运维知识（SKILL.md 与 references/），随 CP 二进制发布，
// 由 GET /api/v1/skill/<path> 静态路由对外提供（ADR-103）。
//
// 双通道设计：本目录同时是 GitHub 仓库的权威源（可直接 clone / npx skills add 安装到宿主），
// 也是 CP 静态路由的内容源——内网无外网时 agent 也能取到。两处内容完全一致，不存在第二份真源。
//
// 白名单式覆盖：只内嵌 SKILL.md 与 references，机器资源（脚本、素材）不内嵌。
// 新增内容类型必须显式加入 go:embed 列表才会进二进制。
package skill

import (
	"embed"
	"io/fs"
)

//go:embed SKILL.md references
var content embed.FS

// FS 返回以本目录为根的只读文件系统，路径形如 "SKILL.md"、"references/destructive-ops.md"。
func FS() fs.FS {
	return content
}
