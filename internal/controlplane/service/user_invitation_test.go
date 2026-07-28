package service

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

type recordingInvitationSender struct {
	to      string
	subject string
	body    string
	err     error
}

func (s *recordingInvitationSender) Send(_ SMTPMessageConfig, to, subject, body string) error {
	s.to, s.subject, s.body = to, subject, body
	return s.err
}

func newInvitationTestService(t *testing.T) (*gorm.DB, *UserInvitationService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "invitation.db")), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserInvitation{}, &model.PlatformSetting{}, &model.AuditLog{}))
	require.NoError(t, db.Create(&model.PlatformSetting{Key: "platform.public_base_url", Value: "https://panel.example.com"}).Error)
	svc := NewUserInvitationService(db)
	svc.SetPasswordCostForTest(4)
	return db, svc
}

func TestUserInvitationDoesNotUseLegacyInvitationBaseURL(t *testing.T) {
	db, svc := newInvitationTestService(t)
	require.NoError(t, db.Where("key = ?", "platform.public_base_url").Delete(&model.PlatformSetting{}).Error)
	require.NoError(t, db.Create(&model.PlatformSetting{Key: "invite.public_base_url", Value: "http://legacy.example.com"}).Error)

	_, err := svc.Create(1, "member@example.com", false)
	require.ErrorIs(t, err, ErrInvitationPublicBaseURLNotConfigured)
}

func invitationToken(url string) string {
	return strings.TrimPrefix(strings.Split(url, "#")[1], "#")
}

func TestUserInvitationAcceptCreatesMemberWithoutPersistingToken(t *testing.T) {
	db, svc := newInvitationTestService(t)
	result, err := svc.Create(1, "member@example.com", false)
	require.NoError(t, err)
	require.Contains(t, result.InvitationURL, "https://panel.example.com/invite#")
	token := invitationToken(result.InvitationURL)
	require.NotContains(t, string(result.Invitation.TokenHash), token)

	user, err := svc.Accept(token, "invited-member", "password123")
	require.NoError(t, err)
	require.Equal(t, model.RoleMember, user.Role)
	require.Equal(t, model.UserStatusActive, user.Status)

	var invitation model.UserInvitation
	require.NoError(t, db.First(&invitation, result.Invitation.ID).Error)
	require.NotNil(t, invitation.UsedAt)
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "user.invitation.accept").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)
}

func TestUserInvitationUsernameConflictDoesNotConsumeInvitation(t *testing.T) {
	db, svc := newInvitationTestService(t)
	require.NoError(t, db.Create(&model.User{Username: "taken", Password: "x"}).Error)
	result, err := svc.Create(1, "member@example.com", false)
	require.NoError(t, err)
	token := invitationToken(result.InvitationURL)

	_, err = svc.Accept(token, "taken", "password123")
	require.ErrorIs(t, err, ErrUserExists)
	user, err := svc.Accept(token, "available", "password123")
	require.NoError(t, err)
	require.Equal(t, "available", user.Username)
}

func TestUserInvitationConcurrentAcceptOnlyOneSucceeds(t *testing.T) {
	_, svc := newInvitationTestService(t)
	result, err := svc.Create(1, "member@example.com", false)
	require.NoError(t, err)
	token := invitationToken(result.InvitationURL)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, username := range []string{"member-one", "member-two"} {
		wg.Add(1)
		go func(username string) {
			defer wg.Done()
			<-start
			_, err := svc.Accept(token, username, "password123")
			results <- err
		}(username)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	invalids := 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrInvitationInvalid) {
			invalids++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, invalids)
}

func TestUserInvitationExpiredAndRevokedAreIndistinguishable(t *testing.T) {
	db, svc := newInvitationTestService(t)
	expired, err := svc.Create(1, "expired@example.com", false)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.UserInvitation{}).Where("id = ?", expired.Invitation.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	_, err = svc.Accept(invitationToken(expired.InvitationURL), "expired", "password123")
	require.ErrorIs(t, err, ErrInvitationInvalid)

	revoked, err := svc.Create(1, "revoked@example.com", false)
	require.NoError(t, err)
	require.NoError(t, svc.Revoke(revoked.Invitation.ID))
	_, err = svc.Accept(invitationToken(revoked.InvitationURL), "revoked", "password123")
	require.ErrorIs(t, err, ErrInvitationInvalid)
}

func TestUserInvitationSMTPFallsBackToManualLinkAndRecordsSuccessfulDelivery(t *testing.T) {
	db, svc := newInvitationTestService(t)
	manual, err := svc.Create(1, "manual@example.com", true)
	require.NoError(t, err)
	require.Equal(t, "not_configured", manual.Invitation.EmailDelivery)
	require.NotEmpty(t, manual.InvitationURL)

	t.Setenv("INVITE_SMTP_PASSWORD", "test-password")
	for key, value := range map[string]string{
		SettingKeyInviteSMTPHost:     "smtp.example.com",
		SettingKeyInviteSMTPPort:     "587",
		SettingKeyInviteSMTPUsername: "mailer",
		SettingKeyInviteSMTPPassword: "${INVITE_SMTP_PASSWORD}",
		SettingKeyInviteSMTPFrom:     "mailer@example.com",
	} {
		require.NoError(t, db.Save(&model.PlatformSetting{Key: key, Value: value}).Error)
	}
	sender := &recordingInvitationSender{}
	svc.SetEmailSenderForTest(sender)
	sent, err := svc.Create(1, "sent@example.com", true)
	require.NoError(t, err)
	require.Equal(t, "sent", sent.Invitation.EmailDelivery)
	require.NotNil(t, sent.Invitation.EmailSentAt)
	require.Equal(t, "sent@example.com", sender.to)
	require.Contains(t, sender.body, sent.InvitationURL)
}
