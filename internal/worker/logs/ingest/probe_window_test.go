package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// vlMaxQueryTimeRange 从 **VL 的实际启动参数**派生上限，而不是引用被测代码的常量。
//
// 为什么必须这样取：若断言写成「跨度 ≤ projectionProbeSpan」，那么把常量本身改大（或像
// 2026-10-12 那样让两端组合溢出）时断言会跟着一起放宽 ⇒ 自证，抓不到任何东西。
// 从 vlsup.BuildArgs 解析则锚在「VL 会接受什么」这个外部事实上——探测窗一旦越过它，
// VL 会以 400 拒绝，`projectionGenerationUsed` 报错并让上游「按未占用继续」，
// 于是代次重名防护被整条跳过（2026-10-08 生产事故的成因；2026-10-12 我自己又踩了一次）。
func vlMaxQueryTimeRange(t *testing.T, retention string) time.Duration {
	t.Helper()
	const flag = "-search.maxQueryTimeRange="
	args, err := vlsup.BuildArgs(vlsup.InstanceConfig{
		Namespace:       vlsup.NamespaceHot,
		Port:            vlsup.DefaultPort(vlsup.NamespaceHot),
		StorageDataPath: t.TempDir(),
		RetentionPeriod: retention,
		// 占位凭据：本用例只读启动参数里的 `-search.maxQueryTimeRange`，不发起任何
		// 需要鉴权的请求。取值沿用本仓既有测试的短占位写法（如 cmdline_test.go 的
		// `local-only`）——**刻意取短**：PR Hygiene 的凭据扫描按
		// `(password|secret|…)[:=]…{12,}` 命中，长占位串会被误报为硬编码凭据。
		AuthUsername: "local-only",
		AuthPassword: "local-only",
	})
	require.NoError(t, err, "构造 VL 启动参数失败")
	for _, arg := range args {
		if strings.HasPrefix(arg, flag) {
			// 上限可能是 `<n>d`（历史上的硬编码 31d）或 `<n>h`（2026-10-22 起由
			// retention 派生，见 vlsup.QueryTimeRangeLimit —— 它刻意用 h 表达，避免
			// 「小数天」的取整歧义）。两种都要认：只认 `d` 会让派生改造后本回归
			// 以「解析失败」而非「越界」告警，把验收点从语义错位成格式错。
			// `time.ParseDuration` 不认识 `d`（Go 没有 day 单位），故 d 形态自己剥。
			raw := strings.TrimPrefix(arg, flag)
			if days, ok := strings.CutSuffix(raw, "d"); ok {
				n, parseErr := strconv.Atoi(days)
				require.NoError(t, parseErr, "无法解析 VL 的 maxQueryTimeRange 天数：%s", raw)
				return time.Duration(n) * 24 * time.Hour
			}
			d, parseErr := time.ParseDuration(raw)
			require.NoError(t, parseErr,
				"无法解析 VL 的 maxQueryTimeRange（既非 <n>d 也非 Go duration）：%q；请同步更新解析", raw)
			return d
		}
	}
	t.Fatalf("VL 启动参数中缺少 %s：探测窗上限失去锚点，本回归会变成自证", flag)
	return 0
}

