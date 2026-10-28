package assistantmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestInitialize_FallsBackAcrossVersions 服务端拒绝新版本时必须降级重试（FR-517）。
//
// 不同上游实现的版本差异很大：只声明一版时，老服务端会直接拒绝握手，
// 而表现是「探测显示协议错误」——真实原因只是版本不匹配。
func TestInitialize_FallsBackAcrossVersions(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		v, _ := req.Params["protocolVersion"].(string)
		seen = append(seen, v)
		// 只接受最老的一版，其余明确拒绝（-32602 invalid params）。
		if v != "2024-10-07" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				// 回显请求 id：真实服务端都这么做，硬编码 id 会掩盖客户端是否校验 id。
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32602, "message": "unsupported protocolVersion"},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"protocolVersion": "2024-10-07", "serverInfo": map[string]any{"name": "old"}},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("应降级成功，实际 %v（尝试过 %v）", err, seen)
	}
	if len(seen) < 2 {
		t.Fatalf("应尝试多个版本，实际只试了 %v", seen)
	}
	if c.negotiatedVersion != "2024-10-07" {
		t.Fatalf("协商版本应记录为 2024-10-07，实际 %q", c.negotiatedVersion)
	}
}

// TestInitialize_DoesNotRetryOnAuthFailure 鉴权失败不得当成版本问题重试。
//
// 把鉴权失败也重试，会让一次失败变成四次超时，并把「密钥错」这个真实原因埋掉。
func TestInitialize_DoesNotRetryOnAuthFailure(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.Initialize(context.Background()); err == nil {
		t.Fatal("401 应失败")
	}
	if calls != 1 {
		t.Fatalf("鉴权失败只应尝试 1 次，实际 %d 次", calls)
	}
}

// TestDoWithRetry_RetriesOn429ThenSucceeds 429 应退避重试并最终成功。
//
// 直接测 doWithRetry 而不经 Initialize：后者还有「多版本 × 重试」的叠乘，
// 会把「是否重试」这个断言埋在时间预算里，测不出真正想测的东西。
func TestDoWithRetry_RetriesOn429ThenSucceeds(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "1")
			// 必须显式声明空体：不写 Content-Length 时客户端会一直等 body 结束，
			// 表现为「请求挂住」而不是「收到 429」。
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"protocolVersion": mcpProtocolVersion},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL,
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	var out map[string]any
	start := time.Now()
	if err := c.doWithRetry(req, 1, &out); err != nil {
		t.Fatalf("429 后重试应成功，实际 %v", err)
	}
	if calls != 3 {
		t.Fatalf("应尝试 3 次（前两次 429），实际 %d", calls)
	}
	// 退避总时长必须**有上界**：两次退避理论约 400ms + 800ms，抖动上限 +25%，
	// 留足余量后卡 5s。这条断言是必需的——只验「重试了 3 次」的话，
	// 「抖动把时长算成纳秒²」（几十万秒）这种缺陷会被漏掉，因为调用最终确实成功了，
	// 只是挂到超时为止。
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("退避总时长应远小于 5s，实际 %v（抖动/退避时长计算疑似有误）", elapsed)
	}
	t.Logf("两次退避共耗时 %v", elapsed)
}

// TestIsProtocolVersionRejected 只有「版本相关」的错误才降级。
func TestIsProtocolVersionRejected(t *testing.T) {
	if !isProtocolVersionRejected(&rpcError{Code: -32602, Message: "invalid params"}) {
		t.Fatal("-32602 应视为可降级")
	}
	if !isProtocolVersionRejected(&rpcError{Code: -32000, Message: "Unsupported protocolVersion"}) {
		t.Fatal("文本提到版本应视为可降级")
	}
	if isProtocolVersionRejected(&rpcError{Code: -32000, Message: "internal error"}) {
		t.Fatal("与版本无关的错误不应降级")
	}
	if isProtocolVersionRejected(ErrAuthRejected) {
		t.Fatal("鉴权失败不应降级")
	}
}
