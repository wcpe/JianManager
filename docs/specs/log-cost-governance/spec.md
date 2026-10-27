# 功能规格：日志成本治理（分层 / 采样 / 保留）

> 状态：开发中、部分落地（本轮统一时长、TTL 余量、删除时间边界、资源准入与物理日续跑修复及已覆盖回归完成；事件体**段回收已接线**（默认关闭，`log_retention.eventstore_prune.*`），`/metrics` 已输出保留/搬运、导出归档与事件段回收序列；归档对象回收未实现、导出归档的生产回收出口未装配等边界见 §7，gz 导入接线归 FR-504、长期吸存观测仍未验，不将整 FR 标交付）
> 本次提交/功能修复验收不承诺性能、长稳或全部故障覆盖；延期专项与已有结果统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)，生产删除三闸默认全关。
> 关联 PRD：**FR-501**　·　ADR：**ADR-098**（日志保留、分层与归档）/**ADR-099**（成本治理与不变量 R）
> 关联：FR-498（采集侧成本）、FR-477（分区 Catalog 与冷热 Lifecycle）、FR-474（容量门禁）
> 实测环境：本地 VictoriaLogs **v1.52.0**（真二进制，独立数据目录，未触碰生产）

> **编号对账记录（2026-10-02）**：本 spec 原在 CHANGELOG 中被标为「FR-499」，而 PRD 的 FR-499 已被**采集登记路径与长临界区解耦**（`docs/specs/log-ingest-register-lock/spec.md`）占用，属重号。经对账：FR-499 保留给登记锁解耦不动，本项（日志成本治理）取 **FR-501**（对账时 501–519 全仓无引用，为明确空号；FR-500 已由 daemon cgroup 隔离占用）。CHANGELOG 与 PRD 登记行已同步改号/补登；**此后引用本项一律用 FR-501**。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态与覆盖语义以
`worker-log-platform-contract/spec.md` 为准。

---

## 2026-10-22 修复约束与待验收边界

本节保留修复约束；本轮代码修复及已覆盖回归按顶部范围声明收口，历史候选“待验/未完成”保留原时点，当前结果以共享验收表为准，不从方案推定未实现功能可用。

- 受管 VL 参数和余量计算复用批准版本的统一 native TTL 解析，支持小数且不预先取整成天；产品 TTL 的分钟/永久保留等策略语义单独解析，不能把二者单位混用。旧配置键维持回退兼容；仅启用相应层时才要求明确正的复制校验/恢复 SLA、实际接收端/driver 与可执行 native TTL 余量。
- 有限 native TTL 到期仍会原生淘汰，不受平台显式删除三闸保护；即使配置缓冲，也不能承诺搬运无限失败时“最后副本永存”。产品搬运年龄、受管 VL 淘汰时间和恢复副本约束分开呈现，失败须在余量内处置。
- 生产删除三闸继续默认全关，Sweeper 不接入常态装配。隔离构造数据上的删除验证，必须由代码白名单生成半开 `_time < cutoff`；核对 cutoff 前后 1ns、其它源/级别与任务完成后的保留集合，不能仅凭任务受理返回成功。
- 冷层驱动复用物理日固定批次、逐记录 journal、真实 write permit/lease/read pin 与退休屏障；Owner 已切 COLD 或清理等待不是完整成功。目标日已存在的 HOT 残留走受控逻辑补录，不覆盖旧 COLD，不把正常 pending 永久跳过。
- cache、Go 软限制、RSS、磁盘和全局预算分别约束；unknown 不能当 0，停止进程的 0-known 与未采样不同。控制器启动立即有界采样，前台/后台/预留准入消费真实预算，同时保留有界 WAL 排空。

待联合验收：统一时长、资源准入、迁移与查询/恢复整链及正式稳态性能；各组件已验证范围见对应规格，不把历史变异或短夹具外推整批通过。默认关闭不创建新分层策略，但既有 Frozen 责任仍由独立恢复 loop 续跑。真实短验 r2 的 steady/recovery 无 400 不等于欠账排空：quiet 后耐久交办未重调问题经 766 条正式回归红转绿，后续双 60 源 30s 稳态/恢复各 64000 条对账已通过；该 [有限联合证据](../worker-log-platform-contract/spec.md#2026-10-22-有限真实联合短验与剩余门禁) 不替代正式长窗。9×30min 未启动，约 371ms 的 FAST 短验 p95 有并行测试干扰，不能标性能达标或最优。

### 当前调优裁定（2026-10-24）

raw 耐久批门与短验数据正确性不替代首屏、完整 Search 或恢复全覆盖；当前性能仍 HOLD，长矩阵未运行；相关性能、长稳和追加故障覆盖专项已延期，不再阻塞本次功能修复提交，生产默认不改。机制保持原准入、重放、物化与 proof 预算，回执优化不靠缓存全文或再造副本换速度；准确读数与剩余条件验证统一见 [验证状态](../worker-log-platform-contract/spec.md#2026-10-24-验证状态r028性能-hold)。旧失败/HOLD 与过程证据保留交付索引，以下原有候选记录按历史时点理解。

### durable outbox r4 成本边界（本地候选已实现，联合验收中）

固定 12 次按原始校验簇重排，既有物化/资源额度不调大；未知 written 先 RO，语义 Err 不转 Pending/copies，不以重复插入掩盖不确定结果。新事件仍由原 WAL 承接，actual-set 历史成员另保 immutable 证明，不能跳过活 WAL 子集 SQL 资格；不通过丢弃或过早回收换吞吐。旧 v8 全门、双短与 live 的原 hash 结论保留，新 r4 ordinary/race、全仓/raw Worker 1s/Search/双短/live/安全及 9 窗仍待绑定真实结果。FAST 短验曾大于 250ms、精确样本为 0，9×30min 继续 blocked，不记最优、验收或交付；准确协议见 [共享契约](../worker-log-platform-contract/spec.md#durable-outbox-r4本地候选已实现联合验收中)。

**r8d 单次事实刷新**：当前受控 4vCPU/8GiB/noSwap、60 源工作集的 20×1000 闭合事件真实 SQL 完成门已通过，精确读数及旧 run4～run7 失败/HOLD 历史见 [共享规格](../worker-log-platform-contract/spec.md#durable-outbox-r8d单次受控耐久门通过联合验收中)。这不是长期/最优或整个性能验收；轮尾 best-effort 元数据改交既有后台通道不改变 strict 新批 actual-plan SQL 门、正文/ACK/PP 责任，不新增预算或配置。新全仓、双短/live、Search 和 9×30min 仍待，本节旧 r4“1s 未测”仅指其原候选时点。

## 1. 不变量 R（区间守恒）——本项的一等不变量

**陈述**：对任何一个日志源，把本批次被消费的行按位置升序排列，其 `Record` 区间在
`rangesTouch` 口径下是一条连续覆盖；采集侧的每一次「抑制」都不得在这条覆盖上留下空洞。

**形式化**：`MergePositionRanges(Process(in)) == MergePositionRanges(in)`（逐段相等）。

**为什么是硬约束（而不是整洁性要求）**：缺口自动消解的判据是「缺口必须完全落在**某一段**
连续覆盖区间内」（`ledger.CoveredByPositionRanges`；复审 P1-1 明确否掉了凸包判定），
而连续覆盖区间由 `gapRangesOfEvents` 从**实际投递的事件**的 `Record` 区间折出。
若抑制让事件凭空消失，被抑制行的位置就落进空洞：任何跨越该位置的既有缺口
（例如一次 `DELIVER_ERROR` 登记的 `[1000,5000)`）将**永远无法自动消解**，
而 `ResumeAcquire` 要求「零未解决缺口」⇒ 该源被永久焊死在暂停态、不再产生新数据。
这正是 2026-10-01/02 那次「13 小时零新数据」事故的形态。

**实现方式**：抑制不丢弃，而是把被抑制的行替换为**汇总事件**：
- `Record` 区间 = 被抑制行的**精确并集跨度**（仅在区间相邻时合并，绝不跨越未被抑制的事件）；
- 走完整管线（WAL → 投递 → 逐字段校验 → 发布），与普通事件无差别；
- 被抑制条数、规则、级别、首条样例全文写进正文与字段。

**精确并集的含义（三条边界）**：
1. 只在 `d.event.Record.Start <= last.Record.End+1`（相邻）时合并 ⇒ 绝不是 min..max 凸包；
2. 段内必须**同级别、同流、同规则** ⇒ 汇总的 `level`/`stream` 字段不撒谎，跨级别的行不混入；
3. 段内必须**同源** ⇒ 汇总不得给别源的行盖自己的源印章。

**汇总事件的契约地位**：`EventID = logtypes.EventID(源, 区间, parser)`、
`CanonicalHash = CanonicalContentHash(时间, 级别, 流, 正文)`，即与普通事件**同一套推导**。
不得改用 `agg-<start>-<end>-<level>` 之类的人造 ID——投影校验按 `event_id` 做 `want`/`allowed`
查表（`verifyProjectionChunk`），人造 ID 会让汇总在校验表里查不到自己。
唯一性是推出来的：汇总的 `(start,end)` 是其成员并集，成员不再出现在输出里，
输出各事件区间两两不相交 ⇒ `(start,end)` 两两不同 ⇒ sha256 推导的 ID 两两不同。

**确定性与幂等**：抑制决策只依赖事件自身携带的数据（级别、流、正文、`EventTimeUTC`），
**不读挂钟**。同一条 WAL 条目重放必然得到同一结论与同一汇总事件（ID 与哈希逐字节一致），
故账本的 `(EventID, CanonicalHash)` 去重语义不受影响：重投是「多写一次」而不是「写出一份不同的东西」。
唯一的例外是风暴降级状态（由外部读数驱动），其差异只导致「多写原文」（安全方向）。

---

## 2. 策略与配置面

| 键 | 默认 | 语义 |
|---|---|---|
| `log_ingest.sampling.enabled` | `false` | 采样总开关（关 = 恒等，零行为变化） |
| `log_ingest.sampling.min_level` | 空 | 等级过滤：低于它折叠为汇总；**无法归类的级别一律放行** |
| `log_ingest.sampling.burst_window` / `burst_threshold` | `0` / `20` | 同源同消息高频抑制 |
| `log_ingest.sampling.budget_max_events_per_window` / `budget_window` / `budget_keep_every` | `0` / `1s` / `10` | 每源预算（quota → 采样比） |
| `log_ingest.sampling.degrade_enabled` / `degrade_target_level` / `degrade_disk_percent` / `degrade_hold` | `false` / `ERROR` / `80` / `30s` | 风暴自动降级 |
| `log_retention.enabled` | `false` | 保留策略总开关 |
| `log_retention.hot_retention` | `90d` | 热层窗口：超期即搬运到冷层（用户最终口径） |
| `log_retention.cold_retention` | `730d` | 冷层保留期（**所有级别统一**；由 COLD 实例 `-retentionPeriod` 执行） |
| `log_retention.trigger.disk_percent` / `min_age` | `0` / `7d` | 磁盘水位提前搬运（取先到）；`0` = 只按年龄 |
| `log_retention.trigger.sweep_at_hour` | `3` | 每日固定时刻执行一轮（进程本地时区）；`-1` = 回退 `trigger.interval` 的 ticker 兼容档 |
| `log_retention.discard` | `false` | **意图闸**：显式允许直接删 |
| `log_retention.by_level.*` | D1 档位 | 按级别 TTL（`3d`/`7d`/`30d`/`90d`，支持 `d`/`w`） |
| `log_retention.sources[].match` / `by_level` | — | 来源级覆盖（`*` 后缀为前缀匹配） |
| `log_retention.sweep.vl_sweep` / `interval` / `timeout` | `false` / `1h` / `30s` | **执行闸** |

**装配口径**：配置经 `Config.SamplingPolicy()` → `ingest.Options.Sampling` → Manager → 每源独立 Pipeline sampler；零值默认关闭。Poll/导入/Flush 前在共享锁外复用 CapacityProvider 更新降级状态，恢复重投使用已入 WAL 的事件，不再次采样。策略在 Load 与运行时入口按原值 Validate，不能先 Normalize 掩盖非法配置。

**VL retention 与产品 TTL 分开解析**：原字串 `90d/730d` 默认与关闭保留策略的行为保持兼容；受管 VL 参数按审批资产规则解析：裸数和大写 `M` 为固定 31 天月、`y` 为 365 天，小写 `m` 非法。产品 `hot_retention/cold_retention` 仍用产品 TTL 口径，不用 Go 分钟单位猜 VL 月份。启动受管 VL 前校验实际 namespace retention（含默认及空白回退）与 HOT/COLD 声明，HOT 180d/VL 90d、COLD 1000d/runtime 730d 等明显矛盾在启用保留策略时拒绝启动。有限 native TTL 仍会按日淘汰，不能承诺搬运长期失败或无限延迟时永不丢失；需在到期前观测失败并处置。

**搬运重试口径**：明确的 `FAILED_RETRYABLE` 冷层迁移进入 Resume（优先于 Owner 已切 COLD 的跳过分支），不能重复 Begin；活动迁移、`FAILED_MANUAL` 与未知状态不自动覆盖。续跑复用最后副本、校验、租约门禁与失败保留原物，不把租约排空/清理失败当作稳定冷层成功。

**取值口径**：`0` = 没填走默认，**负数 = 写错，启动即拒**。校验读**原始取值**而不是
`Normalize()` 之后的值——后者会把「阈值填 -1」悄悄当成「没填」，使校验成为死代码
（该缺陷由变异实验 M10 发现）。

**归并粒度按规则而定**（不是可选的细节）：等级过滤/风暴降级只按 `(级别, 流)` 归并；
高频抑制/预算才按 `(级别, 流, 模式)`。若等级过滤也按模式归并，一百条互不相同的 DEBUG
会产出一百条各自只有一条的汇总——体积没省下来，还多了一层包装。

---

## 3. 决策记录

### D0 默认口径定稿（2026-10-02）：热 90d / 冷 730d — 已采纳
`hot_retention` 默认 `90d`、`cold_retention` 默认 `730d`。**语义定稿**：D1 的分级档位表达的是
「**在热层停留多久**」；**冷层所有级别统一**（730d），不做级别档位——冷层的目的只有长期留存，
再分档只会让「同一条日志在两层的保留期不一致」变成常态。到期动作不变：**搬**（不裸删，双闸默认关）。

**连带修掉的一致性风险**：VL 自身的 `-retentionPeriod` 到期是**直接删**，它**绕过**「删前归档」——
若热层实例 retention（原默认 30d）短于 `hot_retention`（现 90d），VL 会在搬运驱动器之前先删掉数据，
硬规则被静默架空。故：`log_vl.retention_period` 默认改为 `90d`（与热层窗口对齐，留出搬运余量），
新增 `log_vl.cold_retention_period` 默认 `730d`，并让 `vlsup` 支持**按 namespace 的 retention**
（`RetentionByNamespace`，未指定时回退）。回归见 §6.9。

### D1 热层分级（保留期档位）— 已采纳
`debug 3d / info 7d / warn 30d / error 90d`。原为「分级删除」，随 D4 改为**分级搬运/归档**：
段搬运（年龄到）→ 整分区搬 COLD（复用 `catalog`/`lifecycle`）。当前热层窗口默认 90d
（以 D0 定稿为准），原始运行时 retention 与声明的交叉校验见 §2。

### D2 冷层保留 — 已采纳（当前数值以 D0 定稿为准）
COLD 默认保留期为 730d、所有级别统一，HOT 默认 90d；按 namespace retention 已接线，
查询路径保留 Rehydrate 回灌能力。
**落点**：中心侧 HOT/COLD/Rehydrate 三段路径；instance-local gz 只是回退溯源来源，
**不在本项范围**（本项不碰实例本地归档）。

### D3 搬运/删除/回灌全部进 G10 观测面 — 部分采纳（见 §7）
要求「谁、何时、多少、去哪」可随时审计。`ArchiveOutcome` 已含
`Result`/`TargetDirID`/`At`/`Err`，`ArchiveStats` 已含计数与 `LastError`；
**对外暴露点（日志行 + 指标）尚未接线**。

### D4 删前归档 — 已采纳（硬规则，默认开启）
保留期到期数据**绝不裸删**，三选一，①为默认：
1. **首选**：搬运到 COLD（复用 `ROUTING_FROZEN→…→CLEANED` 状态机）——搬运成功 + 校验通过
   才允许 HOT 侧回收；搬运失败 = **保留原物 + 告警**（宁占盘不丢数据）；
2. 次选：**导出为独立副本**（`retention.ActionArchiveCopy`）——已实现，见 §4.1；
   导出 + 逐文件回读校验通过**且删除三闸全开**才允许回收热层，否则副本与原物并存。
   **当前生产未装配 `Remover`**，故实际形态恒为「导出+校验成功 ⇒ 保留原物」（热层不释放），理由见 §4.1 与 §7；
3. **仅当**配置显式 `discard: true` 才允许直接删。

**次序不可调换**（`retention.PlanArchive`）：`move_to_cold` → `archive_copy` → `discard` → `blocked`。
搬运判在导出与删除**之前**。即使运维显式写了 `discard: true`，只要冷层路径可用，默认动作仍是
**搬运**；即使 `discard: true`，只要有归档目的地，默认动作也是**导出归档**而不是删除——
`discard` 的作用是解除「没有归档路径时的阻塞」，不是把默认动作改成删除。
**该次序是变异实验 N13 暴露出来的真实缺陷**：初版把 `discard` 判在前面，
语义变成「只要曾显式开启过删除，默认动作就永久变成删除」，与规则意图相反。

**两道互相独立的闸**（而非一个开关）：执行闸 `sweep.vl_sweep` + 意图闸 `discard`，
两层都满足才可达删除，且受管 VL 还需带 `-delete.enable`（VL 自身默认关，实测未带时
`/delete/*` 返回 400）。理由：这条路径后果不可逆，一处误开与两处同时误开的概率差得很远。
默认路径永远是搬运（数据仍在、仍可查），使**所有配置错误的最坏后果从「数据永久丢失」
降级为「盘没省下来」**。

### 4.1 次选路径 `archive_copy`（导出 → 压缩 → 校验 → 回收）

**语义**：搬不了冷层、但有归档目的地时，导出一份独立副本，而不是留一堆 blocked 的原物。
它是 `blocked` 的替代，不是 `move_to_cold` 的替代（次序见 D4）。

| 环节 | 实现 | 复用而非新造 |
|---|---|---|
| 冻结 | `lifecycle.VLPartitionOps.snapshot` | 既有双闸 `catalog.CheckPhysicalDayDrained`（`day.Frozen` 为真 + `dayPermitCountLocked`==0）+ `prepareHotSource`（force_flush）+ `/internal/partition/snapshot/create`（返回长度必须为 1 且位于受管 snapshots 根下） |
| 压缩 | 逐文件 gzip（`gzip.BestCompression`），归档内路径 `raw/files/<源相对路径>.gz` | 压缩头留空（不写 Name/ModTime）⇒ 同输入同输出，重跑幂等 |
| 清单 | `archive.Manifest`（`archive-manifest/v1`） | 与 FR-478 `logs/archive` 同一 manifest 契约：`checksums`（sha256）/`raw_files`/`coverage`/`time_range` |
| 校验 | 每个对象 `Put` 后**回读 + sha256 比对 + 解压还原比对源字节**；manifest 也回读并重新 `Validate` | `archive.ContentHash` / `archive.ObjectIDOf` / `UnmarshalManifest` |
| 回收 | `Remover.RemovePartition`，前置 `retention.Policy.DeleteGatesOpen()` **与 Catalog 权威态迁移** | **与 `ActionDiscard` 同一套三闸**；生产实现 `VLPartitionOps.RemoveArchivedPartition` 每次删除前都重新向 `catalog.CanReclaimArchivedDay` 取结论（拒绝「带病调用」），并先 detach 再删目录（在 VL 持句柄时删只会解链接、磁盘不释放） |

**不变量（每条都有对应用例）**：

- 导出失败 / 校验不通过 ⇒ `ArchiveKeptOriginal`，**绝不触达回收**，并**解除**导出期建立的物理日冻结；
- 导出+校验成功但三闸未全开 ⇒ `ArchiveCopiedKeptOriginal`（副本与原物并存），**零删除**，权威态不变；
- 三闸全开但删除出口未装配 ⇒ 同上（宁占盘不假报回收）；
- 三闸全开但**未装配归档权威态出口** ⇒ 同上：没有 ARCHIVED 权威态就不许删；
- 权威态迁移成功但删除失败 ⇒ `ArchiveCopiedKeptOriginal` + **回滚 ARCHIVED→HOT**（恢复 owner/dir/generation/原投影/写路由），`ArchiveRolledBack` 计数 +1；
- 删除成功 ⇒ `ArchiveCopied` + `ArchiveCopyReclaimed`，并标记 `HotReclaimed`；
- 已回收后**拒绝**回滚（数据已不在热层，回滚等于伪造）；
- `MarkArchived` 对同 `(provider, prefix)` **幂等**：重跑不追加 journal、不改归档时刻；不同归档位置一律拒绝（一天两份归档必须人工裁定）；
- `retryable`（已有 COLD 迁移失败待续跑）**不会**被导出归档「救」成 `archive_copy`：
  导出不推进也不修复迁移状态机，保持 `blocked` 才是诚实的。

**默认关闭**：`log_retention.archive_copy.enabled: false`；启用但 `root` 为空 ⇒ 启动即拒。

**接线现状（2026-10）**：导出口、归档权威态出口（`newCatalogArchiveAuthority`）与删除出口
（`physical.RemoveArchivedPartition`）在 `apps/worker` **均已装配**；三闸仍默认全关，
故默认部署形态依然是「导出 + 校验成功 ⇒ 保留原物」。装配 `ArchiveReadyForReclaim()` 在启动日志
打印「是否已具备回收条件」，使现场能一眼分辨「没开导出」与「开了导出但闸门没开」。
导出**目的地当前仅本地目录**；`archive.RemoteS3Provider`（S3/MinIO）已存在但未接线，SFTP/WebDAV 无实现。

**已知限制（如实登记）**：归档按**物理日**冻结（`partitions/<utc-day>/` 装着当天所有命名空间），
回收同样按日删除，且 `CanReclaimArchivedDay` 要求该日**无 HOT 残留**——因此一天有多个命名空间时，
需要它们都被归档后才回收，中间轮次是「已归档 + 保留原物」。若进程恰在 `MarkArchived` 与删除之间退出，
会留下「已归档但热层未回收」的安全方向残留，启动日志会点名报出（`PendingArchiveReclaims`），
由下一轮保留策略按三闸重试。


### D5 观测面 — 部分采纳（见 §7）
运行期可动态调整日志等级（debug 用于现场定位、平时压到 info 以降开销）——
**已落地**：`log.level`/`log.format` 有装配点（此前 `log.level: debug` 完全未接到 slog handler），
且装配出的 `slog.LevelVar` 已通过 `/debug/log-level` 暴露为运行期可调入口（GET 读当前值 / POST 改值），
做到**改级别不重启**（见 §6.8 与 §7）。边界：只调等级不改格式、不持久化、仅影响本进程 handler。

---

## 4. 保留策略：动作模型与判定

```go
const (
  ActionMoveToCold  = "move_to_cold"  // 首选
  ActionArchiveCopy = "archive_copy"  // 次选（已实现，见 §4.1）
  ActionDiscard     = "discard"       // 仅显式 discard 可达
  ActionBlocked     = "blocked"       // 无归档路径且未显式放弃 ⇒ 保留原物 + 告警
)
```

执行结果（`ArchiveOutcome.Result`）除既有 `moved`/`kept_original`/`discarded`/`not_due` 外，
新增 `copied`（导出+校验成功且热层已回收）与 `copied_kept_original`
（导出+校验成功但保留原物）。两者都「原物还在」但成因相反，合并会让
「归档一直在成功、只是闸门没开」与「归档一直失败」在读数上无法区分。

**删除过滤器由本包生成，绝不从配置读**（安全关键）。VL 的删除接口接受 LogsQL 过滤器，
若把配置字符串直接当过滤器，一次误写（`filter: "*"`）就能在几分钟内删掉全部日志且不可逆。
因此：级别必须命中白名单（`TRACE/DEBUG/INFO/WARN/ERROR`），源标识必须通过字符集白名单
`^[A-Za-z0-9_.:-]{1,128}$`（`ValidSourceID`），过滤器由二者拼出。
源标识含冒号，**必须加引号**（`log_source_id:"inst:147"`），否则会被解析成字段过滤、语义完全不同。

**计划不按「源 × 级别」展开**：只有写了来源覆盖的源才单独成一条目标，其余走「全实例 + 该级别」。
61 个源 × 5 级别的展开会产出数百个删除任务，而删除接口每次都要扫一遍索引，代价与收益完全不成比例。

**已知边界（粒度张力，如实登记）**：日分区里混着所有级别，而搬运的最小粒度就是「一天」
（`catalog.PartitionKey` 是 `(namespace, utcDay)`）。因此按级别的 TTL **无法**在热层独立生效：
`HotWindow()` 默认取显式 `hot_retention`（90d）；若未配则回退到「级别 TTL 最长档」（= 90d，ERROR 那档）。
按最长档取的代价是 DEBUG 会跟着多留（成本没省到头），但**承诺不失守**。
要做到真正独立的按级别热层保留，需要把级别分流到不同存储命名空间——
与采样侧「把低价值等级路由到独立流」是同一类改动，见 §7 的未做栏。

---

### 2026-10-22 隔离删除边界验证

`ClientDeleter` 只接受代码白名单可生成的规范级别/源过滤器，将时间限制直接合入 filter；批准引擎的闭区间用 `cutoff-1ns` 实现 `_time < cutoff`，不再把独立 start/end 当删除边界。纳秒范围溢出/下溢拒绝。

已在核验 SHA 的批准 v1.52.0、独立回环端口与全新构造数据根验证：输入 6 条，任务完成后精确保留 4 条，cutoff 本身、之后 1ns、其它源与其它级别保留，之前的目标记录删除，隔离引擎正常退出。恢复旧错误 adapter 的隔离 overlay 对同一集合断言真实转红，非编译失败。仅该隔离测试启用 delete，生产三闸与 Sweeper 装配不变。

复验入口：`JM_VL_BIN="$VL_BINARY" go test -p 1 ./internal/worker/logs/retention -run TestApprovedVLDeleteContractExactNanosecondSet -count=1 -v`；`VL_BINARY` 应由本地批准资产注入，测试内核对审批 SHA，缺该环境导致 Skip 不算通过。完整资源/故障/持续性能仍待联合验收。

## 5. 实测：VictoriaLogs v1.52.0 能力边界

| 能力 | 实测结果 |
|---|---|
| per-level / per-tenant retention | **不存在**。`-retentionPeriod` 是实例级单一值，最小 1d、默认 7d |
| `/delete/*` | **存在**，真实路由是 `/delete/run_task`、`/delete/active_tasks`、`/delete/stop_task`（不是 `/delete`） |
| 删除调用形态 | `POST /delete/run_task?filter=<LogsQL>[&start=][&end=]` → `{"task_id":"..."}`；`filter` 是参数名（不是 `query`/`match[]`）；`| delete` 管道**不**被接受 |
| 门禁 | 未开 `-delete.enable` → 400 `requests to /delete/* are disabled`；受 basic auth 保护（无凭据 401） |
| 生效时点 | 异步后台任务；需等待该 task 从 `active_tasks` 消失并核对精确保留集合，受理不等于完成 |
| per-level 真删 | 实测可行：`filter=level:debug` 删净 debug、info/error 原样保留 |
| `-retention.maxDiskSpaceUsageBytes` / `maxDiskUsagePercent` | **存在**（超限自动丢最老 per-day 分区），**尚未接线** |

⇒ per-level TTL 在 VL 侧只有两条路：(a) 按级别路由到不同 namespace/实例（数据面重构，需运维决策）；
(b) `/delete` 定期扫描（需 `-delete.enable`，删除是异步后台作业）。本项取 (b) 且默认关闭。

---

## 6. 测试用例清单

### 6.1 `logs/sampling`（不变量 R）
- `TestInvariantR1_RangeUnionPreservedExactly` — 随机 40 轮 × 5 组策略，逐段相等
- `TestInvariantR1b_InterleavedLevelsWithChaoticClock` — 时间戳乱序下仍成立
- `TestInvariantR2_GapStillAutoResolvesAcrossSuppressedSpan` — **缺口跨越被抑制区间仍能自动消解**（真实账本 + 真实判据 + 真实允许名单）
- `TestInvariantR2b_NaiveDropBreaksGapResolution` — **反证**：朴素丢弃下同一缺口消解不掉
- `TestInvariantR3_NonContiguousInputKeepsItsHoles` — 输入自带空洞必须原样保留（不得缝成凸包）
- `TestInvariantR3b_NoSuppressionIsExactIdentity` — 无抑制时逐字段恒等
- `TestInvariantR4_AggregateIsAnOrdinaryEvent` — 汇总的 ID/哈希与契约推导一致

### 6.2 `logs/sampling`（投递契约）
- `TestDeliveryOrderIsMonotonicAndNonOverlapping` — 就地产出、不失序、不重叠
- `TestMaxRecordEndIsPreserved` — 批内最大 `record_end` 不回退
- `TestEventIDIsUniqueWithinBatchAndContractDerived` — 批内唯一 + 契约推导形式
- `TestAggregateNeverShrinksOrGrowsSpanBeyondMembers` — 不收缩、不越界（不圈入未被抑制的行）

### 6.3 `logs/sampling`（策略行为）
- `TestDisabledPolicyIsIdentity`、`TestLevelFilterFoldsBelowMinLevel`、`TestLevelFilterKeepsUnknownLevel`
- `TestBurstSuppressCountsAndFolds`、`TestBurstWindowExpiry`、`TestBurstDifferentMessagesNotFolded`、`TestBurstUnparseableTimeNeverSuppresses`
- `TestBudgetSamplingRatio`、`TestBudgetDeterministicAcrossRuns`
- `TestMixedLevelsNeverShareAggregate`、`TestMixedSourceInOneBatchNotMerged`、`TestPerSourceIsolation`
- `TestAggregatePayloadIsCompleteAndHonest`、`TestMaxAggregateEventsSplitsRun`、`TestSignatureTableIsBounded`
- `TestStormDegradeStateMachine`、`TestDegradeOnlyRaisesMinLevel`、`TestStatsReporting`
- `TestRankAndLevelBelow`、`TestMaskMessageIsDeterministicAndMasksVolatile`、`TestTruncateRunesKeepsValidUTF8`、`TestSignatureSeparatesLevelAndStream`、`TestPolicyValidateRejectsAndDefaults`

### 6.4 `logs/retention`（硬规则「删前归档」）
- `TestArchiveRule1_MoveFailureKeepsOriginalAndAlerts` — 搬运失败 ⇒ 保留原物 + 告警，**零删除**
- `TestArchiveRule2_ExpiredDataTakesMoveNotDelete` — 到期且有 COLD 路径 ⇒ 必走搬运
- `TestArchiveRule3_NeverNakedDeletesWithoutExplicitDiscard` — 穷举 enabled/vlSweep/hasMover 组合，**零删除**
- `TestArchiveRule3b_NoPathMeansBlockedNotDelete` — 无路径 ⇒ blocked + 告警，不是删
- `TestArchiveRule4_MoveYieldsAuditableTargetAndLeavesUndueData` — 承接物可审计 + 未到点不动
- `TestArchivePrefersMoveEvenWhenDiscardIsExplicit` — **次序**：能搬就搬（N13 缺陷的闸门）
- `TestArchiveExplicitDiscardIsReachable` — **正向对照**：显式放弃后删除确实可达（否则 Rule3 恒真）
- `TestArchiveSkipsUnparseableDay` — 日期不可解析不猜、不动手

#### 6.4.1 次选路径 `archive_copy`（`logs/retention` + `logs/lifecycle`）

- `TestArchiveCopyPlannedWhenMoverMissingButArchiverReady` — 有归档目的地 ⇒ 计划 `archive_copy` 而非 `blocked`
- `TestArchiveCopyNeverOutranksMoveToCold` — **次序**：冷层可用时仍搬运，且**零导出调用**
- `TestArchiveCopyOutranksDiscard` — **次序**：有归档目的地时即使 `discard: true` 也不裸删
- `TestArchiveCopyExportFailureKeepsOriginal` — 导出失败 ⇒ 保留原物，**零回收**
- `TestArchiveCopyVerificationFailureKeepsOriginal` — 校验不通过 ⇒ 保留原物，**零回收**
- `TestArchiveCopyReclaimRequiresAllThreeGates` — 三闸（`discard` / `vl_sweep` / `delete_enabled`）逐闸为缺 ⇒ 均 `copied_kept_original`，**零回收**
- `TestArchiveCopyReclaimsWhenVerifiedAndGatesOpen` — 校验通过 + 三闸全开 ⇒ `copied` 且回收
- `TestArchiveCopyWithoutRemoverKeepsOriginal` — 三闸开但无删除出口 ⇒ 保留原物（不假报回收）
- `TestArchiveCopyReclaimFailureKeepsOriginal` — 回收本身失败 ⇒ 保留原物 + 告警
- `TestArchiveCopyDisabledKeepsBlockedBehaviour` — 默认关闭 ⇒ 行为与改动前完全一致（零导出调用）
- `TestArchiveCopyEnabledWithoutExporterStaysBlocked` — 开关开但出口未装配 ⇒ 仍 `blocked`（不承诺做不到的事）
- `TestArchiveCopyNeverSubstitutesForMigrationRetry` — `retryable` 迁移不被导出「救」成 archive_copy
- `TestDeleteGatesOpenReportsMissingGate` — 三闸判定点名缺失的那道闸
- `TestExportTreeGzipRoundTrip` — **gzip 往返一致性**（含空文件边界）
- `TestExportTreeManifestFollowsArchiveContract` — manifest 字段/`checksums`/`coverage`/`time_range`/`object_id` 逐项对齐 FR-478 契约
- `TestExportTreeIsIdempotent` — 重跑导出产出**逐字节相同**的 manifest
- `TestExportTreeFailsWhenReadBackTampered` — 回读被篡改 ⇒ 报错（校验真的在跑）
- `TestExportTreeRejectsEmptySnapshot` — 空快照不得产出成功（否则空副本 + 三闸 = 永久删除）
- `TestExportTreeRejectsSymlink` — 快照内符号链接一律拒绝
- 配置接线（`internal/worker`）：`TestRetentionArchiveCopyDefaultsOff`、`TestRetentionArchiveCopyEnabledWithoutRootRejected`、`TestRetentionArchiveCopyAndVLDeleteGateTakesEffect`、`TestRetentionArchiveCopyRootFromConfig`

### 6.5 `logs/retention`（策略 / 计划 / 执行器 / HTTP）
- `TestDefaultPolicyMatchesRecommendedTTLs`、`TestNormalizeFillsMissingLevelWithRecommended`、`TestEffectiveTTLUnknownLevelIsKeepForever`、`TestEffectiveTTLSourceOverridePrecedence`、`TestValidateRejectsHazardousConfig`
- `TestBuildFilterRejectsInjection`（12 类注入载荷）、`TestBuildFilterShapesAndQuotesSource`
- `TestPlanDoesNotExpandEverySource`、`TestPlanSkipsKeepForeverAndOverridesMatchingGlobal`、`TestPlanWhenDisabledIsEmpty`
- `TestSweeperDryRunNeverDeletes`、`TestSweeperSubmitsCorrectFilterAndCutoff`、`TestSweeperThrottlesRepeatedFilters`、`TestSweeperIsolatesPerTargetFailures`、`TestSweeperDisabledPolicyIsNoop`
- `TestClientDeleterBuildsRequest`、`TestClientDeleterRejectsMissingTaskID`、`TestClientDeleterRejectsMalformedBody`、`TestClientDeleterSurfacesHTTPError`、`TestClientDeleterWithoutClientFails`、`TestSweeperToRealHTTPEndToEnd`

## 6.7 `logs/retention`（G6 冷层搬运驱动器）

- `TestDriverMovesDuePartitions` — **到点必搬**（驱动器的存在理由）
- `TestDriverKeepsOriginalOnMoveFailure` — **搬运失败留存 + 告警**，零删除
- `TestDriverSkipsColdAndInFlight` — 已在冷层 / 在途迁移不得重复驱动
- `TestDriverDiskWatermarkTriggersEarlyMove` — 磁盘水位触发（年龄 + 水位取先到）
- `TestDriverDiskTriggerRespectsMinAge` — 盘再满也不搬刚写进来的分区
- `TestDriverDiskReadFailureFallsBackToAgeOnly` — 读数拿不到就退回纯年龄，不猜「盘满了」
- `TestDriverNoopWhenDisabledOrEmpty` — 未启用 / 无分区时彻底空操作
- `TestDriverListFailureSurfaces` — 列举失败如实报错（区分「没有分区」与「读不出来」）
- `TestDriverWithoutMoverBlocksInsteadOfDeleting` — 无搬运路径 ⇒ blocked + 保留原物
- `TestDriverRunStopsOnContextCancel` — 周期循环随 ctx 退出且**首轮立即执行**

## 6.8 配置接线（`internal/worker`）

- `TestLogLevelIsParsedAndRejectsGarbage` — `log.level` **有装配点**且非法值启动即拒。
  装配用 `slog.LevelVar`，并已通过 `retention.NewLogLevelController` + `/debug/log-level`
  暴露为**运行期可调**（GET 读 / POST 改）⇒ 构成「运行期调级」；边界见 §6.8（只调等级不改格式、不持久化）
- `TestLogFormatIsParsedAndRejectsGarbage` — `log.format` 同上
- `TestMaxWALBytesHasFiniteDefault` — WAL 字节上界默认有限，且高于正常稳态量级
- `TestWALBudgetNoticeNamesTheUnlimitedCase` — 显式不限必须被点名

## 6.9 热/冷默认口径与按 namespace retention

- `TestSupervisorRetentionPerNamespace` — VL retention **可按 namespace 分开**（热 90d / 冷 730d），
  未指定的 namespace 回退，**空白值不得被当成有效覆盖**
- `TestDefaultPolicyMatchesRecommendedTTLs` — 热 90d / 冷 730d 默认，且冷层不得按级别分档

### 6.6 变异实验（`go test -overlay`，原文件零改动）
- `logs/sampling`：**11/11 全部以断言转红**（变异报告落在 `.tmp/`，不入库 ⇒ 按其方法自行复跑）
- `logs/retention`：**21/21 全部以断言转红**（编号 N1–N21；变异报告落在 `.tmp/`，不入库）

**计数口径（全仓引用处以此为准）**：`retention` 的 21 条 = 策略/计划/执行器/VL 客户端 16 条 + 冷层搬运驱动器新增 5 条（N17–N21）；`sampling` 11 条；两包**合计 32/32**。

历史数字的换算（三个数不相等，但都可由此推出，非互相矛盾）：`692e714b` 的 **27/27** = sampling 11 + retention 16（驱动器尚未接线）；`282c60af` 的 **32/32** = 两包合计（该提交信息把它挂在 "retention" 名下，属当时表述不精确）；`08eb2386` 的 **21/21 + 11/11** = 定稿后的分列口径。ADR-098/ADR-099 与 ARCHITECTURE 引用时统一写作「retention 21/21（含驱动器 5 条）+ sampling 11/11，合计 32/32」。

其中两条变异直接暴露了实现缺陷并据此修复：**M10**（校验读 `Normalize()` 之后 ⇒ 成死代码）、
**N13**（`discard` 判在搬运之前 ⇒ 默认动作被永久改成删除）。

---

## 7. 未做 / 需外部决策（如实登记）

| 项 | 状态 | 说明 |
|---|---|---|
| 冷层搬运**驱动器** | **已接线** | `retention.Driver`（`Step` 可单测 + `Run` **每日固定时刻调度**，时刻由 `log_retention.trigger.sweep_at_hour` 决定（默认 3 点、进程本地时区），启动首轮立即执行）；生产适配层 `apps/worker/log_retention.go` 把 `catalog` 分区与 `lifecycle.DayManager` 接成 `PartitionLister`/`Mover`，在采集运行时之后随同一 ctx 启动。触发口径 = 年龄 + 磁盘水位**取先到**（`log_retention.trigger.*`，默认只按年龄）；磁盘读数复用采集侧**同一份** `ingest.DiskCapacityProvider`。磁盘触发带 `min_age` 下限（默认 7d）：盘再满也不搬正在写的日分区 |
| `ActionArchiveCopy`（导出独立副本） | **已闭环**（含热层回收） | 计划/执行/压缩/清单/校验全链路：`retention` 侧第四支 + `ArchiveExporter` 接口 + 三闸判定；落地实现 `lifecycle.ArchiveCopyExporter`（冻结快照、逐文件 gzip、`archive.Manifest` 契约、回读 sha256 + 解压还原双校验 + manifest 重校验），并输出归档证据（`ProviderKind`/`ObjectPrefix`/`ManifestChecksum`/`CoverageComplete`）；配置 `log_retention.archive_copy.{enabled,provider,root,endpoint,bucket,region,prefix,access_key,secret_key,use_ssl}`（默认关闭；local 需 root、s3/minio 需 endpoint+bucket，缺则启动即拒）；`apps/worker` 用 `newArchiveCopyProvider` 按 provider 构造导出器、`newCatalogArchiveAuthority` 权威态出口与 `physical.RemoveArchivedPartition` 删除出口。<br>**热层回收（2026-10 打通）**：次序为 `冻结物理日 → 导出+校验 → MarkArchived（Catalog journal）→ 删除热层 → MarkHotReclaimed`。`MarkArchived` 把 `Owner` 改为 `archive`、`OwnerDirID` 改为归档标识（不再指向热层目录）、清空旧投影、冻结写入路由，并把归档证据（provider/prefix/manifest sha256/coverage/归档时刻/迁移前权威快照）与退役热层目录一并落盘；删除失败 `RollbackArchived` 恢复 owner/dir/generation/原投影与写入路由（查询能力不丢）；已回收后拒绝回滚。删除前 `catalog.CanReclaimArchivedDay` 复核四项：同日无 HOT 残留、归档证据完整、无在途写许可、无活跃查询租约/视图。`MarkArchived` 对同 `(provider,prefix)` 幂等（不追加 journal、不改归档时刻）。<br>**每一条未归档的退出路径都会解除物理日冻结**（导出失败/三闸未全开/无删除出口/权威态迁移失败），否则该日会永久拒绝写入。<br>**查询面**：ARCHIVED 且本地未恢复 ⇒ `LOG_COVERAGE_ARCHIVE_MISSING` + `ARCHIVE_NOT_RESTORED`（不再误报 `NO_CATALOG_AUTHORITY`），Search 无任何可读 range 时返回 `LOG_ARCHIVE_MISSING`（**不是**空成功）；已恢复（对齐投影）⇒ `archive` tier ⇒ Rehydrate 客户端。<br>**目的地范围（2026-10-27 更新：s3/minio 已接线）**：`log_retention.archive_copy.provider` 支持 `local`（默认，需 `root`）/ `s3` / `minio`（需 `endpoint`+`bucket`，另有 `region`/`prefix`/`access_key`/`secret_key`/`use_ssl`）。启用但目的地不完整一律**启动即拒**（与 `archive_copy.root` 同一取舍：静默按未启用处理会让运维以为归档在跑）。s3/minio 分支复用 FR-478 已真机验过的 `archive.RemoteS3Provider`（纯标准库 path-style SigV4，底层 `internal/worker/storage/s3.go`），不新造协议也不新造凭证模型；对象键布局与本地完全同构（`[prefix/]<ns>/<day>/g<gen>/<rel>`）。装配层构造失败时**只降级导出器**（不装出口 ⇒ 计划不产出 archive_copy ⇒ 到期分区落 blocked 保留原物），不让 Worker 启动失败。SFTP/WebDAV 仍无实现。<br>**已登记边界**：`Provider` 契约是 `Put(... []byte)`，单文件整文件载入内存再压缩；s3 场景 `Get` 还要 `io.ReadAll` 整对象做回读校验——每个对象多一次完整下行。<br>**未做**：归档对象自身的回收（桶内生命周期）与长期吸存验证 |

| eventstore 段/归档对象的回收 | **部分接线**（段已接、归档对象未做） | `eventstore.Store.Prune` 已接入自动调用路径：`ingest.Manager.PruneEventStoreSegments`（实现在 `internal/worker/logs/ingest/eventstore_prune.go`），由 `Manager.RunEventStorePrune` 按**每日本地整点**驱动（默认 3 点），在 `apps/worker/main.go` 与保留驱动器同 ctx 启动。**默认关闭**（`log_retention.eventstore_prune.enabled`，删除类能力默认全关），需显式开启。<br>水位判据 = `min(Positions.Reclaim, 逐字段校验覆盖的连续前缀末端)`：<br>① **不是** `Positions.Delivery`——它的语义是「所有字节均有**请求结果**的连续前缀末端」，请求失败（`DeliveryUnknown`）同样会推进它，用它当水位会删掉仍需重放的事件正文；<br>② 逐字段校验证据取 `persistedSource.VerifiedRuns`（区间集合，相接会被合并），只取**锚点所在那一段**的末端（锚点 = 段内最小 `record_end`），绝不取「所有区间的最大末端」——那会把中间空洞一并宣称已覆盖；<br>③ 夹 `Reclaim` 是因为 WAL 只丢 `record_end <= Reclaim` 的条目，否则被回收身份会从「段内 seen 集合」消失却仍留在 durable WAL ⇒ 被重新判缺、重新追加、VL 出现重复行；<br>④ `VerifiedRuns` 的有界保留（`maxVerifiedRunsPerSource = 64`）丢的是**位置最旧**的区间 ⇒ 锚点段先消失 ⇒ 水位退化 0 ⇒ 少删（安全方向），不可能抬高水位。<br>契约变更：段不再是「VL 数据根丢失后重建 projection 的权威集合」，改为「投递完成前的暂存与重放来源，投递并逐字段校验通过后可回收」——原档由用户在各服务器保留文本日志，平台不承担长期保存责任。<br>回收后 `invalidateEventStoreRowCaches` 推进 `eventsStoredEpoch` 并丢弃 `eventsStoredRows`/`storedCoverageMemo`；`proof.rows` 类判据保持原样（回收后是 fail-loud 的 `errPublicationStale`，不放宽）。<br>**仍未做**：归档对象的回收路径；`CanonicalDayMigrationInput` 对被回收天会硬失败（响亮）、`locateEventAtOrAfter` 对被回收区间只能看到剩余段 |
| COLD/Rehydrate 的 VL 启动参数 | **部分接线** | 按 namespace 差异化 `-retentionPeriod` 已接线，HOT/COLD 默认 90d/730d；此项不等于开启原生空间淘汰或删除，删除仍受既有三闸且默认不开 |
| G10 观测面对外暴露点 | **部分** | `ArchiveStats`/`SweepStats`/`DriverResult`/`sampling.Stats` 已具备读数，驱动器每轮打印 listed/skipped/due/moved/keptOriginal/notDue；`/metrics` 端点**已接线**（Prometheus 格式实机 200），序列由 8 条扩到 **18 条**——新增 3 条导出归档序列（`jm_log_retention_archive_{copied,copy_reclaimed,copy_kept_original}_total`）+ 1 条回滚序列（`jm_log_retention_archive_rolled_back_total`，已迁移 ARCHIVED 但回收失败而退回 HOT 的次数）+ 6 条事件段回收序列（`jm_log_eventstore_prune_{enabled,last_run_timestamp_seconds,reclaimed_segments_total,reclaimed_bytes_total,reclaimed_events_total,failures}`），未启用时同样输出 0 而非缺序列；事件段回收读数取 `Manager.LastEventStorePrune()` 快照而非现算（抓取高频，现算会把一次抓取变成一次全量段扫描）。`log.level` 已接线，且运行期调级**已做**：`/debug/log-level`（GET 读 / POST 改）实机验证 info→debug 即刻出现 DEBUG 行；边界为只调等级不改格式、不持久化、仅本进程 handler |
| 分级验证（info/debug 只存不验） | 未做 | 需落在 `ingest/runtime.go` 的 `verifyProjection` 调用点（该文件由并行工作流持有） |
| 按级别独立热层保留 | 未做（可选路径） | 需把级别分流到不同存储命名空间（见 §4 已知边界），属数据面重构 |
| `log_capacity.max_wal_bytes` 有限默认 + 告警 | **已做** | 默认由 `0`（不限）改为 `DefaultMaxWALBytes`（512MiB，远高于正常稳态的 KB 量级，只在真失控时触发 PAUSED + 登记缺口）；显式写 0 时启动日志点名提醒（`WALBudgetNotice`），使「没配」与「配成不限」不再不可区分 |
| 真机部署复验 | 未做 | 本项**不含部署动作**，未触碰生产与用户实例 |

---

## 8. 回滚

- 采样：`log_ingest.sampling.enabled: false`（即默认值）⇒ `Process` 恒等返回入参，逐指令等价于改动前。
- 保留：`log_retention.enabled: false`（默认）⇒ 不计算、不暴露、不删除。
- 导出归档：`log_retention.archive_copy.enabled: false`（默认）⇒ 不装配导出器、不产出 `archive_copy` 目标，
  行为与改动前逐指令等价。
- 删除路径：`sweep.vl_sweep: false`（默认）**或** `discard: false`（默认）**或** `log_vl.delete_enabled: false`（默认），
  任一即可关闭；三者独立（`archive_copy` 的回收热层走**同一套**三闸，不是另立一套）。
