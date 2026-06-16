# ADR-003: 守护进程 Wrapper 模式

- **日期**: 2025-06-16
- **状态**: accepted
- **上下文**: 游戏服务器需要长时间运行。如果平台（Go 进程）重启、升级、崩溃，不应影响已运行的游戏服。需要进程隔离机制。
- **决策**: 采用 Daemon Wrapper 模式 — Go Worker Node spawn 一个轻量子进程（daemon wrapper），由该子进程作为游戏服 Java 进程的父进程。通过 Unix Socket + 二进制帧协议通信。
- **理由**:
  - 平台进程和游戏服进程完全隔离
  - 平台重启后通过 PID 文件 + Unix Socket 重新连接已有 daemon
  - 二进制帧协议（8 字节头）比 JSON 高效得多
  - 崩溃自动重启（指数退避）在 daemon 层实现
- **后果**:
  - 每个 daemon 实例多一个子进程开销（约 10MB 内存）
  - 需要实现二进制帧协议编解码
  - Windows 上使用 Named Pipe 替代 Unix Socket
- **替代方案**:
  - 直接子进程（platform 死 = 游戏服死）— 最简单但不可接受
  - systemd service 管理（Linux only，跨平台困难）
- **参考**: JianAgent 的 DaemonProcessCommand 实现

- **实现细化（2026-06，17）**: 落地实现时的具体约定，  - wrapper 复用 worker 二进制，通过 `daemon` 子命令模式启动（`jianmanager-worker daemon`），配置经 `JM_DAEMON_WRAPPER_CONFIG` 环境变量（JSON）传递，避免命令行转义问题。
  - 进程组隔离：Linux/macOS 用 `SysProcAttr{Setsid: true}`，Windows 用 `CREATE_NEW_PROCESS_GROUP`（`internal/worker/process/detach_*.go`）。
  - 传输层：`internal/worker/daemon/conn.go` + `conn_unix.go`/`conn_windows.go`` 按 `runtime.GOOS` 分支；Unix Socket（`<pidDir>/<uuid>.sock`）/ Named Pipe（`\\.\pipe\jianmanager-<uuid>`，基于 `gopkg.in/natefinch/npipe.v2`）；Windows 拨号用 `DialTimeout` 避免管道未就绪时无限阻塞。
  - 进程存活探测：`IsPIDAlive` 跨平台实现（unix: signal 0；windows: `OpenProcess`，`pid_alive_*.go`），因 Windows 不支持 signal 0。
  - PID 文件：JSON 结构（`PIDRecord`：wrapper pid、java pid、socket addr、instance uuid），存 `<pidDir>/<uuid>.pid`，兼容旧版裸 PID 数字格式。
  - Java 进程树终止：Windows 上 `cmd.Process.Kill` 仅终止 cmd.exe、其子进程句柄导致 `cmd.Wait` 阻塞，故 wrapper 用 `taskkill /T /F` 递归终止 Java 进程树。
  - Worker 重启恢复：`Manager.RecoverDaemonInstances` 扫描 PID 文件，存活则 `Reconnect` socket，恢复管理，否则清理。
  - 优雅退出：daemon 模式 `Manager.StopAll` 只断开与 wrapper 连接，不杀游戏服（区别于 direct 模式终止进程））。
  - 进程管理策略：`IProcessCommand` 接口（`internal/worker/process/command.go`）按 `ProcessType` 路由 direct/daemon/docker（docker/rcon 返回 `ErrNotImplemented`）。
