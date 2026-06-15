# Rules 索引

> `.claude/rules/` 目录下的所有规则文件，在 Claude Code 会话中自动加载为行为约束。

## 阶段门禁

| 文件 | 用途 |
|---|---|
| [gate-prd.md](gate-prd.md) | PRD → SDD 阶段门禁 |
| [gate-sdd.md](gate-sdd.md) | SDD → Feature 开发门禁 |
| [gate-api.md](gate-api.md) | API Spec → 编码门禁 |
| [gate-merge.md](gate-merge.md) | 合并/发版门禁 |

## 持续规则

| 文件 | 用途 |
|---|---|
| [architecture-invariants.md](architecture-invariants.md) | 架构不变量，任何代码变更都不得违反 |
| [comments.md](comments.md) | 注释规范 |
| [config-files.md](config-files.md) | 配置文件格式和命名规范 |
| [decision-alignment.md](decision-alignment.md) | 代码必须和 ADR 决策一致 |
| [doc-sync.md](doc-sync.md) | 代码变更时文档必须同步更新 |
| [doc-evolution.md](doc-evolution.md) | 文档演进方式（增量/原地/追加） |
| [git-commit.md](git-commit.md) | Git commit message 规范 |
| [scope-discipline.md](scope-discipline.md) | 范围纪律，不做 spec 之外的事 |
| [static-analysis.md](static-analysis.md) | 静态分析和 lint 规范 |
| [testing-and-quality.md](testing-and-quality.md) | 测试和质量要求 |
