# 功能规格：Worker 日志平台 Shared Contracts（FR-473）

> 状态：已冻结（Shared Contracts 基线）　·　关联 PRD：FR-473　·　分支：feature/fr-log-platform-foundation
>
> 本规格是 FR-473～484 的开发前闸门。它定义正式版契约基线；后续发现问题允许通过显式评审修订，但必须同步依赖模块、兼容规则和测试。未通过本规格的验收，不得按模块全面开发 Lifecycle、Archive 或 CP 联邦。

### 2026-10-29 显式评审修订：废除受管恢复分段，`reclaim` 改按投递证据推进

**本次修订改变 Shared Contracts，按 §10 末尾要求重新评审并同步依赖模块、兼容规则和测试。**
它撤销了 §4.3 的「受管恢复副本证明」体系，因为该体系的论证前提已随本日另两项变更一起消失。

#### 前提怎么消失的

1. **WAL 改纯内存队列**（同日）：`source_wal` 表删除，队列内容不再跨重启恢复；可靠性改由
   「持久化 position 水位 + 重启重读」承担（对齐 Filebeat `queue.mem` + registry 语义）。
2. **积压越界改为丢弃最旧、不暂停**：对齐 Fluent Bit `memrb` 语义（丢最旧，配套
   `memrb_dropped_chunks/bytes` 指标）。

§4.3 整套机制的前提是「WAL 里还有未投递副本，需要一个受管副本接管责任后才能删 WAL」。
WAL 不落盘后：没有副本需要别人接管，也没有 WAL 行需要「删得掉」⇒ **机制失去对象**。

更关键的是它成了生产卡死的直接原因：生产 10 个源 `reclaim = 0`，链为
「找不到恢复段 → `ErrReclaimNoSegment` → 补段要求已发布投影证据 → 超热层保留期取不到
→ 补段永不成 → `reclaim` 永为 0 →（旧架构）WAL 撞上限 → 整源停采」。

#### 新规则（取代本节旧 §4.3）

```go
reclaim = min(contiguousCertifiedEnd(entry), max(read_position, durable_position))
```

- `contiguousCertifiedEnd` = 从当前 `reclaim_position` 起、被 **`REQUEST_DONE` 证据连续覆盖**
  到的最远位置（`RequestDoneRangesOf` 归并后逐段推进）。
- **不得用 `delivery_position` 当回收上界**：它的语义是「所有字节均有**请求结果**」，而请求结果
  **包括 `UNKNOWN`**（`acquire.recordHTTPResultLocked` 把响应丢失 / 非 2xx 也登记成批次以保留审计）。
  拿它作上界 = 把「发出去了但不知落没落库」的数据从内存队列删掉。旧架构正好靠
  「恢复段仅在 `REQUEST_DONE` 覆盖时才建」避开了这一层，撤销该体系时必须显式接管这条红线。
- `UNKNOWN` 区间因此成为回收的**停滞点**：直到重投成功，或队列越界按丢弃语义处置——两个出口都是显式的。
- 不再需要 `release_reason`、责任接收者、恢复分段 hold、`recovery_audit` 归档表、补段链。

#### 被撤销的条款

| 位置 | 原内容 | 现状态 |
|---|---|---|
| §4.2 | 「受管恢复分段提交成功」作为一类证据 | **删除** |
| §4.3 | 全部 5 条前置 + `STAGED→…→CLEANED` 状态机 + 三个 `release_reason` | **删除**，代以上方新规则 |
| §4.4 | 「从已登记受管恢复分段恢复」 | 改为「从持久 position 水位 + 重启重读恢复」 |
| §9 | 「重启从 WAL 或已登记恢复分段恢复」/「恢复分段责任状态…」 | 改为验证新规则与 `UNKNOWN` 不得被回收 |
| §10.1 | 「投递与回收：验证受管恢复分段责任状态…」 | 改为验证新规则 |

#### 代码落点

- 语义：`internal/worker/logs/ledger/ledger.go` 的 `tryReclaimLocked` / `contiguousCertifiedEnd`
- 回归：`internal/worker/logs/ledger/reclaim_evidence_test.go`（5 条，含「UNKNOWN 不得被回收」）
- 完整论证与删除清单：`docs/specs/reclaim-by-delivery/spec.md`
- 落地：主体 + 删孤儿 + 回归（三批）

#### 不变的部分（明确保留）

- 四水位模型本身（`read`/`durable`/`delivery`/`reclaim`）与「位置与状态分开保存」
- `durable_position` 同代次单调前进
- 「乱序响应不得用最大位置越过未解决空洞」
- 缺口（`Gap`）机制、人工弃投凭据（`Abandonments`）、`PositionInvariantViolations` 观测面
- `published_evidence.go` 全部调用面（缺口消解 + 延迟投递确认）

### 2026-10-30 显式评审修订：投递主路径取消逐字段回查（verify）

**本次修订改变 Shared Contracts，按 § 10 末尾要求重新评审并同步依赖模块、兼容规则和测试。**
它收窄了 §4.2 证据表中 `VerifiedRuns` 的作用域，并解除了 ADR-099 §9 中
「verify 的 fail-loud 语义不得削弱」这条红线在**投递主路径**上的适用。

> 裁定来源：用户 2026-10-30 明确选择「承认旧规矩过时了，把它正式改掉」（选项 A）。

#### 前提怎么消失的

1. **它占掉了整轮墙钟的绝大部分**：60 源逐源串行基线实测，单轮 99.6s 中
   deliver=90.8s，其中 verify（VL 的 LogsQL 校验查询，含重试退避）=85.3s（**占整轮 85.6%**）。
   同期 Worker CPU 17–45%、VL CPU 7–23%，双方都没打满 ⇒ 瓶颈是「写完再读回来」这个往返本身。
2. **它有正反馈灾难形态**：一旦 VL 负载偏高使校验反复超时，就变成
   「写完 573562 条 → 校验不过 → 换代次再写一遍」，VL 体积曾因此放大到 **75 倍**。
