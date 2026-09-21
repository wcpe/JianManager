# 实例列表树表与拓扑完整网络视图（FR-452 / FR-453）

> 状态：📋 计划　·　关联 PRD：FR-452、FR-453　·　依赖：FR-445（能力画像提供「角色/类型」分组维度）　·　关联 ADR：[ADR-091](../../adr/091-instance-capability-profile.md)

## 1. 背景与目标

**列表页现状**：`apps/control-plane-web/src/pages/InstancesPage.tsx`（1658 行）为**卡片/列表双视图**（`readViewMode`，`:103`；`view` 状态 `:194`）+ 一个独立的「组织分组」树视图（`orgView` → `InstanceGroupManager`，`:834`）。分组维度下拉（`:782-785`）仅 `none` / `node` / `env` / `status` 四档，聚合走 `instance-grouping.ts` 的 `groupInstances`。64 台实例在一屏内**跨节点/跨区无法整体折叠**，运维要逐页翻。

**拓扑页现状**：位于 `/networks/topology`（`NetworksPage.tsx:69-77`，`:127` 渲染 `TopologyGraph`）。拓扑只画**已注册关系**——`GET /topology` 只返回**有注册的 proxy 及其后端**（`apps/control-plane-web/src/api/topology.ts`、`lib/topology.ts:buildTopology`），未注册实例（含 `beacon`、独立服务、已建未挂 BC 的后端）**根本不在图上**；层级由 network 归属推导，节点无负载标签。

实例已有 `region:` / `zone:` / `role:` 维度标签（FR-440/444）：由 Beacon 拉取时写入（`internal/controlplane/service/beacon_sync.go:19-20` 的 `BeaconTagRegionPrefix`/`ZoneTagPrefix`），但**当前仅作为筛选器**（`instance-grouping.ts` 只认 `env:` 前缀），未用于分组。

**目标**：

- **FR-452**：实例列表改**多级可折叠树表**，分组维度**可切换**：`region/zone`（默认）/ 实例分组树 / 网络 / 角色 / 类型 / 节点 / 不分组；无分组信息的实例自然落「未分组」。
- **FR-453**：拓扑改**完整网络视图**——含未注册实例与配套服务；层级从所选分组维度推导（默认 `region/zone`）；节点带健康着色 + 负载标签。

**范围外**：不改注册关系模型（ADR-007）；不引入图库（沿用纯函数布局 + SVG 自绘）。

## 2. 设计

### 2.1 FR-452：树表与可切换分组维度

**分组维度类型扩展**（`apps/control-plane-web/src/components/console/instance-grouping.ts:69`）：

```ts
export type GroupDimension =
  | 'none' | 'node' | 'env' | 'status'          // 现有
  | 'region' | 'zone'                            // 新增（默认 region/zone 两级）
  | 'groupTree' | 'network' | 'role' | 'type'    // 新增
```

- `region`/`zone`：**两级树**——先按 `region:` 标签，再按 `zone:` 标签；缺标签落「未分组」。新增 `regionOf(inst)` / `zoneOf(inst)`（仿 `envOf`，`instance-grouping.ts:32`）。
- `groupTree`：直接复用实例分组树（`InstanceGroupTree.tsx` / `instance-group-tree.ts` 的层级），即现在 `orgView` 那棵树的数据源。
- `network`：按 `Network` 软标签成员归属（多归属落首个，口径同 `lib/topology.ts:groupTopology`）。
- `role` / `type`：读**能力画像**（FR-445）的角色/类型，不再单独维护字面量。
- `none`：平铺（等价现有 `none`）。

**树表形态**：树表 = 分组头行（可折叠，显示分组名 + 成员计数 + 该组聚合健康色带）+ 成员实例行（沿用现有 `InstanceTableHeader`/行渲染 `:1470`）。折叠态存 URL（`?collapsed=region:r1,zone:z2`）。

**未分组归位**：任何维度下关键值为空的实例落固定「未分组」组（排序恒末尾，沿用 `groupInstances` 现有 `.sort` 中空 key 规则）。

**URL 状态**：沿用列表页既有 URL 约定（`instance-list-redesign` 的 `?view=&groupBy=`）：新增 `groupBy=region|zone|groupTree|network|role|type|node|none`。默认 `region`（两级含 zone）。切换维度**即时重排**，不重拉数据（客户端聚合）。

> 说明：现有双视图（卡片 / 列表）+ `orgView` 三态收敛为「**一张树表**」为主视图；卡片视图是否保留作为紧凑模式可另行决定（本 FR 以树表为 `/instances` 默认）。

### 2.2 FR-453：拓扑完整网络视图

