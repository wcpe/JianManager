# 功能规格：客户端分发迁「运营」域 + 全流程向导重做

> 状态：待审　·　关联 PRD：FR-187（增强 FR-086/087/088 客户端分发线）　·　关联 ADR：无（纯前端重做，后端 API 不变）　·　分支：feature/fr-187-client-dist-redesign

## 1. 背景与目标

客户端分发（FR-086~097，✅@v0.8.0）当前：①入口挂在「系统·平台与维护」（`ConsoleSidebar.tsx:135`）而非「运营」；②`ClientChannelsPage.tsx` 创建频道/建密钥是**内联展开表单**（违反新 `.claude/rules/ui-modals.md`）；③首次使用无引导，运营方不清楚「建频道→密钥→发版→接入」的完整链路。

**目标**：把客户端分发迁入「运营」域，并把创建/使用流程重做成**分步引导式工作台**，降低首次使用门槛、全程模态化。P1。**纯前端重做 + 导航迁移，后端 API 完全不变。**

## 2. 需求（要什么）

### 范围内（设计图见本批 brainstorm 会话 3 张 mockup：引导式工作台 / 模态对照）
- **导航迁移**：`/client-channels` 入口从「系统·平台与维护」迁到「运营」域（`ConsoleSidebar` `NAV_GROUPS` operations.children）。路由路径保留（旧链接可达）。保持页面级 RBAC（后端强制平台管理员）不变。
- **频道列表重做**：
  - 空状态 → 大引导卡「创建第一个分发频道」（说明用途 + 主 CTA）。
  - 「新增频道」→ **内容自适应模态**（取代现内联展开表单），遵循 `ui-modals.md`。
  - 非空 → 频道卡片/表，每项显示当前版本、密钥数、最近发布、活跃机器数。
- **频道工作台（详情重做）**：
  - 顶部常驻**就绪度步骤器**：① 创建频道 → ② 拉取密钥 → ③ 发布版本 → ④ 接入启动器。状态由数据推导（keyCount>0、currentVersion>0 等），完成步骤折叠为 ✓，当前未完成步骤高亮 + 引导文案 + CTA。
  - 下设分段：密钥 / 版本 / 统计 / 接入指引（沿用 `ClientVersionsPanel`/`ClientStatsPanel`/`ClientIntegrationGuide` 的能力，重排承载）。
- **发布版本向导**：把现 `ClientVersionsPanel` 的发布流程重做成**分步向导**（选文件 → 逐文件配置 path/sync/platform → 托管目录/说明 → 预览 → 发布），用模态或抽屉（Sheet）承载、内容自适应（超高内部滚动）。
- **全模态化**：建密钥、轮换、删除频道等一律模态（建密钥现为常驻内联表单 → 改模态）。复用 `scrollable-dialog` 壳 / `DangerConfirm`。
- **复制兜底**：`ClientChannelsPage`（密钥明文）与 `ClientIntegrationGuide`（jm-updater.json / javaagent）的复制点改用共享 `copyToClipboard`（预对齐落 main），修 HTTP 非安全上下文复制失败。
- i18n zh/en（只追加自己的键块）；暗亮主题用 token。

### 不做（范围外）
- 改后端任何端点 / 数据模型 / 鉴权（FR-086/087/088 API 原样复用）。
- 改客户端 updater（client-updater/）与 manifest 协议。
- 新增分发能力（仅重排已有：频道/密钥/版本/统计/接入指引/L7 等）。

## 3. 设计（怎么做）

### 3.1 导航（`ConsoleSidebar.tsx`）
- 从 `system.sections[0].children` 移除 `{ to:'/client-channels', ... }`，加入 `operations.children`（位置自定，建议靠近 players/bots）。图标沿用 `DownloadCloud`。
- `navGroupsForRole` 等保持；若现状对该入口无角色门控则维持（页面后端兜底）。`groupRoutes` 自动跟随。

