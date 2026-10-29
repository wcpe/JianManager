**活跃 FR 详细规格索引**（PRD 只留索引行，详情见 spec）：
- FR-521～528（2026-10-23 业务能力退役与对外通道重构）→ `docs/specs/business-capability-retirement/spec.md` 等 6 个业务 spec
- FR-529～533（2026-10-23 配置能力从 Beacon 迁入 JM）→ `docs/specs/config-layered-override/spec.md` 等 4 个配置 spec
- FR-534～535（2026-10-23 业务插件可视化与批量粒度）→ `docs/specs/business-plugin-panel/spec.md`、`docs/specs/business-batch-and-confirmation/spec.md`
- **对外 API 契约**（FR-528）→ `docs/openapi/`
- FR-514～520（2026-10-22 AI Dashboard 与内置助手） → `docs/specs/ai-assistant/shared-contracts.md`
- FR-433~439（2026-09-20 实例角色/群组拓扑 MCP 能力与压测稳定性修复批）→ **七项均免 spec**
- FR-489（2026-09-28 MCP 端点无状态化与 Token 维度运维视图）→ `docs/specs/mcp-stateless-endpoint/spec.md`（+ **ADR-096**，部分取代 ADR-077 决策 5、修订 ADR-080 决策 6）；连带修订 FR-388/389/391、作废 FR-439
- FR-499（2026-10-01 采集登记路径与长临界区解耦）→ `docs/specs/log-ingest-register-lock/spec.md`；含**行级锁审计表**与忙期实测数字；**免 ADR**（不改进程模型 / 协议 / 数据模型，只改进程内锁纪律与锁序）；**不改 CP 侧任何配套**（§3.6 明确回答响应语义影响）
- FR-501（2026-10-02 日志成本治理：分层/采样/保留 + 冷层搬运） → `docs/specs/log-cost-governance/spec.md`
- FR-494（2026-09-29 QQ 机器人告警通知通道）→ `docs/specs/qqbot-notification-channel/spec.md`；增强 FR-085（新增第 9 种通道类型 `qq`），不改既有 8 种通道行为；**免 ADR**（复用既有通道模式，未引入新进程 / 协议 / 数据模型）
- FR-495（2026-09-29 QQ 机器人扫码接入） → `docs/specs/qqbot-group-discovery/spec.md`
- FR-473~484（2026-09-20 Worker 日志平台底座受控规格） → `docs/specs/worker-log-platform-contract/spec.md`
- FR-430（2026-09-10 客户端分发信息架构合并… → `docs/specs/client-dist-ia-merge/spec.md`
- FR-425~429（2026-09-07 客户端分发监控统计优化批… → `docs/specs/client-dist-machine-drilldown/spec.md`
- FR-412~424（2026-08-26 控制台与实例界面重设计批） → `docs/specs/console-log-view-redesign/spec.md`
- FR-342~345（2026-07-16 迭代批：搭建损毁重建 / 实例系统级指标 / 环境变量管理 / 历史日志） → `docs/specs/provision-damaged-rebuild`
- FR-335~340（2026-07-15 验收分诊批二：规模化与全栈补缺） → `docs/specs/topology-scale`
- FR-313（进程级崩溃诊断链路：环形缓冲 + 退出码快照上报持久化 + 前端崩溃诊断面板）→ `docs/specs/crash-diagnostics/spec.md`（需 spec，开发中创建）
- FR-314（实例启动同步预检：预检 RPC + 同步报错不进 STARTING）→ `docs/specs/start-preflight/spec.md`（需 spec，开发中创建）
- FR-305（反向隧道单消息尺寸上限统一治理：`ServerOptions()` 64MiB 在 grpctunnel 路径失效）→ `docs/specs/tunnel-message-size-guard/spec.md`（需 spec，开发中创建；宜随 FR-281 M1 一并处理）
- FR-304（文件上传链路流式化：client-stream UploadFile 与 DownloadFile 对称）→ `docs/specs/upload-streaming/spec.md`（✅ 已交付@v0.15.0）
- FR-298~301（节点运行时库：多运行时泛化 / 自动扫描 / Node.js 安装器 / Bot 接管 / 聚合刷新）→ `docs/specs/node-runtime-library/spec.md`
- FR-306~307（节点包管理器 + 多 registry + 全局包可视化管理）→ `docs/specs/node-package-management/spec.md`
- FR-308（bot-worker 下发部署 + NODE_PATH 全局依赖）→ `docs/specs/bot-worker-deploy/spec.md`
- FR-302（导入现有服务器：就地接管 / 搬迁托管区）→ `docs/specs/import-existing-server/spec.md`
- FR-128~162（控制台体验与可寻址性增强）→ `docs/specs/console-ux-enhancement/spec.md`
- FR-124~127（JBIS 背包域，⛔ 已退役 FR-521）→ `docs/specs/archive/business-integration/fr-124-127-inventory.md`
- FR-046（Sponge 子服支持）→ `docs/specs/provision-sponge/spec.md`（✅ 已交付@v0.13.0）
- FR-114（探针依赖内联 / 缓存预置）→ `docs/specs/probe-dependency-cache/spec.md`（✅ 已交付@v0.13.0）
- FR-073 / 078 / 079 / 080 / 082 / 083 / 084 / 085（ServerProbe 治理桥运营底座在途）→ `docs/specs/serverprobe-ops-inflight/spec.md`
- FR-003 / 041 / 042 / 046 / 059 / 098 / 113 / 114（在途杂项 / 归真 / 延后）→ `docs/specs/inflight-backlog/spec.md`
- FR-053（插件批量部署多服）→ `docs/specs/plugin-batch-deploy/spec.md` + `api.md`
- FR-163~169（前端整体重设计:视觉底座 / 双主题 / 多级分组 / 可组合工作区+超级工作台+导播台 / 监控升级）→ `docs/specs/ui-redesign/design.md`(+ 原型 `preview.html`)
- FR-173~175（CI 发布管线 / 出站网络代理 / 自更新对接 GitHub Releases，关联 ADR-036/037）→ `docs/specs/release-pipeline/`、`docs/specs/network-proxy/`、`docs/specs/self-update-github/`（开发中创建）
- FR-176~184（节点与运行时 UI 重做 / 自更新增强 / 全局任务中心 / jmctl 紧急控制台，关联 ADR-039/040/041/042 + ADR-036 更新） → `docs/specs/self-update-enhancement/spec.md`
- FR-185~190（出站代理面板化+节点级下发 / 更新页服务端缓存+markdown / 客户端分发迁运营+全流程向导重做 / 全站模态纪律+复制兜底 / 重度模态重做 / Worker 二进制 CP 下发，关联 ADR-043 + 增强 FR-174/182/086/072/009/004） → `docs/specs/proxy-visual-config`
- FR-191~194（客户端分发二轮重做：发布文件树+zip 上传 / 拉取密钥可查看 / updater-core 版本管理 / 端到端流程图，关联 ADR-044[FR-192 修订 ADR-022①] / ADR-045[FR-193 补充 ADR-021] + 增强 FR-187/086/091） → `docs/specs/client-dist-publish-redesign`
- FR-196~212（前端 mock API 与测试基座… → `docs/specs/frontend-mock-api/spec.md`
- BUG-A 节点重名覆盖（修 FR-004 注册身份匹配缺陷，见 ADR-039）
- FR-213~221（观测体系重构 + 客户端分发观测 + 共享文件浏览器 + 通知中心… → `docs/specs/file-browser-component`
- FR-222~224 + FIX-A~D（节点上线流程打通 + worker 生命周期修复 + .yml 约定） → `docs/specs/worker-self-setup`
- FR-225~232 + FIX-1~6（0.13.0 开发版归档） → `docs/specs/task-force-stop`
- FR-079 + FR-233~247 + FIX-7~9（UI 重塑 + Docker 落地 + 实例规模化 2026-06-30） → `docs/specs/instance-list-redesign/spec.md`
- FR-170（进程粒度监控采集） → `docs/specs/process-metrics/spec.md`
- FR-264（客户端分发单节点源站安全防护：多维限流 / IP 临时封禁 / key 状态机 / 频道降速保护 / 制品授权收紧 / 启动安全画像 / 独立防护中心）→ `docs/specs/client-dist-security-firewall/spec.md`（已交付@v0.13.0）
- FR-265（客户端分发观测四 Tab 重构：统计/监控/日志只看请求事件，客户端 Tab 独立看运行态与更新结果；新增运行态心跳表/端点、请求日志脱敏详情、实时聚合；清理废弃缓存命中指标；与 FR-264 并行开发互不覆盖）→ `docs/specs/client-dist-observability-rebuild/spec.md`（已交付@v0.13.0）
- FR-266（updater-core 构建元信息内嵌与展示：jar 内写版本 / Git commit / dirty / buildTime，CP 归档读取并在 Core 版本页展示，紧急 hotfix 仍可直接上传）→ `docs/specs/updater-core-build-metadata/spec.md`（已交付@v0.13.0）
- FR-267~272（高密度控制台重塑 2026-07-02） → `docs/specs/console-redesign/design.md`
- FR-273（通用组件包与控件博物馆）：抽出 `@jianmanager/ui` 通用 UI/token/charts 包，主应用消费组件包，并新增 `web/wiki` 控件博物馆展示第一版控件与常用状态矩阵 → `docs/specs/component-package-wiki/spec.md`；关联 ADR-058。
- FR-274（Bot 压测会话 YAML 动作编排与 50 Bot 稳定验收，增强 FR-042）→ `docs/specs/bot-stress-yaml-orchestration/spec.md`
- FR-275/276（CP↔Worker 专用 WS 令牌密钥下发 + 终端 401 诊断兜底：终端/插件桥令牌与用户会话密钥分离，经 gRPC 注册响应下发并持久化；修 FR-080 enroll 不下发密钥致生产终端 401、探针监控失效的产品缺口；修订 ADR-020，见 ADR-061） → `docs/specs/worker-ws-token-secret/spec.md`
- FR-277（SSH 推送式远程部署脚本：操作机经 SSH 密钥推送部署/更新面板与节点到 Linux 主机，环境变量配置；Worker 侧复用 ADR-051 两阶段语义，服务档位 system/user 双轨；见 ADR-063）→ `docs/specs/ssh-deploy-scripts/spec.md`（开发中，剩真机验收）
- FR-281（CP→Worker 指令反向流化：gRPC 反向隧道 + 双模式 + 终端 CP 中转，修订 ADR-002 与「浏览器↔Worker WS」不变量，见 ADR-066）→ `docs/specs/worker-reverse-stream/spec.md`（已落审；M1 指令隧道 → M2 终端中转；NAT 真机验收必验）
- FR-283~288（多语言 monorepo 统一布局与命令面 2026-07-12） → `docs/specs/monorepo-unification/spec.md`
- FR-293~297（导航与控制台缓存体验 2026-07-12… → `docs/specs/console-keepalive/spec.md`
- FIX-验收批一（2026-07-15 mock 整站验收 ~50 条源码分诊后的交互质量修复，fix 走 sdd-fix-bug，不占 §4）
- FR-346~350（客户端分发上传增效与外置对象存储 2026-07-17 迭代批… → `docs/specs/client-upload-efficiency/spec.md`
- FR-351~353（客户端更新器内嵌硬门禁 + 接入/版本面板信息补齐 + 楔子/core 本地日志轮换 2026-07-21… → `docs/specs/client-updater-guide-embed-info/spec.md`
- FR-354~355（对外部署与展示 2026-07-21… → `docs/specs/docker-compose-deploy/spec.md`
- FR-356~361（客户端分发安全/遥测/统计闭环 2026-07-21… → `docs/specs/client-dist-kpi-semantics/spec.md`
- FR-326（CP↔Worker 实例反向对账）→ `docs/specs/instance-reverse-reconcile/spec.md`（✅ 已交付@v0.20.0）
- FR-362/363/365/369~372（500+ Bot 分布式命令压测）
- FR-384~388（Agent 安全操控后台）
- FR-389~392（Agent MCP 长连接运维 + 调用观测）
- FR-373~378（文件权限底座 + 资源管理器批次）
- FR-393~394（节点离线归档与彻底清理）
- FR-395~399（Agent MCP 全域运维 + 500 Bot 资源容量闭环 2026-07-26） → `docs/specs/agent-capability-policy-v2/spec.md`
- FR-404（Bot 乱斗场景与受管稳定性压测） → `docs/specs/bot-combat-soak/spec.md`
- FIX-观测刷新 + FR-400~403（平台全景观测 2026-07-27） → `docs/specs/managed-resource-attribution/spec.md`
- FIX-公开注册 + FR-405（受控用户创建与邮箱邀请） → `docs/specs/user-invitation-and-registration-security/spec.md`
- FR-406~408（监控页全景观测与受管进程探查处置） → `docs/specs/managed-process-diagnostics-control/spec.md`
- FR-409（制品版本库与 ServerProbe 在线分发） → `docs/specs/artifact-version-library/spec.md`
- FR-410（Linux 用户级 systemd 版本化直装与节点部署） → `docs/specs/versioned-user-systemd-deploy/spec.md`
- FR-411（ServerProbe 本地上传来源） → `docs/specs/local-serverprobe-upload/spec.md`
- FR-425~429（客户端分发观测增强批）
- FR-430（客户端分发 IA 合并：两页 + 7 Tab + 旧路由重定向 + 守卫 + i18n）→ `docs/specs/client-dist-ia-merge/spec.md` + ADR-088。代码在分支 HEAD；✅ 已交付@v0.22.0
- FR-431~432（控制台六域导航 IA + 可配置权限树）→ `docs/specs/nav-ia-role-model/spec.md` + ADR-089。**线上验收通过**；UI 原型 gitignore 不入库。✅ 已交付@v0.22.0
- FR-473~484（**Worker 日志平台底座** 2026-09-20）
- FR-484（**canonical 事件体落盘 compact**，2026-09-24） → `docs/specs/worker-log-canonical-eventstore/spec.md`
- FR-502~507（**前端组件库与导航外壳重构** 2026-10-02，重构批，增强 FR-273 组件包 / FR-431~432 控制台导航 IA）
- 已交付 FR 的详情见对应 `docs/specs/<feature>/` 与 git 历史。
- **FR-445~470（生产就绪查漏补缺批 2026-09-21）**
  - FR-445 实例能力画像抽象（type × role capabilities）→ `docs/specs/instance-capability-profile/spec.md` + ADR-091
  - FR-446~447 MC 直探（SLP + Query）与采集优先级降级编排 → `docs/specs/mc-direct-probe/spec.md` + ADR-092（依赖 FR-451）
  - FR-448~450 详情界面多形态适配（backend / proxy / beacon / generic）→ `docs/specs/instance-detail-adaptation/spec.md`（依赖 FR-445）
  - FR-451 实例配置源明面化（启动参数 + server.properties，可引用文件）→ `docs/specs/instance-config-surface/spec.md` + ADR-092
  - FR-452~453 实例列表树表（可切换分组维度）与拓扑页完整网络视图 → `docs/specs/instance-list-and-topology/spec.md`（依赖 FR-445）
  - FR-454 beacon/binary 探针误配修正 → **fix，免 spec**（记 CHANGELOG）
  - FR-455~456 重启韧性加固与孤儿进程周期兜底 → `docs/specs/restart-resilience/spec.md` + ADR-093（**P0 上线阻塞**）
  - FR-457~458 集群级滚动/分批/灰度编排与配置批量下发收敛 → `docs/specs/cluster-ops-orchestration/spec.md`
  - FR-459 实例健康巡检与自愈 → `docs/specs/instance-health-selfheal/spec.md`（依赖 FR-455/456/462）
  - FR-460 失效 bot 自动回收与补足 → `docs/specs/bot-reclaim/spec.md`
  - FR-461~462 集群健康总览墙与异常自动检测（动态基线）→ `docs/specs/cluster-observability/spec.md`
  - FR-463~464 SLO/可用性统计与容量趋势预测 → `docs/specs/capacity-and-slo/spec.md`
  - FR-465 性能归因（含 GC 采集）→ `docs/specs/performance-attribution/spec.md`
  - FR-466 服务器快照与一键回滚 → `docs/specs/instance-snapshot-rollback/spec.md`
  - FR-467 运行期配额强制 → `docs/specs/runtime-quota-enforcement/spec.md`
  - FR-468 二进制/Beacon 版本管理与升级 → `docs/specs/binary-version-management/spec.md`
  - FR-469 跨实例排行与玩家在线趋势 → `docs/specs/cross-instance-ranking/spec.md`
  - FR-470 崩溃诊断增强 → `docs/specs/crash-diagnostics-enhancement/spec.md`

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
| FR-099 | 客户端 OTA 更新进度窗口（进度条 + 速度 + ETA） | — | ✅ 已交付@v0.8.0·端到端真机复验：updater-core（Java8）真下载 + reconcile + sha256 完整性 + 文件级增量/托管区减量/防降级；进度模型 ProgressModel/ProgressView 11 单测绿、SwingProgressView 真桌面（非 headless）代码路径无崩溃。原生 Swing 弹窗人眼目视（动条+实时速度/ETA）已于 2026-07-14 经真启动器 `-javaagent:wedge.jar` 场景由用户确认通过，无剩余尾巴 |
| FR-103 | 插件桥（Bukkit/BC 插件 WS 连入，旧自写） | — | ❌ 已废弃（ADR-014） |
| FR-107 | 后台客户端更新器接入指引 | — | ✅ 已交付@v0.8.0·验收经 FR-187 覆盖 |
| FR-108 | 仪表盘总览环分级配色与负载量纲修正 | — | ✅ 已交付@v0.9.1·验收经 FR-062 覆盖 |
| FR-109 | 服务器状态页显示打磨 | — | ✅ 已交付@v0.9.1·验收经 FR-142 覆盖 |
| FR-110 | 系统更新页未配源仍展示当前版本 | — | ✅ 已交付@v0.9.1·验收经 FR-186 覆盖 |
| FR-111 | 归档/反编译查看器布局优化 | — | ✅ 已交付@v0.9.1·全真栈验收 |
| FR-112 | 平台/运维导航信息架构统一 | — | ✅ 已交付@v0.9.1·验收经 FR-268 覆盖 |
| FR-113 | 全文索引后台化与进度 | P2 | ✅ 已交付@v0.13.0·四档验收 |
| FR-114 | 探针依赖内联/缓存预置 | P3 | ✅ 已交付@v0.13.0·全真栈验收（真机验收：libraries+assets 缓存包随建服下发 6.3MiB，探针首启免联网拉主依赖） |
| FR-115 | 业务桥 Worker 脊柱（JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-122 覆盖 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-116 | CP 业务编排与汇聚脊柱（JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-122 覆盖 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-117 | ServerProbe 业务对接层骨架（JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-122 覆盖 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-118 | 经济 Provider（只读，JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-122 覆盖 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-119 | 业务掌控台 UI v1（manifest 驱动，JBIS M1） | — | ✅ 已交付@v0.10.0·验收经 FR-123 覆盖 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-120 | 经济 Provider（写，JBIS M2） | — | ✅ 已交付@v0.10.0·验收经 FR-123 覆盖 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-121 | 业务写横切硬化（幂等 + 二次确认 + 审计，JBIS M2） | — | ✅ 已交付@v0.10.0·验收经 FR-123 覆盖 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-122 | 经济汇聚与多区聚合（JBIS M2） | — | ✅ 已交付@v0.10.0·四档验收 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-123 | 经济定制页（JBIS M2） | — | ✅ 已交付@v0.10.0·四档验收 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-124 | 扩 AllinInventorySync api 导出读写门面 | P2 | ✅ 已交付@v0.14.0·真机验收：CoreLib serverInfo 解锁（黄金 config + MySQL/Redis）后经真 AIS 2.1.0 api 读 view（getPlayerInventory 真背包，含离线）+ 基础属性写（getInventoryWriteApi.writeBasicAttrs 带 WriteResult 回执）真机验通；物品写门面按 ADR-0017 降级不在范围 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-125 | 背包 Provider | P2 | ✅ 已交付@v0.14.0·真机验收：三插件（CoreLib 1.3.0 + AIS 2.1.0 + ServerProbe）同栈起通，view 返真背包（EMERALD×9…含 nbtBase64）+ writeBasicAttrs 精确生效且幂等 + TrackedItemActionEvent（JOIN_CARRY）订阅汇聚；过程修复探针解码不符嵌套契约写死玩家 bug（c6f287a / ServerProbe 8b5974b） **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-126 | 背包汇聚与存储 | P2 | ✅ 已交付@v0.14.0·真机验收：writeBasicAttrs → JM business.write 审计 + AIS EDIT_ATTRS；追踪事件 JOIN_CARRY 按 domain+dedupKey 汇聚落 business_events（id=1，node/instance 归属正确，payload 全对）；业务事件读视图实例级权限收敛已落地 **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
| FR-127 | 背包定制页 | P2 | ✅ 已交付@v0.14.0·真机验收：背包定制页（/super 背包卡）真背包快照 + 基础属性写二次确认端到端；业务掌控台 object 型入参下发修复（8b568d3，真机复验 writeBasicAttrs 嵌套 base/edited 下发 success） **⛔ 已退役（FR-521）：业务能力整体移出 JM，由下游业务台持有；本 FR 仅作历史记录。** |
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
| FR-222 | worker 免配置自启 setup（节点上线丝滑化，参考 GitHub Actions Runner 下载/上线分离） | P1 | ✅ 已交付@v0.13.0·四档验收 |
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
| FR-244 | 全局动画系统统一 | P2 | ✅ 已交付@v0.13.0 |
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
| FR-261 | 发布页文件资源管理器（专业文件树库重写）：react-arborist 替换 ClientFileTree，新建文件夹/重命名/Ctrl+点选/Shift+连选/Delete 删除/拖拽上传文件/拖拽上传 zip 自动解压（含 GBK）/拖拽移动调整结构/同名冲突提示忽略覆盖保留两者；保留本地编排→点发布才上传架构（feat，需 spec） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-262 | 清理目录树形右键菜单可视化：FR-261 同款文件树展示目录结构，右键菜单标记清理/排除/取消，Ctrl+点选 Shift+连选批量操作，颜色区分（红=清理/绿=排除/无色=不管理），父子联动；产出 managedDirs+cleanExclude（增强 FR-255，依赖 FR-261，需 spec） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-263 | 拉取密钥加密器自动生成与持久化：CP 启动未注入 JIANMANAGER_CLIENT_KEY_ENC_SECRET 时自动生成 AES-256-GCM 密钥并持久化到数据根文件（env 注入优先、双轨），dev 回退内置开发密钥；修订 ADR-044 存储策略；后续增强：密钥列表/编辑支持永不过期展示、设置/清空过期时间，明文弹窗标题不拼接密钥名且复制按钮稳定可用（feat，需 spec + ADR） | P1 | ✅ 已交付@v0.13.0·四档验收 |
| FR-264 | 客户端分发单节点源站安全防护：在无 CDN 部署下补多维限流、字节配额、IP 临时封禁与手动解封、key 状态机、频道降速保护、制品授权收紧、启动安全画像遥测（playerName+machineId+installId+环境特征强制上报）与独立防护中心页面（feat，需 spec）→ `docs/specs/client-dist-security-firewall/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收 |
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
| FR-275 | CP↔Worker 专用 WS 令牌密钥自动下发 | P0 | ✅ 已交付@v0.13.0·全真栈验收（无手工 secret 场景端到端：注册自动下发→终端 101） |
| FR-276 | 终端 WS 密钥不一致 401 诊断兜底：CP 终端代理探测 Worker 握手 401 时返回结构化诊断，前端显示「终端令牌被 Worker 拒绝，疑似该节点 WS 密钥与平台不一致」而非裸「连接已断开」，与网络类断连区分；OPERATIONS 标注密钥同步要求与手动核对修复法（需 spec，并入 worker-ws-token-secret；依赖 FR-275 先落）→ `docs/specs/worker-ws-token-secret/spec.md` | P1 | ✅ 已交付@v0.13.0·四档验收（正向真机通过；诊断分支单测覆盖，修复后 401 无法自然复现） |
| FR-277 | SSH 推送式远程部署脚本 | P1 | ✅ 已交付@v0.15.0 |
| FR-278 | CP 内嵌 Worker 二进制（一键安装/升级不依赖 GitHub） | P1 | ✅ 已交付@v0.15.0 |
| FR-279 | JDK 下载受限网络可用性与失败引导：JDK 一键下载（temurin 等 GitHub 源）在无代理环境 TLS 超时（真机复现 `adoptium/temurin21-binaries ... TLS handshake timeout`）——失败信息改为可操作引导（明示可在设置页配置出站代理、或切换 JDK 镜像源，附入口跳转），并真机验证 Worker 侧 JDK 下载确实经 ADR-043 心跳下发的出站代理生效（增强 FR-033/178/186，免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（代理下发链路 L1 证+引导 UI 上线；网络失败态 env-blocked 已 DOM 证） |
| FR-280 | 出站代理面板提示与连通性测试自定义目标：① 代理设置区补大白话提示，列明哪些流量经此代理（JDK 下载、Worker 资产/自更新拉取、探针依赖、客户端分发外呼等）；② 连通性测试目标从写死 GitHub 改为可输入任意 URL 测试，默认 `https://www.google.com`（提示文案同步去掉「（GitHub）」硬编码）（增强 FR-185/229，免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（任意 URL 实测+可编辑输入框线上 bundle 命中） |
| FR-281 | CP→Worker 指令反向流化（免 NAT / 零入站端口暴露） | P1 | ✅ 已交付@v0.15.0 |
| FR-282 | install-worker.sh 同机多节点 systemd unit 名冲突（fix，增强 FR-222/080/277） | P1 | ✅ 已交付@v0.15.0 |
| FR-283 | 仓库根 pnpm workspace + 前端提升（ref，需 spec） | P2 | ✅ 已交付@v0.16.0 |
| FR-284 | devmock 抽为独立包（ref，免 spec）：`web/src/mocks`→`packages/devmock`，依赖反转（只依赖 API 契约，不再 `import '@/…'` app 内部）；主应用与测试改引 `@jianmanager/devmock`；生产构建仍不含 devmock（ADR-047 不变量保持）。依赖 FR-283 先落 | P2 | ✅ 已交付@v0.16.0 |
| FR-285 | Go 入口迁入 apps（ref，免 spec）：`cmd/*`→`apps/*`（control-plane/worker/jmctl）；`internal/` 不动；同步 `Makefile`/`release.yml`/`Dockerfile.*` 的 go build 路径；go:embed 目标目录不变。main 包无 importer，行为不变（关联 ADR-064） | P2 | ✅ 已交付@v0.16.0 |
| FR-286 | bot-worker 迁入 apps（ref，免 spec）：`bot-worker`→`apps/bot-worker`；同步 Go worker 运行时 spawn 路径解析与打包；须真机验 bot 真入服 spawn 成功（关联 ADR-064）。依赖 FR-285 | P2 | ✅ 已交付@v0.16.0 |
| FR-287 | 统一命令面 go-task（feat，需 spec）：引入 `Taskfile.yml` 作唯一命令入口，各语言暴露 build/test/lint/dev 同套动词委托 go/pnpm/gradlew，Windows+Linux 双跑通；`Makefile` 收敛为被委托层（关联 ADR-064）→ `docs/specs/monorepo-unification/spec.md`。依赖 FR-283/285 路径落定 | P2 | ✅ 已交付@v0.16.0 |
| FR-288 | 前端版本接上单一真源（`internal/version/version.go` 为唯一真值，构建期注入前端，改一处即三处一致） | P2 | ✅ 已交付@v0.16.0 |
| FR-289 | JDK 安装 arch 别名归一化 | P2 | ✅ 已交付@v0.15.0·真机验收（amd64→x64 归一装成；foobar→422 INVALID_ARCH） |
| FR-290 | JDK 下载停滞看门狗与中断错误归网络类（增强 FR-183/279）：下载流中途被掐（代理/跨境线路抖动）时任务无停滞检测，卡在中间进度直至整体超时兜底（真机复现：经 SOCKS5 代理下载 temurin 21 卡 48% 约 9 分钟），且最终错误 `写入临时文件失败: context canceled` 未被 FR-279 归为网络类 → 失败引导（去配代理/换镜像）不触发。加下载停滞看门狗（N 秒零进度 → 快速失败）并把停滞/中断类错误纳入 FR-279 网络类归类与引导（免 spec） | P2 | ✅ 已交付@v0.15.0·单测+真机回归（停滞归网络类/强停不误标单测覆盖；真停滞待自然复现看引导渲染） |
| FR-291 | JDK 安装失败残留目录清理与重试自愈（fix，增强 FR-183）：安装失败/被取消时已建的目标目录不清理（真机复现：卡死任务遗留 4KB 半截 `temurin-21/` 脚手架），此后同版本重装永远撞「目标目录已存在」，须登机手动 rm 才能解——核心操作被死锁在需要 SSH 的境地。改为安装失败即清理本次所建目标目录；安装前若目标目录存在但无完成标记（无 `bin/java`）则视为残骸自动清除重装；完好已装目录仍拒绝覆盖（语义不变）（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（预植残骸自愈装成+完好目录仍拒，同机正反对照） |
| FR-292 | 删除托管 JDK 清理到托管根顶层目录 | P2 | ✅ 已交付@v0.15.0·真机验收（嵌套删除无父壳、邻居 JDK 完好、拒删托管根） |
| FR-293 | 侧栏常驻服务器列：收藏置顶+最近打开（增强 FR-240，补当年搁置的 Part B 常驻实例列）：「选择服务器」按钮下方常驻两区=收藏（置顶）+最近打开（LRU ≤8），行=状态点+名称，点击进该服控制台；行内星标收藏/取消，与搜索弹窗收藏互通（同一 localStorage）；已收藏不重复出现在最近区；双空显示引导；侧栏折叠图标轨态不显示；不为侧栏引入高频轮询（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（收藏/最近渲染、点击客户端跳转进服、与选择器星标互通、双空引导） |
| FR-294 | 页眉集群统计浮窗（增强 FR-162/179，复用 FR-216 铃铛浮窗范式）：在线节点/运行中服务器/崩溃服务器三徽标点击改弹缩略浮窗——节点行=名称+状态点+运行实例数、服务器行=名称+节点+在线人数、崩溃行=名称+节点+崩溃原因（statusReason）；行点击进对应服控制台/节点页，底部「查看全部」保留原筛选页跳转；数据仅浮窗打开时拉取，行数上限 8 超出提示剩余数；崩溃空态友好文案（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（节点浮窗 4 节点各显运行数、运行服浮窗名称+节点+在线、查看全部页脚） |
| FR-295 | 服务器控制台页签 keep-alive 与终端连接管理器（增强 FR-269）：9 个页签访问过即保活、切走不卸载切回瞬时；终端页签往返 WS 不断、滚动缓冲与输入不丢；文件/配置页签目录展开态与未保存草稿保留；终端连接抽为实例级连接管理器（组件订阅制，组件卸载不再直接断 WS）；离开实例整体释放（需 spec+ADR：前端 keep-alive 与终端连接管理架构，React 19.2 `<Activity>`/CSS hidden 取舍进 spec）→ `docs/specs/console-keepalive/spec.md` | P2 | ✅ 已交付@v0.15.0·真机验收（8+ 页签往返终端 WS 握手恒为 1、隐藏页签 DOM 保留、缓冲连续） |
| FR-296 | 跨服务器控制台热缓存（增强 FR-269，**依赖 FR-295**）：最近打开的 ≤3 个服控制台整体保活（LRU 淘汰），A↔B 切换双向瞬时呈现；缓存中的服终端 WS 保持连接、缓冲连续；闲置超时（默认 10 分钟不可见）自动断 WS 降级为界面状态缓存、再进自动重连；LRU 淘汰即完整卸载释放（与 FR-295 同 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（跨服切换两页同存 DOM、组件不重建、WS 存活缓冲连续；闲置 10min 断连+重连、4 台 LRU 淘汰环境受限未真机、单测覆盖） |
| FR-297 | 实例域数据层缓存调优（增强 FR-269/240）：实例详情/指标/serverState 等实例域查询拉长 staleTime/gcTime + placeholderData，回切先呈现缓存数据后台刷新；侧栏常驻列/服务器选择器行悬停预取实例详情（防抖）；现有 vitest 全绿不回归（免 spec） | P2 | ✅ 已交付@v0.15.0·真机验收（跨服回切瞬时呈现无白屏，热缓存命中） |
| FR-298 | 节点运行时库底座与自动扫描 | P1 | ✅ 已交付@v0.15.0·真机验收（扫描命中植入JDK→登记(managed=false)→重扫already去重→删外部登记留文件） |
| FR-299 | Node.js 运行时一键安装（feat，需 spec 并入 node-runtime-library）：nodejs.org dist 便携归档安装（index.json 版本解析、托管目录布局同 JDK），经节点出站代理（ADR-043）；停滞看门狗 / 网络失败引导 / 残骸自愈 / 删除顶层清理与 JDK 链路同语义（FR-289~292 齐平）。依赖 FR-298 | P1 | ✅ 已交付@v0.15.0·真机验收（直连 nodejs.org 装 Node 22.23.1 落盘 bin/node、amd64→x64 归一、任务中心进度、托管删除连文件；no_proxy 绕过 SOCKS 代理直连） |
| FR-300 | bot-worker 接托管 Node 运行时（增强 FR-006 Bot 域，需 spec 并入 node-runtime-library）：Bot spawn 由裸 `exec("node")` 改为优先用节点运行时库登记/托管的 Node（可指定，无则回退 PATH 保兼容），消灭新节点 PATH 缺 node 时 Bot 起不来的脆弱点。依赖 FR-298 | P1 | ✅ 已交付@v0.15.0·真机验收（bot spawn 日志 nodeSource=managed-scan + node=托管 Node 路径，host-2 无系统 node 必用托管） |
| FR-301 | CP 运行时库存聚合缓存与刷新（feat，增强 FR-178 运行时资产页，需 spec 并入 node-runtime-library）：Overview 扩**多运行时矩阵**（节点 × 类型 × 版本 + 引用关系），CP 缓存各节点库存快照、显「上次同步时间」+ **手动刷新**（强制 syncFromWorker），同步失败容忍显旧数据。依赖 FR-298 | P1 | ✅ 已交付@v0.15.0·真机验收（overview 多运行时矩阵+runtimeSyncs+syncedAt、refresh 逐节点结果、node-windows 离线容忍显 None） |
| FR-302 | 导入现有服务器向导（feat，节点级，需 spec + 新 ADR 落地 ADR-007 预留的「导入已有目录」高级模式）：浏览节点目录（复用 FR-178 BrowseDir）→ Worker 探测核心 jar / 内嵌 JDK / `server.properties` 端口 → 导入时二选一：**就地接管**（目录不动，托管区外例外 + **删除实例不删原目录**守则）或**搬进托管区**（移动/拷贝到系统分配目录）→ 登记实例（结构化启动 ADR-008）+ 探到的 JDK 顺带入运行时库 → `docs/specs/import-existing-server/spec.md` | P1 | ✅ 已交付@v0.15.0·真机验收（就地：导入启动Done于原目录+删除原目录完好；搬迁：源清空+托管区落位+Done+删除连目录清） |
| FR-303 | 审计日志 action 名 i18n（增强审计域，免 spec）：全量已知 action 键（`instance.start` 等 60+）中英映射（`audit.actions.*`），审计页显示翻译、悬停/角标露原键、未知键回退原样不崩；守则：本批 FR-298~302 新增审计 action 随身带中英翻译 | P2 | ✅ 已交付@v0.15.0·真机验收（线上bundle含70键中英翻译，审计记录新action；未知键回退单测覆盖） |
| FR-304 | 文件上传链路流式化（增强 FR-009 文件域 + FR-052/053 插件部署，需 spec） | P1 | ✅ 已交付@v0.15.0 |
| FR-305 | 反向隧道单消息尺寸上限统一治理（fix/健壮性，增强 FR-281 隧道传输 + FR-010/114 探针部署，需 spec + 宜随 FR-281 M1 一并做） | P1 | ✅ 已交付@v0.15.0 |
| FR-306 | 节点包管理器与多 registry 配置（feat，泛化 FR-298 运行时库 + 承 FR-299/300 Node 生态，需 spec） | P1 | ✅ 已交付@v0.15.0 |
| FR-307 | 全局运行时包可视化管理 | P1 | ✅ 已交付@v0.16.0 |
| FR-308 | bot-worker runtime 下发部署 + 经全局包运行（feat，节点级，需 spec + 新 ADR 修订 ADR-006，依赖 FR-306+299） | P1 | ✅ 已交付@v0.16.0 |
| FR-309 | 节点删除实例守卫（fix，增强 FR-004 节点管理）：删除节点当前不校验其名下实例，节点没了实例记录成孤儿——终端/操作全 404「节点不存在」（v0.15.0 真机复现：删离线测试节点后其 nat-echo 实例悬挂，用户直撞 404）。节点删除时有实例则 409 拒绝并列出实例（离线节点允许 `?force=` 显式级联删除记录并明示不清理远端文件）（免 spec） | P1 | ✅ 已交付@v0.17.0 |
| FR-310 | 删除运行中实例的进程处置一致性（fix，增强 FR-005/FIX-C） | P1 | ✅ 已交付@v0.17.0 |
| FR-311 | 节点页管理体验补全（增强 FR-033/177，真机测试反馈）：① JDK 已登记行加**编辑**入口（改厂商/版本/arch/路径，走既有 PUT /nodes/:id/jdks/:jid，模态遵守 ui-modals）；② 节点详情头四个统计球排版紧凑化（现占满整行空隙过大，改小尺寸+左聚拢与信息区同排）（免 spec） | P2 | ✅ 已交付@v0.16.0 |
| FR-312 | 实例启动失败原因可见性（增强 FR-269/294，真机复现：实例未绑 JDK 点启动只见「启动中→崩溃」无因——statusReason 已入库已出 API，但实例控制台页无展示位，且 Worker 心跳把 CRASHED 冲回 STOPPED 后卡片唯一展示位 `status===CRASHED` 条件失效）：实例控制台页加失败原因横幅（statusReason 非空即显、标注「上次启动失败」，下次启动由既有 transition 清空），工作台卡片展示条件同步放宽为非空即显（免 spec） | P1 | ✅ 已交付@v0.16.0 |
| FR-313 | 进程级崩溃诊断链路（feat，需 spec）：Worker 维护实例输出环形缓冲（最后 N 行）+ 捕获退出码/信号/运行时长，进程退出生成崩溃快照经 gRPC 上报 CP 持久化（保留最近 K 次）；前端实例控制台「崩溃诊断」展示快照（时间/退出码/尾部输出）——补齐进程秒退（如 jar 缺失）全链路零痕迹盲区（现 proto 不带原因、Worker 不留崩溃日志、终端仅实时流）→ `docs/specs/crash-diagnostics/spec.md` | P1 | ✅ 已交付@v0.17.0 |
| FR-314 | 实例启动同步预检（feat，增强 FR-005 启停链路，需 spec）：点启动 CP 先同步调 Worker 预检（JDK 绑定/可解析、启动目标 jar 或命令存在、工作目录就绪），失败同步返回具体错误（前端弹错+横幅），状态不进 STARTING；过检才转 STARTING 走既有异步启动——终结「配置错误也要启动中→崩溃兜一圈」→ `docs/specs/start-preflight/spec.md` | P1 | ✅ 已交付@v0.16.0 |
| FR-315 | 实例状态光晕视觉（增强 FR-269/240 控制台视觉）：实例控制台容器与工作台实例卡片外圈按状态渐变光晕——RUNNING 绿色柔光、STARTING/STOPPING 琥珀呼吸、CRASHED 红色警示光、STOPPED 无光晕；随双主题取色、平滑过渡动画、遵守 UI 风格偏好不过度（免 spec） | P2 | ✅ 已交付@v0.16.0 |
| FR-316 | 一键搭建版本-JDK 兼容预检（增强 FR-034 搭建向导，FR-308 真机钓出：选 MC 26.1 + 节点最高 Temurin 21 可搭建成功但启动必崩「requires running the server with Java 25」）：向导选定 MC 版本后按核心 Java 需求映射校验所选/默认 JDK，不满足即表单级阻断并引导去 JDK 面板安装；与 FR-314 启动预检互补（搭建时拦 vs 启动时拦）（免 spec） | P2 | ✅ 已交付@v0.17.0 |
| FR-317 | 内存水位启动守卫 | P0 | ✅ 已交付@v0.16.0 |
| FR-318 | CP 压力态 SQLite 嵌套事务错排查（fix，FR-317 事故现场钓出：主机 OOM/swap 风暴恢复初期，实例 stop 转换反复报 `SQL logic error: cannot start a transaction within a transaction`，删 bot 报 INTERNAL_ERROR，重启 CP 即愈）：排查 transition/删除链路在慢 IO 下是否存在事务嵌套或连接状态破坏（glebarez/sqlite 单连接语义），补压力回归测试 | P2 | ✅ 已交付@v0.17.0 |
| FR-319 | 一键搭建异步化 + 全程可观测 | P0 | ✅ 已交付@v0.16.0 |
| FR-320 | API 错误统一落平台日志（feat，真机钓出：provision 失败等 API 错误只回 HTTP 响应，无任何 slog→FR-049 日志中心平台日志恒空，用户「乱七八糟的问题都看不到日志」）：全局 gin 中间件把 4xx 业务拒绝（422/502 等）与 5xx 的 [路径、状态码、error body、用户、IP] slog 化（4xx=warn、5xx=error），经既有 log_slog 桥自动落日志中心 platform 源；核对 /logs 页导航入口可见性（免 spec） | P1 | ✅ 已交付@v0.16.0 |
| FR-321 | 审计日志记录失败操作与错误内容（增强 FR-050，真机钓出：搭建失败审计零记录）：AuditLog 加 success/error 字段，关键写操作（实例生命周期/搭建/删除/上传）失败也记审计并带错误内容；审计页列表失败徽章 + 详情展开错误（免 spec） | P1 | ✅ 已交付@v0.16.0 |
| FR-322 | 心跳流断开告警降噪（fix，真机复现：CP journal 2 小时 484 条「心跳流断开 context canceled」WARN——worker 每拍心跳收完响应即 cancel 属正常收尾，被当异常刷屏淹没真问题，且无节点标识）：Canceled/EOF 正常收尾降 Debug，异常断流保留 WARN 并带 nodeUUID（免 spec） | P2 | ✅ 已交付@v0.16.0 |
| FR-323 | 长操作任务化 | P1 | ✅ 已交付@v0.16.0 |
| FR-324 | 短操作 loading 与文件传输进度补齐（增强 FR-075 反编译 / FR-068 探针 / FR-008/070 文件——用户「1-3 秒短任务要 loading 或进度条」）：① 反编译、探针部署点击后 loading 且完成前防重复点；② 资源管理器 新建/重命名/删除/保存 文件 loading 反馈；③ 文件上传显示百分比进度条（axios onUploadProgress），下载有反馈（浏览器原生下载条 + 内联「下载中」提示）。纯前端 UX 补齐，无数据模型/API 变更（免 spec） | P2 | ✅ 已交付@v0.16.0 |
| FR-325 | Worker reconnect 失败孤儿进程接管兜底 | P1 | ✅ 已交付@v0.17.0 |
| FR-326 | CP↔Worker 实例反向对账（feat，FR-310 孤儿评估缺口 (b)）：Worker 恢复/在跑的实例若 CP 已无记录会永远跑（正向对账已有：CP 以心跳为真源置 STOPPED；反向缺失）。Worker 心跳携带在管实例清单，CP 对无记录者下发处置指令（需防误杀护栏：宽限期+人工确认开关），proto+双侧改造（需 spec，后续批次）→ `docs/specs/instance-reverse-reconcile/spec.md` | — | ✅ 已交付@v0.20.0·真机孤儿检测/dispose/Worker 清理闭环 |
| FR-327 | 页眉任务中心下拉面板（增强 FR-183 任务中心）：点击页眉任务中心入口弹下拉面板（最近 N 条任务：kind/名称/stage 进度/终态徽章），面板内点条目跳任务中心对应项；运行中任务有进度反馈；模态外点击关闭（免 spec） | P2 | ✅ 已交付@v0.17.0 |
| FR-328 | 一键搭建后端子服 MC 版本下拉不可滚动（fix，增强 FR-034 搭建向导，真机复现）：向导后端子服步骤的 MC 版本下拉列表超出可视高度后无法滚动，长版本列表不可选。修复下拉内容区滚动（受模态内 overflow 约束影响一并核查），并核查向导内其他下拉同病（免 spec） | P1 | ✅ 已交付@v0.17.0 |
| FR-329 | 任务中心进度自动刷新（增强 FR-183/319，真机反馈「搭建进度要手动刷新才动」）：任务中心页与页眉任务入口在存在非终态任务时自动轮询刷新（TanStack Query refetchInterval，运行中 ~2s/空闲停轮询），任务到终态即停；SSE 回推列为后续可选不在本 FR（免 spec） | P1 | ✅ 已交付@v0.17.0 |
| FR-330 | 服务器核心节点级缓存（增强 FR-034/319 搭建链路 + FR-179 制品缓存，真机痛点：Paper CDN 节点侧 ~200KB/s，47MB 核心每次搭建都重新下载）：Worker DownloadCore 先查节点核心缓存（sha256 已知按 sha 键、否则 core+version+build 键，落数据根 cache 区），命中跳过远程下载直接复制/链接入实例目录；未命中下载完成校验后入缓存再交付；缓存条目在节点制品缓存面板可见可清理；provision 任务 stage 文案区分「缓存命中/下载中」（免 spec） | P1 | ✅ 已交付@v0.17.0 |
| FR-331 | 搭建中实例硬性禁止启动（fix，增强 FR-319 二轮②启动闸，真机反馈：statusReason「正在下载核心（完成前请勿启动）」仅文案提示，仍可点启动）：provision 任务未终态的实例，前端启动按钮直接禁用（tooltip 指明搭建中+引导看任务中心），后端启动闸核查补漏确保各入口（单启/批量/一键）一律 4xx 拒绝——文案劝阻升级为状态强制（免 spec） | P1 | ✅ 已交付@v0.17.0 |
| FR-332 | 折叠态导航分类图标点击跳转分类首页（fix，增强 FR-131/181 侧边栏折叠，真机复现：折叠后点击分类图标无反应无法跳转）：折叠态下点击分类图标默认导航到该分类下第一个页面（并展示激活态）；展开态行为不变（免 spec） | P2 | ✅ 已交付@v0.17.0 |
| FR-333 | CP 本机应急密码重置子命令（feat，运维应急能力，用户要求「补重置密码命令」以解锁验收环境）：`jianmanager-cp reset-password -u <用户名> [-p <密码>] [-c <config>] [-list]`——bcrypt 重置指定用户密码并解锁账号（Status→Active），`-p` 留空自动生成 16 位随机密码打印，`-list` 列出全部用户名。命令落 CP 二进制自身而非 jmctl：数据库仅 CP 可读写（架构不变量），jmctl 按 ADR-041 不得直连 DB（免 spec） | P1 | ✅ 已交付@v0.17.0 |
| FR-334 | 控制台品牌顶栏贯通（T 型外壳）+ 页眉节点作用域下线（feat，增强 FR-162/181/268 控制台外壳，用户走查「侧栏与页眉交界处错位成台阶、页眉节点作用域下拉几乎无作用」） | P2 | ✅ 已交付@v0.17.0 |
| FR-335 | 群组拓扑规模化 | P2 | ✅ 已交付@v0.17.0 |
| FR-336 | 用户目录服务端搜索分页 + 候选下拉虚拟化 | P2 | ✅ 已交付@v0.17.0 |
| FR-337 | 任务中心列表分页信封 | P2 | ✅ 已交付@v0.17.0 |
| FR-338 | 备份存储配置编辑 | P2 | ✅ 已交付@v0.17.0 |
| FR-339 | 实例控制台玩家/备份定时分区接真 | P2 | ✅ 已交付@v0.17.0 |
| FR-340 | 指标批量序列接口 | P1 | ✅ 已交付@v0.17.0 |
| FR-341 | daemon 实例在 Worker 重启后存活并自动重连 | P0 | ✅ 已交付@v0.17.0 |
| FR-342 | 搭建失败损毁态 + 一键重建 | P1 | ✅ 已交付@v0.18.0 |
| FR-343 | 实例系统级基础指标（无需探针，走系统直取） | P1 | ✅ 已交付@v0.18.0 |
| FR-344 | 实例环境变量管理（双区展示 + 可编辑写入 .env）（feat，增强 FR-034 搭建/结构化启动，用户要求「加环境变量 tab、支持自配启动环境变量写入 .env」） | P2 | ✅ 已交付@v0.18.0 |
| FR-345 | 未运行/历史日志查看（终端回放 DB 历史 + 独立历史增强） | P2 | ✅ 已交付@v0.18.0 |
| FR-346 | 客户端分发上传协议增效 | P1 | ✅ 已交付@v0.18.0 |
| FR-347 | 制品库外置对象存储底座 | P1 | ✅ 已交付@v0.18.0 |
| FR-348 | 制品库存量迁移工具（feat，依赖 FR-347）：存储渠道页发起「迁移到渠道 X」后台任务——逐制品校验搬运→更新 Asset 存储标注→删源；进度可见/失败明细/中断幂等续跑，迁移后下载按新位置路由（需 spec）→ `docs/specs/artifact-storage-migration/spec.md` | P1 | ✅ 已交付@v0.18.0 |
| FR-349 | 制品索引可视化与 S3 一致性对账（feat，依赖 FR-347）：制品列表加存储位置/对账状态列；对账任务（手动触发 + 定期默认每日）比对 Asset 索引与 S3 对象清单，产出缺失（索引有 S3 无）/孤儿（S3 有索引无）差异报告；处置走显式按钮——缺失标记失效并提示重传、孤儿二次确认后一键清理，不做全自动修复（需 spec）→ `docs/specs/artifact-s3-reconcile/spec.md` | P2 | ✅ 已交付@v0.18.0 |
| FR-350 | 发布编排器文件树定点增强：多级目录一次建 + 右键定点添加（feat，增强 FR-191/250/261）：「新建文件夹」支持输入 `a/b/c/d` 自动创建整条层级、支持多行一次建多个；右键目录菜单增「上传文件到此 / 上传文件夹到此」，所选内容落到该目录路径下（免 spec，纯前端——FileExplorer 右键基座与 emptyDirs 机制已有） | P2 | ✅ 已交付@v0.18.0 |
| FR-351 | 发版强制内嵌楔子与 updater-core（feat，增强 FR-193/发布管线）：`make embed-client-updater` / `release.yml` 在 jar 缺失或 0 字节时硬失败并中文报错；正式包禁止缺件；本地 dev 仍可缺 jar 启动并返回 `JAR_NOT_EMBEDDED`（免 spec） | — | ✅ 已交付@v0.19.0 |
| FR-352 | 接入指引补齐内嵌更新器信息 + 版本面板旁路摘要（feat，增强 FR-107/260）：`client-channels?tab=guide` 展示展示版本串、core 整数版本、wedge/core available/size 与下载；未内嵌明确失败态；频道版本列表/详情/发布页旁路展示「内嵌更新器 vX · core N」摘要；本批不含 commit/buildTime 元信息（需 spec）→ `docs/specs/client-updater-guide-embed-info/spec.md` | — | ✅ 已交付@v0.19.0 |
| FR-353 | 楔子与 updater-core 本地诊断日志轮换压缩（feat，增强 FR-090）：`wedge.log` / `updater.log` 各自在打开时若 ≥10MiB 或跨自然日则轮换，旧文件时间戳命名并 gzip，保留最近 5 个归档；轮换失败 fail-open 不挡启动；阈值代码常量、不做运营可配（需 spec）→ `docs/specs/updater-local-log-rotate/spec.md` | — | ✅ 已交付@v0.19.0 |
| FR-354 | Docker Compose 与镜像部署可跑通（feat，对外 compose 路径产品化） | — | ✅ 已交付@v0.19.0 |
| FR-355 | Control Plane 一键下载安装脚本（feat）：仓库提供从 GitHub Releases latest 按 OS/arch 拉取 `control-plane` 的 curl\|bash（及可选 PowerShell）；职责=下载+可执行权限+启动提示；失败可诊断；不做 systemd/Windows 服务化；Worker 仍文档化面板一键装（需 spec）→ `docs/specs/cp-oneclick-install/spec.md` | — | ✅ 已交付@v0.19.0 |
| FR-356 | 客户端分发 KPI 口径与共享语义（feat，增强 FR-095/217/218/219/265）：统一定义「下载健康 / 更新健康 / 活跃客户端精确\|近似 / 安全微标」字段名与计算公式；频道统计 Tab 与分发监控统计 Tab 共用语义（可抽共享组件）；文案区分请求成功 vs 更新成功；允许只读聚合补全缺字段（需 spec）→ `docs/specs/client-dist-kpi-semantics/spec.md` | — | ✅ 已交付@v0.19.0 |
| FR-357 | 分发统计与监控信息加厚 + 分布钻取（feat，增强 FR-095/218/219/249/265）：KPI 绝对数、下载 bytes 趋势、错误码 TopN、失败样例、日志列加厚（player/machine/core/bytes/耗时/可读 err）、详情结构化、分布点击过滤日志/客户端；空态区分无流量/未开遥测/窗外；依赖 FR-356 口径（需 spec）→ `docs/specs/client-dist-stats-monitor-enrich/spec.md` | — | ✅ 已交付@v0.19.0 |
| FR-358 | 安全中心研判处置闭环 + 频道安全摘要（feat，增强 FR-096/264）：画像详情全量字段与风险时间线、事件行一键封 IP/改 key 态/频道防护（DangerConfirm）、频道工作台安全摘要条链到安全中心；安全日志 detail 结构化；不可信字段统一角标；允许频道安全摘要聚合 API（需 spec）→ `docs/specs/client-dist-security-ops-loop/spec.md` | — | ✅ 已交付@v0.19.0 |
| FR-359 | 分发三页跨页深链与统一筛选状态（feat，增强 FR-265/264）：`channelId`/`ip`/`machineId`/`errCode`/`version`/`tab` 等 query 在频道工作台、分发监控、安全中心互通；分布/榜单/事件一键跳转保留筛选；不改后端业务语义（免 spec） | — | ✅ 已交付@v0.19.0 |
| FR-360 | updater-core 遥测与启动画像字段补齐 + 隐私契约（feat，增强 FR-094/264）：客户端上报补齐 core 版本、arch、javaVendor、locale/timezone、memoryTier 等运营诊断字段；明确 required vs diagnostic；隐私 notice/opt-out 与面板展示脱敏规则；允许 schema/落库扩展（需 spec）→ `docs/specs/client-telemetry-enrich-privacy/spec.md` | — | ✅ 已交付@v0.19.0 |
| FR-361 | 分发统计与安全日志 CSV 导出（feat，增强 FR-095/249/264）：管理端导出当前筛选窗口的统计汇总/事件日志/安全日志 CSV；仅平台管理员；导出列遵守 FR-360 隐私脱敏；限流防拖库（需 spec）→ `docs/specs/client-dist-csv-export/spec.md` | — | ✅ 已交付@v0.19.0 |
| FR-362 | Bot 发压节点池与分布式调度底座（feat，增强 FR-038/042/274，500+ Bot 地基） | — | ✅ 已交付@v0.19.0 |
| FR-363 | 可验证 Bot 场景引擎与塔防预设 | — | ✅ 已交付@v0.19.0 |
| FR-364 | ServerProbe 压测断言桥与塔防事件适配（feat，增强 FR-065/066）：定义通用压测事件契约，由可修改的塔防插件发布进入子服/房间、房间准备、开局、波次、伤害、击杀、死亡、重生、结束等事件，ServerProbe 经既有反向 WS→Worker→CP 链路上报并关联运行/Bot/房间/请求；命令、到达和攻击仅在收到可信回执后算成功，重复/乱序/迟到事件不得误判（需 spec，依赖 FR-363；不以聊天文本解析作为正式判据）→ `docs/specs/bot-load-probe-events/spec.md` | — | ❌ 已废弃（未实施，由 FR-369 取代） |
| FR-365 | Bot 长稳重连、进程恢复与状态归真（feat，可靠性增强） | — | ✅ 已交付@v0.20.0 |
| FR-366 | 压测运行状态机、三类负载曲线与自动判定（feat） | — | ❌ 已废弃 |
| FR-367 | 压测场景模板与创建向导前端（feat，完整 Bot 压测配置体验） | — | ❌ 已废弃 |
| FR-368 | 实时压测观测、故障诊断与报告前端（feat，完整 Bot 压测运行体验） | — | ❌ 已废弃 |
| FR-369 | 通用 Bot 命令编排与场景边界修订（feat，取代 FR-364，增强 FR-363/354） | — | ✅ 已交付@v0.20.0 |
| FR-370 | 通用命令压测运行状态机、负载曲线与自动判定（feat，取代 FR-366） | — | ✅ 已交付@v0.20.0 |
| FR-371 | 通用命令压测模板与创建向导前端（feat，取代 FR-367） | — | ✅ 已交付@v0.20.0 |
| FR-372 | 通用命令压测实时观测、诊断与报告前端（feat，取代 FR-368）：会话级聚合 SSE 驱动概览、Bot/失败/事件分页、命令调度与发送进度、屏障、Worker 健康和报告；固定五类失败，支持 action trace、失败子集重试、停止/取消、JSON/CSV 导出。持续标注 `bot.chat` 成功边界，TPS/MSPT/ServerProbe 仅有值时作为 optional legacy 展示（需 spec，依赖 FR-370，可与 FR-371 非依赖部分并行）→ `docs/specs/bot-load-observability/spec.md` | — | ✅ 已交付@v0.20.0·Playwright mock 会话页 + 后端 stream 真连 |
| FR-373 | 文件元数据与权限底座（feat）：`ListFiles`/`BrowseDir` 条目扩展 mode/readable/writable（相对 Worker 进程用户）及可取属主信息；写前 `CheckWritable`；单 path 非递归 `ChmodPath`（二次确认，不 chown/不 root）；permission denied 中文诊断（需 spec，跨 Worker-CP-Web）→ `docs/specs/file-permission-foundation/spec.md` | — | ✅ 已交付@v0.20.0 |
| FR-374 | 导入现有服务器：手输绝对路径 + 权限引导 + 写配置预检（增强 FR-302）：DirectoryPicker 保留并加路径输入；inspect/读目录失败展示诊断与「尝试修复」(FR-373)/换路径/migrate；提交或写关键配置前可写预检，不过则阻断（需 spec，依赖 FR-373）→ `docs/specs/import-permission-path-ux/spec.md` | — | ✅ 已交付@v0.20.0 |
| FR-375 | 资源管理器单窗 Windows 对齐体验（增强 FR-070/071/141/008/031）：可编辑地址栏 + 前进后退历史栈 + 鼠标侧键 + 有栈时拦截浏览器后退；详情列头排序（名/时间/类型/大小/权限）+ 详情/列表/大图标视图切换与偏好记忆；列表/树滚动隔离不带动整页；权限列与锁标 + 编辑器可写态 + 保存/写路径统一预检与「尝试修复」（需 spec，依赖 FR-373）→ `docs/specs/file-explorer-windows-ux/spec.md` | — | ✅ 已交付@v0.20.0 |
| FR-376 | 多实例壳：主区多标签 + 应用内浮动窗 + 浏览器新标签路由（feat）：标签多路径/多文件；弹出可拖拽缩放浮动窗并可收回；独立路由新开浏览器标签深链 path（需 spec，依赖 FR-375 体验契约）→ `docs/specs/file-explorer-multi-window/spec.md` | — | ✅ 已交付@v0.20.0 |
| FR-377 | 跨窗拖放 + 统一剪贴板总线（feat）：主区↔浮动窗↔跨浏览器标签 DnD 与剪切/复制/粘贴同一总线；与既有 rename/move/单文件 copy 语义对齐；系统文件拖入仍 HTML5 drop（需 spec + 新 ADR，依赖 FR-376）→ `docs/specs/file-explorer-cross-window-dnd/spec.md`、ADR-078 | — | ✅ 已交付@v0.20.0 |
| FR-378 | 统一文件管理器壳 + 能力注入，全场景接入（feat）：统一导航/视图/列表树/编辑槽/多窗宿主；Capability 注入（canWrite/canChmod/canUpload/标记清理/编排等）；接入实例文件与配置、平台存储、分发编排与预览、导入选目录等，**不删**分发标记等业务能力（需 spec，依赖 FR-375/376；可与 FR-377 部分并行）→ `docs/specs/file-explorer-unified-shell/spec.md` | — | ✅ 已交付@v0.20.0 |
| FR-384 | Agent Token 模型 + CP 侧鉴权 | — | ✅ 已交付@v0.20.0·真机签发/鉴权/吊销 |
| FR-385 | jm-agent 管理 CLI（feat，仿 jmctl） | — | ✅ 已交付@v0.20.0·真机 whoami/启停 |
| FR-386 | mcp-bridge 独立进程（feat）：新 Go 二进制 `apps/mcp-bridge/`，stdio 默认（远端 IDE 经 HTTPS CP）；工具集仅暴露 CLI 等价的 action 子集；硬拒绝面永不对 MCP 暴露；CP 403 → MCP `isError=true` 含中文拒绝原因；不引入数据库/gRPC/Worker；不暴露独立网络监听（需 spec + 新 ADR-077，依赖 FR-384）→ `docs/specs/mcp-bridge/spec.md` | P1 | ❌ 已废弃（由 FR-389/392 取代：MCP 改 CP 内嵌 Streamable HTTP/SSE，删除 stdio 独立进程） |
| FR-387 | Agent Token 管理 UI（feat）：设置 → Agent Token 页 = 列表 / 新建 / 详情 / 吊销 / 复制命令片段；非管理员 403；UI 契约仅附在 FR-384 spec 末尾（免 spec：纯前端消费 FR-384 API，无新模型/新契约/新 ADR，依赖 FR-384；建议并行 worktree） | — | ✅ 已交付@v0.20.0 |
| FR-388 | Agent 入口可证安全闸（feat） | — | ✅ 已交付@v0.20.0 |
| FR-389 | CP 内嵌 MCP 长连接服务（feat，取代 FR-386 远程入口形态） | — | ✅ 已交付@v0.20.0·真机 initialize/tools/call/踢线（**会话运维模型由 FR-489 取代**：协议会话已移除，SSE 兼容路径保留） |
| FR-390 | Agent 调用流水与 Token 活跃（feat） | — | ✅ 已交付@v0.20.0·真机 mcp/jmagent client 可区分 |
| FR-391 | Agent 观测 UI（feat）：平台管理下 MCP 会话页（列表/踢线/超时说明）+ 调用流水页（按 token/action/client/成功失败/时间筛）+ Agent Token 列表展示 `lastUsedAt` 与 24h 次数 + 审计页可筛 actor=agent / action 前缀 `agent.`；复制「IDE 连接 CP MCP URL」片段；非管理员 403（免 spec：纯前端消费 FR-389/390 API，UI 契约附两 FR spec 末节；依赖 FR-389/390） | — | ✅ 已交付@v0.20.0·DOM 7/7（**MCP 会话页被 FR-489 取代**：改为 Token 活动视图） |
| FR-392 | 退役 mcp-bridge + jmagent 客户端标识 | — | ✅ 已交付@v0.20.0·mcp-bridge 删除 + CI/契约清理 |
| FR-393 | 节点离线归档列表与详情（feat，增强 FR-048 软删可发现性） | — | ✅ 已交付@v0.20.0 |
| FR-394 | 归档节点彻底清理（feat，增强 FR-309 清理闭环） | — | ✅ 已交付@v0.20.0 |
| FR-395 | Agent 能力策略 v2 与节点继承实例作用域（feat，增强 FR-384/389） | P0 | ✅ 已交付@v0.21.0 |
| FR-396 | MCP 节点与实例全生命周期强类型工具 | P0 | ✅ 已交付@v0.21.0 |
| FR-397 | MCP 文件、配置与插件运维强类型工具 | P1 | ✅ 已交付@v0.21.0 |
| FR-398 | MCP Bot 舰队与压测编排全工具 | P0 | ✅ 已交付@v0.21.0 |
| FR-399 | 500 Bot 全链路资源采样与容量报告（feat，增强 FR-343/362/370） | P0 | ✅ 已交付@v0.21.0 |
| FR-400 | 受管资源归因与首页仪表 Tooltip | P1 | ✅ 已交付@v0.21.0 |
| FR-401 | Bot Worker 历史观测与聚合统计（feat，增强 FR-362/399）：常驻采样 Bot Worker RSS、事件循环 P95、活跃/连接中数、真实容量上限及 Go Worker RSS/CPU，按执行节点、目标实例与压测会话关联查询；不以共享进程 RSS 伪造单 Bot 占用。时序留存复用 ADR-013 的 raw/5m/1h 三档（需 spec，依赖 FR-400 的唯一实时快照契约）→ `docs/specs/bot-runtime-observability/spec.md` | P1 | ✅ 已交付@v0.21.0 |
| FR-402 | 平台全景观测总览与下钻（feat，增强 FR-220）：平台首页聚合健康、资源、节点/实例状态、告警、任务与 Bot，展示 TopN/异常项并下钻节点或实例详情，不在首页全量铺开实体表；跨节点 Bot 事件循环 P95 取可用节点最大值（需 spec，依赖 FR-400/401）→ `docs/specs/platform-observability-overview/spec.md` | P1 | ✅ 已交付@v0.21.0 |
| FR-403 | 日志中心平台与节点实例视图分流（feat，增强 FR-150/215）：同一路径分为「平台日志」（仅 CP）与「节点/实例日志」（Worker+实例，按节点/实例聚合筛选）两个主视图；平台管理员保留受控全量检索。复用既有日志表与筛选接口（免 spec） | P1 | ✅ 已交付@v0.21.0 |
| FR-404 | Bot 乱斗场景与受管稳定性压测 | P0 | ✅ 已交付@v0.21.0 |
| FR-405 | 受控用户创建与邮箱邀请（fix + feat，修 FR-001/002/156）：移除匿名注册；平台管理员单请求创建账号；管理员签发仅组成员、7 天、可撤销的一次性邀请，邮件链接由独立 SMTP 与系统设置的统一 HTTP/HTTPS 公共基址投递，后续绝对链接复用该基址，受邀者自助设置凭据（需 spec + ADR-082）→ `docs/specs/user-invitation-and-registration-security/spec.md` | P0 | ✅ 已交付@v0.21.0 |
| FR-406 | 监控页全景观测整合与默认多指标对比（feat，增强 FR-221/400/401/402）：`/monitor` 成为平台/节点/实例观测入口，默认选中对比指标，展示平台健康、异常 TopN、资源归因、共享 Bot Worker 与受管进程并支持 URL 下钻；首页健康与共享 Bot 区块保留在 `/`（免 spec） | P1 | ✅ 已交付@v0.21.0 |
| FR-407 | 受管进程详细探查与异常诊断（feat，增强 FR-170/400）：按 `instanceId+pid` 查询受管实例当前进程树内 PID，返回脱敏命令摘要、父子关系、CPU/RSS/IO/运行时长/线程数可用性，并结合历史样本输出带证据的 RSS 增长、CPU 高占用、IO 高写入、采样陈旧/不足诊断（需 spec）→ `docs/specs/managed-process-diagnostics-control/spec.md` | P1 | ✅ 已交付@v0.21.0 |
| FR-408 | 受管进程细粒度处置与多种关闭方式（feat，增强 FR-005/170/400）：根进程继续复用实例优雅停止/重启/强制终止；非根子进程提供 `terminate` 与 `kill_tree`，Worker 执行瞬间重验 PID 仍属目标实例树，前端 DangerConfirm + 后端 confirm 双保险并写脱敏审计（需 spec）→ `docs/specs/managed-process-diagnostics-control/spec.md` | P1 | ✅ 已交付@v0.21.0 |
| FR-409 | 制品版本库与 ServerProbe 在线分发 | P1 | ✅ 已交付@v0.21.0 |
| FR-410 | Linux 用户级 systemd 版本化直装与节点部署 | P1 | ✅ 已交付@v0.21.0 |
| FR-411 | ServerProbe 本地上传来源（feat，增强 FR-409）：为 `serverprobe` 制品包新增 `local-upload` 内置来源，管理员上传 `.jar` 即流式入现有 CAS 并立即形成可选版本；来源与版本明确区分 GitHub Releases（线上拉取）和本地上传，同名版本按来源消歧；复用全局→Worker→实例选择和 CP-local Worker 拉取，不自动改默认或升级已有实例（需 spec + ADR-085）→ `docs/specs/local-serverprobe-upload/spec.md` | P1 | ✅ 已交付@v0.21.0 |
| FR-412 | 实例顶栏瘦身与实例切换器（feat，增强 FR-293/296）：删除 `服务器控制台 /` 前缀与无信息副标题，7 格 MetaCell 网格改为一行 pill 指标条（可收起并记住偏好），实例名即切换器（复用最近/收藏 + 模糊搜索），切换命中热集不重建终端连接；顶栏无横幅时 ≤ 90px（免 spec） | P1 | ✅ 已交付@v0.22.0 |
| FR-413 | 实例 Tab 重组（feat，增强 FR-037）：「环境变量」并入「文件配置」形成分段，10 → 9 个 Tab 并加图标与分组分隔线，1366px 宽下不横向滚动；`?tab=env` 旧深链自动落到新分段（免 spec） | P1 | ✅ 已交付@v0.22.0 |
| FR-414 | 终端热集上限治理（feat，增强 FR-296/ADR-067）：`HOT_SET_SIZE` 由写死 3 改为可配（默认 6），按实际可见终端数动态提升保活集，闲置断连策略随之调整，为分屏与多 window 场景铺底（免 spec） | P1 | ✅ 已交付@v0.22.0 |
| FR-415 | 控制台输出/输入分离（feat，取代 Terminal 的 xterm 直通输入）：后端为 stdin 管道而非 pty，故输出区改 DOM 虚拟列表（只读、结构化行模型、MC/Java 日志行解析、ANSI 解析、5000 行环形缓冲），输入改真 `<input>` 命令栏（需 spec + ADR-086）→ `docs/specs/console-log-view-redesign/spec.md` | P0 | ✅ 已交付@v0.22.0 |
| FR-416 | 命令栏智能输入（feat，依赖 FR-415）：历史按实例持久化（上限 500、相邻去重）、`^R` 模糊搜索、候选式 Tab 补全（命令 + 第二段玩家名，ghost 预览不盲补）、多行粘贴确认（逐行/仅首行/取消）（免 spec） | P1 | ✅ 已交付@v0.22.0 |
| FR-417 | 输出区观察能力（feat，依赖 FR-415）：日志级别过滤、行内搜索与高亮计数、跳到底部浮标、复制范围菜单（选区/可见屏/全缓冲/仅错误/存 .log），并为停机日志回放、崩溃诊断、启动失败横幅三处补复制按钮（免 spec） | P1 | ✅ 已交付@v0.22.0 |
| FR-418 | 大范围选取与堆栈块复制（feat，依赖 FR-415）：跨行改用锚点选区（行号区间 + 边缘自动滚动，文本取自数据源而非 DOM，规避虚拟列表选区断裂），单行内保留原生字符级选取；Java/MC 异常按堆栈形态归并为可折叠块并支持一键复制整块（需 spec，沉浸 pane 直接复用鼠标选区与复制能力）→ `docs/specs/console-log-view-redesign/spec.md` | P1 | ✅ 已交付@v0.22.0 |
| FR-419 | 历史日志无限回溯（feat，增强 FR-150/345）：`/logs` 由 OFFSET/LIMIT 增加 `(time,id)` 游标分页以消除向上翻页时的 offset 漂移；控制台滚到顶自动加载更早并保持滚动锚定，提供「跳到本次启动」与「跳到时间点」，横幅标明回溯位置与数据层级。归档 NDJSON（超保留期）不纳入本次范围（需 spec）→ `docs/specs/console-log-view-redesign/spec.md` | P1 | ✅ 已交付@v0.22.0 |
| FR-420 | 控制台沉浸工作台（feat，依赖 FR-415/414）：`F11` 进入全屏沉浸态（侧栏/顶栏/Tab 退场），顶部工作台头部承载当前实例、TPS/在线/CPU、可见布局操作与退出；不注册 tmux 前缀键或底部状态栏（需 spec + ADR-087）→ `docs/specs/console-immersive-workspace/spec.md` | P2 | ✅ 已交付@v0.22.0 |
| FR-421 | 沉浸工作台可视化分屏（feat，依赖 FR-420/418）：通过可见按钮提供左右/上下分屏、添加/切换实例、聚焦、最大化和关闭 pane（最多 4 个）；复制直接复用 FR-418 鼠标锚点/堆栈复制，不提供键盘复制模式（需 spec）→ `docs/specs/console-immersive-workspace/spec.md` | P2 | ✅ 已交付@v0.22.0 |
| FR-422 | 实例界面视口自适应骨架（feat，增强 FR-269/296）：实例路由内容区由 `min-h-[520px]` + 整页滚动改为 `flex h-full` 骨架——顶栏与 Tab 栏 `flex-none`、内容区 `flex-1 min-h-0`、滚动收口到卡片内部，使顶栏常驻可见且底部不再留白（屏幕越大原浪费越多）；分段栏与工具栏合并为单条横栏。仅覆盖实例详情路由，不改其他页面滚动模型（需 spec）→ `docs/specs/instance-layout-redesign/spec.md` | P1 | ✅ 已交付@v0.22.0 |
| FR-423 | 实例各 Tab 分栏重排与空态收缩（feat，依赖 FR-422）：按内容宽度需求分栏（窄内容与宽表格分列），等高栏改为不撑高矮卡，空态高度按内容走；概览的「最近事件 / 关注事项 / 崩溃诊断」三个低信息量卡合流为单条「动态与告警」时间线（需 spec）→ `docs/specs/instance-layout-redesign/spec.md` | P1 | ✅ 已交付@v0.22.0 |
| FR-424 | 宽表格列宽与稀疏表治理（feat，依赖 FR-422）：宽表格显式约束列宽与总宽上限，避免主列无限扩张导致名称与元数据之间断开视觉连接；超宽稀疏表（在线玩家、白名单等 2 列表）改行卡片形态，复用 FR-195 的行卡片范式（免 spec） | P1 | ✅ 已交付@v0.22.0 |
| FR-425 | 分发三页统一自定义时间筛选（feat，增强 FR-265/357/359）：频道工作台/分发监控/安全中心统一时间范围组件——保留既有预设档并新增任意起止日期时间（后端 from/to RFC3339 已支持，纯前端组件化），URL 深链同步进 FR-359 体系；区间超 14 天明细保留窗时自动标注「活跃机器为近似值」（消费 activeMachinesExact；免 spec） | P1 | ✅ 已交付@v0.22.0 |
| FR-426 | 机器级更新清单与钻取（feat，增强 FR-217/265）：选时间段列出每台机器（machineId）更新次数/最近更新时间/版本滞后，支持排序分页；点机器看该机器更新事件时间线；新后端查询端点（需 spec → `docs/specs/client-dist-machine-drilldown/spec.md`，**spec 待补**） | P1 | ✅ 已交付@v0.22.0 |
| FR-427 | 更新活动时间线/热力图（feat，增强 FR-218）：按天×小时热力图消费既有小时桶 series；入分发运维页统计 Tab（免 spec，纯前端） | P2 | ✅ 已交付@v0.22.0 |
| FR-428 | 分发概览洞察卡（feat，增强 FR-357）：运维总览洞察卡——同环比、异常标记、KPI 口径解释（需 spec → `docs/specs/client-dist-insight-cards/spec.md`，**spec 待补**） | P1 | ✅ 已交付@v0.22.0 |
| FR-429 | 分发三页布局统一重排（ref，依赖 FR-425~428）：信息架构=洞察卡→时间筛选→概览数字→趋势/热力→明细；随 FR-430 两页化收敛（免 spec） | P2 | ✅ 已交付@v0.22.0 |
| FR-430 | 客户端分发信息架构合并（观测页并入独立「客户端分发运维」页）（feat，增强 FR-215/264/265/356~359/425~429）：三页收敛为「客户端分发」`/client-channels` +「客户端分发运维」`/client-dist-ops`（7 Tab）；旧路由透传重定向；request 日志双视图合一；两页补平台管理员守卫；i18n `clientDistOps.*`。纯前端（需 spec + ADR-088）→ `docs/specs/client-dist-ia-merge/spec.md` | P1 | ✅ 已交付@v0.22.0 |
| FR-431 | 控制台六域导航 IA（feat，增强 FR-268/215/272）：顶层收敛为首页/服务器/群组网络/工作台/观测/客户端分发/平台设置；业务 URL 不变；`/templates` 迁出内容分发；`/alerts` 维持无入口；侧栏可见性改由权限节点裁剪（依赖 FR-432）→ `docs/specs/nav-ia-role-model/spec.md` | P1 | ✅ 已交付@v0.22.0 |
| FR-432 | 可配置权限树与角色模板（feat，增强 FR-002/156）：静态能力域权限目录 + 角色模板/用户绑定/用户 allow-deny 覆盖（deny 永胜；**超管唯一**、不可撤销/降权）+ `RequireAnyPerm`/`requireNodes` 路由与写路径门禁 + `/permissions` 管理页；单平台多用户（ADR-089，不推翻 ADR-004）→ `docs/specs/nav-ia-role-model/spec.md` | P0 | ✅ 已交付@v0.22.0 |
| FR-433 | 实例角色可变更与 beacon 配套服务角色（feat，增强 FR-032/ADR-007） | P1 | ✅ 已交付@v0.23.0 |
| FR-434 | 群组服 Network 与代理注册的 MCP 工具（feat，增强 FR-032/335） | P1 | ✅ 已交付@v0.23.0 |
| FR-435 | 实例分组 MCP 工具（feat，增强 FR-165/ADR-033） | P1 | ✅ 已交付@v0.23.0 |
| FR-436 | daemon 实例 wrapper 已死而 Java 仍活时的孤儿接管（fix，缺陷修复 FR-325 同源场景） | P0 | ✅ 已交付@v0.23.0 |
| FR-437 | 压测容量快照陈旧导致的伪容量不足（fix，缺陷修复 FR-398） | P1 | ✅ 已交付@v0.23.0 |
| FR-438 | bot-worker 大规模舰队的事件循环过载（fix，缺陷修复 FR-398） | P0 | ✅ 已交付@v0.23.0 |
| FR-439 | MCP 会话并发上限造成永久 429（fix，缺陷修复 FR-389） | P1 | ❌ 已废弃 |
| FR-440 | 实例标签 MCP 工具（feat，增强 FR-395/047） | P1 | ✅ 已交付@v0.23.0 |
| FR-441 | 通用二进制运行时搭建（feat，增强 FR-319/FR-342，见 ADR-090） | P1 | ✅ 已交付@v0.23.0 |
| FR-442 | 内置 Beacon 快速搭建预设（feat，依赖 FR-441，见 ADR-090） | P1 | ✅ 已交付@v0.23.0 |
| FR-443 | 向 Beacon 推送拓扑变更（feat，依赖 Beacon FR-222，见 ADR-090） | P2 | ✅ 已交付@v0.23.0 |
| FR-444 | 从 Beacon 拉取拓扑并映射为分组树 | P2 | ✅ 已交付@v0.23.0 |
| FR-445 | 实例能力画像抽象（feat，见 ADR-091）：实例按「类型 × 角色」声明自身能力画像（`capabilities`：有哪些 Tab、依赖探针否、是否 MC 语义），前端据画像显隐内容而非 `if role==='proxy'` 硬编码；新增产品类型只需注册描述符。**验收**：新增一种角色/类型，详情页自动按其声明显隐 Tab，前端零硬编码改动；`generic`/`beacon`/`backend`/`proxy` 四种画像各自正确 | P1 | ✅ 已交付@v0.23.0 |
| FR-446 | MC 直探能力（SLP + Query）（feat，依赖 FR-451，见 ADR-092）：新增 Minecraft **Server List Ping**（零配置，取 MOTD/版本/在线人数/最大人数/favicon）与 **GameSpy4 Query**（需开 `enable-query`，取实名玩家名单/插件列表/地图名）协议客户端，不依赖探针。**验收**：对任意在跑 MC 服不装探针即可拿 MOTD/版本/人数；开启 query 后可拿实名玩家名单；明文 http 拒绝、超时可配 | P1 | ✅ 已交付@v0.23.0 |
| FR-447 | 采集优先级与降级编排（feat，依赖 FR-446）：统一采集编排——**有探针走探针 → 无探针走直探 → 都没有显示"不可用"**（不报错、不伪装 `-1`）；同一指标多源时以探针为准。**验收**：同一实例三态（探针+直探 / 仅直探 / 都无）下详情页均正确呈现，无探针不留占位符垃圾值 | P1 | ✅ 已交付@v0.23.0 |
| FR-448 | 详情界面按能力画像适配（feat，依赖 FR-445）：详情页按 role × type × 探针有无 三轴显隐 Tab 与内容；隐藏 MC 专有 Tab（世界/区块/TPS/插件）对非 MC 实例。**验收**：backend / proxy / beacon / generic 四种实例打开详情页，Tab 与内容各不相同且均合理 | P1 | ✅ 已交付@v0.23.0 |
| FR-449 | BC（proxy）专用界面（feat，依赖 FR-445/447）：BC 实例专属视图——子服列表与各自状态、跨服玩家分布与全网总数、BC 自身运行指标、BC 配置管理（config.yml 关键项）。**验收**：BC 实例可见上述四块，且与已有拓扑/注册关系数据一致、不重复维护 | P2 | ✅ 已交付@v0.23.0 |
| FR-450 | 二进制 / beacon 专用界面（feat，依赖 FR-445）：非 MC 实例专属视图——进程运行指标（CPU/内存/线程/句柄/时长/重启次数）、端口与主动健康检查（HTTP GET/TCP 探活）、配置文件管理、启动命令/参数编辑；**保留基础文件目录管理与 CPU 等基础监控**；抽象到可适配更多产品。**验收**：beacon 与通用二进制实例均呈现该视图且无 MC 专有 Tab | P1 | ✅ 已交付@v0.23.0 |
| FR-451 | 实例配置源明面化（feat，见 ADR-092）：实例启动参数（startCommand/JVM 参数）与 `server.properties` 关键项在详情页**明面可编辑**，并支持**直接引用外部文件**（内联值 vs 文件引用二选一）；不再藏于犄角旮旯。**验收**：可在详情页直接改启动参数与关键配置项并持久化；可切换为引用文件模式且生效 | P1 | ✅ 已交付@v0.23.0 |
| FR-452 | 实例列表树表 + 可切换分组维度（feat，依赖 FR-445）：实例列表由平铺卡片改为**多级可折叠树表**，分组维度**可切换**（标签 region/zone 默认 / 实例分组树 / 网络 / 角色 / 类型 / 节点 / 不分组）；无分组信息的实例（bc/beacon/独立服务）自然落「未分组」。**验收**：64 台一屏可管、点击分组头折叠展开、切换维度即时重排、未分组与二进制实例正确归位 | P1 | ✅ 已交付@v0.23.0 |
| FR-453 | 拓扑页重做：完整网络视图（feat，依赖 FR-452）：拓扑由「仅已注册关系」改为**完整网络视图**（含未注册实例与配套服务），层级从所选分组维度推导（默认标签 region/zone），节点带健康着色与负载标签。**验收**：全部实例上拓扑（不再只 12 台）、层级正确、健康/负载可见、布局均衡 | P1 | ✅ 已交付@v0.23.0 |
| FR-454 | 修正 beacon/binary 探针误配（fix，缺陷修复 FR-441/442）：binary/beacon 实例仍被分配 `ProbePort`，心跳每拍对它们抓 `/metrics` 必失败；`probe_update` 仅排除 `proxy`、未排除 beacon/binary，可对其推送无效 Bukkit jar。现两处补排斥；并归正线上 `beacon` 实例 `type`（`minecraft_java`→`generic`）。**验收**：binary/beacon 不再分配探针端口、心跳不再抓取、`probe_update` 对其返回明确拒绝 | P1 | ✅ 已交付@v0.23.0 |
| FR-455 | 重启韧性加固 | P0 | ✅ 已交付@v0.23.0 |
| FR-456 | 孤儿进程周期检测与兜底（feat，依赖 FR-455，见 ADR-093）：现有清理只发生在 Worker 启动时——`wrapper` 运行期死亡、`direct` 硬崩、`docker` 未恢复三条路径产生的孤儿/状态错位**无周期兜底**。新增运行期周期扫描 + 反向对账覆盖 direct/docker。**验收**：运行期制造 wrapper 死/Java 活、direct 孤儿、docker 残留，均在有限周期内被识别并按策略处置（不静默、不误杀） | P0 | ✅ 已交付@v0.23.0 |
| FR-457 | 集群级滚动/分批/灰度运维编排（feat，增强 FR-058/139）：`instance_batch` 现为无状态并发扇出（固定 16 并发、一次性、无分批/暂停/失败即停），无法安全滚动重启。新增**分批/间隔/失败即停/暂停继续/按比例灰度**语义与进度、取消。**验收**：可对 60+ 台执行「每批 5 台、批间隔 N 秒、失败即停」的滚动重启；有进度与取消 | P1 | ✅ 已交付@v0.23.0 |
| FR-458 | 配置批量下发与跨实例收敛（feat，增强 FR-031）：配置链路现全单实例，跨实例只有只读端口冲突检查。新增「配置模板推送到 N 台」「跨实例配置漂移检测」「一键收敛到基线」。**验收**：可把一份 `server.properties` 推送到选定分组并在漂移时检出与收敛 | P1 | ✅ 已交付@v0.23.0 |
| FR-459 | 实例健康巡检与自愈（feat，依赖 FR-455/456/462）：现仅「进程退出后按退避自动重启」，无假死检测（探针挂但进程在不会重启）、无集群级巡检。新增存活/响应巡检 + 受控自愈策略（自动重启卡死实例、崩溃熔断）。**验收**：模拟假死实例被巡检识别并按策略自动处置；策略可配、动作写审计 | P1 | ✅ 已交付@v0.23.0 |
| FR-460 | 失效 bot 自动回收与补足（feat，增强 FR-398）：`workerEpoch` 不匹配的旧 bot（`desiredState=running`/`status=error`）占容量不产生连接，平台无回收能力（遗留 18 个僵死 bot）。新增自动回收 + 容量自动补足回目标数。**验收**：分片/CP 重启后僵死 bot 有限周期内被清、容量自动补回目标；回收动作写审计 | P1 | ✅ 已交付@v0.23.0 |
| FR-461 | 集群健康总览墙（feat，增强 FR-402）：平台总览现只给计数 + TopN≤5，60+ 台无法一屏看谁异常。新增**逐台健康矩阵/热力墙** + 排序 + 一键定位。**验收**：60+ 台一屏呈现逐台健康态并可下钻到单台 | P1 | ✅ 已交付@v0.23.0 |
| FR-462 | 异常自动检测（动态基线）（feat，增强 FR-011/085）：告警现仅静态阈值（且仅 node cpu/mem/disk）+ 节点离线。新增动态基线（EWMA/同环比）、突降突升、饱和度检测，并覆盖实例级指标。**验收**：注入缓慢劣化/突降，规则在无手工阈值下触发且噪声可控 | P1 | ✅ 已交付@v0.23.0 |
| FR-463 | SLO / 可用性统计（feat，增强 FR-011/220） | P2 | ✅ 已交付@v0.23.0 |
| FR-464 | 容量规划 / 趋势预测（feat，增强 FR-013/221）：无法回答"磁盘/内存按当前增速还剩多久"。新增时序外推与容量趋势告警。**验收**：给出关键资源的耗尽预测时间与置信区间 | P2 | ✅ 已交付@v0.23.0 |
| FR-465 | 性能归因（GC 采集）（feat，增强 FR-060）：原始指标齐（TPS/MSPT/heap/threads/世界）但无归因；探针已暴露 `serverprobe_gc_*` 却未采集。先补 GC 采集入库，再做归因（TPS 低是 GC/区块/实体）。**验收**：GC 次数/耗时入时序；给出 TPS 劣化的主要贡献因子 | P2 | ✅ 已交付@v0.23.0 |
| FR-466 | 服务器快照 / 一键回滚（feat，增强 FR-013/056）：仅有备份（需停服、链式回放）与文件/配置版本回滚，无整机快照 + 时间点回滚。新增快照与一键回滚闭环（回滚前自动建快照）。**验收**：可对实例打快照并从快照一键回滚，过程可追溯 | P2 | ✅ 已交付@v0.23.0 |
| FR-467 | 运行期配额强制（feat，增强 FR-078/079/317）：组配额仅在 Create 校验、运行期不强制；Docker 资源限额仅 docker 模式生效。新增运行期 CPU/内存/磁盘配额强制与超限处置。**验收**：超限实例被识别并按策略处置（告警/限流/停止），非 docker 模式亦生效 | P2 | ✅ 已交付@v0.23.0 |
| FR-468 | 二进制 / Beacon 版本管理与升级（feat，增强 FR-441/442，修订 ADR-090）：二进制/Beacon 重建只重取件、无版本比对/回滚/升级。新增二进制制品版本管理与受控升级（可回滚）。**验收**：可查看当前版本、升到指定版本、并回滚到上一版本 | P2 | ✅ 已交付@v0.23.0 |
| FR-469 | 跨实例排行 + 玩家在线趋势（feat，增强 FR-340/054）：跨实例对比仅限节点内前 12、单指标；无玩家在线趋势分析。新增跨节点/跨实例全局排行与在线趋势/时段分析。**验收**：可按 TPS/MSPT/CPU/内存/在线排序全量实例；给出在线趋势与时段分布 | P2 | ✅ 已交付@v0.23.0 |
| FR-470 | 崩溃诊断增强（feat，增强 FR-313/407）：崩溃仅有退出码+尾 200 行快照，无堆栈解析/同类聚合/OOM 关联。新增堆栈根因归类与崩溃趋势聚合。**验收**：连续崩溃被归类聚合、给出根因线索（OOM/异常类） | P2 | ✅ 已交付@v0.23.0 |
| FR-471 | 在管实例运行态对齐、启动冲突预检与外来运行时接管（feat，增强 FR-302/326/455/456） | P1 | ✅ 已交付@v0.23.0 |
| FR-472 | 僵尸压测会话收敛 | P1 | ✅ 已交付@v0.24.0 |
| FR-473 | Worker 日志平台 Shared Contracts 与 ADR（feat，开发前闸门） | P0 | ✅ 已交付@v0.24.0 |
| FR-474 | Worker 日志采集与持久化通道（feat） | P0 | ✅ 已交付@v0.24.0 |
| FR-475 | 日志 Multiline 与归一化（feat） | P0 | 🔨 开发中 |
| FR-476 | VictoriaLogs 资产分发与 Worker 运行时（feat） | P0 | ✅ 已交付@v0.24.0 |
| FR-477 | 分区 Catalog 与冷热 Lifecycle（feat） | P0 | ✅ 已交付@v0.24.0 |
| FR-478 | Deep Archive 与 Rehydrate（feat，**发布必需**）：Provider local/S3/MinIO；VL 分区按 namespace+UTC 日+generation；manifest 含 schema/parser/engine；受管 Raw Archive；Rehydrate 任务化（租约/合并/超时/预留/generation 隔离）；幂等重恢复 → `docs/specs/worker-log-deep-archive/spec.md` | P1·发布必需 | ✅ 已交付@v0.24.0·**真机 RustFS S3 验收通过**（登记/幂等/manifest/对象往返/Rehydrate 租约+复用+预留拒绝+完成/发布 generation 清理矩阵）；manifest 记录真实 VL build_id |
| FR-479 | Worker 日志查询服务（feat） | P0 | ✅ 已交付@v0.24.0 |
| FR-480 | CP 跨 Worker 日志查询协调器（feat）：**全部授权目标 + 覆盖汇总**；「仅在线」为显式选项；历史持有者；稳定复合排序 K-way merge；可合并聚合；Facet 截断语义；统一时间桶；禁止全量原始日志入 CP；导出复用有界 Search/同一快照，**缺口/截断/超预算不交付成功附件** → `docs/specs/cp-log-query-coordinator/spec.md` | P0 | ✅ 已交付@v0.24.0·logcoord+HTTP+生产 Assemble（inst/node 解析+真实 Readiness）；**真机联邦查询与延迟取证通过**（串行 p95 28.5ms、8 并发 p95 15.2ms） |
| FR-481 | 日志入库切换与 Legacy 只读（feat） | P0 | ✅ 已交付@v0.24.0 |
| FR-482 | 日志中心与仪表盘（feat，**发布必需**）：范围/级别/关键字/时间/列表/Tail/Hits/Stats/Facets；partial/离线/缺数据/截断/积压/Rehydrate 失败等非成功态；Legacy 标识；导出失败不交付成功附件；nextCursor=null≠历史完整；失败不得呈完整零结果 → `docs/specs/logs-center-tiered-ui/spec.md` | P1·发布必需 | ✅ 已交付@v0.24.0·**真浏览器验收通过**（真实联邦列表/Stats·Facets 一致、失败态引导；修了 Stats `agg.count` 读法缺陷）；失败态 DOM 矩阵 19 项 |
| FR-483 | 日志平台文档对账（docs）：Shared Contracts/ADR 迁入受控文档；资产清单与许可；恢复手册；数据损失边界；资源预算；合法配置组合；切换与故障处理；ARCHITECTURE/API/CHANGELOG | — | ✅ 已交付@v0.24.0·docs（已对账 ARCHITECTURE/API/CHANGELOG/PRD/规格状态；已补受控文档 `worker-victorialogs-runtime/asset-inventory.md`（资产清单/许可）+ `worker-log-platform-contract/recovery-manual.md`（恢复与故障处置）+ `worker-log-platform-contract/acceptance-record.md`（**持久真机验收记录**，缓解 `.tmp` 证据不入库）） |
| FR-484 | canonical 事件体落盘 compact（feat） | P1 | ✅ 已交付@v0.24.0 |
| FR-485 | 日志平台可观测性与告警（feat，增强 FR-474/484） | P1 | 🔨 部分交付@v0.24.0（**仅采集健康指标链路已落**：#49 落地 `log_ingest_healthy` 经 Worker 心跳上报 → CP 落库 → 内置告警触发联动；其余范围——state 文件尺寸与增速、persist 写字节/秒、单源 WAL 积压条数与暂停时长、投递失败率与分类、校验退避次数、慢 SQL 计数，以及**节点层 iowait 采集与比率触发**——**均未实现**） |
| FR-486 | Worker 启动时长与状态加载可观测性（feat，性能/可观测，增强 FR-474/484） | P1 | 🔨 开发中·**范围①②④ 已实现并真机验证，范围③ 部分完成**（①冷启动实测 **76 ms** 总耗时、逐阶段 ≤6ms，当年的「8m39s」由大索引导致、正常规模不复现；②`ReportStartupProgress` RPC + `node_startup_progresses` 表 + `/nodes/startup-progress`，注册前即可见、注册后清理；③`Load()` 规模曲线实测线性（100k 行 453ms），已优化扫描缓冲区复用（**内存 −32% / 分配次数 −21%**），**4× 耗时目标未达成**——本机 load 212 且无法 drop_caches，机械盘随机 IO 特性测不准，不盲改；④`state_warn_mb` 默认 512MB，实测中发现并修掉两处「告警永不触发」缺陷：只扫 `var/log` 漏了 `var/index`、告警被埋在「配了采集源」分支内。详见 `docs/specs/worker-startup-observability/spec.md` §7） |
| FR-489 | MCP 端点无状态化与 Token 维度运维视图（feat，修订 **FR-388/389/391**、作废 **FR-439**） | P1 | ✅ 已交付@v0.24.0 |
| FR-490 | 审批票据与状态机内核（feat） | P0 | ✅ 已交付@v0.24.0 |
| FR-491 | 审批执行器与执行回执（feat，依赖 FR-490） | P0 | ✅ 已交付@v0.24.0 |
| FR-492 | 高危 MCP 工具接入审批（feat，依赖 FR-490/491） | P0 | ✅ 已交付@v0.24.0 |
| FR-493 | 审批中心与权限节点（feat，依赖 FR-490）：新增审批中心页面——待审列表（操作 / 目标 / 申请人 / 风险 / 原因 / 证据）+ 批准执行 / 驳回 + 历史追溯；RBAC 权限树新增审批权限节点，**须遵循既有权限树约束**（FR-089「deny 永胜、超管唯一」与 FR-432 可配置权限树），无权限账号 403 | P1 | ✅ 已交付·审批中心页面（待审列表 + 批准执行 / 驳回 + 历史追溯）、`approval.read` / `approval.decide` 权限节点接线、7 项 DOM 用例与 3 档视口 e2e 均已完成；无权限账号 403 由路由守卫 + 服务层双重拦（决策端点另有「Agent 身份不可决策」的服务层拒绝，不靠路由兜底） |
| FR-494 | QQ 机器人告警通知通道（feat，增强 FR-085） | P1 | ✅ 已交付@v0.24.0 |
| FR-495 | QQ 机器人扫码接入与已发现群列表（feat，增强 FR-494） | P1 | 开发中（真机验收：扫码绑定 / 网关连接 / 入群事件自动落库**均通过**；建通道与前端待验） |
| FR-496 | 采集索引迁移至本地 SQLite | P0 | 🔨 开发中 |
| FR-497 | 重启增量对账与全自动收养 | P0 | 🔨 开发中 |
| FR-498 | 单机 60 实例规模压测与容量基线（feat，验证类） | P0 | 🔨 开发中 |
| FR-499 | 采集登记路径与长临界区解耦 | P1 | 🔨 开发中 |
| FR-500 | daemon 子树脱离 Worker 单元 cgroup | P0 | 🔨 开发中 |
| FR-501 | 日志成本治理（分层/采样/保留）与冷层搬运 | P1 | 🔨 开发中 |
| FR-502 | 前端设计底座单一真源与 `dark:` 变体修复（ref，免 spec） | P1 | 🔨 开发中·阶段 1 完成（设计底座单一真源已落：`packages/ui/src/styles/` 的 token/主题/动效入口齐备，主控台与控件博物馆均改为一次 `@import` 取底座；`dark:` 变体经 `@custom-variant` 绑定 `.dark` class。明细见 `docs/specs/unspecced-fr-records/spec.md`） |
| FR-503 | 主题配色对比度自动校验（test，免 spec） | P1 | 🔨 开发中·阶段 2 完成（对比度门禁已落：`packages/ui/src/lib/color-contrast.ts` + 110 项配对断言，5 套主题 × 明暗全覆盖。明细见免 spec 记录） |
| FR-504 | 内容页布局规范层（feat，免 spec） | P1 | 🔨 开发中·阶段 3 完成（布局规范层已落：`packages/ui/src/components/layout/` 提供 PageShell 三壳态 / PageHeader / ScopeBar / SummaryStrip / Toolbar / PlatformTabs 与网格原语；实测 views 下 46 个 `*PageView` 中 41 个已改用该层） |
| FR-505 | 导航外壳组件层（feat，免 spec，承 FR-504 边界） | P1 | 🔨 开发中·阶段 4 完成（导航原语层已落于 `packages/ui/src/components/nav/`；正文所述 `shell/` 路径与 AppShell/ResourceTree 已按「零引用删除」重构，按「已有 FR 只改状态字段」约定未改正文） |
| FR-506 | 工作区导航数据层（feat，免 spec） | P1 | 🔨 开发中·阶段 5 完成（工作区导航数据层已落于 `apps/control-plane-web/src/lib/console/workspace-navigation.ts`；四工作区 × 组 × 页面共 42 条登记 + 3 认证页；权限语义与旧六域逐条等价的 17 项用例全绿） |
| FR-507 | 主控台外壳接入与页面迁移（feat，依赖 FR-502~506，免 spec） | P1 | 🔨 开发中·阶段 6 完成（**四验收全部达成**：①桌面侧栏/移动导航/命令面板三处消费方统一以 `workspace-navigation.ts` 为真源；②内容页 41/46 已用 FR-504 原语，余 5 为认证页与全幅画布页；③真浏览器实测——日志中心 12004 条仅挂 24 行 DOM（0.2%）、滚动重挂载 326.8/286.6/691.0 ms、long task 0 次；④`nav-config.ts` 与死代码 `ConsoleSidebar` 已删，旧 41 条登记冻结成快照继续做等价性对照，`Workspace.tsx` 路由 47→54 条从未削减。全量前端回归 3284/3284 通过） |
| FR-508 | 代码内 FR 编号全量清理（ref，免 spec，承接 `.claude/rules/no-fr-in-code.md` 的存量豁免）：代码 / 注释 / 测试描述里的 `FR-\d+` 全部去除，编号只留文档 | P1 | ✅ 已交付（git 跟踪的代码文件 2,083 个、9,089 处全部清零；`check-no-fr-in-code.sh --stock` 0 命中，`go build`/`go vet`/`tsc -b` 全通过） |
| FR-509 | 侧栏资源面板收敛为「一个分组方式 + 服务器列表」（feat，增强 FR-165/FR-293，免 spec） | — | ✅ 已交付@v0.24.0 |
| FR-510 | 删除侧栏本地自定义分组并清掉其类型错误（ref，免 spec，与 FR-509 同批交付） | P1 | ✅ 已交付@v0.24.0 |
| FR-511 | 侧栏服务器列表精简与虚拟化（feat，增强 FR-165/FR-509，免 spec） | — | ✅ 已交付@v0.24.0 |
| FR-512 | 侧栏服务器行标注「服务端类别」三分法（feat，增强 FR-511，免 spec） | — | ✅ 已交付@v0.24.0 |
| FR-513 | 节点主机级观测采集补全（feat，为首页悬浮卡取证深度；详见 `docs/specs/inflight-backlog/spec.md`） | — | 🔨 开发中·**实现与真机取证均已完成**（Worker `hostobs` 采集 → 心跳 → CP 入库 → 首页悬浮卡四处新版式：负载三档 / CPU 构成含 iowait / 分挂载点磁盘 / 整机 Top 进程+进程数。§6 四个待定项已在实现中定案：不含 guest、进程计数读 `/proc` 走独立 goroutine、挂载点进快照表。**真机截图取证已完成**：`docs/screenshots/fr513-host-obs/` 5 张，实测渲染出负载 42.5/58.7/71.2%、iowait 19.3%（总占用仅 32% 的 IO 瓶颈）、`/var` 95% 而 `/` 14.4%、整机 Top 含 `rsync`/`mysqld` 等非受管进程。取证中一并补齐 `api/metrics.ts` 缺失的 6 个主机级字段契约与 devmock 缺失的 `/metrics/resource-attribution` 端点） |
| FR-514 | AI 供应商、模型与平台配置 | P0 | ✅ 已交付@v0.24.0 |
| FR-515 | Go 内置轻量 Agent 与私有会话 | P0 | ✅ 已交付@v0.24.0 |
| FR-516 | 当前用户受限系统工具与逐调用写入确认（feat，增强 Agent/MCP，需 spec，依赖 FR-515） | P0 | ✅ 已交付@v0.24.0 |
| FR-517 | 外部 Streamable HTTP MCP 客户端与连接治理（feat，需 spec，依赖 FR-514/515/516）：管理员配置远程连接、凭据和工具策略，支持发现分页、工具命名空间、JSON/请求级 SSE 响应、超时与结果限额；未知风险默认确认，不支持 stdio 或旧双端点 SSE；无法证明上游资源隔离的共享连接仅管理员可用。验收：真实远程工具闭环，同名/失效连接隔离，目的地址/重定向与凭据边界可证，普通用户不能借上游管理员权限越界。 | P1 | ✅ 已交付@v0.24.0 |
| FR-518 | 类型化页面上下文与实体选择（feat，需 spec，依赖 FR-515/516）：实例/节点/任务/日志/告警/监控页按白名单注册实体、页签、时间与筛选，生成可移除芯片和发送时快照；千级候选走服务端搜索。验收：移除后不外发该背景，跨页不改历史目标，伪造实体/敏感 query 被拒，禁止抓取整页 DOM，业务动态可带上下文交给助手分析。 | P1 | ✅ 已交付@v0.24.0 |
| FR-519 | 共享聊天界面与全局上下文面板（feat，需 spec，依赖 FR-515/516/518，联调 FR-517）：首页与顶栏呼出的非模态浮窗共用会话、草稿和运行，提供输入/模型选择、消息/工具结果/确认卡、历史、新会话、收缩/展开/停止，移动端全屏。验收：跨路由不丢会话或重复生成，关闭与停止语义分离，长列表有界、键盘可达、登出换用户不串号、流式更新不逐 token 重绘整棵外壳。 | P1 | ✅ 已交付@v0.24.0 |
| FR-520 | AI Dashboard 默认首页与经典首页切换 | P1 | ✅ 已交付@v0.24.0 |
| FR-521 | 退役 JM 业务能力 | P0 | ✅ 已交付@v0.24.0 |
| FR-522 | 业务事件纯透传 + SSE 事件流 | P0 | ✅ 已交付@v0.24.0 |
| FR-523 | 业务批量命令接口 | P0 | ✅ 已交付@v0.24.0 |
| FR-524 | 写穿与离线禁改 | P0 | ✅ 已交付@v0.24.0 |
| FR-525 | 对账协调接口 | P1 | ✅ 已交付@v0.24.0 |
| FR-526 | 桥能力接入 | P1 | ✅ 已交付@v0.24.0 |
| FR-527 | 对外只读统计契约 | P1 | ✅ 已交付@v0.24.0 |
| FR-528 | 对外 API 规范与错误码统一 | P1 | ✅ 已交付@v0.24.0 |
| FR-529 | 配置分层覆盖与有效值解析 | P0 | ✅ 已交付@v0.24.0 |
| FR-530 | 配置写入经 Worker 落盘 + 读回校验 | P0 | ✅ 已交付@v0.24.0 |
| FR-531 | 配置分批灰度与编排 | P0 | ✅ 已交付@v0.24.0 |
| FR-532 | 配置生效编排 | P0 | ✅ 已交付@v0.24.0 |
| FR-533 | Beacon 配置纳管 | P1 | ✅ 已交付@v0.24.0 |
| FR-534 | 业务插件面板 | P1 | ✅ 已交付@v0.24.0 |
| FR-535 | 批量目标解析与跨区二次确认 | P0 | ✅ 已交付@v0.24.0 |
| FR-536 | SSE 连接治理与慢消费者保护 | P0 | ✅ 已交付@v0.24.0 |
| FR-537 | 批量命令流式进度 | P1 | ✅ 已交付@v0.24.0 |
| FR-538 | 供应商协议三选与模型自动发现（feat，增强 FR-514，免 spec 但需契约补测） | P0 | ✅ 已交付@v0.24.0 |
| FR-539 | 供应商级出站代理（feat，增强 FR-514，默认直连） | P0 | ✅ 已交付@v0.24.0 |
| FR-540 | 助手默认启用平台自带 MCP 工具（feat，增强 FR-517，免 spec） | P0 | ✅ 已交付@v0.24.0 |
| FR-541 | 系统提示词、Skill 目录与思考强度（feat，需 spec） | P1 | ✅ 已交付@v0.24.0 |
| FR-542 | 对话历史独立页面与检索（feat，增强 FR-515/519，免 spec） | P1 | ✅ 已交付@v0.24.0 |
| FR-543 | 历史消息编辑与回退（feat，增强 FR-515，需 spec） | P1 | ✅ 已交付@v0.24.0 |
| FR-544 | 助手工具渐进式披露与执行路径统一 | P0 | ✅ 已交付@v0.24.0 |
| FR-545 | 业务事件位点账本与缺口查询（feat，见 ADR-107，依赖 FR-522） | P1 | ✅ 已交付@v0.24.0 |

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
- **500+ Bot 分布式命令压测**（FR-351/352/354/358~361）：离线测试环境中以多 Worker 分片向同一目标服发起 stable、step 和 spike 三类测试；正式通过至少要求 500+ Bot 连续 60 分钟，在线率、命令发送率、调度完成率、Worker 健康率及已配置屏障的到达率均 ≥99%，命令调度 lag p95≤1 秒，Worker/bot-worker 无非预期崩溃；前端必须完成命令模板配置、容量预检、实时观测、失败定位和报告闭环。`bot.chat` 未同步抛错只证明命令发送动作成功，不证明服务器接受、权限通过或产生业务效果；ServerProbe、TPS/MSPT、塔防或其他业务事件仅为可选附加观测，不得成为通用运行的启动、成功或交付前提。
- **Agent MCP 小游戏房间容量验收**（FR-395~399）：仅用 scoped Agent Token + MCP 强类型工具完成「实例准备/配置→Bot 模板与会话→容量预检→100/200/300/400/500 阶梯升压→登录静置→命令进房→移动攻击与周期命令→500 Bot 连续 60 分钟→停止与报告」全流程；默认 50 Bot/Worker，500 实测须至少 10 个具备容量的真实 Worker，不得靠单 bot-worker 放大到 500。容量报告必须分别给出目标服 JVM、各发压 Worker/bot-worker、节点总内存与集群总量的 baseline/峰值/p95/每 Bot 斜率，并把实测值与加 25% 安全余量建议明确分开；环境不足只能标已测规模与外推，禁止宣称 500 实测。
- **MCP 能力补齐与 Beacon 协同验收**（FR-440~444）：
  - FR-440：用 scoped V2 Token 经 MCP 调 `instance_update_tags` 能改实例标签；`agent_whoami` 返回的 capabilities 含 `instance.write`；缺省参数不改动、空数组清空、非空整体覆盖三种语义各验一次；无 `instance.write` 的 Token 调用被拒。
  - FR-441：三类来源（制品库 asset / 远程 URL / 节点本地文件）各搭建一次，均能产出 STOPPED 可启动实例；任务经 `RunAsync` 有阶段进度与终态站内信；下载失败进 `DAMAGED` 且 `provisionSpec` 可重建。
  - FR-442：一键搭出 Beacon 实例并可启动；制品来源优先级（制品库 > GitHub）真机验证；未部署时不影响其余功能。
  - FR-443：配置协同端点后，实例改名/改归属能在 Beacon 侧反映；**端点不可达 / 未配置时本机创建与启动操作全部正常**（兼容非依赖的硬要求）；失败有审计记录。
  - FR-444：能拉取 Beacon 区服结构并生成分组树与 `region:`/`zone:`/`role:` 标签；**Beacon 不可达时本地数据零改动**且返回明确错误。

