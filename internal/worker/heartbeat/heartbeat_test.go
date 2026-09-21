package heartbeat

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/directprobe"
	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeProbeBody 是一份可被 metrics.parseServerProbeMetrics 解析的 ServerProbe /metrics 文本。
const fakeProbeBody = `# HELP serverprobe_tps 每秒 tick
serverprobe_tps{window="1m"} 19.5
serverprobe_mspt_seconds{quantile="avg"} 0.0123
serverprobe_players_online 7
serverprobe_heap_used_bytes 1073741824
serverprobe_heap_max_bytes 2147483648
serverprobe_threads 42
serverprobe_system_cpu_load 0.25
serverprobe_uptime_seconds 3600
serverprobe_world_loaded_chunks{world="world"} 100
serverprobe_world_entities{world="world"} 50
serverprobe_world_tile_entities{world="world"} 20
`

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	_, portStr, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return port
}

func TestCollectInstanceMetrics_ScrapesRunningWithProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeProbeBody))
	}))
	defer srv.Close()
	port := portOf(t, srv.URL)

	snaps := []process.InstanceSnapshot{
		{UUID: "running-with-probe", State: "RUNNING", ProbePort: port},
		{UUID: "running-no-probe", State: "RUNNING", ProbePort: 0},
		{UUID: "stopped-with-probe", State: "STOPPED", ProbePort: port},
	}
	out := collectInstanceMetrics(snaps)

	require.Len(t, out, 1, "仅采集 RUNNING 且 ProbePort>0 的实例")
	s := out[0]
	require.Equal(t, "running-with-probe", s.InstanceUuid)
	require.True(t, s.ProbeAvailable)
	require.Equal(t, 19.5, s.Tps)
	require.InDelta(t, 12.3, s.MsptMillis, 1e-9)
	require.Equal(t, int32(7), s.PlayersOnline)
	require.Equal(t, int64(1073741824), s.HeapUsedBytes)
	require.Equal(t, int32(42), s.Threads)
	require.InDelta(t, 0.25, s.CpuLoad, 1e-9)
	require.Len(t, s.Worlds, 1)
	require.Equal(t, "world", s.Worlds[0].Name)
	require.Equal(t, int64(100), s.Worlds[0].LoadedChunks)
	require.Equal(t, int64(50), s.Worlds[0].Entities)
}

func TestCollectInstanceMetrics_ProbeErrorMarksUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	port := portOf(t, srv.URL)

	out := collectInstanceMetrics([]process.InstanceSnapshot{
		{UUID: "inst-1", State: "RUNNING", ProbePort: port},
	})
	require.Len(t, out, 1, "抓取失败仍产出一条快照（标记探针不可用）")
	require.Equal(t, "inst-1", out[0].InstanceUuid)
	require.False(t, out[0].ProbeAvailable)
}

func TestCollectInstanceMetrics_NoTargets(t *testing.T) {
	require.Nil(t, collectInstanceMetrics(nil))
	require.Nil(t, collectInstanceMetrics([]process.InstanceSnapshot{
		{UUID: "s", State: "STOPPED", ProbePort: 9999},
		{UUID: "r", State: "RUNNING", ProbePort: 0},
	}), "无可采实例返回 nil")
}

// resetDirectProbeState 清空告警/退避全局状态，避免用例间相互污染。
func resetDirectProbeState(t *testing.T) {
	t.Helper()
	probeScrapeState.Lock()
	probeScrapeState.lastSignature = map[string]string{}
	probeScrapeState.Unlock()
	directProbeBackoff.Lock()
	directProbeBackoff.entries = map[string]*probeBackoffEntry{}
	directProbeBackoff.Unlock()
	t.Cleanup(func() { resetDirectProbeStateNoT() })
}

func resetDirectProbeStateNoT() {
	probeScrapeState.Lock()
	probeScrapeState.lastSignature = map[string]string{}
	probeScrapeState.Unlock()
	directProbeBackoff.Lock()
	directProbeBackoff.entries = map[string]*probeBackoffEntry{}
	directProbeBackoff.Unlock()
}

