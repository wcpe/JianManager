# 功能规格：实例控制台玩家/备份定时分区接真（FR-339）

> 状态：草拟　·　关联 PRD：FR-339（FR-269 首版占位归真）　·　分支：待建　·　免 ADR、**无新 API**（纯前端组件接线，后端与 hook 全部既有）

## 1. 背景与目标

实例统一控制台（FR-269）的「玩家」「备份 · 定时」两个页签至今是静态占位：`InstanceConsolePage`（`apps/control-plane-web/src/components/console/InstanceConsolePage.tsx`）的 `TAB_CARD_TYPE`（`:24-31`）不含 `players`/`backup`，页签分发（`:220-242`）落到 `PlaceholderPanel`（`:413-425`），渲染「此分区已归位到服务器控制台 / 第一版原型先完成入口与信息密度…」文案（i18n `serverConsole.placeholderTitle`/`placeholderHint`，`src/i18n/zh.json:3053-3054`、`en.json:3053-3054`）。

而支撑能力**全部已就绪**：

- 后端：player/backup/schedule 的 router+service 全在（`internal/controlplane/router/player.go:297-306` 注册 `/players`、`/bans`、`/instances/:id/whitelist` 等；schedule/backup handler 见 `router/router.go:278-282`）。
- 前端 hook：`useOnlinePlayers/useKickPlayer/useBanPlayer/useUnbanPlayer/useBans/useWhitelist/useWhitelistAction`（`src/api/players.ts`）、`useSchedules(instanceId)`（`src/api/schedules.ts:61-71`）、`useBackups(instanceId)`（`src/api/backups.ts:49-60`）全在。
- 独立页面 `PlayersPage/SchedulesPage/BackupsPage` 的表格/确认/守卫交互可整段复用抽取。
- devmock 已覆盖全部所需端点（players/bans/whitelist 在 `packages/devmock/src/handlers/domains/plugin.ts:545-658`；backups/schedules 在 `domains/backup.ts:155-211/:279+`），DOM 测试可直接跑。

**目标**：两页签接真数据可操作，占位文案全站清零；控制台成为单实例运维的完整入口（玩家治理 + 备份/定时），不再引导用户跳独立页面做单实例操作。

## 2. 需求（要什么）

### 2.1 新组件 `InstancePlayersSegment`（本实例作用域）

落 `apps/control-plane-web/src/components/console/InstancePlayersSegment.tsx`，props `{ instanceId: number }`：

- **在线玩家列表**：`useOnlinePlayers()`（`GET /players` 为全后端聚合、无实例参数，`api/players.ts:68-77`）→ 前端按 `player.instanceId === instanceId` 过滤；本实例探针不可达时展示降级横幅（`OnlinePlayersResult.backends` 中本实例条目 `available=false`，复用 `players.degraded` 文案形态）。
- **踢出 / 封禁**：行内操作 + 原因输入确认弹窗（复用 `PlayersPage` OnlineTab 的 Dialog+原因模式，`src/pages/PlayersPage.tsx:335-360`）；mutation 传 `scope: { instanceId, reason }` 限定单实例作用域（后端 `playerActionRequest.InstanceID` 语义，`router/player.go:31-39`，越权由 `CanAccessInstance` 拒，`:94-101`）。
- **封禁列表**：`useBans()` 全量展示 + 行内 scope 徽章（`network/instance/global`）+ 解封（`DangerConfirm`，复用 BansTab 模式，`PlayersPage.tsx:536-632`）。**不做假实例过滤**：封禁可能以 network/global 作用域影响本实例，隐藏会误导（见 §6）。
- **白名单**：`useWhitelist(instanceId)` + `useWhitelistAction(instanceId)`（实例原生作用域，`api/players.ts:126-145`）——添加表单 + 列表删除 + `available=false`/查询失败的降级与重试（复用 WhitelistTab，`PlayersPage.tsx:634-754`）。
- **不搬迁全局筛选器**：独立页的「子服筛选下拉」「批量勾选跨服踢封」不进本组件（作用域恒为本实例）。

### 2.2 新组件 `InstanceBackupSegment`（本实例作用域）

落 `apps/control-plane-web/src/components/console/InstanceBackupSegment.tsx`，props `{ instanceId: number }`：

