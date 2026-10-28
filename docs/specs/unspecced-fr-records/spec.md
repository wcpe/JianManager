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

**交付状态**：🔨 开发中·阶段 6 进行中（**桌面侧栏已切换**：`DashboardPage` 挂载 `WorkspaceSidebar`，其数据全部取自 `lib/console/workspace-navigation.ts`；仅移动导航 `MobileConsoleNav` 与命令面板 `CommandPalette` 仍以 `nav-config.ts` 为真源，故 `nav-config.ts` 未删。页面迁移未完成）

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

### FR-508 应用业务组件受控化迁入组件库（ref，免 spec，补 FR-502~507 未覆盖的「业务组件所有权」范围，架构决策见 ADR-102）

**需求补充**：把主控台内剩余 **151 个业务组件**按 **a+b 双范式**受控化后迁入 `packages/ui/src/components/views/<域>/`——**a 范式**（74 个纯读组件）走「外壳取数 + props 注入」，数据与加载/错误态由外壳注入、UI 状态留包内；**b 范式**（54 个含写组件）走「受控 + 回调注入」，动作以 `onXxx` 回调上报、由外壳执行 mutation 与 toast。包内硬约束（沿用原 biz-views）：不 import `@/api`/`@/lib`/`@/components`，不依赖 `react-router`/`@tanstack/react-query`/`sonner`，由边界测试守门；随迁的共享纯逻辑入 `src/lib/`（归包，两边都从包取）。**动因**：177 个业务组件中零应用依赖的仅 16 个（12 已迁、7 为两行再导出层、2 为 devtools 面板），纯移动无路可走；且业务组件不进包则组件博物馆无法覆盖它们——而它们组合最复杂、样式最易漂移，恰是最需要跨主题/跨明暗验证的部分（ADR-058 的兑现前提）。**分批**：按 API 模块聚类推进 instances(32) → nodes(19) → metrics(13) → 其余零散，同域组件共享数据结构与 i18n 命名空间，可复用同一套 props 契约与测试夹具。**不做**：抽 `packages/api` 让组件继续自行取数（绕过受控化目标）、只迁纯读组件（覆盖不全且同域被拆两处）、迁移期目录双写（会造成两份实现静默分叉）。**验收**：① 151 个组件全部迁入且应用内不留重复实现；② 包内零 router/react-query/sonner 依赖（边界测试断言）；③ 迁移页面行为不变（数据口径 / 权限 / 深链 / 文案）且既有用例全绿；④ 组件博物馆可见并入的每一个业务组件并在真浏览器实测渲染

**交付状态**：❌ 已废弃（**ADR-103 取代 ADR-102**：业务视图与业务契约退回应用侧，`packages/ui` 回归设计系统；a+b 双范式作为组件写法保留，但「并入组件库」的归属决策撤销。验收口径改为「业务视图在应用侧、包内零业务、边界测试守门」）

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
