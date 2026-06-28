# ADR-045: updater-core 集中版本管理（频道 pin 驱动 manifest agent.core；楔子冻结）

- **日期**: 2026-06-28
- **状态**: accepted
- **补充**: [ADR-021](021-client-distribution-jvm-updater.md)（两件套纯 JVM 方案）；与 [ADR-022](022-client-manifest-trust-and-public-endpoint.md) 防降级 / 单调 version 并存

## 上下文

ADR-021 把客户端更新组件分成两件套：**楔子（wedge）= 稳定 wrapper**（随基础包低频分发、被 `-javaagent` 加载、不自更新）与 **updater-core = 可热更主体**（不被 `-javaagent` 锁、能干净自更新）——这已是 gradle-wrapper 模式（楔子如 `gradlew`、core 如 `gradle-wrapper.jar`）。FR-091 已实现客户端侧 core 自更新：core 消费 manifest 的 `agent.core`，只把**更高** `agent.core.version` 暂存为 pending（下载 + sha256 + selftest），由楔子下次 premain 经 boot-confirm 看门狗 promote，启动失败自动回退 N-1。

但**服务端 / 运营缺集中控制**：manifest 的 `agent.core`（version + per-platform 制品）此前由发布向导**手填透传**（FR-088「透传原值、不编辑」）。运营无法在管理面**集中 pin / 更新 / 回退** updater-core 版本——core 出问题时只能被动等客户端各自 N-1 自发回退，无法主动按频道把全量客户端拉到某个已知好的 core。

需要把 updater-core 做成 gradle-wrapper 式**集中版本管理**：运营上传 core jar、按频道 pin core 版本、一键更新到新版 / 回退坏版，manifest 的 `agent.core` 由该 pin 驱动产出。**楔子保持冻结、单版本、不纳入管理**（ADR-021 决策 2/3：楔子几乎不变，被 `-javaagent` 加载、文件被锁、不便运行时自换）。

## 决策

1. **updater-core 集中版本管理（频道 pin 驱动 `agent.core`）**。
   - **core 制品注册**：运营上传 updater-core jar 入 FR-045 制品库，类型 `client-core`（与 `client-file` 物理分区、按制品自身 sha256 内容寻址去重）；登记为带**版本号**的 core 版本记录（`ClientCoreVersion`：version + 制品 sha256/size/codec）。core 版本号在平台内全局单调递增（与频道无关；core jar 与频道无关，一份可供所有频道复用）。
   - **频道 pin**：`ClientChannel.pinned_core_version`（0 = 自动指向当前已登记的**最新** core 版本）。每频道独立 pin，互不影响。
   - **manifest 生成由 pin 驱动**：`ClientVersionService.BuildManifest` 组装频道 latest 版本快照后，用频道 pin 选定的 core 版本**覆盖** `agent.core`（version + platforms），取代纯手填透传。`agent.wedge` 仍来自版本快照、不被覆盖（楔子冻结、信息性）。

2. **楔子冻结、单版本、不纳入版本管理**。不做楔子上传 / 楔子版本注册 / 楔子自更新 / 楔子 pin。`agent.wedge.version` 恒为发布快照中的值（信息性，客户端不据它自更新，ADR-021 决策 2）。

3. **两条版本轴必须分清**（本 ADR 的核心约束，否则易把 core 回退误判为防降级违规）：
   - **manifest `version`**：**内容版本轴**，单调递增、客户端持久化 `lastSeenVersion` 防降级 / 防重放（ADR-022 §3）。运营回滚内容（FR-088）= 以**更高** manifest version 重发旧文件清单，绝不下发更低号。
   - **`agent.core.version`**：**updater-core 自身版本轴**，由频道 core pin 驱动；**对客户端单调只升不降**——FR-091 / contract §6.3 已证客户端 core 只把「更高」 `agent.core.version` 暂存为 pending（`SelfUpdater.maybeUpdate`：`target <= runningCoreVersion` 或 `target <= selectedVersion` 即跳过），core 自身亦防降级。两轴**正交**：同一次 manifest 拉取里，内容轴与 core 轴各自独立单调。

4. **回退坏 core = 以更高 `agent.core.version` 重发旧 core 字节（不可降版）**，与 FR-088 内容回滚法同构。
   - 因 `agent.core.version` 对客户端单调只升不降（决策 3），「回退」**不能**靠把 pin 指回旧版本号——已升级到坏版的客户端不会降级，回退会无效。
   - 正确做法：把旧 core 的**字节**（content-addressed，同一 sha256）**重新注册为一个新的、更高的 core 版本号**，再把频道 pin 指向这个新版本。客户端看到更高 `agent.core.version` → 照常 promote「上去」→ 跑到的是旧 core 的内容。坏 core 应急下线，且不违反客户端防降级。
   - 即：core 版本号始终单调递增，pin 始终指向「当前应跑的 core 注册版本」（其内容可能是某个历史好版本的字节）。

