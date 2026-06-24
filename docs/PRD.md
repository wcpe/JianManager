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
- **状态**: 🔨 in-progress（归真 2026-06-24：前端 GroupsPage 当前只读，「编辑/删除/配额/成员」UI 缺失、`useDeleteGroup` 未接入；由 FR-156 兑现）
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
- **描述**: 节点和实例指标采集，Recharts 仪表盘展示；实例指标经 ServerProbe 探针抓取，覆盖 TPS/MSPT/堆/线程/CPU/世界负载等富指标（ADR-014）
- **验收标准**:
  - [x] 节点指标：CPU/内存/磁盘/网络（周期采集）
  - [x] 实例指标：MC TPS/在线玩家/内存（MC 专用）
  - [x] 仪表盘页面：Recharts 图表
  - [x] 富指标：经 ServerProbe `/metrics` 抓 MSPT/线程/CPU/世界负载（探针未部署时回退基础三项）
- **关联 API**: `GET /nodes/:id/metrics`, `GET /instances/:id/metrics`
- **关联 ADR**: ADR-014

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
- **状态**: ✅ done（v0.6.0；2026-06-22 真机验收通过：create→编辑→启停切换→删除 端到端验过；后端 CRUD/执行日志 go test + 前端 tsc/lint/vitest 全绿）
- **优先级**: P1
- **描述**: Cron 表达式调度，支持实例启停/命令执行/备份
- **验收标准**:
  - [ ] 创建/编辑/删除定时任务（前端：创建对话框→`POST /schedules`、行内编辑→`PUT /schedules/:id`、删除危险确认→`DELETE /schedules/:id`、启停切换）
  - [x] Cron 表达式解析（后端）
  - [x] 支持 action: start/stop/restart/command/backup（后端）
  - [ ] 执行日志（前端：行展开/抽屉调 `GET /schedules/:id/logs`，列时间/结果/输出）
  - [ ] 定时任务页套 FR-061 高密度风格
- **关联 API**: `POST /schedules`, `GET /schedules`, `PUT /schedules/:id`, `DELETE /schedules/:id`, `GET /schedules/:id/logs`

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
- **状态**: ✅ done（v0.6.0；2026-06-22 真机验收通过：筛选栏 user/action/类型/时间全控件；操作筛选 player.kick→1 条真机验过）
- **优先级**: P2
- **描述**: 操作审计（谁/什么时间/对什么/做了什么）
- **验收标准**:
  - [x] 关键操作自动记录（实例启停/文件修改/用户管理）
  - [x] 审计日志查询（前端筛选栏：用户/操作/目标类型/时间范围/加载更多 → `GET /audit?userId=&action=&targetType=&from=&to=&limit=`；时间按 RFC3339 透传）
  - [x] 审计页套 FR-061 高密度风格

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
- **状态**: ❌ deprecated（FR-067 / ADR-016 退役 RCON——指标改纯 ServerProbe 探针，RCON 采集链路已移除；探针未部署时富指标 N/A 而非回退 RCON）
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

### BUG-006: 已登录用户硬刷新被弹回登录页
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 登录后硬刷新页面（或直接打开受保护深链如 `/settings`）会被弹回 `/login`，即便 localStorage 中有有效 token。根因：`stores/auth.ts` 初始化 `isAuthenticated:false`，token 仅由 `App.tsx` 的 `useEffect→loadFromStorage()` 异步载入，`AuthGuard` 在首帧（effect 执行前）即重定向到 `/login`，且 `LoginPage` 不会把已登录用户弹回。修复：auth store 从 localStorage 同步初始化 + LoginPage 已登录跳回 `/`
- **验收标准**:
  - [x] 登录后硬刷新页面，停留在控制台，不被弹回 `/login`
  - [x] 已登录时直接打开 `/settings` 等受保护深链，正常渲染
  - [x] 退出登录后回到 `/login`
  - [x] 已登录时访问 `/login` 自动跳回 `/`
  - [x] access token 过期仍能经 401 拦截器刷新（无回归）
- **关联 FR**: FR-037（运维控制台布局，受此 bug 影响）

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

## V2 — MC 群组服运维

> 围绕「开好并运维一个 MC 群组服（代理 + 多 Bukkit 子服）」。优先级：配置文件管理最高，其次搭子服 / 插件管理 / 搭代理 / 一键复制修正。关系模型见 ADR-007，启动与运行时见 ADR-008。

### FR-031: 配置文件管理引擎
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 统一管理 MC 全部配置文件——保留注释的多格式读写 + schema 化可视编辑 + 跨文件一致性校验 + 版本回滚
- **验收标准**:
  - [x] 支持 properties/yaml/toml/json/txt 解析与回写，**保留原有注释、键顺序与格式**
  - [x] 内置 server.properties / spigot.yml / paper-global.yml / bukkit.yml / velocity.toml / bungeecord config.yml 字段 schema（类型/默认/说明），可视化表单编辑
  - [x] 表单与原始文本双模式切换，保存后注释不丢失
  - [x] 跨文件/跨实例校验：同节点端口唯一、online-mode 与代理转发配套、forwarding secret 跨代理一致，违规实时提示
  - [x] 每次保存生成配置版本，可查看 diff 并一键回滚
  - [x] 配置读写经 gRPC 委托 Worker（配置在 workDir，归 Worker 所有）
- **备注**: 真机浏览器复验通过——真 BungeeCord config.yml（yaml）与真 Paper server.properties（properties，保留 Mojang 注释头）表单字段级补丁保存、文本/表单切换、跨实例校验、版本 diff/回滚；properties/yaml/toml 补丁单测覆盖
- **关联 ADR**: ADR-007
- **关联 API**: `GET/POST /instances/:id/configs`, `POST /instances/:id/configs/write-fields`, `GET /instances/:id/configs/:file/versions`
- **依赖**: 无（地基）
- **Spec**: `docs/specs/config-engine/`

### FR-032: 节点资源分配与群组服关系模型
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 实例角色化 + proxy↔backend 的 M:N 注册 + 可选群组软标签 + 端口/工作目录由系统分配
- **验收标准**:
  - [x] 实例具备 `role`（proxy / backend / universal）
  - [x] **工作目录由系统分配**：创建对话框移除 workDir 输入，系统在 `servers_dir` 下建 `servers/<name-slug>-<shortid>`，路径只读展示（取代 BUG-004 的必填 UI）
  - [x] 端口池：为新实例自动分配同节点唯一的 server-port/rcon/query，可查看占用情况
  - [x] proxy↔backend 为 **M:N**：一个 backend 可注册进多个 proxy；每条注册含本地 alias/priority/forced-host/restricted
  - [x] 群组（Network）为**非独占软标签**：一个子服可属于多个群组；删除群组不影响子服与注册
  - [x] 群组视图可按标签筛选并批量操作（启停/同步）
- **备注**: 真机端到端复验通过（隔离栈跑通端口分配 25565/25566/25567、M:N 注册、系统分配 workDir）
- **关联 ADR**: ADR-007
- **关联 API**: `GET/POST /networks`, `POST /proxies/:id/registrations`, `GET /nodes/:id/ports`
- **依赖**: FR-031（注册/复制会写代理配置）
- **Spec**: `docs/specs/network-resource-model/`

### FR-033: JDK 与运行时管理
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 平台按节点托管多 JDK，安装/登记多版本，实例绑定并在启动时注入环境变量
- **验收标准**:
  - [x] 节点 JDK 注册表：列出已装 JDK（vendor/版本/arch/路径）
  - [x] 一键安装指定版本 JDK（下载源可配，默认 Adoptium）到系统分配目录；也可登记系统已有 JDK
  - [x] 实例可绑定具体 JDK 或 Java 大版本；目标节点缺失时提示安装
  - [x] 启动实例时自动注入 `JAVA_HOME` 并将 JDK/bin 接入 `PATH`，再叠加实例自定义 `env_vars`
  - [x] 删除被实例占用的 JDK 时拒绝并提示占用方
- **备注**: 真机浏览器复验——节点 JDK 面板列出 4 个 JDK、向导默认绑定最高 JDK、真 Paper 启动注入 GraalVM22；下载源经 `JIANMANAGER_JDK_<VENDOR>_BASE` 可配（单测覆盖），删除占用拒绝单测覆盖
- **关联 ADR**: ADR-008
- **关联 API**: `GET/POST /nodes/:id/jdks`, `DELETE /nodes/:id/jdks/:jid`
- **依赖**: 无（FR-034/035 依赖它）
- **Spec**: `docs/specs/jdk-runtime/`

### FR-034: 搭建 Bukkit 子服
- **状态**: ✅ done（v0.3.0）
- **优先级**: P1
- **描述**: 向导式创建 Paper/Spigot/Purpur 后端子服，自动下载核心、系统分配目录与端口、写好群组服配置、结构化启动
- **验收标准**:
  - [x] 选择核心类型 + MC 版本，从核心仓库/下载源获取 jar
  - [x] 系统自动分配工作目录与端口；自动写 `eula=true`、`online-mode=false`、spigot `bungeecord=true` / paper 代理转发
  - [x] 绑定 JDK、设置内存与 JVM 参数（结构化启动，不手填命令）
  - [x] 创建后可一键启动，状态进入 RUNNING
  - [ ] 可选：创建时即注册进所选代理（延后至 FR-035 代理集成）
- **关联 ADR**: ADR-007, ADR-008
- **关联 API**: `POST /instances`（role=backend, 结构化启动）, `GET /cores`
- **依赖**: FR-031, FR-032, FR-033
- **Spec**: `docs/specs/provision-bukkit/`

### FR-035: 搭建代理（BungeeCord/Velocity）
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 向导式创建代理实例，生成转发配置与 secret，注册后端
- **验收标准**:
  - [x] 选择 BungeeCord/Waterfall 或 Velocity 并获取 jar
  - [x] 系统分配目录/监听端口，生成 config（BC: `ip_forward=true` / Velocity: modern 转发 + 生成 `forwarding-secret`）
  - [x] 将已有 backend 注册进代理（servers + priorities/try），支持 forced-host
  - [x] Velocity secret 自动下发到所注册后端的 paper 配置，并校验跨代理一致
  - [x] 启动代理后玩家可经代理进入后端
  - [ ] Velocity 代理在无 forced-host 的干净配置下也能启动（生成的 `velocity.toml` 显式输出空 `[forced-hosts]` 覆盖 Velocity 内置示例默认，避免引用不存在的 server）
- **备注**: 真 Paper 1.20.4 + 真 BungeeCord 26.1 端到端复验通过——Mineflayer 客户端经代理（25566）进入后端 lobby（`ServerConnector [lobby] connected` + 后端 `ProxyTester joined`）。追加可选 online-mode（持久化，离线模式群组服可关闭）。Velocity 干净启动曾因 `buildVelocityToml` 省略 `[forced-hosts]` 段、被 Velocity 默认示例（factions/minigames.example.com）污染而崩溃，已修复（始终输出空 `[forced-hosts]`）并补回归单测；此前端到端 E2E 仅覆盖 BungeeCord，Velocity 干净启动真机复验待补。
- **关联 ADR**: ADR-007, ADR-008
- **关联 API**: `POST /instances`（role=proxy）, `POST /proxies/:id/registrations`
- **依赖**: FR-031, FR-032, FR-033
- **Spec**: `docs/specs/provision-proxy/`

### FR-036: 一键复制子服 + 配置修正 + 注册
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 复制一个后端子服为独立新实例，自动修正身份配置并按需注册进所选代理
- **验收标准**:
  - [x] 复制产出**独立**新实例（系统分配新目录/新端口）
  - [x] 拷贝 workDir 时排除 session.lock、logs、缓存、usercache 等运行态文件
  - [x] 配置引擎自动修正：新 server-port/rcon/query、服务器名/motd，可选改 level-name；保留 forwarding secret 不变
  - [x] 复制时可勾选注册进 0/1/多个代理（写入各代理 servers + priorities）
  - [x] 复制前预检端口/名称/目录冲突并提示
  - [x] 复制后新子服可直接启动并经所选代理进入
- **备注**: 真机端到端复验通过——克隆 lobby→lobby2（新端口 25567/新目录，世界已拷、运行态已排除、端口/rcon 已修正），独立启动后经代理进入（`ServerConnector [lobby2] connected` + 克隆 `ProxyTester joined`，与源同坐标证明世界忠实复制）。
- **关联 ADR**: ADR-007
- **关联 API**: `POST /instances/:id/clone`
- **依赖**: FR-031, FR-032, FR-033, FR-034, FR-035
- **Spec**: `docs/specs/clone-instance/`

---

## 运维控制台与 Bot 规模化管理

> 把实例终端与 Bot 联动统一进「运维控制台」三段式布局（上=功能导航 / 中=节点切换+实例树 / 下=系统平台导航 / 右=工作区）。Bot 在压测场景下可达上万（容量见 FR-009：50 bots/worker × 256 workers），因此所有 Bot UI 一律「聚合优先、永不全量铺开」。Bot 内核（CRUD/行为/压测/预热）已在 FR-009、FR-021 完成，本批为运维 UX + 规模化 API/UI。

### FR-037: 运维控制台布局
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 以「运维控制台」三段式侧栏替换现有主布局，右侧工作区点实例开单个终端；分屏/分组/导播台为后续阶段。本 FR 范围内追加系统设置页（`设置` 导航接入 `/settings`）
- **验收标准**:
  - [x] 登录后默认进入运维控制台；左侧栏三段：上方功能导航（仪表盘/节点/实例/Bot/告警…）、中部节点下拉+实例树、下方系统平台导航（用户/审计/设置/退出）
  - [x] 节点下拉含「全部节点」+各节点；选「全部」时实例树按节点分组，选某节点时只列该节点实例（复用 `GET /instances?nodeId=`）
  - [x] 实例树每项显示状态点（RUNNING 绿 / STARTING·STOPPING 琥珀 / CRASHED 红 / STOPPED 空心）
  - [x] 点击实例在右侧工作区打开其终端（复用一次性 token + xterm），点另一个实例切换终端
  - [x] 顶部工具栏显示面包屑（运维控制台 / 实例名）；「分屏」「切导播台」为禁用占位
  - [x] 其余页面（节点/用户/审计…）仍按路由在工作区渲染，原有导航按钮全部保留
  - [x] 暗色/亮色主题与 i18n 正常，无样式错乱
- **备注**: 真机复验通过（短视口三段不重叠、设置页、登录态硬刷新由 BUG-006 修复）
- **关联 ADR**: ADR-009（运维控制台为主 Shell）
- **关联 API**: `GET /nodes`, `GET /instances?nodeId=`, `GET /instances/:id/terminal-token`（均已存在）
- **依赖**: FR-007（终端实时）, FR-005（实例生命周期）, FR-026（shadcn/ui 标准化）

### FR-038: Bot 规模化后端 API
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 为 Bot 在上万数量级下提供可扩展的查询与操作 API：列表分页+筛选、聚合摘要、批量操作。替换现有 `GET /bots` 一次性返回扁平数组的实现
- **验收标准**:
  - [x] `GET /bots` 支持分页（page/pageSize）与筛选（instanceId/nodeId/status/behavior/关键字），返回当前页 + 总数
  - [x] `GET /bots/summary` 返回全局或按 `groupBy=instance|node|status|behavior` 的计数聚合，不返回逐条 bot
  - [x] `POST /bots/batch` 支持按 id 列表或筛选条件批量执行 set-behavior / stop / start / delete，返回成功/失败计数（上限 5000、并发 16）
  - [x] 批量操作经 gRPC 委托对应 Worker（CP 侧信号量分片扇出），请求不被阻塞
  - [x] 沿用 `bot:*` 权限与跨组隔离：组成员仅能查询/操作有权实例下的 bot（越权 id 计入 skipped）
  - [x] 1 万级 bot 下分页与摘要接口不全量序列化（DB 聚合），响应时延可接受
- **关联 ADR**: ADR-002（gRPC 节点通信）
- **关联 API**: `GET /bots`（扩展分页/筛选）, `GET /bots/summary`（新增）, `POST /bots/batch`（新增）
- **依赖**: FR-009（Bot 平台）

### FR-039: 控制台实例内 Bot 管理段
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 运维控制台工作区为单实例提供「终端 | Bot」切换，Bot 段聚合优先（概览+筛选/分组+分页+批量），实例树挂 bot 聚合徽标
- **验收标准**:
  - [x] 实例树每个实例显示 bot 聚合徽标（在线/总数），不展开为逐个 bot（数据来自 `GET /bots/summary`）
  - [x] 工作区提供「终端 | Bot」切换；Bot 段顶部为状态概览卡片（总计/在线/连接中/异常）
  - [x] Bot 段支持搜索、按状态/行为筛选与分组，列表分页加载，不一次性渲染全部
  - [x] 可对当前筛选集或选中集批量设行为/停止/删除（调 `POST /bots/batch`）
  - [x] 单 bot 行内可设行为/停止/重连/删除（「重连」映射为 start 重新上线）
  - [x] 从该实例新建 bot 时，实例 id 预填、连接地址用「所在节点 host + 默认端口」预填且可改（FR-032 端口分配落地后可自动填实际 server-port）
- **关联 API**: `GET /bots`, `GET /bots/summary`, `POST /bots/batch`, `POST /bots`, `POST /bots/:id/behavior`, `DELETE /bots/:id`
- **依赖**: FR-037（控制台布局）, FR-038（Bot 规模化 API）

### FR-040: 全局 Bot 管理页重构
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 重构 `/bots` 为跨实例 Bot 总览与管理页（导航位于「实例」与「告警」之间），按实例/节点/状态分组、健康条、批量、与控制台联动
- **验收标准**:
  - [x] 导航「Bot」按钮位于「实例」与「告警」之间，点击进入全局 Bot 页
  - [x] 页顶全局概览卡片（总计/在线/连接中/异常）+ 分布（X 实例·Y 节点）
  - [x] 默认按实例分组的总览（每行：实例/节点/健康条/总数/批量），可切换分组维度（实例/节点/状态/行为）
  - [x] 支持全局搜索与按节点/状态筛选；展开某实例才按行为/状态细分并分页
  - [x] 每行可批量操作；每行「在控制台打开」跳到该实例的 Bot 段
  - [x] 顶部提供「新建 Bot」「压测」入口（压测复用 FR-009 既有后端；完整编排 UI 见 FR-042）
- **关联 API**: `GET /bots/summary?groupBy=`, `GET /bots`, `POST /bots/batch`
- **依赖**: FR-038（Bot 规模化 API）
- **备注**: 分组级健康条当前为「在线/其余」两段；细分至 connecting/error 比例需 `GET /bots/summary` 下推分组级 byStatus（后续小增量）

