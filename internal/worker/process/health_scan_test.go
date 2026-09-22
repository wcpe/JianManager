package process

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- FR-459 巡检/自愈/熔断单测辅助 ---

// addInstance 直接登记一个实例（绕过 Create 的长签名），供巡检测试构造运行时态。
func addInstance(m *Manager, uuid string, state InstanceState, autoRestart bool, probePort, serverPort int) {
	m.mu.Lock()
	m.instances[uuid] = &Instance{
		UUID:        uuid,
		State:       state,
		AutoRestart: autoRestart,
		ProbePort:   probePort,
		ServerPort:  serverPort,
	}
	m.mu.Unlock()
}

// newHealthTestScanner 构造带注入桩的巡检器：存活恒真、响应由 responsive 指针控制、重启记录在 restarts。
func newHealthTestScanner(t *testing.T, action string, threshold int, alive *bool, responsive *bool) (*HealthScanner, *Manager, *[]scanAudit, *[]string) {
	t.Helper()
	return newHealthTestScannerOn(t, NewManager(t.TempDir()), action, threshold, alive, responsive)
}

// newHealthTestScannerOn 同 newHealthTestScanner，但把巡检器挂到调用方给定的 Manager 上
// （供需要预先在 Manager 上造崩溃窗口/记账的用例复用）。
func newHealthTestScannerOn(t *testing.T, m *Manager, action string, threshold int, alive *bool, responsive *bool) (*HealthScanner, *Manager, *[]scanAudit, *[]string) {
	t.Helper()
	audits := &[]scanAudit{}
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		*audits = append(*audits, scanAudit{action: action, targetID: targetID, detail: detail, success: success})
	}
	pol := DefaultHealthPolicy()
	pol.Action = action
	pol.SuspicionThreshold = threshold
	s := NewHealthScanner(m, true, time.Minute, pol)
	s.probeLiveness = func(uuid string) InstanceEvidence { return InstanceEvidence{UUID: uuid, Running: *alive} }
	probe := func(int) bool { return *responsive }
	s.probeTCP = probe
	s.probeHTTP = probe
	restarts := &[]string{}
	s.restart = func(uuid string) error { *restarts = append(*restarts, uuid); return nil }
	return s, m, audits, restarts
}

func hasAudit(audits []scanAudit, action string) bool {
	for _, a := range audits {
		if a.action == action {
			return true
		}
	}
	return false
}

// 验收 1：连续 N 次判假死 → 标原因（warn 档仅告警 + 落审计）。
func TestHealthScan_DeadDetected_Warn(t *testing.T) {
	alive, responsive := true, false
	s, m, audits, restarts := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-dead", StateRunning, true, 0, 25566)

	// 第 1、2 次：仅嫌疑累积，未达阈值。
	for i := 0; i < 2; i++ {
		s.ScanOnce()
	}
	fault, _ := m.healthFault("u-dead")
	require.Equal(t, HealthFaultSuspected, fault, "未达阈值只标嫌疑")
	assert.False(t, hasAudit(*audits, "health.dead_detected"), "未达阈值不落处置审计")

	// 第 3 次：达阈值 → 判定假死 + 标原因 + 落审计（warn 档）。
	s.ScanOnce()
	fault, reason := m.healthFault("u-dead")
	assert.Equal(t, HealthFaultDead, fault)
	assert.Contains(t, reason, "假死")
	assert.True(t, hasAudit(*audits, "health.dead_detected"), "warn 档应落检测审计")
	assert.Empty(t, *restarts, "warn 档不得重启")
}

// 验收 2（单测层面）：action=restart 时假死实例被优雅重启（走注入的 RestartIfRunning 桩）。
func TestHealthScan_DeadDetected_Restart(t *testing.T) {
	alive, responsive := true, false
	s, m, audits, restarts := newHealthTestScanner(t, HealthActionRestart, 3, &alive, &responsive)
	addInstance(m, "u-restart", StateRunning, true, 0, 25566)

	for i := 0; i < 3; i++ {
		s.ScanOnce()
	}
	require.Equal(t, []string{"u-restart"}, *restarts, "达阈值应触发一次优雅重启")
	assert.True(t, hasAudit(*audits, "health.selfheal_restart"), "自愈重启应落审计")
	// 重启成功后嫌疑计数复位，下一轮重新计数（不立刻再触发）。
	assert.False(t, hasAudit(*audits, "health.dead_detected"))
}

// 验收 3：阈值抖动——偶发一次探测失败不触发动作。
func TestHealthScan_JitterDoesNotTrigger(t *testing.T) {
	alive, responsive := true, false
	s, m, _, restarts := newHealthTestScanner(t, HealthActionRestart, 3, &alive, &responsive)
	addInstance(m, "u-jitter", StateRunning, true, 0, 25566)

	s.ScanOnce() // 失败 1 → 嫌疑 1
	responsive = true
	s.ScanOnce() // 恢复 → 清零
	responsive = false
	s.ScanOnce() // 失败 1（重新计数）→ 嫌疑 1

	fault, _ := m.healthFault("u-jitter")
	assert.Equal(t, HealthFaultSuspected, fault, "偶发抖动不应累积到阈值")
	assert.Empty(t, *restarts, "抖动不得触发重启")
}

// 验收 3 补充：连续失败达阈值后恢复，嫌疑清零。
func TestHealthScan_RecoveryClearsSuspicion(t *testing.T) {
	alive, responsive := true, false
	s, m, audits, restarts := newHealthTestScanner(t, HealthActionRestart, 3, &alive, &responsive)
	addInstance(m, "u-recover", StateRunning, true, 0, 25566)

	s.ScanOnce()
	s.ScanOnce()
	responsive = true
	s.ScanOnce() // 恢复

	fault, _ := m.healthFault("u-recover")
	assert.Equal(t, HealthFaultHealthy, fault, "响应恢复应标健康")
	assert.Empty(t, *restarts)
	assert.False(t, hasAudit(*audits, "health.dead_detected"))
}

