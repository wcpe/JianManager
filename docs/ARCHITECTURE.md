# ARCHITECTURE — JianManager

> 本文档始终反映系统当前状态，不保留历史版本。历史决策见 `docs/adr/`。

---

## 1. 系统全景

```
浏览器 (React SPA, go:embed 嵌入 Control Plane)
    │ HTTP REST /api/v1/*        │ WebSocket (鉴权后直连)
    ▼                            ▼
Control Plane (Go 单二进制)      Worker Node WS Server
    │ gRPC
    ▼
Worker Node (Go) × 20~100
    ├── 游戏服进程管理 (direct/daemon/docker/rcon)
    ├── 守护进程 Wrapper
    ├── WebSocket 终端服务 (/ws/terminal)
    ├── 插件桥反向 WS 服务 (/ws/plugin-bridge, 探针主动连入, token, FR-065/ADR-016)
    ├── Bot 管理 → Node.js 子进程 (Mineflayer)
    └── 指标采集
        ▲ HTTP GET (本机回环抓取)  +  ◀ 反向 WS (探针连入, 治理/事件通道)
        └── ServerProbe 探针 jar (运行于游戏服 JVM, FR-010 监控见 ADR-014, 治理桥见 ADR-016)
```

## 2. 三进程模型

| 进程 | 语言 | 部署 | 职责 |
|---|---|---|---|
| Control Plane | Go | 1 个实例 | API、认证、调度、gRPC 客户端池、前端静态文件 |
| Worker Node | Go | 20-100 个实例 | gRPC 服务端、进程管理、Docker 管理、WS 终端服务 |
| Bot Worker | Node.js | 按需 spawn | Mineflayer 连接、行为引擎、寻路、脚本执行 |

## 3. 技术栈

| 层面 | 选型 |
|---|---|
| 后端语言 | Go 1.22+ |
| HTTP 框架 | Gin |
| 数据库 | SQLite(dev) / MySQL(prod) |
| ORM | GORM |
| 节点通信 | gRPC + Protobuf |
| 终端 PTY | creack/pty |
| Docker | docker/docker/client |
| 前端 | React 19 + Vite 6 + shadcn/ui + TailwindCSS |
| 前端状态 | TanStack Query + Zustand |
| 前端路由 | React Router 7 |
| 终端前端 | xterm.js |
| 图表 | Recharts |
| 编辑器 | CodeMirror 6 |
| Bot 运行时 | Node.js 20+ + Mineflayer |
| Bot IPC | stdin/stdout JSON 行协议 |
| 国际化 | i18next |

## 4. Control Plane 架构

```
┌─ 入口层 ────────────────────────────────────────────┐
│  main.go, router.go, grpc_client.go                 │
├─ 中间件层 ──────────────────────────────────────────┤
│  auth, context, audit, ratelimit, error              │
├─ 业务模块 ──────────────────────────────────────────┤
│  auth, user, group, node, instance, terminal,        │
│  file, bot, schedule, backup, monitor, template,     │
│  audit                                               │
│  群组服(V2): network · registration · config · jdk · clone │
├─ 基础设施层 ────────────────────────────────────────┤
│  database, config, logger, event, embed              │
└─────────────────────────────────────────────────────┘
```

### 目录结构

```
cmd/control-plane/main.go
internal/controlplane/
  config/config.go
  database/database.go
  middleware/auth.go
  model/{user,group,node,instance,bot,alert,schedule,backup,template,audit,network,registration,jdk,config_version,file_version}.go
  router/{router,auth,user,group,node,instance,terminal,bot,file,schedule,backup,alert,template,audit,network,config,jdk}.go
  service/{auth,user,group,node,instance,terminal,bot,schedule,backup,alert,template,audit,file,file_version,authz,network,registration,config,jdk,clone}.go
  grpc/{pool,client}.go          # TODO: gRPC 客户端池
  event/bus.go                   # TODO: 事件总线
  ws/gateway.go                  # TODO: WebSocket 网关
  embed/static.go                # TODO: 前端嵌入
```

### 4.1 权限模型（RBAC）

基于「三级角色 + 用户组隔离」的权限模型，参见 ADR-004（用户组替代多租户）。

```
角色层级
  平台管理员 (role=10) → 拥有全部权限，可管理所有用户/组/节点/实例
  组管理员   (role=1)  → 受限于其任组管理员身份的组（group_members.role=1）
  组成员     (role=0)  → 受限于其所属组（group_members.role=0）
```

**权限节点**（`service/authz.go`）：`user:*`、`group:*`、`node:*`、`instance:*`、`file:*`、`terminal:access`、`bot:*`。

**授权链路**：
1. `middleware.JWTAuth` → 解析 JWT，写入 `userId/role`
2. `middleware.LoadAccess` → 调用 `AuthzService.LoadUserAccess` 加载用户的组成员关系（管理组/所属组集合），写入 `access` 上下文
3. 处理器内调用 `AuthzService.CanAccessInstance/CanManageGroup/CanAccessBot` 做资源级隔离判断；平台管理员全量放行

**隔离规则**：
- 实例：通过 `group_instances` 关联判断归属；未分配组的实例仅平台管理员可访问
- 跨组隔离：组 A 成员不能读写组 B 的实例/文件/终端/Bot；未授权访问返回 404（避免泄露存在性）
- 节点管理：限平台管理员
- 配额：创建实例时校验 `MaxInstances`/`MaxBots`/`MaxStorageMB`（0 表示不限）；`GET /groups/:id/quota` 返回用量

## 5. Worker Node 架构

```
┌─ 通信层 ────────────────────────────────────────────┐
│  grpc_server, ws_server                              │
├─ 进程管理层 ────────────────────────────────────────┤
│  ProcessManager → IProcessCommand (策略模式)         │
│    Direct / Daemon / Docker / RCON                   │
├─ 守护进程 ──────────────────────────────────────────┤
│  socket_server, java_process, output_buffer,         │
│  pid_file, commands, frame                           │
├─ 终端层 ────────────────────────────────────────────┤
│  terminal manager, session (PTY + 多观察者)           │
├─ Bot 管理层 ────────────────────────────────────────┤
│  bot manager, worker_pool, ipc, state, prewarm       │
├─ 指标采集 ──────────────────────────────────────────┤
│  collector, mc_ping, rcon_client                     │
├─ 群组服层 (V2) ─────────────────────────────────────┤
│  config_engine(round-trip+schema+校验),              │
│  resource_alloc(端口池+工作目录), jdk_manager,        │
│  launch_spec(结构化启动组装)                          │
└─────────────────────────────────────────────────────┘
```

### 目录结构

```
cmd/worker/main.go           # 含 daemon 子命令分支（wrapper 模式）
cmd/jmctl/                    # 紧急控制台 CLI（list/emergency/stop/kill），仅链 daemon 帧协议包（§6.7，FR-184/ADR-041）
internal/worker/
  config.go                                      # 加载 worker.yaml + env 覆盖（FR-080）
  heartbeat.go
  grpc/{server,handler_instance,handler_bot,handler_file,handler_metrics}.go
  process/{manager,command,direct,daemon,docker,gbk,detach,detach_unix,detach_windows}.go
  daemon/{wrapper,conn,conn_unix,conn_windows,pid_file,pid_alive_unix,pid_alive_windows,buffer,frame}.go
  terminal/{manager,session}.go
  ws/{server,auth,handler_terminal,handler_log}.go
  bot/{manager,worker_pool,ipc,state,prewarm}.go
  metrics/{collector,mc_ping,rcon_client}.go
  config/{parser,schema,validator,version}.go    # V2 配置引擎（保注释 round-trip）
  resource/{port_pool,workdir_alloc}.go          # V2 端口/工作目录系统分配
  jdk/{manager,registry,download}.go             # V2 JDK 托管
  provision/{core_download,launch_spec,clone}.go # V2 搭建/结构化启动/复制
  register/{register,identity}.go                # 注册（带 enroll token）+ 本地身份持久化（FR-080）
```

### 5.1 节点接入与部署（一键安装 / enrollment，FR-080，见 ADR-020）

- **配置加载**：Worker 启动时经 `internal/worker/config.go`（viper）真正加载 `worker.yaml`（CP gRPC 地址、grpc/ws 端口、data_dir、日志），`JIANMANAGER_` 前缀环境变量按路径覆盖。配置落盘取代历史的环境变量堆砌。
- **enrollment token 准入**：新增节点凭 CP 签发的**一次性、限时** enrollment token 注册（取代 FR-004 的「无凭据自助注册」对新节点的开放）。token 经 gRPC metadata `enroll-token` 传给 CP 校验消费（不改 proto）；CP 只对「新节点首次落库」设门槛，已有身份的重注册不强制 token（不破网）。
- **身份持久化**：注册成功换得的 `node_uuid`/`node_secret` 写入数据根 `etc/node-identity.json`（0600，含敏感 secret 不入日志）。Worker 重启优先读该文件复用既有身份走重注册，不重复消费已失效的一次性 token。
- **注册身份匹配（UUID 锚定，见 ADR-039，修复重名覆盖 BUG-A）**：`ControlPlaneHandler.Register` 按三级优先级匹配既有节点，杜绝「另一台机器用同名注册覆写旧节点身份/host」——
  1. **UUID 证明**：Worker 重注册时经 gRPC metadata `node-uuid` + `node-secret` 出示本地身份；命中库中节点且 secret 匹配 → 按 UUID 重注册（更新 host/port/os/arch，允许改名）；secret 不符 → `PermissionDenied`，绝不覆写。
  2. **同机 host 兼容（过渡）**：未升级旧 Worker 只带 name，name 命中既有节点且本次连接 host 与库存 host 一致（同机重启信号）→ 放行重注册并告警建议升级；host 不一致落到 3。
  3. **token 新建**：否则视为新节点，凭有效 enrollment token 准入；若上报名与既有节点撞名 → `AlreadyExists` 拒绝（提示改名），绝不覆写。
- **节点名活跃唯一**：身份由 UUID 锚定，`name` 降为可变标签但活跃节点间唯一——`database.AutoMigrate` 对存量重名活跃节点先去重（追加 `-dup-<id>` 后缀）再建「部分唯一索引」（仅约束 `deleted_at IS NULL` 的活跃行），软删除节点可释放其名供新节点复用（见 ADR-039 §3）。
- **坏节点检测/修复（见 ADR-039 §2）**：`NodeRepairService` 提供检测疑似被串改/重名节点（只读诊断）、把被挤占机器作为新节点重新 enroll（轮换 UUID/secret）、清理孤立 JDK/实例引用；破坏性操作需二次确认（`confirm=true`）并入审计（FR-015/FR-059）。HTTP 入口见 API.md 节点修复章节（UI 入口随 FR-177）。
- **一键安装脚本**：`scripts/install-worker.sh`（Linux/macOS）/ `install-worker.ps1`（Windows）由平台分发，幂等完成「下载或拷贝二进制 → 写 worker.yaml → 以 enroll token 首注册 → 可选注册 systemd / Windows 服务（开机自启、常驻自连）」。enroll token 仅经命令行/环境变量传入、绝不写入 `worker.yaml`。公网 release 端点未架设前以 `--binary` 本地二进制兜底。
- **CP 静态托管安装脚本**：脚本经 `go:embed` 内嵌进 CP 二进制（`internal/controlplane/embed/install_scripts.go`，源由 `make embed-install-scripts` 从 canonical `scripts/` 同步、字节一致由测试守护），CP 以**匿名**路由 `GET /install-worker.sh`、`GET /install-worker.ps1`（根路径、先于 SPA 回退）下发。一键命令 `curl <cp>/install-worker.sh | sh` 据此可拉（此前 CP 不托管这两路径致 curl 404、一键安装失败）。匿名安全：脚本无机密，准入凭据在命令参数里，与签发 token 的管理员 JWT 端点暴露面隔离。
- **面板「添加节点」向导**：CP `POST /nodes/enroll-token` 签发 token 并返回 Linux/Windows 一键命令 + `scriptBaseUrl`（CP 托管脚本基址），前端节点页展示一键命令（复制）+「手动安装步骤」分步兜底命令，供运维粘贴到目标机器执行。

## 6. 通信协议

### 6.1 gRPC（Control Plane ↔ Worker Node）

Protobuf 定义位于 `proto/worker.proto`，包含：

- 生命周期：Register, Heartbeat (双向 stream)
  - `Register` 的身份匹配经 gRPC metadata 携带 `node-uuid`/`node-secret`（重注册出示身份）或 `enroll-token`（新节点准入），均不改 proto；匹配优先级与重名覆盖防护见 §5.1（ADR-039）
  - `Heartbeat` 负载除节点指标（CPU/内存/磁盘/累计网络字节/`load_avg1` 系统负载，FR-062）外携带 `instance_metrics`（每实例 ServerProbe 快照：TPS/MSPT/在线/堆/线程/CPU/uptime + 分世界负载，FR-060）；CP 收心跳经 `IngestHeartbeat` 落库为时序样本（node_cpu/mem/disk/net 速率/load）并据相邻累计字节算网络速率（Worker 不碰 DB）
  - `Heartbeat` 还加性携带 `tasks`（`TaskSnapshot`：task_id/state/progress/error/result/recent_log_lines，FR-183/ADR-040）——Worker 把运行中长任务（如 JDK 安装）的进度随心跳上报，CP 经 `TaskService.IngestSnapshots` upsert `Task` + 幂等追加 `TaskLog`，并在任务**首次进终态**时触发副作用（jdk_install 成功落 `NodeJDK` + 发成功站内信，失败发失败站内信）。日志行编码为 `<绝对序号>\t<正文>`，跨周期重叠窗口按绝对序号去重
  - `HeartbeatResponse` 加性携带 `proxy_url`/`proxy_no_proxy`/`proxy_generation`（出站代理可视化下发，FR-185/ADR-043）——CP 据「节点 custom ? 节点值 : 全局默认」算每节点**期望出站代理**，每拍随心跳响应下发；Worker 仅当 `proxy_generation`（期望代理配置的 FNV 哈希）变化时才 `httpclient.New` 重建出站持有者（`httpclient.Provider` 原子替换，避免每拍重建），新 client 注入到各下载点（JDK/CFR/自更新/服务端 jar）即时生效。真相源 = CP DB（`nodes.proxy_*`），Worker **不落盘**，重连/重启由后续心跳天然重发；下发为空回退本地 `worker.yaml`/env。CP 自身出站代理由设置面板 `proxy.url`/`proxy.no_proxy`（settings DB 覆盖）管控、运行时重建，且作为各节点默认代理（优先级 settings DB > yaml > env）
