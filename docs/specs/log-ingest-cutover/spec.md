# 功能规格：日志入库切换与 Legacy 只读（FR-480）

> 状态：实现中（地基：`LogCutover`+`LegacyLogReader`+DualPath；**HTTP 管理面已注册** GET/PUT `/api/v1/logs/cutover`、GET `/api/v1/logs/legacy`；默认关闭）　·　关联 PRD：FR-480　·　依赖：FR-478/440

## 1. 背景与目标

逐 Worker 从 CP 全量日志入库切换到新路径，保留可解释 Legacy。

本规格不得重新定义 FR-472 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：切换后的 Legacy、导出和控制台接缝语义继承 FR-472；未到期 Legacy 不受 platform 容量淘汰挤出。
- 范围内：切换前能力确认、watermark 和容量预检；未到期 Legacy 独立预算且排除平台容量淘汰；LegacyReader 只读当前 DB 集合，既有 NDJSON 明确不纳入本批集合；控制台订阅与持久化分离。
- 继承 FR-472：Legacy 未到期数据不受 platform 容量淘汰挤出；已有 CP NDJSON 不纳入本批可查集合并返回覆盖边界。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

切换前能力确认、watermark 和容量预检；未到期 Legacy 独立预算且排除平台容量淘汰；LegacyReader 只读当前 DB 集合，既有 NDJSON 明确不纳入本批集合；控制台订阅与持久化分离。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-472 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-472 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-482 文档对账。

## 5. 验收标准

- [ ] 切换崩溃可重试且不双写；Worker/Node 日志停止新入 CP；跨切换查询显示来源和覆盖；告警行为回归通过。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-472 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-472 已冻结；本规格仍须完成切换协议、Legacy 隔离和对应真机验收，未完成前保持开发中。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-475 资产审批冻结。

## 3.1 切换协议

- LegacyLogReader 只读访问切换水位之前仍在 CP `logs` 表中的实例/Worker/Node 存量；UI 必须标 Legacy、来源和可查覆盖窗。已转 NDJSON 的历史不自动进入本批查询。
- 每个 Worker 先完成能力确认、切换 watermark、Legacy 保留预算预检和原子路由切换；失败则保持旧路径并报告阻断原因，不静默丢历史。
- 切换后实例日志与 Worker/Node 自身日志都停止新入 CP；实时控制台订阅和持久化查询推送是两条路径。
- 旧日志按行、新日志按 Multiline 事件时，未建立等价映射前禁止拼接精确趋势；只展示带来源的近似或分段统计。
