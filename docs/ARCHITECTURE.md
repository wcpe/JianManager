# ARCHITECTURE — JianManager

> 本文档始终反映系统当前状态，不保留历史版本。历史决策见 `docs/adr/`。

---

## 1. 系统全景

```
浏览器 (React SPA, go:embed 嵌入 Control Plane)
    │ HTTP REST /api/v1/*  +  WS /ws/terminal（终端经 CP 中转，浏览器不直连 Worker）
    ▼
Control Plane (Go 单二进制)
    │ gRPC —— 指令优先经 Worker 主动建立的「反向隧道」下发（Worker 零入站，FR-281/ADR-066）；
    │         无隧道（老 Worker / 重建窗口）回退 CP 直拨 worker gRPC 端口
    ▼
Worker Node (Go) × 20~100
    ├── 游戏服进程管理 (direct/daemon/docker)
    ├── 守护进程 Wrapper
    ├── WebSocket 终端服务 (/ws/terminal，CP 经 TerminalSession gRPC 桥回环接入；亦作老 CP 直拨回退)
    ├── 插件桥反向 WS 服务 (/ws/plugin-bridge, 探针主动连入, token, FR-065/ADR-016)
    ├── Bot 管理 → Node.js 子进程 (Mineflayer)
    └── 指标采集（ServerProbe /metrics）
        ▲ HTTP GET /metrics (本机回环抓取)  +  ◀ 反向 WS (探针桥, 治理/事件通道)
        └── ServerProbe 探针 jar (运行于游戏服 JVM, FR-010 监控见 ADR-014, 治理桥见 ADR-016)
```

## 2. 三进程模型

| 进程 | 语言 | 部署 | 职责 |
|---|---|---|---|
| Control Plane | Go | 1 个实例 | API、认证、调度、gRPC 客户端池（隧道优先/直拨回退）、终端 WS 中转、前端静态文件 |
| Worker Node | Go | 20-100 个实例 | gRPC 服务端 + 反向隧道客户端、进程管理、Docker 管理、WS 终端服务（本机桥/回退） |
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
  config/                        # control-plane.yml 与环境变量覆盖
  database/                      # GORM 初始化、迁移与数据根解析
  middleware/                    # JWT、访问上下文、审计、限流、分发防护
  model/                         # 用户/组/节点/实例、指标、任务、通知、客户端分发、业务事件等模型
  router/                        # REST API：实例/节点/终端/文件/Bot/监控/客户端分发/业务域等
  service/                       # 领域服务；含 terminal_proxy、business_events、client_*、metric/log/task/notification/selfupdate
  grpc/{pool,handler}.go         # Worker 连接池、注册/心跳/流式事件控制面
  embed/{static,probe,install_scripts,client_updater}.go # 前端、探针、安装脚本与客户端更新器内嵌
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
│  grpc_server, ws_server(/terminal, /plugin-bridge)   │
├─ 进程管理层 ────────────────────────────────────────┤
│  ProcessManager → IProcessCommand (策略模式)         │
│    Direct / Daemon / Docker                          │
├─ 守护进程 ──────────────────────────────────────────┤
│  socket_server, java_process, output_buffer,         │
│  pid_file, commands, frame                           │
├─ 终端与日志流 ──────────────────────────────────────┤
│  process stdout/stderr → WS terminal/log + gRPC events │
├─ Bot 管理层 ────────────────────────────────────────┤
│  bot manager, ipc, Node.js 子进程生命周期             │
├─ 指标采集 ──────────────────────────────────────────┤
│  collector, serverprobe(/metrics), heartbeat snapshots │
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
  config.go                                      # 加载 worker.yml + env 覆盖（FR-080）
  heartbeat/                                     # 心跳负载、任务快照与代理配置下发处理
  setup/                                         # 免配置首次上线向导（FR-222）
  register/{register,identity}.go                # 注册（带 enroll token）+ 本地身份持久化（FR-080）
  grpc/{server,*_ops,plugin_bridge}.go           # 生命周期/文件/归档/配置/Docker/备份/JDK/升级/搜索/探针等模块化 RPC
  process/{manager,command,direct,daemon,docker,images,preflight,detach*}.go
  daemon/{wrapper,conn*,pid_file,pid_alive*,buffer,frame}.go
  ws/{server,bridge}.go                          # 终端 WS 与探针桥 WS
  bot/{manager,ipc}.go                           # Bot 子进程管理与 IPC
  metrics/{collector,serverprobe}.go             # ServerProbe /metrics 抓取与解析
  artifactcache/                                 # 节点服务端核心缓存（内容寻址）
  jdk/                                           # JDK 目录、下载与登记
  decompiler/ search/ storage/ taskreg/ embed/   # 反编译、全文索引、备份存储、任务注册、内嵌资产
```

### 5.1 节点接入与部署（一键安装 / enrollment，FR-080，见 ADR-020）

- **配置加载**：Worker 启动时经 `internal/worker/config.go`（viper）真正加载 `worker.yml`（CP gRPC 地址、grpc/ws 端口、data_dir、日志），`JIANMANAGER_` 前缀环境变量按路径覆盖。配置落盘取代历史的环境变量堆砌。
- **免配置自启 setup（FR-222，见 ADR-051，改写 ADR-020 §2 单脚本写配置编排）**：「下载」（取二进制）与「上线」（写配置 + 注册 + run）解耦——Worker 入口 `runWorker` 加载配置前先自检「是否已配置」（`无 worker.yml/.yaml` **且** `无 <data-dir>/etc/node-identity.json`）。**未配置** → 进入 `internal/worker/setup`：有 TTY 交互逐项问 CP gRPC 地址 / enroll token / 节点名（可选端口、data_dir，给默认值）；无 TTY（CI/管道/systemd/Windows 服务）从命令行参数 + `JIANMANAGER_*` env 读，缺必填（CP 地址 / token）即 fail-fast（不卡住等输入）。setup 顺序：写 `worker.yml`（原子、复刻安装脚本字段、**enroll token 绝不写入**）→ 携 token 经 gRPC 首注册换身份 → 持久化 `node_uuid`/`node_secret` 到 `etc/node-identity.json`（0600）→ **转入正常 run**（内存构造配置 + 复用首注册身份，不重启进程、不重复注册）。**已配置**（有 yml 或有 node-identity，或显式传配置文件路径）→ 跳过 setup 直接 run（现状零变化）。新机器零脚本依赖即可上线，安装脚本（FR-223）退化为「取二进制 + 调 setup」。
- **enrollment token 准入**：新增节点凭 CP 签发的**一次性、限时** enrollment token 注册（取代 FR-004 的「无凭据自助注册」对新节点的开放）。token 经 gRPC metadata `enroll-token` 传给 CP 校验消费（不改 proto）；CP 只对「新节点首次落库」设门槛，已有身份的重注册不强制 token（不破网）。
- **身份持久化**：注册成功换得的 `node_uuid`/`node_secret` 写入数据根 `etc/node-identity.json`（0600，含敏感 secret 不入日志）。Worker 重启优先读该文件复用既有身份走重注册，不重复消费已失效的一次性 token。CP 下发的 **WS 令牌密钥**（`wsTokenSecret`，FR-275/ADR-061）一并持久化于此：启动时优先用其构造终端/插件桥校验（回退 `worker.yml` `jwt_secret` 兼容旧 CP），注册/心跳下发变化时热更新并补写。
- **注册身份匹配（UUID 锚定，见 ADR-039，修复重名覆盖 BUG-A）**：`ControlPlaneHandler.Register` 按三级优先级匹配既有节点，杜绝「另一台机器用同名注册覆写旧节点身份/host」——
  1. **UUID 证明**：Worker 重注册时经 gRPC metadata `node-uuid` + `node-secret` 出示本地身份；命中库中节点且 secret 匹配 → 按 UUID 重注册（更新 host/port/os/arch，允许改名）；secret 不符 → `PermissionDenied`，绝不覆写。
  2. **同机 host 兼容（过渡）**：未升级旧 Worker 只带 name，name 命中既有节点且本次连接 host 与库存 host 一致（同机重启信号）→ 放行重注册并告警建议升级；host 不一致落到 3。
  3. **token 新建**：否则视为新节点，凭有效 enrollment token 准入；若上报名与既有节点撞名 → `AlreadyExists` 拒绝（提示改名），绝不覆写。
- **节点名活跃唯一**：身份由 UUID 锚定，`name` 降为可变标签但活跃节点间唯一——`database.AutoMigrate` 对存量重名活跃节点先去重（追加 `-dup-<id>` 后缀）再建「部分唯一索引」（仅约束 `deleted_at IS NULL` 的活跃行），软删除节点可释放其名供新节点复用（见 ADR-039 §3）。
- **坏节点检测/修复（见 ADR-039 §2）**：`NodeRepairService` 提供检测疑似被串改/重名节点（只读诊断）、把被挤占机器作为新节点重新 enroll（轮换 UUID/secret）、清理孤立 JDK/实例引用；破坏性操作需二次确认（`confirm=true`）并入审计（FR-015/FR-059）。HTTP 入口见 API.md 节点修复章节（UI 入口随 FR-177）。
- **一键安装脚本**：`scripts/install-worker.sh`（Linux/macOS）/ `install-worker.ps1`（Windows）由平台分发，幂等完成「下载或拷贝二进制 → 写 worker.yml → 以 enroll token 首注册 → 可选注册 systemd / Windows 服务（开机自启、常驻自连）」。enroll token 仅经命令行/环境变量传入、绝不写入 `worker.yml`。FR-190 起添加节点一键命令默认签发 CP-local `/worker-assets/:version/{os}/{arch}/worker?token=...` 下载 URL 模板，脚本按运行时平台替换；`enroll.binary_url` 仍可显式覆盖，离线场景保留 `--binary` 本地二进制兜底。
- **CP 静态托管安装脚本**：脚本经 `go:embed` 内嵌进 CP 二进制（`internal/controlplane/embed/install_scripts.go`，源由 `make embed-install-scripts` 从 canonical `scripts/` 同步、字节一致由测试守护），CP 以**匿名**路由 `GET /install-worker.sh`、`GET /install-worker.ps1`（根路径、先于 SPA 回退）下发。一键命令 `curl <cp>/install-worker.sh | sh` 据此可拉（此前 CP 不托管这两路径致 curl 404、一键安装失败）。匿名安全：脚本无机密，准入凭据在命令参数里，与签发 token 的管理员 JWT 端点暴露面隔离。
- **面板「添加节点」向导**：CP `POST /nodes/enroll-token` 签发 token 并返回 Linux/Windows 一键命令 + `scriptBaseUrl`（CP 托管脚本基址），前端节点页展示一键命令（复制）+「手动安装步骤」分步兜底命令，供运维粘贴到目标机器执行。

## 6. 通信协议

### 6.1 gRPC（Control Plane ↔ Worker Node）

Protobuf 定义位于 `proto/worker.proto`，包含：

- 生命周期：Register, Heartbeat (双向 stream), FetchBotWorkerArchive
  - `Register` 的身份匹配经 gRPC metadata 携带 `node-uuid`/`node-secret`（重注册出示身份）或 `enroll-token`（新节点准入），均不改 proto；匹配优先级与重名覆盖防护见 §5.1（ADR-039）
  - `RegisterResponse` 携带 `ws_token_secret`（FR-275，见 ADR-061）：CP↔Worker 专用 **WS 令牌密钥**（只签终端/插件桥令牌，与签用户会话的 `jwt.secret` 隔离，Worker 永不持有后者）。首注册与重注册均下发；Worker 持久化到 `etc/node-identity.json` 并热应用到 WS 校验。CP 侧密钥三轨：显式 `jwt.ws_secret` > 生产 autogen 持久化 `<dataRoot>/etc/ws-token-secret.key`（0600）> dev 回退 `dev-secret-change-me`；空字段（旧 CP）时 Worker 回退本地 `jwt_secret` 配置（向后兼容）
  - `Heartbeat` 负载除节点指标（CPU/内存/磁盘/累计网络字节/`load_avg1` 系统负载，FR-062）外携带 `instance_metrics`（每实例 ServerProbe 快照：TPS/MSPT/在线/堆/线程/CPU/uptime + 分世界负载，FR-060）；CP 收心跳经 `IngestHeartbeat` 落库为时序样本（node_cpu/mem/disk/net 速率/load）并据相邻累计字节算网络速率（Worker 不碰 DB）
  - `Heartbeat` 还加性携带 `tasks`（`TaskSnapshot`：task_id/state/progress/error/result/recent_log_lines，FR-183/ADR-040）——Worker 把运行中长任务（如 JDK 安装）的进度随心跳上报，CP 经 `TaskService.IngestSnapshots` upsert `Task` + 幂等追加 `TaskLog`，并在任务**首次进终态**时触发副作用（jdk_install 成功落 `NodeJDK` + 发成功站内信，失败发失败站内信）。日志行编码为 `<绝对序号>\t<正文>`，跨周期重叠窗口按绝对序号去重
  - `HeartbeatResponse` 加性携带 `ws_token_secret`（FR-275，见 ADR-061）：WS 令牌密钥每拍随心跳下发，Worker 比对「值变化」才热更新终端/插件桥校验并补写身份文件——CP 轮换密钥后 Worker 不重启即自愈（≤1 心跳周期）
  - `FetchBotWorkerArchive`（FR-308，见 ADR-070；CP 侧实现，Worker 调用）：Worker 注册成功后凭 `node_uuid+node_secret`（与重注册同源校验）拉取 CP 内嵌 bot-worker dist 归档；请求携带本地 `known_sha256`，指纹一致 CP 回空归档省流；CP 未内嵌回 `success=false` + 原因（Worker 回退本地已有）。归档 ~25KB 单 unary 传输（64MiB 上限内，FR-305）
  - `HeartbeatResponse` 加性携带 `proxy_url`/`proxy_no_proxy`/`proxy_generation`（出站代理可视化下发，FR-185/ADR-043）——CP 据「节点 custom ? 节点值 : 全局默认」算每节点**期望出站代理**，每拍随心跳响应下发；Worker 仅当 `proxy_generation`（期望代理配置的 FNV 哈希）变化时才 `httpclient.New` 重建出站持有者（`httpclient.Provider` 原子替换，避免每拍重建），新 client 注入到各下载点（JDK/CFR/自更新/服务端 jar）即时生效。真相源 = CP DB（`nodes.proxy_*`），Worker **不落盘**，重连/重启由后续心跳天然重发；下发为空回退本地 `worker.yml`/env。CP 自身出站代理由设置面板 `proxy.url`/`proxy.no_proxy`（settings DB 覆盖）管控、运行时重建，且作为各节点默认代理（优先级 settings DB > yaml > env）
- 实例操作：CreateInstance, StartInstance, StopInstance, RestartInstance, KillInstance, SendCommand, GetInstanceStatus, ListInstances
  - `CreateInstance` 除 `start_command` 外携带 `stop_command`（优雅停止命令，CP 按实例角色派生：backend/universal=`stop`，proxy=`end`），由 daemon wrapper 在优雅停止时写入进程 stdin；并携带 `probe_port`（CP 分配的 ServerProbe 端口，daemon 模式透传到 wrapper→PID 记录，供 Worker 心跳自采与重启恢复，FR-060）；以及 `graceful_stop_timeout_seconds`（CP 从平台设置 `graceful_stop.timeout` 取生效值随启动下发，daemon 透传到 wrapper 做超时强杀兜底，FR-063；值在启动时定型，对设置变更后新启动的实例生效）。同 UUID 幂等重注册会刷新启动命令、JDK、环境变量与 autoRestart；运行中的 daemon 不被打断，旧 strategy 标记过期，正常 Stop→Start 或 Restart 前重建并采用最新规格（FR-233）。docker 模式（FR-078，ADR-019）额外携带 `image`（容器镜像引用）与 `port_mappings`（容器端口↔宿主端口，宿主端口来自 FR-032 端口池），Worker 启动容器前据 `image` 自动拉取缺失镜像
- Docker 镜像管理（FR-078，ADR-019）：ListImages, PullImage, RemoveImage
  - CP 不直连 Docker，节点级镜像列出/拉取/删除经 Worker 委托（守架构边界）；`ListImages` 在节点 Docker 不可用时回 `docker_available=false`，CP 据此提示安装 Docker
- 实例事件流：StreamInstanceEvents (server stream)
  - 同一流承载两类事件：`state_change`（状态转换）与 `stdout`/`stderr`（进程输出）。Worker 进程输出回调分流为「WS 终端广播 + 事件流上报」两路，互不阻塞。CP 侧 EventService 把 `stdout`/`stderr` 经 LogService 落库（日志中心 FR-049），`state_change` 经 SSE 推前端
- 崩溃快照上报：`ReportCrashSnapshot`（FR-313；CP 侧实现，Worker 调用，与注册/心跳同信道）——进程非正常退出（退出码≠0 或 RUNNING/STARTING 态意外退出）时 Worker 组装崩溃现场（退出码/信号/时长 + 终端环形缓冲尾部 200 行/64KB）异步上报，凭 `node_uuid+node_secret` 鉴权且实例须属于该节点；CP 落 `instance_crash_snapshots` 并同事务按实例滚动只留最近 5 条。上报失败（网络/老 CP `Unimplemented`）Worker 记日志丢弃不阻塞状态机；daemon 模式退出码经 wrapper 控制通道事件帧（`TypeEvent`+`java_exit` JSON）上抛，老 wrapper 不发帧、老 Worker 忽略未知帧，新旧互不炸
- 文件操作：ListFiles, ReadFile, WriteFile, UploadFile (client stream), DeleteFile, RenameFile（跨目录即移动）, DownloadFile (server stream), DownloadArchive (server stream), SearchFiles
  - `ReadFile` 是**在线编辑器**读取能力，带 10MiB 护栏（超限截断；前端另有大文件/二进制预览拦截）。**下载不得复用 ReadFile**——曾因下载端点借用它导致超限大文件被静默截断（详见 `DownloadFile`）
  - `DownloadFile` 单文件**原样分块流式**返回（~64KiB 分片，首帧携带文件总大小），任意大小不截断；CP `FileHandler.Download` 先收首帧再写响应头（打开失败/越界/目录/老 Worker 无本 RPC 时仍能返回 JSON 明确错误而非半截文件），并以首帧总大小设 `Content-Length`——流中途失败即字节数不符，客户端按下载失败处理。老 Worker（无本 RPC）明确报错引导升级，**不回退会截断的 ReadFile**
  - `WriteFile` 是实例工作目录内受限写入能力（在线编辑器小文本保存用，unary 受直拨 64MiB 单消息上限约束）。**上传不得复用 WriteFile**——曾因上传端点全量缓冲 + 复用它导致直拨 >64MB 被拒收、反向隧道下双侧内存整块缓冲（FR-304）
  - `UploadFile` 单文件**client-stream 流式上传**（FR-304，与 `DownloadFile` 对称）：首帧携带 `instance_uuid+path`、后续帧纯内容（~64KiB 分片）；Worker 同目录临时文件接收、收完 `os.Rename` 原子覆盖目标，中途任何失败删临时文件不动既有目标；响应回报 `bytes_written` 供 CP 完整性比对。**零帧即关流约定返回业务级失败（无副作用）**，CP 借此逐次探测老 Worker（返回 `Unimplemented`）并回退 `WriteFile` unary（≤64MB，超限明确报错引导升级）。CP 上传 handler 流式读 multipart（目标路径经 query 参数先行）不整块缓冲；FR-052/053 插件单发与批量扇出部署经同一 `uploadToWorker` 统一入口走本 RPC，老 Worker 自动回退，不为插件部署另设 RPC
  - `DownloadArchive` 把选中的文件/目录（目录递归，仅常规文件）即时打包为 zip 边遍历边分片流式返回（每条目经 `validatePath` 防越界/zip-slip，~32KiB 分片，不缓冲整包）；CP `FileHandler.DownloadArchive` 逐帧 `Recv` 写响应并 `Flush`，转为 HTTP `application/zip`（批量下载，FR-070）。资源管理器树内拖拽「移动」复用 `RenameFile`，无独立 move RPC
  - `SearchFiles` 对实例工作目录做全文搜索 / 文件名快速打开（FR-074，见 ADR-017）。索引是 **Worker 本地派生资产**（落数据根 `var/index/<instance-uuid>/`，**不进 CP 数据库**）：Worker 每实例持有一份倒排索引（token→文件集合）+ 文件指纹表，查询前按指纹比对增量更新（增/改/删）再倒排取候选、候选内精确行扫描；`mode=filename` 走文件名子串匹配（行号 0）。CP 仅经 gRPC 转发查询、不持有索引
- **单消息尺寸上限统一（FR-305）**：CP↔Worker 两方向、直拨/隧道双模式共用 **64MiB** 单一真值（`internal/platform/grpcmsg.MaxMessageBytes`）——直拨侧 Worker `ServerOptions()` 收/发显式同值、CP 连接池 `WithDefaultCallOptions` 显式同值（修客户端接收 4MiB 默认暗礁）；隧道侧 grpctunnel 不受理 grpc.ServerOption（原天花板为 4GB 硬编码），由 `grpcmsg.WrapRegistrar` 在注册层以双向拦截器（请求进 handler 前、响应发送前 `proto.Size` 判限）施加等效守卫，超限一律 `ResourceExhausted`+中文引导。大载荷一律走流式 RPC（UploadFile/DownloadFile/DownloadArchive），unary 仅承载 <64MiB 载荷（现存最大 DeployServerProbe ~7.6MB）
- 归档浏览与反编译（FR-075；见 ADR-018）：ListArchiveEntries, ReadArchiveEntry, DecompileClass
  - `ListArchiveEntries`/`ReadArchiveEntry` 用 Go `archive/zip` **只读**列举/读取 jar/zip 内部条目（不起进程、零落盘，条目名经 zip-slip 校验，条目数/单条目字节有上限超出截断，内容嗅探 NUL 判二进制）；`DecompileClass` 经实例绑定 JDK（或系统候选 JDK / `JAVA_HOME` 兜底）**受控 exec** CFR 单 jar 把 `.class`/`.jar`（或 jar 内某 `.class` 抽临时文件）反编译为 Java 源码——CFR 仅静态分析字节码、不加载/运行目标代码，`context` 超时 + 输入体积上限 + 输出截断 + 失败/降级以 `success=false`+结构化 error 返回（不抛错）。CP 加性端点 `GET .../files/archive/entries`、`GET .../files/archive/read`（octet-stream + `X-Truncated`/`X-Binary` 头）、`POST .../files/decompile`，均复用文件「查看」级权限。CFR 分发：配置路径 > 内嵌（`make embed-cfr`，gitignore 不入库）> 数据根缓存 `var/tools/cfr-<ver>.jar` > Maven Central 按需下载（sha256 pin）
- 终端：IssueTerminalToken
- Bot：CreateBot, DeleteBot, ListBots, SetBotBehavior, SendBotCommand；`StreamBotEvents`/富遥测归 FR-041 后续实现，当前 Worker gRPC 以 `ListBots` 快照 + CP 读取时懒回填状态为准
- 探针部署：DeployServerProbe（CP 内嵌 ServerProbe jar + 生成的 config.yml + 运行库缓存 `libraries_zip` 经 gRPC 下发，FR-010/FR-114；见 ADR-014/016）。Worker 写入实例 `plugins/` 并把缓存包解压到实例根 `libraries/`，**在线更新**（FR-068）复用本 RPC 推最新内嵌 jar 与缓存（下次重启生效，可选推送并重启），经 `GET/POST /instances/:id/probe/update`
- 插件桥（FR-065；见 ADR-016）：StreamPluginEvents (server stream，CP 订阅某实例/全部探针经反向 WS 上报的事件流 connected/disconnected/heartbeat/玩家事件)、SendPluginCommand（CP 经 Worker 向探针下发治理/查询指令）、QueryServerState（查询子服全状态骨架）。地基阶段真实承载 connected/disconnected/heartbeat 与通道层，业务事件/治理执行语义留 FR-066/067
  - **JBIS 业务事件汇聚上行（FR-122；见 ADR-027/028）**：同一条 StreamPluginEvents 流复用承载 `domain` 非空的业务域事件（PluginEvent 的 `domain`/`dedup_key`/`raw_json` 字段，Worker 透传不消费语义）。CP 侧 `PlayerEventService` 据 `domain` 分流：玩家/监控事件走在线名册 + SSE，业务事件交 `BusinessEventService` 按 (domain,dedup_key) 去重落 `business_events` 通用信封，经济域(`economy`)再解析信封维护 `economy_balance_mirrors`(node→zone 维度、seq 单调)+`economy_ledger_entries`(审计)。探针侧由 mce `PlayerEconomyChangeEvent`/`PlayerEconomyCatchupEvent` 折算上报（覆盖 web 后台/跨服一切余额变更），currencyId Int→identifier 折算保证跨服聚合不串味。CP 插件无关、只认信封
- 指标：`GetNodeMetrics` 采集 Worker 所在节点 CPU / 内存 / 磁盘实时快照；`GetInstanceMetrics` 请求带 `probe_port`，由 Worker 抓 ServerProbe `/metrics`（**RCON 已退役（FR-067/ADR-016）**——探针未就绪时富指标 N/A，不再回退 RCON）。`GET /api/v1/nodes/:id/metrics` 节点面板端点优先经已连接 Worker 主动拉取，连接池暂无该节点时回退 CP 中最新心跳快照；实例实时面板继续按需 `GetInstanceMetrics` 拉取；**历史时序**（FR-060）由 Worker 心跳推送 `instance_metrics`，二者互补
- 玩家管理：SendPluginCommand（FR-067/ADR-016；CP 经 Worker 反向 WS 向探针下发踢/封/解封/白名单治理指令，探针经服务端 API 执行；在线列表经探针事件聚合）。**RCON 路径已退役**，`ExecRconCommand`/`rcon_client` 移除；探针未连入时优雅降级
- 配置 (V2)：ListConfigFiles, ReadConfig, WriteConfig, ListConfigVersions, RollbackConfig
- 运行时 (V2)：ListJDKs, InstallJDK, RemoveJDK, JDKCatalog, ProbeJDK, ScanRuntimes, InstallRuntime, RemoveRuntime, GetPMConfig, SetPMConfig, ListGlobalPackages, InstallGlobalPackage(异步任务), RemoveGlobalPackage, DownloadCore, InstallForgeServer, ListArtifactCache, EvictArtifactCache, ClearArtifactCache, SetArtifactCacheCap, BrowseDir
  - `ScanRuntimes`（FR-298 节点运行时库）：按类型（jdk/nodejs）扫描节点**常见安装路径**发现运行时候选（`internal/worker/runtimescan`，路径表按 GOOS 内置：jdk=`/usr/lib/jvm/*`/`/opt/java*`/`/opt/jdk*`/sdkman 与 Windows `Program Files\Java|Eclipse Adoptium|Microsoft\jdk*`；nodejs=`/usr/local/bin/node`/`/usr/bin/node`/`/opt/node*/bin/node`/nvm 与 Windows `Program Files\nodejs`、`%APPDATA%\nvm`）。jdk 探测复用 `jdk.detectAt` 语义、nodejs 跑 `node --version` + `node -p process.arch`（arch 保留 nodejs 命名 x64/arm64）；路径不存在/探测失败**静默跳过**不阻断整体；托管根下候选标 `already_registered`，CP 侧再按 DB 已登记路径补标
  - `InstallRuntime` / `RemoveRuntime`（FR-299 Node.js 一键安装）：`InstallRuntime` **仅异步任务路径**（`task_id` 必填，语义同 InstallJDK 异步）——Worker 侧 `internal/worker/runtime` 安装器经 `<mirror>/index.json`（默认 `https://nodejs.org/dist`，镜像可配平台设置 `runtime.mirror.nodejs`）解析该 major 最新版本，下载便携归档（linux tar.gz / windows zip）解压到托管目录 `<数据根>/opt/runtimes/nodejs-<major>/`；下载复用 jdk 包导出的 `DownloadAndExtract` 基建（同一出站 client + 停滞看门狗 FR-290 + 网络失败引导 FR-279），残骸自愈完成标记 = node 可执行文件（`bin/node`|`node.exe`，FR-291），arch 用 nodejs 命名（x64/arm64，CP 按类型归一、未知 422，齐平 FR-289）。终态经心跳落 `node_runtimes`（managed=true）。`RemoveRuntime` 删除托管目录（归一顶层清理、拒根本身与根外路径，FR-292）
  - `GetPMConfig` / `SetPMConfig`（FR-306 节点包管理器）：PM 偏好（npm/pnpm/yarn，pnpm/yarn 经托管 Node 的 **corepack enable** 激活，`internal/worker/pkgmgr`）与多 registry 配置。registry 落节点**托管 .npmrc**（`<数据根>/opt/runtimes/.npmrc` 原子写；默认源/`@scope` 域源/`_authToken` 凭据行），后续 PM 操作（FR-307 全局包）经 `NPM_CONFIG_USERCONFIG` 指向它、不污染用户 `~/.npmrc`。偏好真相源 = CP `node_pm_configs`（Worker 不持久化偏好）；`GetPMConfig` 回读 .npmrc 时**凭据行不回传**；corepack 探测=托管 Node bin 下有无 corepack、PM 版本=`<pm> --version`
  - `InstallJDK` 携带 `mirror_base`（CP 从平台设置 `jdk.mirror.<vendor>` 取生效值后下发；Worker 用它构造下载 URL，使运行时配置的镜像源真生效，FR-033/FR-063；为空回退 Worker 本地 env/官方默认源）
  - `InstallJDK` 加性携带 `task_id`（FR-183/ADR-040）：非空时走**异步**——Worker 登记内存任务表、`go` 后台下载（带字节进度计数）、RPC 立即返回 `task_id`（不再阻塞最长 20min），进度/日志/终态经心跳 `tasks` 上报，CP 据终态落 `NodeJDK` + 发站内信；为空回退同步路径（向后兼容）
  - `InstallJDK` 加性携带 `version`（FR-178）：非空时 Worker 经 **foojay disco API** 按具体版本解析下载源；为空取该大版本最新 GA
  - `JDKCatalog`（FR-178）：Worker 经 foojay disco `/packages` 查某发行版可选具体版本（扩厂商至 Liberica/Microsoft/Semeru/GraalVM… + 保留 Temurin/Corretto/Zulu 直链回退），CP 代理喂前端版本选择器；`buildDownloadURLV` 在 `internal/worker/jdk/foojay.go` 统一「直链回退 vs foojay 解析」分流
  - **节点制品缓存**（FR-178/330，`internal/worker/artifactcache`）：`DownloadCore` 改为**内容寻址缓存命中复用**——按 `sha256` 命中即从 `var/artifact-cache/` 秒拷到实例工作目录（免网络、`touch` lastUsed），未命中下载校验后存入缓存再拷；`sha256` 为空的源（Sponge Maven 等）按 `core|mcVersion|build` **组合键**入缓存/反查命中（FR-330：CP 先把 latest 解析为具体构建再下发键成分；BungeeCord latest 无构建号不参与组合键，不冻结 latest 语义）。**命中时全量校验缓存内容 sha256，损坏条目作废并回退下载**；**同缓存键并发 DownloadCore 单飞**——领队独下、其余等待后从缓存秒取，同核心并发搭建只走一次网络；provision 任务 stage 按响应 `cache_hit` 区分「缓存命中/下载完成」文案。**范围写死：仅服务端核心 jar，不缓存插件/其它下载路径**。`ListArtifactCache`/`EvictArtifactCache`/`ClearArtifactCache`/`SetArtifactCacheCap` 经 CP 端点（仅平台管理员 + 审计，CP 用 asset 表按 sha256 补全 name/version）管理这份缓存（条目带「核心」类型徽章）；容量上限 `artifact_cache.max_bytes`（worker.yml + CP 运行时下发）触发按 `lastUsedAt` 升序 LRU 淘汰；写用临时文件 + 原子 rename 并发安全
  - **SpongeForge 安装**（FR-046）：`InstallForgeServer` 仅服务 `coreType=spongeforge`，Worker 在实例工作目录下载 Forge installer 到 `.jianmanager/forge-installer.jar`，使用实例绑定 JDK 执行 `--installServer`，再把 SpongeForge universal jar 写入 `mods/SpongeForge.jar`；CP 仍先创建 `type=minecraft_java` / `role=backend` 实例。现代 Forge 以 `LaunchSpec.JavaArgFiles=["user_jvm_args.txt", "libraries/net/minecraftforge/forge/<forgeVersion>/{win,unix}_args.txt"]` 结构化启动，`CoreJar=forge-<mc>-<forge>-server.jar` 仅保留为兼容元数据；旧布局仍兼容根目录 `forge-*-server.jar`。该 RPC 不暴露任意路径写入能力，所有产物限制在实例工作目录内
  - `BrowseDir`（FR-178）：只读列出节点某绝对路径下的子目录（空路径返回盘符/根），经 CP 端点 `GET /nodes/:id/browse`（仅平台管理员、防穿越）供前端 JDK 路径登记目录选择器逐级浏览
