package ingest

// #54 修复的**行为面**红/绿：deliver 的「规划段」重活（本批规范化 + 去重 / 内联身份集合重建）
// 必须发生在 m.mu **之外**。
//
// 现场（本分支 #54 专项）：deliver 内有三段 m.mu 临界区，其中
//   - 段 B（内联身份集合重建）：遍历 saved.Events 并在锁内逐条规范化 —— O(段行数)；
//   - 段 C（本批规划）：遍历**全批** events（零宽校验 + 规范化 + 去重）—— O(本批)，
//     现场单批 47 万–173 万条 ⇒ 实测持锁 1m09.5s（随批量线性）✗。
// 两者都**不读也不写任何共享状态**（只碰入参 events、本调用私有的 seen/toWrite；
// saved 本身已是锁内取的值拷贝）⇒ 该锁自初版（55ee3588）起就是"防御性但无保护对象"的锁。
//
// 本文件刻意**不依赖任何新增观测钩子**，因此在**未修改**的代码上就能跑出红：用一条独立的
// 「m.mu 可用性探针」goroutine 在 deliver 执行期间连续 TryLock(m.mu)，测量两个判别量：
//   - maxHold：连续不可获得的最长时长。锁内重活 ⇒ ≈ 整段重活耗时（实测段 C 646ms / 段 B 498ms）；
//     重活外移 ⇒ 只剩 O(1) 段（微秒–毫秒级，含调度抖动）。
//   - unavailableRatio：deliver 期间锁被占用的时间占比。锁内重活 ⇒ ≈ 1.0；外移 ⇒ ≈ 0。
//
// 为什么不用"成功获取次数"当判别量：探针是紧自旋，deliver 进入重活**之前**的 O(1) 段
// 就足以让它拿到上万次（实测 11619 次）⇒ 次数不具判别力，只作"探针确实在轮询"的活性下限。
// 而时长类判别量在两侧相差 3–4 个数量级（0.5s vs 微秒级），阈值留足余量即可稳定红/绿。

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

const (
	// deliverPlanProbeBatch 是探针批的事件条数：与现场单批 47 万–173 万条同量级的下限。
	// 单条规范化（EventTimeUTC 为空 ⇒ time.Parse + CanonicalContentHash）约 1–3 µs ⇒ 本批
	// 重活 ≈ 0.2–0.6s，足以把"锁内重活"钉在锁可用性读数上。
	deliverPlanProbeBatch = 200_000
	// deliverPlanProbeMinAcquires 是**探针活性**下限（不是判别量）：确认观测窗口内探针确实在轮询。
	deliverPlanProbeMinAcquires = 200
	// deliverPlanProbeMaxHold 是"连续不可获得 m.mu"的时长上限。只留 O(1) 段时实测微秒–毫秒级，
	// 这里留 50ms 余量吸收 GC/调度抖动（对照：锁内重活实测 0.5–0.65s，余量 ≥ 10×）。
	deliverPlanProbeMaxHold = 50 * time.Millisecond
	// deliverPlanProbeMaxUnavailableRatio 是 deliver 期间锁被占用时间占比的上限
	// （锁内重活 ⇒ 0.98–1.0；外移后 ⇒ 0.001 量级）。
	deliverPlanProbeMaxUnavailableRatio = 0.2
)

// mutexHoldProbe 是「m.mu 可用性探针」：以 TryLock 轮询测量锁的**可用性**（不改变任何语义）。
type mutexHoldProbe struct {
	m           *Manager
	stop        atomic.Bool
	acquires    atomic.Int64 // 成功获取次数（探针活性读数）
	maxHold     atomic.Int64 // 连续不可获得的最长时长（ns）
	unavailable atomic.Int64 // 连续不可获得时长的累计（ns，上界：可能把相邻两段合并）
	ready       chan struct{}
	done        chan struct{}
}

// startMutexHoldProbe 启动探针并等到它**真正开始轮询**（否则观测窗口会被启动延迟污染）。
func startMutexHoldProbe(m *Manager) *mutexHoldProbe {
	p := &mutexHoldProbe{m: m, ready: make(chan struct{}), done: make(chan struct{})}
	go p.loop()
	<-p.ready
	return p
}