- 实例操作：CreateInstance, StartInstance, StopInstance, RestartInstance, KillInstance, SendCommand, GetInstanceStatus, ListInstances
  - `CreateInstance` 除 `start_command` 外携带 `stop_command`（优雅停止命令，CP 按实例角色派生：backend/universal=`stop`，proxy=`end`），由 daemon wrapper 在优雅停止时写入进程 stdin；并携带 `probe_port`（CP 分配的 ServerProbe 端口，daemon 模式透传到 wrapper→PID 记录，供 Worker 心跳自采与重启恢复，FR-060）；以及 `graceful_stop_timeout_seconds`（CP 从平台设置 `graceful_stop.timeout` 取生效值随启动下发，daemon 透传到 wrapper 做超时强杀兜底，FR-063；值在启动时定型，对设置变更后新启动的实例生效）。docker 模式（FR-078，ADR-019）额外携带 `image`（容器镜像引用）与 `port_mappings`（容器端口↔宿主端口，宿主端口来自 FR-032 端口池），Worker 启动容器前据 `image` 自动拉取缺失镜像
- Docker 镜像管理（FR-078，ADR-019）：ListImages, PullImage, RemoveImage
  - CP 不直连 Docker，节点级镜像列出/拉取/删除经 Worker 委托（守架构边界）；`ListImages` 在节点 Docker 不可用时回 `docker_available=false`，CP 据此提示安装 Docker
- 实例事件流：StreamInstanceEvents (server stream)
  - 同一流承载两类事件：`state_change`（状态转换）与 `stdout`/`stderr`（进程输出）。Worker 进程输出回调分流为「WS 终端广播 + 事件流上报」两路，互不阻塞。CP 侧 EventService 把 `stdout`/`stderr` 经 LogService 落库（日志中心 FR-049），`state_change` 经 SSE 推前端
- 文件操作：ListFiles, ReadFile, WriteFile, DeleteFile, RenameFile（跨目录即移动）, UploadFile (client stream), DownloadFile (server stream), DownloadArchive (server stream), SearchFiles
  - `DownloadArchive` 把选中的文件/目录（目录递归，仅常规文件）即时打包为 zip 边遍历边分片流式返回（每条目经 `validatePath` 防越界/zip-slip，~32KiB 分片，不缓冲整包）；CP `FileHandler.DownloadArchive` 逐帧 `Recv` 写响应并 `Flush`，转为 HTTP `application/zip`（批量下载，FR-070）。资源管理器树内拖拽「移动」复用 `RenameFile`，无独立 move RPC
  - `SearchFiles` 对实例工作目录做全文搜索 / 文件名快速打开（FR-074，见 ADR-017）。索引是 **Worker 本地派生资产**（落数据根 `var/index/<instance-uuid>/`，**不进 CP 数据库**）：Worker 每实例持有一份倒排索引（token→文件集合）+ 文件指纹表，查询前按指纹比对增量更新（增/改/删）再倒排取候选、候选内精确行扫描；`mode=filename` 走文件名子串匹配（行号 0）。CP 仅经 gRPC 转发查询、不持有索引
- 归档浏览与反编译（FR-075；见 ADR-018）：ListArchiveEntries, ReadArchiveEntry, DecompileClass
  - `ListArchiveEntries`/`ReadArchiveEntry` 用 Go `archive/zip` **只读**列举/读取 jar/zip 内部条目（不起进程、零落盘，条目名经 zip-slip 校验，条目数/单条目字节有上限超出截断，内容嗅探 NUL 判二进制）；`DecompileClass` 经实例绑定 JDK（或系统候选 JDK / `JAVA_HOME` 兜底）**受控 exec** CFR 单 jar 把 `.class`/`.jar`（或 jar 内某 `.class` 抽临时文件）反编译为 Java 源码——CFR 仅静态分析字节码、不加载/运行目标代码，`context` 超时 + 输入体积上限 + 输出截断 + 失败/降级以 `success=false`+结构化 error 返回（不抛错）。CP 加性端点 `GET .../files/archive/entries`、`GET .../files/archive/read`（octet-stream + `X-Truncated`/`X-Binary` 头）、`POST .../files/decompile`，均复用文件「查看」级权限。CFR 分发：配置路径 > 内嵌（`make embed-cfr`，gitignore 不入库）> 数据根缓存 `var/tools/cfr-<ver>.jar` > Maven Central 按需下载（sha256 pin）
- 终端：IssueTerminalToken
- Bot：CreateBot, DeleteBot, ListBots, StreamBotEvents (server stream), SendBotCommand
- 探针部署：DeployServerProbe（CP 内嵌 ServerProbe jar + 生成的 config.yml 经 gRPC 下发到实例 plugins 目录，FR-010；见 ADR-014）。**在线更新**（FR-068）复用本 RPC 推最新内嵌 jar（下次重启生效，可选推送并重启），经 `GET/POST /instances/:id/probe/update`
- 插件桥（FR-065；见 ADR-016）：StreamPluginEvents (server stream，CP 订阅某实例/全部探针经反向 WS 上报的事件流 connected/disconnected/heartbeat/玩家事件)、SendPluginCommand（CP 经 Worker 向探针下发治理/查询指令）、QueryServerState（查询子服全状态骨架）。地基阶段真实承载 connected/disconnected/heartbeat 与通道层，业务事件/治理执行语义留 FR-066/067
  - **JBIS 业务事件汇聚上行（FR-122；见 ADR-027/028）**：同一条 StreamPluginEvents 流复用承载 `domain` 非空的业务域事件（PluginEvent 的 `domain`/`dedup_key`/`raw_json` 字段，Worker 透传不消费语义）。CP 侧 `PlayerEventService` 据 `domain` 分流：玩家/监控事件走在线名册 + SSE，业务事件交 `BusinessEventService` 按 (domain,dedup_key) 去重落 `business_events` 通用信封，经济域(`economy`)再解析信封维护 `economy_balance_mirrors`(node→zone 维度、seq 单调)+`economy_ledger_entries`(审计)。探针侧由 mce `PlayerEconomyChangeEvent`/`PlayerEconomyCatchupEvent` 折算上报（覆盖 web 后台/跨服一切余额变更），currencyId Int→identifier 折算保证跨服聚合不串味。CP 插件无关、只认信封
- 指标：GetNodeMetrics, GetInstanceMetrics（请求带 probe_port，Worker 抓 ServerProbe `/metrics`；**RCON 已退役（FR-067/ADR-016）**——探针未就绪时富指标 N/A，不再回退 RCON）——用于**实时**面板的 CP 主动拉取；**历史时序**（FR-060）改由 Worker 心跳推送 `instance_metrics`，二者互补
- 玩家管理：SendPluginCommand（FR-067/ADR-016；CP 经 Worker 反向 WS 向探针下发踢/封/解封/白名单治理指令，探针经服务端 API 执行；在线列表经探针事件聚合）。**RCON 路径已退役**，`ExecRconCommand`/`rcon_client` 移除；探针未连入时优雅降级
- 配置 (V2)：ListConfigFiles, ReadConfig, WriteConfig, ListConfigVersions, RollbackConfig
- 运行时 (V2)：ListJDKs, InstallJDK, RemoveJDK, JDKCatalog, DownloadCore, ListArtifactCache, EvictArtifactCache, ClearArtifactCache, SetArtifactCacheCap, BrowseDir
  - `InstallJDK` 携带 `mirror_base`（CP 从平台设置 `jdk.mirror.<vendor>` 取生效值后下发；Worker 用它构造下载 URL，使运行时配置的镜像源真生效，FR-033/FR-063；为空回退 Worker 本地 env/官方默认源）
  - `InstallJDK` 加性携带 `task_id`（FR-183/ADR-040）：非空时走**异步**——Worker 登记内存任务表、`go` 后台下载（带字节进度计数）、RPC 立即返回 `task_id`（不再阻塞最长 20min），进度/日志/终态经心跳 `tasks` 上报，CP 据终态落 `NodeJDK` + 发站内信；为空回退同步路径（向后兼容）
  - `InstallJDK` 加性携带 `version`（FR-178）：非空时 Worker 经 **foojay disco API** 按具体版本解析下载源；为空取该大版本最新 GA
  - `JDKCatalog`（FR-178）：Worker 经 foojay disco `/packages` 查某发行版可选具体版本（扩厂商至 Liberica/Microsoft/Semeru/GraalVM… + 保留 Temurin/Corretto/Zulu 直链回退），CP 代理喂前端版本选择器；`buildDownloadURLV` 在 `internal/worker/jdk/foojay.go` 统一「直链回退 vs foojay 解析」分流
  - **节点制品缓存**（FR-178，`internal/worker/artifactcache`）：`DownloadCore` 改为**内容寻址缓存命中复用**——按 `sha256` 命中即从 `var/artifact-cache/` 秒拷到实例工作目录（免网络、`touch` lastUsed），未命中下载校验后存入缓存再拷；`sha256` 为空不缓存（缓存键必须是 sha256）。**范围写死：仅服务端核心 jar，不缓存插件/其它下载路径**。`ListArtifactCache`/`EvictArtifactCache`/`ClearArtifactCache`/`SetArtifactCacheCap` 经 CP 端点（仅平台管理员 + 审计，CP 用 asset 表按 sha256 补全 name/version）管理这份缓存；容量上限 `artifact_cache.max_bytes`（worker.yaml + CP 运行时下发）触发按 `lastUsedAt` 升序 LRU 淘汰；写用临时文件 + 原子 rename 并发安全
  - `BrowseDir`（FR-178）：只读列出节点某绝对路径下的子目录（空路径返回盘符/根），经 CP 端点 `GET /nodes/:id/browse`（仅平台管理员、防穿越）供前端 JDK 路径登记目录选择器逐级浏览
- 复制 (V2)：CloneWorkDir（本机复制源工作目录到目标，排除运行态文件）
  - 搭建子服/代理由 Control Plane 编排：分配端口/目录 → CreateInstance → DownloadCore → WriteConfig，不另设 worker 端 Provision RPC
- 备份 (V2)：CreateBackup, RestoreBackup（FR-056/057）
  - Worker 把工作目录打 tar.gz 落数据根 `var/backups/<instanceUUID>/`，据 base_manifest 做增量差异，始终回传完整文件清单供 CP 维护链/基准
  - 恢复按链顺序（全量基 + 各增量）回放；远程后端（S3/SFTP/WebDAV）由 Worker 持 CP 下发的 StorageBackendSpec 直传/拉回，凭证由 CP 从 `${ENV_VAR}` 解析后下发（Worker 不读环境/不碰 DB）

### 6.2 WebSocket（浏览器 ↔ Worker Node）

终端直连，Control Plane 签发一次性 30s token 鉴权：

```
Browser → Control Plane (GET /terminal/token)
  → 返回 {token, ws_url}
Browser → Worker Node (WS ws://worker:port/ws/terminal?token=xxx)
  → 双向终端流
```

消息格式：

```json
// Worker → Browser
{"type":"stdout","instanceId":"xxx","data":"..."}
{"type":"stderr","instanceId":"xxx","data":"..."}
{"type":"state","instanceId":"xxx","state":"RUNNING"}

// Browser → Worker
{"type":"stdin","instanceId":"xxx","data":"..."}
{"type":"resize","instanceId":"xxx","cols":120,"rows":40}
```

### 6.2.1 监控探针 ServerProbe（Worker 抓 `/metrics`，FR-010 / ADR-014）

ServerProbe 是第三方监控探针（TabooLib，单 jar 多端 Bukkit+BungeeCord），作 git 子模块引入 `third_party/ServerProbe`。
CP 经 `go:embed` 内嵌探针 jar（`internal/controlplane/embed/probe/`，`make embed-probe` 目标可选构建），
建服 provision 时经 gRPC `DeployServerProbe(jar, config_yaml)` 把 jar 与最小 config.yml 写入实例 `plugins/`。

每实例系统分配一个 probe 端口（默认 29940 段，同节点唯一）；config.yml 仅开启 `/metrics`、绑定 `127.0.0.1`、监听分配端口。
Worker 抓取链路完全在本机回环、无对外网络面、无 token：

```
provision → CP DeployServerProbe(jar+config) → Worker 写 plugins/ServerProbe.jar + plugins/ServerProbe/config.yml
GetInstanceMetrics(req) → Worker → HTTP GET http://127.0.0.1:<probe_port>/metrics → 解析 serverprobe_* → 富指标
                                  ↓ 探针未就绪/抓取失败
                                  富指标 N/A（RCON 已退役 FR-067/ADR-016，不再回退）
```

同一抓取链路有两个驱动方：**实时面板**由 CP 按需 `GetInstanceMetrics` 拉取；**历史时序**（FR-060）由 Worker 心跳 tick 自抓本机各 RUNNING 实例 `/metrics`，装入 `Heartbeat.instance_metrics` 上报，CP 分级降采样落库。probe 端口经 `CreateInstance.probe_port` 下发并持久化到 daemon PID 记录，Worker 重启可恢复自采。

被抓取的关键指标（解析后透传给 CP/前端）：

```
serverprobe_tps{window="1m"}                → TPS
serverprobe_mspt_seconds{quantile="avg"}    → MSPT（毫秒）
serverprobe_players_online                  → 在线人数（代理端回退 proxy_players_online）
serverprobe_heap_used_bytes / max_bytes     → 内存 used/max
serverprobe_threads                         → 线程
serverprobe_system_cpu_load                 → CPU 占用（0~1，前端转 %）
serverprobe_uptime_seconds                  → 运行时长
serverprobe_world_{loaded_chunks,entities,tile_entities}{world=}  → 按世界负载

### 6.2.2 插件桥反向 WebSocket（探针 ↔ Worker，token，FR-065 / ADR-016）

在 `/metrics` 只读抓取之外，ServerProbe fork 还经**反向 WebSocket** 主动连入本机 Worker，建立实时双向通道（治理/事件/在线更新/全状态查询的地基）。探针只与本机 Worker 通信，绝不直连 CP/DB/gRPC。与 `/metrics` 抓取并存互补：前者只读拉指标，后者双向承载事件与指令。

- 端点：Worker 暴露 `GET /ws/plugin-bridge`，与 `/ws/terminal` 并列、同一 WS 监听端口。
- 方向：探针**主动反向连入** `ws://127.0.0.1:<wsPort>/ws/plugin-bridge?token=<jwt>&instance=<uuid>`（本机回环，零额外对外网络面）。
- 鉴权（实例级 token，复用 JWT secret）：CP 为实例签发 HS256 token（claims `instanceId`+`scope=plugin-bridge`，TTL 数分钟），随探针 config 的 `bridge:` 段下发；Worker 校验**签名 + `scope==plugin-bridge` + token 内 `instanceId == query.instance`** 后建会话，仅握手校验一次。
- 会话表：Worker 维护「实例 UUID → 探针会话」，同实例单活动会话、**新连顶替旧连**；连接/断开冒泡 `connected`/`disconnected` 事件经 gRPC `StreamPluginEvents` 到 CP。
- 心跳与重连：探针周期发 `ping`、Worker 回 `pong`，Worker 读超时判定断线；探针断线后自身指数退避重连（初始 ~1s，上限 ~30s）。
- 探针侧载体：ServerProbe fork core 模块 `BridgeClient`（IOC `@Service`，`@PostEnable` 起 `@PreDestroy` 停），JDK 8 兼容、零三方依赖的最小 RFC 6455 客户端（`MinimalWebSocketClient`）。

