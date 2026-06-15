# ADR-006: Bot Worker 通过 Node.js 子进程实现

- **日期**: 2025-06-16
- **状态**: accepted
- **上下文**: Mineflayer 是 Node.js 库，无法在 Go 中直接使用。需要一种方式让 Go 调度 Node.js 代码。
- **决策**: Go Worker Node 通过 `exec.Command` spawn Node.js 子进程（bot-worker），通过 stdin/stdout JSON 行协议通信。
- **理由**:
  - Mineflayer 生态成熟（行为引擎、寻路、插件），不值得用 Go 重写
  - stdin/stdout IPC 是最简单的进程间通信方式
  - Go 管理 Node.js 子进程生命周期，和管理游戏服子进程模式一致
  - 50 bots/worker 粒度控制内存，单个 worker 崩溃不影响其他
- **后果**:
  - 部署时需要 Node.js 运行时（仅 Bot 功能需要）
  - 需要维护两个语言的代码库
- **替代方案**:
  - Go 重写 Mineflayer（go-mc 等库）— 工作量巨大且生态不成熟
  - gRPC 调度独立 Bot 微服务 — 过度工程化
