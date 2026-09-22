package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/metrics"
)

// FR-459 实例健康巡检与自愈（Worker 侧）。
//
// 动机：既有韧性只在**进程退出之后**被动重启（direct/docker waitLoop、wrapper javaWait），
// 对「进程仍在、却已不可服务」（GC 长停 / 死锁 / World 加载卡死 / 探针降级）的假死完全无感——
// waitLoop 永远等不到退出，实例永远「RUNNING」却不可用，面板不告警、不重启。
//
// 本文件新增：
//   - HealthScanner：运行期周期巡检，对每个在册实例做「存活 ∧ 响应」双维判定；
//   - 假死自愈：连续 SuspicionThreshold 次判假死才动作（warn 仅告警 / restart 走既有优雅重启）；
//   - 崩溃熔断：滚动窗口内重启次数超阈值 → 停自动重启 + 置 CRASHED + 标原因 + 告警，等待人工确认。
//
// 边界（spec §1/§5）：
//   - **不新增杀进程路径**：假死自愈只走既有优雅重启（Manager.Restart 的受锁变体）；
//   - 存活维度**零新代码**，直接复用 ProbeInstanceEvidence（与 FR-455/456 同源）；
//   - 不做跨节点迁移；「进程不在」交由既有崩溃处理（本巡检不重复处置）。

// 健康故障标识（经心跳上报 CP，写入 instances.status_reason / 健康位）。
const (
	// HealthFaultNone 健康（或状态瞬态，无需标注）。
	HealthFaultNone = ""
	// HealthFaultSuspected 假死嫌疑累积中（存活但响应探测失败，尚未达阈值）。
	HealthFaultSuspected = "suspected_dead"
	// HealthFaultDead 已确认假死（连续 N 次存活但不响应）。
	HealthFaultDead = "dead"
	// HealthFaultCrash 进程/容器不存活（崩溃，交由既有处理）。
	HealthFaultCrash = "crashed"
	// HealthFaultCircuitBroken 已熔断（窗口内重启次数超限，停止自动重启，等待人工确认）。
	HealthFaultCircuitBroken = "circuit_broken"
	// HealthFaultHealthy 明确健康（存活 ∧ 响应，或无可探维度仅存活）：供 CP 清空历史 status_reason。
	HealthFaultHealthy = "healthy"
)

// 响应维度探针类型（可配，spec §2.3 probeKind）。
const (
	// HealthProbeTCP 以 TCP connect 服务端口判响应（未部署探针的兜底维度）。
	HealthProbeTCP = "tcp"
	// HealthProbeHTTP 以 HTTP GET ProbePort 判响应（ServerProbe /metrics）。
	HealthProbeHTTP = "http"
)

// 假死自愈动作（spec §2.2）。
const (
	// HealthActionWarn 仅告警 + 落审计 + 标原因（默认）。
	HealthActionWarn = "warn"
	// HealthActionRestart 走既有优雅重启路径自愈。
	HealthActionRestart = "restart"
	// HealthActionCircuitBreak 熔断（非可配动作，作为审计/上报动作标识）。
	HealthActionCircuitBreak = "circuit_break"
)

// 巡检默认值。
//
// 导出的 Default* 是**唯一真源**：worker 配置层（internal/worker/config.go）经 process 包引用，
// 避免「本地默认」与「进程归一默认」两处字面量漂移（FR-459 复审项 12）。
const (
	// DefaultHealthScanInterval 默认巡检周期（spec §2.1）。
	DefaultHealthScanInterval = 30 * time.Second
	// DefaultSuspicionThreshold 默认连续假死判定阈值（spec §2.2）。
	DefaultSuspicionThreshold = 3
	// DefaultCircuitBreakerThreshold 默认崩溃熔断阈值（窗口内重启次数，spec §2.2）。
	DefaultCircuitBreakerThreshold = 5
	// DefaultCircuitBreakerWindow 默认崩溃熔断滚动窗口（spec §2.2）。
	DefaultCircuitBreakerWindow = 10 * time.Minute
	// DefaultStartupWarmup 默认启动宽限期：Start 返回 RUNNING 后的这段时间内不做假死判定，
	// 避免慢启动 MC（World 加载/模组初始化常达 90s+）被连续探针失败误判并反复重启（FR-459 复审项 2）。
	DefaultStartupWarmup = 5 * time.Minute
	// DefaultSelfHealMaxRestarts 假死自愈在一熔断窗口内的重启次数上限（避免假死路径重启风暴，FR-459 复审项 7）。
	DefaultSelfHealMaxRestarts = 3
	// healthScanProbeHost 巡检探针主机：实例与 Worker 同机，直探 loopback。
	healthScanProbeHost = "localhost"
)

// HealthPolicy 是 FR-459 巡检策略的 Worker 侧生效值。
// 本地基线来自 worker.yml（HealthScanConfig）；CP 经心跳响应下发后覆盖
// （enabled 以外的字段在 CP 下发存在时以 CP 为准）。
type HealthPolicy struct {
	// Enabled 是否执行巡检动作（本地开关 ∧ CP 开关，见 HealthScanner.effectivePolicy）。
	Enabled bool
	// ScanInterval 巡检周期（<=0 用默认 30s）。
	ScanInterval time.Duration
	// ProbeKind 响应维度探针类型：tcp / http（空=auto：有 server-port 用 tcp，否则有 probe-port 用 http）。
	ProbeKind string
	// SuspicionThreshold 连续判假死次数阈值（<=0 用默认 3）。
	SuspicionThreshold int
	// Action 假死动作：warn / restart（空/未知回退 warn）。
	Action string
	// CircuitBreakerThreshold 滚动窗口内重启次数阈值（<=0 用默认 5）。
	CircuitBreakerThreshold int
	// CircuitBreakerWindow 滚动窗口（<=0 用默认 10m）。
	CircuitBreakerWindow time.Duration
	// StartupWarmup 启动宽限期（<=0 用默认 5m；<0 表示显式关闭宽限——测试/特殊场景）。
	StartupWarmup time.Duration
	// SelfHealMaxRestarts 一熔断窗口内假死自愈重启次数上限（<=0 用默认 3）。
	SelfHealMaxRestarts int
}

// DefaultHealthPolicy 返回 FR-459 的内置默认策略。
func DefaultHealthPolicy() HealthPolicy {
	return HealthPolicy{
		Enabled:                 true,
		ScanInterval:            DefaultHealthScanInterval,
		ProbeKind:               "",
		SuspicionThreshold:      DefaultSuspicionThreshold,
		Action:                  HealthActionWarn,
		CircuitBreakerThreshold: DefaultCircuitBreakerThreshold,
		CircuitBreakerWindow:    DefaultCircuitBreakerWindow,
		StartupWarmup:           DefaultStartupWarmup,
		SelfHealMaxRestarts:     DefaultSelfHealMaxRestarts,
	}
}

