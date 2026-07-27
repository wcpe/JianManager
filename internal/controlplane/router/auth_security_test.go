package router

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func loginTokenPair(t *testing.T, r *gin.Engine, username, password string) service.TokenPair {
	t.Helper()
	w := makeRequest(r, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": username,
		"password": password,
	}, "")
	require.Equal(t, http.StatusOK, w.Code)
	var pair service.TokenPair
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pair))
	return pair
}

func TestAuth_TokenPurposeAndAlgorithmAreEnforced(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getMemberToken(t, r, "member", "password123")
	pair := loginTokenPair(t, r, "member", "password123")

	w := makeRequest(r, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": pair.AccessToken}, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code, "access token 不得用于刷新")

	w = makeRequest(r, http.MethodGet, "/api/v1/nodes", nil, pair.RefreshToken)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "refresh token 不得访问受保护端点")

	claims := jwt.MapClaims{
		"userId":    float64(1),
		"tokenType": "access",
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
	hs512 := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	forged, err := hs512.SignedString([]byte("test-secret-key-for-testing"))
	require.NoError(t, err)
	w = makeRequest(r, http.MethodGet, "/api/v1/nodes", nil, forged)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "仅允许 HS256")
}

func TestAuth_ExistingTokenIsInvalidAfterAccountSecurityChange(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getMemberToken(t, r, "member", "password123")
	var user model.User
	require.NoError(t, db.Where("username = ?", "member").First(&user).Error)

	newPassword := "newpassword456"
	_, err := service.NewUserService(db).Update(user.ID, nil, nil, &newPassword)
	require.NoError(t, err)
	w := makeRequest(r, http.MethodGet, "/api/v1/nodes", nil, token)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "重置密码后旧 access token 必须立即失效")
}

func TestAuth_ExistingTokenIsInvalidAfterUserIsDisabled(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getMemberToken(t, r, "member", "password123")
	var user model.User
	require.NoError(t, db.Where("username = ?", "member").First(&user).Error)
	status := model.UserStatusDisabled
	_, err := service.NewUserService(db).Update(user.ID, nil, &status, nil)
	require.NoError(t, err)

	w := makeRequest(r, http.MethodGet, "/api/v1/nodes", nil, token)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "禁用用户后旧 access token 必须立即失效")
}

func TestAuth_RequireRoleUsesCurrentDatabaseRole(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	var admin model.User
	require.NoError(t, db.Where("username = ?", "admin").First(&admin).Error)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", admin.ID).Update("role", model.RoleMember).Error)

	w := makeRequest(r, http.MethodGet, "/api/v1/users", nil, token)
	assert.Equal(t, http.StatusForbidden, w.Code, "角色变更后不得继续相信 token 内旧角色")
}

func TestAuth_SetupAdminConcurrentRequestsCreateOnlyOneAdmin(t *testing.T) {
	db := setupTestDB(t)
	left := service.NewAuthService(db, testJWTConfig())
	right := service.NewAuthService(db, testJWTConfig())
	left.SetPasswordCostForTest(4)
	right.SetPasswordCostForTest(4)

	results := make(chan error, 2)
	go func() { _, err := left.SetupAdmin("admin_one", "password123"); results <- err }()
	go func() { _, err := right.SetupAdmin("admin_two", "password123"); results <- err }()
	errA, errB := <-results, <-results
	assert.True(t, (errA == nil && errB == service.ErrAdminAlreadyExists) || (errB == nil && errA == service.ErrAdminAlreadyExists), "初始化只能有一个成功: %v, %v", errA, errB)

	var count int64
	require.NoError(t, db.Model(&model.User{}).Where("role = ?", model.RolePlatformAdmin).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func testJWTConfig() config.JWTConfig {
	return config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: time.Minute, RefreshTTL: time.Hour}
}
