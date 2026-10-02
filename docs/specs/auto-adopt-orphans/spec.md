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
**不做**：不改四层进程模型与 wrapper 的 PID 文件 / socket 协议；不改 CP 侧状态收敛（本 FR 只让 Worker 侧如实上报）；不改 `apps/worker/main.go` 的装配（受本次改动边界限制，见 §5）。

## 2. 设计

### 2.1 判据（与 ADR-093 / restart-resilience §2.1 完全一致）

**收养不是处置**：全程不调用 `Start` / `killTree` / `signalTree`，Java 与 wrapper 的 PID 保持不变。因此判据仍完全沿用 FR-455① 的**归属复核**（`Manager.verifyProcessOwnership` → `DefaultVerifyProcessOwnership`，`internal/worker/process/owner_verify.go`），且「复核不过 → 只告警不杀」的口径一字不改：

| 复核对象 | 调用 | 通过条件（既有实现，未改动） |
|---|---|---|
| wrapper | `verifyProcessOwnership(WrapperPID, uuid, workDir, true)` | `/proc/<pid>/cmdline` 形如 `<worker 二进制> daemon` **且** `/proc/<pid>/environ` 携带本实例 UUID（`JM_DAEMON_WRAPPER_CONFIG` JSON 内）。单凭 argv 不判真——argv 对**任何**实例的 wrapper 都相同 |
| Java | `verifyProcessOwnership(JavaPID, uuid, workDir, false)` | cmdline 含实例 UUID 或工作目录绝对路径，或进程 cwd 等于工作目录（daemon 模式下 Java 由 wrapper 以 `cmd.Dir = WorkDir` 启动，cwd 即工作目录） |

- **两者都通过**才收养；任一不通过 → `orphan.adopt_blocked` 审计（success=false）+ WARN，**不收养、不杀、保留 PID 文件**，下一轮扫描重试。
- 与 ADR-093「wrapper 确证死亡才处置」的关系：本条**不处置任何进程**。`wrapper 死 / Java 活` 不属本条（仍由周期扫描按 `orphan_scan.dispose_policy` 处置）；收养只发生在 **wrapper 存活** 时——正是 ADR-093 决策 1 判定的「不是孤儿、只告警」的那一类，本 FR 把「只告警」升级为「只告警 + 尝试自动接管」。故与「绝不强杀」的权威判据不冲突。
- 平台口径：Unix 经 `/proc` 读 cmdline / environ / cwd；**Windows 无 `/proc`**（environ 读不到）→ wrapper 分支 fail-closed（复核不过 → 只告警不收养），与既有强杀路径同一口径：**宁可不收养，绝不误纳管**（见 §5）。

### 2.2 收养动作（非破坏，幂等）

对 PID 目录中每条记录，前置条件全满足才收养：

1. PID 文件可读、记录有效（`WrapperPID > 0`、`JavaPID > 0`、`JavaPID ≠ WrapperPID`）；记录内 `InstanceUUID` 非空时必须与文件名一致（不一致视为陈旧/错配记录，跳过——只 skip 不处置）。
2. wrapper 与 Java **均存活**（`IsPIDAlive`）。任一不活 → 跳过（`wrapper 死 / Java 活` 是周期扫描的处置域，不是收养域）。
3. **未纳管**：内存表中不存在该 UUID，或存在但 `strategy == nil` 且状态不属「可能仍有活进程」三态（`RUNNING`/`STARTING`/`STOPPING`，见 `mayOwnLiveProcess`）。已纳管 → 跳过（**幂等**：不重复拨号、不重复登记、不重复审计）。

收养动作本身（**仅三步，无任何破坏性操作**）：