**后端扩数据源**：`GET /topology` 现返回 `{proxies, networks}`。新增返回**全部实例**的最小投影（`id/name/type/role/status/nodeId/serverPort/tags`），使未注册实例与配套服务也能上拓扑：

```ts
interface TopologyResponse {
  proxies: TopologyProxy[]         // 既有：含注册
  networks: TopologyNetwork[]      // 既有
  instances: TopologyInstanceBrief[] // 新增：全量实例（含未注册/beacon/独立服务）
}
```

`TopologyInstanceBrief` 复用实例列表已有字段，后端一次 IN 查询组装（避免 N+1，与 `topology-scale` 同口径）。

**前端布局改造**（`apps/control-plane-web/src/lib/topology.ts`）：

- `buildTopology` 增「全量节点」入参：已注册关系仍产 `edges`；未注册实例作为**孤立节点**保留（原图丢弃）。
- 层级从**所选分组维度**推导（对齐全 FR 的 `GroupDimension`）：默认 `region/zone` 两级带；每带内仍 proxy 左列 / backend 右列；无归属实例落「未分组」带。新增 `groupTopologyByDimension(topo, dim, instances)`。
- 节点**健康着色**：复用 `instanceStatusLevel`（现有 `lib/threshold.ts` + `TopologyGraph.tsx:14`）。**负载标签**：节点右下角展示 CPU/内存（探针或节点指标，FR-447/450），无数据时不显示（不装 0）。
- 保持 `topology-scale` 已建的 SVG 视口（pan/zoom/搜索/筛选）不变。

**列表 ↔ 拓扑维度联动**：拓扑的层级维度选择器与列表页 `groupBy` **共用同一枚举**，可各自独立，但默认一致（`region/zone`），减少认知负担。

### 2.3 devmock 同步

`packages/devmock` 的 `/topology` 处理器补 `instances` 全量投影；实例列表 mock 补 `region:` / `zone:` 标签样例，覆盖「未分组」与二进制实例归位。

## 3. 任务拆分

1. `instance-grouping.ts`：`GroupDimension` 扩枚举 + `regionOf`/`zoneOf` + 两级树聚合（`region`→`zone`）；补单测。
2. `InstancesPage.tsx`：分组维度下拉换新枚举；树表渲染（分组头折叠行 + 成员行）；折叠态入 URL；`orgView` 收敛为表内 `groupTree` 维度。
3. 后端：`GET /topology` 增 `instances` 全量投影（IN 查询）。
4. `lib/topology.ts`：`buildTopology` 保留孤立节点 + `groupTopologyByDimension`；补单测（未注册实例上带、层级正确）。
5. `TopologyGraph.tsx`：层级维度选择器 + 节点负载标签（健康着色已有）。
6. devmock：列表 `/topology` mock 同步。

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | FR-452：64 台一屏可管，点击分组头折叠/展开 | 真机过 |
| 2 | FR-452：切换维度（region/zone/groupTree/network/role/type/node/none）即时重排 | DOM 测试 |
| 3 | FR-452：未分组实例（bc/beacon/独立服务）正确落「未分组」组，排在末尾 | DOM 测试 |
| 4 | FR-452：`region` 维先按 region 再按 zone 两级展开 | 单测 |
| 5 | FR-453：**全部实例上拓扑**（不再只 12 台），未注册与配套服务可见 | 真机过 |
| 6 | FR-453：层级随所选维度变化且正确；节点健康着色 + 负载标签可见 | 真机过 |
| 7 | FR-453：布局均衡（沿用视口 pan/zoom/搜索不回归） | 真机过 |

## 5. 风险 / 待定

- **region/zone 标签覆盖率**：`region:`/`zone:` 仅由 Beacon 拉取写入（FR-444），未拉取/非 Beacon 管辖实例无标签 → 全落「未分组」。需确认默认维度在标签稀疏时是否退化为「不分组」，或补齐标签来源。
- **拓扑节点规模**：全量实例上拓扑后节点从 ~12 增至 64+，需确认 `layoutTopology` 的分层带在密集数据下仍均衡（视口已建，布局密度待真机调）。
- **列表页体量**：`InstancesPage.tsx` 现 1658 行且卡片/列表/组织树三态，收敛为树表涉及较大删除，建议拆成首个可提交增量（枚举 + 树表）后再删旧视图。
- **`role`/`type` 维度依赖 FR-445**：未落地前先按实例字段字面值分组，落地后切画像。
- **负载标签数据源**：CPU/内存标签依赖探针或节点指标（FR-447/450），无数据源时不显示而非显示 0。
