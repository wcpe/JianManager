package vlrange

// 合并路径的重复质量口径（2026-10 一致性修复）：
//
// 历史缺陷：merged.go 对「同 id 再次出现」无条件置 duplicated=true，于是**同 id 同哈希的
// 纯重复**（内容完全一致、会被去重丢弃）也会把质量降级为 DupUnresolved；而单 range 路径
// （client.go 的 searchPage）只在「同 id 异哈希」时降级。同一份数据走两条路径给出相反的
// 质量结论 ⇒ 导出侧门闸随路径抖动。
//
// 本文件的用例把两处判据钉在同一语义上：
//  1. 同 id 同哈希 = 纯重复：只有一条进入结果，质量**保持** DupExact；
//  2. 同 id 异哈希 = 歧义副本：两条都保留，质量降级 DupUnresolved。
//
// 转红方式（变异验证）：
//   - 把 merged.go 的 `duplicated = true` 移回同哈希分支（即恢复旧实现）
//     ⇒ TestMergedPureDuplicateKeepsExactQualityAcrossSegments 变红；
//   - 删掉 merged.go 的 `duplicated = true` ⇒
//     TestMergedConflictingHashDowngradesQualityAcrossSegments 变红；
//   - 删掉同哈希分支的 `continue` ⇒ 纯重复用例的行数断言变红。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// mergedDupRow 是喂给假 VL 的一行（只建模合并层关心的字段）。
type mergedDupRow struct {
	at      string
	eventID string
	hash    string
	msg     string
	source  string
}

// newMergedDupClient 造一个按窗口返回行的假 VL：**严格复刻 [start,end) 语义**，
// 行只在自身时刻所属的窗口内返回。这一点很关键——它意味着「同一行被两段同时返回」
// 在真实 VL 上不可达；用例里出现跨段同 id 同哈希的形态，是为了验证合并层的去重口径
// 本身（防守性契约），不代表声称生产上会这么返回。
func newMergedDupClient(t *testing.T, rows []mergedDupRow) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		start, err := time.Parse(time.RFC3339Nano, values.Get("start"))
		if err != nil {
			t.Errorf("窗口起点非法: %v", err)
			return
		}
		end, err := time.Parse(time.RFC3339Nano, values.Get("end"))
		if err != nil {
			t.Errorf("窗口终点非法: %v", err)
			return
		}
		type picked struct {
			at   time.Time
			line string
		}
		var hits []picked
		for _, row := range rows {
			at, err := time.Parse(time.RFC3339Nano, row.at)
			if err != nil {
				t.Errorf("夹具时刻非法: %v", err)
				return
			}
			if at.Before(start) || !at.Before(end) {
				continue
			}
			hits = append(hits, picked{at: at, line: fmt.Sprintf(
				`{"_time":%q,"_msg":%q,"event_id":%q,"log_source_id":%q,"source_generation":"g1",`+
					`"record_start":"1","record_end":"2","level":"INFO","stream":"stdout",`+
					`"canonical_content_hash":%q}`,
				row.at, row.msg, row.eventID, row.source, row.hash)})
		}
		// VL 的 sort by (_time desc) 语义。
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].at.After(hits[j].at) })
		w.Header().Set("Content-Type", "application/json")
		for _, hit := range hits {
			_, _ = w.Write([]byte(hit.line + "\n"))
		}
	}))
	t.Cleanup(srv.Close)

	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, AllowNonLoopback: true})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	return client
}

// mergedDupRanges 返回两个相距 83 天的 range（强制切段：窗口 84 天 > 31d 上限），
// 各自以 StorageNamespace 作来源约束，行按自身 UTC 天归因到对应 range。
func mergedDupRanges() []query.AuthoritativeRange {
	return []query.AuthoritativeRange{
		{Key: catalog.PartitionKey{StorageNamespace: "src-a", UTCDay: "2026-09-22"}, Owner: catalog.OwnerHot, ClosedVisibleSeq: 10},
		{Key: catalog.PartitionKey{StorageNamespace: "src-b", UTCDay: "2026-07-01"}, Owner: catalog.OwnerHot, ClosedVisibleSeq: 10},
	}
}

