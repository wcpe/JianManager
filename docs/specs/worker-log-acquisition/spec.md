# 功能规格：Worker 日志采集与持久化通道（FR-474）

> 状态：已交付@v0.24.0（既有采集链路和历史验收保留；本轮物理定位、持久提交资格、证据分离及恢复责任修复与已覆盖回归完成）
> 本次提交/功能修复验收不承诺性能、长稳或全部故障覆盖；追加最终 ACKLoss、严格完整 Worker 静默 766 等延期且未计通过，统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)。　·　关联 PRD：FR-474　·　依赖：FR-473

### 2026-10-30 显式评审修订：删除 gz 归档采集，只读活文件

**本次修订删除本规格中的归档采集核心链路（`archive_glob` 发现、`ArchiveImporter`、归档共账本、归档缺口与隔离），
§2/§3.1/§3.2/§5 的相关条款按下文与行内删除线失效。**

#### 新规则

- **只读活文件**（`latest.log` / `proxy.log` / STDIO 受管 Raw 等），轮转后的 `.gz`（以及 `.log.N` 纯文本轮转）
  **不再回读**：轮转后直接继续读新文件，不再等「轮转段可读」。
- 新绑定的源**不再补历史段**，从当前活文件末尾开始。
- `archive_glob` 配置键**保留**（规则表 / `worker.yml` / 游标表第 14 列仍会携带与重算它），
  但**不再驱动任何采集**：它现在只是一个被存着、被重算、没人消费者的字段。
- 手动入口 `Manager.ImportArchivesNow` 保留签名但**恒返回 `(0, 0, nil)`**：它同时是 `grpcsvc` 接口成员
  与前端按钮的入口，删接口要跳 `proto`/`grpcsvc`/前端三处；返回空与「确实没有归档可扫」过去也无法区分。
  定时扫描 `RunArchiveScan` 仍在（受 `log.acq.gz_import_enabled` / `log_ingest.archive_scan_interval` 控制），
  但扫出来的东西无人消费——**这两个开关当前也无实际效果**。
- `ledger.SegmentGzip` / `SegmentRotatedText`、`ARCHIVE_*` 缺口原因、各类隔离/重试语义**代码保留但无产生者**：
  既有的 `ARCHIVE_*` 差距仍能被读取与人工裁定，但新采集不会再生新的归档缺口。

#### 理由

1. 用户裁定（原话）：「只需要读取日志然后往 vl 塞，有就有没有就算了」。
2. 10-30 同日已裁定「机械盘不落盘」：归档回溯既是一次性大块磁盘读，又要为断点终读维护
   游标/指纹/内容锚点/归属闸——成本全在机械盘上。
3. 归档回读本身在 10-30 前就反复出问题（纯文本轮转识别、`.lck` 误吞、截断对象丢尾、
   跨代次补账），它是本模块历史缺口的主来源。

#### 代价（如实写，不粉饰）

- **Worker 停机期间轮转掉的日志会丢**：停机期间产生的 `.gz` / `.log.N` 不再补读。
- **新绑定/新部署的源不补历史**：从当前活文件末尾开始，文件中已有内容不会被采集。
- `ARCHIVE_*` 缺口计数、归档导入审计与「轮转压缩不重复」这类验收判据**失去对象**。

#### 被删除的条款（行内标注，下方正文已用删除线处理）

| 位置 | 原内容 | 现状态 |
|---|---|---|
| §2 范围内 | 「FileTailer、STDIO_PRIMARY 和 ArchiveImporter 统一写入事件管道」 | `ArchiveImporter` **删除**，仅剩前两者 |
| §3.1 | 「`latest.log → 轮转 → .gz` 是同一逻辑源的连续分段，`.gz` 接管前必须登记前段结束位置」 | **删除**：轮转后不再回读 |
| §3.1 | 「FILE_PRIMARY 常驻循环自动发现同目录 `.gz`（可用 `archive_glob` 收窄）」 | **删除**（键保留但无消费者） |
| §3.1 | 「gzip 只有在事件已由 durable WAL 覆盖后才标记 `imported`」与损坏/截断 gz 的缺口语义 | **删除**：不再有导入 |
| §3.2 场景表 | 「latest.log 轮转后 ArchiveImporter 介入」/「损坏 gz」 | **删除** |
| §5 验收 | 「轮转压缩不重复；损坏 gz/编码/权限/截断可见」 | **删除**（gz 部分） |
| 本文开头 2026-10-22 修复契约 | 「归档发现、导入和恢复不得递归占用同源操作门；归档状态区分可接管、证据不足与读取失败」/ 归档三态接口 | **删除** |

