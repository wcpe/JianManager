package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newSecurityLogDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.ClientChannel{},
		&model.ClientPullKey{},
		&model.ClientSecurityProfile{},
		&model.ClientSecurityHello{},
		&model.ClientSecurityRiskEvent{},
		&model.ClientProtectionAction{},
		&model.ClientDistEvent{},
		&model.ClientRuntimeState{},
		&model.ClientTelemetry{},
	))
	return db
}

func TestClientDistSecurity_SearchLogsMergesAllTypes(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	require.NoError(t, db.Create(&model.ClientSecurityHello{ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", Accepted: true, CreatedAt: now.Add(-6 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientSecurityRiskEvent{ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", RuleCode: "INVALID_PLAYER_NAME", CreatedAt: now.Add(-5 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "ip", TargetValue: "1.1.1.1", Action: "temp_block", Status: "active", CreatedAt: now.Add(-4 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "s1", MachineID: "m1", IP: "1.1.1.1", Kind: "artifact", Status: 200, CreatedAt: now.Add(-3 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientRuntimeState{ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", IP: "1.1.1.1", LastHeartbeatAt: now.Add(-2 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientTelemetry{ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", Result: "success", CreatedAt: now.Add(-time.Minute)}).Error)

	out, err := svc.SearchLogs(ClientDistSecurityLogFilter{ChannelID: "s1", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 5, out.Total)
	require.Equal(t, []string{"telemetry", "runtime", "request", "risk", "hello"}, logTypes(out.Items))

	byPlayer, err := svc.SearchLogs(ClientDistSecurityLogFilter{PlayerName: "Alex", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, []string{"telemetry", "runtime", "risk", "hello"}, logTypes(byPlayer.Items), "按玩家名筛选时不应混入无玩家字段的请求/动作日志")

	filtered, err := svc.SearchLogs(ClientDistSecurityLogFilter{Type: "telemetry", PlayerName: "Alex", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 1, filtered.Total)
	require.Equal(t, "Alex", filtered.Items[0].PlayerName)
}

func TestClientDistSecurity_ChannelSummaryUsesWindowAndChannel(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	now := time.Now()
	require.NoError(t, db.Create(&model.ClientChannel{ChannelID: "s1", Name: "一区", ProtectionMode: "queue"}).Error)
	require.NoError(t, db.Create(&model.ClientPullKey{ChannelID: "s1", Name: "受限", KeyHash: "hash-1", KeyPrefix: "key-1", SecurityState: ClientKeyStateThrottled}).Error)
	expiresAt := now.Add(time.Hour)
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "ip", TargetValue: "192.0.2.1", ChannelID: "s1", Action: "temp_block", Status: "active", ExpiresAt: &expiresAt}).Error)
	require.NoError(t, db.Create(&model.ClientSecurityRiskEvent{ChannelID: "s1", Severity: "high", CreatedAt: now.Add(-10 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientSecurityRiskEvent{ChannelID: "s1", Severity: "critical", CreatedAt: now.Add(-2 * time.Hour)}).Error)
	require.NoError(t, db.Create(&model.ClientSecurityRiskEvent{ChannelID: "s2", Severity: "critical", CreatedAt: now}).Error)

	out, err := svc.ChannelSummary("s1", time.Hour)
	require.NoError(t, err)
	require.Equal(t, "high", out.RiskLevel)
	require.EqualValues(t, 1, out.AbnormalRequests)
	require.EqualValues(t, 1, out.BlockedIPCount)
	require.EqualValues(t, 1, out.RestrictedKeyCount)
	require.Equal(t, "queue", out.ProtectionMode)
}

func TestClientDistSecurity_ProfileDetailIncludesTimeline(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	now := time.Now()
	profile := model.ClientSecurityProfile{ChannelID: "s1", MachineID: "machine-abcdef", InstallID: "install-abcdef", PlayerName: "Alex", KeyID: 7, OS: "Windows", Arch: "amd64", JavaVendor: "Temurin", Locale: "zh-CN", Timezone: "Asia/Shanghai", MemoryTier: "8-16g", CoreVersion: "2.1.0", FirstSeen: now.Add(-time.Hour), LastSeen: now}
	require.NoError(t, db.Create(&profile).Error)
	require.NoError(t, db.Create(&model.ClientSecurityRiskEvent{ChannelID: "s1", MachineID: profile.MachineID, InstallID: profile.InstallID, RuleCode: "RISK_A", CreatedAt: now.Add(-time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "key", TargetValue: "7", ChannelID: "s1", Action: "key_state", Status: "active", CreatedAt: now.Add(-2 * time.Minute)}).Error)

	out, err := svc.ProfileDetail(profile.ID)
	require.NoError(t, err)
	require.Equal(t, "Temurin", out.JavaVendor)
	require.Len(t, out.RecentEvents, 1)
	require.Len(t, out.ProtectionActions, 1)
	require.Equal(t, "RISK_A", out.RecentEvents[0].RuleCode)
}

func logTypes(items []ClientDistSecurityLogItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Type)
	}
	return out
}
