package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Process-wide caps bound the number of open subscriptions, independently from
// the short-request rate limiter. A subscription never creates an unlimited pool.
var liveSlots = struct {
	sync.Mutex
	total int
	users map[uint]int
}{users: make(map[uint]int)}

func acquireLiveSlot(user uint) (func(), bool) {
	liveSlots.Lock()
	defer liveSlots.Unlock()
	if liveSlots.total >= 32 || liveSlots.users[user] >= 2 {
		return nil, false
	}
	liveSlots.total++
	liveSlots.users[user]++
	return func() {
		liveSlots.Lock()
		defer liveSlots.Unlock()
		liveSlots.total--
		liveSlots.users[user]--
		if liveSlots.users[user] == 0 {
			delete(liveSlots.users, user)
		}
	}, true
}

// Live exposes a fetch-readable SSE stream using the same JWT/permission group
// as Search. It never accepts a query-string token. The middleware has already
// authenticated the bearer; claims are inspected only to shorten its lifetime.
func (h *LogFederationHandler) Live(c *gin.Context) {
	if c.Query("source") == "worker" || c.Query("source") == "control_plane" || c.Query("view") == "platform" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "实时流仅适用于实例日志；节点和平台请读取文本窗口"})
		return
	}
	q, ok := h.buildQuery(c)
	if !ok {
		return
	}
	if q.ToUTC != "" || q.Cursor != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "实时流不接受历史结束时间或分页游标"})
		return
	}
	access := getAccess(c)
	release, ok := acquireLiveSlot(access.UserID)
	if !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "RATE_LIMITED", "message": "实时日志连接已达上限"})
		return
	}
	defer release()
	duration := logcoord.LiveSessionDuration
	bearer := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if bearer != "" {
		var claims jwt.RegisteredClaims
		if _, _, err := jwt.NewParser().ParseUnverified(bearer, &claims); err == nil && claims.ExpiresAt != nil {
			if remain := time.Until(claims.ExpiresAt.Time); remain < duration {
				duration = remain
			}
		}
	}
	if duration <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED"})
		return
	}
	budget, duration, err := liveRequestBudget(c, q.Budget, duration)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	q.Budget = budget
	ctx, cancel := context.WithTimeout(c.Request.Context(), duration)
	defer cancel()
	frames := make(chan logcoord.LiveFrame, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- h.coord.Live(ctx, q, func(frame logcoord.LiveFrame) error {
			select {
			case frames <- frame:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
				return logcoord.ErrLiveSlowConsumer
			}
		})
	}()
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
	controller := http.NewResponseController(c.Writer)
	write := func(frame logcoord.LiveFrame) error {
		_ = controller.SetWriteDeadline(time.Now().Add(3 * time.Second))
		raw, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", frame.Type, raw); err != nil {
			return err
		}
		return controller.Flush()
	}
	// A comment flushes HTTP headers immediately, so the browser can show
	// connecting without waiting for the first matching log or a full TCP buffer.
	if _, err := c.Writer.WriteString(": connecting\n\n"); err != nil {
		return
	}
	c.Writer.Flush()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if c.Request.Context().Err() == nil {
				_ = write(logcoord.LiveFrame{Type: "error", Code: "SESSION_EXPIRED", Message: "会话到期，请重新连接；回放窗口有限，断线可能缺行"})
			}
			return
		case err := <-finished:
			if err != nil {
				_ = write(logcoord.LiveFrame{Type: "error", Code: logcoord.LiveErrorCode(err), Message: err.Error()})
			}
			return
		case <-ticker.C:
			if err := h.revalidateLiveAccess(access, q); err != nil {
				_ = write(logcoord.LiveFrame{Type: "error", Code: "FORBIDDEN", Message: "权限已变化，实时日志已停止"})
				return
			}
			if err := write(logcoord.LiveFrame{Type: "heartbeat"}); err != nil {
				return
			}
		case frame := <-frames:
			if err := write(frame); err != nil {
				return
			}
		}
	}
}

func (h *LogFederationHandler) revalidateLiveAccess(initial *service.UserAccess, q logcoord.Query) error {
	if h.authz == nil {
		return errors.New("missing authorization service")
	}
	fresh, err := h.authz.LoadUserAccess(initial.UserID)
	if err != nil {
		return err
	}
	if !fresh.Can("log.read") {
		return logcoord.ErrViewAuthz
	}
	if fresh.UserID != initial.UserID || fresh.AuthVersion != initial.AuthVersion || fresh.RoleKey != initial.RoleKey || fresh.IsPlatformAdmin != initial.IsPlatformAdmin {
		return logcoord.ErrViewAuthz
	}
	if federationPermissionScope(fresh, q.AuthorizedTargetIDs) != q.PermissionScope {
		return logcoord.ErrViewAuthz
	}
	if fresh.IsPlatformAdmin {
		return nil
	}
	ids, constrained, err := h.authz.AccessibleInstanceIDs(fresh)
	if err != nil {
		return err
	}
	if !constrained {
		return logcoord.ErrViewAuthz
	}
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed["inst:"+strconv.FormatUint(uint64(id), 10)] = true
	}
	for _, target := range q.AuthorizedTargetIDs {
		if !allowed[target] {
			return logcoord.ErrViewAuthz
		}
	}
	return nil
}