### FR-041: Bot 实时遥测与单 Bot 详情面板
- **状态**: ⏸️ deferred
- **优先级**: P2
- **描述**: 将 Bot 实时事件（StreamBotEvents：血量/饥饿/位置/聊天）从 Worker 经 Control Plane 推送到浏览器，提供单 bot 详情面板
- **验收标准**:
  - [ ] Control Plane 将 Worker 的 StreamBotEvents 经 SSE/WS 代理推送到浏览器（参照实例事件 SSE）
  - [ ] 点击单个 bot 打开详情面板，实时显示血量/饥饿/位置/行为
  - [ ] 显示 bot 聊天/事件日志滚动流
  - [ ] 面板可向 bot 发送指令（SendBotCommand）
  - [ ] 仅订阅当前查看的 bot/实例，避免上万 bot 全量推送
- **关联 API**: SSE `/instances/:id/bots/events`（新增）, gRPC StreamBotEvents / SendBotCommand（已存在）
- **依赖**: FR-039（控制台 Bot 段）, FR-009（Bot 平台）

### FR-042: Bot 压测会话编排 UI
- **状态**: ⏸️ deferred
- **优先级**: P2
- **描述**: 为既有压测后端（FR-009）提供前端编排：创建压测会话（目标实例+数量）、批量上线/下线、按会话聚合监控
- **验收标准**:
  - [ ] 从全局 Bot 页「压测」入口创建会话：选目标实例 + bot 数量 + 初始行为
  - [ ] 启动会话后 bot 批量上线，页面按会话聚合显示上线进度与状态分布
  - [ ] 可结束会话批量下线
  - [ ] 压测会话作为一个聚合单元展示，不逐个铺开上万 bot
- **关联 API**: 复用 FR-009 压测后端 + FR-038 摘要/批量
- **依赖**: FR-038（Bot 规模化 API）, FR-040（全局 Bot 页）, FR-009（Bot 平台）

---

## 自包含便携运行时

> 平台运行态数据收纳进单一项目内数据根，FHS 式标准目录布局，JDK/服务器/配置/核心缓存均在根内、便携可迁移。为进行中的 FR-032/033/034 提供统一存储底座，是 FR-043 真实开服的前提。

### FR-044: 项目自包含便携运行时（FHS 数据根 + 核心缓存）
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 平台所有运行态数据统一收口到单一项目内数据根，采用 FHS 式标准目录；便携 JDK、服务器工作目录、配置、下载的核心 jar 缓存均在根内；整体可迁移
- **验收标准**:
  - [x] 单一数据根（默认项目内 `./data`，可经 `JIANMANAGER_DATA_DIR` 覆盖），首启按布局自动初始化
  - [x] FHS 式子目录：`bin/`、`etc/`（平台配置）、`opt/jdks/<vendor>-<ver>/`（便携 JDK）、`var/servers/<slug>-<shortid>/`（服务器工作目录）、`var/log/`、`var/artifacts/`（制品库/资产，内容寻址，见 FR-045）、`cache/`（临时：下载中转/解压）
  - [x] JDK 便携安装进 `opt/jdks/`，取代当前硬编码 `<serversDir>/jdks`（细化 FR-033，路径全在根内）
  - [x] 实例工作目录由系统在 `var/servers/` 下按 slug+shortid 分配，创建时不手填路径（与 FR-032 一致，取代 BUG-004 必填）
  - [ ] 下载的 Paper/核心 jar 入**制品库**（FR-045，内容寻址 + md5/sha256 校验），建服优先命中、缺失才下载（底座 IngestFromURL 已就绪，命中消费侧待 FR-034）
  - [ ] 制品库中的核心可登记为模板来源，与 FR-014 模板打通（待 FR-014）
  - [x] 数据根整体拷贝到另一机器后登记仍自洽（按根相对解析，不写死绝对路径）
- **关联 ADR**: ADR-010（项目自包含便携运行时目录布局，待创建；细化 ADR-007/008）
- **关联 API**: 细化 `GET/POST /nodes/:id/jdks`、`POST /instances`（系统分配目录）、`GET /cores`
- **依赖**: 细化并先于 FR-032/033/034；是 FR-043 真实开服的运行时底座
- **备注**: 与 FR-032/033/034（均 in-progress，FR-033 约 80%、JDK 当前装在 `<serversDir>/jdks`）重叠；本 FR 只承接「自包含便携 + FHS 布局」这层底座，三者存储路径统一收口到本数据根，不重复其功能

---

## 制品库与资产存储

> 平台所有二进制资产（服务器核心、插件、图片、媒体 blob…）统一进内容寻址的制品库，带 md5/sha256 完整性校验，可去重、可追溯、可复用。核心 jar 是第一类资产，模型同样容纳后续插件 / 图片 / 媒体。

### FR-045: 制品库（内容寻址 + 完整性校验）
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 平台资产统一进内容寻址（sha256）的制品库，附 md5/sha256 校验与类型化元数据；存于数据根 `var/artifacts/`，DB 索引；核心 jar 为首类资产，模型可扩展到插件 / 图片 / 媒体
- **验收标准**:
  - [x] `assets` 表：`type`(core|plugin|image|video|archive|blob)、`name`、`version`、`filename`、`sha256`(寻址+去重键)、`md5`、`size`、`content_type`(MIME)、`source_url`、`metadata`(JSON)、`storage_state`(hot|archived|external)、`storage_backend`、`ref_count`、`created_at`、`last_used_at`
  - [x] **按类型分区**的内容寻址存储：资产落 `var/artifacts/<type>/<sha256 前 2 位>/<sha256>.<ext>`；类型内相同内容只存一份（去重），不同类型物理分目录，便于浏览/整类备份/归档
  - [x] 入库即算 sha256+md5；来源提供校验和则比对，不符拒绝入库
  - [x] API：`GET /assets`（按 type 筛选）、`GET /assets/:id`、`POST /assets`（上传/登记），下载入库（建服时自动 ingest 核心）
  - [x] 引用保护：被模板/实例引用的资产删除前拒绝并提示占用方（`ref_count`）
  - [x] **归档就绪**：按 类型/冷热/占用 可选择性归档——归档或外置只改 `storage_state`+`storage_backend`+存储位置，DB 记录与引用（sha256）不变；整类归档由类型分区直接支持（归档策略与外置存储后端为后续 FR，模型先留位）
  - [ ] 核心走制品库后，FR-034 建服优先命中、缺失才下载；FR-014 模板可引用制品库核心（消费侧待 FR-034/FR-014）
  - [x] 类型可扩展：新增 plugin/image/video/blob 仅差元数据/校验，不改存储层
- **关联 ADR**: ADR-011（制品库内容寻址与资产模型，待创建）
- **关联 API**: `GET/POST /assets`, `GET /assets/:id`
- **依赖**: FR-044（数据根提供 `var/artifacts/`）；被 FR-034（建服取核心）、FR-014（模板）复用
- **备注**: 取代 FR-044 原「核心缓存」表述——核心 jar 升级为制品库第一类资产；后续插件管理（参见 FR-103）、图片/媒体库均复用本库

---

## 全链路运维打通

> 把已交付的节点/实例/终端/Bot 各 FR 在真实场景下端到端打通并验收，达到「能正常运维一台 MC 服务器」的标准。这是集成与验收 FR，不引入新功能内核；过程中发现的断点按 BUG-xxx 拆出修复。

### FR-043: 全链路运维打通（节点→实例→终端→Bot 进服）
- **状态**: ✅ done（v0.3.0）
- **优先级**: P0
- **描述**: 打通并验收「节点在线 → 创建并启动 MC 实例 → 终端交互 → 创建 Bot 并真正进入该实例服务器」的完整运维链路，达到可真实运维一台服务器的标准；含闭合 FR-019 终端生产连通性（`ws://localhost` 硬编码）
- **验收标准**:
  - [x] 节点：Worker 注册在线，前端显示在线 + 实时 CPU/内存/磁盘指标
  - [x] 实例：经平台创建并启动一个真实 MC 实例，状态进入 RUNNING，终端可见服务端启动日志
  - [x] 终端：浏览器终端连上并执行命令（如 `list`）、看到输出；连接地址不再硬编码 `ws://localhost`，非本机环境可用（闭合 FR-019 备注）
  - [x] Bot 进服：创建指向该运行实例的 Bot，Bot 实际加入服务器——服务端 `list` / 在线玩家数可见该 Bot，前端 Bot 状态变 `connected`
  - [x] 运维闭环：在终端对该 Bot 下发可见交互（服务端 `say` / Bot 行为切换），再停止实例，Bot 随之断开、状态正确回落 `disconnected`
  - [x] 可复现：全链路可在「一键」脚本或文档化步骤下复现（扩展 FR-028 E2E 覆盖到终端交互 + Bot 进服）
  - [x] 可诊断：任一 hop 失败有明确定位（节点离线 / 实例未起 / 终端 token 失败 / Bot 连接被拒）
- **关联 ADR**: ADR-002（gRPC 节点通信）, ADR-007（群组服关系，Bot 连接目标）
- **关联 API**: gRPC Register / StartInstance / IssueTerminalToken / CreateBot, WS 终端, `GET /instances/:id/terminal-token`
- **依赖**: FR-004, FR-005, FR-007, FR-009, FR-019, FR-021, FR-024（均 done，本 FR 做集成打通与验收）
- **备注**: 与 FR-028（实例 E2E）、BUG-001（实例-启动-终端断裂）重叠；本 FR 扩展到「终端交互 + Bot 进服」的完整运维标准并闭合 FR-019 生产连通性。与运维控制台 UI（FR-037~040）相互独立，可并行；验证可用现有 `/bots` 页，不必等新 UI。

---

## 运维全功能扩展（日志 / 插件 / 玩家 / 备份增强 / 批量 / 节点维护）

> 本批由需求盘点拆出（见 `.tmp/brainstorm-ops-platform-expansion-2026-06-20.md`）。横切约束：**每条 FR 的验收都含 i18n(FR-016) 完整性 + 暗色/亮色主题(FR-026) 正常**。执行：地基优先 + 并行开发（`sdd-parallel-develop`，rebase 线性合并，Agent 不自标 done，逐条用户验收）。
>
> **验收记录（2026-06-20）**：10 条经 build 全绿（go build/vet/test、前端 tsc+vite）+ 浏览器 smoke（各页渲染无 console 错误、i18n zh/en 完整、FR-049/050 日志真实数据流端到端通）后用户验收 done。**真机复验（2026-06-20，真 Paper 1.21 + RCON + 平台 CP/Worker）**：① FR-054 发现并修复 RCON 鉴权包类型 bug（误用类型 2 致 kick/ban/whitelist 形同空操作），修复后真机踢出在线 Bot 成功（commit d1314b5）；② FR-056 发现并修复运行中实例备份因 world/session.lock 独占锁整体失败，修复后真机打包 186 文件/170MB 成功（commit 52a13c9）；③ FR-103 插件桥以真实 WS 客户端验证 token 鉴权+会话+CP 连接中转（真 Bukkit/BC Java 插件需 Maven+JDK17 构建，本环境缺，未构建）；④ FR-057 远程存储后端 S3/WebDAV/SFTP 经单测覆盖，真实 S3/SFTP/WebDAV 端点本环境不可达、live 传输未做。FR-059 已全量迁移至 DangerConfirm，移除 ConfirmDialog。
>
> **验收记录（2026-06-21，ServerProbe 监控探针 epic）**：用 ServerProbe 子模块取代自写插件桥作监控探针（ADR-014 取代 ADR-012）。**真机全链路 E2E（真 CP+Worker + 真 Paper 1.21.1，独立 data-e2e）**：经 CP `provision/bukkit` 建服 → 自动下发探针 jar(937KB)+生成 config.yml（metrics 开启于系统分配 probe 端口 29940）→ 实例启动后 ServerProbe `/metrics` 就绪 → CP `GET /instances/:id/metrics` 返回富指标(probeAvailable=true) → 浏览器实例详情页渲染 TPS=20.00/MSPT=0.17ms/内存 706·1024MB/线程 59/CPU 6.1%/运行时长/三世界负载表，无 console 错误、侧栏无残留「插件桥」入口。用户验收：① FR-010 富指标增强 done；② FR-103 / FR-055 确认退役（deprecated）。FR-057 真传补验经用户决定转 backlog（本机无 MinIO/SFTP/WebDAV 服务端）。

### FR-046: Sponge 子服支持
- **状态**: 📋 todo
- **优先级**: P2
- **描述**: 扩展建服向导，支持 SpongeVanilla / SpongeForge 后端子服，自动获取核心、系统分配目录与端口、结构化启动
- **验收标准**:
  - [ ] 建服向导核心类型新增 SpongeVanilla / SpongeForge，按 MC 版本从官方源获取核心 jar（优先制品库命中）
  - [ ] 系统分配工作目录与端口；写好对应代理转发配置
  - [ ] 绑定 JDK、结构化启动（沿用 FR-034，不手填命令）
  - [ ] 创建后可一键启动进入 RUNNING
  - [ ] i18n + 主题正常
- **关联 ADR**: ADR-007, ADR-008
- **关联 API**: `POST /instances`（role=backend, type=sponge）, `GET /cores`
- **依赖**: FR-033, FR-034
- **Spec**: `docs/specs/provision-sponge/`

### FR-047: 环境/标签多维分组筛选
- **状态**: ✅ done
- **优先级**: P2
- **描述**: 实例支持环境维度（dev/test/prod）与多维分组/筛选视图，按群组(Network)/环境/标签任意组合过滤
- **验收标准**:
  - [ ] 实例可打环境标签（复用 Tags 字段或独立字段，Spec 定）
  - [ ] 实例列表/控制台树支持按 群组/环境/标签 组合筛选
  - [ ] 分组视图可按任一维度聚合
  - [ ] i18n + 主题正常
- **关联 ADR**: ADR-007
- **关联 API**: `GET /instances`（扩展筛选参数）
- **依赖**: FR-032
- **备注**: 与现有 Tags/Network 可能重叠，开发前先核验现状；若已覆盖则降为验证项

### FR-048: 节点维护模式与主动下线
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 节点支持维护模式（cordon：禁新调度）、排空（drain：停止其上实例）、维护标记与主动下线记录
- **验收标准**:
  - [ ] 节点可置「维护模式」，期间拒绝新建/分配实例到该节点并提示
  - [ ] 可对维护中节点排空：停止其上运行实例（迁移为后续可选）
  - [ ] 节点状态展示维护标记，区别于「离线」
  - [ ] 主动下线节点：解除注册并保留记录，复连需重新注册
  - [ ] 危险操作经二次确认（FR-059）+ 审计（FR-015）
  - [ ] i18n + 主题正常
- **关联 API**: `POST /nodes/:id/maintenance`, `POST /nodes/:id/drain`, `DELETE /nodes/:id`
- **依赖**: FR-004

### FR-049: 日志持久化、归档与保留
- **状态**: ✅ done
- **优先级**: P0
- **描述**: 自包含日志方案——采集实例 stdout 与平台运行日志入库，按大小/时间归档到数据根 `var/log`，保留策略可配，**不引入外部 ELK**
- **验收标准**:
  - [ ] 实例运行日志（stdout/stderr）经 Worker 采集并经 gRPC 上报/落库
  - [ ] 平台（Control Plane/Worker）结构化日志持久化，含级别/时间/来源
  - [ ] 归档：超阈值（大小/时间）日志滚动归档到 `var/log`，旧档可清理
  - [ ] 保留策略可配（保留天数/总量上限）
  - [ ] 数据根整体迁移后日志路径自洽（根相对）
- **关联 ADR**: ADR-005（单二进制，不引 ELK）, ADR-010（数据根布局）
- **关联 API**: 内部采集 + `GET /logs`（见 FR-050）
- **依赖**: FR-044

### FR-050: 日志检索与过滤
- **状态**: ✅ done（v0.4.0；随 FR-049 日志中心一并交付，2026-06-20 验收记录已载「FR-049/050 日志数据流端到端通」，发版时校正 stale 状态）
- **优先级**: P0
- **描述**: 前端日志中心 + API，对持久化日志按实例/节点/级别/关键字/时间范围分页检索与过滤，支持导出
- **验收标准**:
  - [x] `GET /logs` 支持筛选（instanceId/nodeId/level/keyword/from/to）+ 分页，DB 侧过滤不全量序列化
  - [x] 前端日志中心页：筛选器 + 分页表 + 实时/历史切换
  - [x] 关键字全文检索（基于 DB 能力，必要时建索引）
  - [x] 可导出当前筛选结果
  - [x] 权限：组成员仅见有权实例日志（沿用 RBAC）
  - [x] i18n + 主题正常
- **关联 API**: `GET /logs`, `GET /logs/export`
- **依赖**: FR-049

### FR-051: 通用文件改前自动备份与版本回滚
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 对经编辑器保存或上传覆盖的任意文件，修改前自动快照，提供版本列表/diff/一键回滚（FR-031 仅覆盖配置文件，本 FR 推广到全部文件）
- **验收标准**:
  - [ ] 编辑保存或上传覆盖已存在文件前，自动生成该文件版本快照
  - [ ] 文件版本列表（时间/大小/操作者），可查看 diff
  - [ ] 一键回滚到指定版本
  - [ ] 版本存储进数据根，有保留上限
  - [ ] 经 gRPC 委托 Worker（文件归 Worker）
  - [ ] i18n + 主题正常
- **关联 API**: `GET /instances/:id/files/versions`, `POST /instances/:id/files/rollback`
- **依赖**: FR-008, FR-044
- **备注**: 与 FR-031 配置版本机制对齐/复用，避免重复实现

### FR-052: 插件/模组单服管理
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 单实例插件/模组列表展示与状态识别、上传（入制品库 type=plugin）、删除、启用/禁用
- **验收标准**:
  - [ ] 列出实例 plugins/mods 目录插件，识别启用/禁用状态
  - [ ] 上传插件：入制品库（FR-045, type=plugin, sha256 去重）并部署到实例
  - [ ] 删除插件（二次确认）
  - [ ] 启用/禁用（重命名/移动，不删除）
  - [ ] 危险操作二次确认（FR-059）+ 审计（FR-015）
  - [ ] i18n + 主题正常
- **关联 ADR**: ADR-011（制品库）
- **关联 API**: `GET/POST /instances/:id/plugins`, `DELETE /instances/:id/plugins/:name`, `POST /instances/:id/plugins/:name/toggle`
- **依赖**: FR-045, FR-008

