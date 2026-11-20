package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 高频命令糖必须走用户侧路由（/api/v1/*），不得回到 Agent 侧（/api/v1/agent/*）。
//
// 两套端点的凭证体系是分离的：/api/v1/agent/* 只认 jmat_，而 jm 登录拿的是 jmut_。
// 早先这些糖错走了 agent 路由，`jm whoami` 直接报「需要有效的 Agent Token」——
// 而 `jm api` 因为直连任意路径反倒正常，把问题掩盖住了。
//
// 只检查引号内的路径字面量（即真实调用），跳过注释：注释里正需要写清楚
// 「不要用 /api/v1/agent」这类说明，若连注释一起扫就会自相矛盾地判红。
func TestCommandsUseUserFacingRoutes(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	files := []string{"commands.go", "api.go", "auth.go", "config_cmd.go", "skill.go"}
	scanned := 0
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(wd, name))
		if err != nil {
			continue // 文件可能尚未创建（如未来拆分）
		}
		scanned++
		for _, lit := range stringLiterals(string(body)) {
			if strings.Contains(lit, "/api/v1/agent") {
				t.Errorf("%s 仍以字面量引用了 Agent 路由 %q；jm 用的是 jmut_ 用户令牌，应改走 /api/v1/*", name, lit)
			}
		}
	}
	assert.Positive(t, scanned, "应至少扫描到一个实现文件；否则路径拼错，本用例会假通过")
}

// stringLiterals 抽出源码里双引号包裹的字符串字面量，顺带跳过行注释。
//
// 够用即可：本用例只需要区分「注释里提到」与「代码里真的用了」。
func stringLiterals(src string) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		parts := strings.Split(line, `"`)
		// 奇数下标即引号内的内容。
		for i := 1; i < len(parts); i += 2 {
			out = append(out, parts[i])
		}
	}
	return out
}

// 每个糖命令都必须真的打到用户侧端点（防止实现改了但路径写错）。
func TestSugarCommandsHitUserRoutes(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/auth/me") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"username": "u", "isPlatformAdmin": false, "nodes": []any{},
			})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	defer ts.Close()

	cfg := Config{Token: "jmut_ok", BaseURL: ts.URL, Output: "json"}
	c := newTestClient(ts, "jmut_ok")

	require.NoError(t, runWhoami(c, cfg, nil))
	require.NoError(t, runList(c, cfg, []string{"nodes"}))
	require.NoError(t, runList(c, cfg, []string{"instances"}))
	require.NoError(t, runInstance(c, cfg, []string{"status", "5"}))

	assert.Equal(t, []string{
		"/api/v1/auth/me",
		"/api/v1/nodes",
		"/api/v1/instances",
		"/api/v1/instances/5",
	}, got)

	for _, p := range got {
		assert.NotContains(t, p, "/agent/", "糖命令不应命中 Agent 路由: %s", p)
	}
}
