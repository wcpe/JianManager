package vlsup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 天界口径是历史上三次事故的共同根因，这里用真实响应形态逐点钉住。
//
// 实测样本（v1.52.0，2026-11-04 窗口）：
//
//	{"hits":[{"fields":{"log_source_id":"inst:317/file"},
//	          "timestamps":["2026-11-01T00:00:00Z","2026-11-02T00:00:00Z",...],
//	          "values":[0,1746,...],"total":243}]}
const sampleHits = `{"hits":[
 {"fields":{"log_source_id":"inst:317/file"},
  "timestamps":["2026-11-01T00:00:00Z","2026-11-02T00:00:00Z","2026-11-03T00:00:00Z","2026-11-04T00:00:00Z"],
  "values":[0,1746,0,243],"total":1989},
 {"fields":{"log_source_id":"inst:318/file"},
  "timestamps":["2026-11-01T00:00:00Z","2026-11-02T00:00:00Z","2026-11-03T00:00:00Z","2026-11-04T00:00:00Z"],
  "values":[2880,0,0,890],"total":3770}
]}`

func TestParseHitsSourceDaysKeepsOnlyNonZeroBuckets(t *testing.T) {
	got, err := parseHitsSourceDays([]byte(sampleHits))
	require.NoError(t, err)

	// 每条 series 有 4 个桶，其中 0 值桶必须被丢弃：
	//   inst:317 → 11-02(1746)、11-04(243)       [11-01 与 11-03 是 0]
	//   inst:318 → 11-01(2880)、11-04(890)       [11-02 与 11-03 是 0]
	// 若把 0 值桶也收进来，分区目录会凭空多出「那天有数据」的记录 ⇒ 用户查到必然为空的 range。
	require.Len(t, got, 4, "只保留 weight>0 的桶，实际: %+v", got)

	want := map[string][]string{
		"inst:317/file": {"2026-11-02", "2026-11-04"},
		"inst:318/file": {"2026-11-01", "2026-11-04"},
	}
	for source, days := range want {
		var gotDays []string
		for _, f := range got {
			if f.LogSourceID == source {
				gotDays = append(gotDays, f.UTCDay)
			}
		}
		require.Equal(t, days, gotDays, "源 %s 的天集合不符", source)
	}
}

// 桶时间必须按 **UTC** 取日。本机是 HKT(+8)，若误用本地时区，`2026-11-04T00:00:00Z`
// 会变成 `2026-11-04 08:00` 仍是同一天——但 `2026-11-03T16:00:00Z` 会变成 `2026-11-04`，
// 整表错位一天。这里用跨零点样本直接把口径钉死。
func TestParseHitsSourceDaysUsesUTCDayNotLocal(t *testing.T) {
	body := `{"hits":[{"fields":{"log_source_id":"inst:1/file"},
	  "timestamps":["2026-11-03T16:00:00Z","2026-11-04T23:59:59Z"],
	  "values":[10,20],"total":30}]}`
	got, err := parseHitsSourceDays([]byte(body))
	require.NoError(t, err)
	require.Len(t, got, 2)

	// 2026-11-03T16:00:00Z 的 UTC 日是 11-03（HKT 才是 11-04）。
	require.Equal(t, "2026-11-03", got[0].UTCDay,
		"桶起点必须按 UTC 取日；按本机时区会整体错一天")
	require.Equal(t, "2026-11-04", got[1].UTCDay)
}

// 无分组值的 series（聚合总数）不能进分区目录——它没有源身份。
func TestParseHitsSourceDaysSkipsSeriesWithoutSource(t *testing.T) {
	body := `{"hits":[{"fields":{},"timestamps":["2026-11-04T00:00:00Z"],"values":[5276],"total":5276}]}`
	got, err := parseHitsSourceDays([]byte(body))
	require.NoError(t, err)
	require.Empty(t, got, "未按源分组的 series 不得进入分区目录")
}

// timestamps 比 values 长（或反之）时不得越界 panic。
func TestParseHitsSourceDaysToleratesMismatchedLengths(t *testing.T) {
	body := `{"hits":[{"fields":{"log_source_id":"inst:1/file"},
	  "timestamps":["2026-11-04T00:00:00Z","2026-11-05T00:00:00Z"],
	  "values":[7],"total":7}]}`
	got, err := parseHitsSourceDays([]byte(body))
	require.NoError(t, err)
	require.Len(t, got, 1, "按较短者处理，不得越界")
	require.Equal(t, "2026-11-04", got[0].UTCDay)
}

// 解析结果必须**确定有序**：否则同一份数据两次重建会产出不同顺序的分区写入序列，
// 让「同一输入渲染稳定」这类对照测试与日志排查都失真。
func TestParseHitsSourceDaysIsDeterministicallySorted(t *testing.T) {
	reversed := `{"hits":[
	 {"fields":{"log_source_id":"inst:9/file"},"timestamps":["2026-11-04T00:00:00Z","2026-11-02T00:00:00Z"],"values":[1,2],"total":3},
	 {"fields":{"log_source_id":"inst:1/file"},"timestamps":["2026-11-04T00:00:00Z"],"values":[1],"total":1}
	]}`
	got, err := parseHitsSourceDays([]byte(reversed))
	require.NoError(t, err)
	require.Equal(t, []string{"inst:1/file", "inst:9/file", "inst:9/file"}, []string{got[0].LogSourceID, got[1].LogSourceID, got[2].LogSourceID})
	require.Equal(t, "2026-11-02", got[1].UTCDay, "同源内按天升序")
	require.Equal(t, "2026-11-04", got[2].UTCDay)
}

// 非法 JSON 必须报错而不是返回空集合：空集合会被上层读成「VL 里一条数据都没有」，
// 进而把整个分区目录清空——这比一次失败严重得多。
func TestParseHitsSourceDaysRejectsMalformed(t *testing.T) {
	_, err := parseHitsSourceDays([]byte(strings.Repeat("not json", 3)))
	require.Error(t, err)
}
