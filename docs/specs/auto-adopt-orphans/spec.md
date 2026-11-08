# 未纳管活进程的全自动收养（FR-497③）

> 状态：🔨 开发中　·　关联 PRD：FR-497（第三部分「未纳管活进程自动收养」）　·　**判据对齐**：ADR-093（进程生命周期韧性与孤儿治理）决策 1、`docs/specs/restart-resilience/spec.md` §2.1（处置前置存活复核）　·　关联 ADR：ADR-003（守护进程 Wrapper 模式）、ADR-050 / ADR-079 / ADR-081（重推 / 反向对账 / 反向隧道）　·　关联 FR：FR-455①（处置前置归属复核）、FR-456（运行期周期孤儿扫描）、FR-459（健康巡检与自愈）、FR-471（漂移观测 + 人工接管）

## 1. 背景与目标

托管进程为四层模型 `CP（无进程操作权）→ Worker → wrapper（setsid 游离）→ sh -c → java`。Worker 重启后由 `Manager.RecoverDaemonInstances` 扫 PID 目录、对存活 wrapper 做有界重试拨号并登记为 RUNNING（ADR-003）。

ADR-093 决策 1 规定：**重试耗尽但 wrapper 仍存活**（确属本实例、仅 socket 瞬时不可达）一律**只告警 + 落审计、保留 PID 文件、不杀**——绝不误杀一个健康服务器。这条判据是对的，但它把「本可接管的服务器」留在了**未纳管**状态，且**没有任何后续出口**：

1. 启动接管只有一次机会（`RecoverDaemonInstances`），此后不再尝试；周期孤儿扫描的 `scanWrapperGone` 只抓「wrapper 死 / Java 活」，对「wrapper 与 Java 都活、只是当时拨不通」**完全沉默**。
2. 该实例于是永久脱管：Worker 内存表没有它 → 健康巡检不覆盖、终端与日志采集没有控制连接（stdout/stderr 不再流出）、心跳不上报 → CP 面板 STOPPED / 无该实例，而磁盘上服务器正在服务。这正是 FR-436 事故的形态（「面板 STOPPED 而进程在跑」）。
3. FR-471 的人工接管（`AdoptForeignRuntime`）针对的是**外来**进程（无 PID 记录、非 wrapper），且要先停后启；对本场景既不匹配、也不满足「重启不影响服务器」的硬约束。

**目标**：给 ADR-093 的「只告警」补上非破坏出口——Worker 启动与运行期周期扫描发现 **PID 目录中未纳管的活进程对**（wrapper 与 Java 均活、PID 文件有效）→ **归属复核**通过 → **自动收养为 RUNNING**（重连 wrapper、恢复控制通道，健康巡检 / 日志采集 / 心跳与既有纳管实例同等对待），**不重启也不杀进程**；复核不过 → 只告警（保留 PID 文件），绝不处置。

**范围内**：`internal/worker/process/` 的收养实现 + 周期扫描相位 + 开关；`internal/worker/config.go` 的配置键；本文档。
**不做**：不改四层进程模型与 wrapper 的 PID 文件 / socket 协议，不另建 CP 状态枚举或规格同步体系。当前装配及不确定生命周期结果经既有查询/心跳收敛的边界见 §2.2、§5。

## 2. 设计

### 2.1 判据（与 ADR-093 / restart-resilience §2.1 完全一致）

**收养不是处置**：全程不调用 `Start` / `killTree` / `signalTree`，Java 与 wrapper 的 PID 保持不变。「复核不过 → 只告警不杀」的口径一字不改。

**收养必须用专用复核 `Manager.verifyAdoptOwnership` → `DefaultVerifyAdoptOwnership`（`internal/worker/process/owner_verify.go`），不得复用强杀路径的 `verifyProcessOwnership`**：后者是「多特征任一命中」的宽口径，其中 `cwd == workDir` 可**单独**通过；它服务于强杀路径（放过一个真孤儿只多等一轮），但收养有两个不可忽略的副作用——`isolateAdoptedWrapper` 会把 wrapper 子树迁进实例 cgroup（此后停 cgroup 即连带终止），且实例取得该 PID 的控制权（后续 `Stop` / `Kill` 会向它发信号）。PID 被 OS 复用后，一个 cwd **恰好**落在该实例 work_dir 下的无关进程即可凭 cwd 单特征通过宽口径复核 → 误收养 → 迁进实例 cgroup → 后续被误杀。故收养路径只认**强特征**：

| 复核对象 | 调用 | 通过条件（实现真源：`DefaultVerifyAdoptOwnership`） |
|---|---|---|
| wrapper | `verifyAdoptOwnership(WrapperPID, uuid, workDir, true)` | `/proc/<pid>/cmdline` 形如 `<worker 二进制> daemon` **且** `/proc/<pid>/environ` 携带本实例 UUID（`JM_DAEMON_WRAPPER_CONFIG` JSON 内）。单凭 argv 不判真——argv 对**任何**实例的 wrapper 都相同 |
| Java | `verifyAdoptOwnership(JavaPID, uuid, workDir, false)` | **强特征**：cmdline 含实例 UUID，或 `/proc/<pid>/environ` 携带本实例 UUID（daemon 模式下 Java 由 wrapper 以 `composeEnv(os.Environ(), cfg)` 拉起，继承 `JM_DAEMON_WRAPPER_CONFIG`）。**UUID 必须自成 token**（前后不得紧邻 hex / `-`），按 NUL 切条目取 `=` 后的值比对——见 §5 子串匹配订正 |

