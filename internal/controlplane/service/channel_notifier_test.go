package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func sampleNote() AlertNotification {
	return AlertNotification{
		Event:   "alert_fired",
		RuleID:  "rule-uuid",
		Title:   "CPU 过高",
		Message: "cpu > 90",
		Level:   model.AlertLevelCritical,
		Count:   3,
		Time:    time.Unix(1700000000, 0).UTC(),
	}
}

func TestValidateChannelConfig(t *testing.T) {
	tests := []struct {
		name        string
		channelType string
		cfg         ChannelConfig
		wantErr     bool
	}{
		{"webhook env ref ok", model.ChannelTypeWebhook, ChannelConfig{URL: "${JM_WH}"}, false},
		{"webhook plain url rejected", model.ChannelTypeWebhook, ChannelConfig{URL: "https://hooks.example.com/x"}, true},
		{"webhook empty url", model.ChannelTypeWebhook, ChannelConfig{}, true},
		{"dingtalk plain url rejected", model.ChannelTypeDingtalk, ChannelConfig{URL: "https://oapi.dingtalk.com/robot/send?access_token=x"}, true},
		{"dingtalk env ref ok", model.ChannelTypeDingtalk, ChannelConfig{URL: "${JM_DING}"}, false},
		{"telegram needs token+chat", model.ChannelTypeTelegram, ChannelConfig{Token: "${TG}"}, true},
		{"telegram token must be env", model.ChannelTypeTelegram, ChannelConfig{Token: "123:ABC", ChatID: "1"}, true},
		{"telegram env ok", model.ChannelTypeTelegram, ChannelConfig{Token: "${TG}", ChatID: "1"}, false},
		{"qq group env ok", model.ChannelTypeQQ, ChannelConfig{AppID: "1020001", AppSecret: "${JM_QQ_SECRET}", TargetType: "group", TargetID: "openid-group"}, false},
		{"qq c2c env ok", model.ChannelTypeQQ, ChannelConfig{AppID: "1020001", AppSecret: "${JM_QQ_SECRET}", TargetType: "c2c", TargetID: "openid-user"}, false},
		{"qq missing appId", model.ChannelTypeQQ, ChannelConfig{AppSecret: "${JM_QQ_SECRET}", TargetType: "group", TargetID: "openid-group"}, true},
		{"qq missing targetId", model.ChannelTypeQQ, ChannelConfig{AppID: "1020001", AppSecret: "${JM_QQ_SECRET}", TargetType: "group"}, true},
		{"qq missing appSecret", model.ChannelTypeQQ, ChannelConfig{AppID: "1020001", TargetType: "group", TargetID: "openid-group"}, true},
		{"qq missing targetType", model.ChannelTypeQQ, ChannelConfig{AppID: "1020001", AppSecret: "${JM_QQ_SECRET}", TargetID: "openid-group"}, true},
		{"qq invalid targetType", model.ChannelTypeQQ, ChannelConfig{AppID: "1020001", AppSecret: "${JM_QQ_SECRET}", TargetType: "channel", TargetID: "openid-group"}, true},
		{"qq plain appSecret rejected", model.ChannelTypeQQ, ChannelConfig{AppID: "1020001", AppSecret: "plain", TargetType: "group", TargetID: "openid-group"}, true},
		{"email needs host/port/to", model.ChannelTypeEmail, ChannelConfig{Host: "smtp.x.com"}, true},
		{"email password must be env", model.ChannelTypeEmail, ChannelConfig{Host: "smtp.x.com", Port: 587, To: "a@x.com", Password: "plain"}, true},
		{"email env password ok", model.ChannelTypeEmail, ChannelConfig{Host: "smtp.x.com", Port: 587, To: "a@x.com", Password: "${PW}"}, false},
		{"inapp always ok", model.ChannelTypeInApp, ChannelConfig{}, false},
		{"unknown type", "carrier-pigeon", ChannelConfig{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateChannelConfig(tt.channelType, &tt.cfg)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResolveChannelConfig_FromEnv(t *testing.T) {
	t.Setenv("JM_TEST_WH_URL", "https://hooks.example.com/secret")
	cfg := &ChannelConfig{URL: "${JM_TEST_WH_URL}"}
	resolved, err := resolveChannelConfig(model.ChannelTypeWebhook, cfg)
	require.NoError(t, err)
	assert.Equal(t, "https://hooks.example.com/secret", resolved.URL)
	// 原配置不被修改。
	assert.Equal(t, "${JM_TEST_WH_URL}", cfg.URL)
}

func TestResolveChannelConfig_MissingEnv(t *testing.T) {
	cfg := &ChannelConfig{URL: "${JM_TEST_DEFINITELY_UNSET}"}
	_, err := resolveChannelConfig(model.ChannelTypeWebhook, cfg)
	assert.Error(t, err)
}

func TestPlainText_IncludesLevelAndCount(t *testing.T) {
	txt := plainText(sampleNote())
	assert.Contains(t, txt, "CRITICAL")
	assert.Contains(t, txt, "CPU 过高")
	assert.Contains(t, txt, "聚合 3 次")
}

// TestChannelNotifier_Dispatch 用 httptest 验证各 IM/webhook 类型的 payload 形状与投递成功。
func TestChannelNotifier_Dispatch(t *testing.T) {
	type capture struct {
		body map[string]interface{}
	}

	cases := []struct {
		channelType string
		assertBody  func(t *testing.T, body map[string]interface{})
	}{
		{model.ChannelTypeWebhook, func(t *testing.T, b map[string]interface{}) {
			assert.Equal(t, "alert_fired", b["event"])
			assert.Equal(t, "critical", b["level"])
		}},
		{model.ChannelTypeDingtalk, func(t *testing.T, b map[string]interface{}) {
			assert.Equal(t, "text", b["msgtype"])
			assert.Contains(t, b["text"].(map[string]interface{})["content"], "CPU 过高")
		}},
		{model.ChannelTypeWecom, func(t *testing.T, b map[string]interface{}) {
			assert.Equal(t, "text", b["msgtype"])
		}},
		{model.ChannelTypeFeishu, func(t *testing.T, b map[string]interface{}) {
			assert.Equal(t, "text", b["msg_type"])
		}},
		{model.ChannelTypeDiscord, func(t *testing.T, b map[string]interface{}) {
			assert.Contains(t, b["content"], "CPU 过高")
		}},
	}

	for _, c := range cases {
		t.Run(c.channelType, func(t *testing.T) {
			cap := &capture{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&cap.body)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			rawCfg, _ := json.Marshal(ChannelConfig{URL: srv.URL})
			notifier := NewChannelNotifier()
			err := notifier.Send(c.channelType, string(rawCfg), sampleNote())
			require.NoError(t, err)
			require.NotNil(t, cap.body)
			c.assertBody(t, cap.body)
		})
	}
}

func TestChannelNotifier_PostFailureOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	rawCfg, _ := json.Marshal(ChannelConfig{URL: srv.URL})
	err := NewChannelNotifier().Send(model.ChannelTypeWebhook, string(rawCfg), sampleNote())
	assert.Error(t, err)
}

func TestChannelNotifier_TelegramRequest(t *testing.T) {
	var gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := NewChannelNotifier()
	notifier.telegramAPIBase = srv.URL
	rawCfg, _ := json.Marshal(ChannelConfig{Token: "test-token", ChatID: "chat-1"})
	err := notifier.Send(model.ChannelTypeTelegram, string(rawCfg), sampleNote())
	require.NoError(t, err)

	assert.Equal(t, "/bottest-token/sendMessage", gotPath)
	assert.Equal(t, "chat-1", gotBody["chat_id"])
	assert.Contains(t, gotBody["text"], "CPU 过高")
}

func TestBuildEmailMessage(t *testing.T) {
	msg := string(buildEmailMessage("ops@example.com", []string{"a@example.com", "b@example.com"}, "[CRITICAL] CPU 过高", "cpu > 90"))
	assert.Contains(t, msg, "From: ops@example.com")
	assert.Contains(t, msg, "To: a@example.com, b@example.com")
	assert.Contains(t, msg, "Subject: =?UTF-8?B?")
	assert.Contains(t, msg, "Content-Type: text/plain; charset=UTF-8")
	assert.Contains(t, msg, "cpu > 90")
}

func TestSplitRecipients(t *testing.T) {
	assert.Equal(t, []string{"a@x.com", "b@x.com"}, splitRecipients("a@x.com, b@x.com"))
	assert.Equal(t, []string{"a@x.com", "b@x.com"}, splitRecipients("a@x.com; b@x.com"))
	assert.Empty(t, splitRecipients("  "))
}

func TestValidWebhookURL(t *testing.T) {
	assert.True(t, validWebhookURL("https://hooks.example.com/x"))
	assert.True(t, validWebhookURL("http://localhost:8080/y"))
	assert.False(t, validWebhookURL("ftp://x"))
	assert.False(t, validWebhookURL("not a url"))
}

// ── QQ 通道（FR-494）──

// TestChannelNotifier_QQCredentialEnvRef 明文 appSecret 必须被拒，${ENV_VAR} 形式通过。
func TestChannelNotifier_QQCredentialEnvRef(t *testing.T) {
	plain := ChannelConfig{AppID: "1020001", AppSecret: "plain", TargetType: "group", TargetID: "openid-group"}
	err := validateChannelConfig(model.ChannelTypeQQ, &plain)
	require.ErrorIs(t, err, ErrCredentialNotEnvRef)

	// appId / targetId 是公开标识，明文不触发凭证校验。
	ref := ChannelConfig{AppID: "1020001", AppSecret: "${JM_QQ_SECRET}", TargetType: "group", TargetID: "openid-group"}
	assert.NoError(t, validateChannelConfig(model.ChannelTypeQQ, &ref))
}

// TestResolveChannelConfig_QQFromEnv QQ 通道解析 ${ENV} 形式的 appSecret，公开标识原样保留。
func TestResolveChannelConfig_QQFromEnv(t *testing.T) {
	t.Setenv("JM_TEST_QQ_SECRET", "secret-from-env")
	cfg := &ChannelConfig{AppID: "1020001", AppSecret: "${JM_TEST_QQ_SECRET}", TargetType: "group", TargetID: "openid-group"}
	resolved, err := resolveChannelConfig(model.ChannelTypeQQ, cfg)
	require.NoError(t, err)
	assert.Equal(t, "secret-from-env", resolved.AppSecret)
	// 非凭证字段按明文原样保留。
	assert.Equal(t, "1020001", resolved.AppID)
	assert.Equal(t, "group", resolved.TargetType)
	assert.Equal(t, "openid-group", resolved.TargetID)
	// 原配置不被修改。
	assert.Equal(t, "${JM_TEST_QQ_SECRET}", cfg.AppSecret)
}

// TestResolveChannelConfig_QQMissingEnv 引用的环境变量不存在时报错。
func TestResolveChannelConfig_QQMissingEnv(t *testing.T) {
	cfg := &ChannelConfig{AppID: "1020001", AppSecret: "${JM_TEST_QQ_UNSET_SECRET}", TargetType: "group", TargetID: "openid-group"}
	_, err := resolveChannelConfig(model.ChannelTypeQQ, cfg)
	assert.Error(t, err)
}

// qqMockAPI qq 通道用例的开放平台假服务端：同一端口上模拟取 token 与发消息两个端点。
// qqMockResp 一组「状态码 + 正文」的固定响应。
type qqMockResp struct {
	status int
	body   string
}

type qqMockAPI struct {
	srv *httptest.Server

	tokenCalls   atomic.Int32
	messageCalls atomic.Int32

	mu sync.Mutex
	// tokenDelay 取 token 端点的响应延迟，用于逼迫并发调用在途汇合。
	tokenDelay    time.Duration
	tokenStatus   int
	tokenResp     string
	messageStatus int
	messageResp   string
	// messageSeq 非空时按序消费（用尽后回退 messageStatus/messageResp），
	// 用于验证「首次失败 → 重试成功」这类多轮交互。
	messageSeq []qqMockResp
	// 最近一次请求的观测点。
	lastPath      string
	lastAuth      string
	lastTokenBody map[string]interface{}
	lastMsgBody   map[string]interface{}
}

// newQQMockAPI 起一个默认成功的开放平台假服务端（token 有效期 2 小时）。
func newQQMockAPI(t *testing.T) *qqMockAPI {
	t.Helper()
	m := &qqMockAPI{
		tokenStatus:   http.StatusOK,
		tokenResp:     `{"access_token":"mock-token","expires_in":7200}`,
		messageStatus: http.StatusOK,
		messageResp:   `{"id":"msg-1","timestamp":1700000000}`,
	}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		delay, tokenStatus, tokenResp, messageStatus, messageResp := m.tokenDelay, m.tokenStatus, m.tokenResp, m.messageStatus, m.messageResp
		m.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app/getAppAccessToken" {
			m.tokenCalls.Add(1)
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.mu.Lock()
			m.lastTokenBody = body
			m.mu.Unlock()
			w.WriteHeader(tokenStatus)
			_, _ = w.Write([]byte(tokenResp))
			return
		}
		m.messageCalls.Add(1)
		m.mu.Lock()
		m.lastPath = r.URL.Path
		m.lastAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&m.lastMsgBody)
		var seq *qqMockResp
		if len(m.messageSeq) > 0 {
			seq = &m.messageSeq[0]
			m.messageSeq = m.messageSeq[1:]
		}
		m.mu.Unlock()
		if seq != nil {
			w.WriteHeader(seq.status)
			_, _ = w.Write([]byte(seq.body))
			return
		}
		w.WriteHeader(messageStatus)
		_, _ = w.Write([]byte(messageResp))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// setTokenResponse 覆盖取 token 端点的响应（状态码 + 正文）。
func (m *qqMockAPI) setTokenResponse(status int, body string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenStatus, m.tokenResp = status, body
}

// setMessageResponse 覆盖发消息端点的响应（状态码 + 正文）。
func (m *qqMockAPI) setMessageResponse(status int, body string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messageStatus, m.messageResp = status, body
}

// setMessageSequence 按序设置发消息端点的前若干次响应，用于多轮交互用例。
func (m *qqMockAPI) setMessageSequence(resps ...qqMockResp) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messageSeq = resps
}

// qqTestCfg 构造一份指向给定 baseURL 的 QQ 通道配置 JSON。
func qqTestCfg(baseURL string) string {
	raw, _ := json.Marshal(ChannelConfig{
		AppID: "1020001", AppSecret: "plain",
		TargetType: qqTargetGroup, TargetID: "openid-group", BaseURL: baseURL,
	})
	return string(raw)
}

// lastTokenRequest/lastMessageRequest 读取观测点（加锁，避免与 handler 并发读写）。
func (m *qqMockAPI) lastTokenRequest() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastTokenBody
}