func (p *mutexHoldProbe) loop() {
	var since time.Time      // 当前"连续不可获得"段的起点（零值 ⇒ 无未结算段）
	var lastFailAt time.Time // 最近一次 TryLock 失败的时刻
	first := true
	// settle 结算一段：d 为该段时长（ns）。
	settle := func(d int64) {
		p.unavailable.Add(d)
		if d > p.maxHold.Load() {
			p.maxHold.Store(d)
		}
		since = time.Time{}
	}
	closeStretch := func() {
		if since.IsZero() {
			return
		}
		settle(time.Since(since).Nanoseconds())
	}
	for !p.stop.Load() {
		if p.m.mu.TryLock() {
			p.m.mu.Unlock()
			if first {
				first = false
				close(p.ready)
			}
			p.acquires.Add(1)
			closeStretch()
			continue
		}
		// 获取失败 ⇒ 有人持锁：从这一刻起累计"连续不可获得"时长。
		lastFailAt = time.Now()
		if since.IsZero() {
			since = lastFailAt
		}
	}
	// 退出时结算仍未闭合的段：用**最后一次观测到失败的时刻**（而不是"现在"）结算，
	// 以免把探针自身的退出延迟误算成他人持锁 ⇒ 读数不依赖探针何时退出。
	if !since.IsZero() {
		settle(lastFailAt.Sub(since).Nanoseconds())
	}
	close(p.done)
}

// deliverPlanProbeReading 是一次探针观测的读数。
type deliverPlanProbeReading struct {
	acquires         int64
	maxHold          time.Duration
	unavailableRatio float64
	elapsed          time.Duration
	result           pipeline.DeliveryResult
	err              error
}

// measureDeliverLockAvailability 在探针观测下执行一次 deliver，返回锁可用性读数。
func measureDeliverLockAvailability(t *testing.T, m *Manager, src SourceConfig, batch []logtypes.Event) deliverPlanProbeReading {
	t.Helper()
	p := startMutexHoldProbe(m)
	start := time.Now()
	res, err := m.deliver(src, batch, false)
	elapsed := time.Since(start)
	// 必须先停探针（并等它结算完未闭合段）再读数：否则"deliver 刚返回、探针还没来得及
	// 观察到锁已释放"会把整段持锁漏掉 ⇒ 读数会依赖调度时序（红/绿都不可靠）。
	p.stop.Store(true)
	<-p.done
	reading := deliverPlanProbeReading{
		acquires: p.acquires.Load(), maxHold: time.Duration(p.maxHold.Load()),
		elapsed: elapsed, result: res, err: err,
	}
	if elapsed > 0 {
		reading.unavailableRatio = float64(p.unavailable.Load()) / float64(elapsed)
	}
	return reading
}

// requireHeavyWorkOutsideMutex 断言「重活不在锁内」：占比与最长连续持锁两个判别量都必须过。
func requireHeavyWorkOutsideMutex(t *testing.T, section string, r deliverPlanProbeReading) {
	t.Helper()
	require.GreaterOrEqual(t, r.acquires, int64(deliverPlanProbeMinAcquires),
		"%s：探针活性（观测窗口内必须真的在轮询）：实测仅 %d 次", section, r.acquires)
	require.Less(t, r.maxHold, deliverPlanProbeMaxHold,
		"%s：最长连续不可获得 m.mu 必须远小于重活耗时（锁内重活 ⇒ ≈ 重活耗时）", section)
	require.Less(t, r.unavailableRatio, deliverPlanProbeMaxUnavailableRatio,
		"%s：deliver 期间 m.mu 被占用时间占比必须接近 0（锁内重活 ⇒ ≈1.0）", section)
}

// newDeliverPlanProbeManager 构造只够 deliver 走到「规划段」的最小 Manager（不落盘、不起 goroutine）：
//   - m.events == nil ⇒ 段存储不可用 ⇒ 走**全量慢路径**（段 B 内联集合 + 段 C 全批规划都要跑）；
//   - vlRoute 是固定失败的桩：本批以**零宽事件收尾** ⇒ 规划段处理完前 n 条后立即以 422 返回，
//     不进入任何写入路径（VL/段存储/缺口消解全不触达）⇒ 用例只测"规划段的持锁"。
func newDeliverPlanProbeManager() *Manager {
	return &Manager{
		vlRoute: func(SourceConfig) (*vlsup.Client, bool, error) {
			return nil, false, errors.New("probe: 本用例不触达 VL 客户端")
		},
		state: persistedState{Sources: map[string]persistedSource{}},
		pipes: map[string]*pipeline.Pipeline{},
	}
}

