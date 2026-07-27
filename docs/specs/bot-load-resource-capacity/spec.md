# 功能规格：500 Bot 全链路资源采样与容量报告

> 状态：已审核　·　关联 PRD：FR-399　·　优先级：P0　·　依赖：FR-398（MCP 编排入口已落地）　·　增强：FR-343/362/370　·　关联 ADR：074-bot-distributed-load（分布式压测）、075-bot-command-orchestration（命令成功边界）　·　下游：TEST-500 真机战役　·　分支：feature/fr-399-bot-load-resource-capacity

## 1. 背景与目标

FR-370 已建立每 5s 一行的 `BotLoadMetricSample` 与最小终态报告（`BotLoadReportJSON`），但采样器对 `executor` / `targetLegacy` / 延迟百分位仍是**空壳**；报告只有 verdict/摘要，**没有**全链路资源峰值、阶段统计与安全容量建议。

FR-399 的目标：在压测运行期间同步采集**目标游戏服**与**各发压节点**资源，按阶段聚合 baseline / 峰值 / p95 / 增量 / 每 Bot 斜率，并输出「实测峰值」与「+25% 安全余量建议」两套可区分数字。MCP（FR-398）可查询当前资源快照、阶段曲线与终态容量报告。缺失指标显式 `unavailable`，**禁止用 0 冒充**。

本期容量目标是：目标 SSH 主机总内存为 64 GiB 时，始终为操作系统和非压测进程预留 8 GiB；压测期间该主机已用内存不得超过 56 GiB。500 Bot 是目标规模而非可伪造的达标标签：若在该内存上限与既有 FR-370 verdict 下无法稳定运行，报告只能给出实测的最大稳定 Bot 数，并明确标为「非 500 Bot 实测」。

本 FR **不**宣称完成 500 Bot 真机战役（TEST-500 另轨）；它交付采样与报告能力，使 TEST-500 有可导出的数据面。

## 2. 需求（要什么）

### 2.1 采样口径（全链路分列，禁止混名）

#### A. 目标游戏服（跟 `session.instance_id` 所属节点，非 executor）

| 字段（逻辑名） | 含义 | 首选数据源 | 缺失时 |
|---|---|---|---|
| `target.processRssBytes` | 游戏根进程及其子进程树的 OS RSS 汇总 | 新增 Worker gRPC 实例资源快照；Worker 按实例根 PID 遍历子进程树 | `null` + `unavailable` |
| `target.heapUsedBytes` / `target.heapMaxBytes` | JVM 堆已用/上限 | ServerProbe → `inst_heap_*` 时序或实时 | `null` + `unavailable` |
| `target.cpuPercent` | 实例进程树 CPU% | 同一 Worker gRPC 实例资源快照 | `null` + `unavailable` |
| `target.uptimeSeconds` | 游戏根进程运行时长 | 同一 Worker gRPC 实例资源快照 | `null` + `unavailable` |
| `target.hostMemUsedBytes` / `target.hostMemTotalBytes` | 目标实例所属节点主机的已用/总内存 | 采样时刻前最近的 `node_mem_used`（≤30s）/节点 `MemoryMB` 注册快照 | `null` + `unavailable` |
| `target.tps` / `target.mspt` | 仅探针可用时 | 已有探针时序；MSPT 保持真实采集口径，不伪称 p95 | `null` + `unavailable`，不进默认 verdict |
| `target.onlinePlayers` | 可选 | 探针 | 同上 |

**语义陷阱（强制）**：

- 探针开启时部分路径用 **heap 覆盖** `memory_mb`；容量报告必须 **RSS 与 heap 分列**，禁止再输出模糊的 `memoryMb` 作为唯一内存字段。
- 目标指标**只**从 Instance 所属节点取，与 Bot 执行分片无关（ADR-074）。

#### B. 发压节点（session 已绑定的每个 executor node）

