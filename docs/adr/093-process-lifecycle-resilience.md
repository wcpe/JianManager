# ADR-093: 进程生命周期韧性与孤儿治理

- **日期**: 2026-09-21
- **状态**: accepted
- **关联**: FR-455 / FR-456 / FR-459 · 缺陷修复 FR-325 / FR-436 · 增强 [ADR-003](003-daemon-wrapper.md)（守护进程 Wrapper 模式——wrapper/setsid/PID 文件基座）/ [ADR-050](050-worker-reconnect-instance-resync.md)（Worker 重连后由 CP 重推实例规格同步注册表——重推触发点）/ [ADR-066](066-worker-reverse-grpc-tunnel.md)（CP→Worker 指令经 Worker 发起的 gRPC 反向隧道）/ [ADR-081](081-reverse-tunnel-only-node-authentication.md)（Worker 仅通过已认证的反向隧道接收控制面指令）/ [ADR-079](079-instance-reverse-reconcile.md)（CP↔Worker 实例反向对账——孤儿反向发现基座）/ [ADR-019](019-docker-containerized-instances.md)（Docker 容器化实例运行——docker 残留场景）

## 上下文

托管进程为四层模型：`CP（无进程操作权）→ Worker → wrapper（setsid 游离）→ sh -c → java`。daemon 模式靠 wrapper 写的 **PID 文件 + Unix socket** 让 Worker 重启后接管（`RecoverDaemonInstances`）。**正常重启下 CP/Worker 均不杀服务器且自动重连**——这条链路已闭环。

用户的上线硬约束是：**worker 与 CP 面板重启都不能影响已启动的服务器、支持重连不断链、不产生孤儿进程**。对照代码与真机，四个缺口与它直接冲突：

1. **接管失败会误杀健康服务器**：`RecoverDaemonInstances` 对 wrapper 存活但 reconnect 失败者，仅重试 3 次（间隔 1/2/4s，约 7s）即走 `reapOrphanWrapper` **强杀 wrapper + Java 两棵树**。若失败只是 socket 文件被误删或瞬时资源紧张，一个**本可接管的运行中服务器会被误杀**。
2. **孤儿无运行期兜底**：清理只发生在 **Worker 启动时**。运行期三条路径产生的孤儿/状态错位无兜底——① wrapper 自己死了（Java 仍活，占端口与 Paper `session.lock`，全局 `reapWrapper` 只把状态置 CRASHED）；② `direct` 实例在 Worker 硬崩时 Java 未 `setsid`，reparent 到 init 变孤儿（反向对账只覆盖 Worker 内存表里的实例，扫不到）；③ `docker` 实例 Worker 重启后容器仍在跑但 Worker 不恢复它（面板 STOPPED 而容器在跑）。
3. **状态真源单一**：`syncInstanceStates` 把心跳**清单**当真源——凡 DB 为 RUNNING/STARTING/STOPPING 而清单未上报者一律置 STOPPED。这会把缺口 2 直接放大为"**面板 STOPPED 而进程在跑**"（FR-436 真机事故：CP 重启后 63 实例集体失联）。
4. **重推触发脆弱**：`onConnected` 仅在隧道计数 `n == 1` 时触发 `ResyncNode`；CP 重启后若旧隧道 `onClose` 晚于新隧道 `onOpen`（瞬时 active=2），新连**不触发重推**，该轮节点规格同步丢失。且 `Register` 已不再触发回调，重推只依赖这一个触发点。

## 决策

1. **处置前置存活复核 + 放缓接管**：接管重试改为**更长退避**（覆盖分钟级瞬时故障）；任何强杀处置前，先以 **cmdline 校验 PID 归属**（确认该 PID 确为目标实例的 Java），复核通过则**只告警不杀**，等下一轮或人工介入。杜绝误杀。

2. **运行期周期孤儿扫描**：Worker 侧新增**周期性**兜底任务（间隔可配、可开关），扫描三态孤儿（wrapper 死/Java 活、direct 孤儿、docker 残留），按策略处置（默认告警，可配自动清理），复用既有 `KillPIDTree` 与反向对账基座。把"只在启动时清理"升级为"运行期持续兜底"。

3. **状态真源收敛**：心跳清单缺失时**不直接翻 STOPPED**；引入**进程侧证据**（PID 文件存在性 / socket 探活 / 进程存活）作为二次确认，二者一致才落状态。消除"通道抖动导致误判"。

4. **重推触发多源化 + 幂等**：`ResyncNode` 可由隧道 `onOpen`、`Register`、心跳三处触发，按节点去重与幂等，消除"单点触发漏推"。

5. **FR-436 分支必须真机验证**：`wrapper 死 / Java 活` 的接管与强杀分支当前只过单测，上线前须在真机构造该场景验证。

## 理由

- **误杀是"重启影响服务器"的最恶劣形态**——用户明确禁止；且误杀不可逆（进程已死）。故决策 1 采"宁可多等一轮，不可误杀"。
- **孤儿无兜底必然长期存在**：运行期 wrapper 死亡、direct 硬崩、docker 残留都是真实路径（非理论），只靠启动时清理等于"跑得越久越脏"。
- **单一真源必然误判**：心跳清单是"Worker 内存表"的快照，任何让内存表暂缺已运行实例的情形都会误判；必须有进程侧证据交叉确认。
- **单触发点必然漏**：CP 重启时隧道重建竞态真实存在（真机多次确认），幂等多源是唯一稳妥解。

## 后果

- 改 `internal/worker/process/{manager,recover_orphan}.go`、`internal/worker/daemon/*`、`internal/controlplane/grpc/{handler,tunnel}.go`、`internal/controlplane/service/instance.go`（`ResyncNode`）。
- 新增 Worker 侧周期兜底任务（可配 `worker.orphan_scan_interval` 与开关）。
- 状态对账加进程侧二次确认，需新增 PID/socket 探测 RPC。
- 审计：孤儿处置、误杀拦截、重推均落审计。
- **上线前真机验证清单**：CP 重启 / Worker 重启 / wrapper 被杀 / direct 硬崩 / docker 残留，五场景逐一确认"服务器不受影响、状态不误判、无孤儿遗留"。

## 备选

- **只在 Worker 启动时清理孤儿（维持现状）**：被否——运行期缺口是真实存在的脏状态来源。
- **不查 cmdline 直接按 PID 杀（维持现状）**：被否——PID 被 OS 复用给无关进程时误杀，风险随机器负载上升。
- **保持"心跳清单即真源"**：被否——正是"面板 STOPPED 而进程在跑"的根因。
- **孤儿一律不自动处置（纯人工确认）**：被否——违反用户"自愈/不产生孤儿"诉求；采"默认告警 + 可配自动清理"折中。
- **重推改为轮询（放弃事件驱动）**：被否——轮询有延迟与成本；多源事件 + 幂等已足够，且保留既有心跳的自然兜底。
