# 产品需求文档（PRD）：JianManager

> 需求的单一真源（WHAT / WHY），也是产品的**需求登记册 + 路线图**。每个需求在 §4 加一行 FR（带优先级 + 状态），交付即标版本；**单功能的详细规格放 `docs/specs/<feature>/`，PRD 只保留「一行 FR + 状态」的索引级**。
>
> 结构遵循 SDD PRD 模板（§1-8）。演进规则见 `.claude/rules/doc-evolution.md`。

## 1. 背景与目标

JianManager 是面向中小型游戏服务器（以 Minecraft 为主）运营商的**多节点管理平台**。一个 Go + 内嵌 React 的**单二进制**部署 Control Plane，按需在各节点部署 Worker，统一管理游戏服实例的全生命周期、终端、文件、监控、告警、备份、Bot、玩家治理、业务对接与玩家客户端 OTA 分发。

**价值主张**：把分散的多机游戏服运维收敛到一个面板——单二进制、零外部依赖即可起步，规模化时一键加节点。

### 非目标
- 不做真正的多租户隔离（tenant_id）——以用户组替代（ADR-004）。
- 不引入 Node.js / Python 作为后端服务（ADR-001）；不为前端单独配 nginx（go:embed，ADR-005）。
- Worker 不直接读写数据库、不直接暴露 HTTP API 给浏览器（架构不变量）。
- V1 不含 MFA、CP 高可用、JVM 深度诊断、真多租户（见 §4 范围外表）。

## 2. 角色

| 角色 | 说明 |
|---|---|
| 平台管理员 | 管理所有用户 / 节点 / 实例 / 平台设置 / 数据库浏览 / 系统更新 |
| 组管理员 | 管理组内成员与分配给本组的实例 |
| 组成员 | 仅操作分配给自己的实例 |
| 系统角色 | Control Plane（唯一浏览器入口 + 调度）· Worker Node（进程 / 终端 / 指标）· Bot Worker（Mineflayer 子进程）· ServerProbe 探针（监控 + 业务对接 agent）· 玩家客户端 updater（OTA） |

## 3. 用户故事

- 作为运营者，我希望在一个面板创建 / 启停 / 监控多台游戏服，以便不用逐机 SSH。
- 作为运营者，我希望浏览器直连实例终端与文件、在线编辑配置，以便随时运维。
- 作为运营者，我希望节点 / 实例的指标、告警、备份、定时任务统一可见可配，以便稳定运营。
- 作为运营者，我希望一键添加节点、面板自更新，以便规模化与免维护。
- 作为运营者，我希望向玩家分发自动更新的客户端整合包，以便统一客户端版本。
- 作为运营者，我希望经统一业务对接面板治理经济 / 背包等插件数据，以便跨服业务运营。

## 4. 功能需求（FR）

> 状态取值：📋 计划 / 🔨 开发中 / ✅ 已交付@vX.Y.Z / ⏸️ 已延后 / ❌ 已废弃。
> 优先级：P0(核心) / P1 / P2 / P3——为规划用，**已交付 / 已废弃后优先级无意义记 `—`**。
> 标 `已交付` 是有门的：仅该 FR 的 spec 验收全过 + 测试 / 真机通过后，由 `sdd-release-version` 发版统一标 `已交付@vX.Y.Z`；开发中不得自标。false-done 走 `sdd-fix-bug` 归真，撤 / 推迟走 `sdd-rollback-change`。

**活跃 FR 详细规格索引**（PRD 只留索引行，详情见 spec）：
- FR-313（进程级崩溃诊断链路：环形缓冲 + 退出码快照上报持久化 + 前端崩溃诊断面板）→ `docs/specs/crash-diagnostics/spec.md`（需 spec，开发中创建）
- FR-314（实例启动同步预检：预检 RPC + 同步报错不进 STARTING）→ `docs/specs/start-preflight/spec.md`（需 spec，开发中创建）
- FR-305（反向隧道单消息尺寸上限统一治理：`ServerOptions()` 64MiB 在 grpctunnel 路径失效）→ `docs/specs/tunnel-message-size-guard/spec.md`（需 spec，开发中创建；宜随 FR-281 M1 一并处理）
- FR-304（文件上传链路流式化：client-stream UploadFile 与 DownloadFile 对称）→ `docs/specs/upload-streaming/spec.md`（实现全绿待真机）
- FR-298~301（节点运行时库：多运行时泛化 / 自动扫描 / Node.js 安装器 / Bot 接管 / 聚合刷新）→ `docs/specs/node-runtime-library/spec.md`
- FR-306~307（节点包管理器 + 多 registry + 全局包可视化管理）→ `docs/specs/node-package-management/spec.md`
- FR-308（bot-worker 下发部署 + NODE_PATH 全局依赖）→ `docs/specs/bot-worker-deploy/spec.md`
- FR-302（导入现有服务器：就地接管 / 搬迁托管区）→ `docs/specs/import-existing-server/spec.md`
- FR-128~162（控制台体验与可寻址性增强）→ `docs/specs/console-ux-enhancement/spec.md`
- FR-124~127（JBIS 背包域）→ `docs/specs/business-integration/fr-124-127-inventory.md`
- FR-046（Sponge 子服支持）→ `docs/specs/provision-sponge/spec.md`（草拟，待审核）
- FR-114（探针依赖内联 / 缓存预置）→ `docs/specs/probe-dependency-cache/spec.md`（开发中，Worker 侧缓存预置已落地，断网真机待验）
- FR-073 / 078 / 079 / 080 / 082 / 083 / 084 / 085（ServerProbe 治理桥运营底座在途）→ `docs/specs/serverprobe-ops-inflight/spec.md`
- FR-003 / 041 / 042 / 046 / 059 / 098 / 113 / 114（在途杂项 / 归真 / 延后）→ `docs/specs/inflight-backlog/spec.md`
- FR-053（插件批量部署多服）→ `docs/specs/plugin-batch-deploy/spec.md` + `api.md`
- FR-163~169（前端整体重设计:视觉底座 / 双主题 / 多级分组 / 可组合工作区+超级工作台+导播台 / 监控升级）→ `docs/specs/ui-redesign/design.md`(+ 原型 `preview.html`)
- FR-173~175（CI 发布管线 / 出站网络代理 / 自更新对接 GitHub Releases，关联 ADR-036/037）→ `docs/specs/release-pipeline/`、`docs/specs/network-proxy/`、`docs/specs/self-update-github/`（开发中创建）
- FR-176~184（节点与运行时 UI 重做 / 自更新增强 / 全局任务中心 / jmctl 紧急控制台，关联 ADR-039/040/041/042 + ADR-036 更新）→ FR-182 `docs/specs/self-update-enhancement/spec.md`、FR-183 `docs/specs/task-center/spec.md`、FR-184 `docs/specs/emergency-cli/spec.md`（已落审）；FR-176/179/180/181 免 spec；FR-177 `docs/specs/node-page-redesign/spec.md`、FR-178 `docs/specs/node-runtime-panels/spec.md`（W2，已自审）
- FR-185~190（出站代理面板化+节点级下发 / 更新页服务端缓存+markdown / 客户端分发迁运营+全流程向导重做 / 全站模态纪律+复制兜底 / 重度模态重做 / Worker 二进制 CP 下发，关联 ADR-043 + 增强 FR-174/182/086/072/009/004）→ FR-185 `docs/specs/proxy-visual-config/`、FR-186 `docs/specs/update-page-cache/`、FR-187 `docs/specs/client-dist-redesign/`、FR-189 `docs/specs/heavy-modal-redesign/`；FR-188 免 spec（规则 + 全站审计改造 + 复制兜底）；FR-190 `docs/specs/worker-binary-cp-cache/spec.md` + `docs/adr/059-worker-binary-cp-cache-distribution.md`（开发中）
- FR-191~194（客户端分发二轮重做：发布文件树+zip 上传 / 拉取密钥可查看 / updater-core 版本管理 / 端到端流程图，关联 ADR-044[FR-192 修订 ADR-022①] / ADR-045[FR-193 补充 ADR-021] + 增强 FR-187/086/091）→ FR-191 `docs/specs/client-dist-publish-redesign/`、FR-192 `docs/specs/pull-key-viewable/`、FR-193 `docs/specs/updater-core-version-mgmt/`（需 spec，开发中创建）；FR-194 免 spec（前端流程图）；BUG-D 面包屑域名 / BUG-E 就绪度 CTA 不弹窗（fix，不占 §4）
- FR-196~212（前端 mock API 与测试基座：MSW v2 内存假后端[有状态、跨 endpoint 全联动] + 双运行形态[`VITE_MOCK` 整站 mock 模式 + jsdom/@testing-library 强断言测试] + 成功默认/按需注入错误 + 实时流全仿真[WS 终端 / SSE 日志·事件·指标] + Playwright E2E + CI 门禁，关联新 ADR-047）→ FR-196/197/198/211/212 `docs/specs/frontend-mock-api/spec.md`（**需 spec**，地基三条＋域簇范式契约＋E2E/CI，开发中创建）；FR-199~210 **免 spec**（域簇机械套用 FR-196/197/198 既定范式 + 既有 `docs/API.md` / `web/src/api/*.ts` 类型契约，不引入新模型/新契约/新 ADR）；FR-211 Playwright E2E（mock 模式整站）+ FR-212 CI 前端质量门禁（PR/push 拦截 lint+vitest+E2E，扩 `release.yml` test 闸）；登录失败整页刷新 = fix 走 `sdd-fix-bug`（不占 §4），其页面级回归并入 FR-199
- BUG-A 节点重名覆盖（修 FR-004 注册身份匹配缺陷，见 ADR-039）：注册改 UUID 锚定三级匹配 + 节点名活跃唯一 + 坏节点检测/修复后端（`NodeRepairService` + `/nodes/repair/*`、`/nodes/:id/reenroll|orphans|purge-orphans`）随 ADR-039 fix 提交落地；坏节点修复**可视化入口**随 FR-177 节点页重做。属缺陷修复（非新 FR），不占 §4 FR 编号
- FR-213~221（观测体系重构 + 客户端分发观测 + 共享文件浏览器 + 通知中心：导航{监控/日志/统计}+任务中心移系统 / 站内信+告警合并通知中心 / 分发观测时序底座+监控页+统计扩维 / 文件浏览器抽取+实例卡片迁移+分发文件预览 / 平台级统计页 / 时序剖析增强，关联 ADR-048[统一通知模型] / ADR-049[分发观测聚合，复用 ADR-013] + 增强 FR-060/086）→ FR-213 `docs/specs/file-browser-component/`、FR-215 `docs/specs/observability-ia-redesign/`、FR-216 `docs/specs/notification-center/`、FR-217 `docs/specs/client-dist-observability/`、FR-220 `docs/specs/platform-statistics/`（需 spec，开发中创建）；FR-214/218/219/221 免 spec（前端复用/消费既有）；FIX-1 发布上传复验 + strict/fail-static 等术语中文化（fix 走 sdd-fix-bug，不占 §4）
- FR-222~224 + FIX-A~D（节点上线流程打通 + worker 生命周期修复 + .yml 约定）：FR-222/223 已由 0.13.0 开发版 FIX-1/2/3 归真闭环；FR-224 已在 v0.12.0 交付；FR-222 `docs/specs/worker-self-setup/`，FR-223/224 免 spec。FIX-A #2 worker 重连重推实例[+ADR-050] / FIX-B #3 终端断连 / FIX-C #4 启停 kill 竞态 / FIX-D 首次上线真机断点（fix 走 sdd-fix-bug，不占 §4）
- FR-225~232 + FIX-1~6（0.13.0 开发版归档）：调试开关 / 通知-任务联动 / 任务强停+筛选 / JDK 登记重做 / 连通性测试族 / 创建向导页 / 复制双模式 / 前端细节已交付；FIX-1/2/3 已把 FR-222/223 节点上线链路归真闭环；FR-227 `docs/specs/task-force-stop/`、FR-228 `docs/specs/jdk-register-redesign/`、FR-229 `docs/specs/connectivity-selftest/`、FR-231 `docs/specs/instance-clone-modes/`；FIX-4 JDK 下载超时 / FIX-5 JDK 登记卡死 / FIX-6 系统更新进页只读缓存为 0.13.0 修复项（fix 走 sdd-fix-bug，不占 §4）
- FR-079 + FR-233~247 + FIX-7~9（UI 重塑 + Docker 落地 + 实例规模化 2026-06-30）：FR-079/233/234/236/237/240/241/243/246/247 已在 0.13.0 开发版交付；FR-235（实例列表页重设计）、FR-244（全局动画系统）仍按 §4 状态继续跟踪。FR-235/133/137/244 本轮消费 FR-247 的服务端分页地基，规格见 `docs/specs/instance-list-redesign/spec.md`（已审核，首批实现落地）。合并关系：FR-238→FR-078，FR-239→FR-236，FR-242→FR-241，FR-245→FR-240。FIX-7 生产 SQL 日志静默 / FIX-8 创建实例节点下拉空 / FIX-9 点击实例卡片无反应为 0.13.0 修复项（fix 走 sdd-fix-bug，不占 §4）。计划见 `.tmp/brainstorm-ui-docker-batch-2026-06-30.md`
- FR-170（进程粒度监控采集）→ `docs/specs/process-metrics/spec.md` + `docs/adr/060-process-granularity-metrics.md`（开发中，首批已落地）；FR-171/172（备份校验和 / 审计分页导出）→ `docs/specs/backup-audit-backend/spec.md`（开发中，首批已落地）；FR-152（备份存储测试连接 + 容量展示）→ `docs/specs/backup-storage-test-capacity/spec.md`（开发中，首批已落地）
- FR-264（客户端分发单节点源站安全防护：多维限流 / IP 临时封禁 / key 状态机 / 频道降速保护 / 制品授权收紧 / 启动安全画像 / 独立防护中心）→ `docs/specs/client-dist-security-firewall/spec.md`（已交付@v0.13.0）
- FR-265（客户端分发观测四 Tab 重构：统计/监控/日志只看请求事件，客户端 Tab 独立看运行态与更新结果；新增运行态心跳表/端点、请求日志脱敏详情、实时聚合；清理废弃缓存命中指标；与 FR-264 并行开发互不覆盖）→ `docs/specs/client-dist-observability-rebuild/spec.md`（已交付@v0.13.0）
- FR-266（updater-core 构建元信息内嵌与展示：jar 内写版本 / Git commit / dirty / buildTime，CP 归档读取并在 Core 版本页展示，紧急 hotfix 仍可直接上传）→ `docs/specs/updater-core-build-metadata/spec.md`（已交付@v0.13.0）
- FR-267~272（高密度控制台重塑 2026-07-02）：A+C Jian 绿默认视觉底座 / 页眉节点作用域 + 侧栏 IA + 页面归位 / 服务器统一控制台 `/instances/:id` / 节点控制台与高密度服务器列表 / 平台首页高密度总览 / 全站 A+C 皮肤收口 → `docs/specs/console-redesign/design.md`（已交付@v0.13.0）；关联 ADR-055/056/057；使用既有 mock-api 完成前端原型，不新增真实后端接口。
- FR-273（通用组件包与控件博物馆）：抽出 `@jianmanager/ui` 通用 UI/token/charts 包，主应用消费组件包，并新增 `web/wiki` 控件博物馆展示第一版控件与常用状态矩阵 → `docs/specs/component-package-wiki/spec.md`；关联 ADR-058。
- FR-274（Bot 压测会话 YAML 动作编排与 50 Bot 稳定验收，增强 FR-042）→ `docs/specs/bot-stress-yaml-orchestration/spec.md`
- FR-275/276（CP↔Worker 专用 WS 令牌密钥下发 + 终端 401 诊断兜底：终端/插件桥令牌与用户会话密钥分离，经 gRPC 注册响应下发并持久化；修 FR-080 enroll 不下发密钥致生产终端 401、探针监控失效的产品缺口；修订 ADR-020，见 ADR-061）→ `docs/specs/worker-ws-token-secret/spec.md`（草拟，FR-275 开发中）
- FR-277（SSH 推送式远程部署脚本：操作机经 SSH 密钥推送部署/更新面板与节点到 Linux 主机，环境变量配置；Worker 侧复用 ADR-051 两阶段语义，服务档位 system/user 双轨；见 ADR-063）→ `docs/specs/ssh-deploy-scripts/spec.md`（开发中，剩真机验收）
- FR-281（CP→Worker 指令反向流化：gRPC 反向隧道 + 双模式 + 终端 CP 中转，修订 ADR-002 与「浏览器↔Worker WS」不变量，见 ADR-066）→ `docs/specs/worker-reverse-stream/spec.md`（已落审；M1 指令隧道 → M2 终端中转；NAT 真机验收必验）
- FR-283~288（多语言 monorepo 统一布局与命令面 2026-07-12）：`apps/`（所有外壳，含 Go/Node/React）+ `packages/`（第一方 JS 库）+ `internal/`（Go 库留根）+ `third_party/` 布局；前端提升为真 pnpm workspace（`web/`→`apps/control-plane-web`、`web/wiki`→`apps/ui-museum`、`web/packages/ui`→`packages/ui`）+ devmock 抽包 + go-task 命令面 + 前端版本接上 `version.go` 真源（ADR-065）；关联新 ADR-064（重申 ADR-005，不取代）→ 共享 spec `docs/specs/monorepo-unification/spec.md`（已落审，含 FR-284/285/286 免 spec 执行边界与 FR-287/288 设计节）。单分支串行执行中（序 283→(284,285)→286→287→288；实现排在 FR-281 反向流之后——内容改动先落、文件移动后落），计划见 `.tmp/brainstorm-monorepo-unification-2026-07-12.md`
- FR-293~297（导航与控制台缓存体验 2026-07-12：侧栏常驻服务器列[收藏置顶+最近打开] / 页眉集群统计浮窗 / 服务器控制台页签 keep-alive+终端连接管理器 / 跨服务器热缓存 / 实例域数据层调优，增强 FR-240/162/179/269；**UI mock 先行**——界面先在 MSW mock 模式下完成可演示再接真栈）→ FR-295/296 共享 `docs/specs/console-keepalive/spec.md` + 新 ADR（前端 keep-alive 与终端连接管理架构，开发中创建）；FR-293/294/297 免 spec。依赖：FR-296←FR-295。**全部 ✅ 已交付@v0.15.0**（rebase+ff-only 落 dev，真机 103.45.143.199 四栈验收；ADR-067 前端 keep-alive 与终端连接管理架构落地）；真机验收顺带修复服务器选择器模态被侧栏 transform/contain 包含块压窄（改 createPortal 到 body，见 CHANGELOG 修复段）。计划见 `.tmp/brainstorm-console-nav-cache-2026-07-12.md`
- 已交付 FR 的详情见对应 `docs/specs/<feature>/` 与 git 历史。