- **定时任务列表**：`useSchedules(instanceId)` → 列表（名称/cron 可读文案/动作/上次执行）+ **启停**（`useUpdateSchedule` 只发 `{enabled}`）+ **删除**（`useDeleteSchedule` + `DangerConfirm`）——交互复用 `SchedulesPage` 的 handler 模式（`src/pages/SchedulesPage.tsx:88-110`）与 `describeCron/validateCron/nextRuns`（`src/lib/cron`）。**不含创建/编辑**（表单较重，独立页已有，页签留「去定时任务页创建」链接）。
- **备份列表**：`useBackups(instanceId, { refetchInterval })` + FR-151 进行中轮询（`hasActiveBackup` + 3s，复用 `BackupsPage.tsx:38-57` 模式）→ 列表（名称/模式徽章/状态徽章/大小/存储/增量链 basedOn）。
- **创建**：全量 / 增量两入口（复用 `handleCreate` 形态，`BackupsPage.tsx:82-96`；增量缺基准的 422 透传 toast）；存储选择保留只读下拉（`useBackupStorages` 列表消费，缺省本地）。
- **恢复**：`useRestoreBackup` + 运行态守卫（实例 `STARTING/RUNNING/STOPPING` 时禁用 + 提示，复用 `instanceLive` 判定，`BackupsPage.tsx:63-69`）+ `DangerConfirm`。
- **删除**：`useDeleteBackup` + 被增量依赖计数警告（`countDependents`，复用 `src/pages/backups-view.ts` 既有纯函数）。
- **不含备份仓库全局配置**（backup-storages 的增删改属独立页/FR-338）。

### 2.3 控制台接线与占位清理

- `InstanceConsolePage` 页签分发（`:220-242`）：`tab === 'players'` → `<InstancePlayersSegment instanceId={instance.id} />`、`tab === 'backup'` → `<InstanceBackupSegment instanceId={instance.id} />`（`TAB_CARD_TYPE` 不动——两分区非工作区卡片类型，`src/lib/workspace-card.ts` 的 `CardType` 枚举不扩）。
- 接线后 `TAB_KEYS`（`:33`）中不再有任何页签落入兜底 → **删除 `PlaceholderPanel` 组件**（`:413-425`）及 i18n 键 `serverConsole.placeholderTitle`/`serverConsole.placeholderHint`（zh/en 各删；注意 `statistics` 命名空间另有同名 `placeholderHint`（`zh.json:3233`）**不误删**）。
- keep-alive 协同（FR-295）：两分区随 `<Activity>` 隐藏时 effects 卸载、`useOnlinePlayers` 的 10s 轮询与备份 3s 轮询自动暂停，切回瞬时呈现——无需额外处理，验收断言覆盖。
- loading/error/empty：由复用交互自带（`common.loading`、各域空态文案）+ 既有共享骨架/空态组件；新增 i18n 仅限分区内标题/引导链接等少量键（zh/en 同步）。

### 2.4 不做（范围外）

- 跨实例玩家画像 / 全局玩家检索（独立页职责）。
- 实时事件流（`usePlayerEvents` SSE 面板）——独立页 LiveTab 已有，控制台概览已有探针连接状态；单实例事件面板如有需要另立 FR。
- 备份仓库管理（FR-057/FR-338 域）、定时任务创建/编辑表单、定时任务跨实例视图。
- 后端任何改动（含给 `GET /players`/`GET /bans` 加实例过滤参数——见 §6）。

## 3. 设计（怎么做）

纯前端（`apps/control-plane-web`）。组件从独立页**抽取复用**而非 import 页面：

- 两个新分区组件自包含数据获取（props 仅 `instanceId`），与 `BotSegment`/`MetricsSegment` 等既有控制台分区形态一致（`src/components/console/` 同目录）。
- 表格用共享 `Table` 组件、确认用 `DangerConfirm`/`Dialog`（模态纪律：确认弹窗属危险确认例外；无内联展开表单——添加白名单为单行输入属行内微交互例外）。
- 复用纯函数不复制：`backups-view.ts`（`hasActiveBackup/countDependents/backupStatusKey/backupStatusLevel/formatSizeMb` 等）、`lib/cron`（`describeCron/validateCron/nextRuns`）直接 import；PlayersPage 内联的确认文案组装逻辑抽公用或就地精简重写（分区无批量模式，逻辑显著更短）。
- i18n：优先复用既有 `players.*`/`schedules.*`/`backups.*` 键；分区级新键（如「去定时任务页创建」）加 `serverConsole.` 命名空间。
- 测试：
  - `InstancePlayersSegment.dom.test.tsx`：在线列表按实例过滤渲染、踢人点击弹确认（含原因输入）、确认后 devmock 收到 `scope.instanceId`、白名单表渲染与添加、探针不可达降级横幅。
  - `InstanceBackupSegment.dom.test.tsx`：定时列表渲染与启停切换（PUT `{enabled}`）、备份列表渲染、创建入口触发 POST、运行中实例恢复按钮禁用。
  - `InstanceConsolePage` 既有 4 件 DOM 测试不受影响（grep 无 placeholder 断言）；补一件页签分发断言（players/backup 页签渲染真分区、无占位文案）。

