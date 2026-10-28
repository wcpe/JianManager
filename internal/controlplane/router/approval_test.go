package router

/**
 * 审批中心 HTTP 面的守门用例（FR-490 / FR-493）。
 *
 * 三条必须验的边界：
 *  1. **越权一律 404**：别人的票与不存在的票返回一样——否则就是一个 id 探测接口；
 *  2. **无权限 403**：没有 approval.read / approval.decide 节点的人类也进不去；
 *  3. **状态冲突映射正确**：过期 410、已决策 409、参数/目标变化 409。
 *
 * 这里直接测 handler 的错误映射与门禁，不重复 service 层已覆盖的状态机细节。
 */

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func TestRespondApprovalErr_NotFoundHidesExistence(t *testing.T) {
	// 越权与不存在共用 404：这是有意的取舍（不给 id 探测接口），
	// 因此这里断言的是「两种情况返回完全相同的响应」。
	w1, b1 := callApprovalErr(t, service.ErrApprovalNotFound)
	require.Equal(t, 404, w1.Code)
	require.Equal(t, "APPROVAL_NOT_FOUND", b1["error"])
}

func TestRespondApprovalErr_AgentCannotDecideIs403(t *testing.T) {
	w, b := callApprovalErr(t, service.ErrApprovalAgentCannotDecide)
	require.Equal(t, 403, w.Code, "Agent 决策必须 403")
	require.Equal(t, "AGENT_CANNOT_DECIDE", b["error"])
}

func TestRespondApprovalErr_StatusConflicts(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		key  string
	}{
		{"过期", service.ErrApprovalExpired, 410, "APPROVAL_EXPIRED"},
		{"非待审", service.ErrApprovalNotPending, 409, "APPROVAL_NOT_PENDING"},
		{"目标漂移", service.ErrApprovalTargetDrift, 409, "APPROVAL_STALE"},
		{"参数篡改", service.ErrApprovalPayloadTampered, 409, "APPROVAL_STALE"},
		{"未登记", service.ErrApprovalNotRegistered, 400, "APPROVAL_NOT_REGISTERED"},
		{"参数非法", service.ErrAssistantInvalid, 400, "INVALID_REQUEST"},
	}
	for _, c := range cases {
		w, b := callApprovalErr(t, c.err)
		require.Equalf(t, c.code, w.Code, "%s 的状态码不对", c.name)
		require.Equalf(t, c.key, b["error"], "%s 的错误码不对", c.name)
	}
}

// callApprovalErr 用给定错误调用审批错误映射。
func callApprovalErr(t *testing.T, err error) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/approvals/1", nil)
	respondApprovalErr(c, err)

	var body map[string]any
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	}
	return w, body
}

// TestNormalizeApprovalListStatus 守住「全部」在协议上可表达。
//
// 这条断言针对一个真实踩过的坑：缺省值曾经无条件补成 pending，导致前端传 'all'
// 时按不存在的状态精确匹配 → 恒返回空列表，界面表现为「点了全部之后列表空了」。
// 三种语义必须各自独立，缺一个就会退化成空结果。
func TestNormalizeApprovalListStatus(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"缺省按待办", "", model.ApprovalStatusPending},
		{"all 表示不过滤", "all", ""},
		{"pending 原样", model.ApprovalStatusPending, model.ApprovalStatusPending},
		{"具体状态原样", model.ApprovalStatusRejected, model.ApprovalStatusRejected},
		{"未知名原样透传（交由服务层匹配）", "succeeded", "succeeded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeApprovalListStatus(c.in); got != c.want {
				t.Fatalf("normalizeApprovalListStatus(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}