- **强特征 / 弱特征的分界**：实例 UUID（出现在 cmdline 或进程环境）是强特征，唯一标识该实例；`cmdline` 含工作目录绝对路径、进程 `cwd` 等于工作目录是**弱特征，单独不足以判真**（环境读到但无本实例 UUID 时一律不判真）。
- **两者都通过**才收养；任一不通过 → `orphan.adopt_blocked` 审计（success=false，detail 带 `evidence` 说明拦截口径，如 `wrapper_argv_mismatch` / `env_uuid_missing` / `weak_evidence_only` / `uuid_evidence_missing`）+ WARN，**不收养、不杀、保留 PID 文件**，下一轮扫描重试。
- 与 ADR-093「wrapper 确证死亡才处置」的关系：本条**不处置任何进程**。`wrapper 死 / Java 活` 不属本条（仍由周期扫描按 `orphan_scan.dispose_policy` 处置）；收养只发生在 **wrapper 存活** 时——正是 ADR-093 决策 1 判定的「不是孤儿、只告警」的那一类，本 FR 把「只告警」升级为「只告警 + 尝试自动接管」。故与「绝不强杀」的权威判据不冲突。
- 平台口径：Unix 经 `/proc` 读 cmdline / environ / cwd；**Windows 无 `/proc`**（environ 读不到）→ 强特征不成立 → 两个分支都 fail-closed（复核不过 → 只告警不收养），与既有强杀路径同一取向：**宁可不收养，绝不误纳管**（见 §5）。


### 2.2 收养动作（非破坏，幂等）

对 PID 目录中每条记录，前置条件全满足才收养：

1. PID 文件可读、记录有效（`WrapperPID > 0`、`JavaPID > 0`、`JavaPID ≠ WrapperPID`）；记录内 `InstanceUUID` 非空时必须与文件名一致（不一致视为陈旧/错配记录，跳过——只 skip 不处置）。
2. wrapper 与 Java **均存活**（`IsPIDAlive`）。任一不活 → 跳过（`wrapper 死 / Java 活` 是周期扫描的处置域，不是收养域）。
3. **未纳管**：内存表中不存在该 UUID，或存在但 `strategy == nil` 且状态不属「可能仍有活进程」三态（`RUNNING`/`STARTING`/`STOPPING`，见 `mayOwnLiveProcess`）。已纳管 → 跳过（**幂等**：不重复拨号、不重复登记、不重复审计）。

收养动作本身（**仅三步，无任何破坏性操作**）：

1. 构造 `daemonStrategy`（`WorkDir` / `ProbePort` 取自 PID 记录，与 `RecoverDaemonInstances` 同源）；
2. **单次** reconnect 拨号 `daemon.Dial(rec.SocketAddr)`；失败 → `orphan.adopt_failed` 审计 + WARN，保留 PID 文件，**下一轮再试**（不在扫描轮内做 127s 递增退避，避免拖住扫描周期；节奏交给 `orphan_scan.interval`）；
3. 成功 → `SetWrapperPID(rec.WrapperPID)` → 登记为 RUNNING：内存表无该 UUID 时新建 `&Instance{State: RUNNING, StartedAt: now, WorkDir, ProbePort, strategy, processType: daemon, AutoRestart: !restored}`；**两条路径都必须写 `StartedAt`**（FR-459 启动宽限期纪元，见 §5 缺陷登记；漏写会让刚接管的实例直接无宽限期）；**已有登记但脱管**（典型：Worker 硬崩后 CP `ResyncInstances` 按 STOPPED 重登记而磁盘进程仍在跑）时**就地补齐运行态**并保留 CP 已下发的规格（`StartCommand` / `StopCommand` / `EnvVars` / JDK / 端口等）——整条覆盖会让后续 `Restart` 拿着空启动命令去拉起新 wrapper。两条路径都复用 FR-459 的熔断态恢复（`restorePersistedCircuit` + `disarmAutoRestart`，与启动接管同口径）；随后落 `orphan.auto_adopted` 审计（success=true）。

登记前在锁内**二次确认未纳管**（拨号期间可能有并发路径抢先，如启动接管或 CP 重推后的人工 Start）：若已有 strategy 或已属「可能仍有活进程」状态，则丢弃本次连接（`strategy.Close()`：只断连接、不下发停止帧、不杀进程），保证 wrapper 的控制连接不被两个 strategy 争抢。

收养登记竞争胜出且归属复核通过后，在共享锁外复用既有 `isolateAdoptedWrapper`，只迁移确认属于本实例的 wrapper/后代；不支持或无权限时仅告警降级，不撤销收养、不杀服。假 cgroupfs 回归不代表真实 systemd/内核隔离验收通过。

重连策略标为 stale，仅承接当前控制通道；下一代通过既有 rebuild 路径读取完整实例规格，不另维护遗漏字段的复制映射。已有规格保留 `StartCommand/StopCommand/EnvVars/JDK/端口`；仅凭 PIDRecord 收养时 `launchSpecPending=true`，完整规格补齐前拒绝会先停再启的 Restart/自愈，保留现有运行时。

`SetLaunchConfig(..., stopCommand ...string)` 兼容旧调用：仅传入可选 StopCommand 时更新停止命令。RUNNING/STARTING/STOPPING 的策略只标 stale、控制连接不中断，停止/崩溃态则锁外关闭旧策略，下一次 Start 按完整规格 rebuild。`ResyncInstances` 对已在表实例复用幂等注册，补齐启动/停止命令、JDK、环境、AutoRestart、停止超时及端口，**不覆盖运行态和工作目录，不启动或重启进程**；未在表实例仍按 STOPPED 登记。它的 skipped 计数表示未新建实例，不表示跳过规格补齐。

