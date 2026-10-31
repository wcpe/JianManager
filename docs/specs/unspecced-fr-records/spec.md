# 免 spec FR 的验收与交付记录（汇总）

- **状态**: 持续累积
- **关联**: PRD §4 的 FR 表

## 这个文件是什么

PRD 的 FR 表按约定「**只保留一行 FR + 状态，详情归 spec**」。但有一批 FR 是**免 spec** 的
（改动集中、边界清晰，不引入新进程 / 协议 / 数据模型 / 新 API），它们没有独立的 spec 目录。

此前这些 FR 的现状描述、真机取证与交付细节被直接写在 PRD 的需求列里，导致表格行长达数百至上千字符，
既难读，也让 PRD 退化成变更日志。按「需求列只写简短介绍」的约定，这些细节迁入本文件，
按 FR 分组保留原文，PRD 行只留简短介绍与状态标记。

**记录原则**：保留原文不改写（迁入是为了让 PRD 可读，不是为了重述历史）；新增免 spec 的 FR
若需要记录细节，追加到本文件对应小节，不要写回 PRD 表格。

## 迁入记录（2026-10-26）

### FR-282 install-worker.sh 同机多节点 systemd unit 名冲突（fix，增强 FR-222/080/277）

**需求补充**：`install-worker.sh` 硬编码 systemd unit 名 `jianmanager-worker`，同一主机装第 2 个 worker（不同 `--install-dir`）会**覆盖**第 1 个的 unit 与服务，致原节点被顶掉（真机复现：host-2 装机把 host-1 的 unit 冲掉，被迫手工建独立 unit 恢复）。改为 unit 名据 `--install-dir` basename 派生（默认 `/opt/jianmanager` 及 basename=jianmanager 仍映射 `jianmanager-worker` 保向后兼容；`jianmanager-node2` → `jianmanager-worker-node2`），使同机多 worker 各占独立 unit 互不覆盖；canonical 与内嵌副本同步、byte 一致测试守护（免 spec）

**交付状态**：✅ 已交付@v0.15.0·真机验收（三 unit 共存互不覆盖，同机新旧脚本正反对照）

### FR-310 删除运行中实例的进程处置一致性（fix，增强 FR-005/FIX-C）

**需求补充**：对 RUNNING 实例发 DELETE，CP 删记录返 200 而 Worker 运行态守卫拒杀进程——DB 与现实脱钩，java 进程成无主孤儿继续跑占端口（v0.15.0 真机复现：删运行中测试实例后 java -jar server.jar 遗留）。改为 CP 对 RUNNING 实例删除先优雅停（超时强杀树）再删，或 409 要求先停；Worker 重启孤儿接管扫描一并评估（免 spec）；真机验收补：删运行中 daemon 实例后工作目录残留（Unix 上 Java 自成进程组存活、RemoveAll 后重写 world 重建目录，并遗留 pid/sock），删目录前按 PID 强杀 wrapper+Java 两棵进程树+死透校验+删后校验重试兜底（与 FR-325 同根）

**交付状态**：✅ 已交付@v0.17.0

### FR-319 一键搭建异步化 + 全程可观测（fix+feat，真机复现

**需求补充**：1.20.1 搭建挂死 180s+——Paper CDN 从节点侧仅 ~200KB/s、47MB jar 需 4 分钟，provision 是同步 HTTP 长事务，前端超时断开→请求 ctx 连锁取消 DownloadCore→留空壳实例（无 server.jar 可点启动→秒退 CRASHED），错误随断开连接进黑洞、CP 零日志零审计）：搭建改任务中心异步——同步段只 ResolveBuild+建实例（立即 201+taskId），下载/写配置/探针在 CP 后台 goroutine（context.Background 独立超时，不受前端断开影响）分阶段推进 Task（下载核心→写配置→部署探针），失败 Task 含完整错误链 + 实例 statusReason 写「搭建未完成：…」；worker DownloadCore 补开始/完成/失败日志；前端向导提交即关框提示看任务中心。**二轮修复（异步化暴露的新缺口）**：① DownloadCore 改临时文件 `.part` + 完整校验后原子 rename——下载中窗口不再躺半截 jar（真机复现：搭建中点启动得 `Invalid or corrupt jarfile server.jar`）；② Task 加 instance_id 关联，CP 启动闸拦截「provision 任务未终态就点启动」并引导看任务中心；③ 搭建全程实例 statusReason 标「搭建中」+ 卡片琥珀提示（免 spec）

**交付状态**：✅ 已交付@v0.16.0

### FR-433 实例角色可变更与 beacon 配套服务角色（feat，增强 FR-032/ADR-007）

**需求补充**：`PUT /api/v1/instances/:id` 支持 `role`（非法值 400 拒绝，与 Create 的 grandfather 回落语义刻意不同），MCP `instance_update_config` 暴露 `role` 枚举；新增 `beacon` 角色标识配套服务实例（非 MC 群组服角色：不参与 proxy↔backend 拓扑、不适用 MC 探针与后端注册、不参与玩家查询）。role 变更不做隐式级联——原为 proxy 的 `server_registrations` 保留，避免误删运维数据。**真机验收通过**（隔离环境 E2E CP/Worker）：①MCP `instance_update_config` schema 暴露 `role` 且枚举完整（`backend/proxy/universal/beacon`、非必填）；②非法值经 MCP 与 HTTP 双侧均被拒（MCP 回「无效的实例角色: superadmin（可选 backend/proxy/universal/beacon）」，HTTP 回 400 `INVALID_REQUEST`），与 Create 的 grandfather 回落语义确不同；③`proxy`/`backend`/`beacon` 三角色均可设并回读；④no-cascade 证实——实例由 proxy→universal→proxy 后其 `server_registrations`（proxyId=1/backendId=3）仍在，未被级联删除

**交付状态**：✅ 已交付@v0.23.0

### FR-434 群组服 Network 与代理注册的 MCP 工具（feat，增强 FR-032/335）

**需求补充**：补齐 MCP 侧群组管理与拓扑能力——`network_list/get/create/update/delete`（删除需 `confirmName` 精确确认）、`network_add_members/remove_member`、`topology_get`（一次聚合代理注册 + 群组成员）、`registration_list/create/delete`；权限与实例分组同面（读 `instance.read`、写 `instance.write`），新增 `networkDomainActions` 独立 action 目录。使 MCP 客户端可完整维护群组服拓扑，不再依赖人工登录面板。**真机验收通过**（隔离环境 E2E CP/Worker）：network 全生命周期闭环（create→list→get→add_members→get 复查→update 改名→remove_member→delete 名不符正确拒绝→delete 精确名成功→list 复查空）；`topology_get` 一致；`registration_create/list/delete` 在 proxy↔backend 上闭环；对非代理实例调 `registration_list` 正确报「目标实例不是代理」

**交付状态**：✅ 已交付@v0.23.0

### FR-435 实例分组 MCP 工具（feat，增强 FR-165/ADR-033）

**需求补充**：`instance_group_tree/members/create/update/delete/add_members/remove_members` 七个工具，让 MCP 客户端可按大区/小区等结构树做批量运维；分组仅承载运维归类，不承载权限或配额（读 `instance.read`、写 `instance.write`）。**真机验收发现缺陷**（隔离环境 E2E CP/Worker）：`instance_group_tree/members/create/update/add_members/remove_members` 六项通过（create 挂树、tree 回读、add/remove 成员、update 改名+移根、members 回读均正确）；但 **`instance_group_delete` 恒被拒**（连空分组 + 精确 `confirmGroupName` 也回「能力/scope 不足或操作被拒绝」）。根因：`mcp/tools.go` 的 `callRegisteredTool` 对带 `ConfirmField` 的工具先调 `authorizeDestructiveTarget`，该函数按 descriptor 的 `ResourceType=instance` 把**分组 id 当实例 id** 填入 `target.InstanceID`，`Authorize→principalCanAccessInstance(p, groupId, 0)` 因实例 scope 不含该 id（且 `NodeID=0` 使节点 scope 兜底失效）而必拒——任何 token 都无法删除分组。修法：非实例资源（分组/群组）改用节点 scope 授权，或在 `authorizeDestructiveTarget` 按 action 区分目标类型。同病的 `network_delete` 因未走 `ConfirmField` 路径（自带 confirm）未受影响。**缺陷已修复**：`authorizeDestructiveTarget` 对分组/群组等「容器」目标改按**节点 scope**授权（`PrincipalHasNodeScope`），精确确认名改从分组/群组领域服务解析（新增 `InstanceGroupService.Get`）；回归测试 4 项（含「分组 id 与实例 id 相同」碰撞）全绿，真机复核（重建 E2E CP）删非空→`分组非空`拒绝、删名不符→拒绝、**删精确名→`{"ok":true}`** 通过

**交付状态**：✅ 已交付@v0.23.0

### FR-436 daemon 实例 wrapper 已死而 Java 仍活时的孤儿接管（fix，缺陷修复 FR-325 同源场景）

**需求补充**：接管扫描原逻辑只检查 wrapper，见到 wrapper 不存活即删 PID 文件——而 wrapper 死、其托管 Java 仍活时（父进程变 init），活着的 Java 从此不可发现，继续占服务端口与 Paper `session.lock`、面板却显示 STOPPED 且实例再起不来（真机事故：控制面重启后 63 个实例集体失联）。现改为按 PID 记录强杀 Java 树，死透才清理、杀不死则保留 PID 文件待下次兜底。**真机验收通过**（隔离环境 E2E CP/Worker，主动构造该分支）：令实例 wrapper 单独 SIGKILL、Java 存活（PPID 变 1、服务端口仍监听、pid 文件陈旧指向死 wrapper），重启 Worker 触发接管扫描——日志逐条命中「wrapper 已不存活但 Java 仍活，按 PID 记录强杀 Java 孤儿」→「孤儿进程树已强杀并清理 PID 文件」；事后确认原孤儿 Java 确已死亡、全机孤儿（PPID=1 残留）计数 0、pid 文件被清理，实例由 CP 重连重推规格重新登记（状态 STOPPED，无泄漏）。修复前的旧逻辑只删 pid 文件、Java 变不可发现，本次强杀分支真机确认生效

**交付状态**：✅ 已交付@v0.23.0

### FR-437 压测容量快照陈旧导致的伪容量不足（fix，缺陷修复 FR-398）

**需求补充**：①容量快照老化阈值 15s 仅比 bot-worker 10s 心跳多 5s 余量，一次 GC/负载抖动即判 `CAPACITY_SNAPSHOT_STALE`；②预检与容量查询工具走 15s TTL 缓存而非实时快照。二者叠加使节点明明有空闲容量仍报「可用容量 0」，需反复重试偶中。现阈值放宽到 30s（3 倍心跳），并让预检与 `loadtest_node_capacity` 优先经 `RefreshBotLoadCapacity` 取实时值。**真机验收通过**（生产 host-1，500 容量满载）：①`loadtest_node_capacity` 与预检均走 `RefreshBotLoadCapacity` 实时读——连续两次调用 `updatedAt`/`lastHeartbeatAt` 持续推进（非冻结）、`eventLoopP95Ms` 随负载变动，证明读的是实时快照而非陈旧缓存；②对满载节点发起预检，blocker 为 `BOT_LOAD_CAPACITY_INSUFFICIENT`（真实满载原因，`totalAvailable=0` 且节点 `unavailableReason` 为空），**未出现 `CAPACITY_SNAPSHOT_STALE`**——即「可用容量 0」是真实满载而非陈旧伪报，与修复前真机「明明有空闲却报 stale」的现象相反。心跳停摆的根因由 FR-438 分片化消除（FR-438 亦记录 `CAPACITY_SNAPSHOT_STALE` 现象不再出现），本项阈值 30s + 实时读共同保证不再误判

**交付状态**：✅ 已交付@v0.23.0

### FR-438 bot-worker 大规模舰队的事件循环过载（fix，缺陷修复 FR-398）

**需求补充**：Mineflayer 为每个 Bot 起 50ms 物理模拟定时器，数百 Bot 时单 Node 进程事件循环被占满（实测 500 容量下 p95 达 5s），keepalive 与心跳均无法调度——服务器集体判 `Timed out` 踢线，容量快照亦停止推进。现按行为判定关闭无需本地碰撞的 Bot 的物理模拟（`idle` 等不自行移动的行为默认关闭，`follow/patrol/guard` 保持开启），并把默认视距降到 `tiny`；另新增 `JM_BOT_WORKER_MAX_BOTS` 配置让容量上限可经 worker.yml 下发。**后续批（2026-09-21）继续压降并取得决定性进展**：①分片化——`bot_worker.shards` 由 6 提到 10，突破单进程单核上限（此前 6 片各 ~95–106% CPU 触顶，现 10 片各 ~41–44%，均值 5.4 核承载 500 Bot）；②内建插件裁剪——压测 Bot 用不到的 24 个 Mineflayer 插件（挖矿/合成/附魔/床/告示牌/粒子…）经 `plugins:{name:false}` 关闭，实测每 Bot CPU 0.713%→0.537%（-24.7%，注意 `digging` 因 pathfinder 经 `block.digTime` 依赖、`simple_inventory` 因 `inventory.js` 调 `bot.setQuickBarSlot` 而**必须保留**，禁用后者会直接崩）；③客户端视距 `tiny`→`2`（协议最小值），每 Bot 内存 31.7MB→16.6MB（-47.6%）；④服务端侧 `view-distance`/`simulation-distance` 10→4、`entity-broadcast-range-percentage` 100→50（20 个 Paper 服），并修正超编目标服 `max-players` 50→60、lobby 服 `spigot.yml` 的 `bungeecord:true` 阻碍直连的问题。**验证结论：500 Bot 全部在线且稳定（472 直连 MC 服 + 10 经 BC 代理，平台记账 activeBots=500），bot-worker 5.4 核 / 13.8GB，eventLoop p95 ~40ms，系统 40 核中用 ~20 核**；FR-437 的 `CAPACITY_SNAPSHOT_STALE` 与心跳停摆现象不再出现。**遗留**：18 个旧 `workerEpoch` 僵死 Bot 占用平台容量记账但不产生连接（`desiredState=running`/`status=error`，分片重启前残留），需平台侧提供失效 Bot 的回收能力

**交付状态**：✅ 已交付@v0.23.0（500 在线稳定；遗留的僵死 Bot 回收由 FR-460 承接并真机确认——`bot_reclaim.dispose_auto`/`refill` 正常运作）

### FR-440 实例标签 MCP 工具（feat，增强 FR-395/047）

**需求补充**：新增 `instance_update_tags` 工具与独立 action `agent.instance_update_tags`（V2 能力 `instance.write`，资源类型 instance），补齐 MCP 侧标签维护能力——现有 `instance_update_config` 未暴露 `tags` 字段，打标签只能绕开 MCP 走 HTTP，无法进 Agent 审计流水。三态语义：缺省=不改动、空数组=清空、非空=整体覆盖。`instance.read` 持有者可读（经 `agent_get_instance`），`instance.write` 持有者可写。**真机验收通过**（隔离环境 E2E CP/Worker）：三态各验一次（设标签→覆盖→清空为 `null`），且每次变更触发 FR-443 推送、Beacon 侧收到的 `jmTags` 与变更逐一吻合

**交付状态**：✅ 已交付@v0.23.0

### FR-441 通用二进制运行时搭建（feat，增强 FR-319/FR-342，见 ADR-090）

**需求补充**：`provision_server` 新增 `coreType=binary`，支持三类来源——① 制品库既有 asset、② 远程 URL、③ 节点本地文件路径；二进制不适用 PaperMC 的版本/构建解析，改为「来源解析 → 下载/复制 → 落盘 → 写启动命令」四段式，全程复用 `TaskService.RunAsync`（新 `TaskKind=binary_provision`）以便前端看到下载进度与终态站内信。启动方式复用既有 `startCommand` 字段（不引入新概念），实例角色默认 `universal`。失败进 `DAMAGED` 并保留 `provisionSpec` 供重建。**真机验收通过**（隔离环境）：①`node_file` 来源搭建 30MB 真实 Beacon 二进制，落盘 `-rwxr-xr-x` 且 sha256 完全一致，实例经 wrapper 启动并完成真实初始化（生成 `beacon.db`/`config.yml`/`secrets/`，持续运行未崩溃）；②`url` 来源的 http 明文被正确拒绝（「url 必须为 https」），`https` 路径经 GitHub 实际下载 30593289 字节成功落盘可执行（`source=url`）；③`asset` 来源由单测覆盖（真机制品库为空，未走该档）

**交付状态**：✅ 已交付@v0.23.0

### FR-442 内置 Beacon 快速搭建预设（feat，依赖 FR-441，见 ADR-090）

**需求补充**：在 `coreType=binary` 之上提供 Beacon 预设——制品来源优先级为「制品库已有版本 > Beacon GitHub Releases」，默认启动命令 `./beacon-1.1.0-linux-amd64`、默认角色 `beacon`（FR-433）、默认 JVM 无关（Go 二进制）。一键搭建后管理台可完成首次鉴权初始化。Beacon 与本平台为**可选协同**关系：未部署 Beacon 时本平台全部能力不受影响。**真机验收通过**：`coreType=beacon` 一键搭建产出 `role=beacon`、`jdkId=0`、`startCommand=./beacon-1.1.0-linux-amd64`（由落盘名派生）的实例；来源解析命中 GitHub Releases（E2E 制品库为空，走「制品库 > GitHub」的第二档，`source=url` 取件 30593289 字节落盘可执行）；实例启动后 Beacon 完成真实初始化（生成 `beacon.db`/`config.yml`/`secrets/`），配独立端口后**持续运行且 HTTP 200**，管理台 `POST /admin/v1/auth/login` 登录成功（首次鉴权初始化闭环）

**交付状态**：✅ 已交付@v0.23.0

### FR-443 向 Beacon 推送拓扑变更（feat，依赖 Beacon FR-222，见 ADR-090）

**需求补充**：实例创建/删除/改归属（name/role/tags）时，若配置了 Beacon 协同端点则推送增量事件，使 Beacon 侧 server 归属与默认入口自动对齐，免去人工双写。**兼容非依赖**：未配置端点即整体跳过；推送失败只写审计与告警、**绝不阻塞或回滚本机操作**；投递尽力而为不做重试队列。实例启停不推送（Beacon 在线状态由 agent 心跳自维护）。**真机验收通过**：正向拉通 mock Beacon（收包含 `X-Beacon-Token` 与 `namespace:prod`，`create`/`ownership` 事件按变更触发，审计落 `instance.beacon_push_ok`）；端点不可达时本机操作**零阻塞**（改名 39ms 完成）且写失败审计；实例启停确认不推送

**交付状态**：✅ 已交付@v0.23.0

### FR-471 在管实例运行态对齐、启动冲突预检与外来运行时接管（feat，增强 FR-302/326/455/456）

**需求补充**：外部启动（tmux/脚本）的服务器进程不被平台接管——DB 记 STOPPED 而磁盘在跑、面板说谎且无出口；启动预检只查 java/目录/jar，不含现场占用，故此时点「启动」会双开撞 `world/session.lock` 与端口。**另修一处破坏性缺陷**：启动期孤儿恢复会依遗留 PID 记录强杀「wrapper 已死 / Java 仍活」的进程，而就地导入实例的 `workDir` 与外部服务器目录相同，归属复核结构上无法区分，真机曾因此一次打断 54 台在跑农场。现新增三件事：①孤儿扫描增第 4 相，按**已注册实例的 workDir**（不限受管 `serversDir`，覆盖就地导入）探测未纳管活进程，经心跳 `foreign_pid/foreign_cmdline` 上报，CP 落 `instances.runtime_drift_*` 供面板提示（只观测不自动处置）；②启动预检补 `work_dir_busy` 与 `port_free` 两项，把双开拦在转 STARTING 之前；③新增接管入口 `POST /instances/:id/adopt-runtime`——先做归属复核（防 PID 回收误杀），再 SIGTERM 进程树等其优雅退出（超时升级 SIGKILL），随后以受管方式拉起；④启动恢复路径**不再强杀**，改为只观测 + 保留 PID 记录（强杀只保留在显式 `orphan.dispose_policy=auto` 的周期扫描）；⑤端口预检改「连接探测 + 试绑有界重试」消除偶发误报。**验收**（真机通过）：外部启动的农场逐台报出漂移；点「接管」后状态转 RUNNING 且与磁盘进程一一对应；对运行中实例点「启动」被预检拒绝而非双开；重启 Worker 不再强杀在跑服务器

**交付状态**：✅ 已交付@v0.23.0

### FR-472 僵尸压测会话收敛

**需求补充**：CP 重启后会话卡在 running 导致补足死循环与写放大（fix，缺陷修复 FR-460/370）：压测会话的生命周期终结依赖显式 `Stop` 调用，CP 重启或 bot-worker 死亡后该路径丢失，会话永久停留 `status=running`。`refillRunningSessions` 无条件捞取**全部** running 会话做容量补足，于是对早已失联的会话每拍重建期望行并失败——失败落在事务内，产生持续写盘。**进程内退避（`refillAttempts` map）结构上兜不住**：它随 CP 进程结束而清零，重启即触发一轮全量重试。**真机现场（2026-09-24）**：生产 CP 残留 132 个历史会话（61 running + 32 pending），1132 条 Bot 记录全数失联，CP 持续写库 **3.5 MB/s**、`cp.out` 涨至 **962 MB**。现按**数据库可见的事实**判定僵尸：`status=running` 且在线 Bot 数为 0 且 `updated_at` 停滞超过 `botZombieSessionIdleThreshold`（默认 **10 分钟** = Bot 宽限期 2m 的 5 倍，容忍 Worker 重启与世代迁移窗口）即收敛为 `stopped`，写 `ended_at`/`last_error` 与强审计 `bot_reclaim.session_reaped`；收敛在 `refillRunningSessions` **之前**执行，使僵尸从根上不进入补足扫描。**不变量**：仍持有在线 Bot 的会话永不被收敛。**验收**（单测 3 条：僵尸必收敛、收敛必留痕、健康会话不被误伤；真机：93 个僵尸会话收敛后 CP 写 IO 由 3.5 MB/s 降至 0.3 MB/s，补足失败日志停止）

**交付状态**：✅ 已交付@v0.24.0

### FR-486 Worker 启动时长与状态加载可观测性（feat，性能/可观测，增强 FR-474/484）

**需求补充**：1.2 GB ingest 状态文件使 Worker 重启后 **8m39s 才注册上线**（2026-09-28 生产实测：19:31:35 启动 → 19:40:14 注册成功），期间主控台看不到节点、实例健康巡检未运行；且加载全程**没有任何进度日志**——平台对自己的启动过程同样无观测（与 FR-485 同源）。范围：① 加载分阶段计时与日志（读取/解析/恢复/首次持久化）；② 进度可见（注册前也能上报启动阶段）；③ 大状态文件加载优化（流式解析、引用化后免全量内联解析）；④ 状态超阈值的启动前告警。**动因**：C2 窗口重启实测；B1a 压缩生效后需复测时长以确认改善

**交付状态**：🔨 开发中·①②④ 已落地（①启动分阶段计时与日志、②注册前进度上报方案A、④状态超阈告警；③大索引加载优化**已验证需要但未实施**——基准实测 Load() 全表扫描 store.go:823，100k 行 441ms，按生产 746,884 行外推约 3.3-4.0s，目标降 ≤100ms@100k）。spec：`docs/specs/worker-startup-observability/spec.md`；真机验证：七段阶段日志齐全、ReportStartupProgress 四次上报全部到达且注册前后凭据正确切换

### FR-502 前端设计底座单一真源与 `dark:` 变体修复（ref，免 spec）

**需求补充**：token 定义从应用侧迁入组件库 `packages/ui/src/styles/`（`theme-map.css` token→Tailwind utility 映射 / `tokens.css` 明暗基线 / `themes.css` 五套主题色覆盖 / `motion.css` 动效关键帧 / `index.css` 汇总入口），主控台与控件博物馆改为「一次 `@import` 即得完整底座」，删除与主控台局部定义已发生漂移的平行副本 `packages/ui/src/styles.css`（组件库自此自包含，不再要求消费方自备 token）；顺带修复全站 `dark:` 变体半失效——显式声明 `@custom-variant dark (&:is(.dark *))` 把变体绑定到应用内 `.dark` class（此前全仓无该声明，Tailwind v4 默认按 `prefers-color-scheme` 媒体查询解析，与 `lib/theme.ts` 写 class 的切主题策略不一致，导致「系统亮色 + 应用暗色」下组件内显式 `dark:` 样式不生效）。**验收**：① 两个消费方均只 `@import` 组件库底座、不再自带 token 副本；② 主控台 `index.css` 1074 → 575 行（基线 = 分支起点 `14472681`），token 迁移前后构建产物一致（CSS/JS 文件名哈希未变）；③ 产物 `:is(.dark *)` 0 → 49 处、`prefers-color-scheme: dark` 1 → 0 处，体积 +0.56 kB；④ 真浏览器实测暗色命中组件内 `dark:`（outline / destructive 按钮计算样式随 `html.dark` 变化）；⑤ 组件库单测全绿

**交付状态**：🔨 开发中·阶段 1 完成（证据 ②③④⑤ 已取得；整批随分支 `feat/web-overhaul` 交付，发版时由 `sdd-release-version` 统一标 `✅ 已交付@vX.Y.Z`）

### FR-503 主题配色对比度自动校验（test，免 spec）

**需求补充**：为「5 套主题色 × 明暗」建立可复算的对比度门禁——新增 `packages/ui/src/lib/color-contrast.ts`（hex / rgb / oklch 解析 + WCAG 2.x 相对亮度与对比度计算 + CSS 作用域合并）与断言集 `packages/ui/src/lib/color-contrast.test.ts`：对设计底座 10 组配色（5 主题 × 亮暗）× 11 对「前景 / 背景」组合共 **110 项**逐项断言（正文类 ≥ 4.5:1，次要文字与状态徽章 ≥ 3:1），失败时一次性列出全部不达标项并给出实测比值；另有解析器与作用域的元断言（`index.css` 与 `themes.css` 中所有选择器均被解析器识别、所有颜色写法均可解析），新增主题漏定义即失败。**动因**：配色校验此前靠人眼，5 主题 × 2 明暗的配对矩阵不可能人工穷举。**验收**：① 110 项配对断言在 10 组配色下全绿；② 改 token / 加主题后不达标即失败，并定位到「哪套主题 · 哪个明暗 · 哪一对变量」；③ 用独立实现的解析器交叉验证同一份 CSS 结论一致（不留「测试与实现同源同错」盲区）

**交付状态**：🔨 开发中·阶段 2 完成（已查出并修复 2 处真实缺陷：teal·亮色与 sunset·亮色的按钮文字配对不达标，改深色墨字后分别实测 7.18:1 与约 6:1，见 `packages/ui/src/styles/themes.css` 注释；组件库测试 240 用例全绿）

### FR-504 内容页布局规范层（feat，免 spec）

**需求补充**：把「每个内容页各自手写骨架类名」收敛为唯一出处——新增 `packages/ui/src/components/layout/`（`PageShell` 三壳态 default 整页滚动 / fixed 固定视口内部滚动 / tool 全占满，`PageHeader`、`ScopeBar`、`SummaryStrip`、`Toolbar`、`PlatformTabs`，以及 `TwoCol` / `ThreeCol` / `MetricGrid` / `CardsGrid` / `SettingsLayout` 等网格原语），页面只从该层取原语；`Panel` 补三段式 `footer` 槽（头 / 身 / 脚），使「工具条 + 数据区 + 页脚」成为标准形状；审计页 `AuditPage` 作为首个迁移试点。**边界**：本层只管「一页内部怎么摆」，页与页之间的骨架归 FR-505。**验收**：① 三种壳态的留白 / 纵向节奏 / 滚动模型各有用例断言；② `Panel` 三段式为向后兼容的新增槽，既有用法不受影响；③ `AuditPage` 迁移后行为不变、原有用例全绿；④ 布局层 24 项用例在组件库单测内运行

**交付状态**：🔨 开发中·阶段 3 完成（原语与用例已落，`AuditPage` 已迁移）

### FR-505 导航外壳组件层（feat，免 spec，承 FR-504 边界）

**需求补充**：新增 `packages/ui/src/components/shell/`——`AppShell`（53px 顶栏 + 侧栏 + 页面区三件套骨架）、`TopNav`（四工作区切换 + 品牌 / 检索 / 操作槽）、`SideNav`（246px 展开 · 折叠 54px，含 `SideNavHead` / `SideNavShortcuts` / `SideNavResource` / `SideNavBottom`）、`ResourceTree`（资源树，限流展开与查询）、`CommandPalette`（⌘K 全局检索）、`ObjectPageHeader`（对象页头：面包屑 + 指标 + 工具）；两层分工写进各自 `index.ts` 注释——本层管「页与页之间共用的骨架」，页内布局归 FR-504，不可混用。**验收**：① 六组件各有 props 契约与用例（折叠 / 展开、键盘操作、空态，合计 55 项）；② 关键尺寸与设计原型一致（顶栏 53px、侧栏 246 / 54px）；③ 控件博物馆提供整合预览页，可在真浏览器按四工作区切换查看

**交付状态**：🔨 开发中·阶段 4 完成（六组件 + 55 项用例；`apps/ui-museum` 预览页真浏览器截图已取得。**重构（纯重命名 + 死代码删除，零行为变更）**：目录 `components/shell/` → `components/nav/`，因本层只含导航原语、不含整体外壳；`AppShell` 与 `ResourceTree` 零引用已删除，余四组件（`TopNav` / `SideNav` / `CommandPalette` / `ObjectPageHeader`）原名保留，用例 38 项随组件平移。正文所写的六组件与旧路径已失效，按「已有 FR 只改状态字段」约定未改正文）

