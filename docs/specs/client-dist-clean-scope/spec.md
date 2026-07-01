# 功能规格：客户端分发清理范围编辑器（多级目录树 + 清空整个 gameDir）

> 状态：待审　·　关联 PRD：FR-255（增强 FR-191/088）　·　关联 ADR：**待定**（clean-all/排除 若改签名 manifest schema，追加 ADR 扩展 ADR-021/022 契约 §2）　·　分支：feature/ui-docker-scale-2026-06-30（流 B，串行于前端 publish 流之后）

## 1. 背景与目标

`managedDirs`（托管/自动清理目录）当前是发布页 meta 步的**逗号分隔顶层目录文本框**（[ClientPublishPage.tsx meta 步]），语义：仅这些目录内会删「服务器已移除、玩家本地多出」的文件（减量）。真机诉求两点：① 需**多级目录树勾选**、能配深层嵌套目录（如 `config/foo/bar`）；② 需能配**「清空整个 gameDir」**——删除清单未列的一切，但保护玩家数据。

关键现状（已核实）：
- **客户端已支持嵌套 managedDirs**：`PathRules.isUnderManaged` 用前缀匹配（`path.startsWith(dir+"/")`，[PathRules.java:59](../../../client-updater/updater-core/src/main/java/top/wcpe/mc/jm/updater/core/PathRules.java)）——`config/foo/bar` 直接可用。→ **item5（多级树）纯前端**。
- **客户端已有玩家区安全清单**：`PathRules.PLAYER_ZONE`（`saves/ screenshots/ logs/ crash-reports/ options.txt…`）纵深防御**永不删**。→ clean-all 天然有安全底座。
- **无「全目录」哨兵**：`isUnderManaged` 无法表达「整个 gameDir」。→ **item6 需客户端 + 服务端改**。

**目标**：把 managedDirs 编辑器重做为**多级目录树勾选**；新增**「清空整个 gameDir」模式**——删清单未列的一切，但 `PLAYER_ZONE` + **运营自定义追加排除**永不删（用户决策：清空除安全清单 + 可自定义追加排除）；发布前二次确认。P1，需 spec。

## 2. 需求（要什么）

### 范围内
- **多级目录树勾选（item5，前端为主）**：meta 步 managedDirs 从文本框改为**目录树**（由草稿文件 path 派生的目录结构）+ 复选框，勾选哪些目录（含深层嵌套）纳入自动清理。产出仍是 manifest `managedDirs: string[]`（可含嵌套路径串，客户端已支持）。保留手动输入兜底（高级）。
- **清空整个 gameDir（item6，跨端）**：一个「清空整个游戏目录」开关。开启后语义 = 删除 gameDir 内清单未列的**一切**，**除**：① 内置 `PLAYER_ZONE`（永不删，纵深防御）；② **运营自定义追加排除**目录/路径列表。
- **自定义追加排除（item6，跨端）**：运营可填「额外永不清理」的目录（如玩家自装 mod 目录），随签名 manifest 下发、客户端遵守。
- **发布二次确认**：开启 clean-all 时，review/发布步强制 `DangerConfirm`，明示「玩家该频道游戏目录内、清单未列且不在保护区的文件将被删除」。
- i18n zh/en；暗亮；遵循 ui-modals。

### 不做（范围外）
- 改 `PLAYER_ZONE` 内置清单本身（保持现有硬编码纵深防御；自定义排除是叠加，不替换）。
- 玩家侧「清理前预览将删哪些」UI（客户端 reconcile 侧，另议）。
- 与 FR-254（文件树拖拽）合并——各自独立 FR，仅同处 publish 页需串行。

## 3. 设计（怎么做）

### 3.1 协议（签名 manifest，最谨慎处——Go 与 Java canonical 必须逐位对齐）
- manifest 表达 clean-all + 排除，**已定稿方案 A**（用户审定）：复用 `managedDirs`——加哨兵值 `"*"` 表示「整个 gameDir」；新增**可选**字段 `cleanExclude: string[]`（追加排除）。`cleanExclude` 空则省略（`omitempty`），老 manifest canonical 字节不变（向后兼容，schemaVersion 维持 1）。
- **canonical JSON 两侧同步**：Go `manifestToTree`/`SignedManifest`（[client_manifest.go](../../../internal/controlplane/service/client_manifest.go)）+ Java `Manifest.parse`/`signingBytes`（updater-core）**必须同时**加同名字段、同序、同 null/omit 规则——否则验签逐位不一致即全体失败。**新增字段务必附 `ServerManifestCompatTest`/`SignaturesTest` 跨端对照用例**。

