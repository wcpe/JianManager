# 功能规格：观测·统计页补齐（平台级聚合统计）

> 状态：开发中（前端 + 测试全绿，待真机验收）　·　关联 PRD：FR-220　·　分支：feat/fr-220-platform-stats

## 1. 背景与目标

FR-215（观测导航重构，波 1 已落）把「监控」域升级为「观测」域、下设 **监控 / 日志 / 统计** 三子类，其中「统计」子项（`/statistics`，`StatisticsPage`）只放了占位空状态「统计页建设中（FR-220）」。本 FR 把它补成**平台级聚合统计**页：一屏给出节点 / 实例 / 玩家 / 客户端分发等**跨节点、跨频道的总量与构成分布**总览，回答运维「这套平台现在整体规模与健康度如何」的问题。

与同批次相邻 FR 的边界：

- **不是 FR-219**（客户端分发频道工作台「统计」Tab）：那是**单频道**的下载/版本看板（消费 `/client-dist/stats`）；**本页是平台级总览**，跨所有频道、并叠加节点/实例/玩家维度。
- **不是 `/`（OverviewPage）旗舰仪表盘**：OverviewPage（FR-061）以**环形仪表盘 + 实时资源曲线**为主，偏「此刻资源水位」；本统计页以**计数卡 + 构成分布**为主，偏「整体规模与构成」。两者数据源有重叠（同用 `/metrics/overview`），但视角与呈现不同、互补不替代。

属 P1，是 FR-213~221 观测重构批次「统计」落点的实质内容（波 2）。

## 2. 需求（要什么）

### 范围内

把 `web/src/pages/StatisticsPage.tsx` 从占位补成平台级聚合统计页，含以下**统计维度**（卡片 + 分布图表）：

1. **节点维度**：节点总数、在线数、离线数、维护中（cordon）数；按 OS / arch 的构成分布。
2. **实例维度**：实例总数、运行中（RUNNING）、已停止（STOPPED）、**已崩溃（CRASHED）**、过渡态（STARTING/STOPPING）；按角色（proxy / backend / universal）与按进程类型（docker / daemon / direct 等 `processType`）的构成分布。
3. **玩家维度**：当前在线玩家总数；可访问后端探针的连通比例（来自 `/players` 的 `backends`，优雅降级）。
4. **客户端分发维度**（平台管理员可见）：选定区间内的拉取总量（manifest / 制品）、活跃客户端数、更新成功率 / 回滚率 / fail-static 率、版本分布 Top、平台（OS）分布；数据来自 FR-217 观测端点 `/client-dist/observability`（跨频道合并=「总」）。**非平台管理员**（该端点 403）此区块**整体降级隐藏**或显示「需平台管理员权限」提示，页面其余维度不受影响。
5. **区间选择**：复用既有 `RangePicker`（`MetricRange`）控制带时间维度的区块（分发观测的区间口径），节点/实例/玩家为「当前快照」不受区间影响。
6. **空 / 错误态**：任一数据源失败或为空时局部降级（该卡/该图显占位），不整页崩溃；未登录由上层守卫拦截（不在本页处理）。
7. **i18n**：zh / en 同步新增本页所需文案键（标题沿用既有 `statistics.title`=统计，新增各维度小标题/标签键）。
8. **UI 风格**：复用既有 `StatCard` / `Panel` / `MiniBar` / `StatusBadge` 原语与 `index.css` 设计 token（柔和弱阴影、大圆角、双主题品牌色变量），不引入新依赖、不硬编码品牌色。

### 不做（范围外）

- **不新增后端端点 / 表 / 字段 / gRPC**：所有平台级聚合**优先复用既有端点**，构成分布在前端从 `/instances`、`/nodes` 列表**客户端聚合**得出（这些列表已按调用方可访问集合收敛）。FR-220 经核定**无数据缺口需补聚合端点**（见 §3「数据来源与缺口论证」），故本 FR 为**纯前端**。
- **不做单频道下钻**（FR-219 的频道 Tab 负责）、**不做分发时序趋势图**（FR-218 分发监控页负责，消费同端点的 `series`）——本页分发区块只取 `/client-dist/observability` 的 `summary` + 分布标量，不画其 `series` 时序曲线，避免与 FR-218 重叠。
- **不做时序剖析增强**（FR-221）、**不动 OverviewPage / MonitoringPage**。
- **不做导出 / 报表下载**（未列入 FR-220，若需另立 FR）。

