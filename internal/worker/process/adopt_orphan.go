package process

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// ③：未纳管活进程的全自动收养。
//
// 背景：Worker 重启时 RecoverDaemonInstances 对「wrapper 仍存活、仅 socket
// 瞬时不可达」的实例只告警 + 保留 PID 文件、不杀（这是对的——绝不误杀健康服务器）；但该实例此后
// **没有任何出口**：启动接管只有一次机会，周期扫描的 scanWrapperGone 只抓「wrapper 死 / Java 活」，
// 对「wrapper 与 Java 都活、只是当时拨不通」完全沉默。于是它永久脱管——健康巡检不覆盖、终端与日志
// 采集没有控制连接、心跳不上报，CP 面板 STOPPED 而磁盘上服务器正在服务（形态）。
//
// 本文件补上这个出口：发现 PID 目录中「未纳管的活进程对」（wrapper 与 Java 均活、PID 文件有效）
// → 归属复核通过 → **只重连、登记为 RUNNING**；不重启、不杀、不碰进程（PID 不变）。
// 判据与 docs/specs/restart-resilience/spec.md §2.1 完全一致：复核不通过一律只告警。
//
// 与（AdoptForeignRuntime）的分工：那条路径处置的是**外来**进程（无 PID 记录、非 wrapper，
// 人工确认后先停后启）；本路径只接管**平台自有 wrapper**（PID 记录 + 环境 UUID 确证），全程不碰进程。

// DefaultOrphanAutoAdopt 未纳管活进程自动收养的默认开关（③，默认启用）。
// 单一真源：worker 配置层（internal/worker/config.go 的 orphan_scan.auto_adopt）引用本常量。
const DefaultOrphanAutoAdopt = true

// unmanagedLiveRecord 一条「未纳管的活进程对」记录（③ 发现阶段产物，纯观测、无副作用）。
type unmanagedLiveRecord struct {
	instanceUUID string
	rec          *daemon.PIDRecord
}

// discoverUnmanagedLiveRecords 是 ③ 的**发现阶段**：枚举 PID 目录，返回本轮全部「PID 记录有效、
// wrapper 与 Java 均存活、且本 Worker 尚未纳管」的记录。不拨号、不登记、不落审计、不碰进程。
//
// 判据与收养阶段的前置条件逐条一致（见 AdoptUnmanagedDaemonInstances 的 1~3 条），故两个阶段不会分歧：
//  1. 记录有效：WrapperPID / JavaPID 均 >0 且互不相同；记录内 InstanceUUID 非空时必须与文件名一致；
//  2. wrapper 与 Java **均存活**（任一不活即跳过：wrapper 死/Java 活属周期扫描的处置域，不是本相）；
//  3. 未纳管（内存表无该 UUID，或有但无 strategy 且状态不属「可能仍有活进程」三态）——已纳管则跳过（幂等）。
//
// 刻意**止于「存活」而不含归属复核**：复核是**收养动作**的安全闸（见 adoptUnmanagedRecord），而发现阶段
// 不做任何处置，无须它的保护。若把复核塞进发现阶段，Windows（无 /proc，wrapper 分支 fail-closed）会连
// 告警都发不出，平台重新回到「磁盘在跑、控制面完全沉默」的原始缺陷（见下方 ③ 背景）。
//
// 成本很轻——一次 PID 目录枚举 + 每条记录两次存活探测（/proc 读取），与既有 scanWrapperGone 同量级，
// 故开关关闭时可直接复用这条发现逻辑，无须另开一条重度扫描路径。
//
// 返回的 error 仅表示 PID 目录读取失败（IsNotExist 已归一为「无记录、无错误」），由调用方决定告警口径。
func (m *Manager) discoverUnmanagedLiveRecords() ([]unmanagedLiveRecord, error) {
	entries, err := os.ReadDir(m.pidDir)
	if err != nil {
		// PID 目录不存在（尚未产生任何 daemon 实例）属正常，不当错误上抛。
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	out := make([]unmanagedLiveRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pid") {
			continue
		}
		instanceUUID := strings.TrimSuffix(entry.Name(), ".pid")
		pidPath := filepath.Join(m.pidDir, entry.Name())
		rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
		if err != nil {
			// 记录损坏/陈旧：不属本相（由启动扫描与删除路径处置），跳过。
			continue
		}
		if !adoptableRecord(instanceUUID, rec) {
			continue
		}
		if !m.pidAlive(rec.WrapperPID) || !m.pidAlive(rec.JavaPID) {
			// wrapper 死 / Java 活 或两者皆死：不是本相的候选（前者由周期扫描按 policy 处置）。
			continue
		}
		if m.instanceAdoptedOrLive(instanceUUID) {
			continue // 已纳管：幂等跳过（不重复拨号、不重复登记、不重复审计）
		}
		if m.takeoverPendingHas(instanceUUID) {
			// 该实例的启动接管仍在后台按序列重试（① 的重试窗口）。此相若并发拨号，会与接管
			// 争抢 wrapper 只保留最新一条的控制连接，且可能先于接管登记、令接管的二次确认白丢一条
			// 有效连接。让路给启动接管；接管收尾后本相下一轮自然接手（幂等跳过已登记者）。
			continue
		}
		out = append(out, unmanagedLiveRecord{instanceUUID: instanceUUID, rec: rec})
	}
	return out, nil
}

