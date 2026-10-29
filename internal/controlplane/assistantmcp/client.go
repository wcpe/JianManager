// Package assistantmcp 外部 MCP 客户端（Streamable HTTP 传输）。
//
// 方向明确：`internal/controlplane/mcp` 是把**本平台能力**暴露给 Agent 的 MCP **服务端**；
// 本包相反，它作为**客户端**去调用外部 MCP 服务。两者语义相反，传输层不可复用。
//
// 支持范围（spec §3 冻结）：
//   - 仅 Streamable HTTP：`POST <endpoint>`，响应可为 `application/json` 或**请求范围 SSE**；
//   - 明确不支持：stdio 子进程、旧式双端点 SSE、OAuth 动态注册。
//
// 外部上游是**不可信输入**（spec §7）：工具描述、annotations、返回值都可能含注入指令。
// 因此本包只做「按协议收发 + 限额」，**不做任何信任决策**——风险分类与启用由管理员
// 显式设定，工具结果的文本永远只当数据。
package assistantmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 协议预算默认值（spec §5.2）。
const (
	// DefaultCallTimeout 单次工具调用上限。
	DefaultCallTimeout = 30 * time.Second
	// DefaultDiscoverTimeout 单页发现上限。
	DefaultDiscoverTimeout = 20 * time.Second
	// DefaultResponseMax 单次响应体上限（1 MiB）；超出即失败，**不截断后当成功**。
	DefaultResponseMax = 1 << 20
	// DefaultEventLineMax 单行事件上限；防「一个不换行的巨大 data 行」吃满内存。
	DefaultEventLineMax = 256 << 10
	// DefaultIdleTimeout 流式响应两帧之间的空闲上限。
	DefaultIdleTimeout = 15 * time.Second
	// MaxToolsPerConnection 单连接工具数上限（spec §5.2：超出整次发现失败）。
	MaxToolsPerConnection = 200
	// discoverPageSize 发现分页大小（上游 `tools/list` 的 cursor 分页）。
	discoverPageSize = 50
)

// 错误分类（供上层映射成 spec §6 的错误码）。
var (
	// ErrUnreachable 上游不可达（网络/连接层）。
	ErrUnreachable = errors.New("上游 MCP 不可达")
	// ErrProtocol 响应不合 JSON-RPC。
	ErrProtocol = errors.New("上游 MCP 协议错误")
	// ErrTooLarge 响应体超过上限。
	ErrTooLarge = errors.New("上游 MCP 响应过大")
	// ErrToolsLimit 工具数超过单连接上限（整次发现失败，不部分提交）。
	ErrToolsLimit = errors.New("上游 MCP 工具数超过上限")
	// ErrAuthRejected 上游拒绝认证（HTTP 401/403）。
	//
	// 与 ErrUnreachable 分开的理由是**排障方向完全不同**：凭据错要去改凭据，
	// 而「不可达」会把人引去看网络、DNS、防火墙。此前的实现把 401 也归成不可达。
	ErrAuthRejected = errors.New("上游 MCP 拒绝认证")
)

// mcpProtocolVersion 我们**首选**声明的 MCP 协议版本。
//
// 服务端可能回降到它支持的版本，协商结果记在 Client.negotiatedVersion，
// 后续请求按**协商值**声明。选 2024-11-05 是因为它已定义 Streamable HTTP
// 与 Mcp-Session-Id 语义，且是兼容面最广的一版。
const mcpProtocolVersion = "2024-11-05"

// mcpProtocolVersionsSupported 我们能够对话的全部协议版本，按「从新到旧」排列。
//
// ## 为什么需要一份列表而不是只声明一版
//
// 不同上游实现的版本差异很大：新服务器要求 2025-06-18（加了结构化输出与
// elicitation），老实现只认 2024-11-05，还有只实现 2024-10-07 早期草案的。
// 只声明一版时，服务端要么回降（我们能接受），要么直接拒绝握手——
// 后者表现为「探测显示协议错误」，而真实原因只是版本不匹配。
//
// ## 为什么按「从新到旧」试而不是先试最老
//
// 新版本包含更多能力（如结构化输出）。先试新的能拿到更好的语义；
// 回降到老版本只在必要时发生。**但只在服务端明确拒绝时**才降级（见 Initialize），
// 不因为「没回 protocolVersion 字段」就降——那会把一个正常的老服务端当成失败。
var mcpProtocolVersionsSupported = []string{
	"2025-06-18",
	"2025-03-26",
	"2024-11-05",
	"2024-10-07",
}

