package retention

import (
	"bufio"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件守 `/metrics` 这个**观测出口**：它不该因为驱动器为 nil 就消失，也不该因为
// 抓取与执行并发就把读数读坏（后者靠 ArchiveExecutor 的 RWMutex，回归在 -race 下跑）。

// metricsFixture 是只关心读数的夹具：1 个早已到点 + 1 个未到点。
//
// 刻意不写成 3 个分区：每轮 Planned=2、Moved=1、NotDue=1 的算式在并发用例里要用来
// 验证「累加没有丢」，个数越少断言越不容易跟着夹具漂移。
func metricsFixture() []PartitionRef {
	return []PartitionRef{
		{StorageNamespace: "inst:147", UTCDay: "20260101", Owner: "hot"}, // 早已到点
		{StorageNamespace: "inst:147", UTCDay: "20260930", Owner: "hot"}, // 年龄 1d，未到点
	}
}

// metricsNoopMover 是无状态搬运出口：并发用例只关心 Stats 的读写竞争，不关心搬到哪。
// 不复用 fakeMover 是有意的——它往 calls 切片里 append，把它放进并发用例会引入
// 另一个与本次改动无关的共享写入，读起来像是 Stats 的锁没管住。
func metricsNoopMover() Mover {
	return MoverFunc(func(_ context.Context, ns, day string) (string, error) {
		return ns + "-" + day, nil
	})
}

// exposition 是一次抓取解析后的视图（无 label，故样本是一对一的名字→值）。
type exposition struct {
	help  map[string]string
	kind  map[string]string
	value map[string]float64
	order []string
}

// 指标名合法性：Prometheus 的数据模型要求 [a-zA-Z_][a-zA-Z0-9_]*。
var metricNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// scrapeMetrics 打一次抓取，并按 Prometheus 文本格式**逐行**解析。
//
// 刻意手写解析而不是「strings.Contains 抽样」：格式错误（少一个 TYPE、值不是数字、
// 行尾缺换行）在真 Prometheus 上表现为整条抓取失败，抽样断言根本发现不了。
func scrapeMetrics(t *testing.T, d *Driver) (*httptest.ResponseRecorder, exposition) {
	t.Helper()
	return scrapeMetricsWithPrune(t, d, nil)
}

// scrapeMetricsWithPrune 是 scrapeMetrics 的可带回收读数变体：
// 回收指标必须有独立注入口，否则「未装配」与「已装配」两种路径只能验证到一种。
func scrapeMetricsWithPrune(t *testing.T, d *Driver, prune EventStorePruneSource) (*httptest.ResponseRecorder, exposition) {
	t.Helper()
	rec := httptest.NewRecorder()
	MetricsHandler(d, prune)(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics 必须 200，得到 %d", rec.Code)
	}
	body := rec.Body.String()
	if body == "" {
		t.Fatal("/metrics 不得返回空 body：未启用也要输出全零序列")
	}
	if !strings.HasSuffix(body, "\n") {
		t.Error("文本格式要求最后一行以换行结尾，否则抓取端会判为截断")
	}

	ex := exposition{help: map[string]string{}, kind: map[string]string{}, value: map[string]float64{}}
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			t.Errorf("不得有空行（抓取端按空白容忍，但空行通常意味着格式拼错了）：%q", line)
			continue
		}
		if !strings.HasPrefix(line, "# ") {
			// 样本行：`name value`（本端点刻意不产出 label）。
			fields := strings.Fields(line)
			if len(fields) != 2 {
				t.Errorf("样本行必须是「名字 值」两段（本端点无 label），得到 %q", line)
				continue
			}
			if !metricNameRE.MatchString(fields[0]) {
				t.Errorf("指标名不合法：%q", fields[0])
			}
			v, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				t.Errorf("样本值必须能按浮点解析，得到 %q（%v）", fields[1], err)
				continue
			}
			if _, dup := ex.value[fields[0]]; dup {
				t.Errorf("同一指标名重复出现：%q（无 label 时即为重复序列）", fields[0])
			}
			ex.value[fields[0]] = v
			ex.order = append(ex.order, fields[0])
			continue
		}
		// 元数据行：`# HELP name text` / `# TYPE name kind`。
		rest := strings.TrimPrefix(line, "# ")
		switch {
		case strings.HasPrefix(rest, "HELP "):
			name, text, ok := strings.Cut(strings.TrimPrefix(rest, "HELP "), " ")
			if !ok || !metricNameRE.MatchString(name) || text == "" {
				t.Errorf("HELP 行格式错误：%q", line)
				continue
			}
			ex.help[name] = text
		case strings.HasPrefix(rest, "TYPE "):
			parts := strings.Fields(strings.TrimPrefix(rest, "TYPE "))
			if len(parts) != 2 || !metricNameRE.MatchString(parts[0]) {
				t.Errorf("TYPE 行格式错误：%q", line)
				continue
			}
			if parts[1] != "counter" && parts[1] != "gauge" {
				t.Errorf("本端点只产出 counter/gauge，出现 %q", parts[1])
			}
			ex.kind[parts[0]] = parts[1]
		default:
			t.Errorf("无法识别的注释行：%q", line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读取 /metrics body 失败：%v", err)
	}

	// 三张表必须一一对应：有样本就要有 HELP 与 TYPE（缺 TYPE 时抓取端会当成 untyped），
	// 有 HELP/TYPE 也必须真有样本（只声明不产出等于骗人）。
	for name := range ex.value {
		if _, ok := ex.help[name]; !ok {
			t.Errorf("指标 %q 缺 HELP 行", name)
		}
		if _, ok := ex.kind[name]; !ok {
			t.Errorf("指标 %q 缺 TYPE 行", name)
		}
	}
	for name := range ex.help {
		if _, ok := ex.value[name]; !ok {
			t.Errorf("指标 %q 只声明了 HELP 却没有样本", name)
		}
	}
	for name := range ex.kind {
		if _, ok := ex.value[name]; !ok {
			t.Errorf("指标 %q 只声明了 TYPE 却没有样本", name)
		}
	}
	return rec, ex
}

