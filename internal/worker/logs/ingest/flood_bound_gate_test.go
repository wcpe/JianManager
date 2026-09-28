package ingest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 门禁：单源洪流 + 投递持续失败时，采集必须被**定界**——源被暂停，且持久化状态文件保持有界。
//
// 事故对照（2026-09-28 生产）：某源投递/校验长时间失败 → reclaim 永不推进 → WAL 条目（每条
// 内联完整正文）无界累积，单源把 ingest.state.json 撑到 1.2 GB，每次持久化全量重写 → 117 MB/s、
// Worker CPU 138%、整机 iowait 90%，并拖慢同机 CP 的 SQLite。本用例是该事故的最小复现：
// 投递端恒 500（事件进 WAL 但出不去），断言 B1b 的上限确实拦住膨胀。
//
// 阈值刻意留宽（断言 < 32 MiB）：修复前同样输入会产出 ≳100 MiB 的状态文件，故该断言
// 在缺陷存在时必然是红的，同时不受计数器/批次的细微差异影响。
func TestFloodWithFailingDeliveryPausesSourceAndKeepsStateBounded(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")

	var buf strings.Builder
	const lines = 100000
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&buf, "[12:00:%02d] [Server thread/INFO]: flood line %d %s\n", i%60, i, strings.Repeat("x", 40))
	}
	require.NoError(t, os.WriteFile(path, []byte(buf.String()), 0o644))

	// 投递恒失败：模拟 VL 长时间不可用/校验不通过。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	source := SourceConfig{
		LogSourceID: "inst:9/file", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:9",
		UTCDay: time.Now().UTC().Format("2006-01-02"),
	}
	m, err := New(Options{Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	key := source.LogSourceID + "/" + source.SourceGeneration
	p := m.pipes[key]
	require.NotNil(t, p, "登记源后应存在对应 pipeline")

	// 反复推进周期，直到源被暂停（上限遍历次数仅用于防止死循环）。
	require.Eventually(t, func() bool {
		m.pollOnce()
		return p.Ledger().Get(p.Key()).AcquirePaused
	}, 30*time.Second, 5*time.Millisecond, "洪流应触发采集暂停（B1b）")

	ent := p.Ledger().Get(p.Key())
	require.True(t, strings.HasPrefix(ent.PauseReason, "wal backlog limit exceeded"),
		"暂停须由积压上限触发：%q", ent.PauseReason)

	walLen := len(p.WAL().Snapshot())
	require.LessOrEqual(t, walLen, 6000, "WAL 积压条目数应被上限拦住（默认 5000 + 单批余量），实测 %d", walLen)

	// 持久化后状态文件必须有界——这正是事故中膨胀到 1.2 GB 的那个文件。
	require.NoError(t, m.persist())
	info, err := os.Stat(filepath.Join(root, "var", "log", "ingest.state.json"))
	require.NoError(t, err)
	require.Less(t, info.Size(), int64(32<<20),
		"状态文件须保持有界（修复前同输入 ≳100 MiB，事故中曾达 1.2 GB），实测 %d 字节", info.Size())
}
