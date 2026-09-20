package embed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 清洁克隆 / 未 embed-web 时：dist 无 index.html，路由仍可注册并回退占位 HTML，不 panic。
func TestRegisterStaticRoutes_FallbackIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterStaticRoutes(r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `<div id="root"></div>`)
	require.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
}

func TestRegisterStaticRoutes_APINotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterStaticRoutes(r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.True(t, strings.Contains(w.Body.String(), "NOT_FOUND") || strings.Contains(w.Body.String(), "接口不存在"))
}