// 契约：内容类型必须带版本号与 UTF-8（HELP 是中文）。
func TestMetricsHandlerContentType(t *testing.T) {
	rec, _ := scrapeMetrics(t, nil)
	if got := rec.Header().Get("Content-Type"); got != promTextContentType {
		t.Fatalf("Content-Type 应为 %q，得到 %q", promTextContentType, got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "version=0.0.4") {
		t.Fatalf("必须声明文本格式版本，得到 %q", got)
	}
}

// 契约：驱动器为 nil（保留策略未启用）时，八个序列一个都不能少，值全为 0。
//
// 这是本端点最容易被「顺手优化」掉的一条：nil 时直接 404 或返回空 body 会让抓取侧
// 无从区分「策略没开」与「worker 挂了」。
func TestMetricsHandlerNilDriverEmitsZeros(t *testing.T) {
	_, ex := scrapeMetrics(t, nil)

	wantNames := []string{
		"jm_log_retention_driver_enabled",
		"jm_log_retention_archive_planned_total",
		"jm_log_retention_archive_moved_total",
		// 导出归档（次选路径）的三条读数：未启用时同样必须输出 0 而非缺序列。
		"jm_log_retention_archive_copied_total",
		"jm_log_retention_archive_copy_reclaimed_total",
		"jm_log_retention_archive_copy_kept_original_total",
		"jm_log_retention_archive_rolled_back_total",
		"jm_log_retention_archive_kept_original_total",
		"jm_log_retention_archive_discarded_total",
		"jm_log_retention_archive_not_due_total",
		"jm_log_retention_archive_last_run_timestamp_seconds",
		"jm_log_retention_archive_last_error_present",
		// 事件段回收：与搬运是两条独立链路，未装配时同样必须输出 0 而非缺序列。
		"jm_log_eventstore_prune_enabled",
		"jm_log_eventstore_prune_last_run_timestamp_seconds",
		"jm_log_eventstore_prune_reclaimed_segments_total",
		"jm_log_eventstore_prune_reclaimed_bytes_total",
		"jm_log_eventstore_prune_reclaimed_events_total",
		"jm_log_eventstore_prune_failures",
	}
	if len(ex.value) != len(wantNames) {
		t.Errorf("应有 %d 个序列，得到 %d（%v）", len(wantNames), len(ex.value), ex.order)
	}
	for _, name := range wantNames {
		v, ok := ex.value[name]
		if !ok {
			t.Errorf("未启用也必须输出 %q", name)
			continue
		}
		if v != 0 {
			t.Errorf("未启用时 %q 必须为 0，得到 %v", name, v)
		}
	}
	// 计数器的 TYPE 不能写成 gauge：抓取端会因此不算 rate()。
	for _, name := range []string{
		"jm_log_retention_archive_planned_total",
		"jm_log_retention_archive_moved_total",
		"jm_log_retention_archive_copied_total",
		"jm_log_retention_archive_copy_reclaimed_total",
		"jm_log_retention_archive_copy_kept_original_total",
		"jm_log_retention_archive_rolled_back_total",
		"jm_log_retention_archive_kept_original_total",
		"jm_log_retention_archive_discarded_total",
		"jm_log_retention_archive_not_due_total",
	} {
		if got := ex.kind[name]; got != "counter" {
			t.Errorf("%q 的 TYPE 应为 counter，得到 %q", name, got)
		}
	}
}

// 契约：导出归档（次选路径）的三条读数必须如实反映 Stats。
//
// 为什么要单独立一条：这三条读数回答的是**不同**问题（导出成功了吗 / 热层真省了吗 /
// 导出成功但没获准删的有多少）。把三者合成一条，会让「归档一直在成功、只是三闸没开」
// 与「盘应该省下来了」在告警里长得一模一样。
func TestMetricsHandlerReflectsArchiveCopyStats(t *testing.T) {
	// 三闸全开 + 装配导出与删除出口：让这条路径真的跑起来（否则读数恒为 0，等于没测）。
	p := threeGatePolicy()
	d := NewDriver(p, &fakeLister{parts: metricsFixture()}, nil, nil)
	d.Executor().SetArchiveExporter(&fakeExporter{})
	d.Executor().SetArchiveAuthority(&fakeAuthority{})
	d.Executor().SetRemover(&fakeRemover{})

	if _, err := d.Step(context.Background(), driverNow); err != nil {
		t.Fatalf("单轮驱动失败：%v", err)
	}
	st := d.Executor().Stats()
	if st.ArchiveCopied == 0 {
		t.Fatal("前置条件：本轮应至少有一个分区导出成功")
	}

	_, ex := scrapeMetrics(t, d)
	for name, want := range map[string]int64{
		"jm_log_retention_archive_copied_total":             st.ArchiveCopied,
		"jm_log_retention_archive_copy_reclaimed_total":     st.ArchiveCopyReclaimed,
		"jm_log_retention_archive_copy_kept_original_total": st.ArchiveCopyKeptOriginal,
	} {
		if got := ex.value[name]; got != float64(want) {
			t.Errorf("%q 期望 %v（取自 Stats），得到 %v", name, want, got)
		}
	}
	if got := ex.value["jm_log_retention_archive_copy_reclaimed_total"]; got == 0 {
		t.Error("三闸全开且删除出口已装配时，回收读数不应为 0（否则该序列永远不动，告警形同虚设）")
	}
}

// 契约：读数必须如实反映 Stats（不是另算一份、也不是写死 0）。
func TestMetricsHandlerReflectsArchiveStats(t *testing.T) {
	d := NewDriver(enabledPolicy(), &fakeLister{parts: metricsFixture()}, metricsNoopMover(), nil)
	if _, err := d.Step(context.Background(), driverNow); err != nil {
		t.Fatalf("单轮驱动失败：%v", err)
	}

	_, ex := scrapeMetrics(t, d)
	if got := ex.value["jm_log_retention_driver_enabled"]; got != 1 {
		t.Errorf("装配后 driver_enabled 应为 1，得到 %v", got)
	}
	// 每轮：2 个候选进计划、1 个搬走、1 个未到点。
	want := map[string]float64{
		"jm_log_retention_archive_planned_total":              2,
		"jm_log_retention_archive_moved_total":                1,
		"jm_log_retention_archive_not_due_total":              1,
		"jm_log_retention_archive_kept_original_total":        0,
		"jm_log_retention_archive_discarded_total":            0,
		"jm_log_retention_archive_last_error_present":         0,
		"jm_log_retention_archive_last_run_timestamp_seconds": float64(driverNow.Unix()),
	}
	for name, wantV := range want {
		if got := ex.value[name]; got != wantV {
			t.Errorf("%q 期望 %v，得到 %v", name, wantV, got)
		}
	}

	// 再核对一次「与 Stats() 一致」而不是与上面写死的数一致：写死的数只证明夹具没变，
	// 与 Stats() 对齐才证明端点没有第二套口径。
	st := d.Executor().Stats()
	if got := ex.value["jm_log_retention_archive_planned_total"]; got != float64(st.Planned) {
		t.Errorf("planned_total 与 Stats 不一致：%v vs %d", got, st.Planned)
	}
	if got := ex.value["jm_log_retention_archive_last_run_timestamp_seconds"]; got != float64(st.LastRunAt.Unix()) {
		t.Errorf("last_run 时间戳与 Stats 不一致：%v vs %s", got, st.LastRunAt)
	}
}

// 契约：没有搬运路径时全部落「保留原物」并留下告警 ⇒ last_error_present 必须变 1。
//
// 这个 gauge 是「有告警」的机器可读投影；它恒为 0 的话，告警规则就永远不会响。
func TestMetricsHandlerReportsLastErrorPresence(t *testing.T) {
	d := NewDriver(enabledPolicy(), &fakeLister{parts: metricsFixture()}, nil, nil)
	if _, err := d.Step(context.Background(), driverNow); err != nil {
		t.Fatalf("单轮驱动失败：%v", err)
	}
	if d.Executor().Stats().LastError == "" {
		t.Fatal("前置条件：无搬运路径必须留下告警文本")
	}

	_, ex := scrapeMetrics(t, d)
	if got := ex.value["jm_log_retention_archive_last_error_present"]; got != 1 {
		t.Errorf("有告警时 last_error_present 应为 1，得到 %v", got)
	}
	if got := ex.value["jm_log_retention_archive_moved_total"]; got != 0 {
		t.Errorf("无搬运路径不得出现 Moved，得到 %v", got)
	}
	if got := ex.value["jm_log_retention_archive_kept_original_total"]; got != 1 {
		t.Errorf("到点但搬不了必须记「保留原物」1 个，得到 %v", got)
	}
}

// 契约（并发）：一边跑 Step 累加、一边抓 /metrics，`-race` 下不得报竞争。
//
// 没有 ArchiveExecutor 的 RWMutex 时，这个用例在 -race 下必挂——它是那次加锁的回归。
// 顺带验证累加不丢：收尾读数必须等于「轮数 × 每轮增量」，而不是少算了某几轮。
func TestMetricsHandlerConcurrentScrapeDuringStep(t *testing.T) {
	const rounds = 50
	d := NewDriver(enabledPolicy(), &fakeLister{parts: metricsFixture()}, metricsNoopMover(), nil)
	handler := MetricsHandler(d, nil)

	var wg sync.WaitGroup
	stepDone := make(chan struct{})
	var stepErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stepDone)
		for i := 0; i < rounds; i++ {
			if _, err := d.Step(context.Background(), driverNow.Add(time.Duration(i)*time.Minute)); err != nil {
				stepErr = err
				return
			}
		}
	}()

	// 抓取侧：一直读到执行侧跑完为止（不在固定次数上停，避免用例因调度变慢变成空转）。
	scrapes := 0
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stepDone:
				return
			default:
			}
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			if rec.Code != http.StatusOK {
				t.Errorf("并发抓取必须仍为 200，得到 %d", rec.Code)
				return
			}
			if !strings.Contains(rec.Body.String(), "jm_log_retention_archive_planned_total") {
				t.Errorf("并发抓取返回了不完整的 body：%q", rec.Body.String())
				return
			}
			scrapes++
		}
	}()
	wg.Wait()

	if stepErr != nil {
		t.Fatalf("并发执行中 Step 失败：%v", stepErr)
	}
	if scrapes == 0 {
		t.Fatal("并发用例没有真正抓到过（否则等于没测）")
	}

	// 累加不丢：每轮固定 2 个计划、1 个搬走、1 个未到点。
	st := d.Executor().Stats()
	if st.Planned != 2*rounds {
		t.Errorf("planned 应为 %d，得到 %d（累加丢了）", 2*rounds, st.Planned)
	}
	if st.Moved != rounds {
		t.Errorf("moved 应为 %d，得到 %d（累加丢了）", rounds, st.Moved)
	}
	if st.NotDue != rounds {
		t.Errorf("not_due 应为 %d，得到 %d（累加丢了）", rounds, st.NotDue)
	}

	// 收尾再抓一次，端点与 Stats 必须收敛到同一个数。
	_, ex := scrapeMetrics(t, d)
	if got := ex.value["jm_log_retention_archive_moved_total"]; got != float64(st.Moved) {
		t.Errorf("并发结束后 moved_total 与 Stats 不一致：%v vs %d", got, st.Moved)
	}
	if got := ex.value["jm_log_retention_archive_planned_total"]; got != float64(st.Planned) {
		t.Errorf("并发结束后 planned_total 与 Stats 不一致：%v vs %d", got, st.Planned)
	}
}

