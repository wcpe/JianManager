# 采集索引迁移至本地 SQLite（FR-496）

> 状态：📋 计划　·　关联 PRD：FR-496　·　关联 ADR：ADR-094（Worker 日志本地数据面）、ADR-095（磁盘权威事件存储）、ADR-093（进程生命周期韧性）
> 依赖：无（可先行）　·　被依赖：FR-497（增量对账依赖索引可精确记账）

## 1. 背景与目标

现采集侧索引为**单一 JSON 文件**（`data/var/log/ingest.state.json`），由 `ingest.Manager.persist` **整本重写**：

- 生产实测：**307MB / 13 个源**（2026-09-30）；单实例日志量大时增长更快（历史事故：索引 ~1.2GB 时单次持久化 >5s，`vlsup` 5s 超时 → 采集静默停摆）；
- 目标形态为**单节点 60 实例**：按 24MB/源外推索引将达 **1.5–3GB** → 每次交付批次都整本重写 → 秒级到数十秒级阻塞，采集吞吐被索引写入拖死。

**目标**：索引改为**本地嵌入数据库（SQLite）**，写入成本从 O(全量) 降到 O(变更行)；保持「不依赖 CP/网络」的自治性（CP 挂掉采集照常记账）；崩溃不丢索引。

**范围内**：索引的存储格式与读写路径、老 state 一次性迁移、配套回归与真机验证。
**不做**：不改交付/投递语义（仍为 FR-497 的条数级对账口径）；不引入远程数据库（违反 HA 要求）；不改账本事件体（`events/`）与 VL 数据面。

## 2. 设计

### 2.1 选型

- **`modernc.org/sqlite`（纯 Go，无 cgo）**：生产构建 `CGO_ENABLED=0` 可直接编译；不引入系统依赖。
- 库文件：`data/var/log/ingest.index.db`（与现有 `var/log` 同级目录，随 Worker 数据目录迁移）。
- 连接模式：**单写者**（Worker 进程内单连接 + `journal_mode=WAL` + `synchronous=NORMAL`）；读路径仅本进程（无跨进程共享）。

### 2.2 表结构（首版）

```sql
-- 每个日志源一行：身份与配置摘要
CREATE TABLE source (
  key            TEXT PRIMARY KEY,   -- LogSourceID/SourceGeneration
  log_source_id  TEXT NOT NULL,
  source_generation TEXT NOT NULL,
  storage_namespace TEXT NOT NULL,
  updated_at     INTEGER NOT NULL
);
-- 采集游标（可恢复到文件内偏移）
CREATE TABLE position (
  key        TEXT PRIMARY KEY REFERENCES source(key),
  read_pos   INTEGER NOT NULL,
  durable_pos INTEGER NOT NULL,
  reclaim_pos INTEGER NOT NULL,
  acquire_paused INTEGER NOT NULL DEFAULT 0,
  pause_reason   TEXT
);
-- 未解缺口（逐条可查、可人工解算）
-- 实现要点（2026-10-01 加固）：WITHOUT ROWID + 复合主键 (key,id)（id 为源内序号，
-- 保证重启后行身份可稳定推导，支撑「只写变更行」）；不设冗余二级索引——实测
-- rowid 表 + idx_gap_key_resolved 会把 85B 源键存 3 份，db 达 450.9MB（原 JSON 306.5MB）；
-- 改造后 276.7MB。
CREATE TABLE gap (
  id         INTEGER NOT NULL,
  key        TEXT NOT NULL,
  start_pos  INTEGER NOT NULL,
  end_pos    INTEGER NOT NULL,
  reason     TEXT NOT NULL,
  detail     TEXT,
  resolved   INTEGER NOT NULL DEFAULT 0,
  resolution TEXT,
  PRIMARY KEY (key, id)
) WITHOUT ROWID;
-- 投影发布状态（重启增量对账的记账面）
CREATE TABLE projection (
  key        TEXT PRIMARY KEY REFERENCES source(key),
  generation TEXT NOT NULL,
  events_stored_through INTEGER NOT NULL DEFAULT 0,
  pending    INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
-- 实例绑定（stdout/stderr 采集所依赖）
CREATE TABLE instance_binding (
  uuid       TEXT PRIMARY KEY,
  namespace  TEXT NOT NULL,
  generation TEXT NOT NULL,
  mode       TEXT NOT NULL,
  work_dir   TEXT NOT NULL
);
```