- **MCP 端点无状态化验收**（FR-489）：①无 `Mcp-Session-Id` 时可连续调用 `initialize` / `tools/list` / `tools/call` 且响应不含该头；②携带陈旧或伪造的 session id 被忽略而非拒绝；③**重启 CP 后同一客户端无需重新 initialize 即可继续调用**（旧行为必失败）；④`GET /api/v1/mcp` 返回 405 且带 `Allow: POST`，`DELETE /api/v1/mcp` 同样返回 405 + `Allow: POST`；⑤SSE 兼容路径 `/sse` + `/message` 行为不变、连接断开即注销；⑥`GET /api/v1/agent/mcp/activity` 按 Token 正确聚合（多 Token / 窗口边界 / 失败计数 / 无数据）；⑦Token 吊销或 scope 下调在**下一个请求**即生效（不再有快照窗口）；⑧无凭据仍 401、策略拒绝仍 200 + `isError=true`。
- **MCP 生产安全门禁验收**（FR-490~493）：
  - FR-490：票据状态机穷举测试全绿；并发批准 / 撤回 / 过期交错不发生双批准；**Agent Token 调批准端点在任何路径下恒被拒**（服务层稳定拒绝并审计），仅人类 JWT 可批准；24 小时过期后不可执行；申请人可经 `own.list` / `own.get` 查询自己提交的申请与状态、并在 `pending` 时 `own.withdraw`，撤回后不可执行。
  - FR-491：目标漂移时执行器不产生副作用且记为 `failed`；重复批准 / 重试幂等；执行回执与领域写入、审计同事务；批准成功但执行失败时批准事实与失败原因均保留。
  - FR-492：**三类绕过口子各有专项断言**——`file_issue_transfer_ticket`（票据 + 流式上传）、`instance_send_command`、`instance_update_config` / `instance_binary_upgrade` / `instance_binary_rollback` / 远程 URL 二进制来源在未审批时**均零副作用**；action 目录全部 `write` / `destructive` 项均有明确的「进审批 / 直执」标注、无遗漏（覆盖测试 fail-closed，新增 action 未标注即失败）；批准后经执行器产生副作用且审计可查；驳回 / 撤回 / 过期均不执行；`start` / `restart` / 热重载仍按既有精确确认直执；CLI / MCP / curl 三入口同策略断言通过；**真机 MCP 实测**（未审批被拒 → 批准 → 执行 → 审计闭环）。**三入口同策略的可证落点**：`POST /agent/instances/:id/stop` 返回 202 + `requestId` 且**实例状态未被改动**（只断言状态码的话，「先停服再出票」的实现也能通过）；审批服务未装配时 HTTP 503 / MCP isError 双入口失败关闭。
  - FR-491（补充）：**目标漂移校验必须真的生效**——出票冻结快照、执行前比对；`version` 以字符串承载（纳秒超出 float64 精确整数上限，存数字会导致「目标没变」也判漂移、每张票永久失败）；无 evidence 的旧票据不阻断。
  - FR-493：真机浏览器完成「待审列表 → 批准执行 → 状态推进」闭环；无审批权限账号访问审批端点返回 403；空 / 常规 / 超大量 / 异常四态齐备。