// 验收 4（单测层面）：持续崩溃实例在窗口内重启超限即熔断。
func TestHealthScan_CircuitBreakerTrips(t *testing.T) {
	alive, responsive := true, true
	s, m, audits, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-crash", StateCrashed, true, 0, 25566)

	// 窗口内 5 次崩溃（达默认阈值 5）。
	for i := 0; i < 5; i++ {
		m.emitCrash("u-crash", CrashInfo{ExitCode: 1, OccurredAt: time.Now()})
	}

	s.ScanOnce()

	inst, ok := m.GetInstance("u-crash")
	require.True(t, ok)
	assert.False(t, inst.AutoRestart, "熔断应停止自动重启")
	assert.Equal(t, StateCrashed, inst.State, "熔断应置 CRASHED")
	fault, reason := m.healthFault("u-crash")
	assert.Equal(t, HealthFaultCircuitBroken, fault)
	assert.Contains(t, reason, "熔断")
	assert.True(t, hasAudit(*audits, "health.circuit_broken"), "熔断应落审计")
	assert.False(t, m.autoRestartAllowed("u-crash"), "熔断期间不允许自动重启")
}

// 验收 4 补充：熔断窗口内重启不足阈值不熔断。
func TestHealthScan_CircuitBreakerBelowThreshold(t *testing.T) {
	alive, responsive := true, true
	s, m, audits, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-crash2", StateCrashed, true, 0, 25566)

	for i := 0; i < 4; i++ {
		m.emitCrash("u-crash2", CrashInfo{ExitCode: 1})
	}
	s.ScanOnce()

	inst, _ := m.GetInstance("u-crash2")
	assert.True(t, inst.AutoRestart, "未超阈值不应熔断")
	assert.False(t, hasAudit(*audits, "health.circuit_broken"))
	fault, _ := m.healthFault("u-crash2")
	assert.Equal(t, HealthFaultCrash, fault)
}

// 验收 5（规格对齐）：熔断**不再**随窗口过期自动解除——spec §5 选择「人工确认解除」，
// 避免崩溃循环在无人状态下被自动放行。窗口过期后仍保持熔断、仍不允许自动重启。
func TestHealthScan_CircuitBreakerNotAutoReleasedOnWindowExpiry(t *testing.T) {
	alive, responsive := true, true
	s, m, audits, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-rel", StateCrashed, true, 0, 25566)

	now := time.Now()
	m.healthState().now = func() time.Time { return now }
	s.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		m.emitCrash("u-rel", CrashInfo{ExitCode: 1})
	}
	s.ScanOnce()
	require.False(t, m.autoRestartAllowed("u-rel"), "应先熔断")

	// 推进时钟越过熔断窗口（默认 10m）：仍应保持熔断（仅人工可解除）。
	now = now.Add(11 * time.Minute)
	s.ScanOnce()

	assert.False(t, m.autoRestartAllowed("u-rel"), "窗口过期不自动解除熔断")
	inst, _ := m.GetInstance("u-rel")
	assert.False(t, inst.AutoRestart, "自动重启仍被熔断关闭")
	fault, reason := m.healthFault("u-rel")
	assert.Equal(t, HealthFaultCircuitBroken, fault)
	assert.Contains(t, reason, "熔断")
	assert.False(t, hasAudit(*audits, "health.circuit_released"), "不应出现自动解除审计")
}

// 验收 5（规格对齐）：熔断只经**显式人工解除**入口恢复（ReleaseCircuitBreaker），
// 且任意配置编辑（SetLaunchConfig）都不得静默清熔断（FR-459 复审项 6）。
func TestHealthScan_CircuitBreakerManualRelease(t *testing.T) {
	alive, responsive := true, true
	s, m, audits, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-man", StateCrashed, true, 0, 25566)

	for i := 0; i < 5; i++ {
		m.emitCrash("u-man", CrashInfo{ExitCode: 1})
	}
	s.ScanOnce()
	require.False(t, m.autoRestartAllowed("u-man"))

	// 配置编辑（重注册下发 autoRestart=true）**不得**再隐式解除熔断。
	m.SetLaunchConfig("u-man", "", "", "", nil, true)
	if _, broken := m.isCircuitBroken("u-man"); !broken {
		t.Fatal("配置编辑不得静默清除熔断（breaker 与 AutoRestart 已解耦）")
	}
	assert.False(t, m.autoRestartAllowed("u-man"), "配置编辑后熔断仍有效")

	// 人工确认解除（CP 人工启动路径）。
	wasReason, released := m.ReleaseCircuitBreaker("u-man")
	assert.True(t, released)
	assert.Contains(t, wasReason, "熔断")
	_, broken := m.isCircuitBroken("u-man")
	assert.False(t, broken, "人工解除后熔断锁清除")
	assert.True(t, m.autoRestartAllowed("u-man"), "人工解除后恢复自动重启")
	inst, _ := m.GetInstance("u-man")
	assert.True(t, inst.AutoRestart, "人工解除后恢复自动重启配置")
	fault, _ := m.healthFault("u-man")
	assert.Equal(t, HealthFaultNone, fault, "人工解除后清健康故障标识")
	assert.True(t, hasAudit(*audits, "health.circuit_released"), "人工解除应落审计")
	for _, a := range *audits {
		if a.action == "health.circuit_released" {
			assert.Contains(t, a.detail, `"operator":"manual"`, "人工解除审计应标 operator=manual")
		}
	}

	// 幂等：未熔断时解除返回 released=false。
	_, released = m.ReleaseCircuitBreaker("u-man")
	assert.False(t, released)
}

