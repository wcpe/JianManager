package middleware

import (
	"log/slog"

	"github.com/gin-gonic/gin"
)

// respCaptureWriter 包装 gin ResponseWriter，捕获响应体前 cap 字节（错误取证用）。
type respCaptureWriter struct {
	gin.ResponseWriter
	buf []byte
	cap int
}

func (w *respCaptureWriter) Write(b []byte) (int, error) {
	if len(w.buf) < w.cap {
		room := w.cap - len(w.buf)
		if room > len(b) {
			room = len(b)
		}
		w.buf = append(w.buf, b[:room]...)
	}
	return w.ResponseWriter.Write(b)
}

// ErrorLog API 错误统一落平台日志（FR-320）：4xx 业务拒绝记 warn、5xx 记 error，
// 附路径/状态码/响应体/用户/IP——经 log_slog 桥自动进日志中心 platform 源，
// 用户在 /logs 页即可追查「某个操作为什么报错」（此前错误只回 HTTP 响应，平台日志恒空）。
// 401（token 过期常态）/404（探路噪音）/429（限流自身会刷）跳过，避免淹没真问题。
func ErrorLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		w := &respCaptureWriter{ResponseWriter: c.Writer, cap: 1024}
		c.Writer = w
		c.Next()

		status := c.Writer.Status()
		if status < 400 || status == 401 || status == 404 || status == 429 {
			return
		}
		userID, _ := c.Get("userId")
		uid, _ := userID.(uint)
		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"user", uid,
			"ip", c.ClientIP(),
			"resp", string(w.buf),
		}
		if status >= 500 {
			slog.Error("API 请求失败", attrs...)
		} else {
			slog.Warn("API 请求被拒", attrs...)
		}
	}
}
