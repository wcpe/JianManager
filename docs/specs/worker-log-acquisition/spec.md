# 功能规格：Worker 日志采集与持久化通道（FR-473）

> 状态：实现中（Worker 配置化 FileTailer → WAL → VL → PublishedProjection 运行时已接线；远程强杀、ACK UNKNOWN→REPLAY_REQUIRED、projection-backed reclaim 已通过，磁盘满/人工恢复矩阵仍待验）　·　关联 PRD：FR-473　·　依赖：FR-472

## 1. 背景与目标

采集账本、源分段、WAL 四水位和至少一次投递。

本规格不得重新定义 FR-472 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：采集失败后的 UNKNOWN/恢复责任由 FR-472 §4.2～§4.4 定义；WAL 合法回收后必须从登记的恢复分段恢复。
- 范围内：FileTailer、STDIO_PRIMARY 和 ArchiveImporter 统一写入事件管道；轮转通过 source_generation 关联；坏记录进入隔离队列；WAL 先 durable 再 delivery，按 FR-472 reclaim 证明回收。
- 继承 FR-472：未知投递结果不进入坏记录隔离；受管恢复分段承担 WAL 回收后的恢复责任，恢复来源未解除前不得普通到期清理。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

FileTailer、STDIO_PRIMARY 和 ArchiveImporter 统一写入事件管道；轮转通过 source_generation 关联；坏记录进入隔离队列；WAL 先 durable 再 delivery，按 FR-472 reclaim 证明回收。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-472 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-472 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-482 文档对账。

## 5. 验收标准

- [ ] 轮转压缩不重复；损坏 gz/编码/权限/截断可见；HTTP 2xx 不直接回收；容量满暂停并报告缺口。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-472 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-472 已冻结；本规格仍须完成 Worker WAL、恢复责任和 Runbook A 真机验收，未完成前保持开发中。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-475 资产审批冻结。

## 3.1 采集账本与投递边界

- `CreateInstanceRequest` 增量字段 19/20/21 为 `log_target_id`、`log_acquire_mode`、`log_source_generation`。CP 以实例 ID 下发授权 namespace，以实例 UUID 与 holder Worker UUID 构造稳定 generation；Java 实例默认 FILE_PRIMARY（工作目录 `logs/latest.log`），通用进程默认 STDIO_PRIMARY。旧 CP 省略字段时不推断数字目标 ID。
- Worker 的首次登记与 Resync 已存在实例路径均登记日志绑定；绑定未持久化成功不确认本次实例注册。`ingest.state.json` 保存采集配置和实例绑定，Worker 重启先恢复它们，不依赖 CP 当时在线。
- STDIO_PRIMARY 的 stdout/stderr 分别追加到 Worker 数据根下、由实例身份哈希命名的受管 Raw 文件，写入并 fsync 后才由常驻采集循环读取；文件尾部偏移沿用同一 WAL/账本恢复语义。FILE_PRIMARY 的 stdout 继续用于实时控制台，不再作为第二份持久事件输入。
- Raw 写入失败持久登记 `STDIO_RAW_WRITE_FAILED`、暂停采集并阻止 cutover；普通“投影已覆盖”不能消除此类可能已丢失的原始数据缺口，必须人工核验。Worker 已注册但无持久日志绑定的实例同样阻止 cutover。
- 自动化验证：`TestInstanceStdioPersistsRawBeforePollingAndRestoresBindings`、`TestInstanceFilePrimaryDoesNotDoubleCollectConsoleOutput`、`TestInstanceRegistrationAndResyncBindManagedLogs`。真实子进程与官方 VL 验证：`TestRealProcessStdoutAndStderrReachManagedVL`；该测试不替代 CP 浏览器创建实例、daemon 重启和轮转矩阵的远端验收。

- 不可变微投影发布兼容旧 journal 的单值 `projection_generation`：首次追加前先读取原已发布 generation，随后将原值与新值一起写入 `projection_generations`。只有明确的隔离全量重建才替换旧集合，不能因字段升级使历史事件从查询输入消失。回归：`TestAppendMicroProjectionPreservesLegacyPublishedGeneration`。
- 每次物理写入前保存 Catalog 权威快照，并通过 Catalog 条件提交原子发布 manifest 与封闭水位。迁移或并发发布先完成时，旧写入者不得覆盖新 owner/manifest；`publication_pending` 在请求前落盘，响应丢失、发布冲突或进程重启后均触发隔离 generation 全量重建。回归：`TestMicroGenerationLostResponseIsExcludedAndRestartCompacts`、`TestOwnerChangeDuringVerificationCannotPublishToNewOwner`、`TestProjectionWritersCannotOverwriteAnotherCommittedManifest`。

- FileTailer、STDIO_PRIMARY、ArchiveImporter 共享同一 `(log_source_id, source_generation)` 账本；`latest.log → 轮转 → .gz` 是同一逻辑源的连续分段，`.gz` 接管前必须登记前段结束位置，禁止按压缩包 hash 重新导入整段。
- `source=worker` 的 Worker/Node 自身日志也进入同一采集面；采集失败不能递归写回自身 VL 失败日志。
- FILE_PRIMARY 常驻循环自动发现同目录 `.gz`（可用 `archive_glob` 收窄），以文件名 UTC 日锚定归一化时间。账本持久保存当前 live 分段的逻辑基址与稳定前缀摘要：同路径替换必须先关闭旧分段 EOF，再由 ArchiveImporter 跳过已经 durable 的物理前缀、补齐未读尾部，最后才给新 `latest.log` 分配后续位置。历史 gzip、live 文件、WAL 和投影继续使用同一 source generation；压缩包 hash 只作 provenance。
- gzip 只有在事件已由 durable WAL 覆盖后才标记 `imported`。损坏或不可读对象登记可见缺口及观测 size/mtime，未变化前保持隔离、不在每轮轮询重复制造缺口；对象变化后才允许受控重试。自动回归：`TestManagerAutoImportsHistoricalGzipBeforeCurrentFile`、`TestManagerRotationImportsOnlyUnreadTailBeforeReplacementFile`、`TestManagerQuarantinesUnchangedCorruptGzipWithoutRepeatingGap`。
- HTTP 2xx 只写入 `REQUEST_DONE`，不能推进 `reclaim_position`；响应丢失进入 `UNKNOWN`，按 event_id/位置核验或从受管恢复分段重放。
- Multiline 缓冲未形成完整事件时只能推进 `read_position`，不得推进 durable checkpoint；崩溃后从首行位置恢复拼接。
- WAL、隔离队列或受管恢复分段达到预算时暂停低优先级采集，返回缺口、暂停原因和恢复动作；不得用丢弃计数伪装完整。

## 3.2 失败验收

空闲采集轮询不得重写/同步全部历史状态；仅位置变化、新事件或错误需要检查持久化。测量“本批 durable 提交延迟”必须核对持久账本已覆盖该批完整事件的结束位置，不能仅观察状态文件 mtime。自动回归：`TestIdlePollDoesNotRewriteDurableHistory`。

| 场景 | 必须结果 |
|---|---|
| latest.log 轮转后 ArchiveImporter 介入 | 同一逻辑源不重复计数，账本连续 |
| VL 写入成功但 ACK 丢失 | UNKNOWN/核验/重放可审计，未确认数据仍有恢复来源 |
| 损坏 gz、编码错误、权限不足 | 隔离记录、缺口可见、后续源不被拖死 |
| WAL/临时盘满 | 暂停或降级，实例管理继续可用，恢复后可继续推进 |