```
建服 provision → CP 签发 plugin-bridge token → 写入探针 config.yml 的 bridge 段（url+instance+token）
探针启用 → BridgeClient 反向连入 ws://127.0.0.1:<wsPort>/ws/plugin-bridge?token=&instance=
  → Worker 校验 token + 建会话(单活动顶替) → 回 welcome
  → 探针发 hello + demo connected 事件；周期 ping/pong 心跳；断线指数退避重连
上行：探针 →(WS) Worker →(gRPC stream StreamPluginEvents) CP →(SSE /instances/:id/players/events) 浏览器
下行：浏览器 →(HTTP) CP →(gRPC SendPluginCommand) Worker →(WS) 探针
```

**实时玩家事件（FR-066，见 ADR-016）**：探针监听玩家进出与跨服路由经反向 WS 上报，Worker 解析结构化字段（玩家名/UUID/消息/子服/from·to）填充 `workerpb.PluginEvent` 冒泡到 CP；CP 侧 `PlayerEventService` 订阅各 Worker 的 `StreamPluginEvents`，维护「实例 UUID → 实时在线名册」（connected 重置、player_join 加入、player_quit 移除、cross_server 更新所在子服、disconnected 清空），并经 SSE `/instances/:id/players/events` 推给前端（首帧 `init` 含连接状态 + 名册快照，之后 `player` 增量）。
- 子服端载体：ServerProbe fork `platform-bukkit` 的 `BukkitPlayerEventListener`（`@SubscribeEvent` 监听 PlayerJoin/Quit/AsyncChat，本子服视角）。
- 代理端载体：`platform-bungee` 的 `BungeePlayerEventListener`（监听 PostLogin/ServerSwitch/PlayerDisconnect，给出精确跨服路由 from→to）。
- 二者经 core `BridgeClient.emitPlayerEvent` 出口上报；插件桥开关关闭（独立使用探针）时不上报。

> 地基阶段（FR-065）打通通道层（会话/握手/心跳/connected·disconnected 冒泡 + proto 一次铺齐）；实时玩家事件采集（FR-066）已落地（见上）；治理执行 + 退役 RCON（FR-067）、在线更新（FR-068）为下游 FR，复用本通道、不再改 proto。

### 6.3 守护进程二进制帧协议

Worker Node 与 daemon wrapper 子进程之间通过二进制帧协议通信。
传输层跨平台：Linux/macOS 用 **Unix Socket**（`<pidDir>/<uuid>.sock`），Windows 用 **Named Pipe**（`\\.\pipe\jianmanager-<uuid>`，基于 `npipe`）。

该 socket 的本机访问方有二：常态由 **Worker Node** 连接（`daemonStrategy`）；CP/Worker 不可用时由 **jmctl 紧急控制台 CLI**（本机运维工具，见 §6.7 / ADR-041）应急直连。二者均仅限本机；浏览器/网络永不直触守护进程 socket（架构不变量）。

```
帧结构 (8 字节头 + 可变载荷):
┌─────────┬──────┬──────┬───────────┬───────────────────┐
│ Channel │ Type │Flags │  Length   │     Payload       │
│  2 B    │ 1 B  │ 1 B  │   4 B    │   0 ~ 4 MB        │
└─────────┴──────┴──────┴───────────┴───────────────────┘

Channel: STDIN(0) STDOUT(1) STDERR(2) CONTROL(3)
Type:    DATA(0x01) COMMAND(0x02) RESPONSE(0x03) HEARTBEAT(0x04)
Flags:   bit0=compressed(zlib)
```

#### daemon wrapper 生命周期（ADR-003）

- **进程隔离**：Worker spawn 独立 wrapper 子进程（复用 worker 二进制的 `daemon` 子命令，配置经 `JM_DAEMON_WRAPPER_CONFIG` 环境变量传递），wrapper 通过 `SysProcAttr{Setsid}`（unix）/ `CREATE_NEW_PROCESS_GROUP`（windows）脱离 Worker 进程组。Worker 退出/重启时 wrapper 继续运行。
- **角色**：wrapper 作为 Java 游戏服进程的父进程，负责启动/指数退避重启 Java、监听 socket、与 Worker 双向帧通信、维护 PID 文件。
- **stdio 转发**：Java 的 stdout/stderr 由 wrapper 编码为 `ChannelStdout/Stderr` 帧发给 Worker，Worker 的 `daemonStrategy.readLoop` 解码后桥接到 `onOutput`（→ WebSocket 终端）；Worker 下发的 stdin/控制命令通过 `ChannelStdin/Control` 帧发给 wrapper。
- **控制命令**（`ChannelControl` + payload 文本）：`stop`（优雅停止）、`kill`（强制）、`ping`（心跳，回 `pong`）。
- **优雅停止命令按角色派生**：收到 `stop` 控制帧后，wrapper 向进程 stdin 写「关服命令」——MC 后端用 `stop`、代理（BungeeCord/Waterfall/Velocity）用 `end`（代理不认 `stop`，误发会挂到超时才强杀）。该命令由 CP 按实例角色派生、经 `CreateInstance` 的 `stop_command` 字段下发并烤进 `WrapperConfig`；为空时回退 `stop`。超时（`JIANMANAGER_GRACEFUL_STOP_TIMEOUT`，默认 30s）仍未退出则强杀兜底。
- **重启前等待上一代退出**：daemon 策略 `Start` 前按 PID 文件等待上一代 wrapper/Java 完全退出（`WaitForPriorExit`，上限 `JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT`，默认 15s），避免快速 stop→start 时旧进程仍占监听端口/socket 导致新进程端口冲突崩溃（`exit status 1`）。
- **强制终止杀整树**：`daemonStrategy.Kill`（重启/强制终止路径）除发 `kill` 控制帧外，兜底用 `taskkill /T` 终止 wrapper→cmd→Java 整棵进程树；不可只杀 wrapper PID，否则 Windows 上 Java 孤儿化继续占监听端口，紧接的 `Start` 会因端口被占而 `BindException` 崩溃。
- **PID 文件恢复**：wrapper 写 `<pidDir>/<uuid>.pid`（JSON：wrapper pid、java pid、socket 地址、instance uuid）。Worker 启动时 `Manager.RecoverDaemonInstances` 扫描 PID 文件，wrapper pid 存活则 reconnect socket 恢复管理，否则清理文件与残留 socket。
- **优雅退出**：daemon 模式下 `Manager.StopAll` 只断开与 wrapper 的连接，不杀游戏服（direct 模式才终止进程）。

#### docker 容器化实例生命周期（ADR-019，FR-078）

- **管理方式**：`dockerStrategy`（`process/docker.go`）作为 `IProcessCommand` 第三种实现，Worker 经本机 Docker Engine API（`github.com/docker/docker/client`，`FromEnv` 自动发现守护进程）管理容器，不叠 daemon wrapper（隔离由 Docker 守护进程提供）。CP 不直连 Docker，所有容器/镜像操作经 gRPC 委托 Worker。
- **容器模型**：一个实例 ⇄ 一个容器，命名 `jianmanager-<uuid>`；`tty=false` + 三路 attach（stdin/stdout/stderr）。`Start` 前若本地缺镜像则 `ImagePull` 拉取，随后 `ContainerCreate`→`ContainerAttach`→`ContainerStart`。
- **工作目录/端口**：系统分配的实例工作目录（ADR-010 数据根的宿主绝对路径）bind-mount 到容器 `/data`，使文件/备份/配置走同一套宿主路径；端口经 `PortBindings` 把容器内端口（MC 约定 25565）发布到宿主端口（FR-032 端口池分配），不引入新网络面。
- **stdio**：容器多路复用输出经 `stdcopy.StdCopy` 解复用为 stdout/stderr 路由到 `onOutput`（→ WS 终端 + 日志采集 FR-049）；终端输入与优雅停止命令经 attach 连接写入容器 stdin。
- **状态机/重启**：容器退出由 `ContainerWait` 异步监听，非正常退出回写 CRASHED 并触发指数退避重启（与 direct 策略一致，统一在 Manager 层记账）。`Stop` 先经 stdin 下发停止命令再 `ContainerStop`（宽限期后 SIGKILL）；`Kill` 用 `ContainerKill`+`ContainerRemove` 确保端口/卷彻底释放。
- **JDK**：docker 模式不注入宿主 JDK（JAVA_HOME/PATH），JDK 随镜像提供（ADR-008 的 JDK 注入对 docker 不适用）。

### 6.4 Bot Worker IPC

```
Go → Node.js (stdin, JSON 行):
  {"cmd":"create-bots","bots":[...]}
  {"cmd":"stop-bots","botIds":[...]}
  {"cmd":"set-behavior","botId":"b1","behavior":"follow","target":"player"}

Node.js → Go (stdout, JSON 行):
  {"evt":"bot-state","bots":[...]}
  {"evt":"bot-event","botId":"b1","type":"chat","data":{...}}
  {"evt":"bot-error","botId":"b1","error":"ECONNREFUSED"}
```

### 6.5 客户端 OTA 公网分发端点（玩家 updater ↔ Control Plane，FR-087 / ADR-022/023）

Control Plane 新增一类**面向玩家公网**的 HTTP 分发端点（客户端 OTA 更新器拉取，非浏览器）：

- **消费端点（玩家，`X-Client-Key` 拉取密钥鉴权）**：`GET /client-channels/:id/manifest`（latest 的 Ed25519 签名 manifest，ETag/304）、`GET /client-artifacts/:sha256`（client-file 制品内容寻址下载，`http.ServeContent` 支持 Range/206）。挂公网 `api` 组（仅限流、无 JWT）。
- **发布端点（运营，JWT 平台管理员，与 FR-086 频道管理同组）**：`POST /client-channels/:id/files`（上传制品入 FR-045 制品库 type=client-file）、`POST /client-channels/:id/versions`（发布版本、单调递增、切 latest 指针）。

**鉴权与信任分层（ADR-022）**：拉取密钥**半公开**（随整包分发必泄露），仅作鉴权路由 + 吊销、**不作内容可信依据**；内容可信靠 manifest 的 **Ed25519 签名**（updater 内置公钥验签，私钥服务端 env 持有）+ **单调 version 防降级**。消费端点与运营浏览器 JWT 入口、发布端点**物理隔离**；L7 防护（限流以 IP 为主）见 ADR-023。manifest 格式与 canonical 签名见 `docs/specs/client-distribution/contract.md`。

**L7 应用层防护（FR-096，见 ADR-023）**：消费端点（manifest/制品）独立子组挂 `ClientDistGuard` 中间件——IP 黑白名单（`client_ip_rules`，deny 优先、有 allow 即白名单模式，运行时可改+入审计）+ **per-IP 令牌桶限流**（机器码不可信，限流以 IP 为主）+ 全局并发信号量；命中拒 403/429，内存计数器经 `GET /client-dist/protection-stats` 可观测（不按请求写库防放大）。缓存即防护（ETag/304 + 内容寻址强缓存，CDN 前置）。**L3/L4 容量型 DDoS 靠 CDN/Anycast/云清洗，不在 JM**。

### 6.6 客户端 OTA 更新器（玩家侧两件套纯 JVM jar，`client-updater/`，FR-089/090/091 / ADR-021）

启动器经 `-javaagent:wedge.jar=<gameDir>` 注入。**楔子（wedge，Java 8，稳定件随基础包分发）** premain 自定位、读同目录 `jm-updater.json`、以独立 `URLClassLoader` **内存加载** updater-core（不锁原 jar）、反射 `Core.run(ctx)`、同步等待 + 超时，全程 **fail-open**（任何异常都放行游戏）。**updater-core（Java 8，兼容低版本游戏 JVM；fat jar 自含 zstd-jni + BouncyCastle）** 拉签名 manifest → Ed25519 验签（BouncyCastle，因 Java 8 无 JDK 内置 Ed25519）+ 防降级 → 文件级 reconcile（增量/减量、托管区/玩家区隔离、CAS）→ 端点不可达 **fail-static** 带本地版本放行。HTTP 用 `HttpURLConnection`（Java 8 无 `java.net.http`）。两件套包名 `top.wcpe.mc.jm.updater.{wedge,core}`。

**core 自更新 + N-1 回退（FR-091）**：manifest `agent.core`（version + 各平台制品）声明更高 core 版本时，core 下载 → sha256 校验 → **selftest**（独立 classloader 载新 jar 校 ABI + zstd 链路）→ 暂存 **pending**；下次 premain 楔子据 `<gameDir>/.jm-updater/core/state.properties`（`selected/prev/pending/tried`，java.util.Properties，wedge↔core 共享格式）跑**选择状态机**：首次加载 pending=**trial** 并起 **boot-confirm 看门狗**（daemon 存活 `bootConfirmSec` 即建 `pending.confirmed` 标志）；下次启动若 pending 已 tried 且无 confirmed（判定上次崩溃/早退）→ **回退 N-1**（弃 pending、留 selected）；已确认 → **promote**（selected=pending、旧 selected 降 N-1）。core jar 内容寻址存 `core/<sha>.jar`，**N-1 零额外全量**。手动回退置 `rollback.flag`。运营整体回滚见 FR-088（服务端以更高 version 重发）。

**机器码身份（FR-092）**：updater 生成稳定、跨平台、不可逆的机器码（多硬件/环境特征组合 SHA-256，首次计算后持久化于 `<userHome>/.jm-updater/machine-id` 保稳定容错），随 manifest/制品请求经 `X-Machine-Id` 携带；CP 在 manifest 拉取时 best-effort 登记入 `client_machines`。**客户端生成、不可信**——仅统计 + 辅助限流，不作信任/授权依据（限流主键为 IP，ADR-023）。

**`.jmpack` 容器（FR-097）**：自有分发容器格式 `magic+版本+flags+meta(路径/sha256/大小/codec/偏移)+payload(各文件压缩段拼接)+尾部 Ed25519 签名`，**签名覆盖原始字节**（非 canonical，Go 服务端打包 / Java updater-core 解包跨语言字节一致，golden 向量固化）。服务端 `JmPackService.PackVersion` 复用已存制品 + 签名入库 `type=client-pack`；客户端 `JmPack.unpack` 验签 + 逐文件按 codec 解压 + sha256 校验。flags 预留加密/diff 位（首期不加密；块级 diff 见 FR-098）。**与 per-file 投递正交**——per-file 仍主投递，`.jmpack` 为就绪容器。

