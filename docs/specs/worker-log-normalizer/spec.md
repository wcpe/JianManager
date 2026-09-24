# 功能规格：Worker 日志 Multiline 与归一化（FR-474）

> 状态：实现中（地基包 `internal/worker/logs/normalize` 已落地；**真机验收通过**——Multiline 5 行归并为 1 事件、跨午夜回拨、损坏编码完整性与半条事件恢复均以真 VL 核对，见 [`acceptance-real.md`](acceptance-real.md)）　·　关联 PRD：FR-474　·　依赖：FR-472/434

## 1. 背景与目标

将行级来源转换为可追溯事件，不混淆事件数与行数。

本规格不得重新定义 FR-472 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：事件必须保留完整 `record_start`/`record_end`，固定 `parser_version`；重新解析走受控 projection/dataset generation。
- 范围内：按 parser_version 执行异常头、堆栈、Caused by、超时和上限；事件保留原文行序列、event_id、解析状态和时区；半条在 WAL 恢复后续接。
- 继承 FR-472：事件保存完整 `record_start`/`record_end`，源分段绑定固定 `parser_version`；重新解析走 projection，不改变默认逻辑事件集合。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

按 parser_version 执行异常头、堆栈、Caused by、超时和上限；事件保留原文行序列、event_id、解析状态和时区；半条在 WAL 恢复后续接。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-472 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-472 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-482 文档对账。

## 5. 验收标准

- [x] 跨午夜/无时间/损坏编码/超长堆栈可恢复；解析失败保留原文；同一输入版本输出稳定；事件数和行数统计分别正确。（真机 + 单测双覆盖，见 `acceptance-real.md`）
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-472 冻结的实际数值，不自行发明未登记阈值。
- [x] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。（真机证据见 `acceptance-real.md`，与单测分列）

## 6. 风险 / 待定

- FR-472 已冻结。**Multiline 真实采集与恢复验收已完成**（见状态行与 [`acceptance-real.md`](acceptance-real.md)）；剩余 Windows 平台证据缺失。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-475 资产审批冻结。

## 3.1 事件边界与统计口径

- Multiline 上限、超时、半条事件、跨午夜和损坏编码都必须有明确状态；解析失败保留原文、源位置范围和 parser_version。
- 事件数与文本行数是两个独立指标：事件数用于 Search/Stats/Facets/Export，行数只用于原文展示、接缝和诊断；Dashboard 与切换前后趋势不得混用。
- 同一源分段绑定固定 parser_version；重新解析是新的 projection/dataset generation，不把新旧投影同时放入默认逻辑集合。

## 3.2 崩溃验收

- 异常头、堆栈和 Caused by 跨 WAL/批次边界强杀后，恢复出的事件保留完整首尾源位置和原文。
- 超过 multiline 上限或超时的半条事件进入显式 `TRUNCATED`/`TIMEOUT` 状态，不静默拼接下一事件。