生命周期取消与 CP 条件回写的唯一详细契约见 [重启韧性规格 §2.5](../restart-resilience/spec.md#25-生命周期取消与不确定结果收敛rv-016)：Start/Restart/排队 Stop/Kill 传播 RPC ctx，未知结果不能当作崩溃证据，也不能覆盖更新后的状态；不引入新状态枚举或 RPC。

**收养后的控制与观察能力**：

- **健康巡检**：FR-459 巡检按 `m.instances` + `State == RUNNING` 取目标，登记即恢复覆盖；
- **日志采集**：进程输出经既有 `Manager.onOutput` 分流（终端 WS / `StreamInstanceEvents` / 日志采集落盘），且心跳把实例上报为 RUNNING 后，CP 侧按既有绑定链路完成/恢复实例日志绑定；
- **状态与运维**：`GetAllInstanceStates` 心跳上报 RUNNING（CP 面板自行收敛），`Stop` / `Restart` / `Kill` / 终端 / 文件 / 配置等既有操作按 `processType == daemon` 正常生效（stop 走 wrapper 控制帧）。

### 2.3 装配与节奏（与既有 orphan_scan 一致）

- **运行期**：作为 `OrphanScanner.ScanOnce` 的**第 0 相**，每 `orphan_scan.interval`（默认 60s）一轮。放在其它相**之前**：本轮收养的实例立刻进入「受管」集合（`managedInstanceRuntime` 的 PID / 工作目录集合），可抑制 direct 相把刚收养实例的 Java 误判为无主进程（同轮内的一致性问题）。
- **启动**：`OrphanScanner.Start` 启动后**立即跑一轮**再进 ticker。`apps/worker/main.go` 在 `RecoverDaemonInstances()` **之后**启动扫描器；而后者自 2026-11-03 起只同步完成「快速首拨」，首拨失败的记录转**后台**重试（见 `docs/specs/restart-resilience/spec.md` §2.1.1），故启动窗口不再被 ≈127s 的重试拖住。接管重试与扫描并行期间，「不重复拨号」由 `Manager.takeoverPending`（在飞集合）保证：本相跳过在飞实例，避免与之争抢 wrapper 唯一控制连接。
- **开关**：`orphan_scan.auto_adopt`（bool，默认 `true` = 默认启用；`false` **只关收养动作**，即归属复核 + 重连 + 登记一律不执行，但**保留发现与告警**——本相降级为 `Manager.DiscoverUnmanagedDaemonInstances` 的只发现路径，仍按变化去重后 WARN + `orphan.scan_detected` 审计）。护栏五.1 同口径：开关关闭时更不杀进程。`orphan_scan.disabled = true` 仍整体关闭周期扫描（既有键，逃生口）。代码级为 `OrphanScanner.SetAutoAdopt(bool)`（构造默认开）。**开关关闭同时约束处置策略**：本相发现到的未纳管活进程对在 `dispose_policy=auto` 下也降级为 warn、不做处置（见 §2.5）。

### 2.4 与 FR-471 接管的分工

| | 本 FR（自动收养） | FR-471（人工接管） |
|---|---|---|
| 对象 | **平台自有** wrapper 托管的进程对（PID 记录 + 环境 UUID 确证） | 已注册实例目录下的**外来**活进程（无 PID 记录、非 wrapper，如 tmux 拉起） |
| 依据 | PID 文件 + 进程归属复核 + 拨号成功 | 扫描观测缓存 + 人工在面板点「接管」 |
| 动作 | **只重连、不碰进程** | 先 SIGTERM 停外来进程、再以受管方式拉起 |
| 出口 | 无人工介入（用户拍板「全自动」，安全闸=归属复核） | 人工确认 |

两者互补：外来进程永不会被本 FR 收养（无 PID 记录即无候选）。

### 2.5 开关关闭时的处置降级（组合风险修复）

**裁定：开关关闭 ≠ 允许处置。** `auto_adopt: false` 的意图是「这些进程我来管」，不是「放弃它们的所有权保护」。

**缺陷形态（修正前）**：`auto_adopt=false` 使未纳管进程对**不被收养**（不进入内存表 → 不在 `managedInstanceRuntime` 的受管 PID / 工作目录集合中），而周期扫描第二相 `scanDirectOrphans` 的判据 `directOrphanWorkDir`（cwd 落在 `serversDir` 下，或 cmdline 引用 `serversDir`）**不排除 wrapper 守护进程**，于是这一对（wrapper 与 Java 的 cwd 都是实例工作目录）会被判为 **direct 孤儿**，在 `dispose_policy=auto` 下进入 `handleDirectOrphan` → `killTree` 整棵进程树。仅凭「配置组合」即可触发，无需任何异常输入。

**判据**：命中「第 0 相本轮发现到的未纳管活进程对」即降级。匹配口径与 `takeoverProtects` 一致（PID 或工作目录任一命中）：

- PID 口径：进程 PID ∈ {`WrapperPID`, `JavaPID`}。这一条不可省——真实 wrapper 由 Worker 以 `exec.Command(exe, "daemon")` 拉起且**不设 `cmd.Dir`**（见 §5），其 cwd 常不等于实例 workDir，只按目录匹配会漏保护；
- 工作目录口径：进程工作目录 == 记录的 `WorkDir`，覆盖该进程对派生出的子进程（如 `sh -c` 中间层）。

保护快照**每轮重建**：仅 `auto_adopt=false` 的轮次填充，`auto_adopt=true` 的轮次显式清空（含降级留痕去重表），保证开关运行期翻转时不残留保护而放过真孤儿。

**降级动作**：不进入 `handleDirectOrphan`，即**不 `killTree`、不落「已处置」审计**，只落一条 WARN + 一条审计（形态见 §6）。仍以 `OrphanKindDirect` finding 回报（`Disposed=false`，带实例 UUID 与降级说明），使「报出了 direct 孤儿却没处置」在扫描结果里同样可解释。

**边界（硬约束）**：

1. 只降级「**未纳管 + 活**」这一类。已纳管实例（含脱管后按 STOPPED 重登记、目录下有残留 Java 的场景）照旧走 `dispose_policy`，行为完全不变；受管 RUNNING 实例的进程照旧跳过。
2. 只在 `dispose_policy=auto` 生效。`warn` 档本就只告警不处置，不新增降级留痕（避免让运维误以为开关在 warn 档生效）。
3. `auto_adopt=true` 时行为完全不变：该收养的收养（进程对进入受管集合，同轮不再被判为 direct 孤儿），该处置的照旧处置。
4. 护栏五.1 不变：降级路径不调用任何杀/终止动作；既有归属复核、在飞接管让路等校验一条不放宽。
5. 降级必须留痕，否则即「处置策略静默失效」——与护栏五.1「不静默」同源。

**不做**：不改变 `wrapper 死 / Java 活` 这一类（`scanWrapperGone` 的处置域，且该形态不可收养，开关语义不覆盖它）；不新增配置键，不改变 `dispose_policy` 的既有取值语义。

**已知残差（如实登记）**：`wrapper 活 / Java 已死` 的中间态（如游戏服刚崩、wrapper 尚未重启）**不属**本条的「活进程对」——发现判据要求两者均存活（§2.2 前置条件 2），故此形态仍按 `dispose_policy` 处置，开关关闭不保护它。刻意不扩大：① 该形态不构成收养候选，把「不构成候选」的对象一并保护会让保护集与发现集脱钩，判据失去单一真源；② 本条的裁定范围是「本该被收养、却因开关关闭而留在未纳管状态」的那一类，该中间态的 wrapper 属另一处置域（与 `wrapper 死 / Java 活` 同侧的边界），需要时按同一手法单独立项。运维若需绝对保护，应配 `dispose_policy=warn`（既有逃生口）。

## 3. 任务拆分

- [x] T1 `adopt_orphan.go`：`Manager.AdoptUnmanagedDaemonInstances`（前置条件 + 归属复核 + 单次拨号 + 登记 + 审计）
- [x] T2 `orphan_scan.go`：`OrphanScanner` 第 0 相接入 + `SetAutoAdopt` + `Start` 立即跑一轮
- [x] T3 `internal/worker/config.go`：`orphan_scan.auto_adopt` 键（默认 true）
- [x] T4 回归四条（收养非破坏 / 复核不过只告警 / 重复扫描幂等 / 关闭开关不收养），逐条实测转红后还原
- [x] T5 `go build ./...`、`CGO_ENABLED=0 go build ./apps/worker`、`gofmt -l`、`go vet`、`go test` 全过
- [ ] T6 文档同步：本文档 §6 常量/审计登记（PRD 状态与 ADR 由 FR-497 主线统一更新，本次不动）
- [ ] T7 真机验收（需用户确认）：A/B/C/D 四臂已过；**C 臂的 Java 分支负例已补齐（实现层，见 §4 验收 2c~2e）**；
      收养后的下游链路（心跳 / 巡检 / Stop）已由真实 java 进程夹具验证（§4 验收 6a~6d），**但真机仍未跑**，见 §4 真机清单
- [x] T8 组合风险修复（§2.5）：`auto_adopt=false` 时未纳管活进程对在 `dispose_policy=auto` 下降级为 warn（不处置 + WARN + 降级审计）；单测六条（4b~4f）逐条实测转红后还原，`go build ./...`、`go test ./internal/worker/process/`、`go test -race` 全过

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | PID 目录中「wrapper 与 Java 均活、未纳管」的实例被自动收养为 RUNNING，且 **PID 不变、无任何杀/重启动作** | 单测（注入桩）+ 真机 |
| 2 | 归属复核不通过时**只告警**（`orphan.adopt_blocked`），不收养、不杀、保留 PID 文件 | 单测（负例：wrapper 分支 / Java 分支各一） |
| 2a | **弱特征不判真**：`cwd` 恰好等于 `workDir`（或 cmdline 含工作目录）但无实例 UUID 强特征的无关进程，即便「wrapper 与 Java 均活、PID 记录有效」也**拒绝收养**：不拨号、不纳管、不杀、保留 PID 文件，审计 `evidence` 标注拦截口径（`weak_evidence_only` / `wrapper_argv_mismatch`） | 单测（真实进程：目录内两个无关 `sleep`，端到端经 `ScanOnce`） |
| 2b | **真实形态仍可收养**：wrapper 为 `argv` 形如 `<worker> daemon` **且** 环境携带本实例 UUID、Java 的 cwd 为 workDir **且** 继承 `JM_DAEMON_WRAPPER_CONFIG` → 收养成功、PID 不变、零杀；单凭 argv（环境无 UUID）不判真 | 单测（真实进程，不注入复核桩） |
| 2c | **Java 分支负例（端到端）**：wrapper 分支**真实通过**（argv 形如 daemon + 环境带本实例 UUID），Java 只有弱特征（cwd == workDir、环境无本实例 UUID）→ Java 分支拒 `weak_evidence_only`；`orphan.adopt_blocked` 的 detail 带 `role=java` 与被拒 PID，零拨号、零杀、PID 文件保留 | 单测（真实进程 + 真实 `DefaultVerifyAdoptOwnership`） |
| 2d | **Java 携带他实例 UUID**：环境里是本实例之外的另一 UUID → 不判属。实测口径：cwd == workDir ⇒ `weak_evidence_only`；cwd 无关 ⇒ `uuid_evidence_missing`（两种形态各一条） | 单测（真实进程） |
| 2e | **子串污染不得判属**：环境值里出现本实例 UUID 这段字符但其前后紧邻 UUID 字符集（hex / `-`）——如他实例 UUID 以本实例 UUID 为前缀、或 `MONITOR_TARGETS=sandbox-<uuid>-replica` —— Java 与 wrapper 两个分支都不得判通过；完整 token（JSON 值、路径分隔包裹）仍须判通过 | 单测（真实进程；改回整块 `strings.Contains` 即转红） |
| 3 | 重复扫描**幂等**：已纳管不重复拨号、不重复登记、不重复审计；收养前后 PID 不变 | 单测 |
| 4 | 关闭开关（`orphan_scan.auto_adopt=false` / `SetAutoAdopt(false)`）后不自动收养，但**仍发现并告警**（WARN + `orphan.scan_detected`，`Adopted=false`、不拨号、不登记、不杀），且同一进程对按变化去重不重复刷审计 | 单测 |
| 4a | 关闭开关且**无**未纳管活进程时不产生噪音日志与审计 | 单测 |
| 4b | **处置降级**（§2.5）：`dispose_policy=auto` + `auto_adopt=false` + 未纳管活进程对 → **不杀**（含 `killTree` 整树、不落 `orphan.scan_disposed`），并留 WARN + 降级审计（`orphan.scan_dispose_blocked`，`reason=auto_adopt_disabled`、`effectivePolicy=warn`、success=false）；同一进程对按变化去重只留一条，PID 变化才算新事件 | 单测 |
| 4c | 降级判据含 PID 口径：wrapper 的 cwd 不等于实例 workDir（真实形态）时仍受保护 | 单测 |
| 4d | `auto_adopt=true` 时行为完全不变：该收养的收养（不判为 direct 孤儿、零杀）、该处置的照旧 `killTree`，且无任何降级留痕 | 单测 |
| 4e | 已纳管实例不受降级影响：受管 RUNNING 进程跳过；脱管后按 STOPPED 重登记、目录下有残留 Java 的实例仍按 `dispose_policy` 处置 | 单测 |
| 4f | `dispose_policy=warn` 行为与审计形态不变（不产生降级留痕） | 单测 |
| 5 | 收养失败（socket 仍拨不通）不产生任何破坏：只告警 + 保留 PID 文件，下一轮重试 | 单测（负例） |
| 6 | 收养后实例与既有纳管实例同等：心跳快照报 RUNNING、健康巡检覆盖、`Stop` 等既有操作可用 | 单测（快照断言）+ **真实 java 进程夹具** + 真机 |
| 6a | 收养后 `GetAllInstanceStates()` 报 RUNNING，PID 为夹具真实 wrapper PID（与该 PID 记录一致、且真存活） | 测（真实 wrapper 子进程 + 真实 java） |
| 6b | 收养后健康巡检**确实检查到该实例**（`ScanOnce` 报告含该 UUID，未被 `state != RUNNING` 跳过），探针可达时判 healthy | 测（真实 java 监听 `/metrics`） |
| 6c | 探针不可达（记录声明 ProbePort 但无人监听）⇒ 连续 `SuspicionThreshold` 拍后 `HealthFaultDead`（默认 warn 不重启）；**收养必须写 `StartedAt`**，否则刚接管就判假死（缺陷回归） | 测（真实 java，不监听端口） |
| 6d | `StopContext` 后 java 真退出、实例记 STOPPED、stale 策略丢弃、PID 文件清理；**停止走的是 wrapper 控制帧**（java 日志出现 `console command: stop` 与完整关服序列，且无「优雅停止超时，强制终止 Java」）；无关进程（另一实例的 wrapper/java、一个无关 `sleep`）不被波及 | 测（真实 wrapper 控制帧 + 真实 java stdin） |
| 7 | 启动窗口（`Start` 后立即一轮）与运行期周期（ticker）两条入口都生效 | 单测 + 真机 |

**真机清单（须用户确认，逐项确认「服务器不受影响、PID 不变、无孤儿遗留」）**：

| 场景 | 操作 | 期望 |
|---|---|---|
| A 启动窗口收养 | 在跑 daemon 实例上停 Worker（保留 wrapper），改坏/删 socket 使首轮接管拨不通 → 恢复 socket → 重启 Worker | 首轮接管按 ADR-093 只告警；扫描器第一时间（或下一轮 ≤60s）自动收养为 RUNNING，**PID 不变**、服务器不断线 |
| B 运行期收养 | Worker 运行中构造「PID 文件有效 + wrapper/Java 均活 + 未纳管」（测试节点上等价于删掉内存表登记的场景） | 一轮扫描内自动收养为 RUNNING，面板由 STOPPED 收敛为 RUNNING |
| C 复核不过 | 构造 PID 文件指向无关进程（PID 复用形态） | 只告警 + `orphan.adopt_blocked` 审计，**不杀任何进程** |
| D 开关 | 配 `orphan_scan.auto_adopt: false` 重启 Worker | 不收养（仍只告警），实例不受影响 |

**已覆盖**：A / B / D 三臂成立（启动窗口收养、运行期收养、开关关闭不收养）；C 臂达成「拒绝收养且不杀任何进程」，
原先只在 **wrapper 分支**返回，**Java 分支负例已按验收 2c~2e 补齐**（真实进程 + 真实复核，不注入桩）。

**测试夹具（新增，随仓库提交）**：
- `internal/worker/process/testdata/mcstub/MinecraftServerStub.java`——最小游戏服夹具（只用 JDK API、
  Java 8 兼容、不依赖 MC 服务端 jar）：周期日志、读 stdin 的 `stop` 走优雅关服、SIGTERM hook、
  可选 `/metrics` 监听、可选日志文件落盘。无 java/javac 的环境一律 `t.Skip`，不影响 CI。
- `internal/worker/process/adopt_realwrapper_linux_test.go`——把**测试二进制复制成名为 `worker` 的文件**
  并以 `worker daemon` 的 argv 独立 spawn，让 wrapper 的 `/proc/cmdline` 与真实形态一致；
  这是「wrapper 分支真实通过」与「Stop 走真实控制帧」两条断言的前提（进程内 wrapper 做不到）。

**残留（如实登记）**：① 收养后的三条下游链路（心跳报 RUNNING / 健康巡检覆盖 / `Stop` 走 wrapper
控制帧）已由**真实 java 进程夹具**验证：`internal/worker/process/testdata/mcstub/`
（`TestFR497_Adopted*` 族，见 §4 验收 6a~6d）；**但真机（生产 Worker + 真 MC 服务端）仍未跑**，故本条仍属
「实现层已验、真机未跑」而不是已验收；② Windows 未实机验证（见 §5，收养在该平台不生效、仅告警）。

## 5. 风险 / 待定

- **Windows 未实机验证（已知降级）**：Windows 无 `/proc`，`processEnvMatchesInstance` 读不到 wrapper 环境 → wrapper 分支复核 fail-closed → **收养在 Windows 上不会生效**（只告警）。这是刻意取向（宁可不收养，绝不误纳管），但意味着该平台仍是原来的「只告警」行为。彻底解决需 Windows 侧读进程环境（PEB / `NtQueryInformationProcess`）或改 wrapper 启动参数携带 UUID——属另一 FR，且会触碰 wrapper 协议（本 FR 不做）。**未在 Windows 实机验证。**
  代码层已落地、但**只在 Linux 上编译校验过**的部分：`adopt_ownership_windows_test.go`（`//go:build windows`）
  断言「环境读不到 ⇒ 两分支都 fail-closed」，`adopt_ownership_platform_test.go`（无构建约束）断言
  UUID token 边界对 Windows 路径形态（反斜杠、盘符）不误判；`GOOS=windows go build/vet ./internal/worker/process/` 通过。
  **仍需真机验**（本 FR 未做，如实登记）：`IsPIDAlive` 的 `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)`
  语义（权限不足时会误判「不在」）、`taskkill /T /F` 的终止范围与异步窗口、PID 复用判据（本项目只用
  PID 数字，不校验进程创建时刻）、以及 `samePath` 在 Windows 上**大小写不敏感**（当前实现是字符串比较，
  `C:\A` ≠ `c:\a`；只影响「cwd 命中」这类弱特征与强杀路径的宽口径，方向偏保守）。
- **装配已接线**：`apps/worker/main.go` 将 `cfg.OrphanScan.AutoAdoptEnabled()` 传给 `SetAutoAdopt`；`auto_adopt: false` 仅关闭收养，`disabled=true` 仍整体关闭周期扫描。配置关闭与真实服务器持续运行的实机验收仍须单独记录。
- **单轮单次拨号**：socket 瞬时抖动时本次不收养，等下一轮（≤`orphan_scan.interval`，默认 60s）。取舍：扫描轮内做长退避会拖住整轮（含 direct/docker 相），且启动接管已承担长退避。
- **复核口径的误判率**（与 restart-resilience §5 同源）：过宽会放过非本实例记录、过窄会永远不收养。本 FR 的收养路径**只认强特征**（实例 UUID），弱特征（cwd / cmdline 含工作目录）不判真——取向是「宁可这一轮不收养，绝不误纳管一个无关进程」，代价是 `/proc` 环境不可读或被启动命令 `env -i` 清空时该实例不会被收养（仍告警）。强杀路径（`verifyProcessOwnership`）的宽口径**不受本 FR 影响**，仍按 restart-resilience §5 的标定计划推进。
- **wrapper cwd 不是归属证据**：真实 wrapper 由 Worker 以 `exec.Command(exe, "daemon")` 拉起、**不设置 `cmd.Dir`**（继承 Worker 的 cwd），故其 cwd 通常不等于实例 workDir；wrapper 分支只能靠「argv 形如 daemon + 环境 UUID」判真。任何「按 cwd 判断 wrapper 归属」的实现都既会误判真、也会漏判真。
- **收养与人工 Stop 的竞态**：PID 文件由 wrapper 退出时清理，正常情况下「刚被 Stop 的实例」不会留下有效 PID 记录；但若清理失败又恰逢 Java 未退净，可能被收养回 RUNNING。已由「wrapper 与 Java 均活」+ 归属复核双重收敛；极端场景待真机观察。
- **收养必须写 `StartedAt`（本次补验修复的缺陷）**：两条登记路径原都未写 `Instance.StartedAt`，而
  `warmupRemaining` 在纪元为零值时返回 0 ——「做了宽限」变成「完全没有宽限」。与另一条既有形态
  （PID 记录只有 `ProbePort`、没有 `ServerPort`；未部署 ServerProbe 时该端口无人监听 ⇒ 响应维度恒失败）
  叠加，一个**刚被接管、可能正在正常服务**的服务器会在 `SuspicionThreshold` 拍（默认 3 × 30s ≈ 90s）
  后被判 `HealthFaultDead`。已修（收养 + 启动接管两处都写纪元），回归见测试
  `TestFR497_AdoptedHealthScanHasStartupWarmup`（删掉该赋值即转红）。
  **仍未修**：`ServerPort` 缺失导致的「响应维度只能落 ProbePort」本身没变——它是 PID 记录格式的
  历史约束（记录里没有 server-port 字段），补它要改 wrapper→PID 文件协议，属另一 FR。当前取向是
  宽限期把误判推后（给 CP 补齐规格的时间），而不是消除误判源。
- **归属判据的子串匹配已收窄（本次补验修复的缺陷）**：`processEnvMatchesInstance` 原对「NUL 换空格后的
  整块环境」做 `strings.Contains`，于是任何一处**出现过**本实例 UUID 这段字符的值都被判成「我就是该实例」
  （他实例 UUID 以本实例 UUID 为前缀、`MONITOR_TARGETS=sandbox-<uuid>-replica` 等）。现改为按 NUL 切条目、
  取 `=` 后的值、并要求 UUID 在值里**自成 token**（前后不得紧邻 hex / `-`）。该谓词同时服务
  `DefaultVerifyProcessOwnership` 的 wrapper 分支，故强杀路径的 wrapper 归属判定一并收紧——方向与
  ADR-093 一致（收窄只会「少杀」）。回归见 `TestFR497_JavaBranchSubstringPollutionRejected` 与
  `TestFR497_WrapperBranchSubstringPollutionRejected`。
- **审计白名单已登记**：CP `ReportOrphanAudit` 的强白名单已含 `orphan.auto_adopted/adopt_blocked/adopt_failed`，proto action 注释亦已同步；未知 action 仍拒绝，不因收养放宽审计输入边界。该接线不替代远端审计落库与归属场景实机验收。

## 6. 关键常量 / 配置键 / 审计 action 登记（实现-文档对齐）

**配置键**（`internal/worker/config.go`，`worker.yml` 下 `orphan_scan.*`）：

| 键 | 默认 | 语义 |
|---|---|---|
| `orphan_scan.auto_adopt` | `true` | 未纳管活进程全自动收养（FR-497③）。`false` 只关收养（复核+重连+登记），**发现与告警保留**：仍 WARN + 落 `orphan.scan_detected`（`reason=auto_adopt_disabled`），按变化去重；**并同时关闭本相发现到的进程对的处置**（`dispose_policy=auto` 下降级为 warn，见 §2.5）。默认口径的单一真源为进程包 `DefaultOrphanAutoAdopt`。**装配已接线**（见 §5）。 |
| `orphan_scan.disabled` | `false` | 既有键：`true` 整体关闭周期扫描（含收养）。 |
| `orphan_scan.interval` | `60s` | 既有键：扫描周期；收养失败的重试节奏即此周期。 |

**关键常量**：本 FR 未引入新的量值常量（无退避、无上限）；复用 `daemon.IsPIDAlive`、`daemon.Dial`、`Manager.verifyAdoptOwnership`（收养专用强特征复核）、`maxScannedProcesses` 等既有基座。收养轮内**不睡眠、不重试**（单次拨号）是刻意的成本约束。

**审计 action 名**（`Manager.auditOrphan` → `ReportOrphanAudit`，targetType=`orphan`；targetID=实例 UUID）：

| action | 触发 | success |
|---|---|---|
| `orphan.auto_adopted` | 归属复核通过且拨号成功，实例被自动收养为 RUNNING | true |
| `orphan.adopt_blocked` | 归属复核不通过（wrapper 或 Java 分支），只告警不收养。detail 的 `evidence` 字段记录拦截口径：`wrapper_argv_mismatch`（argv 非 daemon）/ `env_uuid_missing`（argv 形如 daemon 但环境无本实例 UUID，或按 Java 分支查他人实例）/ `weak_evidence_only`（只有 cwd 或 cmdline 含工作目录这类弱特征）/ `uuid_evidence_missing`（毫无特征）/ `process_unreadable` / `cmdline_unreadable`（进程已不在或无权限） | false |
| `orphan.adopt_failed` | 归属复核通过但 reconnect 拨号失败（socket 仍不可达），保留 PID 文件待下轮 | false |
| `orphan.scan_detected` | `auto_adopt=false` 时发现了未纳管活进程（`kind=unmanaged_live_runtime`、`reason=auto_adopt_disabled`）：只告警不接管；同一进程对按变化去重，只落一条 | true |
| `orphan.scan_dispose_blocked` | `auto_adopt=false` 时该进程对在 `dispose_policy=auto` 下本会作为 direct 孤儿处置，因开关关闭**降级为 warn、不做处置**（§2.5）。detail：`kind=direct_orphan`、`policy="auto"`（配置值）、`effectivePolicy="warn"`（实际生效）、`reason="auto_adopt_disabled"`、`wrapperPid` / `javaPid` / `workDir`；同一进程对按变化去重，只落一条 | false |

> **落库前置**：以上 action 已登记到 CP 强白名单与 proto action 注释；审计仍须经已认证 Worker 通道及既有范围校验，未知 action 不放行，见 §5。`orphan.scan_detected` 为既有 action（三态孤儿的 warn 档同用），开关关闭路径复用同一枚举值，不新增审计输入面；降级留痕复用既有 `orphan.scan_dispose_blocked`（「处置被拦截」语义），以 detail 的 `reason` 与「归属复核不过」（`reason=ownership_unverified`）、「启动接管在飞」（`reason=startup_takeover_inflight`）区分，同样不新增审计输入面。

**新增代码入口**：`Manager.AdoptUnmanagedDaemonInstances`（一轮收养）、`Manager.DiscoverUnmanagedDaemonInstances`（只发现不处置，供 `auto_adopt=false` 用）、`Manager.discoverUnmanagedLiveRecords`（两路共用的发现判据）、`OrphanScanner.SetAutoAdopt`、`OrphanScanner.Start`（立即一轮）、`OrphanScanner.ScanOnce`（第 0 相）、`OrphanScanner.warnUnmanagedNotAdopted`（关闭开关时的去重告警）；新增 `OrphanKindUnmanagedLiveRuntime` 与 `OrphanFinding.Adopted`。§2.5 降级链：`OrphanScanner.setUnmanagedLiveGuard` / `resetUnmanagedLiveGuard`（保护快照每轮重建）、`OrphanScanner.unmanagedLiveGuardHit`（PID / 工作目录双口径命中）、`OrphanScanner.scanDirectOrphans`（命中即跳过 `handleDirectOrphan`）、`OrphanScanner.warnDowngradedDirectOrphans` + `newlyDowngraded`（去重留痕）。

### 5.1 收紧不破坏现有收养：生产环境值的等价性验证（2026-10-27）

§5 已记「子串匹配已收窄」，但那里只有单测形状的推理。收紧判据的最大风险不是「旧缺陷修不好」，而是**把本来能用的收养也拦掉**——那样生产上从「可能误杀」变成「完全收养不了」，后果更直接。因此另做了两项实证：

**① 生产 wrapper 的真实环境值**（`/proc/<wrapper pid>/environ`，原样摘）：

```
JM_DAEMON_WRAPPER_CONFIG={"instance_uuid":"17b4cbb9-d3f0-48e9-b943-31dd0d869676","start_command":"java -Xms1024M -Xmx1024M -jar paper-1.20.1-196.jar nogui","stop_command":"stop","work_dir":"<实例工作目录>",...}
```

（`work_dir` 原值为本机绝对路径，已脱敏；其余字段为原样摘录。）

UUID 两侧是 `"`（JSON 引号），不属 UUID 字符集 ⇒ **命中**，现有收养路径不受影响。

**② 复刻实现逻辑的等价性表**（7 个形态）：

| 形态 | 期望 | 实测 |
|---|---|---|
| 生产真实环境值（JSON 内 UUID 被引号包裹） | 通过 | ✅ 通过 |
| 他实例 UUID = 本实例 UUID + `-aa` 后缀 | 拒绝 | ✅ 拒绝 |
| 前缀污染 `aa<uuid>` | 拒绝 | ✅ 拒绝 |
| 路径包裹 `/srv/<uuid>/server.jar` | 通过 | ✅ 通过 |
| `MONITOR_TARGETS=sandbox-<uuid>-replica` | 拒绝 | ✅ 拒绝 |
| 纯 UUID 值（整条值就是 UUID） | 通过 | ✅ 通过 |
| 无关内容 | 拒绝 | ✅ 拒绝 |

⇒ **生产形态不变（仍收养），而三种污染形态全部被拦**。这是「收紧不破坏现有能力」的直接证据。

**注意这条证据的边界**：它验的是**判据函数**在真实环境值上的行为，不是「生产上真的收养成功」——后者需要一次真实的未纳管进程对才能验，本轮未做。两者不能互相替代。

### 5.2 收养后下游链路的真机补验（2026-10-27）

原先「收养 → 心跳 RUNNING / 健康巡检 / Stop」只有单测，且用替身进程（无 stdin 协议、无 SIGTERM 处理、无日志输出）代替真实服务端。本次补上真夹具：

- 夹具：`internal/worker/process/testdata/mcstub/MinecraftServerStub.java`，**只用 JDK 自带 API**（不依赖任何 Minecraft 服务端 jar）：能按行写日志（供采集侧观察）、处理 SIGTERM 优雅退出、能被 stdin 的 `stop` 命令终止。
- 覆盖：收养后 `GetAllInstanceStates` 报 RUNNING 且 PID 为夹具真实 PID；健康巡检真的检查到该实例（`state==StateRunning` 不被跳过）且区分探针可达/不可达两形态；`StopContext` 后 java 进程真的退出且不误杀无关进程。

**顺带修掉的真缺陷**：收养实例无启动宽限期——`StartedAt` 零值使 `warmupRemaining` 返回 0，健康巡检第一拍就开始判死，而收养正是最需要宽限的场景（刚接管的实例探测链路未必立刻通）。两条登记路径（新建 / 就地补齐）都补写纪元，回归 `TestFR497_AdoptedHealthScanHasStartupWarmup`（删掉该赋值即转红）。

**仍未验的（不得外推）**：

| 项 | 为什么没做 |
|---|---|
| Windows 真机 | 本机是 Linux。该平台无 `/proc`（environ 读不到 ⇒ 两个分支都 fail-closed）、`OpenProcess`/`taskkill` 行为、`samePath` 大小写敏感性均未实机验证。仅到 `GOOS=windows go build/vet` 通过 |
| 真实 MC 服务端（Paper/Bungee）| 夹具是自写的 stub。它证明的是「控制链路与进程生命周期正确」，不是「真 Paper 启动行为」——两者边界不同 |
| 真实 `systemctl restart` 下的收养 | FR-500 已在本项外覆盖（三臂实验）；两者的交叉（重启后收养 + 巡检 + Stop 一次跑通）本轮未做 |