// rpcRequest JSON-RPC 2.0 请求。
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse JSON-RPC 2.0 响应。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// rpcError JSON-RPC 错误体。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("上游返回错误 %d: %s", e.Code, e.Message)
}

// ToolSpec 上游工具描述。
//
// `Annotations` 保留但**不用于风险判定**（spec §4.2：风险由管理员确认，
// 不由 annotations 推导）——上游可以随便写 readOnlyHint。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]any  `json:"annotations,omitempty"`
}

// Client 一个外部 MCP 连接的客户端。
//
// 它绑定 endpoint 与凭据，**不持有任何用户身份**：凭据只属于该连接（spec §7 第 4 条），
// 绝不转发面板 JWT / Cookie / 其它连接的凭据。
type Client struct {
	// httpClient 必须由受保护出站（Guard）构造：端点准入与 IP 固定在那一层完成。
	// 这里不允许调用方塞一个裸 http.Client——那会绕过全部 SSRF 防护。
	httpClient *http.Client
	endpoint   string
	// authHeader 注入到上游的头（authStyle=header 时用自定义头名）。
	authHeaderName string
	// authValue 凭据明文（调用方负责解密；本结构不落盘、不打日志）。
	authValue string
	// authStyle none / header / bearer。
	authStyle string

	responseMax   int64
	eventLineMax  int
	idleTimeout   time.Duration
	callTimeout   time.Duration
	discoverLimit time.Duration

	// nextID JSON-RPC 请求 id 生成（单连接串行使用，无需原子）。
	nextID int

	// sessionID / negotiatedVersion MCP 会话协商（规范: Streamable HTTP）。
	//
	// 为什么必须有：`initialize` 的响应头会下发 `Mcp-Session-Id`，规范要求后续
	// 每个请求都回带它。不回带时**只做 initialize 的探测会通过、而 tools/list 被拒**
	// （服务器通常回 400 missing-session），表现成「探测正常、发现必失败」，
	// 而错误还被归成「不可达」，把排障引向网络。这正是我们漏掉的一处规范要求。
	//
	// 单连接串行使用，无需加锁（与 nextID 同一前提）。
	sessionID         string
	negotiatedVersion string
}

// Options 客户端构造参数。
type Options struct {
	HTTPClient *http.Client
	Endpoint   string
	// AuthStyle none / header / bearer。
	AuthStyle string
	// AuthHeaderName 仅 authStyle=header 时使用，默认 Authorization。
	AuthHeaderName string
	// AuthValue 凭据明文；authStyle=none 时忽略。
	AuthValue string
	// ResponseMax / EventLineMax / IdleTimeout / CallTimeout / DiscoverTimeout
	// 为零时取默认值。
	ResponseMax     int64
	EventLineMax    int
	IdleTimeout     time.Duration
	CallTimeout     time.Duration
	DiscoverTimeout time.Duration
}

// New 构造客户端。
func New(opts Options) (*Client, error) {
	if opts.HTTPClient == nil {
		// 强制要求调用方传受保护出站：忘记传就会静默绕过 SSRF 防护，必须直接拒绝。
		return nil, fmt.Errorf("%w: 必须由受保护出站构造 HTTP 客户端", ErrProtocol)
	}
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("%w: endpoint 为空", ErrProtocol)
	}
	c := &Client{
		httpClient:     opts.HTTPClient,
		endpoint:       endpoint,
		authStyle:      normalizeAuthStyle(opts.AuthStyle),
		authHeaderName: strings.TrimSpace(opts.AuthHeaderName),
		authValue:      opts.AuthValue,
		responseMax:    opts.ResponseMax,
		eventLineMax:   opts.EventLineMax,
		idleTimeout:    opts.IdleTimeout,
		callTimeout:    opts.CallTimeout,
		discoverLimit:  opts.DiscoverTimeout,
	}
	if c.responseMax <= 0 {
		c.responseMax = DefaultResponseMax
	}
	if c.eventLineMax <= 0 {
		c.eventLineMax = DefaultEventLineMax
	}
	if c.idleTimeout <= 0 {
		c.idleTimeout = DefaultIdleTimeout
	}
	if c.callTimeout <= 0 {
		c.callTimeout = DefaultCallTimeout
	}
	if c.discoverLimit <= 0 {
		c.discoverLimit = DefaultDiscoverTimeout
	}
	if c.authHeaderName == "" {
		c.authHeaderName = "Authorization"
	}
	return c, nil
}