| 字段（逻辑名） | 含义 | 首选数据源 | 缺失时 |
|---|---|---|---|
| `executor[].nodeId` | 节点 ID | 运行分配表 | — |
| `executor[].activeBots` | 该节点上**本 run** 的 connected + connecting 数 | CP 按 `stress_session_id + executor_node_id` 聚合 | 0 仅当计数查询成功且确实为 0 |
| `executor[].botWorkerRssBytes` | **bot-worker 进程** RSS | `GetBotCapacity.rss_bytes`（真源是 Node 子进程，非 Go Worker） | `unavailable` |
| `executor[].eventLoopP95Ms` | bot-worker 事件环 p95 | GetBotCapacity | `unavailable` |
| `executor[].nodeMemUsedBytes` / `nodeMemTotalBytes` | **节点主机**内存 | 采样时刻前最近 `node_mem_used`（≤30s）/节点 `MemoryMB` 注册快照 | `null` + `unavailable` |
| `executor[].nodeCpuPercent` | 节点主机 CPU | `node_cpu_pct` | `unavailable` |
| `executor[].health` | ready/legacy/unhealthy 等 | Capacity / 心跳 | `unavailable` |
| `executor[].workerProcessRssBytes` | **Go Worker 进程** RSS | 扩展 `GetBotCapacity` 的 Worker 自身 RSS 字段 | `null` + `unavailable` |

**命名铁律**：现有 capacity 的 `rssBytes` **必须**在报告/MCP 中映射为 `botWorkerRssBytes`（或等价明确名），**禁止**标注为「Worker RSS」。

#### C. 运行业务指标（已有 sampler 部分可复用）

- 在线率 / planned / connected（`counts`）
- 命令发送/失败（`command`）
- 调度 lag 百分位（`latency`，本期若仍无真实源则保持 null，不造假）
- 失败/重连（事件或 errors 聚合；无源则 unavailable）

### 2.2 写入与历史真源

1. **5s 采样**继续写 `bot_load_metric_samples`（同 `(session, sampled_at)` upsert 幂等）。
2. 本 FR **填充** `ExecutorJSON` 与目标资源块；优先扩展现有 JSON 列，避免无必要新表。
3. 目标资源固定采用**方案 T1**：扩展 `TargetLegacyJSON` 为结构化 `targetResource` 对象，保留既有 tps/mspt/onlinePlayers 键的兼容形态；不新增表列或迁移。
4. CapacityDirectory 缓存只服务 preflight/分片；**历史曲线以 sample 表为准**，不可把缓存当时序。

### 2.3 阶段聚合与终态容量报告

对 sample 序列按 `stageIndex` 切片，每阶段输出：

| 统计 | 定义 |
|---|---|
| `baseline` | 按 `sampled_at` 排序后跳过该阶段前三个样本窗口，第 4 个样本为 baseline |
| `peak` | 窗口内最大值（内存类用 max；比率类用窗口最小值规则时与 evaluator 一致处单独注明） |
| `p95` | nearest-rank 百分位；样本数 0 → null |
| `delta` | peak − baseline（baseline/peak 任一 unavailable → delta unavailable） |
| `slopePerBot` | 内存增量 / Δbots；target 与 cluster 的 Δbots 为 `counts.connected` 变化，executor 的 Δbots 为该节点 `activeBots` 变化；Δbots≤0 时为 `null` + `unavailable` |

统计窗口从 baseline（含）开始；阶段不足 4 个样本、字段没有可用样本或任一计算输入不可用时，该字段统计为 `null` 并写入 `unavailable`。`p95` 使用 nearest-rank。保留原始 `delta`，但绝不将 Δbots≤0 的结果伪称为每 Bot 斜率。

**分列汇总**：

- `target`：仅目标服
- `executors[]`：每发压节点
- `executorCluster`：Σ 发压节点（botWorkerRss / nodeMem 等可加总字段；health 不求和）
- `grand`：target + executorCluster（仅对可加总资源；语义在报告 disclaimer 写清）

**实测 vs 建议（强制区分）**：

