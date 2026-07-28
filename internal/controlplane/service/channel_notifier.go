package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ChannelConfig 通道连接配置（FR-085）。按 AlertChannel.Type 取用对应子集，
// 凭证子字段（URL 含 secret、SMTP 密码、bot token）经 ${ENV_VAR} 引用（config-files 规范）。
type ChannelConfig struct {
	// 通用 webhook / dingtalk / wecom / feishu / discord 的目标地址。
	URL string `json:"url,omitempty"`
	// telegram：bot token + 目标 chatId。
	Token  string `json:"token,omitempty"`
	ChatID string `json:"chatId,omitempty"`
	// email（SMTP）。
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
}

// AlertNotification 一条要分发的告警通知（与具体通道无关的语义载荷）。
type AlertNotification struct {
	// Event 取值：alert_fired | alert_resolved。
	Event   string
	RuleID  string
	Title   string
	Message string
	Level   string
	// Count 聚合计数（去抖窗口内累计触发次数，≥1）。
	Count int
	Time  time.Time
}

// credentialFields 返回某通道类型中需经 ${ENV} 引用校验的凭证子字段值。
// 仅这些字段允许（且要求）以 ${ENV_VAR} 形式出现；非凭证字段（chatId/from/host 等）按明文。
func credentialFields(channelType string, cfg *ChannelConfig) []string {
	switch channelType {
	case model.ChannelTypeWebhook, model.ChannelTypeDingtalk, model.ChannelTypeWecom,
		model.ChannelTypeFeishu, model.ChannelTypeDiscord:
		// URL 整串含 access_token/secret，视为凭证。
		return []string{cfg.URL}
	case model.ChannelTypeTelegram:
		return []string{cfg.Token}
	case model.ChannelTypeEmail:
		return []string{cfg.Password}
	default:
		return nil
	}
}

// validateChannelConfig 校验通道配置：类型合法 + 必填项齐 + 凭证字段为 ${ENV_VAR} 引用。
// 站内通道（inapp）无外部配置，恒合法。
func validateChannelConfig(channelType string, cfg *ChannelConfig) error {
	switch channelType {
	case model.ChannelTypeWebhook, model.ChannelTypeDingtalk, model.ChannelTypeWecom,
		model.ChannelTypeFeishu, model.ChannelTypeDiscord:
		if strings.TrimSpace(cfg.URL) == "" {
			return fmt.Errorf("通道地址不能为空")
		}
	case model.ChannelTypeTelegram:
		if strings.TrimSpace(cfg.Token) == "" || strings.TrimSpace(cfg.ChatID) == "" {
			return fmt.Errorf("Telegram 通道需配置 token 与 chatId")
		}
	case model.ChannelTypeEmail:
		if strings.TrimSpace(cfg.Host) == "" || cfg.Port == 0 || strings.TrimSpace(cfg.To) == "" {
			return fmt.Errorf("邮件通道需配置 host、port、to")
		}
	case model.ChannelTypeInApp:
		return nil
	default:
		return fmt.Errorf("不支持的通道类型: %s", channelType)
	}
	// 凭证字段必须是 ${ENV} 引用（非空时）。
	for _, ref := range credentialFields(channelType, cfg) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if !envRefPattern.MatchString(ref) {
			return fmt.Errorf("%w: %q", ErrCredentialNotEnvRef, ref)
		}
	}
	return nil
}

// resolveChannelConfig 解析通道配置中的 ${ENV} 凭证引用为明文（发送前调用）。
// 返回解析后的副本，原配置不变。非凭证字段原样保留。
func resolveChannelConfig(channelType string, cfg *ChannelConfig) (*ChannelConfig, error) {
	out := *cfg
	switch channelType {
	case model.ChannelTypeWebhook, model.ChannelTypeDingtalk, model.ChannelTypeWecom,
		model.ChannelTypeFeishu, model.ChannelTypeDiscord:
		v, err := resolveEnvRefMaybePlain(cfg.URL)
		if err != nil {
			return nil, err
		}
		out.URL = v
	case model.ChannelTypeTelegram:
		v, err := resolveEnvRefMaybePlain(cfg.Token)
		if err != nil {
			return nil, err
		}
		out.Token = v
	case model.ChannelTypeEmail:
		v, err := resolveEnvRefMaybePlain(cfg.Password)
		if err != nil {
			return nil, err
		}
		out.Password = v
	}
	return &out, nil
}

// resolveEnvRefMaybePlain 解析 ${ENV} 引用；非 ${...} 形式按明文原样返回（兼容无 secret 的明文 URL）。
// 与 resolveEnvRef 的区别：后者对明文报错，这里容忍明文（创建时已按字段类型校验过是否强制 ${ENV}）。
func resolveEnvRefMaybePlain(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if envRefPattern.MatchString(ref) {
		return resolveEnvRef(ref)
	}
	return ref, nil
}

// ChannelNotifier 按通道类型把告警通知投递到外部出口（FR-085）。
// inapp 不在此处理（由 dispatcher 直接落库为站内通知）。
type ChannelNotifier struct {
	client          *http.Client
	telegramAPIBase string
}

// NewChannelNotifier 创建通道通知器。
func NewChannelNotifier() *ChannelNotifier {
	return &ChannelNotifier{
		client:          &http.Client{Timeout: 10 * time.Second},
		telegramAPIBase: "https://api.telegram.org",
	}
}

