package service

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

// TestAgentCallLog_RecordTruncatesAction 超长 Action 落库前截断到列宽上限。
//
// 真实触发路径：MCP 对未知工具名拼 "mcp.tool." + toolName，而 toolName 由客户端请求体提供
// （上限 4MB）。不截断则 MySQL 严格模式插入报 data too long，RecordSafe 只打 WARN → 该次调用
// 流水整行消失（等于可被用来抹掉审计痕迹）。故断言截断而非报错。
func TestAgentCallLog_RecordTruncatesAction(t *testing.T) {
	db := setupAgentCallLogDB(t)
	svc := NewAgentCallLogService(db)
	// 常量必须与 agent_call_logs.action varchar(64) 对齐，否则截断长度形同虚设。
	assert.Equal(t, 64, agentCallActionMaxLen)

	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "t", Action: "mcp.tool." + strings.Repeat("x", 4000),
		Client: AgentClientMCP, Success: false,
	}))
	var row model.AgentCallLog
	require.NoError(t, db.First(&row).Error)
	assert.Len(t, row.Action, agentCallActionMaxLen, "超长 Action 必须截断落库")
	assert.Equal(t, "mcp.tool.", row.Action[:len("mcp.tool.")], "保留前部，便于溯源到工具调用前缀")

	// 边界：恰好等于上限时不截断（保证正常长度不受影响）。
	exact := strings.Repeat("a", agentCallActionMaxLen)
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "t", Action: exact, Client: AgentClientMCP, Success: true,
	}))
	var row2 model.AgentCallLog
	require.NoError(t, db.Where("action = ?", exact).First(&row2).Error)
	assert.Equal(t, exact, row2.Action)
}

// TestAgentCallLog_RecordTruncatesActionOnRuneBoundary 多字节 Action 按**字符数**截断，且不得切碎字符。
//
// 上面那条用例只用了 ASCII（strings.Repeat("x", 4000)），覆盖不到真正的缺陷：按**字节**截断会把一个
// 多字节字符切成两半，产出非法 UTF-8；MySQL（utf8mb4 严格模式）对非法字节序列同样报 1366 Incorrect
// string value 整行拒收，于是「截断」这个兜底自己失效，该次调用流水照样静默丢失——正是它要堵的缺口。
// 触发路径真实存在：MCP 对未知工具名拼 "mcp.tool." + toolName，toolName 由请求体提供（不受白名单约束）。
// 另一半是语义错误：varchar(64) 在 MySQL 计**字符**，30 个汉字（90 字节）本可完整入库，按字节判定
// 会把它们无谓截断。
func TestAgentCallLog_RecordTruncatesActionOnRuneBoundary(t *testing.T) {
	db := setupAgentCallLogDB(t)
	svc := NewAgentCallLogService(db)

	const prefix = "mcp.tool."
	// 超长：9 个 ASCII + 200 个汉字（609 字节）。按字节截到 64 必然落在汉字中间。
	action := prefix + strings.Repeat("测", 200)
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "t", Action: action, Client: AgentClientMCP, Success: false,
	}))

	var row model.AgentCallLog
	require.NoError(t, db.First(&row).Error)
	assert.True(t, utf8.ValidString(row.Action),
		"截断结果必须是合法 UTF-8，否则 MySQL 仍会整行拒收，流水静默丢失（等于截断没做）")
	assert.Equal(t, agentCallActionMaxLen, utf8.RuneCountInString(row.Action),
		"按字符数截到列宽上限（varchar(64) 计字符，不是字节）")
	assert.Equal(t, prefix, row.Action[:len(prefix)], "保留前部，便于溯源到工具调用前缀")

	// 边界：30 个汉字 = 30 字符（90 字节）本可完整入库，不得因按字节判定而被无谓截断。
	short := strings.Repeat("测", 30)
	require.Equal(t, 30, utf8.RuneCountInString(short))
	require.Greater(t, len(short), agentCallActionMaxLen, "该样例的字节数需超过上限，才能暴露按字节判定的问题")
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 1, TokenName: "t", Action: short, Client: AgentClientMCP, Success: true,
	}))
	var row2 model.AgentCallLog
	require.NoError(t, db.Where("action = ?", short).First(&row2).Error)
	assert.Equal(t, short, row2.Action, "字符数在限内即不得截断（30 个汉字本可完整入库）")
}

