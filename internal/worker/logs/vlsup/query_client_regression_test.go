package vlsup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestQueryEndpointsForwardExtraFilters 锤死「用户筛选必须下发给 VL」。
//
// # 为什么这条必须存在
//
// 2026-11-06 审查发现：`FieldNames` / `FieldValues` / `Streams` / `StreamFieldNames` /
// `StreamFieldValues` 五个聚合端点的签名里**根本没有 filter 形参**，`q.Filter`
// （用户选的 level / keyword / source）从未进请求。
//
// 后果不是报错，而是「静默返回错数据」：用户选 `level=ERROR` 后看分面，
// 拿到的是**全量**级别分布，而响应里 `complete=true`。
//
// 这种错只能靠断言「请求里真的带了 extra_filters」来守——
// 断言返回值是没用的（返回值在过滤丢失时也完全合法）。
func TestQueryEndpointsForwardExtraFilters(t *testing.T) {
	const filter = `level:ERROR`
	var gotFilters []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotFilters = append(gotFilters, r.Form.Get("extra_filters"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{BaseURL: srv.URL, AllowNonLoopback: true})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	calls := []struct {
		name string
		fn   func() error
	}{
		{"FieldNames", func() error { _, err := c.FieldNames(ctx, "*", filter, start, end); return err }},
		{"FieldValues", func() error { _, err := c.FieldValues(ctx, "*", filter, "level", start, end); return err }},
		{"Streams", func() error { _, _, err := c.Streams(ctx, "*", filter, start, end); return err }},
		{"StreamFieldNames", func() error { _, err := c.StreamFieldNames(ctx, "*", filter, start, end); return err }},
		{"StreamFieldValues", func() error {
			_, _, err := c.StreamFieldValues(ctx, "*", filter, "log_source_id", start, end)
			return err
		}},
	}

	for _, call := range calls {
		gotFilters = nil
		if err := call.fn(); err != nil {
			t.Fatalf("%s: %v", call.name, err)
		}
		if len(gotFilters) != 1 {
			t.Fatalf("%s: 期望 1 次请求，得到 %d", call.name, len(gotFilters))
		}
		if gotFilters[0] != filter {
			t.Fatalf("%s: extra_filters 未下发，实际=%q（用户筛选被静默丢弃）", call.name, gotFilters[0])
		}
	}
}

// TestQueryPostUsesQueryClientNotWriteClient 锤死「查询不能走 5s 写入客户端」。
//
// 2026-11-06 审查：`Post` 一律用 `c.hc`（5s），而它承载的 6 个端点全是 `/select/logsql/*`。
// 实测 `field_values` 对 `_msg` 的 180 天窗要 **3.44s**（仅 20 源）——
// 距 5s 只剩 1.5 倍余量，60 源设计目标必然被掐断。
//
// 这里用「短 QueryTimeout + 慢服务端」证明查询端**确实**受 QueryTimeout 控制：
// 若 Post 错误地用了写入客户端（固定 5s），这个用例拿到的会是成功而不是超时。
func TestQueryPostHonoursQueryTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 拖得比 QueryTimeout 长，但远短于写入客户端的 5s。
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"values":[]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{
		BaseURL:          srv.URL,
		AllowNonLoopback: true,
		QueryTimeout:     50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	if _, err := c.FieldValues(context.Background(), "*", "", "level", start, end); err == nil {
		t.Fatal("查询端点必须受 QueryTimeout 控制；拿到 nil 说明 Post 用的是写入客户端（5s）而非查询客户端")
	}

	// 对照：管理类端点仍应用写入客户端，不受 QueryTimeout 影响。
	if _, err := c.Post(context.Background(), "/internal/partition/list", nil); err != nil {
		t.Logf("管理端点不受 QueryTimeout 约束（预期行为）: %v", err)
	}
}

// TestQueryResponseLimitAllowsLargeAggregation 锤死「查询响应不能被 4MiB 卡死」。
//
// 实测 `field_values` 对 `_msg` 的 180 天窗返回 **44.8MiB**。
// 原实现的 4MiB 上限会把它整个判成 `response exceeds limit`——
// 本该可用的聚合结果变成报错。
func TestQueryResponseLimitAllowsLargeAggregation(t *testing.T) {
	// 返回 8MiB，大于旧的 4MiB 上限、小于新的查询上限。
	payload := make([]byte, 0, 8<<20)
	payload = append(payload, `{"values":[`...)
	for len(payload) < 8<<20 {
		payload = append(payload, `{"value":"x","hits":1},`...)
	}
	payload = append(payload, `{"value":"tail","hits":1}]}`...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{BaseURL: srv.URL, AllowNonLoopback: true})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	values, err := c.FieldValues(context.Background(), "*", "", "_msg", start, end)
	if err != nil {
		t.Fatalf("8MiB 的合法聚合响应不得被判超限: %v", err)
	}
	if len(values) == 0 {
		t.Fatal("期望解析出值")
	}
}
