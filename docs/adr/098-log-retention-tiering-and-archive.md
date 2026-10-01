# ADR-098: 日志保留、分层与归档（热 90 天分级 / 冷 730 天统一 / 删前归档）

- **日期**: 2026-10-02
- **状态**: accepted
- **关联**: FR-477（Partition Catalog 与冷热 Lifecycle）· FR-478（Deep Archive 与 Rehydrate）· FR-474（容量门禁）· FR-498（成本与容量基线）· [ADR-094](094-worker-log-local-data-plane.md)（Worker 日志本地数据面）· [ADR-095](095-canonical-event-store-on-disk.md)（canonical 事件体落盘）· `docs/specs/log-cost-governance/spec.md` · `docs/specs/worker-log-lifecycle/spec.md` · `docs/specs/worker-log-platform-contract/spec.md` §7

## 上下文

FR-473～484 建成了日志平台的骨架：Worker 本地采集 + 账本 + WAL，受管 VictoriaLogs（HOT/COLD/Rehydrate 三命名空间），CP 联邦查询。但**生命周期只有能力、没有策略**：

1. **保留期只有 VL 全局单值**：受管 VL 的 `-retentionPeriod` 默认 `30d`，对 HOT/COLD/Rehydrate 同值（`internal/worker/logs/vlsup/cmdline.go`）；契约 §7 要求的 `move_after_age` / 产品 `online_retention` / VL runtime retention **三层分离**此前只写在注释里，无配置键、无执行器。
2. **分层无人驱动**：`catalog`/`lifecycle` 状态机齐备且真机 Runbook B 通过，但唯一入口是人工 API `POST /nodes/:id/log-runtime/migrate`（`internal/controlplane/router/log_runtime.go`）——HOT 分区不迁移，VL 到点**直接删数据**（无归档动作）。2026-09-28 生产事故中日志数据盘一度成为主要风险面。
3. **到期语义危险**：VL 的 `/delete/*` 一旦启用即**不可逆**，而此前没有任何机制保证"先归档、后删除"。
4. **实例本地 `logs/*.log.gz` 无归档去向**：轮转后的压缩归档只被 ArchiveImporter 在"接管轮转尾部"时解析；平台对其没有"吸存"能力，也没有清理责任（既有代码不删任何实例本地 `.gz`）。
5. 用户已明确口径（含今日补充），需要固化为不可随意漂移的架构决策。

## 决策

### 1. 两层保留口径（设置中可配置）

| 层 | 介质 | 保留窗口 | 级别粒度 |
|---|---|---|---|
| **热层** | 受管 VL + SSD | **90 天** | **按级别分级停留**：`debug 3d / info 7d / warn 30d / error 90d`（TRACE 与 DEBUG 同档），**可在设置中配置**（含源级覆盖） |
| **冷层** | 受管 VL + 大容量盘 | **730 天（2 年）** | **所有级别统一**，不再设级别档位 |

- 冷层不按级别分档是刻意选择：热层分级是为了"保障最近的高价值级别 + 尽快腾出 SSD"，冷层的目的只有一个——长期留存；再分一套档位只会让"同一条日志在两层的保留期不一致"成为常态。
- 级别的白名单固定为 `TRACE/DEBUG/INFO/WARN/ERROR`（`SEVERE/FATAL` 在归一化阶段已折成 `ERROR`），配置里的未知级别**启动即拒**。

### 2. 到期动作 = 搬运（archive-before-delete），绝不裸删

到期日分区的**默认动作是把分区搬运到 COLD**，复用既有迁移状态机 `ROUTING_FROZEN → DRAINING → SNAPSHOTTING → STAGING_VERIFY → ATTACHED_STAGING → OWNER_SWITCHED → QUERY_LEASE_DRAINING → DETACHED → CLEANED`：

- 搬运成功**且校验通过**之后才允许 HOT 侧回收；
- 搬运失败 = **保留原物 + 告警**（宁占盘不丢数据），零删除；
- 无搬运路径且未显式放弃时 = `blocked` + 保留原物，而不是删除；
- 次序不可调换：搬运判在删除**之前**（该次序是变异实验 N13 暴露的真实缺陷——初版把 `discard` 判在前面，语义会变成"只要曾显式开过删除，默认动作就永久变成删除"）。

### 3. 删除需要三道独立条件，默认全关

| 闸 | 键 / 位置 | 默认 | 语义 |
|---|---|---|---|
| 意图闸 | `log_retention.discard` | `false` | 显式声明"允许直接删" |
| 执行闸 | `log_retention.sweep.vl_sweep` | `false` | 是否真的调用 VL 删除接口 |
| VL 自身 | 受管进程 `-delete.enable` | 关 | VL 默认拒绝 `/delete/*`（实测未开时返回 400） |

三者同时成立才可达删除。**默认路径永远是搬运**（数据仍在、仍可查），使"所有配置错误的最坏后果"从「数据永久丢失」降级为「盘没省下来」。

删除过滤器**由平台代码生成，绝不从配置读**：级别必须命中白名单，源标识必须通过字符集白名单 `^[A-Za-z0-9_.:-]{1,128}$` 并加引号（源标识含冒号，不加引号会被解析成字段过滤）。

### 4. 触发口径：年龄 + 磁盘水位取先到（D3）

搬运触发 = 分区年龄到 **或** 磁盘水位到，**取先到者**：