## 3. 设计（怎么做）

### 数据来源与缺口论证（核心：优先复用、确无缺口）

| 维度 | 端点（既有） | 取数方式 | 已有前端 hook |
|---|---|---|---|
| 节点总数/在线 | `GET /api/v1/metrics/overview` → `totals.nodeCount`/`onlineNodeCount` | 直接读标量 | `useMetricOverview` |
| 节点离线/维护/OS/arch 分布 | `GET /api/v1/nodes` | 前端聚合：`status===0` 计离线、`maintenance` 计维护、按 `os`/`arch` 分桶 | `useNodes` |
| 实例运行中 | `GET /api/v1/metrics/overview` → `totals.runningInstances` | 直接读标量 | `useMetricOverview` |
| 实例总数/CRASHED/STOPPED/过渡/角色/进程类型分布 | `GET /api/v1/instances` | 前端聚合：按 `status` 桶（含 `CRASHED`）、按 `role` 桶、按 `processType` 桶 | `useInstances` |
| 在线玩家总数 | `GET /api/v1/metrics/overview` → `totals.onlinePlayers`（与 `/players` 一致口径） | 直接读标量 | `useMetricOverview` |
| 探针连通比例 | `GET /api/v1/players` → `backends[].available` | 前端聚合：available/total | `useOnlinePlayers` |
| 分发拉取/活跃/成功率/版本/平台分布 | `GET /api/v1/client-dist/observability`（省略 `channelId`=总） | 读 `summary` + `versionDist`/`platformDist` | **新增** `useClientDistObservability`（前端 hook，仅消费既有端点） |

> **CRASHED 计数缺口的处置**：`/metrics/overview.totals` 只暴露 `runningInstances`，**不含 CRASHED**。但 `/instances` 列表逐条返回 `status`（枚举含 `CRASHED`，见 `lib/threshold.ts`），故前端可从既有列表直接算出完整状态构成（含 CRASHED/STOPPED/过渡），**无需新增后端聚合端点**。`/instances` 已按调用方可访问实例集合收敛（FR-047），口径与权限天然正确。
>
> 结论：FR-220 **不引入任何后端改动**。唯一新增是前端 API 客户端 `web/src/api/clientStats.ts`（或新建 `clientDistObservability.ts`）里的 `useClientDistObservability` hook，封装对**已存在**的 `GET /client-dist/observability` 的调用。

### 端点契约（本 FR 消费的既有端点，逐一列出，便于 TS 类型对齐）

本 FR **不新增/不修改任何端点**，以下为所消费既有端点的契约摘要（详见 `docs/API.md` 对应小节，权限/错误码以 API.md 为准）：

1. **`GET /api/v1/metrics/overview`**（FR-060）
   - 权限：登录（仅聚合总量与曲线）。Query：`range`（默认 24h）。
   - 响应（本页只用 `totals`）：`{ totals: { nodeCount, onlineNodeCount, runningInstances, onlinePlayers, cpuPct, loadAvg, memUsedBytes, memTotalBytes }, resolution, trends:[...] }`。
   - 错误：400 `INVALID_RANGE`/`INVALID_RESOLUTION`；403 `FORBIDDEN`。

2. **`GET /api/v1/instances`**（FR-047）
   - 权限：`instance.read`。Query：可选多维筛选（本页不传，取全集）。
   - 响应：`InstanceInfo[]`，逐条含 `status`（含 `CRASHED`）、`role`、`processType`、`type`。

3. **`GET /api/v1/nodes`**
   - 权限：登录（节点读）。
   - 响应：`NodeInfo[]`，逐条含 `status`（0 离线/1 在线/2 启动中）、`maintenance`、`os`、`arch`。

4. **`GET /api/v1/players`**（FR-054）
   - 权限：`instance.read`。
   - 响应：`{ players:[...], backends:[{instanceId,instanceName,available,error?}] }`。