// AdoptUnmanagedDaemonInstances 执行一轮「未纳管活进程」自动收养（③）。返回成功收养数与观测结果。
//
// 发现判据见 discoverUnmanagedLiveRecords（1~3 条），此后第 4 条前置条件：
//  4. 归属复核通过（判据见 adoptUnmanagedRecord 与其调用的复核入口；复核不过只告警不收养）。
//
// 收养动作 = 构造 daemon 策略 + **单次** reconnect 拨号 + 登记 RUNNING（复用 熔断态恢复）。
// 拨号失败只告警并保留 PID 文件，交由下一轮扫描重试（不在扫描轮内做长退避，避免拖住整轮扫描）。
func (m *Manager) AdoptUnmanagedDaemonInstances() (int, []OrphanFinding) {
	if m == nil || m.pidDir == "" {
		return 0, nil
	}
	records, err := m.discoverUnmanagedLiveRecords()
	if err != nil {
		slog.Warn("未纳管活进程收养：读取 PID 目录失败，本轮跳过", "dir", m.pidDir, "error", err)
		return 0, nil
	}

	adopted := 0
	findings := make([]OrphanFinding, 0, len(records))
	for _, r := range records {
		ok := m.adoptUnmanagedRecord(r.instanceUUID, r.rec)
		if ok {
			adopted++
		}
		findings = append(findings, unmanagedLiveFinding(r, ok))
	}
	return adopted, findings
}

// DiscoverUnmanagedDaemonInstances 执行一轮「未纳管活进程」的**只发现不处置**观测（③）。
//
// 用途：`orphan_scan.auto_adopt=false` 时仍有观测能力——规格 §2.3/§4 的口径是「`false` 只关收养」，
// 故开关关闭只应关闭**收养动作**（归属复核 + 重连 + 登记），不该关闭「发现并发告警」这件事本身。
// 否则开关一关，磁盘上跑着服务器而平台彻底沉默，运维连线索都没有（正是本次变更要收敛的缺陷形态）。
//
// 与 AdoptUnmanagedDaemonInstances 共用同一发现判据与同一枚举顺序，区别仅在于**不重连、不登记**；
// 返回的 finding 一律 Adopted=false，绝不调用 Start / killTree / signalTree（护栏五.1 同口径）。
// 告警与审计的**按变化去重**由调用方（OrphanScanner）负责——那是扫描节奏的降噪职责，不属于发现逻辑。
func (m *Manager) DiscoverUnmanagedDaemonInstances() []OrphanFinding {
	if m == nil || m.pidDir == "" {
		return nil
	}
	records, err := m.discoverUnmanagedLiveRecords()
	if err != nil {
		slog.Warn("未纳管活进程发现：读取 PID 目录失败，本轮跳过", "dir", m.pidDir, "error", err)
		return nil
	}
	findings := make([]OrphanFinding, 0, len(records))
	for _, r := range records {
		findings = append(findings, unmanagedLiveFinding(r, false))
	}
	return findings
}