func (m *qqMockAPI) lastMessageRequest() (string, string, map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPath, m.lastAuth, m.lastMsgBody
}

// TestChannelNotifier_QQGroupRequest 群聊投递：路径 /v2/groups/{openid}/messages、
// 鉴权头 QQBot <token>、body 为 msg_type=0 的纯文本告警。
func TestChannelNotifier_QQGroupRequest(t *testing.T) {
	m := newQQMockAPI(t)
	rawCfg, _ := json.Marshal(ChannelConfig{
		AppID: "1020001", AppSecret: "plain",
		TargetType: qqTargetGroup, TargetID: "openid-group", BaseURL: m.srv.URL,
	})
	require.NoError(t, NewChannelNotifier().Send(model.ChannelTypeQQ, string(rawCfg), sampleNote()))

	assert.Equal(t, int32(1), m.tokenCalls.Load())
	assert.Equal(t, int32(1), m.messageCalls.Load())

	path, auth, body := m.lastMessageRequest()
	assert.Equal(t, "/v2/groups/openid-group/messages", path)
	assert.Equal(t, "QQBot mock-token", auth)
	assert.Equal(t, float64(0), body["msg_type"])
	assert.Contains(t, body["content"], "CPU 过高")

	// 取 token 用开放平台字段名 appId / clientSecret。
	tokenBody := m.lastTokenRequest()
	assert.Equal(t, "1020001", tokenBody["appId"])
	assert.Equal(t, "plain", tokenBody["clientSecret"])
}

