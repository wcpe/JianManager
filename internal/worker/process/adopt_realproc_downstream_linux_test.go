//go:build linux

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
	"github.com/wcpe/JianManager/internal/worker/metrics"
)

// 验收缺口 ②：收养之后的三条下游链路，全部走**真实进程**。
//
// 被补的缺口原文：「收养完成后的心跳 RUNNING / 健康巡检 / Stop 链路只有单测，且用的是 `sleep`
// 假进程」。三条链路各自的 `sleep` 失真点：
//   - 心跳：不算失真（快照源自内存表），但此前只断言过内存表，没走真 java 进程对；
//   - 巡检：严重失真——`sleep` 不监听端口，探针恒失败，「巡检覆盖到它了」与「探针不可达」在
//     断言上不可区分，而这两件事的运维含义完全不同（前者是缺陷，后者是缺探针）；
//   - Stop：严重失真——`sleep` 不吃 stdin，wrapper 的 `stop` 控制帧无人消费，只能等优雅超时后
//     强杀，于是「停止走的是控制帧还是强杀」这个规格一致性问题根本观察不到。
//
// 下面的用例一律经由真实 wrapper 子进程 + 真实 java 夹具（testdata/mcstub），不注入任何桩。

// TestAdoptedHeartbeatReportsRunning 收养后心跳快照必须报 RUNNING，且 PID 是夹具真实 PID。
//
// 这条链路是 CP 面板从「无此实例」收敛回来的唯一入口：心跳是 CP 认状态的唯一真源
// （除此之外，实例清单本身由 `GetAllInstanceStates` 提供）。若收养登记了却没进快照，CP 依旧看不见它。
func TestAdoptedHeartbeatReportsRunning(t *testing.T) {
	h := newRealAdoptHarness(t, 0)
	h.adopt(t)

	rec := waitPIDRecord(t, h.pidDir, h.uuid)
	snap := h.m.GetAllInstanceStates()
	require.Len(t, snap, 1, "收养后心跳快照必须包含该实例")
	got := snap[0]
	require.Equal(t, h.uuid, got.UUID)
	require.Equal(t, string(StateRunning), got.State, "收养后心跳必须上报 RUNNING（CP 面板据此收敛）")
	// daemon 模式下受管根进程是 wrapper：`GetAllInstanceStates` 取 `strategy.GetPID()`，
	// 而收养只把已存在的 wrapper 接回来，故此处必然等于 PID 记录里的 wrapper PID。
	require.Equal(t, rec.WrapperPID, got.PID, "心跳 PID 必须是真实 wrapper PID（不是夹具编造的值）")
	require.Equal(t, h.wrapper.PID, got.PID)
	require.True(t, isPIDAliveLocal(got.PID), "上报的 PID 必须真的存活")
	require.True(t, isPIDAliveLocal(rec.JavaPID), "被托管的 java 也必须仍存活（收养未碰进程）")
}

