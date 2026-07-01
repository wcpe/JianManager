# 功能规格：客户端更新器架构简化（去验签 + 去 CAS + 楔子自动拉 core + 流式下载）

> 状态：待审　·　关联 PRD：待登记（新 FR）　·　推翻 ADR-022/053，修订 ADR-045，废弃 FR-090/091/097/253

## 1. 背景与目标

客户端 OTA 更新器当前有四层复杂机制：

1. **Ed25519 验签**：manifest 签名 + .jmpack 容器签名 + BouncyCastle 14MB 依赖 + 密钥管理 + FR-253 公钥配置
2. **CAS 内容寻址缓存**：下载的文件按 sha256 全量落盘缓存，LRU 1.5GB 上限
3. **core 自更新**：updater-core 自己下载新版自己（鸡生蛋问题，必须附带 14MB jar）
4. **全内存下载**：Reconciler 把每个文件完整读进 `byte[]`（`Files.write(tmp, content)`），1GB 资源包直接占 1GB 内存

真机暴露四个问题：

- **验签形同虚设**：私钥存在服务器上（自动生成落盘或 env 注入），服务器被攻破时私钥同样泄露，攻击者可伪造签名。验签付出的复杂度（BC 14MB、密钥管理、配置面）换来的安全保障在实际部署下不成立。信任模型实际只靠 HTTPS + 拉取密钥鉴权。
- **CAS 对大文件膨胀**：200MB 资源包改一个 mod → sha256 变 → CAS 存一份全新 200MB → 旧的留着不命中 → 积累到 1.5GB 才 LRU 清理。CAS 是为小文件去重设计的，对整包式分发不适用。
- **core 自更新鸡生蛋**：下载 core 的逻辑在 core 自己身上，首次启动必须有 core 可用，只能整合包附带 14MB jar。
- **全内存下载炸内存**：1GB `pack.allin` 资源包走 `byte[]` 全量读进内存再写盘，客户端内存不够直接 OOM。

**目标**：简化信任模型（HTTPS + token 够用）、去掉 CAS（靠 size+sha256 快筛）、楔子改 gradle-wrapper 模式（整合包只带 ~30KB 楔子，首次自动拉 core，本地保留 3 版用于回滚）、Reconciler 改流式下载（大文件不进内存，支持 HTTP Range 断点续传）。

## 2. 需求（要什么）

### 范围内

#### A. 去掉验签（信任模型简化）
- manifest 去掉 `sig` 段，客户端拉到 manifest 直接用，不再验签
- .jmpack 格式连同验签一起删（现在没在主流程用，Reconciler 从不调 JmPack.unpack）
- `Signatures.java` 整个类删，BouncyCastle 依赖从 updater-core 移除
- `jm-updater.json` 的 `signPublicKey`/`signKeyId` 字段废弃
- 防降级机制去掉（不再有签名覆盖 version，客户端不再记录 lastSeenVersion 拒绝低版本；回滚靠服务端改版本号）
- sha256 文件完整性校验保留（下载后验证文件没坏，不是信任校验）
- 拉取密钥鉴权保留（防外人乱拉）

#### B. 去掉 CAS
- `CasCache.java` 整个类删
- Reconciler 的 `obtainContent` 简化为"直接流式下载"（不再查 CAS、不再全量读进 `byte[]`）
- `.jm-updater/cas/` 目录不再创建
- `Updater.java` 的 `CAS_LIMIT_BYTES` 和 `enforceLimit` 调用删
- 文件级快筛保留：先比 size（快），size 对了再算 sha256（精确确认），都对了跳过下载

#### B2. Reconciler 改流式下载 + 断点续传
- 下载不再全量读进 `byte[]`：`Transport.fetchArtifact` 改为返回 `InputStream`，Reconciler 边读边写盘（64KB 缓冲区）
- sha256 校验改为流式：下载同时用 `DigestInputStream` 计算 hash，下完即得校验值
- 大文件支持 HTTP Range 断点续传：下载中断后下次从断点继续（`Range: bytes=<已下载>-`）
- `atomicWrite` 改为流式写：先写 `.jmtmp` 临时文件，下完 + sha256 校验通过后原子 rename 到目标路径
- zstd 解压改流式：`Codec.decode` 从 `byte[]→byte[]` 改为 `InputStream→InputStream`（zstd-jni 原生支持流式）