1. 构造 `daemonStrategy`（`WorkDir` / `ProbePort` 取自 PID 记录，与 `RecoverDaemonInstances` 同源）；
2. **单次** reconnect 拨号 `daemon.Dial(rec.SocketAddr)`；失败 → `orphan.adopt_failed` 审计 + WARN，保留 PID 文件，**下一轮再试**（不在扫描轮内做 127s 递增退避，避免拖住扫描周期；节奏交给 `orphan_scan.interval`）；
3. 成功 → `SetWrapperPID(rec.WrapperPID)` → 登记为 RUNNING：内存表无该 UUID 时新建 `&Instance{State: RUNNING, WorkDir, ProbePort, strategy, processType: daemon, AutoRestart: !restored}`；**已有登记但脱管**（典型：Worker 硬崩后 CP `ResyncInstances` 按 STOPPED 重登记而磁盘进程仍在跑）时**就地补齐运行态**并保留 CP 已下发的规格（`StartCommand` / `StopCommand` / `EnvVars` / JDK / 端口等）——整条覆盖会让后续 `Restart` 拿着空启动命令去拉起新 wrapper。两条路径都复用 FR-459 的熔断态恢复（`restorePersistedCircuit` + `disarmAutoRestart`，与启动接管同口径）；随后落 `orphan.auto_adopted` 审计（success=true）。

登记前在锁内**二次确认未纳管**（拨号期间可能有并发路径抢先，如启动接管或 CP 重推后的人工 Start）：若已有 strategy 或已属「可能仍有活进程」状态，则丢弃本次连接（`strategy.Close()`：只断连接、不下发停止帧、不杀进程），保证 wrapper 的控制连接不被两个 strategy 争抢。

**收养后与既有纳管实例同等**（无需额外接线）：

- **健康巡检**：FR-459 巡检按 `m.instances` + `State == RUNNING` 取目标，登记即恢复覆盖；
- **日志采集**：进程输出经既有 `Manager.onOutput` 分流（终端 WS / `StreamInstanceEvents` / 日志采集落盘），且心跳把实例上报为 RUNNING 后，CP 侧按既有绑定链路完成/恢复实例日志绑定；
- **状态与运维**：`GetAllInstanceStates` 心跳上报 RUNNING（CP 面板自行收敛），`Stop` / `Restart` / `Kill` / 终端 / 文件 / 配置等既有操作按 `processType == daemon` 正常生效（stop 走 wrapper 控制帧）。

### 2.3 装配与节奏（与既有 orphan_scan 一致）

- **运行期**：作为 `OrphanScanner.ScanOnce` 的**第 0 相**，每 `orphan_scan.interval`（默认 60s）一轮。放在其它相**之前**：本轮收养的实例立刻进入「受管」集合（`managedInstanceRuntime` 的 PID / 工作目录集合），可抑制 direct 相把刚收养实例的 Java 误判为无主进程（同轮内的一致性问题）。
- **启动**：`OrphanScanner.Start` 启动后**立即跑一轮**再进 ticker。`apps/worker/main.go` 在 `RecoverDaemonInstances()` **之后**启动扫描器，故「Worker 启动」窗口由同一扫描器覆盖，且不会与启动接管重复拨号。
- **开关**：`orphan_scan.auto_adopt`（bool，默认 `true` = 默认启用；`false` 只关收养、保留其余三态扫描与告警）。`orphan_scan.disabled = true` 仍整体关闭周期扫描（既有键，逃生口）。代码级为 `OrphanScanner.SetAutoAdopt(bool)`（构造默认开）。

### 2.4 与 FR-471 接管的分工

| | 本 FR（自动收养） | FR-471（人工接管） |
|---|---|---|
| 对象 | **平台自有** wrapper 托管的进程对（PID 记录 + 环境 UUID 确证） | 已注册实例目录下的**外来**活进程（无 PID 记录、非 wrapper，如 tmux 拉起） |
| 依据 | PID 文件 + 进程归属复核 + 拨号成功 | 扫描观测缓存 + 人工在面板点「接管」 |
| 动作 | **只重连、不碰进程** | 先 SIGTERM 停外来进程、再以受管方式拉起 |
| 出口 | 无人工介入（用户拍板「全自动」，安全闸=归属复核） | 人工确认 |

两者互补：外来进程永不会被本 FR 收养（无 PID 记录即无候选）。

## 3. 任务拆分