// 验收 6：正常退出（stop）/崩溃不被巡检误判为假死。
func TestHealthScan_NoFalsePositiveOnStopOrCrash(t *testing.T) {
	alive, responsive := true, true
	s, m, audits, restarts := newHealthTestScanner(t, HealthActionRestart, 3, &alive, &responsive)

	addInstance(m, "u-stopped", StateStopped, true, 0, 25566)
	addInstance(m, "u-starting", StateStarting, true, 0, 25566)
	addInstance(m, "u-stopping", StateStopping, true, 0, 25566)
	// 崩溃但未熔断（AutoRestart 开启、窗口内重启不足阈值）。
	addInstance(m, "u-crashed", StateCrashed, true, 0, 25566)

	for i := 0; i < 5; i++ {
		s.ScanOnce()
	}

	for _, uuid := range []string{"u-stopped", "u-starting", "u-stopping", "u-crashed"} {
		fault, _ := m.healthFault(uuid)
		assert.NotEqual(t, HealthFaultDead, fault, "非运行态不得判假死：%s", uuid)
		assert.NotEqual(t, HealthFaultSuspected, fault, "非运行态不得判假死嫌疑：%s", uuid)
	}
	assert.False(t, hasAudit(*audits, "health.dead_detected"), "正常退出/崩溃不得落假死审计")
	assert.False(t, hasAudit(*audits, "health.selfheal_restart"), "正常退出/崩溃不得触发假死重启")
	assert.Empty(t, *restarts)

	// 人工关闭自动重启的崩溃实例：不判熔断（非本巡检导致）。
	alive2, resp2 := true, true
	s2, m2, audits2, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive2, &resp2)
	addInstance(m2, "u-noauto", StateCrashed, false, 0, 25566)
	for i := 0; i < 5; i++ {
		m2.emitCrash("u-noauto", CrashInfo{ExitCode: 1})
	}
	s2.ScanOnce()
	fault, _ := m2.healthFault("u-noauto")
	assert.Equal(t, HealthFaultCrash, fault, "人工关自动重启的崩溃不判熔断")
	assert.False(t, hasAudit(*audits2, "health.circuit_broken"))
}

// 验收 9：开关关闭后巡检不产生任何动作（含被 CP 远程开启也不生效）。
func TestHealthScan_DisabledProducesNoAction(t *testing.T) {
	m := NewManager(t.TempDir())
	audits := &[]scanAudit{}
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		*audits = append(*audits, scanAudit{action: action})
	}
	// 本地硬关。
	s := NewHealthScanner(m, false, time.Minute, DefaultHealthPolicy())
	s.probeLiveness = func(uuid string) InstanceEvidence { return InstanceEvidence{UUID: uuid, Running: true} }
	s.probeTCP = func(int) bool { return false }
	addInstance(m, "u-off", StateRunning, true, 0, 25566)

	// 即使 CP 下发 enabled=true，本地硬关仍生效。
	s.SetPolicy(HealthPolicy{Enabled: true})
	for i := 0; i < 5; i++ {
		assert.Empty(t, s.ScanOnce(), "关闭开关不得产生巡检结论")
	}
	fault, _ := m.healthFault("u-off")
	assert.Equal(t, HealthFaultNone, fault)
	assert.Empty(t, *audits, "关闭开关不得落任何审计")
}

// CP 下发 enabled=false 时同样不产生动作（本地开启 ∧ CP 关闭）。
func TestHealthScan_CPDisabledProducesNoAction(t *testing.T) {
	alive, responsive := true, false
	s, m, audits, _ := newHealthTestScanner(t, HealthActionRestart, 3, &alive, &responsive)
	addInstance(m, "u-cpoff", StateRunning, true, 0, 25566)
	s.SetPolicy(HealthPolicy{Enabled: false})

	for i := 0; i < 5; i++ {
		assert.Empty(t, s.ScanOnce())
	}
	assert.Empty(t, *audits)
	fault, _ := m.healthFault("u-cpoff")
	assert.Equal(t, HealthFaultNone, fault)
}

// 存活但不响应且无可用探针维度（无端口）→ 仅存活即健康（spec §5 兜底，不误判）。
func TestHealthScan_NoProbeTargetTreatedHealthy(t *testing.T) {
	alive, responsive := true, false
	s, m, audits, _ := newHealthTestScanner(t, HealthActionRestart, 3, &alive, &responsive)
	addInstance(m, "u-noport", StateRunning, true, 0, 0)

	for i := 0; i < 5; i++ {
		s.ScanOnce()
	}
	fault, _ := m.healthFault("u-noport")
	assert.Equal(t, HealthFaultHealthy, fault, "无可探维度仅存活即健康")
	assert.Empty(t, *audits)
}

// 进程不存活（存活维为假）→ 判崩溃，不判假死（交由既有崩溃处理）。
func TestHealthScan_LivenessFalseIsCrashNotDead(t *testing.T) {
	alive, responsive := false, false
	s, m, audits, restarts := newHealthTestScanner(t, HealthActionRestart, 3, &alive, &responsive)
	addInstance(m, "u-gone", StateRunning, true, 0, 25566)

	s.ScanOnce()
	fault, _ := m.healthFault("u-gone")
	assert.Equal(t, HealthFaultCrash, fault)
	assert.False(t, hasAudit(*audits, "health.dead_detected"))
	assert.Empty(t, *restarts)
}

