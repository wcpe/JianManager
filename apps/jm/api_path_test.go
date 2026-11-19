package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `jm api` 必须支持三种路径写法，与 gh api 对齐。
//
// 背景：Git Bash / MSYS2 会把以 `/` 开头的参数改写成 Windows 文件路径，
// 用户写 `/api/v1/x` 实际发出的是 `/C:/Program Files/Git/api/v1/x`。
// gh 的做法是不要求用户设环境变量，而是让「省略前导斜杠」直接可用，
// 并在被改写时明确报错。这里守住「三种写法都通」这条契约。
func TestRunAPI_PathForms(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer ts.Close()

	cfg := Config{BaseURL: ts.URL, Token: "jmut_ok", Output: "json"}

	// 省略前导斜杠：推荐写法，也是 Git Bash 下唯一不受改写影响的形式。
	require.NoError(t, runAPI(cfg, []string{"GET", "api/v1/user/tokens"}))
	// 重复前导斜杠：MSYS 不会改写 `//x`，客户端需自行折叠成单斜杠。
	require.NoError(t, runAPI(cfg, []string{"GET", "//api/v1/user/tokens"}))
	// 完整 URL：不经过 baseURL 拼接。
	require.NoError(t, runAPI(cfg, []string{"GET", ts.URL + "/api/v1/user/tokens"}))

	assert.Equal(t, []string{
		"/api/v1/user/tokens",
		"/api/v1/user/tokens",
		"/api/v1/user/tokens",
	}, got, "三种写法都应落到同一路由")
}

// 被 shell 改写时，必须在发请求前拦下并给出可操作提示。
//
// 不拦的后果很隐蔽：请求打到前端兜底路由，拿回一坨 HTML，
// 用户以为是接口没数据，完全联想不到是 shell 干的。
func TestRunAPI_RejectsShellRewrittenPath(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer ts.Close()

	cfg := Config{BaseURL: ts.URL, Token: "jmut_ok", Output: "json"}
	err := runAPI(cfg, []string{"GET", "/C:/Program Files/Git/api/v1/user/tokens"})
	require.Error(t, err)
	assert.False(t, called, "被改写的路径不应真的发出请求")

	msg := err.Error()
	assert.Contains(t, msg, "shell 改写")
	assert.Contains(t, msg, "省略前导斜杠", "提示里要给出解法")
	assert.Contains(t, msg, "api/v1/user/tokens", "提示里应回显原本的 API 路径")
}

// 检测只针对形态明显的改写，不能误伤正常路由。
func TestLooksLikeRewrittenPath(t *testing.T) {
	rewritten := []string{
		"/C:/Program Files/Git/api/v1/x",
		"C:/Tools/api/v1/x",
		"/c/Program Files/Git/api/v1/x",
		"/opt/Program Files/foo/api/v1/x",
	}
	for _, p := range rewritten {
		assert.True(t, looksLikeRewrittenPath(p), "应识别为被改写: %s", p)
	}
	normal := []string{
		"/api/v1/user/tokens",
		"/api/v1/instances/42",
		"/oauth/device/code",
		"/api/v1/nodes/4/maintenance",
	}
	for _, p := range normal {
		assert.False(t, looksLikeRewrittenPath(p), "不应误判为被改写: %s", p)
	}
}

// rewrittenSuffix 要能从改写后的路径里救回 API 部分，供提示回显。
func TestRewrittenSuffix(t *testing.T) {
	assert.Equal(t, "/api/v1/user/tokens",
		rewrittenSuffix("/C:/Program Files/Git/api/v1/user/tokens"))
	assert.Equal(t, "/oauth/device/code",
		rewrittenSuffix("/C:/Program Files/Git/oauth/device/code"))
	// 认不出标记时原样返回，至少不丢用户输入。
	assert.Equal(t, "/weird/path", rewrittenSuffix("/weird/path"))
}