// TestAdoptedHealthScanCoversHealthy 收养实例被真巡检**检查到**且判为健康（探针可达形态）。
//
// 这里要同时钉住两件事，缺一不可：
//  1. 巡检没有跳过它（`scanOne` 对 `state != RUNNING` 直接跳过；若收养没把状态记成 RUNNING，
//     它会静默地不被检查——覆盖为零，且没有任何日志能看出问题）；
//  2. 探针真的探到了：`Healthy=true` 只能由「存活 ∧ 响应」得出，而响应探针打的是 java 夹具
//     真实监听的 `/metrics`（`HTTPHealthProbe` 真实 HTTP GET）。若有人把响应维度退化成
//     「存活即健康」，`Readiness` 与 `Healthy` 仍为 true，但下面「停掉监听后必须转不健康」的
//     姊妹用例会转红——两条一起才算把响应维度钉住。
func TestAdoptedHealthScanCoversHealthy(t *testing.T) {
	port := freeTCPPort(t)
	h := newRealAdoptHarness(t, port)

	// 前置：夹具的 /metrics 必须真的可达，否则「响应维度通过」可能是「无可用探针端口」的
	// 兼容分支（health_scan.go:878 的 port<=0 兜底）而非真的探到了。两者含义完全不同。
	require.Eventually(t, func() bool { return metrics.HTTPHealthProbe(healthScanProbeHost, port) == nil },
		waitJavaListen(t), 50*time.Millisecond, "前置：java 夹具的 /metrics 必须真的可达")

	inst := h.adopt(t)
	// 响应维度只能落在 ProbePort 上（记录里没有 ServerPort）——这正是真机上收养实例的形态。
	require.Zero(t, inst.ServerPort, "收养记录不含 ServerPort，这是既有形态")
	require.Equal(t, port, inst.ProbePort)

	// 与生产装配同口径：本地开、默认策略、真探针（不注入 probeTCP/probeHTTP/probeLiveness）。
	s := NewHealthScanner(h.m, true, time.Minute, DefaultHealthPolicy())

	// 先证明「巡检确实走到了这个实例」：用报告条数 + UUID 判定覆盖，而不是只看健康位——
	// 只看健康位会把「根本没被检查」误当「检查后健康」。
	reports := s.ScanOnce()
	require.Len(t, reports, 1, "收养实例必须进入巡检目标集")
	require.Equal(t, h.uuid, reports[0].UUID)

	rep := reports[0]
	require.True(t, rep.Liveness, "存活维度应通过（wrapper 真存活）")
	require.True(t, rep.Readiness, "响应维度应通过（java 夹具在 %d 上真的返回 200）", port)
	require.True(t, rep.Healthy)
	fault, reason := h.m.healthFault(h.uuid)
	require.Equal(t, HealthFaultHealthy, fault, "健康巡检应写入 healthy（心跳据此清 status_reason）")
	require.Empty(t, reason)

	// 连续多拍都稳定为健康：排除「首拍偶然通过」。
	for i := 0; i < 2; i++ {
		require.True(t, s.ScanOnce()[0].Healthy, "第 %d 拍仍应健康", i+2)
	}
	// 健康实例不得产生任何处置审计——只算 health.* 类，收养自己的审计不在此列。
	require.Zero(t, countPrefixedAudit(*h.audits, "health."), "健康实例不该落任何处置审计：%+v", *h.audits)
	require.Equal(t, StateRunning, instStateOf(t, h.m, h.uuid), "巡检不得改动健康实例的状态")
}

// TestAdoptedHealthScanDeadWhenProbeUnreachable 探针不可达形态：3 拍后判 HealthFaultDead。
//
// 这条同时是「收养实例没有 ServerPort」这一**真机隐患的可执行记录**（详见下面的宽限期用例）：
// 收养记录里只有 ProbePort（来自 PID 文件）、没有 ServerPort，响应维度于是只能落在 ProbePort 上；
// 未部署 ServerProbe 的实例该端口无人监听 → 恒失败 → 到期判假死。这是当前实现的**既有行为**，
// 本用例把它钉成可回归的事实，而不是假装它不存在。
func TestAdoptedHealthScanDeadWhenProbeUnreachable(t *testing.T) {
	// 记录里声明了 ProbePort，但**无人监听**（未部署 ServerProbe 的真实形态）：响应维度必然失败。
	deadPort := freeTCPPort(t)
	h := newRealAdoptHarnessPorts(t, deadPort, 0)
	h.adopt(t)

	s := NewHealthScanner(h.m, true, time.Minute, DefaultHealthPolicy())
	pol := DefaultHealthPolicy()
	pol.StartupWarmup = -1 // 显式关闭宽限：本用例要验的是「无宽限时的判定」，不是宽限期本身
	s.SetPolicy(pol)

	var last HealthReport
	for i := 0; i < DefaultSuspicionThreshold; i++ {
		reports := s.ScanOnce()
		require.Len(t, reports, 1)
		last = reports[0]
		require.True(t, last.Liveness, "存活 ∧ 不响应 = 假死，不是崩溃")
		require.False(t, last.Readiness)
		if i < DefaultSuspicionThreshold-1 {
			require.Equal(t, HealthFaultSuspected, last.Fault, "未达阈值只标嫌疑（第 %d 拍）", i+1)
		}
	}
	require.Equal(t, HealthFaultDead, last.Fault, "连续 %d 拍响应失败应判假死", DefaultSuspicionThreshold)
	require.Contains(t, last.Reason, "假死")
	fault, _ := h.m.healthFault(h.uuid)
	require.Equal(t, HealthFaultDead, fault, "假死必须经心跳上报 CP")
	require.Contains(t, last.Actions, HealthActionWarn, "默认策略下假死只告警（不重启）")
	require.Equal(t, 1, h.countAudit("health.dead_detected"))
}

