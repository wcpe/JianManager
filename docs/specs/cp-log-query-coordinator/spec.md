# 功能规格：CP 跨 Worker 日志查询协调器（FR-479）

> 状态：实现中（logcoord + 联邦 HTTP + **生产装配**：CP `main` 经 `logcoord.Assemble(pool, nodeSvc, instanceSvc)` 注入；TunnelDialer=`PoolClientFactory`；`inst:`/`node:` 目标解析与真实 Readiness（状态∩隧道）已落地。`*` 全量/历史持有者目录与远程真机联邦仍待验收）　·　关联 PRD：FR-479　·　依赖：FR-478

## 1. 背景与目标

跨节点汇总全部授权目标并保留覆盖完整性。

本规格不得重新定义 FR-472 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：跨 Worker view 使用各目标 `PublishedProjection` 与 `closed_visible_seq` 向量；Export 与 Search 复用同一 view，不重新选择最新集合。
- 范围内：先解析授权目标集合，再经反向隧道创建各 Worker Query View；Search 稳定 K-way merge；Stats 二次聚合；Facet 截断有语义；Export 使用同 view 有界物化和完整校验。
- 继承 FR-472：跨 Worker view 是各目标 `closed_visible_seq` 向量，不承诺全局同一物理时刻；Export 与 Search 复用同一逻辑事件集合和 view。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

先解析授权目标集合，再经反向隧道创建各 Worker Query View；Search 稳定 K-way merge；Stats 二次聚合；Facet 截断有语义；Export 使用同 view 有界物化和完整校验。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-472 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-472 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-482 文档对账。

## 5. 验收标准

- [ ] 离线/未就绪/历史持有者明确出现在 coverage；仅在线为显式选项；列表与导出同快照一致；权限、取消、扇出和流量预算有效。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-472 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-472 已冻结；本规格仍须在 VL RangeClient、`*` 全量/历史持有者目录和远程真机联邦通过前保持“实现中”，不得标已交付。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-475 资产审批冻结。

## 3.1 联邦目标与完整性

- CP 先计算全部授权目标和历史数据持有者，再创建各 Worker Query View；“仅在线”只能由用户显式选择，不能默认当作完整结果。
- 汇总必须区分 `success/offline/not_ready/archive_missing/partial/stale`，覆盖摘要随列表、Stats、Facets 和 Export 返回。
- 实例换过节点时按 Catalog/历史持有者纳入查询；Search 使用稳定复合排序 K-way merge，Stats 使用声明口径的二次聚合。
- Facets 对受控维度精确合并；高基数返回截断维度、截断数和 coverage。`limit` 只限制结果数，扇出、字节、CPU、超时和并发预算另行执行。
- Export 复用同一 view 和逻辑事件集合，完整物化并校验后才生成下载产物；源 Worker 后续离线不改变已完成产物。