// 契约：Stats() 返回的是**值副本**，调用方改它不影响累计读数。
//
// 这条在加了锁之后仍要守：若哪天改成返回指针或返回内部切片视图，
// 锁就白加了（读侧能在锁外改）。
func TestArchiveExecutorStatsReturnsCopy(t *testing.T) {
	d := NewDriver(enabledPolicy(), &fakeLister{parts: metricsFixture()}, metricsNoopMover(), nil)
	if _, err := d.Step(context.Background(), driverNow); err != nil {
		t.Fatalf("单轮驱动失败：%v", err)
	}
	st := d.Executor().Stats()
	st.Moved = math.MaxInt64
	st.LastError = "被调用方改坏了"
	if got := d.Executor().Stats(); got.Moved == math.MaxInt64 || got.LastError != "" {
		t.Fatalf("Stats 必须返回值副本，累计读数被外部改动了：%+v", got)
	}
}

// fakePruneSource 是最小 EventStorePruneSource 实现，用来断言指标如实反映快照。
type fakePruneSource struct {
	ran      bool
	at       time.Time
	segments int64
	bytes    int64
	events   int64
	failures int
	disabled bool
}

func (f fakePruneSource) EventStorePruneSnapshot() (bool, time.Time, int64, int64, int64, int, bool) {
	return f.ran, f.at, f.segments, f.bytes, f.events, f.failures, f.disabled
}

