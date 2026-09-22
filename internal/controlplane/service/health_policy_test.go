package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-459 平台设置面：默认值、可写白名单、写值校验、生效策略组装。

func TestHealthSettings_DefaultsShown(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())
	view, err := svc.Get()
	require.NoError(t, err)

	want := map[string]string{
		SettingKeyHealthScanEnabled:         "true",
		SettingKeyHealthScanInterval:        "30s",
		SettingKeyHealthProbeKind:           "",
		SettingKeyHealthSuspicionThreshold:  "3",
		SettingKeyHealthAction:              "warn",
		SettingKeyHealthCircuitThreshold:    "5",
		SettingKeyHealthCircuitWindow:       "10m",
		SettingKeyHealthStartupWarmup:       "5m",
		SettingKeyHealthSelfHealMaxRestarts: "3",
	}
	for key, val := range want {
		item, ok := findItem(view.Editable, key)
		require.True(t, ok, "健康巡检设置项应在可编辑列表：%s", key)
		assert.True(t, item.Editable)
		assert.Equal(t, val, item.Value, "默认值：%s", key)
	}
}

func TestHealthSettings_WritableAndValidated(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	// 合法写入。
	require.NoError(t, svc.Update(map[string]string{
		SettingKeyHealthAction:             "restart",
		SettingKeyHealthSuspicionThreshold: "4",
		SettingKeyHealthCircuitWindow:      "15m",
		SettingKeyHealthProbeKind:          "tcp",
	}))
	pol := svc.HealthScanPolicy()
	assert.Equal(t, "restart", pol.Action)
	assert.Equal(t, 4, pol.SuspicionThreshold)
	assert.Equal(t, 15*time.Minute, pol.CircuitBreakerWindow)
	assert.Equal(t, "tcp", pol.ProbeKind)
	assert.True(t, pol.Enabled)

	// 非法写入被拒。
	for key, bad := range map[string]string{
		SettingKeyHealthAction:             "boom",
		SettingKeyHealthSuspicionThreshold: "0",
		SettingKeyHealthCircuitThreshold:   "-2",
		SettingKeyHealthProbeKind:          "udp",
		SettingKeyHealthScanEnabled:        "yes",
		SettingKeyHealthScanInterval:       "0s",
		SettingKeyHealthCircuitWindow:      "notaduration",
	} {
		err := svc.Update(map[string]string{key: bad})
		require.Error(t, err, "非法值应被拒：%s=%s", key, bad)
	}
}

// 未落库覆盖时生效策略等于默认。
func TestHealthScanPolicy_Defaults(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())
	pol := svc.HealthScanPolicy()
	assert.True(t, pol.Enabled)
	assert.Equal(t, 30*time.Second, pol.ScanInterval)
	assert.Equal(t, "warn", pol.Action)
	assert.Equal(t, 3, pol.SuspicionThreshold)
	assert.Equal(t, 5, pol.CircuitBreakerThreshold)
	assert.Equal(t, 10*time.Minute, pol.CircuitBreakerWindow)
	assert.Equal(t, 5*time.Minute, pol.StartupWarmup, "启动宽限期默认 5m（FR-459 复审项 2）")
	assert.Equal(t, 3, pol.SelfHealMaxRestarts, "假死自愈重启上限默认 3（复审项 7）")
}

// 新增两键可写、有校验，且生效策略随之变化（FR-459 复审项 2/7 可配）。
func TestHealthSettings_WarmupAndSelfHealCapConfigurable(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())
	require.NoError(t, svc.Update(map[string]string{
		SettingKeyHealthStartupWarmup:       "90s",
		SettingKeyHealthSelfHealMaxRestarts: "2",
	}))
	pol := svc.HealthScanPolicy()
	assert.Equal(t, 90*time.Second, pol.StartupWarmup)
	assert.Equal(t, 2, pol.SelfHealMaxRestarts)

	for _, bad := range []struct{ key, val string }{
		{SettingKeyHealthStartupWarmup, "-1s"},
		{SettingKeyHealthStartupWarmup, "nope"},
		{SettingKeyHealthSelfHealMaxRestarts, "0"},
		{SettingKeyHealthSelfHealMaxRestarts, "x"},
	} {
		require.Error(t, svc.Update(map[string]string{bad.key: bad.val}), "非法值应被拒：%s=%s", bad.key, bad.val)
	}
}

// 熔断告警投递器给启用的平台管理员发站内信（尽力而为）。
func TestHealthAlertNotifier_NotifiesAdmins(t *testing.T) {
	db := newSettingsTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Notification{}, &model.Instance{}, &model.Node{}))
	require.NoError(t, db.Create(&model.User{Username: "admin", Role: model.RolePlatformAdmin, Status: model.UserStatusActive}).Error)
	require.NoError(t, db.Create(&model.User{Username: "member", Role: model.RoleMember, Status: model.UserStatusActive}).Error)
	node := model.Node{Name: "node-01", UUID: "node-uuid"}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{UUID: "inst-uuid", Name: "survival", NodeID: node.ID}
	require.NoError(t, db.Create(&inst).Error)

	notifier := NewHealthAlertNotifier(db, NewNotificationService(db))
	notifier.NotifyInstanceCircuitBroken("node-uuid", "inst-uuid", "窗口内重启超限")

	var notes []model.Notification
	require.NoError(t, db.Find(&notes).Error)
	require.Len(t, notes, 1, "只给平台管理员发信")
	assert.Contains(t, notes[0].Title, "熔断")
	assert.Contains(t, notes[0].Body, "survival")
}
