package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// QQ 开放平台（FR-494）常量。
const (
	// qqDefaultAPIBase 开放平台 API 根地址默认值（现行官方口径：api.bot.qq.com）。
	// 可在通道配置 baseUrl 中覆盖，用于适配沙箱或域名变更。
	qqDefaultAPIBase = "https://api.bot.qq.com"
	// qqTargetGroup 群聊目标（targetId 为 group_openid）。
	qqTargetGroup = "group"
	// qqTargetC2C 单聊目标（targetId 为 user_openid）。
	qqTargetC2C = "c2c"
	// qqDefaultExpiresSec 取 token 响应缺省 expires_in（2 小时）。
	qqDefaultExpiresSec = 7200
	// qqCodeCheckTokenFailed 官方业务错误码：校验 token 失败（系统错误，重试一次通常会好）。
	qqCodeCheckTokenFailed = 11242
	// qqCodeTokenNotPass 官方业务错误码：校验 token 未通过（令牌错误，需重新获取）。
	qqCodeTokenNotPass = 11243
	// qqTokenRefreshAhead token 提前刷新窗口上限：临近过期前 5 分钟开始视为需刷新。
	qqTokenRefreshAhead = 5 * time.Minute
)

// ChannelConfig 通道连接配置（FR-085）。按 AlertChannel.Type 取用对应子集，
// 凭证子字段（URL 含 secret、SMTP 密码、bot token）经 ${ENV_VAR} 引用（config-files 规范）。
type ChannelConfig struct {
	// 通用 webhook / dingtalk / wecom / feishu / discord 的目标地址。
	URL string `json:"url,omitempty"`
	// telegram：bot token + 目标 chatId。
	Token  string `json:"token,omitempty"`
	ChatID string `json:"chatId,omitempty"`
	// qq（FR-494）：开放平台机器人凭证 + 推送目标。
	// appId / targetId 是公开标识（明文）；appSecret 是唯一凭证，强制 ${ENV_VAR} 引用。
	// targetType 取 group（群聊，targetId 为 group_openid）或 c2c（单聊，targetId 为 user_openid）。
	// baseUrl 可选，留空取 qqDefaultAPIBase。
	AppID      string `json:"appId,omitempty"`
	AppSecret  string `json:"appSecret,omitempty"`
	TargetType string `json:"targetType,omitempty"`
	TargetID   string `json:"targetId,omitempty"`
	BaseURL    string `json:"baseUrl,omitempty"`
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
	case model.ChannelTypeQQ:
		// 仅 appSecret 是凭证；appId / targetId 是公开标识，按明文处理。
		return []string{cfg.AppSecret}
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
	case model.ChannelTypeQQ:
		if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.TargetID) == "" {
			return fmt.Errorf("QQ 通道需配置 appId 与 targetId")
		}
		// appSecret 必须显式校验非空：末尾的凭证循环对空值放行（用于兼容「无凭证」通道），
		// 但 QQ 没有密钥就取不到 AccessToken，留空只会在首次投递时才失败。
		if strings.TrimSpace(cfg.AppSecret) == "" {
			return fmt.Errorf("QQ 通道需配置 appSecret")
		}
		if tt := strings.TrimSpace(cfg.TargetType); tt != qqTargetGroup && tt != qqTargetC2C {
			return fmt.Errorf("QQ 通道 targetType 必须是 %s 或 %s（当前: %q）", qqTargetGroup, qqTargetC2C, cfg.TargetType)
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
	// appSecret 是 QQ 专有凭证，这里做一次类型无关的兜底：前端在「填过 QQ 字段后把类型
	// 切走」时会把残留字段一并提交，只按当前类型挑凭证字段会让明文密钥随 config 落库。
	if s := strings.TrimSpace(cfg.AppSecret); s != "" && !envRefPattern.MatchString(s) {
		return fmt.Errorf("%w: %q", ErrCredentialNotEnvRef, s)
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
	case model.ChannelTypeQQ:
		v, err := resolveEnvRefMaybePlain(cfg.AppSecret)
		if err != nil {
			return nil, err
		}
		out.AppSecret = v
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

// qqTokenEntry QQ AccessToken 的缓存条目（按 appId 维度）。
// 同一 appId 的条目兼作在途请求的汇合点：inflight 非 nil 表示已有请求在途，
// 其余调用等待其关闭后复用结果，从而保证「并发不重复取 token」。
type qqTokenEntry struct {
	// token 缓存的 AccessToken（在途请求完成前为空）。
	token string
	// expiresAt 绝对过期时间（本地时钟）。
	expiresAt time.Time
	// lifetime 该 token 的总有效期，用于按比例算提前刷新窗口。
	lifetime time.Duration
	// inflight 非 nil 即在途；关闭即结果就绪。
	inflight chan struct{}
	// err 在途请求的失败原因（成功为 nil）。
	err error
}

// ChannelNotifier 按通道类型把告警通知投递到外部出口（FR-085）。
// inapp 不在此处理（由 dispatcher 直接落库为站内通知）。
type ChannelNotifier struct {
	client          *http.Client
	telegramAPIBase string

	// qqTokens 按 appId 缓存的 AccessToken（FR-494），由 qqMu 保护。
	qqMu     sync.Mutex
	qqTokens map[string]*qqTokenEntry
}

// NewChannelNotifier 创建通道通知器。
func NewChannelNotifier() *ChannelNotifier {
	return &ChannelNotifier{
		client:          &http.Client{Timeout: 10 * time.Second},
		telegramAPIBase: "https://api.telegram.org",
		qqTokens:        make(map[string]*qqTokenEntry),
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
	case model.ChannelTypeQQ:
		return n.sendQQ(resolved, note)
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

// qqAPIResponse 开放平台通用响应壳。开放平台在 HTTP 200 时同样可能返回非 0 code，
// 故业务判定必须与 HTTP 状态码一并看。
type qqAPIResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// qqExpiresIn 兼容开放平台 expires_in 的两种返回形态。
//
// 官方「获取 access_token」页的参数表把它标为 number，但**同一页的返回示例**给出的是
// 字符串（`{"expires_in":"7200"}`），口径不统一。若只接受一种形态，另一种会让整个响应
// 反序列化失败——取不到 token 意味着该通道**全部**投递失败，且只落一条 Warn 日志，
// 故两种都要接受。
type qqExpiresIn int

// UnmarshalJSON 接受 JSON 数字与数字字符串；null/空串按 0 处理（由调用方回退默认值）。
func (e *qqExpiresIn) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "" || s == "null" {
		*e = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("expires_in 形态非法: %s", string(data))
	}
	*e = qqExpiresIn(n)
	return nil
}

// qqTokenResponse 取 AccessToken 响应体。
type qqTokenResponse struct {
	AccessToken string `json:"access_token"`
	// ExpiresIn 有效期（秒），缺省按 7200 处理。
	ExpiresIn qqExpiresIn `json:"expires_in"`
	Code      int         `json:"code"`
	Message   string      `json:"message"`
}

// qqAPIBase 解析 QQ 开放平台 API 根地址：配置留空取默认值，末尾斜杠归一，非法地址直接报错。
func qqAPIBase(cfg *ChannelConfig) (string, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = qqDefaultAPIBase
	}
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("QQ 通道 baseUrl 非法: %q", cfg.BaseURL)
	}
	// 取 token 与发消息都要把 appSecret / AccessToken 发往该地址，明文 http 等同于凭证外泄。
	// 回环地址放行，便于本地调试与测试。
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("QQ 通道 baseUrl 必须是 https（当前 %q）", cfg.BaseURL)
	}
	return base, nil
}