- **QQ 机器人告警通道验收**（FR-494）：①通道类型 `qq` 可创建 / 编辑 / 启停 / 删除，被规则引用时删除返回 409；②明文 `appSecret` 被前端与后端同时拒绝，`${ENV_VAR}` 形式通过；③缺 `appId` / `appSecret` / `targetType` / `targetId`，或 `targetType` 非法时报错；④投递命中 `POST /v2/groups/{group_openid}/messages`（群）或 `/v2/users/{openid}/messages`（单聊），头部为 `Authorization: QQBot <token>`、body 为 `{"content":…,"msg_type":0}`；⑤同一 `appId` 在有效期内复用缓存 token、并发投递不重复取 token、临近过期触发刷新；⑥开放平台 HTTP 200 但业务 `code` 非 0 时判定投递失败，且失败不影响告警事件落库；⑦通道「测试发送」在真实机器人凭证下能在 QQ 群 / 单聊收到消息；⑧中英文案完整，界面不出现裸 key。
- **QQ 机器人扫码接入验收**（FR-495）：①调分享端点可拿到 `qun.qq.com/qunpro/robot/qunshare?...` 格式 URL，前端渲染为可扫二维码，刷新后重新生成；②CP 启动后网关连接状态为 connected（无 `1<<25` 权限时状态为 error 且错误信息明确指向 intent 权限）；③测试号把机器人拉进群后 1 分钟内已发现群列表出现该 `group_openid`，重复进群不产生重复条目；④通道表单走单聊（群聊已下线）：点「扫码绑定」出二维码，扫码完成后 `appId` / `appSecret`（引用名 `${QQ-<appId>}`）/ 目标 openid 自动填充且可直接保存；手工填写路径保留；⑤网关断开时 `qq` 通道测试发送仍成功；⑥三个新端点的响应均不含 `appSecret`，前端任何位置不出现密钥明文；⑦真机全链路：扫码 → 进群 → 后端出现该群 → 选群建通道 → 测试发送到达。

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
| 第四期 | 客户端 OTA 分发 | v0.8.0 / v0.11.0 / **v0.13.0** | ✅ 完成（端到端真客户端 OTA 更新流真机复验：updater-core 真下载+reconcile+sha256+增量/减量/防降级+鉴权 fail-static，真 UI 发布 v1/v2；FR-099 原生进度弹窗人眼目视 2026-07-14 用户确认通过，无剩余尾巴） |
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
