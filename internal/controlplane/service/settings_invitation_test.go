package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/config"
)

func TestSettingsPlatformPublicBaseURLAllowsHTTPAndRejectsQueryOrFragment(t *testing.T) {
	db := newSettingsTestDB(t)
	svc := NewSettingsService(db, &config.Config{})

	require.NoError(t, svc.Update(map[string]string{"platform.public_base_url": "http://panel.example.com"}))
	require.NoError(t, svc.Update(map[string]string{"platform.public_base_url": "https://panel.example.com/path"}))
	for _, value := range []string{
		"http://panel.example.com/path?source=invite",
		"https://panel.example.com/path#fragment",
	} {
		require.ErrorIs(t, svc.Update(map[string]string{"platform.public_base_url": value}), ErrSettingValueInvalid)
	}

	require.ErrorIs(t, svc.Update(map[string]string{SettingKeyInviteSMTPPassword: "plain-password"}), ErrSettingValueInvalid)
	require.NoError(t, svc.Update(map[string]string{
		SettingKeyInviteSMTPPassword: "${INVITE_SMTP_PASSWORD}",
	}))
	view, err := svc.Get()
	require.NoError(t, err)
	baseURL, ok := findItem(view.Editable, "platform.public_base_url")
	require.True(t, ok)
	require.Equal(t, "https://panel.example.com/path", baseURL.Value)
	item, ok := findItem(view.Editable, SettingKeyInviteSMTPPassword)
	require.True(t, ok)
	require.True(t, item.Sensitive)
	require.Equal(t, "(已配置)", item.Value)
}
