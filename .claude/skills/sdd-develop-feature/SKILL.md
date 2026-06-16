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

### Phase 5: 验收（用户确认）

12. **逐条核对验收标准**，向用户输出：
    ```
    FR-005 验收报告：

    ✅ 创建实例：选择节点、类型、启动方式、启动命令
       → POST /api/v1/instances 返回 201，数据库有记录
    ✅ 启动/停止/重启/强制终止操作
       → POST /instances/:id/start 状态变为 RUNNING
    ❌ 状态机：STOPPED → STARTING → RUNNING → STOPPING → STOPPED / CRASHED
       → CRASHED 后未自动重启，缺少指数退避逻辑
    ✅ 崩溃自动重启（指数退避）
       → 实测 kill 进程后 5s 内自动重启
    ✅ 实例分配给用户组
       → 组成员只能看到分配的实例

    通过率: 4/5
    未通过: 状态机 CRASHED 转换
    ```

13. **等待用户确认**：
    - 用户说「全部通过」「可以了」→ 进入 Phase 6
    - 用户指出哪些没通过 → **回到 Phase 3** 修复未通过项
    - Agent 不得自行标记 FR 为 done

### Phase 6: 收尾

14. 用户确认全部通过后：
    - 更新 `docs/ARCHITECTURE.md`（如有架构变更）
    - 更新 `docs/API.md`（如有 API 变更）
    - **由用户确认后**更新 `docs/PRD.md` 中 FR 状态 → `✅ done`
15. **Gate 4 检查**：按 `.claude/rules/gate-merge.md` checklist 逐项核对
16. **提交代码**：遵循 `git-commit.md` 规范

## 约束

- 严格按 Phase 顺序执行，不得跳步
- Phase 1 的 Gate 未通过不得进入 Phase 2
- Phase 4 的审查未通过不得进入 Phase 5
- **Phase 5 验收未通过不得进入 Phase 6**
- **Agent 不得自行将 FR 标记为 done，必须等用户确认验收通过**
- **验收标准必须逐条核对，不能笼统说「已完成」**
