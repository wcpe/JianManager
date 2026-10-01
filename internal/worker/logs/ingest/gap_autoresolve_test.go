package ingest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// flakyVLFixture 给「投影写」加一个可切换的失败开关（读路径复用既有 fixture）。
//
// 为什么要真 HTTP 而不是打桩 deliver：缺陷 A 的现场是「VL 不可用期间投递持续失败」，
// 缺口由真实失败路径（writeProjectionDay → insertInBatches 非 2xx）产生；
// 只在 hook 层打桩会绕过缺口登记的真实条件。
type flakyVLFixture struct {
	inner      *projectionVLFixture
	failWrites atomic.Bool
}

func (f *flakyVLFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/insert/jsonline" && f.failWrites.Load() {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.inner.ServeHTTP(w, r)
}

func (f *flakyVLFixture) writeCount() int {
	f.inner.mu.Lock()
	defer f.inner.mu.Unlock()
	return len(f.inner.writes)
}

func newFlakyVL(t *testing.T) (*vlsup.Client, *flakyVLFixture) {
	t.Helper()
	fixture := &flakyVLFixture{inner: &projectionVLFixture{}}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture
}

func appendLogLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	_, err = f.WriteString(line)
	require.NoError(t, err)
}

// TestDeliveryFailureGapsAutoResolveAfterDeliveryRecovers 是缺陷 A 的核心转红闸门：
// 投递持续失败 → 缺口有界 → 投递恢复 → 缺口**自动清零**（不再依赖人工按源解算）。
//
// 现场（2026-10-01/02）：缺口 50,863–50,864 条/源；投递恢复后缺口仍在（没有任何自动消解路径），
// 而 ResumeAcquire 要求零未解决缺口 → 采集焊死 13+ 小时，最后靠人工解算 13 个源。
//
// 转红：改动前本用例在第 3 步断言（缺口清零）处必红——缺口只能靠 ResolveCoveredGaps/人工路径消解，
// 而两者都不会在投递恢复时被自动触发。
func TestDeliveryFailureGapsAutoResolveAfterDeliveryRecovers(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	// 两行起步：normalize 以「下一条事件头」闭合多行事件，单行只会停在缓冲里（不产生投递）。
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:gap/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:gap", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	pipe := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	require.NotNil(t, pipe)

	// 阶段一：VL 写持续失败。每次失败的批次区间相邻 → 缺口必须合并（有界）。
	fixture.failWrites.Store(true)
	m.pollOnce()
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()),
		"投递失败必须留下可见缺口（绝不静默丢弃）")
	for i := 0; i < 5; i++ {
		appendLogLine(t, logPath, fmt.Sprintf("[12:00:0%d] [Server thread/INFO]: retry batch\n", i+3))
		m.pollOnce()
	}
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry),
		"连续同因失败必须合并为一条覆盖区间（改动前这里是 6 条）")
	require.Greater(t, entry.ErrorCount, 1, "失败次数仍必须可观测")

	// 阶段二：VL 恢复。下一次成功投递（全量重放）即构成「已确认落库」证据。
	fixture.failWrites.Store(false)
	writesBefore := fixture.writeCount()
	appendLogLine(t, logPath, "[12:00:09] [Server thread/INFO]: recovered\n")
	m.pollOnce()
	require.Greater(t, fixture.writeCount(), writesBefore, "投递恢复后必须真正写入 VL")

	// 阶段三：断言缺口自动清零（且留有消解依据，不是被删除）。
	entry = pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.Zero(t, ledger.UnresolvedGapCountOf(entry),
		"投递恢复后缺口必须自动消解，不得再等人工")
	require.NotEmpty(t, entry.Gaps)
	require.True(t, entry.Gaps[0].Resolved)
	require.NotEmpty(t, entry.Gaps[0].Resolution, "消解依据必须可审计")
	// 就绪判据里的缺口阻塞必须随之消失（其它条目如投影覆盖不在本用例范围）。
	for _, reason := range m.CutoverReadiness().Reasons {
		require.NotContains(t, reason, ":unresolved_gaps")
	}
}

// TestPausedSourceSelfHealsEndToEnd 覆盖现场主路径：投递失败 + 积压越界 → 源暂停 →
// 投递恢复 → 存量外发 → 缺口自动消解 → 采集自动恢复（全程无人介入）。
//
// 现场（2026-10-01/02）：10 个源 paused=1、各带 5 万级未解决缺口、13+ 小时零新数据；
// 人工解算 13 个源后才恢复。暂停期间 FileTailer 不读新数据，若没有本用例覆盖的自愈链，
// 「投递 → 回收 → 恢复评估」在暂停源上没有任何触发点。
func TestPausedSourceSelfHealsEndToEnd(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:paused/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:paused", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 自愈默认按源限频 10 秒（避免每轮重投失败存量）；回归用真实路径驱动，
	// 因此把间隔压到「两次轮询之间必然过期」的量级，而不是 sleep 或改默认值。
	m.SetSelfHealInterval(time.Nanosecond)
	pipe := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	require.NotNil(t, pipe)

	// 阶段一：投递失败 + 积压越界 → 暂停。
	fixture.failWrites.Store(true)
	pipe.WAL().SetLimits(1, 0)
	m.pollOnce()
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()))
	appendLogLine(t, logPath, "[12:00:03] [Server thread/INFO]: third\n")
	m.pollOnce()
	entry := pipe.Ledger().Get(pipe.Key())
	require.True(t, entry.AcquirePaused, "积压越界后该源必须暂停（既有语义）")
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry), "相邻失败必须合并")

	// 阶段二：VL 恢复后，一次采集轮应当自愈（暂停期间 tailer 不读，只有自愈入口能推动）。
	fixture.failWrites.Store(false)
	m.pollOnce()
	entry = pipe.Ledger().Get(pipe.Key())
	require.Zero(t, ledger.UnresolvedGapCountOf(entry), "缺口必须自动消解")
	require.False(t, entry.AcquirePaused, "缺口清零 + 积压回落后采集必须自动恢复")
	require.Empty(t, entry.PauseReason)
}

// TestStdioRawWriteFailureGapIsNeverAutoResolved 守住语义红线：
// 「原始字节写入失败」无法由任何投影/重投证据证明已落库，自动路径必须放手，交人工确认。
//
// 转红：若自动消解不带原因排除，本用例会在断言处红（该缺口被错误地标记为已解决）。
func TestStdioRawWriteFailureGapIsNeverAutoResolved(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:raw/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:raw", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	pipe := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	require.NotNil(t, pipe)

	// 正常投递一轮：建立「已发布投影」这一全局证据。
	m.pollOnce()
	require.True(t, m.CutoverReadiness().LedgerReady, "%v", m.CutoverReadiness().Reasons)

	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, 20,
		ledger.GapReasonStdioRawWriteFailed, "raw bytes may never reach disk"))
	// 全局证据成立（该源已在已发布投影内），但该原因必须被排除。
	require.False(t, m.autoResolveGapsFromPublished(source, pipe),
		"不可证明的原因不得被自动消解")
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()))

	// 同一区间上的普通投递失败缺口则可以自动消解：证明排除是按原因而非按区间。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, 20, "DELIVER_ERROR", "transport down"))
	require.True(t, m.autoResolveGapsFromPublished(source, pipe))
	entry := pipe.Ledger().Get(pipe.Key())
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry))
	for _, gap := range entry.Gaps {
		if gap.Reason == ledger.GapReasonStdioRawWriteFailed {
			require.False(t, gap.Resolved)
		} else {
			require.True(t, gap.Resolved)
		}
	}
}
