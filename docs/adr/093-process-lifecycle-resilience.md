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

1. **处置前置存活复核 + 放缓接管**：接管重试改为**更长退避**（覆盖分钟级瞬时故障）；任何强杀处置前，先以 **cmdline（含进程环境）校验 PID 归属**（确认该 PID 确为目标实例的 Java/wrapper）。**权威判据（与实现 `reapOrphanWrapper` 及 spec 一致）：**
   - **wrapper 已确证死亡**（`errOrphanedWrapperGone`，或 reconnect 重试耗尽期间 wrapper 转为不存活）= **真孤儿** → 经归属复核后强杀 Java 树；
   - **wrapper 仍存活**（仅 socket 瞬时不可达/暂不健康）= **不是孤儿** → **只告警 + 落审计、不杀，并保留 PID 文件**，等下一轮扫描或人工介入（强杀会误杀一个 wrapper 与 Java 都健康的运行中服务器）；
   - **归属复核不通过**（PID 可能已被 OS 复用给无关进程）= → **只告警 + 落审计、不杀**，保留 PID 文件等下一轮或人工介入。
   即「复核通过」**不是**「只告警」的充分条件；「只告警」的触发条件是 *wrapper 存活* 或 *归属复核不通过*。杜绝误杀。

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

## 订正（2026-11-03）：归属复核分两种口径，收养不得复用强杀路径的宽口径

**发现**：本 ADR 决策 1 的「归属复核」落到实现是 `DefaultVerifyProcessOwnership` 的「**多特征任一命中**」——其中 `cwd == workDir` 可**单独**通过。该口径服务于**强杀路径**（`reapOrphanWrapper` / direct 孤儿处置）：放过一个真孤儿只是多等一轮，代价可接受。

但 FR-497③ 的**自动收养**也复用了同一入口，而收养有两个强杀路径没有的副作用：① `isolateAdoptedWrapper` 会把 wrapper 子树迁进实例 cgroup（此后停 cgroup 即连带终止）；② 实例取得该 PID 的控制权（后续 `Stop` / `Kill` 向它发信号）。于是「多特征任一命中」在收养路径上变成真实风险：PID 被 OS 复用后，一个 cwd **恰好**落在该实例 work_dir 下的无关进程即可凭 cwd 单特征通过复核 → 被误收养 → 被迁进实例 cgroup → 此后被误杀。真机 T7 的 C 臂只覆盖了「无关 sleep 的 cwd 不在 work_dir 下」这一种构造，故当时被拒；「cwd 恰在 work_dir 下」的构造会通过，缺口未被暴露。

**订正**：
1. 决策 1 的判据按**处置方向**分两种口径，不再共用同一个谓词：
   - 强杀路径（本 ADR 决策 1/2 的处置）继续用 `DefaultVerifyProcessOwnership` 的宽口径——宁可放过真孤儿，不可误杀，「多等一轮」的代价可接受；
   - 收养路径（FR-497③）新增 `DefaultVerifyAdoptOwnership`，**只认强特征**：实例 UUID 出现在 cmdline 或进程环境（wrapper 经 `JM_DAEMON_WRAPPER_CONFIG`、Java 由 `composeEnv` 继承）。`cwd == workDir`、cmdline 含工作目录等**弱特征单独不足以判真**。
2. wrapper 分支的口径按本 ADR 与 FR-497③ 规格原本的措辞收紧为**合取**：「argv 形如 `<worker 二进制> daemon`」**且**「进程环境携带本实例 UUID」。此前实现因前序 `return true` 早退，实际允许 cwd 单特征通过 wrapper 分支，与规格措辞不符。
3. 「复核不通过 → 只告警 + 落审计、**不杀**、保留 PID 文件」一字未改（护栏五.1）；审计 `orphan.adopt_blocked` 的 detail 增 `evidence` 字段说明拦截口径，使「为什么没收养」可解释。

**为什么不是「保持任一命中」**：本条不是接口风格取舍，而是副作用不对称。强杀路径的宽口径误判方向是「少杀一个孤儿」（可恢复）；收养路径的宽口径误判方向是「把一个无关进程纳入平台生命周期并由平台持有其信号权」（不可恢复）。同一判据无法同时服务这两个方向。

**已改文件**：`internal/worker/process/owner_verify.go`（新增 `DefaultVerifyAdoptOwnership`）、`manager.go`（新增 `verifyAdoptOwnership` 入口与 `adoptVerifyOwner` 注入桩）、`adopt_orphan.go`（收养改用新入口 + 审计 evidence）；规格同步见 `docs/specs/auto-adopt-orphans/spec.md` §2.1 / §4 / §5 / §6。