**遥测（FR-094）**：updater reconcile 后 best-effort `POST /client-telemetry`（拉取密钥 + X-Machine-Id，**202 不阻塞**）上报结果/版本/环境(os/java/启动器粗粒度)/耗时/bootSuccess；**隐私 opt-out**（`jm-updater.json` `telemetry:false` 关闭），仅环境粗粒度 + 不可逆机器码、不收集敏感数据。CP 落 `client_telemetry`（明细短保留）+ `client_telemetry_daily`（按 result 日聚合），供 FR-095 成功率/回退率。端点挂 FR-096 守卫。

**统计后台（FR-095）**：`GET /client-dist/stats?channelId=&days=`（平台管理员）**只读聚合** FR-093/094/092 既有表（不引入新表）——下载量趋势/版本分布（`client_dist_daily`）、更新成功率/回退率/结果分布（`client_telemetry_daily`）、活跃机器码数/来源 IP Top10（`client_dist_events` 近窗）。管理台频道详情「统计」Tab 看板（下载趋势复用 `TimeSeriesChart` + 版本分布/数字卡/IP 表，i18n + 暗亮主题）。

**接入指引 + 内嵌更新器 jar（FR-107）**：CP 经 `go:embed` 内嵌 wedge.jar + updater-core.jar（`internal/controlplane/embed/client-updater/`，`.gitignore` 占位 + `make embed-client-updater` 注入，同 ServerProbe 内嵌套路），经平台管理员端点 `GET /client-dist/updater-jars[/:component]` 下载（管理面 JWT，不用拉取密钥）。管理台频道详情「接入指引」Tab 面向**运营方**一页拿齐：下载两 jar + 该频道**专属可复制** `jm-updater.json`（channel/endpoint/密钥占位）+ 启动器 `-javaagent:jm-updater\wedge.jar` 参数（相对路径推荐）+ 放置步骤 + 行为说明（fail-static/fail-open/进度窗/与 authlib-injector 共存）。纯运营面、不改 OTA 协议/manifest/客户端 jar。

### 6.7 jmctl 紧急控制台（本机直连守护进程 socket，`cmd/jmctl/`，FR-184 / ADR-041）

当 Control Plane 与 Worker Node 同时不可用（崩溃/升级）时，daemon wrapper（§6.3，ADR-003）仍托管着运行中的游戏服进程，但**只有 Worker 会说守护进程帧协议**——运营者够不到那台进程（看不到输出、发不了指令、无法优雅停）。`jmctl` 是**独立轻量二进制**（控制面/Worker 之外的第三个 Go 入口），**绕过整个栈、纯本机、依赖极少**地直连守护进程 socket，做「最后一公里」应急运维。

- **依赖边界**：只链 `internal/worker/daemon`（frame + conn + pid_file）与 stdlib-only 的 `internal/platform/dataroot`，**不引入** gRPC / 数据库 / Worker service / CP；编译产物约 3.6MB。依赖闭包经 `go list -deps ./cmd/jmctl` 校验无重量级传递依赖（保证 daemon 包可独立链接，落地无需把帧协议下沉为更中立的包）。
- **寻址与发现**：扫 `pidDir` 下全部 `<uuid>.pid`（`PIDRecord`）即发现本机受管实例，`IsPIDAlive` 探测 wrapper 存活，无需 CP/Worker。`pidDir` 解析优先级：`--pid-dir` > `$JIANMANAGER_DATA_DIR/var/servers` > `./data/var/servers`——**与 Worker 实际写入路径对齐**（`process.Manager` 的 `pidDir` 即 `serversDir` = 数据根下 `var/servers`，ADR-010；ADR-041「以 Worker 实际写入路径为准」据此定为 `var/servers` 而非泛指 `var/pid`）。
- **命令集**（被帧协议界定）：`list`（非交互列本机全部 daemon：UUID/存活/wrapper·java PID/工作目录）、`emergency [--instance <uuid 前缀>]`（交互终端：无参数列存活实例供选择，`Dial` socket 后 Stdout/Stderr 帧实时流到终端、键入行作 Stdin 帧发入、`Ctrl+C` 发 Control 通道 `stop`、2 秒内连按两次发 `kill`、daemon 退出自动退出）、`stop <uuid 前缀>` / `kill <uuid 前缀>`（单发后退出）。所有 `<uuid>` 参数支持**唯一前缀补全**（类 docker/git 短 ID：唯一匹配自动选定、多匹配列候选报错、无匹配报错）。**不做 restart/创建**（启动命令派生属 Worker 职责，ADR-008，jmctl 无 launch spec）。
- **安全模型（ADR-041 §3）**：纯本机、无网络面（只打开本机 socket，不监听端口）、不额外鉴权（能在本机读写守护进程 socket 即等同宿主级运维权限；token/JWT 在 CP 宕机态无处校验）。架构不变量「浏览器/网络永不直触守护进程 socket」不变——jmctl 仅限本机操作者。

## 7. 数据库模型

### ER 关系

```
User ──M:N──▶ Group (GroupMember)
Group ──1:N──▶ GroupQuota
Group ──M:N──▶ Instance (GroupInstance, UNIQUE instance_id)
Node ──1:N──▶ Instance
Instance ──1:N──▶ Backup / Schedule / Bot
Backup ──N:1──▶ Backup (parent_id, 增量备份链, V2)
Backup ──N:1──▶ BackupStorage (storage_id, 远程存储位置, V2)
Instance(proxy) ──M:N──▶ Instance(backend)   # V2 ServerRegistration: alias/priority/forced_host
Network ──M:N──▶ Instance                    # V2 NetworkMember（非独占软标签）
Node ──1:N──▶ NodeJDK                         # V2
Instance ──1:N──▶ InstanceConfigVersion       # V2（仅配置文件，FR-031）
Instance ──1:N──▶ FileVersion                 # V2（任意文件改前快照，FR-051）
AuditLog ──N:1──▶ User
Task ──1:N──▶ TaskLog                          # V2 FR-183（任务滚动日志, ADR-040）
Task ──N:1──▶ User (created_by, 归属/收件人)    # V2 FR-183
Notification ──N:1──▶ User (user_id, 收件人)    # V2 FR-183（站内信, ADR-040）
AlertRule ──1:N──▶ AlertEvent
AlertRule ──N:M──▶ AlertChannel               # V2 channel_ids(JSON 软引用, FR-085 通知路由)
```

### 核心表

| 表 | 关键字段 |
|---|---|
| users | uuid, username, password(bcrypt), role(0/1/10), mfa_secret, status |
| groups | uuid, name, description |
| group_members | group_id, user_id, role(0=member/1=admin) |
| group_quotas | group_id(UNIQUE), max_instances, max_bots, max_storage_mb |
| nodes | uuid(UNIQUE，身份锚定键，ADR-039), name(活跃唯一：部分唯一索引 `uniq_nodes_name_active` WHERE deleted_at IS NULL，软删可释放名), host, grpc_port, ws_port, secret, status(0/1/2), maintenance(bool, cordon 维护模式，与在线/离线正交), os, arch, cpu_cores, memory_mb, disk_total_mb, load_avg1(V2, 系统负载, FR-062), proxy_mode(inherit/custom, 出站代理模式, 默认 inherit, FR-185/ADR-043), proxy_url(节点自定义代理, 仅 custom, 含凭据/API 脱敏), proxy_no_proxy(节点自定义免代理列表, 仅 custom), last_heartbeat, deleted_at |
| instances | uuid, node_id(FK), name, type, role(proxy/backend/universal, V2), process_type, status, start_command, work_dir(系统分配), env_vars(JSON), auto_start, auto_restart, jdk_id(FK, V2), launch_spec(JSON: jvm_args/core_jar/args/omit_nogui, V2), image(docker 模式镜像引用, FR-078), container_id(docker 模式最近容器 ID), rcon_*, forwarding_secret(V2, Velocity 转发), proxy_online_mode(V2, 代理正版校验), server_port/query_port, probe_port(V2, ServerProbe /metrics 端口, 29940 段), mc_*, tags(JSON) |
| group_instances | group_id, instance_id(UNIQUE) |
| instance_group_nodes (V2, FR-165) | uuid, name, parent_id(自引用 FK, NULL=根), sort, deleted_at（实例组织分组树节点，邻接表表达多级嵌套；正交于用户组/网络群组，仅组织归类，ADR-033）；INDEX(parent_id) |
| instance_group_members (V2, FR-165) | group_id(FK instance_group_nodes), instance_id(FK)；UNIQUE(group_id, instance_id)（实例-组织分组 M:N，一实例可属多组；删组只解绑、不删实例） |
| bots | uuid, instance_id(FK), name, status, config(JSON), behavior, worker_id |
| backups | uuid, instance_id(FK), name, file_path, file_size_mb, type(0/1), mode(0 全量/1 增量, V2), status(0/1/2/3), parent_id(FK self, 备份链, V2), manifest(JSON 文件清单, V2), storage_id(FK, V2), storage_key(远程对象键, V2) |
| backup_storages | name(UNIQUE), type(local/s3/sftp/webdav), endpoint, bucket, region, prefix, access_key_env(${ENV_VAR}), secret_key_env(${ENV_VAR}), use_ssl (V2, FR-057) |
| schedules | uuid, instance_id(FK), name, cron_expr, action, payload, enabled |
| schedule_execution_logs | schedule_id(FK), action, status, error, started_at, finished_at |
| alert_rules | uuid, name, trigger_type(V2: metric/instance_crash/node_offline/log_keyword/player_event/backup_failed), level(V2: info/warn/critical), target_type, target_id, metric, operator, threshold, duration_sec, keyword(V2 日志关键字), event_match(V2 玩家事件子类型), channel_ids(V2 JSON 路由通道), dedup_window_sec(V2 去抖), silence_start/silence_end(V2 静默窗口 HH:MM), notify_recover(V2), notify_type, notify_target(FR-011 兼容), enabled |
| alert_events | rule_id, target_id, level(V2), trigger_type(V2), dedup_key(V2 去抖键), value, message, count(V2 聚合计数), resolved, fired_at, last_fired_at(V2), resolved_at, acknowledged/acknowledged_by/acknowledged_at(V2 确认), read(V2 站内已读) |
| alert_channels (V2) | uuid, name, type(webhook/email/dingtalk/wecom/feishu/discord/telegram/inapp), enabled, config(JSON, 凭证子字段 ${ENV_VAR} 引用, FR-085) |
| metric_series (V2) | node_uuid, instance_id, scope(node/instance/world), metric_key, world, unit, last_seen_at; UNIQUE(node_uuid,instance_id,scope,metric_key,world)（时序序列维度，FR-060/ADR-013） |
| metric_sample_raw (V2) | series_id(FK), ts, value(NULL=缺测)；留 ~48h |
| metric_rollup_5m (V2) | series_id(FK), bucket_ts, avg/min/max/last/count；留 ~30d |
| metric_rollup_1h (V2) | series_id(FK), bucket_ts, avg/min/max/last/count；留 ≥1y |
| templates | uuid, name, type, description, start_command, default_work_dir, download_url, config_files(JSON) |
| audit_logs | user_id, action, target_type, target_id, detail(JSON), ip |
| networks (V2) | uuid, name, description（非独占软标签） |
| network_members (V2) | network_id(FK), instance_id(FK)（M:N，一个子服可属多群组） |
| server_registrations (V2) | proxy_id(FK), backend_id(FK), alias, priority, forced_host, restricted, enabled；UNIQUE(proxy_id, alias) |
| node_jdks (V2) | node_id(FK), vendor, major_version, version, arch, path, managed(下载/登记) |
| instance_config_versions (V2) | instance_id(FK), file_path, content, author, created_at |
| file_versions (V2) | instance_id(FK), file_path, content_hash, content(base64,二进制安全), size, author_id, rollback_of_version_id, created_at；INDEX(instance_id,file_path)（FR-051 通用文件改前快照） |
| assets | type(core/plugin/image/video/archive/blob), name, version, filename, sha256(寻址+去重键), md5, size, content_type, source_url, metadata(JSON), storage_state(hot/archived/external), storage_backend, ref_count, rel_path(相对数据根), created_at, last_used_at；UNIQUE(type,sha256) |
| logs (FR-049) | source(instance/control_plane/worker), level(debug/info/warn/error), instance_id, instance_uuid, node_id, stream(stdout/stderr), message, time；复合索引 (source,time)/(level,time)/(instance_id,time)/(node_id,time)，关键字检索走 message 列谓词 |
| ban_records (V2) | uuid, player_name, reason, scope(network/instance/global), scope_id, operator_id(FK), active, created_at, unbanned_at（玩家封禁台账，FR-054；RCON 命令已下发后留档，解封置 active=false 保留历史） |
| platform_settings (V2) | key(PK), value, updated_at（平台配置 DB 覆盖层，仅存被显式覆盖的白名单键；生效优先级 DB 覆盖 > 环境变量 > YAML 默认，FR-063/ADR-015）。network 类键 `proxy.url`（敏感脱敏）/`proxy.no_proxy` 为 CP 全局出站代理（FR-185/ADR-043），保存即重建 CP 出站持有者并作为各节点默认代理（优先级 settings DB > control-plane.yaml > env） |
| self_update_check_caches (FR-186) | id(固定=1, 单行覆盖), result_json(上次成功 CheckResult 的 JSON blob, 整段存不拆字段以免随 CheckResult 演进迁移、反序列化缺字段降级), source(更新源标识冗余, 诊断用), checked_at（系统更新页检查结果服务端缓存；GET /self-update/check 返此缓存不触发 live、refresh 成功后 upsert 覆盖、刷新失败不清，进页即显 + 后台静默刷新，增强 FR-182） |
| tasks (V2, FR-183) | task_id(UNIQUE, UUID 业务键), node_id, kind(jdk_install/…), state(pending/running/succeeded/failed), progress(0~100), title, detail, error, result(成功结果 JSON), created_by(发起人/归属), created_at, updated_at（全局任务中心：长任务进度经心跳 upsert，终态触发副作用，ADR-040） |
| task_logs (V2, FR-183) | task_id, seq, line, ts；UNIQUE(task_id, seq)（任务滚动日志；心跳带绝对序号，幂等追加去重） |
| notifications (V2, FR-183) | user_id(收件人), level(info/success/warning/error), title, body, task_id(关联任务,可空), read_at(NULL=未读), created_at（站内信：任务完成/失败投递，归属隔离） |
| client_channels (FR-086) | channel_id(slug, UNIQUE, 对外标识/URL 段), name, description, current_version(latest 版本指针，0=未发布，FR-088 编排), created_at, updated_at, deleted_at（客户端分发频道，每服一个，ADR-022） |
| client_pull_keys (FR-086) | channel_id(所属频道 slug), name, key_hash(明文 SHA-256, UNIQUE, **不存明文**), key_prefix(识别用前缀), revoked, expires_at, last_used_at, created_at, revoked_at（拉取密钥，半公开凭据；明文仅创建/轮换时一次性返回，吊销即鉴权失效，ADR-022） |
| client_versions (FR-087/088) | channel_id(所属频道 slug), version(单调递增, UNIQUE(channel_id,version), 防降级基准), files_json(文件清单快照), managed_dirs_json(托管目录), agent_json(自更新段, 可空), note, created_by, created_at（版本快照，全保留供运营回滚/diff；manifest 即时组装+签名，回滚=以更高 version 重发旧内容，ADR-022） |
| client_machines (FR-092) | channel_id + machine_id(UNIQUE 组合), hit_count, first_seen, last_seen（客户端机器码登记，manifest 拉取时 best-effort upsert；机器码客户端生成**不可信**，仅统计+辅助限流，不作授权依据，ADR-023） |
| client_dist_events (FR-093) | channel_id, machine_id, ip, kind(manifest/artifact), version, artifact_sha, bytes, status, duration_ms, created_at（拉取/下载明细，**短保留**+滚动清理；按 IP/机器码/频道/版本/时间检索） |
| client_dist_daily (FR-093) | day + channel_id + version + kind(UNIQUE 组合), requests, bytes（按日聚合，**长保留**、写时增量 upsert；供下载量趋势+版本分布，FR-095） |
| client_ip_rules (FR-096) | cidr, mode(deny/allow), note, created_by, created_at（分发端点 IP 防护规则，运行时可改+入审计；deny 优先、有 allow 即白名单模式，ADR-023） |
| client_telemetry (FR-094) | channel_id, machine_id, ip, result, from_version, to_version, os, java_version, launcher, duration_ms, boot_success, error, created_at（客户端遥测明细，**短保留**+滚动清理；仅环境粗粒度+不可逆机器码，隐私可关） |
| client_telemetry_daily (FR-094) | day + channel_id + result(UNIQUE 组合), count（遥测按 result 日聚合，**长保留**；供更新成功率/回退率趋势，FR-095） |
| business_events (FR-116/122) | domain + dedup_key(UNIQUE 组合, 至少一次投递去重), action, node_uuid, instance_uuid, operator(FR-121 回填), payload_json(信封原文), occurred_at, created_at（JBIS 通用业务事件信封表，**插件无关汇聚底座**；探针经反向 WS 桥上报的业务域事件按 (domain,dedup_key) insert-or-ignore 落库，新增域无需改表，**不降采样不丢**，ADR-028） |
| economy_balance_mirrors (FR-122) | node_uuid + zone_id + player_name + currency(UNIQUE 组合, **node→zone 维度**), currency_id, balance(字符串 BigDecimal), last_seq(单调推进游标), last_ledger_id, last_entry_type, occurred_at, updated_at（经济结构化镜像最新余额；按 ledger 事件 seq 单调推进，跨区/跨节点同名玩家独立不串味/不重复计数；汇聚镜像非真源，ADR-028） |
| economy_ledger_entries (FR-122) | ledger_id(UNIQUE, 去重锚点), node_uuid, instance_uuid, zone_id, player_name, currency, currency_id, entry_type, signed_amount(字符串), balance_after(字符串), seq, occurred_at, created_at（经济变更/操作审计，结构化专表 append-only；与 business_events 并存供高效查询/对账，业务数据**不降采样不丢**，ADR-028） |

