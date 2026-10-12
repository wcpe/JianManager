package ingest

// #63 的红/绿：**滚动回收**（探测驱动的小循环）—— 阈值即现场靶数（同一套数 ✓）。
//
// 现场：#62 后 `reclaim` 动了，但速率 **344 位/s/源**（1.14 亿需 3.7 天 ✗✗），且复采差**逐字相同**
// ⇒ "一次性微推后再度停住" ✓。机制：`TryReclaim` 单次上界 = 当前覆盖段的 `CoversTo`，而补段闸只在
// "完全无覆盖"时补 ⇒ 位置被**陈旧段**覆盖时每轮只进一个排空批（≈3500 位 / 10s ✓）。
//
// 靶数（opt2）：最低线 **单源 ≥4,000 位/s**、**四源合计 ≥13,500 位/s**（= 8h 收官）；
// 及格语义 = **追平生成速率**（生产仍灌料 ⇒ 每轮覆盖新增区间，而非只清零存量 ✓）。
// 速率口径与现场一致：`位 / 自愈限频周期(10s)`（现场 344 = 3500/10 ✓）。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

const (
	// reclaimRollDelivery 是夹具的"已确认投递水位"（缺口大 ⇒ 一次微推吃不完 ✓）。
	reclaimRollDelivery = uint64(4_000_000)
	// reclaimRollStaleCover 是预置的**陈旧段**上界（现场形态：`CoversTo ≪ delivery` ✓）。
	reclaimRollStaleCover = uint64(5_000)
	// reclaimRollMinRatePerSource / reclaimRollMinRateFourSources 是 opt2 给的靶数（测试断言用最低线 ✓）。
	reclaimRollMinRatePerSource   = 4000.0
	reclaimRollMinRateFourSources = 13500.0
)

// rollingRatePerSec 把"本轮推进量"换算成**现场口径速率**：位 / 自愈限频周期（10s ✓）。
func rollingRatePerSec(advanced uint64) float64 {
	return float64(advanced) / gapAutoResolveInterval.Seconds()
}

// newRollingReclaimFixture 造 n 个"陈旧段覆盖 + 大缺口 + 停投"的源：
//   - 一条事件覆盖 [0, D]（D 很大：位置跨度可以很大，WAL 仍只有一条 ✓）；
//   - 投递水位已确认到 D（凭据上界 ✓）；
//   - 预置一条只覆盖 [0, 5000) 的段（Xfer ⇒ CanReclaim 放行 ✓）⇒ 一次探测只能推到 5000 ✗；
//   - 源暂停 + 投递面必失败（⇒ 只有恢复/自愈面能治 ✓）。
func newRollingReclaimFixture(t *testing.T, n int) (*Manager, []SourceConfig, []*pipeline.Pipeline) {
	t.Helper()
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	srcs := make([]SourceConfig, 0, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(root, fmt.Sprintf("latest-%d.log", i))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
		srcs = append(srcs, SourceConfig{
			LogSourceID: fmt.Sprintf("inst:roll%d", i), SourceGeneration: "g1", Path: p,
			StorageNamespace: fmt.Sprintf("inst:roll%d", i), UTCDay: runtimeTestUTCDay(),
		})
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: srcs})
	require.NoError(t, err)
	m.SetSelfHealInterval(time.Nanosecond) // 让"每轮"都能真跑（速率仍按现场 10s 周期折算 ✓）

	pipes := make([]*pipeline.Pipeline, 0, n)
	for i, src := range srcs {
		p := m.pipes[src.LogSourceID+"/"+src.SourceGeneration]
		require.NotNil(t, p)
		identity := logtypes.SourceIdentity{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1"}
		require.NoError(t, p.WAL().Append(logtypes.BuildEvent(identity,
			logtypes.RecordRange{Start: 0, End: reclaimRollDelivery},
			"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", "rolling")))
		require.NoError(t, p.WAL().Commit())
		require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{
			StartPos: 0, EndPos: reclaimRollDelivery, HTTPStatus: 200,
		}))
		// 陈旧覆盖段（现场形态 ✓）。
		segID := fmt.Sprintf("projection-recovery-stale-%d", i)
		require.NoError(t, p.BindRecoverySegment(segID, "projection://stale", 0, reclaimRollStaleCover))
		require.NoError(t, p.TransitionRecovery(segID, logtypes.RecoveryDurableVerified, "", ""))
		require.NoError(t, p.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer, "", "projection:stale"))
		// 暂停 + 投递面必失败（唯一出口 = 恢复/自愈面 ✓）。
		require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "TEST_STALL: backlog over limit (fixture)"))
		p.SetDelivery(pipeline.FuncHook(func([]logtypes.Event, bool) (pipeline.DeliveryResult, error) {
			return pipeline.DeliveryResult{}, errors.New("stub: VL 不可用（本用例只考滚动回收）")
		}))
		pipes = append(pipes, p)
	}
	return m, srcs, pipes
}

