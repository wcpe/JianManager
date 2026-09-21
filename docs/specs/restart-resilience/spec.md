# 重启韧性加固与孤儿进程周期兜底（FR-455 / FR-456）

> 状态：📋 计划　·　关联 PRD：FR-455、FR-456　·　依赖：ADR-093（进程生命周期韧性与孤儿治理）　·　关联 ADR：ADR-003（守护进程 Wrapper 模式）、ADR-050（Worker 重连后由 CP 重推实例规格）、ADR-066（CP→Worker 经反向 gRPC 隧道）、ADR-079（CP↔Worker 实例反向对账）、ADR-081（Worker 仅经已认证反向隧道接指令）　·　**P0 上线阻塞**

## 1. 背景与目标

托管进程为四层模型 `CP → Worker → wrapper（setsid 游离）→ sh -c → java`。正常重启下 CP/Worker 均不杀服务器且自动重连（`manager.RecoverDaemonInstances`），这条链路已闭环。但对照代码与真机，仍存在四个与「重启不影响服务器、不产生孤儿」硬约束直接冲突的缺口：

1. **接管失败会误杀健康服务器**：`internal/worker/process/recover_orphan.go` 的 `reconnectWithRetry` 对存活 wrapper 仅重试 3 次（`recoverRetryBackoff = {1s,2s,4s}` ≈ 7s），耗尽即走 `reapOrphanWrapper` **强杀 wrapper + Java 两棵树**。若失败只是 socket 瞬时不可达，本可接管的运行中服务器被误杀。
2. **孤儿无运行期兜底**：清理集中在 Worker 启动时（`RecoverDaemonInstances` 扫 PID 目录）。运行期三条路径产的孤儿无兜底——① wrapper 自身死亡（`daemonStrategy.reapWrapper`，`internal/worker/process/daemon.go:196`）只把状态置 CRASHED，Java 仍活并占端口与 Paper `session.lock`；② `direct` 实例在 Worker 硬崩时 Java 未 `setsid`、reparent 到 init 变孤儿（反向对账只覆盖 Worker 内存表里的实例）；③ `docker` 实例 Worker 重启后容器仍在跑，但 Worker 只在 `Start` 时调 `removeExistingContainer`，不会恢复运行中容器。
3. **状态真源单一**：`internal/controlplane/grpc/handler.go` 的 `syncInstanceStates` 以心跳**清单**为唯一真源——凡 DB 为 RUNNING/STARTING/STOPPING 而清单未上报者一律置 STOPPED。缺口 2 因此放大为「面板 STOPPED 而进程在跑」（FR-436 真机事故：CP 重启后 63 实例集体失联）。
4. **重推触发脆弱**：`internal/controlplane/grpc/tunnel.go:135` 的 `onOpen` 仅在隧道计数 `n == 1` 时触发 `onConnected` → `instanceSvc.ResyncNode`（`apps/control-plane/main.go:783`）。CP 重启后若旧隧道 `onClose` 晚于新隧道 `onOpen`（瞬时 active=2），新连不触发重推，该轮节点规格同步丢失。

**目标**：接管处置前以进程侧证据复核避免误杀；把孤儿清理从「仅启动时」升级为「运行期持续兜底」；状态对账引入进程侧二次确认；重推触发多源化且幂等；FR-436 的 `wrapper 死/Java 活` 分支真机验证。

**范围内**：上述四项的 Worker/CP 侧改造 + 真机验证。**不做**：不改四层进程模型本身；不改 daemon wrapper 的 PID 文件/socket 协议（复用既有 `daemon.*` 基座）；不引入 systemd 等外部进程守护。

## 2. 设计

### 2.1 处置前置存活复核 + 更长退避（FR-455①）

- **更长退避**：`recoverRetryBackoff` 从 `{1s,2s,4s}` 扩为覆盖分钟级的递增序列（如 `{1s,2s,4s,8s,16s,32s,64s}` ≈ 127s），并可经配置注入（测试用小值）。理由：交接窗口的 socket 未就绪/资源紧张常是瞬时的，多等一轮即可接管，无需牺牲服务器。
- **处置前 cmdline/环境存活复核**：新增 `verifyProcessOwnership(pid int, instanceUUID string, workDir string, expectWrapper bool) bool`，在 `reapOrphanWrapper` **任何** `killTree` 之前调用：
  - Unix：读 `/proc/<pid>/cmdline`，校验其确含该实例工作目录/启动命令特征；wrapper 分支再读 `/proc/<pid>/environ` 校验 `JM_DAEMON_WRAPPER_CONFIG` 内的实例 UUID（wrapper argv 仅 `<worker> daemon`、不含 UUID，单凭 argv 会把**任何**实例的 wrapper 都判真）；
  - Windows：以 PID 查进程镜像路径与命令行做同口径匹配，wrapper 分支同样要求命令行携带实例 UUID（wrapper argv 携带 UUID）；
  - **复核不通过 → 只告警 + 落审计，不杀**，保留 PID 文件等下一轮扫描或人工介入（PID 被 OS 复用给无关进程时，杜绝误杀）。

