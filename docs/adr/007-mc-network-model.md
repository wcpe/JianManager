# ADR-007: MC 群组服建模与资源所有权

- **日期**: 2026-06-18
- **状态**: accepted
- **上下文**: 下一阶段聚焦 MC 群组服（代理 + 多 Bukkit 子服）。需要建模代理与子服的关系。关键约束：**一个子服可被多个群组/代理共享**（共享大厅、共享小游戏服），因此「群组拥有子服」的 1:N 容器模型不可行。同时工作目录由谁分配、归谁所有需要明确。
- **决策**:
  1. **实例为独立原子单元**，新增 `role`（proxy / backend / universal）。
  2. **proxy ↔ backend 用 `server_registrations` 建模为 M:N**：每条注册携带「代理内本地属性」`alias`（在该代理 servers{} 中的名字）、`priority`（try/优先级顺序）、`forced_host`、`restricted`、`enabled`。同一 backend 可注册进多个 proxy。
  3. **群组（Network）为可选、非独占软标签**（`networks` + `network_members` M:N）：仅供 UI 分组/筛选/批量操作，一个子服可属于多个群组；**真实路由只由 `server_registrations` 驱动**，群组不做归属容器。
  4. **工作目录由系统分配**：Worker 在 `servers_dir` 下建实例自有目录 `servers/<name-slug>-<shortid>`，用户不可输入，路径只读展示。
- **理由**:
  - M:N 注册直接映射 BungeeCord/Velocity 的真实形态（同一后端地址可被多个代理 servers{} 引用，且各代理可用不同别名）。
  - 系统分配目录消除路径穿越/权限/冲突风险，并为按目录做磁盘配额、一键复制（目标目录自动分配）打基础。
  - 群组软标签兼顾「自由搭配」与「按大区批量运维」。
- **后果**:
  - **取代 BUG-004「workDir 必填且用户填写」的 UI**；现有实例沿用旧 workDir（grandfather）。
  - 共享 backend ⇒ 注册它的所有 proxy（Velocity modern）必须共用同一 forwarding secret，配置一致性校验需跨网络做（见 ADR-008 不涉及，由配置引擎 FR-031 落实）。
  - 数据库新增 `server_registrations`、`networks`、`network_members` 表，实例新增 `role` 字段。
- **替代方案**:
  - 群组作为独占容器（实例 `network_id` 1:N）— 无法表达共享子服，否决。
  - 用户自填工作目录 — 保留为「导入已有目录」高级模式，非默认。
