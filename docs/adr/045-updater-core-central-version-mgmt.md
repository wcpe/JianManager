# ADR-045: updater-core 默认随 CP 内嵌、自动驱动 manifest agent.core（楔子冻结，运营不管理）

- **日期**: 2026-06-28
- **状态**: accepted
- **补充**: [ADR-021](021-client-distribution-jvm-updater.md)（两件套纯 JVM 方案）；[ADR-022](022-client-manifest-trust-and-public-endpoint.md)（防降级 / 单调 version 并存）
- **改写说明**: 本 ADR 初版（同日）定「运营上传 core jar、按频道 pin/更新/回退 manifest agent.core 的集中版本管理」。**真机验收后用户否决该方向**——不想让运营自己管更新器版本，要用控制面板自带的默认更新器。故本 ADR 在 FR-193 发版前**改写为下述「CP 默认」决策**；原「运营 pin/管理」决策作废。

## 上下文

FR-091 已实现客户端侧 updater-core 自更新（boot-confirm 看门狗 + N-1 回退）。问题是 manifest 的 `agent.core`（updater-core 版本 + 制品）由谁定。初版设计让运营在面板上传 core jar、按频道 pin 版本——但运营关心的是**分发内容**（mods/config/资源包），更新器是**平台基建**，让运营管它徒增复杂、易误配坏 core。FR-107 已把 wedge + updater-core jar **内嵌进 CP**（供接入指引下载打包）。

## 决策

1. **updater-core 不由运营管理，默认用 CP 内嵌版本自动驱动 manifest `agent.core`**：CP 把内嵌的 updater-core（FR-107 的 `embed/client-updater/updater-core.jar`）作为所有频道 `agent.core` 的来源——自动算其 sha256/size + 版本号，填入 manifest（per ADR-021「一份 jar 三平台通用」，填各 platform 键）。运营发布版本时**不再手填/上传/pin agent 段**。
2. **楔子（wedge）同理冻结**：CP 内嵌的 wedge 单版本固定，`agent.wedge` 信息性、不纳入管理（ADR-021 决策 2）。
3. **随 CP 自更新跟进**：CP 升级（FR-081）后内嵌的 updater-core 即新版，下次 manifest 自动反映新 `agent.core.version`，客户端按 FR-091 既有逻辑 promote——**无需运营操作**。`agent.core.version` 对客户端仍单调只升（FR-091 / contract §6.3）；CP 默认 core 版本随 CP 版本演进、单调递增。
4. **撤销运营侧 core 管理**：删除「更新器版本」管理页与上传/列表/pin/更新/回退端点（撤销本 ADR 初版落地的运营管理面与 API）。
5. **无内嵌 jar 时优雅降级**：CP 未内嵌 updater-core（未跑 `make embed-client-updater`）时省略 `agent.core`，不破 FR-087/088 既有发布/manifest 流程。

## 理由

- **职责归位**：更新器是平台基建、不是分发内容；随 CP 走最省心、版本一致、避免运营误配坏 core。
- **复用 FR-107 内嵌**：CP 已内嵌 wedge/updater-core 供打包，自然作为 `agent.core` 默认来源，零新增运营动作。
- **客户端逻辑不动**：FR-091 的 core 自更新/回退（只 promote 更高版本）不变；CP 默认版本随 CP 演进即驱动客户端跟进。

## 后果

- 删 `model/service/router` 的 `client_core_version.*`、`/client-core-versions*` 与 `/client-channels/:id/core-pin`、`/core-rollback` 端点、前端「更新器版本」Tab + `ClientCoreVersionsPanel`。
- `service/client_manifest.go`（或版本/发布服务）改由 CP 内嵌 updater-core 自动产出 `agent.core`；内嵌 core 可经 `/client-artifacts/:sha256` 下发。
- 接入指引（FR-107）只读展示「内嵌更新器版本」，告知运营打包用的哪版（非管理面）。
- **关联 FR**：FR-193（本 ADR）、FR-091（客户端消费不变）、FR-107（CP 内嵌 jar）。

## 替代方案

- **运营上传 + pin/更新/回退 core 版本（本 ADR 初版）** — 运营徒增基建管理负担、易误配坏 core，真机验收后用户否决，作废。
- **每频道可选不同 core 版本** — 无实际诉求（更新器是统一基建），YAGNI，否决。