### FR-053: 插件批量部署多服
- **状态**: 📋 todo
- **优先级**: P1
- **描述**: 从制品库选插件，批量部署到选定的多个实例，返回成功/失败汇总
- **验收标准**:
  - [ ] 从制品库（type=plugin）选一个或多个插件
  - [ ] 选目标实例集（按筛选或勾选），批量部署
  - [ ] 经 gRPC 扇出到各 Worker，返回每实例成功/失败 + 汇总
  - [ ] 权限隔离：仅部署到有权实例
  - [ ] 危险操作二次确认 + 审计
  - [ ] i18n + 主题正常
- **关联 API**: `POST /plugins/batch-deploy`
- **依赖**: FR-052, FR-058

### FR-054: 玩家管理（RCON）
- **状态**: ✅ done
- **备注**: FR-067（ADR-016）起治理执行路径由 RCON 改为 ServerProbe 插件桥——踢/封/解封/白名单经 `SendPluginCommand`→探针执行平台 API，在线列表经探针事件聚合（跨服）；功能不变、封禁台账保留，RCON 路径退役
- **优先级**: P1
- **描述**: 经 RCON 实现跨服在线玩家列表（BC 跨服感知，各后端 RCON 聚合）、踢出/封禁/解封/白名单，封禁记录持久化
- **验收标准**:
  - [ ] 在线玩家列表：聚合群组内各后端 RCON 的 list，标注所在子服（BC 跨服感知）
  - [ ] 踢出/封禁/解封：经 RCON 下发对应命令
  - [ ] 白名单增删与查询
  - [ ] 封禁记录持久化到 DB（玩家/原因/操作者/时间/范围），可查询
  - [ ] 操作经二次确认 + 审计
  - [ ] RCON 不可用时优雅降级提示
  - [ ] i18n + 主题正常
- **关联 ADR**: ADR-007（群组服）
- **关联 API**: `GET /players`, `POST /players/:name/{kick,ban,unban}`, `GET/POST /instances/:id/whitelist`, `GET /bans`
- **依赖**: FR-022, FR-035

### FR-055: 玩家管理插件桥增强
- **状态**: ❌ deprecated（2026-06-21；FR-103 退役后失去载体，玩家治理由 RCON 路径承担，未来真有实时事件需求再独立设计；ADR-014）
- **优先级**: P2
- **描述**: 原计划在 FR-103 插件桥之上提供实时玩家事件与精确跨服感知；FR-054 修复 RCON 鉴权 bug（commit d1314b5）后纯 RCON 路径已能真机踢出在线玩家，且 V1 暂无实时事件强需求，遂废弃。
- **关联 ADR**: ADR-014 取代 ADR-012

### FR-056: 增量备份
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 在全量备份基础上支持增量备份（相对上次备份的差异）+ 备份链 + 链式一键恢复
- **验收标准**:
  - [ ] 备份类型增加「增量」，记录父备份形成备份链
  - [ ] 增量仅打包变化文件（基于 mtime/哈希）
  - [ ] 恢复增量时按链回放（全量基 + 各增量）
  - [ ] 备份列表展示类型与链关系
  - [ ] i18n + 主题正常
- **关联 API**: `POST /instances/:id/backups`（扩展 type=incremental）, `POST /backups/:id/restore`
- **依赖**: FR-013

### FR-057: 备份远程存储
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 备份存储位置可配本地或远程（S3/SFTP/WebDAV），支持选择存储后端与远程恢复
- **验收标准**:
  - [ ] 存储后端可配：本地（默认）/ S3 兼容 / SFTP / WebDAV
  - [ ] 创建备份时可选目标存储位置
  - [ ] 远程备份可列出并一键恢复（拉回本地再恢复）
  - [ ] 凭证经 `${ENV_VAR}` 引用，不硬编码（config-files 规范）
  - [ ] 与制品库归档后端模型对齐（FR-045 storage_backend）
  - [ ] i18n + 主题正常
- **关联 API**: `GET/POST /backup-storages`, `POST /instances/:id/backups`（指定 storage）
- **依赖**: FR-013, FR-045

### FR-058: 实例批量操作
- **状态**: ✅ done
- **优先级**: P1
- **描述**: 对任意实例选择集批量执行命令 / 批量启动·停止·重启·强制关服，经 gRPC 扇出（参照 FR-038 Bot 批量）
- **验收标准**:
  - [ ] `POST /instances/batch` 支持按 id 列表或筛选条件，action=command/start/stop/restart/kill
  - [ ] 批量执行命令：向选定运行中实例经 RCON/终端下发
  - [ ] 经 gRPC 扇出（CP 侧信号量分片，上限+并发），返回成功/失败计数
  - [ ] 权限隔离：仅操作有权实例（越权 id 计入 skipped）
  - [ ] 危险操作（批量 kill/stop）二次确认 + 审计
  - [ ] i18n + 主题正常
- **关联 ADR**: ADR-002（gRPC 节点通信）
- **关联 API**: `POST /instances/batch`
- **依赖**: FR-005, FR-018

### FR-059: 危险操作保护体系化
- **状态**: 🔨 in-progress（归真 2026-06-24：单实例「强杀」零二次确认，与「删除/强制关服…均接入」验收不符；由 FR-138/139 兑现）
- **优先级**: P2
- **描述**: 统一封装破坏性操作的保护：二次确认、输入名校验、角色门禁（FR-030 仅做了删除确认弹窗）
- **验收标准**:
  - [ ] 统一危险操作确认组件：删除/强制关服/下线节点/批量破坏性操作均接入
  - [ ] 高危操作（删实例/删节点/批量 kill）要求输入名称二次校验
  - [ ] 角色门禁：组成员对越权范围的危险操作被拒
  - [ ] 所有危险操作审计留痕（FR-015）
  - [ ] i18n + 主题正常
- **关联 API**: 无（前端 + 既有 RBAC/审计）
- **依赖**: FR-030

### FR-103: 插件桥（Bukkit/BC 插件 WS 连入）
- **状态**: ❌ deprecated（2026-06-21；监控由 ServerProbe 探针取代，玩家治理由 RCON 路径覆盖，自写插件桥沉没成本不再维护；ADR-014）
- **优先级**: P2
- **描述**: 原为玩家治理实时通道与富监控指标设计的 Bukkit/BC 插件 WS 通道（ADR-012）；因实际需求被 RCON + ServerProbe 两条更轻路径覆盖而退役，jianmanager-bridge 源码、Worker `/ws/plugin-bridge`、gRPC `StreamPluginEvents`/`SendPluginCommand`、CP `plugin_bridge` service 与前端页一并移除。
- **关联 ADR**: ADR-012 → 被 ADR-014 取代

---

## 可观测性与面板体验

> 在已交付的 ServerProbe 富实时指标（FR-010/ADR-014）之上沉淀时序历史曲线（FR-060），并参考 baota 把前端按高密度运维面板重做信息密度与视觉（FR-061）。两者独立成 feature：观测后端与样式无关、可先行；观测仪表盘以新视觉组件实现。

### FR-060: 时序监控与历史曲线
- **状态**: ✅ done（v0.5.0）
- **优先级**: P1
- **描述**: 在 FR-010 实时指标基础上增加时序存储与历史曲线。Worker 在 30s 心跳里附带节点指标 + 每实例 ServerProbe 快照（含分世界负载），Control Plane 分级降采样持久化（ADR-013），前端总览/节点/实例三级历史图表
- **验收标准**:
  - [ ] 总览页跨节点聚合历史曲线（总 CPU/内存、总在线玩家、运行实例数），可切 1h/6h/24h/7d/30d/90d
  - [ ] 节点详情：节点 CPU/内存/磁盘/网络历史曲线 + 其上各实例 ServerProbe 指标（TPS/MSPT/堆/线程）对比
  - [ ] 实例详情：实例曲线（TPS/MSPT/堆 used·max/线程/CPU）+ 分世界负载曲线（已加载区块/实体/方块实体）
  - [ ] 时间区间切换自动选精度（近端 30s 原始、远端 5min/1h 降采样），曲线连续
  - [ ] 探针不可达时（回退 RCON 仅 TPS/在线，或全缺）曲线在该时段断点（null），不显示假值
  - [ ] 分世界指标随世界增减动态成列（来自 ServerProbe，无需插件）
  - [ ] 数据分档保留并自动清理：原始 ~48h、5min ~30d、1h ≥1 年（卷积与清理可验证）
  - [ ] Control Plane 重启后历史数据不丢失
  - [ ] 图表与时间区间选择器 i18n + 暗色/亮色主题正常
- **关联 ADR**: ADR-013（分级降采样时序存储）；ADR-014（ServerProbe 为实例指标源）
- **关联 API**: `GET /metrics/series`, `GET /metrics/overview`（新增）；扩展 gRPC `Heartbeat` 负载（每实例 ServerProbe 快照）
- **依赖**: FR-010（ServerProbe 富实时指标，done）、FR-004（节点心跳）
- **Spec**: `docs/specs/timeseries-metrics/`

### FR-061: 面板信息密度与视觉改造
- **状态**: ✅ done（v0.5.0）
- **优先级**: P1
- **描述**: 参考 baota 把前端重做为高密度运维面板——常驻多级侧栏、环形资源仪表盘、分区面板、密集表格与状态色系；纯前端重构，不改后端行为
- **验收标准**:
  - [ ] 全站采用统一高密度档位（间距/字号/行高），总览页一屏同时呈现概览指标、历史曲线与实例列表
  - [ ] 常驻多级侧栏替换现有三段式布局；实例快速访问（实例树）与节点切换器保留、不丢失现有能力；用户/组/审计收入菜单
  - [x] 节点/总览页提供环形资源仪表盘（CPU/内存/磁盘，**负载拆至 FR-062**），数值按阈值变色
  - [ ] 列表页为密集表格 + 行内「操作」链接 + 状态徽章 + 迷你资源条，CPU/内存/TPS 按阈值自动变色
  - [ ] 引入状态色系（success/warning/danger/info）与 MC 绿主色，替代纯灰阶
  - [ ] 暗色/亮色两套主题下所有新组件均可读，无对比度问题
  - [ ] 仍基于 shadcn/ui + Tailwind + OKLCH，仅扩展 token + 新增高密度组件变体，不引入新 UI 框架
  - [ ] 改造不改变后端 API 与行为（架构不变量不受影响）
- **关联 ADR**: ADR-009（运维控制台 Shell，本 FR 在其内演进侧栏 IA）
- **关联 API**: 无（纯前端，复用现有 API + FR-060 的 `/metrics`）
- **依赖**: FR-037（运维控制台布局）, FR-060（历史仪表盘为旗舰画布）, FR-026（shadcn/ui 标准化）
- **Spec**: `docs/specs/panel-density-redesign/`
- **补全**: schedules/templates/audit 三页此前漏套高密度风格，随 FR-012 / FR-015 / FR-064 功能补齐时一并覆盖（2026-06-22 e2e 巡检发现）

### FR-062: 节点负载（load average）采集与仪表盘
- **状态**: ✅ done（v0.5.0）
- **优先级**: P2
- **描述**: 节点心跳采集系统负载（load average 1/5/15，gopsutil 跨平台；Windows 经处理器队列长度模拟），Control Plane 落时序，前端节点/总览补「负载」环形仪表盘与历史曲线。补齐 FR-061 验收 #3 中「负载」一项（验收时拆出）
- **验收标准**:
  - [ ] Worker 心跳上报节点 load average（取 1m 为代表值），CP 落 `node_load` 时序
  - [ ] 节点/总览页「负载」环形仪表盘（按 CPU 核数归一后阈值变色）+ 历史曲线
  - [ ] Windows/Linux 均有值（Windows 经 gopsutil 模拟）；取不到时优雅留空不报错
  - [ ] i18n + 暗色/亮色主题正常
- **关联 ADR**: ADR-013（分级降采样时序）；ADR-014（指标源）
- **关联 API**: 扩展 gRPC `Heartbeat` 负载（节点 load）；复用 `GET /metrics/series`
- **依赖**: FR-060（时序存储/查询/前端图表）、FR-004（节点心跳）

---

## 控制台功能补全与平台设置

> 源于 2026-06-22 前端 e2e 截图巡检：发现 3 处「标 done 但前端漂移」（已归真 FR-012 定时任务、FR-015 审计筛选，并在 FR-061 备注 schedules/templates/audit 三页视觉覆盖），新增 2 项能力（FR-063 平台设置、FR-064 模板管理），并登记 2 个小瑕疵（BUG-007 图表告警、BUG-008 401 请求）。

### FR-063: 平台设置（全量平台配置可视化与运行时调整）
- **状态**: ✅ done（v0.6.0；2026-06-22 真机验收通过：分类 + 内部侧边栏 + 3 项真生效——JDK 镜像源、优雅停止端到端真机铁证；备份裁剪接线+单测）
- **优先级**: P2
- **描述**: 系统设置页此前仅主题/语言（客户端 localStorage）。新增服务端平台配置：后端键值存储 + `GET/PUT /settings`（仅平台管理员），前端按**分类 + 内部侧边栏**组织、暴露**全部平台配置**。可安全运行时调整的项落库即生效（覆盖 env/YAML 默认）并接到真实读取点（CP 内即时 / 跨进程下发 Worker）；启动固定/敏感项只读展示、敏感打码并标注「需改配置并重启」。
- **验收标准**:
  - [x] **先写 ADR**：配置覆盖优先级（DB > env > YAML 默认）—— ADR-015
  - [x] 后端 `platform_settings` 存储 + `GET /settings` + `PUT /settings`（RBAC：仅平台管理员）+ AutoMigrate + service 测试
  - [x] **只读展示**项（启动固定）：server host/port、gRPC 端口、数据库 driver/dsn、JWT secret（打码）、access/refresh TTL —— 展示当前生效值 + 「需改配置并重启」提示
  - [x] 敏感值不明文下发前端（secret 打码）
  - [x] i18n zh/en；暗色/亮色正常
  - [x] log.level 落库即时生效（slog LevelVar，CP 内真读取点；单测 + 真机）
  - [x] **可运行时生效项接到真实读取点**：JDK 镜像源 + 优雅停止超时跨进程下发 Worker（扩 InstallJDK/CreateInstance proto，重新生成）；默认备份保留天数接入 CP 定期裁剪任务
  - [x] **设置页分类**：全部设置按类目组织（外观 / 日志 / 运行时 / 备份 / 安全·系统）
  - [x] **设置页内部侧边栏**：页面内嵌侧栏在分类间切换
  - [x] 真机：改 JDK 镜像源 → worker 实下载走新源（假镜像 URL 验证）；改优雅停止超时 → worker 收 gracefulStopTimeoutSeconds=7 且进程 ~9s 退出（7s 档非 30s）；备份裁剪接线运行 + 3 单测；分类侧边栏切换正常
- **关联 ADR**: ADR-015
- **关联 API**: 新增 `GET /settings`、`PUT /settings`；扩 gRPC `InstallJDKRequest.mirror_base` / `CreateInstanceRequest.graceful_stop_timeout_seconds`
- **关联 API**: 新增 `GET /settings`、`PUT /settings`
- **依赖**: 无

### FR-064: 模板管理 UI 与模板删除
- **状态**: ✅ done（v0.6.0；2026-06-22 真机验收通过）
- **优先级**: P2
- **描述**: FR-014 仅做到「用模板建实例」（消费侧，已 done）；本 FR 补模板的 UI 管理——新建（接已有后端 `POST /templates` + 闲置的 `useCreateTemplate`）、删除（新增后端 `DELETE /templates/:id`）。模板与实例松关联（建实例时拷贝 startCommand），删除模板不影响已建实例。顺带按 FR-061 高密度风格重写模板页。
- **验收标准**:
  - [x] 模板页「新建模板」按钮 → 对话框（名称/类型/描述/启动命令/下载URL/默认工作目录）→ `POST /templates`
  - [x] 模板卡/行可删除（DangerConfirm 危险确认）→ 新增 `DELETE /templates/:id`
  - [x] 后端新增 `DELETE /templates/:id`（service + handler + 路由 + 测试）
  - [x] 模板页套 FR-061 高密度风格
  - [x] i18n zh/en
  - [x] 真机：新建模板 → 卡片显示 → 删除回到空态（:8099 端到端验过；后端 DELETE go test 覆盖）
- **关联 FR**: FR-014（用模板建实例，已 done）
- **关联 API**: 复用 `POST /templates`；新增 `DELETE /templates/:id`
- **依赖**: 无

### BUG-007: 监控图表在 0 尺寸容器渲染告警
- **状态**: ✅ done（v0.6.0；2026-06-22 真机验收通过）
- **优先级**: P2
- **描述**: 控制台反复出现 recharts `width(-1)/height(-1) ... should be greater than 0` 告警（×9）。根因：ResponsiveContainer 在隐藏/未激活分段或折叠面板（0 尺寸容器）内、以及自身首帧测量前渲染。修法：弃用 ResponsiveContainer，用 ResizeObserver 实测像素宽直接渲染 LineChart。
- **验收标准**:
  - [x] 控制台不再出现 recharts width/height 告警（:8099 干净 reload 零告警）
  - [x] 折叠面板/未激活分段内的图表切换后正常渲染、无持续空白
  - [x] 修复不破坏现有图表（总览 4 图真机正常渲染）
- **关联 FR**: FR-060（时序图表）, FR-061（面板）

### BUG-008: 前端存在 401 Unauthorized 资源请求
- **状态**: ✅ done（v0.6.0；2026-06-22 真机验收通过）
- **优先级**: P2
- **描述**: 页面加载期控制台出现一条 401 Unauthorized 资源请求。根因：localStorage 中已过期但可刷新的 access token 被加载期 authed 查询带出 401（拦截器再刷新重试）；SSE 经 fetch 绕过拦截器同样 401。修法：请求拦截器发前主动判过期并刷新（共享刷新闸去重）+ SSE 连接前 ensureFreshToken。
- **验收标准**:
  - [x] 定位 401 来源请求（加载期过期 token 抢跑 + SSE 绕拦截器）
  - [x] 修复（请求前主动刷新 + SSE 预刷新）
  - [x] 正常登录态下控制台无 401 报错（:8099 reload 验过）
- **关联 FR**: FR-001（认证）, FR-024（前端对接运行时 API）

---

## ServerProbe 治理桥与运营底座增强（4 里程碑程序）