// TestChannelNotifier_QQC2CRequest 单聊投递：路径 /v2/users/{openid}/messages，
// 且 ${ENV} 形式的 appSecret 在投递前被解析为明文。
func TestChannelNotifier_QQC2CRequest(t *testing.T) {
	t.Setenv("JM_TEST_QQ_SECRET", "secret-from-env")
	m := newQQMockAPI(t)
	rawCfg, _ := json.Marshal(ChannelConfig{
		AppID: "1020001", AppSecret: "${JM_TEST_QQ_SECRET}",
		TargetType: qqTargetC2C, TargetID: "openid-user", BaseURL: m.srv.URL,
	})
	require.NoError(t, NewChannelNotifier().Send(model.ChannelTypeQQ, string(rawCfg), sampleNote()))

	path, auth, body := m.lastMessageRequest()
	assert.Equal(t, "/v2/users/openid-user/messages", path)
	assert.Equal(t, "QQBot mock-token", auth)
	assert.Equal(t, float64(0), body["msg_type"])
	assert.Contains(t, body["content"], "CPU 过高")
	assert.Equal(t, "secret-from-env", m.lastTokenRequest()["clientSecret"])
}

// TestChannelNotifier_QQTokenCacheReuse token 在有效期内复用：连续投递两次只取一次 token。
func TestChannelNotifier_QQTokenCacheReuse(t *testing.T) {
	m := newQQMockAPI(t)
	notifier := NewChannelNotifier()
	rawCfg, _ := json.Marshal(ChannelConfig{
		AppID: "1020001", AppSecret: "plain",
		TargetType: qqTargetGroup, TargetID: "openid-group", BaseURL: m.srv.URL,
	})
	require.NoError(t, notifier.Send(model.ChannelTypeQQ, string(rawCfg), sampleNote()))
	require.NoError(t, notifier.Send(model.ChannelTypeQQ, string(rawCfg), sampleNote()))

	assert.Equal(t, int32(1), m.tokenCalls.Load(), "token 端点只应被请求一次")
	assert.Equal(t, int32(2), m.messageCalls.Load())
}

