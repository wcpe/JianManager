# FR-342 · 搭建失败损毁态 + 一键重建（复用参数）

> 状态：🔨 开发中（v0.18.0）· 类型：feat（增强 FR-319 异步搭建 / FR-034 一键搭建 / ADR-007 代理）
> 盘问结论：任何搭建阶段失败（下载/校验/配置写入/探针部署/Forge 安装器）都进「损毁」态并保留原始搭建参数；「重建」复用已存参数重跑搭建，**零重填**。

## 目标

1. 一键搭建/代理搭建过程中任一阶段失败的实例进入 **DAMAGED（损毁）** 状态（而非静默留 STOPPED），带失败原因。
2. 损毁实例的**原始搭建参数保留**；用户修好网络/环境后点「重建」即复用参数重跑搭建，无需重填。
3. 损毁实例**不可直接启动**（须先重建）；重建成功→STOPPED 可启动，再失败→仍 DAMAGED。

## 设计

### 状态机

- 新增 `InstanceStatusDamaged = "DAMAGED"`。
- DAMAGED 由**搭建/重建任务失败时直接置入**（与 statusReason 同为直写，不走 `transition()` 状态机——搭建失败本就是带外副作用）。
- `validTransitions[DAMAGED]` 留空：DAMAGED 无合法 `→STARTING` 转换，`Start()` 天然被拦；另在 `Start()` 显式守卫返回明确原因「实例已损毁，请先重建」（HTTP 422）。
- 重建成功由任务 work 直写 `STOPPED`＋清 statusReason；重建进行中保持 DAMAGED＋statusReason「重建中…」，在途任务经 `longOpInFlightGate` 拦重复重建/启动。
- 删除对 DAMAGED 实例照常放行（可删损毁实例）。

### 搭建参数持久化（重建的前提）

- 搭建参数（CoreType/MCVersion/Build/JDKID/MemoryMb/JvmArgs/GroupID/OnlineMode）此前**不落库**（provision 时消费后即弃）。
- Instance 新增 `ProvisionSpec string`（`gorm:"type:text"`，JSON）：搭建时存原始请求（server 存 `ProvisionServerRequest`；proxy 存代理搭建请求，带 kind 判别）。GORM AutoMigrate 自动加列。
- 仅经一键搭建/代理搭建创建的实例有 ProvisionSpec；手动/导入实例无（不适用重建）。

### 失败→DAMAGED

- `ProvisionServerAsync` 的 work：`provisionOnWorker` 失败时把实例直写 `status=DAMAGED`＋`status_reason="搭建未完成：<err>"`（原仅写 statusReason 留 STOPPED）。
- 代理搭建 `ProxyService.ProvisionProxyAsync` 同法（阶段二）。

### 重建

- `ProvisionService.RebuildInstance(ctx, instanceID, createdBy)`：校验 `status==DAMAGED` 且 `ProvisionSpec` 非空 → 反序列化请求 → `ResolveBuild` → 起后台任务重跑 `provisionOnWorker` 到**既有实例工作目录**（覆盖残缺 jar/配置）；成功直写 STOPPED＋清原因，失败仍 DAMAGED＋原因「重建未完成…」。
- 端点 `POST /instances/:id/rebuild`（权限 `instance:control`/组作用域，返回 `{taskId}`），任务中心可见进度。

### 前端（阶段三）

- 实例卡片/详情状态徽章新增 DAMAGED（损毁，红/琥珀），显失败原因。
- 损毁实例：启动按钮禁用（tooltip「已损毁，请先重建」）、显「重建」按钮（点击起重建任务、跳任务中心看进度）；重建中禁用重建/启动。

## 阶段（本 FR 分阶段落地）

1. **阶段一（后端·server）**：DAMAGED 状态 + ProvisionSpec 字段 + server 搭建失败→DAMAGED + RebuildInstance + rebuild 端点 + Start 守卫 + 单测 + 文档。
2. **阶段二（后端·proxy）**：代理搭建失败→DAMAGED + proxy 参数入 ProvisionSpec + rebuild 支持 proxy。
3. **阶段三（前端）**：DAMAGED 徽章 + 重建按钮 + 启动禁用。

## 验收

- [ ] 断网/下载核心失败搭建 → 实例显示 DAMAGED（损毁）态（非 STOPPED），带失败原因
- [ ] 修好网络点「重建」→ **零重填**参数，复用原配置重跑搭建 → 成功转 STOPPED 可启动
- [ ] 各搭建阶段失败（下载/校验/配置/探针/forge）均进损毁态；代理搭建同（阶段二）
- [ ] 损毁实例启动被拦（明确原因）；重建中拦重复重建/启动
- [ ] Go build/test 绿、前端 tsc/lint/vitest 绿
- [ ] **真机验**：真机断网造一次搭建失败→重建成功→可启动
