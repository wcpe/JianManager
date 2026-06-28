# 功能规格：出站代理可视化配置（设置面板全局 + 节点级覆盖下发）

> 状态：待审　·　关联 PRD：FR-185（增强 FR-174）　·　关联 ADR：ADR-043（本 FR 创建，扩充 ADR-037）　·　分支：feature/fr-185-proxy-visual-config

## 1. 背景与目标

FR-174（出站网络代理，ADR-037）已交付：CP 与每个 Worker 各从自身 yaml `proxy:` 段读 `httpclient.Config{URL,NoProxy}`，经共享工厂 `internal/platform/httpclient` 构造出站 client。但 FR-174 **明确把以下留作后续**（其 spec §2 范围外）：①面板 UI 配代理 ②CP 下发代理给 Worker ③改 proto/gRPC。

本 FR 正是接手这三件：让平台管理员在**设置面板**配全局/CP 代理、在**节点页**为单节点配代理覆盖，经 gRPC 下发 Worker **运行时生效**，免登机器改 yaml/重启。P1。

**目标**：把出站代理从「各进程改配置文件」升级为「面板一处配齐 + 节点级覆盖 + 运行时热生效」，不破坏 FR-174 的 yaml/env 既有路径。

## 2. 需求（要什么）

### 范围内
- **全局出站代理（设置面板）**：设置页新增「网络」分类（仅平台管理员），配全局代理 `url` + `no_proxy`。保存写入 CP 的 **settings DB 覆盖层**（复用 FR-037/063 的 `useSettings`/`useUpdateSettings` 机制），CP 自身出站 client **运行时重建、免重启**。此全局值同时作为**各节点的默认代理**。
- **节点级覆盖（节点页）**：节点详情页可为单节点配「继承全局 / 自定义」；自定义时填 `url` + `no_proxy`。存 CP DB（节点维度），经 gRPC 下发该 Worker，Worker **运行时重建出站 client、免改 yaml/重启**。
- **优先级**（明确并写进 ADR-043）：
  - CP 自身出站：`settings DB 覆盖（全局）` > `control-plane.yaml proxy` > 环境变量。
  - Worker 出站：`节点自定义（DB 下发）` > `全局默认（DB 下发）` > `worker.yaml proxy` > 环境变量。
- **凭据脱敏**：含 `user:pass` 的代理 URL 在 UI 回显、API 响应、日志中一律脱敏（复用 `httpclient.Sanitize`），不回显明文密码。
- **校验**：保存时复用 `httpclient` 的 URL/scheme 校验，非法（不支持 scheme / 不可解析）即报错，不静默直连。
- **下发可靠性**：节点重连时 CP 依据 DB 重新下发（DB = 真相源，Worker 不落盘为准）。

### 不做（范围外）
- per-下载源不同代理（仍一进程一套出站）。
- 入站/反向代理；MC 代理（BungeeCord/Velocity，FR-035，勿动）。
- 多级代理链 / PAC 脚本。
- 删除 yaml `proxy:` 段（保留为无 DB 覆盖时的回退，向后兼容）。

## 3. 设计（怎么做）

### 3.1 ADR-043（本 FR 创建，扩充 ADR-037）
「**节点级出站代理由 CP 统一管控 + 经 gRPC 下发，Worker 运行时应用**」决策：
- 为何下发（运营在面板一处配齐，免逐台登机器改 yaml）；
- 真相源 = CP DB，递送 = 心跳响应携带（见 3.3），Worker 运行时重建 client、**不落盘**（重连由 CP 依 DB 重发）——符合「DB 仅 CP 读写」「CP→gRPC→Worker」依赖方向，不破架构不变量；
- 优先级链（见 §2）；
- 与 ADR-037 关系：ADR-037 把「UI/下发」列为范围外，本 ADR 取代该立场（扩充而非推翻工厂设计）。
- 决策正文写 ADR，勿在 spec 重复。**ADR 文件名/编号用 `ADR-043`（主控预留号，写死，勿自行 max+1）。**

### 3.2 CP 侧：全局代理（settings 覆盖层 + 运行时重建）
- settings 新增可编辑键 `proxy.url`、`proxy.no_proxy`，归类「network」（前端 `keyCategory` + 后端 settings 白名单）。仅平台管理员可读写；`proxy.url` 标记 sensitive（脱敏展示）。
- CP 出站 client 从「启动时 `httpclient.New` 一次」改为**可运行时重建的持有者**：引入 atomic 持有者（如 `httpclient.Provider`，内含 `atomic.Pointer[http.Client]`），下游（SelfUpdateService/FetchFeed/jar 等）改为「每次取当前 client」。settings 保存 `proxy.*` 后重建并替换持有者。
- 生效优先级：DB 覆盖（settings）非空则用之，否则回退 `control-plane.yaml` 的 `proxy`，再回退 env（即 `httpclient.New` 空配语义）。

