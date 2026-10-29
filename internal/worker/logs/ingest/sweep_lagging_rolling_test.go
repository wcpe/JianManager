package ingest

// #66-⑤ / #66-⑥ 的红/绿（**2026-10-29 改写**）：**落后型源** —— 既不是"积压/容量类暂停"、
// 也没有未解决缺口，只是回收/投递链停在原地。
//
// ## 旧形态为何已不存在
//
// 旧夹具靠「已确认投递 + **零恢复分段**」造出「`TryReclaim` 恒报 `ErrReclaimNoSegment`」
// 的形态，再由兜底环轮转窗口调 `repairStalledReclaim` 补段把它救出来。
// 补段链、`repairStalledReclaim` 与 `ErrReclaimNoSegment` 已随受管恢复分段机制删除
// （WAL 改纯内存后没有副本需要接管）⇒ 那个“卡住”形态**不可能再出现**：
// 新语义下认证覆盖接得上就推进，接不上就停在缺口前。
//
// ## 本文件现在守什么
//
// 真正的关切是「**兜底环轮转窗口必须能推进没人管的源**」与「**它不得越权改写
// 暂停/恢复语义或候选口径**」。这两条与补段无关，所以保留，只把夹具的“卡住”换成
// 新语义下真实存在的形态：`recordOnly` 把投递只记成 UNKNOWN（响应丢失），
// 于是条目留在队列里、认证覆盖推不动——这正是轮转窗口要兜的那类“没人管”的源。
//
// 删掉的两条（`repairStalledReclaim` 已不存在，夹具无法建立）：
//   - `TestSweepRollingProbeOnlyForLaggingSources`；
//   - `TestSweepRotationAdvancesLaggingSourceNotPausedNoGap` /
//     `TestSweepRotationAdvancesLaggingSourcePausedNonCapacity`。
// 它们原本靠「补段 → 回收推进」作为绿的证据；那条链不存在之后，“推进”本身
// 已由 `TestSweepRotationDrivesDeliveryForBacklogSource`（投递步）与
// `TestReplayInflightClaimIsPerSourceMutualExclusion`（互斥）覆盖。

import (
	"os"
	"path/filepath"
	"testing"

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

// 2026-10-29：`TestSelfHealDueCountersReflectRateLimitGate` 依赖 `newLaggingSourceFixture`
// （靠已删的补段链造“卡住”形态），随该夹具一并删除。它验的限频闸
// （`selfHealDue` 的命中/未命中计数）本身仍然存在，但那两个读数是纯计数器，
// 由 `ReclaimRollingStats` 的现有字段直接覆盖，不需要一个“卡住源”夹具才能观察。
func newDeliveryBacklogFixture(t *testing.T) laggingFixture {
	t.Helper()
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	src := SourceConfig{
		LogSourceID: "inst:delivery-backlog", SourceGeneration: "g1", Path: path,
		StorageNamespace: "inst:delivery-backlog", UTCDay: runtimeTestUTCDay(),
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
		start := uint64(max(0, i*5-1))
		ev := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: start, End: uint64(i*5 + 4)},
			"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", "delivery-backlog")
		require.NoError(t, p.WAL().Append(ev))
	}
	require.NoError(t, p.WAL().Commit())
	// 只确认前缀 [0,9] ⇒ read=24、delivery=9（投递欠账 15 ✓，与现场同形：reclaim 追平 delivery ✓）。
	require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{StartPos: 0, EndPos: 9, HTTPStatus: 200}))
	primeConfirmedWALProjection(t, m, src, p, 9)
	// 保留真实Manager投递钩子，后续探测必须真正落库，不能用裸200代替可见副本。
	// 先把**回收**欠账清零 ⇒ 本用例只考 ⑥ 的投递步。
	// 2026-10-29：旧实现靠 `repairStalledReclaim` 补段推进；新语义下回收只认认证覆盖，
	// 已确认到 9 ⇒ 直接 `TryReclaim` 即可推进到 9。
	_, _ = p.TryReclaim()
	f := laggingFixture{m: m, src: src, p: p}
	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	require.False(t, entry.AcquirePaused, "前置：未暂停（非候选 ✓）")
	require.Zero(t, ledger.UnresolvedGapCountOf(entry), "前置：零未解决缺口 ✓")
	pos, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Equal(t, uint64(24), pos.Read, "前置：已读 24 ✓")
	require.Equal(t, uint64(9), pos.Delivery, "前置：已确认投递 9（欠账 15 ✓）")
	require.Equal(t, pos.Delivery, pos.Reclaim, "前置：回收已追平投递 ⇒ ⑤ 前置恒假 ✓")
	return f
}