// RestartIfRunning 仅对 RUNNING 动作（避免把人工已停止/过渡态的实例误重启，FR-459 复审项 4）。
func TestRestartIfRunning_RefusesStopped(t *testing.T) {
	m := NewManager(t.TempDir())
	addInstance(m, "u-stop", StateStopped, true, 0, 0)
	err := m.RestartIfRunning("u-stop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "非运行中")
}

// 过渡态（STARTING/STOPPING）与 CRASHED 同样拒绝：与 spec §5「自愈不得与人工操作抢状态」一致。
func TestRestartIfRunning_RefusesTransitionalStates(t *testing.T) {
	m := NewManager(t.TempDir())
	for _, st := range []InstanceState{StateStarting, StateStopping, StateCrashed} {
		uuid := "u-" + string(st)
		addInstance(m, uuid, st, true, 0, 0)
		err := m.RestartIfRunning(uuid)
		require.Error(t, err, "过渡态/崩溃态不应被假死自愈重启：%s", st)
		assert.Contains(t, err.Error(), "非运行中")
	}
}

// --- FR-459 复审 blocker：daemon 崩溃熔断必须对所有在册实例生效 ---

// daemon 下 Java 崩溃时 wrapper 存活、只上抛退出事件，Worker 记账**恒为 RUNNING**；
// 崩溃熔断的判定必须对所有在册实例（含 RUNNING）评估，否则判定永不执行 = 熔断完全不成立。
// 同时验证熔断的 daemon **生效**路径：向策略下发「禁用自动重启」控制帧。
func TestHealthScan_CircuitBreakerTripsOnRunningDaemonCrashLoop(t *testing.T) {
	alive, responsive := true, true
	s, m, audits, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-daemon", StateRunning, true, 0, 25566)

	// 模拟 daemon 自愈策略（wrapper 控制帧能力）并置 RUNNING 稳态。
	fake := &fakeStrategy{state: StateRunning}
	m.mu.Lock()
	m.instances["u-daemon"].strategy = fake
	m.instances["u-daemon"].processType = ProcessTypeDaemon
	m.mu.Unlock()

	// wrapper 每次 Java 崩溃上抛退出事件 → emitCrash 计数（Worker 记账不变，仍 RUNNING）。
	for i := 0; i < 5; i++ {
		m.emitCrash("u-daemon", CrashInfo{ExitCode: 1, OccurredAt: time.Now()})
	}

	s.ScanOnce()

	inst, ok := m.GetInstance("u-daemon")
	require.True(t, ok)
	assert.False(t, inst.AutoRestart, "RUNNING 实例达阈同样必须熔断并停自动重启")
	_, broken := m.isCircuitBroken("u-daemon")
	assert.True(t, broken, "熔断态独立记账")
	assert.False(t, m.autoRestartAllowed("u-daemon"))
	fault, reason := m.healthFault("u-daemon")
	assert.Equal(t, HealthFaultCircuitBroken, fault)
	assert.Contains(t, reason, "熔断")
	assert.True(t, hasAudit(*audits, "health.circuit_broken"))
	assert.Equal(t, 1, fake.disarmCount, "熔断必须向 daemon 策略下发禁用自动重启控制帧")
	// 存活证据显示进程仍在跑 → 保留真实 RUNNING（不制造反向状态错位）。
	assert.Equal(t, StateRunning, inst.State, "进程侧证据仍在跑时不得伪置 CRASHED")
}

// 熔断后不得再自动重启：direct 自动重启分支的守卫在熔断期间必须为 false（崩溃路径回归）。
func TestHealthScan_CircuitBreakerBlocksAutoRestartGuard(t *testing.T) {
	alive, responsive := true, true
	s, m, _, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-guard", StateRunning, true, 0, 25566)
	for i := 0; i < 5; i++ {
		m.emitCrash("u-guard", CrashInfo{ExitCode: 1})
	}
	s.ScanOnce()
	assert.False(t, m.autoRestartAllowed("u-guard"), "熔断期间 direct/docker 自动重启守卫必须拦住")
}

// --- FR-459 复审 major：启动宽限期 ---

// 慢启动 MC（Start 返回 RUNNING 后仍在加载世界）连续多拍探针失败，不得被误判假死/重启。
func TestHealthScan_StartupWarmupSuppressesDeadDetection(t *testing.T) {
	alive, responsive := true, false
	s, m, audits, restarts := newHealthTestScanner(t, HealthActionRestart, 3, &alive, &responsive)
	addInstance(m, "u-slow", StateRunning, true, 0, 25566)
	now := time.Now()
	s.now = func() time.Time { return now }
	m.mu.Lock()
	m.instances["u-slow"].StartedAt = now
	m.mu.Unlock()

	// 宽限期内（默认 5m）：跑满 3+ 拍（阈值 3）也不得判假死、不得重启。
	for i := 0; i < 5; i++ {
		rep := s.ScanOnce()
		require.Len(t, rep, 1)
		assert.True(t, rep[0].Warmup, "宽限期内应标记 Warmup")
		assert.NotEqual(t, HealthFaultDead, rep[0].Fault, "宽限期内不得判假死")
	}
	assert.Empty(t, *restarts, "宽限期内不得自愈重启")
	assert.False(t, hasAudit(*audits, "health.selfheal_restart"))
	assert.False(t, hasAudit(*audits, "health.dead_detected"))

	// 越过宽限期后仍在假死 → 照常判定并自愈。
	now = now.Add(6 * time.Minute)
	for i := 0; i < 3; i++ {
		s.ScanOnce()
	}
	fault, _ := m.healthFault("u-slow")
	assert.Equal(t, HealthFaultDead, fault, "宽限期过后应正常判定假死")
	assert.Equal(t, []string{"u-slow"}, *restarts)
}

// 崩溃即开启新启动纪元：daemon 下 wrapper 内部重启 Java 后同样被宽限。
func TestHealthScan_WarmupEpochResetsOnCrash(t *testing.T) {
	alive, responsive := true, false
	s, m, _, restarts := newHealthTestScanner(t, HealthActionRestart, 3, &alive, &responsive)
	addInstance(m, "u-epoch", StateRunning, true, 0, 25566)
	base := time.Now()
	// 实例已运行很久（Start 时刻早已越过宽限）……
	m.mu.Lock()
	m.instances["u-epoch"].StartedAt = base.Add(-1 * time.Hour)
	m.mu.Unlock()

	now := base
	s.now = func() time.Time { return now }
	m.healthState().now = func() time.Time { return now }

	// ……但刚刚发生一次崩溃（wrapper 内部重启 Java → 新一轮慢启动）。
	m.emitCrash("u-epoch", CrashInfo{ExitCode: 1})

	for i := 0; i < 5; i++ {
		s.ScanOnce()
	}
	assert.Empty(t, *restarts, "崩溃后的启动纪元应重新获得宽限，不得误判假死")
}

// --- FR-459 复审 minor 7：假死自愈重启风暴护栏 ---

func TestHealthScan_SelfHealRestartCap(t *testing.T) {
	alive, responsive := true, false
	s, m, audits, restarts := newHealthTestScanner(t, HealthActionRestart, 1, &alive, &responsive)
	pol := DefaultHealthPolicy()
	pol.Action = HealthActionRestart
	pol.SuspicionThreshold = 1
	pol.SelfHealMaxRestarts = 2
	s.SetPolicy(pol)
	addInstance(m, "u-storm", StateRunning, true, 0, 25566)

	// 阈值 1（每拍即判假死），上限 2：只允许 2 次自愈重启，之后降级为仅告警。
	for i := 0; i < 6; i++ {
		s.ScanOnce()
	}
	assert.Len(t, *restarts, 2, "假死自愈重启受上限约束（避免重启风暴）")
	assert.True(t, hasAudit(*audits, "health.selfheal_exhausted"), "达上限应落审计")
	_, reason := m.healthFault("u-storm")
	assert.Contains(t, reason, "上限")
}

// 响应恢复后重置自愈重启配额，使下一次故障重新获得完整配额。
func TestHealthScan_SelfHealQuotaResetsOnRecovery(t *testing.T) {
	alive, responsive := true, false
	s, m, _, restarts := newHealthTestScanner(t, HealthActionRestart, 1, &alive, &responsive)
	pol := DefaultHealthPolicy()
	pol.Action = HealthActionRestart
	pol.SuspicionThreshold = 1
	pol.SelfHealMaxRestarts = 1
	s.SetPolicy(pol)
	addInstance(m, "u-quota", StateRunning, true, 0, 25566)

	s.ScanOnce() // 第 1 次自愈
	require.Len(t, *restarts, 1)
	s.ScanOnce() // 达上限
	require.Len(t, *restarts, 1)

	responsive = true
	s.ScanOnce() // 恢复 → 配额清零
	responsive = false
	s.ScanOnce() // 重新计数 → 可再次自愈
	s.ScanOnce()
	assert.Len(t, *restarts, 2, "恢复后应重新获得自愈配额")
}

// --- FR-459 复审 minor 10：实例移除回收健康记账 ---

func TestManager_RemoveDropsHealthAccounting(t *testing.T) {
	m := NewManager(t.TempDir())
	addInstance(m, "u-gone", StateCrashed, true, 0, 25566)
	for i := 0; i < 5; i++ {
		m.emitCrash("u-gone", CrashInfo{ExitCode: 1})
	}
	alive, responsive := true, true
	s, _, _, _ := newHealthTestScannerOn(t, m, HealthActionWarn, 3, &alive, &responsive)
	s.ScanOnce()
	require.False(t, m.autoRestartAllowed("u-gone"))

	require.NoError(t, m.Remove("u-gone"))

	if _, broken := m.isCircuitBroken("u-gone"); broken {
		t.Fatal("实例移除后熔断记账应被回收")
	}
	if n := m.crashRestartCount("u-gone", time.Now(), time.Hour); n != 0 {
		t.Fatalf("实例移除后崩溃窗口应被回收，实际 %d", n)
	}
	fault, _ := m.healthFault("u-gone")
	assert.Equal(t, HealthFaultNone, fault)
}

// --- FR-459 终验 Major #2 / Low #3/#4/#6 回归 ---

// FR-459 终验 Major：人工解除熔断必须向仍在托管的 daemon wrapper 下发对称的「恢复自动重启」帧，
// 否则 Java 下次崩溃仍被 wrapper 的粘性开关拒绝重启，而记账/CP 却以为已解除。
func TestHealthScan_ReleaseCircuitBreakerRearmsDaemonWrapper(t *testing.T) {
	alive, responsive := true, true
	s, m, _, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-rearm", StateRunning, true, 0, 25566)

	fake := &fakeStrategy{state: StateRunning}
	m.mu.Lock()
	m.instances["u-rearm"].strategy = fake
	m.instances["u-rearm"].processType = ProcessTypeDaemon
	m.mu.Unlock()

	for i := 0; i < 5; i++ {
		m.emitCrash("u-rearm", CrashInfo{ExitCode: 1, OccurredAt: time.Now()})
	}
	s.ScanOnce()
	require.True(t, func() bool { _, b := m.isCircuitBroken("u-rearm"); return b }())
	assert.Equal(t, 1, fake.disarmCount, "熔断应向 wrapper 下发禁用帧")
	assert.Equal(t, 0, fake.rearmCount)

	_, released := m.ReleaseCircuitBreaker("u-rearm")
	require.True(t, released)
	assert.Equal(t, 1, fake.rearmCount, "人工解除熔断必须向 daemon wrapper 复位自动重启（对称 enable 帧）")
	assert.Equal(t, 0, len(mustListBroken(m)), "解除后熔断记账应清空")
}

// FR-459 终验 Low #4：熔断持续期间每拍幂等补发禁用帧，兜底首帧丢失导致的哑火。
func TestHealthScan_CircuitBrokenRedisarmsEachScan(t *testing.T) {
	alive, responsive := true, true
	s, m, _, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-re", StateRunning, true, 0, 25566)

	fake := &fakeStrategy{state: StateRunning}
	m.mu.Lock()
	m.instances["u-re"].strategy = fake
	m.instances["u-re"].processType = ProcessTypeDaemon
	m.mu.Unlock()

	for i := 0; i < 5; i++ {
		m.emitCrash("u-re", CrashInfo{ExitCode: 1, OccurredAt: time.Now()})
	}
	s.ScanOnce()
	require.Equal(t, 1, fake.disarmCount, "首拍熔断下发一次")

	// 熔断持续：后两拍应各补发一次（幂等）。
	s.ScanOnce()
	s.ScanOnce()
	assert.Equal(t, 3, fake.disarmCount, "熔断持续期间每拍应幂等补发禁用帧")
}

// FR-459 终验 Low #6：巡检开关由开→关时清一次健康记账，避免 CP 残留陈旧「假死」原因。
func TestHealthScanner_ToggleOffClearsHealthRecords(t *testing.T) {
	alive, responsive := true, false
	s, m, _, _ := newHealthTestScanner(t, HealthActionWarn, 2, &alive, &responsive)
	addInstance(m, "u-dead", StateRunning, true, 0, 25566)

	for i := 0; i < 3; i++ {
		s.ScanOnce()
	}
	fault, reason := m.healthFault("u-dead")
	require.Equal(t, HealthFaultDead, fault)
	require.Contains(t, reason, "假死")

	// CP 关闭巡检：由开→关，应清空健康记账（沿用当前生效策略，仅翻转 Enabled，避免覆盖阈值）。
	pol := s.effectivePolicy()
	pol.Enabled = false
	s.SetPolicy(pol)

	fault, reason = m.healthFault("u-dead")
	assert.Equal(t, HealthFaultNone, fault, "关闭巡检应清空健康记账")
	assert.Empty(t, reason, "关闭巡检应清空陈旧原因（避免 CP 残留假死）")

	// 再开→关不重复清理亦无副作用；开回后重新巡检可再次记账（嫌疑计数已清零，需重新累积到阈值）。
	pol.Enabled = true
	s.SetPolicy(pol)
	s.ScanOnce()
	s.ScanOnce()
	fault, _ = m.healthFault("u-dead")
	assert.Equal(t, HealthFaultDead, fault, "重新开启后应恢复巡检记账")
}

// FR-459 终验 Low #3：熔断态持久化——break 落盘，人工解除清盘，Worker 重启后可恢复。
func TestManager_CircuitBreakerPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	require.False(t, m.restorePersistedCircuit("u-persist"), "无状态文件时不应恢复")
	require.True(t, m.breakCircuit("u-persist", "持续崩溃已熔断：测试原因", false), "首次应触发熔断")
	require.FileExists(t, m.circuitStatePath("u-persist"), "熔断态应落盘")

	// 模拟 Worker 重启：全新 Manager 共享同一 pidDir。
	m2 := NewManager(dir)
	require.True(t, m2.restorePersistedCircuit("u-persist"), "应从状态文件恢复熔断态")
	reason, broken := m2.isCircuitBroken("u-persist")
	require.True(t, broken)
	assert.Equal(t, "持续崩溃已熔断：测试原因", reason)

	// 人工解除：清内存 + 清盘。
	_, released := m2.ReleaseCircuitBreaker("u-persist")
	require.True(t, released)
	assert.NoFileExists(t, m2.circuitStatePath("u-persist"), "解除熔断应清除状态文件")
	m3 := NewManager(dir)
	assert.False(t, m3.restorePersistedCircuit("u-persist"), "解除后重启不得误恢复熔断")
}

