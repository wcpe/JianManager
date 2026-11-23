package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/platform/taillog"
)

// 平台诊断日志处理器（本次新增）。
//
// # 设计边界（与“日志检索”严格区分）
//
// 这只回答一个问题：「 CP 进程自己最近发生了什么？」
// 它**不做**：关键字检索、时间窗查询、分页、多文件聚合、统计。
// 那些属于日志检索（实例日志走 VL 联邦），而进程自身日志的量级与用途
// （崩溃时刻人看几百行）不需要检索能力。
//
// # 为什么只读尾部
//
// 事故现场日志可达 35GB（2026-10-27 worker.out 真实案例）。只读尾部有界区间，
// 使响应耗时与文件大小无关——这一点由 taillog.Read 保证，并有回归测试。

// DiagnosticsLogHandler 提供平台自身的日志诊断视图。
type DiagnosticsLogHandler struct {
	// logPath 是 CP 进程日志文件的绝对路径；空串表示未启用文件日志。
	// 由进程启动时注入（见 apps/control-plane 的 initLogger）。
	logPath func() string
}

// NewDiagnosticsLogHandler 创建处理器。logPath 返回当前进程日志路径，可为 nil。
func NewDiagnosticsLogHandler(logPath func() string) *DiagnosticsLogHandler {
	return &DiagnosticsLogHandler{logPath: logPath}
}

// RegisterRoutes 注册诊断路由。
// logPath 为 nil 时不注册——避免暴露一个必定 503 的端点给浏览器无故探测。
func (h *DiagnosticsLogHandler) RegisterRoutes(rg *gin.RouterGroup) {
	if h == nil || h.logPath == nil {
		return
	}
	rg.GET("/diagnostics/log", h.Tail)
}

// Tail GET /api/v1/diagnostics/log?lines=200
//
// 返回平台进程日志的尾部若干行。
// 权限：仅平台管理员——进程日志含内部路径、配置键值、上游错误细节，
// 不应向组管理员/普通用户暴露。
func (h *DiagnosticsLogHandler) Tail(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "平台日志诊断仅限平台管理员"})
		return
	}

	if h.logPath == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "LOG_FILE_DISABLED",
			"message": "进程日志文件未启用",
		})
		return
	}
	path := h.logPath()
	if path == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "LOG_FILE_DISABLED",
			"message": "进程日志文件未初始化",
		})
		return
	}

	// lines 只用于收紧，实际上限由 taillog 强制夹紧。
	lines, _ := strconv.Atoi(c.Query("lines"))
	res, err := taillog.Read(path, lines, 0)
	if err != nil {
		if errors.Is(err, taillog.ErrFileMissing) {
			// 文件还没产生：不是服务故障，如实告知而不是空成功。
			c.JSON(http.StatusOK, gin.H{
				"content":   "",
				"truncated": false,
				"fileBytes": 0,
				"path":      path,
				"note":      "日志文件尚未创建（进程可能刚启动）",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "LOG_READ_FAILED",
			"message": "读取进程日志失败",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"content":   res.Content,
		"truncated": res.Truncated,
		"fileBytes": res.FileBytes,
		"path":      res.Path,
	})
}
