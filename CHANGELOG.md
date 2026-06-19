# CHANGELOG

> 本文档累积更新，每次发版新增一个版本段。

---

## [Unreleased]

（暂无）

---

## 0.3.0（2026-06-20）

全链路运维打通 + 终端重写 + daemon 健壮性版本。打通「节点 → 实例 → 终端 → Bot 进服」完整运维链路（FR-043），落地一键搭建 Paper 子服向导（FR-034），重写终端体验，并修复一批 daemon 生命周期与控制面健壮性缺陷。

### Added
- **全链路运维打通**（FR-043）：节点在线 → 创建启动真实 MC 实例 → 浏览器终端交互 → Bot 真正进服 → 运维闭环；全链路 e2e 覆盖节点/实例/终端/Bot 进服/停止断开五条验收
- **一键搭建 Paper 子服向导**（FR-034）：选版本/资源，系统分配目录与端口、下载核心、写 eula/server.properties、结构化启动；向导默认绑定节点最高版本已装 JDK
- **接通 Bot 进服 gRPC 链路**：Worker 实现 CreateBot/DeleteBot/SetBotBehavior/SendBotCommand/ListBots 并托管 bot-worker；CP 据 Config 与实例解析连接目标、用 Bot 名作游戏内用户名、经 ListBots 实时回填状态（此前 Worker 侧无实现、dist 为 mock 桩）
- **运维控制台统一**：点击实例名进统一控制台（终端/文件/配置/Bot），工具栏含启动/停止/重启/强制终止
- **终端重写**：上下命令历史 + 右侧历史抽屉；Tab 补全命令、在线玩家名（据输出实时维护）与 `@` 选择器；拖选复制/全选/复制全部/保存日志/右键菜单；停服终端保持连接转只读并续流关服日志
- **文件编辑器语法高亮**（FR-008）：CodeMirror 6 集成，按扩展名 YAML/JSON 语法高亮 + 行号 + 撤销/重做（此前为纯 textarea）

### Fixed
- **daemon 优雅停止看不到停止日志**：停止改为向 stdin 发 MC `stop` 优雅关服、流出完整停止日志（Stopping/Saving worlds…），此前 Windows 一律 `taskkill` 硬杀；超时强杀兜底（`JIANMANAGER_GRACEFUL_STOP_TIMEOUT` 可覆盖）
- **停服后无法再启动**：实时态心跳曾把停止瞬态 STOPPING 覆盖已记账 STOPPED，致 STOPPING→STARTING 被拒；改为仅 RUNNING→CRASHED 纠正
- **daemon 重启 panic**：Start 重置 connectDone，避免 close of closed channel 崩溃整个 Worker
- **崩溃实例永显 RUNNING 不可删**：wrapper 连续快速崩溃放弃自动重启并退出、心跳实时态对账落 CRASHED
- **实例恢复工作目录丢失**：PID 记录持久化 WorkDir，Worker 重启恢复后文件/配置不再 `open` 空路径
- **CP 重启失联**：Heartbeat 重建 gRPC 连接池、对账未恢复实例为 STOPPED，避免 NODE_OFFLINE 与永卡 RUNNING（生命周期 422）
- **Bot 重连/创建**：CP 批量启动补传连接目标、bot-worker 重建已存在连接；创建表单 config JSON 序列化修复 400
- **终端一次性 token TTL**：30s → 10min，修复重开终端复用过期 token 握手失败
- **direct 停止进程树泄漏**：`taskkill /T` 递归终止，避免泄漏 java/node 子进程
- **控制台导航**：点击侧栏导航即关闭实例工作区，修复从实例页打开控制台后点同路由「实例」不关闭
- **daemon 崩溃即时反映**：wrapper 退出区分主动停止与崩溃并即时推送状态，崩溃不再等下次心跳（~30s）才反映到前端

---

## 0.2.0（2026-06-17）

运行时集成补全 + 嵌入前端可用性修复 + i18n 完整化版本。新增实例状态实时推送与模板默认工作目录；修复了单二进制部署下前端无法加载的重定向死循环、前端构建失败与 i18n 文案缺漏。

### Added
- **实例状态实时推送**（FR-018）：StreamInstanceEvents gRPC 流经 Control Plane SSE 代理推送到前端，替代轮询
- **模板默认工作目录**（FR-014）：模板新增 `defaultWorkDir` 字段，从模板创建实例时自动填充工作目录与启动命令
- **InstanceDetailPage 全面 i18n**：实例详情页全部文案接入 i18next
- **Users/Groups/Templates 页面 i18n 化**（FR-016）：三个页面此前完全硬编码中文，接入 i18next 实现中英双语

### Fixed
- **嵌入前端重定向死循环**：单二进制部署时 `c.FileFromFS("index.html")` 触发 `http.FileServer` 的 `/index.html → ./` 规范化 301 跳转，与根路径形成死循环（ERR_TOO_MANY_REDIRECTS），UI 完全无法加载；改为预读 index.html 并以 `c.Data` 直接返回
- **前端 TypeScript 构建失败**：修复 `events.ts`/`AlertsPage`/`BackupsPage`/`UsersPage` 共 8 处类型错误，恢复 `npm run build`
- **i18n 文案缺失**：补齐 `groups` 与 `backups` 命名空间在 en/zh 的缺失键，消除英文模式下的中文泄漏
- **E2E 测试孤儿进程**（FR-028）：`go run` 派生的二进制子进程未随测试结束回收，导致 `go test` 报 `Test I/O incomplete` 退出非零；改用进程树终止（Windows `taskkill /T`）+ `WaitDelay`

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
