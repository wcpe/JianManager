# PRD — JianManager

> 产品需求文档 | 增量演进 | FR 状态流转

---

## FR 状态定义

| 状态 | 含义 |
|---|---|
| 📋 todo | 已定义，未开始 |
| 🔨 in-progress | 开发中 |
| ✅ done | 已完成 |
| ❌ deprecated | 已废弃（保留记录） |
| ⏸️ deferred | 延后到下个版本 |

---

## P0 — 核心功能

### FR-017: 首次启动引导流程
- **状态**: ✅ done
- **优先级**: P0
- **描述**: Control Plane 首次启动时，Web UI 引导管理员设置用户名和密码，替代配置文件/环境变量 bootstrap 方式
- **验收标准**:
  - [x] 后端：`GET /api/v1/setup/status` 返回是否需要初始化
  - [x] 后端：`POST /api/v1/setup` 创建管理员并返回 JWT Token（幂等，已存在返回 409）
  - [x] 前端：无 token 时检测 setup 状态，需初始化则跳转 `/setup` 引导页
  - [x] 前端：引导页表单含用户名、密码、确认密码，提交后自动登录进入 Dashboard
  - [x] 删除旧的 `bootstrapAdmin` 启动逻辑和 `bootstrap` 配置段
  - [x] 已有管理员的旧版升级无影响（setup 状态为 false，正常进入登录页）
- **关联 API**: `GET /setup/status`, `POST /setup`
- **Spec**: `docs/specs/first-launch-setup/`

### FR-001: 用户认证
- **状态**: ✅ done
- **优先级**: P0
- **描述**: JWT 双 Token 认证（15min access + 7d refresh），支持注册/登录/Token 刷新
- **验收标准**:
  - [x] 注册接口，密码 bcrypt 加密存储
  - [x] 登录返回 accessToken + refreshToken
  - [x] accessToken 过期后用 refreshToken 自动刷新
  - [x] 前端 401 时自动跳转登录页
- **关联 ADR**: 无
- **关联 API**: `POST /auth/login`, `POST /auth/register`, `POST /auth/refresh`

### FR-002: 用户与权限管理
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 平台管理员/组管理员/组成员三级角色，基于权限节点的 RBAC
- **验收标准**:
  - [x] 平台管理员可管理所有用户和节点
  - [x] 组管理员可管理组内成员和实例分配
  - [x] 组成员只能操作分配给自己的实例
  - [x] 权限中间件拦截未授权请求
- **关联 API**: `GET/POST /users`, `GET/POST /groups`, `POST /groups/:id/members`

### FR-003: 用户组与配额
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 用户组管理，实例分配给组，配额限制（最大实例数、Bot 数、存储空间）
- **验收标准**:
  - [x] 创建/编辑/删除用户组
  - [x] 组内添加/移除成员
  - [x] 实例分配给组（一个实例只属于一个组）
  - [x] 配额检查：创建实例时校验组配额
- **关联 API**: `POST /groups`, `POST /groups/:id/instances`, `GET /groups/:id/quota`

### FR-004: 节点注册与心跳
- **状态**: ✅ done
- **优先级**: P0
- **描述**: Worker Node 启动时 gRPC 注册到 Control Plane，30s 心跳上报资源指标
- **验收标准**:
  - [x] Worker 首次启动自动注册，获得 node_uuid + node_secret
  - [x] 30s 心跳间隔，上报 CPU/内存/磁盘
  - [x] Control Plane 检测离线（超 90s 无心跳）
  - [x] 前端节点列表实时显示在线状态
- **关联 API**: `GET /nodes`, `GET /nodes/:id`

### FR-005: 实例生命周期管理
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 实例创建/启动/停止/重启/销毁，状态机驱动，支持四种启动方式
- **验收标准**:
  - [x] 创建实例：选择节点、类型、启动方式、启动命令
  - [x] 启动/停止/重启/强制终止操作
  - [x] 状态机：STOPPED → STARTING → RUNNING → STOPPING → STOPPED / CRASHED
  - [x] 崩溃自动重启（指数退避）
  - [x] 实例分配给用户组
