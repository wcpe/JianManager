# SLO / 可用性统计与容量趋势预测（FR-463 / FR-464）

> 状态：🟡 **代码与单测已交付，真机验收待做**（R-1；FR-463/464 端到端已实现，单测/构建覆盖通过；§4 中标「真机（注入）」的验收项尚未在真机验证）　·　关联 PRD：FR-463、FR-464　·　依赖：FR-060（时序底座，已交付）、FR-011/085（告警事件，已交付）、FR-220（统计页）、FR-221（时序剖析）；FR-463 与 FR-461/462（`docs/specs/cluster-observability/spec.md`）共享「窗口聚合」基础设施　·　关联 ADR：ADR-013（分级降采样）

## 1. 背景与目标

### 1.1 现状痛点

- **FR-463**：平台能回答「此刻谁在跑」，不能回答「过去一周可用率多少、坏了几次、平均多久恢复」。全库 grep `uptime 达成率 / MTTR / MTBF / 误差预算` **零命中**。`inst_uptime`（`model.MetricInstUptime`，源自 `serverprobe_uptime_seconds`）只作曲线展示，未做可用性判定；`AlertEvent`（`internal/controlplane/model/alert.go`）带 `FiredAt/ResolvedAt/Resolved`，可导出故障时长，但无聚合视图。
- **FR-464**：运维无法回答「磁盘/内存按当前增速还剩多久耗尽」。全库 grep `预测 / forecast / predict / 外推` **无任何时序外推实现**。`node_disk_used/node_mem_used` 与 `node_disk_total/node_mem_total` 均已入 `metric_series`（`internal/controlplane/model/metric.go`），数据齐备但只画曲线。

### 1.2 目标

- FR-463：按窗口（1d/7d/30d/90d）给出**实例级与平台级**可用率、故障次数、MTTR、MTBF 与误差预算消耗，落统计页展示。
- FR-464：对关键资源（节点磁盘/内存、实例堆内存）给出**线性外推的耗尽预测时间与置信区间**，并在「预计 N 天内耗尽」时经告警体系（FR-011/085）触发趋势告警。

### 1.3 范围外（不做）

- 不引入 Prometheus / 外部 SLO 引擎；复用既有 `metric_rollup_*` 三档降采样（ADR-013）。
- 不做非线性/季节性预测模型（首版只做稳健线性外推 + 残差置信区间）。
- 不改采集侧（Worker 心跳与 ServerProbe 抓取链路不动）。
- 不把 SLO 做成可配置多目标（首版固定「可用 = status 为 running 或探针在线」口径）。

## 2. 设计

### 2.1 FR-463：可用性采样与 SLO 聚合

**可用性口径（决策）**：以「采集到心跳/探针快照」为在线证据，而非只看 `instances.status`（status 会被人工停机混淆）。一条 30s 样本视为该实例「本拍可用」，其判定规则：

| 目标 | 可用证据 |
|---|---|
| 实例 | 该拍存在非 NULL 的 `inst_uptime` 样本（探针在线），或 `instances.status == running` 且节点在线 |
| 节点 | 节点 `status == online` 且 `LastHeartbeat` 在 `overviewNodeFreshWindow`（90s，`internal/controlplane/service/metric.go`）内 |

- **可用率** = 窗口内可用拍数 / 总拍数（分母按窗口时长 / 30s 推算，缺拍计为不可用——「无证据即不可用」是 SLO 的保守默认）。
- **故障次数**：以 `AlertEvent`（triggerType ∈ {instance_crash, node_offline, metric}）的 `FiredAt` 计一次故障；同一 `DedupKey` 在去抖窗口内只计一次。
- **MTTR** = Σ(`ResolvedAt` - `FiredAt`) / 已恢复故障数；未恢复事件不计入均值但单列为「进行中故障数」。
- **MTBF** = 窗口时长 / 故障次数（次数为 0 时返回 null，不返回 Infinity）。
- **误差预算**：由 SLO 目标（默认 99.5%）导出允许不可用时长 = 窗口 × (1 - 目标)；已消耗 = 窗口 × (1 - 实测可用率)。

