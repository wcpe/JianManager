# 功能规格（specs）

命中 **spec-checklist** 的功能在动手前先写一份**工作规格**：一个功能一个目录 `docs/specs/<feature>/`，核心是 `spec.md`（要什么 / 怎么做 / 验收），可按需另立 `api.md`（接口契约）与 `impl.md`（实现进度）。单文件模板见 [`_template.md`](_template.md)。

## 何时写

对每条 feat / 增强类 FR 过 **spec-checklist**（客观判定，不靠感觉）：**命中任意一条 → 写 spec；全部不命中 → 免（PRD 行内标「免 spec」）**。

1. 引入新数据模型 / 表 / Schema
2. 引入新外部接口（API、命令、事件、配置项）
3. 跨模块（改动涉及 ≥2 个进程 / 模块）
4. 需要新 ADR 或推翻已有 ADR
5. 有下游依赖（其他 FR 依赖本 FR 先完成）
6. 涉及并发 / 事务 / 锁 / 状态机

小改动、bug 修复、重构、依赖升级一律免——走 PRD 状态列 + 对应技能即可。

## 与项目级文档的分工（别双源打架）

- [`../PRD.md`](../PRD.md)：持久路线图——功能在 PRD 是**一行 FR + 状态**；活跃 FR 的 spec 路径登记在 §4 上方「规格索引」。
- `docs/specs/<feature>/spec.md`：该功能**开发期的详细工作规格**（比 PRD 那行细）。
- 交付后：持久真相归并回 PRD（FR 翻 `✅ 已交付@vX.Y.Z`）+ [`../ARCHITECTURE.md`](../ARCHITECTURE.md)（更新到现状）+ ADR（若有架构决策）；spec 状态行标 done 留作历史记录，基本不再改。

## 怎么用

1. 建 `docs/specs/<feature>/`，参照 [`_template.md`](_template.md) 写 `spec.md`（状态行：草拟 → 开发中 → ✅ done）。
2. 在 PRD §4 的 FR 行与「规格索引」登记 spec 路径。
3. 按 `/sdd-develop-feature` 流程实现，对着 spec 的任务与验收推进。
4. 交付后归并回项目级文档（见上）。

> spec 是中频文档：开发期动、交付后基本不动。涉及架构决策时在 spec 里**引用** ADR 编号，不重复决策正文。
