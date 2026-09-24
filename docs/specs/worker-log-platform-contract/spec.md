# 功能规格：Worker 日志平台 Shared Contracts（FR-472）

> 状态：已冻结（Shared Contracts 基线）　·　关联 PRD：FR-472　·　分支：feature/fr-log-platform-foundation
>
> 本规格是 FR-472～483 的开发前闸门。它定义正式版契约基线；后续发现问题允许通过显式评审修订，但必须同步依赖模块、兼容规则和测试。未通过本规格的验收，不得按模块全面开发 Lifecycle、Archive 或 CP 联邦。

## 1. 背景与目标

现有日志查询以 CP `logs` 表为中心，无法提供 Worker 本地持久化、分层生命周期、跨 Worker 覆盖状态和可解释失败语义。FR-472 定义所有后续 FR 共用的身份、投递、查询、权限、资源和生命周期边界。

目标是得到可实现、可恢复、可测试的正式版契约，尤其保证：

- 重放、响应丢失和 VL 重启不会让系统在没有恢复证据时回收唯一 WAL；
- 多页查询、统计和导出使用真实固定视图，而不是只把排序键包装成 Cursor；
- Catalog 是查询和写入唯一权威，Worker 重启先恢复 Catalog/journal 再开放受影响范围；
- partial、离线、归档未恢复、权限变化和资源耗尽都能被调用方区分。

## 2. 范围与术语

### 2.1 范围内

- 事件身份、源分段、采集账本和投递重复语义；
- `read_position`、`durable_position`、`delivery_position`、`reclaim_position` 四水位及 WAL 回收；
- Partition Catalog、generation、查询租约和启动恢复；
- Query View、Cursor、Search/Stats/Fields/Facets/Tail/Rehydrate/Export 的共用语义；
- 权限 scope、能力协商、配置合法组合和 Worker 日志资源预算；
- 将治理例外同步到 ADR、`.claude/rules/architecture-invariants.md`、`decision-alignment.md` 与 ARCHITECTURE。

### 2.2 不做

- 新增或重构日志关键字告警引擎；现有告警行为必须继续有效；
- ClickHouse、Loki、RemoteStore、外部采集器或浏览器直连 Worker/VL；
- CP 长期保存新 Worker/Node 查询日志；允许有预算、期限、权限控制的临时导出产物，但它不是日志查询库或新的保留层；
- 本规格不承担最终发行资产打包审批，但必须登记契约能力验证所依据的具体 VL tag/构建标识、实验结果和不支持项；FR-475 负责发行资产、哈希、许可、分发和运行时兼容审批。FR-475 使用其他版本时，必须重新完成受影响的能力验证。

## 3. 共享数据模型

### 3.1 事件身份

每条标准事件必须携带以下不可变身份；归档关联是可选 provenance，不参与事件身份生成：

| 字段 | 规则 |
|---|---|
| `log_source_id` | Worker 内逻辑日志源 UUID；实例、Worker 自身和独立 STDIO 源分别登记，跨 Worker 查询保持原值 |
| `source_generation` | 仅在源内容确认重置、截断或新建源时递增；正常重启续读、轮转/改名/压缩接管沿用被接管内容的 generation |
| `record_start` / `record_end` | generation 内完整事件覆盖的源位置范围；Multiline 事件覆盖从首行到末行，checkpoint 使用 `record_end` |
| `event_id` | `hash(log_source_id, source_generation, record_start, record_end, parser_version)`；用于账本对账，不要求 VL 原生唯一约束 |
| `archive_object_id` | 可选归档 provenance；归档生成或对象重试不得改变 event_id |
| `event_time` / `ingest_time` | 分别为日志语义时间和 Worker 接收时间；查询时间范围默认使用 `event_time` |
| `parser_version` | 源分段绑定的固定解析版本；正常重放和接管不得悄悄更换 |

