package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-560 / ADR-112：用户令牌的签发、校验、交集与吊销。

// setupUserTokenDB 建内存库并迁移用户令牌相关表。
func setupUserTokenDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserToken{}, &model.DeviceCode{}))
	return db
}

// seedUser 建一个启用用户。
func seedUser(t *testing.T, db *gorm.DB, username string, role model.UserRole) *model.User {
	t.Helper()
	u := &model.User{
		Username: username,
		Password: "x",
		Role:     role,
		Status:   model.UserStatusActive,
	}
	require.NoError(t, db.Create(u).Error)
	return u
}

func TestUserToken_CreateAndAuthenticate(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	user := seedUser(t, db, "alice", model.RoleMember)

	res, err := svc.Create(user.ID, CreateUserTokenRequest{Name: "我的笔记本"})
	require.NoError(t, err)
	require.NotEmpty(t, res.Plaintext)
	assert.Contains(t, res.Plaintext, UserTokenPrefix, "明文应带 jmut_ 前缀")
	assert.NotEmpty(t, res.Token.TokenPrefix)
	assert.NotEqual(t, res.Plaintext, res.Token.TokenHash, "库内不得存明文")

	// 明文可校验通过。
	p, err := svc.Authenticate(res.Plaintext)
	require.NoError(t, err)
	assert.Equal(t, user.ID, p.UserID)
	assert.Equal(t, "alice", p.Username)

	// 错误明文被拒。
	_, err = svc.Authenticate(UserTokenPrefix + "bogus")
	assert.ErrorIs(t, err, ErrUserTokenInvalid)

	// 空明文被拒。
	_, err = svc.Authenticate("")
	assert.ErrorIs(t, err, ErrUserTokenInvalid)
}

func TestUserToken_HashNotReversible(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	user := seedUser(t, db, "bob", model.RoleMember)

	res, _ := svc.Create(user.ID, CreateUserTokenRequest{})
	assert.Len(t, res.Token.TokenHash, 64, "sha256 十六进制应为 64 字符")
	assert.NotContains(t, res.Token.TokenHash, res.Plaintext)
}

func TestUserToken_RevokedRejected(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	user := seedUser(t, db, "carol", model.RoleMember)

	res, err := svc.Create(user.ID, CreateUserTokenRequest{})
	require.NoError(t, err)

	require.NoError(t, svc.Revoke(res.Token.ID, user.ID, false))
	_, err = svc.Authenticate(res.Plaintext)
	assert.ErrorIs(t, err, ErrUserTokenRevoked)

	// 幂等：重复吊销不报错。
	assert.NoError(t, svc.Revoke(res.Token.ID, user.ID, false))
}

func TestUserToken_ExpiredRejected(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	user := seedUser(t, db, "dave", model.RoleMember)

	res, _ := svc.Create(user.ID, CreateUserTokenRequest{})
	// 直接把过期时间改到过去。
	require.NoError(t, db.Model(&model.UserToken{}).Where("id = ?", res.Token.ID).
		Update("expires_at", time.Now().Add(-time.Hour)).Error)

	_, err := svc.Authenticate(res.Plaintext)
	assert.ErrorIs(t, err, ErrUserTokenExpired)
}

// TestUserToken_AuthVersionBumpInvalidates 改密码（AuthVersion 递增）后全部令牌失效。
func TestUserToken_AuthVersionBumpInvalidates(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	user := seedUser(t, db, "erin", model.RoleMember)

	res, _ := svc.Create(user.ID, CreateUserTokenRequest{})
	_, err := svc.Authenticate(res.Plaintext)
	require.NoError(t, err)

	// 模拟改密码：AuthVersion 递增。
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.ID).
		Update("auth_version", gorm.Expr("auth_version + 1")).Error)

	_, err = svc.Authenticate(res.Plaintext)
	assert.ErrorIs(t, err, ErrUserTokenMismatch, "AuthVersion 变化应使令牌失效")
}

