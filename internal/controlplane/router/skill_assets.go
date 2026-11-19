package router

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/skill"
)

// Agent 运维知识静态路由（FR-566 / ADR-111）。
//
// 双通道设计：仓库根 `skill/` 既是 GitHub 权威源，也由 CP 以匿名静态路由对外提供，
// 使纯内网（无外网）部署的 agent 也能取到 SKILL.md 与 references。
//
// 匿名可读的理由：文档只描述已公开的工具契约与操作注意事项，
// 不含密钥、内网地址或业务数据；置于 Token 授权之后不增加安全性，只增加 agent 使用成本。

// registerSkillRoutes 挂载 /api/v1/skill/<path> 匿名静态路由。
//
// 必须在 SPA NoRoute 之前注册（与 install-worker 脚本同理），否则会被回退成 index.html。
func registerSkillRoutes(r *gin.Engine) {
	r.GET("/api/v1/skill/*filepath", serveSkillFile)
}

// serveSkillFile 返回内嵌的知识文件。
//
// 安全边界：
//   - 只允许 .md，避免把 go:embed 意外纳入的其它类型暴露出去
//   - 路径经 path.Clean 归一，拒绝目录穿越（fs.ReadFile 亦会拒绝 ".."，此处双保险）
//   - 只读内嵌 FS，不回退到磁盘
func serveSkillFile(c *gin.Context) {
	rel := strings.TrimPrefix(c.Param("filepath"), "/")
	if rel == "" {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "NOT_FOUND",
			"message": "请指定文件，如 /api/v1/skill/SKILL.md",
		})
		return
	}
	// 归一化并拒绝穿越：Clean 后仍含 ".." 视为非法。
	clean := path.Clean(rel)
	if clean == "." || strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_PATH",
			"message": "非法路径",
		})
		return
	}
	if !strings.HasSuffix(clean, ".md") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_PATH",
			"message": "只提供 .md 文件",
		})
		return
	}

	body, err := fs.ReadFile(skill.FS(), clean)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "NOT_FOUND",
			"message": "知识条目不存在: " + clean,
		})
		return
	}

	// 知识文档随版本演进，允许缓存但要求重验证，兼顾内网带宽与更新及时性。
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", body)
}