3. **回收链从不需要它**：回收上界由 `REQUEST_DONE`（HTTP 状态码）直接决定，
   经 `recordHTTPResultLocked` → `contiguousCertifiedEnd`，全程不经过 `VerifiedRuns`。
   已核对：`VerifiedRuns` 在 `ledger` 包内**零命中**，从未被桥接进 `RequestDoneRanges`。
4. **业界没有一家这么做**：Vector / Fluent Bit / Filebeat / Promtail 的共识是
   「后端返回 2xx 即 ACK」，没有任何一家在写完之后回查读。
5. **VL 自身已经实测接受压缩后的写入并返回 200**（v1.52.0，65090B → 895B，约 73×）。

#### 新规则（取代本节旧 §4.2 中 verify 作为投递前置的部分）

- **投递主路径以 HTTP 状态码为唯一结论**：单批要么整批被 VL 接受（2xx）、要么整批失败
  （非 2xx / 传输错）。**没有第三种「写没写进去未知」的状态**——请求体是一次 POST 送完的。
- **不要假定 VL 会去重——它不会。** 2026-11-05 在生产 VL v1.52.0 上实测（同 `_time`、
  同 `event_id`、同 `_msg` 写两次）：两次都 200，查询 `stats count()` 得到 **2**，且两条行的
  `_stream_id` 也相同。VictoriaLogs 是 append-only 时序库，`/insert/jsonline`
  **不做任何字段级唯一约束；`event_id` 只是个普通字段**。
  ⇒ **重投真的会多写行**，「宁可重发」不再天然安全，去重责任必须由写入侧自己承担。

  历史上仓库里曾有三处把去重交给下游，现全部不成立，不得据此论证 at-least-once：

  | # | 原假设 | 现状 |
  |---|---|---|
  | 1 | 写前幂等 `dropEventsAlreadyWritten*` | 2026-11-02 简化后**零生产调用点** |
  | 2 | `projection_generation` 代次过滤 | 2026-11-03 后不再写该标签 |
  | 3 | VL 侧 `event_id` 幂等 | **本次实测证伪**（见上） |

- **逐字段核验保留在取证路径上**，不在投递主路径上同步执行：
  `repairWrittenEarlierDebt` / `tryConfirmPendingPublication` / `hot_retention_window` /
  `scoped_query_budget`。那里面对的是「历史欠账是否真的在 VL 里」，
  与「本批刚发出去的 POST 是否被接受」是两个问题。
- 保守方向由下游的 pending / debt 判据承担：`hasKnownUnpublishedDayDebt` /
  `hasResidualUnpublishedDebt` / `currentPublishedRuns` 在 `verified` 输入为空时
  **判定更保守**（更易保留 pending），属安全方向。
- 自愈退避的「有进展」信号改由**投递成功**提供（`noteDeliveryProgress`），
  与 `recordVerifiedRunsReporting` 同一语义（进展就地解除退避），不退避算法本身不变。

#### 被修改的条款

| 位置 | 原内容 | 现状态 |
|---|---|---|
| §4.2 证据表 | `VerifiedRuns` 精确区间 = 「逐字段可见性证明」（作为投递前置证据） | **降为可选/异步**；投递主路径不再生成，取证路径仍生成 |
| §9 验收 | 「未确认数据不被静默删除」 | 保留（改由 `REQUEST_DONE` + pending/debt 判据保证） |
| ADR-099 §9 | 「任何采样/降级/预算都不得削弱 … verify 的 fail-loud 语义」 | **在投递主路径上解除**；取证路径仍适用 |

#### 代码落点

- 投递路径：`internal/worker/logs/ingest/runtime.go` 的 `writeProjectionDay`（删 `verify` 调用与 `verifyErr` 分支）
- 进展登记：`internal/worker/logs/ingest/gap_autoresolve.go` 的 `noteDeliveryProgress`
- 回归：`internal/worker/logs/vlsup/client_compression_test.go`、`client_gzip_fallback_test.go`
- 落地（提交见 git log）

#### 不变的部分（明确保留）

- §4.2 证据表的其余四类证据与它们的独立性（不互相升级）
- `reclaim = min(contiguousCertifiedEnd, max(read, durable))`
- `UNKNOWN` 区间不得被回收
- 缺口（`Gap`）机制与人工弃投凭据
- 取证路径上的逐字段核验能力与调用面
- 「未确认数据不被静默删除」这条意图

### 2026-10-26 ACKLoss 与静默 766 闭环

以下两项自「2026-10-25 功能修复收口与延期专项」的延期条目中**闭环**；其余延期项（250ms 首屏、原 9×30min 长窗、Worker fileCPU 诊断）**状态不变**，门限不放宽、不计通过。

- **ACKLoss**（请求已发出而 ACK 丢失 ⇒ 结果落 UNKNOWN 态，禁止回收）：可检出并能自愈恢复；注入未解除且无新输入时仍自愈；零丢失、零重复。
- **Worker 静默 766**：`766` 不是错误码，而是**事件条数**——超过单次排空上限 500 条，剩余 266 条依赖既有自愈闭环排空；均在既有 120 秒窗口内完全收敛。

对应回归：`TestDurableOutboxACKLoss*`（含 `ACKLossVisibleTailRecoversWithoutNewInput`）与
`TestDurableOutboxHeldHeadAndSilent766UseRealSQLiteAndVL`（真实 SQLite + VL）。以上闭环**不替代**首屏、长稳与 fileCPU 诊断的延期结论。

### 2026-10-25 功能修复收口与延期专项

**本次提交仅收口本轮代码修复及已有证据覆盖的功能回归，不承诺性能达标、长期稳定或全部故障覆盖。** 用户已接受功能修复收口、性能专项延期；FR-473～484 的既有交付历史保留，FR-475 按 Linux 范围交付、Windows 运行时未认证；FR-496 的本地迁移与脏状态三开按已有集成证据收口，不冒新增生产部署复验。FR-497 全自动收养 T7 等未验项、FR-501 未实现的回收/指标等仍保持开发中；FR-498 性能/容量长稳专项标记延期，需求和门限不改。

