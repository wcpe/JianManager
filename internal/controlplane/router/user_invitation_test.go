package router

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func TestUserCreateRequiresPlatformAdminAndSupportsMemberRoleZero(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/users", map[string]interface{}{
		"username": "direct-member", "password": "password123", "role": 0, "status": 0,
	}, admin)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	response := parseJSON(t, w)
	require.Equal(t, "direct-member", response["username"])
	require.Equal(t, float64(0), response["role"])
	require.Equal(t, float64(0), response["status"])

	member := getMemberToken(t, r, "unprivileged", "password123")
	w = makeRequest(r, http.MethodPost, "/api/v1/users", map[string]interface{}{
		"username": "forbidden", "password": "password123", "role": 0, "status": 0,
	}, member)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestUserInvitationResponseHidesTokenAndListsLifecycleFlags(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&model.PlatformSetting{Key: service.SettingKeyPlatformPublicBaseURL, Value: "https://panel.example.com"}).Error)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/users/invitations", map[string]interface{}{
		"email": "member@example.com", "sendEmail": false,
	}, admin)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	created := parseJSON(t, w)
	invitationURL, ok := created["invitationUrl"].(string)
	require.True(t, ok)
	token := strings.Split(invitationURL, "#")[1]

	w = makeRequest(r, http.MethodGet, "/api/v1/users/invitations", nil, admin)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), token)
	items := parseJSONArray(t, w)
	require.Len(t, items, 1)
	item := items[0].(map[string]interface{})
	require.Equal(t, false, item["used"])
	require.Equal(t, false, item["revoked"])

	w = makeRequest(r, http.MethodPost, "/api/v1/auth/invitations/accept", map[string]string{
		"token": token, "username": "invited-member", "password": "password123",
	}, "")
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = makeRequest(r, http.MethodGet, "/api/v1/users/invitations", nil, admin)
	require.Equal(t, http.StatusOK, w.Code)
	items = parseJSONArray(t, w)
	item = items[0].(map[string]interface{})
	require.Equal(t, true, item["used"])
	require.Equal(t, false, item["revoked"])
}

func TestUserInvitationRequiresConfiguredHTTPSBaseURL(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/users/invitations", map[string]interface{}{
		"email": "member@example.com", "sendEmail": false,
	}, admin)
	require.Equal(t, http.StatusBadRequest, w.Code)
}
