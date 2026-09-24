# 功能规格：Worker 日志查询服务（FR-478）

> 状态：实现中（Worker 主进程已装配 Log RPC；Search/Stats/Fields/Facets/FOLLOW_LIVE/VIEW_BOUNDED 与 Rehydrate/ArchiveStatus 入口已接线；远程 Search/Stats/Facets/Export 通过，Catalog 跨 Tier/完整 Rehydrate 仍待验）　·　关联 PRD：FR-478　·　依赖：FR-472/436/437/438

## 1. 背景与目标

按 Catalog 提供不重叠权威副本的 Worker 查询面。

本规格不得重新定义 FR-472 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：Search/Stats/Fields/Facets/Tail/Rehydrate/ArchiveStatus 只能读取已发布 projection/view；`closed_visible_seq` 空洞和 generation 冲突返回覆盖状态。
- 范围内：QueryPlanner 创建并执行 Query View；Search/Stats/Fields/Facets/Tail/Rehydrate/ArchiveStatus 共用覆盖、Cursor、预算、取消和权限；Tail 区分 FOLLOW_LIVE 与 VIEW_BOUNDED。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

QueryPlanner 创建并执行 Query View；Search/Stats/Fields/Facets/Tail/Rehydrate/ArchiveStatus 共用覆盖、Cursor、预算、取消和权限；Tail 区分 FOLLOW_LIVE 与 VIEW_BOUNDED。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-472 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-472 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-482 文档对账。

## 5. 验收标准

- FOLLOW_LIVE 使用独立观察窗口（缺省 1 秒、请求预算上限 30 秒），Worker Service 每轮重新选择当前已发布 Catalog 范围，允许新投影/新 UTC 日进入；仍严格执行各源封闭水位，不查未发布物理副本。轮询使用临时计划，不创建无限增长的可复用 View。窗口结束始终 `exhausted=false`、`enumeration_state=OPEN`，调用方取消必须是显式 CANCELLED；VIEW_BOUNDED 原固定集合语义不变。回归与真实 VL 验证分别见 `TestFollowLiveReplansNewProjectionAndNewUTCDay`、`TestFollowLiveReportsCallerCancellation`、`TestRealVLPartitionDayIsolation`。

- `LogTail` 没有独立 coverage 响应信封，底层 coverage 不完整时必须返回非成功 gRPC 状态，并在发送事件前拒绝该流；有部分事件或零事件均不能以成功 EOF 表示完整。回归：`TestLogTailPartialCoverageCannotBecomeSuccessfulStream`。

- RangeClient 的 Search、Stats、Facets、Fields、VIEW_BOUNDED Tail 必须将请求时间窗与 Catalog UTC 日求交集，HTTP `end` 为开区间边界。相邻日可共享源身份与 projection generation，但不得因查询未加日界而重复统计；空交集不调用 VL，非法 Catalog 日返回错误。自动回归为 `TestPartitionDayBoundsApplyToSearchStatsAndFacets`，真实资产回归为 `TestRealVLPartitionDayIsolation`（必须显式提供审批二进制及 SHA-256，缺环境时的 Skip 不算验收通过）。

- [ ] HOT/COLD/DEEP 结果不重复；回灌/迁移/恢复造成 view stale 时显式失败；partial 不显示完整零结果；成本预算生效。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-472 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-472 已冻结；本规格仍须完成 Worker VL RangeClient、Catalog 查询和 Runbook B/C 依赖验收，未完成前保持开发中。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-475 资产审批冻结。

## 3.1 QueryPlanner 路由

- QueryPlanner 先按 Catalog 选择不重叠的权威副本，再查询；禁止把所有物理副本交给查询层临时去重。
- 每个响应返回 `complete/partial_reasons`、覆盖目标、缺失层、截断、积压、rehydrate 状态、duplicate_quality 和 stats_quality；partial 不得转成空成功。
- Tail 明确区分 `FOLLOW_LIVE`（控制台原始流或实时订阅）与 `VIEW_BOUNDED`（标准化事件固定视图）；两者不共享“完整历史”语义。
- FR-478 的跨层完整验收依赖 FR-476/438；只测 HOT 只能标 HOT 能力通过。
