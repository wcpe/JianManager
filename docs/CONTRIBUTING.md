# 贡献指南

> 本项目采用 SDD（规格驱动开发）治理：代码与文档在**同一次变更**里一起改，规则固化在 [`.claude/rules/`](../.claude/rules/)（AI 会话自动加载，人类贡献者同样适用）。本文是入口与导航，**各主题的权威定义在被引用的文档里，此处不重复**。

## 1. 开发环境

- Go 1.22+ · Node.js 20+ · pnpm（经 `corepack enable` 激活）· [go-task](https://taskfile.dev)

```bash
go install github.com/go-task/task/v3/cmd/task@latest
task            # 列出全部任务
task dev:cp     # 起 Control Plane（--dev 反代前端 dev server）
task dev:web    # 前端 Vite dev server
task dev:mock   # 前端 mock 模式（MSW 假后端，无需真后端）
task test       # Go + 前端全部测试
task lint       # go vet + tsc + eslint
task web:e2e    # Playwright 真浏览器整站 E2E
```

## 2. 分支与流程

- **`dev`**：日常开发分支；**`master`**：发布分支（PR 目标）。功能 / 修复走 `feature/*`、`fix/*` 短生命周期分支。
- PR 必须通过 CI 双门禁（`web-quality`：lint + vitest + 构建 + E2E；`bot-quality`：bot-worker 类型检查 + eslint）。
- 发版：`master` 打 `vX.Y.Z` tag 触发发布管线出多平台产物与 GitHub Release；紧急修复从发布 tag 切 `hotfix/*` 后回流。

## 3. 提交规范

中文 Conventional Commits：`<type>(<scope>): <中文描述>`，type/scope 枚举、正文格式、最小提交粒度（独立可编译、单一职责）见 [`.claude/rules/git-commit.md`](../.claude/rules/git-commit.md)。**禁止任何 AI 签名 / 尾注**。

## 4. 变更工作流（SDD）

| 变更类型 | 要做的事 |
|---|---|
| 新功能 | ① [PRD](PRD.md) §4 登记一行 FR（状态 `📋 计划`）→ ② 命中 spec-checklist 则先写 [`docs/specs/<feature>/spec.md`](specs/README.md) → ③ 实现 + 测试 → ④ 文档同步 → ⑤ CHANGELOG 未发布段 +1 |
| 修 bug | 先写复现测试（红）→ 最小修复（绿）→ CHANGELOG +1 |
| 架构决策 | **先写 ADR 再写代码**（[docs/adr/](adr/README.md)；推翻旧决策 = 新 ADR 取代，不删旧的） |
| 重构 | 行为不变，前后测试同样全绿 |

**红线**：任何变更不得违反 [`architecture-invariants.md`](../.claude/rules/architecture-invariants.md)（三进程模型 / 通信协议 / 数据所有权 / 依赖方向）；文档同步要求见 [`doc-sync.md`](../.claude/rules/doc-sync.md)；各文档的演进方式（增量 / 原地 / 追加）见 [`doc-evolution.md`](../.claude/rules/doc-evolution.md)。

## 5. 测试与质量

- 新增代码测试覆盖率 ≥60%，核心模块（进程管理 / 状态机 / 协议）≥80%，详见 [`testing-and-quality.md`](../.claude/rules/testing-and-quality.md)。
- 静态检查零容忍：`go vet` / `tsc --noEmit` / `eslint` 全绿（[`static-analysis.md`](../.claude/rules/static-analysis.md)）。
- 只做当前变更范围内的事，顺手发现的问题登记新 FR 而非夹带（[`scope-discipline.md`](../.claude/rules/scope-discipline.md)）。

## 6. 版本号

唯一真源是 [`internal/version/version.go`](../internal/version/version.go)（ADR-065）：开发态恒为 `X.Y.Z-dev`（下一目标版本），仅发版流程改动它——**贡献者不要动版本号**。

## 7. 文档地图

| 想了解 | 看 |
|---|---|
| 需求全景与状态 | [PRD.md](PRD.md) |
| 系统当前真貌 | [ARCHITECTURE.md](ARCHITECTURE.md) |
| 接口契约 | [API.md](API.md) |
| 决策为什么这么定 | [docs/adr/](adr/README.md) |
| 编码 / 命名 / 配置规范 | [CONVENTIONS.md](CONVENTIONS.md) |
| 部署与运维 | [DEPLOY.md](DEPLOY.md) |