// TestActivityByToken_SameTimestampTakesLargerID 同一 Token、同一 created_at 的两行取 id 更大者。
//
// 自联接按 MAX(created_at) 命中的是**所有**同刻行，靠 ORDER BY created_at DESC, id DESC 让最新那行
// 先出现；「已写入」判定必须与此一致（FR-489 review 修复 2）。
func TestActivityByToken_SameTimestampTakesLargerID(t *testing.T) {
	db := setupActivityDB(t)
	svc := NewAgentCallLogService(db)
	now := time.Now().Truncate(time.Second)

	seedActivityToken(t, db, 5, "ci", "jmat_ab12")
	// 两行 created_at 完全一致，仅写入顺序（id）不同：后写的才是「最近操作」。
	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 5, TokenName: "ci", Action: AgentActionWhoami, Client: AgentClientMCP,
		Success: true, IP: "10.0.0.1", CreatedAt: now,
	})
	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 5, TokenName: "ci", Action: AgentActionListInstances, Client: AgentClientMCP,
		Success: true, IP: "10.0.0.1", CreatedAt: now,
	})

	items, err := svc.ActivityByToken(time.Hour)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, AgentActionListInstances, items[0].LastAction, "同刻并列时取 id 更大（更晚写入）那行")
	assert.Equal(t, int64(2), items[0].CallCount, "同刻两行都计入窗口内计数")
}

// TestActivityByToken_LatestEmptyActionNotOverwritten 最新一行 action 为空串时，不得被同刻旧行覆盖。
//
// action 列 not null 但不禁止空串，故不能用 LastAction != "" 当「已写入」哨兵；否则空串行会被
// 同刻更小 id 的旧行反复覆盖，LastActivityAt 与 LastAction 变成较旧那行的值（时间与操作自相矛盾）。
func TestActivityByToken_LatestEmptyActionNotOverwritten(t *testing.T) {
	db := setupActivityDB(t)
	svc := NewAgentCallLogService(db)
	now := time.Now().Truncate(time.Second)

	// 故意不 seed agent_token：让 TokenName 退回流水行快照，从而能观察是哪一行写进去的。
	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 3, TokenName: "旧行快照", Action: AgentActionWhoami, Client: AgentClientMCP,
		Success: true, IP: "10.0.0.1", CreatedAt: now,
	})
	seedActivityLog(t, db, model.AgentCallLog{
		TokenID: 3, TokenName: "最新行快照", Action: "", Client: AgentClientMCP,
		Success: true, IP: "10.0.0.1", CreatedAt: now,
	})

	items, err := svc.ActivityByToken(time.Hour)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Empty(t, items[0].LastAction, "最新行 action 为空串，不能被同刻旧行的 action 覆盖成 whoami")
	assert.Equal(t, "最新行快照", items[0].TokenName, "名称快照同样应取最新那行")
	assert.WithinDuration(t, now, items[0].LastActivityAt, time.Second)
}

// TestActivityByToken_NullSuccessCountsAsFailure success 列可空时 NULL 不得被算成成功。
//
// 模型刻意不加 not null（理由见 model.AgentCallLog.Success：既有库会被整表重建，且库中一旦存在
// success IS NULL 的行，AutoMigrate 会直接失败并阻断 CP 启动），故「NULL 计失败」这一审计不变量
// 由聚合 SQL 承担——本用例锁定该语义。
func TestActivityByToken_NullSuccessCountsAsFailure(t *testing.T) {
	db := setupActivityDB(t)
	svc := NewAgentCallLogService(db)

	seedActivityToken(t, db, 11, "ci", "jmat_ab12")
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 11, TokenName: "ci", Action: AgentActionWhoami,
		Client: AgentClientMCP, Success: true, IP: "10.0.0.1",
	}))
	require.NoError(t, svc.Record(AgentCallRecord{
		TokenID: 11, TokenName: "ci", Action: AgentActionWhoami,
		Client: AgentClientMCP, Success: false, Error: "forbidden", IP: "10.0.0.1",
	}))
	// 写路径不会产生 NULL（GORM Create/Select 均显式落 0/1），这里是模拟历史脏数据 / 人工 SQL 遗留。
	require.NoError(t, db.Exec(`INSERT INTO agent_call_logs
		(token_id, token_name, action, client, success, latency_ms, ip, created_at)
		VALUES (?,?,?,?,NULL,0,?,?)`,
		11, "ci", AgentActionWhoami, AgentClientMCP, "10.0.0.1", time.Now().Truncate(time.Second)).Error)

	items, err := svc.ActivityByToken(time.Hour)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, int64(3), items[0].CallCount)
	assert.Equal(t, int64(2), items[0].FailureCount,
		"NULL 不得落进「非失败」分支被算作成功（否则失败数偏小、审计面全绿）")
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