### 数据库切换

```yaml
database:
  driver: sqlite
  dsn: data/jianmanager.db
  # driver: mysql
  # dsn: "user:pass@tcp(127.0.0.1:3306)/jianmanager?charset=utf8mb4&parseTime=true"
```

## 8. 前端架构

### 8.1 技术栈

| 层面 | 选型 |
|---|---|
| 框架 | React 19 |
| 构建 | Vite 6 |
| 路由 | React Router 7（懒加载） |
| 服务端状态 | TanStack Query（SWR + 缓存 + WS 驱动失效） |
| 客户端状态 | Zustand（auth / theme / sidebar） |
| UI 组件 | shadcn/ui + Radix |
| 样式 | TailwindCSS 4 |
| 终端 | xterm.js |
| 图表 | Recharts |
| 编辑器 | CodeMirror 6 |
| 国际化 | i18next |

### 8.2 全局布局（运维控制台 Shell）

登录后默认进入「运维控制台」Shell（`DashboardPage`，见 ADR-009 / FR-037 / FR-061）：左侧为常驻**高密度多级侧栏**（分组可展开），右侧为工作区。下图为布局示意（左栏现已整合为分组多级侧栏）：

```
┌────────────────┬─────────────────────────────────────────┐
│  JianManager ◧ │ 域›面包屑          [🔎 搜索 ⌘K] 徽标 ✉ 🔔 账户│  ← 全局顶栏（FR-162 / 右对齐 FR-179）
│ ┌────────────┐ ├─────────────────────────────────────────┤
│ │ 总览        │ │                                         │
│ │ ▾ 集群      │ │   工作区                                 │
│ │  节点 实例  │ │   · 点实例 → 该实例终端（单个，xterm）    │
│ │  [全部节点▼]│ │   · 其余导航 → 按路由渲染对应页面          │
│ │  ● Survival│ │   · 未开终端 → 空状态                     │
│ │ ▾ 监控      │ │                                         │
│ │ ▾ 运营      │ │   （侧栏可折叠为仅图标轨 w-14）           │
│ │ ▾ 系统      │ │                                         │
│ │  ·平台与维护│ │                                         │
│ │  ·账户与审计│ │                                         │
│ ├────────────┤ │                                         │
│ │ ●● ☀  主题  │ │  ← 全局主题切换（靛蓝/青绿 + 明暗，FR-164）│
│ │ vX.Y · 许可 │ │                                         │
│ └────────────┘ │                                         │
└────────────────┴─────────────────────────────────────────┘
```

- **左栏（常驻）= 五域多级侧栏（FR-131 / design §7，`ConsoleSidebar`）**：从原 11 个粒度不一的一级精简为 **5 个按运维心智分域的一级**，分组可展开、激活态主色高亮，高频域在上、低频「系统」沉底。
  - 五域：**总览**（单链接）/ **集群** /（组）**监控** /（组）**运营** /（组）**系统**（内分「平台与维护」「账户与审计」两小节）。
  - **集群**组展开 = 节点 + 全部实例 + 群组 + 节点切换器（`全部节点` + 各节点，`GET /nodes`）+ 常驻实例树（`GET /instances?nodeId=`；每项状态点：RUNNING 绿 / STARTING·STOPPING 琥珀 / CRASHED 红 / STOPPED 空心 + bot 聚合徽标）。
  - **监控**组 = 监控总览（`/monitor`，FR-169）+ 告警 + 日志；**运营**组 = 玩家 + Bot + 模板 + 备份 + 备份存储 + 定时任务；**系统**组 =「平台与维护」（运行时与制品 + 客户端分发 + 平台存储 +〔平台管理员〕数据库 + 系统更新）+「账户与审计」（用户 + 用户组 + 设置 + 审计 + 开源许可）。
  - **可折叠图标轨（FR-131）**：可折叠为仅域级图标轨（`w-14`，hover tooltip 显 label，点分组图标即展开侧栏再选子项）；导航区滚动条隐藏但保留滚动（`.scrollbar-none`）。折叠态 / 分组折叠态 / 选中节点持久化 `localStorage`（`stores/console.ts`：`sidebar.collapsed` / `sidebar.collapsedGroups` / `sidebar.selectedNodeId`）。
  - **顶部 logo 点击折叠/展开（FR-181，增强 FR-131）**：侧栏顶部 logo（`Boxes` 图标 + `JianManager` 文字）整体为一个 `<button>`，点击复用 `console.toggleSidebar` 收缩/展开；折叠态仅图标仍可点回展开。`aria-label` 描述「将发生的动作」（展开态=收起 / 折叠态=展开，纯函数 `sidebar-logo.ts:logoToggleLabelKey`）。原 `PanelLeftClose`（展开态）与 `PanelLeftOpen`（折叠态导航区顶部）按钮保留为冗余显式控件，三者均调同一 action。
  - 底部（FR-164/FR-132）：**全局主题切换器** `ThemeSwitcher`——主题色圆点（靛蓝/青绿直选）+ 明暗（lucide 图标 + dropdown 三态直选）；版本号（左下）+ 开源许可入口（右下 → `/licenses`，FR-135）；退出登录已迁至顶栏账户菜单（FR-162）。切语言同步 `<html lang>` 见 `i18n`。
- **顶栏（FR-162，重排 FR-179，`ConsoleHeader`）= 内容区上方全局页眉**（侧栏保持全高，顶栏只占右侧内容列；简约扁平、层次/间距精修）：
  - **左** = 统一面包屑（FR-134，`PageBreadcrumb` + 纯函数 `lib/breadcrumb.ts`）：按路由渲染「域 › 页面」轨迹（与五域 IA 对齐），父级可点跳转、末级加粗；打开实例工作区时末级补实例名（域 › 全部实例 › <名称>）。面包屑容器 `flex-1 min-w-0` 占据剩余宽度并可截断，把右侧操作区推到右缘（窄屏防翻屏）。
  - **右（操作区，靠右对齐，FR-179）** = 常驻搜索框 + 集群概览徽标 + 站内信收件箱挂载点（FR-183 预留）+ 告警铃铛 + 账户菜单。槽位顺序 / 响应式可见性逻辑下沉纯函数 `components/console/header-layout.ts`（`HEADER_RIGHT_SLOTS`/`slotVisibility`/`searchBoxClass`，vitest 覆盖）：
    - **搜索框**（本期占位：UI + `Ctrl/⌘+K` 聚焦，输入暂不联动检索，检索逻辑留后续 FR）由 FR-162 的居中铺中部改为**靠右固定上限宽度**（`w-44 lg:w-56 xl:w-64`）紧贴操作图标，窄屏（<md）隐藏。
    - **集群概览徽标**（在线节点/运行实例/崩溃数，复用 `GET /metrics/overview` + 实例列表本地统计；点击跳转对应筛选：运行/崩溃→`/instances?status=`、在线→`/nodes`）窄屏（<lg）隐藏。
    - **站内信收件箱挂载点**（FR-183）：位于集群徽标与告警铃铛之间、紧邻铃铛之前，**本期仅占位**（`InboxSlot` 渲染 `null` + `TODO(FR-183)`），不实现站内信逻辑。
    - **告警铃铛**（`GET /alerts/events/unread-count` 未读计数 30s 轮询 + 下拉只读最近事件）+ **账户菜单**（用户名/角色 + 退出登录）始终显示（窄屏不隐，确保核心能力常驻）。