```text
measuredPeakBots     = 本 run 实际达到的最大 connected（不是模板目标）
measuredPeakRss...   = 每个资源字段在全 run 内各自的最大有效值，并携带 observedAt/stageIndex
recommendedRss...    = ceil(measuredPeak * 1.25)   // 仅对实测到的整数资源值
safetyMarginRatio    = 0.25                  // 常量，可配置但不默认改
```

- 文案与字段名必须让 Agent/人一眼区分「实测」与「建议」。
- 若 `measuredPeakBots < 500`、实际 connected 过 Bot 的不同 executor 节点数 < 10、目标主机可用内存低于 8 GiB，或既有 FR-370 verdict 非通过：**禁止**输出任何「500 Bot 实测」标签；可输出 `testedScale` + 可选 `extrapolated`（外推须标注假设与不可作为验收依据）。
- `maxStableBots` 是上述内存和 verdict 约束下的实测最大稳定值；它可以小于 500，但必须标为「非 500 Bot 实测」，不能降低或改写 FR-370 的 verdict 规则。

### 2.4 MCP 查询面（加性扩展 FR-398）

| MCP 工具 | 行为 |
|---|---|
| `loadtest_run_metrics` | 自动透出填充后的 sample（含 executor/target 资源），运行中资源快照只走此工具（`observability.read`） |
| `loadtest_run_report` | JSON 增加 `capacity` 段（见 3.3）；CSV 在既有 summary 行增加固定 `capacity*` 列（`bot.read`） |

不新增 `loadtest_run_capacity_snapshot` 工具。

能力：读路径沿用 `bot.read` / `observability.read`（metrics 已是 observability.read）；不新增破坏性能力。

### 2.5 环境元数据（报告附带）

终态 capacity 报告应尽量附带（缺失 → unavailable 列表，不阻断导出）：

- 目标：MC/核心类型与版本、插件清单摘要、JDK 版本、地图/房间标识（若可从实例配置读取）
- 发压：节点名/硬件摘要（CPU 核数、内存总量）、Worker 版本、bot-worker 版本
- 运行：模板名、目标 bot 数、实际 peak bots、阶段表、开始/结束时间

### 2.6 范围内 / 范围外

**范围内**：

- Sampler 填充 executor（bot-worker RSS/eventLoop/active/health）+ 目标 RSS/heap/CPU/uptime（+ 可选 TPS/MSPT）
- 节点主机内存/CPU 进入 executor 侧字段
- 阶段聚合 + 实测/建议 + unavailable 纪律
- Report / MCP 投影
- 单测与小规模真机（≤10 Bot 即可证明采样链路；500 属 TEST-500）

**范围外**：

- bot-worker CPU%（无源则 unavailable）
- 改写默认 verdict 公式（资源报告不替代连接/命令阈值 verdict）
- SSE 流式资源推送
- TEST-500 战役编排本身、10+ Worker 集群运维
- 管理台大屏重做（可复用现有 obs 页字段 enrich，非必须）

### 2.7 不可用字段纪律

1. 所有已定义数值字段固定存在，未知时为 JSON `null`；同层 `unavailable` 数组列出字段名和原因码。数值字段禁止用 `0` 表示未知，也不混用省略键或 `{"status":"unavailable"}` 对象。
2. 聚合：任一输入 unavailable → 该聚合项 unavailable（不跳过当 0）。
3. CSV：空单元格表示 unavailable（与 bot-load-runner 空值规则对齐），不写 `0`。
4. 日志与 Agent 文案使用中文说明「指标不可用」。

## 3. 设计（怎么做）

### 3.1 Sampler 接线（`BotLoadMetricSampler.SampleSession`）

现状：`ExecutorJSON=[]`、`TargetLegacyJSON` 不写（`bot_load_metric_sampler.go` 空壳注释）。

改动：