250ms 首屏性能、原 9×30min 长窗、追加最终 ACKLoss 覆盖、严格完整 Worker 静默 766 及 Worker fileCPU 诊断均从本次功能修复收口的阻塞条件移为**延期专项**：分别仍是未达标、未执行或未完成验证，绝非通过或降门。已有回归、质量、raw COMMIT 与指定真实 live 的有效范围见下表；各证据保留自己的源码/输入归属，本次仅更新文档状态，不把旧 seal 或旧执行结果重标为新文档内容下的实跑。原失败、HOLD、skip 和尚未覆盖分支继续保留；下文有日期的旧候选“待验/禁跑/交付状态不动”等措辞只描述当时，不覆盖本节范围裁定。

### 2026-10-22 修复批次范围与验收

本批针对采集、账本/SQLite、处理/事件段/生命周期、VL 运行时、查询协调及日志面板的既有契约缺陷实施修复，不改变事件身份、源代次、三进程边界或默认删除闸门。当前状态为 **r028 代码修复及已覆盖功能回归完成、完整质量门相对基线通过（余留 CFR 条件未测）、live 原 scope 已完成；性能 HOLD 并延期**。旧 v8 指定联合短场景只对其原源码成立；本次功能修复收口不等于全部缺陷核销、整平台全交付或性能认证。

- 采集恢复必须保存可重建的物理/逻辑位置映射和分段绑定指纹；活文件瞬时 EOF 不是完整行边界。单行/单轮读取预算之外，生产 pipeline 的逻辑 Multiline 缓冲也须有有限默认，溢出显式承接原文与源区间并集，不改独立 Normalizer 库零值语义、不另造 ID 算法或丢弃源责任。
- WAL 的内存 durable 标记、索引提交资格、请求结果前缀、REQUEST_DONE 与 VerifiedRuns 分别代表不同证据；所有投递入口共用提交资格门，交办后静默也须沿既有 poll 有界重取资格，未实际提交前零外发，不能依赖新输入或伪造缺口解锁。自动缺口消解共用精确连续区间和原因允许名单。
- SQLite 对缺口子区间、裁定审计、定位凭据及恢复责任逐字段往返；范围删除计入真实事务/周期行预算。活动恢复引用只有先保存完整审计、取得持久回执并复核 hold/消费者后才能裁剪。
- 重建成本绑定源、UTC 日及稳定内容窗口；物化成功但发布 CAS 失效、部分写和 ACK 未知均消耗相应成本预算。预算不得阻断只读取证、合法复用和正常尾采。
- 生命周期按物理 UTC 日冻结写入接纳并排空在途写者；同批成员持久固定，逐记录 journal 后状态不得被批摘要覆盖。已有 COLD 同日内容不得被覆盖或直接拼接底层存储文件，迟到 HOT 残留须经受控补录与逐字段校验转移责任。
- Query View 固定完整逐源投影并获得真实有界租约；实际读取使用 read pin，取消/到期不得提前拆除仍在读取的目录。纯迁移保留可读旧引用，集合改变则明确失效。未证明全局安全分页边界不得推进游标。
- CP 首屏保存真实 Worker view；Fields/Facets 的顶层覆盖、截断和整请求预算不得在适配/合并中丢失。面板三种导出分别是当前页、当前有效窗全部匹配和显式自选时间段；改范围须新建匹配 view，范围碰巧相同时允许内容相同。当前页复用既有导出 HTTP 的 `range=page`，要求同 view、入页 cursor 与 limit，禁用 Search 缓存并新鲜复核同 view Stats、权限与覆盖，不接收客户端原始行；该 HTTP 增量已实现并通过定向跨层回归；真实 VL 与真后端 Chromium 的指定短场景已通过，准确范围见下述联合短验，不外推所有场景或正式性能验收。
- VL 配置统一版本化时长语义，显式区分 native TTL、产品搬运年龄及恢复副本；有限 TTL 不代表无限最后副本保护。运行期启动/恢复须有真实业务接线，预算采样须接到有界准入；独立诊断必须脱敏、有界且不递归写 VL。

完成判据是原反例纳入正式回归并红转绿、旧状态兼容迁移及脏环境首启/二启/三启逐字段一致、真实隔离 VL 的迁移/租约/分页/导出链通过，并按本规格 §6.6 分列正确性与资源门禁。当前已覆盖功能回归与完整质量门的结果见下表；§6.6 未达性能和未执行长窗移至延期专项，不更改原判据。全仓 lint 仍有存量失败，本批新增为 0，不称 raw 全绿，也不关闭规则造绿。生产配置变更、部署/发布、Windows 运行时和真正断电验证不因本批本地修复自动获得授权或视为已完成。

### 2026-10-24 验证状态（r028，性能 HOLD）

当前机制已实施，验收结果按各自冻结输入归属，不挪用旧源证据；原有历史段落保留原时点。既有正式历史参见 [验收记录](acceptance-record.md)，中间 FAIL/HOLD 与研究流水保留交付审计索引，不在本节重贴。

- 新批仍须真实 actual-complete Apply 的精确资格；混合历史以 immutable 证据另证，活动 WAL 子集不跳 SQL 门。轻 receipt 索引只有同 owner、old cut≤new cut、seq-revision 全组唯一 captured-Durable 才能省重复 SHA；每当前成员仍完整 Marshal、新 SQL Body/FP，歧义、未来、Restore/Replay、refs 水合和 Commit 态变化严格 fallback，最终 old-receipt 精确 live 交集及指针/current-pipe CAS 不变，无全文缓存。
- RO 补证保原 capture、完整当前 ACK 正文及 current-pipe/PP-token 短 CAS，不借最大水位；STAGED 先锁外持久归档 lookup 保身份，再绑定真实 RO receiver。ACKLoss 仍 Unknown，written 尾确认不饿死更早实测 missing 修复；源 owner ctx/原共享查询与物化预算贯穿 proof，最终发布锁后验 attempt 截止，新 permit 后重验父 authority/token/target。有界连续 prefix/局部 lease/hold 公平和 Stop 真 join 保持，网络与归档 SQL 不跨 WAL 锁；证据不足保债，不将现场未采 hint/Verified 的初因说成全部定案。
- selector 仅等价分组，完整源/代次/closed、字段、排序/tie 和预算保持；inline/ref 的正文语义不变。无新产品 API、schema、配置、队列/线程或预算，有限增量安全复核不等于全面认证。