// isLoopbackHost 判断主机名是否为回环地址（localhost / 127.x / ::1）。
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// qqMessageSegment 按 targetType 返回消息接口路径段：group=群聊（groups），c2c=单聊（users）。
func qqMessageSegment(targetType string) (string, error) {
	switch targetType {
	case qqTargetGroup:
		return "groups", nil
	case qqTargetC2C:
		return "users", nil
	default:
		return "", fmt.Errorf("QQ 通道 targetType 必须是 %s 或 %s（当前: %q）", qqTargetGroup, qqTargetC2C, targetType)
	}
}

// qqSnippet 截断响应正文用于错误信息，避免超长响应体污染日志。
func qqSnippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if r := []rune(s); len(r) > 256 {
		return string(r[:256]) + "…"
	}
	return s
}

// qqTokenNeedsRefresh 判定缓存的 AccessToken 是否需要刷新（FR-494）。
// 提前刷新窗口取 min(5 分钟, 总有效期/3)：剩余有效期小于该窗口即视为临近过期。
// 例：官方默认 2 小时有效期 → 窗口 5 分钟；若平台下发更短有效期的 token，
// 窗口按 1/3 比例收缩（而非固定 5 分钟），否则短寿命 token 会每次投递都重新取。
func qqTokenNeedsRefresh(expiresAt time.Time, lifetime time.Duration) bool {
	remaining := time.Until(expiresAt)
	if remaining <= 0 {
		return true
	}
	window := lifetime / 3
	if window > qqTokenRefreshAhead {
		window = qqTokenRefreshAhead
	}
	return remaining < window
}