// TestChannelNotifier_QQTokenRefreshWhenExpiring 临近过期或已过期时投递必须重新取 token，
// 未接近过期则直接复用缓存。
func TestChannelNotifier_QQTokenRefreshWhenExpiring(t *testing.T) {
	tests := []struct {
		name           string
		shift          time.Duration // 第二次投递前对过期时间的偏移（相对当前时刻）
		wantTokenCalls int32
	}{
		{"剩余 2 小时直接复用缓存", 2 * time.Hour, 1},
		{"剩余 2 分钟提前刷新", 2 * time.Minute, 2},
		{"已过期必须刷新", -time.Minute, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newQQMockAPI(t)
			notifier := NewChannelNotifier()
			rawCfg, _ := json.Marshal(ChannelConfig{
				AppID: "1020001", AppSecret: "plain",
				TargetType: qqTargetGroup, TargetID: "openid-group", BaseURL: m.srv.URL,
			})
			require.NoError(t, notifier.Send(model.ChannelTypeQQ, string(rawCfg), sampleNote()))

			// 直接调整缓存条目的过期时间，模拟 token 生命周期推进。
			notifier.qqMu.Lock()
			notifier.qqTokens["1020001"].expiresAt = time.Now().Add(tt.shift)
			notifier.qqMu.Unlock()

			require.NoError(t, notifier.Send(model.ChannelTypeQQ, string(rawCfg), sampleNote()))
			assert.Equal(t, tt.wantTokenCalls, m.tokenCalls.Load())
		})
	}
}

