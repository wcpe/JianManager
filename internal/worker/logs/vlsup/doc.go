// Package vlsup 实现 VictoriaLogs Worker 运行时 supervisor 基础（FR-475）。
//
// 范围（本轮 foundation）：
//   - 按 namespace（hot|cold|rehydrate）管理 VL 实例：start / stop / health / status；
//   - 命令行构造：-storageDataPath、-retentionPeriod、-memory.allowedBytes（HOT 默认模板 512MiB）、
//     仅绑定 localhost；
//   - 资产校验钩子 VerifyAsset(path, sha256)，在 exec 前强制执行，并在 status 中记录 tag/build id；
//   - Basic-auth HTTP 客户端（127.0.0.1 查询；未授权返回错误；base URL 可注入便于测试）；
//   - 独立失败 sink：supervisor 进程错误不得递归写回 VL 日志管道（守卫标志）；
//   - EnforceCacheBudget 占位：区分进程级 cache 预算与 RSS / Worker 日志总预算。
//
// 不在本包范围：proto / CP embed / apps/worker main 接线；真实发行资产打包分发；
// 完整 Catalog / 生命周期 / 查询联邦。契约语义以 docs/specs/worker-log-platform-contract/spec.md
// 与 docs/specs/worker-victorialogs-runtime/spec.md 为准。
//
// 资产依赖基线：VL v1.52.0（见 .tmp/vl-asset-approval-2026-09-20.md）。
package vlsup