#### C. 楔子改 gradle-wrapper 模式
- 整合包**只带 wedge.jar**（去掉 BC 后约 30KB），不再附带 updater-core.jar
- `jm-updater.json` 加 `coreEndpoint` 字段（指向 CP 的 core 版本查询端点），去掉 `coreJar`/`coreVersion`/`signPublicKey`/`signKeyId`
- 楔子新增 JDK 原生 HTTP 下载能力（`HttpURLConnection`，零外部依赖）
- 楔子新增 SHA-256 校验能力（`MessageDigest`，JDK 原生）
- 楔子首次启动：本地无 core → HTTP 请求 coreEndpoint → CP 返回版本信息 → 下载 → sha256 校验 → 存入 `.jm-updater/core/<sha>.jar`
- 楔子后续启动：查 coreEndpoint 发现版本号 > 本地 selected → 下载新版 → 标记 pending → trial → boot-confirm → promote/rollback
- 本地保留最近 3 个 core jar，超出自动清理最老的可用 jar

#### D. 服务端 core 版本管理 + 回滚
- CP 每次 `make embed-client-updater` 时新 core jar 入库归档（不覆盖旧版），版本号递增
- 新增 CP 端点：`GET /client-channels/:id/updater-core` → 返回 `{version, sha256, downloadUrl, size}`（当前选定版本）
- 新增面板操作：运营在「客户端分发 → 频道详情」看到「updater-core 版本」选择器，列出所有归档版本，一键切换"当前用哪个"
- 切换后客户端下次启动查端点 → 拿到旧版本号 → 本地有就直接用、没有就下载

### 不做（范围外）
- 离线签发 / HSM（验签已去掉，不需要）
- delta patch / 字节级 diff（YAGNI，逐文件 size+sha256 快筛对当前规模够用；资源包拆散后天然增量）
- .jmpack 格式保留代码但不再验签——不，格式整个删（散文件下载更优，见 §2 B2）
- 改 reconcile 的减量逻辑 / managedDirs / cleanExclude / clean-all（FR-255 不受影响）

## 2.5 楔子代码冻结约束（关键架构决策）

**核心诉求**：楔子第一版发布后**代码冻结、永不变更**。后续所有逻辑变动（清理规则、下载策略、协议字段等）都在 updater-core 里做，通过楔子自动拉取新版 core 实现。

为此必须冻结三件事：

### 2.5.1 楔子↔core 接口契约冻结
楔子通过反射调用 core 的入口，签名**永久固定**：
```java
package top.wcpe.mc.jm.updater.core;
public final class Core {
    public static int run(Map<String, String> ctx) { ... }
}
```
- 类名 `top.wcpe.mc.jm.updater.core.Core` 不变
- 方法名 `run` 不变
- 参数 `Map<String, String>` 不变
- 返回 `int`（0=放行，非 0=fail-static）不变
- **后续所有 updater-core 版本必须保留此入口**——否则楔子加载不了新版 core，自动更新链条断裂

### 2.5.2 jm-updater.json 原文透传
楔子只解析自己需要的字段（channel/key/endpoint/coreEndpoint/timeoutSec），但**把 jm-updater.json 原始 JSON 文本也放进 ctx**（key 为 `configJson`）。

这样后续 core 需要新配置项时：
- jm-updater.json 加字段 → 楔子不认得但透传原文 → core 自己从 `ctx.get("configJson")` 解析新字段
- **不需要改楔子**

ctx 当前固定 key（楔子写入）：
- `channel` / `key` / `endpoint` / `timeoutSec` / `telemetry` / `gameDir` / `coreVersion`
- `configJson`（jm-updater.json 原文，供 core 自行扩展解析）

