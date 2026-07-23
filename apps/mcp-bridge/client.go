package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AgentClient 调用 CP Agent 运维 API 的薄 HTTP 客户端（不链 gRPC/DB）。
type AgentClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewAgentClient 创建客户端。httpClient 为 nil 时使用带超时的默认客户端。
func NewAgentClient(baseURL, token string, httpClient *http.Client) *AgentClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &AgentClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: httpClient,
	}
}

// APIError CP 返回的业务/鉴权错误（含 HTTP 状态与中文 message）。
type APIError struct {
	Status  int
	Code    string
	Message string
	Body    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// IsForbidden 是否为 403（策略拒绝）。
func (e *APIError) IsForbidden() bool {
	return e.Status == http.StatusForbidden
}

func parseAPIError(status int, raw []byte) *APIError {
	ae := &APIError{Status: status, Body: string(raw)}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err == nil {
		ae.Code = body.Error
		ae.Message = body.Message
	}
	if ae.Message == "" {
		switch status {
		case http.StatusUnauthorized:
			ae.Message = "未授权：Agent Token 无效或已吊销"
		case http.StatusForbidden:
			ae.Message = "操作被拒绝"
		case http.StatusNotFound:
			ae.Message = "资源不存在"
		case http.StatusConflict:
			ae.Message = "操作冲突"
		default:
			ae.Message = fmt.Sprintf("Control Plane 返回 HTTP %d", status)
		}
	}
	return ae
}

// doRaw 发请求并返回原始 JSON body。
func (c *AgentClient) doRaw(ctx context.Context, method, path string, query url.Values) (json.RawMessage, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Control Plane 失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseAPIError(resp.StatusCode, raw)
	}
	if len(raw) == 0 {
		if method == http.MethodPost {
			return json.RawMessage(`{"ok":true}`), nil
		}
		return json.RawMessage(`null`), nil
	}
	return json.RawMessage(raw), nil
}

// Whoami GET /api/v1/agent/whoami
func (c *AgentClient) Whoami(ctx context.Context) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodGet, "/api/v1/agent/whoami", nil)
}

// ListNodes GET /api/v1/agent/nodes
func (c *AgentClient) ListNodes(ctx context.Context) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodGet, "/api/v1/agent/nodes", nil)
}

// ListInstances GET /api/v1/agent/instances
func (c *AgentClient) ListInstances(ctx context.Context, nodeID uint) (json.RawMessage, error) {
	var q url.Values
	if nodeID > 0 {
		q = url.Values{"nodeId": {strconv.FormatUint(uint64(nodeID), 10)}}
	}
	return c.doRaw(ctx, http.MethodGet, "/api/v1/agent/instances", q)
}

// GetInstance GET /api/v1/agent/instances/:id
func (c *AgentClient) GetInstance(ctx context.Context, id uint) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodGet, "/api/v1/agent/instances/"+strconv.FormatUint(uint64(id), 10), nil)
}

// GetInstanceMetrics GET /api/v1/agent/instances/:id/metrics
func (c *AgentClient) GetInstanceMetrics(ctx context.Context, id uint) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodGet, "/api/v1/agent/instances/"+strconv.FormatUint(uint64(id), 10)+"/metrics", nil)
}

// InstanceStart POST /api/v1/agent/instances/:id/start
func (c *AgentClient) InstanceStart(ctx context.Context, id uint) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodPost, "/api/v1/agent/instances/"+strconv.FormatUint(uint64(id), 10)+"/start", nil)
}

// InstanceStop POST /api/v1/agent/instances/:id/stop
func (c *AgentClient) InstanceStop(ctx context.Context, id uint) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodPost, "/api/v1/agent/instances/"+strconv.FormatUint(uint64(id), 10)+"/stop", nil)
}

// InstanceRestart POST /api/v1/agent/instances/:id/restart
func (c *AgentClient) InstanceRestart(ctx context.Context, id uint) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodPost, "/api/v1/agent/instances/"+strconv.FormatUint(uint64(id), 10)+"/restart", nil)
}

// NodeMaintenanceEnter POST /api/v1/agent/nodes/:id/maintenance/enter
func (c *AgentClient) NodeMaintenanceEnter(ctx context.Context, id uint) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodPost, "/api/v1/agent/nodes/"+strconv.FormatUint(uint64(id), 10)+"/maintenance/enter", nil)
}

// NodeMaintenanceLeave POST /api/v1/agent/nodes/:id/maintenance/leave
func (c *AgentClient) NodeMaintenanceLeave(ctx context.Context, id uint) (json.RawMessage, error) {
	return c.doRaw(ctx, http.MethodPost, "/api/v1/agent/nodes/"+strconv.FormatUint(uint64(id), 10)+"/maintenance/leave", nil)
}