5. **core 一份 jar 三平台通用，但 schema 要 per-platform**（ADR-021 理由：一份 jar 三平台通用，免交叉编译）。manifest `agent.core.platforms` 是 per-OS map（`windows`/`macos`/`linux`），客户端 `Platform.tag()` 用 `agentCorePlatforms.get(tag)` 取本机项。故运营**只上传一份 core jar**，后端把同一制品**填进 `windows`/`macos`/`linux` 三个键**（同 sha256/size/codec），避免无谓的多平台上传。`other` 平台（非三大桌面 OS）无 core 项 → 不自更新，沿用 FR-091 既有行为。

6. **客户端不改**（楔子冻结成立）。FR-091 现行逻辑（core 只 promote 更高 `agent.core.version`、boot-fail 回退 N-1、`failedVersion` 防 boot-loop）正是本 FR 所需：「更新」= pin 到更高版本 → 客户端升；「回退」= 重发旧字节为更高版本 → 客户端仍升（到旧内容）。无需改 wedge/core 代码。

7. **端点（平台管理员 JWT + 审计）**：上传 core 制品 / 列 core 版本 / 取频道 pin / 设·更新·回退频道 pin。与玩家拉取密钥端点物理隔离（同 FR-086/087 鉴权分组）。回退端点按决策 4「重发更高版本」语义实现。

8. **向后兼容（不破 FR-087/088）**：频道**无 core pin 且无任何已登记 core 版本**时，manifest `agent.core` **沿用版本快照中的手填透传值**（或省略），现有发布行为不变。仅当频道有可用 core 版本时，pin 驱动才接管 `agent.core`。

## 理由

- **职责落点正确**：更新器版本是「跨频道、跨发布」的横切关注点，集中到 core 版本注册 + 频道 pin，比塞进每次内容发布的 `agent` 字段手填更清晰、可治理。
- **复用既有信任与单调机制**：core 制品走 FR-045 内容寻址制品库（去重、CAS）；`agent.core.version` 的单调性与 ADR-022 的 manifest version 防降级同philosophy，回退用同一「重发更高号」法，无需引入降级通道（降级通道本身就是投毒面）。
- **楔子冻结化解锁难点**：把可变的版本管理放在未被 `-javaagent` 锁的 core，自更新简单可靠（ADR-021 决策 3）；楔子不纳管 = 不引入「运行时自换被锁文件」的难题。
- **零客户端改动**：FR-091 的 promote-higher-only + N-1 回退既有逻辑天然支持「更新」与「重发式回退」，降低本 FR 的真机回归面（楔子 / core jar 不变）。

## 后果

- 数据库：`client_channels` 加 `pinned_core_version`（加性列，默认 0）；新表 `client_core_versions`（version 全局单调 + 制品 sha256/size/codec + note + created_by）。制品库新增 `AssetType` `client-core`。
- manifest 生成：`agent.core` 来源从「版本快照手填透传」改为「频道 core pin 驱动」（有 core 注册时）；无 core 注册时回退手填透传（兼容）。
- API：新增 core 版本管理端点组（上传 / 列表 / 取·设·更新·回退 pin），均 JWT 平台管理员 + 审计。
- 前端：频道工作台新增「更新器版本」段（core 版本列表 + 当前 pin + 上传 + pin/更新/回退[DangerConfirm] + 楔子单版本只读冻结提示）。
- 客户端：**不改**（楔子 / updater-core jar 不变，靠 manifest 内容驱动 FR-091 既有逻辑）。
- 关联 FR：FR-193（本 ADR 创建动因）、增强 FR-091（core 自更新）、补充 ADR-021 / 与 ADR-022 防降级并存。

## 替代方案

- **回退靠降低 `agent.core.version`（pin 指回旧版本号）** — 客户端 core 只升不降（FR-091 已证），已升级客户端不会降级、回退无效。否决，改用「重发旧字节为更高版本」（决策 4）。
- **把 core 版本也纳入「每次内容发布手填 agent」** — 维持现状的手填透传，运营仍无法集中 pin / 跨发布统一 core 版本，且与「更新器版本是横切关注点」相悖。否决。
- **楔子也做版本管理 / 自更新** — 楔子被 `-javaagent` 加载、文件锁、运行时自换难（ADR-021 决策 2/3）；楔子几乎不变、纳管收益低而复杂度高。否决，楔子冻结。
- **core 真按平台分别上传三份 jar** — ADR-021 已定「一份 jar 三平台通用」，分别上传徒增运营负担与不一致风险。否决，上传一份、后端 fan-out 三键（决策 5）。