- **关联 API**: `POST /instances`, `POST /instances/:id/start`, `POST /instances/:id/stop`

### FR-006: 守护进程（Daemon Wrapper）
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 平台进程重启不杀游戏服，通过 Daemon Wrapper 子进程实现进程隔离
- **备注**: 按 ADR-003 真实现：IProcessCommand 策略路由（direct/daemon/docker）、wrapper 子进程进程组隔离、Unix Socket/Named Pipe + 二进制帧协议、PID 文件恢复、daemon 模式优雅退出不杀游戏服。单元/集成测试覆盖帧协议、PID 恢复、StopAll 优雅断开；「Worker 退出后游戏服存活」真机验证待主控执行
- **验收标准**:
  - [x] 启动方式为 daemon 时，spawn 独立子进程管理游戏服
  - [x] 二进制帧协议通信（Unix Socket / Named Pipe）
  - [x] 平台重启后恢复守护进程连接
  - [x] 崩溃自动重启 + PID 文件恢复
- **关联 ADR**: ADR-003

### FR-007: 终端实时
- **状态**: ✅ done
- **优先级**: P0
- **描述**: xterm.js 浏览器终端，直连 Worker Node WebSocket，支持多人同时查看
- **验收标准**:
  - [x] Control Plane 签发一次性 30s token
  - [x] 浏览器持 token 直连 Worker Node WS
  - [x] stdin/stderr 双向流
  - [x] 多人同时查看（读写分离）
  - [x] 环形缓冲区回放最近输出
- **关联 API**: `GET /instances/:id/terminal-token`

### FR-008: 文件管理
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 实例工作目录文件浏览/编辑/上传下载
- **验收标准**:
  - [x] 文件列表浏览（目录树）
  - [x] CodeMirror 在线编辑（YAML/TXT/JSON 高亮）
  - [x] 文件上传（分块）/ 下载（流式）
  - [x] 创建/删除/重命名
- **关联 API**: `GET /instances/:id/files`, `GET /instances/:id/files/read`, `POST /instances/:id/files/write`

---

## P1 — 重要功能

### FR-009: Bot 平台
- **状态**: ✅ done
- **优先级**: P1
- **描述**: Mineflayer Bot 管理，行为引擎、寻路、脚本执行、压测、预热池
- **验收标准**:
  - [x] 创建/删除 Bot（选择目标 MC 服务器）
  - [x] 行为模式切换（follow/guard/patrol/idle/custom）
  - [x] 寻路（mineflayer-pathfinder）
  - [x] 脚本执行 + 进度上报
  - [x] 压测会话（批量上线/下线）
  - [x] 预热池（预创建空闲 bot）
  - [x] 容量：50 bots/worker，256 workers max
- **关联 API**: `POST /bots`, `POST /bots/:id/behavior`, `GET /bots/:id/state`

### FR-010: 监控指标
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 节点和实例指标采集，Recharts 仪表盘展示
- **验收标准**:
  - [x] 节点指标：CPU/内存/磁盘/网络（周期采集）
  - [x] 实例指标：MC TPS/在线玩家/内存（MC 专用）
  - [x] 仪表盘页面：Recharts 图表
- **关联 API**: `GET /nodes/:id/metrics`, `GET /instances/:id/metrics`

### FR-011: 告警规则
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 阈值触发告警，Webhook 通知
- **验收标准**:
  - [x] 创建告警规则（metric + operator + threshold + duration）
  - [x] 触发后发送 Webhook
  - [x] 告警事件列表
- **关联 API**: `POST /alerts/rules`, `GET /alerts/events`

### FR-012: 定时任务
- **状态**: ✅ done
- **优先级**: P1
- **描述**: Cron 表达式调度，支持实例启停/命令执行/备份
- **验收标准**:
  - [x] 创建/编辑/删除定时任务
  - [x] Cron 表达式解析
  - [x] 支持 action: start/stop/restart/command/backup
  - [x] 执行日志
