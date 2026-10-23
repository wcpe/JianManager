package ai

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
)

// 上游错误分类码（FR-514 spec §7）。
//
// 为什么要有稳定分类码，而不是只给一句中文：配置页的「测试连接」按钮需要让管理员
// 一眼分清「密钥错了」「被限流了」「域名解析不到」「被出站准入挡了」——
// 这四类问题的处置动作完全不同（换密钥 / 等一会 / 查网络 / 改 CIDR 放行）。
// 只给一段可能被上游改写的英文错误体，等于让管理员自己猜。
type ErrorCode string

const (
	// CodeUpstreamAuth 上游拒绝认证（401/403）：密钥错误、过期或权限不足。
	CodeUpstreamAuth ErrorCode = "upstream_auth"
	// CodeUpstreamRateLimited 上游限流（429）。
	CodeUpstreamRateLimited ErrorCode = "upstream_rate_limited"
	// CodeUpstreamRejected 上游判为请求不合法（其余 4xx）：
	// 通常是模型标识写错，或该模型不支持所选协议。
	CodeUpstreamRejected ErrorCode = "upstream_rejected"
	// CodeUpstreamError 上游服务端错误（5xx）。
	CodeUpstreamError ErrorCode = "upstream_error"
	// CodeUpstreamTimeout 请求超时（含 context 截止）。
	CodeUpstreamTimeout ErrorCode = "upstream_timeout"
	// CodeUpstreamUnreachable 网络层不可达：DNS / TCP / TLS 失败。
	CodeUpstreamUnreachable ErrorCode = "upstream_unreachable"
	// CodeUpstreamStreamTruncated 流提前结束，未收到正常终止信号。
	CodeUpstreamStreamTruncated ErrorCode = "upstream_stream_truncated"
	// CodeEgressDenied 被受保护出站的准入规则拒绝（私网、元数据地址、重定向等）。
	CodeEgressDenied ErrorCode = "egress_denied"
	// CodeEgressProxyUnsupported 目标需经代理而该模式无法固定已校验 IP。
	CodeEgressProxyUnsupported ErrorCode = "egress_proxy_unsupported"
	// CodeCapabilityUnsupported 模型未声明该能力却要求使用（如 tools）。
	CodeCapabilityUnsupported ErrorCode = "capability_unsupported"
	// CodeToolCallInvalid 工具调用参数不完整或不是 JSON 对象。
	CodeToolCallInvalid ErrorCode = "tool_call_invalid"
)

// UpstreamError 带分类码的上游错误。
//
// Message 是**已脱敏**的人类可读说明（上游错误体已截断）；Code 供程序分支，
// 前端据它决定提示与建议动作，不去解析 Message。
type UpstreamError struct {
	Code    ErrorCode
	Status  int
	Message string
}

func (e *UpstreamError) Error() string {
	if e.Status > 0 {
		return string(e.Code) + "（HTTP " + itoa(e.Status) + "）: " + e.Message
	}
	return string(e.Code) + ": " + e.Message
}

// NewUpstreamError 构造分类错误（供协议适配层与 service 层共用）。
func NewUpstreamError(code ErrorCode, status int, message string) *UpstreamError {
	return &UpstreamError{Code: code, Status: status, Message: message}
}

// itoa 小工具：避免为拼错误串引入 strconv 的重复调用。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// ClassifyStatus 把 HTTP 状态码映射为分类码。
//
// 取舍：403 归 auth 而不是 rejected——上游用 403 表达「这个 key 没有该模型的权限」
// 的情形远多于「请求体不合法」；管理员看到 auth 会去查密钥权限，那个方向更常对。
func ClassifyStatus(status int) ErrorCode {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return CodeUpstreamAuth
	case status == http.StatusTooManyRequests:
		return CodeUpstreamRateLimited
	case status >= 500:
		return CodeUpstreamError
	default:
		return CodeUpstreamRejected
	}
}

// Classify 把任意错误归一成分类码。
//
// 判定顺序即优先级：先看是否已是分类错误，再看 context 是否被取消/超时，
// 然后看网络错误，最后按传输中断特征兜底。都不匹配时归 upstream_error——
// 「未知」也必须给一个可展示的码，不能让 UI 出现空分类。
func Classify(err error) ErrorCode {
	if err == nil {
		return ""
	}
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeUpstreamTimeout
	}
	if errors.Is(err, ErrIncompleteToolCall) {
		return CodeToolCallInvalid
	}
	// 网络层：DNS 解析失败、拨号被拒、TLS 握手失败、以及 url.Error 包裹的超时
	// 都实现 net.Error。放在 context 之后是因为 context 的语义更明确。
	var nerr net.Error
	if errors.As(err, &nerr) {
		if nerr.Timeout() {
			return CodeUpstreamTimeout
		}
		return CodeUpstreamUnreachable
	}
	// 传输中断类：读取流失败、意外 EOF 等。这些不是「上游明确拒绝」，
	// 而是链路在传输中出了问题——对管理员来说处置方向是查网络而非查配置。
	if isTruncatedErr(err) {
		return CodeUpstreamStreamTruncated
	}
	return CodeUpstreamError
}

