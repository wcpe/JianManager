package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newTriggersTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}, &model.AlertChannel{}, &model.Instance{}))
	return db
}

func TestTriggers_MatchRules_TargetFiltering(t *testing.T) {
	db := newTriggersTestDB(t)
	tr := NewAlertEventTriggers(db, NewAlertDispatcher(db), nil, nil)

	global := uint(0)
	specific := uint(42)
	// 全局崩溃规则。
	require.NoError(t, db.Create(&model.AlertRule{Name: "g", Enabled: true, TriggerType: model.AlertTriggerInstanceCrash}).Error)
	// 仅实例 42 的崩溃规则。
	require.NoError(t, db.Create(&model.AlertRule{Name: "s", Enabled: true, TriggerType: model.AlertTriggerInstanceCrash, TargetID: &specific}).Error)
	// 禁用的崩溃规则（经 map 更新落库 false，规避 GORM default:true 对结构体零值的回填）。
	disabled := &model.AlertRule{Name: "d", Enabled: true, TriggerType: model.AlertTriggerInstanceCrash}
	require.NoError(t, db.Create(disabled).Error)
	require.NoError(t, db.Model(disabled).Update("enabled", false).Error)
	// 不同触发类型。
	require.NoError(t, db.Create(&model.AlertRule{Name: "o", Enabled: true, TriggerType: model.AlertTriggerLogKeyword}).Error)
	_ = global

	// 实例 42：命中全局 + 专属 = 2 条。
	matched := tr.matchRules(model.AlertTriggerInstanceCrash, 42)
	assert.Len(t, matched, 2)

	// 实例 7：仅命中全局 = 1 条。
	matched = tr.matchRules(model.AlertTriggerInstanceCrash, 7)
	assert.Len(t, matched, 1)
	assert.Equal(t, "g", matched[0].Name)
}

func TestTriggers_HandleStateChange_CrashFiresAndResolves(t *testing.T) {
	db := newTriggersTestDB(t)
	tr := NewAlertEventTriggers(db, NewAlertDispatcher(db), nil, nil)

	inst := &model.Instance{Name: "smp", UUID: "uuid-1"}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.AlertRule{Name: "crash", Enabled: true, Level: model.AlertLevelCritical, TriggerType: model.AlertTriggerInstanceCrash}).Error)

	// 崩溃 → 触发活跃事件。
	tr.handleStateChange(InstanceEvent{InstanceUUID: "uuid-1", Type: "state_change", Data: "RUNNING→CRASHED"})
	var active int64
	db.Model(&model.AlertEvent{}).Where("resolved = ?", false).Count(&active)
	require.Equal(t, int64(1), active)

	// 恢复运行 → 标记已解决。
	tr.handleStateChange(InstanceEvent{InstanceUUID: "uuid-1", Type: "state_change", Data: "STARTING→RUNNING"})
	db.Model(&model.AlertEvent{}).Where("resolved = ?", false).Count(&active)
	assert.Equal(t, int64(0), active)
}

func TestTriggers_HandleLogLine_KeywordMatch(t *testing.T) {
	db := newTriggersTestDB(t)
	tr := NewAlertEventTriggers(db, NewAlertDispatcher(db), nil, nil)

	inst := &model.Instance{Name: "smp", UUID: "uuid-1"}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.AlertRule{Name: "oom", Enabled: true, TriggerType: model.AlertTriggerLogKeyword, Keyword: "OutOfMemoryError"}).Error)

	// 不命中。
	tr.handleLogLine(InstanceEvent{InstanceUUID: "uuid-1", Type: "stderr", Data: "normal log line"})
	var count int64
	db.Model(&model.AlertEvent{}).Count(&count)
	require.Equal(t, int64(0), count)

	// 命中关键字。
	tr.handleLogLine(InstanceEvent{InstanceUUID: "uuid-1", Type: "stderr", Data: "java.lang.OutOfMemoryError: Java heap space"})
	db.Model(&model.AlertEvent{}).Count(&count)
	require.Equal(t, int64(1), count)

	var e model.AlertEvent
	db.First(&e)
	assert.True(t, e.Resolved, "日志关键字为瞬时事件，直接已解决")
	assert.Contains(t, e.Message, "OutOfMemoryError")
}

func TestTriggers_OnBackupFailed(t *testing.T) {
	db := newTriggersTestDB(t)
	tr := NewAlertEventTriggers(db, NewAlertDispatcher(db), nil, nil)
	require.NoError(t, db.Create(&model.AlertRule{Name: "bk", Enabled: true, TriggerType: model.AlertTriggerBackupFailed}).Error)

	tr.OnBackupFailed(&model.Backup{InstanceID: 3}, "磁盘空间不足")
	var count int64
	db.Model(&model.AlertEvent{}).Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestPlayerEventSubtype(t *testing.T) {
	assert.Equal(t, "join", playerEventSubtype("player_join"))
	assert.Equal(t, "quit", playerEventSubtype("player_quit"))
	assert.Equal(t, "chat", playerEventSubtype("chat"))
	assert.Equal(t, "cross_server", playerEventSubtype("cross_server"))
	assert.Equal(t, "", playerEventSubtype("heartbeat"))
	assert.Equal(t, "", playerEventSubtype("connected"))
}
