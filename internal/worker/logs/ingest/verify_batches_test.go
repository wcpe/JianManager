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

// 回归：投影校验必须**分批**查询，单次查询的记录上限不得超出批量上限。
//
// 事故对照（2026-09-28 生产）：恢复期一次性查询整批积压（实测 limit=747822）→ VL 返回 400
// `cannot calculate [sort by (_time) desc limit 747822], since it requires more than 51MB of memory`
// → 校验被判「永久失败」→ 「日志采集运行时创建失败」→ 采集静默停摆。
// 本用例断言每批查询的上限都有界；修复前同样输入会观察到 1200（=整批）。
func TestVerifyProjectionBoundsQueryLimit(t *testing.T) {
	const total = 1200

	var mu sync.Mutex
	maxLimit, queries := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 校验查询是 GET /select/logsql/query?query=…&limit=N+1，limit 是独立参数。
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
		// 空结果：校验必然不完整，这里只关心「单次查询上限是否有界」。
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

	events := make([]logtypes.Event, 0, total)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < total; i++ {
		events = append(events, logtypes.Event{
			EventID:      "evt-" + strconv.Itoa(i),
			Message:      "line " + strconv.Itoa(i),
			EventTimeUTC: base.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
		})
	}

	// 空结果下校验必然失败——此处只断言查询上限与批次数。
	err = m.verifyProjection(client, SourceConfig{LogSourceID: "inst:9/file", SourceGeneration: "g1", StorageNamespace: "inst:9"}, "g1", events)
	require.Error(t, err)

	mu.Lock()
	defer mu.Unlock()
	// 断言用字面量（而非常量本身），否则「把上限调大 = 取消分批」的变异不会被发现。
	require.LessOrEqual(t, maxLimit, 501, "单次校验查询的记录上限不得超过 501（每批 500 条 + 1 用于探测溢出）")
	require.GreaterOrEqual(t, queries, 3, "1200 条按每批 500 应至少发起 3 次校验查询")
}
