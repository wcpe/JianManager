# 功能规格：在途杂项 / 归真 / 延后（散单 FR）

> 状态：开发中 / 计划 / 已延后（逐 FR 标）　·　关联 PRD：FR-003, FR-041, FR-042, FR-046, FR-059, FR-098, FR-113, FR-114
>
> 本 spec 收容不归属某个大程序、但仍活跃（在途 / 计划 / 已延后 / 归真）的散单 FR 详情，PRD §4 仅留一行索引。

## 1. 归真（父 FR 标 done 实则缺，退「开发中」）

#### FR-003: 用户组与配额
- **优先级**: P0 | **状态**: 开发中（归真 2026-06-24：前端 GroupsPage 当前只读，「编辑/删除/配额/成员」UI 缺失、`useDeleteGroup` 未接入；由 FR-156 兑现） | **关联 API**: `POST /groups`, `POST /groups/:id/instances`, `GET /groups/:id/quota`
- **描述**: 用户组管理，实例分配给组，配额限制（最大实例数、Bot 数、存储空间）
- **验收**:
  - [x] 创建/编辑/删除用户组
  - [x] 组内添加/移除成员
  - [x] 实例分配给组（一个实例只属于一个组）
  - [x] 配额检查：创建实例时校验组配额

#### FR-059: 危险操作保护体系化
- **优先级**: P2 | **状态**: 开发中（归真 2026-06-24：单实例「强杀」零二次确认，与「删除/强制关服…均接入」验收不符；由 FR-138/139 兑现） | **依赖**: FR-030 | **关联 API**: 无（前端 + 既有 RBAC/审计）
- **描述**: 统一封装破坏性操作的保护：二次确认、输入名校验、角色门禁（FR-030 仅做了删除确认弹窗）
- **验收**:
  - [ ] 统一危险操作确认组件：删除/强制关服/下线节点/批量破坏性操作均接入
  - [ ] 高危操作（删实例/删节点/批量 kill）要求输入名称二次校验
  - [ ] 角色门禁：组成员对越权范围的危险操作被拒
  - [ ] 所有危险操作审计留痕（FR-015）
  - [ ] i18n + 主题正常

## 2. 计划 / 在途（todo / 开发中）

#### FR-046: Sponge 子服支持
- **优先级**: P2 | **状态**: 计划 | **依赖**: FR-033, FR-034 | **关联 ADR**: ADR-007, ADR-008 | **关联 API**: `POST /instances`（role=backend, type=sponge）, `GET /cores` | **Spec**: `docs/specs/provision-sponge/spec.md`（草拟，待审核）
- **描述**: 扩展建服向导，支持 SpongeVanilla / SpongeForge 后端子服，自动获取核心、系统分配目录与端口、结构化启动
- **验收**:
  - [ ] 建服向导核心类型新增 SpongeVanilla / SpongeForge，按 MC 版本从官方源获取核心 jar（优先制品库命中）
  - [ ] 系统分配工作目录与端口；写好对应代理转发配置
  - [ ] 绑定 JDK、结构化启动（沿用 FR-034，不手填命令）
  - [ ] 创建后可一键启动进入 RUNNING
  - [ ] i18n + 主题正常

#### FR-053: 插件批量部署多服
- **优先级**: P1 | **状态**: 计划 | **依赖**: FR-052, FR-058 | **关联 API**: `POST /plugins/batch-deploy` | **Spec**: `docs/specs/plugin-batch-deploy/spec.md`（草拟，待审核）
- **描述**: 从制品库选插件，批量部署到选定的多个实例，返回成功/失败汇总
- **验收**:
  - [ ] 从制品库（type=plugin）选一个或多个插件
  - [ ] 选目标实例集（按筛选或勾选），批量部署
  - [ ] 经 gRPC 扇出到各 Worker，返回每实例成功/失败 + 汇总
  - [ ] 权限隔离：仅部署到有权实例
  - [ ] 危险操作二次确认 + 审计
  - [ ] i18n + 主题正常

#### FR-113: 全文索引后台化与进度
- **优先级**: P2 | **状态**: 开发中（本 worktree 已补自动化回归；待主控真机验收） | **关联 FR**: FR-074 | **关联 ADR**: ADR-017, ADR-024
- **描述**: 全文搜索查询时同步增量重建索引，大工作目录首次查询阻塞 UI
- **验收**:
  - [x] 首建移出查询关键路径（后台异步），查询不同步全量重建；小目录有界快路径仍同步出结果
  - [x] 查询时索引未就绪返回 `indexing=true`，前端给「索引中」进度并自动重试
  - [ ] 真机：大目录首查不卡 UI、结果一致

