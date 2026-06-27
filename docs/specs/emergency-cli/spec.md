# 功能规格：jmctl 紧急控制台 CLI

> 状态：定稿待审（doc-first）　·　关联 PRD：FR-184　·　关联 ADR：041（jmctl 直连 daemon）/ 003 / 008 / 010　·　分支：master

## 1. 背景与目标

当 Control Plane 和 Worker Node 同时崩溃/升级/不可用时，daemon wrapper 仍然托管着运行中的 MC 服务器（ADR-003）。但目前没有任何独立工具能连接 daemon 的 Named Pipe/Unix Socket 进行紧急操作（如发送 `stop` 优雅关服、查看输出）。

运营者在这种场景下只能 `taskkill /F` 强杀 Java 进程，可能导致存档损坏。

**目标**：提供一个独立的轻量 CLI 二进制 `jmctl`，能在 CP/Worker 全部不可用时直接连接 daemon wrapper，实现紧急终端交互和关服。

**属于**：FR-184（节点与运行时重做批次新增），ADR-003 daemon wrapper 的自然延伸；架构决策见 ADR-041。

## 2. 需求（要什么）

### 范围内
- **UUID 前缀补全**：所有接受 `<uuid>` 参数的子命令均支持前缀匹配——用户只需输入 UUID 开头若干字符（如 `abc`），程序在存活实例中查找唯一匹配；匹配到唯一实例则自动补全，匹配到多个则列出候选并报错，无匹配则报错。类似 `docker` / `git` 的短 ID 体验。
- **`jmctl emergency`**：交互式紧急终端
  - 无参数时：扫描 pidDir 下所有 `*.pid` 文件，列出存活的 daemon 实例（UUID、工作目录、Java PID），用户选择后连接
  - `--instance <uuid-prefix>`：直接连接指定实例（支持前缀补全）
  - `--pid-dir <path>`：指定 PID 目录（默认从平台数据根推断）
  - 连接后进入交互模式：
    - 用户输入 → `ChannelStdin` 帧 → daemon → MC stdin
    - daemon 回传 `ChannelStdout/Stderr` 帧 → 解码后打印到终端
    - `Ctrl+C` 首次发送 `ChannelControl` 的 `stop` 命令（优雅关服）
    - 连续两次 `Ctrl+C` 发送 `kill` 强制终止
  - 连接断开（daemon 退出）时自动退出
- **`jmctl list`**：列出所有存活 daemon 实例（非交互，适合脚本/快速检查）
- **`jmctl stop <uuid-prefix>`**：单发停服命令后退出（非交互，适合脚本/批量关服；支持前缀补全）
- **`jmctl kill <uuid-prefix>`**：单发强杀命令后退出（支持前缀补全）

### 不做（范围外）
- 不做 Worker/CP 的管理功能（那是面板的事）
- 不做 daemon 的启动/重启（那是 Worker 的事）
- 不做多实例并发终端（紧急工具保持简单）
- 不做远程连接（仅本机 Named Pipe/Unix Socket）

## 3. 设计（怎么做）

### 3.1 二进制结构

```
cmd/
  jmctl/
    main.go          # 入口：解析子命令分发
    emergency.go     # emergency 子命令：交互式终端
    list.go          # list 子命令：列出存活实例
    stop.go          # stop/kill 子命令：单发命令
```

### 3.2 依赖关系

仅依赖 `internal/worker/daemon` 包：
- `PIDFile` / `PIDRecord` — 发现存活实例
- `Dial` — 拨号 Named Pipe/Unix Socket
- `Frame` / `Encode` / `Decode` — 帧编解码
- `Channel*` / `Ctrl*` 常量

不依赖 Worker 主服务、gRPC、数据库等任何重量级模块。

> **依赖闭包硬约束（落地校验）**：用 `go list` 验证 `internal/worker/daemon` 的传递依赖**不含** gRPC/GORM/CP 包；若超标，把帧协议下沉为更中立的包（如 `internal/daemon` / `pkg/daemonproto`）由 jmctl 与 Worker 共用，确保 jmctl 二进制保持 ~5MB 量级。

### 3.3 PID 目录发现

PID 目录**以 Worker 实际写入路径为准**（ADR-041 §1）。核对源码后定位：Worker 的 `process.Manager` 把
`<uuid>.pid` 写到「服务器工作目录根」`serversDir`（`NewManager` 中 `pidDir := serversDir`），而 `serversDir`
= 数据根下 `var/servers`（`dataroot.Root.ServersDir()`，ADR-010）。数据根经环境变量 `JIANMANAGER_DATA_DIR`
覆盖、缺省 `./data`（`dataroot.EnvVar` / `DefaultDir`）。故落地为 `var/servers` 而非泛指的 `var/pid`。

