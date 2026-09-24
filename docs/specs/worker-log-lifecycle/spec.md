# 功能规格：分区 Catalog 与冷热 Lifecycle（FR-476）

> 状态：实现中（地基包 `internal/worker/logs/{catalog,lifecycle}` 已落地，单测绿；**Runbook B 真机迁移链已通过**——FROZEN→…→CLEANED 全链、数据物理迁移、HOT 空/COLD 持有、逐状态崩溃恢复视图，见 `../worker-log-platform-contract/acceptance-record.md` §2）　·　关联 PRD：FR-476　·　依赖：FR-472/436

## 1. 背景与目标

让迁移、迟到写入和重启恢复只有一个查询 owner。

本规格不得重新定义 FR-472 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：Catalog 发布必须携带 `PublishedProjection`；旧目录自动 attach 不得越过 Catalog，纯迁移不自动使仍可读的旧 view 失效。
- 范围内：实现 Catalog/journal 状态机：冻结路由、排空、snapshot、staging 校验、attach 排除、原子切 owner、租约排空、detach、清理；启动先恢复 Catalog，再处理 VL 残留目录。
- 继承 FR-472：租约包含 `lease_id/view_id/generation/expiry`；纯迁移不自动使仍可读的固定 view 失效，Catalog 冲突范围才进入 `VIEW_STALE`/`RECOVERY_REQUIRED`。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

实现 Catalog/journal 状态机：冻结路由、排空、snapshot、staging 校验、attach 排除、原子切 owner、租约排空、detach、清理；启动先恢复 Catalog，再处理 VL 残留目录。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-472 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-472 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-482 文档对账。

## 5. 验收标准

- [ ] 每个崩溃点验证 owner/generation、物理目录、写入路由和查询范围；残留旧目录不被查询；retention 不删除最后有效副本。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-472 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-472 已冻结；本规格仍须完成 Catalog 适配层崩溃恢复、re-attach 过滤。**Runbook B 真机验收已完成**（见状态行与验收记录 §2）；re-attach 过滤与迁移崩溃的具体覆盖度见 `signoff-ledger.md` 的 FR-476 行。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-475 资产审批冻结。

## 3.1 迁移不变量

状态转换固定为：`ROUTING_FROZEN → DRAINING → SNAPSHOTTING → STAGING_VERIFY → ATTACHED_STAGING → OWNER_SWITCHED → QUERY_LEASE_DRAINING → DETACHED → CLEANED`。

- 冻结日期写入路由并排空后才 snapshot；staging attach 成功也不得进入 QueryPlanner。
- `OWNER_SWITCHED` 原子提交 Catalog owner/generation、PublishedProjection 和写入路由；每个查询只选择一个不重叠权威副本。
- 旧查询租约结束后才 detach；detach 成功不等于迁移完成，残留目录重启自动 attach 时仍由 Catalog 排除。
- 迟到事件按 Catalog owner/generation 写 COLD、迁移暂存或受控补录，禁止按 `now-7d` 重新生成 HOT 权威分区。
- `move_after_age`、产品 `online_retention`、VL runtime retention 分离；下一层未完成责任接收和校验前，不得删除最后有效副本。

## 3.2 崩溃验收

每个状态中断都记录 owner/generation、物理目录、写入路由、查询范围和恢复动作；VL 自动 re-attach 不能绕过 Catalog。
