package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是 2026-10-03 现场事故（**persistGate 门闩饿死采集轮**：SIGQUIT 8 个 goroutine 在
// persistCtx 的 Cond.Wait 上等 2+ 分钟，其中一条就是采集轮 ⇒ read_pos 冻结 + VL 零写入）的回归。
//
// 四条性质，全部按**事件序/结构**口径断言（不靠耗时阈值猜）：
//  1. 长链（恢复/重放）持续 incomplete 时，等待中的采集轮必须**在有界步数内**完成一次落库；
//  2. N 个普通等待者按**到达顺序**轮转（显式 FIFO），不被单个持门者压制；
//  3. 优先权：采集轮的落库先于普通队列里的恢复链被服务；
//  4. 老化防反饿：优先队列连续占用达阈值后必须放行一次普通等待者（否则反方向饿死）。

// gateLog 记录门事件序（acquire/release + 序号 + 档位）。
type gateLog struct {
	mu     sync.Mutex
	events []string
}

func (l *gateLog) hook(_ context.Context, event string, seq int64, priority bool) {
	tier := "normal"
	if priority {
		tier = "priority"
	}
	l.mu.Lock()
	l.events = append(l.events, fmt.Sprintf("%s:%d:%s", event, seq, tier))
	l.mu.Unlock()
}

func (l *gateLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// newGateFixture 造一个「有长差异待落库」的 Manager + 采集源。
//
// 长差异用手注入的 WAL 行制造（与 D12/E3 同型）：2000 行 × CycleMaxRows=16 ⇒ 需要 100+ 步，
// 足以让「长链」跑很久；同一 Manager 上另有一个健康源参与采集。
func newGateFixture(t *testing.T, injected int, cycleRows int) (*Manager, string) {
	t.Helper()
	client, _ := newProjectionVL(t)
	// **自管目录**（不用 t.TempDir）：实测偶发红**不是断言失败**，而是 `t.TempDir` 的 RemoveAll 与
	// "后台持久化通道仍在写 var/log"竞争 ⇒ 清理报 `directory not empty` 直接把用例判红 ✗✗。
	// 该竞争与"门公平性"毫无关系，故收尾改为自管目录：先 Stop（确定性停掉后台），再尽力删除
	// （失败不判红 ✓）。用例只对**被测性质**负责，不为清理时序背锅 ✓。
	root, rootErr := os.MkdirTemp("", "jm-gate-fixture-")
	require.NoError(t, rootErr)
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: seed one\n[12:00:02] [Server thread/INFO]: seed two\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:gate", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:gate", UTCDay: runtimeTestUTCDay(),
		}},
		IndexCommit: &stateindex.CommitBudget{CycleMaxRows: cycleRows},
	})
	require.NoError(t, err)
	m.pollOnce()
	const key = "inst:gate/g1"
	pipe := m.pipes[key]
	require.NotNil(t, pipe)
	identity := logtypes.SourceIdentity{LogSourceID: "inst:gate", SourceGeneration: "g1", ParserVersion: "v1"}
	seed := make([]acquire.WALEntry, 0, injected)
	for i := 0; i < injected; i++ {
		end := uint64(1_000_000 + i*10)
		seed = append(seed, acquire.WALEntry{
			Seq: uint64(i + 1),
			Event: logtypes.BuildEvent(identity, logtypes.RecordRange{Start: end - 9, End: end},
				"2026-09-22T10:00:00Z", "2026-09-22T10:00:00Z", "INFO", "stdout", fmt.Sprintf("gate %d", i)),
			Appended: true, Durable: true,
		})
	}
	require.NoError(t, pipe.WAL().Restore(seed))
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "seed"))
	require.NoError(t, pipe.Ledger().ResumeAcquire(pipe.Key()))
	// **确定性收尾**（2026-10-03 实测的偶发红根因）：不 Stop 就交给 t.TempDir 清理 ⇒ 后台
	// 持久化通道仍在写 var/log ⇒ 清理报 "directory not empty" ✗（实测 1/3 概率，且**不是断言失败**，
	// 与"门公平性"毫无关系）。注册 Stop 后清理顺序确定 ✓。
	t.Cleanup(func() {
		_ = m.Stop()           // 确定性停掉后台（持久化通道/对账/容量兜底）
		_ = os.RemoveAll(root) // 尽力删除；清理竞争不判红 ✓（见上方自管目录注释）
	})
	return m, key
}