同一源重新解析属于新的 projection/dataset generation，必须通过受控替换生效，不能把新旧投影同时加入默认逻辑集合。`message`、`_time`、`event_id` 不得进入 `stream_fields`；高基数字段只作为结构化字段，不得被隐式加入流身份。

### 3.2 采集账本

账本按 `(log_source_id, source_generation)` 保存源分段、起止位置、归一化版本、最近四水位、错误计数、缺口和归档关联。Tail 与 ArchiveImporter 必须更新同一账本；`latest.log` 轮转到压缩文件时，轮转记录建立 `source_generation` 关联后才允许 ArchiveImporter 接管。

## 4. 投递、WAL 与回收契约

### 4.1 水位与批次状态

位置和状态分开保存；乱序响应不得用最大位置越过未解决空洞。

| 字段 | 含义 | 规则 |
|---|---|---|
| `read_position` | 运行时采集读指针 | 可在崩溃后从账本重建并回退；不属于单调持久化水位 |
| `durable_position` | 标准事件和账本已写入本地 WAL 且完成 fsync/等效提交 | 同一 generation 内单调前进 |
| `delivery_position` | 已有请求级结果的批次边界 | 伴随 `delivery_state`（NOT_SENT/UNKNOWN/REQUEST_DONE/REPLAY_REQUIRED），不能赋值为 UNKNOWN |
| `reclaim_position` | 之前的 WAL 前缀已由其他可靠来源承担恢复责任 | 只按 §4.3 的回收证明单调前进 |

### 4.2 投递语义与可用证据

正式版基线采用“至少一次投递 + 可审计重复对账”。Worker 为每条事件维护本地 `validation_state`、`delivery_state` 和 `verification_state`；三者独立。

| 证据 | 能说明什么 | 不能说明什么 |
|---|---|---|
| Worker 本地校验通过 | 序列化、字段和时间检查通过 | VL 已接受或已耐久 |
| HTTP 请求成功 | 请求完成 | 所有记录逐条入库、持久落盘或可恢复 |
| 按 event_id 查询核验成功 | 核验时对应事件可见 | 后续掉电仍可恢复、没有重复 |
| 受管恢复分段提交成功 | Worker 有可定位、可校验的恢复来源 | VL 当前索引完整或统计无重复 |

选定的 JSON stream 请求级接口支持请求结果；本次能力基线不得假定原生逐事件接受回执或批次耐久提交回执。非法 JSON 行、请求级错误和未知结果分别进入逐事件状态；服务端全局日志/指标不能伪装成该批次逐记录结果。`UNKNOWN` 不得直接进入坏记录隔离，必须保留恢复或核验责任。

### 4.3 `reclaim_position` 推进规则

默认采用“受管恢复副本证明”，不把日志可靠性建立在尚未验证的 VL 批次耐久回执上。Worker 仅在下列条件全部成立时推进并持久化 `reclaim_position`：

1. 对应 WAL 前缀已生成可重放的受管恢复分段，包含标准事件或原始数据、源身份/generation、完整源位置范围、parser_version、首次 `ingest_time`、event_id 及不可变字段；
2. 恢复分段内容已完成持久提交、校验、登记，账本已持久化引用和 retention 约束；
3. 恢复责任覆盖所有未确认事件，即使 VL 尚未核验；事件状态可保持 `PENDING_VERIFY`/`REPLAY_REQUIRED`，但不能失去恢复来源；
4. 当前 Catalog owner/generation 只需仍能定位有效恢复责任；不要求物理 owner 永远等于原投递目标，迁移后的 COLD/Raw/下一层可承担责任；
5. 回收不会违反恢复分段预算、保留期或正在进行的恢复/迁移租约。

`/internal/force_flush` 只能作为可查询性实验或可见性核验手段，不能单独作为耐久提交凭证。WAL 回收后不等于事件投递完成；下游核验仍由账本状态驱动。恢复分段在所有未解除责任前不得按普通 Raw 到期规则删除。

#### 受管恢复分段责任状态