| 最新验收项 | 实际结果与限域 |
|---|---|
| 完整隔离 ingest race | 硬 4CPU/8GiB/0swap：797 pass、0 fail、3 原 skip；binary/Docker/converter 均退出 0 |
| 普通全 Go | 7517 pass、68 包、0 fail、9 test skip，另 5 无测试包；跳过为原 8 加 grpc CFR 的 TLS 条件 skip，decompiler CFR 本轮 PASS，剩余 CFR 条件未测不作覆盖 |
| vet / lint / build | vet 与 CP/Worker build 均退出 0；原规则 lint 454→416、新增 0、raw exit 1 仍为存量，不称 raw 全绿；各门首尾输入核对一致 |
| raw 耐久批门 | 原 60 源 drain 与 20×1000 真实 SQL COMMIT 通过；p95=0.805227885s、max=0.846686539s、0 超 1s，仅此受控工作集 |
| 6/30 steady / recovery | 各 64000 条、60 源 drain、Stats/Export 数据正确性 PASS；FAST 首屏 p95=516.801758ms / 608.351093ms，均超 250ms。恢复实见 503=44、ACKLoss=0，ACKLoss 本轮未覆盖，不能称恢复全覆盖或伪报原 120s drain 失败 |
| r028 live / 正常容量 | 原严格基础 15 项及第 16 项确认 409 已通过；当前页/时间窗下载、同 view 校验、半开边界和 native 在途取消隔离成立，仅限原 scope，不扩为 TanStack observer E2E 或未覆盖历史/迁移成功分支。正常 800×60 容量路径已验证，不外推独立 pressure、复杂 PP 或 RSS 硬界 |
| 延期：首屏/长稳与最终故障覆盖 | 原 250ms 首屏门未达，原 9×30min 未执行；r028 恢复 ACKLoss=0，追加最终 ACKLoss 覆盖未验。未放宽门限，均不计通过。**ACKLoss 已于 [2026-10-26 闭环](#2026-10-26-ackloss-与静默-766-闭环)；首屏与长稳仍延期** |
| 延期：严格完整 Worker 静默 766 | 准备阶段失败，未进入原 120s 恢复窗，仍为 PARTIAL；原 SQLite 局部回归不替代完整 Worker 证明，不裁定为产品 quiet 反例。**该延期已于 [2026-10-26 闭环](#2026-10-26-ackloss-与静默-766-闭环)** |
| 延期：性能诊断 | 引擎诊断 FAST p95=416.487991ms，仍未达 250ms，不能当正式完整 Search 成绩；Worker fileCPU 业务取证未执行，无新性能或产品根因结论 |

首屏 FAST Pending 不充作完整 Search 样本，Stats 亦不替 Search 分母。此前短档及最后 2/30 steady 均未同时满足正确性与首屏门，失败原证保留；完整 Worker 特定延期766补证的私有夹具协议已对齐、目标766已真实追加，但原18s锁期已采 typed-Deferred 增量为0（另2个非Deferred返回的具体 error 未知，不判 BUSY 或非 BUSY；亚秒尾未采，不推全程从未返回），未进入原120s恢复，证据仍不足、非产品quiet反例；原SQLite单测静默766只属原scope，不替代该完整Worker证明；性能仍 HOLD，9×1800s 未运行，连同追加最终 ACKLoss、严格完整 Worker 静默 766 和 Worker fileCPU 诊断延期；本轮不再为这些专项继续调优或重跑，不改变原输入/门限。PRD 按 FR 分别归真，生产默认不变；不称最优、长期稳定、全部验收或生产投产。

### durable outbox r8d（单次受控耐久门通过，联合验收中）

以下为旧候选时点的历史记录；最新状态以前述验证节为准。

当前 17 文件候选清单 SHA-256 为 `b6837cb8abad3731d36f8405e912b152b84596065aea7943c9b468934850ae04`，独立增量审查未报告新 P1。生产 deferred=true 组合的私有轮尾仅将 best-effort 元数据交给既有后台持久化通道；新批外发仍受原严格 actual-plan 真实 SQL 完成门约束，未把交办当完成。WAL 短锁捕获私有全 Fields 快照，同 plan 的身份与 BodyJSON 来自同次完整编码；live 精确复核遇歧义回退完整正文，不建 EventID 全局缓存，不放松正文/ACK/PP 门，也无 RPC、schema、产品配置或预算变化。

一次受控 **4vCPU/8GiB/noSwap、60 源工作集**的 raw run8 已通过 drain 与原 `p95≤1s` 耐久门：20×1000 个闭合事件全部真实到达 SQL COMMIT 前缀，p95=`0.609764603s`、max=`0.719532125s`、超过 1s 为 `0/20`，raw 退出码 0。这只是该次工作集/20 批门的实测，不称最优、长期稳定、全部验收或生产投产。旧 run4 p95≈1.987s、run5≈1.913s、run6 因真实缺陷 HOLD、run7≈1.214s 均保留；跨 run 宿主负载不同，不把差值当严格性能收益。

新全仓、新双短/live、Search 与正式 9×30min 仍待真实结果，旧 v8/r4 的门和 live 不能套给新 hash。下述“1s 未跑”等描述保留其旧候选时点，不否定本次 r8d 单次门，也不把本次通过泛化到其余未测项。

### durable outbox r4（本地候选已实现，联合验收中）

本节对应 r4 的 16 文件冻结候选（清单 SHA-256 `802bbfe6dc895c801145de00b3034da22124b796c9193caf5261572e06168344`）。三个只读代码补审未报阻断，有限范围安全核对为 0 finding；这些不是新候选全 Go、实际 Worker 或性能验收。旧 v8 的全门、双短、live 及其失败历史保留，不能沿用为 r4 最终绿。

- **组合与耐久资格**：Worker 生产组合根内部显式 `Options.DeferredDelivery=true`，独立 Manager 默认 `false`、保持同步兼容。不新增产品键、RPC、schema、WAL 格式或预算。原始新事件先耐久进入既有 WAL，调度器仅合并有界 source-key、不保存正文；外发前以真实 SQLite 完成的 actual plan 核对活动 WAL 子集的 `CommitIdentity{EventID,CanonicalHash,Record,BodyDigest}`。在该生产 outbox 路径中，generic nil、内存 durable、更高水位、交办/Incomplete、旧 pipe 或旧计划都不能授予资格；资格、原始请求 ACK、VerifiedRuns 与 PublishedProjection 互不升级。
- **混合实际写集合**：actual-set 可以包含已回收的 immutable 历史 X 与活动 WAL Y，不能要求所有正文仍在当前 WAL，也不能因 X 不在 WAL 而跳过 Y 的 SQL 回执。`CaptureDeliverySubset` 分出活动成员与历史候选；Y 必须取得 actual-plan receipt，X 则重读原追加式 eventstore/受控 inline 历史的固定行界、原位置和摘要证据。written hint 只保留原 full actual-set 的窗口/count/digest、原 cut/成员及历史证明，不缓存正文；恢复时重建完整集合再逐项校验，不把当前 cut 冒作原 cut、不让后续追加扩集合。
- **候选、单飞与回收**：冲突判据只针对本源 actual write candidates 及其真实权威兄弟，namespace 聚合历史诊断不硬封禁健康源；合法预存变体沿既有裁定选真实赢家，真冲突仍留 WAL 与可见责任。所有正常/重放/重建/维护/同步入口共用每源 guard，只有仍 active 的 owner ctx 可重入。局部 prefix/range lease、hold 公平轮转与原子条件回收防止旧头部反复占预算；网络 IO 不持全 WAL 锁，最终当前正文/ACK/ledger target 的条件复核失败不抬水位、不裁证明。精确永久丢失的人工审计出口独立保留，不伪造 ACK。Stop 关准入、取消并 join 真实在途任务后才 flush/close，取消或等待超时不等于任务已退出。
- **未知写与归档忙态**：已 written 但结果未知先按原代次/实际 permit 物理目标 RO 确认，不重新插入；语义 Err 保留原类别，不冒作 Pending 或新 copies。归档在采样前用 `ReserveAppend` 原子 reservation 确保窗口可追加，忙态不先消耗 sampler；reservation 只跨采样/Append，不跨 Commit/journal/网络。`ErrWALWindowBusy` 快速可重试，同对象/原游标 CAS 只回退未接收部分并关闭 reader，已接收前缀保留，新尾非相交边界仍可 COMMIT；自动续试依赖现有 `SyncArchiveScan`，不是只启动 Manager 就会重试。
- **固定校验预算与测试协议**：固定 12 次按原始校验簇定格，同 ctx/截止/原窗贯穿 12KiB query 子片、六字段核对与跨响应去重；probe 不是发布/回收证据，不能按 Poll 续预算或把子片结果拼成完整证明。六字段为 `_time/_msg/event_id/level/stream/canonical_content_hash`。公共测试 fixture 本轮以 RawMessage 保留缺失/null/错误类型/转义并按 payload SHA 保证等价，只是测试响应协议修正，不夸为新增生产能力。

当前新 ordinary/race、全仓门、raw 完整 Worker 1s、Search、双短/live 与 9 窗仍待绑定 r4 的真实结果；旧 FAST 短验大于 250ms、精确 Search 样本为 0，不能记性能达标或最优。正式 9×30min 仍 blocked，保持无 Git/依赖/生产/Windows 操作授权，不改 FR 已交付状态。

### 2026-10-22 有限真实联合短验与剩余门禁

本节是旧 v8 源码的历史真实结论，保留其原 hash、环境和失败记录；不作为上述 r4 候选的重新验收证明。

- 同一生产处理实现 `5bc8dba5…`、联合源码清单 `6d5856fa…` 的真实 CP/Worker/VL，完成两个 60 源、30s 负载窗（稳态/故障恢复）；各含预热共 64000 条，在 120s 排空上界内逐事件 ID、canonical hash、位置与 SQLite 恢复责任对账完整，重复为 0、末端 backlog/未回收责任为 0。恢复臂实际注入 VL 503 共 54 次、ACK 丢失 5 次；非空 940 条固定 view 的 Search/Stats/Export 集合一致、Stats 为 exact。此结果闭合指定静默欠账短场景，不宣称永久无故障或全部失败矩阵完成。
- 另用相同 Go 内容与前端 `b9ae9dfc…` 的 Go 同源正式静态页完成本地真 Chromium live 验证：index 与 142 个资产逐 SHA 核对，非 Vite/devmock。64000 底库由独立 API 全量对账；浏览器只验合法 LogsQL 的 1s、1800 条非空 scope，两页各 100、全部/自选段各 1800 且同 SHA，并核对 `[from,to)`、原生 abort、离线导出不下载、不重放。迁移首击/取消零 POST，确认仅 1 POST 并由真实 409 前置条件拒绝、无假成功；这不是浏览器迁移成功或全底库 UI 验收。
- 上述是 bare-local 正确性短验，不是生产部署、4C/8GiB/SSD 性能或 §6.6 达标。FAST 首屏短验 p95 约 371ms，伴随并行测试，不能标为性能达标或最优。正式 9×30min 矩阵尚未启动。
- 最终全 Go 门仍受 PollOverlap 测试时序修复阻挡；新增 race 跳过或弱化原 3ms 门禁的尝试已拒绝并撤回，严格三例 race 已实际通过，仍不能代替当前全 Go/全仓末门。各次源码漂移、环境失败与失效窗口不作最终同内容证据，旧红证不覆盖。

## 1. 背景与目标

现有日志查询以 CP `logs` 表为中心，无法提供 Worker 本地持久化、分层生命周期、跨 Worker 覆盖状态和可解释失败语义。FR-473 定义所有后续 FR 共用的身份、投递、查询、权限、资源和生命周期边界。

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
- 本规格不承担最终发行资产打包审批，但必须登记契约能力验证所依据的具体 VL tag/构建标识、实验结果和不支持项；FR-476 负责发行资产、哈希、许可、分发和运行时兼容审批。FR-476 使用其他版本时，必须重新完成受影响的能力验证。

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
| `reclaim_position` | 之前的 WAL 前缀已由可靠来源承担或已取得确认投递证据 | 只按 §4.3 的规则单调前进 |

### 4.2 投递语义与可用证据

正式版基线采用“至少一次投递 + 可审计重复对账”。Worker 为每条事件维护本地 `validation_state`、`delivery_state` 和 `verification_state`；三者独立。

| 证据 | 能说明什么 | 不能说明什么 |
|---|---|---|
| Worker 本地校验通过 | 序列化、字段和时间检查通过 | VL 已接受或已耐久 |
| 对应批次索引提交成功 | 本批事件取得投递资格；交办/`Incomplete` 不成立 | 远端已接受或已核验 |
| `REQUEST_DONE` 精确区间 | 该区间有真实请求完成 ACK | 逐字段可见或长期可恢复 |
| `VerifiedRuns` 精确区间 | 该区间完成逐字段可见性证明（**取证路径产出，非投递前置**；2026-10-30 修订） | 存在新的 HTTP ACK 或无限保留保证 |
| HTTP 请求成功 | 请求完成 | 所有记录逐条入库、持久落盘或可恢复 |
| 按 event_id 查询核验成功 | 核验时对应事件可见 | 后续掉电仍可恢复、没有重复 |

> 原表末行「受管恢复分段提交成功」已于 [2026-10-29 显式评审修订](#2026-10-29-显式评审修订废除受管恢复分段reclaim-改按投递证据推进) 删除。

选定的 JSON stream 请求级接口支持请求结果；本次能力基线不得假定原生逐事件接受回执或批次耐久提交回执。非法 JSON 行、请求级错误和未知结果分别进入逐事件状态；服务端全局日志/指标不能伪装成该批次逐记录结果。`UNKNOWN` 不得直接进入坏记录隔离，必须保留恢复或核验责任。

### 4.3 `reclaim_position` 推进规则

> **本节已于 2026-10-29 显式评审修订全面重写**，见文首
> [2026-10-29 显式评审修订](#2026-10-29-显式评审修订废除受管恢复分段reclaim-改按投递证据推进)。
> 现行规则：`reclaim = min(contiguousCertifiedEnd, max(read, durable))`，
> 其中 `contiguousCertifiedEnd` 是 `REQUEST_DONE` 证据的连续覆盖末端；
> 受管恢复分段、责任状态机与 `release_reason` 已全部废除。
> 下文原文保留作为历史记录，**不代表现契约**。

<details>
<summary>历史原文（2026-10-29 之前，已废止）</summary>

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

</details>

### 4.4 崩溃状态转换

| 场景 | 状态 | 动作 |
|---|---|---|
| 请求前崩溃 | `delivery_state=NOT_SENT` | 按持久账本定位事件，从尚存 WAL 或重启重读恢复；原 WAL 合法回收不构成恢复失败 |
| 请求已发出、响应丢失 | `delivery_state=UNKNOWN` | 保留恢复责任（UNKNOWN 区间不参与回收）；按 event_id/范围核验，无法确认则重放并记录可能重复 |
| 请求完成但本地账本未提交 | 若存在持久证据则 `REQUEST_DONE` + `verification_state=UNKNOWN`；仅存在崩溃前内存则 `UNKNOWN` | 先核验/补账本；不得根据原执行阶段补写成功状态 |
| VL 在确认窗口内重启 | `REPLAY_REQUIRED` | 受影响 view 失效或暂停；保留恢复责任，健康恢复后再核验 |
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

实现对账补充（2026-09-23）：投影发布必须以物理写入前观察到的 Catalog journal 序号、owner、generation、写入路由和 manifest 为条件执行一次原子提交。核验期间若迁移或其他投影发布者已经推进 Catalog，旧写入者保留恢复责任并隔离重试，不得把旧结果挂到新 owner，也不得覆盖先提交的 manifest。投影请求发出前持久登记 `publication_pending`；写入或发布任一步骤结果不确定时先保留该标记；具备受管已写候选证据时优先按原集合只读取证，证据不足保持不确定并沿既有预算/恢复路径，仍依赖既有 reconcile/恢复调度，不承诺每种无 hint 的 queryErr 都能独立静默自愈。单日副本复用后源级仍待确认时返回 Partial，普通入口也由既有按源唯一退避 timer 续做全 source 逐字段确认及版本/Catalog 条件复核；确认成功可零新写、零新代次解除旧标记，只有实测 missing 才交既有预算重建。尚无新 publication attempt 的 WAL-only 新尾若处于旧 closed 外，不必阻止旧发布确认，但不得因此给新尾补 ACK、扩大 closed、回收 WAL/SQL 责任或宣称 readiness 完整；真正的新发布版本、同 record 异内容或权威变化仍须重新取证，不能用旧确认洗白。

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
- 每个目标的完整逐源 projection（源身份/代次、该源 projection generations、封闭水位）与固定 owner/目录引用；跨 Worker view 是各目标真实 Worker view 和固定读取位置组成的向量，不承诺所有 Worker 同一物理时刻的全局事务快照；
- 与选取引用原子登记的非零有界 TTL 租约，以及每次实际读取独立取得的 request read pin；取消/到期/淘汰禁止新读，已有 pin 直到实际 goroutine 退出才释放。租约 delta 不覆盖发布记录，也不推进导致物化 CAS 失效的发布 revision；
- 创建时的覆盖状态、能力版本和视图过期时间；
- `view_id`、签名版本和失效原因；Worker 只接受本进程签发且未消费的一次创建票据，已关闭/过期或重启前 ID 不能复活；容量超额明确预算失败，不临时改选新集合。

后续 Cursor 只能引用该 view。`closed_visible_seq` 是不含可见性空洞的封闭前缀：此前所有接纳位置都有明确处理结果，明确隔离/排除的记录仍计入 coverage。服务端必须以该前缀或等效不可变投影约束每一页，不能重新查询动态最新集合。Cursor 只携带 `(view_id, logical_sort_key, page_limit)`，不把 watermark 当作未执行的提示字段。

### 6.2 视图失效

- 权限在每次请求和下载时重新校验；权限撤销优先于固定数据集；
- 普通追加继续使用旧冻结 projection；源集合增删、逐源缩水、重建/重新解析/补录替换或删除使原集合不可读时返回 `VIEW_STALE`，不能只比较最大水位。纯迁移只要旧租约仍能读取原固定 owner/目录集合，不自动失效，新 view 才选新 owner；
- Rehydrate 完成后不得悄悄把 DEEP 数据加入旧 view；可等待并创建新 view，或使旧 Cursor 明确失效；
- view 到期、取消或超过资源预算时返回明确状态，不把 `items=[]` 当作完整零结果。

### 6.3 响应状态

查询语义须分别表达固定 view、完整性/partial 原因/目标覆盖、排序与封闭向量、取消/预算状态、重复质量、统计质量和枚举结束；这些不是同一维度。现有 Search/Stats wire 携带独立 `LogQuality`，Fields/Facets wire 未声明该字段时不得虚构 Worker quality；须无损保留现有 coverage/截断证据，CP Facets 可据此保守派生统计质量，Fields 不伪造独立 quality。稳定排序为 `(event_time DESC, log_source_id ASC, source_generation ASC, record_start DESC, record_end DESC, event_id ASC)`，不得使用本地自增 ID 作为跨 Worker 唯一排序。

任一参与 range/Worker 失败或全局安全页边界未证明时可返回 partial 行，但不发跨越未知范围的新 cursor，`exhausted=false`，明确同页重试；`next_cursor` 为空不能改写成历史完整。

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
  - **修订（2026-09-24，FR-484）**：上述 674MiB/24MiB-per-源 是「canonical 事件体常驻内存 + 内联于 state」时的数字。FR-484 把事件体改为**落盘 compact**（语义不变，见 ADR-095）并加界三处跨轮无界累积结构后，稳态 Worker 降至 **64/128/150 源 = 22.5/22.3/20.0MiB**，采集期峰值 **56.0/62.0/61.8MiB**（真机 + 真 VL v1.52.0），二者都**几乎不随源数增长**（常驻量与源数量而非事件总量相关）；`state.json` 由 77 803KiB 降至 139.5KiB 并与事件数解耦。故当前密度上限**不再由 24MiB/源 线性模型决定**，`1GiB ≈ 40 源` 的结论已作废。
  - **容量量测口径（硬约束）**：结论必须 (1) **全局保活被测对象**（存入包级变量；`runtime.KeepAlive` 是编译期屏障，放在读数之后无效）；(2) 取稳态（强制 GC 后）读数；(3) 对存活堆剖析归因，排除量测脚手架自身内存。FR-484 期间两次读错都因违反该口径（一次因未保活把稳态低估约 12 倍，一次因脚手架保留把稳态高估）。
  - 25% 预留门禁由 `vlsup.EnforceReserveRatio`/`Supervisor.SetSubBudget` 实现并可判定（总预算未配置时不做判定，不猜过）。
- 失败判定：出现未报告缺口、越过 closed_visible_seq 空洞、旧 owner 进入查询、导出与同 view 集合不一致、权限侧信道或阈值连续 3 个采样窗口超限即 FAIL。

10k 单 VL 基线（写入约 0.22s、查询 p95 约 23.6ms）只是环境参考；它不能替代上述固定方法和多 Worker 发布验收。

## 7. 权限、配置与资源

- CP 负责用户授权和目标集合；隧道负责已认证 Worker 身份、消息完整性和取消传播；Worker 负责 scope、view 签名、Catalog 范围和资源预算验证。三者不是复制一套业务鉴权引擎。
- CP 和 Worker 双侧校验 `Search/Stats/Fields/Facets/Tail/Rehydrate/Export` scope；授权目标集合由 CP 计算并签入 view；Worker 不接受浏览器直连。
- 物理 UTC 整日迁移可能跨 namespace，CP 必须在 RPC 前要求平台管理员主体与原 `node.manage`，并保留操作者、namespace 格式/节点绑定检查；入口 namespace 仅是探针，单实例组授权不能升级为整日授权。普通 runtime control 的原节点权限不因此收紧。
- `move_after_age`、产品 `online_retention` 和 VL runtime retention 分离映射；底层自动清理不得绕过产品保留和最后有效副本保护。合法部署组合：HOT-only、HOT+COLD、HOT+DEEP、HOT+COLD+DEEP。COLD 关闭且 Archive 开启时必须显式选择 Raw-only/rehydrate 策略，不得隐式丢弃历史。
- Worker 资源预算独立列出 WAL、HOT、COLD、staging、Rehydrate、查询并发、临时导出空间和 RSS；512MiB 只是 HOT cache 模板。
- 导出允许 CP 临时产物，必须有大小/期限/权限和清理预算；下载前复核权限，成功产物完成校验后才公开，不转入 `logs` 表或长期查询索引。

## 8. 任务拆分

- [x] ADR-094 accepted，并同步 architecture-invariants、decision-alignment、ARCHITECTURE 的 Worker 本地日志元数据例外。
- [x] `proto/worker.proto` 已落地 QueryView、Coverage、Cursor、Stats/Facets/Tail/Export 语义和老 Worker 能力协商规则；`protoc` 通过。
- [ ] 实现事件身份、账本和四水位纯逻辑模型；覆盖 4.3/4.4 的状态转换测试。
- [ ] 实现 Catalog/journal 恢复模型；覆盖每个迁移崩溃点的期望 owner、物理目录、可查询范围和恢复动作。
- [x] 登记 VL tag/构建标识、双平台 hash、许可证清单、实验结果和不支持项；force_flush、进程重启、磁盘满等 Worker 级验收下沉 FR-474/476/477。
- [ ] 实现 canonical projection 与冲突状态，证明 Search/Stats/Facets/Export 消费同一投影版本。
- [ ] 为 Query View 实现 `ingest_seq`/`closed_visible_seq`/generation/租约约束；覆盖空洞、回灌、重放、迁移、清理、Rehydrate 完成、重启和权限撤销。
- [ ] 将已确认公共规则分别落入 `worker-log-acquisition`、`worker-log-normalizer`、`worker-log-lifecycle`、`cp-log-query-coordinator`、`log-ingest-cutover`、`logs-center-tiered-ui`，并在各 spec 引用本规格：Legacy 保留边界、Export 共用 view、历史/实时来源分界。
  > **⚠ 本条已无对象（2026-11-09）**：其中 `worker-log-acquisition`（FR-474）与 `log-ingest-cutover`（FR-481）的 spec 已随能力退役而删除；
  > 剩余四个 spec 仍存在。因此本条不可能被完整勾选，**保留仅作历史记录**，不得当作待办追赶。
  > `Legacy 保留边界`与`历史/实时来源分界`两项的前提（CP `logs` 表与 cutover）已不存在；只有 `Export 共用 view` 仍适用于 `cp-log-query-coordinator`。
- [ ] 文档同步：PRD、ARCHITECTURE、API、CHANGELOG 与 FR-483。

## 9. 验收标准

- [ ] 同一 Query View 在回灌、迁移、到期清理期间分页结果稳定；无法维持时在数据变化前阻止后续读取并返回 `VIEW_STALE`，不先返回变化后的页再补发失效。
- [ ] 导出与同 view 的 Search 事件集合、排序、授权和覆盖摘要一致；完整性校验前没有可下载附件；完成后源 Worker 离线不改变已完成产物的语义。
- [ ] 投递主路径以 HTTP 2xx 为唯一结论、不再逐字段回查（2026-10-30 修订）；逐字段核验保留在取证路径（欠账修复/待定发布确认/热层窗口/查询预算）；`VerifiedRuns` 不再作为投递前置证据；重启由「持久 position 水位 + 重启重读」恢复。
- [x] **`UNKNOWN` 区间不得被回收**，回收上界是 `REQUEST_DONE` 证据的连续覆盖末端（2026-10-29 修订；回归 `reclaim_evidence_test.go`）。
- [ ] 重启后 `read`/`durable` 回退到 `reclaim`、tailer 从 `min(durable, read)` 重读那段并能重新产生并投递（取代原「恢复分段责任状态转移」验收项）。
- [ ] canonical projection 让 Search、Stats、Facets、Export 在重复事件和分页边界上使用同一逻辑集合。
- [ ] WAL 逼近预算时出现可见降级、缺口和处理动作；未确认数据不被静默删除。
- [ ] Catalog 切换后残留旧目录即使被 VL 自动 attach，也不进入 QueryPlanner；启动先恢复 Catalog/journal，冲突范围为 PARTIAL/RECOVERY_REQUIRED。
- [ ] 越权 Search/Stats/Fields/Facets/Tail/Rehydrate/Export 在 CP、隧道和 Worker 三层均拒绝，响应不泄露字段或覆盖细节。
- [x] 固定性能环境、负载、持续时间、p95/p99、RSS、磁盘和临时空间初始判据已写入 §6.6；发布级压测仍属后续验收。
- [ ] 现有日志关键字告警链路在切换设计评审中有回归证据，未被新数据路径静默删除或绕过。

## 10. 冻结后变更与下游验收

Shared Contracts 冻结完成；以下项目属于冻结后的显式评审/下游 FR 验收，不重新打开基础契约：

1. 投递与回收：验证 `reclaim = min(REQUEST_DONE 连续覆盖末端, max(read, durability))`、`UNKNOWN` 区间不被回收、重启重读契约（2026-10-29 取代原「受管恢复分段责任状态」验收项）；
2. 身份与重复：验证 canonical projection 在重复、冲突、分页边界和重启后的稳定性；
3. 固定视图：验证每源 `ingest_seq`/`closed_visible_seq` 连续前缀、空洞、重放、重启和数据修复顺序；
4. Catalog 与资源：租约恢复、retention 协同、预算耗尽和受影响范围隔离；
5. 协议与验证基线：具体 VL 测试版本、完整 proto、旧 Worker 能力协商和性能阈值。

当前验证进度：冻结前证据已登记 VL v1.52.0 JSON stream 依赖行为 PASS、g1 响应不确定隔离→g2 重建→PublishedProjection 发布及真实 Search/Stats/Facets/Export 逻辑集合一致性 PASS；v1.52.0 双平台资产审批已完成。Worker 级 ACK-loss、真实 Catalog 崩溃矩阵、失败态和发布级性能阈值仍属于下游 FR 验收。（受管恢复分段已于 2026-10-29 修订废除，不再属于下游验收范围。）

FR-473 已达到“契约已冻结”门槛。后续 A/B/C Worker 生产组件实验分别归入 FR-474/476/477；若改变 Shared Contracts，必须重新评审并同步依赖模块、兼容规则和测试。
