package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const s3DownloadDrainLimitForTest int64 = 64 << 10

type s3BudgetBody struct {
	remaining         int64
	readBytes         atomic.Int64
	closes            atomic.Int64
	closed            chan struct{}
	started           chan struct{}
	closeOnce         sync.Once
	startOnce         sync.Once
	cancelOnce        sync.Once
	cancelOnRead      context.CancelFunc
	blocked           bool
	readErr, closeErr error
}

func newS3BudgetBody(size int64) *s3BudgetBody {
	return &s3BudgetBody{remaining: size, closed: make(chan struct{}), started: make(chan struct{})}
}

func (b *s3BudgetBody) Read(dst []byte) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	if b.blocked {
		<-b.closed
		return 0, errors.New("构造的关闭后读错误")
	}
	if b.readErr != nil {
		return 0, b.readErr
	}
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n := len(dst)
	if int64(n) > b.remaining {
		n = int(b.remaining)
	}
	b.remaining -= int64(n)
	b.readBytes.Add(int64(n))
	for i := 0; i < n; i++ {
		dst[i] = 'x'
	}
	if b.cancelOnRead != nil {
		b.cancelOnce.Do(b.cancelOnRead)
	}
	return n, nil
}

func (b *s3BudgetBody) Close() error {
	b.closes.Add(1)
	b.closeOnce.Do(func() { close(b.closed) })
	return b.closeErr
}

type s3BudgetTransport struct {
	status int
	body   io.ReadCloser
	called atomic.Bool
}

func (t *s3BudgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.called.Store(true)
	return &http.Response{StatusCode: t.status, Header: make(http.Header), Body: t.body, Request: req}, nil
}

func newS3BudgetStore(t *testing.T, status int, body io.ReadCloser) (*S3ObjectStore, *s3BudgetTransport) {
	t.Helper()
	store, err := NewS3ObjectStore(Config{Endpoint: "example.com", Bucket: "test-bucket", AccessKey: "test-key", SecretKey: "test-secret", UseSSL: true})
	require.NoError(t, err)
	transport := &s3BudgetTransport{status: status, body: body}
	store.backend.client = &http.Client{Transport: transport}
	return store, transport
}

func TestS3DownloadErrorBodyBudgetBoundsDrainAndCloses(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, size := range []int64{0, 127, s3DownloadDrainLimitForTest, (4 << 20) + 17} {
			t.Run(fmt.Sprintf("http_%d_bytes_%d", status, size), func(t *testing.T) {
				body := newS3BudgetBody(size)
				store, _ := newS3BudgetStore(t, status, body)
				reader, err := store.Download(context.Background(), "constructed-object")
				actual := body.readBytes.Load()
				t.Logf("http_status=%d response_bytes=%d actual_application_read_bytes=%d close_calls=%d", status, size, actual, body.closes.Load())
				require.Nil(t, reader)
				require.EqualError(t, err, fmt.Sprintf("S3 下载失败: HTTP %d", status))
				want := size
				if want > s3DownloadDrainLimitForTest {
					want = s3DownloadDrainLimitForTest
				}
				require.Equal(t, want, actual, "错误响应只能有限排空，不得先完整读取再报错")
				require.Equal(t, int64(1), body.closes.Load())
			})
		}
	}
}

func TestS3DownloadErrorBodyBudgetPreservesStatusOnReadAndCloseFailure(t *testing.T) {
	for _, name := range []string{"read_failure", "close_failure"} {
		t.Run(name, func(t *testing.T) {
			body := newS3BudgetBody(512)
			if name == "read_failure" {
				body.readErr = errors.New("构造的读取失败")
			} else {
				body.closeErr = errors.New("构造的关闭失败")
			}
			store, _ := newS3BudgetStore(t, http.StatusForbidden, body)
			reader, err := store.Download(context.Background(), "constructed-object")
			require.Nil(t, reader)
			require.EqualError(t, err, "S3 下载失败: HTTP 403")
			require.Equal(t, int64(1), body.closes.Load())
			require.LessOrEqual(t, body.readBytes.Load(), s3DownloadDrainLimitForTest)
		})
	}
}

func TestS3DownloadErrorBodyBudgetHonorsCancellation(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(map[bool]string{false: "during_read", true: "before_read"}[before], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := newS3BudgetBody(256 << 10)
			if before {
				cancel()
			} else {
				body.cancelOnRead = cancel
			}
			store, transport := newS3BudgetStore(t, http.StatusServiceUnavailable, body)
			reader, err := store.Download(ctx, "constructed-object")
			require.Nil(t, reader)
			require.Error(t, err)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			if transport.called.Load() {
				require.EqualError(t, err, "S3 下载失败: HTTP 503")
				require.Equal(t, int64(1), body.closes.Load())
				if before {
					require.Zero(t, body.readBytes.Load())
				} else {
					require.LessOrEqual(t, body.readBytes.Load(), s3DownloadDrainLimitForTest)
				}
			} else {
				require.Zero(t, body.readBytes.Load())
			}
		})
	}
}

func TestS3DownloadErrorBodyBudgetCancellationClosesBlockedReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := newS3BudgetBody(0)
	body.blocked = true
	store, _ := newS3BudgetStore(t, http.StatusForbidden, body)
	result := make(chan error, 1)
	go func() { _, err := store.Download(ctx, "constructed-object"); result <- err }()
	select {
	case <-body.started:
	case <-time.After(2 * time.Second):
		t.Fatal("未开始读取错误响应")
	}
	cancel()
	select {
	case err := <-result:
		require.EqualError(t, err, "S3 下载失败: HTTP 403")
	case <-time.After(2 * time.Second):
		_ = body.Close()
		<-result
		t.Fatal("取消后没有关闭正在阻塞的错误响应体")
	}
	require.Zero(t, body.readBytes.Load())
	require.Equal(t, int64(1), body.closes.Load())
}

func TestS3DownloadErrorBodyBudgetDoesNotConsumeSuccessfulBody(t *testing.T) {
	body := newS3BudgetBody(127)
	store, _ := newS3BudgetStore(t, http.StatusOK, body)
	reader, err := store.Download(context.Background(), "constructed-object")
	require.NoError(t, err)
	require.NotNil(t, reader)
	require.Zero(t, body.readBytes.Load())
	require.Zero(t, body.closes.Load())
	n, err := io.Copy(io.Discard, reader)
	require.NoError(t, err)
	require.Equal(t, int64(127), n)
	require.NoError(t, reader.Close())
	require.Equal(t, int64(1), body.closes.Load())
}
