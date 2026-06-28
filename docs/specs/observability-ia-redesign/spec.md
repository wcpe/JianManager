# 功能规格：观测导航重构（信息架构）

> 状态：开发中　·　关联 PRD：FR-215　·　分支：feat/fr-215-observability-ia

## 1. 背景与目标

当前侧栏五域 IA（总览 / 集群 / 监控 / 运营 / 系统，FR-131 / design §7）里，「监控」域聚了语义不齐的四项：监控总览、告警、日志、任务中心。其中：

- **告警**将在 FR-216 与「站内信（定向消息）」合并为统一「通知中心」（页眉单铃铛 + 独立页 + 侧栏「系统/账户与审计」一个链接），不应再以独立观测子项长期存在。
- **任务中心**是运维操作执行流水（安装 JDK / 备份 / 部署等异步任务），属「系统级维护」心智，而非「观测」心智，错放在监控域。
- 「监控」语义将随 FR-217~221（客户端分发观测、平台级统计、时序剖析增强）扩张到「监控 + 日志 + 统计」三类观测视角，「监控」这个词已不足以概括该域。

本 FR 做**纯信息架构 / 路由 / 侧栏调整，页面内容一律不变**：把「监控」域升级为「观测」域、下设 监控 / 日志 / 统计 三子类；任务中心迁到「系统」；为后续 FR-216/220/221 腾出正确落点。属 P1，是 FR-213~221 观测重构批次的 IA 基座（波 1）。

## 2. 需求（要什么）

### 范围内

1. **「监控」域 → 「观测」域**：一级分组 `monitor` 改为 `observability`，标题 `nav.monitor`(监控) → `nav.observability`(观测)。
2. **观测域下三子类**：
   - **监控**（监控总览）：路由 `/monitor`，挂 `MonitoringPage`（**页面不变**）。
   - **日志**：路由 `/logs`，挂 `LogsPage`（**页面不变**）。
   - **统计**：路由 `/statistics`，本 FR 只放**占位页**（空状态文案「统计页建设中（FR-220）」），实质平台级聚合内容由 **FR-220** 补齐。
3. **任务中心迁「系统」**：从观测域移到「系统 · 平台与维护」小节（`nav.sysPlatform`），路由 `/tasks` 不变（页面不变）。
4. **告警留位（不并入观测三子类）**：见 §3「告警去向」。`/alerts` 路由 + 页面 + 页眉铃铛入口**一律不动**，仅在侧栏从「监控域子项」位置调整，保证页面仍可达。
5. **旧路径兼容**：本 FR 调整侧栏键与新增 `/statistics`，但**复用既有路由路径**（`/monitor`、`/logs`、`/tasks`、`/alerts` 均不改），故无 404 风险；唯一新增是 `/statistics`。为防外部链接习惯，提供 `/monitoring`、`/stats` 等同义旧路径 → 新路径的重定向兼容（见 §3 路由映射表）。
6. **i18n**：zh / en 同步改键（新增 `nav.observability`、`nav.statistics`、统计占位页文案键）。
7. **breadcrumb / pageTitle 对齐**：`monitor`/`logs` 的域归属 `nav.monitor` → `nav.observability`；新增 `statistics`、`tasks` 的映射条目（`tasks` 现缺失，顺带补全到「系统」域）。

### 不做（范围外）

- **不改任何页面内容 / 布局 / 数据逻辑**（MonitoringPage / LogsPage / TasksPage / AlertsPage 均原样）。
- **不做告警与站内信的合并**（FR-216）、**不动页眉铃铛**（FR-216 接手）。
- **不实现统计页实质内容**（FR-220），本 FR 仅占位。
- **不做时序剖析增强**（FR-221）、**不做客户端分发观测**（FR-217/218/219）。
- **不改后端 / API / 数据库**（纯前端 IA）。

## 3. 设计（怎么做）

### 侧栏新结构（`web/src/components/console/ConsoleSidebar.tsx`）

