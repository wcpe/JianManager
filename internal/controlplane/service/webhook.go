package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// WebhookNotifier Webhook 通知器。
type WebhookNotifier struct {
	client *http.Client
}

// NewWebhookNotifier 创建 Webhook 通知器。
func NewWebhookNotifier() *WebhookNotifier {
	return &WebhookNotifier{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// WebhookPayload Webhook 载荷。
type WebhookPayload struct {
	Event   string      `json:"event"`
	RuleID  string      `json:"ruleId"`
	Target  string      `json:"target"`
	Value   float64     `json:"value"`
	Message string      `json:"message"`
	Time    string      `json:"time"`
}

// Send 发送 Webhook 通知。
func (n *WebhookNotifier) Send(url string, payload WebhookPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化 Webhook 载荷失败: %w", err)
	}

	resp, err := n.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("发送 Webhook 失败", "url", url, "error", err)
		return fmt.Errorf("发送 Webhook 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		slog.Warn("Webhook 返回错误", "url", url, "status", resp.StatusCode)
		return fmt.Errorf("Webhook 返回状态码 %d", resp.StatusCode)
	}

	slog.Info("Webhook 已发送", "url", url, "event", payload.Event)
	return nil
}