func normalizeAuthStyle(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "header":
		return "header"
	case "bearer":
		return "bearer"
	default:
		return "none"
	}
}

// Initialize 握手（`initialize`），返回上游声明的协议版本与服务信息。
//
// ## 多版本回退
//
// 按 `mcpProtocolVersionsSupported`（从新到旧）逐个尝试，**只在服务端明确拒绝**
// （JSON-RPC error，通常是「不支持的 protocolVersion」）时才降级到下一版。
//
// 为什么「没回 protocolVersion」不算失败：老实现常直接忽略该字段。
// 把它当失败会让我们把一个可用服务端判成不兼容——那比不降级更糟，
// 因为管理员看到的是「协议错误」，而实际只是对端没回这个可选字段。
//
// 为什么只对特定错误降级：网络失败、鉴权失败、5xx 与版本无关，
// 对它们重试四个版本只会让一次失败变成四次超时，还会把真实原因埋掉。
func (c *Client) Initialize(ctx context.Context) (map[string]any, error) {
	// 总预算：多版本 × 退避会叠乘（4 版 × 4 次尝试 = 16 次请求），
	// 无上限时一次「全面限流」的上游会让探测挂到分钟级。
	// 因此整次握手共用一个上限，用尽即返回最后一个错误。
	deadline := time.Now().Add(c.discoverLimit + assistantMCPRetryBudget)
	var lastErr error
	for i, version := range mcpProtocolVersionsSupported {
		if time.Now().After(deadline) {
			break
		}
		// 每一版各自一份超时预算：共用一份时，前一版的退避会吃掉后一版的预算，
		// 表现为「降级还没试完就超时」——那不是版本不兼容，却被报成版本问题。
		out, err := c.initializeWith(ctx, version)
		if err == nil {
			if i > 0 {
				// 记下实际协商到的版本，便于排障时回答「为什么降级了」。
				c.negotiatedVersion = version
			}
			return out, nil
		}
		lastErr = err
		if !isProtocolVersionRejected(err) {
			// 与版本无关的失败：立刻返回，不把一次失败放大成四次。
			return nil, err
		}
	}
	return nil, lastErr
}

// initializeWith 用指定版本声明一次握手。
func (c *Client) initializeWith(ctx context.Context, version string) (map[string]any, error) {
	var out map[string]any
	// 单次请求仍有硬超时（否则无网络响应时会一直挂住），但预算取
	// **单次调用上限 + 退避预留**：429 退避需要 400ms×2^n 的时间，
	// 若与单次请求共用一个 discoverLimit，退避会把它吃光，
	// 于是「上游限流」表现成「超时」——两者处置完全不同
	// （前者等一会儿再来，后者查网络）。
	attemptBudget := c.discoverLimit + assistantMCPRetryBudget
	callCtx, cancel := context.WithTimeout(ctx, attemptBudget)
	defer cancel()
	err := c.call(callCtx, "initialize", map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "jianmanager-assistant",
			"version": "1.0.0",
		},
	}, &out)
	if err != nil {
		return nil, err
	}
	// 记录协商结果：服务端可能回降到它支持的版本，后续请求要按**协商值**声明，
	// 而不是继续声明我们希望的版本。回包缺该字段时保持空（不发该头）。
	if v, ok := out["protocolVersion"].(string); ok {
		if v = strings.TrimSpace(v); v != "" {
			c.negotiatedVersion = v
		}
	}
	return out, nil
}

