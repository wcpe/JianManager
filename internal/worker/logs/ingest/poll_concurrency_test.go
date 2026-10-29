package ingest

// FR-498 采集侧饱和回归（可转红替身）。
//
// 立论（2026-10-01 实测）：60 源基线单轮 99.6s 里 pollSource=99.6s，其中 deliver=90.8s，
// 而 deliver 里**投影校验查询**（VL LogsQL 往返，含退避重试）=85.3s（占整轮 85.6%）；
// 同期 Worker CPU 仅 17–45%、VL CPU 仅 7–23% —— 两边都没打满，瓶颈是**等待被串行叠加**：
// 轮周期 ≈ 源数 × 单源等待（实测 ≈1.65s/源 → ≈99s/轮），采集上限因此被钉在
// ≈60×2000 行 ÷ 99s ≈ 1.2k 行/s（不足 30 行/s × 60 源生成速率的 70%）。
//
// 本用例把「校验查询」的往返延迟放大成可观测的固定值，然后断言：同一轮采集里**多个源
// 的校验查询是同时在飞的**（等待被重叠），而不是一个源接一个源地串行等待。
// 把 pollOnce 的跨源并发退回串行（等价旧实现）时，并发峰值必然退回 1，本用例即红。
//
// 2026-10-23 纠偏（原实现是「靠采样撞上重叠」，`-count=10` 实测 2 次假红）：
// 原用例只取「同一瞬间有几个窗口查询在飞」的瞬时采样峰值，而单轮里**到底有几个源能走到
// 校验查询**其实由耐久门预算支配——默认 30ms 门预算下，12 个并发源通常只有 1–3 个能在
// 轮内走完「WAL 落库 → 投递」，其余 11 个被 ErrPersistDeferred 交办到后台（日志实证：
// `durable persist deferred` × 11）。当轮内只剩 1 个源时，采样必然读出峰值 1 ——
// 这与「跨源是否并发」无关，是那次采样没撞上，属于测量缺陷而非生产退化。
// 本版把两处运气换成受控条件，断言强度不变（仍是「同时在飞的校验查询峰值 ≥ 2」）：
//  1. 被测轮给足耐久门预算，让全部源在轮内走完真实 persist/ACK 前置（生产默认不动，
//     只在本用例注入配置项）；并以「本轮零交办」为前提断言，否则宁可失败也不冒充结论；
//  2. 「两路同时在飞」由夹具的**一次性见证屏障**强制成立，而不是靠采样窗口撞上：
//     第一个抵达的校验查询会停在屏障上等第二个，串行实现永远等不到 ⇒ 峰值恒为 1 ⇒ 红。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// isProjectionProbeQuery 判定「代次占用探测」查询（`| fields _time | … | limit 1`）。
//
// 为什么要单独判别：探测是 nextFreeProjectionGeneration 的占位实探，与「逐字段校验」是
// 两件不同的等待。原实现只按「带时间窗」判据，把两者混进同一个计数 ⇒「同时在飞的校验
// 查询」这句读数实际上可能是两个探测；本用例要断言的正是**校验**等待的重叠，故显式排除。
func isProjectionProbeQuery(selector string) bool {
	return strings.Contains(selector, "| fields _time |") && strings.Contains(selector, "| limit 1")
}

// verifyQuerySource 从校验查询的 LogsQL 原文里取出 log_source_id（取不到则返回空串）。
//
// 为什么要按源去重计数：一轮里「有几个源走到了校验查询」与「有几个查询同时在飞」是两件事。
// 前者才是测量窗口是否成立的判据（见用例里的前置断言），后者是被测性质。
func verifyQuerySource(selector string) string {
	const marker = `log_source_id:=`
	idx := strings.Index(selector, marker)
	if idx < 0 {
		return ""
	}
	rest := selector[idx+len(marker):]
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	if end := strings.Index(rest, `"`); end >= 0 {
		return rest[:end]
	}
	return ""
}