> **ADR-093 决策（本节旧表述「复核不通过→只告警」的对齐）**：处置判据是「**确属真孤儿**」——即 **wrapper 已确证死亡、仅剩 Java 孤儿**。当复核**通过**（确属本实例）但进程**仍存活/仅瞬时不可达**时（典型：接管 `reconnectWithRetry` 重试耗尽、wrapper 进程未死），**只告警、保留 PID 文件、不杀**——绝不强杀一个 wrapper 与 Java 都健康、只是 socket 一时拨不通的运行中服务器。存活判据优先于「socket 是否拨通」。
- `pidAlive`（`daemon.IsPIDAlive`）保留用于「是否还活着」判断；`verifyProcessOwnership` 用于「是否确属目标实例」判断，二者正交。

### 2.2 运行期周期孤儿扫描（FR-456）

- Worker 侧新增**周期兜底任务**（`apps/worker/main.go` 启动），配置键（**与实现统一**，见 `internal/worker/config.go`）：
  - `orphan_scan.disabled`（bool，默认 `false` = **默认启用**；应急逃生口，显式 `true` 关闭周期扫描，见 `Config.SetDefault("orphan_scan.disabled", false)`）；
  - `orphan_scan.interval`（duration 字符串，默认 `60s`；非法/空回退 60s）；
  - `orphan_scan.dispose_policy`（`warn`（默认，只告警 + 落审计）/ `auto`（自动清理））。
  - 复用启动期 `RecoverDaemonInstances` 的扫描基座与 `daemon.KillPIDTree`。
- 扫描三态孤儿，按策略处置：
  1. **wrapper 死 / Java 活**：遍历 PID 目录，`!pidAlive(WrapperPID) && pidAlive(JavaPID)` → 走 `reapOrphanWrapper(..., errOrphanedWrapperGone)`（经 2.1 复核）。
  2. **direct 孤儿**：Worker 内存表已丢失该实例（Worker 曾硬崩），故需按进程命令行匹配本节点所有受管实例的启动命令/工作目录，发现无主 Java → 按策略告警/清理。判据以内存表为准：**受保护工作目录集合只收「可能仍有活进程」的状态（RUNNING/STARTING/STOPPING）**——含 STOPPING 可避免优雅停止期 Java 被误杀、绕过优雅关服；STOPPED/CRASHED 的进程已死，其工作目录**不**受保护（其 WorkDir 下的残留 Java 须能被识别：Worker 硬崩重启后 CP 会把 direct 实例按 STOPPED + WorkDir 重登记，若一并保护则残留 Java 被漏报）。根 PID 集合按「管理器记名」收（实例 strategy 报出的 PID 即受管）。与 FR-326 反向对账（`orphanRuntimeSvc`）互补。
  3. **docker 残留**：列本机容器（名字 `jianmanager-<uuid>`，见 `dockerStrategy.containerName`），DB/内存表未认作 RUNNING 却容器在跑（含已退出容器）→ 按策略告警/清理。**auto 档保守**：`Running=true` 的容器或内存表仍登记该实例时只告警不删（避免误删启动中/停止中实例的容器），仅删「已退出且内存表无该实例」的平台容器。
- **任何 killTree/removeContainer 前先做归属复核**（FR-455①）：direct 走 `verifyProcessOwnership`；docker 走 `verifyContainerOwnership`——**显式两级证据**：(a) 容器带平台受管标签（`com.jianmanager.managed=true`）且标签内实例 UUID 与容器名一致 = 强归属确证；(b) 标签与容器名不一致 = 归属不通过、拒绝删除；(c) 无标签（本 FR 之前创建的存量容器）= 退化为**容器名格式校验**（`name_format_only`），不构成强归属证明，该降级如实写入审计 detail 的 `evidence` 字段供运维核查。复核不通过只告警不杀，审计 success 依逐项实际结果判定。
- 处置动作一律落审计（`internal/controlplane/service/audit.go` 的 `RecordResultSafe`，经 Worker→CP 出站信道 `ReportOrphanAudit` 上报），保证「不静默」。

### 2.3 状态真源收敛（FR-455③）

