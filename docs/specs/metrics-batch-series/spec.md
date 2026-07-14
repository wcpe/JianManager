# 功能规格：指标批量序列接口（FR-334）

> 状态：草拟　·　关联 PRD：FR-334（增强 FR-060/FR-270，顺带收 gap:FR-270）　·　分支：待建　·　免 ADR（常规 REST 批量查询端点，无架构决策；沿用 ADR-013 时序存储）

## 1. 背景与目标

节点详情「实例」页签的各实例指标对比 `NodeInstanceCompare`（`apps/control-plane-web/src/pages/NodesPage.tsx:169-216`）当前对节点下**每个实例**发一条 `GET /metrics/series?scope=instance&targetId=<uuid>&range=<r>&metrics=<key>`（`useQueries` 并行，`:177-188`），且 `refetchInterval: 30_000` 周期重拉。N 实例 = 每 30s N 条请求：600 实例节点即 ~600 请求/周期的请求风暴，压 CP HTTP 层与浏览器并发队列。

同时该组件与 `NodesPage` 页面级还在用 `useInstances()` **全量拉取**实例再前端过滤（`:172-173`、`:446`）、前端归并各节点实例数（`instanceCountByNode`，`:486-490`）——FR-247 已交付的服务端分页/聚合接口（`/instances/search`、`/instances/aggregate`）在此页未被消费（gap:FR-270）。

后端现状：`internal/controlplane/router/metric.go` 只有单 targetId 端点（`RegisterRoutes` 仅 `GET /metrics/series|/metrics/overview|/metrics/processes/top`，`:27-32`；`Series` 处理器 `:45-121`）；`internal/controlplane/service/metric.go` 的 `QuerySeries`（`:439-466`）按单目标 `WHERE instance_id = ?` 查序列，无批量形态。

**目标**：新增一条多目标批量序列端点，把「N 实例对比」的请求数从 O(N) 压到 O(1)；前端对比组件改单条批量查询并设对比目标上限；节点页实例清单/计数一并切换到 FR-247 聚合接口收口 gap:FR-270。

## 2. 需求（要什么）

### 2.1 后端：`POST /api/v1/metrics/series/batch`

- 一次请求携带 `targetIds`（实例 UUID 数组）+ 与既有单目标端点同语义的 `range` / `metrics` / `resolution`，一次返回各目标的序列数据。
- **上限**：`targetIds` 去重后 ≤ 50，超出 `422 TOO_MANY_TARGETS`；空数组 `400 INVALID_REQUEST`。
- **鉴权**：逐目标复用既有实例访问收敛（与 `GET /metrics/series` 的 `scope=instance` 分支同语义，`router/metric.go:103-111`）。**拍板：无权/不存在的目标剔除不整拒**——响应附 `skipped` 列表（`targetId` + `reason: forbidden|not_found`），有权目标正常返回。理由：对比场景下个别目标越权（如实例刚被移出可访问组）不应让整图 403 空白。
- 响应中单个 targetId 对应的数据与单目标端点 `series` 数组**同构**（`metricKey/unit/world/points[{ts,avg,min,max}]`）。
- v1 仅支持 `scope=instance`（对比场景只有实例维度有 N+1；node 维度单节点单查询无此问题）。
- 契约细节见同目录 `api.md`。

### 2.2 前端：NodeInstanceCompare 改批量 + 目标上限

- `api/metrics.ts` 新增 `useMetricSeriesBatch`，`NodeInstanceCompare` 由 useQueries × N 改为**单条**批量查询（仍 30s 轮询）。
- **对比目标数上限**：最多取前 12 个实例参与对比（名称升序），超出时图表上方显示「仅显示前 12 / 共 N 个实例」提示（i18n 新键）。50 是后端硬上限，12 是前端可读性上限（一图 50 条线不可读）。
- 节点内实例清单改 `useInstanceSearch({ nodeId, pageSize: 12, sort: 'name', order: 'asc' })`（FR-247 已交付，`api/instances.ts:120-137`），`total` 即提示中的 N；替换组件内 `useInstances()` 全量拉取+过滤。

### 2.3 前端：NodesPage 消费聚合接口（gap:FR-270 收口）

- 页面级 `useInstances()`（`NodesPage.tsx:446`）与 `instanceCountByNode` 前端归并（`:486-490`）改为 `useInstanceAggregate().byNode`（`api/instances.ts:91-101/163-172`），列表行与详情头的实例数（`:664/:681`）从聚合结果取。改后 NodesPage 不再有实例全量拉取。

### 2.4 devmock 同步

- `packages/devmock/src/handlers/domains/observ.ts` 增 `POST /metrics/series/batch` 处理器（复用既有 `rangePlan`/`instanceSeries` 生成器，`:547/:592`），保证 mock 模式与 DOM 测试可跑。`/instances/search`、`/instances/aggregate` devmock 已有（`domains/instance.ts:506/:522`），无需新增。

