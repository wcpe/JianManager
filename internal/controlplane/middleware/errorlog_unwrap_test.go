package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestErrorCapturePreservesStreamingWriteDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ErrorLog())
	r.GET("/stream", func(c *gin.Context) {
		if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			c.String(http.StatusInternalServerError, "%v", err)
			return
		}
		c.String(http.StatusOK, "deadline supported")
	})
	server := httptest.NewServer(r)
	defer server.Close()
	response, err := http.Get(server.URL + "/stream")
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(raw))
}
