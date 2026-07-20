# 功能规格：接入指引补齐内嵌更新器信息 + 版本面板旁路摘要

> 状态：草拟　·　关联 PRD：FR-352　·　分支：feature/fr-352-guide-embed-info

## 1. 背景与目标

运营在 `client-channels?tab=guide` 接入客户端更新器时，需要一眼看到 **CP 当前内嵌的楔子 / updater-core 是否可用、展示版本与 core 整数版本、体积**，并在频道版本列表 / 详情 / 发布页有**旁路摘要**，避免「下载失败才发现 jar 未内嵌」或版本信息只散落在别处。

增强已交付 FR-107 / FR-260；与 FR-351（发版硬门禁）互补：351 保证正式包有 jar，本 FR 保证管理台把信息展示完整。

本批**不**展示 FR-266 的 commit / buildTime 元信息。

## 2. 需求（要什么）

- **范围内**
  - 扩展（或明确契约）管理端 `GET /client-dist/updater-jars`（或等价）响应，至少稳定提供：
    - `version`：展示用语义版本串（既有 `ClientUpdaterEmbeddedVersion`）
    - `coreVersion`：内嵌 updater-core **整数**版本（`ClientUpdaterEmbeddedCoreVersion` 解析后的展示用字符串或数字，前端按字符串/数字均可，契约写死一种）
    - `wedge` / `core`：`{ available: bool, size: number }`（既有字段保持兼容）
  - `ClientIntegrationGuide`（guide Tab）：
    - 展示上述字段
    - wedge 不可用时：**禁用**楔子下载 + 中文失败说明（不静默空白）
    - 保留既有 jm-updater.json / javaagent 指引能力
  - 频道**版本列表**（`ClientVersionsPanel`）、**版本详情**（若有独立摘要区）、**发布页**（`ClientPublishPage`）：旁路一行摘要，文案形如「内嵌更新器 v{version} · core {coreVersion}」；不复制整页指引、不强制放下载按钮
  - 前端类型 / devmock / DOM 测与契约对齐
- **不做（范围外）**
  - commit / dirty / buildTime 展示
  - 改玩家侧消费端点或 manifest 语义
  - 强制客户端版本包内附带 wedge
  - 运营可配「展示哪些字段」

## 3. 设计（怎么做）

### 3.1 API

- 在 `ClientUpdaterJarsHandler.Info` 响应中**新增** `coreVersion` 字段，值来自 `embed.ClientUpdaterEmbeddedCoreVersion`（字符串原样返回即可，与现有 `version` 风格一致）。
- 保持既有 `version` / `wedge` / `core` 字段与鉴权（平台管理员 JWT）。
- 同步 `docs/API.md` 与 `packages/devmock` 对应 handler。

### 3.2 前端

- `useUpdaterJarsInfo` 类型扩展 `coreVersion`。
- `ClientIntegrationGuide`：信息块展示 version、coreVersion、各 jar available/size；`available=false` 时下载按钮 disabled + toast/文案说明。
- 抽取极小展示组件或内联文案用于：
  - `ClientVersionsPanel` 标题区旁
  - `ClientPublishPage` 页眉/说明区
  - 版本详情 Dialog 内可选一行（若详情已打开且无合适位置，至少保证列表与发布页有）
- i18n 中/英 key 补齐（横切验收）。

### 3.3 模块边界

- 仅 **control-plane router + embed 常量读取** 与 **control-plane-web + devmock**；不改 client-updater Java 运行时。

### 3.4 ADR

- 不新开 ADR（不推翻既有更新器架构决策）。

## 4. 任务拆分

- [ ] 后端：Info 增加 `coreVersion` + 单测断言字段存在
- [ ] API.md + devmock 同步
- [ ] 前端类型与 guide 完整展示 + 缺 jar 失败态
- [ ] 版本面板 / 发布页旁路摘要
- [ ] DOM / vitest 覆盖
- [ ] 文档同步：PRD FR-352 状态、CHANGELOG 末尾追加、必要时 ARCHITECTURE 一句

## 5. 验收标准

1. 平台管理员调用 `GET /client-dist/updater-jars` 响应含 `version`、`coreVersion`、`wedge.available`、`wedge.size`、`core.available`、`core.size`；缺 jar 时 `available=false` 且 `size=0`。
2. 打开 `client-channels/:id?tab=guide`：可见展示版本、core 整数版本、两 jar 可用性与体积；wedge 不可用时无法成功下载（按钮禁用或明确错误）。
3. 频道版本列表与发布页可见「内嵌更新器 v… · core …」摘要（数值与 Info API 一致）。
4. 自动化：相关 Go 单测 + 前端 DOM/vitest 绿。
5. **真机 / 真浏览器（需用户确认）**：已 embed 的 CP 上 guide + 版本 Tab + 发布页目视字段齐全；可选模拟未 embed 看失败态。

## 6. 风险 / 待定

- 与 FR-351 并行时：dev 未 embed 只能验失败态；正式路径验收依赖 351 或本机已有 jar。
- 多 agent 勿改 PRD 非本行；CHANGELOG 只追加。
