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

func setupAgentCallLogDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AgentCallLog{}))
	return db
}

func TestNormalizeAgentClient(t *testing.T) {
	assert.Equal(t, AgentClientUnknown, NormalizeAgentClient(""))
	assert.Equal(t, AgentClientJmagent, NormalizeAgentClient("jmagent"))
	assert.Equal(t, AgentClientMCP, NormalizeAgentClient("MCP"))
	assert.Equal(t, AgentClientCurl, NormalizeAgentClient(" curl "))
	assert.Equal(t, AgentClientUnknown, NormalizeAgentClient("evil<script>"))
	assert.Equal(t, AgentClientUnknown, NormalizeAgentClient("not-a-known-client"))
	// 超长归 unknown
	long := make([]byte, agentClientHeaderMaxLen+1)
	for i := range long {
		long[i] = 'a'
	}
	assert.Equal(t, AgentClientUnknown, NormalizeAgentClient(string(long)))
}

func TestAgentCallLog_RecordListCountPurge(t *testing.T) {
	db := setupAgentCallLogDB(t)
	svc := NewAgentCallLogService(db)

	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "ci", Action: AgentActionWhoami,
		Client: "jmagent", Success: true, LatencyMs: 12, IP: "1.2.3.4",
	}))
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "ci", Action: AgentActionListNodes,
		Client: "curl", Success: false, Error: "forbidden", LatencyMs: 5, IP: "1.2.3.4",
	}))
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 2, TokenName: "other", Action: AgentActionWhoami,
		Client: "unknown", Success: true, IP: "9.9.9.9",
	}))

	// List 全量
	page, err := svc.List(AgentCallLogFilter{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(3), page.Total)
	assert.Len(t, page.Items, 3)
	// 稳定排序：最新在前
	assert.True(t, !page.Items[0].CreatedAt.Before(page.Items[1].CreatedAt))

	// 过滤 tokenId + success
	tid := uint(1)
	ok := false
	page, err = svc.List(AgentCallLogFilter{TokenID: &tid, Success: &ok, Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)
	assert.Equal(t, AgentActionListNodes, page.Items[0].Action)
	assert.Equal(t, AgentClientCurl, page.Items[0].Client)

	// 过滤 action
	act := AgentActionWhoami
	page, err = svc.List(AgentCallLogFilter{Action: &act, Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(2), page.Total)

	// Count24h
	n, err := svc.Count24h(1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	n, err = svc.Count24h(99)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// Count24hMap
	m, err := svc.Count24hMap([]uint{1, 2, 99})
	require.NoError(t, err)
	assert.Equal(t, int64(2), m[1])
	assert.Equal(t, int64(1), m[2])
	_, has99 := m[99]
	assert.False(t, has99)

	// 超期清理：手动把一条改成 20 天前
	cutoff := time.Now().Add(-20 * 24 * time.Hour)
	require.NoError(t, db.Model(&model.AgentCallLog{}).Where("token_id = ?", 2).Update("created_at", cutoff).Error)
	deleted, err := svc.PurgeExpired()
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	page, err = svc.List(AgentCallLogFilter{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(2), page.Total)
}

func TestAgentCallLog_RecordTruncatesErrorAndStripsToken(t *testing.T) {
	db := setupAgentCallLogDB(t)
	svc := NewAgentCallLogService(db)

	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "t", Action: "a", Success: false, Error: string(long),
	}))
	var row model.AgentCallLog
	require.NoError(t, db.First(&row).Error)
	assert.LessOrEqual(t, len(row.Error), agentCallErrorMaxLen)

	// 含 jmat_ 脱敏
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "t", Action: "b", Success: false, Error: "bad jmat_abcsecret",
	}))
	var row2 model.AgentCallLog
	require.NoError(t, db.Where("action = ?", "b").First(&row2).Error)
	assert.NotContains(t, row2.Error, "jmat_abcsecret")
	assert.Contains(t, row2.Error, "脱敏")
}

func TestAgentCallLog_RecordCapability(t *testing.T) {
	db := setupAgentCallLogDB(t)
	svc := NewAgentCallLogService(db)

	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "v2", Action: AgentActionInstanceStart,
		Capability: AgentCapabilityInstanceLife, Client: "mcp", Success: true,
	}))
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 2, TokenName: "v1", Action: AgentActionInstanceStart,
		Capability: AgentLegacyCapabilityInstanceLife, Client: "curl", Success: false, Error: "forbidden",
	}))
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 3, TokenName: "sess", Action: "mcp.session.open",
		Client: "mcp", Success: true, // capability 空
	}))

	var rows []model.AgentCallLog
	require.NoError(t, db.Order("id ASC").Find(&rows).Error)
	require.Len(t, rows, 3)
	assert.Equal(t, AgentCapabilityInstanceLife, rows[0].Capability)
	assert.Equal(t, AgentLegacyCapabilityInstanceLife, rows[1].Capability)
	assert.Empty(t, rows[2].Capability)
}

func TestAgentCallLog_CustomRetention(t *testing.T) {
	db := setupAgentCallLogDB(t)
	svc := NewAgentCallLogServiceWithRetention(db, 1)
	assert.Equal(t, 1, svc.RetentionDays())

	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "t", Action: AgentActionWhoami, Success: true,
	}))
	// 改成 2 天前 → 超过 1 天保留应被清
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, db.Model(&model.AgentCallLog{}).Where("token_id = ?", 1).Update("created_at", old).Error)
	n, err := svc.PurgeExpired()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}
