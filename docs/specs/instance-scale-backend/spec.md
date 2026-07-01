# 功能规格：实例规模化后端（FR-247）

> 状态：🔨 开发中（已通过审核，实现完成，待真机规模验收）｜ 关联 FR：FR-247（地基，被 FR-235/240/241 消费）｜ 免 ADR（常规 REST 分页/搜索，无架构决策）

## 1. 背景与目标

今 `GET /instances` 一次性返回**全量**实例数组（`[]InstanceInfo`，仅多维筛选无分页）。实例页、导航实例选择器、全局搜索、页眉弹层全靠它**前端全量拉 + 客户端过滤/分组**。实例上千时：① 单次响应体过大、② 前端渲染全量卡顿、③ 客户端子串搜索 / 分组聚合不可持续（nav-shell-v2 spec 已显式把 1000+ 服务端检索 defer 给本 FR）。

瓶颈**不在 DB**（千行 SQL 扫描 <10ms），在**前端拉全量 + 渲全量**。故 FR-247 = 提供**服务端分页 + 搜索 + 排序 + 分组聚合**的查询地基，让上层"永远不必一次拿全集"：列表只取当前页，分组/筛选计数由聚合端点给。

**目标**：新增两个只读查询端点（分页搜索 + 维度聚合），复用既有权限作用域与多维筛选；不破坏既有 `GET /instances`；为 FR-235/240/241 提供统一数据源。**本 FR 只做后端契约**，前端消费是 FR-235/240/241。

## 2. 需求（要什么）

### 2.1 `GET /api/v1/instances/search` — 分页搜索列表
- **权限**：`PermInstanceRead`；非平台管理员强制按可访问组作用域（同既有 `List`）。
- **Query 参数**（均可选，AND 组合）：
  - `q`：自由文本，对实例**名称**子串匹配（大小写不敏感）。
  - 既有筛选维度（语义同 FR-047）：`nodeId` `status` `role` `groupId` `networkId` `env` `tag`。
  - `sort`：`name` | `status` | `createdAt` | `nodeId`，默认 `name`。
  - `order`：`asc` | `desc`，默认 `asc`。
  - `page`：1 基，默认 `1`（<1 归 1）。
  - `pageSize`：默认 `50`，上限 `200`（超过截断到 200，<1 归默认）。
- **200 响应**（新信封，区别于 `GET /instances` 的裸数组）：
  ```json
  { "items": [ /* InstanceInfo[]，字段同既有 */ ], "total": 1234, "page": 1, "pageSize": 50 }
  ```
  `total` = 当前筛选下的全量条数（非本页条数）；越界 `page` → `items: []` 且 `total` 仍为真实总数。

### 2.2 `GET /api/v1/instances/aggregate` — 维度计数
- **权限**：同上（含非管理员作用域）。
- **Query 参数**：与 search 相同的筛选维度（`q` + `nodeId/status/role/groupId/networkId/env/tag`），**honor 全部传入的筛选**。
- **200 响应**：
  ```json
  {
    "total": 1234,
    "byStatus": { "STOPPED": 800, "STARTING": 2, "RUNNING": 400, "STOPPING": 1, "CRASHED": 31 },
    "byNode":   [ { "nodeId": 1, "count": 900 }, { "nodeId": 2, "count": 334 } ],
    "byRole":   { "backend": 1000, "proxy": 30, "universal": 204 }
  }
  ```
  约定：UI 渲染"某维度筛选 chip 计数"时，调用方**自行省略该维度的筛选**再请求（如要"全状态计数"则不传 `status`），后端只忠实地对传入筛选集做分组计数——保持后端单一、把"分面要不要排除自身"交给调用方。

### 2.3 兼容性
- `GET /instances`（裸数组全量）**保持不变**；既有消费方（mock、未迁移页面、测试）不受影响。新端点纯增量。

## 3. 设计（怎么做）

全部在 **Control Plane**（实例数据归 CP DB，无 gRPC/worker 改动，守架构不变量）。

### 3.1 Service 层（`internal/controlplane/service/instance.go`）
- 新增 `InstanceSearchParams`：嵌 `InstanceFilter` + `Query string` `Sort string` `Order string` `Page int` `PageSize int`。
- `SearchInstances(scope []uint, p InstanceSearchParams) (items []model.Instance, total int64, err error)`：
  - `scope == nil` → 平台管理员全量；`scope` 非空 → JOIN `group_instances` 限定可访问组（`len==0` 由 handler 提前返回空，不进此函数）。
  - 复用筛选下推；**env/tag 改在 SQL 侧**精确 LIKE（`tags LIKE '%"env:prod"%'` / `'%"<tag>"%'`，JSON 引号定界，避免分页后再 Go 后置过滤破坏 page/total 一致性）。
  - `q` → `name LIKE '%q%'`。
  - `COUNT(*)` 取 `total` → `Order(<col> <dir>, id asc)`（二级 `id` 保证跨页稳定）`.Limit(pageSize).Offset((page-1)*pageSize)`。
  - sort 白名单映射列名（`name`/`status`/`created_at`/`node_id`），非法值回退 `name`；order 非 `desc` 即 `asc`。**禁止**把原始 sort 串拼进 SQL（防注入）。
  - 遵循 `asset.go` 既有分页范式（`page<1→1`、`Count`→`Order/Limit/Offset`）。
