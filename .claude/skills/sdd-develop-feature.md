---
name: sdd-develop-feature
description: 完整的 Feature 开发工作流（文档 → 脚手架 → 编码 → 审查 → 提交）
---

# Feature 开发工作流

## 触发

用户说 `/sdd-develop-feature FR-XXX` 或「开始开发 XXX 功能」

## 完整流程

### Phase 1: 文档

1. **Gate 1 检查**：按 `.claude/rules/gate-prd.md` checklist 逐项核对 PRD
2. **生成文档骨架**：运行 `/sdd-new-feature FR-XXX <feature-name>`
   - 创建 `docs/specs/<feature>/api.md` 骨架
   - 创建 `docs/specs/<feature>/impl.md` 骨架
   - 更新 PRD 中 FR 状态 → `🔨 in-progress`
3. **生成 API Spec**：运行 `/sdd-gen-api <feature-name>`
   - 填充完整 endpoint 定义
   - 同步更新 `docs/API.md`
4. **Gate 3 检查**：按 `.claude/rules/gate-api.md` checklist 逐项核对 API Spec

### Phase 2: 脚手架

5. **后端脚手架**：运行 `/sdd-scaffold-module <module-name>`
   - 生成 GORM model、Gin router、Service 骨架
6. **前端脚手架**：运行 `/sdd-scaffold-frontend <feature-name>`
   - 生成 API client、页面组件、路由注册

### Phase 3: 编码

7. **实现后端**：
   - service 层业务逻辑
   - gRPC handler（如需要 Worker 侧）
   - 数据库 migration
8. **实现前端**：
   - 页面交互逻辑
   - 表单校验、错误处理
   - 实时更新（WS 事件驱动缓存失效）
9. **测试**：
   - 单元测试覆盖核心逻辑
   - 手动验证主要流程

### Phase 4: 审查

10. **Spec 一致性审查**：运行 `/sdd-review`
    - 检查代码和 API Spec 一致性
    - 检查和 ARCHITECTURE.md 一致性
11. **修复不一致项**

### Phase 5: 收尾

12. **文档同步**：
    - 更新 `docs/ARCHITECTURE.md`（如有架构变更）
    - 更新 `docs/API.md`（如有 API 变更）
    - 更新 `docs/PRD.md` 中 FR 状态 → `✅ done`
13. **Gate 4 检查**：按 `.claude/rules/gate-merge.md` checklist 逐项核对
14. **提交代码**：遵循 `git-commit.md` 规范

## 约束

- 严格按 Phase 顺序执行，不得跳步
- Phase 1 的 Gate 未通过不得进入 Phase 2
- Phase 4 的审查未通过不得进入 Phase 5
- 每个 Phase 可以产出多个 commit
