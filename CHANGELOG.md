# CHANGELOG

> 本文档累积更新，每次发版新增一个版本段。

---

## [Unreleased]

（暂无）

---

## 0.1.1（2026-06-17）

Bug 修复 + 前端 UX 标准化版本。修复终端连接闪烁、启动命令引号、文件浏览器 422 等实际使用问题。

### Added
- **前端通知系统**（FR-030）：引入 sonner Toast 通知库，全局 Toaster 组件
  - 实例操作（启动/停止/重启/终止/删除）使用 Toast 反馈
  - 文件操作（保存/上传/删除/重命名）使用 Toast 反馈
  - 创建实例错误使用 Toast 替代内联 error div
- **ConfirmDialog 组件**：可复用的确认对话框，替代所有 `window.confirm()` 调用
  - 实例删除、Bot 删除、用户删除、备份恢复均使用 ConfirmDialog

### Fixed
- **终端连接闪烁**（BUG-002）：Terminal 组件在 token 加载完成前显示 spinner 占位，不再出现 [连接错误]；WebSocket 连接失败自动重试（最多 3 次，间隔递增）
- **控制台与终端 Tab 重复**（BUG-003）：合并为单一「终端」Tab，上方显示实例指标（TPS/在线/内存），下方为可交互终端
- **文件浏览器 422 错误**（BUG-004）：创建实例时 workDir 改为必填；后端在 workDir 为空时返回明确错误信息
- **启动命令多余引号**（BUG-005）：后端 `sanitizeStartCommand()` 去除外层引号包裹；前端添加「不要用引号包裹」提示
- **cmd /s /c 回归**：Go `exec.Command` 的 `EscapeArg` 与 cmd.exe `/s /c` 不兼容，回退为 `cmd /c`

---

## 0.1.0（2026-06-17）

首个正式版本。覆盖 PRD P0 全部 9 条功能需求 + P1/P2 主要能力。

### Added

#### 核心功能 (P0)
- **首次启动引导**（FR-017）：Web UI 引导管理员设置账号，替代配置文件 bootstrap
- **用户认证**（FR-001）：JWT 双 Token（15min access + 7d refresh），bcrypt 密码加密，自动刷新
- **用户与权限**（FR-002）：平台管理员/组管理员/组成员三级角色，RBAC 权限节点中间件
- **用户组与配额**（FR-003）：组 CRUD、成员管理、实例配额检查（实例数/Bot 数/存储）
- **节点注册与心跳**（FR-004）：gRPC 注册/30s 心跳/90s 离线检测，前端实时在线状态
- **实例生命周期**（FR-005）：状态机（STOPPED→STARTING→RUNNING→STOPPING→CRASHED），指数退避自动重启
- **守护进程**（FR-006）：IProcessCommand 策略路由，daemon wrapper 子进程隔离，Unix Socket/Named Pipe 二进制帧协议，PID 文件恢复
- **终端**（FR-007）：xterm.js 浏览器终端，直连 Worker WebSocket，一次性 30s token，多人同时查看，环形缓冲区回放
- **文件管理**（FR-008）：目录浏览 + CodeMirror 在线编辑 + 分块上传/流式下载 + 路径安全检查

#### 重要功能 (P1)
- **Bot 平台**（FR-009）：Mineflayer Bot 管理，行为引擎框架（idle/follow/patrol/guard）
- **监控指标**（FR-010）：gopsutil 采集 + Recharts 仪表盘
- **告警规则**（FR-011）：阈值触发 + Webhook 通知
- **定时任务**（FR-012）：Cron 调度器 + 执行日志
- **备份恢复**（FR-013）：手动创建/列表/恢复

#### 增强功能 (P2)
- **服务端模板**（FR-014）：预设模板 CRUD + 卡片展示
- **审计日志**（FR-015）：中间件自动记录 + 时间筛选查询
- **i18n**（FR-016/020）：i18next 中英文双语，19 个命名空间

#### 前端
- 11 个管理页面 + 实例详情页（6 Tab：终端/文件/设置/监控/备份/Bot）
- 文件浏览器（CodeMirror）+ 终端组件（xterm.js）+ 创建实例对话框

#### 基础设施
- go:embed 前端嵌入，单二进制部署
- gRPC Control Plane ↔ Worker 通信协议
- Docker 部署支持
- API 限流中间件
- RBAC 授权服务（三级角色 + 用户组隔离 + 资源级访问判断）
- E2E 测试框架（testcontainers-go）

### Fixed
- 终端 WebSocket URL 改用请求 Host 构造，修复反代场景连接失败（FR-007/019）
- 备份恢复改为文件级恢复，正确传递实例工作目录给 Worker
- i18n 缺失 key 补齐 + 组件硬编码中文替换为 t() 调用
- daemon PID 清理测试在 Windows 上的 TempDir 竞态修复
