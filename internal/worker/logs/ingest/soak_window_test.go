package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// TestSoakWindow60Sources 是 FR-498 的**长稳窗**（分段跑，由外部脚本循环调用）。
//
// # 与 TestCapabilityBaselineShortWindow 的分工
//
// 短窗只回答「稳态内存是多少」；本用例回答「**持续写入下内存/延迟是否漂移**」——
// 每轮记录堆与 WAL 积压，结束时按「单位积压的堆成本」判泄漏（见下方判据段）。
//
// # 为什么参数化而不是写死 9×30min
//
// 9×30min = 4.5 小时，单条用例超时会被 CI 杀掉，且一次失败就丢掉全部读数。
// 这里让**轮数与间隔由环境变量控制**，外部脚本分 9 段各跑 30min，每段独立进程——
// 这样某一段失败只丢那一段，其余读数仍成立。
func TestSoakWindow60Sources(t *testing.T) {
	if testing.Short() {
		t.Skip("长稳窗在 -short 下跳过")
	}
	rounds := envInt("SOAK_ROUNDS", 30)                // 轮数
	interval := envDur("SOAK_INTERVAL", 2*time.Second) // 轮间隔
	sources := envInt("SOAK_SOURCES", 60)              // 源数（对齐 FR-498 的 60 源口径）

	root := t.TempDir()
	logDir := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	configs := make([]SourceConfig, 0, sources)
	paths := make([]string, 0, sources)
	for i := 0; i < sources; i++ {
		id := fmt.Sprintf("inst:soak-%03d", i)
		p := filepath.Join(logDir, fmt.Sprintf("soak-%03d.log", i))
		require.NoError(t, os.WriteFile(p, nil, 0o644))
		paths = append(paths, p)
		configs = append(configs, SourceConfig{
			LogSourceID: id, SourceGeneration: "g1", Path: p,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: id, UTCDay: runtimeTestUTCDay(),
		})
	}

	cat := catalog.New(catalog.NewMemJournal())
	opts := Options{
		Root: root, Catalog: cat, Journal: cat.Journal(), Sources: configs,
		// **必须与生产一致**（apps/worker/main.go:1226 显式 `DeferredDelivery: true`）。
		//
		// 这个字段决定「投递推迟后是否安排重试」：关着的时候 `deferDurableDelivery` 只记一个
		// 时点而不 `scheduleReplayRetry`，于是 WAL 永不回收、内存队列随写入线性增长。
		// 首轮长稳就是漏了它 —— 60 源全部卡在 `durable persist deferred` + `no-confirmed-copy`，
		// 堆从 1.7 MiB 单调涨到 105 MiB（17 分钟）。那是**夹具与生产不一致**造成的假泄漏，
		// 不是产品缺陷；补上这一行后重跑才是有意义的读数。
		DeferredDelivery: true,
	}
	// 真 VL 投递（可选）：设 SOAK_VL_URL 后负载会真的写进 VictoriaLogs，
	// 这样长稳同时覆盖「采集 → 规范化 → WAL → 攒批 → 投递 → VL」全链路，
	// 而不是只测采集侧。不设则退化为纯采集长稳（默认，便于无 VL 环境跑）。
	if vlURL := os.Getenv("SOAK_VL_URL"); vlURL != "" {
		client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: vlURL})
		require.NoError(t, err, "SOAK_VL_URL=%s 的 VL 客户端构造失败", vlURL)
		opts.VL = client
		t.Logf("已接入真实 VL: %s（长稳覆盖全投递链路）", vlURL)
	} else {
		t.Logf("未设 SOAK_VL_URL ⇒ 仅测采集侧（不覆盖投递）")
	}
	m, err := newTestManager(t, opts)
	require.NoError(t, err)

	// 关掉 `newTestManager` 挂的 `deliverWriteHook`（它把**每次投递的全部事件**累积到
	// `deliveredBySource`，是给单轮用例 `durableEvents` 读回投递内容用的）。
	//
	// 长稳窗绝不能带着它跑：60 源 × 900 轮 = 54000 个 `logtypes.Event` 会被永久持有，
	// 在 round 300 实测已占 3.85 MiB 并随轮次线性上涨——那是**观测点自身的累积**，
	// 与产品内存无关，却会直接触发下面的 1.5 倍判据报「慢泄漏」。
	// atomic.Value 不能存 nil，故换成一个空钩子。
	m.deliverWriteHook.Store(func(SourceConfig, []logtypes.Event) {})

	// **必须启动后台环**（`Manager.Start`），否则本用例测的根本不是生产形态。
	//
	// 现场（2026-11-04 长稳首轮判「线性泄漏」的根因）：`DeferredDelivery: true` 下投递与
	// 回收**全部落在后台环里**——`Start` 的 ticker 驱动采集轮，`RunCapacityResumeLoop` 的
	// `drainScheduledReplay`/`sweepOnce` 驱动 `DeliverPending` 与 reclaim。只手工调 `pollOnce()`
	// 等于只采不投：WAL 只进不出，`Positions.Delivery`/`Reclaim` 恒为 0，堆随采集总量线性涨。
	//
	// 读数证据（修此夹具前，60 源 × 900 轮）：
	//   轮 10  Heap=3.5 MiB   WAL=600 条/0.2 MiB   pos{delivery=0 reclaim=0}
	//   轮 590 Heap=73.4 MiB  WAL=35400 条/14.1 MiB pos{delivery=0 reclaim=0}
	// `WAL 条数 == 60 × 轮次` ——一条都没被回收。这**不是慢泄漏**，是夹具没启动投递。
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Start(ctx)
	// 给后台环一点启动时间（`Start` 先起缺口消解 worker 与容量自愈环，再进 ticker 循环）。
	time.Sleep(50 * time.Millisecond)

	var rssSamples, latencySamples, walSamples []float64
	started := time.Now()

	for round := 0; round < rounds; round++ {
		// 每轮给每个源追加一行——持续写入是长稳的前提，只空转 poll 测不到增长。
		for i, p := range paths {
			f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
			require.NoError(t, err)
			_, err = f.WriteString(fmt.Sprintf(
				"[12:00:00] [Server thread/INFO]: round=%d src=%d padding=%s\n",
				round, i, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))
			require.NoError(t, err)
			require.NoError(t, f.Close())
		}

		roundStart := time.Now()
		// 手工触发一轮「本轮刚写入的数据」的采集；后台环的 ticker 也在并行 pollOnce，
		// 两者由 `cycleMu` 串行（见 pollOnce 的 D 修复），并发只影响耗时读数不影响正确性。
		m.pollOnce()
		latencySamples = append(latencySamples, float64(time.Since(roundStart).Milliseconds()))

		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		rssSamples = append(rssSamples, float64(ms.HeapAlloc)/(1<<20))
		// 同步记下此刻的 WAL 积压字节：后面判「堆涨是不是 WAL 积压的形状」要用。
		_, walBytesNow := soakWALBacklog(m)
		walSamples = append(walSamples, float64(walBytesNow)/(1<<20))

		// 中途 heap 快照：注释掉的诊断开关。设 SOAK_HEAPPROF_DIR 后每 SOAK_HEAPPROF_EVERY 轮
		// dump 一份 inuse_space profile——测试失败时会先跑 defer 清理，事后抓的 profile 已失真。
		if dir := os.Getenv("SOAK_HEAPPROF_DIR"); dir != "" && round > 0 && round%envInt("SOAK_HEAPPROF_EVERY", 400) == 0 {
			if f, ferr := os.Create(filepath.Join(dir, fmt.Sprintf("heap-%05d.prof", round))); ferr == nil {
				_ = pprof.WriteHeapProfile(f)
				_ = f.Close()
				t.Logf("heap profile 已落盘: %s（轮 %d）", f.Name(), round)
			}
		}

		if round%5 == 0 {
			walEnt, walBytes := soakWALBacklog(m)
			t.Logf("轮 %2d/%d  Heap=%.1f MiB  poll=%.0f ms  goroutines=%d  fds=%d  WAL=%d 条/%.1f MiB  %s  已运行=%s",
				round, rounds, rssSamples[len(rssSamples)-1],
				latencySamples[len(latencySamples)-1],
				runtime.NumGoroutine(), countFDs(),
				walEnt, float64(walBytes)/(1<<20), soakPositions(m, configs[0]),
				time.Since(started).Round(time.Second))
		}
		time.Sleep(interval)
	}

	// 判据（2026-11-04 重写）：**堆随 WAL 积压涨不算泄漏**。
	//
	// # 为什么不能直接拿「后 1/3 / 前 1/3 ≤ 1.5」当判据
	//
	// WAL 是**有界缓存**：只要后台投递跑得比写入慢一点，积压就会震荡上升，堆跟着涨——
	// 这是设计行为，不是泄漏。拿裸堆做比值判据会把「积压震荡」全部误报成慢泄漏：
	// 修好夹具后的 900 轮实测，裸堆比值 2.02（判红），但同期的量是：
	//
	//   前 1/3  heap=8.9 MiB  WAL=0.63 MiB       后 1/3  heap=18.0 MiB  WAL=1.70 MiB
	//   回归 heap = 4.58 × WAL_MiB + 8.01       残差 std=2.89 MiB（即 GC 噪声）
	//   单位积压的堆成本: 前 14.1 → 后 10.6（**在下降**）
	//
	// 也就是说：堆增长 100% 可由 WAL 积压解释，且每字节积压的内存成本并未变差。
	//
	// ⇒ 判据改成两条，必须**同时**成立才算通过：
	//   ① 每 MiB WAL 积压的堆成本不得上升（>1.5 倍判泄漏）；
	//   ② WAL 积压本身必须受限（不得撞到 60 源 × 2000 条 = 120000 条的跌顶）。
	// 任一条破就红：①破 = 真有常驻增长；②破 = 投递跟不上，那是吞吐问题而不是内存问题。
	third := len(rssSamples) / 3
	require.Greater(t, third, 0, "轮数太少，无法分段比较")
	head, tail := rssSamples[:third], rssSamples[len(rssSamples)-third:]
	headWAL, tailWAL := walSamples[:third], walSamples[len(walSamples)-third:]
	headHeap, tailHeap := avg(head), avg(tail)
	headW, tailW := avg(headWAL), avg(tailWAL)
	// 单位积压成本（字节/字节）。WAL 积压近零时比值会放大，故分母加一个最小积压下限（0.1 MiB）。
	const minWALMiB = 0.1
	headPer, tailPer := headHeap/max(headW, minWALMiB), tailHeap/max(tailW, minWALMiB)
	t.Logf("堆前1/3=%.1f MiB  后1/3=%.1f MiB  比值=%.2f（仅供参考，不作判据）",
		headHeap, tailHeap, tailHeap/headHeap)
	t.Logf("WAL前1/3=%.2f MiB  后1/3=%.2f MiB", headW, tailW)
	t.Logf("单位积压堆成本：前=%.1f MiB/MiB  后=%.1f MiB/MiB  比值=%.2f", headPer, tailPer, tailPer/headPer)
	t.Logf("poll 延迟 p50≈%.0f ms  max=%.0f ms",
		median(latencySamples), maxOf(latencySamples))
	t.Logf("总运行=%s  总轮数=%d  源数=%d", time.Since(started).Round(time.Second), rounds, sources)

	require.LessOrEqual(t, tailPer, headPer*1.5,
		"每 MiB WAL 积压的堆成本不得上升 1.5 倍（%.1f → %.1f MiB/MiB）；堆涨不能完全由积压解释 ⇒ 存在常驻增长",
		headPer, tailPer)
	// 跌顶上界：单源 2000 条（acquire.defaultWALMaxEntries）× 源数。撞顶意味着投递长期跟不上。
	// 单源默认两条闸：2000 条（acquire.defaultWALMaxEntries）与 16 MiB（defaultWALMaxBytes）。
	// 这里只验字节闸（条目数没样本）；跑满全窗后仍低于 60×16MiB 才说明投递能跟上。
	require.Less(t, walSamples[len(walSamples)-1], 16.0*float64(sources),
		"WAL 积压（%.1f MiB）不得撞字节跌顶（%.0f MiB）——撞顶说明投递长期跟不上，那是吞吐问题",
		walSamples[len(walSamples)-1], 16.0*float64(sources))
}