恢复分段采用规范事件格式（事件内容、源身份/位置、parser_version、event_id、ingest_time）和 manifest；原始 `.log/.gz` 作为原始证据关联，不作为唯一稳定重建来源。生命周期状态与释放证明分开：

`STAGED → DURABLE_VERIFIED → WAL_RESPONSIBILITY_TRANSFERRED → RELEASED → CLEANED`。

进入 `RELEASED` 前，账本必须持久记录本次采用的一个 `release_reason` 和责任接收者；多个释放证明可以同时成立，但不因此拒绝释放，也不能把未选用的证明当作清理前提：

- `PROJECTION_BACKED`：对应规范事件已经由完整、持久、可定位且符合故障模型的 canonical projection 承担恢复责任；仅有 manifest、成功查询结果或“已发布”标记不成立，且证明不能引用即将删除的分段本身；
- `NEXT_COPY_VERIFIED`：另一份受管副本已经接收恢复责任，完成内容/manifest 校验，并登记保留约束、定位信息和独立清理责任；两份副本不得互相以“对方仍在”为唯一证明；
- `RETENTION_EXPIRED_WITHOUT_HOLDS`：产品保留期已结束，且不存在有效查询租约、恢复任务、迁移、合规或其他保留约束。

`WAL_RESPONSIBILITY_TRANSFERRED`：分段内容已持久提交、校验登记，账本已 fsync 写入引用和保留约束；此时才允许推进对应 `reclaim_position`。`RELEASED` 表示恢复责任已经解除或转移，`CLEANED` 才表示实际文件清理完成。预算不足、证明不完整或仍有 hold 时保留分段并进入降级，后台清理不得自行推断释放理由。

该链条只覆盖同盘进程崩溃；整盘或整机损失需要独立副本/归档证据，不能由本地 WAL+Raw 推导零损失。

### 4.4 崩溃状态转换

| 场景 | 状态 | 动作 |
|---|---|---|
| 请求前崩溃 | `delivery_state=NOT_SENT` | 按持久账本定位事件，从尚存 WAL 或已登记的受管恢复分段恢复；原 WAL 合法回收不构成恢复失败 |
| 请求已发出、响应丢失 | `delivery_state=UNKNOWN` | 保留恢复分段；按 event_id/范围核验，无法确认则重放并记录可能重复 |
| 请求完成但本地账本未提交 | 若存在持久证据则 `REQUEST_DONE` + `verification_state=UNKNOWN`；仅存在崩溃前内存则 `UNKNOWN` | 先核验/补账本；不得根据原执行阶段补写成功状态 |
| VL 在确认窗口内重启 | `REPLAY_REQUIRED` | 受影响 view 失效或暂停；保留恢复来源，健康恢复后再核验 |
| WAL/恢复预算即将耗尽 | `DEGRADED_STORAGE` | 暂停低优先级采集或切换受控 Raw；暴露缺口和人工动作，禁止静默删除未确认前缀 |

### 4.5 逻辑事件投影与重复对账

正式版选择**分区级 canonical projection**作为默认执行方案。物理投递区只承载接收和核验；QueryPlanner 不直接把物理投递区作为 Search、Stats、Facets、Export 的输入。每个 `(storage_namespace, utc_day, generation)` 由投影器按 `event_id` 建立 canonical event stream 和 manifest：

1. 相同 `event_id` 且标准内容相同：只写入一个 canonical 事件，物理副本计入 `duplicate_count`；
2. 相同 `event_id` 内容不同：写入冲突记录并标记 `IDENTITY_CONFLICT`，该逻辑事件不进入 `exact` 结果；
3. canonical projection 未完成或重复未解决：Query View 的 `duplicate_quality` 为 `UNRESOLVED`，Search/Stats/Facets/Export 返回 `PARTIAL`，Export 不发布成功附件；
4. 投影器按账本和受管恢复分段增量更新，崩溃后从 manifest/checkpoint 重建；canonical projection 的磁盘、内存和重建预算单独计入 Worker 日志总预算。

