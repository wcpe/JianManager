package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestNonLegacyLogSourcesIsComplementOfLegacy 钉住「求补集」的语义：
// platform 来源 = 全部已知来源 − Legacy 集合。
//
// 为什么这条重要：保留策略依赖它圈定可清理的行。若求补出错（漏掉某来源），
// 后果是该来源的日志永远不会被保留策略处理——既不清也不归档，直到磁盘写满。
func TestNonLegacyLogSourcesIsComplementOfLegacy(t *testing.T) {
	got := nonLegacyLogSources()

	legacy := map[model.LogSource]bool{}
	for _, src := range legacyLogSources {
		legacy[src] = true
	}
	for _, src := range got {
		assert.Falsef(t, legacy[src], "Legacy 来源 %q 不应出现在 platform 集合中", src)
	}

	// 并集必须覆盖全部已知来源，否则有来源既不属于 Legacy 也不属于 platform，会被保留策略漏掉。
	union := map[model.LogSource]bool{}
	for _, src := range got {
		union[src] = true
	}
	for _, src := range legacyLogSources {
		union[src] = true
	}
	for _, src := range model.AllLogSources {
		assert.Truef(t, union[src], "已知来源 %q 既不在 Legacy 也不在 platform 集合中，会被保留策略漏掉", src)
	}

	// 当前取值域下 Legacy={instance,worker}，补集应恰为 {control_plane}。
	require.Len(t, got, 1)
	assert.Equal(t, model.LogSourceControlPlane, got[0])
}

// TestScopePlatformRetentionProducesEqualityNotNegation 钉住查询形态：
// cutover 打开时必须产生**肯定条件**（source IN ...），而不是 NOT IN。
//
// 为什么必须钉住形态而不是只验结果：两种写法结果等价，但执行计划天差地别。
// 生产实测（2026-10-09，logs 表 109 万行）：NOT IN 触发全表扫描 226ms，
// 等值/IN 走 idx_logs_source_time 索引 8ms。回退成 NOT IN 不会让任何功能测试变红，
// 只会在生产悄悄变慢，因此这里直接断言 SQL 中不含否定形态。
func TestScopePlatformRetentionProducesEqualityNotNegation(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	require.NoError(t, svc.cutover.SetEnabledChecked(true))

	sql := scopedRetentionSQL(t, svc, db)
	assert.NotContainsf(t, sql, "NOT IN", "cutover 打开时不得使用 NOT IN（SQLite 无法走索引，会退化为全表扫描）：%s", sql)
	assert.Containsf(t, sql, "source IN", "应使用肯定条件以命中 idx_logs_source_time：%s", sql)
}

// TestScopePlatformRetentionNoopWhenCutoverOff 钉住关闭语义：cutover 不开时不得附加任何来源过滤。
func TestScopePlatformRetentionNoopWhenCutoverOff(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	require.NoError(t, svc.cutover.SetEnabledChecked(false))

	sql := scopedRetentionSQL(t, svc, db)
	assert.NotContainsf(t, sql, "source", "cutover 关闭时不应附加来源过滤：%s", sql)
}

// TestScopePlatformRetentionKeepsLegacyRowsOut 钉住行为等价性：
// 改写后 Legacy 行仍必须被排除、platform 行仍必须被纳入（这是修复不能破坏的语义）。
//
// 这条防的是「为了让查询走索引而把排除条件写丢」——那会让 Legacy 行被 platform
// 容量淘汰挤出，违背的独立预算承诺。
func TestScopePlatformRetentionKeepsLegacyRowsOut(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	require.NoError(t, svc.cutover.SetEnabledChecked(true))

	old := time.Now().AddDate(0, 0, -90)
	seed(t, db, model.LogEntry{Source: model.LogSourceControlPlane, Level: model.LogLevelInfo, Message: "platform-old", Time: old})
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, Message: "instance-old", Time: old})
	seed(t, db, model.LogEntry{Source: model.LogSourceWorker, Level: model.LogLevelInfo, Message: "worker-old", Time: old})

	var scoped []model.LogEntry
	require.NoError(t, svc.scopePlatformRetention(db.Model(&model.LogEntry{})).Find(&scoped).Error)

	gotSources := map[model.LogSource]bool{}
	for _, row := range scoped {
		gotSources[row.Source] = true
	}
	assert.True(t, gotSources[model.LogSourceControlPlane], "platform 行必须被纳入保留巡检")
	assert.False(t, gotSources[model.LogSourceInstance], "Legacy instance 行必须被排除（有独立预算，不得被 platform 容量淘汰挤出）")
	assert.False(t, gotSources[model.LogSourceWorker], "Legacy worker 行必须被排除")
}

// scopedRetentionSQL 把 scopePlatformRetention 作用后的查询渲染成 SQL，供形态断言使用。
func scopedRetentionSQL(t *testing.T, svc *LogService, db *gorm.DB) string {
	t.Helper()
	stmt := svc.scopePlatformRetention(db.Model(&model.LogEntry{}))
	return stmt.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var rows []model.LogEntry
		return tx.Find(&rows)
	})
}
