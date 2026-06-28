# 功能规格：updater-core 默认随 CP 内嵌静默驱动（取代「运营管理 core 版本」）

> 状态：待审（**2026-06-28 改写**：用户定「不让运营上传/管理 core，用 CP 默认内嵌更新器」，反转原「运营 pin/更新/回退」设计）　·　关联 PRD：FR-193（增强 FR-091/FR-107）　·　关联 ADR：ADR-045（**改写为 CP 默认**，补充 ADR-021）　·　分支：feature/fr-193-updater-core-cp-default

## 1. 背景与目标

原 FR-193 让运营上传 core jar、按频道 pin/更新/回退 manifest `agent.core`。**用户验收后否决**：不想让运营自己管更新器版本，要**用控制面板自带的默认更新器**（CP 已内嵌 wedge + updater-core，FR-107 供接入指引下载），打包时默认用它，更新器版本随 CP 走、运营完全不操心。

**目标**：删掉运营侧「更新器版本」上传/管理页与端点；manifest 的 `agent.core` 由 **CP 内嵌的默认 updater-core 自动产出**；CP 自更新（FR-081）时默认更新器随之更新，所有频道自动跟进。P1。ADR-045 改写为「CP 默认」。

## 2. 需求（要什么）

### 范围内
- **删除运营侧 core 管理**：移除「更新器版本」Tab + `ClientCoreVersionsPanel` + 上传/列表/pin/更新/回退端点（撤销原 FR-193 已建的运营管理面与 API）。
- **manifest `agent.core` 由 CP 内嵌 updater-core 自动驱动**：CP 把内嵌的默认 updater-core（FR-107 的 `embed/client-updater/updater-core.jar`）作为所有频道 `agent.core` 的来源——自动算其 sha256/size + 版本，填入 manifest `agent.core`（per ADR-021 一份 jar 通用，填各 platform 键）。运营发布版本时**不再手填/上传 agent 段**。
- **随 CP 自更新跟进**：CP 升级（FR-081）后内嵌的 updater-core 即新版，下次 manifest 自动反映新 `agent.core`，客户端按 FR-091 既有逻辑 promote。**不需运营操作**。
- ADR-045 改写：「updater-core 默认随 CP 内嵌、自动驱动 manifest agent.core，运营不管理」；楔子同理（FR-107 内嵌 wedge，agent.wedge 信息性）。
- 撤销时清理原 FR-193 后端（core_version model/service/router）：删除或停用（迁移建的表 AutoMigrate 不删、留着无害；代码删干净）。

### 不做（范围外）
- 运营自定义 core 版本 / 上传 core jar（明确不做）。
- 改 FR-091 客户端消费逻辑（wedge/core 不动；它消费 manifest agent.core 不变）。
- 改楔子注入 / agentArgs 协议。

## 3. 设计（怎么做）

### 3.1 ADR-045（改写，补充 ADR-021）
决策：**updater-core 不由运营管理，默认用 CP 内嵌版本自动驱动 manifest `agent.core`**。理由：运营关心的是分发内容（mods/config），更新器是平台基建——随 CP 走最省心、版本一致、避免运营误配坏 core；CP 自更新即更新默认 updater-core，自然跟进。`agent.core.version` 仍对客户端单调（FR-091）；CP 默认 core 版本随 CP 版本演进、单调递增。原「运营 pin/更新/回退」决策**作废**（本 ADR 记录反转理由）。**ADR 文件名/编号沿用 `ADR-045`**（同一 FR、发版前改写，不另起号；ADR 内注明改写）。

