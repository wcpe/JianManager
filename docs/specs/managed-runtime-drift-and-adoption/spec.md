# FR-471：在管实例运行态对齐与外来运行时接管

## 1. 背景与问题

平台长期只管理「能证明归属」的进程（无 wrapper / 无 PID 文件 / 工作目录不在受管根下者一律不认），
这是**刻意的安全边界**（防误杀，见 `internal/worker/process/owner_verify.go`）。

真机现场（2026-09-23）：`/home/mcuser/server` 下 60 台 Paper 由 `start-all.sh` + tmux 在平台之外启动，
平台库中对应 65 个 `work_dir_in_place=1` 实例状态为 `STOPPED`——**面板与现实脱节**，且：

1. **无出口**：现有两条对账机制方向相反，结构上够不着这一态。
   - FR-326 反向对账（`OrphanRuntimeTracker`）处理「Worker 有、CP 无」→ 我们的情形是「CP 有、Worker 无」；
   - FR-456 孤儿扫描只认 `serversDir` 之下，农场目录在外。
2. **启动会双开**：`PreflightStart`（FR-314）只查 java 运行时 / 工作目录 / 启动目标 jar，
   **不查 `world/session.lock` 持有、不查端口占用**。此时从面板点「启动」→ 撞 Paper
   `SessionLock$ExceptionWorldConflict` 与端口冲突，两进程写同一批文件。

## 2. 目标

| 子能力 | 目标 |
|---|---|
| 运行态对齐 | 平台能发现并呈现「已注册实例的工作目录下有未纳管活进程」，不再说谎 |
| 启动冲突预检 | 现场占用（工作目录 / 端口）在转 STARTING **之前**拦截，杜绝双开 |
| 外来运行时接管 | 提供入口把外来进程纳入平台生命周期 |

**不变量**：不改动 FR-326 反向对账语义；漂移检测默认**只观测、不自动处置**；不接管任何无法证明归属的进程。

## 3. 设计

### 3.1 运行态漂移的发现与上报

- **Worker**：`OrphanScanner.ScanOnce` 增第 4 相 `scanForeignInRegisteredWorkdirs`，与既有三相同轮执行、
  **复用同一次进程枚举**。对每个**已注册**实例：若状态不属 Running/Starting/Stopping，且其 `WorkDir`
  （**任意路径，不限 `serversDir`**）下存在活进程（cwd 落在该目录，或命令行引用该目录）、
  且该 PID 不在受管 PID 集合中（并排除 Worker 自身）→ 记为一条漂移观测。
- **传输**：经心跳 `InstanceState.foreign_pid` / `foreign_cmdline`（proto 字段 6/7，增量、老端零值兼容）。
- **CP**：心跳处理中 `applyRuntimeDrift` 落 `instances.runtime_drift_pid / runtime_drift_cmdline / runtime_drift_at`；
  上报状态属运行类或本拍无漂移时清零（要求库中原值非 0，避免稳态无谓写）。
  **只更新已存在实例**（命中 0 行忽略，不越界到 FR-326 孤儿语义）。

### 3.2 启动冲突预检

`PreflightStart` 在既有三项后增两项（docker 实例仍整体放行）：

| 项 | 判据 | 失败语义 |
|---|---|---|
| `work_dir_busy` | 工作目录下存在活进程（判据与孤儿扫描同源 `procInWorkDir`） | 报占用 PID + 处置指引；枚举失败**不拦**（尽力而为，防基础设施读取失败挡住正常启动） |
| `port_free` | `server_port > 0` 且本机已被监听（`net.Listen` 试绑失败即占用，立即 Close） | 报端口号 |

### 3.3 接管

`POST /instances/:id/adopt-runtime` → `InstanceService.AdoptForeignRuntime` → Worker `AdoptForeignRuntime`：

1. 取实例操作锁；实例未注册 → 错误。
2. 无漂移 → 直接走正常启动（幂等）。
3. 有漂移 → **归属复核**（`verifyProcessOwnership`，与 FR-455① 同纪律，防观测与点击之间 PID 被 OS 回收误杀）
   → 对进程树发 SIGTERM（游戏服走 shutdown hook 保存世界、释放 `world/session.lock`）
   → 轮询等待退出 → 超时升级 SIGKILL 进程树 + 存活复核 → 清观测缓存 → 以受管方式 `startLocked`。
4. 任一环节失败**都不启动**（避免双开），并落审计 `orphan.foreign_runtime_adopt_blocked` / `_adopted`。

### 3.4 前端