// normalizeHealthPolicy 归一化策略：非法/未配置项回退默认（安全默认——action 回 warn，
// 阈值回正默认），保证消费方始终拿到可用值。
//
// StartupWarmup 特殊：负值表示「显式关闭宽限」而非未配置（未配置=0 → 回默认）。
func normalizeHealthPolicy(p HealthPolicy) HealthPolicy {
	def := DefaultHealthPolicy()
	if p.ScanInterval <= 0 {
		p.ScanInterval = def.ScanInterval
	}
	switch strings.TrimSpace(strings.ToLower(p.ProbeKind)) {
	case HealthProbeTCP, HealthProbeHTTP:
		p.ProbeKind = strings.TrimSpace(strings.ToLower(p.ProbeKind))
	default:
		p.ProbeKind = ""
	}
	if p.SuspicionThreshold <= 0 {
		p.SuspicionThreshold = def.SuspicionThreshold
	}
	if strings.TrimSpace(strings.ToLower(p.Action)) == HealthActionRestart {
		p.Action = HealthActionRestart
	} else {
		p.Action = HealthActionWarn
	}
	if p.CircuitBreakerThreshold <= 0 {
		p.CircuitBreakerThreshold = def.CircuitBreakerThreshold
	}
	if p.CircuitBreakerWindow <= 0 {
		p.CircuitBreakerWindow = def.CircuitBreakerWindow
	}
	if p.StartupWarmup == 0 {
		p.StartupWarmup = def.StartupWarmup
	}
	if p.SelfHealMaxRestarts <= 0 {
		p.SelfHealMaxRestarts = def.SelfHealMaxRestarts
	}
	return p
}

// HealthReport 单个实例的一轮巡检结论。
type HealthReport struct {
	UUID string
	// Liveness 进程/容器存活证据。
	Liveness bool
	// Readiness 响应探测是否可达（无可用探针端口时为 true——仅存活即健康）。
	Readiness bool
	// Healthy 存活 ∧ 响应。
	Healthy bool
	// Warmup 本轮处于启动宽限期（慢启动阶段不做假死判定，故 Readiness 不参与结论）。
	Warmup bool
	// Fault 当前健康故障标识（HealthFault*；空=健康）。
	Fault string
	// Reason 供 statusReason 的说明（空=无故障）。
	Reason string
	// Actions 本轮对实例执行的动作（warn / restart / circuit_break）。
	Actions []string
}

// healthRecord 单实例的健康记账（scanner 写入、心跳经 InstanceSnapshot 读取）。
type healthRecord struct {
	fault     string
	reason    string
	updatedAt time.Time
}

// restartWindow 单实例的滚动时间窗（熔断崩溃计数 / 假死自愈计数，FR-459 T4 + 复审项 7）。
// 语义对齐 wrapper.javaWait 的 fastCrashes：窗口内次数超阈值即停止自动重启（熔断）或停止自愈重启。
type restartWindow struct {
	times []time.Time
}

// healthState FR-459 的进程管理器侧健康记账：逐实例健康故障 + 滚动窗口 + 熔断态 + 自愈计数。
//
// 独立于 Manager.mu 自有锁：巡检（读进程态、跑探针）耗时，不得把整个 Manager 锁住。
type healthState struct {
	mu      sync.Mutex
	records map[string]*healthRecord
	windows map[string]*restartWindow // 崩溃事件滚动窗口（熔断计数）
	// broken 是熔断态**独立真源**（非空=已熔断）。与 Instance.AutoRestart 解耦：
	// 后者是 CP 下发的「是否允许自动重启」配置，不得兼作「人工是否已确认解除熔断」的语义
	// （否则任意配置编辑都会静默清熔断，FR-459 复审项 6）。
	broken map[string]string
	// selfHeal 假死自愈重启的滚动窗口（次数上限/退避，避免重启风暴，FR-459 复审项 7）。
	selfHeal map[string]*restartWindow
	// now 可注入时钟（测试用），nil=time.Now。
	now func() time.Time
}

func newHealthState() *healthState {
	return &healthState{
		records:  map[string]*healthRecord{},
		windows:  map[string]*restartWindow{},
		broken:   map[string]string{},
		selfHeal: map[string]*restartWindow{},
	}
}

// dropHealthAccounting 回收某实例的全部健康记账（健康故障 / 崩溃窗口 / 熔断态 / 自愈计数）。
// 实例被移除（removeLocked）或重新登记（Create）时调用：此前只增不减，删除实例后残留记账
// 会污染同 UUID 重建实例的熔断判定（FR-459 复审项 10）。同时清理熔断态持久化文件（Low #3），
// 避免重建实例误恢复旧熔断态。
func (m *Manager) dropHealthAccounting(uuid string) {
	if uuid == "" {
		return
	}
	h := m.healthState()
	h.mu.Lock()
	delete(h.records, uuid)
	delete(h.windows, uuid)
	delete(h.broken, uuid)
	delete(h.selfHeal, uuid)
	h.mu.Unlock()
	m.clearPersistedCircuit(uuid)
}

// maxRestartWindowSamples 是滚动窗口样本的保留上限（远超任何阈值口径，仅防无界增长）。
const maxRestartWindowSamples = 64

// clock 返回当前时刻（可注入时钟优先，nil=time.Now）。
func (h *healthState) clock() time.Time {
	if h != nil && h.now != nil {
		return h.now()
	}
	return time.Now()
}

// setHealthFault 写入/清除某实例的健康故障（fault 为空即清除）。reason 空时一并清除原因。
func (m *Manager) setHealthFault(uuid, fault, reason string) {
	if uuid == "" {
		return
	}
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	if fault == HealthFaultNone {
		delete(h.records, uuid)
		return
	}
	h.records[uuid] = &healthRecord{fault: fault, reason: reason, updatedAt: h.clock()}
}

// healthFault 读取某实例的健康故障（fault, reason）；无记录返回空。
func (m *Manager) healthFault(uuid string) (string, string) {
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	rec, ok := h.records[uuid]
	if !ok {
		return HealthFaultNone, ""
	}
	return rec.fault, rec.reason
}

// clearHealthFault 清除某实例健康故障记录。
func (m *Manager) clearHealthFault(uuid string) { m.setHealthFault(uuid, HealthFaultNone, "") }

// clearAllHealthFaults 清空全部实例的健康故障记录（records）。用于巡检开关关闭时的一次性清理
// （FR-459 终验 Low #6）；不动 windows/broken/selfHeal——熔断态与崩溃窗口的处置只经既有入口。
func (m *Manager) clearAllHealthFaults() {
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = map[string]*healthRecord{}
}

