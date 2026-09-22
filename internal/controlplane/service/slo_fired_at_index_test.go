package service

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// sloEventQueryPlanLines 取某条 SQL 的 `EXPLAIN QUERY PLAN` 结果（每行一条计划文本）。
//
// SQLite 的 `EXPLAIN QUERY PLAN` 返回 (id, parent, notused, detail)，此处只取 detail；
// 断言用「是否出现 SEARCH <alias> USING INDEX <name>」而不是整行相等，以免被 SQLite
// 版本间 detail 文案差异误伤（本机 3.x 的措辞不一定与 CI 相同）。
func sloEventQueryPlanLines(t *testing.T, db *gorm.DB, sql string, args ...any) []string {
	t.Helper()
	rows, err := db.Raw("EXPLAIN QUERY PLAN "+sql, args...).Rows()
	require.NoError(t, err, "EXPLAIN QUERY PLAN 执行失败")
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		out = append(out, detail)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestSLOSlEventQuery_UsesFiredAtIndex （M-2）SLO 的故障事件窗口查询必须命中 fired_at 索引。
//
// 缺陷：`sloEventQuery` 的过滤条件是 `e.fired_at >= ? AND e.fired_at <= ? AND e.trigger_type IN ?`，
// 而 `AlertEvent` 原本只为 `rule_id`/`dedup_key` 建索引 → 计划为 `SCAN e`（全表扫 + 排序）。
// 影响面：SLO 是常驻读路径（前端 60s 轮询 × platform/node/instance 三维度，instance 维每实例一次），
// 而 alert_events 只增不减（无 TTL），事件到万级后每次请求都是全表扫。
//
// 本用例同时锁三件事：
//  1. AutoMigrate 能真的把 `idx_alert_events_fired_at` 建出来（存量库依赖这条补建路径）；
//  2. 生产 SQL 文本（含 LEFT JOIN alert_rules + 维度收敛条件）的计划里出现 FiredAt 的 SEARCH，
//     而不是 `SCAN e`；
//  3. 该索引是**追加**的——既有 rule_id / dedup_key 索引仍在（不覆盖、不改动）。
func TestSLOSlEventQuery_UsesFiredAtIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	var idxNames []string
	require.NoError(t, db.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'alert_events'").
		Scan(&idxNames).Error)
	require.Contains(t, idxNames, "idx_alert_events_fired_at",
		"alert_events 需有 fired_at 索引支撑 SLO 的窗口聚合，当前索引：%v", idxNames)
	// 既有索引不得因新增而消失（GORM 索引声明是逐项累加的，此处反向锁住）。
	require.Contains(t, idxNames, "idx_alert_events_rule_id")
	require.Contains(t, idxNames, "idx_alert_events_dedup_key")

	from := time.Now().UTC().Add(-time.Hour)
	to := from.Add(24 * time.Hour)

	// 用**生产函数**构造 SQL，避免测试与实现漂移（若哪天改了过滤条件，这里跟着变）。
	svc := NewMetricService(db)
	query := sloEventQuery(svc.db, from, to)
	stmt := query.Session(&gorm.Session{DryRun: true}).Find(&[]model.AlertEvent{}).Statement
	require.NotEmpty(t, stmt.SQL.String())

	plan := sloEventQueryPlanLines(t, db, stmt.SQL.String(), stmt.Vars...)
	joined := strings.Join(plan, "\n")
	t.Logf("被解释的 SQL：%s", stmt.SQL.String())
	t.Logf("alert_events 计划：\n%s", joined)

	require.NotContains(t, joined, "SCAN e",
		"fired_at 过滤不得退化为对 alert_events 的全表扫：\n%s", joined)
	require.Contains(t, joined, "idx_alert_events_fired_at",
		"计划中应出现 fired_at 索引（SEARCH e USING INDEX idx_alert_events_fired_at），实得：\n%s", joined)
	// 不应出现「先把 alert_events 全表扫成临时表再过滤」的等价退化。
	require.NotContains(t, joined, "USE TEMP B-TREE FOR ORDER BY e",
		"窗口过滤不应退化为排序临时表：\n%s", joined)
}

