package service

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestBcryptPasswordHashing(t *testing.T) {
	password := "test-password-123"
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	require.NoError(t, err)

	// 验证正确密码
	err = bcrypt.CompareHashAndPassword(hashed, []byte(password))
	assert.NoError(t, err)

	// 验证错误密码
	err = bcrypt.CompareHashAndPassword(hashed, []byte("wrong-password"))
	assert.Error(t, err)
}

func TestTokenGeneration(t *testing.T) {
	secret := "test-secret-key"
	accessTTL := 15 * time.Minute

	user := &model.User{
		ID:       1,
		Username: "testuser",
		Role:     model.RolePlatformAdmin,
	}

	// 生成 access token
	now := time.Now()
	accessClaims := &Claims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessStr, err := accessToken.SignedString([]byte(secret))
	require.NoError(t, err)
	assert.NotEmpty(t, accessStr)

	// 解析并验证 access token
	parsedClaims := &Claims{}
	token, err := jwt.ParseWithClaims(accessStr, parsedClaims, func(t *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	require.NoError(t, err)
	assert.True(t, token.Valid)
	assert.Equal(t, uint(1), parsedClaims.UserID)
	assert.Equal(t, "testuser", parsedClaims.Username)
	assert.Equal(t, model.RolePlatformAdmin, parsedClaims.Role)
}

func TestTokenValidation_InvalidSecret(t *testing.T) {
	secret := "correct-secret"

	claims := &Claims{
		UserID:   1,
		Username: "user",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte(secret))

	// 用错误的密钥解析
	parsedClaims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, parsedClaims, func(t *jwt.Token) (interface{}, error) {
		return []byte("wrong-secret"), nil
	})
	assert.Error(t, err)
}

func TestTokenValidation_Expired(t *testing.T) {
	secret := "test-secret"

	claims := &Claims{
		UserID:   1,
		Username: "user",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)), // 已过期
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte(secret))

	parsedClaims := &Claims{}
	parsed, err := jwt.ParseWithClaims(tokenStr, parsedClaims, func(t *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	// 过期 token 会返回错误
	assert.Error(t, err)
	assert.False(t, parsed.Valid, "过期 token 应该无效")
}

func TestClaims_RoleValues(t *testing.T) {
	assert.Equal(t, model.UserRole(0), model.RoleMember)
	assert.Equal(t, model.UserRole(1), model.RoleGroupAdmin)
	assert.Equal(t, model.UserRole(10), model.RolePlatformAdmin)
}