// 契约：回收指标必须如实反映快照，而不是写死 0 或另算一份。
func TestMetricsHandlerReflectsEventStorePrune(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	src := fakePruneSource{
		ran: true, at: at, segments: 54, bytes: 499122176, events: 123456, failures: 2,
	}
	_, ex := scrapeMetricsWithPrune(t, nil, src)

	want := map[string]float64{
		"jm_log_eventstore_prune_enabled":                    1,
		"jm_log_eventstore_prune_last_run_timestamp_seconds": float64(at.Unix()),
		"jm_log_eventstore_prune_reclaimed_segments_total":   54,
		"jm_log_eventstore_prune_reclaimed_bytes_total":      499122176,
		"jm_log_eventstore_prune_reclaimed_events_total":     123456,
		"jm_log_eventstore_prune_failures":                   2,
	}
	for name, w := range want {
		got, ok := ex.value[name]
		if !ok {
			t.Errorf("必须输出 %q", name)
			continue
		}
		if got != w {
			t.Errorf("%q 应为 %v，得到 %v", name, w, got)
		}
	}
}

// 契约：配置关闭时 enabled 必须是 0——否则告警侧会把「没开」当成「开了但零回收」。
func TestMetricsHandlerEventStorePruneDisabled(t *testing.T) {
	src := fakePruneSource{disabled: true}
	_, ex := scrapeMetricsWithPrune(t, nil, src)
	if got := ex.value["jm_log_eventstore_prune_enabled"]; got != 0 {
		t.Fatalf("配置关闭时 enabled 必须为 0，得到 %v", got)
	}
	// 从未运行 ⇒ 时间戳保持 0（不是负的时间零值）。
	if got := ex.value["jm_log_eventstore_prune_last_run_timestamp_seconds"]; got != 0 {
		t.Fatalf("从未运行时时间戳必须为 0，得到 %v", got)
	}
}
