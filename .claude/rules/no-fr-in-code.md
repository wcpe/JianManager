# 代码禁止出现 FR 编号

> **FR 编号只属于文档**（PRD / spec / ADR / CHANGELOG）。
> 代码、文件名、注释一律不写 FR 编号；要追溯「为什么这样写」，用 ADR 编号。

## 为什么

FR 编号是**需求登记号**，回答「这一批要做什么」。它有两个不适合出现在代码里的性质：

1. **会变**。需求重号要改号（见 [prd-authoring.md](prd-authoring.md) §5），而代码里的号一改就是大面积返工。
   反过来，需求撤销时（见 [branch-only-records.md](branch-only-records.md)）代码里会留下指向不存在编号的死引用。
2. **不解释实现**。`// FR-431: 侧栏按权限裁剪` 相对 `// 侧栏按权限裁剪` 没多任何信息——
   想知道 FR-431 是什么，还得去翻 PRD。而 **ADR 编号回答的是「为什么这么选」**，
   那才是注释该承载的东西。

## 规则

### 禁止

| 位置 | 反例 |
|---|---|
| 注释 | `// FR-486 范围③：复用扫描缓冲区` |
| 文件名 | `proxy_fr035_test.go`、`fr513-host-obs-visual.spec.ts` |
| 标识符 | `handleFR486Case()` |
| 字符串/测试描述 | `it('FR-507 阶段6：…')` |

### 允许

- **ADR 编号**：`// 参见 ADR-003: 守护进程 Wrapper 模式`（[comments.md](comments.md) 要求引用 ADR）
- 文档：PRD / `docs/specs/**` / `docs/adr/**` / `CHANGELOG.md`
- 存量代码：本规则出台前已存在的 FR 编号**不动**（见下方"执行范围"）

### 替代写法

想表达的东西其实只有三类，各有正确落点：

| 你原本想写 | 正确写法 |
|---|---|
| 这条需求的背景 | 写进 PRD/spec，代码里不引用 |
| 为什么这样实现 | 写「为什么」本身，需要决策依据时引 ADR 编号 |
| 这段是新加的哪批工作 | 靠 `git blame` / commit message，不靠注释 |

## 执行范围：只约束新增与修改

**存量不动。** 规则出台时代码里已有约 9,041 处 FR 编号、分布在约 2,064 个文件
（`grep -ro "FR-[0-9]\+" --include=*.go --include=*.ts --include=*.tsx apps internal packages`）；
把构建脚本类的 js/jsx/mjs/cjs 一并计入则为约 19,003 处。
全量清理是独立的一轮工作（已登记为 **FR-508**），不在日常改动里顺手做——那会把功能提交搅成噪音。

判定方式：**只检查本次 diff 新增/修改的行**。存量行原样保留，即使它就在你改的那个文件里。

```bash
# 门禁做法（见 scripts/check-no-fr-in-code.sh）
# 只扫 `git diff <base>...HEAD` 的 `+` 行，存量不参与
```

## 门禁

`.claude/scripts/check-no-fr-in-code.sh`：

| 模式 | 命令 | 用途 |
|---|---|---|
| diff（默认） | `sh .claude/scripts/check-no-fr-in-code.sh` | 只扫 diff 的 `+` 行，命中即失败并打印位置。日常提交前跑 |
| 存量 | `sh .claude/scripts/check-no-fr-in-code.sh --stock` | 扫全仓代码现有编号，**期望 0 命中**。FR-508 的验收就是这个 |

配套 CI 步骤在 [gate-merge.md](gate-merge.md) 的静态检查项里。

**误报例外**：文档路径（`docs/`、`CHANGELOG.md`）与 `.claude/rules/` 本身不在扫描范围。
若确有必需保留的场景（例如迁移脚本要按旧编号匹配数据），在该行加
`// no-fr-check: <原因>` 豁免，并在 PR 描述里说明。

## 自检

- [ ] 本次新增/修改的注释里没有 `FR-\d+`
- [ ] 新增文件名不含 `fr\d+`
- [ ] 测试描述（`describe` / `it` / `t.Run` 的字符串）不含 FR 编号
- [ ] 需要决策依据时引的是 ADR 编号，不是 FR 编号
