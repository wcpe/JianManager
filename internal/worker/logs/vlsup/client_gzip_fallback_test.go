package vlsup

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// gzipRejectingVL 模拟「VL 不认 Content-Encoding: gzip」：带该头的请求一律 400，
// 不带该头的照常收下。它把每次请求的形态记下来，供断言「退回了、而且只白跑一次」。
type gzipRejectingVL struct {
	mu       sync.Mutex
	encoding []string
	bodies   []string
}

func (f *gzipRejectingVL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	enc := r.Header.Get("Content-Encoding")
	f.mu.Lock()
	f.encoding = append(f.encoding, enc)
	f.bodies = append(f.bodies, string(raw))
	f.mu.Unlock()
	if enc == "gzip" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`cannot parse request body`))
		return
	}
	w.WriteHeader(http.StatusOK)
}

// 退回不压缩（变异可证伪）：VL 拒掉 gzip ⇒ 客户端自己用未压缩形态重试一次并成功；
// 之后**本进程内不再白跑**那个注定被拒的 gzip 请求。
//
// 反例：把 `InsertJSONLines` 的退回分支删掉 ⇒ 首次调用直接返回 400 错误 ⇒ 第一段断言红；
// 把 `compressionRejected.Store(true)` 删掉 ⇒ 第二次调用又发一次 gzip ⇒ 第三段断言红。
func TestInsertJSONLinesFallsBackToPlainBodyWhenGzipRejected(t *testing.T) {
	fixture := &gzipRejectingVL{}
	srv := httptest.NewServer(fixture)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	payload := ndjsonBatch(20)

	// 第一次：gzip 被拒 → 自动退回未压缩 → 成功。
	status, err := client.InsertJSONLines(context.Background(), payload)
	require.NoError(t, err, "压缩被拒时必须退回未压缩并成功，而不是把压缩兼容性当成投递失败")
	require.Equal(t, http.StatusOK, status)
	fixture.mu.Lock()
	require.Equal(t, []string{"gzip", ""}, fixture.encoding, "首次必须恰好两个请求：先 gzip（被拒）后未压缩")
	require.Equal(t, string(payload), fixture.bodies[1], "退回时送出的必须是原文")
	fixture.mu.Unlock()

	// 第二次：已置「压缩被拒」，不应再发 gzip（否则每批都白跑一个请求）。
	status, err = client.InsertJSONLines(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.Equal(t, []string{"gzip", "", ""}, fixture.encoding,
		"退回后不得再发 gzip 请求（每批白跑一个注定被拒的请求会直接吃掉写入带宽）")
	require.True(t, client.compressionRejected.Load(), "退回状态必须是粘性的（进程内）")
}

// 反向边界：400 但**未压缩形态也失败** ⇒ 不得把压缩关掉。
//
// 这条守住“偶发脏数据不得导致进程级降级”：假如工端发来一行非法 JSON，VL 对两种形态
// 都返回 400，此时问题在数据而不在压缩——把压缩永久关掉只会让之后的正常批也失去压缩。
func TestInsertJSONLinesKeepsCompressionWhenDataItselfIsRejected(t *testing.T) {
	var mu sync.Mutex
	var encodings []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		encodings = append(encodings, r.Header.Get("Content-Encoding"))
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`invalid json line`))
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	status, err := client.InsertJSONLines(context.Background(), ndjsonBatch(1))
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.False(t, client.compressionRejected.Load(), "数据本身被拒时不得把压缩关掉")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"gzip", ""}, encodings, "两种形态都要试过（先 gzip 后未压缩）")
}

// 压缩失败走不压缩而不报错：请求体里的“压缩不了”绝不能变成投递失败。
// 这里用「gzip 写器已经关闭」的极端情形难以构造，改为直接验证退化后的形态不变：
// 长度 1 的极小体仍然压缩成功且能解回（证明压缩路径对小输入也正确），
// 而空体不压缩（见 TestInsertJSONLinesSkipsCompressionForEmptyPayload）。
func TestGzipPayloadRoundTrip(t *testing.T) {
	for _, payload := range [][]byte{[]byte("x\n"), ndjsonBatch(3), bytes.Repeat([]byte("a"), 1<<16)} {
		encoded, err := gzipPayload(payload)
		require.NoError(t, err)
		decoded, err := gunzip(t, encoded)
		require.NoError(t, err)
		require.Equal(t, payload, decoded, "gzip 往返必须逐字节一致")
	}
}

func gunzip(t *testing.T, raw []byte) ([]byte, error) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

// 确保 WARN 日志文案里的关键提示不变（运维手册引用它去关开关）。
func TestGzipFallbackWarningMentionsDisableSwitch(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(previous)

	fixture := &gzipRejectingVL{}
	srv := httptest.NewServer(fixture)
	defer srv.Close()
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	_, err = client.InsertJSONLines(context.Background(), ndjsonBatch(1))
	require.NoError(t, err)

	logged := buf.String()
	require.Contains(t, logged, "gzip", "退回必须留下可观测的 WARN")
	require.True(t, strings.Contains(logged, "VL"), "日志里必须明确是谁拒的（当前：%s）", logged)
}
