# 功能规格：群组拓扑规模化（FR-335）

> 状态：草拟　·　关联 PRD：FR-335（增强 FR-145）　·　分支：待建　·　免 ADR（聚合只读端点 + 前端 SVG 视口交互，无架构决策；关系模型沿用 ADR-007）

## 1. 背景与目标

群组页（NetworksPage）两处 N+1 + 一处布局不可规模化：

1. **拓扑页 per-proxy N+1**：`TopologyGraph`（`apps/control-plane-web/src/components/console/TopologyGraph.tsx:36-46`）对每个 proxy 用 `useQueries` 发一条 `GET /proxies/:id/registrations`；proxy 增多请求数线性涨。后端 `RegistrationService.List`（`internal/controlplane/service/registration.go:85-99`）内部还对每条注册单查 `backendBrief`（`:267-281`），DB 侧又一层 N+1。
2. **列表页 per-network N+1**：`NetworkList`（`apps/control-plane-web/src/pages/NetworksPage.tsx:196-206`）对每个群组用 `useQueries` 发一条 `GET /networks/:id` 只为统计成员健康分布（`memberHealth`，`src/lib/topology.ts:199-233`）；后端 `NetworkService.Get`（`internal/controlplane/service/network.go:116-139`）对每成员单查实例，`List`（`:76-96`）对每群组单发 Count。
3. **布局不可规模化**：`layoutTopology`（`src/lib/topology.ts:154-197`）两列纵向线性堆叠，SVG 高度 `rows*rowHeight + (rows-1)*paddingY` 随节点数线性膨胀（`NODE_H=44/PADDING_Y=18`，`TopologyGraph.tsx:16-20`；单列百级行即 >6000px，110 行 ≈6.8k px）；组件仅 `overflow-x-auto`（`:86`），无缩放/平移/搜索/筛选，百级节点无法定位。

后端 `router/registration.go`、`router/network.go` 均无聚合端点。

**目标**：① 后端一次请求给全整张拓扑（全部 proxy 含注册与后端概要）；② `GET /networks` 概要内联成员健康计数，列表页零详情请求；③ 前端拓扑图升级为可缩放/搜索/筛选/分组分层的 SVG 视口，100+ 节点可用。

## 2. 需求（要什么）

### 2.1 后端：`GET /api/v1/topology` 聚合端点

- 一次返回**全部** proxy（id/name/status/serverPort/nodeId）及其 registrations（条目与既有 `GET /proxies/:id/registrations` 的 `RegistrationView` 同构：注册字段 + `backend` 概要 id/name/role/nodeId/serverPort/status）。
- 附带 `networks`（id/name/memberInstanceIds）供前端按 network 分组布局（软标签非独占，实例可多归属，ADR-007）。
- service 层 **IN 查询组装**（固定次数查询，无 per-proxy/per-registration 循环单查）。
- 权限：平台管理员（与既有 `/proxies/:id/registrations`、`/networks` 同组注册，`internal/controlplane/router/router.go:346-351`）。
- 契约细节见同目录 `api.md`。

### 2.2 后端：`GET /networks` 概要增列成员健康计数

- `NetworkSummary` 增 `memberStatus` 计数桶：`{running, stopped, crashed, starting, stopping}`（实例五态，零补齐）。
- `NetworkService.List` 一次 JOIN+GROUP BY 聚合出全部群组的计数（同时替换现存 per-network Count 循环，`network.go:83-86`）。
- 兼容：既有字段不动；`memberCount` 改为 JOIN 后实际存在实例的计数（悬空成员关系不再计入——与详情页成员列表口径一致，轻微行为修正，见 §6）。

### 2.3 前端：拓扑单查询 + SVG 视口壳

- `TopologyGraph` 去掉 per-proxy `useQueries`，改单条 `useTopology()` 消费聚合端点；`NetworksPage` 拓扑视图不再需要 `useInstances({ role: 'proxy' })`（`NetworksPage.tsx:62/:131` 现仅为拓扑传参）。
- SVG 视口壳（新增交互，全部纯前端）：
  - **pan/zoom**：`viewBox` 状态驱动——滚轮以光标为锚缩放（范围 0.5×~4×）、拖拽平移；容器定高（如 560px）不再随节点数膨胀。
  - **适应视图**按钮：一键把 viewBox 复位到内容包围盒。
  - **名称搜索**：输入高亮匹配节点（非匹配降透明度）、可切换「仅显示匹配及其相邻」过滤。
  - **状态筛选**：按运行/崩溃/过渡/停止（等级复用 `instanceStatusLevel`）+ 禁用连线的显隐筛选。
  - **按 network 分组分层布局**：替代单列堆叠——每个 network 一个分层带（带名标题），带内仍 proxy 左列/backend 右列；不属任何 network 的节点归「未分组」带；多归属实例落首个所属带并加角标提示（见 §6）。布局仍为 `lib/topology.ts` 纯函数（新增 `layoutTopologyGrouped`），vitest 可测。