// TestChannelNotifier_QQTokenNeedsRefreshWindow 提前刷新窗口取 min(5 分钟, 总有效期/3)：
// 短寿命 token 按 1/3 收缩窗口，避免每次投递都重新取 token。
func TestChannelNotifier_QQTokenNeedsRefreshWindow(t *testing.T) {
	tests := []struct {
		name      string
		remaining time.Duration
		lifetime  time.Duration
		want      bool
	}{
		{"2 小时 token 剩 2 小时", 2 * time.Hour, 2 * time.Hour, false},
		{"2 小时 token 剩 2 分钟", 2 * time.Minute, 2 * time.Hour, true},
		{"2 小时 token 剩 4 分钟", 4 * time.Minute, 2 * time.Hour, true},
		{"2 小时 token 剩 10 分钟", 10 * time.Minute, 2 * time.Hour, false},
		{"60 秒 token 剩 30 秒", 30 * time.Second, time.Minute, false},
		{"60 秒 token 剩 10 秒", 10 * time.Second, time.Minute, true},
		{"已过期", -time.Second, 2 * time.Hour, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, qqTokenNeedsRefresh(time.Now().Add(tt.remaining), tt.lifetime))
		})
	}
}

// TestChannelNotifier_QQTokenSingleFlight 并发投递同一 appId 时只有一次在途取 token 请求。
func TestChannelNotifier_QQTokenSingleFlight(t *testing.T) {
	m := newQQMockAPI(t)
	// 拉长取 token 耗时，迫使并发调用在途汇合（其余等待后复用结果）。
	m.mu.Lock()
	m.tokenDelay = 100 * time.Millisecond
	m.mu.Unlock()

	notifier := NewChannelNotifier()
	rawCfg, _ := json.Marshal(ChannelConfig{
		AppID: "1020001", AppSecret: "plain",
		TargetType: qqTargetGroup, TargetID: "openid-group", BaseURL: m.srv.URL,
	})

	const concurrency = 6
	errs := make([]error, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = notifier.Send(model.ChannelTypeQQ, string(rawCfg), sampleNote())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "第 %d 个并发投递失败", i+1)
	}
	assert.Equal(t, int32(1), m.tokenCalls.Load(), "并发投递只应触发一次取 token")
	assert.Equal(t, int32(concurrency), m.messageCalls.Load())
}