> **验收档位图例**：`·全真栈验收`=真 UI+真 CP/Worker+真外部进程端到端；`·四档验收`=单测/集测/单机截图/真浏览器截图（后端 mock 基底）；`·验收经 FR-XXX 覆盖`=能力面被后继 FR 重做/包含并在其验收中验证（映射依据 `.tmp/acceptance/UNMARKED-66-RECONCILE.md`）；**无后缀=交付未验收（真缺口，当前 1 个：099 需真客户端 OTA 场景）**。证据台账 `.tmp/acceptance/ACCEPTANCE-LEDGER.md`。

| 编号 | 需求 | 优先级 | 状态 |
|---|---|---|---|
| FR-001 | 用户认证（JWT 双 Token） | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-002 | 用户与权限管理（三级 RBAC） | — | ✅ 已交付@v0.1.0·验收经 FR-156 覆盖 |
| FR-003 | 用户组与配额 | — | ✅ 已交付@v0.1.0·验收经 FR-156 覆盖 |
| FR-004 | 节点注册与心跳 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-005 | 实例生命周期管理 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-006 | 守护进程（Daemon Wrapper） | — | ✅ 已交付@v0.1.0·验收经 FR-005 覆盖 |
| FR-007 | 终端实时 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-008 | 文件管理 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-009 | Bot 平台 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-010 | 监控指标 | — | ✅ 已交付@v0.13.0·全真栈验收（曾归真 404；端点修复后真机复验：节点实时指标 200 真数据 + 监控页真值） |
| FR-011 | 告警规则 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-012 | 定时任务 | — | ✅ 已交付@v0.6.0·全真栈验收 |
| FR-013 | 备份恢复 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-014 | 服务端模板 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-015 | 审计日志 | — | ✅ 已交付@v0.6.0·全真栈验收 |
| FR-016 | i18n（中英国际化） | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-017 | 首次启动引导流程 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-018 | 实例 gRPC 生命周期操作 | — | ✅ 已交付@v0.2.0·全真栈验收 |
| FR-019 | 终端 WebSocket 全链路 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-020 | 文件管理 gRPC 全链路 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-021 | Bot Mineflayer 集成 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-022 | RCON 指标采集 | — | ❌ 已废弃（ADR-016 退役 RCON） |
| FR-023 | gRPC 客户端真实实现 | — | ✅ 已交付@v0.1.0·验收经 FR-025 覆盖 |
| FR-024 | 前端对接运行时 API | — | ✅ 已交付@v0.1.0·验收经 FR-043 覆盖 |
| FR-025 | Worker→Control Plane gRPC 连通性修复 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-026 | 前端 shadcn/ui 标准化 | — | ✅ 已交付@v0.1.0·验收经 FR-163 覆盖 |
| FR-027 | API 集成测试 | — | ✅ 已交付@v0.1.0·验收经 FR-211 覆盖 |
| FR-028 | 实例创建 E2E 测试 | — | ✅ 已交付@v0.2.0·验收经 FR-034 覆盖 |
| FR-029 | Worker Node 注册与心跳集成 | — | ✅ 已交付@v0.1.0·全真栈验收 |
| FR-030 | 前端通知系统与 UX 标准化 | — | ✅ 已交付@v0.1.1·验收经 FR-183 覆盖 |
| FR-031 | 配置文件管理引擎 | — | ✅ 已交付@v0.4.0·验收经 FR-008 覆盖 |
| FR-032 | 节点资源分配与群组服关系模型 | — | ✅ 已交付@v0.4.0·全真栈验收 |
| FR-033 | JDK 与运行时管理 | — | ✅ 已交付@v0.4.0·全真栈验收 |
| FR-034 | 搭建 Bukkit 子服 | — | ✅ 已交付@v0.3.0·全真栈验收 |
| FR-035 | 搭建代理（BungeeCord/Velocity） | — | ✅ 已交付@v0.4.0·全真栈验收 |
| FR-036 | 一键复制子服 + 配置修正 + 注册 | — | ✅ 已交付@v0.4.0·全真栈验收 |
| FR-037 | 运维控制台布局 | — | ✅ 已交付@v0.3.0·验收经 FR-269 覆盖 |
| FR-038 | Bot 规模化后端 API | — | ✅ 已交付@v0.3.0·验收经 FR-009 覆盖 |
| FR-039 | 控制台实例内 Bot 管理段 | — | ✅ 已交付@v0.3.0·验收经 FR-147 覆盖 |
| FR-040 | 全局 Bot 管理页重构 | — | ✅ 已交付@v0.3.0·验收经 FR-147 覆盖 |
| FR-041 | Bot 实时遥测与单 Bot 详情面板 | P2 | ✅ 已交付@v0.13.0·验收经 FR-147 覆盖 |
| FR-042 | Bot 压测会话编排 UI | P2 | ✅ 已交付@v0.13.0·验收经 FR-274 覆盖 |
| FR-043 | 全链路运维打通（节点→实例→终端→Bot 进服） | — | ✅ 已交付@v0.3.0·全真栈验收 |
| FR-044 | 项目自包含便携运行时（FHS 数据根 + 核心缓存） | — | ✅ 已交付@v0.3.0·全真栈验收 |
| FR-045 | 制品库（内容寻址 + 完整性校验） | — | ✅ 已交付@v0.3.0·验收经 FR-044 覆盖 |
| FR-046 | Sponge 子服支持 | P2 | ✅ 已交付@v0.13.0·全真栈验收（真机验收：SpongeVanilla launcher 引导 Done 3.662s + SpongeForge installer→mods Done 2.586s，现代 Forge argfile 启动收口） |
| FR-047 | 环境/标签多维分组筛选 | — | ✅ 已交付@v0.4.0·验收经 FR-165 覆盖 |
| FR-048 | 节点维护模式与主动下线 | — | ✅ 已交付@v0.4.0·全真栈验收 |
| FR-049 | 日志持久化、归档与保留 | — | ✅ 已交付@v0.4.0·验收经全真栈日志中心战役覆盖 |
| FR-050 | 日志检索与过滤 | — | ✅ 已交付@v0.4.0·验收经全真栈日志中心战役覆盖 |
| FR-051 | 通用文件改前自动备份与版本回滚 | — | ✅ 已交付@v0.4.0·全真栈验收 |
| FR-052 | 插件/模组单服管理 | — | ✅ 已交付@v0.4.0·验收经 FR-143 覆盖 |
| FR-053 | 插件批量部署多服 | P1 | ✅ 已交付@v0.13.0·全真栈验收（真机验收：制品库资产批量部署 2 实例 succeeded 2/failed 0 + 磁盘核验 + 审计 action） |
| FR-054 | 玩家管理（RCON） | — | ✅ 已交付@v0.4.0·验收经 FR-067 覆盖 |
| FR-055 | 玩家管理插件桥增强 | — | ❌ 已废弃（ADR-014） |
| FR-056 | 增量备份 | — | ✅ 已交付@v0.4.0·四档验收 |
| FR-057 | 备份远程存储 | — | ✅ 已交付@v0.4.0·全真栈验收 |
| FR-058 | 实例批量操作 | — | ✅ 已交付@v0.4.0·四档验收 |
| FR-059 | 危险操作保护体系化 | — | ✅ 已交付@v0.10.0·四档验收（复验发现服务器控制台与工作区强杀直发接口未过 DangerConfirm，已修：两处入口统一接危险确认 scope=group；真机复验通过：控制台强杀弹确认框、取消 0 请求、确认才发唯一 kill 且真 java 进程真停不误伤） |
| FR-060 | 时序监控与历史曲线 | — | ✅ 已交付@v0.5.0·全真栈验收（真 ServerProbe 复验：world_entities/world_tile_entities 三方数据吻合[/metrics=/series 末值=server-state]，监控页多序列世界实体/方块实体曲线真数据渲染） |
| FR-061 | 面板信息密度与视觉改造 | — | ✅ 已交付@v0.5.0·四档验收 |
| FR-062 | 节点负载（load average）采集与仪表盘 | — | ✅ 已交付@v0.5.0·四档验收 |
| FR-063 | 平台设置（全量平台配置可视化与运行时调整） | — | ✅ 已交付@v0.6.0·四档验收 |
| FR-064 | 模板管理 UI 与模板删除 | — | ✅ 已交付@v0.6.0·验收经 FR-154 覆盖 |
| FR-065 | 实时插件桥通道地基（探针反向 WS ↔ Worker） | — | ✅ 已交付@v0.7.0·验收经 FR-114 覆盖 |
| FR-066 | 实时玩家事件 + 精确跨服感知 | — | ✅ 已交付@v0.7.0·验收经 FR-146 覆盖 |
| FR-067 | 玩家治理迁到探针 + 退役 RCON 全链路 | — | ✅ 已交付@v0.7.0·验收经 FR-146 覆盖 |
| FR-068 | 探针在线更新（推送即就位 + 下次重启生效） | — | ✅ 已交付@v0.7.0·全真栈验收（真机复验发现运行中实例 Windows classpath 锁致更新 422，已修：同内容跳过 + 锁定降级 + Windows 独占锁自动化回归；真机复验通过：运行中 paper-probe[jar 被 java 进程锁]在线更新返回 200 非 422、deployed:true·restarted:false·probeConnected:true「探针 jar 已就位下次重启生效」，运行实例未被打扰） |
| FR-069 | 实例导航与侧栏树形优化 | — | ✅ 已交付@v0.7.0·验收经 FR-165 覆盖 |
| FR-070 | 文件管理资源管理器化 + 编辑器基础 + Ctrl+S 历史 | — | ✅ 已交付@v0.7.0·验收经 FR-008 覆盖 |
| FR-071 | 配置管理资源管理器化 + 自动发现全部配置 | — | ✅ 已交付@v0.7.0·验收经 FR-008 覆盖 |
| FR-072 | 创建/编辑模态框统一 | — | ✅ 已交付@v0.7.0·验收经 FR-159 覆盖 |
| FR-073 | 编辑器迷你 IDE 增强（CodeMirror） | P2 | ✅ 已交付@v0.13.0·全真栈验收（CodeMirror 编辑/保存/搜索面板/历史回滚真机验，属 FR-141 编辑器核心） |
| FR-074 | 跨文件全文搜索与持久倒排索引 | — | ✅ 已交付@v0.9.0·验收经 FR-113 覆盖（复验发现配置管理入口搜索命中只开文件不定位行，已修：configMode 透传 gotoLine/gotoNonce 至配置编辑器并强制文本模式；真机复验通过：真 server.properties 搜 enable-rcon 命中定位到第 12 行、表单模式点命中强制切回文本并定位） |
| FR-075 | 归档浏览与反编译 | — | ✅ 已交付@v0.9.0·全真栈验收 |
| FR-076 | 全量 Bukkit 状态探查（异步非侵入）+ WS 按需查询 | — | ✅ 已交付@v0.9.0·验收经 FR-142 覆盖（真 Paper+真 ServerProbe 复验：server-state 端点经反向 WS 桥返回真六分区 server/worlds/jvm/**classloader 计数22791/23049/258+加载器层级**/scheduler/listeners，采集期 TPS 稳定 20.0 无可感下降） |
| FR-077 | 「服务器状态」专属 tab | — | ✅ 已交付@v0.9.0·验收经 FR-142 覆盖（真机复验：服务器状态卡渲染真 Bukkit 六分区+classloader 专区真数据[真 JDK21 层级链]，手动刷新按需生效[时间戳更新、实体数实时变化]、默认不轮询[停留数分钟仅首次+手动 2 次请求]） |
| FR-078 | Docker 容器化实例运行 + 镜像管理 + 端口映射 | P1 | ✅ 已交付@v0.13.0·全真栈验收 |
| FR-079 | 实例级资源限额（Docker 模式） | P1 | ✅ 已交付@v0.13.0·全真栈验收 |
| FR-080 | Worker 一键安装 / 傻瓜部署 | P1 | ✅ 已交付@v0.13.0·全真栈验收 |
| FR-081 | 面板自更新（CP/Worker 二进制在线升级） | — | ✅ 已交付@v0.9.0·验收经 FR-182 覆盖 |
| FR-082 | 运行时与制品全局页（JDK + 制品库，可视化） | P2 | ✅ 已交付@v0.13.0·全真栈验收 |
| FR-083 | 平台存储资源管理器（数据根 FHS 浏览）+ FR-044 完善 | P2 | ✅ 已交付@v0.13.0·全真栈验收 |
| FR-084 | 数据库资源管理器（只读浏览） | P2 | ✅ 已交付@v0.13.0·验收经全真栈 DB 浏览战役覆盖 |
| FR-085 | 告警体系全面增强（多通道 + 多类型 + 分级聚合 + 确认历史） | P1 | ✅ 已交付@v0.13.0·验收经 FR-011 覆盖 |
| FR-086 | 客户端分发频道与拉取密钥 | — | ✅ 已交付@v0.8.0·验收经 FR-192 覆盖 |
| FR-087 | 签名 manifest 端点（latest-only）+ 客户端制品分发 | — | ✅ 已交付@v0.8.0·验收经 FR-265 覆盖 |
| FR-088 | 客户端版本发布 + latest 指针 | — | ✅ 已交付@v0.8.0·验收经 FR-191 覆盖 |
| FR-089 | javaagent 楔子 jar（自定位 + 引导 + fail-open） | — | ✅ 已交付@v0.8.0·验收经 FR-258 覆盖 |
| FR-090 | updater-core.jar — reconcile 核心（文件级增量，CAS 部分被 FR-256 废弃） | — | ❌ 已废弃（见 ADR-054） |
| FR-091 | updater-core 自更新 + 客户端 N-1 回退（自更新上移到楔子 FR-258） | — | ❌ 已废弃（见 ADR-054） |
| FR-092 | 客户端机器码身份（仅追踪/统计） | — | ✅ 已交付@v0.8.0·验收经 FR-265 覆盖 |
| FR-093 | 发布/拉取全链路审计与追踪 | — | ✅ 已交付@v0.8.0·验收经 FR-265 覆盖 |
| FR-094 | 客户端遥测上报 | — | ✅ 已交付@v0.8.0·验收经 FR-249 覆盖 |
| FR-095 | 分发统计后台 | — | ✅ 已交付@v0.8.0·验收经 FR-213 覆盖 |
| FR-096 | 分发端点应用层（L7）防护 | — | ✅ 已交付@v0.8.0·验收经 FR-264 覆盖 |
| FR-097 | 自有 `.jmpack` 打包（压缩 + 签名，格式删除） | — | ❌ 已废弃（见 ADR-054） |
| FR-098 | 块级二进制 diff 增量发布 | P2 | ✅ 已交付@v0.13.0·验收经 FR-251 覆盖 |
| FR-099 | 客户端 OTA 更新进度窗口（进度条 + 速度 + ETA） | — | ✅ 已交付@v0.8.0·端到端真机复验：updater-core（Java8）真下载 + reconcile + sha256 完整性 + 文件级增量/托管区减量/防降级；进度模型 ProgressModel/ProgressView 11 单测绿、SwingProgressView 真桌面（非 headless）代码路径无崩溃。**原生 Swing 弹窗的人眼目视（动条+实时速度/ETA）= env-blocked**：原生 OS 窗口不可经浏览器工具截图 + 本地下载过快无可捕捉中间帧，需真启动器 `-javaagent:wedge.jar` 场景人肉/录屏确认 |
| FR-103 | 插件桥（Bukkit/BC 插件 WS 连入，旧自写） | — | ❌ 已废弃（ADR-014） |
| FR-107 | 后台客户端更新器接入指引 | — | ✅ 已交付@v0.8.0·验收经 FR-187 覆盖 |
| FR-108 | 仪表盘总览环分级配色与负载量纲修正 | — | ✅ 已交付@v0.9.1·验收经 FR-062 覆盖 |
| FR-109 | 服务器状态页显示打磨 | — | ✅ 已交付@v0.9.1·验收经 FR-142 覆盖 |
| FR-110 | 系统更新页未配源仍展示当前版本 | — | ✅ 已交付@v0.9.1·验收经 FR-186 覆盖 |
| FR-111 | 归档/反编译查看器布局优化 | — | ✅ 已交付@v0.9.1·全真栈验收 |
| FR-112 | 平台/运维导航信息架构统一 | — | ✅ 已交付@v0.9.1·验收经 FR-268 覆盖 |
| FR-113 | 全文索引后台化与进度 | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-114 | 探针依赖内联/缓存预置 | P3 | ✅ 已交付@v0.13.0·全真栈验收（真机验收：libraries+assets 缓存包随建服下发 6.3MiB，探针首启免联网拉主依赖） |
| FR-115 | 业务桥 Worker 脊柱（JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-122 覆盖 |
| FR-116 | CP 业务编排与汇聚脊柱（JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-122 覆盖 |
| FR-117 | ServerProbe 业务对接层骨架（JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-122 覆盖 |
| FR-118 | 经济 Provider（只读，JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-122 覆盖 |
| FR-119 | 业务掌控台 UI v1（manifest 驱动，JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-123 覆盖 |
| FR-120 | 经济 Provider（写，JBIS M2） | — | ✅ 已交付@v0.10.0·验收经 FR-123 覆盖 |
| FR-121 | 业务写横切硬化（幂等 + 二次确认 + 审计，JBIS M2） | — | ✅ 已交付@v0.10.0·验收经 FR-123 覆盖 |
| FR-122 | 经济汇聚与多区聚合（JBIS M2） | — | ✅ 已交付@v0.10.0·四档验收 |
| FR-123 | 经济定制页（JBIS M2） | — | ✅ 已交付@v0.10.0·四档验收 |
| FR-124 | 扩 AllinInventorySync api 导出读写门面 | P2 | ✅ 已交付@v0.14.0·真机验收：CoreLib serverInfo 解锁（黄金 config + MySQL/Redis）后经真 AIS 2.1.0 api 读 view（getPlayerInventory 真背包，含离线）+ 基础属性写（getInventoryWriteApi.writeBasicAttrs 带 WriteResult 回执）真机验通；物品写门面按 ADR-0017 降级不在范围 |
| FR-125 | 背包 Provider | P2 | ✅ 已交付@v0.14.0·真机验收：三插件（CoreLib 1.3.0 + AIS 2.1.0 + ServerProbe）同栈起通，view 返真背包（EMERALD×9…含 nbtBase64）+ writeBasicAttrs 精确生效且幂等 + TrackedItemActionEvent（JOIN_CARRY）订阅汇聚；过程修复探针解码不符嵌套契约写死玩家 bug（c6f287a / ServerProbe 8b5974b） |
| FR-126 | 背包汇聚与存储 | P2 | ✅ 已交付@v0.14.0·真机验收：writeBasicAttrs → JM business.write 审计 + AIS EDIT_ATTRS；追踪事件 JOIN_CARRY 按 domain+dedupKey 汇聚落 business_events（id=1，node/instance 归属正确，payload 全对）；业务事件读视图实例级权限收敛已落地 |
| FR-127 | 背包定制页 | P2 | ✅ 已交付@v0.14.0·真机验收：背包定制页（/super 背包卡）真背包快照 + 基础属性写二次确认端到端；业务掌控台 object 型入参下发修复（8b568d3，真机复验 writeBasicAttrs 嵌套 base/edited 下发 success） |
| FR-128 | 导航与视图状态可寻址化 + 滚动位置恢复 | P1 | ✅ 已交付@v0.13.0·全真栈验收（真机验收：节点/实例 ?tab= URL 直达全程使用；滚动恢复重试制加固带回归） |
| FR-129 | 实例工作区分屏面板化 | — | ❌ 已废弃（并入 FR-166 可组合卡片画布） |
| FR-130 | 文件与配置合并为统一资源面板 | — | ❌ 已废弃（并入 FR-166 资源卡） |
| FR-131 | 侧边栏可折叠图标轨 + 隐藏滚动条 + 布局持久化 | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-132 | 侧栏底部控件图标化（主题/语言/退出）+ 三态直选 + 底部布局 | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-133 | 实例树搜索/虚拟化/折叠保留/激活态/空态/a11y | P2 | ✅ 已交付@v0.13.0·全真栈验收（分组树 role=tree+树搜索过滤+空态+a11y roving tabindex+orgView URL 化真机验） |
| FR-134 | 统一页头与面包屑组件 | P3 | ✅ 已交付@v0.10.0·四档验收 |
| FR-135 | 开源许可与依赖清单页（独立页，照参考图布局） | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-136 | 实例列表汇总头 + 节点/端口列 + 角色徽标 + proxy↔backend inline | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-137 | 实例列表搜索/排序 + 筛选吸顶可折叠 + 分组单表 | P2 | ✅ 已交付@v0.13.0·全真栈验收（q/sort/order/pageSize URL↔UI 双向绑定+筛选折叠真机验） |
| FR-138 | 单实例操作可发现性与反馈 | — | ✅ 已交付@v0.10.0·四档验收 |
| FR-139 | 批量操作增强 | — | ✅ 已交付@v0.10.0·四档验收 |
| FR-140 | 终端体验增强 | P2 | ✅ 已交付@v0.13.0·全真栈验收（重连/全屏/字号/搜索计数+走查修复：重连一次性 token 重取[e23361d]、空闲保活 ping/pong[81ad8a9/5a9e8e9]经反代 A/B 验通） |
| FR-141 | 资源管理器与配置编辑器增强 | P2 | ✅ 已交付@v0.13.0·全真栈验收（编辑器/目录树/搜索/版本 diff+走查修复：diff 增删红绿着色[4e7593f] 真机验） |
| FR-142 | 详情页监控与探针增强 | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-143 | 插件/模组/资源包/数据包管理增强 | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-144 | 节点页直观化 | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-145 | 群组管理可寻址双栏 + proxy↔backend 拓扑 | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-146 | 玩家管理增强 | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-147 | Bot 规模化管理增强 | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-148 | 趋势图增强 | — | ✅ 已交付@v0.10.0·四档验收 |
| FR-149 | 告警增强 | — | ✅ 已交付@v0.10.0·四档验收 |
| FR-150 | 日志中心增强 | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-151 | 备份页增强 | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-152 | 备份存储测试连接 + 容量展示 | P2 | ✅ 已交付@v0.13.0·全真栈验收（真实端点经 MinIO SigV4 集测 TestS3_RealMinIO 验） |
| FR-153 | 计划任务增强 | — | ✅ 已交付@v0.10.0·四档验收 |
| FR-154 | 模板应用到实例 + 变量填充预览 | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-155 | 平台资产与更新页增强 | P2 | ✅ 已交付@v0.13.0·四档验收（被引用制品禁删、拉取密钥过期预警、DB 敏感列防护、系统更新页 i18n/强确认、制品导入下载进度[axios onUploadProgress]+JDK 导入进度[任务中心]、全网升级金丝雀分批[canary/batch/失败即中止，rollout 阶段+批次进度]全部落地） |
| FR-156 | 用户与组管理能力补齐 | — | ✅ 已交付@v0.10.0·四档验收 |
| FR-157 | 认证体验增强 | — | ✅ 已交付@v0.10.0·四档验收 |
| FR-158 | 设置与审计页增强 | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-159 | 共享对话框统一 | P2 | ✅ 已交付@v0.13.0·四档验收（所有创建/编辑/配置表单已迁共享 Radix Dialog + scrollable 壳；收敛完成，合规例外：命令面板/服务器选择器/终端全屏 3 处 `fixed inset-0` 均为非表单浮层，ui-modals 明文豁免） |
| FR-160 | 共享基件统一（ref 重构） | P2 | ✅ 已交付@v0.13.0·四档验收（原生 confirm=0、原生 table 已归共享 Table[FR-195]、控制台状态徽标统一；本轮补齐标准动作/链接按钮改共享 Button[7 文件 17 处]+Bot/封禁硬编码状态色改 status token；收敛完成，例外保留 tab 下划线/分段 pill/复合可点行/面包屑/终端与浮层内部） |
| FR-161 | 全局响应式与防翻屏基线 | P2 | ✅ 已交付@v0.13.0·四档验收（响应式页壳 jm-page-* 覆盖 ~19 页、高基数列表虚拟化、共享 Table 内建 overflow-x-auto 表内滚动防翻屏、复杂模态→可寻址双栏[NetworksPage]已就位；收敛确认，无裸横向溢出待修） |
| FR-162 | 全局页眉/顶栏（基础信息 + 搜索占位） | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-163 | 视觉底座与设计系统（统一 Panel/StatCard 组件、弃 shadcn Card 松散用法、靛蓝圆角灵动 + 响应式基线） | P1 | ✅ 已交付@v0.10.0·四档验收 |
| FR-164 | 全局双主题（靛蓝 / 青绿）+ 明暗模式（CSS 变量驱动 + 一处切换全站 + 持久） | P1 | ✅ 已交付@v0.10.0·四档验收 |
| FR-165 | 实例多级嵌套分组（前端树+列表+拖拽+批量标记+筛选；后端分组树 parent_id 自引用 + 实例-组 M:N） | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-166 | 可组合卡片工作区（单实例可拖拽画布 + 6 功能卡 + 预设个人级持久化；取代 ADR-030，并入 FR-129/130） | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-167 | 跨实例超级工作台（任意实例卡片拼合 + 实例库拖拽） | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-168 | 工作区导播台（多路 WS 预热 + 瞬切 + 定时轮播 + 并发上限 + 非激活降频） | P2 | ✅ 已交付@v0.10.0·四档验收 |
| FR-169 | 监控页升级（平台/节点/实例 6 指标 + 每图时间筛选 + brush 拖拽轴 + hover 浮窗 + 实时，扩 FR-060/061） | P1 | ✅ 已交付@v0.10.0·四档验收 |
| FR-170 | 进程粒度监控采集（Worker 采每进程 CPU/内存/IO + CP 存储 + 监控页 hover 进程 TOP10）——FR-169 拆出 | P2 | ✅ 已交付@v0.13.0·全真栈验收（真机采集经 WSL Linux worker 真进程 pid CPU/内存/IO 验） |
| FR-171 | 备份完整性校验和（model.Backup 加 checksum + 创建时计算 + 列表/详情展示）——FR-151 拆出 | P3 | ✅ 已交付@v0.13.0·四档验收（远程校验经 verifyBackupChecksum 单测含篡改检出 + 真 S3 字节一致性 TestS3_RealMinIO 传递证 + 恢复路径校验） |
| FR-172 | 审计日志服务端分页与导出（GET /audit 加分页 + 总数 + 导出 endpoint）——FR-158 拆出 | P3 | ✅ 已交付@v0.13.0·四档验收 |
| FR-173 | CI/CD 发布管线（GitHub Actions：push→滚动预发布[取 CHANGELOG Unreleased]、tag→正式 release[取版本段]；交叉编译 CP+Worker linux/amd64+windows/amd64 含 go:embed 前端；产物 + checksums.txt 上传 release；ldflags 注入版本，见 ADR-036） | P1 | ✅ 已交付@v0.11.0·四档验收 |
| FR-174 | 出站网络代理（CP/Worker 每进程可配 HTTP/SOCKS5 + no_proxy，共享出站 HTTP 客户端工厂，覆盖自更新/JDK/服务端 jar/CFR/GitHub API 下载；留空=直连，见 ADR-037） | P1 | ✅ 已交付@v0.11.0·四档验收 |
| FR-175 | 自更新对接 GitHub Releases（增强 FR-081：原生 GitHub Releases API 解析 + stable/prerelease 渠道 + checksums sha256 校验 + 经 FR-174 代理下载；取代 ADR-020 §4 feed 立场，见 ADR-036） | P1 | ✅ 已交付@v0.11.0·四档验收 |
| FR-176 | 全局交互细节修正（卡片 hover 不位移 + 输入焦点态收敛 + 主题化滚动条；增强 FR-163） | P2 | ✅ 已交付@v0.11.0·四档验收 |
| FR-177 | 节点管理页重做（主从双栏 + 左列表可收缩 + 节点页眉重设计 + JDK/制品/端口入口重排；增强 FR-144） | P1 | ✅ 已交付@v0.11.0·四档验收 |
| FR-178 | 节点内 JDK + 制品管理面板（抽屉取代简陋模态 + JDK 异步安装/进度日志接任务中心 + foojay 多厂商多版本 + 目录选择器 + 抽屉 UX 约束 + 节点制品缓存秒建免重下；增强 FR-033/045，关联 FR-082/183） | P1 | ✅ 已交付@v0.11.0·四档验收 |
| FR-179 | 全局页眉重设计 + 搜索右对齐（增强 FR-162） | P2 | ✅ 已交付@v0.11.0·四档验收 |
| FR-180 | 实例工作区页眉重设计（增强 FR-069/166） | P2 | ✅ 已交付@v0.11.0·四档验收 |
| FR-181 | Logo 点击折叠/展开导航栏（增强 FR-131） | P3 | ✅ 已交付@v0.11.0·四档验收 |
| FR-182 | 自更新体验增强（检查更新展示更新内容 + 一键回滚上一版 + 单版本二进制备份；CP+Worker，增强 FR-081） | P1 | ✅ 已交付@v0.11.0·四档验收 |
| FR-183 | 全局任务中心 + 完成站内信（job 模型 + Worker→CP 进度/日志上报 + CP API + 前端任务中心 + 完成/失败站内信；JDK 异步安装首个接入，见 ADR-040） | P1 | ✅ 已交付@v0.11.0·四档验收 |
| FR-184 | jmctl 紧急控制台 CLI（独立轻量二进制 `cmd/jmctl/`，仅依赖 daemon 帧协议包，直连守护进程 Unix Socket/命名管道；emergency/list/stop/kill + UUID 前缀补全，见 ADR-041 与 `docs/specs/emergency-cli/spec.md`） | P2 | ✅ 已交付@v0.11.0·四档验收 |
| FR-185 | 出站代理可视化配置（设置面板配全局/CP 代理 + 节点级覆盖经 gRPC 下发 Worker 运行时重建出站客户端，免登机器改 yaml/重启；优先级 DB 覆盖 > yaml > 环境变量；增强 FR-174，见 ADR-043） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-186 | 系统更新页服务端缓存 + Markdown 更新日志（CP 持久化上次检查结果，进页即显 + 后台静默刷新、刷新失败保留旧缓存；react-markdown/GFM 渲染 release body 取代 `<pre>` 纯文本；增强 FR-182） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-187 | 客户端分发迁「运营」域 + 全流程向导重做（就绪度步骤器 + 空状态引导卡 + 发布版本向导分步 + 建频道/密钥全模态化；后端 API 不变、纯前端重做；增强 FR-086 线） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-188 | 全站模态框纪律（立 `.claude/rules/ui-modals.md`：禁「点击新增→内联展开/布局重排表单」、强制内容自适应模态复用 FR-072；全站违规页审计改造 + 复制按钮 HTTP 非安全上下文兜底[共享 util]，行为不变） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-189 | 重度模态重做（创建实例 + 添加节点：自适应壳修溢出 + 基本/启动/高级 分区或 Tab + Docker 字段条件显隐 + AddNode「自动安装/手动连接」Tab；增强 FR-009/FR-004 对话框，遵循 ui-modals） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-190 | Worker 二进制由 CP 代理缓存下发（CP 经出站代理拉「与自身同版本」worker 资产→缓存→LAN 下发；装机脚本与 UpgradeNode 改走 CP，解内网无 GitHub + latest 与 CP 版本错位） | P1 | ✅ 已交付@v0.13.0·四档验收（真机内网经 WSL 第二机验：无 GitHub 节点从 CP 下载 install-worker.sh + worker 二进制上线） |
| FR-191 | 客户端分发发布/上传/预览定向重做（发布/上传改**独立页面**[非模态，根治点外面关闭丢上传草稿]+上传即锁定文件内容；支持上传 zip 自动按包内结构编排目录；配置/审阅/详情改 Minecraft 文件树预览，内容只读、仅可编排路径/目录/sync/platform；增强 FR-187/088） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-192 | 拉取密钥可查看/可编辑（密钥可逆加密存储[env 密钥]，管理员频道页查看明文+复制+**手动设/改密钥值**；**删「轮换」**[换值会断已分发客户端]、**保留「吊销」但强警告二次确认**；老哈希密钥不可查；修订 ADR-022 决策①，见 ADR-044） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-193 | updater-core 默认随 CP 内嵌静默驱动（楔子+updater-core 用 CP 自带默认版本，manifest `agent.core` 由 CP 内嵌 updater-core 自动产出；**运营不上传/不管理**，删「更新器版本」管理页；CP 自更新时默认更新器随之更新；增强 FR-091/FR-107，见 ADR-045[改写为 CP 默认]） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-194 | 客户端分发页内嵌端到端流程图（运维向大白话：首次发布 / 日常更新两段 + 密钥不可丢/整合包只发一次/楔子固定核心可换 要点；纯前端，增强 FR-187） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-195 | 全站原生 `<table>` → 共享 Table + 行卡片审计（12 组件/~20 表统一共享 Table；节点 JDK/制品缓存改行卡片⇄网格切换 + 筛选；版本号/状态/类型/来源统一徽章；统计类改条形/紧凑列表；引用矩阵与 changelog 表仅调主题色；纯前端换皮、行为不变，性质同 FR-188） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-196 | 前端测试与 mock 运行基座（接入 MSW v2 + jsdom/@testing-library/react；vitest 双环境[保留 node 纯逻辑、新增 jsdom 组件]；`VITE_MOCK` 浏览器 mock 模式开关[setupWorker + 入口条件挂载]；render harness[QueryClient/Router/i18n/theme]；新 ADR-047 前端测试与 mock 架构） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-197 | 有状态内存假后端核心 + 错误注入框架（实体 store=Map+seed+reset、跨实体联动领域层、统一鉴权中间件[token 校验]；按 endpoint 运行时注入 401/403/404/409/500/空/网络错误/延迟；per-domain handler/seed 自动聚合防并行冲突；关联 ADR-047） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-198 | 实时流仿真基座（WS 终端 PTY 伪交互[输入→回显假输出]、SSE 日志/事件/指标持续推送、可注入数据源；mock 模式与测试两用；关联 ADR-047） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-199 | mock 域簇·身份访问（auth/setup/users/groups/audit 有状态 handler + Login/Setup/Users/Groups/Audit 页面强断言测试；含「登录失败不刷新页面」回归） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-200 | mock 域簇·节点与运行时（nodes/nodeRuntime/nodeRepair/jdks/runtimeAssets/selfUpdate handler + Nodes/RuntimeAssets/SystemUpdate 强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-201 | mock 域簇·实例核心（instances/instanceGroups/serverState/ports handler + Instances/InstanceDetail/Overview 强断言测试，创建/启停→列表联动） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-202 | mock 域簇·供给与模板（provision/clone/templates handler + Templates 与创建/克隆流程强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-203 | mock 域簇·群组服网络（networks/registrations/proxy handler + Networks 页 M:N 注册联动强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-204 | mock 域簇·文件与归档（files/fileVersions/storage/archive handler + 文件管理器/Storage 强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-205 | mock 域簇·配置与数据库（configs/db handler + 配置浏览器/Database 强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-206 | mock 域簇·插件玩家经济（plugins/probe/players/economy/business handler + Players/插件管理/业务经济台强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-207 | mock 域簇·备份与计划（backups/backupStorages/schedules handler + Backups/BackupStorages/Schedules 强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-208 | mock 域簇·可观测与日志（metrics/alerts/events/notifications/tasks/logs handler + SSE 流接 FR-198 + Monitoring/Alerts/Tasks/Logs/Dashboard 壳强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-209 | mock 域簇·Bots 与终端（bots/terminal handler + WS 终端接 FR-198 + Bots/InstanceDetail 终端强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-210 | mock 域簇·客户端分发与平台设置（clientChannels/clientVersions/clientStats/licenses/settings handler + ClientChannels/Licenses/Settings 强断言测试） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-211 | Playwright E2E（基于 mock 模式整站，无需真后端）：Playwright 安装 + 配置（webServer 起 `VITE_MOCK=1`）+ 关键路径 E2E 场景（登录→导航→实例创建/启停等跨页流），与 vitest 组件测互补 | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-212 | CI 前端质量门禁（PR 拦截）：新增 `.github/workflows/ci.yml`（on pull_request/push）跑 web lint + vitest(node+dom) + Playwright E2E，任一失败阻断；并把 E2E 加入 `release.yml` 既有 test 闸（lint/vitest 已在该闸，FR-173/ADR-036） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-213 | 共享「文件浏览器」前端组件抽取（目录树/列表 + 内容预览[文本/配置/json 语法高亮] + 下载），实例「资源卡片」文件管理迁移到该共享组件（行为不变）；为客户端分发文件预览（FR-214）提供底座（**需 spec**：组件 props/接口契约、预览类型与降级） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-214 | 客户端分发文件预览：发布页已上传文件 + 版本详情历史文件树，复用 FR-213 共享文件浏览器预览内容与结构（前端复用为主，补一个管理面 JWT 只读制品内容/下载端点——玩家拉取密钥端点与浏览器入口物理隔离不可复用，免 spec，依赖 FR-213） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-215 | 观测导航重构：「监控」大类改名「观测」，下设 监控/日志/统计 三子类；任务中心移到「系统」；纯 IA/路由/侧栏调整、页面内容不变（**需 IA spec**；与在飞 FR-208/210/211 前端测试基座并行协调，测试按新 IA 写） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-216 | 通知中心：站内信（定向消息）+ 告警（系统警报）合并为统一通知流，页眉单铃铛入口（下拉预览）+ 独立「通知中心」页（按类型筛选/已读/查询），侧栏置「系统/账户与审计」（**需 spec + ADR-048 统一通知模型**，依赖 FR-215 落点） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-217 | 客户端分发观测数据底座：定时聚合客户端遥测（FR-093 events）为时序快照——拉取/更新次数、活跃客户端（machineId 去重）、版本分布与滞后、更新成功率/fail-static、下载字节、平台分布；新快照表 + 定时聚合任务 + 查询端点（总 + 按频道/时间筛选）（**需 spec + ADR-049，复用 ADR-013 分级降采样思路**） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-218 | 观测·客户端分发监控页：消费 FR-217 数据出时序趋势 + 分布/榜单，总览 + 可筛单频道（前端消费既有端点，免 spec，依赖 FR-217） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-219 | 客户端分发频道工作台「统计」Tab 扩充维度：复用 FR-217 数据加 活跃客户端/版本分布/更新成功率/平台分布 等（增强既有频道统计，免 spec，依赖 FR-217） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-220 | 观测·统计页补齐：观测三子类之一的「统计」页做成平台级聚合统计（节点/实例/玩家/分发等概览聚合，独立于 FR-219 的频道统计 Tab）（**需 spec**，依赖 FR-215） | P1 | ✅ 已交付@v0.12.0·四档验收 |
| FR-221 | 观测·监控时序剖析增强（**增强 FR-060**，非净新——节点/实例时序底座 ADR-013 已交付）：在既有三档降采样时序上加 多指标对比/叠加、下钻（节点→实例→世界）、自定义聚合粒度、关键指标概览，让全站指标更精准剖析（免 spec，依赖 FR-215） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-222 | worker 免配置自启 setup（节点上线丝滑化，参考 GitHub Actions Runner 下载/上线分离）：worker 启动时若未配置（无 .yml / 无 `etc/node-identity.json`）→ 进入 setup（有 TTY 交互式问 CP gRPC 地址 / enroll token / 节点名；无 TTY/CI 读命令行参数 + env，无人值守）写 worker.yml + 携 enroll token 注册持久化身份 → 转 run；已配置直接 run。「下载」（取二进制）与「上线」（setup+注册+run）解耦（**需 spec + ADR-051**，改写 ADR-020「一键单脚本下载+配置+注册+起」模型；依赖 FR-224 .yml） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-223 | 节点安装脚本重构（配合 FR-222）：检测当前目录已有完整 worker 二进制则跳过下载；「下载」与「上线」分两步；脚本调 worker setup（传参/env）完成配置+注册+可选常驻服务（增强 FR-080/ADR-020，免 spec，依赖 FR-222） | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-224 | 配置文件 .yml 约定统一（.yaml→.yml）：control-plane.yaml→control-plane.yml、worker.yaml→worker.yml + viper 搜索路径 + docker-compose + 安装脚本 + 样例 + docs 同步 + 改 `.claude/rules/config-files.md` 规则；.yml 为准、可选 .yaml 兼容回退不破存量（ref，免 spec） | P2 | ✅ 已交付@v0.12.0·四档验收 |
| FR-225 | 调试模式开关（设置项，热重载）：默认 Gin release 静默 + log info；设置「调试模式」开关开=log debug+Gin debug、关=info+release，走 FR-063 运行时机制即时生效不重启（增强 FR-063，免 spec） | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-226 | 通知中心快捷跳转 + 任务中心联动 + 页眉任务进度：任务类通知（含 JDK 失败）点击跳任务中心对应任务；页眉放任务中心入口 + 在跑任务进度；完成/失败站内信可一键跳（复用既有 Notification.TaskID，增强 FR-216/183，免 spec） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-227 | 任务中心强制停止 + 筛选查询：强制停止=经 gRPC 真中断 Worker 长任务（取消下载 + 清临时文件）+ 任务转终态（新 canceled 态）；列表按 kind/state/node/时间/关键词筛选（增强 FR-183，需 spec：状态机 + 跨进程取消 + proto） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-228 | JDK 登记体验重做：模态目录/文件选择器选 java 可执行文件 → 后端 java -version 探测自动填 vendor/major/version/arch（免手填）；「标记为 Worker 托管」复选框置顶并决定选择器默认根（面板目录/节点目录）（增强 FR-178/033，需 spec：worker java 探测 RPC + 选择器选文件 + 跨模块） | P1 | ✅ 已交付@v0.13.0·全真栈验收 |
| FR-229 | 连通性自检端点族 + 测试按钮：出站代理（设置面板）、JDK 下载源/镜像（运行时）、节点存活（JDK 一键下载前先测、不通即提示不卡死）统一一族测试连通性能力（增强 FR-185/178，需 spec：端点族契约 + 经出站客户端语义） | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-230 | 创建实例独立向导页：创建实例改独立分步向导页（非模态）+ 每步大白话提示，复用既有 create API；与 FR-189 协调（创建出重度模态、改走页，编辑等仍用模态）（增强 FR-005/189，免 spec） | P1 | ✅ 已交付@v0.13.0·全真栈验收 |
| FR-231 | 复制实例 高级/快速 双模式：快速复制=核心 jar + plugins/ + 根配置（server.properties 及根 *.yml/*.properties）；高级复制=目录选择器 + 用户包含/排除筛选；扩 CloneWorkDir 支持 include/选择性复制（增强 FR-036，需 spec：复制语义 + 筛选 + proto） | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-232 | 前端交互细节集：节点页进入默认选中第一个节点；页眉刷新图标刷新当前页数据（非整页 reload）（增强 FR-177/179，免 spec） | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-233 | 实例配置随时编辑：实例详情/工作区提供配置编辑（启动命令 / JVM 参数 / JDK / 环境变量 / 资源限额 / 自动重启等），保存即持久化并提示重启生效；今 `InstanceDetailPage` 仅深链跳画布、无编辑能力（feat，需 spec：新 PATCH /instances/:id 契约 + 跨 CP/web、worker 重读配置） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-234 | 创建实例向导优化：启动命令预填可改示例（如 `java -Xmx2G -jar server.jar nogui`）+ 提示 jar 名/放置位置 + 彻底隐藏工作目录（系统分配，对齐 ADR-007/008）（增强 FR-230，免 spec） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-235 | 实例列表页重设计（面向 1000+ 实例）：卡片 + 列表重做（信息密度 / 美观 / 点击进详情），大规模下虚拟化 + 分组 + 搜索筛选；消费 FR-247 服务端搜索/分页（增强 FR-165/163，需 spec，依赖 FR-247/246/240） | P1 | ✅ 已交付@v0.13.0·四档验收（首屏走 /instances/search + /aggregate 不拉裸数组、URL 状态深链恢复、卡片/列表/分组单表虚拟渲染、滚动恢复；spec §4 全 [x]，vitest 16/16 + Playwright fr235-instance-list 2/2 绿） |
| FR-236 | 一键搭建模态优化 + Docker 傻瓜建服：建后端服 / 建代理两模态遵循 ui-modals + 大白话；并入 Docker 一键建服（预置镜像 `itzg/minecraft-server` + 端口 + 一键起）（增强 FR-035/078，需 spec，含原 FR-239） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-237 | 节点 Docker 可用性检测 + 未装引导：Worker 探测 docker 是否安装/可用，CP/UI 展示状态，未装给安装引导，docker 建服前先测不通即提示不卡死（feat，需 spec：worker 探测 RPC + 跨模块，关联 FR-229 连通性族） | P1 | ✅ 已交付@v0.13.0·全真栈验收 |
| FR-240 | 导航外壳 + 实例选择器重构（面向 1000+ 实例）：先出 2-3 布局原型经确认 → 实现；实例选择支持搜索 / 虚拟列表 / 分组 / 最近&收藏；并入面包屑文案与路由映射纠错（今实例页面包屑文字与实际页不符；此项排最后做）（增强 FR-131/162/134，需 spec + 原型审核，含原 FR-245） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-241 | 全局搜索 / 命令面板（快捷跳转）：Ctrl/⌘+K 唤起，搜实例/节点/页面/操作并跳转（今页眉搜索框为占位）；并入页眉集群徽标 → 可滚动实例选择弹层 + 点击进入；面向 1000+ 走服务端搜索（feat，需 spec：搜索契约，依赖 FR-247，含原 FR-242） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-243 | 页眉顶部加载进度条 + 路由切换过渡：切页弹一次顶部进度条（nprogress 式）+ 统一路由过渡反馈（今无）（feat，免 spec） | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-244 | 全局动画系统统一：修「稀碎卡顿 / 无动画」——统一 hover / 展开 / 弹层 / 路由过渡（今无 framer-motion、散用 CSS transition）（增强 FR-163/176；本轮随 `docs/specs/instance-list-redesign/spec.md` 草拟审核） | P2 | ✅ 已交付@v0.13.0·四档验收（motion token/顶部进度条/路由·侧栏·抽屉主链路 + 共享原语 Panel/Dialog/Chips/Toggle 散落 duration 收敛到 motion token + 词汇表注释落定；high-value 收敛过关，CSS motion token 非 framer-motion） |
| FR-246 | 全站卡片范式提质 + 节点页卡片重设计：统一卡片圆角 / 边角 / 阴影 / 间距（修「连接感 / 阴影太假 / 圆角不符 / 边角瑕疵」）并全站贯彻 + 节点页卡片信息密度重做（增强 FR-163/176/177 落地质量，需 spec：圈定全站范围 + 范式） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-247 | 实例规模化后端（1000+）：服务端实例搜索 / 分页 / 分组聚合 API（今前端全量拉、1000+ 撑不住），为列表页 / 导航实例选择器 / 全局搜索 / 页眉弹层提供统一数据地基（feat，需 spec：新查询契约 + 索引 + 多前端面消费）→ `docs/specs/instance-scale-backend/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-248 | OTA 签名密钥自动生成与面板公钥展示：CP 启动时未注入 env 私钥则自动生成 Ed25519 密钥对并持久化到数据根文件（env 注入优先、双轨），面板展示公钥供运营者配到客户端；修订 ADR-022/038（增强 FR-087，需 spec + ADR-052）→ `docs/specs/client-sign-key-autogen/spec.md` | — | ❌ 已废弃（验签已去，见 ADR-054） |
| FR-249 | OTA 拉取错误追踪与面板查询：manifest/artifact 拉取失败也记录追踪事件（含错误原因），面板分发事件页可按成功/失败筛选并查看错误详情（增强 FR-093，需 spec：DB schema 扩展 + 新查询维度）→ `docs/specs/client-dist-error-tracking/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-250 | 客户端分发发布编排器重做：拖拽多文件/文件夹到浏览器 → 前端本地预览文件树 → 可调整层级/sync 模式 → 点击发布才批量上传到 CP（非逐个上传节省带宽）；消费 FR-251 分块上传（增强 FR-191，需 spec，依赖 FR-251） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-251 | 大文件分块上传端点 + 进度展示：后端新增分块上传协议（init→chunk→complete），前端分片上传 + 实时进度条，支持 4G+ 文件（增强 FR-088，需 spec：新 API 协议）→ `docs/specs/client-chunked-upload/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-252 | 客户端分发术语大白话化：sync 模式（覆盖/仅一次/忽略）加大白话解释、"托管目录"等术语改为运营能看懂的措辞、发布步骤每页加大白话说明（增强 FR-194，免 spec） | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-253 | OTA 客户端信任公钥运行期可配：updater-core 从 jm-updater.json 读信任公钥（缺省回退内置 dev 公钥），CP 一键生成带本机公钥的 jm-updater.json + 接入指引更新（补 FR-248/FR-107 缺口——面板自动生成的公钥此前无处填入客户端；修订 ADR-022 信任模型，需 spec + ADR）→ `docs/specs/client-trust-key-config/spec.md` | — | ❌ 已废弃（signPublicKey 废弃，见 ADR-054） |
| FR-254 | 客户端分发发布页文件树拖拽编排：configure 步支持拖拽移动文件/目录节点改目标路径（增强 FR-191/250，FR-191 曾列增强未做，免 spec） | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-255 | 客户端分发清理范围编辑器：managedDirs 改多级目录树勾选（支持深层嵌套目录）+ 可选「清空整个 gameDir」（除内置玩家区安全清单 + 运营自定义追加排除；需客户端 clean-all 语义 + 服务端 manifest）（增强 FR-191/088，需 spec）→ `docs/specs/client-dist-clean-scope/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-256 | updater-core 架构简化 Phase 1：删验签（Signatures/BouncyCastle/.jmpack）、删 CAS（CasCache）、删 core 自更新（SelfUpdater）、manifest 去 sig 段、去防降级；保留 sha256 完整性校验 + 拉取密钥鉴权（推翻 ADR-022/053，废弃 FR-090/091/097/253/248）→ `docs/specs/updater-arch-simplification/spec.md` | — | ✅ 已交付@v0.13.0·四档验收 |
| FR-257 | Reconciler 流式下载 + HTTP Range 断点续传：Transport 改返回 InputStream、Reconciler 边读边写盘（64KB 缓冲）、DigestInputStream 流式 sha256、zstd 流式解压、断点续传；1GB 文件下载内存 < 10MB（依赖 FR-256）→ `docs/specs/updater-arch-simplification/spec.md` | — | ✅ 已交付@v0.13.0·四档验收 |
| FR-258 | 楔子改 gradle-wrapper 模式：整合包只带 wedge.jar，首次自动拉 core（JDK 原生 HttpURLConnection + sha256 校验）、本地保留 3 版用于回滚、jm-updater.json 原文透传 ctx、Core.run(Map) 接口契约冻结（依赖 FR-256）→ `docs/specs/updater-arch-simplification/spec.md` | — | ✅ 已交付@v0.13.0·四档验收 |
| FR-259 | CP core 版本归档 + 端点 + 面板回滚：make embed-client-updater 归档多版本 core jar、GET /client-channels/:id/updater-core 端点（拉取密钥鉴权）、面板「updater-core 版本」选择器一键切换回滚；补充平台管理员手动上传 updater-core.jar hotfix 并可立即选用（依赖 FR-258）→ `docs/specs/updater-arch-simplification/spec.md`、`docs/specs/updater-core-hotfix-telemetry/spec.md` | — | ✅ 已交付@v0.13.0·四档验收 |
| FR-260 | 客户端更新器架构简化 前端 + ADR + doc-sync：接入指引去 signPublicKey 改"楔子自动拉取"、updater-core 版本选择器面板、新 ADR 推翻 ADR-022/053 修订 ADR-045、API/ARCHITECTURE/PRD/CHANGELOG 同步、重编内嵌 jar；后续增强：接入指引可选择 revealable 拉取密钥并自动填入/下载 `jm-updater.json`，Core 版本页展示最新归档与当前选定，内置/选定 updater-core 归档禁止从制品库删除（依赖 FR-259） | — | ✅ 已交付@v0.13.0·四档验收 |
| FR-263 | 拉取密钥加密器自动生成与持久化：CP 启动未注入 JIANMANAGER_CLIENT_KEY_ENC_SECRET 时自动生成 AES-256-GCM 密钥并持久化到数据根文件（env 注入优先、双轨），dev 回退内置开发密钥；修订 ADR-044 存储策略；后续增强：密钥列表/编辑支持永不过期展示、设置/清空过期时间，明文弹窗标题不拼接密钥名且复制按钮稳定可用（feat，需 spec + ADR） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-264 | 客户端分发单节点源站安全防护：在无 CDN 部署下补多维限流、字节配额、IP 临时封禁与手动解封、key 状态机、频道降速保护、制品授权收紧、启动安全画像遥测（playerName+machineId+installId+环境特征强制上报）与独立防护中心页面（feat，需 spec）→ `docs/specs/client-dist-security-firewall/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-261 | 发布页文件资源管理器（专业文件树库重写）：react-arborist 替换 ClientFileTree，新建文件夹/重命名/Ctrl+点选/Shift+连选/Delete 删除/拖拽上传文件/拖拽上传 zip 自动解压（含 GBK）/拖拽移动调整结构/同名冲突提示忽略覆盖保留两者；保留本地编排→点发布才上传架构（feat，需 spec） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-262 | 清理目录树形右键菜单可视化：FR-261 同款文件树展示目录结构，右键菜单标记清理/排除/取消，Ctrl+点选 Shift+连选批量操作，颜色区分（红=清理/绿=排除/无色=不管理），父子联动；产出 managedDirs+cleanExclude（增强 FR-255，依赖 FR-261，需 spec） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-265 | 客户端分发观测四 Tab 重构：同页面拆统计/监控/日志/客户端；统计/监控/日志仅看分发请求事件，客户端 Tab 独立看运行态与更新结果；新增运行态心跳表/端点、请求日志脱敏详情、实时聚合与运行态联动筛选；清理废弃缓存命中指标；与 FR-264 并行开发互不覆盖 → `docs/specs/client-dist-observability-rebuild/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-266 | updater-core 构建元信息内嵌与展示：jar 内写入版本号 / Git commit / dirty / buildTime，CP 归档读取并在 Core 版本页展示；紧急 hotfix jar 缺少元信息仍可直接上传并立即选用 → `docs/specs/updater-core-build-metadata/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-267 | 高密度控制台设计系统底座：控制台主界面采用 A+C Jian 绿默认运维风格（细边框、小圆角、紧凑间距、等宽数字、状态点、紧凑表格），作为页眉/侧栏/服务器控制台的视觉基础（见 ADR-057） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-268 | 页眉节点作用域 + 侧栏 IA + 页面归位：页眉提供节点作用域、面包屑、搜索、集群状态、任务、通知、账户；节点作用域联动全部服务器列表、命令面板实例结果与创建实例默认节点；侧栏改为平台首页/服务器/群组网络/观测/平台管理，主题入口保留在侧栏底部，单服能力归入服务器控制台（见 ADR-055） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-269 | 服务器统一控制台 `/instances/:id`：单台服务器详情页固定分区为概览/控制台/文件配置/监控/玩家/插件/备份定时/业务/Bot，并保留可组合画布作为高级拼屏能力（见 ADR-056） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-270 | 节点控制台 + 高密度服务器列表：节点页与全部服务器页对齐新 IA，消费 FR-247 搜索/分页/聚合地基，面向 1000+ 实例保持可扫、可筛、可下钻 | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-271 | 平台首页高密度总览：平台首页按节点、服务器、异常、任务、资源、告警等维度重排为一屏速扫总览 | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-272 | 全站页面归位与 A+C 皮肤收口：把玩家、插件、备份、定时、Bot、业务等单服能力收进服务器控制台，平台级页面归入平台管理，并统一 A+C Jian 绿默认高密度外观 | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-273 | 通用组件包与控件博物馆：抽出 `@jianmanager/ui` 通用 UI/token/charts 包，主应用从包引入共享控件，并新增 `web/wiki` 控件博物馆展示第一版通用控件与常用状态矩阵（需 spec + ADR-058；不抽业务组件、不新增后端接口） | — | ✅ 已交付@v0.13.0·四档验收 |
| FR-274 | Bot 压测会话 YAML 动作编排与 50 Bot 稳定验收（增强 FR-042）→ `docs/specs/bot-stress-yaml-orchestration/spec.md` | P2 | ✅ 已交付@v0.13.0·四档验收（代码全测过 spec §4 全 [x]；真机全栈验收：真 CP+Worker+bot-worker+MC1.16.5，50-bot YAML 编排会话 50/50 连续在线 0 掉线，稳定窗口 ~5.5 分钟[缩比 30min 硬标，窗口内 0 drop]） |
| FR-275 | CP↔Worker 专用 WS 令牌密钥自动下发：CP 预配独立于 jwt.secret 的 WS 令牌密钥（显式配置 > 生产态自动生成持久化 > dev 回退），终端/插件桥令牌改用其签发；经 gRPC RegisterResponse 下发（首注册+重注册），Worker 持久化到 node-identity.json（0600）并用于终端+插件桥校验，旧 CP 空字段回退现状（修 FR-080 enroll 不下发密钥致生产终端 401、探针监控失效的产品缺口；需 spec + ADR-061，修订 ADR-020）→ `docs/specs/worker-ws-token-secret/spec.md` | P0 | ✅ 已交付@v0.13.0·全真栈验收（无手工 secret 场景端到端：注册自动下发→终端 101） |
| FR-276 | 终端 WS 密钥不一致 401 诊断兜底：CP 终端代理探测 Worker 握手 401 时返回结构化诊断，前端显示「终端令牌被 Worker 拒绝，疑似该节点 WS 密钥与平台不一致」而非裸「连接已断开」，与网络类断连区分；OPERATIONS 标注密钥同步要求与手动核对修复法（需 spec，并入 worker-ws-token-secret；依赖 FR-275 先落）→ `docs/specs/worker-ws-token-secret/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收（正向真机通过；诊断分支单测覆盖，修复后 401 无法自然复现） |
| FR-277 | SSH 推送式远程部署脚本：`deploy-cp.sh` / `deploy-worker.sh` 两独立 POSIX sh 脚本，从操作机经 SSH 密钥把本地 `make dist` 产物推送部署/更新到 Linux 主机（systemd 常驻、幂等重跑、数据与身份保留、部署后健康检查）；SSH 目标与部署参数经环境变量配置，Worker 首次上线经 `JM_ENROLL_TOKEN` 复用 setup 语义；目标机 root / 免密 sudo / 纯普通用户（user 级 systemd+linger）三档全支持（需 spec + ADR-063）→ `docs/specs/ssh-deploy-scripts/spec.md` | P1 | ✅ 已交付@v0.15.0·真机验收（deploy-cp/worker 更新模式/幂等/身份保留/user档systemd/一键装机全验通） |
| FR-278 | CP 内嵌 Worker 二进制（一键安装/升级不依赖 GitHub）：构建期把 windows/linux amd64 Worker 二进制 go:embed 进 CP（make dist / CI 两阶段：先 worker 后 CP，体积 +~43MB），`/worker-assets` 端点解析顺序改为 内嵌（版本=CP 自身）> 本地缓存 > 远程 feed 兜底——离线/内网/国内网络/本地构建开发版的一键安装与节点升级全程不出网，CP↔Worker 版本强一致（真机复现：安装请求因 GitHub TLS 超时 + 远程无本地构建版本 release 而必然失败）（feat，需 spec + ADR-062 修订 ADR-059 分发来源）→ `docs/specs/worker-embed-distribution/spec.md` | P1 | ✅ 已交付@v0.15.0·真机验收（内嵌下发 sha 逐字节对上、embedded://、装机升级零 GitHub 外呼） |
| FR-279 | JDK 下载受限网络可用性与失败引导：JDK 一键下载（temurin 等 GitHub 源）在无代理环境 TLS 超时（真机复现 `adoptium/temurin21-binaries ... TLS handshake timeout`）——失败信息改为可操作引导（明示可在设置页配置出站代理、或切换 JDK 镜像源，附入口跳转），并真机验证 Worker 侧 JDK 下载确实经 ADR-043 心跳下发的出站代理生效（增强 FR-033/178/186，免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（代理下发链路 L1 证+引导 UI 上线；网络失败态 env-blocked 已 DOM 证） |
| FR-280 | 出站代理面板提示与连通性测试自定义目标：① 代理设置区补大白话提示，列明哪些流量经此代理（JDK 下载、Worker 资产/自更新拉取、探针依赖、客户端分发外呼等）；② 连通性测试目标从写死 GitHub 改为可输入任意 URL 测试，默认 `https://www.google.com`（提示文案同步去掉「（GitHub）」硬编码）（增强 FR-185/229，免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（任意 URL 实测+可编辑输入框线上 bundle 命中） |
| FR-281 | CP→Worker 指令反向流化（免 NAT / 零入站端口暴露）：把 CP→Worker 的指令派发从「CP 直连 worker gRPC `node.Host:GRPCPort`」改为经 Worker 主动建立的**常驻双向流**承载——Worker 只出站、零入站端口暴露，家用 NAT / 内网 / 公网混合环境均可接入（现状真机复现：公网 CP 无法回拨家用 NAT 后的 Windows worker 的 9101/9102）。含 proto 新增双向指令流、CP 连接池由「拨号」改为「往流里派发 + 关联请求/回执」、Worker 由 gRPC 服务端改为「守流听指令」、超时/断线重连/背压语义；终端 WS（9102）一并经 CP 中转（或同机制隧道化）才彻底免入站。**架构级大改，需 spec + ADR-066 修订 ADR-002（gRPC 节点通信）**；设计定案：gRPC 反向隧道（grpctunnel，~70 RPC 零信封）+ 双模式（隧道优先/直拨回退）+ 终端一律经 CP 中转（feat）→ `docs/specs/worker-reverse-stream/spec.md` | P1 | ✅ 已交付@v0.15.0·真机验收（现网双反向隧道自愈、隧道承载探针/上传/终端中转、NAT 场景真跑） |
| FR-282 | install-worker.sh 同机多节点 systemd unit 名冲突（fix，增强 FR-222/080/277）：`install-worker.sh` 硬编码 systemd unit 名 `jianmanager-worker`，同一主机装第 2 个 worker（不同 `--install-dir`）会**覆盖**第 1 个的 unit 与服务，致原节点被顶掉（真机复现：node-2 装机把 node-main 的 unit 冲掉，被迫手工建独立 unit 恢复）。改为 unit 名据 `--install-dir` basename 派生（默认 `/opt/jianmanager` 及 basename=jianmanager 仍映射 `jianmanager-worker` 保向后兼容；`jianmanager-node2` → `jianmanager-worker-node2`），使同机多 worker 各占独立 unit 互不覆盖；canonical 与内嵌副本同步、byte 一致测试守护（免 spec） | P1 | ✅ 已交付@v0.15.0·真机验收（三 unit 共存互不覆盖，同机新旧脚本正反对照） |
| FR-283 | 仓库根 pnpm workspace + 前端提升（ref，需 spec）：引入根 `pnpm-workspace.yaml`；`web/src`→`apps/control-plane-web`、`web/wiki`→`apps/ui-museum`、`web/packages/ui`→`packages/ui`（vite alias 改真实 workspace 依赖）；抽 `packages/tsconfig` + `packages/eslint-config` 共享配置；go:embed 目标不变，仅 `Makefile` embed-web 拷贝源 `web/dist`→`apps/control-plane-web/dist` 改一行。行为不变（关联 ADR-064，重申 ADR-005）→ `docs/specs/monorepo-unification/spec.md` | P2 | 🔨 开发中 |
| FR-284 | devmock 抽为独立包（ref，免 spec）：`web/src/mocks`→`packages/devmock`，依赖反转（只依赖 API 契约，不再 `import '@/…'` app 内部）；主应用与测试改引 `@jianmanager/devmock`；生产构建仍不含 devmock（ADR-047 不变量保持）。依赖 FR-283 先落 | P2 | 📋 计划 |
| FR-285 | Go 入口迁入 apps（ref，免 spec）：`cmd/*`→`apps/*`（control-plane/worker/jmctl）；`internal/` 不动；同步 `Makefile`/`release.yml`/`Dockerfile.*` 的 go build 路径；go:embed 目标目录不变。main 包无 importer，行为不变（关联 ADR-064） | P2 | 📋 计划 |
| FR-286 | bot-worker 迁入 apps（ref，免 spec）：`bot-worker`→`apps/bot-worker`；同步 Go worker 运行时 spawn 路径解析与打包；须真机验 bot 真入服 spawn 成功（关联 ADR-064）。依赖 FR-285 | P2 | 📋 计划 |
| FR-287 | 统一命令面 go-task（feat，需 spec）：引入 `Taskfile.yml` 作唯一命令入口，各语言暴露 build/test/lint/dev 同套动词委托 go/pnpm/gradlew，Windows+Linux 双跑通；`Makefile` 收敛为被委托层（关联 ADR-064）→ `docs/specs/monorepo-unification/spec.md`。依赖 FR-283/285 路径落定 | P2 | 📋 计划 |
| FR-288 | 前端版本接上单一真源（feat，需 spec）：版本唯一真值 = `internal/version/version.go`（ADR-065 既定）；把前端 `__APP_VERSION__` 从独立记版的 `web/package.json` 改为构建期从真源派生注入，改 `version.go` 一处→前端展示+Go `/version`+产物路径（ADR-036）一致，消除前端漂移（实证：真源已 `0.15.0-dev`、前端仍报 `0.14.0`）（关联 ADR-064/065）→ `docs/specs/monorepo-unification/spec.md`。依赖 FR-283 | P2 | 📋 计划 |
| FR-289 | JDK 安装 arch 别名归一化（fix，增强 FR-033/178）：`POST /nodes/:id/jdks/install` 的 `arch` 为自由字符串直透下载源解析，传 `amd64` 时 adoptium 不认（只认 `x64`）→ 下载 404 且错误不指向真因（真机复现：temurin 17 `arch=amd64` → 「下载返回 HTTP 404」，同版本 `x64` 成功）；面板固定送 `x64` 不受影响、API/自动化调用方中招。CP 侧入参归一化常见别名（`amd64`→`x64`、`arm64`→`aarch64`），未知 arch 直接 422 拒绝而非透传出 404（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（amd64→x64 归一装成；foobar→422 INVALID_ARCH） |
| FR-290 | JDK 下载停滞看门狗与中断错误归网络类（增强 FR-183/279）：下载流中途被掐（代理/跨境线路抖动）时任务无停滞检测，卡在中间进度直至整体超时兜底（真机复现：经 SOCKS5 代理下载 temurin 21 卡 48% 约 9 分钟），且最终错误 `写入临时文件失败: context canceled` 未被 FR-279 归为网络类 → 失败引导（去配代理/换镜像）不触发。加下载停滞看门狗（N 秒零进度 → 快速失败）并把停滞/中断类错误纳入 FR-279 网络类归类与引导（免 spec） | P2 | ✅ 已交付@v0.15.0·单测+真机回归（停滞归网络类/强停不误标单测覆盖；真停滞待自然复现看引导渲染） |
| FR-291 | JDK 安装失败残留目录清理与重试自愈（fix，增强 FR-183）：安装失败/被取消时已建的目标目录不清理（真机复现：卡死任务遗留 4KB 半截 `temurin-21/` 脚手架），此后同版本重装永远撞「目标目录已存在」，须登机手动 rm 才能解——核心操作被死锁在需要 SSH 的境地。改为安装失败即清理本次所建目标目录；安装前若目标目录存在但无完成标记（无 `bin/java`）则视为残骸自动清除重装；完好已装目录仍拒绝覆盖（语义不变）（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（预植残骸自愈装成+完好目录仍拒，同机正反对照） |
| FR-292 | 删除托管 JDK 清理到托管根顶层目录（fix，增强 FR-033/228）：删除走 NodeJDK 登记的**嵌套探测路径**（归档外层多包一层，如 `temurin-11/jdk-11.0.31+11`），Worker `RemoveAll` 只删内层、父壳空目录留在托管根下占位（真机复现：删 11 后 `temurin-11/` 空壳仍在，需手动 rm；症状虽被 FR-291 自愈兜住，删除本身应清净）。Worker Remove 把托管根下的目标路径归一到**顶层子目录**整体删除；托管根本身（`rel="."` 现可穿过校验直达 `RemoveAll(根)`，一并堵死）与根外路径拒绝（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（嵌套删除无父壳、邻居 JDK 完好、拒删托管根） |
| FR-293 | 侧栏常驻服务器列：收藏置顶+最近打开（增强 FR-240，补当年搁置的 Part B 常驻实例列）：「选择服务器」按钮下方常驻两区=收藏（置顶）+最近打开（LRU ≤8），行=状态点+名称，点击进该服控制台；行内星标收藏/取消，与搜索弹窗收藏互通（同一 localStorage）；已收藏不重复出现在最近区；双空显示引导；侧栏折叠图标轨态不显示；不为侧栏引入高频轮询（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（收藏/最近渲染、点击客户端跳转进服、与选择器星标互通、双空引导） |
| FR-294 | 页眉集群统计浮窗（增强 FR-162/179，复用 FR-216 铃铛浮窗范式）：在线节点/运行中服务器/崩溃服务器三徽标点击改弹缩略浮窗——节点行=名称+状态点+运行实例数、服务器行=名称+节点+在线人数、崩溃行=名称+节点+崩溃原因（statusReason）；行点击进对应服控制台/节点页，底部「查看全部」保留原筛选页跳转；数据仅浮窗打开时拉取，行数上限 8 超出提示剩余数；崩溃空态友好文案（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（节点浮窗 4 节点各显运行数、运行服浮窗名称+节点+在线、查看全部页脚） |
| FR-295 | 服务器控制台页签 keep-alive 与终端连接管理器（增强 FR-269）：9 个页签访问过即保活、切走不卸载切回瞬时；终端页签往返 WS 不断、滚动缓冲与输入不丢；文件/配置页签目录展开态与未保存草稿保留；终端连接抽为实例级连接管理器（组件订阅制，组件卸载不再直接断 WS）；离开实例整体释放（需 spec+ADR：前端 keep-alive 与终端连接管理架构，React 19.2 `<Activity>`/CSS hidden 取舍进 spec）→ `docs/specs/console-keepalive/spec.md` | P2 | ✅ 已交付@v0.15.0·真机验收（8+ 页签往返终端 WS 握手恒为 1、隐藏页签 DOM 保留、缓冲连续） |
| FR-296 | 跨服务器控制台热缓存（增强 FR-269，**依赖 FR-295**）：最近打开的 ≤3 个服控制台整体保活（LRU 淘汰），A↔B 切换双向瞬时呈现；缓存中的服终端 WS 保持连接、缓冲连续；闲置超时（默认 10 分钟不可见）自动断 WS 降级为界面状态缓存、再进自动重连；LRU 淘汰即完整卸载释放（与 FR-295 同 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（跨服切换两页同存 DOM、组件不重建、WS 存活缓冲连续；闲置 10min 断连+重连、4 台 LRU 淘汰环境受限未真机、单测覆盖） |
| FR-297 | 实例域数据层缓存调优（增强 FR-269/240）：实例详情/指标/serverState 等实例域查询拉长 staleTime/gcTime + placeholderData，回切先呈现缓存数据后台刷新；侧栏常驻列/服务器选择器行悬停预取实例详情（防抖）；现有 vitest 全绿不回归（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（跨服回切瞬时呈现无白屏，热缓存命中） |
| FR-298 | 节点运行时库底座与自动扫描（feat，泛化 FR-033/178/228 JDK 域，需 spec）：运行时抽象加类型维度（jdk / nodejs / python 预留；JDK 沿用 `node_jdks` 不动实例外键，新类型承载表 spec 定）；Worker 新增**扫描常见安装路径** RPC（`/usr/lib/jvm`、`Program Files\Java`、nvm/官方 node 目录等，复用 detect 探测），节点页 JDK 面板扩为「运行时」分区：扫描候选勾选入库 + 手动登记泛化多类型 → `docs/specs/node-runtime-library/spec.md` | P1 | ✅ 已交付@v0.15.0·真机验收（扫描命中植入JDK→登记(managed=false)→重扫already去重→删外部登记留文件） |
| FR-299 | Node.js 运行时一键安装（feat，需 spec 并入 node-runtime-library）：nodejs.org dist 便携归档安装（index.json 版本解析、托管目录布局同 JDK），经节点出站代理（ADR-043）；停滞看门狗 / 网络失败引导 / 残骸自愈 / 删除顶层清理与 JDK 链路同语义（FR-289~292 齐平）。依赖 FR-298 | P1 | ✅ 已交付@v0.15.0·真机验收（直连 nodejs.org 装 Node 22.23.1 落盘 bin/node、amd64→x64 归一、任务中心进度、托管删除连文件；no_proxy 绕过 SOCKS 代理直连） |
| FR-300 | bot-worker 接托管 Node 运行时（增强 FR-006 Bot 域，需 spec 并入 node-runtime-library）：Bot spawn 由裸 `exec("node")` 改为优先用节点运行时库登记/托管的 Node（可指定，无则回退 PATH 保兼容），消灭新节点 PATH 缺 node 时 Bot 起不来的脆弱点。依赖 FR-298 | P1 | ✅ 已交付@v0.15.0·真机验收（bot spawn 日志 nodeSource=managed-scan + node=托管 Node 路径，node-2 无系统 node 必用托管） |
| FR-301 | CP 运行时库存聚合缓存与刷新（feat，增强 FR-178 运行时资产页，需 spec 并入 node-runtime-library）：Overview 扩**多运行时矩阵**（节点 × 类型 × 版本 + 引用关系），CP 缓存各节点库存快照、显「上次同步时间」+ **手动刷新**（强制 syncFromWorker），同步失败容忍显旧数据。依赖 FR-298 | P1 | ✅ 已交付@v0.15.0·真机验收（overview 多运行时矩阵+runtimeSyncs+syncedAt、refresh 逐节点结果、node-windows 离线容忍显 None） |
| FR-302 | 导入现有服务器向导（feat，节点级，需 spec + 新 ADR 落地 ADR-007 预留的「导入已有目录」高级模式）：浏览节点目录（复用 FR-178 BrowseDir）→ Worker 探测核心 jar / 内嵌 JDK / `server.properties` 端口 → 导入时二选一：**就地接管**（目录不动，托管区外例外 + **删除实例不删原目录**守则）或**搬进托管区**（移动/拷贝到系统分配目录）→ 登记实例（结构化启动 ADR-008）+ 探到的 JDK 顺带入运行时库 → `docs/specs/import-existing-server/spec.md` | P1 | ✅ 已交付@v0.15.0·真机验收（就地：导入启动Done于原目录+删除原目录完好；搬迁：源清空+托管区落位+Done+删除连目录清） |
| FR-303 | 审计日志 action 名 i18n（增强审计域，免 spec）：全量已知 action 键（`instance.start` 等 60+）中英映射（`audit.actions.*`），审计页显示翻译、悬停/角标露原键、未知键回退原样不崩；守则：本批 FR-298~302 新增审计 action 随身带中英翻译 | P2 | ✅ 已交付@v0.15.0·真机验收（线上bundle含70键中英翻译，审计记录新action；未知键回退单测覆盖） |
| FR-304 | 文件上传链路流式化（增强 FR-009 文件域 + FR-052/053 插件部署，需 spec）：`POST /instances/:id/files/upload` 现为 CP `io.ReadAll` 全量内存缓冲 + Worker `WriteFile` unary 直传（下载修复 a6a3713 的对称隐患）。复现结论：**直拨模式** >64MB 被 Worker `MaxRecvMsgSize` 显式拒收（65MB → `ResourceExhausted` 68157492 vs 67108864）；**隧道模式**（FR-281）grpctunnel 分帧 16KB、逻辑消息上限 4GB，同载荷**不设防直接吞** → 双模式行为不一致；≤64MB 也双侧整块缓冲，且 CP `FileService.WriteFile` 硬编码 10s 超时慢链路大文件必超时。新增 client-stream `UploadFile` RPC（与 `DownloadFile` 对称：分块传输 + 落临时文件校验后原子改名），CP 上传 handler 改流式转发不再 `ReadAll`；FR-052/053 插件单发/批量扇出复用 `WriteFile` 的部署路径同步兼容改造（阈值/是否保留小文件 unary 由 spec 定）；老 Worker `Unimplemented` 回退 `WriteFile` 保兼容（沿用 ≤64MB 上限并给明确报错） | P1 | ✅ 已交付@v0.15.0·真机验收（70MiB 上传 sha 逐字节一致/13.2s/零临时残留，隧道+直拨双通道在线；六项 spec 验收全过） |
| FR-305 | 反向隧道单消息尺寸上限统一治理（fix/健壮性，增强 FR-281 隧道传输 + FR-010/114 探针部署，需 spec + 宜随 FR-281 M1 一并做）：Worker `ServerOptions()` 的 `MaxRecvMsgSize=64MiB`（5de8778 为 `DeployServerProbe` ~7.6MB 载荷特意从默认 4MiB 放宽）只作用于 `grpc.NewServer`；而反向隧道把 WorkerService 挂在 `grpctunnel.NewReverseTunnelServer`（`internal/worker/tunnel/tunnel.go:107`）上，**不消费任何 `grpc.ServerOption` 消息上限**——grpctunnel v0.3.0 不暴露尺寸上限选项，唯一天花板是硬编码 `math.MaxUint32`≈4GB。后果：① **双传输行为不一致**——直拨 >64MiB 显式 `ResourceExhausted` 拒收，隧道同载荷一路吞到 4GB（FR-304 复现坐实：65MB `WriteFile` 隧道成功）；② 隧道已是 ADR-066 首选路径，那个「特意放宽到 64MiB」的安全边界在主链路上**等于不存在**，大 unary RPC（`DeployServerProbe` jar、老 `WriteFile` 直传、`ReadFile` 10MiB 返回等）整块缓冲、无早拒，构成内存/OOM 暴露面。范围：统一直拨与隧道两路的收/发单消息尺寸策略（含 CP 侧 `MaxCallRecvMsgSize`/`MaxCallSendMsgSize` 与 Worker 收发两向），做成有意为之的显式上限而非默认漂移；大载荷优先走流式（与 FR-304/`DownloadFile` 同向，`DeployServerProbe` 是否流化由 spec 定），合法 7.6MB 探针部署与既有 <64MB 载荷必须仍通过。**待定**：grpctunnel 无原生上限选项，落地手段（应用层拦截器早拒 vs 大 RPC 全面流式化 vs 二者结合 + 文档化 4GB 硬顶）由 spec 决策 → `docs/specs/tunnel-message-size-guard/spec.md` | P1 | ✅ 已交付@v0.15.0·真机验收（隧道 unary 64MiB 守卫与直拨同界；含缺陷修复=守卫只守 unary 不包裹 stream，修探针部署 30s 死锁；探针 3.6s 恢复/70MiB 流式仍通/65MiB 拒收单测锁死） |
| FR-306 | 节点包管理器与多 registry 配置（feat，泛化 FR-298 运行时库 + 承 FR-299/300 Node 生态，需 spec）：Node 22 自带 corepack，节点级选 npm/pnpm/yarn 偏好（默认 npm，pnpm/yarn 经 `corepack enable`+指定版本激活，零额外下载）；多 registry 配置——默认 registry + `@scope` 域源 + 镜像（CN 走 npmmirror 提速），写节点 `.npmrc`/托管配置（含凭据脱敏）。数据模型存 PM 选择 + registry 列表（节点级），Worker RPC 读写 PM/registry 配置 + `corepack enable`；被 FR-307/308 依赖。→ `docs/specs/node-package-management/spec.md` | P1 | ✅ 已交付@v0.15.0·真机验收（corepack 激活 pnpm 11.12.0、.npmrc 三类行落盘、token 脱敏+掩码保存不清空、审计；顺带修 FR-299 tar 符号链接丢弃缺陷） |
| FR-307 | 全局运行时包可视化管理（feat，需 spec 并入 node-package-management，依赖 FR-306）：面板管理节点上的**全局 npm 包**（`<pm> ls -g --json` 解析已装列表）——手动装/删/更新/查版本；装进节点运行时全局目录（PM global prefix，落托管区 `<数据根>/opt/runtimes/global`），装/更新经任务中心异步（慢操作）+ 停滞看门狗/失败引导语义复用（FR-290/279）。前端全局包管理页（列表+搜索+增删改+可更新标记），走已配 registry/镜像 → `docs/specs/node-package-management/spec.md` | P1 | ✅ 已交付@v0.16.0 |
| FR-308 | bot-worker runtime 下发部署 + 经全局包运行（feat，节点级，需 spec + 新 ADR 修订 ADR-006，依赖 FR-306+299）：install-worker.sh 一键装机/CP 下发 bot-worker 源码（`dist`，非 bundle）；其依赖（mineflayer/mineflayer-pathfinder）经 FR-306 的 PM+registry 装为**全局包**，bot spawn 带 `NODE_PATH` 指向全局 node_modules（FR-300 node 解析已就绪）——统一「全局包」模型：装 mineflayer 插件（全局包）bot 立即可用。消除装机节点 bot 真连 MC 缺 426MB 依赖的缺口（真机复现：dist-only spawn `ERR_MODULE_NOT_FOUND: mineflayer`）→ `docs/specs/bot-worker-deploy/spec.md` | P1 | ✅ 已交付@v0.16.0 |
| FR-309 | 节点删除实例守卫（fix，增强 FR-004 节点管理）：删除节点当前不校验其名下实例，节点没了实例记录成孤儿——终端/操作全 404「节点不存在」（v0.15.0 真机复现：删离线测试节点后其 nat-echo 实例悬挂，用户直撞 404）。节点删除时有实例则 409 拒绝并列出实例（离线节点允许 `?force=` 显式级联删除记录并明示不清理远端文件）（免 spec） | P1 | 📋 计划 |
| FR-310 | 删除运行中实例的进程处置一致性（fix，增强 FR-005/FIX-C）：对 RUNNING 实例发 DELETE，CP 删记录返 200 而 Worker 运行态守卫拒杀进程——DB 与现实脱钩，java 进程成无主孤儿继续跑占端口（v0.15.0 真机复现：删运行中测试实例后 java -jar server.jar 遗留）。改为 CP 对 RUNNING 实例删除先优雅停（超时强杀树）再删，或 409 要求先停；Worker 重启孤儿接管扫描一并评估（免 spec） | P1 | 📋 计划 |
| FR-311 | 节点页管理体验补全（增强 FR-033/177，真机测试反馈）：① JDK 已登记行加**编辑**入口（改厂商/版本/arch/路径，走既有 PUT /nodes/:id/jdks/:jid，模态遵守 ui-modals）；② 节点详情头四个统计球排版紧凑化（现占满整行空隙过大，改小尺寸+左聚拢与信息区同排）（免 spec） | P2 | ✅ 已交付@v0.16.0 |
| FR-312 | 实例启动失败原因可见性（增强 FR-269/294，真机复现：实例未绑 JDK 点启动只见「启动中→崩溃」无因——statusReason 已入库已出 API，但实例控制台页无展示位，且 Worker 心跳把 CRASHED 冲回 STOPPED 后卡片唯一展示位 `status===CRASHED` 条件失效）：实例控制台页加失败原因横幅（statusReason 非空即显、标注「上次启动失败」，下次启动由既有 transition 清空），工作台卡片展示条件同步放宽为非空即显（免 spec） | P1 | 🔨 开发中 |
| FR-313 | 进程级崩溃诊断链路（feat，需 spec）：Worker 维护实例输出环形缓冲（最后 N 行）+ 捕获退出码/信号/运行时长，进程退出生成崩溃快照经 gRPC 上报 CP 持久化（保留最近 K 次）；前端实例控制台「崩溃诊断」展示快照（时间/退出码/尾部输出）——补齐进程秒退（如 jar 缺失）全链路零痕迹盲区（现 proto 不带原因、Worker 不留崩溃日志、终端仅实时流）→ `docs/specs/crash-diagnostics/spec.md` | P1 | 📋 计划 |
| FR-314 | 实例启动同步预检（feat，增强 FR-005 启停链路，需 spec）：点启动 CP 先同步调 Worker 预检（JDK 绑定/可解析、启动目标 jar 或命令存在、工作目录就绪），失败同步返回具体错误（前端弹错+横幅），状态不进 STARTING；过检才转 STARTING 走既有异步启动——终结「配置错误也要启动中→崩溃兜一圈」→ `docs/specs/start-preflight/spec.md` | P1 | 🔨 开发中 |
| FR-315 | 实例状态光晕视觉（增强 FR-269/240 控制台视觉）：实例控制台容器与工作台实例卡片外圈按状态渐变光晕——RUNNING 绿色柔光、STARTING/STOPPING 琥珀呼吸、CRASHED 红色警示光、STOPPED 无光晕；随双主题取色、平滑过渡动画、遵守 UI 风格偏好不过度（免 spec） | P2 | 🔨 开发中 |
| FR-316 | 一键搭建版本-JDK 兼容预检（增强 FR-034 搭建向导，FR-308 真机钓出：选 MC 26.1 + 节点最高 Temurin 21 可搭建成功但启动必崩「requires running the server with Java 25」）：向导选定 MC 版本后按核心 Java 需求映射校验所选/默认 JDK，不满足即表单级阻断并引导去 JDK 面板安装；与 FR-314 启动预检互补（搭建时拦 vs 启动时拦）（免 spec） | P2 | 📋 计划 |
| FR-317 | 内存水位启动守卫（feat，真机事故钓出：验收开 Paper -Xmx2048M 把 8G 主机 OOM 至用户态僵死、SSH/面板全失联）：双闸防「启动实例把节点内存跑满」——① **Worker 实时闸**（真源）：StartInstance 前读系统可用内存，估算实例需求（启动命令解析 `-Xmx` + JVM 开销，解析不到用保守默认），可用 − 需求 < 保留水位（worker.yml `memory_guard.reserve_mb`，默认 max(512MB,总内存10%)）即拒绝启动并返回可操作错误；② **CP 预警闸**：启动请求先按节点最近心跳（总内存/已用）预判，不足直接 400 不下发 RPC，心跳过旧（>90s）放行交 Worker 实时判。批量启动并发预留列为后续增强（免 spec） | P0 | ✅ 已交付@v0.16.0 |
| FR-318 | CP 压力态 SQLite 嵌套事务错排查（fix，FR-317 事故现场钓出：主机 OOM/swap 风暴恢复初期，实例 stop 转换反复报 `SQL logic error: cannot start a transaction within a transaction`，删 bot 报 INTERNAL_ERROR，重启 CP 即愈）：排查 transition/删除链路在慢 IO 下是否存在事务嵌套或连接状态破坏（glebarez/sqlite 单连接语义），补压力回归测试 | P2 | 📋 计划 |
| FR-319 | 一键搭建异步化 + 全程可观测（fix+feat，真机复现：1.20.1 搭建挂死 180s+——Paper CDN 从节点侧仅 ~200KB/s、47MB jar 需 4 分钟，provision 是同步 HTTP 长事务，前端超时断开→请求 ctx 连锁取消 DownloadCore→留空壳实例（无 server.jar 可点启动→秒退 CRASHED），错误随断开连接进黑洞、CP 零日志零审计）：搭建改任务中心异步——同步段只 ResolveBuild+建实例（立即 201+taskId），下载/写配置/探针在 CP 后台 goroutine（context.Background 独立超时，不受前端断开影响）分阶段推进 Task（下载核心→写配置→部署探针），失败 Task 含完整错误链 + 实例 statusReason 写「搭建未完成：…」；worker DownloadCore 补开始/完成/失败日志；前端向导提交即关框提示看任务中心。**二轮修复（异步化暴露的新缺口）**：① DownloadCore 改临时文件 `.part` + 完整校验后原子 rename——下载中窗口不再躺半截 jar（真机复现：搭建中点启动得 `Invalid or corrupt jarfile server.jar`）；② Task 加 instance_id 关联，CP 启动闸拦截「provision 任务未终态就点启动」并引导看任务中心；③ 搭建全程实例 statusReason 标「搭建中」+ 卡片琥珀提示（免 spec） | P0 | 🔨 开发中 |
| FR-320 | API 错误统一落平台日志（feat，真机钓出：provision 失败等 API 错误只回 HTTP 响应，无任何 slog→FR-049 日志中心平台日志恒空，用户「乱七八糟的问题都看不到日志」）：全局 gin 中间件把 4xx 业务拒绝（422/502 等）与 5xx 的 [路径、状态码、error body、用户、IP] slog 化（4xx=warn、5xx=error），经既有 log_slog 桥自动落日志中心 platform 源；核对 /logs 页导航入口可见性（免 spec） | P1 | 🔨 开发中 |
| FR-321 | 审计日志记录失败操作与错误内容（增强 FR-050，真机钓出：搭建失败审计零记录）：AuditLog 加 success/error 字段，关键写操作（实例生命周期/搭建/删除/上传）失败也记审计并带错误内容；审计页列表失败徽章 + 详情展开错误（免 spec） | P1 | 🔨 开发中 |
| FR-322 | 心跳流断开告警降噪（fix，真机复现：CP journal 2 小时 484 条「心跳流断开 context canceled」WARN——worker 每拍心跳收完响应即 cancel 属正常收尾，被当异常刷屏淹没真问题，且无节点标识）：Canceled/EOF 正常收尾降 Debug，异常断流保留 WARN 并带 nodeUUID（免 spec） | P2 | 🔨 开发中 |
| FR-323 | 长操作任务化：导入/克隆/备份纳入任务中心（增强 FR-302 导入 / FR-036 克隆 / 备份 FR，FR-319 同族收口——用户「把所有长耗时任务加到任务中心、不堵塞页面、有进度」）：抽公共 `run-as-task` 底座（CreateTask→CP 后台 goroutine→SetStage 阶段进度→MarkSucceeded/Failed→终态站内信），provision（FR-319）一并迁过去 DRY；导入 migrate 搬迁 / 克隆 CloneWorkDir / 备份创建 / 备份恢复 四个现同步阻塞或后台无进度的长操作接入——提交秒回 `{taskId}` 不阻塞（搬迁/拷贝/打包可达数十分钟），任务中心显示 stage 文案 + 终态 + 失败错误链；新增 TaskKind（import/clone/backup_create/backup_restore）+ 前端任务中心筛选/文案 → `docs/specs/long-op-tasks/spec.md`（需 spec：3 端点响应改 {taskId}、抽共享底座跨 3 service+任务模型+前端） | P1 | 🔨 开发中 |
| FR-324 | 短操作 loading 与文件传输进度补齐（增强 FR-075 反编译 / FR-068 探针 / FR-008/070 文件——用户「1-3 秒短任务要 loading 或进度条」）：① 反编译、探针部署点击后 loading 且完成前防重复点；② 资源管理器 新建/重命名/删除/保存 文件 loading 反馈；③ 文件上传显示百分比进度条（axios onUploadProgress），下载有反馈（浏览器原生下载条 + 内联「下载中」提示）。纯前端 UX 补齐，无数据模型/API 变更（免 spec） | P2 | 📋 计划 |

### 范围外（后续版本，暂不纳入 V1）

| 编号 | 需求 | 预计版本 |
|---|---|---|
| FR-100 | MFA（TOTP 二步验证） | V1.1 |
| FR-101 | Control Plane 高可用 | V1.2 |
| FR-102 | 真正的多租户（tenant_id 隔离） | V2.0 |
| FR-104 | JVM 诊断（Arthas/JFR/JMX） | V1.2 |
| FR-105 | 邮件通知 | V1.1 |
| FR-106 | WebSocket 用户→Control Plane（全局事件推送） | V1.1 |

## 5. 非功能需求（NFR）

- **部署**：单二进制（go:embed 前端，ADR-005）；SQLite 零配置起步、MySQL 生产；运行态数据收口单一数据根（FHS，便携可迁移，ADR-010）。
- **架构不变量**：三进程模型（Control Plane / Worker Node / Bot Worker）+ 固定通信协议（gRPC 节点间 / WS 终端 / stdin-stdout Bot IPC / 反向 WS 探针），见 `.claude/rules/architecture-invariants.md`，任何变更不得逾越。
- **安全**：JWT 双 Token（15m access + 7d refresh）；WS 终端一次性 token；探针实例级 token（scope=plugin-bridge）；客户端分发使用 HTTPS + 拉取密钥鉴权 + sha256 完整性校验（ADR-054）+ L7 防护（ADR-023）；敏感信息经 `${ENV}` 注入不入库；破坏性操作二次确认 + 全程审计。
- **性能与规模**：指标时序分层卷积按 TTL 保留；大工作目录全文索引后台化；上万 Bot 以会话/摘要聚合不逐个铺开；离线节点/实例短路无效请求。
- **可维护**：SDD 文档驱动 + 防漂移规则（`.claude/rules/`）；核心模块（process manager / 状态机 / 协议）测试覆盖 ≥80%，新代码 ≥60%。

## 6. 验收标准

- **全链路运维**（节点在线 → 创建启动 MC 实例 → 终端交互 → Bot 进服）可真机端到端跑通（FR-043 已验，v0.3.0）。
- **逐 FR 验收**见对应 `docs/specs/<feature>/`；标 `已交付@vX.Y.Z` 须该 FR 验收标准全部满足 + 自动化测试通过 + 标注的真机/集成项由用户确认通过，由 `sdd-release-version` 发版统一标注。
- **横切验收**（前端类 FR）：i18n（中/英）完整 + 暗/亮色正常 + 关键路径真机验证——三者缺一不算交付。

| 期 | 主题 | 完成判定 | 主要证据 |
|---|---|---|---|
| 第一期（MVP） | 核心平台 | 节点、实例、终端、Bot 运维闭环真机通过 | FR-043 验收、自动化测试、真机记录 |
| 第二期 | MC 群组服运维 | 结构化建服、复制、运行时与制品链路可用 | 对应 FR spec、集成 / 真机验收 |
| 第三期 | 运营底座与可观测 | 监控、日志、玩家治理、备份、更新与资产能力满足运维闭环 | 对应 FR spec、监控 / 更新 / 运维场景验收 |
| 第四期 | 玩家客户端 OTA 分发 | 发布、拉取、更新、追踪、遥测与防护链路闭环 | 分发 spec、端到端更新验收、安全验收 |
| 第五期 | JBIS 业务对接平台 | 经济 / 背包等业务域可通过统一编排、适配器与 UI 管理 | 业务域 spec、探针适配验收、权限 / 审计验收 |
| 第六期 | 控制台体验与可寻址性增强 | 导航、可寻址、响应式、一致性与关键交互符合设计基线 | 前端验收、i18n / 主题 / 响应式检查 |

## 7. 分期（路线）

各期只描述**主题 / 目标**；具体 FR 归属以 §4 状态/版本为准，本节不随 FR 增长而改。

- **第一期（MVP）**：核心平台——认证 / RBAC、节点、实例生命周期、终端、文件、Bot、监控、告警、定时、备份、模板、审计、i18n + gRPC 全链路打通。
- **第二期**：MC 群组服运维——配置引擎、群组服关系模型、JDK 运行时、结构化建服（Bukkit / 代理 / Sponge）、一键复制、便携运行时 + 制品库。
- **第三期**：运营底座与可观测——时序监控、面板改造、ServerProbe 治理桥（退 RCON）、日志 / 插件 / 玩家 / 备份增强、Docker、一键安装、面板自更新、全局资产 / 存储 / DB 浏览、告警体系。
- **第四期**：玩家客户端 OTA 分发——HTTPS 分发、拉取密钥鉴权、sha256 完整性校验、updater 两件套、追踪 / 遥测 / 统计 / L7 防护。
- **第五期**：JBIS 业务对接平台——经济域、背包域，插件无关编排 + 适配器 + manifest。
- **第六期**：控制台体验与可寻址性增强——前端走查整改（导航 / 可寻址 / 页面增强 / 一致性 / 响应式 + 全局页眉）。

### 期 ↔ 版本映射

各期主题对应的发布版本切段（具体 FR 的版本以 §4 为准；带 * 为当前未发版开发段）：

| 期 | 主题 | 覆盖版本 | 状态 |
|---|---|---|---|
| 第一期 | 核心平台 | v0.1.0~v0.3.0 | ✅ 完成（真机 FR-043 已验） |
| 第二期 | MC 群组服运维 | v0.3.0~v0.4.0 / v0.7.0 / **v0.13.0*** | ✅ 完成（FR-046 SpongeVanilla/SpongeForge 真机验收通过） |
| 第三期 | 运营底座与可观测 | v0.4.0~v0.9.1 / **v0.13.0** | ✅ 完成（FR-010/053/114 真机验收通过，随 v0.13.0 发布） |
| 第四期 | 客户端 OTA 分发 | v0.8.0 / v0.11.0 / **v0.13.0** | ✅ 端到端真客户端 OTA 更新流已真机复验（updater-core 真下载+reconcile+sha256+增量/减量/防降级+鉴权 fail-static，真 UI 发布 v1/v2）；**唯一剩余 = FR-099 原生进度弹窗人眼目视 env-blocked**（非代码缺口，需真启动器场景） |
| 第五期 | JBIS 业务对接 | v0.10.0 / v0.13.0 / **v0.14.0*** | ✅ 完成（经济域 FR-115~123 @v0.10.0；背包域 FR-124~127 代码随 v0.13.0 落地，真机受 CoreLib serverInfo 环境门槛阻断，本轮解锁后真机验通、翻交付于 v0.14.0：bot 入服→AIS 落库→view→writeBasicAttrs+审计+追踪事件全链路） |
| 第六期 | 控制台体验增强 | v0.10.0 / v0.12.0 / **v0.13.0** | ✅ 完成（FR-128/133/137/140/141 全真机验收通过，随 v0.13.0 发布） |

**版本切段（自 v0.12.0 tag 后）**——原计划「按主题切 v0.13~v0.16 四版」已放弃：实操时三~六期代码一并打进单个 v0.13.0 tag，故按实际拓扑归口：

- **v0.13.0** 三~六期累积交付：第三期运营底座与可观测收口 + 第六期控制台体验与规模化 + 第四期客户端 OTA 二三轮 + 第五期经济脊柱 FR-115~123 与背包代码 FR-124~127（本地 tag 69dd647，未 push）。
- **v0.14.0** 第五期背包域 FR-124~127 真机验收翻交付（CoreLib serverInfo 环境门槛本版解锁）+ 本轮收口修复（发布向导切步骤清暂存 / 探针写死玩家 / 业务台对象入参）+ 文档归正（版本标签对齐实际 tag 拓扑）。

> 产品成熟（1.0 后稳态）不再加「第 N 期」，改按版本（CHANGELOG / tag）+ 功能（§4 / specs）组织。某期是否完成看 §4 该期 FR 状态是否都 `已交付`。

## 8. 术语表

| 术语 | 含义 |
|---|---|
| Control Plane（控制面） | 唯一面向浏览器的 HTTP 入口 + 认证 / 调度 + gRPC 客户端池 + 内嵌前端静态文件 |
| Worker Node | 节点侧 Go 进程：gRPC 服务端 + 进程管理 + WS 终端 + 指标采集 |
| Bot Worker | Node.js 子进程：Mineflayer Bot + 行为引擎，stdin/stdout JSON IPC |
| ServerProbe | 游戏服内插件探针，反向 WS 连入本机 Worker；监控指标 + 业务对接 agent（ADR-016） |
| 数据根 | 单一项目内 FHS 式运行态数据目录（bin/etc/opt/var/cache），便携可迁移（ADR-010） |
| enrollment token | 新节点一次性、限时的准入凭据（一键安装，ADR-020） |
| manifest | 客户端 OTA / 业务能力清单；客户端 OTA 通过 HTTPS 拉取，使用拉取密钥鉴权，并以 sha256 校验制品完整性（ADR-054） |
| JBIS | 业务对接平台：经济 / 背包等域，CP/Worker/桥/UI 插件无关，探针侧适配器 + manifest |
| 制品库 | 内容寻址（sha256）资产库，去重 + md5/sha256 完整性校验（ADR-011） |
