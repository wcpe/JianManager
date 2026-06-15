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
- **状态**: 📋 todo
- **优先级**: P0
- **描述**: 平台管理员/组管理员/组成员三级角色，基于权限节点的 RBAC
- **验收标准**:
  - [ ] 平台管理员可管理所有用户和节点
  - [ ] 组管理员可管理组内成员和实例分配
  - [ ] 组成员只能操作分配给自己的实例
  - [ ] 权限中间件拦截未授权请求
- **关联 API**: `GET/POST /users`, `GET/POST /groups`, `POST /groups/:id/members`

### FR-003: 用户组与配额
- **状态**: 📋 todo
- **优先级**: P0
- **描述**: 用户组管理，实例分配给组，配额限制（最大实例数、Bot 数、存储空间）
- **验收标准**:
  - [ ] 创建/编辑/删除用户组
  - [ ] 组内添加/移除成员
  - [ ] 实例分配给组（一个实例只属于一个组）
  - [ ] 配额检查：创建实例时校验组配额
- **关联 API**: `POST /groups`, `POST /groups/:id/instances`, `GET /groups/:id/quota`

### FR-004: 节点注册与心跳
- **状态**: 📋 todo
- **优先级**: P0
- **描述**: Worker Node 启动时 gRPC 注册到 Control Plane，30s 心跳上报资源指标
- **验收标准**:
  - [ ] Worker 首次启动自动注册，获得 node_uuid + node_secret
  - [ ] 30s 心跳间隔，上报 CPU/内存/磁盘
  - [ ] Control Plane 检测离线（超 90s 无心跳）
  - [ ] 前端节点列表实时显示在线状态
- **关联 API**: `GET /nodes`, `GET /nodes/:id`

### FR-005: 实例生命周期管理
- **状态**: 📋 todo
- **优先级**: P0
- **描述**: 实例创建/启动/停止/重启/销毁，状态机驱动，支持四种启动方式
- **验收标准**:
  - [ ] 创建实例：选择节点、类型、启动方式、启动命令
  - [ ] 启动/停止/重启/强制终止操作
  - [ ] 状态机：STOPPED → STARTING → RUNNING → STOPPING → STOPPED / CRASHED
  - [ ] 崩溃自动重启（指数退避）
  - [ ] 实例分配给用户组
- **关联 API**: `POST /instances`, `POST /instances/:id/start`, `POST /instances/:id/stop`

### FR-006: 守护进程（Daemon Wrapper）
- **状态**: 📋 todo
- **优先级**: P0
- **描述**: 平台进程重启不杀游戏服，通过 Daemon Wrapper 子进程实现进程隔离
- **验收标准**:
  - [ ] 启动方式为 daemon 时，spawn 独立子进程管理游戏服
  - [ ] 二进制帧协议通信（Unix Socket / Named Pipe）
  - [ ] 平台重启后恢复守护进程连接
  - [ ] 崩溃自动重启 + PID 文件恢复
- **关联 ADR**: ADR-003

### FR-007: 终端实时
- **状态**: 📋 todo
- **优先级**: P0
- **描述**: xterm.js 浏览器终端，直连 Worker Node WebSocket，支持多人同时查看
- **验收标准**:
  - [ ] Control Plane 签发一次性 30s token
  - [ ] 浏览器持 token 直连 Worker Node WS
  - [ ] stdin/stderr 双向流
  - [ ] 多人同时查看（读写分离）
  - [ ] 环形缓冲区回放最近输出
- **关联 API**: `GET /instances/:id/terminal-token`

### FR-008: 文件管理
- **状态**: 📋 todo
- **优先级**: P0
- **描述**: 实例工作目录文件浏览/编辑/上传下载
- **验收标准**:
  - [ ] 文件列表浏览（目录树）
  - [ ] CodeMirror 在线编辑（YAML/TXT/JSON 高亮）
  - [ ] 文件上传（分块）/ 下载（流式）
  - [ ] 创建/删除/重命名
- **关联 API**: `GET /instances/:id/files`, `GET /instances/:id/files/read`, `POST /instances/:id/files/write`

---

## P1 — 重要功能

### FR-009: Bot 平台
- **状态**: 📋 todo
- **优先级**: P1
- **描述**: Mineflayer Bot 管理，行为引擎、寻路、脚本执行、压测、预热池
- **验收标准**:
  - [ ] 创建/删除 Bot（选择目标 MC 服务器）
  - [ ] 行为模式切换（follow/guard/patrol/idle/custom）
  - [ ] 寻路（mineflayer-pathfinder）
  - [ ] 脚本执行 + 进度上报
  - [ ] 压测会话（批量上线/下线）
  - [ ] 预热池（预创建空闲 bot）
  - [ ] 容量：50 bots/worker，256 workers max
- **关联 API**: `POST /bots`, `POST /bots/:id/behavior`, `GET /bots/:id/state`

### FR-010: 监控指标
- **状态**: 📋 todo
- **优先级**: P1
- **描述**: 节点和实例指标采集，Recharts 仪表盘展示
- **验收标准**:
  - [ ] 节点指标：CPU/内存/磁盘/网络（周期采集）
  - [ ] 实例指标：MC TPS/在线玩家/内存（MC 专用）
  - [ ] 仪表盘页面：Recharts 图表
- **关联 API**: `GET /nodes/:id/metrics`, `GET /instances/:id/metrics`

### FR-011: 告警规则
- **状态**: 📋 todo
- **优先级**: P1
- **描述**: 阈值触发告警，Webhook 通知
- **验收标准**:
  - [ ] 创建告警规则（metric + operator + threshold + duration）
  - [ ] 触发后发送 Webhook
  - [ ] 告警事件列表
- **关联 API**: `POST /alerts/rules`, `GET /alerts/events`

### FR-012: 定时任务
- **状态**: 📋 todo
- **优先级**: P1
- **描述**: Cron 表达式调度，支持实例启停/命令执行/备份
- **验收标准**:
  - [ ] 创建/编辑/删除定时任务
  - [ ] Cron 表达式解析
  - [ ] 支持 action: start/stop/restart/command/backup
  - [ ] 执行日志
- **关联 API**: `POST /schedules`, `GET /schedules`

### FR-013: 备份恢复
- **状态**: 📋 todo
- **优先级**: P1
- **描述**: 手动/自动备份，压缩存储，一键恢复
- **验收标准**:
  - [ ] 手动创建备份
  - [ ] 备份列表（大小/时间/类型）
  - [ ] 一键恢复到指定备份
  - [ ] 自动备份（通过定时任务）
- **关联 API**: `POST /instances/:id/backups`, `POST /backups/:id/restore`

---

## P2 — 增强功能

### FR-014: 服务端模板
- **状态**: ⏸️ deferred
- **优先级**: P2
- **描述**: 预设 MC 服务端模板（Paper/Spigot/Forge），一键创建实例
- **验收标准**:
  - [ ] 模板列表（名称/类型/描述/图标）
  - [ ] 从模板创建实例（自动填充启动命令和配置）

### FR-015: 审计日志
- **状态**: 📋 todo
- **优先级**: P2
- **描述**: 操作审计（谁/什么时间/对什么/做了什么）
- **验收标准**:
  - [ ] 关键操作自动记录（实例启停/文件修改/用户管理）
  - [ ] 审计日志查询（按用户/操作/时间筛选）

### FR-016: i18n
- **状态**: 📋 todo
- **优先级**: P2
- **描述**: 中文 + 英文国际化
- **验收标准**:
  - [ ] 前端 i18next 切换
  - [ ] 所有 UI 文本可翻译

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