### 2.5 不做（范围外）

- `scope=node` 的批量（无场景）；`from/to` 自定义窗口入批量 body（对比场景只用 `range` 枚举，需要时增量加）。
- 既有 `GET /metrics/series` 行为不动（其他消费方：`MonitoringPage`、`MetricsSegment`、`use-target-series.ts` 均为单目标场景，不迁移）。
- 序列点位的跨序列 SQL 级合并聚合（沿用现有 per-series 点查询，见 §3.1）。
- SSE/WS 推送替代轮询（另立 FR）。

## 3. 设计（怎么做）

全部在 **Control Plane + 前端**（指标数据归 CP DB，无 gRPC/Worker 改动，守架构不变量）。

### 3.1 Service 层（`internal/controlplane/service/metric.go`）

- 新增 `ResolveInstanceIDs(uuids []string) (map[string]uint, error)`：一次 `SELECT id, uuid FROM instances WHERE uuid IN ?`（现有 `ResolveInstanceID`（`:114-124`）的批量形态；查不到的 uuid 不在 map 中 → handler 归入 `skipped: not_found`）。
- 新增 `QuerySeriesBatch(q SeriesQuery, instanceIDs []string) (resolution string, out map[string][]Series, err error)`：
  - 复用 `selectResolution`（`:127-140`）选档一次，整批同档同窗（响应顶层只出一份 `resolution/from/to`）。
  - **序列表一次查库**：`WHERE instance_id IN ? [AND metric_key IN ?] ORDER BY instance_id, metric_key, world`（对照单目标版 `QuerySeries` 的 `WHERE instance_id = ?`，`:444-453`），按 `instance_id` 分组。
  - 点位加载复用既有 `queryPoints`（`:468-506`，raw/5m/1h 三档各查各表）——每序列一查与单目标版一致；批量收益在 **HTTP 请求数 O(N)→O(1)** 与序列表查询合并，DB 点查为本地 SQLite/MySQL 索引查询开销可接受。若真机压测不达标再做 `series_id IN` 合并点查（见 §6）。
  - 无序列的目标返回空数组条目（`out[targetId] = []`），与单目标端点空 `series` 语义一致，前端可区分「无数据」与「被剔除」。
- `SeriesQuery`（`:42-49`）结构不动，批量走独立入参 `instanceIDs`。

### 3.2 Router 层（`internal/controlplane/router/metric.go`）

- `RegisterRoutes` 增 `m.POST("/series/batch", h.SeriesBatch)`（`:27-32` 处）。POST 承载只读查询：50 个 UUID 入 query string 过长，body 语义清晰；无副作用、幂等。
- `SeriesBatch` 处理流程：
  1. `getAccess` 判空 → 403（与既有处理器一致，`:46-50`）。
  2. 解析 body：`scope` 必须 `instance`（否则 `400 INVALID_SCOPE`）；`targetIds` 去重，空 → `400 INVALID_REQUEST`，>50 → `422 TOO_MANY_TARGETS`；`range` 走 `metricRangeDurations` 枚举（`:34-41`，默认 24h），非法 → `400 INVALID_RANGE`；`resolution` 枚举校验同 `:59-65`，非法 → `400 INVALID_RESOLUTION`。
  3. `ResolveInstanceIDs` 一次解析 uuid→id；解析失败的入 `skipped(not_found)`。
  4. **鉴权批量化**：`h.authz.AccessibleInstanceIDs(access)` 一次取可访问实例 id 集（既有 API，`router/player.go:48` 已用；platform admin 返回 scoped=false 全放行），集合外的目标入 `skipped(forbidden)`——与逐目标 `CanAccessInstance` 判定等价但免 N 次查询。
  5. 幸存目标 `QuerySeriesBatch` → `200 { resolution, from, to, series: {targetId: [...]}, skipped: [...] }`。全部被剔除也返回 200（`series: {}` + 完整 `skipped`），由前端决定呈现。

### 3.3 前端（`apps/control-plane-web/`）

- `src/api/metrics.ts`：
  - 新增 `MetricSeriesBatchResponse`（`resolution/from/to/series: Record<string, MetricSeries[]>/skipped`）与 `useMetricSeriesBatch(params: { scope: 'instance'; targetIds: string[]; range: MetricRange; metrics?: string[]; resolution?: MetricResolution; enabled?: boolean })`。
  - `queryKey: ['metricSeriesBatch', scope, targetIds.join(','), range, metrics?.join(',') ?? '', resolution ?? 'auto']`；`refetchInterval: 30_000`；`enabled: targetIds.length > 0`。类型与既有 `MetricSeries/SeriesPoint`（`:66-86`）复用。
