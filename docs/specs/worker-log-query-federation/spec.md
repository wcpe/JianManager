# 功能规格：Worker 日志查询服务（FR-479）

> 状态：已交付@v0.24.0（本轮完整逐源固定视图、真实租约/read pin、故障分页与全请求预算修复及已覆盖回归完成；既有 Log RPC 接线和验收历史保留）
> 本次提交/功能修复验收不承诺性能、长稳或全部故障覆盖；已有具名回归、正常容量与指定真实 live 及延期范围统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)。　·　关联 PRD：FR-479　·　依赖：FR-473/476/477/478

## 2026-10-22 固定视图与请求预算修复契约

以下保留修复约束，本轮已覆盖回归按顶部声明和共享验收表收口，下文旧候选待验进度仅保留原时点，不外推全部故障或性能通过；租约/物理日协议见 [Lifecycle 规格](../worker-log-lifecycle/spec.md)。

- View 必须冻结完整的逐源投影：源身份、源代次、该源 projection generations、封闭水位与固定读取位置一并深拷贝；不能只存最大水位、目录或扁平 generation 列表。普通追加继续读旧 projection；集合增删、逐源缩水或重建替换使原集合不可读时明确 `VIEW_STALE`。
- 创建 View 与 Catalog 真租约登记原子完成，TTL 必须非零且有界；每次实际 Search/Stats/Fields/Facets/VIEW_BOUNDED 读持 request pin。取消/到期/淘汰只阻止新读，不能在实际读取 goroutine 返回前释放 pin 或删除物理目录。纯迁移保留可读旧引用时不自动切到新 owner。
- 任一参与 range 失败或缺少全局安全页边界证明，可返回 partial 行，但不发跨越未知范围的新 cursor，`exhausted=false`，明确同页重试；Stats 的顶层 coverage/quality，以及 Fields/Facets 现有 wire 支持的 coverage/截断证据不得在 RPC 映射时丢失；当前 Fields/Facets proto 没有 `LogQuality`，不得虚构 Worker quality 或为此新增 proto，CP Facets 质量按覆盖/截断保守派生。空字段/空桶与完整零结果不能混同。
- 字节、时间、扇出与并发是整请求预算，包含初始规划、续页、同戳边界补取及合并任务；入口默认值与执行期零剩余额度分开，零份额不发请求。实际 reader 先限预读与单行字节再分配，不能只在读完整行后计账。
- 故障态与 legacy 兼容必须经真实 Service → gRPC 映射 → CP 适配链验证；现有 RPC 语义足够时不得为修复新增 Worker Export 或即时释放 RPC。

待验收：同一 view 的多源/跨日/迁移/追加/缩水矩阵、确定性交错的 pin 与退休屏障、失败 range 的同页重试、整请求预算及真实 wire 顶层覆盖保真；未执行链路不能用包测通过代替。

### 当前查询合同与验收（2026-10-24）

selector 仅等价分组，完整 source/代次/closed、字段、排序/tie 和预算不变，inline/ref 正文语义保持。首屏 FAST Pending 与完整 Search/Stats 的统计分母分离；表达式变短不推导性能收益。最新门限与未测范围统一见 [验证状态](../worker-log-platform-contract/spec.md#2026-10-24-验证状态r028性能-hold)，各门结果只绑定自身输入。

### 当前实现与定向证据（2026-10-22）

- `query.Options.ViewTTL` 默认 5m，`MaxViewLifetime` 绝对上限 5m，可缩短但不可延长；复用不续期。`SnapshotAndLease` 提供完整 `PublishedProjection + Owner/DirID/Generation`，Planner 私有深拷贝，`GetView/Views` 返回副本。
- Planner 已消费 `Catalog.NewQueryViewID(now)` 的进程 epoch+一次 pending 票据，不自行造 ID；closed/expired 或旧进程 ID 不能重新建立 view。Catalog 容量拒绝映射可重试 `ErrCodeBudgetExceeded`，不能伪成空结果或降级新 view。Catalog/Lifecycle 当前 40 文件同内容的票据/sidecar 故障、CAS/pin 和真双 VL 三启证据及旧容量压力限制见 [生命周期规格](../worker-log-lifecycle/spec.md)；此前查询包 query/race 绿不自动成为末候选联合绿，旧小 PP 容量测量也不套给当前内容。
- 普通 append 继续读冻结的逐源旧集合；`AllowProjectionDrift` 仅保留 wire 兼容，不再豁免破坏集合。源集合变化、逐源缩水至 0 或重建导致原集合不可读时 stale；经完整证明的纯迁移，旧 lease 仍读旧 owner/目录，新 view 才读新 owner。
- 每 request 与实际 range goroutine 分别持 pin，实际 range 返回、所有在途排空后释放；TTL/淘汰/Worker Close 先禁新读。跨 CP 取消/淘汰无即时释放 RPC，Worker TTL 兜底。
- 未知 coverage、range 失败或未调度任务时 partial 行可返，但无新 cursor 且 `exhausted=false`；仅全部 range 证明 `SafeBefore` 的已知空过滤可推进安全栅栏，保持 complete、枚举 OPEN。
- legacy 旧格式只有来源与逐源显式水位证据齐全才升级 scope，缺证据 not-ready；现代 scope 的水位 0 是 `<=0`，不是无上界。
- 首次完整 `query/...` suite 已执行 `go test -p 1 ./internal/worker/logs/query/... -count=1 -timeout=120s`，退出码 0。后续真实 CP/Worker/VL 非空固定 view 的 Search/Stats/Export 与真后端 Chromium 指定分页/半开窗/取消短场景已通过，详见 [有限联合短验](../worker-log-platform-contract/spec.md#2026-10-22-有限真实联合短验与剩余门禁)；更广 TTL/迁移/legacy 矩阵与最终全 Go、正式长窗门禁仍未全部完成。

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
