package service

import (
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func newBusinessEventTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.BusinessEvent{}, &model.EconomyBalanceMirror{}, &model.EconomyLedgerEntry{},
	))
	return db
}

// economyFrame 构造一条经济变更 event 帧原文（模拟探针 BridgeClient.businessEventJson 的产物）。
func economyFrame(t *testing.T, data map[string]string) string {
	t.Helper()
	frame := map[string]any{
		"type":     "event",
		"event":    "economy_change",
		"domain":   economyDomain,
		"dedupKey": data["ledgerId"],
		"data":     data,
	}
	b, err := json.Marshal(frame)
	require.NoError(t, err)
	return string(b)
}

// economyEvt 构造一条携经济信封的 workerpb.PluginEvent（domain/dedupKey/rawJson 由 Worker 透传）。
func economyEvt(t *testing.T, instanceUUID string, data map[string]string) *workerpb.PluginEvent {
	t.Helper()
	return &workerpb.PluginEvent{
		InstanceUuid: instanceUUID,
		Type:         "economy_change",
		Domain:       economyDomain,
		DedupKey:     data["ledgerId"],
		RawJson:      economyFrame(t, data),
		Timestamp:    1000,
	}
}

func economyData(player, currency, zone, entry, signed, after, ledgerID, seq string) map[string]string {
	return map[string]string{
		"playerName":   player,
		"currencyId":   "1",
		"currency":     currency,
		"zoneId":       zone,
		"entryType":    entry,
		"signedAmount": signed,
		"balanceAfter": after,
		"ledgerId":     ledgerID,
		"seq":          seq,
		"occurredAt":   "1700000000000",
	}
}

// TestIngest_DedupByLedgerId 同 (domain,dedupKey) 重复投递只落一条 envelope + 一条审计 + 一行镜像。
func TestIngest_DedupByLedgerId(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	evt := economyEvt(t, "inst-1", economyData("Steve", "coin", "zone-a", "DEPOSIT", "100.00", "100.00", "42", "1"))

	svc.Ingest("node-1", evt)
	svc.Ingest("node-1", evt) // 重发（至少一次投递）
	svc.Ingest("node-1", evt)

	var envCount, ledgerCount, mirrorCount int64
	db.Model(&model.BusinessEvent{}).Count(&envCount)
	db.Model(&model.EconomyLedgerEntry{}).Count(&ledgerCount)
	db.Model(&model.EconomyBalanceMirror{}).Count(&mirrorCount)
	assert.Equal(t, int64(1), envCount, "envelope 应按 (domain,dedupKey) 去重")
	assert.Equal(t, int64(1), ledgerCount, "审计应按 ledgerId 去重")
	assert.Equal(t, int64(1), mirrorCount, "镜像应为单行")

	var mirror model.EconomyBalanceMirror
	require.NoError(t, db.First(&mirror).Error)
	assert.Equal(t, "100.00", mirror.Balance)
	assert.Equal(t, int64(1), mirror.LastSeq)
	assert.Equal(t, int64(42), mirror.LastLedgerID)
}

// TestIngest_MirrorAdvancesBySeq 后续 seq 更大的变更推进镜像余额。
func TestIngest_MirrorAdvancesBySeq(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	svc.Ingest("node-1", economyEvt(t, "inst-1", economyData("Steve", "coin", "zone-a", "DEPOSIT", "100", "100", "1", "1")))
	svc.Ingest("node-1", economyEvt(t, "inst-1", economyData("Steve", "coin", "zone-a", "DEPOSIT", "50", "150", "2", "2")))

	var mirror model.EconomyBalanceMirror
	require.NoError(t, db.Where("player_name = ? AND currency = ?", "Steve", "coin").First(&mirror).Error)
	assert.Equal(t, "150", mirror.Balance, "应推进到最新 seq 的余额")
	assert.Equal(t, int64(2), mirror.LastSeq)

	var ledgerCount int64
	db.Model(&model.EconomyLedgerEntry{}).Count(&ledgerCount)
	assert.Equal(t, int64(2), ledgerCount, "两条不同 ledgerId 审计都应留痕")
}

// TestIngest_OutOfOrderDoesNotRegress 乱序到达的旧 seq 事件不得回退镜像余额（但审计仍 append）。
func TestIngest_OutOfOrderDoesNotRegress(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	// 先到 seq=2（余额 150），后到乱序的 seq=1（余额 100，旧事件）。
	svc.Ingest("node-1", economyEvt(t, "inst-1", economyData("Steve", "coin", "zone-a", "DEPOSIT", "50", "150", "2", "2")))
	svc.Ingest("node-1", economyEvt(t, "inst-1", economyData("Steve", "coin", "zone-a", "DEPOSIT", "100", "100", "1", "1")))

	var mirror model.EconomyBalanceMirror
	require.NoError(t, db.Where("player_name = ?", "Steve").First(&mirror).Error)
	assert.Equal(t, "150", mirror.Balance, "乱序旧事件不得回退余额")
	assert.Equal(t, int64(2), mirror.LastSeq, "seq 游标不得回退")

	var ledgerCount int64
	db.Model(&model.EconomyLedgerEntry{}).Count(&ledgerCount)
	assert.Equal(t, int64(2), ledgerCount, "乱序旧事件审计仍应留痕（不丢账）")
}

