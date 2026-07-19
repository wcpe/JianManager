# 贡献指南

> 本项目采用 SDD（规格驱动开发）治理：代码与文档在**同一次变更**里一起改，规则固化在 [`.claude/rules/`](../.claude/rules/)（AI 会话自动加载，人类贡献者同样适用）。本文是入口与导航，**各主题的权威定义在被引用的文档里，此处不重复**。

## 1. 开发环境

- Go **1.26.2** · Node.js **22**（Bot Worker 与受管节点运行时最低 `22.13.0`）· pnpm（经 `corepack enable` 激活）· [go-task](https://taskfile.dev)
- 发布 workflow 固定使用 Go 1.26.2 / Node.js 22 / JDK21；本地发版构建除上述工具外还需满足探针与客户端更新器的 Java 构建条件。

```bash
go install github.com/go-task/task/v3/cmd/task@latest
task            # 列出全部任务
task dev:cp     # 起 Control Plane（--dev 反代前端 dev server）
task dev:web    # 前端 Vite dev server
task dev:mock   # 前端 mock 模式（MSW 假后端，无需真后端）
task test       # Go + 前端全部测试
task lint       # go vet + tsc + eslint
task web:e2e    # Playwright 真浏览器整站 E2E
task dist       # 前端 + Bot Worker + 全部内嵌资产 + 四个发布二进制
```

## 2. 分支与流程

- **`dev`**：日常开发分支；**`master`**：发布分支（PR 目标）。功能 / 修复走 `feature/*`、`fix/*` 短生命周期分支。
- PR 必须通过 CI 双门禁：`web-quality` 跑 lint + vitest + 构建 + E2E；`bot-quality` 跑 Bot Worker 生产依赖审计 + 类型检查 + lint + 构建。
- 发布 workflow 另有完整门禁：metadata 版本/ref/tag 校验 → 全部内嵌资产（含 Bot Worker）→ Go 与前端测试 → 四产物构建 → Linux/Windows 原生 `--version` smoke → Release。任一步失败都不得发布。
- 发版：先按 §6 把源码切为裸 `X.Y.Z`，在同一提交打 `vX.Y.Z` tag；Git tag / Release 保留 `v`，二进制版本不带 `v`。紧急修复从发布 tag 切 `hotfix/*` 后回流。
- GitHub-hosted runner 与实际 Release 创建只有 push 后才能验证；当前工作区按用户选择暂不 push，因此远端 Actions 仍待验。

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

唯一真源是 [`internal/version/version.go`](../internal/version/version.go)（ADR-065 / [ADR-074](adr/074-release-version-provenance-and-smoke.md)）。当前版本保持 **`0.18.0-dev`**；开发态恒为下一目标版本 `X.Y.Z-dev`，普通贡献者不要自行改版本号。

发布 metadata 由 `scripts/release-metadata.mjs` 统一解析，规则如下：

- **开发构建**：源码 `X.Y.Z-dev`，二进制 / Bot 归档 / 内嵌 Worker manifest 注入 `X.Y.Z-dev+g<7位sha>`；SHA 只存在于构建元数据，不写回源码。
- **正式发布**：先由发版流程把源码改为裸 `X.Y.Z`，在同一提交创建 tag `vX.Y.Z`；Git tag / GitHub Release 使用 `vX.Y.Z`，二进制内部使用裸 `X.Y.Z`。
- **强校验**：正式 tag 与源码裸版本不一致直接失败；普通分支出现无同 SHA 正式 tag 的裸版本也直接失败。普通分支位于已经打 tag 的裸版本提交时不重复发布 `latest`。
- **单点消费**：发布 workflow 的 Bot Worker 内嵌、CP 内嵌 Worker、四个最终二进制、smoke 与 Release 全部使用 metadata job 的同一份输出，禁止各 job 自行拼版本。

针对性本地检查：

```bash
node --test scripts/release-metadata.test.mjs
```

完整版本生命周期仍以 [`.claude/rules/versioning.md`](../.claude/rules/versioning.md) 为准。

## 7. 文档地图

| 想了解 | 看 |
|---|---|
| 需求全景与状态 | [PRD.md](PRD.md) |
| 系统当前真貌 | [ARCHITECTURE.md](ARCHITECTURE.md) |
| 接口契约 | [API.md](API.md) |
| 决策为什么这么定 | [docs/adr/](adr/README.md) |
| 编码 / 命名 / 配置规范 | [CONVENTIONS.md](CONVENTIONS.md) |
| 部署与运维 | [DEPLOY.md](DEPLOY.md) |