// unmanagedLiveFinding 由一条发现记录构造观测结果；adopted 表示本轮是否已实际收养成功。
// PIDs 固定为 [wrapperPID, javaPID]，顺序即语义（与 adoptUnmanagedRecord 的取值一致）。
func unmanagedLiveFinding(r unmanagedLiveRecord, adopted bool) OrphanFinding {
	return OrphanFinding{
		Kind:         OrphanKindUnmanagedLiveRuntime,
		InstanceUUID: r.instanceUUID,
		WorkDir:      r.rec.WorkDir,
		PIDs:         []int{r.rec.WrapperPID, r.rec.JavaPID},
		Adopted:      adopted,
		Detail: fmt.Sprintf("PID 目录中未纳管的活进程对：wrapper(pid=%d) 与 Java(pid=%d) 均存活",
			r.rec.WrapperPID, r.rec.JavaPID),
	}
}

// adoptableRecord 校验 PID 记录本身是否具备「有效」形态（③）：
// wrapper/java PID 齐备且互异；记录内 UUID 非空时必须与 PID 文件名一致（不符即陈旧/错配，跳过）。
// 本条只做「记录形态校验」，不构成归属证明；归属必须再由 verifyAdoptOwnership 的强特征确证。
// 注意：这里**刻意不要求 WorkDir 非空**——发现阶段也复用本函数，而缺工作目录的记录仍应被观测到并
// 发告警（只是不能收养）；收养所需的「记录内容校验」在 adoptUnmanagedRecord 内单独把关。
func adoptableRecord(fileUUID string, rec *daemon.PIDRecord) bool {
	if rec == nil || fileUUID == "" {
		return false
	}
	if rec.WrapperPID <= 0 || rec.JavaPID <= 0 || rec.JavaPID == rec.WrapperPID {
		return false
	}
	if rec.InstanceUUID != "" && rec.InstanceUUID != fileUUID {
		return false
	}
	return true
}

// instanceAdoptedOrLive 报告该实例是否已被本 Worker 纳管（③ 幂等判据）。
//
// 已持有策略（strategy != nil）或状态属「可能仍有活进程」三态（RUNNING/STARTING/STOPPING，
// 见 mayOwnLiveProcess）都算已纳管：前者有控制连接，后者正处于生命周期操作中（重复收养会争抢
// wrapper 只保留最新一条的控制连接）。STOPPED/CRASHED 且无策略的登记（CP 重推的只补不覆盖）不算
// 纳管——磁盘上有活进程而记账为停止，正是本相要收敛的脱管形态。
func (m *Manager) instanceAdoptedOrLive(uuid string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[uuid]
	if !ok {
		return false
	}
	return inst.strategy != nil || mayOwnLiveProcess(inst.State)
}