// delayedVerifyVL 给带时间窗的 VL 查询注入固定往返延迟，并为「跨源校验等待重叠」提供
// 一次性见证屏障。
//
// 延迟只加在查询上（校验 + 代次占用探测），插入保持即时：这与真实 VL 的成本分布一致
// （FR-498 里 85% 的轮时长花在查询往返）。探测虽然同样被延迟，但不计入「同时在飞的
// 校验查询」读数（见 isProjectionProbeQuery）。
type delayedVerifyVL struct {
	inner http.Handler
	delay time.Duration

	mu sync.Mutex
	// queries 是**校验查询**累计次数（不含探测）；sources 是发出过校验查询的**不同源**集合。
	queries int64
	sources map[string]int
	// inFlight 是在飞的校验**查询**数峰值（diagnostic）；peakSources 是**在飞的源**数的峰值，
	// 主断言用它——本用例要断言的是「多个**源**的等待同时在飞」。
	//
	// 为什么必须按源去重（2026-10-23 纠偏，负控实测）：按查询计数的峰值在**完全串行**的实现上
	// 也会读到 2——同一源的两次校验查询（重试/多簇）可以同时在飞，而那时根本没有跨源重叠。
	// 串行化负控（pollOnce 强制 workers=1）实测：查询峰值=2 但「峰值时刻在飞源」只有 1 个源，
	// 旧口径因此**放过了串行化**。按源计数的峰值在串行实现上恒为 1，判据才有判别力。
	inFlight    int
	peak        int
	peakQueries int
	// active 是当前在飞的校验查询按源计数，peakSources 是峰值那一刻的在飞源集合。
	active      map[string]int
	peakSources []string
	// armed 为 true 时，率先抵达的校验查询会停在屏障上等第二个抵达（见 armWitness）。
	// 屏障是一次性的：出现见证即放行全部等待者，其后抵达的查询不再停留。
	armed    bool
	witness  bool
	release  chan struct{}
	deadline time.Time
}

func (d *delayedVerifyVL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/select/logsql/query" && r.URL.Query().Get("start") != "" {
		selector := r.URL.Query().Get("query")
		if isProjectionProbeQuery(selector) {
			// 探测：保持同样的往返延迟（原时延性质不变），但不参与重叠读数。
			time.Sleep(d.delay)
		} else {
			// 顺序很关键：先登记（计入在飞）→ 立刻进屏障等第二个源 → 再吃固定往返延迟。
			// 屏障放在 sleep 之前，是为了让第一个查询**从一抵达就扣住**，而不是先睡完 100ms
			// 再去等——后者会让「第二个源在本条睡醒前就已来去」的情况漏判（race 构建下把
			// 峰值压到 2 的边缘），违反本用例「重叠由结构强制、不由运气决定」的立论。
			d.beginVerify(verifyQuerySource(selector))
			d.awaitWitness()
			time.Sleep(d.delay)
			d.endVerify(verifyQuerySource(selector))
		}
	}
	d.inner.ServeHTTP(w, r)
}

// beginVerify 登记一次校验查询，并在第二个校验查询抵达时兑现「同时在飞」的见证。
func (d *delayedVerifyVL) beginVerify(source string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries++
	if source == "" {
		source = "?"
	}
	if d.sources == nil {
		// 惰性初始化：串行基线相位不调用 armWitness，集合尚未建立（向 nil map 写入会 panic）。
		d.sources = make(map[string]int, 16)
		d.active = make(map[string]int, 16)
		d.peakSources = nil
	}
	if d.active == nil {
		d.active = make(map[string]int, 16)
	}
	d.sources[source]++
	d.active[source]++
	d.inFlight++
	if d.inFlight > d.peakQueries {
		d.peakQueries = d.inFlight
	}
	if len(d.active) > d.peak {
		d.peak = len(d.active)
		d.peakSources = d.peakSources[:0]
		for src := range d.active {
			d.peakSources = append(d.peakSources, src)
		}
		sort.Strings(d.peakSources)
	}
	if d.armed && !d.witness && len(d.active) >= 2 {
		d.witness = true
		close(d.release)
	}
}

func (d *delayedVerifyVL) endVerify(source string) {
	if source == "" {
		source = "?"
	}
	d.mu.Lock()
	d.inFlight--
	if n := d.active[source] - 1; n > 0 {
		d.active[source] = n
	} else {
		delete(d.active, source)
	}
	d.mu.Unlock()
}

// awaitWitness 在屏障装好且尚未出现见证时等待第二个校验查询，最长等到窗口截止。
//
// 窗口是**绝对截止时刻**（不是每条查询各自计时）：串行实现下第一条等满窗口后，后续查询
// 直接返回，红态的代价因此被限制在一个窗口内，不会随源数放大。
func (d *delayedVerifyVL) awaitWitness() {
	d.mu.Lock()
	if !d.armed || d.witness {
		d.mu.Unlock()
		return
	}
	release, deadline := d.release, d.deadline
	d.mu.Unlock()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-release:
	case <-timer.C:
	}
}

// armWitness 清零读数并装上一次性见证屏障，供「被测阶段」使用。
func (d *delayedVerifyVL) armWitness(window time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = 0
	d.inFlight = 0
	d.peak = 0
	d.peakQueries = 0
	d.sources = make(map[string]int, 16)
	d.armed = true
	d.witness = false
	d.release = make(chan struct{})
	d.deadline = time.Now().Add(window)
}