// TestUserToken_DisabledUserRejected 用户被禁用后令牌失效。
func TestUserToken_DisabledUserRejected(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	user := seedUser(t, db, "frank", model.RoleMember)

	res, _ := svc.Create(user.ID, CreateUserTokenRequest{})
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.ID).
		Update("status", model.UserStatusDisabled).Error)

	_, err := svc.Authenticate(res.Plaintext)
	assert.ErrorIs(t, err, ErrUserTokenInvalid)
}

// TestUserToken_RevokeCrossUser 用户不能吊销他人令牌。
func TestUserToken_RevokeCrossUser(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	alice := seedUser(t, db, "alice2", model.RoleMember)
	bob := seedUser(t, db, "bob2", model.RoleMember)

	res, _ := svc.Create(alice.ID, CreateUserTokenRequest{})

	// bob 尝试吊销 alice 的令牌。
	err := svc.Revoke(res.Token.ID, bob.ID, false)
	assert.ErrorIs(t, err, ErrAgentForbidden)

	// 令牌仍有效。
	_, err = svc.Authenticate(res.Plaintext)
	assert.NoError(t, err)

	// 管理员代操作可成功。
	assert.NoError(t, svc.Revoke(res.Token.ID, bob.ID, true))
	_, err = svc.Authenticate(res.Plaintext)
	assert.ErrorIs(t, err, ErrUserTokenRevoked)
}

// TestUserToken_RevokeNonexistentConverges 不存在与越权统一收敛。
func TestUserToken_RevokeNonexistentConverges(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	user := seedUser(t, db, "grace", model.RoleMember)

	err := svc.Revoke(99999, user.ID, false)
	assert.ErrorIs(t, err, ErrAgentForbidden, "不存在应与越权同码，避免存在性探针")
}

func TestUserToken_ListOnlyOwn(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	alice := seedUser(t, db, "alice3", model.RoleMember)
	bob := seedUser(t, db, "bob3", model.RoleMember)

	_, _ = svc.Create(alice.ID, CreateUserTokenRequest{Name: "a1"})
	_, _ = svc.Create(alice.ID, CreateUserTokenRequest{Name: "a2"})
	_, _ = svc.Create(bob.ID, CreateUserTokenRequest{Name: "b1"})

	aliceTokens, err := svc.List(alice.ID)
	require.NoError(t, err)
	assert.Len(t, aliceTokens, 2)
	for _, tok := range aliceTokens {
		assert.Equal(t, alice.ID, tok.UserID)
	}
}

// TestUserToken_AdminCapabilitiesNotNarrowed 平台管理员令牌的 capability 不被裁剪。
func TestUserToken_AdminCapabilitiesNotNarrowed(t *testing.T) {
	db := setupUserTokenDB(t)
	admin := seedUser(t, db, "root", model.RolePlatformAdmin)
	svc := NewUserTokenService(db, NewAuthzService(db))

	res, err := svc.Create(admin.ID, CreateUserTokenRequest{
		Capabilities: []string{AgentCapabilityInstanceLife},
	})
	require.NoError(t, err)

	p, err := svc.Authenticate(res.Plaintext)
	require.NoError(t, err)
	assert.Contains(t, p.Capabilities, AgentCapabilityInstanceLife)
}

// TestUserToken_TTLCap TTL 超过上限时被截断。
func TestUserToken_TTLCap(t *testing.T) {
	db := setupUserTokenDB(t)
	svc := NewUserTokenService(db, nil)
	user := seedUser(t, db, "henry", model.RoleMember)

	// 请求 10 年，应被截到 365 天。
	res, err := svc.Create(user.ID, CreateUserTokenRequest{TTLSeconds: 10 * 365 * 24 * 3600})
	require.NoError(t, err)
	assert.True(t, res.Token.ExpiresAt.Before(time.Now().Add(UserTokenMaxTTL+time.Minute)),
		"TTL 应被截断到上限")
}
