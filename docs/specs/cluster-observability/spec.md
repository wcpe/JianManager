# 集群健康总览墙与异常自动检测（FR-461 / FR-462）

> 状态：📋 计划　·　关联 PRD：FR-461、FR-462（增强 FR-402 / FR-011 / FR-085）　·　依赖：FR-402（平台全景观测总览）、FR-011 / FR-085（告警体系）、FR-400 / FR-401（受管资源与 Bot Worker 观测）　·　关联 ADR：ADR-013（时序指标）

## 1. 背景与目标

**FR-461 痛点**：`PlatformObservabilityService.Overview()`（`service/platform_observability_overview.go`）只产出**计数**（`PlatformObservabilityHealth`）与 **TopN≤5** 的 `Exceptions`（常量 `platformObservabilityLimit = 5`，见 281 行截断），端点 `GET /api/v1/observability/overview`（`router/observability.go`）。60+ 台节点时，运维**无法一屏看清「谁异常」**，只能逐个点。

**FR-462 痛点**：`AlertEvaluator.evaluate()`（`service/alert_evaluator.go`）每 `evalInterval = 60s` 只评估两类规则——`AlertTriggerMetric`（且**仅** `rule.TargetType == "node"`，见 111 行）与 `AlertTriggerNodeOffline`；`getNodeMetric`（186 行）只支持 `cpu`/`memory`/`disk`，且取自 `model.Node` 的**当前快照**而非时序。**实例级 metric 规则不评估**；**无动态基线**，缓慢劣化只能靠人工设死阈值。

**目标**

- FR-461：新增**逐台健康矩阵/热力墙**（每节点一格，着色表征健康度）+ 排序 + 一键定位下钻。
- FR-462：新增**动态基线**（EWMA / 同环比）、**突降突升**、**饱和度**检测，并把 metric 规则覆盖到**实例级**。
- 二者均沿用 FR-402 的**硬约束**：只读 CP 已持久化快照，**绝不触发 Worker RPC**（矩阵一次查询给全，不做逐台 N+1）。

**不做**

- 不改 `MonitoringPage.tsx` 的既有单节点监控视图（矩阵只是新增入口，点击复用既有下钻路由）。
- 不引入新的指标采集；FR-462 复用 ADR-013 的时序库与既有 metric_key。
- 不做告警规则 UI 大改；仅在既有 `AlertRule` 上扩展字段。

## 2. 设计

### 2.1 FR-461 逐台健康矩阵（health-wall）

**新增读模型** `HealthWall`，端点 `GET /api/v1/observability/health-wall`（挂 `ObservabilityHandler.RegisterRoutes`，`router/observability.go:23`）。逐节点一条：

```go
type HealthWallNode struct {
    NodeID     uint    `json:"nodeId"`
    Name       string  `json:"name"`
    Zone       string  `json:"zone,omitempty"`   // 分组/大区（若节点带）
    Freshness  string  `json:"freshness"`        // fresh|stale|offline（复用 resourceFreshnessAt）
    CPUPct     *float64 `json:"cpuPct"`
    MemPct     *float64 `json:"memPct"`
    DiskPct    *float64 `json:"diskPct"`
    Running, Crashed, Stopped int `json:"running,crashed,stopped"` // 实例计数
    ActiveAlerts int   `json:"activeAlerts"`
    BotActive, BotConnecting *int32 `json:"botActive,botConnecting"`
    Level      string  `json:"level"`            // offline|stale|degraded|healthy
    Href       string  `json:"href"`             // 一键定位：/monitoring?node=<uuid>
}
```

- **健康度判定** `Level`：`offline` > `stale` > `degraded`（cpu/mem/disk ≥ 90% 或 `crashed > 0` 或 `activeAlerts > 0`）> `healthy`；复用 `resourceFreshnessAt` 的鲜度口径（`platform_observability_overview.go:149`）。
- **排序**：服务端按 severity 降序 → 指标降序 → `node.ID` 升序；支持 `?sort=cpu|mem|disk|instances|level`。
- **有界**：一次 `SELECT` 全量节点（≤500 上限，去掉 `platformObservabilityLimit` 对矩阵的限制），实例计数用一条 `GROUP BY node_id`（参考 `instanceHealth()` 的聚合写法）。
- **前端**：新增热力墙组件（`apps/control-plane-web/src/pages/` 下新页/组件 + `packages/ui` 网格），置于平台总览页；单元格着色表征 `Level`，tooltip 展开明细，点击 `Href` 下钻单台。
- **平台级权限**：同 `Overview`，`requirePlatformAdmin(c)` 保护。

### 2.2 FR-462 动态基线异常检测

**数据源**：`MetricService.QuerySeries` / `QuerySeriesBatch`（`service/metric.go:456/489`）读 `MetricSampleRaw`（30s，留 ~48h）/ `MetricRollup5m`（30d）/ `MetricRollup1h`（≥1y），scope = `node|instance|world`，metric_key 见 `model/metric.go`（`node_cpu_pct`、`node_mem_used`、`inst_tps`、`inst_mspt`、`inst_cpu_pct`、`inst_players_online`、`inst_heap_used`、`inst_uptime` …）。

**扩展触发类型**（`model/alert.go` 的 `AlertTrigger*` 常量）：新增 `baseline`（动态基线偏离）与 `saturation`（饱和度），保留 `metric`。`AlertEvent.TriggerType` 冗余快照已支持。