// deliverPlanProbeIdentity 是探针事件的源身份。
func deliverPlanProbeIdentity() logtypes.SourceIdentity {
	return logtypes.SourceIdentity{LogSourceID: "probe", SourceGeneration: "g1", ParserVersion: "v1"}
}

// deliverPlanProbeEvents 构造 n 条**需要真正规范化**的事件：EventTimeUTC 为空 ⇒ 每条都走
// time.Parse + CanonicalContentHash（这正是现场 47 万–173 万条批次的单位成本）。
// EventID 各不相同 ⇒ 去重表按真实形态增长（不做"全部重复"的取巧压缩）。
func deliverPlanProbeEvents(n int) []logtypes.Event {
	identity := deliverPlanProbeIdentity()
	out := make([]logtypes.Event, 0, n)
	for i := 0; i < n; i++ {
		rec := logtypes.RecordRange{Start: uint64(i*10 + 1), End: uint64(i*10 + 10)}
		out = append(out, logtypes.Event{
			EventID: logtypes.EventID(identity, rec),
			Source:  identity, Record: rec,
			IngestTimeUTC: "2026-10-05T00:00:00.000000000Z",
			Level:         "INFO", Stream: "stdout", Message: "probe line",
		})
	}
	return out
}

// deliverPlanZeroWidthEvent 是一条零宽事件（Record.End <= Record.Start）：规划段遇它即以 422
// 返回（状态码与错误文本是契约的一部分，见下方断言）。
func deliverPlanZeroWidthEvent() logtypes.Event {
	return logtypes.Event{
		EventID: "probe-zero-width", Source: deliverPlanProbeIdentity(),
		Level: "INFO", Stream: "stdout", Message: "zero width",
	}
}

// TestDeliverBatchPlanRunsOutsideMutex 是**段 C**（本批规范化 + 去重，现场单批 47 万–173 万条、
// 实测持锁 1m09.5s）的红/绿：
//
// 红（未修改代码）：整段重活在 m.mu 内 ⇒ 锁在 0.6s 的重活期**连续不可获得**（占比 ≈ 1.0）⇒
// 两个判别量都转红（并与生产打点 `m.mu 慢持锁 section=deliver.plan hold=646ms` 相互印证）。
// 绿（修复后）：重活移到锁外 ⇒ 重活期锁自由 ⇒ 判别量降到"只剩 O(1) 段"的水平。
func TestDeliverBatchPlanRunsOutsideMutex(t *testing.T) {
	m := newDeliverPlanProbeManager()
	src := SourceConfig{LogSourceID: "probe", SourceGeneration: "g1"}
	// 前 200k 条需要真正规范化，末条零宽 ⇒ 规划段跑完重活后立即 422（不写任何东西）。
	batch := append(deliverPlanProbeEvents(deliverPlanProbeBatch), deliverPlanZeroWidthEvent())

	r := measureDeliverLockAvailability(t, m, src, batch)
	t.Logf("段 C 观测：本批 %d 条 · deliver 用时 %s · 探针获取 m.mu %d 次 · 最长连续不可获得 %s · 被占用占比 %.4f",
		len(batch), r.elapsed, r.acquires, r.maxHold, r.unavailableRatio)

	// 前置：必须真的走进规划段（否则测的不是这条路径）；零宽收尾的状态码与错误文本是契约。
	require.Equal(t, http.StatusUnprocessableEntity, r.result.HTTPStatus, "零宽事件必须以 422 结束（错误：%v）", r.err)
	require.ErrorContains(t, r.err, "zero-width event")
	requireHeavyWorkOutsideMutex(t, "段 C（本批规范化 + 去重）", r)
}

