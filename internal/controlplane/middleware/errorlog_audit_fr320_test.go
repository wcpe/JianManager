package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAudit_RecordsFailureWithError 失败操作也记审计并带响应错误内容（FR-321）。
func TestAudit_RecordsFailureWithError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotSuccess bool
	var gotErr string
	r := gin.New()
	r.Use(Audit(AuditConfig{
		RecordFunc: func(_ uint, _, _, _, _, _ string, success bool, errMsg string) {
			gotSuccess, gotErr = success, errMsg
		},
	}))
	r.POST("/api/v1/instances/42/start", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MEM_GATE", "message": "节点可用内存不足"})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/instances/42/start", nil))

	if gotSuccess {
		t.Fatal("4xx 应记 success=false")
	}
	if !strings.Contains(gotErr, "内存不足") {
		t.Fatalf("审计应带错误内容，实际 %q", gotErr)
	}
}

// TestAudit_SuccessKeepsTrue 成功操作 success=true、error 为空（FR-321 兼容旧语义）。
func TestAudit_SuccessKeepsTrue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotSuccess bool
	gotErr := "sentinel"
	r := gin.New()
	r.Use(Audit(AuditConfig{
		RecordFunc: func(_ uint, _, _, _, _, _ string, success bool, errMsg string) {
			gotSuccess, gotErr = success, errMsg
		},
	}))
	r.POST("/api/v1/instances/42/start", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/instances/42/start", nil))
	if !gotSuccess || gotErr != "" {
		t.Fatalf("成功应 success=true 且无错误，实际 %v %q", gotSuccess, gotErr)
	}
}

// TestErrorLog_CapturesRespBody respCaptureWriter 截获响应体且不破坏下游输出（FR-320）。
func TestErrorLog_CapturesRespBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ErrorLog())
	r.POST("/api/v1/x", func(c *gin.Context) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "E", "message": "boom"})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/x", nil))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "boom") {
		t.Fatalf("中间件不得破坏响应：code=%d body=%s", w.Code, w.Body.String())
	}
}