### 3.2 客户端（updater-core）
- `PathRules.isUnderManaged`：managedDirs 含 `"*"` → 视为「全目录托管」返回 true（除玩家区，玩家区判定在 reconcile 侧本就先行）。
- 新增 `isExcluded(relPath, cleanExclude)`：命中运营自定义排除前缀 → 永不删（与 `isPlayerZone` 并列）。
- `Reconciler` 删除判定：`isUnderManaged && !isPlayerZone && !isExcluded` 才删。传入 `cleanExclude`（来自 manifest）。
- 测试：`"*"` 全托管下仍不删玩家区 + 不删自定义排除；嵌套 managedDirs 前缀匹配；普通 managedDirs 不回归。

### 3.3 服务端（CP）
- `PublishVersionParams`/`ClientVersion`/manifest 组装：接受并透传 `managedDirs`（可含 `"*"`）+ `cleanExclude`；写入签名 manifest。校验（`validateManifestFiles` 邻域）：`cleanExclude` 路径合法性。
- API：`POST .../versions` 请求体加 `cleanExclude`（`managedDirs` 已有，扩语义容 `"*"`）。

### 3.4 前端
- meta 步 managedDirs 编辑器：目录树勾选组件（复用/扩展 `ClientFileTree` 的目录结构或新建轻量目录树）+「清空整个 gameDir」开关 + 自定义排除输入（目录树勾选或标签输入）。
- 发布 clean-all 时 `DangerConfirm` 二次确认。
- api：`usePublishClientVersion` 传 `cleanExclude` + managedDirs 含 `"*"`。

## 4. 任务拆分
- [ ] 协议定稿（方案 A/B）+ Go/Java canonical 双侧加字段 + 跨端对照测试（红→绿，最先做，锁协议）
- [ ] 客户端 PathRules/Reconciler clean-all 哨兵 + 自定义排除 + 测试
- [ ] CP 发布端点/模型透传 cleanExclude + managedDirs `"*"` + 校验 + 测试
- [ ] 前端 managedDirs 目录树编辑器 + clean-all 开关 + 自定义排除 + 发布二次确认 + i18n + dom 测试
- [ ] （若改 manifest schema 语义）**ADR**（扩展 ADR-021/022 契约 §2 clean 语义）；否则 contract/API/ARCH 文档更新
- [ ] doc-sync：`docs/API.md`（发布端点字段）、`docs/ARCHITECTURE.md`（manifest 字段 + 清理语义）、contract、PRD FR-255 计划→开发中、CHANGELOG
- [ ] 重编内嵌 updater-core.jar（`make embed-client-updater`）使内嵌 jar 含 clean-all 能力
- [ ] 中文 commit（协议/客户端/服务端/前端/文档 拆分）

## 5. 验收标准
- client-updater `./gradlew test`（含跨端 canonical 对照）绿；CP `go build`/`test` 绿；前端 tsc/lint/vitest/build 绿。
- 多级目录树可勾选深层嵌套目录，产出 managedDirs 含嵌套路径；客户端按之清理该深层目录。
- 开「清空整个 gameDir」：客户端删清单未列的一切，**但** `PLAYER_ZONE`（saves/options…）+ 运营自定义排除**永不删**——单测断言。
- 发布 clean-all 有二次确认；manifest 验签 Go/Java 逐位一致（跨端对照测试绿）。
- **【需真机，用户确认】** 真机：配深层目录清理 + 配 clean-all（含自定义排除）→ 发布 → 客户端更新后：目标目录多余文件被删、saves/options + 自定义排除项完好、manifest 验签通过。

## 6. 风险 / 待定
- **canonical 双端对齐（最高风险）**：manifest 加字段必须 Go `manifestToTree` 与 Java `signingBytes` 逐位一致，否则验签全败。务必先加跨端对照测试锁协议、再动上层。字段用 `omitempty` 保老 manifest 字节不变（向后兼容）。
- **clean-all 危险性**：即便有玩家区 + 自定义排除，clean-all 仍会删玩家在托管外自放的非保护文件；靠发布二次确认 + 文案 + 玩家区纵深防御兜底。默认关，显式开启。
- **协议方案 A vs B**：A（`"*"` 哨兵 + cleanExclude）最小侵入，推荐；B 更显式。spec 审时定。
- **内嵌 jar 重编**：同 FR-253，clean-all 需内嵌 updater-core.jar 重编为新版方生效。
- **依赖**：与 FR-253 均改 client-updater，但不同文件（Signatures/wedge vs PathRules/Reconciler）+ 都要重编内嵌 jar（整合时注意只重编一次含两者的 jar）。
