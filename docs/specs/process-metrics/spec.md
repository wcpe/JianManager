# 功能规格：进程粒度监控采集

> 状态：开发中　·　关联 PRD：FR-170　·　关联 ADR：ADR-060

## 1. 背景与目标

监控页需要定位单个实例内部或容器内的资源热点。当前平台只有节点级和实例级指标，无法展示进程 TOP10。FR-170 引入受管实例进程 TOPN 快照，供监控总览和实例详情 hover/面板排障。

## 2. 需求

- Worker 周期采集受管实例进程集合的 CPU、内存、IO。
- CP 接收并保存短期快照。
- 前端能展示每实例进程 TOP10。
- 缺测或平台不支持时显式降级。

范围外：

- 全机进程列表。
- kill/renice 等进程控制。
- 完整命令行、环境变量、敏感参数展示。
- 一年以上进程明细留存。

## 3. 设计

### 3.1 样本字段

```json
{
  "instanceId": 1,
  "instanceUuid": "uuid",
  "pid": 1234,
  "name": "java",
  "cpuPercent": 42.5,
  "rssBytes": 536870912,
  "readBytesPerSec": 1024,
  "writeBytesPerSec": 2048,
  "user": "minecraft",
  "commandSummary": "java -Xmx4G -jar server.jar ...",
  "sampledAt": "2026-07-06T12:00:00Z"
}
```

`commandSummary` 必须截断和脱敏，不保存完整命令行。

### 3.2 gRPC 草案

在 Heartbeat 负载中追加：

```proto
message ProcessMetricSample {
  string instance_uuid = 1;
  int32 pid = 2;
  string name = 3;
  double cpu_percent = 4;
  uint64 rss_bytes = 5;
  uint64 read_bytes_per_sec = 6;
  uint64 write_bytes_per_sec = 7;
  string user = 8;
  string command_summary = 9;
  int64 sampled_at_unix_ms = 10;
}
```

`sampled_at_unix_ms` 为 Worker 采样完成时的 Unix 毫秒时间；CP 可在字段缺失或为 0 时降级使用心跳接收时间，但不得把缺测伪造成 Worker 采样时间。

### 3.3 CP 存储草案

新增短期表：

```text
process_metric_snapshots:
  id
  node_uuid
  instance_uuid
  pid
  name
  cpu_percent
  rss_bytes
  read_bytes_per_sec
  write_bytes_per_sec
  user
  command_summary
  sampled_at
```

索引：`(instance_uuid, sampled_at)`、`(node_uuid, sampled_at)`。

TTL：默认 48 小时，后续可配置。

### 3.4 HTTP 契约草案

- `GET /api/v1/metrics/processes/top`
  - Query：`instanceId?`、`nodeId?`、`sort=cpu|memory|io`、`limit=10`；未传实例/节点时返回平台范围 TOPN。
  - 权限：`instance.read`，按实例访问范围过滤。
  - 响应：`[{ instanceId, pid, name, cpuPercent, rssBytes, readBytesPerSec, writeBytesPerSec, user, commandSummary, sampledAt }]`

## 4. 任务拆分

- [x] Worker 进程归属识别与采样单测。
- [x] proto 追加字段并重生成，覆盖 `sampled_at_unix_ms` 到 `sampledAt/sampled_at` 的映射。
- [x] CP 入库、TTL、查询服务与 router。
- [x] 前端监控面板（平台/节点/实例 TOPN、用户与 IO 已展示；hover/focus 明细浮层已落地，真浏览器截图见 `.tmp/evidence/fr170/monitoring-process-top.png` 与 `.tmp/evidence/fr170/monitoring-process-top-hover.png`）。
- [~] 文档同步：ARCHITECTURE、API、CHANGELOG（首批同步，真机验收后收口）。

## 5. 验收标准

- 单测覆盖命令摘要脱敏、TOPN 排序、缺测降级。
- 集测覆盖 Heartbeat 携带样本后 CP 可查询。
- 单机截图覆盖监控页 TOP10 hover（已生成 `monitoring-process-top-hover.png`）。
- 真机验收覆盖 daemon/direct/docker 至少一种受管实例进程采集。

## 6. 风险 / 待定

- Windows/Linux 进程 IO 字段可用性不同。
- docker 容器内进程映射可能受权限限制。
- TTL 是否 48 小时需审核。