// noteProcessCrash 记录一次进程崩溃（供熔断窗口计数）。由 emitCrash 调用，覆盖
// direct/docker waitLoop 与 daemon wrapper 退出事件三条崩溃来源。
func (m *Manager) noteProcessCrash(uuid string) {
	if uuid == "" {
		return
	}
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	win := h.windows[uuid]
	if win == nil {
		win = &restartWindow{}
		h.windows[uuid] = win
	}
	win.times = append(win.times, h.clock())
	// 上限保护：只保留最近 maxRestartWindowSamples 条，避免无界增长（远超任何阈值口径）。
	if len(win.times) > maxRestartWindowSamples {
		win.times = win.times[len(win.times)-maxRestartWindowSamples:]
	}
}

// crashRestartCount 返回窗口内（now-window, now] 的崩溃重启次数（熔断计数口径）。
func (m *Manager) crashRestartCount(uuid string, now time.Time, window time.Duration) int {
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	return pruneWindow(h.windows[uuid], now, window)
}

// lastProcessCrash 返回最近一次崩溃时刻；无记录返回零值。
func (m *Manager) lastProcessCrash(uuid string) time.Time {
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	win := h.windows[uuid]
	if win == nil || len(win.times) == 0 {
		return time.Time{}
	}
	return win.times[len(win.times)-1]
}

// clearRestartWindow 清空某实例的崩溃滚动窗口（如正常停止后重新起算）。
func (m *Manager) clearRestartWindow(uuid string) {
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.windows, uuid)
}

// noteSelfHealRestart 记录一次假死自愈重启（供自愈次数上限/退避计数）。
func (m *Manager) noteSelfHealRestart(uuid string) {
	if uuid == "" {
		return
	}
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	win := h.selfHeal[uuid]
	if win == nil {
		win = &restartWindow{}
		h.selfHeal[uuid] = win
	}
	win.times = append(win.times, h.clock())
	if len(win.times) > maxRestartWindowSamples {
		win.times = win.times[len(win.times)-maxRestartWindowSamples:]
	}
}

// selfHealRestartCount 返回窗口内（now-window, now] 的假死自愈重启次数。
func (m *Manager) selfHealRestartCount(uuid string, now time.Time, window time.Duration) int {
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	return pruneWindow(h.selfHeal[uuid], now, window)
}

// resetSelfHealRestarts 清零某实例的假死自愈计数（响应恢复后重新给予完整重启配额）。
func (m *Manager) resetSelfHealRestarts(uuid string) {
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.selfHeal, uuid)
}

// pruneWindow 丢弃滚动窗口内早于 now-window 的样本，返回窗口内（now-window, now] 剩余样本数。
// win 为空返回 0。就地裁剪，避免窗口无界增长。
func pruneWindow(win *restartWindow, now time.Time, window time.Duration) int {
	if win == nil || len(win.times) == 0 {
		return 0
	}
	cutoff := now.Add(-window)
	kept := win.times[:0]
	for _, ts := range win.times {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	win.times = kept
	return len(win.times)
}

// circuitStateSuffix 是熔断态持久化文件的后缀（位于 pidDir，与 daemon PID 文件同目录）。
const circuitStateSuffix = ".circuit"

// circuitStatePath 返回某实例熔断态状态文件路径；pidDir 未配置时返回空串（不持久化）。
func (m *Manager) circuitStatePath(uuid string) string {
	if m.pidDir == "" || uuid == "" {
		return ""
	}
	return filepath.Join(m.pidDir, uuid+circuitStateSuffix)
}

// persistCircuit 把熔断原因落盘（pidDir 旁小状态文件），供 Worker 重启后恢复熔断态（FR-459 终验 Low #3）。
//
// 动机：Worker 重启会清空内存中的熔断账，而 daemon wrapper 的 autoRestartOff 是**粘性存活于
// wrapper 进程内**的（Worker 重启 wrapper 不死）。不持久化会让 Worker「忘掉」熔断（以为可自动重启）
// 而 wrapper 仍在拒绝重启，形成反向 desync。失败只记日志（尽力而为，不影响本进程内熔断生效）。
func (m *Manager) persistCircuit(uuid, reason string) {
	path := m.circuitStatePath(uuid)
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte(reason), 0o644); err != nil {
		slog.Warn("持久化熔断态失败（Worker 重启后可能丢失熔断）", "instanceId", uuid, "error", err)
	}
}

// clearPersistedCircuit 删除熔断态状态文件（解除熔断 / 实例移除时）。
func (m *Manager) clearPersistedCircuit(uuid string) {
	path := m.circuitStatePath(uuid)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("清理熔断态文件失败", "instanceId", uuid, "error", err)
	}
}

// restorePersistedCircuit 读取并恢复某实例的熔断态（Worker 重启恢复 daemon 实例时）。返回是否恢复。
// 恢复后调用方应保持该实例 AutoRestart=false 并补发禁用帧（wrapper 侧可能已重置或从未收到）。
func (m *Manager) restorePersistedCircuit(uuid string) bool {
	path := m.circuitStatePath(uuid)
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	reason := strings.TrimSpace(string(data))
	if reason == "" {
		reason = "持续崩溃已熔断（Worker 重启前已熔断，恢复熔断态等待人工确认）"
	}
	h := m.healthState()
	h.mu.Lock()
	h.broken[uuid] = reason
	h.mu.Unlock()
	slog.Warn("已从状态文件恢复实例熔断态（等待人工确认）", "instanceId", uuid, "reason", reason)
	return true
}

// breakCircuit 触发熔断：停止自动重启 + 记录熔断原因；forceCrash 为 true 时把实例记账置 CRASHED。
// 返回是否本次真正触发（此前未熔断）。
//
// forceCrash 由调用方按「进程侧证据是否显示仍在运行」决定：daemon 下 wrapper 存活而 Java 已崩时
// Worker 记账本就是 RUNNING（本 FR 待修的错位），熔断置 CRASHED 与该错位一致；但若证据显示进程
// 确实在跑（wrapper 已把 Java 拉起并稳定），强行覆盖为 CRASHED 只会制造反向错位，此时只停自动重启、
// 保留真实状态，等其下次退出后再自然收敛。
func (m *Manager) breakCircuit(uuid, reason string, forceCrash bool) bool {
	if uuid == "" {
		return false
	}
	h := m.healthState()
	h.mu.Lock()
	if _, already := h.broken[uuid]; already {
		h.mu.Unlock()
		return false
	}
	h.broken[uuid] = reason
	h.mu.Unlock()
	// FR-459 终验 Low #3：熔断态落盘，供 Worker 重启后恢复（避免 Worker 忘掉熔断而 wrapper 仍在拒绝重启）。
	m.persistCircuit(uuid, reason)

	var oldState InstanceState
	changed := false
	m.mu.Lock()
	if inst, ok := m.instances[uuid]; ok {
		inst.AutoRestart = false
		if forceCrash && inst.State != StateCrashed {
			oldState = inst.State
			inst.State = StateCrashed
			changed = true
		}
	}
	m.mu.Unlock()
	if changed {
		m.emitStateChange(uuid, oldState, StateCrashed)
	}
	// daemon 生效路径：wrapper 内部按启动期 env 快照自动重启，Worker 无法直接触及——
	// 必须经控制帧显式禁用（否则熔断对 daemon 完全不成立，FR-459 blocker）。
	m.disarmAutoRestart(uuid, reason)
	return true
}

