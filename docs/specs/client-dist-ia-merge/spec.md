# 功能规格：客户端分发信息架构合并（分发监控并入「客户端分发运维」页）

> 状态：✅ 已交付@v0.22.0　·　关联 PRD：**FR-430**　·　关联 ADR：**088**　·　类型：feat（前端 IA/路由/权限/i18n，**后端零业务改动**）
> 关联既有：FR-215 / FR-264 / FR-265 / FR-356~359 / FR-425~429；ADR-049 / ADR-055
> 详细设计：[`design.md`](./design.md)；时序图：[`sequence-diagram.mermaid`](./sequence-diagram.mermaid)；类图：[`class-diagram.mermaid`](./class-diagram.mermaid)
>
> **范围澄清**：本规格只做 **三页 → 两页 IA 合并 + 7 Tab 重组 + 旧路由重定向 + i18n + 路由守卫**。  
> **不含**「Tab 再精简 / 处置交互二次重构」等后续 UI 迭代（若立项须另开 FR + spec，勿并入 FR-430）。  
> 导航侧栏「客户端分发」域属于 **FR-431**；分发相关 API 权限节点属于 **FR-432**（见 `docs/specs/nav-ia-role-model/spec.md`）。

## 1. 背景与目标

客户端分发目前分散在三页：`/client-channels`（作者/发布）、`/client-dist-monitor`（观测，归「观测」域）、`/client-dist-security`（防护中心，归「平台管理」域）。存在：① 监控页「日志」与安全页「日志详情」对同一 `client_dist_events.request` 数据两套 UI（真重复）；② 观测侧与研判处置侧分处两入口、两套跨页深链；③ 三条路由缺平台管理员守卫。

目标：**合并为「客户端分发」（`/client-channels`，不变）+「客户端分发运维」（新路由 `/client-dist-ops`，7 Tab）两页**；`request` 日志两套 UI 合一；补齐路由守卫；页面 B 文案全量 i18n；旧两条路由保留为透传 query 的重定向，不破书签。

## 2. 需求（要什么）

### 2.1 范围内

#### A. 目标信息架构

- **页面 A「客户端分发」** `/client-channels`：内容零改动，工作台 Tab 保持 `keys/versions/core/stats/guide`；`stats` 标注「本频道口径」。
- **页面 B「客户端分发运维」** `/client-dist-ops`：7 Tab 及数据源/粒度/URL 参数见 `design.md` §1.2。
  - 总览 `useClientDistSecurityOverview`
  - 统计 `useClientStats` + `useClientDistErrorSummary`（跨频道，可筛单频道）
  - 实时监控 `useClientDistRealtime` + `useClientDistErrorSummary`；`seg=events` 叠 `useClientDistSecurityEvents`
  - 全量日志 双视图（见 D）
  - 机器/客户端 `ObsOverviewSection`（observability + machines）+ `useClientRuntimeOverview`
  - 画像 `useClientDistSecurityProfiles`(+detail)；`seg=ip|player` 叠 `useClientDistIpAnalysis`/`useClientDistPlayerAnalysis`
  - 处置 `useClientDistSecurityActions` + mutate；`seg=groups` 叠 `useClientDistSecurityGroups`

#### B. 路由与兼容

- 新增 `/client-dist-ops`（页面 B）。
- `/client-dist-security`、`/client-dist-monitor` **保留为参数翻译重定向**：同一 `ClientDistRedirect` 组件，**透传全部 query**，按 `normalizeOpsTab(raw, source)` 映射 `tab/seg/type` 后 `<Navigate replace>` 到 `/client-dist-ops`。
- 冻结 query key 追加 `type`、`seg`（`lib/client-dist-query.ts`）。
- 映射表见 `design.md` §1.3；`data-page="client-dist-ops"`。

#### C. 权限

- `/client-channels` 与 `/client-dist-ops` 包 `RequirePlatformAdmin`；两条重定向路由不包。
- 页面 B 内保留查询级 `enabled: isPlatformAdmin` 纵深防御。

#### D. 全量日志合一（去重）

- 唯一入口 = 运维页「全量日志」Tab：`type != request` → `useClientDistSecurityLogs`（6 类型聚合）；`type = request` → `useClientDistEventSearch` + `useClientDistEventDetail`（加厚列 + 脱敏详情）。
- 缺省 `type=request`；两 hook 均保留；删除监控页独立日志入口。

#### E. 导航与面包屑

- 观测域移除「客户端分发监控」；「平台管理 > 内容与分发」的入口改为「客户端分发运维」（`nav.clientDistOps`）→ `/client-dist-ops`。
- `breadcrumb.ts` 新增 `client-dist-ops` 映射（域 `nav.platformManagement` / 页 `nav.clientDistOps`）；旧 `client-dist-security` 映射策略见 `design.md` §2.4。

#### F. i18n 全量

