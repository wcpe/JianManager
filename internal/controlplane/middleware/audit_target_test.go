package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAuditMiddleware_TargetIDFromActualPath 复现审计目标 ID 取到路由模式占位符 `:id` 的缺陷：
// determineTarget 须从**真实请求路径**取实例 ID，而非 c.FullPath() 的 `/instances/:id` 模式，
// 否则审计「目标」列全渲染成 instance#:id，运维无法定位具体实例（FR-015 真机验收发现）。
func TestAuditMiddleware_TargetIDFromActualPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotType, gotID string
	r := gin.New()
	r.Use(Audit(AuditConfig{
		RecordFunc: func(_ uint, _, targetType, targetID, _, _ string, _ bool, _ string) {
			gotType, gotID = targetType, targetID
		},
	}))
	r.POST("/api/v1/instances/:id/start", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/42/start", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if gotType != "instance" {
		t.Fatalf("targetType = %q, want instance", gotType)
	}
	if gotID != "42" {
		t.Fatalf("targetID = %q, want 42（审计目标不应为路由占位符 :id）", gotID)
	}
}