// autoRestartDisarmer 是支持「运行期禁用自动重启」的策略能力（当前仅 daemon wrapper 协议）。
// 以可选接口而非 IProcessCommand 方法表达：direct/docker 的自动重启由 Manager 侧
// autoRestartAllowed 守卫直接拦住，无需策略配合，也就不必为它们（及其测试替身）加空实现。
type autoRestartDisarmer interface {
	DisableAutoRestart() error
}

// disarmAutoRestart 向实现了禁用能力的策略下发「禁用自动重启」；失败只记日志——
// 熔断态已落记账，Manager 侧守卫（autoRestartAllowed）仍拦住 direct/docker 的自动重启。
func (m *Manager) disarmAutoRestart(uuid, reason string) {
	m.sendDisarmReconciled(uuid, reason, false)
}

// redisarmAutoRestart 在熔断**持续期间**的每一拍幂等补发一次禁用帧（FR-459 终验 Low #4）。
// 首帧可能因「熔断瞬间控制连接恰好断开且即时拨号失败」而丢失，仅一次性下发会哑火——wrapper
// 会继续自动重启注定崩溃的 Java。逐拍重发以幂等语义兜底；失败降级 Debug，避免 wrapper 已收摊
// （熔断时确无 Java 在跑的常见场景）时每拍刷告警。
func (m *Manager) redisarmAutoRestart(uuid, reason string) {
	m.sendDisarmReconciled(uuid, reason, true)
}

// sendDisarmReconciled 下发「禁用自动重启」帧，并在**下发之后复核熔断态**：若此刻熔断已被清除，
// 立即补发一次对称的「恢复自动重启」帧自愈（FR-459 终验 Low，L2 竞态）。
//
// 竞态：disable 与 enable 两次下发都在锁外，可能交错——巡检读到 broken==true 决定补发 disable，
// 恰在此时人工 StartInstance 的 ReleaseCircuitBreaker 已清 broken（锁内）并下发 enable，随后本次
// disable 才落到 wrapper，就把 wrapper 摁回 autoRestartOff==true，而 Worker 记账 broken==false；
// 此后 ReleaseCircuitBreaker 因 !had 提前返回不再补发 enable，wrapper 便无自愈路径地卡死在禁用态。
//
// 不变量：disable 下发后立刻在**同一 goroutine** 上复核 broken——只要它已被清除，就补一次 enable。
// 复核与 disable 同序、enable 恒晚于 disable，故「broken==false ⇒ wrapper 自动重启启用」单调收敛；
// 熔断持续期间 broken 仍为 true，不会产生额外下发（无下发风暴）。
//
// 与锁序：本方法不嵌套任何锁——sendAutoRestartControl / isCircuitBroken / rearmAutoRestart
// 各自独立取放 m.mu.RLock 与 h.mu，维持既有 m.mu→h.mu 单向顺序，IO 全部在锁外。
func (m *Manager) sendDisarmReconciled(uuid, reason string, quiet bool) {
	m.sendAutoRestartControl(uuid, reason, quiet)
	if _, stillBroken := m.isCircuitBroken(uuid); !stillBroken {
		// 熔断已在本次下发期间被解除：晚到的 disable 属过期指令，必须用一次 enable 纠正。
		m.rearmAutoRestart(uuid)
	}
}

// sendAutoRestartControl 是禁用自动重启下发的公共实现；quiet=true 时失败只记 Debug（逐拍补发路径）。
func (m *Manager) sendAutoRestartControl(uuid, reason string, quiet bool) {
	m.mu.RLock()
	var strategy IProcessCommand
	if inst, ok := m.instances[uuid]; ok {
		strategy = inst.strategy
	}
	m.mu.RUnlock()
	disarmer, ok := strategy.(autoRestartDisarmer)
	if !ok {
		return
	}
	if err := disarmer.DisableAutoRestart(); err != nil {
		if quiet {
			slog.Debug("熔断持续期间补发禁用自动重启失败（wrapper 可能已收摊/连接瞬断）",
				"instanceId", uuid, "reason", reason, "error", err)
			return
		}
		slog.Warn("熔断后下发禁用自动重启失败（wrapper 可能仍在自动重启）",
			"instanceId", uuid, "reason", reason, "error", err)
	}
}

// autoRestartRearmer 是 autoRestartDisarmer 的对称恢复能力（FR-459 终验 Major）：
// 人工解除熔断时，向仍在托管的 daemon wrapper 复位其粘性 autoRestartOff。
// 与禁用同用可选接口表达：direct/docker 的自动重启由 Manager 侧守卫直接放行，无需策略配合。
type autoRestartRearmer interface {
	EnableAutoRestart() error
}

// rearmAutoRestart 向实现了恢复能力的策略下发「恢复自动重启」。
//
// 必要性（终验 Major）：熔断在 Java 仍在运行时触发时只发禁用帧、保留 RUNNING、wrapper 继续托管；
// 人工解除若只清 Worker 内存账而不复位 wrapper，Java 下次崩溃仍被 wrapper 的粘性开关拒绝重启，
// 而 CP/Worker 却认为熔断已解除。失败只记日志（wrapper 已收摊退出时失败无害：该实例已停/重建，
// 下次启动会 spawn 全新 wrapper，autoRestartOff 默认 false）。
func (m *Manager) rearmAutoRestart(uuid string) {
	m.mu.RLock()
	var strategy IProcessCommand
	if inst, ok := m.instances[uuid]; ok {
		strategy = inst.strategy
	}
	m.mu.RUnlock()
	rearmer, ok := strategy.(autoRestartRearmer)
	if !ok {
		return
	}
	if err := rearmer.EnableAutoRestart(); err != nil {
		slog.Warn("熔断解除后下发恢复自动重启失败（wrapper 可能仍在拒绝重启）",
			"instanceId", uuid, "error", err)
	}
}

// isCircuitBroken 报告某实例当前是否熔断（返回熔断原因）。
//
// 判据是 healthState.broken 这一**独立真源**，不再借用 Instance.AutoRestart：
// 后者是 CP 下发的配置，任意配置编辑（SetLaunchConfig）都会重写它，若兼作「人工已确认解除」
// 的语义，会让熔断被静默清除（FR-459 复审项 6）。解除只经显式入口（ReleaseCircuitBreaker）。
func (m *Manager) isCircuitBroken(uuid string) (string, bool) {
	h := m.healthState()
	h.mu.Lock()
	defer h.mu.Unlock()
	reason, had := h.broken[uuid]
	return reason, had
}