// TestCollectInstanceMetrics_BudgetFastReturn 采集总预算生效：探针卡住时按预算快速返回，
// 未完成实例补「不可用」空样本，不阻塞整拍（FR-446 审计项 2）。
func TestCollectInstanceMetrics_BudgetFastReturn(t *testing.T) {
	resetDirectProbeState(t)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 卡住，模拟黑洞端口
		_, _ = w.Write([]byte(fakeProbeBody))
	}))
	port := portOf(t, srv.URL)
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	const budget = 60 * time.Millisecond
	start := time.Now()
	out := collectInstanceMetricsAt([]process.InstanceSnapshot{
		{UUID: "stuck-1", State: "RUNNING", ProbePort: port},
		{UUID: "stuck-2", State: "RUNNING", ProbePort: port},
	}, start, budget)
	elapsed := time.Since(start)

	require.Len(t, out, 2, "超预算也要为每个实例给出样本（缺测语义，不静默丢失）")
	assert.Less(t, elapsed, 2*time.Second, "应收敛在预算量级，而非探针的 5s 超时")
	for _, s := range out {
		assert.False(t, s.ProbeAvailable)
		assert.False(t, s.PlayersOnlineAvailable)
	}
}

// TestProbeSourceBackoffSkipsAfterRepeatedFailures 连续失败达阈值后跳拍，恢复后立即清零。
func TestProbeSourceBackoffSkipsAfterRepeatedFailures(t *testing.T) {
	resetDirectProbeState(t)
	now := time.Now()

	recordProbeSourceResult("inst-b", sourceSLP, false, now)
	assert.False(t, shouldSkipProbeSource("inst-b", sourceSLP, now), "首次失败仍每拍重探")

	recordProbeSourceResult("inst-b", sourceSLP, false, now)
	assert.True(t, shouldSkipProbeSource("inst-b", sourceSLP, now.Add(time.Second)), "二次连续失败进入退避")
	assert.False(t, shouldSkipProbeSource("inst-b", sourceSLP, now.Add(probeSourceRetryInterval)),
		"退避窗口到期后恢复重探")

	recordProbeSourceResult("inst-b", sourceSLP, true, now)
	assert.False(t, shouldSkipProbeSource("inst-b", sourceSLP, now), "成功即清零退避")

	// 其它实例/来源互不影响。
	assert.False(t, shouldSkipProbeSource("inst-c", sourceSLP, now))
	assert.False(t, shouldSkipProbeSource("inst-b", sourceQuery, now))
}

// TestProbeSourceBackoffCooldownBounded 退避时长按失败次数递增（60s→120s）且不超过上限，
// 且首档严格大于 30s 心跳节拍（FR-446 复审 N4：首档 == 节拍会被下一拍追上而形同虚设）。
func TestProbeSourceBackoffCooldownBounded(t *testing.T) {
	resetDirectProbeState(t)
	now := time.Now()

	assert.Greater(t, probeSourceRetryInterval, 30*time.Second, "首档退避须大于心跳节拍")

	// 下标 = 连续失败次数；0 表示不进入退避。
	wants := []time.Duration{0, probeSourceRetryInterval, 2 * probeSourceRetryInterval, probeSourceMaxRetryInterval, probeSourceMaxRetryInterval}
	for fails := 1; fails <= len(wants); fails++ {
		recordProbeSourceResult("inst-d", sourceQuery, false, now)
		want := wants[fails-1]
		if want == 0 {
			assert.False(t, shouldSkipProbeSource("inst-d", sourceQuery, now), "首次失败不进入退避")
			continue
		}
		assert.True(t, shouldSkipProbeSource("inst-d", sourceQuery, now.Add(want-time.Second)),
			"第 %d 次失败后退避窗口应达 %s", fails, want)
		assert.False(t, shouldSkipProbeSource("inst-d", sourceQuery, now.Add(want+time.Second)),
			"窗口到期即恢复重探")
	}

	// 继续失败仍封顶在 120s，不再增长。
	for i := 0; i < 6; i++ {
		recordProbeSourceResult("inst-d", sourceQuery, false, now)
	}
	assert.True(t, shouldSkipProbeSource("inst-d", sourceQuery, now.Add(probeSourceMaxRetryInterval-time.Second)))
	assert.False(t, shouldSkipProbeSource("inst-d", sourceQuery, now.Add(probeSourceMaxRetryInterval+time.Second)))
}

