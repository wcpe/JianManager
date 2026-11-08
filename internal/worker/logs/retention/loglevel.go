package retention

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// LogLevelController 是运行期日志等级的可调入口。
//
// 为什么需要它（2026-10-26）：`log.level` 此前只在启动期装配一次，进程内没有任何可达入口，
// 现场要临时看 DEBUG 必须改配置并重启 Worker —— 而重启会中断采集与实例连接。
// 本类型把 `*slog.LevelVar` 暴露成一个受限的 HTTP 动作，做到「改级别不重启」。
//
// 边界（勿夸大）：
//   - 只调**等级**，不改格式（json/text 在装配期定型，改它必须重建 handler）。
//   - 只影响本进程已装配的 handler；子进程（wrapper/java/VL）日志不受影响。
//   - 不持久化：重启后回到 `log.level` 配置值。这是刻意的——运行期调级是应急手段，
//     不该悄悄改掉配置文件里声明的事实。
//   - 无鉴权，依赖与 pprof 相同的 127.0.0.1 回环绑定不新增暴露面。
type LogLevelController struct {
	levelVar *slog.LevelVar
}

// NewLogLevelController 构造控制器。levelVar 为 nil 时所有调用安全空转（返回未启用）。
func NewLogLevelController(levelVar *slog.LevelVar) *LogLevelController {
	return &LogLevelController{levelVar: levelVar}
}

// logLevelRequest 是调级请求体。
type logLevelRequest struct {
	// Level 目标等级，取值同 log.level 配置：debug / info / warn / error。
	Level string `json:"level"`
}

// logLevelResponse 是调级响应体，回显生效值便于调用方确认。
type logLevelResponse struct {
	Level string `json:"level"`
}

// Handler 返回运行期调级处理器。
//
// 语义：GET 读当前等级；POST 改等级（仅接受上述四种写法）。
// 方法不符返回 405，等级非法返回 400，控制器未启用返回 503 —— 都带 error 字段说明原因，
// 不用空 body 让调用方猜。
func (c *LogLevelController) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil || c.levelVar == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "运行期调级未启用（LevelVar 未装配）",
			})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, logLevelResponse{Level: levelName(c.levelVar.Level())})
		case http.MethodPost:
			var req logLevelRequest
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "请求体不是合法 JSON",
				})
				return
			}
			lvl, ok := parseLogLevel(req.Level)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "level 非法（支持 debug/info/warn/error）",
				})
				return
			}
			before := levelName(c.levelVar.Level())
			c.levelVar.Set(lvl)
			after := levelName(c.levelVar.Level())
			if before != after {
				// 调级是应急动作，留一条 INFO 便于事后追溯「谁在什么时候把级别降下来了」。
				// 注意：若本次是调高门槛（如 info→error），这条 INFO 本身可能被过滤掉，属预期。
				slog.Log(r.Context(), slog.LevelInfo, "运行期日志等级已调整",
					"from", before, "to", after)
			}
			writeJSON(w, http.StatusOK, logLevelResponse{Level: after})
		default:
			w.Header().Set("Allow", "GET, POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
				"error": "仅支持 GET（读）与 POST（改）",
			})
		}
	}
}

// parseLogLevel 与 config.LogLevel 的取值集合保持一致。
//
// 为什么不直接复用 config.LogLevel：那个方法挂在 *Config 上、读的是配置字段，
// 而这里是运行期入参；两者取值集合必须一致，故在此重复一份并各自有测试守护。
func parseLogLevel(raw string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return 0, false
	}
}

// levelName 把 slog.Level 折回配置面的四种写法，便于与人读的配置值对齐。
func levelName(l slog.Level) string {
	switch {
	case l <= slog.LevelDebug:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	default:
		return "error"
	}
}

// writeJSON 统一 JSON 响应出口。
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
