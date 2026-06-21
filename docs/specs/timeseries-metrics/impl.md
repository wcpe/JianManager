# 实施计划 — FR-060 时序监控与历史曲线

> 关联 FR: FR-060 | 优先级: P1 | 状态: 🔨 in-progress（后端采集/存储/聚合/API 完成，前端待 FR-061） | 关联 ADR: ADR-013（分级降采样）；ADR-014（ServerProbe 实例指标源）

## 背景

FR-010 只存当前值。指标已在流动：节点经心跳、实例富指标经 ServerProbe（`ScrapeServerProbe`→`ProbeSnapshot`：TPS/MSPT/堆/线程/CPU/uptime/分世界）。本 FR 把它们沉淀为时序，CP 端分级降采样（ADR-013），前端三级历史曲线。Worker 不碰 DB——经心跳上报、CP 落库。深度/分世界指标 ServerProbe 现成提供，无需插件桥（FR-103 已退役）。

## 设计要点
- **节点采集**：沿用 `internal/worker/metrics/collector.go` 心跳指标（CPU/内存/磁盘/网络累计字节）；CP 据相邻累计字节算网络速率。
- **实例采集**：Worker 心跳 tick 对每个 RUNNING 实例 `ScrapeServerProbe(localhost, ProbePort, token)`，探针不可用回退 RCON（`metrics/rcon.go`，仅 TPS/在线），缺测写 NULL。
- **传输**：扩展 `HeartbeatRequest` 追加 `repeated InstanceMetricSample`（含分世界 `WorldSample`），向后兼容。
- **存储**：`metric_series` 维度表 + 三档样本/卷积窄表，`(series_id, ts)` 索引。入库 = upsert series + 写 raw。
- **聚合/清理**：复用 scheduler——每 5min 卷 raw→5m、每 1h 卷 5m→1h、每小时按 TTL 清理（raw 48h/5m 30d/1h ≥400d）。SQLite/MySQL 同构。
- **查询**：`/metrics/series` 按 range 自动选档；`/metrics/overview` 跨节点聚合 + 迷你趋势。

## 任务拆解

### 数据模型与迁移
- [x] `internal/controlplane/model/metric.go`：`MetricSeries`+`MetricSampleRaw`+`MetricRollup5m`+`MetricRollup1h` + scope/metric_key 常量；唯一索引。
- [x] `database.AutoMigrate` 注册四模型。

### Worker 采集
- [x] `internal/worker/heartbeat/heartbeat.go` `collectInstanceMetrics`：心跳路径对 RUNNING 且 `ProbePort>0` 的实例并发 `ScrapeServerProbe`（上限 8、5s 超时），汇成每实例快照；抓取失败置 `probe_available=false`。
- [x] 心跳发送处装入 `instance_metrics`（节点指标沿用既有字段；网络累计字节已在心跳）。
- [x] ProbePort 喂入 Worker：`CreateInstanceRequest.probe_port` → `Manager.Create` 持有 → daemon `WrapperConfig`→PID 记录持久化 → `RecoverDaemonInstances` 重启恢复；`InstanceSnapshot.ProbePort` 供采集器读取。

### proto
- [x] `proto/worker.proto`：`InstanceMetricSample`(+复用 `WorldMetric`)+`HeartbeatRequest.instance_metrics=10`；`CreateInstanceRequest.probe_port=12`；重生成 `workerpb`。

### CP 入库 + 聚合
- [x] `internal/controlplane/service/metric.go`：`Ingest`（节点 + 每实例 + 分世界 → upsert series + 写 raw）、`IngestHeartbeat`（心跳负载→样本，网络速率据相邻累计算、回绕跳过、探针不可用写 NULL）、`QuerySeries`（选档取数）、`Overview`/`overviewAt`（跨节点聚合）。
- [x] `internal/controlplane/grpc/handler.go`：`Heartbeat` 经 `MetricIngester` 接口调 `IngestHeartbeat`（避免 grpc→service 循环依赖）；`main.go` 注入。
- [x] 5m/1h 聚合 + TTL 清理并入 `MetricService.Start()` 后台循环（既有，复用）。

### 路由 `/metrics`
- [x] `internal/controlplane/router/metric.go`：`GET /metrics/series`（既有）、`GET /metrics/overview`（RBAC 读校验）；`router.go` 注册、`Services.Metric` 接线（既有）。

### 前端（**延后到 FR-061**，依赖其 `TimeSeriesChart`/`RangePicker`）
- [ ] `web/src/api/metrics.ts`：`useMetricSeries`、`useMetricOverview`。
- [ ] `OverviewPage`（聚合曲线）、节点详情（节点曲线 + 其上实例对比）、实例详情（实例曲线 + 分世界曲线）。
- [ ] `web/src/i18n/{zh,en}.json`：`metrics.*` 键。

### 测试
- [x] `metric_test.go` + `metric_heartbeat_test.go`（service）：ingest/upsert、5m/1h 聚合、TTL 清理、自动选档、缺测(NULL)、心跳入库节点/实例/分世界、网络速率推导与回绕、overview 总量+聚合曲线。
- [x] `metric_test.go`+`metric_overview_test.go`（router）：series happy/越权(403)/非法参数(400)；overview happy/非法 range/非法 resolution。
- [x] `heartbeat_test.go`（worker）：`collectInstanceMetrics` 仅采 RUNNING+探针、抓取失败标记不可用、无目标返回 nil。
- [x] `pid_file_test.go`（worker daemon）：PID 记录含 `ProbePort` 往返。

## 验证
- [x] `go build ./...`、`go vet ./...` 通过
- [x] `go test ./internal/controlplane/... ./internal/worker/...` 通过
- [ ] `cd web && npx tsc --noEmit && npm run lint && npm run build`（前端随 FR-061 实施）
- [ ] 真机：真 Paper + 探针，CP 连续运行验证历史曲线累积、CP 重启后历史不丢、48h/30d/1y 过期清理（待用户真机验收）

## 实现说明（与 api.md 的偏差）
- `GET /metrics/overview` 实际响应为 `{ totals:{nodeCount,onlineNodeCount,runningInstances,cpuPct,memUsedBytes,memTotalBytes,onlinePlayers}, resolution, trends:[{metricKey,unit,points}] }`（嵌套 totals + 聚合曲线数组），较 api.md 初稿更规整；`alertsActive` 未纳入（属 FR-011 告警域，避免跨域耦合）。api.md 已同步为此形状。
- 节点 `mem_total/disk_total` 不逐拍入时序（近静态），总览容量取 Node 表当前值；时序仅留 `*_used` 与 CPU/网络速率。

## 依赖 / 顺序
- **后端独立可先行**（采集/存储/聚合/API 与样式无关）。
- **前端图表依赖 FR-061** 的 `TimeSeriesChart`/`RangePicker`——前端排在 FR-061 Phase 2 之后。

## 不做（范围外，见 scope-discipline）
- 不自采 ServerProbe 之外的深度指标；不复活插件桥（FR-103 已退役）。
- 告警与时序联动（沿用 FR-011 实时阈值）。
- 指标导出/下载、外部 TSDB 后端。
- 不用 ServerProbe 自身本地历史文件作源（分散、无法跨节点聚合，见 ADR-013 替代方案）。

## 开放问题
- 网络速率在 CP 端据相邻心跳累计字节算（首拍无速率；节点重启计数器回绕需钳制）。
- 大量实例时单次心跳负载偏大、每 tick 多实例并发抓探针的开销；本期不优化（记 backlog）。