// TestAdoptedHealthScanHasStartupWarmup 收养实例**必须有**启动宽限期（缺陷回归）。
//
// 缺陷形态（补验时在真机上确认）：收养的两条登记路径都没有写 `Instance.StartedAt`，而
// `warmupRemaining` 在纪元为零值时返回 0 —— 「做了宽限」变成「完全没有宽限」。叠加收养实例的另一
// 个特征（PID 记录只有 ProbePort，未部署 ServerProbe 时无人监听），一个**刚刚才被接管、完全可能
// 正在正常服务**的服务器会在 3 拍（默认 30s 一拍 ≈ 90s）后被判 `HealthFaultDead`；若 CP 把动作
// 下发成 restart，巡检还会去重启它（实际被 `launchSpecPending` 拦住，但那是另一道闸，不该是唯一
// 一道）。**把「刚接管」判成「假死」是错误结论**：宽限期内的语义必须是「不明」。
//
// 转红方式（改回缺陷行为）：删掉 adopt_orphan.go 两条登记路径里的 `StartedAt`（以及
// recover_orphan.go registerAdoptedDaemon 里那条），本用例立即转红——第 1 拍就拿到
// fault=suspected_dead 而不是 Warmup。
func TestAdoptedHealthScanHasStartupWarmup(t *testing.T) {
	deadPort := freeTCPPort(t)
	h := newRealAdoptHarnessPorts(t, deadPort, 0)
	h.adopt(t)

	inst, _ := h.m.GetInstance(h.uuid)
	require.False(t, inst.StartedAt.IsZero(), "收养登记必须写入启动宽限期纪元（StartedAt）")

	// 用真时钟（不注入 now）：验证的是「现在这一拍距纪元足够近」。
	s := NewHealthScanner(h.m, true, time.Minute, DefaultHealthPolicy())
	reports := s.ScanOnce()
	require.Len(t, reports, 1)
	rep := reports[0]
	require.True(t, rep.Warmup, "刚接管的实例必须在启动宽限期内（否则慢启动会被判假死）")
	require.NotEqual(t, HealthFaultDead, rep.Fault, "宽限期内绝不能判假死")
	require.NotEqual(t, HealthFaultSuspected, rep.Fault, "宽限期内不该累积假死嫌疑")
	require.Empty(t, rep.Actions, "宽限期内不得有任何处置动作")
	require.Zero(t, h.countAudit("health.dead_detected"))
	require.Zero(t, h.countAudit("health.selfheal_restart"))

	// 连跑多拍仍是宽限（默认 5 分钟，远超本用例耗时）。
	for i := 0; i < 4; i++ {
		require.True(t, s.ScanOnce()[0].Warmup, "第 %d 拍仍应在宽限期内", i+2)
	}
	require.Zero(t, h.countAudit("health.dead_detected"), "宽限期内不得落假死审计")

	// 姊妹断言：宽限期一过，同一实例照常被判（宽限是「延后」而不是「豁免」）。
	pol := DefaultHealthPolicy()
	pol.StartupWarmup = -1
	s.SetPolicy(pol)
	for i := 0; i < DefaultSuspicionThreshold; i++ {
		s.ScanOnce()
	}
	fault, _ := h.m.healthFault(h.uuid)
	require.Equal(t, HealthFaultDead, fault, "宽限期过后必须恢复正常判定（否则是掩盖而非宽限）")
}

