package ingest

// #66-⑤ 的红/绿：**落后型源** —— 既不是"积压/容量类暂停"、也没有未解决缺口，只是回收链卡住
// （reclaim 远落后于已确认投递水位、且零恢复分段覆盖）⇒ 现行**三条面全都到不了它**：
//
//	① 投递确认面（releaseRecovery 顶部，runtime.go:5447）：只在"有新批次被确认投递"时才跑 ✗
//	   —— 落后源已停投/无新批次 ⇒ 一次都跑不到；
//	② 自愈面（selfHealPausedSource 步 0）：准入是「已暂停 **或** 仍有未解决缺口」
//	   （gap_autoresolve.go:136）⇒ 本类源两者皆非 ⇒ 直接早退 ✗；
//	③ 兜底环轮转窗口（maybeResumePausedSources）：候选资格要求「已暂停 且 原因属积压/容量类」
//	   （runtime.go:6742-6747）⇒ 形态 B 走早退、形态 A 计入 skipEnumeration ⇒ 零动作 ✗。
//
// 与现场一致：新窗四源**零 `滚动回收` 行**、全库「本轮未推进（回收被拒）」= 0 条 ⇒
// **不是"到了被拒"，是"根本没到"** ✓（"被拒"行只在 rolling 内部才会打 ⇒ rolling 从未被调用 ✓）。
//
// 修法（#66-⑤）：把"只滚动/补段"的 step-0 助手挂进兜底环轮转窗口的**早退之前**
//（不动步 1/2/3 的准入红线 ✓、hold 优先不变 ✓、每轮有界 ✓）⇒ 轮转窗口扫遍所有管道
// ⇒ 任意落后源在 ⌈E/batch⌉ 轮内必被推进一次 ✓。
//
// 两形态：
//   - **形态 B（主证）**：未暂停 + 零缺口 ⇒ 现行三面无一路径 ⇒ 零滚动（红）；
//   - **形态 A（父裁令点名的"非容量类暂停"）**：暂停原因非积压类 ⇒ 现行被 skipEnumeration 挡下（红）。
//     注：形态 A 在生产里**仍会**被自愈面②覆盖（暂停即过其状态闸 ✓），本用例纳入它是为证明新钩子
//     对"暂停但非容量类"同样有效，且**候选红线不变**（Candidates 恒 0 ✓）。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

type laggingFixture struct {
	m   *Manager
	src SourceConfig
	p   *pipeline.Pipeline
}

// newLaggingSourceFixture：durable 存量 + 已确认投递水位（24）+ **零恢复分段** + 零缺口
// ⇒ `TryReclaim` 恒报"无段覆盖"（回收链卡住 ✓），且现行没有任何面会来推进它（见文件头 ✓）。
func newLaggingSourceFixture(t *testing.T) laggingFixture {
	t.Helper()
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	src := SourceConfig{
		LogSourceID: "inst:lagging", SourceGeneration: "g1", Path: path,
		StorageNamespace: "inst:lagging", UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{src},
	})
	require.NoError(t, err)
	key := src.LogSourceID + "/" + src.SourceGeneration
	p := m.pipes[key]
	require.NotNil(t, p)
	identity := logtypes.SourceIdentity{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1"}
	for i := 0; i < 5; i++ {
		start := uint64(i * 5)
		ev := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: start, End: start + 4},
			"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", "lagging")
		require.NoError(t, p.WAL().Append(ev))
	}
	require.NoError(t, p.WAL().Commit())
	// 投递水位 = 24（区间 [0,24] 连续 ✓）：这是补段的上界，不越界未来 ✓。
	require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{StartPos: 0, EndPos: 24, HTTPStatus: 200}))
	m.SetSelfHealInterval(time.Nanosecond)
	// 投递面**必失败**（确定性桩）：现场"没人能走到投递确认面"的形态 ✓。
	p.SetDelivery(pipeline.FuncHook(func([]logtypes.Event, bool) (pipeline.DeliveryResult, error) {
		return pipeline.DeliveryResult{}, errors.New("stub: VL 不可用（本用例只考兜底环轮转窗口）")
	}))
	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	require.Zero(t, ledger.UnresolvedGapCountOf(entry), "前置：零未解决缺口（落后形态 ✓）")
	require.Empty(t, entry.RecoveryRefs, "前置：零恢复分段（洞的形态 ✓）")
	return laggingFixture{m: m, src: src, p: p}
}

