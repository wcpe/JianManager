# 功能规格：日志中心与可观测仪表盘（FR-482）

> 状态：已交付@v0.24.0（本轮确认、三范围导出、失败态与取消修复及已覆盖回归完成；r028 原 scope 真实 live 的基础 15 项及第 16 项确认 409 已过，既有浏览器验收按原输入保留）
> 本次提交/功能修复验收不外推全部 UI 分支、迁移成功、性能或长稳；已有结果及延期专项统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)。　·　关联 PRD：FR-482　·　依赖：FR-480/481

## 2026-10-22 面板修复契约与验收

本节保留目标行为；本轮已覆盖前端回归及 r028 原 scope 真后端 live 按顶部声明收口，下文 v8/r4 候选进度保留原输入/时点，不充当前结论；不能把 devmock 或指定 live 外推全部整链。HTTP 导出协议继承 [CP 协调器规格](../cp-log-query-coordinator/spec.md)。

- **迁移确认**：首击仅打开既有 `DangerConfirm`，固定节点、入口 namespace 与 UTC 日，并明确实际影响是该节点整物理 UTC 日、可能多个 namespace，不能承诺只迁入口 namespace。确认后才 POST，取消零 POST；失败/超时不显示成功，也不自动重放人工动作。迁移入口按已落地的 HTTP 平台管理员主体门展示或禁用并说明原因，不凭入口目标推断全日已授权；同页普通运行时控制仍用原节点权限，不连带收紧。源码/DOM 已对齐；真后端浏览器已验证确认单 POST 与真实 409 拒绝，无假成功，不能称成功搬迁。
- **同一 scope 真源**：Search/Stats/Facets/Export 共用规范化条件，包含 `onlineOnly` 与实际绝对移窗时间；同 scope 可复用同 view，条件变更必须重开匹配 view。占位旧数据、pending 或旧 scope 响应不能用于新条件下载。
- **三种导出范围**：当前页＝点击时同 view、入页 cursor 与 limit 对应的完整逻辑页；当前有效窗全部匹配＝该窗口全部匹配，而非省略时间条件；显式自选时间段＝先在 Dialog 填写范围，变化后创建新 view。范围碰巧相同时允许文件内容相同，不能用“必须不同”替代集合校验。
- 当前页走服务端同 view 新鲜 Search/Stats 与权限、coverage、quality 复核，不把界面原始行回传服务端，也不能借本地 NDJSON 生成绕过 pending/partial 门禁。经典路径同样要真正只导出当前页并复核权限；失败或取消不得下载成功附件。
- **状态分别展示**：Stats 与 Facets 各自呈现 partial、quality、truncated 与错误；失败不能显示成精确 0。停机历史区分 403、500、超时和成功空列表，失败提供重试。
- **有界请求生命周期**：AbortSignal 贯穿 axios、短退避与重试；跟随在上一请求结束后调度下一轮，不重叠堆积。搜索使用小幅 debounce，显式更新 anchor，保留位置凭据与回溯能力。

旧 v8 前端候选已完成：15 文件 154 条 DOM、3 条 devmock 合同、tsc、前端/依赖 mock 范围 lint（零 warnings）、离线 build 与本地 Chromium 5 条全部通过；同内容源码指纹与独立复核一致。两处 P2 的有效平台身份和真实请求取消，以及联邦 `[from,to)` 半开窗均已红转绿；经典 `/logs` 保留既有闭区间。浏览器附件逐行/顺序/内容与 SHA 核对为第一页 100、第二页 61、全部匹配 161、移窗 2、自选半开段 1 条。

