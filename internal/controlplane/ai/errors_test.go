package ai

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// 错误分类与脱敏（FR-514 spec §7）。
//
// 这两个性质决定了配置页「测试连接」的可用性：分类错了，管理员查错方向；
// 脱敏漏了，上游回显的密钥就进了日志与截图。

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorCode
	}{
		{http.StatusUnauthorized, CodeUpstreamAuth},
		{http.StatusForbidden, CodeUpstreamAuth},
		{http.StatusTooManyRequests, CodeUpstreamRateLimited},
		{http.StatusBadRequest, CodeUpstreamRejected},
		{http.StatusNotFound, CodeUpstreamRejected},
		{http.StatusUnprocessableEntity, CodeUpstreamRejected},
		{http.StatusInternalServerError, CodeUpstreamError},
		{http.StatusBadGateway, CodeUpstreamError},
		{http.StatusServiceUnavailable, CodeUpstreamError},
	}
	for _, tc := range cases {
		if got := ClassifyStatus(tc.status); got != tc.want {
			t.Errorf("status %d: 期望 %s，实际 %s", tc.status, tc.want, got)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"nil 返回空", nil, ""},
		{"已分类错误优先", NewUpstreamError(CodeUpstreamRateLimited, 429, "限流"), CodeUpstreamRateLimited},
		{"context 超时", context.DeadlineExceeded, CodeUpstreamTimeout},
		{"包装的 context 超时", &url.Error{Op: "Post", Err: context.DeadlineExceeded}, CodeUpstreamTimeout},
		{"工具调用不完整", ErrIncompleteToolCall, CodeToolCallInvalid},
		{"包装的工具调用不完整",
			errors.Join(errors.New("上下文"), ErrIncompleteToolCall), CodeToolCallInvalid},
		{"网络不可达", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, CodeUpstreamUnreachable},
		{"流截断", errors.New("上游流意外结束（既无 [DONE] 也无 finish_reason）"), CodeUpstreamStreamTruncated},
		{"读取流失败", errors.New("读取上游流失败: unexpected EOF"), CodeUpstreamStreamTruncated},
		{"未知归为 upstream_error", errors.New("某种没见过的错误"), CodeUpstreamError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Fatalf("期望 %q，实际 %q", tc.want, got)
			}
		})
	}
}

func TestUpstreamErrorFormat(t *testing.T) {
	e := NewUpstreamError(CodeUpstreamAuth, 401, "密钥无效")
	if got := e.Error(); !strings.Contains(got, "upstream_auth") ||
		!strings.Contains(got, "401") || !strings.Contains(got, "密钥无效") {
		t.Fatalf("错误串应含分类码、状态码与原因，实际 %q", got)
	}
	// 无状态码时不应出现 "HTTP 0"。
	e2 := NewUpstreamError(CodeUpstreamTimeout, 0, "超时")
	if strings.Contains(e2.Error(), "HTTP") {
		t.Fatalf("无状态码时不应渲染 HTTP，实际 %q", e2.Error())
	}
}

func TestRedactUpstreamBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// mustNotContain 掩蔽后不得出现的片段。
		mustNotContain []string
		// mustContain 必须保留的内容（诊断信息不能被一起抹掉）。
		mustContain []string
	}{
		{
			name:           "OpenAI 风格密钥",
			in:             `{"error":{"message":"Incorrect API key provided: sk-abc123def456ghi"}}`,
			mustNotContain: []string{"abc123def456ghi"},
			mustContain:    []string{"Incorrect API key"},
		},
		{
			name:           "Anthropic 风格密钥",
			in:             `invalid x-api-key: sk-ant-api03-XyZ987654321`,
			mustNotContain: []string{"XyZ987654321"},
			mustContain:    []string{"x-api-key"},
		},
		{
			name:           "Bearer 头回显",
			in:             `Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig`,
			mustNotContain: []string{"eyJhbGciOiJIUzI1NiJ9"},
			mustContain:    []string{"Bearer"},
		},
		{
			name:           "Google 风格密钥",
			in:             `{"error":{"message":"API key not valid: AIzaSyD-1234567890abcdef"}}`,
			mustNotContain: []string{"SyD-1234567890abcdef"},
			mustContain:    []string{"API key not valid"},
		},
		{
			name:           "JSON 密钥字段",
			in:             `{"api_key":"totally-secret-value","model":"gpt-4o-mini"}`,
			mustNotContain: []string{"totally-secret-value"},
			mustContain:    []string{"api_key", "gpt-4o-mini"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactUpstreamBody(tc.in)
			for _, s := range tc.mustNotContain {
				if strings.Contains(got, s) {
					t.Fatalf("密钥片段 %q 未被掩蔽，结果 %q", s, got)
				}
			}
			for _, s := range tc.mustContain {
				if !strings.Contains(got, s) {
					t.Fatalf("诊断信息 %q 被误删，结果 %q", s, got)
				}
			}
		})
	}
}

func TestRedactUpstreamBodyKeepsOrdinaryText(t *testing.T) {
	// 不误伤普通文本：模型名写错时，上游会回「model not found: gpt-4x」，
	// 这句话是排查的主要线索，绝不能被掩蔽规则吃掉。
	in := `{"error":{"message":"The model ` + "`gpt-4x`" + ` does not exist or you do not have access to it."}}`
	if got := RedactUpstreamBody(in); got != in {
		t.Fatalf("普通诊断文本不应被改写:\n in=%q\nout=%q", in, got)
	}
}
