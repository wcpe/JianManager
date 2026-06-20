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
    ├── WebSocket 终端服务 + 插件桥 (/ws/plugin-bridge)
    ├── Bot 管理 → Node.js 子进程 (Mineflayer)
    └── 指标采集
        ▲ WS (token 鉴权, 同机回环)
        └── 平台插件 (Bukkit/BC, 运行于游戏服 JVM)  // 见 ADR-012
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
internal/worker/
  config.go
  register.go
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
```

## 6. 通信协议

### 6.1 gRPC（Control Plane ↔ Worker Node）

Protobuf 定义位于 `proto/worker.proto`，包含：

- 生命周期：Register, Heartbeat (双向 stream)
- 实例操作：CreateInstance, StartInstance, StopInstance, RestartInstance, KillInstance, SendCommand, GetInstanceStatus, ListInstances
  - `CreateInstance` 除 `start_command` 外携带 `stop_command`（优雅停止命令，CP 按实例角色派生：backend/universal=`stop`，proxy=`end`），由 daemon wrapper 在优雅停止时写入进程 stdin
- 实例事件流：StreamInstanceEvents (server stream)
  - 同一流承载两类事件：`state_change`（状态转换）与 `stdout`/`stderr`（进程输出）。Worker 进程输出回调分流为「WS 终端广播 + 事件流上报」两路，互不阻塞。CP 侧 EventService 把 `stdout`/`stderr` 经 LogService 落库（日志中心 FR-049），`state_change` 经 SSE 推前端
- 文件操作：ListFiles, ReadFile, WriteFile, DeleteFile, UploadFile (client stream), DownloadFile (server stream)
- 终端：IssueTerminalToken
- Bot：CreateBot, DeleteBot, ListBots, StreamBotEvents (server stream), SendBotCommand
- 插件桥 (V2)：StreamPluginEvents (server stream, 插件事件经 Worker 冒泡给 CP)、SendPluginCommand（CP 经 Worker 下发指令给插件）。见 ADR-012
- 指标：GetNodeMetrics, GetInstanceMetrics
- 玩家管理：ExecRconCommand（经实例 RCON 执行命令并回传输出，FR-054；RCON 端口/密码由 CP 随请求下发，Worker 不访问 DB；`available=false` 优雅降级）
- 配置 (V2)：ListConfigFiles, ReadConfig, WriteConfig, ListConfigVersions, RollbackConfig
- 运行时 (V2)：ListJDKs, InstallJDK, RemoveJDK, DownloadCore
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

### 6.2.1 WebSocket（平台插件 ↔ Worker Node）插件桥

平台侧插件（Bukkit/BungeeCord，运行于游戏服 JVM，与 Worker 同机）经 WS 连入 Worker，
与终端 WS 并列、复用同一 WS 监听端口。插件**只与 Worker 通信**，不直连 CP/DB/gRPC（见 ADR-012）。

鉴权：CP 为实例签发插件桥 token（HS256 JWT，claims 含 `instanceId` + `scope=plugin-bridge`，TTL 数分钟），
运维写入插件配置；插件握手携带 `?token=...&instance=<uuid>`，Worker 用同一 `JIANMANAGER_JWT_SECRET` 校验。

```
Browser → Control Plane (POST /api/v1/instances/:id/plugin-token)
  → 返回 {token, wsUrl, expiresIn}（写入插件配置）