### 2.5.3 coreEndpoint 返回格式冻结
```
GET /client-channels/:id/updater-core → { "version": int, "sha256": string, "downloadUrl": string, "size": long }
```
格式冻结——后续 CP 升级只能**加字段**不能删/改已有字段。楔子只读这四个字段，多余字段忽略。

### 2.5.4 楔子不解析 manifest
楔子**永远不接触 manifest**——拉 manifest、解析文件列表、reconcile 全是 core 的职责。楔子只管"拉 core + 加载 core + 调 Core.run"。这样 manifest 格式后续怎么变都不影响楔子。



### 3.1 楔子（wedge）— 新增 HTTP 下载 + 版本管理

新增类 `CoreFetcher`（JDK 原生，零外部依赖）：
- `fetch(CoreEndpointInfo info)` → 下载 jar 字节 + sha256 校验
- `HttpURLConnection` 做 HTTP GET（支持超时、重试 1 次）
- `MessageDigest.getInstance("SHA-256")` 校验下载内容

`WedgeConfig` 改动：
- 去掉 `coreJar`/`coreVersion`/`signPublicKey`/`signKeyId`
- 加 `coreEndpoint`（CP 端点地址，如 `https://你的服务器/api/v1/client-channels/my-channel/updater-core`）

`Wedge.premain` 新流程：
```
1. 读 jm-updater.json（channel/key/endpoint/coreEndpoint/timeoutSec）
2. 查 .jm-updater/core/state.properties 本地状态
3. HTTP 请求 coreEndpoint → CP 返回 {version, sha256, downloadUrl, size}
4. 决策：
   - 本地无 jar（首次）→ 下载 CP 返回的版本 → 存为 pending
   - CP 版本 > 本地 selected → 下载 → 存为 pending
   - CP 版本 = 本地 selected → 直接用
   - CP 版本 < 本地 selected → 用本地 selected（不降级本地）
5. CoreSelector.select（不变：pending trial / promote / rollback）
6. 加载 selected/pending jar → CoreLoader.loadAndRun（不变）
7. boot-confirm 看门狗（不变）
8. 清理：保留最近 3 个 jar，删最老的
```

### 3.2 updater-core — 删验签 + 删 CAS + 简化 reconcile

删除：
- `Signatures.java`（整个类）
- `CasCache.java`（整个类）
- `JmPack.java`（整个类，.jmpack 格式废弃）
- `CoreSelfTest.java` 中与验签相关的自检（保留 zstd 解压自检）

`Manifest.java` 改动：
- 去掉 `sig`/`sigKeyId` 字段
- `verify` 方法删
- `agentCoreVersion`/`agentCoreArtifact` 段保留（楔子用，但信息来源从 coreEndpoint 而非 manifest——见 3.4）

`Updater.java` 改动：
- 步骤 2（验签）删
- 步骤 3（防降级 lastSeenVersion）删
- 步骤 5（CAS LRU 清理）删
- 步骤 8（core 自更新 SelfUpdater）删——自更新上移到楔子

`Reconciler.java` 改动：
- `obtainContent` 去掉 CAS 查询，改为流式下载（`InputStream` → 边读边写盘 + DigestInputStream 算 sha256）
- `applyFile` 去掉 `cas.put`；`atomicWrite` 改为流式写（临时文件 → sha256 校验通过 → rename）
- `obtainContent` 支持断点续传：检查 `.jmtmp` 文件已下载大小 → HTTP Range 续传
- 快筛逻辑保留：`quickMatch`（size → md5 → sha256）
- `estimateDownloadBytes` 去掉 `cas.has` 检查

`Transport.java` 改动：
- `fetchArtifact` 返回类型从 `byte[]` 改为 `InputStream`（或新增 `fetchArtifactStream`）
- `HttpTransport` 支持 HTTP Range 请求头（`Range: bytes=<offset>-`）

`SelfUpdater.java`：整个类删（自更新上移到楔子）
`CoreSelectStore.java`：保留（楔子复用 state.properties 状态机）

