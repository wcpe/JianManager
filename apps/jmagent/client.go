package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client 是面向 CP Agent 运维 API 的薄 HTTP 客户端（标准库 net/http）。
// 不引入 gRPC / 数据库 / Worker 依赖。
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// newClient 按配置构造客户端。httpClient 可在测试中替换。
func newClient(cfg Config) *Client {
	return &Client{
		baseURL: cfg.CPURL,
		token:   cfg.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// APIError 表示 CP 返回的非 2xx 业务/鉴权错误。
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d (%s)", e.Status, e.Code)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// asAPIError 从 error 链中提取 *APIError。
func asAPIError(err error) (*APIError, bool) {
	if err == nil {
		return nil, false
	}
	if ae, ok := err.(*APIError); ok {
		return ae, true
	}
	return nil, false
}

// apiErrorBody 匹配 CP 统一错误 JSON：{"error":"...","message":"..."}。
type apiErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// doJSON 发起请求，成功时返回响应体字节；非 2xx 返回 *APIError（403 保留中文 message）。
// path 形如 /api/v1/agent/whoami；query 可为 nil。
func (c *Client) doJSON(method, path string, query url.Values) ([]byte, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("Control Plane 基址为空")
	}
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("构造请求 URL 失败: %w", err)
	}
	if query != nil {
		u.RawQuery = query.Encode()
	}

	req, err := http.NewRequest(method, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 CP 失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8MiB 上限
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}

	ae := &APIError{Status: resp.StatusCode}
	var eb apiErrorBody
	if json.Unmarshal(body, &eb) == nil {
		ae.Code = eb.Error
		ae.Message = eb.Message
	}
	// 403/硬拒绝：优先用 CP 中文 message；空则给稳定兜底文案
	if ae.Message == "" {
		switch resp.StatusCode {
		case http.StatusForbidden:
			ae.Message = "操作被拒绝（403）：写白名单/scope 不足或命中硬拒绝面"
		case http.StatusUnauthorized:
			ae.Message = "未授权（401）：Agent Token 无效、已吊销或已过期"
		case http.StatusNotFound:
			ae.Message = "资源不存在（404）"
		default:
			msg := strings.TrimSpace(string(body))
			if msg == "" {
				ae.Message = fmt.Sprintf("请求失败（HTTP %d）", resp.StatusCode)
			} else {
				ae.Message = fmt.Sprintf("请求失败（HTTP %d）: %s", resp.StatusCode, msg)
			}
		}
	}
	return nil, ae
}

// get / post 便捷方法。
func (c *Client) get(path string, query url.Values) ([]byte, error) {
	return c.doJSON(http.MethodGet, path, query)
}

func (c *Client) post(path string) ([]byte, error) {
	return c.doJSON(http.MethodPost, path, nil)
}