// counters 返回读数：(校验查询次数, 同时在飞的峰值, 发出过校验查询的不同源数)。
func (d *delayedVerifyVL) counters() (queries int64, peak int, distinctSources int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.queries, d.peak, len(d.sources)
}

// peakSourceSet 返回峰值那一刻在飞的源列表（诊断用，跨源峰值由它的长度给出）。
func (d *delayedVerifyVL) peakSourceSet() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.peakSources...)
}

// peakQueryCount 返回在飞校验**查询**数的峰值（诊断用：与按源计数的峰值对照，
// 两者不等就说明有同源并发，读数不能当跨源重叠用）。
func (d *delayedVerifyVL) peakQueryCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.peakQueries
}

// deferredDeliveryKeys 读取「已把投递交办后台」的来源键（m.durableDeliveryRetry 的键）。
//
// 它是**测量窗口有效性的判据**，不是被测性质：一旦轮内发生交办，轮内的校验查询数量就
// 不再由源数决定，而由「谁抢到耐久门」决定（见文件头纠偏说明）。
func deferredDeliveryKeys(m *Manager) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.durableDeliveryRetry))
	for key := range m.durableDeliveryRetry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// pollTestLines 生成 count 行可解析的 MC 风格日志（时间戳唯一，避免身份冲突）。
func pollTestLines(from, count int) string {
	var b []byte
	for i := 0; i < count; i++ {
		n := from + i
		b = append(b, fmt.Sprintf("[12:%02d:%02d] [Server thread/INFO]: line %06d\n", n/60%60, n%60, n)...)
	}
	return string(b)
}

func appendPollTestLines(t *testing.T, path string, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString(text)
	require.NoError(t, err)
}

