# ADR-076: Agent Token 与 CP 唯一策略真源

- **日期**: 2026-07-23
- **状态**: accepted（部分决策被 ADR-080 取代）
- **上下文**: 需要 AI/脚本/CI 操控后台，且危险权限默认拒绝、资源可 scope、与紧急 CLI（ADR-041）区分。

## 决策

1. **专用 Agent Token**（独立于人类 JWT），hash 入库，明文仅创建时返回。**仍有效。**
2. **CP 为策略唯一真源**：写白名单、scope、硬拒绝、审计均在 CP 判定；CLI/MCP/curl 不得本地发明策略。**仍有效。**
3. **默认只读 + 写白名单**；MVP 写项仅 `instance.life`（start/stop/restart）与 `node.maintenance`。**V1 兼容仍有效；长期扩展模型由 ADR-080 的 capability 分组取代。**
4. **硬拒绝面**：用户/组/权限、平台设置、DB 浏览、自更新、删节点/实例、kill、制品/密钥管理、审计删除。**平台治理与秘密面仍永久拒绝；实例删除/强杀/节点删除等由 ADR-080 改为“未登记默认拒绝，需后续 FR 显式开放”。**
5. 审计 `actor_kind=agent` 与 `user` 区分。**仍有效。**

## 理由

- 与人类同权仅靠审计不足；jmctl 无 CP 鉴权不适合作日常 agent 面。
- 三入口同闸避免「curl 绕过 MCP」。

## 后果

- 新表 `agent_tokens`、中间件、Agent API；FR-385/386/387 依赖本 ADR。
- 与 ADR-029（业务高危写）分离：业务写不进 agent MVP 白名单。

## 关系

- ADR-041（jmctl 紧急）：正交，不合并。
- FR-384~388。
- **部分被 ADR-080 取代**：V1 写白名单作为长期模型、旧硬拒绝边界中的实例/节点破坏性与制品管理；专用 Token、CP 真源、JWT/jmctl 分离继续有效。