- **右 = 工作区（可组合卡片画布，FR-166 / ADR「可组合卡片工作区」取代 ADR-030）**：
  - 点实例 → 工作区打开该实例的**可拖拽卡片画布**（`components/console/WorkspaceCanvas`，基于 `react-grid-layout`）：**卡片 = 实例 × 功能**，自由摆放 / 调大小 / 流式不重叠；**同时仅一个实例**，点另一个切换。原固定六 Tab 已**取代为画布 + 快捷预设**——卡片类型 = 终端 / 资源（文件+配置合一，承 FR-130）/ 插件 / 监控 / 服务器状态 / 业务·经济·背包（JBIS）/ Bot，逐种复用既有面板组件（卡内容分发 `WorkspaceCardBody`），**画布化后全部既有工作区能力均作为卡片可达，无功能退化**。**监控**卡 = 该实例 FR-060 历史曲线（TPS/MSPT/堆/在线/线程/CPU + 分世界区块）。
  - **统一卡壳** `WorkspaceCard`：grip 拖拽手柄（`draggableHandle=".workspace-card-grip"`，仅按住卡头 grip 才移动，卡内终端/编辑器交互不被吞）+ 实例·功能标签 + 全屏（临时最大化单卡）+ 关闭。卡 resize / 全屏切换后派发 `window` resize，触发终端 `fit` 与编辑器 relayout。
  - **惰性挂载**（承 ADR「未挂载卡不建 WS」）：仅渲染当前画布上的卡片，故终端 WS / metrics 轮询只对画布上的卡建立；未加入画布的功能不预渲染。
  - **预设（个人级 localStorage）**：命名保存画布布局（纯函数 `lib/workspace-preset.ts` 序列化/校验/规整 + `lib/workspace-card.ts` 卡片类型目录，vitest 覆盖）。内置「快捷预设」= **运维台**（默认：大终端 + 状态 + 资源）/ 纯终端 / 资源；用户可「另存为」自定义预设、删除。画布/卡片/预设运行态由 `stores/workspace.ts`（Zustand，按实例 id 记忆，各卡自管 dirty）承载，**不进 URL**（与 `console.ts` 的侧栏/选中态分离）。`/instances/:id` 深链回退页 `InstanceDetailPage` 挂载即 `openInstance` 进同一画布。
  - **文件**段 = 共享资源管理器 `components/explorer/ResourceExplorer`（FR-070）：左懒加载目录树（`FileTree`）+ 右目录内容（`FileList` 多选/右键/拖拽源）/ CodeMirror 编辑器（`editor/CodeEditor`，多格式高亮 + Ctrl+S 拦截保存接 FR-051 历史）。交互全集（新建文件夹/重命名/删除/剪切复制粘贴/树内拖拽移动/拖拽上传/单文件流式与多选 zip 批量下载/shift·ctrl·全选多选）抽为纯函数（`selection`/`clipboard`/`paths`/`language`，vitest 覆盖）；删除/回滚走 `DangerConfirm`（FR-059），历史版本经右侧抽屉 `VersionDrawer`。`ResourceExplorer` 接受可选 `config` 能力注入（编辑器插槽 / 左栏插槽 / 配置版本抽屉），不注入即为纯文件资源管理器。**此组件为 FR-071/073/074/075/082/083/084 复用地基**。归档浏览/反编译（FR-075）叠加为右栏互斥面板：`FileList` 双击/右键按 jar/zip→`ArchiveViewer`（内部条目子树 + 点文本条目只读查看 + 点 `.class` 触发反编译）、`.class`→`DecompileViewer`（只读 Java 源码），与文本编辑器三者互斥占用右栏；API client `api/archive.ts`，只读端点不触碰写操作。
  - **配置**段 = `components/config-explorer/ConfigExplorer`（FR-071）：**复用 `ResourceExplorer`** 并注入配置能力——打开文件改用 `ConfigFileEditor`（schema 表单/文本双模式 + 跨文件校验 + Ctrl+S 存**配置版本**，FR-031；文本模式复用共享 `CodeEditor` 多格式高亮）；左栏顶部 `FavoritesBar`（收藏书签存 `localStorage`，纯函数 `favorites.ts` + 已发现配置面板 `GET /configs/discover` 递归全部配置，分组纯函数 `discover.ts`）；历史经 `ConfigVersionDrawer`（FR-031 配置版本/diff/回滚）。树/列表本身呈现工作目录全部文件，满足「目录树呈现自动发现的全部配置」。原独立三栏 `ConfigEditor` 已移除。
  - 其余路由在工作区按路由渲染。**总览页（`OverviewPage`）** = 环形仪表盘 + 跨节点聚合历史曲线（FR-060：总 CPU/内存/在线玩家）+ 密集实例表；**节点页（`NodesPage`，FR-177 主从双栏重做，取代原卡片网格/列表 + 手搓 `fixed inset-0` 模态）** = 左**可收缩节点列表**（窄图标轨 ⇄ 展开，收缩态 `localStorage` 持久；顶集群汇总头复用 `summarizeNodes` + 搜索 + `AddNodeDialog`；行 = 状态点呼吸灯/名/host/mini 水位/实例数，选中高亮、离线置灰）+ 右**选中节点实时详情**（身份块 + 操作 kebab[维护/排空/下线，走 `DangerConfirm`] + `ResourceGauge` CPU/内存/磁盘/负载 + **分段 Tabs**：概览 `NodeOverviewSection` / 实例 `NodeInstanceCompare` / JDK `NodeJDKPanel` / 制品缓存 `NodeArtifactCachePanel`（FR-178 组件改挂分段，抽屉入口下线）/ 端口 `NodePortsPanel` / 监控历史曲线 / 坏节点修复 `NodeRepairPanel`[BUG-A：诊断 + 重 enroll + 清孤儿，接 `/nodes/repair/*`·`/nodes/:id/reenroll|orphans|purge-orphans`，破坏性走 `DangerConfirm`]）；未选节点右栏空态。列表筛选/选中态/收缩态持久抽纯函数 `lib/node-list.ts`（vitest 覆盖）。分段切换稳定工具条、布局不重组（FR-178 §5 抽屉 UX 约束）。**开源许可页（`LicensesPage`，`/licenses`，FR-135）** = 构建期 `scripts/gen-licenses.mjs` 扫描 web + bot-worker(npm) + Go(go-licenses) 生成 `web/public/licenses.json`（静态资源、非 `/api`），页面提供包名搜索 + 运行时/开发分区计数 + 表格 [包名·版本·许可证·作者] + 行内展开许可证全文。
  - **跨实例超级工作台（FR-167，`/super`，集群域独立入口，复用 ADR-034）**：把可组合画布的作用域从「限当前实例」扩展为**跨实例**——同一画布并存任意实例的卡（如 4 个不同实例终端拼监看墙）。两作用域在 `stores/workspace.ts` 清晰并存：单实例画布 `canvasByInstance[id]`（卡省略 instanceId，按实例 id 记忆）与超级工作台 `superCanvas`（**卡显式携带 `instanceId`**）。页面 `components/console/SuperWorkbenchPage` = 左侧可收起**实例库** `InstanceLibrary`（搜索实例 + 实例展开看 6+ 功能；**HTML5 原生 DnD 拖拽源**：拖实例=加该实例默认卡组、拖功能=加单卡、多选批量拖=一次拼监看墙；放置区 dragover 高亮 + 松手落位）+ 右侧跨实例画布（复用同一 `WorkspaceCard` 卡壳与网格、**惰性挂载**未上画布的卡不建 WS）。卡片所属实例名由 `WorkspaceCard` 按 `instanceId` 自解析（每卡可属不同实例）。**跨实例预设**与单实例共享同一份 `userPresets` localStorage（`lib/workspace-preset` 序列化扩为携 `instanceId`，**向后兼容**无 instanceId 的旧预设）。拖拽载荷的序列化/解析与「载荷→卡片」「跨实例卡去重（同实例同功能去重，多实例同功能并存）」抽为纯函数 `lib/instance-library.ts`（vitest 覆盖）。
  - **工作区导播台（FR-168，`/director`，集群域独立入口 / 超级工作台工具栏「导播台」按钮进，ADR-035）**：在多个**场景**（= FR-167 跨实例预设）间像 OBS **瞬切零延迟** + 缩略图条 + 定时轮播。页面 `components/console/DirectorConsolePage`：① **场景缩略图条** `DirectorSceneStrip`（一排场景，点击 / 数字键 1-9 / ←→ 瞬切；三态指示——active 主色脉动 / 预热绿点 / cold 灰点；右侧并发上限滑杆）；② **舞台**把所有**预热场景的画布同时挂载**（`DirectorCanvas`，只读网格复用 `WorkspaceCard` 卡壳），仅 active 可见。**核心 = ADR-035 预热并发模型**：要瞬切零延迟，目标场景的卡 WS 必须**已保活**；但多场景同时全速渲染会过载浏览器（WS 同域 ~6 连接 + 多 xterm/图表重绘吃满 CPU），故——**场景三态状态机**（纯逻辑 `lib/director.ts`，vitest 覆盖 LRU 驱逐 / 状态转移 / 轮播序列）：激活唯一 + **预热是受并发上限约束的集合**（默认保守 3，可配 1~6），新预热超限按 **LRU 驱逐**最久未激活的预热场景（降 cold，下次切换重连）；**非激活降频 / 暂停渲染**——非激活场景的 `DirectorCanvas` 用 `content-visibility:hidden`（浏览器跳过整棵子树布局/绘制）+ 终端经 `lib/director-render.ts` 的 `DirectorRenderProvider active=false` 让 xterm **暂停 render 但 WS 继续收数据进缓冲**（`Terminal.tsx` 加 paused 模式累积输出），切回一次性 flush。**cold 场景不挂载**（不建 WS）。导播台运行态（场景定义 + 状态机 + 轮播）由 `stores/director.ts`（Zustand，场景/上限/轮播间隔 localStorage 持久）承载，**纯前端**——只管理既有终端/监控 WS 的保活与渲染节流，不新增协议、不逾越进程边界（守架构不变量）。**真机多连接压测为硬验收维度**（单元只覆盖状态机逻辑）。
- **设计系统（FR-061 + FR-163 视觉底座）**：OKLCH token 驱动；主色为**靛蓝 `#6366F1`**（FR-163，替换原 MC 绿）+ 状态色系（success/warning/danger/info，阈值驱动变色，见 `lib/threshold.ts`）+ 13px 密度档位。**设计底座 token（FR-163，`index.css`）**：柔和弱阴影 `shadow-soft` / 主色晕染抬升 `shadow-lift`（hover）/ iOS 缓动 `ease-ios` / 呼吸灯 `animate-breathing`（运行对象脉动光环）/ 大圆角基线（`--radius` 0.75rem，卡片 `rounded-xl`）。**交互细节（FR-176 增强 FR-163）**：卡片/行/chip 原语（`Panel`/`*WorktableCard`/`SummaryChips`/`ConfigRow`/背包格子）hover **只换阴影不位移**（去 `hover:-translate-y`，留 `hover:shadow-lift`，过渡收为 `transition-[box-shadow]`，避免布局抖动）；输入焦点环收敛为 `ring-2 + ring-ring/40`（原 `ring-[3px] + ring-ring/50`，配 `border-ring` 边线，焦点态不糊邻近文字）；全局**主题化细滚动条**（`::-webkit-scrollbar` + Firefox `scrollbar-width: thin`/`scrollbar-color`，配色取中性主题变量 `--muted-foreground` 经 `color-mix`，随明暗 + 双主题自适配；保留 `.scrollbar-none` opt-out）。**统一卡片原语**：`Panel`（分区/容器，新增可选 `icon`/`tone`/`hoverable`）+ `StatCard`（KPI 卡，「按指标混搭」逻辑下沉纯函数 `lib/stat-card.ts`/`lib/tone.ts`）+ `ResourceGauge`/`MiniBar`/`StatusBadge`（`components/ui`）与 `TimeSeriesChart`/`RangePicker`（`components/charts`）；**弃 shadcn `Card` 松散用法**（`card.tsx` 标 `@deprecated`，eslint `no-restricted-imports` 阻断新引入，见 ADR-032）。**全局双主题（FR-163 底座 + FR-164 落地）**：组件层零硬编码品牌色，品牌色全经 CSS 变量（`--primary`/`--primary-foreground`/`--accent`/`--accent-foreground`/`--ring`/`--brand-shadow`/`--chart-1`）。第二主题**青绿 `#14B8A6`** 仅在 `index.css` 用 `[data-theme="teal"]` 与 `[data-theme="teal"].dark` 覆盖这组品牌变量（结构色/状态色不动），靛蓝为默认（无 `data-theme` 即承 `:root`/`.dark`）。**主题色（`colorTheme: indigo|teal`）与明暗（`light|dark|system`）正交、各自 `localStorage` 持久**；纯逻辑下沉 `lib/theme.ts`（`resolveColorTheme`/`colorThemeAttr`/`resolveMode`/`nextMode` + 套用 helper），`stores/theme.ts` 统管两轴。**主题/明暗初始化提到 app 入口**（`main.tsx` 在 React 挂载前 `initThemeFromStorage()` 套 `<html data-theme>` + `.dark`），登录/初始化页也套主题且首屏无闪。一处切（侧栏底部 `ThemeSwitcher`）全站 CSS 变量实时跟变（按钮/曲线/选中态/进度条随主色）。仍基于 shadcn/ui + Tailwind v4 + OKLCH，不引入新框架。
- 暗色/亮色主题与 i18n（zh/en）正常；选中实例/节点为客户端 UI 状态，不进 URL。
- **响应式基线（FR-163）**：栅格断点沿用 Tailwind `sm/md/lg`（如总览 KPI `grid-cols-2 sm:grid-cols-3 lg:grid-cols-6`），卡片原语 `Panel`/`StatCard` 流式宽度自适应、不破栅格。

### 8.3 页面结构

#### 首次启动引导 `/setup`

独立于 Dashboard 布局的全屏页面，无需认证。首次启动时（数据库中无管理员账号）自动跳转。

```
┌──────────────────────────────────────────────────────┐
│                                                      │
│            ┌──────────────────────────┐              │
│            │  🎮 JianManager          │              │
│            │  欢迎使用，请设置管理员账号  │              │
│            │                          │              │
│            │  用户名: [______________] │              │
│            │  密  码: [______________] │              │
│            │  确  认: [______________] │              │
│            │                          │              │
│            │  [    开始使用    ]       │              │
│            └──────────────────────────┘              │
│                                                      │
└──────────────────────────────────────────────────────┘
```

**API**: `GET /setup/status` → `POST /setup` → 自动登录跳转 `/`

#### 总览仪表盘 `/`

```
┌──────────────────────────────────────────────────────┐
│  概览卡片行                                            │
│  ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐        │
│  │ 节点    │ │ 实例   │ │ 运行中  │ │ Bot    │        │
│  │ 3 在线  │ │ 12 总计 │ │ 9 运行  │ │ 45 连接│        │
│  └────────┘ └────────┘ └────────┘ └────────┘        │
│                                                      │
│  ┌──────────────────────┐ ┌──────────────────────┐  │
│  │ 最近告警              │ │ 最近操作日志          │  │
│  │ • CPU > 90% @node-01  │ │ • admin 启动 sv-01   │  │
│  │ • 内存 > 85% @node-02 │ │ • admin 备份 sv-03   │  │
│  └──────────────────────┘ └──────────────────────┘  │
│                                                      │
│  节点资源概览                                          │
│  ┌────────────────────────────────────────────────┐  │
│  │ node-01  CPU ████░░ 65%  MEM ██████░ 78%      │  │
│  │ node-02  CPU ██░░░░ 30%  MEM ████░░░ 50%      │  │
│  └────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

**数据来源**: `GET /nodes`, `GET /instances`, `GET /alerts/events`, `GET /audit`

#### 节点列表 `/nodes`

```
┌──────────────────────────────────────────────────────┐
│  节点管理                    [筛选: 全部/在线/离线]     │
│                                                      │
│  ┌─────────────────────────────────────────────────┐ │
│  │ 名称       │ IP           │ 状态  │ CPU  │ 内存  │ │
│  │ node-01   │ 10.0.0.1     │ 🟢在线 │ 65%  │ 78%  │ │
│  │ node-02   │ 10.0.0.2     │ 🟢在线 │ 30%  │ 50%  │ │
│  │ node-03   │ 10.0.0.3     │ 🔴离线 │ --   │ --   │ │
│  └─────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

**点击节点** → 节点详情页（指标图表、实例列表、资源使用）

#### 实例列表 `/instances`

```
┌──────────────────────────────────────────────────────┐
│  实例管理                    [+ 创建实例]              │
│                                                      │
│  [筛选: 节点▼] [类型▼] [状态▼] [搜索...]              │
│                                                      │
│  ┌─────────────────────────────────────────────────┐ │
│  │ 名称          │ 节点    │ 类型    │ 状态   │ 操作│ │
│  │ Survival     │ node-01 │ MC Java │ 🟢运行 │ ▶⏸⟳🗑│ │
│  │ Creative     │ node-01 │ MC Java │ ⏹停止 │ ▶⏸⟳🗑│ │
│  │ Proxy        │ node-02 │ 通用    │ 🟢运行 │ ▶⏸⟳🗑│ │
│  └─────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

**操作按钮**: 启动(▶) / 停止(⏸) / 重启(⟳) / 强制终止(🗑) / 一键复制(⧉，仅 backend，V2)
**点击实例名** → 在运维控制台工作区打开该实例的**可组合卡片画布**（见 §8.2「右=工作区」，FR-166；画布工具栏含启动/停止/重启/强制终止 + 快捷预设 + 添加卡片 + 另存预设）；`/instances/:id` 作为直链兜底保留，`InstanceDetailPage` 挂载即 `openInstance` 进同一画布。
**组织分组视图**（V2，FR-165）: 筛选栏「组织分组」开关切到「左分组树 + 右列表」专用形态（design §4.4）——左树多级嵌套（新建/嵌套子组/折叠优先/选中，节点挂子树聚合去重计数），右列表复用工作台卡 + 组路径面包屑 + 批量「标记入组」，支持把实例拖入左树某组（HTML5 原生 DnD）。与既有多维筛选 + `groupBy` 维度分组**并列正交**，互不破坏。分组树正交于用户组（RBAC）与网络群组（部署），仅 CP 读写（`/instance-groups`，ADR-033）。

#### 实例工作区（可组合卡片画布，取代固定 Tab）

实例工作区已从「固定六 Tab（一次看一个）」升级为**可拖拽卡片画布**（FR-166，取代 ADR-030 的固定分屏方向）：

```
┌──────────────────────────────────────────────────────┐
│ [▣]Survival Server 🟢RUNNING [代理]  [停止][重启][强杀]│  ← 实例工作区页眉（FR-180）
│    PaperMC · node-a:25565        [运维台▾][+加卡][💾][✕]│
│  ┌──────────────────────────┐ ┌────────────────────┐  │
│  │ ⠿ 终端  Survival          │ │ ⠿ 服务器状态        │  │
│  │  (xterm 直连 Worker WS)   │ │  (在线/世界/运行态) │  │
│  │                          │ ├────────────────────┤  │
│  │                          │ │ ⠿ 资源（文件+配置） │  │
│  └──────────────────────────┘ └────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