> 源于 2026-06-22 sdd-brainstorming（四轮澄清 + MCSManager 对标）。21 FR（FR-065~085）/ 5 ADR（016~020），分 4 里程碑：**里程碑内并行、里程碑间串行**（前一个落 main 再开下一个）。**M1 必先行**（产出后续复用的编辑器壳/WS 通道/玩家事件）。计划全文 `.tmp/brainstorm-serverprobe-bridge-and-ux-2026-06-22.md`（不入库）。
> 横切约束：每条 FR 验收含 **i18n(FR-016) 完整 + 暗色/亮色(FR-026) 正常 + 真机验证**。
> **deprecated 校正**：FR-022（RCON 指标采集）随 FR-067 退役标 superseded；FR-103/FR-055 维持归档；FR-085 把 FR-105（邮件）作为告警通道提前纳入。

### 里程碑 M1 — 探针治理桥 + 退 RCON + 编辑基础 + 前端 UX

#### FR-065: 实时插件桥通道地基（探针反向 WS ↔ Worker）
- **状态**: ✅ done（已交付@v0.7.0；ADR-016 + worker/ws bridge 单测[token/会话/握手/scope]全过、proto protoc 重生成、栈内 StreamPluginEvents 流活。深层真机[真 Paper 起探针 BridgeClient 反向 WS 实连]待用户真实环境补验，本环境 JDK 嵌套布局受限）
- **优先级**: P0
- **描述**: 打通「ServerProbe fork 反向 WS 连入本机 Worker」的实时双向通道，为玩家事件/治理/在线更新/全状态查询铺底。复活并扩展 ADR-012 的 WS 通道（载体改为 ServerProbe 探针）
- **验收标准**:
  - [ ] 先写 **ADR-016**（取代 ADR-014「探针只读+RCON 治理」；更新 architecture-invariants 插件桥行指向 ADR-016）
  - [ ] 探针 fork 反向 WS 客户端：实例级 token 连入 `/ws/plugin-bridge?token=&instance=`，HS256 握手（scope+instance 校验），断线指数退避重连
  - [ ] Worker 重建 `/ws/plugin-bridge` + 「实例 UUID→会话」表（单活动会话顶替）+ token 校验（复用 JWT secret）
  - [ ] CP 签发插件桥 token（实例级，类比终端 token）；探针 config 下发携带 token+ws 地址
  - [ ] proto 一次铺齐桥全面（StreamPluginEvents/SendPluginCommand + 事件/命令/状态查询 message），workerpb 经 protoc 重新生成（禁 sed）
  - [ ] 真机：真 Paper + 探针 fork 连入真 Worker，日志见会话+心跳+重连
- **关联 ADR**: ADR-016（取代 ADR-014，复活并扩展 ADR-012）
- **依赖**: 无（epic 地基）

#### FR-066: 实时玩家事件 + 精确跨服感知
- **状态**: ✅ done（已交付@v0.7.0；e2e PlayerEventService 订阅活、玩家页+Live Events tab 浏览器验+降级、探针事件监听 compileKotlin 过。深层真机[真玩家 join/quit/chat/跨服事件流]待用户真实环境补验）
- **优先级**: P1
- **描述**: 玩家 join/quit/chat + BC 代理端跨服路由经探针实时推送到浏览器，提供在线玩家列表与事件流，精确感知玩家所在子服
- **验收标准**:
  - [x] 探针监听 Bukkit join/quit/chat（`BukkitPlayerEventListener`）+ BC 端跨服路由（`BungeePlayerEventListener`：PostLogin/ServerSwitch/PlayerDisconnect）→ 经 `BridgeClient.emitPlayerEvent` WS 上报（探针三模块 compileKotlin 过）
  - [x] Worker 解析结构化字段填充 `PluginEvent` → CP `PlayerEventService` 订阅 gRPC StreamPluginEvents → SSE `/instances/:id/players/events`（init 首帧 + player 增量）
  - [x] 前端在线玩家列表实时（SSE 名册）+ 标注所在子服 + 事件流面板；仅订阅当前实例（按实例 UUID 过滤）；未连入降级提示
  - [ ] 真机：真 Paper+真 BC，Bot/玩家 进/切/退/发言 → 前端实时见事件+正确子服（**待真机验**：本环境无真 Paper/BC + JDK21 仅作探针 compileKotlin 验证）
- **关联 API**: SSE `/instances/:id/players/events`（新增）, gRPC StreamPluginEvents
- **依赖**: FR-065

#### FR-067: 玩家治理迁到探针 + 退役 RCON 全链路
- **状态**: ✅ done（已交付@v0.7.0；RCON 全链路删除经全量 go test 验证、治理走 SendPluginCommand+探针 BridgeCommandHandler、FR-022 deprecated/FR-054 更新、玩家页文案修。深层真机[经探针真机踢/封]待用户真实环境补验）
- **优先级**: P1
- **描述**: 玩家治理（踢/封/解封/白名单/在线列表）从 RCON 切到探针通道，并完全移除 RCON 全链路；指标变纯探针
- **验收标准**:
  - [ ] 治理改走 CP→gRPC SendPluginCommand→Worker→WS→探针执行；在线列表改探针聚合（跨服）；封禁记录保留
  - [ ] **删 RCON 全链路**：worker metrics/grpc rcon、端口分配、schema 跨校验、GetInstanceMetrics 回退、配置 rcon 项、实例模型 RCON 字段（迁移安全可留列标 deprecated）
  - [ ] 指标纯探针：未部署=N/A + 「需部署探针」提示；RCON 单测删/改写为探针路径
  - [ ] PRD：FR-022 标 superseded、FR-054 治理路径更新为探针
  - [ ] 真机：经探针真机踢/封/解封/白名单生效
- **关联 ADR**: ADR-016
- **关联 API**: gRPC SendPluginCommand；`POST /players/:name/{kick,ban,unban}`（路径改探针）
- **依赖**: FR-065, FR-066

#### FR-068: 探针在线更新（推送即就位 + 下次重启生效）
- **状态**: ✅ done（已交付@v0.7.0；后端端点+service+单测、探针更新卡浏览器验[连接状态+内嵌版本 0.1.0+更新/更新并重启]。深层真机[真推送 jar→重启→新版本连入]待用户真实环境补验）
- **优先级**: P2
- **描述**: 平台「点一下」把最新探针 jar 推到实例，下次重启生效，可选一键推送并重启
- **验收标准**:
  - [ ] CP「更新探针」（单+批量）经 gRPC DeployServerProbe（已存在）推最新 jar；展示「下次重启生效」+ 版本对比；可选「推送并重启」
  - [ ] 在位/连接/版本状态在实例详情可见；批量 gRPC 扇出返回成功/失败
  - [ ] 真机：改 jar→点更新→jar 被替换→重启后新版本连入（版本号为证）
- **关联 API**: gRPC DeployServerProbe（复用）
- **依赖**: FR-065（弱）

#### FR-069: 实例导航与侧栏树形优化
- **状态**: ✅ done（已交付@v0.7.0；浏览器 UI 真机：节点下拉瘦身+By node/env/status 分组+树展开+状态点，零控制台错误。取舍：Network 轴改节点/环境/状态）
- **优先级**: P2
- **描述**: ConsoleSidebar 节点下拉瘦身 + 实例分组按钮改树形结构
- **验收标准**:
  - [ ] 节点下拉瘦身；实例分组改树形（节点/群组层级展开折叠，状态点保留）；折叠记忆（console store）
  - [ ] 大量实例不卡、不全量铺开；现有能力无损（点实例开终端/节点切换/bot 徽标）
- **依赖**: 无

#### FR-070: 文件管理资源管理器化 + 交互全集 + 编辑器基础 + Ctrl+S 历史
- **状态**: ✅ done（已交付@v0.7.0；浏览器 UI 真机：双栏+真实配置+多选+New/Upload/Download-zip/Delete/Paste/Select-All 工具栏；交互/Ctrl+S 历史 vitest 覆盖）
- **优先级**: P1
- **描述**: 文件管理改资源管理器双栏（左树右内容），CodeMirror 多格式高亮，标准资源管理器操作全集 + 拖拽/批量/多选，Ctrl+S 拦截记历史。**含共享「资源管理器+编辑器」组件**，供 FR-071/073/074/075/082/083/084 复用
- **验收标准**:
  - [ ] 资源管理器双栏（左树懒加载/右内容·编辑器）；CodeMirror 多格式高亮
  - [ ] 操作全集：新建文件/夹、重命名（补 gRPC rename）、删除、剪切/复制/粘贴、移动（树内拖拽）
  - [ ] 拖拽上传 + 按钮上传 + 批量上传；下载（单流式 + 多选/夹打 zip 流式，新增 endpoint）
  - [ ] 多选：shift 连选 + ctrl 点选 + 全选 → 批量删/下/移
  - [ ] Ctrl+S 拦截保存 + 记历史版本（接 FR-051）；历史抽屉 版本/diff/回滚；删除/覆盖二次确认（FR-059）
  - [ ] 真机：拖拽/批量/多选/重命名/剪切粘贴/移动 + Ctrl+S 存 + 回滚 端到端
- **关联 FR**: FR-008, FR-020（补 rename）, FR-051（版本/回滚）
- **依赖**: 无（共享组件地基）

#### FR-071: 配置管理资源管理器化 + 自动发现全部配置 + Ctrl+S 历史
- **状态**: ✅ done（已交付@v0.7.0；浏览器 UI 真机：收藏栏+自动发现 8 配置[含嵌套 plugins/ServerProbe/config.yml 递归]+复用 FR-070 组件）
- **优先级**: P1
- **描述**: 配置管理复用 FR-070 组件，自动发现实例 server 目录下全部实际配置文件，收藏 + 交互全集
- **验收标准**:
  - [ ] 复用 FR-070 组件；**自动发现实例 server 目录全部配置文件**（不限 schema 化）目录树呈现
  - [ ] schema 文件保表单/文本双模式（FR-031）；非 schema 纯文本高亮；Ctrl+S 存+配置版本（FR-031）；跨文件校验保留
  - [ ] 收藏功能（常用配置书签）；同享 FR-070 交互全集（重命名/多选/批量/拖拽/剪切粘贴/移动）
  - [ ] 真机：发现全部配置、编辑非 schema、Ctrl+S 存+版本回滚、收藏+重命名+多选生效
- **关联 FR**: FR-031（配置引擎）
- **依赖**: FR-070

#### FR-072: 创建/编辑模态框统一（高度自适应 + 可编辑下拉 + 校验 + 必填提示）
- **状态**: ✅ done（已交付@v0.7.0；浏览器 UI 真机：创建框必填标记+校验错误阻断+可编辑下拉[Type/Node/JDK/Group]+Create disabled+工作目录系统分配只读）
- **优先级**: P1
- **描述**: 所有创建/编辑模态框高度自适应（不顶满屏），系统可获取项改可编辑下拉框 + 校验 + 必填/选填明确
- **验收标准**:
  - [ ] 所有创建/编辑框 max-height+内部滚动，短视口可用；系统项改可编辑下拉(combobox)（节点/JDK/核心/版本/群组/代理/模板…）
  - [ ] 字段校验（必填+格式）+ 提交前阻断 + 错误内联；必填(*)/选填明确；覆盖所有创建框
  - [ ] 真机：短视口可滚动提交、下拉可选可改、必填拦截
- **关联 FR**: FR-030
- **依赖**: 无

### 里程碑 M2 — 编辑器迷你 IDE + 搜索索引 + 归档反编译 + 探针全状态

#### FR-073: 编辑器迷你 IDE 增强（CodeMirror）
- **状态**: 🔨 in-progress
- **优先级**: P2
- **描述**: 在 FR-070 共享编辑器上接 CodeMirror 全套编辑能力
- **验收标准**:
  - [ ] 快捷键全集 + 搜索/替换（正则可选）+ 撤销/重做 + 删除一行/复制一行 + 批量注释/取消（按文件类型注释符）
  - [ ] 快捷键与 Ctrl+S 历史保存统一不冲突；真机各能力实测
- **依赖**: FR-070

#### FR-074: 跨文件全文搜索与持久倒排索引
- **状态**: ✅ done（已交付@v0.9.0；自动化验收全绿 + **真机已验**：真 Worker 倒排索引，content 搜索关键字命中 3 文件含行号+片段、filename 快速打开命中 2 文件）
- **优先级**: P2
- **描述**: Worker 侧建并增量维护持久倒排全文索引，多文件关键字搜索 + 文件名快速打开 + 忽略规则
- **验收标准**:
  - [ ] 先写 **ADR-017**（引擎 bleve/SQLite FTS5；落 `var/index/`；增量；Worker 本地资产不碰 CP DB）
  - [ ] Worker 建+增量索引（忽略规则 glob）；CP→gRPC SearchFiles→Worker 查 → 命中文件+行+片段；文件名快速打开
  - [ ] 前端搜索面板（关键字→结果→跳转定位）；大目录响应可接受
  - [ ] 真机：建索引、搜命中、改文件增量更新、忽略规则生效
- **关联 ADR**: ADR-017
- **关联 API**: gRPC SearchFiles（新增）
- **依赖**: FR-070

#### FR-075: 归档浏览与反编译
- **状态**: ✅ done（已交付@v0.9.0；自动化验收全绿 + **真机已验**：真 jar 归档列 543 条目、经 JAVA_HOME JDK21 跑 CFR 0.152 反编译 .class 出真实 Java 源码）
- **优先级**: P1
- **描述**: 打开 jar/zip 浏览内部、查看内部文本、Worker 侧 CFR 反编译 class/jar 输出只读源码
- **验收标准**:
  - [ ] 先写 **ADR-018**（CFR 单 jar 打包/调用；只读+超时+体积上限+受控 exec）
  - [ ] 打开 jar/zip（Go archive/zip 列条目树）；查看内部文本流式到只读编辑器；树内展开归档为子树
  - [ ] 反编译 class/jar：Worker 经实例 JDK 跑 CFR → Java 源码流（超时+体积上限+失败降级）
  - [ ] 真机：打开真 plugin jar、看内部 plugin.yml、反编译 class 出源码
- **关联 ADR**: ADR-018
- **依赖**: FR-070

#### FR-076: 全量 Bukkit 状态探查（异步非侵入）+ WS 按需查询
- **状态**: ✅ done（已交付@v0.9.0；自动化验收全绿 + **真机已验**：真 Paper 1.21.1(Java 21)+活探针反向桥，按需采全 7 分区状态 server/worlds/jvm/classloader(27717 类)/scheduler/listeners，采集后服务器无异常）
- **优先级**: P2
- **描述**: 探针 fork 全量采集 Bukkit 内部状态（含 class 加载器），异步非侵入，经 WS 按需请求/响应返回
- **验收标准**:
  - [x] 探针扩展状态面：server/worlds/JVM/**class 加载器**/调度器/监听器等内部数据（探针 `BukkitServerStateCollector` + core `ServerStateSupport`）
  - [x] **有界 + 超时降级 N/A，绝不拖慢服务器**（沿用「只读优先，绝不成为事故源」）：主线程快照 3s 限时、大集合 `bounded` 裁剪、JVM/classloader 经 MXBean、子项 `runCatching` 兜底；classloader 不枚举类
  - [x] 经 WS 按需请求/响应（开 tab/刷新才查，复用 FR-065 `QueryServerState`/`query_state`）；轻指标仍走 /metrics；不支持项降级
  - [ ] 真机：真 Paper 按需拉全状态返回真实数据，TPS 无可感下降（**待真机验**，无 Paper/JDK21 环境）
- **关联 ADR**: ADR-016
- **依赖**: FR-065

#### FR-077: 「服务器状态」专属 tab
- **状态**: ✅ done（已交付@v0.9.0；自动化验收全绿 + **真机已验**：server-state 端点经活探针返回完整状态 JSON，无探针时优雅降级「探针未连入」）
- **优先级**: P2
- **描述**: 实例工作区新增「服务器状态」tab，展示整个 Bukkit 状态 + 内部 class 加载器等
- **验收标准**:
  - [x] 新增「服务器状态」tab；展示 FR-076 全量状态分区密集表（FR-061 风格）+ class 加载器专区（`ServerStateSegment`）
  - [x] 手动刷新（按需，不持续轮询，`refetchInterval: false`）；加载/未连入/采集失败态清晰；大数据超阈值折叠「展开全部」纯前端切片不卡
  - [ ] 真机：tab 渲染真 Bukkit 全状态、刷新生效、classloader 区有数据（**待真机验**，无 Paper/JDK21 环境）
- **依赖**: FR-076

### 里程碑 M3 — Docker 容器化 + 资源限额 + 部署/自更新（对齐 MCSManager 运营底座）

#### FR-078: Docker 容器化实例运行 + 镜像管理 + 端口映射
- **状态**: 🔨 in-progress
- **优先级**: P1
- **描述**: dockerStrategy 真实现（取代当前 ErrNotImplemented 占位），Worker 经本机 Docker SDK 管容器，镜像管理 + 端口映射
- **验收标准**:
  - [ ] 先写 **ADR-019**（Worker 经本机 Docker SDK 管容器，守架构边界——管本机容器、不暴露新网络面；镜像/端口/资源模型）
  - [ ] dockerStrategy 真实现：create/start/stop/kill/exec 经 Docker SDK
  - [ ] 镜像管理：拉取/列出/删除本地镜像，建实例选镜像（默认 Docker Hub，registry 可配）
  - [ ] 端口映射：容器↔宿主（复用 FR-032 端口池）；容器 stdio 接终端（attach）+ 日志采集（FR-049）；纳入状态机/监控/备份（卷挂载）
  - [ ] 真机：Docker 模式建+启 MC 实例（拉镜像→映射端口→终端见日志→可进服），停/删干净
- **关联 ADR**: ADR-019, ADR-003（docker 不在其范围，本 FR 落地）
- **依赖**: FR-005, FR-032
- **备注**: 当前 `internal/worker/process/docker.go` 为占位实现

#### FR-079: 实例级资源限额（Docker 模式）
- **状态**: 🔨 in-progress
- **优先级**: P1
- **描述**: Docker 模式实例的 CPU/内存（/磁盘）上限，UI 设置 + 监控对比
- **验收标准**:
  - [ ] 实例模型加资源字段（cpu/mem[/磁盘]）；Docker 启动注入 `--cpus`/`--memory`；UI 设置（FR-072 校验）
  - [ ] 监控对比 实际占用 vs 上限（FR-060/061 图表，超限标红）；非 Docker 模式提示需 Docker
  - [ ] 真机：设 1CPU/2G → 容器 cgroup 实际受限（stress 验）
- **关联 ADR**: ADR-019
- **依赖**: FR-078

