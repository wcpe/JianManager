package database

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newSeedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := New(config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlertRule{}))
	return db
}

// TestSeedDefaultAlertRulesCreatesWithEnabledFalse 预置规则必须存在，且**默认不启用**。
//
// 「不启用」是这块功能的核心约束：阈值是业务判断，替用户默认打开告警
// 等于在他们渠道里凭空塞消息。
//
// 变异：把 Enabled: false 改成 true，本用例转红。
func TestSeedDefaultAlertRulesCreatesWithEnabledFalse(t *testing.T) {
	db := newSeedTestDB(t)
	require.NoError(t, seedDefaultAlertRules(db))

	var rules []model.AlertRule
	require.NoError(t, db.Find(&rules).Error)
	require.Len(t, rules, len(defaultAlertRuleSeeds), "所有种子规则都应当被创建")

	for _, r := range rules {
		require.False(t, r.Enabled, "规则 %q 必须默认不启用", r.Name)
		require.Empty(t, r.ChannelIDs, "种子规则不绑定告警渠道（那要用户自己选）")
		require.Equal(t, model.AlertTriggerMetric, r.TriggerType)
		require.Equal(t, "node", r.TargetType)
		require.NotEmpty(t, r.Metric, "每条规则都要有指标键")
		require.NotEmpty(t, r.Operator, "每条规则都要有比较符")
	}
}

// TestSeedDefaultAlertRulesIdempotent 重复调用不得重复创建。
//
// 依据是 UUID：这是规则的身份，不是名字（名字用户可能改）。
func TestSeedDefaultAlertRulesIdempotent(t *testing.T) {
	db := newSeedTestDB(t)
	require.NoError(t, seedDefaultAlertRules(db))
	require.NoError(t, seedDefaultAlertRules(db)) // 第二次
	require.NoError(t, seedDefaultAlertRules(db)) // 第三次

	var count int64
	require.NoError(t, db.Model(&model.AlertRule{}).Count(&count).Error)
	require.Equal(t, int64(len(defaultAlertRuleSeeds)), count, "重复执行不得增加规则数")
}

// TestSeedDoesNotOverwriteUserEdits 用户改过阈值/启用状态后，重跑不得覆盖。
//
// 这是容易写错的地方：很容易想成「每次都同步成最新默认值」。
// 但用户把 90 改成 85、或把它打开，那是**用户配置**，覆盖等于偷换。
func TestSeedDoesNotOverwriteUserEdits(t *testing.T) {
	db := newSeedTestDB(t)
	require.NoError(t, seedDefaultAlertRules(db))

	// 模拟用户把内存规则改成 85 并启用。
	require.NoError(t, db.Model(&model.AlertRule{}).
		Where("uuid = ?", "seed-node-mem-high").
		Updates(map[string]any{"threshold": 85.0, "enabled": true}).Error)

	// 再跑一次 seed。
	require.NoError(t, seedDefaultAlertRules(db))

	var rule model.AlertRule
	require.NoError(t, db.Where("uuid = ?", "seed-node-mem-high").First(&rule).Error)
	require.Equal(t, 85.0, rule.Threshold, "用户改过的阈值不得被覆盖回默认")
	require.True(t, rule.Enabled, "用户启用过的状态不得被覆盖回禁用")
}

// TestSeedCoversKeyMetrics 种子必须覆盖本轮新增/修复的关键指标。
//
// 这防止「改了指标名但忘了同步种子」——那样预置规则会指向不存在的指标，
// 且因为不启用，永远不会有人发现。
func TestSeedCoversKeyMetrics(t *testing.T) {
	db := newSeedTestDB(t)
	require.NoError(t, seedDefaultAlertRules(db))

	var rules []model.AlertRule
	require.NoError(t, db.Find(&rules).Error)

	want := map[string]bool{
		model.MetricNodeMemPct:       false,
		model.MetricNodeDiskPct:      false,
		model.MetricNodeIOWait:       false,
		model.MetricLogIngestHealthy: false,
		model.MetricLogVLDiskPct:     false,
	}
	for _, r := range rules {
		if _, ok := want[r.Metric]; ok {
			want[r.Metric] = true
		}
	}
	for metric, covered := range want {
		require.True(t, covered, "指标 %q 应当有预置规则", metric)
	}
}