**扩展规则字段**（`model.AlertRule`）：

```go
BaselineMethod   string  // ewma | yoy | mom（同比/环比）
BaselineWindowSec int    // 基线窗口（默认 3600）
Sensitivity      float64 // k 倍标准差/中位绝对偏差（默认 3）
MinDelta         float64 // 最小绝对增量，滤小噪声
Direction        string  // up | down | both
Scope            string  // node | instance（决定评估维度）
```

**算法（无状态，按时序窗口现算，避免跨进程状态）**：

| 检测 | 口径 |
|---|---|
| EWMA 偏离 | 对序列最近窗口算 `ewma` 与残差 `σ`/`MAD`，`|x − ewma| > k · max(σ, MAD, ε)` |
| 同环比 | 与前一周期同相位窗口（5m/1h rollup）比对，偏离比 > 灵敏度 |
| 突降突升 | 窗口内 `Δx / Δt` 超 `k·σ`（rate-of-change），`Direction` 过滤方向 |
| 饱和度 | `value / max` 持续 ≥ 阈值（disk/mem/heap/线程） |

**噪声控制**：要求连续 N 次越界（复用 `AlertRule.DurationSec`）+ `MinDelta` + 去抖窗口 `DedupWindowSec` + 静默窗口（`inSilenceWindow`），全部复用 `AlertDispatcher`（`service/alert_dispatcher.go`）的既有能力，本 FR 不新增通知链路。多通道（webhook/钉钉/企微/飞书/discord/telegram/email/inapp）经 `channel_notifier.go` 自动可用。

**实例级覆盖**：`evaluate()` 增加 `TargetType == "instance"`（及 `Scope == instance`）分支，对每实例序列（`MetricScopeInstance`）求值——补齐当前「实例级 metric 规则静默不评估」的缺口。

**评估频率**：raw 档为 30s，评估周期保持 60s（或降到 30s 对齐，避免漏拍）；`onlineThreshold = 90s` 不变。

## 3. 任务拆分

| # | 步骤 | 依赖 |
|---|---|---|
| 1 | `service/health_wall.go`：`HealthWall` 读模型 + `Level` 判定（复用 `resourceFreshnessAt`）+ 排序 | — |
| 2 | `router/observability.go` 注册 `GET /observability/health-wall`；单测（60+ 节点、排序、权限） | 1 |
| 3 | 前端健康墙组件 + 总览页接入 + 一键定位下钻；单机/真浏览器截图 | 2 |
| 4 | `model/alert.go`：`baseline`/`saturation` 触发类型 + `AlertRule` 新字段 + 迁移 | — |
| 5 | `service/alert_baseline.go`：EWMA / 同环比 / 突降突升 / 饱和度纯函数（可单测，注入窗口序列） | 4 |
| 6 | `alert_evaluator.go`：接入基线评估 + 扩展 `TargetType=instance` metric 分支 | 5 |
| 7 | 样本拉取适配器：经 `MetricService.QuerySeries` 取窗口（node/instance） | 5 |
| 8 | 端到端：注入劣化/突降样本，验证规则触发且噪声可控；前端告警可见 | 6,7 |

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 60+ 台节点一屏呈现逐台健康态（矩阵/热力墙），配色区分 `offline/stale/degraded/healthy` | **要真机过**（真 60+ 节点） |
| 2 | 矩阵可排序（按 level/cpu/mem/disk/实例数），点击任意格一键下钻到单台监控/实例 | **要真机过** |
| 3 | 矩阵**不触发 Worker RPC**，一次查询返回全量（无 N+1） | 单测（fake 断言零 RPC） |
| 4 | FR-462：注入**缓慢劣化**（如 mem 每小时 +3%）样本，`baseline` 规则在**无手工阈值**下触发 | **要真机过**（或时序回放） |
| 5 | FR-462：注入**突降**（TPS 台阶下坠），`突降` 检测触发并标 `Direction=down` | 单测 + 真机 |
| 6 | FR-462：饱和度规则（disk/heap）在逼近上限时触发 | 单测 |
| 7 | 实例级 metric 规则（`TargetType=instance`）被评估（当前静默缺口消除），触发落 `AlertEvent` 且经既有通道外发 | 单测 + 真机 |
| 8 | 噪声可控：正常波动（< k·σ）**不**触发；去抖窗口内重复只计 `Count` 不重复通知 | 单测（回放基线样本） |
| 9 | 平台级权限：非平台管理员访问 `health-wall` 被拒 | 单测 |

## 5. 风险 / 待定

- **矩阵规模**：节点数若达数百，逐节点实例计数需一条 `GROUP BY` 聚合而非逐台查询；上限与分页策略待真机压测确认。
- **基线冷启动**：新序列样本不足一个窗口时无法算基线，首版**跳过**（不发告警），避免开服即误报；冷启动时长待定。
- **无状态 vs 有状态**：首版从时序库现算（无跨进程状态，重启无副作用），代价是每周期一次窗口查询。若查询压力大，再引入内存 EWMA 缓存 + 落盘快照。
- **同环比的数据依赖**：`yoy`（同比）需 ≥1 周历史，仅 `1h` rollup 可支撑；上线初期 `yoy` 规则应默认关闭。
- **与既有静态阈值规则的关系**：新增 `baseline`/`saturation` 为**并列**触发类型，不替换 `metric`；存量规则零改动。是否提供「阈值 → 基线」迁移向导，待定。