上述 v3 前端回归是 Chromium+devmock，不能冒作真后端。后续同一前端内容与 Go 同源正式静态资产的 bare-local 真 Chromium/CP/Worker/VL 指定短场景也已通过：64k 底库另有完整 API 对账，UI 验证非空 1s、1800 条合法 LogsQL scope；两页各 100、全部/自选段各 1800 同 SHA，半开边界、原生 abort、离线导出无附件，以及迁移确认 1 POST 收真实 409 均有实证。142 资产逐 SHA 与 Go/UI 内容已核对，详见 [有限联合短验](../worker-log-platform-contract/spec.md#2026-10-22-有限真实联合短验与剩余门禁)。这不是生产、全底库 UI、迁移成功或 4C/8GiB 性能验收，FR 不因此改为本批已交付。

当前 durable outbox r4 是本地候选已实现、联合验收中。上述前端与真后端 live 仅对旧 v8 的 Go/UI 输入成立；后台实现变化后，UI 文件未变也不能复用旧 live 核销新 hash。r4 须重绑真实分页/下载/取消/失败及安全验证；新 ordinary/race、全仓/raw 完整 Worker 1s、Search、双短/live 与 9 窗仍待实测，不更新 FR 交付状态，不将旧 1800 行 scope 或 FAST 短验当新性能通过。实现协议及六字段公共测试 fixture 修正限域见 [共享契约](../worker-log-platform-contract/spec.md#durable-outbox-r4本地候选已实现联合验收中)。

### 当前实现参数（2026-10-22，前端及真后端短场景通过、完整末门待验）

- 当前页联邦导出携 `range=page`、规范 scope、已有 viewId、入页 cursor 和页面 limit 100；冻结完整原始 Search 逻辑页，对服务端附件逐字段、ID 与顺序校验，不依赖虚拟列表挂载范围，也不凭本地 Stats/pending 绕过服务器闸门。
- 全部匹配携 `range=all`、当前实际移窗 from/to 和同 viewId，不携 cursor；请求 limit 1000000 仅表达期望，服务端仍按 10000 行/64MiB 默认配置等上限收紧。UTC 时间 Dialog 改 scope 时不带旧 view；范围相同允许集合相同。
- 经典当前页新鲜 GET 同 page 条件并比对完整内容，403 或集合变化不下载。迁移 `retry:false`，响应未知保留未知状态，不自动重复人工动作。迁移使用 `/auth/me` 已加载的有效平台主体（含模板绑定），明确否定优先于旧 JWT 数字角色；未加载时仅沿既有兼容回退，服务端授权始终为准。非管理员显示禁用原因，普通运行时控制不收紧；整日跨 namespace 的 DangerConfirm 文案与 DOM/真后端浏览器的取消、单 POST 和失败不重放已核对；真实 409 拒绝不是搬迁成功证据。
- axios 与 250/600ms 短退避消费 AbortSignal；关键词 debounce 250ms；主跟随在前次请求结束后 3s 再调度，位置凭据仍每 30s 独立查询。禁用跟随时精确取消该 scope/queryHash 且无其它 active observer 的在途 Search/Stats/Facets，真实 signal/XHR abort 已验证，不误取消独立 scope 或经典查询。上述数值是当前实现参数，不是时延/吞吐达标结论。

## 1. 背景与目标

将新查询覆盖与失败状态完整呈现给用户。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：UI 展示 FR-473 的 `complete`、`partial_reasons`、`duplicate_quality`、`stats_quality` 和 `enumeration_state`；`nextCursor=null` 不单独代表全量历史结束。
- 范围内：列表、Stats、Facets、Tail、Export 均消费 coverage；区分 partial、offline、gap、truncated、backlog、rehydrate failed、Legacy；保留 FR-419 回溯锚定、跳启动、跳时间和 view stale 重开。
- 继承 FR-473：`nextCursor=null` 仅表示当前 view 可枚举结束；`partial`、`DUPLICATE_UNRESOLVED`、`VIEW_STALE` 和来源分界必须显式展示。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

列表、Stats、Facets、Tail、Export 均消费 coverage；区分 partial、offline、gap、truncated、backlog、rehydrate failed、Legacy；保留 FR-419 回溯锚定、跳启动、跳时间和 view stale 重开。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- [ ] 失败不得显示为空成功；导出只有校验后可下载且下载前复核权限；文件历史/实时来源分界可见；i18n 和真浏览器验收。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-473 已冻结；本规格仍须完成真实联邦覆盖、失败态和浏览器验收，未完成前保持开发中。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-476 资产审批冻结。

## 3.1 用户可见失败态

- partial 必须展示实际查询的节点、层级、缺口原因和重试/恢复动作；offline、not-ready、archive-missing、采集 backlog、rehydrate failed、truncated 分开呈现。
- Legacy 数据必须有来源标识和时间覆盖；`nextCursor=null` 只表示当前 view 枚举结束，不表示所有历史完整。
- Export 失败、权限撤销、VIEW_STALE 或覆盖不完整时不得下载成功附件；完成且校验通过的产物不因源 Worker 后续离线而改成失败。
- 页面保持发布必需的真实浏览器验收：列表、Stats、Facets、Tail、Export 对同一 view 的集合和 coverage 一致，失败态提供可操作引导。