// Send 把一条通知投递到指定通道。解析凭证 ${ENV} → 按类型构造 payload → HTTP/SMTP 投递。
func (n *ChannelNotifier) Send(channelType string, rawConfig string, note AlertNotification) error {
	cfg, err := parseChannelConfig(rawConfig)
	if err != nil {
		return err
	}
	resolved, err := resolveChannelConfig(channelType, cfg)
	if err != nil {
		return err
	}
	switch channelType {
	case model.ChannelTypeWebhook:
		return n.postJSON(resolved.URL, webhookBody(note))
	case model.ChannelTypeDingtalk:
		return n.postJSON(resolved.URL, dingtalkBody(note))
	case model.ChannelTypeWecom:
		return n.postJSON(resolved.URL, wecomBody(note))
	case model.ChannelTypeFeishu:
		return n.postJSON(resolved.URL, feishuBody(note))
	case model.ChannelTypeDiscord:
		return n.postJSON(resolved.URL, discordBody(note))
	case model.ChannelTypeTelegram:
		return n.sendTelegram(resolved, note)
	case model.ChannelTypeEmail:
		return n.sendEmail(resolved, note)
	case model.ChannelTypeInApp:
		// 站内通知不经此投递（dispatcher 已落库）。
		return nil
	default:
		return fmt.Errorf("不支持的通道类型: %s", channelType)
	}
}

// parseChannelConfig 解析通道配置 JSON 串。空串返回空配置。
func parseChannelConfig(raw string) (*ChannelConfig, error) {
	cfg := &ChannelConfig{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("解析通道配置失败: %w", err)
	}
	return cfg, nil
}

// postJSON 向 url POST 一个 JSON body，4xx/5xx 视为失败。
func (n *ChannelNotifier) postJSON(url string, body interface{}) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("序列化通知载荷失败: %w", err)
	}
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("发送通知失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("通道返回状态码 %d", resp.StatusCode)
	}
	return nil
}

// plainText 生成通道无关的纯文本正文（含级别、计数）。
func plainText(note AlertNotification) string {
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(strings.ToUpper(note.Level))
	b.WriteString("] ")
	b.WriteString(note.Title)
	if note.Message != "" {
		b.WriteString("\n")
		b.WriteString(note.Message)
	}
	if note.Count > 1 {
		b.WriteString("\n(聚合 ")
		b.WriteString(strconv.Itoa(note.Count))
		b.WriteString(" 次)")
	}
	b.WriteString("\n时间: ")
	b.WriteString(note.Time.Format(time.RFC3339))
	return b.String()
}

// webhookBody 通用 webhook 结构化载荷（兼容 FR-011 WebhookPayload 字段）。
func webhookBody(note AlertNotification) map[string]interface{} {
	return map[string]interface{}{
		"event":   note.Event,
		"ruleId":  note.RuleID,
		"title":   note.Title,
		"message": note.Message,
		"level":   note.Level,
		"count":   note.Count,
		"time":    note.Time.Format(time.RFC3339),
	}
}

// dingtalkBody 钉钉机器人 text 消息。
func dingtalkBody(note AlertNotification) map[string]interface{} {
	return map[string]interface{}{
		"msgtype": "text",
		"text":    map[string]string{"content": plainText(note)},
	}
}

// wecomBody 企业微信群机器人 text 消息。
func wecomBody(note AlertNotification) map[string]interface{} {
	return map[string]interface{}{
		"msgtype": "text",
		"text":    map[string]string{"content": plainText(note)},
	}
}

// feishuBody 飞书自定义机器人 text 消息。
func feishuBody(note AlertNotification) map[string]interface{} {
	return map[string]interface{}{
		"msg_type": "text",
		"content":  map[string]string{"text": plainText(note)},
	}
}

// discordBody Discord webhook content 消息。
func discordBody(note AlertNotification) map[string]interface{} {
	return map[string]interface{}{"content": plainText(note)}
}

// sendTelegram 经 Bot API sendMessage 投递。
func (n *ChannelNotifier) sendTelegram(cfg *ChannelConfig, note AlertNotification) error {
	api := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(n.telegramAPIBase, "/"), cfg.Token)
	return n.postJSON(api, map[string]interface{}{
		"chat_id": cfg.ChatID,
		"text":    plainText(note),
	})
}

// sendEmail 经 SMTP 投递纯文本邮件。支持 STARTTLS（587）与隐式 TLS（465）。
func (n *ChannelNotifier) sendEmail(cfg *ChannelConfig, note AlertNotification) error {
	from := cfg.From
	if from == "" {
		from = cfg.Username
	}
	tos := splitRecipients(cfg.To)
	if len(tos) == 0 {
		return fmt.Errorf("邮件通道缺少收件人")
	}
	subject := fmt.Sprintf("[%s] %s", strings.ToUpper(note.Level), note.Title)
	return sendSMTPMessage(SMTPMessageConfig{
		Host: cfg.Host, Port: cfg.Port, Username: cfg.Username, Password: cfg.Password, From: from,
	}, tos, subject, plainText(note))
}

// buildEmailMessage 组装 RFC 822 邮件（UTF-8 纯文本，主题 base64 编码避免乱码）。
func buildEmailMessage(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// splitRecipients 拆分逗号/分号分隔的收件人列表。
func splitRecipients(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// validWebhookURL 校验 URL 形态（http/https），供测试发送前的轻量预检。
func validWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