```
总览（/）
集群（实例树/节点切换 + nodes/instances/networks/super/director）
观测  ← 原「监控」改名，key: monitor→observability
  ├─ 监控总览   /monitor      MonitoringPage（不变）
  ├─ 日志       /logs         LogsPage（不变）
  └─ 统计       /statistics   StatisticsPage（占位，FR-220 补齐）
运营（players/bots/client-channels/templates/backups/backup-storages/schedules）
系统
  ├─ 平台与维护（sysPlatform）
  │    ├─ 运行时与制品  /runtime-assets
  │    ├─ 平台存储      /storage
  │    ├─ 任务中心      /tasks    ← 由「观测(原监控)」迁入
  │    └─（平台管理员）数据库 /database、系统更新 /system-update
  └─ 账户与审计（sysAccount）：users/groups/settings/audit/licenses
```

> 任务中心放「平台与维护」而非「账户与审计」，因其是平台运维执行流水（与运行时/存储同属维护类），与账户/审计无关。

### 告警去向（与 FR-216 协调，避免撞车）

- 本 FR **不把告警并入观测三子类**（三子类严格为 监控 / 日志 / 统计）。
- `/alerts` 路由、`AlertsPage`、页眉 `AlertBell`（`ConsoleHeader.tsx`，链接 `/alerts`）**全部保持不变**，页面经页眉铃铛始终可达。
- 侧栏中告警的处置：**保留一个「告警」链接在「观测」域内**（紧随三子类，作为过渡），保证从侧栏也能进入，不让其在侧栏孤立或丢失入口。在子项上以注释标注「FR-216 接手后迁出至 系统/账户与审计 的通知中心」。
- **最终去向由 FR-216 决定**：FR-216 落地时把告警 + 站内信合并为「通知中心」，页眉单铃铛 + 侧栏「系统/账户与审计」一个链接，届时从观测域移除本 FR 留下的过渡「告警」链接。本 FR 不预先实现该迁移，仅留位、留注释，避免与 FR-216 改动重叠冲突。

### 路由映射（旧 → 新 · 重定向兼容）

| 旧路径 | 新路径 | 处理 | 说明 |
|---|---|---|---|
| `/monitor` | `/monitor` | 不变 | 监控总览页路径保持，仅侧栏归属域改名 |
| `/logs` | `/logs` | 不变 | 日志页路径保持 |
| `/tasks` | `/tasks` | 不变 | 任务中心仅侧栏位置迁移，路径保持 |
| `/alerts` | `/alerts` | 不变 | 告警页 + 页眉铃铛保持，FR-216 接手 |
| —（新增） | `/statistics` | 新增 | 统计占位页（FR-220 补内容） |
| `/monitoring` | `/monitor` | **301 重定向** | 同义旧链接习惯兜底（`<Navigate replace>`） |
| `/stats` | `/statistics` | **301 重定向** | 统计同义短链兜底 |
| `/observability` | `/monitor` | **301 重定向** | 误用域名作路径时落到监控总览 |

> 重定向用 React Router 的 `<Route element={<Navigate to=... replace />}>`（SPA 内 client-side 301 等价），保证既有/手输旧链接不 404。新增 `/statistics` 与三条兼容重定向是本 FR 路由表的全部增量。

### 路由表改动（`web/src/components/console/Workspace.tsx`）

- 新增 `const StatisticsPage = lazy(...)` + `<Route path="statistics" element={<StatisticsPage />} />`。
- 新增三条 `<Route path="..." element={<Navigate to="..." replace />} />` 兼容重定向。
- 其余路由（monitor/logs/tasks/alerts...）保持不动。

### 统计占位页（`web/src/pages/StatisticsPage.tsx`，新建）

- 极简空状态组件：标题（`nav.statistics`=统计）+ 建设中提示（i18n `statistics.placeholder` = 「统计页建设中（FR-220）」）。复用既有空状态视觉风格（与 `WorkspaceEmpty` 类似的居中提示），不引入新依赖。

### breadcrumb / pageTitle（`web/src/lib/breadcrumb.ts`、`web/src/lib/pageTitle.ts`）