### 2.3 读写与崩溃语义

- 每次 `persist` 只写**本批次变更的行**（UPSERT），事务提交即持久；WAL 保证崩溃可恢复。
- 启动时一次性迁移：读取旧 `ingest.state.json` → 事务写入 SQLite → 校验行数/关键字段一致 → 旧文件**改名归档**（保留一个版本，不删除）。
- 迁移失败/校验不一致 → 拒绝启动采集并明确报错（不静默降级），保留旧文件供人工处置。
- 旧文件还含 `source_configs` 与 `instances`（实例绑定，stdout/stderr 采集依赖），**一并迁移**并在校验中逐项比对。
- **回滚步骤（写成脚本 `scripts/rollback-log-index.sh`）**：停止 Worker → 归档现有 `ingest.index.db` → 把归档的旧 JSON 改回 `ingest.state.json` → 启动 Worker；因两格式共用同一语义层（游标/缺口/投影），回滚不需要反向导出。
- 保留 JSON 只读导出（`ingest.index.db` → 导出命令）供排障与回滚。

### 2.4 索引有界化：历史投递批次（`delivery_batch`）裁剪

**问题（FR-498 实测 @60 源）**：索引里唯一随总量线性增长的表是 `delivery_batch`（≈**5 MB/天 @60 源**），
且原先**没有任何裁剪路径**——60 源长跑会把索引无界撑大，与 §1 的目标（写入成本随总量解耦、索引稳态
不随总量增长）相悖。其余表在稳态下是 1–2.5 MiB 量级：`source_wal` 已按条目 UPSERT/删除，`gap`/
`position`/`projection`/`source_aux` 与总量无关。

**消费者与判据（列全，不凭感觉）**：`delivery_batch` 行的完整读链只有一条——

```
rows(delivery_batch) → ingest.indexStateToState → ledger.Entry.DeliveryBatches
  → ledger.contiguousDeliveryEnd(batches, floor = Positions.Reclaim)
```

三个调用点的 `floor` 都是 reclaim：`ledger.RecordDelivery`、`ledger.Restore`、
`ingest.comparisonState`（§2.3 的迁移逐字段校验）。`Positions.Delivery` 是由它**派生**的值
（落库保留原值，`Restore`/比对时按同一口径重算）；`DeliveryState`（最近一批状态）与 `ErrorCount`
另存于 `source_aux.payload`，而「该源最近一次投递是否 UNKNOWN」读的是账本字段 `DeliveryState`，
**不读批次列表**（`pipeline.DeliveryState` / `reconcile` 的 UNKNOWN 判定）。

`contiguousDeliveryEnd` 的语义是「所有字节均有请求结果的连续前缀末端」：`pos` 从 `floor` 起、只增；
跨度 `s` 只有在 `s.end > pos` 时才推进 `pos`。因此 `end <= floor` 的跨度对两个分支都是恒假——它是
**恒等元**：删掉它既不改变本次计算结果，也不改变后续跨度的处理结果（后续只依赖当前 `pos`，而
`pos` 未被它改变）。

⇒ **安全水位 = `Positions.Reclaim`**：`batch.End <= Reclaim` 的条目确定不会再被需要。语义上与 reclaim
的既有含义一致：reclaim 只能经 `logtypes.CanReclaim` 门禁推进（恢复分段到 `WAL_RESPONSIBILITY_TRANSFERRED`
及其后、无 hold），即「这些字节的恢复责任已由受管恢复分段/投影承担」——责任既已转移，批次级请求
结果对这些字节不再承载决定性信息。

判据的单调性（为什么裁过一次之后永远安全）：水位只前进（`TryReclaim` 仅在 `target > Reclaim` 时推进；
`AdvanceRead` 只把读指针夹到水位之上），而 `end <= W` 在更晚的 `W' >= W` 下依然成立。

