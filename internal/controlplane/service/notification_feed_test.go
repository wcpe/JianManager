package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newFeedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:feeddb_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Notification{}, &model.AlertRule{}, &model.AlertEvent{}))
	return db
}

// 播种：user1 两条站内信（一未读一已读）+ 两条告警（一未读一已读），跨越不同时间。
func seedFeed(t *testing.T, db *gorm.DB) {
	t.Helper()
	base := time.Now()
	notifSvc := NewNotificationService(db)
	require.NoError(t, notifSvc.Create(1, model.NotificationLevelSuccess, "JDK 安装完成", "node-1 Temurin 21", "task-1"))
	require.NoError(t, notifSvc.Create(1, model.NotificationLevelError, "备份失败", "磁盘空间不足", ""))
	// user2 的站内信不应被 user1 看到。
	require.NoError(t, notifSvc.Create(2, model.NotificationLevelInfo, "他人消息", "不可见", ""))
	// 把 user1 的「JDK 安装完成」标记已读，使 user1 余 1 条未读站内信（配合下方 1 条未读告警）。
	var jdkNotif model.Notification
	require.NoError(t, db.Where("user_id = ? AND title = ?", 1, "JDK 安装完成").First(&jdkNotif).Error)
	require.NoError(t, notifSvc.MarkRead(1, jdkNotif.ID))

	rule := &model.AlertRule{Name: "CPU 过载", TargetType: "node"}
	require.NoError(t, db.Create(rule).Error)
	// 一条未读 warn 告警（较新）+ 一条已读 critical 告警（较旧）。
	require.NoError(t, db.Create(&model.AlertEvent{
		RuleID: rule.ID, Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		Message: "CPU 91% 超阈值", Read: false, FiredAt: base.Add(1 * time.Minute),
	}).Error)
	require.NoError(t, db.Create(&model.AlertEvent{
		RuleID: rule.ID, Level: model.AlertLevelCritical, TriggerType: model.AlertTriggerInstanceCrash,
		Message: "实例崩溃", Read: true, FiredAt: base.Add(-10 * time.Minute),
	}).Error)
}

func newFeedSvc(db *gorm.DB) *NotificationFeedService {
	return NewNotificationFeedService(db, NewNotificationService(db), NewAlertService(db))
}

// 合并流：user1 全部=2 站内信 + 2 告警=4 条；按时间倒序；带来源与级别映射。
func TestFeed_Merge(t *testing.T) {
	db := newFeedTestDB(t)
	seedFeed(t, db)
	svc := newFeedSvc(db)

	items, total, err := svc.Feed(1, FeedFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 4, total)
	require.Len(t, items, 4)

	// 含两种来源。
	var msgCount, alertCount int
	for _, it := range items {
		switch it.Source {
		case FeedSourceMessage:
			msgCount++
		case FeedSourceAlert:
			alertCount++
		}
	}
	require.Equal(t, 2, msgCount)
	require.Equal(t, 2, alertCount)

	// 倒序：相邻项时间不递增。
	for i := 1; i < len(items); i++ {
		require.False(t, items[i].CreatedAt.After(items[i-1].CreatedAt), "应按时间倒序")
	}

	// 级别映射：找到 warn 告警 → warning；critical → error。
	for _, it := range items {
		if it.Source == FeedSourceAlert && it.Body == "CPU 91% 超阈值" {
			require.Equal(t, string(model.NotificationLevelWarning), it.Level)
		}
		if it.Source == FeedSourceAlert && it.Body == "实例崩溃" {
			require.Equal(t, string(model.NotificationLevelError), it.Level)
		}
	}
}

// 来源筛选：只看消息 / 只看告警。
func TestFeed_SourceFilter(t *testing.T) {
	db := newFeedTestDB(t)
	seedFeed(t, db)
	svc := newFeedSvc(db)

	msgs, mTotal, err := svc.Feed(1, FeedFilter{Source: FeedSourceMessage})
	require.NoError(t, err)
	require.EqualValues(t, 2, mTotal)
	for _, it := range msgs {
		require.Equal(t, FeedSourceMessage, it.Source)
	}

	alerts, aTotal, err := svc.Feed(1, FeedFilter{Source: FeedSourceAlert})
	require.NoError(t, err)
	require.EqualValues(t, 2, aTotal)
	for _, it := range alerts {
		require.Equal(t, FeedSourceAlert, it.Source)
	}
}

