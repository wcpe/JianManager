package ingest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 回归：跨日重登记不得因 UTCDay 默认值被判「另一种配置」（2026-10-01 生产事故）。
//
// 事故链：RegisterSource 归一化把空 UTCDay 填成「今天」，而身份比较用整个
// SourceConfig 逐字段判等 → 午夜后同一源的再登记因登记日不同被判
// 「source identity already bound to another configuration」→ 实例日志采集
// 登记被拒 → 实例无法启动（当日 11 台起不来）。修复：比较前把 UTCDay 归零。
// 把 sameSourceConfig 改回逐字段比较即转红。
func TestSameSourceConfigIgnoresUTCDay(t *testing.T) {
	base := SourceConfig{
		LogSourceID:      "inst:9/file",
		SourceGeneration: "g1",
		StorageNamespace: "inst:9",
	}

	a := base
	a.UTCDay = "2026-09-30"
	b := base
	b.UTCDay = "2026-10-01" // 跨日：仅登记日不同

	require.True(t, sameSourceConfig(a, b),
		"仅登记日不同不构成「另一种配置」，跨日重登记必须放行")

	c := base
	c.UTCDay = "2026-10-01"
	c.SourceGeneration = "g2"
	require.False(t, sameSourceConfig(a, c),
		"真实的配置差异仍须拒绝")
}
