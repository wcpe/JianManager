package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestClientDistGuard_ReservesSlotsForOtherIPs 单一来源不能占满全部 BUSY 并发槽。
func TestClientDistGuard_ReservesSlotsForOtherIPs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	entered := make(chan string, 2)
	release := make(chan struct{})
	r := gin.New()
	r.Use(ClientDistGuard(nil, 100, 100, 8))
	r.GET("/artifact", func(c *gin.Context) {
		entered <- c.ClientIP()
		<-release
		c.Status(http.StatusOK)
	})

	serve := func(ip string) chan *httptest.ResponseRecorder {
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/artifact", nil)
			req.RemoteAddr = ip + ":1234"
			recorder := httptest.NewRecorder()
			r.ServeHTTP(recorder, req)
			result <- recorder
		}()
		return result
	}

	first := serve("198.51.100.1")
	require.Equal(t, "198.51.100.1", <-entered)

	secondSameIP := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/artifact", nil)
	req.RemoteAddr = "198.51.100.1:4321"
	r.ServeHTTP(secondSameIP, req)
	require.Equal(t, http.StatusTooManyRequests, secondSameIP.Code)

	otherIP := serve("198.51.100.2")
	require.Equal(t, "198.51.100.2", <-entered)
	close(release)
	require.Equal(t, http.StatusOK, (<-first).Code)
	require.Equal(t, http.StatusOK, (<-otherIP).Code)
}
