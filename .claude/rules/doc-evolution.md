# 文档演进规则

> 每类文档的演进方式不同，遵循以下规则。

| 文档 | 路径 | 演进方式 | 说明 |
|---|---|---|---|
| PRD | `docs/PRD.md` | **增量 FR + 状态流转（索引级，详情进 specs）** | 遵循 SDD PRD 模板 §1-8 结构。§4 是**一行一个 FR 的登记表**（编号｜需求｜优先级｜状态）；新 FR = 加一行；已有 FR **只改状态字段**（📋 计划 → 🔨 开发中 → ✅ 已交付@vX.Y.Z，另有 ⏸️ 已延后 / ❌ 已废弃），交付/废弃后**仍留在 §4 表内**、不迁出。**单功能的详细描述与验收放 `docs/specs/<feature>/`，PRD 不内嵌 FR 全文**；活跃 FR 在 §4 上方「规格索引」指明其 spec 路径 |
| ARCHITECTURE | `docs/ARCHITECTURE.md` | **原地更新到当前真貌** | 始终反映系统当前状态；不保留历史版本；历史决策见 ADR |
| API | `docs/API.md` | **原地更新到当前真貌** | 和代码保持同步；endpoint 增删改直接体现在文档中 |
| ADR | `docs/adr/*.md` | **只追加 + 取代，不删** | 新 ADR 编号递增；推翻旧决策时新 ADR 引用旧 ADR（superseded-by）；旧 ADR 不删除 |
| CHANGELOG | `CHANGELOG.md` | **累积 + 发版分段** | `[Unreleased]` 段累积当前变更；发版时移到版本号段下 |
| Conventions | `docs/conventions.md` | **原地更新** | 规范变更直接修改 |
| Feature Impl | `docs/specs/<feature>/impl.md` | **开发中持续更新，完成后归档** | 开发时打勾任务、记录实际进度；feature 完成后状态标记 ✅ done 不再修改 |

## 关键原则

1. **需要知道「现在是什么样」的文档** → 原地更新（ARCHITECTURE, API, Conventions）
2. **需要知道「为什么这么选」的文档** → 只追加不删（ADR）
3. **需要知道「什么时候做了什么」的文档** → 累积追加（CHANGELOG）
4. **需要知道「需求的生命周期」的文档** → 增量 + 状态流转（PRD）
