# ADR-094: Worker 日志本地数据面与 Control Plane 联邦查询

- **日期**: 2026-09-20
- **状态**: accepted
- **关联**: FR-473～484 · ADR-005 · ADR-013 · ADR-066 · ADR-081

## 背景

现有架构不变量要求 Worker 不访问 Control Plane 业务数据库，CP 是 SQLite/MySQL 的唯一读写方。日志平台需要在 Worker 本地运行 VictoriaLogs，并保存采集账本、WAL、Partition Catalog、canonical projection 和受管 Raw/分层归档；查询由 CP 经已认证反向 gRPC 隧道联邦。若不区分“CP 业务数据”和“Worker 日志数据面”，实现会直接撞上现行数据所有权规则。

## 决策提案

1. Worker 仍不得访问 CP 业务数据库、权限真源或指标时序存储；所有跨节点控制和用户授权经 CP gRPC/API 完成。
2. 允许 Worker 在受管数据根持久化 Worker-owned 日志数据面：VL HOT/COLD/Rehydrate 数据、WAL、规范事件恢复分段、Partition Catalog、采集账本、projection manifest/checkpoint、冲突和租约元数据。Worker 本地 SQLite 仅保存这些元数据，不是第二个全文检索引擎。
3. CP 是唯一浏览器入口和联邦协调器；Worker/VL 只监听 localhost，CP→Worker 只经 Worker 主动建立的反向隧道，浏览器不得直连。
4. CP 不把新 Worker/Node 查询日志全量写入 `logs` 表；CP 允许按预算、期限、权限控制生成临时导出产物，但临时产物不是日志查询库或新的长期保留层。
5. Search/Stats/Fields/Facets/Tail/Rehydrate/Export 均使用 FR-473 Query View、Catalog owner、coverage 和 scope 契约；Worker 不接受浏览器自定义查询绕过 CP 授权。

## 理由

- 保留 CP 业务库唯一所有权，同时给 Worker 日志数据面足够的本地故障恢复和低延迟能力。
- 通过 Catalog 和 PublishedProjection 把物理 VL 分区、逻辑事件集合与跨节点查询覆盖绑定，避免把所有副本直接丢给查询层去重。
- 复用 ADR-081 的反向隧道和最小暴露面，不增加 Worker 浏览器入口。

## 后果

- `.claude/rules/architecture-invariants.md`、`decision-alignment.md` 和 `docs/ARCHITECTURE.md` 必须明确此受控例外；在本 ADR 被接受、FR-473 冻结前不得开始全面实现。
- FR-476 仍独立负责 VL 发行资产的 tag、包/解包校验、许可和双平台兼容审批；本 ADR 不批准任何具体 VL 版本。
- ADR-013 的 CP 指标时序所有权不变；日志数据面与指标数据面不能互相替代。

## 历史评审关注点

- 本地日志数据根的权限、空间配额和清理边界是否满足部署环境。
- Worker 本地 SQLite 元数据 schema 是否与 Catalog/journal 恢复模型一致。
- FR-473 的 g1→g2、PublishedProjection、恢复责任释放和性能实验是否通过。

## 取代关系

本 ADR 不取代 ADR-005、ADR-013、ADR-066 或 ADR-081；只补充它们在日志数据面边界上的适用范围。