**实现落点**：新增 `internal/controlplane/service/slo.go`，`SLOService`：
```go
type SLOQuery struct { Scope model.MetricScope; NodeUUID, InstanceID string; From, To time.Time; Target float64 }
type SLOResult struct {
    Availability float64; TotalSamples, UpSamples int
    Incidents, ActiveIncidents int
    MTTRSeconds, MTBFSeconds *float64
    BudgetAllowedSec, BudgetBurnedSec float64
}
func (s *SLOService) Compute(q SLOQuery) (SLOResult, error)
```
- 可用拍数：复用 `MetricService.latestSum` 的「序列 + 时间窗」模式，改为对 `inst_uptime`（instance scope）逐序列 `COUNT(value IS NOT NULL)`；rollup 档无 NULL 语义，故按窗口跨度选档：**≤48h 走 raw（逐样本计数）／≤30d 走 5m 桶／更长走 1h 桶**，rollup 档每桶计入的拍数取 `min(count, 桶秒/30)`（5m→10、1h→120），在 SLO 文档注明该近似。
- 故障事件：查 `AlertEvent` 按 `TargetID` + 时间窗 + `resolved` 聚合，函数按 `NodeUUID/InstanceID → 数值 ID` 转换（复用 `MetricService.ResolveInstanceID`）。
- 平台级 = 各实例可用拍数求和 / 总拍数求和（不做简单平均，避免实例数变化扭曲）。

### 2.2 FR-464：容量外推

新增 `internal/controlplane/service/capacity.go`，`CapacityService.Forecast(req)`：

- **输入**：目标序列（`node_disk_used`/`node_mem_used` + 对应 `*_total` 作上限；实例 `inst_heap_used` + `inst_heap_max`），分析窗口（`range` 缺省 **24h**，按跨度自动选档——≤6h raw、≤30d 5m、更长 1h；推荐 7d 走 5m 档以获得更稳的斜率）。
- **方法（决策：Theil–Sen 中位斜率 + 最小二乘残差）**：对窗口内 `(t, avg)` 点集算 Theil–Sen 稳健斜率 β（对缺测/抖动不敏感，优于普通最小二乘），截距取 `median(v − β·t)`（对离群更稳健）。
  - 耗尽时间 `T_exhaust` = (capacity_limit - v_now) / β，仅在 β > 0 时有意义（β ≤ 0 → 「无增长趋势，不预测」）。
  - **可预测上界**：`T_exhaust` 超过 **100 年**（或置信上界超过）时判为「实际不耗尽」——`ExhaustAt`/`ExhaustLowDays`/`ExhaustHighDays` 全为 `null` 并在 `note` 说明。原因是 `time.Duration` 以纳秒为 int64（约 292 年即溢出），极缓增长（如 β≈1e-3 B/s）会把耗尽时间算成过去时间、让前端把「几乎不增长」误读为「马上耗尽」。
  - **置信区间**：用残差标准差 σ_res 与斜率标准误 σ_β，给出 `T` 的 80% 区间（正态近似）；β 的区间跨 0 时返回「趋势不显著」。