- 图例（`TopologyLegend`）与节点盒渲染（`TopoNodeBox`）沿用。

### 2.4 前端：列表页健康分布直接用概要计数

- `NetworkList` 删除 per-network `useQueries`（`NetworksPage.tsx:196-206`），`HealthDistribution` 直接消费 `NetworkSummary.memberStatus`；`lib/topology.ts` 增纯函数 `memberHealthFromStatus(counts): MemberHealth`（starting+stopping → transitioning 桶，口径与既有 `memberHealth` 一致）。

### 2.5 devmock 同步

- `packages/devmock/src/handlers/domains/network.ts`：增 `GET /topology` 处理器（由既有 `registrations`/`networks`/实例假数据组装同构响应）；`toSummary`（`GET /networks`，`:158-161`）增 `memberStatus`。

### 2.6 不做（范围外）

- 引入图库（d3/reactflow 等）——沿用 SVG 自绘 + 纯函数布局（FR-145 既定取向）。
- 拓扑编辑（增删注册）——仍走既有注册管理入口。
- 千级节点的 SVG 虚拟化/Canvas 渲染（百级为本 FR 目标；千级另立 FR）。
- WebSocket/SSE 实时拓扑推送（沿用 TanStack Query 缓存失效）。
- 非管理员的拓扑只读视图（权限面不动）。

## 3. 设计（怎么做）

全部在 **Control Plane + 前端**（关系数据归 CP DB，无 gRPC/Worker 改动）。

### 3.1 Service 层

- `internal/controlplane/service/registration.go` 新增 `Topology()`（或独立 `TopologyService`，拍板：挂 RegistrationService，复用其 db 与 BackendBrief 类型）：
  1. `SELECT … FROM instances WHERE role = 'proxy'`（proxy 概要）。
  2. `SELECT … FROM server_registrations WHERE proxy_id IN ? ORDER BY priority asc, id asc`（一次取全量注册，排序与既有 `List` 一致，`registration.go:91`）。
  3. `SELECT id,name,role,node_id,server_port,status FROM instances WHERE id IN ?`（distinct backendId 一次取概要，替代 per-registration `backendBrief`）。
  4. 内存组装 `[]ProxyTopology{ Proxy概要, Registrations []RegistrationView }`；backend 概要缺失（实例已删）时 `backend: null` 容错（与既有 `backendBrief` 查不到返 nil 一致，`:267-281`）。
- `internal/controlplane/service/network.go`：
  - `List` 改一次聚合：`SELECT nm.network_id, i.status, COUNT(*) FROM network_members nm JOIN instances i ON i.id = nm.instance_id GROUP BY nm.network_id, i.status`，映射进 `NetworkSummary.MemberStatus`（五态零补齐）与 `MemberCount`（求和）。
  - 供拓扑用的 `MembersIndex()`：`SELECT network_id, instance_id FROM network_members`（一次全取；networks 本身走既有 `Find`）。
- 新类型：`ProxyTopology`、`TopologyResult{ Proxies []ProxyTopology; Networks []NetworkTopoBrief }`、`NetworkTopoBrief{ ID, Name, MemberInstanceIDs []uint }`、`MemberStatusCounts{ Running, Stopped, Crashed, Starting, Stopping int }`（JSON 小写键见 api.md）。

### 3.2 Router 层

- 新增 `internal/controlplane/router/topology.go`：`TopologyHandler`（依赖 RegistrationService + NetworkService），`GET /topology` → 组装 `TopologyResult`。注册在 **admin 组**（`router.go` 的 `registrationHandler`/`networkHandler` 同段，`:346-351`）。
- `router/network.go` 的 `List` 无改动（响应结构随 service 层 `NetworkSummary` 扩展自然带出）。

### 3.3 前端

- 新增 `apps/control-plane-web/src/api/topology.ts`：`TopologyResponse` 类型 + `useTopology()`（`GET /topology`，queryKey `['topology']`；`Registration` 类型复用 `api/registrations.ts:5-22`）。
- `src/lib/topology.ts`：
  - `buildTopology` 入参形态不变（`ProxyRegistrations[]`），由 `TopologyResponse.proxies` 直接映射（proxy 概要字段满足 `buildTopology` 消费面：id/name/status/serverPort/nodeId，`:74-83`）。
  - 新增 `groupTopology(topo, networks): GroupedTopology`（节点按 memberInstanceIds 归带、未归属入 ungrouped、多归属取首带）与 `layoutTopologyGrouped(grouped, opts): LaidTopology & { bands: [{name, y, height}] }`——带内沿用两列布局逻辑，带间留分隔；纯函数 + vitest。
  - 新增 `memberHealthFromStatus(counts: MemberStatusCounts): MemberHealth`。
- `src/components/console/TopologyGraph.tsx` 重构：
  - 数据：`useTopology()` 单查询（删 `:36-46` useQueries 块）。
  - 视口：`<svg viewBox={vb} …>` 外包定高容器；wheel（`preventDefault` + 光标锚点换算）与 pointer 拖拽改 `viewBox`；工具条（适应视图按钮 + 搜索 Input + 状态筛选 pills + 禁用线开关）。
  - 高亮/过滤在渲染层做（节点/连线 opacity 或裁剪），布局结果缓存不重算。
