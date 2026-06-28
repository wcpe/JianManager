# 功能规格：系统更新页服务端缓存 + Markdown 更新日志

> 状态：🔨 开发中（实现完成，待真机验收）　·　关联 PRD：FR-186（增强 FR-182/FR-081）　·　关联 ADR：无（沿用 ADR-036/020 自更新）　·　分支：feature/fr-186-update-page-cache

## 1. 背景与目标

FR-182 自更新体验增强已交付，但实际落地有两个缺口：
1. **进页面什么都看不到**：`SystemUpdatePage.tsx` 未点「检查更新」时整页空白引导（`:96`），检查走 GitHub 在线拉取，慢/失败/限流时看不到任何历史信息。
2. **更新日志不是 markdown**：更新说明用 `<pre>` 纯文本渲染（`:127`），GitHub release body 的 markdown（标题/列表/代码/链接）显示为生肉。FR-182 spec 曾提「轻 markdown」，实际只落了 `<pre>`。

**目标**：CP 服务端缓存上次成功检查结果，进页面**直接回显**＋后台静默刷新；更新说明用真正的 markdown 渲染。P1。

## 2. 需求（要什么）

### 范围内
- **服务端缓存**：CP 持久化「上次成功检查结果」（完整 CheckResult：latestVersion/source/notes/各组件状态/checkedAt）。进系统更新页**直接读缓存渲染**（无需先点检查），并在后台静默触发一次刷新；刷新成功更新缓存与界面，**刷新失败保留旧缓存**并标注「上次检查：<相对时间>」。
- **Markdown 更新日志**：更新说明（release body）用 `react-markdown` + `remark-gfm` 渲染（标题/列表/代码块/链接/表格），替换现 `<pre>`；链接点击走宿主确认（不在 iframe 内直跳）；长内容 `max-h` + 主题化滚动条（FR-176）；暗亮主题适配。
- 「检查更新」按钮 = 显式强制刷新（live + 更新缓存），保留现有手动语义。

### 不做（范围外）
- 自动定时检查/自动升级（沿用 FR-081 手动边界）。
- 多版本历史缓存（只存最近一次成功结果，单条覆盖）。
- 渲染任意富 HTML（markdown 之外不放开；防 XSS 用 react-markdown 默认不渲染裸 HTML）。

## 3. 设计（怎么做）

### 3.1 服务端缓存（`internal/controlplane`）
- 持久化最近一次**成功** CheckResult：新建轻量单行表 `self_update_check_cache`（或复用 kv/settings blob），字段 `result_json TEXT` + `checked_at` + `source`。每次成功 live 检查后 upsert 覆盖。
- 端点调整（与现 `useSelfUpdateCheck` 对齐）：
  - `GET /self-update/check`：**返回缓存**（带 `checkedAt`，不触发 live 调用，毫秒级返回）；缓存空时返回 `cached:false` 让前端触发刷新。
  - `POST /self-update/check/refresh`（或 `GET /self-update/check?refresh=1`）：执行 live 检查（经 FR-174/185 代理）→ 成功则更新缓存并返回新结果；失败返回 error 但**不清缓存**。
  - 落地择一并写清；保持 RBAC（仅平台管理员）+ 不破坏现有响应结构（加 `checkedAt`/`cached` 字段为加性）。

### 3.2 前端（`SystemUpdatePage.tsx` + `api/selfUpdate.ts`）
- 进页面：先 `GET check`（缓存）即时渲染；若 `cached:false` 或缓存过期（如 > N 分钟，可不设硬过期，仅后台刷新），后台静默 `refresh`。
- 顶部展示「上次检查：<相对时间>」+ 刷新中指示；刷新失败 toast 但保留旧数据。
- 更新说明块：抽出 `ReleaseNotes` 组件，用 `react-markdown`+`remark-gfm` 渲染 `result.notes`；自定义 `a` 渲染走 `openLink`/确认；`code`/`pre`/`ul`/`h*` 用主题 token 样式。
- 「检查更新」按钮 → 调 refresh。

### 3.3 依赖
- web 新增 `react-markdown` + `remark-gfm`（package.json）。确认与 React 19/Vite 6 兼容版本；仅前端依赖。

## 4. 任务拆分
- [x] CP：`self_update_check_caches` 持久化（单行表 `model.SelfUpdateCheckCache` + `database.AutoMigrate`）+ 成功检查后 upsert（`persistCheckCache`/`Save`）
- [x] CP：`GET /self-update/check` 返回缓存（`CachedCheck`，加 `checkedAt`/`cached`，不触发 live）+ `POST /self-update/check/refresh` live 检查更新缓存（`RefreshCheck`，失败不清缓存）
- [x] 前端：进页读缓存即时渲染（`useSelfUpdateCheck` 默认 enabled）+ 后台刷新（`useRefreshSelfUpdateCheck` + 进页 `useEffect` 静默触发）+ 「上次检查：<相对时间>」+ 刷新失败保留旧数据（仅 toast）
- [x] 前端：`ReleaseNotes` 用 react-markdown/remark-gfm 渲染，替换 `<pre>`；链接走宿主确认（`isSafeExternalLink`+`window.confirm`）；暗亮主题（全 token）
- [x] web 依赖：`react-markdown@^9` + `remark-gfm@^4`（React 19/Vite 兼容）
- [x] doc-sync：PRD FR-186「计划」→「开发中」（只改本行）；ARCHITECTURE ER（新缓存表）；API.md（check/refresh 端点变更）；CHANGELOG `[Unreleased]` 末尾追加
- [x] 中文 commit（control-plane / web / deps 拆 commit）

## 5. 验收标准
- CP 单测：成功检查写缓存；`GET check` 返回缓存且不触发 live；`refresh` 失败不清缓存。
- 前端 tsc/eslint/build 绿；markdown 渲染 headings/list/code/link 正确；暗亮主题可读。
- **【需真机，用户确认】** 进系统更新页**无需点检查即见**上次 GitHub release 的 markdown 更新日志；点「检查更新」走 live 刷新；断网/限流（UPDATE_RATE_LIMITED）时页面仍显上次缓存 + 标注上次检查时间。

## 6. 风险 / 待定
- **缓存结构跟随 CheckResult 演进**：存 JSON blob 而非拆字段，避免 CheckResult 加字段时迁移；反序列化兼容旧 blob（缺字段降级）。
- **与 FR-185 同碰 self-update 区**：FR-185 改出站 client 取用方式、FR-186 改检查结果缓存——同模块不同关注点，各自加性，rebase 小冲突易解。
- **react-markdown 体积/兼容**：选与 React 19 兼容的版本；GFM 经 remark-gfm；确保不引入裸 HTML 渲染（安全默认）。
