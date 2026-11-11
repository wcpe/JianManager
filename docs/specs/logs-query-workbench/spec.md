# 功能规格：实例日志检索分析与字段探索（FR-557）

- 状态：规划草案
- 关联 PRD：FR-557
- 依赖：既有日志联邦 Query View、Search、Stats、Facets、Fields、Streams 与 stream-field 查询链路

## 1. 目标

把 VictoriaLogs 已存在的只读检索能力接入日志中心：在同一授权范围、同一时间范围和同一 Query View 下，同时展示日志列表、全窗口时间分布、级别/来源聚合、字段与流探索，并允许从字段/流追加筛选。

## 2. 数据边界

- 仅实例日志使用本规格的 VL 能力。
- 节点日志和平台日志仍是普通文本文件尾部窗口，不参与 VL 字段统计、全量总数或历史分页。
- 浏览器不直连 Worker 或 VictoriaLogs；所有请求经过 Control Plane 授权与反向隧道。
- 列表、统计、字段、流和导出必须绑定同一 Query View 身份；View 失效时显式提示并重新查询，不静默复用旧结果。

## 3. 功能范围

1. 全时间窗日志量：使用既有 Stats `timeBucket`，按时间范围自动选择有限桶数，显示 count，并保留 partial/truncated/unsupported 状态。
2. 聚合分析：级别、来源、流和受支持的数值字段聚合；字段不支持数值聚合时显示原因。
3. Fields：消费 `field_names` 和字段值查询，展示字段名、常见值和追加筛选入口。
4. Streams：展示 `log_source_id`、stream、命中数及完整性；`complete=false` 时不得把 hits 当作精确值。
5. Stream fields：消费流字段名和值，用于来源/流的细化筛选。
6. 日志详情：展开单条日志时显示结构化字段、原始消息、来源名称、流和时间。

## 4. 验收标准

- 列表与时间图的 `from/to`、授权目标、过滤条件和 `viewId` 完全一致。
- 切换字段/流筛选会生成新的筛选身份；旧查询结果不可冒充新筛选结果。
- 全窗口时间图不使用当前页时间戳伪造；无时序结果或聚合失败时显示明确状态。
- 真实 VictoriaLogs v1.52.0 环境完成 Search、Stats time bucket、Fields、Facets、Streams、stream-field、分页和导出回归。
- 非平台管理员的授权范围、节点查询拒绝、空授权拒绝和跨主体 View 复用保护保持有效。
- 查询超时、预算超限、429、字段不存在、Worker 不支持和 VL 不可用分别有可行动提示，不返回空成功。

## 5. 非目标

本规格不引入日志删除、retention 管理、租户管理、保存搜索会话、仪表盘编排或 AI 日志分析。