5. **`GET /api/v1/client-dist/observability`**（FR-217）
   - 权限：**JWT，平台管理员**（非管理员 403 `FORBIDDEN`）。审计：`client_dist_observability.query`。
   - Query：省略 `channelId`=跨频道总；`range`（无 from/to 时枚举 `24h`/`7d`/`30d`/`90d`/`180d`，默认 `7d`）。
   - 响应（本页用 `summary` + `versionDist`/`platformDist`）：`{ summary:{ manifestPulls, artifactPulls, downloadBytes, activeMachines, successRate, failStaticRate, rollbackRate, casHitRate, ... }, versionDist:[{version,count}], platformDist:[{os,count}], series:[...], lagDist:[...] }`。
   - 未知 `channelId` 返 200 空时序+零汇总（本页不传 channelId，取总）。

> **权限分层提示**：本页主体（节点/实例/玩家维度）登录即可见；分发区块依赖平台管理员端点，**对非管理员前端整体降级**（捕获 403 → 隐藏该区块或显权限提示），不致整页 403/崩溃。这是前端按既有端点权限做的呈现降级，**不新增权限节点**。

### 前端实现（`web/src`）

- **新增 hook** `useClientDistObservability(params: { channelId?: string; range: MetricRange })`：放 `api/clientStats.ts`（与既有 `useClientStats` 同域）或新建 `api/clientDistObservability.ts`；`useQuery` 封装 `GET /client-dist/observability`，定义 `ClientDistObservability` TS 接口（`summary`/`versionDist`/`platformDist` 等，对齐上方契约）。**仅消费既有端点，无后端改动。**
- **改造** `StatisticsPage.tsx`：
  - 顶部：标题（`statistics.title`）+ `RangePicker`（控制分发区块区间）。
  - **节点 / 实例 / 玩家 KPI 行**：一排 `StatCard`（节点在线/总、实例运行/总、CRASHED 计数以 `danger` tone 提示、在线玩家、探针连通比例）。
  - **构成分布区**：用 `Panel` 承载若干「分布条」——实例按状态、实例按角色、节点按 OS/arch、（管理员）分发版本 Top / 平台分布；分布条复用 `MiniBar`（占比着色）或简单的「标签 + 计数 + 占比条」行列表（纯展示，不新建图表组件；如已有合适分布组件则复用）。
  - **分发概览区（管理员）**：`StatCard` 行展示拉取总量、活跃客户端、成功率/回滚率/fail-static 率；403 时整区降级。
  - 局部加载/错误/空态：各卡/各区独立降级，沿用既有页（如 OverviewPage/MonitoringPage）的空态写法。
- **纯函数下沉**：状态/角色/OS 分桶聚合逻辑抽为可单测的纯函数（如 `lib/platform-stats.ts`：`tallyBy(list, keyFn)` → `{key,count,pct}[]`），便于 vitest 覆盖，页面只做渲染编排。
- **i18n**：`web/src/i18n/zh.json` + `en.json` 新增本页维度小标题/标签键（如 `statistics.nodes`/`statistics.instances`/`statistics.players`/`statistics.distribution`/`statistics.crashed`/`statistics.dist`（分发）/`statistics.activeMachines`/`statistics.successRate` 等；沿用既有 `statistics.title`）。移除/保留占位键 `statistics.placeholder`：保留键不删（FR-215 测试/历史可能引用），但页面不再渲染占位空状态。
- **Mock 同步**：`web/src/mocks/handlers/domains/observ.ts` 新增 `GET /client-dist/observability` 处理（返结构化假数据 + `requireAuth`/平台管理员校验路径，与既有 `/metrics/overview` handler 同风格），保证 MSW 内存假后端下统计页可渲染、vitest 不打真网络。

### 测试（vitest，jsdom）

- **纯函数单测**：`tallyBy`/占比计算（含空列表、单一桶、CRASHED 计入）。
- **页面 dom 测试** `StatisticsPage.dom.test.tsx`：注入 mock 后渲染 → 断言节点/实例/玩家 KPI 出数、实例状态分布含 CRASHED、（管理员态）分发区块出现；注入分发端点 403 → 分发区块降级且页面不崩溃、其余维度仍在；注入 `/metrics/overview` 或 `/instances` 500 → 局部降级、页面不崩溃。

