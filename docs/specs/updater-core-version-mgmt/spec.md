# 功能规格：updater-core 版本显式管理（gradle-wrapper 式）

> 状态：待审　·　关联 PRD：FR-193（增强 FR-091）　·　关联 ADR：ADR-045（本 FR 创建，补充 ADR-021）　·　分支：feature/fr-193-updater-core-version-mgmt

## 1. 背景与目标

ADR-021 的两件套（楔子 wedge = 稳定 wrapper、updater-core = 可热更 agent）已是 gradle-wrapper 模式；FR-091 已实现客户端侧 core 自更新 + boot-confirm 看门狗 + N-1 自动回退。但**服务端/运营缺集中控制**：当前 manifest 的 `agent.core`（version + per-platform 制品）由发布向导**手填透传**（FR-088「透传原值、不编辑」），运营无法在面板**集中 pin / 更新 / 回退** core 版本——更新器（core）出问题时只能靠客户端自发 N-1，运营被动。

**目标**：把 **updater-core** 做成 gradle-wrapper 式**集中版本管理**：运营上传 core jar、按频道 pin core 版本、一键更新到新版 / 回退到旧版，manifest 的 `agent.core` 由 pin 驱动。**楔子（wedge）冻结、只一个版本、不纳入管理**。P1。补充 ADR-021。

## 2. 需求（要什么）

### 范围内
- **core 制品注册**：运营上传 updater-core jar（按平台，复用 FR-045 制品库，类型如 `client-core`），登记为带**版本号**的 core 制品（version + 各 platform → 制品 sha256/size）。
- **频道 core pin**：每频道 pin 一个 core 版本（默认指向最新已登记 core）；manifest 生成时 `agent.core` 由该 pin 驱动产出（不再纯手填）。
- **更新 / 回退**：运营把 pin **更新**到更新的 core 版本；**回退**坏 core **不靠降 `agent.core.version`**（客户端 core 只升不降，见 §3.1）——而是**以更高版本号重发旧 core 字节为新版**（沿用 FR-088 内容回滚法），客户端照常 promote「上去」到该新版（内容=旧 core）。
- **管理面 UI**：频道页新增「更新器版本」段/Tab：列 core 版本、当前 pin、上传新 core jar、pin/更新/回退（破坏性走 `DangerConfirm`）。
- **楔子冻结**：wedge 单版本固定，`agent.wedge` 恒定、不做版本管理。
- **客户端消费复用 FR-091**：wedge `CoreSelector`（`client-updater/wedge`）已按 manifest `agent.core` promote / boot-confirm / N-1 回退——**本 FR 优先不改 wedge 代码**（楔子冻结），靠 manifest 内容驱动既有逻辑；**须验证** wedge 接受 pin 指定的版本（含运营回退到旧 core 时 `agent.core.version` 降版，wedge 照常 promote 到它）。
- ADR-045：updater-core 集中版本管理（频道 pin 驱动 manifest agent.core + 运营 pin/更新/回退 + core 制品注册），补充 ADR-021（楔子冻结、core 集中管控）。

### 不做（范围外）
- 楔子版本管理 / 楔子自更新（楔子冻结，ADR-021 决策 2）。
- 改 wedge 注入方式 / agentArgs 协议。
- 改 manifest 文件级 reconcile（FR-090）。

## 3. 设计（怎么做）

### 3.1 ADR-045（本 FR 创建，补充 ADR-021）
决策：core 集中版本管理（频道 pin 驱动 `agent.core`、运营可 pin/更新/回退、core 制品注册复用 FR-045）；**楔子冻结单版本**；**两条版本轴区分**——
- **manifest `version`**：内容版本，单调递增、客户端防降级（ADR-022⑦，不变）；
- **`agent.core.version`**：updater-core 自身版本，**对客户端单调只升不降**（FR-091 / contract §6.3：core 只暂存「更高版本」为 pending，绝不降级——agent 自身的防降级）。运营「回退」坏 core = **以更高 `agent.core.version` 重发旧 core 字节**（同 FR-088 内容回滚 + ADR-022⑦ 防降级），客户端照常 promote 上去、跑到旧内容。故 `agent.core.version` **也单调递增**，pin 始终指向「当前应跑的 core 注册版本」。
决策正文写 ADR，勿在 spec 重复。**ADR 文件名/编号用 `ADR-045`（主控预留，写死）。**

