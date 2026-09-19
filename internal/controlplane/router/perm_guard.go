package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// requireNodes 校验当前用户命中任一权限节点；平台管理员恒通过。
// 写路径必须传写节点（如 instance.operate），禁止只传 *.read。
func requireNodes(c *gin.Context, nodes ...string) bool {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权限"})
		return false
	}
	if access.IsPlatformAdmin {
		return true
	}
	for _, n := range nodes {
		if access.HasNode(n) {
			return true
		}
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足", "nodes": nodes})
	return false
}
