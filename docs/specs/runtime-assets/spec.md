# 功能规格：运行时与制品全局页

> 状态：已交付　·　关联 PRD：FR-082　·　分支：dev

## 1. 背景与目标

JianManager 已有节点级 JDK 管理（FR-033/FR-178）与平台制品库（FR-045），但运营者需要一个平台级入口快速回答：

- 每个节点有哪些 JDK、哪些实例正在引用它们。
- 制品库按类型占用多少空间、冷热/归档状态如何、哪些制品仍有引用。
- 删除 JDK 或制品前能看到占用关系，避免误删。

FR-082 属于 P2 全局可视化能力：不新增存储模型，不跨进程读取 Worker 文件系统，只聚合 Control Plane 现有表。

## 2. 需求（要什么）

- 提供「运行时与制品」全局页，分为 JDK 运行时区与制品库区；入口位于控制台系统域，路由为 `/runtime-assets`。
- JDK 运行时区展示：
  - 节点数、JDK 数、被引用 JDK 数、实例引用边数。
  - 节点 × `vendor-major` 引用矩阵，格内数字代表引用实例数。
  - 每个 JDK 的节点、版本、架构、路径、托管来源与引用实例清单。
  - 引用清单需区分 `direct`（实例绑定具体 `jdk_id`）与 `major`（实例按 `java_major_version` 解析）。
- 制品库区展示：
  - 制品总数、总占用、被引用制品数、冷热/归档分布。
  - 按类型分组展示制品明细，支持类型筛选、仅被引用筛选、名称/版本/sha256/文件名搜索。
  - `client-file` 制品需要显示 metadata 中的客户端路径或编码信息，方便定位 OTA 文件来源。
- 删除操作复用已有端点：
  - 删除 JDK：`DELETE /nodes/:id/jdks/:jid`，保留 FR-033/FR-228 引用保护与托管/外部来源差异。
  - 删除制品：`DELETE /assets/:id`，保留 FR-045 引用保护。
- 范围内：只读聚合端点、前端全局页、mock 假后端、自动化测试、文档同步。
- 不做（范围外）：
  - 不新增实例↔制品引用模型；制品当前只展示现有 `ref_count` 与类型聚合，不臆造实例连接。
  - 不做 Worker 侧文件扫描或跨节点制品缓存浏览；节点本地缓存仍归 FR-178 节点运行时面板。
  - 不改变 JDK / 制品删除端点的业务规则。

## 3. 设计（怎么做）

### 3.1 后端

- 新增 `RuntimeAssetsService` 作为 Control Plane 只读聚合服务。
- 聚合来源：
  - `nodes`：节点名称与在线状态。
  - `node_jdks`：节点 JDK 记录。
  - `instances`：仅查询 `id/uuid/name/status/node_id/jdk_id/java_major_version`，避免加载无关敏感字段。
  - `assets`：制品元数据与冷热/引用计数。
- JDK 引用解析规则：
  - `instances.jdk_id != 0` 且同节点时，绑定到对应 JDK，标记 `binding=direct`。
  - `instances.jdk_id == 0` 且 `java_major_version != 0` 时，在同节点同大版本 JDK 中选 `id` 最大者，标记 `binding=major`。
  - 跨节点不串台，未绑定实例不产生引用边。
- 制品分组：按 `type` 升序分组；组内沿用 `id desc`；统计 `totalSize/referencedCount/hotCount/archivedCount/externalCount`。
- HTTP 端点：`GET /api/v1/runtime-assets/overview`，限平台管理员；普通成员或未登录访问必须返回 401/403，不泄露聚合数据。
- 聚合查询保持 O(表数) 次批量读取：节点、JDK、实例轻量字段、制品各读一次；不得在 JDK/实例循环里追加数据库查询。

### 3.2 前端

- 新增 `RuntimeAssetsPage`，挂载到控制台路由 `/runtime-assets` 与导航。
- 通过 `useRuntimeAssetsOverview` 获取聚合载荷，并将 Go `nil slice` 归一为前端空数组，避免空态白屏。
- 纯展示逻辑放在 `runtime-assets-view.ts`：字节格式化、JDK 矩阵构建、制品筛选、短 sha 展示。
- 删除后通过 TanStack Query 失效 `runtime-assets-overview`，保证列表联动刷新。

### 3.3 Mock 与测试数据

- mock 假后端 `/runtime-assets/overview` 必须镜像后端 JDK 引用解析：seed 至少覆盖一个 `direct` 引用和一个 `major` 引用，便于 DOM 测试看见引用 chip 与汇总数字。
- mock 制品至少覆盖 `core`、`plugin` 与 `client-file` 类型，其中 `client-file` metadata 包含客户端相对路径，用于验证 OTA 制品定位信息展示。
- 删除 JDK / 制品的 mock 写操作必须驱动 overview 重新查询后的集合变化，不能只改当前组件本地状态。

### 3.4 文档与契约

- `docs/API.md` 记录聚合端点响应结构与引用解析语义。
- `docs/ARCHITECTURE.md` 说明该页为 CP 侧只读聚合，不新增 proto/表。
- `CHANGELOG.md` 未发布段记录 FR-082 收口。

## 4. 任务拆分

- [x] 补齐本规格并对齐 PRD/API/ARCHITECTURE 既有描述。
- [x] 后端聚合测试覆盖 direct/major/跨节点/空集/制品统计。
- [x] 路由测试覆盖平台管理员可访问、未登录/普通成员不可访问。
- [x] 前端纯函数测试覆盖矩阵、筛选、字节与 sha 展示。
- [x] DOM 测试覆盖 seed 渲染、JDK direct/major 引用可见、删除联动、错误态、制品筛选、client-file 路径显示。
- [x] mock 假后端补齐 direct/major 引用与 client-file seed，确保页面验收不是空引用假阳性。
- [x] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG。

## 5. 验收标准

- `GET /api/v1/runtime-assets/overview` 在平台管理员权限下返回 JDK 矩阵、JDK 汇总、制品类型分组、制品汇总。
- JDK 引用解析满足：
  - 直接绑定实例进入对应 JDK，`binding=direct`。
  - 大版本绑定解析到同节点同大版本中 `id` 最大的 JDK，`binding=major`。
  - 不跨节点解析；未绑定实例不产生引用。
- 制品统计满足：按类型分组、总占用、被引用数、冷热/归档/外部分布准确。
- 端点仅平台管理员可访问：未登录返回 401，普通成员返回 403。
- 前端页面可展示 seed JDK 与制品，JDK 引用 chip 同时覆盖 `direct` 与 `major`；删除 JDK 后 overview 联动刷新；API 500 时显示错误态。
- 前端制品筛选可按类型、仅被引用、搜索关键字收敛；`client-file` metadata 路径可见。
- 自动化验证：
  - `go test ./internal/controlplane/service -run RuntimeAssets`
  - `go test ./internal/controlplane/router -run RuntimeAssets`
  - `npm --prefix web run test:node -- src/pages/runtime-assets-view.test.ts`
  - `npm --prefix web run test:dom -- src/pages/RuntimeAssetsPage.dom.test.tsx`
  - 交付前至少跑 `npm --prefix web run lint`、`npm --prefix web run build` 与相关 Go 测试。

## 6. 风险 / 待定

- 制品引用关系目前只有 `ref_count`，无法展示实例级占用方；本 FR 明确不补模型，避免虚构连接。
- 若后续 FR 需要实例↔制品级溯源，应另立数据模型 / 迁移 / ADR。
- 真机维度主要是 CP DB 聚合与前端展示，不涉及新 Worker RPC；本期以自动化与 mock DOM 验收为主。
