package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAuditMiddleware_PasswordNotRecordedInDetail 复现审计 detail 泄漏凭据的缺陷：
// PUT /users/:id 重置密码（FR-156）经审计中间件记为 user.update，原始请求体含明文新密码，
// 若不脱敏则明文落 audit_logs、审计页可见且随导出（FR-155）外带。
// detail 须掩蔽凭据字段，但保留非敏感字段供运维定位。
func TestAuditMiddleware_PasswordNotRecordedInDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotAction, gotDetail string
	r := gin.New()
	r.Use(Audit(AuditConfig{
		RecordFunc: func(_ uint, action, _, _, detail, _ string, _ bool, _ string) {
			gotAction, gotDetail = action, detail
		},
	}))
	r.PUT("/api/v1/users/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	const plaintext = "S3cret-NewPass!"
	body := `{"role":"admin","password":"` + plaintext + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/5", strings.NewReader(body))
	r.ServeHTTP(httptest.NewRecorder(), req)

	if gotAction != "user.update" {
		t.Fatalf("action = %q, want user.update", gotAction)
	}
	if strings.Contains(gotDetail, plaintext) {
		t.Fatalf("审计 detail 泄漏明文密码: %s", gotDetail)
	}
	if !strings.Contains(gotDetail, "admin") {
		t.Fatalf("审计 detail 应保留非敏感字段（role）供定位: %s", gotDetail)
	}
}

// TestSanitizeAuditDetail 覆盖凭据字段掩蔽规则：大小写/命名变体、嵌套对象、数组元素，
// 以及非 JSON 请求体按原样记录（保持现状，不因脱敏失败丢审计）。
func TestSanitizeAuditDetail(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantGone []string // 掩蔽后不得出现的明文
		wantKeep []string // 须保留的非敏感内容
	}{
		{
			name:     "顶层 password",
			body:     `{"password":"p@ss1","role":"member"}`,
			wantGone: []string{"p@ss1"},
			wantKeep: []string{"member"},
		},
		{
			name:     "命名变体 newPassword/refreshToken/apiSecret",
			body:     `{"newPassword":"np-2","refreshToken":"rt-3","apiSecret":"as-4","note":"keep"}`,
			wantGone: []string{"np-2", "rt-3", "as-4"},
			wantKeep: []string{"keep"},
		},
		{
			name:     "大小写不敏感 Password",
			body:     `{"Password":"P-5"}`,
			wantGone: []string{"P-5"},
		},
		{
			name:     "嵌套对象与数组",
			body:     `{"smtp":{"password":"deep-6"},"items":[{"token":"arr-7","id":9}]}`,
			wantGone: []string{"deep-6", "arr-7"},
			wantKeep: []string{"9"},
		},
		{
			name:     "非 JSON 原样保留",
			body:     `not-json password=raw-8`,
			wantKeep: []string{"not-json password=raw-8"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeAuditDetail([]byte(tt.body))
			for _, s := range tt.wantGone {
				if strings.Contains(got, s) {
					t.Errorf("明文 %q 未被掩蔽: %s", s, got)
				}
			}
			for _, s := range tt.wantKeep {
				if !strings.Contains(got, s) {
					t.Errorf("非敏感内容 %q 丢失: %s", s, got)
				}
			}
		})
	}
}