#### 代码落点

- 删除主体：`acquire/archive*.go`、`content_anchor.go`、`mapping_rebuild_evidence.go`（约 2656 行），
  `Pipeline.AssessArchive/ImportArchive`（约 600 行），`ingest/archive_budget.go` 与 `discoverSourceArchives`
  的实际调用段、`backfillArchiveUnderOwner/commitBackfill`
- 保留：`acquire/content_identity.go`（inode 复用防护与 tailer 活文件身份仍需要，且必须与历史数据
  用同一个 512 字节窗口才能逐字匹配）、`ImportArchivesNow` 空实现、`RunArchiveScan` 壳
- 落地提交：`6ff9ca49`

#### 不变的部分（明确保留）

- 采集账本、四水位、至少一次投递、`REQUEST_DONE` 回收上界与 UNKNOWN 不得被回收
- 物理/逻辑位置分离与指纹能力（ledger 侧保留）
- 缺口定界与自动消解（§3.3）、暂停源自愈
- WAL 纯内存队列与丢最旧语义（2026-10-29 修订）
- 「采集游标跨重启存活（`ingest.cursor`，不 fsync）」——这是内存架构下唯一的采集侧持久面

## 2026-10-22 修复契约与待验收项

本节保留本批修复约束；本轮代码修复及已覆盖回归按顶部声明收口，下文旧候选待验记录保留原时点、不冒当前全域通过；共享语义继承 [Shared Contracts](../worker-log-platform-contract/spec.md)。

- **物理与逻辑位置分离**：源分段同时保留逻辑起点与文件物理起点，续读以所属分段映射换算；指纹绑定源代次、分段身份、逻辑与物理采点、窗口长度及摘要。缺字段的旧状态保持“证据未知”，不得伪造匹配或把逻辑水位直接用于文件 Seek；重锚后的映射与指纹须经二启、三启保持一致。
- **持久提交资格是所有投递入口的前置门**：正常采集、暂停源排空、append 被拒后的排空必须等待对应批次的索引提交成功。WAL 内存中的 durable 标记不代表索引已提交；后台交办、`Incomplete`、提交失败或超时均不能外发该批事件，后续成功重试才可放行。
- **三种证据不能互相升级**：`Positions.Delivery` 仅是请求结果连续前缀；ACK 只能来自真实 `REQUEST_DONE`；`VerifiedRuns` 只能来自完整逐字段可见性核验。条数相同、最大位置、恢复责任转移或人工裁定均不生成核验凭据。自动缺口消解仍要求精确连续区间与原因允许名单，人工永久裁定另存审计。
- **读入前限制字节**：单行与单轮读取独立有界；活文件暂时 EOF 不能提交无换行的半行。UTF-8、CRLF 和跨轮续写必须保持正文与源字节位置完整；超长记录须受管隔离或背压，不能裸截断并跳过源责任。~~归档发现、导入和恢复不得递归占用同源操作门；归档状态区分可接管、证据不足与读取失败。~~ **2026-10-30 删**：归档采集整体删除（只读活文件），归档发现/导入/恢复与三态接口已一并下线。
- ~~**恢复责任先审计后摘引用**：`CLEANED` 记录先在锁外按有界批次持久归档并取得回执，再短锁复核状态、hold、消费者与候选身份；失败或并发变化则保留活动引用。~~ **2026-10-29 删**：该机制属于受管恢复分段，已随其废除（WAL 改纯内存后无副本需接管）；`recovery_audit` 表与 `RecoveryArchiveStore` 一并删除。
- **物化成本独立计账**：按源、UTC 日、稳定内容窗口预留并持久记录成本；部分写、ACK 未知及“写成功但发布 CAS 失效”均消耗实际或保守成本。耗尽只阻止再造副本，不阻止只读取证、合法复用、正常尾采与有界 WAL 排空。迁移冻结期必须先取得 Catalog 物理日写许可才可写入、核验和发布。

待验收：对应反例正式回归红转绿；旧 JSON/SQLite 与残留 WAL、~~归档、~~事件段环境的首启→二启→三启及回滚后再启；逐轮核对位置、指纹、缺口、审计与恢复责任。`SIGKILL` 恢复不等同真实断电耐久。

（**2026-10-30 改**：验收环境不再包含「残留归档」——归档采集已整体删除；此外索引库 `:memory:` 与事件段 tmpfs 已使「二启/三启逐字段一致」失去对象，重启后索引与段均不存在。）