func mustListBroken(m *Manager) map[string]string {
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]string, len(h.broken))
	for k, v := range h.broken {
		out[k] = v
	}
	return out
}

// --- FR-459 终验（L2 竞态）：scan 补发 disable 与人工解除 enable 的交错 ---

// racingStrategy 是线程安全的 IProcessCommand 替身，模拟 daemon wrapper 的**粘性** autoRestartOff：
//   - DisableAutoRestart → off=true（记录 "disable"）；
//   - EnableAutoRestart  → off=false（记录 "enable"）。
//
// 支持 setArmed(hook)：武装后下一次 DisableAutoRestart 在**落地前**调用 hook——测试据此把 disable
// 卡在「下发在途」的中间态，从而确定性地构造「enable 先到、disable 后到」的交错。
type racingStrategy struct {
	mu        sync.Mutex
	off       bool
	log       []string
	armed     bool
	onDisable func()
}

func (f *racingStrategy) Start(context.Context) error { return nil }
func (f *racingStrategy) Stop() error                 { return nil }
func (f *racingStrategy) Kill() error                 { return nil }
func (f *racingStrategy) SendCommand(string) error    { return nil }
func (f *racingStrategy) State() InstanceState        { return StateRunning }
func (f *racingStrategy) Close() error                { return nil }
func (f *racingStrategy) GetPID() int                 { return 0 }