#### FR-080: Worker 一键安装 / 傻瓜部署
- **状态**: 🔨 in-progress
- **优先级**: P1
- **描述**: CP「添加节点」向导生成一键安装命令（脚本 + enrollment token + 可选系统服务）
- **验收标准**:
  - [x] 先写 **ADR-020**（enrollment 一键安装 + 自更新机制/来源/校验/CP 编排）
  - [x] CP「添加节点」向导生成一键命令（含一次性 enrollment token）
  - [x] 安装脚本（Linux sh / Win ps1）：下载对应平台 Worker 二进制 + 写配置 + enrollment 注册到 CP + 可选注册系统服务（systemd/Windows service）
  - [x] enrollment token 一次性限时；注册换 node_uuid/secret（复用 FR-004）；装后自启自连，前端见在线
  - [ ] 真机：另一机器/容器跑一键命令 → 节点自动注册上线（**待真机验**：公网 release 端点未架设，当前以 `--binary` 本地兜底）
- **关联 ADR**: ADR-020
- **依赖**: FR-004

#### FR-081: 面板自更新（CP/Worker 二进制在线升级）
- **状态**: ✅ done（已交付@v0.9.0；自动化验收全绿 + **真机已验**：self-update/check 端点返回 controlPlane.currentVersion=0.9.0 + os/arch，未配源优雅处理；二进制热升级编排逻辑单测覆盖）
- **优先级**: P1
- **描述**: 可配更新源 + sha256 校验，CP 自更新 + 经 gRPC 编排全网 Worker 升级，daemon 下不杀游戏服
- **验收标准**:
  - [ ] 更新源可配（release feed/私有 URL）+ 检查更新；CP 自更新（下载→sha256 校验→替换→平滑重启）
  - [ ] Worker 升级经 CP gRPC 编排（推送/通知 → 下载校验替换重启，daemon 不杀游戏服）；全网逐节点进度 + 失败回滚/重试
  - [ ] 仅平台管理员 + 审计（FR-015）；真机：CP/Worker 旧→新在线升级（版本号为证），daemon 下游戏服不掉
- **关联 ADR**: ADR-020
- **依赖**: FR-004, FR-006

### 里程碑 M4 — 全局页/可视化 + 存储浏览 + 告警体系

#### FR-082: 运行时与制品全局页（JDK + 制品库，按实例区分 + 可视化）
- **状态**: 🔨 in-progress
- **优先级**: P2
- **描述**: JDK 托管（FR-033）+ 制品库（FR-045）拆为全局页，按实例区分引用关系，可视化展示
- **验收标准**:
  - [ ] JDK 区：跨节点 JDK 矩阵（节点×版本）+ 每项绑定它的实例清单；制品区：资产列表 + 每项引用它的实例/模板（ref_count 下钻）
  - [ ] 可视化：实例↔JDK / 实例↔制品 引用关系（矩阵或关系图）；按实例/节点/类型筛选；占用/去重/冷热可视；删受引用项拒绝指出占用方
  - [ ] 真机：全局页渲染真 JDK/制品 + 引用关系正确
- **关联 FR**: FR-033, FR-045
- **依赖**: FR-033, FR-045（均 done）

#### FR-083: 平台存储资源管理器（数据根 FHS 浏览）+ FR-044 完善
- **状态**: 🔨 in-progress
- **优先级**: P2
- **描述**: 复用 FR-070 explorer 浏览数据根 FHS 布局，占用统计 + 受控清理，收尾 FR-044
- **验收标准**:
  - [ ] 复用 FR-070 explorer 浏览数据根 FHS（bin/etc/opt/var/cache）；各目录占用统计；cache 可清理；归档可见（FR-045 storage_state）
  - [ ] 收尾 FR-044 剩余项（制品库消费侧命中若未闭合）；只读为主 + 受控清理（二次确认）
  - [ ] 真机：浏览真数据根、占用统计正确、清理 cache 生效
- **关联 FR**: FR-044
- **依赖**: FR-070, FR-044

#### FR-084: 数据库资源管理器（只读浏览）
- **状态**: 🔨 in-progress
- **优先级**: P2
- **描述**: 复用 FR-070 explorer 只读浏览 CP DB 表与行，敏感列脱敏，仅平台管理员
- **验收标准**:
  - [ ] 左表树（CP DB 全部表）/ 右行只读浏览 + 分页 + 排序 + 简单过滤
  - [ ] 仅平台管理员；**只读**（守 DB 仅 CP 写边界）；敏感列（password_hash/secret/token）打码；大表分页不卡
  - [ ] 真机：浏览真 CP DB 各表 + 分页 + 脱敏
- **关联 API**: CP 只读元数据/行查询 API（新增）
- **依赖**: FR-070

#### FR-085: 告警体系全面增强（多通道 + 多类型 + 分级聚合 + 确认历史）
- **状态**: 🔨 in-progress
- **优先级**: P1
- **描述**: 在 FR-011 阈值告警基础上扩多通知通道 + 多触发类型 + 分级聚合静默 + 确认历史
- **验收标准**:
  - [ ] 多通道：邮件/钉钉/企业微信/飞书/Discord/Telegram/站内（现仅 webhook）；通道配置+测试发送；凭证经 `${ENV}`（config-files）
  - [ ] 多触发类型：实例崩溃/节点离线/日志关键字/玩家事件（接 FR-066）/备份失败（现仅指标阈值）
  - [ ] 分级（info/warn/critical）+ 去抖聚合 + 静默窗口 + 恢复通知；路由规则（按级别/类型/实例→通道）
  - [ ] 确认/认领 + 历史归档 + 已读状态 + 筛选
  - [ ] 真机：各类型触发 → 多通道收到 → 分级/聚合/静默生效 → 确认入历史
- **关联 FR**: FR-011；FR-105（邮件，提前纳入）；玩家事件类型依赖 FR-066
- **依赖**: FR-011

---

## 客户端分发与自动更新（玩家客户端 OTA）

> 源于 2026-06-23 sdd-brainstorming（含同日二次扩展：追踪 / 身份 / 遥测 / 统计 / 防护 / diff 打包）。JM 的**第三条产品线**（运维 + 运营 + **客户端分发**），与 Beacon（服务端集群治理）边界互斥、不交叉。
>
> **整体形态**：玩家用第三方启动器（HMCL/PCL2）→ 启动器 JVM 参数注入 `-javaagent:楔子.jar` → 楔子 premain 动态加载 `updater-core.jar` → 拉服务端**签名 manifest（latest-only，单调版本）** → **文件级**增量/减量更新客户端资源（mods/资源包/配置/runtime）→ 放行游戏。**控制面挂 ≠ 玩家进不去**（fail-static）。
>
> **范围边界（YAGNI）**：JM 只交付 ①**服务端分发后端**（频道/密钥/签名 manifest/制品分发/审计/遥测/统计/L7 防护，复用 FR-045 制品库）②**两个客户端 jar 组件**（楔子 + updater-core）。**客户端整包打包、HMCL/PCL2 启动器适配与参数注入、玩家侧分发由运营方自理，不在 JM 范围**。每服一个 channel（对应每服独立整包）。
>
> **首期不含**：配置灰度 cohort（延后）、块级二进制 diff（FR-098，先做文件级增量）、内容加密（仅签名防篡改，`.jmpack` 预留加密位）。
>
> **关联 ADR**：ADR-021（客户端更新组件纯 JVM 方案）、ADR-022（签名信任模型 + per-channel key + 防降级/重放 + 密钥轮换 + 公网端点）、ADR-023（分发端点防护与公网暴露：L7 + CDN）。
>
> **架构归属**：客户端组件（楔子 + updater-core）为 JM 仓内 `client-updater/` 子目录（Java/Gradle，monorepo，类比既有 `bot-worker/`；2026-06-23 改：不再独立仓+子模块，见 ADR-021 修订）；服务端能力在 JM 主仓 Go 侧。面向玩家公网端点的鉴权（拉取密钥）与防护见 ADR-022/023，与运营者浏览器入口隔离。
>
> 计划全文 `.tmp/brainstorm-client-distribution-2026-06-23.md`（不入库）。

### FR-086: 客户端分发频道与拉取密钥
- **状态**: ✅ done（已交付@v0.8.0；频道/密钥/版本管理浏览器 UI 真机已验（建频道+一次性明文密钥+发布向导）；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: 每服/每整合包一个分发频道（channel），每频道一把拉取密钥（玩家侧 updater 拉 manifest/制品用）；密钥落库只存哈希、明文创建/轮换时一次性返回不可二次读取、可吊销/轮换；管理台「客户端分发」页管理频道与密钥
- **验收标准**:
  - [ ] channel CRUD（id/名称/当前版本指针占位/描述）；每服一 channel
  - [ ] 拉取密钥：创建（名称+可选过期）/列出/吊销/轮换；落库只存 SHA-256 哈希，明文仅创建/轮换时返回一次、不可二次读取
  - [ ] 密钥经请求头鉴权（玩家侧 updater 用）；吊销即失效
  - [ ] 创建/吊销/轮换写入审计（FR-015，明文不入 detail）
  - [ ] 管理台「客户端分发」页：频道列表 + 密钥管理；i18n（FR-016）+ 暗色/亮色（FR-026）正常
- **关联 ADR**: ADR-022
- **关联 API**: `GET/POST /client-channels`、`POST /client-channels/:id/keys`、`DELETE /client-channels/:id/keys/:kid`（新增，登记时定稿）
- **依赖**: 无（地基，同构 Beacon FR-42 密钥套路）

### FR-087: 签名 manifest 端点（latest-only）+ 客户端制品分发
- **状态**: ✅ done（已交付@v0.8.0；签名 manifest + 制品分发真机已验（客户端拉取+Ed25519 验签+制品下载，跨语言固化）；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: 玩家侧 updater 拉取的核心契约——带 key 鉴权的远程 manifest 端点（**只提供频道当前 latest 版本**，服务端私钥签名）+ 客户端制品分发（复用 FR-045 内容寻址）；manifest 描述 latest 版本的完整文件清单与同步策略，**带单调递增版本号**，支持平台变体。**这是服务端线与客户端线的接口契约，需最先定稿**
- **验收标准**:
  - [ ] `GET /client-channels/:id/manifest`（key 鉴权）返回**仅 latest** 的签名 manifest：**单调递增 `version`**、`files[]`（path/sha256/md5/size/sync∈{strict,once,ignore} + **zstd 压缩制品引用**）、`managedDirs[]`、楔子&updater-core 自更新段、**平台变体**（win/mac/linux）
  - [ ] **签名（Ed25519）覆盖 `version` 与文件全集**；updater 内置公钥验签；**不提供版本历史/任意版本查询**（客户端只认 latest）
  - [ ] 客户端制品入 FR-045 制品库（新增 `type=client-file`），内容寻址 `var/artifacts/client-file/...`，去重；上传/登记 API
  - [ ] 文件 url **可配 CDN base**（回源 JM）；制品分发支持断点续传（Range）、大文件（JRE/整合包）友好
  - [ ] manifest 高频拉取可缓存（CDN 友好）+ 发布后及时失效（版本化 URL / 短 TTL）
  - [ ] 未授权（无效/吊销 key）403；签名缺失/不符客户端拒绝
- **关联 ADR**: ADR-022、ADR-011（制品库）
- **关联 API**: `GET /client-channels/:id/manifest`、`GET /client-artifacts/:sha256`、`POST /client-channels/:id/files`（新增）
- **依赖**: FR-086、FR-045

### FR-088: 客户端版本发布 + latest 指针（服务端留历史供回滚/diff）
- **状态**: ✅ done（已交付@v0.8.0；版本历史/运营回滚/发布向导浏览器 UI 真机已验；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: 发布新版即更新频道 **latest 指针并重签 manifest（版本号单调递增）**；服务端**保留历史版本制品**（供运营回滚与未来块级 diff 计算）；**运营回滚 = 以更高版本号重发旧内容为新 latest**（保持版本单调、不触发客户端防降级）。客户端只认 latest；客户端本地 N-1 回退见 FR-091
- **验收标准**:
  - [ ] 发布：一组 client-file 制品 + managedDirs/sync 组成版本，生成**单调递增版本号**，切 latest 指针 + 重签 manifest
  - [ ] 服务端**保留历史版本**（制品内容寻址去重，供回滚/diff）；**不向客户端暴露版本历史/任意版本拉取**
  - [ ] **运营回滚**：选历史内容**以更高版本号重发为 latest**（客户端按单调版本正常前进、不被防降级拒绝）
  - [ ] 发布/回滚入审计（FR-015）
  - [ ] 管理台版本列表 + 发布/回滚（二次确认 FR-059）；i18n + 主题正常
- **关联 ADR**: ADR-022
- **关联 API**: `POST /client-channels/:id/versions`、`POST /client-channels/:id/rollback`（新增）
- **依赖**: FR-087

### FR-089: javaagent 楔子 jar（自定位 + 引导 + fail-open）
- **状态**: ✅ done（已交付@v0.8.0；真机已验：HMCL/PCL2 用真 MC 1.20.1 注入 -javaagent，premain 自定位+引导+fail-static+fail-open+多 javaagent 共存均通过；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: 极小 Java jar，经第三方启动器 JVM 参数 `-javaagent` 注入；premain 自定位、解析 gameDir、动态加载并调用 updater-core；更新失败 fail-static 放行带旧版进游戏。**楔子自身任何异常都 fail-open（绝不挡住游戏启动）**。设计为稳定件，随基础整包分发（低频，不依赖自更新）
- **验收标准**:
  - [ ] premain 用 `getCodeSource().getLocation()` 自定位 jar 路径，反推同目录 updater-core 与配置；不依赖 cwd
  - [ ] 解析 gameDir（agentArgs `-javaagent:楔子.jar=<gameDir>` 优先，兜底解析 `sun.java.command` 的 `--gameDir`）；多版本隔离正确
  - [ ] 用独立 classloader 动态加载 `updater-core.jar`（内存加载避文件锁）并调用入口，传 gameDir/channel
  - [ ] 同步等待 updater-core：成功放行；更新失败/超时 **fail-static** 放行带旧版 + 显眼提示
  - [ ] **楔子 fail-open**：premain 全程 try/catch，**楔子自身任何异常（定位失败 / core 缺失 / 加载错误）都放行游戏**，绝不因楔子挡住启动
  - [ ] **多 javaagent 共存**：与外置登录 `authlib-injector` 等其他 `-javaagent` 同挂时均正常（真机验 HMCL+PCL2 楔子与 authlib-injector 并存、加载顺序无关）
  - [ ] agentArgs 协议（gameDir/channel/可选 endpoint）定稿、文档化（楔子↔core 接口契约）
  - [ ] 玩家面向提示文案 **i18n**
  - [ ] 真机：HMCL + PCL2 各注入一次，premain 正常引导、游戏正常启动
- **关联 ADR**: ADR-021
- **依赖**: FR-090（同仓协同，agentArgs 契约）

### FR-090: updater-core.jar — reconcile 核心（文件级增量 + 并发锁 + 缓存治理）
- **状态**: ✅ done（已交付@v0.8.0；真机已验：真 MC 客户端 OTA reconcile 增量写入真 .minecraft/mods、断网 fail-static 放行；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: 客户端更新主体（Java jar，被楔子动态加载）。拉签名 manifest 验签 → reconcile：**文件级**增量（下 hash 不符文件、zstd 解压）+ 减量（删托管区多余文件）；md5/size 快筛 + sha256 信任校验；托管区/玩家区隔离；CAS 本地缓存 + 清理；单实例并发锁；按平台取文件集；fail-static
- **验收标准**:
  - [ ] 拉 manifest（带 channel key）→ 内置公钥验签（含 `version`）；签名不符拒绝；**防降级**：拒绝 `version` 低于本地已见最高版本的 manifest
  - [ ] reconcile：遍历 managedDirs 算 hash vs manifest → **文件级**增量下载（md5/size 快筛命中跳过，下载 zstd 制品 **解压** + 下载后 **sha256 强校验**）+ 减量（删 managedDirs 内 manifest 未列文件）
  - [ ] **托管区/玩家区隔离**：仅 managedDirs 可增删；玩家区（saves/、options.txt、screenshots/、logs/）永不碰；config 文件按 sync 策略 strict/once/ignore
  - [ ] CAS 本地缓存（内容寻址 sha256）：命中免下；**缓存清理（LRU / 容量上限，连同 N-1 控制磁盘占用）**
  - [ ] **单实例并发锁**：单 gameDir 仅一个 updater 运行（文件锁），防同时开两个游戏 / 重复启动并发改目录
  - [ ] 按本机平台取 manifest 平台变体文件集（win/mac/linux）
  - [ ] 原子放置（temp 下载 + 校验通过再原子换）；中断不损坏客户端
  - [ ] **fail-static**：manifest 端点不可达/断网 → 带本地现有版本返回成功（楔子放行进游戏）+ 显眼提示
  - [ ] **target Java 8**（兼容低版本游戏 JVM）：HTTP 用 `HttpURLConnection`、`MessageDigest` + 轻量 JSON；Ed25519 用 BouncyCastle（Java 8 无 JDK 内置），zstd 用 zstd-jni，fat jar 自含
  - [ ] **本地诊断日志**（供排障与遥测 FR-094）
  - [ ] 真机：增量/减量正确、玩家区未动、断网 fail-static、并发不冲突
- **关联 ADR**: ADR-021、ADR-022
- **关联 API**: 消费 FR-087 的 manifest/制品端点
- **依赖**: FR-087（manifest 契约）

### FR-091: updater-core 自更新 + 客户端 N-1 回退（启动失败自动回滚）
- **状态**: ✅ done（已交付@v0.8.0；真机已验：三次 premain promote、坏 core 自动回退 N-1；真机发现并修复 boot-loop（failedVersion）；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: updater-core 自身的版本自更新（楔子动态加载，无 `-javaagent` 文件锁）+ **本地保留 N-1 + boot-success 启动确认 + 新版加载失败自动回退 N-1**（靠 CAS 缓存零重下）
- **验收标准**:
  - [ ] updater-core 自更新：manifest 自更新段声明应有版本+hash；不符则下新 jar、**验签 + selftest 通过后切换**、失败回退旧 jar；内存加载避锁、下次 premain 加载新版
  - [ ] **N-1 保留**：每次成功更新保留上一可用版本快照（靠 CAS，零额外全量）
  - [ ] **boot-success 确认机制**：更新后标记 `pending`，游戏成功启动（游戏内 hook 或超时无崩溃）标 `confirmed`；**下次启动若发现上次 `pending` 未确认（判定上次崩溃）→ 自动回退 N-1**
  - [ ] 手动/运营触发回退亦支持（运营侧整体回滚见 FR-088，本 FR 是客户端执行端）
  - [ ] 自更新与回退原子、失败不损坏、可重入
  - [ ] 真机：推 core 新版→下次生效；注入坏版本→启动失败→下次自动回退 N-1 成功进游戏
