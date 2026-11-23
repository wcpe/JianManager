package router

// 确认端点的错误映射（§7）。
//
// 这组用例针对一个真实缺陷：领域执行失败曾落进通用映射变成 500 INTERNAL_ERROR。
// 那个响应有两个问题——
//   - 对用户：读起来像平台 bug，而实际是「动作已批准、已尝试、执行侧失败」；
//   - 对客户端：500 通常被当作可重试，但票据此时**已消费**，重试只会得到 409。
// 因此这里断言的是**状态码与 retryable 标记**，而不只是「有报错」。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// callWriteConfirmError 用给定的错误调用映射函数并返回响应。
func callWriteConfirmError(t *testing.T, err error) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/assistant/confirmations/1/decide", nil)
	writeConfirmError(c, err)

	var body map[string]any
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	}
	return w, body
}

func TestWriteConfirmError_ToolExecFailedIs502NotRetryable(t *testing.T) {
	// 领域失败必须单独识别：502 + retryable=false。
	// 若有人把它并回通用分支，这里会得到 500 或 400，用例立即变红。
	domainErr := errors.New("节点 acc-node-1 未连接")
	w, body := callWriteConfirmError(t, &service.AssistantToolExecError{Err: domainErr})

	require.Equal(t, http.StatusBadGateway, w.Code,
		"领域执行失败应为 502（请求没问题，执行侧失败），实际 %d", w.Code)
	require.Equal(t, "TOOL_EXEC_FAILED", body["error"])
	require.Equal(t, false, body["retryable"],
		"票据已消费，必须显式告诉调用方不要原样重试")
	// 真实原因必须透出：笼统的「内部错误」会让运维无从定位。
	require.Contains(t, fmt.Sprint(body["message"]), "未连接")
}

func TestWriteConfirmError_UnwrapsToDomainError(t *testing.T) {
	// Unwrap 必须能穿过包装：上层若要按具体领域错误分别处理（如 ErrInstanceRunning
	// 提示「先停止」），不该因为被包装而失去判断能力。
	sentinel := service.ErrAssistantForbidden
	wrapped := &service.AssistantToolExecError{Err: fmt.Errorf("包装: %w", sentinel)}
	require.True(t, errors.Is(wrapped, sentinel), "Unwrap 应让 errors.Is 穿过包装")
	_ = wrapped
}

func TestWriteConfirmError_PreservesTicketSemantics(t *testing.T) {
	// 反向校验：新加的分支不能把原有票据语义吃掉。
	// 这三个是 409/410 家族，若被新分支拦走，客户端会失去「重新出票」与
	// 「动作已执行过」的区分能力。
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"过期", service.ErrAssistantConfirmExpired, http.StatusGone},
		{"已消费", service.ErrAssistantConfirmConsumed, http.StatusConflict},
		{"已失效", service.ErrAssistantConfirmStale, http.StatusConflict},
	}
	for _, c := range cases {
		w, _ := callWriteConfirmError(t, c.err)
		require.Equalf(t, c.want, w.Code, "%s 的状态码不该被新分支改变", c.name)
	}
}