// DiscoverTools 分页拉取工具清单（spec §4.3）。
//
// 超额语义（spec §4.3 冻结）：工具数 > MaxToolsPerConnection 时**整次发现失败**，
// 不落库、不部分提交。部分提交会让「目录里有什么」取决于分页顺序，
// 用户看到残缺集合却以为完整；保留旧目录同样危险（管理员以为已更新）。
func (c *Client) DiscoverTools(ctx context.Context) ([]ToolSpec, error) {
	ctx, cancel := context.WithTimeout(ctx, c.discoverLimit)
	defer cancel()

	var all []ToolSpec
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Tools      []ToolSpec `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := c.call(ctx, "tools/list", params, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Tools...)
		if int64(len(all)) > MaxToolsPerConnection {
			// 一旦超限立即失败：继续拉完再判会白跑几十页，且上游可能借此拖时间。
			return nil, fmt.Errorf("%w: 已发现 %d 个工具，上限 %d", ErrToolsLimit, len(all), MaxToolsPerConnection)
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			// 防御上游返回同一个 cursor 造成死循环。
			break
		}
		cursor = page.NextCursor
	}
	if int64(len(all)) > MaxToolsPerConnection {
		return nil, fmt.Errorf("%w: 共 %d 个工具，上限 %d", ErrToolsLimit, len(all), MaxToolsPerConnection)
	}
	return all, nil
}

// CallTool 调用一个上游工具，返回结果文本。
//
// 结果**只当不可信数据**：本函数不做任何清洗或解释，也不据此改变任何策略。
func (c *Client) CallTool(ctx context.Context, remoteName string, args map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{
		"name":      remoteName,
		"arguments": args,
	}, &out); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	if out.IsError {
		return b.String(), fmt.Errorf("%w: 上游工具返回错误", ErrProtocol)
	}
	return b.String(), nil
}

// call 发一次 JSON-RPC 请求并解析响应（兼容 JSON 与请求范围 SSE 两种响应形态）。
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.nextID++
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: c.nextID, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("%w: 构造请求失败: %v", ErrProtocol, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: 构造 HTTP 请求失败: %v", ErrProtocol, err)
	}
	req.Header.Set("Content-Type", "application/json")
	// 同时声明两种响应形态都接受：Streamable HTTP 允许上游按需选择。
	req.Header.Set("Accept", "application/json, text/event-stream")
	// 会话协商：服务端在 initialize 响应头下发会话 id，后续请求必须回带。
	// 未协商时（首次 initialize）这两个头自然不发。
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	if c.negotiatedVersion != "" {
		req.Header.Set("MCP-Protocol-Version", c.negotiatedVersion)
	}
	c.applyAuth(req)

	return c.doWithRetry(req, c.nextID, out)
}

// doWithRetry 发请求，并对 **429 / 503** 做有界退避重试。
//
// ## 为什么只重试这两个状态
//
// 429 表示「你太快了，等一下再来」，503 表示「暂时不可用」——两者都是
// **明确的、可自愈的**信号。其它 4xx 是请求本身有问题，重试只会重复同样的错误；
// 网络超时也不重试（可能已经在服务端执行，重试会变成重复副作用）。
//
// ## 为什么退避要有上限与抖动
//
// 无上限会让一个持续限流的上游把请求挂到超时；无抖动会让多个并发调用
// 在同一时刻一起重试，把限流变成尖峰。这里用「指数 + 小幅抖动 + 总预算」。
func (c *Client) doWithRetry(req *http.Request, expectID int, out any) error {
	// 请求体需要可重放：退避后要用同一份 body 再发一次。
	// （调用方总是传 []byte，构造新请求是安全的。）
	var bodyBytes []byte
	if req.Body != nil {
		if req.GetBody != nil {
			if rc, err := req.GetBody(); err == nil {
				bodyBytes, _ = io.ReadAll(rc)
				_ = rc.Close()
			}
		} else {
			// 无 GetBody（手工构造的请求）：**直接把当前 body 读出来缓存**。
			// 不这么做的话，第一次发送会消耗掉 body，重试时发空请求——
			// 表现为「上游返回奇怪的错误」，而真正原因是重试根本没带内容。
			bodyBytes, _ = io.ReadAll(req.Body)
			_ = req.Body.Close()
			// 首次尝试也要用缓存重建，否则第一次就是空 body。
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
	}

	var lastErr error
	delay := assistantMCPRetryBaseDelay
	for attempt := 0; attempt <= assistantMCPMaxRetries; attempt++ {
		attemptReq := req
		if attempt > 0 && bodyBytes != nil {
			attemptReq = req.Clone(req.Context())
			attemptReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
		retryable, err := c.doOnce(attemptReq, expectID, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == assistantMCPMaxRetries {
			return err
		}
		// 抖动最多 +25%：避免多个并发调用同时重试形成尖峰。
		// 注意单位：jitter 必须按 **delay 的比例**算，不能把两个 Duration 相乘
		// （那是纳秒²，会得到几十万秒的等待）。mcpJitter 返回 0..jitterBuckets-1。
		jitter := delay / 4 * time.Duration(mcpJitter(len(bodyBytes))) / mcpJitterBuckets
		select {
		case <-time.After(delay + jitter):
		case <-req.Context().Done():
			return fmt.Errorf("%w: %v", ErrUnreachable, req.Context().Err())
		}
		delay *= 2
	}
	return lastErr
}

// doOnce 发一次请求；返回 retryable 表示「这个失败值得重试」。
func (c *Client) doOnce(req *http.Request, expectID int, out any) (retryable bool, err error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 捕获会话 id：服务端可能在任何响应里下发（通常 initialize）。以最新值为准。
	if sid := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); sid != "" {
		c.sessionID = sid
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 401/403 与「连不上」是两件事：凭据错时管理员该去改凭据，
		// 而不是去查网络。分成 UNREACHABLE 会把排障方向带错。
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return false, fmt.Errorf("%w: 上游拒绝认证（HTTP %d），请检查凭据", ErrAuthRejected, resp.StatusCode)
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			// 可重试：把 Retry-After 透出到错误里，便于排障时知道上游要求的等待时间。
			if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
				return true, fmt.Errorf("%w: 上游限流（HTTP %d，Retry-After: %s）", ErrUnreachable, resp.StatusCode, ra)
			}
			return true, fmt.Errorf("%w: 上游限流（HTTP %d）", ErrUnreachable, resp.StatusCode)
		}
		return false, fmt.Errorf("%w: 上游返回 HTTP %d", ErrUnreachable, resp.StatusCode)
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(ct, "text/event-stream"):
		// 请求范围 SSE：读到本请求的终止事件即结束，**不是长连接**（spec §5.1）。
		return false, c.readSSEResponse(resp.Body, expectID, out)
	default:
		return false, c.readJSONResponse(resp.Body, expectID, out)
	}
}

// applyAuth 按 authStyle 注入凭据。
//
// 只注入该连接自己的凭据（spec §7 第 4 条）：本函数没有任何读取面板 JWT / Cookie
// 的路径，也不接受「从别处借一个凭据」。
func (c *Client) applyAuth(req *http.Request) {
	switch c.authStyle {
	case "bearer":
		if c.authValue != "" {
			req.Header.Set("Authorization", "Bearer "+c.authValue)
		}
	case "header":
		if c.authValue != "" {
			req.Header.Set(c.authHeaderName, c.authValue)
		}
	default:
		// none：不带任何凭据。
	}
}

// readJSONResponse 读单条 JSON-RPC 响应。
func (c *Client) readJSONResponse(body io.Reader, expectID int, out any) error {
	limited := io.LimitReader(body, c.responseMax+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("%w: 读取响应失败: %v", ErrUnreachable, err)
	}
	if int64(len(raw)) > c.responseMax {
		// 超限即失败，**不截断后当成功**（截断的 JSON 会解析出错，但更糟的是
		// 某些体积无关的响应截断后仍能解析，于是「部分数据」被当成完整结果）。
		return fmt.Errorf("%w: 响应超过 %d 字节", ErrTooLarge, c.responseMax)
	}
	return c.decodeRPC(raw, expectID, out)
}

// readSSEResponse 读请求范围 SSE，取最后一个带 result 的帧。
func (c *Client) readSSEResponse(body io.Reader, expectID int, out any) error {
	reader := newEventReader(body, c.eventLineMax, c.responseMax)
	for {
		data, ok, err := reader.Next()
		if err != nil {
			return err
		}
		if !ok {
			// 流结束但没拿到结果：按协议错误处理（不能假装成功）。
			return fmt.Errorf("%w: SSE 流结束但未收到 JSON-RPC 结果", ErrProtocol)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		var probe rpcResponse
		if err := json.Unmarshal(data, &probe); err != nil {
			// 单帧不是 JSON-RPC：跳过（上游可能先发 event 通知）。
			continue
		}
		if probe.ID == 0 {
			// 通知帧（无 id）：跳过，不当作结果。
			continue
		}
		// **必须是本请求的响应**（能力面对齐）。
		//
		// 为什么一定要校验 id：MCP 允许服务端在同一条流里发起**反向请求**
		// （`sampling/createMessage`、`roots/list`、`elicitation/create`）。
		// 这些都是「服务器问你」的消息，带自己的 id、但没有 result/error 字段。
		// 不校验 id 就会把它们当成本次调用的结果——而 finishRPC 解一个既无 result
		// 又无 error 的响应，会解出**零值**（例如 tools/list 得到空工具列表），
		// 于是表现为「上游一切正常但工具数为 0」这种极难定位的静默失败。
		if probe.ID != expectID {
			continue
		}
		return c.finishRPC(probe, out)
	}
}

// decodeRPC 解析单条 JSON 响应。
func (c *Client) decodeRPC(raw []byte, expectID int, out any) error {
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("%w: 响应不是合法 JSON-RPC", ErrProtocol)
	}
	if resp.ID != expectID {
		return fmt.Errorf("%w: 响应 id %d 与请求 id %d 不匹配", ErrProtocol, resp.ID, expectID)
	}
	return c.finishRPC(resp, out)
}

// finishRPC 把响应体解到 out。
func (c *Client) finishRPC(resp rpcResponse, out any) error {
	if resp.Error != nil {
		// **保留原始 *rpcError 在错误链上**（%w 而非 %v）：
		// 版本降级判定要按错误码区分「上游拒绝这个 protocolVersion」与
		// 「鉴权/网络失败」。用 %v 会把错误码拍平成文本，判定只能靠猜字符串。
		return fmt.Errorf("%w: %w", ErrProtocol, resp.Error)
	}
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		// 结果结构与预期不符：这是**上游契约不一致**，不能当成功。
		// 例如 tools/list 返回的 tools 不是数组时，静默当「没有工具」会让管理员
		// 以为上游没工具，而实际是解析错了。
		return fmt.Errorf("%w: 结果结构不符: %v", ErrProtocol, err)
	}
	return nil
}

// Ping 连通性探测（`initialize` 成功即视为可达）。
func (c *Client) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	if _, err := c.Initialize(ctx); err != nil {
		return time.Since(start), err
	}
	return time.Since(start), nil
}

// isProtocolVersionRejected 判断错误是否属于「服务端不接受这个协议版本」。
//
// ## 为什么必须窄判
//
// 只有版本不匹配才应该降级重试。把网络失败/鉴权失败也当成版本问题，
// 会让一次失败放大成四次超时，并且最终错误变成最后一版的报错——
// 真实原因（比如密钥错）被彻底埋掉。
//
// ## 判据
//
// JSON-RPC 的 `-32602`（invalid params）与 `-32600`（invalid request）常被用来
// 报「不支持的 protocolVersion」；部分实现用 `-32000`（server error）并附文字。
// 因此同时看**错误码**与**错误文本是否提到版本**，两者任一命中即视为可降级。
func isProtocolVersionRejected(err error) bool {
	if err == nil {
		return false
	}
	var rpc *rpcError
	if errors.As(err, &rpc) {
		switch rpc.Code {
		case -32602, -32600:
			return true
		}
		return mentionsProtocolVersion(rpc.Message)
	}
	// 非 JSON-RPC 错误（HTTP 状态、解码失败等）：只有当文本明确指向版本时才降级。
	return mentionsProtocolVersion(err.Error())
}

// mentionsProtocolVersion 判断文本是否在说版本不兼容（英文/中文两种常见措辞）。
func mentionsProtocolVersion(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"protocolversion", "protocol version", "协议版本", "unsupported version", "version not supported",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// 429/503 退避重试的参数。
const (
	// assistantMCPRetryBaseDelay 首次退避时长。取 400ms：足够让上游的限流窗口
	// 松开一点，又不至于让一次工具调用明显变慢。
	assistantMCPRetryBaseDelay = 400 * time.Millisecond
	// assistantMCPMaxRetries 重试次数上限。3 次（共 4 次尝试）在总预算内
	// 覆盖了绝大多数限流抖动，又不会让一个持续限流的上游把请求挂死。
	assistantMCPMaxRetries = 3
	// assistantMCPRetryBudget 退避所需的额外预算。
	// 400ms + 800ms + 1600ms ≈ 2.8s，取 5s 留抖动与调度余量。
	assistantMCPRetryBudget = 5 * time.Second
)

// mcpJitter 用输入长度派生一个小抖动种子。
//
// 为什么不用 math/rand：这里不需要统计随机性，只需要「不同请求的抖动不完全一致」。
// 用内容长度做种子足够打散并发重试，且不引入全局随机源（可测、可复现）。
// mcpJitterBuckets 抖动档位数：把 delay 的 1/4 切成这么多档随机取一档。
const mcpJitterBuckets = 7

// mcpJitter 返回 0..mcpJitterBuckets-1 的确定性档位。
//
// 用 seed 而非真随机：同一 seed 得到同一档位，测试可复现；
// 不同请求的 seed（请求体长度）不同，因此仍能打散并发重试。
// **不要返回 time.Duration**——调用方要按比例乘 delay，返回时长会变成时长²。
func mcpJitter(seed int) int {
	if seed < 0 {
		seed = -seed
	}
	return seed % mcpJitterBuckets
}
