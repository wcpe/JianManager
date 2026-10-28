package router

// Agent/Token 身份不得触达逐调用确认端点（FR-516 §7 冻结项）。
//
// ## 为什么这条必须单独验
//
// 「确认」是人与模型之间的授权边界：模型提出要写，人点头才执行。
// 如果 Agent Token 能调决定端点，模型就能自己批准自己——确认卡会退化成装饰。
// 这条边界的实现方式是**结构性**的：确认端点挂在 `protected`（人类 JWT）组下，
// 而 Agent Token 只能访问 `/api/v1/mcp`，两套鉴权在路由树上互不交叉。
//
// 因此本用例断言的是「物理上打不到」，而不是「打到了但被某个 if 拒绝」——
// 前者不会因为后来者改动某个判断而失效，后者会。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAgentToken_CannotReachConfirmationDecide Agent Token 调决定端点必须进不去。
func TestAgentToken_CannotReachConfirmationDecide(t *testing.T) {
	_, r, _, agentPlain, inst := setupMCP(t)

	// 先造出一张真实的 pending 票据（用管理员 + 真实对话路径太重，
	// 这里只需一个存在的 id：即便 id 不存在，鉴权层也必须先拒，
	// 若返回 404 而不是 401/403，说明请求已经越过鉴权层到了业务层）。
	_ = inst

	paths := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/assistant/confirmations/1/decide"},
		{http.MethodGet, "/api/v1/assistant/confirmations/1"},
		// 顺带覆盖同类：待确认列表也在私有数据面内。
		{http.MethodGet, "/api/v1/assistant/runs/1/confirmations"},
	}

	for _, p := range paths {
		req := httptest.NewRequest(p.method, p.path, nil)
		req.Header.Set("Authorization", "Bearer "+agentPlain)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		// 关键判据：必须是鉴权失败（401/403），不能是 200。
		// 若是 404，说明请求已进入业务层、只是没找到对象——那意味着
		// Agent 身份确实越过了鉴权层，这条边界就破了。
		require.NotEqual(t, http.StatusOK, w.Code,
			"%s %s: Agent Token 绝不能成功调用确认端点（模型不能自己批准自己）", p.method, p.path)
		require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, w.Code,
			"%s %s: 应为鉴权失败(401/403)，实际 %d——404 意味着已越过鉴权层",
			p.method, p.path, w.Code)
	}
}

// TestHumanJWT_CanReachConfirmationDecide 人类 JWT 能到达业务层（对照组）。
//
// 没有这条，「上面那个用例恒过」也可能只是因为路由根本没注册。
// 断言 404（对象不存在）而不是 401，恰好证明请求**穿过了鉴权层**。
func TestHumanJWT_CanReachConfirmationDecide(t *testing.T) {
	_, r, adminJWT, _, _ := setupMCP(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assistant/confirmations/999999", nil)
	req.Header.Set("Authorization", "Bearer "+adminJWT)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.NotEqual(t, http.StatusUnauthorized, w.Code,
		"人类 JWT 不应被鉴权层拒绝")
	require.NotEqual(t, http.StatusForbidden, w.Code,
		"人类 JWT 不应被鉴权层拒绝（确认端点的判据是票据归属，不是权限节点）")
	// 404 = 穿过了鉴权层但对象不存在，正是期望。
	require.Equal(t, http.StatusNotFound, w.Code,
		"不存在的票据应回 404（这证明请求确实到达了业务层），实际 %d: %s", w.Code, w.Body.String())
}
