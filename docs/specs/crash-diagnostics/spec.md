# 功能规格：进程级崩溃诊断链路（crash-diagnostics）

> 状态：开发中　·　关联 PRD：FR-313　·　分支：feature/fr-313-crash-diagnostics

## 1. 背景与目标

实例进程秒退（jar 缺失、端口占用、JVM 参数错误等）时全链路零痕迹：proto 状态上报不带原因、Worker 不留崩溃日志、终端只有实时流（浏览器不在场即丢失）。FR-312 已补 statusReason 横幅（启动失败一句话）、FR-314 已补启动预检（拦配置错误），但「进程为什么死」的现场——退出码、信号、最后的输出——没有任何留存。本 FR 补齐崩溃现场留存与回看，P1。

## 2. 需求（要什么）

- Worker 为每个运行实例维护输出环形缓冲（stdout/stderr 合流，最后 N 行，N=200、单实例上限 64KB）
- 进程退出时捕获：退出码、信号（Unix；Windows 留空）、本次运行时长
- 非正常退出（退出码 ≠ 0，或 RUNNING/STARTING 态意外退出）生成崩溃快照：时间戳 + 退出码 + 信号 + 时长 + 尾部输出
- 快照经 gRPC 上报 CP 持久化；每实例保留最近 K 次（K=5），超出按时间滚动删除
- 前端实例控制台新增「崩溃诊断」区：快照列表（时间/退出码/信号/时长）+ 尾部输出展开（等宽字体）+ 空态
- 范围内：daemon / direct / docker 启动方式（docker 记容器退出码）；i18n 中英 + 双主题
- 不做（范围外）：崩溃自动重启策略、日志全量持久化（FR-049 日志中心域）、崩溃根因智能分析

## 3. 设计（怎么做）

- **Worker 侧**：复用既有 `internal/worker/daemon/buffer.go` 的 RingBuffer——终端 WS server（`internal/worker/ws/server.go`）已维护 per-instance 缓冲，快照尾部输出直接从该缓冲截取（不足 N 行取全部）；在进程管理器状态转 CRASHED / 意外退出路径组装快照并异步上报，上报失败（网络/老 CP `Unimplemented`）记日志丢弃，不阻塞状态机
- **proto**：CP 侧 gRPC 服务新增 unary `ReportCrashSnapshot(ReportCrashSnapshotRequest) returns (Empty)`（Worker→CP 方向，与注册/心跳同信道——隧道/直拨双模式天然可用，不新增连接路径，符合架构不变量）
- **CP 侧**：新表 `instance_crash_snapshots`（`instance_id` 索引 / `occurred_at` / `exit_code` / `signal` / `duration_ms` / `tail_output` TEXT），AutoMigrate 注册；插入后按实例修剪只留 K 条；实例删除时级联清快照
- **REST API**：`GET /api/instances/:id/crash-snapshots`（权限 = 实例读权限节点，倒序返回）
- **前端**：实例控制台页（InstanceConsolePage）加「崩溃诊断」卡片，TanStack Query 拉取，与 FR-312 失败横幅互补（横幅一句话 → 诊断卡看现场）
- 无新 ADR：无跨进程新通道、无新协议形态，沿用既有 gRPC 回调信道与 GORM 建模模式

## 4. 任务拆分

- [ ] proto：`ReportCrashSnapshot` 消息 + RPC，重新生成
- [ ] Worker：退出码/信号/时长捕获 + 尾部输出截取 + 组装上报（含 Unimplemented 兜底）；单测（快照组装、非正常退出判定、截取边界）
- [ ] CP：model + 迁移 + gRPC handler + 修剪 + 级联删 + REST API；单测（修剪只留 K、权限、倒序）
- [ ] 前端：崩溃诊断卡 + i18n（中/英）+ 双主题适配；vitest（渲染/空态/展开）
- [ ] 文档同步：ARCHITECTURE（表 + RPC）、API.md、PRD 状态、CHANGELOG 段尾

## 5. 验收标准

- 单测全绿：Worker 快照组装/截取边界、CP 修剪与级联删、API 权限与排序
- 前端 vitest 全绿：列表渲染、尾部输出展开、空态
- **真机（需用户确认）**：① 秒退场景（改坏 jar 路径启动）控制台出现快照，含非零退出码与最后输出；② RUNNING 实例外部 kill，快照出现且时长正确；③ 连续崩 6 次只留最近 5 条；④ 老 Worker（无此功能）对新 CP 不炸、新 Worker 对老 CP 不炸
- 横切：中英文完整、暗/亮主题正常、关键路径真机过

## 6. 风险 / 待定

- N=200 行 / 64KB、K=5 为写死默认值，不做配置项（YAGNI，需要再提 FR）
- Windows 无信号语义，signal 留空字符串；退出码取 ExitCode 原值
- wrapper（daemon）模式输出经守护进程中转，尾部截取以 Worker 侧终端缓冲为准；若实现中发现 direct 模式无既有缓冲，则在进程管理器 spawn 时挂同款 RingBuffer（实现细节，不改设计面）