因此 Search、Stats、Facets 和 Export 共享同一 canonical manifest/version，不允许 Search 临时合并而 Stats 直接统计物理记录。分页排序键为 `(event_time DESC, log_source_id ASC, source_generation ASC, record_start DESC, record_end DESC, event_id ASC)`；canonical stream 中 `event_id` 唯一，物理重放不会在页边界漏掉或重复返回。canonical projection 是受管 VL 分区/事件投影，不是 Worker SQLite 上的第二套全文检索数据库；SQLite 只保存 manifest、checkpoint、冲突和位置元数据。

实现对账补充（2026-09-23）：共享 namespace/UTC 日的 manifest 使用 `source_projections[]` 将 `(log_source_id, source_generation)`、该源已发布的微 generation 列表和该源封闭水位绑定。查询输入为这些完整三元组的 OR，不得把全部源和全部 generation 分别 OR 后求笛卡尔组合，否则其他源的不确定 generation 可能漏入。追加/压缩一个源时保留其他源；manifest/version 对整个绑定列表计算摘要，源集合变化必须使旧 View 失效。旧 journal 的单值/列表 generation 按已登记源转换，保持兼容；当前源的回收与切换核验不能借用另一源更大的水位。

## 5. Catalog、迁移和启动恢复

### 5.1 Catalog 记录

每个 `(storage_namespace, utc_day)` 维护唯一 owner、`generation`、物理位置、状态、写入水位、迁移 journal、最后校验和租约记录。租约记录包含 `lease_id`、归属 `view_id`、generation、创建/到期时间、失效原因和持有者；租约计数是派生值。物理目录扫描结果只是输入，不能自行成为权威。

每个 `log_source_id/source_generation` 维护 Worker 本地单调 `ingest_seq`。`closed_visible_seq` 是从最小未解决接纳序号开始的连续前缀：事件必须已经进入 canonical projection，或已明确隔离/拒绝并计入 coverage；存在 UNKNOWN、待核验或投影空洞时不得越过。它由账本事务推进并写入 Catalog/journal，重启时按账本、projection manifest 和隔离队列重建，不能取最大已发送/已查询序号。跨 Worker Query View 保存各目标的 `closed_visible_seq` 向量。

`PublishedProjection` 是一次 Catalog 持久提交发布的不可变对象，至少包含 projection manifest/version、covered source generations、`closed_visible_seq` 向量、coverage/conflict summary、query location/generation reference。数据文件和索引先完成准备与核验，再原子发布该对象；读取方不能分别取“最新 manifest”和“最新水位”自行组合。重放沿用原 `ingest_seq`，不重新分配接纳序号；空洞只阻塞所属源分段，其他源可独立推进；空洞解决且对应 projection 发布后，原源前缀才继续推进。

实现对账补充（2026-09-23）：投影发布必须以物理写入前观察到的 Catalog journal 序号、owner、generation、写入路由和 manifest 为条件执行一次原子提交。核验期间若迁移或其他投影发布者已经推进 Catalog，旧写入者保留恢复责任并隔离重试，不得把旧结果挂到新 owner，也不得覆盖先提交的 manifest。投影请求发出前持久登记 `publication_pending`；写入或发布任一步骤结果不确定时，该标记保持，后续批次与重启都必须从受管事件集合构建新的隔离 generation，核验并发布后才能解除。

### 5.2 迁移状态

`ROUTING_FROZEN → DRAINING → SNAPSHOTTING → STAGING_VERIFY → ATTACHED_STAGING → OWNER_SWITCHED → QUERY_LEASE_DRAINING → DETACHED → CLEANED`。

- `ATTACHED_STAGING` 的副本可被 VL 挂载，但 QueryPlanner 必须排除它；
- `OWNER_SWITCHED` 是唯一改变查询/写入权威的原子 Catalog 提交；
- `DETACHED` 不等于完成，目录残留、journal 和校验未完成时仍是恢复态；
- 迟到事件按 Catalog owner/generation 路由到 COLD、迁移暂存或受控补录，不按 `now-7d` 猜测。

