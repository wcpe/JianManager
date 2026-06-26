package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestAlertService_CreateRule_MultiType(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)

	// 日志关键字触发规则（FR-085）。
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name:        "log-error",
		TriggerType: model.AlertTriggerLogKeyword,
		Level:       model.AlertLevelCritical,
		TargetType:  "instance",
		Keyword:     "OutOfMemoryError",
		ChannelIDs:  []uint{1, 2},
	})
	require.NoError(t, err)
	assert.Equal(t, model.AlertTriggerLogKeyword, rule.TriggerType)
	assert.Equal(t, model.AlertLevelCritical, rule.Level)
	assert.Equal(t, "[1,2]", rule.ChannelIDs)

	// 非法触发类型被拒。
	_, err = svc.CreateRule(CreateRuleRequest{Name: "x", TriggerType: "telepathy", TargetType: "node"})
	require.Error(t, err)

	// 非法级别被拒。
	_, err = svc.CreateRule(CreateRuleRequest{Name: "x", Level: "apocalyptic", TargetType: "node"})
	require.Error(t, err)
}

func TestAlertService_CreateRule_DefaultsMetric(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	// 不传 triggerType/level → 默认 metric/warn（FR-011 兼容）。
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name: "cpu", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90,
	})
	require.NoError(t, err)
	assert.Equal(t, model.AlertTriggerMetric, rule.TriggerType)
	assert.Equal(t, model.AlertLevelWarn, rule.Level)
	assert.True(t, rule.NotifyRecover)
}

func TestAlertService_UpdateRule(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	rule, err := svc.CreateRule(CreateRuleRequest{Name: "r", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90})
	require.NoError(t, err)

	off := false
	newLevel := model.AlertLevelCritical
	chs := []uint{3}
	silence := "23:00"
	updated, err := svc.UpdateRule(rule.ID, UpdateRuleRequest{
		Enabled: &off, Level: &newLevel, ChannelIDs: &chs, SilenceStart: &silence,
	})
	require.NoError(t, err)
	assert.False(t, updated.Enabled)
	assert.Equal(t, model.AlertLevelCritical, updated.Level)
	assert.Equal(t, "[3]", updated.ChannelIDs)
	assert.Equal(t, "23:00", updated.SilenceStart)

	// 不存在的规则。
	_, err = svc.UpdateRule(99999, UpdateRuleRequest{Enabled: &off})
	require.ErrorIs(t, err, ErrAlertRuleNotFound)
}

func TestAlertService_AcknowledgeAndRead(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	rule, err := svc.CreateRule(CreateRuleRequest{Name: "r", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90})
	require.NoError(t, err)

	now := time.Now()
	event := &model.AlertEvent{RuleID: rule.ID, Level: model.AlertLevelWarn, Message: "m", FiredAt: now, LastFiredAt: &now}
	require.NoError(t, db.Create(event).Error)

	// 初始未读。
	unread, err := svc.UnreadCount()
	require.NoError(t, err)
	assert.Equal(t, int64(1), unread)

	// 确认 → acknowledged + read。
	acked, err := svc.Acknowledge(event.ID, 7)
	require.NoError(t, err)
	assert.True(t, acked.Acknowledged)
	require.NotNil(t, acked.AcknowledgedBy)
	assert.Equal(t, uint(7), *acked.AcknowledgedBy)
	assert.True(t, acked.Read)

	unread, err = svc.UnreadCount()
	require.NoError(t, err)
	assert.Equal(t, int64(0), unread)

	// 确认不存在的事件。
	_, err = svc.Acknowledge(99999, 7)
	require.ErrorIs(t, err, ErrAlertEventNotFound)
}

func TestAlertService_ListEvents_Filters(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	rule, err := svc.CreateRule(CreateRuleRequest{Name: "r", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90})
	require.NoError(t, err)

	now := time.Now()
	mk := func(level, trig string, resolved, ack bool) {
		require.NoError(t, db.Create(&model.AlertEvent{
			RuleID: rule.ID, Level: level, TriggerType: trig, Resolved: resolved, Acknowledged: ack,
			FiredAt: now, LastFiredAt: &now,
		}).Error)
	}
	mk(model.AlertLevelCritical, model.AlertTriggerMetric, false, false)
	mk(model.AlertLevelWarn, model.AlertTriggerLogKeyword, true, false)
	mk(model.AlertLevelInfo, model.AlertTriggerPlayerEvent, false, true)

	// 按级别筛。
	crit, critTotal, err := svc.ListEvents(EventFilter{Level: model.AlertLevelCritical})
	require.NoError(t, err)
	require.Len(t, crit, 1)
	assert.Equal(t, int64(1), critTotal)

	// 按已确认筛。
	notAck := false
	un, _, err := svc.ListEvents(EventFilter{Acknowledged: &notAck})
	require.NoError(t, err)
	require.Len(t, un, 2)

	// 按触发类型筛。
	kw, _, err := svc.ListEvents(EventFilter{TriggerType: model.AlertTriggerLogKeyword})
	require.NoError(t, err)
	require.Len(t, kw, 1)

	// Rule 预加载 + 无筛选时总数为全量。
	all, allTotal, err := svc.ListEvents(EventFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, int64(3), allTotal)
	assert.Equal(t, "r", all[0].Rule.Name)
}

// TestAlertService_ListEvents_KeywordTimePaging 覆盖 FR-149 新增的关键字 / 时间范围 / 分页筛选。
func TestAlertService_ListEvents_KeywordTimePaging(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	rule, err := svc.CreateRule(CreateRuleRequest{Name: "r", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90})
	require.NoError(t, err)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msgs := []string{"cpu high one", "cpu high two", "cpu high three", "cpu high four", "cpu high five"}
	for i, m := range msgs {
		ts := base.Add(time.Duration(i) * time.Hour)
		require.NoError(t, db.Create(&model.AlertEvent{
			RuleID: rule.ID, Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
			Message: m, FiredAt: ts, LastFiredAt: &ts,
		}).Error)
	}

	// 关键字模糊匹配 message。
	hits, total, err := svc.ListEvents(EventFilter{Keyword: "high"})
	require.NoError(t, err)
	assert.Equal(t, int64(5), total)
	require.Len(t, hits, 5)

	one, oneTotal, err := svc.ListEvents(EventFilter{Keyword: "three"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), oneTotal)
	require.Len(t, one, 1)

	// 时间范围（含边界）：base+1h..base+3h → 第 2~4 条共 3。
	from := base.Add(time.Hour)
	to := base.Add(3 * time.Hour)
	ranged, rangedTotal, err := svc.ListEvents(EventFilter{From: &from, To: &to})
	require.NoError(t, err)
	assert.Equal(t, int64(3), rangedTotal)
	require.Len(t, ranged, 3)

	// 分页：每页 2，共 5 → 第 1 页 2 条、总数 5；第 3 页 1 条。
	page1, pTotal, err := svc.ListEvents(EventFilter{PageSize: 2, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(5), pTotal)
	require.Len(t, page1, 2)
	page3, _, err := svc.ListEvents(EventFilter{PageSize: 2, Page: 3})
	require.NoError(t, err)
	require.Len(t, page3, 1)
}
