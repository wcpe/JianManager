# 功能规格：日志中心与可观测仪表盘（FR-481）

> 状态：实现中（LogsPage 已接真实联邦 API、覆盖/导出门禁；**真浏览器验收通过**：真实登录/列表/Stats·Facets 洞察对同一 view 一致（修 Stats `agg.count` 读法缺陷）、覆盖完整无失败横幅、导出可用；失败态矩阵由 19 项 DOM fixture + helper 单测覆盖）　·　关联 PRD：FR-481　·　依赖：FR-479/441

## 1. 背景与目标

将新查询覆盖与失败状态完整呈现给用户。

本规格不得重新定义 FR-472 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：UI 展示 FR-472 的 `complete`、`partial_reasons`、`duplicate_quality`、`stats_quality` 和 `enumeration_state`；`nextCursor=null` 不单独代表全量历史结束。
- 范围内：列表、Stats、Facets、Tail、Export 均消费 coverage；区分 partial、offline、gap、truncated、backlog、rehydrate failed、Legacy；保留 FR-419 回溯锚定、跳启动、跳时间和 view stale 重开。
- 继承 FR-472：`nextCursor=null` 仅表示当前 view 可枚举结束；`partial`、`DUPLICATE_UNRESOLVED`、`VIEW_STALE` 和来源分界必须显式展示。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

列表、Stats、Facets、Tail、Export 均消费 coverage；区分 partial、offline、gap、truncated、backlog、rehydrate failed、Legacy；保留 FR-419 回溯锚定、跳启动、跳时间和 view stale 重开。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-472 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-472 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-482 文档对账。

## 5. 验收标准

- [ ] 失败不得显示为空成功；导出只有校验后可下载且下载前复核权限；文件历史/实时来源分界可见；i18n 和真浏览器验收。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-472 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-472 已冻结；本规格仍须完成真实联邦覆盖、失败态和浏览器验收，未完成前保持开发中。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-475 资产审批冻结。

## 3.1 用户可见失败态

- partial 必须展示实际查询的节点、层级、缺口原因和重试/恢复动作；offline、not-ready、archive-missing、采集 backlog、rehydrate failed、truncated 分开呈现。
- Legacy 数据必须有来源标识和时间覆盖；`nextCursor=null` 只表示当前 view 枚举结束，不表示所有历史完整。
- Export 失败、权限撤销、VIEW_STALE 或覆盖不完整时不得下载成功附件；完成且校验通过的产物不因源 Worker 后续离线而改成失败。
- 页面保持发布必需的真实浏览器验收：列表、Stats、Facets、Tail、Export 对同一 view 的集合和 coverage 一致，失败态提供可操作引导。