- **输出**：
```go
type ForecastResult struct {
    TargetID, MetricKey string
    NowValue, LimitValue float64
    SlopePerSec float64
    ExhaustAt *time.Time
    ExhaustLowDays, ExhaustHighDays *float64 // 80% CI
    Confidence string // high|low|insufficient（按样本数与残差定级）
}
```
- **多序列口径（B-2，与归因卡同规则）**：同一目标同一「已用」指标可能存在**多条实例级序列**——实例换节点后 `node_uuid` 变化即产生新序列（序列身份含 `node_uuid`）。`NowValue`/`samples`/斜率必须**与序列枚举顺序无关**：
  - **按序列身份择一 + 逐时点去重**：每条序列贡献其独有缺口的时点，同一时点只保留一条序列的值（优先「窗口内最新时点所属的活跃序列」，其余时点由更旧的序列补全）。这样实例迁移当口既不重复累计、也不因换序列丢历史而跌回「样本不足」。定序键为「末值时刻（新→旧）→ 点数（多→少）→ 逐点字典序」，全程确定，不依赖 SQL 行序。
  - **`NowValue` = 窗口内最新非空时点的值**（不是「枚举顺序末条序列的末点」）。`samples` = 择一后趋势线的点数（不是各序列点数之和）。
  - 与 `attribution.go` 的 m2 修复（按 series 身份择一 + 跨 world 求和）**同口径**：都要消除「哪条序列存活取决于查询返回顺序」的不确定性。
  - 实例级序列收敛为一条后，多序列合计**不**相加（这些指标是同一台实例的同一测量，换节点不会让堆内存翻倍）；world 级分区指标不走本路径（`world != ""` 跳过，保持既有过滤）。
- **告警接入**：`Confidence != insufficient` 且 `ExhaustLowDays < 阈值`（默认 7d，规则可配）→ 经 `AlertRule.TriggerType=metric` 同构路径发一条趋势告警（`DedupKey` 键含 targetId+metricKey）。趋势告警是**瞬时型**（`Resolvable=false`：每次查询独立重算，没有「条件恢复」这一事件可观测），落库即视为已解决；重复抑制由重发间隔保证（默认 6h，规则配了 `DedupWindowSec` 时以它为准）——故「首次触发 → 条件恢复 → 再次恶化」时第二次仍能告警，而 60s 轮询不会每天堆出上千条事件。
- **降噪（决策）**：预测按需计算（查询触发），不做后台常驻扫描；趋势告警仅对「窗口内点 ≥ 100 且 β 显著」的目标评估。

### 2.3 API 与前端

| 端点 | 语义 |
|---|---|
| `GET /metrics/slo?scope=&targetId=&range=&target=` | `SLOResult`（node/instance） |
| `GET /metrics/capacity/forecast?scope=&targetId=&metrics=node_disk_used,node_mem_used&range=7d` | `[]ForecastResult`（`range` 缺省 24h；7d 为推荐值而非默认） |

路由挂 `internal/controlplane/router/metric.go` 的 `MetricHandler.RegisterRoutes`（`m.GET("/slo", …)`、`m.GET("/capacity/forecast", …)`）；实例维度权限复用 `authz.CanAccessInstance`，与既有 `Series`/`SeriesBatch` 同口径。前端在统计页（FR-220）新增「可用性」区块、在实例/节点详情新增「容量预测」卡片（复用 `packages/ui` 的 Panel 与既有图表）。

## 3. 任务拆分

- [x] ① `internal/controlplane/service/slo.go`：`SLOService.Compute`（可用拍聚合 + AlertEvent 故障时长）＋单测（构造样本/事件边界）。
- [x] ② `internal/controlplane/service/capacity.go`：Theil–Sen + 残差 CI（纯函数可穷举测试）＋单测（含 β≤0、样本不足）。
- [x] ③ 路由与 DTO：`router/metric.go` 新端点 + 权限收敛 + OpenAPI/`docs/API.md`。
- [x] ④ 趋势告警接线（依赖 ②；复用 FR-011/085 事件与节流——瞬时型触发 + 重发间隔抑制）。
- [x] ⑤ 前端：统计页「可用性」区块 + 节点/实例「容量预测」卡片（依赖 ③；含加载/错误/不适用态）。
- [x] ⑥ 文档同步：本 spec、PRD FR-463/464 状态、ARCHITECTURE/API。
- [ ] ⑦ **待真机验收**：崩溃-恢复注入（验收 3）、增长数据注入触发趋势告警（验收 6）。

## 4. 验收标准