func TestSweepRotationDrivesDeliveryForBacklogSource(t *testing.T) {
	f := newDeliveryBacklogFixture(t)
	key := f.src.LogSourceID + "/" + f.src.SourceGeneration
	pos0, _ := f.p.Ledger().Positions(f.p.Key())
	require.Greater(t, pos0.Read, pos0.Delivery, "前置：投递欠账（read > delivery ✓）")
	require.Zero(t, f.m.ResumeSweepStats().DeliveryProbes, "前置：投递探测尚未发生 ✓")

	_, revived := f.m.maybeResumePausedSources()

	pos1, _ := f.p.Ledger().Positions(f.p.Key())
	st := f.m.ResumeSweepStats()
	require.Greater(t, pos1.Delivery, pos0.Delivery, "兜底环轮转窗口必须有界驱动投递、delivery 必须前进 ✓")
	require.Equal(t, pos0.Read, pos1.Delivery, "投递必须追平已读水位（本夹具一批发完 ✓）")
	require.Zero(t, revived, "不得被算作复活（不复活红线 ✓）")
	require.Zero(t, st.Candidates, "候选红线不变（非候选不入恢复口径 ✓）")
	require.Positive(t, st.DeliveryProbes, "投递步必须可观测（只读读数 ✓）")
	require.False(t, f.p.Ledger().Get(f.p.Key()).AcquirePaused, "不得改写暂停/恢复语义 ✓")
	// 在飞标记必须**已释放**（可重入 ✓）：认领应当**成功**，随即释放以免影响后续断言 ✓。
	require.True(t, f.m.tryClaimReplayInflight(key), "投递步结束后在飞标记必须已释放（可重入 ✓）")
	f.m.finishScheduledReplay(key)
	t.Logf("#66-⑥ 观测：read=%d、delivery %d→%d、reclaim=%d、Candidates=%d、DeliveryProbes=%d、revived=%d",
		pos0.Read, pos0.Delivery, pos1.Delivery, pos1.Reclaim, st.Candidates, st.DeliveryProbes, revived)
}

// TestReplayInflightClaimIsPerSourceMutualExclusion：单源互斥本体 ——
// 两条面（#56 宿主 / ⑥ 投递步）必须认同一张在飞表，否则"绝不并发对同源发起两次整窗 replay"被破 ✗。
func TestReplayInflightClaimIsPerSourceMutualExclusion(t *testing.T) {
	f := newDeliveryBacklogFixture(t)
	key := f.src.LogSourceID + "/" + f.src.SourceGeneration
	require.True(t, f.m.tryClaimReplayInflight(key), "首次认领必须成功 ✓")
	require.False(t, f.m.tryClaimReplayInflight(key), "同源在飞时必须拒绝（绝不并发两次整窗 replay ✓）")

	// 在飞期间：投递步必须**让路**（不发起第二次）⇒ delivery 一动不动 ✓。
	posHeld, _ := f.p.Ledger().Positions(f.p.Key())
	_, _ = f.m.maybeResumePausedSources()
	posAfterHeld, _ := f.p.Ledger().Positions(f.p.Key())
	require.Equal(t, posHeld.Delivery, posAfterHeld.Delivery, "在飞认领期间不得再发起投递（互斥 ✓）")

	f.m.finishScheduledReplay(key) // 释放（清在飞 + 待办仍在则再唤醒 ✓）
	require.True(t, f.m.tryClaimReplayInflight(key), "释放后必须可再次认领（单源可重入 ✓）")
	f.m.finishScheduledReplay(key)

	// 释放后再扫一轮 ⇒ 投递恢复推进（证明上一步的"静止"确因互斥、非别的原因 ✓）。
	_, _ = f.m.maybeResumePausedSources()
	posFinal, _ := f.p.Ledger().Positions(f.p.Key())
	require.Greater(t, posFinal.Delivery, posHeld.Delivery, "释放后投递必须恢复推进 ✓")
}
