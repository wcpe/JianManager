# 功能规格：OTA 客户端信任公钥运行期可配

> 状态：❌ 已废弃（ADR-054 / FR-256 废弃 `signPublicKey` 与 `signKeyId`）　·　关联 PRD：FR-253（已废弃）　·　关联 ADR：ADR-053（历史记录，已被 ADR-054 推翻）　·　分支：feature/ui-docker-scale-2026-06-30（流 A，可 worktree 并行）
>
> **废弃注记（2026-07-03）**：本文保留 FR-253 的历史设计记录，不再代表现行实现；现行 `jm-updater.json` 不再携带签名公钥，客户端分发信任模型见 ADR-054、`docs/ARCHITECTURE.md` 与 `docs/API.md`。

## 1. 背景与目标

FR-248 让服务端在生产态未注入私钥时**自动生成 Ed25519 签名密钥并面板展示公钥**，提示「请将公钥配入客户端 updater-core」。但真机暴露**该步无落地路径**：客户端信任公钥是 `Signatures.production()` 里**编译期写死的常量 `KEY_K1`**（[Signatures.java:56](../../../client-updater/updater-core/src/main/java/top/wcpe/mc/jm/updater/core/Signatures.java)，当前是开发用公钥），`jm-updater.json` 无公钥字段、楔子 ctx 也不传（[Wedge.java:98-103](../../../client-updater/wedge/src/main/java/top/wcpe/mc/jm/updater/wedge/Wedge.java)）。运营者要用自动生成的密钥，只能改源码重编 `updater-core.jar`——门槛极高，且 CP 内嵌/指引下载的 jar 都是 dev 公钥，**生产自动密钥签的 manifest 会被客户端验签拒绝**（fail-static，OTA 断更）。

**目标**：让 updater-core **运行期从 `jm-updater.json` 读信任公钥**（缺省回退内置 dev 公钥，保兼容/开发）；CP **一键生成带本机公钥的 `jm-updater.json`**；接入指引补此步。据此**修订 ADR-022**（信任根可由随整合包分发的配置提供，非只编译期内置）→ **ADR-053**。P1。**打通 FR-248 的「配到客户端」闭环**，让 OTA 生产态真正可用、免重编。

## 2. 需求（要什么）

### 范围内
- **jm-updater.json 加信任公钥字段**（扁平字符串，兼容楔子 `MiniJson` 仅解析扁平对象的限制）：
  - `signPublicKey`：X.509 SPKI DER 的 base64（服务端 `ManifestSigner.PublicKeySPKIBase64()` 同格式）。
  - `signKeyId`（可选，默认 `k1`）：与 manifest `sig.keyId` 对应。
- **楔子透传**：`WedgeConfig` 解析上述字段；`Wedge` 放入 ctx（`signPublicKey`/`signKeyId`）传给 `Core.run`。
- **updater-core 运行期裁决信任根**：`Core.run` 若 ctx 提供 `signPublicKey` → 用之构造 `Signatures`（`signKeyId → 公钥`）；否则回退 `Signatures.production()`（内置 dev 公钥，保 dev/兼容）。验签逻辑（`Signatures.verify`）不变。
- **CP 生成带公钥的 jm-updater.json**：面板（FR-248 签名公钥卡片 / FR-107 接入指引）提供**「下载 jm-updater.json」**——按频道生成，`signPublicKey`（本机签名器公钥）+ `signKeyId` + `channel` + `endpoint`（CP 公网基址）预填；`key`（拉取密钥）留占位由运营粘贴，或选定某密钥后填入。经 JWT 平台管理员。
- **接入指引更新**：`ClientIntegrationGuide` 的 `jm-updater.json` 示例含 `signPublicKey`；加下载按钮；文案说明「此公钥来自面板签名公钥卡片，随整合包分发即建立信任」。
- **ADR-053**：修订 ADR-022（信任根供给：编译期内置 **或** 随整合包 pin 的配置公钥——整合包本就是信任分发载体，配置公钥与内置公钥信任等价；私钥仍服务端持有、防投毒模型不变）。

### 不做（范围外）
- 多公钥轮换（k2…）配置：本期单公钥（`signKeyId` 默认 k1，够覆盖 FR-248 单密钥自动生成）；轮换留后续（`Signatures` 内部 trustStore 已是 map，未来扩配置数组即可）。
- 改验签算法/canonical 规则（Ed25519 + BouncyCastle 不动）。
- 服务端签名/密钥生成（那是 FR-248，已做）。
- updater-core 瘦身（zstd/BC 体积，另议）。

## 3. 设计（怎么做）