// reclaimOf 读某源当前回收位置。
func reclaimOf(t *testing.T, p *pipeline.Pipeline) uint64 {
	t.Helper()
	pos, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	return pos.Reclaim
}

// TestRollingReclaimKeepsUpWithGeneration：单源/四源速率 + 二轮"追平"。
func TestRollingReclaimKeepsUpWithGeneration(t *testing.T) {
	m, srcs, pipes := newRollingReclaimFixture(t, 4)

	// 一轮（每源一次自愈调用）：
	before := make([]uint64, len(pipes))
	for i, p := range pipes {
		before[i] = reclaimOf(t, p)
		m.selfHealPausedSource(srcs[i], p)
	}
	var advanced1 uint64
	for i, p := range pipes {
		advanced1 += reclaimOf(t, p) - before[i]
	}
	single := rollingRatePerSec(advanced1 / uint64(len(pipes)))

	// 读数（Z3-c）：与现场判据同一套数 ✓。
	st := m.ReclaimRollingStats()
	t.Logf("#63 观测：四源本轮合计推进 %d 位 ⇒ 单源 %.1f 位/s、四源合计 %.1f 位/s（靶数 4,000 / 13,500）· 一轮 steps=%d firstAdvance=%d elapsed=%s",
		advanced1, single, rollingRatePerSec(advanced1), st.Last.Steps, st.Last.FirstAdvance, st.Last.Elapsed)

	require.GreaterOrEqual(t, single, reclaimRollMinRatePerSource,
		"单源速率必须达到靶数（现场 344 位/s ✗✗；最低线 4,000 ⇒ 8h 收官）")
	require.GreaterOrEqual(t, rollingRatePerSec(advanced1), reclaimRollMinRateFourSources,
		"四源合计必须达到靶数（最低线 13,500 位/s）")
	for i, p := range pipes {
		require.Equal(t, reclaimRollDelivery, reclaimOf(t, p),
			"第 %d 源必须一次吃到位（陈旧段 + 大缺口形态 ⇒ 链式推进到已确认水位 ✓）", i)
	}

	// 二轮"追平生成速率"：各源追加一截**新确认**区间（模拟生产仍在灌料 ✓）。
	const gen = uint64(1_000_000)
	before2 := make([]uint64, len(pipes))
	for i, p := range pipes {
		before2[i] = reclaimOf(t, p)
		identity := logtypes.SourceIdentity{LogSourceID: srcs[i].LogSourceID, SourceGeneration: srcs[i].SourceGeneration, ParserVersion: "v1"}
		// 暂停态下 WAL.Append 会被门禁拒（设计如此 ✓）⇒ 造增量时临时放行、随后重新暂停，
		// 以保持"停投 + 自愈面驱动"的夹具形态不变 ✓。
		require.NoError(t, p.Ledger().ResumeAcquire(p.Key()))
		require.NoError(t, p.WAL().Append(logtypes.BuildEvent(identity,
			logtypes.RecordRange{Start: reclaimRollDelivery, End: reclaimRollDelivery + gen},
			"2026-10-05T00:01:00Z", "2026-10-05T00:01:01Z", "INFO", "stdout", "gen")))
		require.NoError(t, p.WAL().Commit())
		require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "TEST_STALL: backlog over limit (fixture)"))
		require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{
			StartPos: reclaimRollDelivery, EndPos: reclaimRollDelivery + gen, HTTPStatus: 200,
		}))
		m.selfHealPausedSource(srcs[i], p)
	}
	var advanced2 uint64
	for i, p := range pipes {
		advanced2 += reclaimOf(t, p) - before2[i]
	}
	t.Logf("#63 观测（二轮追平）：四源合计推进 %d 位 ⇒ 合计 %.1f 位/s", advanced2, rollingRatePerSec(advanced2))
	require.GreaterOrEqual(t, rollingRatePerSec(advanced2), reclaimRollMinRateFourSources,
		"二轮必须继续追平新生数据（及格语义：追平生成速率 > 清零存量 ✓）")
}