// TestIngest_CrossZoneNoCollision 跨区同名玩家独立镜像，不串味/不重复计数。
func TestIngest_CrossZoneNoCollision(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	svc.Ingest("node-1", economyEvt(t, "inst-a", economyData("Steve", "coin", "zone-a", "DEPOSIT", "100", "100", "1", "1")))
	svc.Ingest("node-1", economyEvt(t, "inst-b", economyData("Steve", "coin", "zone-b", "DEPOSIT", "999", "999", "2", "1")))

	rows, err := svc.ListEconomyMirror(EconomyMirrorQuery{PlayerName: "Steve", Currency: "coin"})
	require.NoError(t, err)
	require.Len(t, rows, 2, "同名玩家跨区应为两行独立镜像")
	byZone := map[string]string{}
	for _, r := range rows {
		byZone[r.ZoneID] = r.Balance
	}
	assert.Equal(t, "100", byZone["zone-a"])
	assert.Equal(t, "999", byZone["zone-b"], "另一区余额不得串味")
}

// TestIngest_CrossNodeSameZoneNoCollision 不同节点即便 zoneId 相同也独立镜像（node→zone 维度）。
func TestIngest_CrossNodeSameZoneNoCollision(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	svc.Ingest("node-1", economyEvt(t, "inst-1", economyData("Steve", "coin", "zone-x", "DEPOSIT", "10", "10", "1", "1")))
	svc.Ingest("node-2", economyEvt(t, "inst-2", economyData("Steve", "coin", "zone-x", "DEPOSIT", "20", "20", "2", "1")))

	rows, err := svc.ListEconomyMirror(EconomyMirrorQuery{PlayerName: "Steve"})
	require.NoError(t, err)
	assert.Len(t, rows, 2, "跨节点同区应为两行（node→zone 维度）")
}

// TestAggregateEconomyByZone 跨区聚合返回逐 (node,zone) 行，不盲目求和。
func TestAggregateEconomyByZone(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	svc.Ingest("node-1", economyEvt(t, "inst-a", economyData("Steve", "coin", "zone-a", "DEPOSIT", "100", "100", "1", "1")))
	svc.Ingest("node-1", economyEvt(t, "inst-b", economyData("Steve", "coin", "zone-b", "DEPOSIT", "200", "200", "2", "1")))
	svc.Ingest("node-1", economyEvt(t, "inst-c", economyData("Alex", "coin", "zone-a", "DEPOSIT", "5", "5", "3", "1")))

	rows, err := svc.AggregateEconomyByZone("Steve", "coin")
	require.NoError(t, err)
	require.Len(t, rows, 2, "Steve 在两区应返回两行")
	for _, r := range rows {
		assert.Equal(t, "Steve", r.PlayerName)
		assert.NotEmpty(t, r.ZoneID)
	}

	_, err = svc.AggregateEconomyByZone("", "coin")
	assert.Error(t, err, "playerName 必填")
}

// TestLeaderboardEconomy_OrdersByNumericBalance 排行按余额**数值**倒序，纠正字符串字典序错误。
func TestLeaderboardEconomy_OrdersByNumericBalance(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	// 字符串字典序会把 "99.9" 排在 "1000"/"100" 前；正确数值序应为 1000 > 100 > 99.9。
	svc.Ingest("node-1", economyEvt(t, "inst-a", economyData("Rich", "coin", "z", "DEPOSIT", "1000", "1000", "1", "1")))
	svc.Ingest("node-1", economyEvt(t, "inst-b", economyData("Mid", "coin", "z2", "DEPOSIT", "100", "100", "2", "1")))
	svc.Ingest("node-1", economyEvt(t, "inst-c", economyData("Low", "coin", "z3", "DEPOSIT", "99.9", "99.9", "3", "1")))

	rows, err := svc.LeaderboardEconomy(EconomyLeaderboardQuery{Currency: "coin"})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"Rich", "Mid", "Low"}, []string{rows[0].PlayerName, rows[1].PlayerName, rows[2].PlayerName},
		"应按余额数值倒序（非字符串字典序）")
	assert.Equal(t, 1, rows[0].Rank)
	assert.Equal(t, 2, rows[1].Rank)
	assert.Equal(t, 3, rows[2].Rank)
	assert.Equal(t, "coin", rows[0].Currency)
	assert.Equal(t, "1000", rows[0].Balance)
}

// TestLeaderboardEconomy_RequiresCurrency 排行须锚定单一货币（跨货币不可比）。
func TestLeaderboardEconomy_RequiresCurrency(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	_, err := svc.LeaderboardEconomy(EconomyLeaderboardQuery{})
	assert.Error(t, err, "currency 必填")
}