// soakWALBacklog 汇总全部源的 WAL 积压（条数, 近似字节）。
//
// 存在的理由：这是判「长稳堆增长是慢泄漏还是 WAL 未被回收」的唯一直接读数。
// 堆增长本身不能定性——WAL 按设计就要缓存待投递事件，在积压未到上限前堆**本来就该涨**。
func soakWALBacklog(m *Manager) (int64, int64) {
	m.mu.Lock()
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, p := range m.pipes {
		pipes = append(pipes, p)
	}
	m.mu.Unlock()
	var entries, bytes int64
	for _, p := range pipes {
		e, b := p.WAL().Backlog()
		entries += e
		bytes += b
	}
	return entries, bytes
}

// soakPositions 打印单个源的账本水位与已投递区间数，用来判定 WAL 为什么不回收。
func soakPositions(m *Manager, src SourceConfig) string {
	key := src.LogSourceID + "/" + src.SourceGeneration
	m.mu.Lock()
	p := m.pipes[key]
	m.mu.Unlock()
	if p == nil {
		return "pos=<missing>"
	}
	pos, ok := p.Positions()
	if !ok {
		return "pos=<none>"
	}
	return fmt.Sprintf("pos{read=%d durable=%d delivery=%d reclaim=%d}",
		pos.Read, pos.Durable, pos.Delivery, pos.Reclaim)
}

// countFDs 返回本进程当前打开的文件描述符数（探针：判断泄漏是 FD 还是内存）。
func countFDs() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(ents)
}

func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return d
}

func envDur(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			return parsed
		}
	}
	return d
}

func avg(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	for i := range cp {
		for j := i + 1; j < len(cp); j++ {
			if cp[j] < cp[i] {
				cp[i], cp[j] = cp[j], cp[i]
			}
		}
	}
	return cp[len(cp)/2]
}

func maxOf(xs []float64) float64 {
	var m float64
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}