- **关联 API**: `POST /schedules`, `GET /schedules`, `GET /schedules/:id/logs`

### FR-013: 备份恢复
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 手动/自动备份，压缩存储，一键恢复
- **验收标准**:
  - [x] 手动创建备份
  - [x] 备份列表（大小/时间/类型）
  - [x] 一键恢复到指定备份
  - [x] 自动备份（通过定时任务，依赖 Scheduler 启动）
- **关联 API**: `POST /instances/:id/backups`, `POST /backups/:id/restore`

---

## P2 — 增强功能

### FR-014: 服务端模板
- **状态**: ✅ done
- **优先级**: P2
- **描述**: 预设 MC 服务端模板（Paper/Spigot/Forge），一键创建实例
- **验收标准**:
  - [x] 模板列表（名称/类型/描述/图标）
  - [x] 从模板创建实例（自动填充启动命令和配置）

### FR-015: 审计日志
- **状态**: ✅ done
- **优先级**: P2
- **描述**: 操作审计（谁/什么时间/对什么/做了什么）
- **验收标准**:
  - [x] 关键操作自动记录（实例启停/文件修改/用户管理）
  - [x] 审计日志查询（按用户/操作/时间筛选）

### FR-016: i18n
- **状态**: ✅ done
- **优先级**: P2
- **描述**: 中文 + 英文国际化
- **验收标准**:
  - [x] 前端 i18next 切换
  - [x] 所有 UI 文本可翻译

---

## V1 增强 — 运行时集成

> 以下 FR 用于完善已有功能的运行时集成，消除 TODO。

### FR-029: Worker Node 注册与心跳集成
- **状态**: ✅ done
- **优先级**: P0
- **描述**: Worker Node 启动时自动向 Control Plane 注册，周期性上报心跳指标，Control Plane 检测离线
- **验收标准**:
  - [x] Worker 启动后自动连接 Control Plane gRPC 端口并发送 Register 请求
  - [x] 注册成功后获得 node_uuid 和 node_secret
  - [x] 每 30s 发送一次心跳，携带 CPU/内存/磁盘指标
  - [x] Control Plane 超过 90s 未收到心跳标记节点为离线
  - [x] Worker 断线后自动重连 Control Plane
  - [x] 前端节点列表显示在线/离线状态和实时指标
- **依赖**: FR-004（节点注册与心跳）
- **关联 API**: gRPC Register, Heartbeat

### FR-018: 实例 gRPC 生命周期操作
- **状态**: ✅ done
- **优先级**: P0
- **描述**: Control Plane 通过 gRPC 委托 Worker Node 执行实例的创建、启动、停止、重启、销毁操作；实例状态变更通过 StreamInstanceEvents gRPC 流经 CP SSE 代理推送到前端
- **验收标准**:
  - [x] 前端创建实例 → Control Plane → gRPC CreateInstance → Worker 创建进程
  - [x] 前端启动实例 → Control Plane → gRPC StartInstance → Worker 启动进程
  - [x] 前端停止实例 → Control Plane → gRPC StopInstance → Worker 停止进程
  - [x] 实例状态变更通过 StreamInstanceEvents 实时推送到前端（当前用轮询替代）
  - [x] 操作失败时前端显示错误信息
- **依赖**: FR-029（Worker 注册）
- **关联 API**: gRPC CreateInstance, StartInstance, StopInstance, RestartInstance

### FR-019: 终端 WebSocket 全链路
- **状态**: ✅ done
- **备注**: 链路可跑通，但 `terminal_proxy` baseURL 硬编码 `ws://localhost`（生产不可用），且实现为 CP 代理而非「浏览器直连 Worker」

### FR-020: 文件管理 gRPC 全链路
- **状态**: ✅ done
- **备注**: 后端读写/上传下载/删除已实现，缺少 rename；前端用 textarea 替代 CodeMirror，无上传 UI