**实现**（`internal/worker/logs/ledger/delivery_batch_prune.go`）：

- 判据的唯一实现是纯函数 `ledger.PruneDeliveryBatches(batches, watermark, keepRecent)`；
- 调用点只有两个账本写路径：`RecordDelivery`（登记批次后）与 `TryReclaim`（**不论回收是否放行**都按
  最终水位裁一次——被门禁挡住的源同样会被裁，不会因为挡着就长期保留无用历史）；
- **同一水位只裁一次**（`Entry.deliveryPrunedThrough`，进程内状态、不落库）：水位是唯一能让既有条目
  变得可裁的事件（新登记的批次末位在当前读指针附近，只可能在水位之上），故「水位没变就不再扫列表」
  ——稳态（每次投递、每轮采集 250ms/源）都是 O(1)，否则一个「水位被门禁长期挡住」的源会每轮扫描整张
  保留列表（积压场景可达十万级），把空间优化变成 CPU 负担。代价如实登记：整体落在水位之下的**回填**
  批次（如 `ResolveDeliveryThroughRecovery` 补登记的历史区间）会留到下一次水位前进才被清掉——它本就是
  恒等元，留着不影响任何派生值；
- **自证守卫**：裁剪前后按同一水位重算的连续前缀必须逐位相等；不等即说明判据与派生计算已不自洽，
  此时原样返回完整列表与错误，调用方按「不裁」处理（保留更多永远是安全方向）；
- **失败只告警不阻断**：任何异常都收敛为「本轮不裁 + 一条 `slog.Warn`」，绝不冒泡到投递登记/回收推进/
  持久化/启动路径——少裁、不裁永远是安全方向；
- **内存与索引同时有界**：账本自己持批次列表，故裁剪点必须在账本内；若改成「落库前过滤一遍」，库会
  变小而内存仍随总量线性增长（本 FR 就是在索引层发现的问题，不能只在索引层修）；
- 批量删除走既有增量写路径（`ApplyScoped` 按归属比对指纹差异），不新增任何 SQL/DELETE 语句，也不
  绕开镜像（`delivery_batch` 行的 `ordinal` 是列表下标，故**库里的列表必须与账本列表逐条对应**：
  这正是「只在账本内裁、不在落库前过滤」的第二个理由——否则关闭裁剪的逃生开关会说谎）。

**与既有语义不冲突（逐条）**：

| 关联 | 为何不冲突 |
|---|---|
| §2.3 迁移校验 | 比对口径 `comparisonState` 对**两侧**用同一判据归一后再逐字段比对：裁剪只发生在写路径上，归档 JSON 与索引库的裁剪进度可以不同（真机可达形态：迁移已在库中提交、归档尚未完成），不归一会把一次正常启动误判为「索引与旧状态不一致」而**拒绝启动采集**。归一化只吃掉恒等元差异：水位之上一字节的差异照样判不一致（负向对照用例守着），水位本身不同也照样判不一致 |
| FR-497 增量对账 | 对账输入是「已发布 scope 的权威事件集合 + projection 记账面」，与批次列表无关；`ContiguousDeliveryEnd` 逐字未改，读回的批次列表仍给出同一 `delivery_position` |
| 暂停 / 回收语义 | 水位推进门禁（`CanReclaim` / hold / `RecoveryRefs` 选取）一律未改；裁剪只在水位**之后**动作，`TryReclaim` 的返回值与错误语义与改动前逐字一致 |
| 去重 / 审计 | `DeliveryState`（最近状态）、`ErrorCount`、`RecoveryRefs`、`Gaps`、实例绑定均另存且原样保留；裁剪不触碰其他任何表 |

**可配置**：键 `log_index.batch_prune.*`（登记见 §6），默认**开启**；`keep_recent` 是审计尾窗
（默认 0 = 严格按水位；只多留不少留，不弱化有界性）；非法值（负条数）回退默认，配置误写不放宽判据。

## 3. 验收标准

