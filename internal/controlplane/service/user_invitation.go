package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const invitationValidity = 7 * 24 * time.Hour

var (
	// ErrInvitationInvalid 统一隐藏邀请是否存在、已用、撤销或过期的状态。
	ErrInvitationInvalid = errors.New("邀请无效")
	// ErrInvitationAlreadyUsed 表示已接受邀请不可撤销。
	ErrInvitationAlreadyUsed = errors.New("邀请已使用")
	// ErrInvitationPublicBaseURLNotConfigured 表示无法生成邀请链接。
	ErrInvitationPublicBaseURLNotConfigured = errors.New("未配置平台公共基址")
)

// InvitationEmailSender 抽象邮件投递，测试可替换为内存实现。
type InvitationEmailSender interface {
	Send(config SMTPMessageConfig, to, subject, body string) error
}

// UserInvitationService 处理邀请签发、撤销与原子接受。
type UserInvitationService struct {
	db           *gorm.DB
	passwordCost int
	sender       InvitationEmailSender
}

// NewUserInvitationService 创建邀请服务。
func NewUserInvitationService(db *gorm.DB) *UserInvitationService {
	return &UserInvitationService{db: db, passwordCost: bcrypt.DefaultCost, sender: SMTPMessageSender{}}
}

// SetPasswordCostForTest 设置测试 bcrypt 成本。
func (s *UserInvitationService) SetPasswordCostForTest(cost int) { s.passwordCost = cost }

// SetEmailSenderForTest 替换邀请邮件发送器。
func (s *UserInvitationService) SetEmailSenderForTest(sender InvitationEmailSender) {
	s.sender = sender
}

// InvitationIssueResult 是签发邀请的对外结果；URL 仅在本次签发中返回。
type InvitationIssueResult struct {
	Invitation    *model.UserInvitation
	InvitationURL string
}

// Create 签发成员邀请；邮件失败不会回滚邀请，管理员仍可手动发送一次性链接。
func (s *UserInvitationService) Create(createdByID uint, email string, sendEmail bool) (*InvitationIssueResult, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, err
	}
	baseURL, err := s.publicBaseURL()
	if err != nil {
		return nil, err
	}
	token, hash, err := newInvitationToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	invitation := &model.UserInvitation{
		Email:         email,
		TokenHash:     hash,
		TokenPrefix:   token[:8],
		Role:          model.RoleMember,
		ExpiresAt:     now.Add(invitationValidity),
		CreatedByID:   createdByID,
		EmailDelivery: "not_configured",
	}
	if err := s.db.Create(invitation).Error; err != nil {
		return nil, fmt.Errorf("创建邀请失败: %w", err)
	}
	invitationURL := strings.TrimRight(baseURL, "/") + "/invite#" + token
	if !sendEmail {
		return &InvitationIssueResult{Invitation: invitation, InvitationURL: invitationURL}, nil
	}
	if err := s.sendInvitationEmail(invitation.Email, invitationURL); err != nil {
		if errors.Is(err, ErrSMTPNotConfigured) {
			return &InvitationIssueResult{Invitation: invitation, InvitationURL: invitationURL}, nil
		}
		if updateErr := s.db.Model(invitation).Update("email_delivery", "failed").Error; updateErr != nil {
			return nil, fmt.Errorf("记录邀请邮件失败状态失败: %w", updateErr)
		}
		invitation.EmailDelivery = "failed"
		return &InvitationIssueResult{Invitation: invitation, InvitationURL: invitationURL}, nil
	}
	if err := s.db.Model(invitation).Updates(map[string]any{"email_delivery": "sent", "email_sent_at": now}).Error; err != nil {
		return nil, fmt.Errorf("记录邀请邮件发送状态失败: %w", err)
	}
	invitation.EmailDelivery, invitation.EmailSentAt = "sent", &now
	return &InvitationIssueResult{Invitation: invitation, InvitationURL: invitationURL}, nil
}

// List 返回全部未软删除邀请，令牌哈希不会序列化。
func (s *UserInvitationService) List() ([]model.UserInvitation, error) {
	var invitations []model.UserInvitation
	if err := s.db.Order("created_at DESC, id DESC").Find(&invitations).Error; err != nil {
		return nil, fmt.Errorf("查询邀请失败: %w", err)
	}
	return invitations, nil
}

