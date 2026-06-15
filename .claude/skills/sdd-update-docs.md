---
name: sdd-update-docs
description: 批量检查并更新所有文档（同步代码变更到文档）
---

# 文档批量更新

## 触发

用户说 `/sdd-update-docs` 或「更新文档」或「文档过期了」

## 流程

### Phase 1: 扫描差异

1. 对比代码和文档，识别过期项：

   **API.md vs 代码：**
   - 扫描 `internal/controlplane/router/` 中的 endpoint
   - 对比 `docs/API.md` 中的定义
   - 找出：代码有但文档没有的 / 文档有但代码没有的 / 字段不一致的

   **ARCHITECTURE.md vs 代码：**
   - 检查目录结构是否和文档描述一致
   - 检查数据库模型是否和 GORM model 一致
   - 检查 gRPC proto 是否和文档描述一致

   **PRD.md vs 实际状态：**
   - 检查标记为 `🔨 in-progress` 的 FR 是否真的在开发中
   - 检查标记为 `📋 todo` 的 FR 是否已经实现（代码已存在但状态未更新）

2. 输出差异报告：
   ```
   ## 文档差异报告

   ### docs/API.md
   - ⚠️ POST /api/v1/bots/:id/command 存在于代码但未记录
   - ⚠️ GET /api/v1/nodes/:id/metrics 响应体和代码不一致

   ### docs/ARCHITECTURE.md
   - ⚠️ 代码新增了 internal/controlplane/cache/ 目录，文档未记录

   ### docs/PRD.md
   - ⚠️ FR-005 状态为 todo 但代码已实现
   ```

### Phase 2: 更新

3. 逐项修复差异：
   - API.md：原地更新到当前真貌
   - ARCHITECTURE.md：原地更新到当前真貌
   - PRD.md：更新 FR 状态
   - ADR：如有新增架构决策，追加 ADR

### Phase 3: 验证

4. 按 `.claude/rules/gate-merge.md` checklist 逐项核对，确认文档同步完成
5. 输出变更摘要

## 约束

- 严格遵循文档演进规则（doc-evolution.md）
- API.md 和 ARCHITECTURE.md 是「原地更新」，不得保留历史版本
- PRD.md 是「增量 + 状态流转」，新 FR 追加，已有 FR 只改状态
- ADR 是「只追加 + 取代」，不得删除或修改已有 ADR
