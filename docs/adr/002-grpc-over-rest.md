# ADR-002: gRPC 作为节点间通信协议

- **日期**: 2025-06-16
- **状态**: accepted
- **上下文**: Control Plane 需要和 20-100 个 Worker Node 通信，包括实例操作、文件传输、Bot 管理、指标采集等。需要高性能、类型安全、双向流支持。
- **决策**: 使用 gRPC + Protobuf 作为 Control Plane ↔ Worker Node 的通信协议。
- **理由**:
  - Go 原生支持，`protoc` 代码生成保证类型安全
  - 支持双向 stream（心跳、事件流、文件传输）
  - HTTP/2 多路复用，单连接承载所有 RPC
  - protobuf 二进制编码，比 JSON 小 3-10 倍
- **后果**:
  - 需要维护 `.proto` 文件和代码生成脚本
  - 调试不如 HTTP REST 直观（需 grpcurl 或 BloomRPC）
- **替代方案**:
  - REST + WebSocket（更简单但无双向 stream、无类型安全）— 参见 MCSManager 的 Socket.IO 方案
  - NATS/Redis PubSub（更松耦合但增加外部依赖，不适合此规模）
