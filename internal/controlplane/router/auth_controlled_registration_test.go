package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAuthRegisterRouteIsAbsent 验证匿名入口不会再创建可登录用户。
func TestAuthRegisterRouteIsAbsent(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"username": "anonymous-member",
		"password": "password123",
	}, "")

	require.Equal(t, http.StatusNotFound, w.Code)
}
