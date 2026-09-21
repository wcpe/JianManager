# SLO / 可用性统计与容量趋势预测（FR-463 / FR-464）

> 状态：📋 计划　·　关联 PRD：FR-463、FR-464　·　依赖：FR-060（时序底座，已交付）、FR-011/085（告警事件，已交付）、FR-220（统计页）、FR-221（时序剖析）；FR-463 与 FR-461/462（`docs/specs/cluster-observability/spec.md`）共享「窗口聚合」基础设施　·　关联 ADR：ADR-013（分级降采样）

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
- 可用拍数：复用 `MetricService.latestSum` 的「序列 + 时间窗」模式，改为对 `inst_uptime`（instance scope）逐序列 `COUNT(value IS NOT NULL)`；rollup 档无 NULL 语义，故**可用性统计固定走 raw 档**（48h 内）或对更早窗口用「5m 桶 count>0 即该桶 10 拍可用」近似（在 SLO 文档注明该近似）。
- 故障事件：查 `AlertEvent` 按 `TargetID` + 时间窗 + `resolved` 聚合，函数按 `NodeUUID/InstanceID → 数值 ID` 转换（复用 `MetricService.ResolveInstanceID`）。
- 平台级 = 各实例可用拍数求和 / 总拍数求和（不做简单平均，避免实例数变化扭曲）。

### 2.2 FR-464：容量外推

新增 `internal/controlplane/service/capacity.go`，`CapacityService.Forecast(req)`：

- **输入**：目标序列（`node_disk_used`/`node_mem_used` + 对应 `*_total` 作上限；实例 `inst_heap_used` + `inst_heap_max`），分析窗口（默认 7d，走 5m rollup）。
- **方法（决策：Theil–Sen 中位斜率 + 最小二乘残差）**：对窗口内 `(t, avg)` 点集算 Theil–Sen 稳健斜率 β（对缺测/抖动不敏感，优于普通最小二乘），截距为窗口末值。
  - 耗尽时间 `T_exhaust` = (capacity_limit - v_now) / β，仅在 β > 0 时有意义（β ≤ 0 → 「无增长趋势，不预测」）。
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
- **告警接入**：`Confidence != insufficient` 且 `ExhaustLowDays < 阈值`（默认 7d，规则可配）→ 经 `AlertRule.TriggerType=metric` 同构路径发一条趋势告警（复用 `DedupKey` 去抖，键含 targetId+metricKey）。
- **降噪（决策）**：预测按需计算（查询触发），不做后台常驻扫描；趋势告警仅对「窗口内点 ≥ 100 且 β 显著」的目标评估。

### 2.3 API 与前端

| 端点 | 语义 |
|---|---|
| `GET /metrics/slo?scope=&targetId=&range=&target=` | `SLOResult`（node/instance） |
| `GET /metrics/capacity/forecast?scope=&targetId=&metrics=node_disk_used,node_mem_used&range=7d` | `[]ForecastResult` |

路由挂 `internal/controlplane/router/metric.go` 的 `MetricHandler.RegisterRoutes`（`m.GET("/slo", …)`、`m.GET("/capacity/forecast", …)`）；实例维度权限复用 `authz.CanAccessInstance`，与既有 `Series`/`SeriesBatch` 同口径。前端在统计页（FR-220）新增「可用性」区块、在实例/节点详情新增「容量预测」卡片（复用 `packages/ui` 的 Panel 与既有图表）。

## 3. 任务拆分

- [ ] ① `internal/controlplane/service/slo.go`：`SLOService.Compute`（可用拍聚合 + AlertEvent 故障时长）＋单测（构造样本/事件边界）。
- [ ] ② `internal/controlplane/service/capacity.go`：Theil–Sen + 残差 CI（纯函数可穷举测试）＋单测（含 β≤0、样本不足）。
- [ ] ③ 路由与 DTO：`router/metric.go` 新端点 + 权限收敛 + OpenAPI/`docs/API.md`。
- [ ] ④ 趋势告警接线（依赖 ②；复用 FR-011/085 事件与去抖）。
- [ ] ⑤ 前端：统计页「可用性」区块 + 节点/实例「容量预测」卡片（依赖 ③）。
- [ ] ⑥ 文档同步：本 spec、PRD FR-463/464 状态、ARCHITECTURE/API。

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 指定窗口内可查实例/平台可用率、故障次数、平均恢复时长（MTTR） | 单测 + 真机 |
| 2 | MTBF 与误差预算消耗正确；无故障窗口 MTBF 返回 null 而非 Infinity | 单测 |
| 3 | 人工制造一次实例崩溃-恢复，窗口统计的故障 1 次、MTTR≈恢复耗时 | 真机（注入） |
| 4 | 磁盘/内存按当前增速给出耗尽预测时间与 80% 置信区间 | 单测 + 真机 |
| 5 | 对无增长/样本不足的目标返回「不显著/样本不足」，不伪造预测 | 单测（负例） |
| 6 | 预计 N 天内耗尽时触发趋势告警且去抖生效 | 真机（注入增长数据） |
| 7 | 实例维度查询对无权用户返回 403（沿用 CanAccessInstance） | 单测 |

## 5. 风险 / 待定

- **可用性口径拍板**：`inst_uptime` 依赖探针，未装探针的实例（beacon/binary，且 FR-454 将停止对其抓探针）如何计可用性？候选：退化为「status==running 且节点在线」，或标记 scope 不支持 SLO。**建议首版按后者：无探针实例单列「不适用」**。
- **rollup 档丢失 NULL 语义**：5m/1h 档只存聚合值，无法精确还原「本拍是否可用」；> 48h 窗口的可用率只能按「桶内有样本」近似。需在文档明确该近似误差上限（一个 5m 桶最多 10 拍）。
- **趋势告警噪声**：线性外推对阶跃变化（清理磁盘）会误报，需靠 β 显著性与去抖窗口抑制；必要时引入 FR-462 的动态基线。
