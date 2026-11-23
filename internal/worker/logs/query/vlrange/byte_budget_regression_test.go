package vlrange

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

type pr61Transport func(*http.Request) (*http.Response, error)

func (f pr61Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type pr61Body struct {
	io.Reader
	read *atomic.Uint64
}

func (r *pr61Body) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read.Add(uint64(n))
	return n, err
}
func (*pr61Body) Close() error { return nil }
func pr61ByteClient(t *testing.T, transport pr61Transport) *Client {
	t.Helper()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: "http://127.0.0.1:9428", HTTPClient: &http.Client{Transport: transport}})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	return client
}

// 超大单行和预读必须在读取器边界拦住，不能先读完整行再裁结果。
func TestPR61StreamHardReadBoundary(t *testing.T) {
	for _, budget := range []uint64{1, 64, 1024} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			var actual atomic.Uint64
			client := pr61ByteClient(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: &pr61Body{Reader: strings.NewReader(strings.Repeat("x", 5<<20) + "\n"), read: &actual}}, nil
			})
			consumed := 0
			n, cut, err := client.stream(context.Background(), url.Values{}, budget, 0, func([]byte) error { consumed++; return nil })
			require.NoError(t, err)
			require.True(t, cut)
			require.LessOrEqual(t, actual.Load(), budget)
			require.Equal(t, actual.Load(), n, "预读也要计入真实消费")
			require.Zero(t, consumed)
		})
	}
}

// 解析错误不是退款凭据；八个并行错误响应已读的字节必须保留到下一轮额度。
func TestPR61StatsDecodeErrorsKeepReservationCharges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 12)
	release := make(chan struct{})
	var actual atomic.Uint64
	client := pr61ByteClient(t, func(r *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: &pr61Body{Reader: strings.NewReader(strings.Repeat("x", 14) + "\n"), read: &actual}}, nil
	})
	cat := catalog.New(nil)
	for i := 0; i < 12; i++ {
		key := catalog.PartitionKey{StorageNamespace: fmt.Sprintf("src-%02d", i), UTCDay: "2026-09-22"}
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
		rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "pv-1", CoverageComplete: true, QueryLocationDirID: "hot-1", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 100}}
		require.NoError(t, cat.Put(rec))
	}
	svc := query.NewService(query.NewPlanner(cat, nil), client, "pr61")
	done := make(chan query.StatsResponse, 1)
	go func() { done <- svc.Stats(ctx, query.QueryRequest{Budget: query.Budget{MaxBytes: 128}}, nil) }()
	for i := 0; i < 8; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("未到达八个错误响应屏障")
		}
	}
	close(release)
	select {
	case resp := <-done:
		require.False(t, resp.Coverage.Complete)
	case <-ctx.Done():
		t.Fatal("错误预算调度未收敛")
	}
	require.LessOrEqual(t, actual.Load(), uint64(128), "错误结果清零会反复退款，实际读到180字节")
}

func TestPR61MergedErrorsKeepBytes(t *testing.T) {
	var actual atomic.Uint64
	client := pr61ByteClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: &pr61Body{Reader: strings.NewReader("xxxxxxxxxxxxxx\n"), read: &actual}}, nil
	})
	ranges := []query.AuthoritativeRange{{Key: catalog.PartitionKey{StorageNamespace: "src-a", UTCDay: "2026-09-22"}, Owner: catalog.OwnerHot}}
	res, err := client.SearchMerged(context.Background(), ranges, query.RangeQuery{Budget: query.Budget{MaxBytes: 16}})
	require.Error(t, err)
	require.Equal(t, actual.Load(), res.Bytes, "无合法items也必须保留真实消费")
}
