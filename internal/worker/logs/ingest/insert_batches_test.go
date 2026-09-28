package ingest

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 回归：恢复期的大批量写入必须**分批**投递，不得拼成单个大请求。
//
// 事故对照（2026-09-28 生产）：vlsup 客户端是 http.Client{Timeout: 5 * time.Second}，
// 而恢复期把积压一次性 InsertJSONLines；状态文件达 1.2 GB 时单请求远超 5s →
// `vlsup: insert request: ... context deadline exceeded` → writeProjectionDay 失败 →
// 「日志采集运行时创建失败」→ 采集静默停摆（无重试、无告警；两次重启均复现）。
// 本用例断言超过上限的事件数被拆成多个请求，且单请求条数不超上限。
func TestInsertInBatchesSplitsLargePayload(t *testing.T) {
	const total = 1200

	var mu sync.Mutex
	requests, maxLines := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lines := 0
		sc := bufio.NewScanner(bytes.NewReader(body))
		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) > 0 {
				lines++
			}
		}
		mu.Lock()
		requests++
		if lines > maxLines {
			maxLines = lines
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	journal := catalog.NewMemJournal()
	m, err := New(Options{
		Root: t.TempDir(), VL: client, Catalog: catalog.New(journal), Journal: journal,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	events := make([]logtypes.Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, logtypes.Event{Message: fmt.Sprintf("line %d", i)})
	}

	status, err := m.insertInBatches(client, events, "g1")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 3, requests, "1200 条按每批 %d 应拆成 3 个请求", insertBatchMaxEvents)
	require.LessOrEqual(t, maxLines, insertBatchMaxEvents, "单请求条数不得超过上限")
}