> **交付状态**：下表「方式」列的**单测**部分已由本次实现覆盖并通过（`go test ./internal/controlplane/service/... ./internal/controlplane/router/...`）；
> 标 **真机（注入）/真机** 的条目为**待真机验收**，本表勾选状态尚未获得真机证据，故不标 ✅。

| # | 验收项 | 方式 | 状态 |
|---|---|---|---|
| 1 | 指定窗口内可查实例/平台可用率、故障次数、平均恢复时长（MTTR）。**平台可用率口径**：有可用证据（存在 `inst_uptime` 序列且实例仍存在）时给出**拍加权**可用率（各实例可用拍求和 / 总拍求和，非实例简单平均）；窗口内**无任何可用证据**（分母为 0，如尚无装探针的实例）时返回 `applicable:false`、`totalSamples:0`，前端渲染「不适用」而非「0%」/「预算 100% 已消耗」 | 单测 + 真机 | [x] 单测 / [ ] 真机 |
| 2 | MTBF 与误差预算消耗正确；无故障窗口 MTBF 返回 null 而非 Infinity | 单测 | [x] |
| 3 | 人工制造一次实例崩溃-恢复，窗口统计的故障 1 次、MTTR≈恢复耗时 | 真机（注入） | [ ] 待真机验收 |
| 4 | 磁盘/内存按当前增速给出耗尽预测时间与 80% 置信区间 | 单测 + 真机 | [x] 单测 / [ ] 真机 |
| 5 | 对无增长/样本不足的目标返回「不显著/样本不足」，不伪造预测 | 单测（负例） | [x] |
| 6 | 预计 N 天内耗尽时触发趋势告警且去抖生效 | 真机（注入增长数据） | [ ] 待真机验收（单测已覆盖触发/节流/恢复后再告警） |
| 7 | 实例维度查询对无权用户返回 403（沿用 CanAccessInstance） | 单测 | [x] |

> **真机验收前置（验收 1）**：平台可用率需要目标环境**存在 `inst_uptime` 序列**（即至少一个实例装过 ServerProbe 并上报 `serverprobe_uptime_seconds`）才会给数。若在**未装探针**的环境验收 `GET /metrics/slo?scope=platform&range=24h`，实返 `applicable:false` / `totalSamples:0` 属**预期口径**（无证据不伪造），**不判为不达标**——须先接入探针再验平台可用率数值，或改验实例维度。
> 沙箱实测（W9 验收环境，`/tmp/e2e3-w9/cp/jm.db`）：`metric_series` 中 `inst_uptime` 序列数 = 0（现存实例 13 个），故 `scope=platform` 返回 `applicable:false`、`totalSamples:0`；前端据此渲染「不适用」。

## 5. 风险 / 待定