### 3.2 频道列表与工作台（`ClientChannelsPage.tsx` 重构）
- 拆 `CreateChannelForm`（内联）→ `CreateChannelDialog`（模态，`scrollableDialogContentClass` + `ScrollableDialogBody`）。
- 空状态引导卡组件；非空列表卡片化（复用现 `useClientChannels` 数据 + stats）。
- `ChannelDetail` → `ChannelWorkbench`：顶部 `ReadinessStepper`（纯展示，状态由 detail 推导）+ 分段内容。
- 建密钥内联表单 → `CreateKeyDialog`（模态）。轮换/吊销/删除沿用 `DangerConfirm`。

### 3.3 发布向导（`ClientVersionsPanel` 内发布流程重做）
- 现「上传文件→逐文件配置→发布」重排为分步骤向导（步骤指示 + 上一步/下一步 + 预览页），承载于模态/抽屉。复用现 `useClientVersions`/发布 mutation 与后端 `POST .../versions`，**不改后端**。
- 内容自适应：长文件列表内部滚动，头/脚（步骤导航）固定。

### 3.4 遵循模态纪律
- 本 FR 所有新增/重做的创建/编辑交互严格按 `.claude/rules/ui-modals.md`：禁内联展开、强制内容自适应模态/抽屉、禁固定尺寸溢出。

## 4. 任务拆分
- [ ] 导航迁移：`ConsoleSidebar` 把 client-channels 从 system 迁到 operations + i18n（若 label 键归类调整）
- [ ] 频道列表：空状态引导卡 + 卡片化 + 「新增频道」改 `CreateChannelDialog` 模态
- [ ] 频道工作台：`ReadinessStepper`（状态推导）+ 分段重排
- [ ] 建密钥改 `CreateKeyDialog` 模态；轮换/吊销/删除沿用 DangerConfirm
- [ ] 发布版本向导：分步化 + 模态/抽屉承载（复用现发布 mutation）
- [ ] i18n zh/en 追加；暗亮主题校验
- [ ] doc-sync：PRD FR-187「计划」→「开发中」（只改本行）；ARCHITECTURE 前端导航/页面章节（客户端分发归属运营）；CHANGELOG `[Unreleased]` 末尾追加一行
- [ ] 中文 commit（`feat(web)`/`refactor(web)` 按 git-commit 规范拆 commit：导航迁移 = refactor，向导/模态化 = feat/refactor 视行为）

## 5. 验收标准
- 前端 tsc/eslint/build 绿；后端无改动（API 测试不受影响）。
- 「客户端分发」入口出现在「运营」域；旧路由 `/client-channels` 可达。
- 频道列表空状态显示引导卡；「新增频道」弹**内容自适应模态**（非内联展开）。
- 频道工作台顶部就绪度步骤器随频道状态正确推导（无密钥/未发版时高亮对应步骤）。
- 发布版本走分步向导（模态/抽屉），内容超高内部滚动、不固定尺寸溢出。
- 建密钥/轮换/吊销/删除全部模态化，页面无任何「点击新增→内联展开表单」。
- **【需真机，用户确认】** 浏览器（chrome-devtools 驱动）走通完整流程：建频道（模态）→ 建密钥（模态）→ 发布版本（向导）→ 看接入指引；zh/en 切换 + 暗亮主题均正常。

## 6. 风险 / 待定
- **与 FR-188 的边界**：客户端分发页的模态化归本 FR；FR-188 审计改造**不碰** client-dist 相关文件（`ClientChannelsPage`/`ClientVersionsPanel`/`ClientStatsPanel`/`ClientIntegrationGuide`），避免双改冲突。
- **共享 `ConsoleSidebar.tsx`**：FR-187 改 nav 分组（client-channels 归属）；FR-188 不碰 nav（只碰 Dialog 反模式）→ 同文件但不同区域，rebase 风险低。
- **就绪度状态来源**：activeMachines/今日下载等指标若需后端字段，优先复用现 stats 接口；缺则降级隐藏，不为此改后端（属范围外）。
- **i18n 键冲突**：与并行 FR 同改 zh/en.json → 各自追加独立键块，不改他人行。