- 新增 Worker RPC `ProbeInstanceEvidence`（`proto/` + `internal/worker/grpc/server.go`），对一批实例返回进程侧证据：wrapper/Java PID 是否存活、daemon socket 是否可达、docker 容器是否在跑。
- `syncInstanceStates`（`internal/controlplane/grpc/handler.go`）改逻辑：当某实例 DB 为 RUNNING/STARTING/STOPPING 但心跳清单未上报时，**不再直接翻 STOPPED**；先向该 Worker 拉进程侧证据，**证据也认为已不在跑**（PID 无、socket 不可达）才落 STOPPED；证据显示仍在跑则保持当前态并标 `statusReason`（消除通道抖动误判）。
- 为防对账长时间卡在非终态，证据拉取失败/超时设宽限（连续 N 拍不一致才落 STOPPED）。

### 2.4 重推多源化 + 幂等（FR-455②）

- `onOpen`（`tunnel.go`）去掉 `n == 1` 限制：隧道**每次**建立都触发 `onConnected`（幂等重推，由 `ResyncDeduper` 按节点去重），避免瞬时 active=2 漏推。
- 长驻订阅类副作用（`EventService.StartWorkerStream` / `PlayerEventService.StartWorkerStream` / `recoverConnectedBotFleetSubscriptions`）与重推**语义分离**：走 `onFirstConnected`，仅在节点「从无到有」（active 0→1）时触发**一次**，避免瞬时 active=2 重复建立长驻订阅流（stdout/stderr 双写、事件重复扇出）；两个 `StartWorkerStream` 另加 per-node 单飞守卫做纵深防御。
- 追加触发源：`Register` 成功后与心跳（低频，作自然兜底）均可触发同一幂等重推入口。
- Worker 侧 `ResyncInstances`（`internal/worker/grpc/server.go`）已是「只补不覆盖」语义，天然幂等，无需改动。
- 心跳里 `syncInstanceStates` 的**证据对账异步化**（`NewEvidenceReconcileDispatcher`，按节点单飞）：最长 8s 的进程侧证据拉取移出心跳应答关键路径（重推/心跳不被阻塞）。

## 3. 任务拆分

- [ ] T1 `recover_orphan.go`：扩退避序列 + `verifyProcessOwnership` + `reapOrphanWrapper` 前置复核（依赖无）
- [ ] T2 Worker 运行期孤儿扫描任务 + 三项扫描 + 策略开关 + 审计（依赖 T1）
- [ ] T3 `proto/` 加 `ProbeInstanceEvidence` + Worker 实现 + `syncInstanceStates` 真源收敛（依赖 T2 的证据口径）
- [ ] T4 `tunnel.go`/`main.go` 重推多源化 + per-node 幂等（依赖无）
- [ ] T5 单测覆盖复核拦截、周期扫描三态、真源收敛、重推幂等（依赖 T1~T4）
- [ ] T6 真机五场景验证（依赖全部）

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 存活 wrapper 因 socket 瞬时不可达时，退避窗口内接管成功，**不误杀** | 单测 + 真机 |
| 2 | cmdline/环境复核不通过（PID 被复用）时只告警不杀，PID 文件保留 | 单测 + 负例 |
| 2b | 退避耗尽但 wrapper 仍存活（确属本实例、仅瞬时不可达）时只告警不杀、保留 PID 文件；仅 wrapper 已死（真孤儿）才处置（ADR-093） | 单测 + 真机 |
| 3 | 运行期制造 wrapper 死/Java 活、direct 孤儿、docker 残留，均在有限周期内被识别并按策略处置，且落审计 | 真机 |
| 4 | 心跳清单缺项但进程侧证据显示在跑时，状态不翻 STOPPED | 单测 + 真机 |
| 5 | 隧道瞬时 active=2 时新连仍触发重推，且重复触发幂等无副作用 | 单测 |
| 6 | 配置开关可关闭周期扫描/切告警档 | 单测 |

**真机五场景清单（必须真机过，逐场景确认「服务器不受影响、状态不误判、无孤儿遗留」）**：

| 场景 | 操作 | 期望 |
|---|---|---|
| A **CP 重启** | 停 CP → 重启 CP | 已运行服务器不断链；CP 恢复后状态仍 RUNNING、重推不漏 |
| B **Worker 重启** | 停 Worker → 重启 Worker | `RecoverDaemonInstances` 接管存活 wrapper；服务器不受影响 |
| C **wrapper 被杀** | `kill -9` wrapper PID（保留 Java） | 周期扫描识别「wrapper 死/Java 活」，按策略处置，不产出永久孤儿（**FR-436 分支真机验证**） |
| D **direct 硬崩** | `kill -9` Worker（direct 实例） | 无主 Java 被扫出并按策略处置，不占端口/锁 |
| E **docker 残留** | 重启 Worker（docker 实例运行中） | 容器在跑却被面板记 STOPPED 的情形被识别/收敛，容器不误删 |

