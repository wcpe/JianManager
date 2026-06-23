# ADR-021: 客户端分发与自动更新——纯 JVM 方案（javaagent 楔子 + 动态加载 updater-core，不用 Go）

- **日期**: 2026-06-23
- **状态**: accepted

## 上下文

JM 要新增「客户端分发与自动更新」第三条产品线（玩家客户端 OTA），让运营方能把整合包客户端的可变内容（mods / 资源包 / 配置 / runtime）做集中版本管理与增量下发，玩家重启即最新。本 ADR 只确立**客户端侧更新组件的形态与技术栈**；服务端信任模型与公网端点见 [ADR-022](022-client-manifest-trust-and-public-endpoint.md)。

约束与既定前提（前序需求探讨已定）：

- **玩家用第三方启动器（HMCL / PCL2）**，且**客户端整包打包、启动器适配、玩家侧分发由运营方自理，不在 JM 范围**。JM 只交付客户端侧的更新组件 jar。
- 第三方启动器**只能给一个通用注入点：JVM 启动参数**（不会去调任意 exe）。所以注入只能走 `-javaagent:xxx.jar`——这在 MC 玩家圈是成熟做法（外置登录 authlib-injector 即 javaagent）。
- 曾倾向用 **Go 独立二进制**做更新器（跨平台单文件、自更新好做、进程外容错）。

但在「javaagent 注入」这个已定前提下重新推演，Go 的核心优势**失效**：

- **「脱离 JVM」失效**：楔子是 `-javaagent`，premain 时更新器已经跑在游戏 JVM 内，Go 的「不依赖 JVM」无从体现。
- **「进程外」失效**：楔子无论如何要同步等更新完才放行游戏；Go 子进程不比 premain 内直接执行更省事，反多一层 fork + IPC。
- **「能在 JRE 未就位时工作」用不上**：premain 已在 JVM 内，无论 Go 还是 jar 都不能替换正在使用的 JRE（只能下好下次生效）。

而 jar 的优势全部成立：一份 jar 三平台通用（免交叉编译与分发三个二进制）；与楔子统一技术栈（一个仓、一套 Gradle，且与 ServerProbe / Beacon agent 同为 JVM 系 jar）；premain 内弹 Swing 进度窗现成。

## 决策

1. **客户端更新组件为「两件套」纯 JVM jar**，不用 Go：
   - **楔子 jar（wedge）**：极小（仅引导）。经启动器 JVM 参数 `-javaagent:楔子.jar` 注入；premain 用 `getCodeSource().getLocation()` **自定位**、解析 gameDir、用独立 classloader **动态加载 `updater-core.jar`** 并调用其入口、同步等待结果。
   - **updater-core.jar**：更新主体。拉签名 manifest、reconcile（增量/减量）、校验、CAS 缓存、自更新、客户端回滚。

2. **楔子稳定、随基础整包分发（低频）**。它几乎不变，被 `-javaagent` 加载（Windows 上文件被锁、不便运行时自换），故**不依赖运行时自更新**——极少数需改时由运营方重打基础包替换。

3. **updater-core 是热更主体**。它**不被 `-javaagent` 直接加载**（楔子以 `URLClassLoader` 内存加载，文件不被持续锁），因此能干净自更新：下新 jar、验签 + selftest 通过后切换、失败回退、下次 premain 加载新版。这把「jar 自更新的文件锁」难点落在容易的那块（core），难的那块（wedge）设计成无需常更。此分层与「基础包（低频）vs 动态内容（高频）」一致：**wedge 属基础包，updater-core 属动态内容**。

4. **fail-static**：updater-core 在更新端点不可达 / 断网时，带本地现有版本返回成功，楔子放行进游戏 + 显眼提示。控制面挂 ≠ 玩家进不去（与 Beacon agent、ServerProbe 同一可用性原则）。

5. **归属为 JM 仓内子目录 `client-updater/`（Java / Gradle，monorepo）**，类比既有 `bot-worker/`（Node.js 子目录、独立构建、非 Go 组件）——不单独建仓、不走 git 子模块。client-updater 独立 Gradle 构建产出 wedge / updater-core 两 jar，**不进 Go 主构建**；服务端分发能力在 JM 主仓 Go 侧。楔子↔core 的 agentArgs 协议（gameDir / channel / 可选 endpoint）为接口契约，已定稿（`docs/specs/client-distribution/contract.md`）。
   > **2026-06-23 修订**：原定「独立仓 + JM 子模块」（同构 ServerProbe/Beacon agent），改为 monorepo 子目录。理由：updater 是全新自研、无 fork 上游、无需独立仓；monorepo 避免子模块「指向未 push commit / 上游分叉 reconcile」的运维成本（ServerProbe 子模块踩过）；与 `bot-worker/` 同构，JM 仓本就是 Go + React + Node 多语言，加 Java 子目录一致。