### 3.3 Worker 侧：节点代理下发 + 运行时重建
- **节点 DB 模型**：nodes 表加 `proxy_mode`（`inherit`|`custom`）、`proxy_url`、`proxy_no_proxy`（custom 时有效）。迁移加性、默认 `inherit`。
- **下发通道**（落地择一）：CP 按「节点 custom ? 节点值 : 全局默认」算出期望代理下发。
  - 选项 A（推荐）心跳携带：`HeartbeatResponse` **当前仅 `timestamp`、Worker 尚未处理其内容**——加 `proxy_url`/`proxy_no_proxy` + `proxy_generation`(hash)，Worker **新增对心跳响应的处理**，generation 变化才重建（避免每拍重建）。重连天然重发、零新 RPC。
  - 选项 B：`RegisterResponse` 携带初始代理（注册/重连即应用）+ 新增 `SetProxy` 单 RPC 推运行时变更（更贴近现「CP 发起 unary」范式）。
  - **硬性要求（不论选哪个）**：① 节点重连/重启后 CP 依 DB 重新下发并生效；② 用 generation/hash 比较，仅变化时重建 client。
- **Worker 应用**：Worker 同样把出站 client 改为 atomic 持有者；收到心跳 proxy generation 变化时，`httpclient.New` 重建并替换，注入到 selfupdate/JDK/CFR/jar 下载点（这些点已在 FR-174 收口为工厂 client，本 FR 改为从持有者取）。
- **优先级**：下发值非空用之；为空（极早期未下发）回退 `worker.yaml proxy`，再回退 env。
- proto 改动后 `make proto`/`buf generate` 重生成；ARCHITECTURE 通信章节同步。

### 3.4 前端
- **设置页**：`SettingsPage.tsx` `SettingCategory` 加 `network`；`keyCategory` 把 `proxy.*` 归 network；图标（如 `Network`/`Globe`）。`proxy.url`/`proxy.no_proxy` 走可编辑行（文本框 + 校验提示）。**新增交互如需弹窗一律遵循 `.claude/rules/ui-modals.md`**（本页主要是行内编辑，无新增模态）。
- **节点页**：节点详情加「出站代理」面板/段：单选 `继承全局` / `自定义`；自定义展开 URL + no_proxy 输入 + 保存。展示当前生效来源（继承/自定义）。API：`PATCH /nodes/:id/proxy`（或并入既有节点更新端点）。
- i18n zh/en（只追加自己的键块）；暗亮主题用 token。

### 3.5 错误与可观测
- 代理握手失败的下载 error 透出脱敏代理地址；不静默回退直连。
- 节点离线时面板标注「待下发」（下次心跳生效）。

## 4. 任务拆分
- [ ] 写 `docs/adr/043-node-proxy-push.md`（ADR-043，预留号写死）
- [ ] `httpclient` 加运行时持有者（atomic client provider）+ 单测
- [ ] CP：settings 加 `proxy.url`/`proxy.no_proxy`（network 类、sensitive）+ 保存后重建 CP 出站持有者；优先级 DB>yaml>env
- [ ] proto：`HeartbeatResponse` 加 proxy 字段 + generation；`make proto` 重生成
- [ ] CP：节点 DB 模型加 `proxy_mode`/`proxy_url`/`proxy_no_proxy` + 迁移 + 心跳按节点算期望代理下发 + `PATCH /nodes/:id/proxy`（平台管理员 + 审计）
- [ ] Worker：出站 client 改持有者 + 心跳 proxy generation 变化时重建并注入各下载点
- [ ] 前端：设置页 network 分类 + 节点页出站代理面板（继承/自定义）+ i18n + 脱敏展示
- [ ] doc-sync：PRD FR-185「计划」→「开发中」（只改本行）；ARCHITECTURE 通信/ER/配置章节；API.md（节点代理端点 + settings network 键）；ADR-043；CHANGELOG `[Unreleased]` 末尾追加一行
- [ ] 中文 commit（按 proto/control-plane/worker/web 拆 commit，git-commit 规范）

## 5. 验收标准
- 单测：`httpclient` 持有者重建/并发取用安全；优先级判定（DB>yaml>env、节点 custom>全局>yaml>env）；脱敏；非法 URL 报错。
- CP/Worker 编译 + 既有相关测试仍绿；proto 重生成无残留。
- 设置页「网络」分类可配全局代理；保存即生效（CP 出站立即走新代理，无需重启）。
- 节点页可配「继承/自定义」覆盖；保存经心跳下发，Worker 运行时重建出站 client。
- **【需真机，用户确认】** 在需代理环境：①面板配全局 `http://127.0.0.1:7890` 后 CP「检查更新」经代理拉到 GitHub；②给某节点配 `socks5://...` 自定义后，该节点 JDK/jar 下载经该代理；③节点改回「继承全局」后恢复用全局；④重启该 Worker 后 CP 依 DB 重新下发、代理仍生效。HTTP/SOCKS5/继承各至少一条。

## 6. 风险 / 待定
- **CP 出站持有者改造面**：FR-174 已把出站点收口为注入 client；本 FR 改为「从持有者取」，需 grep 确认所有注入点都改为运行时取用（否则保存后旧 client 仍在用）。
- **心跳 vs 新 RPC**：择一落地；心跳携带需注意 generation 比较避免每次重建。
- **与 FR-186 同碰 CP self-update 区**：FR-186 改 self-update 缓存、FR-185 改 self-update 取 client 的方式——同文件不同关注点，rebase 可能小冲突（FR-185 改 client 注入、FR-186 改缓存持久化），各自加性、易解。
- **真机需代理环境**：agent 无代理时单测 + httptest 覆盖逻辑，真机项标「待真机验」由用户确认。
