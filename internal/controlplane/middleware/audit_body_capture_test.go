package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 回归（客户端分发「上传过大文件报错」）：审计中间件不得缓冲大/流式请求体——
// multipart 上传与 octet-stream 分片此前会被 io.ReadAll 整体读进内存再回填（大文件 → OOM）；
// 超大 JSON 同样跳过缓冲（handler 不受影响，仅审计 detail 记占位）。
func TestAuditSkipsBodyCaptureForUploads(t *testing.T) {
	gin.SetMode(gin.TestMode)

	run := func(t *testing.T, contentType string, body []byte, handler gin.HandlerFunc) *gin.Context {
		t.Helper()
		r := gin.New()
		r.Use(Audit(AuditConfig{})) // RecordFunc nil：只走请求体捕获路径，不落库
		r.POST("/upload", handler)
		req := httptest.NewRequest("POST", "/upload", bytes.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		req.ContentLength = int64(len(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 200, w.Code)
		return nil
	}

	// 1) multipart：handler 能完整解析表单（未被中间件消费）。
	t.Run("multipart intact", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "big.bin")
		_, _ = fw.Write([]byte(strings.Repeat("x", 64<<10)))
		require.NoError(t, mw.Close())

		var gotName string
		var gotSize int64
		run(t, mw.FormDataContentType(), buf.Bytes(), func(c *gin.Context) {
			fh, err := c.FormFile("file")
			require.NoError(t, err, "multipart 体必须完整可用")
			gotName = fh.Filename
			gotSize = fh.Size
			c.Status(200)
		})
		require.Equal(t, "big.bin", gotName)
		require.Positive(t, gotSize)
	})

	// 2) octet-stream（分片 PUT 原始字节）：handler 读到的字节与发送一致。
	t.Run("octet-stream intact", func(t *testing.T) {
		payload := bytes.Repeat([]byte{0xAB}, 1<<20)
		run(t, "application/octet-stream", payload, func(c *gin.Context) {
			raw, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			require.Equal(t, payload, raw, "分片字节流必须原样到达 handler")
			c.Status(200)
		})
	})

	// 3) 超大 JSON（> 8MiB）：跳过缓冲，handler 仍能正常绑定。
	t.Run("oversized json intact", func(t *testing.T) {
		big := map[string]any{"blob": strings.Repeat("y", 9<<20)}
		raw, err := json.Marshal(big)
		require.NoError(t, err)
		run(t, "application/json", raw, func(c *gin.Context) {
			var parsed map[string]any
			require.NoError(t, c.ShouldBindJSON(&parsed), "超大 JSON 必须原样到达 handler")
			require.Len(t, parsed["blob"], 9<<20)
			c.Status(200)
		})
	})

	// 4) 常规 JSON（< 8MiB）：仍被缓冲用于审计脱敏（行为回归保障）。
	t.Run("normal json still captured", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"name": "ok", "password": "secret"})
		var captured string
		r := gin.New()
		r.Use(Audit(AuditConfig{RecordFunc: func(_ uint, _, _, _, detail, _ string, _ bool, _ string) {
			captured = detail
		}}))
		r.POST("/api/v1/users", func(c *gin.Context) { c.Status(200) }) // 路径须映射到审计动作（user.create）
		req := httptest.NewRequest("POST", "/api/v1/users", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(len(raw))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 200, w.Code)
		require.Contains(t, captured, `"name":"ok"`)
		require.Contains(t, captured, `"password":"***"`, "常规体仍需脱敏捕获")
	})
}