// postJSONRaw 以 JSON POST 请求并返回响应状态码与正文（供需读业务错误码的通道使用）。
func (n *ChannelNotifier) postJSONRaw(api string, headers map[string]string, body interface{}) (int, []byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("序列化请求载荷失败: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, api, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("读取通道响应失败: %w", err)
	}
	return resp.StatusCode, respBody, nil
}

// fetchQQAccessToken 经开放平台换取 AccessToken，返回 token 与有效期。
// 失败时 HTTP 状态可能仍是 200 而 body 里 code 非 0，故状态码与 code 都要判。
func (n *ChannelNotifier) fetchQQAccessToken(baseURL, appID, appSecret string) (string, time.Duration, error) {
	api := baseURL + "/app/getAppAccessToken"
	status, body, err := n.postJSONRaw(api, nil, map[string]string{
		"appId":        appID,
		"clientSecret": appSecret,
	})
	if err != nil {
		return "", 0, fmt.Errorf("获取 QQ AccessToken 失败: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", 0, fmt.Errorf("获取 QQ AccessToken 失败: HTTP 状态码 %d，响应 %s", status, qqSnippet(body))
	}
	var parsed qqTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("解析 QQ AccessToken 响应失败（HTTP 状态码 %d，响应 %s）: %w", status, qqSnippet(body), err)
	}
	if parsed.Code != 0 {
		return "", 0, fmt.Errorf("获取 QQ AccessToken 失败: HTTP 状态码 %d，code=%d message=%s", status, parsed.Code, parsed.Message)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return "", 0, fmt.Errorf("获取 QQ AccessToken 失败: 响应缺少 access_token（HTTP 状态码 %d，响应 %s）", status, qqSnippet(body))
	}
	expiresIn := int(parsed.ExpiresIn)
	if expiresIn <= 0 {
		expiresIn = qqDefaultExpiresSec
	}
	return parsed.AccessToken, time.Duration(expiresIn) * time.Second, nil
}

// qqAccessToken 取可用的 AccessToken（FR-494）：缓存未接近过期则直接复用、不发请求；
// 同一 appId 并发调用时只有一次在途请求，其余等待并复用其结果（单飞）。
func (n *ChannelNotifier) qqAccessToken(baseURL, appID, appSecret string) (string, error) {
	n.qqMu.Lock()
	entry, ok := n.qqTokens[appID]
	if !ok {
		entry = &qqTokenEntry{}
		n.qqTokens[appID] = entry
	}
	if entry.inflight != nil {
		// 已有同 appId 在途请求：等它结束后复用结果，不重复取 token。
		wait := entry.inflight
		n.qqMu.Unlock()
		<-wait
		n.qqMu.Lock()
		entry = n.qqTokens[appID]
		if entry != nil && entry.err == nil && entry.token != "" {
			token := entry.token
			n.qqMu.Unlock()
			return token, nil
		}
		err := fmt.Errorf("获取 QQ AccessToken 失败")
		if entry != nil && entry.err != nil {
			err = entry.err
		}
		n.qqMu.Unlock()
		return "", err
	}
	if entry.token != "" && !qqTokenNeedsRefresh(entry.expiresAt, entry.lifetime) {
		// 缓存命中且未接近过期：直接复用，不发请求。
		token := entry.token
		n.qqMu.Unlock()
		return token, nil
	}
	// 登记在途请求后释放锁，避免持锁做网络 IO（其余调用会在此汇合）。
	entry.inflight = make(chan struct{})
	inflight := entry.inflight
	n.qqMu.Unlock()

	token, lifetime, err := n.fetchQQAccessToken(baseURL, appID, appSecret)

	n.qqMu.Lock()
	entry.inflight = nil
	if err != nil {
		entry.err = err
		entry.token = ""
		entry.expiresAt = time.Time{}
		entry.lifetime = 0
	} else {
		entry.err = nil
		entry.token = token
		entry.lifetime = lifetime
		entry.expiresAt = time.Now().Add(lifetime)
	}
	n.qqMu.Unlock()
	close(inflight)

	if err != nil {
		return "", err
	}
	return token, nil
}

