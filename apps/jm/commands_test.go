package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout 临时劫持 os.Stdout，执行 fn 后返回捕获文本。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// TestRunWhoami_TextAndJSON 覆盖 whoami 成功 text/json 输出。
func TestRunWhoami_TextAndJSON(t *testing.T) {
	// 用户侧身份结构（jmut_ 令牌）：jm 走 /api/v1/auth/me 而非 Agent 的 whoami。
	payload := map[string]any{
		"userId":          float64(7),
		"username":        "ci",
		"role":            float64(10),
		"roleKey":         "platform_admin",
		"isPlatformAdmin": true,
		"nodes":           []any{"instance.read", "node.read"},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/auth/me", r.URL.Path)
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmat_ok")

	// text：用户侧字段
	cfg := Config{Token: "jmut_ok", BaseURL: ts.URL, Output: "text"}
	out := captureStdout(t, func() {
		require.NoError(t, runWhoami(c, cfg, nil))
	})
	assert.Contains(t, out, "username:")
	assert.Contains(t, out, "ci")
	assert.Contains(t, out, "platform_admin")
	assert.Contains(t, out, "isPlatformAdmin:")

	// json：原样返回 CP 响应
	cfg.Output = "json"
	out = captureStdout(t, func() {
		require.NoError(t, runWhoami(c, cfg, nil))
	})
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	assert.Equal(t, "ci", m["username"])
}

// TestRunInstanceStart_Forbidden403 写操作 403：错误为中文且 asAPIError 可断言。
func TestRunInstanceStart_Forbidden403(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/instances/9/start", r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"FORBIDDEN","message":"写白名单/scope 不足或硬拒绝"}`))
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmat_ro")
	cfg := Config{Token: "jmat_ro", BaseURL: ts.URL, Output: "text"}
	err := runInstance(c, cfg, []string{"start", "9"})
	require.Error(t, err)
	ae, ok := asAPIError(err)
	require.True(t, ok)
	assert.Equal(t, 403, ae.Status)
	assert.Contains(t, ae.Message, "硬拒绝")
}

// TestRunInstanceStart_Success 生命周期成功返回 ok。
func TestRunInstanceStart_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/instances/3/restart", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmat_w")
	cfg := Config{Token: "jmat_w", BaseURL: ts.URL, Output: "text"}
	out := captureStdout(t, func() {
		require.NoError(t, runInstance(c, cfg, []string{"restart", "3"}))
	})
	assert.Contains(t, out, "ok:")
	assert.Contains(t, out, "true")
}

// TestRunListInstances_NodeFilter 带 --node 过滤。
func TestRunListInstances_NodeFilter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "2", r.URL.Query().Get("nodeId"))
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "name": "lobby", "status": "RUNNING", "nodeId": 2, "role": "backend"},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmat_ok")
	cfg := Config{Token: "jmat_ok", BaseURL: ts.URL, Output: "text"}
	out := captureStdout(t, func() {
		require.NoError(t, runList(c, cfg, []string{"instances", "--node", "2"}))
	})
	assert.Contains(t, out, "lobby")
	assert.Contains(t, out, "RUNNING")
}

// TestRunListNodes_Empty 空列表 text 提示。
func TestRunListNodes_Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/nodes", r.URL.Path)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmat_ok")
	cfg := Config{Token: "jmat_ok", BaseURL: ts.URL, Output: "text"}
	out := captureStdout(t, func() {
		require.NoError(t, runList(c, cfg, []string{"nodes"}))
	})
	assert.Contains(t, out, "无节点")
}

// TestRunNodeMaintenance_EnterLeave 维护 enter/leave 落到同一端点，靠请求体区分。
//
// 用户侧是单个 `POST /nodes/:id/maintenance` + `{"enabled":bool}`，
// 与 Agent 侧的 `/maintenance/enter|leave` 两个子路径不同；糖的对外用法不变，
// 差异在客户端内部消化，这里正是守这条转换的用例。
func TestRunNodeMaintenance_EnterLeave(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err == nil {
			bodies = append(bodies, b)
		} else {
			bodies = append(bodies, nil)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmut_ok")
	cfg := Config{Token: "jmut_ok", BaseURL: ts.URL, Output: "json"}

	require.NoError(t, runNode(c, cfg, []string{"maintenance", "enter", "4"}))
	require.NoError(t, runNode(c, cfg, []string{"maintenance", "leave", "4"}))

	assert.Equal(t, []string{
		"/api/v1/nodes/4/maintenance",
		"/api/v1/nodes/4/maintenance",
	}, paths)
	require.Len(t, bodies, 2)
	assert.Equal(t, true, bodies[0]["enabled"], "enter 应传 enabled=true")
	assert.Equal(t, false, bodies[1]["enabled"], "leave 应传 enabled=false")
}

// TestRunInstanceStatus_Success status 拉详情。
func TestRunInstanceStatus_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/instances/11", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 11, "uuid": "u-11", "name": "survival", "status": "STOPPED",
			"nodeId": 1, "role": "universal", "type": "minecraft_java", "processType": "daemon",
		})
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmat_ok")
	cfg := Config{Token: "jmat_ok", BaseURL: ts.URL, Output: "text"}
	out := captureStdout(t, func() {
		require.NoError(t, runInstance(c, cfg, []string{"status", "11"}))
	})
	assert.Contains(t, out, "survival")
	assert.Contains(t, out, "STOPPED")
}

// TestRunWhoami_MissingToken 无 token 直接失败，不发请求。
func TestRunWhoami_MissingToken(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	c := newTestClient(ts, "")
	cfg := Config{Token: "", BaseURL: ts.URL, Output: "text"}
	err := runWhoami(c, cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), envToken)
	assert.False(t, called)
}

// TestRunInstance_InvalidID 非数字 id 被本地拒绝。
func TestRunInstance_InvalidID(t *testing.T) {
	c := newClient(Config{BaseURL: "http://127.0.0.1:9", Token: "x"})
	cfg := Config{Token: "x", BaseURL: "http://127.0.0.1:9", Output: "text"}
	err := runInstance(c, cfg, []string{"status", "abc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "正整数")
}

// TestRunList_UnknownTarget 未知 list 目标。
func TestRunList_UnknownTarget(t *testing.T) {
	c := newClient(Config{BaseURL: "http://127.0.0.1:9", Token: "x"})
	cfg := Config{Token: "x", Output: "text"}
	err := runList(c, cfg, []string{"pods"})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "pods") || strings.Contains(err.Error(), "nodes"))
}