// TestAdoptedStopGoesThroughWrapperControlFrame 收养后 Stop：
//   - java 进程真的退出、实例记 STOPPED；
//   - 没有误杀无关进程；
//   - **并记录停止走的是哪条路径**（wrapper 控制帧 vs 强杀）——这是规格一致性问题。
//
// 关于「走哪条路径」的实现事实（daemon.go:298 Stop / :316 ControlStop / :337 即时拨号 / :343 强杀）：
// 收养路径的 `SetWrapperPID` 只填了 `os.FindProcess(pid)` 占位句柄，不带真实 `*exec.Cmd`，故
// 「强杀兜底」在收养实例上实际只能发一个单进程信号。真正的判据是**控制连接在不在**：
//   - 收养拨号成功 → `d.conn != nil` → 发送 `ControlStop` 控制帧 → wrapper 把 stop 写进 java 的
//     stdin → java 夹具打印 `console command: stop` 并走优雅关服序列。这是**规格期望**的路径。
//
// 本用例的观测点与它为何能区分两条路径：
// java 夹具把停止过程**同时**写进自己的日志文件（`--log`，模拟真实 MC 的 logs/latest.log，
// 也正是平台采集侧读取的观测面）。于是：
//   - 控制帧路径：stop 被写进 java 的 stdin，java 打印 `console command: stop` 并走关服序列
//     （`Saving worlds complete` 是走完的凭证）——这只能由被托管进程自己产生；
//   - 强杀路径：进程直接被信号杀掉，日志里不会有关服序列；若 wrapper 走的是「优雅超时后强杀」，
//     wrapper 自己的日志会打出 `优雅停止超时，强制终止 Java`。
//
// 为什么要用日志文件而不是 `Manager.SetOutputHandler` 的 stdio：wrapper 收到 stop 后立即关闭控制
// 连接（readLoop 退出），java 关服序列的最后几行常常来不及经 stdio 帧转发回 Worker。用 stdio 做
// 断言会把「转发还没跟上」误判成「停止没走控制帧」——那是夹具的观测面选错了，不是产品缺陷。
func TestAdoptedStopGoesThroughWrapperControlFrame(t *testing.T) {
	h := newRealAdoptHarness(t, 0)
	inst := h.adopt(t)

	rec := waitPIDRecord(t, h.pidDir, h.uuid)
	javaPID := rec.JavaPID
	wrapperPID := rec.WrapperPID
	decoyPID := startDecoy(t) // 一个与实例完全无关的存活进程

	// 前置：采集侧确实看得到实例日志（否则下面的「没有关服日志」不能说明任何事）。
	before := waitFileContains(t, h.javaLog, "Done (0.421s)!", waitJavaListen(t))

	// 确认夹具此刻确实在跑。
	require.True(t, isPIDAliveLocal(javaPID))
	require.True(t, isPIDAliveLocal(wrapperPID))

	// 优雅停止超时压到 1s 级：万一控制帧没被消费，用例不至于等 30s 才发现。
	t.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "1s")
	require.NoError(t, h.m.StopContext(t.Context(), h.uuid))

	// ① 停止路径：java 夹具收到 stdin 的 stop 并走完整优雅关服序列（只可能来自 wrapper 控制帧）。
	after := waitFileContains(t, h.javaLog, "Saving worlds complete", 15*time.Second)
	require.Contains(t, after, "console command: stop", "停止必须是 wrapper 控制帧写进 java stdin：\n%s", after)
	require.Contains(t, after, "Stopping the server (console-stop)", "应走优雅关服序列：\n%s", after)
	require.Greater(t, len(after), len(before), "关服序列必须是在停止动作之后追加的")
	// 强杀路径的反例日志：wrapper 在优雅超时后强杀 Java 会打出这条。
	require.NotContains(t, readFileOrEmpty(h.wrapper.LogPath), "优雅停止超时，强制终止 Java",
		"不得退化为超时强杀")

	// ② java 真的退出（不是「Manager 认为它退了」）。
	require.Eventuallyf(t, func() bool { return !isPIDAliveLocal(javaPID) },
		15*time.Second, 50*time.Millisecond, "Stop 后 java 必须退出，wrapper 日志：\n%s", readFileOrEmpty(h.wrapper.LogPath))

	// ③ 实例记账为 STOPPED，stale 策略被丢弃（下一代按完整规格重建）。
	require.Equal(t, StateStopped, instStateOf(t, h.m, h.uuid))
	require.Nil(t, inst.strategy, "stale 策略在 Stop 后被丢弃")

	// ④ 无关进程没被误杀。
	require.True(t, isPIDAliveLocal(decoyPID), "Stop 不得波及无关进程（PID 复用/进程组串杀的经典事故面）")

	// ⑤ wrapper 自身随后收摊（AutoRestart=false），PID 文件随之清理——收养链条能干净收尾。
	require.Equal(t, wrapperPID, h.wrapper.PID)
	require.True(t, h.wrapper.waitExit(15*time.Second), "wrapper 应在 java 退出后收摊，日志：\n%s", readFileOrEmpty(h.wrapper.LogPath))
	require.False(t, pidFileExists(daemon.PIDFileName(h.pidDir, h.uuid)), "wrapper 退出时应清理 PID 文件")
}