// adoptUnmanagedRecord 对一条未纳管的活进程记录执行收养：归属复核 → 单次拨号 → 登记 RUNNING。
// 返回 true 表示已登记为 RUNNING（收养成功）；false 表示被复核拦截或拨号失败（只告警，PID 文件保留）。
//
// 安全闸（与 ① 同口径，但**判据更严**）：配送任何动作之前先做归属复核——
// wrapper 分支必须 argv 形如 `<worker> daemon` **且** 进程环境携带本实例 UUID；Java 分支必须有强特征
// （实例 UUID 出现在 cmdline 或进程环境）。cwd / cmdline 含工作目录这类弱特征**单独不足以判真**
// （`DefaultVerifyAdoptOwnership`）：收养会把 wrapper 子树迁进实例 cgroup 并取得其控制权，一旦按 cwd
// 单特征误收养，后续 Stop / Kill 就会打到无关进程上。任一不通过即放弃收养（`orphan.adopt_blocked`），
// **不杀、不重启、保留 PID 文件**——收养是非破坏动作，复核不过的代价只是「这次不接管」，而非「误杀」。
//
// 拨号目标取自 PID 记录的 socket 地址（实例级唯一），拨通即证明该实例的 wrapper 正在服务。
func (m *Manager) adoptUnmanagedRecord(instanceUUID string, rec *daemon.PIDRecord) bool {
	if ok, evidence := m.verifyAdoptOwnership(rec.WrapperPID, instanceUUID, rec.WorkDir, true); !ok {
		m.auditOrphan("orphan.adopt_blocked", instanceUUID,
			fmt.Sprintf(`{"instanceUuid":%q,"pid":%d,"role":"wrapper","reason":"ownership_unverified","evidence":%q,"workDir":%q}`,
				instanceUUID, rec.WrapperPID, evidence, rec.WorkDir),
			false, "wrapper 归属复核不通过，拒绝收养（PID 可能已被复用，或仅有 cwd 等弱特征）")
		slog.Warn("未纳管活进程归属复核不通过（wrapper 分支），只告警不收养，保留 PID 文件",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "workDir", rec.WorkDir, "evidence", evidence)
		return false
	}
	if ok, evidence := m.verifyAdoptOwnership(rec.JavaPID, instanceUUID, rec.WorkDir, false); !ok {
		m.auditOrphan("orphan.adopt_blocked", instanceUUID,
			fmt.Sprintf(`{"instanceUuid":%q,"pid":%d,"role":"java","reason":"ownership_unverified","evidence":%q,"workDir":%q}`,
				instanceUUID, rec.JavaPID, evidence, rec.WorkDir),
			false, "Java 归属复核不通过，拒绝收养（PID 可能已被复用，或仅有 cwd 等弱特征）")
		slog.Warn("未纳管活进程归属复核不通过（Java 分支），只告警不收养，保留 PID 文件",
			"instanceId", instanceUUID, "javaPid", rec.JavaPID, "workDir", rec.WorkDir, "evidence", evidence)
		return false
	}

	// WorkDir 从 PID 记录恢复，否则文件/配置操作会因空工作目录失败（与启动接管同源）。
	strategy := newDaemonStrategy(m, CommandSpec{
		UUID: instanceUUID, WorkDir: rec.WorkDir, ProcessType: ProcessTypeDaemon, ProbePort: rec.ProbePort,
	})
	// 单次拨号：失败即留待下一轮（扫描周期），不在本相内做长时间退避。
	if err := m.dialWrapper(strategy, rec.SocketAddr); err != nil {
		m.auditOrphan("orphan.adopt_failed", instanceUUID,
			fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"reason":"reconnect_failed","socket":%q}`,
				instanceUUID, rec.WrapperPID, rec.JavaPID, rec.SocketAddr),
			false, "归属复核通过但 reconnect 拨号失败，保留 PID 文件待下轮重试")
		slog.Warn("未纳管活进程收养拨号失败（socket 仍不可达），保留 PID 文件待下轮重试",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "socket", rec.SocketAddr, "error", err)
		return false
	}
	strategy.SetWrapperPID(rec.WrapperPID)

	// 终验 Low #3 同口径：若 Worker 重启前该实例已熔断，收养时同样恢复熔断态，
	// 避免「Worker 以为可自动重启」而 wrapper 的粘性 autoRestartOff 仍在拒绝，形成反向 desync。
	restored := m.restorePersistedCircuit(instanceUUID)

	m.mu.Lock()
	if cur, ok := m.instances[instanceUUID]; ok {
		if cur.strategy != nil || mayOwnLiveProcess(cur.State) {
			// 拨号期间已有并发路径抢先纳管（启动接管 / CP 重推后的人工 Start）：丢弃本次连接，
			// 避免两个策略对象争抢 wrapper 只保留最新一条的控制连接。只断连接，不下发停止帧。
			m.mu.Unlock()
			_ = strategy.Close()
			slog.Info("未纳管活进程在收养登记前已被其它路径纳管，放弃本次收养",
				"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID)
			return false
		}
		// 实例已在内存表中但处于脱管态（典型：Worker 硬崩后 CP ResyncInstances 按 STOPPED 重登记，
		// 而磁盘上进程仍在跑）。**就地补齐运行态**，保留 CP 已下发的规格（StartCommand / StopCommand /
		// EnvVars / JDK / 端口等）——整条覆盖会让后续 Restart 拿着空启动命令去拉起新 wrapper。
		if cur.WorkDir == "" {
			cur.WorkDir = rec.WorkDir
		}
		if cur.ProbePort == 0 {
			cur.ProbePort = rec.ProbePort
		}
		cur.strategy = strategy
		// 重连策略只负责当前控制通道；下一代必须按 CP 完整规格重建。
		cur.strategyStale = true
		cur.launchSpecPending = strings.TrimSpace(cur.StartCommand) == ""
		cur.processType = ProcessTypeDaemon
		cur.State = StateRunning
		// 启动宽限期的纪元起点：本路径让实例**从非 RUNNING 跳到 RUNNING**，与
		// startLocked 里的 `inst.StartedAt = time.Now()` 是同一个语义位置，必须同时写。
		//
		// 为什么不能省（真机隐患，补验时确认）：warmupRemaining 在纪元为零值时返回 0，即
		// 「做了宽限」变成「完全没有宽限」。而收养实例有两个恰好会踩上去的特征：① ServerPort 为 0
		// （记录里没有它），响应维度只能落 ProbePort；② ProbePort 来自 PID 记录，未部署 ServerProbe
		// 时无人监听。于是探针恒失败 + 无宽限 ⇒ 连续 SuspicionThreshold（默认 3）拍后直接判
		// HealthFaultDead，并在 action=restart 下试图重启一个**刚刚才接管、可能正在正常服务**的服务器
		// （实际被 launchSpecPending 拦住，见 RestartIfRunning）。写纪元不能修好「探针不可达」本身，
		// 它修的是「刚接管就给实例记一笔假死」这个误判：宽限期内的结论必须是「不明」而不是「死」。
		cur.StartedAt = time.Now()
		if restored {
			cur.AutoRestart = false
		}
	} else {
		// StartedAt：同 buildAdoptStartedAt 的语义——收养即向 RUNNING 的一次跳变，必须写宽限期纪元。
		m.instances[instanceUUID] = &Instance{
			UUID:              instanceUUID,
			State:             StateRunning,
			StartedAt:         time.Now(),
			AutoRestart:       !restored,
			WorkDir:           rec.WorkDir,
			ProbePort:         rec.ProbePort,
			strategy:          strategy,
			strategyStale:     true,
			launchSpecPending: true,
			processType:       ProcessTypeDaemon,
		}
	}
	m.mu.Unlock()

	// 登记竞争胜出且双进程归属已确认后，锁外复用既有隔离；失败只告警，不撤销收养。
	m.isolateAdoptedWrapper(instanceUUID, rec)

	if restored {
		// 与启动接管同口径：幂等补发一次禁用帧（wrapper 已置位时无副作用），使 wrapper 与记账对齐。
		m.disarmAutoRestart(instanceUUID, "恢复熔断态（未纳管活进程自动收养）")
	}
	m.auditOrphan("orphan.auto_adopted", instanceUUID,
		fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"workDir":%q,"socket":%q,"policy":"auto_adopt"}`,
			instanceUUID, rec.WrapperPID, rec.JavaPID, rec.WorkDir, rec.SocketAddr),
		true, "")
	slog.Info("已自动收养未纳管的活进程（仅重连，未重启未杀进程）",
		"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID, "workDir", rec.WorkDir)
	return true
}