### FR-021: Bot Mineflayer 集成
- **状态**: ✅ done
- **优先级**: P1
- **描述**: Bot Worker 通过 Mineflayer 连接 Minecraft 服务器，支持行为引擎（follow/guard/patrol/idle）
- **验收标准**:
  - [x] 创建 Bot 后 Bot Worker 通过 Mineflayer 连接目标 MC 服务器
  - [x] 连接成功后 Bot 状态变为 connected
  - [x] 切换行为模式（follow/guard/patrol/idle）后 Bot 行为改变
  - [x] follow 模式跟随目标玩家移动
  - [x] guard 模式在固定位置警戒
  - [x] Bot 断开连接后状态变为 disconnected
- **依赖**: FR-009（Bot 平台）
- **关联 API**: POST /bots, POST /bots/:id/behavior

### FR-022: RCON 指标采集
- **状态**: ✅ done
- **优先级**: P1
- **描述**: Worker Node 通过 RCON 协议查询 Minecraft 服务器的 TPS 和在线玩家数
- **验收标准**:
  - [x] 实例运行时 Worker 通过 RCON 连接查询 TPS
  - [x] 实例运行时 Worker 通过 RCON 查询在线玩家列表
  - [x] 前端实例详情页显示 TPS 和在线玩家数
  - [x] RCON 连接失败时优雅降级（显示 N/A）
- **依赖**: FR-010（监控指标）
- **关联 API**: GET /instances/:id/metrics

### FR-023: gRPC 客户端真实实现
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 替换 proto/workerpb 中的桩代码，实现真实的 gRPC 客户端和服务端 RPC 调用
- **备注**: 核心 RPC 真实，但残留桩：Worker `Register` 返回 `NodeSecret:"placeholder"`、`IssueTerminalToken` 返回 unimplemented
- **验收标准**:
  - [x] Worker 启动后能成功向 Control Plane 注册（Register RPC 返回真实 node_uuid）
  - [x] Worker 每 30s 发送心跳，Control Plane 更新节点指标
  - [x] Control Plane 通过 gRPC 启动/停止 Worker 上的实例
  - [x] Control Plane 通过 gRPC 查询 Worker 上的文件列表
  - [x] gRPC 调用超时后正确返回错误
- **依赖**: FR-029（Worker 注册心跳）
- **关联 API**: gRPC Register, Heartbeat, StartInstance, StopInstance, ListFiles
- **Spec**: `docs/specs/grpc-real/`

### FR-024: 前端对接运行时 API
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 前端页面对接 FR-029~022 的真实 API，实现完整的前后端联调
- **验收标准**:
  - [x] 节点列表页面显示在线节点的实时 CPU/内存/磁盘指标（30s 自动刷新）
  - [x] 实例详情页终端 Tab 能连接 Worker WebSocket 并显示终端输出
  - [x] 实例详情页文件 Tab 能浏览/编辑 Worker 上的文件
  - [x] 实例详情页显示 TPS 和在线玩家数（依赖 FR-022 RCON）
  - [x] 创建/启动/停止实例操作能通过 gRPC 委托给 Worker 执行
  - [x] Bot 管理页面能创建 Bot 并显示连接状态
- **依赖**: FR-023（gRPC 真实实现）
- **关联 API**: GET /nodes/:id/metrics, WS /ws/terminal, GET /instances/:id/files
- **Spec**: `docs/specs/frontend-runtime/`

### FR-025: Worker→Control Plane gRPC 连通性修复
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 修复 Worker Node 无法连接 Control Plane gRPC 端口（9100）的问题，确保注册和心跳链路畅通
- **验收标准**:
  - [x] Control Plane 启动后 gRPC Server 监听 9100 端口（`netstat` 可见）
  - [x] Worker 启动后成功注册到 Control Plane，日志显示 `注册成功 nodeUUID=xxx`
  - [x] Worker 每 30s 发送心跳，Control Plane `nodes` 表 `last_heartbeat` 字段持续更新
  - [x] Control Plane 未启动时 Worker 启动不 panic，日志显示重连等待
  - [x] Worker 断线后自动重连 Control Plane，恢复心跳
  - [x] 前端节点列表显示 Worker 为「在线」状态