### 当前恢复与回执合同（2026-10-24）

C1/026 保原 capture、完整 ACK 正文、current pipe/PP token、锁外归档身份及真实 RO receiver；owner ctx/原预算贯穿 proof，发布锁与新 permit 后复验截止及父 authority/token/target。混合 immutable 历史另证，活 WAL 子集仍须实际 SQL 资格；r028 每当前成员完整 Marshal/新 Body/FP，仅严格旧 complete-index 条件下省 SHA，歧义全部 fallback，新资格不提前发布。inline/ref 正文、局部 lease/hold 公平、条件回收及 Stop 真 join 均保持；细节与最新验收统一见 [共享契约](../worker-log-platform-contract/spec.md#2026-10-24-验证状态r028性能-hold)。

### durable outbox r4 接缝（本地候选已实现，联合验收中）

Worker 组合根内部 `Options.DeferredDelivery=true`，独立 Manager 默认 `false`、同步兼容；无新产品配置/RPC/schema/WAL 格式或预算。新事件仍先写原 WAL，有界待办只存 source-key。`CommitAndQualify/DeliverQualified` 和 `CommitIdentity` 将真实 actual-plan SQLite 完成资格与外发分开，身份包括 EventID、canonical hash、record 区间及完整正文摘要，generic nil/水位/旧 pipe 不替代本批资格。

实际 G 可以写 immutable 历史 X+活 WAL Y：`CaptureDeliverySubset` 捕获原 cut 下活动成员，Y 必须获实际 SQL 回执，X 则由固定历史行界与摘要重读证明；不要求全部正文都在当前 WAL，也不因 X 缺席而跳过 Y。written hint 保原 full actual-set 的 count/window/digest/cut/成员和历史证明，不存正文，RO 恢复重建完整集合后复核；候选级冲突走既有裁定，namespace 历史诊断不封禁健康源，真冲突留 WAL。

每源 guard 覆盖全入口，局部 lease/条件回收保留 hold 公平与人工审计出口，Stop 真 join 后关资源。~~归档采样前先 `ReserveAppend`，忙态无 sampler 副作用；~~ **2026-10-30 删**：归档采样已整体删除，`ReserveAppend` 无调用方。只对同对象/原游标 CAS 回退未接收部分并关 reader，已接收前缀及可提交新尾保留。自动重试由现有 SyncArchiveScan 接线驱动。准确协议及未知写/语义 Err 边界见 [共享契约](../worker-log-platform-contract/spec.md#durable-outbox-r4本地候选已实现联合验收中)。以下旧包测/双短仅属原 source，r4 的 ordinary/race、完整 Worker、Search 与正式性能仍待真实重绑，不标交付。

### 已落地领域字段与已冻结读取接口（集成验收中）

- `Segment.PhysicalStart`（`physical_start`）是逻辑 `StartPos` 对应的文件偏移；`PhysicalMappingKnown`（`physical_mapping_known`）区分真实 0 与旧状态缺证据。映射为 `PhysicalStart + (logicalPos - StartPos)`，越界不能推断匹配。`ResolveContentAnchor` 拒绝未知映射；旧非零 live 在 Poll/PrepareRotation 前稳定以 `PHYSICAL_MAPPING_UNKNOWN` 背压，旧有逻辑/身份的归档断点返回 `ArchiveUnknown`，不升级为已知 0，须外部归属证明或人工恢复。对应三条真实反例红转绿且 acquire/pipeline 包回归通过，不能据此关闭 Manager/SQLite 整链验收。
- `Entry.ConfirmedFingerprint` 绑定 `SegmentPath/SegmentStartPos/SegmentIdentityBytes/SegmentIdentitySHA256/LogicalPos/PhysicalOffset/WindowBytes/SHA256`；登记经 `SetConfirmedFingerprint` 核对现存分段映射与身份，相同证据幂等，旧平面摘要不会自动升级成绑定证据。文件替换时先 PrepareRotation 并校验旧凭据，再刷新确认线，不能先采样新文件再给旧段贴指纹；新增真实反例已红转绿。
- `RequestDoneRanges/DeliveryEvidence/FoldSpansUnknown` 字段已落地；本批最终证据契约要求真实请求完成 ACK 独立且最高，诊断按 State 精确归并并保留跨 State 重叠，UNKNOWN 优先旧 NOT_SENT，不能吞掉后续未知结果，也不从 Delivery/Reclaim 反造 ACK；混合态、相邻折叠、粗化失精度与 JSON 三启定向回归已过，完整长跑/SQLite 消费及最终门禁仍待。旧已裁且无证据的区间诚实为 no_evidence/UNKNOWN。任何折叠精度缺失（旧缺字段或超精确子区间上限后粗化）标记 `FoldSpansUnknown` 并让人工永久放弃整批拒绝，不能把凸包内健康字节标成丢失；旧粗化状态三启不能洗白为精确。
- 已冻结 `acquire.ReadLimits{MaxLineBytes, MaxBytesPerPoll}` 与 `pipeline.Options.ReadLimits`：零字段沿 `DefaultReadLimits()`，默认物理行 256KiB、每轮 4MiB，包含换行且独立于 WAL 限额；归并后每轮额度至少比单行上限多 1 字节。超长行以 `ReadLimitError`/`READ_LINE_TOO_LONG` 留可见责任，停在原行首，放宽配置后续读而非拆成普通事件；acquire/pipeline 同内容末轮正式定向、完整包 test/race/vet 均退出码 0。此采集/管道绿不代替 Manager 持久完成门、交办/Incomplete 零外发与整 Worker 联合验收。
- ~~归档三态接口冻结为 `ArchiveCovered/ArchiveTail/ArchiveUnknown`；发现层使用 `Pipeline.AssessArchive`，未知对象保留原物与旧身份并稳定隔离，不能仅用 `!Import` 把未知当“已覆盖”。~~ **2026-10-30 删**：归档采集整体删除（`6ff9ca49`），`AssessArchive`/`ImportArchive`/三态接口已下线。
- `Options.MaterializationLimits` 的 rebuild 内部默认为每稳定内容窗口 4 次、同源/UTC 日滚动 1h 内 8 次；本批另已批准 live_compaction 每窗口/小时各 4 次，所有 Class 共享默认 1GiB 小时 wire 硬顶，并在生产显式为必要压缩保留其中 1/4。类别/保留尚在实施复验，准确兼容与安全初始策略见 [索引规格 §2.8](../log-index-sqlite/spec.md#28-物化类别与必要压缩保留已批准实施复验中)。这不是采样/丢弃预算、全 Worker 磁盘/RSS 上限或性能最优值；端到端守门、正常尾采与重启重新资格证据仍待完成。
- `archiveCleanedRecovery` 每轮最多 `min(16, MaxRows, CycleMaxRows)` 条，超时/仓储失败保留活动责任；`RecoveryArchiveRecord` 携完整 Key/Ref/Fingerprint，回执含 Key/SegmentID/Fingerprint/非空 ArchiveID，`AcquireRecoveryUse` 的幂等释放跟踪实际消费者。实际 SQLite 归档调度及 hold 竞态复验尚未完成。
- 投递源码已消费 Catalog `AcquireWritePermit/ReleaseWritePermit`，许可目标用于选择固定 owner/目录客户端；完整写入→校验→发布的所有入口覆盖及冻结日零外写仍须联合证明。
- 新增交办后静默欠账修复：按源登记 `durableDeliveryRetry`，复用既有 poll 与有界 replay 排空器，每轮重新取得真实持久提交资格，不靠新输入、暂停或伪造缺口；按已有自愈间隔重试，旧观察不得清除新登记，未落库时仍零外发。766 条超过单次 500 条排空且随后无新输入的正式反例已红转绿。真实 60 源短验 r2 已无 400，但 quiet 后耐久交办欠账缺少重调暴露该问题；后续相同生产处理内容的双 60 源 30s 稳态/故障恢复短验已完成 64000 条逐 ID/hash/位置/SQL 责任排空。精确故障注入、固定 view 与真浏览器范围见 [联合短验证据](../worker-log-platform-contract/spec.md#2026-10-22-有限真实联合短验与剩余门禁)；这不是正式性能长窗或所有恢复场景完成。

## 1. 背景与目标

采集账本、源分段、WAL 四水位和至少一次投递。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：采集失败后的 UNKNOWN 保留由 FR-473 §4.2～§4.4 定义；WAL 合法回收后由「持久 position 水位 + 重启重读」恢复（**2026-10-29**：受管恢复分段已废除，原「从登记的恢复分段恢复」不再适用）。
- 范围内：FileTailer、STDIO_PRIMARY ~~和 ArchiveImporter~~ 统一写入事件管道；轮转通过 source_generation 关联；坏记录进入隔离队列；WAL 先 durable 再 delivery，按 FR-473 reclaim 证明回收。**2026-10-30 删 `ArchiveImporter`**：归档采集整体删除，只剩 FileTailer / STDIO_PRIMARY。
- 继承 FR-473：未知投递结果不进入坏记录隔离；UNKNOWN 区间不参与回收（回收上界是 `REQUEST_DONE` 证据的连续覆盖末端），因此它天然保留重投机会。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

FileTailer、STDIO_PRIMARY ~~和 ArchiveImporter~~ 统一写入事件管道；轮转通过 source_generation 关联；坏记录进入隔离队列；WAL 先 durable 再 delivery，按 FR-473 reclaim 证明回收。

（**2026-10-30 删 `ArchiveImporter`**：归档采集整体删除，见本文开头修订节。）

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- [ ] ~~轮转压缩不重复；损坏 gz/编码/权限/截断可见~~ **2026-10-30 删**：gzip 一半随归档采集删除（只读活文件），仅保留「编码/权限/截断可见」（针对活文件）；HTTP 2xx 不直接回收；容量满暂停并报告缺口。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-473 已冻结；本规格仍须完成 Worker WAL、恢复责任和 Runbook A 真机验收，未完成前保持开发中。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-476 资产审批冻结。

## 3.1 采集账本与投递边界

- `CreateInstanceRequest` 增量字段 19/20/21 为 `log_target_id`、`log_acquire_mode`、`log_source_generation`。CP 以实例 ID 下发授权 namespace，以实例 UUID 与 holder Worker UUID 构造稳定 generation；Java 实例默认 FILE_PRIMARY（工作目录 `logs/latest.log`），通用进程默认 STDIO_PRIMARY。旧 CP 省略字段时不推断数字目标 ID。
- Worker 的首次登记与 Resync 已存在实例路径均登记日志绑定；绑定未持久化成功不确认本次实例注册。`ingest.state.json` 保存采集配置和实例绑定，Worker 重启先恢复它们，不依赖 CP 当时在线。
- STDIO_PRIMARY 的 stdout/stderr 分别追加到 Worker 数据根下、由实例身份哈希命名的受管 Raw 文件，写入并 fsync 后才由常驻采集循环读取；文件尾部偏移沿用同一 WAL/账本恢复语义。FILE_PRIMARY 的 stdout 继续用于实时控制台，不再作为第二份持久事件输入。
- Raw 写入失败持久登记 `STDIO_RAW_WRITE_FAILED`、暂停采集并阻止 cutover；普通“投影已覆盖”不能消除此类可能已丢失的原始数据缺口，必须人工核验。Worker 已注册但无持久日志绑定的实例同样阻止 cutover。
- 自动化验证：`TestInstanceStdioPersistsRawBeforePollingAndRestoresBindings`、`TestInstanceFilePrimaryDoesNotDoubleCollectConsoleOutput`、`TestInstanceRegistrationAndResyncBindManagedLogs`。真实子进程与官方 VL 验证：`TestRealProcessStdoutAndStderrReachManagedVL`；该测试不替代 CP 浏览器创建实例、daemon 重启和轮转矩阵的远端验收。

- 不可变微投影发布兼容旧 journal 的单值 `projection_generation`：首次追加前先读取原已发布 generation，随后将原值与新值一起写入 `projection_generations`。只有明确的隔离全量重建才替换旧集合，不能因字段升级使历史事件从查询输入消失。回归：`TestAppendMicroProjectionPreservesLegacyPublishedGeneration`。
- 在读取 canonical 快照前捕获本源/本日发布版本，随计划进入短发布区复核；旧完成不得覆盖该源的新 scope，其他源的并发发布则基于当前 Catalog 合并。封闭水位只来自本次实际逐字段校验的写入面，追加可并入本源既有 closed 水位，Replace 不用实时 Durable 或段末端抬高未覆盖范围；VL IO、扫描与校验仍在共享锁外。
- 每次物理写入前保存 Catalog 权威快照，并通过 Catalog 条件提交原子发布 manifest 与封闭水位。迁移或并发发布先完成时，旧写入者不得覆盖新 owner/manifest；发布尝试只有通过本源快照接纳后才更新 generation/attempt/pending，在请求前持久化。写入完成仅在 attempt 仍匹配时清 pending，旧完成不能覆盖新尝试。响应丢失、发布冲突或进程重启后保留不确定状态，隔离代次重建或按下面的内容证据确认，不能仅凭 HTTP 成功清除。回归：`TestMicroGenerationLostResponseIsExcludedAndRestartCompacts`、`TestOwnerChangeDuringVerificationCannotPublishToNewOwner`、`TestProjectionWritersCannotOverwriteAnotherCommittedManifest`、`TestPR61OldReplayCompletionCannotClearNewPending`。
- **静默 pending 续修（RV-018）**：常驻对账区分在途尝试与遗留失败；无在途时不依赖新输入，按实际 UTC 日对已发布白名单取证。count 只检查额外行，仍须逐事件身份/哈希/正文/时间校验并确认 closed 覆盖；缺失日按重放 day/耗时预算续跑，已修日不重复写，未确认全部日不得提前清全源 pending。清除前复核源/日发布版本、Catalog authority/本源 token、attempt、无在途与 generation，任何变化维持 pending。回归：`TestPR61SilentPendingRetriesWithoutNewInput`、`TestPR61PendingExactCountRejectsWrongContents`、`TestPR61SilentPendingContinuesAcrossDayBudget`。
- **跨日压缩（RV-022）**：tail 快路径选择前检查本批事件实际 UTC 日对应的本源投影代次数（上限 64），不只检查源登记日。到限转 canonical 全量规划；单代 Replace 必须将该日完整 canonical 集写入新代，不能把差集写入面配上替换旧白名单，否则旧历史会消失。普通未到限追加路径仍保留增量成本。回归：`TestPR61CrossDayProjectionCompactionPreservesHistory`。
- **对账轮公平性（RV-019）**：候选枚举不提前推进处理游标；每个源实际处理后才推进一格，预算取消时下轮接第一个未处理源。原生产逻辑保持，本轮 `TestPR61ReconcileActualRoundsVisitEverySource` 用 8/16 源真实多轮与视图/回退两条路径验证，不以取候选次数代替处理证据。

- ~~FileTailer、STDIO_PRIMARY、ArchiveImporter 共享同一 `(log_source_id, source_generation)` 账本；`latest.log → 轮转 → .gz` 是同一逻辑源的连续分段，`.gz` 接管前必须登记前段结束位置，禁止按压缩包 hash 重新导入整段。~~ **2026-10-30 改**：账本仍由 FileTailer / STDIO_PRIMARY 共享，但 `.gz` 不再回读——轮转后直接继续读新文件，「`.gz` 接管前必须登记前段结束位置」这条要求随之失效。
- `source=worker` 的 Worker/Node 自身日志也进入同一采集面；采集失败不能递归写回自身 VL 失败日志。
- ~~FILE_PRIMARY 常驻循环自动发现同目录 `.gz`（可用 `archive_glob` 收窄），以文件名 UTC 日锚定归一化时间。~~ **2026-10-30 删**：只读活文件，轮转后的 `.gz` / `.log.N` 不再回读；`archive_glob` 键保留但不再驱动任何采集。原文其余部分（同路径替换先关闭旧分段 EOF、由 ArchiveImporter 跳过已 durable 前缀再给新 `latest.log` 分配位置）随归档链路整体下线。
- ~~gzip 只有在事件已由 durable WAL 覆盖后才标记 `imported`。损坏或不可读对象登记可见缺口及观测 size/mtime，未变化前保持隔离、不在每轮轮询重复制造缺口；对象变化后才允许受控重试。**截断对象**（已读出行之后，下一次读取以零字节 + `unexpected EOF` 收尾）同样必须留 `ARCHIVE_READ_ERROR` 缺口，已读出的完整行仍计入——不得以静默 `break` 丢尾。~~ **2026-10-30 删**：不再有归档导入，`ARCHIVE_*` 缺口不再产生（既有历史缺口仍可读取与人工裁定）。原回归 `TestManagerAutoImportsHistoricalGzipBeforeCurrentFile`、`TestManagerRotationImportsOnlyUnreadTailBeforeReplacementFile`、`TestManagerQuarantinesUnchangedCorruptGzipWithoutRepeatingGap`、`TestArchiveTruncatedGzipRecordsGap` 已随实现删除。
- HTTP 2xx 只写入 `REQUEST_DONE`；**回收上界只看 `REQUEST_DONE` 的连续覆盖**（不只看 2xx，也不看 `delivery_position`——后者含 UNKNOWN）。响应丢失进入 `UNKNOWN`，按 event_id/位置核验或重投。
- Multiline 缓冲未形成完整事件时只能推进 `read_position`，不得推进 durable checkpoint；崩溃后从首行位置恢复拼接。
- ~~WAL、隔离队列或受管恢复分段达到预算时暂停低优先级采集~~ ⇒ **2026-10-29 改**：WAL 是**纯内存队列**，越界不再暂停而是**丢弃最旧**（对齐 Fluent Bit `memrb`），并以缺口与 `DropStats` 双维如实登记。隔离队列仍按原语义暂停。

## 3.2 失败验收

空闲采集轮询不得重写/同步全部历史状态；仅位置变化、新事件或错误需要检查持久化。测量“本批 durable 提交延迟”必须核对持久账本已覆盖该批完整事件的结束位置，不能仅观察状态文件 mtime。自动回归：`TestIdlePollDoesNotRewriteDurableHistory`。

| 场景 | 必须结果 |
|---|---|
| ~~latest.log 轮转后 ArchiveImporter 介入~~ | ~~同一逻辑源不重复计数，账本连续~~ **2026-10-30 删**：归档导入已整体删除（只读活文件），该场景不再发生 |
| 轮转后继续读新活文件 | **2026-10-30 新口径**：同一 source_generation 继续推进；轮转期间被压缩掉的字节不再回读（显式接受丢失） |
| VL 写入成功但 ACK 丢失 | UNKNOWN/核验/重放可审计，未确认数据仍有恢复来源 |
| ~~损坏 gz（含中途截断）、编码错误、权限不足~~ | ~~隔离记录、缺口可见、后续源不被拖死~~ **2026-10-30 改**：gz 部分随归档采集删除；活文件的编码错误/权限不足/截断仍必须缺口可见、后续源不被拖死 |
| WAL/临时盘满 | 暂停或降级，实例管理继续可用，恢复后可继续推进 |

## 3.3 缺口定界与自动消解（2026-10-02 缺陷 A 加固）

缺口（`Gap`）语义恒为「这段可能没落库、需要补」；本节只做**保守方向**的事——区间只扩大不缩小，
绝不产生「已确认落库」的假象。消解（标记 `resolved`）必须由外部证据驱动，证据只有两类。

- **登记合并**：同因、相邻/重叠（间隔 ≤1 字节的源分隔符）的缺口并成一条覆盖区间；单次登记在尾部
  有限窗口内回溯，代价与缺口总量无关。连续失败因此在账本上恒为常数条（现场 5 万级缺口正是这样涨起来的）。
- **数量级上限**：单源未解决缺口按**原因**分组，每组 ≤ `DefaultMaxUnresolvedGapsPerSource`（64）；
  越界时把该原因最旧的一批折叠为一条覆盖区间——**原因之间绝不合并**，否则会丢掉按原因判定的语义
  （例：`STDIO_RAW_WRITE_FAILED` 不可由投影自动消解）。已解决缺口滚动保留最近
  `DefaultMaxResolvedGapsPerSource`（64）条。观测：`Ledger.GapObservability` 暴露
  Unresolved/Resolved/MergedTotal/FoldedTotal，`Gap.Detail` 记录合并与折叠摘要。折叠基于同一原始切片
  计算各原因的离散 victim，一次重建；只替换/移除同因成员，保留夹间其他原因（含永久洞）、已解决审计与登记顺序，不能因前一桶重写使后一桶下标失效。
- **自动消解（两条出口，缺一不可）**：两条出口共用**同一判据**（`ledger.ResolveGapsCoveredByRanges`）：
  **原因允许名单（默认拒绝）+ 区间连续覆盖**。
  1. **投递成功覆盖**：一次投递成功（写 VL + 逐字段可见性校验 + catalog 发布）后，消解**完全落在**
     本次实际写入面**某一段连续区间**内、且原因在允许名单里的未解决缺口。
  2. **已发布投影覆盖**：对仍持有缺口的源，用 `publishedClosedForSource` 的发布证据 + 该源
     **逐字段校验覆盖区间凭据**（`persistedSource.VerifiedRuns`：每次成功投递把校验通过的区间
     合并持久化）消解，覆盖「WAL 条目已被回收、无法再重投」的尾部。该路径较贵（按天聚合事件区间），
     按源限频（默认 10s，`Manager.SetSelfHealInterval` 可注入）。
  **为什么不能用凸包/末端水位**（2026-10-02 复审 P1-1/P1-2）：`[min(Record.Start), max(Record.End)]`
  凸包与「缺口末端 ≤ closed」都会把**从未落库**的区间算成已覆盖——`APPEND_REJECTED`（append 被拒，
  该批从未进 WAL）、容量门禁暂停、`ARCHIVE_*` / `ROTATED_SEGMENT_NOT_READY` 之类的合成缺口
  （位置为 `(0,0)`）都可能因此被标成「已确认落库」，空洞被永久掩盖并放行 `ResumeAcquire`；
  `replay=true` 时证据被扩为全量 canonical 集，凸包覆盖整段历史，风险最大。
  **允许名单**（事件确实在 WAL/段里、本次会被重写并逐字段校验）：`DELIVER_ERROR`、
  `DELIVER_ERROR_WORKER_SOURCE`、`WAL_COMMIT_FAILED`；名单之外一律不自动消解（含
  `STDIO_RAW_WRITE_FAILED` 与一切「从未进 WAL」的原因），交重读补投或人工核验。
  人工接口 `ResolveGapsThrough` / `ResolveCoveredGapsForSource` 语义不变（仍按末端消解，
  可显式确认放弃不可证明的原因）。永久放弃凭据按实际连续区间并集分别记录，只合并相接/重叠项，
  不以首尾凸包覆盖健康位置；实名操作人、理由、时间、Through 与最近 64 条上限及持久化语义保持。
  回收只按 §4.3 新规则（`REQUEST_DONE` 连续覆盖末端）判定（**2026-10-29**：原「匹配恢复段 `[from, CoversTo)` 的半开相交」已随受管恢复分段废除；`Abandonments` 仍作为人工裁定凭据保留，供 `event_status` 判 ABANDONED 与 WAL 剪枝使用）。
- **恢复分类不等于落库证据**：积压回落时可将 `APPEND_REJECTED` 与
  `DELIVER_ERROR/DELIVER_ERROR_WORKER_SOURCE` 分账分类为待重读，必须使用各自明确允许集；
  末端水位只作候选扫描上界，独立 found 标志支持零位置。未知、~~归档、~~Raw 写失败、WAL 提交失败不得顺带消解。
  （**2026-10-30**：`ARCHIVE_*` 已不再产生，但历史残留的归档缺口仍按原规则「不自动消解」，仍不能被前端水位洗白。）
  原 `ResolveGapsThroughExcept` 语义不变；待重读分类不能扩大上述终局自动消解名单。
- **暂停源自愈**：采集暂停期间 FileTailer 拒绝读取，「读 → 投递 → 回收 → 恢复评估」整条链没有触发点
  （2026-10-01/02 现场：10 个源 `paused=1`、各带 50,863–50,864 条未解决缺口、持续 13+ 小时零新数据，
  最终靠人工按源解算 13 个源才恢复）。采集轮现在对「已暂停或仍持有缺口」的源执行限频自愈：
  `DeliverPending`（只投递、不读取）→ 登记投递结果 → 推进回收并按滞回条件评估恢复；
  `ResumeAcquire` 仍要求「零未解决缺口」，门禁不放松。

自动回归（转红实测见 CHANGELOG [Unreleased]）：
`ledger.TestGapStormMergesAdjacentSameReasonFailures`（相邻合并）、
`ledger.TestGapStormStaysBoundedAcrossScatteredFailures`（有界）、
`ledger.TestGapFoldKeepsReasonsSeparate`（按原因折叠）、
`ledger.TestResolveGapsCoveredByRangeRequiresFullCoverage`（区间包含判据）、
`ledger.TestResolveGapsCoveredByRangesRejectsConvexHull`（连续覆盖 ≠ 凸包）、
`ledger.TestResolveGapsCoveredByRangesIsAllowlist`（原因允许名单 / 空名单必须报错）、
`ledger.TestMergePositionRangesBridgesSingleSeparator`、
`ingest.TestAppendRejectedGapInsideDeliveryHullIsNeverAutoResolved`（凸包内部的未落库批次不得被消解）、
`ingest.TestDeliveryEvidenceRequiresContiguousCoverage`、
`ingest.TestPublishedProjectionGapResolutionNeedsVerifiedRuns`（末端水位 + 凭据前置）、
`ledger.TestResolvedGapsAreTrimmedButAuditTailKept`、
`acquire.TestPausedSourceSelfHealsAfterDeliveryRecovers`、
`acquire.TestEvaluateResumeDoesNotClearForeignPause`、
`ingest.TestDeliveryFailureGapsAutoResolveAfterDeliveryRecovers`、
`ingest.TestPausedSourceSelfHealsEndToEnd`、
`ingest.TestStdioRawWriteFailureGapIsNeverAutoResolved`。
