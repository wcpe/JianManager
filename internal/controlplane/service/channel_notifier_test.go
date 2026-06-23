package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
