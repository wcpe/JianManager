# FR-178 spec：节点 JDK 面板 + 节点制品缓存

> 状态：待审　·　关联 PRD：FR-178（增强 FR-033/045，关联 FR-082/183）　·　分支：feature/fr-178-node-panels　·　依赖：FR-183（JDK 异步安装+任务中心，已落）、FR-176（卡片基线，已落）

## 1. 背景与目标

两个痛点：
1. **JDK 管理简陋**：`NodesPage` 手搓 `fixed inset-0` 模态套 `NodeJDKPanel` 原生 `<table>`（横向溢出、`confirm()` 删除、「登记已有」用切换隐显内联表单重组布局）；一键下载只支持 Temurin/Corretto/Zulu + 只能装"大版本最新 GA"。
2. **建实例重复下载慢**：Worker `DownloadCore`（`internal/worker/grpc/provision_ops.go`）**每次都把核心 jar 重新下载到该实例工作目录、不查本地有没有**。删实例再建 = 重下，大 jar 等很久。

**目标**：节点页有两块**名副其实的节点级**面板——(a) 像样的 JDK 管理（抽屉、多厂商/版本、目录选择器）；(b) **节点制品缓存**（按 sha256 的本地缓存，秒建免重下 + 可视化管理）。全局制品库管理仍归控制面板（FR-082），节点页不重复。

## 2. 关键判定（已与用户确认）

- **制品（jar/插件）是平台全局、内容寻址、存控制面板**（`/assets` → CP `var/artifacts`，ADR-011）。**全局库的传/删管理留 FR-082，节点页不做**。
- **节点页的"制品" = 节点本地缓存**（性能优化，真·节点级）：Worker 按 sha256 缓存下载过的核心/插件，建实例命中即秒拷、免重下；节点页可视化看/清这份缓存。
- **JDK 真·节点级**（`NodeJDK` 按 `node_id`）：节点内做完整 JDK 管理。

## 3. 范围

### 范围内
- **节点制品缓存（后端）**：Worker 内容寻址缓存层 + `DownloadCore` 命中复用 + 手动清 + 可选容量上限 LRU 自动淘汰。
- **节点制品缓存面板（前端）**：列缓存项（名/版本/大小/最近用）+ 总占用 + 上限设置 + 手动清/逐项清。
- **JDK 面板重做**：抽屉式、表格不溢出、foojay 多厂商/具体版本、目录选择器、`DangerConfirm` 删除；安装下发接 FR-183 任务中心（已就位）。
- **抽屉容器**取代手搓模态 + **抽屉 UX 约束**（§5）。

### 非目标
- 不做全局制品库管理（FR-082）；不改 JDK 异步安装编排（FR-183 已做）；不做节点页范式重做（FR-177）。

## 4. 设计

### 4.1 节点制品缓存层（Worker，`internal/worker`）
- **缓存范围（首版写死）**：只覆盖 `DownloadCore` 的**服务端核心 jar**（建实例下载，正是重下痛点）。**不缓存**插件/其它下载路径（范围蔓延，后续 FR 另议）。
- **缓存目录**：数据根下持久内容寻址缓存 `<root>/var/artifact-cache/<sha256[:2]>/<sha256>`（区别于 `cache/` 临时中转）。每项带 sidecar `<sha256>.meta`（name/type/version/sourceUrl/size/cachedAt）+ `lastUsedAt`（命中时 touch）。
- **`DownloadCore` 改造**（`provision_ops.go`）：入参已有 `Sha256`。
  1. `Sha256` 非空且缓存命中 → **直接从缓存拷贝到实例工作目录**（校验 size，免网络），touch lastUsed。
  2. 未命中 → 走现有 `downloadFile` 下载（边下边算 sha256、校验），**落地后存入缓存** + 写 meta，再拷进工作目录。
  3. `Sha256` 为空（少数源无校验）→ 不缓存、按现状下载（缓存键必须是 sha256，无键不缓存）。
- **LRU 容量上限**：节点可配 `artifact_cache.max_bytes`（worker.yaml + 可经 CP 设置下发，默认 0=不限）。存入新项后若超上限 → 按 `lastUsedAt` 升序淘汰直到回落（不淘汰正被引用项——可选，首版可简单按 LRU，重下成本可接受）。
- 并发安全：缓存写用临时文件 + 原子 rename；同 sha256 并发下载用单飞或容忍重复写（最后 rename 胜）。

