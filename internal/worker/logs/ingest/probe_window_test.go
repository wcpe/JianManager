package ingest

import (
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
func vlMaxQueryTimeRange(t *testing.T) time.Duration {
	t.Helper()
	const flag = "-search.maxQueryTimeRange="
	args, err := vlsup.BuildArgs(vlsup.InstanceConfig{
		Namespace:       vlsup.NamespaceHot,
		Port:            vlsup.DefaultPort(vlsup.NamespaceHot),
		StorageDataPath: t.TempDir(),
		RetentionPeriod: "30d",
		AuthUsername:    "probe-window-test",
		AuthPassword:    "probe-window-test",
	})
	require.NoError(t, err, "构造 VL 启动参数失败")
	for _, arg := range args {
		if strings.HasPrefix(arg, flag) {
			// VL 的参数写成 `31d`，而 time.ParseDuration 不支持 `d` 单位（只到 `h`），
			// 故自己解析「<n>d」形态；出现其它单位时显式失败，不静默跳过。
			raw := strings.TrimPrefix(arg, flag)
			days, ok := strings.CutSuffix(raw, "d")
			require.True(t, ok, "本回归假定 VL 以 `d` 为单位给出上限，实得 %q；请同步更新解析", raw)
			n, parseErr := strconv.Atoi(days)
			require.NoError(t, parseErr, "无法解析 VL 的 maxQueryTimeRange 天数：%s", raw)
			return time.Duration(n) * 24 * time.Hour
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
	limit := vlMaxQueryTimeRange(t)

	// ① 纯时间算术：按被测代码的窗口构造方式复算总跨度。
	now := time.Now().UTC()
	end := now.Add(projectionProbeFutureSlack)
	start := end.Add(-projectionProbeSpan)
	require.LessOrEqual(t, end.Sub(start), limit,
		"探测窗总跨度 %s 越过 VL 上限 %s；VL 会以 400 拒绝，重名防护将整条失效", end.Sub(start), limit)
	// 顺带守住「上界确实覆盖了未来行」这一面（否则本回归会在另一个方向上失去意义）。
	require.Greater(t, end.Sub(now), time.Duration(0),
		"上界必须晚于 now：无日期日志行的事件时间可合法落在未来，取 now 会让探测对其失明")
	// 起点必须仍覆盖 HOT retention 全期（30d）：这是 2026-10-10 扩窗的目的。
	require.GreaterOrEqual(t, now.Sub(start), 30*24*time.Hour,
		"起点回看量不足 30d（HOT retention 全期）：对更早的重放会结构性失明")

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
	for _, span := range spans {
		require.LessOrEqual(t, span, limit,
			"实际探测请求的窗口跨度 %s 越过 VL 上限 %s（生产上会被 400 拒绝并静默关闭重名防护）", span, limit)
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