### 3.2 后端（`internal/controlplane`）
- 内嵌默认 updater-core 来源：复用 FR-107 的 `internal/controlplane/embed/client-updater/`（go:embed 的 updater-core.jar / wedge.jar）。
- `agent.core` 自动产出（`service/client_manifest.go` 或版本/发布服务）：读内嵌 updater-core 字节 → sha256/size + **版本号**（来源落地拍：① 构建期 ldflags/版本文件注入 updater-core 版本；② 退化用 CP 版本 `internal/version`；③ 内嵌一个 core 版本元数据文件。选一、写清）→ 填 manifest `agent.core{version, platforms{各 OS: 同一内嵌制品}}`。制品需可经 `/client-artifacts/:sha256` 下发（把内嵌 core 当作内容寻址制品登记/可取）。
- 撤销原 FR-193 后端：删 `model/client_core_version.go`、`service/client_core_version.go`、`router/client_core_version.go` 及其路由布线、频道 `PinnedCoreVersion` 字段的运营写入端点（字段可留但不再由运营改）。
- 兼容：删管理端点后，既有 FR-087/088 发布/manifest 流程不破；`agent.core` 改由内嵌默认驱动（取代原手填透传 + pin）。

### 3.3 前端
- 删 `web/src/pages/ClientChannelsPage.tsx` 工作台的「更新器版本」TabsTrigger + TabsContent；删 `ClientCoreVersionsPanel.tsx` 及其 api hooks。
- 可选：接入指引（`ClientIntegrationGuide`）或频道页加一句只读说明「更新器用平台默认版本 vX，随控制台升级自动更新」（不是管理面，仅告知；落地拍要不要）。

## 4. 任务拆分
- [ ] 改写 `docs/adr/045-*.md`（CP 默认；注明反转原 pin 决策）
- [ ] 后端：`agent.core` 由内嵌 updater-core 自动产出（版本来源择一）+ 内嵌 core 可经制品端点下发
- [ ] 后端：删原 FR-193 core_version model/service/router + 路由布线 + 运营 pin 写入端点 + 单测调整
- [ ] 前端：删「更新器版本」Tab + `ClientCoreVersionsPanel` + 相关 hooks
- [ ] doc-sync：PRD FR-193（已改）、ARCHITECTURE（agent.core 来源=CP 内嵌默认）、API.md（删 core 管理端点）、`specs/client-distribution/contract.md`（agent.core 来源）、ADR-045 改写、CHANGELOG 末尾追加
- [ ] 中文 commit（control-plane / web 拆 commit）

## 5. 验收标准
- 后端 go build/vet/test 绿（删管理端点后既有 manifest/发布测试仍绿）；前端 tsc/lint/build 绿。
- 频道工作台**无「更新器版本」Tab**；运营无任何 core 上传/管理入口。
- 发布版本后，manifest 的 `agent.core` 自动 = CP 内嵌默认 updater-core（version + sha256 制品可下发）。
- **【需真机，用户确认】** 用 CP 内嵌 wedge/updater-core jar + 真 MC：楔子加载 CP 默认 core、按 manifest `agent.core` 正常 reconcile；运营全程不碰 core。CP 升级后默认 core 版本随之更新（manifest 自动反映）。

## 6. 风险 / 待定
- **内嵌 core 版本号来源**：updater-core 自身版本如何让 CP 知道（构建注入 / 退化用 CP 版本 / 元数据文件）——落地择一并写清，确保 `agent.core.version` 单调且与内嵌 jar 对应。
- **内嵌 core 当制品下发**：`/client-artifacts/:sha256` 需能取到内嵌 core 字节（登记为内容寻址制品或特判内嵌）。
- **撤销原 FR-193 代码**：删 model/service/router 要干净（连带路由布线、装配、测试）；迁移建的表留着无害（AutoMigrate 不删表）。
- **与 FR-191/192 关系**：FR-193 删 ClientChannelsPage 的「更新器版本」Tab + 后端；FR-191（发布页）= ClientVersionsPanel + 路由；FR-192 = KeysSegment。同碰 ClientChannelsPage 的不同区（FR-193 删 Tab、FR-192 改 KeysSegment）——建议 FR-192+193 同一 worktree 串行做，避免抢同文件。
