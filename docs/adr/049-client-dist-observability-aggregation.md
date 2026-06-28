# ADR-049: 客户端分发观测聚合时序

- **日期**: 2026-06-28
- **状态**: accepted
- **上下文**:
  客户端分发（FR-086~097）已落地全链路遥测来源——拉取/下载明细 `client_dist_event`（FR-093，短保留 14d + 写时增量 `client_dist_daily` 长保留）、更新结果遥测 `client_telemetry`（FR-094，短保留 + 写时 `client_telemetry_daily` 按 result 聚合）、机器码登记 `client_machine`（FR-092）。
  FR-095 的 `ClientDistStats.Overview` 只对单频道做近 N 天 ad-hoc GROUP BY，**按「日」粒度且只服务一个频道工作台看板**，无法支撑 FR-218「观测·客户端分发监控页」要的**跨频道 + 平台总览 + 任意时间范围 + 小时级时序曲线**，也未沉淀「活跃客户端去重 / 版本滞后 / 平台分布 / CAS 命中」等观测维度为可回看的时序。
  既有 `client_dist_daily` / `client_telemetry_daily` 是**写时增量**（玩家拉取热路径上 upsert），维度受限（只 channel×version×kind / channel×result），且与明细的 machineId/平台/CAS 等富维度脱节——直接扩这两表会污染热路径写入、且按日粒度太粗。
  架构不变量：Worker 不直连 DB，遥测明细本就由 CP 持有，聚合必须落 CP；单二进制自包含（ADR-001/005），不可引入外部 TSDB。
  ADR-013（节点/实例时序）已确立「CP 端后台定时把高频源卷积为分级时序 + TTL 清理，复用 scheduler」的范式，本 ADR 复用其**思路**而非其**表**。

- **决策**:
  1. **新增独立快照表 `client_dist_snapshot`，离线后台聚合，不碰玩家热路径**：后台任务周期性把保留窗内的 `client_dist_event` + `client_telemetry` 卷积为**按 频道 × 小时桶**的观测快照，与写时聚合（`*_daily`）解耦——热路径只管 best-effort 写明细，富维度观测交给离线卷积，互不阻塞。
  2. **单档小时桶（精简 ADR-013 的三档为一档），理由**：分发遥测数据量远小于节点/实例的 30s 高频样本（事件是「每次拉取/上报」级，且明细本就只留 14d）；观测页诉求是「周/月范围看小时级趋势」，无需 30s 近端高清，也无需 ≥1 年的二级下卷——故单一小时档即可覆盖，避免无谓的多表/多级卷积复杂度。保留 `client_dist_snapshot` 自身 ≥180d（小时桶 × 半年 ≈ 4320 行/频道，量级可控），明细到期照旧由 FR-093/094 各自滚动清理。
  3. **快照维度（每 频道×小时桶 一行）**：
     - 拉取侧（源 `client_dist_event`）：`manifest_pulls` / `artifact_pulls`（按 kind）、`download_bytes`（总响应字节）、`cas_hit`/`cas_miss`（artifact 命中 = 304；未命中 = 200/206）、`active_machines`（**桶内** machineId 去重计数）、平台/版本分布（JSON：见决策 5）。
     - 更新侧（源 `client_telemetry`）：`update_total` 及 `update_success`/`update_fail_static`/`update_rolled_back`/`update_error`（按 result 分桶计数）、`version_lag` 分布（`toVersion` vs 频道 `current_version` 的滞后分布，JSON）。
  4. **machineId 去重口径（写清，避免误读）**：
     - **桶内（`active_machines` 列）= 该小时内 `client_dist_event` 的 machineId 精确去重计数**（`COUNT(DISTINCT machine_id)`，排除空串）。machineId 客户端可伪造、不可信（ADR-023），仅作统计近似，不作授权依据。
     - **跨桶/区间「活跃客户端」≠ 各桶 `active_machines` 简单求和**（同一客户端跨小时会重复计数 → 求和是「人次」上界而非「独立客户端数」）。故查询端点对**仍在明细保留窗（14d）内**的区间，「活跃客户端独立数」由**实时回查 `client_dist_event` 做区间级 `COUNT(DISTINCT machine_id)`** 得精确值；超出明细保留窗的历史区间，只能返回各桶 `active_machines` 的**求和（标注为「活跃人次」近似上界）**，不谎报精确独立数。响应用 `activeMachinesExact`（bool）标明该区间是否为精确去重，前端据此措辞。
  5. **分布维度存 JSON map**：版本分布 `version_dist`、平台分布 `platform_dist`（OS）、版本滞后 `lag_dist` 以 `map[string]int64` 序列化进 TEXT 列。理由同 ADR-013 决策 2 的窄表精神——维度基数动态（版本号、OS 种类、滞后档会增），用 JSON 免改表结构；查询端点跨桶合并 map 求和。单频道单小时的分布基数小，JSON 体积可忽略。
  6. **聚合幂等 + 只卷已完结的桶**：仿 ADR-013 `RollupAndPurge`——每个 tick 只聚合 `< 当前小时桶起点` 的完结桶，按 `(channel, bucket)` 唯一键 upsert（重算覆盖，幂等可重跑）；`channel_id` 取明细的频道（制品端点跨频道共享、明细 channel 可空 → 归入空频道桶，查询「总」时含之）。
  7. **聚合落 CP、复用 scheduler 式后台 goroutine**：`ClientDistObservabilityService.Start()` 起独立 ticker（默认每 10min 卷一次完结小时桶 + 按 TTL 清快照），与 `MetricService`/`ClientDistTrackingService` 同构。Worker 不参与。
  8. **查询端点 `GET /client-dist/observability`（平台管理员 + 审计）**：支持「总」（不传 channelId，跨频道合并）与「单频道」（传 channelId）、任意 `from/to` 或 `range` 枚举；返回**时序数组**（各小时桶的拉取/更新/字节/活跃等）+ **区间分布聚合**（版本/平台/结果/滞后跨桶合并）+ 区间汇总标量（总拉取、成功率、活跃独立数及其精确性标志）。