插件 → Worker Node (WS ws://worker:wsPort/ws/plugin-bridge?token=xxx&instance=<uuid>)
  → 事件上行：插件 →(WS) Worker →(gRPC StreamPluginEvents) CP →(SSE /plugins/events) 浏览器
  → 指令下行：浏览器 →(HTTP) CP →(gRPC SendPluginCommand) Worker →(WS) 插件
```

消息格式（插件 ↔ Worker，JSON 行）：

```json
// 插件 → Worker（事件上行）
{"type":"event","event":"player_join","instanceId":"xxx","data":{"player":"Steve"},"ts":1718870000}
{"type":"event","event":"player_quit","instanceId":"xxx","data":{"player":"Steve"}}
{"type":"event","event":"player_chat","instanceId":"xxx","data":{"player":"Steve","message":"hi"}}
{"type":"event","event":"server_status","instanceId":"xxx","data":{"online":3,"players":["A","B","C"]}}
{"type":"hello","instanceId":"xxx","data":{"platform":"bukkit","pluginVersion":"0.1.0"}}
{"type":"pong"}
{"type":"command_result","id":"<cmdId>","data":{"ok":true}}

// Worker → 插件（指令下行）
{"type":"command","id":"<cmdId>","action":"kick","args":{"player":"Steve","reason":"..."}}
{"type":"command","id":"<cmdId>","action":"ban","args":{"player":"Steve","reason":"..."}}
{"type":"command","id":"<cmdId>","action":"whitelist_add","args":{"player":"Steve"}}
{"type":"ping"}
```

会话：Worker 维护「实例 UUID → 插件会话」表（同实例同时仅一活动会话，新连顶替旧连）；
连接/断开作为 `connected`/`disconnected` 事件经 `StreamPluginEvents` 冒泡到 CP，前端据此展示已连插件列表/连接状态。

### 6.3 守护进程二进制帧协议

Worker Node 与 daemon wrapper 子进程之间通过二进制帧协议通信。
传输层跨平台：Linux/macOS 用 **Unix Socket**（`<pidDir>/<uuid>.sock`），Windows 用 **Named Pipe**（`\\.\pipe\jianmanager-<uuid>`，基于 `npipe`）。

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
AlertRule ──1:N──▶ AlertEvent
```

### 核心表

| 表 | 关键字段 |
|---|---|
| users | uuid, username, password(bcrypt), role(0/1/10), mfa_secret, status |
| groups | uuid, name, description |
| group_members | group_id, user_id, role(0=member/1=admin) |
| group_quotas | group_id(UNIQUE), max_instances, max_bots, max_storage_mb |
| nodes | uuid, name, host, grpc_port, ws_port, secret, status(0/1/2), maintenance(bool, cordon 维护模式，与在线/离线正交), os, arch, cpu_cores, memory_mb, disk_total_mb, last_heartbeat |
| instances | uuid, node_id(FK), name, type, role(proxy/backend/universal, V2), process_type, status, start_command, work_dir(系统分配), env_vars(JSON), auto_start, auto_restart, jdk_id(FK, V2), launch_spec(JSON: jvm_args/core_jar/args/omit_nogui, V2), docker_*, rcon_*, forwarding_secret(V2, Velocity 转发), proxy_online_mode(V2, 代理正版校验), server_port/query_port, mc_*, tags(JSON) |
| group_instances | group_id, instance_id(UNIQUE) |
| bots | uuid, instance_id(FK), name, status, config(JSON), behavior, worker_id |
| backups | uuid, instance_id(FK), name, file_path, file_size_mb, type(0/1), mode(0 全量/1 增量, V2), status(0/1/2/3), parent_id(FK self, 备份链, V2), manifest(JSON 文件清单, V2), storage_id(FK, V2), storage_key(远程对象键, V2) |
| backup_storages | name(UNIQUE), type(local/s3/sftp/webdav), endpoint, bucket, region, prefix, access_key_env(${ENV_VAR}), secret_key_env(${ENV_VAR}), use_ssl (V2, FR-057) |
| schedules | uuid, instance_id(FK), name, cron_expr, action, payload, enabled |
| schedule_execution_logs | schedule_id(FK), action, status, error, started_at, finished_at |
| alert_rules | uuid, name, target_type, target_id, metric, operator, threshold, duration_sec, notify_type, notify_target, enabled |
| alert_events | rule_id, target_id, value, message, resolved, fired_at |
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

登录后默认进入「运维控制台」三段式 Shell（`DashboardPage`，见 ADR-009 / FR-037）：左侧栏分上/中/下三段，右侧为工作区。

```
┌────────────────┬─────────────────────────────────────────┐
│  JianManager   │  运维控制台 / <实例名>   [分屏] [切导播台]   │  ← 工作区工具栏（面包屑 + 禁用占位）
│ ┌────────────┐ ├─────────────────────────────────────────┤
│ │ 功能导航    │ │                                         │
│ │ 仪表盘      │ │                                         │
│ │ 节点 实例   │ │   工作区                                 │
│ │ Bot 告警    │ │   · 点实例 → 该实例终端（单个，xterm）    │
│ │ 模板 计划   │ │   · 其余导航 → 按路由渲染对应页面          │
│ │ 备份       │ │   · 未开终端 → 空状态                     │
│ ├────────────┤ │                                         │
│ │ [全部节点▼] │ │                                         │
│ │ 实例树      │ │                                         │
│ │ ● Survival │ │                                         │
│ │ ◐ Lobby    │ │                                         │
│ │ ○ Creative │ │                                         │
│ ├────────────┤ │                                         │
│ │ 系统平台    │ │                                         │
│ │ 用户 用户组 │ │                                         │
│ │ 审计 设置   │ │                                         │
│ │ [主题][语言]│ │                                         │
│ │ 退出  vX.Y │ │                                         │
│ └────────────┘ │                                         │
└────────────────┴─────────────────────────────────────────┘
```

- **左栏（约 240px，固定）三段**：
  - **上 = 功能导航**：仪表盘 / 节点 / 实例 / Bot / 告警 / 模板 / 计划任务 / 备份（NavLink 路由）。
  - **中 = 节点切换 + 实例树（常驻）**：节点下拉（`全部节点` + 各节点，来自 `GET /nodes`）；`全部节点` 时实例树按节点分组，选某节点时只列该节点实例（`GET /instances?nodeId=`）。每项状态点：RUNNING 绿 / STARTING·STOPPING 琥珀 / CRASHED 红 / STOPPED 空心。
  - **下 = 系统平台导航**：用户 / 用户组 / 审计 / 设置 + 主题切换 + 语言切换 + 退出 + 版本号。
- **右 = 工作区**：
  - 点实例 → 在工作区打开其终端（复用一次性 token + xterm，复刻实例详情页终端 Tab）；**同时仅一个终端**，点另一个实例切换。
  - 工具栏：面包屑 `运维控制台 / <实例名>` + 禁用占位按钮「分屏」「切导播台」（分屏/导播台为后续阶段）。
  - 其余路由（节点 / 用户 / 审计 / 实例详情…）仍在工作区按路由渲染，既有页面不变。
- 暗色/亮色主题与 i18n 正常。
- 选中实例 / 选中节点为客户端 UI 状态，存 Zustand（`stores/console.ts`），不进 URL。

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
**点击实例名** → 在运维控制台工作区打开该实例（Tabs: 终端 / 文件 / 配置 / Bot，工具栏含启动/停止/重启/强制终止）；`/instances/:id` 详情页作为直链兜底保留。

#### 实例详情 `/instances/:id`

```
┌──────────────────────────────────────────────────────┐
│  ← 返回    Survival Server    [启动] [停止] [重启]    │
│  状态: 🟢运行中 | 节点: node-01 | 类型: MC Java       │
│                                                      │
│  ┌────────┬────────┬────────┬────────┬────────┬────┐ │
│  │ 控制台  │ 终端   │ 文件    │ 配置   │ 备份   │ Bot │ │
│  └────────┴────────┴────────┴────────┴────────┴────┘ │
│                                                      │
│  (Tab 内容区)                                        │
│                                                      │
└──────────────────────────────────────────────────────┘
```

**Tab: 控制台** — 实时日志输出（只读 xterm.js）+ 命令输入框
**Tab: 终端** — 可交互终端（读写 xterm.js，直连 Worker Node WS）
**Tab: 文件** — 文件树 + CodeMirror 编辑器
**Tab: 配置** — 实例运行配置（结构化启动：绑定 JDK / 内存 / JVM 参数；自动重启；环境变量）+ **MC 配置文件编辑器**（V2：server.properties/spigot/paper/velocity 等，配置文件树 + 可视表单↔原始双模式、保留注释、校验提示、版本 diff/回滚）
**Tab: 备份** — 备份列表 + 创建备份 + 恢复
**Tab: Bot** — 该实例关联的 Bot 列表

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
- **群组服 `/networks`** (V2): 拓扑视图（代理 + 已注册后端，含各子服在线人数）；管理 proxy↔backend 注册（别名/优先级/forced-host）；群组软标签筛选与批量启停；「搭建子服 / 搭建代理」向导入口
- **玩家管理 `/players`** (V2): 在线玩家（聚合各后端 RCON `list`，标注所在子服，BC 跨服感知）/封禁记录/白名单三视图；踢出/封禁二次确认 + 原因输入，解封；RCON 不可用子服降级提示（FR-054）
- **运行时/JDK** (V2): 在节点详情页 `/nodes/:id` 增「JDK」标签——列出已装 JDK、安装指定版本、登记系统已有 JDK、查看被哪些实例占用
- **配置编辑器** (V2): 位于实例详情「配置」Tab——MC 配置文件树 + 可视表单/原始双模式 + 一致性校验 + 版本 diff/回滚（非独立页面）

### 8.4 核心用户流程

#### 流程 1: 管理员首次使用

```
登录 → 看到空仪表盘 → 添加节点（输入节点地址）
→ 节点上线 → 创建实例（选择节点 + 配置）
→ 启动实例 → 进入终端 → 游戏服运行
```

#### 流程 2: 日常运维

```
登录 → 仪表盘看到实例状态 → 点击实例
→ 查看控制台日志 → 发送命令
→ 如需修改文件 → 切换到文件 Tab → 编辑 → 保存
→ 如需重启 → 点击重启按钮
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
→ 配置 Tab 调 server.properties/paper → 启动整个群组 → 玩家经代理进服
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
  components/   # 共享组件 (console[控制台侧栏/工作区/终端面板], ui/shadcn, terminal, chart)
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

**Control Plane**: `control-plane.yaml` — server port, gRPC port, database, JWT secret（管理员账号通过首次启动 Web 引导创建，见 FR-017）；`log_store`（日志中心，FR-049）
**Worker Node**: `worker.yaml` — node name, Control Plane address, gRPC/WS ports, data_dir, Docker, Bot 配置

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
│   ├── log/          # 运行日志
│   └── artifacts/    # 制品库（内容寻址，见 §14 / ADR-011）
└── cache/            # 临时：下载中转/解压
```

- 登记路径**按数据根相对存储**（如 `var/servers/hub-a1b2c3d4`），整体拷到另一机器后仍自洽。
- Worker 收到 CP 下发的相对工作目录后，按本节点数据根解析为绝对路径并创建。

## 12. 部署

**开发**: `go run ./cmd/control-plane --dev` + `cd web && npm run dev`
**生产**: 多节点部署，Control Plane 一个 + Worker Node 多个
**Docker**: `Dockerfile.control-plane` + `Dockerfile.worker` + `docker-compose.yml`

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
