package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuditConfig 审计中间件配置。
type AuditConfig struct {
	// RecordFunc 记录审计日志的回调函数。
	// success/errMsg：操作结果（FR-321）——失败操作也留痕并带错误内容（响应 error body 截断）。
	RecordFunc func(userID uint, action, targetType, targetID, detail, ip string, success bool, errMsg string)
}

// Audit 审计日志中间件，自动记录关键操作（成功与失败都记，FR-321）。
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

			// 捕获响应体前段：失败时其 error/message 即审计错误内容（FR-321）。
			w := &respCaptureWriter{ResponseWriter: c.Writer, cap: 512}
			c.Writer = w

			// 执行请求
			c.Next()

			// 记录审计日志
			if cfg.RecordFunc != nil {
				userID, _ := c.Get("userId")
				uid, _ := userID.(uint)

				action := determineAction(method, c.FullPath())
				// 目标 ID 须从真实请求路径取，c.FullPath() 是 /instances/:id 路由模式，
				// 会把审计目标记成占位符 :id（无法定位具体实例）。
				targetType, targetID := determineTarget(c.Request.URL.Path)

				ip := c.ClientIP()
				detail := sanitizeAuditDetail(body)
				if len(detail) > 1024 {
					detail = detail[:1024] + "..."
				}

				if action != "" {
					status := c.Writer.Status()
					success := status < 400
					errMsg := ""
					if !success {
						errMsg = string(w.buf)
					}
					cfg.RecordFunc(uid, action, targetType, targetID, detail, ip, success, errMsg)
				}
			}
		} else {
			c.Next()
		}
	}
}

// sensitiveAuditKeyFragments 命中即掩蔽的键名片段（小写包含比对）。
// 覆盖 password/newPassword/passwd、secret/apiSecret、token/refreshToken 等命名变体。
var sensitiveAuditKeyFragments = []string{"password", "passwd", "secret", "token"}

// sanitizeAuditDetail 把请求体转成可落库的审计 detail：JSON 体递归掩蔽凭据类字段
// （键名含 password/passwd/secret/token，大小写不敏感）为 "***"；非 JSON / 无敏感键原样返回。
// 审计要回答「谁对什么做了什么」，凭据明文不属该范畴——落库即长期泄漏
// （FR-015 审计页可见、FR-155 可全量导出），如 PUT /users/:id 重置密码的请求体。
func sanitizeAuditDetail(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return string(body)
	}
	var v interface{}
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return string(body)
	}
	masked, changed := maskSensitiveValues(v)
	if !changed {
		return string(body)
	}
	out, err := json.Marshal(masked)
	if err != nil {
		return string(body)
	}
	return string(out)
}

// maskSensitiveValues 递归掩蔽 map/数组里的敏感键值，返回是否有改动（无改动则保留原文）。
func maskSensitiveValues(v interface{}) (interface{}, bool) {
	switch t := v.(type) {
	case map[string]interface{}:
		changed := false
		for k, val := range t {
			if isSensitiveAuditKey(k) {
				t[k] = "***"
				changed = true
				continue
			}
			if nv, c := maskSensitiveValues(val); c {
				t[k] = nv
				changed = true
			}
		}
		return t, changed
	case []interface{}:
		changed := false
		for i, item := range t {
			if nv, c := maskSensitiveValues(item); c {
				t[i] = nv
				changed = true
			}
		}
		return t, changed
	default:
		return v, false
	}
}

func isSensitiveAuditKey(key string) bool {
	k := strings.ToLower(key)
	for _, frag := range sensitiveAuditKeyFragments {
		if strings.Contains(k, frag) {
			return true
		}
	}
	return false
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
	case method == "POST" && strings.HasSuffix(path, "/plugins/batch-deploy"):
		return "plugin.batchDeploy"
	case method == "POST" && strings.Contains(path, "/plugins") && strings.HasSuffix(path, "/toggle"):
		return "plugin.toggle"
	case method == "POST" && strings.Contains(path, "/plugins"):
		return "plugin.deploy"
	case method == "DELETE" && strings.Contains(path, "/plugins"):
		return "plugin.delete"
	case method == "POST" && strings.HasSuffix(path, "/instances/batch"):
		// 批量操作（FR-058）：危险操作（批量 kill/stop）留痕，请求体含动作与目标。
		return "instance.batch"
	case method == "POST" && strings.HasSuffix(path, "/instances/probe/update"):
		// 批量探针在线更新（FR-068）：留痕，请求体含 ids/filter 与 restart。
		return "probe.update.batch"
	case method == "POST" && strings.Contains(path, "/instances") && strings.HasSuffix(path, "/probe/update"):
		// 单实例探针在线更新（FR-068）：留痕，含目标实例与 restart。
		return "probe.update"
	case method == "POST" && strings.Contains(path, "/instances") && strings.HasSuffix(path, "/business"):
		// JBIS 业务命令下发（FR-116/FR-121）：中间件兜底留痕（覆盖读+写），
		// 写动作另由 BusinessHandler 记结构化 business.write（含 taskId/reason）。请求体含 domain/action/payload。
		return "business.dispatch"
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
	case method == "POST" && strings.Contains(path, "/nodes") && strings.HasSuffix(path, "/maintenance"):
		return "node.maintenance"
	case method == "POST" && strings.Contains(path, "/nodes") && strings.HasSuffix(path, "/drain"):
		return "node.drain"
	case method == "DELETE" && strings.Contains(path, "/nodes"):
		return "node.delete"
	default:
		return ""
	}
}

// determineTarget 从路径推断操作目标类型和 ID。
func determineTarget(path string) (targetType, targetID string) {
	path = strings.TrimPrefix(path, "/api/v1")

	switch {
	case strings.HasSuffix(path, "/plugins/batch-deploy"):
		return "plugin", "batch-deploy"
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
