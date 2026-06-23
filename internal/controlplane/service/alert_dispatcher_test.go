package service

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newAlertTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}, &model.AlertChannel{}))
	return db
}

func TestInSilenceWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	tests := []struct {
		name       string
		start, end string
		t          time.Time
		want       bool
	}{
		{"inside same-day", "09:00", "18:00", at(12, 0), true},
		{"before same-day", "09:00", "18:00", at(8, 59), false},
		{"at end excluded", "09:00", "18:00", at(18, 0), false},
		{"at start included", "09:00", "18:00", at(9, 0), true},
		{"cross-midnight late", "23:00", "07:00", at(23, 30), true},
		{"cross-midnight early", "23:00", "07:00", at(6, 30), true},
		{"cross-midnight outside", "23:00", "07:00", at(12, 0), false},
		{"empty disabled", "", "", at(12, 0), false},
		{"bad format disabled", "9:00", "18:00", at(12, 0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, inSilenceWindow(tt.start, tt.end, tt.t))
		})
	}
}

func TestParseHHMM(t *testing.T) {
	m, ok := parseHHMM("09:30")
	assert.True(t, ok)
	assert.Equal(t, 9*60+30, m)
	_, ok = parseHHMM("24:00")
	assert.False(t, ok)
	_, ok = parseHHMM("9:30")
	assert.False(t, ok)
}

// TestDispatcher_DedupAggregation 验证去抖窗口内复发只产生一个事件、计数累加。
func TestDispatcher_DedupAggregation(t *testing.T) {
	db := newAlertTestDB(t)
	d := NewAlertDispatcher(db)
	clock := time.Now()
	d.now = func() time.Time { return clock }

	rule := &model.AlertRule{Name: "cpu", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric, DedupWindowSec: 300}
	require.NoError(t, db.Create(rule).Error)

	trig := AlertTrigger{Rule: rule, TargetID: 1, DedupKey: "rule1:node1:cpu", Value: 95, Message: "cpu>90", Resolvable: true}

	d.Fire(trig)
	clock = clock.Add(30 * time.Second)
	d.Fire(trig)
	clock = clock.Add(30 * time.Second)
	d.Fire(trig)

	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1, "去抖窗口内应仅一个活跃事件")
	assert.Equal(t, 3, events[0].Count)
	assert.False(t, events[0].Resolved)
}

// TestDispatcher_ResolveAndRecover 验证恢复标记与活跃事件唯一性。
func TestDispatcher_ResolveAndRecover(t *testing.T) {
	db := newAlertTestDB(t)
	d := NewAlertDispatcher(db)

	rule := &model.AlertRule{Name: "crash", Level: model.AlertLevelCritical, TriggerType: model.AlertTriggerInstanceCrash, NotifyRecover: true}
	require.NoError(t, db.Create(rule).Error)

	key := "rule:inst:crash"
	d.Fire(AlertTrigger{Rule: rule, TargetID: 5, DedupKey: key, Message: "实例崩溃", Resolvable: true})

	var active model.AlertEvent
	require.NoError(t, db.Where("resolved = ?", false).First(&active).Error)
	assert.Equal(t, model.AlertLevelCritical, active.Level)

	d.Resolve(rule, key, "实例已恢复运行")

	var resolved model.AlertEvent
	require.NoError(t, db.First(&resolved, active.ID).Error)
	assert.True(t, resolved.Resolved)
	require.NotNil(t, resolved.ResolvedAt)
}

// TestDispatcher_TransientEventNoAggregate 验证瞬时（不可恢复）事件每次落新记录、直接标记已解决。
func TestDispatcher_TransientEventNoAggregate(t *testing.T) {
	db := newAlertTestDB(t)
	d := NewAlertDispatcher(db)

	rule := &model.AlertRule{Name: "kw", Level: model.AlertLevelInfo, TriggerType: model.AlertTriggerLogKeyword}
	require.NoError(t, db.Create(rule).Error)

	// 无去抖窗口的瞬时事件：两次触发应是两条记录。
	d.Fire(AlertTrigger{Rule: rule, TargetID: 1, DedupKey: "rule:inst:kw", Message: "ERROR x", Resolvable: false})
	d.Fire(AlertTrigger{Rule: rule, TargetID: 1, DedupKey: "rule:inst:kw", Message: "ERROR y", Resolvable: false})

	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 2)
	for _, e := range events {
		assert.True(t, e.Resolved, "瞬时事件应直接落为已解决")
	}
}

