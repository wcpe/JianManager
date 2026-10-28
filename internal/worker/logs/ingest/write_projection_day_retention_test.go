package ingest

// writeProjectionDay 的热层保留期过滤回归（2026-10-28 生产停采事件）。
//
// ## 现场
//
// 19 个源里 10 个停采，`reclaim_pos=0` 从 16:48 起一直没动过，backlog 撞 20000 上限。
//
// ## 真因（逐环实验确认，不是推测）
//
//  1. VL hot 以 `-retentionPeriod=30d` 运行。对**早于 now-30d** 的数据点，
//     VL 返回 **HTTP 200 但立刻丢弃**（实测：写 _time=2026-09-20 的记录 →
//     HTTP 200，随后查该窗口 → 0 行）。这是 VL 的既定行为，不是 bug。
//  2. 归档导入会产生 9/20 的事件（日志行：`archiveEvents:5781,
//     firstEventTime:2026-09-20T02:47:59Z`）。
//  3. 代码里已有「热层可发布窗口」防护（hot_retention_window.go，2026-10-21 加的），
//     但它只接在**整窗重发**（`deliver`）与**重建**（`rebuildDaysInner`）两条路径上：
//     生产日志证实它确实在工作（543 次「整窗重发跳过超出热层保留期」）。
//  4. **`writeProjectionDay` 没有接这道闸**。它只处理 1 个 UTC 天，直接写 VL。
//     归档导入的 9/20 批次走的就是它。
//  5. 于是：写 → 200 假成功 → 5.6s 后投影校验（9/20 窗口）查 0 行 → 超时 →
//     `PublicationPending` 永不清 → 重建重试新建代次（inst:327 已有 370 个代次、
//     编号到 2644）→ delivery 不动 → reclaim 不动 → WAL 撞上限 → 采集暂停。
//
// ## 本文件钉住的性质
//
// 整天早于 `now - hotRetention` 的 UTC 天，`writeProjectionDay` **不得向 VL 发任何写入请求**。
// 不写、不校验、如实记日志（不静默丢）——与 `deliver` / `rebuildDaysInner` 一致。

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// TestWriteProjectionDaySkipsDayBeyondHotRetention 是本修复的正例。
//
// 构造：hotRetention = 30d，写入面是一个 45 天前完成的 UTC 天。
// 期望：一次 VL 写请求都不发（数据写进去也只会被静默丢弃，白白占用一条代次）。
func TestWriteProjectionDaySkipsDayBeyondHotRetention(t *testing.T) {
	var inserts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/insert/jsonline" {
			atomic.AddInt32(&inserts, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	journal := catalog.NewMemJournal()
	m, err := New(Options{
		Root: t.TempDir(), VL: client, Catalog: catalog.New(journal), Journal: journal,
		// 30 天保留期：与生产 hot 实例一致。
		HotRetention: 30 * 24 * time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	// 45 天前的那一天，整天都早于 now-30d ⇒ 热层不可能可见。
	beyondDay := time.Now().UTC().AddDate(0, 0, -45).Format("2006-01-02")
	source := SourceConfig{
		LogSourceID: "inst:probe/file", SourceGeneration: "g1",
		StorageNamespace: "inst:probe", UTCDay: beyondDay,
	}
	events := []logtypes.Event{logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "inst:probe/file", SourceGeneration: "g1", ParserVersion: "v1"},
		logtypes.RecordRange{Start: 0, End: 9},
		beyondDay+"T02:00:00Z", beyondDay+"T02:00:01Z", "INFO", "stdout", "old line",
	)}

	_, err = m.writeProjectionDay(source, events, events, "projection-probe", nil, false, false, func() {})

	// 允许返回错误（那批注定不可发布），但**绝不**能发出写入请求。
	require.Zero(t, atomic.LoadInt32(&inserts),
		"整天早于 now-hotRetention 的批次不得写入 VL（写进去也会被静默丢弃），实际发出 %d 次写入", inserts)
	_ = err
}

// TestWriteProjectionDayWritesDayInsideHotRetention 是反向对照：
// 窗口内的天必须照常写入，否则本修复会把正常数据也挡掉。
func TestWriteProjectionDayWritesDayInsideHotRetention(t *testing.T) {
	var inserts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/insert/jsonline" {
			atomic.AddInt32(&inserts, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	journal := catalog.NewMemJournal()
	m, err := New(Options{
		Root: t.TempDir(), VL: client, Catalog: catalog.New(journal), Journal: journal,
		HotRetention: 30 * 24 * time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	// 1 天前 ⇒ 窗口内。
	insideDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	source := SourceConfig{
		LogSourceID: "inst:probe/file", SourceGeneration: "g1",
		StorageNamespace: "inst:probe", UTCDay: insideDay,
	}
	events := []logtypes.Event{logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "inst:probe/file", SourceGeneration: "g1", ParserVersion: "v1"},
		logtypes.RecordRange{Start: 0, End: 9},
		insideDay+"T02:00:00Z", insideDay+"T02:00:01Z", "INFO", "stdout", "fresh line",
	)}

	_, _ = m.writeProjectionDay(source, events, events, "projection-probe", nil, false, false, func() {})

	require.Greater(t, atomic.LoadInt32(&inserts), int32(0),
		"窗口内的天必须照常写入（本修复不得误伤）")
}
