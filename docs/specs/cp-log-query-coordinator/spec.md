# 功能规格：CP 跨 Worker 日志查询协调器（FR-480）

> 状态：实现中（logcoord + 联邦 HTTP + **生产装配**：CP `main` 经 `logcoord.Assemble(pool, nodeSvc, instanceSvc)` 注入；TunnelDialer=`PoolClientFactory`；`inst:`/`node:` 目标解析与真实 Readiness（状态∩隧道）已落地。`*` 全量/历史持有者目录与远程真机联邦仍待验收）　·　关联 PRD：FR-480　·　依赖：FR-479

## 1. 背景与目标

跨节点汇总全部授权目标并保留覆盖完整性。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：跨 Worker view 使用各目标 `PublishedProjection` 与 `closed_visible_seq` 向量；Export 与 Search 复用同一 view，不重新选择最新集合。
- 范围内：先解析授权目标集合，再经反向隧道创建各 Worker Query View；Search 稳定 K-way merge；Stats 二次聚合；Facet 截断有语义；Export 使用同 view 有界物化和完整校验。
- 继承 FR-473：跨 Worker view 是各目标 `closed_visible_seq` 向量，不承诺全局同一物理时刻；Export 与 Search 复用同一逻辑事件集合和 view。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

先解析授权目标集合，再经反向隧道创建各 Worker Query View；Search 稳定 K-way merge；Stats 二次聚合；Facet 截断有语义；Export 使用同 view 有界物化和完整校验。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- [ ] 离线/未就绪/历史持有者明确出现在 coverage；仅在线为显式选项；列表与导出同快照一致；权限、取消、扇出和流量预算有效。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-473 已冻结；本规格仍须在 VL RangeClient、`*` 全量/历史持有者目录和远程真机联邦通过前保持“实现中”，不得标已交付。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-476 资产审批冻结。

## 3.1 联邦目标与完整性

- CP 先计算全部授权目标和历史数据持有者，再创建各 Worker Query View；“仅在线”只能由用户显式选择，不能默认当作完整结果。
- 汇总必须区分 `success/offline/not_ready/archive_missing/partial/stale`，覆盖摘要随列表、Stats、Facets 和 Export 返回。
- 实例换过节点时按 Catalog/历史持有者纳入查询；Search 使用稳定复合排序 K-way merge，Stats 使用声明口径的二次聚合。
- Facets 对受控维度精确合并；高基数返回截断维度、截断数和 coverage。`limit` 只限制结果数，扇出、字节、CPU、超时和并发预算另行执行。
- Export 复用同一 view 和逻辑事件集合，完整物化并校验后才生成下载产物；源 Worker 后续离线不改变已完成产物。
- Search/Stats 顶层 coverage 与 Worker 顶层 partial 原因同样参与聚合；cut/open 用 OR，不能被某个目标的成功或已结束覆盖。预算排除目标必须显式 `BUDGET_EXCEEDED`，Stats 保持 partial，不能呈现完整零统计。
- `max_bytes` 是整个联邦请求的预算（零值入口默认 32 MiB），读取前公平预留给参与 Worker；子 RPC 必须得到正数额度且总预留不超过总量，零份额不拨号、不创建 view、不查询。扇出上限、最多 8 个在途与请求取消/超时共同约束调度；缓存身份包含主体、权限范围与预算，不能跨授权主体复用。
- Search 普通、快速与缓存响应可选保留 `ingest_position_gaps[]`，HTTP 门面原样透传，即使事件列表为空也不能丢弃。字段/兼容口径见 [API.md](../../API.md) 对应日志平台章节；位置凭据独立展示，不污染 coverage/quality/Export。
- Worker 扫描截断或未完整同时间戳边界组时保守不出全局 cursor，`exhausted=false`；正常 limit 分页继续使用原 view 与复合 cursor。预算部分结果与可安全分页不是同一概念。

## 3.2 覆盖率原因码：`ENGINE_NOT_READY` 与 `TARGET_NO_DATA` 的区分（2026-10-19）

**背景（真机现场）**：dev 节点 VL 端口在听、`vlReady=true`、直查 VL 有数据，而日志页显示
「日志查询服务未启用」。根因是 `target_missing_in_worker_response` 被与 `NOT_READY` /
`EMPTY_WORKER_RESPONSE` 一起归一化成 `ENGINE_NOT_READY`，把「该目标没有日志」说成了
「查询引擎未就绪」，把排查引向 VL。

**判定口径**（`internal/controlplane/logcoord/targets.go`）：

| 情形 | 目标 state | 原因码 | 对 `complete` 的影响 |
|---|---|---|---|
| 响应**非空**、但缺该目标 | `success` | 无（该目标无数据，合法终态） | 不影响 |
| 响应**本身为空** | `not_ready` | `TARGET_NO_DATA` | `complete=false`（不静默放行导出） |

**为什么空响应不能判 success**：Worker 在「没有任何匹配分区」与「有分区却枚举不出来」两种
情形下都返回空 targets（`planner.selectKeys` 为空 ⇒ `coverage.targets` 亦为空，且
`WorkerSearchResponse.Coverage` 只是 `Targets` 的另一投影、同为空），现有 wire 无法区分。
判 success 会把后一类故障静默成「无数据 + complete=true + 可导出」。

**为什么原因码不能沿用 ENGINE_NOT_READY**：前端 `stateFromReason` 会把
`ENGINE_NOT_READY` 渲染成「日志查询服务未启用」，而复用该码正是本次误导出错的来源。
`TARGET_NO_DATA` 走 partial 分支，文案为「该范围没有日志数据」。

**真正的引擎未就绪信号**（仍归 `ENGINE_NOT_READY`，语义不得丢失）：
`dial_failed` / `search_failed` / `worker_view_failed` / `log_rpc_unsupported` /
`empty_worker_response`（`resp == nil`）。

**范围说明（本节为事后追认）**：该修正属 FR-502/503/504 之外的 CP 侧语义修订，
由用户明确要求后实施；在此登记以避免后续追溯时误认为它属于上述任一 FR 的既定范围。
