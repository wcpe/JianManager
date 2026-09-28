package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// newVerifyTestManager 构造只带校验参数的最小 Manager（本包内可直接落字段）。
func newVerifyTestManager(min, max, timeout time.Duration) *Manager {
	m := &Manager{}
	m.verificationTimeout = timeout
	m.verifyBackoffMin = min
	m.verifyBackoffMax = max
	return m
}

func verifyTestEvent() logtypes.Event {
	return logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "inst:1/file", SourceGeneration: "g1", ParserVersion: "v1"},
		logtypes.RecordRange{Start: 0, End: 9},
		"2026-09-28T00:00:00Z", "2026-09-28T00:00:01Z", "INFO", "stdout", "line",
	)
}

// 不可重试的 VL 错误（4xx 语义错误 / 认证失败）必须**立即失败**，不得重试到超时（B1c）。
//
// 事故背景：投影校验原先对任何错误都以固定 200ms 重试到 30 秒超时（最多约 150 次查询/源），
// 生产上 `cannot execute query [...]` 这类 4xx 亦被如此重试，与单源积压叠加把 VL 与磁盘压垮。
func TestVerifyProjectionFailsFastOnPermanentError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("cannot execute query [bad range]"))
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, Username: "u", Password: "p"})
	require.NoError(t, err)

	m := newVerifyTestManager(10*time.Millisecond, 50*time.Millisecond, 5*time.Second)
	start := time.Now()
	err = m.verifyProjection(client, SourceConfig{LogSourceID: "inst:1/file", SourceGeneration: "g1"}, "projection-1", []logtypes.Event{verifyTestEvent()})
	elapsed := time.Since(start)

	require.Error(t, err, "4xx 属不可重试错误，应失败")
	require.Contains(t, err.Error(), "verification failed permanently", "须标注为不可重试失败：%v", err)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "不得重试：应恰好 1 次查询")
	require.Less(t, elapsed, time.Second, "应立即返回，而不是等满校验窗口")
}

// 认证失败同属不可重试（vlsup.ErrUnauthorized 语义）。
func TestVerifyProjectionFailsFastOnUnauthorized(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, Username: "u", Password: "p"})
	require.NoError(t, err)

	m := newVerifyTestManager(10*time.Millisecond, 50*time.Millisecond, 5*time.Second)
	err = m.verifyProjection(client, SourceConfig{LogSourceID: "inst:1/file", SourceGeneration: "g1"}, "projection-1", []logtypes.Event{verifyTestEvent()})

	require.Error(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "401 不得重试")
}

// 数据「尚未可见」属可重试情形，但必须**退避**而非固定间隔轮询（B1c）。
//
// 判据用请求次数：2 秒窗口下，退避 200ms→400ms→800ms→1600ms 的累计时点为
// 0 / 200 / 600 / 1400ms，共 4 次；而原实现固定 200ms 会有约 11 次。断言 ≤6
// 既能证明退避生效，又不依赖精确时序（实现无抖动，次数是确定的）。
func TestVerifyProjectionBacksOffWhileDataNotVisible(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK) // 200 + 空响应 = 尚未可见（complete=false, err=nil）
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, Username: "u", Password: "p"})
	require.NoError(t, err)

	m := newVerifyTestManager(defaultVerifyBackoffMin, defaultVerifyBackoffMax, 2*time.Second)
	start := time.Now()
	err = m.verifyProjection(client, SourceConfig{LogSourceID: "inst:1/file", SourceGeneration: "g1"}, "projection-1", []logtypes.Event{verifyTestEvent()})
	elapsed := time.Since(start)

	require.Error(t, err, "始终不可见应在窗口结束时失败")
	require.Contains(t, err.Error(), "not fully visible before deadline")
	n := atomic.LoadInt32(&calls)
	require.LessOrEqual(t, n, int32(6), "退避后 2 秒窗口内的查询次数应显著低于固定轮询的约 11 次，实测 %d", n)
	require.GreaterOrEqual(t, n, int32(2), "仍须多次尝试（可重试情形不得只试一次），实测 %d", n)
	require.GreaterOrEqual(t, elapsed, 600*time.Millisecond, "应至少经历一次退避等待")
}

// 退避序列本身：指数增长并封顶（纯函数，确定性断言）。
func TestNextVerifyBackoffDoublesAndCaps(t *testing.T) {
	require.Equal(t, defaultVerifyBackoffMin, nextVerifyBackoff(0, 0), "起点为默认最小值")
	require.Equal(t, 400*time.Millisecond, nextVerifyBackoff(200*time.Millisecond, 2*time.Second))
	require.Equal(t, 800*time.Millisecond, nextVerifyBackoff(400*time.Millisecond, 2*time.Second))
	require.Equal(t, 2*time.Second, nextVerifyBackoff(2*time.Second, 2*time.Second), "封顶")
	require.Equal(t, 2*time.Second, nextVerifyBackoff(1600*time.Millisecond, 2*time.Second), "越过上限即取上限")
}

// vlsup.IsPermanent 的判定面：4xx 与认证为永久；传输类/5xx 不是。
func TestVlsupIsPermanentClassification(t *testing.T) {
	require.True(t, vlsup.IsPermanent(&vlsup.StatusError{Path: "/select/logsql/query", Code: 400, Body: "bad"}))
	require.True(t, vlsup.IsPermanent(&vlsup.StatusError{Path: "/x", Code: 422, Body: "bad"}))
	require.False(t, vlsup.IsPermanent(&vlsup.StatusError{Path: "/x", Code: 503, Body: "unavailable"}), "5xx 可瞬时恢复，应可重试")
	require.False(t, vlsup.IsPermanent(nil))
	require.True(t, strings.Contains((&vlsup.StatusError{Path: "/x", Code: 400, Body: "bad"}).Error(), "status 400"),
		"错误文案须保留状态码")
}
