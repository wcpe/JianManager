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
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
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