- 复制 (V2)：CloneWorkDir（本机复制源工作目录到目标，排除运行态文件）
- 导入 (FR-302)：`InspectServerDir`（探测现成目录的核心 jar / 内嵌 JDK / `server.properties` 端口 / eula，守卫路径绝对存在且非托管区内已有实例目录）；`ImportServerDir`（`migrate` 模式实际搬迁=同盘 `os.Rename` 优先、跨盘递归拷贝+数量字节校验后清源；`in_place` 模式 no-op 回原路径）。落地 ADR-007 预留的「导入已有目录」高级模式，见 `docs/adr/`（导入例外与就地删除守则）
- 删除清理：RemoveInstance（CP 删除实例时移除 Worker 注册表条目、删除工作目录与派生搜索索引）。运行中/启动中/停止中的实例先经 CP 同步 Stop 编排收敛；节点记录缺失、节点离线或 Worker 未连接时中止删除并保留 CP 记录，只有在线 Worker 明确清理成功后才删实例记录，防止节点进程/目录成为无主孤儿。`RemoveAll` 仅限托管区（数据根 `var/servers`）内，托管区外（历史手填绝对路径、**FR-302 就地导入实例**）跳过目录删除并经 `work_dir_skipped` 回报；就地导入实例 CP 侧亦显式下发 `SkipWorkDir`（双保险），但仍须成功清理 Worker 注册后方可删除 CP 记录
  - 搭建子服/代理由 Control Plane 编排：分配端口/目录 → CreateInstance → DownloadCore 或 InstallForgeServer → WriteConfig，不另设通用 worker 端 Provision RPC
- 备份 (V2)：CreateBackup, RestoreBackup, TestStorageBackend（FR-056/057/152）
  - Worker 把工作目录打 tar.gz 落数据根 `var/backups/<instanceUUID>/`，据 base_manifest 做增量差异，始终回传完整文件清单供 CP 维护链/基准
  - 恢复按链顺序（全量基 + 各增量）回放；远程后端（S3/SFTP/WebDAV）由 Worker 持 CP 下发的 StorageBackendSpec 直传/拉回，凭证由 CP 从 `${ENV_VAR}` 解析后下发（Worker 不读环境/不碰 DB）
  - `TestStorageBackend` 仅做连通性与容量探测：CP 选择在线 Worker 下发已保存的后端规格，Worker 侧用同一 `internal/worker/storage` 抽象执行 `Ping`/`Stat`。S3/WebDAV/SFTP 通过小对象写入→读取→删除验证读写权限；失败以业务错误码回 CP，HTTP 层仍返回 `ok=false` 供前端行内展示。

### 6.2 WebSocket（浏览器 ↔ Worker Node）

终端经 CP 代理桥接，Control Plane 签发一次性 30s token 鉴权。token 用 **WS 令牌密钥**签发/校验（FR-275，见 ADR-061）：CP 与 Worker 共享的专用密钥（经注册/心跳自动下发，见 §6.1），与签用户会话的 `jwt.secret` 隔离：

```
Browser → Control Plane (POST /instances/:id/terminal/token)
  → 返回 {token, wsUrl}（wsUrl 指向 CP 代理端点 /ws/terminal）
Browser → CP (/ws/terminal?token=xxx) → CP 校验并转拨 → Worker (:wsPort/ws/terminal?token=xxx)
  → Worker 独立校验同一 token → 双向终端流（browser ↔ CP ↔ worker 桥接）
```

**空闲保活（FR-140）**：CP 终端代理（browser 侧与 worker 侧两个连接）与 Worker 终端桥都装 WS ping/pong 心跳——每 ~30s 发一次 ping、收到对端任意帧（含 pong）即续 ~70s 读超时。空闲终端（如 Paper 长时间无输出）经反向代理/LB 时不会被中间层按空闲超时（常见 60s）断连；同时据读超时检测真正的死连。ping 用 `WriteControl`（gorilla 保证与其它写并发安全），不与桥接/广播写互斥。

消息格式：

```json
// Worker → Browser
{"type":"stdout","instanceId":"xxx","data":"..."}
{"type":"stderr","instanceId":"xxx","data":"..."}
{"type":"state","instanceId":"xxx","state":"RUNNING"}
// CP 代理 → Browser（错误分支，FR-276）：Worker 以 401/403 拒绝令牌时给定向诊断
{"type":"state","state":"error","code":"WORKER_TOKEN_REJECTED","data":"终端令牌被 Worker 拒绝（HTTP 401）：该节点的 WS 令牌密钥与平台不一致。…"}

// Browser → Worker
{"type":"stdin","instanceId":"xxx","data":"..."}
{"type":"resize","instanceId":"xxx","cols":120,"rows":40}
```

### 6.2.1 监控探针 ServerProbe（Worker 抓 `/metrics`，FR-010 / ADR-014）

ServerProbe 是第三方监控探针（TabooLib，单 jar 多端 Bukkit+BungeeCord），作 git 子模块引入 `third_party/ServerProbe`。
CP 经 `go:embed` 内嵌探针 jar 与构建期运行库缓存（`internal/controlplane/embed/probe/`，`make embed-probe` 目标可选构建）。
建服 provision 时经 gRPC `DeployServerProbe(jar, config_yaml, libraries_zip)` 把 jar 与最小 config.yml 写入实例 `plugins/`，并把 TabooLib/Kotlin 运行库缓存解压到实例工作目录的 Maven local repository（默认 `libraries/`）。
Worker 对运行库缓存只接受 `libraries/` 根路径，拒绝绝对路径、路径穿越、反斜杠路径和超体积条目；预置失败会让 `DeployServerProbe` 返回明确错误，避免慢网/离线首启时才暴露依赖下载问题（FR-114）。