// TestAdoptedStopKeepsUnrelatedDaemon 负例侧写：让**另一个**实例（同 pidDir、不同 UUID）
// 在收养实例被 Stop 时保持运行，钉住「Stop 只作用于目标实例」。
//
// 为什么值得单独一条：收养与 Stop 都按 PID/进程组操作，而 wrapper 的 `KillPIDTree` 走的是
// `kill(-pgid)`——一旦两个 wrapper 落在同一进程组（例如都没有 setsid），停一个就会带走另一个。
// 生产里 wrapper 由 `setDetachedAttr` 保证独立会话，但收养路径**没有**重新 spawn，因此这条属性
// 是否真的成立只能实测。
func TestAdoptedStopKeepsUnrelatedDaemon(t *testing.T) {
	other := newRealAdoptHarness(t, 0)
	other.adopt(t)
	otherRec := waitPIDRecord(t, other.pidDir, other.uuid)

	h := newRealAdoptHarness(t, 0)
	h.adopt(t)
	rec := waitPIDRecord(t, h.pidDir, h.uuid)

	t.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "1s")
	require.NoError(t, h.m.StopContext(t.Context(), h.uuid))
	require.Eventually(t, func() bool { return !isPIDAliveLocal(rec.JavaPID) }, 15*time.Second, 50*time.Millisecond)

	require.True(t, isPIDAliveLocal(otherRec.WrapperPID), "另一个实例的 wrapper 必须存活")
	require.True(t, isPIDAliveLocal(otherRec.JavaPID), "另一个实例的 java 必须存活")
	require.Equal(t, StateRunning, instStateOf(t, other.m, other.uuid))
}

// instStateOf 读实例记账状态（不存在返回 StateStopped）。
func instStateOf(t *testing.T, m *Manager, uuid string) InstanceState {
	t.Helper()
	inst, ok := m.GetInstance(uuid)
	if !ok {
		return StateStopped
	}
	return inst.State
}

// waitJavaListen 返回「等 java 夹具监听就绪」的预算：JVM 冷启动 + 类加载在慢机上可达数秒。
func waitJavaListen(t *testing.T) time.Duration {
	t.Helper()
	if v := os.Getenv("JM_ADOPT_JAVA_LISTEN_BUDGET_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 20 * time.Second
}

// dumpHarness 排障助手：把现场关键信息拼成一行（断言失败信息里用，避免「条件不满足」这种空话）。
func dumpHarness(h *realAdoptHarness) string {
	return fmt.Sprintf("uuid=%s pidDir=%s workDir=%s wrapperPid=%d javaPid=%d log=%s",
		h.uuid, h.pidDir, h.workDir, h.wrapper.PID, javaPIDOfRaw(filepath.Join(h.pidDir, h.uuid+".pid")), h.wrapper.LogPath)
}

// javaPIDOfRaw 从 PID 文件读 Java PID（无容错版本，供排障用）。
func javaPIDOfRaw(pidPath string) int {
	rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
	if err != nil || rec == nil {
		return 0
	}
	return rec.JavaPID
}

// countPrefixedAudit 统计 action 以 prefix 开头的审计条数（区分「收养类」与「巡检类」）。
func countPrefixedAudit(audits []scanAudit, prefix string) int {
	n := 0
	for _, a := range audits {
		if strings.HasPrefix(a.action, prefix) {
			n++
		}
	}
	return n
}