// ReleaseCircuitBreaker 显式解除某实例的崩溃熔断（人工确认，spec §2.2/§5）：
// 清熔断锁 + 清崩溃窗口（给予一次干净的重启配额）+ 恢复自动重启 + 清健康故障标识。
// 返回解除前的原因；released=false 表示此前并未熔断（幂等）。
//
// 调用点：Worker 收到 CP 的人工启动指令（gRPC StartInstance）——「熔断期间人工启动不受阻」
// 且「移除熔断锁后恢复自动重启」（spec §4 验收 5）由同一次人工动作完成。
//
// FR-459 终验 Low（L2 竞态）：即便本次调用未观测到熔断记账（had==false，多为「熔断刚被并发的
// 巡检/另一次解除清掉」），也幂等补发一次 enable。否则那条「scan 已读到 broken 并补发 disable、
// 而解除方此处的 enable 先到、disable 后到」的交错留下的 wrapper 粘性禁用态将**再无恢复路径**
// （后续人工 Start 因 !had 提前返回，永不下发 enable）。真正的收敛由 sendDisarmReconciled 的
// 下发后复核保证；此处 enable 是第二道保险，且只在当前确未熔断时下发（避免与并发 breakCircuit
// 反向交错成「已熔断却被 enable」）。
func (m *Manager) ReleaseCircuitBreaker(uuid string) (reason string, released bool) {
	if uuid == "" {
		return "", false
	}
	h := m.healthState()
	h.mu.Lock()
	reason, had := h.broken[uuid]
	delete(h.broken, uuid)
	delete(h.windows, uuid)
	delete(h.selfHeal, uuid)
	h.mu.Unlock()
	if !had {
		// 幂等自愈（去掉「!had 提前返回就不发 enable」的恢复死角）：若本实例仍 RUNNING，
		// 其 wrapper 可能因「另一次并发解除时 enable 先到、scan 补发的 disable 后到」而停在
		// 粘性禁用态——此时记账 broken 已空（sendDisarmReconciled 的复核会尝试自愈，但若该
		// 复核仍被极窄的连接重排穿越、或 frame 在 wrapper 侧丢失，则仅靠人工 Start 才能复位）。
		// 故此处对 RUNNING 实例再幂等补一次 enable。对 direct/docker（未实现复位能力）是空操作；
		// 已停/已崩实例下次启动会 spawn 全新 wrapper（autoRestartOff 默认 false），无需亦不应打扰。
		if m.instanceState(uuid) == StateRunning {
			if _, broken := m.isCircuitBroken(uuid); !broken {
				m.rearmAutoRestart(uuid)
			}
		}
		return "", false
	}
	// FR-459 终验 Low #3：清除熔断态持久化文件（否则 Worker 重启后会误恢复已解除的熔断）。
	m.clearPersistedCircuit(uuid)

	m.mu.Lock()
	if inst, ok := m.instances[uuid]; ok {
		inst.AutoRestart = true
	}
	m.mu.Unlock()
	m.clearHealthFault(uuid)
	// FR-459 终验 Major：向仍在托管的 daemon wrapper 复位粘性「禁用自动重启」。
	// 熔断在 Java 仍运行时触发只发禁用帧、保 RUNNING，此处若不复位，Java 下次崩溃仍被 wrapper
	// 拒绝重启，而记账/CP 却认为已解除（解除仅在实例停/重建后才生效的隐性降级）。
	m.rearmAutoRestart(uuid)
	m.auditHealthManual("health.circuit_released", uuid, "人工确认解除熔断（人工启动）："+reason)
	slog.Info("实例熔断已人工解除，恢复自动重启", "instanceId", uuid, "wasReason", reason)
	return reason, true
}

// autoRestartAllowed 报告实例当前是否允许自动重启（熔断期间为 false）。
// 供 direct/docker 策略的自动重启分支在决策前查询（镜像 wrapper.javaWait 的 fastCrashes 守卫）。
func (m *Manager) autoRestartAllowed(uuid string) bool {
	_, broken := m.isCircuitBroken(uuid)
	return !broken
}

// auditHealth 记录一条 FR-459 健康/自愈审计：复用 FR-455/456 的孤儿审计通道
// （Worker→CP 同址出站 gRPC），detail 标注 operator=auto（spec §2.2 动作分级）。
func (m *Manager) auditHealth(action, targetID, reason string, success bool, errMsg string) {
	detail := fmt.Sprintf(`{"operator":"auto","kind":"health","reason":%q}`, reason)
	m.auditOrphan(action, targetID, detail, success, errMsg)
}

// auditHealthManual 记录一条**人工触发**的健康类动作审计（detail 标 operator=manual）：
// 与自动自愈区分开，供运维追责面判断熔断是「谁解除的」（spec §2.2 动作分级）。
func (m *Manager) auditHealthManual(action, targetID, reason string) {
	detail := fmt.Sprintf(`{"operator":"manual","kind":"health","reason":%q}`, reason)
	m.auditOrphan(action, targetID, detail, true, "")
}

// HealthScanner FR-459 运行期周期巡检器（仿 OrphanScanner：New... + Start(ctx) + ScanOnce）。
type HealthScanner struct {
	mgr *Manager
	// localEnabled 是本地配置开关（worker.health_scan_enabled）；关闭=硬关，CP 无法远程开启。
	localEnabled bool
	// baseInterval 是本地配置的周期（worker.health_scan_interval），CP 未下发时生效。
	baseInterval time.Duration

	// policyMu 守护 policy（CP 下发经 SetPolicy 覆盖）。
	policyMu sync.RWMutex
	policy   HealthPolicy

	// suspicionMu 守护逐实例假死嫌疑计数。
	suspicionMu sync.Mutex
	suspicion   map[string]int

	// 开关瞬变检测（FR-459 终验 Low #6）：上一拍「生效开关」状态。生效开关由
	// localEnabled ∧ CP 下发 Enabled 决定；由开→关时清一次健康记账，避免 CP 残留陈旧原因。
	policyToggleMu sync.Mutex
	lastActive     bool

	// 以下为可注入桩：nil=真实现，测试注入以免真探测/真重启。
	probeLiveness func(uuid string) InstanceEvidence
	probeTCP      func(port int) bool
	probeHTTP     func(port int) bool
	now           func() time.Time
	restart       func(uuid string) error
}

// NewHealthScanner 构造巡检器。enabled 为本地开关，interval<=0 取默认 30s，policy 为空取默认。
func NewHealthScanner(mgr *Manager, enabled bool, interval time.Duration, policy HealthPolicy) *HealthScanner {
	if interval <= 0 {
		interval = DefaultHealthScanInterval
	}
	pol := normalizeHealthPolicy(policy)
	s := &HealthScanner{
		mgr:          mgr,
		localEnabled: enabled,
		baseInterval: interval,
		policy:       pol,
		suspicion:    map[string]int{},
		probeTCP:     func(port int) bool { return metrics.TCPHealthProbe(healthScanProbeHost, port, 0) == nil },
		probeHTTP:    func(port int) bool { return metrics.HTTPHealthProbe(healthScanProbeHost, port) == nil },
		now:          time.Now,
	}
	s.lastActive = s.effectivePolicy().Enabled
	return s
}

