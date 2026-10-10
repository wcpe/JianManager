package vlrange

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestPR61HTTPErrorBodySharesByteBudget(t *testing.T) {
	var actual atomic.Uint64
	client := pr61ByteClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: &pr61Body{Reader: strings.NewReader(strings.Repeat("x", 4096)), read: &actual}}, nil
	})
	res, err := client.Stats(context.Background(), query.AuthoritativeRange{Key: queryKey()}, query.RangeQuery{Budget: query.Budget{MaxBytes: 1}})
	require.Error(t, err)
	var status *vlsup.StatusError
	require.True(t, errors.As(err, &status))
	require.Equal(t, uint64(1), actual.Load(), "错误描述不能先额外读取1MiB")
	require.Equal(t, actual.Load(), res.Bytes)
	require.Len(t, status.Body, 1)
}

// 自动重定向会在查询消费回调之外偷偷预读旧响应，预算入口必须拒绝自动跟随。
func TestPR61HTTPRedirectCannotBypassByteBudget(t *testing.T) {
	var actual atomic.Uint64
	var calls atomic.Int32
	client := pr61ByteClient(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path == "/redirected" {
			return &http.Response{StatusCode: 200, Body: &pr61Body{Reader: strings.NewReader("{\"_jm_count\":1}\n"), read: &actual}}, nil
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://127.0.0.1:9428/redirected"}},
			Body: &pr61Body{Reader: strings.NewReader(strings.Repeat("x", 4096)), read: &actual}}, nil
	})
	res, err := client.Stats(context.Background(), query.AuthoritativeRange{Key: queryKey()}, query.RangeQuery{Budget: query.Budget{MaxBytes: 1}})
	t.Logf("真实读入=%d 计账=%d 请求数=%d", actual.Load(), res.Bytes, calls.Load())
	require.Error(t, err)
	require.Equal(t, uint64(1), actual.Load())
	require.Equal(t, actual.Load(), res.Bytes)
	require.Equal(t, int32(1), calls.Load(), "预算入口不能暗中跟随再发请求")
}

func TestPR61StreamZeroAndOverflowBudgets(t *testing.T) {
	var calls atomic.Int32
	var actual atomic.Uint64
	client := pr61ByteClient(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: &pr61Body{Reader: strings.NewReader("{\"_jm_count\":1}\n"), read: &actual}}, nil
	})
	n, err := client.vl.StreamBudget(context.Background(), "/query", url.Values{}, 0, func(io.Reader) error { return nil })
	require.Error(t, err)
	require.Zero(t, n)
	require.Zero(t, calls.Load())
	res, err := client.Stats(context.Background(), query.AuthoritativeRange{Key: queryKey()}, query.RangeQuery{Budget: query.Budget{MaxBytes: math.MaxUint64}})
	require.NoError(t, err)
	require.Len(t, res.Points, 1, "uint64转int64溢出不得被读成空成功")
	require.Equal(t, actual.Load(), res.Bytes)
}

type pr61CancelBody struct {
	reader io.Reader
	cancel context.CancelFunc
	closed atomic.Bool
}

func (r *pr61CancelBody) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.cancel()
	return n, err
}
func (r *pr61CancelBody) Close() error { r.closed.Store(true); return nil }

func TestPR61StreamCancellationClosesBodyAndKeepsBytes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &pr61CancelBody{reader: strings.NewReader("{\"_jm_count\":1}\n"), cancel: cancel}
	client := pr61ByteClient(t, func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: body}, nil })
	res, err := client.Stats(ctx, query.AuthoritativeRange{Key: queryKey()}, query.RangeQuery{Budget: query.Budget{MaxBytes: 100}})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, body.closed.Load())
	require.Positive(t, res.Bytes, "取消后的预读不能退款")
}