- 新增 `nav.clientDistOps` + 命名空间 `clientDistOps.*`（zh/en 双写），覆盖页面 B **全部**用户可见文案（含存量硬编码，实测约 190 条）。
- 复用 `common.*` 与 `clientDistMonitor.*`/`clientDistObs.*` 既有键，不重复造键；`protectionCenter.*` 命名空间不存在（无可复用/无死键）。

### 2.2 不做（范围外）

- 不改后端 / API / DB / Worker / proto（纯前端）。
- 不删除任何安全或观测数据端点（`/client-dist/security/logs`、`/client-dist/events/search` 等均保留）。
- 不清理既有 `clientDistMonitor.*` / `clientDistObs.*` / `clientChannels.*` 键（保留定义，避免连锁）。
- 不改页面 A 的功能与交互。

## 3. 设计要点（摘要，详见 design.md）

- `ClientDistRedirect`（单组件，prop `source: 'security'|'monitor'`）+ `lib/client-dist-ops-tab.ts` 归一化。
- 页面 B 拆为外壳 `ProtectionCenterPage.tsx` + 4 个区块组件（`ClientDistLogsTab`/`OpsStatisticsTab`/`OpsRealtimeTab`/`OpsClientsTab`）+ 共享件 `ops-shared.tsx`；安全侧 3 区块（总览/画像/处置）同页重组。
- i18n 策略：T02/T03 保持原硬编码中文（结构迁移），T04 分 a/b/c 三子步骤替换为 `t()`。

## 4. 任务拆分（顶层 5 项，T04 含 3 子步骤；详见 design.md §7）

- [x] **T01 基础设施与兼容层**（无依赖）：`lib/client-dist-ops-tab.ts`(+test)、`pages/ClientDistRedirect.tsx`、`lib/client-dist-query.ts`(+test)、`i18n` 外壳键。
- [x] **T02 合并页签区块组件**（依赖 T01）：`ops-shared.tsx`、`ClientDistLogsTab.tsx`(+dom test)、`OpsStatisticsTab.tsx`、`OpsRealtimeTab.tsx`、`OpsClientsTab.tsx`(+dom test)。
- [x] **T03 页面 B 外壳 + 新路由/重定向 + 守卫 + 导航/面包屑**（依赖 T02）：`ProtectionCenterPage.tsx`、`Workspace.tsx`、`nav-config.ts`、`breadcrumb.ts`、`ClientChannelsPage.tsx`。
- [x] **T04 i18n 全量**（依赖 T03，含 3 并行子步骤）：T04a 外壳+总览+全量日志；T04b 统计+实时监控+机器客户端；T04c 画像+处置+toast。
- [x] **T05 旧监控页下线 + 测试迁移 + 文档/ADR**（依赖 T04）。

## 5. 验收标准

1. `/client-dist-ops` 渲染「客户端分发运维」，7 Tab（总览/统计/实时监控/全量日志/机器·客户端/画像/处置），`data-page="client-dist-ops"`。
2. `/client-dist-security`、`/client-dist-monitor` 均**透传 query** 重定向到 `/client-dist-ops`；旧 `tab` 别名（`events/ip/players/groups` 与 `statistics/monitor/logs/clients`）落到对应新 Tab/分档，缺失/非法落 `overview`。
3. 全量日志 `type=all` 展示 6 类（含 `hello/telemetry`）；`type=request` 展示加厚列（玩家名/Core 版本/字节/耗时）并可打开脱敏详情，`X-Client-Key` 仅 `present`、无明文。
4. 事件行「封禁 IP / 改 key 态 / 频道防护」仍经 `DangerConfirm` 确认后才写入（既有行为保留）。
5. `/client-channels` 与 `/client-dist-ops` 非管理员直达 → 重定向 `/`；两条重定向路由不拦截。
6. 导航：观测组无「客户端分发监控」；「平台管理 > 内容与分发」有「客户端分发运维」→ `/client-dist-ops`；面包屑 `/client-dist-ops` = [平台管理, 客户端分发运维]。
7. 切英文后页面 B 无中文兜底外露；`missing-keys.test.ts`（zh/en 集合一致 + 静态键全定义）全绿。
8. `ClientDistMonitoringPage.tsx` 及其 dom test 已删除；旧 8 用例在新页复现（非管理员用例改为重定向断言）。
9. `tsc --noEmit` / `lint` / `vitest run` / `npm run build` 全绿。
10. PRD §4 登记 FR-430；ADR-088 落盘；ARCHITECTURE / CHANGELOG 同步。
11. **真机验收（需用户确认）**：见 design.md §8.7。

## 6. 风险 / 待定

- **深链路径变更**：靠两条重定向兜底；保留期以产品约定为准（见 design.md §8.6-1）。
- **测试改动面大**：非管理员语义由「页内降级提示」变「重定向首页」，须同步改断言。
- **i18n ≈190 键**：需 zh/en 同步、`logTypeLabels` 等常量函数化；拆 3 子步骤并行降低单任务体量。
- **`breadcrumb.ts` 旧映射与旧键死键清理**：本稿保稳不清理，列入 §8.6 待定。
- **后端零改动**：全部端点复用，devmock 已覆盖。
