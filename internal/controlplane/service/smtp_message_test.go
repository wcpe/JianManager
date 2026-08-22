package service

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSMTPMessage_ConfigValidation(t *testing.T) {
	// 缺少任一必填项（Host/Port/From/收件人）都返回 ErrSMTPNotConfigured。
	cases := []struct {
		name    string
		config  SMTPMessageConfig
		to      string
		wantErr bool
	}{
		{"empty host", SMTPMessageConfig{Port: 587, From: "a@x.com"}, "b@x.com", true},
		{"bad port 0", SMTPMessageConfig{Host: "smtp.x.com", Port: 0, From: "a@x.com"}, "b@x.com", true},
		{"bad port 70000", SMTPMessageConfig{Host: "smtp.x.com", Port: 70000, From: "a@x.com"}, "b@x.com", true},
		{"empty from", SMTPMessageConfig{Host: "smtp.x.com", Port: 587}, "b@x.com", true},
		{"empty recipient", SMTPMessageConfig{Host: "smtp.x.com", Port: 587, From: "a@x.com"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := (SMTPMessageSender{}).Send(tc.config, tc.to, "主题", "正文")
			require.ErrorIs(t, err, ErrSMTPNotConfigured)
		})
	}
}

func TestSMTPMessage_ValidConfigProceedsToConnect(t *testing.T) {
	// 配置合法：不应返回「未配置」，而是进入连接阶段（本机无 SMTP → 连接错误）。
	// 这验证配置校验闸在连接前生效，同时不依赖真实 SMTP 服务器。
	err := (SMTPMessageSender{}).Send(
		SMTPMessageConfig{Host: "127.0.0.1", Port: 1, From: "a@x.com"},
		"b@x.com", "主题", "正文",
	)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrSMTPNotConfigured, "配置合法不应报未配置")
}

func TestSMTPMessage_BuildMessageFormat(t *testing.T) {
	msg := buildEmailMessage("sender@example.com", []string{"a@x.com", "b@x.com"}, "邀请您加入", "你好，请点击链接完成注册。")
	text := string(msg)

	// From/To/主题 base64/UTF-8 内容类型。
	assert.Contains(t, text, "From: sender@example.com")
	assert.Contains(t, text, "To: a@x.com, b@x.com")
	assert.Contains(t, text, "Subject: =?UTF-8?B?")
	// 主题 base64 解码后应等于原文。
	encoded := strings.TrimSuffix(strings.TrimPrefix(
		strings.Split(strings.Split(text, "Subject: ")[1], "?=")[0], "=?UTF-8?B?"), "")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	assert.Equal(t, "邀请您加入", string(decoded))
	assert.Contains(t, text, "Content-Type: text/plain; charset=UTF-8")
	assert.Contains(t, text, "你好，请点击链接完成注册。")
	// RFC 822 行尾。
	assert.Contains(t, text, "\r\n")
}

func TestSMTPMessage_SplitRecipients(t *testing.T) {
	assert.Equal(t, []string{"a@x.com", "b@x.com"}, splitRecipients("a@x.com, b@x.com"))
	assert.Equal(t, []string{"a@x.com"}, splitRecipients(" a@x.com ; "))
	assert.Empty(t, splitRecipients("  , ; "))
}
