# 功能规格：Deep Archive 与 Rehydrate（FR-478）

> 状态：实现中（Remote S3 Provider、Worker Archive Registry/Rehydrate RPC 已接线；**真机 RustFS S3 验收通过**：受管 Raw Put/校验/幂等登记、manifest 封存、对象往返、Rehydrate 任务租约/同 mergeKey 复用/磁盘预留拒绝/完成；manifest engine 记录真实 VL build_id；发布 generation 清理矩阵待验）　·　关联 PRD：FR-478　·　依赖：FR-477

## 1. 背景与目标

以可校验 Raw/VL manifest 支持历史恢复。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：归档/恢复分段释放理由、责任接收者和 hold 规则继承 FR-473；本规格不把 manifest-only 当作可清理证明。
- 范围内：按 storage_namespace+UTC 日+generation 归档；Raw 复制/校验/登记；Rehydrate 任务有租约、合并、取消、超时、磁盘预留和临时 retention；恢复不改变原始 _time。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

按 storage_namespace+UTC 日+generation 归档；Raw 复制/校验/登记；Rehydrate 任务有租约、合并、取消、超时、磁盘预留和临时 retention；恢复不改变原始 _time。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- [ ] 重复恢复幂等；对象损坏/上传中断可重试；恢复超过默认 retention 可查；临时实例和产物按 generation 隔离清理。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-473 已冻结；本规格仍须完成发布 generation 清理矩阵和发布级验收，未完成前保持开发中。真机 RustFS S3 验收（登记/幂等/manifest/往返/Rehydrate 任务）已通过，证据见 `.tmp/fr433-experiments/D-deep/`。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-476 资产审批冻结。

## 3.1 Archive 与 Rehydrate 契约

- 受管 Raw Archive 是复制、校验、manifest 登记成功的恢复来源，不代表实例目录仍保留 `.gz`；STDIO_PRIMARY 必须由 Worker 生成受管 Raw 分段。
- VL 分区键为 `storage_namespace + UTC 日 + generation`，不是每实例每天一个天然分区；manifest 固定 schema、parser、engine、generation、内容校验和源范围。
- Rehydrate 任务必须有 `task_id`、租约、并发合并键、取消、超时、磁盘预留、临时 VL retention 覆盖原始 `_time` 和 generation 隔离；重复请求复用在途任务。
- 恢复完成前原始事件的 `_time`、event_id、ingest_seq 不变；临时分区清理不得删除仍被 Query View 或恢复任务持有的副本。

## 3.2 发布验收

对象上传中断、manifest 损坏、恢复超过默认 retention、同一归档并发恢复和任务超时均返回结构化状态；成功恢复必须可定位、可查询、可重复核验。