- **关联 ADR**: ADR-021
- **依赖**: FR-090

### FR-092: 客户端机器码身份（仅追踪/统计）
- **状态**: ✅ done（已交付@v0.8.0；真机已验：X-Machine-Id 上报 + 服务端登记（activeMachines）；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: updater 按玩家环境生成稳定唯一机器码（硬件指纹 hash，跨 win/mac/linux），随更新/遥测请求携带，作客户端身份用于日志/统计/限流维度。**仅追踪统计，不做更新授权门禁**
- **验收标准**:
  - [ ] 机器码：多硬件特征（主板/CPU/磁盘等）组合 hash，稳定（部分变化容错）、**不可逆**（不暴露原始硬件信息）、跨平台生成
  - [ ] 随 manifest 拉取/制品下载/遥测请求携带；服务端登记并关联
  - [ ] **声明机器码客户端生成、不可信**：用于统计与**辅助**限流，**不作信任/授权依据；限流以 IP 为主、机器码为辅**
  - [ ] 隐私：机器码为不可逆 hash，遥测合规告知玩家
- **关联 ADR**: ADR-023
- **依赖**: 无（被 FR-093/094/095/096 复用）

### FR-093: 发布/拉取全链路审计与追踪
- **状态**: ✅ done（已交付@v0.8.0；真机已验：manifest/制品事件落库 + 检索；真机发现并修复制品下载频道归属；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: 每次版本发布（运营侧）+ 每次 manifest 拉取/制品下载（玩家侧）记录审计：IP + 机器码 + 频道/版本 + 字节数 + 结果/耗时；含数据量治理
- **验收标准**:
  - [ ] 发布事件：操作者/频道/版本/时间入审计（FR-015）
  - [ ] 拉取/下载事件：IP、机器码、频道、版本、文件/字节数、结果、耗时落库
  - [ ] **数据量治理**：明细短保留 + 滚动清理 + 聚合长保留（参考 FR-060 时序降采样），防 DB 膨胀
  - [ ] 可按 IP/机器码/频道/版本/时间检索
- **关联 ADR**: ADR-023
- **依赖**: FR-092

### FR-094: 客户端遥测上报
- **状态**: ✅ done（已交付@v0.8.0；真机已验：reconcile 后客户端 best-effort 上报、服务端落库聚合；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P2
- **描述**: updater 上报遥测——更新结果（成功/失败/回退）、当前版本、客户端环境（OS/Java/启动器粗粒度）、失败原因、耗时、boot-success
- **验收标准**:
  - [ ] 上报端点（带 channel key + 机器码）；结构化遥测落库
  - [ ] 字段：结果/版本/环境/失败原因/耗时/boot-success 状态
  - [ ] **隐私告知 + 可关**（合规）；不收集敏感个人数据
  - [ ] 数据量治理（保留期 + 聚合，同 FR-093）
- **关联 ADR**: ADR-023
- **依赖**: FR-092

### FR-095: 分发统计后台
- **状态**: ✅ done（已交付@v0.8.0；真机已验：统计看板浏览器渲染（趋势/版本/成功率/活跃机器码/TopIP）+ i18n 切 EN；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P2
- **描述**: 管理台分发看板——下载量、来源 IP 分布、版本分布、活跃机器码、流量趋势、更新成功率/回退率（来自 FR-093/094 聚合）
- **验收标准**:
  - [ ] 看板：下载量/趋势、来源 IP（地理/段）分布、版本分布、活跃机器码数、更新成功率/回退率 图表
  - [ ] 数据来自 FR-093/094 聚合（时序）；按频道/时间区间筛选
  - [ ] i18n + 主题正常
- **关联 ADR**: ADR-023
- **依赖**: FR-093、FR-094

### FR-096: 分发端点应用层（L7）防护
- **状态**: ✅ done（已交付@v0.8.0；真机已验：IP deny→403（早于鉴权）+ protection-stats + 防护拒绝时 fail-static 仍放行；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: 分发端点应用层防护——限流、IP 黑白名单、连接数限制、异常识别、缓存；**容量型 L3/L4 攻击靠 CDN/云清洗，不在 JM 内**
- **验收标准**:
  - [ ] 限流：**以 IP 为主维度**（机器码不可信、仅辅助）+ 端点级速率/突发限制
  - [ ] IP 黑名单（直接拒）+ 白名单模式（仅白名单可访问）；运行时可改、入审计
  - [ ] 连接数/并发限制；异常流量识别（突增/异常 UA/无效 key 高频）
  - [ ] 可缓存内容（manifest/制品）走缓存，让多数请求不打源站
  - [ ] **明确边界**：L3/L4 容量型 DDoS 靠 CDN/Anycast/云清洗，JM 只做 L7
  - [ ] 防护动作可观测
- **关联 ADR**: ADR-023
- **依赖**: FR-087（挂端点上）、FR-092（IP 为主、机器码辅）

### FR-097: 自有 `.jmpack` 打包（压缩 + 签名）
- **状态**: ✅ done（已交付@v0.8.0；真机已验：打包入库 .jmpack（magic/meta 正确）+ Java 解 Go golden 跨语言；updater-core 真 Java 8(1.8.0_422) JVM 端到端复验通过）
- **优先级**: P1
- **描述**: 自有分发容器格式 + 工具：zstd 压缩 + Ed25519 签名（**首期不含加密、不含块级 diff**，格式预留扩展位）。服务端打包、客户端解包
- **验收标准**:
  - [ ] 容器格式：header（magic/格式版本/flags）+ meta（路径/sha256/大小/算法）+ payload（zstd 压缩）+ 签名；**flags 预留加密/diff 位**
  - [ ] 底层全用成熟库（zstd + Ed25519）；**不自研加密算法、首期不加密**
  - [ ] 服务端打包工具（发布时压缩 + 签名入库）+ 客户端解包（验签 + 解压）
  - [ ] 与 FR-087 制品、FR-090 reconcile 衔接
- **关联 ADR**: ADR-021、ADR-022
- **依赖**: FR-087、FR-090

### FR-098: 块级二进制 diff 增量发布
- **状态**: ⏸️ deferred（延后；首期 FR-090 做文件级增量）
- **优先级**: P2
- **描述**: 对变化的大文件只传内部差异（zstd patch-from）——发布侧算 patch 入库、manifest 提供 patch 引用、客户端应用 patch；在文件级增量之上进一步最小化传输。复用 FR-097 容器（启用 diff flag）
- **验收标准（延后细化）**:
  - [ ] 发布侧：相对上版算文件块级 patch（zstd patch-from），patch 内容寻址入库
  - [ ] manifest 文件项带 patch 引用（oldhash→newhash）
  - [ ] 客户端：有旧文件 + 有 patch 则下 patch 应用，否则下全量；应用后 sha256 校验
- **关联 ADR**: ADR-021
- **依赖**: FR-090、FR-097

### FR-099: 客户端 OTA 更新进度窗口（进度条 + 速度 + ETA）
- **状态**: ✅ done（已交付@v0.8.0；验收通过 2026-06-24 用户确认；提交 `9a22d28`）
- **优先级**: P1
- **描述**: updater-core 在更新期（reconcile 下载 mods 增量 + core 自更新 jar）弹出**独立 Swing 进度窗口**，实时显示总体进度条、当前下载速度、预计剩余时间(ETA)、当前文件名；下载完成自动关闭再放行 MC。**因 wedge premain 早于 MC 渲染线程/LWJGL**，无法注入 MC 自身加载画面，故由 updater 自弹独立窗口。headless 环境自动降级为文本进度（不弹窗、不报错、不阻断）。玩家面向文案 i18n（zh/en）。**纯客户端**，无服务端/manifest 改动；保持楔子 fail-open/fail-static 与"绝不挡启动"不变
- **验收标准**:
  - [x] 有内容要下载时更新期弹出 Swing 窗口：进度条(0→100%) + 当前速度 + ETA + 当前文件名，随下载实时刷新（PCL2 GUI 真机可视）
  - [x] 覆盖客户端实际下载路径：mods 文件增量、core 自更新 jar(FR-091)；.jmpack 不在客户端 reconcile 下载路径（仅服务端打包+独立解包工具），N/A
  - [x] 下载完成窗口自动关闭，MC 正常启动到主菜单（PCL2 真机 latest.log 主菜单）
  - [x] "已是最新"无下载时不弹窗（惰性显示，首个 beginFile 才 show）
  - [x] **headless**（`GraphicsEnvironment.isHeadless()`）自动降级为文本进度，不报错、不阻断（真 Java 8 headless 36MB 端到端）
  - [x] 玩家手动关窗 = 停止下载、以本地版本放行（fail-static）；窗口任意异常不逃逸阻断（fail-open）（真机关窗→downloaded=5/6→放行）
  - [x] 窗口文案 zh/en 随 `user.language` 切换（CoreMessagesTest）
  - [x] core 仍 **Java 8 字节码**、零额外依赖（Swing/AWT = JDK 自带）（真 Java 8(1.8.0_422) 加载无 UnsupportedClassVersionError）
  - [x] 真机：真 MC 1.20.1 经 PCL2 GUI 启动，36MB 下载肉眼可见进度窗（用户确认）
- **关联 ADR**: ADR-021
- **依赖**: FR-090（reconcile 下载）、FR-091（core 自更新）

### FR-107: 后台客户端更新器接入指引
- **状态**: ✅ done（已交付@v0.8.0；验收通过 2026-06-24 用户确认；提交 `45af3c9`/`3fb91fb`/`21a408e`）
- **优先级**: P1
- **描述**: 后台「客户端分发 → 频道详情」加「接入指引」Tab，面向**运营方**：一页拿齐——下载 CP 内嵌的 wedge.jar/updater-core.jar、该频道**专属可复制**的 jm-updater.json（channelId/endpoint/密钥占位）、启动器 `-javaagent` 参数（相对路径推荐）、放置步骤、行为说明（fail-static/fail-open/进度窗/多 agent 共存）。CP 经 `go:embed` 内嵌两 jar + admin JWT 下载端点。纯运营面，不改 OTA 协议/manifest/客户端 jar 本身
- **验收标准**:
  - [x] 频道详情「接入指引」Tab 渲染：机制简述 + 内嵌版本 + 4 步骤 + 行为说明（真机浏览器 zh/en 均验）
  - [x] 下载按钮命中 admin JWT 端点，返回内嵌真 jar 字节（wedge 20371B / core 14478092B 精确一致）；未内嵌 404 友好提示 + 按钮禁用
  - [x] jm-updater.json 按频道生成（channel=本频道）+ endpoint 可编辑 + 复制；javaagent 参数复制
  - [x] i18n zh/en + 暗亮主题；jar 下载 JWT admin 鉴权（无 token 401、非法组件 400）
  - [x] CP `go:embed` 内嵌 wedge/updater-core jar（仿 probe，`.gitignore` 占位）+ `make embed-client-updater` 注入
  - [x] 后端单测（Info/组件校验/未内嵌兜底/鉴权）4 项 + 真机端点下载真 jar
- **关联 ADR**: ADR-021、ADR-022
- **依赖**: FR-086（频道/密钥）、FR-089/090（客户端 jar）、FR-099（进度窗口，行为说明引用）

---

## 走查优化（v0.9.0 真机走查发现，2026-06-24）

> v0.9.0 浏览器真机走查产出的缺陷/UX/性能/工程改进。BUG 走 sdd-fix-bug，FR 走 sdd-develop-feature；分 3 波 rebase→ff 集成（波1缺陷 → 波2 UX → 波3 性能/工程）。

### BUG-009: 数据库浏览页直接访问/刷新崩溃
- **状态**: ✅ done（已修复@v0.9.1；自动化验收：单测 + tsc/lint/build 全绿；建议浏览器真机复验）
- **优先级**: P0
- **描述**: 直接访问或刷新 `/database`（FR-084）抛 `Cannot read properties of null (reading 'length')`（源 DatabasePage，数据 fetch→setData→render 时读 null 的 length），整页空白；SPA 进入与硬刷新行为不一致
- **验收标准**:
  - [ ] 直接访问/刷新 /database 不崩、表列表正常渲染
  - [ ] 无选中表 / 空表 / 加载中时不读 null.length（加 `?? []` 守卫）
  - [ ] 硬刷新与侧栏 SPA 进入表现一致、浏览器真机无控制台报错
- **关联 FR**: FR-084

### BUG-010: 归档浏览树重复文件夹 + 双击 jar 误选中
- **状态**: ✅ done（已修复@v0.9.1；自动化验收：单测 + tsc/lint/build 全绿；建议浏览器真机复验）
- **优先级**: P1
- **描述**: 打开 jar 归档树，zip 同时含目录条目（`io/`）+ 文件条目（`io/...`）时同名顶级文件夹重复出现 2 次；双击 jar 同时勾选复选框（冒出「清空选择」）又打开归档
- **验收标准**:
  - [ ] 归档内部树同名目录节点唯一（按路径段 Map 合并）
  - [ ] 双击 jar 仅打开归档查看器、不勾选该行
  - [ ] 真机打开 server.jar/插件 jar 树无重复、双击正确
- **关联 FR**: FR-075

### BUG-011: 长会话 token 过期无静默续期致 401
- **状态**: ✅ done（已修复@v0.9.1；自动化验收：单测 + tsc/lint/build 全绿；建议浏览器真机复验）
- **优先级**: P1
- **描述**: access token 15min TTL，长操作会话中过期后请求直接 401 失败（走查多次复现），未用 refresh token 静默续期重放
- **验收标准**:
  - [ ] access token 过期时自动用 refresh token 续期并重放原请求
  - [ ] refresh 也失效才登出跳登录；并发 401 续期串行化不重复刷新
  - [ ] 真机：过期后继续操作不弹 401
- **关联 FR**: FR-001

### BUG-012: Worker 启 MC 无绑定 JDK 时静默用错 Java 版本
- **状态**: ✅ done（已修复@v0.9.1；自动化验收：单测 + tsc/lint/build 全绿；建议浏览器真机复验）
- **优先级**: P1
- **描述**: 实例未绑定 JDK 时启动命令为裸 `java`，落 PATH 的 Java；Paper 1.21 等需 Java21，PATH 若 Java8 则 `UnsupportedClassVersionError` 崩在游戏服日志、面板无明确提示
- **验收标准**:
  - [ ] 启动前校验将用 java 大版本与实例/核心要求，不符返回明确错误（不让进程崩在日志）
  - [ ] 错误信息指引绑定/安装合适 JDK
  - [ ] 真机：未绑 JDK 起 Paper 1.21 给清晰报错而非静默崩
- **关联 FR**: FR-008, FR-033

### FR-108: 仪表盘总览环分级配色与负载量纲修正
- **状态**: ✅ done（已交付@v0.9.1；自动化验收：tsc/lint/vitest(227)/build 全绿；建议浏览器真机复验）
- **优先级**: P2
- **描述**: 总览三环（CPU/负载/内存）高值全红无分级；负载以百分比塞进 0-100 环、超核数显示 >100%（如 103%）视觉破裂
- **验收标准**:
  - [ ] 负载改 load÷核数 或单列量纲、环按核数封顶不破 100%
  - [ ] 三环分级配色（绿/黄/红阈值，合理默认）
  - [ ] 真机：高负载宿主显示合理、不全红误导
- **关联 FR**: FR-060, FR-061

### FR-109: 服务器状态页显示打磨
- **状态**: ✅ done（已交付@v0.9.1；自动化验收：tsc/lint/vitest(227)/build 全绿；建议浏览器真机复验）
- **优先级**: P2
- **描述**: 类加载器父链按字符断词换行难读；仅手动刷新；state_json 字符串透传各处自行 parse（易踩字符串字段当对象白屏）
- **验收标准**:
  - [ ] 类加载器父链以 `→` chip 或等宽+省略可读呈现
  - [ ] 「打开期间自动刷新（可配间隔）」开关，关闭仍手动刷新
  - [ ] state_json 在 api 层统一 parse + 出 TS 类型，组件不裸 parse
  - [ ] 真机：父链可读、自动刷新生效
- **关联 FR**: FR-076, FR-077

### FR-110: 系统更新页未配源仍展示当前版本
- **状态**: ✅ done（已交付@v0.9.1；自动化验收：tsc/lint/vitest(227)/build 全绿；建议浏览器真机复验）
- **优先级**: P2
- **描述**: 未配 feed_url 时系统更新页只显警告、隐藏 CP/各 Worker 当前版本（API 实际已返回）
- **验收标准**:
  - [ ] 未配源也展示 CP + 各 Worker 当前版本表 + 配源提示
  - [ ] 配源后正常版本对比/升级
  - [ ] 真机：未配源页面可见当前版本
- **关联 FR**: FR-081

### FR-111: 归档/反编译查看器布局优化
- **状态**: ✅ done（已交付@v0.9.1；自动化验收：tsc/lint/vitest(227)/build 全绿；建议浏览器真机复验）
- **优先级**: P2
- **描述**: 打开归档/反编译查看器后变 树｜文件列表｜查看器 三栏，中屏拥挤
- **验收标准**:
  - [ ] 打开查看器时折叠文件列表或改抽屉式、中屏不溢出
  - [ ] 关闭查看器恢复双栏
  - [ ] 真机：中屏布局舒适
- **关联 FR**: FR-075

### FR-112: 平台/运维导航信息架构统一
- **状态**: ✅ done（已交付@v0.9.1；自动化验收：tsc/lint/vitest(227)/build 全绿；建议浏览器真机复验）
- **优先级**: P3
- **描述**: 平台级运维页分散（运行时与制品/客户端分发为顶级，平台存储/数据库/系统更新埋设置下），IA 不一致
- **验收标准**:
  - [ ] 平台/运维页按一致分组归类
  - [ ] 路由 URL 不变、不破坏书签
  - [ ] 真机：导航清晰
- **关联 FR**: FR-069

### FR-113: 全文索引后台化与进度
- **状态**: 🔨 开发中
- **优先级**: P2
- **描述**: 全文搜索查询时同步增量重建索引，大工作目录首次查询阻塞 UI
- **验收标准**:
  - [ ] 首建移出查询关键路径（后台异步），查询不同步全量重建；小目录有界快路径仍同步出结果
  - [ ] 查询时索引未就绪返回 `indexing=true`，前端给「索引中」进度并自动重试
  - [ ] 真机：大目录首查不卡 UI、结果一致
- **关联 FR**: FR-074 | **关联 ADR**: ADR-017, ADR-024

