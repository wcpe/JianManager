# 功能规格：Worker 日志查询服务（FR-479）

> 状态：实现中（Worker 主进程已装配 Log RPC；Search/Stats/Fields/Facets/FOLLOW_LIVE/VIEW_BOUNDED 与 Rehydrate/ArchiveStatus 入口已接线；远程 Search/Stats/Facets/Export 通过，Catalog 跨 Tier/完整 Rehydrate 仍待验）　·　关联 PRD：FR-479　·　依赖：FR-473/476/477/478

## 1. 背景与目标

按 Catalog 提供不重叠权威副本的 Worker 查询面。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：Search/Stats/Fields/Facets/Tail/Rehydrate/ArchiveStatus 只能读取已发布 projection/view；`closed_visible_seq` 空洞和 generation 冲突返回覆盖状态。
- 范围内：QueryPlanner 创建并执行 Query View；Search/Stats/Fields/Facets/Tail/Rehydrate/ArchiveStatus 共用覆盖、Cursor、预算、取消和权限；Tail 区分 FOLLOW_LIVE 与 VIEW_BOUNDED。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

QueryPlanner 创建并执行 Query View；Search/Stats/Fields/Facets/Tail/Rehydrate/ArchiveStatus 共用覆盖、Cursor、预算、取消和权限；Tail 区分 FOLLOW_LIVE 与 VIEW_BOUNDED。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- FOLLOW_LIVE 使用独立观察窗口（缺省 1 秒、请求预算上限 30 秒），Worker Service 每轮重新选择当前已发布 Catalog 范围，允许新投影/新 UTC 日进入；仍严格执行各源封闭水位，不查未发布物理副本。轮询使用临时计划，不创建无限增长的可复用 View。窗口结束始终 `exhausted=false`、`enumeration_state=OPEN`，调用方取消必须是显式 CANCELLED；VIEW_BOUNDED 原固定集合语义不变。回归与真实 VL 验证分别见 `TestFollowLiveReplansNewProjectionAndNewUTCDay`、`TestFollowLiveReportsCallerCancellation`、`TestRealVLPartitionDayIsolation`。

- `LogTail` 没有独立 coverage 响应信封，底层 coverage 不完整时必须返回非成功 gRPC 状态，并在发送事件前拒绝该流；有部分事件或零事件均不能以成功 EOF 表示完整。回归：`TestLogTailPartialCoverageCannotBecomeSuccessfulStream`。

- RangeClient 的 Search、Stats、Facets、Fields、VIEW_BOUNDED Tail 必须将请求时间窗与 Catalog UTC 日求交集，HTTP `end` 为开区间边界。相邻日可共享源身份与 projection generation，但不得因查询未加日界而重复统计；空交集不调用 VL，非法 Catalog 日返回错误。自动回归为 `TestPartitionDayBoundsApplyToSearchStatsAndFacets`，真实资产回归为 `TestRealVLPartitionDayIsolation`（必须显式提供审批二进制及 SHA-256，缺环境时的 Skip 不算验收通过）。

- [ ] HOT/COLD/DEEP 结果不重复；回灌/迁移/恢复造成 view stale 时显式失败；partial 不显示完整零结果；成本预算生效。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-473 已冻结；本规格仍须完成 Worker VL RangeClient、Catalog 查询和 Runbook B/C 依赖验收，未完成前保持开发中。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-476 资产审批冻结。

## 3.1 QueryPlanner 路由

- QueryPlanner 先按 Catalog 选择不重叠的权威副本，再查询；禁止把所有物理副本交给查询层临时去重。
- 每个响应返回 `complete/partial_reasons`、覆盖目标、缺失层、截断、积压、rehydrate 状态、duplicate_quality 和 stats_quality；partial 不得转成空成功。
- Tail 明确区分 `FOLLOW_LIVE`（控制台原始流或实时订阅）与 `VIEW_BOUNDED`（标准化事件固定视图）；两者不共享“完整历史”语义。
- FR-479 的跨层完整验收依赖 FR-477/478；只测 HOT 只能标 HOT 能力通过。
- Search 正常路径保留 `fields → time sort → limit` 与 union 查询；仅在页边界同时间戳组完整性尚未证明时补取窄时间组，完整组按冻结复合键排序及过滤 cursor，不恢复整窗口多键全表排序。补取与跨段续读共用剩余字节预算；不完整组只能返回严格更新的安全前缀，不能发出越过缺失组的游标。
- Search/Stats/Fields 采用有界增量调度，最多 8 个真实在途；先预留总扫描额度、完成归并后释放未用份额，零份额不调度。入站 `max_bytes=0` 默认整请求 32 MiB，执行时不能把零剩余额度转成默认额度；merged 各段及边界组共享额度。NDJSON 流式读取同时限制预读和超大单行，不先读完整超大行再检查。
- fanout 截断以原始 Catalog ranges 计，不以合并后任务数偷换；Stats 发起前标 `BUDGET_EXCEEDED` 与 partial 统计质量。达到扫描额度或取消时停止调度并取消在途，正常 limit 分页不误标预算失败。
- `LogSearchResponse.ingest_position_gaps` 单独返回人工永久放弃位置凭据，实际源/代次由采集账本提供；未知值不推断。它不改变查询时间覆盖、事件统计质量或导出判据；协议字段与 CP 无损 HTTP 形态见 [API.md](../../API.md)。

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-479 Worker 日志查询服务（feat）

**需求补充**：Catalog 驱动不重叠权威副本；Search/Stats/Tail/Fields/Facets/StorageHealth/Rehydrate/ArchiveStatus；覆盖状态与 Cursor/水位；取消与预算；**完整验收依赖 477/478** → `docs/specs/worker-log-query-federation/spec.md`

**交付状态**：✅ 已交付@v0.24.0·全查询 RPC/受管 VL 已接线，远程 Search/Stats/Facets/Export 通过；**CP 联邦真机查询通过**（`complete=true`、目标 success、返回事件）