// TestRollingReclaimBoundedAndHoldSafe：单轮有界（不霸占）+ hold 优先（一步不推进）。
func TestRollingReclaimBoundedAndHoldSafe(t *testing.T) {
	m, srcs, pipes := newRollingReclaimFixture(t, 1)
	p := pipes[0]

	// 单轮上界：maxSteps=1 ⇒ 只推进**一步**（= 陈旧段末端），且**下一轮继续**前进（有界但不饿死 ✓）。
	m.reclaimRollMaxSteps = 1
	m.selfHealPausedSource(srcs[0], p)
	require.Equal(t, reclaimRollStaleCover, reclaimOf(t, p), "一步只应推到陈旧段末端（单轮有界 ✓）")
	m.selfHealPausedSource(srcs[0], p)
	require.Greater(t, reclaimOf(t, p), reclaimRollStaleCover, "下一轮必须继续前进（不饿死 ✓）")

	// hold 优先：给覆盖段加 hold ⇒ 一步都不能推进（且不得补段绕开 ✓）。
	refsBefore := len(p.Ledger().Get(p.Key()).RecoveryRefs)
	require.NoError(t, p.Ledger().SetRecoveryHold(p.Key(), "projection-recovery-stale-0", true))
	beforeHold := reclaimOf(t, p)
	m.reclaimRollMaxSteps = 0 // 恢复默认上界
	for i := 0; i < 3; i++ {
		m.selfHealPausedSource(srcs[0], p)
	}
	require.Equal(t, beforeHold, reclaimOf(t, p), "hold 期间回收一步都不许推进（hold 优先 ✓）")
	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	require.Len(t, entry.RecoveryRefs, refsBefore, "hold 期间不得新增任何段（不许绕开人工约束 ✓）")
}

// newReclaimChainFixture 造一条**首尾相接**的陈旧段链（#64 现场形态：段数 ≫ 单轮步数）：
// 第 i 段覆盖 [i*w, (i+1)*w)，末段末端 = N*w = 已确认投递水位 ✓（位置**始终被覆盖** ⇒ 只能一段一跳 ✓）。
// tune 非 nil 时先改 Options（用于验证配置面上的步数上界 ✓）。
func newReclaimChainFixture(t *testing.T, n int, tune func(*Options)) (*Manager, SourceConfig, *pipeline.Pipeline) {
	t.Helper()
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	src := SourceConfig{
		LogSourceID: "inst:chain", SourceGeneration: "g1", Path: path,
		StorageNamespace: "inst:chain", UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	opts := Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src}}
	if tune != nil {
		tune(&opts)
	}
	m, err := newTestManager(t, opts)
	require.NoError(t, err)
	m.SetSelfHealInterval(time.Nanosecond)
	p := m.pipes[src.LogSourceID+"/"+src.SourceGeneration]
	require.NotNil(t, p)
	const width = uint64(100_000)
	total := uint64(n) * width
	identity := logtypes.SourceIdentity{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1"}
	require.NoError(t, p.WAL().Append(logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 0, End: total},
		"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", "chain")))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{StartPos: 0, EndPos: total, HTTPStatus: 200}))
	for i := 0; i < n; i++ {
		segID := fmt.Sprintf("projection-recovery-chain-%d", i)
		require.NoError(t, p.BindRecoverySegment(segID, "projection://chain", uint64(i)*width, uint64(i+1)*width))
		require.NoError(t, p.TransitionRecovery(segID, logtypes.RecoveryDurableVerified, "", ""))
		require.NoError(t, p.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer, "", "projection:chain"))
	}
	require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "TEST_STALL: backlog over limit (fixture)"))
	p.SetDelivery(pipeline.FuncHook(func([]logtypes.Event, bool) (pipeline.DeliveryResult, error) {
		return pipeline.DeliveryResult{}, errors.New("stub: VL 不可用（本用例只考滚动回收）")
	}))
	return m, src, p
}

