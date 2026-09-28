package ingest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 回归：校验查询**不得**用 limit 截断返回，否则宽时间窗下本片记录会凑不齐。
//
// 事故对照（2026-09-28 生产，由参数日志定位）：分片按**索引**切，其时间跨度可能很宽
// （实测 500 条跨约 10 小时），窗口内还会有同批其他分片的记录；平台却传
// `limit = len(events)+1 = 501`，VL 按时间返回前 501 行 → 本片 500 条凑不齐 →
// 误判「not fully visible」→ 运行时创建失败 → 采集静默停摆。
// 本用例让假 VL 忠实模拟该截断：其他分片记录时间更早、排在前面。
func TestVerifyProjectionDoesNotTruncateByLimit(t *testing.T) {
	const chunkN, otherN = 500, 300
	base := time.Now().UTC().Add(-24 * time.Hour)

	mk := func(prefix string, n int, offset time.Duration) []logtypes.Event {
		out := make([]logtypes.Event, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, logtypes.Event{
				EventID:      prefix + strconv.Itoa(i),
				Message:      prefix + " " + strconv.Itoa(i),
				EventTimeUTC: base.Add(offset + time.Duration(i)*time.Millisecond).Format(time.RFC3339Nano),
				Source:       logtypes.SourceIdentity{LogSourceID: "inst:9/file", SourceGeneration: "g1"},
			})
		}
		return out
	}
	// 其他分片（同批、时间更早 → 在 VL 返回中排前面）
	others := mk("other-", otherN, 0)
	// 本批 = 其他分片 + 本片（本片时间更晚）
	batch := append(append([]logtypes.Event{}, others...), mk("chunk-", chunkN, 10*time.Hour)...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := -1
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		rows := batch // VL 按时间升序返回窗口内记录
		if limit >= 0 && limit < len(rows) {
			rows = rows[:limit] // 忠实模拟 limit 截断
		}
		for _, e := range rows {
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
	m.verificationTimeout = 300 * time.Millisecond
	m.verifyBackoffMin = 10 * time.Millisecond
	m.verifyBackoffMax = 20 * time.Millisecond

	source := SourceConfig{LogSourceID: "inst:9/file", SourceGeneration: "g1", StorageNamespace: "inst:9"}
	require.NoError(t, m.verifyProjection(client, source, "g1", batch),
		"宽时间窗下不得用 limit 截断，否则本片记录凑不齐会被误判为不可见")
}