### FR-506 工作区导航数据层（feat，免 spec）

**需求补充**：新增 `apps/control-plane-web/src/components/console/workspace-navigation.ts`，把导航登记从「六域扁平」改为「**四工作区 × 组 × 页面**」三层（服务器运维 / 观测与自动化 / 运营与分发 / 平台管理），按设计交付的原路由对照表（45 条 = 37 导航 + 5 子路由 + 3 认证页）归位：本层登记 42 条（37 + 5），3 个认证页独立于工作区。**新增而非改写** `nav-config.ts`——旧六域定义原样保留作对照物与回滚路径（可回退），两套数据并存并由测试逐条证明等价：37 个导航目的地的 `perm` / `labelKey` / `icon` 与旧真源同路径项逐条同值（含 any-of 数组）；5 个子路由在旧真源无对应项，**继承其入口页 `perm`**（看不见列表页即看不见其深链），属新增项而非权限放宽；裁剪算法同形复刻，不 import 旧私有实现。**验收**：① 登记条数对账 42 + 3 = 45，三层结构无残留 sections；② 旧导航 37 条零遗漏、新增恰为 5 条子路由；③ 11 类权限场景（空权限 / 单权限 / 多权限 / any-of 单边 / 观测只读 / 运营域 / 平台域 / 全量节点 / 管理员短路）下新旧可见面无差异；④ 路径归一化（query/hash、尾斜杠、重复斜杠、缺前导斜杠）与未命中返回 null；⑤ 空权限下不出现任何实例 / 发布 / 会话深链

**交付状态**：🔨 开发中·阶段 5 完成（登记数据 + 17 项用例全绿；新增 i18n 键**已全部补进** `src/i18n/{zh,en}.json`，中英齐备。**路径更正**：导航真源实际位于 `apps/control-plane-web/src/lib/console/workspace-navigation.ts`，本条正文所写的 `components/console/` 位置已失效——按「已有 FR 只改状态字段」约定未改正文）

### FR-507 主控台外壳接入与页面迁移（feat，依赖 FR-502~506，免 spec）

**需求补充**：把主控台从旧六域外壳切到新工作区导航——桌面侧栏（旧六域实现 `ConsoleSidebar`）、移动导航 `MobileConsoleNav` 与命令面板 `CommandPalette` 三处消费方一律改以 `workspace-navigation.ts` 为导航真源，接上 `AppShell` / `TopNav` / `SideNav` / `ResourceTree`；既有内容页按 FR-504 布局规范逐个迁移；列表页渲染性能一并处理——阶段 1 实测审计页 250 条、任务页数百条记录一次性进 DOM 已使用例在 10s 超时阈值边缘抖动（无虚拟化、无窗口裁剪，开销随条数线性恶化），属本轮核心目标范围。**验收**：① 四工作区导航在桌面侧栏 / 移动导航 / 命令面板三处一致可达，权限裁剪与旧实现等价；② 迁移页面行为不变（空 / 常规 / 超量 / 异常四态齐备）；③ 目标列表页大数据量下真浏览器渲染开销不再随条数线性恶化（有前后对比数据）；④ 全部消费方切换后删除 `nav-config.ts` 的六域定义——`Workspace.tsx` 路由表承接原 RouteRegistry 语义，**不得为缩短侧栏删登记项**

**交付状态**：🔨 开发中·阶段 6 完成（**四验收已达成，待真浏览器前后对比数据**）。① 三处消费方全部切换：桌面侧栏 `WorkspaceSidebar`、移动导航 `MobileConsoleNav`、命令面板 `CommandPalette` 均以 `lib/console/workspace-navigation.ts` 为唯一导航真源；**移动导航改为「底部格子=四工作区」**——旧实现把六域顶层组铺成 5 格并 `slice(0,5)`，观测/客户端分发/平台设置在手机上永远点不到（静默功能缺失），按工作区切格后固定 4 格不再截断，工作区内部的分组与侧栏同源。② 内容页迁移：`views` 下 46 个 `*PageView/*Page` 中 41 个已用 FR-504 布局原语；余 5 个为认证页（Login/Setup/Invite，不在任何工作区）与全幅画布工具页（SuperWorkbench/Director，按设计不用内容页骨架）。③ 虚拟化：`AuditPageView`、`TasksPageView` 等 22 处已接 `lib/shared/virtual-list.ts` 的 `useVirtualRows`（变高行 + 行高实测合并 + 宽度变更丢旧实测重测 + overscan），渲染开销不再随条数线性恶化。**真浏览器前后对比数据（验收③明示要求）已采集**：Playwright 1.61 + 真 Chromium、dev server 跑当前代码，新增 `apps/control-plane-web/e2e/render-cost-benchmark.spec.ts`；日志中心 **12004 条数据仅挂载 24 行 DOM（0.2%）**，滚到底再回顶的重挂载耗时 326.8 / 286.6 / 691.0 ms，**long task 0 次** —— 渲染开销与数据量解耦。审计页在 mock dev server 下只有 ~15 条真实数据（`devmock/handlers/domains/identity.ts` 的 `audit` 集合；dom 用例里的 250 条是测试用 msw 注入的），故审计页只作小数据量对照，不作大数据量证据。④ `nav-config.ts` 已删除，连同死代码 `ConsoleSidebar`（接线层 + 受控视图 + 两份用例）；类型下沉到 `lib/shared/navigation-types.ts`。**等价性证据固化**：旧六域 41 条登记冻结为 `apps/control-plane-web/src/components/console/__fixtures_nav-snapshot.json`，`workspace-navigation.test.ts` 据其重算旧权限语义并逐条对照，17 项全绿；`route-chunks.test.ts` 验证新层 42 条登记全部可预取。`Workspace.tsx` 路由表未被削减）

### FR-509 侧栏资源面板收敛为「一个分组方式 + 服务器列表」（feat，增强 FR-165/FR-293，免 spec）

**需求补充**：侧栏此前有 8 种分组模式（`pinned`/`node`/`network`/`tag`/`topology`/`name`/`created`/`custom`），其中 `custom` 是仅存 localStorage 的本地分组（换机即失）、`topology` 与后端分组树同源（FR-444 已把 Beacon 拓扑映射进 `instance_group_nodes`）、`name`/`created` 是排序不是分类。本 FR 删除 `custom`，把 `topology` 并入 `groupTree`，并**放弃常驻分组树与标签两块**（试做后判定：标签是次要维度，无论平铺还是折叠都会挤占侧栏；分组树常驻会把侧栏变成第二棵树）——收敛为**搜索 + 状态筛选 + 一个分组方式下拉（置顶 / 按节点 / 按分组 / 按群组 / 按标签 / 按名称 / 按创建时间）+ 服务器列表**。`groupTree` 维度按树的深度优先序分段、只在段内列出该子树成员（M:N 多归保留：同一实例可在多个段各出现一次），空组不占位置，层级用段名前缀缩进表达。**边界**：不改 ADR-033 的 M:N、不新增数据表与后端接口。**验收**：① 侧栏只有一个维度下拉，无 `custom`，无常驻分组树/标签块；② 历史 localStorage 存有 `custom` 时回落默认；③ `groupTree` 维度按树序分段、含后代成员、空组不出现；④ M:N 实例在多个段各出现一次；⑤ 置顶维度按收藏顺序且快照行可取消置顶；⑥ 空/无匹配有引导态；⑦ 真机过一遍（dev:mock 与真实 CP）。**实施备注**：devmock 的 `/instances/search` 原不支持 `groupId`，已按子树语义补齐（与 `GET /instance-groups/:id/instances` 同口径）。

**交付状态**：✅ 已交付@v0.24.0（tsc 零错、eslint 0 error、前端 2970 用例全绿；真机验证侧栏仅剩搜索/筛选/分组方式/列表）

### FR-510 删除侧栏本地自定义分组并清掉其类型错误（ref，免 spec，与 FR-509 同批交付）

**需求补充**：删除 `apps/control-plane-web/src/lib/console/sidebar-server-groups.ts` 整个文件与 `SidebarResourcePanel` 中依赖它的建组/重命名/删组/拖拽归组逻辑及 `modeCustom` i18n 键。**动因**：该文件把 localStorage 反序列化结果按 `Partial<SidebarServerGroupsState>` 取值后 `.groups`/`.members` 仍被推断为 `{}` 的成员访问，产生 6 处 `tsc` 错误（`sidebar-server-groups.ts:35/36/37/42/43/44`），与 `WorkspaceSidebar.tsx:114` 的 `InstanceGroupKeyMap`（`Map<number, string[]>`）误传给期望 `Map<number, string>` 共 10 处编译错误，阻塞 `tsc -b`。删除该本地分组后其 6 处错误随文件消失，剩余 4 处（`InstanceGroupKeyMap` 接线 + `ReactNode` 未用导入 + `sidebar-tools.ts:65` 可能 undefined）在本条内一并修正。**验收**：① `pnpm exec tsc -b` 零错误（不被管道退出码掩盖，需直读输出）；② 全仓不再有 `sidebar-server-groups` 的引用；③ 侧栏既有交互（搜索/状态筛选/收藏/打开服务器）行为不变；④ 既有 dom 用例全绿

**交付状态**：✅ 已交付@v0.24.0（`sidebar-server-groups.ts` 已移出、`custom` 模式与拖拽建组已删、4 个 i18n 键已摘、10 个编译错误清零、`buildSections` 抽到 `lib/console/sidebar-sections.ts`；`tsc -b` 零错、侧栏 56 用例全绿）

### FR-511 侧栏服务器列表精简与虚拟化（feat，增强 FR-165/FR-509，免 spec）

**需求补充**：按用户逐条要求把侧栏收敛为「服务器列表 + 分组方式菜单 + 三个点『更多』」。① **可收纳按钮全部挪进「更多」菜单**：侧栏主体不再渲染 `SidebarPinnedTools` 工具行，四条工具（网络拓扑 / 分组管理 / 超级工作台 / 导播台）只在菜单两区里出现，置回的工具也只落在「显示在侧边栏」区。② **「更多」是纯三个点小图标**（22×22、无文字、`aria-label` 落在真 `<button>` 上），位置在「版本号 / 开源协议」那一行的**正上方**（`SidebarPreferences.moreMenu` 插槽）。③ **服务器行改为紧凑两行 36px**（原 30px 单行、试做 44px 后按用户反馈压回）：第一行名称、第二行备注（实例模型无独立 remark 字段，取 `statusReason` —— 崩溃原因、未绑定 JDK 等自由文本即其备注语义；为空则留空，**不回落节点名**，避免与组头重复）。④ **行尾显示在线人数「当前/最大」**（用户先要 玩家+TPS、后改为只留人数并要求带上限）：新增指标键 `inst_players_max`——心跳 `InstanceMetricSample` 补 `max_players_available`（proto 字段 26）由 Worker 填充 `maxPlayers`/可用位，CP 落 `inst_players_max` 时序（可用位为 false 时写 NULL 断点，**不做**在线人数那条 「探针/SLP 可用就回退」的兼容判定，因为上限并非三源必带，回退会伪造「上限 0 人」）；前端在同一批 `/metrics/series/batch` 里取回两者拼成 `16/50`，上限缺测时只显示当前人数。CPU / 内存移到悬停卡。另修「分组方式」按钮点选后残留黑框：原生 `<button>` 未套库内统一焦点环，菜单关闭焦点回位时露出浏览器默认 `outline`，已改用 `focusRing`；筛选项改为**选完即关**（原 `preventDefault` 保持打开，用户反馈「选择框不消失」）。⑤ **悬停卡补齐详情**：CPU / 内存 / 在线玩家 / TPS 四宫格 + 活动心跳带 + 节点:端口 / 类型·角色 / 标签 / 创建时间。⑥ **列表虚拟化**：新增 `flattenSidebarLines` 把「分段」与「文件树」两种渲染形态铺平成一条可虚拟化行序列，走 `useVirtualRows` 变高模式（服务器行 44 / 组头 26），行高常量 `SERVER_ROW_HEIGHT` / `GROUP_HEAD_HEIGHT` 与消费它们的 CSS 同源可测；行间距一律折进行盒，不用 `space-y-*`。⑦ **指标取数改到真实可见行 + 增量缓存**：此前接线层把整个窗口（最多 100 台）交给 `useInstanceWindowMetrics`，而该 hook 单次只取前 50 个目标，第 51 台以后**永远没有指标**；且每次窗口变化都整窗重取 1h raw 全量序列（devmock 实测 400~550KB / 次）。现在面板按虚拟 `range` 只把视口内的目标交给 hook，hook 改为「累积缓存 + 只请求缺失/过期目标」并新增 `refreshToken` 让同目标集合按 30s 节拍可刷新。**边界**：不改后端接口、不改 ADR-033 的 M:N 语义、不动 `/instances` 页的表格虚拟化。**验收**：① 侧栏主体无任何工具行，点击「更多」可跨区拖拽收纳（实测 `pinned=["/super"]`）；② 三个点无文字且位于开源协议上方；③ 行高实测 36px、行尾为「N人 / Ntps」两行、CPU·内存只在悬停卡；④ 悬停卡含 CPU/内存/玩家/TPS/心跳；⑤ 101 行数据时 DOM 只挂 24 行、行高累计零漂移（900+33×36+1512=3600=scrollHeight）；⑨ 分组方式按钮点选后无黑框残留、菜单立即关闭；⑥ 滚动平移时只对新进视口目标发请求（实测 2 台 / 37KB）；⑦ `tsc -b` 零错、`eslint .` 0 error、全量 2979 用例通过；⑧ 真机（dev:mock 5200）逐条过一遍。

**交付状态**：✅ 已交付（`go build`/`go vet` 干净、CP 全包测试通过、tsc 零错、eslint 0 error、前端 2982 + Go 新增 4 用例全绿；真机实测 32px 行高零漂移、行尾「18/50」、`inst_players_max` 与当前人数同批取回、侧栏 1h+5m 请求 575KB→128KB）

### FR-512 侧栏服务器行标注「服务端类别」三分法（feat，增强 FR-511，免 spec）

**需求补充**：运维视角只需三类——**插件服 / 代理 / 二进制**，不细分 Paper/Spigot/Bukkit（用户明确）。**动因**：现有两个维度都答不出「跑的是什么程序」——`type` 只有 minecraft_java/generic，`role` 描述的是群组拓扑角色（与「跑什么程序」正交，代理本身也可能以二进制部署），真正的依据 `coreType` 此前只写进 `provision_spec` 这段 JSON、列表响应不解析。**实现**：`Instance` 新增 `kind` 列（plugin/proxy/binary，带索引）+ `KindOrInfer()` 回退（`role=proxy`→代理、`type=generic`→二进制、其余→插件服）与 `KindForCoreType()` 映射（velocity/waterfall/bungeecord→代理，binary/beacon→二进制，其余 MC 核心→插件服；未知值归最常见的一类而非报错）；五条写入路径显式赋值（MC 搭建 / 代理搭建 / 二进制与 Beacon 预设 / 导入就地与搬迁 / 克隆继承源实例）。前端 `instance-kind.ts` 提供**与后端同口径**的回退推断（避免两条链各推断一次产生错位）。**显示**（用户在 10 套原型中选定「备注行 = 淡徽标 + 备注」）：类别徽标放在**侧栏第二行行首**，有备注时接 ` · 备注`——因为 `statusReason` 只有崩溃/异常才有值，正常实例第二行本来是空的，把类别常驻在此信息量最大且**完全不挤名称宽度**（实测名称仍 171px，与不标记时持平）。**边界**：不改 role 语义、不改群组拓扑、不引入新的筛选/排序入口。**验收**：① 三类在侧栏正确区分（真机：beacon-1=二进制 / creative-plot=插件服 / lobby-proxy=代理）；② 异常行显示「代理 · 进程异常退出（exit code 1）」；③ 旧数据无 `kind` 时按 role/type 回退，不显示空白；④ 前后端推断口径一致（各有单测覆盖同一组用例）；⑤ `go build` 干净、CP 全包测试通过、tsc 零错、eslint 0 error、前端 3001 用例全绿；⑥ 真机过一遍。

**交付状态**：✅ 已交付（后端 `kind` 列 + 5 条写入路径 + 前后端同口径回退；真机侧栏三类显示正确）

### FR-516 当前用户受限系统工具与逐调用写入确认（feat，增强 Agent/MCP，需 spec，依赖 FR-515）

**需求补充**：复用领域工具与原授权真源，补最小受限概览/任务/告警读工具；读取自动，全部写入冻结目标与参数并经人类确认，执行前重验 RBAC/资源归属，确认不提权；与 FR-490/491 的票据/执行规划对齐，不改变现有 jmat MCP 契约。验收：组外/无写权拒绝，未确认零副作用，过期/重复/改参确认不执行，工具结果与审计闭环，平台治理和秘密面不可绕过。

**交付状态**：✅ 已交付@v0.24.0 **此次真机抓出一个真实缺陷（已修）**：`targetForApproval` 把**非 node 目标一律当实例 id** 交给 `ResolveInstanceTarget`，而 `bot_send_command` 的 `id` 是 **Bot id**——Bot 2 属于实例 1，于是拿 2 去查实例、失败后回退 `{InstanceID: 2}` 被判越权，对外只剩笼统的「能力/scope 不足」，**平台管理员在自己完全可访问的实例上发 Bot 命令也必然被拒**。它长期潜伏的原因是测试/演示环境里 `bot.id` 与 `instance.id` 常常都是 1，恰好撞上就「碰巧通过」。修法：新增 `AgentTokenService.ResolveBotTarget` 按 Bot 记录解析所属实例与节点，`targetForApproval` 按 `ResourceType` 分派（bot/botrun 走 Bot 归属解析，解析失败返回**空目标**而不是退化成「把 bot id 当实例 id 蒙一次」）。回归用例 `TestApprovalIntercept_BotTargetUsesBotOwnership` 刻意让 `bot.id(2) ≠ instance.id(1)`，并含「scope 外实例上的 Bot 必须被拒」的反向断言；回退修复后该例转红且错误文本与真机一致。 **同 run 单张 pending 已强制**：`ux_assistant_confirmations_pending` 部分唯一索引（迁移含存量归正，脏库三启通过），行为层面本就成立（首个写调用出票即 return），索引作为将来改动的兜底。**四条边界已真机验证**（过期→410/票据 expired、改参→409/票据 invalidated、重复 approve→409 且副作用与审计各只一次、等待期撤权→409/票据 invalidated，均确认未执行），并以「注入缺陷→断言变红」反向验证；过程中发现「重复 approve」只看状态码测不出 CAS 失效（前置 status 检查会先挡住），判据须含副作用次数或审计行数）

### FR-538 供应商协议三选与模型自动发现（feat，增强 FR-514，免 spec 但需契约补测）

**需求补充**：①**协议在配置里显式选择**（`openai-chat` / `openai-responses` / `anthropic`）——适配器与前端下拉已存在，本项要把「选择后可真正跑通」补齐真机证据；②**从上游自动拉取模型列表**：新增 `POST /assistant/admin/providers/:id/discover-models`，按协议走各自的列表端点（OpenAI 系 `GET /models`、Anthropic `GET /v1/models`），把上游返回的 `id` 与本地已配模型做差集，前端在模型区给「从上游拉取」按钮并支持勾选导入（`contextWindow` 等能力由管理员确认，不从名称猜）。**动因**：现在只能手抄模型名，而模型名是易变的外部标识，手抄必错；用户明确要求「从上游自动拉取」。**边界**：发现失败不得影响已配模型；上游无列表端点时给出分类错误而非静默空列表。

**交付状态**：✅ 已交付@v0.24.0

### FR-539 供应商级出站代理（feat，增强 FR-514，默认直连）

**需求补充**：`assistant_providers` 增代理配置（`proxyMode` = `direct`/`env`/`custom`，`proxyUrl` 仅在 custom 时填写，口令部分复用凭据加密不入响应），**默认 `direct` 直连**（不继承进程环境代理，避免「本机设了全局代理导致 AI 出站被悄悄改道」）。每供应商独立构建出站客户端，与既有「出站准入（仅 https / 私网放行）」串联：代理只改变连到哪里，不放松准入判定。**验收**：直连不受 `HTTPS_PROXY` 影响；custom 代理生效且口令不回显；非法代理 URL 整单拒绝并点名；代理不可达时错误分类为 `upstream_unreachable` 而非 `egress_denied`。

**交付状态**：✅ 已交付·三模式（direct / env / custom）贯通全链路：`assistant_providers` 增 `proxy_mode`/`proxy_url`/`proxy_has_auth`（默认 direct，显式不回退环境代理）；`httpclient.Guard` 抽出统一 `clientWithProxy` 并新增 `ClientWithProxyEnv`/`ClientWithProxy`；`egressClientFor` 按模式产出客户端，代理只改「连到哪」不放松准入；保存期校验 scheme/host（custom 缺地址或非法协议直接拒绝并点名）；**地址与口令永不回显**，只回 `proxyHasAuth` 布尔；切回 direct 时清空旧地址（避免误切回 custom 静默启用废弃代理）。前端供应商表单加三选一 + 地址输入（编辑态留空=保持原值）。3 项新用例覆盖默认值/校验/回显边界

### FR-541 系统提示词、Skill 目录与思考强度（feat，需 spec）

**需求补充**：①`assistant_settings` 增 `systemPrompt`（可空，空则用内置默认），在 `/settings/ai` 可编辑，改动对**新 run** 生效（历史 run 不回溯改口径）；②**Skill = 目录 + `SKILL.md`**（参照 Vantaloom 的 CC 兼容格式）：`<dataRoot>/skills/<name>/SKILL.md`，YAML frontmatter 含 `name` / `description` / `whenToUse` / `version` / `source`，正文为指令；启动时扫描目录装载、只读官方目录 + 可写的本地目录两层，管理页可查看/启停，启用后按固定顺序拼进系统提示词并在会话中可见「本轮启用了哪些 skill」。**关键约束**：skill 只能**收窄**工具可见范围（`toolsAllow`），不能扩权——否则绕过 FR-516 的「写必确认」；skill 是纯文本，不执行任何代码；目录名即技能名，`SKILL.md` 缺失的子目录不算技能。③**思考强度**：模型配置增 `reasoningEffort`（`minimal`/`low`/`medium`/`high`，可空=不指定），按协议映射到上游参数（OpenAI 系 `reasoning_effort`、Anthropic `thinking.budget_tokens`），并在 `ai.Request` 增对应字段贯穿适配器。

**交付状态**：✅ 已交付（前后端）·真机验证（Skill 目录 + 系统提示词 + 思考强度）·①`assistant_settings.system_prompt` 已可读写并接入组装（顺序固定：内置基线 → 自定义 → 技能）；②skills 目录 `<dataRoot>/var/skills/<name>/SKILL.md` + 装载器 + 2 个官方示例 + 管理端点（列正文与 skipped 原因 / 手动重扫），真机日志可见 WARN 跳过坏技能，真实对话按 instance-triage 指令作答；③`assistant_models.reasoning_effort` 贯穿 `ai.Request` 并按协议映射（openai-chat 顶层字符串 / responses 对象 / anthropic thinking.budget_tokens 且自动抬高 max_tokens 避免被思考吃光），未知档位一律不发送。前端配置 UI 未做

### FR-528 对外 API 规范与错误码统一（docs，免 spec，契约

**需求补充**：`docs/openapi/external.yaml`，依赖 FR-522/523/524/527）：在 `docs/API.md` 建立「对外接口」章节，区分**对内 API**（用户 JWT，随版本可变）与**对外 API**（Agent Token，契约稳定）；统一错误码表：`INSTANCE_OFFLINE` / `PROBE_UNAVAILABLE` / `DOMAIN_UNAVAILABLE` / `ACTION_UNSUPPORTED` / `TIMEOUT` / `BEST_EFFORT` / `IDEMPOTENT_REPLAY` / `REPLAY_GAP` / `PATH_ESCAPE`。验收：文档含完整请求/响应示例；错误码在路由层有统一常量与测试；无未文档化的对外端点。

**交付状态**：✅ 已交付@v0.24.0

### FR-538 业务事件位点账本与缺口查询（feat，见 ADR-107，依赖 FR-522）

**需求补充**：业务台（Loom）要求在离线/重启后能精确定位漏了哪些事件，而「JM 不存业务数据」（ADR-106）禁止持久化 payload。两者不冲突——真正需要的是**投递可恢复**而非**内容持久化**：位点是传输元数据，payload 才是业务数据。新增 `business_event_cursors` 表，只存 `stream_seq`/`instance_id`/`domain`/`seq`/`dedup_key`/`occurred_at`/`created_at`，**明文禁止加 payload 列**。写入异步批量（默认 500 条或 1 秒）、队列有界（默认 10000，满则丢最旧并计数）、失败只记 WARN 且**不影响事件投递**。读侧新增 `GET /business/cursor?instanceId=&domain=&fromSeq=&limit=`，返回区间内存在过的位点（`limit` 默认 1000、上限 5000、按 `seq` 升序、无数据返回空数组而非 404），**只返回位点、不计算缺失区间**（JM 不知道调用方收到了什么，差集必须由调用方做）。保留期默认 7 天，后台每小时巡检按主键分批删除过期行，**查询不触发删除**。验收：扇出热路径不因写入器慢而阻塞（慢写入器下投递照常完成）；批量触发（满批/定时）与队列满丢最旧可验证；`Close()` 刷出剩余；写失败不 panic；缺口查询区间过滤/排序/limit 上限/空结果正确；裁剪只删过期行；`go build`/`go vet` 通过。

**交付状态**：✅ 已交付@v0.24.0

### FR-323 长操作任务化

**需求补充**：导入/克隆/备份纳入任务中心（增强 FR-302 导入 / FR-036 克隆 / 备份 FR，FR-319 同族收口——用户「把所有长耗时任务加到任务中心、不堵塞页面、有进度」）：抽公共 `run-as-task` 底座（CreateTask→CP 后台 goroutine→SetStage 阶段进度→MarkSucceeded/Failed→终态站内信），provision（FR-319）一并迁过去 DRY；导入 migrate 搬迁 / 克隆 CloneWorkDir / 备份创建 / 备份恢复 四个现同步阻塞或后台无进度的长操作接入——提交秒回 `{taskId}` 不阻塞（搬迁/拷贝/打包可达数十分钟），任务中心显示 stage 文案 + 终态 + 失败错误链；新增 TaskKind（import/clone/backup_create/backup_restore）+ 前端任务中心筛选/文案 → `docs/specs/long-op-tasks/spec.md`（需 spec：3 端点响应改 {taskId}、抽共享底座跨 3 service+任务模型+前端）

**交付状态**：✅ 已交付@v0.16.0

### FR-369 通用 Bot 命令编排与场景边界修订（feat，取代 FR-364，增强 FR-363/354）

**需求补充**：新增 `command_schedule` 相对时间命令、稳定 commandId/occurrence checkpoint、绝对计划时间、本地集中 scheduler、取消、固定有限重试和结果聚合；`send_command`/命令发送成功仅表示 `bot.chat` 未同步抛错，不等待服务器回执。ServerProbe、塔防插件、TPS/MSPT 与业务事件仅为独立可选观测，不改变命令结果、preflight 或默认 verdict；既有 Scenario V2/塔防预设保留为显式 legacy，真实业务事件数据源不作为本批交付前提（需 spec + ADR-075，依赖 FR-362/352，可与 FR-365 非依赖部分并行）→ `docs/specs/bot-load-probe-events/spec.md`、`docs/specs/bot-load-platform/super-spec.md`

**交付状态**：✅ 已交付@v0.20.0·2 Bot 编排闭环 + 500 物化/10×50 批次/checkpoint；单 Worker 全员 connected 受信号量限制

### FR-370 通用命令压测运行状态机、负载曲线与自动判定（feat，取代 FR-366）

**需求补充**：模板冻结 commandSchedule/loadProfile/thresholds；支持 stable/step/spike，聚合在线、命令发送、调度 lag、可选屏障和 Worker 健康，输出 verdict、maxStableBots、失败分类与 JSON/CSV 报告。默认适用指标均 ≥99%、schedule lag p95≤1 秒、非预期 crash=0；无屏障指标为 not_applicable，TPS/MSPT/业务事件不进入默认判定（需 spec，依赖 FR-362/352/354/358）→ `docs/specs/bot-load-runner/spec.md`

**交付状态**：✅ 已交付@v0.20.0·metrics/SSE/projection 真机 + harness 源码；残余：未跑满 500×60min harness

### FR-475 日志 Multiline 与归一化（feat）

**需求补充**：Java/MC 多行归并；level/time/logger/instance_id；Multiline 上限/超时/半条恢复；时区跨午夜；解析失败保留原文+状态；parser_version；**事件数 vs 行数**口径 → `docs/specs/worker-log-normalizer/spec.md`

**交付状态**：🔨 开发中·**真机验收通过**（真 VL v1.52.0：Multiline 5 行归并为 1 事件、跨午夜回拨、半条事件 Flush 闭合、账本 ready；**修复损坏编码致 canonical hash 与 VL 正文不一致的缺陷**——净化 + `encoding_sanitized` 审计标记，VL 侧重算 hash 一致）；7 条新回归 + 2 项变异验证；Windows 平台证据待补；**2026-10-02 缺陷加固（GBK 乱码 / 时间戳偏移）**：按源字符集解码（auto 判据拒绝替换字符、`source_charset` 审计）与按源时区解释 `[HH:MM:SS]`（`TimeZone`/节点默认、内嵌 tzdata、`event_time_zone` 审计；节点级默认的配置面 `log_ingest.time_zone`——默认留空=UTC 零行为变化、非法值启动即拒）；转红回归见 CHANGELOG [Unreleased] 与 `worker-log-normalizer/spec.md` §3.3/§3.4

### FR-481 日志入库切换与 Legacy 只读（feat）

