package ingest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 回归：校验查询必须以**事件自身的** source 标识过滤，而不是源配置里的。
//
// 事故对照（2026-09-28 生产，逐字段二分定位）：VL 里存的是 login-01 的
// `source_generation`，而平台查询用的是源配置里的 beacon-main 代号 → 查询恒 0 条 →
// 判「不可见」→ 运行时创建失败 → 采集停摆（且与超时/批量/窗口无关）。
// 本用例让假 VL 只认事件里的代号：若查询用了配置里的代号，就查不到 → 校验失败。
func TestVerifyProjectionFiltersByEventSourceNotConfig(t *testing.T) {
	const cfgGeneration = "instance:CONFIG@worker:w"
	const evtGeneration = "instance:EVENT@worker:w"

	base := time.Now().UTC().Add(-time.Hour)
	events := make([]logtypes.Event, 0, 2)
	for i := 0; i < 2; i++ {
		events = append(events, logtypes.Event{
			EventID:      "evt-" + strconv.Itoa(i),
			Message:      "line " + strconv.Itoa(i),
			EventTimeUTC: base.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
			Source:       logtypes.SourceIdentity{LogSourceID: "inst:142/file", SourceGeneration: evtGeneration},
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, `source_generation:="`+evtGeneration+`"`) {
			w.WriteHeader(http.StatusOK) // 空结果：配置代号查不到任何东西
			return
		}
		for _, e := range events {
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
	m.verificationTimeout = 200 * time.Millisecond
	m.verifyBackoffMin = 10 * time.Millisecond
	m.verifyBackoffMax = 20 * time.Millisecond

	// 源配置故意用另一个代号（模拟生产里的错配）。
	source := SourceConfig{LogSourceID: "inst:142/file", SourceGeneration: cfgGeneration, StorageNamespace: "inst:142"}
	require.NoError(t, m.verifyProjection(client, source, "g1", events),
		"校验必须按事件自身的 source_generation 过滤，否则生产里的配置错配会让校验永远失败")
}