- 磁盘触发带 **最小年龄下限（默认 7 天）**：盘再满也不搬"正在写的日分区"；
- 磁盘读数**复用采集侧同一份** `ingest.DiskCapacityProvider`——两处各采一次迟早会给出不同结论，现场只会表现成"有时降级有时不降级"；
- 读数失败**退回纯年龄**，不猜"盘满了"；列举失败如实报错（区分"没有分区"与"读不出来"）。

### 5. 实例本地 `logs/*.log.gz` 归档吸存（冷层）

- 实例 `logs/*.log.gz` 以**文件身份**、**原样（压缩态）**吸存到冷层；
- **只归档文件，不解析、不入事件流**（零事件层重复）：它不是 canonical 事件的第二份副本，而是原始证据；
- **幂等去重由系统负责**（同一文件重复吸存不产生第二份）；去重判据在实现时冻结（文件身份 = 源 + 文件名/大小 + 内容校验的组合，不得依赖"最后一次吸存时间"这类可变量）；
- **本地 gz 一律不动**：不在 Worker 侧删除或搬走实例本地的 `.gz`（用户决定将其留作最后备份，清理责任在实例侧）。

### 6. 对象存储：第一阶段不上，保留第 3 档能力位

- 第一阶段冷层介质为**本地大容量盘**（受管 COLD 实例），不引入对象存储作为必需组件；
- FR-478 已有的 S3 Provider / Rehydrate **能力不删除**，保留为第 3 档（异地 / 超长期 / 合规）的能力位与后续评估位；
- Deep Archive 与 COLD 的关系：COLD 是"可分层的本地长期留存"，DEEP 是"异地/独立副本"；启用 DEEP 的唯一条件由后续评估决定，不以对标为由默认开启。

### 7. 已知边界（如实登记）

- **粒度的张力**：日分区（`catalog.PartitionKey = (namespace, utcDay)`）里混着所有级别，而搬运的最小粒度就是"一天"。因此**按级别的 TTL 无法在热层独立生效**——`HotWindow()` 取显式热层窗口；真正的独立分级需要把级别分流到不同存储命名空间（与采样侧"把低价值等级路由到独立流"是同一类改动），列为后续评估项，不在本 ADR 内承诺。
- **冷层保留的执行责任**在受管 COLD 实例的 `-retentionPeriod`；平台侧 `cold_retention` 承担**声明与校验**（让"热多久 / 冷多久"在同一处可读、可校验），不重复实现一套删除逻辑。
- 本 ADR **不启用任何删除**：三闸默认全关是本决策的一部分，不是"待办"。

## 理由

- **"省空间"的合法诉求不是"让日志消失"**：保留策略配错在现场只表现为"日志不见了"，不会有任何报错。把默认动作定成"搬运"，让配置错误的最坏后果落在"盘没省下来"而不是"数据没了"。
- **两道闸而非一个开关**：删除路径后果不可逆，一处误开与两处同时误开的概率差得很远；再加 VL 自身的 `-delete.enable`，形成三层纵深。
- **冷层统一档位**：与热层分级的目的不同（一个为腾空间、一个为长期留存），混用只会产生无运维价值的不一致。
- **gz 只归档文件不解析**：解析会把它变成"事件的又一份来源"，引入重复对账与代次管理的全部复杂度；而它的价值恰恰是**原始证据**——原样保存才能在最坏情况下作为最后手段。
- **本地 gz 不动**：平台删掉"用户自留的最后备份"是不可接受的风险错配。

## 后果

- **正面**：日志数据盘有了上界（热 90d 上限 + 冷 730d 上限）；所有配置错误的最坏后果从"数据永久丢失"降级为"盘没省下来"；分层有了自动驱动，不再依赖人工 API。
- **实现状态（截至本 ADR 写入时）**：
  - 已落地（提交 `692e714b`、`282c60af`）：`internal/worker/logs/retention`（策略/计划/执行器/VL 客户端/驱动器）、`apps/worker/log_retention.go` 生产适配层、`log_retention.*` 配置面主体、**冷层搬运驱动器**（周期调度 + 首轮立即执行 + 年龄/水位取先到）；变异 32/32 全红。
  - 工作区已接线（写入时未提交，以代码/CHANGELOG 为准）：`log_retention.cold_retention`、`log_retention.trigger.*` 配置面；受管 VL 的**按 namespace retention**（`vlsup.RetentionByNamespace`）。仍待：把热层窗口与受管 HOT 的 `-retentionPeriod` 对齐到 90 天、COLD 侧 `-delete.enable` 的显式默认关闭说明。
  - 待实现：gz 文件吸存（第 5 条）；到期/搬运/删除的指标与日志行对外暴露（"谁、何时、多少、去哪"可审计）。
  - 文档对账：`docs/specs/log-cost-governance/spec.md` 的 D1/D2 口径按本 ADR 更新为热 90d / 冷 730d（该 spec 正由实现侧同步）；PRD 的 FR 编号需对账（`log-cost-governance` 一侧的 FR 编号与 PRD `FR-499`（采集登记锁解耦）冲突，CHANGELOG 亦标 FR-499）。
- **可转红防护**：`log_retention` 的 21+5 条回归与 32 条变异（含"搬运失败留存+零删除""穷举组合下未显式 discard 零裸删""显式 discard 后删除确实可达"的正反对照）钉住本 ADR 的规则；改动规则必须同步这些用例。
- **不影响**：`.claude/rules/architecture-invariants.md` 与 `decision-alignment.md` 的同步不在本次写入范围（由文档纪律流程后续处理）。