### 5.3 启动恢复顺序

1. 读取并校验本地 Catalog 与迁移 journal；
2. 将未完成 journal 重放到幂等的逻辑状态，标出冲突 generation；
3. 对照实际目录、VL partition list 和 manifest，建立“可查询/可写入/不可用”范围；
4. 只为 Catalog 选定的 owner 建立 QueryPlanner 路由；残留旧目录即使被 VL 自动 attach，也不得加入查询；
5. 受影响范围进入 `RECOVERY_REQUIRED` 或 `PARTIAL`，返回缺口和恢复动作；其他范围才开放服务。

VL 进程健康、分区恢复完成、查询范围完整可用是三个独立状态。不能用目录存在或 HTTP health 代替 Catalog 一致性。

## 6. 固定查询视图与 RPC 共用语义

### 6.1 Query View

未携带 `view_id` 的首次请求创建 `query_view`，携带有效 `view_id` 的请求复用既有基础事件集合。Search、Stats、Facets 和 Export 可以执行不同结果变换，但不得因此重新选择最新数据集合。View 包含：

- 规范化筛选、绝对 UTC 时间范围和稳定复合排序；
- 固定授权目标集合（或用户显式选择的“仅在线”集合）；
- 每个目标的 `closed_visible_seq`、Catalog `generation`、owner 和读取租约；跨 Worker view 是各目标固定读取位置组成的向量，不承诺所有 Worker 同一物理时刻的全局事务快照；
- 创建时的覆盖状态、能力版本和视图过期时间；
- `view_id`、签名版本和失效原因。

后续 Cursor 只能引用该 view。`closed_visible_seq` 是不含可见性空洞的封闭前缀：此前所有接纳位置都有明确处理结果，明确隔离/排除的记录仍计入 coverage。服务端必须以该前缀或等效不可变投影约束每一页，不能重新查询动态最新集合。Cursor 只携带 `(view_id, logical_sort_key, page_limit)`，不把 watermark 当作未执行的提示字段。

### 6.2 视图失效

- 权限在每次请求和下载时重新校验；权限撤销优先于固定数据集；
- 数据集合变化（重建、重新解析、补录、删除）破坏 `closed_visible_seq` 时，返回 `VIEW_STALE`；纯迁移只要旧租约仍能读取原固定集合，不自动失效；
- Rehydrate 完成后不得悄悄把 DEEP 数据加入旧 view；可等待并创建新 view，或使旧 Cursor 明确失效；
- view 到期、取消或超过资源预算时返回明确状态，不把 `items=[]` 当作完整零结果。

### 6.3 响应状态

所有查询响应必须包含 `view_id`、`complete`、`partial_reasons[]`、目标覆盖摘要、排序版本、`closed_visible_seq` 向量摘要、取消/预算状态，以及独立的 `duplicate_quality`、`stats_quality` 和 `enumeration_state`；完整性、重复质量、统计质量和枚举结束不是同一维度。稳定排序为 `(event_time DESC, log_source_id ASC, source_generation ASC, record_start DESC, record_end DESC, event_id ASC)`，不得使用本地自增 ID 作为跨 Worker 唯一排序。

Tail 使用显式 `FOLLOW_LIVE`（动态流）或 `VIEW_BOUNDED`（固定视图尾部）语义；两者不可混称。

### 6.4 RPC 最小消息

```protobuf
message AbsoluteTimeRange { string from_utc = 1; string to_utc = 2; }
message AuthorizedTargets {
  repeated string target_ids = 1; // CP 计算后的授权集合
  bool online_only = 2;           // 仅在线必须显式选择
}
message QueryBudget { uint32 limit = 1; uint64 max_bytes = 2; uint32 timeout_ms = 3; }
enum TailMode { FOLLOW_LIVE = 0; VIEW_BOUNDED = 1; }
message QueryViewRef { string view_id = 1; string cursor = 2; string order_version = 3; }
message Coverage {
  bool complete = 1;
  repeated string partial_reasons = 2;
  repeated TargetCoverage targets = 3;
  string enumeration_state = 4; // OPEN/EXHAUSTED/STALE/CANCELLED
}
message Quality { string duplicate_quality = 1; string stats_quality = 2; }
message SearchResponse {
  QueryViewRef view = 1; Coverage coverage = 2; Quality quality = 3;
  repeated LogEvent items = 4; string next_cursor = 5; bool exhausted = 6;
}
```