// sendQQ 经 QQ 开放平台主动推送纯文本消息（FR-494）。
// targetType=group 走群聊接口、c2c 走单聊接口；均为主动推送（不带 msg_id），不依赖入站消息上下文。
func (n *ChannelNotifier) sendQQ(cfg *ChannelConfig, note AlertNotification) error {
	base, err := qqAPIBase(cfg)
	if err != nil {
		return err
	}
	segment, err := qqMessageSegment(cfg.TargetType)
	if err != nil {
		return err
	}
	token, err := n.qqAccessToken(base, cfg.AppID, cfg.AppSecret)
	if err != nil {
		return err
	}
	api := fmt.Sprintf("%s/v2/%s/%s/messages", base, segment, url.PathEscape(cfg.TargetID))
	status, body, err := n.qqPostMessage(api, token, note)
	if err != nil {
		return err
	}
	if err := checkQQResponse("发送 QQ 消息", status, body); err != nil {
		if !qqTokenRejected(status, body) {
			return err
		}
		// 缓存的令牌可能被平台提前作废（如轮换）。不重试的话，该通道会用失效令牌
		// 一直失败到刷新窗口才自愈，期间告警静默丢失。清缓存重取一次再试即可自愈。
		n.qqInvalidate(cfg.AppID)
		token, retryErr := n.qqAccessToken(base, cfg.AppID, cfg.AppSecret)
		if retryErr != nil {
			return retryErr
		}
		status, body, err = n.qqPostMessage(api, token, note)
		if err != nil {
			return err
		}
		return checkQQResponse("发送 QQ 消息", status, body)
	}
	return nil
}

// qqPostMessage 发一条主动推送消息（msg_type=0 纯文本），返回状态码与正文。
func (n *ChannelNotifier) qqPostMessage(api, token string, note AlertNotification) (int, []byte, error) {
	status, body, err := n.postJSONRaw(api, map[string]string{"Authorization": "QQBot " + token}, map[string]interface{}{
		"content":  plainText(note),
		"msg_type": 0, // 0 = 纯文本
	})
	if err != nil {
		return 0, nil, fmt.Errorf("发送 QQ 消息失败: %w", err)
	}
	return status, body, nil
}

// qqTokenRejected 判断响应是否表示访问令牌不可用：HTTP 401，或官方业务码
// 11242（校验 token 失败）/ 11243（校验 token 未通过）。用于触发「清缓存重取」自愈。
func qqTokenRejected(status int, body []byte) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	var parsed qqAPIResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	return parsed.Code == qqCodeCheckTokenFailed || parsed.Code == qqCodeTokenNotPass
}

// qqInvalidate 丢弃某 appId 的令牌缓存，强制下次重新获取。
func (n *ChannelNotifier) qqInvalidate(appID string) {
	n.qqMu.Lock()
	delete(n.qqTokens, appID)
	n.qqMu.Unlock()
}

// checkQQResponse 判定开放平台业务响应：HTTP 非 2xx 报错；HTTP 2xx 但 body 含非 0 code 同样报错。
// 只判状态码会漏掉频控（40034100）、内容含 URL（40054010）这类业务失败。
func checkQQResponse(action string, status int, body []byte) error {
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s失败: HTTP 状态码 %d，响应 %s", action, status, qqSnippet(body))
	}
	var parsed qqAPIResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 响应可能不是 JSON（或为空），能解析出错误码的场景已由上面的分支覆盖。
		return nil
	}
	if parsed.Code != 0 {
		return fmt.Errorf("%s失败: HTTP 状态码 %d，code=%d message=%s", action, status, parsed.Code, parsed.Message)
	}
	return nil
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