**需求补充**：停止**实例与 Worker/Node**日志新入 CP `logs`；platform/audit + **LegacyLogReader**；Legacy 独立保留预算与到期规则；容量预检不足不切换；逐 Worker 切换水位；控制台订阅与持久化推送分离；历史 Multiline 可追溯原文；未统一口径禁止拼精确趋势 → `docs/specs/log-ingest-cutover/spec.md`

**交付状态**：✅ 已交付@v0.24.0·地基/接线·cutover+Legacy+DualPath + **HTTP 管理面已注册**（默认关闭）；Legacy/cutover 行为由 service/router 单测覆盖；DualPath federated 侧由 `/logs/federation` 承担（无生产调用方，保留 API 不加投机接线）

### FR-485 日志平台可观测性与告警（feat，增强 FR-474/484）

**需求补充**：把「单源洪流拖垮 Worker」这类事故的信号变成平台自己能看见——新增指标：state 文件尺寸与增速、persist 写字节/秒、单源 WAL 积压条数与暂停时长、投递失败率与分类、校验退避次数、慢 SQL 计数与最长耗时；经 Worker 上报（proto 扩展）→ CP 落库 → 告警规则联动（metric 触发 + FR-462 动态基线做增速突升检测），并附推荐阈值与处置手册入口。**动因**：2026-09-28 生产事故（ingest.state.json 涨至 1.2 GB、117 MB/s 持续重写 25 小时、iowait 91%、CP 慢 SQL 0.4–2.4s）**完全靠人肉排查发现**，平台当时对这些信号一无所知、无法自行告警。**范围按实证扩大**：节点层同样缺关键信号——**iowait 未被采集**（事故中最刺眼的 91% 无法告警）；node_disk_used/node_mem_used 以**字节**存储、无比率触发，「磁盘 >85%」类绝对阈值当前**无法表达**。故本 FR 一并补齐 iowait 采集与比率触发，**告警规则并入本 FR 一次配好**

**交付状态**：🔨 部分交付@v0.24.0（**仅采集健康指标链路已落**：#49 落地 `log_ingest_healthy` 经 Worker 心跳上报 → CP 落库 → 内置告警触发联动；其余范围——state 文件尺寸与增速、persist 写字节/秒、单源 WAL 积压条数与暂停时长、投递失败率与分类、校验退避次数、慢 SQL 计数，以及**节点层 iowait 采集与比率触发**——**均未实现**）

### FR-491 审批执行器与执行回执（feat，依赖 FR-490）

**需求补充**：批准 → 持久执行器认领 → 领域 adapter 在事务内执行 + 写执行回执 + 强审计；执行前重校验目标漂移与资源版本，漂移即 `failed` 且不执行；重试幂等，失败保留批准事实与失败原因。**执行器与既有异步机制的关系须在 spec 明确**：优先复用 `TaskService.RunAsync`（FR-441 已用其承载异步任务与阶段进度）而非另造一套调度，避免出现两条互不相识的异步链路

**交付状态**：✅ 已交付·**执行器 + 回执 + 两道重校验均生效**：`RunAsync` 承载长操作、payload 哈希重校验、执行回执（`ok/failed/unverified/in_flight`）已落，双审计（`approval.approved` / `approval.executed`）真机可查。**本轮修掉两处「有代码但不生效」**：① `SetTargetVerifier` 此前只有定义、全仓无调用点，目标漂移校验恒跳过——新增 `service/approval_target_drift.go` 并在 `main.go` 装配，出票时经 `CaptureTargetSnapshot` 冻结快照；② `version` 原以数字写入 evidence，而纳秒值（约 1.8e18）超出 float64 精确整数上限 2^53，JSON 往返必被舍入 → 「目标没变」也判漂移、每张票执行永久失败；改为十进制字符串承载（不退回毫秒：毫秒会把同一毫秒内两次真实写更新误判为「没变」，属安全漏洞）

### FR-492 高危 MCP 工具接入审批（feat，依赖 FR-490/491）

**需求补充**：**登记方式 = 以 `agentActionCatalog` 为准穷举，不按操作类型直觉列举**，且**必须按 action 粒度标记而非按 `Operation` 分类**（`instance_start` / `stop` / `restart` 同属 `write`，但本项只要求 `stop` 进审批，按分类无法表达该差异）。**已知必须覆盖的三类绕过口子**：①`file_issue_transfer_ticket`——签发票据后走流式上传即可写盘，是绕过 `file_write_text` 的完整路径；②`instance_send_command`——控制台命令等价任意代码执行，当前连精确确认都没有；③`instance_update_config` 改 `startCommand`、`instance_binary_upgrade` / `instance_binary_rollback`、`provision_server` 的远程 URL 二进制来源——三者均等价「下载并执行任意二进制」；`instance_snapshot_rollback` 覆盖既有数据，同样纳入。服务层失败关闭：MCP 调用只落票据返 `requestId`、**零副作用**；`start` / `restart` / 配置热重载保留直执 + 现有精确确认；同步更新 agent-safety-gate 契约表（CLI / MCP / curl 三入口同策略）。**`approval_required` 与既有 `Operation` 的关系须在 spec 定义**（正交审批标记 or 第四种 Operation，不得两者兼有）

**交付状态**：✅ 已交付·**19 条登记表 + 三入口同策略**：登记表为代码常量（`approvalRequiredActions`），PRD 点名的三类绕过口子全部在内，18/19 已真机闭合（`bot_send_command` 待 Bot 记录）；漏登记由 `mcp` 包覆盖门禁守（新增危险词根工具未登记即 CI 失败）。**本轮补齐三入口同策略的真实缺口**：审批原先只接在 MCP `CallTool`，`POST /agent/instances/:id/stop`（curl/CLI）直接执行——同一个 Agent Token 换 transport 即可跳过审批；新增 `AgentOpsHandler.gateApproval`，HTTP 以 **202 Accepted** + `requestId` 承载同语义，映射规则只有「去掉 `agent.` 前缀后必须命中登记表」一条（不猜、不模糊匹配），未装配审批时两条入口都失败关闭。`docs/specs/agent-safety-gate/contract.md` 已补 §0 / §2.1 / §6（此前 `approval` 出现 0 次）

### FR-540 助手默认启用平台自带 MCP 工具（feat，增强 FR-517，免 spec）

**需求补充**：把「本平台自己的 MCP 网关」作为一种**内置连接**接入助手工具面并**默认启用**，免去管理员手工配一条指向自身的连接。**关键约束**：现有自环检测 `MCP_SELF_LOOP` 是为了拦「管理员误把外部连接指向本平台」，不能简单放开——内置连接走独立通道（进程内直接调用工具注册表，不经 HTTP 回环），因此既没有回环风险，也不需要伪造 Agent Token。工具可见性沿用既有规则：只读自动执行、写/未知走逐调用确认。**动因**：用户要求「默认启用本项目自带的 MCP 工具」；否则装好助手后工具面是空的，得先手配自环连接（且会被自环检测拦住）。

**交付状态**：✅ 已交付（前后端）·真机验证·采用**进程内直连**（非 HTTP 回环，故不触碰 FR-517 的自环检测）：`assistant_settings.builtin_tools_enabled`（默认启用、`*bool` 以区分「未配置」与「显式关闭」），关闭时**连 system_* 一并不可见**（否则「关了一半」比没开关更危险）。真机实测：关闭后本轮 `tool_call` 事件为 0，模型如实回答「工具不在清单里」且不编造数据；技能仍生效（二者正交）

### FR-542 对话历史独立页面与检索（feat，增强 FR-515/519，免 spec）

**需求补充**：新增 `/assistant/history` 页面——列表按时间倒序、支持**关键词检索**（标题 + 消息正文，服务端 `q` 参数、ILIKE/`LIKE` 转义、分页游标）、按会话打开详情、可继续对话。后端 `GET /assistant/conversations` 增 `q`（服务端搜索，不在前端过滤全量）。**性能约束**：消息正文检索走 SQL `LIKE` 并对 `conversation_id` 建索引；结果只回摘要不拉全部消息体。

**交付状态**：✅ 已交付（前后端）·真机验证·`GET /assistant/conversations?q=` 服务端检索（命中标题或未取代消息正文、LIKE 通配符转义、归属条件外层与子查询双重限定、游标分页）。真机实测：搜「技能」命中正文会话、搜「1024」命中标题、无匹配返 0 条。前端历史页未做

### FR-543 历史消息编辑与回退（feat，增强 FR-515，需 spec）

**需求补充**：①**编辑**：允许对历史里**用户消息**做更正，生成新版本并保留原文本（`assistant_messages` 增版本/被取代标记），编辑后该消息之后的分支标记为「已废弃」；②**回退**：把会话回退到某条消息（丢弃其后全部消息与 run 引用），回退前**必须**确认（不可逆），并在审计留痕（`assistant.conversation.rollback`，记录 from/to 消息 id 与其文本摘要）。**关键约束**：不得物理删除已执行过工具调用的消息对应审计；回退只影响会话可见内容与后续上下文，**不撤销已发生的副作用**（这点必须在 UI 明说，否则用户会以为回退等于撤销了启停服等操作）。

**交付状态**：✅ 已交付（前后端）·真机验证·①编辑 `PATCH /assistant/messages/:id`：只允许自己的 user 消息、首次编辑保留 `original_content`（不覆盖历史栈）、不自动重跑；②回退 `POST /assistant/conversations/:id/rollback`：软删（`superseded_at`）不物理删除以免丢失审计线索、有活跃 run 时 409、按 `(turn,id)` 定序只影响目标会话。真机实测：编辑保留原文并 edit_count=1、回退 superseded=1 且消息仍可查、编辑 assistant 消息返回 `MESSAGE_NOT_EDITABLE`（修掉了原先误落 500 的映射缺口）。前端 UI 未做

### FR-289 JDK 安装 arch 别名归一化

JDK 安装 arch 别名归一化（fix，增强 FR-033/178）：`POST /nodes/:id/jdks/install` 的 `arch` 为自由字符串直透下载源解析，传 `amd64` 时 adoptium 不认（只认 `x64`）→ 下载 404 且错误不指向真因（真机复现：temurin 17 `arch=amd64` → 「下载返回 HTTP 404」，同版本 `x64` 成功）；面板固定送 `x64` 不受影响、API/自动化调用方中招。CP 侧入参归一化常见别名（`amd64`→`x64`、`arm64`→`aarch64`），未知 arch 直接 422 拒绝而非透传出 404（免 spec）

### FR-292 删除托管 JDK 清理到托管根顶层目录

删除托管 JDK 清理到托管根顶层目录（fix，增强 FR-033/228）：删除走 NodeJDK 登记的**嵌套探测路径**（归档外层多包一层，如 `temurin-11/jdk-11.0.31+11`），Worker `RemoveAll` 只删内层、父壳空目录留在托管根下占位（真机复现：删 11 后 `temurin-11/` 空壳仍在，需手动 rm；症状虽被 FR-291 自愈兜住，删除本身应清净）。Worker Remove 把托管根下的目标路径归一到**顶层子目录**整体删除；托管根本身（`rel="."` 现可穿过校验直达 `RemoveAll(根)`，一并堵死）与根外路径拒绝（免 spec）

### FR-307 全局运行时包可视化管理

全局运行时包可视化管理（feat，需 spec 并入 node-package-management，依赖 FR-306）：面板管理节点上的**全局 npm 包**（`<pm> ls -g --json` 解析已装列表）——手动装/删/更新/查版本；装进节点运行时全局目录（PM global prefix，落托管区 `<数据根>/opt/runtimes/global`），装/更新经任务中心异步（慢操作）+ 停滞看门狗/失败引导语义复用（FR-290/279）。前端全局包管理页（列表+搜索+增删改+可更新标记），走已配 registry/镜像 → `docs/specs/node-package-management/spec.md`

### FR-317 内存水位启动守卫

内存水位启动守卫（feat，真机事故钓出：验收开 Paper -Xmx2048M 把 8G 主机 OOM 至用户态僵死、SSH/面板全失联）：双闸防「启动实例把节点内存跑满」——① **Worker 实时闸**（真源）：StartInstance 前读系统可用内存，估算实例需求（启动命令解析 `-Xmx` + JVM 开销，解析不到用保守默认），可用 − 需求 < 保留水位（worker.yml `memory_guard.reserve_mb`，默认 max(512MB,总内存10%)）即拒绝启动并返回可操作错误；② **CP 预警闸**：启动请求先按节点最近心跳（总内存/已用）预判，不足直接 400 不下发 RPC，心跳过旧（>90s）放行交 Worker 实时判。批量启动并发预留列为后续增强（免 spec）

### FR-325 Worker reconnect 失败孤儿进程接管兜底

Worker reconnect 失败孤儿进程接管兜底（fix，FR-310 孤儿评估缺口 (a)，ADR-003 守护链路；真机事故实证：FR-277 主机实例 1-86876de3 启动报 Paper `session.lock already locked`——前次运行的孤儿 java 仍活着占世界锁）：Worker 重启接管扫描中 wrapper 存活但 reconnect 拨号失败时，现只删 PID 文件（`manager.go` reconnect 失败分支），活进程从此永久失联。改为保留 PID 文件重试有界次数，仍失败则按 PID 记录的 WrapperPID 杀进程树并记日志，杜绝孤儿永久化（免 spec）

### FR-334 控制台品牌顶栏贯通（T 型外壳）+ 页眉节点作用域下线（feat，增强 FR-162/181/268 控制台外壳，用户走查「侧栏与页眉交界处错位成台阶、页眉节点作用域下拉几乎无作用」）

控制台品牌顶栏贯通（T 型外壳）+ 页眉节点作用域下线（feat，增强 FR-162/181/268 控制台外壳，用户走查「侧栏与页眉交界处错位成台阶、页眉节点作用域下拉几乎无作用」）：外壳改 T 型——整宽顶栏（品牌区 + 面包屑 + 操作区）在上、其下「侧栏 + 工作区」一行；品牌区宽度与侧栏同步、右缘对齐连线，顶栏下缘=侧栏上缘，消除原侧栏 logo 区与页眉两条错位分割线的交界台阶；logo 折叠触发器（FR-181）迁入顶栏品牌区。删除页眉节点作用域下拉（`selectedNodeId` 数据面保留，全部服务器页自带节点筛选/命令面板/向导预填照常）；顶栏高度与 logo/搜索框尺寸下调（见 ADR-071，免 spec）

### FR-363 可验证 Bot 场景引擎与塔防预设

可验证 Bot 场景引擎与塔防预设（feat，增强 FR-274）：结构化场景支持按比例分组、固定随机种子、同步屏障，以及 `wait_spawn` / 主城区域漫游 / `send_command` / 等待探针事件 / `move_to_and_wait` / 锁敌追击 / `attack_until` / 死亡重生与重新进房；每步具备超时、重试、取消和成功/失败结果，兼容代理跨服与单服多房间。塔防预设支持「主城行走→命令进房→同步开局→抵达区域→持续普通攻击」；首版不含技能、放塔、商店和复杂背包（需 spec，依赖 FR-362）→ `docs/specs/bot-scenario-engine/spec.md`

### FR-384 Agent Token 模型 + CP 侧鉴权

Agent Token 模型 + CP 侧鉴权（feat）：`agent_tokens` 表 + `POST/GET/DELETE /api/v1/agent/tokens` 颁发/吊销/列表 + `agent_token` 中间件（独立于人类 JWT）+ 策略引擎真源（默认只读 + 写白名单 + scope 收敛 + 硬拒绝面）+ 审计 `actor_kind=agent` 贯通；CP 为唯一真源，jm-agent/mcp-bridge/curl 三入口共享同一闸（需 spec + 新 ADR-079，跨 middleware/service/router/model）→ `docs/specs/agent-token/spec.md`

### FR-392 退役 mcp-bridge + jmagent 客户端标识

退役 mcp-bridge + jmagent 客户端标识（feat/docs）：删除 `apps/mcp-bridge` 与 stdio 文档路径；README/接入指引改为 CP MCP URL；`jmagent` 全局发送 `X-JM-Agent-Client: jmagent`；构建/CI/发布物去掉 mcp-bridge；更新 FR-388 契约与 ADR-077 状态为 superseded by 改写版（需 spec 或并入 FR-389 退役章；依赖 FR-389 主路径可连后执行，可与 FR-391 尾段并行）→ 退役说明可落 `docs/specs/cp-mcp-server/spec.md` 退役章

### FR-444 从 Beacon 拉取拓扑并映射为分组树

从 Beacon 拉取拓扑并映射为分组树（feat，见 ADR-090）：读取 Beacon 的区服结构（BC 集群 → 大区 → 小区）与 server 归属，映射为 JianManager 实例分组树（FR-165）并为实例补 `region:`/`zone:`/`role:` 标签（FR-440）。首次拉取为手动触发，避免自动建树与人工分组冲突；Beacon 不可达时返回明确错误且**不改动本地任何数据**。**真机验收通过**：空树 → 拉取创建 4 个分组（BC 集群→大区→小区 三层层级正确落库），本地不存在的 server 被跳过并列 `skippedServers`；二次拉取幂等（`createdGroups=0`）；审计落 `instance.beacon_pull_ok`；不可达时本地数据零改动

### FR-455 重启韧性加固

重启韧性加固（fix/feat，缺陷修复 FR-325/436 同源，见 ADR-093）：①接管重试仅 3 次（约 7s）失败即 `reapOrphanWrapper` 强杀 wrapper+Java，会**误杀本可接管的健康服务器**——改为更长退避 + 存活复核（cmdline 校验）后才处置；②重推仅隧道 `n==1` 触发，CP 重启后可能漏推——补多触发点与幂等；③正向对账以心跳清单为唯一真源导致状态误判——收敛真源。**验收**：真机重启 CP/Worker 后已运行服务器**零误杀、状态不误判、重推不漏**；FR-436 的 wrapper 死/Java 活分支**真机验证通过**

### FR-385 jm-agent 管理 CLI（feat，仿 jmctl）

jm-agent 管理 CLI（feat，仿 jmctl）：独立 Go 二进制 `apps/jmagent/`，仅引入 HTTP 客户端与标准库，不链 gRPC/DB；子命令 MVP = `whoami/list nodes/list instances/instance status metrics logs/start stop restart/node maintenance enter leave`，写白名单仅含 `instance.life`+`node.maintenance`；非交互 `--output json` 给脚本/CI；403/硬拒绝文案稳定；Token 仅 env 与 `--token` 传入不落盘（需 spec + 新 ADR-079，依赖 FR-384；建议并行 worktree）→ `docs/specs/jm-agent-cli/spec.md`

### FR-389 CP 内嵌 MCP 长连接服务（feat，取代 FR-386 远程入口形态）

CP 内嵌 MCP 长连接服务（feat，取代 FR-386 远程入口形态）：Control Plane 提供 **Streamable HTTP（主）+ 兼容 SSE** MCP 端点；鉴权复用 Agent Token（FR-384）；工具集与 jm-agent 对齐、硬拒绝不注册；**可运维会话**：列表（token 名/前缀、IP、已连时长、最近 tool、传输类型 streamable_http sse）、强制踢线、空闲超时、绝对超时、每 Token/全局并发上限；会话态内存为主（单 CP 进程，HA 粘滞不在本期）；平台管理员 API 管理会话（需 spec + **改写 ADR-077** 为「CP 内嵌 MCP 网关」；依赖 FR-384）→ `docs/specs/cp-mcp-server/spec.md`

### FR-390 Agent 调用流水与 Token 活跃（feat）

Agent 调用流水与 Token 活跃（feat）：统一 `agent_call_logs`（或等价）记录 MCP tool 与 Agent HTTP API（含 jmagent）的 action、tokenId、client（`X-JM-Agent-Client`：mcp jmagent curl unknown）、IP、成功/失败、耗时、时间；读路径也记；保留窗口可配置（默认建议 7~30 天）；Token 鉴权继续 bump `lastUsedAt` 并提供 24h 调用次数聚合只读字段/接口；写操作可仍写既有 audit 并在 detail 标明 actor（需 spec，依赖 FR-384；可与 FR-389 并行）→ `docs/specs/agent-call-log/spec.md`

### FR-528

对外 API 规范（OpenAPI）

✅ 完成（2026-10-25）：`docs/openapi/` 8 个分文件契约（external + 4 paths + 3 components）+ `scripts/check-openapi.sh` 三段校验（YAML 语法、$ref 可解析、与 Gin 路由双向一致）。**15/15 路由完全一致**。`docs/API.md` 新增「对外 API 总览」「业务事件流」「业务通道约定」「桥状态」「分层配置与纳管」五节。**不生成任何代码**。

## PRD 索引段迁入（2026-10-26）

### FR-521～528（2026-10-23 业务能力退役与对外通道重构）

FR-521～528（2026-10-23 业务能力退役与对外通道重构）：**第五期 JBIS 业务域整体退役**（FR-115~127 全部标退役），JM 不再持有任何业务语义与业务数据表；保留并规范化「纯透传通道」——业务事件经 SSE 原样转发、批量命令、写穿与离线禁改、对账协调、桥能力接入（订阅/reload/状态/分片）、只读统计契约、对外 API 规范。用户已确认：**存量业务表直接删除不备份**；事件缓冲按 **100+ 实例规模**设计（30s 窗口或 20 万条双阈值）。依赖：521→522→523→524，522→525，526/527/528 可并行。**边界铁律**：JM 不理解任何业务语义——不定义动作名（含快照动作）、不规定 payload/output 字段、不解析 manifest 内容；对账只做「拉取并透传」，差异由业务台计算。退役决策见 **ADR-106**（104/105 已占用，顺延）。**跨仓依赖**：事件序号（seq）能力由 ServerProbe 侧提供，按其仓库自身 FR 编号登记，本仓不占用编号。

### FR-529～533（2026-10-23 配置能力从 Beacon 迁入 JM）

FR-529～533（2026-10-23 配置能力从 Beacon 迁入 JM）：**Beacon 的配置中心与交付编排整体迁入 JM，迁完后 Beacon 配置功能完全禁用**。JM 现有底子：`InstanceConfigVersion` 版本链（hash/content/双向回滚引用）、`ConfigBaseline` + `DetectDrift` + `Converge`（批量收敛雏形）、`config_patch` 按字段补丁。缺口：**无层级继承**（只有单实例维度）、**无分批灰度**（Converge 是循环逐台）、**无生效确认**（写完不触发 reload）。五条 FR 补齐：分层覆盖（529）→ 落盘校验（530）→ 分批灰度（531）→ 生效编排（532）→ Beacon 侧禁用与数据迁移（533）。依赖链严格串行，531 依赖 529/530，532 依赖 530/531，533 最后。**配置真源唯一**：迁移期 Beacon 只读、JM 为唯一写面，禁止双写。

### FR-534～535（2026-10-23 业务插件可视化与批量粒度）

FR-534～535（2026-10-23 业务插件可视化与批量粒度）：FR-534 让 JM 渲染「本实例探针注册了哪些业务域及健康状态」（数据来自 ServerProbe 的 `core.business_status`，JM 只渲染不解释语义，不建业务表）；FR-535 把批量命令目标扩展到分组多选（`groupIds[]` + `includeDescendants`），并引入两阶段「预览 → confirmToken → 执行」与风险分级（跨大区强制确认、超 50 实例强制确认、`DESTRUCTIVE` 动作要求填原因）。**三仓分层**：JM 只做通道与渲染，ServerProbe 提供数据面与规格声明，Beacon 保留路由控制面。

### FR-433~439（2026-09-20 实例角色/群组拓扑 MCP 能力与压测稳定性修复批）

FR-433~439（2026-09-20 实例角色/群组拓扑 MCP 能力与压测稳定性修复批）：FR-433 实例角色可变更与 `beacon` 角色 / FR-434 群组服与拓扑 MCP 工具 / FR-435 实例分组 MCP 工具 / FR-436 daemon 孤儿 Java 接管 / FR-437 容量快照陈旧 / FR-438 bot-worker 事件循环过载 / FR-439 MCP 会话并发上限——**七项均免 spec**（改动集中且边界清晰：角色枚举扩展 + MCP 工具补齐 + 四项缺陷修复，随批交付）

### FR-473~484（**Worker 日志平台底座** 2026-09-20）

FR-473~484（**Worker 日志平台底座** 2026-09-20）：正式版完整底座——采集/处理/持久/HOT VL/COLD Lifecycle/Deep+Rehydrate/Worker 层内联邦/CP 跨节点联邦/停实例日志入 CP/日志中心改造。能力齐套、三层按配置启用；Paper 默认 FILE_PRIMARY；CP 仅 platform+audit+Legacy 只读。**不做**：CH/Loki 实现、日志告警引擎、浏览器直连 VL。架构审核：范围与 FR 划分保留。**门禁重分类（2026-09-20）**：FR-473 冻结=「契约与治理可开工」——退出条件为 Shared Contracts 齐备 + 依赖层证据（VL 行为/supervisor-equivalent smoke/纯逻辑）+ **ADR-094 accepted** + **proto/能力协商终审** + **VL 资产正式审批** + **性能判据方法与初始阈值入契约**；**Worker 生产组件级集成测不是冻结前置**（否则与「冻结前不实现 475+」死锁），改为 FR-474（Runbook A WAL/恢复分段）/ FR-476（Runbook C supervisor wiring）/ FR-477（Runbook B Catalog 崩溃与旧 owner 过滤）的开发验收条件。计划见 `.tmp/brainstorm-worker-log-platform-foundation-2026-09-20.md` 与 `.tmp/fr433-contract-freeze-checklist-2026-09-20.md`（均不入库）→ FR-473～484 见 §4（状态：**FR-473 契约已冻结**；FR-473~484 **开发中·地基/接线**；478/482 发布必需）；FR-483 docs **开发中·文档对账**

### FR-502~507（**前端组件库与导航外壳重构** 2026-10-02，重构批，增强 FR-273 组件包 / FR-431~432 控制台导航 IA）

FR-502~507（**前端组件库与导航外壳重构** 2026-10-02，重构批，增强 FR-273 组件包 / FR-431~432 控制台导航 IA）：设计底座单一真源与 `dark:` 变体修复 / 主题配色对比度自动校验 / 内容页布局规范层 / 导航外壳组件层 / 工作区导航数据层 / 主控台外壳接入——**六项均免 spec**（改动收敛在组件库与外壳：token 迁移 + 测试门禁 + 组件层新增 + 导航数据新增，不引入新进程 / 新协议 / 新数据模型 / 新 API；PRD 行内验收即门禁）→ 状态见 §4；**不新增 ADR**（沿用既有 Tailwind v4 + CSS 变量 token 体系与 monorepo 布局，见 ADR-032 / ADR-055~057 / ADR-064，无新架构决策）。依据设计方《资源优先工作区》提案（7 图 + 交互原型 + 45 路由对照表，过程材料不入库）

### **对外 API 契约**（FR-528）

**对外 API 契约**（FR-528）：`docs/openapi/` 下 OpenAPI 3.1 分文件契约（`external.yaml` 入口 + `paths/{business,stream,stats,bridge}.yaml` + `components/{common,errors,schemas}.yaml`）。**只覆盖对外 API**（15 个接口，Agent Token 鉴权），对内 200+ 接口仍用手写 `docs/API.md`。**不生成任何代码**（无 Go stub、无 TS 类型），仅作文档与校验。`scripts/check-openapi.sh` 做三项校验：YAML 语法、`$ref` 可解析、与 Gin 路由双向一致。

### BUG-A 节点重名覆盖（修 FR-004 注册身份匹配缺陷，见 ADR-039）

BUG-A 节点重名覆盖（修 FR-004 注册身份匹配缺陷，见 ADR-039）：注册改 UUID 锚定三级匹配 + 节点名活跃唯一 + 坏节点检测/修复后端（`NodeRepairService` + `/nodes/repair/*`、`/nodes/:id/reenroll|orphans|purge-orphans`）随 ADR-039 fix 提交落地；坏节点修复**可视化入口**随 FR-177 节点页重做。属缺陷修复（非新 FR），不占 §4 FR 编号

### FIX-验收批一（2026-07-15 mock 整站验收 ~50 条源码分诊后的交互质量修复，fix 走 sdd-fix-bug，不占 §4）

FIX-验收批一（2026-07-15 mock 整站验收 ~50 条源码分诊后的交互质量修复，fix 走 sdd-fix-bug，不占 §4）：①危险操作补确认（群组成员移除/安全组删除/进入维护）+ DangerConfirm 在途禁用；②三页 Tab 态 URL 同步（频道工作台/安全中心/分发监控）+ line 激活态加固；③表单必填错误改触碰/提交后展示（useFieldGate 门控，补 FR-072 缺口，接线 ~11 弹窗）；④登录/刷新防重复 + 用户/组写操作成功失败 toast（补 FR-232 缺口）；⑤设置数值项客户端校验（与后端 validateSettingValue 一致）；⑥节点搜索清右栏 + 定时卡片真实下次执行（补 FR-153 缺口）；⑦监控图例限定名 + 对比图 Y 轴按量纲格式化（补 FR-221 缺口）；⑧3 模态改自适应壳（ui-modals 合规）；⑨空态/骨架/白名单错误重试 + SHA 截断复制/配额 GB/SK 提示/管理页路由守卫；⑩devmock 保真（JDK 删除 409 / 日志排序）。9 条报告误报驳回 + 3 条待真机复验，订正台账见 `.tmp/acceptance/acceptance-corrections-2026-07-15.md`

### **FR-445~470（生产就绪查漏补缺批 2026-09-21）**