// TestChannelNotifier_QQBusinessError 开放平台 HTTP 200 但 code 非 0 时必须判为失败。
func TestChannelNotifier_QQBusinessError(t *testing.T) {
	newCfg := func(baseURL string) string {
		raw, _ := json.Marshal(ChannelConfig{
			AppID: "1020001", AppSecret: "plain",
			TargetType: qqTargetGroup, TargetID: "openid-group", BaseURL: baseURL,
		})
		return string(raw)
	}

	t.Run("取 token 返回业务错误码", func(t *testing.T) {
		m := newQQMockAPI(t)
		m.setTokenResponse(http.StatusOK, `{"code":100007,"message":"appid or secret is invalid"}`)
		err := NewChannelNotifier().Send(model.ChannelTypeQQ, newCfg(m.srv.URL), sampleNote())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "100007")
		assert.Contains(t, err.Error(), "appid or secret is invalid")
		// 未取到 token 时不应发起投递。
		assert.Equal(t, int32(0), m.messageCalls.Load())
	})

	t.Run("发消息返回频控错误码", func(t *testing.T) {
		m := newQQMockAPI(t)
		m.setMessageResponse(http.StatusOK, `{"code":40034100,"message":"频率限制"}`)
		err := NewChannelNotifier().Send(model.ChannelTypeQQ, newCfg(m.srv.URL), sampleNote())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "40034100")
		assert.Contains(t, err.Error(), "频率限制")
	})

	t.Run("发消息返回内容含 URL 错误码", func(t *testing.T) {
		m := newQQMockAPI(t)
		m.setMessageResponse(http.StatusOK, `{"code":40054010,"message":"不允许发送URL"}`)
		err := NewChannelNotifier().Send(model.ChannelTypeQQ, newCfg(m.srv.URL), sampleNote())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "40054010")
	})

	t.Run("发消息返回 HTTP 非 2xx", func(t *testing.T) {
		m := newQQMockAPI(t)
		m.setMessageResponse(http.StatusBadGateway, `{"message":"bad gateway"}`)
		err := NewChannelNotifier().Send(model.ChannelTypeQQ, newCfg(m.srv.URL), sampleNote())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "502")
	})

	t.Run("取 token 返回 HTTP 非 2xx", func(t *testing.T) {
		m := newQQMockAPI(t)
		m.setTokenResponse(http.StatusUnauthorized, `{"message":"unauthorized"}`)
		err := NewChannelNotifier().Send(model.ChannelTypeQQ, newCfg(m.srv.URL), sampleNote())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})
}