- [x] T1 `adopt_orphan.go`：`Manager.AdoptUnmanagedDaemonInstances`（前置条件 + 归属复核 + 单次拨号 + 登记 + 审计）
- [x] T2 `orphan_scan.go`：`OrphanScanner` 第 0 相接入 + `SetAutoAdopt` + `Start` 立即跑一轮
- [x] T3 `internal/worker/config.go`：`orphan_scan.auto_adopt` 键（默认 true）
- [x] T4 回归四条（收养非破坏 / 复核不过只告警 / 重复扫描幂等 / 关闭开关不收养），逐条实测转红后还原
- [x] T5 `go build ./...`、`CGO_ENABLED=0 go build ./apps/worker`、`gofmt -l`、`go vet`、`go test` 全过
- [ ] T6 文档同步：本文档 §6 常量/审计登记（PRD 状态与 ADR 由 FR-497 主线统一更新，本次不动）
- [ ] T7 真机验收（需用户确认）：见 §4 真机清单

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | PID 目录中「wrapper 与 Java 均活、未纳管」的实例被自动收养为 RUNNING，且 **PID 不变、无任何杀/重启动作** | 单测（注入桩）+ 真机 |
| 2 | 归属复核不通过时**只告警**（`orphan.adopt_blocked`），不收养、不杀、保留 PID 文件 | 单测（负例：wrapper 分支 / Java 分支各一） |
| 3 | 重复扫描**幂等**：已纳管不重复拨号、不重复登记、不重复审计；收养前后 PID 不变 | 单测 |
| 4 | 关闭开关（`orphan_scan.auto_adopt=false` / `SetAutoAdopt(false)`）后不自动收养，其余扫描行为不变 | 单测 |
| 5 | 收养失败（socket 仍拨不通）不产生任何破坏：只告警 + 保留 PID 文件，下一轮重试 | 单测（负例） |
| 6 | 收养后实例与既有纳管实例同等：心跳快照报 RUNNING、健康巡检覆盖、`Stop` 等既有操作可用 | 单测（快照断言）+ 真机 |
| 7 | 启动窗口（`Start` 后立即一轮）与运行期周期（ticker）两条入口都生效 | 单测 + 真机 |

**真机清单（须用户确认，逐项确认「服务器不受影响、PID 不变、无孤儿遗留」）**：

| 场景 | 操作 | 期望 |
|---|---|---|
| A 启动窗口收养 | 在跑 daemon 实例上停 Worker（保留 wrapper），改坏/删 socket 使首轮接管拨不通 → 恢复 socket → 重启 Worker | 首轮接管按 ADR-093 只告警；扫描器第一时间（或下一轮 ≤60s）自动收养为 RUNNING，**PID 不变**、服务器不断线 |
| B 运行期收养 | Worker 运行中构造「PID 文件有效 + wrapper/Java 均活 + 未纳管」（测试节点上等价于删掉内存表登记的场景） | 一轮扫描内自动收养为 RUNNING，面板由 STOPPED 收敛为 RUNNING |
| C 复核不过 | 构造 PID 文件指向无关进程（PID 复用形态） | 只告警 + `orphan.adopt_blocked` 审计，**不杀任何进程** |
| D 开关 | 配 `orphan_scan.auto_adopt: false` 重启 Worker | 不收养（仍只告警），实例不受影响 |

## 5. 风险 / 待定

