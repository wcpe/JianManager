package ingest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 回归：投影校验必须**分簇**查询，单次查询覆盖的事件数有上限。
//
// 事故对照（2026-09-28 生产）：恢复期一次性查询整批积压（实测 limit=747822）→ VL 返回 400
// `cannot calculate [sort by (_time) desc limit 747822], since it requires more than 51MB of memory`
// → 校验被判「永久失败」→「日志采集运行时创建失败」→ 采集静默停摆。
//
// FR-498 P0 起分簇粒度是 verifyChunkEvents（默认 2000，与写入批上限 insertBatchMaxEvents
// **解耦**：写入批上限保护单次插入请求体积，校验成本由窗口内返回行数决定）。本用例用显式
// 小限额驱动真实限额路径：超过上限的批必须被切成多次查询——把限额调大到 ≥ 整批 /
// 取消分簇即红。
func TestVerifyProjectionChunksBoundQuerySize(t *testing.T) {
	const total = 1200
	const chunkEvents = 500

	var mu sync.Mutex
	maxLimit, queries := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 校验查询是 GET /select/logsql/query?query=…（FR-498 起不再传 limit：截断会让本簇
		// 记录凑不齐而被误判「不可见」，见 verify_no_limit_test.go）。
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
			mu.Lock()
			if n > maxLimit {
				maxLimit = n
			}
			mu.Unlock()
		}
		mu.Lock()
		queries++
		mu.Unlock()
		// 空结果：校验必然不完整，这里只关心「分簇是否生效」。
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "")
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

	// 缩短重试窗口，避免测试等待默认超时。
	m.verificationTimeout = 300 * time.Millisecond
	m.verifyBackoffMin = 10 * time.Millisecond
	m.verifyBackoffMax = 20 * time.Millisecond
	m.verifyChunkEvents = chunkEvents

	events := make([]logtypes.Event, 0, total)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < total; i++ {
		events = append(events, logtypes.Event{
			EventID:      "evt-" + strconv.Itoa(i),
			Message:      "line " + strconv.Itoa(i),
			EventTimeUTC: base.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
		})
	}

	// 空结果下校验必然失败——此处只断言分簇（查询次数）与查询参数。
	err = m.verifyProjection(client, SourceConfig{LogSourceID: "inst:9/file", SourceGeneration: "g1", StorageNamespace: "inst:9"}, "g1", events)
	require.Error(t, err)

	mu.Lock()
	defer mu.Unlock()
	// 断言用字面量（而非常量本身），否则「把上限调大 = 取消分簇」的变异不会被发现。
	require.GreaterOrEqual(t, queries, 3, "1200 条按每簇 500 条应至少发起 3 次校验查询")
	require.LessOrEqual(t, maxLimit, 501, "校验查询不得用 limit 截断（截断会误判「不可见」）")
}