// TestChannelNotifier_QQExpiresInShapes expires_in 的返回形态必须两种都接受：
// 官方参数表把它标为 number，但同一页的返回示例给的是字符串 "7200"。
// 只认一种形态会让整个响应反序列化失败，而取不到 token 意味着该通道全部投递失败。
func TestChannelNotifier_QQExpiresInShapes(t *testing.T) {
	cases := []struct {
		name    string
		resp    string
		wantErr bool
	}{
		{"数字形态", `{"access_token":"t","expires_in":7200}`, false},
		{"字符串形态", `{"access_token":"t","expires_in":"7200"}`, false},
		{"字段缺失回退默认值", `{"access_token":"t"}`, false},
		{"null 回退默认值", `{"access_token":"t","expires_in":null}`, false},
		{"空串回退默认值", `{"access_token":"t","expires_in":""}`, false},
		{"非数字字符串报错", `{"access_token":"t","expires_in":"soon"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newQQMockAPI(t)
			m.setTokenResponse(http.StatusOK, tc.resp)
			err := NewChannelNotifier().Send(model.ChannelTypeQQ, qqTestCfg(m.srv.URL), sampleNote())
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestQQAPIBase_SchemeGuard baseUrl 只接受 https，回环地址除外：
// 取 token 与发消息都要把 appSecret / AccessToken 发往该地址，明文 http 等同于凭证外泄。
func TestQQAPIBase_SchemeGuard(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		wantErr bool
		want    string
	}{
		{name: "留空取默认值", baseURL: "", want: qqDefaultAPIBase},
		{name: "官方 https", baseURL: "https://api.bot.qq.com", want: "https://api.bot.qq.com"},
		{name: "末斜杠归一", baseURL: "https://api.bot.qq.com/", want: "https://api.bot.qq.com"},
		{name: "非回环 http 被拒", baseURL: "http://api.example.com", wantErr: true},
		{name: "回环 http 放行（本地调试与测试）", baseURL: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{name: "localhost http 放行", baseURL: "http://localhost:8080", want: "http://localhost:8080"},
		{name: "非法地址被拒", baseURL: "://bad", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := qqAPIBase(&ChannelConfig{BaseURL: tc.baseURL})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestValidateChannelConfig_AppSecretUnderOtherType 前端把通道类型从 qq 切走后，
// 残留的明文 appSecret 仍会随 config 提交；校验必须与类型无关地拦住它，
// 否则明文密钥会随 config 落库（只按当前类型挑凭证字段是拦不住的）。
func TestValidateChannelConfig_AppSecretUnderOtherType(t *testing.T) {
	// 类型是 webhook，但 config 里残留明文 appSecret。
	err := validateChannelConfig(model.ChannelTypeWebhook, &ChannelConfig{
		URL: "${JM_WEBHOOK}", AppSecret: "plain",
	})
	require.ErrorIs(t, err, ErrCredentialNotEnvRef)

	// ${ENV} 形式放行。
	require.NoError(t, validateChannelConfig(model.ChannelTypeWebhook, &ChannelConfig{
		URL: "${JM_WEBHOOK}", AppSecret: "${JM_QQ_SECRET}",
	}))

	// 未填 appSecret 的既有类型不受影响。
	require.NoError(t, validateChannelConfig(model.ChannelTypeWebhook, &ChannelConfig{URL: "${JM_WEBHOOK}"}))
}

// TestChannelNotifier_QQTokenRejectedRetry 缓存令牌被平台提前作废时（HTTP 401 或
// 业务码 11242 / 11243），必须清缓存重取并重试一次——否则该通道会用失效令牌
// 一直失败到刷新窗口才自愈，期间告警静默丢失。
func TestChannelNotifier_QQTokenRejectedRetry(t *testing.T) {
	cases := []struct {
		name  string
		first qqMockResp
	}{
		{"HTTP 401", qqMockResp{http.StatusUnauthorized, `{"message":"unauthorized"}`}},
		{"业务码 11243 校验 token 未通过", qqMockResp{http.StatusOK, `{"code":11243,"message":"check token not pass"}`}},
		{"业务码 11242 校验 token 失败", qqMockResp{http.StatusOK, `{"code":11242,"message":"check token failed"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newQQMockAPI(t)
			m.setMessageSequence(tc.first, qqMockResp{http.StatusOK, `{"id":"msg-1"}`})
			err := NewChannelNotifier().Send(model.ChannelTypeQQ, qqTestCfg(m.srv.URL), sampleNote())
			require.NoError(t, err, "清缓存重取令牌后应当投递成功")
			assert.Equal(t, int32(2), m.messageCalls.Load(), "首次失败后应重试一次")
			assert.Equal(t, int32(2), m.tokenCalls.Load(), "重试前应清缓存并重新取令牌")
		})
	}

	t.Run("非令牌类业务错误不重试", func(t *testing.T) {
		m := newQQMockAPI(t)
		m.setMessageResponse(http.StatusOK, `{"code":40034100,"message":"频率限制"}`)
		err := NewChannelNotifier().Send(model.ChannelTypeQQ, qqTestCfg(m.srv.URL), sampleNote())
		require.Error(t, err)
		assert.Equal(t, int32(1), m.messageCalls.Load(), "频控属于业务失败，重试无意义")
		assert.Equal(t, int32(1), m.tokenCalls.Load(), "不应因业务失败而重取令牌")
	})
}
