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
