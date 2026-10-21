package vlsup

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestQueryTimeRangeLimitFollowsRetention 钉住「查询时间窗上限随 retention 派生」。
//
// 缺陷形态（2026-10-22 复审 P1）：`-search.maxQueryTimeRange` 曾是硬编码 `31d`，
// 而同处注释自己写着「31d = retention 30d + 1d 余量」——即它本来就是 retention 的
// 函数，只是用字面量写死了。一旦 retention 变动，两者**静默脱钩**：
// 保留期 120 天，任何跨度 >31d 的查询被 VL 400 拒绝；而投影代次占用探测**必须**
// 覆盖「同一代次名在保留期内的任意一天」，覆盖不到就「按未占用继续」⇒ 重名防护
// 整条失效 ⇒ 撞上上一生命周期的同名行 ⇒ 永不发布 ⇒ 采集停摆（2026-10-08 事故形态）。
//
// 变异验证：把 `QueryTimeRangeLimit` 改回返回 "31d" ⇒ 本用例转红。
func TestQueryTimeRangeLimitFollowsRetention(t *testing.T) {
	cases := []struct {
		retention string
		want      string
	}{
		{"30d", "744h"},   // 30d + 1d = 31d = 744h（历史验证过的值，必须逐位还原）
		{"120d", "2904h"}, // 120d + 1d = 121d
		{"90d", "2184h"},
		{"720h", "744h"}, // 等价写法：h 后缀
	}
	for _, tc := range cases {
		t.Run(tc.retention, func(t *testing.T) {
			cfg := InstanceConfig{RetentionPeriod: tc.retention}
			got := cfg.QueryTimeRangeLimit()
			require.Equal(t, tc.want, got,
				"查询时间窗上限必须 = retention + 1d 余量（保留期越长，探测窗必须越大）")

			// 解析后必须严格大于 retention：否则覆盖不到保留期最后一天。
			d, err := parseRetentionDuration(tc.retention)
			require.NoError(t, err)
			limit, err := time.ParseDuration(got)
			require.NoError(t, err)
			require.Greater(t, limit, d, "上限必须严格大于 retention，否则探测覆盖不全")
			require.GreaterOrEqual(t, limit-d, 24*time.Hour, "余量至少 1 天")
		})
	}

	t.Run("非法 retention 回落到默认而非 31d", func(t *testing.T) {
		// 空值/乱码不能让上限缩回 31d —— 那会把「覆盖不全」的静默失败重新引入。
		for _, bad := range []string{"", "  ", "abc", "0d", "-5d"} {
			cfg := InstanceConfig{RetentionPeriod: bad}
			got := cfg.QueryTimeRangeLimit()
			def, err := parseRetentionDuration(DefaultRetentionPeriod)
			require.NoError(t, err)
			limit, err := time.ParseDuration(got)
			require.NoError(t, err)
			require.Greater(t, limit, def,
				"非法输入 %q 必须回落到默认 retention 派生值（实际 %s）", bad, got)
		}
	})
}

// TestBuildArgsUsesDerivedQueryTimeRange 确认派生值真的进了命令行。
//
// 上面那个用例只测纯函数；如果 BuildArgs 仍写死 "31d"，纯函数再对也没用。
// 这正是本项目两次「测试平凡通过」的形态之一：断言一个没被调用的函数。
func TestBuildArgsUsesDerivedQueryTimeRange(t *testing.T) {
	cfg := InstanceConfig{
		Namespace:       NamespaceHot,
		Port:            DefaultPortHot,
		StorageDataPath: "/tmp/vl-hot",
		RetentionPeriod: "120d",
		AuthUsername:    DefaultAuthUsername,
		AuthPassword:    "p",
	}
	args, err := BuildArgs(cfg)
	require.NoError(t, err)

	var got string
	for _, a := range args {
		if strings.HasPrefix(a, "-search.maxQueryTimeRange=") {
			got = strings.TrimPrefix(a, "-search.maxQueryTimeRange=")
		}
	}
	require.Equal(t, "2904h", got,
		"BuildArgs 必须使用派生值；写死 31d 会在 retention=120d 时让探测覆盖不全")
	require.NotContains(t, args, "-search.maxQueryTimeRange=31d", "不得再出现硬编码 31d")
}