// TestWaitingCollectorCompletesUnderLongChain（性质 1）：普通档（恢复/重放链）持续 incomplete 时，
// 等待中的**优先档**（采集轮）必须能在有界步数内拿到门并完成一次落库。
//
// 转红方式（实测）：把门的授予改成「不看队列、谁抢到算谁」（复刻改动前的 Broadcast+抢锁）——
// 长链每步只让 5ms 就回来夺门，优先等待者被持续压制，本用例在「有界步数内完成」处变红。
func TestWaitingCollectorCompletesUnderLongChain(t *testing.T) {
	m, _ := newGateFixture(t, 300, 16)
	log := &gateLog{}
	m.persistGateStage = log.hook

	// **按现场形态构造**（这一步是判别力的关键）：现场的长链是**紧循环**（恢复链一步接一步地
	// 重试，中间不让 CPU），而等待者（采集轮）在 Cond.Wait 上等。因此这里：
	//   - 把并行度压到 1：让「谁先拿到锁」由调度决定，而不是靠多核碰运气（避免假绿/假红 ✗）；
	//   - 长链**不让路**（紧循环）：复刻现场「持门者 5ms 回来一次」的形态。
	// 这样：修复后（显式队列 + 让路）优先等待者必然被授予 ✓；改成「谁抢到算谁」后它必然被压制 ✗。
	prevProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prevProcs)

	const chainSteps = 200
	chainDone := make(chan struct{})
	go func() {
		defer close(chainDone)
		for i := 0; i < chainSteps; i++ {
			if err := m.persistCtx(context.Background(), persistNormalOneStep); err != nil {
				return
			}
		}
	}()
	// 让长链先占住门（至少完成一步），再让采集轮来排队。
	require.Eventually(t, func() bool { return len(log.snapshot()) >= 1 }, 5*time.Second, time.Millisecond,
		"长链未开始（夹具未造出争用）")

	collectorSeq := m.persistJoin.Load() // 覆盖判据的基准：本次请求的序号
	// 夹具前提（结果语义，不依赖事件序里"恰好出现某个档位的 acquire" ✗）：长链必须先动起来。
	require.Eventually(t, func() bool { return len(log.snapshot()) >= 1 }, 5*time.Second, time.Millisecond,
		"夹具前提：长链必须先动起来（至少一步走完）")

	collectorDone := make(chan error, 1)
	started := time.Now()
	go func() { collectorDone <- m.persist() }() // 采集轮落库 = 优先档
	select {
	case err := <-collectorDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("等待中的采集轮落库在 10s 内没完成：被长链饿死（复刻现场 2+ 分钟）")
	}
	elapsed := time.Since(started)

	// 事件序断言：采集轮（优先档）必须**在长链后续步之间**被服务一次，而不是排在链尾。
	events := log.snapshot()
	priorityAcquired := -1
	normalAcquiredAfter := 0
	for index, event := range events {
		if strings.HasPrefix(event, "acquire:") {
			if strings.HasSuffix(event, ":priority") {
				if priorityAcquired < 0 {
					priorityAcquired = index
				}
				continue
			}
			if priorityAcquired < 0 {
				normalAcquiredAfter++
			}
		}
	}
	// **判据 = 结果语义**（用户指令）：等待中的采集轮落库"确实发生了"——两种见证任一即可 ✓：
	//   ① 拿到门被服务（事件序里出现优先档 acquire，且在长链结束之前 ✓）；
	//   ② 被某次提交**覆盖**（covered 越过它的序号 ⇒ 它的变更确实已落库 ✓，更快）。
	// 规划移到门外后 ② 成为常态；坚持"必须出现优先档 acquire"是在测实现细节 ✗（实测会假红）。
	servedByGate := priorityAcquired >= 0
	servedByCover := m.persistCovered.Load() > collectorSeq
	require.True(t, servedByGate || servedByCover,
		"等待中的采集轮落库必须确实发生（授予门 或 被提交覆盖；二者皆无为饿死 ✗），事件序=%v", events)
	require.Less(t, elapsed, 10*time.Second, "必须有界完成（实测 %s）", elapsed)
	if servedByGate {
		require.Less(t, priorityAcquired, len(events)-1,
			"若走授予门路径，必须在长链仍在跑的时候被服务（而不是排在链尾）")
	}
	_ = chainDone
	_ = normalAcquiredAfter

	// 显式停一次（夹具注册的 Stop 也会跑；这里先停 + 等后台写盘彻底静默）：
	// 精确定位过：偶发红其实**不是断言失败**，而是 t.TempDir 清理与"后台持久化通道仍在写 var/log"
	// 竞争（RemoveAll 报 directory not empty ✗）。先 Stop 再让出一次调度，清理顺序即确定 ✓。
	_ = m.Stop()
	time.Sleep(20 * time.Millisecond)
}