### FR-114: 探针依赖内联/缓存预置
- **状态**: 📋 计划
- **优先级**: P3
- **描述**: 探针首启联网拉 TabooLib 依赖（~30s+），慢网/离线首启探针失败
- **验收标准**:
  - [ ] 探针 jar 内联依赖或 Worker 侧 TabooLib 缓存预置
  - [ ] 离线/慢网首启探针可用
  - [ ] 真机：断网首启探针正常 enable
- **关联 FR**: FR-065, ADR-016

### 工程整治（chore/ref，不占 FR 号，走 sdd-refactor-code/手工）
- **CHORE: 前端路由级代码分割**（#13）— PluginManager 798KB/index 411KB，vite 警告 >500KB；路由 `lazy()` 拆分、首屏减包
- **CHORE: `.gitattributes` 规范换行与生成代码合并**（#17/#18）— `*.pb.go merge=union linguist-generated` + `eol=lf` 统一，止 CRLF diff 污染与 pb.go 合并地狱
- **CHORE: 内嵌 CFR + 镜像探针/CFR 嵌入校验**（#14/#20）— 发版 `make embed-cfr` 内嵌反编译器；镜像构建显式校验探针/CFR 真嵌入并告警

---

## JBIS 业务对接平台（全能业务 agent，3 里程碑程序）

> 源于 2026-06-24 sdd-brainstorming（方向对齐 + 两插件契约调研）。13 FR（FR-115~127）/ 5 ADR（025~029），分 3 里程碑：**里程碑内可并行、里程碑间串行**（前一个落 main 再开下一个）。**M1 垂直切片必先行**（economy.balance 读穿全 5 层，脊柱一次成型，后续加动作/加域近 O(1)）。计划全文 `.tmp/brainstorm-jbis-business-integration-2026-06-24.md`（不入库）；设计总纲 `docs/specs/business-integration/design.md`；两插件对接契约 `.tmp/business-integration-plugin-contracts.md`。
> 核心形态：**一个 ServerProbe = 本服唯一全能 agent**（对外单连接/单身份，对内监控层 core/api 只读纯净 + 业务对接层 platform 独立事故域，两层不共享线程/异常边界）。核心链路 CP/Worker/桥/DB/UI **插件无关**，只认 `domain + action + payload信封 + dedupKey`；唯一认识具体插件的是探针侧 per-plugin **适配器(Provider)** + 每域 **manifest 能力清单**（范式 A 适配器 + 能力发现）。
> 跨 3 仓：JianManager（编排/存储/UI）+ ServerProbe 子模块（适配器 + 演进 ADR）+ AllinInventorySync（扩 api）。数据所有权不变：业务真源仍在各插件存储，JM 侧存汇聚镜像 + 操作审计。
> 横切约束：每条 FR 验收含 **i18n(FR-016) 完整 + 暗色/亮色(FR-026) 正常 + 真机验证**；高危写（改余额/改背包）必须二次确认 + 审计留痕贯通。

### 里程碑 M1 — 垂直切片（economy.balance 读穿全链，脊柱成型）

#### FR-115: 业务桥 Worker 脊柱
- **状态**: 🔨 开发中（Worker 侧代码完成：proto 加 domain/payload_json/dedup_key 重生成、桥帧/事件穿透、命令帧业务路由、单测全绿 go build/vet + ws/grpc 全量 -race；待 M1 收口随整链真机确认 done）
- **优先级**: P1
- **描述**: Worker 侧打通业务命令路由与事件汇聚流，复用既有反向 WS 桥（ADR-016），不新增进程/协议
- **验收标准**:
  - [x] 先写 **ADR-027**（业务命令复用桥 command/event + `domain.verb` 寻址）
  - [x] `proto/worker.proto` 加性新增 command/event 的 `domain`/`dedup_key`（command 另加 `payload_json`）可选字段，workerpb 经 protoc 重生成（禁手改）
  - [x] 桥 JSON 帧加 `domain` 字段（即业务/监控分流标志，非空非 core=业务；单连接无需独立 session scope，见 ADR-025）：下行命令帧带 domain/payloadJson 供探针 BusinessHost 路由、上行业务事件帧带 domain/dedupKey 透传给 CP
  - [x] 单测：命令帧业务/治理分流（domain/payloadJson 仅业务带）+ 事件 domain/dedupKey 冒泡 + 监控帧分流
- **关联 ADR**: ADR-027
- **依赖**: 无（脊柱起点）

#### FR-116: CP 业务编排与汇聚脊柱
- **状态**: 🔨 开发中（业务命令下发路径已完成：`BusinessService.Dispatch`(domain.action+payload→SendPluginCommand wait=true，纯函数 mapBusinessResponse 全降级矩阵) + `POST /instances/:id/business` 端点 + 接线，单测 build/vet + service -race 全绿。manifest 汇聚待 FR-117 提供 manifest 源后做；envelope 事件存储留 M2/FR-122 真实业务事件流时落地）
- **优先级**: P1
- **描述**: CP 侧业务命令下发、manifest 能力汇聚、业务事件去重落库（插件无关通用信封）
- **验收标准**:
  - [x] 先写 **ADR-028**（CP 业务数据与时序监控分表分策略）
  - [x] CP 下发业务命令（携 `requestId`，operator 透传留 FR-121），域不可用/探针未连/节点未连一律优雅降级（available=false + 友好提示，绝不 5xx）
  - [ ] 各节点各域 manifest 汇聚为平台级能力视图（待 FR-117 manifest 源）
  - [ ] 业务事件经通用 envelope 表（domain/action/payload-json/dedupKey/node/operator/ts）按 dedupKey 去重落库 + 读端点（待 M2 真实业务事件流，FR-122）
  - [ ] 端到端：CP 下发 `economy.balance` 经 Worker→桥往返收到结果（待 FR-117/118，M1 收口真机）
- **关联 ADR**: ADR-027, ADR-028
- **依赖**: FR-115

#### FR-117: ServerProbe 业务对接层骨架
- **状态**: 📋 计划
- **优先级**: P1
- **描述**: ServerProbe platform 层新增业务对接骨架（BusinessHost + Provider 框架 + 事故域隔离 + manifest），core/api 保持只读纯净
- **验收标准**:
  - [ ] 先写 **ADR-025**（ServerProbe 监控探针→全能业务 agent：对外单 agent、对内分层、事故域隔离）+ ServerProbe 子模块仓自身演进 ADR
  - [ ] BusinessHost 注册 Provider、声明/上报 manifest、接桥 `scope=business` 会话
  - [ ] 事故域隔离：业务 Provider 用独立线程池 + 独立异常边界 + 守护式初始化（业务模块故障降级为该域不可用）
  - [ ] **真机**：业务 Provider 抛异常/卡死，监控采集与桥心跳完全不受影响
- **关联 ADR**: ADR-025（+ ServerProbe 仓 ADR）
- **依赖**: FR-115

#### FR-118: 经济 Provider（只读）
- **状态**: 📋 计划
- **优先级**: P1
- **描述**: ServerProbe 经济适配器 wrap MultiCurrencyEconomyService，实现 economy.balance 只读 + 经济域 manifest
- **验收标准**:
  - [ ] 先写 **ADR-026**（适配器 + manifest 能力发现路线，非插件实现 SPI；修正设计总纲 §6 范式 B 倾向）
  - [ ] 经 `ServicesManager.load(MultiCurrencyEconomyService)` + `isReady()` 发现，未就绪/不在场降级为能力不可用
  - [ ] `economy.balance` 动作 + manifest 声明（能力 + 字段 schema）
  - [ ] **真机**：真 mce 服上查到真实余额；mce 卸载后该域降级
- **关联 ADR**: ADR-026
- **依赖**: FR-117

#### FR-119: 业务掌控台 UI v1（manifest 驱动）
- **状态**: 📋 计划
- **优先级**: P1
- **描述**: 前端按汇聚 manifest 动态渲染域/能力，调用 economy.balance 并展示
- **验收标准**:
  - [ ] 按 CP 汇聚的 manifest 动态列出域与能力（不硬编码经济/背包）
  - [ ] 输入玩家名调用 economy.balance、展示余额；域不可用显式提示
  - [ ] i18n 完整 + 暗/亮色正常
- **关联 ADR**: —
- **依赖**: FR-116

> **M1 收口（真机）**：浏览器→CP→Worker→桥→探针→真 mce 查到真实余额；事故域隔离验证（业务故障不拖垮监控）。脊柱成型后 M2/M3 加动作/加域近 O(1)。

### 里程碑 M2 — 经济整域（写 + 横切硬化 + 定制页）

#### FR-120: 经济 Provider（写）
- **状态**: 📋 计划
- **优先级**: P1
- **描述**: 经济适配器写动作 deposit/withdraw/adjust(set)/transfer/consume/refund，守 mce 契约硬约束
- **验收标准**:
  - [ ] 幂等键 `pluginName="JianManager" + BusinessOrder(jm_task_id)`，重试严禁换键（防 MCE-LEDGER-0001 冲突）
  - [ ] 金额 BigDecimal **字符串**承载信封（禁 double）；强制异步线程调用（不阻塞主线程）；mce 错误码透传
  - [ ] `economy.set` 经 read-then-adjust 差额实现并标注非原子取舍
  - [ ] **真机**：真 mce 加/扣/转账成功且幂等（重发同键不双花）、余额不足/账户冻结等错误码正确
- **关联 ADR**: ADR-026
- **依赖**: FR-118

#### FR-121: 业务写横切硬化（幂等 + 二次确认 + 审计）
- **状态**: 📋 计划
- **优先级**: P1
- **描述**: 端到端幂等链 + 高危写权限与二次确认 + 操作者身份审计留痕贯通
- **验收标准**:
  - [ ] 先写 **ADR-029**（业务高危写权限与二次确认模型）
  - [ ] 端到端幂等：CP 生成 `jm_task_id` → 桥透传 → 直达插件幂等键，跨节点重试天然防重
  - [ ] 高危写（改余额/改背包）需 permission node + 阈值二次确认
  - [ ] 操作者身份（哪个管理员/哪个节点/为什么）映射进插件审计流水，平台侧可追溯
- **关联 ADR**: ADR-029
- **依赖**: FR-116, FR-120

#### FR-122: 经济汇聚与多区聚合
- **状态**: 📋 计划
- **优先级**: P1
- **描述**: 汇聚所有来源的经济变更（含 web/跨服）+ 多区/多节点正确聚合 + 结构化镜像/审计存储
- **验收标准**:
  - [ ] 探针订阅 `PlayerEconomyChangeEvent` + `PlayerEconomyCatchupEvent` 上报，CP 按 `ledgerId` 去重（至少一次投递）
  - [ ] `node→zoneId` 维度聚合：跨区同名玩家余额不串味/不重复计数（currencyId Int↔identifier 转换）
  - [ ] 结构化经济镜像 + 操作审计表（分表分策略 ADR-028）
  - [ ] **真机**：web 后台/其他服的余额变更都汇聚到 JM、跨区不混
- **关联 ADR**: ADR-028
- **依赖**: FR-116, FR-118

#### FR-123: 经济定制页
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 经济业务定制页（余额/排行/转账/流水），高危操作二次确认
- **验收标准**:
  - [ ] 余额查询 + 排行（旁路实现，因 mce 公开 API 无排行）+ 转账 + 流水查询
  - [ ] 面板发起转账/加扣走二次确认 UI；i18n + 暗/亮色
  - [ ] **真机**：定制页对真 mce 查余额排行流水、发起转账生效
- **关联 ADR**: —
- **依赖**: FR-119, FR-121, FR-122

### 里程碑 M3 — 背包整域（扩 api + 适配器 + 定制页）

#### FR-124: 扩 AllinInventorySync api 导出写门面
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 在 AllinInventorySync 仓扩 `api/` 模块，导出背包读写门面（当前公开 api 零写入、读不到离线）。**跨仓 FR，在该仓走其自身 SDD 流程**
- **验收标准**:
  - [ ] 该仓写自身 ADR（api 扩展导出写门面契约）
  - [ ] `getInventory(uuid)`：回源加载（含离线）+ ItemStack→ItemDTO（material/amount/meta/enchants/NBT）
  - [ ] `giveItem/removeItem/applyInventory(...)`：带 Result 回执 + 业务幂等键 + 在线归属校验（拒绝改"正在他服在线"的玩家）+ 委托内部 InventoryEditService delta 通道（两层锁 + CAS）
  - [ ] ItemStack↔JSON codec（信封承载）
  - [ ] **真机**：第三方经 api 读任意玩家背包 + 带回执发/收物品 + 重发幂等不刷物品
- **关联 ADR**: AllinInventorySync 仓 ADR
- **依赖**: 无（独立仓，可与 M1/M2 并行起）

#### FR-125: 背包 Provider
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: ServerProbe 背包适配器 wrap 扩展后的 AllinInventorySync api，含读写动作 + 追踪事件订阅 + manifest
- **验收标准**:
  - [ ] `inventory.snapshot`/`inventory.view`/`inventory.give`/`inventory.remove` 动作 + 背包域 manifest
  - [ ] 订阅 `TrackedItemActionEvent`（重点物品流转）上报
  - [ ] **真机**：inventory.view 看到真实物品清单、give/remove 生效且幂等、离线写下次登录生效
- **关联 ADR**: ADR-026
- **依赖**: FR-117, FR-124

#### FR-126: 背包汇聚与存储
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 背包追踪事件汇聚 + 操作审计 + 离线写"待生效"状态呈现
- **验收标准**:
  - [ ] 追踪事件（JOIN_CARRY/DROP/PICKUP/MOVE_TO_CONTAINER）汇聚去重落库
  - [ ] 背包操作审计表（谁对谁做了什么物品操作）
  - [ ] 离线写后端如实呈现"已写入、待玩家上线生效"，不谎报"已到手"
- **关联 ADR**: ADR-028
- **依赖**: FR-116, FR-121

#### FR-127: 背包定制页
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 背包业务定制页（快照查看/物品清单/远程干预），高危操作二次确认
- **验收标准**:
  - [ ] 玩家背包快照查看 + 物品清单展示（经导出的 ItemDTO）
  - [ ] 远程发物品/收物品走二次确认 UI；离线写显示待生效；i18n + 暗/亮色
  - [ ] **真机**：定制页看真玩家背包、远程发物品生效（在线即时/离线下次登录）
- **关联 ADR**: —
- **依赖**: FR-119, FR-121, FR-125, FR-126

---

## 控制台体验与可寻址性增强

> 源于 2026-06-24 三轮前端真机走查（导航/实例操作/详情页 + 节点/群组/玩家/Bot/监控/告警/日志/备份/计划/模板/运行时/分发/更新/账户/设置/审计全量页）+ 7 个改造方向，共 131 个优化点。决策（sdd-brainstorming）：按主题聚合为 34 FR（FR-128~161）+ 11 BUG（BUG-013~023）+ 2 ADR（030/031），坏的走 BUG-###、大改造配新 ADR。计划全文 `.tmp/brainstorm-console-ux-2026-06-24.md`（不入库）。
> 横切约束：每条 FR 验收含 **i18n(FR-016) 完整 + 暗/亮色(FR-026) 正常 + 真机验证**。
> 波次：**Wave0** 地基/基件先行减返工（ADR-030/031 + FR-160/159）；**Wave1** 架构主线（FR-128 解锁可寻址 → FR-130 → FR-129）；**Wave2** 页面增强大并行（FR-131~158）；BUG/归真随时并行（sdd-fix-bug）。

### 地基 · 可寻址性与工作区（配 ADR-030/031）

#### FR-128: 导航与视图状态可寻址化 + 滚动位置恢复
- **状态**: 📋 计划
- **优先级**: P1
- **描述**: 修订「打开实例存 store、不进 URL」取舍，让导航/视图状态可寻址，支持「返回上一个位置」与鼠标侧键前进/后退（ADR-031）
- **验收标准**:
  - [ ] 先写 **ADR-031**；打开实例走 `/instances/:id`，移除 `openInstanceId` 双轨与「路由变即关闭」hack
  - [ ] 列表筛选/分组、详情激活 Tab、群组/节点下钻进 URL（searchParams/子路由），可深链、刷新还原
  - [ ] 内容滚动容器接入滚动位置恢复；**真机**：鼠标侧键能从实例详情退回列表且筛选/滚动还原
- **关联 ADR**: ADR-031
- **依赖**: 无（地基起点）
- **来源**: 走查 #109,110,111,112,113,43

#### FR-129: 实例工作区分屏面板化
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 兑现 FR-037「分屏/导播台禁用占位」，工作区从固定单 Tab 演进为可拖拽分屏的面板（终端/资源/插件/监控/Bot），状态外提为 panel 树（ADR-030）
- **验收标准**:
  - [ ] 先写 **ADR-030**；面板可水平/垂直拆分、比例可拖拽，可同时打开多个面板；多文件以面板内 Tab 承载
  - [ ] 非激活/未打开面板惰性挂载（不再六 Tab 全预渲染建 WS/discover）；资源面板去固定 h-600 随容器自适应
  - [ ] 各面板自管 dirty/未保存草稿；**真机**：左终端右配置同屏协同
- **关联 ADR**: ADR-030
- **依赖**: FR-130
- **来源**: 走查 #53,42,61,59,58

#### FR-130: 文件与配置合并为统一资源面板
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 删除割裂的「文件」「配置」双 Tab，合为一个资源面板（ConfigExplorer 已是 ResourceExplorer 薄封装，合并近零成本）
- **验收标准**:
  - [ ] 「文件」「配置」合为一个标签/面板；配置发现/收藏作为该面板可选视图
  - [ ] 既有文件浏览/编辑/上传下载/版本回滚能力不丢；**真机**：单面板内完成原两 Tab 全部操作
- **关联 ADR**: ADR-030
- **依赖**: 无
- **来源**: 走查 #52,58

### 侧栏 / 导航

#### FR-131: 侧边栏可折叠图标轨 + 隐藏滚动条 + 布局持久化
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 侧栏可折叠为仅图标轨（hover tooltip 显 label）、导航区滚动条隐藏（保留滚动）、折叠态/选中节点持久化
- **验收标准**:
  - [ ] 折叠开关：展开 w-60 / 折叠仅图标 + tooltip；导航滚动条隐藏不占位
  - [ ] 折叠态、选中节点、导航分组折叠态持久化 localStorage，刷新不重置
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #51,10,13,54

#### FR-132: 主题/语言切换图标化 + 三态直选 + html lang 同步
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 去掉 emoji 主题/语言按钮，改 lucide 图标 + 文字；主题三态可直选（非盲循环）；切语言同步 `<html lang>`
- **验收标准**:
  - [ ] 主题用 Sun/Moon/Monitor 图标 + 文字、可直选 light/dark/system；语言用图标 + 语言名
  - [ ] 切语言同步 document.documentElement.lang
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #56,57,16,17