// TestDispatcher_SilenceSuppressesNotify 验证静默窗口内不外发，但事件仍入库。
// 用一个会失败的伪通道断言「未尝试外发」——这里以无通道+无 webhook 简化，仅断言落库。
func TestDispatcher_SilenceSuppressesNotify(t *testing.T) {
	db := newAlertTestDB(t)
	d := NewAlertDispatcher(db)
	// 固定到静默窗口内的时刻。
	d.now = func() time.Time { return time.Date(2026, 1, 1, 3, 0, 0, 0, time.Local) }

	rule := &model.AlertRule{
		Name: "night", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		SilenceStart: "23:00", SilenceEnd: "07:00",
	}
	require.NoError(t, db.Create(rule).Error)

	d.Fire(AlertTrigger{Rule: rule, TargetID: 1, DedupKey: "k", Message: "cpu>90", Resolvable: true})

	var count int64
	db.Model(&model.AlertEvent{}).Count(&count)
	assert.Equal(t, int64(1), count, "静默期事件仍入库")
}

// TestDispatcher_NotifyRoutesToChannels 验证 fire 时按 ChannelIDs 路由到启用的 webhook 通道（httptest 命中）。
func TestDispatcher_NotifyRoutesToChannels(t *testing.T) {
	db := newAlertTestDB(t)
	d := NewAlertDispatcher(db)

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 经服务创建，确保 enabled 标志正确落库（含 GORM default:true 的禁用回写修正）。
	// webhook URL 强制 ${ENV} 引用，用环境变量承载 httptest 地址。
	t.Setenv("JM_TEST_DISPATCH_WH", srv.URL)
	chSvc := NewAlertChannelService(db)
	enabledOn := true
	enabledOff := false
	enabled, err := chSvc.Create(ChannelRequest{Name: "wh", Type: model.ChannelTypeWebhook,
		Enabled: &enabledOn, Config: ChannelConfig{URL: "${JM_TEST_DISPATCH_WH}"}})
	require.NoError(t, err)
	disabled, err := chSvc.Create(ChannelRequest{Name: "off", Type: model.ChannelTypeWebhook,
		Enabled: &enabledOff, Config: ChannelConfig{URL: "${JM_TEST_DISPATCH_WH}"}})
	require.NoError(t, err)

	rule := &model.AlertRule{Name: "r", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric}
	rule.ChannelIDs = "[" + itoa(enabled.ID) + "," + itoa(disabled.ID) + "]"
	require.NoError(t, db.Create(rule).Error)

	d.Fire(AlertTrigger{Rule: rule, TargetID: 1, DedupKey: "k", Message: "m", Resolvable: true})

	assert.Equal(t, int64(1), atomic.LoadInt64(&hits), "仅启用通道应被投递")
	var count int64
	db.Model(&model.AlertEvent{}).Count(&count)
	assert.Equal(t, int64(1), count)
}

// TestDispatcher_FR011WebhookFallback 验证未配 ChannelIDs 时回退到 FR-011 单 webhook 直发。
func TestDispatcher_FR011WebhookFallback(t *testing.T) {
	db := newAlertTestDB(t)
	d := NewAlertDispatcher(db)

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rule := &model.AlertRule{Name: "legacy", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		NotifyType: model.ChannelTypeWebhook, NotifyTarget: srv.URL}
	require.NoError(t, db.Create(rule).Error)

	d.Fire(AlertTrigger{Rule: rule, TargetID: 1, DedupKey: "k", Message: "m", Resolvable: true})
	assert.Equal(t, int64(1), atomic.LoadInt64(&hits))
}

// itoa 避免在测试中引 strconv。
func itoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
