package ingest

// #58 的红/绿：`ResolveCoveredGapsForSource` 的 cycleMu 临界区不得包含重活（尤其 `m.persist()`）。
//
// 现场铁证（观测仪 #57 首条 Warn）：`cycleMu 慢持有 section=resolveGapsForSource hold=1m26.374992592s`
// ⇒ 它就是 resolve-gaps 探针 20/20 全忙（每次吃满 5s 预算）、45s 挂死形、dump 里
// `goroutine 3457 [sync.Mutex.Lock, 2 minutes]` 帧的**上游持有者**。
//
// 修前"持锁期间真正在干什么"（逐项，O 记号）：
//   1. m.mu 读 stopped / pipes[ns]                       O(1)
//   2. ctx.Err()                                          O(1)
//   3. Ledger().Get()（Entry.clone）                      O(缺口数 + 批次数 + 放弃凭据数)
//   4. 遍历 entry.Gaps 求 through                          O(缺口数)
//   5. AbandonGapsThrough（逐 gap 改写 + trim + merge）    O(缺口数)（账本写）
//   6. resumeAcquireRespectingGates → ResumeAcquire        O(缺口数)（账本写）
//   7. m.persist()  索引规划 + Apply + fsync + WAL 剪枝    O(全量状态) —— **1m26s 的主项**
// 修后：1 留下；3/4 外移（锁外重活）；5/6 是"换入最小必要状态"（留在复验后）；7 外移。
//
// 守卫充分性自证：resolve 判定读到的输入只有 ——(a) m.stopped、(b) m.pipes[ns] 身份、
// (c) 该源账本条目的 Positions.Read/Durable 与 Gaps[*].{Resolved,StartPos,EndPos}、
// (d) resumeAcquireRespectingGates 读的 Entry.AcquirePaused/PauseReason。
// 其中 (c)(d) 全在账本里，而 `Ledger.touch` 在**所有写路径的入口 `require()`** 内推进
// （ledger.go:1115-1119 的注释即为此设计）⇒ 任何账本写都会改变 `Ledger().Revision(key)` ✓；
// (a)(b) 由第三段的 stopped/管道身份复验覆盖 ✓。故 `rev` 守卫覆盖全部输入（无需补 coverSignature：
// 本函数不读 state 的段覆盖水位；`m.persist()` 读全量 state，但它是提交之后的独立落库步骤，
// 与同族路径 ResolveCoveredGaps 末尾的 m.persist() 同属"锁外落库" ✓）。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// resolvePersistFatten 是"把 persist 撑大"的时长：修前它整段在 cycleMu 内 ⇒ 探针必然读到 ≥该值；
// 修后 persist 在锁外 ⇒ 探针只读到 O(1) 守卫段（阈值余量 ≥8×）。
const resolvePersistFatten = 400 * time.Millisecond

// cycleMuResolutionProbe 是 **cycleMu 可用性外部探针**（不依赖被观测代码的自报读数）：
// 连续 TryLock 记录"最长连续不可获得"与累计不可获得时长。
type cycleMuResolutionProbe struct {
	m           *Manager
	stop        atomic.Bool
	maxHold     atomic.Int64
	unavailable atomic.Int64
	ready       chan struct{}
	done        chan struct{}
}

func startCycleMuResolutionProbe(m *Manager) *cycleMuResolutionProbe {
	p := &cycleMuResolutionProbe{m: m, ready: make(chan struct{}), done: make(chan struct{})}
	go func() {
		var since, lastFail time.Time
		first := true
		settle := func(d int64) {
			p.unavailable.Add(d)
			if d > p.maxHold.Load() {
				p.maxHold.Store(d)
			}
			since = time.Time{}
		}
		for !p.stop.Load() {
			if p.m.cycleMu.TryLock() {
				p.m.cycleMu.Unlock()
				if first {
					first = false
					close(p.ready)
				}
				if !since.IsZero() {
					settle(time.Since(since).Nanoseconds())
				}
				continue
			}
			lastFail = time.Now()
			if since.IsZero() {
				since = lastFail
			}
		}
		if !since.IsZero() { // 退出时用"最后一次观测到失败的时刻"结算（不把探针退出延迟算进持有）
			settle(lastFail.Sub(since).Nanoseconds())
		}
		close(p.done)
	}()
	<-p.ready
	return p
}

func (p *cycleMuResolutionProbe) finish() (time.Duration, time.Duration) {
	p.stop.Store(true)
	<-p.done
	return time.Duration(p.maxHold.Load()), time.Duration(p.unavailable.Load())
}

