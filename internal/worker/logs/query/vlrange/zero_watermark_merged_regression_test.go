package vlrange

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 回归（2026-11-05，第四个同根缺陷）：零水位在**合并路径**被当成零上界。
//
// # 三处判据必须逐字一致
//
//	cvs=0 表示「没有封窗信息」，不得解释成「上界为 0」。已经修好两处：
//	  A. 单 range 行级  client.go  `q.ClosedVisibleSeq == 0 || ev.Record.End <= q.ClosedVisibleSeq`
//	  B. LogsQL selector client.go  `if q.ClosedVisibleSeq > 0 { record_end<=... }`
//	但合并粗筛的内存归因仍是修复前的旧条件 `!scoped || cvs > 0`，
//	于是 unscoped（简化投递路径补建的记录）+ cvs=0 时**每一条行都被丢弃**。
//
// # 后果与前三起事故同一形态
//
// 同一份数据，仅因「多了一个 UTC 天分区」而走进合并组，就从 N 行变成 0 行，
// 且下游看到的是 Exhausted=true / quality=exact ⇒ 报 complete=true。
func TestMergedNamespaceScopeZeroWatermarkKeepsRows(t *testing.T) {
	// ensureCatalogPartition 补建记录的实际形态：Projection 非 nil，
	// 但没有 SourceProjections / CoveredSourceGenerations / ClosedVisibleSeq。
	rng := query.AuthoritativeRange{
		TargetID:         "inst:320/2026-11-02",
		Key:              catalog.PartitionKey{StorageNamespace: "inst:320", UTCDay: "2026-11-02"},
		Owner:            catalog.OwnerHot,
		ClosedVisibleSeq: 0,
		Projection:       &catalog.PublishedProjection{QueryLocationDirID: "hot-1", QueryGeneration: 1},
	}
	ev := logtypes.Event{
		EventID:      "e1",
		EventTimeUTC: "2026-11-02T12:00:00Z",
	}
	ev.Source.LogSourceID = "inst:320/file"
	ev.Record.Start, ev.Record.End = 1, 194

	require.True(t, rangeMatchesEvent(rng, ev),
		"命名空间形态 + 零水位必须与单 range 路径同口径（client.go 的 cvs==0 分支）："+
			"否则合并组会把整页行全部丢弃并报成「完整无数据」")
}

// 反向守卫：cvs>0 时上界语义一个字都不能放宽。
func TestMergedZeroWatermarkFixKeepsNonZeroBound(t *testing.T) {
	rng := query.AuthoritativeRange{
		TargetID:         "inst:320/2026-11-02",
		Key:              catalog.PartitionKey{StorageNamespace: "inst:320", UTCDay: "2026-11-02"},
		Owner:            catalog.OwnerHot,
		ClosedVisibleSeq: 100,
		Projection:       &catalog.PublishedProjection{QueryLocationDirID: "hot-1", QueryGeneration: 1},
	}
	ev := logtypes.Event{EventID: "e2", EventTimeUTC: "2026-11-02T12:00:00Z"}
	ev.Source.LogSourceID = "inst:320/file"
	ev.Record.Start, ev.Record.End = 1, 101

	require.False(t, rangeMatchesEvent(rng, ev),
		"cvs>0 是真实封窗语义，越过水位的行必须继续被挡住")

	ev.Record.End = 100
	require.True(t, rangeMatchesEvent(rng, ev), "恰好等于水位的行必须可见")
}