// 纯重复（同 id 同哈希）即便跨段到达也必须只保留一条且**不降级**质量。
//
// 为什么不能降级：重复行本身被 continue 丢弃，用户看到的结果无歧义；把它记成
// DupUnresolved 等于谎报「未对账」，会让导出侧门闸对正常重复数据误封。
func TestMergedPureDuplicateKeepsExactQualityAcrossSegments(t *testing.T) {
	client := newMergedDupClient(t, []mergedDupRow{
		// 较新段（src-a / 09-22）与较老段（src-b / 07-01）各一份，内容完全一致。
		{at: "2026-09-22T10:00:00Z", eventID: "dup", hash: "h-same", msg: "dup", source: "src-a"},
		{at: "2026-07-01T10:00:00Z", eventID: "dup", hash: "h-same", msg: "dup", source: "src-b"},
	})
	res, err := client.SearchMerged(context.Background(), mergedDupRanges(), query.RangeQuery{
		Budget: query.Budget{Limit: 100, MaxBytes: 1 << 20},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"dup"}, eventIDs(res.Items), "同 id 同哈希的重复副本必须被去重成一条")
	require.Equal(t, "dup", res.Items[0].Message)
	require.Equal(t, query.DupExact, res.DuplicateQuality,
		"纯重复不构成歧义，合并路径不得因此宣称 unresolved（与单 range 路径同口径）")
}

// 同 id 异哈希跨段到达：两条都保留（内容已分歧、无法判定真伪），质量必须降级。
func TestMergedConflictingHashDowngradesQualityAcrossSegments(t *testing.T) {
	client := newMergedDupClient(t, []mergedDupRow{
		{at: "2026-09-22T10:00:00Z", eventID: "conflict", hash: "h-new", msg: "newer", source: "src-a"},
		{at: "2026-07-01T10:00:00Z", eventID: "conflict", hash: "h-old", msg: "older", source: "src-b"},
	})
	res, err := client.SearchMerged(context.Background(), mergedDupRanges(), query.RangeQuery{
		Budget: query.Budget{Limit: 100, MaxBytes: 1 << 20},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"conflict", "conflict"}, eventIDs(res.Items),
		"歧义副本两条都要保留给上层归因，不得静默丢弃")
	require.Equal(t, "newer", res.Items[0].Message)
	require.Equal(t, "older", res.Items[1].Message)
	require.Equal(t, query.DupUnresolved, res.DuplicateQuality, "同 id 异哈希不得宣称 exact")
}

// 单段内（真实可达形态）的纯重复：searchPage 已按 id+哈希去重，合并层同样保持 exact。
// 这条用例守护的是「生产上真正会出现的 132 行冗余段」形态。
func TestMergedPureDuplicateInsideSingleSegmentKeepsExactQuality(t *testing.T) {
	client := newMergedDupClient(t, []mergedDupRow{
		{at: "2026-09-22T10:00:01Z", eventID: "dup", hash: "h-same", msg: "dup", source: "src-a"},
		{at: "2026-09-22T10:00:01Z", eventID: "dup", hash: "h-same", msg: "dup", source: "src-a"},
	})
	res, err := client.SearchMerged(context.Background(), []query.AuthoritativeRange{
		{Key: catalog.PartitionKey{StorageNamespace: "src-a", UTCDay: "2026-09-22"}, Owner: catalog.OwnerHot, ClosedVisibleSeq: 10},
	}, query.RangeQuery{
		TimeRange: query.TimeRange{FromUTC: "2026-09-22T00:00:00Z", ToUTC: "2026-09-23T00:00:00Z"},
		Budget:    query.Budget{Limit: 100, MaxBytes: 1 << 20},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1, "同 id 同哈希必须去重成一条")
	require.Equal(t, query.DupExact, res.DuplicateQuality, "纯重复不得降级质量")
}