1. **回退**：迁移后重放 13 源历史窗口，游标/缺口/投影与迁移前逐字段一致（脚本比对）。
2. **续传回归**：重启 Worker 后从上次位置续采（沿用既有 tailer 用例 + 新增索引层用例）。
3. **性能（真机）**：60 源规模下**单次持久化 ≤50ms**（现 307MB 时同口径为数百 ms 起、外推至 GB 级为秒级）；采样打印 P50/P95。
4. **崩溃不丢**：随机 `kill -9` Worker，重启后索引无丢失/无回退超过一个批次（与 FR-497 的允许重复口径一致）。
5. **可运维**：`sqlite3` 直接查询缺口/暂停源；提供「导出 JSON」命令。
6. **零行为变更**：现有 ingest/query 全套测试绿；`gofmt`/`vet` 干净。
7. **二次启动 + 脏环境（纪律，本次事故换来）**：验收必须覆盖「迁移 → 首启 → 停 → **二启** → 三启」，且二启/三启跑在**脏环境**（VL 中仍留有上一轮生命周期的同名代次行、残留 WAL、迁移前归档文件）上；部署后按「部署后复验」**立刻再重启一次**验证通过才算完成。只有单轮首启证据不算通过。规则原文：`.claude/rules/testing-and-quality.md`「状态 / 迁移类变更」、`.claude/rules/gate-merge.md`「部署后复验」。
8. **索引有界化（§2.4，FR-498 实测问题）**：`delivery_batch` 行数/体积不随累计投递总量增长，且**不该裁的绝不裁**。判据与实测：
   - 60 源 × 6000 批/源（≈生产 4 天 @1500 批/源/天）夹具：裁剪开启（默认）**0 行**、库 864 KB；同夹具**关闭裁剪**（等价旧行为）**360 000 行**、16.9 MB 行载荷、库 **25.6 MB**（≈6.4 MB/天，与 FR-498 实测的 5 MB/天同量级，说明夹具形态贴合生产）；`keep_recent=128` 时 7 680 行 / 1.37 MB；
   - 水位滞后窗口（回收慢于投递）下保留量只与滞后有关：3500 与 6500 批/源（滞后窗口同为 500 批）**给出同一行数与同一行载荷** ⇒ 与累计总量无关；
   - 边界：`End == reclaim` 的条目被裁（恒等元）、`End == reclaim + 1` 的条目**绝不裁**（它仍把连续前缀从 reclaim 推到 reclaim+1）；
   - 裁剪后水位（四水位逐项）、最近投递状态、恢复引用、实例绑定、落库往返（读回的批次列表仍自证同一 `delivery_position`）与稳态零写入（重新持久化不得因裁剪反复重写）逐项照旧；迁移校验在「库已裁、归档 JSON 完整」的形态下正常通过。
   - 回归：`internal/worker/logs/ledger/delivery_batch_prune_test.go`、`internal/worker/logs/ingest/delivery_batch_prune_test.go`；转红臂见 §5。

## 4. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 新依赖（第三方驱动） | 纯 Go、广泛使用；锁版本；构建/运行无 cgo |
| 迁移一次性错误 | 迁移前归档旧文件；校验失败拒绝启动；提供回滚命令 |
| 单文件损坏 | WAL + 定期 `PRAGMA integrity_check` 巡检（随 Worker 健康巡检 FR-459） |
| **投影代次名被复用（2026-10-01 生产事故，已修）** | 代次名派生自本机状态计数器，状态重生（迁移/回滚/重部署）会令计数器回退，与 VL 中上一轮生命周期的行重名——VL 只追加（「取代」仅改查询侧白名单），而校验只按代次名过滤，`(同源, 同名, 校验窗口)` 命中旧行即判 `unexpected or duplicate`，启动整窗重发时**必然**卡死（首启低号侥幸过关、二启爬到旧整窗号段即死）。修法：选名前探测 VL 是否已占用，占用即进位（`nextFreeProjectionGeneration`）。回归：`generation_reuse_test.go`（改回旧行为即红，错误串与生产同类） |
| **索引被 `delivery_batch` 无界撑大（§2.4，FR-498 实测 5 MB/天 @60 源）** | 按 reclaim 水位裁剪历史批次（判据有证明、有自证守卫、失败只告警）。风险点与处置：①判据被后人改坏（放宽一字节即会裁掉仍被需要的条目）→ 自证守卫拦下并保留完整历史，且用例对判据敏感（`-overlay` 放宽一字节实测转红）；②迁移校验因两侧裁剪进度不同而误判 → 比对口径两侧同判据归一（去掉归一的变异实测转红）；③裁剪后 `delivery_position` 漂移 → 恒等元性质 + 「读回的批次列表必须自证同一前缀」断言；④「裁不掉」变成「采集出错」→ 写路径不返回裁剪错误，异常只 `slog.Warn`（逃生开关 `log_index.batch_prune.enabled=false`） |

