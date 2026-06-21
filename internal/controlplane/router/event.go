package router

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// EventHandler 实例事件 SSE 推送。
type EventHandler struct {
	eventSvc *service.EventService
}

// NewEventHandler 创建事件处理器。
func NewEventHandler(eventSvc *service.EventService) *EventHandler {
	return &EventHandler{eventSvc: eventSvc}
}

// RegisterRoutes 注册事件路由。
func (h *EventHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/instances/events", h.StreamEvents)
}

// StreamEvents SSE 推送实例状态变更事件。
func (h *EventHandler) StreamEvents(c *gin.Context) {
	ch, unsub := h.eventSvc.Subscribe()
	defer unsub()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	// 发送初始连接确认
	fmt.Fprintf(c.Writer, "event: connected\ndata: {}\n\n")
	c.Writer.Flush()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data := fmt.Sprintf(`{"instanceUuid":"%s","type":"%s","data":"%s","timestamp":%d}`,
				evt.InstanceUUID, evt.Type, evt.Data, evt.Timestamp)
			if _, err := fmt.Fprintf(c.Writer, "event: instance\ndata: %s\n\n", data); err != nil {
				slog.Debug("SSE 写入失败", "err", err)
				return
			}
			c.Writer.Flush()
		}
	}
}