// TestPersistGateFairFIFO（性质 2）：多个普通等待者必须**按到达顺序**被服务（显式 FIFO），
// 不被单个持门者压制。
//
// 到达顺序 == `persistJoin` 分配序号（本用例用 2ms 间隔让到达顺序确定 ⇒ 序号顺序即到达顺序）。
//
// 转红方式（实测）：把授予改成 LIFO（授予队尾、复刻「谁最后来谁先走」的反 FIFO 形态）——
// 服务序会被逆转，本用例在「严格递增」处变红。
func TestPersistGateFairFIFO(t *testing.T) {
	m, _ := newGateFixture(t, 2000, 16)
	var mu sync.Mutex
	var served []int64
	// 等待者用带标记的 ctx 区分（长链也走同一条落库入口，靠 seq 区分不了——它每次调用都会拿新序号）。
	type waiterMark struct{}
	marker := waiterMark{}
	m.persistGateStage = func(ctx context.Context, event string, seq int64, priority bool) {
		if event != "acquire" || priority || ctx.Value(marker) == nil {
			return
		}
		mu.Lock()
		served = append(served, seq)
		mu.Unlock()
	}

	// 长链持门者（普通档，一步一让）+ 5 个普通等待者。
	stop := make(chan struct{})
	var chainWG sync.WaitGroup
	chainWG.Add(1)
	go func() {
		defer chainWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := m.persistCtx(context.Background(), persistNormalOneStep); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(20 * time.Millisecond) // 让长链先占门

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.persistCtx(context.WithValue(context.Background(), marker, true), persistNormalOneStep)
		}()
		time.Sleep(2 * time.Millisecond) // 依次到达
	}
	wg.Wait()
	close(stop)
	chainWG.Wait()

	mu.Lock()
	got := append([]int64(nil), served...)
	mu.Unlock()
	require.Len(t, got, 5, "5 个等待者都必须被服务（不得被长链饿死）")
	for i := 1; i < len(got); i++ {
		require.Less(t, got[i-1], got[i], "服务顺序必须严格按到达顺序（显式 FIFO），实测 %v", got)
	}
}

