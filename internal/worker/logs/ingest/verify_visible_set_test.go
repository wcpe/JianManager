package ingest

// 回归：`visibleEventsInVL` 批不齐时**不得逐条重查**。
//
// 事故对照（2026-11-02 真机）：
//
//	批次平均 67 条 ⇒ 本应 1 次查询，实际花 67 次
//	实测总查询 290,727，其中 events=1 占 285,896（98.3%）
//
// 根因：`verifyProjectionQueryInWindow` 已算出 `seen` 集合，但只交一个 bool。
// 调用方拿不到集合 ⇒ 只能逐条重查。而外层硬超时仅 3s，512 条回退必然超时
// ⇒ 本批 0 条可见 ⇒ 缺口 0 条消解（生产 10-29：171 源被暂停、1295 源阻塞）。
//
// 本用例直接驱动 `visibleEventsInVL` 并**数查询次数**：
// 一批 100 条只有 40 条可见时，必须恰好 1 次查询。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestVisibleEventsInVLDoesNotFallBackPerEvent(t *testing.T) {
	const total = 100
	const visible = 40
	base := time.Now().UTC().Add(-time.Hour)

	events := make([]logtypes.Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, logtypes.Event{
			EventID:      "vset-" + strconv.Itoa(i),
			Message:      "line " + strconv.Itoa(i),
			EventTimeUTC: base.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
			Level:        "INFO",
			Stream:       "stdout",
		})
	}

	var queries atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		// 不管问什么，永远只返回前 visible 条（模拟“批不齐”）。
		for i := 0; i < visible; i++ {
			e := events[i]
			fmt.Fprintf(w, "{\"event_id\":%q,\"_time\":%q,\"_msg\":%q,\"level\":%q,\"stream\":%q}\n",
				e.EventID, e.EventTimeUTC, e.Message, e.Level, e.Stream)
		}
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	src := SourceConfig{LogSourceID: "node:vset", SourceGeneration: "g1", StorageNamespace: "ns:vset"}
	src.UTCDay = base.UTC().Format("2006-01-02")
	m := &Manager{}

	got, err := m.visibleEventsInVL(context.Background(), client, src, []string{"g1"}, events)
	require.NoError(t, err)

	// 可见集合必须准确。
	require.Len(t, got, visible, "必须恰好认出 %d 条可见", visible)

	// 关键断言：**1 次查询**。
	// 旧实现会在这里变成 1 + 100 次（每批 1 次 + 逐条 100 次）。
	require.EqualValues(t, 1, queries.Load(),
		"批不齐也必须只查 1 次；逐条重查就是 2026-11-02 事故的形态（实测占 98.3%%）")
}