// TestSLOSlEventQuery_FiredAtIndexAppliedOnLegacyDB （M-2 存量库路径）在没有索引的老库上
// 先造数据、再按 CP 启动路径 AutoMigrate 一次，确认索引被**补建**且查询计划随之改变
// （SCAN e → SEARCH）。这条覆盖「部署新二进制时存量生产库如何拿到索引」。
func TestSLOSlEventQuery_FiredAtIndexAppliedOnLegacyDB(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	// 1) 模拟存量库：手工建**无 fired_at 索引**的老表结构 + 若干历史事件。
	require.NoError(t, db.Exec(`CREATE TABLE alert_events (
		id integer PRIMARY KEY AUTOINCREMENT,
		rule_id integer NOT NULL,
		target_id integer,
		level varchar(16) DEFAULT 'warn',
		trigger_type varchar(32) DEFAULT 'metric',
		dedup_key varchar(256),
		value real,
		direction varchar(8),
		message varchar(512),
		count integer DEFAULT 1,
		resolved numeric DEFAULT false,
		fired_at datetime,
		last_fired_at datetime,
		resolved_at datetime,
		acknowledged numeric DEFAULT false,
		acknowledged_by integer,
		acknowledged_at datetime,
		read numeric DEFAULT false
	)`).Error)
	require.NoError(t, db.Exec("CREATE INDEX idx_alert_events_rule_id ON alert_events(rule_id)").Error)
	require.NoError(t, db.Exec("CREATE INDEX idx_alert_events_dedup_key ON alert_events(dedup_key)").Error)
	require.NoError(t, db.Exec("CREATE TABLE alert_rules (id integer PRIMARY KEY AUTOINCREMENT, target_type varchar(16), deleted_at datetime)").Error)

	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 200; i++ {
		require.NoError(t, db.Exec(
			"INSERT INTO alert_events (rule_id, target_id, trigger_type, fired_at) VALUES (?, ?, ?, ?)",
			uint(i%3+1), uint(i%5+1), model.AlertTriggerInstanceCrash, base.Add(time.Duration(i)*time.Second)).Error)
	}

	svc := NewMetricService(db)
	query := sloEventQuery(svc.db, base, base.Add(24*time.Hour))
	stmt := query.Session(&gorm.Session{DryRun: true}).Find(&[]model.AlertEvent{}).Statement

	// 2) 补索引前的计划：全表扫（正是审查员实测的 `SCAN e`）。
	before := strings.Join(sloEventQueryPlanLines(t, db, stmt.SQL.String(), stmt.Vars...), "\n")
	require.Contains(t, before, "SCAN e", "前提校验：无索引时应为全表扫，实得：\n%s", before)

	// 3) 走 CP 启动的迁移路径。
	require.NoError(t, db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))

	// 4) 补索引后：同样的 SQL 文本改用索引。
	after := strings.Join(sloEventQueryPlanLines(t, db, stmt.SQL.String(), stmt.Vars...), "\n")
	t.Logf("补索引前：\n%s\n补索引后：\n%s", before, after)
	require.NotContains(t, after, "SCAN e", "AutoMigrate 补建索引后不应再全表扫：\n%s", after)
	require.Contains(t, after, "idx_alert_events_fired_at")

	// 5) 数据不受迁移影响（AutoMigrate 是纯加索引，不重写行）。
	var total int64
	require.NoError(t, db.Table("alert_events").Count(&total).Error)
	require.Equal(t, int64(200), total)
}

// TestSLOSlEventQuery_IndexNotAbusedByFullRangeScan （M-2 边界）窗口覆盖全表时索引
// 不再是净收益，但**计划不得退化为「用 fired_at 索引 + 反复回表」的二次开销路径**。
// 这条用例的实质断言是「查询结果与索引是否存在无关」——索引只影响计划，不影响语义，
// 是为「加索引顺手改了 WHERE」这类错误留一道回归线。
func TestSLOSlEventQuery_IndexNotAbusedByFullRangeScan(t *testing.T) {
	seedEvents := func(t *testing.T, withIndex bool) []model.AlertEvent {
		t.Helper()
		db, err := gorm.Open(sqlite.Open("file:"+t.Name()+strconv.FormatBool(withIndex)+"?mode=memory&cache=shared"), &gorm.Config{})
		require.NoError(t, err)
		require.NoError(t, db.AutoMigrate(&model.AlertRule{}, &model.AlertEvent{}))
		if !withIndex {
			require.NoError(t, db.Exec("DROP INDEX idx_alert_events_fired_at").Error)
		}
		base := time.Now().UTC().Add(-time.Hour)
		rule := &model.AlertRule{Name: "r", TriggerType: model.AlertTriggerMetric, TargetType: "instance", Enabled: true}
		require.NoError(t, db.Create(rule).Error)
		for i := 0; i < 10; i++ {
			require.NoError(t, db.Create(&model.AlertEvent{
				RuleID: rule.ID, TargetID: 1, TriggerType: model.AlertTriggerInstanceCrash,
				DedupKey: "k" + strconv.Itoa(i), FiredAt: base.Add(time.Duration(i) * time.Minute),
			}).Error)
		}
		svc := NewMetricService(db)
		var got []model.AlertEvent
		require.NoError(t, sloEventQuery(svc.db, base.Add(-time.Minute), base.Add(time.Hour)).Find(&got).Error)
		return got
	}

	withIdx := seedEvents(t, true)
	withoutIdx := seedEvents(t, false)
	require.Len(t, withIdx, 10)
	require.Equal(t, len(withoutIdx), len(withIdx), "索引存在与否不得改变窗口过滤的命中集合")
}
