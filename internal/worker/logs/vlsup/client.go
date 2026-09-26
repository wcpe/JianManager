package vlsup

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUnauthorized 表示 VL Basic-auth 未通过（401/403）。
var ErrUnauthorized = errors.New("vlsup: unauthorized")

// ErrNotLoopback 表示客户端 base URL 非 localhost（生产强制 localhost 查询）。
var ErrNotLoopback = errors.New("vlsup: client base URL must be loopback")

// Client 是面向 127.0.0.1 VictoriaLogs 实例的 Basic-auth HTTP 查询助手。
type Client struct {
	baseURL  string
	username string
	password string
	hc       *http.Client
}

// ClientOptions 构造 Client。
type ClientOptions struct {
	// BaseURL 例如 http://127.0.0.1:19441 。测试可注入 httptest.Server URL（仍是 127.0.0.1）。
	BaseURL  string
	Username string
	Password string
	// HTTPClient 可注入；nil 时使用默认超时客户端。
	HTTPClient *http.Client
	// AllowNonLoopback 仅测试使用：跳过 loopback 校验。生产必须保持 false。
	AllowNonLoopback bool
}

// NewClient 构造 localhost VL 客户端。BaseURL 可注入以便单元测试。
func NewClient(opts ClientOptions) (*Client, error) {
	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		return nil, fmt.Errorf("vlsup: client baseURL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("vlsup: parse baseURL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("vlsup: unsupported baseURL scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if !opts.AllowNonLoopback && !IsLoopbackHost(host) {
		return nil, fmt.Errorf("%w: %q", ErrNotLoopback, host)
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	return &Client{
		baseURL:  strings.TrimRight(raw, "/"),
		username: opts.Username,
		password: opts.Password,
		hc:       hc,
	}, nil
}

// BaseURL 返回注入的 base URL。
func (c *Client) BaseURL() string { return c.baseURL }

// IsLoopbackHost 报告 host 是否为回环地址（127.0.0.1 / localhost / ::1）。
func IsLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Health 请求 /health；成功（2xx）返回 nil，未授权返回 ErrUnauthorized。
func (c *Client) Health(ctx context.Context) error {
	_, err := c.Get(ctx, "/health", nil)
	return err
}

// Get 以 Basic-auth GET baseURL+path，可选 query。响应体非 2xx 时返回错误。
// 401/403 映射为 ErrUnauthorized。
func (c *Client) Get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("vlsup: nil client")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("vlsup: build request: %w", err)
	}
	if c.username != "" || c.password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vlsup: request %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s status %d", ErrUnauthorized, path, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("vlsup: %s status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// Stream 以 Basic-auth GET 请求并把成功响应流交给消费方。
// 查询接口返回 NDJSON，不能先把完整响应读入内存；调用方负责在 consume 中设置
// 行数、字节数和取消边界。非 2xx 响应仍只读取有界错误正文。
func (c *Client) Stream(ctx context.Context, path string, query url.Values, consume func(io.Reader) error) error {
	if c == nil {
		return fmt.Errorf("vlsup: nil client")
	}
	if consume == nil {
		return fmt.Errorf("vlsup: nil stream consumer")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("vlsup: build request: %w", err)
	}
	if c.username != "" || c.password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("vlsup: request %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: %s status %d", ErrUnauthorized, path, resp.StatusCode)
		default:
			return fmt.Errorf("vlsup: %s status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	return consume(resp.Body)
}

// InsertJSONLines 将规范事件以 NDJSON 投递到 Worker 本地 VL。
// 调用方仍需单独记录请求状态和可见性核验，HTTP 2xx 不构成逐记录耐久证明。
func (c *Client) InsertJSONLines(ctx context.Context, lines []byte) (int, error) {
	if c == nil {
		return 0, fmt.Errorf("vlsup: nil client")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/insert/jsonline", bytes.NewReader(lines))
	if err != nil {
		return 0, fmt.Errorf("vlsup: build insert request: %w", err)
	}
	req.Header.Set("Content-Type", "application/stream+json")
	if c.username != "" || c.password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("vlsup: insert request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, fmt.Errorf("%w: insert status %d", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("vlsup: insert status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.StatusCode, nil
}

// Post calls a localhost management endpoint with Basic auth and returns a
// bounded response. Partition endpoints in VictoriaLogs v1.52.0 require POST.
func (c *Client) Post(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("vlsup: nil client")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, fmt.Errorf("vlsup: build POST request: %w", err)
	}
	if c.username != "" || c.password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vlsup: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if readErr != nil {
		return nil, readErr
	}
	if len(body) > 4<<20 {
		return nil, fmt.Errorf("vlsup: POST %s response exceeds limit", path)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %s status %d", ErrUnauthorized, path, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("vlsup: POST %s status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
