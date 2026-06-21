package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/database"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func setupAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmpDir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmpDir+"/test.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	})
	return db
}

func TestAuditService_List_WithTimeFilter(t *testing.T) {
	db := setupAuditTestDB(t)
	svc := NewAuditService(db)

	// 创建用户（外键需要）
	user := &model.User{
		UUID:     "test-user-uuid",
		Username: "testuser",
		Password: "hashed",
		Role:     model.RolePlatformAdmin,
		Status:   model.UserStatusActive,
	}
	require.NoError(t, db.Create(user).Error)

	// 创建不同时间的审计日志
	now := time.Now()
	logs := []model.AuditLog{
		{UserID: user.ID, Action: "instance.start", TargetType: "instance", TargetID: "1", CreatedAt: now.Add(-3 * time.Hour)},
		{UserID: user.ID, Action: "instance.stop", TargetType: "instance", TargetID: "1", CreatedAt: now.Add(-2 * time.Hour)},
		{UserID: user.ID, Action: "user.create", TargetType: "user", TargetID: "2", CreatedAt: now.Add(-1 * time.Hour)},
	}
	for i := range logs {
		logs[i].UUID = fmt.Sprintf("log-uuid-%d", i)
		require.NoError(t, db.Create(&logs[i]).Error)
	}

	// 测试 From 过滤：只查最近 2 小时内的
	from := now.Add(-2*time.Hour - 30*time.Minute)
	filter := AuditFilter{From: &from}
	result, err := svc.List(filter)
	assert.NoError(t, err)
	assert.Len(t, result, 2) // instance.stop + user.create

	// 测试 To 过滤：只查 2 小时前的
	to := now.Add(-2*time.Hour + 30*time.Minute)
	filter = AuditFilter{To: &to}
	result, err = svc.List(filter)
	assert.NoError(t, err)
	assert.Len(t, result, 2) // instance.start + instance.stop

	// 测试 From + To 范围
	from2 := now.Add(-2*time.Hour - 30*time.Minute)
	to2 := now.Add(-1*time.Hour + 30*time.Minute)
	filter = AuditFilter{From: &from2, To: &to2}
	result, err = svc.List(filter)
	assert.NoError(t, err)
	assert.Len(t, result, 2) // instance.stop + user.create

	// 测试无过滤：返回全部
	filter = AuditFilter{}
	result, err = svc.List(filter)
	assert.NoError(t, err)
	assert.Len(t, result, 3)
}

func TestAuditService_List_WithActionFilter(t *testing.T) {
	db := setupAuditTestDB(t)
	svc := NewAuditService(db)

	user := &model.User{
		UUID:     "test-user-uuid-2",
		Username: "testuser2",
		Password: "hashed",
		Role:     model.RolePlatformAdmin,
		Status:   model.UserStatusActive,
	}
	require.NoError(t, db.Create(user).Error)

	require.NoError(t, db.Create(&model.AuditLog{UUID: "log-1", UserID: user.ID, Action: "instance.start", TargetType: "instance", TargetID: "1"}).Error)
	require.NoError(t, db.Create(&model.AuditLog{UUID: "log-2", UserID: user.ID, Action: "instance.stop", TargetType: "instance", TargetID: "1"}).Error)

	action := "instance.start"
	filter := AuditFilter{Action: &action}
	result, err := svc.List(filter)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "instance.start", result[0].Action)
}
