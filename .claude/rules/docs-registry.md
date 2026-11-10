# docs 只放已登记的文档

> **仓库的 `docs/` 不是草稿区、不是证据库、不是临时目录。**
> 往里放东西前先确认它属于哪一类，且已经把登记动作做完。

## 规则

### 一、`docs/` 下只允许「已登记」的文档

`docs/` 顶层只放这几类（对应 [doc-evolution.md](doc-evolution.md)）：

| 文档 | 路径 |
|---|---|
| PRD | `docs/PRD.md` |
| ARCHITECTURE | `docs/ARCHITECTURE.md` |
| API | `docs/API.md` |
| CHANGELOG | `CHANGELOG.md`（仓根） |
| Conventions | `docs/CONVENTIONS.md` |
| ADR | `docs/adr/*.md` + 索引 `docs/adr/README.md` |
| Feature Spec | `docs/specs/<feature>/` + 索引 `docs/specs/README.md` |
| 其他（DEPLOY / LIFECYCLE / CONTRIBUTING 等） | 已在根目录登记确认的固定集 |

**「登记」的具体含义**：至少有下面之一——

- 在 PRD §4 的 FR 行或「规格索引」里有引用
- 在 `docs/adr/README.md` 里有条目
- 在 `docs/specs/README.md` 或该 feature 的 spec 里被引用
- 属于上表的固定文档集

### 二、禁止自建 `docs/` 子目录

**禁止**为了放「顺带产生的东西」而在 `docs/` 下新建目录。典型违规：

```
docs/specs/_evidence/     ← 验收记录
```

用 `_` 前缀自封一个「特殊目录」不构成豁免。

### 三、验收记录 / 证据 / 临时产物放哪里

| 产物 | 正确位置 |
|---|---|
| 功能验收记录 | 该功能的 `docs/specs/<feature>/spec.md` 里新增一节 |
| 跨功能的验收报告 | 先写成 spec（过 spec-checklist），放在 `docs/specs/<该次工作>/spec.md` |
| 一次性证据（日志摘录、读数、命令输出） | **不提交**；需要引用就在 spec/PRD 里摘录关键行 |
| 运行期产物 | `temp://` / `/tmp`，不进仓 |

**判定口径**：如果一个文件「删了不影响任何人理解当前系统」，它就不该在 `docs/` 里。
历史决策进 ADR，需求状态进 PRD，详细规格进 spec——三者之外没有第四类容器。

## 为什么要这条

### 反面实例（2026-11-10）

做 PR#71 全量 FR 验收时，自建了 `docs/specs/_evidence/` 放 6 个验收记录文件，
并用 `_` 前缀暗示「这是特殊目录，不算正式文档」。

后果：

1. **没人知道它们属于哪个功能**——目录名不含 feature 标识
2. **越攒越多**——每个批次都想「顺手留个证据」，目录变成第二个 PRD
3. **与 spec 双源**——同一个功能的验收记录散在两处，以后改一处另一处就过时
4. **绕过登记**——这些文件既不在 PRD、也不在 specs/README、也不在 ADR 索引里

正确的做法：验收记录写进该 FR 对应的 `docs/specs/<feature>/spec.md`；
跨批次的汇总要么进 `docs/specs/<工作>/spec.md`，要么只把结论摘录进 PRD/CHANGELOG。

## 自检

提交前跑：

```sh
sh .claude/scripts/check-docs-registry.sh
```

它会列出 `docs/` 下所有**未登记**的文件/目录。期望输出为空。