// TestDeliverInlineIdentityRebuildRunsOutsideMutex 是**段 B**（旧格式/段未落时逐条遍历内联
// saved.Events 重建身份集合）的红/绿：同一探针手法，重活源换成内联切片。
//
// 红（未修改代码）：saved.Events 的遍历与规范化整段在 m.mu 内 ⇒ 判据同上转红。
// 绿（修复后）：锁内只留 O(1) 值拷贝快照（切片头 + 布尔），遍历移到锁外。
//
// 附带观察（本用例的现场价值）：段 B 的持锁**不在任何 watchMutexHold 观测段内** ⇒
// "候选 2"的现场读数看不到它；本用例把它变成可测。
func TestDeliverInlineIdentityRebuildRunsOutsideMutex(t *testing.T) {
	m := newDeliverPlanProbeManager()
	src := SourceConfig{LogSourceID: "probe", SourceGeneration: "g1"}
	key := src.LogSourceID + "/" + src.SourceGeneration
	// 内联事件（EventsStored=false 的旧格式/段未落形态）：权威集合在该切片里。
	inline := deliverPlanProbeEvents(deliverPlanProbeBatch)
	m.mu.Lock()
	m.state.Sources[key] = persistedSource{Events: inline}
	m.mu.Unlock()

	// 本批只放一条零宽事件：段 B 的重活跑完即 422 ⇒ 不触达 VL/段存储。
	batch := []logtypes.Event{deliverPlanZeroWidthEvent()}
	r := measureDeliverLockAvailability(t, m, src, batch)
	t.Logf("段 B 观测：内联集合 %d 条 · deliver 用时 %s · 探针获取 m.mu %d 次 · 最长连续不可获得 %s · 被占用占比 %.4f",
		len(inline), r.elapsed, r.acquires, r.maxHold, r.unavailableRatio)

	require.Equal(t, http.StatusUnprocessableEntity, r.result.HTTPStatus, "零宽事件必须以 422 结束（错误：%v）", r.err)
	require.ErrorContains(t, r.err, "zero-width event")
	requireHeavyWorkOutsideMutex(t, "段 B（内联身份集合重建）", r)
}

// 说明（可转红记录）：本文件的两个用例在**未修改**的 deliver 上实测红（读数为
// 段 C 495.61491ms / 段 B 408.605382ms 的最长连续不可获得，被占用占比 0.9996 / 0.9897）；
// 修复后转绿（1.163µs / 0s，占比 0.0000）。若把重活重新挪回 m.mu（等价于撤销 #54），
// 两者立即再次转红——这就是"绿"不是靠阈值巧合、而是靠"重活是否持锁"这一结构性事实的直接证据。

// TestPlanDeliveryBatchOutsideMutex 是 #54 的**结构性**红/绿（与 A-Step2 的
// TestBuildWALRowsOutsideLock 同型）：用 Manager.deliverPlanHook 断言"本批规范化 + 去重"
// 发生在锁外，并用负例证明该断言**具备红的敏感性**。
//
// 绿（修复后）：锁外调用 ⇒ 钩子处的 TryLock 成功（未持锁）；
// 负例（红敏感性）：持锁调用同一函数 ⇒ 钩子必须观察到"已持锁"。
func TestPlanDeliveryBatchOutsideMutex(t *testing.T) {
	m := &Manager{}
	var held atomic.Bool
	m.deliverPlanHook.Store(func() {
		if m.mu.TryLock() {
			m.mu.Unlock()
			return
		}
		held.Store(true)
	})

	identity := deliverPlanProbeIdentity()
	newEvent := func(id string, start, end uint64, message string) logtypes.Event {
		return logtypes.Event{
			EventID: id, Source: identity, Record: logtypes.RecordRange{Start: start, End: end},
			IngestTimeUTC: "2026-10-05T00:00:00.000000000Z", Level: "INFO", Stream: "stdout", Message: message,
		}
	}
	events := []logtypes.Event{
		newEvent("e1", 1, 9, "a"),
		newEvent("e2", 10, 19, "b"),
		newEvent("e3", 20, 29, "c"),
	}
	seen := map[string]string{}
	toWrite, fail := m.planDeliveryBatchOutsideLock(events, seen)
	require.Nil(t, fail)
	require.False(t, held.Load(),
		"#54：本批规范化 + 去重不得在 m.mu 内（现场单批 47万–173万条曾持锁 1m09.5s）")

	// 契约：追加序 = 输入序（FIFO），且规范化必须落到 toWrite 的事件体上。
	require.Len(t, toWrite, 3)
	for i, want := range []string{"e1", "e2", "e3"} {
		require.Equal(t, want, toWrite[i].EventID, "追加序必须严格等于输入序（第 %d 条）", i)
	}
	require.NotEmpty(t, toWrite[0].EventTimeUTC, "规范化必须补齐 EventTimeUTC")
	require.Equal(t,
		logtypes.CanonicalContentHash(toWrite[0].EventTimeUTC, "INFO", "stdout", "a"),
		toWrite[0].CanonicalHash, "规范化必须补齐 canonical 内容哈希")
	// 契约：去重表按本次投递结果推进（同批内后续重复才能被识别）。
	require.Equal(t, map[string]string{"e1": toWrite[0].CanonicalHash, "e2": toWrite[1].CanonicalHash, "e3": toWrite[2].CanonicalHash}, seen)

	// 负例（红敏感性）：持锁调用 ⇒ 钩子必须观察到"已持锁"。
	held.Store(false)
	m.mu.Lock()
	_, fail = m.planDeliveryBatchOutsideLock(events, map[string]string{})
	m.mu.Unlock()
	require.Nil(t, fail)
	require.True(t, held.Load(), "负例：持锁调用时钩子必须观察到已持锁（证明绿断言可红）")
}