1. 从本 session 持久化的 `bot_load_batches.executor_node_id` 解析 executor 节点集合；不得使用会返回全节点的 CapacityDirectory 快照作为 session 集合真源。
2. 对每个 executor node 调用 **已有** `BotLoadCapacityProvider` / CapacityDirectory（注意缓存 TTL 与并发上限，沿用 `bot_load_capacity.go` 常量），并由 CP 聚合本 session 的 `activeBots`，映射：
   ```json
   {
     "nodeId": 1,
     "activeBots": 12,
     "botWorkerRssBytes": 123456789,
     "eventLoopP95Ms": 4.2,
     "nodeMemUsedBytes": null,
     "nodeMemTotalBytes": null,
     "nodeCpuPercent": null,
     "workerProcessRssBytes": null,
     "health": "ready"
   }
   ```
   节点主机内存/CPU：查询采样时刻前最近的时序点，超过 30s 则对应键为 null；总内存取节点 `MemoryMB` 注册快照。
3. 目标实例：新增 Worker gRPC 实例资源快照，按实例根 PID 聚合子进程树的 RSS/CPU/uptime；目标主机内存按同一 30s 时序守卫读取。组装 `targetResource` 写入 `TargetLegacyJSON`。
4. **不在 CP 直连 bot-worker**（三进程边界）；一律 Worker gRPC/心跳。

### 3.2 聚合服务

新增（建议）`BotLoadCapacityReportService` 或扩展 `BotLoadReportService`：

- 输入：sessionID
- 读全量或按阶段分页的 samples
- 输出 `CapacityReport` 结构（Go struct + JSON tags 稳定）

纯函数聚合（percentile/slope）必须单测；时钟可注入。

### 3.3 报告 JSON 加性字段

在既有 `BotLoadReportJSON` 上增加（示例）：

```json
{
  "capacity": {
    "schemaVersion": 1,
    "testedScale": { "peakBots": 10, "plannedExecutorNodeCount": 1, "observedExecutorNodeCount": 1, "claimedAs500": false },
    "targetHostMemory": { "totalBytes": 68719476736, "reserveBytes": 8589934592, "budgetBytes": 60129542144, "withinReserve": true },
    "safetyMarginRatio": 0.25,
    "stages": [
      {
        "stageIndex": 0,
        "target": { "processRssBytes": { "baseline": 1, "peak": 2, "p95": 2, "delta": 1, "slopePerBot": null } },
        "executors": [],
        "executorCluster": {},
        "unavailable": ["target.heapUsedBytes"]
      }
    ],
    "measuredPeak": { "bots": 10, "targetProcessRssBytes": 2, "executorBotWorkerRssBytesSum": 3 },
    "recommended": { "targetProcessRssBytes": 3, "executorBotWorkerRssBytesSum": 4 },
    "environment": { "target": {}, "executors": [], "unavailable": [] },
    "disclaimer": "资源报告不证明玩法正确性；命令成功边界见 ADR-075。measured 为实测，recommended 为 measured×(1+safetyMarginRatio)。"
  }
}
```

旧字段保持兼容；`disclaimer` 资源段与全局 disclaimer 并存。

### 3.4 MCP

- `tools_loadtest_query.go`：`loadtest_run_report` 透传新字段；metrics 无需新增工具或协议。

### 3.5 文档与 ADR

- 更新 `docs/ARCHITECTURE.md` 压测观测段：资源采样真源与命名。
- 更新 `docs/API.md`：HTTP metrics 的 `targetLegacy.targetResource` 与终态 report 的 `capacity` 必须同步写明精确 nullable JSON 契约。
- 更新 MCP 工具描述：metrics 用于运行中快照、report 用于终态 capacity；不新增工具。
- **不强制新 ADR**：本期仍遵循 ADR-074/075，不改变三进程模型或 capacity 语义。

## 4. 任务拆分

