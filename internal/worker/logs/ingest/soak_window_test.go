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

	// 判据（2026-11-04 重写，同日再修正一次）：**堆随 WAL 积压涨不算泄漏**。
	//
	// # 为什么不能直接拿「后 1/3 / 前 1/3 ≤ 1.5」当判据
	//
	// WAL 是**有界缓存**：只要后台投递跑得比写入慢一点，积压就会震荡上升，堆跟着涨——
	// 这是设计行为，不是泄漏。拿裸堆做比值判据会把「积压震荡」全部误报成慢泄漏。
	//
	// # 为什么又不能只留「单位积压成本」（同日实跑后发现的自身缺陷）
	//
	// 本窗是 1 行/s/源 的低速长稳（对齐长时间作业的真实节奏），WAL 积压一直只有
	// **0.06–0.09 MiB**（300 轮也才 1 条/源左右）。这个量级下「单位积压成本」的
	// 分母近乎噪声，无论怎么算都在几十~几百之间跳。
	//
	// 更糟的是：如果给分母加一个下限 floor，那两者就变成同一个常数 floor，
	// 比值退化成裸堆比值（实测：加 floor=0.1 时两边都是 X/0.1，比值与裸堆完全相同）——
	// 这个指标就完全失去诊断价值了。
	//
	// ⇒ 两个量必须**分别**验，各自在自己的量级上有意义：
	//   ① 裸堆形状：后 1/3 / 前 1/3 ≤ 1.5（低速窗里积压几乎不涨，裸堆就该稳；
	//      高速窗里它可以超，那时由②③负责解释）；
	//   ② 单位积压成本：仅在积压足够大（≥ minWALMiB）时才计算与断言，否则报 n/a；
	//   ③ 积压不得撞跌顶：撞顶说明投递长期跟不上（吞吐问题，非内存问题）。
	//
	// 历史读数（用于校准阈值）：
	//   - 900 轮加速窗（20ms 轮间隔）：堆 9.6→14.3（1.49），WAL 0.74→1.31，成本 13.0→10.9（0.84）
	//   - 1800 轮 × 1s 低速窗（本窗）：堆 5.7→6.1（1.08），WAL 0.07→0.06（几乎不积压）
	third := len(rssSamples) / 3
	require.Greater(t, third, 0, "轮数太少，无法分段比较")
	head, tail := rssSamples[:third], rssSamples[len(rssSamples)-third:]
	headWAL, tailWAL := walSamples[:third], walSamples[len(walSamples)-third:]
	headHeap, tailHeap := avg(head), avg(tail)
	headW, tailW := avg(headWAL), avg(tailWAL)
	t.Logf("堆前1/3=%.1f MiB  后1/3=%.1f MiB  比值=%.2f", headHeap, tailHeap, tailHeap/headHeap)
	t.Logf("WAL前1/3=%.3f MiB  后1/3=%.3f MiB", headW, tailW)

	// ② 单位积压成本：只在积压足够大时才有意义。
	// 门槛 0.3 MiB：低于这个量级时积压本身是噪声，相除只会放大噪声。
	const minWALMiB = 0.3
	if headW >= minWALMiB && tailW >= minWALMiB {
		headPer, tailPer := headHeap/headW, tailHeap/tailW
		t.Logf("单位积压堆成本：前=%.1f MiB/MiB  后=%.1f MiB/MiB  比值=%.2f", headPer, tailPer, tailPer/headPer)
		require.LessOrEqual(t, tailPer, headPer*1.5,
			"每 MiB WAL 积压的堆成本不得上升 1.5 倍（%.1f → %.1f MiB/MiB）", headPer, tailPer)
	} else {
		t.Logf("单位积压堆成本：n/a（积压两侧均需 ≥%.1f MiB，实测前=%.3f 后=%.3f；"+
			"本窗是低速窗，积压几乎不涨，该项不适用）", minWALMiB, headW, tailW)
	}
	t.Logf("poll 延迟 p50≈%.0f ms  max=%.0f ms",
		median(latencySamples), maxOf(latencySamples))
	t.Logf("总运行=%s  总轮数=%d  源数=%d", time.Since(started).Round(time.Second), rounds, sources)

	// ① 裸堆形状：低速窗里积压几乎不涨，堆就该稳；高速窗可放宽到只靠②解释。
	// 用「积压是否真的在涨」来选门限，避免拿一个阈值硬套两种节奏。
	walGrew := tailW > headW*2 && tailW > 0.1
	budget := 1.5
	if walGrew {
		budget = 3.0
		t.Logf("积压明显在涨（%.2f → %.2f MiB）⇒ 裸堆门限放宽到 %.1f（由积压解释）", headW, tailW, budget)
	}
	require.LessOrEqual(t, tailHeap, headHeap*budget,
		"堆增长超预算（%.1f → %.1f MiB，门限 %.1f）。积压几乎未涨时这只能意味着常驻增长",
		headHeap, tailHeap, budget)

	// ③ 跌顶上界：单源两条闸——2000 条（acquire.defaultWALMaxEntries）与 16 MiB
	// （defaultWALMaxBytes）。这里只验字节闸（条目数没样本）；跑满全窗后仍低于
	// 60×16MiB 才说明投递能跟上。
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