请求必须携带绝对 UTC 时间范围、授权目标集合、规范化筛选、`QueryBudget` 和取消上下文；Stats 的 `count/sum/min/max/avg` 必须声明输入是逻辑事件集合，AVG 以 `sum/count` 二次聚合。Facets 只能对受控维度给出 exact；高基数维度必须返回截断标记、上限和覆盖状态。`limit` 只限制返回条数，不等于查询成本上限。最终字段编号、错误枚举、分页上限、Stats/Facets 合并字段和旧 Worker 能力协商在 `proto/worker.proto` 中冻结；此处语义先于字段名。

Export 是 CP Coordinator 的业务操作，不新增 Worker Export RPC：CP 使用同一 `QueryViewRef` 分页调用 `LogSearch`，完成有界物化、逻辑事件集合校验和下载权限复核后才发布临时产物。老 Worker 对新增 Log RPC 返回 `Unimplemented` 时，CP 必须标记 `LOG_UNSUPPORTED`/not-ready，不得把空响应当作完整结果。

### 6.5 配置组合、stream_fields 与能力协商

- 合法组合只有 `HOT-only`、`HOT+COLD`、`HOT+DEEP`、`HOT+COLD+DEEP`；COLD 关闭且 Archive 开启时必须显式选择 Raw-only 或 rehydrate 策略。
- `stream_fields` 白名单固定为低基数路由字段（如 `worker_id`、`instance_id`、`log_source_id`、`source_generation`、`level`、`stream`）；不得包含 `event_id`、`message`、`_time` 或任意用户字段。
- 能力协商必须返回协议版本、支持的 QueryView/Quality/Coverage/Tail/Export 能力、最大 limit、最大请求字节和取消语义；不支持能力不得静默降级为完整结果。
- Worker 资源总预算覆盖 WAL、HOT、COLD、staging、Rehydrate、canonical projection、查询并发、临时导出空间和 RSS；`512MiB` 只代表 HOT cache 模板。

### 6.6 初始工程性能判据

冻结基线采用可复现的工程门禁，后续只能通过显式评审修订：