- **可用性口径**：`inst_uptime` 依赖探针。**实现取「有可用证据的拍」为在线**：实例用非 NULL 的 `inst_uptime` 拍、节点用非 NULL 的 `node_cpu_pct` 拍（无节点状态历史表，故不能按 `status`/`LastHeartbeat` 逐拍回溯）。平台维度可用率只统计「有 `inst_uptime` 序列**且实例仍存在**」的实例，分母 = 参与实例数 × 单实例期望拍数——未装探针的实例既不进分子也不进分母，等效于「不适用」（`applicable=false`，前端显示「不适用」而非「预算 100% 已消耗」）。
  - **前提（M6）**：平台分母的「实例仍存在」判定依赖 `instances` 行；`metric_series` 从不删除，故实例删除后残留的孤儿序列**不再计入分母**。推论：装过探针后被删除的实例即时退出统计；**长期离线但实例行仍在**的实例仍计入分母（其序列在窗口内无新样本 → 计为不可用），这是「缺拍计不可用」保守口径的预期行为，不是缺陷。若某实例被删除又重建（UUID 变化），新序列是全新身份，旧序列因实例行消失而失效。
  - **前提（N-1）**：上述「实例仍存在」必须是「**未软删**」。实例删除（`InstanceService.Delete`）走 `tx.Delete(&model.Instance{}, id)` = GORM 软删，行仍留在 `instances` 表内，故平台分母的 `EXISTS` 子查询**必须显式带 `instances.deleted_at IS NULL`**——`EXISTS (...)` 是手写 SQL 片段，GORM 不会为它注入软删谓词（软删谓词只对 `Model(&X{})` 由 `callbacks.BuildQuerySQL` 注入）。同理，跨实例排行（`ranking.go`）JOIN `instances` 也必须带 `i.deleted_at IS NULL`，与 `instance_group.go` / `beacon_sync.go` 的既有范式一致。硬删（`NodeService.Delete` 的 `Unscoped().Delete` 级联）因行不存在而天然被排除。
  - **退化窗口（B-3）**：平台分母 = `perInstance × 实例数`，其中 `perInstance = span / unitSec` 取整。当 **`span < unitSec`**（raw 档 `unitSec=30`，如 `from=T&to=T+5s`；`parseMetricRange` 只校验 `to > from`，故可直达）时 `perInstance = 0` → 原先 `float64(up)/0 = NaN`，而 `encoding/json` 编不出 NaN → gin 的 `c.JSON` 写出 **200 + 空 body**，调用方既拿不到数据也拿不到错误（比 500 更难排查）。
    - **口径：与「无可用证据」同构判为 `applicable=false`**（而非像实例维那样把分母兜底成 1）。理由：窗口不足一个采样间隔时「单实例期望拍数」根本不可定义，按 0 拍算会把「窗口太短」误报成「全时不可用」（可用率 0）并凭空空耗误差预算；显式「不适用」比伪造数值诚实，且与前端据 `applicable=false` 显示「不适用」的既有约定一致。此时 `totalSamples=0`、`availability=0`、`budgetAllowedSec=0`、`budgetBurnedSec=0`。实例维/节点维保留各自的 `total <= 0 → 1` 兜底（单目标无「参与实例数」歧义）。
    - **同类加固（同批完成）**：①**所有** SLO 出口字段（`availability`/`budgetAllowedSec`/`budgetBurnedSec`/`mttrSeconds`/`mtbfSeconds`/`target`）都不得漏出 NaN/Inf；②`target` 入口归一化——`strconv.ParseFloat("NaN", 64)` 会**成功**返回 NaN，而 `NaN <= 0` 恒为 false，故裸 `if target <= 0` 拦不住 NaN，`1-NaN` 会污染全部预算字段；`sloNormalizeTarget` 对 NaN/±Inf/≤0 一律回落默认 99.5%（入口层 `v <= 0 || v > 1` 之外再做出口侧兜底，任何调用方都带不进 NaN）。回归测试 `slo_platform_nan_test.go` 逐字段覆盖三档 scope × 多种退化窗口。
- **告警规则删除后的历史事件（N-7，决策）**：SLO 的故障/MTTR/MTBF 来自 `AlertEvent` 快照（每条事件冗余了当时的 `Level`/`TriggerType`），而规则本身可被删除（`AlertService.DeleteRule` = `db.Delete` = **软删**）。**决策：规则被删后其历史事件仍计入 SLO**——已发生过的故障属于既成事实，不因事后删规则而消失；否则 `Incidents`/`MTBF` 会在删规则后凭空下降、`MTTR` 也会漂移，破坏 SLO 的可审计性。
  - **实现**：`slo.go` 的 `sloEventQuery` 用 `LEFT JOIN alert_rules`（**刻意不用 INNER JOIN**，尽管 GORM 对 `Joins("...")` 的裸 SQL 串不注入软删谓词、当前两者行为恰好相同——语义不得依赖该实现细节）。规则行不可见（被软删）时，维度判定走与「旧规则无 `target_type`」**同一回退分支**：按触发类型家族归维（`node_offline` → 节点、`instance_crash` → 实例）；`metric` 触发因维度歧义仍不计入（宁可少计也不跨维度错计）。
  - **边界**：与实例/节点侧口径一致——删实例/删节点**不会**回滚其历史故障计数；本决策把「规则」纳入同一口径。若某规则需真正销账其历史事件，应显式清理 `alert_events`，而非依赖 SLO 侧的 JOIN 行为。
