# FR-341 — daemon 实例在 Worker 重启后存活并自动重连（ADR-003 闭环）

> 状态：🔨 开发中 ｜ 优先级：P0 ｜ 关联：ADR-003（守护进程 Wrapper）、FR-310（删运行中实例强杀进程树）、FR-325（孤儿 wrapper 兜底清理）

## 1. 背景与问题

ADR-003 的核心承诺是：游戏服由 **daemon wrapper 子进程**托管，**Worker 重启不牵连游戏服**——Worker 恢复后经 wrapper 的 Unix Socket 重新接管（`RecoverDaemonInstances` 的 takeover reconnect 分支）。

代码里生存路径已齐备（wrapper 的 Accept 循环支持重连；spawn 时 `setsid` 脱离进程组；`StopAll` 对 daemon 实例「只断开连接、不杀游戏服」），但真机 `systemctl restart jianmanager-worker` 后 wrapper 与游戏服**一起死**，Worker 恢复后命中「daemon wrapper 已不存活，清理残留」分支而非「接管重连」。经真机复现定位到三个叠加杀手：

| # | 杀手 | 机理 | 影响 |
|---|---|---|---|
| K1 | **stdout/stderr 断管 SIGPIPE**（主因） | wrapper 的 `cmd.Stdout/Stderr` = `newSlogWriter`（非 `*os.File`）→ Go `exec` 建 OS 管道、读端在 Worker 内。Worker 死→读端关→wrapper 下一次日志写 fd 1/2→Go 运行时默认 SIGPIPE **终止** wrapper | wrapper 崩，游戏服失去父进程被拖死 / 变孤儿 |
| K2 | **systemd cgroup 连坐** | worker unit 无 `KillMode`，默认 `control-group`——重启时 systemd 对整个 cgroup 发 SIGKILL，`setsid` 逃得出进程组但逃不出 cgroup | wrapper 被 systemd 直接 SIGKILL |
| K3 | **优雅停机无界挂起** | SIGTERM 处理 `grpcServer.GracefulStop()` 等所有活跃 RPC 收敛，但终端会话 / 反向隧道 / 日志流是长连接永不自然结束 → 挂到 `TimeoutStopSec`(默认 90s) 才被 SIGKILL；`manager.StopAll()`（daemon 优雅断开）在其后，迟迟不执行 | 重启慢 90s；StopAll 的干净断开被拖延/跳过 |

三者叠加：即便单独修 K2（`KillMode=process` 让 java 逃过 cgroup），wrapper 仍因 K1 的 SIGPIPE 而死、连累 java 变孤儿（真机已复现此中间态）。必须三管齐下。

## 2. 目标

Worker 因升级 / 崩溃 / 手动 `systemctl restart` 重启时：

1. **游戏服进程存活**：daemon 模式实例的 java 进程 PID 在 Worker 重启前后不变。
2. **自动接管重连**：Worker 恢复后经 wrapper socket 重连，实例状态回 `RUNNING`，终端 / 日志 / 停止 / 发送命令恢复可用（命中 takeover reconnect，而非「清理残留」）。
3. **优雅停机有界**：Worker 收到 SIGTERM 后 ≤5s 完成 gRPC 停机（不再 90s 挂起）。

## 3. 方案（三处最小改动）

### 3.1 修 K1 — wrapper 忽略 SIGPIPE

wrapper 进程（`worker daemon` 子命令）启动即 `signal.Ignore(syscall.SIGPIPE)`（Unix-only，Windows 无 SIGPIPE 且断管返错不崩）。此后写已断的 fd 1/2 返回 `EPIPE`（slog 丢弃该行）而**不终止进程**。Worker 存活期间 wrapper 日志照旧汇入 Worker slog；Worker 死后 wrapper 日志静默丢弃（可接受：wrapper 仅 Debug 级零星日志，游戏服日志走独立重连的 socket 通道，不受影响）。

- 新增 build-tag 文件 `internal/worker/daemon/sigpipe_unix.go` / `sigpipe_other.go`，导出 `IgnoreBrokenPipe()`。
- 在 `daemon.Run(cfg)` 入口调用。

### 3.2 修 K2 — worker systemd 单元 `KillMode=process`

`scripts/install-worker.sh` 的 `[Service]` 段加 `KillMode=process`：systemd 重启只对 unit 主进程（Worker）发信号，不波及 cgroup 内经 `setsid` 脱离的 wrapper。同步内嵌副本 `internal/controlplane/embed/install-scripts/install-worker.sh`（字节一致，受 `install_scripts_test.go` 守护），补守护测试断言 `KillMode=process` 存在。

### 3.3 修 K3 — 优雅停机设上限

`apps/worker/main.go` 的 SIGTERM 处理：`grpcServer.GracefulStop()` 放独立 goroutine，`select` 等其完成或 5s 超时；超时则 `grpcServer.Stop()` 强制关活跃连接。确保 `manager.StopAll()`（daemon 优雅断开 + direct/docker 真停）及时执行、Worker 迅速退出。

### 3.4 不回归约束

- **FR-310**：删除**运行中** daemon 实例仍走 `ReapDaemonForDelete` 强杀 wrapper+java 两棵进程树再清目录（删除 = 用户显式要求终止，与「重启存活」正交）。
- **StopAll 语义**：daemon 实例走「`strategy.Close()` 断开、不杀游戏服」；direct/docker 实例仍真停。本次不改。

## 4. 验收标准（真机，FR-277 主机 node-2）

- [ ] AC1：provision+start 一个 daemon 实例到 `RUNNING`，记录 java PID → `systemctl restart jianmanager-worker` → java PID **不变**（进程存活）。
- [ ] AC2：重启后 Worker 日志出现「已连接 wrapper socket」（takeover reconnect），实例状态经 CP 查询回 `RUNNING`；终端可交互、可停止。
- [ ] AC3：重启耗时 ≤10s（优雅停机不再 90s 挂起）。
- [ ] AC4：删除该运行中实例 → 无孤儿 java、端口释放、工作目录移除、无残留 pid/sock（FR-310 不回归）。
- [ ] AC5：`go test ./...` 全绿（含 `install_scripts_test.go` 的 KillMode 守护 + 现有 daemon/recover 测试）。

## 5. 影响面

- `internal/worker/daemon/`（新增 SIGPIPE 忽略 + `Run` 调用）
- `apps/worker/main.go`（优雅停机上限）
- `scripts/install-worker.sh` + `internal/controlplane/embed/install-scripts/install-worker.sh` + `install_scripts_test.go`
- 文档：`docs/ARCHITECTURE.md`（进程生存与重启接管一节）、`docs/PRD.md`（FR-341 登记）