### 3.2 后端（`internal/controlplane`）
- core 制品登记：上传 core jar（per platform）入制品库（FR-045，type=client-core），登记 core 版本 → platform→artifact 映射（新表或复用 asset metadata，落地拍）。
- 频道 pin：`ClientChannel` 加 `pinned_core_version int`（0=用最新已登记 core）；或独立 pin 表（落地拍，倾向频道字段）。
- manifest 生成（`service/client_manifest.go`）：`agent.core` 由频道 pin 的 core 版本 + 其 platform 制品产出（取代发布向导手填透传；保留兼容——无 core 注册时 `agent.core` 省略或沿用旧透传）。
- 端点（平台管理员 + 审计）：上传 core 制品、列 core 版本、设/更新/回退频道 pin。

### 3.3 前端
- 频道页「更新器版本」段：core 版本列表 + 当前 pin 高亮 + 上传 core jar（per platform）+ pin/更新/回退（DangerConfirm）+ 楔子版本只读展示（冻结提示）。i18n + 暗亮。

### 3.4 客户端不改（FR-091 既有逻辑正合需求）
- FR-091 现行（core 只 promote 更高 `agent.core.version`、boot-fail 回退 N-1）**正是本 FR 所需**：「更新」=pin 到更高版本→客户端升；「回退」=重发旧字节为更高版本→客户端仍升（到旧内容）。**无需改 wedge/core**（楔子冻结成立）。
- core 制品 **platform 维度**：manifest schema `agent.core.platforms` 为 per-OS map（`map[string]ManifestAgentArtifact`），但 ADR-021 决策「一份 core jar 三平台通用」——故运营**上传一份 core jar 即可**，后端把它填进各 platform 键（或约定单一通配键，落地核对 wedge 取 platform 的逻辑，避免无谓多平台上传）。

## 4. 任务拆分
- [ ] 写 `docs/adr/045-updater-core-central-version-mgmt.md`（ADR-045，预留号写死，补充 ADR-021）
- [ ] 后端：core 制品登记（FR-045 复用）+ core 版本注册 + 频道 `pinned_core_version` + 迁移
- [ ] 后端：manifest 生成 `agent.core` 由 pin 驱动（兼容无注册时）+ 端点（上传/列表/pin/更新/回退，管理员 + 审计）+ 单测
- [ ] 前端：频道页「更新器版本」段（列表/上传/pin/更新/回退/楔子只读）+ i18n
- [ ] 客户端验证：wedge CoreSelector 按 pin 版本（含回退）行为；如需微调单列上报
- [ ] doc-sync：PRD FR-193「计划」→「开发中」；ARCHITECTURE（agent.core 由 pin 驱动 + ER 频道字段/表）+ ADR-045；API.md（core 版本端点）；`docs/specs/client-distribution/contract.md`（agent.core 来源说明）；CHANGELOG 末尾追加
- [ ] 中文 commit（control-plane / web / 如需 client-updater 拆 commit）

## 5. 验收标准
- 单测：core 版本注册 / pin / 更新 / 回退；manifest `agent.core` 反映 pin；无注册时兼容（不破 FR-087/088 现有发布）。
- 后端编译 + 既有测试绿；前端 tsc/lint/build 绿。
- **【需真机，用户确认】** 运营上传 core jar、pin → manifest `agent.core` 反映；更新 pin → 客户端下次启动 promote 新 core；**回退（以更高版本重发旧 core 字节）→ 客户端 promote「上去」到旧内容**（坏 core 应急、不违反防降级）；楔子始终不变。真机用 wedge/core jar + 真 MC 验证。

## 6. 风险 / 待定
- **版本轴混淆**：manifest version（防降级）vs agent.core.version（pin 驱动可升降）必须在 ADR-045 与代码注释讲清，否则易把 core 回退误判为防降级违规。
- **回退必须重发为更高版本（不可降版）**：客户端 core 只升不降（FR-091 / contract §6.3 已证）；若误把回退做成「降 `agent.core.version`」，已升级客户端不会降、回退无效。务必用「重发旧字节为更高版本」法（同 FR-088）。故无需改 wedge（楔子冻结成立）。
- **core 制品 platform 维度**：schema 为 per-OS map 但 ADR-021 一份 jar 通用——上传一份、后端填各 platform 键；落地核对 wedge 取 platform 逻辑（§3.4）。
- **与 FR-191/192 关系**：FR-193 主后端 + 频道页新段，与 FR-191（发布向导）/FR-192（密钥 tab）低耦合；同碰 ClientChannelsPage 的按新 Tab/段隔离。
