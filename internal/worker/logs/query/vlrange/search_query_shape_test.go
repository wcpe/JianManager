package vlrange

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 回归：搜索查询的管道顺序是性能关键（2026-09-30 生产：inst:153/2026-09-27 巨日
// 74.7 万条，整目标被标 not_ready、平台 complete=false）。
//
// VL 只在 `| fields` 先于 `| sort by`、且 `| sort by` 后**紧跟** `| limit` 时才走
// top-K；否则对全宽行做全量排序物化 → 超内建内存上限 → 400 cannot execute query。
// 本用例锁住顺序：把 fields 移到 sort 之后、或去掉/移走 limit，即转红。
func TestSearchQueryFieldsBeforeSortAndLimitAfter(t *testing.T) {
	rng := query.AuthoritativeRange{
		Key: catalog.PartitionKey{StorageNamespace: "inst:9", UTCDay: "2026-09-27"},
	}
	q := query.RangeQuery{Budget: query.Budget{Limit: 99}}
	got := searchQuery(rng, q)

	iFields := strings.Index(got, "| fields ")
	iSort := strings.Index(got, "| sort by ")
	require.Greater(t, iFields, -1, "必须显式投影字段：否则 VL 对全宽行排序物化")
	require.Greater(t, iSort, iFields, "`| fields` 必须先于 `| sort by`")
	require.Regexp(t, `\| sort by \([^)]*\) \| limit \d+$`, got,
		"`| sort by` 后必须紧跟 `| limit`（top-K），且 limit 为最后一个管道")
	require.True(t, strings.HasSuffix(got, "| limit 100"),
		"limit 应为 EffectiveLimit+1（99+1=100），实际: %s", got)
	// 排序键只允许 `_time desc`：多键会让 VL 放弃时间有序块合并、整分区物化排序
	//（2026-09-30 实测 77M 行/169MB/2.9s vs 仅时间 40ms）。恢复多键即转红。
	require.Contains(t, got, "| sort by (_time desc) | limit ",
		"VL 侧只按 _time desc 排序；全键次序由客户端 SortEvents 与游标承接")
}