func (f *racingStrategy) setArmed(hook func()) {
	f.mu.Lock()
	f.armed = true
	f.onDisable = hook
	f.mu.Unlock()
}

func (f *racingStrategy) DisableAutoRestart() error {
	f.mu.Lock()
	armed, hook := f.armed, f.onDisable
	f.mu.Unlock()
	if armed && hook != nil {
		// 模拟禁用帧「下发在途」：先让并发解除的 enable 落地，再让本条 disable 落地。
		hook()
	}
	f.mu.Lock()
	f.off = true
	f.log = append(f.log, "disable")
	f.mu.Unlock()
	return nil
}

func (f *racingStrategy) EnableAutoRestart() error {
	f.mu.Lock()
	f.off = false
	f.log = append(f.log, "enable")
	f.mu.Unlock()
	return nil
}

func (f *racingStrategy) isOff() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.off
}

func (f *racingStrategy) lastOp() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.log) == 0 {
		return ""
	}
	return f.log[len(f.log)-1]
}

// TestHealthScan_RedisarmVsRelease_RaceConvergesToEnabled 是本竞态的核心交错用例（L2）：
//
// 构造「scan 正把补发 disable 下发在途（已读到 broken==true）时，人工解除并发清 broken 并下发 enable，
// 随后该 disable 才落到 wrapper」——修复前终态是 wrapper 被摁回禁用态而 Worker broken==false，且因
// 后续 ReleaseCircuitBreaker 走 !had 提前返回而无自愈路径。修复后要求：
//   - 终态绝不出现「broken==false ∧ wrapper autoRestartOff==true」；
//   - 最后一次下发必须是 enable（由下发后复核自愈纠正晚到的 disable）。
func TestHealthScan_RedisarmVsRelease_RaceConvergesToEnabled(t *testing.T) {
	alive, responsive := true, true
	s, m, _, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-race", StateRunning, true, 0, 25566)

	fake := &racingStrategy{}
	m.mu.Lock()
	m.instances["u-race"].strategy = fake
	m.instances["u-race"].processType = ProcessTypeDaemon
	m.mu.Unlock()

	// 1) 先触发熔断：首拍 evaluateCircuitBreaker → breakCircuit → disarmAutoRestart（此时未武装 hook，
	//    disable 正常落地；下发后复核 broken 仍为 true，不补发 enable）。
	for i := 0; i < 5; i++ {
		m.emitCrash("u-race", CrashInfo{ExitCode: 1, OccurredAt: time.Now()})
	}
	s.ScanOnce()
	require.True(t, func() bool { _, b := m.isCircuitBroken("u-race"); return b }(), "应先熔断")
	require.True(t, fake.isOff(), "熔断应已向 wrapper 下发 disable")

	// 2) 武装 hook：让下一拍「熔断持续期补发 disable」在下发前阻塞。
	disableStarted := make(chan struct{})
	proceed := make(chan struct{})
	fake.setArmed(func() {
		close(disableStarted)
		<-proceed
	})

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		s.ScanOnce() // 熔断持续 → redisarmAutoRestart → disable 阻塞在 hook（下发在途）
	}()

	<-disableStarted // scan 已读到 broken==true 并进入 disable 下发

	// 3) 并发人工解除：清 broken + 下发 enable（此刻 scan 的 disable 尚在途）。
	_, released := m.ReleaseCircuitBreaker("u-race")
	require.True(t, released, "解除应生效")
	close(proceed) // 放行在途 disable：它将在 enable 之后落地

	<-scanDone // 等 scan 走完「下发后复核 → 自愈 enable」

	assert.False(t, fake.isOff(),
		"终态不得为「Worker broken==false 且 wrapper autoRestartOff==true」")
	assert.Equal(t, "enable", fake.lastOp(), "最后一次下发必须是 enable（自愈纠正晚到的 disable）")
	assert.False(t, func() bool { _, b := m.isCircuitBroken("u-race"); return b }())
}

