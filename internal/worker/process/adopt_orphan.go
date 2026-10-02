package process

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-497③：未纳管活进程的全自动收养。
//
// 背景（ADR-093 决策 1 的缺口）：Worker 重启时 RecoverDaemonInstances 对「wrapper 仍存活、仅 socket
// 瞬时不可达」的实例只告警 + 保留 PID 文件、不杀（这是对的——绝不误杀健康服务器）；但该实例此后
// **没有任何出口**：启动接管只有一次机会，周期扫描的 scanWrapperGone 只抓「wrapper 死 / Java 活」，
// 对「wrapper 与 Java 都活、只是当时拨不通」完全沉默。于是它永久脱管——健康巡检不覆盖、终端与日志
// 采集没有控制连接、心跳不上报，CP 面板 STOPPED 而磁盘上服务器正在服务（FR-436 形态）。
//
// 本文件补上这个出口：发现 PID 目录中「未纳管的活进程对」（wrapper 与 Java 均活、PID 文件有效）
// → 归属复核通过 → **只重连、登记为 RUNNING**；不重启、不杀、不碰进程（PID 不变）。
// 判据与 ADR-093 / docs/specs/restart-resilience/spec.md §2.1 完全一致：复核不通过一律只告警。
//
// 与 FR-471（AdoptForeignRuntime）的分工：那条路径处置的是**外来**进程（无 PID 记录、非 wrapper，
// 人工确认后先停后启）；本路径只接管**平台自有 wrapper**（PID 记录 + 环境 UUID 确证），全程不碰进程。

// DefaultOrphanAutoAdopt 未纳管活进程自动收养的默认开关（FR-497③，默认启用）。
// 单一真源：worker 配置层（internal/worker/config.go 的 orphan_scan.auto_adopt）引用本常量。
const DefaultOrphanAutoAdopt = true

// AdoptUnmanagedDaemonInstances 执行一轮「未纳管活进程」自动收养（FR-497③）。返回成功收养数与观测结果。
//
// 逐条 PID 记录的判定顺序（任一前置条件不满足即跳过，绝不处置）：
//  1. 记录有效：WrapperPID / JavaPID 均 >0 且互不相同；记录内 InstanceUUID 非空时必须与文件名一致；
//  2. wrapper 与 Java **均存活**（任一不活即跳过：wrapper 死/Java 活属周期扫描的处置域，不是收养域）；
//  3. 未纳管（内存表无该 UUID，或有但无 strategy 且状态不属「可能仍有活进程」三态）——已纳管则跳过（幂等）；
//  4. 归属复核通过（wrapper 分支带环境 UUID 确证，Java 分支按 cmdline/cwd 匹配；复核不过只告警不收养）。
//
// 收养动作 = 构造 daemon 策略 + **单次** reconnect 拨号 + 登记 RUNNING（复用 FR-459 熔断态恢复）。
// 拨号失败只告警并保留 PID 文件，交由下一轮扫描重试（不在扫描轮内做长退避，避免拖住整轮扫描）。
func (m *Manager) AdoptUnmanagedDaemonInstances() (int, []OrphanFinding) {
	if m == nil || m.pidDir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(m.pidDir)
	if err != nil {
		// PID 目录不存在（尚未产生任何 daemon 实例）属正常，不告警。
		if !os.IsNotExist(err) {
			slog.Warn("未纳管活进程收养：读取 PID 目录失败，本轮跳过", "dir", m.pidDir, "error", err)
		}
		return 0, nil
	}

	adopted := 0
	findings := make([]OrphanFinding, 0)
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
		ok := m.adoptUnmanagedRecord(instanceUUID, rec)
		finding := OrphanFinding{
			Kind:         OrphanKindUnmanagedLiveRuntime,
			InstanceUUID: instanceUUID,
			WorkDir:      rec.WorkDir,
			PIDs:         []int{rec.WrapperPID, rec.JavaPID},
			Adopted:      ok,
			Detail: fmt.Sprintf("PID 目录中未纳管的活进程对：wrapper(pid=%d) 与 Java(pid=%d) 均存活",
				rec.WrapperPID, rec.JavaPID),
		}
		if ok {
			adopted++
		}
		findings = append(findings, finding)
	}
	return adopted, findings
}

// adoptableRecord 校验 PID 记录本身是否具备「有效」形态（FR-497③）：
// wrapper/java PID 齐备且互异；记录内 UUID 非空时必须与 PID 文件名一致（不符即陈旧/错配，跳过）。
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

// instanceAdoptedOrLive 报告该实例是否已被本 Worker 纳管（FR-497③ 幂等判据）。
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
// 安全闸（与 FR-455① / ADR-093 同口径）：配送任何动作之前先做归属复核——wrapper 分支必须确证
// （argv 形如 `<worker> daemon` **且** /proc/<pid>/environ 携带本实例 UUID），Java 分支按 cmdline/cwd
// 匹配工作目录。任一不通过即放弃收养（`orphan.adopt_blocked`），**不杀、不重启、保留 PID 文件**——
// 收养是非破坏动作，复核不过的代价只是「这次不接管」，而非「误杀」。
//
// 拨号目标取自 PID 记录的 socket 地址（实例级唯一），拨通即证明该实例的 wrapper 正在服务。
func (m *Manager) adoptUnmanagedRecord(instanceUUID string, rec *daemon.PIDRecord) bool {
	if !m.verifyProcessOwnership(rec.WrapperPID, instanceUUID, rec.WorkDir, true) {
		m.auditOrphan("orphan.adopt_blocked", instanceUUID,
			fmt.Sprintf(`{"instanceUuid":%q,"pid":%d,"role":"wrapper","reason":"ownership_unverified","workDir":%q}`,
				instanceUUID, rec.WrapperPID, rec.WorkDir),
			false, "wrapper 归属复核不通过，拒绝收养（PID 可能已被复用）")
		slog.Warn("未纳管活进程归属复核不通过（wrapper 分支），只告警不收养，保留 PID 文件",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "workDir", rec.WorkDir)
		return false
	}
	if !m.verifyProcessOwnership(rec.JavaPID, instanceUUID, rec.WorkDir, false) {
		m.auditOrphan("orphan.adopt_blocked", instanceUUID,
			fmt.Sprintf(`{"instanceUuid":%q,"pid":%d,"role":"java","reason":"ownership_unverified","workDir":%q}`,
				instanceUUID, rec.JavaPID, rec.WorkDir),
			false, "Java 归属复核不通过，拒绝收养（PID 可能已被复用）")
		slog.Warn("未纳管活进程归属复核不通过（Java 分支），只告警不收养，保留 PID 文件",
			"instanceId", instanceUUID, "javaPid", rec.JavaPID, "workDir", rec.WorkDir)
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

	// FR-459 终验 Low #3 同口径：若 Worker 重启前该实例已熔断，收养时同样恢复熔断态，
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
		cur.processType = ProcessTypeDaemon
		cur.State = StateRunning
		if restored {
			cur.AutoRestart = false
		}
	} else {
		m.instances[instanceUUID] = &Instance{
			UUID:        instanceUUID,
			State:       StateRunning,
			AutoRestart: !restored,
			WorkDir:     rec.WorkDir,
			ProbePort:   rec.ProbePort,
			strategy:    strategy,
			processType: ProcessTypeDaemon,
		}
	}
	m.mu.Unlock()

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