**FR-445~470（生产就绪查漏补缺批 2026-09-21）**：P0 重启韧性/孤儿进程 + P1 集群运维与观测 + 实例详情多形态适配与 MC 直探 + P2 增强。**依赖序**：`FR-445→448/449/450`、`FR-445→452→453`、`FR-446→447`、`FR-451→446/458`、`FR-455/456/462→459`（其余相互独立）。**实现分阶段**：批 1 并行为 P0 韧性（455/456）+ P1 运维（457/458/459/460）+ P1 观测（461/462）+ 界面采集（445~454）；批 2 依赖批 1（453、459）；P2（463~470）最后。ADR：091（能力画像）/ 092（配置源与直探）/ 093（进程韧性）。

## PRD 索引段状态记录迁入（2026-10-26）

### FR-362/363/365/369~372（500+ Bot 分布式命令压测）

FR-362/363/365/369~372（500+ Bot 分布式命令压测）：FR-362/363 已交付@v0.19.0；FR-365/369～372 已交付@v0.20.0（500 物化/分片与 2 Bot 真连闭环；单 Worker 全员 connected 与 60min 长稳 harness 为残余，见 CHANGELOG）

### FR-384~388（Agent 安全操控后台）

FR-384~388（Agent 安全操控后台）：FR-384/385/387/388 已交付@v0.20.0；FR-386 ❌ 已废弃（由 FR-389/392 取代）

### FR-389~392（Agent MCP 长连接运维 + 调用观测）

FR-389~392（Agent MCP 长连接运维 + 调用观测）：✅ 已交付@v0.20.0（CP 内嵌 MCP + 调用流水 + 观测 UI + mcp-bridge 退役）

### FR-373~378（文件权限底座 + 资源管理器批次）

FR-373~378（文件权限底座 + 资源管理器批次）：✅ 已交付@v0.20.0

### FR-393~394（节点离线归档与彻底清理）

FR-393~394（节点离线归档与彻底清理）：✅ 已交付@v0.20.0

### FR-425~429（客户端分发观测增强批）

FR-425~429（客户端分发观测增强批）：**实现已随 FR-430 运维页落地**（时间筛选/洞察卡/热力图/布局等并入 `/client-dist-ops`）；FR-426/428 独立 spec 仍待补。✅ 已交付@v0.22.0


---

## 附：PRD §4 需求列精简前原文（2026-10-31 迁移）

> PRD §4 的需求列已精简为「标题（目的）」，实现细节按 `.claude/rules/prd-authoring.md` 迁到此处保留原文。
> 以下各条为**精简前的完整原文**，未改写。这些 FR 无独立 spec，故详情落在本文件。

### FR-173 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.11.0·四档验收）

CI/CD 发布管线（GitHub Actions：push→滚动预发布[取 CHANGELOG Unreleased]、tag→正式 release[取版本段]；交叉编译 CP+Worker linux/amd64+windows/amd64 含 go:embed 前端；产物 + checksums.txt 上传 release；ldflags 注入版本，见 ADR-036）

### FR-174 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.11.0·四档验收）

出站网络代理（CP/Worker 每进程可配 HTTP/SOCKS5 + no_proxy，共享出站 HTTP 客户端工厂，覆盖自更新/JDK/服务端 jar/CFR/GitHub API 下载；留空=直连，见 ADR-037）

### FR-175 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.11.0·四档验收）

自更新对接 GitHub Releases（增强 FR-081：原生 GitHub Releases API 解析 + stable/prerelease 渠道 + checksums sha256 校验 + 经 FR-174 代理下载；取代 ADR-020 §4 feed 立场，见 ADR-036）

### FR-178 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.11.0·四档验收）

节点内 JDK + 制品管理面板（抽屉取代简陋模态 + JDK 异步安装/进度日志接任务中心 + foojay 多厂商多版本 + 目录选择器 + 抽屉 UX 约束 + 节点制品缓存秒建免重下；增强 FR-033/045，关联 FR-082/183）

### FR-183 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.11.0·四档验收）

全局任务中心 + 完成站内信（job 模型 + Worker→CP 进度/日志上报 + CP API + 前端任务中心 + 完成/失败站内信；JDK 异步安装首个接入，见 ADR-040）

### FR-185 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

出站代理可视化配置（设置面板配全局/CP 代理 + 节点级覆盖经 gRPC 下发 Worker 运行时重建出站客户端，免登机器改 yaml/重启；优先级 DB 覆盖 > yaml > 环境变量；增强 FR-174，见 ADR-043）

### FR-186 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

系统更新页服务端缓存 + Markdown 更新日志（CP 持久化上次检查结果，进页即显 + 后台静默刷新、刷新失败保留旧缓存；react-markdown/GFM 渲染 release body 取代 `<pre>` 纯文本；增强 FR-182）

### FR-187 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

客户端分发迁「运营」域 + 全流程向导重做（就绪度步骤器 + 空状态引导卡 + 发布版本向导分步 + 建频道/密钥全模态化；后端 API 不变、纯前端重做；增强 FR-086 线）

### FR-188 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

全站模态框纪律（立 `.claude/rules/ui-modals.md`：禁「点击新增→内联展开/布局重排表单」、强制内容自适应模态复用 FR-072；全站违规页审计改造 + 复制按钮 HTTP 非安全上下文兜底[共享 util]，行为不变）

### FR-189 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

重度模态重做（创建实例 + 添加节点：自适应壳修溢出 + 基本/启动/高级 分区或 Tab + Docker 字段条件显隐 + AddNode「自动安装/手动连接」Tab；增强 FR-009/FR-004 对话框，遵循 ui-modals）

### FR-190 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收（真机内网经 WSL 第二机验：无 GitHub 节点从 CP 下载 install-worker.sh + worker 二进制上线））

Worker 二进制由 CP 代理缓存下发（CP 经出站代理拉「与自身同版本」worker 资产→缓存→LAN 下发；装机脚本与 UpgradeNode 改走 CP，解内网无 GitHub + latest 与 CP 版本错位）

### FR-191 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

客户端分发发布/上传/预览定向重做（发布/上传改**独立页面**[非模态，根治点外面关闭丢上传草稿]+上传即锁定文件内容；支持上传 zip 自动按包内结构编排目录；配置/审阅/详情改 Minecraft 文件树预览，内容只读、仅可编排路径/目录/sync/platform；增强 FR-187/088）

### FR-192 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

拉取密钥可查看/可编辑（密钥可逆加密存储[env 密钥]，管理员频道页查看明文+复制+**手动设/改密钥值**；**删「轮换」**[换值会断已分发客户端]、**保留「吊销」但强警告二次确认**；老哈希密钥不可查；修订 ADR-022 决策①，见 ADR-044）

### FR-193 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

updater-core 默认随 CP 内嵌静默驱动（楔子+updater-core 用 CP 自带默认版本，manifest `agent.core` 由 CP 内嵌 updater-core 自动产出；**运营不上传/不管理**，删「更新器版本」管理页；CP 自更新时默认更新器随之更新；增强 FR-091/FR-107，见 ADR-045[改写为 CP 默认]）

### FR-195 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

全站原生 `<table>` → 共享 Table + 行卡片审计（12 组件/~20 表统一共享 Table；节点 JDK/制品缓存改行卡片⇄网格切换 + 筛选；版本号/状态/类型/来源统一徽章；统计类改条形/紧凑列表；引用矩阵与 changelog 表仅调主题色；纯前端换皮、行为不变，性质同 FR-188）

### FR-196 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

前端测试与 mock 运行基座（接入 MSW v2 + jsdom/@testing-library/react；vitest 双环境[保留 node 纯逻辑、新增 jsdom 组件]；`VITE_MOCK` 浏览器 mock 模式开关[setupWorker + 入口条件挂载]；render harness[QueryClient/Router/i18n/theme]；新 ADR-047 前端测试与 mock 架构）

### FR-197 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

有状态内存假后端核心 + 错误注入框架（实体 store=Map+seed+reset、跨实体联动领域层、统一鉴权中间件[token 校验]；按 endpoint 运行时注入 401/403/404/409/500/空/网络错误/延迟；per-domain handler/seed 自动聚合防并行冲突；关联 ADR-047）

### FR-198 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

实时流仿真基座（WS 终端 PTY 伪交互[输入→回显假输出]、SSE 日志/事件/指标持续推送、可注入数据源；mock 模式与测试两用；关联 ADR-047）

### FR-199 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

mock 域簇·身份访问（auth/setup/users/groups/audit 有状态 handler + Login/Setup/Users/Groups/Audit 页面强断言测试；含「登录失败不刷新页面」回归）

### FR-200 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

mock 域簇·节点与运行时（nodes/nodeRuntime/nodeRepair/jdks/runtimeAssets/selfUpdate handler + Nodes/RuntimeAssets/SystemUpdate 强断言测试）

### FR-201 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

mock 域簇·实例核心（instances/instanceGroups/serverState/ports handler + Instances/InstanceDetail/Overview 强断言测试，创建/启停→列表联动）

### FR-204 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

mock 域簇·文件与归档（files/fileVersions/storage/archive handler + 文件管理器/Storage 强断言测试）

### FR-206 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

mock 域簇·插件玩家经济（plugins/probe/players/economy/business handler + Players/插件管理/业务经济台强断言测试）

### FR-207 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

mock 域簇·备份与计划（backups/backupStorages/schedules handler + Backups/BackupStorages/Schedules 强断言测试）

### FR-208 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

mock 域簇·可观测与日志（metrics/alerts/events/notifications/tasks/logs handler + SSE 流接 FR-198 + Monitoring/Alerts/Tasks/Logs/Dashboard 壳强断言测试）

### FR-209 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

mock 域簇·Bots 与终端（bots/terminal handler + WS 终端接 FR-198 + Bots/InstanceDetail 终端强断言测试）

### FR-210 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

mock 域簇·客户端分发与平台设置（clientChannels/clientVersions/clientStats/licenses/settings handler + ClientChannels/Licenses/Settings 强断言测试）

### FR-211 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

Playwright E2E（基于 mock 模式整站，无需真后端）：Playwright 安装 + 配置（webServer 起 `VITE_MOCK=1`）+ 关键路径 E2E 场景（登录→导航→实例创建/启停等跨页流），与 vitest 组件测互补

### FR-212 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

CI 前端质量门禁（PR 拦截）：新增 `.github/workflows/ci.yml`（on pull_request/push）跑 web lint + vitest(node+dom) + Playwright E2E，任一失败阻断；并把 E2E 加入 `release.yml` 既有 test 闸（lint/vitest 已在该闸，FR-173/ADR-036）

### FR-213 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

共享「文件浏览器」前端组件抽取（目录树/列表 + 内容预览[文本/配置/json 语法高亮] + 下载），实例「资源卡片」文件管理迁移到该共享组件（行为不变）；为客户端分发文件预览（FR-214）提供底座（**需 spec**：组件 props/接口契约、预览类型与降级）

### FR-214 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

客户端分发文件预览：发布页已上传文件 + 版本详情历史文件树，复用 FR-213 共享文件浏览器预览内容与结构（前端复用为主，补一个管理面 JWT 只读制品内容/下载端点——玩家拉取密钥端点与浏览器入口物理隔离不可复用，免 spec，依赖 FR-213）

### FR-215 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

观测导航重构：「监控」大类改名「观测」，下设 监控/日志/统计 三子类；任务中心移到「系统」；纯 IA/路由/侧栏调整、页面内容不变（**需 IA spec**；与在飞 FR-208/210/211 前端测试基座并行协调，测试按新 IA 写）

### FR-216 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

通知中心：站内信（定向消息）+ 告警（系统警报）合并为统一通知流，页眉单铃铛入口（下拉预览）+ 独立「通知中心」页（按类型筛选/已读/查询），侧栏置「系统/账户与审计」（**需 spec + ADR-048 统一通知模型**，依赖 FR-215 落点）

### FR-217 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

客户端分发观测数据底座：定时聚合客户端遥测（FR-093 events）为时序快照——拉取/更新次数、活跃客户端（machineId 去重）、版本分布与滞后、更新成功率/fail-static、下载字节、平台分布；新快照表 + 定时聚合任务 + 查询端点（总 + 按频道/时间筛选）（**需 spec + ADR-049，复用 ADR-013 分级降采样思路**）

### FR-219 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

客户端分发频道工作台「统计」Tab 扩充维度：复用 FR-217 数据加 活跃客户端/版本分布/更新成功率/平台分布 等（增强既有频道统计，免 spec，依赖 FR-217）

### FR-220 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.12.0·四档验收）

观测·统计页补齐：观测三子类之一的「统计」页做成平台级聚合统计（节点/实例/玩家/分发等概览聚合，独立于 FR-219 的频道统计 Tab）（**需 spec**，依赖 FR-215）

### FR-221 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

观测·监控时序剖析增强（**增强 FR-060**，非净新——节点/实例时序底座 ADR-013 已交付）：在既有三档降采样时序上加 多指标对比/叠加、下钻（节点→实例→世界）、自定义聚合粒度、关键指标概览，让全站指标更精准剖析（免 spec，依赖 FR-215）

### FR-222 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

worker 免配置自启 setup（节点上线丝滑化，参考 GitHub Actions Runner 下载/上线分离）：worker 启动时若未配置（无 .yml / 无 `etc/node-identity.json`）→ 进入 setup（有 TTY 交互式问 CP gRPC 地址 / enroll token / 节点名；无 TTY/CI 读命令行参数 + env，无人值守）写 worker.yml + 携 enroll token 注册持久化身份 → 转 run；已配置直接 run。「下载」（取二进制）与「上线」（setup+注册+run）解耦（**需 spec + ADR-051**，改写 ADR-020「一键单脚本下载+配置+注册+起」模型；依赖 FR-224 .yml）

### FR-223 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.13.0·四档验收）

节点安装脚本重构（配合 FR-222）：检测当前目录已有完整 worker 二进制则跳过下载；「下载」与「上线」分两步；脚本调 worker setup（传参/env）完成配置+注册+可选常驻服务（增强 FR-080/ADR-020，免 spec，依赖 FR-222）

### FR-224 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.12.0·四档验收）

配置文件 .yml 约定统一（.yaml→.yml）：control-plane.yaml→control-plane.yml、worker.yaml→worker.yml + viper 搜索路径 + docker-compose + 安装脚本 + 样例 + docs 同步 + 改 `.claude/rules/config-files.md` 规则；.yml 为准、可选 .yaml 兼容回退不破存量（ref，免 spec）

### FR-225 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.13.0·四档验收）

调试模式开关（设置项，热重载）：默认 Gin release 静默 + log info；设置「调试模式」开关开=log debug+Gin debug、关=info+release，走 FR-063 运行时机制即时生效不重启（增强 FR-063，免 spec）

### FR-226 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

通知中心快捷跳转 + 任务中心联动 + 页眉任务进度：任务类通知（含 JDK 失败）点击跳任务中心对应任务；页眉放任务中心入口 + 在跑任务进度；完成/失败站内信可一键跳（复用既有 Notification.TaskID，增强 FR-216/183，免 spec）

### FR-227 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

任务中心强制停止 + 筛选查询：强制停止=经 gRPC 真中断 Worker 长任务（取消下载 + 清临时文件）+ 任务转终态（新 canceled 态）；列表按 kind/state/node/时间/关键词筛选（增强 FR-183，需 spec：状态机 + 跨进程取消 + proto）

### FR-228 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·全真栈验收）

JDK 登记体验重做：模态目录/文件选择器选 java 可执行文件 → 后端 java -version 探测自动填 vendor/major/version/arch（免手填）；「标记为 Worker 托管」复选框置顶并决定选择器默认根（面板目录/节点目录）（增强 FR-178/033，需 spec：worker java 探测 RPC + 选择器选文件 + 跨模块）

### FR-229 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.13.0·四档验收）

连通性自检端点族 + 测试按钮：出站代理（设置面板）、JDK 下载源/镜像（运行时）、节点存活（JDK 一键下载前先测、不通即提示不卡死）统一一族测试连通性能力（增强 FR-185/178，需 spec：端点族契约 + 经出站客户端语义）

### FR-230 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·全真栈验收）

创建实例独立向导页：创建实例改独立分步向导页（非模态）+ 每步大白话提示，复用既有 create API；与 FR-189 协调（创建出重度模态、改走页，编辑等仍用模态）（增强 FR-005/189，免 spec）

### FR-231 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.13.0·四档验收）