- **rollup 档丢失 NULL 语义**：5m/1h 档只存聚合值，无法精确还原「本拍是否可用」；> 48h 窗口的可用率只能按「桶内有样本」近似。需在文档明确该近似误差上限（一个 5m 桶最多 10 拍）。
  - **近似上限由常量推导并断言（m1）**：`ticksPerBucket` 由 `metricBucket5m`/`metricBucket1h` ÷ `sloSampleIntervalSec`（30s，与 `model.MetricSampleRaw` 的「30s 粒度」同口径）**计算**得出（5m→10、1h→120），不再写死字面量；`slo_query_count_test.go` 断言数值并额外断言「桶宽是采样间隔的整数倍」——整除性是该近似成立的前提（桶宽非整数倍时 cap 会向下取整，可用拍被低估），而它此前没有任何断言，采样间隔一旦改动只会静默失真。
- **`applicable` 在三档 scope 上同义（M-5，决策）**：判据是**序列是否存在**（该目标历史上是否上报过可用性指标），不是可用拍数是否为 0。
  - **无序列**（实例从未装探针 / 节点从未上报 `node_cpu_pct`）→ `applicable=false`、`totalSamples=0`、预算字段全 0，前端渲染「不适用」。
  - **有序列但窗口内零可用拍** → `applicable=true` + 可用率 0 + 预算全部消耗——这是**真实的全时不可用结论**，不是「不适用」；把判据误写成「可用拍为 0 即不适用」会恰好藏起最需要告警的情况。
  - **为什么必须统一**（本轮代码审查 M-5 判为缺陷、已修）：m1 修复原先只落在平台维，实例/节点维只要 `span > 0` 就无条件 `applicable=true`，而 `total` 被 `total <= 0 → 1` 兜底与「窗口/采样间隔」推算撑起（**分母不是 0**）→ 无证据被当成「全程宕机」。三处证据表明这与设计冲突：①前端 `SLOSection.tsx` 只按 `!data.applicable` 渲染 `slo.notApplicable`，文案「不适用（窗口内无可用证据）」与 scope 无关，无法区分两档；②`MonitoringPage.tsx` 对 node/instance 渲染**同一张卡片**，故实例页也会命中该分支；③本节明言「未装探针的实例」在生产是常见形态（沙箱 13 个实例 0 条 `inst_uptime` 序列），若实例维按「全时宕机」报，最需要看 SLO 的那批实例给出的恰是误导数字。
  - **不适用只否定可用率与误差预算，不否定故障事实**：`incidents`/`activeIncidents`/`mttrSeconds`/`mtbfSeconds` 来自 `AlertEvent` 快照，与「有没有可用性序列」无关，无序列时仍如实给出（某实例探针从未可用但崩溃事件照常落库时，实例页仍应显示崩溃次数）。
- **趋势告警投递移出请求线程（M-8，决策）**：`GET /metrics/capacity/forecast` 命中阈值时**只同步**完成去抖判定与事件落库，**外发投递**（webhook/IM/邮件的 HTTP/SMTP，`ChannelNotifier` 超时 10s）改由 `AlertDispatcher` 的后台队列执行（`AlertTrigger.DeliverAsync`）。
  - **为什么**：`Notify` 由处理器同步调用，原实现会让该 GET 的最坏耗时被外部 webhook 拖住 10s 并全程阻塞 gin handler；前端 60s 轮询下持续占用连接。这是「用户请求驱动的写 + 外发」，与既有 `AlertEvaluator` 的后台轮询形态不同。
  - **为什么不重复发**（异步化的硬约束）：去抖依赖 `dueForNotify` 读上一事件 + `Fire` 写新事件这一对操作，**必须串行**——若把整个 `Fire` 丢进 goroutine，两次并发 GET 会都通过检查（都还没写库）→ 落两条事件 + 外发两次。故队列只承载「已判定要发」的投递动作（与去抖判定 1:1），并以 `CapacityTrendAlerter.mu` 串行化判定与落库（实测无锁时 8 个并发调用产生 3 次重复触发）。
  - **投递语义是「至少一次」**：队列满（要求 ≥42 分钟投递积压）或分发器已 `Stop` 时退化为内联投递（保通知、留 Warn 日志），不静默丢弃；`Stop()` 会排空在途通知后再返回。
