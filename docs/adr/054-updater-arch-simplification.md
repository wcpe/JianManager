# ADR-054: 客户端更新器架构简化——去验签 + 楔子 gradle-wrapper 模式 + core 归档多版本

- **日期**: 2026-07-02
- **状态**: accepted
- **推翻**: [ADR-022](022-client-manifest-trust-and-public-endpoint.md)（签名信任模型——信任根从「Ed25519 签名」改为「HTTPS + 拉取密钥鉴权」）；[ADR-053](053-trust-key-config-supply.md)（信任公钥运行期可配——验签已去，`signPublicKey`/`signKeyId` 废弃）
- **修订**: [ADR-045](045-updater-core-central-version-mgmt.md)（core 默认随 CP 内嵌单版本——改为归档多版本 + 运营面板可选 + 楔子自动拉取）；[ADR-068](068-client-dist-zstd-patch-from.md)（FR-098 以可选 zstd patch-from 形式恢复窄口径增量发布）
- **废弃 FR**: FR-090（CAS 内容寻址缓存）、FR-091（core 自更新，上移到楔子）、FR-097（`.jmpack` 容器）、FR-253（信任公钥可配）、FR-248（签名密钥自动生成）
- **关联**: FR-256（去验签 + 去 CAS + 去 jmpack）、FR-257（流式下载）、FR-258（楔子 gradle-wrapper）、FR-259（core 版本归档 + 端点）、FR-260（前端 + ADR + doc-sync）

## 上下文

客户端 OTA 更新器此前有四层复杂机制（ADR-022/045/053 体系）：

1. **Ed25519 验签**：manifest 签名 + `.jmpack` 容器签名 + BouncyCastle 14MB 依赖 + 密钥管理（FR-248 自动生成 + FR-253 公钥可配）。
2. **CAS 内容寻址缓存**：下载文件按 sha256 全量落盘缓存，LRU 1.5GB 上限（FR-090）。
3. **core 自更新**：updater-core 自己下载新版自己（FR-091，鸡生蛋问题，整合包必须附带 14MB jar）。
4. **全内存下载**：Reconciler 把每个文件完整读进 `byte[]`，1GB 资源包直接占 1GB 内存。

真机暴露四个问题（详见 `docs/specs/updater-arch-simplification/spec.md` §1）：

- **验签形同虚设**：私钥存在服务器上（自动生成落盘或 env 注入），服务器被攻破时私钥同样泄露，攻击者可伪造签名。验签付出的复杂度（BC 14MB、密钥管理、配置面）换来的安全保障在实际部署下不成立——信任模型实际只靠 HTTPS + 拉取密钥鉴权。ADR-022 决策 2「防投毒全靠签名，不靠 key、不靠传输层」的安全前提（私钥不泄露）在自动生成落盘方案下被架空。
- **CAS 对大文件膨胀**：200MB 资源包改一个 mod → sha256 变 → CAS 存一份全新 200MB → 旧的留着不命中 → 积累到 1.5GB 才 LRU 清理。CAS 是为小文件去重设计的，对整包式分发不适用。
- **core 自更新鸡生蛋**：下载 core 的逻辑在 core 自己身上，首次启动必须有 core 可用，只能整合包附带 14MB jar。
- **全内存下载炸内存**：1GB 资源包走 `byte[]` 全量读进内存再写盘，客户端内存不够直接 OOM。

## 决策

### 1. 信任根从「Ed25519 签名」改为「HTTPS + 拉取密钥鉴权」（推翻 ADR-022）

manifest 去掉 `sig` 段，客户端拉到 manifest 直接用，不再验签。`Signatures.java` 整个类删，BouncyCastle 依赖从 updater-core 移除（jar 从 ~16MB 降到 ~2MB）。防降级机制去掉（不再记录 lastSeenVersion 拒绝低版本；回滚靠服务端改版本号）。

信任模型实际为：
- **拉取密钥鉴权**（保留，ADR-022 决策 1 不变）：防外人乱拉、区分频道、可吊销。
- **HTTPS 传输**：防中间人。
- **sha256 完整性校验**（保留）：防下载损坏，非信任校验。

私钥在服务器上验签形同虚设——服务器被攻破时私钥同样泄露，签名防不住源端投毒。去掉签名后信任模型与实际部署一致，复杂度大幅下降。

### 2. signPublicKey / signKeyId 废弃（推翻 ADR-053）