## 5. 风险 / 待定

- **cmdline 匹配的误判率**：匹配特征过宽会误复核通过（放过真孤儿），过窄会误拦截（放过误杀）。待定：以工作目录绝对路径为主键 + 启动命令特征为辅，真机标定。
- **direct 孤儿扫描成本**：全机进程枚举的 CPU/权限开销，需设定扫描上限与降级（枚举失败仅告警）。已实现单轮上限（见 §6）。
- **状态收敛的宽限期**：`N` 取值需在「抖动容忍」与「及时反映」间权衡，待真机观察心跳周期后拍板。
- **对账 RPC 频率**：仅对「DB 运行态但清单缺失」的少数实例触发，避免每拍全量拉取。

## 6. 关键常量 / 配置键 / 审计 action 登记（实现-文档对齐）

下表把实现中的关键常量、配置键与审计 action 名统一登记，供 reviewer 与文档核对（实现为准，文档随实现更新）。

**配置键**（`internal/worker/config.go`，`worker.yml` 下 `orphan_scan.*`，Viper `SetDefault`）：

| 键 | 默认 | 语义 |
|---|---|---|
| `orphan_scan.disabled` | `false` | `true` = 关闭周期扫描（应急逃生口）。注意：本文早前误写为 `worker.orphan_scan_enabled`（默认开），**以实现为准**——语义等价（都表示「默认启用」）。 |
| `orphan_scan.interval` | `60s` | 扫描周期；非法/空回退 60s。 |
| `orphan_scan.dispose_policy` | `warn` | `warn` 只告警 + 落审计 / `auto` 自动清理。 |

**关键常量**：

| 常量 | 值 | 位置 | 语义 |
|---|---|---|---|
| `recoverRetryBackoff` | `1s→2s→4s→8s→16s→32s→64s`（≈127s） | `recover_orphan.go` | 接管 reconnect 的有界递增重试窗口 |
| `orphanKillVerifyAttempts` / `orphanKillVerifyInterval` | `10` / `100ms` | `recover_orphan.go` | 强杀后存活复核的次数/间隔 |
| `maxScannedProcesses` | `8192` | `orphan_scan.go` | 单轮 direct 扫描进程枚举上限（超限降级告警） |
| `maxScannedContainers` | `2048` | `orphan_scan.go` | 单轮 docker 残留扫描容器枚举上限 |
| `maxScanRoundDuration` | `30s` | `orphan_scan.go` | 单轮扫描耗时告警阈值 |
| `evidenceSocketProbeTimeout` | `500ms` | `evidence.go` | 单次 daemon socket 探活超时 |
| `evidenceReconcileGraceBeats` | `3` | `evidence.go` | 证据不可得时的连续宽限拍数 |
| `evidenceProbeTimeout` | `8s` | `evidence.go` | CP→Worker 证据拉取超时（现经派发器异步，不再阻塞心跳） |
| `ResyncDeduper` cooldown | `60s` | `apps/control-plane/main.go`（`NewResyncDeduper`） | 重推按节点冷却窗口 |
| `reportTimeout`（orphan audit） | `10s` | `internal/worker/orphanaudit` | 孤儿审计上报超时 |

**审计 action 名**（`internal/controlplane/service/audit.go#RecordResultSafe`，targetType=`orphan`；direct 孤儿以 WorkDir 为 targetID）：

| action | 触发 |
|---|---|
| `orphan.scan_detected` | warn 档发现孤儿（三态） |
| `orphan.scan_disposed` | auto 档实际处置（success 依逐项结果） |
| `orphan.scan_dispose_blocked` | auto 档归属复核不过 / docker running 容器或内存表在册实例保守不删 |
| `orphan.dispose_blocked` | 接管兜底：wrapper 仍存活（瞬时不可达）或归属复核不过 → 只告警不杀 |
| `orphan.dispose_reaped` | 接管兜底：真孤儿强杀并清理 PID/socket |

**Worker→CP 上报通道**：`ReportOrphanAudit`（`proto/worker.proto`；CP 实现 `internal/controlplane/grpc/orphan_audit.go`；Worker 侧 `internal/worker/orphanaudit`）——与 `ReportCrashSnapshot` 同源的出站信道 + 节点身份鉴权，落 `RecordResultSafe`。

**主要装配入口**：`Manager.SetOrphanAuditHandler`（Worker 审计回调）、`ControlPlaneHandler.SetOrphanAuditRecorder`、`ControlPlaneHandler.SetReconcileDispatcher`、`TunnelRegistry.SetOnConnected` / `SetOnFirstConnected`。