每实例系统分配一个 probe 端口（默认 29940 段，同节点唯一）；config.yml 仅开启 `/metrics`、绑定 `127.0.0.1`、监听分配端口。
Worker 抓取链路完全在本机回环、无对外网络面、无 token；依赖缓存 zip 仅接受 `libraries/` 根路径，拒绝绝对路径、路径穿越、反斜杠路径和超体积条目：

```
provision → CP DeployServerProbe(jar+config+libraries_zip)
          → Worker 写 plugins/ServerProbe.jar + plugins/ServerProbe/config.yml + libraries/**
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
- 鉴权（实例级 token，用 **WS 令牌密钥**签发/校验，FR-275/ADR-061——不再复用签用户会话的 `jwt.secret`）：CP 为实例签发 HS256 token（claims `instanceId`+`scope=plugin-bridge`，长 TTL 等效实例生命周期），随探针 config 的 `bridge:` 段下发；Worker 校验**签名 + `scope==plugin-bridge` + token 内 `instanceId == query.instance`** 后建会话，仅握手校验一次。密钥轮换会使已下发探针 token 失效，需经 FR-068 在线更新/重建服重发探针配置。
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

> 地基阶段（FR-065）打通通道层（会话/握手/心跳/connected·disconnected 冒泡 + proto 一次铺齐）；实时玩家事件采集（FR-066）已落地（见上）；治理执行与 RCON 退役（FR-067）已落地，在线更新（FR-068）复用本通道、不再改 proto。

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
- **PID 文件恢复**：wrapper 写 `<pidDir>/<uuid>.pid`（JSON：wrapper pid、java pid、socket 地址、instance uuid）。Worker 启动时 `Manager.RecoverDaemonInstances` 扫描 PID 文件，wrapper pid 存活则 reconnect socket 恢复管理，wrapper 已死则清理文件与残留 socket。wrapper 存活但 reconnect 拨号失败时（FR-325 兜底）：有界重试（3 次、间隔 1s/2s/4s 递增，期间保留 PID 文件保证实例仍可发现）；耗尽后按 PID 记录先强杀 wrapper 进程树再补杀 Java 树（`daemon.KillPIDTree`：Windows `taskkill /T /F`、Unix 杀进程组，Java 在 Unix 上自成进程组故须补杀），存活复核确认死透才清 PID 文件与 socket；杀不死（权限等）保留 PID 文件待下次接管扫描再兜底——杜绝孤儿永久失联（真机事故：残留 java 占 Paper `session.lock`）。
- **优雅退出**：daemon 模式下 `Manager.StopAll` 只断开与 wrapper 的连接，不杀游戏服（direct 模式才终止进程）。
- **Worker 重启后 wrapper 存活（FR-341，落地 ADR-003 承诺）**：daemon wrapper 须在 Worker 升级/崩溃/`systemctl restart` 后存活、由恢复的 Worker 经 socket 重连接管（即上条「PID 文件恢复」的 reconnect 分支，而非「清理残留」）。真机验证曾因三处叠加缺陷 wrapper 全被杀、只命中「清理残留」，已各个击破：① wrapper 的 stdout/stderr 是父 Worker 建立的 OS 管道，Worker 死后写 fd 1/2 触发 SIGPIPE、被 Go 运行时对标准流的默认动作终止——wrapper 启动即 `signal.Ignore(SIGPIPE)`（Unix，见 `daemon.IgnoreBrokenPipe`；Windows 无此语义为空操作），改为 EPIPE 丢弃不崩；② systemd worker 单元默认 `KillMode=control-group` 会连坐 SIGKILL cgroup 内经 setsid 脱离的 wrapper——单元改 `KillMode=process`（`install-worker.sh` + CP 内嵌副本，仅向主进程发信号，`install_scripts_test.go` 守护）；③ Worker SIGTERM 的 `grpcServer.GracefulStop()` 因终端/反向隧道/日志长连接永不收敛而挂起至 `TimeoutStopSec`(默认 90s) 才被 SIGKILL、拖延甚至跳过 `StopAll` 的优雅断开——改为 5s 上限、超时强制 `Stop()`。删除运行中实例仍强杀两棵进程树（上条 FR-310），与「重启存活」正交。

#### docker 容器化实例生命周期（ADR-019，FR-078）

- **管理方式**：`dockerStrategy`（`process/docker.go`）作为 `IProcessCommand` 第三种实现，Worker 经本机 Docker Engine API（`github.com/docker/docker/client`，`FromEnv` 自动发现守护进程）管理容器，不叠 daemon wrapper（隔离由 Docker 守护进程提供）。CP 不直连 Docker，所有容器/镜像操作经 gRPC 委托 Worker。
- **容器模型**：一个实例 ⇄ 一个容器，命名 `jianmanager-<uuid>`；`tty=false` + 三路 attach（stdin/stdout/stderr）。`Start` 前若本地缺镜像则 `ImagePull` 拉取，随后 `ContainerCreate`→`ContainerAttach`→`ContainerStart`。
- **工作目录/端口**：系统分配的实例工作目录（ADR-010 数据根的宿主绝对路径）bind-mount 到容器 `/data`，使文件/备份/配置走同一套宿主路径；端口经 `PortBindings` 把容器内端口（MC 约定 25565）发布到宿主端口（FR-032 端口池分配），不引入新网络面。
- **资源限额（FR-079）**：CP 在实例模型/API 中持久化 `cpuLimit`（核数）、`memLimitMb`、`diskLimitMb`，创建/编辑时 `0`、负值或留空均归一化为不限制；启动下发经 `CreateInstance` proto 传给 Worker。Worker `dockerStrategy` 只把 CPU / 内存写进真实 Docker `HostConfig.NanoCPUs` / `HostConfig.Memory`，Docker stats 优先按容器 cgroup 口径回报 CPU% / 内存实际值 / 内存上限。磁盘限额在当前 bind-mount 工作目录模型下无法可靠强制，故仅持久化与前端展示，不向 `HostConfig.StorageOpt` 假装注入。
- **stdio**：容器多路复用输出经 `stdcopy.StdCopy` 解复用为 stdout/stderr 路由到 `onOutput`（→ WS 终端 + 日志采集 FR-049）；终端输入与优雅停止命令经 attach 连接写入容器 stdin。
- **状态机/重启**：容器退出由 `ContainerWait` 异步监听，非正常退出回写 CRASHED 并触发指数退避重启（与 direct 策略一致，统一在 Manager 层记账）。`Stop` 先经 stdin 下发停止命令再 `ContainerStop`（宽限期后 SIGKILL）；`Kill` 用 `ContainerKill`+`ContainerRemove` 确保端口/卷彻底释放。
- **JDK**：docker 模式不注入宿主 JDK（JAVA_HOME/PATH），JDK 随镜像提供（ADR-008 的 JDK 注入对 docker 不适用）。

### 6.4 Bot Worker IPC

```
Go → Node.js (stdin, JSON 行):
  {"cmd":"create-bots","bots":[{"id":"b1","behavior":"idle","behaviorConfig":{...}}]}
  {"cmd":"stop-bots","botIds":[...]}
  {"cmd":"set-behavior","botId":"b1","behavior":"follow","target":"player","config":{...}}
  {"cmd":"send-command","botId":"b1","command":"say hello"}
  {"cmd":"run-script","scriptId":"s1","botIds":["b1"],"steps":[...]}
  {"cmd":"stop-script","scriptId":"s1"}

Node.js → Go (stdout, JSON 行):
  {"evt":"worker-ready"}
  {"evt":"heartbeat","seq":1,"timestamp":...}
  {"evt":"bot-state","bots":[...]}
  {"evt":"bot-event","botId":"b1","type":"chat","data":{...}}
  {"evt":"bot-error","botId":"b1","error":"ECONNREFUSED"}
  {"evt":"script-progress","scriptId":"s1","status":"running","progress":50}