**实例工作区页眉**（FR-180，`WorkspaceToolbar` + 纯函数 `console/workspace-header.ts`，重设计 FR-166 工具栏、增强 FR-069）：左=**身份块**（角色色块图标[proxy 路由 / backend 方块 / universal 多方块，运行态主色块] + 实例名 + 状态徽标[过渡态脉冲] + 角色徽标[proxy 主色 / backend info 次色 / universal 隐藏] + `类型·节点:端口` 元信息行）；中=**关键操作**（生命周期按钮分组明示：运行→停止/重启/强制终止，否则→启动，图标 + 文案高可发现）；右=画布控件（快捷预设 ▾ / 添加卡片 / 另存预设 / 关闭）。与全局顶栏（FR-162/179）同色系同圆角（`bg-card/40` + 语义 `status-*`/`accent`/`primary` 令牌，明暗 + 双主题随 CSS 变量切换）；节点名由画布经 `useNodes` 解析后传入，避免页眉自查节点表。原「控制台 / 实例名」面包屑前缀移除（全局顶栏已呈现 域 › 全部实例 › 名称 完整轨迹，页眉聚焦身份 + 操作不重复）。

- **卡片类型**（各复用既有面板，惰性挂载，未上画布不建 WS）：
  - **终端** — 可交互终端（读写 xterm.js，直连 Worker Node WS，`TerminalPane`）
  - **资源** — 文件 + 配置**合一**（`ConfigExplorer` = `ResourceExplorer` + 配置能力，承 FR-130）：文件树 + CodeMirror 编辑器 + 配置 schema 双模式/校验/版本 + 收藏
  - **插件** — 插件安装与管理（`PluginManager`）
  - **监控** — FR-060 历史曲线 + 实时指标（`MetricsSegment`）
  - **服务器状态** — 在线玩家 / 世界 / 运行态（`ServerStateSegment`）
  - **业务 / 经济 / 背包（JBIS）** — `BusinessSegment` / `EconomySegment` / `InventorySegment`
  - **Bot** — 该实例关联的 Bot（`BotSegment`）
- **快捷预设**（原 Tab 降级而来，个人级 localStorage）：内置「运维台」（默认 = 大终端 + 状态 + 资源）/ 纯终端 / 资源；可「另存为」自定义预设。
- **备份** 仍可经实例列表/详情操作入口与既有 `useBackups` API 使用（不再占工作区固定 Tab）。

#### 创建实例（对话框）

```
┌──────────────────────────────────────────┐
│  创建实例（向导）                         │
│                                          │
│  角色: (●)Bukkit子服  ( )代理  ( )通用    │
│  名称: [survival1                ]       │
│  节点: [node-01              ▼]          │
│  核心: [Paper 1.20.4         ▼] 自动下载  │
│  JDK : [Temurin 17           ▼] 缺则安装  │
│  内存: [2G]   JVM: [Aikar flags  ▼]      │
│  工作目录: 系统自动分配（只读展示）        │
│  ☑ 崩溃自动重启   ☐ 跟随节点自启          │
│  注册到代理: [☑ proxyA   ☐ proxyB]        │
│  用户组(权限): [Team A ▼]  群组: [生存大区▼]│
│                                          │
│       [取消]              [创建]         │
└──────────────────────────────────────────┘

> 工作目录与端口由系统分配（不再由用户输入，见 §13.2）；MC 子服用结构化启动（绑定 JDK + 内存 + JVM 参数 + core jar），不再手填启动命令；代理/通用角色字段相应不同。
```

#### Bot 管理 `/bots`（全局总览，FR-040 / ADR-009：聚合优先、永不全量铺开）

跨实例总览与管理页（导航位于「实例」与「告警」之间）。页顶概览卡片走 `GET /bots/summary`（无 groupBy），分组总览走 `GET /bots/summary?groupBy=`（实例/节点/状态/行为），逐条 Bot 只在展开某组时分页窥视（`GET /bots`）。批量经 `POST /bots/batch` 按筛选委托。上万 Bot 不逐行渲染。

```
┌──────────────────────────────────────────────────────────────┐
│  Bot 管理                              [压测(占位)] [+ 新建 Bot] │
│  ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐                  │
│  │ 总计   │ │ 在线   │ │ 连接中 │ │ 异常   │                  │
│  │ 1280   │ │ 940    │ │ 120    │ │ 30     │                  │
│  │3实例·2节点                                                  │
│  └────────┘ └────────┘ └────────┘ └────────┘                  │
│  [🔍 搜索名称] [节点▾] [状态▾]        分组: [实例]节点 状态 行为 │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │ ☐ │ 分组(实例/节点/…) │ 健康条          │ 总数 │ 操作      │ │
│  │ ☐ │ ▸ Survival(node-a)│ ██████░░░ 在线6/8│  8   │在控制台打开 批量▾│
│  │ ☐ │ ▾ Creative(node-b)│ ███░░░░░░       │ 320  │在控制台打开 批量▾│
│  │   │   └ 展开窥视：分页拉该组首页 Bot（peek 10/页，只读）    │ │
│  └──────────────────────────────────────────────────────────┘ │
│  （勾选 ≥1 组 → 顶部批量条：设行为 / 停止 / 删除，逐组聚合计数） │
└──────────────────────────────────────────────────────────────┘
```

> 健康条仅「在线 vs 其余」两段（摘要分组只给 `online`=connected + `total`）。「在控制台打开」(仅实例分组) → `console store.openInstance(id)` + 跳 `/`，回到控制台工作区。单 Bot 实时遥测/详情面板见 FR-041，控制台内 per-instance Bot 段见 FR-039，压测会话编排见 FR-042（本页仅占位入口）。

#### 用户管理 `/users`

```
┌──────────────────────────────────────────────────────┐
│  用户管理                     [+ 创建用户]            │
│                                                      │
│  ┌─────────────────────────────────────────────────┐ │
│  │ 用户名    │ 角色       │ 所属组   │ 状态  │ 操作 │ │
│  │ admin    │ 平台管理员 │ --      │ 🟢启用│ ✏️🗑 │ │
│  │ alice    │ 组管理员   │ Team A  │ 🟢启用│ ✏️🗑 │ │
│  │ bob      │ 组成员     │ Team A  │ 🟢启用│ ✏️🗑 │ │
│  └─────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

#### 用户组管理 `/groups`

```
┌──────────────────────────────────────────────────────┐
│  用户组管理                   [+ 创建组]              │
│                                                      │
│  组: Team A [编辑] [删除]                             │
│  成员: alice (管理员), bob (成员) [+ 添加成员]         │
│  配额: 实例 3/10 | Bot 15/50 | 存储 2.1G/10G         │
│  分配实例: Survival, Creative [分配实例]               │
│                                                      │
│  ─────────────────────────────────────               │
│                                                      │
│  组: Team B [编辑] [删除]                             │
│  ...                                                 │
└──────────────────────────────────────────────────────┘
```

#### 定时任务 `/schedules`

```
┌──────────────────────────────────────────────────────┐
│  定时任务                     [+ 创建任务]            │
│                                                      │
│  ┌─────────────────────────────────────────────────┐ │
│  │ 名称         │ 实例      │ Cron     │ 操作   │启用│ │
│  │ 每日重启     │ Survival  │ 0 4 * * *│ restart│ ☑ │ │
│  │ 每周备份     │ *         │ 0 3 * * 0│ backup │ ☑ │ │
│  └─────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

#### 其他页面

- **备份 `/backups`**: 按实例分组的备份列表，支持创建/恢复/删除
- **模板 `/templates`**: 服务端模板列表，平台管理员可管理
- **审计日志 `/audit`**: 操作日志表格，按用户/操作/时间筛选
- **设置 `/settings`**: 系统设置（仅平台管理员）
- **系统更新 `/system-update`** (V2，FR-081/FR-175/FR-182/ADR-036 §7/ADR-042): 侧栏「设置」组，仅平台管理员可见。更新源默认读 **GitHub Releases**（`update.github_repo`=`wcpe/JianManager` + `channel`：`stable`=`/releases/latest`、`prerelease`=滚动 `latest` tag，feed 为可选回退）。检查更新（CP 自身 + 各节点版本对比，`source` 标更新源，notes 独立说明块）、CP 自更新、单节点升级、全网逐节点编排（rollout 运行中短轮询进度）；**升级前自动备份当前二进制**，CP 与各节点可一键**回滚 v{backupVersion}**到上一版（无备份禁用，FR-182）。升级/回滚均危险操作走统一 `DangerConfirm`（scope=platform）二次确认
- **群组服 `/networks`** (V2): 拓扑视图（代理 + 已注册后端，含各子服在线人数）；管理 proxy↔backend 注册（别名/优先级/forced-host）；群组软标签筛选与批量启停；「搭建子服 / 搭建代理」向导入口
- **玩家管理 `/players`** (V2): 在线玩家（探针事件实时聚合，标注所在子服，BC 跨服感知，FR-066）/封禁记录/白名单三视图；踢出/封禁二次确认 + 原因输入，解封（经探针插件桥 `SendPluginCommand` 执行，FR-067）；探针未连入降级提示。**「实时事件」标签**经 SSE 驱动在线名册 + 事件流
- **运行时/JDK** (V2): 在节点详情页 `/nodes/:id` 增「JDK」标签——列出已装 JDK、安装指定版本、登记系统已有 JDK、查看被哪些实例占用
- **配置编辑器** (V2): 位于工作区**资源卡**（文件+配置合一，FR-130/FR-166）——复用资源管理器（`ConfigExplorer`，FR-071）呈现工作目录全部配置（递归自动发现）+ schema 表单/原始双模式 + 一致性校验 + 配置版本 diff/回滚 + 收藏书签（非独立页面）

### 8.4 核心用户流程

#### 流程 1: 管理员首次使用

```
登录 → 看到空仪表盘 → 添加节点（输入节点地址）
→ 节点上线 → 创建实例（选择节点 + 配置）
→ 启动实例 → 进入终端 → 游戏服运行
```

#### 流程 2: 日常运维

```
登录 → 仪表盘看到实例状态 → 点击实例（进可组合卡片画布，默认运维台布局）
→ 在终端卡查看日志 → 发送命令
→ 如需修改文件 → 在资源卡（文件+配置合一）编辑 → 保存（或「+ 添加卡片」加一张资源卡并排）
→ 如需重启 → 点击工具栏重启按钮
```

#### 流程 3: Bot 压测

```
Bot 页面 → 创建压测会话 → 选择目标实例 + bot 数量
→ 开始压测 → 观察 bot 陆续上线
→ 查看 bot 状态（位置/血量/行为）
→ 结束压测 → bot 批量下线
```

#### 流程 4: 用户组管理

```
创建用户组 → 设置配额 → 添加成员
→ 分配实例给组 → 成员登录后只能看到分配的实例
```

#### 流程 5: 开一个 MC 群组服（V2）

```
搭建代理(Velocity，自动生成 secret) → 搭建 lobby 子服(系统配端口/转发/JDK)
→ 一键复制 lobby 为 survival1（系统改端口/名称）→ 勾选注册进代理
→ 资源卡调 server.properties/paper → 启动整个群组 → 玩家经代理进服
```

### 8.5 前端嵌入

前端通过 `go:embed all:dist` 嵌入 Control Plane 二进制。开发模式下 Gin 反代到 Vite dev server。

### 8.6 目录结构

```
web/src/
  api/          # Axios client + per-module API (TanStack Query hooks)
  ws/           # WebSocket client, provider, hooks
  stores/       # Zustand (auth, theme, console[选中实例/节点])
  pages/        # 页面（懒加载）；DashboardPage = 运维控制台 Shell；V2 新增 NetworksPage(群组服拓扑) + 节点详情 JDK 标签
  components/   # 共享组件 (console[控制台侧栏/可组合卡片画布 WorkspaceCanvas/卡壳/终端面板], ui/shadcn, terminal, chart)
                # V2: config-editor(表单/原始/版本) · provision-wizard · jdk-manager · clone-dialog · registration-editor
                # DangerConfirm: 统一危险操作二次确认（高危需输入名校验 + 角色门禁，FR-059）
  hooks/        # 自定义 hooks
  i18n/         # 中文 + 英文（danger 命名空间 = 危险操作文案）
  lib/          # 工具函数（jwt 解码声明、danger 角色门禁判定）
  router.tsx
  route-permissions.ts
```

### 8.7 危险操作保护（FR-059）

所有破坏性操作统一经 `components/DangerConfirm.tsx` 二次确认，替代 `window.confirm` 与零散内联确认弹窗：

- **二次确认**：基于 shadcn Dialog，主按钮恒为 `destructive` 样式。
- **高危输入名校验**：传 `confirmText`（通常为资源名）后，用户须逐字输入该名称方可确认（删实例/删用户等）。
- **角色门禁**：传 `scope`（`group` = 组管理员+，如删实例/删备份/删 Bot；`platform` = 仅平台管理员，如删用户/删群组）。越权用户确认按钮禁用并提示；前端仅做 UI 拦截，最终拒绝由 Control Plane RBAC 中间件强制（架构不变量）。审计经既有后端中间件留痕。
- 角色来自 `stores/auth` 解码自身 access token 的 `role` 声明（`lib/jwt.ts`），门禁判定纯函数为 `lib/danger.ts#canRunDanger`。

其它 FR 的新破坏性操作（如 FR-048 节点下线、FR-052 删插件、FR-058 批量 kill）应复用此组件，按上述 `scope`/`confirmText` 约定接入。

## 9. Bot Worker 架构

```
bot-worker/src/
  ipc/          # stdin/stdout JSON 行协议
  bot/          # Mineflayer 连接、重连、生命周期
  behavior/     # 行为引擎 (Tick 250ms): follow, guard, patrol, idle, custom
  script/       # 脚本执行器 + 进度上报
  debug/        # 交互式调试会话
  pathfinder/   # mineflayer-pathfinder 封装
  state/        # 3s 周期状态上报
  health/       # 心跳检测
```

容量：50 bots/worker, 256 workers max ≈ 12,800 bots

## 10. 状态机

```
STOPPED → STARTING → RUNNING → STOPPING → STOPPED
                                  ↓
                               CRASHED → STARTING (指数退避)
```

## 11. 配置

**Control Plane**: `control-plane.yaml` — server port, gRPC port, database, JWT secret（管理员账号通过首次启动 Web 引导创建，见 FR-017）；`log_store`（日志中心，FR-049）；`proxy`（出站代理，FR-174，见 §11.2）
**Worker Node**: `worker.yaml` — node name, Control Plane address, gRPC/WS ports, data_dir, Docker, Bot 配置；`proxy`（出站代理，FR-174，见 §11.2）

`log_store`（日志持久化/归档/保留，均有默认值，零配置即用）：

