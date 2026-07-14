package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestResetUserPassword(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))

	locked := model.UserStatusDisabled
	require.NoError(t, db.Create(&model.User{
		Username: "admin", Password: "old-hash", Role: model.RolePlatformAdmin, Status: locked,
	}).Error)

	t.Run("重置密码并解锁", func(t *testing.T) {
		user, err := ResetUserPassword(db, "admin", "new-secret-123")
		require.NoError(t, err)
		assert.Equal(t, "admin", user.Username)

		var reloaded model.User
		require.NoError(t, db.Where("username = ?", "admin").First(&reloaded).Error)
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(reloaded.Password), []byte("new-secret-123")))
		assert.Equal(t, model.UserStatusActive, reloaded.Status)
	})

	t.Run("用户不存在", func(t *testing.T) {
		_, err := ResetUserPassword(db, "ghost", "x")
		assert.ErrorIs(t, err, ErrUserNotFound)
	})
}

func TestGenerateResetPassword(t *testing.T) {
	a, err := GenerateResetPassword()
	require.NoError(t, err)
	b, err := GenerateResetPassword()
	require.NoError(t, err)
	assert.Len(t, a, 16)
	assert.NotEqual(t, a, b)
}