// SetPolicy 应用 CP 下发的巡检策略（每次心跳调用、幂等）。enabled 由 localEnabled ∧ 下发值决定。
func (s *HealthScanner) SetPolicy(policy HealthPolicy) {
	if s == nil {
		return
	}
	pol := normalizeHealthPolicy(policy)
	s.policyMu.Lock()
	s.policy = pol
	s.policyMu.Unlock()
	s.noteToggle()
}

// noteToggle 检测「生效开关」由开→关的瞬变，并在该时刻清一次健康记账（FR-459 终验 Low #6）。
//
// 关闭后不再有巡检上报去纠正 CP，若不清理，最后一轮写入的「假死」原因会永久残留在面板与健康墙。
// 清空 records 后，后续心跳上报的 Health/StatusReason 为空，CP 据此把 status_reason 清掉。
func (s *HealthScanner) noteToggle() {
	active := s.effectivePolicy().Enabled
	s.policyToggleMu.Lock()
	was := s.lastActive
	s.lastActive = active
	s.policyToggleMu.Unlock()
	if was && !active {
		s.clearHealthRecords()
	}
}

// clearHealthRecords 清空全部健康故障记账（records）与假死嫌疑计数。
// 仅用于巡检开关由开→关的瞬变（FR-459 终验 Low #6）；熔断态（broken）不在此清理——解除只经人工入口。
func (s *HealthScanner) clearHealthRecords() {
	if s == nil || s.mgr == nil {
		return
	}
	s.mgr.clearAllHealthFaults()
	s.suspicionMu.Lock()
	s.suspicion = map[string]int{}
	s.suspicionMu.Unlock()
	slog.Info("实例健康巡检开关已关闭，已清空健康记账（避免面板残留陈旧原因）")
}

// effectivePolicy 返回当前生效策略（本地开关与 CP 策略合并）。
func (s *HealthScanner) effectivePolicy() HealthPolicy {
	s.policyMu.RLock()
	pol := s.policy
	s.policyMu.RUnlock()
	if pol.ScanInterval <= 0 {
		pol.ScanInterval = s.baseInterval
	}
	pol.Enabled = s.localEnabled && pol.Enabled
	return pol
}

// Start 启动周期巡检 goroutine（随 ctx 取消而退出）。mgr 为空或不启用时不启动。
//
// 用每轮重建的 timer 而非固定 ticker：使 CP 下发的 scan_interval 无需重启即生效
// （interval 变化在下一轮起算）。
func (s *HealthScanner) Start(ctx context.Context) {
	if s == nil || s.mgr == nil || !s.localEnabled {
		return
	}
	go func() {
		slog.Info("实例健康巡检已启用", "interval", s.baseInterval)
		for {
			interval := s.effectivePolicy().ScanInterval
			if interval <= 0 {
				interval = DefaultHealthScanInterval
			}
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				slog.Info("实例健康巡检已停止")
				return
			case <-timer.C:
				s.ScanOnce()
			}
		}
	}()
}

// ScanOnce 执行一轮巡检并（按策略）执行自愈动作。返回本轮结论（供单测/观测）。
// 开关关闭时不产生任何动作与结论（spec §4 验收 9）。
func (s *HealthScanner) ScanOnce() []HealthReport {
	if s == nil || s.mgr == nil {
		return nil
	}
	pol := s.effectivePolicy()
	// FR-459 终验 Low #6：开关由开→关的瞬变清一次健康记账（幂等；SetPolicy 亦会触发，双保险）。
	s.noteToggle()
	if !pol.Enabled {
		return nil
	}
	uuids := s.mgr.ListInstances()
	now := s.now()

	reports := make([]HealthReport, 0, len(uuids))
	for _, uuid := range uuids {
		reports = append(reports, s.scanOne(uuid, pol, now))
	}
	s.pruneSuspicion(uuids)
	return reports
}

// scanOne 巡检单个实例并返回结论。
//
// 顺序（FR-459 复审 blocker/major 修复后的口径）：
//  1. **熔断评估先于一切**：崩溃窗口对**所有在册实例**评估（含 RUNNING）——daemon 下 Java 崩溃时
//     wrapper 存活、只上抛退出事件，Worker 记账恒 RUNNING，若把熔断判定挂在「非 RUNNING」分支上，
//     判定永不执行（原实现 blocker）。
//  2. 非 RUNNING → 崩溃/瞬态分类（不再承担熔断判定）。
//  3. RUNNING → 存活维（不存活即崩溃）→ 响应维（不响应累计假死嫌疑），其中**启动宽限期**内
//     跳过假死判定，避免慢启动 MC 被误判（原实现 major）。
func (s *HealthScanner) scanOne(uuid string, pol HealthPolicy, now time.Time) HealthReport {
	mgr := s.mgr
	mgr.mu.RLock()
	inst, ok := mgr.instances[uuid]
	if !ok {
		mgr.mu.RUnlock()
		return HealthReport{UUID: uuid}
	}
	state := inst.State
	probePort := inst.ProbePort
	serverPort := inst.ServerPort
	autoRestart := inst.AutoRestart
	startedAt := inst.StartedAt
	mgr.mu.RUnlock()

	// 1) 熔断评估（含 RUNNING）。
	if rep, broken := s.evaluateCircuitBreaker(uuid, pol, now, state, autoRestart); broken {
		s.clearSuspicion(uuid)
		return rep
	}

	if state != StateRunning {
		s.clearSuspicion(uuid)
		return s.classifyNonRunning(uuid, state)
	}

	// 2) 存活维度：复用 FR-455/456 同源证据。
	ev := s.liveness(uuid)
	if !ev.Running {
		s.clearSuspicion(uuid)
		reason := "进程/容器不存活"
		mgr.setHealthFault(uuid, HealthFaultCrash, reason)
		return HealthReport{UUID: uuid, Liveness: false, Readiness: false, Fault: HealthFaultCrash, Reason: reason}
	}

	kind, port := readinessTarget(pol, probePort, serverPort)
	if port <= 0 {
		// 无可用响应维度（未部署探针且无端口）：仅存活即健康（spec §5 兜底）。
		s.clearSuspicion(uuid)
		mgr.setHealthFault(uuid, HealthFaultHealthy, "")
		return HealthReport{UUID: uuid, Liveness: true, Readiness: true, Healthy: true}
	}

	if s.probeReadiness(kind, port) {
		wasSuspect := s.clearSuspicion(uuid)
		// 响应恢复：清零假死自愈计数，使下一次故障重新获得完整重启配额（FR-459 复审项 7）。
		mgr.resetSelfHealRestarts(uuid)
		mgr.setHealthFault(uuid, HealthFaultHealthy, "")
		if wasSuspect {
			slog.Info("实例响应已恢复", "instanceId", uuid)
		}
		return HealthReport{UUID: uuid, Liveness: true, Readiness: true, Healthy: true}
	}

	// 3) 启动宽限期：Start 返回 RUNNING（或 daemon 下 Java 刚重启）后的 warmup 内不判假死。
	// MC 慢启动（World 加载/模组初始化）常远超单拍巡检周期，不加宽限会被判「存活但不响应」
	// 并在 action=restart 下反复重启一个正在正常启动的实例（FR-459 复审项 2）。
	if warmupRemaining := s.warmupRemaining(pol, startedAt, mgr.lastProcessCrash(uuid), now); warmupRemaining > 0 {
		s.clearSuspicion(uuid)
		// 不动既有健康故障（宽限期结论不明：既非确认假死、也非确认健康，不应清 CP 已写的原因）。
		fault, reason := mgr.healthFault(uuid)
		return HealthReport{UUID: uuid, Liveness: true, Readiness: false, Warmup: true, Fault: fault, Reason: reason}
	}

	// 存活 ∧ 不响应 → 假死嫌疑累积。
	n := s.bumpSuspicion(uuid)
	if n < pol.SuspicionThreshold {
		reason := fmt.Sprintf("假死嫌疑：进程在但 %s 响应探测连续失败 %d/%d 次", kind, n, pol.SuspicionThreshold)
		mgr.setHealthFault(uuid, HealthFaultSuspected, reason)
		return HealthReport{UUID: uuid, Liveness: true, Readiness: false, Fault: HealthFaultSuspected, Reason: reason}
	}

	reason := fmt.Sprintf("假死：进程在但 %s 响应探测连续失败 %d 次", kind, n)
	mgr.setHealthFault(uuid, HealthFaultDead, reason)
	rep := HealthReport{UUID: uuid, Liveness: true, Readiness: false, Fault: HealthFaultDead, Reason: reason}
	s.actOnDead(uuid, pol, reason, now, &rep)
	return rep
}