### 3.1 客户端（client-updater）
- `wedge/WedgeConfig.java`：`parse` 增读 `signPublicKey`/`signKeyId`（扁平串，`MiniJson` 原生支持）；字段加进 `WedgeConfig`。
- `wedge/Wedge.java`：ctx 增 `signPublicKey`/`signKeyId`（空则不放或放空串）。
- `updater-core/Signatures.java`：`production()` 保留；**导出**一个由「keyId→公钥 base64」构造的入口（现有 `withTrustStore(Map)` 提升可见性或加 `fromConfig(keyId, pubB64)`）。
- `updater-core/Core.java`：`run(ctx)` 里——`signPublicKey` 非空则 `Signatures.fromConfig(signKeyId或k1, signPublicKey)`，否则 `Signatures.production()`；无效公钥（base64/DER 解析失败）**回退 production() 并记日志**（不因坏配置直接 fail-static 挡启动——保守：坏配置退回内置，验签自然失败走 fail-static 放行本地版）。
- 测试：`SignaturesTest` 增「配置公钥验签通过 / 配置无效回退内置 / 无配置用内置」；`Core`/`Wedge` 测试覆盖 ctx 透传。

### 3.2 服务端（CP）
- 复用 FR-248 签名器公钥；新增按频道生成 jm-updater.json 的端点（如 `GET /client-channels/:id/updater-config`，JWT 平台管理员）→ 返回含 `signPublicKey`/`signKeyId`/`channel`/`endpoint` 的 JSON（`key` 占位）。endpoint 取 CP 对外基址（复用既有配置/请求推断）。
- 前端：签名公钥卡片（FR-248）或接入指引加「下载 jm-updater.json」按钮消费该端点。

### 3.3 前端指引
- `ClientIntegrationGuide.tsx`：`jm-updater.json` 示例块加 `signPublicKey` 行 + 下载按钮 + 大白话步骤（「把面板公钥随整合包下发即建立信任，无需改客户端源码」）。i18n zh/en。

## 4. 任务拆分
- [ ] client-updater：WedgeConfig/Wedge ctx 透传 + Signatures 配置入口 + Core 裁决 + 回退；Java 测试（红→绿）
- [ ] CP：jm-updater.json 生成端点（复用 FR-248 signer 公钥）+ 测试
- [ ] 前端：接入指引/签名卡片下载按钮 + 示例含公钥 + i18n + dom 测试
- [ ] **ADR-053**（修订 ADR-022 信任根供给）；ADR-022 加「部分被 ADR-053 修订」关系行
- [ ] doc-sync：`docs/API.md`（新端点）、`docs/ARCHITECTURE.md`（客户端信任根来源：内置 + 配置）、PRD FR-253 计划→开发中、CHANGELOG
- [ ] 中文 commit（feat(client-updater 经 build 脚本/无独立 scope 则 web/control-plane)、docs(adr)、feat(web) 拆分）
- [ ] **重编内嵌 updater-core.jar**（`make embed-client-updater`）使 CP 内嵌 jar 含「读配置公钥」能力——否则运营下载的内嵌 jar 仍是旧版不读配置

## 5. 验收标准
- client-updater `./gradlew test` 绿；CP `go build`/`test` 绿；前端 tsc/lint/vitest/build 绿。
- 配置了 `signPublicKey` 的 jm-updater.json → updater-core 用该公钥验签；公钥与服务端签名私钥匹配则验签通过、不匹配则拒绝（fail-static）。
- 无 `signPublicKey` → 回退内置 dev 公钥（dev 端到端不回归）。
- 面板可下载按频道预填公钥的 jm-updater.json；指引展示此步。
- **【需真机，用户确认】** 生产态 CP（FR-248 自动生成密钥）→ 面板下载 jm-updater.json（含自动公钥）→ 放进整合包 → 客户端实机验签通过、OTA 更新成功；换错公钥则更新被拒（fail-static 放行本地版、不崩）。

## 6. 风险 / 待定
- **信任模型（ADR-053 详述）**：公钥入 `jm-updater.json`（随整合包分发）看似"配置即信任根"弱化，实则**等价**——整合包（含 updater-core.jar 本身）就是信任分发载体，能篡改 json 者亦能换 jar；签名保护的是**后续 OTA 更新通道**（服务端泄露/MITM），非初始整合包（运营直接分发）。私钥仍服务端持有，投毒模型不变。
- **内嵌 jar 需重编**：CP 内嵌的 updater-core.jar 必须重编为「读配置」新版，否则运营下载的仍是旧 jar（不读配置、用内置 dev 公钥）——任务拆分已列 `make embed-client-updater`。
- **坏配置处理**：无效公钥回退内置而非 fail-fast——保守选择，避免运营配错公钥把玩家挡在游戏外；代价是配错时静默走 fail-static（日志可查）。
- **MiniJson 扁平限制**：故用扁平 `signPublicKey` 单串而非 `trustedKeys` map；多密钥轮换留后续（届时或扩 MiniJson 或换分隔串）。