// assertLaggingRedShape：红形态前置 —— 回收被"无段覆盖"拒（现行三面都不来治 ⇒ 恒被拒 ✓）。
func (f laggingFixture) assertLaggingRedShape(t *testing.T) {
	t.Helper()
	pos, ok := f.p.Ledger().Positions(f.p.Key())
	require.True(t, ok)
	require.Zero(t, pos.Reclaim, "前置：回收停在 0（落后形态 ✓）")
	require.Equal(t, uint64(24), pos.Delivery, "前置：已确认投递水位 24（补段上界 ✓）")
	_, err := f.p.TryReclaim()
	require.True(t, errors.Is(err, ledger.ErrReclaimNoSegment), "前置红形态（无段覆盖）：%v", err)
}

func TestSweepRotationAdvancesLaggingSourceNotPausedNoGap(t *testing.T) {
	f := newLaggingSourceFixture(t)
	require.False(t, f.p.Ledger().Get(f.p.Key()).AcquirePaused, "形态 B 前置：源未暂停 ✓")
	f.assertLaggingRedShape(t)

	// 反证（根因本体）：自愈面（步 0）对本形态**不会**触发 —— 状态闸（未暂停 且 零缺口）直接早退 ✓。
	require.False(t, f.m.selfHealPausedSource(f.src, f.p), "自愈面在本形态下必须空转（准入不满足 ✓）")
	posSelfHeal, _ := f.p.Ledger().Positions(f.p.Key())
	require.Zero(t, posSelfHeal.Reclaim, "自愈面在本形态下不会推进回收（'三面都到不了'的根因 ✓）")

	before := f.m.ReclaimRollingStats()
	require.Zero(t, before.Rounds, "前置：滚动回收一次都没跑过（现场零行 ✓）")

	_, revived := f.m.maybeResumePausedSources()

	after := f.m.ReclaimRollingStats()
	pos, _ := f.p.Ledger().Positions(f.p.Key())
	require.Greater(t, after.Rounds, before.Rounds, "兜底环轮转窗口必须发起一次滚动回收 ✓")
	require.Greater(t, after.Advanced, before.Advanced, "回收必须链式推进（有推进量 ✓）")
	require.Equal(t, uint64(24), pos.Reclaim, "回收必须推进到已确认投递水位 24 ✓")
	var backfilled []ledger.RecoveryRef
	for _, ref := range f.p.Ledger().Get(f.p.Key()).RecoveryRefs {
		if strings.HasPrefix(ref.SegmentID, "delivery-confirmed-") {
			backfilled = append(backfilled, ref)
		}
	}
	require.Len(t, backfilled, 1, "必须补出唯一一段「投递确认」覆盖段（与自愈面同源助手 ✓）")
	require.Equal(t, uint64(0), backfilled[0].CoversFrom)
	require.Equal(t, uint64(24), backfilled[0].CoversTo)

	// **准入红线不变**：本源仍未进入兜底环的候选/复活口径（Candidates=0、revived=0 ✓）。
	st := f.m.ResumeSweepStats()
	require.Zero(t, st.Candidates, "落后型源不得被算作恢复候选（红线不变 ✓）")
	require.Zero(t, revived, "落后型源不得被算作复活（红线不变 ✓）")
	require.False(t, f.p.Ledger().Get(f.p.Key()).AcquirePaused, "未暂停的源不得被本钩子改写暂停/恢复语义 ✓")
	require.Positive(t, st.RollingProbes, "必须记录发起过一次滚动探测（可观测 ✓）")
	t.Logf("#66-⑤ 观测（形态 B：未暂停+零缺口）：rounds %d→%d、advanced %d→%d、firstAdvance=%d、"+
		"reclaim=%d、delivery=%d、Candidates=%d、RollingProbes=%d",
		before.Rounds, after.Rounds, before.Advanced, after.Advanced, after.FirstAdvance,
		pos.Reclaim, pos.Delivery, st.Candidates, st.RollingProbes)
}