- `src/pages/NetworksPage.tsx`：拓扑视图传参改造（TopologyGraph 自取数据后 `proxies` prop 移除）；`NetworkList` 删 useQueries、`HealthDistribution` 改吃 `memberHealthFromStatus(n.memberStatus)`。

### 3.4 测试

- Go：`Topology()` 单测（IN 组装正确、backend 缺失容错、空拓扑）；`NetworkService.List` 聚合单测（计数桶正确、悬空成员剔除、零补齐）。
- vitest：`groupTopology`/`layoutTopologyGrouped`/`memberHealthFromStatus` 纯函数测试；TopologyGraph DOM 测试（单请求断言、搜索高亮、状态筛选、fit-view 复位 viewBox）；NetworksPage DOM 测试更新（列表零 per-network 请求）。

## 4. 任务拆分

- [ ] Service：`RegistrationService.Topology()` + `NetworkService.List` 聚合改造 + `MembersIndex()`（含 Go 单测）
- [ ] Router：`topology.go` 新 handler + admin 组注册
- [ ] devmock：`GET /topology` + `/networks` 概要 `memberStatus`
- [ ] 前端 api：`api/topology.ts`（类型 + `useTopology`）
- [ ] 前端 lib：`groupTopology` / `layoutTopologyGrouped` / `memberHealthFromStatus`（含 vitest）
- [ ] 前端 `TopologyGraph`：单查询 + viewBox pan/zoom + 适应视图 + 搜索 + 状态筛选 + 分组分层渲染（i18n 键 zh/en）
- [ ] 前端 `NetworksPage`：列表健康分布改概要计数、删 per-network useQueries
- [ ] DOM 测试新增/更新（请求数断言 + 交互断言）
- [ ] 文档同步：PRD 状态、`docs/API.md`（新端点 + `/networks` 响应变更）、CHANGELOG；ARCHITECTURE 无架构变更不动

## 5. 验收标准

- [ ] **拓扑请求数 O(1)**：进入拓扑视图仅 1 条 `GET /topology`（外加页面既有 `GET /networks`），无 per-proxy 注册请求（MSW 请求计数断言；真机 Network 面板复验，需用户确认）。
- [ ] **列表请求数**：群组列表页零 per-network 详情请求，健康分布条数据与逐个打开详情所见一致（DOM 测试 + Go 聚合单测双向）。
- [ ] **100+ 节点可用**：devmock seed 100+ 实例注册拓扑，容器定高不膨胀；滚轮缩放、拖拽平移、适应视图复位、名称搜索定位高亮、状态筛选均可操作（DOM 测试覆盖交互，缩放灵敏度真机走查，需用户确认）。
- [ ] **分组分层**：成员归属正确落带、未分组带兜底、多归属节点有角标；带内 proxy/backend 两列关系与连线正确（vitest 纯函数 + DOM）。
- [ ] **同构性**：`/topology` 中任一 proxy 的 `registrations` 与对其单发 `GET /proxies/:id/registrations` 响应逐字段一致（Go 测试断言）。
- [ ] **兼容**：`GET /proxies/:id/registrations`、`GET /networks/:id` 契约不变；既有 networks/registrations 测试全绿。
- [ ] vitest / `go test ./...` 全绿。

## 6. 风险 / 待定

- **`/topology` 载荷上界**：全量 proxy×注册一次返回，百级注册 JSON ≈ 数百 KB 可接受；若未来达数千注册需分页/按 network 过滤参数（预留 query `?networkId=` 增量加，本期不做）。
- **`networks` 内联进拓扑响应**：为「按 network 分组布局」提供成员归属，是对 PRD 行「返回全部 proxy 含注册」的最小扩充。若评审认为载荷应更克制，回退方案 = 前端按连通分量聚类分组（无 network 命名，体验降级）——待评审拍板。
- **多归属实例的分组呈现**：软标签非独占（ADR-007），一实例可属多 network。拍板「落首个所属带 + 角标 +n」；备选「每带重复渲染」会使连线跨带复杂化，不取。真机走查若语义混淆再调。
- **`memberCount` 口径修正**：JOIN 后悬空成员（实例已删但 network_members 残留）不再计入，与详情页 `Get` 的跳过口径（`network.go:127-128`）对齐；数字可能比现值小——属修正非回归，CHANGELOG 注明。
- **wheel 缩放与页面滚动冲突**：SVG 容器内 wheel 需 `preventDefault`，可能与整页滚动手感冲突；采用「Ctrl+滚轮缩放 / 裸滚轮不拦截」或「悬停即拦截」两种模式之一，真机走查定。
- **权限面**：`/topology` 限平台管理员（与现有注册/群组读取一致）。若后续要求组管理员看到其可访问实例的子拓扑，需按 AccessibleInstanceIDs 裁剪——另立 FR。
