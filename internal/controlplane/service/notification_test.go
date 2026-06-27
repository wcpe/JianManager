package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newNotifTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:notifdb_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Notification{}))
	return db
}

// 创建/未读数/列表/标记已读/全部已读 的端到端。
func TestNotificationService_Flow(t *testing.T) {
	db := newNotifTestDB(t)
	svc := NewNotificationService(db)

	require.NoError(t, svc.Create(1, model.NotificationLevelInfo, "a", "body-a", ""))
	require.NoError(t, svc.Create(1, model.NotificationLevelSuccess, "b", "body-b", "task-x"))
	require.NoError(t, svc.Create(2, model.NotificationLevelError, "c", "body-c", ""))

	// user1 未读数 = 2。
	n, err := svc.UnreadCount(1)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)

	// user1 列表（全部）2 条，最新在前。
	list, err := svc.List(1, false, 0)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "b", list[0].Title)

	// 标记第一条已读。
	require.NoError(t, svc.MarkRead(1, list[1].ID))
	n, _ = svc.UnreadCount(1)
	require.EqualValues(t, 1, n)

	// 仅未读列表只剩 1 条。
	unread, err := svc.List(1, true, 0)
	require.NoError(t, err)
	require.Len(t, unread, 1)

	// 全部已读。
	updated, err := svc.MarkAllRead(1)
	require.NoError(t, err)
	require.EqualValues(t, 1, updated)
	n, _ = svc.UnreadCount(1)
	require.Zero(t, n)
}

// 跨用户标记已读被拒（归属隔离）。
func TestNotificationService_MarkRead_CrossUserDenied(t *testing.T) {
	db := newNotifTestDB(t)
	svc := NewNotificationService(db)
	require.NoError(t, svc.Create(1, model.NotificationLevelInfo, "a", "", ""))
	var note model.Notification
	require.NoError(t, db.Where("user_id = ?", uint(1)).First(&note).Error)

	// user2 标记 user1 的站内信 → 不存在（隔离）。
	err := svc.MarkRead(2, note.ID)
	require.ErrorIs(t, err, ErrNotificationNotFound)

	// user1 自己标记成功；再标记一次幂等（已读）不报错。
	require.NoError(t, svc.MarkRead(1, note.ID))
	require.NoError(t, svc.MarkRead(1, note.ID))
}