- `SEGMENT_DOMAIN`：`monitor`/`logs` 的值 `nav.monitor` → `nav.observability`；移除/迁移 `alerts` 仍归 `nav.observability`（过渡期与侧栏一致）；新增 `statistics: nav.observability`、`tasks: nav.system`。
- `SEGMENT_PAGE` / `SEGMENT_TITLE_KEYS`：新增 `statistics: nav.statistics`、`tasks: nav.tasks`（`tasks` 现缺失，补齐）。

### i18n（`web/src/i18n/zh.json`、`en.json`）

- `nav.observability`：观测 / Observability
- `nav.statistics`：统计 / Statistics
- `nav.monitoring`（监控总览 / Monitoring Overview）保持作为监控子项 label。
- `statistics.placeholder`：统计页建设中（FR-220） / Statistics page under construction (FR-220)
- `nav.monitor`（监控 / Monitoring）键**保留**（breadcrumb 历史 + 避免误删它引用），但侧栏一级不再用它作域名。

## 4. 任务拆分

- [x] 写本 IA spec（信息架构映射 + 路由映射 + 告警去向）
- [x] `ConsoleSidebar.tsx`：监控域→观测域（key/labelKey）+ 三子类（监控/日志/统计）+ 告警过渡留位；任务中心迁「系统·平台与维护」
- [x] `Workspace.tsx`：新增 `/statistics` 路由 + 三条兼容重定向（`/monitoring`、`/stats`、`/observability`）
- [x] 新建 `StatisticsPage.tsx`（占位空状态）
- [x] `breadcrumb.ts` / `pageTitle.ts`：域归属改名 + 补 statistics/tasks 映射
- [x] i18n zh/en：新增 `nav.observability`、`nav.statistics`、`statistics.placeholder`
- [x] vitest：侧栏渲染含观测{监控/日志/统计}、任务中心在系统、旧路径重定向、breadcrumb 域归属
- [x] 文档同步：PRD FR-215 状态、ARCHITECTURE 前端导航章节
- [ ] 真机验收（需用户确认）

## 5. 验收标准

- 侧栏出现「观测」一级域，展开含 监控总览 / 日志 / 统计 三子项；不再出现「监控」作为一级域名。
- 「任务中心」出现在「系统 · 平台与维护」小节下，不再在观测域。
- 点击统计子项进入 `/statistics`，显示占位空状态「统计页建设中（FR-220）」。
- 既有路径 `/monitor`、`/logs`、`/tasks`、`/alerts` 全部仍可正常进入对应页（页面内容与改动前一致，无回归、无 404）。
- 旧/同义路径 `/monitoring`、`/stats`、`/observability` 重定向到对应新路径、不 404。
- 告警页仍可经页眉铃铛与侧栏过渡链接到达。
- i18n zh / en 均有新键，切换语言侧栏文案正确（观测 / Observability、统计 / Statistics）。
- `npm run build` / `tsc --noEmit` / `lint` / `vitest run` 全绿。
- **真机验收（需用户确认）**：登录后真浏览器点侧栏「观测」三子项 + 「系统」下任务中心，各页正常；旧链接手输不炸。单元/E2E 绿不替代此项。

## 6. 风险 / 待定

- **与 FR-216 的告警归属重叠**：本 FR 在观测域留「告警」过渡链接 + 注释，FR-216 落地时负责迁出到通知中心并移除该过渡项。两者改的文件可能都触及 `ConsoleSidebar.tsx`，需 FR-216 基于本 FR 结果再改（本 FR 先落 main）。
- **与在飞 FR-208/210/211 测试基座并行**：本 FR 改侧栏键 `monitor`→`observability`、新增 `/statistics`、迁 `/tasks` 侧栏位置。`/tasks`、`/alerts`、`/monitor`、`/logs` 路由路径不变，TasksPage/AlertsPage 等组件级 dom 测试（直接渲染页面组件，不经路由）不受影响；E2E `app.spec.ts` 按 heading 名导航（实例/节点/玩家）亦不受影响。**需知会那批：任何按侧栏一级「监控」文案或断言观测域结构的测试，改按「观测」+ 三子类写。**
- **`collapsedGroups` 持久键**：分组 key 由 `monitor` 改为 `observability` 后，用户既有「监控域折叠状态」持久值失效（落到默认展开），无功能影响，可接受。
