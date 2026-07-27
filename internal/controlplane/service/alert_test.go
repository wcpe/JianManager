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
	inApp := model.AlertChannel{Name: "in-app", Type: model.ChannelTypeInApp, Enabled: true}
	webhook := model.AlertChannel{Name: "webhook", Type: model.ChannelTypeWebhook, Enabled: true}
	require.NoError(t, db.Create(&inApp).Error)
	require.NoError(t, db.Create(&webhook).Error)

	// 日志关键字触发规则（FR-085）。
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name:        "log-error",
		TriggerType: model.AlertTriggerLogKeyword,
		Level:       model.AlertLevelCritical,
		TargetType:  "instance",
		Keyword:     "OutOfMemoryError",
		ChannelIDs:  []uint{inApp.ID, webhook.ID},
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

// TestAlertService_CreateRule_PersistsDisabledRecovery 关闭恢复通知必须按 false 落库。
func TestAlertService_CreateRule_PersistsDisabledRecovery(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	disabled := false
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name: "cpu", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90, NotifyRecover: &disabled,
	})
	require.NoError(t, err)

	var stored model.AlertRule
	require.NoError(t, db.First(&stored, rule.ID).Error)
	assert.False(t, stored.NotifyRecover)
}

// TestAlertService_CreateRule_RejectsPlaintextWebhook 禁止把带密钥的 webhook URL 明文写库。
func TestAlertService_CreateRule_RejectsPlaintextWebhook(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	_, err := svc.CreateRule(CreateRuleRequest{
		Name: "cpu", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90,
		NotifyType: model.ChannelTypeWebhook, NotifyTarget: "https://hooks.example.test/?token=secret",
	})
	require.Error(t, err)
}

// TestAlertService_ListRules_RedactsLegacyWebhookTarget 列表响应不得回显 webhook 目标。
func TestAlertService_ListRules_RedactsLegacyWebhookTarget(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name: "cpu", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90,
		NotifyType: model.ChannelTypeWebhook, NotifyTarget: "${JM_ALERT_WEBHOOK}",
	})
	require.NoError(t, err)

	rules, err := svc.ListRules()
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, rule.ID, rules[0].ID)
	assert.Empty(t, rules[0].NotifyTarget)
}

func TestAlertService_CreateRule_Validation(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)

	valid := CreateRuleRequest{Name: "r", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90}
	cases := []struct {
		name string
		mut  func(*CreateRuleRequest)
	}{
		{"非法目标类型", func(r *CreateRuleRequest) { r.TargetType = "cluster" }},
		{"指标规则必须绑定节点目标", func(r *CreateRuleRequest) { r.TargetType = "instance" }},
		{"节点离线规则必须绑定节点目标", func(r *CreateRuleRequest) { r.TriggerType = model.AlertTriggerNodeOffline; r.TargetType = "instance" }},
		{"实例崩溃规则必须绑定实例目标", func(r *CreateRuleRequest) { r.TriggerType = model.AlertTriggerInstanceCrash; r.TargetType = "node" }},
		{"去抖窗口不能为负", func(r *CreateRuleRequest) { r.DedupWindowSec = -1 }},
		{"持续时间不能为负", func(r *CreateRuleRequest) { r.DurationSec = -1 }},
		{"静默开始时间格式非法", func(r *CreateRuleRequest) { r.SilenceStart = "9:00" }},
		{"静默结束时间格式非法", func(r *CreateRuleRequest) { r.SilenceEnd = "24:00" }},
		{"不存在的通道不能引用", func(r *CreateRuleRequest) { r.ChannelIDs = []uint{404} }},
		{"日志关键字不能为空", func(r *CreateRuleRequest) { r.TriggerType = model.AlertTriggerLogKeyword; r.TargetType = "instance" }},
		{"玩家事件匹配类型非法", func(r *CreateRuleRequest) {
			r.TriggerType = model.AlertTriggerPlayerEvent
			r.TargetType = "instance"
			r.EventMatch = "death"
		}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			tt.mut(&req)
			_, err := svc.CreateRule(req)
			require.Error(t, err)
		})
	}

	_, err := svc.CreateRule(CreateRuleRequest{
		Name: "player", TriggerType: model.AlertTriggerPlayerEvent, TargetType: "instance", EventMatch: "chat",
	})
	require.NoError(t, err)
}

func TestAlertService_UpdateRule(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	rule, err := svc.CreateRule(CreateRuleRequest{Name: "r", TargetType: "node", Metric: "cpu", Operator: ">", Threshold: 90})
	require.NoError(t, err)
	channel := model.AlertChannel{Name: "in-app", Type: model.ChannelTypeInApp, Enabled: true}
	require.NoError(t, db.Create(&channel).Error)

	off := false
	newLevel := model.AlertLevelCritical
	chs := []uint{channel.ID}
	silence := "23:00"
	updated, err := svc.UpdateRule(rule.ID, UpdateRuleRequest{
		Enabled: &off, Level: &newLevel, ChannelIDs: &chs, SilenceStart: &silence,
	})
	require.NoError(t, err)
	assert.False(t, updated.Enabled)
	assert.Equal(t, model.AlertLevelCritical, updated.Level)
	assert.Equal(t, "[1]", updated.ChannelIDs)
	assert.Equal(t, "23:00", updated.SilenceStart)

	// 不存在的规则。
	_, err = svc.UpdateRule(99999, UpdateRuleRequest{Enabled: &off})
	require.ErrorIs(t, err, ErrAlertRuleNotFound)
}

func TestAlertService_UpdateRule_Validation(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name: "log", TriggerType: model.AlertTriggerLogKeyword, TargetType: "instance", Keyword: "ERROR",
	})
	require.NoError(t, err)

	badDedup := -1
	_, err = svc.UpdateRule(rule.ID, UpdateRuleRequest{DedupWindowSec: &badDedup})
	require.Error(t, err)

	badSilence := "25:00"
	_, err = svc.UpdateRule(rule.ID, UpdateRuleRequest{SilenceStart: &badSilence})
	require.Error(t, err)

	missingChannel := []uint{404}
	_, err = svc.UpdateRule(rule.ID, UpdateRuleRequest{ChannelIDs: &missingChannel})
	require.Error(t, err)

	emptyKeyword := ""
	_, err = svc.UpdateRule(rule.ID, UpdateRuleRequest{Keyword: &emptyKeyword})
	require.Error(t, err)

	playerRule, err := svc.CreateRule(CreateRuleRequest{
		Name: "player", TriggerType: model.AlertTriggerPlayerEvent, TargetType: "instance", EventMatch: "join",
	})
	require.NoError(t, err)
	badEventMatch := "death"
	_, err = svc.UpdateRule(playerRule.ID, UpdateRuleRequest{EventMatch: &badEventMatch})
	require.Error(t, err)
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
