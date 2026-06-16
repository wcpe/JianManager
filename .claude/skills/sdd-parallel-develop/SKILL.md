---
name: sdd-parallel-develop
description: 并行开发多个 FR — 分析依赖、创建工作树、spawn 并行 Agent、自动合并。当用户说「并行开发」「同时做这几个」「FR-001 FR-004 一起做」时触发。
---

# 并行开发工作流

## 触发

用户说 `/sdd-parallel-develop FR-001 FR-003 FR-004` 或「并行开发这几个功能」或「同时做 FR-XXX 和 FR-YYY」

## 输入格式

```
/sdd-parallel-develop FR-001 FR-003 FR-004
```

或带依赖声明：

```
/sdd-parallel-develop FR-003 FR-004 --depends-on FR-001,FR-002
```

## 完整流程

### Phase 1: 分析与规划

1. **读取 PRD.md**，获取每个指定 FR 的内容（描述、验收标准、优先级）

2. **判断类型**：每个 FR 是 feat 还是 fix？
   - PRD 中状态为 `📋 todo` 的新功能 → `feat`
   - PRD 中状态为 `🔨 in-progress` 但实现有缺陷 → `fix`
   - 不确定时向用户提问确认

3. **分析依赖关系**：
   - 读取 ARCHITECTURE.md 理解模块依赖
   - 读取 API.md 检查 endpoint 依赖
   - 判断哪些 FR 可以并行，哪些必须串行
   - 依赖规则：
     - 共享同一个数据库表的 FR 可并行（不同文件）
     - 一个 FR 的 API 被另一个 FR 调用的 → 有依赖，不能并行
     - 纯前端 FR 和纯后端 FR 可并行

4. **输出执行计划**：
   ```
   并行组 1（无依赖，同时执行）:
     FR-003 用户组 (feat)
     FR-004 节点注册 (feat)

   并行组 2（依赖组 1 完成）:
     FR-005 实例生命周期 (feat)

   预计: N 个 worktree, M 次 merge
   ```
   向用户展示计划，确认后继续。

### Phase 2: 创建 SDD 文档

5. **为每个 FR 生成文档骨架**：
   ```
   对每个 FR:
     - 创建 docs/specs/<feature>/api.md（从 API.md 提取 + 补充）
     - 创建 docs/specs/<feature>/impl.md（任务拆解）
     - 更新 PRD.md 中该 FR 状态 → 🔨 in-progress
   ```

6. **提交文档**：
   ```
   git add docs/ docs/PRD.md
   git commit -m "docs(<scope>): 为 FR-XXX FR-YYY 生成开发文档"
   ```

### Phase 3: 创建 Worktree

7. **为每个 FR 创建独立 worktree 和分支**：
   ```bash
   # 从当前 main 创建
   git worktree add .worktrees/<feature-name> -b feat/<feature-name>
   ```
   命名规则：
   - FR-001 用户认证 → `.worktrees/user-auth` → 分支 `feat/user-auth`
   - FR-004 节点注册 → `.worktrees/node-reg` → 分支 `feat/node-reg`

### Phase 4: Spawn 并行 Agent

8. **为每个 worktree 启动一个 Agent**，所有 Agent 在同一个消息中 spawn：

   每个 Agent 的 prompt 包含：
   ```
   你在 worktree .worktrees/<feature-name> 中开发 <FR 编号> <功能名>。

   ## 工作目录
   .worktrees/<feature-name>

   ## 任务
   实现 <FR 编号>，参照：
   - docs/PRD.md 中的验收标准
   - docs/specs/<feature>/api.md 中的 API 定义
   - docs/ARCHITECTURE.md 中的架构约束

   ## 约束
   - 遵守 .claude/rules/ 下所有规则
   - 每个最小可编译块提交一个 commit
   - commit message 中文，禁止阶段性词语，禁止 AI 签名
   - 不修改其他 worktree 负责的文件
   - 产出文件范围：<列出该 FR 涉及的目录和文件>
   - 确保 go build ./... / npm run build 通过

   ## 完成后
   返回完成状态和 commit 列表。
   ```

9. **等待所有 Agent 完成**。收到每个 Agent 的完成通知后，记录其 commit 信息。

### Phase 5: 合并

10. **按依赖顺序合并**：
    ```
    git checkout main

    # 无依赖的先合（顺序无所谓）
    git merge feat/user-auth
    git merge feat/node-reg

    # 有依赖的后合
    git merge feat/instance
    ```

11. **合并冲突处理**：
    - 尝试自动解决：优先保留 main 的内容
    - 如果自动解决失败，暂停并通知用户：
      ```
      ⚠️ 合并 feat/xxx 时发生冲突，无法自动解决。
      冲突文件: <列出>
      请手动解决后运行: git merge --continue
      解决后告诉我继续。
      ```
    - 如果自动解决成功，提交 merge commit

### Phase 6: 验证与收尾

12. **合并后验证**：
    ```bash
    go build ./...          # 编译通过
    go vet ./...            # 静态检查
    ```

13. **更新文档**：
    - PRD.md 中所有已合并 FR 状态 → ✅ done
    - 更新 ARCHITECTURE.md（如有架构变更）
    - 更新 API.md（如有 API 变更）

14. **清理 worktree**：
    ```bash
    git worktree remove .worktrees/<feature-name>
    git branch -d feat/<feature-name>
    ```

15. **提交收尾**：
    ```
    git commit -m "docs: FR-XXX FR-YYY 完成，更新文档状态"
    ```

16. **输出摘要**：
    ```
    ## 并行开发完成

    ### 已合并
    - ✅ FR-003 用户组 (feat/group, 5 commits)
    - ✅ FR-004 节点注册 (feat/node, 3 commits)

    ### 编译状态
    - go build: ✅ 通过
    - go vet: ✅ 通过

    ### 文档更新
    - PRD.md: FR-003 ✅ done, FR-004 ✅ done
    - API.md: 新增 4 个 endpoint
    ```

## 依赖分析规则

```
可以并行:
  - 不同模块的 feat（user/group/node 各自独立）
  - 前端 + 后端（web/ 和 internal/ 不重叠）
  - 不同 service 层的实现

不能并行:
  - 共享同一个 service 文件的 FR
  - 一个 FR 的输出是另一个 FR 的输入
  - 都需要修改 router.go 注册路由的 FR（需要串行，或拆分 router 文件）
```

## 产出文件隔离规则

每个 Agent 只能修改以下范围的文件：

| FR 模块 | 可修改的目录 |
|---|---|
| auth | `internal/controlplane/{model/user,router/auth,service/auth,middleware/auth}*` |
| user | `internal/controlplane/{router/user,service/user}*` |
| group | `internal/controlplane/{model/group,router/group,service/group}*` |
| node | `internal/controlplane/{model/node,router/node,service/node}`, `internal/worker/{register,heartbeat}*` |
| instance | `internal/controlplane/{model/instance,router/instance,service/instance}`, `internal/worker/process/*` |
| terminal | `internal/controlplane/{router/terminal,service/terminal}`, `internal/worker/{terminal,ws}/*` |
| file | `internal/controlplane/router/file`, `internal/worker/{grpc/handler_file,docker}*` |
| bot | `internal/controlplane/{model/bot,router/bot,service/bot}`, `internal/worker/bot/*`, `bot-worker/*` |

共用文件（go.mod、Makefile、proto/）只在第一个 FR 中修改，后续 FR 不得覆盖。
