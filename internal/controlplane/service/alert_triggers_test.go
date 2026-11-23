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

func newTriggersTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
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

func TestTriggers_PlayerEventMatchFiltering(t *testing.T) {
	db := newTriggersTestDB(t)
	tr := NewAlertEventTriggers(db, NewAlertDispatcher(db), nil, nil)
	require.NoError(t, db.Create(&model.AlertRule{
		Name: "chat", Enabled: true, TriggerType: model.AlertTriggerPlayerEvent, EventMatch: "chat",
	}).Error)

	ch := make(chan PlayerEvent, 2)
	go tr.consumePlayerEvents(ch)
	ch <- PlayerEvent{InstanceID: 9, InstanceName: "smp", Type: "player_join", PlayerName: "Steve"}
	ch <- PlayerEvent{InstanceID: 9, InstanceName: "smp", Type: "chat", PlayerName: "Steve", Message: "hello"}
	close(ch)

	require.Eventually(t, func() bool {
		var count int64
		db.Model(&model.AlertEvent{}).Count(&count)
		return count == 1
	}, time.Second, 10*time.Millisecond)

	var event model.AlertEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, model.AlertTriggerPlayerEvent, event.TriggerType)
	assert.Contains(t, event.Message, "hello")
}

func TestPlayerEventSubtype(t *testing.T) {
	assert.Equal(t, "join", playerEventSubtype("player_join"))
	assert.Equal(t, "quit", playerEventSubtype("player_quit"))
	assert.Equal(t, "chat", playerEventSubtype("chat"))
	assert.Equal(t, "cross_server", playerEventSubtype("cross_server"))
	assert.Equal(t, "", playerEventSubtype("heartbeat"))
	assert.Equal(t, "", playerEventSubtype("connected"))
}

// TestTriggers_LogKeywordIndependentOfStorage 锁定一个**仍然成立且更重要**的不变量：
//
// 关键字告警消费的是实例事件流（`InstanceEvent` stdout/stderr，见 consumeInstanceEvents），
// 与“日志存在哪”完全无关。
//
// 历史（已失效的前提）：本测试原名 `...UnaffectedByCutover`，参数化验证「cutover 开关
// 不影响告警」。2026-10-29 删除 CP `logs` 表与 LogCutover 后，那个变量已不存在；
// 但**该不变量本身反而更关键**了——因为日志存储路径整个换掉了（CP 表 → VL 联邦），
// 一旦告警意外取决于存储，这次改造就会静默停掉告警。故保留并改写为直接验证。
func TestTriggers_LogKeywordIndependentOfStorage(t *testing.T) {
	db := newTriggersTestDB(t)
	tr := NewAlertEventTriggers(db, NewAlertDispatcher(db), nil, nil)
	require.NoError(t, db.Create(&model.Instance{Name: "smp", UUID: "uuid-1"}).Error)
	require.NoError(t, db.Create(&model.AlertRule{
		Name: "oom", Enabled: true, TriggerType: model.AlertTriggerLogKeyword, Keyword: "OutOfMemoryError",
	}).Error)

	tr.handleLogLine(InstanceEvent{InstanceUUID: "uuid-1", Type: "stderr", Data: "java.lang.OutOfMemoryError: Java heap space"})

	var count int64
	db.Model(&model.AlertEvent{}).Count(&count)
	require.EqualValues(t, 1, count, "关键字告警必须照常触发（它消费事件流，与日志存储路径无关）")
}