// Revoke 撤销未使用邀请。
func (s *UserInvitationService) Revoke(id uint) error {
	now := time.Now()
	result := s.db.Model(&model.UserInvitation{}).Where("id = ? AND used_at IS NULL AND revoked_at IS NULL", id).Update("revoked_at", now)
	if result.Error != nil {
		return fmt.Errorf("撤销邀请失败: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}
	var invitation model.UserInvitation
	if err := s.db.First(&invitation, id).Error; err != nil {
		return fmt.Errorf("查询邀请失败: %w", err)
	}
	if invitation.UsedAt != nil {
		return ErrInvitationAlreadyUsed
	}
	return nil
}

// Accept 原子消费有效邀请并创建 active member；用户名冲突会回滚消费。
func (s *UserInvitationService) Accept(token, username, password string) (*model.User, error) {
	if len(token) < 32 {
		return nil, ErrInvitationInvalid
	}
	hash := sha256.Sum256([]byte(token))
	now := time.Now()
	var user *model.User
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var invitation model.UserInvitation
		if err := tx.Where("token_hash = ?", hash[:]).First(&invitation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvitationInvalid
			}
			return fmt.Errorf("查询邀请失败: %w", err)
		}
		result := tx.Model(&model.UserInvitation{}).
			Where("id = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", invitation.ID, now).
			Update("used_at", now)
		if result.Error != nil {
			return fmt.Errorf("消费邀请失败: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrInvitationInvalid
		}
		hashed, err := bcrypt.GenerateFromPassword([]byte(password), s.passwordCost)
		if err != nil {
			return fmt.Errorf("加密密码失败: %w", err)
		}
		user = &model.User{Username: username, Password: string(hashed), Role: model.RoleMember, Status: model.UserStatusActive}
		if err := tx.Create(user).Error; err != nil {
			if isUniqueConstraint(err) {
				return ErrUserExists
			}
			return fmt.Errorf("创建受邀用户失败: %w", err)
		}
		if err := tx.Create(&model.AuditLog{
			UserID: user.ID, Action: "user.invitation.accept", TargetType: "user_invitation", TargetID: strconv.FormatUint(uint64(invitation.ID), 10), Detail: "{}",
		}).Error; err != nil {
			return fmt.Errorf("记录接受邀请审计失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

func normalizeEmail(raw string) (string, error) {
	address, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || address.Address != strings.TrimSpace(raw) || !strings.Contains(address.Address, "@") {
		return "", fmt.Errorf("邮箱地址非法")
	}
	return address.Address, nil
}

func newInvitationToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("生成邀请令牌失败: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func (s *UserInvitationService) publicBaseURL() (string, error) {
	var setting model.PlatformSetting
	if err := s.db.First(&setting, "key = ?", SettingKeyPlatformPublicBaseURL).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrInvitationPublicBaseURLNotConfigured
		}
		return "", fmt.Errorf("读取邀请公共基址失败: %w", err)
	}
	if err := validatePublicBaseURL(setting.Value); err != nil {
		return "", ErrInvitationPublicBaseURLNotConfigured
	}
	return setting.Value, nil
}

func (s *UserInvitationService) sendInvitationEmail(to, invitationURL string) error {
	settings, err := s.invitationSMTPSettings()
	if err != nil {
		return err
	}
	body := "您已获邀加入 JianManager。请在 7 天内打开以下链接设置用户名和密码：\n\n" + invitationURL
	return s.sender.Send(settings, to, "JianManager 邀请", body)
}

func (s *UserInvitationService) invitationSMTPSettings() (SMTPMessageConfig, error) {
	values := map[string]string{}
	var settings []model.PlatformSetting
	if err := s.db.Where("key IN ?", []string{SettingKeyInviteSMTPHost, SettingKeyInviteSMTPPort, SettingKeyInviteSMTPUsername, SettingKeyInviteSMTPPassword, SettingKeyInviteSMTPFrom}).Find(&settings).Error; err != nil {
		return SMTPMessageConfig{}, fmt.Errorf("读取邀请 SMTP 配置失败: %w", err)
	}
	for _, setting := range settings {
		values[setting.Key] = setting.Value
	}
	port, err := strconv.Atoi(values[SettingKeyInviteSMTPPort])
	if values[SettingKeyInviteSMTPHost] == "" || values[SettingKeyInviteSMTPFrom] == "" || err != nil || port < 1 || port > 65535 {
		return SMTPMessageConfig{}, ErrSMTPNotConfigured
	}
	password, err := resolveEnvironmentReference(values[SettingKeyInviteSMTPPassword])
	if err != nil {
		return SMTPMessageConfig{}, ErrSMTPNotConfigured
	}
	return SMTPMessageConfig{Host: values[SettingKeyInviteSMTPHost], Port: port, Username: values[SettingKeyInviteSMTPUsername], Password: password, From: values[SettingKeyInviteSMTPFrom]}, nil
}

var environmentReferencePattern = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

func resolveEnvironmentReference(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	match := environmentReferencePattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return "", errors.New("SMTP 密码必须为环境变量引用")
	}
	resolved, ok := os.LookupEnv(match[1])
	if !ok {
		return "", errors.New("SMTP 密码环境变量未配置")
	}
	return resolved, nil
}

func validatePublicBaseURL(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("平台公共基址必须为无查询参数或片段的 HTTP/HTTPS 地址")
	}
	return nil
}