// TestResolveCoveredGapsForSourceHoldsCycleMuOnlyBriefly：
//   - 合成一个"persist 很慢"的解算（persistPlanHold 是既有测试钩子 ⇒ 确定性）；
//   - 断言：解算期间 cycleMu **最长连续不可获得 < 50ms**（修前 ≥400ms）+ 观测仪零慢持有；
//   - 同时守住语义不变量：缺口被放弃、凭据逐字（reason/operator/区间）、采集被恢复、无错误。
func TestResolveCoveredGapsForSourceHoldsCycleMuOnlyBriefly(t *testing.T) {
	m, _, key, p := newRebuildProbeManager(t)
	m.cycleMuProfile = true // #57 观测仪打开：用它的"慢持有"读数做第二条独立判据
	m.cycleMuWarnAt.Store(time.Now().UnixNano())
	// 把 persist 撑大 400ms（确定性）：修前这段整段落在 cycleMu 内（现场 1m26s 的形态）。
	m.persistPlanHold = func() { time.Sleep(resolvePersistFatten) }

	// 夹具：该源有一个未解决缺口 + 处于暂停态（放弃裁定 + 恢复采集都真的发生）。
	require.NoError(t, p.Ledger().RecordGap(p.Key(), 10, 20, "TEST_PERMANENTLY_LOST", "fixture gap"))
	require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "TEST_PAUSE fixture"))

	probe := startCycleMuResolutionProbe(m)
	start := time.Now()
	err := m.ResolveCoveredGapsForSource(context.Background(), key, "tester")
	elapsed := time.Since(start)
	maxHold, unavailable := probe.finish()

	require.NoError(t, err, "解算必须成功（语义不变量）")
	t.Logf("#58 观测：解算用时 %s（其中 persist 被撑大 %s）· cycleMu 最长连续不可获得 %s · 累计不可获得 %s · 慢持有计数 %d",
		elapsed, resolvePersistFatten, maxHold, unavailable, m.CycleMuSlowHoldCount())

	require.GreaterOrEqual(t, elapsed, resolvePersistFatten,
		"前置：本次解算确实包含一次被撑大的 persist（否则测的不是这条路径）")
	require.Less(t, maxHold, 50*time.Millisecond,
		"#58：解算的 cycleMu 持有不得包含 persist（修前整段在内 ⇒ 实测 ≥%s）", resolvePersistFatten)
	require.Zero(t, m.CycleMuSlowHoldCount(),
		"#57 观测仪判据：解算不得产生任何慢持有（>100ms）")

	// 语义不变量（逐位）：缺口被放弃 + 凭据逐字 + 采集恢复。
	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	require.Len(t, entry.Gaps, 1)
	require.True(t, entry.Gaps[0].Resolved, "缺口必须被放弃（Resolved=true）")
	require.Contains(t, entry.Gaps[0].Resolution, "PERMANENTLY_LOST by tester at", "凭据文案必须逐字不变")
	require.Equal(t, "PERMANENTLY_LOST", entry.Gaps[0].ReasonCode,
		"放弃凭据的 reason_code 覆盖 RecordGap 的原 reason（既有语义，逐位不变）")
	require.Equal(t, "tester", entry.Gaps[0].ResolvedBy)
	require.Len(t, entry.Abandonments, 1, "必须留下一条放弃凭据（审计面）")
	require.Equal(t, uint64(20), entry.Abandonments[0].To, "凭据区间取实际被放弃缺口的并集")
	require.Equal(t, "tester", entry.Abandonments[0].Operator)
	require.False(t, entry.AcquirePaused, "放弃后必须恢复采集（ResumeAcquire 语义不变）")
}

// TestResolveCoveredGapsForSourceDiscardsWhenSourceChangedOutsideLock：
// 守卫充分性的直接证据 —— 锁外计算期间该源账本又变（合成：并发 RecordGap 推高修订号）
// ⇒ 第三段复验必须**不写入任何变更**地返回明确错误（"变则弃"），缺口保持未解决。
func TestResolveCoveredGapsForSourceDiscardsWhenSourceChangedOutsideLock(t *testing.T) {
	m, _, key, p := newRebuildProbeManager(t)
	require.NoError(t, p.Ledger().RecordGap(p.Key(), 10, 20, "TEST_PERMANENTLY_LOST", "fixture gap"))

	// 在「锁外计算」窗口的**末尾**注入一次账本写（真实形态：采集轮/回收推进该源账本）。
	// 注入点用 #58 新增的仅测试观测点（在第二段末尾、第三段取锁前触发 ⇒ 窗口精确）。
	var fired atomic.Bool
	m.resolveGapsPlanHook = func() {
		if fired.CompareAndSwap(false, true) {
			_ = p.Ledger().AdvanceRead(p.Key(), 99)
		}
	}

	err := m.ResolveCoveredGapsForSource(context.Background(), key, "tester")
	require.Error(t, err, "锁外期间源已变化 ⇒ 必须拒绝写入并让调用方重试")
	require.Contains(t, err.Error(), "发生变化（未写入任何变更）")

	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	require.False(t, entry.Gaps[0].Resolved,
		"复验未过时**不得**写入任何变更：缺口必须保持未解决（防丢失更新）")
	require.Empty(t, entry.Abandonments, "不得留下任何放弃凭据")
}

// 变异记录（2026-10-05，逐字还原）：把第三段尾部的 `m.cycleMu.Unlock(); wc.done()` 与
// `return m.persist()` 换序（= 落库重新放进临界区，即修前形态）⇒ 首用例的 maxHold 断言与
// 慢持有计数断言同时转红（实测 ≥400ms），还原后转绿。
