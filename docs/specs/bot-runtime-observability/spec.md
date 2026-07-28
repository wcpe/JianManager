# 功能规格：Bot Worker 历史观测与聚合统计

> 状态：开发中　·　关联 PRD：FR-401　·　优先级：P1　·　依赖：FR-400　·　下游：FR-402

## 1. 背景与目标

FR-400 提供 Worker/Bot Worker 的唯一实时快照，但没有长期趋势。FR-399 的 `bot_load_metric_samples` 只在活跃压测会话期间写入，不能作为全局历史真源。

本 FR 将 FR-400 当前快照以 30 秒节奏沉淀到 ADR-013 时序存储，提供节点、目标实例和压测会话关联的共享 Bot Worker 资源趋势。共享资源只按真实进程与聚合 Bot 数表达，绝不推导单 Bot 占用。

## 2. 需求（要什么）

### 范围内

1. 常驻记录每节点 Bot Worker RSS/CPU、Go Worker RSS/CPU、活跃数、连接中数、事件循环 P95、容量上限与可用状态；不可用形成时序断点，不写零。
2. 支持按执行节点、目标实例、压测会话和任意时间范围查询。目标实例与会话仅用于解析其所属节点/执行节点集合，结果明确为“共享运行时观察值”，不把共享资源归因给单个会话或 Bot。
3. 复用 ADR-013 留存：raw 约 48 小时、5 分钟约 30 天、1 小时约 400 天；长范围自动选档，也允许显式档位。
4. 在节点监控页新增 Bot Worker 图卡；展示断点、不可用原因和共享资源说明。

### 不做

- 不新增单 Bot RSS、CPU、均摊内存或任意进程采集。
- 不新增 Worker 入站端口、CP 直拨或 Worker 直连数据库。
- 不把 FR-399 会话样本迁移、复制或改写为全局历史真源。
- 不在本 FR 做平台跨节点总览（FR-402）。

## 3. 设计（怎么做）

### 3.1 历史真源

CP 新增 `BotRuntimeMetricSampler`，每 30 秒读取 FR-400 已持久化的节点当前快照并调用 `MetricService.Ingest`。它不调用 RPC、不触发 Bot Worker，且单节点缺值不阻塞其他节点。新增 node scope 指标键：

- `bot_worker_rss_bytes`、`worker_process_rss_bytes`（bytes）
- `bot_worker_cpu_pct`、`worker_process_cpu_pct`（percent）
- `bot_active_count`、`bot_connecting_count`、`bot_capacity_max`（count）
- `bot_event_loop_p95_ms`（ms）

快照不可用、节点离线或超过 90 秒时不写数值样本；查询自然得到断点。状态/不可用原因来自 FR-400 当前快照，作为响应元数据而不是用数值伪造。

### 3.2 关联查询

新增 `/metrics/bot-runtime` 查询面，底层复用 node scope 的 ADR-013 序列：

- `nodeId`：返回该节点的共享运行时序列；
- `instanceId`：解析实例所属节点后查询该节点；
- `sessionId`：解析压测会话持久化的 executor 节点集合及目标实例，按节点返回并聚合；
- 不传关联条件：仅平台管理员可查询全平台节点聚合，供 FR-402 使用。

会话关联是筛选关联而非资源所有权；响应固定带 `sharedRuntime=true` 与说明。与现有 `MetricSeries` 保持 node scope，不新增 session 维度表或第二套降采样表。

### 3.3 前端

节点监控页追加“Bot Worker（共享）”图卡：Bot Worker/Go Worker 的 RSS 与 CPU、活跃/连接中数、事件循环 P95、容量上限。首帧 CPU 与空样本显示断线，不画零；只在平台管理员或可访问该节点的上下文展示，沿用节点指标权限。

## 4. 任务拆分

- [x] 为 sampler 正常、缺失、陈旧、多节点隔离、Start/Stop 幂等写红测。
- [x] 新增指标键与 30 秒 CP sampler，接入主程序生命周期。
- [x] 实现关联查询、档位选择、共享运行时声明与权限收敛。
- [x] 增加节点监控图卡、空态和中英文文案。
- [x] 更新 API、ARCHITECTURE、PRD、CHANGELOG；回归 ADR-013 卷积与 TTL。

## 5. 验收标准

1. 可用快照在 30 秒内形成 node scope 样本；Bot Worker/Go Worker CPU 由真实进程 CPU 时间差计算，首帧、PID 更换和无 Bot Worker 均形成明确断点而非 0。
2. raw/5m/1h 查询遵循 ADR-013 的留存和自动分辨率选择，长范围不读取全量 raw。
3. 节点、实例、会话筛选只返回关联节点；会话响应明确共享语义，绝无单 Bot RSS 字段。
4. 非管理员不能查询全平台聚合；节点级查询继续受既有资源可访问权限约束。
5. 节点监控页正确绘制可用数据与断线，未新增依赖。
6. 服务、路由、前端、降采样回归测试通过；真 CP+Worker+Bot Worker 环境中创建/停止 Bot 后曲线变化、断隧道后曲线断开，仍需用户确认。

## 6. 风险 / 待定

- Bot Worker 与 Go Worker CPU 必须来自受管 PID 的真实 CPU 时间差；不得以 RSS、事件循环 P95 或节点 CPU 代替。
- 会话期间节点上的共享 Bot Worker 可能服务其他会话，API 与 UI 必须持续标注共享语义。
- FR-400 的快照字段是本 FR 唯一输入契约；若字段变更，必须先修订 FR-400 spec 与 API 契约。