// warmupRemaining 返回启动宽限期的剩余时长（<=0 = 已过宽限或显式关闭）。
// 纪元起点取「实例启动时刻」与「最近一次崩溃时刻」的较晚者：崩溃即开启一个新启动纪元，
// 使 daemon 下 wrapper 内部重启 Java 后的慢启动同样被宽限（wrapper 存活、Worker 记账不变，
// 若只看 Start 时刻，Java 重启后的启动阶段会被误判假死）。
func (s *HealthScanner) warmupRemaining(pol HealthPolicy, startedAt, lastCrash, now time.Time) time.Duration {
	if pol.StartupWarmup < 0 {
		return 0 // 显式关闭宽限（测试/特殊场景）
	}
	epoch := startedAt
	if lastCrash.After(epoch) {
		epoch = lastCrash
	}
	if epoch.IsZero() {
		return 0 // 无纪元信息（如 PID 恢复实例）：不宽限，保持既有判定
	}
	return pol.StartupWarmup - now.Sub(epoch)
}

// evaluateCircuitBreaker 对单个实例评估崩溃熔断，返回（结论报告, 是否处于熔断态）。
//
// 「计数」与「判定」在这里完成，不再依赖实例是否处于非 RUNNING（原实现把判定放在
// classifyNonRunning，daemon 下 Java 崩溃而 wrapper 存活时 Worker 记账恒 RUNNING，
// 判定永不执行 = 熔断完全不成立）。计数由 noteProcessCrash/emitCrash 覆盖三条崩溃来源
// （direct/docker waitLoop、daemon wrapper 退出事件）。
func (s *HealthScanner) evaluateCircuitBreaker(uuid string, pol HealthPolicy, now time.Time, state InstanceState, autoRestart bool) (HealthReport, bool) {
	mgr := s.mgr
	if reason, broken := mgr.isCircuitBroken(uuid); broken {
		mgr.setHealthFault(uuid, HealthFaultCircuitBroken, reason)
		// FR-459 终验 Low #4：熔断持续期间每拍幂等补发一次禁用帧，兜底首帧丢失（连接瞬断/即时拨号失败）
		// 导致「熔断只生效于记账、wrapper 仍在自动重启」的哑火。
		mgr.redisarmAutoRestart(uuid, reason)
		return HealthReport{UUID: uuid, Fault: HealthFaultCircuitBroken, Reason: reason}, true
	}
	// 自动重启已被人工关闭（非本巡检导致）：无可熔断的自动重启，保持既有崩溃记账。
	if !autoRestart {
		return HealthReport{}, false
	}
	n := mgr.crashRestartCount(uuid, now, pol.CircuitBreakerWindow)
	if n < pol.CircuitBreakerThreshold {
		return HealthReport{}, false
	}
	reason := fmt.Sprintf("持续崩溃已熔断：%s 内重启 %d 次（阈值 %d），已停止自动重启等待人工确认",
		pol.CircuitBreakerWindow, n, pol.CircuitBreakerThreshold)
	// 进程侧证据仍显示在跑时只停自动重启、保留真实状态；证据不明/已停则置 CRASHED（spec §2.2）。
	forceCrash := true
	if state == StateRunning {
		forceCrash = !s.liveness(uuid).Running
	}
	rep := HealthReport{UUID: uuid, Fault: HealthFaultCircuitBroken, Reason: reason}
	if mgr.breakCircuit(uuid, reason, forceCrash) {
		rep.Actions = []string{HealthActionCircuitBreak}
		mgr.auditHealth("health.circuit_broken", uuid, reason, true, "")
		slog.Warn("实例持续崩溃，已熔断停止自动重启（等待人工确认）",
			"instanceId", uuid, "restarts", n, "window", pol.CircuitBreakerWindow)
	}
	mgr.setHealthFault(uuid, HealthFaultCircuitBroken, reason)
	return rep, true
}

// classifyNonRunning 处理非 RUNNING 实例（熔断已在上游统一评估，此处只标崩溃/瞬态）。
func (s *HealthScanner) classifyNonRunning(uuid string, state InstanceState) HealthReport {
	mgr := s.mgr
	switch state {
	case StateStopped:
		mgr.clearHealthFault(uuid)
		return HealthReport{UUID: uuid}
	case StateStarting, StateStopping:
		// 瞬态：不判假死、不动状态、不重复告警（保留既有原因）。
		_, reason := mgr.healthFault(uuid)
		return HealthReport{UUID: uuid, Reason: reason}
	}

	// CRASHED：自动重启已被人工关闭（非本巡检导致）时不判熔断。
	if !mgr.autoRestartEnabled(uuid) {
		reason := "实例已崩溃且未开启自动重启"
		mgr.setHealthFault(uuid, HealthFaultCrash, reason)
		return HealthReport{UUID: uuid, Fault: HealthFaultCrash, Reason: reason}
	}
	reason := "实例已崩溃（等待自动重启）"
	mgr.setHealthFault(uuid, HealthFaultCrash, reason)
	return HealthReport{UUID: uuid, Fault: HealthFaultCrash, Reason: reason}
}

