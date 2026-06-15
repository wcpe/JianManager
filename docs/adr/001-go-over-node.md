# ADR-001: Go 作为后端语言

- **日期**: 2025-06-16
- **状态**: accepted
- **上下文**: 需要一个适合进程管理、单二进制部署、低内存占用的后端语言。核心场景是管理数百个游戏服进程和守护进程。
- **决策**: 使用 Go 1.22+ 作为 Control Plane 和 Worker Node 的实现语言。
- **理由**:
  - goroutine 天然适合管理数千并发进程/连接
  - 单二进制部署，`scp` 即跑，无运行时依赖
  - 静态编译 20-50MB，Node.js 运行时 100MB+
  - `encoding/binary` + Unix Socket 原生支持二进制帧协议
  - `GOOS/GOARCH` 一行交叉编译
- **后果**:
  - 团队需要 Go 经验
  - 前后端类型共享需要 codegen 或手动同步
- **替代方案**:
  - Node.js/TypeScript（生态好但单线程、内存高、部署依赖运行时）— 参见 JianAgent
  - Rust（性能极致但开发效率低、生态不够成熟）