// TestLeaderboardEconomy_FiltersAndLimit zone/node 过滤 + Limit 收敛 Top-N。
func TestLeaderboardEconomy_FiltersAndLimit(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	svc.Ingest("node-1", economyEvt(t, "inst-a", economyData("A", "coin", "zone-a", "DEPOSIT", "10", "10", "1", "1")))
	svc.Ingest("node-1", economyEvt(t, "inst-b", economyData("B", "coin", "zone-b", "DEPOSIT", "20", "20", "2", "1")))
	svc.Ingest("node-2", economyEvt(t, "inst-c", economyData("C", "coin", "zone-a", "DEPOSIT", "30", "30", "3", "1")))

	// 仅 zone-a：A(node-1) 与 C(node-2) 两行。
	zoneRows, err := svc.LeaderboardEconomy(EconomyLeaderboardQuery{Currency: "coin", ZoneID: "zone-a"})
	require.NoError(t, err)
	require.Len(t, zoneRows, 2)
	assert.Equal(t, "C", zoneRows[0].PlayerName, "zone-a 内 30 > 10")

	// 仅 node-1：A(zone-a) 与 B(zone-b) 两行。
	nodeRows, err := svc.LeaderboardEconomy(EconomyLeaderboardQuery{Currency: "coin", NodeUUID: "node-1"})
	require.NoError(t, err)
	require.Len(t, nodeRows, 2)

	// Limit=1：只取榜首。
	top1, err := svc.LeaderboardEconomy(EconomyLeaderboardQuery{Currency: "coin", Limit: 1})
	require.NoError(t, err)
	require.Len(t, top1, 1)
	assert.Equal(t, "C", top1[0].PlayerName, "全局榜首应为 30")
}

// TestLeaderboardEconomy_CrossZoneSamePlayerSeparateRows 跨区同名玩家各占一行参与排行（不合并）。
func TestLeaderboardEconomy_CrossZoneSamePlayerSeparateRows(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	svc.Ingest("node-1", economyEvt(t, "inst-a", economyData("Steve", "coin", "zone-a", "DEPOSIT", "100", "100", "1", "1")))
	svc.Ingest("node-1", economyEvt(t, "inst-b", economyData("Steve", "coin", "zone-b", "DEPOSIT", "50", "50", "2", "1")))

	rows, err := svc.LeaderboardEconomy(EconomyLeaderboardQuery{Currency: "coin"})
	require.NoError(t, err)
	require.Len(t, rows, 2, "同名玩家跨区是不同账户，各占一行")
	assert.Equal(t, "zone-a", rows[0].ZoneID, "zone-a 余额 100 居首")
	assert.Equal(t, "zone-b", rows[1].ZoneID)
}

// TestIngest_NonBusinessIgnored domain 为空（监控/治理事件）不落任何业务表。
func TestIngest_NonBusinessIgnored(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	svc.Ingest("node-1", &workerpb.PluginEvent{InstanceUuid: "inst-1", Type: "player_join", PlayerName: "Steve"})

	var envCount int64
	db.Model(&model.BusinessEvent{}).Count(&envCount)
	assert.Equal(t, int64(0), envCount, "非业务事件不落 envelope")
}

// TestIngest_BadEconomyDataStillEnvelopes data 缺关键字段时不落结构化，但 envelope 仍留原文（不丢事件）。
func TestIngest_BadEconomyDataStillEnvelopes(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	// 缺 currency/playerName 的坏 data，但带 dedupKey。
	bad := map[string]string{"ledgerId": "77", "zoneId": "zone-a"}
	frame := economyFrame(t, bad)
	svc.Ingest("node-1", &workerpb.PluginEvent{
		InstanceUuid: "inst-1", Type: "economy_change", Domain: economyDomain, DedupKey: "77", RawJson: frame,
	})

	var envCount, mirrorCount int64
	db.Model(&model.BusinessEvent{}).Count(&envCount)
	db.Model(&model.EconomyBalanceMirror{}).Count(&mirrorCount)
	assert.Equal(t, int64(1), envCount, "坏 data 的业务事件 envelope 仍应留原文")
	assert.Equal(t, int64(0), mirrorCount, "坏 data 不落结构化镜像")
}

// TestListBusinessEvents 按域过滤倒序取最近事件。
func TestListBusinessEvents(t *testing.T) {
	db := newBusinessEventTestDB(t)
	svc := NewBusinessEventService(db)
	svc.Ingest("node-1", economyEvt(t, "inst-1", economyData("Steve", "coin", "zone-a", "DEPOSIT", "1", "1", "1", "1")))
	svc.Ingest("node-1", economyEvt(t, "inst-1", economyData("Steve", "coin", "zone-a", "DEPOSIT", "1", "2", "2", "2")))

	events, err := svc.ListBusinessEvents(BusinessEventQuery{Domain: economyDomain})
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.True(t, events[0].ID > events[1].ID, "应按 id 倒序（最近在前）")
	assert.Equal(t, economyDomain, events[0].Domain)

	none, err := svc.ListBusinessEvents(BusinessEventQuery{Domain: "inventory"})
	require.NoError(t, err)
	assert.Empty(t, none, "其它域应为空")
}