// TestProbeConfigWithBackoffSkipsBackedOffSource 退避中的来源端口置 0（编排链跳过），其余保持不变。
func TestProbeConfigWithBackoffSkipsBackedOffSource(t *testing.T) {
	resetDirectProbeState(t)
	now := time.Now()
	recordProbeSourceResult("inst-e", sourceSLP, false, now)
	recordProbeSourceResult("inst-e", sourceSLP, false, now)

	cfg, att := probeConfigWithBackoff(process.InstanceSnapshot{
		UUID: "inst-e", State: "RUNNING", ProbePort: 8123, ServerPort: 25565, QueryPort: 25566,
	}, now)

	assert.True(t, att.probe)
	assert.False(t, att.slp, "SLP 处于退避")
	assert.True(t, att.query)
	assert.Equal(t, 8123, cfg.ProbePort)
	assert.Zero(t, cfg.ServerPort, "退避来源端口置 0，编排链跳过")
	assert.Equal(t, 25566, cfg.QueryPort)
	assert.NotZero(t, cfg.SLPTimeout, "直探超时显式传入（CP 下发/内置默认）")
	assert.NotZero(t, cfg.QueryTimeout)
}

// TestPruneProbeStateDropsRemovedInstances 实例删除后其告警与退避状态被清理（FR-446 审计项 10）。
func TestPruneProbeStateDropsRemovedInstances(t *testing.T) {
	resetDirectProbeState(t)
	now := time.Now()
	recordProbeSourceResult("gone", sourceSLP, false, now)
	recordInstanceSourceResult("gone", sourceSLP, "实例指标来源皆不可用: SLP 超时")
	recordProbeSourceResult("kept", sourceSLP, false, now)
	recordInstanceSourceResult("kept", sourceSLP, "实例指标来源皆不可用: SLP 超时")

	pruneProbeState([]process.InstanceSnapshot{{UUID: "kept", State: "RUNNING"}})

	probeScrapeState.Lock()
	_, goneHas := probeScrapeState.lastSignature["gone"]
	_, keptHas := probeScrapeState.lastSignature["kept"]
	probeScrapeState.Unlock()
	assert.False(t, goneHas, "已删除实例的告警状态应清理")
	assert.True(t, keptHas, "仍在管实例的状态保留")

	directProbeBackoff.Lock()
	_, goneBackoff := directProbeBackoff.entries["gone|"+sourceSLP]
	_, keptBackoff := directProbeBackoff.entries["kept|"+sourceSLP]
	directProbeBackoff.Unlock()
	assert.False(t, goneBackoff)
	assert.True(t, keptBackoff)

	// 实例全删（含无可采实例）也清空，避免状态无界增长。
	pruneProbeState(nil)
	directProbeBackoff.Lock()
	assert.Empty(t, directProbeBackoff.entries)
	directProbeBackoff.Unlock()
}

// TestUnreachableSourcesReasonIncludesErrorCategory 告警文案并入错误类别与退避标注（FR-446 审计项 4）。
func TestUnreachableSourcesReasonIncludesErrorCategory(t *testing.T) {
	snap := process.InstanceSnapshot{UUID: "i1", ServerPort: 25565, QueryPort: 25566}
	tel := &metrics.InstanceTelemetry{
		SLPErr:   fmt.Errorf("SLP 连接失败: %w", os.ErrDeadlineExceeded),
		QueryErr: fmt.Errorf("Query 连接失败: %w", syscall.ECONNREFUSED),
	}
	reason := unreachableSourcesReason(snap, probeAttempt{slp: true, query: true}, tel)
	assert.Contains(t, reason, "SLP(server_port=25565): timeout")
	assert.Contains(t, reason, "Query(query_port=25566): connection refused")

	// 处于退避而未尝试的来源要显式标注，避免被误读成「探测成功」而静默。
	reason = unreachableSourcesReason(snap, probeAttempt{}, tel)
	assert.Contains(t, reason, "SLP(server_port=25565): 退避中")
	assert.Contains(t, reason, "Query(query_port=25566): 退避中")

	// 任一来源可用 → 无告警文案。
	ok := &metrics.InstanceTelemetry{SLPAvailable: true}
	assert.Empty(t, unreachableSourcesReason(snap, probeAttempt{slp: true}, ok))

	// 本拍未配置任何来源 → 空串（不算故障）。
	assert.Empty(t, unreachableSourcesReason(process.InstanceSnapshot{UUID: "i2"}, probeAttempt{}, &metrics.InstanceTelemetry{}))
}

