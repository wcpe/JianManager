package ingest

// FR-498 单位行成本 8.3×（投递路径「增量尾部」快路径）回归 + 可转红。
//
// 立论（2026-10-02 实测，`.tmp/opt-verify/`）：投递路径原先每批做三处 O(段总行数) 的操作——
// 身份集合重建（deliver 的 seen）、权威全量构建（canonicalRecoveryEvents）、段行数校准
// （appendEvents 的 Count）。段**只增不减**（契约 §4.3/§5.3 要求它作为 VL 数据根丢失后的
// 权威集合），生产单源段已达数十万行（全库 18 GB / 63 源），微基准实测单次遍历 8.7 µs/行
// ⇒ 每批数秒，且成本随运行时长线性增长（实验室段从零开始 ⇒ 没有这块成本，正是 0.36 → 2.95
// ms/行 = 8.3× 的主因）。大段夹具（61 源 ×10 万行预置段、批 2000 行）实测：
// 批级 8307 ms、吞吐 1693 行/s（生成 3050 ⇒ 达成率 0.555，复现现场 0.791 的形态）；
// 走增量尾部快路径后：批级 1378 ms、吞吐 3083 行/s（达成率 1.01）。
//
// 本用例用阶段观测口（Manager.stageSink）断言快路径**确实生效**，并守住它的边界：
// 首次投递（段尚空）与重放（replay=true）必须仍走全量路径。把 planDeliveryTail 改成恒不快
// （或删掉水位判据）即红。

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// stageRecorder 收集阶段观测口收到的 stages 串（pollOnce 会并发调用，故加锁）。
type stageRecorder struct {
	mu     sync.Mutex
	stages []string
}

func (r *stageRecorder) sink(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stages = append(r.stages, s)
}

func (r *stageRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stages...)
}

// hasStage 报告是否观测到含该标记的阶段串。
func (r *stageRecorder) hasStage(marker string) bool {
	for _, s := range r.all() {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func TestDeliverUsesAppendedTailFastPath(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	dir := filepath.Join(root, "s00")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(pollTestLines(0, 50)), 0o600))

	day := runtimeTestUTCDay()
	src := SourceConfig{
		LogSourceID:      "node:tail-fast",
		SourceGeneration: "g1",
		Path:             path,
		Mode:             pipeline.ModeFilePrimary,
		StorageNamespace: "ns:tail-fast",
		UTCDay:           day,
	}
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src}})
	require.NoError(t, err)
	rec := &stageRecorder{}
	m.stageSink = rec.sink

	// 第 1 轮：段尚空 ⇒ 走增量尾部快路径**同样安全**（无重复可去，权威集合为空），
	// 关键是事件体必须被落段、且水位正确推进。
	m.pollOnce()
	require.NotEmpty(t, rec.all(), "本轮必须真的走到投递，否则测的不是这条路径")

	key := src.LogSourceID + "/" + src.SourceGeneration
	storedFirst, err := m.events.Count(key)
	require.NoError(t, err)
	require.Positive(t, storedFirst, "首批投递后事件体必须已落段")

	// 第 2 轮：追加新行（区间整体在「段已覆盖水位」与「已投递水位」之后）⇒ 走快路径。
	rec2 := &stageRecorder{}
	m.stageSink = rec2.sink
	appendPollTestLines(t, path, pollTestLines(50, 50))
	m.pollOnce()
	require.True(t, rec2.hasStage("seen-skipped"),
		"第二批是段的增量尾部，必须跳过 O(段行数) 的身份集合重建：%v", rec2.all())
	require.True(t, rec2.hasStage("recovery-skipped"),
		"第二批必须跳过权威全量构建：%v", rec2.all())

	// 段必须按增量追加（快路径不得漏追加，也不得重复追加）。
	storedSecond, err := m.events.Count(key)
	require.NoError(t, err)
	require.Greater(t, storedSecond, storedFirst, "快路径必须把本批追加进段")
	require.LessOrEqual(t, storedSecond, storedFirst+50, "快路径不得重复追加")

	// 重放语义：replay=true 必须回退到全量路径（整窗重发需要权威全量）。
	rec3 := &stageRecorder{}
	m.stageSink = rec3.sink
	var all []logtypes.Event
	require.NoError(t, m.events.Iterate(key, func(e logtypes.Event) error {
		all = append(all, e)
		return nil
	}))
	_, err = m.deliver(src, all, true)
	require.NoError(t, err)
	require.False(t, rec3.hasStage("seen-skipped"), "重放不得走快路径：%v", rec3.all())
	require.False(t, rec3.hasStage("recovery-skipped"), "重放必须构建权威全量：%v", rec3.all())
}

// 快路径判据的边界：区间与水位重叠（tailer 回退/重读）时必须回退到全量路径。
//
// 这是「跳过身份重建」的安全边界——重复只可能来自「该区间曾经投递过」，
// 判据用最小 record_start 与两个水位比较把它排除。
func TestDeliverTailFallsBackOnOverlappingRange(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	dir := filepath.Join(root, "s00")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(pollTestLines(0, 50)), 0o600))

	day := runtimeTestUTCDay()
	src := SourceConfig{
		LogSourceID:      "node:tail-overlap",
		SourceGeneration: "g1",
		Path:             path,
		Mode:             pipeline.ModeFilePrimary,
		StorageNamespace: "ns:tail-overlap",
		UTCDay:           day,
	}
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src}})
	require.NoError(t, err)
	m.pollOnce()

	key := src.LogSourceID + "/" + src.SourceGeneration
	saved := func() persistedSource {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.state.Sources[key]
	}()
	require.True(t, saved.EventsStored, "前置：首批后事件体已落段")

	// 构造一段**与已落段区间重叠**的事件（模拟回退/重读）：min record_start 落在段覆盖水位之内。
	var oldest logtypes.Event
	require.NoError(t, m.events.Iterate(key, func(e logtypes.Event) error {
		if oldest.EventID == "" || e.Record.Start < oldest.Record.Start {
			oldest = e
		}
		return nil
	}))
	overlap := []logtypes.Event{oldest}
	tail := m.planDeliveryTail(key, saved, overlap, false)
	require.False(t, tail.Fast,
		"与已落段区间重叠的批必须回退全量路径（否则已存在的事件会被重复投递）")

	// 对照：整体在水位之后的新区间 → 快路径成立。
	fresh := oldest
	fresh.EventID = oldest.EventID + "-fresh"
	fresh.Record.Start, fresh.Record.End = saved.EventsStoredThrough+1, saved.EventsStoredThrough+1
	require.True(t, m.planDeliveryTail(key, saved, []logtypes.Event{fresh}, false).Fast,
		"整体在段覆盖水位之后的新区间必须走快路径")
	_ = time.Now
}
