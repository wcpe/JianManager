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
//
// ## 2026-11-02：为什么夹具必须先建 Manager 再写日志
//
// FILE_PRIMARY 源的登记路径一律 `StartAtEOF: true`（生产裁定 D：新源不回头吃别人的历史，
// 见 cursor_registry.go 的文件头与 runtime.go 的 Register）。本文件原先**先** `os.WriteFile`
// 写入 50 行、**再** newTestManager ⇒ 那 50 行在管道出生前就存在 ⇒ 被 EOF 起点整体跳过
// ⇒ 一轮采集零事件、rec.all() 为空 ⇒ 断言报「本轮必须真的走到投递」。
//
// 这里**不能**通过放宽生产默认值来修（那会违反「新源不补历史」的产品约定）。正确的前置条件是
// 「先登记源、再产生日志」——正是生产里一个源从「刚被绑定的空文件」开始播日志的形状。
// 所以夹具改成：先写空文件 → newTestManager（此时 EOF 起点 = 0）→ 再追加 50 行。
//
// ## 2026-11-02 第二修：夹具必须用**唯一且干净**的段根
//
// 上面那个改法还不够：`eventstore.MemoryRoot` 只用 `filepath.Base(root)` 做 tmpfs 目录名，
// 而 `t.TempDir()` 的 basename 在跨运行之间是稳定的 `001` ⇒ **同名/不同名的用例会共用同一个
// /dev/shm 段根**（docs/CONVENTIONS.md 已记录的坑）。实测：只写了 50 行，段里却有 100 行
// （`m.events.Count` 读到 100）——那是别的用例残留的段。于是 `EventsStoredThrough` 被推到
// 4500，而第二批的 `[2250,4500)` 落在它之后、却只落在 `Delivery=2205` 之后的那一截里，
// 快路径的三条判据不再同时成立 ⇒ `seen-skipped` 不出现 ⇒ 本用例假红（单独跑一次能碰上干净目录时又是绿的，
// 这就是它一度“时红时绿”的原因）。
//
// 所以两个用例都改成 `uniqueSegmentRoot` + 建 Manager 前先清一次（与 cursor_registry_restore_test.go 同一写法）。

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
	root, _ := freshSegmentRoot(t, "deliver-tail-fast")
	dir := filepath.Join(root, "s00")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "latest.log")
	// 先建空文件（EOF 起点 = 0），源登记之后才产生日志（见文件头「为什么夹具必须先建 Manager」）。
	require.NoError(t, os.WriteFile(path, nil, 0o600))

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

	// 首批 50 行：在管道已登记（EOF = 0）之后写入 ⇒ 全部属于「本进程出生之后的新字节」。
	appendPollTestLines(t, path, pollTestLines(0, 50))

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
	root, _ := freshSegmentRoot(t, "deliver-tail-overlap")
	dir := filepath.Join(root, "s00")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "latest.log")
	// 同 TestDeliverUsesAppendedTailFastPath：先空文件、登记后写（新源从 EOF 起读）。
	require.NoError(t, os.WriteFile(path, nil, 0o600))

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
	appendPollTestLines(t, path, pollTestLines(0, 50))
	m.pollOnce()

	key := src.LogSourceID + "/" + src.SourceGeneration
	saved := func() persistedSource {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.state.Sources[key]
	}()
	require.True(t, saved.EventsStored, "前置：首批后事件体已落段")
	// 夹具自检（不代替被测行为，只是避免把「本来就没投递」误读成「快路径判据错」）：
	// 首批必须真的投递并落段，否则下面的「区间重叠」判据就没有可重叠的对象。
	stored, err := m.events.Count(key)
	require.NoError(t, err)
	require.Positive(t, stored, "夹具前置：首批必须已落段（否则本用例无法构造重叠区间）")

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