## 4. 任务拆分

- [x] 写本 spec（平台级维度 + 数据来源缺口论证 + 端点契约 + gate-api 自检）
- [x] 新增前端 hook `useClientDistObservability`（消费既有 `/client-dist/observability`，定义 TS 类型）
- [x] 抽 `lib/platform-stats.ts` 纯函数（分桶/占比）+ 单测
- [x] 改造 `StatisticsPage.tsx`：KPI 行 + 构成分布区 + 分发概览区（管理员降级）+ 局部空/错误态
- [x] i18n zh/en 新增本页维度键
- [x] mock 新增 `/client-dist/observability` handler（落在 `domains/client.ts`——client-dist 路由既有归属，非 observ.ts）
- [x] vitest：纯函数 + `StatisticsPage.dom.test.tsx`（含 CRASHED 分布、403 降级、500 不崩）；并修 FR-215 `Workspace.routes.dom.test.tsx` 占位文案断言（改为统计页标题）
- [x] 文档同步：PRD FR-220 状态 → 🔨 开发中、ARCHITECTURE §8 统计子项描述更新为平台级聚合（FR-220 已补）、本 spec 任务勾选；**API.md 无需改**（不动端点）
- [ ] 真机验收（需用户确认）

## 5. 验收标准

- 进入 `/statistics`（观测 → 统计），不再显示「统计页建设中」占位，而是平台级聚合统计页。
- **节点维度**：显示节点总数/在线/离线/维护，OS/arch 构成分布与 `/nodes` 数据一致。
- **实例维度**：显示实例总数与运行中/已停止/**已崩溃（CRASHED）**/过渡态计数，角色与进程类型构成分布与 `/instances` 数据一致；CRASHED 有显著（danger）视觉。
- **玩家维度**：显示在线玩家总数（与 `/metrics/overview.totals.onlinePlayers` 一致）与探针连通比例。
- **分发维度（平台管理员）**：显示拉取总量/活跃客户端/成功率/回滚率/版本 Top/平台分布，跨频道总口径，与 `/client-dist/observability` 一致；**非平台管理员**该区块降级（隐藏或权限提示），页面其余维度正常、不报 403、不崩溃。
- 区间切换（RangePicker）改变分发区块区间口径；节点/实例/玩家为当前快照不受影响。
- 任一数据源 500 / 空 → 局部降级、整页不崩。
- i18n zh / en 均有新键，切换语言文案正确。
- **不新增任何后端端点/表/字段**（diff 中后端无改动；仅前端 + 文档）。
- `npm run build` / `tsc --noEmit` / `lint` / `vitest run` 全绿（CI 门禁口径）。
- **真机验收（需用户确认）**：登录后真浏览器进观测→统计，平台管理员见全维度含分发；以非平台管理员账号进同页，分发区块降级、其余正常。单元/E2E 绿不替代此项。

## 6. 风险 / 待定

- **与 FR-218 的分发区块边界**：本页分发区块只取 `summary` + 分布标量、不画 `series` 时序，FR-218（分发监控页）负责时序趋势。若后续 FR-218 落地，二者同源（`/client-dist/observability`）但呈现不重叠；需 FR-218 实现时知会复用同一 hook。
- **分发端点权限（平台管理员）**：登录但非平台管理员用户，分发区块经 403 降级——需确保前端 axios 拦截器不把该 403 当全局登出/整页错误处理（仅本请求局部 catch）。落地时验证：该 403 不触发全局 401/登出逻辑。
- **`/instances` 全集规模**：本页取 `/instances` 全集做前端聚合，超大规模（数千实例）下载列表可能偏重。当前 FR 仍按既有列表端点（已分页/收敛能力以现状为准）实现；若未来实例数极大需服务端聚合，另立 FR，不在本 FR 提前优化（YAGNI）。
- **占位 i18n 键保留**：`statistics.placeholder`/`placeholderHint` 保留不删（避免 FR-215 历史引用断裂），仅页面不再渲染占位。