- **依赖**: FR-023（gRPC 客户端真实实现）
- **关联 API**: gRPC Register, Heartbeat

### FR-026: 前端 shadcn/ui 标准化
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 将前端页面从手写样式迁移到 shadcn/ui 组件库默认样式，统一视觉风格
- **验收标准**:
  - [x] 所有表格使用 `<Table>` 组件替代手写 `<table>` 标签
  - [x] 所有按钮使用 `<Button>` 组件（variant: default/destructive/outline/ghost）
  - [x] 所有对话框使用 `<Dialog>` 组件替代手写 modal
  - [x] 所有表单输入使用 `<Input>` / `<Select>` / `<Checkbox>` 组件
  - [x] 所有状态标签使用 `<Badge>` 组件（variant: default/success/warning/destructive）
  - [x] 页面标题使用 `<h1>` + shadcn 排版规范，间距统一
  - [x] 暗色/亮色主题切换正常，无样式错乱
- **依赖**: FR-024（前端对接运行时 API）

### FR-027: API 集成测试
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 为核心 REST API 编写集成测试，使用 httptest + 真实 SQLite 数据库
- **验收标准**:
  - [x] 认证 API 测试：注册→登录→刷新 token→401 拦截
  - [x] 实例 API 测试：创建→查询→启动→停止→删除（happy path + 错误路径）
  - [x] 节点 API 测试：列表→详情→删除离线节点
  - [x] 用户组 API 测试：创建→添加成员→设置配额→超额拒绝
  - [x] 每个测试使用独立 SQLite 数据库，测试间隔离
  - [x] `go test ./internal/controlplane/...` 全部通过
- **依赖**: FR-025（gRPC 连通性修复）

### FR-028: 实例创建 E2E 测试
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 端到端验证「管理员创建实例并启动」的完整流程，覆盖前端→Control Plane→Worker 全链路
- **验收标准**:
  - [x] 启动 Control Plane + Worker 进程
  - [x] 通过 API 创建管理员账号（setup 流程）
  - [x] 通过 API 创建实例并分配到 Worker 节点
  - [x] 通过 API 启动实例，验证状态变为 RUNNING
  - [x] 通过 API 停止实例，验证状态变为 STOPPED
  - [x] 通过 API 删除实例
  - [x] 测试脚本可一键运行（`make e2e` 或 `go test -tags=e2e`）
- **依赖**: FR-025（gRPC 连通性修复）, FR-027（API 集成测试）

### BUG-001: 实例创建-启动-终端全链路断裂
- **状态**: ✅ done
- **优先级**: P0
- **描述**: FR-005/FR-018/FR-019 标记 done 但实际链路断裂，5 个 bug 导致实例无法启动、终端无法输出
- **关联 FR**: FR-005, FR-018, FR-019
- **Spec**: `docs/specs/instance-lifecycle-fix/`

---

## V1 Bug 修复与 UX 增强

> 以下 FR 用于修复第一期交付后的实际使用问题和前端 UX 标准化。

### BUG-002: 终端连接状态闪烁
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 进入实例终端 Tab 时，先显示 [连接错误] [连接已断开] 再显示「已连接」，体验差。根因：Terminal 组件在 token 加载完成前就渲染并尝试 WebSocket 连接
- **验收标准**:
  - [x] token 未加载完成时，终端区域显示加载占位（spinner 或骨架屏），不显示终端
  - [x] token 加载完成后才创建 WebSocket 连接，不再出现 [连接错误]
  - [x] 连接断开时仅显示一次 [连接已断开]，不重复
  - [x] WebSocket 连接失败时自动重试（最多 3 次，间隔递增）
- **关联 FR**: FR-007（终端实时）, FR-019（终端 WebSocket 全链路）

