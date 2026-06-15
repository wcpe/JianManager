# ADR-005: 前端 go:embed 嵌入 Control Plane

- **日期**: 2025-06-16
- **状态**: accepted
- **上下文**: 前端是 React SPA，需要一种方式和 Control Plane 一起部署。传统方式是 nginx 反代，但这增加了运维复杂度。
- **决策**: 使用 Go 的 `go:embed` 将前端构建产物嵌入 Control Plane 二进制。开发模式下 Gin 反代到 Vite dev server。
- **理由**:
  - 单二进制部署：`scp` 一个文件即可，不需要 nginx
  - 开发体验不受影响：Vite HMR 通过反代正常工作
  - 生产环境零配置：SPA fallback 由 Gin 处理
- **后果**:
  - 每次前端变更需要重新编译 Control Plane
  - 二进制体积增加（通常增加 5-20MB）
- **替代方案**:
  - nginx 反代 + 独立前端部署 — 灵活但增加运维负担
  - Worker Node 也内嵌前端 — 不需要，Worker 不直接服务用户