// TestUnreachableSourcesSignatureStableAcrossCategoryJitter 变化告警的**稳定判据**不受错误类别抖动
// 与退避态切换影响（FR-446 复审 N5）：否则同一故障会因文案每秒变化而每拍重发 WARN。
func TestUnreachableSourcesSignatureStableAcrossCategoryJitter(t *testing.T) {
	snap := process.InstanceSnapshot{UUID: "i1", ServerPort: 25565, QueryPort: 25566}

	// 同一次故障：错误类别互换 → 判据一致（仅来源标签集合）。
	a := unreachableSourcesSignature(snap, probeAttempt{slp: true, query: true}, &metrics.InstanceTelemetry{
		SLPErr:   os.ErrDeadlineExceeded,
		QueryErr: syscall.ECONNREFUSED,
	})
	b := unreachableSourcesSignature(snap, probeAttempt{slp: true, query: true}, &metrics.InstanceTelemetry{
		SLPErr:   syscall.ECONNREFUSED,
		QueryErr: os.ErrDeadlineExceeded,
	})
	assert.Equal(t, "slp,query", a)
	assert.Equal(t, a, b, "错误类别抖动不得改变判据")

	// 来源进出退避（尝试过 → 退避中）→ 判据仍一致（不因 "timeout"↔"退避中" 文案切换重发 WARN）。
	assert.Equal(t, a, unreachableSourcesSignature(snap, probeAttempt{}, &metrics.InstanceTelemetry{}))

	// 任一来源可用 → 空判据（恢复）；未配置任何来源 → 空判据（不算故障）。
	assert.Empty(t, unreachableSourcesSignature(snap, probeAttempt{slp: true}, &metrics.InstanceTelemetry{SLPAvailable: true}))
	assert.Empty(t, unreachableSourcesSignature(process.InstanceSnapshot{UUID: "i2"}, probeAttempt{}, &metrics.InstanceTelemetry{}))
}

// TestRecordInstanceSourceResultDeduplicatesBySignature 告警按稳定判据去重：文案变化不重发、判据变化才重发。
func TestRecordInstanceSourceResultDeduplicatesBySignature(t *testing.T) {
	resetDirectProbeState(t)

	recordInstanceSourceResult("i1", "slp", "实例指标来源皆不可用: SLP(server_port=25565): timeout")
	probeScrapeState.Lock()
	first := probeScrapeState.lastSignature["i1"]
	probeScrapeState.Unlock()
	assert.Equal(t, "slp", first)

	// 同一判据、不同文案（错误类别抖动）→ 判据不变。
	recordInstanceSourceResult("i1", "slp", "实例指标来源皆不可用: SLP(server_port=25565): connection refused")
	probeScrapeState.Lock()
	second := probeScrapeState.lastSignature["i1"]
	probeScrapeState.Unlock()
	assert.Equal(t, "slp", second)

	// 判据变化（新增一源故障）→ 更新判据。
	recordInstanceSourceResult("i1", "slp,query", "实例指标来源皆不可用: ...")
	probeScrapeState.Lock()
	third := probeScrapeState.lastSignature["i1"]
	probeScrapeState.Unlock()
	assert.Equal(t, "slp,query", third)

	// 恢复 → 清空。
	recordInstanceSourceResult("i1", "", "")
	probeScrapeState.Lock()
	_, ok := probeScrapeState.lastSignature["i1"]
	probeScrapeState.Unlock()
	assert.False(t, ok)
}