- **理由**:
  - 离线卷积独立表，把「富维度观测」与「玩家热路径写入」彻底解耦：热路径零新增负担，观测维度可随需扩展而不动既有 `*_daily` 与明细。
  - 单档小时桶贴合分发观测的真实数据量与回看诉求，避免照搬三档 RRD 的过度工程（YAGNI）。
  - machineId 去重的「桶内精确 / 跨区间按保留窗精确或人次近似」口径，诚实表达不可信机器码的统计边界，不在 UI 谎报精确独立数。
  - 复用 scheduler 后台范式与 ADR-013 的幂等卷积写法，不引入新常驻组件，契合单二进制。

- **后果**:
  - 新增表 `client_dist_snapshot`（接入 AutoMigrate）、新增 `ClientDistObservabilityService`（聚合 + 查询）、新增端点 `GET /client-dist/observability`。
  - 同步 `docs/API.md`（新端点）、`docs/ARCHITECTURE.md`（数据模型章节 + 数据表 + 后台任务）、`docs/PRD.md` FR-217 状态。
  - 与 FR-095（`ClientDistStats`）并存、不替代：FR-095 仍服务单频道工作台「统计」Tab 的按日看板；本 ADR 服务观测页的跨频道/平台时序。FR-218/219 消费本端点。
  - 与 FR-093/094 协作：本服务为其明细的「观测时序留存层」；明细到期清理不影响已卷积的快照。

- **替代方案**:
  - **扩 `client_dist_daily`/`client_telemetry_daily` 加维度** — 污染玩家热路径写入、按日粒度太粗、维度耦合，否决。
  - **照搬 ADR-013 三档降采样** — 分发数据量撑不起三档的收益，徒增表与卷积复杂度，否决（单档已够；未来量级暴增可再分档）。
  - **查询时实时 GROUP BY 明细（不建快照表）** — 明细仅留 14d，无法回看更久；大区间全表扫描慢；观测页需周/月趋势，否决（FR-095 的 ad-hoc 仅服务单频道近 30d，量级可接受，本观测页范围更大）。
  - **存 machineId 集合做跨区间精确去重** — 集合膨胀、存储/计算重，否决；改用「保留窗内回查明细精确 + 窗外人次近似」的诚实折中。
  - **引入外部 TSDB（Prometheus/ClickHouse）** — 与单二进制自包含相悖，否决。