## 订正（2026-11-04）：`auto_adopt=false` 时必须同时关闭「未纳管活进程对」的处置

**发现**：本 ADR 决策 1 要求「wrapper 仍存活、仅 socket 瞬时不可达」一律只告警不杀。FR-497③ 给出的出口是自动收养，并给了 `orphan_scan.auto_adopt` 开关（`false` = 应急逃生口）。但收养开关与**处置策略**是两个独立旋钮，二者存在一个能用**纯配置组合**触发的缺口：

`auto_adopt=false` → 该进程对不被收养（不进入 `instances`）→ 因而不在 `managedInstanceRuntime` 的受管 PID / 工作目录集合中；而运行期周期扫描的 direct 相判据 `directOrphanWorkDir` 只按「cwd 落在 `serversDir` 下 / cmdline 引用 `serversDir`」认定 direct 孤儿，**不区分 wrapper 守护进程与 direct 裸进程**。于是这一对（wrapper 与 Java 的 cwd 都是实例工作目录）在 `dispose_policy=auto` 下被判为 direct 孤儿 → `handleDirectOrphan` → `killTree` 整棵进程树。这直接违反决策 1 的实质结论（「不是孤儿、不得处置」），只是绕道处置策略执行。

**订正**：**开关关闭 ≠ 允许处置。** `auto_adopt=false` 是运维明确表达「这些进程我来管」，不是「放弃它们的所有权保护」；自动处置若照旧执行，等于用处置策略绕过运维的明示意图，最坏后果是杀掉运维正在手动管理的服务器。故：

1. `dispose_policy=auto` 与 `auto_adopt=false` 同时成立时，对「**本轮发现的未纳管 + 活**」这一类进程对（即 `discoverUnmanagedLiveRecords` 的产出）**降级为 warn**：不 `killTree`、不落「已处置」审计，只落一条 WARN + 一条审计。
2. **降级必须留痕**：复用既有 action `orphan.scan_dispose_blocked`（success=false），detail 用 `reason="auto_adopt_disabled"` + `effectivePolicy="warn"` 与「归属复核不过」「启动接管在飞」两种既有拦截区分开。理由与护栏五.1 同源——**策略静默失效**比策略不生效更危险：运维既看不到「配置的处置策略在这一类上没生效」，也无法解释「扫描报了 direct 孤儿却没杀」。不新增审计输入面（白名单不变）。
3. **边界一字不放宽**：只降级「未纳管 + 活」这一类；已纳管实例（含脱管后按 STOPPED 重登记、目录下有残留 Java 的场景）照旧走 `dispose_policy`；`auto_adopt=true` 时行为完全不变；`dispose_policy=warn` 时不产生降级留痕；`wrapper 死 / Java 活` 不属本条（仍按 `dispose_policy` 处置，该形态不可收养，开关语义不覆盖它）。
   已知残差（如实登记）：**`wrapper 活 / Java 已死` 的中间态也不受保护**——发现判据要求两者均存活，该形态不构成收养候选，故一律按 `dispose_policy` 处置。刻意不扩大：让保护集与发现集脱钩会让判据失去单一真源；需要绝对保护时应配 `dispose_policy=warn`。
4. 保护快照**每轮重建**（`auto_adopt=true` 的轮次显式清空），避免开关运行期翻转后残留保护而放过真孤儿。

匹配口径为「PID ∈ {WrapperPID, JavaPID}」**或**「工作目录 == 记录 WorkDir」：前一条不可省——真实 wrapper 由 Worker 以 `exec.Command(exe, "daemon")` 拉起且不设 `cmd.Dir`，其 cwd 常不等于实例 workDir（见上文订正与 auto-adopt-orphans spec §5）。

**为什么不是「关开关就整相关掉扫描」**：`auto_adopt=false` 是**收养动作**的逃生口，不是**观测能力**的逃生口。连发现一并关掉会让「磁盘在跑、平台记账 STOPPED、控制面完全沉默」这一缺陷形态复活（本 ADR 决策 1 与 FR-497③ 的共同靶心）。故仍发现、仍告警，只是不收养、也不处置。

**已改文件**：`internal/worker/process/orphan_scan.go`（`OrphanScanner` 新增 `unmanagedLive` 保护快照 + `setUnmanagedLiveGuard` / `resetUnmanagedLiveGuard` / `unmanagedLiveGuardHit` / `warnDowngradedDirectOrphans` / `newlyDowngraded`；`scanDirectOrphans` 命中即降级）、`orphan_scan_unmanaged_downgrade_test.go`（新增六条回归）；规格同步见 `docs/specs/auto-adopt-orphans/spec.md` 新增 §2.5 与 §4 验收 4b~4f、§6 配置键/审计/代码入口。


