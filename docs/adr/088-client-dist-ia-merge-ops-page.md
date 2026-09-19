# ADR-088: 客户端分发信息架构合并 —— 观测页并入独立「客户端分发运维」页

- **日期**: 2026-09-10
- **状态**: accepted
- **关联**: FR-430（本决策登记）、FR-215、FR-264、FR-265、FR-356~359、FR-425~429、ADR-049、ADR-055

## 上下文

客户端分发能力在前端呈三页分散：

- **客户端分发（频道工作台）** `/client-channels`：作者/发布侧，按键位粒度管理频道、拉取密钥、版本、Core 与单频道统计。
- **客户端分发监控** `/client-dist-monitor`：观测侧，四 Tab（统计 / 监控 / 日志 / 客户端），归「观测」域。
- **客户端分发安全（防护中心）** `/client-dist-security`：研判处置侧，八 Tab（总览 / 异常请求 / 日志详情 / 客户端画像 / IP 剖析 / 封禁与降级 / 玩家名剖析 / 安全分组），归「平台管理 > 内容与分发」。

暴露出三类问题：

1. **真重复**：监控页「日志」Tab（`useClientDistEventSearch` → `/client-dist/events/search`）与安全页「日志详情」Tab（`useClientDistSecurityLogs` → `/client-dist/security/logs` 的 `request` 类）底层同读 `model.ClientDistEvent`（`internal/controlplane/service/client_dist_security.go` 的 `securityRequestLogs`），同一批数据两套 UI。
2. **语义交织但页各为政**：安全页 8 Tab 与监控页 4 Tab 中，请求侧统计（监控 `statistics`）、更新侧运行态（监控 `clients`）、安全研判（安全 `overview/events/profiles/...`）应在一个运维视角内协同，却分布在两个入口、两套跨页深链。
3. **权限缺口**：`/client-channels`、`/client-dist-security`、`/client-dist-monitor` 三条路由均未包平台管理员路由守卫（`RequirePlatformAdmin`），仅靠侧栏按角色隐藏入口 + 后端 RBAC 兜底，URL 直达仍可进页。

同时既有跨页深链体系（FR-359）已冻结 query 键 `channelId/ip/machineId/errCode/version/tab/from/to`，并被三页与多处测试依赖，任何路径变更必须保真兼容。

## 决策

1. **合并为两页**：作者/发布侧「客户端分发」`/client-channels` 保持不变；观测侧与安全研判侧合并为一页「客户端分发运维」，**正式路由取 `/client-dist-ops`**。
2. **运维页 7 Tab**：总览 · 统计 · 实时监控 · 全量日志 · 机器/客户端 · 画像 · 处置。安全页存量 `events→实时监控(seg=events)`、`ip/players→画像(seg)`、`groups→处置(seg=groups)`、`overview/profiles/actions` 原地保留（功能零丢失，仅入口重组）。
3. **旧路由保留为参数翻译重定向**：`/client-dist-security` 与 `/client-dist-monitor` 均保留路由，通过同一 `ClientDistRedirect` 组件**透传全部 query** 并按归一化规则映射 `tab/seg/type` 后 `<Navigate replace>` 到 `/client-dist-ops`。不 404、不丢筛选。
4. **全量日志双视图合一**：唯一入口 = 运维页「全量日志」Tab。`type != request` 用安全聚合端点（覆盖 6 类事件，为「全量」基线）；`type = request` 用请求明细端点的加厚列 + 脱敏详情（对齐 FR-357/FR-265）。两套 hook 均保留，不再各页一份。
5. **补齐路由守卫**：`/client-channels` 与 `/client-dist-ops` 包 `RequirePlatformAdmin`；两条重定向路由不包（仅 Navigate）。
6. **运维页文案全量 i18n**：新增命名空间 `clientDistOps.*` + `nav.clientDistOps`，zh/en 双写；存量安全页硬编码中文一并抽取；复用 `common.*` 与 `clientDistMonitor.*`/`clientDistObs.*` 既有键。

## 被修订或被取代的既有决定

- **ADR-049（分发观测聚合）**：快照表、聚合任务与查询端点**不变**；本决策仅重组前端消费方 IA，不触及聚合语义。
- **ADR-055（资源层级导航）**：观测域移除「客户端分发监控」入口，该页改归「平台管理 > 内容与分发」为「客户端分发运维」，域归属变化。
- **FR-215（观测 IA 重构）**：观测域子项由 {监控总览 / 日志 / 统计 / 客户端分发监控} 收敛为 {监控总览 / 日志 / 统计}。
- **FR-264（防护中心）**：页面改名与 Tab 重排，IP 封禁 / key 状态机 / 频道保护 / 安全画像 / 分组等能力全部保留。
- **FR-265（分发观测四 Tab）**：统计 / 监控 / 日志 / 客户端四类语义保留，作为运维页对应 Tab 的来源；「日志」语义升级为「全量日志」。
- **FR-359（跨页深链）**：query 键集合冻结不变，新增 `type`/`seg` 两键；深链主路径由 `security/monitor` 迁至 `ops`，旧路径由重定向保真。
- **FR-425~429（分发监控统计优化批）**：其组件（时间筛选 / 机器钻取 / 热力图 / 洞察卡 / 布局重排）作为运维页与频道工作台共用资产原样复用。

## 后果

**正面**

- 消除 `request` 数据双 UI，日志排障与全量事件流统一入口。
- Tab 语义清晰：请求侧（统计/实时监控/全量日志）、更新侧（机器/客户端）、研判处置侧（画像/处置）分层明确。
- 补齐平台管理员路由守卫，收紧 URL 直达权限缺口。
- 导航入口从「观测 + 平台管理」两处收敛，运维心智单一入口。

**负面 / 权衡**

- **深链路径变更**：外部书签与既有链接路径改变，靠两条重定向兜底（保留期以产品约定为准）。
- **测试改动面大**：删 1 个测试文件（8 用例）、迁/改 8 个测试文件；非管理员语义由「页内降级」变「重定向首页」。
- **i18n 新增约 190 键**（存量硬编码全量抽取），需 zh/en 同步、`missing-keys` 守门。
- 变更纯属前端，无后端/DB 影响。

## 回滚方式

- 纯前端改动，无数据库迁移与后端接口变更；`git revert` 对应 feature 提交即可恢复三页原状。
- 重定向路由与 `/client-dist-ops` 可独立保留或移除：即便回滚合并，旧路径本身仍有效（回滚后 `security/monitor` 恢复为实体页，需同步移除新增重定向）。

## 替代方案

- **合成单页（13 Tab）**：配置类操作与封禁类操作风险等级混同，Tab 过载，放弃。
- **沿用 `/client-dist-security` 仅改标题**：改动面最小，但路径语义与「运维」页名不符，用户明确选择改名，放弃。
- **不补路由守卫**：维持现状靠后端 RBAC，但 URL 直达可进页，与平台管理页惯例不一致，放弃。