func TestSweepRotationAdvancesLaggingSourcePausedNonCapacity(t *testing.T) {
	f := newLaggingSourceFixture(t)
	key := f.p.Key()
	require.NoError(t, f.p.Ledger().PauseAcquire(key, "hold: 运维保持（非积压类原因，测试桩）"))
	f.assertLaggingRedShape(t)
	entry := f.p.Ledger().Get(key)
	require.True(t, entry.AcquirePaused)
	require.False(t, acquire.IsBacklogPauseReason(entry.PauseReason), "形态 A 前置：原因非积压/容量类 ✓")

	before := f.m.ReclaimRollingStats()
	_, revived := f.m.maybeResumePausedSources()
	after := f.m.ReclaimRollingStats()
	pos, _ := f.p.Ledger().Positions(key)
	require.Greater(t, after.Rounds, before.Rounds, "非容量类暂停的落后源也必须被轮转窗口推进 ✓")
	require.Equal(t, uint64(24), pos.Reclaim, "回收必须推进到已确认投递水位 24 ✓")
	require.Zero(t, revived)
	st := f.m.ResumeSweepStats()
	require.Zero(t, st.Candidates, "候选红线仍不变（非积压类暂停不入候选 ✓）")
	require.Positive(t, st.SkipEnumeration, "读数保留：它仍**不是**恢复候选 ✓（只是回收被顺带推进 ✓）")
	t.Logf("#66-⑤ 观测（形态 A：非容量类暂停）：rounds %d→%d、advanced %d→%d、reclaim=%d、"+
		"delivery=%d、Candidates=%d、skipEnumeration=%d",
		before.Rounds, after.Rounds, before.Advanced, after.Advanced, pos.Reclaim,
		pos.Delivery, st.Candidates, st.SkipEnumeration)
}

func TestSweepRollingProbeOnlyForLaggingSources(t *testing.T) {
	f := newLaggingSourceFixture(t)
	// 把源变成"无欠账"（回收 ≥ 投递）：补段并推进回收一次 ✓。
	require.NoError(t, f.m.repairStalledReclaim(f.p, f.src))
	_, _ = f.p.TryReclaim()
	pos, ok := f.p.Ledger().Positions(f.p.Key())
	require.True(t, ok)
	require.GreaterOrEqual(t, pos.Reclaim, pos.Delivery, "前置：无欠账（健康形态 ✓）")

	probesBefore := f.m.ResumeSweepStats().RollingProbes
	roundsBefore := f.m.ReclaimRollingStats().Rounds
	_, _ = f.m.maybeResumePausedSources()
	require.Equal(t, probesBefore, f.m.ResumeSweepStats().RollingProbes, "健康源不得发起滚动探测（成本定界 ✓）")
	require.Equal(t, roundsBefore, f.m.ReclaimRollingStats().Rounds, "健康源不得产生滚动轮次 ✓")
}

func TestSelfHealDueCountersReflectRateLimitGate(t *testing.T) {
	f := newLaggingSourceFixture(t)
	f.m.SetSelfHealInterval(time.Hour)
	pass0 := f.m.ReclaimRollingStats().SelfHealDuePass
	skip0 := f.m.ReclaimRollingStats().SelfHealDueSkip
	require.True(t, f.m.selfHealDue("k:one"), "首次必然越过限频闸 ✓")
	require.False(t, f.m.selfHealDue("k:one"), "同源同间隔内第二次必被限频闸挡下 ✓")
	st := f.m.ReclaimRollingStats()
	require.Equal(t, pass0+1, st.SelfHealDuePass, "命中计数必须 +1（只读诊断 ✓）")
	require.Equal(t, skip0+1, st.SelfHealDueSkip, "未命中计数必须 +1（只读诊断 ✓）")
	// **状态闸不计入**（口径对齐 ✓）：它发生在 selfHealDue **之前** ⇒ 这两个读数只反映限频闸 ✓。
}