### BUG-003: 实例详情页控制台与终端 Tab 合并
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 实例详情页同时存在「控制台」和「终端」两个 Tab，两者都渲染 xterm 终端，功能重复。应合并为一个 Tab：上方显示实例指标（TPS/在线/内存），下方为可交互终端
- **验收标准**:
  - [x] 仅保留一个「终端」Tab，移除原「控制台」Tab
  - [x] 该 Tab 上方显示实例状态指标（TPS、在线玩家、内存，仅 RUNNING 状态显示）
  - [x] 该 Tab 下方为终端区域，RUNNING 状态时可输入命令，其他状态只读
  - [x] 状态 banner（CRASHED 红色/STARTING 黄色）保留在指标区域下方
  - [x] 不改变其他 Tab（配置/文件/备份/Bot）的布局
- **关联 FR**: FR-007（终端实时）, FR-010（监控指标）

### BUG-004: 文件浏览器 workDir 空值 422 错误
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 实例创建时未指定工作目录（workDir 为空），进入文件 Tab 时 API 返回 422 错误。应做前后端防御：创建实例时 workDir 必填，文件浏览器在 workDir 为空时显示友好提示
- **验收标准**:
  - [x] 创建实例对话框中 workDir 字段改为必填，为空时表单不可提交
  - [x] 创建实例对话框提供 workDir 的默认值建议（如 `/opt/mc-server` 或模板的默认目录）
  - [x] 文件浏览器在 workDir 为空时显示提示信息而非 422 错误
  - [x] 后端 ListFiles 在实例 workDir 为空时返回明确的错误信息
- **关联 FR**: FR-005（实例生命周期）, FR-008（文件管理）

### BUG-005: 启动命令多余引号导致执行失败
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 实例配置的启动命令被附带多余单引号，导致 Windows 执行时报错 `'\"C:\...\java.exe\"' 不是内部或外部命令`。需排查引号来源（前端提交/后端存储/Worker 执行）并修复
- **验收标准**:
  - [x] 前端提交的 startCommand 不包含额外引号包裹
  - [x] 后端存储和返回的 startCommand 保持原样
  - [x] Worker 执行 startCommand 时不会额外添加引号
  - [x] 启动命令含空格路径时能正确执行（如 `C:\Program Files\java.exe -jar server.jar`）
  - [x] 前端配置 Tab 显示的启动命令和创建时填写的一致
- **关联 FR**: FR-005（实例生命周期）, FR-006（守护进程）

### FR-030: 前端通知系统与 UX 标准化
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 引入标准 Toast 通知组件替换隐藏 div 通知方式；弹窗和输入使用模态框（Dialog）；禁止随意改变页面布局
- **验收标准**:
  - [x] 安装并集成 Toast 通知库（如 sonner），全局挂载 `<Toaster>` 组件
  - [x] 实例操作（启动/停止/重启）使用 Toast 通知：「实例启动中…」「实例已停止」等
  - [x] 文件操作（保存/上传/删除/重命名）使用 Toast 通知反馈结果
  - [x] 错误消息统一使用 Toast error 样式显示，替代内联 error div
  - [x] 删除确认、文件重命名等交互使用 Dialog 模态框，替代 `window.confirm()`
  - [x] 所有表单输入在模态框内完成，不改变页面主布局
  - [x] 现有页面布局（侧边栏/头部/内容区域）不发生变化
- **依赖**: BUG-002（终端修复完成后避免冲突）
- **关联 FR**: FR-026（shadcn/ui 标准化）

---

## V1 不包含（后续版本）

| FR | 描述 | 预计版本 |
|---|---|---|
| FR-100 | MFA（TOTP 二步验证） | V1.1 |
| FR-101 | Control Plane 高可用 | V1.2 |
| FR-102 | 真正的多租户（tenant_id 隔离） | V2.0 |
| FR-103 | 插件桥（Bukkit 插件 WS 连入） | V1.1 |
| FR-104 | JVM 诊断（Arthas/JFR/JMX） | V1.2 |
| FR-105 | 邮件通知 | V1.1 |
| FR-106 | WebSocket 用户→Control Plane（全局事件推送） | V1.1 |