`jm-updater.json` 的 `signPublicKey`/`signKeyId` 字段废弃。验签已去掉，无需配置信任公钥。`GET /client-dist/sign-key` 端点删除（FR-248 面板公钥展示随验签一起作废）。`GET /client-channels/:id/updater-config` 端点保留但不再返回 `signPublicKey`；后续修复进一步移除 `coreEndpoint` 配置字段，只返回 API 根 `endpoint`，楔子由 `endpoint + channel` 自动拼接 updater-core 端点（见决策 3）。

### 3. core 改归档多版本 + 运营面板可选 + 楔子自动拉取（修订 ADR-045）

ADR-045 决策 1「core 默认随 CP 内嵌单版本、自动驱动 manifest `agent.core`」修订为：

- **CP 每次 `make embed-client-updater` 时新 core jar 入库归档**（不覆盖旧版，制品类型 `client-updater-core`，版本号递增）。
- **运营面板可选**：频道详情「Core 版本」Tab 列出所有归档版本，一键切换频道选定版本。
- **楔子自动拉取**（gradle-wrapper 模式）：整合包只带 ~30KB wedge.jar，不再附带 updater-core.jar。楔子首次启动自动经 `endpoint + channel` 拼出的 updater-core 端点拉取 core jar，本地保留 3 版用于回滚；`jm-updater.json` 禁止配置完整 `coreEndpoint`。
- **新增端点**：`GET /client-channels/:id/updater-core`（拉取密钥鉴权，返回当前选定分发信息 `{version, sha256, downloadUrl, size}`；`version` 为频道级递增分发版本，`sha256` 为实际 core jar）+ `GET /client-channels/:id/updater-core/versions`（JWT 平台管理员，列归档版本）+ `PUT /client-channels/:id/updater-core/selected`（JWT 平台管理员，切换选定版本）。

manifest 的 `agent.core` 段保留但信息来源改为 updater-core 查询端点（不再由 manifest 驱动 core 自更新）。

### 4. 楔子代码冻结约束（关键架构决策）

楔子第一版发布后**代码冻结、永不变更**。后续所有逻辑变动（清理规则、下载策略、协议字段等）都在 updater-core 里做，通过楔子自动拉取新版 core 实现。为此冻结四件事（详见 spec §2.5）：

#### 4.1 楔子↔core 接口契约冻结
楔子通过反射调用 core 的入口，签名**永久固定**：
```java
package top.wcpe.mc.jm.updater.core;
public final class Core {
    public static int run(Map<String, String> ctx) { ... }
}
```
类名 `top.wcpe.mc.jm.updater.core.Core`、方法名 `run`、参数 `Map<String, String>`、返回 `int`（0=放行，非 0=fail-static）均不变。**后续所有 updater-core 版本必须保留此入口**——否则楔子加载不了新版 core，自动更新链条断裂。`CoreLoaderTest` 用真实构建的 core jar 验证反射调用链路。

#### 4.2 jm-updater.json 原文透传
楔子只解析自己需要的字段（`channel`/`key`/`endpoint`/`timeoutSec`），但把 `jm-updater.json` 原始 JSON 文本也放进 ctx（key 为 `configJson`）。后续 core 需要新配置项时只需 `jm-updater.json` 加字段 → 楔子不认得但透传原文 → core 自己从 `ctx.get("configJson")` 解析新字段，**不需要改楔子**。`endpoint` 必须是 API 根路径（如 `/api/v1`），`coreEndpoint` 配置字段禁止出现。

ctx 固定 key（楔子写入）：`channel` / `key` / `endpoint` / `timeoutSec` / `telemetry` / `gameDir` / `coreVersion` / `configJson`。

#### 4.3 coreEndpoint 返回格式冻结
```
GET /client-channels/:id/updater-core → { "version": int, "sha256": string, "downloadUrl": string, "size": long }
```
格式冻结——后续 CP 升级只能**加字段**不能删/改已有字段。楔子只读这四个字段，多余字段忽略。`version` 是给楔子比较用的频道级分发版本，不要求等于归档列表中的 jar 版本；切换到旧 `sha256` 回滚时也必须递增。

#### 4.4 楔子不解析 manifest
楔子**永远不接触 manifest**——拉 manifest、解析文件列表、reconcile 全是 core 的职责。楔子只管「拉 core + 加载 core + 调 Core.run」。这样 manifest 格式后续怎么变都不影响楔子。

### 5. 去 CAS + 流式下载（FR-257）

