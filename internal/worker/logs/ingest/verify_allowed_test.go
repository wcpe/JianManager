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

// 回归：分批校验必须接受「同批其他分片」的记录。
//
// 事故对照（2026-09-28 生产）：把校验按索引分批后，各分片的时间范围重叠（真实事件常共享
// 时间戳），第 N 片的查询会返回其他分片的事件；若只以本片为期望集，就会被判成
// `unexpected or duplicate projection event_id` → 运行时创建失败 → 采集停摆。
// 本用例让假 VL 对每个查询都返回时间窗内的**全部**事件（必然跨片），断言校验仍然通过。
func TestVerifyProjectionAcceptsOtherChunksOfSameBatch(t *testing.T) {
	const total = 1200
	base := time.Now().UTC().Add(-time.Hour)

	events := make([]logtypes.Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, logtypes.Event{
			EventID:      "evt-" + strconv.Itoa(i),
			Message:      "line " + strconv.Itoa(i),
			EventTimeUTC: base.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
		})
	}

	// 假 VL：按查询的时间窗返回全部命中事件（因此必然包含其他分片的记录）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, errStart := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
		end, errEnd := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
		if errStart != nil || errEnd != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, e := range events {
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

	source := SourceConfig{LogSourceID: "inst:9/file", SourceGeneration: "g1", StorageNamespace: "inst:9"}
	require.NoError(t, m.verifyProjection(client, source, "g1", events),
		"同批其他分片的记录不得被判为 unexpected")
}