### 4.2 缓存管理 API（Worker gRPC + CP，加性）
- proto 新增（或复用）Worker RPC：`ListArtifactCache`（返回 [{sha256,name,type,version,size,lastUsedAt}]，meta 缺失时只回 sha256/size）、`EvictArtifactCache(sha256)`、`ClearArtifactCache`、`SetArtifactCacheCap(maxBytes)` / 读当前 cap + 总占用。
- CP 端点（仅平台管理员 + 审计）：`GET /nodes/:id/artifact-cache`（列表 + 总占用 + cap；CP 可用自身 asset 表按 sha256 补全 name/version）、`DELETE /nodes/:id/artifact-cache/:sha256`、`POST /nodes/:id/artifact-cache/clear`、`PUT /nodes/:id/artifact-cache/cap`。

### 4.3 JDK foojay 多厂商/版本（Worker `internal/worker/jdk`）
- `buildDownloadURL` 现硬编码 3 家。新增经 **foojay disco API**（`https://api.foojay.io/disco/v3.0/packages?distribution=&version=&architecture=&operating_system=&archive_type=&latest=`）取 `links.pkg_download_redirect`，经 FR-174 出站代理 client。厂商扩到 foojay 支持集（+Liberica/Microsoft/Semeru/GraalVM…），保留 3 家直链回退。
- CP `GET /nodes/:id/jdk/catalog?vendor=&major=`（CP 代理查 foojay，统一出站代理、避前端跨域）返回可选发行版/版本喂前端选择器。

### 4.4 目录选择器（JDK 路径登记）
- 新增节点级只读目录浏览 `GET /nodes/:id/browse?path=`（经 gRPC 委托 Worker `file_ops.ListDir`，仅平台管理员、防穿越）。前端目录选择器逐级浏览选目录。

### 4.5 面板组件 + 抽屉容器（与 FR-177 的协作）
- **JDK 面板、制品缓存面板做成可复用组件**（独立组件，不绑死容器）。
- FR-178 先落地、那时节点页仍是旧版：用统一右侧 `Sheet`/Drawer 承载（**取代** `NodesPage:506-531` 手搓 `fixed inset-0` 模态），宽、可滚动、主题化滚动条（FR-176）。
- **FR-177 主从双栏落地后**：同一批面板组件**改挂右栏分段**（FR-177 §3.3），**不重写**——抽屉是 FR-178 的过渡承载，分段是终态。FR-177 实现时把抽屉入口下线、组件复用进右栏。

## 5. 抽屉 UX 约束（立规，FR-177 沿用）
- **禁止**"点按钮切换隐显内联表单致面板其余内容上下重排（卡片重组摧毁布局）"。表单/操作常驻稳定区或进独立子视图/分段，布局不跳。
- 滚动用主题化滚动条；明暗/双主题/i18n 全程随变量。

## 6. 任务拆分
- [ ] Worker：制品缓存层（缓存目录 + `DownloadCore` 命中复用 + meta/lastUsed + LRU + 并发安全）+ 单测
- [ ] Worker：foojay disco 查询（厂商/版本扩展 + 回退）+ 单测
- [ ] proto：缓存管理 RPC（List/Evict/Clear/Cap）+ 重生成
- [ ] CP：`/nodes/:id/artifact-cache`（CRUD + sha256→name 补全）、`/nodes/:id/jdk/catalog`、`/nodes/:id/browse` + 路由/RBAC/审计
- [ ] 前端：抽屉容器 + JDK 面板重做（不溢出/版本选择器/目录选择器/DangerConfirm）+ 节点制品缓存面板（列/清/上限）+ api 层
- [ ] doc-sync：API.md、ARCHITECTURE（节点缓存层/foojay）、CHANGELOG、PRD FR-178 行
- [ ] 测试：缓存命中/未命中/LRU 淘汰、foojay 解析、缓存/catalog/browse 端点、前端面板纯逻辑

## 7. 验收
- [ ] **缓存命中免重下**：建实例下载某 jar → 删实例 → 重建 = 缓存命中、秒建不走网络（真机验）
- [ ] 缓存面板：看缓存项/总占用、手动清/逐项清；设上限后超限自动按 LRU 淘汰
- [ ] JDK 抽屉：表格不溢出、路径可复制、删走 DangerConfirm
- [ ] 一键下载 ≥5 厂商 + 具体版本选择；下发后任务中心见进度（FR-183）
- [ ] 登记已有：目录选择器选路径（非手敲）；表单稳定布局不重组
- [ ] 抽屉 UX 约束全满足；i18n 中/英 + 明暗 + 双主题
- [ ] **真机闸（用户验）**：缓存秒建、foojay 装 JDK、目录浏览、缓存清理真实生效

## 8. 风险 / 待定
- LRU 是否避让"正被实例引用"的缓存项：首版可不避让（重下成本可接受），如要避让需 CP 传引用集——倾向首版简单 LRU。
- 节点级目录浏览/缓存管理是新 API 面：守架构不变量（CP 经 gRPC 委托 Worker，不直接读节点 FS）。
- foojay 查询走 CP 代理（统一出站代理 FR-174）。