// TestHealthScan_ReleaseWhenNotBroken_StillRearmsRunningWrapper 覆盖修复的另一半：
// 去掉「ReleaseCircuitBreaker 在 !had 时提前返回、永不下发 enable」的死角——对 RUNNING 实例，
// 即便本次调用未观测到熔断记账，也应幂等补一次 enable 复位 wrapper 的粘性禁用。
func TestHealthScan_ReleaseWhenNotBroken_StillRearmsRunningWrapper(t *testing.T) {
	alive, responsive := true, true
	_, m, _, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-redundant", StateRunning, true, 0, 25566)

	fake := &racingStrategy{}
	m.mu.Lock()
	m.instances["u-redundant"].strategy = fake
	m.instances["u-redundant"].processType = ProcessTypeDaemon
	m.mu.Unlock()

	// 模拟历史交错留下的残留：wrapper 停在禁用态，而 Worker 记账 broken 已空。
	fake.mu.Lock()
	fake.off = true
	fake.mu.Unlock()

	reason, released := m.ReleaseCircuitBreaker("u-redundant")
	assert.False(t, released, "未熔断时 released=false（幂等语义不变）")
	assert.Empty(t, reason)
	assert.False(t, fake.isOff(),
		"未熔断时的人工 Start 亦须幂等复位 RUNNING wrapper 的粘性禁用（去掉 !had 死角）")
	assert.Equal(t, "enable", fake.lastOp())
}

// TestHealthScan_ReleaseWhenNotBroken_StoppedWrapperUntouched 保证上一条不引入新下发：
// 非 RUNNING 实例（下次启动会 spawn 全新 wrapper，autoRestartOff 默认 false）不应被打扰，
// 避免每次人工 Start 都对已停实例发无效控制帧 / 刷告警日志。
func TestHealthScan_ReleaseWhenNotBroken_StoppedWrapperUntouched(t *testing.T) {
	alive, responsive := true, true
	_, m, _, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	addInstance(m, "u-stopped", StateStopped, false, 0, 0)

	fake := &racingStrategy{}
	m.mu.Lock()
	m.instances["u-stopped"].strategy = fake
	m.instances["u-stopped"].processType = ProcessTypeDaemon
	m.mu.Unlock()

	_, released := m.ReleaseCircuitBreaker("u-stopped")
	assert.False(t, released)
	assert.Empty(t, fake.lastOp(), "已停实例无托管 wrapper，不应因人工 Start 而下发任何控制帧")
}

