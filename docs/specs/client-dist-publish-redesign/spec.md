# 功能规格：客户端分发发布/上传/预览定向重做

> 状态：待审　·　关联 PRD：FR-191（增强 FR-187/088）　·　关联 ADR：无（纯前端 + 现有端点复用）　·　分支：feature/fr-191-client-dist-publish-redesign

## 1. 背景与目标

FR-187 把发布做成分步向导，但真机暴露：① 发布向导（`ClientVersionsPanel` 的 `PublishWizard`）用 `<Dialog onOpenChange={v=>!v&&onClose()}>`，点遮罩/Esc **直接关闭、丢掉草稿列表**（文件已传服务端、但「发哪些 + 各自路径/策略」的编排草稿没了）——用户实测数据丢失。② 配置/审阅/版本详情都是**平铺表格**，文件多了看不清结构。③ 不能批量上传，逐个文件设路径很慢。

**目标**：发布向导**防误关 + 上传即锁定**；支持上传 **zip 压缩包自动按包内目录结构编排**；配置/审阅/详情改 **Minecraft 文件树预览**（内容只读、仅可编排）。P1。**纯前端重做，复用现有上传/发布端点，不改后端、不改 manifest 协议。**

## 2. 需求（要什么）

### 范围内（设计图见本批 brainstorm 的「文件树发布」mockup）
- **发布/上传改独立页面（非模态，2026-06-28 用户定）**：把发布向导从模态框移到**独立路由页**（如 `/client-channels/:id/publish`），根治「点模态框外面就关闭、丢上传草稿」——页面级不存在「点外面关闭」。页内分步编排；离开页（返回/路由切换）有未发布草稿时拦截确认。已上传文件**内容锁定**（本就内容寻址 sha256 不可变），UI 明示锁定态，只能编排或移除、不能改字节。
- **zip 压缩包上传自动编排**：可上传 `.zip`；**客户端 JS 解包**，每个文件 entry 经现有 `usePublishClientFile` 上传得 sha256/md5/size，**path 取自 zip 内相对路径**，自动编排成下方文件树。混合上传（散文件 + zip）累加。
- **Minecraft 文件树预览**：配置（编排）/ 审阅 / 版本详情把 `ManifestFile[]` 按 `path` 目录层级渲染为**树**（mods/ config/ resourcepacks/ …）。内容**只读**；可编排：改文件目标路径（移动节点）、改 `sync`（strict/once/ignore）、改 `platform`、移除文件。
- 抽纯函数 + 测试先行（`lib/client-publish-wizard.ts` 扩展：path→树构建、路径编辑/校验、dirty 判定）。
- i18n zh/en（只追加自己的键块）；暗亮主题用 token；遵循 `.claude/rules/ui-modals.md`。

### 不做（范围外）
- 文件**内容**编辑（仅编排路径/策略）。
- 改后端发布/上传端点或 manifest 协议（复用 `POST /client-channels/:id/files`、`POST .../versions`）。
- agent 段（楔子/core 版本）—— 归 FR-193。
- 超大 zip 服务端解包（本期前端解包；spec §6 记为待定，超大再议）。

## 3. 设计（怎么做）

### 3.1 发布改独立页面（非模态）
- 把现 `ClientVersionsPanel` 的模态 `PublishWizard` 移到**独立路由页**（`/client-channels/:id/publish`），在 `Workspace`/App 路由注册；版本 tab 的「发布新版本」按钮改为**导航到该页**（非开模态）。页内承载分步编排（选文件→逐文件配置→托管/说明→预览→发布），**不再是模态、不会因点外面关闭**。
- **离开守卫**：页内有未发布草稿时，返回/路由切换/`beforeunload` 拦截二次确认「放弃发布草稿？」。发布成功/取消回频道工作台版本 tab。
- 草稿态可选 sessionStorage 暂存防刷新丢失（增强，spec 内拍）。
- **务必实测上传可用**（用户反馈「上传不了文件」——确认散文件 + zip 在新页面真能传成功，不只是 UI）。

### 3.2 zip 上传（前端解包）
- 引入轻量解包库（`fflate` 优先，体积小、纯 JS）；选 `.zip` → 解包遍历 entries（跳过目录项）→ 每个 entry：`usePublishClientFile` 上传（codec=none）→ 得 sha256/md5/size → 组 `ManifestFile{ path=entry 相对路径（POSIX 化）, sha256, md5, size, sync='strict' 默认, platform='', artifact }`。
- 进度反馈（多文件上传）；失败的 entry 标出可重试。

### 3.3 文件树（`ManifestFile[]` ↔ 树）
- `buildFileTree(files: ManifestFile[])` 纯函数：按 `path` 的 `/` 分段构目录树（叶=文件、枝=目录）。
- `FileTree` 组件递归渲染：目录可折叠，文件行显示 名称 + sync 徽标 + platform + 锁定图标 + 编排操作（改路径/策略/删除，可拖拽移动目录——拖拽为增强，最低要求可用「编辑路径」输入达成编排）。
- 配置步骤可编排；审阅/详情步骤只读展示（同一 `FileTree`，`readonly` 开关）。

### 3.4 复用后端
- `usePublishClientFile`（逐文件上传）、`usePublishClientVersion`（提交 `files/managedDirs/agent/note`）原样复用；编排只改前端构造的 `ManifestFile.path/sync/platform`。

## 4. 任务拆分
- [ ] `lib/client-publish-wizard.ts`：buildFileTree / 路径编辑校验 / dirty 判定 + vitest（红→绿）
- [ ] `PublishWizard` 防误关（preventDefault + 二次确认）+ 上传锁定态 UI
- [ ] zip 前端解包（fflate）+ 逐 entry 上传 + path 自动编排 + 进度/失败重试
- [ ] `FileTree` 组件（编排态 + 只读态），接入 配置/审阅/版本详情
- [ ] web 依赖：`fflate`（或同类轻量 unzip）
- [ ] i18n zh/en 追加；暗亮主题；遵循 ui-modals
- [ ] doc-sync：PRD FR-191「计划」→「开发中」（只改本行）；CHANGELOG `[Unreleased]` 末尾追加
- [ ] 中文 commit（feat/refactor(web) 拆 commit）

## 5. 验收标准
- 前端 tsc/eslint/build + vitest 绿；后端无改动。
- 向导有草稿时点遮罩/Esc **不丢草稿**（二次确认才关）；已上传文件锁定、不可改内容。
- 上传 `.zip` → 按包内目录树自动编排；散文件 + zip 混合累加。
- 配置/审阅/详情以 **MC 文件树**展示；可改路径/sync/platform/删除；预览只读。
- **【需真机，用户确认】** 浏览器走通：传 zip→编排→预览→发布；中途点遮罩不丢草稿；zh/en + 暗亮正常。

## 6. 风险 / 待定
- **大 zip 前端解包内存**：超大整合包前端解包可能吃内存；本期前端，spec 记待定——若实测卡顿，改服务端解包端点（后续 FR）。
- **拖拽编排复杂度**：拖拽移动目录为增强；最低要求用「编辑路径」输入即可编排，先保底再加拖拽。
- **dirty 判定**：草稿态判定要准（已上传或已改路径即 dirty），避免空向导也拦截。
