package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// logPersistSkipKey 是一个 slog 属性键：带此属性的日志记录不落库，
// 用于打断「日志服务自身的错误日志 → 落库失败 → 再记日志」的潜在递归。
const logPersistSkipKey = "_log_persist_skip"

// SkipPersist 返回一个标记「本条日志不落库」的 slog 属性。
// 日志服务内部的错误日志应携带它（见 log.go / log_archive.go 的内部告警）。
func SkipPersist() slog.Attr { return slog.Bool(logPersistSkipKey, true) }

// persistHandler 是一个 slog.Handler 装饰器：在委托给底层 handler（stdout）之外，
// 把平台（Control Plane）结构化日志同时落库到 LogService（FR-049）。
//
// 落库经 LogService.Ingest 异步非阻塞，不拖慢日志调用方；带 SkipPersist 属性的记录跳过落库。
type persistHandler struct {
	inner slog.Handler
	svc   *LogService
	attrs []slog.Attr
}

// NewPersistSlogHandler 包装一个底层 handler，使经它输出的平台日志同时落库。
// 当 svc 为 nil 或未启用持久化时，直接返回 inner（零开销旁路）。
func NewPersistSlogHandler(inner slog.Handler, svc *LogService) slog.Handler {
	if svc == nil || !svc.cfg.Enabled || !svc.cfg.PersistPlatform {
		return inner
	}
	return &persistHandler{inner: inner, svc: svc}
}

// Enabled 委托底层 handler。
func (h *persistHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle 先落库（除非标记跳过），再委托底层 handler 输出到 stdout。
func (h *persistHandler) Handle(ctx context.Context, r slog.Record) error {
	skip := false
	var sb strings.Builder
	collect := func(a slog.Attr) bool {
		if a.Key == logPersistSkipKey {
			if a.Value.Kind() == slog.KindBool && a.Value.Bool() {
				skip = true
			}
			return true
		}
		// 把属性平铺进正文，便于检索（k=v）。
		sb.WriteByte(' ')
		sb.WriteString(a.Key)
		sb.WriteByte('=')
		sb.WriteString(a.Value.String())
		return true
	}
	for _, a := range h.attrs {
		collect(a)
	}
	r.Attrs(func(a slog.Attr) bool { return collect(a) })

	if !skip {
		msg := r.Message + sb.String()
		ts := r.Time
		if ts.IsZero() {
			ts = time.Now()
		}
		h.svc.Ingest(IngestEntry{
			Source:  model.LogSourceControlPlane,
			Level:   mapSlogLevel(r.Level),
			Message: msg,
			Time:    ts,
		})
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs 透传到底层并累积属性，使后续 Handle 能把它们一并落库。
func (h *persistHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &persistHandler{inner: h.inner.WithAttrs(attrs), svc: h.svc, attrs: merged}
}

// WithGroup 透传到底层（分组语义不影响落库的扁平正文）。
func (h *persistHandler) WithGroup(name string) slog.Handler {
	return &persistHandler{inner: h.inner.WithGroup(name), svc: h.svc, attrs: h.attrs}
}

// mapSlogLevel 把 slog 级别映射为日志模型级别。
func mapSlogLevel(l slog.Level) model.LogLevel {
	switch {
	case l >= slog.LevelError:
		return model.LogLevelError
	case l >= slog.LevelWarn:
		return model.LogLevelWarn
	case l >= slog.LevelInfo:
		return model.LogLevelInfo
	default:
		return model.LogLevelDebug
	}
}
