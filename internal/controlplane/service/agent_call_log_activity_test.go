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

// Token 维度活动聚合（ADR-096 决策 6 / FR-489）：
// 数据源是 agent_call_logs（FR-390），按 token_id 在窗口内聚合。

// setupActivityDB 建库并迁移活动聚合涉及的两张表（流水 + Token 名/前缀来源）。
func setupActivityDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AgentCallLog{}, &model.AgentToken{}))
	return db
}

func seedActivityToken(t *testing.T, db *gorm.DB, id uint, name, prefix string) {
	t.Helper()
	require.NoError(t, db.Create(&model.AgentToken{
		ID: id, Name: name, TokenHash: prefix + "-hash", TokenPrefix: prefix,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}).Error)
}

func seedActivityLog(t *testing.T, db *gorm.DB, row model.AgentCallLog) {
	t.Helper()
	require.NoError(t, db.Create(&row).Error)
}

// TestActivityByToken_MultiTokenAggregation 多 Token：计数/失败数/最近操作/来源分布。
func TestActivityByToken_MultiTokenAggregation(t *testing.T) {
	db := setupActivityDB(t)
	svc := NewAgentCallLogService(db)
	// 种子时间与写入路径（time.Now()）保持同一时区：SQLite 以文本存储 datetime，
	// 混用 UTC 文本与本地时间比较会因偏移量不同而错判窗口边界。
	now := time.Now().Truncate(time.Second)

	seedActivityToken(t, db, 7, "运维自动化", "jmat_ab12")
	seedActivityToken(t, db, 9, "备用", "jmat_cd34")

	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 7, TokenName: "旧名", Action: AgentActionWhoami, Client: AgentClientMCP,
		Success: true, IP: "10.0.0.5", CreatedAt: now.Add(-2 * time.Hour),
	})
	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 7, TokenName: "旧名", Action: AgentActionWhoami, Client: AgentClientMCP,
		Success: false, Error: "forbidden", IP: "10.0.0.5", CreatedAt: now.Add(-90 * time.Minute),
	})
	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 7, TokenName: "旧名", Action: AgentActionListInstances, Client: AgentClientCurl,
		Success: true, IP: "10.0.0.6", CreatedAt: now.Add(-30 * time.Minute),
	})
	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 9, TokenName: "备用", Action: AgentActionListNodes, Client: AgentClientMCP,
		Success: true, IP: "10.0.0.9", CreatedAt: now.Add(-10 * time.Minute),
	})

	items, err := svc.ActivityByToken(24 * time.Hour)
	require.NoError(t, err)
	require.Len(t, items, 2, "无流水的 Token 不应出现")

	// 排序：最近活动降序（token 9 更近）。
	assert.Equal(t, uint(9), items[0].TokenID)
	assert.Equal(t, uint(7), items[1].TokenID)

	got := items[1]
	assert.Equal(t, "运维自动化", got.TokenName, "名称取 agent_tokens 当前值而非流水快照")
	assert.Equal(t, "jmat_ab12", got.TokenPrefix)
	assert.Equal(t, int64(3), got.CallCount)
	assert.Equal(t, int64(1), got.FailureCount)
	assert.Equal(t, AgentActionListInstances, got.LastAction, "最近操作为窗口内最新一行的 action")
	assert.WithinDuration(t, now.Add(-30*time.Minute), got.LastActivityAt, time.Second)
	assert.Equal(t, []string{"10.0.0.5", "10.0.0.6"}, got.ClientIPs, "来源 IP 去重且有序")
	assert.Equal(t, map[string]int64{AgentClientMCP: 2, AgentClientCurl: 1}, got.Clients)

	assert.Equal(t, int64(1), items[0].CallCount)
	assert.Equal(t, int64(0), items[0].FailureCount)
	assert.Equal(t, AgentActionListNodes, items[0].LastAction)
	assert.Equal(t, map[string]int64{AgentClientMCP: 1}, items[0].Clients)
}

// TestActivityByToken_WindowBoundary 窗口边界：更早的记录不计入更短窗口。
func TestActivityByToken_WindowBoundary(t *testing.T) {
	db := setupActivityDB(t)
	svc := NewAgentCallLogService(db)
	// 种子时间与写入路径（time.Now()）保持同一时区：SQLite 以文本存储 datetime，
	// 混用 UTC 文本与本地时间比较会因偏移量不同而错判窗口边界。
	now := time.Now().Truncate(time.Second)

	seedActivityToken(t, db, 1, "ci", "jmat_ab12")
	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 1, TokenName: "ci", Action: AgentActionWhoami, Client: AgentClientMCP,
		Success: true, IP: "10.0.0.5", CreatedAt: now.Add(-30 * time.Minute),
	})
	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 1, TokenName: "ci", Action: AgentActionListNodes, Client: AgentClientMCP,
		Success: false, Error: "boom", IP: "10.0.0.5", CreatedAt: now.Add(-2 * time.Hour),
	})

	short, err := svc.ActivityByToken(time.Hour)
	require.NoError(t, err)
	require.Len(t, short, 1)
	assert.Equal(t, int64(1), short[0].CallCount, "2 小时前的记录不在 1h 窗口内")
	assert.Equal(t, int64(0), short[0].FailureCount)
	assert.Equal(t, AgentActionWhoami, short[0].LastAction)

	long, err := svc.ActivityByToken(24 * time.Hour)
	require.NoError(t, err)
	require.Len(t, long, 1)
	assert.Equal(t, int64(2), long[0].CallCount, "24h 窗口含两条")
	assert.Equal(t, int64(1), long[0].FailureCount)
	assert.Equal(t, AgentActionWhoami, long[0].LastAction, "最近操作仍是最新一行")

	// 非正窗口应报错，避免退化成无界扫描。
	_, err = svc.ActivityByToken(0)
	assert.Error(t, err)
}

// TestActivityByToken_NoData 无数据：返回空结果且不报错。
func TestActivityByToken_NoData(t *testing.T) {
	db := setupActivityDB(t)
	svc := NewAgentCallLogService(db)

	items, err := svc.ActivityByToken(24 * time.Hour)
	require.NoError(t, err, "无流水不是错误（前端空态依赖此语义）")
	assert.Empty(t, items)
}

// TestActivityByToken_TokenRowRemovedFallsBackToLogSnapshot Token 行被硬删时退回流水里的名称快照。
func TestActivityByToken_TokenRowRemovedFallsBackToLogSnapshot(t *testing.T) {
	db := setupActivityDB(t)
	svc := NewAgentCallLogService(db)

	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 42, TokenName: "已删 Token", Action: AgentActionWhoami, Client: AgentClientMCP,
		Success: true, IP: "10.0.0.7", CreatedAt: time.Now(),
	})

	items, err := svc.ActivityByToken(24 * time.Hour)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "已删 Token", items[0].TokenName)
	assert.Empty(t, items[0].TokenPrefix, "Token 行已不在，前缀无从取得")
}
