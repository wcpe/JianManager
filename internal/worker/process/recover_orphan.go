package process

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-325：Worker 重启接管扫描中「wrapper 存活但 reconnect 拨号失败」的兜底。
// 旧行为只删 PID 文件，活着的 wrapper/Java 从此不可发现（孤儿永久化，真机事故：
// 残留 java 占 Paper session.lock 致实例再也起不来）。现改为：
// 有界重试（期间保留 PID 文件）→ 耗尽后按 PID 记录强杀孤儿进程树 → 死透才清理。
//
// FR-455①/ADR-093 修订：强杀只对**真孤儿**（wrapper 已确证死亡、仅剩 Java）生效；
// 重试耗尽但 wrapper 仍存活（确属本实例、仅 socket 瞬时不可达）一律只告警 + 落审计、保留 PID 文件。
// 且任何强杀前必须通过归属复核 verifyProcessOwnership，复核不过同样只告警不杀。

// DefaultRecoverRetryBackoff reconnect 失败的有界重试间隔默认序列（递增，覆盖分钟级瞬时故障，FR-455①）。
// 旧序列仅 {1s,2s,4s}≈7s：交接窗口的 socket 未就绪/资源紧张常是瞬时的，多等一轮即可接管，
// 无需牺牲服务器。扩为 1s→2s→4s→8s→16s→32s→64s（≈127s），显著降低「拨不通即强杀」的误杀面。
//
// 导出以便 worker 配置层（internal/worker/config.go）引用为默认口径的单一真源，
// 并可经配置 recover.retry_backoff / Manager.SetRecoverRetryBackoff 注入（测试用小值）。
var DefaultRecoverRetryBackoff = []time.Duration{
	time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
	16 * time.Second, 32 * time.Second, 64 * time.Second,
}

// SetRecoverRetryBackoff 注入接管 reconnect 的重试间隔序列（FR-455①，worker main 按配置装配）。
//
// 空/nil 表示回退默认序列；非正项被丢弃（它们只会造成无意义的重试），全部非法时同样回退默认。
// 入口做防御性拷贝，避免调用方后续改写切片影响运行期重试节奏。
func (m *Manager) SetRecoverRetryBackoff(seq []time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoverBackoff = normalizeRecoverBackoff(seq)
}

// recoverRetrySequence 返回当前生效的重试间隔序列（配置注入优先，缺省即默认序列）。
func (m *Manager) recoverRetrySequence() []time.Duration {
	m.mu.RLock()
	seq := m.recoverBackoff
	m.mu.RUnlock()
	if len(seq) > 0 {
		return seq
	}
	return DefaultRecoverRetryBackoff
}