```

Bot 压测 YAML 编排（FR-274）保持单一解析点：Control Plane 接收 JSON 请求体中的 `orchestrationYaml`，负责 YAML 解析、语义校验、原文持久化与 `orchestrationSummary` 摘要生成；启动会话时将规范化后的阶段、循环、错峰和 custom steps 序列化进既有 gRPC `behavior_config` 字段，并把 Bot 行为置为 `orchestrated`。Worker Node 不解析 YAML，也不重建编排语义，只把 `behavior_config` 透传到 bot-worker 的 IPC `behaviorConfig`。bot-worker 的 `orchestrated` 行为按 `startDelayMs` 错峰启动、按阶段切换内部 `idle/follow/patrol/guard/custom` 行为，并通过 `bot-event` 上报 `orchestration-phase` 事件供真实环境验收确认。

### 6.5 客户端 OTA 公网分发端点（玩家 updater ↔ Control Plane，FR-087 / ADR-022/023）

Control Plane 新增一类**面向玩家公网**的 HTTP 分发端点（客户端 OTA 更新器拉取，非浏览器）：

- **消费端点（玩家，`X-Client-Key` 拉取密钥鉴权）**：`GET /client-channels/:id/manifest`（latest manifest，FR-256 起去签名不再验签，见 [ADR-054](../adr/054-updater-arch-simplification.md)，ETag/304）、`GET /client-channels/:id/updater-core`（楔子拉取 core 版本信息，FR-259）、`GET /client-artifacts/:sha256`（client-file 制品内容寻址下载，`http.ServeContent` 支持 Range/206）、`POST /client-channels/:id/telemetry/heartbeat`（FR-265 启动心跳，写运行态不写更新结果）。挂公网 `api` 组（仅限流、无 JWT）。
- **发布端点（运营，JWT 平台管理员，与 FR-086 频道管理同组）**：`POST /client-channels/:id/files`（上传制品入 FR-045 制品库 type=client-file）、大文件分块上传 `POST/PUT/DELETE /client-channels/:id/uploads/...`（FR-251，init→chunk→complete→abort，喂同一 CAS）、上传增效 `POST /client-channels/:id/files/precheck` + `POST /client-channels/:id/files/batch`（FR-346，批量 sha256 秒传预查命中免传 + ≤8MiB 小文件聚合上传，落同一 CAS；上限 500 hash/次、200 文件·32MiB/批）、`POST /client-channels/:id/versions`（发布版本、单调递增、切 latest 指针）、`POST /client-channels/:id/updater-core/versions`（手动上传 updater-core.jar hotfix，归档为 client-updater-core，可立即选为频道 core）。

**发布编排前端流程（`ClientPublishPage`，FR-191 独立路由页 → FR-250 延迟批量上传 → FR-346 增效）**：选文件/拖拽（散文件 + **文件夹** `webkitGetAsEntry` 递归保相对路径 + zip 前端 fflate 解包 + `webkitdirectory` 选择器）后文件以浏览器内 `File` **本地暂存**（草稿仅 File + name/size/相对 path，**无 sha256**）；文件树/路径/sync/platform 编排、删除全在本地零网络；点「发布」才批量上传，经 `lib/efficientUpload.ts` 编排（FR-346）：本地 name+size 近似去重 → 串行 WebCrypto 算原始内容 sha256（≤256MiB 者；超限直接分块不预查）→ 分批秒传预查（命中免传直接引用既有制品；预查失败降级全量上传不阻断）→ miss ≤8MiB 贪心装箱走聚合端点、>8MiB 走 FR-251 `uploadFileChunked`（complete 顺带 expectedSha256 强校验）→ **并发 4** 任务池 + 单调进度聚合（hashing/uploading 双阶段、不倒退不 NaN、可取消、失败停批保草稿可重试）→ 得各 sha256/md5/size 组 `ManifestFile[]` → `POST .../versions`。**选文件/拖拽/发布前删除的文件从不上传**（省带宽）；纯编排逻辑抽在 `lib/client-publish-wizard.ts` 与 `lib/clientUploadPlan.ts`（装箱/并发池/进度聚合/hash，纯函数可测）。上传 codec=none（本期不压缩）。

**鉴权与信任分层（ADR-022/023；信任模型见 [ADR-054](../adr/054-updater-arch-simplification.md)）**：拉取密钥**半公开**（随整包分发必泄露），仅作鉴权路由 + 吊销、**不作内容可信依据**；内容可信靠 **HTTPS + 拉取密钥鉴权 + sha256 完整性校验**（FR-256 起去掉 manifest Ed25519 验签——私钥在服务器上验签形同虚设，推翻 ADR-022/053）。消费端点与运营浏览器 JWT 入口、发布端点**物理隔离**；L7 防护（限流以 IP 为主）见 ADR-023。manifest 格式见 `docs/specs/client-distribution/contract.md`。**拉取密钥可查看 / 永久使用（FR-192/ADR-044）**：拉取密钥**发出后永久使用**（随整合包分发、所有后续更新都依赖它）。鉴权仍只用 `key_hash` 比对，另存 AES-256-GCM 可逆加密副本 `key_enc`（主密钥 env 注入优先，未配则自动生成并持久化，失败时降级为不可查看但不阻断鉴权），平台管理员经 `GET .../keys/:keyId/reveal`（+ 审计 `client_key.reveal`）查看明文。运营操作面：创建可填自定义值（留空自动生成），`PUT .../keys/:keyId` 改值/名称（改值重算 `key_hash` + 重写 `key_enc`，审计 `client_key.update`）；**不提供轮换**（换 key 会使已分发客户端断更，与永久使用矛盾）；吊销保留并强警告。可查看与拉取密钥「半公开、非信任根」的真实信任级一致。

**L7 应用层防护（FR-096 + FR-264，见 ADR-023）**：消费端点（manifest / updater-core / 制品 / security hello）与运营浏览器 JWT 入口隔离，并叠加两层防护。第一层 `ClientDistGuard` 提供 IP 黑白名单（`client_ip_rules`，deny 优先、有 allow 即白名单模式）、per-IP 令牌桶限流与全局并发信号量，命中拒 403/429，内存计数器经 `GET /client-dist/protection-stats` 可观测。第二层 `ClientDistSecurityService` 提供单节点源站安全防护：IP 临时封禁（`client_protection_actions`，`status=active|expired|canceled`，命中先于密钥校验返回 `IP_TEMP_BLOCKED` + `Retry-After`）、per-key / per-channel 限速、制品下载并发与字节配额、key 状态机（`normal / observe / throttled / suspended / revoked`）、频道保护模式（只能降速 / 降级，不封禁频道）和制品授权收紧（只能拉所属频道 latest/回滚窗口/选定 updater-core 引用的 sha，越权返回 `ARTIFACT_NOT_ALLOWED`）。Range 下载仍由 `http.ServeContent` 支持 206，但拒绝 multi-range，小 Range 会进入风险事件。缓存即防护（ETag/304 + 内容寻址强缓存，CDN 前置）。**L3/L4 容量型 DDoS 靠 CDN/Anycast/云清洗，不在 JM**。

**启动安全画像与客户端分发安全（FR-264）**：updater-core 启动早期调用 `POST /client-security/hello`，body 上报 `playerName`（来自 `jm-updater.json`，承认可伪造，仅作粗略参考）、`machineId`、`installId`、频道、core/wedge/manifest 版本与 OS/Java/launcher/locale/timezone/memoryTier 等粗粒度环境特征。CP 写入 `client_security_hellos` 明细，并按 `(channel_id, machine_id, install_id)` upsert `client_security_profiles`；非法玩家名等写 `client_security_risk_events`。后续遥测与心跳不强制携带 `X-Player-Name`，只需稳定标识 `X-Machine-Id` + `X-Install-Id` + 拉取密钥，CP 写入 `client_telemetry` / `client_runtime_states` 时优先兼容 header，缺省从最新安全画像反查玩家名。管理台独立 `/client-dist-security` 页面命名为「客户端分发安全」，不触碰发布页，消费 `/client-dist/security/*` 聚合端点展示安全总览、异常请求、全量日志详情、客户端画像、IP/玩家剖析、封禁与降级管理、安全分组；隐私告知不再作为独立 Tab 展示。

### 6.6 客户端 OTA 更新器（玩家侧两件套纯 JVM jar，`client-updater/`，FR-089/090/091 / ADR-021）

启动器经 `-javaagent:wedge.jar=<gameDir>` 注入。**楔子（wedge，Java 8，~30KB 稳定件随基础包分发，代码冻结见 [ADR-054](../adr/054-updater-arch-simplification.md) §4）** premain 自定位、读同目录 `jm-updater.json`、**gradle-wrapper 模式**：只接受 API 根 `endpoint`（如 `/api/v1`），据 `endpoint + channel` 自动拼接 updater-core 端点并拉取 updater-core jar（JDK 原生 `HttpURLConnection` + `MessageDigest` SHA-256 校验，本地 `.jm-updater/core/` 保留 3 版用于回滚）→ 以独立 `URLClassLoader` **内存加载** selected core（不锁原 jar）→ 反射 `Core.run(ctx)`（入口签名冻结 `Core.run(Map<String,String>)`，ctx 含 `configJson` 原文透传供 core 后续扩展解析不改楔子）→ 同步等待 + 超时，全程 **fail-open**（任何异常都放行游戏）。楔子**永不接触 manifest**。**updater-core（Java 8，兼容低版本游戏 JVM；fat jar 自含 zstd-jni，~2MB 去掉 BouncyCastle）** 拉 manifest（不再验签）→ 文件级 reconcile（增量/减量、托管区/玩家区隔离、**流式下载 + sha256 完整性校验 + HTTP Range 断点续传**，FR-257；FR-098 起本地旧文件 hash 命中时优先应用 manifest 可选 zstd patch，失败回退完整 artifact）→ 端点不可达 **fail-static** 带本地版本放行。HTTP 用 `HttpURLConnection`（Java 8 无 `java.net.http`）。两件套包名 `top.wcpe.mc.jm.updater.{wedge,core}`。

**清理范围 manifest 字段（FR-255，增强 FR-191/088）**：manifest `managedDirs`（托管/自动清理目录）支持嵌套路径串（如 `config/foo`，客户端前缀匹配）；含哨兵 `"*"` 时语义 = **清空整个 gameDir**——删除清单未列的一切，**除**内置玩家区安全清单 `PLAYER_ZONE`（`saves/ screenshots/ logs/ crash-reports/ options.txt` 等纵深防御永不删）+ 运营自定义追加排除 `cleanExclude`（`string[]`，命中前缀永不删，叠加在 `PLAYER_ZONE` 之上）。`cleanExclude` 空则省略（`omitempty`，老 manifest JSON 不变、向后兼容，schemaVersion 维持 1）。删除判定：`isUnderManaged && !isPlayerZone && !isExcluded` 才删。发布页 meta 步提供目录树勾选（由草稿文件派生）+ clean-all 开关 + 自定义排除标签输入；clean-all 发布强制 `DangerConfirm` 二次确认。

**楔子自动拉 core + N-1 回退（FR-258，取代 FR-091 core 自更新；见 [ADR-054](../adr/054-updater-arch-simplification.md) §3）**：整合包只带 ~30KB wedge.jar，首次启动楔子按 API 根 `endpoint` 自动查询 updater-core 端点（CP 返回选定分发信息 `{version,sha256,downloadUrl,size}`；`version` 为频道级递增分发版本，`sha256` 为实际 core jar）→ 下载 core jar → SHA-256 校验 → 存入 `.jm-updater/core/<sha>.jar` 标记 pending；`CoreSelector.select` 状态机据 `<gameDir>/.jm-updater/core/state.properties`（`selected/prev/pending/tried`，wedge↔core 共享格式）跑选择：首次加载 pending=**trial** 并起 **boot-confirm 看门狗**（daemon 存活 `bootConfirmSec` 即建 `pending.confirmed`）；下次启动若 pending 已 tried 且无 confirmed（判定上次崩溃/早退）→ **回退 N-1**（弃 pending、留 selected）；已确认 → **promote**（selected=pending、旧 selected 降 N-1）。本地保留最近 3 个 core jar 超出自动清理最老。**运营面板一键切换选定 core 版本**（FR-259）：CP 归档多版本、运营切「Core 版本」Tab 选定 → 服务端维护频道级递增分发版本；客户端下次启动按 endpoint 自动查询，看到更大的 `version` 后下载目标 `sha256`，因此切回旧归档 jar 回滚也会生效。`failedVersion` 记录某版 trial 失败后不再重试（避免 boot-loop）。运营整体回滚见 FR-088（服务端以更高 version 重发）。

**客户端信任模型：HTTPS + 拉取密钥鉴权（FR-256，见 [ADR-054](../adr/054-updater-arch-simplification.md)，推翻 ADR-022/053）**：FR-256 起去掉 manifest Ed25519 验签——旧方案把签名私钥放在服务器侧，服务器被攻破即可能伪造签名，验签付出的复杂度（BouncyCastle 14MB、密钥管理、配置面）换来的安全保障在实际部署下不成立。信任模型简化为：**拉取密钥鉴权**（防外人乱拉、区分频道、可吊销，ADR-022 决策 1 不变）+ **HTTPS 传输**（防中间人）+ **sha256 完整性校验**（防下载损坏，非信任校验）。`jm-updater.json` 的 `signPublicKey`/`signKeyId` 字段废弃。`GET /client-dist/sign-key` 端点删除（FR-248 面板公钥展示随验签一起作废）。`GET /client-channels/:id/updater-config` 保留但只返回 API 根 `endpoint`（不再含签名公钥，也不再含 `coreEndpoint` 配置字段）。updater-core.jar 从 ~16MB（含 BC）降到 ~2MB。

**updater-core 归档多版本 + 运营面板可选（服务端，FR-259，见 [ADR-054](../adr/054-updater-arch-simplification.md) §3，修订 ADR-045）**：CP 每次 `make embed-client-updater` 时新 core jar 入库归档为 `client-updater-core` 制品类型（多版本不覆盖，归档版本号递增），而非 ADR-045 的「单版本内嵌自动驱动」；平台管理员也可在频道「Core 版本」Tab 手动上传 updater-core.jar hotfix，经 `POST /client-channels/:id/updater-core/versions` 归档为同一制品类型，并可立即选为该频道版本。updater-core.jar 构建时内嵌 `META-INF/jm-updater-core.properties` 与 Manifest 元信息（`version/gitCommit/dirty/buildTime`）；CP 归档时优先读取并写入资产 metadata，Core 版本页展示 `displayVersion` 与 commit/dirty，缺少元信息的紧急 hotfix jar 仍可直接上传。运营经频道详情「Core 版本」Tab 列出归档版本、一键切换频道选定版本（`ClientChannel.SelectedCoreSHA256`），页面同时展示最新归档与当前选定状态。updater-core 查询端点返回频道选定 `sha256` 及 `ClientChannel.SelectedCoreVersion` 分发版本供楔子自动拉取；分发版本与归档版本解耦，回滚旧 `sha256` 时仍递增。manifest `agent.core` 段保留但信息来源改为该查询端点（不再驱动 core 自更新）。`/client-artifacts/:sha256` 端点扩展支持 `client-updater-core` 类型分发 core jar。为避免误删导致楔子拉不到 core，制品库删除会保护内置来源 `client-updater-core` 与任何频道当前选定的 core 归档（返回 `ASSET_IN_USE`）。楔子（wedge）仍内嵌单版本固定（~30KB，代码冻结）。接入指引（FR-107）只读展示内嵌更新器版本。

**机器码身份（FR-092）**：updater 生成稳定、跨平台、不可逆的机器码（多硬件/环境特征组合 SHA-256，首次计算后持久化于 `<userHome>/.jm-updater/machine-id` 保稳定容错），随 manifest/制品请求经 `X-Machine-Id` 携带；CP 在 manifest 拉取时 best-effort 登记入 `client_machines`。**客户端生成、不可信**——仅统计 + 辅助限流，不作信任/授权依据（限流主键为 IP，ADR-023）。

**`.jmpack` 容器（FR-097，已废弃）**：FR-256 起连同验签一起删除——散文件下载更优（见 [ADR-054](../adr/054-updater-arch-simplification.md)）。`JmPack.java` / `JmPackService` / `POST .../pack` 端点均已删。

**遥测（FR-094）**：updater reconcile 后 best-effort `POST /client-telemetry`（拉取密钥 + `X-Machine-Id` + `X-Install-Id`，兼容可选 `X-Player-Name`，**202 不阻塞**）上报结果/版本/环境(os/java/启动器粗粒度)/耗时/bootSuccess；**隐私 opt-out**（`jm-updater.json` `telemetry:false` 关闭），仅环境粗粒度 + 不可逆机器码 + 通过安全画像反查的客户端声明玩家名，不收集敏感数据。CP 落 `client_telemetry`（明细短保留）+ `client_telemetry_daily`（按 result 日聚合），供 FR-095 成功率/回退率。端点挂 FR-096 守卫。

**统计后台（FR-095）**：`GET /client-dist/stats?channelId=&days=`（平台管理员）**只读聚合** FR-093/092 请求数据（不引入新表）——下载量趋势/版本分布（`client_dist_daily`）、请求成功/失败分布、活跃机器码数/来源 IP Top10（`client_dist_events` 近窗）。FR-265 起，频道工作台「统计」Tab 只看分发 HTTP 请求历史统计，不再展示更新成功率、运行版本、平台或滞后分布。

**观测数据底座（FR-217，见 ADR-049；FR-265 修订口径）**：`ClientDistObservabilityService` 后台 goroutine（每 10min，复用 scheduler 式 ticker，同 `MetricService`）**离线**把保留窗内的 `client_dist_events`+`client_telemetry` 卷积为按**频道×小时桶**的快照 `client_dist_snapshots`（幂等 upsert、重算近 48h 完结桶纳延迟明细、单档小时桶留 ≥180d 自清），与玩家热路径的写时聚合（`*_daily`）**解耦**。查询端点 `GET /client-dist/observability`（平台管理员 + 审计）返**跨频道/单频道**时序 + 区间分布聚合 + 汇总率；**machineId 去重口径**：桶内精确计数，跨区间在明细保留窗(14d)内回查明细做精确去重（`activeMachinesExact=true`）、窗外退化为各桶人次求和近似（`false`），不谎报精确独立数。聚合落 CP（架构不变量：Worker 不直连 DB）。

**客户端分发观测四 Tab（FR-265）**：管理台 `/client-dist-monitor` 标题统一为「客户端分发观测」，同页拆 **统计 / 监控 / 日志 / 客户端** 四个 Tab，并建立清晰数据边界：统计 / 监控 / 日志只消费 `client_dist_events` 与 `client_dist_daily`，分别看请求历史统计、近实时健康度和脱敏明细；客户端 Tab 消费 `client_runtime_states` 最新心跳与 `client_telemetry` 更新结果，展示运行版本 / core 版本 / 平台 / 启动器 / 滞后分布与更新结果趋势。启动心跳 `POST /client-channels/:id/telemetry/heartbeat` 只 upsert 运行态，不写 `client_telemetry`，因此不会污染更新成功率；页面文案只称「近 5 分钟启动客户端 / 今日启动客户端」，不承诺真实在线。

**接入指引 + 内嵌更新器 jar（FR-107/259）**：CP 经 `go:embed` 内嵌 wedge.jar（~30KB）+ updater-core.jar（`make embed-client-updater` 注入，CP 启动时自动归档入库供楔子拉取），经平台管理员端点 `GET /client-dist/updater-jars[/:component]` 下载 wedge（管理面 JWT，不用拉取密钥）。管理台频道详情「接入指引」Tab 面向**运营方**一页拿齐：下载 wedge.jar + 该频道**专属可复制** `jm-updater.json`（channel/API 根 endpoint/密钥占位，FR-259 起不再含签名公钥与 coreEndpoint 配置字段；可从频道密钥列表选择一把 revealable 密钥并自动填入明文，复制与下载同源）+ 启动器 `-javaagent:jm-updater\wedge.jar` 参数（相对路径推荐）+ 放置步骤 + 行为说明（fail-static/fail-open/进度窗/与 authlib-injector 共存）。updater-core 不在整合包内——楔子首次启动按 endpoint 自动拼接端点并拉取，运营可在「Core 版本」Tab 切换回滚。纯运营面、不改 OTA 协议/manifest/客户端 jar。

### 6.7 jmctl 紧急控制台（本机直连守护进程 socket，`apps/jmctl/`，FR-184 / ADR-041）

当 Control Plane 与 Worker Node 同时不可用（崩溃/升级）时，daemon wrapper（§6.3，ADR-003）仍托管着运行中的游戏服进程，但**只有 Worker 会说守护进程帧协议**——运营者够不到那台进程（看不到输出、发不了指令、无法优雅停）。`jmctl` 是**独立轻量二进制**（控制面/Worker 之外的第三个 Go 入口），**绕过整个栈、纯本机、依赖极少**地直连守护进程 socket，做「最后一公里」应急运维。

- **依赖边界**：只链 `internal/worker/daemon`（frame + conn + pid_file）与 stdlib-only 的 `internal/platform/dataroot`，**不引入** gRPC / 数据库 / Worker service / CP；编译产物约 3.6MB。依赖闭包经 `go list -deps ./apps/jmctl` 校验无重量级传递依赖（保证 daemon 包可独立链接，落地无需把帧协议下沉为更中立的包）。
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
Instance ──1:N──▶ Backup / Schedule / Bot / BotStressSession
BotStressSession ──1:N──▶ Bot (stress_session_id, 可空)
Backup ──N:1──▶ Backup (parent_id, 增量备份链, V2)
Backup ──N:1──▶ BackupStorage (storage_id, 远程存储位置, V2)
Instance(proxy) ──M:N──▶ Instance(backend)   # V2 ServerRegistration: alias/priority/forced_host
Network ──M:N──▶ Instance                    # V2 NetworkMember（非独占软标签）
Node ──1:N──▶ NodeJDK                         # V2
Node ──1:N──▶ NodeRuntime                     # V2 FR-298（非 JDK 运行时：nodejs/python 预留）
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
| nodes | uuid(UNIQUE，身份锚定键，ADR-039), name(活跃唯一：部分唯一索引 `uniq_nodes_name_active` WHERE deleted_at IS NULL，软删可释放名), host, grpc_port, ws_port, secret, status(0/1/2), maintenance(bool, cordon 维护模式，与在线/离线正交), os, arch, cpu_cores, memory_mb, disk_total_mb, load_avg1(V2, 系统负载, FR-062), proxy_mode(inherit/custom, 出站代理模式, 默认 inherit, FR-185/ADR-043), proxy_url(节点自定义代理, 仅 custom, 含凭据/API 脱敏), proxy_no_proxy(节点自定义免代理列表, 仅 custom), last_heartbeat, runtime_synced_at(V2, FR-301, 上次运行时库存从 Worker 同步成功时间——JDK syncFromWorker 成功即刷新, NULL=从未同步, 运行时资产页「上次同步」锚点), deleted_at |
| instances | uuid, node_id(FK), name, type, role(proxy/backend/universal, V2), process_type, status, start_command, work_dir(系统分配), env_vars(JSON), auto_start, auto_restart, jdk_id(FK, V2), launch_spec(JSON: jvm_args/core_jar/args/omit_nogui, V2), image(docker 模式镜像引用, FR-078), container_id(docker 模式最近容器 ID), cpu_limit/mem_limit_mb/disk_limit_mb(docker 资源限额，0=不限制，磁盘仅展示), forwarding_secret(V2, Velocity 转发), proxy_online_mode(V2, 代理正版校验), server_port/query_port, probe_port(V2, ServerProbe /metrics 端口, 29940 段), mc_*, tags(JSON), work_dir_in_place(FR-302, 就地导入标记=工作目录在托管区外、删除不删原目录) |
| group_instances | group_id, instance_id(UNIQUE) |
| instance_group_nodes (V2, FR-165) | uuid, name, parent_id(自引用 FK, NULL=根), sort, deleted_at（实例组织分组树节点，邻接表表达多级嵌套；正交于用户组/网络群组，仅组织归类，ADR-033）；INDEX(parent_id) |
| instance_group_members (V2, FR-165) | group_id(FK instance_group_nodes), instance_id(FK)；UNIQUE(group_id, instance_id)（实例-组织分组 M:N，一实例可属多组；删组只解绑、不删实例） |
| instance_crash_snapshots (FR-313) | instance_id(FK, INDEX), occurred_at, exit_code(无法获知=-1), signal(Unix 信号名；Windows/非信号退出为空), duration_ms, tail_output(TEXT, ≤200 行/64KB Worker 侧截取)（进程非正常退出现场；Worker 经 ReportCrashSnapshot 上报，写入同事务按实例滚动只留最近 5 条，删实例级联清） |
| bot_stress_sessions | uuid, instance_id(FK), name, name_prefix, status(pending/running/stopped/error), bot_count, behavior, config(JSON), orchestration_yaml(TEXT), orchestration_summary(JSON), succeeded, failed, last_error, started_at, ended_at |
| bots | uuid, instance_id(FK), stress_session_id(FK，可空), name, status, config(JSON), behavior, worker_id |
| backups | uuid, instance_id(FK), name, file_path, file_size_mb, type(0/1), mode(0 全量/1 增量, V2), status(0/1/2/3), parent_id(FK self, 备份链, V2), manifest(JSON 文件清单, V2), storage_id(FK, V2), storage_key(远程对象键, V2), checksum/checksum_algo(归档完整性, FR-171) |
| process_metric_snapshots | node_uuid, instance_uuid, pid, name, cpu_percent, rss_bytes, read_bytes_per_sec, write_bytes_per_sec, user, command_summary(截断脱敏), sampled_at（受管实例进程 TOPN 短期快照，按 48h TTL 清理，FR-170） |
| backup_storages | name(UNIQUE), type(local/s3/sftp/webdav), endpoint, bucket, region, prefix, access_key_env(${ENV_VAR}), secret_key_env(${ENV_VAR}), use_ssl, last_test_at/last_test_ok/last_test_message（FR-057/FR-152；backupCount/usedBytes 从 backups 聚合） |
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
| audit_logs | user_id, action, target_type, target_id, detail(JSON), ip, failed(bool，零值=未失败), error(varchar 512)（FR-321：失败操作也留痕并带响应错误内容；FR-172 分页 envelope 走服务端 total；NDJSON 导出按批次流式输出白名单字段，`audit.export` detail 仅记录格式、过滤摘要与成功/失败状态） |
| networks (V2) | uuid, name, description（非独占软标签） |
| network_members (V2) | network_id(FK), instance_id(FK)（M:N，一个子服可属多群组） |
| server_registrations (V2) | proxy_id(FK), backend_id(FK), alias, priority, forced_host, restricted, enabled；UNIQUE(proxy_id, alias) |
| node_jdks (V2) | node_id(FK), vendor, major_version, version, arch, path, managed(下载/登记) |
| node_runtimes (V2, FR-298) | node_id(FK), type(varchar16: nodejs/python 预留，jdk 不落本表), name(展示名如 "Node.js 22"), version, major, arch, path, managed；UNIQUE(node_id,type,path)（节点运行时库非 JDK 承载表：JDK 沿用 node_jdks 不迁移、实例外键零变更；读侧与 node_jdks 拼统一 Runtime 视图、写侧各走各表） |
| node_pm_configs (V2, FR-306) | node_id(UNIQUE), pm(varchar16 default npm: npm/pnpm/yarn), registries(text, JSON 数组 [{name,url,scope,token}])（节点包管理器配置单例：token 入库明文、API 出参与日志脱敏（掩码回传=不改），Worker 侧落托管 .npmrc + corepack enable） |
| instance_config_versions (V2) | instance_id(FK), file_path, content, author, created_at |
| file_versions (V2) | instance_id(FK), file_path, content_hash, content(base64,二进制安全), size, author_id, rollback_of_version_id, created_at；INDEX(instance_id,file_path)（FR-051 通用文件改前快照） |
| assets | type(core/plugin/image/video/archive/blob/client-file/client-pack/client-core/client-updater-core), name, version, filename, sha256(寻址+去重键), md5, size, content_type, source_url, metadata(JSON), storage_state(hot/archived/external/**lost**；lost=索引存在但外置对象缺失，FR-349), storage_backend(local/s3), storage_channel_id(FK artifact_storage_channels，0=本地/无渠道；位置由记录自述，FR-347), ref_count, rel_path(相对数据根=跨后端存储键), created_at, last_used_at；UNIQUE(type,sha256) |
| artifact_storage_channels | name(UNIQUE), type(local/s3), endpoint, bucket, region, prefix, access_key_enc/secret_key_enc(AES-256-GCM 可逆加密，复用 FR-192 KeyEncryptor), use_ssl, presign_ttl_seconds(默认 600，[60,3600]), active(全表恰一条，写路径路由开关), builtin(内置「本机存储」不可删不可编辑), last_test_at/last_test_ok/last_test_message（FR-347，见 ADR-073；与备份域 backup_storages 独立） |
| artifact_migrations (FR-348) | task_id(UNIQUE，1:1 关联 tasks), target_channel_id(INDEX), total, migrated, failed, skipped, created_at, updated_at（一次存量迁移任务的登记与实时四计数；计数不塞 Task.Result/Detail，保证失败与中断时仍可精确查询） |
| artifact_migration_failures (FR-348) | task_id(INDEX), asset_id, sha256, filename, size, reason(TEXT), created_at（逐制品失败明细，按任务隔离保留；前端查询最多展示 500 条，总数看 artifact_migrations.failed） |
| artifact_reconcile_runs (FR-349) | channel_id, channel_name(快照), status(running/succeeded/failed), triggered_by(manual/scheduled), started_at/finished_at, index_count/object_count/matched_count/missing_count/orphan_count, error_message, created_at（逐 S3 渠道对账运行记录） |
| artifact_reconcile_diffs (FR-349) | run_id, channel_id, kind(missing/orphan), asset_id/sha256, object_key, size, last_modified, status(open/resolved), resolved_at, resolved_action(marked_lost/cleaned/stale), resolve_error, created_at（差异快照与显式处置状态） |
| artifact_reconcile_settings (FR-349) | id=1, enabled(default true), interval_hours(default 24，[1,720]), next_run_at, updated_at（定期对账单行设置） |
| logs (FR-049) | source(instance/control_plane/worker), level(debug/info/warn/error), instance_id, instance_uuid, node_id, stream(stdout/stderr), message, time；复合索引 (source,time)/(level,time)/(instance_id,time)/(node_id,time)，关键字检索走 message 列谓词 |
| ban_records (V2) | uuid, player_name, reason, scope(network/instance/global), scope_id, operator_id(FK), active, created_at, unbanned_at（玩家封禁台账，FR-054；保留历史治理记录，解封置 active=false 保留历史） |
| platform_settings (V2) | key(PK), value, updated_at（平台配置 DB 覆盖层，仅存被显式覆盖的白名单键；生效优先级 DB 覆盖 > 环境变量 > YAML 默认，FR-063/ADR-015）。network 类键 `proxy.url`（敏感脱敏）/`proxy.no_proxy` 为 CP 全局出站代理（FR-185/ADR-043），保存即重建 CP 出站持有者并作为各节点默认代理（优先级 settings DB > control-plane.yml > env） |
| self_update_check_caches (FR-186) | id(固定=1, 单行覆盖), result_json(上次成功 CheckResult 的 JSON blob, 整段存不拆字段以免随 CheckResult 演进迁移、反序列化缺字段降级), source(更新源标识冗余, 诊断用), checked_at（系统更新页检查结果服务端缓存；GET /self-update/check 返此缓存不触发 live、refresh 成功后 upsert 覆盖、刷新失败不清，进页即显 + 后台静默刷新，增强 FR-182） |
| tasks (V2, FR-183) | task_id(UNIQUE, UUID 业务键), node_id, instance_id(FR-319 provision 任务关联实例，启动闸据此拦在途搭建), kind(jdk_install/runtime_install/pkg_install/provision/import/clone/backup_create/backup_restore/artifact_migrate), state(pending/running/succeeded/failed/canceled), progress(0~100), title, detail, error, result(成功结果 JSON), created_by(发起人/归属), created_at, updated_at（全局任务中心：长任务进度经心跳 upsert 或 CP 侧直写，终态触发副作用，ADR-040）。**长操作任务化（FR-323）**：CP 侧长操作（一键搭建 / 导入 migrate 搬迁 / 克隆拷贝 / 备份创建恢复）经共享底座 `TaskService.RunAsync`（CreateTask→后台 goroutine→SetStage 阶段进度→MarkSucceeded/Failed→终态站内信，业务副作用如 statusReason/Backup record 状态由 work 自负）统一纳入任务中心——提交秒回 `{…, taskId}` 不阻塞（搬迁/拷贝/打包可数十分钟），进度/失败在任务中心可见；就地导入（O(1) 无拷贝）保持同步。**制品存量迁移（FR-348）**使用 `kind=artifact_migrate/node_id=0` 的 CP 本地任务；因发起时必须同步落 `artifact_migrations` 登记，采用手写 `CreateTask→建登记→goroutine→MarkRunning/终态` 生命周期而不走 `RunAsync` |
| task_logs (V2, FR-183) | task_id, seq, line, ts；UNIQUE(task_id, seq)（任务滚动日志；心跳带绝对序号，幂等追加去重） |
| notifications (V2, FR-183) | user_id(收件人), level(info/success/warning/error), title, body, task_id(关联任务,可空), read_at(NULL=未读), created_at（站内信：任务完成/失败投递，归属隔离）。**统一通知中心（FR-216，ADR-048）只在读侧聚合**：`NotificationFeedService` 查询时把本表（按用户）+ `alert_events`（全局）合并为一条通知流（`source` 判别 message/alert、级别就近映射），**不新建表、不双写**；写入源不变，标记已读下推各源 |
| client_channels (FR-086/193/259/264) | channel_id(slug, UNIQUE, 对外标识/URL 段), name, description, current_version(latest 内容版本指针，0=未发布，FR-088 编排), pinned_core_version(已废弃：FR-259 起改 selected_core_sha256 驱动 coreEndpoint，本列不再使用、json 不暴露，见 ADR-054 修订 ADR-045), selected_core_sha256(FR-259: 频道选定 updater-core 归档 sha256), selected_core_version(FR-259: 频道级 core 分发版本；楔子只按它递增判断是否下载，回滚旧 sha 时也递增), protection_mode/protection_policy_json/protection_updated_at(FR-264: 频道保护模式，只允许降速/降级), created_at, updated_at, deleted_at（客户端分发频道，每服一个，ADR-022/054） |
| client_pull_keys (FR-086/192/264) | channel_id(所属频道 slug), name, key_hash(明文 SHA-256, UNIQUE, **鉴权依据**), key_enc(明文 AES-256-GCM 可逆加密副本, base64(nonce‖密文), **不序列化给客户端**；FR-192/ADR-044), key_prefix(识别用前缀), revoked, expires_at, last_used_at, security_state/throttle_policy_json/security_note/security_updated_at(FR-264: normal/observe/throttled/suspended/revoked 状态机), created_at, revoked_at（拉取密钥，半公开凭据；鉴权用哈希比对，另存可逆加密副本供管理员查看明文，env 注入加密密钥未配则不写 key_enc、密钥不可查看；吊销或 suspended 即鉴权/拉取受限，ADR-022/ADR-044） |
| client_versions (FR-087/088) | channel_id(所属频道 slug), version(单调递增, UNIQUE(channel_id,version)), files_json(文件清单快照), managed_dirs_json(托管目录), clean_exclude_json(运营自定义排除, FR-255), agent_json(core 段, 可空；其 agent.core 自 FR-259 起由频道选定 core 版本驱动（ADR-054 修订 ADR-045），无选定版本时回退此手填透传/省略), note, created_by, created_at（版本快照，全保留供运营回滚/diff；manifest 即时组装，FR-256 起不再签名，回滚=以更高 version 重发旧内容，ADR-022/054） |
| client_machines (FR-092) | channel_id + machine_id(UNIQUE 组合), hit_count, first_seen, last_seen（客户端机器码登记，manifest 拉取时 best-effort upsert；机器码客户端生成**不可信**，仅统计+辅助限流，不作授权依据，ADR-023） |
| client_dist_events (FR-093/249) | channel_id, machine_id, ip, kind(manifest/artifact), version, artifact_sha, bytes, status, err_code(FR-249 语义错误码，成功空/失败填码，index), duration_ms, created_at（拉取/下载明细，**短保留**+滚动清理；按 IP/机器码/频道/版本/时间/**成功失败(outcome)/错误码**检索；FR-249 起拉取失败含 401 鉴权失败也记录事件，defer 前置到鉴权前捕获最终 status/errCode） |
| client_dist_daily (FR-093) | day + channel_id + version + kind(UNIQUE 组合), requests, bytes（按日聚合，**长保留**、写时增量 upsert；供下载量趋势+版本分布，FR-095） |
| client_ip_rules (FR-096) | cidr, mode(deny/allow), note, created_by, created_at（分发端点 IP 防护规则，运行时可改+入审计；deny 优先、有 allow 即白名单模式，ADR-023） |
| client_telemetry (FR-094) | channel_id, machine_id, player_name, ip, result, from_version, to_version, os, java_version, launcher, duration_ms, boot_success, error, created_at（客户端遥测明细，**短保留**+滚动清理；仅环境粗粒度+不可逆机器码/玩家名，隐私可关；player_name 不可信，仅排障） |
| client_telemetry_daily (FR-094) | day + channel_id + result(UNIQUE 组合), count（遥测按 result 日聚合，**长保留**；供更新成功率/回退率趋势，FR-095） |
| client_security_profiles (FR-264) | channel_id + machine_id + install_id(UNIQUE 组合), player_name/player_name_norm(玩家名仅粗略参考、可伪造), key_id/key_prefix, first_seen/last_seen/last_ip, core_version/wedge_version/manifest_version, os/os_version/arch, java_vendor/java_version/java_arch, launcher/locale/timezone/memory_tier, risk_score/risk_level/protection_state, labels_json（启动安全画像最新态，供防护中心剖析） |
| client_security_hellos (FR-264) | channel_id, machine_id, install_id, player_name, accepted, err_code, ip, key_id/key_prefix, user_agent, payload_json, created_at（updater-core 启动安全画像上报明细；缺必填 400、不落画像最新态） |
| client_security_risk_events (FR-264) | subject_type/subject_value, channel_id, machine_id, install_id, player_name, ip, key_id/key_prefix, rule_code, severity, score_delta, action, reason, detail_json, created_at（风险事件明细，记录非法玩家名、小 Range、异常请求等可解释事件） |
| client_protection_actions (FR-264) | target_type(ip/key/channel), target_value, channel_id, action(temp_block/key_state/channel_protection/…), status(active/expired/canceled), policy_json, reason, auto, expires_at, created_by, created_at/updated_at/canceled_at（IP 临时封禁、key 状态与频道保护动作；active IP 封禁先于密钥校验生效） |
| client_security_groups (FR-264) | name(UNIQUE), kind(manual/rule), target_type, rule_json, action_policy_json, enabled, created_by, created_at/updated_at（防护中心安全分组配置） |
| client_security_counters (FR-264) | scope + key + bucket(UNIQUE), value, updated_at（安全计数/配额桶辅助） |
| client_runtime_states (FR-265) | channel_id + machine_id(UNIQUE 组合), player_name, ip, platform, java_version, launcher, core_version, local_version, first_seen_at, last_heartbeat_at, created_at/updated_at（客户端最新启动运行态；启动心跳只 upsert 此表，不写 client_telemetry；machine_id/player_name 不可信，仅统计/联动筛选） |
| client_dist_snapshots (FR-217/265) | channel_id + bucket_ts(UNIQUE 组合, **频道×小时桶**), manifest_pulls, artifact_pulls, download_bytes, active_machines(桶内 machine_id 去重), version_dist/platform_dist/lag_dist(JSON map), update_total/success/fail_static/rolled_back/error（**观测时序快照**，后台离线把 client_dist_events+client_telemetry 卷积而来，与写时聚合解耦；单档小时桶留 ≥180d；供观测·分发监控页跨频道/平台时序，ADR-049） |
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

SQLite 驱动为 glebarez/sqlite（纯 Go），连接池**收敛为单连接**（`SetMaxOpenConns(1)`，FR-318）：该驱动 COMMIT 因 SQLITE_BUSY 失败时事务保持打开、且不实现 `driver.Validator`/`SessionResetter`，多连接下自身锁竞争会把带打开事务的连接毒化回池（此后抽到即报 `cannot start a transaction within a transaction`，直到重启）；CP 是数据库唯一读写方（见架构不变量「数据所有权」），单连接消除自库锁竞争即消除毒化前提，代价是库访问串行化（SQLite 写本就单写者，规模内可接受）。MySQL 不受此限，池参数走驱动默认。

### 数据库资源管理器（FR-084）

Control Plane 持有数据库唯一读写入口，浏览器与 Worker/Bot 均不直接连接数据库。FR-084 在该边界内新增只读资源管理器：`DBBrowseService` 复用当前进程的 `*gorm.DB`，通过 GORM migrator 枚举表/列，通过白名单校验后的 `db.Table(name)` 执行分页 `SELECT`。表名、排序列、过滤列必须命中元数据白名单；过滤值参数化绑定，不拼接用户输入；列名命中密码、密钥、token、secret、node_secret 等敏感片段时由服务端替换为 `******` 后再返回。该能力无写入、迁移、导出、执行 SQL 入口，REST handler 全部挂平台管理员权限组并返回 401/403 对齐其他平台级资源。

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

> 当前方向（FR-267~272，见 ADR-055/056/057；顶栏贯通 FR-334/ADR-071）：控制台外壳从功能域导航进一步收敛为 **平台 → 节点 → 服务器** 资源主轴，外壳为 **T 型**——整宽顶栏在上（品牌区 + 面包屑 + 搜索、集群状态、任务、通知、账户），其下「侧栏 + 工作区」一行。`JianManager` 品牌固定在顶栏左端品牌区（宽度与侧栏同步、右缘对齐连线），主题入口固定在侧栏底部。左侧导航只放跨服务器或平台级入口（平台首页 / 服务器 / 群组网络 / 观测 / 平台管理）。原页眉节点作用域下拉已随顶栏贯通下线（ADR-071），其 `selectedNodeId` 数据面保留、改由全部服务器页自带节点筛选驱动，仍联动命令面板实例结果与创建实例默认节点。单服能力默认归入 `/instances/:id` 的服务器统一控制台；FR-166 可组合画布、FR-167 超级工作台、FR-168 导播台作为高级拼屏/监看能力保留，不作为单服默认入口。

小屏下桌面侧栏隐藏后由 `MobileConsoleNav` 接管主导航：底部固定显示高频分组，点击分组在底部上方展开同一套 IA 的入口列表，避免手机端失去导航能力。群组网络入口中 `/networks` 使用精确匹配，`/networks/topology` 不再同时点亮「分组管理」。桌面侧栏折叠/展开使用受控宽度动画，抽屉关键帧保持在边界内并以静止态结束，避免越过停止线后回弹。

登录后默认进入「运维控制台」Shell（`DashboardPage`，见 ADR-009 / FR-037 / FR-061）：外壳为 **T 型**——整宽顶栏在上、其下「侧栏 + 工作区」一行（顶栏贯通 FR-334/ADR-071）。下图为早期布局示意（logo 尚画在侧栏顶、含已下线的节点作用域下拉，均以下方正文为准），当前实现已按 FR-267~272 收敛到平台 → 节点 → 服务器主轴、并按 ADR-071 将 logo 抬入整宽顶栏品牌区：

```
┌────────────────┬─────────────────────────────────────────┐
│  JianManager ◧ │ 域›面包屑          [🔎 搜索 ⌘K] 徽标 ✉ 🔔 账户│  ← 全局顶栏（FR-162 / 右对齐 FR-179）
│ ┌────────────┐ ├─────────────────────────────────────────┤
│ │ 总览        │ │                                         │
│ │ ▾ 集群      │ │   工作区                                 │
│ │  节点 实例  │ │   · 点实例 → 该实例终端（单个，xterm）    │
│ │  [全部节点▼]│ │   · 其余导航 → 按路由渲染对应页面          │
│ │  ● Survival│ │   · 未开终端 → 空状态                     │
│ │ ▾ 观测      │ │                                         │
│ │ ▾ 运营      │ │   （侧栏可折叠为 3.5rem 仅图标轨）        │
│ │ ▾ 系统      │ │                                         │
│ │  ·平台与维护│ │                                         │
│ │  ·账户与审计│ │                                         │
│ ├────────────┤ │                                         │
│ │ ●● ☀  主题  │ │  ← 全局主题切换（Jian绿/青绿 + 明暗，FR-164）│
│ │ vX.Y · 许可 │ │                                         │
│ └────────────┘ │                                         │
└────────────────┴─────────────────────────────────────────┘
```

- **左栏（常驻）= 资源主轴侧栏（FR-268，`ConsoleSidebar` / `nav-config.ts`）**：一级分组为 **平台首页 / 服务器 / 群组网络 / 观测 / 平台管理**，分组可展开、激活态使用 A+C Jian 绿主色，高频资源入口在上、平台管理入口沉底。
  - **服务器**组展开 = 全部服务器、跨服玩家、Bot 总览、节点、超级工作台、导播台。服务器选择不依赖常驻实例树，主要走全部服务器页、节点页、命令面板搜索与 `/instances/:id` 深链。
  - **常驻服务器列（FR-293，增强 FR-240，`SidebarServerList`）**：「选择服务器」按钮（`ServerSelector`）下方常驻两区 = 收藏（置顶）+ 最近打开（LRU ≤8，已收藏去重）；行 = 状态点 + 名称（title 含节点名），点击进该服控制台并计入最近，行内星标可收藏/取消。与选择器弹窗共用 localStorage（`server-selector.favorites` / `server-selector.recent`），读写收敛 `components/console/server-selection.ts` 共享 store（模块级订阅 + `useSyncExternalStore`），弹窗、常驻列与直接路由进入实例（`InstanceConsolePage` 记入最近）三路互通；状态点数据走列表内 id 的低频合并查询（60s，复用 `['instances', id]` 同源端点），不为侧栏引入高频轮询；双空显示引导文案，折叠图标轨态不渲染。
  - **群组网络**组展开 = 网络拓扑（`/networks/topology`）与分组管理（`/networks`）。`/networks` 精确匹配，避免拓扑页同时点亮两个入口。
  - **观测 / 平台管理**承载跨服务器能力：监控总览、日志中心、统计分析、客户端分发监控，以及模板、客户端分发、运行时资产、存储、备份仓库、全局备份、任务中心、定时任务、通知中心、用户、用户组、审计、设置、许可、数据库、系统更新等平台级页面。`/players`、`/bots`、`/backups`、`/schedules` 均进入统一导航真源，桌面侧栏、移动导航与命令面板同步可达（FR-272）。
  - **可折叠图标轨（FR-131）**：可折叠为 `3.5rem` 仅域级图标轨（浏览器 100% 缩放、默认根字号下为 **56 CSS px**；设备像素截图不替代 CSS 宽度口径）。hover tooltip 显 label；折叠态域图标直接导航到该域第一个有权限的子路由，Logo / `PanelLeftOpen` 负责显式展开。导航区滚动条隐藏但保留滚动（`.scrollbar-none`）。折叠态 / 分组折叠态 / 选中节点持久化 `localStorage`（`stores/console.ts`：`sidebar.collapsed` / `sidebar.collapsedGroups` / `sidebar.selectedNodeId`）。
  - **品牌区折叠/展开（FR-181，增强 FR-131；顶栏贯通 FR-334/ADR-071 后迁至顶栏品牌区）**：logo（品牌图标 + `JianManager` 文字）整体为一个 `<button>`，点击复用 `console.toggleSidebar` 收缩/展开；折叠态仅图标仍可点回展开。`aria-label` 描述「将发生的动作」（展开态=收起 / 折叠态=展开，纯函数 `sidebar-logo.ts:logoToggleLabelKey`）。展开态品牌区右侧另有 `PanelLeftClose` 显式收起按钮、折叠态导航区顶部有 `PanelLeftOpen` 展开按钮，均调同一 action。
  - 底部（FR-164/FR-132）：**全局主题切换器** `ThemeSwitcher`——主题色圆点（Jian 绿默认，兼容旧 `indigo` 存储值 / 青绿第二主题）+ 明暗（lucide 图标 + dropdown 三态直选）；版本号（左下）+ 开源许可入口（右下 → `/licenses`，FR-135）；退出登录已迁至顶栏账户菜单（FR-162）。切语言同步 `<html lang>` 见 `i18n`。
- **顶栏（FR-162，重排 FR-179，顶栏贯通 FR-334/ADR-071，`ConsoleHeader`）= 横跨整宽的全局顶栏**（T 型外壳：顶栏在上、其下「侧栏 + 工作区」一行；简约扁平、层次/间距精修）：
  - **左** = 品牌区 + 统一面包屑（FR-134，`BrandSegment` + `PageBreadcrumb` + 纯函数 `lib/breadcrumb.ts`）：品牌区（logo + 折叠开关）宽度经 CSS（`.jm-brand-segment`）与侧栏同步收放、右缘与侧栏右缘对齐连成一条竖线，顶栏下缘 = 侧栏上缘，左上角交界处合为连续横竖线（消除原侧栏 logo 区与页眉两条错位分割线的台阶）；其右为面包屑，按资源主轴渲染「平台 › 节点 › 服务器」或页面轨迹，父级可点跳转、末级加粗，打开服务器统一控制台时末级补服务器名。面包屑容器 `flex-1 min-w-0` 占据剩余宽度并可截断，把右侧操作区推到右缘（窄屏防翻屏）。原节点作用域下拉（FR-268）已下线，`selectedNodeId` 数据面保留（见本节方向段）。窄屏（<sm）侧栏隐藏，品牌区随之隐藏，顶栏回落为「面包屑 + 操作区」满宽。
  - **右（操作区，靠右对齐，FR-179）** = 常驻搜索框 + 集群概览徽标 + **统一通知铃铛**（FR-216）+ 账户菜单。槽位顺序 / 响应式可见性逻辑下沉纯函数 `components/console/header-layout.ts`（`HEADER_RIGHT_SLOTS`/`slotVisibility`/`searchBoxClass`，vitest 覆盖）：
    - **搜索框**已接入全局命令面板（`CommandPalette`）：点击或 `Ctrl/⌘+K` 打开，单输入框同时消费 `GET /instances/search` 的服务端实例搜索，并按统一导航真源 `flatNavItems(role)` 检索当前角色可达页面（另含节点与操作命令）；由 FR-162 的居中铺中部改为**靠右固定上限宽度**（`w-40 lg:w-52 xl:w-60`，ADR-071 尺寸下调）紧贴操作图标，窄屏（<md）隐藏。
    - **集群概览徽标**（在线节点/运行实例/崩溃数，复用 `GET /metrics/overview` + 实例列表本地统计；点击跳转对应筛选：运行/崩溃→`/instances?status=`、在线→`/nodes`）窄屏（<lg）隐藏。
    - **统一通知铃铛**（FR-216，见 ADR-048）：**合并原「站内信收件箱(FR-183)」+「告警铃铛(FR-162)」为单一入口**（`NotificationBell`，原 `inbox`/`alertBell` 两槽并为单 `notifications` 槽）。统一未读计数（`GET /notifications/feed/unread-count` = 本人站内信未读 + 全局告警未读，30s 轮询）+ 下拉只读预览最近混合通知（消息/告警各带来源标识与级别色点，`GET /notifications/feed?pageSize=8`）+「查看全部」跳 `/notifications` 通知中心页。+ **账户菜单**（用户名/角色 + 退出登录）始终显示（窄屏不隐，确保核心能力常驻）。
- **右 = 工作区（路由页面 + 服务器统一控制台，FR-166/269）**：
  - 点实例 → 直接导航到 `/instances/:id` 并打开**服务器统一控制台**（`InstanceConsolePage`）：固定分区为概览 / 控制台 / 文件配置 / 监控 / 玩家 / 插件 / 备份定时 / 业务 / Bot，激活分区写入 `?tab=`，刷新可还原；顶部状态条提供运行态、节点、端口、在线玩家、TPS/MSPT 与启动 / 停止 / 重启 / 强杀 / 打开终端等操作。FR-166 可组合卡片画布仍保留为超级工作台 / 导播台等高级拼屏能力，不作为单服默认入口。
  - **控制台 keep-alive 与跨服热缓存**（FR-295/296，见 ADR-067）：`/instances/:id` 不再直接挂 `InstanceConsolePage`，而由跨服热缓存宿主 `InstanceConsoleCache` 渲染——维护最近打开的 ≤3 个服控制台（LRU，`lib/console-hot-cache.ts` 纯逻辑），每个成员一份 `<Activity mode>` 包裹的控制台，命中热集切 `visible` 瞬时呈现、未命中入列、超容按淘汰偏好（先无草稿者，草稿由 `lib/console-draft-registry.ts` 登记）整体卸载释放；配合 `Workspace` 把 `/instances/*` 路由 key 归并为固定值，实例间切换不触发路由级 remount。单服内部 9 个页签访问过即以 `<Activity mode="hidden">` 保活（DOM/本地状态保留、effects 卸载 → 隐藏页签轮询自动暂停），切回瞬时。**终端连接抽为模块级单例 `lib/terminal-session-manager`**（组件订阅制：xterm 实例与 WS 常驻管理器，组件/页签卸载只解绑渲染层不断连；重连退避 FIX-B、一次性 token 现取 FR-140、401 诊断 FR-276 语义原样迁入），热集成员用 `pin`/`markVisible`/`markHidden` 联动——hidden 成员 WS 保持连接、闲置超 10 分钟自动断连降级、切回自动重连；LRU 淘汰 / 离开控制台 / 登出（`disposeAll`）即整体释放。**独立表面（FR-166 画布卡片 / FR-168 导播台）仍卸载即释放**（`release` 受 `pinnedIds` 约束，非 pin 即 dispose，ADR-035 资源模型不变）。
  - **统一卡壳** `WorkspaceCard`：grip 拖拽手柄（`draggableHandle=".workspace-card-grip"`，仅按住卡头 grip 才移动，卡内终端/编辑器交互不被吞）+ 实例·功能标签 + 全屏（临时最大化单卡）+ 关闭。卡 resize / 全屏切换后派发 `window` resize，触发终端 `fit` 与编辑器 relayout。
  - **惰性挂载**（承 ADR「未挂载卡不建 WS」）：仅渲染当前画布上的卡片，故终端 WS / metrics 轮询只对画布上的卡建立；未加入画布的功能不预渲染。
  - **预设（个人级 localStorage）**：命名保存画布布局（纯函数 `lib/workspace-preset.ts` 序列化/校验/规整 + `lib/workspace-card.ts` 卡片类型目录，vitest 覆盖）。内置「快捷预设」= **运维台**（默认：大终端 + 状态 + 资源）/ 纯终端 / 资源；用户可「另存为」自定义预设、删除。画布/卡片/预设运行态由 `stores/workspace.ts`（Zustand，按实例 id 记忆，各卡自管 dirty）承载，**不进 URL**（与 `console.ts` 的侧栏/选中态分离）。`/instances/:id` 由 `InstanceDetailPage` 直接挂载服务器统一控制台；需要拼屏时从超级工作台或导播台进入画布能力。
  - **文件**段 = 共享资源管理器 `components/explorer/ResourceExplorer`（FR-070）：左懒加载目录树（`FileTree`）+ 右目录内容（`FileList` 多选/右键/拖拽源）/ CodeMirror 编辑器（`editor/CodeEditor`，多格式高亮 + Ctrl+S 拦截保存接 FR-051 历史）。交互全集（新建文件夹/重命名/删除/剪切复制粘贴/树内拖拽移动/拖拽上传/单文件流式与多选 zip 批量下载/shift·ctrl·全选多选）抽为纯函数（`selection`/`clipboard`/`paths`/`language`，vitest 覆盖）；删除/回滚走 `DangerConfirm`（FR-059），历史版本经右侧抽屉 `VersionDrawer`。`ResourceExplorer` 接受可选 `config` 能力注入（编辑器插槽 / 左栏插槽 / 配置版本抽屉），不注入即为纯文件资源管理器。**此组件为 FR-071/073/074/075/082/083/084 复用地基**。归档浏览/反编译（FR-075）叠加为右栏互斥面板：`FileList` 双击/右键按 jar/zip→`ArchiveViewer`（内部条目子树 + 点文本条目只读查看 + 点 `.class` 触发反编译）、`.class`→`DecompileViewer`（只读 Java 源码），与文本编辑器三者互斥占用右栏；API client `api/archive.ts`，只读端点不触碰写操作。
  - **共享文件浏览器** `components/file-browser/FileBrowser`（FR-213）：**展示型、数据源经 props 注入、不耦合具体后端**的只读浏览组件——左目录树/列表（`FileBrowserTree`，支持「懒加载分层」与「扁平全量+`tree.ts` 建树」两形态）+ 右内容预览（`FilePreview` 复用 `editor/CodeEditor` 只读多格式高亮；二进制/超大/错误**显式降级**为「不可预览+下载兜底」）。数据/下载/操作全经 `FileBrowserSource`/`FileBrowserAction` 契约注入（组件主体不 import 任何后端 api）；实例工作目录适配器见 `file-browser/sources/instanceSource`（二进制=NUL 字节、超大=1 MiB 阈值判定）。实例「资源卡片」（`console/InstanceResourceCard`）= 「管理」Tab 全功能 `ConfigExplorer`（能力不减）+「文件」Tab 纯文件 `ResourceExplorer`（通用 `fileVersions` / diff / 回滚生产入口，FR-204）+「浏览」Tab 共享 `FileBrowser`；客户端分发文件预览（FR-214）复用同一组件喂 manifest 数据源（`sources/clientDistSource`，经管理面 sha256 端点读已上传制品文本）。**发布编排页（FR-250）尚未上传时的内容预览另有 `sources/localDraftSource`**——直接读浏览器内 `File` 文本（同一 NUL/1 MiB 降级口径），零网络、无 sha256 依赖。
  - **配置**段 = `components/config-explorer/ConfigExplorer`（FR-071）：**复用 `ResourceExplorer`** 并注入配置能力——打开文件改用 `ConfigFileEditor`（schema 表单/文本双模式 + 跨文件校验 + Ctrl+S 存**配置版本**，FR-031；文本模式复用共享 `CodeEditor` 多格式高亮）；左栏顶部 `FavoritesBar`（收藏书签存 `localStorage`，纯函数 `favorites.ts` + 已发现配置面板 `GET /configs/discover` 递归全部配置，分组纯函数 `discover.ts`）；历史经 `ConfigVersionDrawer`（FR-031 配置版本/diff/回滚）。树/列表本身呈现工作目录全部文件，满足「目录树呈现自动发现的全部配置」。原独立三栏 `ConfigEditor` 已移除。
  - 其余路由在工作区按路由渲染。**总览页（`OverviewPage`）** = 环形仪表盘 + 跨节点聚合历史曲线（FR-060：总 CPU/内存/在线玩家）+ 异常实例 / 近期任务 / 活跃告警三栏一屏速扫（各最多 5 条、独立加载/空态/错误降级，FR-271）+ 虚拟渲染密集实例表（mock 模式 1000+ 服务器时仅渲染可视窗口）；**节点页（`NodesPage`，FR-177 主从双栏重做，取代原卡片网格/列表 + 手搓 `fixed inset-0` 模态）** = 左**可收缩节点列表**（窄图标轨 ⇄ 展开，收缩态 `localStorage` 持久；顶集群汇总头复用 `summarizeNodes` + 搜索 + `AddNodeDialog`；行 = 状态点呼吸灯/名/host/mini 水位/实例数，选中高亮、离线置灰）+ 右**选中节点实时详情**（身份块 + 操作 kebab[维护/排空/下线，走 `DangerConfirm`] + `ResourceGauge` CPU/内存/磁盘/负载 + **分段 Tabs**：概览 `NodeOverviewSection` / 实例 `NodeInstanceCompare` / JDK `NodeJDKPanel` / 制品缓存 `NodeArtifactCachePanel`（FR-178 组件改挂分段，抽屉入口下线）/ 端口 `NodePortsPanel` / 监控历史曲线 / 坏节点修复 `NodeRepairPanel`[BUG-A：诊断 + 重 enroll + 清孤儿，接 `/nodes/repair/*`·`/nodes/:id/reenroll|orphans|purge-orphans`，破坏性走 `DangerConfirm`]）；未选节点右栏空态。列表筛选/选中态/收缩态持久抽纯函数 `lib/node-list.ts`（vitest 覆盖）。分段切换稳定工具条、布局不重组（FR-178 §5 抽屉 UX 约束）。**开源许可页（`LicensesPage`，`/licenses`，FR-135）** = 构建期 `scripts/gen-licenses.mjs` 扫描 control-plane-web(pnpm) + bot-worker(npm) + Go(go-licenses) + client-updater(Gradle runtimeClasspath) + ServerProbe(Gradle taboo 发行依赖) 五个发行来源，任一来源为空即失败，生成 `apps/control-plane-web/public/licenses.json`（静态资源、非 `/api`）；页面提供包名搜索 + 运行时/开发分区计数 + 表格 [包名·版本·许可证·作者] + 行内展开许可证全文。
  - **跨实例超级工作台（FR-167，`/super`，集群域独立入口，复用 ADR-034）**：把可组合画布的作用域从「限当前实例」扩展为**跨实例**——同一画布并存任意实例的卡（如 4 个不同实例终端拼监看墙）。两作用域在 `stores/workspace.ts` 清晰并存：单实例画布 `canvasByInstance[id]`（卡省略 instanceId，按实例 id 记忆）与超级工作台 `superCanvas`（**卡显式携带 `instanceId`**）。页面 `components/console/SuperWorkbenchPage` = 左侧可收起**实例库** `InstanceLibrary`（搜索实例 + 实例展开看 6+ 功能；**HTML5 原生 DnD 拖拽源**：拖实例=加该实例默认卡组、拖功能=加单卡、多选批量拖=一次拼监看墙；放置区 dragover 高亮 + 松手落位）+ 右侧跨实例画布（复用同一 `WorkspaceCard` 卡壳与网格、**惰性挂载**未上画布的卡不建 WS）。卡片所属实例名由 `WorkspaceCard` 按 `instanceId` 自解析（每卡可属不同实例）。**跨实例预设**与单实例共享同一份 `userPresets` localStorage（`lib/workspace-preset` 序列化扩为携 `instanceId`，**向后兼容**无 instanceId 的旧预设）。拖拽载荷的序列化/解析与「载荷→卡片」「跨实例卡去重（同实例同功能去重，多实例同功能并存）」抽为纯函数 `lib/instance-library.ts`（vitest 覆盖）。
  - **工作区导播台（FR-168，`/director`，集群域独立入口 / 超级工作台工具栏「导播台」按钮进，ADR-035）**：在多个**场景**（= FR-167 跨实例预设）间像 OBS **瞬切零延迟** + 缩略图条 + 定时轮播。页面 `components/console/DirectorConsolePage`：① **场景缩略图条** `DirectorSceneStrip`（一排场景，点击 / 数字键 1-9 / ←→ 瞬切；三态指示——active 主色脉动 / 预热绿点 / cold 灰点；右侧并发上限滑杆）；② **舞台**把所有**预热场景的画布同时挂载**（`DirectorCanvas`，只读网格复用 `WorkspaceCard` 卡壳），仅 active 可见。**核心 = ADR-035 预热并发模型**：要瞬切零延迟，目标场景的卡 WS 必须**已保活**；但多场景同时全速渲染会过载浏览器（WS 同域 ~6 连接 + 多 xterm/图表重绘吃满 CPU），故——**场景三态状态机**（纯逻辑 `lib/director.ts`，vitest 覆盖 LRU 驱逐 / 状态转移 / 轮播序列）：激活唯一 + **预热是受并发上限约束的集合**（默认保守 3，可配 1~6），新预热超限按 **LRU 驱逐**最久未激活的预热场景（降 cold，下次切换重连）；**非激活降频 / 暂停渲染**——非激活场景的 `DirectorCanvas` 用 `content-visibility:hidden`（浏览器跳过整棵子树布局/绘制）+ 终端经 `lib/director-render.ts` 的 `DirectorRenderProvider active=false` 让 xterm **暂停 render 但 WS 继续收数据进缓冲**（`Terminal.tsx` 加 paused 模式累积输出），切回一次性 flush。**cold 场景不挂载**（不建 WS）。导播台运行态（场景定义 + 状态机 + 轮播）由 `stores/director.ts`（Zustand，场景/上限/轮播间隔 localStorage 持久）承载，**纯前端**——只管理既有终端/监控 WS 的保活与渲染节流，不新增协议、不逾越进程边界（守架构不变量）。**真机多连接压测为硬验收维度**（单元只覆盖状态机逻辑）。
- **设计系统（FR-061 + FR-163 视觉底座 + FR-267 A+C 收口 + FR-273 组件包）**：CSS 变量 token 驱动；默认亮色为 **A+C Jian 绿 `#158053`**，辅助钴蓝 `#2563EB`，结构色为背景 `#F5F6F8` / 面板 `#FFFFFF` / 边框 `#D7DCE3`，状态色系继续由 success/warning/danger/info 与阈值 helper（见 `@jianmanager/ui/lib/threshold`）驱动。**设计底座 token（`index.css`）**：`--primary: #158053`、`--brand-forest: #158053`、`--brand-cobalt: #2563EB`、`--radius: 0.375rem`、`--workspace-bg-image` 指向 A+C 背景素材；暗色模式按 B 高密度专业运维方向保留更深表面层级。**交互细节（FR-176/244/267）**：卡片/行/chip 原语 hover 只换阴影不位移；输入焦点环使用 `ring-2 + ring-ring/40`；全局 motion token（`--motion-duration-fast/normal/slow/route`、`--motion-easing-standard/emphasized`）在 app 与 `@jianmanager/ui` 两侧暴露，侧栏、顶栏、路由、移动导航面板、工具条与固定顶层进度条 `TopLoadingBar` 均引用同一时长/缓动；React Query 请求/变更忙碌时进度条进入循环加载态；`prefers-reduced-motion` 下保留切页/进度条状态反馈动画，仅关闭平滑滚动；全局主题化细滚动条随明暗 + 双主题自适配。**通用组件包**：`packages/ui`（FR-283 迁仓库根，见 ADR-064）以 `@jianmanager/ui` **pnpm workspace 真依赖**（源码 exports，消费方 Vite 直接转译；Tailwind 侧各 app 显式 `@source` 声明其源码为扫描源）暴露 Button / Panel / StatCard / StatusBadge / SummaryChips / Table / Form primitives、`RangePicker` / `TimeSeriesChart` / `MonitorChart` / `MetricsOverviewStrip` 等通用 chart，以及 `utils` / `threshold` / `brush` / `chart-hover` / `monitor-metrics` helper；旧 `web/src/components/ui`、第一版通用 chart 与 helper 入口保留兼容 re-export。**控件博物馆**：`apps/ui-museum`（原 `web/wiki`，FR-283 更名迁移）是独立 Vite workspace 应用，直接消费 `@jianmanager/ui` 展示 Foundation / Actions / Forms / Data / Overlay / Monitoring 控件矩阵。**弃 shadcn `Card` 松散用法**（`card.tsx` 标 `@deprecated`，eslint `no-restricted-imports` 阻断新引入，见 ADR-032）。**全局双主题（FR-164）**：组件层零硬编码品牌色，品牌色全经 CSS 变量（`--primary`/`--primary-foreground`/`--accent`/`--accent-foreground`/`--ring`/`--brand-shadow`/`--chart-1`）。第二主题青绿 `#14B8A6` 仅在 `index.css` 用 `[data-theme="teal"]` 与 `[data-theme="teal"].dark` 覆盖这组品牌变量（结构色/状态色不动）；Jian 绿为默认（无 `data-theme` 即承 `:root`/`.dark`，兼容旧 `colorTheme: indigo` 存储值）。**主题色（`colorTheme: indigo|teal`）与明暗（`light|dark|system`）正交、各自 `localStorage` 持久**；纯逻辑下沉 `lib/theme.ts`，`stores/theme.ts` 统管两轴。**主题/明暗初始化提到 app 入口**（`main.tsx` 在 React 挂载前 `initThemeFromStorage()` 套 `<html data-theme>` + `.dark`），登录/初始化页也套主题且首屏无闪。一处切（侧栏底部 `ThemeSwitcher`）全站 CSS 变量实时跟变（按钮/曲线/选中态/进度条随主色）。仍基于 shadcn/ui + Tailwind v4 + OKLCH，不引入新框架。
- 暗色/亮色主题与 i18n（zh/en）正常；选中实例/节点为客户端 UI 状态，不进 URL。
- **响应式基线（FR-163）**：栅格断点沿用 Tailwind `sm/md/lg/xl`（如总览 KPI `grid-cols-1 sm:grid-cols-2 md:grid-cols-3 xl:grid-cols-6`），页面壳 `jm-page-stack` 全宽流式铺满工作区，页眉与工具条允许换行；卡片原语 `Panel`/`StatCard` 流式宽度自适应、不破栅格。移动端工作区底部预留导航安全区，避免底部导航遮挡主要操作。

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

#### 告警管理 `/alerts`

`AlertsPage` 保留规则 / 事件 / 通道三 Tab：规则按 `triggerType` 动态展示指标、关键字、玩家事件匹配与节点 / 实例目标选择；事件列表支持级别、触发类型、规则、通道类型、确认 / 恢复、关键字、时间范围与分页筛选；通道页管理 webhook、邮件、钉钉、企业微信、飞书、Discord、Telegram、站内等通道。告警事件仍按 ADR-048 作为认证用户全局可见的运维事件进入统一通知流，确认或全部已读后同时刷新 `/alerts` 与 `/notifications` 读侧缓存。

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
**点击实例名** → 导航到 `/instances/:id` 并打开该实例的**服务器统一控制台**（见 §8.2「右=工作区」，FR-269；控制台顶部含启动/停止/重启/强制终止 + 打开终端，固定分区含概览 / 控制台 / 文件配置 / 监控 / 玩家 / 插件 / 备份定时 / 业务 / Bot）；FR-166 可组合卡片画布保留在超级工作台 / 导播台作为高级拼屏能力。
**组织分组视图**（V2，FR-165/133）: 筛选栏「组织分组」开关切到「左分组树 + 右列表」专用形态（design §4.4）——左树多级嵌套（新建/嵌套子组/折叠优先/选中，节点挂子树聚合去重计数），补名称搜索、匹配上下文展示、虚拟渲染、`role=tree/treeitem`、`aria-selected/expanded` 与 Enter/Space 键盘操作；右列表复用工作台卡 + 组路径面包屑 + 批量「标记入组」，支持把实例拖入左树某组（HTML5 原生 DnD）。与既有多维筛选 + `groupBy` 维度分组**并列正交**，互不破坏。分组树正交于用户组（RBAC）与网络群组（部署），仅 CP 读写（`/instance-groups`，ADR-033）。

**分页/聚合查询地基**（FR-247/235/137/128）：既有 `GET /instances` 一次性返回全量裸数组，实例上千时响应体过大 + 前端全量渲染卡顿。新增两个只读端点作为规模化查询地基（供 FR-235/240/241 消费）：`GET /instances/search`（分页 + 名称子串搜索 + 多维筛选 + 排序，新信封 `{items,total,page,pageSize}`）与 `GET /instances/aggregate`（按状态/节点/角色维度计数，零补全全枚举键）。`/instances` 主页面已消费这两个端点：搜索/状态/节点/网络/环境/标签/视图/分组/排序/方向/pageSize 写入 URL，卡片视图、平铺表格与分组单表均用 `useVirtualRows` 只挂载可视窗口并按需翻页，分组列表用 sticky 组头避免每组重复表头；滚动位置按 `pathname+search` 存 sessionStorage。复用既有权限作用域与筛选语义；`GET /instances` 保持不变，仍供尚未迁移的页面使用。

#### 可组合卡片画布（超级工作台 / 导播台高级拼屏引擎）

单服默认入口已是固定分区的**服务器统一控制台**（FR-269 / ADR-056，见上「点击实例名」），可组合卡片画布不再作为单实例默认工作区、也不再有单实例画布路由。该画布引擎（FR-166，取代 ADR-030 的固定分屏方向）保留为**跨实例超级工作台**（FR-167，`SuperWorkbenchPage` + `SuperWorkbenchToolbar`，`/super`）与**工作区导播台**（FR-168，`DirectorConsolePage` + `DirectorCanvas`，`/director`）的高级拼屏能力：任意实例的功能卡自由拖拽 / 缩放拼合、命名预设个人级持久化（`stores/workspace.ts`、`workspace-card` / `workspace-preset`）。示意：

```
┌──────────────────────────────────────────────────────┐
│ 超级工作台（跨实例）                  [预设▾][+加卡][💾]│  ← 画布工具栏（SuperWorkbenchToolbar）
│  实例库拖拽添加 · 卡片携各自 instanceId                 │
│  ┌──────────────────────────┐ ┌────────────────────┐  │
│  │ ⠿ 终端  Survival          │ │ ⠿ 服务器状态        │  │
│  │  (xterm 经 CP WS 中转)    │ │  (在线/世界/运行态) │  │
│  │                          │ ├────────────────────┤  │
│  │                          │ │ ⠿ 资源（文件+配置） │  │
│  └──────────────────────────┘ └────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

**画布工具栏**（`SuperWorkbenchToolbar`，FR-167）：标题 + 跨实例快捷预设 ▾ / 添加卡片 / 另存预设；跨实例画布无「当前实例」，故不含单实例生命周期操作，卡片来自左侧实例库拖拽、各携自己的 `instanceId`。导播台（`DirectorCanvas`）为只读播放视图，不可编辑布局。单实例生命周期（启动 / 停止 / 重启 / 强制终止 + 打开终端）由服务器统一控制台状态条承载（FR-269，见上）。与全局顶栏（FR-162/179）同色系同圆角（`bg-card/40` + 语义令牌，明暗 + 双主题随 CSS 变量切换）。

- **卡片类型**（各复用既有面板，惰性挂载，未上画布不建 WS）：
  - **终端** — 可交互终端（读写 xterm.js，经 CP `/ws/terminal` 中转，`TerminalPane`；FR-281 后 CP→Worker 段优先走 TerminalSession gRPC 桥）
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
>
> 对话框形态（FR-189）：套 `scrollableDialogContentClass` + `ScrollableDialogBody` 自适应壳（头/脚固定、正文超高内部滚动），宽 `sm:max-w-2xl`；字段按「基本 / 启动 / 高级」分区 + 双列网格缩短高度；**Docker 资源限额相关字段（镜像 / CPU / 内存 / 磁盘）仅启动方式=docker 时出现**，非 docker 不占位，并提示「留空/0 = 不限制」（磁盘仅记录展示）。「添加节点」对话框同套自适应壳，结果视图用「自动安装 / 手动连接」两 Tab（手动连接=已部署 Worker 凭 CP 地址 + 一次性 token 直接启动注册，复用同一签发结果），复制点统一走 `copyToClipboard`（兼容 HTTP 非安全上下文）。
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

> 健康条仅「在线 vs 其余」两段（摘要分组只给 `online`=connected + `total`）。「在控制台打开」(仅实例分组) → `console store.openInstance(id)` + 跳 `/`，回到控制台工作区。单 Bot 行可打开实时遥测/详情面板（SSE `/bots/:id/events` + `SendBotCommand`），压测会话以持久化会话聚合展示与批量启停；含 YAML 编排的会话展示阶段数、循环、总时长与行为摘要，并可打开详情查看原始 YAML。控制台内 per-instance Bot 段见 FR-039。

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
- **设置 `/settings`**: 系统设置（仅平台管理员）；页面按外观、日志、运行时、网络、备份、安全 / 系统共 6 类展示。DB 覆盖优先级为 DB > env > YAML；各键是否即时改变运行态以 `effectiveImmediately` 与对应消费链路为准（FR-063）
- **系统更新 `/system-update`** (V2，FR-081/FR-175/FR-182/ADR-036 §7/ADR-042): 侧栏「设置」组，仅平台管理员可见。更新源默认读 **GitHub Releases**（`update.github_repo`=`wcpe/JianManager` + `channel`：`stable`=`/releases/latest`、`prerelease`=滚动 `latest` tag，feed 为可选回退）。检查更新（CP 自身 + 各节点版本对比，`source` 标更新源，notes 独立说明块）、CP 自更新、单节点升级、全网逐节点编排（rollout 运行中短轮询进度）；**升级前自动备份当前二进制**，CP 与各节点可一键**回滚 v{backupVersion}**到上一版（无备份禁用，FR-182）。升级/回滚均危险操作走统一 `DangerConfirm`（scope=platform）二次确认
- **群组服 `/networks` + `/networks/topology`** (V2): `/networks/topology` 为拓扑视图（代理 + 已注册后端，含各子服在线人数），`/networks` 为群组管理；管理 proxy↔backend 注册（别名/优先级/forced-host）；群组软标签筛选与批量启停；「搭建子服 / 搭建代理」向导入口
- **玩家管理 `/players`** (V2): 在线玩家（探针事件实时聚合，标注所在子服，BC 跨服感知，FR-066）/封禁记录/白名单三视图；踢出/封禁二次确认 + 原因输入，解封（经探针插件桥 `SendPluginCommand` 执行，FR-067）；探针未连入降级提示。**「实时事件」标签**经 SSE 驱动在线名册 + 事件流
- **运行时/JDK** (V2): 在节点详情页 `/nodes/:id` 增「JDK」标签——列出已装 JDK、安装指定版本、登记系统已有 JDK、查看被哪些实例占用
- **运行时与制品全局页 `/runtime-assets`（FR-082 / FR-301）**：系统域平台维护入口，Control Plane 聚合 `nodes`/`node_jdks`/`node_runtimes`/`instances`/`assets` 现有表，不新增 proto 或 Worker RPC。JDK 区按 `instances.jdk_id` 生成 `direct` 引用、按同节点 `java_major_version` 解析到同大版本最大 id JDK 生成 `major` 引用；制品区只展示 FR-045 既有 `ref_count`、类型分组、冷热/归档/外置状态与 `client-file` metadata 路径，不臆造实例级制品连接。FR-301 起 overview 加性携带**多运行时矩阵**（`node_jdks`(type=jdk，含引用实例) + `node_runtimes`(nodejs/python 预留，引用恒空) 读侧拼装）与每节点/整体上次同步时间（`nodes.runtime_synced_at`，JDK syncFromWorker 成功即刷新）；`POST /api/v1/runtime-assets/refresh` 强制全节点库存同步——单节点失败容忍（逐节点回报 ok/error，DB 旧数据保留显旧），写审计 `runtime_assets.refresh`。端点均限平台管理员。
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
登录 → 仪表盘看到实例状态 → 点击实例（进入 `/instances/:id` 服务器统一控制台）
→ 在「控制台」分区查看日志 → 发送命令
→ 如需修改文件 → 在「文件配置」分区编辑 → 保存
→ 如需重启 → 点击控制台顶部重启按钮
```

#### 流程 3: Bot 压测

```
Bot 页面 → 创建压测会话 → 选择目标实例 + bot 数量
→ 填写 YAML 编排（可留空回退旧 behavior）→ CP 校验并保存原始 YAML + 摘要
→ 开始压测 → CP 下发 orchestrated behavior_config，Worker 透传，bot-worker 按阶段执行
→ 观察 bot 陆续上线
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
web/
  packages/ui/  # @jianmanager/ui 通用 UI/token/charts/helper 源码包（FR-273）
  wiki/         # 控件博物馆 Vite 子项目，直接消费 @jianmanager/ui（FR-273）
  src/
    api/          # Axios client + per-module API (TanStack Query hooks)
    ws/           # WebSocket client, provider, hooks
    stores/       # Zustand (auth, theme, console[选中实例/节点])
    pages/        # 页面（懒加载）；DashboardPage = 运维控制台 Shell；V2 新增 NetworksPage(群组服拓扑) + 节点详情 JDK 标签
    components/   # 业务/页面组件；ui 与第一版通用 charts 为 @jianmanager/ui 兼容 re-export
                  # V2: config-editor(表单/原始/版本) · provision-wizard · jdk-manager · clone-dialog · registration-editor
                  # DangerConfirm: 统一危险操作二次确认（高危需输入名校验 + 角色门禁，FR-059）
    hooks/        # 自定义 hooks
    i18n/         # 中文 + 英文（danger 命名空间 = 危险操作文案）
    lib/          # 应用工具函数；通用 helper 由 @jianmanager/ui 供给
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
  behavior/     # 行为引擎 (Tick 250ms): follow, guard, patrol, idle, custom, orchestrated
  script/       # 脚本执行器 + 进度上报
  debug/        # 交互式调试会话
  pathfinder/   # mineflayer-pathfinder 封装
  state/        # 3s 周期状态上报
  health/       # 心跳检测
```

容量：50 bots/worker, 256 workers max ≈ 12,800 bots

Worker spawn bot-worker 的 node 可执行经解析策略选定（FR-300，`internal/worker/bot/noderesolver.go`）：显式配置（`ManagerConfig.NodePath`，V1 只留结构无配置面）> 节点本地扫描最高 major Node（复用 `runtimescan` node 路径表）> 回退 PATH `"node"`（保兼容）；解析一次缓存、spawn 失败重扫重试一次，路径与来源（explicit-config/managed-scan/path-fallback）打进 Bot 启动日志。

**dist 分发与依赖（FR-308，见 ADR-070 修订 ADR-006）**：bot-worker dist 由构建期 `make embed-botworker` 打成确定性 tar.gz（含 `package.json` 保 ESM 语义）内嵌 CP；Worker 注册成功后经 unary RPC `FetchBotWorkerArchive`（`node_uuid+node_secret` 与重注册同源鉴权，指纹一致回空归档省流）自愈物化到 `<数据根>/opt/bot-worker/`（`internal/worker/botdist`，sha256 复核 + 临时目录 rename 原子换入）。入口解析顺序：`JIANMANAGER_BOT_WORKER_PATH` 显式覆盖（不自愈）> 数据根物化副本 > 旧相对路径 `bot-worker/dist/index.js`；CP 未内嵌/不可达回退本地已有，只告警不阻断启动。运行时依赖（mineflayer / mineflayer-pathfinder）不随归档分发，由 FR-307 托管全局包提供：自愈时在 dist 同级建 `node_modules` 链接 → 托管全局 node_modules（Windows junction / 其余 symlink），NODE_PATH 仅作 CJS 兜底；spawn 前预检缺装即返回「请到节点『全局包管理』安装 …」的可操作指引。

`orchestrated` 是组合行为，不直接解析 YAML：它只消费 Control Plane 已规范化的 `behaviorConfig`，按阶段创建并切换既有行为类。`custom` 阶段继续复用现有步骤执行器字段，Go 侧把 YAML 的 `durationMs` 映射为 bot-worker 已支持的 `duration`，避免两端维护两套 YAML 语义。

## 10. 状态机

```
STOPPED → STARTING → RUNNING → STOPPING → STOPPED
                                  ↓
                               CRASHED → STARTING (指数退避)

搭建/重建任一阶段失败 → DAMAGED → (重建，复用参数重跑搭建) → STOPPED   (FR-342：损毁态)
```

**损毁态与重建（FR-342）**：一键搭建/代理搭建过程中任一阶段失败（下载/校验/配置写入/探针部署/Forge 安装器）的实例进入 `DAMAGED`（损毁），原始搭建参数存实例 `provision_spec`（JSON），失败原因写 `status_reason`。DAMAGED 由搭建/重建任务失败时直写（同 statusReason，不走 `transition()` 状态机）；`validTransitions[DAMAGED]` 留空 + `Start()` 显式守卫 → 损毁实例不可直接启动（返回 `PREFLIGHT_FAILED`「已损毁，请先重建」）。`POST /instances/:id/rebuild` 复用 `provision_spec` 重跑搭建到既有工作目录（覆盖残缺 jar/配置），成功直写 STOPPED、失败仍 DAMAGED，重建在途经长操作闸拦重复重建/启动。

**启动前双闸（FR-317 内存水位守卫）**：STOPPED/CRASHED → STARTING 的转换前有两道内存闸，防止启动实例把节点内存跑满至失去响应（真机事故：Paper -Xmx2048M 致 8G 主机 swap 风暴、SSH/面板全失联）。① **CP 预警闸**（`InstanceService.memoryGate`）：按节点最近心跳（`memory_mb`/`memory_used_mb`，90s 内有效）预判 `可用 − 预估需求 < 保留水位` 即拒绝、不下发 RPC 不翻状态；心跳过旧/字段缺失放行（fail-open）。② **Worker 实时闸**（`process.Manager.preflightMemory`，先于 Java 版本预检、对 direct/daemon/docker 普适）：启动瞬间读系统可用内存做同一判定，被拒保持原状态并返回可操作错误。估算口径共享 `internal/platform/memguard`：docker 用 `MemLimitMB`，宿主解析 `-Xmx`（×1.15 + 256MB JVM 开销），解析不到用保守默认 768MB；保留水位默认 `max(512MB, 总内存 10%)`，Worker 侧可经 `worker.yml` `memory_guard.reserve_mb` 覆盖、`memory_guard.disabled` 应急关闭。读数失败一律 fail-open（守卫故障不应瘫痪启动能力）。 **在途搭建闸（FR-319）**：一键搭建异步化后实例秒回 STOPPED 可点启动，但核心可能还在后台下载——`InstanceService.Start` 查有无关联本实例、未终态的 `provision` 任务（`tasks.instance_id`），有则拒启引导看任务中心；配合 worker `DownloadCore` 临时文件 `.part`+原子 rename（下载窗口不留半截 jar）与实例 `statusReason=搭建中` 标注，堵住「点启动读到 corrupt/缺失 jar」。

## 11. 配置

**Control Plane**: `control-plane.yml` — server port, gRPC port, database, JWT secret（管理员账号通过首次启动 Web 引导创建，见 FR-017）；`log_store`（日志中心，FR-049）；`proxy`（出站代理，FR-174，见 §11.2）
**Worker Node**: `worker.yml` — node name, Control Plane address, gRPC/WS ports, data_dir, Docker, Bot 配置；`proxy`（出站代理，FR-174，见 §11.2）；`memory_guard`（启动内存闸，FR-317：`reserve_mb` 保留水位 MB，0=默认 max(512MB,总内存 10%)；`disabled` 应急关闭）

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

**API 错误统一落平台日志（FR-320）**：全局 gin 中间件 `middleware.ErrorLog` 把 API 失败响应（4xx 业务拒绝=warn、5xx=error，401/404/429 噪音跳过）连同路径/状态码/响应体/用户/IP slog 化，经 `PersistSlogHandler` 桥自动落日志中心 platform 源——`/logs` 页可直接追查「某操作为什么报错」。此前错误只回 HTTP 响应，平台日志恒空、失败随连接断开进黑洞（FR-319 真机事故的观测性根因）。

### 11.1 项目自包含数据根（FHS 布局，ADR-010）

平台运行态数据统一收口到单一数据根，默认进程工作目录下 `./data`，可经环境变量 `JIANMANAGER_DATA_DIR` 覆盖；进程启动时若不存在按布局自动初始化（CP 与 Worker 同源约定，由 `internal/platform/dataroot` 解析）。

```
data/
├── bin/              # 平台/辅助可执行
├── etc/              # 平台与节点配置；当前 OTA 不再生成 client-sign-key.pem 作为信任根，key_enc 主密钥可由 JIANMANAGER_CLIENT_KEY_ENC_SECRET 注入或自动生成持久化为 client-key-enc.key；CP 侧 WS 令牌密钥生产 autogen 持久化为 ws-token-secret.key（0600，FR-275/ADR-061，勿删——删除即轮换、已下发探针 token 失效）；Worker 侧 node-identity.json 含 wsTokenSecret
├── opt/jdks/         # 便携 JDK：<vendor>-<ver>/（取代旧的 <serversDir>/jdks）
├── opt/runtimes/     # 便携非 JDK 运行时：nodejs-<major>/（FR-299 一键安装托管目录）
├── var/
│   ├── servers/      # 服务器工作目录：<slug>-<shortid>/（系统分配）
│   ├── index/        # 全文搜索倒排索引：<instance-uuid>/（Worker 本地派生，ADR-017）
│   ├── artifact-cache/ # 节点制品缓存：<sha256[:2]>/<sha256>(+.meta)（Worker 本地派生，FR-178）
│   ├── log/          # 运行日志
│   └── artifacts/    # 制品库（内容寻址，见 §14 / ADR-011；含 client-file 与 client-updater-core 归档；CP 全局，区别于上方节点本地缓存）
└── cache/            # 临时/派生缓存：下载中转/解压；worker-assets/<version>/<os>-<arch>/ 为 FR-190 Worker 二进制 CP 代理缓存；client-uploads/<uploadId>/ 为大文件分块上传临时分片区（FR-251，complete 拼装喂 CAS 后清理，CP 重启清残留）
```

- 登记路径**按数据根相对存储**（如 `var/servers/hub-a1b2c3d4`），整体拷到另一机器后仍自洽。
- Worker 收到 CP 下发的相对工作目录后，按本节点数据根解析为绝对路径并创建。
- **平台存储资源管理器（FR-083）**：Control Plane 提供只读数据根浏览与占用统计，固定展示 `bin`、`etc`、`opt/jdks`、`var/servers`、`var/log`、`var/artifacts`、`cache` 七类 FHS 子目录；缺失目录仍在概览中以 `exists=false` 列出。浏览端点只列直接子项、不读文件内容，路径经数据根边界校验，`cache/` 是唯一可受控清理的目录。全部端点限平台管理员，且只触达 CP 本机数据根；Worker 本机目录仍经节点/实例文件能力访问。

### 11.2 出站网络代理（每进程 HTTP/SOCKS5，FR-174 / ADR-037；可视化配置 + 下发 FR-185 / ADR-043）

CP 与各 Worker 的**所有出站下载**统一收口到共享出站 HTTP 客户端工厂 `internal/platform/httpclient`（`Config{URL, NoProxy}` + `New(cfg) (*http.Client, error)`），按本进程代理配置出站。收口的出站点：

| 进程 | 出站点 | 用途 |
|---|---|---|
| CP + Worker | `internal/platform/selfupdate.DownloadWith` | 自更新二进制下载（`Download` 保留为 DefaultClient 薄包装，生产走 `DownloadWith`） |
| CP | `service.SelfUpdateService`（`resolveRelease`：GitHub Releases API / feed 回退 + CP 自升下载 + 升级前备份/回滚，FR-175/FR-182/ADR-036 §7/ADR-042；`EnsureWorkerAsset`：FR-190/FR-278 Worker 二进制解析顺序 本地缓存 > CP 内嵌物化 > 远程 feed，见 ADR-062） | 更新源解析（默认 GitHub Releases，feed 回退）+ CP 自身升级/回滚 + Worker 二进制分发（内嵌优先，安装/升级同版本 Worker 不出网） |
| CP | `service.CoreService` | PaperMC API 与 Sponge 官方 Maven metadata 解析服务端核心版本/构建 |
| CP | `service.AssetService.IngestFromURL` | 远端制品（服务端核心等）下载入库 |
| Worker | `grpc.Server.UpgradeWorker` | Worker 升级二进制下载；FR-190 起下载 URL 由 CP-local `/worker-assets/:version/:os/:arch/worker?token=...` 提供，Worker 无需访问公网 release 源 |
| Worker | `jdk.Manager`（`downloadAndExtract` / Zulu 元数据 API / foojay disco API） | JDK 归档下载 + foojay 版本目录/下载源解析（FR-178） |
| Worker | `worker/grpc.DownloadCore`（`downloadFile`，经 `artifactcache` 命中复用） | 服务端 jar 下载到实例工作目录（FR-178 缓存命中即免下载） |
| Worker | `decompiler.Provider` | CFR 反编译器按需下载（Maven Central） |

配置（CP `control-plane.yml` 与各 Worker `worker.yml` 各加 `proxy:` 段，互相独立；分布式各机网络环境不同）：

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
- **CP 全局代理（设置面板「网络」分类）**：`platform_settings` 白名单加 `proxy.url`（敏感、脱敏展示）、`proxy.no_proxy`；保存复用 `httpclient.New` 校验、落库后经回调重建 CP 出站持有者。生效优先级 **settings DB > control-plane.yml > env**。此全局值同时作为**各节点默认代理**。
- **节点级覆盖（节点页「代理」分段）**：`nodes.proxy_mode`（inherit/custom）+ `proxy_url`/`proxy_no_proxy`。`PATCH /nodes/:id/proxy`（平台管理员 + 审计）设置；CP 据「custom ? 节点值 : 全局默认」算每节点期望代理 + `proxy_generation`（FNV 哈希），随**心跳响应**（§6.1）下发；Worker `heartbeat` 处理响应，generation 变化才重建出站持有者（避免每拍重建）。节点出站优先级 **节点 custom（DB 下发） > 全局默认（DB 下发） > worker.yml > env**。
- **真相源 = CP DB**，Worker **不落盘**；重连/重启由后续心跳天然重发（符合「DB 仅 CP 读写」「CP→gRPC→Worker」不变量）。含凭据 URL 在 UI 回显 / API 响应 / 日志一律 `httpclient.Sanitize` 脱敏；节点离线时面板标注「待下发」（下次心跳生效）。

## 12. 部署

**开发**: `go run ./apps/control-plane --dev` + `pnpm --filter control-plane-web dev`（FR-283 pnpm workspace）
**生产**: 多节点部署，Control Plane 一个 + Worker Node 多个
**Docker**: `Dockerfile.control-plane` + `Dockerfile.worker` + `docker-compose.yml`

### 12.1 构建与发布管线（GitHub Actions，FR-173，见 ADR-036）

`.github/workflows/release.yml` 在 `ubuntu-latest` 全程交叉编译产出 GitHub Releases 制品，三 job 串联：

- **prepare-embeds**（一次性产出全部 `go:embed` 资产，平台无关跨 matrix 复用）：`submodules: recursive` 拉取 `third_party/ServerProbe`，装 Go / Node20 / JDK21；构前端（`gen-licenses` → `vite build` → 复制到 `internal/controlplane/embed/dist/`）+ 内嵌探针 jar 与离线依赖缓存（`embed-probe` 生成 `ServerProbe.jar`、`probe-libraries.zip`、`probe.json`）+ 客户端更新器两件套（`embed-client-updater`，以 `--release 8` 在 JDK21 上构 Java8 字节码）+ CFR 反编译器（`embed-cfr`，sha256 pin 与 `decompiler/cfr.go` 常量一致）；embed 目录作 job artifact 上传。该 job 顺带解析触发类型算出注入版本经 job output 下传（正式=去前缀 tag `vX.Y.Z`，预发布=`0.0.0-dev+<shortsha>`）。
- **build**（matrix `linux/amd64` + `windows/amd64`）：下载 embed artifact 还原到 `internal/**/embed/`，`GOOS/GOARCH go build -ldflags "-X .../internal/version.Version=<v>"` 编 control-plane 与 worker（共 4 个二进制），命名 `<component>-<os>-<arch>[.exe]`（ADR-036 §1）。
- **release**：汇总 4 二进制 + 生成 `checksums.txt`（每件 sha256，ADR-036 §2），用 `scripts/changelog-extract.mjs` 取发布说明——push tag `v*` → 正式 release（取该版本段，`prerelease=false`）；push `master` → 覆盖固定 tag `latest` 预发布（FR-182 由 `nightly` 改名，取 `[Unreleased]` 段，`prerelease=true`，先删旧 release 再重建以仅保留本次产物）。

发布二进制**内嵌全部可选资产**「下载即用」：CP 自带前端 + 探针 + 客户端更新器，Worker 自带 CFR（ADR-036 §5）。`go:embed` 对缺失/空目录会编译失败，故 prepare-embeds 任一内嵌步骤失败即 fail-fast。版本注入在 build/release 两 job 按 prepare-embeds 同一 output 取值，保证二进制内 `version.Version` 与 release tag 一致。发布制品的命名/校验/渠道契约由 ADR-036 固化，供 FR-175 自更新对接 GitHub Releases 消费（ADR-020 §4 的 feed 来源立场由 FR-175 落地时标 superseded）。

### 12.2 SSH 推送式远程部署（FR-277，见 ADR-063）

在既有**拉取式**安装（目标机上 `curl <cp>/install-worker.sh | sh`，§5.1）之外的**推送式**通道：操作机执行 `scripts/deploy-cp.sh` / `scripts/deploy-worker.sh`（POSIX sh），经 SSH 密钥把本地 `make dist` 产物推送部署 / 更新到 Linux + systemd 主机。配置全经 `JM_*` 环境变量（与目标机二进制消费的 `JIANMANAGER_*` 命名空间隔离，按需显式映射）。

- **定位**：传输 + 编排层，零上线逻辑。Worker 首次上线 = scp 二进制 + 仓内 `install-worker.sh` → 远端 `--binary … --service` 执行（上线语义全走 ADR-051：worker 自配 setup、token 经 env 不落普通文件）；CP 首次部署 = 推二进制 + 最小 `control-plane.yml`（端口 + sqlite，密钥零写入，靠 ADR-061 生产态自动生成）+ unit + HTTP 探活。
- **更新部署**：远端有无 unit 自动判定；stop → 旧二进制留 `.bak` → 换新 → start，不碰 `control-plane.yml` / `worker.yml` / `node-identity.json` / unit。幂等可重复执行。
- **服务档位双轨**（ADR-063 §2）：`system`（root 直连或非 root 免密 sudo，`/etc/systemd/system`）/ `user`（纯普通用户，`~/.config/systemd/user` + `systemctl --user`，强制 linger 保断连常驻、开不了即报错）。`install-worker.sh` 为此扩 `--service-scope system|user`（默认 system 现状零变化），拉取式一键安装同获非 root 能力。

## 13. MC 群组服模型（V2）

> 对应 PRD FR-031~036、ADR-007/008。代理 + 多 Bukkit 子服的开服与运维。开发中。

### 13.1 角色与关系
- 实例 `role`：`proxy`（BungeeCord/Velocity）、`backend`（Bukkit/Paper 子服）、`universal`（通用进程）。实例是独立原子单元。
- **proxy ↔ backend 为 M:N**（`server_registrations`）：一个 backend 可注册进多个 proxy（共享大厅/小游戏）；每条注册带「代理内本地属性」alias/priority/forced_host/restricted。
- **群组（Network）为非独占软标签**（`network_members` M:N）：仅供分组/筛选/批量操作，子服可属多群组；真实路由只由 `server_registrations` 驱动。

### 13.2 资源所有权（系统分配）
- **工作目录**：系统在数据根 `var/servers` 下分配 `<name-slug>-<shortid>`（CP 分配并按相对路径登记，Worker 解析为绝对路径），用户不可输入，路径只读展示（取代 BUG-004 必填 UI，落位见 §11.1 / ADR-010）。
- **端口**：端口池为新实例分配同节点唯一的 server-port/query/probe，代理监听端口同理；分配由 Worker 实施、CP 登记。
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
- **CAS 相对路径 = 跨后端存储键**（FR-347 修订 ADR-011：`var/artifacts` 从唯一物理落点升格为默认后端 + 键规范，见 ADR-073）——local 后端即数据根物理路径，S3 后端为 `<渠道 prefix>/<rel_path>` 对象键；类型分区/sha256 去重/索引模型不变。

### 14.2 入库与完整性
- 入库即算 sha256+md5；调用方提供期望校验和则比对，不符拒收。
- 同 `(type, sha256)` 命中 → 复用记录并刷新 `last_used_at`，不重复落盘。
- 入口：multipart 上传 / 从本地路径登记 / 下载入库（`IngestFromURL`，供 FR-034 建服取核心复用）。

### 14.3 生命周期与引用保护
- `storage_state`(hot/archived/external) + `storage_backend`(local/s3) + `storage_channel_id` 驱动归档/外置；**位置由记录自述，不由全局状态推断**（FR-347）。归档只改状态与位置，DB 记录与引用（sha256）不变。
- `ref_count`>0（被模板/实例引用）的资产删除前拒绝。删除物理清理按记录后端路由：local 删 CAS 文件、s3 删渠道对象（均尽力而为）。

### 14.3a 外置对象存储渠道（FR-347，见 ADR-073）
- **BlobStore 抽象**（`internal/controlplane/blobstore`）：`Kind/PutFile/Open/Stat/Delete/List/ListPage/Presign` 统一 local（数据根 CAS，行为与主线逐字节等价）与 s3（纯标准库 SigV4 header 签名 + query 预签名、path-style、`UNSIGNED-PAYLOAD` 流式 PUT）；`ListPage` 以不透明续传令牌分页全量遍历（s3=ListObjectsV2 continuation-token，local=排序后的末键游标），供大 bucket 对账。**CP 侧独立实现，不 import `internal/worker/storage`**（进程边界，ADR-073 决策 3）。
- **渠道表 `artifact_storage_channels`**：内置「本机存储」行（Builtin+local）幂等 seed、不可删不可编辑、无活跃行时兜底活跃；单活跃渠道（事务先清后设）为写路径唯一路由开关；凭证 AES-256-GCM 可逆加密（复用 FR-192 KeyEncryptor，加密器未配置时创建/编辑 s3 渠道 422 快失败不落明文）；删除守卫=内置/活跃/被制品引用均拒，且存在非终态 `artifact_migrate` 任务时粗粒度禁止删除任何渠道。
- **写路径与失效自愈**：仅 `client-file` 类型经活跃渠道路由；s3 上传成功后记录 `storage_backend=s3 + storage_channel_id + storage_state=external`，失败快失败不回落。普通去重命中不重传；若命中的是 `lost+s3` 资产，Ingest 把临时文件补传回**记录自述渠道**而非当前活跃渠道，成功后复位 `external`，补传失败则整个 Ingest 失败。
- **读路径消费方分野**（ADR-073 决策 6）：玩家端点完整鉴权/安全策略通过后，健康 s3 制品回 **302 预签名短时效 URL**；`storage_state=lost` 则在后端分流前回 **410 ARTIFACT_LOST**，不跳转到必 404 的对象 URL。管理面下载/预览同样对 lost 回 410；健康 s3 制品仍由 CP BlobStore 代理直流。部署约束：updater `HttpURLConnection` 不跨 http↔https 跟随 302，CP 与 S3 endpoint 须同协议；CP 与 S3 时钟偏移超 TTL 时预签名会 403，运维需保持 NTP。

### 14.3b 制品存量迁移（FR-348）
- **任务与单在途约束**：`ArtifactMigrationService` 在 CP 进程内执行，任务为 `kind=artifact_migrate/node_id=0`；发起临界区先检查不存在 `pending/running` 同类任务，再真连探测目标渠道，同步创建 Task 与 `artifact_migrations` 登记后启动最长 12h 的后台 goroutine。第二个并发发起返回 409；目标不存在返回 404，探测/凭证解析失败返回 422。
- **固定迁移顺序**：每条 `client-file` 必须按「**读源并复核 size/sha256 → 写目标 → 先更新 Asset 的 `storage_backend/storage_channel_id/storage_state`（带旧位置乐观守卫）→ 再删除源对象/文件**」执行。更新记录前任一步失败都记入 `artifact_migration_failures`、不删源并继续下一条；记录已指向目标后删源失败只记警告，读取已安全切换，残留交 FR-349。源/目标 s3 物理 endpoint+bucket+prefix 相同时跳过删源，避免删除刚写入的同键对象。
- **计数与终态**：`artifact_migrations` 持久维护 `total/migrated/failed/skipped`；任一制品失败则任务 `failed` 并可查逐条原因，否则 `succeeded`。仅迁移 `client-file`，发起时已在目标的记录计 `skipped`。
- **取消、孤儿与续跑**：任务中心取消 `node_id=0` 任务时直接置 `canceled`，迁移循环在每条开始前检查任务状态并退出。CP 启动时 `RecoverOrphans` 将遗留的非终态迁移任务置 `failed`。终态或中断后重新向同一目标发起的是新 task；已成功条因记录已自述目标位置自动跳过，因此天然幂等续跑。

### 14.3c S3 索引一致性对账（FR-349）
- **范围**：对账单位为一个 s3 渠道；只遍历 `var/artifacts/client-file/` 命名空间。索引集合为该渠道全部 s3 client-file 资产（含 lost），对象集合经 `ListPage` 全量分页取得；local 渠道与 probe/、其他类型目录不参与。
- **差异**：非 lost 索引键在对象侧不存在 → missing；对象键在该渠道全部索引（含 lost）不存在 → orphan；一致项只计数。lost 不重复报告 missing，但其键持续排除 orphan，防止误删人工恢复对象。
- **执行与调度**：`ArtifactReconcileService` 建 running 记录后异步执行，同渠道进程内在途去重；全局触发跳过在途渠道。`Start` 把 CP 重启遗留 running 置 failed，并以分钟 tick 检查单行设置；默认 enabled/24h，首个周期从启动或启用时刻起算，不在启动瞬间扫描。
- **显式处置**：成功报告的 missing 经按钮标 `Asset.StorageState=lost`；orphan 经 DangerConfirm 后逐键删除。两条路径均在处置时重查当前索引：资产已删/迁走或孤儿键已被新上传引用时翻 `stale`，不误改、不误删；删除失败保持 open 并记录错误供重试。对账本身只读，不自动修复。
- **可视化**：运行时资产页 client-file 表格通过 overview 的 `artifactChannels` 映射存储位置，展示正常/lost 状态；同页对账区提供设置、全局触发、运行历史和分页报告，不改存储渠道页。

### 14.4 API 与鉴权
- `GET /assets`（按 type 筛选、分页）、`GET /assets/:id`、`POST /assets`（上传/登记）、`DELETE /assets/:id`。
- 平台级共享资源，统一由平台管理员管理（同节点/模板的平台管理员收敛）。

### 14.5 插件批量部署（FR-053）
- 批量部署入口 `POST /plugins/batch-deploy` 从制品库选择 `type=plugin` 资产，并指定实例 ID 集或实例筛选条件。
- Control Plane 先校验资产类型与文件名，再按实例管理权限收敛目标集合；普通成员的越权实例不返回存在性细节。
- 部署只写入实例工作目录下的 `plugins/`，不处理 `mods/`。CP 读取制品内容后复用 Worker V2 `WriteFile` 写入 `plugins/<asset-file>.jar`，避免新增 Worker RPC。
- 并发编排沿用实例批量操作的 fan-out 模式，响应按实例聚合 `success/failed/skipped`；计数口径是“实例”，不是“实例 × 插件”。
- 写操作经审计中间件记录 `plugin.batchDeploy`，审计详情只记录资产 ID、实例数量与结果汇总，不记录制品字节。
