package vlsup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 回归：查询类请求（Get/Stream）必须走**独立**的查询客户端，不得与写入共用 5s 硬超时。
//
// 事故对照（2026-09-28 生产）：校验查询 `Get .../select/logsql/query` 反复
// `context deadline exceeded (Client.Timeout exceeded while awaiting headers)`——查询耗时随
// 事件时间跨度增长（projection 过滤无索引，可能扫全天），5s 会误杀 → 校验失败 →
// 「日志采集运行时创建失败」→ 采集静默停摆（重启复现）。
// 本用例注入 150ms 查询超时：查询应超时（证明走 hq），而写入仍用默认 5s（应成功）。
func TestQueryTimeoutIsSeparateFromWriteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{BaseURL: srv.URL, QueryTimeout: 150 * time.Millisecond})
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "/health", nil)
	require.Error(t, err, "查询应受 QueryTimeout 约束（走 hq），而不是固定 5s 的写入客户端")

	status, err := c.InsertJSONLines(context.Background(), []byte("{}\n"))
	require.NoError(t, err, "写入仍用默认 5s 客户端，300ms 的插入不应超时")
	require.Equal(t, http.StatusOK, status)
}