#### FR-133: 实例树搜索/虚拟化/折叠保留/激活态/空态/a11y
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 侧栏实例树加名称搜索、长列表虚拟化、折叠「实例」组时树不消失、激活态正确、空态 CTA、分组维度 a11y
- **验收标准**:
  - [ ] 树头名称搜索（命中自动展开）；展开分支成员行虚拟化；节点切换+实例树不随导航组折叠而消失
  - [ ] 打开实例时「实例」组呈激活态；空态给「创建实例」CTA；分组维度切换补 aria-pressed
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #8,9,11,12,14,15

#### FR-134: 统一页头与面包屑组件
- **状态**: 📋 计划
- **优先级**: P3
- **描述**: 各页统一页头（标题/副标题/字号一致 + 可点面包屑），消除三页标题字号不一与二级页无「当前在哪」
- **验收标准**:
  - [ ] 统一页头组件被各页复用，标题字号一致；二级页有可点面包屑
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #18,131

### 开源合规

#### FR-135: 开源许可与依赖清单页
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 侧栏底部版本旁加「开源许可」入口，新建页聚合 web(npm)+bot-worker(npm)+Go(go.mod) 直接与传递依赖的名称/版本/许可证/链接
- **验收标准**:
  - [ ] 版本右侧「开源许可」链接 → 依赖清单页；依赖与许可证在构建期扫描生成（不手维护）
  - [ ] 三处依赖来源全覆盖；i18n + 暗/亮色正常
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #55

### 实例列表 / 操作

#### FR-136: 实例列表汇总头 + 节点/端口列 + 角色徽标 + proxy↔backend inline
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 列表加顶部汇总头（运行/停止/崩溃计数，可点=设筛选）、节点·端口列、角色统一徽标、proxy 行 inline 展开已注册 backend
- **验收标准**:
  - [ ] sticky 汇总 chip（运行 N/停止 N/崩溃 M/总数）可点设状态筛选
  - [ ] 表加「节点:端口」列（serverPort 已有数据）；角色三态统一语义色徽标；proxy 行可 inline 展开 backend 摘要
- **关联 ADR**: —
- **依赖**: 无（受益于 FR-128）
- **来源**: 走查 #126,127,128,130

#### FR-137: 实例列表搜索/排序 + 筛选吸顶可折叠 + 分组单表
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 加名称搜索与列排序、筛选区吸顶可折叠、分组视图改单表 sticky 组头（不再每组重复整套表头）
- **验收标准**:
  - [ ] 名称模糊搜索 + 表头排序（至少名称/状态）；筛选/批量条 sticky，长表滚动仍可见
  - [ ] 分组视图单表 + sticky 组头（组名+计数+聚合状态点）
- **关联 ADR**: —
- **依赖**: 无（受益于 FR-128 筛选进 URL）
- **来源**: 走查 #25,114,115,129

#### FR-138: 单实例操作可发现性与反馈
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 次要操作收 kebab、per-row loading/disabled、乐观更新、过渡态动效、运行态禁用+tooltip、详情页操作对齐列表、按钮 aria
- **验收标准**:
  - [ ] 行内仅留启停/重启主操作，余项收「⋯」菜单（删除标红）；操作中行按钮 loading+disabled 防连点
  - [ ] 运行态克隆/删除改禁用+tooltip（非消失）；详情页头部补齐与列表一致操作；过渡态徽章脉冲
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #19,26,27,29,31,33,35

#### FR-139: 批量操作增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 批量部分失败展开明细+保留失败选择、按选中集状态感知禁用、命令下发二次确认、选择范围（可见/不可见）提示、停止防回车直提
- **验收标准**:
  - [ ] 部分失败列出失败实例名+原因并保留其选择供重试；无意义动作按状态分布禁用+tooltip
  - [ ] 批量命令下发前复述「将向 N 个实例下发：<命令>」确认；批量停止/强杀需明确确认不被回车直提
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #21,22,23,24,28,32

### 实例详情面板

#### FR-140: 终端体验增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 终端加重连按钮、全屏、字号调节、搜索（addon-search）、读写态指示、右键菜单 a11y + i18n
- **验收标准**:
  - [ ] 重试耗尽/断开显示「重新连接」按钮；全屏 + 字号 +/−；Ctrl+F 搜索高亮
  - [ ] 常驻可写/只读徽标；右键菜单语义化（role/Esc/方向键）+ 文案 i18n
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #39,40,41,45

#### FR-141: 资源管理器与配置编辑器增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 暗色 CodeMirror、大文件/二进制防护、文件树虚拟化+键盘导航、搜索高亮+范围限定、串行批量进度、配置表单字段分组校验、文本↔表单切换保护、diff 着色
- **验收标准**:
  - [ ] CodeMirror 随暗色模式切主题；超大/二进制文件拦截（只读分段/转下载）；文件树虚拟化 + role=tree 键盘导航
  - [ ] 搜索片段高亮 + 目录/类型范围；粘贴/移动串行批量给进度+部分失败汇报；配置表单字段分组+即时校验；版本 diff 增删着色
- **关联 ADR**: —
- **依赖**: 无（与 FR-129/130 协同）
- **来源**: 走查 #49,60,62,63,64,65,66,67,68

#### FR-142: 详情页监控与探针增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 详情页引入实例历史曲线（复用 TimeSeriesChart）、探针富指标阈值着色、探针未安装可操作指引、指标卡停机折叠
- **验收标准**:
  - [ ] 终端/监控面板展示 TPS/内存/玩家历史曲线（带 RangePicker）；TPS/MSPT/CPU 按阈值绿/黄/红
  - [ ] 探针未安装提示带安装指引/入口；停机时指标卡折叠不占大块空白
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #38,47,48

#### FR-143: 插件/模组/资源包/数据包管理增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 按类型分区管理（插件/模组/资源包/数据包）、jar 元信息解析、拖拽+进度上传、同名覆盖确认、运行态需重启提示、市场入口预留
- **验收标准**:
  - [ ] 区分 plugins/mods/datapacks/resourcepacks 分区与校验；解析 plugin.yml/fabric.mod.json 展示版本/作者/依赖
  - [ ] 拖拽上传 + 进度；同名覆盖二次确认；启禁/删除提示「重启后生效」
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #50,69,70

### 节点

#### FR-144: 节点页直观化
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 节点页加集群汇总头、主表精简列（次要信息进详情）、操作收 kebab、详情展开分段+可见才轮询
- **验收标准**:
  - [ ] 顶部 sticky 集群概览（在线/离线/维护 + 集群 CPU/内存/磁盘聚合水位）
  - [ ] 主表保留名称/状态/CPU/内存/磁盘/操作（网络/系统/IP 进详情）；操作收 kebab（排空/下线标危险）
  - [ ] 详情区分段（概览/趋势/实例对比）；展开/不可见时不起多余轮询
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #118,119,72,120,73,74

### 群组（网络）

#### FR-145: 群组管理可寻址双栏 + proxy↔backend 拓扑
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 群组详情从滚动模态改为可寻址双栏（左成员/右候选）、加 proxy↔backend 拓扑视图、列表成员健康分布、候选补节点/状态/端口、成员状态用 StatusBadge
- **验收标准**:
  - [ ] 群组详情可深链（路由/searchParams）；管理改左右双栏消嵌套滚动
  - [ ] proxy↔backend 注册关系拓扑/缩进可视；列表行显示成员状态分布（运行/崩溃）；候选项含节点·状态·端口可筛
- **关联 ADR**: —
- **依赖**: 无（受益于 FR-128）
- **来源**: 走查 #121,122,123,124,125

### 玩家

#### FR-146: 玩家管理增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 在线玩家跨服全局封禁 + 多选批量踢/封、实时事件流暂停+类型过滤+清空、操作按钮统一语义色
- **验收标准**:
  - [ ] 行勾选批量踢/封 + 跨服全局封禁（同名多服一键）；按子服分组/筛选
  - [ ] 事件流暂停滚动开关 + 类型过滤 chips + 清空；踢/封按钮用 destructive 语义色
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #75,76,77

### Bot

#### FR-147: Bot 规模化管理增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: Bot 健康条多段着色（兑现 FR-040 备注「细分 connecting/error」）、多组批量删除二次确认+进度、行为参数化入口
- **验收标准**:
  - [ ] 健康条按 connected/connecting/error/stopped 多段着色（数据来自 summary byStatus）
  - [ ] 多组批量删除前 DangerConfirm + 串行进度提示；行为下拉旁「配置」入口暴露巡逻路径/跟随目标等参数
- **关联 ADR**: —
- **依赖**: FR-040（Bot 全局管理页，已交付）
- **来源**: 走查 #78,79,80

### 监控 / 告警 / 日志

#### FR-148: 趋势图增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: TimeSeriesChart 加多序列图例、Y 轴自适应 domain（非占比指标）、空态 i18n
- **验收标准**:
  - [ ] 多序列常驻图例区分；TPS/MSPT 等窄幅指标 Y 轴 `domain=['auto','auto']` 显波动；空态走 i18n
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #84,85

#### FR-149: 告警增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 规则行内启停 Switch（补 enabled 字段）、测试通道结果反馈、事件时间范围+规则+关键字筛选+分页、静默跨夜可视+时区标注
- **验收标准**:
  - [ ] 规则表 Switch 直接 toggle enabled（编辑弹窗补 enabled 字段）；测试通道弹「✓已送达/✗错误详情」
  - [ ] 事件加时间范围+规则+关键字筛选与分页；静默时段跨夜可视化 + 时区标注
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #81,82,86

#### FR-150: 日志中心增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 日志加实时跟随（tail）、时间范围筛选、虚拟滚动、级别本地化+统一配色、导出范围/格式
- **验收标准**:
  - [ ] 「实时跟随」开关 + 时间范围；长列表虚拟滚动；级别 Badge i18n + 与告警页统一配色
  - [ ] 导出可选范围（当前页/全部匹配/时间段）与格式
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #87

### 备份 / 存储 / 计划 / 模板

#### FR-151: 备份页增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 进行中状态自动刷新进度、总占用+份数汇总、校验和展示、增量备份链父子可视+删除依赖警告
- **验收标准**:
  - [ ] 存在「进行中」备份时条件轮询进度直至完成；顶部汇总（总占用/份数/最近成功）
  - [ ] 展示校验和；增量行显示父备份，删父备份警告「N 个增量依赖」
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #89,93,94

#### FR-152: 备份存储测试连接 + 容量展示
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 备份存储后端加「测试连接」、列表显示已用容量/已存份数
- **验收标准**:
  - [ ] 表单与行各有「测试连接」即时反馈成功/失败原因；列表显示每后端备份数/已用空间
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #90

#### FR-153: 计划任务增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: cron 人类可读翻译 + 下次执行预览 + 常用预设、时区标注、编辑 command 回填保护
- **验收标准**:
  - [ ] 合法表达式显示可读描述 + 接下来 3-5 次执行时间；常用预设快捷按钮；cron 与「上/下次执行」标注时区
  - [ ] 编辑 command 任务时回填或显式提示「留空将清除原命令」
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #95 + #91 可视化部分（校验 bug 见 BUG-020）

#### FR-154: 模板应用到实例 + 变量填充预览
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 模板页加「用此模板创建实例」入口 + 变量填充预览、startCommand 复制、版本/更新时间
- **验收标准**:
  - [ ] 模板卡片「用此模板创建」跳转创建流程并预填；含占位变量时填充预览
  - [ ] startCommand 一键复制 + 展开查看；显示版本/更新时间
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #92

### 平台 / 资产 / 更新 / 数据库

#### FR-155: 平台资产与更新页增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 制品/JDK 下载进度、被引用制品禁删前移、拉取密钥过期预警、系统更新前置检查+回滚+金丝雀、数据库敏感列防护
- **验收标准**:
  - [ ] 制品/JDK 导入下载显示进度；refCount>0 时删除按钮禁用+tooltip；拉取密钥按 expiresAt 显「已/即将过期」
  - [ ] 系统更新展示 changelog/回滚说明、CP 自更新强确认、全网升级金丝雀分批；数据库敏感列禁排序/过滤+标注
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #96,97,98,99

### 账户 / 设置 / 认证

#### FR-156: 用户与组管理能力补齐
- **状态**: 📋 计划
- **优先级**: P1
- **描述**: 补齐缺失的管理能力——用户改密/启停/编辑；用户组编辑/删除/配额修改/成员增删 UI 接入（兑现 FR-003 归真）
- **验收标准**:
  - [ ] 用户行可改密/启停/编辑；组可编辑/删除（接通已实现的 useDeleteGroup）/改配额/管成员
  - [ ] 创建用户角色选项带权限差异说明；**真机**：改密后新密码可登录、停用账户被拒登录
- **关联 ADR**: —
- **依赖**: 无（归真 FR-003）
- **来源**: 走查 #100,101

#### FR-157: 认证体验增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 登录密码显隐切换、Setup 密码强度提示+实时规则、字段级错误聚焦
- **验收标准**:
  - [ ] 密码框 eye 显隐；Setup 实时强度条 + 规则 checklist；密码不一致错误绑定到 confirm 字段并聚焦
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #104

#### FR-158: 设置与审计页增强
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 设置切分类未保存拦截 + 生效时机说明 + 安全项隔离；审计导出 + 行详情展开 + 真实总数分页
- **验收标准**:
  - [ ] 设置切分类有未保存草稿时拦截确认；非即时项 tooltip 说明生效时机；security 项视觉隔离
  - [ ] 审计支持导出 + 行展开看变更详情 + 显示真实命中总数与分页
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #105 + 审计

### 一致性 / 响应式（ref + 基线）

#### FR-159: 共享对话框统一
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 裸 div 模态框（CreateUser/CreateGroup/Provision* 及节点/玩家/告警弹窗）统一迁 Radix Dialog（role/aria-modal/焦点陷阱/Esc/autofocus）；一次性 secret 改持久可复制展示
- **验收标准**:
  - [ ] 全部对话框走 shadcn/Radix Dialog，键盘 Esc 关、焦点陷阱、打开自动聚焦首字段
  - [ ] forwarding secret 等关键凭据用带复制的持久面板展示（非 15s toast）
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #102,107,103,88

#### FR-160: 共享基件统一
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 表格/按钮/危险确认/状态徽标/语义色收敛到单一来源（消除原生 table、原生 button、原生 confirm()、裸状态文本三套并存）
- **验收标准**:
  - [ ] 列表统一 shadcn Table；按钮统一 Button；危险确认统一 DangerConfirm（移除原生 confirm）
  - [ ] 状态统一 StatusBadge（详情页/群组成员等不再裸英文枚举）；危险/语义色用 token 不硬编码
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #108,77,2,125
- **备注**: ref 重构，行为不变，走 sdd-refactor-code

#### FR-161: 全局响应式与防翻屏基线
- **状态**: 📋 计划
- **优先级**: P2
- **描述**: 控制台外壳与各页响应式（移动断面 + 抽屉式侧栏）、列表分页/虚拟滚动、页头工具栏吸顶、模态去嵌套滚动
- **验收标准**:
  - [ ] 窄屏侧栏收抽屉 + 汉堡；工作区/表格不被挤到不可用
  - [ ] 长列表分页或虚拟滚动；页头/工具栏 sticky；模态用抽屉/双栏替代内层嵌套滚动
- **关联 ADR**: —
- **依赖**: 无
- **来源**: 走查 #7,116,117,114

### 缺陷（BUG-013~023，走 sdd-fix-bug，修复后入 CHANGELOG）

- **BUG-013**: 实例详情「备份」标签永久「加载中」空壳——`BackupsTab` 硬编码 loading 从不调 `useBackups`，备份/恢复/下载/删除全不可用（走查 #1）
- **BUG-014**: 实例详情头部状态显原始英文 `CRASHED`，与列表本地化「崩溃」不一致（走查 #2）
- **BUG-015**: i18n 泄漏——英文模式图表硬编码中文「暂无数据」+ 单实例操作 toast 中文硬编码（走查 #3,30）
- **BUG-016**: 主题首帧 FOUC——`useThemeStore` 初值固定 system 不读 localStorage，保存为 dark 者首屏闪浅色（走查 #5）
- **BUG-017**: 离线实例详情仍重复触发 422——未在节点/实例离线时短路 token/discover 请求（走查 #6）
- **BUG-018**: 配置/文件编辑器切换/关闭丢弃未保存草稿、无拦截，造成数据丢失（走查 #36,37）
- **BUG-019**: 节点列表离线节点显示 0% 资源条而非「无数据」，误导为在线空载（走查 #71）
- **BUG-020**: 计划任务 cron 非法表达式（如 `,,,`）通过前端 `FIELD_RE` 校验（走查 #91 校验部分）
- **BUG-021**: 告警规则编辑弹窗缺 enabled 字段，前端无法切换规则启用态（走查 #81 部分）
- **BUG-022**: 创建用户密码下限(6)与 Setup 初始化(8)不一致（走查 #106）
- **BUG-023**: 仪表盘负载环判级**待复核**——走查 #83 据代码推断「倍数被当百分比致判级失效」，但 FR-108 在 v0.9.1 走查已验证「分级配色本就生效、全红是机器确实满载」，二者冲突，需先真机复核再定是否为缺陷（不擅自归真 FR-108）

### 归真（父 FR 标 done 实则缺，退「开发中」+ sdd-fix-bug）

- **FR-003**（用户组与配额）：验收「创建/编辑/删除用户组」标 done，但 `GroupsPage` 当前只读、`useDeleteGroup` 未接入 UI → 由 FR-156 兑现，状态退回开发中（走查 #101）
- **FR-059**（危险操作保护体系化）：验收「删除/强制关服…均接入」，但单实例「强杀」零二次确认（与批量要求 FORCE 自相矛盾）→ 由 FR-139/138 兑现，状态退回开发中（走查 #20）

---

## V1 不包含（后续版本）

| FR | 描述 | 预计版本 |
|---|---|---|
| FR-100 | MFA（TOTP 二步验证） | V1.1 |
| FR-101 | Control Plane 高可用 | V1.2 |
| FR-102 | 真正的多租户（tenant_id 隔离） | V2.0 |
| FR-104 | JVM 诊断（Arthas/JFR/JMX） | V1.2 |
| FR-105 | 邮件通知 | V1.1 |
| FR-106 | WebSocket 用户→Control Plane（全局事件推送） | V1.1 |

> FR-103（插件桥）已提前纳入「运维全功能扩展」批次（因 FR-055 依赖）。
