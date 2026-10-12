package ingest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 回归：校验查询的时间窗必须夹在本源的 UTC 日内。
//
// 事故对照（2026-09-30 生产，整节点离线）：09-28 批次首条事件在 00:00:00.022Z，
// 窗口下探 1 秒落到 09-27T23:59:59.022Z，把 09-27 的日界事件（23:59:59.126，
// 属于**另一天的写批**，不在本批 allowed 中）带进结果集 →
// `unexpected or duplicate projection event_id` → 运行时创建失败 → 节点离线。
// 本用例让假 VL 返回窗口内全部事件；窗口若不夹日，日界邻居事件必然入选 → 转红。
func TestVerifyProjectionClampsWindowToUTCDay(t *testing.T) {
	const day = "2026-09-28"
	// 本批：09-28 当天的事件，首条紧贴日界（00:00:00.022Z）。
	events := []logtypes.Event{
		{EventID: "evt-28-a", Message: "day start", EventTimeUTC: "2026-09-28T00:00:00.022Z"},
		{EventID: "evt-28-b", Message: "day mid", EventTimeUTC: "2026-09-28T12:00:00.000Z"},
	}
	// 邻居：09-27 的日界事件——只应出现在 09-27 自己的窗口里。
	neighbor := logtypes.Event{EventID: "evt-27-tail", Message: "prev day tail", EventTimeUTC: "2026-09-27T23:59:59.126Z"}

	// 假 VL：按查询时间窗返回全部命中事件（与真实 VL 的窗口语义一致）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, errStart := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
		end, errEnd := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
		if errStart != nil || errEnd != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		all := append(append([]logtypes.Event{}, events...), neighbor)
		for _, e := range all {
			when, err := time.Parse(time.RFC3339Nano, e.EventTimeUTC)
			if err != nil || when.Before(start) || when.After(end) {
				continue
			}
			fmt.Fprintf(w, "{\"event_id\":%q,\"_time\":%q,\"_msg\":%q}\n", e.EventID, e.EventTimeUTC, e.Message)
		}
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	journal := catalog.NewMemJournal()
	m, err := New(Options{Root: t.TempDir(), VL: client, Catalog: catalog.New(journal), Journal: journal})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	source := SourceConfig{LogSourceID: "inst:9/file", SourceGeneration: "g1", StorageNamespace: "inst:9", UTCDay: day}
	require.NoError(t, m.verifyProjection(client, source, "g1", events),
		"相邻日的日界事件不得因 ±1s 宽容窗被误判为 unexpected")
}