// TestCollectInstanceMetrics_BackoffBaseIsProbeCompletion 退避窗口从**探测完成时刻**起算而非拍起点
// （FR-446 复审 N4）：以拍起点为基准会被下一拍提前追上。
func TestCollectInstanceMetrics_BackoffBaseIsProbeCompletion(t *testing.T) {
	resetDirectProbeState(t)

	const delay = 400 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(http.StatusInternalServerError) // 探针返回非 200 → 不可用
	}))
	defer srv.Close()

	snap := process.InstanceSnapshot{UUID: "inst-n4", State: "RUNNING", ProbePort: portOf(t, srv.URL)}
	// 连续两拍把该来源推入退避（阈值 2）。
	collectInstanceMetricsAt([]process.InstanceSnapshot{snap}, time.Now(), 10*time.Second)
	collectInstanceMetricsAt([]process.InstanceSnapshot{snap}, time.Now(), 10*time.Second)
	after := time.Now()

	directProbeBackoff.Lock()
	e := directProbeBackoff.entries["inst-n4|"+sourceProbe]
	directProbeBackoff.Unlock()
	require.NotNil(t, e, "连续失败达阈值应进入退避")

	// 完成时刻起算 → nextRetry 距 after 仍接近整个首档；拍起点起算则会短 delay。
	assert.Greater(t, e.nextRetry.Sub(after), probeSourceRetryInterval-delay/2,
		"退避窗口应自探测完成时刻起算，实际 nextRetry-after=%s", e.nextRetry.Sub(after))
}

// TestCollectInstanceBudget_FollowsEffectiveTimeouts 单拍采集预算**由生效直探超时推导**
// （FR-446 复审 NEW-ISSUE A）：运营把 direct_probe.* 调大后预算随之变大，不再被硬编码 15s 封顶。
func TestCollectInstanceBudget_FollowsEffectiveTimeouts(t *testing.T) {
	t.Cleanup(func() { metrics.SetDirectProbeTimeouts(0, 0) })

	// 默认超时：预算 = 余量 + 探针上限 + 3s + 3s。
	metrics.SetDirectProbeTimeouts(0, 0)
	def := collectInstanceBudget()
	assert.Equal(t, directprobe.CollectBudgetFor(directprobe.DefaultTimeout, directprobe.DefaultTimeout), def)

	// 拉到大：预算随之变大（而不是仍停在固定值）。
	metrics.SetDirectProbeTimeouts(directprobe.MaxTimeout, directprobe.MaxTimeout)
	big := collectInstanceBudget()
	assert.Equal(t, directprobe.MaxCollectBudget(), big)
	assert.Greater(t, big, def, "预算必须随生效超时增大，否则大超时组合仍会被静默砍掉")
}

// TestCollectInstanceBudget_CoversWorstCaseBelowTick 不变量（FR-446 复审 NEW-ISSUE A）：
// 允许上界下，单实例同源串行最坏 ≤ 预算 ≤ 节拍 − 余量。
func TestCollectInstanceBudget_CoversWorstCaseBelowTick(t *testing.T) {
	t.Cleanup(func() { metrics.SetDirectProbeTimeouts(0, 0) })

	metrics.SetDirectProbeTimeouts(directprobe.MaxTimeout, directprobe.MaxTimeout)
	budget := collectInstanceBudget()

	worst := directprobe.ProbeScrapeTimeoutCap + 2*directprobe.MaxTimeout
	assert.GreaterOrEqual(t, budget, worst, "预算必须 ≥ 单实例最坏（探针 5s + slp + query）")
	assert.LessOrEqual(t, budget+directprobe.HeartbeatTickReserve, directprobe.HeartbeatInterval,
		"预算 + 余量必须 ≤ 心跳节拍，否则心跳积压")
	assert.Less(t, budget, directprobe.HeartbeatInterval)
	assert.True(t, directprobe.BudgetCoversTick(budget))
}

