# ADR-076: Agent Token 与 CP 唯一策略真源

- **日期**: 2026-07-23
- **状态**: proposed（随 FR-384 落地转 accepted）
- **上下文**: 需要 AI/脚本/CI 操控后台，且危险权限默认拒绝、资源可 scope、与紧急 CLI（ADR-041）区分。

## 决策

1. **专用 Agent Token**（独立于人类 JWT），hash 入库，明文仅创建时返回。
2. **CP 为策略唯一真源**：写白名单、scope、硬拒绝、审计均在 CP 判定；CLI/MCP/curl 不得本地发明策略。
3. **默认只读 + 写白名单**；MVP 写项仅 `instance.life`（start/stop/restart）与 `node.maintenance`。
4. **硬拒绝面**：用户/组/权限、平台设置、DB 浏览、自更新、删节点/实例、kill、制品/密钥管理、审计删除。
5. 审计 `actor_kind=agent` 与 `user` 区分。

## 理由

- 与人类同权仅靠审计不足；jmctl 无 CP 鉴权不适合作日常 agent 面。
- 三入口同闸避免「curl 绕过 MCP」。

## 后果

- 新表 `agent_tokens`、中间件、Agent API；FR-385/386/387 依赖本 ADR。
- 与 ADR-029（业务高危写）分离：业务写不进 agent MVP 白名单。

## 关系

- ADR-041（jmctl 紧急）：正交，不合并。
- FR-384~388。