### 3.3 updater-core 体积变化
- 去掉 BouncyCastle（~14MB）→ updater-core.jar 预计降到 1-2MB
- zstd-jni 保留（仍用于制品流式解压，Codec.java）
- 最终 jar 大小取决于 zstd-jni 体积（约 1MB），预期 updater-core.jar ~2MB

### 3.4 服务端（CP）

新增：
- `GET /client-channels/:id/updater-core` 端点 → 返回 `{version, sha256, downloadUrl, size}`
  - 鉴权：拉取密钥（与 manifest 端点同级，非 JWT）
  - 返回当前选定版本的 core jar 信息
  - downloadUrl 指向 `/client-artifacts/:sha256`（复用既有制品分发端点）
- core 版本归档：`make embed-client-updater` 时新 jar 入库 `type=client-updater-core`，版本号递增
- 面板「updater-core 版本」选择器：列出归档版本，运营一键切换

manifest 改动：
- 去掉 `sig`/`sigKeyId` 段
- `agent.core` 段保留但信息来源改为 coreEndpoint（不再由 manifest 驱动 core 自更新）

删除：
- `jmpack.go` / `jmpack_service.go` / `jmpack_test.go`（.jmpack 打包端点删）
- manifest 签名逻辑（`SignRaw`/`Sign` 方法删，signer 仅用于...全部删）

### 3.5 前端
- 接入指引（ClientIntegrationGuide）去掉 signPublicKey 相关，core 下载改为"楔子自动拉取"说明
- 新增「updater-core 版本」选择器面板（频道详情页）
- i18n zh/en

## 4. 任务拆分

### 阶段 1：协议 + 客户端简化（最先做，锁协议）
- [ ] updater-core：删 Signatures/CasCache/JmPack/SelfUpdater + Manifest 去 sig + Reconciler 去 CAS + Updater 去验签/防降级/CAS清理/自更新
- [ ] updater-core：Reconciler 改流式下载（InputStream 边读边写 + DigestInputStream + 断点续传 + zstd 流式解压）
- [ ] updater-core：Transport 改返回 InputStream + 支持 HTTP Range
- [ ] updater-core：build.gradle 去 BouncyCastle 依赖
- [ ] updater-core：`./gradlew test` 绿（删验签/CAS/jmpack 相关测试、保留 reconcile/快筛/core 加载测试 + 新增流式下载/断点续传测试）
- [ ] CP：manifest 去签名逻辑 + jmpack 端点删 + 制品分发端点确认支持 Range
- [ ] CP：`go build`/`test` 绿

### 阶段 2：楔子自动拉 core（楔子代码冻结版本）
- [ ] wedge：新增 CoreFetcher（HTTP 下载 + sha256 校验）
- [ ] wedge：WedgeConfig 改（去 coreJar/coreVersion/signPublicKey，加 coreEndpoint）
- [ ] wedge：Wedge.premain 新流程（查端点 → 下载 → 本地版本管理 → CoreSelector）
- [ ] wedge：jm-updater.json 原文透传到 ctx（`configJson` 键，供 core 后续扩展）
- [ ] wedge：本地保留 3 版 + 自动清理
- [ ] wedge：**接口契约冻结测试**——用真实构建的 core jar 验证 `Core.run(Map)` 反射调用成功（保证后续 core 版本接口稳定）
- [ ] wedge：`./gradlew test` 绿

### 阶段 3：服务端 core 版本管理 + 回滚
- [ ] CP：core 版本归档（make embed-client-updater 入库 type=client-updater-core）
- [ ] CP：`GET /client-channels/:id/updater-core` 端点 + 测试
- [ ] CP：面板版本选择器 + 切换逻辑 + 测试
- [ ] CP：`go build`/`test` 绿

### 阶段 4：前端 + 文档
- [ ] 前端：接入指引更新 + 版本选择器 + i18n + dom 测试
- [ ] ADR：新 ADR 推翻 ADR-022/053、修订 ADR-045
- [ ] doc-sync：API.md / ARCHITECTURE.md / PRD / CHANGELOG
- [ ] 重编内嵌 updater-core.jar（`make embed-client-updater`）