// TestWarnIfBudgetBreaksTickGuardrail 预算与节拍失配时**不静默**：显式 WARN（护栏闸门）。
func TestWarnIfBudgetBreaksTickGuardrail(t *testing.T) {
	// 当前契约下自洽 → 不触发（不可断言日志，断言判据函数即可）。
	assert.True(t, directprobe.BudgetCoversTick(directprobe.MaxCollectBudget()))

	// 失配判据：预算 + 余量 > 节拍，或预算小于单实例最坏（任一为真即应告警）。
	assert.False(t, directprobe.BudgetCoversTick(directprobe.HeartbeatInterval))
	// 直接调用不得 panic（并覆盖告警分支；日志由 slog 落到测试输出）。
	warnIfBudgetBreaksTickGuardrail(directprobe.HeartbeatInterval, directprobe.MaxTimeout, directprobe.MaxTimeout)
	warnIfBudgetBreaksTickGuardrail(time.Millisecond, directprobe.MaxTimeout, directprobe.MaxTimeout)
}

// TestCollectInstanceMetrics_OverBudgetKeepsUnavailableSamples 超预算不静默丢：每个实例仍给出
// 样本（不可用语义），并保留「用已完成部分快速返回」的既有行为（FR-446 审计项 2 / NEW-ISSUE A 要求 4）。
func TestCollectInstanceMetrics_OverBudgetKeepsUnavailableSamples(t *testing.T) {
	resetDirectProbeState(t)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(fakeProbeBody))
	}))
	port := portOf(t, srv.URL)
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	out := collectInstanceMetricsAt([]process.InstanceSnapshot{
		{UUID: "over-budget", State: "RUNNING", ProbePort: port},
	}, time.Now(), 50*time.Millisecond)

	require.Len(t, out, 1)
	assert.Equal(t, "over-budget", out[0].InstanceUuid)
	assert.False(t, out[0].ProbeAvailable, "未完成实例按不可用落 NULL，而非伪造值")
}

// TestFillInstanceMetricSample TrimsUnconsumedFields 心跳只填有 CP 消费者的字段；
// 文本类直探字段不再每拍携带（FR-446 审计项 3），在线人数可用位随编排结果下发。
func TestFillInstanceMetricSampleTrimsUnconsumedFields(t *testing.T) {
	tel := metrics.Orchestrate(
		&metrics.ProbeSnapshot{TPS: 19.5, PlayersOnline: 7},
		&metrics.SLPSnapshot{Motd: "M", Version: "1.20.4", PlayersMax: 20, Favicon: "data:image/png;base64,x"},
		&metrics.QuerySnapshot{Plugins: []string{"WorldEdit"}, Map: "world", PlayerNames: []string{"Steve"}},
	)
	sample := &workerpb.InstanceMetricSample{InstanceUuid: "u1"}
	fillInstanceMetricSample(sample, tel)

	assert.True(t, sample.ProbeAvailable)
	assert.Equal(t, 19.5, sample.Tps)
	assert.Equal(t, int32(7), sample.PlayersOnline)
	assert.True(t, sample.PlayersOnlineAvailable)
	assert.True(t, sample.SlpAvailable)
	assert.True(t, sample.QueryAvailable)

	// 预留字段不填充（减少每拍载荷）。
	assert.Empty(t, sample.Motd)
	assert.Empty(t, sample.Version)
	assert.Empty(t, sample.PlayerNames)
	assert.Empty(t, sample.Plugins)
	assert.Empty(t, sample.Map)
	assert.Zero(t, sample.MaxPlayers)
	assert.Empty(t, sample.SourceMask)
	assert.False(t, sample.PlayerNamesPartial)
}

// TestCollectInstanceMetrics_SkipsNonProbeInstances FR-454：探针不适用（代理/通用二进制/Beacon）
// 的实例不分配探针端口（ProbePort=0），心跳采集据 ProbePort>0 过滤——不再对它们每拍抓取 /metrics
// 必失败（采集器只对 ProbePort>0 的 RUNNING 实例发起抓取，此类实例无目标返回 nil）。
func TestCollectInstanceMetrics_SkipsNonProbeInstances(t *testing.T) {
	out := collectInstanceMetrics([]process.InstanceSnapshot{
		{UUID: "beacon", State: "RUNNING", ProbePort: 0},
		{UUID: "binary", State: "RUNNING", ProbePort: 0},
		{UUID: "proxy", State: "RUNNING", ProbePort: 0},
	})
	require.Nil(t, out, "无探针端口的实例不被采集，避免每拍必失败")
}