// TestPersistGatePriorityFirst（性质 3）：**后到的优先档**（采集轮/登记/解算）必须先于已排队的
// 普通等待者（恢复链）被服务——用户面延迟优先于恢复链吞吐。
//
// 转红方式（实测）：去掉「优先队列优先」的判定（按纯到达顺序服务）——优先等待者会排在 3 个普通
// 等待者之后，本用例在「优先档早于最后一个普通档」处变红。
func TestPersistGatePriorityFirst(t *testing.T) {
	m, _ := newGateFixture(t, 300, 16)
	var mu sync.Mutex
	var order []string
	// 普通等待者带标记（长链不带 ⇒ 被排除）；优先档无标记需求，全部记录。
	type waiterMark struct{}
	marker := waiterMark{}
	m.persistGateStage = func(ctx context.Context, event string, seq int64, priority bool) {
		if event != "acquire" {
			return
		}
		if !priority && ctx.Value(marker) == nil {
			return
		}
		tier := "normal"
		if priority {
			tier = "priority"
		}
		mu.Lock()
		order = append(order, fmt.Sprintf("%s:%d", tier, seq))
		mu.Unlock()
	}

	stop := make(chan struct{})
	var chainWG sync.WaitGroup
	chainWG.Add(1)
	go func() { // 普通档长链（持门者）
		defer chainWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := m.persistCtx(context.Background(), persistNormalOneStep); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(20 * time.Millisecond)

	// 先排 3 个普通等待者，再插一个优先等待者。
	var normalsWG sync.WaitGroup
	for i := 0; i < 3; i++ {
		normalsWG.Add(1)
		go func() {
			defer normalsWG.Done()
			_ = m.persistCtx(context.WithValue(context.Background(), marker, true), persistNormalOneStep)
		}()
		time.Sleep(2 * time.Millisecond)
	}
	priorityDone := make(chan error, 1)
	go func() { priorityDone <- m.persist() }()

	select {
	case err := <-priorityDone:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("优先档落库超时")
	}
	normalsWG.Wait()
	close(stop)
	chainWG.Wait()

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	// 断言口径（合并语义下「等待者被覆盖而无需取门」是合法的，故不按数量断言 ✗）：
	//   ① 至少有一个普通等待者被服务（说明确有排队竞争）；
	//   ② **优先档必须早于最后一个被服务的普通等待者**——这正是「优先权」的判别点：
	//      没有优先权时它会按到达顺序排在所有普通等待者之后（变异即在此转红）。
	priorityAt := -1
	lastNormalAt := -1
	normalsServed := 0
	for index, event := range got {
		if strings.HasPrefix(event, "priority:") {
			if priorityAt < 0 {
				priorityAt = index
			}
			continue
		}
		normalsServed++
		lastNormalAt = index
	}
	require.GreaterOrEqual(t, normalsServed, 1, "至少要有普通等待者被服务（否则夹具没造出排队），实测 %v", got)
	require.GreaterOrEqual(t, priorityAt, 0, "优先档必须被服务过，实测 %v", got)
	require.Less(t, priorityAt, lastNormalAt,
		"优先档必须**抢在已排队的普通等待者之前**被服务（优先权），实测 %v", got)
}

// TestPersistGateAgingPreventsReverseStarvation（性质 4）：优先档**连续占用**达阈值后必须放行一次
// 普通等待者——否则持续到达的采集轮会把恢复/重放链饿死（反方向饥饿 ✗）。
//
// 转红方式（实测）：设 `m.persistPriorityStreak = -1`（关闭老化，复刻「无反饿保护」）——
// 普通等待者永远排在持续到达的优先档之后，本用例在「有界优先授予次数内被服务」处变红。
func TestPersistGateAgingPreventsReverseStarvation(t *testing.T) {
	// 夹具要点：**一个长优先档调用**（采集轮那次「循环到做完」的落库：2000 行 / 16 行一步
	// ≈ 125 步）+ 一个普通等待者（恢复链）——优先档会在整段过程中反复重新排队（老化计数随之增长），
	// 这正是「持续占用优先档」的形态。
	// 并行度压到 1：让「谁先拿锁」由队列决定而不是靠多核碰运气（避免假绿/假红 ✗）。
	prevProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prevProcs)

	m, _ := newGateFixture(t, 2000, 16)
	m.persistPriorityStreak = 2 // 老化阈值压到 2：优先档连续 2 次后必须放行普通档

	var mu sync.Mutex
	priorityAcquires := 0
	normalServed := false
	m.persistGateStage = func(_ context.Context, event string, seq int64, priority bool) {
		if event != "acquire" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if priority {
			if !normalServed {
				priorityAcquires++
			}
			return
		}
		normalServed = true
	}

	priorityDone := make(chan error, 1)
	go func() { priorityDone <- m.persist() }() // 优先档长调用（采集轮形态）
	// 等优先档确实占过门（至少一次授予），再让普通等待者来排队。
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return priorityAcquires >= 1
	}, 5*time.Second, time.Millisecond, "优先档未开始（夹具未造出争用）")

	normalDone := make(chan error, 1)
	go func() { normalDone <- m.persistCtx(context.Background(), persistNormalOneStep) }()

	select {
	case err := <-normalDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		<-priorityDone
		t.Fatal("普通等待者在 10s 内没被服务（老化失效 ⇒ 反方向饿死）")
	}
	// 事件序：普通等待者必须**在优先档长调用结束之前**被服务过。
	select {
	case <-priorityDone:
		t.Fatal("夹具前提不成立：优先档长调用已经结束，测不到「长调用期间的放行」")
	default:
	}
	mu.Lock()
	acquires := priorityAcquires
	mu.Unlock()
	// 老化阈值 2 ⇒ 放行普通档之前至多 2 次优先授予（留一点余量给「已在飞的授予」）。
	require.LessOrEqual(t, acquires, 4,
		"老化必须让普通档在阈值附近的少量优先授予内通过（实测优先授予 %d 次）", acquires)
	<-priorityDone
	mu.Lock()
	require.True(t, normalServed)
	mu.Unlock()
}