// TestProjectionProbeWindowWithinVLHardLimit 守住「探测窗总跨度 ≤ VL 硬上限」。
//
// **转红实测**：把 projectionProbeFutureSlack 加上而起点仍按 now-projectionProbeSpan 算
// （即 2026-10-12 我引入 32d 的那版写法）⇒ 本用例红。
//
// 两种取证方式并存：
//   - 纯时间算术（不依赖任何夹具行为，最快最稳）；
//   - 端到端（真实走一次探测，从夹具记录的实际 start/end 复算跨度），确保被测路径**真的**
//     把窗口构造对了，而不是只有常量对。
func TestProjectionProbeWindowWithinVLHardLimit(t *testing.T) {

	// ① 纯时间算术：按被测代码的窗口构造方式复算总跨度。
	//
	// **必须用生产默认配置派生，不能写死 30d**（2026-10-22 复审 P1-3）：
	// 探测窗的职责是「覆盖该保留期内的任意一天」，而保留期是可配置的。
	// 原先这里写死 `30d`，于是在 `DefaultRetentionPeriod` 提到 120d、
	// 默认热窗口 90d 之后，用例仍然全绿 —— 而真实覆盖缺口（第 31～90 天）
	// 已经回来了。写死配置值让守卫对配置变更失明，正是本项目反复吃亏的形态。
	// **探测窗必须覆盖整个 hot retention，且恰好等于「服务端上限」**。
	//
	// 这两件事是同一个等式：`QueryTimeRangeLimit()` 派生为 `retention + 1d`，
	// 而 `hotRetention` 与它同源（都取 cfg.LogVL.RetentionPeriod）⇒
	//     派生窗 = hotRetention + 1d = 服务端上限
	// 所以「覆盖保留期」与「不被 VL 400」不冲突，任何钳制都是多余的（2026-10-22 复审 P1-2）。
	for _, retention := range []time.Duration{
		30 * 24 * time.Hour,  // 生产现役 30d
		90 * 24 * time.Hour,  // 默认热窗口
		120 * 24 * time.Hour, // DefaultRetentionPeriod
		7 * 24 * time.Hour,   // 更短：应等于 8d（比 31d 常量小，能区分派生与常量）
	} {
		m := &Manager{hotRetention: retention}
		span := m.projectionProbeWindow()
		now := time.Now().UTC()
		end := now.Add(projectionProbeFutureSlack)
		start := end.Add(-span)

		// ① 不得越过 VL 硬上限（否则探测整条 400）。
		// **上限必须按同一个 retention 派生**：`-search.maxQueryTimeRange` 由
		// `QueryTimeRangeLimit()` = retention+1d 得出，而 hotRetention 与它同源。
		// 用固定的 30d 上限去校验 90d 的场景，测的就不是生产形态了。
		limitForRetention := vlMaxQueryTimeRange(t, fmt.Sprintf("%dd", int(retention.Hours()/24)))
		require.LessOrEqual(t, end.Sub(start), limitForRetention,
			"hotRetention=%s 时探测窗总跨度 %s 越过 VL 上限 %s", retention, end.Sub(start), limitForRetention)
		// ② 上界必须覆盖未来行。
		require.Greater(t, end.Sub(now), time.Duration(0),
			"上界必须晚于 now：无日期日志行的事件时间可合法落在未来")
		// ③ **必须覆盖整个保留期**（这是探测的全部意义）。
		require.GreaterOrEqual(t, now.Sub(start), retention,
			"hotRetention=%s 时起点只回看 %s，对更早的同名旧行结构性失明"+
				"⇒ 探测恒报未占用 ⇒ 代次重名 ⇒ 永不发布（2026-10-08 事故形态）",
			retention, now.Sub(start))
		// ④ **精确值**：窗口 = retention + 余量。
		// 少了这条，把派生逻辑删掉退回常量也能过（30d+1d == 31d 常量，恰好相同），
		// 那样本回归抓不到「派生被移除」——本项目反复出现的假绿形态。
		require.Equal(t, retention+projectionProbeSlackDays*24*time.Hour, span,
			"hotRetention=%s 时探测窗必须精确等于 retention+余量（实际 %s）", retention, span)
	}

	// ② `hotRetention` 未配置（测试/旧调用方）时必须退回历史验证过的 31d，
	//    而不是退化成 0（那会让探测窗消失了）。
	require.Equal(t, projectionProbeSpan, (&Manager{}).projectionProbeWindow(),
		"未配置 hotRetention 时必须退回 31d 下限")

	// ② 端到端：真实走一次取名探测，用夹具记录的实际窗口复算。
	//    直接调 nextFreeProjectionGenerationWithClient（而非 pollOnce）：后者是否触发探测取决于
	//    该轮是否有可投递事件，形态脆弱；此方法本身就是探测的**唯一实现**入口，
	//    且 P2-11 回归已证实它会发出实探（probes 递增）。
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	// FILE_PRIMARY 要求 Path 存在（与 P2-11 回归同法：空文件即可，本用例不投递事件）。
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "node:probe-window", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:probe-window", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	got := m.nextFreeProjectionGenerationWithClient(client, source, "projection-9")
	require.Equal(t, "projection-10", got, "前置：取名应当只进位一次（名字未占用）")

	fixture.mu.Lock()
	spans := append([]time.Duration(nil), fixture.probeSpans...)
	fixture.mu.Unlock()
	require.NotEmpty(t, spans, "本用例须真的发出过占用探测查询（否则夹具读数为空，断言会静默失效）")
	// 该夹具的 Manager 未设 HotRetention ⇒ 探测窗退回 31d 常量，
	// 故按生产现役 30d 的上限（744h）校验即可。
	limitDefault := vlMaxQueryTimeRange(t, "30d")
	for _, span := range spans {
		require.LessOrEqual(t, span, limitDefault,
			"实际探测请求的窗口跨度 %s 越过 VL 上限 %s（生产上会被 400 拒绝并静默关闭重名防护）", span, limitDefault)
	}
}