// isTruncatedErr 判断是否为「流被切断」类错误。
func isTruncatedErr(err error) bool {
	msg := err.Error()
	for _, marker := range []string{"意外结束", "读取上游流失败", "unexpected EOF"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// 上游错误体里疑似凭据的掩蔽规则（FR-514 spec §1「掩蔽疑似凭据」）。
//
// 为什么必须做：上游的 401 错误体经常**回显它收到的那个密钥**（"Incorrect API key
// provided: sk-xxx"）。这段文本会进 message、进前端提示、可能进日志——等于把
// 本来只在服务端内存里流转的凭据写进了可读面。管理员自己知道密钥，但日志与截图不会。
//
// 关于 `Bearer`（踩过的坑）：Go 的 regexp 是 RE2，**不支持**负向前瞻，所以无法写成
// 「authorization 后面跟的不是 Bearer」。而 `Authorization: Bearer eyJ...` 里的
// `Bearer` 本身符合「一串字母」的取值特征，会被 header 规则当成密钥掩成
// `Authorization: *** ***`——诊断词 "Bearer" 就没了。
//
// 解法不是占位符（占位符同样会被 header 规则再掩一次），而是**用值的形态区分**：
// 真实 token 至少含一个非字母字符（`.` `-` `_` 数字）或者很长；而 `Bearer` 是纯字母
// 且只有 6 位。因此把 header 规则的取值要求写成「长度 ≥8」——`Bearer` 恰好被排除，
// 短 token 由前一条前缀规则（sk-/AIza）兜住。
var upstreamSecretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// OpenAI 风格 sk-xxx / Anthropic sk-ant-xxx。
	{regexp.MustCompile(`sk-[A-Za-z0-9._\-]{6,}`), "sk-***"},
	// Google 风格 AIza...
	{regexp.MustCompile(`AIza[A-Za-z0-9._\-]{6,}`), "AIza***"},
	// JSON 里的密钥字段：保留字段名，掩掉值（便于看出「是哪个字段被拒」）。
	{regexp.MustCompile(`(?i)("(?:api[-_]?key|access[-_]?token|secret|authorization)"\s*:\s*")[^"]*(")`), "${1}***${2}"},
	// 「回显密钥」的常见句式：上游把收到的密钥原样写在句子里，键名不以 sk- 等前缀开头时
	// 前面几条都抓不到。这里按**句式**抓「provided: xxx」这类形态，只掩掉冒号后的那段，
	// 保留句式本身（它说明了失败原因）。
	//
	// 这一条是端到端验证里实测发现的：上游返回 `Incorrect API key provided: bad-key` 时，
	// 前缀规则完全失效，密钥原文会原样进 message。
	{regexp.MustCompile(`(?i)(api[ _-]?key[^:]{0,40}:\s*)[A-Za-z0-9._\-]{4,}`), "${1}***"},
	// Authorization: Bearer <token>：整段换成 `Bearer ***`，保留 "Bearer" 这个诊断词。
	// 放在 header 规则之前，且要求 token ≥8 位（Bearer 自带的值是 6 位字母，故不会被这条吃掉）。
	{regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._\-]{8,}`), "${1}***"},
	// x-api-key: xxx / Authorization: xxx。
	// 取值要求 ≥8 位：既覆盖真实 token，又不会把紧随其后的 `Bearer` 当成值（它只有 6 位）。
	{regexp.MustCompile(`(?i)((?:x-api-key|authorization)\s*[:=]\s*)[A-Za-z0-9._\-]{8,}`), "${1}***"},
}

// RedactUpstreamBody 掩蔽上游错误体中的疑似凭据。
//
// 只掩「看起来就是密钥」的片段，不做通用脱敏：把整段错误体清空会让管理员
// 失去「上游到底在抱怨什么」这一关键线索，而这恰恰是排查模型名写错的主要依据。
func RedactUpstreamBody(body string) string {
	out := body
	for _, p := range upstreamSecretPatterns {
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
}
