package middleware

import (
	"bytes"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuditConfig 审计中间件配置。
type AuditConfig struct {
	// RecordFunc 记录审计日志的回调函数。
	RecordFunc func(userID uint, action, targetType, targetID, detail, ip string)
}

// Audit 审计日志中间件，自动记录关键操作。
func Audit(cfg AuditConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 只记录写操作
		method := c.Request.Method
		if method != "GET" && method != "OPTIONS" && method != "HEAD" {
			// 读取请求体用于审计
			var body []byte
			if c.Request.Body != nil {
				body, _ = io.ReadAll(c.Request.Body)
				c.Request.Body = io.NopCloser(bytes.NewReader(body))
			}

			// 执行请求
			c.Next()

			// 记录审计日志
			if cfg.RecordFunc != nil {
				userID, _ := c.Get("userId")
				uid, _ := userID.(uint)

				action := determineAction(method, c.FullPath())
				targetType, targetID := determineTarget(c.FullPath())

				ip := c.ClientIP()
				detail := string(body)
				if len(detail) > 1024 {
					detail = detail[:1024] + "..."
				}

				if action != "" {
					cfg.RecordFunc(uid, action, targetType, targetID, detail, ip)
				}
			}
		} else {
			c.Next()
		}
	}
}

// determineAction 从 HTTP 方法和路径推断操作名称。
func determineAction(method, path string) string {
	path = strings.TrimPrefix(path, "/api/v1")

	switch {
	case method == "POST" && strings.Contains(path, "/auth/login"):
		return "auth.login"
	case method == "POST" && strings.Contains(path, "/auth/register"):
		return "auth.register"
	case method == "POST" && strings.Contains(path, "/instances") && strings.HasSuffix(path, "/start"):
		return "instance.start"
	case method == "POST" && strings.Contains(path, "/instances") && strings.HasSuffix(path, "/stop"):
		return "instance.stop"
	case method == "POST" && strings.Contains(path, "/instances") && strings.HasSuffix(path, "/restart"):
		return "instance.restart"
	case method == "POST" && strings.Contains(path, "/instances") && strings.HasSuffix(path, "/kill"):
		return "instance.kill"
	case method == "POST" && strings.Contains(path, "/instances"):
		return "instance.create"
	case method == "PUT" && strings.Contains(path, "/instances"):
		return "instance.update"
	case method == "DELETE" && strings.Contains(path, "/instances"):
		return "instance.delete"
	case method == "POST" && strings.Contains(path, "/users"):
		return "user.create"
	case method == "PUT" && strings.Contains(path, "/users"):
		return "user.update"
	case method == "DELETE" && strings.Contains(path, "/users"):
		return "user.delete"
	case method == "POST" && strings.Contains(path, "/groups"):
		return "group.create"
	case method == "PUT" && strings.Contains(path, "/groups"):
		return "group.update"
	case method == "DELETE" && strings.Contains(path, "/groups"):
		return "group.delete"
	case method == "POST" && strings.Contains(path, "/files/write"):
		return "file.write"
	case method == "DELETE" && strings.Contains(path, "/files"):
		return "file.delete"
	default:
		return ""
	}
}

// determineTarget 从路径推断操作目标类型和 ID。
func determineTarget(path string) (targetType, targetID string) {
	path = strings.TrimPrefix(path, "/api/v1")

	switch {
	case strings.Contains(path, "/instances/"):
		return "instance", extractID(path, "/instances/")
	case strings.Contains(path, "/users/"):
		return "user", extractID(path, "/users/")
	case strings.Contains(path, "/groups/"):
		return "group", extractID(path, "/groups/")
	case strings.Contains(path, "/nodes/"):
		return "node", extractID(path, "/nodes/")
	default:
		return "", ""
	}
}

// extractID 从路径中提取 ID。
func extractID(path, prefix string) string {
	idx := strings.Index(path, prefix)
	if idx < 0 {
		return ""
	}
	rest := path[idx+len(prefix):]
	// 取到下一个 / 或末尾
	if slashIdx := strings.Index(rest, "/"); slashIdx >= 0 {
		return rest[:slashIdx]
	}
	return rest
}
