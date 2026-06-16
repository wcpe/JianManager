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
    ├── WebSocket 终端服务
    ├── Bot 管理 → Node.js 子进程 (Mineflayer)
    └── 指标采集
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
  model/{user,group,node,instance,bot,alert,schedule,backup,template,audit}.go
  router/{router,auth,user,group,node,instance,terminal,bot,file,schedule,backup,alert,template,audit}.go
  service/{auth,user,group,node,instance,terminal,bot,schedule,backup,alert,template,audit,file,authz}.go
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
```

## 6. 通信协议

### 6.1 gRPC（Control Plane ↔ Worker Node）

Protobuf 定义位于 `proto/worker.proto`，包含：

- 生命周期：Register, Heartbeat (双向 stream)
- 实例操作：CreateInstance, StartInstance, StopInstance, RestartInstance, KillInstance, SendCommand, GetInstanceStatus, ListInstances
- 实例事件流：StreamInstanceEvents (server stream)
- 文件操作：ListFiles, ReadFile, WriteFile, DeleteFile, UploadFile (client stream), DownloadFile (server stream)
- 终端：IssueTerminalToken
- Bot：CreateBot, DeleteBot, ListBots, StreamBotEvents (server stream), SendBotCommand
- 指标：GetNodeMetrics, GetInstanceMetrics

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
- **控制命令**（`ChannelControl` + payload 文本）：`stop`（优雅停止 Java 进程树）、`kill`（强制）、`ping`（心跳，回 `pong`）。
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
| nodes | uuid, name, host, grpc_port, ws_port, secret, status(0/1/2), os, arch, cpu_cores, memory_mb, disk_total_mb, last_heartbeat |
| instances | uuid, node_id(FK), name, type, process_type, status, start_command, work_dir, env_vars(JSON), auto_start, auto_restart, docker_*, rcon_*, mc_*, tags(JSON) |
| group_instances | group_id, instance_id(UNIQUE) |
| bots | uuid, instance_id(FK), name, status, config(JSON), behavior, worker_id |
| backups | uuid, instance_id(FK), name, file_path, file_size_mb, type(0/1), status(0/1/2) |
| schedules | uuid, instance_id(FK), name, cron_expr, action, payload, enabled |
| schedule_execution_logs | schedule_id(FK), action, status, error, started_at, finished_at |
| alert_rules | uuid, name, target_type, target_id, metric, operator, threshold, duration_sec, notify_type, notify_target, enabled |
| alert_events | rule_id, target_id, value, message, resolved, fired_at |
| templates | uuid, name, type, description, start_command, download_url, config_files(JSON) |
| audit_logs | user_id, action, target_type, target_id, detail(JSON), ip |

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

### 8.2 全局布局

```
┌─────────────────────────────────────────────────────┐
│  Header（固定顶部）                                    │
│  [Logo]  JianManager         [通知] [暗色模式] [用户▼] │
├──────────┬──────────────────────────────────────────┤
│          │                                          │
│ Sidebar  │  Content（可滚动）                         │
│ （固定）   │                                          │
│          │  当前页面内容                               │
│ 仪表盘    │                                          │
│ 节点     │                                          │
│ 实例     │                                          │
│ Bot      │                                          │
│ ─────    │                                          │
│ 用户组   │                                          │
│ 用户     │                                          │
│ 模板     │                                          │
│ ─────    │                                          │
│ 定时任务  │                                          │
│ 备份     │                                          │
│ 审计日志  │                                          │
│ ─────    │                                          │
│ 设置     │                                          │
│          │                                          │
├──────────┴──────────────────────────────────────────┤
```

- Header 高度 56px，固定在顶部
- Sidebar 宽度 240px，可折叠到 64px（只显示图标）
- Content 区域自适应，最大宽度 1400px 居中
- 暗色/亮色主题切换

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

**操作按钮**: 启动(▶) / 停止(⏸) / 重启(⟳) / 强制终止(🗑)
**点击实例名** → 实例详情页（Tabs: 控制台 / 终端 / 文件 / 配置 / 备份 / Bot）

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
**Tab: 配置** — 实例配置表单（启动命令、自动重启、环境变量等）
**Tab: 备份** — 备份列表 + 创建备份 + 恢复
**Tab: Bot** — 该实例关联的 Bot 列表

#### 创建实例（对话框）

```
┌──────────────────────────────────────────┐
│  创建实例                                 │
│                                          │
│  名称: [Survival Server          ]       │
│  节点: [node-01              ▼]          │
│  类型: [Minecraft Java        ▼]         │
│  启动方式: [daemon (推荐)     ▼]         │
│  工作目录: [/servers/survival    ]       │
│  启动命令: [java -Xmx2G -jar paper.jar] │
│                                          │
│  ☐ 跟随节点自动启动                       │
│  ☑ 崩溃自动重启                           │
│                                          │
│  用户组: [Team A                  ▼]     │
│                                          │
│       [取消]              [创建]         │
└──────────────────────────────────────────┘
```

#### Bot 管理 `/bots`

```
┌──────────────────────────────────────────────────────┐
│  Bot 管理                     [+ 创建 Bot] [压测]     │
│                                                      │
│  ┌─────────────────────────────────────────────────┐ │
│  │ 名称       │ 目标服务器   │ 状态    │ 行为   │ 操作│ │
│  │ Guard-01  │ Survival    │ 🟢连接  │ guard  │ ⏹⚙ │ │
│  │ Scout-01  │ Survival    │ 🟡连接中│ patrol │ ⏹⚙ │ │
│  │ AFK-01    │ Creative    │ 🔴错误  │ idle   │ ⏹⚙ │ │
│  └─────────────────────────────────────────────────┘ │
│                                                      │
│  Bot 详情（点击展开）                                  │
│  ┌─────────────────────────────────────────────────┐ │
│  │ 血量: 20/20  饥饿: 18/20  位置: x:100 y:64 z:200│ │
│  │ 维度: overworld  行为: guard → 巡逻中             │ │
│  │                                                 │ │
│  │ [切换行为: follow / guard / patrol / idle]       │ │
│  │ [发送命令]                                       │ │
│  └─────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

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

### 8.5 前端嵌入

前端通过 `go:embed all:dist` 嵌入 Control Plane 二进制。开发模式下 Gin 反代到 Vite dev server。

### 8.6 目录结构

```
web/src/
  api/          # Axios client + per-module API (TanStack Query hooks)
  ws/           # WebSocket client, provider, hooks
  stores/       # Zustand (auth, theme, sidebar, terminal)
  pages/        # 页面（全部 React.lazy 懒加载）
  components/   # 共享组件 (layout, ui/shadcn, terminal, chart)
  hooks/        # 自定义 hooks
  i18n/         # 中文 + 英文
  lib/          # 工具函数
  router.tsx
  route-permissions.ts
```

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

**Control Plane**: `control-plane.yaml` — server port, gRPC port, database, JWT secret（管理员账号通过首次启动 Web 引导创建，见 FR-017）
**Worker Node**: `worker.yaml` — node name, Control Plane address, gRPC/WS ports, servers_dir, Docker, Bot 配置

## 12. 部署

**开发**: `go run ./cmd/control-plane --dev` + `cd web && npm run dev`
**生产**: 多节点部署，Control Plane 一个 + Worker Node 多个
**Docker**: `Dockerfile.control-plane` + `Dockerfile.worker` + `docker-compose.yml`