// normalizeRecoverBackoff 过滤序列中的非正项并拷贝；无有效项返回 nil（调用方回退默认序列）。
func normalizeRecoverBackoff(seq []time.Duration) []time.Duration {
	out := make([]time.Duration, 0, len(seq))
	for _, d := range seq {
		if d > 0 {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// errOrphanedWrapperGone 标记「wrapper 已死、仅剩 Java 孤儿」的接管场景：
// 无需再杀 wrapper（已不存在），只需清掉残留的 Java，故跳过其杀树与存活复核。
var errOrphanedWrapperGone = errors.New("wrapper 已不存活，仅剩 Java 孤儿")

// 强杀后存活复核的有界等待：Windows taskkill /T /F 异步终止进程树，
// 杀完立查可能误报存活，故轮询确认（上限 attempts×interval）。
const (
	orphanKillVerifyAttempts = 10
	orphanKillVerifyInterval = 100 * time.Millisecond
)

// dialWrapper 恢复路径的 reconnect 拨号；测试经 recoverDial 注入假拨号。
func (m *Manager) dialWrapper(s *daemonStrategy, addr string) error {
	if m.recoverDial != nil {
		return m.recoverDial(s, addr)
	}
	return s.Reconnect(addr)
}

// pidAlive 进程存活探测；测试经 recoverPIDAlive 注入假读数。
func (m *Manager) pidAlive(pid int) bool {
	if m.recoverPIDAlive != nil {
		return m.recoverPIDAlive(pid)
	}
	return daemon.IsPIDAlive(pid)
}

// killTree 按 PID 强杀整棵进程树；测试经 recoverKillTree 注入假杀手。
func (m *Manager) killTree(pid int) error {
	if m.recoverKillTree != nil {
		return m.recoverKillTree(pid)
	}
	return daemon.KillPIDTree(pid)
}

// retrySleep 重试/复核间隔等待；测试经 recoverSleep 注入免真等待。
func (m *Manager) retrySleep(d time.Duration) {
	if m.recoverSleep != nil {
		m.recoverSleep(d)
		return
	}
	time.Sleep(d)
}

// reconnectWithRetry 对存活 wrapper 做有界重试的 reconnect 拨号（FR-325）。
// 首拨失败后按当前生效的重试序列（配置注入优先，缺省 DefaultRecoverRetryBackoff）递增间隔重试；
// 期间不动 PID 文件（保证实例在后续扫描中仍可发现）。全部失败返回最后一次错误。
func (m *Manager) reconnectWithRetry(strategy *daemonStrategy, addr, instanceUUID string) error {
	err := m.dialWrapper(strategy, addr)
	if err == nil {
		return nil
	}
	return m.retryReconnect(strategy, addr, instanceUUID, err)
}

// retryReconnect 在「首拨已失败」的前提下，按当前生效序列继续有界重试（FR-455①）。
//
// firstErr 是首拨的错误，作为本轮重试的起点错误；重试间隔与次数与旧实现逐字一致
// （reconnectWithRetry = 首拨 + 本函数），差别只在于调用时机——启动路径把它搬到后台，
// 使一段可能长达 127s 的等待不再挡住运行期孤儿扫描器的启用。
func (m *Manager) retryReconnect(strategy *daemonStrategy, addr, instanceUUID string, firstErr error) error {
	backoffs := m.recoverRetrySequence()
	err := firstErr
	for i, backoff := range backoffs {
		slog.Warn("reconnect wrapper 失败，保留 PID 文件稍后重试",
			"instanceId", instanceUUID, "attempt", i+1, "maxRetries", len(backoffs),
			"backoff", backoff, "error", err)
		m.retrySleep(backoff)
		if err = m.dialWrapper(strategy, addr); err == nil {
			slog.Info("reconnect wrapper 重试成功", "instanceId", instanceUUID, "attempt", i+1)
			return nil
		}
	}
	return err
}

// startupTakeoverTask 是一条「wrapper 存活但首拨失败」的 PID 记录：启动路径已放弃就地重试，
// 转由后台继续（见 startStartupTakeoverRetries）。
//
// strategy 复用首拨时构造的同一对象（与旧实现的就地重试同源），不对 wrapper 建立第二条控制连接。
type startupTakeoverTask struct {
	instanceUUID string
	rec          *daemon.PIDRecord
	strategy     *daemonStrategy
	firstErr     error
}

// takeoverPeer 是「启动接管在飞」期间对外声明的一条保护信息，供运行期孤儿扫描识别并让路。
type takeoverPeer struct {
	instanceUUID string
	wrapperPID   int
	javaPID      int
	workDir      string
}

// markTakeoverPending 声明某实例的启动接管仍在后台重试（本缺陷修复的并发隔离面）。
// 扫描器的第 0 相会跳过它、direct 相会保护它的 PID 与工作目录，避免争抢控制连接或误判强杀。
func (m *Manager) markTakeoverPending(instanceUUID string, rec *daemon.PIDRecord) {
	if m == nil || instanceUUID == "" || rec == nil {
		return
	}
	m.mu.Lock()
	if m.takeoverPending == nil {
		m.takeoverPending = make(map[string]takeoverPeer, 1)
	}
	m.takeoverPending[instanceUUID] = takeoverPeer{
		instanceUUID: instanceUUID,
		wrapperPID:   rec.WrapperPID,
		javaPID:      rec.JavaPID,
		workDir:      rec.WorkDir,
	}
	m.mu.Unlock()
}

// clearTakeoverPending 撤销在飞声明：该实例的接管已收尾（成功登记或已落审计），扫描不必再让路。
func (m *Manager) clearTakeoverPending(instanceUUID string) {
	if m == nil || instanceUUID == "" {
		return
	}
	m.mu.Lock()
	delete(m.takeoverPending, instanceUUID)
	m.mu.Unlock()
}

// takeoverPendingHas 报告该实例的启动接管是否仍在后台重试（第 0 相据此跳过，避免并发拨号）。
func (m *Manager) takeoverPendingHas(instanceUUID string) bool {
	if m == nil || instanceUUID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.takeoverPending[instanceUUID]
	return ok
}

// takeoverPendingPeers 返回在飞集合的快照（扫描轮开始时取一次，避免整轮反复加锁）。
func (m *Manager) takeoverPendingPeers() []takeoverPeer {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.takeoverPending) == 0 {
		return nil
	}
	out := make([]takeoverPeer, 0, len(m.takeoverPending))
	for _, peer := range m.takeoverPending {
		out = append(out, peer)
	}
	return out
}

// takeoverProtects 报告 pid / workDir 是否属于「仍在飞的启动接管」。
//
// 处置动作（killTree）前的最后一道复核用它兜住 TOCTOU：direct 相的受管集合在轮首取一次，
// 若后台接管在此期间登记成功，集合就已成陈旧视图——此时唯一正确的动作是放弃处置。
func (m *Manager) takeoverProtects(pid int, workDir string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.takeoverPending) == 0 {
		return false
	}
	cleanWorkDir := ""
	if workDir != "" {
		cleanWorkDir = filepath.Clean(workDir)
	}
	for _, peer := range m.takeoverPending {
		if pid > 0 && (pid == peer.wrapperPID || pid == peer.javaPID) {
			return true
		}
		if cleanWorkDir != "" && peer.workDir != "" && filepath.Clean(peer.workDir) == cleanWorkDir {
			return true
		}
	}
	return false
}

// WaitStartupTakeover 等待启动接管的后台重试收敛；timeout<=0 表示无限等待。超时返回 false。
//
// 生产启动路径**不**等（后台重试本就允许慢），本方法供测试断言与优雅关闭路径使用。
func (m *Manager) WaitStartupTakeover(timeout time.Duration) bool {
	if m == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		m.takeoverWG.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// startStartupTakeoverRetries 启动后台协程，按原序列继续完成首拨失败记录的有界重试（本缺陷修复）。
//
// 串行处理各记录（与旧实现同序），因此单条最长 ~127s 的等待只推迟**其它记录的接管**，
// 不再推迟扫描器启用；接管收尾语义（成功登记 / alive_unreachable 审计 / 只观测不强杀）逐字沿用。
func (m *Manager) startStartupTakeoverRetries(tasks []startupTakeoverTask) {
	if len(tasks) == 0 {
		return
	}
	m.takeoverWG.Add(1)
	go func() {
		defer m.takeoverWG.Done()
		for _, task := range tasks {
			err := m.retryReconnect(task.strategy, task.rec.SocketAddr, task.instanceUUID, task.firstErr)
			if err == nil {
				m.registerAdoptedDaemon(task.instanceUUID, task.rec, task.strategy)
			} else {
				m.settleStartupTakeoverFailure(task.instanceUUID, task.rec, err)
			}
			// 撤销让路必须在收尾动作**之后**：处置/登记期间扫描器仍须回避该实例。
			m.clearTakeoverPending(task.instanceUUID)
		}
	}()
}

// settleStartupTakeoverFailure 接管重试彻底失败后的归类处置（FR-325 / ADR-093 / FR-471，语义与旧实现一致）：
//   - wrapper 仍存活：属「确属本实例、仅 socket 瞬时不可达」→ 只告警 + 落审计、保留 PID 文件、不杀
//     （强杀会误杀一个 wrapper 与 Java 都健康、只是暂时拨不通的服务器）；
//   - wrapper 已死：只观测 + 落审计（启动路径无任何静默破坏），交由周期扫描按 policy 与 FR-471 接管处置。
func (m *Manager) settleStartupTakeoverFailure(instanceUUID string, rec *daemon.PIDRecord, reconnectErr error) {
	if rec.WrapperPID > 0 && m.pidAlive(rec.WrapperPID) {
		detail := fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"reason":"alive_unreachable"}`,
			instanceUUID, rec.WrapperPID, rec.JavaPID)
		m.auditOrphan("orphan.dispose_blocked", instanceUUID, detail, false,
			"接管重试耗尽但 wrapper 仍存活（仅瞬时不可达），按 ADR-093 只告警不杀")
		slog.Warn("接管重试耗尽但 wrapper 仍存活，按 ADR-093 只告警不杀，保留 PID 文件等下一轮/人工介入",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID, "error", reconnectErr)
		return
	}
	m.observeOrphanAtStartup(instanceUUID, rec, "startup_reconnect_failed")
}

// registerAdoptedDaemon 把「已与 wrapper 建立控制连接」的接管结果登记为 RUNNING
// （FR-455① 启动接管 / FR-497③ 自动收养共用同一收尾口径）。
//
// 登记前在锁内**二次确认未纳管**（与 adoptUnmanagedRecord 同口径）：拨号期间可能有并发路径抢先
// 纳管（运行期扫描的第 0 相自动收养、CP 重推后的人工 Start）。此时丢弃本次连接（strategy.Close()
// 只断连接、不下发停止帧、不杀进程），避免两个策略对象争抢 wrapper 只保留最新一条的控制连接。
//
// 旧实现无条件覆盖内存表即可，因为扫描器在接管之后才启动、不存在并发路径；本缺陷修复让接管重试
// 与扫描器并行后，这条确认从「防御」变成**现实必需**。
func (m *Manager) registerAdoptedDaemon(instanceUUID string, rec *daemon.PIDRecord, strategy *daemonStrategy) bool {
	strategy.SetWrapperPID(rec.WrapperPID)
	// 接管侧的 cgroup 隔离（见 daemon/cgroup.go）：已运行的 wrapper 由**旧二进制** spawn，仍在 Worker
	// 单元 cgroup 内。若不在此刻迁移，本次部署后的第一个 `systemctl restart` 依旧会连坐杀掉全部实例
	// ——那正是本缺陷的验收场景本身。迁移只改 cgroup 归属、不发任何信号，失败仅告警、保持原状。
	m.isolateAdoptedWrapper(instanceUUID, rec)

	// FR-459 终验 Low #3：若 Worker 重启前该实例已熔断，从状态文件恢复熔断态——否则 Worker
	// 会「忘掉」熔断（以为可自动重启）而 wrapper 粘性 autoRestartOff 仍在拒绝重启，形成反向 desync。
	restored := m.restorePersistedCircuit(instanceUUID)

	m.mu.Lock()
	if cur, ok := m.instances[instanceUUID]; ok && (cur.strategy != nil || mayOwnLiveProcess(cur.State)) {
		state := cur.State
		m.mu.Unlock()
		_ = strategy.Close()
		slog.Info("启动接管在登记前发现实例已被其它路径纳管，放弃本次连接",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "state", string(state))
		return false
	}
	m.instances[instanceUUID] = &Instance{
		UUID:  instanceUUID,
		State: StateRunning,
		// StartedAt：与自动收养同口径（FR-497③ 补验）——本路径同样是「从无到 RUNNING」的跳变，
		// 不写则 warmupRemaining 拿不到纪元、直接返回 0，刚接管的实例失去启动宽限期。
		StartedAt:         time.Now(),
		AutoRestart:       !restored,
		WorkDir:           rec.WorkDir,
		ProbePort:         rec.ProbePort,
		strategy:          strategy,
		strategyStale:     true,
		launchSpecPending: true,
		processType:       ProcessTypeDaemon,
	}
	m.mu.Unlock()
	if restored {
		// 恢复熔断后须让 wrapper 与 Worker 记账对齐：worker 重启前 wrapper 的粘性开关可能已置位，
		// 也可能因重启窗口丢失该帧——幂等补发一次禁用帧（wrapper 已置位时无副作用）。
		m.disarmAutoRestart(instanceUUID, "恢复熔断态（Worker 重启）")
	}
	slog.Info("已恢复 daemon 实例", "instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "circuitRestored", restored)
	return true
}

// observeOrphanAtStartup 在 Worker 启动恢复路径上「只观测、不处置」一条孤儿（FR-471）。
//
// 为什么启动路径必须非破坏：换二进制/重启 Worker 是高频运维动作，而 PID 目录里可能残留
// 陈旧记录（wrapper 已死、Java PID 已被复用或本就属他人）。旧行为在此按记录强杀 Java 树，
// 真机事故（2026-09-23）一次打断 54 台在跑农场。既然 FR-471 已提供启动冲突预检（防双开）与
// 漂移呈现 + 接管入口，启动路径就不再需要任何静默破坏：
//
//   - 只记审计（`orphan.startup_detected_not_reaped`，success=true 表示「观测成功」而非「已处置」）
//     并打 WARN，**保留 PID 文件**；
//   - 交周期孤儿扫描按 `orphan.dispose_policy` 处置（默认 warn 只告警；显式 auto 才强杀）；
//   - FR-471 的漂移观测与 `adopt-runtime` 接管是人工/受控的解决出口。
//
// reason 用于审计定位触发分支（startup_wrapper_gone_java_alive / startup_reconnect_failed）。
func (m *Manager) observeOrphanAtStartup(instanceUUID string, rec *daemon.PIDRecord, reason string) {
	if rec == nil {
		return
	}
	detail := fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"kind":%q,"policy":"observe","workDir":%q}`,
		instanceUUID, rec.WrapperPID, rec.JavaPID, reason, rec.WorkDir)
	m.auditOrphan("orphan.startup_detected_not_reaped", instanceUUID, detail, true, "")
	slog.Warn("启动期发现 daemon 孤儿：按 FR-471 非破坏策略只观测不强杀（保留 PID 文件，交由周期扫描与接管流程处置）",
		"instanceId", instanceUUID, "reason", reason,
		"wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID, "workDir", rec.WorkDir)
}

// reapOrphanWrapper 处置孤儿进程树（FR-325 及同源的 wrapper 已死场景）。
//
// 仅由**显式 auto 策略**的周期孤儿扫描调用（FR-471 起，启动恢复路径不再调用本函数）。
// // 两种入口：
//   - reconnectErr == errOrphanedWrapperGone：wrapper 已经不在，只剩 Java 孤儿（**真孤儿**）。
//     只清 Java，跳过对已消失 wrapper 的杀树与存活复核。
//   - 其余（reconnect 重试耗尽）：先判定 wrapper 是否仍存活——
//     · wrapper 仍存活：属「确属本实例、仅 socket 瞬时不可达/暂不健康」（ADR-093 决策 1），
//     **只告警 + 落审计、保留 PID 文件、不杀**，等下一轮扫描或人工介入（强杀会误杀一个
//     wrapper 与 Java 都健康、只是 socket 一时拨不通的运行中服务器）；
//     · wrapper 已在重试期间死亡：退化为真孤儿场景，按 PID 记录强杀 Java 树。
//     Unix 上 Java 经 wrapper 的 applyProcAttr 自成进程组，杀 wrapper 组够不到它，
//     而 Java 正是占 session.lock 的真孤儿；Windows 上 taskkill /T 已覆盖子树，
//     补杀已死 PID 报错无害（以存活复核为准）。
//
// PID 文件处置语义：确认全部死透 → 清 PID 文件与残留 socket；仍有存活（权限不足等）→
// 保留 PID 文件，让下次 Worker 重启的接管扫描仍能发现并再次兜底，杜绝孤儿永久失联。
//
// FR-455① 处置前置存活复核：任何 killTree 之前，先以 cmdline/cwd 复核该 PID 是否确属本实例
// （verifyProcessOwnership）。复核不通过（PID 可能已被 OS 复用给无关进程）→ **只告警 + 落审计，
// 不杀**，并保留 PID 文件等下一轮扫描或人工介入，杜绝误杀。
func (m *Manager) reapOrphanWrapper(instanceUUID, pidPath string, rec *daemon.PIDRecord, reconnectErr error) {
	wrapperGone := errors.Is(reconnectErr, errOrphanedWrapperGone)

	// ADR-093 决策 1（覆盖 spec §2.1 旧表述「复核不通过→只告警」）：接管重试耗尽但 wrapper 仍存活，
	// 判为「确属本实例、仅瞬时不可达」——只告警、保留 PID 文件、不杀。只有当 wrapper 已确证死亡
	// （真孤儿）才进入处置。存活判据即「wrapper 进程仍存活」，与 socket 是否拨通无关。
	if !wrapperGone && rec.WrapperPID > 0 && m.pidAlive(rec.WrapperPID) {
		detail := fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"reason":"alive_unreachable"}`,
			instanceUUID, rec.WrapperPID, rec.JavaPID)
		m.auditOrphan("orphan.dispose_blocked", instanceUUID, detail, false,
			"接管重试耗尽但 wrapper 仍存活（仅瞬时不可达），按 ADR-093 只告警不杀")
		slog.Warn("接管重试耗尽但 wrapper 仍存活，按 ADR-093 只告警不杀，保留 PID 文件等下一轮/人工介入",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID, "error", reconnectErr)
		return
	}
	if !wrapperGone {
		// wrapper 已在重试期间死亡：退化为「wrapper 死 / Java 活」真孤儿场景，仅处置 Java。
		slog.Warn("reconnect 重试期间 wrapper 已死亡，退化为 Java 真孤儿处置",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
	}

	// 到达此处仅剩「wrapper 已不存活、Java 仍活」的真孤儿场景（wrapper 存活的瞬时不可达已在上面
	// 只告警返回）。
	slog.Warn("wrapper 已不存活但 Java 仍活，按 PID 记录强杀 Java 孤儿",
		"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)

	// 组装待处置目标：wrapper 已消失，只针对 Java（对已死 PID 杀树只会刷无意义告警，
	// 且其 PGID 可能已被系统复用，误杀无关进程）。仅对仍存活的 Java 建目标。
	type orphanTarget struct {
		pid           int
		expectWrapper bool
		role          string
	}
	targets := make([]orphanTarget, 0, 1)
	if rec.JavaPID > 0 && rec.JavaPID != rec.WrapperPID && m.pidAlive(rec.JavaPID) {
		targets = append(targets, orphanTarget{pid: rec.JavaPID, expectWrapper: false, role: "java"})
	}

	keptByVerify := false
	killedPIDs := make([]int, 0, len(targets))
	for _, tgt := range targets {
		// FR-455①：处置前 cmdline 存活复核。不通过只告警 + 落审计，不杀。
		if !m.verifyProcessOwnership(tgt.pid, instanceUUID, rec.WorkDir, tgt.expectWrapper) {
			keptByVerify = true
			detail := fmt.Sprintf(`{"instanceUuid":%q,"pid":%d,"role":%q,"reason":"ownership_unverified"}`, instanceUUID, tgt.pid, tgt.role)
			m.auditOrphan("orphan.dispose_blocked", instanceUUID, detail, false, "处置前置存活复核不通过，拒绝强杀（PID 可能已被复用）")
			slog.Warn("孤儿处置前置存活复核不通过，只告警不杀，保留 PID 文件",
				"instanceId", instanceUUID, "pid", tgt.pid, "role", tgt.role, "workDir", rec.WorkDir)
			continue
		}
		if err := m.killTree(tgt.pid); err != nil {
			slog.Warn("强杀孤儿进程树报错（可能已死或权限不足，以存活复核为准）",
				"instanceId", instanceUUID, "pid", tgt.pid, "role", tgt.role, "error", err)
		}
		killedPIDs = append(killedPIDs, tgt.pid)
	}

	if len(killedPIDs) > 0 && !m.waitPIDsGone(killedPIDs) {
		slog.Warn("孤儿进程树强杀后仍有存活，保留 PID 文件待下次接管扫描再兜底",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
		return
	}
	if keptByVerify {
		// 有 PID 因复核不通过未处置 → 保留 PID 文件等下一轮或人工介入，绝不因「部分处置」误清。
		return
	}

	_ = os.Remove(pidPath)
	if rec.SocketAddr != "" {
		daemon.RemoveSocket(rec.SocketAddr)
	}
	m.auditOrphan("orphan.dispose_reaped", instanceUUID,
		fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d}`, instanceUUID, rec.WrapperPID, rec.JavaPID),
		true, "")
	slog.Warn("孤儿进程树已强杀并清理 PID 文件",
		"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
}

// waitPIDsGone 有界等待一组 PID 全部退出（0/负值跳过），全部退出返回 true。
func (m *Manager) waitPIDsGone(pids []int) bool {
	for attempt := 0; attempt < orphanKillVerifyAttempts; attempt++ {
		if attempt > 0 {
			m.retrySleep(orphanKillVerifyInterval)
		}
		anyAlive := false
		for _, pid := range pids {
			if pid > 0 && m.pidAlive(pid) {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			return true
		}
	}
	return false
}