实例详情页与列表/卡片在 `runtimeDriftPid > 0` 时显示「运行态漂移」标记（含未纳管 PID 与命令行摘要），
并提供**接管**按钮（写操作，走二次确认）。i18n zh/en，双主题。

### 3.5 启动期孤儿处置改为非破坏（本次事故驱动）

真机事故（2026-09-23）：换二进制重启 Worker 时，`RecoverDaemonInstances` 依**遗留 daemon PID 记录**
把「wrapper 已死、Java 仍活」判为真孤儿并**强杀 Java 树**，一次打断 54 台在跑农场（其 cwd 与 in-place
实例的 `workDir` 相同，故 FR-455① 的归属复核也**无法区分**「我们起的 java」与「他人同目录起的 java」）。

旧判据的立意（防「面板 STOPPED 却占端口、实例再也起不来」）现已被本 FR 的启动冲突预检与接管入口
非破坏地覆盖，故修订：

| 路径 | 旧行为 | 新行为 |
|---|---|---|
| 启动恢复：wrapper 已死 / Java 活 | 按 PID 记录强杀 Java 树 | **只观测**（审计 `orphan.startup_detected_not_reaped` + WARN）、**保留 PID 记录**，交周期扫描与接管流程 |
| 启动恢复：reconnect 失败且 wrapper 已死 | 同上强杀 | 同上只观测 |
| 启动恢复：reconnect 失败但 wrapper 仍活 | 只告警不杀（ADR-093） | 不变 |
| 周期孤儿扫描（`orphan.dispose_policy=auto`） | 按策略强杀 | **不变**（显式 opt-in 保留杀能力；默认 `warn`） |

**不变量**：启动路径（高频运维动作）不再有任何静默破坏；强杀能力只保留在显式开启的 `auto` 策略下。

## 4. 验收

- **真机判据**：对 `/home/mcuser/server` 农场，平台逐台报出漂移；点「接管」→ 实例转 RUNNING 且与磁盘进程
  一一对应；对运行中实例点「启动」→ 被预检拒绝而非双开。
- 单测：漂移发现（含 `serversDir` 之外命中）、无漂移不误报、受管运行中不误报、预检正反例、
  接管后状态一致、CP 写/清漂移与截断、前端标记与接管确认。

## 5. 已知边界与后续

1. **与 FR-456 的语义交叉**：若实例已注册、状态 STOPPED、且其工作目录**在 `serversDir` 之内**，
   FR-456 `scanDirectOrphans` 会把它判为 direct 孤儿（auto 档下清理），而 FR-471 会同时记为「可接管的漂移」。
   两者意图相反。当前默认策略为 `warn`（都不处置），故无实际危害；后续需明确取舍
   （建议：已注册实例的目录优先走「接管」，或 FR-471 仅对就地导入/受管根之外的实例生效）。
2. **接管语义**：无 wrapper 时无法经控制台通道优雅停服，接管采用「SIGTERM 后以受管方式重启」，
   即接管伴随一次重启；这是诚实的能力边界，非缺陷。
3. **落库列名**：显式固定 `runtime_drift_pid`，因 gorm 默认命名会把 `PID` 蛇形化为 `p_id`（同 `grpc_port` 先例）。

## 6. 修复过程中发现并修正的相邻缺陷（F3/F5/F7）

本轮真机验收额外暴露三处缺陷，一并修复：

### F3｜崩溃快照尾输出跨重启残留致根因误判
终端环形缓冲对整个实例生命周期共享、不随重启轮转，上一轮崩溃文本会残留进本轮快照；
分类器按优先级取词，把本次崩溃判成上一轮的原因（实测：注入端口占用却判 `oom`）。
**修复**：`Manager.SetInstanceStartHandler` 在 `startLocked` 的「新纪元」处触发，
装配层接线至 `TerminalServer.ResetBuffer`——与 FR-459「启动即开新健康纪元」同构。

### F5｜删除实例未级联清理二进制绑定
`instance_binary_bindings` 无 DB 级外键，删除事务漏清会残留孤儿行。
**修复**：删除事务内级联 `Delete(&model.InstanceBinaryBinding{})`。

### F7｜端口预检偶发误报（阻断合法启动）
原 `checkPortFree` 以「试绑失败」判占用，会因本进程瞬时状态（并发预检的短命 socket、
刚 Close 未完全释放）误判，把正常启动挡在门外（真机：同一空闲端口被误拒、重试即成功；
并连带触发健康自愈/重启交互，4 台实例最终 CRASHED）。
**修复**：改「**连接探测**（`127.0.0.1`/`[::1]` 连通即确证有监听者）+ **试绑有界重试一次**」，
空闲端口不再误报。