// TestPlanDeliveryBatchContractUnchanged 守住 #54 外移后**逐字不变**的三条契约：
// 去重语义（同 ID 同哈希 ⇒ 跳过）、身份冲突（同 ID 异哈希 ⇒ 硬失败、status 0）、
// 以及两种 422 失败形态（零宽事件 / 规范化失败）。
func TestPlanDeliveryBatchContractUnchanged(t *testing.T) {
	m := &Manager{}
	identity := deliverPlanProbeIdentity()
	mk := func(id string, start, end uint64, message string) logtypes.Event {
		return logtypes.Event{
			EventID: id, Source: identity, Record: logtypes.RecordRange{Start: start, End: end},
			IngestTimeUTC: "2026-10-05T00:00:00.000000000Z", Level: "INFO", Stream: "stdout", Message: message,
		}
	}

	// 1) 同 ID 同哈希 ⇒ 跳过（不重复写），其余保序。
	first, fail := m.planDeliveryBatchOutsideLock([]logtypes.Event{mk("e1", 1, 9, "a")}, map[string]string{})
	require.Nil(t, fail)
	dup := mk("e1", 1, 9, "a")
	dup.EventTimeUTC, dup.CanonicalHash = first[0].EventTimeUTC, first[0].CanonicalHash
	toWrite, fail := m.planDeliveryBatchOutsideLock(
		[]logtypes.Event{dup, mk("e2", 10, 19, "b")}, map[string]string{"e1": first[0].CanonicalHash})
	require.Nil(t, fail)
	require.Len(t, toWrite, 1, "同 ID 同哈希必须跳过（去重语义不变）")
	require.Equal(t, "e2", toWrite[0].EventID)

	// 2) 同 ID 异哈希 ⇒ IDENTITY_CONFLICT（硬失败，status 0）。
	//    注意：这里必须让事件**不带** event_time_utc（否则 normalize 会原样返回、沿用旧哈希，
	//    冲突根本不会被识别——那测的就不是这条分支）。
	conflict := mk("e1", 1, 9, "tampered")
	toWrite, fail = m.planDeliveryBatchOutsideLock([]logtypes.Event{conflict}, map[string]string{"e1": first[0].CanonicalHash})
	require.Nil(t, toWrite)
	require.NotNil(t, fail)
	require.Equal(t, 0, fail.result.HTTPStatus, "身份冲突必须保持 status 0（不是 422）")
	require.ErrorContains(t, fail.err, "IDENTITY_CONFLICT")

	// 3) 零宽事件 ⇒ 422 + 原文错误。
	toWrite, fail = m.planDeliveryBatchOutsideLock(
		[]logtypes.Event{{EventID: "zero", Source: identity, Level: "INFO", Stream: "stdout"}}, map[string]string{})
	require.Nil(t, toWrite)
	require.NotNil(t, fail)
	require.Equal(t, http.StatusUnprocessableEntity, fail.result.HTTPStatus)
	require.ErrorContains(t, fail.err, "zero-width event zero")

	// 4) 规范化失败（无 event_time_utc 且 ingest_time_utc 非法）⇒ 422。
	broken := mk("bad", 30, 39, "c")
	broken.IngestTimeUTC = "not-a-timestamp"
	toWrite, fail = m.planDeliveryBatchOutsideLock([]logtypes.Event{broken}, map[string]string{})
	require.Nil(t, toWrite)
	require.NotNil(t, fail)
	require.Equal(t, http.StatusUnprocessableEntity, fail.result.HTTPStatus)
	require.ErrorContains(t, fail.err, "has no stable event or ingest time")
}