- [x] **Spec 审核通过**（2026-07-27，用户确认）
- [x] 失败测试先行：executor 映射命名、unavailable 不写 0、聚合 baseline/peak/p95/delta/slope、measured vs recommended、scale&lt;500 禁止 claimedAs500
- [x] Worker/proto：实例进程树资源快照 + Worker 自身 RSS
- [x] Sampler：填充 `ExecutorJSON`（GetBotCapacity + 节点时序 + 本 session 计数）
- [x] Sampler：填充目标资源（进程树 RSS/CPU/uptime、heap 与目标主机内存分列）
- [x] Capacity 聚合 + 扩展 `BuildJSON`/`BuildCSV`
- [x] MCP 投影 + 契约测试（复用既有 metrics/report 读取路径，无新增工具）
- [x] 文档：PRD 状态→开发中→（交付时）已交付；ARCHITECTURE/API/CHANGELOG
  - [x] 小规模真机（≤10 Bot 或无 Bot 的「采样空跑」）：证据 `.tmp/acceptance-fr399-2026-07-27.txt`
- [ ] TEST-500 不在本任务强制范围；本 FR done 不依赖 500 实测

## 5. 验收标准

1. **命名正确**：对外 JSON 不出现把 bot-worker RSS 称作 Worker RSS 的字段；`workerProcessRssBytes` 只表示 Go Worker 自身 RSS，无源时 unavailable。
2. **无 0 冒充**：单测覆盖「源错误/源缺失 → null/unavailable」，断言响应体无虚假 0。
3. **分列统计**：同一样本可同时读出 target 与至少一个 executor 的资源（在源可用时）。
4. **阶段聚合**：至少 2 个 stage 的 fixture 样本能按跳过前三帧、第 4 帧 baseline 的固定口径算出 baseline/peak/p95/delta。
5. **实测 vs 建议**：`recommended = ceil(measured * 1.25)`，字段分离且带资源峰值观察时间。
6. **规模诚实**：peakBots&lt;500、observed executor&lt;10、目标主机未保留 8 GiB 或 verdict 未通过时 `claimedAs500=false` 且文案无「500 实测」。
7. **MCP**：`loadtest_run_metrics` 可见填充后的 executor；`loadtest_run_report` 含 `capacity`（终态）。
8. **回归**：`go test ./internal/controlplane/...` 全绿；默认 verdict 行为不变；ADR-075 disclaimer 仍在。
9. **真机**（需用户确认）：小规模采样链路证据；**不要求** 500 Bot。
10. **TEST-500**：仅当环境具备 ≥10 Worker 且用户发起时另跑；失败不回滚本 FR 代码交付结论（但不得在报告中谎称 500）。

## 6. 风险 / 待定

| 项 | 说明 | 建议默认 |
|---|---|---|
| 目标资源 JSON 落点 | `TargetLegacyJSON.targetResource` | **T1，锁定** |
| 是否新增 `loadtest_run_capacity_snapshot` | metrics 是否足够 | **不新增**，终态靠 report、运行中靠 metrics |
| Go Worker RSS | Worker 自身 RSS | 扩展 `GetBotCapacity`，无采集结果才 unavailable |
| baseline 预热定义 | 固定 skip | 跳过前三个样本，第 4 帧为 baseline；不足四帧 unavailable |
| 节点时序与 5s sample 对齐 | 最新点 vs 插值 | **查询采样时刻前最近一点**，过旧（&gt;30s）标 unavailable |
| 64 GiB 主机容量约束 | 操作系统预留 | 固定预留 **8 GiB**；不满足时仅报告最大稳定实测规模，不得降级为 500 实测 |
| 与 FR-370 空壳 latency/barrier | 是否本 FR 顺手填 | **不强制**；资源优先 |
| 审核前禁止实现 | SDD 闸 | **遵守**：本 spec 通过前不写业务代码 |

## 7. 与 TEST-500 的边界

```
FR-399 = 数据面（采样 + 聚合 + 报告 + MCP）
TEST-500 = 战役（阶梯 100…500 + 60min 稳定 + 环境门槛）
```

TEST-500 依赖 FR-396/398/399 与真实集群；环境不足时只记录已测规模与外推，结果**不得**写成 500 实测（PRD / brainstorm 已确认）。