- **Windows 未实机验证（已知降级）**：Windows 无 `/proc`，`processEnvMatchesInstance` 读不到 wrapper 环境 → wrapper 分支复核 fail-closed → **收养在 Windows 上不会生效**（只告警）。这是刻意取向（宁可不收养，绝不误纳管），但意味着该平台仍是原来的「只告警」行为。彻底解决需 Windows 侧读进程环境（PEB / `NtQueryInformationProcess`）或改 wrapper 启动参数携带 UUID——属另一 FR，且会触碰 wrapper 协议（本 FR 不做）。**未在 Windows 实机验证。**
- **`apps/worker/main.go` 装配未接线**：`orphan_scan.auto_adopt` 键已登记（`internal/worker/config.go`），但键 → 扫描器的装配点在 `apps/worker/main.go`，不在本次改动边界内，暂未接线。因此当前**运行期默认开启**（与目标一致），关闭手段为：整体关闭 `orphan_scan.disabled=true`（已接线）或代码级 `SetAutoAdopt(false)`；`auto_adopt: false` 的 YAML 生效需在装配点补一行（留给 FR-497 主线或后续小改动）。
- **单轮单次拨号**：socket 瞬时抖动时本次不收养，等下一轮（≤`orphan_scan.interval`，默认 60s）。取舍：扫描轮内做长退避会拖住整轮（含 direct/docker 相），且启动接管已承担长退避。
- **复核口径的误判率**（与 restart-resilience §5 同源）：过宽会放过非本实例记录、过窄会永远不收养。本 FR 只把复核用于**收养**（无破坏性），风险面低于强杀路径；匹配口径仍需真机标定。
- **收养与人工 Stop 的竞态**：PID 文件由 wrapper 退出时清理，正常情况下「刚被 Stop 的实例」不会留下有效 PID 记录；但若清理失败又恰逢 Java 未退净，可能被收养回 RUNNING。已由「wrapper 与 Java 均活」+ 归属复核双重收敛；极端场景待真机观察。
- **CP 侧审计白名单尚未补三条（越界项，本次未改）**：CP 的 `ReportOrphanAudit` 对 action 做**强白名单校验**（`internal/controlplane/grpc/orphan_audit.go` 的 `orphanAuditAllowedActions`），不在表内一律 `InvalidArgument` 拒收（审计库是运维追责面，不能让 Worker 任意写）。本 FR 的三个新 action（`orphan.auto_adopted` / `orphan.adopt_blocked` / `orphan.adopt_failed`）**当前不在该白名单**，故收养动作在本节点仍有 WARN 日志（不静默），但**进不了 CP 审计库**（Worker 侧会看到「孤儿审计上报失败，丢弃」）。按该文件自身的纪律（「新增动作必须同时更新本表、spec 与 proto 注释」），需在 CP 侧补三条白名单 + `proto/worker.proto` 的 action 注释；两者均**不在本次改动边界内**（本 FR 只允许改 `internal/worker/process/` 与 `internal/worker/config.go`），由 FR-497 主线或后续小改动补齐。

## 6. 关键常量 / 配置键 / 审计 action 登记（实现-文档对齐）

**配置键**（`internal/worker/config.go`，`worker.yml` 下 `orphan_scan.*`）：

| 键 | 默认 | 语义 |
|---|---|---|
| `orphan_scan.auto_adopt` | `true` | 未纳管活进程全自动收养（FR-497③）。`false` 只关收养，其余扫描与告警不变。默认口径的单一真源为进程包 `DefaultOrphanAutoAdopt`。**装配点待接线**（见 §5）。 |
| `orphan_scan.disabled` | `false` | 既有键：`true` 整体关闭周期扫描（含收养）。 |
| `orphan_scan.interval` | `60s` | 既有键：扫描周期；收养失败的重试节奏即此周期。 |

**关键常量**：本 FR 未引入新的量值常量（无退避、无上限）；复用 `daemon.IsPIDAlive`、`daemon.Dial`、`Manager.verifyProcessOwnership`、`maxScannedProcesses` 等既有基座。收养轮内**不睡眠、不重试**（单次拨号）是刻意的成本约束。

**审计 action 名**（`Manager.auditOrphan` → `ReportOrphanAudit`，targetType=`orphan`；targetID=实例 UUID）：

| action | 触发 | success |
|---|---|---|
| `orphan.auto_adopted` | 归属复核通过且拨号成功，实例被自动收养为 RUNNING | true |
| `orphan.adopt_blocked` | 归属复核不通过（wrapper 或 Java 分支），只告警不收养 | false |
| `orphan.adopt_failed` | 归属复核通过但 reconnect 拨号失败（socket 仍不可达），保留 PID 文件待下轮 | false |

> ⚠️ **落库前置（CP 侧待补）**：以上三个 action 必须同时补进 `internal/controlplane/grpc/orphan_audit.go` 的 `orphanAuditAllowedActions` 强白名单与 `proto/worker.proto` 的 action 注释，否则 CP 会以 `InvalidArgument` 拒收（Worker 侧仍留 WARN 日志）。这两处不在本 FR 的改动边界内，见 §5。

**新增代码入口**：`Manager.AdoptUnmanagedDaemonInstances`（一轮收养）、`OrphanScanner.SetAutoAdopt`、`OrphanScanner.Start`（立即一轮）、`OrphanScanner.ScanOnce`（第 0 相）；新增 `OrphanKindUnmanagedLiveRuntime` 与 `OrphanFinding.Adopted`。