// 仅未读：1 未读站内信 + 1 未读告警 = 2。
func TestFeed_UnreadFilter(t *testing.T) {
	db := newFeedTestDB(t)
	seedFeed(t, db)
	svc := newFeedSvc(db)

	items, total, err := svc.Feed(1, FeedFilter{Unread: true})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, items, 2)
	for _, it := range items {
		require.False(t, it.Read)
	}
}

// 关键字：同时作用两源标题/正文。
func TestFeed_Keyword(t *testing.T) {
	db := newFeedTestDB(t)
	seedFeed(t, db)
	svc := newFeedSvc(db)

	// "失败" 命中站内信「备份失败」标题。
	items, total, err := svc.Feed(1, FeedFilter{Keyword: "失败"})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	require.Equal(t, FeedSourceMessage, items[0].Source)

	// "CPU" 命中告警正文。
	items2, total2, err := svc.Feed(1, FeedFilter{Keyword: "CPU"})
	require.NoError(t, err)
	require.EqualValues(t, 1, total2)
	require.Equal(t, FeedSourceAlert, items2[0].Source)
}

// 分页：pageSize=1 时跨源归并切片，total=4，可翻 4 页。
func TestFeed_Paging(t *testing.T) {
	db := newFeedTestDB(t)
	seedFeed(t, db)
	svc := newFeedSvc(db)

	seen := map[string]bool{}
	for p := 1; p <= 4; p++ {
		items, total, err := svc.Feed(1, FeedFilter{Page: p, PageSize: 1})
		require.NoError(t, err)
		require.EqualValues(t, 4, total)
		require.Len(t, items, 1)
		key := items[0].Source + ":" + itoa(items[0].ID)
		require.False(t, seen[key], "分页不应重复条目")
		seen[key] = true
	}
	require.Len(t, seen, 4)

	// 越界页返回空。
	empty, _, err := svc.Feed(1, FeedFilter{Page: 99, PageSize: 1})
	require.NoError(t, err)
	require.Empty(t, empty)
}

// 注：itoa 复用 alert_dispatcher_test.go 中包内既有同名工具。

// 未读计数 = 本人未读站内信 + 全局未读告警。
func TestFeed_UnreadCount(t *testing.T) {
	db := newFeedTestDB(t)
	seedFeed(t, db)
	svc := newFeedSvc(db)

	n, err := svc.UnreadCount(1)
	require.NoError(t, err)
	require.EqualValues(t, 2, n) // 1 站内信未读 + 1 告警未读
}

// 标记单条已读：按 source 下推；message 跨用户被拒。
func TestFeed_MarkRead(t *testing.T) {
	db := newFeedTestDB(t)
	seedFeed(t, db)
	svc := newFeedSvc(db)

	// 找一条未读站内信。
	var unreadNotif model.Notification
	require.NoError(t, db.Where("user_id = ? AND read_at IS NULL", 1).First(&unreadNotif).Error)
	require.NoError(t, svc.MarkRead(1, FeedSourceMessage, unreadNotif.ID))

	// 跨用户标记被拒。
	require.Error(t, svc.MarkRead(2, FeedSourceMessage, unreadNotif.ID))

	// 找未读告警并标记。
	var unreadAlert model.AlertEvent
	require.NoError(t, db.Where("read = ?", false).First(&unreadAlert).Error)
	require.NoError(t, svc.MarkRead(1, FeedSourceAlert, unreadAlert.ID))

	// 非法来源被拒。
	require.Error(t, svc.MarkRead(1, "bogus", 1))

	// 未读数归零。
	n, err := svc.UnreadCount(1)
	require.NoError(t, err)
	require.Zero(t, n)
}

// 全部已读：站内信本人 + 告警全局，未读归零。
func TestFeed_MarkAllRead(t *testing.T) {
	db := newFeedTestDB(t)
	seedFeed(t, db)
	svc := newFeedSvc(db)

	updated, err := svc.MarkAllRead(1)
	require.NoError(t, err)
	require.EqualValues(t, 2, updated) // 1 站内信 + 1 告警未读

	n, err := svc.UnreadCount(1)
	require.NoError(t, err)
	require.Zero(t, n)
}