#### FR-114: 探针依赖内联/缓存预置
- **优先级**: P3 | **状态**: 计划 | **关联 FR**: FR-065, ADR-016 | **Spec**: `docs/specs/probe-dependency-cache/spec.md`（草拟，待审核）
- **描述**: 探针首启联网拉 TabooLib 依赖（~30s+），慢网/离线首启探针失败
- **验收**:
  - [x] Worker 侧 TabooLib/Kotlin 缓存预置：`libraries_zip` 只接受 `libraries/` 根路径并解压到实例工作目录根
  - [x] 构建链路：父仓 Go 工具从探针 jar 内 `env.properties`/`version.properties` 解析依赖，按 Maven layout 下载、写本地 sha1、生成稳定 zip 与 probe.json
  - [x] CP 下发：建服自动部署与在线更新 helper 均携带 `LibrariesZip`，缺缓存时告警但保持旧行为
  - [x] 安全防护：路径穿越、绝对路径、反斜杠路径、超大条目/总量/条目数拒绝
  - [x] 前端探针更新卡展示离线依赖缓存大小与短指纹，可在真实浏览器监控分区验收
  - [ ] 真机：断网首启探针正常 enable，`libraries/` 不再触发首启联网下载

> **工程整治**（chore/ref，不占 FR 号，走 sdd-refactor-code/手工）：① 前端路由级代码分割（#13，PluginManager 798KB/index 411KB，路由 `lazy()` 拆分）；② `.gitattributes` 规范换行与生成代码合并（#17/#18，`*.pb.go merge=union linguist-generated` + `eol=lf`）；③ 内嵌 CFR + 镜像探针/CFR 嵌入校验（#14/#20，发版 `make embed-cfr` + 镜像构建显式校验）。

## 3. 已交付 / 已延后（delivered / deferred）

#### FR-041: Bot 实时遥测与单 Bot 详情面板
- **优先级**: P2 | **状态**: 已交付@v0.13.0 | **依赖**: FR-039（控制台 Bot 段）, FR-009（Bot 平台） | **关联 API**: SSE `/bots/:id/events`, gRPC StreamBotEvents / SendBotCommand
- **描述**: 将 Bot 实时事件（StreamBotEvents：血量/饥饿/位置/聊天）从 Worker 经 Control Plane 推送到浏览器，提供单 bot 详情面板
- **验收**:
  - [x] Control Plane 将 Worker 的 StreamBotEvents 经 SSE 代理推送到浏览器（参照实例事件 SSE）
  - [x] 点击单个 bot 打开详情面板，实时显示血量/饥饿/位置/行为
  - [x] 显示 bot 聊天/事件日志滚动流
  - [x] 面板可向 bot 发送指令（SendBotCommand）
  - [x] 仅订阅当前查看的 bot，避免上万 bot 全量推送

#### FR-042: Bot 压测会话编排 UI
- **优先级**: P2 | **状态**: 已交付@v0.13.0 | **依赖**: FR-038（Bot 规模化 API）, FR-040（全局 Bot 页）, FR-009（Bot 平台） | **关联 API**: `/bots/stress-sessions` + FR-038 摘要/批量
- **描述**: 提供持久化压测会话后端 API 与前端编排：创建压测会话（目标实例+数量）、批量上线/下线、按会话聚合监控
- **验收**:
  - [x] 从全局 Bot 页「压测」入口创建会话：选目标实例 + bot 数量 + 初始行为
  - [x] 启动会话后 bot 批量上线，页面按会话聚合显示上线进度与状态分布
  - [x] 可结束会话批量下线
  - [x] 压测会话作为一个聚合单元展示，不逐个铺开上万 bot

#### FR-098: 块级二进制 diff 增量发布
- **优先级**: P2 | **状态**: 已交付@v0.13.0 | **依赖**: FR-087、FR-257、ADR-054 | **关联 ADR**: ADR-021、ADR-054
- **描述**: 对变化的大文件只传内部差异（zstd patch-from）——发布侧相对上一版同路径文件算 patch 入库，manifest 在完整 artifact 旁提供可选 patch 引用，客户端有匹配旧文件时应用 patch，失败或不匹配回退完整 artifact。在文件级增量之上进一步最小化传输。**不复活已废弃的 FR-097 `.jmpack` 容器**，patch 仍走现有单文件 `client-file` 制品与 `/client-artifacts/:sha256`。
- **验收**:
  - [x] 发布侧：相对上版算文件块级 patch（zstd patch-from），patch 内容寻址入库
  - [x] manifest 文件项带 patch 引用（oldhash→newhash）
  - [x] 客户端：有旧文件 + 有 patch 则下 patch 应用，否则下全量；应用后 sha256 校验
