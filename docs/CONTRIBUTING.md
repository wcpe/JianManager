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

### E2E 前置：清掉 `DISPLAY`（SSH / X11 转发环境）

**经 SSH 进入、且 shell 带有 `DISPLAY`（如 `DISPLAY=localhost:11.0`，即 X11 转发）时，必须先取消它再跑 Playwright，否则每个 `click()` 都会超时。**

```bash
env -u DISPLAY pnpm --filter control-plane-web e2e   # 等价 task web:e2e
```

原因：此类 `DISPLAY` 会让 Chromium 的 `requestAnimationFrame` **完全不触发**（实测空白页 1.5s 内 0 次回调；`setTimeout` 正常）。Playwright 的点击前要做「稳定性」检查——要求元素连续两帧几何不变——rAF 停摆使该检查永不满足，于是所有点击都会以 `waiting for element to be visible, enabled and stable` 超时。症状具有强误导性：

- 失败点几乎全部落在 `login()` 的第一步（登录按钮点击），看起来像认证或页面缺陷；
- 元素实际**位置静止、可见、未被禁用、无遮挡、无动画**，`fill()` 也能正常工作——唯独 `click()` 失败；
- 与产品代码无关，`git bisect` / 回滚版本都无效。

判定与自查：

```bash
echo "${DISPLAY:-<未设置>}"     # 有值且形如 localhost:N → 需 unset
```

```js
// 在任意页面执行：返回 0 即命中该陷阱
await page.evaluate(() => new Promise((res) => {
  let n = 0
  requestAnimationFrame(() => { n++ })
  setTimeout(() => res(n), 1000)
}))
```

CI 的 runner 不带 `DISPLAY`，因此**该问题只在本地出现，CI 恒为绿**——本地红而 CI 绿时优先按此自查。

## 2. 分支与流程

本仓采用**单主干 GitHub Flow**：主干唯一为 `master`，始终可发布。

- **禁止直推主干**：任何变更（含单人开发期、含发版提交）都必须经 PR 合入，`master` 不接受直接 push。
- **短分支命名**：`feature/*`（新功能）、`fix/*`（修 bug）、`refactor/*`（重构）、`hotfix/*`（线上紧急修复，从发布 tag 切出）、`docs/*`、`chore/*`；分支从最新 `master` 切出，生命周期尽量短，合入后立即删除。
- **合并要求**：PR 提交前清理 WIP / `fixup!` 提交；一个 commit 只做一件事，不混合 `feat` / `fix` / `refactor`；优先 rebase + fast-forward 合入；禁止 squash 多意图 PR；禁止「整版本一个大提交」；合并后删除源分支。
- **回滚**：用 `git revert` 反向提交，不做历史改写；**严禁 force push 主干**；严禁 `--no-verify`；严禁 amend 已 push 的提交。
- PR 必须通过 CI 质量门禁：`web-quality` 跑 lint + vitest + 构建 + E2E；`bot-quality` 跑 Bot Worker 生产依赖审计 + 类型检查 + lint + 构建；`go-quality` 跑 Go 编译、静态检查与日志联邦回归测试。
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

唯一真源是 [`internal/version/version.go`](../internal/version/version.go)（ADR-065 / [ADR-074](adr/074-release-version-provenance-and-smoke.md)）。当前正式发布提交使用 **`0.18.0`**，对应 tag `v0.18.0`；后续开发态恒为下一目标版本 `X.Y.Z-dev`，普通贡献者不要自行改版本号。

发布 metadata 由 `scripts/release-metadata.mjs` 统一解析，规则如下：

- **开发构建**：源码 `X.Y.Z-dev`，二进制 / Bot 归档 / 内嵌 Worker manifest 注入 `X.Y.Z-dev+g<7位sha>`；SHA 只存在于构建元数据，不写回源码。
- **正式发布**：先由发版流程把源码改为裸 `X.Y.Z`，在同一提交创建 tag `vX.Y.Z`；Git tag / GitHub Release 使用 `vX.Y.Z`，二进制内部使用裸 `X.Y.Z`。
- **发版前置红线**：创建发版 commit 与 tag **必须在基线提交（当前主干 tip）的远程 CI 全绿之后**。CI 未绿（含 flake 导致的红）时禁止改动 `version.go`、禁止归档 CHANGELOG、禁止打 tag；本地测试全绿不能替代 CI 结论。详见 [`.claude/rules/gate-merge.md`](../.claude/rules/gate-merge.md) §发版红线。
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

## 8. 分支保护（新建仓即开启）

- 主干 `master` 受保护，**新建仓库时即开启**，不依赖事后补配。
- **必须经 PR 合并**，不允许直推主干。
- **必须 CI 检查通过才可合并**：至少通过 `web-quality`、`bot-quality`、`go-quality`、`agent-gate`；发布另受 release 门禁约束。
- **管理员同样受约束**，任何角色都不得绕过上述要求直接推主干。
- **禁止 force push、禁止删除主干**。
- **建议要求线性历史**（对应 rebase + fast-forward 合入）。

## 9. 标签（精简 10 个）

GitHub 没有「标签即代码」的原生文件，标签在仓库设置里维护（Settings → Labels），按下面这张表建一次即可：

| 标签 | 用途 |
|---|---|
| `bug` / `enhancement` / `docs` / `refactor` | Issue / PR 模板自动打，标识类型 |
| `hotfix` | 线上紧急，优先处理 |
| `P0` / `P1` / `P2` | 紧急 / 重要 / 一般，新 Issue 由负责人 triage 时打 |
| `待验证` | 已合入主干，等待提单人回测验证 |
| `破坏性变更` | 含迁移成本，发版前必须确认迁移说明 |

- `hotfix` + `P0` + `破坏性变更` 同时出现时，发版前必须确认迁移说明已写入 CHANGELOG 与 PR。
