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
- **状态**: 🔨 in-progress（归真：后端 CRUD+执行日志齐全，前端原仅只读列表；2026-06-22 e2e 截图巡检发现 done 误标，退回补齐。已补：创建/编辑/删除对话框 + 删除危险确认 + 行内启停 + 执行日志行展开 + Cron 基本校验，并套 FR-061 高密度风格；待主树构建/类型/单测验证与用户验收）
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
- **状态**: 🔨 in-progress（归真：后端筛选 user/action/targetType/from/to 全支持；前端筛选 UI 已补全，待主树构建 + 真机验证后由用户确认 done）
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
- **备注**: 真 Paper 1.20.4 + 真 BungeeCord 26.1 端到端复验通过——Mineflayer 客户端经代理（25566）进入后端 lobby（`ServerConnector [lobby] connected` + 后端 `ProxyTester joined`）。追加可选 online-mode（持久化，离线模式群组服可关闭）。
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
- **状态**: ✅ done
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
- **状态**: 📋 todo
- **优先级**: P2
- **描述**: 系统设置页此前仅主题/语言（客户端 localStorage）。新增服务端平台配置：后端键值存储 + `GET/PUT /settings`（仅平台管理员），前端按分组表单暴露**全部平台配置**。可安全运行时调整的项落库即生效（覆盖 env/YAML 默认）；启动固定/敏感项只读展示、敏感打码并标注「需改配置并重启」。
- **验收标准**:
  - [ ] **先写 ADR**：配置覆盖优先级（DB > env > YAML 默认）与生效边界，评估对 ADR-005 / `config-files` 规则的影响
  - [ ] 后端 `platform_settings` 存储 + `GET /settings` + `PUT /settings`（RBAC：仅平台管理员）+ AutoMigrate + service 测试
  - [ ] **可运行时生效**项（落库覆盖默认、改完即生效无需重启，且接到真实读取点）：日志级别、JDK 下载镜像源（Temurin/Corretto/Zulu）、优雅停止超时、默认备份保留天数
  - [ ] **只读展示**项（启动固定）：server host/port、gRPC 端口、数据库 driver/dsn、JWT secret（打码）、access/refresh TTL —— 展示当前生效值 + 「需改配置并重启」提示
  - [ ] 敏感值不明文下发前端（secret 打码或不返回）
  - [ ] 前端系统设置页保留外观/语言分组，新增「平台配置」分组表单，按可编辑/只读分区，保存调 `PUT /settings`
  - [ ] i18n zh/en；暗色/亮色正常
  - [ ] 真机：改日志级别即时生效；改 JDK 镜像源后下 JDK 走新源；只读项正确反映当前配置
- **关联 ADR**: 需新增（配置覆盖优先级）；评估 ADR-005
- **关联 API**: 新增 `GET /settings`、`PUT /settings`
- **依赖**: 无

### FR-064: 模板管理 UI 与模板删除
- **状态**: 📋 todo
- **优先级**: P2
- **描述**: FR-014 仅做到「用模板建实例」（消费侧，已 done）；本 FR 补模板的 UI 管理——新建（接已有后端 `POST /templates` + 闲置的 `useCreateTemplate`）、删除（新增后端 `DELETE /templates/:id`）。模板与实例松关联（建实例时拷贝 startCommand），删除模板不影响已建实例。顺带按 FR-061 高密度风格重写模板页。
- **验收标准**:
  - [ ] 模板页「新建模板」按钮 → 对话框（名称/类型/描述/启动命令/下载URL/默认工作目录）→ `POST /templates`
  - [ ] 模板卡/行可删除（DangerConfirm 危险确认）→ 新增 `DELETE /templates/:id`
  - [ ] 后端新增 `DELETE /templates/:id`（service + handler + 路由 + 测试）
  - [ ] 模板页套 FR-061 高密度风格
  - [ ] i18n zh/en
  - [ ] 真机：新建 Paper 模板 → 建实例对话框能选到并自动填充 → 删除该模板、已建实例不受影响
- **关联 FR**: FR-014（用模板建实例，已 done）
- **关联 API**: 复用 `POST /templates`；新增 `DELETE /templates/:id`
- **依赖**: 无

### BUG-007: 监控图表在 0 尺寸容器渲染告警
- **状态**: 🔨 in-progress
- **优先级**: P2
- **描述**: 控制台反复出现 recharts `width(-1)/height(-1) ... should be greater than 0` 告警（×9）。根因：ResponsiveContainer 在隐藏/未激活分段或折叠面板（0 尺寸容器）内渲染，可能导致切换时图表瞬时空白。
- **验收标准**:
  - [ ] 控制台不再出现 recharts width/height 告警
  - [ ] 折叠面板/未激活分段内的图表切换后正常渲染、无持续空白
  - [ ] 修复不破坏现有图表（总览/节点/实例监控）
- **关联 FR**: FR-060（时序图表）, FR-061（面板）

### BUG-008: 前端存在 401 Unauthorized 资源请求
- **状态**: 🔨 in-progress
- **优先级**: P2
- **描述**: 页面加载期控制台出现一条 401 Unauthorized 资源请求。需定位来源（endpoint + 触发时机 + 根因），判定是 token 刷新时序、未鉴权探测还是真实缺陷，并修复或消除噪声。
- **验收标准**:
  - [ ] 定位 401 来源请求（endpoint + 触发时机 + 根因）
  - [ ] 修复（补鉴权 / 改时序 / 若无害则消除噪声请求）
  - [ ] 正常登录态下控制台无 401 报错
- **关联 FR**: FR-001（认证）, FR-024（前端对接运行时 API）

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
