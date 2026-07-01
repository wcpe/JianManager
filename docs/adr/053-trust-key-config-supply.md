# ADR-053: 客户端信任公钥运行期可配（修订 ADR-022 信任根供给）

- **日期**: 2026-07-01
- **状态**: accepted
- **修订**: [ADR-022](022-client-manifest-trust-and-public-endpoint.md) 决策 2/8 的**信任根公钥供给方式**（原「公钥编入 updater-core 编译期内置」补充为「编译期内置 **或** 随整合包 pin 的配置公钥」）。ADR-022 的 8 条核心决策与 `accepted` 状态不变；投毒模型（私钥服务端持有、签名防篡改、key 半公开）不受影响。

## 上下文

ADR-022 决策 2 规定客户端 updater-core **内置公钥**验签 manifest，私钥服务端持有。ADR-052（FR-248）让服务端生产态**自动生成 Ed25519 密钥对**并面板展示公钥——解决私钥供给门槛。但真机暴露**公钥侧无落地路径**：

- updater-core 的 `Signatures.production()` 在**编译期**把公钥固化为源码常量 `KEY_K1`（[Signatures.java:56](../../client-updater/updater-core/src/main/java/top/wcpe/mc/jm/updater/core/Signatures.java)），当前是开发用公钥。
- `jm-updater.json`（楔子同目录配置）无公钥字段、楔子 ctx 也不传。
- FR-248 面板展示的自动生成公钥**无处填入客户端**——运营者要用自动密钥只能改源码重编 `updater-core.jar`，门槛极高。
- CP 内嵌 / 指引下载的 jar 都是 dev 公钥；**生产自动密钥签的 manifest 会被客户端验签拒绝**（fail-static，OTA 断更）。

这使 FR-248 的「配到客户端」闭环断裂——自动生成密钥虽解决私钥侧，但公钥侧仍卡在编译期。

## 决策

1. **信任根公钥供给双轨**（修订 ADR-022 决策 2 供给部分）：
   - **编译期内置**（既有）：`Signatures.production()` 源码常量 `KEY_K1`，保 dev/兼容零配置。
   - **运行期配置**（新增）：updater-core 从 `jm-updater.json` 的 `signPublicKey`（X.509 SPKI DER base64，与服务端 `PublicKeySPKIBase64()` 同格式）+ `signKeyId`（默认 `k1`）读信任公钥。楔子 `WedgeConfig` 解析上述字段、`Wedge` ctx 透传给 `Core.run`；`Core.run` 裁决：ctx 有 `signPublicKey` → `Signatures.fromConfig(keyId, pubB64)` 构造单公钥信任根；无 → 回退 `Signatures.production()`。

2. **无效配置回退内置**（保守）：`signPublicKey` 解析失败（非法 base64 / 非 Ed25519 SPKI DER）→ `fromConfig` 返回 null → `Core.run` 回退 `production()` 并记日志。不因坏配置直接 fail-static 挡启动——验签自然失败走既有 fail-static 放行本地版（日志可查）。

3. **CP 一键生成带公钥的 jm-updater.json**：新增 `GET /client-channels/:id/updater-config`（JWT 平台管理员），复用 FR-248 签名器公钥，返回完整 `jm-updater.json` 字段（`signPublicKey` + `signKeyId` + `channel` + `endpoint`[CP 公网基址按请求推断] + `key`[占位空串]）。前端接入指引加「下载 jm-updater.json」按钮消费该端点。

4. **信任等价论证**：公钥入 `jm-updater.json`（随整合包分发）看似"配置即信任根"弱化，实则**等价**——整合包（含 updater-core.jar 本身）就是信任分发载体，能篡改 json 者亦能换 jar；签名保护的是**后续 OTA 更新通道**（服务端泄露/MITM），非初始整合包（运营直接分发）。私钥仍服务端持有、投毒模型不变（ADR-022 决策 2 核心：防投毒全靠签名，不靠 key/传输层）。

5. **单公钥本期**：`signKeyId` 默认 `k1`，够覆盖 FR-248 单密钥自动生成。多公钥轮换（k2…）留后续——`Signatures` 内部 `trustStore` 已是 `Map<String, byte[]>`，未来扩配置数组即可。

## 理由

- **闭环 FR-248**：自动生成的公钥此前无处填入客户端，等于白生成。配置公钥让运营者免重编即用自动密钥。
- **整合包即信任载体**：基础整包本就由运营方分发（ADR-022 决策 6），公钥随包 pin 与编译期内置信任等价——能换 jar 者已等同整包控制权，OTA 签名防的是后续通道投毒。
- **保守回退**：坏配置退回内置而非 fail-fast，避免运营配错公钥把玩家挡在游戏外；代价是配错时静默走 fail-static（日志可查），可接受。
- **MiniJson 扁平限制**：楔子 `MiniJson` 仅解析扁平对象（零三方依赖、Java 8），故用扁平 `signPublicKey` 单串而非 `trustedKeys` map；多密钥轮换留后续（届时或扩 MiniJson 或换分隔串）。

## 后果

- 客户端 `jm-updater.json` 新增 `signPublicKey` / `signKeyId` 两字段（扁平字符串，向后兼容——缺省回退内置）。
- CP 新增 `GET /client-channels/:id/updater-config` 端点（JWT 平台管理员，复用 FR-248 signer）。
- 前端接入指引（FR-107）jm-updater.json 示例含 `signPublicKey` + 「下载 jm-updater.json」按钮。
- **内嵌 updater-core.jar 需重编**（`make embed-client-updater`）使 CP 内嵌 jar 含「读配置公钥」能力——否则运营下载的内嵌 jar 仍是旧版不读配置。重编留整合阶段统一做（两流并行避 jar 版本冲突）。
- 代码：`Signatures.fromConfig` / `Core.resolveSignatures` / `WedgeConfig` 加字段 / `Wedge.buildContext` 透传 / `client_updater_config.go` handler + 测试。
- 文档：`docs/API.md` 增端点；`docs/ARCHITECTURE.md` 记客户端信任根来源（内置 + 配置）；ADR-022 加「部分被 ADR-053 修订」关系行。

## 替代方案

- **维持编译期内置唯一**（ADR-022 原状）— 让运营者改源码重编 updater-core.jar 才能用自动密钥，门槛极高且 CP 内嵌 jar 仍是 dev 公钥，否决。
- **配置公钥 fail-fast 挡启动** — 坏配置直接挡游戏启动，与楔子 fail-open 精神冲突（绝不因更新挡启动），否决；保守回退内置让验签自然失败走 fail-static 放行本地版。
- **多公钥 trustedKeys map** — 楔子 MiniJson 不支持嵌套对象，需引第三方 JSON 或换分隔串，YAGNI（本期单公钥够），否决留后续。
