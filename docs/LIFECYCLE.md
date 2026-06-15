# 开发生命周期

> 本文档定义 JianManager 的开发流程规范。不追踪具体阶段，阶段进度由 PRD.md 的 FR 状态体现。

---

## 开发循环

### 新需求

```
1. PRD.md 追加新 FR（状态 📋 todo）
2. /sdd-develop-feature FR-XXX
   → 文档 → 脚手架 → 编码 → 审查 → 收尾
3. 完成后 FR 状态 → ✅ done
4. Gate 4 检查（gate-merge.md checklist）
5. 合并到 main
```

### Bug 修复

```
/sdd-fix-bug
→ 不需要新 FR，直接修复 + 回归测试
```

### 紧急修复

```
/sdd-hotfix
→ 最短路径止血，后续补做彻底修复（记录为新 FR）
```

### 重构

```
/sdd-refactor-code
→ 不改变外部行为，可能需要更新 ADR
```

### 依赖升级

```
/sdd-bump-dependencies
→ 周期性执行，minor/patch 直接升级，major 逐个评估
```

---

## 发版周期

```
开发积累 → 版本号确定 → /sdd-release-version → CHANGELOG → tag → 打包
                     ↑                                       |
                     └───────────────────────────────────────┘
```

- **patch**: Bug 修复积累到一定量，或有安全修复
- **minor**: 新 FR 完成若干个
- **major**: 有破坏性变更（API 不兼容、数据库 migration 不兼容）

---

## ADR 变动规则

### 什么情况下需要新 ADR

| 场景 | 需要 ADR | 示例 |
|---|---|---|
| 选择新的外部依赖 | ✅ | 引入 Redis 作为缓存 |
| 更换通信协议 | ✅ | gRPC 改为 HTTP/2 |
| 更换数据库 | ✅ | SQLite 改为 PostgreSQL |
| 更换前端框架 | ✅ | React 改为 Vue |
| 新增进程模型 | ✅ | 新增独立 Scheduler 进程 |
| 新增一个 API endpoint | ❌ | 日常 feature 开发 |
| 重构内部实现 | ❌ | 不改变外部接口 |
| Bug 修复 | ❌ | 除非 bug 暴露了架构问题 |

### ADR 生命周期

```
新决策:
  1. 创建 docs/adr/NNN-<标题>.md，状态 proposed
  2. 确认后状态 → accepted

推翻旧决策:
  1. 创建新 ADR，状态 accepted
  2. 新 ADR 引用 "supersedes ADR-NNN"
  3. 旧 ADR 状态 → superseded-by ADR-MMM
  4. 旧 ADR 文件不删除、不修改内容

编号始终自增，永不复用。
```

### 预估频率

- minor 版本：0-2 个新 ADR
- major 版本：2-5 个新 ADR
- 项目全生命周期：30-80 个 ADR

---

## 文档变动频率速查

### 常变动（每个 feature / 每次发版）

| 文档 | 变动频率 | 演进方式 |
|---|---|---|
| `docs/PRD.md` | 每个新需求 | 增量追加 FR + 状态流转 |
| `docs/API.md` | 每个 feature | 原地更新 |
| `docs/specs/<feature>/impl.md` | 每个 feature | 开发中更新，完成后归档 |
| `CHANGELOG.md` | 每次发版 | 累积 + 发版分段 |

### 中等变动（架构变更时）

| 文档 | 变动频率 | 演进方式 |
|---|---|---|
| `docs/ARCHITECTURE.md` | 架构变更时 | 原地更新 |
| `docs/adr/*.md` | 架构决策变更时 | 只追加 + 取代 |
| `docs/CONVENTIONS.md` | 规范调整时 | 原地更新 |

### 罕变动（通常只写一次）

| 文档 | 变动频率 | 说明 |
|---|---|---|
| `CLAUDE.md` | 项目结构大改时 | Claude Code 入口指引 |
| `.claude/rules/*.md` | 流程优化时 | 规则调整需谨慎 |
| `.claude/skills/*.md` | 新增工作流时 | 技能可逐步扩展 |
| `scripts/check-sdd.sh` | CI 调整时 | 门禁脚本 |

---

## 后续新会话的启动流程

```
1. Claude 自动读取 CLAUDE.md → 了解项目上下文
2. Claude 自动加载 .claude/rules/*.md → 遵守规则
3. 用户说「继续开发 FR-XXX」→ Claude 读取对应 spec 继续
4. 用户说「检查门禁」→ Claude 按 gate-*.md checklist 逐项核对
```

**无需重复解释架构或规则** —— 所有上下文都在文件中。