// TestHealthScan_ScanReleaseConcurrent_NoBadTerminalState 以受限并发循环压测两把锁的交错，
// 供 -race 暴露数据竞争，并断言「broken 与 wrapper 禁用态」在反复 scan/release 后一致收敛。
//
// FR-459 终验 L2 回归修复：原实现每轮只灌崩溃计数后就并发 scan/release，但 ReleaseCircuitBreaker
// 会清空崩溃窗口（health_scan.go delete(h.windows)），并发下 scan 常读不到 crashRestartCount>=阈值
// → 熔断从未触发、审计恒 0（用例空转，末尾非空转断言恒失败）。现改为每轮：
//  1. **先确定性熔断**（emitCrash×阈值 + ScanOnce），保证熔断/审计路径确实被执行；
//  2. 再用 racingStrategy.setArmed 把「熔断持续期补发的 disable」卡在下发在途，与 release 的 enable
//     制造「enable 先到、disable 后到」交错——对齐 sendDisarmReconciled 的下发后复核自愈路径；
//  3. 逐轮断言不变量「broken==false ⇒ wrapper 启用」。
func TestHealthScan_ScanReleaseConcurrent_NoBadTerminalState(t *testing.T) {
	alive, responsive := true, true
	s, m, _, _ := newHealthTestScanner(t, HealthActionWarn, 3, &alive, &responsive)
	// 并发路径会从 scan 与 release 两条 goroutine 触发审计回调，改用加锁 sink 避免测试自身数据竞争。
	var auditMu sync.Mutex
	var auditCount int
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		auditMu.Lock()
		auditCount++
		auditMu.Unlock()
	}
	addInstance(m, "u-conc", StateRunning, true, 0, 25566)

	fake := &racingStrategy{}
	m.mu.Lock()
	m.instances["u-conc"].strategy = fake
	m.instances["u-conc"].processType = ProcessTypeDaemon
	m.mu.Unlock()

	auditTotal := func() int {
		auditMu.Lock()
		defer auditMu.Unlock()
		return auditCount
	}
	// 每轮重新灌入的崩溃配额：达默认熔断阈值即确定性熔断。
	const crashBurst = DefaultCircuitBreakerThreshold

	// 1) 前置：确定性触发一次熔断，证明该用例确实会走到审计下发路径（消除「空转」）。
	for j := 0; j < crashBurst; j++ {
		m.emitCrash("u-conc", CrashInfo{ExitCode: 1, OccurredAt: time.Now()})
	}
	s.ScanOnce()
	require.True(t, func() bool { _, b := m.isCircuitBroken("u-conc"); return b }(),
		"前置：崩溃达阈值应先确定性熔断")
	assert.Positive(t, auditTotal(), "确定性熔断应落审计（用例非空转）")
	// 复位到干净态（release 同时清窗口与熔断账），再进入并发交错压测。
	m.ReleaseCircuitBreaker("u-conc")
	auditBefore := auditTotal()

	// 2) 并发交错压测：每轮确定性熔断后，制造「disable 在途 vs release enable」交错。
	const rounds = 40
	for i := 0; i < rounds; i++ {
		for j := 0; j < crashBurst; j++ {
			m.emitCrash("u-conc", CrashInfo{ExitCode: 1, OccurredAt: time.Now()})
		}
		s.ScanOnce()
		require.True(t, func() bool { _, b := m.isCircuitBroken("u-conc"); return b }(),
			"第 %d 轮：崩溃达阈值应先确定性熔断", i)

		// 武装 hook：让本拍「熔断持续期补发 disable」在下发前阻塞（scan 已读到 broken==true 并进入下发）。
		disableStarted := make(chan struct{})
		proceed := make(chan struct{})
		fake.setArmed(func() {
			close(disableStarted)
			<-proceed
		})
		scanDone := make(chan struct{})
		go func() {
			defer close(scanDone)
			s.ScanOnce() // 熔断持续 → redisarmAutoRestart → disable 卡在 hook（下发在途）
		}()
		<-disableStarted // scan 已读到 broken==true 并进入 disable 下发

		// 并发人工解除：清 broken + 下发 enable（此刻 scan 的 disable 尚在途，enable 先落地）。
		_, released := m.ReleaseCircuitBreaker("u-conc")
		require.True(t, released, "第 %d 轮：解除应生效", i)
		fake.setArmed(nil) // 解除武装，避免影响下一轮熔断首帧下发
		close(proceed)     // 放行在途 disable：它将在 enable 之后落地
		<-scanDone         // 等 scan 走完「下发后复核 → 自愈 enable」

		// 逐轮不变量：release 完成后 broken==false，wrapper 必须处于启用态。
		_, broken := m.isCircuitBroken("u-conc")
		assert.False(t, broken, "第 %d 轮：release 后不得残留熔断记账", i)
		assert.False(t, fake.isOff(),
			"第 %d 轮：并发 scan/release 后不得残留「broken==false ∧ wrapper 禁用」的终态", i)
	}

	// 3) 收敛断言 + 非空转：最后一轮显式解除后，broken==false 时 wrapper 必须启用，
	//    且并发轮次确实持续产生了审计下发（证明用例真在跑而非空转）。
	m.ReleaseCircuitBreaker("u-conc")
	assert.False(t, func() bool { _, b := m.isCircuitBroken("u-conc"); return b }())
	assert.False(t, fake.isOff(),
		"并发 scan/release 后不得残留「broken==false ∧ wrapper 禁用」的终态")
	assert.Greater(t, auditTotal(), auditBefore, "并发交错轮次应持续触发审计下发（用例非空转）")
}