- 环境：linux-amd64/windows-amd64，4 vCPU、8GiB RAM、SSD/NVMe 数据盘；记录文件系统和 VL build_id。
- 负载：10,000 条规范事件（约 1.45MiB）作为组件基线；持续 30 分钟同时采集、投影、迁移模拟和查询，查询并发 8、Search limit 200；发布验收再增加真实多 Worker 并发压测。
- 延迟：Search p95 ≤ 250ms、p99 ≤ 500ms；单批 1,000 事件的本地 durable/账本提交 p95 ≤ 1s。
- 资源：单 Worker 日志数据面 RSS ≤ 1GiB（不含文件系统 page cache）；WAL+staging+临时 Export 预留不得超过总日志预算的 25%；磁盘使用达到 80% 进入降级，达到 90% 必须暂停不可恢复写入。
  - **口径（2026-09-24 实测确定）**：「日志数据面」= **Worker 进程日志部分（进程 RSS − 非日志基线）+ 受管 VL 进程 RSS 之和**。实测基线：Worker 非日志基线 ≈200MiB。
  - 实测（每源 3000 事件）：Worker ≈24MiB/源、VL ≈80–95MiB；64 源稳态 Worker ≈674MiB、VL ≈76MiB（**合计 ≈750MiB < 1GiB，达标**）；30 分钟 8 并发压测 Worker 峰值 857MiB（含查询堆高水位，GC 后回落）。按此口径 **1GiB ≈ 40 源**。
  - **修订（2026-09-24，FR-483）**：上述 674MiB/24MiB-per-源 是「canonical 事件体常驻内存 + 内联于 state」时的数字。FR-483 把事件体改为**落盘 compact**（语义不变，见 ADR-095）并加界三处跨轮无界累积结构后，稳态 Worker 降至 **64/128/150 源 = 22.5/22.3/20.0MiB**，采集期峰值 **56.0/62.0/61.8MiB**（真机 + 真 VL v1.52.0），二者都**几乎不随源数增长**（常驻量与源数量而非事件总量相关）；`state.json` 由 77 803KiB 降至 139.5KiB 并与事件数解耦。故当前密度上限**不再由 24MiB/源 线性模型决定**，`1GiB ≈ 40 源` 的结论已作废。
  - **容量量测口径（硬约束）**：结论必须 (1) **全局保活被测对象**（存入包级变量；`runtime.KeepAlive` 是编译期屏障，放在读数之后无效）；(2) 取稳态（强制 GC 后）读数；(3) 对存活堆剖析归因，排除量测脚手架自身内存。FR-483 期间两次读错都因违反该口径（一次因未保活把稳态低估约 12 倍，一次因脚手架保留把稳态高估）。
  - 25% 预留门禁由 `vlsup.EnforceReserveRatio`/`Supervisor.SetSubBudget` 实现并可判定（总预算未配置时不做判定，不猜过）。
- 失败判定：出现未报告缺口、越过 closed_visible_seq 空洞、旧 owner 进入查询、导出与同 view 集合不一致、权限侧信道或阈值连续 3 个采样窗口超限即 FAIL。

10k 单 VL 基线（写入约 0.22s、查询 p95 约 23.6ms）只是环境参考；它不能替代上述固定方法和多 Worker 发布验收。

## 7. 权限、配置与资源

- CP 负责用户授权和目标集合；隧道负责已认证 Worker 身份、消息完整性和取消传播；Worker 负责 scope、view 签名、Catalog 范围和资源预算验证。三者不是复制一套业务鉴权引擎。
- CP 和 Worker 双侧校验 `Search/Stats/Fields/Facets/Tail/Rehydrate/Export` scope；授权目标集合由 CP 计算并签入 view；Worker 不接受浏览器直连。
- `move_after_age`、产品 `online_retention` 和 VL runtime retention 分离映射；底层自动清理不得绕过产品保留和最后有效副本保护。合法部署组合：HOT-only、HOT+COLD、HOT+DEEP、HOT+COLD+DEEP。COLD 关闭且 Archive 开启时必须显式选择 Raw-only/rehydrate 策略，不得隐式丢弃历史。
- Worker 资源预算独立列出 WAL、HOT、COLD、staging、Rehydrate、查询并发、临时导出空间和 RSS；512MiB 只是 HOT cache 模板。
- 导出允许 CP 临时产物，必须有大小/期限/权限和清理预算；下载前复核权限，成功产物完成校验后才公开，不转入 `logs` 表或长期查询索引。

## 8. 任务拆分

- [x] ADR-094 accepted，并同步 architecture-invariants、decision-alignment、ARCHITECTURE 的 Worker 本地日志元数据例外。
- [x] `proto/worker.proto` 已落地 QueryView、Coverage、Cursor、Stats/Facets/Tail/Export 语义和老 Worker 能力协商规则；`protoc` 通过。
- [ ] 实现事件身份、账本和四水位纯逻辑模型；覆盖 4.3/4.4 的状态转换测试。
- [ ] 实现 Catalog/journal 恢复模型；覆盖每个迁移崩溃点的期望 owner、物理目录、可查询范围和恢复动作。
- [x] 登记 VL tag/构建标识、双平台 hash、许可证清单、实验结果和不支持项；force_flush、进程重启、磁盘满等 Worker 级验收下沉 FR-473/436/437。
- [ ] 实现 canonical projection 与冲突状态，证明 Search/Stats/Facets/Export 消费同一投影版本。
- [ ] 为 Query View 实现 `ingest_seq`/`closed_visible_seq`/generation/租约约束；覆盖空洞、回灌、重放、迁移、清理、Rehydrate 完成、重启和权限撤销。
- [ ] 将已确认公共规则分别落入 `worker-log-acquisition`、`worker-log-normalizer`、`worker-log-lifecycle`、`cp-log-query-coordinator`、`log-ingest-cutover`、`logs-center-tiered-ui`，并在各 spec 引用本规格：Legacy 保留边界、Export 共用 view、历史/实时来源分界。
- [ ] 文档同步：PRD、ARCHITECTURE、API、CHANGELOG 与 FR-482。

