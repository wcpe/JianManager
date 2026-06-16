# CHANGELOG

> 本文档累积更新，每次发版新增一个版本段。

---

## [Unreleased]

### Added
- SDD 开发体系（文档 + 规则 + 技能）
- PRD, ARCHITECTURE, API, ADR 基础文档
- RBAC 授权服务（`service/authz.go`）：三级角色权限节点、用户组隔离、实例/Bot 资源级访问判断
- 权限中间件：`LoadAccess` 加载授权上下文、`RequirePermission`/`RequireGroupAccess`/`RequireInstanceAccess` 等
- `GET /api/v1/groups/:id/quota` 接口，返回组配额及当前用量（实例数/Bot 数/存储用量）

### Changed
- 实例/文件/终端/Bot 路由按用户组隔离：组管理员仅管理本组、组成员仅操作所属组实例，跨组访问返回 404
- 节点管理限平台管理员
- 实例创建配额检查补全 `MaxBots`（组 Bot 数达上限拒绝）与 `MaxStorageMB`（组内备份总大小超额拒绝）

### Changed
- FR-006 守护进程按 ADR-003 真实现：引入 `IProcessCommand` 策略接口按 `ProcessType` 路由（direct/daemon/docker）；daemon 模式 spawn 独立 wrapper 子进程（复用 worker 二进制 `daemon` 子命令），脱离 Worker 进程组隔离；通过 Unix Socket（Linux/macOS）/ Named Pipe（Windows，npipe）+ 二进制帧协议通信；PID 文件（JSON）恢复 + `RecoverDaemonInstances` 重连；daemon 模式 `StopAll` 优雅断开不杀游戏服；`registerOnWorker` 补传 `ProcessType`。

#### 核心功能 (P0)
- 用户认证：JWT 双 Token（15min access + 7d refresh），bcrypt 密码加密
- 用户与权限：平台管理员/组管理员/组成员三级角色，RBAC 权限中间件
- 用户组与配额：组 CRUD、成员管理、实例配额检查
- 节点注册：gRPC 注册/心跳/离线检测（90s 超时）
- 实例生命周期：状态机 + 指数退避自动重启
- 守护进程：二进制帧协议 + PID 文件恢复 + 环形缓冲区
- 终端：gorilla/websocket + xterm.js + 30s 一次性 token
- 文件管理：目录浏览 + 在线编辑 + 上传下载 + 路径安全检查

#### 重要功能 (P1)
- Bot 平台：行为引擎框架（idle/follow/patrol/guard）
- 监控指标：gopsutil 采集 + Recharts 仪表盘
- 告警规则：阈值触发 + Webhook 通知
- 定时任务：Cron 调度器
- 备份恢复：手动创建/列表/删除

#### 增强功能 (P2)
- 服务端模板：CRUD + 卡片式展示
- 审计日志：中间件自动记录 + 查询
- i18n：i18next 中英文

#### 前端
- 11 个管理页面 + 实例详情页（6 Tab）
- 文件浏览器 + 终端组件 + 创建实例对话框

#### 基础设施
- go:embed 前端嵌入，单二进制部署
- Docker 部署
- gRPC 协议桩代码
- API 限流中间件
- 15 个单元测试