## 订正（2026-10-27）：归属判据的 UUID 匹配收窄为 token 边界

**发现**：决策 1 的归属复核落到实现后，`processEnvMatchesInstance` 对「`/proc/<pid>/environ` 的 NUL 换空格后的整块环境」做 `strings.Contains(environ, instanceUUID)`。环境变量的**值**是任意字符串，于是一切实体只要在某个值里**出现过**本实例 UUID 这段字符，就会被判成「这个进程就是该实例」：

- 他实例的 UUID 恰好以本实例 UUID 为前缀（UUID 是变长文本片段，前缀相同不互斥）；
- 某个变量的值里带实例目录名 / 监控目标列表（`MONITOR_TARGETS=sandbox-<uuid>-replica`），也就是「看过这个 UUID」而不是「我就是它」；
- 两个 UUID 拼接后的中段。

误判方向与决策 1 的宽口径**不同**：宽口径（`cwd == workDir`）的误判是「把无关进程当成该实例」，后果是误杀；本条误判是「把**别的实例**的进程当成本实例」，而收养路径会取得该 PID 的控制权——后续 `Stop` / `Kill` 打到的正是**另一个实例**。强杀路径的 wrapper 分支用的是同一个谓词，故同样受影响。

**订正**：`readProcessEnviron` 保留原始 NUL（不再换空格），`processEnvMatchesInstance` 按 NUL 切条目、取 `=` 之后的值、要求 UUID 在值里**自成 token**——命中位置前后不得紧邻 UUID 字符集（hex 数字与 `-`）。`DefaultVerifyAdoptOwnership` 的 Java 分支 cmdline 判定同样换用该谓词。

**为什么不是「保持 contains」**：归属复核的语义是「这个进程**就是**该实例」，不是「某处出现过这串字符」。值里出现过是弱得多的证据，而收养给它的后果是不可逆的控制权转移。收窄只会让判据更保守（少判真 ⇒ 少收养 / 少杀），方向与决策 1「宁可多等一轮，不可误杀」一致。

**已改文件**：`internal/worker/process/owner_verify.go`（`readProcessEnviron` 返回原始串、`processEnvMatchesInstance` 按条目取值 + `containsUUIDToken` / `isUUIDCharAt`、Java 分支 cmdline 判定改用它）；回归 `adopt_javabranch_linux_test.go` 的 `TestFR497_JavaBranchSubstringPollutionRejected` / `TestFR497_WrapperBranchSubstringPollutionRejected` 与 `adopt_ownership_platform_test.go`（`TestContainsUUIDToken_Boundaries`）；规格同步见 `docs/specs/auto-adopt-orphans/spec.md` §2.1 / §5。

## 订正（2026-10-27）：收养登记必须写 `StartedAt`（启动宽限期纪元）

**发现**：FR-497③ 的两条收养登记路径（新建 / 就地补齐）都没有写 `Instance.StartedAt`，而 FR-459 的 `warmupRemaining` 在纪元为零值时返回 0——「做了宽限」变成「完全没有宽限」。叠加收养实例的既有形态（PID 记录只有 `ProbePort`、没有 `ServerPort`；未部署 ServerProbe 时该端口无人监听 ⇒ 响应维度恒失败），一个**刚被接管、完全可能正在正常服务**的服务器会在 `SuspicionThreshold` 拍（默认 3 × 30s ≈ 90s）后被判 `HealthFaultDead`。

**订正**：收养的两条登记路径写上 `StartedAt = time.Now()`；同一语义位置上的 `registerAdoptedDaemon`（启动接管）同步补写，避免两条接管路径口径不一。

**边界（一字不放宽）**：这不是「放宽假死判定」——宽限期只把结论从「死」改成「不明」，越过宽限期后照常判定（回归用例同时断言这一点）。**误判源本身未修**：`ServerPort` 缺失来自 PID 记录格式（记录里没有 server-port 字段），补它要改 wrapper→PID 文件协议，属另一 FR。

**已改文件**：`internal/worker/process/adopt_orphan.go`、`recover_orphan.go`；回归 `TestFR497_AdoptedHealthScanHasStartupWarmup`（删掉赋值即转红）；规格同步见 `docs/specs/auto-adopt-orphans/spec.md` §2.2 / §5。