// TestRollingReclaimEatsSegmentChainWithinStepCap（#64-③）：
//   - **N=32 段链** ⇒ 单轮 ≤32 步必须吃完（每段一跳 ✓）；
//   - **N=48 段链** ⇒ 第一轮**如实打满**（`steps == 32` ⇒ 现场据此可辨 ✗✓），第二轮接着吃完 ✓；
//   - **配置面**：`Options.ReclaimRoll{MaxSteps: 64}` ⇒ N=48 段链**单轮**吃完（热调生效 ✓）。
func TestRollingReclaimEatsSegmentChainWithinStepCap(t *testing.T) {
	const width = uint64(100_000)

	// ① N=32：单轮吃完（≤32 步 ✓）。
	m32, src32, p32 := newReclaimChainFixture(t, 32, nil)
	m32.selfHealPausedSource(src32, p32)
	require.Equal(t, 32*width, reclaimOf(t, p32), "32 段链必须在一个单轮内吃完（≤32 步 ✓）")
	st32 := m32.ReclaimRollingStats()
	require.LessOrEqual(t, st32.Last.Steps, 32)
	t.Logf("#64 观测（N=32）：steps=%d firstAdvance=%d advance=%d elapsed=%s",
		st32.Last.Steps, st32.Last.FirstAdvance, st32.Last.Advanced, st32.Last.Elapsed)

	// ② N=48：第一轮如实打满，第二轮吃完。
	m48, src48, p48 := newReclaimChainFixture(t, 48, nil)
	m48.selfHealPausedSource(src48, p48)
	st48 := m48.ReclaimRollingStats()
	t.Logf("#64 观测（N=48 第一轮）：steps=%d advance=%d reclaim=%d（链总长 %d）",
		st48.Last.Steps, st48.Last.Advanced, reclaimOf(t, p48), 48*width)
	require.Equal(t, 32, st48.Last.Steps, "第一轮必须**如实**打满步数上界（现场可辨 ✓）")
	// 诚实口径：滚动循环自己推进 32 段（= 步数上界 ✓）；随后 `selfHealPausedSource` 步 3 的
	// `EvaluateResume`（其内部也调一次 TryReclaim ✓）会再推进最多一段 ⇒ 落在 [32w, 48w) 即正确 ✓。
	require.GreaterOrEqual(t, reclaimOf(t, p48), 32*width, "至少要走满步数上界（部分推进 ✓）")
	require.Less(t, reclaimOf(t, p48), 48*width, "第一轮不得吃完（48 段 > 32 步 ⇒ 必然分轮 ✓）")
	m48.selfHealPausedSource(src48, p48)
	require.Equal(t, 48*width, reclaimOf(t, p48), "第二轮必须接着吃完（不饿死 ✓）")

	// ③ 配置面：Options.ReclaimRoll{MaxSteps: 64} ⇒ N=48 单轮吃完（现场热调免发版 ✓）。
	m64, src64, p64 := newReclaimChainFixture(t, 48, func(o *Options) {
		o.ReclaimRoll = &ReclaimRollTuning{MaxSteps: 64}
	})
	m64.selfHealPausedSource(src64, p64)
	require.Equal(t, 48*width, reclaimOf(t, p64), "配置面提到 64 步后，48 段链必须单轮吃完（旋钮真的生效 ✓）")
}