## 5. 验收标准
- updater-core `./gradlew test` 绿（验签/CAS/jmpack 相关测试删除，reconcile/快筛/core 加载测试保留）
- wedge `./gradlew test` 绿（CoreFetcher 下载 + sha256 校验 + 版本管理 + 本地清理）
- CP `go build`/`test` 绿（manifest 去签名、jmpack 端点删、core 版本端点 + 选择器）
- 前端 tsc/lint/vitest/build 绿
- updater-core.jar 体积显著下降（去掉 BC 14MB，预期 ~2MB）
- **1GB 文件下载内存占用 < 10MB**（流式下载 + 64KB 缓冲，不再全量读进 byte[]）
- **断点续传**：下载中断后重试从断点继续，不从头开始
- 整合包只含 wedge.jar（~30KB）+ jm-updater.json，不含 updater-core.jar
- 首次启动楔子自动拉 core 成功、后续版本升级/回滚正常
- **【需真机，用户确认】** 首次启动（本地无 core）→ 楔子拉取 core → 游戏正常启动更新；CP 切换 core 版本 → 客户端下次启动用旧版
- **楔子↔core 接口契约测试**：用第一版楔子加载最新版 core jar → Core.run(Map) 反射调用成功 → 证明接口契约稳定，后续 core 更新不会断链

## 6. 风险 / 待定

### 6.1 首次启动断网
楔子拉不到 core → fail-open（放行游戏，不更新）。与现在"附带 core 但 manifest 端点不可达 → fail-static"不同——gradle-wrapper 模式下首次断网=无更新但游戏可玩。**可接受**：首次安装通常在线，断网是极端情况。

### 6.2 coreEndpoint 端点不可达
楔子用本地最近 selected 版本（如果有）；本地一个 jar 都没有 → fail-open 放行游戏。比现在好：现在端点不可达 fail-static（带本地版本放行），新版改为 fail-open（无本地版本也放行，只是不更新）。

### 6.3 去掉防降级
不再拒绝低版本 manifest。但实际威胁场景是"攻击者重放旧 manifest 投毒"——现在信任靠 HTTPS + token，攻击者没有 token 拉不到 manifest，有 token 的人（玩家）重放旧 manifest 只能让自己用旧版（不影响他人）。**可接受**。

### 6.4 推翻既有 ADR 的影响
- ADR-022（签名信任模型）被推翻——信任根从"签名"改为"HTTPS + token"
- ADR-053（配置公钥供给）被推翻——signPublicKey 废弃
- ADR-045（core 默认随 CP 内嵌）被修订——内嵌改为归档多版本 + 运营可选
- FR-090/091/097/253 废弃或回滚
- 需新 ADR 记录这次架构简化决策

### 6.5 coreEndpoint 返回信息的安全性
coreEndpoint 返回的 `{version, sha256, downloadUrl, size}` 本身走 HTTPS + 拉取密钥鉴权。攻击者即使截获，没有 token 拉不到；有 token 的人（玩家）拉到的是合法版本信息。sha256 校验保证下载的 jar 没坏。**足够**。

### 6.6 楔子代码冻结后的后续 core 安全性
楔子第一版发布后代码冻结。后续 core 更新靠"楔子查 coreEndpoint → 发现新版 → 下载 → trial → boot-confirm → promote"。核心安全保证：
- **boot-confirm 看门狗**：新版 core 启动崩溃 → 不 promote → 下次回退旧版（已有 FR-091 机制，不变）
- **failedVersion 记录**：某版 core trial 失败后标记，不再重试同版本（避免 boot-loop）
- **服务端一键回滚**：core 有逻辑 bug（不崩但行为错）→ 运营面板切回旧版 → 客户端下次启动用旧版
- **sha256 校验**：下载的 core jar 字节完整性保证
- **最坏情况**：所有 core 版本都有问题 → 运营发新版 core 修复 → 客户端自动拉新版