按优先级：
1. `--pid-dir` 显式指定
2. 环境变量 `JIANMANAGER_DATA_DIR`（缺省 `./data`）指向的数据根下 `var/servers/`（目录存在才采用）
3. 找不到则报错提示用 `--pid-dir`

### 3.4 交互式终端（emergency）

```
┌─ 终端 ─────────────────────────────────┐
│ [jmctl] 已连接实例 abc123              │
│ [jmctl] Java PID: 12345               │
│ [jmctl] 输入命令发送到 MC 控制台       │
│ [jmctl] Ctrl+C = 优雅关服, 连按 = 强杀 │
│                                         │
│ > 15:32:01 [Server] Server started      │ ← daemon stdout 帧
│ > 15:32:05 [Server] Player joined       │
│ list                                    │ ← 用户输入 → stdin 帧
│ > 15:32:10 [Server] There are 1/20...   │
│ ^C                                      │
│ [jmctl] 已发送 stop 命令，等待关服...   │
│ > 15:32:15 [Server] Stopping server     │
│ > 15:32:16 [Server] Saving worlds       │
│ [jmctl] 连接已断开，daemon 已退出       │
└─────────────────────────────────────────┘
```

### 3.5 构建

Makefile/构建脚本增加 `jmctl` target：
```
go build -o jmctl ./cmd/jmctl
```

编译产物体积预估：~5MB（仅 daemon 包 + 标准库）。

## 4. 任务拆分

- [ ] `cmd/jmctl/main.go` — 入口与子命令路由
- [ ] `cmd/jmctl/list.go` — 扫描 PID 目录、列出存活实例
- [ ] `cmd/jmctl/emergency.go` — 交互式终端（拨号 + 帧收发 + 信号处理）
- [ ] `cmd/jmctl/stop.go` — 单发 stop/kill 命令
- [ ] 单元测试：PID 扫描、帧收发循环
- [ ] 文档同步：PRD 状态、ARCHITECTURE（新增 jmctl 二进制）、CHANGELOG

## 5. 验收标准

- [ ] `jmctl list` 能正确列出所有存活 daemon 实例（UUID、工作目录、Java PID、socket 地址）
- [ ] `jmctl emergency` 无参数时列出实例供选择，选择后进入交互终端
- [ ] `jmctl emergency --instance <uuid-prefix>` 直连指定实例（前缀补全）
- [ ] 交互终端能收到 MC 服务器输出（stdout/stderr）
- [ ] 交互终端能发送命令到 MC 服务器 stdin
- [ ] `Ctrl+C` 发送 stop 命令，MC 服务器优雅关服
- [ ] 连续 `Ctrl+C` 发送 kill 命令强制终止
- [ ] daemon 退出后终端自动退出
- [ ] `jmctl stop <uuid-prefix>` 发送 stop 后退出（前缀补全）
- [ ] `jmctl kill <uuid-prefix>` 发送 kill 后退出（前缀补全）
- [ ] UUID 前缀补全：唯一匹配自动连接，多个匹配列出候选并报错，无匹配报错
- [ ] **真机验收（需用户确认）**：在 Worker 停止的状态下，通过 jmctl 成功连接 daemon 并优雅关服

## 6. 风险 / 待定

- PID 目录位置需要和现有数据根逻辑保持一致，可能需要检查 Worker 实际使用的 pidDir 路径
- Windows Named Pipe 权限：如果 Worker 和 jmctl 以不同用户运行，可能需要调整 Pipe ACL（当前场景下通常同用户，暂不处理）

## 7. 安全模型（见 ADR-041 §3）

- **纯本机、无网络面**：只打开本机 Unix Socket / 命名管道，不监听任何端口、不做远程访问。
- **不做额外鉴权**：能在本机读写守护进程 socket 文件即等同宿主级运维权限（足以 kill 该进程）；token/JWT 既无必要、在 CP 宕机态也无处校验。这是有意的「绕栈直连 daemon」应急通道。
- **不破架构不变量**：守护进程 socket 仍只被「本机 Worker / 本机 jmctl」访问；浏览器/网络永不直触 daemon socket。落地时同步 ARCHITECTURE 角色图与 `.claude/rules/architecture-invariants.md`，增列 jmctl 为本机访问方。
- `emergency`/`stop`/`kill` 高影响但仅本机可发起，不额外二次确认（CLI 即显式操作）；单发 `stop` 默认优雅、不自动硬杀，强杀须显式 `kill` 或交互态连按两次 `Ctrl+C`。
