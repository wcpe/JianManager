# 文档同步

> 代码变更时，相关文档必须同步更新。不允许「代码改了文档没改」。

## 触发条件与对应文档

| 代码变更类型 | 必须同步的文档 |
|---|---|
| 新增/修改/删除 API endpoint | `docs/API.md` 对应章节 |
| 新增/修改数据库表或字段 | `docs/ARCHITECTURE.md` 数据库模型章节 |
| 新增/修改 gRPC service 或 message | `docs/ARCHITECTURE.md` 通信协议章节 + `proto/*.proto` |
| 新增/修改配置项 | `docs/CONVENTIONS.md` 或对应配置文档 |
| 新增/修改进程模型或通信路径 | `docs/ARCHITECTURE.md` 架构图 + ADR |
| 新增/修改页面结构或导航 | `docs/ARCHITECTURE.md` 前端架构章节 |
| Feature 实现完成 | `docs/PRD.md` 中对应 FR 状态 → `✅ done` |
| 新增架构决策 | `docs/adr/NNN-标题.md` |

## 同步时机

- **提交代码前**：检查本次变更涉及哪些文档，一并更新
- **合并前**（Gate 4）：按 `gate-merge.md` checklist 检查文档是否同步

## 不需要同步的情况

- 纯重构（行为不变，只是代码结构变化）— 但如果重构影响了架构分层，需要更新 ARCHITECTURE.md
- Bug 修复（不影响 API 或数据模型）
- 测试代码变更