```yaml
log_store:
  enabled: true                 # 是否启用日志入库与归档
  persist_platform: true        # 平台结构化日志是否一并落库
  retention_days: 14            # 保留天数，<=0 不按时间清理
  max_total_mb: 512             # 表内日志总量上限(MB)，<=0 不按总量清理
  archive_interval_minutes: 30  # 后台归档/保留巡检周期
```

归档目录恒为数据根 `var/log`（不可配，保证便携自洽）：超阈值的旧日志按 NDJSON（`logs-YYYY-MM-DD.ndjson`）滚动落盘后从表中清理。

### 11.1 项目自包含数据根（FHS 布局，ADR-010）

平台运行态数据统一收口到单一数据根，默认进程工作目录下 `./data`，可经环境变量 `JIANMANAGER_DATA_DIR` 覆盖；进程启动时若不存在按布局自动初始化（CP 与 Worker 同源约定，由 `internal/platform/dataroot` 解析）。

```
data/
├── bin/              # 平台/辅助可执行
├── etc/              # 平台与节点配置
├── opt/jdks/         # 便携 JDK：<vendor>-<ver>/（取代旧的 <serversDir>/jdks）
├── var/
│   ├── servers/      # 服务器工作目录：<slug>-<shortid>/（系统分配）
│   ├── index/        # 全文搜索倒排索引：<instance-uuid>/（Worker 本地派生，ADR-017）
│   ├── artifact-cache/ # 节点制品缓存：<sha256[:2]>/<sha256>(+.meta)（Worker 本地派生，FR-178）
│   ├── log/          # 运行日志
│   └── artifacts/    # 制品库（内容寻址，见 §14 / ADR-011；CP 全局，区别于上方节点本地缓存）
└── cache/            # 临时：下载中转/解压
```

- 登记路径**按数据根相对存储**（如 `var/servers/hub-a1b2c3d4`），整体拷到另一机器后仍自洽。
- Worker 收到 CP 下发的相对工作目录后，按本节点数据根解析为绝对路径并创建。

### 11.2 出站网络代理（每进程 HTTP/SOCKS5，FR-174 / ADR-037；可视化配置 + 下发 FR-185 / ADR-043）

CP 与各 Worker 的**所有出站下载**统一收口到共享出站 HTTP 客户端工厂 `internal/platform/httpclient`（`Config{URL, NoProxy}` + `New(cfg) (*http.Client, error)`），按本进程代理配置出站。收口的出站点：

| 进程 | 出站点 | 用途 |
|---|---|---|
| CP + Worker | `internal/platform/selfupdate.DownloadWith` | 自更新二进制下载（`Download` 保留为 DefaultClient 薄包装，生产走 `DownloadWith`） |
| CP | `service.SelfUpdateService`（`resolveRelease`：GitHub Releases API / feed 回退 + CP 自升下载 + 升级前备份/回滚，FR-175/FR-182/ADR-036 §7/ADR-042） | 更新源解析（默认 GitHub Releases，feed 回退）+ CP 自身升级/回滚 |
| CP | `service.CoreService` | PaperMC API 解析服务端核心版本/构建 |
| CP | `service.AssetService.IngestFromURL` | 远端制品（服务端核心等）下载入库 |
| Worker | `grpc.Server.UpgradeWorker` | Worker 升级二进制下载 |
| Worker | `jdk.Manager`（`downloadAndExtract` / Zulu 元数据 API / foojay disco API） | JDK 归档下载 + foojay 版本目录/下载源解析（FR-178） |
| Worker | `worker/grpc.DownloadCore`（`downloadFile`，经 `artifactcache` 命中复用） | 服务端 jar 下载到实例工作目录（FR-178 缓存命中即免下载） |
| Worker | `decompiler.Provider` | CFR 反编译器按需下载（Maven Central） |

配置（CP `control-plane.yaml` 与各 Worker `worker.yaml` 各加 `proxy:` 段，互相独立；分布式各机网络环境不同）：

```yaml
proxy:
  url: ""        # 代理地址；scheme 决定类型 http:// / https:// / socks5://。留空=直连
  no_proxy: ""   # 逗号分隔免代理：localhost,127.0.0.1,10.0.0.0/8,.internal.example
```

行为规则：

- `url` **留空 = 沿用改造前行为**：回退 `http.ProxyFromEnvironment`，仍尊重 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` 环境变量（零配置/旧部署不受影响）。
- `url` 非空时**优先于环境变量**；`http`/`https` 经 `Transport.Proxy`，`socks5` 经 `golang.org/x/net/proxy` 构造 dialer 挂 `DialContext`。两类均遵守 `no_proxy`（`no_proxy` 命中走直连）。
- 含凭据的代理 URL 经 `${ENV}` 注入、不硬编码（config-files 规范）；日志/错误透出代理地址时**脱敏 `user:pass`**。
- 启动时 `proxy.url` 非法 → CP/Worker **fail-fast** 退出（配置错误早暴露，不静默直连）。
- **不在范围**：备份远程存储（SFTP/WebDAV/S3，用户自有端点）、通知/Webhook 投递、Worker 抓本机 ServerProbe `/metrics`（loopback）——均非外部制品下载，不经本工厂。

#### 可视化配置 + 节点级下发（FR-185 / ADR-043）

在 yaml/env 之上叠加「面板集中配 + 运行时热生效 + 节点级覆盖」层（**不删 yaml 路径，作回退**）：

- **运行时持有者**：CP 与 Worker 的出站 client 由「启动 `New` 一次」升级为 `httpclient.Provider`（内含 `atomic.Pointer[http.Client]`）。上表各出站点改为「每次从持有者取当前 client」（`SetHTTPClientProvider(provider.Client)`），保存/下发新代理后 `Rebuild` 原子替换即对后续下载生效、无需重启。
- **CP 全局代理（设置面板「网络」分类）**：`platform_settings` 白名单加 `proxy.url`（敏感、脱敏展示）、`proxy.no_proxy`；保存复用 `httpclient.New` 校验、落库后经回调重建 CP 出站持有者。生效优先级 **settings DB > control-plane.yaml > env**。此全局值同时作为**各节点默认代理**。
- **节点级覆盖（节点页「代理」分段）**：`nodes.proxy_mode`（inherit/custom）+ `proxy_url`/`proxy_no_proxy`。`PATCH /nodes/:id/proxy`（平台管理员 + 审计）设置；CP 据「custom ? 节点值 : 全局默认」算每节点期望代理 + `proxy_generation`（FNV 哈希），随**心跳响应**（§6.1）下发；Worker `heartbeat` 处理响应，generation 变化才重建出站持有者（避免每拍重建）。节点出站优先级 **节点 custom（DB 下发） > 全局默认（DB 下发） > worker.yaml > env**。
- **真相源 = CP DB**，Worker **不落盘**；重连/重启由后续心跳天然重发（符合「DB 仅 CP 读写」「CP→gRPC→Worker」不变量）。含凭据 URL 在 UI 回显 / API 响应 / 日志一律 `httpclient.Sanitize` 脱敏；节点离线时面板标注「待下发」（下次心跳生效）。

## 12. 部署

**开发**: `go run ./cmd/control-plane --dev` + `cd web && npm run dev`
**生产**: 多节点部署，Control Plane 一个 + Worker Node 多个
**Docker**: `Dockerfile.control-plane` + `Dockerfile.worker` + `docker-compose.yml`

### 12.1 构建与发布管线（GitHub Actions，FR-173，见 ADR-036）

`.github/workflows/release.yml` 在 `ubuntu-latest` 全程交叉编译产出 GitHub Releases 制品，三 job 串联：

- **prepare-embeds**（一次性产出全部 `go:embed` 资产，平台无关跨 matrix 复用）：`submodules: recursive` 拉取 `third_party/ServerProbe`，装 Go / Node20 / JDK21；构前端（`gen-licenses` → `vite build` → 复制到 `internal/controlplane/embed/dist/`）+ 内嵌探针 jar（`embed-probe`）+ 客户端更新器两件套（`embed-client-updater`，以 `--release 8` 在 JDK21 上构 Java8 字节码）+ CFR 反编译器（`embed-cfr`，sha256 pin 与 `decompiler/cfr.go` 常量一致）；embed 目录作 job artifact 上传。该 job 顺带解析触发类型算出注入版本经 job output 下传（正式=去前缀 tag `vX.Y.Z`，预发布=`0.0.0-dev+<shortsha>`）。
- **build**（matrix `linux/amd64` + `windows/amd64`）：下载 embed artifact 还原到 `internal/**/embed/`，`GOOS/GOARCH go build -ldflags "-X .../internal/version.Version=<v>"` 编 control-plane 与 worker（共 4 个二进制），命名 `<component>-<os>-<arch>[.exe]`（ADR-036 §1）。
- **release**：汇总 4 二进制 + 生成 `checksums.txt`（每件 sha256，ADR-036 §2），用 `scripts/changelog-extract.mjs` 取发布说明——push tag `v*` → 正式 release（取该版本段，`prerelease=false`）；push `master` → 覆盖固定 tag `latest` 预发布（FR-182 由 `nightly` 改名，取 `[Unreleased]` 段，`prerelease=true`，先删旧 release 再重建以仅保留本次产物）。

发布二进制**内嵌全部可选资产**「下载即用」：CP 自带前端 + 探针 + 客户端更新器，Worker 自带 CFR（ADR-036 §5）。`go:embed` 对缺失/空目录会编译失败，故 prepare-embeds 任一内嵌步骤失败即 fail-fast。版本注入在 build/release 两 job 按 prepare-embeds 同一 output 取值，保证二进制内 `version.Version` 与 release tag 一致。发布制品的命名/校验/渠道契约由 ADR-036 固化，供 FR-175 自更新对接 GitHub Releases 消费（ADR-020 §4 的 feed 来源立场由 FR-175 落地时标 superseded）。

## 13. MC 群组服模型（V2）

> 对应 PRD FR-031~036、ADR-007/008。代理 + 多 Bukkit 子服的开服与运维。开发中。

### 13.1 角色与关系
- 实例 `role`：`proxy`（BungeeCord/Velocity）、`backend`（Bukkit/Paper 子服）、`universal`（通用进程）。实例是独立原子单元。
- **proxy ↔ backend 为 M:N**（`server_registrations`）：一个 backend 可注册进多个 proxy（共享大厅/小游戏）；每条注册带「代理内本地属性」alias/priority/forced_host/restricted。
- **群组（Network）为非独占软标签**（`network_members` M:N）：仅供分组/筛选/批量操作，子服可属多群组；真实路由只由 `server_registrations` 驱动。

### 13.2 资源所有权（系统分配）
- **工作目录**：系统在数据根 `var/servers` 下分配 `<name-slug>-<shortid>`（CP 分配并按相对路径登记，Worker 解析为绝对路径），用户不可输入，路径只读展示（取代 BUG-004 必填 UI，落位见 §11.1 / ADR-010）。
- **端口**：端口池为新实例分配同节点唯一的 server-port/rcon/query，代理监听端口同理；分配由 Worker 实施、CP 登记。
- **JDK/运行时**：按节点维护 `node_jdks` 注册表，支持安装多版本（默认 Adoptium）；JDK 装入数据根 `opt/jdks`（见 §11.1）；实例绑定 JDK，启动注入 JAVA_HOME/PATH。

### 13.3 配置引擎
- 多格式 **保留注释** 的 round-trip 读写：properties / yaml / toml / json / txt。
- 内置 MC 配置 schema（server.properties、spigot.yml、paper-global.yml、bukkit.yml、velocity.toml、bungeecord config.yml）。
- 跨文件/跨实例/跨网络一致性校验：端口唯一、`online-mode=false` 与代理转发配套、`forwarding-secret` 在共享 backend 的所有 proxy 间一致。
- 每次保存生成 `instance_config_versions`，可 diff / 回滚。
- **通用文件版本（FR-051）**：编辑器保存或上传覆盖**已存在**的任意文件前，CP 经 gRPC 读旧内容落库 `file_versions`（base64 二进制安全），提供版本列表 / diff / 一键回滚。与配置版本同机制但刻意分表：配置版本带 schema/校验语义，通用文件版本只关心字节内容。保留上限与触发快照大小阈值由 `file_version.max_per_file` / `file_version.max_size_bytes` 配置，超大文件（如世界存档）跳过快照。复用 `unifiedDiff`、`ErrNodeNotConnected` 等既有领域逻辑。

### 13.4 结构化启动（取代自由文本命令）
- MC 实例由 `jdk + jvm_args + core_jar + args` 派生启动命令，Worker 组装 `cd <workDir> && <jdk>/bin/java <args> -jar core.jar nogui`（根治 BUG-005 引号问题）；universal 实例仍可自由命令。

### 13.5 一键复制子服
- 复制产出独立新实例（系统分配新目录/端口）；拷贝 workDir 时排除 session.lock / logs / 缓存 / usercache。
- 配置引擎修正身份字段（端口 / 名称 / motd，可选 level-name），保留 forwarding secret；按勾选注册进 0/1/多个代理（写入各代理 servers + priorities）。

## 14. 制品库（内容寻址，ADR-011）

> 平台所有二进制资产（核心 jar、插件、图片、视频、媒体 blob…）统一进内容寻址的制品库，带 sha256/md5 完整性校验，可去重、可追溯、可复用。核心 jar 是第一类资产，模型同样容纳后续插件/图片/媒体。物理根位于数据根 `var/artifacts`（见 §11.1）。

### 14.1 类型分区 + 内容寻址（CAS）
- 资产存 `var/artifacts/<type>/<sha256 前 2 位>/<sha256>.<ext>`；类型内按 sha256 去重，类型间物理分目录（便于浏览/整类备份/归档）。
- `type` ∈ `core | plugin | image | video | archive | blob`。sha256 既是寻址键也是去重键，登记 `rel_path` 相对数据根存储（便携）。

### 14.2 入库与完整性
- 入库即算 sha256+md5；调用方提供期望校验和则比对，不符拒收。
- 同 `(type, sha256)` 命中 → 复用记录并刷新 `last_used_at`，不重复落盘。
- 入口：multipart 上传 / 从本地路径登记 / 下载入库（`IngestFromURL`，供 FR-034 建服取核心复用）。

### 14.3 生命周期与引用保护
- `storage_state`(hot/archived/external) + `storage_backend` 驱动归档/外置（归档策略与外部后端为后续 FR，此处先立模型）；归档只改状态与位置，DB 记录与引用（sha256）不变。
- `ref_count`>0（被模板/实例引用）的资产删除前拒绝。

### 14.4 API 与鉴权
- `GET /assets`（按 type 筛选、分页）、`GET /assets/:id`、`POST /assets`（上传/登记）、`DELETE /assets/:id`。
- 平台级共享资源，统一由平台管理员管理（同节点/模板的平台管理员收敛）。