- `AggregateInstances(scope []uint, f InstanceFilter, q string) (InstanceAggregate, error)`：
  - 同作用域 + 同筛选（含 q、env/tag SQL 侧）构造基查询。
  - `byStatus`：`SELECT status, COUNT(*) GROUP BY status`；`byRole`：`GROUP BY role`；`byNode`：`GROUP BY node_id`；`total`：`COUNT(*)`。
  - 返回结构体 `InstanceAggregate{ Total int64; ByStatus map[string]int64; ByNode []NodeCount; ByRole map[string]int64 }`，零计数项以 0 补全（byStatus/byRole 五态/三角色固定键）。
- 既有 `List/ListByGroups/applyDBFilters/filterByTags` 不动（兼容老端点）。新增 `applySearchFilters`（含 env/tag SQL）供新路径用；或给 `applyDBFilters` 加 `tagsInSQL bool` 开关——取**前者**（不改老行为）。

### 3.2 Router 层（`internal/controlplane/router/instance.go`）
- `GET /instances/search` → `(h *InstanceHandler) Search`：鉴权 → 解析 query（含 page/pageSize/sort/order 边界）→ 组装 `InstanceSearchParams` → 取 scope（管理员 nil / 否则 `accessibleGroupIDs`，空则直接返回 `{items:[],total:0,page,pageSize}`）→ `SearchInstances` → 信封 JSON。
- `GET /instances/aggregate` → `(h *InstanceHandler) Aggregate`：同鉴权/scope → `AggregateInstances` → JSON。
- 注册在实例路由组（注意：放在 `/instances/:id` 之前或用静态段，避免 `:id` 吞掉 `search`/`aggregate`）。

### 3.3 数据模型与索引（`internal/controlplane/model/instance.go`）
- `Name`、`Status` 加 `index`（GORM struct tag → AutoMigrate 自动建索引；幂等、不破存量）。`node_id`/`role` 已有索引。为 1000→10k+ 规模的 filter/sort/aggregate 提供支撑（千级非必需但低成本、面向声明的"1000+"）。
- 无新表、无新字段、无 schema 破坏。

### 3.4 不做（范围外）
- 前端消费（列表虚拟化/分组/搜索框接入）= FR-235/240/241。
- 游标分页 / 全文索引引擎（offset 分页对 1000s~10k 足够；过度设计不做）。
- 跨字段模糊（uuid/type/node 名搜索）：v1 仅 name；如需后续扩 `q` 覆盖面，增量加。

## 4. 任务拆分
1. **模型索引**：`Name`/`Status` 加 `index`；确认 AutoMigrate 建索引。
2. **Service**：`InstanceSearchParams` / `InstanceAggregate` / `NodeCount` 类型 + `applySearchFilters`（env/tag SQL）+ `SearchInstances` + `AggregateInstances`。
3. **Router**：`Search` / `Aggregate` 两 handler + 路由注册（避开 `:id` 冲突）。
4. **测试**（先行）：service 与 handler 集成测试（见 §5）。
5. **文档同步**：`docs/API.md` 增两端点；`docs/ARCHITECTURE.md` 实例查询一节补"分页/聚合查询地基"。`CHANGELOG` 未发布段。

## 5. 验收标准
- [ ] **分页**：`pageSize=50` 下 `items` ≤50；`total` = 筛选全量数；`page` 越界 → `items:[]` 且 `total` 不变；`pageSize>200` 截断到 200。
- [ ] **搜索**：`q` 子串匹配名称（大小写不敏感）；与各筛选维度 AND 组合正确。
- [ ] **排序**：`name`/`status`/`createdAt`/`nodeId` × `asc`/`desc` 均生效；同键值跨页顺序稳定（二级 id）；非法 `sort`/`order` 安全回退（无 SQL 注入）。
- [ ] **聚合**：`byStatus`/`byRole` 含全部枚举键（零补 0）、`byNode` 覆盖出现的节点；各维度计数之和 = `total`；honor 传入筛选。
- [ ] **权限**：非平台管理员经 search/aggregate 只见其可访问组实例（与既有 `ListByGroups` 一致）；无可访问组 → 空结果。
- [ ] **兼容**：`GET /instances` 行为/响应不变，既有实例相关测试全绿。
- [ ] **真机（规模）**：seed ≥300（目标演示 1000）实例后，`/instances/search?page=1&pageSize=50` 即时返回首页 50 + 正确 `total`；翻页 `page=2` 不重不漏；`/instances/aggregate` 计数与 seed 分布一致；`q` 过滤生效。响应明显快于全量拉（payload 由全量→单页）。

## 6. 风险 / 待定
- **env/tag 用 SQL LIKE**（替代老的 Go 后置精确过滤）：JSON 引号定界（`"env:prod"`）精度足够；极端构造的标签子串理论上误命中，概率可忽略，写测试覆盖典型用例。若发现误命中再升级为 JSON 函数（SQLite `json_each` / MySQL `JSON_CONTAINS`）。
- **默认排序 `name asc`**：便于浏览；调用方可覆盖。若产品更想"最近创建优先"，改默认 `createdAt desc`（待审确认）。
- **offset 分页**：深翻页（page 数千）有 offset 成本，但实例规模目标 1000s，可接受；不引入游标。