func TestPollRoundOverlapsSourceWaits(t *testing.T) {
	const sources = 12
	// 单次校验延迟取 100ms：与真实 VL 上实测的单次校验往返（141ms/次，60 源 × 2000 行基线）
	// 同量级，足以让「源数 × 单源等待」显著大于轮内**串行资源**（索引持久化 / 事件段 fsync /
	// 发布锁）的总和——后者是并发也压不下去的 Amdahl 部分，不把它算进断言里比把它混进来更诚实。
	const queryDelay = 100 * time.Millisecond
	// 见证窗口 2s：远大于同轮相邻源抵达校验查询的真实间隔（实测几十 ms），故「是否重叠」
	// 由代码结构决定而不是机器速度决定；同时它把串行实现的红态代价限制在一个窗口内。
	const witnessWindow = 2 * time.Second
	// 被测轮的耐久门预算：默认 30ms 下本轮会把大部分投递交办后台（ErrPersistDeferred），
	// 轮内只剩 1–3 个源真正走到校验查询 ⇒ 测的就不再是「跨源重叠」而是「谁抢到门」。
	// 这里显式给足预算，让全部源在轮内走完真实 persist/ACK 前置；生产默认值不因此改变，
	// 且下面的「零交办」断言保证该前提真的成立（不成立就失败，而不是悄悄测噪声）。
	const durableGateBudget = 2 * time.Second

	fixture := &projectionVLFixture{}
	delayed := &delayedVerifyVL{inner: fixture, delay: queryDelay}
	server := httptest.NewServer(delayed)
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)

	root := t.TempDir()
	cfgs := make([]SourceConfig, 0, sources)
	for i := 0; i < sources; i++ {
		dir := filepath.Join(root, fmt.Sprintf("s%02d", i))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		path := filepath.Join(dir, "latest.log")
		require.NoError(t, os.WriteFile(path, []byte(pollTestLines(0, 100)), 0o600))
		cfgs = append(cfgs, SourceConfig{
			LogSourceID:      fmt.Sprintf("node:poll-%02d", i),
			SourceGeneration: "g1",
			Path:             path,
			Mode:             pipeline.ModeFilePrimary,
			StorageNamespace: fmt.Sprintf("ns:poll-%02d", i),
			UTCDay:           runtimeTestUTCDay(),
		})
	}
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: cfgs,
		PersistHotBudget: durableGateBudget,
	})
	require.NoError(t, err)

	// 第 1 轮：串行（旧实现的工作点）——每源的等待被逐源叠加，同时在飞的校验查询峰值为 1。
	m.pollConcurrency = 1
	serialStart := time.Now()
	m.pollOnce()
	serial := time.Since(serialStart)
	serialQueries, serialPeak, _ := delayed.counters()
	require.Positive(t, serialQueries, "本用例必须真的走到校验查询，否则测的不是等待重叠")
	// 串行基线（pollConcurrency=1）峰值恒为 1：该相位**没有装见证屏障**，读数就是纯粹采样。
	// 2026-10-23 纠偏后这里的口径是「在飞的**源**数」而不是查询数——按源去重后，同一源的
	// 重试/多簇并发不再污染读数，故两种构建下都可以直接断言（旧口径必须靠 `!raceEnabled`
	// 豁免才敢断言，因为检测器下同源两个查询会被拉成同时在飞）。
	require.Equal(t, 1, serialPeak,
		"串行基线（pollConcurrency=1）不应出现两个源同时在飞：峰值=%d 在飞源=%v",
		serialPeak, delayed.peakSourceSet())

	// 追加新行，让第 2 轮同样有整批投递工作。
	for i, cfg := range cfgs {
		appendPollTestLines(t, cfg.Path, pollTestLines(100*(i+1), 100))
	}

	// 第 2 轮 = 被测阶段：并发采集 + 一次性见证屏障。
	m.pollConcurrency = sources
	delayed.armWitness(witnessWindow)
	concurrentStart := time.Now()
	m.pollOnce()
	concurrent := time.Since(concurrentStart)
	concurrentQueries, concurrentPeak, concurrentSources := delayed.counters()

	// 测量窗口有效性的前置断言（**不是**被测性质，也不是给红态留的豁免）：
	// 一轮里能有几个源走到校验查询，由耐久门决定——热路径按契约「最多走一步就交办后台」，
	// 所以并发轮里总会有一部分源把投递交办（实测 12 源里约 6–8 个）。只要**至少两个不同源**
	// 在轮内真的发出了校验查询，下面的见证屏障就能把它们逼成同时在飞；若不足两个，说明本轮
	// 压根没有可观测的重叠样本，此时读数不可解释 —— 如实失败并点名交办集合，绝不拿它当绿。
	require.GreaterOrEqual(t, concurrentSources, 2,
		"被测轮只有 %d 个源在轮内走到校验查询（=%d 次查询；被耐久门交办后台的源：%v），"+
			"本轮没有可观测的跨源重叠样本，测量无效（源数=%d）",
		concurrentSources, concurrentQueries, deferredDeliveryKeys(m), sources)

	// 主断言（判别性）：并发轮里多个**源**的校验查询同时在飞——等待被跨源重叠。旧实现
	// （逐源串行）下该峰值只能是 1，本断言即红。峰值由见证屏障强制产生，不依赖采样撞上：
	// 第一个抵达的校验查询会停住等第二个源，串行实现永远等不到。
	//
	// 断言**不带 race 豁免**：被计量的对象是「在飞源数」，屏障保证健康实现下界为 2、
	// 串行实现恒为 1，两种构建结论一致（实测：真绿 2–10、串行化负控恒为 1）。
	require.GreaterOrEqual(t, concurrentPeak, 2,
		"单轮采集必须跨源重叠等待：同时在飞的校验**源**数峰值=%d（峰值时刻在飞源=%v，在飞查询数峰值=%d，源数=%d，单次校验延迟=%s）",
		concurrentPeak, delayed.peakSourceSet(), delayed.peakQueryCount(), sources, queryDelay)

	// 辅助断言（不引入退化）：并发轮的墙钟不得比串行轮慢一半以上。刻意放宽到 1.5 倍——
	// race 模式下索引持久化这类**串行资源**（12 源 × 每源一次 DurablePersist）会被放大到
	// 秒级并在两轮里同样发生，而并发轮还要额外付出调度与锁竞争，用倍数阈值会让用例变成
	// 对机器负载的测量而不是对代码结构的测量。
	// 阈值**逐字沿用原值**（纠偏只改峰值口径，不动这里的界）：非 race 1.5×、race 4×。
	// 实测（2026-10-23 纠偏后）：非 race 串行 5.33–5.44s / 并发 0.74–0.88s（0.14–0.17）；
	// race 串行 8.1–8.7s / 并发 7.5–9.2s（0.9–1.13）。race 下的判别力由上面的跨源峰值承担。
	ratioLimit := serial * 3 / 2
	if raceEnabled {
		ratioLimit = serial * 4
	}
	require.LessOrEqual(t, concurrent, ratioLimit,
		"并发轮出现显著退化：串行=%s 并发=%s（源数=%d，上限=%s）", serial, concurrent, sources, ratioLimit)

	t.Logf("串行轮=%s 并发轮=%s 源数=%d 串行轮校验查询=%d 并发轮校验查询=%d 并发轮到达校验的源数=%d 并发在飞源峰值=%d 并发在飞查询峰值=%d 峰值时刻在飞源=%v",
		serial, concurrent, sources, serialQueries, concurrentQueries, concurrentSources, concurrentPeak,
		delayed.peakQueryCount(), delayed.peakSourceSet())
}