- **`alert_events` 无 TTL（M-2，已知缺口，记录不实现）**：`alert_events` 只增不减，没有清理/TTL 逻辑；SLO 的窗口聚合因此是该表的主要常驻读取方。本轮只补 `FiredAt` 索引（`idx_alert_events_fired_at`，`EXPLAIN QUERY PLAN` 实测 `SCAN e` → `SEARCH e USING INDEX idx_alert_events_fired_at`）以消除全表扫，**不擅自加清理逻辑**——事件是告警体系的可审计事实（N-7 决策「规则被删后历史事件仍计入 SLO」正是同一价值取向），设定保留期属于产品决策（保留多久、是否需要归档/导出、是否受审计合规约束），应由独立 FR 明确后再实施。在那之前，索引已把单次 SLO 请求的成本从「全表扫 + 排序」降为「按时间窗定位」，代价随窗口内事件数而非总事件数增长。
- **趋势告警噪声**：线性外推对阶跃变化（清理磁盘）会误报，需靠 β 显著性与去抖窗口抑制；必要时引入 FR-462 的动态基线。
  **实现取舍**：趋势告警复用既有 `TriggerType=metric` 规则作为投递规则与去抖窗口（`CapacityTrendAlerter`，`DedupKey = metric:<ruleID>:<targetID>:capacity:<metricKey>`），不新增触发类型；无匹配 metric 规则时不触发（尊重用户配置，不凭空造告警）。预测本身是查询触发的，不做后台常驻扫描。
- **平台 SLO 为常数条 SQL（M-1，性能）**：`sloUpTicksForSeries` 原对**每条序列**各发一条 `COUNT`（raw 档）/ `Pluck("count")`（rollup 档），而平台维序列数 ≈ 实例数，于是 SELECT 条数随实例数线性增长（审查实测：5 实例=7 条、50 实例=52 条）；`/metrics/slo?scope=platform` 是前端 60s 轮询，该 N+1 会直接放大成持续往返成本。现改为**常数条 SQL**：raw 档单条条件聚合 `COUNT(*) WHERE series_id IN ? AND value IS NOT NULL AND ts BETWEEN ?`（逐序列计数再求和 ≡ 全体非空样本计数，同一行只属于一条序列），rollup 档单条 `SUM(MIN(count, ticksPerBucket))`（与原先 Go 侧逐桶 `min` 后累加是同一运算）。数值语义逐字节不变，回归测试锁定三档「4 实例与 40 实例的 SQL 条数相等」。
- **Theil–Sen 的 O(n²) 成本**：两点斜率中位数在 30d/5m 窗口（~8640 点）约 3700 万对。实现按 `capacityMaxSlopePoints=800` 等距抽稀（保留首尾）后再算斜率，点集仍用于残差/标准误，故稳健性与精度不受实质影响。
- **spec↔现状偏差（已按现状实现）**：`slo.go`/`capacity.go` 落在 `MetricService` 上（`ComputeSLO` / `ForecastCapacity`），不新立独立 Service 类型——与既有 `AnalyzeAttribution`/`InstanceRanking` 同构，避免为纯查询再穿一层 `Services` 装配；无实例/节点状态历史表（`InstanceEvent` 是内存 pub/sub 不落库），故可用性只能走 `inst_uptime` 拍 + `AlertEvent` 口径。
