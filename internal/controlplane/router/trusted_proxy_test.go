package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestSetup_DoesNotTrustForwardedFor 未配置反向代理时，客户端不得伪造 X-Forwarded-For。
func TestSetup_DoesNotTrustForwardedFor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := Setup(&Services{}, "test-secret")
	r.GET("/client-ip", func(c *gin.Context) {
		c.String(http.StatusOK, c.ClientIP())
	})

	req := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	req.RemoteAddr = "198.51.100.9:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.77")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "198.51.100.9", recorder.Body.String())
}