- `src/pages/NodesPage.tsx`：
  - `NodeInstanceCompare`（`:169-216`）：`useInstances()` 换 `useInstanceSearch({ nodeId: node.id, pageSize: 12, sort: 'name', order: 'asc' })`；`useQueries` 块（`:177-188`）整体替换为一条 `useMetricSeriesBatch({ scope: 'instance', targetIds, range, metrics: [metric] })`；`series` 组装从 `data.series[inst.uuid]` 取（`COMPARE_METRICS` 键 `inst_tps/inst_mspt/inst_heap_used/inst_threads` 不变，`:161-166`）；`total > 12` 时渲染「仅显示前 12 / 共 N」提示。
  - 页面级：`useInstances()`（`:446`）删除，`instanceCountByNode`（`:486-490`）改由 `useInstanceAggregate()` 的 `byNode` 构造 Map，消费点（`:664/:681`）不变。
- `packages/devmock/src/handlers/domains/observ.ts`：增 `domainRoute('post', '/metrics/series/batch', ...)`——校验 targetIds 上限/空、按 targetId 生成 `instanceSeries(range)` 同构数据、支持 `metrics` 过滤，形态与真后端契约一致。

### 3.4 测试

- Go：service 单测（批量分组正确、无序列目标空数组、metrics 过滤）；handler 单测（上限 422、空 400、range/resolution 非法 400、**鉴权：受限用户的越权目标进 skipped 且响应不含其数据**、not_found 进 skipped、全剔除仍 200）。
- 前端 vitest：`useMetricSeriesBatch` 契约测试（对 devmock）；`NodesPage` DOM 测试更新——断言对比页签只发 1 条 batch 请求（MSW 请求计数）、超 12 实例出提示、实例计数走 aggregate。

## 4. 任务拆分

- [ ] Service：`ResolveInstanceIDs` + `QuerySeriesBatch`（含 Go 单测）
- [ ] Router：`POST /metrics/series/batch` 处理器 + 路由注册（含鉴权/边界单测）
- [ ] devmock：`/metrics/series/batch` 处理器
- [ ] 前端 api：`useMetricSeriesBatch` + 类型 + 契约测试
- [ ] 前端 `NodeInstanceCompare`：批量查询 + `useInstanceSearch({nodeId})` + 前 12 上限提示（i18n 键 zh/en）
- [ ] 前端 `NodesPage`：`useInstanceAggregate().byNode` 替换全量拉取归并（gap:FR-270）
- [ ] DOM 测试更新与新增（请求数断言）
- [ ] 文档同步：PRD 状态、`docs/API.md`（新端点）、CHANGELOG；ARCHITECTURE 无架构变更不动

## 5. 验收标准

- [ ] **请求数**：600 实例节点打开「实例」对比页签，每 30s 刷新周期内指标请求由 ~600 条降到 **1 条**（batch）；页签首次进入总请求 ≤ 3（batch + search + aggregate）。mock 下以 MSW 请求计数断言，真机以浏览器 Network 面板复验（需用户确认）。
- [ ] **同构性**：batch 响应中任一 targetId 的序列数组与对其单发 `GET /metrics/series` 的 `series` 字段逐字段一致（Go 测试断言）。
- [ ] **鉴权**：受限用户请求含越权/不存在目标 → 有权目标正常返回、越权目标进 `skipped` 且无数据泄露（单测覆盖）。
- [ ] **边界**：51 个目标 → 422；空 targetIds → 400；非法 range/resolution → 400；全部剔除 → 200 + 空 series。
- [ ] **上限提示**：>12 实例节点，图表仅 12 条线 + 「仅显示前 12 / 共 N」提示可见（DOM 测试）。
- [ ] **兼容**：`GET /metrics/series` 行为不变，既有 metrics 相关测试全绿。
- [ ] vitest / `go test ./...` 全绿。

## 6. 风险 / 待定

- **点位仍 per-series 查询**：批量后单请求内 DB 查询数 ≈ 序列数（≤50 目标 × 每目标少量序列，单 metric 过滤下 ≈ 目标数）。SQLite 本地索引查询微秒级，预计可接受；若真机 600 实例（分 50 一批）仍慢，升级为 `queryPoints` 的 `series_id IN` 合并批查（结构已预留分组）。
- **剔除 vs 整拒已拍板剔除**：调用方必须消费 `skipped` 才能区分「无数据」与「无权」；前端 v1 仅在开发态 console.warn，不做 UI 呈现——是否需要在图例中标注被剔除目标，待真机走查定。
- **对比上限取「名称序前 12」**：未按运行状态优先（RUNNING 优先需后端自定义排序，`/instances/search` 的 `sort=status` 是字典序不满足）。若产品要「运行中优先」，需给 search 端点加复合排序，另行增量。
- **50 上限值**：与 `processes/top` 的 limit 上限（`parseProcessTopLimit`，`router/metric.go:210-216`）对齐取 50；前端一次最多要 12，余量充足。若未来出现 >50 的合法批量消费方，分批并发由调用方处理。