`CasCache.java` 整个类删，Reconciler 改流式下载：`Transport.fetchArtifact` 返回 `InputStream`，Reconciler 边读边写盘（64KB 缓冲区），`DigestInputStream` 流式算 sha256。大文件支持 HTTP Range 断点续传。`atomicWrite` 改为流式写（临时文件 → sha256 校验通过 → 原子 rename）。1GB 文件下载内存占用恒定 < 10MB。

## 理由

- **验签的安全前提不成立**：ADR-022 决策 2 的安全前提是「私钥服务端持有、不泄露」。但 FR-248 让生产态自动生成私钥并持久化到 `<dataRoot>/etc/client-sign-key.pem`（0600）——服务器被攻破即私钥泄露，签名防不住源端投毒。真正防源端投毒的是运营方保证首发渠道可信（ADR-022 决策 6），OTA 通道靠 HTTPS + token 足够。
- **复杂度与收益不匹配**：验签换来的 14MB jar + 密钥管理 + 配置面 + 跨语言签名一致性维护，在实际部署下安全收益为零（私钥与内容同源泄露）。
- **CAS 对整包分发反膨胀**：CAS 为小文件去重设计，整包式分发改一个 mod 即全量重存。逐文件 size+sha256 快筛对当前规模够用。
- **gradle-wrapper 模式解鸡生蛋**：core 自更新的下载逻辑在 core 自己身上，首次启动必须有 core。楔子拉 core 把「首次可用」问题从 core 自身移到 ~30KB 楔子，整合包体积从 ~16MB 降到 ~30KB。
- **楔子冻结保可演进**：楔子发布后无法再更新已分发的楔子代码，所有后续逻辑变动必须靠更新 core 实现。冻结接口契约 + configJson 透传 + 楔子不碰 manifest 三件事，使 core 可独立演进不断链。

## 后果

- **删**：`Signatures.java`、`CasCache.java`、`JmPack.java`、`SelfUpdater.java`（updater-core）；manifest 签名逻辑、jmpack 端点、sign-key 端点（CP 后端）；`ClientSignKeyCard` 组件 + `clientDistSignKey` API + i18n + mock（前端）。
- **加**：`CoreFetcher.java`（楔子 HTTP 下载 + sha256 校验）；`coreEndpoint` 端点族 + core 版本归档（CP）；`ClientUpdaterCoreSelector` 版本选择器面板（前端）。
- **manifest 格式变**：去 `sig`/`sigKeyId` 段，schema 向后不兼容（客户端旧版验签会失败，但旧客户端本就需升级 core 才能继续工作——gradle-wrapper 模式下首版楔子拉新版 core 即适配）。
- **updater-core.jar 体积**：从 ~16MB（含 BC）降到 ~2MB（仅 zstd-jni）。整合包只含 wedge.jar（~30KB）+ jm-updater.json。
- **信任模型简化**：不再需要签名密钥管理（生成/持久化/轮换/配置公钥），运营接入门槛降低。安全保证靠 HTTPS + 拉取密钥 + sha256 完整性校验 + boot-confirm 看门狗 + 服务端一键回滚。
- **关联 FR 废弃**：FR-090（CAS）、FR-091（core 自更新，上移到楔子）、FR-097（.jmpack）、FR-253（公钥可配）、FR-248（签名密钥自动生成）。
- **后续 core 安全性**：靠 boot-confirm 看门狗（新版崩溃不 promote → 回退旧版）+ failedVersion 记录（不重试同版本）+ 服务端一键回滚 + sha256 校验 + 最坏情况运营发新版 core 修复。

## 替代方案

- **维持签名信任模型（ADR-022/053）** — 验签安全前提在自动生成落盘方案下不成立，复杂度换不来实质安全，否决。
- **私钥离线/HSM 签发** — 中小运营商无 HSM 条件、离线签发流程过重，且 HTTPS + token 已满足实际威胁模型，YAGNI，否决。
- **core 仍随整合包附带（ADR-045 原状）** — 整合包 14MB、core 升级需重发整包，gradle-wrapper 模式下 30KB 楔子 + 自动拉 core 更优，否决。
- **保留 CAS 但改小文件模式** — 整包式分发下 CAS 反膨胀，逐文件 size+sha256 快筛够用，否决。
- **delta patch / 字节级 diff** — 本 ADR 当时否决通用 diff / 容器式增量；FR-098 后续以 [ADR-068](068-client-dist-zstd-patch-from.md) 收敛为可选 zstd patch-from，不恢复 CAS / `.jmpack`。