// TestAllVLQueriesCarryExplicitTimeWindow 守住「发往 VL 的每个查询都必须显式带 start/end」。
//
// **为什么值得一条文件级断言**：不带时间窗的查询会被 VL 以默认全时段
// （[1677-09-21, 2262-04-11]）处理并被 `-search.maxQueryTimeRange` 以 **400** 拒绝
// （实测记录见 vlsup/cmdline.go）。而这类遗漏在本仓已发生**三次**：
//   - 2026-09-28 投影校验查询：被拒 ⇒ 误判「不可见」⇒ 采集静默停摆（多轮盲改后才由参数日志定位）；
//   - 2026-10-08 代次占用探测：被拒 ⇒ 「按未占用继续」⇒ 重名防护静默失效；
//   - 2026-10-12 dropEventsAlreadyWritten 的幂等查询：被拒 ⇒ 「证据缺失照写」⇒ 幂等过滤整条失效。
//
// 三次的形态完全一致：**请求恒失败 + 上层静默降级**，而单测夹具只按窗口过滤行、
// 对「没有窗口」一律放行 ⇒ 在单元测试里结构性不可见。故这里直接对夹具记录的**每一条**
// query 请求断言窗口存在性，而不是只测某一个调用点。
//
// **转红实测**：把 dropEventsAlreadyWritten 的 `"start"/"end"` 两行去掉
// （即该函数修复前的原样）⇒ 本用例红，并指名到那条未带窗口的查询。
func TestAllVLQueriesCarryExplicitTimeWindow(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守「每次 VL 查询都必须带显式时间窗」——靠投递路径上的代次占用探测与逐字段校验查询，两者都随简化删除。
	// 简化后 `deliverCtx` 只做「去重 → 写段 → 写 VL」，那些机制不在投递路径上运行，
	// 断言前提不存在。保留为明确跳过而不是默默删掉。
	t.Skip("投递路径简化（2026-11-02）：「每次 VL 查询都必须带显式时间窗」——靠投递路径上的代次占用探测与逐字段校验查询，两者都随简化删除")
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: windowed\n[12:00:02] [Server thread/INFO]: query\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "node:all-windowed", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:all-windowed", UTCDay: runtimeTestUTCDay(),
	}

	// ① 常规投递（走校验查询路径）。
	first, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	first.pollOnce()
	require.NoError(t, first.Stop())

	// ② 慢路径/replace 路径（走 dropEventsAlreadyWritten 的幂等查询路径）：
	//    把同一份日志原地重写触发重放，使非尾部路径被走到。
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: windowed\n[12:00:02] [Server thread/INFO]: query\n[12:00:03] [Server thread/INFO]: again\n"), 0o644))
	second, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	second.pollOnce()
	require.NoError(t, second.Stop())

	fixture.mu.Lock()
	records := append([]queryWindowRecord(nil), fixture.queryWindows...)
	fixture.mu.Unlock()
	require.NotEmpty(t, records, "本用例须真的发出过 VL 查询（否则夹具读数为空，断言会静默失效）")

	for _, rec := range records {
		require.True(t, rec.HasWindow,
			"存在未带 start/end 的 VL 查询（会被 VL 以 400 拒绝并让上层静默降级）：query=%s", rec.Query)
		require.False(t, rec.End.Before(rec.Start),
			"VL 查询窗口倒置（end < start）：query=%s start=%s end=%s",
			rec.Query, rec.Start.Format(time.RFC3339Nano), rec.End.Format(time.RFC3339Nano))
	}
}