// TestCollectionHotPathIsNonBlockingUnderGatePressure（架构级修复 ②）：**采集/投递侧不再等门**——
// 即使持久化门被一段"按住不放"的步骤长期占着，热路径也必须在有界时间内返回（把落库交给后台通道）。
//
// 现场形态（2026-10-03 二度复现）：SIGQUIT 显示采集轮与投递侧共 8 个调用方停在同一把 Cond 上等
// 1 分钟；根因是一次落库的**单个单元可以停在一分钟级的 SQL 语句上**（单元预算只在语句之间检查）
// ⇒ 「让路/优先」都救不了，只能**移除"采集等持久化"的依赖方向** ✗。
//
// 转红方式（实测）：把热路径改回阻塞式（`persistHot` 内部调用 `persist()`）——本用例在
// 「有界返回」处变红（它会一直等在门上）。
func TestCollectionHotPathIsNonBlockingUnderGatePressure(t *testing.T) {
	m, _ := newGateFixture(t, 500, 16)

	// 用一个测试钩子把门**确定性地按住**（模拟"单元停在一分钟级 SQL 语句上"）。
	hold := make(chan struct{})
	m.persistStepHold = func() { <-hold }

	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = m.persistCtx(context.Background(), persistNormalOneStep) // 持门者 = 同步一步落库（恢复步已改走热路径，不再整步占门） // 普通档：拿门 → 被钩子按住
	}()
	require.Eventually(t, func() bool {
		select {
		case <-m.persistRequests:
			return false
		default:
			return true
		}
	}, time.Second, time.Millisecond) // 让持门者先就位（它的 acquire 会走事件钩子，这里用极短等待即可）
	time.Sleep(30 * time.Millisecond)

	m.persistHotBudget = 50 * time.Millisecond
	started := time.Now()
	require.NoError(t, m.persistHot(), "热路径必须返回 nil（超时交给后台通道，而不是报错）")
	elapsed := time.Since(started)
	require.Less(t, elapsed, 2*time.Second,
		"采集/投递侧在门被按住时必须**有界返回**（实测 %s；阻塞式实现会一直等）", elapsed)
	require.Less(t, elapsed, 500*time.Millisecond,
		"热路径等待上界应当只有几十毫秒量级（预算 50ms + 调度余量），实测 %s", elapsed)

	// 交办后的落库由后台通道完成（最终一致）：见 TestAsyncHandoffDoesNotClaimCoverage 对
	// 「真落库后才推进覆盖水位」的断言；这里只保住"热路径有界返回"这一条性质。
	// （不再去消费 persistRequests 通道：后台 worker 也在消费它，直接读取会与它竞争 ⇒ 假红 ✗。）
	select {
	case <-holderDone:
		t.Fatal("夹具前提不成立：持门者已结束，测不到「门被按住」的形态")
	default:
	}
	close(hold)
	<-holderDone
}

// TestAsyncHandoffDoesNotClaimCoverage（架构级修复 ③）：交办**不得谎报覆盖**——
// 未真正落库前 `persistCovered` 不得推进；后台通道真正做完后才推进。
//
// 转红方式（实测）：在交办时直接推进 `persistCovered`（谎报"已覆盖"）——本用例在
// 「交办后覆盖水位不得前进」处变红。
func TestAsyncHandoffDoesNotClaimCoverage(t *testing.T) {
	m, _ := newGateFixture(t, 500, 16)

	hold := make(chan struct{})
	m.persistStepHold = func() { <-hold }
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = m.persistCtx(context.Background(), persistNormalOneStep) // 持门者 = 同步一步落库（恢复步已改走热路径，不再整步占门）
	}()
	time.Sleep(30 * time.Millisecond)

	m.persistHotBudget = 30 * time.Millisecond
	coveredBefore := m.persistCovered.Load()
	require.NoError(t, m.persistHot())
	coveredAfter := m.persistCovered.Load()
	require.Equal(t, coveredBefore, coveredAfter,
		"交办不等于落库：未真正落库前覆盖水位不得推进（否则静默丢更新）")

	// 放开门：后台通道与持门者都完成，覆盖水位最终推进 ✓（真落库才推进）。
	close(hold)
	require.Eventually(t, func() bool { return m.persistCovered.Load() > coveredBefore },
		20*time.Second, 20*time.Millisecond, "后台通道真正落库后，覆盖水位必须推进")
	<-holderDone
}
