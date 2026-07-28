package service

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/smtp"
)

// ErrSMTPNotConfigured 表示 SMTP 配置不完整，调用方可降级为手动发送链接。
var ErrSMTPNotConfigured = errors.New("SMTP 未配置")

// SMTPMessageConfig 是发送单封邮件所需的 SMTP 连接配置。
type SMTPMessageConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// SMTPMessageSender 使用现有 SMTP STARTTLS/隐式 TLS 传输能力发送邮件。
type SMTPMessageSender struct{}

// Send 发送一封 UTF-8 纯文本邮件。
func (SMTPMessageSender) Send(config SMTPMessageConfig, to, subject, body string) error {
	return sendSMTPMessage(config, []string{to}, subject, body)
}

func sendSMTPMessage(config SMTPMessageConfig, recipients []string, subject, body string) error {
	if config.Host == "" || config.Port < 1 || config.Port > 65535 || config.From == "" || len(recipients) == 0 {
		return ErrSMTPNotConfigured
	}
	message := buildEmailMessage(config.From, recipients, subject, body)
	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)
	var auth smtp.Auth
	if config.Username != "" {
		auth = smtp.PlainAuth("", config.Username, config.Password, config.Host)
	}
	if config.Port != 465 {
		return smtp.SendMail(addr, auth, config.From, recipients, message)
	}
	return sendSMTPImplicitTLS(addr, config.Host, auth, config.From, recipients, message)
}

func sendSMTPImplicitTLS(addr, host string, auth smtp.Auth, from string, recipients []string, message []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("连接 SMTP(TLS) 失败: %w", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("建立 SMTP 会话失败: %w", err)
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err := c.Rcpt(recipient); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(message); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