## 4. 任务拆分

- [ ] `InstancePlayersSegment`：在线列表（实例过滤+降级横幅）+ 踢/封确认 + 封禁列表/解封 + 白名单查改
- [ ] `InstanceBackupSegment`：定时列表启停/删 + 备份列表/创建/恢复（守卫）/删除（依赖警告）
- [ ] `InstanceConsolePage` 页签分发接两组件；删 `PlaceholderPanel` 与 `serverConsole.placeholder*` i18n 键（zh/en，避开 statistics 同名键）
- [ ] 新增 i18n 键（zh/en 同步）
- [ ] DOM 测试两件新增 + 控制台分发断言一件
- [ ] 文档同步：PRD 状态、CHANGELOG；ARCHITECTURE 前端章节「控制台分区」若列举页签实现状态则同步；API.md 无变更

## 5. 验收标准

- [ ] **玩家页签接真**：mock 下打开「玩家」页签渲染本实例在线玩家真数据；点「踢出」弹确认（可填原因），确认后请求携带 `scope.instanceId`；白名单表渲染并可添加/删除；封禁列表可见可解封（DOM 测试覆盖）。
- [ ] **备份页签接真**：「备份 · 定时」页签渲染本实例定时任务列表并可启停（断言 PUT body `{enabled}`）与删除；备份列表渲染，创建入口（全量/增量）触发创建；运行中实例恢复入口禁用（DOM 测试覆盖）。
- [ ] **占位清零**：`grep -r "第一版原型"` 与 `serverConsole.placeholder` 全仓零残留；`PlaceholderPanel` 无引用并删除；任何页签不再渲染占位文案。
- [ ] **keep-alive**：页签隐藏后玩家 10s 轮询/备份 3s 轮询暂停、切回不白屏（沿用 FR-295 机制，DOM 测试断言隐藏期无新请求）。
- [ ] **作用域正确**：分区内所有读写均限定本实例（在线列表过滤、kick/ban 带 instanceId、whitelist/schedules/backups 天然按实例）；无全局筛选器泄漏进分区。
- [ ] vitest 全绿（含既有 1300+ 用例不回归）；真机走查两页签各完成一次真实操作闭环（踢人或白名单增删 + 备份创建/定时启停），需用户确认。

## 6. 风险 / 待定

- **在线玩家为前端过滤**：`GET /players` 聚合全部可达后端再前端按 instanceId 过滤，实例多时响应体偏大——本 FR 不改后端；若规模化痛（数百后端），再给 `/players` 加 `instanceId` 查询参数（另立增量，与 FR-340 的批量思路同族）。
- **封禁列表展示全量**：拍板不按实例过滤（network/global 封禁同样作用于本实例，隐藏误导）；备选「默认过滤 scope=instance+global、可切全部」——若真机走查觉得噪声大再收。
- **proxy 实例的玩家页签**：在线玩家归属后端子服，proxy 实例过滤后恒空；白名单对 proxy 也不可用（探针返回 unavailable）。v1 接受空态+降级提示；是否对 proxy 实例改为展示「其网络内全部后端玩家」待产品定。
- **备份创建的存储选择**：保留只读下拉是低成本复用，但控制台分区可能希望更克制（缺省本地、隐藏下拉）；先保留，真机走查按信息密度取舍。
- **定时任务不提供创建**：单实例场景「就地建一条备份定时」是合理诉求，但表单（cron 预设/校验/预览）较重；v1 以链接引导去独立页，若走查反馈强烈再把 `ScheduleFormDialog` 抽公用组件复用进分区。