## 9. 验收标准

- [ ] 同一 Query View 在回灌、迁移、到期清理期间分页结果稳定；无法维持时在数据变化前阻止后续读取并返回 `VIEW_STALE`，不先返回变化后的页再补发失效。
- [ ] 导出与同 view 的 Search 事件集合、排序、授权和覆盖摘要一致；完整性校验前没有可下载附件；完成后源 Worker 离线不改变已完成产物的语义。
- [ ] 响应丢失、VL 确认窗口重启、非法 NDJSON、批次部分接受均能按表进入 UNKNOWN/PENDING_VERIFY 等状态；没有 HTTP 2xx 直接回收 WAL 的路径；重启从 WAL 或已登记恢复分段恢复。
- [ ] 恢复分段责任状态能从 WAL 转移到 projection/下一份副本，并仅在明确解除条件满足后清理。
- [ ] canonical projection 让 Search、Stats、Facets、Export 在重复事件和分页边界上使用同一逻辑集合。
- [ ] WAL 逼近预算时出现可见降级、缺口和处理动作；未确认数据不被静默删除。
- [ ] Catalog 切换后残留旧目录即使被 VL 自动 attach，也不进入 QueryPlanner；启动先恢复 Catalog/journal，冲突范围为 PARTIAL/RECOVERY_REQUIRED。
- [ ] 越权 Search/Stats/Fields/Facets/Tail/Rehydrate/Export 在 CP、隧道和 Worker 三层均拒绝，响应不泄露字段或覆盖细节。
- [x] 固定性能环境、负载、持续时间、p95/p99、RSS、磁盘和临时空间初始判据已写入 §6.6；发布级压测仍属后续验收。
- [ ] 现有日志关键字告警链路在切换设计评审中有回归证据，未被新数据路径静默删除或绕过。

## 10. 冻结后变更与下游验收

Shared Contracts 冻结完成；以下项目属于冻结后的显式评审/下游 FR 验收，不重新打开基础契约：

1. 投递与回收：验证受管恢复分段责任状态、责任解除条件、清理保护和预算耗尽动作；
2. 身份与重复：验证 canonical projection 在重复、冲突、分页边界和重启后的稳定性；
3. 固定视图：验证每源 `ingest_seq`/`closed_visible_seq` 连续前缀、空洞、重放、重启和数据修复顺序；
4. Catalog 与资源：租约恢复、retention 协同、预算耗尽和受影响范围隔离；
5. 协议与验证基线：具体 VL 测试版本、完整 proto、旧 Worker 能力协商和性能阈值。

当前验证进度：冻结前证据已登记 VL v1.52.0 JSON stream 依赖行为 PASS、g1 响应不确定隔离→g2 重建→PublishedProjection 发布及真实 Search/Stats/Facets/Export 逻辑集合一致性 PASS；v1.52.0 双平台资产审批已完成。Worker 级恢复分段/ACK-loss、真实 Catalog 崩溃矩阵、失败态和发布级性能阈值仍属于下游 FR 验收。

FR-472 已达到“契约已冻结”门槛。后续 A/B/C Worker 生产组件实验分别归入 FR-473/436/437；若改变 Shared Contracts，必须重新评审并同步依赖模块、兼容规则和测试。