复制实例 高级/快速 双模式：快速复制=核心 jar + plugins/ + 根配置（server.properties 及根 *.yml/*.properties）；高级复制=目录选择器 + 用户包含/排除筛选；扩 CloneWorkDir 支持 include/选择性复制（增强 FR-036，需 spec：复制语义 + 筛选 + proto）

### FR-233 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

实例配置随时编辑：实例详情/工作区提供配置编辑（启动命令 / JVM 参数 / JDK / 环境变量 / 资源限额 / 自动重启等），保存即持久化并提示重启生效；今 `InstanceDetailPage` 仅深链跳画布、无编辑能力（feat，需 spec：新 PATCH /instances/:id 契约 + 跨 CP/web、worker 重读配置）

### FR-234 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

创建实例向导优化：启动命令预填可改示例（如 `java -Xmx2G -jar server.jar nogui`）+ 提示 jar 名/放置位置 + 彻底隐藏工作目录（系统分配，对齐 ADR-007/008）（增强 FR-230，免 spec）

### FR-235 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收（首屏走 /instances/search + /aggregate 不拉裸数组、URL 状态深链恢复、卡片/列表/分组单表虚拟渲染、滚动恢复；spec §4 全 [x]，vitest 16/16 + Playwright fr235-instance-list 2/2 绿））

实例列表页重设计（面向 1000+ 实例）：卡片 + 列表重做（信息密度 / 美观 / 点击进详情），大规模下虚拟化 + 分组 + 搜索筛选；消费 FR-247 服务端搜索/分页（增强 FR-165/163，需 spec，依赖 FR-247/246/240）

### FR-236 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

一键搭建模态优化 + Docker 傻瓜建服：建后端服 / 建代理两模态遵循 ui-modals + 大白话；并入 Docker 一键建服（预置镜像 `itzg/minecraft-server` + 端口 + 一键起）（增强 FR-035/078，需 spec，含原 FR-239）

### FR-237 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·全真栈验收）

节点 Docker 可用性检测 + 未装引导：Worker 探测 docker 是否安装/可用，CP/UI 展示状态，未装给安装引导，docker 建服前先测不通即提示不卡死（feat，需 spec：worker 探测 RPC + 跨模块，关联 FR-229 连通性族）

### FR-240 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

导航外壳 + 实例选择器重构（面向 1000+ 实例）：先出 2-3 布局原型经确认 → 实现；实例选择支持搜索 / 虚拟列表 / 分组 / 最近&收藏；并入面包屑文案与路由映射纠错（今实例页面包屑文字与实际页不符；此项排最后做）（增强 FR-131/162/134，需 spec + 原型审核，含原 FR-245）

### FR-241 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

全局搜索 / 命令面板（快捷跳转）：Ctrl/⌘+K 唤起，搜实例/节点/页面/操作并跳转（今页眉搜索框为占位）；并入页眉集群徽标 → 可滚动实例选择弹层 + 点击进入；面向 1000+ 走服务端搜索（feat，需 spec：搜索契约，依赖 FR-247，含原 FR-242）

### FR-244 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.13.0）

全局动画系统统一：修「稀碎卡顿 / 无动画」——统一 hover / 展开 / 弹层 / 路由过渡（今无 framer-motion、散用 CSS transition）（增强 FR-163/176；本轮随 `docs/specs/instance-list-redesign/spec.md` 草拟审核）

### FR-246 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

全站卡片范式提质 + 节点页卡片重设计：统一卡片圆角 / 边角 / 阴影 / 间距（修「连接感 / 阴影太假 / 圆角不符 / 边角瑕疵」）并全站贯彻 + 节点页卡片信息密度重做（增强 FR-163/176/177 落地质量，需 spec：圈定全站范围 + 范式）

### FR-250 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

客户端分发发布编排器重做：拖拽多文件/文件夹到浏览器 → 前端本地预览文件树 → 可调整层级/sync 模式 → 点击发布才批量上传到 CP（非逐个上传节省带宽）；消费 FR-251 分块上传（增强 FR-191，需 spec，依赖 FR-251）

### FR-252 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.13.0·四档验收）

客户端分发术语大白话化：sync 模式（覆盖/仅一次/忽略）加大白话解释、"托管目录"等术语改为运营能看懂的措辞、发布步骤每页加大白话说明（增强 FR-194，免 spec）

### FR-254 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.13.0·四档验收）

客户端分发发布页文件树拖拽编排：configure 步支持拖拽移动文件/目录节点改目标路径（增强 FR-191/250，FR-191 曾列增强未做，免 spec）

### FR-260 需求原文（优先级 — / 状态 ✅ 已交付@v0.13.0·四档验收）

客户端更新器架构简化 前端 + ADR + doc-sync：接入指引去 signPublicKey 改"楔子自动拉取"、updater-core 版本选择器面板、新 ADR 推翻 ADR-022/053 修订 ADR-045、API/ARCHITECTURE/PRD/CHANGELOG 同步、重编内嵌 jar；后续增强：接入指引可选择 revealable 拉取密钥并自动填入/下载 `jm-updater.json`，Core 版本页展示最新归档与当前选定，内置/选定 updater-core 归档禁止从制品库删除（依赖 FR-259）

### FR-261 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

发布页文件资源管理器（专业文件树库重写）：react-arborist 替换 ClientFileTree，新建文件夹/重命名/Ctrl+点选/Shift+连选/Delete 删除/拖拽上传文件/拖拽上传 zip 自动解压（含 GBK）/拖拽移动调整结构/同名冲突提示忽略覆盖保留两者；保留本地编排→点发布才上传架构（feat，需 spec）

### FR-262 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

清理目录树形右键菜单可视化：FR-261 同款文件树展示目录结构，右键菜单标记清理/排除/取消，Ctrl+点选 Shift+连选批量操作，颜色区分（红=清理/绿=排除/无色=不管理），父子联动；产出 managedDirs+cleanExclude（增强 FR-255，依赖 FR-261，需 spec）

### FR-263 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

拉取密钥加密器自动生成与持久化：CP 启动未注入 JIANMANAGER_CLIENT_KEY_ENC_SECRET 时自动生成 AES-256-GCM 密钥并持久化到数据根文件（env 注入优先、双轨），dev 回退内置开发密钥；修订 ADR-044 存储策略；后续增强：密钥列表/编辑支持永不过期展示、设置/清空过期时间，明文弹窗标题不拼接密钥名且复制按钮稳定可用（feat，需 spec + ADR）

### FR-267 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

高密度控制台设计系统底座：控制台主界面采用 A+C Jian 绿默认运维风格（细边框、小圆角、紧凑间距、等宽数字、状态点、紧凑表格），作为页眉/侧栏/服务器控制台的视觉基础（见 ADR-057）

### FR-268 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

页眉节点作用域 + 侧栏 IA + 页面归位：页眉提供节点作用域、面包屑、搜索、集群状态、任务、通知、账户；节点作用域联动全部服务器列表、命令面板实例结果与创建实例默认节点；侧栏改为平台首页/服务器/群组网络/观测/平台管理，主题入口保留在侧栏底部，单服能力归入服务器控制台（见 ADR-055）

### FR-269 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

服务器统一控制台 `/instances/:id`：单台服务器详情页固定分区为概览/控制台/文件配置/监控/玩家/插件/备份定时/业务/Bot，并保留可组合画布作为高级拼屏能力（见 ADR-056）

### FR-272 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.13.0·四档验收）

全站页面归位与 A+C 皮肤收口：把玩家、插件、备份、定时、Bot、业务等单服能力收进服务器控制台，平台级页面归入平台管理，并统一 A+C Jian 绿默认高密度外观

### FR-273 需求原文（优先级 — / 状态 ✅ 已交付@v0.13.0·四档验收）

通用组件包与控件博物馆：抽出 `@jianmanager/ui` 通用 UI/token/charts 包，主应用从包引入共享控件，并新增 `web/wiki` 控件博物馆展示第一版通用控件与常用状态矩阵（需 spec + ADR-058；不抽业务组件、不新增后端接口）

### FR-275 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.13.0·全真栈验收（无手工 secret 场景端到端：注册自动下发→终端 101））

CP↔Worker 专用 WS 令牌密钥自动下发：CP 预配独立于 jwt.secret 的 WS 令牌密钥（显式配置 > 生产态自动生成持久化 > dev 回退），终端/插件桥令牌改用其签发；经 gRPC RegisterResponse 下发（首注册+重注册），Worker 持久化到 node-identity.json（0600）并用于终端+插件桥校验，旧 CP 空字段回退现状（修 FR-080 enroll 不下发密钥致生产终端 401、探针监控失效的产品缺口；需 spec + ADR-061，修订 ADR-020）→ `docs/specs/worker-ws-token-secret/spec.md`

### FR-277 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0）

SSH 推送式远程部署脚本：`deploy-cp.sh` / `deploy-worker.sh` 两独立 POSIX sh 脚本，从操作机经 SSH 密钥把本地 `make dist` 产物推送部署/更新到 Linux 主机（systemd 常驻、幂等重跑、数据与身份保留、部署后健康检查）；SSH 目标与部署参数经环境变量配置，Worker 首次上线经 `JM_ENROLL_TOKEN` 复用 setup 语义；目标机 root / 免密 sudo / 纯普通用户（user 级 systemd+linger）三档全支持（需 spec + ADR-063）→ `docs/specs/ssh-deploy-scripts/spec.md`

### FR-278 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0）

CP 内嵌 Worker 二进制（一键安装/升级不依赖 GitHub）：构建期把 windows/linux amd64 Worker 二进制 go:embed 进 CP（make dist / CI 两阶段：先 worker 后 CP，体积 +~43MB），`/worker-assets` 端点解析顺序改为 内嵌（版本=CP 自身）> 本地缓存 > 远程 feed 兜底——离线/内网/国内网络/本地构建开发版的一键安装与节点升级全程不出网，CP↔Worker 版本强一致（真机复现：安装请求因 GitHub TLS 超时 + 远程无本地构建版本 release 而必然失败）（feat，需 spec + ADR-062 修订 ADR-059 分发来源）→ `docs/specs/worker-embed-distribution/spec.md`

### FR-279 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（代理下发链路 L1 证+引导 UI 上线；网络失败态 env-blocked 已 DOM 证））

JDK 下载受限网络可用性与失败引导：JDK 一键下载（temurin 等 GitHub 源）在无代理环境 TLS 超时（真机复现 `adoptium/temurin21-binaries ... TLS handshake timeout`）——失败信息改为可操作引导（明示可在设置页配置出站代理、或切换 JDK 镜像源，附入口跳转），并真机验证 Worker 侧 JDK 下载确实经 ADR-043 心跳下发的出站代理生效（增强 FR-033/178/186，免 spec）

### FR-280 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（任意 URL 实测+可编辑输入框线上 bundle 命中））

出站代理面板提示与连通性测试自定义目标：① 代理设置区补大白话提示，列明哪些流量经此代理（JDK 下载、Worker 资产/自更新拉取、探针依赖、客户端分发外呼等）；② 连通性测试目标从写死 GitHub 改为可输入任意 URL 测试，默认 `https://www.google.com`（提示文案同步去掉「（GitHub）」硬编码）（增强 FR-185/229，免 spec）

### FR-281 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0）

CP→Worker 指令反向流化（免 NAT / 零入站端口暴露）：把 CP→Worker 的指令派发从「CP 直连 worker gRPC `node.Host:GRPCPort`」改为经 Worker 主动建立的**常驻双向流**承载——Worker 只出站、零入站端口暴露，家用 NAT / 内网 / 公网混合环境均可接入（现状真机复现：公网 CP 无法回拨家用 NAT 后的 Windows worker 的 9101/9102）。含 proto 新增双向指令流、CP 连接池由「拨号」改为「往流里派发 + 关联请求/回执」、Worker 由 gRPC 服务端改为「守流听指令」、超时/断线重连/背压语义；终端 WS（9102）一并经 CP 中转（或同机制隧道化）才彻底免入站。**架构级大改，需 spec + ADR-066 修订 ADR-002（gRPC 节点通信）**；设计定案：gRPC 反向隧道（grpctunnel，~70 RPC 零信封）+ 双模式（隧道优先/直拨回退）+ 终端一律经 CP 中转（feat）→ `docs/specs/worker-reverse-stream/spec.md`

### FR-283 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.16.0）

仓库根 pnpm workspace + 前端提升（ref，需 spec）：引入根 `pnpm-workspace.yaml`；`web/src`→`apps/control-plane-web`、`web/wiki`→`apps/ui-museum`、`web/packages/ui`→`packages/ui`（vite alias 改真实 workspace 依赖）；抽 `packages/tsconfig` + `packages/eslint-config` 共享配置；go:embed 目标不变，仅 `Makefile` embed-web 拷贝源 `web/dist`→`apps/control-plane-web/dist` 改一行。行为不变（关联 ADR-064，重申 ADR-005）→ `docs/specs/monorepo-unification/spec.md`

### FR-284 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.16.0）

devmock 抽为独立包（ref，免 spec）：`web/src/mocks`→`packages/devmock`，依赖反转（只依赖 API 契约，不再 `import '@/…'` app 内部）；主应用与测试改引 `@jianmanager/devmock`；生产构建仍不含 devmock（ADR-047 不变量保持）。依赖 FR-283 先落

### FR-285 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.16.0）

Go 入口迁入 apps（ref，免 spec）：`cmd/*`→`apps/*`（control-plane/worker/jmctl）；`internal/` 不动；同步 `Makefile`/`release.yml`/`Dockerfile.*` 的 go build 路径；go:embed 目标目录不变。main 包无 importer，行为不变（关联 ADR-064）

### FR-286 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.16.0）

bot-worker 迁入 apps（ref，免 spec）：`bot-worker`→`apps/bot-worker`；同步 Go worker 运行时 spawn 路径解析与打包；须真机验 bot 真入服 spawn 成功（关联 ADR-064）。依赖 FR-285

### FR-288 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.16.0）

前端版本接上单一真源（feat，需 spec）：版本唯一真值 = `internal/version/version.go`（ADR-065 既定）；把前端 `__APP_VERSION__` 从独立记版的 `web/package.json` 改为构建期从真源派生注入，改 `version.go` 一处→前端展示+Go `/version`+产物路径（ADR-036）一致，消除前端漂移（实证：真源已 `0.15.0-dev`、前端仍报 `0.14.0`）（关联 ADR-064/065）→ `docs/specs/monorepo-unification/spec.md`。依赖 FR-283

### FR-289 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（amd64→x64 归一装成；foobar→422 INVALID_ARCH））

JDK 安装 arch 别名归一化（fix，增强 FR-033/178）：`POST /nodes/:id/jdks/install` 的 `arch` 为自由字符串直透下载源解析，传 `amd64` 时 adoptium 不认（只认 `x64`）→ 下载 404 且错误不指向真因（真机复现：temurin 17 `arch=amd64` → 「下载返回 HTTP 404」，同版本 `x64` 成功）；面板固定送 `x64` 不受影响、API/自动化调用方中招。CP 侧入参归一化常见别名（`amd64`→`x64`、`arm64`→`aarch64`），未知 arch 直接 422 拒绝而非透传出 404（免 spec）

### FR-290 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·单测+真机回归（停滞归网络类/强停不误标单测覆盖；真停滞待自然复现看引导渲染））

JDK 下载停滞看门狗与中断错误归网络类（增强 FR-183/279）：下载流中途被掐（代理/跨境线路抖动）时任务无停滞检测，卡在中间进度直至整体超时兜底（真机复现：经 SOCKS5 代理下载 temurin 21 卡 48% 约 9 分钟），且最终错误 `写入临时文件失败: context canceled` 未被 FR-279 归为网络类 → 失败引导（去配代理/换镜像）不触发。加下载停滞看门狗（N 秒零进度 → 快速失败）并把停滞/中断类错误纳入 FR-279 网络类归类与引导（免 spec）

### FR-291 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（预植残骸自愈装成+完好目录仍拒，同机正反对照））

JDK 安装失败残留目录清理与重试自愈（fix，增强 FR-183）：安装失败/被取消时已建的目标目录不清理（真机复现：卡死任务遗留 4KB 半截 `temurin-21/` 脚手架），此后同版本重装永远撞「目标目录已存在」，须登机手动 rm 才能解——核心操作被死锁在需要 SSH 的境地。改为安装失败即清理本次所建目标目录；安装前若目标目录存在但无完成标记（无 `bin/java`）则视为残骸自动清除重装；完好已装目录仍拒绝覆盖（语义不变）（免 spec）

### FR-292 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（嵌套删除无父壳、邻居 JDK 完好、拒删托管根））

删除托管 JDK 清理到托管根顶层目录（fix，增强 FR-033/228）：删除走 NodeJDK 登记的**嵌套探测路径**（归档外层多包一层，如 `temurin-11/jdk-11.0.31+11`），Worker `RemoveAll` 只删内层、父壳空目录留在托管根下占位（真机复现：删 11 后 `temurin-11/` 空壳仍在，需手动 rm；症状虽被 FR-291 自愈兜住，删除本身应清净）。Worker Remove 把托管根下的目标路径归一到**顶层子目录**整体删除；托管根本身（`rel="."` 现可穿过校验直达 `RemoveAll(根)`，一并堵死）与根外路径拒绝（免 spec）

### FR-293 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（收藏/最近渲染、点击客户端跳转进服、与选择器星标互通、双空引导））

侧栏常驻服务器列：收藏置顶+最近打开（增强 FR-240，补当年搁置的 Part B 常驻实例列）：「选择服务器」按钮下方常驻两区=收藏（置顶）+最近打开（LRU ≤8），行=状态点+名称，点击进该服控制台；行内星标收藏/取消，与搜索弹窗收藏互通（同一 localStorage）；已收藏不重复出现在最近区；双空显示引导；侧栏折叠图标轨态不显示；不为侧栏引入高频轮询（免 spec）

### FR-294 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（节点浮窗 4 节点各显运行数、运行服浮窗名称+节点+在线、查看全部页脚））

页眉集群统计浮窗（增强 FR-162/179，复用 FR-216 铃铛浮窗范式）：在线节点/运行中服务器/崩溃服务器三徽标点击改弹缩略浮窗——节点行=名称+状态点+运行实例数、服务器行=名称+节点+在线人数、崩溃行=名称+节点+崩溃原因（statusReason）；行点击进对应服控制台/节点页，底部「查看全部」保留原筛选页跳转；数据仅浮窗打开时拉取，行数上限 8 超出提示剩余数；崩溃空态友好文案（免 spec）

### FR-296 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（跨服切换两页同存 DOM、组件不重建、WS 存活缓冲连续；闲置 10min 断连+重连、4 台 LRU 淘汰环境受限未真机、单测覆盖））

跨服务器控制台热缓存（增强 FR-269，**依赖 FR-295**）：最近打开的 ≤3 个服控制台整体保活（LRU 淘汰），A↔B 切换双向瞬时呈现；缓存中的服终端 WS 保持连接、缓冲连续；闲置超时（默认 10 分钟不可见）自动断 WS 降级为界面状态缓存、再进自动重连；LRU 淘汰即完整卸载释放（与 FR-295 同 spec）

### FR-297 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（跨服回切瞬时呈现无白屏，热缓存命中））

实例域数据层缓存调优（增强 FR-269/240）：实例详情/指标/serverState 等实例域查询拉长 staleTime/gcTime + placeholderData，回切先呈现缓存数据后台刷新；侧栏常驻列/服务器选择器行悬停预取实例详情（防抖）；现有 vitest 全绿不回归（免 spec）

### FR-298 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0·真机验收（扫描命中植入JDK→登记(managed=false)→重扫already去重→删外部登记留文件））

节点运行时库底座与自动扫描（feat，泛化 FR-033/178/228 JDK 域，需 spec）：运行时抽象加类型维度（jdk / nodejs / python 预留；JDK 沿用 `node_jdks` 不动实例外键，新类型承载表 spec 定）；Worker 新增**扫描常见安装路径** RPC（`/usr/lib/jvm`、`Program Files\Java`、nvm/官方 node 目录等，复用 detect 探测），节点页 JDK 面板扩为「运行时」分区：扫描候选勾选入库 + 手动登记泛化多类型 → `docs/specs/node-runtime-library/spec.md`

### FR-299 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0·真机验收（直连 nodejs.org 装 Node 22.23.1 落盘 bin/node、amd64→x64 归一、任务中心进度、托管删除连文件；no_proxy 绕过 SOCKS 代理直连））

Node.js 运行时一键安装（feat，需 spec 并入 node-runtime-library）：nodejs.org dist 便携归档安装（index.json 版本解析、托管目录布局同 JDK），经节点出站代理（ADR-043）；停滞看门狗 / 网络失败引导 / 残骸自愈 / 删除顶层清理与 JDK 链路同语义（FR-289~292 齐平）。依赖 FR-298

### FR-300 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0·真机验收（bot spawn 日志 nodeSource=managed-scan + node=托管 Node 路径，host-2 无系统 node 必用托管））

bot-worker 接托管 Node 运行时（增强 FR-006 Bot 域，需 spec 并入 node-runtime-library）：Bot spawn 由裸 `exec("node")` 改为优先用节点运行时库登记/托管的 Node（可指定，无则回退 PATH 保兼容），消灭新节点 PATH 缺 node 时 Bot 起不来的脆弱点。依赖 FR-298

### FR-301 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0·真机验收（overview 多运行时矩阵+runtimeSyncs+syncedAt、refresh 逐节点结果、node-windows 离线容忍显 None））

CP 运行时库存聚合缓存与刷新（feat，增强 FR-178 运行时资产页，需 spec 并入 node-runtime-library）：Overview 扩**多运行时矩阵**（节点 × 类型 × 版本 + 引用关系），CP 缓存各节点库存快照、显「上次同步时间」+ **手动刷新**（强制 syncFromWorker），同步失败容忍显旧数据。依赖 FR-298

### FR-303 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.15.0·真机验收（线上bundle含70键中英翻译，审计记录新action；未知键回退单测覆盖））

审计日志 action 名 i18n（增强审计域，免 spec）：全量已知 action 键（`instance.start` 等 60+）中英映射（`audit.actions.*`），审计页显示翻译、悬停/角标露原键、未知键回退原样不崩；守则：本批 FR-298~302 新增审计 action 随身带中英翻译

### FR-304 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0）

文件上传链路流式化（增强 FR-009 文件域 + FR-052/053 插件部署，需 spec）：`POST /instances/:id/files/upload` 现为 CP `io.ReadAll` 全量内存缓冲 + Worker `WriteFile` unary 直传（下载修复 a6a3713 的对称隐患）。复现结论：**直拨模式** >64MB 被 Worker `MaxRecvMsgSize` 显式拒收（65MB → `ResourceExhausted` 68157492 vs 67108864）；**隧道模式**（FR-281）grpctunnel 分帧 16KB、逻辑消息上限 4GB，同载荷**不设防直接吞** → 双模式行为不一致；≤64MB 也双侧整块缓冲，且 CP `FileService.WriteFile` 硬编码 10s 超时慢链路大文件必超时。新增 client-stream `UploadFile` RPC（与 `DownloadFile` 对称：分块传输 + 落临时文件校验后原子改名），CP 上传 handler 改流式转发不再 `ReadAll`；FR-052/053 插件单发/批量扇出复用 `WriteFile` 的部署路径同步兼容改造（阈值/是否保留小文件 unary 由 spec 定）；老 Worker `Unimplemented` 回退 `WriteFile` 保兼容（沿用 ≤64MB 上限并给明确报错）

### FR-305 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0）

反向隧道单消息尺寸上限统一治理（fix/健壮性，增强 FR-281 隧道传输 + FR-010/114 探针部署，需 spec + 宜随 FR-281 M1 一并做）：Worker `ServerOptions()` 的 `MaxRecvMsgSize=64MiB`（5de8778 为 `DeployServerProbe` ~7.6MB 载荷特意从默认 4MiB 放宽）只作用于 `grpc.NewServer`；而反向隧道把 WorkerService 挂在 `grpctunnel.NewReverseTunnelServer`（`internal/worker/tunnel/tunnel.go:107`）上，**不消费任何 `grpc.ServerOption` 消息上限**——grpctunnel v0.3.0 不暴露尺寸上限选项，唯一天花板是硬编码 `math.MaxUint32`≈4GB。后果：① **双传输行为不一致**——直拨 >64MiB 显式 `ResourceExhausted` 拒收，隧道同载荷一路吞到 4GB（FR-304 复现坐实：65MB `WriteFile` 隧道成功）；② 隧道已是 ADR-066 首选路径，那个「特意放宽到 64MiB」的安全边界在主链路上**等于不存在**，大 unary RPC（`DeployServerProbe` jar、老 `WriteFile` 直传、`ReadFile` 10MiB 返回等）整块缓冲、无早拒，构成内存/OOM 暴露面。范围：统一直拨与隧道两路的收/发单消息尺寸策略（含 CP 侧 `MaxCallRecvMsgSize`/`MaxCallSendMsgSize` 与 Worker 收发两向），做成有意为之的显式上限而非默认漂移；大载荷优先走流式（与 FR-304/`DownloadFile` 同向，`DeployServerProbe` 是否流化由 spec 定），合法 7.6MB 探针部署与既有 <64MB 载荷必须仍通过。**待定**：grpctunnel 无原生上限选项，落地手段（应用层拦截器早拒 vs 大 RPC 全面流式化 vs 二者结合 + 文档化 4GB 硬顶）由 spec 决策 → `docs/specs/tunnel-message-size-guard/spec.md`

### FR-306 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.15.0）

节点包管理器与多 registry 配置（feat，泛化 FR-298 运行时库 + 承 FR-299/300 Node 生态，需 spec）：Node 22 自带 corepack，节点级选 npm/pnpm/yarn 偏好（默认 npm，pnpm/yarn 经 `corepack enable`+指定版本激活，零额外下载）；多 registry 配置——默认 registry + `@scope` 域源 + 镜像（CN 走 npmmirror 提速），写节点 `.npmrc`/托管配置（含凭据脱敏）。数据模型存 PM 选择 + registry 列表（节点级），Worker RPC 读写 PM/registry 配置 + `corepack enable`；被 FR-307/308 依赖。→ `docs/specs/node-package-management/spec.md`

### FR-307 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.16.0）

全局运行时包可视化管理（feat，需 spec 并入 node-package-management，依赖 FR-306）：面板管理节点上的**全局 npm 包**（`<pm> ls -g --json` 解析已装列表）——手动装/删/更新/查版本；装进节点运行时全局目录（PM global prefix，落托管区 `<数据根>/opt/runtimes/global`），装/更新经任务中心异步（慢操作）+ 停滞看门狗/失败引导语义复用（FR-290/279）。前端全局包管理页（列表+搜索+增删改+可更新标记），走已配 registry/镜像 → `docs/specs/node-package-management/spec.md`

### FR-308 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.16.0）

bot-worker runtime 下发部署 + 经全局包运行（feat，节点级，需 spec + 新 ADR 修订 ADR-006，依赖 FR-306+299）：install-worker.sh 一键装机/CP 下发 bot-worker 源码（`dist`，非 bundle）；其依赖（mineflayer/mineflayer-pathfinder）经 FR-306 的 PM+registry 装为**全局包**，bot spawn 带 `NODE_PATH` 指向全局 node_modules（FR-300 node 解析已就绪）——统一「全局包」模型：装 mineflayer 插件（全局包）bot 立即可用。消除装机节点 bot 真连 MC 缺 426MB 依赖的缺口（真机复现：dist-only spawn `ERR_MODULE_NOT_FOUND: mineflayer`）→ `docs/specs/bot-worker-deploy/spec.md`

### FR-309 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.17.0）

节点删除实例守卫（fix，增强 FR-004 节点管理）：删除节点当前不校验其名下实例，节点没了实例记录成孤儿——终端/操作全 404「节点不存在」（v0.15.0 真机复现：删离线测试节点后其 nat-echo 实例悬挂，用户直撞 404）。节点删除时有实例则 409 拒绝并列出实例（离线节点允许 `?force=` 显式级联删除记录并明示不清理远端文件）（免 spec）

### FR-310 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.17.0）

删除运行中实例的进程处置一致性（fix，增强 FR-005/FIX-C）：对 RUNNING 实例发 DELETE，CP 删记录返 200 而 Worker 运行态守卫拒杀进程——DB 与现实脱钩，java 进程成无主孤儿继续跑占端口（v0.15.0 真机复现：删运行中测试实例后 java -jar server.jar 遗留）。改为 CP 对 RUNNING 实例删除先优雅停（超时强杀树）再删，或 409 要求先停；Worker 重启孤儿接管扫描一并评估（免 spec）；真机验收补：删运行中 daemon 实例后工作目录残留（Unix 上 Java 自成进程组存活、RemoveAll 后重写 world 重建目录，并遗留 pid/sock），删目录前按 PID 强杀 wrapper+Java 两棵进程树+死透校验+删后校验重试兜底（与 FR-325 同根）

### FR-311 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.16.0）

节点页管理体验补全（增强 FR-033/177，真机测试反馈）：① JDK 已登记行加**编辑**入口（改厂商/版本/arch/路径，走既有 PUT /nodes/:id/jdks/:jid，模态遵守 ui-modals）；② 节点详情头四个统计球排版紧凑化（现占满整行空隙过大，改小尺寸+左聚拢与信息区同排）（免 spec）

### FR-312 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.16.0）

实例启动失败原因可见性（增强 FR-269/294，真机复现：实例未绑 JDK 点启动只见「启动中→崩溃」无因——statusReason 已入库已出 API，但实例控制台页无展示位，且 Worker 心跳把 CRASHED 冲回 STOPPED 后卡片唯一展示位 `status===CRASHED` 条件失效）：实例控制台页加失败原因横幅（statusReason 非空即显、标注「上次启动失败」，下次启动由既有 transition 清空），工作台卡片展示条件同步放宽为非空即显（免 spec）

### FR-315 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.16.0）

实例状态光晕视觉（增强 FR-269/240 控制台视觉）：实例控制台容器与工作台实例卡片外圈按状态渐变光晕——RUNNING 绿色柔光、STARTING/STOPPING 琥珀呼吸、CRASHED 红色警示光、STOPPED 无光晕；随双主题取色、平滑过渡动画、遵守 UI 风格偏好不过度（免 spec）

### FR-316 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

一键搭建版本-JDK 兼容预检（增强 FR-034 搭建向导，FR-308 真机钓出：选 MC 26.1 + 节点最高 Temurin 21 可搭建成功但启动必崩「requires running the server with Java 25」）：向导选定 MC 版本后按核心 Java 需求映射校验所选/默认 JDK，不满足即表单级阻断并引导去 JDK 面板安装；与 FR-314 启动预检互补（搭建时拦 vs 启动时拦）（免 spec）

### FR-317 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.16.0）

内存水位启动守卫（feat，真机事故钓出：验收开 Paper -Xmx2048M 把 8G 主机 OOM 至用户态僵死、SSH/面板全失联）：双闸防「启动实例把节点内存跑满」——① **Worker 实时闸**（真源）：StartInstance 前读系统可用内存，估算实例需求（启动命令解析 `-Xmx` + JVM 开销，解析不到用保守默认），可用 − 需求 < 保留水位（worker.yml `memory_guard.reserve_mb`，默认 max(512MB,总内存10%)）即拒绝启动并返回可操作错误；② **CP 预警闸**：启动请求先按节点最近心跳（总内存/已用）预判，不足直接 400 不下发 RPC，心跳过旧（>90s）放行交 Worker 实时判。批量启动并发预留列为后续增强（免 spec）

### FR-318 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

CP 压力态 SQLite 嵌套事务错排查（fix，FR-317 事故现场钓出：主机 OOM/swap 风暴恢复初期，实例 stop 转换反复报 `SQL logic error: cannot start a transaction within a transaction`，删 bot 报 INTERNAL_ERROR，重启 CP 即愈）：排查 transition/删除链路在慢 IO 下是否存在事务嵌套或连接状态破坏（glebarez/sqlite 单连接语义），补压力回归测试

### FR-319 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.16.0）

一键搭建异步化 + 全程可观测（fix+feat，真机复现：1.20.1 搭建挂死 180s+——Paper CDN 从节点侧仅 ~200KB/s、47MB jar 需 4 分钟，provision 是同步 HTTP 长事务，前端超时断开→请求 ctx 连锁取消 DownloadCore→留空壳实例（无 server.jar 可点启动→秒退 CRASHED），错误随断开连接进黑洞、CP 零日志零审计）：搭建改任务中心异步——同步段只 ResolveBuild+建实例（立即 201+taskId），下载/写配置/探针在 CP 后台 goroutine（context.Background 独立超时，不受前端断开影响）分阶段推进 Task（下载核心→写配置→部署探针），失败 Task 含完整错误链 + 实例 statusReason 写「搭建未完成：…」；worker DownloadCore 补开始/完成/失败日志；前端向导提交即关框提示看任务中心。**二轮修复（异步化暴露的新缺口）**：① DownloadCore 改临时文件 `.part` + 完整校验后原子 rename——下载中窗口不再躺半截 jar（真机复现：搭建中点启动得 `Invalid or corrupt jarfile server.jar`）；② Task 加 instance_id 关联，CP 启动闸拦截「provision 任务未终态就点启动」并引导看任务中心；③ 搭建全程实例 statusReason 标「搭建中」+ 卡片琥珀提示（免 spec）

### FR-320 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.16.0）

API 错误统一落平台日志（feat，真机钓出：provision 失败等 API 错误只回 HTTP 响应，无任何 slog→FR-049 日志中心平台日志恒空，用户「乱七八糟的问题都看不到日志」）：全局 gin 中间件把 4xx 业务拒绝（422/502 等）与 5xx 的 [路径、状态码、error body、用户、IP] slog 化（4xx=warn、5xx=error），经既有 log_slog 桥自动落日志中心 platform 源；核对 /logs 页导航入口可见性（免 spec）

### FR-321 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.16.0）

审计日志记录失败操作与错误内容（增强 FR-050，真机钓出：搭建失败审计零记录）：AuditLog 加 success/error 字段，关键写操作（实例生命周期/搭建/删除/上传）失败也记审计并带错误内容；审计页列表失败徽章 + 详情展开错误（免 spec）

### FR-322 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.16.0）

心跳流断开告警降噪（fix，真机复现：CP journal 2 小时 484 条「心跳流断开 context canceled」WARN——worker 每拍心跳收完响应即 cancel 属正常收尾，被当异常刷屏淹没真问题，且无节点标识）：Canceled/EOF 正常收尾降 Debug，异常断流保留 WARN 并带 nodeUUID（免 spec）

### FR-323 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.16.0）

长操作任务化：导入/克隆/备份纳入任务中心（增强 FR-302 导入 / FR-036 克隆 / 备份 FR，FR-319 同族收口——用户「把所有长耗时任务加到任务中心、不堵塞页面、有进度」）：抽公共 `run-as-task` 底座（CreateTask→CP 后台 goroutine→SetStage 阶段进度→MarkSucceeded/Failed→终态站内信），provision（FR-319）一并迁过去 DRY；导入 migrate 搬迁 / 克隆 CloneWorkDir / 备份创建 / 备份恢复 四个现同步阻塞或后台无进度的长操作接入——提交秒回 `{taskId}` 不阻塞（搬迁/拷贝/打包可达数十分钟），任务中心显示 stage 文案 + 终态 + 失败错误链；新增 TaskKind（import/clone/backup_create/backup_restore）+ 前端任务中心筛选/文案 → `docs/specs/long-op-tasks/spec.md`（需 spec：3 端点响应改 {taskId}、抽共享底座跨 3 service+任务模型+前端）

### FR-324 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.16.0）

短操作 loading 与文件传输进度补齐（增强 FR-075 反编译 / FR-068 探针 / FR-008/070 文件——用户「1-3 秒短任务要 loading 或进度条」）：① 反编译、探针部署点击后 loading 且完成前防重复点；② 资源管理器 新建/重命名/删除/保存 文件 loading 反馈；③ 文件上传显示百分比进度条（axios onUploadProgress），下载有反馈（浏览器原生下载条 + 内联「下载中」提示）。纯前端 UX 补齐，无数据模型/API 变更（免 spec）

### FR-325 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.17.0）

Worker reconnect 失败孤儿进程接管兜底（fix，FR-310 孤儿评估缺口 (a)，ADR-003 守护链路；真机事故实证：FR-277 主机实例 1-86876de3 启动报 Paper `session.lock already locked`——前次运行的孤儿 java 仍活着占世界锁）：Worker 重启接管扫描中 wrapper 存活但 reconnect 拨号失败时，现只删 PID 文件（`manager.go` reconnect 失败分支），活进程从此永久失联。改为保留 PID 文件重试有界次数，仍失败则按 PID 记录的 WrapperPID 杀进程树并记日志，杜绝孤儿永久化（免 spec）

### FR-327 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

页眉任务中心下拉面板（增强 FR-183 任务中心）：点击页眉任务中心入口弹下拉面板（最近 N 条任务：kind/名称/stage 进度/终态徽章），面板内点条目跳任务中心对应项；运行中任务有进度反馈；模态外点击关闭（免 spec）

### FR-328 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.17.0）

一键搭建后端子服 MC 版本下拉不可滚动（fix，增强 FR-034 搭建向导，真机复现）：向导后端子服步骤的 MC 版本下拉列表超出可视高度后无法滚动，长版本列表不可选。修复下拉内容区滚动（受模态内 overflow 约束影响一并核查），并核查向导内其他下拉同病（免 spec）

### FR-329 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.17.0）

任务中心进度自动刷新（增强 FR-183/319，真机反馈「搭建进度要手动刷新才动」）：任务中心页与页眉任务入口在存在非终态任务时自动轮询刷新（TanStack Query refetchInterval，运行中 ~2s/空闲停轮询），任务到终态即停；SSE 回推列为后续可选不在本 FR（免 spec）

### FR-330 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.17.0）

服务器核心节点级缓存（增强 FR-034/319 搭建链路 + FR-179 制品缓存，真机痛点：Paper CDN 节点侧 ~200KB/s，47MB 核心每次搭建都重新下载）：Worker DownloadCore 先查节点核心缓存（sha256 已知按 sha 键、否则 core+version+build 键，落数据根 cache 区），命中跳过远程下载直接复制/链接入实例目录；未命中下载完成校验后入缓存再交付；缓存条目在节点制品缓存面板可见可清理；provision 任务 stage 文案区分「缓存命中/下载中」（免 spec）

### FR-331 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.17.0）

搭建中实例硬性禁止启动（fix，增强 FR-319 二轮②启动闸，真机反馈：statusReason「正在下载核心（完成前请勿启动）」仅文案提示，仍可点启动）：provision 任务未终态的实例，前端启动按钮直接禁用（tooltip 指明搭建中+引导看任务中心），后端启动闸核查补漏确保各入口（单启/批量/一键）一律 4xx 拒绝——文案劝阻升级为状态强制（免 spec）

### FR-332 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

折叠态导航分类图标点击跳转分类首页（fix，增强 FR-131/181 侧边栏折叠，真机复现：折叠后点击分类图标无反应无法跳转）：折叠态下点击分类图标默认导航到该分类下第一个页面（并展示激活态）；展开态行为不变（免 spec）

### FR-333 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.17.0）

CP 本机应急密码重置子命令（feat，运维应急能力，用户要求「补重置密码命令」以解锁验收环境）：`jianmanager-cp reset-password -u <用户名> [-p <密码>] [-c <config>] [-list]`——bcrypt 重置指定用户密码并解锁账号（Status→Active），`-p` 留空自动生成 16 位随机密码打印，`-list` 列出全部用户名。命令落 CP 二进制自身而非 jmctl：数据库仅 CP 可读写（架构不变量），jmctl 按 ADR-041 不得直连 DB（免 spec）

### FR-334 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

控制台品牌顶栏贯通（T 型外壳）+ 页眉节点作用域下线（feat，增强 FR-162/181/268 控制台外壳，用户走查「侧栏与页眉交界处错位成台阶、页眉节点作用域下拉几乎无作用」）：外壳改 T 型——整宽顶栏（品牌区 + 面包屑 + 操作区）在上、其下「侧栏 + 工作区」一行；品牌区宽度与侧栏同步、右缘对齐连线，顶栏下缘=侧栏上缘，消除原侧栏 logo 区与页眉两条错位分割线的交界台阶；logo 折叠触发器（FR-181）迁入顶栏品牌区。删除页眉节点作用域下拉（`selectedNodeId` 数据面保留，全部服务器页自带节点筛选/命令面板/向导预填照常）；顶栏高度与 logo/搜索框尺寸下调（见 ADR-071，免 spec）

### FR-335 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

群组拓扑规模化（feat，增强 FR-145，验收分诊：拓扑页每 proxy 拉 registration + 列表页每 network 拉详情双 N+1；SVG 纵向线性堆叠超高无缩放/筛选）：后端新增拓扑聚合端点（一次返回全部 proxy 及注册关系）+ NetworkSummary 内联成员健康计数；前端拓扑单查询消费 + SVG 视口控件（pan/zoom/适应视图/搜索/状态筛选/分层布局）→ `docs/specs/topology-scale/spec.md`

### FR-336 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

用户目录服务端搜索分页 + 候选下拉虚拟化（feat，增强 FR-003/156，验收分诊：`/users` 前后端均无搜索/分页，共享 Combobox 无虚拟化，用户量大时成员弹窗全量挂载）：后端 `GET /users` 加 `q/limit/offset` 返 `{items,total}`（兼容旧无参全量）；共享 Combobox 引入窗口渲染；群组成员弹窗默认只显已选+少量候选、键入走服务端搜索 → `docs/specs/users-search-pagination/spec.md`

### FR-337 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

任务中心列表分页信封（feat，增强 FR-183，验收分诊：任务接口固定 Limit(100) 返裸数组无 total/offset，前端整批渲染无翻页/总数，规模化既看不到全量也一次全挂）：后端 List 加 Count+offset 返 `{items,total,limit,offset}`（对齐 FR-247 分页约定）；前端「共 N 条」+加载更多/无限滚动（保 FR-329 非终态轮询行为）；devmock 同步契约 → `docs/specs/tasks-pagination/spec.md`

### FR-338 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

备份存储配置编辑（feat，增强 FR-057，验收分诊：备份存储自交付起前后端均无 Update，改配置只能删了重建）：后端 `PUT /backup-storages/:id`（复用既有 validateCandidate/凭证 ${ENV} 引用校验/名称冲突校验，404/422 语义与 Create 一致）；前端编辑弹窗（受控填入现值、区分 create/update）+ 表格编辑入口；devmock 同步 → `docs/specs/backup-storage-update/spec.md`

### FR-339 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.17.0）

实例控制台玩家/备份定时分区接真（feat，FR-269 首版占位归真，验收分诊：两页签停留「第一版原型」静态占位而后端/独立页面/hook 均已就绪）：抽实例作用域 `InstancePlayersSegment`（复用 useOnlinePlayers/useBans/useWhitelist 等）与 `InstanceBackupSegment`（useSchedules(id)+useBackups(id)）接入控制台页签；短期不能全接的分区从 TAB_KEYS 撤下不留假入口；清理占位文案 → `docs/specs/console-players-backup-segments/spec.md`

### FR-340 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.17.0）

指标批量序列接口（feat，增强 FR-060/270，2026-07-15 验收分诊：节点详情实例对比对 N 个实例逐条 `GET /metrics/series` 造成 N+1 请求风暴且 30s 重拉）：后端新增多目标批量序列端点（一次查库按 targetId 分组返回 `{targetId→series[]}`，逐目标复用既有实例访问鉴权）；前端 NodeInstanceCompare 改单条批量查询 + 对比目标数上限；节点内实例清单改用 FR-247 已交付的聚合/搜索接口（gap:FR-270 归真一并收口）→ `docs/specs/metrics-batch-series/spec.md`

### FR-341 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.17.0）

daemon 实例在 Worker 重启后存活并自动重连（fix，ADR-003 闭环，2026-07-15 验收 FR-325 真机复现：`systemctl restart` 后 daemon wrapper 与游戏服一起死、命中「清理残留」而非「接管重连」）：定位三叠加杀手并各个击破——K1 wrapper 的 stdout/stderr 断管 SIGPIPE 崩溃（主因，`worker daemon` 子进程启动即 `signal.Ignore(SIGPIPE)`、Unix-only）；K2 systemd cgroup 连坐（worker unit 加 `KillMode=process`，同步内嵌副本+守护测试）；K3 优雅停机 90s 无界挂起（`GracefulStop` 设 5s 上限、超时强制 `Stop()`，确保 `StopAll` 的 daemon 优雅断开及时执行）；不回归 FR-310（删运行中实例仍强杀两棵进程树）→ `docs/specs/daemon-survival/spec.md`

### FR-342 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.18.0）

搭建失败损毁态 + 一键重建（feat，增强 FR-319 异步搭建，真机反馈：断网/下载核心失败后实例只留 STOPPED+statusReason「搭建未完成」，无重建入口、只能删了重填参数）：新增实例状态 DAMAGED（损毁），任何搭建阶段失败（下载/校验/配置写入/探针部署/Forge 安装器）都进损毁态并保留原始搭建参数；新增「重建」动作复用已存配置重跑搭建（成功→STOPPED、再失败→仍 DAMAGED），零重填；状态机加 DAMAGED 转移 + 重建 RPC/端点 + 前端损毁态徽章与重建按钮（需 spec）→ `docs/specs/provision-damaged-rebuild/spec.md`

### FR-343 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.18.0）

实例系统级基础指标（无需探针，走系统直取）（feat，增强 FR-170/142，真机反馈：实例未连 ServerProbe 时 TPS/MSPT/CPU/内存无源、概览显示占位）：把 Worker 侧已采集的 gopsutil 进程 CPU%/RSS 内存/运行时长（FR-170 进程指标）暴露到实例详情 API + 控制台概览，作为不依赖探针的基础指标底座；无探针时仍显真实 CPU/内存/运行时长，TPS/MSPT/在线数标注「需探针」而非假值（需 spec）→ `docs/specs/instance-system-metrics/spec.md`

### FR-344 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.18.0）

实例环境变量管理（双区展示 + 可编辑写入 .env）（feat，增强 FR-034 搭建/结构化启动，用户要求「加环境变量 tab、支持自配启动环境变量写入 .env」）：实例详情新增「环境变量」页签——上区可增删改自定义启动环境变量、保存写入工作目录 .env 文件、下次启动注入生效；下区展示运行中 JVM 实际完整环境（Worker 上报启动时所用 env，含继承 PATH/JAVA_HOME，只读，停机提示仅运行时可读）；env 编辑 API + .env 文件契约 + 启动管线注入 + 前端页签（需 spec）→ `docs/specs/instance-env-vars/spec.md`

### FR-345 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.18.0）

未运行/历史日志查看（终端回放 DB 历史 + 独立历史增强）（feat，增强控制台终端与日志能力，真机反馈：手动关服后终端变黑看不到关服过程、停止态无历史）：实例停止/崩溃时终端自动从 DB 回放最近 N 行历史（可上滚、重连/切页签/刷新不丢），关服不清屏；实例级历史日志查看支持时间筛选/搜索/导出（在现有 Logs 能力上加实例入口）；软依赖 FIX-3（重启优雅关服使关服日志先存在）（需 spec）→ `docs/specs/instance-log-history/spec.md`

### FR-346 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.18.0）

客户端分发上传协议增效：秒传预查 + 小文件聚合 + 有限并发（feat，增强 FR-250/251，真机反馈：数千文件整合包发布逐文件串行 init→chunk→complete 请求过多）：新增批量 sha256 预查端点（前端上传前算哈希、一请求查 N 个、命中制品库 CAS 者跳过上传直接引用既有制品）+ 小文件聚合上传端点（一请求携带多个小文件，免三段协议）+ 前端文件级有限并发与进度整合；大文件保留 FR-251 分块协议（需 spec）→ `docs/specs/client-upload-efficiency/spec.md`

### FR-347 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.18.0）

制品库外置对象存储底座：BlobStore 抽象 + 本地/S3 适配器 + 存储渠道配置页 + 302 预签名下载（feat，增强 FR-088 修订 ADR-011 存储节，用户诉求：制品上传自部署 rustfs 做数据分发、CP 不存大对象）：抽 BlobStore 接口（Put/Open/Stat/Delete/List/Presign，后续新后端只加适配器）——本地适配器封装现 CAS、S3 适配器复用 worker SigV4 纯标准库实现并补 presign；新表制品存储渠道 + 「文件存储配置」管理页（CRUD/连通测试/设活跃渠道；凭证面板直填可逆加密存储，沿 FR-192 先例）；Asset.StorageBackend 按记录路由下载——local 走 ServeContent、s3 返 302 预签名短时效 URL（updater 已开跟随重定向，真机必验）（需 spec + ADR）→ `docs/specs/artifact-external-storage/spec.md`

### FR-350 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.18.0）

发布编排器文件树定点增强：多级目录一次建 + 右键定点添加（feat，增强 FR-191/250/261）：「新建文件夹」支持输入 `a/b/c/d` 自动创建整条层级、支持多行一次建多个；右键目录菜单增「上传文件到此 / 上传文件夹到此」，所选内容落到该目录路径下（免 spec，纯前端——FileExplorer 右键基座与 emptyDirs 机制已有）

### FR-351 需求原文（优先级 — / 状态 ✅ 已交付@v0.19.0）

发版强制内嵌楔子与 updater-core（feat，增强 FR-193/发布管线）：`make embed-client-updater` / `release.yml` 在 jar 缺失或 0 字节时硬失败并中文报错；正式包禁止缺件；本地 dev 仍可缺 jar 启动并返回 `JAR_NOT_EMBEDDED`（免 spec）

### FR-354 需求原文（优先级 — / 状态 ✅ 已交付@v0.19.0）

Docker Compose 与镜像部署可跑通（feat，对外 compose 路径产品化）：打磨 `Dockerfile.control-plane` / `Dockerfile.worker` + `docker-compose.yml` + `make docker*`；校正 compose env 与 Worker/CP 配置键一致；`docs/DEPLOY.md` Docker 专节；验收=干净环境 `compose build && up` 后浏览器可达 CP 并进入登录/引导，配置键错误不静默；**不**要求本 FR 内 UI 见 Worker 在线、**不**推 GHCR/K8s（需 spec）→ `docs/specs/docker-compose-deploy/spec.md`

### FR-359 需求原文（优先级 — / 状态 ✅ 已交付@v0.19.0）

分发三页跨页深链与统一筛选状态（feat，增强 FR-265/264）：`channelId`/`ip`/`machineId`/`errCode`/`version`/`tab` 等 query 在频道工作台、分发监控、安全中心互通；分布/榜单/事件一键跳转保留筛选；不改后端业务语义（免 spec）

### FR-362 需求原文（优先级 — / 状态 ✅ 已交付@v0.19.0）

Bot 发压节点池与分布式调度底座（feat，增强 FR-038/042/274，500+ Bot 地基）：将目标游戏实例与执行 Bot 的 Worker 节点解耦，CP 发现具备 Node/bot-worker 运行时的可用发压节点及容量，启动前生成跨 Worker 分片计划并做容量预检；批量、幂等地下发创建/停止与逐批回执，容量不足或节点失联时明确拒绝/降级，不产生无主 Bot 记录。真机门禁（2026-07-21 用户确认缩比）：≥2 真 Worker、>50 跨节点分片预检、缩比真实 connected（非仅 DB 行）、容量不足 ready=false、stop 与故障区分；满规模 10 Worker/500 真连为可选扩容（需 spec + ADR-074）→ `docs/specs/bot-distributed-load/spec.md`

### FR-363 需求原文（优先级 — / 状态 ✅ 已交付@v0.19.0）

可验证 Bot 场景引擎与塔防预设（feat，增强 FR-274）：结构化场景支持按比例分组、固定随机种子、同步屏障，以及 `wait_spawn` / 主城区域漫游 / `send_command` / 等待探针事件 / `move_to_and_wait` / 锁敌追击 / `attack_until` / 死亡重生与重新进房；每步具备超时、重试、取消和成功/失败结果，兼容代理跨服与单服多房间。塔防预设支持「主城行走→命令进房→同步开局→抵达区域→持续普通攻击」；首版不含技能、放塔、商店和复杂背包（需 spec，依赖 FR-362）→ `docs/specs/bot-scenario-engine/spec.md`

### FR-365 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0）

Bot 长稳重连、进程恢复与状态归真（feat，可靠性增强）：Bot 被踢/断线后按指数退避+随机抖动自动重连；bot-worker 子进程崩溃后恢复本节点期望 Bot 集合，Worker 重启/短暂离线后由 CP desired-state reconcile；状态带序号与时间，进程死亡后立即收敛而非残留旧 `connected`，恢复后重走当前阶段幂等入口。真机验收覆盖批量踢出、杀 bot-worker、重启 Worker、服务端离线后恢复，且无重连风暴、幽灵在线、无界定时器和持续内存增长（需 spec，依赖 FR-362；可与 FR-364 并行）→ `docs/specs/bot-runtime-recovery/spec.md`

### FR-366 需求原文（优先级 — / 状态 ❌ 已废弃）

压测运行状态机、三类负载曲线与自动判定（feat）：分离可复用场景模板与每次运行记录，支持固定 500+ 长稳、阶梯升压找最大稳定并发、突发登录/开局/攻击洪峰；运行状态覆盖预检/启动/运行/降级/停止/完成/失败/取消，聚合 Bot 在线率、连接耗时、动作成功率、重连与错误，以及 Worker CPU/RSS、节点资源和目标服 TPS/MSPT，按阈值自动给出 verdict、最大稳定容量和安全停止。默认严格验收：500+ 连续 60 分钟，在线/进房/抵达/攻击成功率均 ≥99%，TPS≥19、MSPT p95≤50ms、Bot/Worker/目标服无崩溃；阶梯与洪峰报告含 p50/p95/p99 并区分目标服/发压端/网络/动作失败（需 spec，依赖 FR-362~354）→ `docs/specs/bot-load-runner/spec.md`

### FR-367 需求原文（优先级 — / 状态 ❌ 已废弃）

压测场景模板与创建向导前端（feat，完整 Bot 压测配置体验）：Bot 管理信息架构拆为舰队/压测会话/场景模板；五步向导覆盖目标与节点池、连接配置、分组与行为、负载曲线、阈值与预检，结构化编辑为主并保留高级 YAML，支持保存/复制/恢复模板、容量分片预览、预计时长和风险提示。验收要求无需手写 YAML 即可配置塔防场景，500 Bot 容量不足时启动前明确缺口，5000 条 Mock 数据不全量铺开，并通过中英 i18n、暗亮主题、键盘与可访问性验收（需 spec，依赖 FR-362/352/355 API 契约）→ `docs/specs/bot-load-wizard/spec.md`

### FR-368 需求原文（优先级 — / 状态 ❌ 已废弃）

实时压测观测、故障诊断与报告前端（feat，完整 Bot 压测运行体验）：会话详情提供实时概览/Bot 明细/性能指标/失败诊断/事件流/配置，消费会话级聚合推送而非为 500 Bot 各开浏览器 SSE；展示连接漏斗、阶段进度、节点分布、TPS/MSPT、CPU/RSS、动作成功率和错误分类，支持失败 Bot 下钻、只重试失败子集、停止/取消及 JSON/CSV 报告导出，大列表分页/虚拟化且筛选可深链。真机验收要求 500 Bot 运行期间持续更新无需手刷，可定位到 Worker/Bot/步骤/探针事件/错误，并完整展示三类测试结果与最大稳定并发；中英、双主题、键盘和屏幕阅读器全过（需 spec，依赖 FR-366；可与 FR-367 并行）→ `docs/specs/bot-load-observability/spec.md`

### FR-369 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0）

通用 Bot 命令编排与场景边界修订（feat，取代 FR-364，增强 FR-363/354）：新增 `command_schedule` 相对时间命令、稳定 commandId/occurrence checkpoint、绝对计划时间、本地集中 scheduler、取消、固定有限重试和结果聚合；`send_command`/命令发送成功仅表示 `bot.chat` 未同步抛错，不等待服务器回执。ServerProbe、塔防插件、TPS/MSPT 与业务事件仅为独立可选观测，不改变命令结果、preflight 或默认 verdict；既有 Scenario V2/塔防预设保留为显式 legacy，真实业务事件数据源不作为本批交付前提（需 spec + ADR-075，依赖 FR-362/352，可与 FR-365 非依赖部分并行）→ `docs/specs/bot-load-probe-events/spec.md`、`docs/specs/bot-load-platform/super-spec.md`

### FR-370 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0）

通用命令压测运行状态机、负载曲线与自动判定（feat，取代 FR-366）：模板冻结 commandSchedule/loadProfile/thresholds；支持 stable/step/spike，聚合在线、命令发送、调度 lag、可选屏障和 Worker 健康，输出 verdict、maxStableBots、失败分类与 JSON/CSV 报告。默认适用指标均 ≥99%、schedule lag p95≤1 秒、非预期 crash=0；无屏障指标为 not_applicable，TPS/MSPT/业务事件不进入默认判定（需 spec，依赖 FR-362/352/354/358）→ `docs/specs/bot-load-runner/spec.md`

### FR-371 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0）

通用命令压测模板与创建向导前端（feat，取代 FR-367）：Bot 管理拆为舰队/会话/命令模板；五步向导覆盖目标实例、连接配置、命令编排、负载曲线和阈值预检，支持结构化编辑/高级 YAML、模板 CRUD、容量分片与风险预览；不要求业务字段或 ServerProbe，5000 条 Mock 数据保持分页/虚拟化并通过中英、双主题、键盘与可访问性验收（需 spec，依赖 FR-362/352/359 API 契约）→ `docs/specs/bot-load-wizard/spec.md`

### FR-384 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0·真机签发/鉴权/吊销）

Agent Token 模型 + CP 侧鉴权（feat）：`agent_tokens` 表 + `POST/GET/DELETE /api/v1/agent/tokens` 颁发/吊销/列表 + `agent_token` 中间件（独立于人类 JWT）+ 策略引擎真源（默认只读 + 写白名单 + scope 收敛 + 硬拒绝面）+ 审计 `actor_kind=agent` 贯通；CP 为唯一真源，jm-agent/mcp-bridge/curl 三入口共享同一闸（需 spec + 新 ADR-079，跨 middleware/service/router/model）→ `docs/specs/agent-token/spec.md`

### FR-385 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0·真机 whoami/启停）

jm-agent 管理 CLI（feat，仿 jmctl）：独立 Go 二进制 `apps/jmagent/`，仅引入 HTTP 客户端与标准库，不链 gRPC/DB；子命令 MVP = `whoami/list nodes/list instances/instance status\|metrics\|logs/start\|stop\|restart/node maintenance enter\|leave`，写白名单仅含 `instance.life`+`node.maintenance`；非交互 `--output json` 给脚本/CI；403/硬拒绝文案稳定；Token 仅 env 与 `--token` 传入不落盘（需 spec + 新 ADR-079，依赖 FR-384；建议并行 worktree）→ `docs/specs/jm-agent-cli/spec.md`

### FR-387 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0）

Agent Token 管理 UI（feat）：设置 → Agent Token 页 = 列表 / 新建 / 详情 / 吊销 / 复制命令片段；非管理员 403；UI 契约仅附在 FR-384 spec 末尾（免 spec：纯前端消费 FR-384 API，无新模型/新契约/新 ADR，依赖 FR-384；建议并行 worktree）

### FR-388 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0）

Agent 入口可证安全闸（feat）：同 Token 同输入行为契约表（**CLI ↔ CP-MCP(Streamable HTTP/SSE) ↔ curl**，原 mcp-bridge 入口退役）+ 真机复验矩阵（≥30 条断言：scope 外/Token 吊销/过期/写白名单外/硬拒绝面[用户写/删实例/DB 浏览/自更新/审计删除]/并发两入口同时写/速率限制）+ CI agent-gate 闸 + 契约入 `docs/API.md`（需 spec，依赖 FR-384/385/**389** 完成后起跑；扩展见 FR-389 批）→ `docs/specs/agent-safety-gate/spec.md`

### FR-389 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0·真机 initialize/tools/call/踢线（**会话运维模型由 FR-489 取代**：协议会话已移除，SSE 兼容路径保留））

CP 内嵌 MCP 长连接服务（feat，取代 FR-386 远程入口形态）：Control Plane 提供 **Streamable HTTP（主）+ 兼容 SSE** MCP 端点；鉴权复用 Agent Token（FR-384）；工具集与 jm-agent 对齐、硬拒绝不注册；**可运维会话**：列表（token 名/前缀、IP、已连时长、最近 tool、传输类型 streamable_http\|sse）、强制踢线、空闲超时、绝对超时、每 Token/全局并发上限；会话态内存为主（单 CP 进程，HA 粘滞不在本期）；平台管理员 API 管理会话（需 spec + **改写 ADR-077** 为「CP 内嵌 MCP 网关」；依赖 FR-384）→ `docs/specs/cp-mcp-server/spec.md`

### FR-390 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0·真机 mcp/jmagent client 可区分）

Agent 调用流水与 Token 活跃（feat）：统一 `agent_call_logs`（或等价）记录 MCP tool 与 Agent HTTP API（含 jmagent）的 action、tokenId、client（`X-JM-Agent-Client`：mcp\|jmagent\|curl\|unknown）、IP、成功/失败、耗时、时间；读路径也记；保留窗口可配置（默认建议 7~30 天）；Token 鉴权继续 bump `lastUsedAt` 并提供 24h 调用次数聚合只读字段/接口；写操作可仍写既有 audit 并在 detail 标明 actor（需 spec，依赖 FR-384；可与 FR-389 并行）→ `docs/specs/agent-call-log/spec.md`

### FR-391 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0·DOM 7/7（**MCP 会话页被 FR-489 取代**：改为 Token 活动视图））

Agent 观测 UI（feat）：平台管理下 MCP 会话页（列表/踢线/超时说明）+ 调用流水页（按 token/action/client/成功失败/时间筛）+ Agent Token 列表展示 `lastUsedAt` 与 24h 次数 + 审计页可筛 actor=agent / action 前缀 `agent.`；复制「IDE 连接 CP MCP URL」片段；非管理员 403（免 spec：纯前端消费 FR-389/390 API，UI 契约附两 FR spec 末节；依赖 FR-389/390）

### FR-392 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0·mcp-bridge 删除 + CI/契约清理）

退役 mcp-bridge + jmagent 客户端标识（feat/docs）：删除 `apps/mcp-bridge` 与 stdio 文档路径；README/接入指引改为 CP MCP URL；`jmagent` 全局发送 `X-JM-Agent-Client: jmagent`；构建/CI/发布物去掉 mcp-bridge；更新 FR-388 契约与 ADR-077 状态为 superseded by 改写版（需 spec 或并入 FR-389 退役章；依赖 FR-389 主路径可连后执行，可与 FR-391 尾段并行）→ 退役说明可落 `docs/specs/cp-mcp-server/spec.md` 退役章

### FR-393 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0）

节点离线归档列表与详情（feat，增强 FR-048 软删可发现性）：主节点列表仅活跃节点；`NodesPage` 增加「归档」分段/Tab，列出已软删下线节点并只读查看摘要（名/host/原状态/下线时间等）；在线下线与名下实例守卫沿用 FR-048/309（在线拒绝、未 force 的 409 清单）。验收：离线下线后主列表消失、归档可见；在线仍拒；compose 1CP+1Worker 真机走通（需 spec）→ `docs/specs/node-offline-archive/spec.md`

### FR-394 需求原文（优先级 — / 状态 ✅ 已交付@v0.20.0）

归档节点彻底清理（feat，增强 FR-309 清理闭环）：归档区对已下线节点提供「清理」= 硬删除节点库记录；有残留实例记录须 force 确认后级联删实例平台记录（**不清理远端文件**，文案明示）；DangerConfirm 输入节点名；写审计。验收：清理后主列表与归档均不可见；未 force 有实例则拒绝；compose 真机验（需 spec，依赖 FR-393 UI 入口；硬删 API 可与 FR-393 后端并行）→ `docs/specs/node-offline-archive/spec.md`（同目录清理章）

### FR-395 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.21.0）

Agent 能力策略 v2 与节点继承实例作用域（feat，增强 FR-384/389）：把 `write_allowlist` 扩为强类型能力分组（节点读/操作/破坏性、实例读/生命周期/命令/搭建配置/内容/破坏性、Bot 读/管理/压测、观测读），节点 scope 自动覆盖该节点当前与未来实例，实例 ID scope 继续支持最小授权；旧 Token 迁移后不扩权，未知动作默认拒绝；`tools/list` 按能力裁剪，调用流水记录能力与目标；永久禁区仍为用户/组/RBAC、Agent Token 管理、密钥明文、数据库浏览、自更新、平台设置（需 spec + 新 ADR 取代 ADR-076 对 Agent 硬拒绝面的旧定义，并修订 ADR-077；被 FR-396~399 依赖）→ `docs/specs/agent-capability-policy-v2/spec.md`

### FR-396 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.21.0）

MCP 节点与实例全生命周期强类型工具（feat，增强 FR-005/302/314/342/389）：在 FR-395 策略闸内开放节点详情/指标/维护/排空/Docker 与运行时可用性/归档清理，以及实例搜索/指标/日志/崩溃快照/创建搭建/导入克隆/配置/启停重启重建/命令/批量/强杀/删除；全部直接复用 CP service 与现有状态机、启动预检、内存水位和任务闸，不经本机 HTTP 回环；强杀、删除和节点清理须独立 destructive 能力 + 服务端精确确认；不开放返回明文 enrollment token 的节点准入签发（需 spec，依赖 FR-395）→ `docs/specs/mcp-instance-operations/spec.md`

### FR-397 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.21.0）

MCP 文件、配置与插件运维强类型工具（feat，增强 FR-008/031/052/373/389）：开放目录/元数据/文本读写/权限预检与 chmod/移动删除/历史 diff 回滚，配置发现/字段与文本写入/校验/历史，以及从既有制品 assetId 部署、启停、删除插件；控制与数据分离——MCP 不承载大型 base64/jar/世界包，改签发短时单用途且绑定 Token/scope/实例/path 的流式 HTTP 传输票据；保留路径越界、二进制与文本体积护栏、原子落盘和危险确认（需 spec，依赖 FR-395；可与 FR-396/398 并行）→ `docs/specs/mcp-content-operations/spec.md`

### FR-398 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.21.0）

MCP Bot 舰队与压测编排全工具（feat，增强 FR-362/369~372/389）：普通 Bot 列表/详情/创建/停止/删除/行为/命令，压测模板 CRUD 与结构化 JSON/YAML 命令计划/负载曲线/阈值，以及运行创建→发压节点容量→预检→启动→状态→停止→失败子集重试→分页 Bot/失败/事件/指标→JSON/CSV 报告全部由强类型 tool 覆盖；目标实例与每个执行节点均过 FR-395 scope，planToken 保持不透明，5000 条明细走分页/游标，命令成功边界继续遵循 ADR-075（需 spec，依赖 FR-395）→ `docs/specs/mcp-bot-orchestration/spec.md`

### FR-399 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.21.0）

500 Bot 全链路资源采样与容量报告（feat，增强 FR-343/362/370）：压测运行同步采样目标服 JVM 进程 RSS/堆/CPU/运行时长（探针可用时附 TPS/MSPT）、各发压节点 Worker RSS/bot-worker RSS/节点内存与 Bot 数，以及在线率/命令发送/调度延迟/失败/重连；报告按阶段输出 baseline、峰值、p95、增量、每 Bot 内存斜率，分别列目标服/各发压节点/集群总量，并区分「500 Bot 实测峰值」与「加 25% 安全余量建议」；目标 SSH 主机为 64 GiB 时保留 8 GiB 系统内存，不满足时只报告最大稳定实测规模；缺失指标标 unavailable 不以零冒充，MCP 可查快照/曲线/终态容量报告（需 spec，依赖 FR-398，资源字段跨 Worker/CP/报告/MCP）→ `docs/specs/bot-load-resource-capacity/spec.md`

### FR-400 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.21.0）

受管资源归因与首页仪表 Tooltip（feat，增强 FR-169/170）：修复观测刷新后，首页 CPU/负载/内存仪表提供采样时间、陈旧/离线态与可访问 Tooltip；按节点→受管实例→进程 TopN 解释资源占用并可下钻监控页，归因接口可查询已就绪共享 Bot Worker 的真实容量或缺测原因。仅覆盖节点、Worker、受管实例进程树与 Bot Worker，不采集任意 OS 进程；页面可见时 10s 刷新、隐藏暂停，复用既有 90s 新鲜度阈值（需 spec，依赖 FIX-观测刷新）→ `docs/specs/managed-resource-attribution/spec.md`

### FR-403 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.21.0）

日志中心平台与节点实例视图分流（feat，增强 FR-150/215）：同一路径分为「平台日志」（仅 CP）与「节点/实例日志」（Worker+实例，按节点/实例聚合筛选）两个主视图；平台管理员保留受控全量检索。复用既有日志表与筛选接口（免 spec）

### FR-404 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.21.0）

Bot 乱斗场景与受管稳定性压测（feat，增强 FR-274/352/398/399）：`attack_until` 加性支持死亡后受限重生恢复、混合玩家/敌对生物确定性随机目标、无目标寻路搜敌与客户端攻击活跃度；MCP `loadtest_run_create` 可提交冻结的 Scenario V2。使用受管隔离 Paper + ServerProbe 运行 1 Worker × 50 Bot × 1 小时，只验证客户端攻击活动、重生恢复和资源稳定性，不把它表述为服务端伤害/击杀或 TEST-500（需 spec，依赖 FR-398/399）→ `docs/specs/bot-combat-soak/spec.md`

### FR-406 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.21.0）

监控页全景观测整合与默认多指标对比（feat，增强 FR-221/400/401/402）：`/monitor` 成为平台/节点/实例观测入口，默认选中对比指标，展示平台健康、异常 TopN、资源归因、共享 Bot Worker 与受管进程并支持 URL 下钻；首页健康与共享 Bot 区块保留在 `/`（免 spec）

### FR-409 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.21.0）

制品版本库与 ServerProbe 在线分发（feat，增强 FR-045/068/114/173/174）：在既有 CAS `assets` 上新增通用制品包 / 来源 / 版本层；首个包 ServerProbe 由 CP 同步 GitHub Releases、校验并缓存 jar，Worker 从 CP 拉取。版本按全局默认 → Worker 默认 → 实例显式覆盖解析；Worker 改默认仅影响新实例，已有实例须手动切换以升级或回滚。不再嵌入 ServerProbe 源码、jar 或依赖缓存（需 spec + ADR）→ `docs/specs/artifact-version-library/spec.md`

### FR-410 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.21.0）

Linux 用户级 systemd 版本化直装与节点部署（feat，增强 FR-277/282）：操作机 SSH 推送 CP 或 Worker 至 Linux 普通用户，二进制按版本/UTC 时间/摘要落入永久保留的不可变目录，`current` 原子指针决定运行版本；稳定保留 `data`、CP 配置与 Worker 身份，旧裸二进制布局首次自动迁移；新增 CP / Worker 显式回滚脚本。仅覆盖 user unit，不引入公开镜像、Docker Compose 生产部署或 Kubernetes（需 spec + ADR）→ `docs/specs/versioned-user-systemd-deploy/spec.md`

### FR-412 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.22.0）

实例顶栏瘦身与实例切换器（feat，增强 FR-293/296）：删除 `服务器控制台 /` 前缀与无信息副标题，7 格 MetaCell 网格改为一行 pill 指标条（可收起并记住偏好），实例名即切换器（复用最近/收藏 + 模糊搜索），切换命中热集不重建终端连接；顶栏无横幅时 ≤ 90px（免 spec）

### FR-413 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.22.0）

实例 Tab 重组（feat，增强 FR-037）：「环境变量」并入「文件配置」形成分段，10 → 9 个 Tab 并加图标与分组分隔线，1366px 宽下不横向滚动；`?tab=env` 旧深链自动落到新分段（免 spec）

### FR-414 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.22.0）

终端热集上限治理（feat，增强 FR-296/ADR-067）：`HOT_SET_SIZE` 由写死 3 改为可配（默认 6），按实际可见终端数动态提升保活集，闲置断连策略随之调整，为分屏与多 window 场景铺底（免 spec）

### FR-416 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.22.0）

命令栏智能输入（feat，依赖 FR-415）：历史按实例持久化（上限 500、相邻去重）、`^R` 模糊搜索、候选式 Tab 补全（命令 + 第二段玩家名，ghost 预览不盲补）、多行粘贴确认（逐行/仅首行/取消）（免 spec）

### FR-417 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.22.0）

输出区观察能力（feat，依赖 FR-415）：日志级别过滤、行内搜索与高亮计数、跳到底部浮标、复制范围菜单（选区/可见屏/全缓冲/仅错误/存 .log），并为停机日志回放、崩溃诊断、启动失败横幅三处补复制按钮（免 spec）

### FR-424 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.22.0）

宽表格列宽与稀疏表治理（feat，依赖 FR-422）：宽表格显式约束列宽与总宽上限，避免主列无限扩张导致名称与元数据之间断开视觉连接；超宽稀疏表（在线玩家、白名单等 2 列表）改行卡片形态，复用 FR-195 的行卡片范式（免 spec）

### FR-425 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.22.0）

分发三页统一自定义时间筛选（feat，增强 FR-265/357/359）：频道工作台/分发监控/安全中心统一时间范围组件——保留既有预设档并新增任意起止日期时间（后端 from/to RFC3339 已支持，纯前端组件化），URL 深链同步进 FR-359 体系；区间超 14 天明细保留窗时自动标注「活跃机器为近似值」（消费 activeMachinesExact；免 spec）

### FR-429 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.22.0）

分发三页布局统一重排（ref，依赖 FR-425~428）：信息架构=洞察卡→时间筛选→概览数字→趋势/热力→明细；随 FR-430 两页化收敛（免 spec）

### FR-433 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

实例角色可变更与 beacon 配套服务角色（feat，增强 FR-032/ADR-007）：`PUT /api/v1/instances/:id` 支持 `role`（非法值 400 拒绝，与 Create 的 grandfather 回落语义刻意不同），MCP `instance_update_config` 暴露 `role` 枚举；新增 `beacon` 角色标识配套服务实例（非 MC 群组服角色：不参与 proxy↔backend 拓扑、不适用 MC 探针与后端注册、不参与玩家查询）。role 变更不做隐式级联——原为 proxy 的 `server_registrations` 保留，避免误删运维数据。**真机验收通过**（隔离环境 E2E CP/Worker）：①MCP `instance_update_config` schema 暴露 `role` 且枚举完整（`backend/proxy/universal/beacon`、非必填）；②非法值经 MCP 与 HTTP 双侧均被拒（MCP 回「无效的实例角色: superadmin（可选 backend/proxy/universal/beacon）」，HTTP 回 400 `INVALID_REQUEST`），与 Create 的 grandfather 回落语义确不同；③`proxy`/`backend`/`beacon` 三角色均可设并回读；④no-cascade 证实——实例由 proxy→universal→proxy 后其 `server_registrations`（proxyId=1/backendId=3）仍在，未被级联删除

### FR-434 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

群组服 Network 与代理注册的 MCP 工具（feat，增强 FR-032/335）：补齐 MCP 侧群组管理与拓扑能力——`network_list/get/create/update/delete`（删除需 `confirmName` 精确确认）、`network_add_members/remove_member`、`topology_get`（一次聚合代理注册 + 群组成员）、`registration_list/create/delete`；权限与实例分组同面（读 `instance.read`、写 `instance.write`），新增 `networkDomainActions` 独立 action 目录。使 MCP 客户端可完整维护群组服拓扑，不再依赖人工登录面板。**真机验收通过**（隔离环境 E2E CP/Worker）：network 全生命周期闭环（create→list→get→add_members→get 复查→update 改名→remove_member→delete 名不符正确拒绝→delete 精确名成功→list 复查空）；`topology_get` 一致；`registration_create/list/delete` 在 proxy↔backend 上闭环；对非代理实例调 `registration_list` 正确报「目标实例不是代理」

### FR-435 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

实例分组 MCP 工具（feat，增强 FR-165/ADR-033）：`instance_group_tree/members/create/update/delete/add_members/remove_members` 七个工具，让 MCP 客户端可按大区/小区等结构树做批量运维；分组仅承载运维归类，不承载权限或配额（读 `instance.read`、写 `instance.write`）。**真机验收发现缺陷**（隔离环境 E2E CP/Worker）：`instance_group_tree/members/create/update/add_members/remove_members` 六项通过（create 挂树、tree 回读、add/remove 成员、update 改名+移根、members 回读均正确）；但 **`instance_group_delete` 恒被拒**（连空分组 + 精确 `confirmGroupName` 也回「能力/scope 不足或操作被拒绝」）。根因：`mcp/tools.go` 的 `callRegisteredTool` 对带 `ConfirmField` 的工具先调 `authorizeDestructiveTarget`，该函数按 descriptor 的 `ResourceType=instance` 把**分组 id 当实例 id** 填入 `target.InstanceID`，`Authorize→principalCanAccessInstance(p, groupId, 0)` 因实例 scope 不含该 id（且 `NodeID=0` 使节点 scope 兜底失效）而必拒——任何 token 都无法删除分组。修法：非实例资源（分组/群组）改用节点 scope 授权，或在 `authorizeDestructiveTarget` 按 action 区分目标类型。同病的 `network_delete` 因未走 `ConfirmField` 路径（自带 confirm）未受影响。**缺陷已修复**：`authorizeDestructiveTarget` 对分组/群组等「容器」目标改按**节点 scope**授权（`PrincipalHasNodeScope`），精确确认名改从分组/群组领域服务解析（新增 `InstanceGroupService.Get`）；回归测试 4 项（含「分组 id 与实例 id 相同」碰撞）全绿，真机复核（重建 E2E CP）删非空→`分组非空`拒绝、删名不符→拒绝、**删精确名→`{"ok":true}`** 通过

### FR-436 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.23.0）

daemon 实例 wrapper 已死而 Java 仍活时的孤儿接管（fix，缺陷修复 FR-325 同源场景）：接管扫描原逻辑只检查 wrapper，见到 wrapper 不存活即删 PID 文件——而 wrapper 死、其托管 Java 仍活时（父进程变 init），活着的 Java 从此不可发现，继续占服务端口与 Paper `session.lock`、面板却显示 STOPPED 且实例再起不来（真机事故：控制面重启后 63 个实例集体失联）。现改为按 PID 记录强杀 Java 树，死透才清理、杀不死则保留 PID 文件待下次兜底。**真机验收通过**（隔离环境 E2E CP/Worker，主动构造该分支）：令实例 wrapper 单独 SIGKILL、Java 存活（PPID 变 1、服务端口仍监听、pid 文件陈旧指向死 wrapper），重启 Worker 触发接管扫描——日志逐条命中「wrapper 已不存活但 Java 仍活，按 PID 记录强杀 Java 孤儿」→「孤儿进程树已强杀并清理 PID 文件」；事后确认原孤儿 Java 确已死亡、全机孤儿（PPID=1 残留）计数 0、pid 文件被清理，实例由 CP 重连重推规格重新登记（状态 STOPPED，无泄漏）。修复前的旧逻辑只删 pid 文件、Java 变不可发现，本次强杀分支真机确认生效

### FR-437 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

压测容量快照陈旧导致的伪容量不足（fix，缺陷修复 FR-398）：①容量快照老化阈值 15s 仅比 bot-worker 10s 心跳多 5s 余量，一次 GC/负载抖动即判 `CAPACITY_SNAPSHOT_STALE`；②预检与容量查询工具走 15s TTL 缓存而非实时快照。二者叠加使节点明明有空闲容量仍报「可用容量 0」，需反复重试偶中。现阈值放宽到 30s（3 倍心跳），并让预检与 `loadtest_node_capacity` 优先经 `RefreshBotLoadCapacity` 取实时值。**真机验收通过**（生产 host-1，500 容量满载）：①`loadtest_node_capacity` 与预检均走 `RefreshBotLoadCapacity` 实时读——连续两次调用 `updatedAt`/`lastHeartbeatAt` 持续推进（非冻结）、`eventLoopP95Ms` 随负载变动，证明读的是实时快照而非陈旧缓存；②对满载节点发起预检，blocker 为 `BOT_LOAD_CAPACITY_INSUFFICIENT`（真实满载原因，`totalAvailable=0` 且节点 `unavailableReason` 为空），**未出现 `CAPACITY_SNAPSHOT_STALE`**——即「可用容量 0」是真实满载而非陈旧伪报，与修复前真机「明明有空闲却报 stale」的现象相反。心跳停摆的根因由 FR-438 分片化消除（FR-438 亦记录 `CAPACITY_SNAPSHOT_STALE` 现象不再出现），本项阈值 30s + 实时读共同保证不再误判

### FR-438 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.23.0）

bot-worker 大规模舰队的事件循环过载（fix，缺陷修复 FR-398）：Mineflayer 为每个 Bot 起 50ms 物理模拟定时器，数百 Bot 时单 Node 进程事件循环被占满（实测 500 容量下 p95 达 5s），keepalive 与心跳均无法调度——服务器集体判 `Timed out` 踢线，容量快照亦停止推进。现按行为判定关闭无需本地碰撞的 Bot 的物理模拟（`idle` 等不自行移动的行为默认关闭，`follow/patrol/guard` 保持开启），并把默认视距降到 `tiny`；另新增 `JM_BOT_WORKER_MAX_BOTS` 配置让容量上限可经 worker.yml 下发。**后续批（2026-09-21）继续压降并取得决定性进展**：①分片化——`bot_worker.shards` 由 6 提到 10，突破单进程单核上限（此前 6 片各 ~95–106% CPU 触顶，现 10 片各 ~41–44%，均值 5.4 核承载 500 Bot）；②内建插件裁剪——压测 Bot 用不到的 24 个 Mineflayer 插件（挖矿/合成/附魔/床/告示牌/粒子…）经 `plugins:{name:false}` 关闭，实测每 Bot CPU 0.713%→0.537%（-24.7%，注意 `digging` 因 pathfinder 经 `block.digTime` 依赖、`simple_inventory` 因 `inventory.js` 调 `bot.setQuickBarSlot` 而**必须保留**，禁用后者会直接崩）；③客户端视距 `tiny`→`2`（协议最小值），每 Bot 内存 31.7MB→16.6MB（-47.6%）；④服务端侧 `view-distance`/`simulation-distance` 10→4、`entity-broadcast-range-percentage` 100→50（20 个 Paper 服），并修正超编目标服 `max-players` 50→60、lobby 服 `spigot.yml` 的 `bungeecord:true` 阻碍直连的问题。**验证结论：500 Bot 全部在线且稳定（472 直连 MC 服 + 10 经 BC 代理，平台记账 activeBots=500），bot-worker 5.4 核 / 13.8GB，eventLoop p95 ~40ms，系统 40 核中用 ~20 核**；FR-437 的 `CAPACITY_SNAPSHOT_STALE` 与心跳停摆现象不再出现。**遗留**：18 个旧 `workerEpoch` 僵死 Bot 占用平台容量记账但不产生连接（`desiredState=running`/`status=error`，分片重启前残留），需平台侧提供失效 Bot 的回收能力

### FR-439 需求原文（优先级 P1 / 状态 ❌ 已废弃）

MCP 会话并发上限造成永久 429（fix，缺陷修复 FR-389）：`MaxSessionsPerToken=4`/`MaxGlobalSessions=32` 在客户端「重连建新会话但不关旧会话」时会耗尽配额，此后连 `initialize` 都建不了会话，只能重启控制面清空内存会话恢复。现并发上限语义改为「0=不限制」且为默认（当时会话生命周期还由空闲 30m/绝对 24h 超时兜底），`Normalize()` 不再把 0 回落到正数，需要限流的环境可显式设正值；同时 MCP 会话失效响应当时保留规范要求的 404 + `SESSION_GONE` 错误码，便于与「路由不存在」区分排障。**真机验收通过**（隔离环境 E2E）：对同一 token 连续发起 8 次 `initialize`（旧上限 `MaxSessionsPerToken=4` 下第 5 次即 429），实测 8/8 全 HTTP 200 且各得独立 `Mcp-Session-Id`、无 429——并发上限「默认不限制」生效

### FR-440 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

实例标签 MCP 工具（feat，增强 FR-395/047）：新增 `instance_update_tags` 工具与独立 action `agent.instance_update_tags`（V2 能力 `instance.write`，资源类型 instance），补齐 MCP 侧标签维护能力——现有 `instance_update_config` 未暴露 `tags` 字段，打标签只能绕开 MCP 走 HTTP，无法进 Agent 审计流水。三态语义：缺省=不改动、空数组=清空、非空=整体覆盖。`instance.read` 持有者可读（经 `agent_get_instance`），`instance.write` 持有者可写。**真机验收通过**（隔离环境 E2E CP/Worker）：三态各验一次（设标签→覆盖→清空为 `null`），且每次变更触发 FR-443 推送、Beacon 侧收到的 `jmTags` 与变更逐一吻合

### FR-441 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

通用二进制运行时搭建（feat，增强 FR-319/FR-342，见 ADR-090）：`provision_server` 新增 `coreType=binary`，支持三类来源——① 制品库既有 asset、② 远程 URL、③ 节点本地文件路径；二进制不适用 PaperMC 的版本/构建解析，改为「来源解析 → 下载/复制 → 落盘 → 写启动命令」四段式，全程复用 `TaskService.RunAsync`（新 `TaskKind=binary_provision`）以便前端看到下载进度与终态站内信。启动方式复用既有 `startCommand` 字段（不引入新概念），实例角色默认 `universal`。失败进 `DAMAGED` 并保留 `provisionSpec` 供重建。**真机验收通过**（隔离环境）：①`node_file` 来源搭建 30MB 真实 Beacon 二进制，落盘 `-rwxr-xr-x` 且 sha256 完全一致，实例经 wrapper 启动并完成真实初始化（生成 `beacon.db`/`config.yml`/`secrets/`，持续运行未崩溃）；②`url` 来源的 http 明文被正确拒绝（「url 必须为 https」），`https` 路径经 GitHub 实际下载 30593289 字节成功落盘可执行（`source=url`）；③`asset` 来源由单测覆盖（真机制品库为空，未走该档）

### FR-442 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

内置 Beacon 快速搭建预设（feat，依赖 FR-441，见 ADR-090）：在 `coreType=binary` 之上提供 Beacon 预设——制品来源优先级为「制品库已有版本 > Beacon GitHub Releases」，默认启动命令 `./beacon-1.1.0-linux-amd64`、默认角色 `beacon`（FR-433）、默认 JVM 无关（Go 二进制）。一键搭建后管理台可完成首次鉴权初始化。Beacon 与本平台为**可选协同**关系：未部署 Beacon 时本平台全部能力不受影响。**真机验收通过**：`coreType=beacon` 一键搭建产出 `role=beacon`、`jdkId=0`、`startCommand=./beacon-1.1.0-linux-amd64`（由落盘名派生）的实例；来源解析命中 GitHub Releases（E2E 制品库为空，走「制品库 > GitHub」的第二档，`source=url` 取件 30593289 字节落盘可执行）；实例启动后 Beacon 完成真实初始化（生成 `beacon.db`/`config.yml`/`secrets/`），配独立端口后**持续运行且 HTTP 200**，管理台 `POST /admin/v1/auth/login` 登录成功（首次鉴权初始化闭环）

### FR-443 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

向 Beacon 推送拓扑变更（feat，依赖 Beacon FR-222，见 ADR-090）：实例创建/删除/改归属（name/role/tags）时，若配置了 Beacon 协同端点则推送增量事件，使 Beacon 侧 server 归属与默认入口自动对齐，免去人工双写。**兼容非依赖**：未配置端点即整体跳过；推送失败只写审计与告警、**绝不阻塞或回滚本机操作**；投递尽力而为不做重试队列。实例启停不推送（Beacon 在线状态由 agent 心跳自维护）。**真机验收通过**：正向拉通 mock Beacon（收包含 `X-Beacon-Token` 与 `namespace:prod`，`create`/`ownership` 事件按变更触发，审计落 `instance.beacon_push_ok`）；端点不可达时本机操作**零阻塞**（改名 39ms 完成）且写失败审计；实例启停确认不推送

### FR-444 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

从 Beacon 拉取拓扑并映射为分组树（feat，见 ADR-090）：读取 Beacon 的区服结构（BC 集群 → 大区 → 小区）与 server 归属，映射为 JianManager 实例分组树（FR-165）并为实例补 `region:`/`zone:`/`role:` 标签（FR-440）。首次拉取为手动触发，避免自动建树与人工分组冲突；Beacon 不可达时返回明确错误且**不改动本地任何数据**。**真机验收通过**：空树 → 拉取创建 4 个分组（BC 集群→大区→小区 三层层级正确落库），本地不存在的 server 被跳过并列 `skippedServers`；二次拉取幂等（`createdGroups=0`）；审计落 `instance.beacon_pull_ok`；不可达时本地数据零改动

### FR-445 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

实例能力画像抽象（feat，见 ADR-091）：实例按「类型 × 角色」声明自身能力画像（`capabilities`：有哪些 Tab、依赖探针否、是否 MC 语义），前端据画像显隐内容而非 `if role==='proxy'` 硬编码；新增产品类型只需注册描述符。**验收**：新增一种角色/类型，详情页自动按其声明显隐 Tab，前端零硬编码改动；`generic`/`beacon`/`backend`/`proxy` 四种画像各自正确

### FR-446 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

MC 直探能力（SLP + Query）（feat，依赖 FR-451，见 ADR-092）：新增 Minecraft **Server List Ping**（零配置，取 MOTD/版本/在线人数/最大人数/favicon）与 **GameSpy4 Query**（需开 `enable-query`，取实名玩家名单/插件列表/地图名）协议客户端，不依赖探针。**验收**：对任意在跑 MC 服不装探针即可拿 MOTD/版本/人数；开启 query 后可拿实名玩家名单；明文 http 拒绝、超时可配

### FR-447 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

采集优先级与降级编排（feat，依赖 FR-446）：统一采集编排——**有探针走探针 → 无探针走直探 → 都没有显示"不可用"**（不报错、不伪装 `-1`）；同一指标多源时以探针为准。**验收**：同一实例三态（探针+直探 / 仅直探 / 都无）下详情页均正确呈现，无探针不留占位符垃圾值

### FR-448 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

详情界面按能力画像适配（feat，依赖 FR-445）：详情页按 role × type × 探针有无 三轴显隐 Tab 与内容；隐藏 MC 专有 Tab（世界/区块/TPS/插件）对非 MC 实例。**验收**：backend / proxy / beacon / generic 四种实例打开详情页，Tab 与内容各不相同且均合理

### FR-449 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

BC（proxy）专用界面（feat，依赖 FR-445/447）：BC 实例专属视图——子服列表与各自状态、跨服玩家分布与全网总数、BC 自身运行指标、BC 配置管理（config.yml 关键项）。**验收**：BC 实例可见上述四块，且与已有拓扑/注册关系数据一致、不重复维护

### FR-450 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

二进制 / beacon 专用界面（feat，依赖 FR-445）：非 MC 实例专属视图——进程运行指标（CPU/内存/线程/句柄/时长/重启次数）、端口与主动健康检查（HTTP GET/TCP 探活）、配置文件管理、启动命令/参数编辑；**保留基础文件目录管理与 CPU 等基础监控**；抽象到可适配更多产品。**验收**：beacon 与通用二进制实例均呈现该视图且无 MC 专有 Tab

### FR-451 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

实例配置源明面化（feat，见 ADR-092）：实例启动参数（startCommand/JVM 参数）与 `server.properties` 关键项在详情页**明面可编辑**，并支持**直接引用外部文件**（内联值 vs 文件引用二选一）；不再藏于犄角旮旯。**验收**：可在详情页直接改启动参数与关键配置项并持久化；可切换为引用文件模式且生效

### FR-452 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

实例列表树表 + 可切换分组维度（feat，依赖 FR-445）：实例列表由平铺卡片改为**多级可折叠树表**，分组维度**可切换**（标签 region/zone 默认 / 实例分组树 / 网络 / 角色 / 类型 / 节点 / 不分组）；无分组信息的实例（bc/beacon/独立服务）自然落「未分组」。**验收**：64 台一屏可管、点击分组头折叠展开、切换维度即时重排、未分组与二进制实例正确归位

### FR-453 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

拓扑页重做：完整网络视图（feat，依赖 FR-452）：拓扑由「仅已注册关系」改为**完整网络视图**（含未注册实例与配套服务），层级从所选分组维度推导（默认标签 region/zone），节点带健康着色与负载标签。**验收**：全部实例上拓扑（不再只 12 台）、层级正确、健康/负载可见、布局均衡

### FR-454 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

修正 beacon/binary 探针误配（fix，缺陷修复 FR-441/442）：binary/beacon 实例仍被分配 `ProbePort`，心跳每拍对它们抓 `/metrics` 必失败；`probe_update` 仅排除 `proxy`、未排除 beacon/binary，可对其推送无效 Bukkit jar。现两处补排斥；并归正线上 `beacon` 实例 `type`（`minecraft_java`→`generic`）。**验收**：binary/beacon 不再分配探针端口、心跳不再抓取、`probe_update` 对其返回明确拒绝

### FR-455 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.23.0）

重启韧性加固（fix/feat，缺陷修复 FR-325/436 同源，见 ADR-093）：①接管重试仅 3 次（约 7s）失败即 `reapOrphanWrapper` 强杀 wrapper+Java，会**误杀本可接管的健康服务器**——改为更长退避 + 存活复核（cmdline 校验）后才处置；②重推仅隧道 `n==1` 触发，CP 重启后可能漏推——补多触发点与幂等；③正向对账以心跳清单为唯一真源导致状态误判——收敛真源。**验收**：真机重启 CP/Worker 后已运行服务器**零误杀、状态不误判、重推不漏**；FR-436 的 wrapper 死/Java 活分支**真机验证通过**

### FR-456 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.23.0）

孤儿进程周期检测与兜底（feat，依赖 FR-455，见 ADR-093）：现有清理只发生在 Worker 启动时——`wrapper` 运行期死亡、`direct` 硬崩、`docker` 未恢复三条路径产生的孤儿/状态错位**无周期兜底**。新增运行期周期扫描 + 反向对账覆盖 direct/docker。**验收**：运行期制造 wrapper 死/Java 活、direct 孤儿、docker 残留，均在有限周期内被识别并按策略处置（不静默、不误杀）

### FR-457 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

集群级滚动/分批/灰度运维编排（feat，增强 FR-058/139）：`instance_batch` 现为无状态并发扇出（固定 16 并发、一次性、无分批/暂停/失败即停），无法安全滚动重启。新增**分批/间隔/失败即停/暂停继续/按比例灰度**语义与进度、取消。**验收**：可对 60+ 台执行「每批 5 台、批间隔 N 秒、失败即停」的滚动重启；有进度与取消

### FR-458 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

配置批量下发与跨实例收敛（feat，增强 FR-031）：配置链路现全单实例，跨实例只有只读端口冲突检查。新增「配置模板推送到 N 台」「跨实例配置漂移检测」「一键收敛到基线」。**验收**：可把一份 `server.properties` 推送到选定分组并在漂移时检出与收敛

### FR-459 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

实例健康巡检与自愈（feat，依赖 FR-455/456/462）：现仅「进程退出后按退避自动重启」，无假死检测（探针挂但进程在不会重启）、无集群级巡检。新增存活/响应巡检 + 受控自愈策略（自动重启卡死实例、崩溃熔断）。**验收**：模拟假死实例被巡检识别并按策略自动处置；策略可配、动作写审计

### FR-460 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

失效 bot 自动回收与补足（feat，增强 FR-398）：`workerEpoch` 不匹配的旧 bot（`desiredState=running`/`status=error`）占容量不产生连接，平台无回收能力（遗留 18 个僵死 bot）。新增自动回收 + 容量自动补足回目标数。**验收**：分片/CP 重启后僵死 bot 有限周期内被清、容量自动补回目标；回收动作写审计

### FR-461 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

集群健康总览墙（feat，增强 FR-402）：平台总览现只给计数 + TopN≤5，60+ 台无法一屏看谁异常。新增**逐台健康矩阵/热力墙** + 排序 + 一键定位。**验收**：60+ 台一屏呈现逐台健康态并可下钻到单台

### FR-462 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

异常自动检测（动态基线）（feat，增强 FR-011/085）：告警现仅静态阈值（且仅 node cpu/mem/disk）+ 节点离线。新增动态基线（EWMA/同环比）、突降突升、饱和度检测，并覆盖实例级指标。**验收**：注入缓慢劣化/突降，规则在无手工阈值下触发且噪声可控

### FR-463 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

SLO / 可用性统计（feat，增强 FR-011/220）：无 uptime 达成率 / MTTR / MTBF / 误差预算。新增按窗口的可用性与可靠性统计与展示。**验收**：可查看指定窗口内实例/平台的可用率、故障次数、平均恢复时长（平台可用率为**拍加权**；窗口内无任何可用证据即无探针实例时按 `applicable:false` 渲染「不适用」，不伪造数值——详见 `docs/specs/capacity-and-slo/spec.md` §4）

### FR-464 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

容量规划 / 趋势预测（feat，增强 FR-013/221）：无法回答"磁盘/内存按当前增速还剩多久"。新增时序外推与容量趋势告警。**验收**：给出关键资源的耗尽预测时间与置信区间

### FR-465 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

性能归因（GC 采集）（feat，增强 FR-060）：原始指标齐（TPS/MSPT/heap/threads/世界）但无归因；探针已暴露 `serverprobe_gc_*` 却未采集。先补 GC 采集入库，再做归因（TPS 低是 GC/区块/实体）。**验收**：GC 次数/耗时入时序；给出 TPS 劣化的主要贡献因子

### FR-466 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

服务器快照 / 一键回滚（feat，增强 FR-013/056）：仅有备份（需停服、链式回放）与文件/配置版本回滚，无整机快照 + 时间点回滚。新增快照与一键回滚闭环（回滚前自动建快照）。**验收**：可对实例打快照并从快照一键回滚，过程可追溯

### FR-467 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

运行期配额强制（feat，增强 FR-078/079/317）：组配额仅在 Create 校验、运行期不强制；Docker 资源限额仅 docker 模式生效。新增运行期 CPU/内存/磁盘配额强制与超限处置。**验收**：超限实例被识别并按策略处置（告警/限流/停止），非 docker 模式亦生效

### FR-468 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

二进制 / Beacon 版本管理与升级（feat，增强 FR-441/442，修订 ADR-090）：二进制/Beacon 重建只重取件、无版本比对/回滚/升级。新增二进制制品版本管理与受控升级（可回滚）。**验收**：可查看当前版本、升到指定版本、并回滚到上一版本

### FR-469 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

跨实例排行 + 玩家在线趋势（feat，增强 FR-340/054）：跨实例对比仅限节点内前 12、单指标；无玩家在线趋势分析。新增跨节点/跨实例全局排行与在线趋势/时段分析。**验收**：可按 TPS/MSPT/CPU/内存/在线排序全量实例；给出在线趋势与时段分布

### FR-470 需求原文（优先级 P2 / 状态 ✅ 已交付@v0.23.0）

崩溃诊断增强（feat，增强 FR-313/407）：崩溃仅有退出码+尾 200 行快照，无堆栈解析/同类聚合/OOM 关联。新增堆栈根因归类与崩溃趋势聚合。**验收**：连续崩溃被归类聚合、给出根因线索（OOM/异常类）

### FR-471 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.23.0）

在管实例运行态对齐、启动冲突预检与外来运行时接管（feat，增强 FR-302/326/455/456）：外部启动（tmux/脚本）的服务器进程不被平台接管——DB 记 STOPPED 而磁盘在跑、面板说谎且无出口；启动预检只查 java/目录/jar，不含现场占用，故此时点「启动」会双开撞 `world/session.lock` 与端口。**另修一处破坏性缺陷**：启动期孤儿恢复会依遗留 PID 记录强杀「wrapper 已死 / Java 仍活」的进程，而就地导入实例的 `workDir` 与外部服务器目录相同，归属复核结构上无法区分，真机曾因此一次打断 54 台在跑农场。现新增三件事：①孤儿扫描增第 4 相，按**已注册实例的 workDir**（不限受管 `serversDir`，覆盖就地导入）探测未纳管活进程，经心跳 `foreign_pid/foreign_cmdline` 上报，CP 落 `instances.runtime_drift_*` 供面板提示（只观测不自动处置）；②启动预检补 `work_dir_busy` 与 `port_free` 两项，把双开拦在转 STARTING 之前；③新增接管入口 `POST /instances/:id/adopt-runtime`——先做归属复核（防 PID 回收误杀），再 SIGTERM 进程树等其优雅退出（超时升级 SIGKILL），随后以受管方式拉起；④启动恢复路径**不再强杀**，改为只观测 + 保留 PID 记录（强杀只保留在显式 `orphan.dispose_policy=auto` 的周期扫描）；⑤端口预检改「连接探测 + 试绑有界重试」消除偶发误报。**验收**（真机通过）：外部启动的农场逐台报出漂移；点「接管」后状态转 RUNNING 且与磁盘进程一一对应；对运行中实例点「启动」被预检拒绝而非双开；重启 Worker 不再强杀在跑服务器

### FR-472 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.24.0）

僵尸压测会话收敛：CP 重启后会话卡在 running 导致补足死循环与写放大（fix，缺陷修复 FR-460/370）：压测会话的生命周期终结依赖显式 `Stop` 调用，CP 重启或 bot-worker 死亡后该路径丢失，会话永久停留 `status=running`。`refillRunningSessions` 无条件捞取**全部** running 会话做容量补足，于是对早已失联的会话每拍重建期望行并失败——失败落在事务内，产生持续写盘。**进程内退避（`refillAttempts` map）结构上兜不住**：它随 CP 进程结束而清零，重启即触发一轮全量重试。**真机现场（2026-09-24）**：生产 CP 残留 132 个历史会话（61 running + 32 pending），1132 条 Bot 记录全数失联，CP 持续写库 **3.5 MB/s**、`cp.out` 涨至 **962 MB**。现按**数据库可见的事实**判定僵尸：`status=running` 且在线 Bot 数为 0 且 `updated_at` 停滞超过 `botZombieSessionIdleThreshold`（默认 **10 分钟** = Bot 宽限期 2m 的 5 倍，容忍 Worker 重启与世代迁移窗口）即收敛为 `stopped`，写 `ended_at`/`last_error` 与强审计 `bot_reclaim.session_reaped`；收敛在 `refillRunningSessions` **之前**执行，使僵尸从根上不进入补足扫描。**不变量**：仍持有在线 Bot 的会话永不被收敛。**验收**（单测 3 条：僵尸必收敛、收敛必留痕、健康会话不被误伤；真机：93 个僵尸会话收敛后 CP 写 IO 由 3.5 MB/s 降至 0.3 MB/s，补足失败日志停止）

### FR-473 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.24.0）

Worker 日志平台 Shared Contracts 与 ADR（feat，开发前闸门）：新 ADR（Worker 本地 VictoriaLogs + CP 联邦；与 ADR-005/013/066/081 对账）；**冻结正式版基线**：事件身份与采集账本、投递语义与重复对账、checkpoint/WAL/ACK/回收四水位、Partition Catalog 与唯一查询所有权、IngestRouter、PublishedProjection、canonical projection 与 `closed_visible_seq`、`logs.lifecycle` 配置分层与合法部署组合、**完整 gRPC 消息契约**、权限覆盖全查询/导出面、Worker 日志资源预算与**性能判据方法及初始阈值**、能力协商、Legacy/导出/覆盖语义；`stream_fields` 白名单。**冻结退出=契约+治理+依赖层证据**（ADR-094 accepted、proto/协商终审、VL 资产正式审批、性能判据入契约）；**Worker 级 Runbook A/B/C 不作为冻结前置**，分别归属 FR-474/476/477 验收 → `docs/specs/worker-log-platform-contract/spec.md`

### FR-474 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.24.0）

Worker 日志采集与持久化通道（feat）：FileTailer + StdIO + ArchiveImporter **共用采集账本**；源分段身份与 `latest.log→轮转→.gz` 关联；cursor/WAL/分水位回收/坏记录隔离；STDIO 受管 Raw 分段；容量耗尽与缺口可见；**hash+size 仅识别归档对象**；不引入 Filebeat 等。**验收含 Runbook A**（Worker WAL 强杀/ACK 丢失/reclaim/恢复分段释放与 hold 保护真实 PASS）→ `docs/specs/worker-log-acquisition/spec.md`

### FR-475 需求原文（优先级 P0 / 状态 🔨 开发中）

日志 Multiline 与归一化（feat）：Java/MC 多行归并；level/time/logger/instance_id；Multiline 上限/超时/半条恢复；时区跨午夜；解析失败保留原文+状态；parser_version；**事件数 vs 行数**口径 → `docs/specs/worker-log-normalizer/spec.md`

### FR-476 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.24.0）

VictoriaLogs 资产分发与 Worker 运行时（feat）：**CP 只内嵌/分发**经校验官方 OSS 资产（linux-amd64 + windows-amd64）；Worker 本地 supervisor 管理 HOT/COLD/Rehydrate（localhost + 本地鉴权）；资产审批=tag/包与解包校验/许可/兼容；安全停机；资源总预算；失败日志防递归；cache≠RSS。**验收含 Runbook C**（wiring/防递归/预算降级/老 Worker Unimplemented）→ `docs/specs/worker-victorialogs-runtime/spec.md`

### FR-477 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.24.0）

分区 Catalog 与冷热 Lifecycle（feat）：Partition Catalog（owner/generation/journal）；迁移状态机（冻结路由→排空→snapshot→staging→**QueryPlanner 排除**→原子切 owner→租约排空→detach→清理）；迟到日志按 Catalog 路由；retention 协同；`cold.enabled=false` 单层合法；**detach≠完成**。**验收含 Runbook B**（Catalog 适配层各状态崩溃恢复 + re-attach 后旧 owner 过滤）→ `docs/specs/worker-log-lifecycle/spec.md`

### FR-479 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.24.0）

Worker 日志查询服务（feat）：Catalog 驱动不重叠权威副本；Search/Stats/Tail/Fields/Facets/StorageHealth/Rehydrate/ArchiveStatus；覆盖状态与 Cursor/水位；取消与预算；**完整验收依赖 477/478** → `docs/specs/worker-log-query-federation/spec.md`

### FR-481 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.24.0）

日志入库切换与 Legacy 只读（feat）：停止**实例与 Worker/Node**日志新入 CP `logs`；platform/audit + **LegacyLogReader**；Legacy 独立保留预算与到期规则；容量预检不足不切换；逐 Worker 切换水位；控制台订阅与持久化推送分离；历史 Multiline 可追溯原文；未统一口径禁止拼精确趋势 → `docs/specs/log-ingest-cutover/spec.md`

### FR-483 需求原文（优先级 — / 状态 ✅ 已交付@v0.24.0·docs（已对账 ARCHITECTURE/API/CHANGELOG/PRD/规格状态；已补受控文档 `worker-victorialogs-runtime/asset-inventory.md`（资产清单/许可）+ `worker-log-platform-contract/recovery-manual.md`（恢复与故障处置）+ `worker-log-platform-contract/acceptance-record.md`（**持久真机验收记录**，缓解 `.tmp` 证据不入库）））

日志平台文档对账（docs）：Shared Contracts/ADR 迁入受控文档；资产清单与许可；恢复手册；数据损失边界；资源预算；合法配置组合；切换与故障处理；ARCHITECTURE/API/CHANGELOG

### FR-484 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.24.0）

canonical 事件体落盘 compact（feat）：把 FR-473 §4.3/§5.3「VL 数据根重建的权威事件集合」由 state.json 内联+内存常驻改为追加式磁盘段存储（NDJSON + MANIFEST 原子替换），**内容与语义不变、仍不可按 reclaim 裁剪**；含旧格式迁移、崩溃/截断恢复语义、`persist()` 流式写元数据；并加界三处跨轮无界累积结构（`n.events`/`linePos`/`delivered`）收敛采集期峰值。目标单 Worker 150 源稳态 RSS ≤300MiB → `docs/specs/worker-log-canonical-eventstore/spec.md` + ADR-095

### FR-485 需求原文（优先级 P1 / 状态 🔨 部分交付@v0.24.0（**仅采集健康指标链路已落**：#49 落地 `log_ingest_healthy` 经 Worker 心跳上报 → CP 落库 → 内置告警触发联动；其余范围——state 文件尺寸与增速、persist 写字节/秒、单源 WAL 积压条数与暂停时长、投递失败率与分类、校验退避次数、慢 SQL 计数，以及**节点层 iowait 采集与比率触发**——**均未实现**））

日志平台可观测性与告警（feat，增强 FR-474/484）：把「单源洪流拖垮 Worker」这类事故的信号变成平台自己能看见——新增指标：state 文件尺寸与增速、persist 写字节/秒、单源 WAL 积压条数与暂停时长、投递失败率与分类、校验退避次数、慢 SQL 计数与最长耗时；经 Worker 上报（proto 扩展）→ CP 落库 → 告警规则联动（metric 触发 + FR-462 动态基线做增速突升检测），并附推荐阈值与处置手册入口。**动因**：2026-09-28 生产事故（ingest.state.json 涨至 1.2 GB、117 MB/s 持续重写 25 小时、iowait 91%、CP 慢 SQL 0.4–2.4s）**完全靠人肉排查发现**，平台当时对这些信号一无所知、无法自行告警。**范围按实证扩大**：节点层同样缺关键信号——**iowait 未被采集**（事故中最刺眼的 91% 无法告警）；node_disk_used/node_mem_used 以**字节**存储、无比率触发，「磁盘 >85%」类绝对阈值当前**无法表达**。故本 FR 一并补齐 iowait 采集与比率触发，**告警规则并入本 FR 一次配好**

### FR-486 需求原文（优先级 P1 / 状态 🔨 开发中·**范围①②④ 已实现并真机验证，范围③ 部分完成**（①冷启动实测 **76 ms** 总耗时、逐阶段 ≤6ms，当年的「8m39s」由大索引导致、正常规模不复现；②`ReportStartupProgress` RPC + `node_startup_progresses` 表 + `/nodes/startup-progress`，注册前即可见、注册后清理；③`Load()` 规模曲线实测线性（100k 行 453ms），已优化扫描缓冲区复用（**内存 −32% / 分配次数 −21%**），**4× 耗时目标未达成**——本机 load 212 且无法 drop_caches，机械盘随机 IO 特性测不准，不盲改；④`state_warn_mb` 默认 512MB，实测中发现并修掉两处「告警永不触发」缺陷：只扫 `var/log` 漏了 `var/index`、告警被埋在「配了采集源」分支内。详见 `docs/specs/worker-startup-observability/spec.md` §7））

Worker 启动时长与状态加载可观测性（feat，性能/可观测，增强 FR-474/484）：1.2 GB ingest 状态文件使 Worker 重启后 **8m39s 才注册上线**（2026-09-28 生产实测：19:31:35 启动 → 19:40:14 注册成功），期间主控台看不到节点、实例健康巡检未运行；且加载全程**没有任何进度日志**——平台对自己的启动过程同样无观测（与 FR-485 同源）。范围：① 加载分阶段计时与日志（读取/解析/恢复/首次持久化）；② 进度可见（注册前也能上报启动阶段）；③ 大状态文件加载优化（流式解析、引用化后免全量内联解析）；④ 状态超阈值的启动前告警。**动因**：C2 窗口重启实测；B1a 压缩生效后需复测时长以确认改善

### FR-489 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.24.0）

MCP 端点无状态化与 Token 维度运维视图（feat，修订 **FR-388/389/391**、作废 **FR-439**）：CP 内嵌 MCP 网关的 Streamable HTTP 路径**去会话化**（对齐 MCP SEP-2567）——不再下发/要求 `Mcp-Session-Id`，每请求独立鉴权并用重建的 principal 授权（顺带消除「会话内 principal 快照致 Token 吊销/降权不即时生效」的授权缺口）；移除会话空闲/绝对超时、全局与每 Token 并发上限、`SESSION_GONE` 与超限 429、会话列表/踢线；`GET /api/v1/mcp` 改为 405、`DELETE /api/v1/mcp` 改为 405 + `Allow: POST`（均无会话可保活/可终止）。**SSE 兼容路径保留**（`/sse` + `/message`），其连接态收敛为「SSE 传输连接登记」——那是传输固有需求，不是协议会话。运维视图随之由会话维度改为 Token 维度：移除 `GET/DELETE /api/v1/agent/mcp/sessions`，新增 `GET /api/v1/agent/mcp/activity?window=`（按 `agent_call_logs` 聚合），前端「MCP 会话」页改为活动视图（路由 `/mcp-activity`，旧路径保留重定向）→ `docs/specs/mcp-stateless-endpoint/spec.md`（+ **ADR-096**）

### FR-490 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.24.0）

审批票据与状态机内核（feat）：为 MCP 高危操作建立统一审批真源——票据状态机（`pending` / `withdrawn` / `rejected` / `expired` / `executing` / `succeeded` / `failed`）+ 冻结目标载荷与证据快照 + 24 小时过期 + 幂等键 + 并发 CAS + 强审计；**申请人自查与撤回能力对齐 Beacon 语义**：`own.list` / `own.get`（查询自己提交的申请与状态）/ `own.withdraw`（仅 `pending` 可撤回），使 Agent 提交后能轮询结果、闭环不断在盲等；**批准仅人类 JWT**，Agent Token 在服务层永久不可批准（不靠工具列表隐藏）。需修订既有「MCP 不做面板审批」结论（ADR-076/077 与 cp-mcp-server spec）

### FR-491 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.24.0）

审批执行器与执行回执（feat，依赖 FR-490）：批准 → 持久执行器认领 → 领域 adapter 在事务内执行 + 写执行回执 + 强审计；执行前重校验目标漂移与资源版本，漂移即 `failed` 且不执行；重试幂等，失败保留批准事实与失败原因。**执行器与既有异步机制的关系须在 spec 明确**：优先复用 `TaskService.RunAsync`（FR-441 已用其承载异步任务与阶段进度）而非另造一套调度，避免出现两条互不相识的异步链路

### FR-492 需求原文（优先级 P0 / 状态 ✅ 已交付@v0.24.0）

高危 MCP 工具接入审批（feat，依赖 FR-490/491）：**登记方式 = 以 `agentActionCatalog` 为准穷举，不按操作类型直觉列举**，且**必须按 action 粒度标记而非按 `Operation` 分类**（`instance_start` / `stop` / `restart` 同属 `write`，但本项只要求 `stop` 进审批，按分类无法表达该差异）。**已知必须覆盖的三类绕过口子**：①`file_issue_transfer_ticket`——签发票据后走流式上传即可写盘，是绕过 `file_write_text` 的完整路径；②`instance_send_command`——控制台命令等价任意代码执行，当前连精确确认都没有；③`instance_update_config` 改 `startCommand`、`instance_binary_upgrade` / `instance_binary_rollback`、`provision_server` 的远程 URL 二进制来源——三者均等价「下载并执行任意二进制」；`instance_snapshot_rollback` 覆盖既有数据，同样纳入。服务层失败关闭：MCP 调用只落票据返 `requestId`、**零副作用**；`start` / `restart` / 配置热重载保留直执 + 现有精确确认；同步更新 agent-safety-gate 契约表（CLI / MCP / curl 三入口同策略）。**`approval_required` 与既有 `Operation` 的关系须在 spec 定义**（正交审批标记 or 第四种 Operation，不得两者兼有）

### FR-493 需求原文（优先级 P1 / 状态 ✅ 已交付·审批中心页面（待审列表 + 批准执行 / 驳回 + 历史追溯）、`approval.read` / `approval.decide` 权限节点接线、7 项 DOM 用例与 3 档视口 e2e 均已完成；无权限账号 403 由路由守卫 + 服务层双重拦（决策端点另有「Agent 身份不可决策」的服务层拒绝，不靠路由兜底））

审批中心与权限节点（feat，依赖 FR-490）：新增审批中心页面——待审列表（操作 / 目标 / 申请人 / 风险 / 原因 / 证据）+ 批准执行 / 驳回 + 历史追溯；RBAC 权限树新增审批权限节点，**须遵循既有权限树约束**（FR-089「deny 永胜、超管唯一」与 FR-432 可配置权限树），无权限账号 403

### FR-494 需求原文（优先级 P1 / 状态 ✅ 已交付@v0.24.0）

QQ 机器人告警通知通道（feat，增强 FR-085）：在既有通道体系内新增第 9 种通道类型 `qq`，把告警经 QQ 开放平台官方 HTTP 接口**主动推送**到指定群聊或单聊——`appId` + `appSecret` 换 AccessToken，再 `POST /v2/groups/{group_openid}/messages`（或 `/v2/users/{openid}/messages`）发 `msg_type=0` 纯文本。**动因**：国内运维值班阵地常在 QQ 群，缺 QQ 系出口使「告警产生 → 人看到」的链路偏长。**架构取舍**：保持 CP 纯出站——不监听端口、不接收 QQ 事件、无常驻长连接、不做被动回复；由告警的低频单向特性与官方主动消息配额（每群 / 每好友 1000 条/天）支撑该取舍。**已知边界**：群消息禁含 URL（`40054010`）；接收方可单方关闭主动推送；`group_openid` 无官方列表接口，须手工填入 → `docs/specs/qqbot-notification-channel/spec.md`

### FR-495 需求原文（优先级 P1 / 状态 开发中（真机验收：扫码绑定 / 网关连接 / 入群事件自动落库**均通过**；建通道与前端待验））

QQ 机器人扫码接入与已发现群列表（feat，增强 FR-494）：终结手抄 `group_openid`——控制台**扫码绑定机器人**（二维码经 `q.qq.com` 绑定页，回调自动取得 `appId`/`appSecret`/`user_openid`，密钥落盘 `<dataRoot>/etc/qq/` 并以 `${QQ-<appId>}` 引用、不入库）；CP 常驻 QQ 网关 WSS 长连接（`intents=1<<25`）；**群聊路径已下线**（平台拒绝群主动消息，群列表与分享链接端点保留待平台开放）。**产品决策**：手动选群（进群事件无归因字段，自动建通道会产生垃圾通道）/ 单机器人共享（个人主体名额有限，token 缓存按 `appId` 设计）/ WebSocket（完全出站，不暴露入站端口）。**已知边界**：分享链接官方定位是「加好友」，扫码能否直达进群待真机实测；`1<<25` 需申请权限；离线窗口的入群永久丢失（无列表接口补偿）；个人认证进群上限 500 → `docs/specs/qqbot-group-discovery/spec.md`

### FR-496 需求原文（优先级 P0 / 状态 🔨 开发中）

采集索引迁移至本地 SQLite（feat，增强 FR-473~484 底座；spec：`docs/specs/log-index-sqlite/spec.md`）：现索引为单一 JSON 整本重写——实测 307MB/13 源，60 实例外推 1.5–3GB，单次持久化将达秒级（历史事故：~1.2GB 时 >5s 触发 `vlsup` 超时、采集停摆）。改为纯 Go 嵌入式 SQLite（WAL、单写者、只写变更行）+ 旧文件一次性迁移归档。验收：60 源下单次持久化 ≤50ms；迁移逐字段一致；`kill -9` 不丢索引。**2026-10-01 加固（FR-498 实测发现）**：迁移后索引里唯一随总量线性增长的表变成 `delivery_batch`（≈**5 MB/天 @60 源**）且原先无裁剪路径，长跑会把索引无界撑大——按 `reclaim` 水位裁剪历史批次（判据有证明：`End <= Reclaim` 的条目对 `delivery_position` 的连续前缀计算是**恒等元**，且水位单调；失败只告警不阻断，有自证守卫；配置键 `log_index.batch_prune.*` 见 spec §6）。60 源 × 6000 批/源夹具实测：360 000 行 / 库 25.6 MB → **0 行 / 864 KB**；水位滞后窗口内保留量只与滞后有关、与累计总量无关（spec §2.4/§3.8）

### FR-497 需求原文（优先级 P0 / 状态 🔨 开发中）

重启增量对账与全自动收养（feat，**推动 FR-455/456 落地** + 增强 FR-471；见 ADR-093；「重启增量对账」子项 spec：`docs/specs/log-startup-reconcile/spec.md`；另一半「全自动收养」**已立项并实现**（spec：`docs/specs/auto-adopt-orphans/spec.md`，T1–T5 已过、T7 真机待做））：现状重启整窗重发（13 源 ~5 分钟，60 源将达十几至几十分钟），且未纳管活进程仅告警（`orphan_scan.dispose_policy=warn`）、接管重试仅 3 次（~7s）失败即强杀健康服务器（FR-455① 地雷）。改为：启动按「源×天」与 VL **条数级对账**只补缺（绝不丢、允许极少重复）+ 存活 wrapper 自动重连 + 未纳管活进程经 PID 文件与进程属主校验后**自动收养**为 RUNNING。验收：60 源重启 ≤60s；人为删 VL 某天可检出并补回；随机杀 Worker 100 次零丢失；校验不过只告警不杀

### FR-498 需求原文（优先级 P0 / 状态 🔨 开发中）

单机 60 实例规模压测与容量基线（feat，验证类）：真机扩至 60 源实测四条基线——Worker CPU/内存、VL 内存、索引持久化 P50/P95、查询 P95（「全部时间」路径按产品引导口径一并观察）。验收：出实测数字与阈值，作为 60 台上线判据；含 heap 建议与错峰策略。**2026-10-01 采集侧 P0 续（spec：`docs/specs/log-verify-chunking/spec.md`）**：跨源并发后单轮 85.6% 仍在**投影校验**（写后强制可见性校验），实测其成本构成——① 数据已可见时单次查询耗时与「窗口内返回行数」线性（本地夹具 60 源 ×30 行/s，窗口 900 s / 2.7 万行 / 9.3 MB → 137.8 ms，其中 VL 首字节仅 3.5 ms、逐行 JSON 解析 71.3 ms）；② 旧实现按**索引**每 500 条切一片、各片时间窗互相包含 → **每片把整批读回一遍**（读回量 k×n）且批内串行；③ 但「1.42 s/批」的主体是**等 VictoriaLogs 异步可见**——可见延迟恒定 ≈1.02 s（20/20 次探针），而退避探测点 0/0.2/0.6/1.4 s 在 1.0 s 处没有探测点。修法：按**事件时间**分簇（`verifyChunkEvents` 默认 2000，与写批上限解耦）+ 同批内 4 路并发 + 判定类错误优先（取消噪音不掩盖根因）+ 退避封顶 2 s→500 ms。**等价性**：判定规则/选择器/时间窗/日夹取/三类错误文案/退避语义逐字未改，可见行集合的并集不变（详见 spec §3.6）。**实测**（真进程夹具；夹具脚本与产物落在 `.tmp/`，该目录**不入库** ⇒ 外部读者不可复算，故此处只给夹具参数与判据）：读回放大 **1.00×**（旧实现 4 片各读整批）；60 源 ×60 行/s 索引写入 **3.79–3.93 k 行/s**（≥3.0k 目标）；批级 verify 由 1411 ms 降到 1000 ms 量级。**可转红回归**：4 组变异实测转红（去时间排序 → 8000 行读回 / 并发置 1 → 峰值 1 / 完整性判定恒真 → 少写漏检 / 删内容比对 → 写错漏检），逐字还原后转绿。**2026-10-01 索引持久化切分（FR-498 P0 续；spec：`docs/specs/log-index-sqlite/spec.md` §2.5/§3.3/§6）**：跨源并发把最多 8 个源的批次**合并成一次 SQLite 事务**，单次持久化 p95 因此随负载线性恶化（66–73 ms @1.8k 行/s → 198–228 @3.6k → 348–366 @5.4k），是 60 源下**唯一越过**「单次持久化 ≤50 ms」验收线的指标。修法三件：①**提交单元切分**——一次持久化按「行数上限（默认 512，可配）+ 耗时目标（默认 40 ms，执行中硬上界 = 目标×5/4 = 50 ms）」切成若干 `BEGIN IMMEDIATE`…`COMMIT`，控制器按实测行数/耗时比例收缩或扩张预算；单元是「写（父表在前）→ 删（子表在前）」有序序列的**连续切片**，外键顺序与「只写变更行」判据逐字不变；写入序把 `position`（四水位）**排到最后**——任何崩溃前缀都只会让水位偏旧（重启后从更早位置续读 ⇒ 至多重放，与 FR-497「允许重复」同口径），不会领先（那才会丢事件）；小周期仍是单事务，既有崩溃回归形态不变。②**删除批量化**——逐行 `ExecContext` 的 DELETE 合并成多值语句（单列主键 `IN (?,…)`、多列主键 `IN (VALUES …)`），与 UPSERT 同一批量节奏，仍完全由镜像差异驱动、不新增绕过镜像的删除路径。③**WAL 归并移出提交路径**——`wal_autocheckpoint=0` + 周期末尾显式 `wal_checkpoint(PASSIVE)`：归并不再落在某一次 `COMMIT` 内部（不拖长任何一次提交、不在持写锁期间做页拷贝），成本仍完整计入周期总耗时（独立探针：单提交单元 Max **225 → 56 ms**、p95 49 → 47 ms）。**实测（真进程夹具 60 源 × 60 行/s × 180 s，同机同 harness，三个分钟窗口；夹具与产物在 `.tmp/`，该目录不入库 ⇒ 不可复算，故只给参数与判据）**：单提交单元 p50 32–40 → **13–14 ms**、p95 205–223 → **28–31 ms** ✅、Max 331–351 → **79–100 ms**（≤2× 判据；末窗口 100 ms 恰在边界，成因是系统级偶发停顿而非行数失控）；每事务行数被 512 硬约束（`maxRows=512`）；稳态最小二乘吞吐 3 581 行/s 与切分前**逐位同量级**、稳态中位 3 829 行/s、累计采到 100 %、暂停/缺口 0/0。**可转红回归**：`stateindex/commit_chunk_test.go` 三条（每事务行数有界〔无时序依赖〕/ p95 与不切分臂的比值断言 + 非竞态下的 50 ms 绝对断言 / 切分前后逐字段等价）、`ingest/persist_chunk_test.go`（**强切分预算下的迁移比对逐字段等价**）、删除批量化的语句数断言；三组变异（预算抬到无穷、删除退回逐行、删除顺序改回表正序）实测转红后逐字还原。**配置接线**：`log_index.batch_prune.*`（此前只经 `Options.IndexPrune` 生效、`worker.yml` 覆盖不到）与新增 `log_index.persist.*` 均已接入 `internal/worker/config.go` + `apps/worker/main.go`，并有「配置关 → 裁剪关、且批次行数退化到线性」的决定性用例

### FR-499 需求原文（优先级 P1 / 状态 🔨 开发中）

采集登记路径与长临界区解耦（fix，事故根因修复；spec：`docs/specs/log-ingest-register-lock/spec.md`）：2026-10-01 生产事故——CP 下发实例规格时 Worker 的「登记实例日志采集」在 07:39/07:46/07:49/08:44 **四次全 DeadlineExceeded**，11 台实例无法启动（同期实例进程侧已能对账，只有这一步挂住）。**根因**：登记以 `cycleMu` 作入口互斥，而该锁被**整轮采集**（逐源 Poll → persist → VL 投递 → 投影校验）在吞吐饱和期连续持有（实测单轮 3.15s，生产可达分钟级），CP 的 10 秒 RPC 截止必然到点。**修法**：登记自成一锁 `registerMu`，与 `cycleMu` 解耦（锁序 `cycleMu → registerMu → pendingMu → mu`），`Stop` 补取 `registerMu` 串行化关库；**登记语义不变**（仍同步、返回即已建管道并提交内存绑定；**落库自 2026-10-03 `0fa2246b` 起为有界尽力**——超持久化门预算即交办后台通道，不再承诺「返回即已落盘」，见 spec §3.5 补充）→ **CP 侧零配套改动**。验收：同一忙期下登记耗时由「1.00s 到点超时（真实需等满 3.1s）」降为 0.1–4.6ms；`new_binding` 与 `idempotent_rebind` 两形态各有可转红回归（逐字还原旧实现实测转红）；`-race` 与 logs 全量测试全绿

### FR-500 需求原文（优先级 P0 / 状态 🔨 开发中）

daemon 子树脱离 Worker 单元 cgroup（fix，事故根因修复；spec：`docs/specs/daemon-cgroup-isolation/spec.md`）：2026-10-01 生产事故——**每次 `systemctl restart jm-prod-worker` 杀死全部 11 台正在运行的 MC 服务器**。**根因不在 Worker 代码**：unit 缺 `KillMode=process`（`KillMode=control-group`、`KillSignal=15`），systemd 对单元 cgroup 内全员发 SIGTERM，`setsid` 逃得出进程组、逃不出 cgroup；Worker 关闭路径当时已正确（日志 `daemon 实例已断开连接（wrapper 继续运行）` 先于 `wrapper 进程退出 err="signal: terminated"`，且 11 台实例的 Java 自身日志同秒 `Stopping server` 证明 Java 被 cgroup 级 SIGTERM 直接打中）。**修法**：运行期自持 cgroup 隔离——wrapper 启动时**先自迁**到 `…/app.slice/jianmanager-daemons/<uuid>`（早于 `startJava`，java 随后 fork 继承），Worker 接管存活 wrapper 时按 PPID 链**连后代一并迁移**（覆盖「部署后第一个重启」场景）；仅在自身 cgroup 确为 `*.service` 单元时生效，失败只告警保持原状，`JM_DAEMON_CGROUP_ISOLATION=0` 可关。**契约**：Worker 关闭路径只断连不杀实例；显式停止/删除照旧；cgroup 边界不依赖 unit 配置（见 restart-resilience spec §7）。验收：真实进程端到端用例（关闭路径 + 单元级 cgroup SIGTERM 后 wrapper/java 存活、可被接管、显式 Stop 仍能停）逐字还原旧实现实测转红；真实 worker 三臂实验（red 复现生产现场 / fixed-adopt / fixed-self 存活）；`go build`、`go vet`、`gofmt`、`go test ./internal/worker/...`、`-race` 全绿

### FR-501 需求原文（优先级 P1 / 状态 🔨 开发中）

日志成本治理（分层/采样/保留）与冷层搬运（feat，增强 FR-473~484 日志底座；spec：`docs/specs/log-cost-governance/spec.md`，关联 **ADR-098**（保留/分层/归档）与 **ADR-099**（成本治理与不变量 R））：① 采集侧 `sampling`——等级过滤 / 同源同消息高频抑制 / 每源预算 / 风暴自动降级，四类策略**默认全关**；抑制以**汇总事件**承接，**不变量 R**（抑制不得在源位置覆盖上留空洞：`MergePositionRanges(Process(in)) == MergePositionRanges(in)`；R2 缺口仍能自动消解 + R2b 朴素丢弃必不能消解为锚点），不得绕过缺口 default-deny 判据与 `VerifiedRuns`；② 保留侧 `retention`——按级别分级档位（debug 3d / info 7d / warn 30d / error 90d；**热层 90d / 冷层 730d 统一**，设置可配）+ 源级覆盖 + 白名单删除过滤器（级别 + 源标识字符集，绝不从配置读过滤器）+ **删前归档硬规则**（默认动作=搬运 COLD；搬运失败=保留原物+告警、零删除；删除需 `discard`+`sweep.vl_sweep` 双闸 + 受管 VL `-delete.enable`，**默认全关**）；③ 冷层搬运驱动器 `retention.Driver`——周期调度 + 首轮立即执行，触发=年龄 + 磁盘水位**取先到**（磁盘触发 min_age=7d），判据与人工迁移入口 `SetPartitionMigrator` 逐字对齐。**编号对账**：本 FR 号经 2026-10-02 对账确定（CHANGELOG 原误标 FR-499，与「采集登记路径与长临界区解耦」冲突；详见 spec 顶部对账记录）
