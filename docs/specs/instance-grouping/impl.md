# 实施计划 — FR-047 环境/标签多维分组筛选

> 关联 FR: FR-047 | 优先级: P2 | 状态: 🔨 in-progress | 依赖: FR-032

## 现状核验结论（开工前，YAGNI）

核验 `model/instance.go`(Tags)、`model/network.go`(FR-032)、`service/instance.go`、`router/instance.go`、
`web/.../InstancesPage.tsx`、`web/.../console/InstanceTree.tsx` 后判定：

**已覆盖（复用，不重造）：**
- ✅ **群组(Network)维度**：`Network`/`NetworkMember` M:N 软标签（ADR-007/FR-032）、`NetworkService`
  CRUD + 成员 + 批量运维、`/networks` 全套路由、前端 `NetworksPage` 群组分组视图 + 批量启停。
- ✅ **节点维度**：`GET /instances?nodeId=` + 控制台树 `groupInstancesByNode` 按节点分组。
- ✅ **角色/状态/组维度**：`InstanceService.List(nodeID, status, groupID, role)` 已支持。

**真实缺口（本 FR 补齐）：**
1. ❌ **环境维度完全缺失**：无 dev/test/prod 概念。
2. ❌ **`Tags` 字段是死字段**：model 定义了 `Tags string`(JSON) 但全代码库零读写——无 API 可设置、无筛选可用。
3. ❌ **`GET /instances` 不支持按 network / env / tag 筛选**：只有 node/status/group/role。
4. ❌ **实例列表 / 控制台树无组合筛选与按维度分组视图**。

**结论**：群组维度已被 FR-032 覆盖；**环境维度 + 标签维度 + 三者组合筛选 + 按维度分组视图** 是真实缺口。
按指引复用 `Tags`（约定 `env:` 前缀实现环境，避免新增字段/迁移），扩展筛选，补 Tags 可写路径，前端加筛选器 + 分组。

## 任务拆解

### 后端
- [x] `model/instance_tags.go`：纯函数 `ParseTags` / `EnvFromTags` / `MatchTagFilter`（标签解析 + `env:` 约定），单测全覆盖
- [x] `service/instance.go`：`InstanceFilter` 聚合筛选参数（nodeID/status/groupID/role/networkID/env/tag）；`List`/`ListByGroups` 改签名走过滤；network 走 JOIN，env/tag 在 DB 侧 LIKE 预筛 + 应用层精确校验
- [x] `service/instance.go`：`Update` 支持写 `Tags`（去重、规范化）
- [x] `router/instance.go`：`GET /instances` 扩展 `networkId`/`env`/`tag` query；`PUT /instances/:id` 支持 `tags`
- [x] `api/instances.ts`：`InstanceInfo.tags`；`useInstances` 参数扩展；`useUpdateInstance` 写 tags

### 前端
- [x] `components/console/instance-grouping.ts`：纯函数 `parseEnv` / `groupBy`（环境/群组/标签聚合），单测
- [x] `InstancesPage.tsx`：群组/环境/标签组合筛选器 + 分组视图切换 + 环境徽标
- [x] `InstanceTree.tsx`：按维度分组（节点/环境/群组）
- [x] i18n `grouping` 命名空间 zh+en；主题用 CSS 变量（Badge/Button/Select）

### 文档
- [x] `docs/API.md`：`GET /instances` query + `PUT /instances/:id` tags
- [x] `docs/PRD.md`：FR-047 状态（保持 in-progress，待用户验收）

## 约定

- **环境维度复用 Tags**：`env:dev` / `env:test` / `env:prod`，单实例至多一个 env 标签（取首个）。
- **标签规范化**：trim、去空、去重、保序；大小写敏感（与 Network 名一致）。
- **无新增 proto / 无 DB 迁移**：纯加性，向后兼容（既有实例 Tags 为空 → 视为无环境/无标签）。
