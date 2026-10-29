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
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 门禁：单源洪流 + 投递持续失败时，采集/回收必须被**定界**——队列被裁到上限内（最旧优先丢），
// 且持久化状态文件保持有界。
//
// 事故对照（2026-09-28 生产）：某源投递/校验长时间失败 → reclaim 永不推进 → WAL 条目（每条
// 内联完整正文）无界累积，单源把 ingest.state.json 撑到 1.2 GB，每次持久化全量重写 → 117 MB/s、
// Worker CPU 138%、整机 iowait 90%，并拖慢同机 CP 的 SQLite。本用例是该事故的最小复现。
//
// 2026-10-29 用户裁定后，定界手段从「暂停采集（不丢数据）」改为「丢弃最旧、不暂停、不背压」
// （对齐 Fluent Bit `memrb`）。所以本用例守的**核心不变量不变**（“内存/落盘状态必须恒有界”），
// 变的只是实现形态：断言从「源被暂停」改为「队列被裁到上限内 + 丢弃可观测且不暂停」。
// 反过来，若有人把丢弃退回成“什么都不做”（既不暂停也不丢），状态就会重新无界——
// 那才是这个事故真正要拦住的东西。
//
// 阈值刻意留宽（断言 < 32 MiB）：修复前同样输入会产出 ≳100 MiB 的状态文件，故该断言
// 在缺陷存在时必然是红的，同时不受计数器/批次的细微差异影响。
//
// **用例名保留（含 "PausesSource"）**：它是 2026-09-28 事故修复时的名字，已被 CI 分片清单
// 与历史验收记录引用；改名会让“这条用例还在不在”的可追溯性变差。语义以注释与断言为准。
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

	// 反复推进周期，直到积压确实被裁（上限遍历次数仅用于防止死循环）。
	//
	// 2026-10-29 起越界不再暂停采集，所以“定界生效”的读数是**队列被裁**：条目数落到上限内、
	// 且 DropStats 已经长大（真丢了——不是“恰好没再涨”）。
	require.Eventually(t, func() bool {
		m.pollOnce()
		dropped, _ := p.WAL().DropStats()
		return dropped > 0
	}, 30*time.Second, 5*time.Millisecond, "洪流必须触发丢弃（B1b 定界）")

	ent := p.Ledger().Get(p.Key())
	require.False(t, ent.AcquirePaused,
		"处置洪流不得暂停采集（2026-10-29 用户裁定：不背压）")
	require.Empty(t, ent.PauseReason, "不得再产生积压暂停原因")

	// 队列必须卡在生效上限内（默认 2000 条 + 单批余量；留宽容同理见下）。
	walLen := len(p.WAL().Snapshot())
	require.LessOrEqual(t, walLen, 6000, "WAL 积压条目数应被上限拦住，实测 %d", walLen)

	// 丢弃必须可观测（对齐 Fluent Bit memrb_dropped_chunks/dropped_bytes）：
	// 不背压就必须有人在数据被丢时看得到，否则运维只看到“日志少了”却分不清是丢还是源没写。
	if n, b := p.WAL().DropStats(); n <= 0 || b <= 0 {
		t.Fatalf("洪流丢弃必须同时计入条目数与字节数，实测 entries=%d bytes=%d", n, b)
	}
	// 丢弃必须在账本里留下可查的凭据，且不得被自动消解（真丢不回来的字节）。
	droppedGaps := 0
	for _, g := range ent.Gaps {
		if g.Reason == ledger.GapReasonWALBacklogDropped {
			droppedGaps++
			require.False(t, g.Resolved, "丢弃类缺口不得被自动消解")
		}
	}
	require.Positive(t, droppedGaps, "丢弃必须登记 WAL_BACKLOG_DROPPED 凭据（不静默）")

	// 持久化后索引库（含 WAL 伴生文件）必须有界——这正是事故中膨胀到 1.2 GB 的那个状态。
	require.NoError(t, m.persist())
	var total int64
	for _, name := range []string{"ingest.index.db", "ingest.index.db-wal", "ingest.index.db-shm"} {
		if info, err := os.Stat(filepath.Join(root, "var", "log", name)); err == nil {
			total += info.Size()
		}
	}
	require.Less(t, total, int64(32<<20),
		"状态索引须保持有界（修复前同输入 ≳100 MiB，事故中曾达 1.2 GB），实测 %d 字节", total)
}
