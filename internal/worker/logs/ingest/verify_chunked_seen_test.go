package ingest

// 回归：**分片分支**必须把子片可见集合合并后交回（2026-11-02 定论）。
//
// 事故形态（两处叠加，实测「缺口已消解」恒 0）：
//
//	// runtime.go:8899-8917
//	if end < len(events) {
//	    for start := 0; start < len(events); {
//	        ok, part, queryErr := m.verifyProjectionQueryInWindow(...)  // 薄包装，集合在这里丢掉
//	        complete = complete && ok
//	        start = end
//	    }
//	    return complete, nil, stats, nil     // 集合恒为 nil
//	}
//
// 调用方拿到 nil ⇒ visibleSet[id] 恒 false ⇒ 整批全判不可见。
//
// 为何之前没被发现：现有用例（100 条 × 短 ID）累计 selector 远低于
// scopedProjectionQueryMaxBytes = 12KB，**根本走不到分片分支**。
// 而生产 60 源常态批就会撞上（固定前缀本身已占数百字节）。
//
// 本用例用**长 EventID** 强制越过 12KB，确保至少切出 2 个子片。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestVisibleEventsInVLChunkedBranchMergesSeen(t *testing.T) {
	// 每条 ID 用 200 字节，60 条 ≈ 12KB ⇒ 叠加固定前缀后必被切成 ≥2 片。
	const total = 60
	const visible = 40
	pad := strings.Repeat("a", 200)
	base := time.Now().UTC().Add(-time.Hour)

	events := make([]logtypes.Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, logtypes.Event{
			EventID:      fmt.Sprintf("chunk-%03d-%s", i, pad),
			Message:      "line " + fmt.Sprint(i),
			EventTimeUTC: base.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
			Level:        "INFO",
			Stream:       "stdout",
		})
	}

	// 先确认这批真的会分片，否则本用例是在测错分支。
	src := SourceConfig{LogSourceID: "node:chunk", SourceGeneration: "g1", StorageNamespace: "ns:chunk"}
	src.UTCDay = base.UTC().Format("2006-01-02")
	scope := []string{"g1"}
	firstEnd, err := scopedProjectionChunkEnd(src, scope, events, 0)
	require.NoError(t, err)
	require.Less(t, firstEnd, total,
		"前置条件：本夹具必须能切出多子片（实际第一片 end=%d，总数=%d），否则测不到分片分支",
		firstEnd, total)

	var queries atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		// 只返回查询里 event_id:in(...) 包含、且属于“前 visible 条”的那些。
		// 为何要按 query 过滤：分片后每片只问到自己的 event_id，若不过滤会把
		// 其他片的事件也吐回去，被判 unexpected（真实 VL 不会这样）。
		q := r.URL.Query().Get("query")
		for i := 0; i < visible; i++ {
			e := events[i]
			if !strings.Contains(q, strconv.Quote(e.EventID)) {
				continue
			}
			fmt.Fprintf(w, "{\"event_id\":%q,\"_time\":%q,\"_msg\":%q,\"level\":%q,\"stream\":%q}\n",
				e.EventID, e.EventTimeUTC, e.Message, e.Level, e.Stream)
		}
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	m := &Manager{}

	got, err := m.visibleEventsInVL(context.Background(), client, src, events)
	require.NoError(t, err)

	// 核心断言：分片分支必须把子片查到的集合交回来。
	// 修复前这里是 0（集合被 nil 吞掉）。
	require.NotEmpty(t, got, "分片分支必须交回可见事件（修复前恒为 0，因为 return complete, nil, ...）")
	require.Len(t, got, visible, "必须恰好认出 %d 条可见", visible)

	for i := 0; i < visible; i++ {
		found := false
		for _, e := range got {
			if e.EventID == events[i].EventID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("可见事件 #%d 不在交回的集合里（分片后集合没被合并）", i)
		}
	}
}
