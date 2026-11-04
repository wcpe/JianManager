package vlrange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// TestSelectorDoesNotFilterOutEverythingWhenProjectionHasNoScopes 钉住一个曾经
// 让**生产日志查询整页为空**的缺陷（2026-11-05 真机复现）。
//
// # 故障形态
//
// 简化投递路径后，Catalog 分区记录由 `ingest.ensureCatalogPartition` 补建，它只填
// `QueryLocationDirID` / `QueryGeneration`，**刻不填** `SourceProjections`、
// `ClosedVisibleSeq`（理由见该函数注释：“简化后一批数据只写一次、行上也没有代次标签”）。
//
// 而 `selectorQuery` 用 `scoped := len(Projection.SourceProjections) > 0` 分流：
// 少一个 `SourceProjections` ⇒ `scoped=false` ⇒ 走 `!scoped` 分支，无条件追加
// `record_end:<=ClosedVisibleSeq`。而该记录里 `ClosedVisibleSeq` 恒为 0，
// 于是谓词变成 `record_end:<=0` —— **VL 里每一条都被过滤掉**。
//
// # 真机证据
//
// 同一个源在 VictoriaLogs 直查：
//
//	加  record_end:<=0      -> 0 条
//	去掉 record_end 条件  -> 194 条
//
// 接口却返回 `coverage.complete=true` / `enumeration_state=EXHAUSTED` /
// `quality=exact` —— **空结果被报成“完整且无数据”**，比报错更难排查。
//
// # 本用例断言什么
//
// 当 Catalog 记录没有限定范围（`SourceProjections` 为空）且水位为 0 时，
// selector **不得**内建任何 `record_end` 上界——那种记录的含义是
// “整体可查”（旧实现靠 `!scoped` 自由走 storage_namespace 分支，正是这个语义）。
func TestSelectorDoesNotFilterOutEverythingWhenProjectionHasNoScopes(t *testing.T) {
	// 与 ensureCatalogPartition 实际写入的形状一致：只有位置与代次，没有 SourceProjections，
	// 也没有 ClosedVisibleSeq。
	rng := query.AuthoritativeRange{
		Key: queryKey(),
		Projection: &catalog.PublishedProjection{
			QueryLocationDirID: "dir-1",
			QueryGeneration:    1,
		},
	}
	selector := selectorQuery(rng, query.RangeQuery{ClosedVisibleSeq: 0})

	require.NotContains(t, selector, "record_end:<=",
		"无范围投影 + 水位 0 时不得内建 record_end 上界：那会把整个分区过滤成 0 行，"+
			"且被报成 complete=true（真机实测：加 record_end:<=0 → 0 条，去掉 → 194 条）。selector=%s", selector)
}

// TestSelectorStillBoundsWhenClosedVisibleSeqIsSet 守住反面：
// 水位**明确大于 0** 时上界必须还在（那是“已封窗”的真实语义，不得为了让上面那条过关而删掉）。
func TestSelectorStillBoundsWhenClosedVisibleSeqIsSet(t *testing.T) {
	rng := query.AuthoritativeRange{
		Key: queryKey(),
		Projection: &catalog.PublishedProjection{
			QueryLocationDirID: "dir-1",
			QueryGeneration:    1,
		},
	}
	selector := selectorQuery(rng, query.RangeQuery{ClosedVisibleSeq: 42})
	require.Contains(t, selector, "record_end:<=42")
}

// TestSelectorEndToEndReturnsRowsForScopeLessProjection 确认缺陷在真实 HTTP 适配上也会
// 吃行（不只是字符串匹配）：服务端按 record_end 过滤，返回空。
func TestSelectorEndToEndReturnsRowsForScopeLessProjection(t *testing.T) {
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		// 模拟 VL 对 `record_end:<=0` 的行为：一行都不返回。
		if containsRecordEndZero(seenQuery) {
			return
		}
		_, _ = w.Write([]byte(`{"_time":"2026-09-22T12:00:00Z","_msg":"hello","event_id":"e1","log_source_id":"src-1","source_generation":"g1","record_start":"37482","record_end":"37832","level":"INFO","stream":"stdout","canonical_content_hash":"h1"}` + "\n"))
	}))
	defer srv.Close()

	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, AllowNonLoopback: true})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	res, err := client.Search(context.Background(), query.AuthoritativeRange{
		Key: queryKey(),
		Projection: &catalog.PublishedProjection{
			QueryLocationDirID: "dir-1",
			QueryGeneration:    1,
		},
	}, query.RangeQuery{
		TimeRange:        query.TimeRange{FromUTC: "2026-09-22T00:00:00Z", ToUTC: "2026-09-23T00:00:00Z"},
		Budget:           query.Budget{Limit: 10, MaxBytes: 1 << 20},
		ClosedVisibleSeq: 0,
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1, "无范围投影的分区必须能查到行（selector=%s）", seenQuery)
}

func containsRecordEndZero(selector string) bool {
	return strings.Contains(selector, "record_end:<=0")
}