6. **楔子极致健壮、fail-open**：premain 全程 try/catch，楔子自身任何异常（自定位失败 / core 缺失 / 加载错误 / 更新超时）都**放行游戏**——楔子比 core 更关键，它唯一允许的失败模式是「放行」，绝不因楔子或更新挡住玩家启动。

7. **多 javaagent 共存**：玩家常已挂外置登录 `authlib-injector`（其本身即 javaagent）等其他 `-javaagent`，楔子须与之并存、加载顺序无关；在 HMCL / PCL2 上真机验证楔子与 authlib-injector 同挂不冲突。

## 理由

- **顺应注入约束**：第三方启动器只给 JVM 参数，javaagent 是唯一通用注入点；jar 与该注入点天然同栈。
- **Go 在本场景无净收益**：其三大优势在 javaagent premain 下全部失效，而代价（交叉编译、三平台分发、与楔子异构、premain 调子进程的复杂度）仍在。
- **wedge/core 分层化解 jar 自更新锁**：把易变逻辑放进未被 `-javaagent` 锁的 core，自更新简单可靠。
- **统一技术栈、复用既有工程惯例**：与 ServerProbe / Beacon agent 同为 JVM 系独立仓 + 子模块，构建、发版、嵌入路径有先例可循。

## 后果

- 客户端组件为 JM 仓内 `client-updater/`（Java / Gradle 子目录，monorepo，类比 `bot-worker/`）；客户端线全 JVM，独立 Gradle 构建产 jar、不进 Go 主构建，无 Go 工具链引入。
- updater-core 用 JDK 自带能力（HTTP / `MessageDigest`）+ 轻量 JSON + 必要的少量三方库，保持 jar 精简（与探针自写最小客户端的克制一致）。
  - **更新（2026-06-23，FR-089 真机后修正）**：**updater-core 改 target Java 8**（楔子本就 Java 8）——老整合包/启动器仍跑 Java 8，core 若编到 17 会 `UnsupportedClassVersionError`、楔子加载失败（真机证实）。Java 8 无 `java.net.http`（→ 改用 `HttpURLConnection`）、无 JDK 内置 Ed25519（15+，→ 引 **BouncyCastle** `bcprov-jdk18on` 打进 fat jar）。即「零三方依赖」让位于「低版本 JVM 广兼容」；签名仍为标准 Ed25519、与服务端 Go 逐位兼容。已在真 Java 8(1.8.0_422) JVM 经楔子加载 + 真 CP OTA + BC 验签端到端验证。
- 楔子的 agentArgs 协议、相对路径 / cwd 兼容性需在 FR-089 真机（HMCL + PCL2）验证；自定位（`getCodeSource`）使相对路径只需「够 JVM 加载到 jar」，后续定位不依赖 cwd。
- **关联 FR**：FR-089（楔子）、FR-090（updater-core reconcile）、FR-091（core 自更新 + 客户端回滚）。
- 与 Beacon 无交叉：本线是客户端分发，Beacon 是服务端集群治理，故障域与职责互斥。

## 替代方案

- **Go 独立更新器二进制** — 在 javaagent 注入前提下「脱 JVM / 进程外」优势失效，而交叉编译、三平台分发、与楔子异构、premain 调子进程的成本仍在，否决；仅当未来改做**自研独立启动器**（需在无 JVM 时安装 JRE）才重新考虑原生二进制。
- **单一 jar（楔子与更新逻辑合一，不分层）** — 更新逻辑随 `-javaagent` 被加载锁住，自更新困难，否决。
- **自研完整启动器替代第三方启动器** — 体验最完整但要玩家迁移、且打包/分发明确移出 JM 范围，否决（留作长期可选，届时 updater-core 主体可复用、仅换「谁调起它」）。
- **更新器做成 Fabric/Forge mod（preLaunch 入口）** — mod 扫描发生在加载器引导之后，用 mod 自身去更新 mods 目录太晚，否决（premain 早于 mod 扫描，是正确时机）。
