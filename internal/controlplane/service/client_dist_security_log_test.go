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
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
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
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "ip", TargetValue: "203.0.113.11", Action: "temp_block", Status: "active", CreatedAt: now.Add(-4 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "s1", MachineID: "m1", IP: "203.0.113.11", Kind: "artifact", Status: 200, CreatedAt: now.Add(-3 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientRuntimeState{ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", IP: "203.0.113.11", LastHeartbeatAt: now.Add(-2 * time.Minute)}).Error)
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

func TestClientDistSecurity_SearchLogsHonorsTimeWindow(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	// 窗口内外的同一类记录各一条：只有窗口内的应出现（R31）。
	require.NoError(t, db.Create(&model.ClientSecurityHello{ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", Accepted: true, CreatedAt: now.Add(-time.Hour)}).Error)
	require.NoError(t, db.Create(&model.ClientSecurityHello{ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", Accepted: true, CreatedAt: now.Add(-10 * 24 * time.Hour)}).Error)
	// 运行态源的时间列是 last_heartbeat_at，必须也吃时间窗。
	require.NoError(t, db.Create(&model.ClientRuntimeState{ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", LastHeartbeatAt: now.Add(-2 * time.Hour)}).Error)
	require.NoError(t, db.Create(&model.ClientRuntimeState{ChannelID: "s1", MachineID: "m2", PlayerName: "Alex", LastHeartbeatAt: now.Add(-20 * 24 * time.Hour)}).Error)

	from := now.Add(-24 * time.Hour)
	to := now
	out, err := svc.SearchLogs(ClientDistSecurityLogFilter{ChannelID: "s1", From: &from, To: &to, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 2, out.Total, "近 24h 窗口应只含窗口内的 hello + runtime")
	require.Equal(t, []string{"hello", "runtime"}, logTypes(out.Items))

	// 不带时间窗时保持旧行为（全量），避免破坏已有调用方。
	all, err := svc.SearchLogs(ClientDistSecurityLogFilter{ChannelID: "s1", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 4, all.Total)
}

func TestClientDistSecurity_SearchLogsHonorsErrCode(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	// 两类错误码各一条，外加一条无错误码的成功请求。
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "s1", MachineID: "m1", Kind: "manifest", Status: 401, ErrCode: "INVALID_CLIENT_KEY", CreatedAt: now.Add(-time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "s1", MachineID: "m2", Kind: "artifact", Status: 404, ErrCode: "ARTIFACT_NOT_FOUND", CreatedAt: now.Add(-2 * time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "s1", MachineID: "m3", Kind: "manifest", Status: 200, CreatedAt: now.Add(-3 * time.Minute)}).Error)
	// 风险事件的「错误码」是规则码，也应参与同一筛选。
	require.NoError(t, db.Create(&model.ClientSecurityRiskEvent{ChannelID: "s1", MachineID: "m1", RuleCode: "INVALID_CLIENT_KEY", Severity: "low", CreatedAt: now.Add(-4 * time.Minute)}).Error)

	// 精确筛 INVALID_CLIENT_KEY：只应拿到一条 request + 一条 risk。
	out, err := svc.SearchLogs(ClientDistSecurityLogFilter{ErrCode: "INVALID_CLIENT_KEY", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 2, out.Total, "应只含同错误码的 request 与 risk")
	for _, item := range out.Items {
		require.Equal(t, "INVALID_CLIENT_KEY", item.ErrCode)
	}

	// 不存在的错误码：空集，不能因为筛选被忽略而回退成「全部」。
	none, err := svc.SearchLogs(ClientDistSecurityLogFilter{ErrCode: "NO_SUCH_CODE", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 0, none.Total)
}

func TestClientDistSecurity_SearchLogsDoesNotPanicOnOverflowPage(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name      string
		page      int
		seedHello int
		wantTotal int
	}{
		{name: "max-int", page: maxInt},
		{name: "page-size-product-overflow", page: maxInt/200 + 1},
		{name: "far-beyond-small-total", page: 1000, seedHello: 2, wantTotal: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newSecurityLogDB(t)
			for i := 0; i < tt.seedHello; i++ {
				require.NoError(t, db.Create(&model.ClientSecurityHello{ChannelID: "s1", MachineID: "m1"}).Error)
			}
			svc := NewClientDistSecurityService(db, nil, nil)
			require.NotPanics(t, func() {
				out, err := svc.SearchLogs(ClientDistSecurityLogFilter{Page: tt.page, PageSize: 200})
				require.NoError(t, err)
				require.Empty(t, out.Items)
				require.Equal(t, tt.wantTotal, out.Total)
			})
		})
	}
}

// TestClientDistSecurity_SearchLogsReportsTrueTotal 单源命中超过一页时，total 必须是真实命中数（R38）。
//
// 原状：各源先 LIMIT page*pageSize 再取 len(items) 当 total，150 条命中在首页会报成 100，
// 视图的「已截断」提示永不出现——用户以为看到的就是全部。
func TestClientDistSecurity_SearchLogsReportsTrueTotal(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	// 单源 150 条 hello，首页 pageSize=100。
	for i := 0; i < 150; i++ {
		require.NoError(t, db.Create(&model.ClientSecurityHello{
			ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", Accepted: true,
			CreatedAt: now.Add(-time.Duration(i) * time.Second),
		}).Error)
	}

	out, err := svc.SearchLogs(ClientDistSecurityLogFilter{ChannelID: "s1", Page: 1, PageSize: 100})
	require.NoError(t, err)
	require.Len(t, out.Items, 100, "首页仍只给一页")
	require.Equal(t, 150, out.Total, "total 必须是真实命中数，而不是本页条数")

	// 第二页应拿到剩下的 50 条（此前各源都被裁到同一上限，会出现越翻越少）。
	page2, err := svc.SearchLogs(ClientDistSecurityLogFilter{ChannelID: "s1", Page: 2, PageSize: 100})
	require.NoError(t, err)
	require.Len(t, page2.Items, 50)
	require.Equal(t, 150, page2.Total)
}

// TestClientDistSecurity_AnalysisFillsTrafficFields 剖析表的流量/密钥维度必须是真实读数（R39）。
//
// 原状：IP 剖析的 keyCount、玩家剖析的 downloadBytes/abnormalRequests 从未赋值，
// 序列化出去恒为 0——界面上「0 把密钥 / 0 B 下载 / 0 次异常」和「真的没有」无法区分。

// TestClientDistSecurity_ActionFilterChannelAndIPCombined 处置流水的「频道 + IP」组合筛选
// 必须同时生效（review 发现：`channel_id = ? OR target_value = ?` 与后续 AND 条件拼接时，
// SQL 的 AND 优先级高于 OR，IP 条件会被整体绕过——选中某频道后按 IP 筛，仍返回该频道全部处置）。
func TestClientDistSecurity_ActionFilterChannelAndIPCombined(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	now := time.Now()

	// 同频道内：一条是目标 IP 的封禁，一条是别的 IP 的封禁。
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "ip", TargetValue: "192.0.2.10", ChannelID: "s1", Action: "temp_block", Status: "active", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "ip", TargetValue: "198.51.100.7", ChannelID: "s1", Action: "temp_block", Status: "active", CreatedAt: now.Add(-time.Minute)}).Error)
	// 另一频道里也有同一 IP 的封禁：跨频道不得串味。
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "ip", TargetValue: "192.0.2.10", ChannelID: "s2", Action: "temp_block", Status: "active", CreatedAt: now.Add(-2 * time.Minute)}).Error)

	out, err := svc.SearchLogs(ClientDistSecurityLogFilter{Type: "action", ChannelID: "s1", IP: "192.0.2.10", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 1, out.Total, "频道 + IP 必须同时约束；缺括号会返回 s1 的全部处置")
	require.Len(t, out.Items, 1)
	require.Equal(t, "192.0.2.10", out.Items[0].Detail["targetValue"])
}

// TestClientDistSecurity_SourcesWithoutErrCodeAreExcluded 带 errCode 筛选时，
// 没有错误码维度的源（处置流水 / 运行态快照）必须整源排除，而不是静默返回无关记录。
func TestClientDistSecurity_SourcesWithoutErrCodeAreExcluded(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	now := time.Now()

	// 一条有错误码的请求明细，外加一条处置流水与一条运行态快照（两者都没有 error code）。
	require.NoError(t, db.Create(&model.ClientDistEvent{ChannelID: "s1", MachineID: "m1", Kind: "manifest", Status: 401, ErrCode: "INVALID_CLIENT_KEY", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "ip", TargetValue: "192.0.2.10", ChannelID: "s1", Action: "temp_block", Status: "active", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.ClientRuntimeState{ChannelID: "s1", MachineID: "m1", PlayerName: "Alex", LastHeartbeatAt: now}).Error)

	out, err := svc.SearchLogs(ClientDistSecurityLogFilter{ErrCode: "INVALID_CLIENT_KEY", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 1, out.Total, "只有 request 有该错误码维度；action/runtime 必须整源排除")
	require.Len(t, out.Items, 1)
	require.Equal(t, "request", out.Items[0].Type)

	// 不带 errCode 时三个源都应出现（确认排除逻辑只在筛选时生效，没有误伤常态）。
	all, err := svc.SearchLogs(ClientDistSecurityLogFilter{ChannelID: "s1", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 3, all.Total)
}

// TestClientDistSecurity_ActionFilterSQLKeepsConditionsGrouped 钉住处置流水查询的 SQL 形态：
// 「频道 OR 目标值」与「IP 目标」必须各自成组。
//
// 【定性说明】这不是在修一个正在发生的 bug。复审已实测：GORM 会给每个 Where 子句各自加括号，
// 所以即便源码里不写括号，AND/OR 优先级也不会被破坏——此前「IP 条件被整体绕过」的判断是误报。
// 这条断言的价值是把「条件分组」固定成不依赖 ORM 实现细节的契约：升级 GORM、或有人把多个条件
// 合并进同一个 Where 字符串时，行为测试可能仍然绿，而这里会先红。
func TestClientDistSecurity_ActionFilterSQLKeepsConditionsGrouped(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)

	stmt := svc.securityActionQuery(ClientDistSecurityLogFilter{
		ChannelID: "s1",
		IP:        "192.0.2.10",
	}).Session(&gorm.Session{DryRun: true}).Find(&[]model.ClientProtectionAction{}).Statement

	sql := stmt.SQL.String()
	require.Contains(t, sql, "(channel_id = ? OR target_value = ?)", "频道维度必须整体成组，SQL: %s", sql)
	require.Contains(t, sql, "(target_type = ? AND target_value = ?)", "IP 维度必须整体成组，SQL: %s", sql)
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

// TestSetKeyStateWritesChannelID 改密钥态产生的处置记录必须带频道（R37）。
//
// 原状：记录里 ChannelID 为空，而流水按频道筛的条件是 `channel_id = ? OR target_value = ?`
// （key 记录的 target_value 是密钥 ID，永远不等于频道 slug），于是「选 A 频道看动作流水」
// 看不到刚改的 A 频道 key，切「全部」才出现。
func TestSetKeyStateWritesChannelID(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	require.NoError(t, db.Create(&model.ClientChannel{ChannelID: "s1", Name: "一区"}).Error)
	key := model.ClientPullKey{ChannelID: "s1", Name: "正式包", KeyHash: "hash-r37", KeyPrefix: "jmck_r37"}
	require.NoError(t, db.Create(&key).Error)

	require.NoError(t, svc.SetKeyState(key.ID, ClientKeyStateThrottled, "排查限速"))

	// 按频道筛动作流水：必须能看到刚产生的这一条。
	rows, err := svc.ListActionsFiltered(ClientActionFilter{ChannelID: "s1"})
	require.NoError(t, err)
	require.Len(t, rows, 1, "按所属频道应能看到 key_state 处置")
	require.Equal(t, "s1", rows[0].ChannelID)
	require.Equal(t, "key_state", rows[0].Action)

	// 另一个频道下不应出现。
	other, err := svc.ListActionsFiltered(ClientActionFilter{ChannelID: "s2"})
	require.NoError(t, err)
	require.Empty(t, other)
}

func TestListActionsFiltered(t *testing.T) {
	db := newSecurityLogDB(t)
	svc := NewClientDistSecurityService(db, nil, nil)
	now := time.Now()
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "ip", TargetValue: "192.0.2.9", ChannelID: "s1", Action: "temp_block", Status: "active", Reason: "spike", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "key", TargetValue: "7", ChannelID: "s1", Action: "key_state", Status: "active", Reason: "throttle", CreatedAt: now.Add(-time.Minute)}).Error)
	require.NoError(t, db.Create(&model.ClientProtectionAction{TargetType: "ip", TargetValue: "198.51.100.1", ChannelID: "s2", Action: "temp_block", Status: "expired", Reason: "old", CreatedAt: now.Add(-2 * time.Minute)}).Error)

	all, err := svc.ListActionsFiltered(ClientActionFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3)

	ips, err := svc.ListActionsFiltered(ClientActionFilter{TargetType: "ip"})
	require.NoError(t, err)
	require.Len(t, ips, 2)

	active, err := svc.ListActionsFiltered(ClientActionFilter{Status: "active"})
	require.NoError(t, err)
	require.Len(t, active, 2)

	byQ, err := svc.ListActionsFiltered(ClientActionFilter{Q: "spike"})
	require.NoError(t, err)
	require.Len(t, byQ, 1)
	require.Equal(t, "192.0.2.9", byQ[0].TargetValue)

	byChannel, err := svc.ListActionsFiltered(ClientActionFilter{ChannelID: "s2"})
	require.NoError(t, err)
	require.Len(t, byChannel, 1)
	require.Equal(t, "expired", byChannel[0].Status)

	// 超上限钳到 500，缺省/非法回落 200。
	over, err := svc.ListActionsFiltered(ClientActionFilter{Limit: 600})
	require.NoError(t, err)
	require.Len(t, over, 3)
	neg, err := svc.ListActionsFiltered(ClientActionFilter{Limit: -1})
	require.NoError(t, err)
	require.Len(t, neg, 3)
}