## 5. 验证方式

- 单元/回归：ingest、ledger、acquire 包测试 + 新增索引层用例；
- 真机：与 FR-498（规模压测）同批次执行，出索引持久化 P50/P95 数字。
- 索引有界化（§2.4）的转红验证（`-overlay` 变异，脚本 `.tmp/opt-prune/run_red_arms.sh`、日志 `.tmp/opt-prune/out/`，均不入库）：

  | 臂 | 变异 | 实测 |
  |---|---|---|
  | arm0 | 无变异（含注入的守卫用例） | **绿** |
  | arm1 | 判据放宽一字节（`End <= watermark+1`） | **红**：`裁剪改变了连续前缀`（`expected: 1 actual: 2`）＋`TestPruneDeliveryBatchesIsIdempotent` 红；同时打出守卫告警 `历史投递批次裁剪被守卫拦下，本轮保留完整历史（不影响采集正确性）` |
  | arm1b | arm1 + 注入的「守卫用例」单独跑 | **绿**：写路径无错误、仍被需要的条目在、连续前缀不变 ⇒ **「失败只告警不阻断」的可执行证据** |
  | arm2 | arm1 + 摘掉自证守卫 | **红**：`TestPruneDeliveryBatchesOnLedgerWritePaths` 红（`[800,801)` 被误裁） |
  | arm2b | arm2 + 注入的「守卫用例」单独跑 | **红**：`仍被需要的条目绝不能被裁掉` ⇒ 守卫是承重件（不是装饰） |
  | arm1c | arm1 + ingest 边界回归 | **红**：守卫保留完整历史 ⇒ 「库里只剩水位之上那一条」的形态断言不成立（判据被改坏可被端到端抓住） |
  | arm2c | arm2 + ingest 边界回归 | **红**：边界条目被误裁、四水位/往返断言随之失败 |
  | arm3 | 去掉迁移比对口径的裁剪归一 | **红**：`水位之下的差异（恒等元）在比对口径下必须等价` ⇒ 迁移校验确需归一 |
  | arm4 | 删掉两个裁剪调用点（= 改回不裁剪） | **红**：`裁剪开启时行数必须与批量 N 解耦，实测 8000 行` ⇒ 有界性断言对裁剪敏感 |

## 6. 配置键登记（§2.4 索引有界化）

| 键 | 默认 | 语义 | 生效路径 |
|---|---|---|---|
| `log_index.batch_prune.enabled` | `true` | 历史投递批次裁剪开关；`false` = 完全不裁（应急逃生口：怀疑裁剪影响判定时先关它） | `ingest.Options.IndexPrune`（包内生效）；**YAML 接线待做**——需改 `internal/worker/config.go`（`LogIndexConfig` + `SetDefault`）与 `apps/worker/main.go`（构造 `ingest.Options` 时传 `cfg.LogIndex.IndexPruneConfig()`），超出本次改动面（与 FR-497 首次落地时的登记口径一致） |
| `log_index.batch_prune.keep_recent` | `0` | 无条件保留的最近批次条数（审计尾窗）；只多留不少留，不弱化有界性；负数为误写 → 回退默认 0 | 同上 |

> 未接线期间的生效口径：`Options.IndexPrune == nil` 即 `DefaultDeliveryBatchPruneConfig()`（默认开启、
> 严格按水位），因此**默认行为在生产路径上已经生效**，只是暂不能从 `worker.yml` 覆盖。