// actOnDead 对已确认假死的实例执行配置动作（warn / restart）。
func (s *HealthScanner) actOnDead(uuid string, pol HealthPolicy, reason string, now time.Time, rep *HealthReport) {
	if _, broken := s.mgr.isCircuitBroken(uuid); broken {
		rep.Fault = HealthFaultCircuitBroken
		rep.Reason = "实例已熔断（自动重启已停止），跳过假死自愈"
		s.mgr.setHealthFault(uuid, HealthFaultCircuitBroken, rep.Reason)
		return
	}
	if pol.Action != HealthActionRestart {
		rep.Actions = append(rep.Actions, HealthActionWarn)
		s.mgr.auditHealth("health.dead_detected", uuid, reason, true, "")
		slog.Warn("实例假死（warn 档：仅告警 + 落审计 + 标原因，不动作）", "instanceId", uuid, "reason", reason)
		return
	}

	// 重启风暴护栏（FR-459 复审项 7）：崩溃路径有熔断兜底，假死路径此前没有任何次数上限，
	// 一个持续假死的实例会被每轮巡检重启一次（永不停止）。超过窗口内配额即降级为仅告警。
	if n := s.mgr.selfHealRestartCount(uuid, now, pol.CircuitBreakerWindow); n >= pol.SelfHealMaxRestarts {
		rep.Reason = fmt.Sprintf("%s；自愈重启已达上限（%s 内 %d 次），暂不再自动重启，等待人工介入",
			reason, pol.CircuitBreakerWindow, n)
		s.mgr.setHealthFault(uuid, HealthFaultDead, rep.Reason)
		s.mgr.auditHealth("health.selfheal_exhausted", uuid, rep.Reason, true, "")
		slog.Warn("实例假死自愈重启已达上限，暂停自动重启（等待人工介入）",
			"instanceId", uuid, "restarts", n, "window", pol.CircuitBreakerWindow)
		return
	}

	rep.Actions = append(rep.Actions, HealthActionRestart)
	err := s.restartInstance(uuid)
	if err != nil {
		s.mgr.auditHealth("health.selfheal_restart", uuid, reason, false, err.Error())
		rep.Reason = reason + "；优雅重启失败：" + err.Error()
		slog.Warn("实例假死自愈（优雅重启）失败", "instanceId", uuid, "reason", reason, "error", err)
		return
	}
	s.mgr.noteSelfHealRestart(uuid)
	s.mgr.auditHealth("health.selfheal_restart", uuid, reason, true, "")
	// 重启成功后复位嫌疑计数，避免下一轮立刻再次触发（重启期间实例可能仍不可探）。
	s.clearSuspicion(uuid)
	slog.Warn("实例假死，已优雅重启自愈", "instanceId", uuid, "reason", reason)
}

// restartInstance 执行假死自愈的优雅重启：默认走 RestartIfRunning（受锁 + 复核仍为运行类），
// **不新增杀进程路径**。
func (s *HealthScanner) restartInstance(uuid string) error {
	if s.restart != nil {
		return s.restart(uuid)
	}
	return s.mgr.RestartIfRunning(uuid)
}

// liveness 返回某实例的存活证据：默认复用 ProbeInstanceEvidence（与 FR-455/456 同源）。
func (s *HealthScanner) liveness(uuid string) InstanceEvidence {
	if s.probeLiveness != nil {
		return s.probeLiveness(uuid)
	}
	return s.mgr.probeOneEvidence(uuid)
}

// probeReadiness 按探针类型执行响应探测。
func (s *HealthScanner) probeReadiness(kind string, port int) bool {
	if kind == HealthProbeHTTP {
		return s.probeHTTP(port)
	}
	return s.probeTCP(port)
}

// readinessTarget 按策略与端口可用性择响应探测目标（kind, port）；无可探目标时 port<=0。
func readinessTarget(pol HealthPolicy, probePort, serverPort int) (string, int) {
	if pol.ProbeKind == HealthProbeHTTP {
		if probePort > 0 {
			return HealthProbeHTTP, probePort
		}
		if serverPort > 0 {
			return HealthProbeTCP, serverPort // 显式 http 但未部署探针：回退 TCP（spec §5 兜底）
		}
		return "", 0
	}
	if pol.ProbeKind == HealthProbeTCP {
		if serverPort > 0 {
			return HealthProbeTCP, serverPort
		}
		if probePort > 0 {
			return HealthProbeHTTP, probePort
		}
		return "", 0
	}
	// auto：优先服务端口（TCP，最贴近「能不能连」），否则探针（HTTP）。
	if serverPort > 0 {
		return HealthProbeTCP, serverPort
	}
	if probePort > 0 {
		return HealthProbeHTTP, probePort
	}
	return "", 0
}

// autoRestartEnabled 报告实例记账是否开启自动重启。
func (m *Manager) autoRestartEnabled(uuid string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[uuid]
	if !ok {
		return false
	}
	return inst.AutoRestart
}

// instanceState 返回某实例当前记账状态；实例不在册返回零值（StateStopped）。
func (m *Manager) instanceState(uuid string) InstanceState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inst, ok := m.instances[uuid]; ok {
		return inst.State
	}
	return StateStopped
}

// bumpSuspicion 递增某实例的假死嫌疑计数并返回新值。
func (s *HealthScanner) bumpSuspicion(uuid string) int {
	s.suspicionMu.Lock()
	defer s.suspicionMu.Unlock()
	s.suspicion[uuid]++
	return s.suspicion[uuid]
}

// clearSuspicion 清零某实例嫌疑计数，返回此前是否有未清计数（用于恢复降噪）。
func (s *HealthScanner) clearSuspicion(uuid string) bool {
	s.suspicionMu.Lock()
	defer s.suspicionMu.Unlock()
	had := s.suspicion[uuid] > 0
	delete(s.suspicion, uuid)
	return had
}

// pruneSuspicion 清理已不在册实例的嫌疑计数（实例删除/迁移）。
func (s *HealthScanner) pruneSuspicion(uuids []string) {
	known := make(map[string]struct{}, len(uuids))
	for _, u := range uuids {
		known[u] = struct{}{}
	}
	s.suspicionMu.Lock()
	for u := range s.suspicion {
		if _, ok := known[u]; !ok {
			delete(s.suspicion, u)
		}
	}
	s.suspicionMu.Unlock()
}
